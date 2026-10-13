# ADR-003: Cluster Autoscaler External Provider Interface

Status: Draft

## Context
Need to integrate with Kubernetes Cluster Autoscaler without forking scheduler/custom Pod-watching logic.

## Decision
- Implement Cluster Autoscaler external gRPC cloud provider as a separately deployable adapter service (replaceable, narrow boundary).
- Pin exact Cluster Autoscaler release and target Kubernetes version.
- Node groups: per-zone + per-profile (hybrid). Templates must be zone-specific. Bootstrap must set standard topology labels.
- Crane API owns placement/policy/credits; Dominator owns zone execution; Subd owns host enforcement.
- Adapter and all control-plane services emit OTLP traces/metrics when configured.

## Notes
- Prototype to report ≥1 static node group with valid zone-specific node template.
