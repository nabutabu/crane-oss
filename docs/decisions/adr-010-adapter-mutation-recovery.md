# ADR-010: Durable Adapter Mutation and Recovery Semantics

Status: Accepted

Date: 2026-10-16

## Context

The external provider RPC does not carry a stable autoscaler decision ID.
Network failures can therefore occur after Crane commits a target change but
before the adapter returns a response. Treating every retry as a new delta
would double-provision capacity; deduplicating by `(group, delta)` would merge
legitimate later decisions.

## Decision

- Each external increase/decrease handler creates one UUID request ID.
- Retries caused by an unavailable downstream Crane response reuse that UUID
  and the same expected target revision and desired target.
- A later external RPC, including an equal delta, receives a new UUID.
- Crane persists the request identity, expected revision, resulting revision,
  desired target, and immutable operation parameters transactionally with the
  target mutation.
- A definite compare-and-set conflict is returned to the caller; the adapter
  does not silently rebase or retry it with a newly read revision.
- After an adapter restart or an ambiguous lost response, recovery is by
  reading the durable target and revision. The adapter does not replay an
  unacknowledged delta from local memory.
- Explicit named-node deletion is a separate atomic, idempotent batch
  operation. It validates the complete node identity set before changing the
  target or creating removal operations.

## Consequences

Downstream retries are safe without confusing them with independent scaling
decisions. Recovery depends on durable Crane state rather than adapter-local
state, so restart behavior is deterministic. The protocol cannot provide
exactly-once semantics for an arbitrary external caller across process
restarts, so reconciliation remains part of the control loop.
