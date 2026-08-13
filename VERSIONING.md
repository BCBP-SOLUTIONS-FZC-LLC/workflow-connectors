# Versioning and releases

Go module, versioned via Git tags per [SemVer 2.0.0](https://semver.org/). `pkg/registry` and `pkg/connectors` are both public API.

| Bump | When you change |
|------|-----------------|
| **MAJOR** | Breaking change to `Connector`, `Definition`/`Field`, or any `Type*`/`FieldKind*` constant |
| **MINOR** | New connector type, new optional field, new backward-compatible capability |
| **PATCH** | Bug fix, documentation-only change |

## Consume a release

```bash
go env -w GOPRIVATE=github.com/BCBP-SOLUTIONS-FZC-LLC/*
go get github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors@vX.Y.Z
```

## Maintainer release process

1. Merge to `main`.
2. Move `[Unreleased]` into a new `## [X.Y.Z] - YYYY-MM-DD` section in `CHANGELOG.md`.
3. `make ci` locally.
4. `git tag -a vX.Y.Z -m "vX.Y.Z" && git push origin vX.Y.Z`.
5. Notify `definition_service` and `execution_service` owners if MINOR or MAJOR.
