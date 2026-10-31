#!/usr/bin/env bash
set -euo pipefail

# Extract NPX version from package.json
# Usage: ./extract-npx-version.sh

# shellcheck source=SCRIPTDIR/semver.sh
source "$(dirname "${BASH_SOURCE[0]}")/semver.sh"

# Path to package.json
PACKAGE_JSON="npx/bifrost/package.json"

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

# Validate version format: SemVer 2.0.0 (X.Y.Z, X.Y.Z-rc.1, X.Y.Z+build)
if ! is_semver "${VERSION}"; then
  echo "❌ Invalid version format '${VERSION}'. Expected format: MAJOR.MINOR.PATCH"
  exit 1
fi

echo "📦 Extracted NPX version: ${VERSION}"

# Set outputs (only when running in GitHub Actions)
if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
  {
    echo "version=${VERSION}"
    echo "full-tag=npx/bifrost/v${VERSION}"
  } >> "$GITHUB_OUTPUT"
else
  echo "::notice::GITHUB_OUTPUT not set; skipping outputs (local run?)"
fi