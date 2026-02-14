#!/usr/bin/env bash
set -euo pipefail

# Test E2E UI with Playwright
# Usage: ./test-e2e-ui.sh

# Setup Go workspace for CI
source "$(dirname "$0")/setup-go-workspace.sh"

echo "🧪 Running E2E UI tests..."

CONFIGS_DIR=".github/workflows/configs"

# Cleanup function to ensure all services are stopped
cleanup() {
  echo "🧹 Cleaning up..."
  if [ -n "${SERVER_PID:-}" ]; then
    kill "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
  fi
  rm -f "${SERVER_LOG:-}"
  docker compose -f "$CONFIGS_DIR/docker-compose.yml" down 2>/dev/null || true
}

# Register cleanup handler to run on script exit (success or failure)
trap cleanup EXIT

# Build UI
echo "🎨 Building UI..."
make build-ui

# Build bifrost-http binary
echo "🔨 Building bifrost-http binary..."
mkdir -p tmp
cd transports/bifrost-http
go build -o ../../tmp/bifrost-http .
cd ../..

# Start Docker services
echo "🐳 Starting Docker services (PostgreSQL, Redis, etc.)..."
docker compose -f "$CONFIGS_DIR/docker-compose.yml" up -d

# Wait for Docker services to be healthy with polling
echo "⏳ Waiting for Docker services to be ready..."
MAX_WAIT=300
ELAPSED=0
SERVICES_READY=false

# Get expected number of services
EXPECTED_SERVICES=$(docker compose -f "$CONFIGS_DIR/docker-compose.yml" config --services 2>/dev/null | wc -l | tr -d ' ')

while [ $ELAPSED -lt $MAX_WAIT ]; do
  # Get running container count
  RUNNING_COUNT=$(docker compose -f "$CONFIGS_DIR/docker-compose.yml" ps --status running -q 2>/dev/null | wc -l | tr -d ' ')

  # Check health status: count healthy and unhealthy (starting/unhealthy) services
  HEALTH_OUTPUT=$(docker compose -f "$CONFIGS_DIR/docker-compose.yml" ps --format "{{.Name}}:{{.Health}}" 2>/dev/null)
  HEALTHY_COUNT=$(echo "$HEALTH_OUTPUT" | grep -c ":healthy") || HEALTHY_COUNT=0
  UNHEALTHY_COUNT=$(echo "$HEALTH_OUTPUT" | grep -cE ":(starting|unhealthy)") || UNHEALTHY_COUNT=0

  # All services are ready when:
  # 1. All expected services are running
  # 2. No services are in "starting" or "unhealthy" state
  if [ "$RUNNING_COUNT" -eq "$EXPECTED_SERVICES" ] && [ "$UNHEALTHY_COUNT" -eq "0" ]; then
    SERVICES_READY=true
    echo "✅ All Docker services are ready ($HEALTHY_COUNT with healthchecks, ${ELAPSED}s)"
    break
  fi

  sleep 2
  ELAPSED=$((ELAPSED + 2))
  echo "   ⏳ Waiting for services... ($RUNNING_COUNT/$EXPECTED_SERVICES running, $HEALTHY_COUNT healthy, $UNHEALTHY_COUNT starting, ${ELAPSED}s/${MAX_WAIT}s)"
done

if [ "$SERVICES_READY" = false ]; then
  echo "❌ Docker services failed to become healthy within ${MAX_WAIT}s"
  echo "   Current service status:"
  docker compose -f "$CONFIGS_DIR/docker-compose.yml" ps
  exit 1
fi

# Reset PostgreSQL database to clean state
echo "🧹 Resetting PostgreSQL database..."
docker exec -e PGPASSWORD=bifrost_password "$(docker compose -f "$CONFIGS_DIR/docker-compose.yml" ps -q postgres)" \
  psql -U bifrost -d postgres \
  -c "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = 'bifrost' AND pid <> pg_backend_pid();" \
  -c "DROP DATABASE IF EXISTS bifrost;" \
  -c "CREATE DATABASE bifrost;"

# Start bifrost-http server with default config
SERVER_LOG=$(mktemp)
echo "🚀 Starting bifrost-http server..."
./tmp/bifrost-http --app-dir "$CONFIGS_DIR/default" --port 18080 --log-level debug 2>&1 | tee "$SERVER_LOG" &
SERVER_PID=$!

# Wait for server to be ready
echo "⏳ Waiting for server to start..."
MAX_WAIT=60
ELAPSED=0
SERVER_READY=false

while [ $ELAPSED -lt $MAX_WAIT ]; do
  if grep -q "successfully started bifrost, serving UI on http://localhost:18080" "$SERVER_LOG" 2>/dev/null; then
    SERVER_READY=true
    echo "✅ Server started successfully"
    break
  fi

  # Check if server process is still running
  if ! kill -0 $SERVER_PID 2>/dev/null; then
    echo "❌ Server process died before starting"
    exit 1
  fi

  sleep 1
  ELAPSED=$((ELAPSED + 1))
done

if [ "$SERVER_READY" = false ]; then
  echo "❌ Server failed to start within ${MAX_WAIT}s"
  exit 1
fi

# Install Playwright dependencies
echo "📦 Installing Playwright dependencies..."
cd tests/e2e
npm ci
npx playwright install --with-deps chromium

# Run Playwright tests (BASE_URL = browser; BIFROST_BASE_URL = global-setup API calls).
# Forward MCP_SSE_HEADERS so the mcp-registry SSE test can use it (set in workflow env).
echo "🎭 Running Playwright E2E tests..."
CI=true SKIP_WEB_SERVER=1 BASE_URL=http://localhost:18080 BIFROST_BASE_URL=http://localhost:18080 \
  MCP_SSE_HEADERS=${MCP_SSE_HEADERS:-} \
  npx playwright test --workers=4
PLAYWRIGHT_EXIT=$?

cd ../..

echo "✅ E2E UI tests completed"
exit $PLAYWRIGHT_EXIT
