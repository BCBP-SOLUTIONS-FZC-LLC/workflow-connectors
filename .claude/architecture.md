# Architecture

workflow-connectors is a library with a **core / adapter** split: connector cores validate input and, for `storage`, `send-email`, `chat-notify` and `document-extract`, talk to small `ProviderClient` ports (`rest-call` and `sql-query` call internal services directly through alias config); provider adapters implement those ports per provider family (`storage/gocloud` covers S3, Azure Blob and GCS through gocloud.dev; `sendgrid` and `msgraph` are our own HTTP clients, not vendor SDKs); the `connectors` facade builds the cores from a `Config` the consumer fills with adapters, stores and clients. The consumer (`execution_service`'s `cmd/connector-worker`) is the composition root. The facade's own transitive third-party imports are only `gopkg.in/yaml.v3` and `github.com/google/uuid`; `pkg/registry` imports only the standard library.

## Package Layout

```
pkg/
├── registry/                     # dependency-free contract imported by workflow-definition-service
│   ├── registry.go               #   Type* constants, FieldKind*, RetryPolicy*, IsIdempotentMethod, Field, Definition, All()
│   └── definitions.go            #   storage / send-email / rest-call / chat-notify field tables and retry policies
└── connectors/
    ├── connector.go              # Connector interface, New(Config) → map[type]Connector (4 types)
    ├── config.go                 # Config: Aliases, HTTPClient, InternalToken, StorageProviders, SendEmailProviders,
    │                             #         ChatNotifyClient, SendIntents, DocRefs
    ├── errors.go                 # re-exported sentinels, ErrorClass, ClassOf, IsTransient, IsPermanent, IsRetryable
    ├── retry.go                  # RetryDecision, DecideRetry, AutoRetryAllowed
    ├── internalauth.go           # WithTenant / TenantFromContext, WithDepartments / DepartmentsFromContext
    ├── shared/                   # leaf: errors + classes, delivery sentinels, limits and HTTP clients, field and
    │                             #       HTTP helpers, ClientCache, tenant/departments context, Google key check
    ├── aliasconfig/              # rest-call / sql-query alias schema, Load + Validate, ResolveEndpoint / ResolveQuery
    ├── storage/                  # core: fetch / upload / delete, createDocument, inline content, client cache, mock
    │   ├── gocloud/              #   adapter: aws-s3, azure-blob, gcp-gcs (gocloud.dev/blob)
    │   └── googledrive/          #   adapter: google-drive (Drive v3) on a documents.Store
    ├── sendemail/                # core: validation, attachments, send intents, outcome classification (outcome.go), mock
    │   ├── ses/                  #   adapter: aws-ses (sesv2, SDK retries off)
    │   ├── sendgrid/             #   adapter: sendgrid (requests built per call, POST /v3/mail/send)
    │   ├── msgraph/              #   adapter: microsoft-365 (Graph sendMail, client credentials)
    │   └── gmail/                #   adapter: google-workspace (Gmail API, raw MIME in mime.go)
    ├── restcall/                 # core: alias-only internal HTTP calls
    ├── chatnotify/               # core: create-channel / invite-to-channel / post-message on an injected client, mock
    ├── sqlquery/                 # core: alias-only internal query service call (implemented, not wired into New)
    ├── documentextract/          # core: analyze a document via an injected client, mock (implemented, not wired)
    ├── docref/                   # document refs: Ref, Store + ContentStore ports, Service, in-memory test stores
    │   ├── s3content/            #   ContentStore on the worker's S3 client (aws-sdk-go-v2/service/s3)
    │   └── valkeystore/          #   Store on go-redis (Lua create-only, WAITAOF), CheckDurability
    ├── documents/                # Drive document registry: state machine, Store port, InProgressError, MemoryStore
    │   └── sqlstore/             #   PostgreSQL Store on platform-pgcommon + migrations/ (embedded)
    └── sendintent/               # send-email send intents: Store port, DuplicateRequestError, MemoryStore
        └── sqlstore/             #   PostgreSQL Store on platform-pgcommon + migrations/ (embedded)

test/                             # black-box tests, same module as the library (no test/go.mod)
├── unit/                         # no Docker: retry matrix, worker scenarios, regressions, one dir per package
├── postgres/                     # -tags=integration: stores direct + through PgBouncer, concurrent migrations
└── integration/                  # -tags=integration: workflow scenarios, multireplica, s3content, valkeystore
tools/go.mod                      # second module: golangci-lint as a `tool` dependency
scripts/                          # merge_coverage.py (suite profiles → coverage.out), init-floci.sh (dev bucket)
docker-compose.yml                # postgres 17, pgbouncer (transaction mode), valkey 8 (AOF, noeviction), floci S3
docs/                             # architecture/ (README + mermaid/*.mmd), integration/, lld/, runbooks/
```

White-box tests stay next to the code only where they need unexported identifiers: `pkg/connectors/connector_internal_test.go`, `pkg/registry/registry_internal_test.go`, `pkg/connectors/shared/clientcache_test.go`, `pkg/connectors/docref/valkeystore/durability_test.go`, and the adapter packages' `*_test.go` (`storage/gocloud`, `storage/googledrive`, `sendemail/{gmail,msgraph,sendgrid,ses}`).

## Runtime Dependencies (what every consumer inherits)

| Module (root `go.mod`) | Used by | For |
|---|---|---|
| `github.com/BCBP-SOLUTIONS-FZC-LLC/platform-pgcommon/v2` v2.0.1 | `documents/sqlstore`, `sendintent/sqlstore` | pool, `RunInTx`, `ErrNoRows`, SQLSTATE helpers, `migrate.Runner` |
| `github.com/redis/go-redis/v9` v9.22.0 | `docref/valkeystore` | Valkey metadata store |
| `github.com/aws/aws-sdk-go-v2` (+ `credentials`, `service/s3`, `service/sesv2`) | `docref/s3content`, `storage/gocloud`, `sendemail/ses` | S3 document content, tenant S3 buckets, SES |
| `gocloud.dev` v0.46.0, `github.com/Azure/azure-sdk-for-go/sdk/storage/azblob` | `storage/gocloud` | portable blob access (S3, Azure, GCS) |
| `google.golang.org/api` v0.293.0 | `storage/googledrive` (`drive/v3`), `sendemail/gmail` (`gmail/v1`) | Drive and Gmail APIs |
| `golang.org/x/oauth2` v0.37.0 | `storage/gocloud` (GCS), `storage/googledrive`, `sendemail/gmail`, `sendemail/msgraph` (`clientcredentials`) | OAuth token sources |
| `github.com/sendgrid/sendgrid-go` | `sendemail/sendgrid` | message model (`helpers/mail`) |
| `github.com/google/uuid` | cores, `docref`, `documents`, `sendintent` | ref IDs, attempt IDs, reservation tokens |
| `gopkg.in/yaml.v3` | `aliasconfig` | alias registry file |
| `github.com/stretchr/testify` | tests only | in the root `require` because `test/` is in the root module |

`golangci-lint` is **not** in the root module (`tools/go.mod`). There is no OTel, Prometheus or logging dependency: the library emits no metrics or logs; the worker logs `RetryDecision.LogAttrs()` (`log/slog` attributes) and counts failures itself.

## Dependency Rules (enforced in CI)

**go-arch-lint** (`.go-arch-lint.yml`, `make arch-lint`, CI step "Architecture lint"; `_test.go` files excluded; `depOnAnyVendor: false`):

| Component | Packages | May depend on | Vendors |
|---|---|---|---|
| `registry` | `pkg/registry` | nothing | none |
| `shared` | `pkg/connectors/shared` | nothing | none |
| `aliasconfig` | `pkg/connectors/aliasconfig` | — | `yaml` |
| `connector_core` | `storage`, `sendemail`, `chatnotify`, `restcall`, `sqlquery`, `documentextract` | `registry`, `shared`, `aliasconfig`, `send_intents`, `docref` | `uuid` |
| `documents` | `pkg/connectors/documents` | `shared` | `uuid` |
| `document_sqlstore` | `documents/sqlstore` | `documents`, `shared` | `pgcommon` |
| `docref` | `pkg/connectors/docref` | — | `uuid` |
| `docref_valkeystore` | `docref/valkeystore` | `docref` | `valkey` (go-redis) |
| `docref_s3content` | `docref/s3content` | `docref` | `aws-sdk` |
| `send_intents` | `pkg/connectors/sendintent` | `shared` | `uuid` |
| `send_intents_sqlstore` | `sendintent/sqlstore` | `send_intents`, `shared` | `pgcommon` |
| `provider_adapters` | `storage/{gocloud,googledrive}`, `sendemail/{ses,sendgrid,msgraph,gmail}` | `connector_core`, `documents`, `shared` | `uuid`, `aws-sdk`, `azure-sdk`, `gocloud`, `google-api`, `oauth2`, `sendgrid` |
| `connectors` (facade) | `pkg/connectors` | `connector_core`, `shared`, `aliasconfig`, `registry`, `send_intents`, `docref` | none |

Consequences: no core imports a cloud SDK; the facade never imports an adapter, a `sqlstore`, `valkeystore` or `s3content`; `pgx` is **not** a declared vendor, so a direct `pgx` import fails arch-lint. (The `docref` component comment in `.go-arch-lint.yml` still mentions "its PostgreSQL store"; there is none — refs live in Valkey + S3.)

**golangci-lint** (`.golangci.yml`, v2 config; `standard` + `revive`, `errcheck`, `staticcheck`, `bodyclose`, `depguard`): the `depguard` rule **`pgcommon-only`** denies `github.com/jackc/pgx`, `database/sql` and `github.com/lib/pq` everywhere, tests included. `revive` is excluded for `_test.go`.

**Database access rule:** every PostgreSQL statement goes through platform-pgcommon on the worker's `*pgcommon.Pool`, inside `pgcommon.RunInTx` (so `StatementTimeout` / `LockTimeout` apply, also behind PgBouncer); migrations go through `migrate.Runner` on a direct DSN.

## Consumer Integration

### `execution_service` — `cmd/connector-worker` (run side)

Full guide: [`docs/integration/connector-worker.md`](../docs/integration/connector-worker.md) (dependencies, infrastructure, configuration, startup order, per-task context, retries, operations, go-live checklist, compiling skeleton). In short:

1. Pool: `pgcommon.ConfigFromEnv()` → `pgcommon.NewPool` (through PgBouncer, `PG_BOUNCER_MODE=true`).
2. Migrations: `documentsql.ApplySchema(ctx, runner)` and `intentsql.ApplySchema(ctx, runner)` with `runner := &migrate.Runner{DSN: pgcommon.MigrationDSNFromEnv()}` (direct connection; safe from every replica at once).
3. Fail-fast checks: `valkeystore.CheckDurability(ctx, valkey)` and `s3content.New(s3Client, bucket).Check(ctx, "docrefs/")`.
4. `docref.NewService(valkeystore.New(valkey, valkeystore.Options{}), content, docref.WithKeyPrefix("docrefs/"))`.
5. `connectors.New(connectors.Config{…})` with every registry provider: `gocloud.NewProvider` (aws-s3, azure-blob, gcp-gcs), `googledrive.NewProvider(documentsql.New(pool))`, `sendgrid.NewProvider`, `ses.NewProvider`, `msgraph.NewProvider`, `gmail.NewProvider`; `SendIntents: intentsql.New(pool)`; `DocRefs`; `InternalToken`; `Aliases` from `aliasconfig.Load`.
6. Per task: `connectors.WithTenant`, `connectors.WithDepartments` (rest-call), resolved `secret_ref` values in the input, a timeout below `documents.DefaultLease` (15 min), `Execute`, then `connectors.DecideRetry` before any automatic retry.
7. Operations: `ResetClients()` (storage, send-email) after credential rotation; scheduled `documentsql.Store.PruneAttempts`; `pool.DrainAndClose` on shutdown.

### `workflow-definition-service` (compile side)

Imports only `pkg/registry`: `registry.All()` for the connector catalogue and field tables, `Field.IsSecretRef()` to keep credentials out of inline values, `RetryPolicy` and `IsIdempotentMethod` for display and validation. It also enforces the alias host allowlist when aliases are written (this library only rejects malformed `baseURL`s).

### Internal services called by `rest-call` / `sql-query`

They receive `x-internal-token` (`Config.InternalToken`) and `x-departments` (comma-joined `dept_uuid:role` pairs from `WithDepartments`) on every request; they must authorize from those headers.
