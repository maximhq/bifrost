#!/usr/bin/env bash
set -euo pipefail

# E2E API tests: /v1, /integrations, /api (Newman/Postman).
# Usage:
#   ./test-e2e-api.sh                          # Bifrost already running at BIFROST_BASE_URL
#   ./test-e2e-api.sh <bifrost-binary> [port]   # Start Bifrost with config, then run tests
# Config: tests/integrations/python/config.json (merged with Postgres when starting server)
# Requires: Newman installed; provider API keys in environment when starting Bifrost

if command -v readlink >/dev/null 2>&1 && readlink -f "$0" >/dev/null 2>&1; then
  SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
else
  SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd -P)"
fi
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd -P)"
E2E_API_DIR="$REPO_ROOT/tests/e2e/api"
E2E_API_CONFIG="$REPO_ROOT/tests/integrations/python/config.json"

export BIFROST_BASE_URL="${BIFROST_BASE_URL:-http://localhost:8080}"

# ----- Optional: start Bifrost if binary path given -----
if [ -n "${1:-}" ]; then
  BIFROST_BINARY="$1"
  PORT="${2:-8080}"
  POSTGRES_HOST="${POSTGRES_HOST:-localhost}"
  POSTGRES_PORT="${POSTGRES_PORT:-5432}"
  POSTGRES_USER="${POSTGRES_USER:-bifrost}"
  POSTGRES_PASSWORD="${POSTGRES_PASSWORD:-bifrost_password}"
  POSTGRES_DB="${POSTGRES_DB:-bifrost}"
  POSTGRES_SSLMODE="${POSTGRES_SSLMODE:-disable}"
  export POSTGRES_HOST POSTGRES_PORT POSTGRES_USER POSTGRES_PASSWORD POSTGRES_DB POSTGRES_SSLMODE

  if [ ! -f "$BIFROST_BINARY" ] || [ ! -x "$BIFROST_BINARY" ]; then
    echo "❌ Bifrost binary not found or not executable: $BIFROST_BINARY" >&2
    exit 1
  fi
  if [ ! -f "$E2E_API_CONFIG" ]; then
    echo "❌ Config not found: $E2E_API_CONFIG" >&2
    exit 1
  fi

  TEMP_DIR=$(mktemp -d)
  MERGED_CONFIG="$TEMP_DIR/config.json"
  SERVER_LOG="$TEMP_DIR/server.log"
  BIFROST_PID=""

  cleanup() {
    local exit_code=$?
    if [ -n "${BIFROST_PID:-}" ] && kill -0 "$BIFROST_PID" 2>/dev/null; then
      kill "$BIFROST_PID" 2>/dev/null || true
      wait "$BIFROST_PID" 2>/dev/null || true
    fi
    rm -rf "$TEMP_DIR"
    exit $exit_code
  }
  trap cleanup EXIT

  echo "📝 Merged config (providers + Postgres)..."
  if command -v jq >/dev/null 2>&1; then
    jq --arg host "$POSTGRES_HOST" --arg port "$POSTGRES_PORT" --arg user "$POSTGRES_USER" \
       --arg pass "$POSTGRES_PASSWORD" --arg db "$POSTGRES_DB" --arg ssl "$POSTGRES_SSLMODE" \
       '. + {
         "config_store": {"enabled": true, "type": "postgres", "config": {"host": $host, "port": $port, "user": $user, "password": $pass, "db_name": $db, "ssl_mode": $ssl}},
         "logs_store": {"enabled": true, "type": "postgres", "config": {"host": $host, "port": $port, "user": $user, "password": $pass, "db_name": $db, "ssl_mode": $ssl}}
       }' "$E2E_API_CONFIG" > "$MERGED_CONFIG"
  else
    python3 - "$E2E_API_CONFIG" "$MERGED_CONFIG" << 'PYEOF'
