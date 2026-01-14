#!/bin/bash

# Script to verify if bifrost-http was successfully released
# This ensures Docker images are only built after a successful bifrost-http release
# Exits with code 0 if release is verified or not needed, exits with code 78 to skip if release failed

set -e

VERSION=$1
RELEASE_NEEDED=$2

if [ -z "$VERSION" ]; then
    echo "❌ Error: Version not provided"
    exit 1
fi

# If release was not needed, skip verification
if [ "$RELEASE_NEEDED" = "false" ]; then
    echo "ℹ️  Bifrost-http release was not needed, skipping verification"
    echo "   Docker images will be built with existing version"
    exit 0
fi

echo "🔍 Verifying bifrost-http release v${VERSION}..."

# Check if the git tag exists
if ! git rev-parse "transports/bifrost-http/v${VERSION}" >/dev/null 2>&1; then
    echo "⚠️  Git tag transports/bifrost-http/v${VERSION} not found"
    echo "   Bifrost-http release did not complete successfully"
    echo "   Skipping Docker image build..."
    exit 78  # Exit code 78 will be used to skip the job
fi

echo "✅ Git tag found: transports/bifrost-http/v${VERSION}"

# Check if the GitHub release exists
if [ -n "$GH_TOKEN" ]; then
    echo "🔍 Checking GitHub release..."
    if gh release view "transports/bifrost-http/v${VERSION}" >/dev/null 2>&1; then
        echo "✅ GitHub release found for transports/bifrost-http/v${VERSION}"
    else
        echo "⚠️  GitHub release for transports/bifrost-http/v${VERSION} not found"
        echo "   Bifrost-http release did not complete successfully"
        echo "   Skipping Docker image build..."
        exit 78  # Exit code 78 will be used to skip the job
    fi
else
    echo "⚠️  Warning: GH_TOKEN not set, skipping GitHub release check"
fi

# Check if dist binaries exist for the version
echo "🔍 Checking if release binaries exist..."
BINARY_FOUND=false

# Check for common binary paths
for arch in "darwin/amd64" "darwin/arm64" "linux/amd64"; do
    BINARY_PATH="dist/${arch}/bifrost-http"
    if [ -f "$BINARY_PATH" ]; then
        echo "✅ Found binary: $BINARY_PATH"
        BINARY_FOUND=true
        break
    fi
done

if [ "$BINARY_FOUND" = false ]; then
    echo "⚠️  Warning: No release binaries found in dist/, but continuing..."
    echo "    This might be expected if binaries are uploaded to external storage"
fi

echo ""
echo "✅ Verification complete: bifrost-http v${VERSION} was successfully released"
echo "    Proceeding with Docker image build..."

