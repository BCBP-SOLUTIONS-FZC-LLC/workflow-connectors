# Security policy

## Reporting a vulnerability

**Do not open a public GitHub issue for security vulnerabilities.**

Report by email to **vijay@bcbpsolutions.com** with the subject line `[workflow-connectors] Security vulnerability`.

## Trust model and known limitations

`pkg/connectors`' real implementations will handle live provider credentials (resolved via `platform-secrets`, never a raw value in `IOMapping`/`context_json` — see `design/LLD/workflow_connectors.md` §6.2/§8). Any future `Connector` implementation must not log, cache, or otherwise persist a resolved credential outside the single `Execute()` call it was resolved for.

## Scope

In scope: credential handling once real implementations land, SSRF/injection risk in the `rest-call`/`sql-query` alias-resolution path. Out of scope: the external providers' own vulnerabilities (AWS, SendGrid, Slack, etc.).
