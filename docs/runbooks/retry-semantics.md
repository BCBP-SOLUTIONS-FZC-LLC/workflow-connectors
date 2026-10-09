# Retry semantics and error classification

Every error a connector returns is classified as exactly one of:

| Class | Meaning | Automatic retry |
|---|---|---|
| **Transient** | The same call may succeed later: throttling, timeouts, 5xx, connection failures, an upload already in progress | **Yes**, if the connector type's retry policy also allows it (below) |
| **Permanent** | Retrying the same input cannot succeed: invalid input, not found, access denied, integrity violation, most 4xx | **Never**. Someone must fix the input, the data or the configuration |
| **Unknown** | The library cannot tell: an unrecognised error, a cancelled call, an email whose delivery outcome is unknown | **Never**, so it is treated like a permanent failure until a person decides |

Classification is deterministic: the same error always gets the same class, and the same error, connector type and method always get the same retry decision.

## For workflow authors: what recovers by itself

| Situation | Class | Recovers automatically? | What to do if it keeps failing |
|---|---|---|---|
| Provider throttling (S3 `SlowDown`, HTTP 429, Drive rate limit) | Transient | Yes, with backoff | Reduce parallelism in the workflow |
| Provider outage (5xx, timeouts, connection refused, DNS) | Transient | Yes, with backoff | Wait for the provider; the task fails after the worker's attempt limit |
| Drive document being uploaded by another task | Transient | Yes | Avoid uploading the same file name from parallel branches |
| Missing input, bad credential, unknown alias | Permanent | No | Fix the task configuration or the tenant's credential |
| Object or file not found on fetch (S3 `NoSuchKey`, HTTP 404, Drive file missing); a `delete` of a missing object or file succeeds (idempotent) | Permanent | No | Check the key or name the workflow passes |
| Access denied (S3 `AccessDenied`, HTTP 401/403) | Permanent | No | Fix the credential's permissions |
| Document ref expired or unknown, its S3 object missing, or the object altered | Permanent | No | Re-run the step that produced the ref |
| Email rejected (invalid recipient, unverified sender, HTTP 4xx) | Permanent | No | Fix the address or the sender setup |
| Email throttled or never reached the provider (429, DNS, connection refused or timed out) | Transient | **Yes**: nothing was sent, so a retry cannot duplicate it | — |
| Email whose outcome is unknown (timeout after sending, 5xx) | Unknown | **No**: it may already have been delivered | Follow [`email-delivery.md`](email-delivery.md): verify, then resend explicitly |
| `rest-call` 5xx/timeout on `POST`/`PATCH` | Transient | **No**: a non-idempotent call may have taken effect | Make the endpoint idempotent, or retry deliberately |
| `chat-notify` failure | any | No (policy `unsafe`) | Retry deliberately |
| Anything the library does not recognise | Unknown | No | Inspect the error; report it so it can be classified |

## The decision: `connectors.DecideRetry`

```go
d := connectors.DecideRetry(connectorType, err, aliasMethod) // aliasMethod: rest-call's HTTP method, else ""
if d.Retry { /* re-run with backoff, up to the attempt limit */ }
logger.LogAttrs(ctx, slog.LevelWarn, "connector task failed", d.LogAttrs()...)
// retry, error_class, error_reason, retry_policy, retry_rule
```

1. Only a **transient** error may be retried. Permanent and unknown never are.
2. The connector type's **retry policy** (`registry.Definition.Retry`) must allow it:

| Type | Policy | A transient failure is retried when… |
|---|---|---|
| `storage` | `safe` | always |
| `rest-call` | `conditional` | the alias's method is idempotent (`GET`, `HEAD`, `PUT`, `DELETE`, `OPTIONS`, `TRACE`) |
| `send-email` | `not-delivered` | the provider provably never accepted the message (`ErrNotDelivered`) |
| `chat-notify` | `unsafe` | never |

`connectors.AutoRetryAllowed(type, err, method)` returns just `d.Retry`. `connectors.ClassOf(err)`, `IsTransient` and `IsPermanent` expose the class directly. There is no `errors.Is` sentinel per class (`ErrTransient`/`ErrPermanent` were removed): `errors.Is` matches a classified error anywhere in the chain, while `ClassOf` applies precedence (invalid input is permanent even when it wraps a transient cause), so the two could disagree.

