# ADR-005: Zone Placement, Scale-down, Failure Handling

Status: Draft

## Placement
- Crane API owns global placement/policy/credits. Dominator zone-scoped execution. Subd host-scoped enforcement.
- Zone selection: filter eligible zones (constraints/capacity/availability), rank by headroom/latency/cost/fault-domain distribution.
- Use allocatable minus reserved for decisions; catalog snapshot not atomic reservation.
- Node templates zone-specific; standard topology labels required.

## Scale-down
- Autoscaler requests reduction. Drain via Kubernetes respecting PDBs. Defer termination if drain incomplete.
- Credits released after instance terminated and catalog updated. No auto-credit on target drop.

## Failure Handling
- Zone unavailable → mark unavailable for new provisioning; do not auto-terminate healthy nodes on transient discovery failures.
- Idempotency, durable desired state, periodic reconciliation, heartbeat resilience, partial provisioning handling.