import sys, json, os
with open(sys.argv[1]) as f: c = json.load(f)
pg = {"host": os.environ.get("POSTGRES_HOST", "localhost"), "port": os.environ.get("POSTGRES_PORT", "5432"), "user": os.environ.get("POSTGRES_USER", "bifrost"), "password": os.environ.get("POSTGRES_PASSWORD", "bifrost_password"), "db_name": os.environ.get("POSTGRES_DB", "bifrost"), "ssl_mode": os.environ.get("POSTGRES_SSLMODE", "disable")}
c["config_store"] = {"enabled": True, "type": "postgres", "config": pg}
c["logs_store"] = {"enabled": True, "type": "postgres", "config": dict(pg)}
with open(sys.argv[2], "w") as f: json.dump(c, f, indent=2)
PYEOF
  fi

  echo "🔄 Resetting PostgreSQL database..."
  DOCKER_COMPOSE_FILE="$REPO_ROOT/.github/workflows/configs/docker-compose.yml"
  if [ -f "$DOCKER_COMPOSE_FILE" ]; then
    POSTGRES_CONTAINER=$(docker compose -f "$DOCKER_COMPOSE_FILE" ps -q postgres)
    if [ -n "$POSTGRES_CONTAINER" ]; then
      ESCAPED_DB_NAME="${POSTGRES_DB//\"/\"\"}"
      docker exec "$POSTGRES_CONTAINER" psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d postgres -c "DROP DATABASE IF EXISTS \"$ESCAPED_DB_NAME\";" 2>/dev/null || true
      docker exec "$POSTGRES_CONTAINER" psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d postgres -c "CREATE DATABASE \"$ESCAPED_DB_NAME\";" 2>/dev/null || true
    fi
  fi

  echo "🚀 Starting Bifrost on port $PORT..."
  "$BIFROST_BINARY" --app-dir "$TEMP_DIR" --port "$PORT" --log-level debug > "$SERVER_LOG" 2>&1 &
  BIFROST_PID=$!

  MAX_WAIT=60
  ELAPSED=0
  while [ $ELAPSED -lt $MAX_WAIT ]; do
    if grep -q "successfully started bifrost" "$SERVER_LOG" 2>/dev/null; then
      echo "   ✅ Bifrost started"
      break
    fi
    if ! kill -0 "$BIFROST_PID" 2>/dev/null; then
      echo "   ❌ Bifrost process exited"
      cat "$SERVER_LOG"
      exit 1
    fi
    sleep 1
    ELAPSED=$((ELAPSED + 1))
  done

  if [ $ELAPSED -ge $MAX_WAIT ]; then
    echo "   ❌ Bifrost did not start within ${MAX_WAIT}s"
    cat "$SERVER_LOG"
    exit 1
  fi

  export BIFROST_BASE_URL="http://localhost:$PORT"
fi

# ----- Run tests (/v1, /integrations, /api) -----
echo ""
echo "🧪 Running E2E API tests (Newman)"
echo "   BIFROST_BASE_URL=$BIFROST_BASE_URL"
echo ""

if ! command -v newman &>/dev/null; then
  echo "❌ Newman is not installed. Install with: npm install -g newman" >&2
  exit 1
fi

if [ -f "$E2E_API_DIR/setup-plugin.sh" ]; then
  echo "📦 Setting up test plugin (optional)..."
  "$E2E_API_DIR/setup-plugin.sh" 2>/dev/null || echo "   Plugin setup skipped"
fi
if [ -f "$E2E_API_DIR/setup-mcp.sh" ]; then
  echo "🔌 Setting up test MCP server (optional)..."
  "$E2E_API_DIR/setup-mcp.sh" 2>/dev/null || echo "   MCP setup skipped"
fi
echo ""

cd "$E2E_API_DIR"

# In CI (e.g. GitHub Actions), generate HTML reports for artifact upload
REPORT_ARGS=""
if [ "${GITHUB_ACTIONS:-}" = "true" ] || [ "${CI:-0}" = "1" ]; then
  REPORT_ARGS="--html"
fi

echo "=========================================="
echo "Running /v1 test suite..."
echo "=========================================="
if ! ./run-newman-tests.sh $REPORT_ARGS; then
  echo "❌ /v1 test suite failed"
  exit 1
fi

echo "=========================================="
echo "Running /integrations test suites..."
echo "=========================================="
if ! ./run-all-integration-tests.sh $REPORT_ARGS; then
  echo "❌ /integrations test suites failed"
  exit 1
fi

echo "=========================================="
echo "Running /api test suite..."
echo "=========================================="
if ! ./run-newman-api-tests.sh $REPORT_ARGS; then
  echo "❌ /api test suite failed"
  exit 1
fi

echo ""
echo "✅ All E2E API tests passed (/v1, /integrations, /api)"
