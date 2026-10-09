# Runtime Flows, Concurrency & Failure

All flows run inside the worker's `Execute` call; the library starts no goroutine of its own outside a call and keeps no state besides the provider-client caches. Error classes are given as *class* (transient / permanent / unknown).

## 1. Construction — `connectors.New(cfg)`

1. Empty `InternalToken` → error (rest-call sends it on every request).
2. Builds `storage.New(cfg.StorageProviders, cfg.DocRefs)`, `sendemail.New(cfg.SendEmailProviders, cfg.DocRefs, sendemail.WithSendIntents(cfg.SendIntents))`, `chatnotify.New(cfg.ChatNotifyClient)`, `restcall.New(cfg.Aliases, cfg.HTTPClient, cfg.InternalToken)`.
3. `restcall.New` wraps the client with `shared.InternalHTTPClient` (copy, no redirects, HTTP/1.1 only, nil → default transport clone without client-wide timeout).
4. Returns `map[type]Connector`; a duplicate `Type()` panics (`indexByType`). Each call creates fresh client caches (limit 256 each for storage and send-email).

## 2. Storage — `storage.(*Connector).Execute`

Common prefix: `bucket` and `key` required (`ErrValidation`) → `clientFor`: `provider` required and configured (`ErrValidation`) → `ClientCache.Acquire(sha256(provider, bucket, every credential field))`, building with the `ProviderConstructor` on a miss (constructor errors through `shared.Classify`, so a missing credential stays `ErrValidation`) → `defer handle.Release()` → dispatch on `operation` (`fetch` / `upload` / `delete`, else `ErrValidation`).

### 2.1 Fetch

1. With `createDocument: true`: `refTenant` requires `Config.DocRefs` (`ErrValidation`) and `WithTenant` (`ErrMissingTenant`) **before** any provider call.
2. `client.Fetch(ctx, bucket, key, limit)` with `limit` = `MaxInlineBytes` (1 MiB) inline, `MaxObjectBytes` (50 MiB) for a document. Inline + `shared.ErrTooLarge` → `ErrValidation` ("set createDocument"); other errors → `shared.Classify("storage fetch", err)`.
3. Output `contentType`, `sizeBytes`, `fetchedAt` (UTC).
4. `createDocument`: `docRefs.Create(ctx, tenant, contentType, content)` → `contentRef`. Ref errors via `classifyRef`: resolution failure → `ErrValidation` (keeps the docref sentinel); `docref.ErrUnavailable` → transient; else by cause.
5. Inline: valid UTF-8 → `content` + `contentEncoding: "utf-8"`; otherwise base64 + `"base64"`.

### 2.2 Upload

1. `content` required. If it is a ref (`docref.IsRef`): tenant + `DocRefs` required, then `docRefs.Read(ctx, tenant, ref, MaxObjectBytes)` resolves and **verifies** it (size, SHA-256) before anything is uploaded; `contentType` defaults to the ref's. Otherwise `contentEncoding` `""`/`utf-8` or `base64` (decoded; invalid → `ErrValidation`).
2. More than 50 MiB → `ErrValidation`.
3. `client.Upload(...)` → errors via `Classify`.
4. Output `sizeBytes`; plus `contentRef` = the input ref when the content was a ref, or a new ref only when `createDocument: true` (created **after** the upload succeeded). A plain upload creates no ref.

### 2.3 Delete

`client.Delete(ctx, bucket, key)`; output `{}`. On `aws-s3` / `azure-blob` / `gcp-gcs` deleting a missing object succeeds (`gcerrors.NotFound` ignored).

### 2.4 gocloud adapter specifics

S3 clients are built from the tenant's `accessKey` / `secretKey` / `region` only (no `config.LoadDefaultConfig`), each with its own cloned transport so `Close()` drops only its idle connections. Azure account and container names are pattern-checked before going into the URL. GCS keys pass `ValidateGoogleServiceAccountKey`; token requests use a background context with a 30 s client; the GCS client never follows redirects. Error class: `ClassifyCause` (AWS code, HTTP status, network) first, then the gocloud portable code (NotFound / PermissionDenied / InvalidArgument / FailedPrecondition / AlreadyExists / Unimplemented permanent; ResourceExhausted / DeadlineExceeded / Internal transient).

## 3. Google Drive — `storage/googledrive`

`identityFor` requires `WithTenant`; the identity is `{tenant, "google-drive", folderID, name}`. `attempt` is a fresh UUID; `claimedAt` is taken **before** the claim.

