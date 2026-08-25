# Changelog

All notable changes to `workflow-connectors` will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- `send-email` is real for all 4 providers (LLD §10 Decision #22): `sendgrid` (`sendgrid-go`), `aws-ses` (`aws-sdk-go-v2/service/sesv2`), `microsoft-365` (hand-rolled Graph `sendMail` REST call + `oauth2/clientcredentials`, not the official Kiota-generated SDK), `google-workspace` (`google.golang.org/api/gmail/v1`, domain-wide-delegation service account impersonating `senderEmail`). `Config.SendEmailClient` is replaced by `Config.SendEmailProviders map[string]SendEmailProviderConstructor`, mirroring `storage`'s per-call-construction-with-cache pattern exactly — `provider` is required, no default, and an unconfigured provider is `ErrValidation`, never a mock fallback. `microsoft-365`/`google-workspace` have no server-side template mechanism, so `body` is required for those two regardless of `templateId`. `attachments` entries (document refs) resolve to real bytes via a `docRefStore` now shared across `storage` and `send-email` (see Fixed below) — a filename is synthesized from the resolved content type since the field carries no per-attachment name of its own.
- `pkg/connectors` — real `Execute()` implementations for all 6 v1 connector types, replacing the previous stubs. `rest-call`/`sql-query` dispatch over HTTP to internal platform services (resolved via the new `pkg/connectors/aliasconfig` static registry); `storage`/`send-email`/`document-extract`/`chat-notify` run against a small provider-client interface backed by an in-memory mock, swappable for a real SDK later via `Config`.
- `pkg/connectors/aliasconfig` — the `endpointAlias`/`queryAlias` static-registry schema, `Load` (fail-fast validation), and `ResolveEndpoint`/`ResolveQuery`.
- `connectors.New` now takes a `Config` (alias registry, HTTP client, internal token, provider-client overrides) and returns `(map[string]Connector, error)` — a breaking API change from the previous argument-less, infallible `New()`.
- `connectors.WithDepartments`/`DepartmentsFromContext` — the context seam `rest-call`/`sql-query` use to attach the caller-forwarded `x-departments` header; both fail closed (`ErrMissingInternalAuth`) if absent, matching `x-internal-token`'s mandatory status.
- `pkg/registry` — `Definition`/`Field` metadata for all 6 v1 connector types (`design/LLD/workflow_connectors.md` §6.4).
- `registry.IsIdempotentMethod` — resolves `rest-call`'s conditional retry policy per-call (GET/HEAD/PUT/DELETE/OPTIONS/TRACE idempotent, POST/PATCH not) — a signal `Definition.Retry` alone couldn't express.
- `registry.Field.IsSecretRef()` — one place to check whether a field's value must come from OpenBao rather than travel inline, instead of every caller hand-rolling `f.Kind == FieldKindSecretRef`.
- `registry.FieldKindTimestamp` — `storage.fetchedAt`/`send-email.sentAt` no longer fall back to a plain string.
- `storage` is real for all 4 providers (LLD §10 Decision #19/#20): `aws-s3`/`azure-blob`/`gcp-gcs` via `gocloud.dev/blob` (new `storage_gocloud.go`), `google-drive` via its own client (new `storage_drive.go`, search-by-name-in-folder + update-if-found/create-otherwise for idempotent uploads, Shared Drive-aware). New provider-specific registry fields, named globally-unique across providers on purpose: `azureAccountName`/`azureAccountKey`, `gcpServiceAccountKey`/`projectId`, `driveServiceAccountKey`. `Config.StorageClient` is replaced by `Config.StorageProviders map[string]StorageProviderConstructor` — a per-call constructor per provider (credentials are per-tenant, resolved fresh per call) with a small bounded client cache. `provider` is required — no default, and an unconfigured/unimplemented provider is a validation error, never a fallback.
- `docref.go` — a `storageConnector`-owned doc-ref store, replacing the old per-client `DocRefResolver`/`docRefRegistrar` optional-interface pattern (only ever worked because tests reused one mock instance across calls; a real, per-call-constructed provider client can't carry that state itself).

### Fixed

- `docRefStore` was owned per-connector-instance, constructed fresh inside `newStorage` — a doc ref minted by `storage`'s fetch (`createDocument: true`) was never resolvable by any other connector, silently breaking `send-email.attachments`' documented "list of document refs" contract. `connectors.New()` now constructs one `docRefStore` shared by `storage` and `send-email`.
- `storage`'s `provider` field description still said "Defaults to aws-s3 when omitted" — stale since Decision #20 removed the default entirely.
- `chat-notify.visibility` shipped as an enum field with no `EnumValues` — would have rendered as an empty dropdown to any UI consumer of `/connectors/registry`. Now `public`/`private`, matching `design/LLD/workflow_connectors.md` §6.4.6 (rev 7.2).
- `document-extract.confidence` was a single scalar float; the LLD's own "per-field confidence" wording means it needs to be keyed by field name — now `FieldKindMap`.
- `registry.All()`/`connectors.New()` silently overwrote a map entry on a duplicate `Type` (e.g. a copy-paste bug reusing a type constant); both now panic loudly instead.
- Per-field registry tests added (`Name`/`Kind`/`Required`/`EnumValues` for all 6 types) — aggregate-count-only assertions had let the `visibility` bug above ship unnoticed.
- `send-email`/`document-extract`/`chat-notify` briefly had a `provider` field with zero real per-provider implementation behind it (a mock-only client for all providers) — removed until each ships real code, matching LLD Decision #20.
- `storageConnector` silently fell back to an in-memory mock for any unconfigured provider, and defaulted a missing `provider` to `aws-s3` — both removed; both are now `ErrValidation`.
