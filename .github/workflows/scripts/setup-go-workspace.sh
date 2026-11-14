#!/usr/bin/env bash
set -euo pipefail

export GOTOOLCHAIN=auto

# If go.work exists, skip
if [ -f "go.work" ]; then
  echo "🔍 Go workspace already exists, skipping initialization"
  return
fi


# Setup Go workspace for CI
# Usage: source setup-go-workspace.sh
echo "🔧 Setting up Go workspace..."
if [ -f "go.work" ]; then
  echo "✅ Go workspace already exists, skipping init"
  return 0 2>/dev/null || exit 0
fi
go work init
go work use ./core
go work use ./framework
go work use ./transports
go work use ./cli
# The Warp runner builds tests/cmd/seed/warpseed inside the workspace; its own go.sum
# does not cover framework's dependencies, so it cannot build with GOWORK=off.
go work use ./tests/cmd/seed
# Discover plugin modules instead of listing them, so a newly added plugin
# never needs a matching CI edit. test-all-plugins.sh globs plugins/*/go.mod
# the same way, so the two lists cannot drift apart.
for plugin_dir in ./plugins/*/; do
  if [ -f "$plugin_dir/go.mod" ]; then
    go work use "$plugin_dir"
  fi
done
echo "✅ Go workspace initialized"
