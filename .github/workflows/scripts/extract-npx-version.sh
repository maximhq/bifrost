#!/bin/bash
set -euo pipefail

# Extract NPX version from tag
# Usage: ./extract-npx-version.sh

# Extract tag name from ref
TAG_NAME=${GITHUB_REF#refs/tags/}
echo "📋 Processing tag: $TAG_NAME"

# Validate tag format (npx/vX.Y.Z)
if [[ ! $TAG_NAME =~ ^npx/v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "❌ Invalid tag format '$TAG_NAME'. Expected format: npx/vMAJOR.MINOR.PATCH"
  exit 1
fi

# Extract version (remove 'npx/v' prefix to get just the version number)
VERSION=${TAG_NAME#npx/v}

echo "📦 Extracted NPX version: $VERSION"
echo "🏷️ Full tag: $TAG_NAME"

# Set outputs
echo "version=$VERSION" >> $GITHUB_OUTPUT
echo "full-tag=$TAG_NAME" >> $GITHUB_OUTPUT
