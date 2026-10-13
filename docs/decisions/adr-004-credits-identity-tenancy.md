# ADR-004: Credits, Identity, Tenancy

Status: Draft

## Credits
- Initial model: capacity entitlement (simultaneous vCPU/memory/nodes) for admission control.
- Cost-based (instance-hours) may follow; units must not be conflated.
- Release credits after actual termination (not on target-size reduction). Maintain reservation/commitment ledger with reconciliation.

## Identity & Tenancy
- Team identity from trusted source (platform registry/namespace ownership/workload identity). Reject service-name-only authorization.
- Pools team-dedicated by default; shared only with explicit RBAC.
- Crane API is authoritative for reservations/budgets (CRD validation not sufficient).
- Resolve identity from trusted metadata, not arbitrary workload fields.
