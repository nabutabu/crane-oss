# crane-oss — Technical Review: CI/CD, Secrets, and Code Quality

**Status:** Pre-refinement review
**Audience:** Technical managers and staff engineers
**Scope:** Repository `github.com/nabutabu/crane-oss` (Go monorepo; binaries `crane-api`, `dominator`, `subd`)

## Purpose

This document catalogs concrete issues found during a full read-only review of the
repository. It is intended to drive a refinement pass before the project is presented
more broadly. Each item states **what** is wrong, **where** it is, and **why** it must be
fixed.

The project is known to be incomplete. Issues that are simply *unfinished work*
(no context propagation, unimplemented stubs, missing features) are **explicitly out of
scope** and are listed only in the appendix so they are not confused with defects. The
review is intentionally strict: the goal is a codebase that reads as deliberate and
production-minded to staff engineers.

## Severity legend

| Level | Meaning |
|-------|---------|
| **Critical** | Security exposure or data loss risk. Must be fixed before any external sharing. |
| **High** | Breaks the build/release path, causes incorrect behavior, or undermines trust in the pipeline. |
| **Medium** | Reliability, maintainability, or correctness issue that a reviewer will flag. |
| **Low** | Style, idioms, and polish. |

---

## Executive summary

The single most serious problem is that **real and placeholder secrets are committed to
the git repository**, including an AWS access key ID and database passwords. This is a
release blocker on its own: the repository cannot be shared or shown without first
rotating and purging those credentials.

The second major theme is that **CI does not verify anything meaningful**. CI builds the
binaries but never runs tests, `go vet`, or `gofmt`, so the pipeline is green while the
code currently fails `go vet` and formatting checks. The release workflow is also
functionally broken (it references an invalid GitHub Actions context), producing
unversioned artifacts.

The remaining issues are correctness and engineering-hygiene items that are visible to a
careful reviewer: panics on malformed input, data-layer queries that do not do what they
claim, silent error swallowing, and package/API design that does not follow Go idioms.

---

## 1. Secrets committed to git — CRITICAL

These are the highest-priority findings. Any secret that has ever been committed must be
treated as compromised, regardless of whether it is later deleted, because it remains in
git history.

### 1.1 AWS access key ID committed as `account_id_whitelist`

- **Location:** `spire/server/server.conf:19`
- **What:** `account_id_whitelist = ["AKIA…VFE7"]` (full value redacted). The value is formatted as an
  AWS **access key ID** (`AKIA...`), not an AWS account ID (a 12-digit number), and the
  file is tracked by git (`git ls-files` confirms it).
- **Why fix:** A real-looking AWS access key ID is in version control history. Even if the
  matching secret key is not present, an access key ID is a direct lead for an attacker and
  indicates credentials were pasted into a config file. This must be rotated and the file
  scrubbed from history. It also demonstrates a misunderstanding of what the whitelist
  field expects, which is a functional problem too.

### 1.2 Real plaintext AWS keypair on disk

- **Location:** `golden_image_generator/aws-credentials.auto.pkrvars.hcl:1-3`
- **What:** A full AWS `aws_access_key` / `aws_secret_key` pair and a private IP are stored
  in plaintext. The file is correctly gitignored (`.gitignore:1`) and is *not* tracked, but
  it exists on disk and holds long-lived credentials.
- **Why fix:** Long-lived static cloud credentials sitting in plaintext on a workstation are
  a breach waiting to happen. They should be moved to a secrets manager or short-lived
  assume-role/OIDC flow, and the exposed pair should be rotated and deleted.

### 1.3 Database password hardcoded in committed SPIRE config

- **Location:** `spire/server/server.conf:13` (`REDACTED_DB_PASSWORD`, with `sslmode=disable`),
  also documented in `README.md:44,137` and `cmd/dominator/README.md:29`.
- **Why fix:** Committed credentials plus disabled TLS verification is a textbook
  anti-pattern. Even for a local/dev default, presenting this to staff engineers reads as a
  security-ignorant codebase. Dev defaults should come from environment variables and the
  repo should contain a clearly-marked example, never a working secret.

