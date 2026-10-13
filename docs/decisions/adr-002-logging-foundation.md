# ADR-002: Structured Logging Foundation

Status: Accepted

## Decision
- Use stdlib `log/slog` for all logging (Go 1.25) with OTLP-integrated observability.
- Introduce `internal/logging`, `internal/config`, and `internal/telemetry` packages.
- Environment variables: `LOG_LEVEL` (debug/info/warn/error, default info), `LOG_FORMAT` (json/text). Default to text locally; json in containers via environment.
- Each binary creates a logger with `service`, `version`, `commit` fields.
- OTLP telemetry (traces and metrics) emitted via OTLP gRPC exporters when `OTEL_EXPORTER_OTLP_ENDPOINT` is configured; fallback to no-op providers if unset.
- Control-plane services (crane-api, dominator) emit structured logs/audit trail and OTLP telemetry. Data-plane (subd) logs locally to journald and reports outcomes via heartbeat (no open listener for telemetry) - boundary maintained.
- Redact secrets (DSNs, passwords, raw bodies, connection info). Replace library `log.Fatal` with returned errors; only main exits. Fix invalid format verbs.