### 3.1 Upload

1. `docs.Claim(identity, attempt, DefaultLease)`. Not claimed (a live owner) → `*documents.InProgressError` (transient); Drive is never touched.
2. `Advance(→ UPLOADING)`; on error `release` (Fail on a detached 10 s context) and return.
3. `write` under `leaseDeadline` = `claimedAt + (lease − 1 min)` (or `lease/2` if that is ≤ 0), so a write never outlives its ownership:
   1. the recorded `ObjectID` → `update` (a 404 falls through);
   2. else the oldest file tagged `appProperties.connectorDocumentId = doc.ID` (left by a crashed attempt), else the oldest **untagged** file of that name (hand-placed; adopted) → `update` (tags it), then remove other files tagged with this document;
   3. else `create` (tagged), then remove other tagged files (a create cut off by the deadline may have completed).
4. `Complete(result)` on a detached 10 s context; if it fails, `release` so a retry can claim at once and adopt the written file by its tag.

### 3.2 Fetch

`docs.Get(identity)`: recorded `ObjectID` → download it; a busy row without a file → `InProgressError`; an idle row without a file → permanent; no row → the oldest untagged file of that name (else permanent "drive file not found"). A file tagged by another document is never read by name.

### 3.3 Delete

Always `Claim` (inserting a row if none, so a delete can never race an upload adopting the same file) → `Advance(→ DELETING)` → `deleteFiles` under the lease deadline (recorded file + every file tagged with the document; if neither, the oldest untagged same-name file; already-gone files count as deleted) → `Remove` on a detached 10 s context (a failed `Remove` releases the row so a retry finds nothing left and removes it). Deleting an absent document succeeds.

### 3.4 Classification

A class already attached wins (`InProgressError`, store `classify`); `documents.ErrOwnershipLost` → transient (a retry converges); Drive API reasons `rateLimitExceeded`, `userRateLimitExceeded`, `sharingRateLimitExceeded`, `backendError`, `internalError` → transient (often 403); else the HTTP status; else the network cause. Input errors stay `ErrValidation`, never `ErrUpstream`.

## 4. send-email — `sendemail.(*Connector).Execute`

Every return goes through `finish`, which sets `sent`, `deliveryOutcome` (`OutcomeOf(err)`, defaulting to `not_delivered` for non-delivery errors such as validation) and, on success, `messageId` + `sentAt`.

1. **Validate:** `senderEmail` and `receiverEmail` required and each a single bare address (`net/mail.ParseAddress`, no display name); `templateId` or `body`; `body` required for `microsoft-365` / `google-workspace`.
2. **Attachments:** `resolveAttachments` (needs `DocRefs` and a tenant) calls `docRefs.Read(ctx, tenant, ref, MaxAttachmentBytes − total)` for each ref, so the 25 MiB total is enforced before download; too large / resolution failure → `ErrValidation`; a store outage → `NotDelivered("document-ref store", 0, …)` (transient when `docref.ErrUnavailable`). Filenames are synthesised: `attachment-<n><ext>` from the content type.
3. **Send intent** (only with `messageKey`): needs `Config.SendIntents` (`ErrValidation`) and a tenant; `resendAttempt` parsed (`resend: true` requires `resendAttempt ≥ 1`; `resendAttempt` without `resend` is `ErrValidation`; int, int64, whole float64 or `json.Number`). `Reserve(ctx, tenant, key, resendFrom, token=uuid)`:
   - error → `recoverReservation`: read back (detached, 10 s) and proceed only if the row carries **this call's token** and is still `pending`; else `NotDelivered("send-intent store", …)` — nothing sent;
   - not reserved → `duplicateResult`: an `accepted` intent returns **success** with `sent: false, duplicate: true, status, attempts, providerMessageId, deliveryOutcome: "accepted"`; any other status returns the same shape (no `providerMessageId`) **with** `*sendintent.DuplicateRequestError` (permanent).
