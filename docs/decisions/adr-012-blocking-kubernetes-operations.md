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