### 1.4 Hardcoded secrets in `docker-compose.yml`

- **Location:** `docker-compose.yml:7,37,56` (`REDACTED_DB_PASSWORD`), `:58-59`
  (`AWS_ACCESS_KEY_ID=test` / `AWS_SECRET_ACCESS_KEY=test` for LocalStack), `:82`
  (`-joinToken <join_token>`).
- **Why fix:** The values are local/test-only, but committing credentials into compose
  normalizes a bad habit and trains contributors to copy the pattern to real environments.
  Compose should read from an untracked `.env` file.

### 1.5 Sample tokens committed

- **Location:** `appsettings.json:4` (`"Token": "REDACTED_TOKEN"`),
  `golden_image_generator/templates/subd-appsettings.json:3` (`"Token": "YOUR_HOST_TOKEN"`).
- **Why fix:** Placeholder values are acceptable *only if clearly named as examples*. The
  `"REDACTED_TOKEN"` value in a tracked config is ambiguous and should be replaced with an
  obvious placeholder plus documentation explaining that it is sourced from the
  environment. Host tokens authorize `subd`; the code currently builds token auth but never
  sends or checks it (see 6.4), which makes this config meaningless and dangerous.

### 1.6 Secrets leak into logs and a Kubernetes ConfigMap

- **Locations:**
  - DSN (contains DB password) logged on error: `cmd/crane-api/main.go:97-102`.
  - Raw request bodies logged: `internal/dominator/server.go:127,134`.
  - Connection info logged: `internal/execute/executor.go:114,152`.
  - DB password injected into a **ConfigMap** (not a Secret) by cloud-init:
    `internal/provider/awscompute/cloud-init.yaml:62,182`.
- **Why fix:** Secrets must never reach logs, and credentials must live in Kubernetes
  `Secret` objects, not `ConfigMap` objects that any `get configmap` RBAC holder can read.
  These are the same class of mistake as §1.1–1.4: secret handling is not treated as a
  first-class concern.

### 1.7 RDS created without encryption or backups

- **Location:** `internal/provider/awscompute/provider.go:422` (`StorageEncrypted: false`),
  `:432` (`BackupRetentionPeriod: 0`).
- **Why fix:** A reviewer will immediately question a data store provisioned unencrypted
  with no backups. For a system that stores host lifecycle state, this is a data-durability
  and compliance gap.

### 1.8 Recommended remediation for §1

1. Rotate **every** credential exposed above (AWS keys, DB password), and revoke the
   committed access key ID.
2. Purge the affected files from git history (`git filter-repo` / BFG), then force-push
   with coordination.
3. Add a secret-scanning gate (e.g. `gitleaks`/`trufflehog`) to CI so this cannot recur.
4. Move all runtime secrets to environment variables or a secrets manager; keep only
   obviously-fake examples in the repo.

---

## 2. CI/CD — CRITICAL / HIGH

### 2.1 CI never runs tests, vet, or formatting — the pipeline is decorative

- **Location:** `.github/workflows/go.yml:24-25` — the only step is `go build -v ./...`.
- **Why fix:** `go build` does not run `go vet` and does not execute tests. A green CI badge
  therefore guarantees almost nothing. This is the most damaging credibility issue after
  the committed secrets: any staff engineer who opens the workflow will see the project has
  no automated verification. Tests already exist in the repo but are never exercised.
- **Fix:** run, at minimum, `gofmt -l`, `go vet ./...`, and `go test ./...` (with
  `-race` where feasible). Fail the job on any of them.

### 2.2 Code currently fails `go vet` and `gofmt` — CI is green anyway

- **Locations:**
  - `internal/subd/runner.go:31,36` — `log.Printf` used with the error-wrapping directive
    `%w`, which `log` does not support.
  - `cmd/dominator/main.go:34` — `log.Fatal` called with a possible `Printf` format
    directive (`log.Fatal("Couldnt get X509 %v", err)`).
  - `gofmt -l` reports `internal/badhost/checks/catalog.go` and `internal/badhost/config.go`.
