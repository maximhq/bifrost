#!/usr/bin/env bash
set -euo pipefail

# Run integration tests with Bifrost binary and PostgreSQL
# Usage: ./run-integration-tests.sh <bifrost-binary-path> [port]

# Get the absolute path of the script directory
if command -v readlink >/dev/null 2>&1 && readlink -f "$0" >/dev/null 2>&1; then
  SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
else
  SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd -P)"
fi

# Repository root (3 levels up from .github/workflows/scripts)
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd -P)"

# Parse arguments
if [ "${1:-}" = "" ]; then
  echo "Usage: $0 <bifrost-binary-path> [port]" >&2
  echo "" >&2
  echo "Arguments:" >&2
  echo "  bifrost-binary-path  Path to the bifrost-http binary" >&2
  echo "  port                 Port to run Bifrost on (default: 8080)" >&2
  exit 1
fi

BIFROST_BINARY="$1"
PORT="${2:-8080}"

# PostgreSQL configuration (from environment or defaults)
POSTGRES_HOST="${POSTGRES_HOST:-localhost}"
POSTGRES_PORT="${POSTGRES_PORT:-5432}"
POSTGRES_USER="${POSTGRES_USER:-bifrost}"
POSTGRES_PASSWORD="${POSTGRES_PASSWORD:-bifrost_password}"
POSTGRES_DB="${POSTGRES_DB:-bifrost}"
POSTGRES_SSLMODE="${POSTGRES_SSLMODE:-disable}"

# Validate binary exists and is executable
if [ ! -f "$BIFROST_BINARY" ]; then
  echo "❌ Error: Bifrost binary not found: $BIFROST_BINARY" >&2
  exit 1
fi

if [ ! -x "$BIFROST_BINARY" ]; then
  echo "❌ Error: Bifrost binary is not executable: $BIFROST_BINARY" >&2
  exit 1
fi

echo "🧪 Running Bifrost Integration Tests"
echo "   Binary: $BIFROST_BINARY"
echo "   Port: $PORT"

# Create temp directory for merged config
TEMP_DIR=$(mktemp -d)
MERGED_CONFIG="$TEMP_DIR/config.json"
echo "📁 Using temp directory: $TEMP_DIR"

# Cleanup function
cleanup() {
  local exit_code=$?
  echo ""
  echo "🧹 Cleaning up..."
  
  # Kill Bifrost server if running
  if [ -n "${BIFROST_PID:-}" ]; then
    echo "   Stopping Bifrost server (PID: $BIFROST_PID)..."
    kill "$BIFROST_PID" 2>/dev/null || true
    wait "$BIFROST_PID" 2>/dev/null || true
  fi
  
  # Remove temp directory
  if [ -d "$TEMP_DIR" ]; then
    echo "   Removing temp directory..."
    rm -rf "$TEMP_DIR"
  fi
  
  exit $exit_code
}
trap cleanup EXIT

# Create merged config
echo "📝 Creating merged config with PostgreSQL..."

# Base config from tests/integrations
BASE_CONFIG="$REPO_ROOT/tests/integrations/config.json"

if [ ! -f "$BASE_CONFIG" ]; then
  echo "❌ Error: Base config not found: $BASE_CONFIG" >&2
  exit 1
fi

# Use jq to merge configs if available, otherwise use Python
#
# NOTE: The following config merge INTENTIONALLY OVERWRITES any existing
# config_store and logs_store keys from the base config. This is required
# because:
#   1. Integration tests MUST use the local PostgreSQL instance to validate
#      database-related functionality (config persistence, logging, etc.)
#   2. The base config (tests/integrations/config.json) typically has these
#      stores disabled; we need to fully replace them with enabled PostgreSQL
#      config pointing to the test container.
#   3. Deep-merging is NOT desired here - we need a complete, known-good
#      PostgreSQL configuration regardless of what the base config contains.
#
# Edge cases handled:
#   - Base config has no store keys: jq/Python adds them (no issue)
#   - Base config has stores disabled: fully replaced with enabled PostgreSQL
#   - Base config has different store type (e.g., sqlite): fully replaced
#   - Base config has partial PostgreSQL config: fully replaced to ensure
#     correct credentials for the test container
#
if command -v jq >/dev/null 2>&1; then
  # jq '. + {...}' performs shallow merge at top level, fully replacing
  # config_store and logs_store keys (intentional - see note above)
  jq --arg host "$POSTGRES_HOST" \
     --arg port "$POSTGRES_PORT" \
     --arg user "$POSTGRES_USER" \
     --arg pass "$POSTGRES_PASSWORD" \
     --arg db "$POSTGRES_DB" \
     --arg ssl "$POSTGRES_SSLMODE" \
     '. + {
    "config_store": {
      "enabled": true,
      "type": "postgres",
      "config": {
        "host": $host,
        "port": $port,
        "user": $user,
        "password": $pass,
        "db_name": $db,
        "ssl_mode": $ssl
      }
    },
    "logs_store": {
      "enabled": true,
      "type": "postgres",
      "config": {
        "host": $host,
        "port": $port,
        "user": $user,
        "password": $pass,
        "db_name": $db,
        "ssl_mode": $ssl
      }
    }
  }' "$BASE_CONFIG" > "$MERGED_CONFIG"
