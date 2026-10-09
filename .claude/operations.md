# Operations

The library has no process, server or configuration of its own; "operations" here means what the library guarantees, what the consuming worker must provide, and how the repository is built, tested and released.

## 1. Security

### 1.1 Trust model (`SECURITY.md` § Trust model)

- **Credentials arrive resolved.** The worker resolves every `secret_ref` field (`registry.Field.IsSecretRef()`) from OpenBao for the job's own tenant and passes the value in `Execute`'s input. The library never sees an OpenBao path.
- **Credentials live only in memory**, in the storage / send-email client caches, keyed by a SHA-256 of every credential field (plus bucket or sender), capped at 256 entries. A retired client is closed once its last call finishes. Credentials are never logged, returned in outputs or errors, or persisted.
- **Internal calls carry caller identity:** `x-internal-token` (`Config.InternalToken`, required by `New`) and `x-departments` (from `WithDepartments`); missing or malformed departments fail before any request.

### 1.2 Guards in the code

| Threat | Guard |
|---|---|
| Request forgery via aliases | alias-only (never a raw URL); `aliasconfig.Validate` (`baseURL` http/https, host, no userinfo; path starts with `/`); `shared.NewInternalRequest` pins scheme + host per request; path params escaped and never `""`/`.`/`..`; query params cannot override template keys |
| Header forwarding on redirect | `InternalHTTPClient` and `ProviderHTTPClient` never follow redirects (also under the Gmail, Graph, Drive OAuth transports and the GCS client) |
| HTTP/2 silent replay | provider and internal clients are HTTP/1.1 only (a caller `RoundTripper` that is not `*http.Transport` is used as-is and must not enable HTTP/2) |
| SSRF through tenant credentials | `ValidateGoogleServiceAccountKey` (Google token endpoint and universe only); Azure account / container patterns; Entra `tenantId` GUID-or-domain pattern |
| Worker environment leaking into tenant clients | S3 and SES clients built only from the tenant's credentials and region (no `config.LoadDefaultConfig`) |
| Email recipient / header injection | `senderEmail` / `receiverEmail` must be single bare addresses; Gmail MIME quotes and encodes display names and strips CR/LF; SES encodes the From name |
| Duplicate emails | SES `aws.NopRetryer`; SendGrid requests built per call (the SDK's shared client mixed concurrent bodies); write tracing for outcome classification; send intents |
| Cross-tenant document access | refs scoped by tenant in Valkey; exact `<prefix><tenant>/<uuid>` key check and bucket check before any S3 call; size + SHA-256 verified on every read |
| Unbounded memory | 50 MiB objects, 1 MiB inline, 10 MiB responses, 25 MiB attachments (`ReadAllLimited` holds at most limit + 1 bytes) |
| Session cookies in workflow data | `rest-call` drops `Set-Cookie` from returned headers |
| Direct database access | `depguard` `pgcommon-only`; arch-lint grants pgcommon only to the two `sqlstore` packages |

### 1.3 Supply chain

Every GitHub Action is pinned to a full commit SHA with a version comment (`validate-quality.yml` fails on an unpinned `uses:`); workflow files may not contain HTML-escaped operators; compose images are pinned by digest; `govulncheck` (v1.1.4, pinned) runs on `./pkg/...`; Trivy scans `go.mod` / `go.sum` (`skip-dirs: tools`) for CRITICAL / HIGH / UNKNOWN; a CycloneDX SBOM is produced in CI and attached to releases.

## 2. Configuration

**The library reads no environment variables.** The consumer reads its configuration and passes objects in (`connectors.Config`, `*pgcommon.Pool`, `*redis.Client` / `*redis.ClusterClient`, `*s3.Client`, `aliasconfig.Config`). The worker-side variables (pgcommon's `DATABASE_URL` / `PG_*`, `PG_BOUNCER_MODE`, `PG_STATEMENT_TIMEOUT`, `PG_LOCK_TIMEOUT`, `MIGRATION_DATABASE_URL`, Valkey and bucket settings, the internal token, the alias file) are tabulated in [`docs/integration/connector-worker.md`](../docs/integration/connector-worker.md) § 3.

Library defaults the worker can rely on: `docref.DefaultTTL` 24 h, `valkeystore.DefaultWaitAOFTimeout` 2 s (clamped 250 ms under the client read timeout, minimum 100 ms), `documents.DefaultLease` 15 min (keep the per-call timeout below it), `sendintent.StalePendingAfter` 15 min, `shared.DefaultHTTPTimeout` 30 s (internal call without an alias timeout; OAuth token requests), `shared.ProviderHTTPTimeout` 2 min, client cache limit 256.

**Local environment** (`.env-example` → `.env` via `make setup`; the Makefile `-include`s it and docker compose reads it):

| Variable | Purpose |
|---|---|
| `GOPRIVATE`, `GONOSUMDB` | `github.com/BCBP-SOLUTIONS-FZC-LLC/*` (SSH access to platform-pgcommon) |
| `WC_PG_PORT` 55432, `WC_PGBOUNCER_PORT` 55433, `WC_VALKEY_PORT` 56379, `WC_FLOCI_PORT` 4580 | compose host ports (chosen not to collide with the IAM stacks); the Makefile derives `INT_ENV` from them |
| `TEST_POSTGRES_DSN`, `TEST_PGBOUNCER_DSN`, `TEST_VALKEY_ADDR`, `TEST_S3_ENDPOINT`, `TEST_VALKEY_DOCKER` | read by the tagged tests; the Make targets set them from `WC_*` |
| `CI` | unset locally; when set, a missing `TEST_*` variable fails the test instead of skipping it |
| `DATABASE_URL`, `PG_*`, `PG_BOUNCER_MODE=true`, `PG_STATEMENT_TIMEOUT=5s`, `PG_LOCK_TIMEOUT=2s`, `PG_POOL_NAME`, `MIGRATION_DATABASE_URL` | pgcommon's variables pointed at the compose stack, as a worker would run (not read by the library) |

## 3. CI/CD and Release

### 3.1 Workflows (`.github/workflows/`)

| Workflow | Trigger | Does |
|---|---|---|
| `ci.yml` | push to `main`, PRs to `main`, manual | `changes` (`detect-changes.sh`: docs-only → skip; plus the draft / `skip-ci` gate) → `validate-test` ∥ `validate-quality` (both always called, with `skip`, so the required checks report) ∥ `api-compat` (apidiff vs the last stable tag, warning only) → `trivy` (table gate + SARIF + CycloneDX SBOM artifact) → `pr-summary` (`.github/scripts/pr-summary.js`). `build-image-cache`, `lint-dockerfile` and `smoke-tests` are documented **no-op placeholders** for the org branch-protection ruleset. Concurrency: superseded PR runs cancel; every push to `main` runs. |
| `validate-test.yml` (reusable; job `test`) | from `ci.yml` / `release.yml` (`skip`, `ref` inputs; `GO_PRIVATE_TOKEN`, `CI_REPO_READ_TOKEN` passed explicitly) | `docker compose up -d --wait` → private module access → `go mod verify` → `make test-ci` → coverage gate (`COVERAGE_THRESHOLD: '98'`, `.github/scripts/coverage-gate.sh`) → compose logs on failure → upload `coverage.out` → `make build`. Sets `TEST_*` env to the compose ports; `CI=true` makes infrastructure tests mandatory. |
| `validate-quality.yml` (reusable; job `quality`) | same | `make mod-verify`, HTML-entity check, SHA-pinned actions check, gofmt, `make tidy-check`, `make vet`, `make lint`, `make arch-lint`, `make docs-check`, `make ci-scripts-test`, `make vuln-check` |
| `docs.yml` | `ARCHITECTURE.md`, `docs/architecture/**` | `python3 scripts/docs_check.py` (runs on docs-only changes, which `ci.yml` skips) |
| `changelog-check.yml` | PRs touching `pkg/**`, `go.mod`, `go.sum` | fails unless `CHANGELOG.md` changed in the PR |
| `release.yml` | tags `v*.*.*` / `v*.*.*-*`, manual (`tag`) | `verify` (`verify-release-tag.sh`: dispatch only from `main` or the tag, tag on HEAD, on `origin/main`, major matches `/vN`; `verify-changelog-entry.sh` via `changelog-section.sh`) → `validate-test` ∥ `validate-quality` ∥ `api-compat` (**blocking**: `make api-compat API_NEW_VERSION=<tag>`) → `sbom` (Trivy gate + SARIF + SBOM) → `publish` (`extract-release-notes.sh`, `release-checksums.sh`: source archive + `checksums.txt`, Cosign `sign-blob` + `verify-blob`, `create-github-release.sh`; `-` in the tag → prerelease) |

Required checks: "Validate / Test / test" and "Validate / Quality / quality" (job names must not change). PRs must come from branches of this repository, not forks (forks do not receive `GO_PRIVATE_TOKEN`). `.githooks/pre-commit` (installed by `make setup` / `make install-hooks`) runs `make tidy-check`, `make fmt-check`, `make lint`.

### 3.2 Release state

v1.0.0 is tagged (2026-10-09; also `v0.1.0-beta.1`). v2.0.0 is **not tagged**: everything since is in `CHANGELOG.md` `[Unreleased]`, already on the `/v2` module path. Process (`VERSIONING.md`): merge to `main` → move `[Unreleased]` into `## [X.Y.Z] - YYYY-MM-DD` → `make ci` → `git tag -a vX.Y.Z -m "vX.Y.Z" && git push origin vX.Y.Z` → notify `definition_service` and `execution_service` owners for MINOR / MAJOR. Consumers: `go get github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2@vX.Y.Z` with `GOPRIVATE` set. There is no image, binary or Cosign signature.

## 4. Testing Strategy

All tests are in the root module. `make test-unit` needs no Docker; everything tagged `integration` needs the compose stack.

| Suite | Packages (Makefile var) | Tag | Needs | Covers |
|---|---|---|---|---|
| unit | `./test/unit/...` (`TEST_UNIT_PKGS`) + `./pkg/...` (`TEST_INTERNAL_PKGS`, white-box) | none | nothing | per-package black-box tests (`test/unit/<package>/`), `retry_matrix_test.go`, `worker_scenarios_test.go`, `regression_test.go` (`TestREG01`–`TestREG10`), adapters against test servers, Valkey against a scripted RESP server, googledrive's in-memory registry leg |
| postgres | `./test/postgres/...` + `./pkg/connectors/storage/googledrive/...` (`TEST_POSTGRES_PKGS`) | `integration` | PostgreSQL direct + PgBouncer | store contract suites, concurrency (exactly-one-winner), timeouts, classification, lost reservation replies (`test/postgres/sendemail/reservation_test.go`), concurrent migrations and the stores through PgBouncer (`test/postgres/postgres_test.go`, `TestPG01`–`TestPG03`), googledrive's PostgreSQL leg (`stores_postgres_test.go`) |
| integration | `./test/integration/...` (`TEST_INT_PKGS`) | `integration` | Valkey, floci S3, PostgreSQL via PgBouncer, Docker for restarts | workflow scenarios across replicas (`TestIT01_…` onward), `multireplica` (`TestMultiReplica_*`), `s3content`, `valkeystore` (durability, idempotent lost-reply retry, `TestACL_DocumentedRulesSuffice`, restarts with `TEST_VALKEY_DOCKER=1`) |

- **Compose stack** (`docker-compose.yml`): `postgres:17-alpine` (`max_connections=300`, db `connectors`), `edoburu/pgbouncer` v1.25.2 (transaction mode), `valkey/valkey:8-alpine` (`--appendonly yes --appendfsync always --maxmemory-policy noeviction`, 256 MB), `floci/floci:2.1.0-compat` (S3; `scripts/init-floci.sh` creates `workflow-connectors-documents` with the `docrefs/` 2-day rule; healthy only once the lifecycle rule exists). All digest-pinned. `make docker-up` = `docker compose up -d --wait`.
- **Skip vs fail:** without the `TEST_*` variable a tagged test skips; with `CI` set it fails ("cannot be skipped in CI").
- **Parallelism:** `make test` / `test-ci` / `race` run the three suites with `make -j3`; the postgres suite uses `-parallel $(TEST_POSTGRES_PARALLEL)` (default 4) and prints a failing-tests summary from `.coverage/postgres.raw`. Store tests isolate by tenant or a throwaway database / schema.
- **Coverage:** `-coverpkg=$(COVER_PKG_LIST)` (all `./pkg/...`) per suite → `.coverage/*.out` → `scripts/merge_coverage.py` (max count) → `coverage.out`. Gate 98%; current 99.96%.
- **Libraries:** tests use `testify` (`assert`, `require`); database access in tests goes through pgcommon too (`depguard`).
- **`vet` / `lint`** run twice: default build and `-tags=integration` / `--build-tags=integration`, so tagged files are checked.

## 5. Runbooks

| Runbook | Covers |
|---|---|
| [`docs/runbooks/document-refs.md`](../docs/runbooks/document-refs.md) | the S3 + Valkey model, bucket settings (IAM incl. `s3:ListBucket`, SSE-KMS, lifecycle 2 days), Valkey settings, sizing, the exact worker `ACL SETUSER` line, retention, backup / restore, failover (`WaitReplicas`), monitoring signals |
| [`docs/runbooks/email-delivery.md`](../docs/runbooks/email-delivery.md) | delivery guarantees, the three outcomes, handling `unknown` and `not_delivered`, `messageKey` / `resend` / `resendAttempt` |
| [`docs/runbooks/retry-semantics.md`](../docs/runbooks/retry-semantics.md) | what recovers by itself, `DecideRetry`, worker obligations, per-provider class mappings |
| [`docs/integration/connector-worker.md`](../docs/integration/connector-worker.md) § 7 | credential rotation (`ResetClients`), `PruneAttempts` schedule, send-intent retention, shutdown, Valkey failover |

## 6. Dependency Degradation Matrix

| Dependency down / degraded | Affected | Behaviour | Class |
|---|---|---|---|
| PostgreSQL / PgBouncer unreachable, timeout, deadlock, too many connections | Drive storage ops; send-email with `messageKey` | Drive call fails before touching Drive; send-email: Reserve error with no recoverable reservation → `not_delivered`, nothing sent | transient |
| pgcommon pool closed (process shutting down) | same | fails | unknown (left for redelivery) |
| PostgreSQL lost after the send | send-email `Record` | success still reported; `sendIntentWarning`; a failed send gets `IntentUnrecorded` | success / unknown |
| Valkey unreachable, `OOM`, failover / topology replies, `NOREPLICAS` | `createDocument`, ref uploads, attachments | ref not created / not resolved; attachments fail before sending (`not_delivered`) | transient (`ErrUnavailable` / network) |
| Valkey AOF not confirmed in `WaitAOFTimeout` | ref creation | object removed, `ErrNotPersisted` | transient |
| Valkey misconfigured (no AOF, eviction policy) | worker startup | `CheckDurability` returns `*DurabilityError`; worker must refuse to start | — |
| Valkey primary failover (`WaitReplicas` 0) | refs acknowledged just before | ref lost → consumer gets `ErrNotFound` | permanent (runbook: re-run producing step) |
| S3 document bucket unreachable / throttled | ref create / resolve | create fails (no metadata written); resolve fails | by cause (5xx / throttling / network transient) |
| Document object deleted or altered | ref resolve | `ErrSourceMissing` / `ErrIntegrityViolation` → `ErrValidation`; nothing sent or uploaded | permanent |
| Missing `s3:ListBucket` | worker startup | `s3content.Store.Check` fails (missing objects would read as 403) | — |
| Tenant storage provider 5xx / throttling / timeouts | storage | `ErrUpstream` | transient (retried, policy `safe`) |
| Tenant storage 403 / 404 / invalid | storage | `ErrUpstream` | permanent |
| Email provider unreachable before writing, 429, 408 | send-email | `not_delivered` | transient (retried) |
| Email provider timeout / reset after writing, 5xx | send-email | `unknown`; never retried automatically | unknown |
| Email provider 4xx rejection, attachment over provider limit | send-email | `not_delivered` / `ErrValidation` | permanent |
| Drive rate limiting (403 rate-limit reasons), 5xx | google-drive | `ErrUpstream`; row released | transient |
| Concurrent call owns the Drive document | google-drive | `InProgressError` | transient |
| Internal service 503 / 429 / timeout | rest-call | `ErrUpstream` + output with status | transient; retried only for idempotent methods |
| Internal service 3xx / other 4xx | rest-call | `ErrUpstream` (redirect not followed) | permanent |
| No chat-notify provider configured | chat-notify | `ErrValidation` | permanent |
