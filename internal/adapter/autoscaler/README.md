# Autoscaler External Provider Adapter (Prototype)

This package will implement the Cluster Autoscaler external gRPC cloud provider interface.
For Phase 0, we provide scaffolding and a minimal static node group template to validate
discovery/template semantics.

Notes:
- Pin exact Cluster Autoscaler release and Kubernetes version.
- Node groups must be zone-specific; templates must include correct labels/taints/topology.
- Implement as a separate deployable service (narrow boundary).
