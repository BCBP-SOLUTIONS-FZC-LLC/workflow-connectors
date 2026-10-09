# API Reference

Module `github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2`. Every package under `pkg/` is public API under SemVer (`VERSIONING.md`). Import paths below are relative to the module (`…/v2/pkg/…`).

## 1. API Conventions

- **Connector contract:** `Execute(ctx, input map[string]any) (map[string]any, error)`. Input keys are the registry field names (`pkg/registry/definitions.go`). The output map can be non-nil **with** an error: `send-email` always returns `deliveryOutcome`; `rest-call` returns `status`, `headers`, `body` (and `bodyTruncated`) for a status ≥ 300.
- **Context carries the call identity:** `connectors.WithTenant` (document refs, Drive registry, send intents) and `connectors.WithDepartments` (`rest-call`, `sql-query`). Missing → `ErrMissingTenant` / `ErrMissingInternalAuth` (permanent).
- **Credentials arrive resolved in the input.** Fields with `Kind == FieldKindSecretRef` are resolved from OpenBao by the worker; the library never reads secrets or environment variables.
- **Every error has exactly one class** (transient / permanent / unknown), read with `connectors.ClassOf`. There are no per-class `errors.Is` sentinels. Sentinels below classify *kind*, not retryability.
- **Numbers:** `rest-call` / `sql-query` decode JSON numbers as `json.Number`; `shared.StringField`, `StringSliceField` and `FormatParam` accept them.
- **Concurrency:** connectors are safe for concurrent `Execute`; keep the map `connectors.New` returns for the process lifetime (each call builds new client caches).

## 2. `pkg/registry` (dependency-free)

| Identifier | Notes |
|---|---|
| `TypeStorage` `"storage"`, `TypeSendEmail` `"send-email"`, `TypeRestCall` `"rest-call"`, `TypeChatNotify` `"chat-notify"`, `TypeDocumentExtract` `"document-extract"`, `TypeSQLQuery` `"sql-query"` | the last two have connectors but no `Definition` in `All()` |
| `FieldKind` + `FieldKindString`, `Bool`, `Int`, `Float`, `Enum`, `List`, `Map`, `Any`, `DocRef` (`"document_ref"`), `SecretRef` (`"secret_ref"`), `Timestamp` | |
| `RetryPolicy` + `RetryPolicySafe` (storage), `RetryPolicyUnsafe` (chat-notify), `RetryPolicyConditional` (rest-call), `RetryPolicyNotDelivered` (send-email) | |
| `IsIdempotentMethod(method) bool` | GET, HEAD, PUT, DELETE, OPTIONS, TRACE (case-insensitive) |
| `Field{Name, Kind, Required, EnumValues, Condition, Description}`, `Field.IsSecretRef()` | |
| `Definition{Type, DisplayName, Description, Inputs, Outputs, Retry}` | |
| `All() map[string]Definition` | storage, send-email, rest-call, chat-notify; panics on a duplicate type |

## 3. `pkg/connectors` (facade)

```go
type Connector interface {
    Type() string
    Execute(ctx context.Context, input map[string]any) (map[string]any, error)
}
type Config struct {
    Aliases            aliasconfig.Config
    HTTPClient         *http.Client                              // nil: default; see shared.InternalHTTPClient
    InternalToken      string                                    // required
    StorageProviders   map[string]storage.ProviderConstructor
    SendEmailProviders map[string]sendemail.ProviderConstructor
    ChatNotifyClient   chatnotify.ProviderClient                 // nil: every call is ErrValidation
    SendIntents        sendintent.Store                          // nil: messageKey is ErrValidation
    DocRefs            *docref.Service                           // nil: createDocument, ref content, attachments are ErrValidation
}
func New(cfg Config) (map[string]Connector, error) // error only for an empty InternalToken
```

