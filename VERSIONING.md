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
| **v2.0.0** | **Current** — released 2026-10-10 (`CHANGELOG.md` `[2.0.0]`) | `github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2` |
| v1.0.0 | Released 2026-10-09; not supported (see `SECURITY.md`) | `github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors` |

Consumers: `go get github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2@v2.0.0` with `GOPRIVATE` set. The next release is v2.0.1 for fixes and v2.1.0 for backward-compatible features; changes since v2.0.0 collect under `[Unreleased]`.

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
4. `git tag -a vX.Y.Z -m "vX.Y.Z" && git push origin vX.Y.Z`. The release workflow (`release.yml`) first runs **verify**: it refuses a manual dispatch from any ref but `main` or the tag, a tag that does not point at a commit on `origin/main`, one whose major version does not match the module path's `/vN` suffix (`.github/scripts/verify-release-tag.sh`), and one with no `## [X.Y.Z]` section in `CHANGELOG.md` (`verify-changelog-entry.sh`; a prerelease such as `vX.Y.Z-rc.1` may use the `## [X.Y.Z]` section). It then re-runs the test and quality gates at the tag, runs the **blocking API-compatibility check** (`make api-compat API_NEW_VERSION=vX.Y.Z`: a MINOR or PATCH release fails on an incompatible exported-API change — run it locally before tagging), scans dependencies with Trivy, and publishes a GitHub Release with the changelog section, a source archive, a CycloneDX SBOM and a Cosign-signed `checksums.txt`.
5. Notify `definition_service` and `execution_service` owners if MINOR or MAJOR.

## Verifying a release

Each GitHub Release carries `workflow-connectors_vX.Y.Z_source.tar.gz` (the tree the Go module proxy serves for the tag), `sbom.cyclonedx.json`, `checksums.txt` over both, and `checksums.txt.sigstore.json` — a Cosign keyless signature made by `release.yml` at the tag. To verify:

```bash
cosign verify-blob \
  --bundle checksums.txt.sigstore.json \
  --certificate-identity-regexp '^https://github\.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/\.github/workflows/release\.yml@refs/(tags/v.+|heads/main)$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  checksums.txt
sha256sum --check checksums.txt
```

Consumers who only `go get` the module are already protected by the Go checksum database for public modules; for this private module (`GONOSUMDB`), pin the tag and compare `go.sum` across environments, or verify the source archive above.
