#!/usr/bin/env bash
# Release "Build source archive" job (the library's counterpart of
# platform-pgcommon's "Build binaries"): writes the source archive of
# RELEASE_TAG — the tree the Go module proxy serves for the tag — and its
# .sha256, and records the asset name for the publish job.
set -euo pipefail

: "${RELEASE_TAG:?RELEASE_TAG is required}"

SOURCE_ARCHIVE="workflow-connectors_${RELEASE_TAG}_source.tar.gz"
git archive --format=tar.gz --prefix="workflow-connectors-${RELEASE_TAG#v}/" -o "$SOURCE_ARCHIVE" "$RELEASE_TAG"
sha256sum "$SOURCE_ARCHIVE" > "${SOURCE_ARCHIVE}.sha256"
echo "$SOURCE_ARCHIVE" > release-asset.name
echo "  ✔  ${SOURCE_ARCHIVE}"
cat "${SOURCE_ARCHIVE}.sha256"