else
  # Fallback to Python if jq is not available
  # Same intentional overwrite behavior as jq path (see note above)
  python3 - "$BASE_CONFIG" "$MERGED_CONFIG" << 'EOF'
import sys
import json
import os

base_path = sys.argv[1]
merged_path = sys.argv[2]

with open(base_path, "r") as f:
    config = json.load(f)

postgres_config = {
    "host": os.environ.get("POSTGRES_HOST", "localhost"),
    "port": os.environ.get("POSTGRES_PORT", "5432"),
    "user": os.environ.get("POSTGRES_USER", "bifrost"),
    "password": os.environ.get("POSTGRES_PASSWORD", "bifrost_password"),
    "db_name": os.environ.get("POSTGRES_DB", "bifrost"),
    "ssl_mode": os.environ.get("POSTGRES_SSLMODE", "disable")
}

# Intentionally overwrite any existing store config to force PostgreSQL
# for integration tests (see detailed note in bash section above)
config["config_store"] = {
    "enabled": True,
    "type": "postgres",
    "config": postgres_config
}

config["logs_store"] = {
    "enabled": True,
    "type": "postgres",
    "config": postgres_config.copy()
}

with open(merged_path, "w") as f:
    json.dump(config, f, indent=2)
EOF
fi

echo "   ✅ Merged config created at: $MERGED_CONFIG"

# Reset PostgreSQL database
echo "🔄 Resetting PostgreSQL database..."
DOCKER_COMPOSE_FILE="$REPO_ROOT/.github/workflows/configs/docker-compose.yml"

if [ -f "$DOCKER_COMPOSE_FILE" ]; then
  POSTGRES_CONTAINER=$(docker compose -f "$DOCKER_COMPOSE_FILE" ps -q postgres 2>/dev/null || true)
  
  if [ -n "$POSTGRES_CONTAINER" ]; then
    docker exec "$POSTGRES_CONTAINER" \
      psql -U "$POSTGRES_USER" -d postgres -c "DROP DATABASE IF EXISTS $POSTGRES_DB; CREATE DATABASE $POSTGRES_DB;" \
      2>/dev/null || echo "   ⚠️  Could not reset database (container may not be running)"
    echo "   ✅ Database reset complete"
  else
    echo "   ⚠️  PostgreSQL container not found, skipping database reset"
  fi
else
  echo "   ⚠️  Docker compose file not found, skipping database reset"
fi

# Start Bifrost server
echo "🚀 Starting Bifrost server..."
SERVER_LOG="$TEMP_DIR/server.log"

"$BIFROST_BINARY" --app-dir "$TEMP_DIR" --port "$PORT" --log-level debug > "$SERVER_LOG" 2>&1 &
BIFROST_PID=$!

echo "   Started Bifrost with PID: $BIFROST_PID"

# Wait for server to be ready
echo "⏳ Waiting for Bifrost to start..."
MAX_WAIT=60
ELAPSED=0
SERVER_READY=false

while [ $ELAPSED -lt $MAX_WAIT ]; do
  if grep -q "successfully started bifrost" "$SERVER_LOG" 2>/dev/null; then
    SERVER_READY=true
    echo "   ✅ Bifrost started successfully"
    break
  fi
  
  # Check if server process is still running
  if ! kill -0 "$BIFROST_PID" 2>/dev/null; then
    echo "   ❌ Bifrost process died unexpectedly"
    echo "   Server log:"
    cat "$SERVER_LOG"
    exit 1
  fi
  
  sleep 1
  ELAPSED=$((ELAPSED + 1))
done

if [ "$SERVER_READY" = false ]; then
  echo "   ❌ Bifrost failed to start within ${MAX_WAIT}s"
  echo "   Server log:"
  cat "$SERVER_LOG"
  exit 1
fi

# Set environment variable for tests
export BIFROST_BASE_URL="http://localhost:$PORT"
echo "   BIFROST_BASE_URL=$BIFROST_BASE_URL"

# Run Python integration tests
echo ""
echo "🧪 Running Python integration tests..."
echo "="

cd "$REPO_ROOT/tests/integrations"

# Check if uv is available
if command -v uv >/dev/null 2>&1; then
  echo "📦 Installing dependencies with uv..."
  uv sync --quiet
  
  echo ""
  echo "🏃 Running tests..."
  TEST_EXIT_CODE=0
  uv run python run_all_tests.py --verbose || TEST_EXIT_CODE=$?
else
  echo "⚠️  uv not found, trying pip..."
  
  # Create virtual environment if needed
  if [ ! -d ".venv" ]; then
    python3 -m venv .venv
  fi
  
  source .venv/bin/activate
  pip install -q -e .
  
  echo ""
  echo "🏃 Running tests..."
  TEST_EXIT_CODE=0
  python run_all_tests.py --verbose || TEST_EXIT_CODE=$?
fi

echo ""
echo "="

if [ $TEST_EXIT_CODE -eq 0 ]; then
  echo "✅ All integration tests passed!"
else
  echo "❌ Some integration tests failed (exit code: $TEST_EXIT_CODE)"
fi

# Exit with test result code (cleanup trap will run)
exit $TEST_EXIT_CODE

