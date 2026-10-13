# ADR-001: Phase 0 - Architecture and Compatibility Spike

Status: Draft

## 1. Versions and Interfaces
- Target Kubernetes: to be confirmed (recommend 1.30+ for Cluster Autoscaler compatibility)
- Cluster Autoscaler: pin exact release (e.g. v1.30.x). Do not rely on generic interface.
- CRD API: use `platform.crane.io/v1alpha1` (proposed). Treat as alpha; plan for conversion if needed.
- External provider: implement Cluster Autoscaler external gRPC cloud provider interface as a separately deployable adapter service (not embedded in crane-api). Keeps boundary narrow and replaceable.

## 2. Node Group Topology
- Node groups: per-zone and per-profile (hybrid). Rationale: allows zone-scoped placement, correct zone-specific node templates, safer scale-down per zone, and balanced placement policies.
- Node templates must be zone-specific; never advertise cross-zone templates.
- Node bootstrap must set standard labels: `topology.kubernetes.io/region`, `topology.kubernetes.io/zone`, `platform.crane.io/node-pool`. Cloud metadata (region/zone) must be sourced from provider/instance identity or bootstrap config.

## 3. Identity, Tenancy, Credits
- Team identity: derived from trusted source (platform team registry/namespace ownership/workload identity). Service-name-only authorization is rejected.
- Pool tenancy: team-dedicated by default; shared pools only if explicitly configured with appropriate RBAC.
- Credits: capacity entitlement (simultaneous vCPU/memory/nodes) is the initial enforcement model (admission control). Cost-based metering (instance-hours) may be added later; do not conflate units. Reconciliation: track reservations/commitments in ledger; release credits only after actual termination (not on target reduction).

## 4. Placement, Scale-down, Authority
- Authority: Crane API owns global placement/policy/credits; Dominator owns zone execution; Subd owns host enforcement and drain.
- Scale-down: autoscaler requests reduction; drain via Kubernetes with PDB respect; defer termination if drain cannot complete; credits released after instance is terminated and catalog updated.
- Zone failure: mark zone unavailable for new provisioning on failure; do not auto-terminate healthy existing nodes on transient discovery failures. Existing instances remain running.

## 5. Observability Defaults (no new deps)
- Logging: stdlib `log/slog`. Env: `LOG_LEVEL` (debug/info/warn/error, default info), `LOG_FORMAT` (json/text, default text local, json in containers). Always include `service`, `version`, `commit`. Redact secrets (DSN/passwords, raw bodies, connection info). Fix invalid format verbs; replace library `log.Fatal` with returned errors (main exits).
- Metrics: expose `/metrics` (Prometheus preferred; expvar fallback). Liveness/readiness split on control-plane services.
- Tracing: thread request_id/trace context through capacity/provisioning decisions; full tracing later.
- Boundary: control-plane (crane-api, dominator) full telemetry/audit; data-plane (subd) logs locally to journald, reports outcome via heartbeat (no open listener for telemetry).

## 6. Prototype & Exit Criteria
- Implement minimal external provider adapter prototype reporting >=1 static node group with valid zone-specific node template.
- ADRs stored under `docs/decisions/`.
- Exit: design decisions recorded + prototype validates discovery/template semantics against chosen autoscaler interface.
