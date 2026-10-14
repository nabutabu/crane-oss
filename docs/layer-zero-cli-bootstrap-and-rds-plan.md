# Layer-Zero Bootstrap: CLI-Driven Progress + Automatic RDS

**Status:** Draft (no code changes yet) | **Owner:** TBD | **Related:** `docs/layer-zero-automation-plan.md`, `docs/implementation-plan.md` §5.1/§10

## 1. Summary

Today `cmd/crane-bootstrap` requires an already-reachable PostgreSQL DSN and only
initializes the Crane schema and seeds the first zone. It never provisions the
RDS instance it records the topology into, and layer-zero bootstrap progress is
modeled as a durable PostgreSQL state machine (`zone_bootstrap_operations` +
`capacity.BootstrapController`) that the first-zone CLI does not even drive.

This plan changes two things:

1. **Progress lives in the CLI, not the database.** The layer-zero bootstrap is
   an explicit, human-driven sequence owned by `crane-bootstrap`. Each step
   prints a phase line to the terminal. Recovery comes from re-running the
   command against idempotent, adoptable resources — not from a persisted phase
   row read back from Postgres.
2. **`crane-bootstrap` provisions or adopts RDS itself.** `ProvisionSpireDB`
   (generalized to `EnsurePostgres`) is wired into the CLI so a single command
   brings up the PostgreSQL instance, creates the `spire` and `crane` databases,
   installs the Crane schema, and seeds the first zone.

The end state is a one-command layer-zero bring-up from a developer machine:

```bash
crane-bootstrap -bootstrap-config bootstrap.json -config first-zone.json
```

## 2. Goals / Non-goals

### Goals
- Single CLI command provisions/adopts RDS, creates both databases, initializes
  the Crane schema, and seeds the first zone.
- All bootstrap phases are reported to stdout/stderr with clear success/failure
  per step; optional `--json` machine-readable mode.
- Every step is idempotent and adoptable so a crashed/interrupted run can simply
  be re-run safely.
- Keep a `-dsn` override so local development (Docker Postgres) still works
  without touching AWS.
- No database password or full connection string is ever logged or persisted.

### Non-goals
- No change to the capacity admission boundary, `SetTarget`, or credit model.
- No change to `crane-api` topology routes or the `/v1/first-zone` API.
- No Kubernetes control-plane bring-up (still out of scope for this plan).
- No migration/upgrade of earlier POC schemas (the first-go baseline stands).

## 3. Current state (evidence)

| Concern | Location | Today |
|---|---|---|
| CLI entry | `cmd/crane-bootstrap/main.go` | Requires `-dsn` + `-config`; validates `-bootstrap-config` but only uses it for `-dry-run`. Never calls AWS. |
| AWS bootstrap config | `internal/bootstrap/config.go` | `Config.Validate()` exists (version, region, VPC, subnets, SGs, RDS, SPIRE). Has an unused `first_zone json.RawMessage` field. |
| RDS creation | `internal/provider/awscompute/provider.go:319` (`ProvisionSpireDB`) | Creates/adopts SG + RDS, waits for availability, returns `DBConnectionInfo`. Hardcodes `dbName`, `dbUser`, `dbInstanceID`, subnet group, SG name, instance class. |
| RDS caller | `internal/execute/executor.go:122,152` | Legacy action executor calls it with hardcoded VPC/subnet/SG IDs. |
| Schema + seed | `internal/capacity/schema.go`, `internal/capacity/catalog.go:18` (`SeedFirstZone`) | `InitializeSchema` (repeatable) + `SeedFirstZone` (idempotent upsert by durable IDs). |
| DB-backed progress | `internal/capacity/bootstrap.go`, `internal/capacity/store.go:295` (`CreateZoneBootstrap`), `internal/capacity/catalog.go:163` (`BootstrapPhase`), `internal/capacity/schema.sql:105` (`zone_bootstrap_operations`) | Leased/fenced `create_zone` phase machine. Driven by the API path; **not** used by the first-zone CLI. |

## 4. Design decisions

### 4.1 CLI is the bootstrap progress authority
The layer-zero bootstrap sequence is owned and reported by the CLI process.

- Add a small, testable progress reporter in `internal/bootstrap` that emits one
  line per phase: `pending → running → ok/failed`, with a human-readable detail
  and optional `--json` output.
- Do **not** write phase rows. The database only ends up holding the final
  desired topology (`zones`, `clusters`, `team_budgets`, `node_pools`,
  `node_groups`) written atomically by `SeedFirstZone`.
