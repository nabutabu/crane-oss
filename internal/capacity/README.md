# Phase 1 capacity persistence

`capacity.InitializeSchema(ctx, db)` installs the canonical first-go PostgreSQL
schema from `schema.sql` in one transaction under an advisory lock. The explicit
`crane-bootstrap` command initializes the schema before seeding the first zone.
`crane-api` and Dominator only call `capacity.CheckSchema` at startup and fail
with instructions to run bootstrap when the schema is missing. The baseline is
repeatable on the dedicated, initially empty `crane` database; it does not keep a
version ledger or upgrade earlier POC schemas. It includes the host/action and
health tables required by the current control loops.

Platform configuration seeds `team_budgets`, `zones`, `clusters`, `node_pools`, and
`node_groups`. Memory entitlement is in MiB, CPU in integer vCPUs, and node count
in machines. Group launch parameters are version-pinned JSON (for example
`{"image_id":"ami-pinned","instance_type":"m7i.xlarge"}`). A group is admitted
only when its zone and cluster are ready, its pool is enabled, and its own state
is active. A cluster belongs to one zone, and groups cannot name a different
provider zone. The pool determines team identity; clients cannot choose it.

`Store.SetTarget` accepts a `pkg/api.CapacityRequest`. Its `ClusterID` must be
derived from authenticated adapter identity by the future API handler. One
transaction checks policy, locks the group and team budget, checks the target
revision, and commits the target, request outcome, per-host credit reservations,
host intents, and executable provisioning operations. `(group_id, request_id)`
deduplicates only retries of one handler. Changing any request field while
reusing the key returns `ErrReplayMismatch`; a new key with an old revision
returns `ErrConflict`. `Target` supports restart recovery and target refresh.

Budget checks include every unreleased reservation, including launched excess
capacity. Decreases cancel only pending, unclaimed provisioning intents. They
mark those hosts terminated and release their reservations atomically. Claimed
or launched work keeps its reservation and is marked excess when required;
registered/ready nodes cannot be removed by a target decrease. Explicit node
removal is separate, with exclusive host ownership, a drain deadline, and a
recorded preserve-capacity policy. Creation never releases credits.

Controllers claim persisted operations directly through `Claim`, `Renew`, and
`BeginLaunch`; they must not copy them into the legacy action queue. Legacy
lifecycle service mutations and drain/replacement actions reject capacity-managed
hosts, preserving one authority. Each claim increments a fencing generation.
Lease renewal, launch authorization, and result recording reject stale or expired
generations. A recovered provider launch uses
the same persisted ClientToken and immutable launch parameters. The cloud API
does not understand PostgreSQL fences: provider idempotency remains necessary
for a delayed cloud call after lease loss. `RecordLaunch` commits provider
identity and reservation state, leaving the host booting, never ready.

`CreateZoneBootstrap` and `BootstrapPhase` persist the layer-zero phase machine
and fenced handoff gate. `CreateRemoval` persists named-host drain ownership and,
for autoscaler deletion, a revision-checked target decrement. `Host` reads the
extended inventory; `Observe` records heartbeat freshness and observed version
without accepting placement or readiness. The bootstrap executor, drain and
termination executor, trusted readiness watcher, and authenticated adapter
transport are Phase 2 work. Launch JSON and bootstrap JSON must not contain
plaintext join tokens or cloud credentials.

Run the transaction tests against a disposable PostgreSQL database:

```sh
CRANE_TEST_DATABASE_URL='postgres://postgres:password@localhost:5432/crane_test?sslmode=disable' go test -count=1 ./internal/capacity
```

Each test creates and removes only its own randomly named schema. CI supplies
PostgreSQL 16 and runs these tests as part of `go test ./...`. Tests exercise
rollback at every write stage, concurrent replay/CAS, cross-group team budgets,
safe cancellation, retained credits, provider-token recovery, fencing,
bootstrap handoff, and exclusive removal ownership.
