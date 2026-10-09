# Workflow Connectors — Low-Level Design

## Tender Management SaaS Platform — Workflow Subsystem

| Field | Value |
|---|---|
| Document type | Low-Level Design (LLD) — Part I module design, Part II connector system design |
| Module | `workflow-connectors` (shared connector library) |
| Go module | `github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2` |
| Status | **`v2.0.0` released 2026-10-10** at module path `…/workflow-connectors/v2` (breaking since `v1.0.0`; see §13, §19) |
| System design | Part II of this document (§S1–§S11) — the *Automatic Connector Tasks & Connector Workers* design, rev 8.14, moved here from `docs/lld/workflow_connectors.md`. Sibling services' LLDs cite it as `workflow_connectors.md` in the design repo |
| Consumers | `definition_service` (`pkg/registry` only); `execution_service` `cmd/connector-worker` (`pkg/connectors` and the provider adapters) |
| Sibling modules | `platform-events`, `platform-gincommon`, `platform-pgcommon` (same `platform-libs` style); only `platform-pgcommon/v2` (v2.0.1) is a dependency of this one, for the two PostgreSQL stores |
| Deployment stage | Library — nothing to deploy; consumed by tag through the Go module proxy (§13) |

### Revision history

| Rev | Date | Change |
|---|---|---|
| 1.15 | 2026-10-10 | **v2.0.0 released.** PR #1 merged to `main`; `CHANGELOG.md` `[Unreleased]` cut to `[2.0.0] - 2026-10-10`; status, VERSIONING, SECURITY and `.claude` updated; tag `v2.0.0` released through `release.yml` (verify → validate-test ∥ validate-quality ∥ API compatibility → SBOM → Cosign-signed GitHub Release). |
| 1.14 | 2026-10-10 | **Doc alignment with the code; store transactions; worker guide.** Both PostgreSQL stores (`documents/sqlstore`, `sendintent/sqlstore`) run every statement, single statements included, in `pgcommon.RunInTx`, so the pool's statement and lock timeouts bound every call (`Pool.WithConn` would not); `documents` `Claim` is one `INSERT … ON CONFLICT DO UPDATE … WHERE` statement instead of insert-then-update-then-select; platform-pgcommon pinned at **v2.0.1**. New consumer wiring guide `docs/integration/connector-worker.md`, cited by OQ-14 and OQ-17. Part I re-checked against the code: package tree (`retry.go`, `sendintent`, `test/`, `tools/`, `scripts/`), direct dependencies, dependency rules (cores and facade may use `sendintent` and `docref`), `Config` (`SendIntents`, `DocRefs`), ports (`AttachmentLimiter`, `documents.Store`, `sendintent.Store`, `docref.Store`/`ContentStore`), the client cache's least-recently-used eviction, `storage` upload's `contentRef` rule, the §17 error taxonomy (`ClassOf`, docref sentinels), test layout and coverage (99.96 %, gate 98 %), release checks; stale Part II catalogue notes on `send-email` (real providers, `not-delivered` retry, document-ref service) corrected. |
| 1.13 | 2026-10-10 | **Third production-readiness review fixes.** *Security:* Gmail display names are encoded with `mail.Address` and can no longer add recipients. `rest-call`/`sql-query` URLs must keep the alias host (`shared.NewInternalRequest`), path templates must start with `/`, `.`/`..`/empty path params and `queryParams` overriding template keys are refused, `x-departments` fails closed on an empty or malformed list, and internal calls use HTTP/1.1 only. Document-ref resolution requires the exact derived object key. *Correctness:* removed `ErrTransient`/`ErrPermanent` (`errors.Is` could disagree with `ClassOf`). `resend` is a compare-and-swap on `resendAttempt`, a stale `pending` intent (`StalePendingAfter`, 15 min) can be resent, a lost reservation reply is recovered by its token (migration 000002), and a duplicate of an accepted intent succeeds. Every send-email failure carries `deliveryOutcome`. Writing that has begun is never `not_delivered`. SES templates keep attachments, Gmail rate-limit 403s are transient, and per-provider attachment limits apply. Document-ref `Put` is idempotent on a lost reply, `READONLY`/`MASTERDOWN`/`NOREPLICAS` are transient, `WaitReplicas` is added, and cluster `CheckDurability` checks replicas. Drive adopts a hand-placed same-name file and releases its row when a registry step fails; registry writes after a Drive call are bounded at 10 s. gocloud delete of a missing object succeeds. Both PostgreSQL stores classify database errors. A `rest-call` error is classified by its status alone, and JSON numbers stay exact (`json.Number`). *Operations:* the runbook's Valkey ACL is complete and tested as a restricted user, the failover limits of `WAITAOF 1 0` are documented, the docs on pgcommon timeout scope are corrected, and the release tag check accepts several tags on one commit and requires the tag to be on `main`. |
| 1.12 | 2026-10-09 | **Second production-readiness review fixes.** Drive upload/fetch/delete errors classified through the Drive adapter (rate-limit 403s were class unknown, never retried); `s3content.Store.Check(ctx, keyPrefix)` probes under the document prefix (the documented IAM grant); send intents record only for the current pending attempt (`Record(ctx, id, attempt, …)`, `sendintent.ErrStaleRecord`) with a 10 s bound; provider HTTP clients are HTTP/1.1 only (Go's HTTP/2 client replays a POST after a PROTOCOL_ERROR reset); Valkey reads and deletes go to the key's master, cluster redirects are transient (`docref.ErrUnavailable`) and reload the slot map, `WaitAOFTimeout` is clamped below the client read timeout; OAuth token failures classed by the token endpoint's status; Drive lease budget measured from the claim and delete bounded by it; tenant IDs validated (UTF-8, no control characters, ≤ 255 bytes); internal calls bounded per call (`shared.CallTimeout`) so alias timeouts above 30 s are honoured; Trivy skips `tools/`; `make tidy-check` covers both modules; OQ-5 resolved. |
| 1.11 | 2026-10-09 | **Production-readiness review fixes; module path `/v2`.** SendGrid requests are built per call (the SDK client mutated a shared body, mixing concurrent emails). Tenant credentials that name endpoints are validated (Google `token_uri`/universe, Azure account and container names, Graph `tenantId`) — SSRF. Provider HTTP clients never follow redirects (`shared.ProviderHTTPClient`). Email outcomes are decided by whether the request was written (`sendemail.TraceWrites`, `ClassifyAfterSend`); an unrecorded send intent makes the failure class unknown. Valkey: script and `WAITAOF` in one pipeline on the key's node; `CheckDurability` probes `WAITAOF` on every master. S3: only `NoSuchKey` is a missing document; `Check` verifies it. Document refs: malformed IDs not found, malformed metadata an integrity violation, bounded cleanup. Drive: the write is bounded by the lease, leftover tagged files are removed on create and delete, a failed completion releases the row; `PruneAttempts` (migration 000002). `ProviderClient.Fetch` takes `maxBytes`; upload creates a ref only with `createDocument`. Client cache evicts least recently used. golangci-lint moved to `tools/go.mod`; images pinned by digest; release tag checked against the module path. |
| 1.10 | 2026-10-09 | **Transient / permanent / unknown error classification (OQ-9 resolved).** Every connector error now has one class (`shared.Class`; `connectors.ClassOf`, `IsTransient`, `IsPermanent`; the `ErrTransient`/`ErrPermanent` sentinels added here were removed before v2.0.0, since `errors.Is` could disagree with `ClassOf`), attached where the provider's error is understood: AWS error codes (S3 `NoSuchKey`/`AccessDenied`/`InvalidBucketName` permanent; `SlowDown`/`RequestTimeout`/`InternalError`/`ServiceUnavailable` transient), HTTP status (408/425/429/500/502/503/504 transient; 3xx and other 4xx permanent; other 5xx unknown), network failures (timeouts, DNS, refused, reset transient; cancellation unknown), gocloud codes (Azure, GCS), Drive reasons (rate-limit 403s transient), document resolution (`ErrNotFound`/`ErrSourceMissing`/`ErrIntegrityViolation` permanent; read timeouts and `docref.ErrUnavailable` transient), email (`not_delivered` by status or cause; `unknown` outcome always unknown). New `connectors.DecideRetry` → `RetryDecision{Retry, Class, Reason, Policy, Rule}` with `LogAttrs()`; only transient errors are retried, and only per policy. New policy `not-delivered` for `send-email` (was `unsafe`): a transient, provably undelivered failure is retried; a send intent whose last outcome was `not_delivered` is re-reserved without `resend`. `ErrUpstream` alone no longer means retryable; unknown errors are not retried. Reference: `docs/runbooks/retry-semantics.md` (§9.2, §17). |
| 1.9 | 2026-10-09 | **Document content moved to S3; Valkey keeps reference metadata only.** New `docref.Service` (`Create`, `Open` (streaming, verifying), `Read`, `Delete`) over a `ContentStore` port and the metadata `Store`. `docref/s3content` writes each document to the platform's S3 document bucket at `<prefix><tenant_id>/<uuid>` with the worker's own S3 client; `docref/valkeystore` keeps a hash at `docref:{uuid}` (`reference_id`, `tenant_id`, `bucket`, `object_key`, `content_type`, `size`, `sha256`, `created_at`, `updated_at`) and no content. Every resolution checks the tenant before calling S3, then verifies size and SHA-256: `ErrNotFound`, `ErrSourceMissing`, `ErrIntegrityViolation`. Valkey memory is O(refs), not O(document size). Removed: the content field, byte (de)serialization, `ErrCorrupt`, source-location and version fields, Valkey sizing by document size. Runbook renamed `docs/runbooks/document-refs.md`. Rationale in §4.1. |
| 1.8 | 2026-10-09 | **Document refs stored only in Valkey; the PostgreSQL layer removed.** `docref/valkeystore` (go-redis v9.22.0) is the single authoritative store: one hash per ref at `docref:{tenant_id}:{uuid}` (rev 1.9 changed it to `docref:{uuid}`) (ID, tenant, source location, content type, size, SHA-256, bytes, version, created/updated/expires timestamps); create-only Lua script; `WAITAOF` before a ref is returned; 24 h key TTL; `Delete`. `docref/sqlstore`, its migrations, `docref.Cached` and `Config.DocRefCacheBytes` removed — no table, repository, migrations, dual write, fallback read or cache. `valkeystore.CheckDurability` enforces `appendonly yes`, `appendfsync always|everysec`, `maxmemory-policy noeviction`. Runbook `docs/runbooks/document-refs-valkey.md` (since renamed `docs/runbooks/document-refs.md`). Rationale in §4.1. OQ-1 stays resolved, now via Valkey. Tests against real Valkey including a Valkey restart (§14.4). |
| 1.7 | 2026-10-09 | **Document refs moved to a shared durable store (OQ-1 resolved).** New `docref` port and `docref/sqlstore` on the worker's PostgreSQL (platform-pgcommon): `connector_document_refs` holds ref, tenant, source location, content type, size, SHA-256, bytes and expiry; committed before a ref is returned; immutable; tenant-scoped; 24 h TTL with `Prune`. Worker memory is a bounded, disposable read-through cache (`docref.Cached`). `shared.DocRefStore` removed; `Config.DocRefs`/`DocRefCacheBytes`; refs require `WithTenant`. Multi-replica integration tests (§14.4). |
| 1.6 | 2026-10-09 | **Email delivery semantics made explicit and enforced.** Contract: not idempotent; at-most-once without retry, effectively at-least-once when retried after an uncertain outcome; exactly-once not provided (§5.4.2a). New `ErrNotDelivered`/`ErrDeliveryUnknown` classes (never `ErrUpstream`) and `*sendemail.SendError`; adapters classify status/transport; `deliveryOutcome` output on success and failure; `connectors.AutoRetryAllowed` gate; SES SDK retries disabled (`aws.NopRetryer` — the default retried `SendEmail` up to 3 times). Optional `sendintent` duplicate-request protection (`messageKey`, `resend`) on platform-pgcommon. Runbook `docs/runbooks/email-delivery.md`. OQ-15 resolved; OQ-16/17 open. |
| 1.5 | 2026-10-09 | **Document registry on platform-pgcommon.** `documents/sqlstore` now takes the worker's `*pgcommon.Pool` (v2.0.0, as org-membership and definition_service) instead of a `*sql.DB`: `pool.WithConn` for single statements, `pgcommon.RunInTx` for audited transitions; migrations ship in golang-migrate format and are applied with `sqlstore.ApplySchema(ctx, runner)`, tracked in `connector_documents_migrations`. CI configures private module access (`GO_PRIVATE_TOKEN`). OQ-14 records that `execution_service` must move from pgcommon v1 to v2 before wiring. |
| 1.4 | 2026-10-09 | **Drive duplicate-upload race fixed with a database uniqueness authority.** New `documents` port and state machine (`PENDING_UPLOAD` → `UPLOADING` → `AVAILABLE`/`FAILED`, plus `DELETING`), `documents/sqlstore` on the worker's PostgreSQL (`uq_connector_documents_identity`, owner/lease compare-and-set, audit table, migrations), `documents.MemoryStore`. `googledrive.NewProvider(store)`; Drive files addressed by recorded ID and tagged with the document ID for crash adoption; `connectors.WithTenant`/`ErrMissingTenant`. §4.5, §8.4, DOC-1..6, OQ-12 (resolved), OQ-13/14 (open). Race tests run against the in-memory store and PostgreSQL (CI service container; `make test-postgres`). |
| 1.3 | 2026-10-09 | **Reference-counted client lifecycle.** `shared.ClientCache` replaces the per-connector map: calls hold a handle, overflow and the new `Connector.ResetClients()` retire entries, and a retired client is closed exactly once after its last release (CACHE-5/6). Adapters with releasable resources implement `io.Closer`. Tests cover in-flight survival, no reuse after reset, close-after-final-release, concurrent users, reset cycles without leaks, and exactly-once close, all under `-race`. Coverage gate 83%. |
| 1.2 | 2026-10-09 | **Production-readiness hardening, from a code review.** Release blockers: `rest-call`/`sql-query` never follow redirects (Go re-sent `x-internal-token`/`x-departments` to the redirect host); numeric path/query parameters are plain numbers (`1234567` was sent as `1.234567e+06`); `chat-notify`/`document-extract` fail with `ErrValidation` without a provider instead of silently using the mock. High: email address fields must be one bare address; binary inline content is base64 with `contentEncoding`; size caps on objects, inline content, responses, attachments and `DocRefStore` (now 256 MiB + 1 h TTL); default 30 s HTTP timeout and bounded OAuth token requests. Medium: AWS clients built from tenant values only, `region` required for aws-s3; SES From names encoded; Drive query uses single-quoted literals; `shared.Classify` keeps validation errors and the cause chain (OQ-2/3/4 resolved, OQ-1/9 partly); upload `contentRef` resolves; Gmail MIME quoted-printable body and wrapped base64. Low: alias `baseURL` shape check, `Set-Cookie` dropped, registry descriptions. Coverage gate 82%. |
| 1.1 | 2026-10-09 | **Single LLD for the repo.** The cross-repo design (`docs/lld/workflow_connectors.md`, rev 8.14) moved in as **Part II** with its full text, decision log and revision history; its sections are renumbered §S1–§S11 (and §S-Appendix) so they never collide with Part I's §1–§21. Every citation in this document, `README.md`, `ARCHITECTURE.md`, `SECURITY.md` and `CONTRIBUTING.md` now points at the §S sections, and the separate file is deleted. Part I wording that called it the "governing LLD" now names Part II. No design change. |
| 1.0 | 2026-10-09 | **First module-scoped LLD.** Follows the `iam-org-membership` LLD's section layout, scoped to what this module owns. The cross-repo design (`workflow_connectors.md`) stays authoritative and unchanged; this document refines its §6 into the module's package layout, public Go API, in-process state, error taxonomy, test strategy and open questions, re-verified against the code. Records the provider-adapter split (`storage/{gocloud,googledrive}`, `sendemail/{ses,sendgrid,msgraph,gmail}`), the vendor-aware `.go-arch-lint.yml`, and the CI/release pipeline realigned to `iam-org-membership`'s. |

### Table of Contents

1. [Document Overview](#1-document-overview)
2. [Module Responsibilities and Boundaries](#2-module-responsibilities-and-boundaries)
3. [Architecture and Package Layout](#3-architecture-and-package-layout)
4. [Data Model — In-Process State](#4-data-model--in-process-state)
5. [API Contract — Public Go API](#5-api-contract--public-go-api)
6. [Caching Design](#6-caching-design)
7. [Event Architecture](#7-event-architecture)
8. [Key Execution Flows](#8-key-execution-flows)
9. [Concurrency, Consistency, and Failure Handling](#9-concurrency-consistency-and-failure-handling)
10. [Security](#10-security)
11. [Observability](#11-observability)
12. [Configuration](#12-configuration)
13. [Distribution and Versioning](#13-distribution-and-versioning)
14. [Testing Strategy](#14-testing-strategy)
15. [Data Handling and Compliance](#15-data-handling-and-compliance)
16. [Open Questions and Sign-off Register](#16-open-questions-and-sign-off-register)
17. [Appendix — Error Taxonomy](#17-appendix--error-taxonomy)
18. [Integration Details](#18-integration-details)
19. [Migration Strategy](#19-migration-strategy)
20. [Operational Considerations](#20-operational-considerations)
21. [Performance Considerations](#21-performance-considerations)

**Part II — Connector System Design**

- [S1. Overview & Scope](#s1-overview--scope) · [S2. Architecture](#s2-architecture) · [S3. DSL Shape](#s3-dsl-shape-workflow-models) · [S4. Compiler Behavior](#s4-compiler-behavior-definition_service) · [S5. Runtime Behavior](#s5-runtime-behavior-execution_service)
- [S6. The Shared Connector Library & Worker Runtime](#s6-the-shared-connector-library--worker-runtime) — S6.1 placement · S6.2 credentials · S6.3 interface · S6.4 catalogue · S6.5 runtime loop
- [S7. External Systems & Licensing](#s7-external-systems--licensing) · [S8. Security & Data Handling](#s8-security--data-handling) · [S9. Follow-Ups](#s9-follow-ups-non-blocking) · [S10. Design Decision Log](#s10-design-decision-log) · [S11. Revision history](#s11-revision-history) · [S-Appendix — BPMN Authoring Reference](#s-appendix--bpmn-authoring-reference-for-ui-implementers)

---

## 1. Document Overview

This document is the low-level design for the **`workflow-connectors` Go module** — the shared library that implements the connector catalogue of the platform's automatic BPMN service tasks (`connector:`-prefixed). A connector task fetches, checks or sends something without a human acting on it, and completes through the same workflow path a human task does.

The module has **no process of its own**. It is two packages: `pkg/registry`, the connector type list and field shapes, which `definition_service` imports for its compile-time check and authoring-template generator; and `pkg/connectors`, the `Execute` implementations, which `execution_service`'s `cmd/connector-worker` imports and calls. Everything about running connectors — stream consumption, dedup, leases, pools, timeouts, retries, credential reads and completion callbacks — belongs to the worker; its design is recorded in Part II (§S5, §S6.5), not built here.

The document has two parts. **Part I (§1–§21)** gives the exact package layout, dependency rules, public Go API, per-connector input/output contract, in-process state, error taxonomy and test strategy needed to build, extend and consume the module. **Part II (§S1–§S11)** is the connector system design across `workflow-models`, `definition_service`, `execution_service` and this module — the end-to-end flow, the catalogue's field tables, the decision log ("Decision #N" anywhere in this repo refers to §S10) and the design's revision history. Where Part I and Part II disagree, **Part II is authoritative** and the discrepancy is flagged in §16.

**The code is at this state.** Every statement in Part I was checked against the code on 2026-10-10; the breaking changes since v1.0.0 (§19) shipped as v2.0.0 on 2026-10-10. Wiring the module into a worker, step by step: [`docs/integration/connector-worker.md`](../integration/connector-worker.md).

### 1.1 Relationship to Part II

| Part II section | What it specifies | Where Part I refines it |
|---|---|---|
| §S6.1 | Worker placement; the two-package split | §2, §3 |
| §S6.2 | Credentials resolved by the worker; document refs; internal-only `rest-call`/`sql-query` and their two headers | §2.3, §4.1, §8.2, §10 |
| §S6.3 | The `Connector` interface; `pkg/registry` vs `pkg/connectors` | §3, §5.2 |
| §S6.4 | The six-type catalogue, fields, providers, retry policy | §5.3, §9.2 |
| §S6.5 | Runtime loop (worker) | §18.2 (consumer obligations only) |
| §S8 | Security and data handling | §10, §15 |
| §S9 | Follow-ups (provider implementation, error taxonomy, alias allowlist) | §16 |
| §S10 Decisions #4, #6, #12–#14, #17–#23 | Settled decisions this module implements | Cited inline; collated in the Appendix |

---

## 2. Module Responsibilities and Boundaries

### 2.1 In scope

The module owns:

- **The `Connector` contract** — `Type() string` and `Execute(ctx, input map[string]any) (map[string]any, error)`, and the facade `connectors.New(Config)` that builds the active connectors keyed by type.
- **The catalogue metadata** (`pkg/registry`) — `Type*` constants, `FieldKind*`, `RetryPolicy`, `Field`, `Definition`, `All()` and `IsIdempotentMethod`. The single generated source of truth for authoring templates (§S6.3).
- **Connector cores** — input validation, output shaping and the `ProviderClient` port for `storage`, `send-email`, `chat-notify`, `rest-call`, `sql-query` and `document-extract`.
- **Provider adapters** — real SDK implementations for `storage` (`aws-s3`, `azure-blob`, `gcp-gcs` through `gocloud.dev/blob`; `google-drive` through Drive v3) and `send-email` (`aws-ses`, `sendgrid`, `microsoft-365` through Graph `sendMail`, `google-workspace` through Gmail).
- **In-process mocks** — `MockStorageClient`, `MockSendEmailClient` and the chat/extract mocks, for tests only. No connector falls back to one: without a real provider client every call fails with `ErrValidation`.
- **The alias schema** (`aliasconfig`) — `Config`, `Endpoint`, `Query`, `Load`, `Validate`, `ResolveEndpoint`, `ResolveQuery`.
- **Document references** (`docref`, `docref/s3content`, `docref/valkeystore`) — the resolution service that lets one connector's output be another's input on any worker replica: content in the platform's S3 document bucket, reference metadata in Valkey, verified on every resolution.
- **The Drive document registry** (`documents`, `documents/sqlstore`) and **send intents** (`sendintent`, `sendintent/sqlstore`) — the ports, in-memory stores and PostgreSQL stores (on platform-pgcommon, with their migrations) behind Drive uploads (§4.5) and send-email duplicate-request protection (§5.4.2a).
- **The retry decision** — every error's class (`connectors.ClassOf`) and `connectors.DecideRetry`, which the worker applies before any automatic retry (§9.2).
- **The internal-call header contract** — `x-internal-token` and `x-departments` names and the `WithDepartments` context carrier.

### 2.2 Out of scope (owned elsewhere)

| Concern | Owner | Why not here |
|---|---|---|
| Connector worker process: Valkey Stream consumption, dedup, execution lease, bounded pools, per-type timeout, retry/backoff, dead-lettering | `execution_service` `cmd/connector-worker` | Runtime concerns of one process; the library is called by it (§S6.1, §S6.5) |
| Completion and failure callbacks (`/internal/connector-tasks/:id/{complete,fail}`) and `IOMapping.Outputs` renaming | `execution_service` | The worker forwards `Execute`'s map verbatim (§S6.5 step 3, Decision #16) |
| Reading credentials from OpenBao | `execution_service` worker | Credentials arrive resolved; OpenBao stays out of this import graph (§S6.2) |
| Writing, rotating and revoking credentials; credential forms | `definition_service` | Credential custody (Decision #15) |
| Alias registry storage, CRUD API and internal-host allowlist | `definition_service` | Org-owned configuration (Decision #21, §S9) |
| BPMN compilation, `UNKNOWN_CONNECTOR_TYPE`, authoring templates | `definition_service` | Compile-side; it imports `pkg/registry` only |
| Connector-task DSL shape (`StageTypeConnector`, `IOMapping`) | `workflow-models` | Shared DSL module |
| Logging, metrics, tracing | `execution_service` worker | The library has no observability dependencies by design (§11) |
| A Document Service | — | Not proposed; document refs are opaque strings (§S6.2, Decision #14) |

The module **never reads a secret store, never opens a database connection, never listens on a port and never logs.** Every external call it makes is one the caller asked for by calling `Execute`. The stores it touches are the worker's, through clients the worker passes in: the Drive document registry (§4.5) and send intents (§5.4.2a) in the worker's PostgreSQL, through its `*pgcommon.Pool` (platform-pgcommon, the org's database layer); document-ref metadata in the worker's Valkey and content in the platform's S3 document bucket (§4.1).

### 2.3 Ownership split — credentials and aliases

Two inputs this module relies on are deliberately owned by other services:

- **Credentials.** For every field the registry marks `FieldKindSecretRef`, `Execute` receives the **resolved value**. The worker reads `connectors/<tenant>/<type>/<field>` for the job's own tenant just before the call (§S6.2, rev 8.10/8.13). A field the tenant stored nothing for is absent, and the adapter refuses it with an error naming the provider. The registry's secret-field descriptions must never claim a credential is author-supplied (pinned by `TestSecretRefDescriptions_DoNotClaimAuthorSupplied`).
- **Aliases.** `rest-call`/`sql-query` resolve `endpointAlias`/`queryAlias` against an `aliasconfig.Config` the worker fetches from `definition_service` at startup and passes to `New` (Decision #21). The library validates its shape (`Validate`) but not its hosts: the internal-host allowlist is enforced where aliases are written, because this module takes no deployment configuration (§S9).

---

## 3. Architecture and Package Layout

```
workflow-connectors/
├── pkg/
│   ├── registry/                     # catalogue metadata — stdlib only
│   │   ├── registry.go               # Type*, FieldKind*, RetryPolicy, Field, Definition, All(), IsIdempotentMethod
│   │   └── definitions.go            # storage / send-email / rest-call / chat-notify definitions
│   └── connectors/                   # facade
│       ├── connector.go              # Connector interface, New(Config)
│       ├── config.go                 # Config
│       ├── errors.go                 # re-exported sentinels; ErrorClass, ClassOf, IsTransient, IsPermanent, IsRetryable
│       ├── retry.go                  # DecideRetry, RetryDecision (LogAttrs), AutoRetryAllowed
│       ├── internalauth.go           # WithDepartments / DepartmentsFromContext, WithTenant / TenantFromContext
│       ├── aliasconfig/              # alias schema, Load, Validate, Resolve*  (yaml.v3)
│       ├── shared/                   # errors and classes, field helpers, header names, tenant/departments ctx, size limits, HTTP clients, ClientCache (stdlib)
│       ├── docref/                   # document refs: Service (Create/Lookup/Open/Read/Delete), Store + ContentStore ports, memory stores (tests)
│       │   ├── s3content/            # S3 content store (aws-sdk-go-v2 s3): system of record for document content
│       │   └── valkeystore/          # Valkey metadata store (go-redis): create-only Lua write, WAITAOF, CheckDurability
│       ├── storage/                  # core: ProviderClient, ProviderConstructor, cache, fetch/upload/delete, mock
│       │   ├── gocloud/              # adapter: aws-s3 · azure-blob · gcp-gcs (gocloud.dev/blob)
│       │   └── googledrive/          # adapter: google-drive (drive/v3)
│       ├── sendemail/                # core: ProviderClient, AttachmentLimiter, EmailMessage, outcomes (SendError), cache, attachments, mock
│       │   ├── ses/                  # adapter: aws-ses (aws-sdk-go-v2 sesv2)
│       │   ├── sendgrid/             # adapter: sendgrid (v3 Mail Send over our own HTTP client; sendgrid-go helpers/mail)
│       │   ├── msgraph/              # adapter: microsoft-365 (Graph sendMail + oauth2/clientcredentials)
│       │   └── gmail/                # adapter: google-workspace (gmail/v1) + raw MIME builder
│       ├── documents/                # Drive document registry: Store port, state machine, MemoryStore
│       │   └── sqlstore/             # PostgreSQL Store on platform-pgcommon v2 + migrations/ (ApplySchema)
│       ├── sendintent/               # send-email duplicate-request protection: Store port, MemoryStore
│       │   └── sqlstore/             # PostgreSQL Store on platform-pgcommon v2 + migrations/ (ApplySchema)
│       ├── chatnotify/               # core + mock (no real provider)
│       ├── restcall/                 # core (net/http)
│       ├── sqlquery/                 # core (net/http) — not wired into New
│       └── documentextract/          # core + mock — not wired into New
├── test/                             # black-box tests (§14)
│   ├── unit/                         # no build tag, no Docker: per-package suites, retry matrix, worker scenarios, REG-01…REG-10
│   ├── postgres/                     # -tags integration: documents, sendintent, sendemail stores on PostgreSQL / PgBouncer
│   └── integration/                  # -tags integration: multireplica, s3content, valkeystore, workflow scenarios
├── docs/
│   ├── lld/workflow-connectors-library-lld.md   # this document (Part I module, Part II system design)
│   ├── integration/connector-worker.md          # consumer wiring guide for execution_service's connector worker
│   ├── runbooks/                     # document-refs, email-delivery, retry-semantics
│   └── architecture/mermaid/         # diagrams embedded in ARCHITECTURE.md
├── tools/go.mod                      # golangci-lint, pinned outside the library's module graph
├── scripts/                          # merge_coverage.py, init-floci.sh
├── docker-compose.yml                # test stack: PostgreSQL 17, PgBouncer, Valkey 8, floci S3
├── .go-arch-lint.yml                 # component + vendor rules (§3.2)
├── .golangci.yml                     # revive, errcheck, staticcheck, bodyclose, depguard (pgcommon-only)
├── .github/workflows/                # ci, validate-quality, validate-test, release, docs, changelog-check
├── .github/scripts/                  # arch-lint, coverage-gate, pr-summary, release helpers
└── Makefile                          # §14.6, §20
```

### 3.1 Third-party dependencies

```
require (
    github.com/Azure/azure-sdk-for-go/sdk/storage/azblob  v1.8.0     // storage/gocloud
    github.com/BCBP-SOLUTIONS-FZC-LLC/platform-pgcommon/v2 v2.0.1    // documents/sqlstore and sendintent/sqlstore — the org's database layer (pool, tx, migrate)
    github.com/aws/aws-sdk-go-v2                          v1.43.7    // storage/gocloud, sendemail/ses (aws.Config, NopRetryer)
    github.com/aws/aws-sdk-go-v2/credentials              v1.19.37   // storage/gocloud, sendemail/ses
    github.com/aws/aws-sdk-go-v2/service/s3               v1.102.2   // storage/gocloud, docref/s3content
    github.com/aws/aws-sdk-go-v2/service/sesv2            v1.67.0    // sendemail/ses
    github.com/google/uuid                                v1.6.0     // docref ids, sendemail, documents/sendintent memory stores, googledrive, mocks
    github.com/redis/go-redis/v9                          v9.22.0    // docref/valkeystore — the platform's Valkey client
    github.com/sendgrid/sendgrid-go                       v3.16.1+incompatible // sendemail/sendgrid (helpers/mail only)
    github.com/stretchr/testify                           v1.12.1    // tests only
    gocloud.dev                                           v0.46.0    // storage/gocloud
    golang.org/x/oauth2                                   v0.37.0    // storage/gocloud, googledrive, msgraph, gmail
    google.golang.org/api                                 v0.293.0   // storage/googledrive, sendemail/gmail
    gopkg.in/yaml.v3                                      v3.0.1     // aliasconfig
)
```

The module requires `go 1.26.9`. The AWS SDK's `config` package is only an indirect dependency: clients are built from `aws.Config` literals holding the tenant's values (§8.3), never from the worker's AWS environment.

`golangci-lint` is pinned in a separate tools module (`tools/go.mod`, run as `go tool -modfile=tools/go.mod golangci-lint`) so its dependencies never enter a consumer's module graph, and `govulncheck` and `go-arch-lint` are pinned `go run`/`go install` versions — none is a runtime dependency. There is **no** dependency on an OpenBao client or the Temporal SDK. The only `platform-*` module is `platform-pgcommon/v2`; the Valkey client (go-redis) is confined to `docref/valkeystore`.

**Microsoft Graph uses a hand-rolled `sendMail` POST plus `oauth2/clientcredentials`, not `msgraph-sdk-go`** — the SDK is Kiota-generated and pulls a large dependency tree for a single authenticated JSON POST (Decision #22).

### 3.2 Dependency rules (enforced in CI)

`pkg/registry` and `shared` → standard library only. `aliasconfig` → `yaml.v3`. Connector cores → `registry`, `shared`, `aliasconfig`, `sendintent`, `docref`, `uuid`. `documents` and `sendintent` → `shared`, `uuid`. `documents/sqlstore` and `sendintent/sqlstore` → their port, `shared` (error classes) and `platform-pgcommon/v2` only — the only components allowed a database library, and they use pgcommon's own API (`pgcommon.RunInTx`, `Tx`, `Row`, `TxOptions`, `ErrNoRows`, the `Is*` error classifiers); `pgx` is only an indirect dependency, through pgcommon (§18.3). `docref/valkeystore` → `docref` and `go-redis` — the only component allowed the Valkey client. `docref/s3content` → `docref` and the AWS SDK. `docref` → `uuid` only. Provider adapters → their core, `documents`, `shared`, `uuid` and the cloud SDKs. Facade (`pkg/connectors`) → cores, `shared`, `aliasconfig`, `registry`, `sendintent`, `docref` — **never an adapter**; the worker injects adapter constructors through `Config`.

Enforced by `go-arch-lint` (`.go-arch-lint.yml`, pinned v1.15.0, `make arch-lint`, the **Architecture lint** step of `validate-quality.yml`). Unlike a service's config, it sets `depOnAnyVendor: false` and declares every third-party module as a vendor granted per component: the core/adapter split exists so that only adapters may import a cloud SDK, and the linter enforces that, not only module-internal layering. `deepScan: false` (import-level checks only); `_test.go` files are excluded. Verified to report both an SDK import in a core and an internal import in `registry`.

More rules sit alongside it: the CI workflow-file checks reject HTML-escaped operators in `.github/workflows/` and `.github/scripts/`, and any third-party action not pinned to a full commit SHA (`owner/action@<40-hex> # vX.Y.Z`; a tag can be moved to different code, a SHA cannot); and `golangci-lint` runs `bodyclose`, so every HTTP response body in `restcall`, `sqlquery`, `sendgrid`, `msgraph` and `googledrive` is closed.

### 3.3 Platform integration

The module integrates with the platform through its two consumers (§18) and the infrastructure clients the worker passes in (PostgreSQL pool, Valkey client, S3 client). It shares conventions with the `platform-libs` modules — tag-pinned private module, `GOPRIVATE=github.com/BCBP-SOLUTIONS-FZC-LLC/*`, CHANGELOG-gated releases — and imports only `platform-pgcommon/v2`, in `documents/sqlstore` and `sendintent/sqlstore`.

---

## 4. Data Model — In-Process State

The module has **no database of its own**. Its in-process state lives in memory for the lifetime of the map `New` returned (§4.2–§4.4). Persisted state lives in the worker's infrastructure: document content in the platform's S3 document bucket and reference metadata in Valkey (§4.1), and the Drive document registry (§4.5) and send intents in the worker's PostgreSQL, created by `ApplySchema` from the worker's migration step.

### 4.1 Document references (`docref`) — content in S3, metadata in Valkey

**S3 is the system of record for document content; Valkey stores only the reference.** A ref is a lightweight pointer to an S3 object, not a snapshot. Consumers never read content from Valkey: they resolve refs through `docref.Service`, the only path from a ref to bytes.

| | Store | Package | Holds |
|---|---|---|---|
| Content | The platform's S3 document bucket | `docref/s3content` (`ContentStore` port) | One object per ref at `<prefix><tenant_id>/<uuid>` |
| Metadata | Valkey | `docref/valkeystore` (`Store` port) | One hash per ref at `docref:{uuid}` — no content, no credentials |

| Valkey field | Notes |
|---|---|
| `reference_id` | The ref, `docref:<uuid v4>` — also the key; opaque to every consumer (§S6.2) |
| `tenant_id` | Owner; checked on every resolution |
| `bucket`, `object_key` | The S3 location of the content |
| `content_type`, `size`, `sha256` | Metadata; size and SHA-256 are verified on every resolution |
| `created_at`, `updated_at` | Valkey server time (`TIME`) in Unix ms; equal, since refs are never updated |

A hash is a few hundred bytes (`TestPut_StoresMetadataOnly` asserts the nine fields and < 1 KiB), so Valkey memory is O(refs × metadata): 10,000 refs ≈ 5 MB, whatever the documents weigh.

**Credentials.** The content store uses the **worker's own** S3 client (its IAM role or credential chain) on a platform-owned bucket — the platform's credential model. Tenant credentials are never used: `send-email` has none for storage, and a source may be Azure, GCS or Drive. A ref never carries credentials.

**Write — `Create(ctx, tenant, contentType, bytes)`.**
1. Validate the tenant ID (it scopes the object key: no `/`, `\`, `.`, `..`, or empty).
2. `PutObject` to `<prefix><tenant_id>/<uuid>`.
3. Compute SHA-256 and size.
4. `Store.Put` the metadata: one Lua script (`EXISTS` → `TIME` → `HSET` → `PEXPIREAT`), create-only (`ErrExists`) and idempotent — if the key already holds exactly this ref (`HMGET` compare; ref IDs are random, so only this call's own earlier attempt whose reply was lost can have written it) the script rewrites `updated_at` with its own value, so `WAITAOF` has a write on this connection to confirm, and succeeds — and `WAITAOF 1 <WaitReplicas> <timeout>` (`WaitReplicas` 0 by default: primary AOF only; see the runbook's Failover section) sent with it as **one pipeline on the key's node** — `WAITAOF` confirms only the writes of its own connection (`valkeystore.ErrNotPersisted` if not confirmed).
5. Return the ref. A returned ref always points at an existing object; if step 4 fails, the object is deleted (best effort; the lifecycle rule removes any leftover).

**Resolve — `Open` (stream) / `Read` (bytes, with a limit).**
1. `HGETALL docref:{uuid}`. Missing, expired, or another tenant's → `ErrNotFound` (DocumentReferenceNotFound).
2. Tenant check, **before any S3 request**: the record's `tenant_id` is the caller's, its `bucket` is the configured bucket, and its `object_key` is exactly `<prefix><tenant_id>/<uuid>` for the ref's own ID. Otherwise `ErrIntegrityViolation` — a forged or corrupted record can never reach any other object, another tenant's or the same tenant's.
3. `Read` only: `size` above the caller's limit → `ErrTooLarge`, decided without downloading.
4. `GetObject`. Missing → `ErrSourceMissing` (DocumentSourceMissing).
5. The object's length must equal `size` (checked before reading the body); the stream must end at exactly `size` bytes and hash to `sha256` (checked as it streams). Otherwise `ErrIntegrityViolation` (DocumentIntegrityViolation), returned by the reader in place of `io.EOF`.

`Open` never buffers: a consumer must treat content as valid only after reading to `io.EOF` without error. `send-email` (attachments, 25 MiB total) and `storage` upload (50 MiB) use `Read`, so the document is verified in full before anything is sent or uploaded. The three resolution failures (and `ErrTooLarge`) are `ErrValidation` to the workflow and keep their `docref` sentinel for `errors.Is`; a Valkey or S3 outage is `ErrUpstream` for `storage` (retryable) and `ErrNotDelivered` for `send-email`.

**Delete.** `Service.Delete(tenant, id)` removes the metadata (only for the owning tenant — a Lua script compares `tenant_id`), then the object; once the metadata is gone nothing resolves the ref.

**Consistency.** Both writes finish before a ref is returned, so whichever replica runs the next task resolves it — no replica affinity. Metadata is create-only and never changes; concurrent creators of one ID: exactly one wins. The object can change underneath a ref (an S3 overwrite or delete); every resolution detects it and fails closed, so altered content is never served.

**Durability and retention.** Valkey: `appendonly yes`, `appendfsync always|everysec`, `maxmemory-policy noeviction` (ref keys carry a TTL, so any eviction policy could drop a live ref), checked at startup by `valkeystore.CheckDurability`. Refs expire after 24 h (Valkey key TTL). S3: SSE-KMS, public access blocked, a lifecycle rule expiring the document prefix after 2 days (an object outlives its ref; orphans are removed); `s3content.Store.Check` verifies the bucket at startup. Operations: `docs/runbooks/document-refs.md`.

**Rationale — content in S3, pointers in Valkey (rev 1.9).** Rev 1.8 stored the document bytes in the Valkey hash, which made Valkey memory grow with document volume and size (up to 50 MiB per ref, for 24 h). Content belongs in object storage: S3 is built for it, priced for it and already holds the documents. Valkey keeps what suits it — small key-value records with native TTL, atomic create-only writes and AOF durability. The trade-off is that a pointer can go stale where a snapshot cannot; the stored size and SHA-256, verified on every resolution, turn a changed or missing object into an explicit failure instead of silently serving different bytes. Earlier: rev 1.7 used a PostgreSQL table behind a per-replica cache (removed in rev 1.8 as a redundant persistence layer).

- Written by `storage` fetch, or upload of literal content, with `createDocument: true`; read by `storage` upload (content of the form `docref:…`) and by `send-email` (every `attachments` entry). An upload whose content is a ref returns that same ref.
- Requires `WithTenant`.
- Without `Config.DocRefs`, refs are disabled (`ErrValidation`) — never kept only in worker memory. `docref.NewMemoryService` is for unit tests only.

### 4.2 Provider-client caches

`storage.Connector.clients` and `sendemail.Connector.clients`: a `shared.ClientCache[ProviderClient]` keyed by a SHA-256 hex digest (§6), capped at 256 entries, with reference-counted client lifetimes (§6.2–§6.4).

### 4.3 Alias configuration (`aliasconfig.Config`)

Supplied by the worker; captured **by value** at `New` (Decision #21 — no hot reload). YAML shape accepted by `Load`:

```yaml
version: 1
restCall:
  - alias: tender-get-application         # unique within restCall
    method: GET                           # GET POST PUT PATCH DELETE HEAD OPTIONS (case-insensitive)
    baseURL: http://tender-service.internal
    pathTemplate: /api/v1/applications/{applicationId}
    timeout: 5s                           # optional; 0 = caller's context only
sqlQuery:
  - alias: get-active-tenants             # unique within sqlQuery
    baseURL: http://tender-service.internal
    path: /internal/queries/execute
    queryId: get-active-tenants           # resolved by the owning service, never SQL text
    paramCount: 1                         # >= 0; 0 = not checked
    timeout: 5s
```

`Validate` rejects a missing or duplicate alias, an invalid method, a missing `baseURL`/`pathTemplate`/`path`/`queryId`, and a negative `paramCount`.

### 4.4 Registry types

| Type | Fields |
|---|---|
| `Definition` | `Type`, `DisplayName`, `Description`, `Inputs []Field`, `Outputs []Field`, `Retry RetryPolicy` |
| `Field` | `Name`, `Kind FieldKind`, `Required`, `EnumValues`, `Condition` (free text, e.g. "required for aws-s3"), `Description`; `IsSecretRef()` |
| `FieldKind` | `string`, `bool`, `int`, `float`, `enum`, `list`, `map`, `any`, `document_ref`, `secret_ref`, `timestamp` |
| `RetryPolicy` | `safe`, `unsafe`, `conditional`, `not-delivered` |

---

### 4.5 Drive document registry (`documents`, `documents/sqlstore`)

Google Drive has no atomic create-if-absent, so a uniqueness authority outside Drive decides which upload owns a document (Part I §8.4). `documents.Store` is the port; `sqlstore.Store` implements it on PostgreSQL 13+; `documents.MemoryStore` has the same semantics for tests and a single process.

**`connector_documents`** — one row per logical document.

| Column | Type | Notes |
|---|---|---|
| `id` | `uuid` PK | `gen_random_uuid()` — the database-generated document ID; tags the Drive file (`appProperties.connectorDocumentId`) |
| `tenant_id`, `provider`, `container`, `filename` | `text` | The identity. **`uq_connector_documents_identity UNIQUE (tenant_id, provider, container, filename)`** — the only uniqueness authority. `container` is the Drive folder ID |
| `state` | `text` | `PENDING_UPLOAD`, `UPLOADING`, `AVAILABLE`, `FAILED`, `DELETING` (`chk_connector_documents_state`) |
| `version` | `bigint` | Incremented by every transition |
| `object_id` | `text` | The Drive file ID once written; kept across a replace and across `FAILED` |
| `content_type`, `size_bytes` | | From the last successful upload |
| `owner`, `lease_expires_at`, `claimed_at` | | The claiming attempt and its lease. `chk_connector_documents_owner`: a busy row always has both owner and lease; an idle row has neither |
| `last_error`, `failed_attempts` | | The last failure, kept for audit; `failed_attempts` is never reset |
| `created_at`, `updated_at` | `timestamptz` | |

**`connector_document_attempts`** — one row per finished attempt (`available`, `failed`, `deleted`), with `attempt`, `error`, `started_at`, `finished_at`. No foreign key, so a deleted document keeps its trail.

**Access** is through platform-pgcommon v2: every statement runs in `pgcommon.RunInTx`, a single statement in a transaction of its own and an audited transition with its audit insert, so the pool's `StatementTimeout` and `LockTimeout` bound every call. pgcommon applies those only to transactions it opens; `Pool.WithConn` would not. `Claim` is one `INSERT … ON CONFLICT DO UPDATE … WHERE` (insert, or take over an idle or expired row). Database errors carry a retry class (`shared.WithClass`): serialization failure, deadlock, lock not available, query canceled, operator intervention, connection exception and insufficient resources are transient; a closed pool is unknown (the process is shutting down, so no retry here can succeed, but the call is not wrong); anything else is classified by its cause. Migrations ship in `documents/sqlstore/migrations` (golang-migrate format) and are applied by `sqlstore.ApplySchema`, tracked in `connector_documents_migrations`.

**Transitions** (each one atomic; every transition after `Claim` is a compare-and-set on `(id, owner, live lease)`):

| Method | SQL | From → to |
|---|---|---|
| `Claim` | one `INSERT … ON CONFLICT ON CONSTRAINT uq_connector_documents_identity DO UPDATE … WHERE state IN ('AVAILABLE','FAILED') OR lease_expires_at <= now() RETURNING`; no row returned → `Get` (not claimed; if the busy row was deleted meanwhile, claim again, at most 3 tries) | none / `AVAILABLE` / `FAILED` / expired → `PENDING_UPLOAD` |
| `ClaimExisting` | the takeover `UPDATE` only; `ErrNotFound` when no row | as above, for deletes |
| `Advance` | one owner-checked `UPDATE` | `PENDING_UPLOAD` → `UPLOADING` or `DELETING` |
| `Complete` | owner-checked `UPDATE` + audit `INSERT`, one transaction | → `AVAILABLE`, owner and lease cleared |
| `Fail` | owner-checked `UPDATE` + audit `INSERT`, one transaction | → `FAILED`, owner and lease cleared |
| `Remove` | owner-checked `DELETE` + audit `INSERT`, one transaction | row deleted |

No transaction is held open across a Drive call. The lease (`documents.DefaultLease`, 15 min) is set from the database clock, so worker clock skew does not matter. `sqlstore.Store.PruneAttempts(ctx, cutoff, batch)` deletes audit rows that finished before `cutoff`, at most `batch` per call (index from migration 000002); the worker runs it on a schedule, repeating while it returns `batch`.

## 5. API Contract — Public Go API

### 5.1 Conventions

- **Input** is `map[string]any` as decoded from JSON: strings, `bool`, `[]any`, `map[string]any`. Helpers read fields leniently — a wrong type reads as the zero value — and validation then reports the field as missing.
- **Output** is `map[string]any` whose **top-level keys only** are addressable by `IOMapping.Outputs.Source` (Decision #13).
- **Errors** match a kind sentinel for `errors.Is` (`ErrValidation`, `ErrUpstream`, `ErrMissingInternalAuth`, `ErrMissingTenant`, `ErrNotDelivered`, `ErrDeliveryUnknown`; §17) and carry one retry class, read with `connectors.ClassOf` (§9.2); the underlying provider or network error stays in the chain for `errors.As`.
- **Context** carries cancellation and, for `rest-call`/`sql-query`, the departments (`WithDepartments`).

### 5.2 Facade

```go
type Connector interface {
    Type() string
    Execute(ctx context.Context, input map[string]any) (map[string]any, error)
}

type Config struct {
    Aliases            aliasconfig.Config
    HTTPClient         *http.Client                              // copied, never follows redirects, HTTP/1.1 only; nil → default transport, each call bounded by its alias timeout (else 30 s)
    InternalToken      string                                    // required
    StorageProviders   map[string]storage.ProviderConstructor
    SendEmailProviders map[string]sendemail.ProviderConstructor
    ChatNotifyClient   chatnotify.ProviderClient                 // nil → every chat-notify call ErrValidation
    SendIntents        sendintent.Store                          // nil → a messageKey is ErrValidation (§5.4.2a)
    DocRefs            *docref.Service                           // nil → document refs disabled (ErrValidation) (§4.1)
}

func New(cfg Config) (map[string]Connector, error)

var ErrValidation, ErrUpstream, ErrMissingInternalAuth, ErrMissingTenant,
    ErrNotDelivered, ErrDeliveryUnknown error                    // = shared.*
type ErrorClass = shared.Class                                   // ClassUnknown, ClassTransient, ClassPermanent
func ClassOf(err error) (ErrorClass, string)
func IsTransient(err error) bool
func IsPermanent(err error) bool
func IsRetryable(err error) bool                                 // transient only; prefer DecideRetry
func DecideRetry(connectorType string, err error, method string) RetryDecision
func AutoRetryAllowed(connectorType string, err error, method string) bool
func WithDepartments(ctx context.Context, departments []string) context.Context
func DepartmentsFromContext(ctx context.Context) ([]string, bool)
func WithTenant(ctx context.Context, tenantID string) context.Context
func TenantFromContext(ctx context.Context) (string, bool)
```

`New` returns `storage`, `send-email`, `chat-notify` and `rest-call`; it fails on an empty `InternalToken` and panics on a duplicate `Type`. `sql-query` and `document-extract` are constructible from their own packages (`sqlquery.New`, `documentextract.New`) but are not in `New` and not in `registry.All()` (§16 OQ-7).

Provider ports and adapter constructors:

| Port | Signature | Adapters (`NewProvider`) |
|---|---|---|
| `storage.ProviderClient` | `Fetch(ctx, bucket, key, maxBytes) ([]byte, string, error)` (an object larger than `maxBytes` fails with `ErrTooLarge`) · `Upload(ctx, bucket, key, []byte, contentType) error` · `Delete(ctx, bucket, key) error` (a missing object is already deleted: `nil`) | `storage/gocloud` (aws-s3, azure-blob, gcp-gcs — selects by `provider`), `storage/googledrive` |
| `sendemail.ProviderClient` | `Send(ctx, EmailMessage) (messageID string, error)` | `sendemail/ses`, `sendemail/sendgrid`, `sendemail/msgraph`, `sendemail/gmail` |
| `sendemail.AttachmentLimiter` (optional) | `MaxAttachmentBytes() int64` — the provider's own attachment limit, below the core's 25 MiB | all four email adapters (SendGrid 20 MiB, Graph 3 MiB, SES and Gmail 25 MiB) |
| `chatnotify.ProviderClient` | `CreateChannel` · `InviteToChannel` · `PostMessage` | none yet |
| `documentextract.ProviderClient` | `Analyze(ctx, AnalyzeRequest) (AnalyzeResult, error)` | none yet |

`ProviderConstructor` is `func(ctx context.Context, params map[string]any) (ProviderClient, error)`; `params` is the call's full input, so the adapter reads its own credential fields. `googledrive.NewProvider(docs documents.Store)` returns one.

Store ports (implemented by the worker-facing stores; each has an in-memory implementation for tests):

| Port | Methods | Implementations |
|---|---|---|
| `documents.Store` | `Claim(ctx, Identity, attempt, lease) (Document, claimed bool, error)` · `ClaimExisting` (same) · `Advance(ctx, docID, attempt, State)` · `Complete(ctx, docID, attempt, Result)` · `Fail(ctx, docID, attempt, reason)` · `Remove(ctx, docID, attempt) error` · `Get(ctx, Identity) (Document, found bool, error)` | `documents/sqlstore` (`sqlstore.New(*pgcommon.Pool)`), `documents.MemoryStore` |
| `sendintent.Store` | `Reserve(ctx, tenantID, messageKey, resendFrom int, token) (Intent, reserved bool, error)` · `Get(ctx, tenantID, messageKey) (Intent, found bool, error)` · `Record(ctx, intentID, attempt int, Status, providerMessageID, detail) error` (`ErrStaleRecord` for a superseded attempt) | `sendintent/sqlstore` (`sqlstore.New(*pgcommon.Pool)`), `sendintent.MemoryStore` |
| `docref.Store` (metadata) | `Put(ctx, Ref) (Ref, error)` (create-only, `ErrExists`; idempotent for the same ref) · `Get(ctx, tenantID, id) (Ref, found bool, error)` · `Delete(ctx, tenantID, id) error` | `docref/valkeystore` (`valkeystore.New(redis.UniversalClient, Options{TTL, DisableWaitAOF, WaitAOFTimeout, WaitReplicas})`), `docref.MemoryStore` |
| `docref.ContentStore` | `Bucket() string` · `Put(ctx, key, []byte, contentType) error` · `Open(ctx, bucket, key) (io.ReadCloser, size int64, error)` (`ErrObjectNotFound`) · `Delete(ctx, bucket, key) error` | `docref/s3content` (`s3content.New(API, bucket)`; `Check(ctx, keyPrefix)` at startup), `docref.MemoryContent` |

`docref.NewService(refs Store, content ContentStore, opts ...ServiceOption)` (option `WithKeyPrefix`) is what connectors use: `Create(ctx, tenantID, contentType, []byte) (Ref, error)`, `Lookup(ctx, tenantID, id) (Ref, error)`, `Open(ctx, tenantID, id) (Ref, io.ReadCloser, error)`, `Read(ctx, tenantID, id, maxBytes) (Ref, []byte, error)`, `Delete(ctx, tenantID, id) error`.

### 5.3 Connector catalogue

| Type | Registry | In `New` | Providers | Retry |
|---|---|---|---|---|
| `storage` | yes | yes | `aws-s3`, `azure-blob`, `gcp-gcs`, `google-drive` (real) | `safe` |
| `send-email` | yes | yes | `sendgrid`, `aws-ses`, `microsoft-365`, `google-workspace` (real) | `not-delivered` |
| `rest-call` | yes | yes | — (internal services only) | `conditional` |
| `chat-notify` | yes | yes | none yet — `ErrValidation` without `Config.ChatNotifyClient` | `unsafe` |
| `sql-query` | no | no | — (internal services only) | (§S6.4: safe) |
| `document-extract` | no | no | none yet — `ErrValidation` without a client | (§S6.4: safe) |

### 5.4 Key connector specifications

#### 5.4.1 `storage`

| Input | Rule |
|---|---|
| `provider` | Required; must be a key of `Config.StorageProviders` (no default, no mock fallback — Decision #20) |
| `operation` | `fetch` \| `upload` \| `delete` |
| `bucket`, `key` | Required. For `google-drive`, `bucket` is the folder ID and `key` the file name |
| `content` | Upload only, required. A value of the form `docref:…` is resolved (same tenant, unexpired) to its bytes from S3 and verified (size, SHA-256) before the upload — not found, source missing or integrity violation is `ErrValidation`; otherwise the literal string is uploaded |
| `contentType` | Upload; read by the core though not a registry field; defaults to the ref's content type when `content` is a ref |
| `createDocument` | Fetch: `true` registers the bytes and returns `contentRef`; `false` returns `content` inline, allowed up to 1 MiB — base64 with `contentEncoding: base64` when the bytes are not valid UTF-8. Upload of literal content: `true` also registers the uploaded bytes and returns `contentRef` |
| `contentEncoding` | Upload, literal `content`: `utf-8` (default) or `base64` |
| Credentials | `aws-s3`: `accessKey`, `secretKey`, `region` (all required); `azure-blob`: `azureAccountName`, `azureAccountKey`; `gcp-gcs`: `gcpServiceAccountKey` (service-account JSON only), `projectId` optional; `google-drive`: `driveServiceAccountKey` |

| Output | Operation |
|---|---|
| `contentType`, `sizeBytes`, `fetchedAt` (UTC), and `content` + `contentEncoding` or `contentRef` | fetch |
| `sizeBytes`, and `contentRef` when `content` was a ref (the same ref) or `createDocument` is `true` (a new ref to the uploaded bytes) | upload |
| `{}` | delete |

Drive specifics: uploads, fetches and deletes go through the document registry (§4.5, §8.4) — only the claimant writes, files are addressed by their recorded Drive file ID, and a concurrent upload, fetch-before-first-write or delete of the same document gets `*documents.InProgressError`. A re-upload replaces the same file in place; a file placed outside the connector (untagged) is adopted by the first upload of its name, and with no registry row is fetched or deleted by name, oldest untagged match first; delete of an absent document succeeds; the tenant comes from `connectors.WithTenant` (`ErrMissingTenant` without it); every call sets `SupportsAllDrives`/`IncludeItemsFromAllDrives` (Decision #20(4)).

#### 5.4.2 `send-email`

| Input | Rule |
|---|---|
| `provider` | Required; must be a key of `Config.SendEmailProviders` |
| `senderEmail`, `receiverEmail` | Required; each must be exactly one bare address (`net/mail.ParseAddress`), so a comma-separated value cannot add recipients |
| `templateId` or `body` | One is required. `microsoft-365`/`google-workspace` require `body` (no server-side templates) |
| `subject`, `senderName`, `receiverName`, `contentType` (`text/plain` \| `text/html`) | Optional |
| `attachments` | List of doc refs; each must resolve in the store, and together they may total at most 25 MiB of raw bytes, and at most the provider's own limit (a client implementing `sendemail.AttachmentLimiter`: SendGrid 20 MiB, Microsoft Graph inline `sendMail` 3 MiB, SES and Gmail 25 MiB), else `ErrValidation` (`deliveryOutcome: not_delivered`, permanent). Named `attachment-N` + extension from content type (`.pdf`, `.png`, `.jpg`, `.txt`, `.csv`) |
| Credentials | `sendgrid`: `apiKey`; `aws-ses`: `accessKey`, `secretKey`, `region`; `microsoft-365`: `tenantId`, `clientId` (plain), `clientSecret`; `google-workspace`: `serviceAccountKey` (domain-wide delegation impersonating `senderEmail`, `gmail.send` scope) |

Optional `messageKey`, `resend` and `resendAttempt` drive duplicate-request protection (§5.4.2a). Output: `sent`, `messageId`, `sentAt` (UTC), `deliveryOutcome` (`accepted` | `not_delivered` | `unknown`, returned with every result — a failure before sending, validation included, is `not_delivered`), `sendIntentId` (with `messageKey`), and `sendIntentWarning` when the outcome could not be recorded on the intent; a duplicate adds `duplicate`, `status`, `attempts` and, when accepted, `providerMessageId`. `messageId` is SendGrid's `X-Message-Id`, SES's `MessageId`, Gmail's message id, and **empty for `microsoft-365`** (Graph's `sendMail` returns 202 with no id). SendGrid treats a status `>= 300` as failure.

#### 5.4.2a Email delivery semantics

**Contract.** `send-email` is **not idempotent**. A send that is not retried is **at-most-once**; a send retried after an uncertain outcome is **effectively at-least-once** — it may be delivered twice. **Exactly-once delivery is not provided.** No supported provider accepts a caller-supplied idempotency key, and a lost success response cannot be distinguished from a send that never reached the provider.

**Failure classification.** Every failed send is exactly one of:

| Class | `deliveryOutcome` | When | Resend can duplicate? |
|---|---|---|---|
| `ErrNotDelivered` | `not_delivered` | A 4xx (incl. 429), or the request provably never left: dial/DNS/TLS failure, context done before sending, client not built, OAuth token refused | No |
| `ErrDeliveryUnknown` | `unknown` | Everything else: timeout, reset, EOF, 5xx, crash, an unclassified client error (conservative default) | **Yes** |

Adapters classify their own provider's responses (`sendemail.ClassifyStatus`, `ClassifyTransport`); the core treats anything unclassified as unknown. `*sendemail.SendError` exposes outcome, provider and HTTP status, and never matches `ErrUpstream` — it hides an `ErrUpstream` cause from `Unwrap`. A failed send returns its output map (`sent: false`, `deliveryOutcome`) together with the error.

**Automatic retry only when it cannot duplicate (rev 1.10).** Retry policy `not-delivered`: `connectors.DecideRetry` allows a retry only for a **transient** `ErrNotDelivered` (DNS failure, connection refused or connect timeout, 429/408 or an AWS throttling code): the provider provably never accepted the message. A permanent `not_delivered` (invalid recipient, unverified sender: other 4xx) and every `ErrDeliveryUnknown` (class unknown) are never retried automatically. A send intent whose last outcome was `not_delivered` is re-reserved without `resend` (atomically: of concurrent retries one proceeds). Below the connector: the SES client is built with `aws.NopRetryer` (the SDK's standard retryer would resend `SendEmail` up to 3 times on throttling, 5xx and transient errors — verified against an `httptest` server: 3 requests without it, 1 with it); Gmail's `messages.send` (`gensupport.SendRequest`) makes one attempt; the SendGrid and Graph POSTs are ours, built per call over `shared.ProviderHTTPClient` (HTTP/1.1 only, no redirects, 2-minute bound), and are not retried by `net/http`.

**Duplicate-request protection (optional, not idempotency).** With `Config.SendIntents` (`sendintent.Store`; PostgreSQL `sendintent/sqlstore` on platform-pgcommon, table `connector_send_intents`, `uq_connector_send_intents_key (tenant_id, message_key)`, `sqlstore.ApplySchema` with its own tracking table, `connector_send_intents_migrations`), a call with `messageKey` reserves the key in one atomic statement before anything is sent. A second request with the same key sends nothing: if the intent is `accepted` it succeeds (`duplicate: true`, `providerMessageId`; a redelivered job is idempotent), otherwise it gets `*sendintent.DuplicateRequestError` (matches `ErrValidation`, carries `Attempts`). `resend: true` with `resendAttempt: n` is the explicit opt-in to send again: `Store.Reserve(ctx, tenant, key, resendFrom, token)` re-reserves (`attempts` + 1) only while `attempts = n` and the intent is not `pending` (or has been pending, unchanged, for `sendintent.StalePendingAfter`, 15 minutes: the worker stopped mid-send) — a compare-and-swap, so a redelivered resend sees `n+1` and is a duplicate; after an `unknown` outcome a resend may deliver a duplicate. Each call stamps its reservation with a fresh token (`reservation_token`, migration 000002); if `Reserve` returns an error, the call reads the intent back (`Store.Get`, bounded, detached) and proceeds only when it carries its token and is still `pending`. The outcome is recorded on the intent; recording never turns a successful send into an error. A `messageKey` without a configured store is `ErrValidation`, never silently ignored; it needs `WithTenant`.

**Observability** is the worker's (the library has none): log `deliveryOutcome`, provider, status, `sendIntentId`; count `connector_send_email_outcomes_total{provider,outcome}`; alert on `unknown`. Operator steps: `docs/runbooks/email-delivery.md`.

#### 5.4.3 `rest-call`

| Input | Rule |
|---|---|
| `endpointAlias` | Required; resolved with `aliasconfig.ResolveEndpoint` |
| `pathParams` | Map; each `{name}` in `pathTemplate`'s path is replaced with its `url.PathEscape`d value, which must not be empty, `.` or `..`; one after the template's `?` is `url.QueryEscape`d; a missing name or unterminated `{` is `ErrValidation` |
| `queryParams` | Map; values rendered with `shared.FormatParam` and URL-encoded; a key the template's query string already sets is `ErrValidation`, never overridden |
| `body` | Any; JSON-encoded and sent with `Content-Type: application/json` when present and non-nil |

Request: alias method (upper-cased), `baseURL` (trailing `/` trimmed) + rendered path (`pathTemplate` must start with `/`; the built URL must keep `baseURL`'s scheme and host with no userinfo, else `ErrValidation` — `shared.NewInternalRequest`), headers `x-internal-token` and `x-departments` (comma-joined; a department that is empty or contains `,`, CR or LF is `ErrMissingInternalAuth`), over HTTP/1.1 only. Output: `status`, `headers` (multi-values joined with `, `), `body` (JSON-decoded when the response `Content-Type` contains `json` and the body is exactly one JSON value, numbers as `json.Number`; else a string; `nil` when empty). A status `>= 300` returns **both** the output and `ErrUpstream`, classified by the status alone; redirects are never followed (§10.2). The response body is capped at 10 MiB: over it, a `2xx` is `ErrValidation`, while an error status's body is cut to its first 10 MiB (or to what arrived, if its read fails) and returned as a string with `bodyTruncated: true`; and `Set-Cookie` is dropped from `headers`. Numeric path and query parameters are rendered as plain numbers (`shared.FormatParam`), never in exponent form.

#### 5.4.4 `sql-query` (not wired)

`queryAlias` required; `params` must have exactly `paramCount` entries when `paramCount > 0`. POSTs `{"queryId", "params"}` to `baseURL + path` (`path` must start with `/`; same URL check as `rest-call`) with both headers; returns `{resultSet: []map[string]any}`, numbers as `json.Number`. Never sees SQL text and never holds a database connection (Decision #17).

#### 5.4.5 `chat-notify`

`method`: `create-channel` (`channelName`, `visibility` required) → `messageId` = channel ID; `invite-to-channel` (`channelNameOrId`, non-empty `users`) → empty `messageId`; `post-message` (`channelOrUser`, `message` required; `thread` optional) → `messageId`. Output always carries `sent: true`. `messageBlock` and `attachments` are in the registry but not yet read by the core.

#### 5.4.6 `document-extract` (not wired)

`documentLocation`: `inline` (`documentRef` required) or `s3` (`documentBucket`, `documentName` required, `documentVersion` optional → ref `s3://bucket/name/version`). Flags `analyzeForm`, `analyzeSignatures`, `analyzeLayout`, `analyzeQueries` (the last requires `query`). Output: `rawText`, plus `fields`/`signaturesDetected`/`answers` per flag, and `confidence` when present.

### 5.5 Error mapping

See §17. The worker decides every automatic retry with `connectors.DecideRetry` (§9.2), never from the kind sentinel alone: `ErrValidation` is always permanent, `ErrUpstream` may be transient, permanent or unknown, and `ErrMissingInternalAuth`/`ErrMissingTenant` are worker wiring failures.

---

## 6. Caching Design

### 6.1 Keys, values, lifetime

| Cache | Key = SHA-256 hex over (NUL-separated) | Value | Lifetime |
|---|---|---|---|
| `storage` | provider, `bucket`, then sorted `accessKey`, `azureAccountKey`, `azureAccountName`, `driveServiceAccountKey`, `gcpServiceAccountKey`, `projectId`, `region`, `secretKey` | `storage.ProviderClient` | Until it is the least recently used entry when a 257th is added, `ResetClients`/`Invalidate` retires it, or the process ends |
| `send-email` | provider, `senderEmail`, then sorted `accessKey`, `apiKey`, `clientId`, `clientSecret`, `region`, `secretKey`, `serviceAccountKey`, `tenantId` | `sendemail.ProviderClient` | Same |

`bucket` is in the storage key because gocloud binds a client to one bucket at open time; `senderEmail` is in the email key because Gmail binds one impersonated mailbox (Decision #23(1)).

### 6.2 Lookup algorithm

1. Read `provider`; empty → `ErrValidation`. Not in the constructor map → `ErrValidation`.
2. `Acquire(key, build)`: under the mutex, on a hit increment the entry's reference count and return a handle.
3. On a miss, release the mutex and call the constructor with the full input. An error passes through `shared.Classify` and nothing is cached.
4. Under the mutex: if another call stored the key meanwhile, take a reference on that entry and close the client just built (unused). Otherwise, if the cache holds 256 entries, retire the least recently acquired one (§6.3), then store the new entry with one reference.
5. `Execute` uses `handle.Client()` and calls `handle.Release()` when it returns (`defer`). `Release` is idempotent; `Client()` after `Release` panics, because it could race with `Close`.

### 6.3 Invalidation

A rotated credential produces a different key, so the old client is never hit again. Entries are **retired** on overflow (the least recently acquired entry), by `Connector.ResetClients()` (all) or `ClientCache.Invalidate(key)` (one): a retired entry leaves the map at once, so the next call builds a new client, but its client is closed only when its reference count reaches zero — immediately if no handle is out, otherwise on the last `Release`. Close is decided under the mutex and runs after it, exactly once per client, via `io.Closer` (gocloud: bucket and its own S3 transport; SES, SendGrid, Gmail, Graph, Drive: the idle connections of their own `shared.ProviderHTTPClient` transport). A client that is not an `io.Closer` (the mocks) is dropped.

### 6.4 Failure mode

The cache cannot fail independently: it is a local map. Two concurrent misses for one key both construct; the first stored client wins and the other is closed unused. A `Close` error is ignored — the client is being discarded and no caller is left to report it to.

### 6.5 Cache invariants

| # | Invariant |
|---|---|
| CACHE-1 | A client is reused only for an identical provider and credential set (and bucket or sender). |
| CACHE-2 | The cache is never the source of any result; every entry is rebuildable from the call's own input. |
| CACHE-3 | The mutex is never held across a constructor or provider call. |
| CACHE-4 | The cache is bounded (256 entries per connector). |
| CACHE-5 | No caller ever receives a closed client, and no in-flight call loses its client to another goroutine's reset or overflow. |
| CACHE-6 | Every retired client is closed exactly once, after its last handle is released; nothing leaks across reset cycles. |

---

## 7. Event Architecture

**Not applicable.** The module publishes and consumes no events, has no outbox and no schema-registry presence. Connector-task events (`WorkflowTaskCreated`, the Valkey Stream hop) and their dedup are `execution_service`'s (§S5.4, §S6.5).

---

## 8. Key Execution Flows

### 8.1 Storage fetch feeding an email attachment

1. Worker resolves storage and email credentials for the job's tenant.
2. `byType["storage"].Execute(ctx, {provider, operation: fetch, bucket, key, createDocument: true, …})` → core validates, gets or builds the client (§6.2), calls `Fetch`, `docref.Service.Create`s the document (object to S3, then metadata to Valkey, both before returning), returns `contentRef` (`docref:<uuid>`).
3. `IOMapping` stores `contentRef` in a workflow variable; a later task maps it into `attachments`.
4. `byType["send-email"].Execute(ctx, {…, attachments: [contentRef]})` → core resolves the ref through `docref.Service.Read` (metadata from Valkey, tenant check, object from S3, size + SHA-256 verified) — on whichever replica runs this task — builds `EmailAttachment{attachment-1.pdf, application/pdf, bytes}`, calls `Send` on the adapter.

The two tasks may run on different replicas, and either may restart in between. Pinned by `TestSendEmail_AttachmentFromStorage_ResolvesViaSharedDocRefs` and the `TestMultiReplica_*` suite (§14).

### 8.2 `rest-call` to an internal service

1. Worker sets `connectors.WithDepartments(ctx, ["<dept_uuid>:<role>", …])`.
2. `Execute` checks `endpointAlias`, resolves it (unknown → `aliasconfig.ErrUnknownAlias`), then requires departments in the context (absent → `ErrMissingInternalAuth`, **before** building any request).
3. Renders the path, encodes the body, applies the alias timeout, sets both headers, sends.
4. Decodes the response (at most 10 MiB); returns the map, with `ErrUpstream` on `>= 300`. A redirect is returned, not followed.

### 8.3 Provider client construction (per adapter)

| Adapter | Construction | Validation error |
|---|---|---|
| `gocloud` aws-s3 | `aws.Config{Region, Credentials}` from the tenant's values only (never the worker's AWS environment) → `s3blob.OpenBucket` | `accessKey`/`secretKey`/`region` missing |
| `gocloud` azure-blob | shared-key credential → container client for `https://<account>.blob.core.windows.net/<bucket>` → `azureblob.OpenBucket` | account name/key missing |
| `gocloud` gcp-gcs | `google.CredentialsFromJSONWithType(..., ServiceAccount, devstorage.read_write)` → `gcsblob.OpenBucket` | key missing; non-service-account key rejected |
| `googledrive` | `drive.NewService` with service-account JSON, `DriveScope` | key missing |
| `ses` | `aws.Config{Region, Credentials}` from the tenant's values only → `sesv2.NewFromConfig` | `accessKey`/`secretKey`/`region` missing |
| `sendgrid` | `apiKey` + `shared.ProviderHTTPClient()`; each send builds its own v3 Mail Send request | `apiKey` missing |
| `msgraph` | `clientcredentials.Config` (token URL per `tenantId`, scope `graph.microsoft.com/.default`) → HTTP client | any of `tenantId`/`clientId`/`clientSecret` missing |
| `gmail` | `google.JWTConfigFromJSON(key, gmail.send)`, `Subject = senderEmail` → `gmail.NewService` | key or `senderEmail` missing |

The core passes a constructor error through `shared.Classify`: these validation errors stay `ErrValidation`, so a missing or invalid credential is never retried; any other construction error becomes `ErrUpstream`, classified by its cause (§9.2). Gmail, Graph, GCS and Drive token requests run on a background context with a 30-second HTTP client (`shared.DefaultHTTPTimeout`), because cached clients outlive the call that built them. The email adapters and Drive call their APIs through `shared.ProviderHTTPClient`: HTTP/1.1 only, no redirects, each call bounded by 2 minutes (`shared.ProviderHTTPTimeout`) unless the caller's context ends sooner; gocloud uses its own transport per client, bounded by the caller's context.

---

### 8.4 Drive upload through the document registry

1. Identity = (`WithTenant` tenant, `google-drive`, folder ID, filename). No tenant → `ErrMissingTenant`.
2. `Claim` with a fresh attempt ID. Not claimed → return `*documents.InProgressError{DocumentID, State}` (matches `ErrUploadInProgress` and `ErrUpstream`); **Drive is never called**.
3. `Advance → UPLOADING`. `ErrOwnershipLost` → `ErrUpstream`, nothing written.
4. Write, bounded by the lease (from the claim, minus a 1-minute margin): the recorded `object_id` → `update`; else the oldest file tagged `connectorDocumentId = id` (left by a crashed attempt) → adopt and `update`; else the oldest **untagged** file of the same name in the folder (placed there outside the connector) → adopt and `update`; else `create` with the filename as metadata and the tag. Every `update` also sets the tag, so an adopted file belongs to the document from then on. A recorded file that no longer exists (deleted in the Drive UI) falls through to the next choice. After adopting or creating, any other file tagged with the document (a create that completed after its deadline) is removed, best effort.
5. Drive error → `Fail` and return the classified error. Success → `Complete` with the file ID, type and size; a `Complete` that fails → `Fail`. Every registry write after the claim's Drive work (`Complete`, `Fail`, `Remove`) runs on a context detached from the caller's cancellation and bounded by 10 s (`recordTimeout`), so the row leaves its busy state at once without hanging on an unreachable database.
6. A registry failure after a successful claim (`Advance`, `Complete`, `Remove`) releases the row with `Fail` the same way, so a retry claims it at once instead of getting `InProgressError` until the lease expires.

Delete is the same shape: `Claim` — inserting a row when the name has none, so a delete is serialised with any upload that could adopt the same file — → `Advance → DELETING` → delete the recorded file and every file tagged with the document; a document that holds neither (no row before the delete, or an upload that never wrote) deletes instead what an upload would adopt: the oldest untagged same-name file → `Remove`. A file already gone counts as deleted, so a retry after a failed `Remove` (row released `FAILED`) finds nothing left and removes the row. A same-name file the document never held (placed after its own file existed) is left alone. Fetch takes no ownership: it reads the recorded file, returns `InProgressError` while the row is busy and has no file yet, and with no row reads the oldest untagged same-name file — so after a delete it finds nothing rather than stale content. Files tagged with another document (another tenant's, when tenants share a folder) are never adopted, read or deleted by name. Residual: two tenants sharing a folder can both adopt the same untagged file at once (Drive has no conditional update); identity includes the tenant, so this needs two tenants writing one folder.

## 9. Concurrency, Consistency, and Failure Handling

### 9.1 Concurrency

The map `New` returns is shared by every concurrent `Execute` in the worker's pools. `restcall`, `sqlquery`, `chatnotify` and `documentextract` are value types over immutable configuration. `storage`/`sendemail` guard their client caches with a `sync.Mutex` held only around map access; `docref.Service`, `docref/valkeystore` and `docref/s3content` hold only their clients, and Valkey runs each create-only script atomically, so concurrent writes and resolutions from every replica need no coordination. All mocks are mutex-guarded. The full suite runs under `-race` as a blocking CI gate.

### 9.2 Idempotency and retry policy

The library **never retries**, and no provider client it builds retries a send. Every error carries one class, and the worker retries within its per-type timeout only where **`connectors.DecideRetry(type, err, method)`** allows it. The decision is deterministic: (1) only a **transient** error may be retried, never a permanent or an unknown one; (2) the type's `registry.Definition.Retry` (Decision #6) must also allow it.

| Class | Meaning | Examples |
|---|---|---|
| Transient | May succeed later | S3 `SlowDown`/`RequestTimeout`/`InternalError`/`ServiceUnavailable`; HTTP 408/425/429/500/502/503/504; timeouts, DNS, connection refused/reset; Drive rate-limit reasons (403); `InProgressError`; `ErrOwnershipLost`; document-registry and send-intent PostgreSQL serialization failures, deadlocks, lock/statement timeouts, operator intervention, connection exceptions, insufficient resources; `docref.ErrUnavailable`; email throttling or never-sent |
| Permanent | Cannot succeed as is | `ErrValidation` (incl. `docref.ErrNotFound`/`ErrSourceMissing`/`ErrIntegrityViolation`, duplicate `messageKey`); `ErrMissingTenant`, `ErrMissingInternalAuth`; S3 `NoSuchKey`/`AccessDenied`/`InvalidBucketName`; HTTP 3xx and other 4xx; gocloud `NotFound`/`PermissionDenied`/`InvalidArgument`; email rejections |
| Unknown | Cannot tell | Unrecognised errors; cancellation; other 5xx (501, 505, 507); `ErrDeliveryUnknown` |

Classes are attached where the error is understood: `shared.ClassifyCause` (AWS `ErrorCode()`, then `HTTPStatusCode()`, then network cause), `shared.HTTPStatusError` (rest-call, sql-query), the gocloud adapter (portable codes for Azure/GCS), the Drive adapter (googleapi reasons and status), `*sendemail.SendError.ErrorClass` (outcome, then provider code, status or cause), `*documents.InProgressError`, and storage's document-ref mapping. Unknown is the conservative default: such an error is not retried, and a rising unknown rate means a mapping is missing. Full tables: `docs/runbooks/retry-semantics.md`.

| Type | Policy | A transient failure is retried when… |
|---|---|---|
| `storage` | `safe` | Always. Fetch and delete are idempotent; S3/Azure/GCS upload overwrites; Drive upload updates in place; a document-ref retry mints a new ref |
| `send-email` | `not-delivered` | The failure is `ErrNotDelivered`: nothing reached the provider, so no duplicate is possible. `ErrDeliveryUnknown` is class unknown and never retried; the SES client's SDK retries are disabled (§5.4.2a) |
| `chat-notify` | `unsafe` | Never: a retry posts twice |
| `rest-call` | `conditional` | `registry.IsIdempotentMethod(alias.Method)`: GET, HEAD, PUT, DELETE, OPTIONS, TRACE |

`RetryDecision` reports `Retry`, `Class`, `Reason`, `Policy` and the deciding `Rule` (`transient`, `transient, not delivered`, `permanent`, `unknown`, `non-idempotent method`, `may have been delivered`, `policy unsafe`, `unknown connector type`); `LogAttrs()` renders them for the worker's log line. The worker bounds transient retries (exponential backoff with jitter, an attempt limit within the task timeout), so a persistent transient failure still ends, and a permanent one never loops.

§S6.5 step 0 makes a task's connector run once per task regardless of redelivery; that guarantee is the worker's, not this module's.

### 9.3 Failure scenarios

| Scenario | Detection | Result |
|---|---|---|
| `provider` omitted or not configured | `clientFor` | `ErrValidation`; no provider call |
| Credential field absent or invalid | Adapter constructor | `ErrValidation` ("build <provider> client: …"), message names the provider |
| Provider SDK/API error | Adapter returns error | `ErrUpstream` with the provider's message |
| Object not found (fetch) | Provider error / Drive lookup empty | `ErrUpstream` |
| Attachment ref not found (expired, another tenant's, never created), its object missing, or its object altered | `docref.Service.Read` | `ErrValidation` (+ `docref.ErrNotFound` / `ErrSourceMissing` / `ErrIntegrityViolation`) before the client is built |
| Valkey or S3 unreachable, Valkey full (`noeviction` refuses writes), or metadata not confirmed in the AOF (`ErrNotPersisted`) | `docref.Service` | storage: `ErrUpstream` (retried, `safe` — a retry mints a new ref); send-email attachments: `ErrNotDelivered` (nothing sent) |
| Unknown alias | `aliasconfig.Resolve*` | `ErrValidation`, also matching `aliasconfig.ErrUnknownAlias` |
| Departments missing from context, empty, or one empty or containing `,`/CR/LF | `shared.DepartmentsHeaderValue` | `ErrMissingInternalAuth` (permanent); no request sent |
| Internal service unreachable / timeout | `http.Client.Do` error | `ErrUpstream`, with the transport error in the chain |
| Internal service redirects | `3xx` status (redirects not followed) | `ErrUpstream`; `rest-call` also returns the response map |
| Payload over a size limit | `shared.ReadAllLimited`, inline/attachment/upload checks | `ErrValidation` |
| Malformed or multi-recipient email address | `validateAddress` | `ErrValidation`; nothing sent |
| Connector with no provider (`chat-notify`, `document-extract`) | `Execute` | `ErrValidation`; nothing reported as sent |
| send-email: provider timeout, reset, 5xx, or crash mid-send | Adapter classification | `ErrDeliveryUnknown`, `deliveryOutcome: unknown`, class unknown; never retried automatically; may have been delivered |
| send-email: provider 4xx, or request never left | Adapter classification | `ErrNotDelivered`, `deliveryOutcome: not_delivered`; transient (429/408/throttling code, DNS, connection failure) → retried; otherwise permanent → not retried |
| send-email: same `messageKey` again | `sendintent.Store.Reserve` | Accepted intent: success, `duplicate: true`. Otherwise `*sendintent.DuplicateRequestError` (`ErrValidation`); nothing sent |
| Second Drive upload/delete of a document already owned by a live call | `Claim` not claimed | `*documents.InProgressError` (`ErrUploadInProgress`, `ErrUpstream`); Drive never called |
| Drive write fails after the claim | Adapter error | Row → `FAILED` with `last_error`; audit row; the error is returned; a retry claims the `FAILED` row |
| Registry write fails after the claim (`Advance`, `Complete`, `Remove`) | Store error | Row released → `FAILED` (bounded, detached context); a retry claims it at once; a delete's retry finds its files gone and removes the row |
| Registry error from PostgreSQL | `documents/sqlstore` | Serialization failure, deadlock, lock/statement timeout, operator intervention, connection exception, insufficient resources → transient; closed pool → unknown (the process is shutting down; another replica can run the task) |
| Worker dies after Drive create, before `Complete` | Row stays busy until its lease expires | The next claimant adopts the tagged file — no second file |
| Owner outlives its lease | `Complete`/`Fail` CAS | `ErrOwnershipLost`; the new owner's state is never overwritten |
| Drive call without `WithTenant` | `identityFor` | `ErrMissingTenant`; never retryable |
| Internal service `>= 400` (any `>= 300`) | Status check | `ErrUpstream`; `rest-call` also returns the response map |
| Response body undecodable | Decode error | `ErrUpstream`, permanent (`sql-query` checks the status first, then decodes) |
| Context cancelled | SDK / `net/http` | The underlying error, wrapped as `ErrUpstream` where the adapter wraps it |

**Failure invariants:**

| # | Invariant |
|---|-----------|
| FAIL-1 | **No silent success.** A missing or unconfigured provider is `ErrValidation`; no connector falls back to an in-memory mock — `storage`/`send-email` (Decision #20(1)) and, since rev 1.2, `chat-notify`/`document-extract`. |
| FAIL-2 | **No default provider.** An omitted `provider` is never treated as `aws-s3` or any other (Decision #20(2)). |
| FAIL-3 | **Fail closed on missing internal auth.** `rest-call`/`sql-query` send nothing without departments in the context, and `New` refuses an empty `InternalToken`. |
| FAIL-4 | **No library-held partial state.** A failed call leaves at most a cached client and, for a fetch, nothing registered (registration follows a successful fetch). |
| FAIL-5 | **Validation before side effects.** Every core validates its required fields, and `send-email` resolves attachments, before building a client or calling a provider. |

### 9.4 Consistency guarantees

- **Doc ref → bytes**: a ref resolves to exactly the bytes stored (checksum-verified), on every replica, until it expires — across restarts and deployments.
- **Output shape**: each connector returns the same top-level keys for the same operation on success; `rest-call` returns them on `>= 300` too.
- **Catalogue ↔ implementation**: `registry.All()` lists exactly the types `New` builds (pinned by `TestAll_FourTypes` and `TestNew_FourTypes`).

### 9.5 Operational invariants

| # | Invariant |
|---|---|
| OPS-1 | Call `New` once per process; each call creates new client caches. Point `Config.DocRefs` at `docref.NewService(valkeystore.New(...), s3content.New(...))` with the worker's own S3 client, never at `docref.NewMemoryService`, and fail startup when `valkeystore.CheckDurability` or `s3content.Store.Check(ctx, keyPrefix)` fails. |
| OPS-2 | Register every provider the registry advertises; an advertised but unregistered provider fails every call that names it. |
| DOC-1 | At most one `connector_documents` row exists per tenant + provider + folder + filename — enforced by `uq_connector_documents_identity`, never by reading first. |
| DOC-2 | Only the row's owner writes to Drive for that document; every transition after `Claim` is a compare-and-set on owner and live lease. |
| DOC-3 | A busy row always has an owner and a lease; an idle row has neither (`chk_connector_documents_owner`). |
| DOC-4 | Drive files are addressed by the recorded file ID; Drive folder contents never decide uniqueness. |
| DOC-5 | Every finished attempt is recorded in `connector_document_attempts` in the same transaction as the state change. |
| DOC-6 | `documents.DefaultLease` exceeds the worker's per-connector timeout, so a live call never loses its lease. |

---

## 10. Security

### 10.1 Tenant isolation

The connectors take the tenant only where they store something on the tenant's behalf: Drive registry rows, document refs and send intents read it from `connectors.WithTenant` (`ErrMissingTenant` without it); everything else is tenant-agnostic. Isolation rests on these facts: (1) the worker resolves only the job's own tenant's credentials (§S6.2); (2) every provider client is keyed by its full credential set (CACHE-1), so one tenant's client is never handed to another; (3) doc refs are random UUIDs, so one task cannot name another's bytes. Document refs are additionally scoped to their tenant: the stored tenant and the object key's tenant prefix are checked before any S3 request (§4.1). (4) S3 and SES clients are built from the tenant's values only, so no worker AWS setting (region, endpoint, profile) reaches a tenant's client.

### 10.2 Network isolation

- `rest-call`/`sql-query` reach only aliases, never raw URLs; the internal-host allowlist on alias `baseURL` is enforced at alias write in `definition_service` (§S9, `CONNECTOR_ALIAS_ALLOWED_HOSTS`).
- Provider adapters reach only their provider's public API endpoints, through the SDK's default endpoints; the Graph base URL is the constant `https://graph.microsoft.com/v1.0`.

### 10.3 Input validation

- Path parameters are `url.PathEscape`d; query parameters are URL-encoded.
- Gmail raw MIME strips CR and LF from sender/receiver names and addresses, subject and content type, and Q-encodes names and subject (Decision #23(2)); the other three email adapters build structured requests.
- Graph's sender is `url.PathEscape`d into the request path.
- `gcp-gcs` accepts only a `service_account` key — an `external_account` (workload identity) key could make the worker read its own filesystem or metadata identity.
- Alias config is validated on `Load` (§4.3).

### 10.4 Authorization rules

| Call | Authentication | Authorization |
|---|---|---|
| `rest-call` / `sql-query` → internal service | `x-internal-token` (`Config.InternalToken`) | `x-departments` (`dept_uuid:role` pairs) — evaluated by the receiving service |
| Adapter → provider | The tenant's resolved credential | The provider's own (bucket policy, sender verification, Workspace delegation, Graph app permissions) |

---

## 11. Observability

**None in the module, by design.** It never logs and imports no logging, metrics or tracing library (`retry.go` uses only the standard library's `log/slog.Attr` type, for `RetryDecision.LogAttrs`), so it adds nothing to a consumer's dependency graph and never decides how a consumer observes it. Observability is the worker's: it times and counts each `Execute`, labels it by type and by `errors.Is` class (§17), and records outcomes.

What the module gives the worker to observe with: a stable `Type()` per connector; sentinel-classified errors whose messages name the connector, operation, alias or provider (e.g. `connectors: upstream call failed: rest-call "tender-get-application": status 503`); and the full `rest-call` response on a failed status. Error messages never include credential values.

---

## 12. Configuration

The module reads **no environment variables and no files** except through `aliasconfig.Load(path)`, which the caller invokes. All configuration is passed to `New`:

| `Config` field | Required | Default | Used by |
|---|---|---|---|
| `InternalToken` | yes | — (`New` fails) | `rest-call` |
| `Aliases` | no | empty (every alias unknown) | `rest-call` |
| `HTTPClient` | no | a client with no client-wide timeout: each call is bounded by its alias's `timeout`, else 30 s (`shared.CallTimeout`). Any client is copied, set never to follow redirects, and limited to HTTP/1.1: an `*http.Transport` (or none) is cloned with HTTP/2 off; any other `RoundTripper` is used as-is and must not enable HTTP/2 | `rest-call` |
| `StorageProviders` | no | empty (every storage call `ErrValidation`) | `storage` |
| `SendEmailProviders` | no | empty (every email call `ErrValidation`) | `send-email` |
| `ChatNotifyClient` | no | none — every call `ErrValidation` | `chat-notify` |
| `DocRefs` | no | none — document refs disabled (`ErrValidation`); production: `docref.NewService(valkeystore.New(vk, valkeystore.Options{TTL, WaitAOFTimeout, WaitReplicas}), s3content.New(s3Client, bucket), docref.WithKeyPrefix(prefix))` | `storage`, `send-email` |
| `SendIntents` | no | none — a `messageKey` is `ErrValidation`; production: `sendintent/sqlstore.New(pool)` | `send-email` |

Recommended production wiring:

```go
storageProviders := map[string]storage.ProviderConstructor{
    "aws-s3": gocloud.NewProvider, "azure-blob": gocloud.NewProvider,
    "gcp-gcs": gocloud.NewProvider, "google-drive": googledrive.NewProvider(docsqlstore.New(pool)), // documents/sqlstore
}
emailProviders := map[string]sendemail.ProviderConstructor{
    "sendgrid": sendgrid.NewProvider, "aws-ses": ses.NewProvider,
    "microsoft-365": msgraph.NewProvider, "google-workspace": gmail.NewProvider,
}
```

The full worker wiring — infrastructure, environment, startup checks, per-task context and the retry loop — is in [`docs/integration/connector-worker.md`](../integration/connector-worker.md).

---

## 13. Distribution and Versioning

### 13.1 Artefacts

No image, binary, chart or migration. A release is a Git tag `vX.Y.Z` consumed through the Go module proxy with `GOPRIVATE=github.com/BCBP-SOLUTIONS-FZC-LLC/*`.

| Consumer | Imports | Compiles in |
|---|---|---|
| `definition_service` | `pkg/registry` | Standard library only |
| `execution_service` worker | `pkg/connectors` + chosen adapter packages | Cloud SDKs only through the adapters it imports |

### 13.2 Release pipeline

`release.yml` on a version tag, in platform-pgcommon's structure: **verify** (a manual dispatch only from `main` or the tag; the tag points at the checkout, any of several tags on one commit; the commit is on `main`; the tag's major version matches the module path, `/v2` for v2.x; `CHANGELOG.md` has `## [X.Y.Z]`, which a prerelease may share) → Validate / Test ∥ Validate / Quality at the tag ∥ **API compatibility, blocking** ∥ Build image (placeholder) → Trivy and Smoke tests (placeholder) after test + image, **Build source archive** after both gates (pgcommon's "Build binaries") (apidiff against the previous stable tag; a non-major release fails on an incompatible exported-API change) → Trivy filesystem scan (CRITICAL/HIGH/UNKNOWN fail; skips `tools/`; SARIF to the Security tab) and CycloneDX SBOM → **publish**: the changelog section with a `go get` line, a source archive, `checksums.txt` over it and the SBOM signed with Cosign (keyless, bundle `checksums.txt.sigstore.json`), and the GitHub Release. A tag with a `-` suffix is a pre-release. `ci.yml` runs the same gates on every PR and push to `main`, skipping the build/test jobs on a documentation-only change (`detect-changes.sh`) while still reporting the required checks; `docs.yml` checks the diagrams on such changes; `api-compat` there is a warning.

### 13.3 Versioning

SemVer per `VERSIONING.md`: MAJOR for a breaking change to `Connector`, `Definition`/`Field` or a `Type*`/`FieldKind*` constant; MINOR for a new type, optional field or capability; PATCH for fixes. README additionally treats a change to `Config`, a connector's input/output names or a sentinel as breaking. The provider-adapter move is classified in §16 OQ-5.

---

## 14. Testing Strategy

Tests live in `./test`, laid out like iam-org-membership's. Only **white-box** tests that need unexported code stay beside it in `pkg/` (`*_internal_test.go` or package-internal `_test.go` files): the provider adapters (`sendemail/{gmail,msgraph,sendgrid,ses}`, `storage/{gocloud,googledrive}`), which swap the SDK behind each adapter's private interface; `shared`'s client cache, which inspects its internal entries; `valkeystore`'s pure durability rules; and the duplicate-type panics of `connectors.New` and `registry.All`. Every other package's black-box tests are under **`test/unit/<package>`** (`aliasconfig`, `chatnotify`, `connectors`, `docref`, `documentextract`, `documents`, `registry`, `restcall`, `s3content`, `sendemail`, `sendintent`, `shared`, `sqlquery`, `storage`, `valkeystore`), and the infrastructure suites under **`test/integration/{multireplica,s3content,valkeystore}`** and **`test/postgres/{documents,sendintent,sendemail}`** (tag `integration`; the store contract suites run their in-memory leg there too). At the root of each tree: **`test/unit`** (no build tag, no Docker) holds the retry decision matrix (every connector type × error kind × method, `TestRetryMatrix_*`), worker scenarios that run the worker's retry loop through `connectors.New` (throttled-then-accepted email delivered once, persistent connect failure bounded by the attempt limit, lost response not retried and its duplicate refused, S3 `SlowDown` retried, `NoSuchKey` once, integrity violation with nothing sent), and regression guards `REG-01`…`REG-10`, one per fixed production defect; **`test/postgres`** (tag `integration`) checks deployment shape (`TestPG01`…`TestPG03`): four replicas migrating both schemas at once (applied exactly once, idempotent), and the document registry and send intents through PgBouncer in transaction mode; **`test/integration`** (tag `integration`) runs whole workflows (`TestIT01`…`TestIT06`) across worker replicas on real Valkey, floci S3 and PostgreSQL behind PgBouncer: the invoice workflow with a throttled send, an object deleted or overwritten between tasks, a lost response refused as a duplicate on another replica, an expired reference, and rest-call recovering for GET but not POST. Infrastructure comes from **`docker-compose.yml`** (PostgreSQL 17, PgBouncer, Valkey 8 with the production persistence flags, floci): `make docker-up` / `make docker-down`; `make test-postgres`, `make test-integration`, `make test` and `make test-ci` start it themselves; only `make test-unit` runs without Docker. CI starts the same compose stack and runs with `CI` set, so no infrastructure test can skip.

### 14.1 Connector core tests

External `_test` packages under `test/unit/<package>` drive `Execute` through mocks or `httptest.Server`: every validation branch; unconfigured and omitted provider; constructor errors wrapped as `ErrUpstream`; provider-error propagation; `createDocument` minting a resolvable ref; the templateless-provider rule; unresolvable attachments; different senders not sharing a cached client; both auth headers on the wire; missing departments failing before any request; unknown alias making no network call; query and path parameter handling; `paramCount` mismatch.

### 14.2 Provider adapter tests

Package-internal tests swap the SDK behind each adapter's private interface (`driveFilesAPI`, `sesAPI`, `sendGridAPI`, `graphAPI`, `gmailMessagesAPI`); `gocloud` runs against `gocloud.dev/blob/memblob`; `msgraph`'s real HTTP path runs against `httptest`. Each covers its missing-credential error and the request it builds (template vs body, HTML vs text, attachments, raw MIME, Drive update-in-place and idempotent delete).

### 14.3 Contract tests

`test/unit/registry`: every connector's field names, kinds, required flags and enum values; retry policies; `IsIdempotentMethod`; the secret-description rule. `test/unit/connectors`: `New` builds exactly the registry's four types and refuses an empty token.

### 14.4 Integration tests

None against real providers: they would need live tenant credentials. Real-provider verification belongs to the worker's environment-level tests.

**Valkey, S3 and PostgreSQL** are the real dependencies under test. The **multi-replica suite** (`test/integration/multireplica`) starts separate worker replicas — each its own `connectors.New`, Valkey client and S3 client — on one Valkey and one document bucket and covers: write on A, read on B; the writer terminating right after the write; a rolling deployment; concurrent writers across three replicas; twelve concurrent creates of one ID (exactly one winner); delete on one replica seen at once on another, then recreate; a missing ref (`ErrValidation`); and tenant isolation. `docref/s3content` (floci S3, `TEST_S3_ENDPOINT`) covers: create and resolve; a missing object (`ErrSourceMissing`); a same-size overwrite (checksum) and a different-size overwrite (size, before the body is read) (`ErrIntegrityViolation`); a 48 MiB object streamed without buffering; 24 concurrent resolutions; delete. `docref/valkeystore` covers metadata-only hashes (exactly nine fields, < 1 KiB), create-only, missing key, delete and recreate, tenant isolation, TTL and the durability rules; with `TEST_VALKEY_DOCKER` it restarts its own Valkey containers to show metadata recovered from the AOF alone, documents still resolving from S3 after the restart, and a non-durable server refused. `docref` (in memory) covers forged metadata refused before S3, a 256 MiB stream with < 8 MiB allocated, and a failed metadata write leaving no object; `TestDocRefs_UnresolvableSource_FailsBeforeAnySideEffect` shows nothing is sent or uploaded from a missing or altered object. The `documents/sqlstore` and `sendintent/sqlstore` contract suites (`test/postgres/{documents,sendintent}`) run every case against the in-memory store and PostgreSQL; the `storage/googledrive` scenarios run their in-memory leg in the unit suite and their PostgreSQL leg (`stores_postgres_test.go`, tag `integration`) in the postgres suite. `make test-integration` starts the `docker-compose.yml` stack (PostgreSQL 17, PgBouncer, Valkey 8 with `--appendonly yes --appendfsync always --maxmemory-policy noeviction`, floci S3); CI starts the same stack and, with `CI` set, fails rather than skips without `TEST_POSTGRES_DSN`, `TEST_PGBOUNCER_DSN`, `TEST_VALKEY_ADDR`, `TEST_VALKEY_DOCKER` or `TEST_S3_ENDPOINT`. The Drive tests cover the five required race scenarios — two simultaneous uploads, a second request during an upload, failure then retry, different filenames, 24 concurrent uploads of one filename — plus crash adoption, adoption of a hand-placed same-name file (and uploads and deletes of it racing across replicas), release of the row when a registry write fails after the claim, stale-owner refusal, tenant separation, the database refusing a duplicate row (`23505`) or an ownerless busy row, and the audit trail. All run under `-race`.

### 14.5 Security regression tests (canonical)

| Test | Guards |
|---|---|
| `TestBuildRawMIME_ReceiverNameWithCRLF_CannotInjectHeaders` | Email header injection (Decision #23(2)) |
| `TestSendEmail_SameCredentialsDifferentSender_DoesNotShareCachedClient` | Cross-sender client reuse (Decision #23(1)) |
| `TestOpenGCSBucket_NonServiceAccountCredential_IsRejected` | Non-service-account GCS credentials |
| `TestRestCall_MissingDepartments_FailsClosedBeforeAnyRequest`, `TestSQLQuery_MissingDepartments_FailsClosed` | Internal calls without caller identity |
| `TestRestCall_AttachesMandatoryHeaders`, `TestSQLQuery_AttachesMandatoryHeadersAndBindsParams` | Both headers always sent |
| `TestSecretRefDescriptions_DoNotClaimAuthorSupplied` | Credential-provenance wording (rev 8.13) |

### 14.6 Coverage and test targets

`make test-unit` runs the unit suite without Docker; `make test-postgres` and `make test-integration` run one suite each against the compose stack; `make test` runs all three in parallel. `make test-ci` runs the three suites in parallel under `-race`, as iam-org-membership does: **unit** (`test/unit/...` plus the white-box tests in `./pkg/...`, no Docker), **postgres** (`test/postgres/...` and `storage/googledrive`'s PostgreSQL leg, `-tags integration`) and **integration** (`test/integration/...`, `-tags integration`). Each writes its own profile under `.coverage/` across `./pkg/...` (`-coverpkg`); `scripts/merge_coverage.py` merges them into one `coverage.out` (max count per block). The merged total is gated by `.github/scripts/coverage-gate.sh` at `COVERAGE_THRESHOLD` **98%**, set in `validate-test.yml`; the last run (2026-10-10) reported **99.96%**. The threshold is a ratchet: raise it as coverage improves, never lower it. `make ci` runs tidy-check (library and tools module), fmt-check, vet, lint (both with and without the `integration` tag), arch-lint, docs-check, test-ci and build.

---

## 15. Data Handling and Compliance

### 15.1 What passes through

| Data | Where | Retention in the module |
|---|---|---|
| Provider credentials | `Execute` input; captured inside cached provider clients | Until the cache resets or the process exits; never logged, never returned |
| Document bytes | `storage` fetch/upload; the platform's S3 document bucket; email attachments | Fetched-with-`createDocument` and uploaded bytes are stored as S3 objects (SSE-KMS, tenant-prefixed keys) until the bucket lifecycle rule expires them (2 days); Valkey holds only metadata (tenant, S3 location, size, SHA-256), never bytes; otherwise only for the call |
| Email addresses, names, subject, body | `send-email` input → provider | Only for the call |
| Internal request/response bodies | `rest-call`/`sql-query` | Only for the call; returned to the worker |

### 15.2 Ownership boundary

The module is a processor, never a store of record: apart from the Drive document registry's identity and audit rows (§4.5), which hold no document content, it persists nothing, and every datum belongs to the tenant and the workflow instance that supplied it. Erasure and retention obligations are the owning services'. Document bytes behind refs are stored as objects in the platform's S3 document bucket under tenant-prefixed keys and removed by the bucket lifecycle rule (2 days); their reference metadata (no bytes) lives in Valkey for the ref TTL (24 h) (§4.1). Worker memory holds bytes only for the call.

### 15.3 Third-party transfer

Every provider call is one the tenant configured with its own credentials, to its own provider account. No v1 connector forwards a document to a third party the tenant did not choose (§S8; `llm-verify` was removed for this reason, Decision #3).

---

## 16. Open Questions and Sign-off Register

| # | Open question / decision | Owner | Status |
|---|---|---|---|
| OQ-1 | **Document refs were worker-local.** A ref minted on one replica did not resolve on another and a restart lost it. **Resolved (rev 1.7; revised rev 1.8 and rev 1.9)** — document content is stored in the platform's S3 document bucket (`docref/s3content`, the system of record for content) and reference metadata in Valkey (`docref/valkeystore`), shared by every replica: create-only, confirmed in the AOF before a ref is returned, tenant-checked before any S3 request, size and SHA-256 verified on every resolution, 24 h TTL, never evicted (`noeviction`, checked at startup). Verified by multi-replica, S3 and Valkey-restart integration tests (§14.4). | Workflow team | Resolved |
| OQ-2 | **Two error paths bypassed the sentinels:** an unknown alias and `http.Client.Do` failures. **Resolved (rev 1.2)** — an unknown alias is `ErrValidation` (still matching `aliasconfig.ErrUnknownAlias`); a transport failure is `ErrUpstream`. | Workflow team | Resolved |
| OQ-3 | **Missing credentials surfaced as `ErrUpstream`,** conflicting with §S6.2. **Resolved (rev 1.2)** — `shared.Classify` keeps an adapter's `ErrValidation`, so a missing or invalid credential (including a non-service-account GCS key or malformed Google key) is `ErrValidation`, as §S6.2 specifies. | Workflow team | Resolved |
| OQ-4 | **Default `http.Client` had no timeout.** **Resolved (rev 1.2; since rev 1.12 per call)** — every internal call has a deadline, its alias's `timeout` or else 30 seconds (`shared.CallTimeout`); a nil `Config.HTTPClient` carries no client-wide timeout, and OAuth token requests (Gmail, Graph, GCS) use a 30-second client too. | Workflow team | Resolved |
| OQ-5 | **Version for the breaking changes since v1.0.0.** Removing `storage.New*Provider`/`sendemail.New*Provider`, `shared.DocRefStore`, and changing `storage.New`/`sendemail.New`, `ProviderClient.Fetch` and upload's `contentRef` all break callers. **Resolved (rev 1.11):** released as **v2.0.0** at module path `github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2`; `VERSIONING.md` defines MAJOR as any breaking change to an exported identifier or documented behaviour, and the release workflow refuses a tag whose major version does not match the module path. | Workflow team | Resolved |
| OQ-6 | **Standard-library and `golang.org/x/net` advisories.** `govulncheck` reported 11 reachable advisories (GO-2026-6599/6600/6603/6605/6607/6608/6610–6613/6617) in `net/http`, `html/template`, `net/textproto`, `crypto/tls` (fixed in Go 1.26.9) and `golang.org/x/net` (fixed in v0.60.0). **Resolved** — `go 1.26.9`, `x/net` v0.60.0 (with `x/crypto` v0.57.0, `x/sync` v0.23.0, `x/sys` v0.48.0, `x/text` v0.42.0). One unreachable advisory remains, GO-2026-5932 (`x/crypto/openpgp`, unmaintained, no fix): no package here imports it, and `govulncheck` passes. | Workflow team | Resolved |
| OQ-7 | **`sql-query` and `document-extract` are implemented but unwired** (not in `New`, not in `registry.All()`). §S9 wants `sql-query`'s data-access model reviewed before more is built on it, and `document-extract`'s providers ship with its `provider` field (Decision #20(3)). | Workflow team | Deferred (§S9) |
| OQ-8 | **`chat-notify` and `document-extract` real providers** (`slack`/`teams`; `aws-textract`/`azure-document-intelligence`/`gcp-document-ai`/`abbyy-vantage`). Each ships as a new adapter subpackage under its core (§3) with its registry `provider` field. | Workflow team | Open (§S9) |
| OQ-9 | **Transient vs permanent error taxonomy** mapped from each provider SDK, so `ErrUpstream` does not make every provider failure look retryable (§S9 "Hardening"). **Resolved (rev 1.10):** every connector error has a class (transient, permanent, unknown), attached by each provider integration — S3/AWS codes, HTTP status, network causes, gocloud codes (Azure, GCS), Drive reasons, email outcomes, document resolution — and `connectors.DecideRetry` retries only transient errors, per policy (§9.2, §17, `docs/runbooks/retry-semantics.md`). Unknown errors are not retried. | Workflow team | Resolved |
| OQ-10 | **Alias hot reload.** `Config.Aliases` is captured by value at `New`; a re-fetch in the worker cannot reach built connectors. **Resolved — not doing** (Decision #21). | Workflow team | Resolved |
| OQ-11 | **`chat-notify` `messageBlock`/`attachments`** are registry inputs the core does not read yet; they become meaningful with a real provider (OQ-8). | Workflow team | Open (with OQ-8) |
| OQ-12 | **Drive duplicate uploads.** Drive has no atomic create-if-absent. **Resolved (rev 1.4)** — `documents.Store` (§4.5) on the worker's PostgreSQL is the uniqueness authority; only the claimant writes; a concurrent upload gets a deterministic `InProgressError`; re-upload and retry replace in place (§8.4). | Workflow team | Resolved |
| OQ-13 | **Lease-overrun residual.** If an owner outlives its 15-minute lease *and* creates a Drive file concurrently with the new owner, two files can exist (the row still records one). Prevented while the worker's per-connector timeout stays below the lease (DOC-6); a reconciler could delete tagged files the row does not record. | Workflow team | Open (accepted, low risk) |
| OQ-15 | **Email exactly-once.** Not achievable with these providers (no idempotency key; lost responses are ambiguous). **Resolved by contract (rev 1.6)** — at-most-once without retry, effectively at-least-once with one; outcomes classified and surfaced; no automatic retries; optional duplicate-request protection (§5.4.2a). | Workflow team | Resolved |
| OQ-16 | **`chat-notify` has the same non-idempotent shape** (posting twice duplicates the message). It is already `unsafe` and has no real provider; when one ships, apply §5.4.2a's classification. | Workflow team | Open (with OQ-8) |
| OQ-17 | **Worker retry gate and outcome telemetry.** `execution_service` must gate every automatic retry (worker loop, activity retry policy, stream redelivery) on `connectors.DecideRetry`, bound transient retries (backoff, attempt limit), log every decision (`RetryDecision.LogAttrs`), count `connector_task_failures_total{type, provider, class, retried}`, forward the send-email output map with `/fail`, and emit the outcome log and metric. See [`docs/integration/connector-worker.md`](../integration/connector-worker.md) §6. | Workflow team + Execution | Open |
| OQ-14 | **`execution_service` wiring.** It must move to **platform-pgcommon v2, v2.0.1 or newer** (it is on v1.1.1 on `main`, v1.2.1 on `feat/service`), run `sqlstore.ApplySchema(ctx, runner)` in its migration step, pass its `*pgcommon.Pool` to `sqlstore.New` for `googledrive.NewProvider`, and set `WithTenant` on every call. If it uses row-level security, `connector_documents` needs a tenant policy like its other tables. Step-by-step: [`docs/integration/connector-worker.md`](../integration/connector-worker.md). | Workflow team + Execution | Open |

---

## 17. Appendix — Error Taxonomy

All connector errors are Go errors. The worker reads their **kind** with `errors.Is` against the sentinels below and their **retry class** with `connectors.ClassOf` (or decides with `connectors.DecideRetry`, §9.2). There is no `errors.Is` sentinel per class: the class column is what `ClassOf` returns.

| Sentinel | Message prefix | Raised when | Class |
|---|---|---|---|
| `ErrValidation` | `connectors: invalid input` | Missing/invalid required field; unknown `operation`/`method`/`documentLocation`; omitted or unconfigured `provider`; unresolvable document ref; `paramCount` mismatch; path template error; body encode error; request build error | Permanent |
| `ErrUpstream` | `connectors: upstream call failed` | Provider SDK/API error; network or timeout error; client construction error other than a validation error; `>= 300` status; undecodable response. The cause stays in the chain | Transient, permanent or unknown by provider mapping (§9.2) |
| `ErrMissingInternalAuth` | `connectors: missing internal auth context (WithDepartments not called)` | `rest-call`/`sql-query` without departments in context | Permanent: worker bug |
| `ErrNotDelivered` (`*sendemail.SendError`) | `connectors: not delivered: <provider> (status N): …` | send-email: the provider definitively did not accept the message | Transient (429/408/throttling code, DNS, connection failure) → retried; else permanent. Unknown when the outcome could not be recorded on the send intent |
| `ErrDeliveryUnknown` (`*sendemail.SendError`) | `connectors: delivery outcome unknown (the provider may have accepted the message): <provider> (status N): … — do not resend without verifying delivery; a resend may duplicate the email` | send-email: ambiguous outcome | Unknown: **never retried automatically**; a resend may duplicate |
| `sendintent.ErrDuplicateRequest` (`*sendintent.DuplicateRequestError`) | `connectors: duplicate send request: messageKey … already has send intent …` | A messageKey already reserved (and not last `not_delivered`), whose intent is pending or unknown (a duplicate of an accepted intent succeeds); also matches `ErrValidation`; nothing sent | Permanent |
| `ErrMissingTenant` | `connectors: missing tenant context (WithTenant not called)` | A Drive or document-ref call without `WithTenant` | Permanent: worker bug |
| `documents.ErrUploadInProgress` (`*documents.InProgressError`) | `…: documents: upload in progress: document <id> is <STATE> by another call` | Another call owns the Drive document; also matches `ErrUpstream`; `errors.As` gives `DocumentID` and `State` | Transient |
| `documents.ErrOwnershipLost` | `documents: ownership lost` | The caller's lease was taken over; wrapped in `ErrUpstream` by the adapter | Transient |
| `aliasconfig.ErrUnknownAlias` | `aliasconfig: unknown alias` | Wrapped together with `ErrValidation` when `endpointAlias`/`queryAlias` is not in `Config.Aliases` | Permanent |
| `shared.ErrTooLarge` | `connectors: payload too large` | A size cap exceeded (object, inline content, response, attachments, upload); always together with `ErrValidation` | Permanent |
| `docref.ErrNotFound`, `ErrSourceMissing`, `ErrIntegrityViolation`, `ErrTooLarge`, `ErrInvalidTenant` | `docref: document reference not found` / `document source object missing` / `document integrity violation` / `document too large` / `invalid tenant id` | A document ref that cannot resolve as it stands (§4.1); wrapped with `ErrValidation` by `storage` and `send-email` (`not_delivered`) | Permanent |
| `docref.ErrUnavailable` (incl. `valkeystore.ErrNotPersisted`) | `docref: store temporarily unavailable` | Valkey full, a write not confirmed in the AOF, a node refusing during failover or resharding, a cluster redirect; `storage`: `ErrUpstream`; `send-email`: `ErrNotDelivered` | Transient |

`connectors.New` returns a plain error (`connectors: InternalToken is required …`) for an empty token; `aliasconfig.Load` returns `aliasconfig: reading|parsing <path>: …` or a `Validate` error.

---

## 18. Integration Details

### 18.1 Integration with `definition_service`

| Direction | Mechanism | Description |
|---|---|---|
| definition_service → this | Go import `pkg/registry` | `registry.All()` drives the `UNKNOWN_CONNECTOR_TYPE` compile check and the authoring-template generator; `Field.IsSecretRef()` marks credential fields an author may not map (`CONNECTOR_SECRET_AUTHORED`) |
| definition_service → (HTTP) → worker → this | `GET /internal/connector-aliases` → `aliasconfig.Config` | The alias registry this module resolves against (Decision #21) |

### 18.2 Integration with `execution_service` `cmd/connector-worker`

| Direction | Mechanism | Description |
|---|---|---|
| worker → this | Go import `pkg/connectors` + adapters | `New(Config)` once at startup; `Execute` per task inside its bounded pool, per-type timeout and lease |
| worker → this | `connectors.WithDepartments` | Forwarded `x-departments` for `rest-call`/`sql-query` |
| this → worker | `Execute` return | Output map forwarded verbatim to `/internal/connector-tasks/:id/complete`; error classified per §17 for retry or `/fail` |

Consumer obligations are listed in `ARCHITECTURE.md` (Consumer conformance checklist); the step-by-step wiring guide is [`docs/integration/connector-worker.md`](../integration/connector-worker.md).

### 18.2a Integration with Valkey

Document-ref metadata (§4.1): the worker passes its go-redis client (`redis.UniversalClient` — standalone, Sentinel or Cluster; each ref is one key, so Cluster slots never matter) to `valkeystore.New` and calls `valkeystore.CheckDurability` at startup. Required whenever `createDocument`, ref content or email attachments are used. Commands used: `EVALSHA`/`EVAL`, and inside the scripts `EXISTS`, `HMGET`, `HSET`, `TIME`, `PEXPIREAT`, `HGET`, `DEL`; `HGETALL`, `WAITAOF`, `CONFIG GET` (startup), `PING` — the exact ACL line is in docs/runbooks/document-refs.md and is tested. `READONLY`, `MASTERDOWN`, `NOREPLICAS`, `OOM`, `LOADING` and cluster redirects are `docref.ErrUnavailable` (transient). CI tests the store against Valkey 8.

### 18.2b Integration with the S3 document bucket

Document-ref content (§4.1): the worker passes its own `*s3.Client` (IAM role; never tenant credentials) and the bucket name to `s3content.New`, and calls `Store.Check` (HeadBucket) at startup. Operations used: `PutObject`, `GetObject`, `DeleteObject`, `HeadBucket`. Required IAM: those object actions on `<bucket>/<prefix>*` and `s3:ListBucket` (so a missing object is a 404). CI tests the store against floci 2.1.0 (the platform's AWS emulator).

### 18.3 Integration with the worker's PostgreSQL

**All database access goes through platform-pgcommon v2 — no exceptions, tests included.** Configuration: the worker builds the pool with `pgcommon.ConfigFromEnv` (PG_* / DATABASE_URL, `PG_BOUNCER_MODE`) and `pgcommon.NewPool`, and migrates with `migrate.Runner{DSN: pgcommon.MigrationDSNFromEnv()}` (direct to PostgreSQL, never PgBouncer). Operations: the stores take the `*pgcommon.Pool` and run every statement, single statements and audited transitions alike, in `pgcommon.RunInTx`, so the pool's transaction timeouts apply, using pgcommon's own types (`pgcommon.Tx`, `Row`, `TxOptions`, `ErrNoRows`) — never `pgx`, `pgxpool` or `database/sql` directly — so the pool's GUC injection, PgBouncer mode, timeouts, metrics and tracing apply to every query. Enforced twice: `.go-arch-lint.yml` grants only `platform-pgcommon/v2/**` (pgx is not a permitted vendor), and golangci's `depguard` rule `pgcommon-only` rejects `github.com/jackc/pgx`, `database/sql` and `github.com/lib/pq` everywhere, test files included.

The Drive document registry (§4.5): built on **platform-pgcommon v2**, like org-membership's and definition_service's repositories. The worker runs `sqlstore.ApplySchema(ctx, runner)` in its migration step (versions tracked in `connector_documents_migrations`, the `outbox.ApplySchema` pattern) and passes its `*pgcommon.Pool` to `sqlstore.New`, which `googledrive.NewProvider` takes. Every statement runs in `pgcommon.RunInTx`, so the pool's GUC injection, PgBouncer mode, statement and lock timeouts, metrics and tracing apply. Required whenever `google-drive` is wired. CI tests the store against PostgreSQL 17.

### 18.3a Integration with OpenBao

None direct. The worker reads `connectors/<tenant>/<type>/<field>` and passes values in `input`.

### 18.4 Integration with internal platform services

`rest-call` (any internal API registered as an alias) and `sql-query` (an owning service's query-execution endpoint). Both receive `x-internal-token` and `x-departments` on every call.

### 18.5 Integration with provider APIs

| Provider | Adapter | API |
|---|---|---|
| AWS S3 | `storage/gocloud` | S3 via `gocloud.dev/blob/s3blob` |
| Azure Blob | `storage/gocloud` | Blob via `gocloud.dev/blob/azureblob` |
| Google Cloud Storage | `storage/gocloud` | GCS JSON API via `gocloud.dev/blob/gcsblob` |
| Google Drive | `storage/googledrive` | Drive v3 `files` (list, get media, create, update, delete) |
| Amazon SES | `sendemail/ses` | SES v2 `SendEmail` |
| SendGrid | `sendemail/sendgrid` | v3 Mail Send |
| Microsoft 365 | `sendemail/msgraph` | Graph v1.0 `POST /users/{sender}/sendMail` |
| Google Workspace | `sendemail/gmail` | Gmail v1 `users.messages.send` (raw RFC 2822) |

### 18.6 Dependency table

| Contract | Caller → Callee | Posture on failure | Ref |
|---|---|---|---|
| `pkg/registry` import | definition_service → this | Compile-time; no runtime failure | §18.1 |
| `Execute` | worker → this | Returns classified error; worker retries per policy, then fails the task | §9, §17 |
| Document-ref metadata | this (`docref/valkeystore`) → Valkey | storage: `ErrUpstream` (retryable); send-email: `ErrNotDelivered`; startup refuses a non-durable server | §4.1, §18.2a |
| Document-ref content | this (`docref/s3content`) → S3 | Missing object: `ErrSourceMissing`; altered: `ErrIntegrityViolation` (both `ErrValidation`); outage: storage `ErrUpstream`, send-email `ErrNotDelivered` | §4.1, §18.2b |
| Provider API call | this (adapter) → provider | `ErrUpstream`; no partial state in the module | §9.3 |
| Internal service call | this (`rest-call`/`sql-query`) → service | `ErrUpstream` on status `>= 300` or a network error; redirects never followed | §8.2 |

---

## 19. Migration Strategy

### 19.1 Provider-adapter import paths (v2.0.0)

Every import path gains `/v2`: `github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/...`.

| Before (v1) | After (v2) |
|---|---|
| `storage.NewGocloudStorageProvider` | `gocloud.NewProvider` — `pkg/connectors/storage/gocloud` |
| `storage.NewDriveStorageProvider` | `googledrive.NewProvider` — `pkg/connectors/storage/googledrive` |
| `sendemail.NewSESProvider` | `ses.NewProvider` — `pkg/connectors/sendemail/ses` |
| `sendemail.NewSendGridProvider` | `sendgrid.NewProvider` — `pkg/connectors/sendemail/sendgrid` |
| `sendemail.NewGraphSendMailProvider` | `msgraph.NewProvider` — `pkg/connectors/sendemail/msgraph` |
| `sendemail.NewGmailProvider` | `gmail.NewProvider` — `pkg/connectors/sendemail/gmail` |

The other breaking changes since v1.0.0 are listed in `CHANGELOG.md` and §16 OQ-5: among them `storage.New`/`sendemail.New` take a `*docref.Service`, `ProviderClient.Fetch` takes `maxBytes`, `shared.DocRefStore` is gone (document refs need `Config.DocRefs`), upload returns `contentRef` only with `createDocument` or ref content, Drive and document refs need `WithTenant`, and `send-email` returns `deliveryOutcome` with every result. Compatibility shims at the old adapter names are impossible: the core would have to import its adapters, which is an import cycle and exactly what §3.2 forbids. The worker updates its provider maps in the same change that bumps the version.

### 19.2 Upgrading consumers

1. Require `github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2` in the worker's `go.mod` (and vendored copy, if any) and rewrite its imports to the `/v2` path; move the worker to platform-pgcommon v2, v2.0.1 or newer (§16 OQ-14).
2. Replace the six constructor references (§19.1); `google-drive` becomes `googledrive.NewProvider(sqlstore.New(pool))` (`documents/sqlstore`).
3. Run both `ApplySchema(ctx, runner)` functions (`documents/sqlstore`, `sendintent/sqlstore`) in the worker's migration step, set `Config.DocRefs` (and `Config.SendIntents` if `messageKey` is used), and set `connectors.WithTenant` on every call.
4. Gate automatic retries on `connectors.DecideRetry` (§9.2). Full guide: [`docs/integration/connector-worker.md`](../integration/connector-worker.md).
5. Run the worker's tests; `definition_service` needs no change (it imports `pkg/registry` only).

### 19.3 Rollback

Pin the previous tag. The registry tables can stay: the previous version ignores them. Re-applying the newer version later reuses the rows.

---

## 20. Operational Considerations

The module has no runtime of its own; these are the points a worker operator should watch.

### 20.1 Provider failures by class

| Signal (worker-side, from §17 classes) | Likely cause | Action |
|---|---|---|
| `ErrValidation` rising for one type | Authoring error, or a provider advertised but not registered (OPS-2) | Fix the plan, or register the provider |
| `ErrValidation` "build <provider> client" | Missing or invalid tenant credential | Ask the tenant admin to (re)submit the credential |
| `ErrValidation` "chat-notify has no provider configured" | `Config.ChatNotifyClient` not set | Wire a real provider; until then chat tasks fail rather than report a false send |
| `ErrUpstream` from one provider only | Provider outage, throttling or revoked access | Provider status; tenant credential validity |
| `ErrMissingInternalAuth` | Worker not setting departments | Worker bug — fix wiring |
| `aliasconfig: unknown alias` | Alias deleted, or worker started before it was added (no hot reload) | Restart the worker after alias changes |

### 20.0 send-email outcomes

Runbook: `docs/runbooks/email-delivery.md`. Watch `connector_send_email_outcomes_total{outcome="unknown"}`; for each unknown outcome verify delivery at the provider before any resend. `connector_send_intents` shows `status`, `attempts` and `detail` per `messageKey`; a row left `pending` means the worker stopped mid-send, or the outcome could not be recorded — treat it as `unknown`; once unchanged for `sendintent.StalePendingAfter` (15 min) it can be resent with `resend` and `resendAttempt`.

### 20.1a Drive document registry

- Rows stuck in `PENDING_UPLOAD`/`UPLOADING`/`DELETING` past `lease_expires_at` mean a worker died mid-call; the next upload takes them over, so no action is needed unless one recurs.
- `failed_attempts` and `connector_document_attempts` show retry history per document; a document that keeps failing points at a credential or Drive permission problem. Schedule `sqlstore.Store.PruneAttempts` so the audit table does not grow without bound (§4.5).
- `ErrUploadInProgress` rising for one document means concurrent tasks upload the same file — expected to converge; a sustained rate means a workflow uploads the same name in parallel branches.

### 20.2 Memory

Workers hold no refs in memory; restarting a worker loses nothing. Valkey holds a few hundred bytes per ref — size `maxmemory` by ref count (10,000 refs ≈ 5 MB), alert at 75 %, and alert on any `evicted_keys` (must stay 0 under `noeviction`). Document bytes are in S3, not in worker or Valkey memory; `Open` streams them, and `send-email`/`storage` upload hold at most 25/50 MiB per call. A Sentinel or managed failover can lose metadata acknowledged only by the old primary's AOF; the consuming task then fails with `ErrValidation` (`ErrNotFound`) and the producing step is re-run. See `docs/runbooks/document-refs.md`.

### 20.3 Dependency vulnerabilities

`make vuln-check` (pinned `govulncheck`) and the Trivy step run on every PR and release. A new advisory in a provider SDK is fixed here and released; the worker then bumps its pin.

---

## 21. Performance Considerations

### 21.1 Client construction

Building a provider client is the expensive step (OAuth token source, Google service creation, a new transport). The per-credential cache (§6) amortises it across calls with the same tenant credentials; the 256-entry cap bounds memory, evicting the least recently used client, so a worker with more credential sets than that keeps its hot clients.

### 21.2 Whole-object buffering

`storage` reads a fetched object fully into memory and uploads from a `[]byte`; `send-email` holds every attachment in memory and base64-encodes it. Only `docref.Service.Open` streams (the connectors use `Read`, which buffers after verifying), so every payload has a cap: objects 50 MiB, inline fetch 1 MiB, internal responses 10 MiB, attachments 25 MiB in total. Large objects should use `createDocument: true` only when another connector needs the bytes.

### 21.3 Lock contention

The cache and store mutexes guard map operations only (CACHE-3), never I/O, so contention is negligible next to provider latency.

### 21.4 Internal calls

`rest-call`/`sql-query` reuse the caller's `http.Client` and its connection pool; pass a shared, tuned client in `Config.HTTPClient` rather than relying on the default.

---

## Appendix — Traceability to Part II decisions

| Decision (§S10) | What it fixed | Where implemented |
|---|---|---|
| #4, #12, #21 | Alias-based, internal-only `rest-call`/`sql-query`; registry owned by `definition_service`; no hot reload | `aliasconfig`, `restcall`, `sqlquery`; §4.3, §8.2 |
| #6 | Per-type retry policy | `registry` `Retry`; §9.2 |
| #13 | Top-level output keys only | All `Execute` returns; §5.1 |
| #14 | Opaque document refs | `docref.Service` + `docref/s3content` (S3 content) + `docref/valkeystore` (Valkey metadata); §4.1 |
| #17 | `sql-query` as an HTTP proxy, never a DB connection | `sqlquery`; §5.4.4 |
| #18, #19 | Multi-provider `storage`; globally unique credential field names | `storage` core + `gocloud`/`googledrive`; §5.4.1 |
| #20 | No mock fallback, no default provider, `provider` field only with real code, Shared Drive support | `storage`/`sendemail` `clientFor`; registry; `googledrive`; §9.3 |
| #22 | Real `send-email` providers; Graph without the SDK; shared doc-ref store; synthesized attachment names | `sendemail` core + four adapters; §5.4.2 |
| #23 | `senderEmail` in the cache key; CR/LF stripping in raw MIME | `emailCacheKey`; `gmail/mime.go`; §6.1, §10.3 |

---

# Part II — Connector System Design

> Moved here from `docs/lld/workflow_connectors.md` (rev 8.14) with its full text. Section numbers carry an **S** prefix; references into other repos' documents (`execution_service.md`, `definition_service.md`, `workflow_models_lib.md`) are unchanged. Sibling services' LLDs cite this design as `workflow_connectors.md` in the design repo.

## S1. Overview & Scope

This document specifies a small, fixed, extensible catalogue of automatic workflow tasks; steps that fetch, check, or send something without a human acting on them; and how they're declared, compiled, run, and executed across `workflow-models`, `definition_service`, `execution_service`, and a new shared connector library and worker runtime (`workflow-connectors`, §S6) that runs inside `execution_service` itself.

**Explicitly not in scope.** This is not a mechanism for arbitrary or dynamically-targeted business logic. Every connector type is a small, fixed, developer-built piece of code, known ahead of time and registered explicitly; never something a workflow author points at an arbitrary URL or database at authoring time. Real business logic; what a domain service does with its own data; continues to live entirely in that domain service, unchanged from today's model (`execution_service.md` §1.3/§8.1). This document only adds a way for a workflow to react automatically to a task instead of waiting on a person, using the exact same completion path a person already uses.

**Build order.** The pieces here have a real dependency order, not an arbitrary one:

1. `workflow-models`; the connector-task DSL shape, defined once, since both compiling and running services already depend on this module for every other DSL shape.
2. `definition_service`; stores and serves the authoring-side templates a workflow author sees (including every field a connector needs, credentials included — §S6.2), kept in sync with `workflow-models`, and compiles against what `workflow-models` already defines.
3. `execution_service`; runs against what `workflow-models`/`definition_service` already define.
4. The shared connector library and worker runtime (`cmd/connector-worker`, inside `execution_service`'s own repo); the actual execution, decided last since it depends on nothing upstream of it changing.

**Field ownership across the boundary.** `workflow_models_lib.md` §2.3/§4.1 owns the concrete `StageDef.ConnectorType`/`StageDef.IOMapping` fields §S3 below defines the shape of. `definition_service.md` §4.1.2/§4.1.3.3 and `execution_service.md` §2.4/§3.1/§4.3/§6.4 carry the small footprint changes §S4/§S5 below describe — neither service needed a structural change beyond that footprint; both were written placement-agnostic from the start, which is exactly what made resolving §S6.1's placement question a documentation-only change with no ripple into either.

## S2. Architecture

```text
 author (definition_service templates, sourced from workflow-models —
 every field the connector needs, including credentials, §S6.2)
        |
        v
 definition_service compiles (§S4)
        |
        v
 execution_service creates the task exactly as it does today (§S5)
        |
        v
 WorkflowTaskCreated fires (connector_type set; already exists for every task)
        |
        v
 cmd/server's existing /internal/events
 handler additionally pushes onto a
 Valkey Stream for connector-typed
 tasks (§S6.5 step 0) — the only inbound
 listener stays cmd/server's; cmd/
 connector-worker has none (§S6.1)
        |
        v
 cmd/connector-worker consumes via
 XREADGROUP/XACK, a pure Stream
 consumer (§S6.5)
        |
        v
 dispatch to the registered Connector (§S6.3),
 per-connector-type bounded worker pool
        |
        v
 Connector.Execute(ctx, input) — input already carries
 everything needed: author-supplied config, with any
 provider credential resolved from its OpenBao secret
 path (§S6.2) for storage/send-email/document-extract/
 chat-notify, or a forwarded internal-auth context for
 rest-call/sql-query
        |
        v
   success                        failure / unregistered type
        |                                    |
        v                                    v
 execution_service's new           the new fail-signal endpoint (§S6.5) —
 completion endpoint (§S5.5) —      routes to the same FAILED/DEGRADED
 a sibling of the human path,      outcome a non-retryable Activity
 not a re-route through it         error already produces
        |                                    |
        v                                    v
 workflow continues, exactly       workflow fails, exactly as any
 as if a human had completed it    other non-retryable failure would
```

**Connector tasks are automation-only — there is no human fallback path.** If a connector type isn't registered, or a connector call ultimately fails (retries and the internal execution timeout both exhausted, §S6.5), the task does not sit open waiting for a person. It fails the workflow through the interpreter's existing `FAILED`/`DEGRADED` machinery — a main/Sequential-path task produces `FAILED`, a task inside a `Parallel` branch contributes to `DEGRADED` — via a new signal `cmd/connector-worker` calls, the same outcome a non-retryable Activity error already produces today (§S10 Decision #9).

## S3. DSL Shape (`workflow-models`)

A connector-typed stage is `enums.StageTypeConnector`, a new value alongside `prep`/`review`/`approve`/`send_task`/`receive_task` (`pkg/enums.StageType`), carrying:

- `StageDef.ConnectorType` — the connector type name (a plain string, not a fixed enum member; see §S4.2 on why), parsed once at compile time from the BPMN `connector:` prefix.
- `StageDef.IOMapping` — a structured input/output mapping, reusing the exact same `IOMapping`/`IOVar` shape a callActivity's `ExecutionStep.IOMapping` uses; not reinvented.

These two fields (`workflow_models_lib.md` §2.3/§4.1) are the single place either service looks to know what a valid connector-task node looks like. Neither service derives it independently.

## S4. Compiler Behavior (`definition_service`)

### S4.1 The element and its scoped exception

A `<bpmn:serviceTask>` carrying `<zeebe:taskDefinition type="connector:<name>"/>` compiles like a task-producing node. This is a narrow, explicit exception to `definition_service.md`'s Tier-3 permanent rejection of `bpmn:serviceTask` (§S4.1.2): only a `serviceTask` whose `type` carries the `connector:` prefix is accepted. A `serviceTask` with any other `type`; or none; is still rejected exactly as today; arbitrary or custom service-task scripting remains permanently out of scope. This mirrors how `userTask`'s own type handling already has two branches (a registered type compiles fully; an unrecognized one still compiles, as a passthrough); `serviceTask` gets an analogous, but stricter, two-branch treatment: `connector:`-prefixed compiles, anything else is rejected, never silently passed through, since an arbitrary service task is exactly what Tier 3 exists to keep out.

Its input mapping reuses `ZeebeIOMapping`; the same type `callActivity` already populates from `<zeebe:ioMapping>`; so no new XML-parsing plumbing is needed, only a new element handler that reads the already-parsed structure into the DSL shape from §S3.

### S4.2 Validation: lenient by design, matching an existing convention

The known connector-type list comes from a compile-time Go dependency — `workflow-connectors`' lightweight `pkg/registry` sub-package (§S6.3), not the heavier `pkg/connectors` sub-package holding the actual runtime implementations — the same shape of dependency `workflow-models` already is for both services; no new database table, no new runtime call to keep something in sync.

An unrecognized connector type at publish time warns; it does not fail the publish. This matches the existing, already-shipped behavior for an unrecognized `StageDef.Type` (`definition_service.md` §4.1.3: "compiles as a passthrough stage... it is not rejected"). Treating a connector type the same way is a deliberate consistency choice, not an oversight; the alternative (hard-failing an unknown connector type) would make this the one place in the compiler where an unrecognized identifier behaves differently from everywhere else, for no benefit: the connector type named here might still be registered before the workflow is ever instantiated, so failing the publish over it would only block authors prematurely. Whether it's actually registered is a question runtime settles deliberately and visibly (§S2, §S6.5) — as a real workflow failure now, not a silent degrade — never something the compiler needs to guess at in advance. The same lenient treatment applies to an `endpointAlias`/`queryAlias` (§S6.4.4/§S6.4.5) that doesn't resolve at runtime — the compiler doesn't validate aliases at all, since only `cmd/connector-worker`'s own internal-API registry can.

### S4.3 Authoring experience (`definition_service`)

The Modeler-facing configuration form for a given connector type (what fields it asks for) is **not** resolved dynamically off BPMN/Zeebe XML namespaces. `definition_service` stores these forms and serves them to whoever authors a workflow, generated from `pkg/registry`'s own connector definitions (§S6.3); one generated source of truth, not a hand-maintained list that can drift from what's actually implemented. This form is also where every field a connector needs — including, for `storage`/`send-email`/`document-extract`/`chat-notify`, the provider credential itself — gets collected (§S6.2). This is a natural extension of `definition_service`'s own design-time authoring/compilation charter (`execution_service.md` §1.3), not a separate UI-facing service's job — the connector-type catalogue is fixed, code-generated metadata, not tenant-authored stored content (contrast with BE-for-UI's "custom BPMN module"/"reusable authoring component" library, which *is* new stored data and stays out of this service's schema, `execution_service.md` §1.3/Appendix A.2 #31).

## S5. Runtime Behavior (`execution_service`)

### S5.1 Task creation; no new mechanism

`CreateTaskActivity`'s dispatch table (`execution_service.md` §3.1) extends to cover connector-typed nodes, alongside `prep`/`review`/`approve`, unrecognized-`Type` passthrough, and the `call_pool` admin-stub task. No new Activity, no new retry-policy class; the same DB-write activity class, the same signal-wait mechanics as every other task type. This is the load-bearing design choice in this whole document: a connector-typed task is created exactly like a human task, and completed through exactly the same path, so the Temporal interpreter itself needs no new dispatch branch, no new Activity, and no new retry policy. Every connector-typed task dispatches automatically the instant it's created — no decision, no preference lookup, nothing gates it.

### S5.2 Data model

`workflow_task` (`execution_service.md` §4.3) gains a nullable `connector_type` column; not in `extras_json`; because `cmd/connector-worker` needs to filter and query tasks by connector type efficiently, the same reason `department_id` is already a real column rather than JSON-only.

### S5.3 No decision mechanism in v1

Every connector-typed task in the v1 catalogue is unconditionally automatic. No v1 connector type needs an auto/manual decision mechanism (a per-user default plus a task-level override) — that story only ever applies to a connector replacing a pre-existing human task, where a tenant's own staff might want the option to keep doing the check themselves. `llm-verify` is the one connector design that had that story, and it is not part of the v1 catalogue (§S6.4.7, §S10 Decision #3); with no v1 connector needing it, the decision mechanism has no consumer.

This is intentionally not rebuilt as unused scaffolding. If a future connector type ever has the same story, the mechanism (a `SupportsManualOverride`-shaped registry flag, gating a stored default plus a task-level override) is straightforward to reintroduce — but nothing in v1 needs it, so nothing speculative is carried forward.

"Automatic" means no human involvement at any point in a connector task's life, including on failure — there is no manual-override decision and no manual fallback (§S2, §S6.5 Decision #9). A future connector type that genuinely needs a human in the loop on failure would need its own design, not an implicit extension of this one.

### S5.4 Events

`WorkflowTaskCreated`'s payload (`execution_service.md` §6.4, `exec_eventschema/workflow_task_created.json`) gains an optional `connector_type` field. `cmd/connector-worker` (§S6) is a new named consumer of this event specifically for connector-typed tasks, alongside the event's existing consumers (Notification, the LLM cache pre-warm, Dashboard).

### S5.5 Completion; a sibling endpoint, not the human path (see §S10 Decision #16)

`cmd/connector-worker` (§S6.5) calls a **new** `execution_service` endpoint once it has a result — `POST /internal/connector-tasks/:id/complete` (success) or `/:id/fail` (failure, §S6.5 step 3) — not the human `/tasks/:id/complete` path. `execution_service`'s `TaskService.checkHumanActionable` rejects every human-facing task action (`Claim`/`Complete`/`Defer`/`Reassign`) on a connector-typed task, since one has no human assignee to act on. `execution_service` *does* distinguish who or what is calling it — deliberately, as a safety property.

The new endpoint is a **sibling** of the human path, not a re-route through it: both ultimately call the same `TemporalClient.SignalWorkflow` (`stage-transition`/`stage-fail`) that resolves against the interpreter's own pending-signal machinery (§S2), so §S6.1's "never touches the Temporal SDK directly" constraint on `cmd/connector-worker` still holds — the new endpoint is what touches the SDK, on `execution_service`'s own side, exactly as `TaskService`'s existing methods already do. A connector task has exactly one legitimate resolver (the worker that dispatched it), never a concurrent human racing it, so the endpoint reads the task's own current `record_version` itself rather than trusting a client-supplied one. Idempotent under `cmd/connector-worker`'s own Stream-redelivery-driven retries via a terminal-status check (the long tail) plus a short-TTL dedup key (the narrow race between "signal delivered" and "the resulting DB write commits," which the interpreter's pending-map resolution does not absorb on its own).

## S6. The Shared Connector Library & Worker Runtime

### S6.1 Worker Placement — decided

**Decided: Option B.** A new `cmd/connector-worker` binary lives inside `execution_service`'s own repo, sharing its CI/deploy pipeline with the existing `cmd/server`/`cmd/worker` — a distinct process, not folded into either. The workflow team owns and runs the connector runtime directly, as trusted platform infrastructure, rather than delegating it to a domain service or a separate standalone repo.

**Still true regardless:** a connector worker is not a Temporal Worker — `cmd/connector-worker` never registers against `execution_service`'s Temporal task queues or touches the Temporal SDK (`execution_service.md` §3.7/§4.6 are unrelated). Different node types needing different handling never meant different Temporal Workers per BPMN node type; it means one dispatch-table entry per connector type, inside this one process. `cmd/connector-worker` has **no inbound HTTP surface at all** — it consumes `WorkflowTaskCreated` from a Valkey Stream that `cmd/server`'s existing `/internal/events` handler pushes connector-typed events onto (§S6.5 step 0), not by running a listener of its own; it still dispatches to a per-connector-type bounded worker pool (§S6.5), and still completes (or now fails, §S10 Decision #9) via `execution_service`'s existing signal paths (§S5.5) — the event-driven dispatch mechanism (§S10 Decisions #1/#2) is independent of where the process runs or how the event physically reaches it.

**Why Temporal workflow replay cannot itself cause a duplicate connector execution.** This design was compared directly against making the connector a real Temporal Activity (which would give native replay-safety and retry/backoff) and the comparison came out in favor of keeping the design as-is (§S10 Decision #1 rationale) — because the replay concern the alternative would solve isn't actually a risk here. Temporal's replay guarantee covers only the workflow *function's* own execution: every `ExecuteActivity`/signal-wait/timer call is recorded in history, and replay reconstructs the workflow's in-memory state from that history without re-running anything already recorded as complete. `cmd/connector-worker`'s code runs entirely *outside* that boundary — it's a separate process reacting to an event, exactly like a human reacting to a task appearing in their inbox — so replay has no path to re-invoke it; replaying the workflow just means the workflow function's deterministic code reconstructs "I am waiting on a `stage-transition` signal for node X," it does not re-run or re-trigger anything external. The real, different risk — the same event being delivered to `cmd/connector-worker` twice, whether over SNS/SQS's at-least-once guarantee upstream or a redelivered, unacked Valkey Stream entry downstream — is closed by §S6.5 step 0's dedup mechanism, not by anything Temporal-specific.

`workflow-connectors` (the Go module) still holds `pkg/registry` (imported by `definition_service` alone — both its compile-time check and its authoring-template generator, §S4.3) and `pkg/connectors` (imported by `execution_service`'s `cmd/connector-worker` as an ordinary module dependency, same as `platform-events`) — no repo/module restructuring beyond this.

### S6.2 Configuration & Credentials — author-supplied, resolved by reference

**Ordinary fields come from the diagram; a provider credential never does.** An ordinary field flows through the compiled task's `IOMapping`/`context_json` exactly like any other workflow variable — including `provider` (§S6.4), an ordinary field like any other, never itself a secret. A provider credential — for `storage`/`send-email`/`document-extract`/`chat-notify` — is stored once per tenant, connector type and field: an administrator submits it through `definition_service`'s credential form, which writes the raw value to **OpenBao** (the same mechanism `execution_service.md` §9.4 already uses for the platform's own infrastructure secrets, extended here to a per-tenant secret created by an administrator rather than a static, ops-provisioned one). The diagram never mentions it: a connector task always uses the tenant's stored credential, and an input whose target is a secret field is refused at publish (`CONNECTOR_SECRET_AUTHORED`, definition LLD §3.1.4). There is no runtime callback to a domain service and no platform-ambient credential lookup — the worker runs as trusted platform infrastructure (§S6.1).

**`cmd/connector-worker` reads the credential from OpenBao's KV-v2 read API, called only just before `Connector.Execute()`, in memory.** The raw credential is never written to `context_json`, never recorded in Temporal's workflow history, and never carried in the `WorkflowTaskCreated` event payload or on the Valkey Stream `cmd/server` pushes connector-typed events onto (§S6.5): nothing in a task names a credential, not even a path, because the worker derives it.

**The worker reads only the job's own credential path, and chooses it itself.** `WriteCredential` stores a credential for tenant T, connector type C and field F at exactly `connectors/<T>/<C>/<F>`, and nowhere else. For each secret field the registry declares for C, `cmd/connector-worker` reads that path for the job's own tenant and sets the field to the value it finds. A value an older plan still carries for a secret field is discarded unread. A field the tenant has stored nothing for is left out: the registry does not say which of a type's secret fields a provider needs, so the connector refuses an absent credential itself, with its own validation error naming the provider (§S6.4). Any other read failure, such as an unauthorised or unavailable store, fails the task with its class (`secret_unauthorized`, `upstream_error`) before the connector runs. The worker's OpenBao token can read every tenant's subtree, so choosing the path from the job's own tenant is the tenant boundary at this hop.

**Rotation and revocation.** Rotating a credential is re-submitting the authoring form — `WriteCredential` overwrites the same OpenBao path, and KV-v2's own versioning keeps the history. Revoking one is a separate, explicit action: an admin-gated `DELETE /api/v1/connectors/credentials/:connector_type/:field_name` destroys every version at that path outright (OpenBao KV-v2's metadata-destroy endpoint, not the data-delete soft-delete, which stays recoverable) — a genuine revoke, for a compromised credential or a decommissioned connector task. TTL/expiry enforcement and notifying an in-flight workflow instance still holding a resolved value are both out of scope.

**"Document ref"** — used throughout §S6.4's field tables (`storage.content`/`contentRef`, `document-extract.documentRef`, `send-email.attachments`) — is a plain opaque string, issued by the `storage` connector's own `fetch`/`upload` operations and consumed as-is by any other connector's document-ref-typed field. No separate Document Service exists or is proposed here; this is just enough shape to make those fields concrete. A connector receiving a document ref it didn't issue itself treats it as an opaque token to pass through, not something to parse.

**`rest-call`/`sql-query` are the exception — internal platform APIs only, never external ones.** Their `endpointAlias`/`queryAlias` (§S6.4.4/§S6.4.5) resolves, via a small config/registry `cmd/connector-worker` owns itself, to another microservice within this platform (e.g. Tender's own API, PMS's own API) — never an arbitrary external domain. Every call `cmd/connector-worker` makes through this path carries **two required pieces, both mandatory, neither optional**:

1. The platform's existing internal-service-to-service token (`execution_service.md` §5.7/§9.2's `x-internal-token`/`INTERNAL_API_TOKEN` — the same mechanism `execution_service`'s own `/internal/*` routes already require), proving the call originates from trusted platform infrastructure.
2. The acting user/tenant's forwarded `x-departments` header (`dept_uuid:role` pairs — see the cross-repo header-format note in `execution_service.md`/`definition_service.md`), so the target internal service can apply its own row-level/role-based authorization if it needs to.

No per-tenant *secret* is involved for these two connector types — but the auth itself is real, mandatory, and explicitly documented as such, never waved away as unnecessary.

**Net effect on domain services.** Tender/PMS supply nothing but ordinary workflow variables through the existing `IOMapping` mechanism now — the same way every other task type already receives its data. There is no callback contract, no credential ownership, no template ownership left on the domain-service side at all.

### S6.3 The Connector Interface

```text
Connector interface:
  Type() string                                        // the registered name, e.g. "storage"
  Execute(ctx, input) (output map[string]any, error)   // input carries everything: author-supplied
                                                        // config, with any credential already resolved
                                                        // from its OpenBao secret path (§S6.2), or
                                                        // (rest-call/sql-query) ctx carries the forwarded
                                                        // internal-auth context. output's top-level keys
                                                        // are what IOMapping.Outputs.Source can address —
                                                        // no nested/dot-path access in v1 (§S6.5)
```

`workflow-connectors` — a new, `platform-libs`-style Go module (alongside `platform-events`, `platform-gincommon`, `platform-pgcommon`), owned by the workflow team — splits into two packages so that a compile-time consumer that only needs the type list never pays for the runtime's dependencies:

- **`pkg/registry`** — lightweight: type names, display metadata, JSON-schema-shaped input/output descriptions. Zero heavy SDK dependencies. Imported by `definition_service`, for both §S4.2's `UNKNOWN_CONNECTOR_TYPE` compile-time check and its authoring-template generator (§S4.3) — the same service, one import.
- **`pkg/connectors`** — heavy: the actual `Execute()` implementations, pulling in the AWS SDK, the SendGrid client, DB drivers, etc. Imported only by `execution_service`'s `cmd/connector-worker` (§S6.1).

A small generator tool introspects `pkg/registry`'s shapes to emit the element-template JSON `definition_service` serves (§S4.3) — one generated source of truth, not a hand-maintained list that can drift from what `pkg/connectors` actually implements.

### S6.4 The Catalogue

Six v1 connector types, retry guidance stated per type below as final, ratified policy — not a recommended default. For each: the BPMN-author-supplied `IOMapping` inputs flow through the workflow's own `context_json`, unchanged mechanism; where a field is a provider credential, what actually travels is a secrets-store reference, not the raw value (§S6.2). Output is written back via `execution_service`'s connector-task completion endpoint (§S5.5, §S6.5 step 3). Every design below is original, informed only by the general shape and full property set of Camunda's equivalent out-of-the-box connector (never its code, type identifiers, or template content — §S7).

**Four of the six are designed as multi-provider (§S10 Decision #18): `storage`, `send-email`, `document-extract`, `chat-notify`.** `rest-call`/`sql-query` have no `provider` field — they only ever target this platform's own internal services (§S6.4.4/§S6.4.5), where "provider" has no meaning. **A `provider` field ships only once real per-provider code exists for that type (§S10 Decision #20)** — `storage` and `send-email` have it today, each with four real providers; `document-extract`/`chat-notify` have no real provider yet (every call fails with a validation error rather than using a mock, Decision #20(1)), so their tables below describe the eventual design, not the current schema. `provider` is always required, never defaulted (§S10 Decision #20) — a task must name one explicitly. `cmd/connector-worker` holds one client per `(connector type, provider)` pair rather than one per type; `Execute()` (§S6.3) looks up the pair the `provider` field names. Every provider implements the same `*ProviderClient` interface already defined for that connector type — the interface itself didn't need to change, only the number of implementations behind it.

#### S6.4.1 `storage` (fetch / upload / delete)

Camunda reference shape: S3/Azure Blob/GCS/Box connectors. Four providers (§S10 Decision #18): `aws-s3`, `azure-blob`, `gcp-gcs`, `google-drive`.

`content`/`contentRef` below use the "document ref" shape defined in §S6.2 — an opaque string, issued by this connector's own `fetch`/`upload` output for other connectors to consume as-is. Operational fields are shared across every provider; only the credential fields and the name of the container-like field differ.

| | Field | Type | Notes |
| --- | --- | --- | --- |
| IOMapping input | `provider` | enum (`aws-s3`\|`azure-blob`\|`gcp-gcs`\|`google-drive`) | Required, no default (§S10 Decision #20). |
| IOMapping input | `operation` | enum (`fetch`\|`upload`\|`delete`) | |
| IOMapping input | `bucket` | string | The container-like field — a bucket (`aws-s3`/`gcp-gcs`), a container (`azure-blob`), or a folder ID (`google-drive`). One field name across all four; the value's meaning is provider-specific. |
| IOMapping input | `key` | string (dynamic, e.g. sourced from a workflow variable) | The document path/identifier. |
| IOMapping input | `content` | document ref | Only for `upload`. |
| IOMapping input | `createDocument` | bool | For `fetch` — `true` creates a document reference, `false` returns content inline (same distinction Camunda's own S3 connector draws); for `upload` of literal content, `true` also creates a reference to the uploaded bytes. |
| Output | `contentRef` or `content` (+ `contentEncoding`) | document ref / inline | `contentRef` for large objects, inline `content` (up to 1 MiB, `utf-8` or `base64`) for small ones. |
| Output | `contentType`, `sizeBytes`, `fetchedAt` | string / int / timestamp | |

Provider-specific credential fields (all secrets-store references, §S6.2). Field names are globally unique across all four providers, not just within one provider's own row — this is load-bearing, not a style choice; see §S9 phase 2's note on why:

| Provider | Credential fields |
| --- | --- |
| `aws-s3` | `accessKey`, `secretKey`, `region` |
| `azure-blob` | `azureAccountName`, `azureAccountKey` |
| `gcp-gcs` | `gcpServiceAccountKey`, `projectId` |
| `google-drive` | `driveServiceAccountKey` (domain-wide delegation, or the target folder shared directly with the service account) |

Retry guidance: safe to retry (read for `fetch`, idempotent overwrite for `upload` when `key` is caller-supplied and stable) for `aws-s3`/`azure-blob`/`gcp-gcs`. `google-drive` has no path-keyed upsert the way the other three do: its uploads, fetches and deletes go through the document registry (Part I §4.5, §8.4) — a row per tenant + folder + filename claimed by compare-and-set, so only one call at a time writes a document. An upload updates the row's recorded file in place; with none recorded it adopts the file tagged with the document ID (a crashed attempt's), else the oldest untagged same-name file (one placed in the folder by hand, which it tags), and only then creates one. That is what keeps this type-level `RetryPolicySafe` honest for Drive — a retry never creates a second file — not a separate provider-level retry policy. Delete removes the document's recorded, tagged or adopted file and is idempotent (a missing file is already deleted), as it is for the other three.

#### S6.4.2 `send-email`

Camunda reference shape: SendGrid connector. Four providers (§S10 Decision #18): `sendgrid`, `aws-ses`, `microsoft-365`, `google-workspace`.

| | Field | Type | Notes |
| --- | --- | --- | --- |
| IOMapping input | `provider` | enum (`sendgrid`\|`aws-ses`\|`microsoft-365`\|`google-workspace`) | Required, no default (§S10 Decision #20). |
| IOMapping input | `senderName`, `senderEmail` | string | |
| IOMapping input | `receiverName`, `receiverEmail` | string | Workflow-instance data (e.g. an applicant's address) — sourced from workflow variables. |
| IOMapping input | `subject` | string | |
| IOMapping input | `contentType` | enum (`text/plain`\|`text/html`) | |
| IOMapping input | `body` | string | Required for `microsoft-365`/`google-workspace` regardless of `templateId` (§S10 Decision #22) — those two have no template mechanism to fall back on. |
| IOMapping input | `attachments` | list of document refs | Resolved through the same document-ref service `storage` writes to (`docref.Service`, Part I §4.1; §S10 Decision #22) — a doc ref minted by a `storage` fetch is attachable here. No per-attachment filename field exists; one is synthesized from the resolved content type. |
| IOMapping input | `templateId` | string, optional | Template variant — if set, `subject`/`body` are ignored in favor of the provider's own template rendering. `microsoft-365`/`google-workspace` have no equivalent server-side template mechanism; ignored for those two. |
| Output | `sent` | bool | |
| Output | `messageId`, `sentAt` | string / timestamp | `microsoft-365`'s `sendMail` returns no message ID synchronously (202 Accepted, empty body) — `messageId` is empty for that provider. |

Provider-specific credential fields:

| Provider | Credential fields |
| --- | --- |
| `sendgrid` | `apiKey` (secrets-store reference) |
| `aws-ses` | `accessKey`, `secretKey` (secrets-store references), `region` (plain config value) |
| `microsoft-365` | `tenantId`, `clientId` (plain config values — identifiers, not credentials), `clientSecret` (secrets-store reference; Graph API app credentials, `Mail.Send` application permission) |
| `google-workspace` | `serviceAccountKey` (secrets-store reference; domain-wide delegation, `gmail.send` scope) |

Retry guidance: **not** safely retryable in general (duplicate-send risk). Since Part I rev 1.10 the policy is `not-delivered`: only a transient failure the provider provably never accepted (`ErrNotDelivered`: DNS, connection refused, 429/408) is retried automatically; an unknown outcome never is (Part I §5.4.2a, §9.2).

#### S6.4.3 `document-extract` (OCR / structured extraction)

Camunda reference shape: Amazon Textract connector. Four providers (§S10 Decision #18): `aws-textract`, `azure-document-intelligence`, `gcp-document-ai`, `abbyy-vantage` — the four engines Camunda's own Intelligent Document Processing feature itself supports, confirmed directly against Camunda's docs rather than assumed.

**Real-time only in v1, for every provider.** Textract's own async mode (submit a job, then poll or get an SNS callback on completion) doesn't fit `Connector.Execute`'s single synchronous call/return (§S6.3), and an SNS callback is the same inbound, correlated-response shape §S6.4.7 already defers. `executionType`, `outputS3Bucket`, and the notification-channel fields are dropped from v1 entirely rather than carried as dead properties nothing consumes — async/polling revisits once §S6.4.7's inbound mechanism actually exists (§S10 Decision #11).

The output shape below is normalized across all four providers — each has its own native response format, but `Connector.Execute` maps every provider's result onto this one shape so a workflow author's `IOMapping.Outputs` doesn't need to branch on which provider ran.

| | Field | Type | Notes |
| --- | --- | --- | --- |
| IOMapping input | `provider` | enum (`aws-textract`\|`azure-document-intelligence`\|`gcp-document-ai`\|`abbyy-vantage`) | Required, no default (§S10 Decision #20) — design only, no real implementation yet. |
| IOMapping input | `documentLocation` | enum (`s3`\|`inline`) | `s3` is meaningful only for `aws-textract`; every other provider always receives the document inline via `documentRef`, regardless of this field. |
| IOMapping input | `documentBucket`, `documentName`, `documentVersion` | string | Only if `documentLocation = s3` (`aws-textract` only). |
| IOMapping input | `documentRef` | document ref | Only if `documentLocation = inline`. |
| IOMapping input | `analyzeForm`, `analyzeSignatures`, `analyzeLayout`, `analyzeQueries` | bool | Same four analyze-flags across all providers; a provider without a native equivalent for one flag (e.g. `abbyy-vantage` has no signature-specific mode) folds it into its closest existing extraction pass rather than erroring. |
| IOMapping input | `query` | string | Only if `analyzeQueries`. |
| IOMapping input | `clientRequestToken`, `jobTag`, `kmsKeyId` | string, all optional | `aws-textract`-specific; ignored by the other three providers. |
| Output | `fields` | map | For `analyzeForm`. |
| Output | `rawText` | string | Full-text extraction. |
| Output | `signaturesDetected` | list | For `analyzeSignatures`. |
| Output | `answers` | list | For `analyzeQueries`. |
| Output | `confidence` | map | Keyed by field name — one confidence score per extracted field, not a single scalar. |

Provider-specific credential fields (all secrets-store references, §S6.2):

| Provider | Credential fields |
| --- | --- |
| `aws-textract` | `accessKey`, `secretKey`, `region` |
| `azure-document-intelligence` | `endpoint`, `apiKey` |
| `gcp-document-ai` | `serviceAccountKey`, `projectId`, `location`, `processorId` |
| `abbyy-vantage` | `endpoint`, `apiKey` (Vantage is cross-cloud — no cloud IAM credential shape) |

Retry guidance: safe to retry (read-only call against the source document) for every provider — unambiguous now that there's no async-mode job-submission step to worry about double-triggering.

#### S6.4.4 `rest-call`

Camunda reference shape: the generic REST connector — scoped down significantly, see below.

**Internal platform APIs only, alias-based, never a raw URL** (§S10 Decision #4) — the workflow never supplies a real URL, and the target is never an external domain. This closes the SSRF/unknown-idempotency risk flagged against a generic "call whatever the plan names" mechanism in this feature's own originating investigation, while still allowing a general-purpose internal-API connector to exist.

| | Field | Type | Notes |
| --- | --- | --- | --- |
| IOMapping input | `endpointAlias` | string | A symbolic name resolved against `cmd/connector-worker`'s own internal-service registry — a static config file it loads at startup, redeployed to add/change an alias (§S10 Decision #12) — never a raw URL. |
| IOMapping input | `pathParams`, `queryParams`, `body` | map / any | |
| Output | `status` | int | |
| Output | `headers` | map | |
| Output | `body` | any | |

Auth (§S6.2): every call carries the platform's `x-internal-token` and the acting user/tenant's forwarded `x-departments` header — both mandatory, no per-call configuration needed.

Retry guidance: retryable only if the resolved internal endpoint's method is idempotent (e.g. a `GET` lookup) — a `POST`/non-idempotent endpoint gets no automatic retry.

#### S6.4.5 `sql-query`

Camunda reference shape: the generic SQL connector — scoped down significantly, see below.

**Internal platform databases only, alias-based, read-only, never raw SQL** — same rationale as `rest-call`: a raw arbitrary query would reopen the "never an arbitrary/dynamically-targeted call" boundary §S1 establishes. v1 scopes this connector to pre-registered, read-only named queries against this platform's own databases only; write queries and external databases are both out of scope for v1.

**Data-access model, confirmed (see §S10 Decision #17): an internal HTTP proxy, structurally identical to `rest-call`, never a direct database connection.** `queryAlias` resolves to the *owning service's own* internal query-execution endpoint plus a `queryId` that service runs on our behalf against its own pre-registered, read-only statement — `cmd/connector-worker` never holds credentials to, or opens a connection against, any platform database directly, and never sees or constructs SQL text. This reading is what makes this section's own "both required, both mandatory" auth statement below literally true: `x-internal-token`/`x-departments` are real HTTP headers on that request, which have no meaning on a raw database connection.

| | Field | Type | Notes |
| --- | --- | --- | --- |
| IOMapping input | `queryAlias` | string | A pre-registered named read-only query, resolved against `cmd/connector-worker`'s own internal registry — the same static config file `rest-call`'s `endpointAlias` uses (§S10 Decision #12) — to the owning service's own query-execution endpoint plus a `queryId`, never raw SQL and never a direct DB connection (Decision #17). |
| IOMapping input | `params` | list | Bound as query parameters by the owning service itself, never string-interpolated by this connector. |
| Output | `resultSet` | list of rows | Row count bounded (a fixed cap, e.g. a few hundred rows) to keep `context_json` small — a large result set is a design smell for a workflow variable, not this connector's job to paginate. |

Auth (§S6.2): same as `rest-call` — `x-internal-token` plus forwarded `x-departments`, both mandatory.

Retry guidance: retryable, because scoping to read-only aliases makes every v1 query naturally idempotent.

#### S6.4.6 `chat-notify` (Slack/Teams)

Camunda reference shape: the Slack connector. Two providers (§S10 Decision #18): `slack`, `teams` — both named explicitly, not "Slack or Teams, pick one."

| | Field | Type | Notes |
| --- | --- | --- | --- |
| IOMapping input | `provider` | enum (`slack`\|`teams`) | Required, no default (§S10 Decision #20) — design only, no real implementation yet. |
| IOMapping input | `method` | enum (`create-channel`\|`invite-to-channel`\|`post-message`) | Same three methods for both providers — Teams' Graph API has a direct equivalent for each (create channel, add member, post message). |
| IOMapping input | `channelName` | string | Only for `create-channel`. |
| IOMapping input | `visibility` | enum (`public`\|`private`) | Only for `create-channel`. |
| IOMapping input | `inviteBy`, `channelNameOrId`, `users` | string / list | Only for `invite-to-channel`. |
| IOMapping input | `channelOrUser`, `thread`, `messageType`, `message` or `messageBlock`, `attachments` | string / list | Only for `post-message`. |
| Output | `sent` | bool | |
| Output | `messageId` | string | |

Provider-specific credential fields (all secrets-store references, §S6.2):

| Provider | Credential fields |
| --- | --- |
| `slack` | `authToken` (bot token, `channels:manage`/`chat:write` scopes) |
| `teams` | `tenantId`, `clientId`, `clientSecret` (Graph API app credentials, `Channel.Create`/`ChannelMessage.Send` scopes) |

Retry guidance: **not** safely retryable (duplicate-message risk), same as `send-email` — single attempt, for both providers.

#### S6.4.7 Deferred from v1

**Inbound webhook** (Camunda reference: HTTP Webhook connector) was explicitly deferred — a structurally different, inbound (not outbound) connector shape: an external system starts or advances a task by calling in, rather than a connector calling out. Revisit once there's a concrete use case; its design would need its own correlation/auth model (Camunda's own shape uses HMAC/API-key/JWT auth plus a FEEL correlation-key expression, per this feature's own research trail), not a straightforward extension of the outbound catalogue above.

**`llm-verify`** (AI document verification) was designed, then removed from the v1 catalogue entirely (§S10 Decision #3) — see §S5.3 for what its removal took with it (the auto/manual decision mechanism).

### S6.5 Runtime Loop

Placement-agnostic in principle (§S6.1 settled on one specific process, `cmd/connector-worker`, but nothing below depended on that):

0. **Delivery and dedup — a two-hop concern** (event redelivery, not workflow replay; §S10 Decision #10). Upstream, `WorkflowTaskCreated` arrives at `cmd/server`'s existing `/internal/events` handler over SNS/SQS (at-least-once) exactly as today — that hop dedups via the existing `processed_event(event_id, consumer, event_type, processed_at)` table and pattern (`execution_service.md` §6.3) before anything is pushed onward, unchanged. For connector-typed tasks specifically, that handler additionally pushes the event onto a **Valkey Stream** — a Stream, not a plain list, because a list's `BRPOP` gives no redelivery safety net if a consumer crashes mid-item, while a Stream's consumer-group semantics do. `cmd/connector-worker` reads via `XREADGROUP` and only `XACK`s an entry once step 3 below has actually completed (success or fail-signal), or once it dead-letters the entry (below) — an un-acked entry (a crashed or slow consumer) becomes eligible for another consumer to reclaim (`XCLAIM`/`XAUTOCLAIM`), the Stream's own redelivery mechanism; no `processed_event` row is needed at this second hop, since the Stream's own ack-tracking already gives at-least-once-plus-idempotent-handler semantics for it. (Separately: Temporal workflow *replay* cannot itself cause a duplicate connector execution in this design at all, regardless of either hop's dedup — see §S6.1's closing note.)

   **A task's connector runs once, however often its entry is delivered.** Redelivery covers a failed callback as well as a dead consumer, and a connector's side effect (an email, a non-idempotent `rest-call`) must not repeat with it. For each entry the worker, in order:

   - dead-letters an entry delivered more than `CONNECTOR_STREAM_MAX_DELIVERIES` times (default 10): it copies the entry, with its reason, onto the stream `CONNECTOR_STREAM_DEAD_LETTER_KEY` and acks it. The task stays open for an operator to force-forward it or re-drive the entry. A re-drive within the outcome's 24 hours re-sends the recorded outcome; a later one runs the connector again. `connector_dispatch_dead_lettered_total` counts these.
   - re-sends the recorded outcome, if the task has one, and acks. The connector does not run again. An outcome that cannot be read leaves the entry pending.
   - takes an execution lease on the task once a pool slot is free, lasting the connector type's timeout plus 10 seconds. While another consumer holds the lease, or the cache cannot be reached, the entry stays pending.
   - runs the connector, records its outcome (the output, or the error class) for 24 hours, then calls back and acks.

   **Only a dead consumer's entries are reclaimed.** An entry's idle time starts at delivery, and would otherwise keep running while the entry waits for a pool slot and while it runs. The worker touches every entry it has read and not yet finished with (`XCLAIM … JUSTID` to itself, which resets the idle time without counting a delivery) three times per `CONNECTOR_STREAM_CLAIM_MIN_IDLE`. An entry becomes reclaimable only once its consumer stops touching it: the consumer died, left the entry pending on purpose, or has a run that outlived its lease and is treated as hung. A consumer that is handed a second copy of an entry it already holds leaves it to the first. A reclaimed entry whose delivery count cannot be read is still dispatched, uncapped for that delivery.

   **A run that has started finishes.** Shutdown stops reading entries. A run already under way keeps its own context, records its outcome and reports it. The process waits for the longest connector timeout plus the callback timeout plus 10 seconds, and the pod's termination grace period exceeds that wait.

   Reclaim runs every `CONNECTOR_STREAM_CLAIM_MIN_IDLE`, so redeliveries of an abandoned entry land up to twice that apart. The outcome's 24 hours exceed twice the cap times that wait. The worker refuses to start otherwise, or with a wait under 3 seconds, or with a dead-letter stream that is the stream itself. The completion endpoints treat a callback for a resolved task as a no-op (§S5.5), which is what makes a re-send safe. One window remains: when the side effect has happened but the outcome was not recorded (the process was killed, or Valkey was unreachable) and the entry was not acked, the connector runs again once its lease expires. Closing it needs the external system's own idempotency key.

1. `WorkflowTaskCreated` arrives with `connector_type` set. If the handler doesn't recognize that type, it calls the fail-signal path (step 3) immediately instead of ignoring the event — an unregistered connector type is a real workflow failure now, not a silent no-op (§S2, §S10 Decision #9).
2. Dispatch to that connector type's own bounded worker pool (sized to the external system's own rate limits, not a single shared pool — a storage fetch and an email send have very different latency/rate-limit profiles), then call `Connector.Execute(ctx, input)` under a per-connector-type internal execution timeout — a ceiling on total time including retries/backoff. `input` already carries everything needed (§S6.2): author-supplied config for `storage`/`send-email`/`document-extract`/`chat-notify`, with any provider credential resolved from its OpenBao secret path just before the call, or `endpointAlias`/`queryAlias` (resolved against the static alias-registry config file, §S10 Decision #12) plus a forwarded internal-auth context for `rest-call`/`sql-query`.
3. On success, call `execution_service`'s new `POST /internal/connector-tasks/:id/complete` endpoint (§S5.5, §S10 Decision #16) with `Connector.Execute`'s raw `output` map, unmodified. The rename step — `IOMapping.Outputs` mapping each of `output`'s top-level keys (§S6.3) to a workflow variable, no nested/dot-path access in v1 (§S10 Decision #13) — happens on `execution_service`'s own side, inside that endpoint, not in `cmd/connector-worker`: `CreateTaskActivity` already persisted the compiled stage's `IOMapping.Outputs` alongside its resolved inputs at task-creation time, so `execution_service` is where that mapping already lives, and the connector-worker→execution_service contract stays "send whatever `Execute()` returned, verbatim." On failure, the connector's own retry/backoff (§S6.4's per-type policy) runs inside this same handler; once exhausted, **or once this step's own internal execution timeout elapses first**, the handler calls `POST /internal/connector-tasks/:id/fail` instead of leaving the task open — the task never sits open waiting for anyone (§S10 Decision #9). Both endpoints route through the interpreter's existing `stage-transition`/`stage-fail` signal resolution and its `FAILED`/`DEGRADED` machinery, the same outcome a non-retryable Activity error already produces on the main/Sequential path or inside a `Parallel` branch, respectively — no new interpreter state, only a new way to reach the existing ones. The task's `DueDate`/`FollowUpDate` SLA timer becomes functionally moot for a connector task under this model: with no human wait ever left open, it simply never fires before the task resolves one way or the other.

## S7. External Systems & Licensing

The v1 catalogue (§S6.4 — six connector types) calls external services (a storage provider, an email provider, an OCR provider, a chat provider) directly from `cmd/connector-worker` (§S6.1); `rest-call`/`sql-query` call only this platform's own internal APIs/databases (§S6.2), never an external domain.

**Camunda's own connectors and engine are not reused, and this is a licensing decision, not a preference.** Checked directly against Camunda's published licensing terms:

- The core Zeebe/Camunda 8 engine's source is under the Camunda License v1 (source-available, not open source); its compiled runtime requires purchasing the Enterprise Edition for production use. This was already moot here since this platform runs its own Temporal-based engine, not real Zeebe; it only confirms that door was never open.
- Camunda's out-of-the-box connectors (S3, SendGrid, Textract, Slack, and every other one in its 40+ catalogue) are the same story one level down: only the Connector SDK, the plain REST connector, the Connector Runtime Docker image, and the Connectors Bundle Docker image are Apache 2.0. Every other out-of-the-box connector's source is under that same Camunda License v1, gated the same non-production/Enterprise way when compiled and distributed.

Consequence: this design does not adopt Camunda's actual connector type identifiers, nor copies their element-template JSON files. What is used: the element-template JSON *format* itself (a documented, open mechanism Camunda Modeler reads, not Camunda's proprietary content) and the full field/property shape of what each connector type in §S6.4 needs; informed by public documentation, not copied from licensed source. The `connector:` type-naming convention (§S4.1) and every template this design produces are original.

## S8. Security & Data Handling

- **Provider credentials for `storage`/`send-email`/`document-extract`/`chat-notify` never reach `context_json`, Temporal's workflow history, the `WorkflowTaskCreated` event payload, or the Valkey Stream that payload is additionally pushed onto for connector-typed tasks (§S6.5).** `definition_service` still collects the raw value once, at author time, but writes it to OpenBao and passes only the resulting secret path through `IOMapping`/`context_json` (§S6.2); `cmd/connector-worker` resolves that path to the real value in memory, only inside `Connector.Execute()`. This is mandated design, not a UI-side convention.
- **A task reads only its own tenant's credentials.** The worker refuses any secret reference other than `connectors/<task tenant>/<connector type>/<field>` before reading it (§S6.2), because its OpenBao token is not scoped to a tenant.
- **`rest-call`/`sql-query` calls always carry both required auth pieces** — the platform's `x-internal-token` and the acting user/tenant's forwarded `x-departments` header (comma-separated `dept_uuid:role` pairs, e.g. `018e1f2a-...:reviewer`) — never one without the other (§S6.2). No per-tenant secret is involved for these two connector types, but the auth itself is mandatory and explicit.
- **No v1 connector automatically forwards a received document to a third-party service with no person in the loop.** `llm-verify` — the connector design that would have done exactly that, forwarding to an external LLM provider — is not part of the v1 catalogue (§S6.4.7, §S10 Decision #3); nothing in v1 raises that specific trust-boundary question.

## S9. Follow-Ups (Non-Blocking)

None of the items below is an open design question on this side — each is a narrower, operational item tracked separately, and none blocks or contradicts anything in §S1–§S8.

- **`definition_service`'s alias-discovery UX for `rest-call`/`sql-query`.** §S6.4.4/§S6.4.5's `endpointAlias`/`queryAlias` are typed as free-text strings against the alias registry (now owned by `definition_service` — §S10 Decision #21); an author currently has no way to see which aliases actually exist while filling in the authoring form. `GET /internal/connector-aliases` (Decision #21) makes this addressable but no UI consumes it yet. The compiler already degrades a dangling alias the same lenient way as any other unrecognized identifier (§S4.2), so this doesn't block v1 — but a blind-typed, security-load-bearing field is a real authoring-UX gap worth closing.
- **Credential orphan cleanup and TTL.** *Rotation* needs no mechanism — OpenBao KV-v2 versions on overwrite, so re-submitting the authoring form rotates the value, and the read side always resolves the latest version. *Revocation* is explicit and admin-gated (§S6.2). What is missing is a sweep: nothing deletes the secret at a path whose owning connector task is removed or whose workflow version is retired, so it is leaked until someone revokes it by hand. Credentials are never rowed in Postgres — there is no `created_at`/`rotated_at`/`expires_at` anywhere to drive a sweep from — so automatic cleanup would have to hook the archive path directly into revoke. TTL/expiry is deliberately undesigned. Doesn't block the OpenBao architecture in §S6.2.
- **`rest-call`'s alias `BaseURL` is bounded by an internal-host allowlist, enforced at alias-write time** (`definition_service.md` §10.15). "Never a raw URL, always an internal service" (§S6.4.4) is a rule the write path enforces rather than a convention asserted in comments: a write is rejected unless the host is on `CONNECTOR_ALIAS_ALLOWED_HOSTS` — exact hosts, or a leading-dot suffix rule — and unless the scheme is `http`/`https` with no embedded userinfo. It fails closed, so an unconfigured deployment rejects every alias write rather than accepting any host. Enforcement is on that write path and not in `restcall.go`: the allowlist is deployment configuration and this library takes none.
- **`sql-query`'s data-access model needs a fuller review before any more is built on it.** Deliberately not scoped or designed further right now.
- **Real SDK implementation for §S6.4's 14 (4 types × 4 providers, minus chat-notify's 2) provider clients.** §S10 Decision #18 designed the shape. **`storage` and `send-email` are both done as of 2026-08-25** (§S10 Decision #19/#20 for `storage`, #22 for `send-email`) — 8 of the 14 provider clients are real in `workflow-connectors`. **Neither is yet wired into `cmd/connector-worker/deps.go`** — blocked on `workflow-connectors` actually being pushed (nothing is pushed anywhere yet) and `execution_service`'s `go.mod`/vendored copy being bumped past the pre-`storage` commit it's still pinned to; a `TODO` in `deps.go` names exactly what to wire once unblocked. The remaining 2 types are still unstarted, running against their in-memory mocks — for each, the `provider` registry field ships *with* its real implementation, not ahead of it — advertising a provider choice with no code behind it is misleading (§S10 Decision #20):
  1. **`document-extract`'s 4 providers** (`aws-textract`/`azure-document-intelligence`/`gcp-document-ai`/`abbyy-vantage`) — `storage`/`send-email`'s per-call-construction-with-cache pattern is the template; 4 independently-shippable slices.
  2. **`chat-notify`'s 2 providers** (`slack`/`teams`).
  3. **Hardening** — a shared error taxonomy (transient vs. permanent, mapped from each provider SDK's own errors) so Decision #6's per-type `Retry` policy stays meaningful once multiple providers sit behind one type; LLD rev bump + CHANGELOG.

  Each of these ships independently — a type/provider pair can go out without waiting on the rest.

## S10. Design Decision Log

| # | Date | Decision | Rationale |
| --- | --- | --- | --- |
| 1 | 2026-08-07 | A connector's result reaches the workflow via the *existing* task-completion path (an event-driven callback), not a new Temporal Activity dispatched to a dedicated task queue. | Needs zero changes to the Temporal interpreter for the success path — completion is just another signal-wait resolution. `cmd/connector-worker`'s code runs entirely outside the workflow function's own Temporal replay boundary, so replay cannot re-invoke it — the real risk is event redelivery (SNS/SQS upstream, a Valkey Stream downstream, §S10 Decision #10), closed by §S6.5 step 0's two-hop dedup, a different, already-solved problem on this platform. Failure detection itself is a separate, later decision — §S10 Decision #9. |
| 2 | 2026-08-12 | Worker placement: a new `cmd/connector-worker` binary lives inside `execution_service`'s own repo, sharing its CI/deploy pipeline, a distinct process from `cmd/server`/`cmd/worker`. It is not a Temporal Worker — it never registers against `execution_service`'s Temporal task queues or touches the Temporal SDK. | The workflow team owns and runs the connector runtime as trusted platform infrastructure rather than delegating it to a domain service or a separate standalone repo. This is what makes §S6.2's credential design safe without a runtime callback to anywhere else. |
| 3 | 2026-08-12 | v1 catalogue is six connector types: `storage`, `send-email`, `document-extract`, `rest-call`, `sql-query`, `chat-notify`. Every one is unconditionally automatic — no manual-override mechanism exists in v1. Inbound webhook is deferred (a structurally different, inbound-not-outbound shape). | `llm-verify` was designed, then removed — it was the only connector type that ever needed a manual-override decision (it replaced a pre-existing human check); with it gone, no v1 connector has that story, so no decision mechanism is carried forward as unused scaffolding. |
| 4 | 2026-08-12 | `rest-call`/`sql-query` are alias-based and internal-platform-only — a workflow only ever supplies a pre-registered symbolic name (`endpointAlias`/`queryAlias`), never a raw URL or SQL string, and the resolved target is always another service/database on this platform, never an external domain. Every call carries both the platform's `x-internal-token` and the caller's forwarded `x-departments` header (`dept_uuid:role` pairs), both mandatory. | Without aliasing, a generic REST/SQL connector reopens exactly the risk this feature is scoped to avoid — unbounded-target SSRF exposure and unknown retry-safety. No per-tenant secret is needed for either type, but the auth itself is real and mandatory, not waved away. |
| 5 | 2026-08-13 | Provider credentials for `storage`/`send-email`/`document-extract`/`chat-notify` travel as an **OpenBao secret path** (KV v2), never a raw value. `definition_service` writes the raw credential to OpenBao at author time; `IOMapping`/`context_json` carries only the path; `cmd/connector-worker` resolves it to the real value via OpenBao's KV-v2 read API, in memory, only inside `Connector.Execute()`. | Letting the raw credential flow through `context_json` like an ordinary workflow variable would be under-protected relative to how this platform already treats its own comparable secrets (execution_service.md §9.4), and would expose it more widely besides (workflow history, the task-created event payload). Reusing the platform's existing secrets-management mechanism (OpenBao), extended to per-tenant author-time secrets, closes that gap without inventing new infrastructure. |
| 15 | 2026-08-13 | Connector-authoring templates (§S4.3) and credential custody (§S6.2) are owned by `definition_service`, not BE-for-UI. | Fixed, code-generated catalogue metadata (from `pkg/registry`) with no tenant-stored content of its own is a better fit for `definition_service`, which already owns design-time authoring/compilation and already imports the same registry package (`definition_service.md` §10.14/§10.15) — not BE-for-UI, whose own database is reserved for genuinely new stored entities like its "custom BPMN module"/"reusable authoring component" library (`execution_service.md` §1.3/Appendix A.2 #31), a different kind of responsibility this document's connector catalogue never needed. |
| 6 | 2026-08-12 | Per-connector retry/idempotency policy (§S6.4) is ratified as final: safe to retry — `storage`, `document-extract`, `sql-query`; not safely retryable — `send-email`, `chat-notify`; retryable only if the resolved method is idempotent — `rest-call`. | Fetching a document or running a read-only query is naturally safe to retry; sending an email or chat message is not (double-send risk). Stating this as policy rather than a recommendation gives `cmd/connector-worker`'s per-type retry/backoff a single source of truth. |
| 7 | 2026-08-12 | `cmd/connector-worker` bounds each connector call with a per-connector-type internal execution timeout (covering total time including retries/backoff). On timeout or retry exhaustion, the handler calls the fail-signal path (§S10 Decision #9) instead of leaving the task open — no interpreter, DSL, or schema change beyond the fail-signal itself. | A connector task's `DueDate`/`FollowUpDate` SLA timer becomes functionally moot under this model — with no human wait ever left open, it never fires before the task resolves one way or the other. `DEGRADED` stays scoped to `Parallel`-branch failure generally, now including a connector task's own fail-signal if it happens to be inside one. |
| 8 | 2026-08-07 | Camunda's own connector type identifiers, element templates, and runtime are not reused. | Camunda's actual out-of-the-box connectors and engine require a paid Enterprise license for commercial/production use (§S7), confirmed directly against Camunda's published licensing terms. The open template *format* and full field/property shape are used as reference; the specific licensed content is not. |
| 9 | 2026-08-12 | Connector tasks are fully automation-only: neither an unregistered connector type nor an exhausted/failed connector call falls back to a human. Both now route through a new signal `cmd/connector-worker` calls, which the interpreter resolves exactly like a non-retryable Activity error already does today — `FAILED` on the main/Sequential path, contributing to `DEGRADED` inside a `Parallel` branch. | There is no existing "fail this task" signal today — `FAILED`/`DEGRADED` have only ever originated from an Activity error raised inside the workflow function itself — so this is new interpreter surface, not a documentation change; flagged as the highest-risk item in this design. |
| 10 | 2026-08-12 | `WorkflowTaskCreated` reaches `cmd/connector-worker` via a Valkey Stream, not a listener of its own. `cmd/server`'s existing `/internal/events` handler additionally pushes connector-typed events onto the Stream; `cmd/connector-worker` is a pure `XREADGROUP`/`XACK` consumer with no inbound HTTP surface, no ingress `NetworkPolicy` grant, and no `INTERNAL_API_TOKEN` of its own. | Considered against giving `cmd/connector-worker` its own inbound listener (matching §S6.1's "distinct process" framing more literally) — rejected for a materially larger attack surface with no offsetting benefit. Valkey is already deployed and used by `cmd/server` today (the compiled-plan cache, the idempotency store); this is a new usage of existing infrastructure, not a new platform-wide dependency. A Stream, not a plain list, is used specifically for its consumer-group ack/redelivery semantics — a list's `BRPOP` has none. |
| 11 | 2026-08-12 | `document-extract` is scoped to real-time execution only in v1 — `executionType`, `outputS3Bucket`, and the notification-channel fields are dropped from the catalogue entirely. | Textract's own async mode (submit-then-poll-or-notify) doesn't fit `Connector.Execute`'s single synchronous call/return, and an SNS callback is the same inbound, correlated shape §S6.4.7 already defers — carrying those fields without a design for consuming them would be dead scaffolding, not forward-compatibility. |
| 12 | 2026-08-12 | `rest-call`'s `endpointAlias` and `sql-query`'s `queryAlias` resolve against a static config file `cmd/connector-worker` loads at startup — no new database table, no admin CRUD API. | Simplest mechanism that still satisfies the alias-based SSRF-prevention story (Decision #4); a DB-table alternative would add real scope (schema, endpoints, authz for who can register an alias) for a registry six connector types don't yet need to change at runtime. |
| 13 | 2026-08-12 | `Connector.Execute` returns `map[string]any`; `IOMapping.Outputs`' `Source` addresses only that map's top-level keys in v1 — no dot-path/nested access. | The alternative (nested path access, e.g. into `document-extract`'s own `fields` map) needs real path-parsing code with no existing precedent anywhere in this codebase to reuse; a flat top-level map is enough to make every v1 connector's output usable as a workflow variable today. |
| 14 | 2026-08-12 | "Document ref" (used across `storage`, `document-extract`, `send-email`'s field tables) is a plain opaque string, issued by the `storage` connector's own `fetch`/`upload` output and consumed as-is by any other connector's document-ref-typed field — no separate Document Service. | The term was used five times across the catalogue without ever being defined; a minimal, self-issued opaque reference is enough to make those fields concrete without inventing a new subsystem this feature doesn't otherwise need. |
| 16 | 2026-08-17 | A connector task's result reaches the workflow via a **new** `execution_service` endpoint (`POST /internal/connector-tasks/:id/{complete,fail}`), not the same completion endpoint a human uses. Both endpoints are thin siblings that ultimately call the same interpreter signal resolution (`stage-transition`/`stage-fail`) — Decision #1's substance ("an existing signal-wait resolution, not a new Temporal Activity") still holds; only the HTTP entry point differs. | `execution_service`'s `TaskService.checkHumanActionable` explicitly rejects a connector-typed task on every human-facing action, including `Complete`, since one has no human assignee. `cmd/connector-worker` never touches the Temporal SDK directly (§S6.1) — the new endpoint is what touches it, on `execution_service`'s own side, same as every other `TaskService` method already does. The endpoint also applies `IOMapping.Outputs`' rename (Decision #13) itself, since `execution_service` is where that mapping is persisted (at task-creation time); `cmd/connector-worker` sends `Connector.Execute`'s raw output, unmodified. |
| 17 | 2026-08-17 | `sql-query`'s data-access model, confirmed: an internal HTTP proxy to the owning service's own query-execution endpoint (structurally identical to `rest-call`), never a direct database connection held by `cmd/connector-worker`. | §S6.4.5's "internal platform databases only" phrasing, combined with §S6.2's "both required, both mandatory" `x-internal-token`/`x-departments` header language, was ambiguous between this reading and a direct-DB-connection alternative (headers have no meaning on a raw DB connection, which was the deciding factor). Direct-DB access was rejected as a materially larger security surface — one process holding credentials to multiple services' own databases — for no offsetting benefit over each service running its own already-trusted, already-scoped query. |
| 18 | 2026-08-24 | `storage`, `send-email`, `document-extract`, `chat-notify` each become multi-provider: `aws-s3`/`azure-blob`/`gcp-gcs`/`google-drive`; `sendgrid`/`aws-ses`/`microsoft-365`/`google-workspace`; `aws-textract`/`azure-document-intelligence`/`gcp-document-ai`/`abbyy-vantage`; `slack`/`teams` (§S6.4.1/6.4.2/6.4.3/6.4.6). A new `provider` input field selects which one, defaulting to the first-listed value when a task omits it. `rest-call`/`sql-query` are unaffected — internal-platform-only, no provider concept applies. | v1's catalogue committed to exactly one Camunda-reference provider per type; this org's actual environment spans multiple clouds and vendors per type, and hardcoding one would force every tenant onto whichever single vendor was picked first. Each provider still implements the same per-type interface (§S6.3) already defined — additive, not a redesign. |
| 19 | 2026-08-25 | **`storage` is real for all 4 providers.** (1) No OpenBao path migration: every provider's credential fields are given globally-unique names (§S6.4.1's table) instead — `resolveSecrets`/`Reader.Read` key purely off field name, never enumerate by provider, so a collision-free name avoids any path-format migration; this is pre-prod with zero real secrets ever written for `storage`. (2) `Config.StorageProviders` is a `map[string]StorageProviderConstructor` (one constructor per provider), not a map of pre-built clients — credentials are per-tenant and resolved fresh per call, so there's no fixed client to build once at `cmd/connector-worker` startup; a small bounded in-memory cache (keyed by provider+bucket+credential hash) avoids re-authenticating identical calls, real for `google-drive` specifically (its client construction does a JWT-bearer OAuth token exchange). (3) Doc-ref handling (`createDocument`/`content` round trip) is a store owned by `storageConnector` itself, constructed once — a per-call-constructed real client can't carry that state the way a single shared mock instance could. `aws-s3`/`azure-blob`/`gcp-gcs` are backed by `gocloud.dev/blob` (one dependency, three drivers) behind the unchanged `StorageProviderClient` interface; `google-drive` gets its own client since gocloud.dev has no Drive driver (a file/folder API, not bucket/key) — its `Upload` searches by name-in-folder and updates-if-found rather than blind-creating, which is what keeps this type's `RetryPolicySafe` (Decision #6) honest for Drive too. | The migration/pre-built-map/per-client-doc-ref shape doesn't fit the real code (`pkg/connectors/config.go`'s single-client-per-type fields, `resolveSecrets`'s field-name-only keying, `MockStorageClient`'s doc-ref map only ever working because tests reuse one instance). |
| 20 | 2026-08-25 | **Four `storage`/registry hardening rules.** (1) `storage` has no nil-client→mock fallback — an unconfigured or unwired provider returns `ErrValidation`, not a memory-only "success" that vanishes on restart. (2) An omitted `provider` field is `ErrValidation` too, never a default to `aws-s3` — a caller must always name a provider explicitly. (3) `send-email`/`document-extract`/`chat-notify` carry no `provider` field in the registry until each ships real per-provider code — none of the three has a real implementation behind it, so the field would offer a choice the platform couldn't fulfill; §S6.4.2/6.4.3/6.4.6's tables reflect this until real work lands with each. (4) `google-drive`'s Drive API calls set `SupportsAllDrives`/`IncludeItemsFromAllDrives` — without them, a service account frequently can't see or write a folder shared via a Shared Drive (the realistic way an org grants folder access), even when explicitly shared with it. | A provider silently degrading to an in-memory mock, or a task silently defaulting to a provider it never asked for, are both failure modes that hide real misconfiguration instead of surfacing it — unacceptable once real credentials/real data are involved, not just mocks. Advertising a provider "choice" with zero code behind it is equally misleading to whoever reads `/connectors/registry`. |
| 21 | 2026-08-25 | **Alias-registry ownership is `definition_service`'s.** `definition_service` holds `connector_rest_alias`/`connector_sql_alias` tables (no `tenant_id`, no RLS — org-owned config, not tenant data, same class as `processed_event`) and serves `GET`/`POST`/`DELETE /internal/connector-aliases`, gated by the existing `x-internal-token` middleware (no new role/auth mechanism). `execution_service`'s `cmd/connector-worker` fetches the registry via HTTP at startup; `workflow-connectors`' `aliasconfig.Config`/`Endpoint`/`Query` types are the shared shape both `restcall.go`/`sqlquery.go` resolve against. **Hot-reload is not implemented**: `pkg/connectors`' `restCall`/`sqlQuery` connectors capture `Config.Aliases` by value at `New()` time, so a periodic re-fetch in connector-worker wouldn't reach already-built connector instances without also changing `Config.Aliases`'s type in `workflow-connectors` — deliberately not done; fetch-once-at-startup is today's actual static-per-process behavior. | Per-tenant credentials (§S6.2) and now module/starter-template authoring both live in `definition_service`; the alias allowlist — which internal services are callable at all — is the same kind of platform-configuration concern, not something `execution_service`'s runtime should own or need to redeploy a binary to change. A static YAML file requiring a redeploy for a one-line alias edit was the wrong layer for org-owned, ops-managed configuration. |
| 22 | 2026-08-25 | **`send-email` is real for all 4 providers, following `storage`'s pattern (§S10 Decision #19/#20).** (1) **Microsoft Graph uses a hand-rolled `sendMail` REST call plus `oauth2/clientcredentials`, not the official `msgraph-sdk-go` SDK** — that SDK is Kiota-generated and pulls in a large transitive dependency tree for what is here a single authenticated JSON POST, the same shape `rest-call`'s own dispatch already uses. (2) **`microsoft-365`'s `tenantId`/`clientId` are plain config values, not secrets-store references** — they're identifiers, not credentials, matching the existing `storage` precedent (`projectId` plain beside `gcpServiceAccountKey`). Only `clientSecret` is a real secret. (3) `docRefStore` is constructed once in `connectors.New()` and shared across `storage` and `send-email` — a doc ref minted by `storage`'s fetch must be resolvable by `send-email`, per the documented "attachments is a list of document refs" contract. (4) No per-attachment filename field exists in the registry — a filename is synthesized from the resolved content type instead. `google-workspace` reuses Drive's existing service-account/domain-wide-delegation pattern from `storage_drive.go`; Gmail's API only accepts a raw RFC 2822 message, so a small shared `buildRawMIME` helper (stdlib `mime`/`net/textproto` only) builds it — Amazon SES v2's own `SendEmail` API, by contrast, accepts structured `Attachments` directly on its `Simple` message type, so no raw-MIME path was needed there. | Same reasoning as Decision #19/#20: don't reach for a heavy dependency or invent new mechanisms when a lean one already fits, and don't ship a documented field ("attachments") that silently doesn't work end to end. |
| 23 | 2026-08-25 | **Two `send-email` invariants: cache-key completeness, and header-injection safety.** (1) **`emailCacheKey` includes `senderEmail`** for every provider — `google-workspace`'s client binds one impersonated mailbox (`jwtConfig.Subject`) at construction time, so the cache key must vary with `senderEmail`, not just provider+credentials: the same `serviceAccountKey` sending as two different `senderEmail` values must never reuse another sender's cached client. (2) **`buildRawMIME` strips embedded CR/LF** from every value feeding a raw RFC 2822 header line (Gmail's raw-message path only) — `senderName`/`receiverName`/`subject`, where `receiverName`/`receiverEmail` are explicitly documented as typically sourced from workflow-instance data (end-user-submitted form input per §S6.4.2), so an unstripped embedded `\r\n` could otherwise inject arbitrary extra headers (e.g. `Bcc:`) into the outgoing message; `sendgrid`/`aws-ses`/`microsoft-365` build structured (JSON) requests, not raw header text, so this risk is specific to Gmail's raw-MIME path. Graph's `sendMail` URL path also `url.PathEscape`s `senderEmail` rather than concatenating it unescaped. | Both are exercised by regression tests covering the scenario the happy-path tests don't otherwise reach: two calls sharing credentials with different senders, and a name/subject value containing raw CRLF. |

## S11. Revision history

| Rev | Date | Change |
| --- | --- | --- |
| 1.0–4.2 | 2026-08-07 to 2026-08-12 | Initial design through four rounds of direction changes: worker placement proposed, reopened, and resolved twice; a credential/content callback API added, then removed entirely; `llm-verify` designed, scoped to a manual-override mechanism, then removed from the catalogue; the v1 catalogue narrowed from three connector types to seven to six; a companion document (`automatic_connector_tasks.md`) merged in and deleted. Superseded by the clean rewrite below. |
| 5.0 | 2026-08-12 | **Clean rewrite: settled design stated directly, no structural change left half-resolved.** Provider credentials for `storage`/`send-email`/`document-extract`/`chat-notify` now travel as a secrets-store reference, never a raw value (§S6.2/§S8) — closes the exposure gap in the previous raw-`context_json` design. §S6.4's per-connector retry/idempotency guidance is ratified as final policy. A new per-connector-type internal execution timeout in `cmd/connector-worker` (§S6.5) bounds a hung or failing call independently of the task's own SLA timer, resolving the former timeout/SLA/`DEGRADED` question with no interpreter, DSL, or schema change. §S9 now carries only two narrow, non-blocking operational follow-ups (credential rotation/cleanup; confirming BE-for-UI's own implementation) instead of open design questions. §S10's Decision Log and this Revision History are both trimmed to current-state-only, dropping the four-round churn narrative in favor of stating the settled design directly. Section numbers 1–11 are unchanged from rev 4.2, so every external cross-reference into this document (from `definition_service.md`, `execution_service.md`, `workflow_models_lib.md`) still resolves correctly. |
| 6.0 | 2026-08-12 | **Six gaps closed, surfaced by two independent implementation-readiness reviews (one with full project history, one deliberately blind) run before starting real implementation.** Credentials (§S6.2/§S6.4/§S8/§S10 Decision #5) concretized to AWS Secrets Manager + secret ARN, not just "a secrets-store reference." Event delivery to `cmd/connector-worker` (§S2/§S6.1/§S6.5/§S10 Decision #10) redesigned around a Valkey Stream `cmd/server`'s existing event handler pushes onto, rather than a new inbound listener — `cmd/connector-worker` now has no inbound HTTP surface at all. `document-extract` (§S6.4.3/§S10 Decision #11) scoped to real-time execution only, dropping async/polling fields that didn't fit `Connector.Execute`'s synchronous shape. `Connector.Execute`'s output (§S6.3/§S6.5/§S10 Decision #13) is now concretely `map[string]any`, with `IOMapping.Outputs` addressing only top-level keys in v1. The `endpointAlias`/`queryAlias` registry (§S6.4.4/§S6.4.5/§S9/§S10 Decision #12) is now a static config file, with BE-for-UI's alias-discovery UX flagged as a non-blocking follow-up. "Document ref" (§S6.2/§S10 Decision #14), used five times with no definition, is now a minimal opaque string issued by `storage` and consumed as-is elsewhere. **The largest single change**: connector tasks are now fully automation-only (§S2/§S4.2/§S5.3/§S6.5/§S10 Decision #9) — neither an unregistered connector type nor an exhausted/failed call falls back to a human anymore; both route through a new interpreter signal resolving to the existing `FAILED`/`DEGRADED` machinery. This is genuinely new interpreter surface (verified directly against `execution_service.md`: no signal for "this task failed" exists today — `FAILED`/`DEGRADED` have only ever originated from an Activity error inside the workflow function itself), not a documentation change, and is called out as the highest-risk item for implementation. Section numbers 1–11 stay unchanged from rev 5.0. |
| 7.0 | 2026-08-13 | **BE-for-UI/build-vs-absorb architecture review.** Connector-authoring templates (§S1/§S2/§S4.3/§S6.1/§S6.3) and credential custody (§S6.2/§S8/§S10 Decision #5, new Decision #15) reassigned from BE-for-UI to `definition_service` throughout this document — fixed, code-generated catalogue metadata with no tenant-stored content, a better fit for the service that already owns design-time authoring/compilation and already imports `pkg/registry`. BE-for-UI's own-implementation follow-up (§S9) removed as moot — `definition_service` is a real, existing service, not a hypothetical one. `definition_service` inherits the alias-discovery-UX follow-up (§S9) in its place. Secrets mechanism switched platform-wide from AWS Secrets Manager to **OpenBao** (§S2, §S6.2, §S6.3, §S6.5, §S8, §S10 Decision #5) — "secret ARN" replaced by "OpenBao secret path" (KV v2) everywhere; `secretsmanager:GetSecretValue` replaced by OpenBao's KV-v2 read API. No change to §S3–§S5, §S7, or the catalogue itself (§S6.4) — this pass only moves who owns the authoring/credential surface and what secrets backend it targets, not the connector execution model. |
| 7.1 | 2026-08-13 | Implementation begun: `workflow-models` (`StageTypeConnector`/`StageDef.ConnectorType`/`IOMapping`), `definition_service` (the `connector:` compiler exception, `/connectors/registry`, `/connectors/credentials`), and a new `workflow-connectors` module (`pkg/registry`, `pkg/connectors` stubs) are built and tested. Confirmed directly with the user: the authoring surface is a customized, restricted `bpmn.js` instance, not Camunda Desktop/Web Modeler — Camunda's own element-template JSON format (`$schema`/`appliesTo`/`properties[].binding`) doesn't apply here, and no such artifact is being built. Added the new Appendix below (a plain XML/API reference for whoever builds the `bpmn.js` palette entry and properties panel) in its place — a distillation of already-settled facts, not a new decision. |
| 7.2 | 2026-08-14 | **Two implementation-review passes (2 normal + 2 blind) found this table itself internally ambiguous in three places, closed here.** §S6.4.6's `visibility` never stated its own enum values — now `public`/`private`, split onto its own row rather than sharing a "string / enum" row with `channelName`. §S6.4.3's `signaturesDetected` ("bool / list") and §S6.4.5's `params` ("list / map") each named two candidate types with no resolution — both settled to `list`, matching what `pkg/registry` already implemented (the code's choice was reasonable; the table just never confirmed it). §S6.4.3's `confidence` output reworded from "float" to "map" (keyed by field name) to match its own "per-field confidence" language — a single scalar can't carry one score per extracted field. No change to the connector catalogue's actual scope or any other section. |
| 7.3 | 2026-08-17 | **Two contradictions found and fixed during real implementation of `cmd/connector-worker` and `workflow-connectors`' six real `Connector.Execute()` bodies, both now built.** §S5.5/§S10 Decision #1 corrected: completion goes through a **new** `execution_service` endpoint (`POST /internal/connector-tasks/:id/{complete,fail}`), not literally "the same endpoint a human uses" — `TaskService.checkHumanActionable` had already made that literal statement false (§S10 Decision #16). §S6.4.5 clarified: `sql-query` is an internal HTTP proxy to the owning service's own query-execution endpoint, never a direct database connection — the ambiguity between the two readings is resolved in code and here (§S10 Decision #17). §S6.5 step 3 also corrected on which side applies `IOMapping.Outputs`' rename: `execution_service`'s own new endpoint, not `cmd/connector-worker`, since that's where the mapping is already persisted at task-creation time. No change to the catalogue's scope, the six connector types, or any other decision in this document. |
| 8.0 | 2026-08-24 | **Real-provider gap found and designed: `storage`/`send-email`/`document-extract`/`chat-notify` had zero real SDK integration in `cmd/connector-worker` — every one ran against its in-memory mock in production, undocumented anywhere before this pass.** All four become multi-provider (§S6.4.1/6.4.2/6.4.3/6.4.6, §S10 Decision #18) rather than committing to one vendor per type: `storage` (S3/Azure Blob/GCS/Google Drive), `send-email` (SendGrid/SES/Microsoft 365/Google Workspace), `document-extract` (Textract/Azure AI Document Intelligence/GCP Document AI/ABBYY Vantage — the last three confirmed against Camunda's own Intelligent Document Processing feature, not assumed), `chat-notify` (Slack/Teams). §S6.2 updated to note `provider` is an ordinary field, never itself a secret. Design only — no SDK code written this pass; `rest-call`/`sql-query` untouched. |
| 8.1 | 2026-08-25 | **§S9's flat provider-implementation follow-up staged into 7 ordered phases**, found during a status-board review pass to need concrete sequencing before anyone could start it. Surfaced two real gotchas the flat list hid: the OpenBao credential path is a hardcoded 3-segment format duplicated in lockstep across `execution_service` and `definition_service` since a 2026-08-14 path-traversal fix, so adding `provider` as a 4th segment is a breaking two-repo change, not a `deps.go` wiring detail; and Decision #6's per-type "storage is safe to retry" policy doesn't hold for `google-drive`'s opaque-`fileId`-keyed creates once storage has multiple providers, flagged for revisiting when that phase starts. No change to the catalogue's scope or any other decision. |
| 8.2 | 2026-08-25 | **`storage` implemented real for all 4 providers (§S10 Decision #19), same day as rev 8.1's staging** — three corrections to that staging's literal wording found doing the work: no OpenBao path migration (collision-free per-provider field names instead, §S6.4.1's table updated); `Config.StorageProviders` is a per-call constructor map with a small credential-hash cache, not a map of pre-built clients (per-tenant secrets can't be baked in at `cmd/connector-worker` startup); doc-ref handling moved to a store owned by the connector itself, not the per-call client. `send-email`/`document-extract`/`chat-notify` are unaffected, still mock — §S9 updated to reflect `storage`'s completion and drop the now-superseded migration/pre-built-map framing for the remaining 3 types' own follow-on work. |
| 8.3 | 2026-08-25 | **Review of rev 8.2's own implementation found four more corrections (§S10 Decision #20), same day.** `storage`'s nil-client→mock fallback and the omitted-`provider`-defaults-to-`aws-s3` behavior are both removed — an unconfigured provider or a missing `provider` field are now `ErrValidation`, never a silent, memory-only "success." `send-email`/`document-extract`/`chat-notify`'s `provider` field (added in the original phase-1 pass, before any of the three had real code) is removed until each ships its actual implementation — §S6.4/§S6.4.2/§S6.4.3/§S6.4.6 updated to say so directly rather than describe a schema that isn't there. `google-drive`'s Drive calls now request Shared Drive access explicitly, since a service account otherwise can't reliably read/write a folder shared via a Shared Drive. |
| 8.4 | 2026-08-25 | **Alias-registry ownership moves to `definition_service` (§S10 Decision #21), same day.** §S9's alias-discovery-UX follow-up updated to reflect the new `GET /internal/connector-aliases` endpoint (still no UI consumer); two new follow-ups added and explicitly flagged, not fixed: `rest-call`'s alias `BaseURL` has no internal-host enforcement despite being documented as internal-only, and `sql-query`'s data-access model needs a fuller review before more is built on it. No change to §S6.3's `Connector`/`*ProviderClient` interfaces or §S6.4's catalogue scope. |
| 8.5 | 2026-08-25 | **`send-email` implemented real for all 4 providers (§S10 Decision #22), same day.** §S6.4.2's table drops its "design only" notes and corrects the `microsoft-365` credential table (`clientSecret` is the only secrets-store reference; `tenantId`/`clientId` are plain config values). §S9's provider-implementation follow-up updated: 8 of 14 provider clients now real (`storage` + `send-email`), only `document-extract`/`chat-notify` remain. A real cross-connector bug found and fixed along the way: `docRefStore` is now shared across `storage`/`send-email` via `connectors.New()` instead of being constructed fresh per connector, so a `storage`-minted doc ref is actually resolvable as an email attachment. |
| 8.6 | 2026-08-25 | **Post-shipment review of rev 8.5's own `send-email` code found and fixed two real bugs (§S10 Decision #23), same day.** A cache-key gap that could silently send email impersonating the wrong `google-workspace` mailbox once two different senders shared one service account; and an email header-injection gap in Gmail's raw-MIME builder (`senderName`/`receiverName`/`subject` reaching raw RFC 2822 header lines with no CR/LF stripping — `receiverName` is documented as typically workflow-instance/end-user data). Both fixed, both covered by regression tests, the cache one confirmed to fail pre-fix. No change to §S6.4's catalogue or interfaces. |
| 8.7 | 2026-09-09 | **Credential rotation and cleanup (§S9's own tracked gap) closed for the revocation half — rotation itself needed no new mechanism.** §S6.2 now documents both directly: rotation is re-submitting the authoring form onto the same OpenBao path; revocation is a new admin-gated `DELETE /api/v1/connectors/credentials/:connector_type/:field_name`, backed by a real KV-v2 metadata-destroy on `port.SecretsClient`. §S9's bullet for this removed now that it's settled design, not an open item. |
| 8.8 | 2026-09-10 | The 9 dotted-lowercase `workflow.task.*` citations renamed to PascalCase, following `execution_service.md` rev 1.45's reversal of Appendix A.4 decision 11. Documentation-only; no connector contract, alias, or auth detail changes. |
| 8.9 | 2026-09-15 | **`rest-call`'s alias `BaseURL` gap closed; §S9's remaining credential gap narrowed to orphan cleanup and TTL.** The `BaseURL` bullet was a known, deliberately-deferred hole: `restcall.Execute` attaches the caller's real `x-internal-token`/`x-departments` headers to whatever host an alias names, and the alias registry is org-wide with no RLS, so an outward-pointing alias handed that host a valid internal credential. `definition_service` now enforces an internal-host allowlist at alias-write time (`CONNECTOR_ALIAS_ALLOWED_HOSTS`, exact hosts or a leading-dot suffix rule), rejects non-http(s) schemes and embedded userinfo, and fails closed on an empty allowlist — enforcement sits on the write path rather than in `restcall.go` because the allowlist is deployment configuration and this library takes none (`definition_service.md` §10.15). This had to land before any real alias row is seeded. The credential follow-up is re-stated at its true scope: rotation needs no mechanism and revocation shipped in rev 8.7, so what remains is that nothing sweeps a secret whose owning connector task or workflow version is gone, and that no TTL exists — neither of which has anything in Postgres to drive a sweep from. |
| 8.10 | 2026-09-24 | **The worker reads only the task's own credential path (§S6.2, §S8).** A secret-reference value is the only path an instance can influence, and the worker's token spans every tenant, so a task in one tenant could name another tenant's credential and have it read. The worker now requires exactly `connectors/<task tenant>/<connector type>/<field>`, checks every reference before reading any, and fails the task with `secret_ref_invalid` otherwise. |
| 8.11 | 2026-09-24 | **A task's connector runs once, however often its entry is delivered (§S6.5 step 0).** A failed callback left the entry unacked, and every redelivery re-ran the connector, roughly every 30 seconds with no limit. An entry still queued or running looked idle and was reclaimed, and shutdown cancelled a run mid-flight so that it reported nothing. The worker now resolves the actor first, records each outcome and re-sends it on redelivery, holds an execution lease per task, touches the entries it is still handling so only a dead consumer's are reclaimed, lets a started run finish across shutdown, and dead-letters an entry past its delivery cap. |
| 8.12 | 2026-10-02 | **No call-pool step** (§S3). A connector stage's input/output mapping is described by the call step's own mapping; the call-pool step is gone from the plan (workflow-models LLD rev 2.15). |
| 8.13 | 2026-10-07 | **A connector always uses the tenant's stored credential; a diagram never names one (§S6.2, §S8, the authoring contract).** The worker had required an authored secret-reference to equal the task's own credential path, which made the authored value a selector with nothing to choose. It now reads each secret field the registry declares for the connector type from `connectors/<task tenant>/<connector type>/<field>`, discards any value a plan still carries for such a field, leaves out a field with no stored credential for the connector to refuse by its own validation, and fails the task on any other read failure; `secret_ref_invalid` is no longer raised for a path a task names. Definition refuses a connector input whose target is a secret field (`CONNECTOR_SECRET_AUTHORED`). An input's `source` is `=name` for an instance variable or any other text for a literal, and an output's `source` names a key of the connector's result with or without `=` (workflow-models LLD rev 2.16). |
| 8.14 | 2026-10-08 | **The worker no longer resolves an actor (§S6.5 step 0).** Execution names the tenant's automation principal itself, from the identity subsystem, when it puts the task on the stream and again on each callback, so the callbacks carry no actor and the worker's per-tenant actor map is gone. A tenant with no principal is refused before the entry reaches the stream, so the connector never runs for it (execution LLD rev 1.70). |

## S-Appendix — BPMN Authoring Reference (for UI Implementers)

Plain reference for whoever builds the connector palette entry and properties panel in the platform's customized `bpmn.js` instance. **Not** Camunda Desktop/Web Modeler — Camunda's element-template JSON format doesn't apply here (§S10 Decision log, rev 7.1 row). Nothing below is a new decision; every fact is already settled elsewhere in this document or already live in code.

### The XML contract

A connector-typed stage is a plain `<bpmn:serviceTask>` carrying two `<bpmn:extensionElements>` children — this is the exact shape `definition_service`'s compiler parses (`internal/bpmn_compiler/parser.go`/`bpmncore/compile.go`) and its own tests assert. Don't improvise a different shape.

```xml
<bpmn:serviceTask id="Activity_1" name="Send confirmation email">
  <bpmn:extensionElements>
    <zeebe:taskDefinition type="connector:send-email"/>
    <zeebe:ioMapping>
      <zeebe:input source="sendgrid" target="provider"/>
      <zeebe:input source="no-reply@example.com" target="senderEmail"/>
      <zeebe:input source="=applicantEmail" target="receiverEmail"/>
      <zeebe:input source="Your application was received" target="subject"/>
      <zeebe:input source="text/plain" target="contentType"/>
      <zeebe:input source="Thanks for applying." target="body"/>
      <zeebe:output source="=sent" target="email_sent"/>
      <zeebe:output source="=messageId" target="email_message_id"/>
    </zeebe:ioMapping>
  </bpmn:extensionElements>
</bpmn:serviceTask>
```

- `zeebe:taskDefinition`'s `type` attribute is always `connector:<name>`, where `<name>` is one of `pkg/registry`'s type constants: `storage`, `send-email`, `document-extract`, `rest-call`, `sql-query`, `chat-notify`.
- Every ordinary input/output field a connector type declares (§S6.4) becomes one `<zeebe:input>`/`<zeebe:output>` element. `target` is always the field's own name (`apiKey`, `bucket`, `operation`, ...), exactly as named in `pkg/registry`. `source` is whatever the author entered — a literal value, or `=<variableName>` to reference an instance variable — except for credential fields (see below), which have no input element at all. An output's `source` names a key of the connector's result, with or without the leading `=`.

### Where the field list comes from

Don't hand-copy §S6.4's tables into the UI. Call `GET /connectors/registry` (served live by `definition_service`, generated from the same `pkg/registry` Go package the compiler validates against) and render the returned field list directly. This can never drift out of sync the way a hand-maintained copy would.

### The one rule that isn't optional: credential fields

Four connector types have a `secret_ref`-kind field: `storage.accessKey`/`.secretKey`, `send-email.apiKey`, `document-extract.accessKey`/`.secretKey`, `chat-notify.authToken`. A credential is never part of the diagram:

1. An administrator stores it once per tenant, connector type and field: `POST /connectors/credentials` with `{"connector_type", "field_name", "value"}`.
2. The BPMN has **no** `<zeebe:input>` for a credential field. A diagram that has one is refused at publish (`CONNECTOR_SECRET_AUTHORED`), whatever its `source`.

The worker reads the stored credential itself when the task runs (§S6.2, §S8), so a credential never reaches the BPMN XML, `context_json`, Temporal's workflow history or the `WorkflowTaskCreated` event payload.

The worked example above shows this: `send-email` has no `apiKey` input, and everything else is a literal or a `=variable` reference, written directly.
