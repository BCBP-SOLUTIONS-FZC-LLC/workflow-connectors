# Runbook — send-email delivery outcomes

## What the system guarantees

| Situation | Guarantee |
|---|---|
| A send that is not retried | **At-most-once**: the email is delivered once or not at all |
| A send retried after an uncertain outcome | **Effectively at-least-once**: the email may be delivered twice |
| Exactly-once delivery | **Not provided, by any component** |

None of the providers (SendGrid, Amazon SES, Microsoft Graph, Gmail) accepts a caller-supplied idempotency key on send. When a send times out or its response is lost, nobody — not the connector, not the worker, not the provider API — can tell whether the provider accepted it. `send-email` is therefore **not idempotent**, and it is **retried automatically only when that cannot duplicate the message**:

- its registry retry policy is `not-delivered`: `connectors.DecideRetry("send-email", …)` allows a retry only for a **transient** failure the provider **provably never accepted** (`ErrNotDelivered`): DNS failure, connection refused or connect timeout, 429/408 or an AWS throttling code;
- a permanent `not_delivered` failure (invalid recipient, unverified sender, bad credential: provider 4xx) is never retried; fix the cause;
- an `unknown` outcome (timeout or reset after sending, 5xx) is never retried automatically;
- a retry with a `messageKey` re-reserves its intent only if the intent's last outcome was `not_delivered`;
- a request whose provider write had **started** when it failed (even part-way through the body) is `unknown`, not `not_delivered`: only failures before the first byte (DNS, dial, TLS handshake) are provably not delivered;
- the Amazon SES client is built with the AWS SDK's retries **disabled** (the SDK's default would resend `SendEmail` up to 3 times on throttling, 5xx and timeouts); SendGrid, Gmail and Graph are called once per send.

Classification of every failure: [`retry-semantics.md`](retry-semantics.md).

## The three outcomes

Every send-email call reports `deliveryOutcome` in its output — also when it fails. A failure before anything was sent (invalid input, an attachment over a limit, a document-ref store outage, a send-intent store failure, a client that could not be built) reports `not_delivered`. A duplicate request (below) reports the existing intent's outcome.

| `deliveryOutcome` | Error | Meaning | Duplicate if resent? |
|---|---|---|---|
| `accepted` | none | The provider accepted the message (2xx) | **Yes** — it was sent |
| `not_delivered` | `ErrNotDelivered` (or `ErrValidation` for invalid input) | The provider definitively did not accept it: rejected (4xx, including 429 throttling and Gmail's 403 `rateLimitExceeded`/`userRateLimitExceeded`), or the request never left (connection refused, DNS or TLS failure, cancelled before sending, client could not be built, input invalid). Retried automatically only when transient (429, 408, Gmail rate-limit 403, DNS, connection failure) | No |
| `unknown` | `ErrDeliveryUnknown` | It may have been accepted: timeout, connection reset, 5xx, worker crash mid-send | **Possibly** |

`errors.As(err, &sendemail.SendError{})` gives the provider and HTTP status. Classification is conservative: anything not provably "never accepted" is `unknown`.

## Observing outcomes

The library has no logging or metrics by design; the connector worker emits them from the result. It should, for every send-email call:

- **log** `deliveryOutcome`, provider, HTTP status (from `SendError`), tenant, task ID, `sendIntentId` and `messageKey` when present — at `warn` for `unknown`;
- **count** `connector_send_email_outcomes_total{provider, outcome}` and alert when `outcome="unknown"` rises above its baseline;
- **record** the outcome on the task failure (`/internal/connector-tasks/:id/fail` carries the output map with `deliveryOutcome`).

With a send-intent store configured, `connector_send_intents` keeps one row per `messageKey` with `status`, `attempts`, `provider_message_id` and `detail` (the error text) for audit.

## Handling an `unknown` outcome

1. **Do not resend blindly.** The recipient may already have the email.
2. **Verify delivery outside the system where you can:**
   - SendGrid — Activity Feed / Email Activity API, by recipient and time;
   - Amazon SES — sending events (if configured) or CloudWatch delivery metrics;
   - Microsoft 365 — the sender mailbox's Sent Items, or message trace;
   - Google Workspace — the sender's Sent mail, or the Email Log Search.
3. **If it was delivered**, close the task manually; do not resend.
4. **If it was not delivered, or you cannot tell and a duplicate is acceptable**, resend explicitly: re-run the task with the same `messageKey`, `resend: true` and `resendAttempt: <n>`, where `n` is the intent's current `attempts` (shown as `attempts` in the duplicate output and in the `DuplicateRequestError` text, or read from `connector_send_intents`). This is the only way to resend a key that already has an intent, and it is recorded (`attempts` + 1). It is a compare-and-swap on that attempt: it proceeds only while attempt `n` is current and finished (or pending but stale, step 5), so if the resend task itself is redelivered it sees attempt `n+1` and is a duplicate — one operator action sends at most once. **A manual resend after an `unknown` outcome may deliver a duplicate.**
5. **An intent stuck in `pending`** (the worker stopped mid-send, or the outcome could not be recorded) can be resent once it is stale: unchanged for `sendintent.StalePendingAfter` (15 minutes, well beyond the 2-minute provider bound and the 10-second recording bound). Treat it as `unknown`: verify delivery with the provider first, then resend as in step 4 with `resendAttempt` set to its current attempt. A resend of a pending intent younger than that is a duplicate (it may still be sending), and a redelivered resend names a superseded attempt and sends nothing.

## Handling `not_delivered`

The email was not sent, and no duplicate is possible from this attempt. A **transient** cause (throttling, DNS, connection failure) is retried automatically by the worker, up to its attempt limit. A **permanent** cause (credential, sender verification, recipient address, provider quota) is not: fix it, then resend.

## Duplicate-request protection (`messageKey`)

An outcome is recorded only for the attempt that is still the intent's current, `pending` one: a late result of an earlier attempt (for example a worker writing its outcome again after that attempt was resent) is refused (`sendintent.ErrStaleRecord`) instead of overwriting the newer attempt — writing `not_delivered` there would let an automatic retry send a duplicate alongside it. Recording is bounded to 10 s, so a stuck database cannot hold a call whose email the provider already accepted.

A caller can pass a business-level `messageKey` (for example `invoice-42-reminder`). With a send-intent store configured (`connectors.Config.SendIntents`), the first request reserves the key and later requests with the same key send nothing — catching a retried workflow step, a double submission or a redelivered event. Their output carries `duplicate: true`, the intent's `sendIntentId`, `status`, `attempts` and `deliveryOutcome`:

- a duplicate of an **accepted** intent **succeeds** (no error) and also carries `providerMessageId`: a job redelivered after the worker crashed before acknowledging a successful send is idempotent;
- a duplicate of a **pending** or **unknown** intent fails with `DuplicateRequestError` (permanent, never retried); its `deliveryOutcome` is `unknown`.

Each reservation is stamped with a token unique to the call. If the store's reply to the reservation is lost (the row committed, the connection dropped), the call reads the intent back (bounded, detached from its cancellation) and proceeds only if the intent carries its own token and is still `pending`; otherwise it sends nothing (`not_delivered`).

This is **not idempotency**. It stops the same request entering the service twice; it cannot stop a provider from delivering a message twice when an operator resends after an `unknown` outcome. An intent left `pending` means the worker stopped mid-send, or the outcome could not be recorded (the output carries `sendIntentWarning`, and the failure is class unknown with reason `send intent not recorded`): treat it as `unknown`.
