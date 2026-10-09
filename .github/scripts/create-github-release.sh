#!/usr/bin/env bash
# Creates the GitHub release with the SBOM and source checksum attached.
set -euo pipefail

: "${RELEASE_TAG:?RELEASE_TAG is required}"
: "${GH_TOKEN:?GH_TOKEN is required}"

for f in release-notes.md sbom.cyclonedx.json; do
  test -f "$f" || {
    echo "::error::Release asset missing: ${f}"
    exit 1
  }
done

# Source archive matching what the Go module proxy serves for this tag, plus
# a checksum verifiable with `sha256sum --check checksums.txt`.
SOURCE_ARCHIVE="workflow-connectors_${RELEASE_TAG}_source.tar.gz"
git archive --format=tar.gz --prefix="workflow-connectors-${RELEASE_TAG#v}/" -o "$SOURCE_ARCHIVE" "$RELEASE_TAG"
sha256sum "$SOURCE_ARCHIVE" sbom.cyclonedx.json > checksums.txt

PRERELEASE_FLAG=""
if echo "$RELEASE_TAG" | grep -q -- '-'; then
  PRERELEASE_FLAG="--prerelease"
fi

gh release create "$RELEASE_TAG" \
  --title "$RELEASE_TAG" \
  --notes-file release-notes.md \
  "$SOURCE_ARCHIVE" \
  checksums.txt \
  sbom.cyclonedx.json \
  $PRERELEASE_FLAG
