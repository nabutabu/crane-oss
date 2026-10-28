# Bootstrap the AWS platform

Use a fresh deployment and empty database for this baseline. There is no migration
or reuse workflow for previous Crane bootstraps. Set required `development_stage`
and `group_suffix` in the shared bootstrap configuration (see `bootstrap.example.json`).
The initial worker group uses `<provider>-<development_stage>-<region>-<az>-<group_suffix>`;
AWS maps a native standard AZ such as `us-west-2a` to naming region `us-west-2`
and AZ `a`. Choose an explicit distinguishing suffix for additional clusters/accounts.
The first cluster, membership, and shared pool need no team records. Admission
opens only after the validated handoff marks the membership ready/approved.

Bootstrap uses the repository's Packer golden-image generator for host software
and versioned GHCR images for Crane API and Dominator. It creates a private
SPIRE Network Load Balancer (NLB) before image generation so the AMI can contain
its DNS hostname. No S3 bucket is created or used.

The command and bootstrap runner use the provider contract described in
[ADR-009](decisions/adr-009-vendor-agnostic-architecture.md). AWS is the sole
registered implementation. Version 3 configuration sets `provider` to `aws`;
`provider_config` contains `region`, optional `account_id`, `network`, `rds`,
`spire_host`, and `platform_host`. Shared SPIRE and platform software settings
remain in the top-level `spire` and `platform` objects. Older provisioning
config versions are rejected.

## Prepare the endpoint and build an AMI

Configure an AWS CLI profile with the operator permissions below. Use AWS CLI
v2 and either a built `crane-bootstrap` binary or Go matching `go.mod` to run
the command from this checkout. Packer builds the deployment AMI.

1. Copy `bootstrap.example.json` and set its deployment name,
   `provider_config.region`, and pinned
   Crane image references. The example image version is illustrative: select a
   release containing these bootstrap changes, including the bootstrap-tools
   binary archive, and use that same release in Packer. Images must use an exact
   version, full commit SHA, or digest; `latest` is rejected.
2. Run the preparation command:

   ```sh
   AWS_PROFILE=crane-bootstrap go run ./cmd/crane-bootstrap \
     -bootstrap-config bootstrap.example.json -prepare-images
   ```

   This provisions the VPC, subnets, security groups, internal NLB, TCP listener
   and empty SPIRE target group. It does not create hosts, databases or runtime
   secrets. It prints the private identity endpoint and saves preparation
   output in `.crane-bootstrap/<deployment>/endpoint.json` beside the config.
   In that file, `identity_endpoint` is the NLB hostname and
   `provider_output.spire_bundle_parameter` is the bundle parameter name.
3. Follow [golden_image_generator/README.md](../golden_image_generator/README.md)
   to build the AMI with this hostname, bundle parameter, region, trust domain
   and matching SPIRE/Kubernetes versions. The existing setup scripts run at
   image creation time. Agents and Kubernetes clusters are never started on the
   temporary build machine.
4. Set `provider_config.spire_host.ami_id` and
   `provider_config.platform_host.ami_id` to the resulting AMI in
   `bootstrap.example.json`. Both initial
   hosts currently use amd64 images; the generator also supports arm64 for other
   consumers. Bootstrap verifies the AMI tags against the prepared endpoint and
   configured versions.
5. Start bootstrap:

   ```sh
   AWS_PROFILE=crane-bootstrap go run ./cmd/crane-bootstrap \
     -bootstrap-config bootstrap.example.json
   ```

## Configure the Collector backend

The platform bootstrap installs the shared OpenTelemetry Collector after it
creates Kubernetes and the `crane-system` namespace. Backend settings are not
part of `bootstrap.example.json`, AWS Secrets Manager, or the AMI. Supply them
as native Collector configuration in an operator-owned Kubernetes Secret:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: otel-exporter-config
  namespace: crane-system
type: Opaque
stringData:
  exporter.yaml: |
    exporters:
      otlp_http/backend:
        endpoint: https://your-otlp-backend.example
        headers:
          Authorization: "Bearer YOUR_CREDENTIAL"
```

The required object is `crane-system/otel-exporter-config`; its required key is
`exporter.yaml`. Use the endpoint, headers and optional TLS file settings for
the chosen OTLP/HTTP backend. Keep credentials in headers rather than endpoint
URLs, and keep the Secret out of Git, bootstrap JSON, AMIs, SSM payloads and
logs. A non-secret example is in
[`deploy/observability/exporter.example.yaml`](../deploy/observability/exporter.example.yaml).

On a fresh deployment, bootstrap validates the core cluster, starts Crane API
and Dominator, and marks the zone ready even when the exporter Secret is absent.
Its handoff says **Observability configuration pending**, prints the Crane API
address and target catalog host ID, and includes private connection instructions.
Structured output and `result.json` include `control_plane_endpoint`,
`target_host_id`, `observability_status`, and `connection_instructions`.

Install the AWS Session Manager plugin locally, then open the SSM port-forwarding
session shown in the handoff. In a second terminal,
write the request and authorization header into protected local files. Retrieve
the deployment runtime secret ARN from `provider_output.runtime_secret_arn` in
the saved bootstrap result; never print its `admin_token`. For example:

```sh
umask 077
mkdir -p /tmp/crane-operator
aws secretsmanager get-secret-value --secret-id '<runtime-secret-arn>' \
  --query SecretString --output text \
  | jq -r '"Authorization: Bearer " + .admin_token' > /tmp/crane-operator/admin-header
