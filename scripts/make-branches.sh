#!/usr/bin/env bash
# Builds the three workshop branches from the marked-up source tree:
#   solution      everything traced (markers removed)
#   checkpoint-1  tracer provider + server handler only
#   main          the starter: logs and metrics, no tracing
#
# In transfers/*.go, code between "// >>> STEPn" and "// <<< STEPn" belongs to
# step n; a "// STEPn-ALT: <code>" line is what the starter has instead.
# Run from a clean working tree. Needs git and Go.
set -euo pipefail
cd "$(dirname "$0")/.."

[ -z "$(git status --porcelain 2>/dev/null)" ] || { echo "commit or stash your changes first"; exit 1; }

remove_step() { # drop step $1's code, keep its ALT lines
  local s="$1" f
  for f in transfers/*.go; do
    awk -v s="$s" '
      $0 ~ "// >>> " s "$" { skip = 1; next }
      $0 ~ "// <<< " s "$" { skip = 0; next }
      skip { next }
      { sub("// " s "-ALT: ", ""); print }' "$f" > "$f.tmp" && mv "$f.tmp" "$f"
  done
}

keep_step() { # keep step $1's code, drop its markers and ALT lines
  local s="$1" f
  for f in transfers/*.go; do
    awk -v s="$s" '
      $0 ~ "// (>>>|<<<) " s "$" { next }
      $0 ~ "// " s "-ALT: " { next }
      { print }' "$f" > "$f.tmp" && mv "$f.tmp" "$f"
  done
}

fix_imports() {
  if command -v goimports >/dev/null; then goimports -w transfers
  else go run golang.org/x/tools/cmd/goimports@latest -w transfers; fi
  gofmt -w transfers
  go build ./...
}

source_ref=$(git rev-parse --abbrev-ref HEAD)

git checkout -q -B solution "$source_ref"
keep_step STEP1; keep_step STEP2; fix_imports
git commit -qam "solution: fully traced transfers service"

git checkout -q -B checkpoint-1 "$source_ref"
keep_step STEP1; remove_step STEP2; fix_imports
git commit -qam "checkpoint-1: tracer provider and server handler"

git checkout -q -B main "$source_ref"
remove_step STEP1; remove_step STEP2; git rm -q transfers/telemetry.go; fix_imports
git commit -qam "starter: logs and metrics, tracing to be added"

git checkout -q "$source_ref"
echo "Built branches: main (starter), checkpoint-1, solution. Source stays on $source_ref."
