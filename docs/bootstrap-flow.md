# Crane bootstrap flow

This document describes the bootstrap behavior implemented for fresh Crane
deployments. Existing deployments are intentionally out of scope; this is not a
migration or adoption workflow.

There are two related workflows:

1. The initial deployment bootstrap, which creates the shared layer-zero
   services and the first Kubernetes cluster.
2. The durable `create_cluster` workflow, which is submitted
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

## Durable `create_cluster`

After initial deployment, an administrator submits `create_cluster` to
`POST /v1/bootstrap` with `CRANE_ADMIN_TOKEN`. Read its durable state through
`GET /v1/bootstrap/{operation-id}` using the same admin authentication.

The API validates immutable request, cluster and zone IDs plus public
configuration or opaque references. It rejects credentials and join tokens,
serializes submissions for a cluster, and commits pending topology with the
operation. Exact request retries return the original operation; changed intent
or conflicting active work is rejected.

Run the provider-neutral worker:

```sh
crane-bootstrap -worker \
  -dsn "$CRANE_DATABASE_URL" \
  -bootstrap-config bootstrap.json
```

The worker verifies schema readiness, selects a provider through the registry,
and claims cluster-creation work with an owner, lease expiration, and fence.
It renews leases during remote steps. Expired leases can be reclaimed after a
restart, and stale owners cannot commit durable results.

Its phase sequence remains:

```text
Requested
  -> InfrastructureProvisioning
  -> ControlPlaneInitializing
  -> NetworkInstalling
  -> DominatorInstalling
  -> Validating
  -> Ready
```

The AWS executor verifies the pinned account and deployment-owned VPC,
reconciles a protected host tagged `cluster-<cluster-id>` using the configured
platform AMI and instance profile, and records it outside ordinary worker
capacity. It initializes kubeadm, persists private API discovery and the CA
hash, and installs Cilium. It registers the cluster's zone-specific Dominator
and Subd identities and installs those services. The Dominator runs on that
cluster's protected control-plane host behind its private Service. The final
validation checks the node, networking and authenticated Dominator before the
transaction activates the cluster and membership.

AWS-specific operations, remote scripts, ownership checks and identity setup
remain in the provider implementation. Shared capacity/bootstrap packages own
intent, leases, phase ordering and durable handoff. Ordinary worker
provisioning remains owned by the registered zone Dominator.

## Removed legacy zone attachment

`attach_zone` is temporarily unavailable. Admin-authenticated submissions return
HTTP 503 with `{"code":"attachment_unavailable"}` before provider validation or
database writes. Historical operation status remains readable, but neither the
bootstrap worker nor its provider executor can execute an attachment.

The removed path created a protected host in the new zone, joined that host to
the existing cluster, installed Subd, and then deployed a zone Dominator on the
existing control-plane host. Removal does not delete any existing cloud
resources or modify older deployments. Schema fields that allow historical
attachment status are retained.

[Issue #100](https://github.com/nabutabu/crane-oss/issues/100) defines the planned
replacement. Crane API will prepare zone networking and an empty group,
reconcile the zone's Dominator in its hosting cluster, then mark membership
ready with zero hosts. Requesting a worker will be a separate capacity call,
executed by that Dominator through the existing worker provisioning and
readiness contract. The replacement controller, API Kubernetes permissions,
network reconciliation, and shared SPIRE reference changes are future work.