jq -n --rawfile exporter /secure/exporter.yaml \
  '{request_id:"configure-observability-1",stringData:{"exporter.yaml":$exporter},rollout_deployment:"otel-collector"}' \
  > /tmp/crane-operator/observability-request.json
curl --request PUT --max-time 620 \
  --header @/tmp/crane-operator/admin-header \
  --header 'Content-Type: application/json' \
  --data-binary @/tmp/crane-operator/observability-request.json \
  'http://127.0.0.1:43060/v1/hosts/crane-platform%2Fbootstrap%2Fplatform/kubernetes/namespaces/crane-system/secrets/otel-exporter-config'
```

Replace the encoded host ID with the handoff's `target_host_id`, URL-encoding
the complete ID as one segment. The request blocks through Crane API,
Dominator and Subd until the Secret is verified and the Collector rollout is
ready. No bootstrap rerun is required. HTTP 200 proves readiness, not New Relic
delivery. A failed rollout leaves the Secret written and returns the failed
stage; retry identical contents with the same request ID, or use a new ID when
fixing the configuration or rotating credentials. See
[Kubernetes operations](kubernetes-operations.md) for the protocol and limits.

Bootstrap preserves this operator-owned Secret on reruns and reports missing
configuration separately from Kubernetes access errors. Core zone readiness
and observability readiness are separate. Subd's platform configuration uses
the existing local `/etc/kubernetes/admin.conf`; other bootstrap hosts do not
receive Kubernetes credentials. Protect and remove local request/header files
when finished.

The bootstrap-created internal Collector objects have separate responsibilities:

| Object | Data | Readers |
| --- | --- | --- |
| `otel-exporter-config` Secret | Backend exporter YAML | Collector |
| `crane-otel-receiver` Secret | Internal receiver bearer token | Collector |
| `crane-otel-client` Secret | Internal receiver authorization header | Crane API and Dominator |
| `otel-collector-tls` Secret | Collector receiver certificate and key | Collector |
| `otel-collector-ca` ConfigMap | Public receiver certificate | Crane API and Dominator |

The applications use only the internal receiver credentials. They never receive
the backend credential. Backend credential rotation requires restarting or
rerunning the Collector; it does not require restarting Crane API or Dominator.
Use the Secret endpoint with a new request ID and `rollout_deployment` for backend
rotation. Receiver token or certificate rotation requires rerunning bootstrap so the
Collector and both applications update together.

Packer uses the normal AWS credential chain, for example
`AWS_PROFILE=crane-bootstrap`; the bootstrap AWS adapter uses the same chain.
There are no `-aws-profile` or `-aws-region` flags; set the profile in the
environment and the region in `provider_config.region`. Never store AWS keys
in the template. Attach the
image-build policy alongside the bootstrap operator policy:
[golden-image-iam-policy.json](golden-image-iam-policy.json). It grants the
Packer EC2 image, temporary-instance, key pair, and security-group permissions
that the bootstrap operator policy alone does not.

Use `-dry-run -json` for a local phase preview. It does not contact AWS or
verify AMIs, registry access or permissions. `-operation-timeout` defaults to
90 minutes. `-json` emits newline-delimited phase/detail/output events.
Redirect or pipe CLI output to retain a transcript; SSM output is retrieved and
redacted before being printed.

## Network and identity

The internal NLB listens on TCP 8081 in the deployment's two private subnets.
Cross-zone forwarding reaches the single SPIRE backend regardless of the NLB
node used by a client. The NLB security group accepts 8081 from both host
security groups; the SPIRE host accepts 8081 from the NLB security group.
Target client IP preservation is disabled so the SPIRE host's own agent can
reach its server through the stable NLB hostname. The SPIRE security group
also accepts TCP 44001 from the platform security group for private Subd mTLS;
the platform group's existing self-traffic rule covers its local Subd.
Bootstrap checks ownership, VPC, subnets, security groups, target-group and
listener settings on reruns. TLS passes through to SPIRE unchanged.

Bootstrap configures and starts Subd on both hosts after registering their
host-specific systemd workload identities. Phase 9 verifies the running service
identity, real mTLS reads, a harmless revision-1 update and duplicate retry, and
rejection of writes from the read-only probe before marking the zone ready.
Both hosts are protected catalog inventory outside worker groups and credits.
Subd does not send heartbeats; catalog health remains unknown until monitoring
is integrated. See [Subd configuration and APIs](subd.md).

Use matching release binaries/images and a newly built golden AMI containing
the private endpoint and systemd attestor. Old POC databases are not upgraded;
initialize a fresh compatible database explicitly. Bootstrap retries preserve
the prepared endpoint and reuse matching registrations and host records.

The AMI contains the NLB DNS hostname, trust-domain name, region and SSM public
bundle parameter name. It contains no SPIRE private keys, SVIDs, agent data,
runtime credentials, initialized cluster or CA bundle. The SPIRE host generates
its authority when first started, then publishes the current public CA bundle
to `/crane/<deployment>/spire-bundle` in Parameter Store. A systemd timer
refreshes that parameter every five minutes to include CA rotations.

Before starting, the AMI's SPIRE agent service downloads the bundle through the
authenticated AWS API using its instance role, validates the PEM certificate,
and atomically installs it. Download/validation failure prevents agent startup.
Only the SPIRE role has `ssm:PutParameter` on this exact parameter; agents have
`ssm:GetParameter`. The parameter uses the Advanced tier to accommodate bundles
containing overlapping CA certificates. AWS may charge for this tier.

The trust domain is the identity namespace (for example `crane.internal`).
The CA signs workload identities; the public trust bundle lets an agent verify
the SPIRE server before AWS instance-identity attestation authenticates the
agent to the server. This establishes internal mTLS identity independently of
whether an API is accessible from the internet.

Preserve the NLB when maintaining its backend. Bootstrap refuses to add a
different backend alongside an existing target: server replacement must first
restore the authority's datastore/keys and explicitly reconcile the old target.
Recreating the NLB changes its AWS-generated hostname and requires rebuilding
AMIs. This implementation still has one SPIRE server and one Kubernetes
control-plane host; the load balancer does not make the authority highly
available.

RDS is encrypted and private, with separate `spire` and `crane` users.
Protected hosts have public IPs for outbound AWS and package/registry access;
security groups allow no public inbound access. Management uses Systems Manager.
Kubernetes and Crane endpoints remain private. Workload identities are
registered before platform deployment. Admission opens only after Kubernetes,
Cilium, database, API and real SPIFFE mTLS checks succeed; worker target stays
zero.

Crane API and Dominator run their released images, rather than host binaries
mounted into a generic container. Public GHCR images need no pull credentials.
Private GHCR images require Kubernetes pull credentials. For private ECR,
prepare the AMI's kubelet credential provider/registry access before use; a host
IAM role alone does not configure containerd or kubelet registry authentication.
The generated platform role permits ECR reads in the deployment account/region.

## Local state and retries

The default state directory is `.crane-bootstrap/<deployment>` beside the
bootstrap config; override it with `-state-dir`. Back up this directory with
the operator's deployment files when moving machines.

`config.json` records the account, selected availability zones and deployment
configuration. Preparation may omit AMIs and image references; each may be
supplied once after preparation, then becomes pinned. Changed deployment
settings are rejected. State files are atomically replaced with mode 0600;
`endpoint.json` retains preparation output and `result.json` records successful
handoff. These files contain references and configuration, never credentials.
Runtime passwords and API tokens live in Secrets Manager.

Results contain shared `name`, `provider`, `identity_endpoint`, and (after host
creation) `api_server_endpoint` fields. AWS instance IDs, region, private host
address, trust-bundle parameter name, and runtime secret reference are nested
under `provider_output`.

Rerun with the same configuration and state directory. Resources are discovered
by stable names and `CraneBootstrap` ownership tags; installation and schema
seeding reconcile existing state. Scripts are sent directly through SSM Run
Command and serialized on the host with a lock. Run one invocation per
deployment at a time; concurrent local or multi-machine provisioning is not
supported. Missing local state can be recovered only by reconstructing the
original configuration and explicit resource selections.

Bootstrap preserves infrastructure on failure. Teardown is an explicit
operator action; RDS deletion protection remains enabled.

## Operator IAM permissions

[bootstrap-iam-policy.json](bootstrap-iam-policy.json) is a policy template.
Replace `ACCOUNT_ID`, `REGION` and `DEPLOYMENT_NAME` throughout it.

The operator needs EC2/network provisioning and discovery, encrypted RDS
provisioning, deployment runtime IAM role/profile creation and pass-role,
Secrets Manager access, Systems Manager host commands and private port-forwarding
sessions, and ELBv2 creation,
target registration and discovery. First use may create RDS and ELB
service-linked roles. No S3 permissions are needed. Runtime hosts use instance
roles, never operator keys.

The template scopes tunnel targets to the deployment tag, permits only the
`AWS-StartPortForwardingSession` session document, and scopes session data-channel
and resume/termination permissions to the caller's own sessions, following
[AWS's Session Manager policy examples](https://docs.aws.amazon.com/systems-manager/latest/userguide/getting-started-restrict-access-quickstart.html).

Building the golden image with Packer uses the same operator identity and needs
the additional EC2 image-build permissions in
[golden-image-iam-policy.json](golden-image-iam-policy.json). Attach both
policies to the image-build identity; replace `REGION` in both.

## Database-only setup

The existing explicit local workflow remains:

```sh
go run ./cmd/crane-bootstrap -dsn "$CRANE_DATABASE_URL" -config first-zone.json
```

Services verify schema readiness at startup; bootstrap initializes the schema.
