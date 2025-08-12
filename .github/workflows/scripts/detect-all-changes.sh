#!/bin/bash
set -euo pipefail

# Detect what components need to be released based on version changes
# Usage: ./detect-all-changes.sh

echo "🔍 Auto-detecting version changes across all components..."

# Initialize outputs
CORE_NEEDS_RELEASE="false"
FRAMEWORK_NEEDS_RELEASE="false"
PLUGINS_NEED_RELEASE="false"
BIFROST_HTTP_NEEDS_RELEASE="false"
CHANGED_PLUGINS="[]"

# Get current versions
CORE_VERSION=$(cat core/version)
FRAMEWORK_VERSION=$(cat framework/version)
TRANSPORT_VERSION=$(cat transports/version)

echo "📦 Current versions:"
echo "   Core: $CORE_VERSION"
echo "   Framework: $FRAMEWORK_VERSION"  
echo "   Transport: $TRANSPORT_VERSION"

START_FROM="none"

# Check Core
echo ""
echo "🔧 Checking core..."
CORE_TAG="core/v${CORE_VERSION}"
if git rev-parse --verify "$CORE_TAG" >/dev/null 2>&1; then
  echo "   ⏭️ Tag $CORE_TAG already exists"
else
  # Get previous version
  LATEST_CORE_TAG=$(git tag -l "core/v*" | sort -V | tail -1)
  if [ -z "$LATEST_CORE_TAG" ]; then
    echo "   ✅ First core release: $CORE_VERSION"
    CORE_NEEDS_RELEASE="true"
  else
    PREVIOUS_CORE_VERSION=${LATEST_CORE_TAG#core/v}
    echo "   📋 Previous: $PREVIOUS_CORE_VERSION, Current: $CORE_VERSION"
    if [ "$(printf '%s\n' "$PREVIOUS_CORE_VERSION" "$CORE_VERSION" | sort -V | tail -1)" = "$CORE_VERSION" ] && [ "$PREVIOUS_CORE_VERSION" != "$CORE_VERSION" ]; then
      echo "   ✅ Core version incremented: $PREVIOUS_CORE_VERSION → $CORE_VERSION"
      CORE_NEEDS_RELEASE="true"
    else
      echo "   ⏭️ No core version increment"
    fi
  fi
fi

# Check Framework
echo ""
echo "📦 Checking framework..."
FRAMEWORK_TAG="framework/v${FRAMEWORK_VERSION}"
if git rev-parse --verify "$FRAMEWORK_TAG" >/dev/null 2>&1; then
  echo "   ⏭️ Tag $FRAMEWORK_TAG already exists"
else
  LATEST_FRAMEWORK_TAG=$(git tag -l "framework/v*" | sort -V | tail -1)
  if [ -z "$LATEST_FRAMEWORK_TAG" ]; then
    echo "   ✅ First framework release: $FRAMEWORK_VERSION"
    FRAMEWORK_NEEDS_RELEASE="true"
  else
    PREVIOUS_FRAMEWORK_VERSION=${LATEST_FRAMEWORK_TAG#framework/v}
    echo "   📋 Previous: $PREVIOUS_FRAMEWORK_VERSION, Current: $FRAMEWORK_VERSION"
    if [ "$(printf '%s\n' "$PREVIOUS_FRAMEWORK_VERSION" "$FRAMEWORK_VERSION" | sort -V | tail -1)" = "$FRAMEWORK_VERSION" ] && [ "$PREVIOUS_FRAMEWORK_VERSION" != "$FRAMEWORK_VERSION" ]; then
      echo "   ✅ Framework version incremented: $PREVIOUS_FRAMEWORK_VERSION → $FRAMEWORK_VERSION"
      FRAMEWORK_NEEDS_RELEASE="true"
    else
      echo "   ⏭️ No framework version increment"
    fi
  fi
fi

# Check Plugins
echo ""
echo "🔌 Checking plugins..."
PLUGIN_CHANGES=()

for plugin_dir in plugins/*/; do
  if [ ! -d "$plugin_dir" ]; then
    continue
  fi
  
  plugin_name=$(basename "$plugin_dir")
  version_file="${plugin_dir}version"
  
  if [ ! -f "$version_file" ]; then
    echo "   ⚠️ No version file for: $plugin_name"
    continue
  fi
  
  current_version=$(cat "$version_file" | tr -d '\n\r')
  if [ -z "$current_version" ]; then
    echo "   ⚠️ Empty version file for: $plugin_name"
    continue
  fi
  
  tag_name="plugins/${plugin_name}/v${current_version}"
  echo "   📦 Plugin: $plugin_name (v$current_version)"
  
  if git rev-parse --verify "$tag_name" >/dev/null 2>&1; then
    echo "      ⏭️ Tag already exists"
    continue
  fi
  
  latest_tag=$(git tag -l "plugins/${plugin_name}/v*" | sort -V | tail -1)
  if [ -z "$latest_tag" ]; then
    echo "      ✅ First release"
    PLUGIN_CHANGES+=("$plugin_name")
  else
    previous_version=${latest_tag#plugins/${plugin_name}/v}
    if [ "$(printf '%s\n' "$previous_version" "$current_version" | sort -V | tail -1)" = "$current_version" ] && [ "$previous_version" != "$current_version" ]; then
      echo "      ✅ Version incremented: $previous_version → $current_version"
      PLUGIN_CHANGES+=("$plugin_name")
    else
      echo "      ⏭️ No version increment"
    fi
  fi
done

if [ ${#PLUGIN_CHANGES[@]} -gt 0 ]; then
  PLUGINS_NEED_RELEASE="true"
  echo "   🔄 Plugins with changes: ${PLUGIN_CHANGES[*]}"
else
  echo "   ⏭️ No plugin changes detected"
fi

# Check Bifrost HTTP
echo ""
echo "🚀 Checking bifrost-http..."
TRANSPORT_TAG="transports/v${TRANSPORT_VERSION}"
if git rev-parse --verify "$TRANSPORT_TAG" >/dev/null 2>&1; then
  echo "   ⏭️ Tag $TRANSPORT_TAG already exists"
else
  LATEST_TRANSPORT_TAG=$(git tag -l "transports/v*" | sort -V | tail -1)
  if [ -z "$LATEST_TRANSPORT_TAG" ]; then
    echo "   ✅ First transport release: $TRANSPORT_VERSION"
    BIFROST_HTTP_NEEDS_RELEASE="true"
  else
    PREVIOUS_TRANSPORT_VERSION=${LATEST_TRANSPORT_TAG#transports/v}
    echo "   📋 Previous: $PREVIOUS_TRANSPORT_VERSION, Current: $TRANSPORT_VERSION"
    if [ "$(printf '%s\n' "$PREVIOUS_TRANSPORT_VERSION" "$TRANSPORT_VERSION" | sort -V | tail -1)" = "$TRANSPORT_VERSION" ] && [ "$PREVIOUS_TRANSPORT_VERSION" != "$TRANSPORT_VERSION" ]; then
      echo "   ✅ Transport version incremented: $PREVIOUS_TRANSPORT_VERSION → $TRANSPORT_VERSION"
      BIFROST_HTTP_NEEDS_RELEASE="true"
    else
      echo "   ⏭️ No transport version increment"
    fi
  fi
fi

# Convert plugin array to JSON
if [ ${#PLUGIN_CHANGES[@]} -eq 0 ]; then
  CHANGED_PLUGINS_JSON="[]"
else
  CHANGED_PLUGINS_JSON=$(printf '%s\n' "${PLUGIN_CHANGES[@]}" | jq -R . | jq -s .)
fi

# Summary
echo ""
echo "📋 Release Summary:"
echo "   Core: $CORE_NEEDS_RELEASE (v$CORE_VERSION)"
echo "   Framework: $FRAMEWORK_NEEDS_RELEASE (v$FRAMEWORK_VERSION)"
echo "   Plugins: $PLUGINS_NEED_RELEASE (${#PLUGIN_CHANGES[@]} plugins)"
echo "   Bifrost HTTP: $BIFROST_HTTP_NEEDS_RELEASE (v$TRANSPORT_VERSION)"

# Set outputs
echo "core-needs-release=$CORE_NEEDS_RELEASE" >> $GITHUB_OUTPUT
echo "framework-needs-release=$FRAMEWORK_NEEDS_RELEASE" >> $GITHUB_OUTPUT
echo "plugins-need-release=$PLUGINS_NEED_RELEASE" >> $GITHUB_OUTPUT
echo "bifrost-http-needs-release=$BIFROST_HTTP_NEEDS_RELEASE" >> $GITHUB_OUTPUT
echo "changed-plugins=$CHANGED_PLUGINS_JSON" >> $GITHUB_OUTPUT
echo "core-version=$CORE_VERSION" >> $GITHUB_OUTPUT
echo "framework-version=$FRAMEWORK_VERSION" >> $GITHUB_OUTPUT
echo "transport-version=$TRANSPORT_VERSION" >> $GITHUB_OUTPUT