## Worker obligations

The library never retries, logs or emits metrics itself. The connector worker must:

- **Gate every automatic retry** (retry loop, activity retry policy, queue redelivery) on `DecideRetry`. Nothing else may re-run a failed task automatically.
- **Bound transient retries**: exponential backoff with jitter and a maximum attempt count within the task's timeout. A transient failure can persist; after the limit the task fails.
- **Log every decision** with `d.LogAttrs()` (plus type, tenant, task ID, provider).
- **Count every decision**: `connector_task_failures_total{type, provider, class, retried}` with `class = d.Class.String()` and `retried = d.Retry`. Alert on a rising `class="unknown"` rate, because those errors need classifying or a person.

## Provider mappings

The class is attached where the provider's error is understood: in the adapter or core that made the call. The original error always stays in the chain for `errors.As`.

### S3 (storage `aws-s3`, document content)

AWS error codes (from any error exposing `ErrorCode()`) are checked first, before the HTTP status:

| Code | Class |
|---|---|
| `NoSuchKey`, `NoSuchBucket`, `NotFound`, `AccessDenied`, `InvalidBucketName`, `InvalidAccessKeyId`, `SignatureDoesNotMatch`, `InvalidArgument`, `InvalidRequest`, `EntityTooLarge`, `KeyTooLongError`, `MessageRejected` | Permanent |
| `RequestTimeout`, `RequestTimeTooSkewed`, `SlowDown`, `Throttling`, `ThrottlingException`, `TooManyRequestsException`, `InternalError`, `ServiceUnavailable` | Transient |
| any other code | by HTTP status, then by network cause |

### Azure Blob and GCS (storage `azure-blob`, `gcp-gcs`)

gocloud's portable code, used when no provider code or status decided:

| gocloud code | Class |
|---|---|
| `NotFound`, `PermissionDenied`, `InvalidArgument`, `FailedPrecondition`, `AlreadyExists`, `Unimplemented` | Permanent |
| `ResourceExhausted`, `DeadlineExceeded`, `Internal` | Transient |
| `Canceled`, `Unknown` | Unknown |

### Google Drive (storage `google-drive`)

| Error | Class |
|---|---|
| Reason `rateLimitExceeded`, `userRateLimitExceeded`, `sharingRateLimitExceeded`, `backendError`, `internalError` (often sent as **403**) | Transient |
| Document upload in progress (`documents.InProgressError`) | Transient |
| Document registry and send intents (PostgreSQL, `documents/sqlstore`, `sendintent/sqlstore`): serialization failure (`40001`), deadlock (`40P01`), lock not available (`55P03`), query canceled / statement timeout (`57014`), operator intervention (class `57`), connection exception (class `08`), insufficient resources (class `53`), claim contention with a concurrent delete | Transient |
| Document registry: pool closed (the worker is shutting down; another replica can run the task) | Unknown |
| File not found on fetch, document with no stored file | Permanent |
| Any other status | HTTP table below |

### HTTP (rest-call, sql-query, and any provider status)

Each internal call has its own deadline: the alias's `timeout`, or 30 s when the alias sets none (`shared.CallTimeout`). A timeout longer than 30 s on an alias is honoured. Internal calls (`rest-call`, `sql-query`) and email providers are called over HTTP/1.1 only: Go's HTTP/2 client replays a request body after a `PROTOCOL_ERROR` stream reset, which could deliver an email twice or repeat a non-idempotent `rest-call`.

| Status | Class |
|---|---|
| 408, 425, 429, 500, 502, 503, 504 | Transient |
| 3xx (redirects are never followed), 400, 401, 403, 404 and every other 4xx | Permanent |
| Other 5xx (501, 505, 507, …) | Unknown |

A `sql-query` response that cannot be decoded is permanent: the endpoint broke its contract. A `rest-call` error status is classified by the status alone: its body is read without failing (cut to 10 MiB, `bodyTruncated: true`), so an oversized or cut-off error page never turns a transient `503` into a permanent error.

