#!/usr/bin/env bash
set -euo pipefail

# API integrations test: compiles bifrost-http, runs it against PostgreSQL using
# tests/config.json (with a runtime config_store/logs_store overlay), then runs
# the api-management newman collection via tests/e2e/api/runners/run-newman-api-tests.sh.

if command -v readlink >/dev/null 2>&1 && readlink -f "$0" >/dev/null 2>&1; then
  SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
else
  SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd -P)"
fi
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd -P)"

CONFIGS_DIR="$REPO_ROOT/.github/workflows/configs"
COMPOSE_FILE="$CONFIGS_DIR/docker-compose.yml"
SOURCE_CONFIG="$REPO_ROOT/tests/config.json"
RUNNER="$REPO_ROOT/tests/e2e/api/runners/run-newman-api-tests.sh"
BIN_DIR="$REPO_ROOT/tmp"
BIFROST_BINARY="$BIN_DIR/bifrost-http"

PORT="${PORT:-8080}"
POSTGRES_HOST="${POSTGRES_HOST:-localhost}"
POSTGRES_PORT="${POSTGRES_PORT:-5432}"
POSTGRES_USER="${POSTGRES_USER:-bifrost}"
POSTGRES_PASSWORD="${POSTGRES_PASSWORD:-bifrost_password}"
POSTGRES_DB="${POSTGRES_DB:-bifrost}"
POSTGRES_SSLMODE="${POSTGRES_SSLMODE:-disable}"
export POSTGRES_HOST POSTGRES_PORT POSTGRES_USER POSTGRES_PASSWORD POSTGRES_DB POSTGRES_SSLMODE

if [ ! -f "$SOURCE_CONFIG" ]; then
  echo "❌ Config not found: $SOURCE_CONFIG" >&2
  exit 1
fi
if [ ! -f "$RUNNER" ]; then
  echo "❌ Runner not found: $RUNNER" >&2
  exit 1
fi
if [ ! -f "$COMPOSE_FILE" ]; then
  echo "❌ docker-compose file not found: $COMPOSE_FILE" >&2
  exit 1
fi

if ! command -v jq >/dev/null 2>&1; then
  echo "❌ jq is required" >&2
  exit 1
fi
if ! command -v newman >/dev/null 2>&1; then
  echo "❌ newman is required (npm install -g newman newman-reporter-htmlextra)" >&2
  exit 1
fi

source "$SCRIPT_DIR/setup-go-workspace.sh"

TEMP_DIR=$(mktemp -d)
MERGED_CONFIG="$TEMP_DIR/config.json"
SERVER_LOG="$TEMP_DIR/server.log"
BIFROST_PID=""

cleanup() {
  local exit_code=$?
  if [ -n "${BIFROST_PID:-}" ] && kill -0 "$BIFROST_PID" 2>/dev/null; then
    echo "🧹 Stopping bifrost (PID $BIFROST_PID)..."
    kill "$BIFROST_PID" 2>/dev/null || true
    wait "$BIFROST_PID" 2>/dev/null || true
  fi
  echo "🧹 Stopping Docker services..."
  docker compose -f "$COMPOSE_FILE" down 2>/dev/null || true
  rm -rf "$TEMP_DIR"
  exit $exit_code
}
trap cleanup EXIT

echo "🎨 Building UI..."
(cd "$REPO_ROOT" && make build-ui)

echo "🔨 Building bifrost-http binary..."
mkdir -p "$BIN_DIR"
(cd "$REPO_ROOT/transports/bifrost-http" && go build -o "$BIFROST_BINARY" .)

echo "🐳 Starting Docker services (PostgreSQL + dependencies)..."
docker compose -f "$COMPOSE_FILE" up -d

