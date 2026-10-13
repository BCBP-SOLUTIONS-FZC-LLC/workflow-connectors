# Runtime Flows, Concurrency & Failure

All flows run inside the worker's `Execute` call; the library starts no goroutine of its own outside a call and keeps no state besides the provider-client caches. Error classes are given as *class* (transient / permanent / unknown).

## 1. Construction — `connectors.New(cfg)`

1. Empty `InternalToken` → error (rest-call sends it on every request); `cfg.Aliases.Validate()` failing → error (a zero `Aliases` is valid).
2. Builds `storage.New(cfg.StorageProviders, cfg.DocRefs)`, `sendemail.New(cfg.SendEmailProviders, cfg.DocRefs, sendemail.WithSendIntents(cfg.SendIntents))`, `chatnotify.New(cfg.ChatNotifyClient)`, `restcall.New(cfg.Aliases, cfg.HTTPClient, cfg.InternalToken)`.
3. `restcall.New` wraps the client with `shared.InternalHTTPClient` (copy, no redirects, HTTP/1.1 only; nil client or transport → `shared.BaseTransport()` with no proxy and no client-wide timeout; a wrapped `http.DefaultTransport` never panics).
4. Returns `map[type]Connector`; a duplicate `Type()` panics (`indexByType`). Each call creates fresh client caches (limit 256 each for storage and send-email).

## 2. Storage — `storage.(*Connector).Execute`

Common prefix: `bucket` and `key` required, each at most 1024 bytes, valid UTF-8 and free of control characters (NUL, CR, LF, DEL, C1) (`ErrValidation`, before any client is built, so a bad name never reaches a provider or the Drive registry) → `clientFor`: `provider` required and configured (`ErrValidation`) → `ClientCache.Acquire(sha256(provider, bucket, every credential field))`, building with the `ProviderConstructor` on a miss (constructor errors through `shared.Classify`, so a missing credential stays `ErrValidation`) → `defer handle.Release()` → dispatch on `operation` (`fetch` / `upload` / `delete`, else `ErrValidation`).

### 2.1 Fetch

1. With `createDocument: true`: `refTenant` requires `Config.DocRefs` (`ErrValidation`) and `WithTenant` (`ErrMissingTenant`) **before** any provider call.
2. `client.Fetch(ctx, bucket, key, limit)` with `limit` = `MaxInlineBytes` (1 MiB) inline, `MaxObjectBytes` (50 MiB) for a document. Inline + `shared.ErrTooLarge` → `ErrValidation` ("set createDocument"); other errors → `shared.Classify("storage fetch", err)`.
3. Output `contentType`, `sizeBytes`, `fetchedAt` (UTC).
4. `createDocument`: `docRefs.Create(ctx, tenant, contentType, content)` → `contentRef`. Ref errors via `classifyRef`: resolution failure → `ErrValidation` (keeps the docref sentinel); `docref.ErrUnavailable` → transient; else by cause.
5. Inline: valid UTF-8 → `content` + `contentEncoding: "utf-8"`; otherwise base64 + `"base64"`.

### 2.2 Upload

1. `content` required. If it is a ref (`docref.IsRef`): tenant + `DocRefs` required, then `docRefs.Read(ctx, tenant, ref, MaxObjectBytes)` resolves and **verifies** it (size, SHA-256) before anything is uploaded; `contentType` defaults to the ref's. Otherwise `contentEncoding` `""`/`utf-8` or `base64` (decoded; invalid → `ErrValidation`).
2. More than 50 MiB → `ErrValidation`, checked **before** the content is copied or decoded: inline text by its length, base64 by its exact decoded size (`base64DecodedSize`: line breaks and padding excluded), a ref by `Read`'s limit.
3. `client.Upload(...)` → errors via `Classify`.
4. Output `sizeBytes`; plus `contentRef` = the input ref when the content was a ref, or a new ref only when `createDocument: true` (created **after** the upload succeeded). A plain upload creates no ref.

### 2.3 Delete

