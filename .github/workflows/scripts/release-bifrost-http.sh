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

echo "🔧 Using versions:"
echo "   Core: $CORE_VERSION"
echo "   Framework: $FRAMEWORK_VERSION"

# Update transport dependencies
echo "🔧 Updating transport dependencies..."
cd transports
go get "github.com/maximhq/bifrost/core@$CORE_VERSION"
go get "github.com/maximhq/bifrost/framework@$FRAMEWORK_VERSION"
go mod tidy

# Only commit if there are changes
if ! git diff --quiet go.mod go.sum; then
  git add go.mod go.sum
  git commit -m "transports: bump core to $CORE_VERSION; framework to $FRAMEWORK_VERSION"
else
  echo "No dependency changes detected in transports/go.mod or transports/go.sum"
fi

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
bash ./.github/workflows/scripts/install-cross-compilers.sh

# Build Go executables
echo "🔨 Building executables..."
cd transports
bash ./.github/workflows/scripts/build-executables.sh

# Configure and upload to R2
echo "📤 Uploading binaries..."
cd ..
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
echo "success=true" >> "$GITHUB_OUTPUT"
