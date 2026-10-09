# Security Policy

## Supported Versions

| Version | Supported |
|---------|-----------|
| 2.x     | ✅ Active — v2.0.0 is not tagged yet (`[Unreleased]` in `CHANGELOG.md`, status in `VERSIONING.md`) |
| 1.x     | ❌ Not supported — v1.0.0 has the defects fixed in 2.0.0 (SendGrid message mix-up under concurrency, SSRF through tenant credentials); upgrade once v2.0.0 is tagged |

Older major versions are not patched. Once v2.0.0 is tagged, consumers should pin the latest 2.x tag (module path `github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2`).

## Reporting a Vulnerability

**Do not open a public GitHub issue for security vulnerabilities.**

Email: vijay@bcbpsolutions.com
Subject: `[workflow-connectors] Security vulnerability`

Include in your report:
- Description of the vulnerability and the affected component (connector core, provider adapter, alias resolution, document-ref store, registry, etc.)
- Steps to reproduce
- Potential impact (credential exposure, cross-tenant access, request forgery against an internal service, email header injection, DoS, etc.)
- Suggested fix or patch (if any)

### Response timeline

| Step | Target |
|------|--------|
| Initial acknowledgement | 48 hours |
| Severity assessment | 5 business days |
| Patch release (critical/high) | 14 days |
| Public disclosure | After patch ships |

We follow responsible disclosure. Reporters will be credited in release notes unless anonymity is requested.

## Trust model

This module is a library. It runs inside `execution_service`'s `cmd/connector-worker`, within the worker's trust boundary, and has no server, listener or persistent state of its own.

- **Credentials arrive resolved.** For every field the registry marks `secret_ref`, the worker reads the tenant's stored credential from OpenBao (`connectors/<tenant>/<type>/<field>`, the job's own tenant only) just before calling `Execute`, and passes the value in the input. A connector never sees an OpenBao path, and a credential never appears in `IOMapping`, `context_json`, workflow history or the event stream (LLD §S6.2, §S8).
- **Credentials are held only in memory.** A connector may use a resolved credential to build a provider client, and `storage`/`send-email` cache those clients in process, keyed by a SHA-256 hash of every credential field (plus bucket or sender) and capped at 256 entries. A client is therefore reused only for an identical credential set, never across tenants or senders. A retired client (least-recently-used eviction at the cap, or `ResetClients()`, e.g. after rotation) is closed as soon as its last in-flight call finishes, so old credentials do not linger in open clients.
- **Credentials are never logged, returned or persisted.** No connector writes a credential to a log, an error message, an output map, a file or any external store. Any new connector or provider adapter must follow the same rule, and must cache credential-bearing clients only through its core's credential-keyed cache.
- **Internal calls carry caller identity.** `rest-call` and `sql-query` reach only internal platform services by alias, never a raw URL, and every call carries `x-internal-token` and the acting user's `x-departments`. Without departments in the context they fail before sending anything.

## Scope

Areas of particular sensitivity in this module:

- **Credential handling** — resolved credentials in `Execute` input and in cached provider clients; the cache key must cover every credential field so no client is shared across credential sets.
- **Alias resolution** (`rest-call`, `sql-query`) — request forgery or path injection against internal services. Path parameters are escaped; `aliasconfig` rejects a `baseURL` that is not a plain `http`/`https` URL; the host allowlist is enforced where aliases are written, in `definition_service`.
- **Email header injection** — `send-email` fields such as receiver name and subject often come from end-user form data; Gmail's raw-MIME path strips CR/LF from every header value.
- **Provider credential types** — `gcp-gcs` accepts only `service_account` keys, so a workload-identity configuration cannot make the worker use its own ambient identity.
- **Tenant credentials that name endpoints (SSRF)** — a Google service-account key carries its own `token_uri`, which the Google libraries POST a signed assertion to and quote the reply from in errors. `shared.ValidateGoogleServiceAccountKey` accepts only Google's token endpoint and universe for `gcp-gcs`, `google-drive` and `google-workspace`. Likewise the Azure account and container names (`azure-blob`) must match Azure's naming rules and the Entra `tenantId` (`microsoft-365`) must be a GUID or domain, because each is placed in a URL. A change that builds a request URL or token endpoint from tenant input without such a check lets a tenant aim the worker at internal hosts.
- **Provider HTTP clients** — every provider client (`shared.ProviderHTTPClient`, and the OAuth transports layered on it) never follows a redirect: a redirect would re-send the request, and its bearer token or API key, to whichever host it names. It speaks HTTP/1.1 only, because Go's HTTP/2 client replays a request body after a `PROTOCOL_ERROR` stream reset, which could deliver an email twice.
- **Concurrency of cached provider clients** — a cached client is shared by concurrent calls with the same credentials, so it must hold no per-call mutable state. The SendGrid adapter builds each request itself because `sendgrid.Client.SendWithContext` writes the message body onto the shared client, which mixed up concurrent emails (one recipient got another's message twice; the other was never sent but reported accepted).
- **Document refs** (`docref`, `docref/s3content`, `docref/valkeystore`) — content is stored in the platform's S3 document bucket (customer document bytes: SSE-KMS, public access blocked, writes limited to the workers' IAM role), and Valkey holds only reference metadata (tenant, S3 location, size, SHA-256) — never content and never credentials. Every resolution checks the tenant and that the object key is exactly the ref's own `<prefix><tenant>/<uuid>` before calling S3, then verifies size and SHA-256, so an overwritten object or a forged record is refused (`ErrIntegrityViolation`), never served. A change that fetches an object before the tenant check, skips verification, or stores content or credentials in Valkey breaks tenant isolation or integrity.
- **Redirects on internal calls** — `rest-call`/`sql-query` never follow a redirect, because Go would re-send `x-internal-token` and `x-departments` to the redirect target. Any change to their HTTP client must keep this.
- **Email recipients** — `senderEmail`/`receiverEmail` must each be one bare address, so a comma-separated value cannot add recipients.
- **Tenant isolation of AWS clients** — S3 and SES clients are built only from the tenant's credentials and region; reading the worker's AWS environment or config files into them would leak worker settings across tenants.
- **Drive document registry** — uniqueness of Drive documents rests on the `connector_documents` unique constraint and owner compare-and-set in the worker's PostgreSQL. A change that decides uniqueness by listing a Drive folder, or that writes to Drive without owning the row, reintroduces duplicate files.
- **Email sends** — `send-email` is retried automatically only for a transient failure the provider provably never accepted (`ErrNotDelivered`: DNS, connection failure, throttling); a send whose outcome is unknown is never retried, because no provider supports an idempotency key and it may already have been delivered. A change that retries an unknown outcome, classifies a post-send timeout or 5xx as not delivered, or re-enables the AWS SDK retryer on the SES client reintroduces duplicate emails. See `docs/runbooks/email-delivery.md` and `docs/runbooks/retry-semantics.md`.
- **Registry metadata** — a field wrongly marked as non-secret would let an author map a credential into a plan.

Out of scope: vulnerabilities in the external providers themselves (AWS, Azure, Google, Microsoft, SendGrid); OpenBao storage and access policy (`execution_service`, `definition_service`); the alias allowlist (`definition_service`). Report those to their owners.

Threat model (STRIDE): [`ARCHITECTURE.md`](ARCHITECTURE.md#threat-model).