- **Why fix:** These are objective, tool-verifiable defects. Because CI does not run the
  tools (§2.1), the repository ships unformatted code and vet violations. Adding the checks
  in §2.1 will immediately surface these — they should be fixed in the same change.

### 2.3 Release workflow is broken — invalid GitHub context

- **Location:** `.github/workflows/release.yml:23-29,79-80`.
- **What:** Artifact names and the release tag use `${{ github.inputs.version }}`, which is
  not a valid GitHub Actions context. The correct form is `${{ inputs.version }}` (or
  `github.event.inputs.version`). The expression resolves to an empty string, so artifacts
  become `subd-v-linux-amd64`, the tag becomes `v`, and the release name becomes `Release v`.
- **Why fix:** The advertised release process produces malformed, unversioned artifacts.
  Any reviewer tracing "how do we release?" will find a path that does not work.

### 2.4 No version injection into binaries

- **Locations:** `.github/workflows/release.yml`, `Dockerfile.crane-api:10`,
  `Dockerfile.dominator:10` — all use plain `go build` with no `-ldflags`.
- **Why fix:** There is no way to tell which commit or version a running binary came from.
  Production Go projects inject `-X main.version/commit/date` via `-ldflags`. Without this,
  operations cannot correlate a deployed binary to source.

### 2.5 No test gate before publishing containers

- **Location:** `.github/workflows/deploy-ecr.yml:28-38` — builds and pushes images with no
  prior test/build verification.
- **Why fix:** A broken commit can be published straight to ECR. Publishing should depend on
  the same verification job used by CI.

### 2.6 Container tags are mutable `:latest` only

- **Location:** `.github/workflows/deploy-ecr.yml:28-30,36-38`.
- **Why fix:** `:latest` is overwritten on every run, so there is no immutable artifact to
  roll back to or to reference per environment. Tag with the git SHA and semantic version,
  and consider digest pinning.

### 2.7 Long-lived AWS keys instead of OIDC

- **Location:** `.github/workflows/deploy-ecr.yml:16-18` — uses
  `secrets.AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY`.
- **Why fix:** Storing long-lived cloud keys as repo secrets is the same risk class as §1.
  GitHub OIDC federation to an IAM role removes the static secret entirely and is the
  current best practice.

### 2.8 No linting, dependency hygiene, or security automation

- **What:** No `golangci-lint` config, no Dependabot/renovate, no CodeQL, no image
  scanning, no SBOM, no artifact signing.
- **Why fix:** At the level this project is being presented, their absence is noticed.
  These are low-effort additions that signal maturity.

### 2.9 Deployment is fully manual and undocumented as a pipeline

- **What:** There is no automated path from commit to running artifact. Binary releases,
  ECR pushes, and Packer AMI builds are all `workflow_dispatch`/manual, and hosts must be
  updated out-of-band. There is no Helm/GitOps/manifests for `crane-api` or `dominator`.
- **Why fix:** "What does a deployment look like?" currently has no defensible answer. Even
  a documented manual runbook plus a scripted deploy job is a large credibility improvement
  over an implicit process.

### 2.10 Docker images are not built to reproducible/production standards

- **Locations:** `Dockerfile.crane-api`, `Dockerfile.dominator`.
- **What:** No `-trimpath`, no `-buildvcs`, base images use mutable tags
  (`golang:1.25-alpine`, `alpine:latest`) rather than digests, the runtime image runs as
  **root** (no `USER`), and there is no `HEALTHCHECK`. Only `linux/amd64` is produced even
  though the deployment targets are ARM (`t4g.micro`).
- **Why fix:** Reproducibility, least-privilege, and correctness of the target architecture
  are baseline expectations. Running as root and shipping the wrong architecture are both
  immediate review blockers.

### 2.11 Go version and module hygiene drift

