#!/usr/bin/env bash
set -euo pipefail

# Validate that Go config types in transports/bifrost-http/lib/config.go
# stay in sync (fields + enum values) with transports/config.schema.json.
# Walks the type graph recursively via go/types rather than regex-parsing source.

if command -v readlink >/dev/null 2>&1 && readlink -f "$0" >/dev/null 2>&1; then
  SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
else
  SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd -P)"
fi
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
TOOL_DIR="$SCRIPT_DIR/schemasync"

cd "$REPO_ROOT"

if ! command -v go >/dev/null 2>&1; then
  echo "❌ go toolchain required for schema-sync validation"
  exit 2
fi

# Ensure go.work exists at the repo root. schemasync's packages.Load needs
# it to resolve bifrost's local modules against each other. On fresh CI
# runners go.work is not checked in, so we provision it here inline.
# Sibling scripts (test-bifrost-http.sh etc.) call setup-go-workspace.sh
# via `source`, but that relies on the `return` builtin which has
# platform-dependent edge cases under `set -e`; we instead do the same
# work inline so this wrapper is self-contained.
if [ ! -f "$REPO_ROOT/go.work" ]; then
  echo "🔧 Setting up Go workspace (go.work not found)..."
  (
    cd "$REPO_ROOT"
    go work init
    for mod in ./core ./framework \
               ./plugins/compat ./plugins/governance ./plugins/jsonparser \
               ./plugins/logging ./plugins/maxim ./plugins/mocker \
               ./plugins/otel ./plugins/prompts ./plugins/semanticcache \
               ./plugins/telemetry \
               ./transports ./cli; do
      if [ -f "$REPO_ROOT/$mod/go.mod" ]; then
        go work use "$mod"
      fi
    done
  )
  echo "✅ Go workspace initialized at $REPO_ROOT/go.work"
else
  echo "🔍 Go workspace already exists at $REPO_ROOT/go.work, skipping initialization"
fi

echo "🔍 Validating Go ↔ config.schema.json sync (recursive, AST-based)"
echo "=================================================================="

# The schemasync tool is its own module (separate go.mod). Build it with
# GOWORK=off so the tool's deps (golang.org/x/tools) resolve against its
# own go.mod, not the repo's go.work. At runtime the tool itself sets
# GOWORK=<repo-root>/go.work when loading bifrost packages.
(cd "$TOOL_DIR" && GOWORK=off go build -o /tmp/schemasync .)
/tmp/schemasync \
  --schema "$REPO_ROOT/transports/config.schema.json" \
  --pkg-root "$REPO_ROOT" \
  --helm-values "$REPO_ROOT/helm-charts/bifrost/values.schema.json" \
  --helm-helpers "$REPO_ROOT/helm-charts/bifrost/templates/_helpers.tpl"
