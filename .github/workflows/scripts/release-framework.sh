#!/usr/bin/env bash
set -euo pipefail

# Release framework component
# Usage: ./release-framework.sh <version>

# Source Go utilities for exponential backoff
source "$(dirname "$0")/go-utils.sh"

# Making sure version is provided
if [ $# -ne 1 ]; then
  echo "Usage: $0 <version>" >&2
  exit 1
fi

VERSION_RAW="$1"
# Ensure leading 'v' for module/tag semver
if [[ "$VERSION_RAW" == v* ]]; then
  VERSION="$VERSION_RAW"
else
  VERSION="v$VERSION_RAW"
fi

TAG_NAME="framework/${VERSION}"

echo "📦 Releasing framework $VERSION..."

# Ensure we have the latest version
git pull origin
# Fetching all tags
git fetch --tags >/dev/null 2>&1 || true

# Get core version from version file
CORE_VERSION="v$(tr -d '\n\r' < core/version)"


# Before starting the test, we need to update hello-word plugin core dependencies
echo "🔧 Updating hello-word plugin core dependencies..."
cd examples/plugins/hello-world
go_get_with_backoff "github.com/maximhq/bifrost/core@$CORE_VERSION"
go mod tidy
git add go.mod go.sum
cd ../../..

echo "🔧 Using core version: $CORE_VERSION"

# Update framework dependencies
echo "🔧 Updating framework dependencies..."
cd framework
go_get_with_backoff "github.com/maximhq/bifrost/core@$CORE_VERSION"
go mod tidy
git add go.mod go.sum

# Check if there are any changes to commit
git add go.mod go.sum


# Validate framework build
echo "🔨 Validating framework build..."
go build ./...
# Starting dependencies of framework tests
echo "🔧 Starting dependencies of framework tests..."
# Use docker compose (v2) if available, fallback to docker-compose (v1)
if command -v docker-compose >/dev/null 2>&1; then
  docker-compose -f ../tests/docker-compose.yml up -d
elif docker compose version >/dev/null 2>&1; then
  docker compose -f ../tests/docker-compose.yml up -d
else
  echo "❌ Neither docker-compose nor docker compose is available"
  exit 1
fi
sleep 20
go test -coverprofile=coverage.txt -coverpkg=./... ./...

# Upload coverage to Codecov
if [ -n "${CODECOV_TOKEN:-}" ]; then
  echo "📊 Uploading coverage to Codecov..."
  curl -Os https://uploader.codecov.io/latest/linux/codecov
  chmod +x codecov
  ./codecov -t "$CODECOV_TOKEN" -f coverage.txt -F framework
  rm -f codecov coverage.txt
else
  echo "ℹ️ CODECOV_TOKEN not set, skipping coverage upload"
  rm -f coverage.txt
fi

# Shutting down dependencies
echo "🔧 Shutting down dependencies of framework tests..."
# Use docker compose (v2) if available, fallback to docker-compose (v1)
if command -v docker-compose >/dev/null 2>&1; then
  docker-compose -f ../tests/docker-compose.yml down
elif docker compose version >/dev/null 2>&1; then
  docker compose -f ../tests/docker-compose.yml down
else
  echo "❌ Neither docker-compose nor docker compose is available"
  exit 1
fi
cd ..

echo "✅ Framework build validation successful"

# Check if there are any changes to commit
if ! git diff --cached --quiet; then
  git config user.name "github-actions[bot]"
  git config user.email "41898282+github-actions[bot]@users.noreply.github.com"
  git commit -m "framework: bump core to $CORE_VERSION --skip-pipeline"
  # Push the bump so go.mod/go.sum changes are recorded on the branch
  CURRENT_BRANCH="$(git rev-parse --abbrev-ref HEAD)"
  git push origin "$CURRENT_BRANCH"
  echo "🔧 Pushed framework bump to $CURRENT_BRANCH"
else
  echo "No dependency changes detected; skipping commit."
fi

# Capturing changelog
CHANGELOG_BODY=$(cat framework/changelog.md)
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
PREV_TAG=$(git tag -l "framework/v*" | sort -V | tail -1)
if [[ "$PREV_TAG" == "$TAG_NAME" ]]; then
  PREV_TAG=$(git tag -l "framework/v*" | sort -V | tail -2 | head -1)
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
if git rev-parse --verify "$TAG_NAME" >/dev/null 2>&1; then
  echo "Tag $TAG_NAME already exists; skipping tag creation."
else
  git tag "$TAG_NAME" -m "Release framework $VERSION" -m "$CHANGELOG_BODY"
  git push origin "$TAG_NAME"
fi

# Create GitHub release
TITLE="Framework $VERSION"

# Mark prereleases when version contains a hyphen
PRERELEASE_FLAG=""
if [[ "$VERSION" == *-* ]]; then
  PRERELEASE_FLAG="--prerelease"
fi

LATEST_FLAG=""
if [[ "$VERSION" != *-* ]]; then
  LATEST_FLAG="--latest"
fi

BODY="## Framework Release $VERSION

$CHANGELOG_BODY

### Installation

\`\`\`bash
go get github.com/maximhq/bifrost/framework@$VERSION
\`\`\`

---
_This release was automatically created and uses core version: \`$CORE_VERSION\`_"

echo "🎉 Creating GitHub release for $TITLE..."
if gh release view "$TAG_NAME" >/dev/null 2>&1; then
  echo "ℹ️ Release $TAG_NAME already exists. Skipping creation."
else
  gh release create "$TAG_NAME" \
    --title "$TITLE" \
    --notes "$BODY" \
    ${PRERELEASE_FLAG} ${LATEST_FLAG}
    
fi

echo "✅ Framework released successfully"
echo "success=true" >> "$GITHUB_OUTPUT"