`client.Delete(ctx, bucket, key)`; output `{}`. On `aws-s3` / `azure-blob` / `gcp-gcs` deleting a missing object **in a bucket that exists** succeeds. gocloud maps a missing bucket, a wrong S3 region (301), a GCS 403 and an unknown Azure account (DNS "no such host") to the same `NotFound` code, so the adapter checks the SDK error itself (`objectMissing`): S3 `NoSuchKey`, or a bare HEAD 404 `NotFound` confirmed by a successful `HeadBucket`; Azure `BlobNotFound` only (`ContainerNotFound` is an error); GCS a 404 confirmed by listing at most one object (`storage.objects.list`, granted by every object role; the message is never relied on). Anything else is an error (missing bucket / forbidden / wrong region permanent; an Azure account host that does not resolve is unknown; a failing bucket check — labelled `aws-s3 bucket check (HeadBucket)` / `gcp-gcs bucket check (objects.list)` — classified as itself). An S3 403 says the credential needs `s3:ListBucket`.

### 2.4 gocloud adapter specifics

S3 clients are built from the tenant's `accessKey` / `secretKey` / `region` only (no `config.LoadDefaultConfig`), each with its own transport (from `shared.BaseTransport()`) so `Close()` drops only its idle connections. Azure account and container names are pattern-checked before going into the URL. GCS keys pass `ValidateGoogleServiceAccountKey`; token requests use a background context with a 30 s client; the GCS client never follows redirects. Error class (`classify`): an Azure `*azcore.ResponseError` or GCS `*googleapi.Error` by its HTTP status (`ClassifyHTTPStatus`: 408/429/5xx transient, 403 and other 4xx permanent) — neither exposes a status `ClassifyCause` reads; a DNS NXDOMAIN for `*.blob.core.windows.net` **unknown** (usually a misspelt account, but private-endpoint / split-horizon DNS outages also answer NXDOMAIN, so it is never retried automatically nor declared permanent); then `ClassifyCause` (AWS code, HTTP status, network), then the gocloud portable code (NotFound / PermissionDenied / InvalidArgument / FailedPrecondition / AlreadyExists / Unimplemented permanent; ResourceExhausted / DeadlineExceeded / Internal transient).

## 3. Google Drive — `storage/googledrive`

`identityFor` requires `WithTenant`; the identity is `{tenant, "google-drive", folderID, name}`. `attempt` is a fresh UUID; `claimedAt` is taken **before** the claim.

**One tenant per Drive folder.** The registry is per tenant, so two tenants pointed at the same folder each get their own row for the same name, and both may adopt (update and tag) the same hand-placed untagged file, or one may delete what the other just adopted. Configure each tenant with its own folder (or shared drive); the library cannot detect a shared one.

**Clocks.** Leases are set by the database (`now()` in the SQL store), but the Drive write deadline (`leaseDeadline`) and fetch's "is this lease live?" check use the **worker's** clock. Worker hosts and the database must be NTP-synchronised; the 1-minute `leaseMargin` absorbs ordinary skew, a skew near it lets a write outlive its lease.

