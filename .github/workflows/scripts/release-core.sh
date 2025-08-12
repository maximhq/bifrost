#!/bin/bash
set -euo pipefail

# Release core component
# Usage: ./release-core.sh <version>

VERSION="$1"
TAG_NAME="core/v${VERSION}"

echo "🔧 Releasing core v$VERSION..."

# Validate core build
echo "🔨 Validating core build..."
cd core
go mod tidy
go build ./...
go test ./...
cd ..
echo "✅ Core build validation successful"

# Create and push tag
echo "🏷️ Creating tag: $TAG_NAME"
git tag "$TAG_NAME" -m "Release core v$VERSION"
git push origin "$TAG_NAME"

# Create GitHub release
TITLE="Core v$VERSION"
BODY="## Core Release v$VERSION

### 🔧 Core Library v$VERSION

This release contains updates to the core Bifrost library.

### Installation

\`\`\`bash
go get github.com/maximhq/bifrost/core@$TAG_NAME
\`\`\`

### Next Steps
1. Framework will be updated automatically if needed
2. Plugins will be updated automatically if needed  
3. Bifrost HTTP will be updated automatically if needed

---
_This release was automatically created from version file: \`core/version\`_"

echo "🎉 Creating GitHub release for $TITLE..."
gh release create "$TAG_NAME" \
  --title "$TITLE" \
  --notes "$BODY"

echo "✅ Core released successfully"
echo "success=true" >> $GITHUB_OUTPUT
