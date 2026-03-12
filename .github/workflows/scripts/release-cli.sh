#!/usr/bin/env bash
set -euo pipefail

# Release bifrost CLI component
# Usage: ./release-cli.sh <version>

# Get the absolute path of the script directory
if command -v readlink >/dev/null 2>&1 && readlink -f "$0" >/dev/null 2>&1; then
  SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
else
  SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd -P)"
fi
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd -P)"

# Validate input argument
if [ "${1:-}" = "" ]; then
  echo "Usage: $0 <version>" >&2
  exit 1
fi

VERSION="$1"
TAG_NAME="cli/v${VERSION}"

echo "🚀 Releasing bifrost CLI v$VERSION..."

# Validate CLI build
echo "🔨 Validating CLI build..."
COMMIT="${GITHUB_SHA:-$(git rev-parse HEAD 2>/dev/null || echo 'unknown')}"
(cd "$REPO_ROOT/cli" && go build -ldflags "-X main.version=v${VERSION} -X main.commit=${COMMIT}" ./...)
echo "✅ CLI build validation successful"

# Build CLI executables
echo "🔨 Building executables..."
bash "$SCRIPT_DIR/build-cli-executables.sh" "$VERSION"

# --- Preflight checks (no side effects) ---

# Capturing changelog
CHANGELOG_BODY=$(cat "$REPO_ROOT/cli/changelog.md")
# Skip comments from changelog
CHANGELOG_BODY=$(printf '%s\n' "$CHANGELOG_BODY" | sed '/<!--/,/-->/d')
# If changelog is empty, return error
if [ -z "$CHANGELOG_BODY" ]; then
  echo "❌ Changelog is empty"
  exit 1
fi
echo "📝 New changelog: $CHANGELOG_BODY"

# Finding previous tag
echo "🔍 Finding previous tag..."
TAG_COUNT=$(git tag -l "cli/v*" | wc -l | tr -d ' ')
if [[ "$TAG_COUNT" -eq 0 ]]; then
  PREV_TAG=""
else
  PREV_TAG=$(git tag -l "cli/v*" | sort -V | tail -1)
  if [[ "$PREV_TAG" == "$TAG_NAME" ]]; then
    PREV_TAG=$(git tag -l "cli/v*" | sort -V | tail -2 | head -1)
    [[ "$PREV_TAG" == "$TAG_NAME" ]] && PREV_TAG=""
  fi
fi
echo "🔍 Previous tag: $PREV_TAG"

# Get message of the tag and compare changelogs
if [[ -n "$PREV_TAG" ]]; then
  echo "🔍 Getting previous tag message..."
  PREV_CHANGELOG=$(git tag -l --format='%(contents:body)' "$PREV_TAG")
  echo "📝 Previous changelog body: $PREV_CHANGELOG"

  # Checking if tag message is the same as the changelog
  if [[ "$PREV_CHANGELOG" == "$CHANGELOG_BODY" ]]; then
    echo "❌ Changelog is the same as the previous changelog"
    exit 1
  fi
else
  echo "ℹ️ No previous CLI tag found. Skipping changelog comparison."
fi

# Verify GitHub token before any publish steps
if [ -z "${GH_TOKEN:-}" ] && [ -z "${GITHUB_TOKEN:-}" ]; then
  echo "Error: GH_TOKEN or GITHUB_TOKEN is not set. Please export one to authenticate the GitHub CLI."
  exit 1
fi

# --- Publish steps (all checks passed) ---

# Configure and upload to R2
echo "📤 Uploading binaries..."
bash "$SCRIPT_DIR/configure-r2.sh"
bash "$SCRIPT_DIR/upload-cli-to-r2.sh" "$TAG_NAME"

# Create and push tag
if git rev-parse -q --verify "refs/tags/$TAG_NAME" >/dev/null; then
  echo "ℹ️ Tag $TAG_NAME already exists. Reusing it."
else
  echo "🏷️ Creating tag: $TAG_NAME"
  git tag "$TAG_NAME" -m "Release CLI v$VERSION" -m "$CHANGELOG_BODY"
  git push origin "$TAG_NAME"
fi

# Create GitHub release
TITLE="Bifrost CLI v$VERSION"

# Mark prereleases when version contains a hyphen
PRERELEASE_FLAG=""
if [[ "$VERSION" == *-* ]]; then
  PRERELEASE_FLAG="--prerelease"
fi

BODY="## Bifrost CLI Release v$VERSION

$CHANGELOG_BODY

### Installation

#### Binary Download
\`\`\`bash
npx @maximhq/bifrost --cli-version v$VERSION
\`\`\`

---
_This release was automatically created._"

if gh release view "$TAG_NAME" >/dev/null 2>&1; then
  echo "ℹ️ GitHub release $TAG_NAME already exists. Skipping."
else
  echo "🎉 Creating GitHub release for $TITLE..."
  gh release create "$TAG_NAME" \
    --title "$TITLE" \
    --notes "$BODY" \
    ${PRERELEASE_FLAG}
fi

echo "✅ Bifrost CLI released successfully"

# Print summary
echo ""
echo "📋 Release Summary:"
echo "   🏷️  Tag: $TAG_NAME"
echo "   🎉 GitHub release: Created"

if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
  echo "success=true" >> "$GITHUB_OUTPUT"
fi
