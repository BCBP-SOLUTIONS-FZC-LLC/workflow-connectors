# Contributing

Internal shared library for the workflow engine's connector-task feature.

## Development setup

```bash
git clone https://github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors
cd workflow-connectors
make setup
make lint
make test
```

## Project layout

```
pkg/registry/     ← Lightweight metadata (definition_service imports this)
pkg/connectors/    ← Real Connector implementations (execution_service imports this)
```

`pkg/connectors` may import `pkg/registry`; never the reverse.

## Pull request checklist

- [ ] `make ci` passes locally
- [ ] New exported symbols have godoc comments
- [ ] `CHANGELOG.md` `[Unreleased]` section updated
- [ ] If a field table in `pkg/registry` changed: confirm it against `design/LLD/workflow_connectors.md` §6.4 directly, not from memory