echo "⏳ Waiting for Docker services to become healthy..."
MAX_WAIT=300
ELAPSED=0
EXPECTED_SERVICES=$(docker compose -f "$COMPOSE_FILE" config --services 2>/dev/null | wc -l | tr -d ' ')
while [ $ELAPSED -lt $MAX_WAIT ]; do
  RUNNING_COUNT=$(docker compose -f "$COMPOSE_FILE" ps --status running -q 2>/dev/null | wc -l | tr -d ' ')
  HEALTH_OUTPUT=$(docker compose -f "$COMPOSE_FILE" ps --format "{{.Name}}:{{.Health}}" 2>/dev/null)
  UNHEALTHY_COUNT=$(echo "$HEALTH_OUTPUT" | grep -cE ":(starting|unhealthy)" || true)
  if [ "$RUNNING_COUNT" -eq "$EXPECTED_SERVICES" ] && [ "$UNHEALTHY_COUNT" -eq "0" ]; then
    echo "✅ Docker services ready (${ELAPSED}s)"
    break
  fi
  sleep 2
  ELAPSED=$((ELAPSED + 2))
done
if [ $ELAPSED -ge $MAX_WAIT ]; then
  echo "❌ Docker services failed to become healthy within ${MAX_WAIT}s"
  docker compose -f "$COMPOSE_FILE" ps
  exit 1
fi

echo "🔄 Resetting PostgreSQL database..."
POSTGRES_CONTAINER=$(docker compose -f "$COMPOSE_FILE" ps -q postgres)
if [ -n "$POSTGRES_CONTAINER" ]; then
  ESCAPED_DB_NAME="${POSTGRES_DB//\"/\"\"}"
  docker exec "$POSTGRES_CONTAINER" psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d postgres \
    -c "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '$POSTGRES_DB' AND pid <> pg_backend_pid();" >/dev/null 2>&1 || true
  docker exec "$POSTGRES_CONTAINER" psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d postgres \
    -c "DROP DATABASE IF EXISTS \"$ESCAPED_DB_NAME\";"
  docker exec "$POSTGRES_CONTAINER" psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d postgres \
    -c "CREATE DATABASE \"$ESCAPED_DB_NAME\";"
fi

echo "📝 Building merged config (tests/config.json + postgres overlay)..."
jq --arg host "$POSTGRES_HOST" --arg port "$POSTGRES_PORT" --arg user "$POSTGRES_USER" \
   --arg pass "$POSTGRES_PASSWORD" --arg db "$POSTGRES_DB" --arg ssl "$POSTGRES_SSLMODE" \
   '. + {
     "config_store": {"enabled": true, "type": "postgres", "config": {"host": $host, "port": $port, "user": $user, "password": $pass, "db_name": $db, "ssl_mode": $ssl}},
     "logs_store":   {"enabled": true, "type": "postgres", "config": {"host": $host, "port": $port, "user": $user, "password": $pass, "db_name": $db, "ssl_mode": $ssl}}
   }' "$SOURCE_CONFIG" > "$MERGED_CONFIG"

echo "🚀 Starting bifrost-http on port $PORT..."
"$BIFROST_BINARY" --app-dir "$TEMP_DIR" --port "$PORT" --log-level debug > "$SERVER_LOG" 2>&1 &
BIFROST_PID=$!

MAX_WAIT=60
ELAPSED=0
while [ $ELAPSED -lt $MAX_WAIT ]; do
  if grep -q "successfully started bifrost" "$SERVER_LOG" 2>/dev/null; then
    echo "✅ Bifrost started (PID $BIFROST_PID)"
    break
  fi
  if ! kill -0 "$BIFROST_PID" 2>/dev/null; then
    echo "❌ Bifrost process exited during startup"
    cat "$SERVER_LOG"
    exit 1
  fi
  sleep 1
  ELAPSED=$((ELAPSED + 1))
done
if [ $ELAPSED -ge $MAX_WAIT ]; then
  echo "❌ Bifrost did not start within ${MAX_WAIT}s"
  cat "$SERVER_LOG"
  exit 1
fi

export BIFROST_BASE_URL="http://localhost:$PORT"

REPORT_ARGS=""
if [ "${GITHUB_ACTIONS:-}" = "true" ] || [ "${CI:-0}" = "1" ]; then
  REPORT_ARGS="--html"
fi

echo ""
echo "🧪 Running api-management newman collection..."
"$RUNNER" $REPORT_ARGS
