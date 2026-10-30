# Crane bootstrap flow

This document describes the bootstrap behavior implemented for fresh Crane
deployments. Existing deployments are intentionally out of scope; this is not a
migration or adoption workflow.

There are two related workflows:

1. The initial deployment bootstrap, which creates the shared layer-zero
   services and the first Kubernetes cluster.
2. The durable `create_cluster` and `attach_zone` workflow, which is submitted
   to `crane-api` after layer zero is ready and executed by a bootstrap worker.

The second workflow reuses the first deployment's VPC, RDS instance, Crane API,
and SPIRE authority. It does not create another layer-zero deployment.

## Initial deployment

### 1. Prepare the private identity endpoint

The operator runs `crane-bootstrap -prepare-images` with the versioned provider
configuration. The AWS implementation reconciles the deployment VPC and creates
the private SPIRE Network Load Balancer, listener, and empty target group. It
persists the deployment configuration and the NLB DNS name in the local state
directory (`.crane-bootstrap/<deployment>`).

This step does not create Kubernetes, RDS, runtime credentials, or application
hosts. The NLB hostname, rather than an individual server IP, is the stable
identity endpoint used by deployment AMIs.

### 2. Build the deployment AMI

The golden-image flow installs the pinned Kubernetes, SPIRE, bootstrap-tool, and
Crane application versions. The prepared NLB hostname, trust domain, and public
bundle parameter are supplied to the image generator.

The image does not contain SVIDs, private keys, agent state, runtime database
credentials, or changing CA bundles. Agents retrieve their initial public bundle
from the deployment's IAM-protected SSM parameter when they start.

### 3. Validate local bootstrap state

The real run (`crane-bootstrap -bootstrap-config ...`) validates the version-3
configuration, pinned image references, deployment identity, and previously
prepared endpoint. Existing local state must belong to the same deployment and
must have compatible shared configuration. A changed identity endpoint or
incompatible configuration stops the run instead of creating a second
deployment.

### 4. Reconcile shared infrastructure and credentials

The provider creates or discovers the deployment runtime Secret in AWS Secrets
Manager and reconciles the deployment-owned VPC, subnets, security groups, IAM
roles, instance profiles, RDS, and SPIRE NLB. Ownership tags and deployment
identity make retries converge on existing resources.

The runtime Secret contains database credentials and Crane admin/adapter tokens.
Those values are not accepted in bootstrap request parameters, stored in
operation discovery fields, baked into AMIs, or printed in logs.

### 5. Create the protected SPIRE and platform hosts

Bootstrap launches the protected SPIRE host and protected platform host with the
pinned AMI. The SPIRE host runs the SPIRE server and publishes the trust bundle
through the protected path. The platform host becomes the initial Kubernetes
control-plane host and is recorded outside normal capacity-managed node groups.

The protected-host record prevents ordinary capacity reconciliation from
terminating or resizing these hosts as worker capacity.

### 6. Start SPIRE and register initial identities

The identity phase starts SPIRE and waits for health. It reconciles workload
entries for Crane API, the first Dominator, and the platform Subd agent. Entries
are bound to exact namespace, service-account, systemd, and AWS workload
selectors. Conflicting existing selectors fail the run rather than weakening
authorization.

### 7. Initialize Kubernetes and Cilium

The platform host starts its SPIRE agent and runs `kubeadm init` if the control
plane does not already exist. It uses the configured Kubernetes version, private
advertise address, pod CIDR, and containerd socket. Existing control planes are
reused; they are never reset by a retry.

Bootstrap installs the pinned Cilium chart and waits for the Cilium DaemonSet,
Cilium operator, and all Kubernetes nodes to become ready.

### 8. Initialize and seed Crane's database

The platform host initializes the canonical first-go PostgreSQL schema, then
renders the first-zone input with trusted values from Kubernetes: the private
API endpoint, CA hash, account, region, and Kubernetes versions.

The `-pending` database-only bootstrap seeds the first zone, cluster,
cluster-zone membership, node pool, and node group in initializing/pending
states. They are not made ready until the validation handoff succeeds.

### 9. Install Kubernetes attestation and observability

