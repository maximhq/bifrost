#!/bin/bash
set -euo pipefail

# Release bifrost-http component
# Usage: ./release-bifrost-http.sh <version>

VERSION="$1"
TAG_NAME="transports/v${VERSION}"

echo "🚀 Releasing bifrost-http v$VERSION..."

# Get latest versions
LATEST_CORE_TAG=$(git tag -l "core/v*" | sort -V | tail -1)
LATEST_FRAMEWORK_TAG=$(git tag -l "framework/v*" | sort -V | tail -1)

if [ -z "$LATEST_CORE_TAG" ]; then
  CORE_VERSION="v$(cat core/version)"
else
  CORE_VERSION=${LATEST_CORE_TAG#core/}
fi

if [ -z "$LATEST_FRAMEWORK_TAG" ]; then
  FRAMEWORK_VERSION="v$(cat framework/version)"
else
  FRAMEWORK_VERSION=${LATEST_FRAMEWORK_TAG#framework/}
fi

echo "🔧 Using versions:"
echo "   Core: $CORE_VERSION"
echo "   Framework: $FRAMEWORK_VERSION"

# Update transport dependencies
echo "🔧 Updating transport dependencies..."
cd transports
go get "github.com/maximhq/bifrost/core@core/$CORE_VERSION"
go get "github.com/maximhq/bifrost/framework@framework/$FRAMEWORK_VERSION"
go mod tidy

# Build UI static files
echo "🎨 Building UI..."
cd ../ui
npm ci
npm run build
cd ../transports

# Validate transport build
echo "🔨 Validating transport build..."
go build ./...
go test ./...
echo "✅ Transport build validation successful"

# Install cross-compilation toolchains
echo "📦 Installing cross-compilation toolchains..."
cd ..
./.github/workflows/scripts/install-cross-compilers.sh

# Build Go executables
echo "🔨 Building executables..."
cd transports
./.github/workflows/scripts/build-executables.sh

# Configure and upload to R2
echo "📤 Uploading binaries..."
cd ..
./.github/workflows/scripts/configure-r2.sh
./.github/workflows/scripts/upload-to-r2.sh "$TAG_NAME"

# Create and push tag
echo "🏷️ Creating tag: $TAG_NAME"
git tag "$TAG_NAME" -m "Release transports v$VERSION"
git push origin "$TAG_NAME"

# Create GitHub release
TITLE="Bifrost HTTP v$VERSION"
BODY="## Bifrost HTTP Transport Release v$VERSION

### 🚀 Bifrost HTTP Transport v$VERSION

This release includes the complete Bifrost HTTP transport with all dependencies updated.

### Dependencies
- **Core**: \`$CORE_VERSION\`
- **Framework**: \`$FRAMEWORK_VERSION\`
- **Plugins**: Latest compatible versions

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
_This release was automatically created with dependencies: core \`$CORE_VERSION\`, framework \`$FRAMEWORK_VERSION\`_"

echo "🎉 Creating GitHub release for $TITLE..."
gh release create "$TAG_NAME" \
  --title "$TITLE" \
  --notes "$BODY"

echo "✅ Bifrost HTTP released successfully"
echo "success=true" >> $GITHUB_OUTPUT
