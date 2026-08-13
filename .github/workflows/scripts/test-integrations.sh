#!/usr/bin/env bash
set -euo pipefail

# Test integration tests by building bifrost-http from source, starting it,
# and running Python and TypeScript SDK integration tests
# Usage: ./test-integrations.sh

# Get the absolute path of the script directory
if command -v readlink >/dev/null 2>&1 && readlink -f "$0" >/dev/null 2>&1; then
  SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
else
  SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd -P)"
fi

# Repository root (3 levels up from .github/workflows/scripts)
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd -P)"

# Setup Go workspace for CI (go.work is gitignored, must be regenerated)
source "$SCRIPT_DIR/setup-go-workspace.sh"

echo "🧪 Running Integration Tests"
echo "   Repository root: $REPO_ROOT"

# Configuration
TEST_PORT="${PORT:-8080}"
TEST_HOST="${HOST:-localhost}"
BIFROST_PID=""
TEST_FAILED=0
LOG_FILE="$(mktemp /tmp/bifrost-integrations.XXXXXX.log)"

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

  if [ -n "${MCP_SERVER_PID:-}" ]; then
    echo "   Stopping MCP test server (PID: $MCP_SERVER_PID)..."
    kill "$MCP_SERVER_PID" 2>/dev/null || true
    wait "$MCP_SERVER_PID" 2>/dev/null || true
  fi

  # :- guards both: cleanup is defined before either variable is assigned.
  rm -f "${LOG_FILE:-}" "${MCP_SERVER_LOG_FILE:-}" 2>/dev/null || true

  exit $exit_code
}
trap cleanup EXIT

# Step 1: Obtain the bifrost-http binary
echo ""
cd "$REPO_ROOT"

# CI's build-gateway job supplies the binary as an artifact; only build it here
# when running locally (or if CI ever drops that job).
#
# The local build calls `go build` directly rather than `make build`: the
# Makefile passes GOWORK=off unless LOCAL is set, which discards the workspace
# setup-go-workspace.sh just wrote and sends the transport to the module proxy
# for core/framework/plugins. That resolves published versions, so any change
# spanning transports and an unreleased framework fails to compile here while
# building fine on a developer machine.
if [ "${SKIP_GATEWAY_BUILD:-0}" = "1" ]; then
  if [ ! -x "$REPO_ROOT/tmp/bifrost-http" ]; then
    echo "❌ SKIP_GATEWAY_BUILD=1 but no executable binary at $REPO_ROOT/tmp/bifrost-http" >&2
    exit 1
  fi
  echo "⏭️  Using prebuilt bifrost-http binary at $REPO_ROOT/tmp/bifrost-http"
else
  echo "🔨 Building UI + bifrost-http binary..."
  mkdir -p "$REPO_ROOT/tmp"
  # `make build`, not a bare `go build`: the Makefile target carries the build contract
  # (CGO_ENABLED=1, the sqlite_static tag, version ldflags, GOOS/GOARCH resolution, and
  # Linux static linking). Reproducing it here by hand means the integration suite can
  # certify a binary that differs from the artifact CI actually ships.
  #
  # LOCAL=1 keeps the go.work this script's caller just wrote. Without it the target sets
  # GOWORK=off and resolves core/framework/plugins from the module proxy, which fails to
  # build any change that spans transports and an unreleased framework - the same reason
  # the SKIP_GATEWAY_BUILD branch above exists.
  (cd "$REPO_ROOT" && make LOCAL=1 build)
fi

if [ ! -f "$REPO_ROOT/tmp/bifrost-http" ]; then
  echo "❌ Error: bifrost-http binary not found at $REPO_ROOT/tmp/bifrost-http"
  exit 1
fi

echo "✅ Build complete: $REPO_ROOT/tmp/bifrost-http"

# Step 1b: Start the local MCP fixture backing config.json's sse_mcp client.
#
# That client used to point at a hosted Composio endpoint via MCP_SSE_URL and
# friends. When the endpoint was decommissioned the config kept referencing it,
# so the gateway spent its startup retrying a dead host. examples/mcps/remote-test-server
# serves the same MCP protocol over SSE on 3012 with no auth, which is all the
# sse_mcp client needs - and it costs no secret and no egress allowlist entry.
#
# Non-fatal if missing: an unreachable MCP client makes the gateway log and move
# on, exactly as it did with the dead remote, and none of the provider
# integration tests assert on MCP.
MCP_TEST_SERVER="$REPO_ROOT/examples/mcps/remote-test-server/bin/remote-test-server"
if [ -x "$MCP_TEST_SERVER" ]; then
  echo ""
  echo "🔌 Starting local MCP test server (SSE on 3012)..."
  # mktemp rather than a fixed /tmp path, matching LOG_FILE above: a predictable name in a
  # world-writable directory can be pre-created as a symlink by another local process, and this
  # redirect would then truncate whatever it points at with the runner's permissions.
  if ! MCP_SERVER_LOG_FILE="$(mktemp /tmp/mcp-test-server.XXXXXX.log)"; then
    echo "❌ Failed to create MCP test server log file" >&2
    exit 1
  fi
  MCP_HTTP_PORT=3011 MCP_SSE_PORT=3012 "$MCP_TEST_SERVER" > "$MCP_SERVER_LOG_FILE" 2>&1 &
  MCP_SERVER_PID=$!
  for _ in $(seq 1 40); do
    if nc -z localhost 3012 2>/dev/null; then break; fi
    sleep 0.25
  done
  if nc -z localhost 3012 2>/dev/null; then
    echo "   ✅ MCP test server ready (PID: $MCP_SERVER_PID)"
  else
    echo "   ⚠️  MCP test server did not come up; sse_mcp client will be unreachable"
  fi