Bootstrap installs the Kubernetes RBAC needed by the SPIRE Kubernetes attestor
and Subd. Service-account token automount is disabled; workload identity comes
from SPIRE.

It also installs the internal OpenTelemetry Collector credentials, TLS material,
CA ConfigMap, and Collector deployment. Operator-owned backend exporter
configuration is separate. Missing exporter configuration leaves observability
pending without preventing core Crane API and Dominator startup.

### 10. Deploy Crane API, Dominator, and Subd

The runtime Secret is materialized as the `crane-system/crane-runtime` Secret.
Crane API and the first Dominator are deployed in `crane-system`, pinned to the
protected platform node, and connected to the SPIRE agent socket and private
Collector endpoint. Crane API listens on port `43060`; Dominator listens on
port `44000`.

The platform Subd agent uses its zone-specific SPIFFE identity and reports
heartbeats to the first Dominator.

### 11. Validate and commit readiness

The handoff validation checks SPIRE, trust-bundle delivery, Kubernetes API
health, node readiness, Cilium, application rollouts, database connectivity,
Subd behavior, Dominator behavior, SPIFFE identities, and private SPIFFE-mTLS.

Only after that validation succeeds does bootstrap atomically mark the first
cluster, zone, and membership ready, approve access, persist `result.json`, and
report the private Crane API endpoint. Observability status is reported
separately from core zone readiness.

## Durable `create_cluster` and `attach_zone`

After the initial deployment is ready, an administrator submits either operation
to Crane API:

```http
POST /v1/bootstrap
Authorization: Bearer <CRANE_ADMIN_TOKEN>
```

The operation status is read with:

```http
GET /v1/bootstrap/{operation-id}
Authorization: Bearer <CRANE_ADMIN_TOKEN>
```

The admin bearer token is used for both endpoints. The adapter token is not used
for bootstrap; adapter credentials are reserved for capacity admission and are
bound to a specific cluster identity.

### 1. Validate and serialize the request

The API accepts only `create_cluster` and `attach_zone`. Every request requires
an immutable request ID, cluster ID, zone ID, and JSON parameters containing
public configuration or opaque references.

Parameters are rejected if they contain password, secret, token, credential, or
private-key fields. Join tokens and credentials are never supplied by the
operator.

PostgreSQL advisory locking serializes operations for the same cluster. This
prevents concurrent requests from racing topology creation or zone attachment.

### 2. Apply idempotency and conflict checks

Replaying the same request ID with the same kind, cluster, zone, and parameters
returns the existing operation ID. Reusing that request ID with different intent
returns a replay mismatch.

An operation is rejected if another unfinished operation already targets the
same cluster/zone combination.

### 3. Seed pending topology and create the operation

`create_cluster` seeds a new initializing cluster, its first initializing zone,
membership, node pool, and node group. `attach_zone` requires the target cluster
to already be ready and seeds the new initializing zone, membership, pool, and
group.

The topology seed and operation rows are committed in one transaction. The
operation starts in phase `Requested` and its intent is immutable.

### 4. The worker claims the operation

The provider-neutral worker runs as:

```sh
crane-bootstrap -worker \
  -dsn "$CRANE_DATABASE_URL" \
  -bootstrap-config bootstrap.json
```

It verifies schema readiness, selects the configured provider through the
provider registry, polls PostgreSQL, and claims work with an owner, lease
expiration, and fence value.

The worker renews the lease while a remote provider step is running. If it
crashes, an expired lease can be reclaimed by another worker. A stale worker
cannot commit a result after losing its fence.

### 5. Load trusted cluster discovery

Before executing a phase, the worker loads the cluster relationship from the
database. For `attach_zone`, the existing private Kubernetes API endpoint and
CA hash come from the trusted cluster catalog, not from request parameters.

### 6. Provision protected infrastructure

The AWS infrastructure phase verifies the AWS account, discovers exactly one
deployment-owned VPC, reuses shared security groups, Secrets Manager, RDS, IAM,
and instance profiles, and selects the requested availability zone.

For `create_cluster`, it creates or discovers one protected host tagged
`cluster-<cluster-id>`. For `attach_zone`, it creates or discovers one protected
host tagged `zone-<zone-id>`.

