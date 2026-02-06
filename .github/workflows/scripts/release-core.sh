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

cd ..

# Capturing changelog
CHANGELOG_BODY=$(cat core/changelog.md)
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
PREV_TAG=$(git tag -l "core/v*" | sort -V | tail -1)
if [[ "$PREV_TAG" == "$TAG_NAME" ]]; then
  PREV_TAG=$(git tag -l "core/v*" | sort -V | tail -2 | head -1)
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
git tag "$TAG_NAME" -m "Release core v$VERSION" -m "$CHANGELOG_BODY"
git push origin "$TAG_NAME"

# Create GitHub release
TITLE="Core v$VERSION"

# Mark prereleases when version contains a hyphen
PRERELEASE_FLAG=""
if [[ "$VERSION" == *-* ]]; then
  PRERELEASE_FLAG="--prerelease"
fi

LATEST_FLAG=""
if [[ "$VERSION" != *-* ]]; then
  LATEST_FLAG="--latest"
fi

BODY="## Core Release v$VERSION

$CHANGELOG_BODY

### Installation

\`\`\`bash
go get github.com/maximhq/bifrost/core@v$VERSION
\`\`\`

---
_This release was automatically created from version file: \`core/version\`_"

echo "🎉 Creating GitHub release for $TITLE..."
gh release create "$TAG_NAME" \
  --title "$TITLE" \
  --notes "$BODY" \
  ${PRERELEASE_FLAG} ${LATEST_FLAG}

echo "✅ Core released successfully"
echo "success=true" >> "$GITHUB_OUTPUT"
