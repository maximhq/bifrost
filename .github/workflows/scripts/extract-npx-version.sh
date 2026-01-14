#!/usr/bin/env bash
set -euo pipefail

# Extract NPX version from package.json
# Usage: ./extract-npx-version.sh

# Path to package.json
PACKAGE_JSON="npx/package.json"

if [[ ! -f "${PACKAGE_JSON}" ]]; then
  echo "❌ package.json not found at ${PACKAGE_JSON}"
  exit 1
fi

echo "📋 Reading version from ${PACKAGE_JSON}"

# Extract version from package.json using jq
VERSION=$(jq -r '.version' "${PACKAGE_JSON}")

if [[ -z "${VERSION}" ]] || [[ "${VERSION}" == "null" ]]; then
  echo "❌ Failed to extract version from package.json"
  exit 1
fi

# Validate version format (X.Y.Z or prerelease like X.Y.Z-rc.1)
if [[ ! "${VERSION}" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$ ]]; then
  echo "❌ Invalid version format '${VERSION}'. Expected format: MAJOR.MINOR.PATCH"
  exit 1
fi

echo "📦 Extracted NPX version: ${VERSION}"

# Set outputs (only when running in GitHub Actions)
if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
  {
    echo "version=${VERSION}"
    echo "full-tag=npx/v${VERSION}"
  } >> "$GITHUB_OUTPUT"
else
  echo "::notice::GITHUB_OUTPUT not set; skipping outputs (local run?)"
fi