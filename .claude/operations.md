# Operations

The library has no process, server or configuration of its own; "operations" here means what the library guarantees, what the consuming worker must provide, and how the repository is built, tested and released.

## 1. Security

### 1.1 Trust model (`SECURITY.md` § Trust model)

- **Credentials arrive resolved.** The worker resolves every `secret_ref` field (`registry.Field.IsSecretRef()`) from OpenBao for the job's own tenant and passes the value in `Execute`'s input. The library never sees an OpenBao path.
- **Credentials live only in memory**, in the storage / send-email client caches, keyed by a SHA-256 of every credential field (plus bucket or sender), capped at 256 entries. A retired client is closed once its last call finishes. Credentials are never logged, returned in outputs or errors, or persisted.
- **Internal calls carry caller identity:** `x-internal-token` (`Config.InternalToken`, required by `New`), `x-tenant-id` (`WithTenant`), `x-user-id` (`WithActor`, the tenant's automation principal) and, optionally, `x-workflow-department-id` (`WithLaneDepartment`); a missing tenant or actor, or a CR/LF in any of them, fails before any request. No `x-departments` is sent.

### 1.2 Guards in the code

| Threat | Guard |
|---|---|
| Request forgery via aliases | alias-only (never a raw URL); `aliasconfig.Validate` (`baseURL` http/https, host, no userinfo, query or fragment; `pathTemplate` starts with `/`, no `#`, no raw space, control character, `"` `<` `>` `\` `^`, backtick or pipe outside a placeholder, no unterminated `{`, valid `%XX` escapes, a query that parses and has no `;`; run by `Load` — strict YAML, unknown keys rejected — and again by `connectors.New`); `shared.NewInternalRequest` pins scheme + host per request and refuses a `#`; path params escaped and never `""`/`.`/`..`; query params cannot override template keys and need a non-empty name |
| Header forwarding on redirect | `InternalHTTPClient` and `ProviderHTTPClient` never follow redirects (also under the Gmail, Graph, Drive OAuth transports, the Gmail / Graph OAuth token requests — own transport, HTTP/1.1, 30 s — and the S3, Azure and GCS clients — own transport, 2-min timeout) |
| Internal token through an egress proxy | `InternalHTTPClient`'s default transport has `Proxy` nil (no `HTTP_PROXY` / `HTTPS_PROXY`); a caller-supplied `*http.Transport` keeps its own proxy |
| Wrapped `http.DefaultTransport` (otelhttp, httpmock) | `shared.BaseTransport()` falls back to Go's default transport settings instead of panicking on a type assertion |
| Ambiguous parameter rendering | path / query params must be scalars (`FormatScalarParam`; a list, map or nil is `ErrValidation`); the alias's fixed query is kept verbatim and caller params appended |
| HTTP/2 silent replay | provider and internal clients are HTTP/1.1 only (a caller `RoundTripper` that is not `*http.Transport` is used as-is and must not enable HTTP/2) |
| SSRF through tenant credentials | `ValidateGoogleServiceAccountKey` (Google token endpoint and universe only); Azure account / container patterns; Entra `tenantId` GUID-or-domain pattern |
| Worker environment leaking into tenant clients | S3 and SES clients built only from the tenant's credentials and region (no `config.LoadDefaultConfig`). **Exception:** the GCS SDK honours `STORAGE_EMULATOR_HOST`; if the worker's environment set it, gcp-gcs traffic would go unauthenticated to that host — the worker must never set it (`gcp-gcs` refuses to build a client while it is set) |
| Email recipient / header injection | `senderEmail` / `receiverEmail` must be single bare addresses; body `contentType` only `text/plain` / `text/html`; Gmail MIME quotes and encodes display names, strips CR/LF and normalises attachment content types (`mime.ParseMediaType` / `FormatMediaType`, else `application/octet-stream`); SES encodes the From name |
| Endpoint host injection | the SES `region` must be one lower-case DNS label |
| Sender spoofing within a tenant | **not guarded**: `senderEmail` is not bound to the credential; customers restrict Graph `Mail.Send` (Exchange Application Access Policy) and keep Google domain-wide delegation narrow |
| Drive delete of a folder by name | by-name lookups exclude folders, shortcuts and `application/vnd.google-apps.*` files (Drive's delete skips the trash); a recorded file that is trashed or moved out of the folder is no longer the document's (never overwritten, read or deleted); a document with a recorded file never deletes an untagged same-name file; one tenant per Drive folder |
| Tampered document ref | `Delete` removes the record and only the ref's own canonical object, never the object the record names |
| Wrong object addressed | gocloud keys it would rewrite (`__0xNN__`) are `ErrValidation` before any request: a control char or `../`; on azure-blob also `"` `#` `%` `?` `\` DEL and a trailing `/` |
| Duplicate emails | SES `aws.NopRetryer`; Gmail sent as one media-upload request (`ChunkSize(0)`); SendGrid requests built per call (the SDK's shared client mixed concurrent bodies); write tracing for outcome classification; send intents |
| Cross-tenant document access | refs scoped by tenant in Valkey; exact `<prefix><tenant>/<uuid>` key check and bucket check before any S3 call; size + SHA-256 verified on every read |
| Unbounded memory | 50 MiB objects, 1 MiB inline, 10 MiB responses, 25 MiB attachments, email body 5 MiB and subject 998 bytes (`ReadAllLimited` holds at most limit + 1 bytes) |
| Session cookies in workflow data | `rest-call` drops `Set-Cookie` from returned headers |
| Direct database access | `depguard` `pgcommon-only`; arch-lint grants pgcommon only to the two `sqlstore` packages |

### 1.3 Supply chain

Every GitHub Action is pinned to a full commit SHA with a version comment (`validate-quality.yml` fails on an unpinned `uses:`); workflow files may not contain HTML-escaped operators; compose images are pinned by digest; `govulncheck` (v1.1.4, pinned) runs on `./pkg/...`; Trivy scans `go.mod` / `go.sum` (`skip-dirs: tools`) for CRITICAL / HIGH / UNKNOWN; a CycloneDX SBOM is produced in CI and attached to releases.

## 2. Configuration

**The library reads no environment variables** — but the GCS SDK it builds for `gcp-gcs` honours `STORAGE_EMULATOR_HOST`: the worker must **never** set it (tenant GCS traffic would go, unauthenticated, to that host). The consumer reads its configuration and passes objects in (`connectors.Config`, `*pgcommon.Pool`, `*redis.Client` / `*redis.ClusterClient`, `*s3.Client`, `aliasconfig.Config`). The worker-side variables (pgcommon's `DATABASE_URL` / `PG_*`, `PG_BOUNCER_MODE`, `PG_STATEMENT_TIMEOUT`, `PG_LOCK_TIMEOUT`, `MIGRATION_DATABASE_URL`, Valkey and bucket settings, the internal token, the alias file) are tabulated in [`docs/integration/connector-worker.md`](../docs/integration/connector-worker.md) § 3.

Library defaults the worker can rely on: `docref.DefaultTTL` 24 h, `valkeystore.DefaultWaitAOFTimeout` 2 s (clamped 250 ms under the client read timeout, minimum 100 ms), `documents.DefaultLease` 15 min (keep the per-call timeout below it), `sendintent.StalePendingAfter` 15 min, `shared.DefaultHTTPTimeout` 30 s (internal call without an alias timeout; OAuth token requests), `shared.ProviderHTTPTimeout` 2 min, client cache limit 256.

**Local environment** (`.env-example` → `.env` via `make setup`; the Makefile `-include`s it and docker compose reads it):

| Variable | Purpose |
|---|---|
| `GOPRIVATE`, `GONOSUMDB` | `github.com/BCBP-SOLUTIONS-FZC-LLC/*` (SSH access to platform-pgcommon) |
| `WC_PG_PORT` 55432, `WC_PGBOUNCER_PORT` 55433, `WC_VALKEY_PORT` 56379, `WC_FLOCI_PORT` 4580, `WC_VALKEY_CLUSTER_PORTS` 57001-57006 | compose host ports (chosen not to collide with the IAM stacks); the Makefile derives `INT_ENV` from them (`TEST_VALKEY_CLUSTER_ADDRS` = the first three ports of the cluster range, which is both the host and the container range: the nodes announce it) |
| `TEST_POSTGRES_DSN`, `TEST_PGBOUNCER_DSN`, `TEST_VALKEY_ADDR`, `TEST_S3_ENDPOINT`, `TEST_VALKEY_DOCKER` | read by the tagged tests; the Make targets set them from `WC_*` |
| `TEST_VALKEY_CLUSTER_ADDRS` | comma-separated seed nodes of the `valkey-cluster` compose profile's cluster (3 masters + 3 replicas; `127.0.0.1:57001,127.0.0.1:57002,127.0.0.1:57003`, the first three ports of `WC_VALKEY_CLUSTER_PORTS`; the tests look the roles up, a failover swaps them); `make docker-up` starts the profile and the Make targets set the variable, CI too; unset, the cluster test skips locally and fails with `CI` set |
| (precedence) | under `make`, a value in `.env` **overrides** the same variable exported in the shell (make gives a makefile assignment precedence); override one run with `make <target> VAR=value` or `make -e`. CI has no `.env` |
| `CI` | unset locally; when set, a missing `TEST_*` variable fails the test instead of skipping it |
| `DATABASE_URL`, `PG_*`, `PG_BOUNCER_MODE=true`, `PG_STATEMENT_TIMEOUT=5s`, `PG_LOCK_TIMEOUT=2s`, `PG_POOL_NAME`, `MIGRATION_DATABASE_URL` | pgcommon's variables pointed at the compose stack, as a worker would run (not read by the library) |

## 3. CI/CD and Release

### 3.1 Workflows (`.github/workflows/`)

| Workflow | Trigger | Does |
|---|---|---|
| `ci.yml` | push to `main`, PRs to `main`, manual | `changes` (`detect-changes.sh`: docs-only → skip — `docs/integration/connector-worker.md` counts as code, its skeleton is compiled; plus the draft / `skip-ci` gate) → `validate-test` ∥ `validate-quality` (both always called, with `skip`, so the required checks report) ∥ `api-compat` (apidiff vs the last stable tag, warning only) → `trivy` (table gate + SARIF + CycloneDX SBOM artifact; Trivy DB cache key includes the UTC date, so it refreshes daily — same in `release.yml`) → `pr-summary` (`.github/scripts/pr-summary.js`). `build-image-cache`, `lint-dockerfile` and `smoke-tests` are documented **no-op placeholders** for the org branch-protection ruleset. Concurrency: superseded PR runs cancel; every push to `main` runs. |
| `validate-test.yml` (reusable; job `test`) | from `ci.yml` / `release.yml` (`skip`, `ref` inputs; `GO_PRIVATE_TOKEN`, `CI_REPO_READ_TOKEN` passed explicitly) | images pulled with retries from `mirror.gcr.io` (`WC_REGISTRY`) → `docker compose up -d --wait` (`COMPOSE_PROFILES: valkey-cluster`, so pull, up and logs include the cluster) → private module access → `go mod verify` → `make test-ci` → coverage gate (`COVERAGE_THRESHOLD: '98'`, `.github/scripts/coverage-gate.sh`) → compose logs on failure → upload `coverage.out` → `make build`. Sets `TEST_*` env to the compose ports; `CI=true` makes infrastructure tests mandatory. |
| `validate-quality.yml` (reusable; job `quality`) | same | `make mod-verify`, HTML-entity check, SHA-pinned actions check, gofmt, `make tidy-check`, `make vet`, `make lint`, `make arch-lint` (go-arch-lint v1.15.0 installed into a version-specific cache dir and its version verified), `make docs-check`, `make docs-compile` (the `docs/integration/connector-worker.md` skeleton compiles), `make ci-scripts-test`, `make vuln-check` |
| `docs.yml` | `ARCHITECTURE.md`, `docs/architecture/**` | `python3 scripts/docs_check.py` (runs on docs-only changes, which `ci.yml` skips) |
| `changelog-check.yml` | PRs touching `pkg/**`, `go.mod`, `go.sum` | fails unless `CHANGELOG.md` changed in the PR |
| `release.yml` | tags `v*.*.*` / `v*.*.*-*`, manual (`tag`) | pgcommon's job graph: `verify` (`verify-release-tag.sh`, `verify-changelog-entry.sh` via `changelog-section.sh`) → `validate-test` ∥ `validate-quality` ∥ `api-compat` (**blocking**) ∥ `build-image-cache` (placeholder) → `trivy` (Trivy gate + SARIF + SBOM) and `smoke-tests` (placeholder), both after test + image; `build-source-archive` after both gates (`build-source-archive.sh`: archive + `.sha256`) → `publish` (`extract-release-notes.sh`, `release-checksums.sh` re-checks the archive and writes `checksums.txt`, Cosign `sign-blob` + `verify-blob`, `create-github-release.sh`; `-` in the tag → prerelease) |

Every job runs on `ubuntu-24.04` (pinned, never `ubuntu-latest`). Required checks: "Validate / Test / test" and "Validate / Quality / quality" (job names must not change). PRs must come from branches of this repository, not forks (forks do not receive `GO_PRIVATE_TOKEN`). `.githooks/pre-commit` (installed by `make setup` / `make install-hooks`) runs `make tidy-check`, `make fmt-check`, `make lint`.

### 3.2 Release state

**v2.2.0** is the current release (2026-10-22; the caller identity on `rest-call` and `sql-query`, new exports only, `make api-compat` reported no incompatible change); **v2.1.0** (2026-10-13, tag on `fba27ec`, module path `…/workflow-connectors/v2`; new exports, `make api-compat` reported no incompatible change); **v2.0.0** (2026-10-10, tag on `5ef76fa`, moved from `7e3d826` (first run hit Docker Hub's pull rate limit) and `f392b27` before its final release — a published tag is never moved or deleted again; a fix ships as a new PATCH), v1.0.0 (2026-10-09) and `v0.1.0-beta.1` precede it. New changes collect under `CHANGELOG.md` `[Unreleased]`. Process (`VERSIONING.md`): merge to `main` → move `[Unreleased]` into `## [X.Y.Z] - YYYY-MM-DD` → `make ci` → `git tag -a vX.Y.Z -m "vX.Y.Z" && git push origin vX.Y.Z` → notify `definition_service` and `execution_service` owners for MINOR / MAJOR. Consumers: `go get github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2@vX.Y.Z` with `GOPRIVATE` set. There is no image or binary; the GitHub Release carries a source archive, the SBOM and a Cosign-signed `checksums.txt` (`VERSIONING.md` *Verifying a release*).

## 4. Testing Strategy

All tests are in the root module. `make test-unit` needs no Docker; everything tagged `integration` needs the compose stack.

| Suite | Packages (Makefile var) | Tag | Needs | Covers |
|---|---|---|---|---|
| unit | `./test/unit/...` (`TEST_UNIT_PKGS`) + `./pkg/...` (`TEST_INTERNAL_PKGS`, white-box) | none | nothing | per-package black-box tests (`test/unit/<package>/`), `retry_matrix_test.go`, `worker_scenarios_test.go`, `regression_test.go` (`TestREG01`–`TestREG20`, except REG13 / REG15 in `storage/googledrive/facade_test.go` / `storage/gocloud/facade_test.go`), adapters against test servers, Valkey against a scripted RESP server, googledrive's in-memory registry leg |
| postgres | `./test/postgres/...` + `./pkg/connectors/storage/googledrive/...` (`TEST_POSTGRES_PKGS`) | `integration` | PostgreSQL direct + PgBouncer | store contract suites, concurrency (exactly-one-winner), timeouts, classification, lost reservation replies (`test/postgres/sendemail/reservation_test.go`), concurrent migrations and the stores through PgBouncer (`test/postgres/postgres_test.go`, `TestPG01`–`TestPG03`), the send-intent schema upgrade from v2.0.0 (`test/postgres/upgrade_test.go`: `TestPG04` 000003–000005 applied by 4 replicas under live Reserve / Record traffic, `TestPG05` down to version 2 and up again, `TestPG06` a hand-edited `attempts < 1` row stops the upgrade at 000004's VALIDATE, `TestPG07` both schemas reach their latest embedded version), Release / PruneIntents / `ErrNotReserved` (`test/postgres/sendintent/release_prune_test.go`, `prune_race_test.go`), bounded Drive error text (`test/postgres/documents/errortext_test.go`), googledrive's PostgreSQL leg (`stores_postgres_test.go`) |
| integration | `./test/integration/...` (`TEST_INT_PKGS`) | `integration` | Valkey, Valkey Cluster, floci S3, PostgreSQL via PgBouncer, Docker for restarts | workflow scenarios across replicas (`workflow_scenarios_test.go` `TestIT01`–`TestIT06`; `production_scenarios_test.go` `TestIT07`–`TestIT10`: an accepted send's redelivery succeeds as a duplicate after its attachment ref was deleted / expired, an attachment failure after reserving records `not_delivered` and the key is re-sent once, a Valkey ACL refusal of the ref write deletes the S3 object again), `multireplica` (`TestMultiReplica_*`), `s3content`, `valkeystore` (durability, idempotent lost-reply retry, `TestACL_DocumentedRulesSuffice`, restarts with `TEST_VALKEY_DOCKER=1`, and, against the real `valkey-cluster` compose profile (3 masters + 3 replicas, `TEST_VALKEY_CLUSTER_ADDRS`): `TestCluster_SlotMigration_PutGetDelete`, `TestCluster_DocumentedACL_Suffices`, `TestCluster_CheckDurability_EveryMasterAndReplica` (a replica without the worker's ACL user fails, not `ErrConfigUnavailable`), `TestCluster_ManualFailover_RefsRouteToNewMaster` (`CLUSTER FAILOVER`, then `Get` / `Put` / `Delete` reach the promoted replica at once; fails back in cleanup)) |

- **Compose stack** (`docker-compose.yml`): `postgres:17-alpine` (`max_connections=300`, db `connectors`), `edoburu/pgbouncer` v1.25.2 (transaction mode), `valkey/valkey:8-alpine` (`--appendonly yes --appendfsync always --maxmemory-policy noeviction`, 256 MB), `floci/floci:2.1.0-compat` (S3; `scripts/init-floci.sh` creates `workflow-connectors-documents` with the `docrefs/` 2-day rule; healthy only once the lifecycle rule exists). Profile `valkey-cluster`: three Valkey masters with one replica each in one container (same image; `scripts/valkey-cluster.sh` is PID 1, starts the six nodes, `--cluster create --cluster-replicas 1`, and shuts them down on SIGTERM), ports `WC_VALKEY_CLUSTER_PORTS` (default 57001–57006, the first three start as masters) announced as `127.0.0.1` with identical host and container ports, a fresh cluster on every container start; healthy only when every node reports `cluster_state:ok` and knows all six nodes, none is flagged failing, and three replicas have `master_link_status:up`. All digest-pinned, pulled from `${WC_REGISTRY}`. `make docker-up` = `docker compose --profile valkey-cluster up -d --wait` (`COMPOSE_PROFILE_FLAGS`); `make docker-down` removes the profile's container too.
- **Skip vs fail:** without the `TEST_*` variable a tagged test skips; with `CI` set it fails ("cannot be skipped in CI").
- **Parallelism:** `make test` / `test-ci` / `race` run the three suites with `make -j3`; the postgres suite uses `-parallel $(TEST_POSTGRES_PARALLEL)` (default 4) and prints a failing-tests summary from `.coverage/postgres.raw`. Store tests isolate by tenant or a throwaway database / schema.
- **Coverage:** `-coverpkg=$(COVER_PKG_LIST)` (all `./pkg/...`) per suite → `.coverage/*.out` → `scripts/merge_coverage.py` (max count) → `coverage.out`. Gate 98%; current 99.97%.
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
| Valkey cluster master fails (no server reply) | ref ops on its slots | that call fails by cause; a deduplicated slot-map reload is scheduled and the masters cache emptied, so the promoted replica is used at once (not after go-redis's 60 s reload) | transient (network) |
| Valkey unreachable, `OOM`, failover / topology replies, `NOREPLICAS`, `BUSY`, maxclients, pool timeout, cluster slot map not loadable | `createDocument`, ref uploads, attachments | ref not created / not resolved; attachments fail before sending (`not_delivered`) | transient (`ErrUnavailable` / network) |
| Valkey AOF not confirmed in `WaitAOFTimeout` | ref creation | ref and object removed, `ErrNotPersisted` | transient |
| Valkey misconfigured (no AOF, eviction policy) | worker startup | `CheckDurability` returns `*DurabilityError` (check it before `ErrConfigUnavailable`, which matches only when every node's only failure is a reply refusing `CONFIG GET` itself — `NOPERM` or `ERR unknown command` / `unknown subcommand`; any other reply (`WRONGPASS`: a node without the worker's ACL user, `LOADING`, `BUSY`, maxclients), an unreachable node or a timeout is its own failure, never `ErrConfigUnavailable`); worker must refuse to start | — |
| Valkey primary failover (`WaitReplicas` 0) | refs acknowledged just before | ref lost → consumer gets `ErrNotFound` | permanent (runbook: re-run producing step) |
| S3 document bucket unreachable / throttled | ref create / resolve | create fails (no metadata written); resolve fails | by cause (5xx / throttling / network transient) |
| Document object deleted or altered | ref resolve | `ErrSourceMissing` / `ErrIntegrityViolation` → `ErrValidation`; nothing sent or uploaded | permanent |
| Missing `s3:ListBucket`, `s3:PutObject` or `s3:DeleteObject` on the document bucket | worker startup | `s3content.Store.Check` fails, naming the grant (without `s3:ListBucket` missing objects read as 403) | — |
| Valkey ACL refusal (`NOPERM`, e.g. no `+asking` on a cluster) | ref create / resolve / delete | refused command returned as is; a failed create deletes its S3 object | unknown (not retried) |
| Tenant aws-s3 credential without `s3:ListBucket` | storage delete | a missing object reads as 403: error with a hint, never "already deleted" | permanent |
| Tenant storage provider 5xx / throttling / timeouts | storage | `ErrUpstream` | transient (retried, policy `safe`) |
| Tenant storage 403 / 404 / invalid | storage | `ErrUpstream` | permanent |
| Azure delete of a blob with snapshots (409 `SnapshotsPresent`) | storage delete | `ErrUpstream` explaining that snapshots are never deleted | permanent |
| Email provider unreachable before writing, 429, 408 | send-email | `not_delivered` | transient (retried) |
| Email provider timeout / reset after writing, 5xx | send-email | `unknown`; never retried automatically | unknown |
| Email provider 4xx rejection, attachment over provider limit, Gmail MIME message over 25,000,000 bytes | send-email | `not_delivered` / `ErrValidation` | permanent |
| Pre-send stage (attachments, client build) past 11 min 50 s after the reservation | send-email with `messageKey` | nothing sent, recorded `not_delivered` | transient |
| Drive rate limiting (403 rate-limit reasons), 5xx | google-drive | `ErrUpstream`; row released | transient |
| Concurrent call owns the Drive document | google-drive | `InProgressError` | transient |
| Internal service 503 / 429 / timeout | rest-call | `ErrUpstream` + output with status | transient; retried only for idempotent methods |
| Internal service 3xx / other 4xx | rest-call | `ErrUpstream` (redirect not followed) | permanent |
| No chat-notify provider configured | chat-notify (not in `registry.All()`: cannot be authored) | `ErrValidation` | permanent |
| Internal service 2xx with a body over 10 MiB | rest-call | `ErrUpstream` + `ErrTooLarge`, reason "response too large"; output with `status`, `headers`, `method`, `bodyTruncated` | permanent (the call was processed; never `ErrValidation`) |

## 7. Upgrade Notes (v2.0.0 → v2.1.0, `CHANGELOG.md` [2.1.0])

- **Valkey ACL:** the worker's user needs `+asking` (cluster slot migrations); without it a ref operation during a migration fails with `NOPERM` (unknown, not retried). `TestACL_DocumentedRulesSuffice` / `TestCluster_DocumentedACL_Suffices` replay the runbook's rules.
- **S3:** the document bucket needs `s3:PutObject` and `s3:DeleteObject` under the prefix as well as `s3:ListBucket` (`Store.Check` now probes them); tenant aws-s3 credentials need `s3:ListBucket` for delete to recognise a missing object.
- **chat-notify** is no longer in `registry.All()`: in workflow-definition-service a chat-notify step now gets an `UNKNOWN_CONNECTOR_TYPE` publish **warning** (not an error), and its template and credential write/revoke are refused — tell its owners first.
- **Stricter input:** send-email `contentType` exactly `text/plain` / `text/html` (a `; charset=…` or upper-case value is refused); `messageKey` / `resendAttempt` bounds; storage `bucket` / `key` rules; rest-call scalar params; sql-query `params` list; strict alias YAML (unknown keys, query / fragment in `baseURL`, `pathTemplate` rules: `#`, invalid characters or escapes, `;` in its query); send-email body ≤ 5 MiB, subject ≤ 998 bytes; Gmail attachments ≤ 17,000,000 raw bytes; storage keys gocloud cannot address (control char, `../`; on azure-blob `"` `#` `%` `?` `\` DEL, trailing `/`).
- **Migrations:** send-intent `000003`–`000005` (`000005` builds an index under a `SHARE` lock; on a large table create `ix_connector_send_intents_prune` `CONCURRENTLY` by hand first — an invalid index left by a failed concurrent build is dropped and rebuilt, a valid one kept). Schedule `PruneIntents`.
- **Worker:** call `valkeystore.Options.Validate()` at startup; pass rest-call's output `method` to `DecideRetry`; never set `STORAGE_EMULATOR_HOST` (`gcp-gcs` refuses to build a client while it is set); give Drive folders by ID.
- **Valkey:** `CheckDurability` now fails (not `ErrConfigUnavailable`) on `WRONGPASS` — every master and replica needs the worker's ACL user — and on `LOADING`, `BUSY`, maxclients.
