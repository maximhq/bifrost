#!/usr/bin/env bash
set -euo pipefail

# Run bifrost-http build, schema checks, and unit tests only
# Usage: ./test-bifrost-http.sh

# Get the absolute path of the script directory
if command -v readlink >/dev/null 2>&1 && readlink -f "$0" >/dev/null 2>&1; then
  SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
else
  SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd -P)"
fi

# Setup Go workspace for CI
source "$(dirname "$0")/setup-go-workspace.sh"

echo "🧪 Running bifrost-http tests..."

# Validate that config.schema.json and values.schema.json are in sync
echo "🔍 Validating schema consistency between config.schema.json and values.schema.json..."
VALIDATE_SCHEMA_SCRIPT="$SCRIPT_DIR/validate-helm-schema.sh"
if [ -f "$VALIDATE_SCHEMA_SCRIPT" ]; then
  if ! "$VALIDATE_SCHEMA_SCRIPT"; then
    echo "❌ Schema validation failed. The Helm chart values.schema.json is not in sync with config.schema.json"
    exit 1
  fi
  echo "✅ Schema validation passed"
else
  echo "⚠️  Warning: validate-helm-schema.sh not found, skipping schema validation"
fi

# Unit tests only need a nonempty directory for main.go's go:embed directive.
# Keep existing assets when run locally; no UI build or gateway artifact is needed.
mkdir -p transports/bifrost-http/ui
[ -f transports/bifrost-http/ui/.gitkeep ] || echo "placeholder" > transports/bifrost-http/ui/.gitkeep

# Validate transport build
echo "🔨 Validating transport build..."
cd transports
go build ./...

# Run unit tests with coverage
echo "🧪 Running unit tests with coverage..."
# The tests/ subtree contains live gateway/provider tests, not unit tests.
# integrations/ contains SDK converter unit tests and must remain included.
packages=()
package_list=$(go list ./...)
while IFS= read -r package; do
  case "$package" in
    */bifrost-http/tests|*/bifrost-http/tests/*) continue ;;
  esac
  packages+=("$package")
done <<< "$package_list"
if [ "${#packages[@]}" -eq 0 ]; then
  echo "❌ No transport unit-test packages found" >&2
  exit 1
fi
go test -short -race -v -timeout 40m -coverprofile=coverage.txt "${packages[@]}"

# Upload coverage to Codecov
if [ -n "${CODECOV_TOKEN:-}" ]; then
  echo "📊 Uploading coverage to Codecov..."
  curl -Os https://uploader.codecov.io/latest/linux/codecov
  chmod +x codecov
  ./codecov -t "$CODECOV_TOKEN" -f coverage.txt -F transports
  rm -f codecov coverage.txt
else
  echo "ℹ️ CODECOV_TOKEN not set, skipping coverage upload"
  rm -f coverage.txt
fi

echo "✅ Bifrost-HTTP unit tests completed successfully"
