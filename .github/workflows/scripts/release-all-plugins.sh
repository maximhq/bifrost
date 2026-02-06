#!/usr/bin/env bash
set -euo pipefail

# Release all changed plugins sequentially
# Usage: ./release-all-plugins.sh '["plugin1", "plugin2"]'

# Validate that an argument was provided
if [ $# -eq 0 ]; then
  echo "❌ Error: Missing required argument"
  echo "Usage: $0 '<JSON_ARRAY_OF_PLUGINS>'"
  echo "Example: $0 '[\"plugin1\", \"plugin2\"]'"
  exit 1
fi

CHANGED_PLUGINS_JSON="$1"

# Verify jq is available
if ! command -v jq >/dev/null 2>&1; then
  echo "❌ Error: jq is required but not installed"
  echo "Please install jq to parse JSON input"
  exit 1
fi

# Validate that the input is valid JSON
if ! echo "$CHANGED_PLUGINS_JSON" | jq empty >/dev/null 2>&1; then
  echo "❌ Error: Invalid JSON provided"
  echo "Input: $CHANGED_PLUGINS_JSON"
  echo "Please provide a valid JSON array of plugin names"
  exit 1
fi


echo "🔌 Processing plugin releases..."
echo "📋 Changed plugins JSON: $CHANGED_PLUGINS_JSON"

# No work early‐exit if array is empty
if jq -e 'length==0' <<<"$CHANGED_PLUGINS_JSON" >/dev/null 2>&1; then
  echo "⏭️ No plugins to release"
  echo "success=true" >> "${GITHUB_OUTPUT:-/dev/null}"
  exit 0
fi

# Convert JSON array to bash array using readarray to avoid word-splitting
if ! readarray -t PLUGINS < <(echo "$CHANGED_PLUGINS_JSON" | jq -r '.[]' 2>/dev/null); then
  echo "❌ Error: Failed to parse plugin names from JSON"
  echo "Input: $CHANGED_PLUGINS_JSON"
  exit 1
fi

# Verify release-single-plugin.sh exists and is executable
RELEASE_SCRIPT="./.github/workflows/scripts/release-single-plugin.sh"
if [ ! -f "$RELEASE_SCRIPT" ]; then
  echo "❌ Error: Release script not found: $RELEASE_SCRIPT"
  exit 1
fi

if [ ! -x "$RELEASE_SCRIPT" ]; then
  echo "❌ Error: Release script is not executable: $RELEASE_SCRIPT"
  exit 1
fi

if [ ${#PLUGINS[@]} -eq 0 ]; then
  echo "⏭️ No plugins to release"
  echo "success=true" >> "${GITHUB_OUTPUT:-/dev/null}"
  exit 0
fi

echo "🔄 Releasing ${#PLUGINS[@]} plugins:"
for p in "${PLUGINS[@]}"; do
  echo "   • $p"
done

FAILED_PLUGINS=()
SUCCESS_COUNT=0
OVERALL_EXIT_CODE=0

# Release each plugin
for plugin in "${PLUGINS[@]}"; do
  echo ""
  echo "🔌 Releasing plugin: $plugin"

  # Capture the exit code of the plugin release
  if "$RELEASE_SCRIPT" "$plugin"; then
    PLUGIN_EXIT_CODE=$?
    echo "✅ Successfully released: $plugin"
    SUCCESS_COUNT=$((SUCCESS_COUNT + 1))
  else
    PLUGIN_EXIT_CODE=$?
    echo "❌ Failed to release plugin '$plugin' (exit code: $PLUGIN_EXIT_CODE)"
    FAILED_PLUGINS+=("$plugin")
    OVERALL_EXIT_CODE=1
  fi
done

# Summary
echo ""
echo "📋 Plugin Release Summary:"
echo "   ✅ Successful: $SUCCESS_COUNT/${#PLUGINS[@]}"
echo "   ❌ Failed: ${#FAILED_PLUGINS[@]}"

if [ ${#FAILED_PLUGINS[@]} -gt 0 ]; then
  echo "   Failed plugins: ${FAILED_PLUGINS[*]}"
  echo "success=false" >> "${GITHUB_OUTPUT:-/dev/null}"
  echo "❌ Plugin release process completed with failures"
  exit $OVERALL_EXIT_CODE
else
  echo "   🎉 All plugins released successfully!"
  echo "success=true" >> "${GITHUB_OUTPUT:-/dev/null}"
  echo "✅ All plugin releases completed successfully"
fi
