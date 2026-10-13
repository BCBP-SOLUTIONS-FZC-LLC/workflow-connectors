# API Reference

Module `github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2`. Every package under `pkg/` is public API under SemVer (`VERSIONING.md`). Import paths below are relative to the module (`…/v2/pkg/…`).

## 1. API Conventions

- **Connector contract:** `Execute(ctx, input map[string]any) (map[string]any, error)`. Input keys are the registry field names (`pkg/registry/definitions.go`). The output map can be non-nil **with** an error: `send-email` always returns `deliveryOutcome`; `rest-call` returns `method` with every result once the request was sent (transport errors included), and `status`, `headers`, `body` (and `bodyTruncated`; `body` is then the first 10 MiB, or what arrived, as a string) for a status ≥ 300 or a 2xx over 10 MiB.
- **Context carries the call identity:** `connectors.WithTenant` (document refs, Drive registry, send intents) and `connectors.WithDepartments` (`rest-call`, `sql-query`). Missing → `ErrMissingTenant` / `ErrMissingInternalAuth` (permanent).
- **Credentials arrive resolved in the input.** Fields with `Kind == FieldKindSecretRef` are resolved from OpenBao by the worker; the library never reads secrets or environment variables (the GCS SDK it builds honours `STORAGE_EMULATOR_HOST`, which the worker must never set).
- **Every error has exactly one class** (transient / permanent / unknown), read with `connectors.ClassOf`. There are no per-class `errors.Is` sentinels. Sentinels below classify *kind*, not retryability.
- **Numbers:** `rest-call` / `sql-query` decode JSON numbers as `json.Number`; `shared.StringField`, `StringSliceField` and `FormatScalarParam` accept them.
- **Concurrency:** connectors are safe for concurrent `Execute`; keep the map `connectors.New` returns for the process lifetime (each call builds new client caches).

## 2. `pkg/registry` (dependency-free)