**Names, not items.** Every lookup by name (`untaggedByName`) considers content files only: the `listByName` query excludes folders and shortcuts, and the code skips every `application/vnd.google-apps.*` type (folders, shortcuts, Docs, Sheets, …), so a delete never removes a folder by name (Drive's delete bypasses the trash), an upload never adopts one and a fetch never tries to download one.

### 3.1 Upload

1. `docs.Claim(identity, attempt, DefaultLease)`. Not claimed (a live owner) → `*documents.InProgressError` (transient); Drive is never touched.
2. `Advance(→ UPLOADING)`; on error `release` (Fail on a detached 10 s context) and return.
3. `write` under `leaseDeadline` = `claimedAt + (lease − 1 min)` (or `lease/2` if that is ≤ 0), so a write never outlives its ownership:
   1. the recorded `ObjectID` → `update` (a 404 falls through);
   2. else the oldest file tagged `appProperties.connectorDocumentId = doc.ID` (left by a crashed attempt), else the oldest **untagged** file of that name (hand-placed; adopted) → `update` (tags it), then remove other files tagged with this document;
   3. else `create` (tagged), then remove other tagged files (a create cut off by the deadline may have completed).
4. `Complete(result)` on a detached 10 s context; if it fails, `release` so a retry can claim at once and adopt the written file by its tag.

### 3.2 Fetch

`docs.Get(identity)`; a row is *live* when busy and its `LeaseExpiresAt` is after the worker's `time.Now()`:
- live and `DELETING`, or live without a recorded file (a first upload) → `InProgressError` (transient);
- a recorded `ObjectID` → download it (an idle row, a live re-upload, or a busy row whose lease expired); if Drive reports it missing (deleted outside the connector), fall through to the next rule, as `write` does;
- otherwise — no row, a `FAILED` row that never wrote, or a crashed upload / delete whose lease expired — the file a retried upload would adopt: the oldest file tagged with the row's ID, else the oldest untagged content file of the name; none → permanent "drive file not found". A crashed call therefore never makes fetch report "in progress" past its lease, and fetch reads the same untagged file delete would remove.

A file tagged by another document is never read by name.

### 3.3 Delete

Always `Claim` (inserting a row if none, so a delete can never race an upload adopting the same file) → `Advance(→ DELETING)` → `deleteFiles` under the lease deadline (recorded file + every file tagged with the document; if neither, the oldest untagged same-name file; already-gone files count as deleted) → `Remove` on a detached 10 s context (a failed `Remove` releases the row so a retry finds nothing left and removes it). Deleting an absent document succeeds.

### 3.4 Classification

A class already attached wins (`InProgressError`, store `classify`); `documents.ErrOwnershipLost` → transient (a retry converges); Drive API reasons `rateLimitExceeded`, `userRateLimitExceeded`, `sharingRateLimitExceeded`, `backendError`, `internalError` → transient (often 403); else the HTTP status; else the network cause. Input errors stay `ErrValidation`, never `ErrUpstream`.

## 4. send-email — `sendemail.(*Connector).Execute`

Every return goes through `finish`, which sets `sent`, `deliveryOutcome` (`OutcomeOf(err)`, defaulting to `not_delivered` for non-delivery errors such as validation) and, on success, `messageId` + `sentAt`.

1. **Validate:** `provider` present and configured (`constructorFor`, so a missing or unknown provider never reserves an intent); `senderEmail` and `receiverEmail` required and each a single bare address (`net/mail.ParseAddress`, no display name); `templateId` or `body`; `body` required for `microsoft-365` / `google-workspace`; `contentType` only `""`, `text/plain` or `text/html` (the registry enum).
2. **Send intent** (only with `messageKey`) — reserved **before** attachments are resolved, so a redelivered job for an accepted intent is a duplicate success even if its attachment refs have expired, and nothing is downloaded again. `messageKey` ≤ 512 bytes, valid UTF-8, no control characters; `resendAttempt` parsed (`resend: true` requires `resendAttempt ≥ 1`; at most `math.MaxInt32`; without `resend` it is `ErrValidation`; int, int64, whole float64 or `json.Number`) — all `ErrValidation` before the store is touched. Needs `Config.SendIntents` (`ErrValidation`) and a tenant. `Reserve(ctx, tenant, key, resendFrom, token=uuid)`:
   - error matching `sendintent.ErrNotReserved` (the store knows nothing was reserved: `sqlstore` marks a failure of the statement itself, so `COMMIT` was never sent, and a failed read-back after an upsert that changed nothing) → `NotDelivered("send-intent store", …)` with the cause's class, no read-back.
   - any other error is **ambiguous** (the commit may have happened, its reply lost) → `recoverReservation`: read back (detached, 10 s) and proceed only if the row carries **this call's token** and is still `pending`. Otherwise → `releaseReservation` (store implements `sendintent.Releaser`; detached, 10 s): `Release` sets the row `not_delivered` by token while still `pending` and reports `released`. Released → `NotDelivered` with the cause's class (a transient failure may be retried). Anything else — not found, another caller's token, release matched nothing, release failed, or no `Releaser` → `NotDelivered` with `IntentUnrecorded` (class unknown, never retried automatically): not finding the row is no proof it did not commit (with synchronous replication an interrupted commit becomes visible later), and a retry would meet that `pending` row as a duplicate.
   - not reserved → `duplicateResult`: an `accepted` intent returns **success** with `sent: false, duplicate: true, status, attempts, providerMessageId, messageId` (= `providerMessageId`), `sentAt` (the intent's `UpdatedAt`, when the outcome was recorded) and `deliveryOutcome: "accepted"`; any other status returns `sent, duplicate, sendIntentId, status, attempts, deliveryOutcome` **with** `*sendintent.DuplicateRequestError` (permanent).
3. **Attachments:** `resolveAttachments` (needs `DocRefs` and a tenant) calls `docRefs.Read(ctx, tenant, ref, MaxAttachmentBytes − total)` for each ref, so the 25 MiB total is enforced before download; too large / resolution failure → `ErrValidation`; a store outage → `NotDelivered("document-ref store", 0, …)` (transient when `docref.ErrUnavailable`). Either is recorded `not_delivered` on the intent. Filenames are synthesised: `attachment-<n><ext>` from the content type.
4. **Client:** `clientFor` (cache key covers `senderEmail` + every email credential field). A constructor failure that is not `ErrValidation` is `NotDelivered(provider, 0, err)` (never `ErrUpstream`). `aws-ses` requires `region` to be one lower-case DNS label (it becomes part of the endpoint host).
5. **Provider limit:** if the client implements `AttachmentLimiter` with a limit below 25 MiB (SendGrid 20 MiB, Microsoft 365 2,900,000 bytes), the total raw bytes are checked → `ErrValidation`. Microsoft 365 also refuses, before sending, a `sendMail` request over 4,000,000 bytes as encoded (body + base64 attachments) → `NotDelivered`, permanent.
6. `ctx.Err()` before sending → `NotDelivered` (cancelled, nothing written).
7. `client.Send(ctx, msg)`; error → `classifySend` (keeps an adapter's `ErrNotDelivered` / `ErrDeliveryUnknown`; anything else, including a mid-send cancellation, → unknown). **Panics:** once an intent is reserved, `Execute` defers one `guard.recordPanic`: a panic before the provider call (attachments, client constructor, limiter) records `not_delivered`, one in or after `Send` records `unknown`, then the panic is re-raised (a bug is never hidden, and the intent is not left `pending`). A panic inside `finish` itself (in `Record`) is re-raised without a second record.
8. **Record:** `intents.Record(intentID, attempt, status=outcome, messageID, detail)` on `context.WithoutCancel` + 10 s. `detail` and the message ID are made storable first (the output `messageId` is the same sanitised value, so the worker can store the output as `jsonb`, which refuses `\u0000`) (`recordable`: invalid UTF-8 replaced, NUL removed, cut to 4 KiB on a rune boundary — PostgreSQL text refuses invalid UTF-8 and NUL). A failure never turns a successful send into an error; it adds `sendIntentWarning` and, for a failed send, marks `SendError.IntentUnrecorded` (class unknown: no automatic retry, since the retry would be refused as a duplicate).

**Retention:** `sqlstore.(*Store).PruneIntents(ctx, olderThan, batch)` (and `MemoryStore.PruneIntents`) deletes finished (non-`pending`) intents last updated more than `olderThan` ago, `batch` rows per call. Pruning **reopens the duplicate window**: a request with a pruned key is a new message and is sent again, so keep intents longer than any redelivery or replay can arrive. The SQL is `DELETE … WHERE id IN (SELECT id … WHERE status <> 'pending' AND updated_at < cutoff LIMIT $2 FOR UPDATE SKIP LOCKED) AND status <> 'pending' AND updated_at < cutoff`: under READ COMMITTED a row re-reserved concurrently is re-checked only against the DELETE's own `WHERE`, so the conditions are repeated there (without them the now-`pending` intent was deleted and a redelivered job sent the email again); rows another transaction holds are skipped. Served by the partial index `ix_connector_send_intents_prune`.

**Sender identity:** `senderEmail` is **not** bound to the credential. A Graph app with `Mail.Send` application permission, or a Google service account with domain-wide delegation, can send as any mailbox in the tenant's organisation; the library sends as whatever `senderEmail` the workflow gives. Customers restrict this at the provider (Exchange Online Application Access Policy / RBAC for Applications scoped to the allowed mailboxes; a narrowly scoped delegation for the service account).

### 4.1 Outcome classification in adapters

Adapters send with `ctx, tracker := sendemail.TraceWrites(ctx)` (marks on the first header field written). No response: `ClassifyAfterSend` → not delivered if nothing was written (DNS, dial, TLS, connect timeout), otherwise unknown. A response: `ClassifyStatus` → 4xx (including 429) not delivered, everything else (5xx) unknown. OAuth token failures (Gmail, Graph) are classified by the token endpoint's status (400/401 permanent, 429/5xx transient; unreachable transient) as not delivered. Gmail 403 `rateLimitExceeded` / `userRateLimitExceeded` → transient not delivered. SES runs with `aws.NopRetryer`; SendGrid builds each request itself (`POST /v3/mail/send`) with no shared mutable state. `SendError.ErrorClass` for not delivered: provider API code (`"api …"`) ▸ HTTP status ▸ cause.

## 5. rest-call — `restcall.Connector.Execute`

1. `endpointAlias` required; `aliasconfig.ResolveEndpoint` (unknown → `ErrValidation` wrapping `ErrUnknownAlias`).
2. `shared.DepartmentsHeaderValue(ctx)` — missing or malformed → `ErrMissingInternalAuth` before any request.
3. `RenderPathTemplate(pathTemplate, pathParams)` (values must be scalars — `FormatScalarParam`, a list / map / nil is `ErrValidation`; path-escaped, no empty / dot segments; query placeholders query-escaped); `body` JSON-encoded if present.
4. `context.WithTimeout(ctx, CallTimeout(alias.Timeout))` (alias timeout, else 30 s).
5. `NewInternalRequest` pins scheme + host to the alias `baseURL`, no userinfo; `ApplyQueryParams` parses the alias query strictly (`url.ParseQuery`), refuses overriding a fixed key, keeps the alias query verbatim and appends scalar params sorted by key; headers `x-internal-token`, `x-departments`, `Content-Type: application/json` with a body.
6. From here every result carries `method` (upper-case; the worker passes it to `DecideRetry`). Transport error → output `{method}` + `Classify` (network cause). Status ≥ 300 (redirects are never followed) → output `{status, method, headers (Set-Cookie dropped), body}` (+ `bodyTruncated: true` when the error body was > 10 MiB or cut off) **and** `ErrUpstream` + `HTTPStatusError` (class from the status alone). 2xx → body decoded (`json.Number` for JSON content types, else string). A 2xx over 10 MiB → output with the first 10 MiB as text and `bodyTruncated: true`, **and** a permanent `ErrUpstream` + `ErrTooLarge` (reason `ReasonResponseTooLarge`, "response too large"), not `ErrValidation`: the service processed the call.

`sql-query` (not wired, experimental) follows the same path with `POST {queryId, params}` to the alias `path`; `params` must be a list (or absent) of exactly `paramCount` values (`ErrValidation`); it decodes `{resultSet}` (malformed → permanent; over 10 MiB → permanent `ErrUpstream` + `ErrTooLarge`, no output).

## 6. Document refs — `docref.Service`

**Create** (`storage` fetch / upload with `createDocument`): `validTenant` → `id = docref:<uuid>`, `key = <prefix><tenant>/<uuid>` → `content.Put` (S3) **first** → `refs.Put` (Valkey: create-only Lua + `WAITAOF` pipelined on the key's node; `valkeystore.ErrNotPersisted` if the AOF did not confirm in time, or fewer than `WaitReplicas` replicas confirmed) → on a metadata failure (or an ambiguous one), delete the ref (harmless if absent) and then the object, each on its own detached 5 s context, and return the error. On a cluster, `Put` / `Get` / `Delete` follow one `MOVED` (one synchronous slot-map reload per burst, coalesced across operations, then retry on `MasterForKey`) or `ASK` (no reload: the importing node is found in a host:port cache of the masters, refreshed only on a miss and at most once a second; `ASKING` + command pipelined on it, for `Put` with the `WAITAOF`; a refused `ASKING` such as `NOPERM` is returned as is, not transient). A returned ref always points at an existing, durable record.

**Resolve** (`Lookup` → `Open` / `Read`): `validTenant` → ID must be `docref:` + a UUID (else `ErrNotFound` without a lookup) → `refs.Get` (master read; missing, expired or other-tenant → `ErrNotFound`) → the record must have the same tenant and ID, the service's bucket and exactly `objectKey(tenant, id)`, a non-negative size and a 64-hex SHA-256 (else `ErrIntegrityViolation`) — all **before** any S3 call → `Read` refuses `Size > maxBytes` (`ErrTooLarge`) before downloading → `content.Open` (`NoSuchKey` → `ErrSourceMissing`; a known length ≠ `Size` → `ErrIntegrityViolation`) → `verifyingReader` counts and hashes; reading past `Size`, or EOF with a different size or hash, returns `ErrIntegrityViolation` instead of `io.EOF`.

**Delete:** `Lookup` (not found → nil; `ErrIntegrityViolation` → `refs.Delete`, then a best-effort delete of the canonical `objectKey(tenant, id)` in the service's bucket — never the record's bucket/key — nil) → `refs.Delete` (tenant-checked script) → `content.Delete` (a failure returns an error saying the ref is gone and the object is left for the lifecycle rule). **Expiry:** Valkey TTL (24 h) for the ref; the bucket lifecycle rule (2 days) for the object.

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
| Reservation token | a caller whose `Reserve` reply was lost proceeds only if the committed reservation is its own; if it cannot even read it back, it releases the reservation by token (`Release`: `pending` + own token only) or reports a non-retryable failure |
| Valkey create-only Lua + `WAITAOF` on the same connection | one ref per ID; an acknowledged ref is in the primary's AOF (and `WaitReplicas` replicas' with that option) |
| `pgcommon.RunInTx` per statement | statement and lock timeouts bound every store call |
| Detached, bounded contexts (`recordTimeout` 10 s in send-email and Drive, `cleanupTimeout` 5 s per step in docref) | outcomes are recorded and rows released even after the caller's context ends, without hanging on a dead dependency |
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
| Worker crash mid-Drive-upload | lease expires (15 min); the next claim adopts the tagged file; until then a fetch is `InProgressError`, after it a fetch reads past the crashed row (tagged file, else the untagged one) |
| Drive delete of a name that is only a folder / shortcut / Google Doc | succeeds and removes nothing: by-name lookups consider content files only |
| `gocloud` delete in a missing bucket / container, wrong S3 region, forbidden | permanent error, never "already deleted" |
| Worker crash mid-send with a `messageKey` | intent stays `pending`; an explicit resend naming the attempt can take it over after 15 min (`StalePendingAfter`) |
| Valkey full (`OOM`), failover / topology replies, `BUSY`, maxclients, pool timeout, AOF not confirmed | `docref.ErrUnavailable` / `ErrNotPersisted` → transient |
| Valkey ACL refuses the ref write (`NOPERM`, e.g. no `+asking` during a slot migration) | class unknown, not retried; the S3 object is deleted again (`TestIT10_…`) |
| Cluster slot migration (`ASK` / `MOVED`) | followed once; refs keep working (`TestCluster_SlotMigration_PutGetDelete`) |
| send-email `Reserve` fails ambiguously (reply lost) | read back: own pending row → send; else released by token → `not_delivered` with the cause's class; else `IntentUnrecorded`, class unknown |
| Panic after the reservation | intent recorded (`not_delivered` before `Send`, `unknown` from it on), panic re-raised |
| Valkey primary failover with `WaitReplicas` 0 | an acknowledged ref can be lost; the consuming step fails with not found (runbook: re-run from the producing step) |
| PostgreSQL lock or statement timeout, deadlock, connection loss | transient; pool closed (shutdown) → unknown |
