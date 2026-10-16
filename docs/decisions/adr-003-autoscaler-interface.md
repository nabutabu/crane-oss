# ADR-003: Cluster Autoscaler External Provider Interface

Status: Accepted

Date: 2026-10-16

## Context
Need to integrate with Kubernetes Cluster Autoscaler without forking scheduler/custom Pod-watching logic.

## Decision
- Implement the Cluster Autoscaler external gRPC cloud provider as a
  separately deployable adapter service. It is a replaceable protocol boundary,
  not a package embedded in `crane-api`.
- The adapter exposes only groups whose `ClusterID` matches its authenticated
  cluster identity. It passes the selected `NodeGroupID` through unchanged;
  it never chooses a different group or zone.
- `IncreaseSize` and `DecreaseTargetSize` are translated into a
  revision-checked Crane target mutation. The adapter creates one UUID per
  external RPC handler and reuses it only while retrying that RPC. The UUID is
  not derived from `(group, delta)` and is not reused for a later equal delta.
- If the adapter loses its response after Crane commits, recovery is by
  `TargetSize`/target revision refresh and reconciliation. The adapter must not
  apply the delta a second time.
- `DeleteNodes` carries explicit node identities and uses the separate durable
  removal workflow. It is never implemented as an implicit target decrease.
- Node groups are per `(cluster, zone, capacity class)` and templates are
  zone-specific. Bootstrap sets region, zone, and Crane pool labels.
- Crane API owns placement, policy, credits, and target authority; Dominator
  owns zone execution; Subd owns host-local enforcement.
- Adapter and control-plane services emit OTLP traces/metrics when configured.

## Notes
- The Phase 0 compatibility baseline is defined in ADR-001.
- The first executable prototype may use static groups, but it must implement
  the same identity, not-found, target-refresh, and error semantics as the
  production adapter.
