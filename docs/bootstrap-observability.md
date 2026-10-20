# Bootstrap-managed layer-zero observability

The first-zone bootstrap installs a pinned OpenTelemetry Collector in
`crane-system` after Kubernetes and Cilium are ready. Crane API and Dominator
send OTLP/gRPC logs, traces and metrics to its shared internal Service. The
Collector runs as a single Deployment on the protected platform node. No public
endpoint is created.

## Backend configuration

Backend settings are native Collector configuration, supplied through the
operator-owned Kubernetes Secret `crane-system/otel-exporter-config`. There
are no backend settings in bootstrap JSON, no backend secret-store IAM grant,
and no backend credential baked into the AMI. Any OTLP/HTTP-compatible backend
can be used.

The required Secret key `exporter.yaml` contains a configuration fragment:

```yaml
exporters:
  otlp_http/backend:
    endpoint: https://your-otlp-backend.example
    headers:
      Authorization: "Bearer YOUR_CREDENTIAL"
```

Use the endpoint and headers required by your backend, or omit `headers` for
an unauthenticated backend. Paths in an OTLP endpoint are supported. Put
credentials in headers, not endpoint URLs. Use native Collector syntax and
escaping, including `$$` for literal dollar signs. See the non-secret
[exporter example](../deploy/observability/exporter.example.yaml).

The managed ConfigMap supplies receivers, pipelines, TLS defaults, retries,
batching and queues. The Secret fragment is loaded afterward and customizes
only `exporters.otlp_http/backend`; changes to other components are outside
this contract. Keep backend TLS certificate verification enabled. Optional
Secret keys can hold CA/client certificate files, referenced by absolute paths
under `/etc/otel-exporter` using native `tls.ca_file`, `tls.cert_file`
and `tls.key_file` settings. All Secret files are mounted read-only in the
Collector; neither application mounts them.

## First install and reruns

1. Run bootstrap normally. It creates the cluster and namespace, then stops
   with instructions if the Secret or its nonempty `exporter.yaml` key is
   missing. It does not deploy the Collector/applications or finalize readiness.
   Preserve the local deployment state and all created resources.
2. Use your deployment system or private administrative access to the platform
   host. For the AWS implementation, use the existing SSM Session Manager
   access workflow. On that host, use
   `KUBECONFIG=/etc/kubernetes/admin.conf` with sufficient privileges.
3. Supply the Secret from a protected local file or your deployment secret
   system. Do not include the file in Git, command arguments, SSM Run Command
   payloads, bootstrap JSON, logs or deployment state. For example, with a
   securely supplied file already on the administrative host:

   ```sh
   kubectl -n crane-system create secret generic otel-exporter-config \
     --from-file=exporter.yaml=/secure/exporter.yaml
   ```

4. Rerun bootstrap with the same configuration and state directory. It reuses
   resources and completes deployment after the Collector is ready.

Bootstrap checks key presence and reads only Secret metadata for rollout
tracking. It never copies the backend values into scripts or state and never
creates, overwrites or deletes this Secret. An invalid fragment prevents
Collector readiness and blocks bootstrap completion. Inspect Collector
diagnostics privately; do not print merged configuration containing credentials.

The removed `provider_config.observability` block is rejected in new input.
Remove it from your operator configuration. Pinned state from the previous
draft is normalized by discarding only that deprecated block; other identity,
placement and ownership checks remain active. No deployment-state reset or
resource replacement is required.

If upgrading a Collector already running the previous draft, restore backend
delivery and drain all sending queues before this rollout: renaming the
exporter from `otlp_http/newrelic` to `otlp_http/backend` changes its persistent
queue identity. Keep the old queue files and operator-owned secrets until
delivery is verified; bootstrap does not migrate or delete them.

## Rotation and receiver credentials

Update the operator-owned Secret through your deployment system, then restart
the Collector and verify delivery:

```sh
kubectl -n crane-system rollout restart deployment/otel-collector
kubectl -n crane-system rollout status deployment/otel-collector
```

Alternatively, rerun bootstrap: the Secret UID/resourceVersion participates in
the rollout annotation. Unchanged reruns leave the Collector running. Backend
credential rotation alone does not require restarting Crane API or Dominator.
Secret file updates alone do not reload the running Collector configuration.

Bootstrap separately generates and retains the private receiver token and TLS
material under `/var/lib/crane/otel`, reusing it on reruns. Applications
receive only client authentication headers and the public receiver certificate.
To rotate this internal material, replace the retained files and rerun
bootstrap to refresh the Collector and both application Deployments.

## Delivery and acceptance

The retained local PV advertises 10 GiB at `/var/lib/crane/otel-queue`.
This is a directory on the platform filesystem: it neither preallocates space
nor enforces a disk quota. Bootstrap requires 10 GiB free before installation;
the default platform disk remains 40 GiB. Size and monitor the disk for the OS,
images and telemetry. Queues survive pod restarts on that node and disk, but
not loss of the node or disk.

The internal receiver is
`otel-collector.crane-system.svc.cluster.local:4317`, using TLS and a bearer
token. Both applications use `hostNetwork`, so receiver TLS/authentication
remain essential alongside NetworkPolicy. No stdout `filelog` ingestion is
added.

Collector readiness confirms process health, not backend ingestion. Before
second-zone provisioning, demonstrate startup/request/background/error records,
correlation, redaction, graceful flush and restart/outage queue recovery.
See the [deployment guide](../deploy/observability/README.md) for delivery
metrics and optional New Relic dashboard/query examples. Bootstrap does not
automatically conduct backend outage tests.

For local verification with Docker, run
`CRANE_TEST_COLLECTOR=1 go test -run TestCollectorBackendSecretIntegration -v ./internal/provider/awscompute`.
This uses synthetic credentials and a local TLS backend. If Docker runs in a
different network namespace (for example Docker Desktop), set
`CRANE_TEST_COLLECTOR_BACKEND_HOST` to an address where containers can reach
the test process; the default is `host.docker.internal`.
