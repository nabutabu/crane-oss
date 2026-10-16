# ADR-005: Zone Placement, Scale-down, Failure Handling

Status: Accepted

Date: 2026-10-16

## Placement
- Crane API owns global placement/policy/credits. Dominator zone-scoped execution. Subd host-scoped enforcement.
- Zone selection is used only for new zone/cluster placement. A node-capacity
  request already names a cluster-scoped node group, so it is never redirected
  to another zone.
- For future new-zone placement, filter unavailable zones first, then rank by
  headroom, latency, cost, and fault-domain distribution. Persist the chosen
  zone in the operation; retries reuse it.
- Use allocatable minus reserved for decisions; catalog snapshot not atomic reservation.
- Node templates zone-specific; standard topology labels required.

## Scale-down
- Autoscaler requests reduction. Drain via Kubernetes respecting PDBs. Defer termination if drain incomplete.
- Credits released after instance terminated and catalog updated. No auto-credit on target drop.
- `DecreaseTargetSize` cancels only pending, unclaimed provisioning intent.
  `DeleteNodes`, repair, and maintenance use explicit host identities and a
  durable exclusive removal operation.

## Failure Handling
- Zone unavailable → mark unavailable for new provisioning; do not auto-terminate healthy nodes on transient discovery failures.
- Idempotency, durable desired state, periodic reconciliation, heartbeat resilience, partial provisioning handling.
- Dominator is the sole Kubernetes drain coordinator. A blocked or expired
  drain remains observable and does not implicitly terminate the host.
- Provider operations carry immutable client tokens and launch parameters;
  recovery retries the same token or reconciles provider inventory.
