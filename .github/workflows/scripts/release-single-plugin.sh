#!/usr/bin/env bash
set -euo pipefail

# Release a single plugin
# Usage: ./release-single-plugin.sh <plugin-name> [core-version] [framework-version]
if [[ $# -lt 1 ]]; then
  echo "Usage: $0 <plugin-name> [core-version] [framework-version]"
  exit 1
fi

PLUGIN_NAME="$1"

# Get core version from parameter or latest tag
if [ -n "${2:-}" ]; then
  CORE_VERSION="$2"
else
  # Get latest core version from git tags
  LATEST_CORE_TAG=$(git tag -l "core/v*" | sort -V | tail -1)
  if [ -z "$LATEST_CORE_TAG" ]; then
    echo "❌ No core tags found, using version from file"
    CORE_VERSION="v$(tr -d '\n\r' < core/version)"
  else
    CORE_VERSION=${LATEST_CORE_TAG#core/}
  fi
fi

# Get framework version from parameter or latest tag
if [ -n "${3:-}" ]; then
  FRAMEWORK_VERSION="$3"
else
  # Get latest framework version from git tags
  LATEST_FRAMEWORK_TAG=$(git tag -l "framework/v*" | sort -V | tail -1)
  if [ -z "$LATEST_FRAMEWORK_TAG" ]; then
    echo "❌ No framework tags found, using version from file"
    FRAMEWORK_VERSION="v$(tr -d '\n\r' < framework/version)"
  else
    FRAMEWORK_VERSION=${LATEST_FRAMEWORK_TAG#framework/}
  fi
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
  go get "github.com/maximhq/bifrost/core@${CORE_VERSION}"
  go get "github.com/maximhq/bifrost/framework@${FRAMEWORK_VERSION}"
  go mod tidy
  git add go.mod go.sum || true

  # Validate build
  echo "🔨 Validating plugin build..."
  go build ./...

  # Run tests if any exist
  if go list ./... | grep -q .; then
    echo "🧪 Running plugin tests..."
    go test ./...
  fi

  echo "✅ Plugin $PLUGIN_NAME build validation successful"
else
  echo "ℹ️ No go.mod found, skipping Go dependency update"
fi

cd ../..

# Commit and push changes if any
if ! git diff --cached --quiet; then
  echo "🔧 Committing and pushing changes..."
  git commit -m "plugins/${PLUGIN_NAME}: bump core to $CORE_VERSION and framework to $FRAMEWORK_VERSION --skip-pipeline"
  git push -u origin HEAD
else
  echo "ℹ️ No staged changes to commit"
fi

# Create and push tag
echo "🏷️ Creating tag: $TAG_NAME"

if git rev-parse "$TAG_NAME" >/dev/null 2>&1; then
  echo "ℹ️ Tag already exists: $TAG_NAME (skipping creation)"
else
  git tag "$TAG_NAME" -m "Release plugin $PLUGIN_NAME v$PLUGIN_VERSION"
  git push origin "$TAG_NAME"
fi

# Create GitHub release
TITLE="Plugin $PLUGIN_NAME v$PLUGIN_VERSION"

# Mark prereleases when version contains a hyphen
PRERELEASE_FLAG=""
if [[ "$PLUGIN_VERSION" == *-* ]]; then
  PRERELEASE_FLAG="--prerelease"
fi

BODY="## Plugin Release: $PLUGIN_NAME v$PLUGIN_VERSION

### 🔌 Plugin: $PLUGIN_NAME v$PLUGIN_VERSION

This release updates the $PLUGIN_NAME plugin.

### Dependencies
- **Core**: \`$CORE_VERSION\`
- **Framework**: \`$FRAMEWORK_VERSION\`

### Installation

\`\`\`bash
# Update your go.mod to use the new plugin version
go get github.com/maximhq/bifrost/plugins/$PLUGIN_NAME@v$PLUGIN_VERSION
\`\`\`

### Plugin Details
- **Name**: $PLUGIN_NAME
- **Version**: $PLUGIN_VERSION
- **Core Dependency**: $CORE_VERSION
- **Framework Dependency**: $FRAMEWORK_VERSION

---
_This release was automatically created from version file: \`plugins/$PLUGIN_NAME/version\`_"

echo "🎉 Creating GitHub release for $TITLE..."

if gh release view "$TAG_NAME" >/dev/null 2>&1; then
  echo "ℹ️ Release $TAG_NAME already exists. Skipping creation."
else
  gh release create "$TAG_NAME" \
    --title "$TITLE" \
    --notes "$BODY" \
    ${PRERELEASE_FLAG}
fi

echo "✅ Plugin $PLUGIN_NAME released successfully"
echo "success=true" >> "${GITHUB_OUTPUT:-/dev/null}"