- **What:** `go.mod` declares `go 1.25.3`, Dockerfiles use `1.25`, README says "Go 1.21+".
  All dependencies in `go.mod` are marked `// indirect` (including direct ones), and
  `go mod tidy -diff` shows unused dependencies still present (e.g. `secretsmanager`,
  `bmizerany/pq`) plus stale `go.sum` entries.
- **Why fix:** Inconsistent toolchains and an untidy `go.mod` are the first things a staff
  engineer notices. Running `go mod tidy` and pinning one Go version is trivial and
  high-signal.

---

## 3. Correctness bugs — HIGH

These are defects, not "unfinished work". They produce wrong behavior today and will be
found by any reviewer reading the logic.

### 3.1 Failed actions are also marked done

- **Location:** `internal/execute/worker.go:41-49`.
- **What:** On execute failure the code calls `MarkFailed` (ignoring its error) and then
  falls through to `MarkDone` with no `return`/`continue`.
- **Why fix:** Failed remediations are recorded as successful, corrupting state and making
  the system's behavior untrustworthy. This is a silent data-correctness bug.

### 3.2 Every detected problem escalates to host replacement

- **Location:** `internal/activitymanager/policy.go:44`.
- **What:** `EvaluateEscalation` unconditionally returns `EscalationReplace`; all window and
  escalation logic above it is dead code. The README and architecture docs describe a
  graduated escalation policy that the code does not implement.
- **Why fix:** The system destroys/replaces hosts on every problem regardless of severity —
  the opposite of the documented design. This is a high-impact behavioral defect.

### 3.3 mTLS heartbeat handler can panic on malformed input

- **Location:** `internal/dominator/server.go:100` — `cert.URIs[0].String()` with no length
  check; `:114-118` and `:139-143` call `http.Error(...500)` **without returning**, so
  execution continues and can dereference a nil `host`.
- **Why fix:** A malformed or empty SPIFFE URI from a client can crash the server. Missing
  `return` after `http.Error` also causes superfluous writes and wrong status codes. This is
  remotely triggerable and must be fixed.

### 3.4 Data-layer queries do not do what they claim

- **Locations:**
  - `internal/hostcatalog/store/repository.go:169-173` — `GetByZone` ignores the `zone`
    argument (no `WHERE`), returning all hosts.
  - `internal/hostcatalog/store/repository.go:302-304` — `GetByToken` filters on a `token`
    column that does not exist in the schema.
  - `internal/badhost/problem/problem_store.go:46-50` — `SELECT` with a `GROUP BY host_id`
    that is invalid in PostgreSQL (non-aggregated columns).
- **Why fix:** These are not stubs — they look implemented but are broken. `GetByZone`
  returning everything is a correctness and potentially isolation issue.

### 3.5 SPIFFE trust-domain mismatch breaks mTLS

- **Locations:** Code authorizes `spiffe://crane-oss/...` (`internal/dominator/server.go:47`,
  `internal/subd/client.go:30`), while every config and doc uses `crane.internal`
  (`spire/server/server.conf:4`, `spire/agent/agent.conf:3`, `docs/auth.md:29`, etc.).
- **Why fix:** Authentication would fail in any real deployment. It also shows
  config/code drift that a reviewer will question.

### 3.6 `subd` collects actions then discards all errors

- **Location:** `internal/subd/services_collector.go:114` — `PerformActions` returns `nil`
  unconditionally even though it accumulates errors.
- **Why fix:** Failures are silently reported as success, the same failure mode as §3.1.

### 3.7 Unsafe LRU cache (leak, double-insert, panic)

- **Location:** `internal/activitymanager/problemcache/lr_cache.go:48,101-115`.
- **What:** `NewCache` starts a cleanup goroutine with no stop/context (leaks one goroutine
  per instance); `Record` can re-insert a node twice; zero capacity panics on
  `removeNode(lrc.LR.Next)`. The tests themselves document the panic (`lr_cache_test.go:289-301`).
- **Why fix:** A cache that leaks and panics is a reliability liability, and shipping a test
  that asserts broken behavior is a red flag.

