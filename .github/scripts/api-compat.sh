#!/usr/bin/env bash
# API-compatibility gate for the module's exported API (every package under
# pkg/ is public: there is no internal/ tree). Taken from platform-pgcommon,
# without its internal/ re-export handling, which this module does not need.
#
#   api-compat.sh vX.Y.Z[-pre]   release mode: compare with the highest stable
#                                tag below vX.Y.Z and FAIL on any incompatible
#                                change unless vX.Y.Z bumps the major version.
#   api-compat.sh                PR mode: compare with the highest stable tag
#                                merged into HEAD and only WARN (the next
#                                version is not known yet; the release gate
#                                decides).
#
# Needs the full tag history (actions/checkout fetch-depth: 0) and module
# downloads for both trees (platform-pgcommon is private: GOPRIVATE and
# credentials, as in the validate workflows). The base is taken with
# `git archive`. APIDIFF_VERSION pins golang.org/x/exp/cmd/apidiff (x/exp has
# no tags, so it is a pseudo-version); `make api-compat` passes the pinned one.
set -euo pipefail

: "${APIDIFF_VERSION:?APIDIFF_VERSION is required (make api-compat sets it)}"
NEW=${1:-}

stable_re='^v[0-9]+\.[0-9]+\.[0-9]+$'
major() { local v=${1#v}; echo "${v%%.*}"; }

if [ -n "$NEW" ]; then
  if ! [[ "$NEW" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-.+)?$ ]]; then
    echo "::error title=API compatibility::'$NEW' is not a vX.Y.Z[-pre] version"
    exit 1
  fi
  core=${NEW%%-*}
  # Highest stable tag strictly below the release's X.Y.Z (a hotfix of an
  # older line is compared with that line, not with the newest release).
  BASE=$( { git tag --list 'v*' | grep -E "$stable_re" | grep -vxF "$core" || true; echo "$core"; } \
          | sort -V | grep -B1 -xF "$core" | head -n1)
  [ "$BASE" = "$core" ] && BASE=''
  level=error
else
  BASE=$(git tag --merged HEAD --list 'v*' | grep -E "$stable_re" | sort -V | tail -n1 || true)
  level=warning
fi

if [ -z "$BASE" ]; then
  echo "No earlier stable release tag — nothing to compare against."
  exit 0
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

GOBIN="$tmp/bin" go install "golang.org/x/exp/cmd/apidiff@${APIDIFF_VERSION}"
MOD=$(go list -m)

# The head tree is the working tree (uncommitted changes included), copied so
# it can be rewritten; the base is the tag, via git archive.
mkdir -p "$tmp/base" "$tmp/head"
git archive "$BASE" | tar -x -C "$tmp/base"
git ls-files -z --cached --others --exclude-standard \
  | while IFS= read -r -d '' f; do [ -e "$f" ] && printf '%s\0' "$f"; done \
  | tar --null -T - -cf - | tar -x -C "$tmp/head"

# A major release moves the module path (Go semantic import versioning:
# .../workflow-connectors -> .../workflow-connectors/v2). apidiff compares packages
# by import path, so put the base under the head's path first; the
# major-version rule below then decides whether its changes are allowed.
BASE_MOD=$(awk '$1 == "module" { print $2; exit }' "$tmp/base/go.mod")
if [ "$BASE_MOD" != "$MOD" ]; then
  echo "Module path changed since ${BASE}: ${BASE_MOD} -> ${MOD}; comparing ${BASE} under ${MOD}."
  perl -pi -e 's#^module \Q'"$BASE_MOD"'\E\s*$#module '"$MOD"'\n#' "$tmp/base/go.mod"
  find "$tmp/base" -name '*.go' -print0 \
    | xargs -0 perl -pi -e 's#"\Q'"$BASE_MOD"'\E/#"'"$MOD"'/#g'
fi

# Loading errors (a tree that does not type-check) must be visible: apidiff's
# stderr is kept, and set -e stops here on a failure.
(cd "$tmp/base" && "$tmp/bin/apidiff" -m -w "$tmp/base.export" "$MOD")
(cd "$tmp/head" && "$tmp/bin/apidiff" -m -w "$tmp/head.export" "$MOD")

# test/ holds only _test.go packages: no API, so their "package added" lines
# are noise. Everything importable lives under pkg/.
only_api() { grep -vE "^- package ${MOD//./\\.}/test(/|$)" || true; }
"$tmp/bin/apidiff" -m "$tmp/base.export" "$tmp/head.export" | only_api > "$tmp/all.txt"
"$tmp/bin/apidiff" -m -incompatible "$tmp/base.export" "$tmp/head.export" | only_api | grep -E '^- ' > "$tmp/incompatible.txt" || true

echo "::group::API changes since ${BASE}"
if [ -s "$tmp/all.txt" ]; then cat "$tmp/all.txt"; else echo "(none)"; fi
echo "::endgroup::"

if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
  {
    echo "### API compatibility vs \`${BASE}\`${NEW:+ (releasing \`${NEW}\`)}"
    echo
    if [ -s "$tmp/all.txt" ]; then
      echo '```'
      cat "$tmp/all.txt"
      echo '```'
    else
      echo "No exported API change."
    fi
  } >> "$GITHUB_STEP_SUMMARY"
fi

if [ ! -s "$tmp/incompatible.txt" ]; then
  echo "No incompatible API change since ${BASE}."
  exit 0
fi

if [ -n "$NEW" ] && [ "$(major "$NEW")" -gt "$(major "$BASE")" ]; then
  echo "Incompatible API changes since ${BASE}, allowed by the major-version bump to ${NEW}."
  exit 0
fi

while IFS= read -r line; do
  echo "::${level} title=Incompatible API change since ${BASE}::${line#- }"
done < "$tmp/incompatible.txt"

if [ "$level" = error ]; then
  echo "${NEW} is not a major release, but the exported API changed incompatibly since ${BASE} (above)."
  echo "Restore compatibility, or release a new major version: v$(( $(major "$BASE") + 1 )).0.0 with the module path ending in /v$(( $(major "$BASE") + 1 )) (every consumer then changes its imports)."
  exit 1
fi
echo "Incompatible API changes since ${BASE} (warning only in CI; the release gate fails a non-major release on them)."
