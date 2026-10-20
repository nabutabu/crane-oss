# Crane API application log export

Crane API exports standard OpenTelemetry logs over OTLP/gRPC when an endpoint
is configured. It also writes redacted JSON to stdout for `kubectl logs`.
Log export remains opt-in at each entrypoint: Crane API and Dominator enable
it, while Subd and other binaries retain local logging.
The bridge uses the same `LOG_LEVEL` for both destinations (default `info`,
`debug` only when explicitly selected), without application log sampling.

The layer-zero bootstrap configures both Crane API and Dominator. For a manual
Crane API rollout against an internal Collector, the relevant settings are:

```yaml
- name: OTEL_EXPORTER_OTLP_ENDPOINT
  value: https://otel-collector.crane-system.svc.cluster.local:4317
- name: OTEL_EXPORTER_OTLP_PROTOCOL
  value: grpc
- name: OTEL_EXPORTER_OTLP_CERTIFICATE
  value: /etc/crane-otel/ca.crt
- name: OTEL_EXPORTER_OTLP_HEADERS
  valueFrom:
    secretKeyRef:
      name: crane-otel-client
      key: headers
- name: OTEL_SERVICE_NAME
  value: crane-api
- name: POD_NAME
  valueFrom:
    fieldRef:
      fieldPath: metadata.name
- name: OTEL_RESOURCE_ATTRIBUTES
  value: deployment.environment.name=production,k8s.cluster.name=layer-zero,deployment.id=YOUR_DEPLOYMENT,service.instance.id=$(POD_NAME)
```

Do not put sensitive data in resource attributes.

The resource defaults are `service.name=crane-api`, the compiled
`service.version`, and `deployment.environment.name` from `ENVIRONMENT`.
`OTEL_SERVICE_NAME` and `OTEL_RESOURCE_ATTRIBUTES` override defaults consistently
for logs, traces and metrics. Include deployment, cluster and instance identity
in deployment configuration.

Standard SDK configuration is honored:

- `OTEL_EXPORTER_OTLP_{LOGS,TRACES,METRICS}_ENDPOINT` overrides the common endpoint
  for that signal. A signal is enabled only with a common or signal endpoint.
- Common and signal-specific `HEADERS`, `CERTIFICATE`, `CLIENT_CERTIFICATE`,
  `CLIENT_KEY`, `INSECURE` and `TIMEOUT` variables configure authentication, TLS
  and exporter timeouts. Timeouts are in milliseconds. Headers must use the
  standard comma-separated, URL-encoded format; supply them through Secrets.
- Use an `https://` endpoint for TLS. Use an explicit `http://` endpoint for an
  internal plaintext receiver. Bare host:port endpoints now use the SDK TLS
  default; set `INSECURE=true` explicitly if needed.
- `PROTOCOL`, including per-signal overrides, must be `grpc`. Unsupported
  protocols return a configuration error and disable export with a local warning.

The applications use only internal receiver credentials when exporting to a
Collector. Supply backend credentials only to the Collector through its Kubernetes Secret. Use application OTLP as the only
Crane API and Dominator log ingestion path; exclude both stdout streams from any
filelog pipeline.

Dominator uses the same endpoint, TLS trust and receiver authentication
settings as Crane API. Its records retain `service.name=dominator`, the
compiled version and deployment resource attributes, while the shared redaction
handler protects both stdout and OTLP. The bootstrapper supplies the same
Collector configuration to both Deployments.

Redaction runs before fan-out. Messages, groups, nested values, attached fields
and standard-library log calls use the existing shared redaction rules. With
context-aware slog calls, valid trace/span IDs appear in stdout and the native
OTel log record correlation fields. Exporter diagnostics use a separate local
handler and deliberately omit raw exporter error details.

Startup logging follows exporter initialization. Terminal errors are logged
before shutdown. Shutdown attempts logs, traces and metrics concurrently within
a ten-second deadline, independently of the canceled service context. Stdout
continues working if export initialization fails or the Collector is unavailable.

Logs have a bounded in-memory queue of 2,048 records, batches of up to 512 and a
one-second export interval. Exporter timeouts/retries are bounded. Queue overflow,
prolonged outages, abrupt termination, and shutdown deadline expiry can lose
records. This is not a durable application audit log. A Collector persistent
queue protects records only after they reach that Collector.

## Rollout gate

The application pipeline, HTTP request instrumentation, background context,
and Collector deployment are separate review units. See
[Collector deployment and live acceptance](../deploy/observability/README.md)
for manifests, dashboard inputs, and the acceptance procedure. Keep zone provisioning paused until the complete gate is
recorded: startup, request, background and controlled error logs in the configured backend;
searchable resource metadata and log-to-trace correlation; synthetic-secret
redaction in stdout and export; graceful-termination flush; Collector restart and
backend-outage queue recovery within configured capacity. No acceptance is
claimed by passing the in-process receiver tests alone.

## HTTP request observability

Every Crane API route, including authentication failures and unmatched requests,
has a server span and completion log. The API extracts W3C trace context and
creates its own UUID request ID, returned in `X-Request-ID`. Context-aware
processing logs share that request ID and span. Completion fields are `method`,
matched `route`, `status`, `duration_ms`, and `panicked`. Unknown routes use the
constant `unmatched`; no raw paths, query strings, headers or bodies are logged.
Panics are recorded without their potentially sensitive values and propagate to
Go's HTTP server. Server failures mark spans as errors.

Metrics `http.server.request.count` and `http.server.request.duration` (seconds)
carry method, matched route and status dimensions. They share the resource
configuration of traces and logs. Capacity authentication preserves the active
server span instead of replacing it with the incoming parent context.

## Background processing

Activity analysis, bad-host scans, and action execution create spans at their
operation boundaries. Their slog calls pass context through to OTLP and include
stable operation names alongside existing host, action and zone identifiers.
The API joins its background loops before closing the database and flushing
telemetry. The action worker observes cancellation without waiting for its next
one-minute polling tick. These shared context-aware calls do not enable log
export in services that do not explicitly enable it.