| Identifier | Notes |
|---|---|
| `ErrValidation`, `ErrMissingInternalAuth`, `ErrUpstream`, `ErrMissingTenant`, `ErrNotDelivered`, `ErrDeliveryUnknown` | aliases of the `shared` sentinels |
| `ErrorClass` (= `shared.Class`), `ClassUnknown`, `ClassTransient`, `ClassPermanent` | `Class.String()`: `"unknown"`, `"transient"`, `"permanent"` |
| `ClassOf(err) (ErrorClass, string)`, `IsTransient`, `IsPermanent`, `IsRetryable` (= transient) | |
| `RetryDecision{Retry, Class, Reason, Policy, Rule}`, `RetryDecision.LogAttrs() []slog.Attr` | attrs: `retry`, `error_class`, `error_reason`, `retry_policy`, `retry_rule` |
| `DecideRetry(connectorType string, err error, method string) RetryDecision` | `method` = the rest-call alias's HTTP method; `""` otherwise |
| `AutoRetryAllowed(connectorType, err, method) bool` | `DecideRetry(…).Retry` |
| `WithTenant`, `TenantFromContext`, `WithDepartments`, `DepartmentsFromContext` | re-export `shared` |

`DecideRetry` rules (`Rule` values): unknown type → `"unknown connector type"`; permanent → `"permanent"`; unknown class → `"unknown"`; transient + `safe` → `"transient"`; + `conditional` → `"transient"` if `IsIdempotentMethod(method)` else `"non-idempotent method"`; + `not-delivered` → `"transient, not delivered"` if `errors.Is(err, ErrNotDelivered)` else `"may have been delivered"`; any other policy (`unsafe`) → `"policy unsafe"`.

## 4. Connector cores, ports and mocks

| Package | Constructor | Port | Mock (tests only) |
|---|---|---|---|
| `connectors/storage` | `New(providers map[string]ProviderConstructor, docRefs *docref.Service) *Connector`; `(*Connector).ResetClients()` | `ProviderClient{Fetch(ctx, bucket, key string, maxBytes int64) ([]byte, string, error); Upload(ctx, bucket, key string, content []byte, contentType string) error; Delete(ctx, bucket, key string) error}`; `ProviderConstructor func(ctx, params map[string]any) (ProviderClient, error)` | `NewMockStorageClient()` (`SetError`, `Reset`) |
| `connectors/sendemail` | `New(providers map[string]ProviderConstructor, docRefs *docref.Service, opts ...Option) *Connector`; `WithSendIntents(store) Option`; `ResetClients()` | `ProviderClient{Send(ctx, EmailMessage) (messageID string, err error)}`; optional `AttachmentLimiter{MaxAttachmentBytes() int64}`; `EmailMessage`, `EmailAttachment` | `NewMockSendEmailClient()` (`Sent`, `SetError`, `Reset`) |
| `connectors/restcall` | `New(aliases aliasconfig.Config, httpClient *http.Client, internalToken string) Connector` | — | — |
| `connectors/chatnotify` | `New(client ProviderClient) Connector` | `ProviderClient{CreateChannel; InviteToChannel; PostMessage}` | `NewMockChatNotifyClient()` (`Calls`, `SetError`, `Reset`; `ChatCall{Method, Args}`) |
| `connectors/sqlquery` (not wired) | `New(aliases, httpClient, internalToken) Connector` | — (input `queryAlias`, `params`; output `resultSet`) | — |
| `connectors/documentextract` (not wired) | `New(client ProviderClient) Connector` | `ProviderClient{Analyze(ctx, AnalyzeRequest) (AnalyzeResult, error)}` | `NewMockDocumentExtractClient()` |

**send-email delivery outcome API** (`sendemail/outcome.go`): `Outcome` + `OutcomeAccepted` `"accepted"`, `OutcomeNotDelivered` `"not_delivered"`, `OutcomeUnknown` `"unknown"`; `SendError{Outcome, Provider, StatusCode, Err, IntentUnrecorded}` with `Error`, `Unwrap` (declassified view), `Is` (only `ErrNotDelivered` or `ErrDeliveryUnknown`), `ErrorClass` (unrecorded intent or unknown outcome → unknown; not delivered → provider API code ▸ HTTP status ▸ cause); `NotDelivered(provider, status, err)`, `Unknown(provider, status, err)`; `TraceWrites(ctx) (ctx, *WriteTracker)`, `(*WriteTracker).Wrote()`; `ClassifyAfterSend(provider, err, tracker)`, `ClassifyStatus(provider, status, err)`, `ClassifyTransport(provider, err)`; `OutcomeOf(err) Outcome`.

