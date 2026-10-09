#!/usr/bin/env bash
# Extracts the RELEASE_TAG section from CHANGELOG.md into release-notes.md and
# appends the Go module import line consumers pin.
set -euo pipefail

: "${RELEASE_TAG:?RELEASE_TAG is required}"

VERSION="${RELEASE_TAG#v}"
awk -v ver="[${VERSION}]" '
  substr($0, 1, 3) == "## " && index($0, ver) > 0 { found=1; next }
  /^## \[/ { if (found) exit }
  found { print }
' CHANGELOG.md | sed -e 's/^[[:space:]]*$//' > release-notes.md

if [ ! -s release-notes.md ]; then
  echo "::error file=CHANGELOG.md::Missing entry for ${VERSION}"
  exit 1
fi

MODULE=$(go list -m)
{
  echo ""
  echo "---"
  echo "**Go module:** \`go get ${MODULE}@${RELEASE_TAG}\`"
} >> release-notes.md
