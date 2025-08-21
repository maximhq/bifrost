#!/usr/bin/env bash
set -euo pipefail

# Release bifrost-http component
# Usage: ./release-bifrost-http.sh <version>

# Validate input argument
if [ "${1:-}" = "" ]; then
  echo "Usage: $0 <version>" >&2
  exit 1
fi

VERSION="$1"
TAG_NAME="transports/v${VERSION}"

echo "🚀 Releasing bifrost-http v$VERSION..."

# Get latest versions
# Ensure tags are available (CI often does shallow clones)
git fetch --tags --force >/dev/null 2>&1 || true
LATEST_CORE_TAG=$(git tag -l "core/v*" | sort -V | tail -1)
LATEST_FRAMEWORK_TAG=$(git tag -l "framework/v*" | sort -V | tail -1)

if [ -z "$LATEST_CORE_TAG" ]; then
  CORE_VERSION="v$(tr -d '\n\r' < core/version)"
else
  CORE_VERSION=${LATEST_CORE_TAG#core/}
fi

if [ -z "$LATEST_FRAMEWORK_TAG" ]; then
  FRAMEWORK_VERSION="v$(tr -d '\n\r' < framework/version)"
else
  FRAMEWORK_VERSION=${LATEST_FRAMEWORK_TAG#framework/}
fi

# Get latest plugin versions
echo "🔌 Getting latest plugin release versions..."
declare -A PLUGIN_VERSIONS

# First, get versions for plugins that exist in the plugins/ directory
for plugin_dir in plugins/*/; do
  if [ -d "$plugin_dir" ]; then
    plugin_name=$(basename "$plugin_dir")
    # Get the latest released version for this plugin
    LATEST_PLUGIN_TAG=$(git tag -l "plugins/${plugin_name}/v*" | sort -V | tail -1)
    
    if [ -z "$LATEST_PLUGIN_TAG" ]; then
      # No release yet, use version from file
      PLUGIN_VERSION="v$(tr -d '\n\r' < "${plugin_dir}version")"
      echo "   📦 $plugin_name: $PLUGIN_VERSION (from version file - not yet released)"
    else
      PLUGIN_VERSION=${LATEST_PLUGIN_TAG#plugins/${plugin_name}/}
      echo "   📦 $plugin_name: $PLUGIN_VERSION (latest release)"
    fi
    
    PLUGIN_VERSIONS["$plugin_name"]="$PLUGIN_VERSION"
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

# Update transport dependencies to use latest plugin releases
echo "🔧 Using latest plugin release versions for transport..."
PLUGINS_USED=()

# Track which plugins are actually used by the transport
cd transports
for plugin_name in "${!PLUGIN_VERSIONS[@]}"; do
  plugin_version="${PLUGIN_VERSIONS[$plugin_name]}"
  
  # Check if transport depends on this plugin
  if grep -q "github.com/maximhq/bifrost/plugins/$plugin_name" go.mod; then
    echo "  📦 Using $plugin_name plugin $plugin_version"
    go get "github.com/maximhq/bifrost/plugins/$plugin_name@$plugin_version"
    PLUGINS_USED+=("$plugin_name:$plugin_version")
  fi
done

# Also ensure core and framework are up to date
echo "  🔧 Updating core to $CORE_VERSION"
go get "github.com/maximhq/bifrost/core@$CORE_VERSION"
echo "  📦 Updating framework to $FRAMEWORK_VERSION" 
go get "github.com/maximhq/bifrost/framework@$FRAMEWORK_VERSION"

go mod tidy

# Only commit if there are changes
if ! git diff --quiet go.mod go.sum; then
  git add go.mod go.sum
  commit_msg="transports: update to core $CORE_VERSION, framework $FRAMEWORK_VERSION"
  if [ ${#PLUGINS_USED[@]} -gt 0 ]; then
    commit_msg="$commit_msg, plugins: ${PLUGINS_USED[*]}"
  fi
  echo "✅ Transport dependencies updated"
else
  echo "ℹ️  No dependency changes detected in transports"
fi

# Validate transport build
echo "🔨 Validating transport build..."
cd transports
go test ./...
cd ..
echo "✅ Transport build validation successful"

cd ..

# Commit and push changes if any
if ! git diff --cached --quiet; then
  echo "🔧 Committing and pushing changes..."
  git commit -m "${commit_msg:-"transports: update dependencies"} --skip-pipeline"
  git push -u origin HEAD
else
  echo "ℹ️ No staged changes to commit"
fi

echo "🎨 Building UI..."
make build-ui

# Install cross-compilation toolchains
echo "📦 Installing cross-compilation toolchains..."
bash ./.github/workflows/scripts/install-cross-compilers.sh

# Build Go executables
echo "🔨 Building executables..."
bash ./.github/workflows/scripts/build-executables.sh

# Configure and upload to R2
echo "📤 Uploading binaries..."
bash ./.github/workflows/scripts/configure-r2.sh
bash ./.github/workflows/scripts/upload-to-r2.sh "$TAG_NAME"

# Create and push tag
echo "🏷️ Creating tag: $TAG_NAME"
git tag "$TAG_NAME" -m "Release transports v$VERSION"
git push origin "$TAG_NAME"

# Create GitHub release
TITLE="Bifrost HTTP v$VERSION"

# Mark prereleases when version contains a hyphen
PRERELEASE_FLAG=""
if [[ "$VERSION" == *-* ]]; then
  PRERELEASE_FLAG="--prerelease"
fi

# Generate plugin version summary
PLUGIN_UPDATES=""
if [ ${#PLUGINS_USED[@]} -gt 0 ]; then
  PLUGIN_UPDATES="

### 🔌 Plugin Versions
This release includes the following plugin versions:
"
  for plugin_info in "${PLUGINS_USED[@]}"; do
    plugin_name="${plugin_info%%:*}"
    plugin_version="${plugin_info##*:}"
    PLUGIN_UPDATES="$PLUGIN_UPDATES- **$plugin_name**: \`$plugin_version\`
"
  done
else
  # Show all available plugin versions even if not directly used
  PLUGIN_UPDATES="

### 🔌 Available Plugin Versions
The following plugin versions are compatible with this release:
"
  for plugin_name in "${!PLUGIN_VERSIONS[@]}"; do
    plugin_version="${PLUGIN_VERSIONS[$plugin_name]}"
    PLUGIN_UPDATES="$PLUGIN_UPDATES- **$plugin_name**: \`$plugin_version\`
"
  done
fi

BODY="## Bifrost HTTP Transport Release v$VERSION

### 🚀 Bifrost HTTP Transport v$VERSION

This release includes the complete Bifrost HTTP transport with all dependencies updated.

### Dependencies
- **Core**: \`$CORE_VERSION\`
- **Framework**: \`$FRAMEWORK_VERSION\`
$PLUGIN_UPDATES
### Installation

#### Docker (Recommended)
\`\`\`bash
docker run -p 8080:8080 maximhq/bifrost:v$VERSION
\`\`\`

#### Binary Download
\`\`\`bash
npx @maximhq/bifrost --transport-version v$VERSION
\`\`\`

### Docker Images
- **\`maximhq/bifrost:v$VERSION\`** - This specific version
- **\`maximhq/bifrost:latest\`** - Latest version (updated with this release)

---
_This release was automatically created with dependencies: core \`$CORE_VERSION\`, framework \`$FRAMEWORK_VERSION\`. All plugins have been validated and updated._"

if [ -z "${GH_TOKEN:-}" ] && [ -z "${GITHUB_TOKEN:-}" ]; then
  echo "Error: GH_TOKEN or GITHUB_TOKEN is not set. Please export one to authenticate the GitHub CLI."
  exit 1
fi

echo "🎉 Creating GitHub release for $TITLE..."
gh release create "$TAG_NAME" \
  --title "$TITLE" \
  --notes "$BODY" \
  ${PRERELEASE_FLAG}

echo "✅ Bifrost HTTP released successfully"

# Print summary
echo ""
echo "📋 Release Summary:"
echo "   🏷️  Tag: $TAG_NAME"
echo "   🔧 Core version: $CORE_VERSION"
echo "   🔧 Framework version: $FRAMEWORK_VERSION"
echo "   📦 Transport: Updated"
if [ ${#PLUGINS_USED[@]} -gt 0 ]; then
  echo "   🔌 Plugins used: ${PLUGINS_USED[*]}"
else
  echo "   🔌 Available plugins: $(printf "%s " "${!PLUGIN_VERSIONS[@]}")"
fi
echo "   🎉 GitHub release: Created"

echo "success=true" >> "$GITHUB_OUTPUT"
