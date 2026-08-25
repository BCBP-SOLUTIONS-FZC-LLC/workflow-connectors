# workflow-connectors

Shared library for the workflow engine's automatic connector-task feature. Full design: `docs/lld/workflow_connectors.md` (this repo's in-repo copy of the design repo's LLD, kept content-identical to it). Consumed as a private Go module — not deployed on its own.

## Packages

- **`pkg/registry`** — lightweight, compile-time metadata (type names, authoring-template field descriptions, retry policy) for the v1 catalogue. Zero heavy SDK dependencies. Imported by `definition_service` for its compile-time connector-type check and authoring-template generator.
- **`pkg/connectors`** — the real `Connector` implementations. Imported only by `execution_service`'s `cmd/connector-worker`.
- **`pkg/connectors/aliasconfig`** — the static `endpointAlias`/`queryAlias` registry schema and loader `rest-call`/`sql-query` resolve against, and that `cmd/connector-worker` loads at startup.

## v1 catalogue

`storage`, `send-email`, `document-extract`, `rest-call`, `sql-query`, `chat-notify` — full field tables in `pkg/registry` and the LLD §6.4.

## Status

All 6 `pkg/connectors` implementations are real. `rest-call` and `sql-query` dispatch to internal platform services over HTTP (an alias-resolved endpoint for `rest-call`, an owning service's own query-execution endpoint for `sql-query` — neither needs a third-party account). The other 4 (`storage`, `send-email`, `document-extract`, `chat-notify`) run against a small provider-client interface backed by an in-memory mock (`Config.StorageClient` etc.) until a real SDK is wired in — swapping one in later is a single-file, single-line-of-wiring change, no `Execute()` changes needed.
