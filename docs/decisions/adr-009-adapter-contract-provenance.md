# ADR-009: Pinned External Adapter Contract Provenance

Status: Accepted

Date: 2026-10-16

## Context

The Cluster Autoscaler external provider API is an upstream compatibility
boundary. Recreating its protobuf service locally or silently regenerating it
from a different release could change method names, status expectations, or
wire compatibility without an obvious Crane code change.

## Decision

- The Phase 0 adapter targets the `cluster-autoscaler-1.35.0` external gRPC
  contract.
- Crane vendors only the upstream `.proto` and generated Go bindings required
  by the adapter; it does not vendor the complete Cluster Autoscaler source.
- The source URL, release tag, generated-file provenance, and SHA-256 hashes
  are recorded in `internal/adapter/autoscaler/protos/README.md` and checked
  by an executable provenance test.
- Updating the Cluster Autoscaler minor release requires a compatibility
  review, regenerated bindings, updated hashes, and the contract test suite;
  version tags must not float implicitly.

## Consequences

The adapter can prove which upstream wire contract it implements and detect
accidental protocol drift in review or CI. A protocol upgrade is explicit
work, but the cost is bounded to the generated bindings and compatibility
tests.
