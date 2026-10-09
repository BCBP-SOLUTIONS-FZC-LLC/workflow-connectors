#!/usr/bin/env bash
# Extracts the CHANGELOG.md section documenting RELEASE_TAG into
# release-notes.md (a prerelease may use its base version's section — see
# changelog-section.sh) and appends the Go module line consumers pin and how
# to verify the release assets.
set -euo pipefail

: "${RELEASE_TAG:?RELEASE_TAG is required}"

# shellcheck source=changelog-section.sh
source "$(dirname "$0")/changelog-section.sh"

VERSION="${RELEASE_TAG#v}"
if ! SECTION=$(changelog_section_version "$RELEASE_TAG"); then
  echo "::error file=CHANGELOG.md::Missing entry for ${VERSION}"
  exit 1
fi
# Plain redirects, not `| tee file | true`: under pipefail, `true` exits
# without reading, tee can die of SIGPIPE and fail the whole release.
changelog_section_body "$SECTION" > release-notes.md

if [ ! -s release-notes.md ]; then
  echo "::error file=CHANGELOG.md::Section [${SECTION}] for ${VERSION} is empty"
  exit 1
fi

# Read from go.mod: the module path carries the /vN suffix (Go semantic
# import versioning), and this needs no Go toolchain.
MODULE=$(awk '$1 == "module" { print $2; exit }' go.mod)

{
  if [ "$SECTION" != "$VERSION" ]; then
    echo "> Release candidate ${RELEASE_TAG} of ${SECTION}."
    echo ""
  fi
  cat release-notes.md
  echo ""
  echo "---"
  echo "**Go module:** \`go get ${MODULE}@${RELEASE_TAG}\`"
  echo "**Verify:** \`checksums.txt\` covers the source archive and the SBOM and is Cosign keyless-signed (bundle: \`checksums.txt.sigstore.json\`); see VERSIONING.md *Verifying a release*."
} > release-notes.md.tmp
mv release-notes.md.tmp release-notes.md
cat release-notes.md
