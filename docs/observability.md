# Observability — Current State and Plan

**Status:** Assessment + logging implementation plan
**Scope:** `crane-api`, `dominator`, `subd`, and supporting packages

## Current State

The project has essentially **no real observability**. Logging exists everywhere
but is unstructured, metric/tracing surfaces are absent, and health checks are
shallow. Findings below cite the state of the repo at the time of writing.

### A. Logging — unstructured, non-correlated, partially unsafe

- Every binary and package uses stdlib `log` (`log.Println` / `log.Printf` /
  `log.Fatal`). No levels, no JSON, no key-value fields, no request/host/trace
  IDs. `go.mod` contains **zero** observability dependencies.
- Examples: `cmd/crane-api/main.go:163`, `internal/badhost/detector.go:34-79`,
  `internal/activitymanager/manager.go:84-89`, `internal/execute/executor.go:29-188`,
  `internal/hostcatalog/store/repository.go:23-303`, `internal/dominator/server.go:106-149`.
- **Secrets / sensitive data can reach logs:**
  - DSN containing the DB password on error: `cmd/crane-api/main.go:97-102`.
  - Raw heartbeat request bodies: `internal/dominator/server.go:127,134`.
  - RDS connection info (username, secret ARN, SG): `internal/execute/executor.go:114,152`.
- **Invalid log calls** (violate `go vet`):
  - `log.Printf(..., %w, ...)` wrapped errors: `internal/subd/runner.go:31,36`.
  - `log.Fatal("... %v", err)` with printf-style args: `cmd/dominator/main.go:34`.
- **Errors are logged-and-swallowed** throughout the control loops, making failures
  invisible: `internal/badhost/detector.go:50-53`, `internal/activitymanager/manager.go:51-55`,
  `internal/subd/client.go:54`, `internal/subd/services_collector.go:114` (returns `nil` always).
- `log.Fatal` used inside library/execution paths, killing the process on
  recoverable errors: `internal/execute/executor.go:94,111,143`, `internal/dominator/server.go:56-59`.
- **No audit trail.** The closest artifact is the persisted `actions` table
  (`internal/execute/postgres_action_store.go:22-26`); it does not record
  decision rationale, the problems that drove a decision, or any actor identity.

### B. Metrics — absent

- No Prometheus, OpenTelemetry, or `expvar` anywhere. No `/metrics` endpoint on
  any binary. None of the key business signals (hosts scanned, problems by
  check/severity, actions by type and outcome, queue depth, heartbeat age,
  provider/DB latency) are measurable.

### C. Tracing — absent

- The problem → decision → action pipeline is correlated only by `host_id` in
  Postgres (`internal/badhost/problem/problem_store.go`, `internal/execute/postgres_action_store.go`).
  No request IDs, no spans, no context propagation.

### D. Health checks — shallow, no liveness/readiness split

- `crane-api`: `/health` always returns 200 (`cmd/crane-api/main.go:162-165`);
  `/v1/db/health` pings the DB (`cmd/crane-api/main.go:167-177`).
- `dominator`: `/v1/health` returns a fixed string (`internal/dominator/server.go:77-82`);
  `/v1/db/health` pings the DB (`internal/dominator/server.go:64-75`).
- No `HEALTHCHECK` in either `Dockerfile.crane-api` or `Dockerfile.dominator`.
- No readiness concept anywhere.

### E. Error handling / alerting — no signal path

- No alerting hooks or notification mechanism of any kind. Logged-and-dropped
  errors (see A) mean an operator has no way to learn something is failing except
  reading logs by hand.

### F. Access / audit — nothing wired

- `internal/hostcatalog/http/handlers.go` exposes state/health transitions with no
  authentication and is not registered in any mux (dead code, a liability if ever
  wired). There is no audit middleware or audit log for API calls.
- The only trust boundary that exists is subd ↔ dominator mTLS via SPIRE
  (`internal/dominator/server.go:47-54`, `cmd/subd/main.go:45-49`); dominator
  extracts the host ID from the SPIFFE ID (`internal/dominator/server.go:97-119`)
  but never logs an auditable "who did what" record.

### G. Deployment / runtime

- `docker-compose.yml` has no healthchecks, restart policies, resource limits, or
  log config.
