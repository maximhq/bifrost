#!/usr/bin/env bash
set -euo pipefail
shopt -s nullglob

# Detect what components need to be released based on version changes
# Usage: ./detect-all-changes.sh
echo "🔍 Auto-detecting version changes across all components..."

# Initialize outputs
CORE_NEEDS_RELEASE="false"
FRAMEWORK_NEEDS_RELEASE="false"
PLUGINS_NEED_RELEASE="false"
BIFROST_HTTP_NEEDS_RELEASE="false"
DOCKER_NEEDS_RELEASE="false"
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
  # Extract major.minor track from version (e.g., "1.3.55" -> "1.3", "1.4.0-prerelease1" -> "1.4")
  CORE_BASE_VERSION=$(echo "$CORE_VERSION" | sed 's/-.*$//')
  CORE_MAJOR_MINOR=$(echo "$CORE_BASE_VERSION" | cut -d. -f1,2)
  echo "   🔍 Checking track: ${CORE_MAJOR_MINOR}.x"
  
  # Get previous version in the same track
  LATEST_CORE_TAG=$(git tag -l "core/v${CORE_MAJOR_MINOR}.*" | sort -V | tail -1)
  echo "🏷️ Latest core tag in track ${CORE_MAJOR_MINOR}.x: $LATEST_CORE_TAG"
  if [ -z "$LATEST_CORE_TAG" ]; then
    echo "   ✅ First core release in track ${CORE_MAJOR_MINOR}.x: $CORE_VERSION"
    CORE_NEEDS_RELEASE="true"
  else
    if [[ "$CORE_VERSION" == *"-"* ]]; then
      # current_version has prerelease, so include all versions but prefer stable
      ALL_TAGS=$(git tag -l "core/v${CORE_MAJOR_MINOR}.*" | sort -V)      
      STABLE_TAGS=$(echo "$ALL_TAGS" | grep -v '\-')      
      PRERELEASE_TAGS=$(echo "$ALL_TAGS" | grep '\-' || true)
      if [ -n "$STABLE_TAGS" ]; then
        # Get the highest stable version
        LATEST_CORE_TAG=$(echo "$STABLE_TAGS" | tail -1)
        echo "latest core tag (stable preferred): $LATEST_CORE_TAG"
      else
        # No stable versions, get highest prerelease
        LATEST_CORE_TAG=$(echo "$PRERELEASE_TAGS" | tail -1)
        echo "latest core tag (prerelease only): $LATEST_CORE_TAG"
      fi
    else
      # VERSION has no prerelease, so only consider stable releases in same track
      LATEST_CORE_TAG=$(git tag -l "core/v${CORE_MAJOR_MINOR}.*" | grep -v '\-' | sort -V | tail -1)
      echo "latest core tag (stable only): $LATEST_CORE_TAG"
    fi
    PREVIOUS_CORE_VERSION=${LATEST_CORE_TAG#core/v}
    echo "   📋 Previous: $PREVIOUS_CORE_VERSION, Current: $CORE_VERSION"
    # Fixed: Use head -1 instead of tail -1 for your sort -V behavior, and check against current version
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
  # Extract major.minor track from version (e.g., "1.3.55" -> "1.3", "1.4.0-prerelease1" -> "1.4")
  FRAMEWORK_BASE_VERSION=$(echo "$FRAMEWORK_VERSION" | sed 's/-.*$//')
  FRAMEWORK_MAJOR_MINOR=$(echo "$FRAMEWORK_BASE_VERSION" | cut -d. -f1,2)
  echo "   🔍 Checking track: ${FRAMEWORK_MAJOR_MINOR}.x"
  
  ALL_TAGS=$(git tag -l "framework/v${FRAMEWORK_MAJOR_MINOR}.*" | sort -V)
  STABLE_TAGS=$(echo "$ALL_TAGS" | grep -v '\-')
  PRERELEASE_TAGS=$(echo "$ALL_TAGS" | grep '\-' || true)
  LATEST_FRAMEWORK_TAG=""
  if [ -n "$STABLE_TAGS" ]; then
    LATEST_FRAMEWORK_TAG=$(echo "$STABLE_TAGS" | tail -1)
    echo "latest framework tag (stable preferred): $LATEST_FRAMEWORK_TAG"
  else
    LATEST_FRAMEWORK_TAG=$(echo "$PRERELEASE_TAGS" | tail -1)
    echo "latest framework tag (prerelease only): $LATEST_FRAMEWORK_TAG"  
  fi      
  if [ -z "$LATEST_FRAMEWORK_TAG" ]; then
    echo "   ✅ First framework release in track ${FRAMEWORK_MAJOR_MINOR}.x: $FRAMEWORK_VERSION"
    FRAMEWORK_NEEDS_RELEASE="true"
  else
    PREVIOUS_FRAMEWORK_VERSION=${LATEST_FRAMEWORK_TAG#framework/v}
    echo "   📋 Previous: $PREVIOUS_FRAMEWORK_VERSION, Current: $FRAMEWORK_VERSION"
    # Fixed: Use head -1 instead of tail -1 for your sort -V behavior, and check against current version
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

  # Extract major.minor track from version (e.g., "1.3.55" -> "1.3", "1.4.0-prerelease1" -> "1.4")
  plugin_base_version=$(echo "$current_version" | sed 's/-.*$//')
  plugin_major_minor=$(echo "$plugin_base_version" | cut -d. -f1,2)
  echo "      🔍 Checking track: ${plugin_major_minor}.x"

  if [[ "$current_version" == *"-"* ]]; then
      # current_version has prerelease, so include all versions but prefer stable
      ALL_TAGS=$(git tag -l "plugins/${plugin_name}/v${plugin_major_minor}.*" | sort -V)
      STABLE_TAGS=$(echo "$ALL_TAGS" | grep -v '\-' || true)
      PRERELEASE_TAGS=$(echo "$ALL_TAGS" | grep '\-' || true)
      
      if [ -n "$STABLE_TAGS" ]; then
        # Get the highest stable version
        LATEST_PLUGIN_TAG=$(echo "$STABLE_TAGS" | tail -1)
        echo "latest plugin tag (stable preferred): $LATEST_PLUGIN_TAG"
      else
        # No stable versions, get highest prerelease
        LATEST_PLUGIN_TAG=$(echo "$PRERELEASE_TAGS" | tail -1)
        echo "latest plugin tag (prerelease only): $LATEST_PLUGIN_TAG"
      fi
  else
    # VERSION has no prerelease, so only consider stable releases in same track
    LATEST_PLUGIN_TAG=$(git tag -l "plugins/${plugin_name}/v${plugin_major_minor}.*" | grep -v '\-' | sort -V | tail -1 || true)
    echo "latest plugin tag (stable only): $LATEST_PLUGIN_TAG"
  fi

  latest_tag=$LATEST_PLUGIN_TAG
  if [ -z "$latest_tag" ]; then
    echo "      ✅ First release in track ${plugin_major_minor}.x"
    PLUGIN_CHANGES+=("$plugin_name")
  else
    previous_version=${latest_tag#plugins/${plugin_name}/v}
    echo "previous version: $previous_version"
    echo "current version: $current_version"
    echo "latest tag: $latest_tag"
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
DOCKER_TAG_EXISTS="false"

# Check if Git tag exists
GIT_TAG_EXISTS="false"
if git rev-parse --verify "$TRANSPORT_TAG" >/dev/null 2>&1; then
  echo "   ⏭️ Git tag $TRANSPORT_TAG already exists"
  GIT_TAG_EXISTS="true"
fi

# Check if Docker tag exists on DockerHub
echo "   🐳 Checking DockerHub for tag v${TRANSPORT_VERSION}..."
DOCKER_CHECK_RESPONSE=$(curl -s "https://registry.hub.docker.com/v2/repositories/maximhq/bifrost/tags/v${TRANSPORT_VERSION}/" 2>/dev/null || echo "")
if [ -n "$DOCKER_CHECK_RESPONSE" ] && echo "$DOCKER_CHECK_RESPONSE" | grep -q '"name"'; then
  echo "   ⏭️ Docker tag v${TRANSPORT_VERSION} already exists on DockerHub"
  DOCKER_TAG_EXISTS="true"
else
  echo "   ❌ Docker tag v${TRANSPORT_VERSION} not found on DockerHub"
fi

# Determine if release is needed
if [ "$GIT_TAG_EXISTS" = "true" ] && [ "$DOCKER_TAG_EXISTS" = "true" ]; then
  echo "   ⏭️ Both Git tag and Docker image exist - no release needed"
else
  # Extract major.minor track from version (e.g., "1.3.55" -> "1.3", "1.4.0-prerelease1" -> "1.4")
  TRANSPORT_BASE_VERSION=$(echo "$TRANSPORT_VERSION" | sed 's/-.*$//')
  TRANSPORT_MAJOR_MINOR=$(echo "$TRANSPORT_BASE_VERSION" | cut -d. -f1,2)
  echo "   🔍 Checking track: ${TRANSPORT_MAJOR_MINOR}.x"
  
  # Get all transport tags in the same track, prioritize stable over prerelease for same base version
  ALL_TRANSPORT_TAGS=$(git tag -l "transports/v${TRANSPORT_MAJOR_MINOR}.*" | sort -V)
  
  # Function to get base version (remove prerelease suffix)
  get_base_version() {
    echo "$1" | sed 's/-.*$//'
  }
  
  # Find the latest version, prioritizing stable over prerelease
  LATEST_TRANSPORT_TAG=""
  LATEST_BASE_VERSION=""
  
  for tag in $ALL_TRANSPORT_TAGS; do
    version=${tag#transports/v}
    base_version=$(get_base_version "$version")
    
    # If this base version is newer, or same base version but current is stable and we had prerelease
    if [ -z "$LATEST_BASE_VERSION" ] || \
       [ "$(printf '%s\n' "$LATEST_BASE_VERSION" "$base_version" | sort -V | tail -1)" = "$base_version" ]; then
      
      if [ "$base_version" = "$LATEST_BASE_VERSION" ]; then
        # Same base version - prefer stable (no hyphen) over prerelease, otherwise take the later one
        if [[ "$version" != *"-"* ]]; then
          # Current is stable, always prefer it
          LATEST_TRANSPORT_TAG="$tag"
        elif [[ "${LATEST_TRANSPORT_TAG#transports/v}" == *"-"* ]]; then
          # Both are prereleases, take the later one (thanks to sort -V)
          LATEST_TRANSPORT_TAG="$tag"
        fi
      else
        # New base version is higher
        LATEST_TRANSPORT_TAG="$tag"
        LATEST_BASE_VERSION="$base_version"
      fi
    fi
  done
  
  if [ -n "$LATEST_TRANSPORT_TAG" ]; then
    echo "   🏷️ Latest transport tag: $LATEST_TRANSPORT_TAG"
  fi
  if [ -z "$LATEST_TRANSPORT_TAG" ]; then
    echo "   ✅ First transport release in track ${TRANSPORT_MAJOR_MINOR}.x: $TRANSPORT_VERSION"
    if [ "$GIT_TAG_EXISTS" = "false" ]; then
      echo "   🏷️  Git tag missing - transport release needed"
      BIFROST_HTTP_NEEDS_RELEASE="true"
    fi
  else
    PREVIOUS_TRANSPORT_VERSION=${LATEST_TRANSPORT_TAG#transports/v}
    echo "   📋 Previous: $PREVIOUS_TRANSPORT_VERSION, Current: $TRANSPORT_VERSION"
    
    # Function to compare versions with proper prerelease handling
    # Returns 0 if $1 < $2, 1 otherwise
    version_less_than() {
      local v1="$1"
      local v2="$2"
      
      # Extract base versions (remove prerelease suffix)
      local base1=$(echo "$v1" | sed 's/-.*$//')
      local base2=$(echo "$v2" | sed 's/-.*$//')
      
      # Compare base versions
      if [ "$base1" != "$base2" ]; then
        # Different base versions, use sort -V
        [ "$(printf '%s\n' "$base1" "$base2" | sort -V | head -1)" = "$base1" ]
        return $?
      fi
      
      # Same base version, check prereleases
      local pre1=$(echo "$v1" | grep -o '\-.*$' || echo "")
      local pre2=$(echo "$v2" | grep -o '\-.*$' || echo "")
      
      if [ -z "$pre1" ] && [ -n "$pre2" ]; then
        # v1 is stable, v2 is prerelease: v2 < v1
        return 1
      elif [ -n "$pre1" ] && [ -z "$pre2" ]; then
        # v1 is prerelease, v2 is stable: v1 < v2
        return 0
      elif [ -n "$pre1" ] && [ -n "$pre2" ]; then
        # Both prereleases, compare them
        [ "$(printf '%s\n' "$pre1" "$pre2" | sort -V | head -1)" = "$pre1" ]
        return $?
      else
        # Both stable and same base: equal
        return 1
      fi
    }
    
    # Check if current version is greater than previous
    if version_less_than "$PREVIOUS_TRANSPORT_VERSION" "$TRANSPORT_VERSION"; then
      echo "   ✅ Transport version incremented: $PREVIOUS_TRANSPORT_VERSION → $TRANSPORT_VERSION"
      if [ "$GIT_TAG_EXISTS" = "false" ]; then
        echo "   🏷️  Git tag missing - transport release needed"
        BIFROST_HTTP_NEEDS_RELEASE="true"
      fi
    else
      echo "   ⏭️ No transport version increment"
    fi
  fi
fi
  
# Check if Docker image needs to be built (independent of transport release)
if [ "$DOCKER_TAG_EXISTS" = "false" ]; then
  echo "   🐳 Docker image missing - docker release needed"
  DOCKER_NEEDS_RELEASE="true"
fi


# Convert plugin array to JSON (compact format)
if [ ${#PLUGIN_CHANGES[@]} -eq 0 ]; then
  CHANGED_PLUGINS_JSON="[]"
else
  CHANGED_PLUGINS_JSON=$(printf '%s\n' "${PLUGIN_CHANGES[@]}" | jq -R . | jq -s -c .)
fi

echo "CHANGED_PLUGINS_JSON: $CHANGED_PLUGINS_JSON"

# Summary
echo ""
echo "📋 Release Summary:"
echo "   Core: $CORE_NEEDS_RELEASE (v$CORE_VERSION)"
echo "   Framework: $FRAMEWORK_NEEDS_RELEASE (v$FRAMEWORK_VERSION)"
echo "   Plugins: $PLUGINS_NEED_RELEASE (${#PLUGIN_CHANGES[@]} plugins)"
echo "   Bifrost HTTP: $BIFROST_HTTP_NEEDS_RELEASE (v$TRANSPORT_VERSION)"
echo "   Docker: $DOCKER_NEEDS_RELEASE (v$TRANSPORT_VERSION)"

# Set outputs (only when running in GitHub Actions)
if [ -n "${GITHUB_OUTPUT:-}" ]; then
  {
    echo "core-needs-release=$CORE_NEEDS_RELEASE"
    echo "framework-needs-release=$FRAMEWORK_NEEDS_RELEASE"
    echo "plugins-need-release=$PLUGINS_NEED_RELEASE"
    echo "bifrost-http-needs-release=$BIFROST_HTTP_NEEDS_RELEASE"
    echo "docker-needs-release=$DOCKER_NEEDS_RELEASE"
    echo "changed-plugins=$CHANGED_PLUGINS_JSON"
    echo "core-version=$CORE_VERSION"
    echo "framework-version=$FRAMEWORK_VERSION"
    echo "transport-version=$TRANSPORT_VERSION"
  } >> "$GITHUB_OUTPUT"
else
  echo "ℹ️ GITHUB_OUTPUT not set; skipping outputs write (local run)"
fi