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
  COMPOSE="docker-compose"
elif docker compose version >/dev/null 2>&1; then
  COMPOSE="docker compose"
else
  echo "❌ Neither docker-compose nor docker compose is available"
  exit 1
fi
$COMPOSE -f tests/docker-compose.yml up -d
sleep 20

# The framework logstore tests fail (not skip) in CI when ClickHouse is
# unreachable, and `up -d` does not wait for health, so gate on its /ping.
echo "⏳ Waiting for ClickHouse to become ready..."
for attempt in $(seq 1 60); do
  if $COMPOSE -f tests/docker-compose.yml exec -T clickhouse wget --spider -q http://127.0.0.1:8123/ping 2>/dev/null; then
    echo "✅ ClickHouse is ready"
    break
  fi
  if [ "$attempt" -eq 60 ]; then
    echo "❌ ClickHouse did not become ready within 120s"
    $COMPOSE -f tests/docker-compose.yml logs --tail=50 clickhouse || true
    exit 1
  fi
  sleep 2
done

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