## 5. Provider adapters

| Package | Constructor | Registry provider | Required input | Notes |
|---|---|---|---|---|
| `storage/gocloud` | `NewProvider(ctx, params) (storage.ProviderClient, error)` | `aws-s3` (default when `provider` empty), `azure-blob`, `gcp-gcs` | aws-s3: `accessKey`, `secretKey`, `region`; azure: `azureAccountName` (`^[a-z0-9]{3,24}$`), `azureAccountKey`, container name rules; gcs: `gcpServiceAccountKey` (validated) | client built from tenant values only; delete of a missing object succeeds; errors classified by cause, then gocloud code |
| `storage/googledrive` | `NewProvider(docs documents.Store) storage.ProviderConstructor` | `google-drive` | `driveServiceAccountKey` (validated); tenant in ctx | nil `docs` → `ErrValidation`; uniqueness from the registry |
| `sendemail/ses` | `NewProvider(ctx, params)` | `aws-ses` | `accessKey`, `secretKey`, `region` | `aws.NopRetryer`; 25 MiB attachment limit |
| `sendemail/sendgrid` | `NewProvider(ctx, params)` | `sendgrid` | `apiKey` | request built per call (no shared mutable state); 20 MiB |
| `sendemail/msgraph` | `NewProvider(ctx, params)` | `microsoft-365` | `tenantId` (GUID or domain), `clientId`, `clientSecret`; `body` | Graph `sendMail`; 3 MiB |
| `sendemail/gmail` | `NewProvider(ctx, params)` | `google-workspace` | `serviceAccountKey` (validated), `senderEmail`; `body` | domain-wide delegation impersonating `senderEmail`; 403 `rateLimitExceeded` / `userRateLimitExceeded` transient; 25 MiB |

## 6. `connectors/aliasconfig`

`Config{Version, RestCall []Endpoint, SQLQuery []Query}`; `Endpoint{Alias, Method, BaseURL, PathTemplate, Timeout}`; `Query{Alias, BaseURL, Path, QueryID, ParamCount, Timeout}` (YAML keys `restCall`, `sqlQuery`, `baseURL`, `pathTemplate`, `queryId`, `paramCount`, …). `Load(path) (Config, error)` (reads, parses, validates); `(Config).Validate()` (aliases unique and non-empty, method valid, `baseURL` absolute http/https with host and no userinfo, `pathTemplate` / `path` start with `/`, `queryId` required, `paramCount ≥ 0`); `IsValidMethod`; `ResolveEndpoint(cfg, alias)`, `ResolveQuery(cfg, alias)`; `ErrUnknownAlias` (the cores wrap it in `ErrValidation`).

## 7. `connectors/docref` (+ `s3content`, `valkeystore`)

| Identifier | Notes |
|---|---|
| `Prefix = "docref:"`, `DefaultTTL = 24 * time.Hour` | |
| `Ref{ID, TenantID, Bucket, ObjectKey, ContentType, Size, SHA256, CreatedAt, UpdatedAt}` | metadata only |
| `Store{Put; Get(ctx, tenantID, id) (Ref, bool, error); Delete}` | `Put` create-only and idempotent for an identical ref; `ErrExists` otherwise |
| `ContentStore{Bucket(); Put(ctx, key, content, contentType); Open(ctx, bucket, key) (io.ReadCloser, int64, error); Delete}` | `Open` of a missing object → `ErrObjectNotFound` |
| `NewService(refs Store, content ContentStore, opts ...ServiceOption) *Service`, `WithKeyPrefix(prefix)` | |
| `(*Service).Create(ctx, tenantID, contentType, content) (Ref, error)`, `Lookup`, `Open(ctx, tenantID, id) (Ref, io.ReadCloser, error)`, `Read(ctx, tenantID, id, maxBytes) (Ref, []byte, error)`, `Delete` | `Open`'s reader returns `ErrIntegrityViolation` instead of `io.EOF` on a size / hash mismatch |
| `ErrNotFound`, `ErrSourceMissing`, `ErrIntegrityViolation`, `ErrTooLarge`, `ErrInvalidTenant` | resolution failures (`IsResolutionFailure`) → `ErrValidation` in the connectors |
| `ErrUnavailable` | transient store failure |
| `ErrExists`, `ErrObjectNotFound` | store-level |
| `IsResolutionFailure(err)`, `IsRef(s)`, `NewID()`, `Checksum(content)` | |
| `NewMemoryStore()`, `NewMemoryStoreWithTTL(ttl)` (`SetClock`, `FailPut`, `Overwrite`), `NewMemoryContent(bucket)` (`Len`), `NewMemoryService()` | unit tests only; not shared across replicas |

