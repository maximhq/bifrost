#!/usr/bin/env bash
set -euo pipefail

# Test all plugins
# Usage: ./test-all-plugins.sh [<JSON_ARRAY_OF_PLUGINS>]
# If no argument provided, tests all plugins in the plugins/ directory

# Setup Go workspace for CI
source "$(dirname "$0")/setup-go-workspace.sh"

echo "🧪 Running plugin tests..."

# Cleanup function to ensure Docker services are stopped
cleanup_docker() {
  echo "🧹 Cleaning up Docker services..."
  if command -v docker-compose >/dev/null 2>&1; then
    docker-compose -f tests/docker-compose.yml down 2>/dev/null || true
  elif docker compose version >/dev/null 2>&1; then
    docker compose -f tests/docker-compose.yml down 2>/dev/null || true
  fi
}

# Register cleanup handler to run on script exit (success or failure)
trap cleanup_docker EXIT

# Starting dependencies of plugin tests
echo "🔧 Starting dependencies of plugin tests..."
# Use docker compose (v2) if available, fallback to docker-compose (v1)
if command -v docker-compose >/dev/null 2>&1; then
  docker-compose -f tests/docker-compose.yml up -d
elif docker compose version >/dev/null 2>&1; then
  docker compose -f tests/docker-compose.yml up -d
else
  echo "❌ Neither docker-compose nor docker compose is available"
  exit 1
fi
sleep 20

# Determine which plugins to test
if [ $# -gt 0 ] && [ -n "$1" ]; then
  CHANGED_PLUGINS_JSON="$1"
  
  # Verify jq is available
  if ! command -v jq >/dev/null 2>&1; then
    echo "❌ Error: jq is required but not installed"
    exit 1
  fi
  
  # Validate that the input is valid JSON
  if ! echo "$CHANGED_PLUGINS_JSON" | jq empty >/dev/null 2>&1; then
    echo "❌ Error: Invalid JSON provided"
    exit 1
  fi
  
  # No work early‐exit if array is empty
  if jq -e 'length==0' <<<"$CHANGED_PLUGINS_JSON" >/dev/null 2>&1; then
    echo "⏭️ No plugins to test"
    exit 0
  fi
  
  # Convert JSON array to bash array
  if ! readarray -t PLUGINS < <(echo "$CHANGED_PLUGINS_JSON" | jq -r '.[]' 2>/dev/null); then
    echo "❌ Error: Failed to parse plugin names from JSON"
    exit 1
  fi
else
  # Test all plugins in the plugins/ directory
  PLUGINS=()
  for plugin_dir in plugins/*/; do
    if [ -d "$plugin_dir" ] && [ -f "$plugin_dir/go.mod" ]; then
      plugin_name=$(basename "$plugin_dir")
      PLUGINS+=("$plugin_name")
    fi
  done
fi

if [ ${#PLUGINS[@]} -eq 0 ]; then
  echo "⏭️ No plugins to test"
  exit 0
fi

echo "🔌 Testing ${#PLUGINS[@]} plugins:"
for p in "${PLUGINS[@]}"; do
  echo "   • $p"
done

FAILED_PLUGINS=()
SUCCESS_COUNT=0
OVERALL_EXIT_CODE=0

# Test each plugin
for plugin in "${PLUGINS[@]}"; do
  echo ""
  echo "🔌 Testing plugin: $plugin"
  
  PLUGIN_DIR="plugins/$plugin"
  
  if [ ! -d "$PLUGIN_DIR" ]; then
    echo "⚠️ Warning: Plugin directory not found: $PLUGIN_DIR (skipping)"
    continue
  fi
  
  if [ ! -f "$PLUGIN_DIR/go.mod" ]; then
    echo "ℹ️ No go.mod found for $plugin, skipping tests"
    SUCCESS_COUNT=$((SUCCESS_COUNT + 1))
    continue
  fi
  
  cd "$PLUGIN_DIR"
  
  # Validate build
  echo "🔨 Validating plugin build..."
  if ! go build ./...; then
    echo "❌ Build failed for plugin: $plugin"
    FAILED_PLUGINS+=("$plugin")
    OVERALL_EXIT_CODE=1
    cd ../..
    continue
  fi
  
  # Run tests with coverage if any exist
  if go list ./... | grep -q .; then
    # Run E2E tests for governance plugin (currently disabled)
    if [ "$plugin" = "governance" ]; then
      echo "🧪 Running governance plugin tests..."
      # Governance plugin tests are currently disabled in release script
      # Just run regular tests
      if go test -v -timeout 20m -coverprofile=coverage.txt -coverpkg=./... ./...; then
        echo "✅ Tests passed for: $plugin"
        SUCCESS_COUNT=$((SUCCESS_COUNT + 1))
      else
        echo "❌ Tests failed for: $plugin"
        FAILED_PLUGINS+=("$plugin")
        OVERALL_EXIT_CODE=1
      fi
    else
      echo "🧪 Running plugin tests with coverage..."
      if go test -v -timeout 20m -coverprofile=coverage.txt -coverpkg=./... ./...; then
        echo "✅ Tests passed for: $plugin"
        SUCCESS_COUNT=$((SUCCESS_COUNT + 1))
      else
        echo "❌ Tests failed for: $plugin"
        FAILED_PLUGINS+=("$plugin")
        OVERALL_EXIT_CODE=1
      fi
    fi
    
    # Upload coverage to Codecov
    if [ -n "${CODECOV_TOKEN:-}" ] && [ -f coverage.txt ]; then
      echo "📊 Uploading coverage to Codecov..."
      curl -Os https://uploader.codecov.io/latest/linux/codecov
      chmod +x codecov
      ./codecov -t "$CODECOV_TOKEN" -f coverage.txt -F "plugin-${plugin}"
      rm -f codecov coverage.txt
    else
      rm -f coverage.txt
    fi
  else
    echo "ℹ️ No tests found for $plugin"
    SUCCESS_COUNT=$((SUCCESS_COUNT + 1))
  fi
  
  cd ../..
done

# Summary
echo ""
echo "📋 Plugin Test Summary:"
echo "   ✅ Successful: $SUCCESS_COUNT/${#PLUGINS[@]}"
echo "   ❌ Failed: ${#FAILED_PLUGINS[@]}"

if [ ${#FAILED_PLUGINS[@]} -gt 0 ]; then
  echo "   Failed plugins: ${FAILED_PLUGINS[*]}"
  echo "❌ Plugin tests completed with failures"
  exit $OVERALL_EXIT_CODE
else
  echo "   🎉 All plugin tests passed!"
  echo "✅ Plugin tests completed successfully"
fi
