#!/usr/bin/env bash
set -euo pipefail

# Release a single plugin
# Usage: ./release-single-plugin.sh <plugin-name> [core-version] [framework-version]

# Source Go utilities for exponential backoff
source "$(dirname "$0")/go-utils.sh"
if [[ $# -lt 1 ]]; then
  echo "Usage: $0 <plugin-name> [core-version] [framework-version]"
  exit 1
fi

PLUGIN_NAME="$1"

# Get core version from parameter or version file
if [ -n "${2:-}" ]; then
  CORE_VERSION="$2"
else
  CORE_VERSION="v$(tr -d '\n\r' < core/version)"
fi

# Get framework version from parameter or version file
if [ -n "${3:-}" ]; then
  FRAMEWORK_VERSION="$3"
else
  FRAMEWORK_VERSION="v$(tr -d '\n\r' < framework/version)"
fi

# Ensure we have the latest version
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

echo "🔌 Releasing plugin: $PLUGIN_NAME"
echo "🔧 Core version: $CORE_VERSION"
echo "🔧 Framework version: $FRAMEWORK_VERSION"

PLUGIN_DIR="plugins/$PLUGIN_NAME"
VERSION_FILE="$PLUGIN_DIR/version"

if [ ! -f "$VERSION_FILE" ]; then
  echo "❌ Version file not found: $VERSION_FILE"
  exit 1
fi

PLUGIN_VERSION=$(tr -d '\n\r' < "$VERSION_FILE")
TAG_NAME="plugins/${PLUGIN_NAME}/v${PLUGIN_VERSION}"

echo "📦 Plugin version: $PLUGIN_VERSION"
echo "🏷️ Tag name: $TAG_NAME"


# Update plugin dependencies
echo "🔧 Updating plugin dependencies..."
cd "$PLUGIN_DIR"

# Update core dependency
if [ -f "go.mod" ]; then
  go_get_with_backoff "github.com/maximhq/bifrost/core@${CORE_VERSION}"
  go_get_with_backoff "github.com/maximhq/bifrost/framework@${FRAMEWORK_VERSION}"
  go mod tidy
  git add go.mod go.sum || true

  # Validate build
  echo "🔨 Validating plugin build..."
  go build ./...
  echo "✅ Plugin $PLUGIN_NAME build validation successful"
else
  echo "ℹ️ No go.mod found, skipping Go dependency update"
fi

cd ../..

# Commit and push changes if any
if ! git diff --cached --quiet; then
  git config user.name "github-actions[bot]"
  git config user.email "41898282+github-actions[bot]@users.noreply.github.com"
  echo "🔧 Committing and pushing changes..."
  git commit -m "plugins/${PLUGIN_NAME}: bump core to $CORE_VERSION and framework to $FRAMEWORK_VERSION --skip-pipeline"
  git push -u origin HEAD
else
  echo "ℹ️ No staged changes to commit"
fi

# Capturing changelog
CHANGELOG_BODY=$(cat $PLUGIN_DIR/changelog.md)
# Skip comments from changelog
CHANGELOG_BODY=$(echo "$CHANGELOG_BODY" | grep -v '^<!--' | grep -v '^-->' || true)
# If changelog is empty, return error
if [ -z "$CHANGELOG_BODY" ]; then
  echo "❌ Changelog is empty"
  exit 1
fi
echo "📝 New changelog: $CHANGELOG_BODY"

# Finding previous tag
echo "🔍 Finding previous tag..."
PREV_TAG=$(git tag -l "plugins/${PLUGIN_NAME}/v*" | sort -V | tail -1)
if [[ "$PREV_TAG" == "$TAG_NAME" ]]; then
  PREV_TAG=$(git tag -l "plugins/${PLUGIN_NAME}/v*" | sort -V | tail -2 | head -1)
fi

# Only validate changelog changes if there's a previous tag
if [ -n "$PREV_TAG" ]; then
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
else
  echo "ℹ️ No previous tag found - this is the first release"
fi


# Create and push tag
echo "🏷️ Creating tag: $TAG_NAME"

if git rev-parse "$TAG_NAME" >/dev/null 2>&1; then
  echo "ℹ️ Tag already exists: $TAG_NAME (skipping creation)"
else
  git tag "$TAG_NAME" -m "Release plugin $PLUGIN_NAME v$PLUGIN_VERSION" -m "$CHANGELOG_BODY"
  git push origin "$TAG_NAME"
fi

# Create GitHub release
TITLE="Plugin $PLUGIN_NAME v$PLUGIN_VERSION"

# Mark prereleases when version contains a hyphen
PRERELEASE_FLAG=""
if [[ "$PLUGIN_VERSION" == *-* ]]; then
  PRERELEASE_FLAG="--prerelease"
fi

# Mark as latest if not a prerelease
LATEST_FLAG=""
if [[ "$PLUGIN_VERSION" != *-* ]]; then
  LATEST_FLAG="--latest"
fi


BODY="## Plugin Release: $PLUGIN_NAME v$PLUGIN_VERSION

$CHANGELOG_BODY

### Installation

\`\`\`bash
# Update your go.mod to use the new plugin version
go get github.com/maximhq/bifrost/plugins/$PLUGIN_NAME@v$PLUGIN_VERSION
\`\`\`

---
_This release was automatically created from version file: \`plugins/$PLUGIN_NAME/version\`_"

echo "🎉 Creating GitHub release for $TITLE..."

if gh release view "$TAG_NAME" >/dev/null 2>&1; then
  echo "ℹ️ Release $TAG_NAME already exists. Skipping creation."
else
  gh release create "$TAG_NAME" \
    --title "$TITLE" \
    --notes "$BODY" \
    ${PRERELEASE_FLAG} ${LATEST_FLAG}    
fi

echo "✅ Plugin $PLUGIN_NAME released successfully"
echo "success=true" >> "${GITHUB_OUTPUT:-/dev/null}"