- Recovery replaces resume: every phase is idempotent, so the operator re-runs
  the command. The DB never needs to answer "which phase was I in?".

### 4.2 Retire DB-backed progress for the layer-zero path
- Stop treating `zone_bootstrap_operations` / `operations(kind='create_zone')` /
  `capacity.BootstrapController` as the layer-zero progress authority.
- Keep the table and controller **only** if/when the API-driven subsequent-zone
  bootstrap is built (separate effort). This plan does not delete the schema; it
  removes the layer-zero dependency on it and records the intent in an ADR.
- `capacity.InitializeSchema` still creates the table (repeatable baseline) so
  the API path remains possible; the first-zone CLI simply never writes to it.

> **Trade-off:** we lose the durable phase/audit trail in Postgres. Mitigation:
> the CLI emits a complete ordered log and returns a non-zero exit code on the
> first failing phase. A future ADR can reintroduce durable *audit* events
> without reintroducing a resume state machine.

### 4.3 RDS creation via a generalized `ProvisionSpireDB`
- Rename/introduce `EnsurePostgres(ctx, spec PostgresSpec) (api.DBConnectionInfo, error)`
  on the provider, derived from `bootstrap.Config`. `ProvisionSpireDB` becomes a
  thin wrapper (or is removed once callers migrate).
- Remove package-level hardcoding (`dbName`, `dbUser`, `dbInstanceID`,
  `dbSubnetGroup`, `dbSGName`, `dbInstanceClass`, `spireServerSG`); every value
  comes from config.
- Make it fully idempotent/adoptable:
  - Describe SG by name+tag; create if absent; treat `InvalidPermission.Duplicate`
    as success (already partially handled at `provider.go:378`).
  - Treat `DBInstanceAlreadyExists` as adoption; verify engine/region/network/tags
    match; reuse the Secrets Manager secret ARN.
  - Wait for `available` with `ctx` cancellation (existing `waitForRDSAvailable`).
- After availability, create missing databases (`spire`, `crane`) by connecting
  to the `postgres` maintenance database.

### 4.4 Single-command orchestration
`crane-bootstrap` gains an internal orchestrator that runs ordered phases and
reports each one:

```text
[1/7] validate configuration ............... ok
[2/7] ensure RDS security group ............ ok (sg-0a365ae1dce045677)
[3/7] ensure RDS instance .................. ok (adopted spire-server-db)
[4/7] wait for RDS availability ............ ok (spire-server-db.<region>.rds.amazonaws.com:5432)
[5/7] ensure databases (spire, crane) ...... ok
[6/7] initialize Crane schema .............. ok
[7/7] seed first zone ...................... ok (zone us-west-2a, target_nodes=0)
first zone us-west-2a is ready; node group target remains zero
```

- `-dsn` provided → skip phases 2–5 and use the DSN (local/dev path).
- `-dsn` omitted and `-bootstrap-config` provided → run phases 2–5, derive the
  `crane` DSN from the resolved secret.
- `-dry-run` → validate both configs and print the planned phase list; no AWS or
  DB side effects.

## 5. CLI contract

| Flag | Behavior |
|---|---|
| `-config` | First-zone topology JSON (`zone`, `cluster`, `budget`, `pool`, `group`). |
| `-bootstrap-config` | Versioned layer-zero AWS JSON (`internal/bootstrap/config.go`). |
| `-dsn` | Optional explicit DSN; when set, AWS provisioning is skipped. |
| `-aws-region` | Override region from config (existing). |
| `-dry-run` | Validate only; print planned phases; no side effects. |
| `-operation-timeout` | Overall context deadline (RDS wait currently up to 20 min; keep default ≥ 30 min). |
| `-json` | New: emit one JSON object per phase for automation. |

If `-bootstrap-config` embeds the unused `first_zone` field, the CLI may use it
in place of `-config` (optional convenience; keep `-config` fully supported).

## 6. Config additions (`internal/bootstrap/config.go`)

`RDSConfig` currently carries identifier/engine/version/instance class/storage/
deletion protection. Add the fields the generalized provider needs:

- `RDS.MasterUsername` (default `postgres`)
- `RDS.Port` (default `5432`)
- `RDS.MultiAZ`, `RDS.StorageType`, `RDS.ManageMasterUserPassword`
- `RDS.Databases` (e.g. `["spire","crane"]`) or explicit `SpireDatabase`/`CraneDatabase`
- `Network.DBSubnetGroupName` (or derive from `private_subnet_ids`)
- `Network.RDBSecurityGroupName` (default `spire-rds-sg`)
- `Network.PostgresIngressSecurityGroupID` (the EC2 SG allowed to reach RDS)

