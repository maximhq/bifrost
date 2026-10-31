#!/bin/bash
# Boots a clean bifrost-http for a collection runner that needs an instance of its own: no
# providers, sqlite config and logs stores in a throwaway app dir, request logging on and
# inference auth off. Sourced by the runners under runners/individual/, which set an EXIT trap
# to stop_clean_bifrost. The server inherits the runner's environment, so a collection's
# env.<NAME> provider keys and BIFROST_SETUP_TOKEN resolve as they would on any other server.

CLEAN_BIFROST_PID=""
CLEAN_BIFROST_DIR=""

# boot_clean_bifrost starts <binary> on <port> and returns once the server says it has started.
# The server gets the setup token the collections send by default unless the runner set one, so a
# local run with --binary is not refused on its management calls. It goes in the environment, not
# the config, where it would win over a token the runner exports later.
boot_clean_bifrost() {
    local binary="$1" port="$2" elapsed=0
    export BIFROST_SETUP_TOKEN="${BIFROST_SETUP_TOKEN:-bifrost-e2e-setup-token}"
    if [ -z "$binary" ] || [ ! -x "$binary" ]; then
        echo "Error: --binary must point to an executable bifrost-http binary" >&2
        return 1
    fi
    if (echo >/dev/tcp/127.0.0.1/"$port") 2>/dev/null; then
        echo "Error: port $port is already in use; pass --port to use another" >&2
        return 1
    fi
    CLEAN_BIFROST_DIR="$(mktemp -d)"
    cat > "$CLEAN_BIFROST_DIR/config.json" <<EOF
{
  "\$schema": "https://www.getbifrost.ai/schema",
  "client": {
    "drop_excess_requests": false,
    "initial_pool_size": 50,
    "allowed_origins": ["*"],
    "enable_logging": true,
    "enforce_auth_on_inference": false,
    "max_request_body_size_mb": 100
  },
  "config_store": { "enabled": true, "type": "sqlite", "config": { "path": "$CLEAN_BIFROST_DIR/config.db" } },
  "logs_store": { "enabled": true, "type": "sqlite", "config": { "path": "$CLEAN_BIFROST_DIR/logs.db" } }
}
EOF
    "$binary" --app-dir "$CLEAN_BIFROST_DIR" --port "$port" --log-level info > "$CLEAN_BIFROST_DIR/server.log" 2>&1 &
    CLEAN_BIFROST_PID=$!
    until grep -q "successfully started bifrost" "$CLEAN_BIFROST_DIR/server.log" 2>/dev/null; do
        if ! kill -0 "$CLEAN_BIFROST_PID" 2>/dev/null; then
            echo "Error: bifrost exited before it was ready" >&2
            cat "$CLEAN_BIFROST_DIR/server.log" >&2
            return 1
        fi
        if [ "$elapsed" -ge 90 ]; then
            echo "Error: bifrost did not start within 90s" >&2
            cat "$CLEAN_BIFROST_DIR/server.log" >&2
            return 1
        fi
        sleep 1
        elapsed=$((elapsed + 1))
    done
    echo "Bifrost ready on :$port (app dir $CLEAN_BIFROST_DIR)"
}

# stop_clean_bifrost stops the server and removes its app dir. On a failed run it first copies
# the server log into <report-dir>, because the app dir does not outlive the runner. A server still
# up CLEAN_BIFROST_STOP_GRACE seconds (35) after the stop signal is killed: its shutdown waits on open
# requests with no limit, and a run must not hang on one Newman gave up on. The grace outlasts the
# server's own 30s cleanup limit.
stop_clean_bifrost() {
    local code=$? report_dir="$1" waited=0 grace="${CLEAN_BIFROST_STOP_GRACE:-35}"
    if [ -n "$CLEAN_BIFROST_PID" ] && kill -0 "$CLEAN_BIFROST_PID" 2>/dev/null; then
        kill "$CLEAN_BIFROST_PID" 2>/dev/null || true
        while kill -0 "$CLEAN_BIFROST_PID" 2>/dev/null && [ "$waited" -lt "$grace" ]; do
            sleep 1
            waited=$((waited + 1))
        done
        if kill -0 "$CLEAN_BIFROST_PID" 2>/dev/null; then
            echo "Bifrost did not stop within ${grace}s; killing it" >&2
            kill -9 "$CLEAN_BIFROST_PID" 2>/dev/null || true
        fi
        wait "$CLEAN_BIFROST_PID" 2>/dev/null || true
    fi
    if [ -n "$CLEAN_BIFROST_DIR" ]; then
        if [ "$code" -ne 0 ] && [ -n "$report_dir" ]; then
            mkdir -p "$report_dir"
            cp "$CLEAN_BIFROST_DIR/server.log" "$report_dir/server.log" 2>/dev/null || true
            echo "Server log saved to: $report_dir/server.log"
        fi
        rm -rf "$CLEAN_BIFROST_DIR"
    fi
    return "$code"
}
