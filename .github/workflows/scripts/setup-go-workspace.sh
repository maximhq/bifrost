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

# The transport imports requestguard before its first public module tag exists.
# Keep the workspace use entry for plugin tests, and pin the transport's exact
# requirement to the local source so Go does not query the module proxy.
for module_dir in core framework plugins/requestguard; do
  module_path="$(awk '$1 == "module" { print $2; exit }' "$module_dir/go.mod" | tr -d '\r')"
  module_version="$(awk -v module="$module_path" '$1 == module { print $2; exit }' transports/go.mod | tr -d '\r')"
  if [ -n "$module_path" ] && [ -n "$module_version" ]; then
    go work edit -replace="$module_path@$module_version=./$module_dir"
  fi
done
echo "✅ Go workspace initialized"
