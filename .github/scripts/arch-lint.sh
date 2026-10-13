#!/usr/bin/env bash
# Enforces .go-arch-lint.yml rules with a pinned go-arch-lint.
#
# The binary is installed (go install, once) into a version-specific cache
# directory and its reported version is checked before use, so neither CI
# nor a developer's machine runs whatever go-arch-lint happens to be first on
# PATH — a different version can parse or enforce the rules differently.
# CI caches GO_ARCH_LINT_DIR (validate-quality.yml, key includes the version).
#
# Env: GO_ARCH_LINT_VERSION (default v1.15.0), GO_ARCH_LINT_DIR (default
# ~/.cache/workflow-connectors/go-arch-lint/<version>), GO (default go).
set -euo pipefail

version=${GO_ARCH_LINT_VERSION:-v1.15.0}
dir=${GO_ARCH_LINT_DIR:-${XDG_CACHE_HOME:-$HOME/.cache}/workflow-connectors/go-arch-lint/$version}
bin="$dir/go-arch-lint"

# installed_version prints the version the binary reports ("Linter version:
# vX.Y.Z", ANSI colour codes stripped), or nothing.
installed_version() {
  [ -x "$bin" ] || return 0
  "$bin" version 2>/dev/null \
    | sed 's/\x1b\[[0-9;]*m//g' \
    | sed -n 's/^Linter version:[[:space:]]*//p' \
    | head -n 1
}

if [ "$(installed_version)" != "$version" ]; then
  mkdir -p "$dir"
  GOBIN="$dir" "${GO:-go}" install "github.com/fe3dback/go-arch-lint@$version"
fi
got=$(installed_version)
if [ "$got" != "$version" ]; then
  echo "arch-lint: $bin reports version '${got:-unknown}', want $version" >&2
  exit 1
fi

"$bin" check --project-path .
