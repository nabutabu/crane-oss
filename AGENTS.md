# AGENTS.md — crane-oss

Guidance for coding agents and humans working in this repository. Read this
before changing code. The repository is an evolving control plane, so verify
behavior in the tree and in the tests instead of relying on old plans or
comments.

## What this is

Crane OSS is a Go control plane for cloud infrastructure monitoring and host
remediation. It detects unhealthy hosts, records their lifecycle, makes
capacity and remediation decisions, provisions through cloud providers, and
coordinates host agents.

The main services are:

- `crane-api`: the control-plane HTTP API for health, topology, capacity, and
  bootstrap operations.
- `dominator`: a zone-side service that receives host heartbeats and returns
  desired host state. It uses SPIFFE mTLS for the `subd` client.
- `subd`: the host agent. It reports heartbeats and applies the desired state
  returned by Dominator.
- `crane-bootstrap`: a provider-neutral CLI for repeatable first-zone bootstrap;
  AWS is the sole current provider implementation.
- `crane-bootstrap-probe`: a small probe used to validate bootstrap behavior.

## Verify over trust

- Before changing a protocol, database schema, or bootstrap step, read the
  implementation and its tests, then check the relevant decision record under
  `docs/decisions/`.
- Do not claim a feature is complete because a type or package exists. Trace it
  through the service entrypoint and HTTP route, worker, or command that uses
  it.
- Treat `docs/implementation-plan.md` as design context. Confirm current status
  from the tree, tests, and wiring.
- Keep changes idempotent where they touch provisioning, schema setup,
  topology seeding, reconciliation, or retries. These paths may be rerun after
  partial failure.
- Do not put credentials, database passwords, raw request bodies, connection
  information, or provider secrets into logs, telemetry, test fixtures, or
  error messages. Preserve the redaction behavior in `internal/logging`.

## Architecture and ownership

```text
cmd/crane-api/       HTTP control-plane entrypoint
cmd/dominator/       zone-side heartbeat service entrypoint
cmd/subd/            host-agent entrypoint
cmd/crane-bootstrap/ provider-neutral operator bootstrap entrypoint

internal/activitymanager/  problem analysis and remediation decisions
internal/badhost/          checks, detection, and problem persistence
internal/bootstrap/        provider-neutral bootstrap orchestration, state, and progress
internal/capacity/         first-go schema, topology, admission, and stores
internal/config/           environment and database configuration
internal/dominator/        desired-state policy and heartbeat HTTP server
internal/execute/          action queue, workers, and provider execution
internal/hostcatalog/      host lifecycle service and PostgreSQL repository
internal/logging/          slog setup and secret redaction
internal/provider/         cloud-provider interface and AWS implementation
internal/provider/registry/ provider composition and bootstrap factory selection
internal/subd/             host heartbeat client and collectors
internal/telemetry/        OTLP traces and metrics with no-op fallback
pkg/api/                   shared public API and domain types
```

Authority boundaries are deliberate:

- Crane's shared architecture is vendor agnostic across bootstrap, runtime
  execution, and host management. AWS is the sole current implementation.
  Keep AWS-specific behavior in `internal/provider/awscompute` behind provider
  capabilities; see `docs/decisions/adr-009-vendor-agnostic-architecture.md`.
- Crane API owns global topology, placement, policy, and capacity admission.
- Dominator owns zone execution and desired host state.
- Subd owns host-local enforcement and reports the result by heartbeat.
- The capacity path is the lifecycle authority for hosts admitted through a
  capacity transaction. Legacy action-queue methods must reject or guard those
  hosts rather than creating a second lifecycle authority.
- Credits are capacity entitlements. Release them after actual termination and
  catalog confirmation, not merely when a desired target is reduced.
- Zone-specific node templates and topology labels must remain zone-specific;
  never advertise a cross-zone template as if it were local.

## Data and security invariants

- Database schema initialization is explicit and repeatable. Services check
  schema readiness and do not silently mutate the database at startup.
- Bootstrap resources are discovered using stable ownership and deployment
  identity, so retries converge on existing resources rather than creating
  duplicates.
- A transient provider or zone-discovery failure must not terminate healthy
  existing hosts. Mark a zone unavailable for new provisioning and reconcile
  later.
- Drain before termination when the host is reachable; respect the drain
  coordinator and Kubernetes disruption policy. Release capacity only after
  termination is confirmed.
- Host identity must come from trusted provider or workload identity. Do not
  authorize from arbitrary request fields or service names alone.
- Control-plane services use `log/slog` and the shared logging/telemetry
  packages. Keep request context and tracing context across capacity,
  provisioning, and remediation decisions.
- `internal/dominator` authenticates `subd` with SPIFFE mTLS and checks the
  expected identity. Preserve that boundary when changing heartbeat handling.

## Bootstrap and image reuse

- Keep bootstrap orchestration and `cmd/crane-bootstrap` vendor agnostic. Put
  all AWS-specific bootstrap behavior, including AWS API calls, resource
  reconciliation, image validation, remote execution, script templates,
  identity and endpoint details, and AWS configuration validation, under
  `internal/provider/awscompute`. Bootstrap must call provider capabilities
  through a provider-neutral contract. The AWS implementation is the current
  provider, not a reason to add AWS branches to the generic workflow.