| Identifier | Notes |
|---|---|
| `TypeStorage` `"storage"`, `TypeSendEmail` `"send-email"`, `TypeRestCall` `"rest-call"`, `TypeChatNotify` `"chat-notify"`, `TypeDocumentExtract` `"document-extract"`, `TypeSQLQuery` `"sql-query"` | the last two have experimental connectors but no `Definition`; chat-notify's definition is outside `All()` |
| `FieldKind` + `FieldKindString`, `Bool`, `Int`, `Float`, `Enum`, `List`, `Map`, `Any`, `DocRef` (`"document_ref"`), `SecretRef` (`"secret_ref"`), `Timestamp` | |
| `RetryPolicy` + `RetryPolicySafe` (storage), `RetryPolicyUnsafe` (chat-notify), `RetryPolicyConditional` (rest-call), `RetryPolicyNotDelivered` (send-email) | |
| `IsIdempotentMethod(method) bool` | GET, HEAD, PUT, DELETE, OPTIONS, TRACE (case-insensitive) |
| `Field{Name, Kind, Required, EnumValues, Condition, Description}`, `Field.IsSecretRef()` | |
| `Definition{Type, DisplayName, Description, Inputs, Outputs, Retry}` | |
| `All() map[string]Definition` | the authorable types: storage, send-email, rest-call; panics on a duplicate type. `provider` is `Required` for storage and send-email |
| `ChatNotifyDefinition() Definition` | chat-notify's definition, deliberately not in `All()` (no provider, no per-call credential in its port) |
| `Lookup(connectorType) (Definition, bool)` | `All()` plus chat-notify (built by `connectors.New`); what `DecideRetry` uses. Definitions are built once (package-level map); each call returns a copy (`Inputs`, `Outputs`, `EnumValues` cloned), so callers may modify it |

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
func New(cfg Config) (map[string]Connector, error) // error for an empty InternalToken or cfg.Aliases.Validate() failing; storage / send-email values implement interface{ ResetClients() }
```

| Identifier | Notes |
|---|---|
| `ErrValidation`, `ErrMissingInternalAuth`, `ErrUpstream`, `ErrMissingTenant`, `ErrNotDelivered`, `ErrDeliveryUnknown` | aliases of the `shared` sentinels |
| `ErrorClass` (= `shared.Class`), `ClassUnknown`, `ClassTransient`, `ClassPermanent` | `Class.String()`: `"unknown"`, `"transient"`, `"permanent"` |
| `ClassOf(err) (ErrorClass, string)`, `IsTransient`, `IsPermanent`, `IsRetryable` (= transient) | |
| `RetryDecision{Retry, Class, Reason, Policy, Rule}`, `RetryDecision.LogAttrs() []slog.Attr` | attrs: `retry`, `error_class`, `error_reason`, `retry_policy`, `retry_rule` |
| `DecideRetry(connectorType string, err error, method string) RetryDecision` | `method` = rest-call's output `method` (the HTTP method sent); `""` otherwise. Policy from `registry.Lookup` |
| `AutoRetryAllowed(connectorType, err, method) bool` | `DecideRetry(…).Retry` |
| `WithTenant`, `TenantFromContext`, `WithDepartments`, `DepartmentsFromContext` | re-export `shared` |

`DecideRetry` rules (`Rule` values): unknown type → `"unknown connector type"`; permanent → `"permanent"`; unknown class → `"unknown"`; transient + `safe` → `"transient"`; + `conditional` → `"transient"` if `IsIdempotentMethod(method)` else `"non-idempotent method"`; + `not-delivered` → `"transient, not delivered"` if `errors.Is(err, ErrNotDelivered)` else `"may have been delivered"`; any other policy (`unsafe`) → `"policy unsafe"`.

## 4. Connector cores, ports and mocks

| Package | Constructor | Port | Mock (tests only) |
|---|---|---|---|
| `connectors/storage` | `New(providers map[string]ProviderConstructor, docRefs *docref.Service) *Connector`; `(*Connector).ResetClients()` | `ProviderClient{Fetch(ctx, bucket, key string, maxBytes int64) ([]byte, string, error); Upload(ctx, bucket, key string, content []byte, contentType string) error; Delete(ctx, bucket, key string) error}`; `ProviderConstructor func(ctx, params map[string]any) (ProviderClient, error)` | `NewMockStorageClient()` (`SetError`, `Reset`). Input: `provider` required; `bucket` / `key` ≤ 1024 bytes, UTF-8, no control characters |
| `connectors/sendemail` | `New(providers map[string]ProviderConstructor, docRefs *docref.Service, opts ...Option) *Connector`; `WithSendIntents(store) Option`; `ResetClients()` | `ProviderClient{Send(ctx, EmailMessage) (messageID string, err error)}`; optional `AttachmentLimiter{MaxAttachmentBytes() int64}`; `EmailMessage`, `EmailAttachment`. Input: `contentType` `""` / `text/plain` / `text/html` only; `messageKey` ≤ 512 bytes, UTF-8, no control chars; `resendAttempt` 1..2147483647; `body` ≤ 5 MiB, `subject` ≤ 998 bytes. With an intent reserved, the pre-send stage is bounded to 11 min 50 s (unexported `preSendBudget`); a post-reservation failure whose outcome cannot be recorded is always a `SendError` with `IntentUnrecorded` (class unknown) | `NewMockSendEmailClient()` (`Sent`, `SetError`, `Reset`) |
| `connectors/restcall` | `New(aliases aliasconfig.Config, httpClient *http.Client, internalToken string) Connector` (path / query params must be scalars); `ReasonResponseTooLarge = shared.ReasonResponseTooLarge` (`"response too large"`; ClassOf reason, permanent, of a 2xx over `MaxResponseBytes`; wraps `ErrUpstream` + `ErrTooLarge`, not `ErrValidation`) | — | — |
| `connectors/chatnotify` | `New(client ProviderClient) Connector` | `ProviderClient{CreateChannel; InviteToChannel; PostMessage}` | `NewMockChatNotifyClient()` (`Calls`, `SetError`, `Reset`; `ChatCall{Method, Args}`) |
| `connectors/sqlquery` (not wired; **experimental**, no tenant scoping; `paramCount` exact, non-list `params` → `ErrValidation`; a 2xx body over 10 MiB → permanent `ErrUpstream` + `ErrTooLarge`, reason `shared.ReasonResponseTooLarge`, not `ErrValidation`) | `New(aliases, httpClient, internalToken) Connector` | — (input `queryAlias`, `params`; output `resultSet`) | — |
| `connectors/documentextract` (not wired; **experimental**, no tenant scoping, inline refs not docref-verified) | `New(client ProviderClient) Connector` | `ProviderClient{Analyze(ctx, AnalyzeRequest) (AnalyzeResult, error)}` | `NewMockDocumentExtractClient()` |

**send-email delivery outcome API** (`sendemail/outcome.go`): `Outcome` + `OutcomeAccepted` `"accepted"`, `OutcomeNotDelivered` `"not_delivered"`, `OutcomeUnknown` `"unknown"`; `SendError{Outcome, Provider, StatusCode, Err, IntentUnrecorded}` with `Error`, `Unwrap` (declassified view), `Is` (only `ErrNotDelivered` or `ErrDeliveryUnknown`), `ErrorClass` (unrecorded intent or unknown outcome → unknown; not delivered → provider API code ▸ HTTP status ▸ cause); `NotDelivered(provider, status, err)`, `Unknown(provider, status, err)`; `TraceWrites(ctx) (ctx, *WriteTracker)`, `(*WriteTracker).Wrote()`; `ClassifyAfterSend(provider, err, tracker)`, `ClassifyStatus(provider, status, err)`, `ClassifyTransport(provider, err)`; `OutcomeOf(err) Outcome`.

## 5. Provider adapters

| Package | Constructor | Registry provider | Required input | Notes |
|---|---|---|---|---|
| `storage/gocloud` | `NewProvider(ctx, params) (storage.ProviderClient, error)` | `aws-s3` (the adapter's own default for an empty `provider`; the storage core requires `provider`), `azure-blob`, `gcp-gcs` | aws-s3: `accessKey`, `secretKey`, `region`; azure: `azureAccountName` (`^[a-z0-9]{3,24}$`), `azureAccountKey`, container name rules; gcs: `gcpServiceAccountKey` (validated) | client built from tenant values only, own transport (`shared.BaseTransport()`, GCS included), `ProviderHTTPTimeout` (2 min) client timeout, redirects never followed, **SDK retries off** (S3 `aws.NopRetryer`, Azure `MaxRetries: -1`, GCS `RetryNever`: one request per call); keys gocloud would hex-escape refused as `ErrValidation` before any request (all: control chars, `../`; azure also `"` `#` `%` `?` `\` DEL, trailing `/`); Azure 409 `SnapshotsPresent` on delete permanent with an explanatory message (snapshots never deleted); delete succeeds only on a provably missing object (S3 `NoSuchKey` or 404 + `HeadBucket`, Azure `BlobNotFound`, GCS 404 + one-object list); missing bucket / region, 403 are errors; classified by Azure / GCS HTTP status (`ClassifyHTTPStatus`), Azure NXDOMAIN unknown, then cause (AWS code, status, network), then gocloud code |
| `storage/googledrive` | `NewProvider(docs documents.Store) storage.ProviderConstructor` | `google-drive` | `driveServiceAccountKey` (validated); tenant in ctx | nil `docs` → `ErrValidation`; uniqueness from the registry; by-name lookups skip folders, shortcuts and Google-native files; a recorded file that is trashed or moved out of the folder (checked with one `files.get` of `trashed,parents` before update / download / delete) is treated as gone: upload and fetch fall back, delete leaves it alone; one tenant per Drive folder |
| `sendemail/ses` | `NewProvider(ctx, params)` | `aws-ses` | `accessKey`, `secretKey`, `region` (one lower-case DNS label) | `aws.NopRetryer`; 25 MiB attachment limit |
| `sendemail/sendgrid` | `NewProvider(ctx, params)` | `sendgrid` | `apiKey` | request built per call (no shared mutable state); 20 MiB |
| `sendemail/msgraph` | `NewProvider(ctx, params)` | `microsoft-365` | `tenantId` (GUID or domain), `clientId`, `clientSecret`; `body` | Graph `sendMail`; attachments ≤ 2,900,000 bytes raw, whole encoded request ≤ 4,000,000 bytes (JSON not HTML-escaped); token client without redirects |
| `sendemail/gmail` | `NewProvider(ctx, params)` | `google-workspace` | `serviceAccountKey` (validated), `senderEmail`; `body` | domain-wide delegation impersonating `senderEmail`; message uploaded as `message/rfc822` (media upload, one request); attachment content types normalised (`mime.ParseMediaType`, else `application/octet-stream`); `Subject` folded within 998-character lines; 403 `rateLimitExceeded` / `userRateLimitExceeded` transient; attachments ≤ 17,000,000 bytes raw, encoded message ≤ 25,000,000 bytes; token client without redirects |