The host must use the pinned platform AMI, configured instance type, and a
private address. The host is recorded as protected bootstrap infrastructure,
not as ordinary worker capacity.

### 7. Initialize a new cluster control plane (`create_cluster` only)

The worker runs kubeadm initialization on the new cluster host if needed,
discovers the private Kubernetes API endpoint, and computes the CA discovery
hash. Only the private endpoint, CA hash, host identity, private IP, and image
ID are returned to the durable controller.

The controller validates the endpoint before persisting it to the cluster
catalog. Join credentials are not persisted.

### 8. Install networking or join the existing cluster

For `create_cluster`, the worker installs the pinned Cilium chart on the new
cluster and waits for Cilium and node readiness.

For `attach_zone`, the worker:

1. Finds the trusted existing control-plane host.
2. Runs `kubeadm token create --ttl 10m --print-join-command` there.
3. Parses the command instead of executing arbitrary returned shell text.
4. Requires the endpoint to match the trusted API endpoint.
5. Requires the token to match kubeadm's expected format.
6. Requires the CA hash to match the trusted catalog hash.
7. Runs the validated `kubeadm join` command on the new zone host.
8. Waits for kubelet and the joined node to become active.

The short-lived join token exists only during this provider step. It is not in
the API request, operation parameters, or durable discovery result.

### 9. Register zone-specific identity and install Subd

The worker reconciles a Dominator SPIFFE identity:

```text
spiffe://<trust-domain>/dominator/<zone-id>
```

It is bound to the exact `crane-system/dominator-<zone-id>` service account.
It also registers a Subd identity for the protected host and installs Subd on:

- The new cluster's protected control-plane host for `create_cluster`.
- The new zone host for `attach_zone`.

### 10. Deploy the zone Dominator

Each zone gets a separate Kubernetes Deployment, service account, SPIFFE
identity, private Service, and database route.

The Dominator pod is pinned to the protected control-plane node. For an attached
zone, the new zone host joins the existing cluster, while that zone's Dominator
runs on the existing cluster's protected control-plane host.

Each Dominator uses a private Kubernetes Service named
`dominator-<zone-id>`. This avoids host-network port collisions when multiple
zone Dominators share one control-plane host. The provider records the Service's
private ClusterIP as the zone route on port `44000`.

### 11. Persist trusted results

After infrastructure, the controller records the protected provider host.

After control-plane initialization, it records private Kubernetes discovery.

After Dominator installation, it records the private Dominator endpoint and
expected SPIFFE identity in `zone_dominators`.

Database validation rejects public endpoints, wrong ports, non-HTTPS URLs, and
identity paths that do not exactly match the operation's zone.

### 12. Validate the zone and finalize the handoff

The final validation checks the node, Cilium, Kubernetes readiness, zone-specific
Dominator rollout, private Service address, and private SPIFFE-mTLS probe.

The operation can transition to `Ready` only after the trusted provider reports a
successful validation. The final transaction marks the operation succeeded,
marks the zone and membership ready, approves access, and clears the lease.

## Phase sequences

The durable state machine is:

```text
create_cluster:
Requested
  -> InfrastructureProvisioning
  -> ControlPlaneInitializing
  -> NetworkInstalling
  -> DominatorInstalling
  -> Validating
  -> Ready

attach_zone:
Requested
  -> InfrastructureProvisioning
  -> NetworkInstalling
  -> DominatorInstalling
  -> Validating
  -> Ready
```

`attach_zone` has no control-plane initialization phase because it joins an
existing cluster. Neither operation is allowed to become ready without the
validation handoff.

## Ownership boundaries

- `crane-api` owns authentication, idempotency, topology, durable intent, and
  status reporting.
- The generic bootstrap package owns phase ordering, provider-neutral contracts,
  leases, and fences.
- The AWS provider owns AWS APIs, SSM execution, kubeadm, Cilium, Kubernetes
  manifests, SPIRE entry creation, and AWS-specific validation.
- PostgreSQL owns the durable operation and topology state.
- Dominator owns zone execution and desired host state.
- Subd owns host-local enforcement and heartbeat reporting.

The generic workflow does not contain AWS branches. AWS is the currently
registered provider implementation behind the provider-neutral contract.
