# Architecture

## Why two packages

`pkg/registry` and `pkg/connectors` are split so that a compile-time-only consumer never pays for the runtime's dependencies: `definition_service` needs the type list and authoring-template shapes, not the AWS SDK/SendGrid client/DB drivers `pkg/connectors` will eventually pull in. `pkg/connectors` may import `pkg/registry` (for its `Type*` constants); `pkg/registry` never imports `pkg/connectors`.

## Secret resolution boundary

`Connector.Execute(ctx, input)` receives already-**resolved** credential values for every `registry.Field.IsSecretRef()` field — never an OpenBao path. The caller (`cmd/connector-worker`) resolves each secret path to its real value immediately before calling `Execute`. This keeps OpenBao entirely out of this module's import graph, consistent with the hard rule that `pkg/registry` (and, by extension, `pkg/connectors`) never gains a heavy/infra dependency it doesn't need, and keeps the mocked connectors trivially testable with plain values.

## `sql-query`'s data-access model

`sql-query` is structurally identical to `rest-call`, not a direct database connection: an owning service's own internal query-execution endpoint resolves `queryId` against its own pre-registered, read-only statement and performs the actual parameter binding. This connector never sees or constructs SQL text. `x-internal-token`/`x-departments` are real HTTP headers attached to that request — a raw DB connection has no meaningful transport for either, which is the main reason a direct-connection design was rejected.

## Status

All 6 `pkg/connectors` implementations are real. `rest-call`/`sql-query` dispatch over HTTP, resolved against `pkg/connectors/aliasconfig`'s static registry. `storage`/`send-email`/`document-extract`/`chat-notify` run against an in-memory mock provider client by default (`Config.StorageClient` etc.) — real S3/SendGrid/Textract/Slack SDK integrations are separate, later work, tracked in `execution_service`'s own task board.