## 6. `connectors/aliasconfig`

`Config{Version, RestCall []Endpoint, SQLQuery []Query}`; `Endpoint{Alias, Method, BaseURL, PathTemplate, Timeout}`; `Query{Alias, BaseURL, Path, QueryID, ParamCount, Timeout}` (YAML keys `restCall`, `sqlQuery`, `baseURL`, `pathTemplate`, `queryId`, `paramCount`, …). `Load(path) (Config, error)` (reads, parses strictly — unknown keys and a second YAML document with content are errors; empty extra documents (a trailing `---`, optionally with comments) are ignored; an empty file is an empty config — validates); `(Config).Validate()` (aliases unique and non-empty, method valid, `baseURL` absolute http/https with host and no userinfo, query or fragment, `pathTemplate` / `path` start with `/`, no `;` in a `pathTemplate`'s query (which must also parse: a bad escape such as `%zz` is refused) (Go's query parser would drop that pair and hide a fixed key), `queryId` required, `paramCount ≥ 0`); `IsValidMethod`; `ResolveEndpoint(cfg, alias)`, `ResolveQuery(cfg, alias)`; `ErrUnknownAlias` (the cores wrap it in `ErrValidation`).

## 7. `connectors/docref` (+ `s3content`, `valkeystore`)

| Identifier | Notes |
|---|---|
| `Prefix = "docref:"`, `DefaultTTL = 24 * time.Hour` | |
| `Ref{ID, TenantID, Bucket, ObjectKey, ContentType, Size, SHA256, CreatedAt, UpdatedAt}` | metadata only |
| `Store{Put; Get(ctx, tenantID, id) (Ref, bool, error); Delete}` | `Put` create-only and idempotent for an identical ref; `ErrExists` otherwise |
| `ContentStore{Bucket(); Put(ctx, key, content, contentType); Open(ctx, bucket, key) (io.ReadCloser, int64, error); Delete}` | `Open` of a missing object → `ErrObjectNotFound` |
| `NewService(refs Store, content ContentStore, opts ...ServiceOption) *Service`, `WithKeyPrefix(prefix)` | |
| `(*Service).Create(ctx, tenantID, contentType, content) (Ref, error)`, `Lookup`, `Open(ctx, tenantID, id) (Ref, io.ReadCloser, error)`, `Read(ctx, tenantID, id, maxBytes) (Ref, []byte, error)`, `Delete` | `Create` cleans up ref then object (5 s each, detached) on a failed metadata write; `Open`'s reader returns `ErrIntegrityViolation` instead of `io.EOF` on a size / hash mismatch; `Delete` of a tampered ref removes the record and (best effort) its canonical `<prefix><tenant>/<uuid>` object, never the object the record names, and returns nil |
| `ErrNotFound`, `ErrSourceMissing`, `ErrIntegrityViolation`, `ErrTooLarge`, `ErrInvalidTenant` | resolution failures (`IsResolutionFailure`) → `ErrValidation` in the connectors |
| `ErrUnavailable` | transient store failure |
| `ErrExists`, `ErrObjectNotFound` | store-level |
| `IsResolutionFailure(err)`, `IsRef(s)`, `NewID()`, `Checksum(content)` | |
| `NewMemoryStore()`, `NewMemoryStoreWithTTL(ttl)` (`SetClock`, `FailPut`, `Overwrite`), `NewMemoryContent(bucket)` (`Len`), `NewMemoryService()` | unit tests only; not shared across replicas |

