# Contributing

Internal shared library for the workflow engine's connector-task feature.

## Development setup

```bash
git clone https://github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors
cd workflow-connectors
make setup
make lint
make test-unit
```

`make test-unit` needs no Docker. Before opening a pull request, run `make test` (or `make test-ci`, as CI does): it starts `docker-compose.yml` (PostgreSQL, PgBouncer, Valkey, floci S3) and runs the unit, postgres and integration suites. `make docker-down` stops the stack.

Open pull requests from a branch of this repository, not a fork: CI downloads the private `platform-pgcommon` module with the org-level `GO_PRIVATE_TOKEN` secret, which GitHub does not pass to workflows triggered from forks, so a fork's run fails at `go mod download`.

## Project layout

```
pkg/registry/            ← Lightweight metadata (definition_service imports this; standard library only)
pkg/connectors/          ← Real Connector implementations (execution_service imports this)
  <connector>/           ← connector cores: storage, sendemail, chatnotify, restcall, sqlquery, documentextract
  <connector>/<provider> ← provider adapters: storage/{gocloud,googledrive}, sendemail/{ses,sendgrid,msgraph,gmail}
  docref/{s3content,valkeystore}, documents/sqlstore, sendintent/sqlstore, aliasconfig, shared
test/unit/               ← black-box unit tests, one directory per package (no Docker)
test/postgres/           ← PostgreSQL store tests, direct and through PgBouncer (tag integration)
test/integration/        ← Valkey, floci S3 and multi-replica tests (tag integration)
```

`pkg/connectors` may import `pkg/registry`; never the reverse. Only white-box tests that need unexported code stay beside the code in `pkg/`. The full dependency rules are in `.go-arch-lint.yml` (`make arch-lint`); see `ARCHITECTURE.md` and the README's Testing section.

## Pull request checklist

- [ ] `make ci` passes locally
- [ ] New exported symbols have godoc comments
- [ ] `CHANGELOG.md` `[Unreleased]` section updated
- [ ] If a field table in `pkg/registry` changed: confirm it against the LLD (`docs/lld/workflow-connectors-library-lld.md`) §S6.4 directly, not from memory
