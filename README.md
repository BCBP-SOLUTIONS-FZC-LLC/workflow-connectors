# workflow-connectors

Shared library for the workflow engine's automatic connector-task feature (`design/LLD/workflow_connectors.md`). Consumed as a private Go module — not deployed on its own.

## Packages

- **`pkg/registry`** — lightweight, compile-time metadata (type names, authoring-template field descriptions, retry policy) for the v1 catalogue. Zero heavy SDK dependencies. Imported by `definition_service` for its compile-time connector-type check and authoring-template generator.
- **`pkg/connectors`** — the real `Connector` implementations (currently stubs — see CHANGELOG). Imported only by `execution_service`'s `cmd/connector-worker`.

## v1 catalogue

`storage`, `send-email`, `document-extract`, `rest-call`, `sql-query`, `chat-notify` — full field tables in `pkg/registry` and `design/LLD/workflow_connectors.md` §6.4.

## Status

`pkg/connectors`' six implementations are stubs (`ErrNotImplemented`) — real provider calls are separate, later work. `pkg/registry`'s metadata is complete and usable today.
