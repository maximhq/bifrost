#!/bin/bash
# Global setup for MCP client E2E tests: start the http-no-ping-server on port 3001.
# The API Management collection adds a test MCP client with connection_string http://localhost:3001/
# so this server must be running for Add/Update/Delete MCP Client tests to pass.

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
MCP_SERVER_DIR="$REPO_ROOT/examples/mcps/http-no-ping-server"
MCP_PORT=3001
PID_FILE="$SCRIPT_DIR/.mcp-server.pid"

# Check if MCP server is already listening on 3001 (e.g. from a previous run)
if command -v nc &>/dev/null; then
  if nc -z 127.0.0.1 "$MCP_PORT" 2>/dev/null; then
    if pgrep -f "http-no-ping-server" >/dev/null 2>&1; then
      echo "MCP server already running on port $MCP_PORT (test MCP client will use http://localhost:$MCP_PORT/)."
      exit 0
    fi
    echo "Port $MCP_PORT is occupied by a non-MCP process. Aborting setup."
    exit 1
  fi
elif command -v bash &>/dev/null && (echo >/dev/tcp/127.0.0.1/"$MCP_PORT") 2>/dev/null; then
  if pgrep -f "http-no-ping-server" >/dev/null 2>&1; then
    echo "MCP server already running on port $MCP_PORT (test MCP client will use http://localhost:$MCP_PORT/)."
    exit 0
  fi
  echo "Port $MCP_PORT is occupied by a non-MCP process. Aborting setup."
  exit 1
fi

if [ ! -d "$MCP_SERVER_DIR" ]; then
  echo "MCP server source not found: $MCP_SERVER_DIR"
  echo "MCP client tests will use fallback (accept 404/500)."
  exit 0
fi

# Build the server
echo "Building MCP test server (http-no-ping-server)..."
cd "$MCP_SERVER_DIR" || exit 0
if ! go build -o http-no-ping-server . 2>/dev/null; then
  echo "WARNING: MCP server build failed. MCP client tests will use fallback (accept 404/500)."
  exit 0
fi

# Start in background
if [ ! -f "./http-no-ping-server" ]; then
  echo "WARNING: MCP server binary not found. MCP client tests will use fallback."
  exit 0
fi

# Clean up stale PID file safely (only kill if process is our MCP server)
if [ -f "$PID_FILE" ]; then
  old_pid="$(cat "$PID_FILE" 2>/dev/null || true)"
  if [[ -n "$old_pid" && "$old_pid" =~ ^[0-9]+$ ]] && ps -p "$old_pid" -o args= 2>/dev/null | grep -q "http-no-ping-server"; then
    kill "$old_pid" 2>/dev/null || true
  fi
fi
rm -f "$PID_FILE"

echo "Starting MCP server on http://localhost:$MCP_PORT/ ..."
./http-no-ping-server &
echo $! > "$PID_FILE"

# Wait for port to be open (max 10s)
for i in $(seq 1 20); do
  if (command -v nc &>/dev/null && nc -z 127.0.0.1 "$MCP_PORT" 2>/dev/null) \
     || (command -v bash &>/dev/null && (echo >/dev/tcp/127.0.0.1/"$MCP_PORT") 2>/dev/null); then
    echo "MCP server ready at http://localhost:$MCP_PORT/ (test MCP client will use this URL)."
    exit 0
  fi
  [ $i -eq 20 ] && break
  sleep 0.5
done

echo "WARNING: MCP server may not have started in time. MCP client tests may fail or use fallback."
exit 0
