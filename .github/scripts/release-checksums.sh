#!/usr/bin/env bash
# Publish job: writes the source archive of RELEASE_TAG (the same tree the Go
# module proxy serves for the tag) and the aggregate checksums.txt over it and
# the SBOM, which release.yml signs with `cosign sign-blob` and
# create-github-release.sh attaches. Format matches
# `sha256sum --check checksums.txt`.
set -euo pipefail

: "${RELEASE_TAG:?RELEASE_TAG is required}"

test -f sbom.cyclonedx.json || {
  echo "::error title=Checksums::sbom.cyclonedx.json missing — did the SBOM artifact download?"
  exit 1
}

SOURCE_ARCHIVE="workflow-connectors_${RELEASE_TAG}_source.tar.gz"
git archive --format=tar.gz --prefix="workflow-connectors-${RELEASE_TAG#v}/" -o "$SOURCE_ARCHIVE" "$RELEASE_TAG"
echo "$SOURCE_ARCHIVE" > release-asset.name

sha256sum "$SOURCE_ARCHIVE" sbom.cyclonedx.json > checksums.txt
echo "  ✔  checksums.txt covers the source archive and the SBOM"
cat checksums.txt
