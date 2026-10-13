#!/usr/bin/env bash
# Compiles the worker skeleton in docs/integration/connector-worker.md
# (the ```go block under "## Appendix: worker skeleton") against this
# checkout, so the documented wiring can never drift from the API.
#
# The block is built as package `worker` of a scratch module that requires
# this module through a `replace` to the checkout. The scratch go.mod copies
# the root go.mod's requirements and go.sum, so the build needs no network
# (only the module cache `go mod download` fills) and adds nothing to the
# repository. `go vet` runs on it too.
#
# Usage: scripts/docs_compile.sh  (from the repository root; `make docs-compile`)
# DOCS_COMPILE_DOC=<file> checks another Markdown file with the same appendix.
set -euo pipefail

root=$(pwd)
doc=${DOCS_COMPILE_DOC:-docs/integration/connector-worker.md}
go_bin=${GO:-go}

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/worker"

# The first ```go block after the appendix heading, without its fences.
awk '
  /^## Appendix: worker skeleton/ { appendix = 1; next }
  appendix && !inblock && /^```go[[:space:]]*$/ { inblock = 1; next }
  inblock && /^```[[:space:]]*$/ { exit }
  inblock { print }
' "$doc" > "$work/worker/worker.go"

if [ ! -s "$work/worker/worker.go" ]; then
  echo "docs-compile: no \`\`\`go block under \"## Appendix: worker skeleton\" in $doc" >&2
  exit 1
fi

module=$(sed -n 's/^module[[:space:]]\{1,\}//p' go.mod)
# Any version of the module's major works: the replace points at the checkout.
major=${module##*/}
case "$major" in
  v[0-9]*) version="$major.0.0" ;;
  *) version=v0.0.0 ;;
esac
# A go.mod interpreted string: the checkout path may hold spaces.
quote() { printf '"%s"' "$(printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g')"; }
{
  echo "module example.invalid/docscompile"
  echo
  sed -n '/^go[[:space:]]/p; /^toolchain[[:space:]]/p' go.mod
  echo
  echo "require $module $version"
  echo
  echo "replace $module => $(quote "$root")"
  echo
  # The root's own requirements, so the scratch module selects the same
  # versions and the copied go.sum covers them.
  sed -n '/^require[[:space:]]*(/,/^)/p; /^require[[:space:]][^(]/p' go.mod
  echo
  # The root's own replace directives, which the build of the checkout
  # needs as well (replaces apply only in the main module). A relative
  # target ("./x", "../x") is made absolute against the checkout.
  awk -v root="$root" '
    function directive(line,   arrow, left, right, target, rest, bare, close_at) {
      arrow = index(line, "=>")
      if (arrow == 0) return line
      left = substr(line, 1, arrow + 1)
      right = substr(line, arrow + 2)
      sub(/^[[:space:]]+/, "", right)
      target = right; rest = ""
      if (substr(right, 1, 1) == "\"") {
        close_at = index(substr(right, 2), "\"")
        if (close_at > 0) { target = substr(right, 1, close_at + 1); rest = substr(right, close_at + 2) }
      } else if (match(right, /[[:space:]]/)) {
        target = substr(right, 1, RSTART - 1); rest = substr(right, RSTART)
      }
      bare = target
      gsub(/^"|"$/, "", bare)
      if (bare ~ /^\.\.?\//) {
        target = root "/" bare
        gsub(/\\/, "\\\\", target); gsub(/"/, "\\\"", target)
        target = "\"" target "\""
      }
      return left " " target rest
    }
    /^replace[[:space:]]*\(/ { inblock = 1; print; next }
    inblock && /^\)/ { inblock = 0; print; next }
    inblock { print directive($0); next }
    /^replace[[:space:]]/ { print directive($0) }
  ' go.mod
} > "$work/go.mod"
cp go.sum "$work/go.sum"

(
  cd "$work"
  # GOWORK=off: a go.work above the scratch directory (or GOWORK in the
  # environment) must not take over module resolution. -mod=mod on the
  # command line rather than in GOFLAGS, so the caller's GOFLAGS are kept.
  export GOWORK=off
  "$go_bin" build -mod=mod ./worker
  "$go_bin" vet -mod=mod ./worker
)
echo "docs-compile: $doc worker skeleton compiles"
