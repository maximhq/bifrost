#!/bin/bash
set -euo pipefail

# Release framework component
# Usage: ./release-framework.sh <version>

VERSION="$1"
TAG_NAME="framework/v${VERSION}"

echo "📦 Releasing framework v$VERSION..."

# Get latest core version
LATEST_CORE_TAG=$(git tag -l "core/v*" | sort -V | tail -1)
if [ -z "$LATEST_CORE_TAG" ]; then
  CORE_VERSION="v$(cat core/version)"
else
  CORE_VERSION=${LATEST_CORE_TAG#core/}
fi

echo "🔧 Using core version: $CORE_VERSION"

# Update framework dependencies
echo "🔧 Updating framework dependencies..."
cd framework
go get "github.com/maximhq/bifrost/core@core/$CORE_VERSION"
go mod tidy

# Validate framework build
echo "🔨 Validating framework build..."
go build ./...
go test ./...
cd ..
echo "✅ Framework build validation successful"

# Create and push tag
echo "🏷️ Creating tag: $TAG_NAME"
git tag "$TAG_NAME" -m "Release framework v$VERSION"
git push origin "$TAG_NAME"

# Create GitHub release
TITLE="Framework v$VERSION"
BODY="## Framework Release v$VERSION

### 📦 Framework Library v$VERSION

This release updates the framework to use **core $CORE_VERSION**.

### Dependencies
- **Core**: \`$CORE_VERSION\`

### Installation

\`\`\`bash
go get github.com/maximhq/bifrost/framework@$TAG_NAME
\`\`\`

---
_This release was automatically created and uses core version: \`$CORE_VERSION\`_"

echo "🎉 Creating GitHub release for $TITLE..."
gh release create "$TAG_NAME" \
  --title "$TITLE" \
  --notes "$BODY"

echo "✅ Framework released successfully"
echo "success=true" >> $GITHUB_OUTPUT
