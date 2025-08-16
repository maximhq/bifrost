#!/bin/bash
set -euo pipefail

# Release all changed plugins sequentially
# Usage: ./release-all-plugins.sh '["plugin1", "plugin2"]'

CHANGED_PLUGINS_JSON="$1"

echo "🔌 Processing plugin releases..."
echo "📋 Changed plugins JSON: $CHANGED_PLUGINS_JSON"

# Parse JSON array and extract plugin names
if [ "$CHANGED_PLUGINS_JSON" = "[]" ] || [ -z "$CHANGED_PLUGINS_JSON" ]; then
  echo "⏭️ No plugins to release"
  echo "success=true" >> $GITHUB_OUTPUT
  exit 0
fi

# Convert JSON array to bash array
PLUGINS=($(echo "$CHANGED_PLUGINS_JSON" | jq -r '.[]'))

if [ ${#PLUGINS[@]} -eq 0 ]; then
  echo "⏭️ No plugins to release"
  echo "success=true" >> $GITHUB_OUTPUT
  exit 0
fi

echo "🔄 Releasing ${#PLUGINS[@]} plugins: ${PLUGINS[*]}"

FAILED_PLUGINS=()
SUCCESS_COUNT=0

# Release each plugin
for plugin in "${PLUGINS[@]}"; do
  echo ""
  echo "🔌 Releasing plugin: $plugin"
  
  if ./.github/workflows/scripts/release-single-plugin.sh "$plugin"; then
    echo "✅ Successfully released: $plugin"
    SUCCESS_COUNT=$((SUCCESS_COUNT + 1))
  else
    echo "❌ Failed to release: $plugin"
    FAILED_PLUGINS+=("$plugin")
  fi
done

# Summary
echo ""
echo "📋 Plugin Release Summary:"
echo "   ✅ Successful: $SUCCESS_COUNT/${#PLUGINS[@]}"
echo "   ❌ Failed: ${#FAILED_PLUGINS[@]}"

if [ ${#FAILED_PLUGINS[@]} -gt 0 ]; then
  echo "   Failed plugins: ${FAILED_PLUGINS[*]}"
  echo "success=false" >> $GITHUB_OUTPUT
  exit 1
else
  echo "   🎉 All plugins released successfully!"
  echo "success=true" >> $GITHUB_OUTPUT
fi
