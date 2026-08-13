# Architecture

## Why two packages

`pkg/registry` and `pkg/connectors` are split so that a compile-time-only consumer never pays for the runtime's dependencies: `definition_service` needs the type list and authoring-template shapes, not the AWS SDK/SendGrid client/DB drivers `pkg/connectors` will eventually pull in. `pkg/connectors` may import `pkg/registry` (for its `Type*` constants); `pkg/registry` never imports `pkg/connectors`.

## Status

`pkg/connectors`' six implementations are stubs returning `ErrNotImplemented` — real provider calls (S3, SendGrid, Textract, Slack, the internal `rest-call`/`sql-query` paths) are separate, later work, tracked in `execution_service`'s own task board.