### 3.8 Unchecked slice indexing on external/untrusted data

- **Locations:** `internal/provider/awscompute/provider.go:493,547` (and `:284,314`);
  `internal/subd/packages_collector.go:35-37`; `internal/subd/services_collector.go:43-44`.
- **Why fix:** Indexing `[0]` on API responses and parsed command output without a length
  check panics on empty/malformed data. This is a robustness defect a reviewer will flag.

### 3.9 Deregistration delay stored as a tag, not an attribute

- **Location:** `internal/provider/awscompute/provider.go:277`.
- **Why fix:** The value is written as an ELBv2 *tag* instead of the target-group
  `DeregistrationDelay.TimeoutSeconds` attribute, so the setting has no effect. It is a
  subtle functional bug.

---

## 4. Error handling and reliability — HIGH / MEDIUM

### 4.1 `log.Fatal` in library/execution paths

- **Locations:** `internal/execute/executor.go:94,111,143`;
  `internal/dominator/server.go:38,56-59`.
- **Why fix:** Calling `log.Fatal`/`Fatalf` inside reusable packages kills the whole process
  on a recoverable error and makes the code untestable. Only `main` should decide to exit.

### 4.2 Startup continues after fatal init errors

- **Locations:** `cmd/crane-api/main.go:99-102` (DB open error only logged),
  `:137-145` (config load and `BuildChecks` errors logged, then possibly-nil values are
  used); `cmd/dominator/main.go:43-46`.
- **Why fix:** The service can start in a broken state (nil DB, nil checks) and fail later
  in confusing ways. Fail fast on startup.

### 4.3 Errors are logged and dropped throughout the control loops

- **Locations:** `internal/badhost/detector.go:50-53,62-65`;
  `internal/activitymanager/manager.go:51-55,87-90`;
  `internal/dominator/server.go:85-86`; `internal/subd/client.go:52-55,63-73`.
- **Why fix:** Swallowing errors makes failures invisible and untraceable. At minimum,
  errors that abort a logical operation should be returned/wrapped, not just printed.

### 4.4 Response bodies and resources not closed

- **Location:** `internal/subd/client.go:63-73` — `response.Body` is never closed in
  `Heartbeat`.
- **Why fix:** Leaks connections/file descriptors under load. A classic Go review comment.

### 4.5 Missing `rows.Err()` checks

- **Locations:** `internal/badhost/problem/problem_store.go:59,99`;
  `internal/hostcatalog/store/repository.go:180,233`.
- **Why fix:** Iteration errors after a query are silently ignored, masking partial/failed
  reads.

### 4.6 Non-graceful shutdown in some loops

- **Locations:** `internal/execute/worker.go:27`, `internal/badhost/detector.go:36`,
  `internal/activitymanager/manager.go:37` — `for range ticker.C` without a
  `select { case <-ctx.Done(): }`.
- **Why fix:** These workers cannot stop cleanly, which complicates tests and deployment.

---

## 5. Security (non-secret) — HIGH / MEDIUM

### 5.1 Host-state HTTP handlers are unauthenticated and unused

- **Location:** `internal/hostcatalog/http/handlers.go` (whole package).
- **Why fix:** The package exposes state transitions with no authentication and is never
  registered in a mux (dead code). Shipping an exported, unauthenticated state-mutation API
  — even if currently unwired — is a liability and will be questioned.

### 5.2 Token authentication is built but never enforced

- **Location:** `internal/subd/client.go:22-27,29` — `Token` is stored but never sent, and
  `hostID` is never used.
- **Why fix:** The configuration implies authentication that does not actually happen,
  giving a false sense of security and leaving `subd` endpoints effectively open.

### 5.3 Disabled TLS verification and weak transport

- **Location:** `spire/server/server.conf:13` (`sslmode=disable`); cloud-init RDS config
  uses `sslmode=require` (encryption without certificate verification).
- **Why fix:** Consistent, verified TLS is expected; `disable` in a committed config is a
  clear red flag.

---

## 6. API design and Go idioms — MEDIUM / LOW