Extend `Validate()` accordingly (ranges, non-empty, `deletion_protection == true`,
`storage_gib >= 20`), keeping the existing "secrets by reference only" rule.

## 7. Implementation tasks

| # | Task | Files |
|---|---|---|
| 1 | Add `bootstrap.Reporter` (phases, ok/fail, `--json`) with unit tests | `internal/bootstrap/progress.go` (+ `_test.go`) |
| 2 | Add `EnsurePostgres` + `PostgresSpec`; remove hardcoded RDS literals; keep `ProvisionSpireDB` wrapper | `internal/provider/provider.go`, `internal/provider/awscompute/provider.go` |
| 3 | Make RDS provisioning idempotent/adoptable (SG, instance, secret) | `internal/provider/awscompute/provider.go` |
| 4 | Add database-creation helper (`spire`, `crane`) over the admin connection | `internal/provider/awscompute/` (new file) |
| 5 | Extend `bootstrap.Config` + `Validate()` with the new fields | `internal/bootstrap/config.go` |
| 6 | Orchestrate phases in the CLI: validate → RDS → databases → schema → seed, with reporting; honor `-dsn`, `-dry-run`, `-json` | `cmd/crane-bootstrap/main.go` |
| 7 | Resolve Secrets Manager credentials → build `crane` DSN (`sslmode=require`); never log secrets | `cmd/crane-bootstrap/main.go`, provider helper |
| 8 | Migrate/retire `ProvisionSpireDB` callers in the legacy executor | `internal/execute/executor.go` |
| 9 | Document the decision to move layer-zero progress to CLI output | new `docs/decisions/adr-007-*.md`, `docs/decisions/README.md` |
| 10 | Update README runbook (`CRANE_DATABASE_URL`, one-command bring-up, `-dsn` dev path) | `README.md` |

## 8. Idempotency & recovery semantics

Because there is no persisted phase to resume from, **every phase must be safe to
repeat**:

- SG/instance/secret: describe-then-adopt; duplicates are success.
- Database creation: check `pg_database` before `CREATE DATABASE`.
- Schema: `InitializeSchema` is already repeatable (`pg_advisory_xact_lock`).
- Topology: `SeedFirstZone` already upserts by durable IDs.
- Re-run after partial failure re-executes from phase 1; earlier phases become
  no-ops. The CLI exits non-zero at the first failed phase with a clear message.

## 9. Testing

- **Unit:** reporter output (human + JSON); `bootstrap.Config.Validate()` for new
  fields; phase ordering and exit codes with an injected failing step.
- **Provider (mocked SDK/LocalStack):** SG adoption; `DBInstanceAlreadyExists`
  adoption; duplicate-ingress tolerance; secret reuse; database creation when
  `spire`/`crane` already exist; wait-for-availability cancellation.
- **Integration (local Postgres via `CRANE_TEST_DATABASE_URL`):** run the seed
  path end-to-end; assert schema tables exist and `SeedFirstZone` is idempotent
  across two runs; assert no `zone_bootstrap_operations` rows are written.
- **Dry-run:** asserts zero AWS/DB calls.

## 10. Risks / open questions

1. **Lost durable resume** — accepted; mitigated by strict idempotency. Confirm
   acceptable for layer zero.
2. **Secret handling** — ensure the generated DSN (with password) is never
   printed, logged, or persisted; scrub errors from the AWS SDK.
3. **Long RDS wait vs timeout** — RDS can take >20 min; keep `-operation-timeout`
   comfortably above the provider wait, and surface progress while waiting.
4. **Executor callers** — `internal/execute/executor.go` uses hardcoded IDs;
   decide whether to migrate it now (task 8) or leave it and only wrap the old
   method.
5. **Scope of the DB progress machine** — is `zone_bootstrap_operations` fully
   deprecated, or retained solely for a future API-driven subsequent-zone
   bootstrap? Recommendation: retain the table, remove the layer-zero dependency,
   and record the intent in ADR-007.
6. **Config source of truth** — should `first_zone` move into
   `bootstrap.json` (single file) or stay as a separate `-config` file? Default:
   support both, prefer `-config` for now.

## 11. Out of scope (future)

- API-driven subsequent-zone bootstrap and its (possibly durable) controller.
- SPIRE server / EC2 host provisioning and SSM validation (`docs/layer-zero-automation-plan.md`).
- Kubernetes control-plane bring-up and workload handoff.
- Durable audit events for bootstrap steps.
