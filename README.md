# workflow-connectors

Shared library for the workflow engine's automatic connector-task feature. Full design: `docs/lld/workflow_connectors.md` (this repo's in-repo copy of the design repo's LLD, kept content-identical to it). Consumed as a private Go module — not deployed on its own.

## Packages

- **`pkg/registry`** — lightweight, compile-time metadata (type names, authoring-template field descriptions, retry policy) for the v1 catalogue. Zero heavy SDK dependencies. Imported by `definition_service` for its compile-time connector-type check and authoring-template generator.
- **`pkg/connectors`** — the real `Connector` implementations. Imported only by `execution_service`'s `cmd/connector-worker`.
- **`pkg/connectors/aliasconfig`** — the static `endpointAlias`/`queryAlias` registry schema and loader `rest-call`/`sql-query` resolve against, and that `cmd/connector-worker` loads at startup.

## v1 catalogue

`storage`, `send-email`, `rest-call` and `chat-notify` are active; field tables are in `pkg/registry` and the LLD §6.4. `sql-query` and `document-extract` are held back.

## Status

Four connectors are active in `connectors.New`: `rest-call` (HTTP to an alias-resolved internal endpoint), `storage` (`aws-s3`, `azure-blob`, `gcp-gcs`, `google-drive`), `send-email` (`sendgrid`, `aws-ses`, `microsoft-365`, `google-workspace`) and `chat-notify`, which runs on an in-memory mock client until a real provider is wired in through `Config.ChatNotifyClient`. `sql-query` and `document-extract` are implemented in their subpackages but are neither wired into `connectors.New` nor listed in `registry.All()`.