**`docref/s3content`:** `API` (PutObject, GetObject, DeleteObject, HeadBucket — the `*s3.Client` subset), `New(client API, bucket string) *Store`, `(*Store).Check(ctx, keyPrefix) error`, plus the `ContentStore` methods.

**`docref/valkeystore`:** `New(client redis.UniversalClient, opts Options) *Store` (`*redis.Client` standalone / Sentinel or `*redis.ClusterClient`; any other type errors at use); `Options{TTL, DisableWaitAOF, WaitAOFTimeout, WaitReplicas}`; `DefaultWaitAOFTimeout = 2 * time.Second`; `ErrNotPersisted` (wraps `docref.ErrUnavailable`); `Key(id)`; `Fields` (the nine hash fields); `CheckDurability(ctx, client) error`; `*DurabilityError{Problems}`; `ErrConfigUnavailable`.

## 8. `connectors/documents` (+ `sqlstore`)

| Identifier | Notes |
|---|---|
| `State` + `StatePendingUpload`, `StateUploading`, `StateAvailable`, `StateFailed`, `StateDeleting`; `State.Busy()` | busy = pending / uploading / deleting |
| `Identity{TenantID, Provider, Container, Filename}`, `Document{ID, Identity, State, Version, ObjectID, ContentType, SizeBytes, Owner, LeaseExpiresAt, ClaimedAt, LastError, FailedAttempts, CreatedAt, UpdatedAt}`, `Result{ObjectID, ContentType, SizeBytes}` | |
| `Store{Claim(ctx, identity, attempt, lease) (Document, bool, error); ClaimExisting; Advance(ctx, docID, attempt, to); Complete(ctx, docID, attempt, Result); Fail(ctx, docID, attempt, reason); Remove(ctx, docID, attempt); Get(ctx, identity)}` | |
| `ErrOwnershipLost`, `ErrNotFound`, `ErrUploadInProgress`, `*InProgressError{DocumentID, State}` (transient; matches `ErrUploadInProgress` and `shared.ErrUpstream`) | |
| `DefaultLease = 15 * time.Minute` | must exceed the worker's per-call timeout |
| `NewMemoryStore()` (`SetClock`, `Attempts() []AttemptRecord`, `Len`) | single process only |

**`documents/sqlstore`:** `New(pool *pgcommon.Pool) *Store`, `ApplySchema(ctx, runner *migrate.Runner) error` (uses only `DSN`, `Logger`, `LockTimeout`), `MigrationsTable = "connector_documents_migrations"`, `(*Store).PruneAttempts(ctx, cutoff time.Time, batch int) (int64, error)`.

## 9. `connectors/sendintent` (+ `sqlstore`)