else
  echo "⚠️  MCP test server not built at $MCP_TEST_SERVER; sse_mcp client will be unreachable"
fi

# Step 2: Start Bifrost server with Python integration test config
echo ""
echo "🚀 Starting Bifrost server..."
echo "   Config: tests/integrations/python/config.json"
echo "   Host: $TEST_HOST"
echo "   Port: $TEST_PORT"

# Start server in background with Python config directory
"$REPO_ROOT/tmp/bifrost-http" \
  -host "$TEST_HOST" \
  -port "$TEST_PORT" \
  -log-style json \
  -log-level info \
  -app-dir "$REPO_ROOT/tests/integrations/python" \
  > "$LOG_FILE" 2>&1 &

BIFROST_PID=$!
echo "   Started with PID: $BIFROST_PID"


# The gateway's own stdout/stderr goes to $LOG_FILE, which cleanup() deletes on exit. A
# bootstrap failure (bad config path, unopenable store, port in use) is therefore invisible
# in CI - the job reports only "process died unexpectedly". Dump the tail before exiting.
dump_server_log() {
  echo ""
  echo "----- last 50 lines of bifrost server log ($LOG_FILE) -----"
  tail -n 50 "$LOG_FILE" 2>/dev/null || echo "   (log file unreadable)"
  echo "-----------------------------------------------------------"
}

# Wait for server to be ready
echo "⏳ Waiting for Bifrost to be ready..."
MAX_WAIT=30
ELAPSED=0
SERVER_READY=false

while [ $ELAPSED -lt $MAX_WAIT ]; do
  if curl --connect-timeout 10 --max-time 20 -sf "http://$TEST_HOST:$TEST_PORT/health" > /dev/null 2>&1; then
    SERVER_READY=true
    echo "✅ Bifrost is ready (took ${ELAPSED}s)"
    break
  fi
  
  # Check if server process is still running
  if ! kill -0 "$BIFROST_PID" 2>/dev/null; then
    echo "❌ Bifrost process died unexpectedly"
    dump_server_log
    exit 1
  fi
  
  sleep 1
  ELAPSED=$((ELAPSED + 1))
done

if [ "$SERVER_READY" = false ]; then
  echo "❌ Bifrost failed to start within ${MAX_WAIT}s"
  dump_server_log
  exit 1
fi

# Set environment variable for tests
export BIFROST_BASE_URL="http://$TEST_HOST:$TEST_PORT"
echo "   BIFROST_BASE_URL=$BIFROST_BASE_URL"

# Step 3: Run Python integration tests
echo ""
echo "🐍 Running Python integration tests..."
echo "="

cd "$REPO_ROOT/tests/integrations/python"

# Check if uv is available
if command -v uv >/dev/null 2>&1; then
  echo "📦 Installing Python dependencies with uv..."
  uv sync --frozen --quiet
  
  echo ""
  echo "🏃 Running Python tests..."
  if ! uv run pytest -v --tb=short; then
    echo "⚠️  Python tests failed"
    TEST_FAILED=1
  fi
else
  echo "⚠️  uv not found, trying pip..."
  
  # Create virtual environment if needed
  if [ ! -d ".venv" ]; then
    python3 -m venv .venv
  fi
  
  source .venv/bin/activate
  pip install -q -e .
  
  echo ""
  echo "🏃 Running Python tests..."
  if ! pytest -v --tb=short; then
    echo "⚠️  Python tests failed"
    TEST_FAILED=1
  fi
fi

# Step 4: Run TypeScript integration tests
echo ""
echo "📘 Running TypeScript integration tests..."
echo "="

cd "$REPO_ROOT/tests/integrations/typescript"

# Install dependencies if needed
if [ ! -d "node_modules" ]; then
  echo "📦 Installing TypeScript dependencies with npm..."
  npm ci
fi

echo ""
echo "🏃 Running TypeScript tests..."
if ! npm test; then
  echo "⚠️  TypeScript tests failed"
  TEST_FAILED=1
fi

# Summary
echo ""
echo "="
if [ $TEST_FAILED -eq 1 ]; then
  echo "❌ Some integration tests failed"
  exit 1
else
  echo "✅ All integration tests passed!"
fi
