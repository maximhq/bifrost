#!/bin/bash
set -euo pipefail

# Release a single plugin
# Usage: ./release-single-plugin.sh <plugin-name> [core-version]

PLUGIN_NAME="$1"

# Get core version from parameter or latest tag
if [ -n "${2:-}" ]; then
  CORE_VERSION="$2"
else
  # Get latest core version from git tags
  LATEST_CORE_TAG=$(git tag -l "core/v*" | sort -V | tail -1)
  if [ -z "$LATEST_CORE_TAG" ]; then
    echo "❌ No core tags found, using version from file"
    CORE_VERSION="v$(cat core/version)"
  else
    CORE_VERSION=${LATEST_CORE_TAG#core/}
  fi
fi

echo "🔌 Releasing plugin: $PLUGIN_NAME"
echo "🔧 Core version: $CORE_VERSION"

PLUGIN_DIR="plugins/$PLUGIN_NAME"
VERSION_FILE="$PLUGIN_DIR/version"

if [ ! -f "$VERSION_FILE" ]; then
  echo "❌ Version file not found: $VERSION_FILE"
  exit 1
fi

PLUGIN_VERSION=$(cat "$VERSION_FILE" | tr -d '\n\r')
TAG_NAME="plugins/${PLUGIN_NAME}/v${PLUGIN_VERSION}"

echo "📦 Plugin version: $PLUGIN_VERSION"
echo "🏷️ Tag name: $TAG_NAME"

# Update plugin dependencies
echo "🔧 Updating plugin dependencies..."
cd "$PLUGIN_DIR"

# Update core dependency
if [ -f "go.mod" ]; then
  go get "github.com/maximhq/bifrost/core@core/$CORE_VERSION"
  go mod tidy
  
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

# Create and push tag
echo "🏷️ Creating tag: $TAG_NAME"
git tag "$TAG_NAME" -m "Release plugin $PLUGIN_NAME v$PLUGIN_VERSION"
git push origin "$TAG_NAME"

# Create GitHub release
TITLE="Plugin $PLUGIN_NAME v$PLUGIN_VERSION"

BODY="## Plugin Release: $PLUGIN_NAME v$PLUGIN_VERSION

### 🔌 Plugin: $PLUGIN_NAME v$PLUGIN_VERSION

This release updates the $PLUGIN_NAME plugin.

### Dependencies
- **Core**: \`$CORE_VERSION\`

### Installation

\`\`\`bash
# Update your go.mod to use the new plugin version
go get github.com/maximhq/bifrost/plugins/$PLUGIN_NAME@$TAG_NAME
\`\`\`

### Plugin Details
- **Name**: $PLUGIN_NAME
- **Version**: $PLUGIN_VERSION
- **Core Dependency**: $CORE_VERSION

---
_This release was automatically created from version file: \`plugins/$PLUGIN_NAME/version\`_"

echo "🎉 Creating GitHub release for $TITLE..."
gh release create "$TAG_NAME" \
  --title "$TITLE" \
  --notes "$BODY"

echo "✅ Plugin $PLUGIN_NAME released successfully"
echo "success=true" >> $GITHUB_OUTPUT