- Keep provider-specific CLI options and config parsing with the provider
  implementation. Preserve retry, ownership, identity, and security invariants
  when moving existing AWS bootstrap code. See
  `docs/bootstrap-provider-refactor.md` for the refactor plan.
- Reuse the repository's golden-image generator and its setup scripts in
  bootstrap wherever possible. Before adding a bootstrap-specific artifact,
  installer, or distribution path, inspect `golden_image_generator/`, its
  Packer templates, and the relevant Git history. Do not reimplement image or
  host setup behavior in the bootstrap CLI merely for convenience.
- Do not upload locally rebuilt Crane binaries to an ad hoc S3 artifact bucket
  when the AMI, the existing image-generation flow, or the established GHCR/ECR
  release artifacts can provide them. Any exception must be justified in the
  relevant decision record.
- Bake the prepared private SPIRE Network Load Balancer DNS hostname into
  deployment AMIs; never bake an individual server IP. Preserve the NLB across
  backend maintenance. Do not bake agent state, private keys, SVIDs, or changing
  CA bundles into AMIs. Agents fetch their initial public bundle from the
  deployment's IAM-protected SSM parameter before starting; only the SPIRE host
  can publish that parameter. Do not introduce S3 for endpoint discovery or
  bundle delivery.
- A SPIFFE trust domain and CA/trust bundle are for authenticating workloads
  and authorizing mTLS peers (for example `subd` and Dominator); they are not
  for exposing the Kubernetes or Crane API to the public internet. Preserve
  the existing private-network and SPIFFE mTLS boundary when changing
  bootstrap or image creation.

## Go style and local verification

Use the standard Go toolchain and keep the module tidy. There is no third-party
linter in the repository; the style gate is the same set of checks used by CI:

```sh
gofmt -w $(find . -type f -name '*.go' -not -path './vendor/*')
gofmt -l .                         # must print nothing
go vet ./...
go mod tidy                        # update go.mod and go.sum when imports change
go build ./...
go test ./...
```

The CI workflow and integration tests require PostgreSQL and LocalStack for the
integration tests. The expected local test configuration is:

```sh
CRANE_TEST_DATABASE_URL='postgres://postgres:phase1-test@localhost:5432/crane_test?sslmode=disable' \
AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test \
AWS_REGION=us-east-1 AWS_DEFAULT_REGION=us-east-1 \
go test ./...
```

Use `gofmt -l .` and `go vet ./...` for a source-only check. Use
`go test -short ./...` for a quick iteration loop when slow tests are not needed.
Integration tests should use
the repository's PostgreSQL and LocalStack fixtures; do not point them at a
personal or production database.

Tests should exercise behavior at the lowest useful boundary. Store tests use
SQL mocks or a dedicated integration database, provider tests use fake
providers or LocalStack, and HTTP tests should verify authentication,
redaction, health behavior, and durable state transitions. Add tests for new
decision branches and retry/idempotency behavior.

## Important files and documentation

- `README.md`: user-facing setup, endpoints, configuration, and project map.
- `docs/invariants.md`: system invariants that changes must preserve.
- `docs/host_lifecycle.md`: host states, transitions, and lifecycle authority.
- `docs/auth.md`: API authentication expectations.
- `docs/aws-bootstrap.md`: AWS bootstrap prerequisites and recovery behavior.
- `docs/ci-oidc.md`: CI identity and optional ECR publishing.
- `docs/implementation-plan.md`: phased design context; verify status in code.
- `docs/decisions/`: architecture decision records.
- `.github/workflows/go.yml`: CI tidy, formatting, vet, build, and test gate.
- `.github/workflows/release.yml`: versioning, GoReleaser, and image release.

## Environment

Common control-plane settings include:

- `CRANE_DATABASE_URL`, or `DB_HOST`, `DB_PORT`, `DB_USER`, `DB_PASSWORD`,
  `DB_NAME`, `DB_SSLMODE`, and `DB_SSLROOTCERT` for PostgreSQL.
- `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_REGION`, and
  `AWS_DEFAULT_REGION` for AWS SDK operations and local tests.
- `CRANE_ADMIN_TOKEN` for topology and bootstrap administration.
- `CRANE_ADAPTER_TOKEN` for capacity admission.
- `SPIFFE_TRUST_DOMAIN` for Dominator/Subd identity validation.
- `LOG_LEVEL` and `LOG_FORMAT` for structured logging.
- `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_SERVICE_NAME`, and
  `OTEL_RESOURCE_ATTRIBUTES` for telemetry.

Do not commit real credentials or environment files containing them. Keep
deployment-specific values in the deployment system or local ignored files.

## Change checklist

Before handing off a change:

1. Check the affected package, entrypoint wiring, and relevant ADRs.
2. Run `gofmt` if Go files changed, then `gofmt -l .` and `go vet ./...`.
3. Run the applicable unit and integration tests; use the full command set above
   when the local services are available.
4. Review the diff for accidental secrets, generated files, schema drift, and
   changes outside the requested scope.
5. Update documentation when an endpoint, environment variable, lifecycle
   rule, or operator workflow changes.
