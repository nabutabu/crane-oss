# ADR-012: Blocking Kubernetes operations through expected state

Status: Accepted

Crane API accepts typed administrator operations and routes them using trusted
catalog placement to the zone's Dominator. Dominator owns revision assignment
and delivers explicit expected state over the existing private SPIFFE mTLS
Subd protocol. Subd uses deployment-configured local Kubernetes credentials.
Requests block through enforcement and return sanitized final outcomes; no
background apply, outbound heartbeat, arbitrary manifest or shell endpoint is
introduced. The first operation creates or updates one Secret and optionally
restarts a same-namespace Deployment and awaits rollout readiness.

Failed operations may leave a verified Secret and an unsuccessful rollout.
Retry identity, revision fencing and a stable rollout marker make explicit
retries safe without promising rollback or exactly-once Kubernetes writes.
Secret contents stay out of central operation records and telemetry. Subd's
root-only durable expected-state file may contain Secret data, as does the
cluster's native Secret storage. Responses expose stage outcomes only.

This extends ADR-011's explicit-update resources. Kubernetes node readiness,
drain and eviction remain control-plane duties. Workload rollout readiness
here is part of the requested Secret operation, not a node readiness decision.
Only new deployments are supported; services never migrate existing databases.

Initial bootstrap marks the zone ready after core Kubernetes, Cilium, identity,
database and private Subd validation. Observability is a separate condition:
`configuration_pending`, `ready`, or `failed`. Missing backend configuration
must not prevent Crane API from starting, because its first administrative
request supplies that configuration. The request may optionally identify a
same-namespace Deployment to restart and await; observability requests explicitly
name `otel-collector`. Bootstrap preserves operator-owned backend Secrets and
prints an API-ready handoff with an encrypted private connection method.
Bootstrap's writer probe now uses invalid revision zero to verify authorization
without applying state or competing with Dominator's revision allocation.