**`docref/s3content`:** `API` (PutObject, GetObject, DeleteObject, HeadBucket — the `*s3.Client` subset), `New(client API, bucket string) *Store`, `(*Store).Check(ctx, keyPrefix) error` (HeadBucket, missing-key probe, put + delete probe under the prefix), plus the `ContentStore` methods.

**`docref/valkeystore`:** `New(client redis.UniversalClient, opts Options) *Store` (`*redis.Client` standalone / Sentinel or `*redis.ClusterClient`; any other type errors at use); `Options{TTL, DisableWaitAOF, WaitAOFTimeout, WaitReplicas}` (sub-ms durations rounded up to 1 ms), `(Options).Validate() error`, `ErrInvalidOptions` (negative `WaitReplicas`; such a store refuses every `Put`); `DefaultWaitAOFTimeout = 2 * time.Second`; `ErrNotPersisted` (wraps `docref.ErrUnavailable`); errors: capacity / topology replies (`HasErrorPrefix`), `OOM`, `NOREPLICAS`, pool timeout, unloadable slot map → `docref.ErrUnavailable`; other replies (ACL `NOPERM`, incl. a refused `ASKING` — which replaces the command's error only when the command failed) class unknown; a cluster node failing without a reply (not the caller's context) schedules a slot-map reload; one `MOVED` / `ASK` redirect followed per operation; `Key(id)`; `Fields` (the nine hash fields); `CheckDurability(ctx, client) error`; `*DurabilityError{Problems}`; `ErrConfigUnavailable` (matched only when every node's failure is a reply refusing `CONFIG GET` itself — `NOPERM`, `ERR unknown command` / `unknown subcommand`; `WRONGPASS`, `LOADING` and other replies are failures — check `*DurabilityError` first).

## 8. `connectors/documents` (+ `sqlstore`)

| Identifier | Notes |
|---|---|
| `State` + `StatePendingUpload`, `StateUploading`, `StateAvailable`, `StateFailed`, `StateDeleting`; `State.Busy()` | busy = pending / uploading / deleting |
| `Identity{TenantID, Provider, Container, Filename}`, `Document{ID, Identity, State, Version, ObjectID, ContentType, SizeBytes, Owner, LeaseExpiresAt, ClaimedAt, LastError, FailedAttempts, CreatedAt, UpdatedAt}`, `Result{ObjectID, ContentType, SizeBytes}` | |
| `Store{Claim(ctx, identity, attempt, lease) (Document, bool, error); ClaimExisting; Advance(ctx, docID, attempt, to); Complete(ctx, docID, attempt, Result); Fail(ctx, docID, attempt, reason); Remove(ctx, docID, attempt); Get(ctx, identity)}` | |
| `ErrOwnershipLost`, `ErrNotFound`, `ErrUploadInProgress`, `*InProgressError{DocumentID, State}` (transient; matches `ErrUploadInProgress` and `shared.ErrUpstream`) | |
| `DefaultLease = 15 * time.Minute` | must exceed the worker's per-call timeout |
| `NewMemoryStore()` (`SetClock`, `Attempts() []AttemptRecord`, `Len`) | single process only |

**`documents/sqlstore`:** `New(pool *pgcommon.Pool) *Store`, `ApplySchema(ctx, runner *migrate.Runner) error` (uses only `DSN`, `Logger`, `LockTimeout`), `MigrationsTable = "connector_documents_migrations"`, `(*Store).PruneAttempts(ctx, cutoff time.Time, batch int) (int64, error)`. `Fail` stores at most 1 KiB of error text (valid UTF-8, no NUL).

## 9. `connectors/sendintent` (+ `sqlstore`)

| Identifier | Notes |
|---|---|
| `Status` + `StatusPending` `"pending"`, `StatusAccepted` `"accepted"`, `StatusNotDelivered` `"not_delivered"`, `StatusUnknown` `"unknown"` | |
| `StalePendingAfter = 15 * time.Minute` | |
| `Intent{ID, TenantID, MessageKey, Status, Attempts, ReservationToken, ProviderMessageID, Detail, CreatedAt, UpdatedAt}` | |
| `Store{Reserve(ctx, tenantID, messageKey string, resendFrom int, token string) (Intent, bool, error); Get(ctx, tenantID, messageKey) (Intent, bool, error); Record(ctx, intentID string, attempt int, status Status, providerMessageID, detail string) error}` | `resendFrom` 0 = no resend |
| `ErrDuplicateRequest`, `ErrIntentNotFound`, `ErrStaleRecord`, `*DuplicateRequestError{IntentID, MessageKey, Status, Attempts}` (matches `ErrDuplicateRequest` and `shared.ErrValidation`) | |
| `Releaser{Release(ctx, tenantID, messageKey, token string) (released bool, err error)}` | optional `Store` capability: releases a reservation by its token (one conditional write; never another caller's reservation or a recorded outcome); send-email uses it after an ambiguous Reserve error when the read-back does not show its own pending row; only `released == true` lets the error keep its class |
| `ErrNotReserved` | wraps a Reserve failure that provably reserved nothing (anything before COMMIT was sent: acquire, BEGIN, transaction setup, the statement); send-email skips recovery and keeps the error's class |
| `ReleasedDetail` | the `Detail` `Release` records |
| `ErrNoToken` | `Release` without a token |
| `ErrPruneArgs` | `PruneIntents` with a non-positive age or batch |
| `NewMemoryStore()` (`SetClock`), `(*MemoryStore).Release`, `(*MemoryStore).PruneIntents(ctx, olderThan time.Duration, batch int) (int64, error)` | single process only |

**`sendintent/sqlstore`:** `New(pool *pgcommon.Pool) *Store`, `(*Store).Release(ctx, tenantID, messageKey, token) (bool, error)`, `(*Store).PruneIntents(ctx, olderThan time.Duration, batch int) (int64, error)` (deletes finished intents last updated more than `olderThan` ago, at most `batch` per call; never pending ones; repeat while it returns `batch`), `ApplySchema(ctx, runner)` (migrations `000001`–`000005`), `MigrationsTable = "connector_send_intents_migrations"`.

## 10. `connectors/shared` (public helpers)

| Area | Identifiers |
|---|---|
| Errors | `ErrValidation`, `ErrMissingInternalAuth`, `ErrUpstream`, `ErrMissingTenant`, `ErrNotDelivered`, `ErrDeliveryUnknown`, `ErrTooLarge`; `Classify(op, err)`, `TooLarge(what, limit)`; `ReasonResponseTooLarge` (`"response too large"`, the permanent ClassOf reason of a rest-call / sql-query 2xx over `MaxResponseBytes`) |
| Classes | `Class` (+ consts, `String`), `Classifier`, `ClassifiedError{Class, Reason, Err}`, `WithClass`, `Transient`, `Permanent`, `ClassOf`, `IsTransient`, `IsPermanent`, `IsRetryable`, `ClassifyHTTPStatus`, `HTTPStatusError`, `ClassifyCause`, `ClassifyByCause` |
| Limits / clients | `MaxObjectBytes`, `MaxInlineBytes`, `MaxResponseBytes`, `MaxAttachmentBytes`, `DefaultHTTPTimeout`, `ProviderHTTPTimeout`, `ReadAllLimited`, `BaseTransport` (safe `*http.Transport` base even when `http.DefaultTransport` is wrapped), `InternalHTTPClient` (default transport: no proxy), `ProviderHTTPClient`, `CallTimeout` |
| HTTP helpers | `InternalTokenHeader` `"x-internal-token"`, `DepartmentsHeader` `"x-departments"`, `RenderPathTemplate`, `NewInternalRequest`, `ApplyQueryParams` (with params given: parses the alias query with `url.ParseQuery` — unparsable → `ErrValidation`; a fixed key → `ErrValidation`; alias query kept verbatim, params appended sorted), `FlattenHeaders`, `DecodeResponseBody`, `DecodeErrorResponseBody`, `DecodeBody`, `DecodeJSON`, `FormatScalarParam` (non-scalar → `ErrValidation`), `FormatParam` (deprecated) |
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
| `ErrUpstream` + `ErrTooLarge`, reason `shared.ReasonResponseTooLarge` (= `restcall.ReasonResponseTooLarge`) | permanent | a rest-call or sql-query 2xx response body over 10 MiB (the call was processed; rest-call's output keeps `status`, `headers`, `method`, `bodyTruncated`) |
| `*documents.InProgressError` | transient | another call owns the Drive document |
| `*sendemail.SendError` (`ErrNotDelivered` / `ErrDeliveryUnknown`) | not delivered: by status / code / cause; unknown outcome: unknown | every failed send |
| `docref.ErrUnavailable`, `valkeystore.ErrNotPersisted` | transient (wrapped by the connectors) | Valkey refused or could not confirm a write |
| Store DB errors (`sqlstore` `classify`) | transient for serialization failure, deadlock, lock not available, query canceled, operator intervention, connection exception, insufficient resources; unknown for a closed pool | PostgreSQL |