`sql-query` and `document-extract` are implemented but not registered in `registry.All()` (not wired into `connectors.New`), so `DecideRetry` answers `unknown connector type` for them and **never retries them automatically**. Their errors are still classified, for logs and metrics; registering a type gives it a retry policy.

### Email (send-email)

The library sends email through provider HTTP APIs (SendGrid, SES, Microsoft Graph, Gmail), not SMTP. Whether a failed send could have been accepted is decided from evidence: each adapter traces whether the transport began writing the HTTP request (`sendemail.TraceWrites`: its first header field). A failure before that — whatever the error type, including a deadline that expired while connecting — is `not_delivered`; a failure after it, even part-way through the request, is `unknown`. The SMTP rules map onto this as follows:

| SMTP rule | API equivalent | Delivery outcome | Class | Retried |
|---|---|---|---|---|
| Connection timeout | Connect timeout (dial, or the call's deadline while connecting): the request was never written | `not_delivered` | Transient | **Yes** |
| DNS failure | DNS lookup failed | `not_delivered` | Transient | **Yes** |
| Temporary 4xx | 429 throttling, 408, an AWS throttling code, Gmail 403 `rateLimitExceeded`/`userRateLimitExceeded` | `not_delivered` | Transient | **Yes** |
| Invalid recipient address | 400 (e.g. SES `MessageRejected`, SendGrid invalid `to`) | `not_delivered` | Permanent | No |
| Permanent 5xx mailbox rejection | Other provider 4xx rejections (401, 403, 413, …) | `not_delivered` | Permanent | No |
| — | 425 Too Early | `not_delivered` | Transient | **Yes** |
| — | OAuth token request failed (Graph, Gmail): nothing was sent | `not_delivered` | By the token endpoint's status: 400/401 permanent, 429/5xx transient; unreachable → transient | Transient: **Yes** |
| — | Timeout or reset once writing the request **began** (even part-way through the body); any 5xx; any 3xx (redirects are not followed, and a provider redirect may follow acceptance) | `unknown` | Unknown | **No**: it may have been delivered |

A retried send with a `messageKey` re-reserves its send intent only when that intent's last outcome was `not_delivered`; any other earlier outcome still needs the explicit `resend` with `resendAttempt` (the intent's current attempt; a redelivered resend is then a duplicate). A duplicate of an `accepted` intent succeeds without sending. If the outcome could not be recorded on the intent (its store was unavailable), the intent stays `pending` and the failure is class **unknown** (reason `send intent not recorded`): no automatic retry, because it would be refused as a duplicate — verify delivery, then resend explicitly (a pending intent can be resent once it has been unchanged for 15 minutes).

### Document references

| Failure | Error | Class |
|---|---|---|
| Missing (or expired, or another tenant's) Valkey reference | `docref.ErrNotFound` | Permanent |
| Missing S3 object | `docref.ErrSourceMissing` | Permanent |
| Checksum or size mismatch, or a record whose object key is not the ref's own `<prefix><tenant>/<uuid>` | `docref.ErrIntegrityViolation` | Permanent |
| Network timeout or reset reading the object, S3 throttling or 5xx | — | Transient |
| Valkey full (`noeviction` refusing writes: `OOM`), a write not confirmed in its AOF (or by `WaitReplicas` replicas), or a node refusing during failover or resharding (`READONLY`, `MASTERDOWN`, `NOREPLICAS`, `LOADING`, `MOVED`/`ASK`, `TRYAGAIN`, `CLUSTERDOWN`) | `docref.ErrUnavailable` | Transient: a retry creates a new ref |
| Valkey or S3 unreachable | — | Transient (network cause) |

### Network failures (any provider)

| Failure | Class |
|---|---|
| Deadline exceeded, an i/o timeout | Transient |
| DNS lookup failure | Transient |
| Connection refused, connection failed while dialing | Transient |
| Connection reset, broken pipe, unexpected EOF | Transient |
| Context cancelled | Unknown: the worker stopped the call (shutdown, its own timeout policy) |

### Library errors

| Error | Class |
|---|---|
| `ErrValidation` (missing or invalid input, unknown alias or provider, oversize payload, duplicate `messageKey`) | Permanent |
| `ErrMissingTenant`, `ErrMissingInternalAuth` (worker wiring) | Permanent |
