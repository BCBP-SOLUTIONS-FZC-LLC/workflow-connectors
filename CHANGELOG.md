# Changelog

All notable changes to `workflow-connectors` will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- `pkg/registry` — `Definition`/`Field` metadata for all 6 v1 connector types (`design/LLD/workflow_connectors.md` §6.4).
- `pkg/connectors` — `Connector` interface and stub implementations (`ErrNotImplemented`) for all 6 v1 types. Real provider implementations are separate, later work.
- `registry.IsIdempotentMethod` — resolves `rest-call`'s conditional retry policy per-call (GET/HEAD/PUT/DELETE/OPTIONS/TRACE idempotent, POST/PATCH not) — a signal `Definition.Retry` alone couldn't express.
- `registry.Field.IsSecretRef()` — one place to check whether a field's value must come from OpenBao rather than travel inline, instead of every caller hand-rolling `f.Kind == FieldKindSecretRef`.
- `registry.FieldKindTimestamp` — `storage.fetchedAt`/`send-email.sentAt` no longer fall back to a plain string.

### Fixed

- `chat-notify.visibility` shipped as an enum field with no `EnumValues` — would have rendered as an empty dropdown to any UI consumer of `/connectors/registry`. Now `public`/`private`, matching `design/LLD/workflow_connectors.md` §6.4.6 (rev 7.2).
- `document-extract.confidence` was a single scalar float; the LLD's own "per-field confidence" wording means it needs to be keyed by field name — now `FieldKindMap`.
- `registry.All()`/`connectors.New()` silently overwrote a map entry on a duplicate `Type` (e.g. a copy-paste bug reusing a type constant); both now panic loudly instead.
- Per-field registry tests added (`Name`/`Kind`/`Required`/`EnumValues` for all 6 types) — aggregate-count-only assertions had let the `visibility` bug above ship unnoticed.
