#!/usr/bin/env bash
set -euo pipefail

# Test framework component
# Usage: ./test-framework.sh

# Setup Go workspace for CI
source "$(dirname "$0")/setup-go-workspace.sh"

echo "🧪 Running framework tests..."

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

# Starting dependencies of framework tests
echo "🔧 Starting dependencies of framework tests..."
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

# Validate framework build
echo "🔨 Validating framework build..."
cd framework
go build ./...
echo "✅ Framework build validation successful"

# Run framework tests with coverage
echo "🧪 Running framework tests with coverage..."
go test --race -coverprofile=coverage.txt -coverpkg=./... ./...

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
cd ..

echo "✅ Framework tests completed successfully"