These do not break behavior but are exactly what staff engineers use to judge whether a
codebase was written with intent.

### 6.1 Package named `http`

- **Location:** `internal/hostcatalog/http/handlers.go:1` — package name `http` shadows
  `net/http`, which the same file imports.
- **Fix:** Rename to something like `handlers` or `httpapi`.

### 6.2 Non-idiomatic package directory names

- **Locations:** `internal/badhost/checks/hostStore_checks/`,
  `internal/badhost/checks/aws_checks/`.
- **Fix:** Go package names should be lowercase with no underscores and no mixed case
  (`hoststore`, `awschecks`).

### 6.3 `pkg/api` types are unusable due to unexported fields

- **Location:** `pkg/api/types.go:24-41` — `Capacity` and `Fleet` have only unexported
  fields, so they can never be constructed or read outside the package. Field `Host_id`
  uses snake_case and JSON tags are inconsistent.
- **Fix:** Export fields or provide constructors/accessors; use `HostID` and consistent tags.

### 6.4 Error strings are capitalized and unwrapped

- **Location:** `internal/hostcatalog/service/service.go:49,55`
  (`errors.New("Host not found")`, `errors.New("Not a valid next state")`).
- **Fix:** Go error strings should be lowercase and wrapped with `%w` to preserve causes for
  `errors.Is`/`errors.As`.

### 6.5 Exported identifiers lack doc comments

- **Locations:** Most of `internal/*` and `pkg/api` (e.g. `internal/execute/action.go`,
  `internal/hostcatalog/store/repository.go`, `pkg/api/types.go`).
- **Fix:** Every exported type/function needs a doc comment starting with its name. This is
  the cheapest, highest-visibility polish item.

### 6.6 Duplicated and copy-pasted code

- **Locations:** scan logic repeated across `repository.go` methods
  (`GetByID`/`GetByProviderID`/`GetByZone`/`ListHosts`/`GetByToken`, lines ~43-347).
- **Fix:** Extract a shared scan helper. Repetition invites the drift seen in §3.4.

### 6.7 Shadowing and import smells

- **Locations:** `internal/badhost/detector.go:61` — loop variable `problem` shadows the
  imported `problem` package; `internal/provider/awscompute/provider.go:16-17` imports the
  same EC2 types package twice under two names.
- **Fix:** Rename the loop variable; drop the duplicate import.

### 6.8 Parse-by-string-split for SPIFFE identity

- **Location:** `internal/dominator/server.go:100-104` splits the SPIFFE ID on `/` and takes
  the last element.
- **Fix:** Parse the URI properly; a trailing slash yields an empty host ID.

---

## 7. Appendix

### 7.1 Recommended remediation order

1. **Rotate and purge all secrets (§1).** Nothing else matters until this is done.
2. **Make CI real (§2.1–2.2):** `gofmt`, `go vet`, `go test`; fix the violations it exposes.
3. **Repair the release workflow (§2.3–2.4)** and add version injection.
4. **Fix correctness bugs (§3):** failing-as-done, escalation, panic handling, broken
   queries.
5. **Harden the pipeline (§2.5–2.11):** test gate, immutable tags, OIDC, reproducible
   images, `go mod tidy`.
6. **Polish API/idioms (§6)** for presentation.

### 7.2 Explicitly out of scope (known incomplete work)

The following were observed but are considered intentional work-in-progress and are **not**
treated as defects in this review:

- Missing context propagation in the DB layer.
- Unimplemented stubs such as `DrainHost` and `GetInstanceStatus`.
- Unused/dead packages that exist as scaffolding (e.g. `internal/provider/noop_provider.go`,
  `internal/provider/fake.go`, and the two-provider-interface mismatch).
- Missing tests in several packages (noting only that CI should eventually enforce
  coverage once the feature set stabilizes).
- Hardcoded infrastructure identifiers used during bring-up.

### 7.3 Note on scope

This review was performed read-only; no code was modified. Line numbers refer to the state
of the repository at the time of review and may shift after edits.
