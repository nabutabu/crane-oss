# ADR-014: Dominator-driven worker provisioning for new deployments

Status: Accepted

## Context

Capacity admission already writes a durable, fenced provision operation, but a
new worker must be launched, enrolled in SPIRE, configured, joined to its
cluster, converged to expected state, and observed ready before that operation
can succeed. These steps cross PostgreSQL, AWS, SPIRE, Subd, and Kubernetes;
retries must resume the same host without creating a second lifecycle
authority.

## Decision

- The zone's registered Dominator claims provision operations and owns their
  execution lease. Each external step is idempotent or uses the persisted AWS
  client token. Fencing protects every durable lifecycle handoff.
- The AWS runtime launches from the immutable group template and a prepared
  deployment image. It uses the deployment's worker instance profile, public
  SPIRE bundle parameter, and private Subd endpoint. It does not grant workers
  control-plane AWS permissions.
- Dominator reconciles the exact AWS-IID-parented Subd SPIRE entry, configures
  Subd over SSM, supplies a short-lived Kubernetes bootstrap token over the
  pinned Subd SPIFFE mTLS identity, and removes the token after the join call.
- Dominator binds the Kubernetes Node to the catalog host by private address,
  verifies provider identity and scheduling metadata, applies expected state,
  and waits for Node, Cilium, and Subd readiness before marking the host READY
  and completing the operation.
- The operation, host, and fence are included in traces. Stage events identify
  launch, enrollment, and readiness; operation counters and duration record
  bounded outcomes; readiness waits have a dedicated counter. Logs contain
  stable IDs and generic step outcomes, never AWS/SSM output, join tokens, or
  raw provider errors.

Dominator Pods mount their Kubernetes service-account token. Dedicated RBAC grants
cluster-wide Node list/update and kube-system Pod list plus Secret create/delete
for short-lived join tokens. These permissions apply to both the initial platform
Dominator and additional zone Dominators; Crane API does not mount a token.

## Deployment scope

This feature is for new deployments initialized with the current bootstrap
workflow and its worker IAM profile and prepared image metadata. It does not
upgrade, migrate, or retrofit an existing deployment or database. Services
continue to require the explicitly initialized current schema at startup; no
startup schema mutation or database migration is introduced.

## Consequences

Provisioning remains AWS-specific behind the provider runtime contract while
capacity ownership and lifecycle transitions stay provider-neutral. A
transient failure leaves the operation retryable and its capacity reservation
held. Capacity is considered ready only after Kubernetes and Subd report the
same converged host; reducing a target does not itself release credits.
