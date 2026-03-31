#!/bin/bash
# Build hello-world plugin for E2E tests
# Run from tests/e2e/api/ (or any dir; script finds repo root)

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# Repo root is three levels up from tests/e2e/api
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
PLUGIN_DIR="$REPO_ROOT/examples/plugins/hello-world"
BUILD_DIR="$PLUGIN_DIR/build"

echo "Building hello-world plugin..."

# Check if plugin source exists
if [ ! -d "$PLUGIN_DIR" ]; then
  echo "ERROR: Plugin source directory not found: $PLUGIN_DIR"
  echo "Plugin tests will be skipped."
  exit 0
fi

# Create build directory
mkdir -p "$BUILD_DIR"

# Build the plugin (native for current OS/arch)
cd "$PLUGIN_DIR" || exit 1
if command -v make &>/dev/null; then
  make build-test-plugin 2>/dev/null || make dev 2>/dev/null || true
else
  CGO_ENABLED=1 go build -buildmode=plugin -o "build/hello-world.so" . 2>/dev/null || true
fi

if [ -f "build/hello-world.so" ]; then
  echo "Plugin built successfully: $PLUGIN_DIR/build/hello-world.so"
else
  echo "WARNING: Plugin build failed or skipped (e.g. cross-compilation). Plugin tests may fail."
fi
