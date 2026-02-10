#!/usr/bin/env bash
set -euo pipefail

# Test bifrost-http component
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

# Cleanup function to ensure Docker services are stopped
cleanup_docker() {
  echo "🧹 Cleaning up Docker services..."
  docker compose -f "$CONFIGS_DIR/docker-compose.yml" down 2>/dev/null || true
}

CONFIGS_DIR=".github/workflows/configs"

# Register cleanup handler to run on script exit (success or failure)
trap cleanup_docker EXIT

# Build UI first before we can validate the transport build
echo "🎨 Building UI..."
make build-ui

# Building hello-world plugin
echo "🔨 Building hello-world plugin..."
cd examples/plugins/hello-world
make build
cd ../../..

# Validate transport build
echo "🔨 Validating transport build..."
cd transports
go build ./...

# Run unit tests with coverage
echo "🧪 Running unit tests with coverage..."
go test --race -coverprofile=coverage.txt -coverpkg=./... ./...

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

# Build the binary for integration testing
echo "🔨 Building binary for integration testing..."
mkdir -p ../tmp
cd bifrost-http
go build -o ../../tmp/bifrost-http .
cd ..

# Run integration tests with different configurations
echo "🧪 Running integration tests with different configurations..."
CONFIGS_TO_TEST=(
  "default"
  "emptystate"
  "noconfigstorenologstore"
  "witconfigstorelogstorepostgres"
  "withconfigstore"
  "withconfigstorelogsstorepostgres"
  "withconfigstorelogsstoresqlite"
  "withdynamicplugin"
  "withobservability"
  "withsemanticcache"
  "withpostgresmcpclientsinconfig"
)

TEST_BINARY="../tmp/bifrost-http"
CONFIGS_DIR="../.github/workflows/configs"

# Running docker compose
echo "🐳 Starting Docker services (PostgreSQL, Weaviate, Redis)..."
docker compose -f "$CONFIGS_DIR/docker-compose.yml" up -d

# Wait for services to be healthy with polling
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

for config in "${CONFIGS_TO_TEST[@]}"; do
  echo "  🔍 Testing with config: $config"
  config_path="$CONFIGS_DIR/$config"

  # Clean up databases before each config test for a clean slate
  echo "    🧹 Resetting PostgreSQL database..."
  docker exec -e PGPASSWORD=bifrost_password "$(docker compose -f "$CONFIGS_DIR/docker-compose.yml" ps -q postgres)" \
    psql -U bifrost -d postgres \
    -c "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = 'bifrost' AND pid <> pg_backend_pid();" \
    -c "DROP DATABASE IF EXISTS bifrost;" \
    -c "CREATE DATABASE bifrost;"

  echo "    🧹 Cleaning up SQLite database files for config: $config..."
  find "$config_path" -type f \( -name "*.db" -o -name "*.db-shm" -o -name "*.db-wal" \) -delete 2>/dev/null || true
  echo "    ✅ Database cleanup complete"

  if [ ! -d "$config_path" ]; then
    echo "    ⚠️  Warning: Config directory not found: $config_path (skipping)"
    continue
  fi

  # Create a temporary log file for server output
  SERVER_LOG=$(mktemp)

  # Start the server in background with a timeout, logging to file and console
  timeout 120s $TEST_BINARY --app-dir "$config_path" --port 18080 --log-level debug 2>&1 | tee "$SERVER_LOG" &
  SERVER_PID=$!

  # Wait for server to be ready by looking for the startup message
  echo "    ⏳ Waiting for server to start..."
  MAX_WAIT=30
  ELAPSED=0
  SERVER_READY=false

  while [ $ELAPSED -lt $MAX_WAIT ]; do
    if grep -q "successfully started bifrost, serving UI on http://localhost:18080" "$SERVER_LOG" 2>/dev/null; then
      SERVER_READY=true
      echo "    ✅ Server started successfully with config: $config"
      break
    fi

    # Check if server process is still running
    if ! kill -0 $SERVER_PID 2>/dev/null; then
      echo "    ❌ Server process died before starting with config: $config"
      rm -f "$SERVER_LOG"
      exit 1
    fi

    sleep 1
    ELAPSED=$((ELAPSED + 1))
  done

  if [ "$SERVER_READY" = false ]; then
    echo "    ❌ Server failed to start within ${MAX_WAIT}s with config: $config"
    kill $SERVER_PID 2>/dev/null || true
    wait $SERVER_PID 2>/dev/null || true
    rm -f "$SERVER_LOG"
    exit 1
  fi

  # Run get_curls.sh to test all GET endpoints
  echo "    🧪 Running API endpoint tests..."
  GET_CURLS_SCRIPT="$SCRIPT_DIR/get_curls.sh"

  if [ -f "$GET_CURLS_SCRIPT" ]; then
    BASE_URL="http://localhost:18080" "$GET_CURLS_SCRIPT"
    CURL_EXIT_CODE=$?

    if [ $CURL_EXIT_CODE -eq 0 ]; then
      echo "    ✅ API endpoint tests passed for config: $config"
    else
      echo "    ❌ API endpoint tests failed for config: $config (exit code: $CURL_EXIT_CODE)"
      kill $SERVER_PID 2>/dev/null || true
      wait $SERVER_PID 2>/dev/null || true
      rm -f "$SERVER_LOG"
      exit 1
    fi
  else
    echo "    ⚠️  Warning: get_curls.sh not found at $GET_CURLS_SCRIPT (skipping endpoint tests)"
  fi

  # Kill the server
  kill $SERVER_PID 2>/dev/null || true
  wait $SERVER_PID 2>/dev/null || true

  # Clean up log file
  rm -f "$SERVER_LOG"

  # Clean up any lingering processes
  sleep 1
done

cd ..
echo "✅ Bifrost-HTTP tests completed successfully"
