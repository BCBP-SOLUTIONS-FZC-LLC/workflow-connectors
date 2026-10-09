# Versioning and releases

Go module, versioned via Git tags per [SemVer 2.0.0](https://semver.org/). `pkg/registry` and `pkg/connectors` are both public API.

| Bump | When you change |
|------|-----------------|
| **MAJOR** | Any breaking change to an exported identifier (`Connector`, `Config`, constructors such as `storage.New`/`sendemail.New`, the `ProviderClient` interfaces, `Definition`/`Field`, any `Type*`/`FieldKind*` constant) or to documented behaviour. A new major version also changes the module path: v2.x lives at `…/workflow-connectors/v2`, v3.x would live at `…/v3` |
| **MINOR** | New connector type, new optional field, new backward-compatible capability |
| **PATCH** | Bug fix, documentation-only change |

## Current release status

| Version | Status | Module path |
|---|---|---|
| **v2.0.0** | **Unreleased** — the changes under `[Unreleased]` in `CHANGELOG.md`; not tagged yet | `github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2` |
| v1.0.0 | Latest tag (2026-10-09); not supported (see `SECURITY.md`) | `github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors` |

Until v2.0.0 is tagged, no `/v2` version can be fetched through the module proxy. When it is, the `[Unreleased]` section becomes `## [2.0.0] - YYYY-MM-DD` (step 2 below).

## Consume a release

```bash
go env -w GOPRIVATE=github.com/BCBP-SOLUTIONS-FZC-LLC/*
go get github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2@vX.Y.Z
```

Consumers move between major versions by changing their import paths (`…/workflow-connectors/pkg/…` → `…/workflow-connectors/v2/pkg/…`); both majors can coexist in one build.

## Maintainer release process

1. Merge to `main`. Release tags are cut from `main`, never from a feature branch.
2. Move `[Unreleased]` into a new `## [X.Y.Z] - YYYY-MM-DD` section in `CHANGELOG.md`.
3. `make ci` locally.
4. `git tag -a vX.Y.Z -m "vX.Y.Z" && git push origin vX.Y.Z`. The release workflow (`release.yml`) refuses a tag that does not point at a commit on `origin/main` or whose major version does not match the module path's `/vN` suffix (`.github/scripts/verify-release-tag.sh`), and one with no `## [X.Y.Z]` section in `CHANGELOG.md` (`verify-changelog-entry.sh`). It re-runs the test and quality gates at the tag, scans dependencies with Trivy and publishes a GitHub Release with the changelog section, a source archive, a CycloneDX SBOM and `checksums.txt`.
5. Notify `definition_service` and `execution_service` owners if MINOR or MAJOR.
