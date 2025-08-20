#!/usr/bin/env bash
set -euo pipefail

# Release framework component
# Usage: ./release-framework.sh <version>

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

# Fetching all tags
git fetch --tags >/dev/null 2>&1 || true

# Get latest core version
LATEST_CORE_TAG=$(git tag -l "core/v*" | sort -V | tail -1)
if [ -z "$LATEST_CORE_TAG" ]; then
  CORE_VERSION="v$(tr -d '\n\r' < core/version)"
else
  CORE_VERSION=${LATEST_CORE_TAG#core/}
fi

echo "🔧 Using core version: $CORE_VERSION"

# Update framework dependencies
echo "🔧 Updating framework dependencies..."
cd framework
go get "github.com/maximhq/bifrost/core@$CORE_VERSION"
go mod tidy
git add go.mod go.sum

# Check if there are any changes to commit
git add go.mod go.sum

# Check if there are any changes to commit
if ! git diff --cached --quiet; then
  git commit -m "framework: bump core to $CORE_VERSION"
  # Push the bump so go.mod/go.sum changes are recorded on the branch
  CURRENT_BRANCH="$(git rev-parse --abbrev-ref HEAD)"
  git push origin "$CURRENT_BRANCH"
else
  echo "No dependency changes detected; skipping commit."
fi

# Validate framework build
echo "🔨 Validating framework build..."
go build ./...
# Starting dependencies of framework tests
echo "🔧 Starting dependencies of framework tests..."
docker-compose -f ../tests/docker-compose.yml up -d
sleep 20
go test ./...
# Shutting down dependencies
echo "🔧 Shutting down dependencies of framework tests..."
docker-compose -f ../tests/docker-compose.yml down
cd ..

echo "✅ Framework build validation successful"

# Create and push tag
echo "🏷️ Creating tag: $TAG_NAME"
if git rev-parse --verify "$TAG_NAME" >/dev/null 2>&1; then
  echo "Tag $TAG_NAME already exists; skipping tag creation."
else
  git tag "$TAG_NAME" -m "Release framework $VERSION"
  git push origin "$TAG_NAME"
fi

# Create GitHub release
TITLE="Framework $VERSION"
BODY="## Framework Release $VERSION

### 📦 Framework Library $VERSION

This release updates the framework to use **core $CORE_VERSION**.

### Dependencies
- **Core**: \`$CORE_VERSION\`

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
    --notes "$BODY"
fi

echo "✅ Framework released successfully"
echo "success=true" >> "$GITHUB_OUTPUT"
