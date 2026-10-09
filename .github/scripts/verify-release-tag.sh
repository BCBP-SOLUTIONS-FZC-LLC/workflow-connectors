#!/usr/bin/env bash
# Release front gate (release.yml's verify job). Ensures a workflow_dispatch
# was started from the release branch or from refs/tags/RELEASE_TAG — GitHub
# runs release.yml and the reusable validate workflows from the *dispatching*
# ref, so a dispatch from a feature branch would release the tag with that
# branch's unreviewed pipeline — that the checked-out HEAD is RELEASE_TAG's
# commit (exact tag, not branch tip), that the commit is on the release branch (origin/main, or
# origin/$RELEASE_BRANCH) so a tag pushed on an unreviewed branch is never
# released, and that the tag's major version matches the module path: Go
# requires a /vN suffix for v2 and later, so a v2+ tag on a path without it
# (or a v0/v1 tag on a /vN path) publishes a version `go get` cannot use.
#
# Needs the release branch's remote-tracking ref: release.yml checks out with
# fetch-depth: 0, which fetches every branch.
set -euo pipefail

: "${RELEASE_TAG:?RELEASE_TAG is required}"
release_branch="${RELEASE_BRANCH:-main}"

# Dispatch ref (GITHUB_EVENT_NAME / GITHUB_REF are set by the runner).
if [ "${GITHUB_EVENT_NAME:-push}" = "workflow_dispatch" ]; then
  case "${GITHUB_REF:-}" in
    "refs/heads/${release_branch}" | "refs/tags/${RELEASE_TAG}")
      echo "  ✔  dispatched from ${GITHUB_REF}"
      ;;
    *)
      echo "::error title=Dispatch ref::workflow_dispatch from '${GITHUB_REF:-}' is refused — the release workflow and its reusable validate workflows would run from that ref, not from ${RELEASE_TAG}. Re-run with 'Use workflow from' set to ${release_branch} or to the tag ${RELEASE_TAG}."
      exit 1
      ;;
  esac
fi

# Every tag on HEAD, not just the one git describe happens to pick when
# several tags point at the same commit. Captured first: with pipefail,
# grep -q exiting early could fail the pipeline through SIGPIPE.
head_tags=$(git tag --points-at HEAD)
if ! grep -qxF -- "$RELEASE_TAG" <<<"$head_tags"; then
  echo "::error title=Tag mismatch::RELEASE_TAG=${RELEASE_TAG} does not point at the checked-out HEAD"
  exit 1
fi

if ! git rev-parse --verify --quiet "refs/remotes/origin/${release_branch}^{commit}" >/dev/null; then
  echo "::error title=Release branch missing::origin/${release_branch} is not available; check out with fetch-depth: 0"
  exit 1
fi
if ! git merge-base --is-ancestor HEAD "refs/remotes/origin/${release_branch}"; then
  echo "::error title=Tag not on ${release_branch}::RELEASE_TAG=${RELEASE_TAG} points at a commit that is not on origin/${release_branch}"
  exit 1
fi

if ! [[ "$RELEASE_TAG" =~ ^v([0-9]+)\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
  echo "::error title=Tag format::RELEASE_TAG=${RELEASE_TAG} is not a semantic version (vMAJOR.MINOR.PATCH)"
  exit 1
fi
major="${BASH_REMATCH[1]}"
# Read from go.mod: this runs before Go is set up in the release job.
module=$(awk '/^module /{print $2; exit}' go.mod)
if [[ "$module" =~ /v([0-9]+)$ ]]; then
  suffix="${BASH_REMATCH[1]}"
else
  suffix=""
fi
if (( major >= 2 )); then
  if [ "$suffix" != "$major" ]; then
    echo "::error file=go.mod,title=Module path::tag ${RELEASE_TAG} needs module path .../v${major}; go.mod declares ${module}"
    exit 1
  fi
elif [ -n "$suffix" ]; then
  echo "::error file=go.mod,title=Module path::tag ${RELEASE_TAG} cannot be released from module path ${module} (/v${suffix})"
  exit 1
fi
echo "  ✔  tag verified: ${RELEASE_TAG} (module ${module})"
