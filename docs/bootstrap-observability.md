# Bootstrap-managed layer-zero observability

The AWS first-zone bootstrap installs the pinned OpenTelemetry Collector in
`crane-system` after Kubernetes and Cilium are ready, then configures Crane API
and Dominator to send OTLP/gRPC logs, traces and metrics to the same internal
Service. The Collector is a single Deployment pinned to the protected platform
node, not a sidecar. No public endpoint is created.

## Configuration and credentials

Set `provider_config.observability.new_relic_secret_arn` in the bootstrap
configuration to the ARN of a Secrets Manager secret in the configured AWS
account and region. The secret's `SecretString` must be the raw New Relic ingest
license key. The endpoint defaults to `https://otlp.nr-data.net`; set
`new_relic_otlp_endpoint` to the account's regional HTTPS origin when required.
The endpoint cannot contain credentials, a path or a query string.

The secret ARN may be added after the image-preparation phase, but it becomes
pinned when the bootstrap configuration is resolved for infrastructure
creation. Later changes require a new deployment name. The platform instance
role receives `secretsmanager:GetSecretValue` only for this ARN; the SPIRE role
does not. Bootstrap fetches the key without printing it and places it in the
Collector-only `new-relic-otlp` Kubernetes Secret. Do not put the key in Git,
bootstrap output, application settings or resource attributes.

Bootstrap creates a random receiver token and a self-signed TLS certificate
for `otel-collector.crane-system.svc.cluster.local`. It retains the token and
private key under `/var/lib/crane/otel` on the protected platform host and
reuses them on reruns. Kubernetes Secrets expose the receiver token only to the
Collector; the application client Secret contains only the OTLP authorization
header. Crane API and Dominator mount the public certificate as their trust
root. Rotation is done by replacing the retained receiver material and rerunning
bootstrap so all related Kubernetes objects and Deployments are updated
together.

## Delivery and recovery

The pinned Collector uses a 10 GiB retained local PersistentVolume at
`/var/lib/crane/otel-queue`, owned by UID/GID 10001 and bound to the protected
platform node. Bootstrap requires at least 10 GiB free on the platform host
before it installs the Collector; the default platform volume is now 40 GiB to
leave room for the OS, Kubernetes images and queue space. Configure enough
`platform_host.storage_gib` for your image set. Its signal queues persist across
Collector pod restarts on that node and disk. They do not survive loss of the
host or disk. Bootstrap verifies
Collector rollout readiness before it deploys/restarts the applications and
validates Collector rollout alongside Crane API, Dominator and Cilium.

The applications send to
`otel-collector.crane-system.svc.cluster.local:4317` using TLS and bearer-token
authentication. The Collector exports OTLP/HTTP over TLS to the configured New
Relic endpoint. NetworkPolicy allows Crane API and Dominator receiver traffic;
both application Deployments use `hostNetwork`, so TLS and receiver
authentication remain the effective controls for host-origin traffic. No
stdout `filelog` receiver is added for these applications.

Collector readiness confirms the process is serving; it does not confirm New
Relic ingestion. Before second-zone provisioning, complete the live New Relic
acceptance steps in the
[observability deployment guide](../deploy/observability/README.md), including
redaction, graceful application flush, and queue recovery during a backend
outage. The bootstrap does not automatically conduct backend outage tests.
