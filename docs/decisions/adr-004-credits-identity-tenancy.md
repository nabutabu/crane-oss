# ADR-004: Credits, Identity, Tenancy

Status: Accepted

Date: 2026-10-16

## Credits
- Initial model: capacity entitlement (simultaneous vCPU/memory/nodes) for admission control.
- Cost-based (instance-hours) may follow; units must not be conflated.
- Release credits after actual termination (not on target-size reduction). Maintain reservation/commitment ledger with reconciliation.
- A reservation is created atomically with the target revision and provisioning
  intent. `reserved` covers admitted-but-not-confirmed capacity,
  `committed` covers a confirmed provider allocation, and `released` is only
  set after termination and catalog confirmation.
- A target decrease cancels only unclaimed pending intent. It cannot release a
  reservation for a leased or externally launched operation.

## Identity & Tenancy
- Team identity from trusted source (platform registry/namespace ownership/workload identity). Reject service-name-only authorization.
- Pools team-dedicated by default; shared only with explicit RBAC.
- Crane API is authoritative for reservations/budgets (CRD validation not sufficient).
- Resolve identity from trusted metadata, not arbitrary workload fields.
- The adapter authenticates as a cluster principal; `ClusterID` comes from that
  authenticated identity, not the request body. Crane resolves team ownership,
  pool limits, and launch parameters from durable node-group policy.
- The initial implementation uses a deployment-scoped identity registry mapping
  the adapter certificate/SPIFFE identity to one cluster. Team identity is a
  later platform integration, but the API must not accept a caller-owned team
  identifier as authority.