4. **Client:** `clientFor` (cache key covers `senderEmail` + every email credential field). A constructor failure that is not `ErrValidation` is `NotDelivered(provider, 0, err)` (never `ErrUpstream`).
5. **Provider limit:** if the client implements `AttachmentLimiter` with a limit below 25 MiB (SendGrid 20 MiB, Microsoft 365 3 MiB), the total raw bytes are checked → `ErrValidation`.
6. `ctx.Err()` before sending → `NotDelivered` (cancelled, nothing written).
7. `client.Send(ctx, msg)`; error → `classifySend` (keeps an adapter's `ErrNotDelivered` / `ErrDeliveryUnknown`; anything else, including a mid-send cancellation, → unknown).
8. **Record:** `intents.Record(intentID, attempt, status=outcome, messageID, detail)` on `context.WithoutCancel` + 10 s. A failure never turns a successful send into an error; it adds `sendIntentWarning` and, for a failed send, marks `SendError.IntentUnrecorded` (class unknown: no automatic retry, since the retry would be refused as a duplicate).

### 4.1 Outcome classification in adapters

Adapters send with `ctx, tracker := sendemail.TraceWrites(ctx)` (marks on the first header field written). No response: `ClassifyAfterSend` → not delivered if nothing was written (DNS, dial, TLS, connect timeout), otherwise unknown. A response: `ClassifyStatus` → 4xx (including 429) not delivered, everything else (5xx) unknown. OAuth token failures (Gmail, Graph) are classified by the token endpoint's status (400/401 permanent, 429/5xx transient; unreachable transient) as not delivered. Gmail 403 `rateLimitExceeded` / `userRateLimitExceeded` → transient not delivered. SES runs with `aws.NopRetryer`; SendGrid builds each request itself (`POST /v3/mail/send`) with no shared mutable state. `SendError.ErrorClass` for not delivered: provider API code (`"api …"`) ▸ HTTP status ▸ cause.

## 5. rest-call — `restcall.Connector.Execute`

1. `endpointAlias` required; `aliasconfig.ResolveEndpoint` (unknown → `ErrValidation` wrapping `ErrUnknownAlias`).
2. `shared.DepartmentsHeaderValue(ctx)` — missing or malformed → `ErrMissingInternalAuth` before any request.
3. `RenderPathTemplate(pathTemplate, pathParams)` (path-escaped, no empty / dot segments; query placeholders query-escaped); `body` JSON-encoded if present.
4. `context.WithTimeout(ctx, CallTimeout(alias.Timeout))` (alias timeout, else 30 s).
5. `NewInternalRequest` pins scheme + host to the alias `baseURL`, no userinfo; `ApplyQueryParams` refuses overriding a template key; headers `x-internal-token`, `x-departments`, `Content-Type: application/json` with a body.
6. Transport error → `Classify` (network cause). Status ≥ 300 (redirects are never followed) → output `{status, headers (Set-Cookie dropped), body}` (+ `bodyTruncated: true` when the error body was > 10 MiB or cut off) **and** `ErrUpstream` + `HTTPStatusError` (class from the status alone). 2xx → body decoded (`json.Number` for JSON content types, else string; > 10 MiB → `ErrValidation` via `TooLarge`).

`sql-query` (not wired) follows the same path with `POST {queryId, params}` to the alias `path`, checks `paramCount`, and decodes `{resultSet}` (malformed → permanent).

## 6. Document refs — `docref.Service`

**Create** (`storage` fetch / upload with `createDocument`): `validTenant` → `id = docref:<uuid>`, `key = <prefix><tenant>/<uuid>` → `content.Put` (S3) **first** → `refs.Put` (Valkey: create-only Lua + `WAITAOF` pipelined on the key's node; `valkeystore.ErrNotPersisted` if the AOF did not confirm in time, or fewer than `WaitReplicas` replicas confirmed) → on a metadata failure, delete the object on a detached 10 s context and return the error. A returned ref always points at an existing, durable record.

**Resolve** (`Lookup` → `Open` / `Read`): `validTenant` → ID must be `docref:` + a UUID (else `ErrNotFound` without a lookup) → `refs.Get` (master read; missing, expired or other-tenant → `ErrNotFound`) → the record must have the same tenant and ID, the service's bucket and exactly `objectKey(tenant, id)`, a non-negative size and a 64-hex SHA-256 (else `ErrIntegrityViolation`) — all **before** any S3 call → `Read` refuses `Size > maxBytes` (`ErrTooLarge`) before downloading → `content.Open` (`NoSuchKey` → `ErrSourceMissing`; a known length ≠ `Size` → `ErrIntegrityViolation`) → `verifyingReader` counts and hashes; reading past `Size`, or EOF with a different size or hash, returns `ErrIntegrityViolation` instead of `io.EOF`.

**Delete:** `Lookup` (not found → nil) → `refs.Delete` (tenant-checked script) → `content.Delete` (failure left to the lifecycle rule). **Expiry:** Valkey TTL (24 h) for the ref; the bucket lifecycle rule (2 days) for the object.

## 7. Retry decision — `connectors.DecideRetry(type, err, method)`

`ClassOf(err)` → unknown connector type: no retry → permanent or unknown class: no retry → transient: by `registry.RetryPolicy` — `safe` (storage) yes; `conditional` (rest-call) only if `IsIdempotentMethod(method)`; `not-delivered` (send-email) only if `errors.Is(err, ErrNotDelivered)` (provably never accepted); `unsafe` (chat-notify) never. The worker must call it before **every** automatic retry (loop, activity retry policy, queue redelivery), bound attempts with backoff, and log `LogAttrs()`. A redelivered send-email job with a `messageKey` whose intent is `accepted` succeeds without sending.

## 8. Concurrency Model

| Mechanism | Guarantee |
|---|---|
| `shared.ClientCache` (mutex + per-entry refcount, LRU by an acquire clock) | a client is closed exactly once, only after its last handle is released; `ResetClients` / `Invalidate` / eviction never close a client under a running call; two concurrent misses on one key keep the first stored client and close the other |
| Cache key = SHA-256 of provider + bucket / sender + every credential field | a client is shared only by identical credential sets, never across tenants or senders |
| `uq_connector_documents_identity` + single-statement `Claim` upsert | at most one row per Drive document; exactly one of concurrent claims wins; a concurrent delete cannot slip between insert and takeover |
| Owner + live-lease CAS on every transition, `leaseDeadline` on Drive work | a call whose lease expired gets `ErrOwnershipLost` and records nothing; its Drive write is cut off before the lease ends |
| `uq_connector_send_intents_key` + single-statement `Reserve` upsert | exactly one of concurrent requests with one `messageKey` reserves; a resend is a CAS on `attempts`, so a redelivered resend is a duplicate |
| `Record` conditional on `attempts` + `pending` | a late outcome of a superseded attempt never overwrites a newer one (`ErrStaleRecord`) |
| Reservation token | a caller whose `Reserve` reply was lost proceeds only if the committed reservation is its own |
| Valkey create-only Lua + `WAITAOF` on the same connection | one ref per ID; an acknowledged ref is in the primary's AOF (and `WaitReplicas` replicas' with that option) |
| `pgcommon.RunInTx` per statement | statement and lock timeouts bound every store call |
| Detached, bounded contexts (`recordTimeout` 10 s in send-email and Drive, `cleanupTimeout` 10 s in docref) | outcomes are recorded and rows released even after the caller's context ends, without hanging on a dead dependency |
| HTTP/1.1-only provider and internal clients | no silent HTTP/2 body replay after a stream reset |

## 9. Failure Scenarios (selected)

| Scenario | Result |
|---|---|
| Ref created on replica A, resolved on replica B | resolves (shared Valkey + S3; `TestMultiReplica_WriteOnA_ReadOnB`) |
| Source object deleted between tasks | `ErrSourceMissing` → `ErrValidation`; nothing sent or uploaded (`TestIT02_…`) |
| Source object overwritten | `ErrIntegrityViolation`, never served (`TestIT03_…`) |
| Ref expired / another tenant's | `ErrNotFound`, permanent (`TestIT05_…`, `TestREG06_…`) |
| Email response lost after the provider accepted it | unknown, not retried; a redelivered job with the same `messageKey` is refused as a duplicate (`TestIT04_…`, `TestWorker_EmailLostResponse_NotRetried_DuplicateRefused`) |
| Email throttled (429) | not delivered + transient → retried (`TestIT01_…`, `TestWorker_EmailThrottledThenAccepted_RetriedOnce`) |
| Two simultaneous Drive uploads of one name | one writes, the other gets `InProgressError` and never touches Drive |
| Worker crash mid-Drive-upload | lease expires (15 min); the next claim adopts the tagged file |
| Worker crash mid-send with a `messageKey` | intent stays `pending`; an explicit resend naming the attempt can take it over after 15 min (`StalePendingAfter`) |
| Valkey full (`OOM`), failover replies, AOF not confirmed | `docref.ErrUnavailable` / `ErrNotPersisted` → transient |
| Valkey primary failover with `WaitReplicas` 0 | an acknowledged ref can be lost; the consuming step fails with not found (runbook: re-run from the producing step) |
| PostgreSQL lock or statement timeout, deadlock, connection loss | transient; pool closed (shutdown) → unknown |
