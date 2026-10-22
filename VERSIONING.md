# Versioning and releases

Go module, versioned via Git tags per [SemVer 2.0.0](https://semver.org/). `pkg/registry` and `pkg/connectors` are both public API.

| Bump | When you change |
|------|-----------------|
| **MAJOR** | Any breaking change to an exported identifier (`Connector`, `Config`, constructors such as `storage.New`/`sendemail.New`, the `ProviderClient` interfaces, `Definition`/`Field`, any `Type*`/`FieldKind*` constant) or to documented behaviour a correct caller relies on (see [What is not a breaking change](#what-is-not-a-breaking-change)). A new major version also changes the module path: v2.x lives at `…/workflow-connectors/v2`, v3.x would live at `…/v3` |
| **MINOR** | New connector type, new optional field, new backward-compatible capability |
| **PATCH** | Bug fix, documentation-only change |

## What is not a breaking change

"Documented behaviour" means the contract the docs promise to a correct caller, not every input the code happened to accept. These changes are **MINOR** (or **PATCH** when they are pure fixes with nothing new exported), provided each is listed in `CHANGELOG.md` under `### Changed` (or `### Removed`) with an **Upgrade:** note when a consumer has to act:

- **Refusing input that could never succeed** or that contradicted a documented contract, with `ErrValidation` (for example a `contentType` outside the registry enum, a storage `key` that is not valid UTF-8, an alias YAML key `Config` does not define, an alias `pathTemplate` whose query or fragment could not be sent as written).
- **Fixing behaviour that contradicted the docs**, even when a caller could observe the old behaviour (for example a storage delete that reported success on a missing bucket, a `paramCount: 0` that did not mean "no parameters", an error class that made `DecideRetry` give the wrong answer).
- **Removing a `registry.All()` entry for a connector with no working provider** (every authored step could only fail), as long as `registry.Lookup` and `connectors.New` keep the type so existing tasks fail the same way at run time.

Still **MAJOR**, whatever the motive: a changed or removed exported identifier or signature, a changed meaning of an exported identifier a correct caller relies on (a sentinel, an error class, a `RetryPolicy`, a field name or kind), a removed connector type or input/output field, and a refused input that a documented contract allowed. The **apidiff gate** (`make api-compat API_NEW_VERSION=vX.Y.Z`, blocking in `release.yml`) enforces the exported-API half for MINOR and PATCH releases; the behavioural half is the reviewer's call, against this list.

## Current release status

| Version | Status | Module path |
|---|---|---|
| **v2.2.0** | **Current** — released 2026-10-22 (`CHANGELOG.md` `[2.2.0]`) | `github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2` |
| v2.1.0 | Released 2026-10-13 (`CHANGELOG.md` `[2.1.0]`) | `github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2` |
| v2.0.0 | Released 2026-10-10 (`CHANGELOG.md` `[2.0.0]`) | `github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2` |
| v1.0.0 | Released 2026-10-09; not supported (see `SECURITY.md`) | `github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors` |

Consumers: `go get github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2@v2.2.0` with `GOPRIVATE` set. Next: v2.2.1 for fixes, v2.3.0 for backward-compatible features; changes after v2.2.0 collect under `[Unreleased]`.

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

Each GitHub Release carries `workflow-connectors_vX.Y.Z_source.tar.gz` (the tree the Go module proxy serves for the tag), `sbom.cyclonedx.json`, `checksums.txt` over both, and `checksums.txt.sigstore.json` — a Cosign keyless signature made by `release.yml` at the tag. Verifying needs **Cosign v3** (the release signs with v3.0.6, whose Sigstore bundle format Cosign 2.x cannot read: it fails with "bundle does not contain cert for verification"). To verify:

```bash
cosign verify-blob \
  --bundle checksums.txt.sigstore.json \
  --certificate-identity-regexp '^https://github\.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/\.github/workflows/release\.yml@refs/(tags/v.+|heads/main)$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  checksums.txt
sha256sum --check checksums.txt
```

Consumers who only `go get` the module are already protected by the Go checksum database for public modules; for this private module (`GONOSUMDB`), pin the tag and compare `go.sum` across environments, or verify the source archive above.
