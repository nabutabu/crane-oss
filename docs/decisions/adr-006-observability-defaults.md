# ADR-006: Observability Defaults (Control/Data Plane Split)

Status: Accepted

## Logging
- Use stdlib `log/slog`. Env: `LOG_LEVEL`, `LOG_FORMAT`. Include `service`, `version`, `commit` on all logs.
- JSON in containers, text locally. Redact secrets (DSN, passwords, raw bodies, RDS/connection info).
- Fix invalid format verbs; replace library `log.Fatal` with returned errors (main exits).

## Telemetry (OTLP)
- All control-plane components emit OTLP traces and metrics via gRPC exporters. Use `OTEL_EXPORTER_OTLP_ENDPOINT` to enable export; otherwise use no-op providers.
- Propagate trace context (TraceContext/Baggage). Thread request_id/trace context through capacity/provisioning decisions; full distributed tracing is mandated as part of Phase 0+ baseline.
- Metrics emitted via OTLP; also expose `/metrics` (Prometheus) where applicable as a convenience surface.
- Resource attributes include `service.name`, `service.version`, `deployment.environment`.

## Metrics & Health
- Expose liveness/readiness on control-plane (crane-api, dominator). Metrics include capacity requests, node counts, credits, zone capacity, provisioning/reconciliation failures.
- Key signals: hosts scanned, problems by check/severity, actions by type/outcome, queue depth, heartbeat age, provider/DB latency.

## Tracing & Audit
- Audit events on catalog state transitions, remediation decisions, capacity/credit decisions with trace correlation.

## Control/Data Plane Boundary
- Control-plane (crane-api, dominator): full telemetry/audit (logs + OTLP). Data-plane (subd): logs locally to journald, reports outcome via heartbeat only (no open listener). Host identity bound to registered provider instance.
