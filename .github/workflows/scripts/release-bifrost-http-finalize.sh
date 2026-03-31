#!/usr/bin/env bash
set -euo pipefail

# Finalize bifrost-http release: changelog, tagging, GitHub release, R2 latest copy
# Usage: ./release-bifrost-http-finalize.sh <version>

# Validate input argument
if [ "${1:-}" = "" ]; then
  echo "Usage: $0 <version>" >&2
  exit 1
fi

VERSION="$1"
TAG_NAME="transports/v${VERSION}"

echo "🏷️ Finalizing bifrost-http v$VERSION release..."

# Get core and framework versions from version files
CORE_VERSION="v$(tr -d '\n\r' < core/version)"
FRAMEWORK_VERSION="v$(tr -d '\n\r' < framework/version)"

# Re-compute plugin versions from version files and transports/go.mod
declare -A PLUGIN_VERSIONS
PLUGINS_USED=()

for plugin_dir in plugins/*/; do
  if [ -d "$plugin_dir" ]; then
    plugin_name=$(basename "$plugin_dir")
    PLUGIN_VERSION="v$(tr -d '\n\r' < "${plugin_dir}version")"
    PLUGIN_VERSIONS["$plugin_name"]="$PLUGIN_VERSION"
  fi
done

# Check which plugins are actually used by the transport
while IFS= read -r plugin_line; do
  plugin_name=$(echo "$plugin_line" | awk -F'/' '{print $NF}' | awk '{print $1}')
  plugin_version=$(echo "$plugin_line" | awk '{print $NF}')

  # Use version file version if available, otherwise use go.mod version
  if [[ -n "${PLUGIN_VERSIONS[$plugin_name]:-}" ]]; then
    PLUGINS_USED+=("$plugin_name:${PLUGIN_VERSIONS[$plugin_name]}")
  else
    PLUGIN_VERSIONS["$plugin_name"]="$plugin_version"
    PLUGINS_USED+=("$plugin_name:$plugin_version")
  fi
done < <(grep "github.com/maximhq/bifrost/plugins/" transports/go.mod)

echo "🔧 Versions:"
echo "   Core: $CORE_VERSION"
echo "   Framework: $FRAMEWORK_VERSION"
echo "   Plugins:"
for plugin_name in "${!PLUGIN_VERSIONS[@]}"; do
  echo "     - $plugin_name: ${PLUGIN_VERSIONS[$plugin_name]}"
done

# Capturing changelog
CHANGELOG_BODY=$(cat transports/changelog.md)
# Skip comments from changelog
CHANGELOG_BODY=$(echo "$CHANGELOG_BODY" | grep -v '^<!--' | grep -v '^-->')
# If changelog is empty, return error
if [ -z "$CHANGELOG_BODY" ]; then
  echo "❌ Changelog is empty"
  exit 1
fi
echo "📝 New changelog: $CHANGELOG_BODY"

# Finding previous tag
echo "🔍 Finding previous tag..."
PREV_TAG=$(git tag -l "transports/v*" | sort -V | tail -1)
if [[ "$PREV_TAG" == "$TAG_NAME" ]]; then
  PREV_TAG=$(git tag -l "transports/v*" | sort -V | tail -2 | head -1)
fi
echo "🔍 Previous tag: $PREV_TAG"

# Get message of the tag
echo "🔍 Getting previous tag message..."
PREV_CHANGELOG=$(git tag -l --format='%(contents)' "$PREV_TAG")
echo "📝 Previous changelog body: $PREV_CHANGELOG"

# Checking if tag message is the same as the changelog
if [[ "$PREV_CHANGELOG" == "$CHANGELOG_BODY" ]]; then
  echo "❌ Changelog is the same as the previous changelog"
  exit 1
fi

# Create and push tag
echo "🏷️ Creating tag: $TAG_NAME"
git config user.name "github-actions[bot]"
git config user.email "41898282+github-actions[bot]@users.noreply.github.com"
git tag "$TAG_NAME" -m "Release transports v$VERSION" -m "$CHANGELOG_BODY"
git push origin "$TAG_NAME"

# Create GitHub release
TITLE="Bifrost HTTP v$VERSION"

# Mark prereleases when version contains a hyphen
PRERELEASE_FLAG=""
if [[ "$VERSION" == *-* ]]; then
  PRERELEASE_FLAG="--prerelease"
fi

LATEST_FLAG=""
if [[ "$VERSION" != *-* ]]; then
  LATEST_FLAG="--latest"
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

$CHANGELOG_BODY

### Installation

#### Docker
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
  ${PRERELEASE_FLAG} ${LATEST_FLAG}

echo "✅ Bifrost HTTP released successfully"

# Copy versioned R2 path to latest/ for stable releases
if [[ "$VERSION" != *-* ]]; then
  if [ -n "${R2_ENDPOINT:-}" ] && [ -n "${R2_BUCKET:-}" ]; then
    echo "📤 Copying versioned binaries to latest/ on R2..."
    R2_ENDPOINT="$(echo "$R2_ENDPOINT" | tr -d '[:space:]')"
    aws s3 sync "s3://$R2_BUCKET/bifrost/v$VERSION/" "s3://$R2_BUCKET/bifrost/latest/" \
      --endpoint-url "$R2_ENDPOINT" \
      --profile "${R2_AWS_PROFILE:-R2}" \
      --no-progress \
      --delete
    echo "✅ Latest binaries updated on R2"
  fi
fi

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