- No version injection via `-ldflags` in `Dockerfile.crane-api` / `Dockerfile.dominator`
  or `.github/workflows/*` — a running binary cannot be correlated to a commit.
- CI only runs `go build` (`.github/workflows/go.yml:24-25`).

### H. Configurability

- No log-level, log-format, metrics-port, or tracing toggles. Only `DB_*` / `AWS_*`
  env vars (`cmd/crane-api/main.go:89-93`, `cmd/dominator/main.go:18-22`) plus
  `bhd.json` and `appsettings.json`.

## Should every part be observable?

Yes — but signals should respect the trust boundary between control plane and
data plane.

- **Control plane (`crane-api`, `dominator`)** — full observability: structured
  logs, metrics, and an audit trail. `docs/invariants.md:5` ("all mutations go
  through the Host Catalog") makes the catalog the natural instrumentation
  chokepoint.
- **Data plane (`subd`)** — observable, but not by streaming telemetry to the
  control plane. subd runs root on tenant/attacker-influenced hosts. It should:
  1. emit structured logs locally to journald (it is a systemd unit on the host);
  2. report **outcome** through the heartbeat it already sends to dominator
     (`internal/subd/runner.go:45-53`) so the control plane can produce fleet
     metrics; and
  3. keep no open listener. A thin agent is also a smaller attack surface.

## Logging Implementation Plan

Phase 1 introduces structured logging with no behavior change and no new
dependencies (stdlib `log/slog`, Go 1.25).

1. **Shared logger setup (per binary).** Add a small setup in each `cmd/*/main.go`
   that builds a `*slog.Logger` from env vars:
   - `LOG_LEVEL` (debug/info/warn/error, default `info`)
   - `LOG_FORMAT` (json/text, default `text` for local, `json` in containers)
   - Always add a `service` field (e.g. `crane-api`, `dominator`, `subd`) and
     `version`/`commit` once build metadata is injected (phase 5).
2. **Thread the logger through constructors.** Pass `*slog.Logger` into the main
   constructed types (`BadHostDetector`, `ActivityManager`, `Worker`, `DefaultExecutor`,
   `PostgresActionStore`, `PostgresHostStore`, `HostCatalogService`, `PolicyResolver`,
   `dominator.Server`, `subd Runner/Client/collectors`) instead of package-level
   `log`. Keep a temporary standard-log→slog bridge during migration if needed.
3. **Replace hot-path log calls with typed fields.** Convert the existing
   `log.Printf("[X] ...")` lines into `logger.Info/Debug/Warn/Error` calls with
   structured attributes, e.g. `host_id`, `action_type`, `check`, `problem_type`,
   `zone`. Use `Warn`/`Error` (never `Info`) for failures.
4. **Fix correctness and safety issues found in A:**
   - Remove/redact the DSN, raw-body, and connection-info logging
     (`cmd/crane-api/main.go:97-102`, `internal/dominator/server.go:127,134`,
     `internal/execute/executor.go:114,152`).
   - Fix invalid format verbs (`internal/subd/runner.go:31,36`,
     `cmd/dominator/main.go:34`).
   - Replace `log.Fatal` in library code with returned errors; only `main`
     decides to exit (`internal/execute/executor.go:94,111,143`,
     `internal/dominator/server.go`).
5. **Audit-grade events (control plane).** Emit JSON structured events on:
   - every remediation decision (`internal/activitymanager/manager.go:84`) with
     `host_id`, `action_type`, and the problems that drove the decision;
   - every catalog state transition (`internal/hostcatalog/service/service.go:45`)
     with `host_id`, `from_state`, `to_state`.
6. **Verify.** Run `gofmt -l`, `go vet ./...`, `go test ./...` after migration
   (CI currently does none of these — see `.github/workflows/go.yml:24-25`).

### Later phases (not in this pass)

- `/metrics` (Prometheus or `expvar`) on `crane-api` and `dominator`.
- Liveness/readiness split + `HEALTHCHECK` in Dockerfiles.
- `-ldflags` version injection wired through CI.
- subd journald logging + heartbeat enrichment (convergence outcome).
- mTLS auth + audit middleware before wiring `internal/hostcatalog/http` handlers.