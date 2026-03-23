#!/usr/bin/env bash
set -euo pipefail

# Prepare bifrost-http release: update dependencies, build UI, validate, commit/push
# Usage: ./release-bifrost-http-prep.sh <version>

# Get the absolute path of the script directory
# Use readlink if available (Linux), otherwise use cd/pwd (macOS compatible)
if command -v readlink >/dev/null 2>&1 && readlink -f "$0" >/dev/null 2>&1; then
  SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
else
  SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd -P)"
fi

# Source Go utilities for exponential backoff
source "$SCRIPT_DIR/go-utils.sh"

# Validate input argument
if [ "${1:-}" = "" ]; then
  echo "Usage: $0 <version>" >&2
  exit 1
fi

VERSION="$1"

echo "🚀 Preparing bifrost-http v$VERSION release..."

# Get core and framework versions from version files
CORE_VERSION="v$(tr -d '\n\r' < core/version)"
FRAMEWORK_VERSION="v$(tr -d '\n\r' < framework/version)"

echo "🔍 DEBUG: CORE_VERSION: $CORE_VERSION"
echo "🔍 DEBUG: FRAMEWORK_VERSION: $FRAMEWORK_VERSION"


# Get plugin versions from version files
echo "🔌 Getting plugin versions from version files..."
declare -A PLUGIN_VERSIONS

# Get versions for plugins that exist in the plugins/ directory
for plugin_dir in plugins/*/; do
  if [ -d "$plugin_dir" ]; then
    plugin_name=$(basename "$plugin_dir")
    PLUGIN_VERSION="v$(tr -d '\n\r' < "${plugin_dir}version")"
    PLUGIN_VERSIONS["$plugin_name"]="$PLUGIN_VERSION"
    echo "   📦 $plugin_name: $PLUGIN_VERSION (from version file)"
  fi
done

# Also check for any plugins already in transport go.mod that might not be in plugins/ directory
cd transports
echo "🔍 Checking for additional plugins in transport go.mod..."
# Parse go.mod plugin lines and add missing ones
while IFS= read -r plugin_line; do
  plugin_name=$(echo "$plugin_line" | awk -F'/' '{print $NF}' | awk '{print $1}')
  current_version=$(echo "$plugin_line" | awk '{print $NF}')

  # Only add if we don't already have this plugin
  if [[ -z "${PLUGIN_VERSIONS[$plugin_name]:-}" ]]; then
    echo "   📦 $plugin_name: $current_version (from transport go.mod)"
    PLUGIN_VERSIONS["$plugin_name"]="$current_version"
  fi
done < <(grep "github.com/maximhq/bifrost/plugins/" go.mod)
cd ..

echo "🔧 Using versions:"
echo "   Core: $CORE_VERSION"
echo "   Framework: $FRAMEWORK_VERSION"
echo "   Plugins:"
for plugin_name in "${!PLUGIN_VERSIONS[@]}"; do
  echo "     - $plugin_name: ${PLUGIN_VERSIONS[$plugin_name]}"
done

# Update transport dependencies to use plugin versions from version files
echo "🔧 Using plugin versions from version files for transport..."

# Track which plugins are actually used by the transport
cd transports
for plugin_name in "${!PLUGIN_VERSIONS[@]}"; do
  plugin_version="${PLUGIN_VERSIONS[$plugin_name]}"

  # Check if transport depends on this plugin
  if grep -q "github.com/maximhq/bifrost/plugins/$plugin_name" go.mod; then
    echo "  📦 Using $plugin_name plugin $plugin_version"
    go_get_with_backoff "github.com/maximhq/bifrost/plugins/$plugin_name@$plugin_version"
  fi
done

# Also ensure core and framework are up to date

echo "  🔧 Updating core to $CORE_VERSION"
go_get_with_backoff "github.com/maximhq/bifrost/core@$CORE_VERSION"

echo "  📦 Updating framework to $FRAMEWORK_VERSION"
go_get_with_backoff "github.com/maximhq/bifrost/framework@$FRAMEWORK_VERSION"

go mod tidy

cd ..

# We need to build UI first before we can validate the transport build
echo "🎨 Building UI..."
make build-ui

# Building hello-world plugin
echo "🔨 Building hello-world plugin..."
cd examples/plugins/hello-world
make build
cd ../../..

# Validate transport build
echo "🔨 Validating transport build..."
cd transports
go build ./...
cd ..
echo "✅ Transport build validation successful"

# Note: Migration tests run as a separate CI job (test-migrations) before this release job

# Commit and push changes if any
# First, pull latest changes to avoid conflicts
CURRENT_BRANCH="$(git rev-parse --abbrev-ref HEAD)"
if [ "$CURRENT_BRANCH" = "HEAD" ]; then
  # In detached HEAD state (common in CI), use GITHUB_REF_NAME or default to main
  CURRENT_BRANCH="${GITHUB_REF_NAME:-main}"
fi

echo "Pulling latest changes from origin/$CURRENT_BRANCH..."
if ! git pull origin "$CURRENT_BRANCH"; then
  echo "❌ Error: git pull origin $CURRENT_BRANCH failed"
  exit 1
fi

# Stage any changes made to transports/
git add transports/

# Check if there are staged changes after pulling
if ! git diff --cached --quiet; then
  git config user.name "github-actions[bot]"
  git config user.email "41898282+github-actions[bot]@users.noreply.github.com"
  echo "🔧 Committing and pushing changes..."
  git commit -m "transports: update dependencies --skip-pipeline"
  git push -u origin HEAD
else
  echo "ℹ️ No staged changes to commit"
fi

echo "✅ Prep complete for bifrost-http v$VERSION"
echo "success=true" >> "$GITHUB_OUTPUT"
