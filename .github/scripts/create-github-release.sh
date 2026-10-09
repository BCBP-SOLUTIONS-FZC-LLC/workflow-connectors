#!/usr/bin/env bash
# Creates the GitHub release with the source archive, the SBOM, checksums.txt
# and its Cosign Sigstore bundle attached. The archive and checksums.txt come
# from release-checksums.sh; release.yml signs checksums.txt before this runs.
set -euo pipefail

: "${RELEASE_TAG:?RELEASE_TAG is required}"
: "${GH_TOKEN:?GH_TOKEN is required}"

test -f release-asset.name || {
  echo "::error::release-asset.name missing — run release-checksums.sh first"
  exit 1
}
SOURCE_ARCHIVE=$(cat release-asset.name)

for f in release-notes.md "$SOURCE_ARCHIVE" checksums.txt checksums.txt.sigstore.json sbom.cyclonedx.json; do
  test -f "$f" || {
    echo "::error::Release asset missing: ${f}"
    exit 1
  }
done

PRERELEASE_FLAG=""
if echo "$RELEASE_TAG" | grep -q -- '-'; then
  PRERELEASE_FLAG="--prerelease"
fi

gh release create "$RELEASE_TAG" \
  --title "$RELEASE_TAG" \
  --notes-file release-notes.md \
  "$SOURCE_ARCHIVE" \
  checksums.txt \
  checksums.txt.sigstore.json \
  sbom.cyclonedx.json \
  $PRERELEASE_FLAG