| Identifier | Notes |
|---|---|
| `Status` + `StatusPending` `"pending"`, `StatusAccepted` `"accepted"`, `StatusNotDelivered` `"not_delivered"`, `StatusUnknown` `"unknown"` | |
| `StalePendingAfter = 15 * time.Minute` | |
| `Intent{ID, TenantID, MessageKey, Status, Attempts, ReservationToken, ProviderMessageID, Detail, CreatedAt, UpdatedAt}` | |
| `Store{Reserve(ctx, tenantID, messageKey string, resendFrom int, token string) (Intent, bool, error); Get(ctx, tenantID, messageKey) (Intent, bool, error); Record(ctx, intentID string, attempt int, status Status, providerMessageID, detail string) error}` | `resendFrom` 0 = no resend |
| `ErrDuplicateRequest`, `ErrIntentNotFound`, `ErrStaleRecord`, `*DuplicateRequestError{IntentID, MessageKey, Status, Attempts}` (matches `ErrDuplicateRequest` and `shared.ErrValidation`) | |
| `NewMemoryStore()` (`SetClock`) | single process only |

**`sendintent/sqlstore`:** `New(pool *pgcommon.Pool) *Store`, `ApplySchema(ctx, runner)`, `MigrationsTable = "connector_send_intents_migrations"`.

## 10. `connectors/shared` (public helpers)

| Area | Identifiers |
|---|---|
| Errors | `ErrValidation`, `ErrMissingInternalAuth`, `ErrUpstream`, `ErrMissingTenant`, `ErrNotDelivered`, `ErrDeliveryUnknown`, `ErrTooLarge`; `Classify(op, err)`, `TooLarge(what, limit)` |
| Classes | `Class` (+ consts, `String`), `Classifier`, `ClassifiedError{Class, Reason, Err}`, `WithClass`, `Transient`, `Permanent`, `ClassOf`, `IsTransient`, `IsPermanent`, `IsRetryable`, `ClassifyHTTPStatus`, `HTTPStatusError`, `ClassifyCause`, `ClassifyByCause` |
| Limits / clients | `MaxObjectBytes`, `MaxInlineBytes`, `MaxResponseBytes`, `MaxAttachmentBytes`, `DefaultHTTPTimeout`, `ProviderHTTPTimeout`, `ReadAllLimited`, `InternalHTTPClient`, `ProviderHTTPClient`, `CallTimeout` |
| HTTP helpers | `InternalTokenHeader` `"x-internal-token"`, `DepartmentsHeader` `"x-departments"`, `RenderPathTemplate`, `NewInternalRequest`, `ApplyQueryParams`, `FlattenHeaders`, `DecodeResponseBody`, `DecodeErrorResponseBody`, `DecodeBody`, `DecodeJSON`, `FormatParam` |
| Fields | `AsMap`, `StringField`, `BoolField`, `StringSliceField` |
| Context | `WithTenant`, `TenantFromContext`, `WithDepartments`, `DepartmentsFromContext`, `DepartmentsHeaderValue` |
| Cache | `ClientCache[C]` (`NewClientCache(limit)`, `Acquire(key, build)`, `Reset`, `Invalidate`, `Len`), `ClientHandle[C]` (`Client`, `Release`) |
| Credentials | `ValidateGoogleServiceAccountKey(raw, field)` |

## 11. Errors a consumer should recognise

| Error | Class | Raised when |
|---|---|---|
| `ErrValidation` (and `DuplicateRequestError`, ref resolution failures, `ErrTooLarge`, unknown alias) | permanent | bad input, missing provider / store configuration, unresolvable ref, over a size limit |
| `ErrMissingInternalAuth`, `ErrMissingTenant` | permanent | worker did not set departments / tenant |
| `ErrUpstream` | from the cause (adapter class, HTTP status, AWS code, network) | provider or internal service failure |
| `*documents.InProgressError` | transient | another call owns the Drive document |
| `*sendemail.SendError` (`ErrNotDelivered` / `ErrDeliveryUnknown`) | not delivered: by status / code / cause; unknown outcome: unknown | every failed send |
| `docref.ErrUnavailable`, `valkeystore.ErrNotPersisted` | transient (wrapped by the connectors) | Valkey refused or could not confirm a write |
| Store DB errors (`sqlstore` `classify`) | transient for serialization failure, deadlock, lock not available, query canceled, operator intervention, connection exception, insufficient resources; unknown for a closed pool | PostgreSQL |
