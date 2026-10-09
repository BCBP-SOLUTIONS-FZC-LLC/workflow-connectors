#!/usr/bin/env bash
# Publish job: re-verifies the source archive against the .sha256 written by
# the build-source-archive job (proves the artifact hand-off did not alter
# it), then writes the aggregate checksums.txt over the archive and the SBOM,
# which release.yml signs with `cosign sign-blob` and
# create-github-release.sh attaches. Format matches
# `sha256sum --check checksums.txt`.
set -euo pipefail

test -f release-asset.name || {
  echo "::error title=Checksums::release-asset.name missing — did the source-archive artifact download?"
  exit 1
}
SOURCE_ARCHIVE=$(cat release-asset.name)
for f in "$SOURCE_ARCHIVE" "${SOURCE_ARCHIVE}.sha256" sbom.cyclonedx.json; do
  test -f "$f" || {
    echo "::error title=Checksums::${f} missing — did the artifacts download?"
    exit 1
  }
done

sha256sum --check --strict "${SOURCE_ARCHIVE}.sha256"
sha256sum "$SOURCE_ARCHIVE" sbom.cyclonedx.json > checksums.txt
echo "  ✔  checksums.txt covers the source archive and the SBOM"
cat checksums.txt
