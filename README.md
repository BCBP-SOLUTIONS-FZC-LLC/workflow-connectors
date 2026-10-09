# workflow-connectors

The shared connector library for the workflow engine — the **fixed catalogue of automatic BPMN service tasks** (`connector:`-prefixed) and their implementations. It owns the catalogue metadata the authoring side compiles against (`pkg/registry`) and the `Execute` implementations the Execution Service's connector worker runs (`pkg/connectors`): `storage` and `send-email` with real provider adapters, `rest-call` to internal platform services, and `chat-notify`. It has no process of its own — it runs inside `execution_service`'s `cmd/connector-worker`, which owns delivery, retries, credentials and completion.

**Repository:** `github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2`
**Module:** Go 1.26.9 · private module · a library: no server, no binary, no image, no database of its own (the Drive document registry and send intents run on the worker's PostgreSQL), and no logging, metrics, tracing or event code
**Design:** one LLD, `docs/lld/workflow-connectors-library-lld.md` (**rev 1.13**). Part I (§1–§21) is the module design; Part II (§S1–§S11) is the *Automatic Connector Tasks & Connector Workers* system design (rev 8.14) across `workflow-models`, `definition_service`, `execution_service` and this module. Where they disagree, Part II is authoritative. "LLD §S…" and "Decision #N" in this README cite Part II; "module LLD §…" cites Part I.

---

## Mental model

| This library owns | It does NOT own |
|---|---|
| The `Connector` contract — `Type()` + `Execute(ctx, input) (map[string]any, error)` — and `connectors.New(Config)` | The connector worker: Valkey Stream consumption, dedup, leases, pools, timeouts, retries (`execution_service`) |
| `pkg/registry` — `Type*`, `FieldKind*`, `RetryPolicy`, `Definition`/`Field`, `All()`; the source of truth for authoring templates | Completion/failure callbacks and `IOMapping.Outputs` renaming (`execution_service`) |
| Connector cores — validation, output shape, the `ProviderClient` port, mocks (`storage`, `sendemail`, `chatnotify`, `restcall`, `sqlquery`, `documentextract`) | Reading credentials from OpenBao — values arrive already resolved (`execution_service` worker) |
| Provider adapters — `storage/{gocloud,googledrive}`, `sendemail/{ses,sendgrid,msgraph,gmail}`; the only packages that import a cloud SDK | Writing, rotating and revoking credentials (`definition_service`) |
| `aliasconfig` — the alias schema `rest-call`/`sql-query` resolve against, its loader and validation | The alias registry's storage, CRUD API and internal-host allowlist (`definition_service`) |
| `docref` — document refs shared by `storage` and `send-email`: content in the platform's S3 document bucket (`docref/s3content`), reference metadata in Valkey (`docref/valkeystore`), verified on every resolution | BPMN compilation and the `connector:` task rule (`definition_service`); the DSL shape (`workflow-models`) |
| The internal-call header contract (`x-internal-token`, `x-departments`) | Logging, metrics, tracing — the worker's |

The worker is the composition root: it maps provider names to adapter constructors and passes them in `Config`. `pkg/connectors` never imports an adapter, so a consumer that imports it alone compiles no cloud SDK.

---

## Why this library exists

A `connector:` service task runs with no human and completes through the same workflow path a person's task does. Two services need the catalogue for different reasons:

- **Definition Service** needs only the type list and field shapes — to reject an unknown connector type at compile time and to generate authoring templates. It must not pull in the AWS, Azure, Google and SendGrid SDKs to do that.
- **Execution Service's connector worker** needs the implementations and their SDKs.

Keeping both in one module means the catalogue and the code that implements it can never drift: `registry.All()` lists exactly the types `connectors.New` builds (pinned by tests). Splitting each multi-provider connector into an SDK-free core and per-provider adapters means a new provider is one new package, and the core's behaviour — validation, caching, document refs, error classes — is shared by every provider. `.go-arch-lint.yml` enforces that split, including which third-party modules each package may import.

---

## API overview

The public surface is Go, not HTTP. Source of truth: `pkg/connectors/connector.go`, `config.go` and each connector package; full contract in the module LLD §5.

```go
func New(cfg Config) (map[string]Connector, error)   // fails on empty InternalToken; panics on duplicate Type

type Config struct {
    Aliases            aliasconfig.Config
    HTTPClient         *http.Client                              // copied, never follows redirects, HTTP/1.1 only; nil → default transport, each call bounded by its alias timeout (else 30 s)
    InternalToken      string                                    // required
    StorageProviders   map[string]storage.ProviderConstructor
    SendEmailProviders map[string]sendemail.ProviderConstructor
    ChatNotifyClient   chatnotify.ProviderClient                 // nil → every chat-notify call fails (ErrValidation)
    SendIntents        sendintent.Store                          // nil → a send-email messageKey is ErrValidation
    DocRefs            *docref.Service                           // nil → document refs disabled (ErrValidation)
}

var ErrValidation, ErrUpstream, ErrMissingInternalAuth, ErrMissingTenant, ErrNotDelivered, ErrDeliveryUnknown error
func WithDepartments(ctx context.Context, departments []string) context.Context
func WithTenant(ctx context.Context, tenantID string) context.Context
func ClassOf(err error) (ErrorClass, string)                    // also IsTransient, IsPermanent
func DecideRetry(connectorType string, err error, method string) RetryDecision
```

### Connectors built by `New` (4)

| Type | Operation | Providers | Retry |
|---|---|---|---|
| `storage` | `fetch` / `upload` / `delete` a document | `aws-s3`, `azure-blob`, `gcp-gcs` (`storage/gocloud`), `google-drive` (`storage/googledrive`) | `safe` (every transient failure) |
| `send-email` | Send an email, with document-ref attachments | `sendgrid`, `aws-ses`, `microsoft-365` (`sendemail/msgraph`), `google-workspace` (`sendemail/gmail`) | `not-delivered` — retried automatically only for a transient failure the provider provably never accepted; not idempotent (see Email delivery semantics) |
| `rest-call` | HTTP call to an internal platform endpoint, by alias | none — internal services only | `conditional` (transient failures of idempotent methods only) |
| `chat-notify` | `create-channel` / `invite-to-channel` / `post-message` | none yet — set `Config.ChatNotifyClient`; without one every call fails with `ErrValidation` | `unsafe` |

### Implemented but not wired (2)

| Type | Package | Why not wired |
|---|---|---|
| `sql-query` | `pkg/connectors/sqlquery` | Data-access model under review before more is built on it (LLD §S9) |
| `document-extract` | `pkg/connectors/documentextract` | No real provider yet; its `provider` field ships with real code (LLD §S10 Decision #20) |

Neither is listed in `registry.All()`. Per-field input/output tables are in `pkg/registry/definitions.go` and the module LLD §5.4.

---

## Input validation

Every connector validates its required fields before building a provider client or making any call. Errors wrap one of these sentinels, re-exported from `pkg/connectors`, so callers branch with `errors.Is`:

| Sentinel | Raised when | Class |
|---|---|---|
| `ErrValidation` | Missing or invalid required field; unknown `operation`/`method`/alias; omitted or unconfigured `provider`; missing or invalid credential; malformed email address; unresolvable document ref (not found, source missing, integrity violation); payload over a size limit; path-template error | Permanent |
| `ErrNotDelivered` | `send-email` only: the provider definitively did not accept the message (4xx, or the request never left) | Transient (429, 408, DNS, connection failure) or permanent (other 4xx) |
| `ErrDeliveryUnknown` | `send-email` only: the provider **may** have accepted it (timeout, reset, 5xx, crash) | Unknown: never retried automatically; a resend may deliver a duplicate (see `docs/runbooks/email-delivery.md`) |
| `ErrUpstream` | Provider SDK/API error (never for `send-email`); network or timeout error; `>= 300` status from an internal service; undecodable response | Transient, permanent or unknown, by provider mapping |
| `ErrMissingInternalAuth`, `ErrMissingTenant` | Called without `connectors.WithDepartments` / `WithTenant`, or with no departments, or a department that is empty or contains `,`, CR or LF | Permanent: a worker wiring bug |

Every error wraps exactly one of these sentinels **and** has exactly one class: **transient** (may recover; retried if the type's policy allows), **permanent** (needs a fix; never retried) or **unknown** (unrecognised; never retried automatically). Read the class only with `connectors.ClassOf(err)`, `IsTransient`, `IsPermanent` or `DecideRetry`; there is no `errors.Is` sentinel per class (an error can wrap a transient cause yet be permanent, e.g. invalid input). Each adapter maps its provider's errors: S3 error codes (`NoSuchKey` permanent, `SlowDown` transient, …), gocloud codes for Azure and GCS, Drive reasons (rate-limit 403s are transient), HTTP statuses (408/429/500/502/503/504 transient, other 4xx permanent) and network failures. The full tables are in [`docs/runbooks/retry-semantics.md`](docs/runbooks/retry-semantics.md). The original provider or network error stays in the chain for `errors.As`. An unknown alias additionally matches `aliasconfig.ErrUnknownAlias`.

### Notable validation rules

| Rule | Enforcement |
|---|---|
| `provider` is required, with no default and no mock fallback | `clientFor` in the `storage`/`sendemail` cores → `ErrValidation` (LLD §S10 Decision #20) |
| `microsoft-365`/`google-workspace` require `body` | They have no server-side templates, so `templateId` cannot stand in |
| Every `attachments` entry must resolve | Resolved through `Config.DocRefs` (same tenant, unexpired, size and SHA-256 verified) before the client is built |
| `rest-call` path parameters cannot alter the path | Each `{name}` value in the path is `url.PathEscape`d and must not be empty, `.` or `..`; one in the template's query string is `url.QueryEscape`d; a missing name is `ErrValidation` |
| `rest-call`/`sql-query` never leave the alias's host | `pathTemplate`/`path` must start with `/` (`aliasconfig.Validate`), and every request URL must keep the `baseURL`'s scheme and host with no userinfo (`shared.NewInternalRequest`) — `ErrValidation` otherwise |
| `queryParams` cannot override the alias | A key the `pathTemplate`'s query string already sets is `ErrValidation` |
| `rest-call`/`sql-query` never send without caller identity | Departments required in the context; `InternalToken` required at `New` |
| `sql-query` parameter count matches the alias | `len(params) == paramCount` when `paramCount > 0` |
| Email addresses are single, bare addresses | `net/mail.ParseAddress` must yield exactly the input, with no display name — no comma-separated extra recipients |
| Payload sizes are bounded | Objects 50 MiB, inline fetch 1 MiB (set `createDocument` above that), internal responses 10 MiB, attachments 25 MiB total (raw bytes; lower per provider: SendGrid 20 MiB, Microsoft 365 3 MiB — inline `sendMail`; SES and Gmail 25 MiB) |
| Inline binary content survives JSON | A fetched object that is not valid UTF-8 is returned base64 with `contentEncoding: "base64"`; upload accepts the same |
| Alias configuration is well-formed | `aliasconfig.Validate`: unique aliases, valid method, `baseURL` an `http`/`https` URL with a host and no userinfo, `pathTemplate`/`path`/`queryId` present, `paramCount >= 0` |

---

### Email delivery semantics

**`send-email` is not idempotent, and exactly-once delivery is not provided.** No supported provider (SendGrid, SES, Graph, Gmail) accepts a caller-supplied idempotency key, and a lost success response cannot be told apart from a send that never arrived.

| | Guarantee |
|---|---|
| Not retried | **At-most-once** |
| Retried after an uncertain outcome | **Effectively at-least-once** — the email may be delivered twice |

- **Retried automatically only when it cannot duplicate.** Retry policy `not-delivered`: `connectors.DecideRetry` allows a retry only for a transient failure the provider provably never accepted (`ErrNotDelivered`: DNS, connection refused or timed out, 429). Permanent rejections (invalid recipient) and every `unknown` outcome are never retried automatically. Every failure is `ErrNotDelivered` or `ErrDeliveryUnknown`, never `ErrUpstream`; the SES client is built with the AWS SDK's own retries disabled (the default resends `SendEmail` up to 3 times), and SendGrid, Gmail and Graph are called once per send.
- **Ambiguous outcomes are visible.** The output carries `deliveryOutcome` (`accepted`, `not_delivered`, `unknown`) — also on failure — and `errors.As(err, *sendemail.SendError)` gives provider and HTTP status.
- **Resending is explicit.** Optional `messageKey` + a send-intent store reject a duplicate *request* before anything is sent; `resend: true` with `resendAttempt: <n>` (the intent's current `attempts`, shown with the duplicate) is the only opt-in to send that key again — a compare-and-swap on that attempt, so a redelivered resend task sends nothing. A duplicate of an `accepted` intent succeeds without sending (`duplicate: true`), so a job redelivered after a crash before its acknowledgement is idempotent. This is duplicate-request protection, **not idempotency** of the send itself: a resend after `unknown` may duplicate the email.

Operator guidance: [`docs/runbooks/email-delivery.md`](docs/runbooks/email-delivery.md).

## Architecture

Connector cores surrounded by provider adapters; dependencies point inward. Layer diagram, package dependency graph, execution and internal-auth flows, client cache, document refs, failure domains and threat model are in **[`ARCHITECTURE.md`](ARCHITECTURE.md)**; standalone Mermaid diagrams live in **[`docs/architecture/`](docs/architecture/README.md)**.

```text
pkg/registry                 ← connector catalogue (types, fields, retry policies), imported by definition_service
                               and pkg/connectors (standard library only)
pkg/connectors               ← facade: Config, New, DecideRetry, error re-exports — imported by execution_service's
  │                            cmd/connector-worker
  ├─ aliasconfig             ← alias schema, loader + Validate, resolvers
  ├─ shared                  ← errors and classes (ClassOf), delivery outcomes, field helpers, internal requests and
  │                            header names, tenant/departments context, HTTP clients and size limits, Google key
  │                            checks, ClientCache (standard library only)
  ├─ docref                  ← document refs: Service (Create · Lookup · Open · Read · Delete), Store + ContentStore ports, memory stores (tests)
  │  ├─ s3content            ← S3 content store (system of record for document content)
  │  └─ valkeystore          ← Valkey metadata store: create-only, idempotent writes, WAITAOF, CheckDurability
  ├─ sendintent              ← send-email duplicate-request protection: Store port, in-memory store
  │  └─ sqlstore             ← PostgreSQL store on platform-pgcommon + migrations
  ├─ documents               ← document registry port, state machine, in-memory store
  │  └─ sqlstore             ← PostgreSQL store on platform-pgcommon + migrations (worker supplies *pgcommon.Pool)
  ├─ storage · sendemail · chatnotify · documentextract
  │                          ← connector cores: validation, ProviderClient port, mock client for tests (no SDKs)
  ├─ restcall · sqlquery     ← connector cores for internal services: alias-resolved HTTP, no provider port
  │                            (sqlquery and documentextract are implemented but not wired into New — LLD OQ-7)
  ├─ storage/gocloud         ← aws-s3 · azure-blob · gcp-gcs through gocloud.dev (+ AWS and Azure SDKs, oauth2)
  ├─ storage/googledrive     ← Google Drive API (+ the documents registry)
  └─ sendemail/ses · sendemail/sendgrid · sendemail/msgraph · sendemail/gmail
                             ← provider adapters: SES SDK · own HTTP POST (sendgrid-go mail helpers only) ·
                               own HTTP POST with oauth2 (no Graph SDK) · Gmail API
```

### Dependency rules (enforced by `go-arch-lint` in CI)

| Component | May depend on | May import (vendors) |
|---|---|---|
| `registry` (`pkg/registry`) | Nothing internal | Nothing — standard library only |
| `shared` | Nothing internal | Nothing — standard library only |
| `aliasconfig` | Nothing internal | `gopkg.in/yaml.v3` |
| `connector_core` (the six connector packages) | `registry`, `shared`, `aliasconfig`, `send_intents`, `docref` | `github.com/google/uuid` |
| `documents` | `shared` | `github.com/google/uuid` |
| `send_intents` (`sendintent`) | `shared` | `github.com/google/uuid` |
| `docref` | Nothing internal | `github.com/google/uuid` |
| `docref_valkeystore` (`docref/valkeystore`) | `docref` | `github.com/redis/go-redis/v9` |
| `docref_s3content` (`docref/s3content`) | `docref` | `aws-sdk-go-v2` (`service/s3`) |
| `document_sqlstore` (`documents/sqlstore`) | `documents`, `shared` | `platform-pgcommon/v2` only — never `pgx`/`database/sql` directly (pgcommon's `Conn`, `Row`, `Tx`, `TxOptions`, `ErrNoRows`); with `send_intents_sqlstore`, the only components allowed a database library |
| `send_intents_sqlstore` (`sendintent/sqlstore`) | `send_intents`, `shared` | `platform-pgcommon/v2` only |
| `provider_adapters` (the six adapter packages) | `connector_core`, `documents`, `shared` | `uuid`, AWS SDK, Azure SDK, `gocloud.dev`, `google.golang.org/api`, `golang.org/x/oauth2`, SendGrid |
| `connectors` (facade) | `connector_core`, `shared`, `aliasconfig`, `registry`, `send_intents`, `docref` | — (never an adapter or a store implementation) |

`depOnAnyVendor: false` — every third-party import is checked per component, so a cloud SDK cannot creep back into a core. `_test.go` files are excluded.

### In-process state

| Concern | Mechanism | Notes |
|---|---|---|
| **Persistent store** | None of its own | The Drive document registry and send intents live in the **worker's** PostgreSQL: `documents/sqlstore` and `sendintent/sqlstore` run on the `*pgcommon.Pool` the worker passes in and ship their migrations, applied with each package's `ApplySchema` and tracked in their own `connector_documents_migrations` / `connector_send_intents_migrations` tables. Document refs live in S3 and Valkey (below) |
| **Send intents** (optional) | `sendintent.Store` (PostgreSQL in production) | One row per tenant + `messageKey`, unique by constraint; rejects a duplicate send-email request before sending. Not idempotency |
| **Drive document registry** | `documents.Store` (PostgreSQL in production) | One row per tenant + provider + folder + filename, unique by constraint; the uniqueness authority for Drive uploads (see Security) |
| **Provider-client cache** | `shared.ClientCache` per `storage`/`send-email` connector, reference-counted | Keyed by SHA-256 of provider + bucket/sender + every credential field; 256 entries. A call holds a handle for its duration; overflow or `ResetClients()` retires entries, and a retired client is closed (`io.Closer`) once its last call finishes — never while in use |
| **Document refs** | `docref.Service` — **S3 holds the content** (`docref/s3content`, the platform's document bucket, worker credentials); **Valkey holds only the reference** (`docref/valkeystore`) | Hash `docref:{uuid}` → `reference_id`, `tenant_id`, `bucket`, `object_key`, `content_type`, `size`, `sha256`, `created_at`, `updated_at` — a few hundred bytes, no content. Every resolution checks the tenant, fetches from S3 and verifies size + SHA-256 (`ErrNotFound`, `ErrSourceMissing`, `ErrIntegrityViolation`). Streams large documents (`Open`). 24 h TTL. Operations: [`docs/runbooks/document-refs.md`](docs/runbooks/document-refs.md) |
| **Alias config** | `aliasconfig.Config`, captured by value at `New` | Fetched by the worker at startup; no hot reload (LLD §S10 Decision #21) |
| **Events** | None | No publishing, consuming or schema registry |

### Third-party dependencies

| Library | Version | Used by |
|---|---|---|
| `aws-sdk-go-v2` (`credentials`, `service/s3`, `service/sesv2`) | v1.19.37 / v1.102.2 / v1.67.0 | `storage/gocloud`, `docref/s3content`, `sendemail/ses` |
| `azure-sdk-for-go/sdk/storage/azblob` | v1.8.0 | `storage/gocloud` |
| `gocloud.dev` | v0.46.0 | `storage/gocloud` |
| `google.golang.org/api` | v0.293.0 | `storage/googledrive`, `sendemail/gmail` |
| `golang.org/x/oauth2` | v0.37.0 | `storage/gocloud`, `storage/googledrive`, `sendemail/msgraph`, `sendemail/gmail` |
| `sendgrid-go` | v3.16.1 | `sendemail/sendgrid` (the `mail` request types only; requests are sent by the adapter's own client) |
| `github.com/redis/go-redis/v9` | v9.22.0 | `docref/valkeystore` (the platform's Valkey client, same version as org-membership) |
| `github.com/google/uuid` | v1.6.0 | `docref` (ref IDs), `documents`, `sendintent`, `sendemail`, `chatnotify` (mock IDs), `storage/googledrive` |
| `gopkg.in/yaml.v3` | v3.0.1 | `aliasconfig` |
| `stretchr/testify` | v1.12.1 | Tests only |

No OpenBao client or Temporal SDK; the Valkey client is used only by `docref/valkeystore`. The one `platform-*` module is `platform-pgcommon/v2` (v2.0.1), used only by `documents/sqlstore` and `sendintent/sqlstore` — all database access goes through it, as on every other service. Microsoft Graph uses a hand-rolled `sendMail` POST rather than `msgraph-sdk-go` (LLD §S10 Decision #22).

---

## Integrating with other services

### 1. Prerequisites

`GOPRIVATE=github.com/BCBP-SOLUTIONS-FZC-LLC/*` and access to the private module. Pin a tag; never add a `replace` directive pointing at a filesystem path.

```bash
go env -w GOPRIVATE=github.com/BCBP-SOLUTIONS-FZC-LLC/*
go get github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2@vX.Y.Z
go mod tidy
go mod vendor   # if the consuming service vendors dependencies
```

### 2. Definition Service — compile side

Import `pkg/registry` only.

```go
defs := registry.All()
def, known := defs[registry.TypeStorage]     // !known → UNKNOWN_CONNECTOR_TYPE
for _, f := range def.Inputs {
    if f.IsSecretRef() { /* never authorable — CONNECTOR_SECRET_AUTHORED */ }
}
```

### 3. Connector worker — run side

**Step-by-step wiring guide: [`docs/integration/connector-worker.md`](docs/integration/connector-worker.md)** — the source of truth for the worker side. It covers dependencies, infrastructure (PostgreSQL and PgBouncer, the Valkey persistence settings and ACL, the S3 bucket and IAM, network egress), configuration, the startup sequence (pool, migrations, durability checks, `connectors.New` with every provider), per-task context, the retry gate, operations and a verification checklist, plus a worker skeleton compiled against this version. It is not repeated here.

In outline: call `New` once at startup with every advertised provider registered, `DocRefs` (S3 content + Valkey metadata) and `SendIntents` set, then `Execute` per task:

```go
byType, err := connectors.New(cfg) // cfg as in the guide's Startup section
// per task: input already carries resolved secret_ref values for the job's own tenant
ctx = connectors.WithTenant(ctx, tenantID)          // document refs, Drive registry, send intents
ctx = connectors.WithDepartments(ctx, departments) // rest-call / sql-query
out, err := byType[registry.TypeStorage].Execute(ctx, input)
```

The pre-flight checklist is in the guide ([Verification checklist](docs/integration/connector-worker.md#8-verification-checklist)) and in `ARCHITECTURE.md` (Consumer conformance checklist).

### 4. Internal services called by `rest-call` / `sql-query`

Every call carries `x-internal-token` (the platform's service-to-service token) and `x-departments` (comma-joined `dept_uuid:role` pairs). Authenticate the token first, then apply your own row-level or role authorization from `x-departments`. `sql-query` posts `{"queryId", "params"}` and expects `{"resultSet": [...]}`. Both connectors speak HTTP/1.1 only and decode JSON numbers as `json.Number`, so integer IDs beyond 2^53 keep every digit. A `rest-call` error response (status `>= 300`) is classified by its status alone; its body is capped at 10 MiB (cut to the first 10 MiB, or what arrived if the read failed, returned as a string with `bodyTruncated: true`).

### 5. Handling errors

Classify with `errors.Is` against the sentinels (above), and use `errors.As` for provider-specific errors and for `*sendemail.SendError` (provider, HTTP status, outcome). A failed send-email still returns its output map with `deliveryOutcome`; record it on the task failure. On a `rest-call` status `>= 300` (redirects are never followed), the output map (`status`, `headers`, `body`) is returned alongside `ErrUpstream`. Full taxonomy: module LLD §17.

### 6. Retries and rate limits

The library never retries and has no rate limiter, and no provider client it builds retries a send (the SES SDK retryer is disabled). Every automatic retry — worker loop, workflow activity retry policy, queue redelivery — must ask **`connectors.DecideRetry(connectorType, err, aliasMethod)`**. It is deterministic: only a **transient** error may be retried, never a permanent or unknown one, and the type's `registry.RetryPolicy` must also allow it — `storage` always, `rest-call` only for an idempotent method, `send-email` only when the message was provably not delivered, `chat-notify` never. The decision carries the class, reason and rule; the worker logs it (`d.LogAttrs()`), counts it (`connector_task_failures_total{type, provider, class, retried}`) and bounds transient retries with backoff and an attempt limit. Resending an email after a permanent or unknown failure is an explicit action (`messageKey` + `resend: true` + `resendAttempt`). Provider rate limits are absorbed by the worker's per-type bounded pools (LLD §S6.5). Mappings and guidance for workflow authors: [`docs/runbooks/retry-semantics.md`](docs/runbooks/retry-semantics.md).

---

## Local development

### Prerequisites

- Go 1.26.9+
- `GOPRIVATE=github.com/BCBP-SOLUTIONS-FZC-LLC/*` (`GONOSUMDB` too) and an SSH key registered with the BCBP org
- `.env` (created from `.env-example` by `make setup`, git-ignored): the compose host ports (`WC_*`), the `TEST_*` variables the integration tests read, and platform-pgcommon's `PG_*` / `MIGRATION_DATABASE_URL` pointed at the compose stack. The library itself reads no configuration from the environment. Docker for the integration infrastructure in `docker-compose.yml` (PostgreSQL, PgBouncer, Valkey, floci S3) — `make test-unit` needs none

### Setup

```bash
git clone https://github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors
cd workflow-connectors
make setup       # .env from .env-example + go mod download + install the pre-commit hook (tidy-check, fmt-check, lint)
make ci          # everything CI runs
```

### Common commands

| Command | Description |
|---|---|
| `make setup` | Create `.env` from `.env-example` if missing, `go mod download`, install the pre-commit hook |
| `make ci` | tidy-check (library and tools module) · fmt-check · vet · lint · arch-lint · test-ci · build |
| `make test-unit` | Unit suite: `test/unit/...` + white-box tests in `./pkg/...` — no Docker |
| `make test-postgres` | Postgres suite (`-tags integration`): `test/postgres/...` + `storage/googledrive`'s PostgreSQL leg — direct and through PgBouncer |
| `make test-integration` | Integration suite (`-tags integration`): `test/integration/...` — Valkey, floci S3, PostgreSQL, Valkey restart tests |
| `make test` | All three suites in parallel (`-j3`) |
| `make test-ci` | All three suites in parallel with `-race` and per-suite coverage (`.coverage/*.out`), merged into `coverage.out` by `scripts/merge_coverage.py` |
| `make race` | All three suites with `-race` |
| `make docker-up` / `make docker-down` | Start (`docker compose up -d --wait`) / remove the stack: PostgreSQL :55432, PgBouncer :55433, Valkey :56379, floci S3 :4580 (ports overridable via `WC_*_PORT`). Every target except `test-unit` starts it itself |
| `make lint` | golangci-lint (pinned in `tools/go.mod`, run as `go tool -modfile=tools/go.mod golangci-lint`, so its dependencies stay out of the library's `go.mod`) |
| `make arch-lint` | go-arch-lint (pinned v1.15.0) against `.go-arch-lint.yml` |
| `make docs-check` | `ARCHITECTURE.md`'s mermaid blocks are identical to `docs/architecture/mermaid/*.mmd` (also in CI) |
| `make ci-scripts-test` | Regression tests for `detect-changes.sh`, CI's docs-only decision (scratch git repos; also in CI) |
| `make api-compat` | apidiff against the last stable tag: warns; with `API_NEW_VERSION=vX.Y.Z` fails on an incompatible change unless it is a major bump (release gate) |
| `make vet` | `go vet`, run twice: the default build and every test build tag (`integration`); `make lint` does the same |
| `make fmt` / `make fmt-check` / `make tidy` / `make tidy-check` | Go basics (`tidy`/`tidy-check` cover the library and the `tools` module) |
| `make build` | Compile-check every package |
| `make vuln-check` | govulncheck (pinned) over `./pkg/...` |
| `make cover` / `make cover-func` | Coverage HTML report / per-function summary |
| `make godoc` | pkgsite at `http://localhost:8080` |
| `make mod-verify` | `go mod verify` |
| `make install-hooks` | Copy `.githooks/pre-commit` into `.git/hooks` |
| `make clean` | Remove coverage artefacts |
| `make help` | List all targets |

### Running a single test

```bash
go test ./test/unit/storage/ -run TestStorage_UploadThenFetch_RoundTrips -count=1 -v
go test ./pkg/connectors/sendemail/gmail/ -run TestBuildRawMIME -count=1 -v
```

### Calling a connector locally

There is no server to call. Drive a connector through its mock from a test:

```go
client := storage.NewMockStorageClient()
conn := storage.New(map[string]storage.ProviderConstructor{
    "aws-s3": func(context.Context, map[string]any) (storage.ProviderClient, error) { return client, nil },
}, docref.NewMemoryService()) // tests only: production uses S3 content + Valkey metadata

out, err := conn.Execute(ctx, map[string]any{
    "provider": "aws-s3", "operation": "upload",
    "bucket": "b1", "key": "k1", "content": "hello", "contentType": "text/plain",
})
```

### Developer tools

| Tool | Purpose |
|---|---|
| `make godoc` | Browse the public API in pkgsite |
| `.githooks/pre-commit` | Runs `make tidy-check`, `make fmt-check` and `make lint` before each commit (`make install-hooks`) |
| `docs/architecture/mermaid/*.mmd` | Diagram sources — open in a Mermaid-aware IDE or [mermaid.live](https://mermaid.live) |

---

## Testing

Tests live in `./test`, laid out like iam-org-membership's. Only **white-box** tests that need unexported code stay beside it in `pkg/`: the provider adapters (`sendemail/{gmail,msgraph,sendgrid,ses}`, `storage/{gocloud,googledrive}`), which swap the SDK behind each adapter's private interface; `shared`'s client cache, which inspects its internal entries; `valkeystore`'s pure durability rules; and the duplicate-`Type` panics of `pkg/connectors` and `pkg/registry` (`indexByType`). Every other package's black-box tests are under **`test/unit/<package>`** (`aliasconfig`, `chatnotify`, `connectors`, `docref`, `documentextract`, `documents`, `registry`, `restcall`, `s3content`, `sendemail`, `sendintent`, `shared`, `sqlquery`, `storage`, `valkeystore`), and the infrastructure suites under **`test/integration/{multireplica,s3content,valkeystore}`** and **`test/postgres/{documents,sendemail,sendintent}`** (tag `integration`; the store contract suites run their in-memory leg there too; `test/postgres/sendemail` covers a lost reservation reply and a redelivered resend against the PostgreSQL send-intent store). At the root of each tree: **`test/unit`** (no build tag, no Docker) holds the retry decision matrix (every connector type × error kind × method, `TestRetryMatrix_*`), worker scenarios that run the worker's retry loop through `connectors.New` (throttled-then-accepted email delivered once, persistent connect failure bounded by the attempt limit, lost response not retried and its duplicate refused, S3 `SlowDown` retried, `NoSuchKey` once, integrity violation with nothing sent), and regression guards `REG-01`…`REG-10`, one per fixed production defect; **`test/postgres`** (tag `integration`) checks deployment shape: four replicas migrating both schemas at once (applied exactly once, idempotent), and the document registry and send intents through PgBouncer in transaction mode; **`test/integration`** (tag `integration`) runs whole workflows across worker replicas on real Valkey, floci S3 and PostgreSQL behind PgBouncer: the invoice workflow with a throttled send, an object deleted or overwritten between tasks, a lost response refused as a duplicate on another replica, an expired reference, and rest-call recovering for GET but not POST. Infrastructure comes from **`docker-compose.yml`** (PostgreSQL 17, PgBouncer, Valkey 8 with the production persistence flags, floci): `make docker-up` / `make docker-down`; `make test-postgres`, `make test-integration`, `make test` and `make test-ci` start it themselves; only `make test-unit` runs without Docker. CI starts the same compose stack and runs with `CI` set, so no infrastructure test can skip.

### Canonical tests (do not break)

- **`TestBuildRawMIME_ReceiverNameWithCRLF_CannotInjectHeaders`** (`sendemail/gmail`) — end-user-supplied names and subjects cannot inject email headers (LLD §S10 Decision #23).
- **`TestSendEmail_SameCredentialsDifferentSender_DoesNotShareCachedClient`** (`test/unit/sendemail`) — a cached Gmail client bound to one sender is never reused for another.
- **`TestRestCall_MissingDepartments_FailsClosedBeforeAnyRequest`** / **`TestSQLQuery_MissingDepartments_FailsClosed`** (`test/unit/{restcall,sqlquery}`) — internal calls never go out without caller identity.
- **`TestOpenGCSBucket_NonServiceAccountCredential_IsRejected`** (`storage/gocloud`) — only `service_account` keys are accepted for `gcp-gcs`.
- **`TestSendEmail_AttachmentFromStorage_ResolvesViaSharedDocRefs`** (`test/unit/connectors`) — a ref fetched by `storage` resolves as a `send-email` attachment.
- **`TestMultiReplica_*`** (`test/integration/multireplica`, Valkey + S3) — separate worker replicas (own `New`, own Valkey and S3 clients) sharing one Valkey and one document bucket: A writes/B reads, writer terminates after the write, rolling deployment, concurrent writers across replicas, concurrent create of one ID (one winner), delete and recreate, missing ref and tenant isolation.
- **`TestS3_ChecksumMismatch_IsIntegrityViolation`**, **`TestS3_OverwrittenObject_FailsValidation`**, **`TestS3_MissingObject_IsSourceMissing`**, **`TestDocRefs_UnresolvableSource_FailsBeforeAnySideEffect`** (`test/integration/s3content`; the last in `test/unit/connectors`) — altered or missing content is never served, sent or uploaded.
- **`TestPut_StoresMetadataOnly`** (`test/integration/valkeystore`) — Valkey holds exactly the nine metadata fields per ref, no content, under 1 KiB.
- **`TestValkeyRestart_RefsRecoverFromAOF`**, **`TestValkeyRestart_DocumentsStillResolveFromS3`**, **`TestNonDurableServer_IsRefused`** (`test/integration/valkeystore`, own Valkey containers) — metadata survives a Valkey restart from the AOF alone and documents still resolve from S3; a server without AOF or with an eviction policy is refused.
- **`TestDrive_HighConcurrency_ExactlyOneCanonicalDocument`**, **`TestDrive_TwoSimultaneousUploads_OneWinsOneGetsInProgress`** and the rest of `storage/googledrive` (run against the in-memory store and PostgreSQL) — concurrent Drive uploads converge on one document and one file; `TestDrive_HandPlacedFile_ConcurrentUploadAndDelete_Converge` — uploads and deletes from separate replicas over a hand-placed file end with one adopted file or none.
- **`TestStore_ConcurrentClaimsOnNewIdentity_ExactlyOneWinner`**, **`TestPostgres_UniqueConstraintRejectsSecondRowForIdentity`** (`test/postgres/documents`) — the database itself enforces one row per document.
- **`TestAll_FourTypes`** / **`TestNew_FourTypes`** (`test/unit/registry`, `test/unit/connectors`) — the registry and `New` stay in step.
- **`TestSecretRefDescriptions_DoNotClaimAuthorSupplied`** (`test/unit/registry`) — credential provenance wording (LLD rev 8.13).

### Coverage

`.github/scripts/coverage-gate.sh` reads `go tool cover -func=coverage.out`'s total and fails below `COVERAGE_THRESHOLD`, set to **98%** in `validate-test.yml`. Coverage is measured across every package under `./pkg/...` (`-coverpkg`, `COVER_PKG_LIST` in the Makefile) by the unit, postgres and integration suites, run in parallel with `-race` and merged into `coverage.out` (`scripts/merge_coverage.py`). The last run reported **99.96%**. The threshold is a ratchet: raise it as coverage improves, never lower it.

---

## Configuration

The library reads **no environment variables**. Everything is passed to `connectors.New`:

| `Config` field | Required | Default |
|---|---|---|
| `InternalToken` | yes | — (`New` fails) |
| `Aliases` | no | empty — every alias unknown |
| `HTTPClient` | no | a client with no client-wide timeout: each call is bounded by its alias's `timeout`, else 30 s (`shared.CallTimeout`). Any client is copied, set never to follow redirects, and limited to HTTP/1.1: an `*http.Transport` (or none) is cloned with HTTP/2 off; any other `RoundTripper` is used as-is and must not enable HTTP/2 |
| `StorageProviders` | no | empty — every `storage` call `ErrValidation` |
| `SendEmailProviders` | no | empty — every `send-email` call `ErrValidation` |
| `ChatNotifyClient` | no | none — every `chat-notify` call fails with `ErrValidation` |
| `SendIntents` | no | none — a `send-email` call with a `messageKey` fails with `ErrValidation` |
| `DocRefs` | no | none — `createDocument`, ref content and email attachments fail with `ErrValidation`. Production: `docref.NewService(valkeystore.New(...), s3content.New(...))` |

Per-call credentials arrive in `Execute`'s input under the registry's field names: `accessKey`/`secretKey`/`region` (aws-s3, aws-ses — `region` required for both), `azureAccountName`/`azureAccountKey`, `gcpServiceAccountKey`, `driveServiceAccountKey`, `apiKey` (sendgrid), `tenantId`/`clientId`/`clientSecret` (microsoft-365), `serviceAccountKey` (google-workspace), `authToken` (chat-notify).

---

## Security

| Topic | Guidance |
|---|---|
| **Credentials never travel in a plan** | For every `secret_ref` field the worker reads the tenant's stored credential from OpenBao just before `Execute` and passes the value. A connector never sees an OpenBao path, and a diagram never names a credential (LLD §S6.2, rev 8.13) |
| **Per-credential client isolation** | Provider clients are cached by a hash of every credential field (plus bucket or sender), so one tenant's client is never handed to another |
| **Internal calls only** | `rest-call`/`sql-query` take aliases, never URLs; the alias `baseURL` allowlist is enforced at alias write in `definition_service` (LLD §S9) |
| **Mandatory internal auth** | Every internal call carries `x-internal-token` and `x-departments`; missing either fails closed before any request |
| **Header-injection safety** | Gmail's raw MIME strips CR/LF from every header value and Q-encodes names and subject; Graph path-escapes the sender |
| **`gcp-gcs` service-account keys only** | Workload-identity configurations that would read the worker's own filesystem or metadata identity are refused |
| **Document refs** | Random UUIDs. The reference's tenant is checked, and its object key must sit under the caller's tenant prefix, **before** any S3 request — a ref from another tenant never resolves, on any replica. Content is read with the worker's own S3 credentials (none are stored in refs) and its size and SHA-256 are verified on every resolution, so an overwritten object is refused, never served; 24 h TTL |
| **No redirects on internal calls** | `rest-call`/`sql-query` never follow a redirect, so `x-internal-token` and `x-departments` can never be re-sent to another host |
| **One recipient per address field** | `senderEmail`/`receiverEmail` must each be a single bare address, so a value like `a@x.com, b@y.com` cannot add recipients |
| **One Drive file per logical document** | Drive cannot create "if absent", so uniqueness comes from the `connector_documents` unique constraint, never from listing the folder. Only the upload that claims the row writes to Drive; a concurrent one gets a retryable `documents.ErrUploadInProgress` and never touches Drive. A same-name file placed in the folder by hand is adopted (updated and tagged with the document ID) by the first upload, not duplicated, and removed by the delete |
| **No worker AWS settings in tenant clients** | S3 and SES clients are built only from the tenant's credentials and region, never the worker's AWS environment or config files |

Vulnerability reporting: [SECURITY.md](SECURITY.md). STRIDE threat model: `ARCHITECTURE.md`.

---

## Observability

None in the library, by design — it imports no logger, metrics or tracing package, so it adds nothing to a consumer's dependency graph. The worker times and counts each `Execute`, labels it by connector `Type()` and by error sentinel, and records outcomes. Error messages name the connector, operation, alias or provider, and never include credential values.

---

## Distribution

No image, binary or Helm chart. The two PostgreSQL stores embed their SQL migrations, which the worker applies with `ApplySchema`. A release is a Git tag consumed through the Go module proxy.

| Consumer | Imports | Compiles in |
|---|---|---|
| `definition_service` | `pkg/registry` | Standard library only |
| `execution_service` `cmd/connector-worker` | `pkg/connectors`, the adapter packages it wires, `docref/{s3content,valkeystore}`, `documents/sqlstore`, `sendintent/sqlstore` | Cloud SDKs only through those adapters; `aws-sdk-go-v2` (S3) through `docref/s3content`, go-redis through `docref/valkeystore`, platform-pgcommon v2 through the two `sqlstore` packages |

SemVer, described in [VERSIONING.md](VERSIONING.md). A change to `Config`, a connector's input or output field names, or a sentinel error is breaking. After a PR merges, move `[Unreleased]` in `CHANGELOG.md` to a versioned heading, then tag:

```bash
git tag -a vX.Y.Z -m "vX.Y.Z"
git push origin vX.Y.Z
```

---

## CI

Six workflow files, in the same structure as platform-pgcommon's (scoped to a library that builds no image):

- **`ci.yml`** — orchestrator. A **`changes`** job (`.github/scripts/detect-changes.sh`) decides whether the build/test jobs run: a documentation-only change (any `*.md`, `docs/architecture/*.mmd`), a draft PR or a PR labelled `skip-ci` skips them. The workflow itself always runs, and the reusable validate workflows receive a `skip` input and skip their own job, so the required checks still report on a docs-only PR (a `paths-ignore` would leave them pending forever). Then `validate-test.yml` ∥ `validate-quality.yml` ∥ **API compatibility** (apidiff against the last stable tag, warning only); after tests, a Trivy filesystem scan of `go.mod`/`go.sum` (CRITICAL/HIGH/UNKNOWN fail; `tools/` skipped) with SARIF and a CycloneDX SBOM; a PR summary comment. "Build image (cache)", "Lint Dockerfile" and "Smoke tests" are no-op placeholders for the org branch-protection ruleset (there is no Dockerfile, image or runtime).
- **`validate-test.yml`** (reusable) — images pulled (with retries) from `mirror.gcr.io`, Google's Docker Hub mirror (`WC_REGISTRY`; anonymous Docker Hub pulls from runner IPs hit the rate limit) → `docker compose up -d --wait` (the same stack as local; with `CI` set no infrastructure test can skip) → `make test-ci` (unit, postgres and integration suites in parallel, `-race`, merged coverage) → coverage gate (**98%**) → compose logs on failure → coverage artifact → `make build`.
- **`validate-quality.yml`** (reusable) — `go mod verify` → HTML-escaped-operator check on workflow files → **every action pinned to a commit SHA** → gofmt → `go mod tidy` drift → vet → golangci-lint → **go-arch-lint** → `make docs-check` → `make ci-scripts-test` (`detect-changes.sh`'s docs-only verdict, 22 cases) → govulncheck.
- **`docs.yml`** — on changes to `ARCHITECTURE.md` or `docs/architecture/**`: the diagram check (`scripts/docs_check.py`), which the skipped quality job does not run on a docs-only change.
- **`changelog-check.yml`** — fails a PR touching `pkg/`, `go.mod` or `go.sum` without a `CHANGELOG.md` update.
- **`release.yml`** — tag-triggered (`vX.Y.Z`, `vX.Y.Z-*`, or manual dispatch with a tag), with the same job graph as platform-pgcommon's: **verify** (`verify-release-tag.sh`: a dispatch only from `main` or the tag itself, the tag points at the checkout, the commit is on `origin/main`, its major version matches the module path's `/vN`; `verify-changelog-entry.sh`: a `## [X.Y.Z]` section, which a prerelease may share with its base version) → `validate-test.yml` ∥ `validate-quality.yml` ∥ **API compatibility, blocking** (a non-major release fails on an incompatible exported-API change) ∥ "Build image (cache)" (placeholder) → **Trivy CVE scan** + SBOM and "Smoke tests" (placeholder), both after test + image as in CI; **Build source archive** after both validation gates (pgcommon's "Build binaries") → **GitHub Release**: notes from the changelog section, the archive re-checked against its `.sha256`, `checksums.txt` over the archive and the SBOM, **Cosign keyless-signed** (bundle `checksums.txt.sigstore.json`). pgcommon's "Cross-language compatibility" and "Push image → GHCR" have no counterpart (no CLI, no image).

Helper scripts live in `.github/scripts/`; `make ci-scripts-test` and `make api-compat` run two of them locally. **Secrets (org-level):** `GO_PRIVATE_TOKEN` — stored as a git credential ("Configure private module access") so `go mod download` and apidiff can fetch the private `platform-pgcommon` module, with `GOPRIVATE`/`GONOSUMDB` set; `CI_REPO_READ_TOKEN` — every checkout uses it, falling back to `github.token`. The reusable workflows declare both and the callers pass them explicitly (no `secrets: inherit`), so PR test code never runs with other secrets in scope.

---

## Cross-service dependencies

### 1. Outbound calls (this library → other systems)

Made only from inside `Execute`, on the worker's behalf.

| Operation | Dependency | Package | On failure |
|---|---|---|---|
| `storage` on `aws-s3` / `azure-blob` / `gcp-gcs` | Tenant's object store | `storage/gocloud` | `ErrUpstream`; worker retries (`safe`) |
| `storage` on `google-drive` | Google Drive v3 | `storage/googledrive` | `ErrUpstream`; worker retries (`safe`) |
| `send-email` on `aws-ses` / `sendgrid` | SES v2 / SendGrid v3 | `sendemail/ses`, `sendemail/sendgrid` | `ErrNotDelivered` (retried only if transient) or `ErrDeliveryUnknown` (never retried) |
| `send-email` on `microsoft-365` | Graph v1.0 `sendMail` (+ Entra ID token) | `sendemail/msgraph` | `ErrNotDelivered` (retried only if transient) or `ErrDeliveryUnknown` (never retried) |
| `send-email` on `google-workspace` | Gmail v1 (+ Google OAuth) | `sendemail/gmail` | `ErrNotDelivered` (retried only if transient) or `ErrDeliveryUnknown` (never retried) |
| `rest-call` | Internal platform service (by alias) | `restcall` | `ErrUpstream` on status `>= 300` (response returned too) or network error |
| `sql-query` (not wired) | Owning service's query endpoint | `sqlquery` | `ErrUpstream` on status `>= 300` or network error |

### 2. Inbound callers (other services → this library)

| Caller | Uses | Purpose |
|---|---|---|
| `definition_service` | `pkg/registry` | `UNKNOWN_CONNECTOR_TYPE` compile check; authoring-template generation; secret-field marking |
| `execution_service` `cmd/connector-worker` | `pkg/connectors` + adapters | Runs every connector task |

### 3. Data supplied by other services

| Supplier | What | How it arrives |
|---|---|---|
| `definition_service` | Alias registry | Worker fetches `GET /internal/connector-aliases` at startup → `Config.Aliases` |
| OpenBao (via the worker) | Tenant credentials | Resolved values in `Execute`'s input |
| `execution_service` | Task input, departments | `IOMapping`-resolved input map; `WithDepartments` |

### 4. Infrastructure dependencies

| System | Role |
|---|---|
| **S3** (platform document bucket) | Document content for refs — the system of record (`docref/s3content`), reached with the worker's own IAM role. SSE-KMS, public access blocked, lifecycle rule expiring the document prefix after 2 days; checked at startup by `s3content.Store.Check`. See [`docs/runbooks/document-refs.md`](docs/runbooks/document-refs.md) |
| **Valkey** (8+) | Document-ref metadata only (`docref/valkeystore`), shared by every replica — a few hundred bytes per ref, independent of document size. Must run `appendonly yes`, `appendfsync always` (or `everysec`), `maxmemory-policy noeviction`; checked at startup by `valkeystore.CheckDurability` |
| **PostgreSQL** (the worker's database, 13+) | Send intents (optional) — `connector_send_intents`, applied with `sendintent/sqlstore.ApplySchema`. Drive document registry — `connector_documents` (unique per tenant + folder + filename) and `connector_document_attempts` (audit). Required when `google-drive` is wired; run `sqlstore.ApplySchema(ctx, runner)` from the worker's migration step (own tracking table `connector_documents_migrations`) |

Nothing else: no queue, topic or schema registry.

---

## Out of scope

| Concern | Where it lives |
|---|---|
| Connector worker: stream delivery, dedup, leases, pools, timeouts, retries, dead-lettering | `execution_service` `cmd/connector-worker` |
| Completion/failure callbacks and output renaming | `execution_service` |
| Reading credentials from OpenBao | `execution_service` worker |
| Writing, rotating, revoking credentials; alias registry and its allowlist | `definition_service` |
| BPMN compilation, the `connector:` task rule, authoring templates | `definition_service` |
| Connector-task DSL shape | `workflow-models` |
| Logging, metrics, tracing | `execution_service` worker |
| A Document Service | Not proposed — document refs are opaque strings (LLD §S10 Decision #14) |

---

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for development setup and the PR checklist. Adding a provider or a connector type: `ARCHITECTURE.md` (Extending the catalogue).

| Document | Description |
|---|---|
| [`ARCHITECTURE.md`](ARCHITECTURE.md) | Layer model, dependency rules, execution and internal-auth flows, client cache, document refs, failure domains, invariants, consumer checklist, threat model |
| [`docs/architecture/`](docs/architecture/README.md) | Standalone Mermaid diagrams |
| [`docs/lld/workflow-connectors-library-lld.md`](docs/lld/workflow-connectors-library-lld.md) | The LLD, rev 1.13 — Part I: §5 public API and connector contracts, §16 open questions, §17 error taxonomy, §19 migration; Part II: system design, §S6.4 catalogue field tables, §S10 decision log |
| [`.go-arch-lint.yml`](.go-arch-lint.yml) | Executable dependency rules |
| [`VERSIONING.md`](VERSIONING.md) | SemVer rules and release process |
| [`docs/runbooks/email-delivery.md`](docs/runbooks/email-delivery.md) | send-email delivery outcomes, what to log and alert on, and how to handle an `unknown` outcome |
| [`docs/runbooks/retry-semantics.md`](docs/runbooks/retry-semantics.md) | Transient / permanent / unknown classification, `DecideRetry` rules, provider mappings (S3, Azure/GCS, Drive, HTTP, email, document refs), what workflow authors can expect to recover |
| [`docs/runbooks/document-refs.md`](docs/runbooks/document-refs.md) | Document refs: S3 content and Valkey metadata — bucket settings and IAM, Valkey persistence, error meanings, retention, backup and restore, monitoring |
| [`CHANGELOG.md`](CHANGELOG.md) | Per-version changes, including import-path migrations |
| [`SECURITY.md`](SECURITY.md) | Vulnerability reporting |

---

## License / ownership

BCBP Solutions FZC LLC — internal platform library, owned by the workflow team (`@BCBP-SOLUTIONS-FZC-LLC/workflow`, see `.github/CODEOWNERS`). Private repository with no `LICENSE` file. Not for external distribution.
