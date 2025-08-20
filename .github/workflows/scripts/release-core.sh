#!/usr/bin/env bash
set -euo pipefail

# Release core component
# Usage: ./release-core.sh <version>

if [[ "${1:-}" == "" ]]; then
  echo "Usage: $0 <version>"
  echo "Example: $0 1.2.0"
  exit 1
fi
VERSION="$1"

TAG_NAME="core/v${VERSION}"

echo "🔧 Releasing core v$VERSION..."

# Validate core build
echo "🔨 Validating core build..."
cd core

if [[ ! -f version ]]; then
  echo "❌ Missing core/version file"
  exit 1
fi
FILE_VERSION="$(cat version | tr -d '[:space:]')"
if [[ "$FILE_VERSION" != "$VERSION" ]]; then
  echo "❌ Version mismatch: arg=$VERSION, core/version=$FILE_VERSION"
  exit 1
fi

go mod download
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

# Mark prereleases when version contains a hyphen
PRERELEASE_FLAG=""
if [[ "$VERSION" == *-* ]]; then
  PRERELEASE_FLAG="--prerelease"
fi

BODY="## Core Release v$VERSION

### 🔧 Core Library v$VERSION

This release contains updates to the core Bifrost library.

### Installation

\`\`\`bash
go get github.com/maximhq/bifrost/core@v$VERSION
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
  --notes "$BODY" \
  ${PRERELEASE_FLAG}

echo "✅ Core released successfully"
echo "success=true" >> "$GITHUB_OUTPUT"
