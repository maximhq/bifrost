#!/bin/bash

# Bifrost MCP Harness Newman Test Runner
#
# Boots a fresh Bifrost server and runs collections/mcp-harness.json against it.
# Each folder in the collection is one test case, named by its ID (ADM-05,
# LOG-27, ...), and its requests carry that ID as a prefix.
# The collection is generated from runners/build-mcp-harness.mjs at the start of each run and is
# not committed; TESTS=ID,ID runs only those tests.
#
# The server starts empty: SQLite config and logs stores in a throwaway app dir,
# no providers and no MCP clients at boot. Dashboard auth is enabled before the
# collection runs, so requests prove admin access with Basic credentials the
# runner passes in as admin_auth_header. The setup token the server boots with
# is what lets the runner create that first admin account.
#
# The MCP library syncs at boot from a one-entry mock catalog file the runner
# writes, not from the public catalog, so library tests start from known entries.
#
# The upstream MCP servers are an mcpchaos fleet (tests/cmd/mcpconsole), built
# and started here. Tests reach it through two collection variables: chaos_url,
# the fleet's base URL for both its MCP endpoints and its control API, and
# chaos_bin, the binary a STDIO client spawns to bridge to the fleet. Each test
# resets the servers it uses through the control API, so tests do not depend on
# one another.
#
# Requires a built bifrost-http binary; this runner boots its own server so the
# state each test starts from is exactly what its assertions expect.

set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
API_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
REPO_ROOT="$(cd "$API_DIR/../../.." && pwd)"
cd "$API_DIR"

COLLECTION="collections/mcp-harness.json"
REPORT_DIR="newman-reports/mcp-harness"

GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m'

BIFROST_BINARY=""
PORT="8093"
CHAOS_PORT="8494"
FOLDER=""
REPORTERS="cli"
VERBOSE=""
BAIL=""

SETUP_TOKEN="${BIFROST_SETUP_TOKEN:-bifrost-mcp-harness-setup-token}"
ADMIN_USERNAME="${BIFROST_E2E_ADMIN_USERNAME:-admin}"
ADMIN_PASSWORD="${BIFROST_E2E_ADMIN_PASSWORD:-Bifrost-E2E-Admin-Pass1!}"

while [[ $# -gt 0 ]]; do
    case "$1" in
        --binary) BIFROST_BINARY="$2"; shift 2 ;;
        --port) PORT="$2"; shift 2 ;;
        --chaos-port) CHAOS_PORT="$2"; shift 2 ;;
        --folder) FOLDER="$2"; shift 2 ;;
        --html) REPORTERS="${REPORTERS},html"; shift ;;
        --json) REPORTERS="${REPORTERS},json"; shift ;;
        --verbose) VERBOSE="--verbose"; shift ;;
        --bail) BAIL="--bail"; shift ;;
        --help)
            echo "Usage: $0 --binary <path-to-bifrost-http> [OPTIONS]"
            echo ""
            echo "Options:"
            echo "  --binary <path>   Path to a built bifrost-http binary (required)"
            echo "  --port <port>     Port to boot the server on (default: 8093)"
            echo "  --chaos-port <p>  Port for the mcpchaos fleet (default: 8494)"
            echo "  --folder <name>   Run only this collection folder (e.g. \"ADM\" or \"ADM-05 ...\")"
            echo "  --html            Generate HTML report"
            echo "  --json            Generate JSON report"
            echo "  --verbose         Show detailed Newman output"
            echo "  --bail            Stop on first failure"
            echo "  --help            Show this help message"
            exit 0 ;;
        *) echo -e "${RED}Unknown option: $1${NC}"; exit 1 ;;
    esac
done

echo -e "${GREEN}==============================================${NC}"
echo -e "${GREEN}Bifrost MCP Harness Test Runner${NC}"
echo -e "${GREEN}==============================================${NC}"
echo ""

if ! command -v newman &>/dev/null; then
    echo -e "${RED}Error: Newman is not installed${NC}"
    echo "Install it with: npm install -g newman"
    exit 1
fi
if ! command -v node &>/dev/null; then
    echo -e "${RED}Error: node is required to enable dashboard auth${NC}"
    exit 1
fi
if [ -z "$BIFROST_BINARY" ] || [ ! -x "$BIFROST_BINARY" ]; then
    echo -e "${RED}Error: --binary must point to an executable bifrost-http binary${NC}"
    exit 1
fi
if ! node runners/build-mcp-harness.mjs --out "$COLLECTION" > /dev/null; then
    echo -e "${RED}Error: could not generate $COLLECTION from runners/build-mcp-harness.mjs${NC}"
    exit 1
fi

mkdir -p "$REPORT_DIR"

SERVER_PID=""
SERVER_DIR=""
CHAOS_PID=""
CHAOS_DIR=""

cleanup() {
    for pid in "$SERVER_PID" "$CHAOS_PID"; do
        if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
            kill "$pid" 2>/dev/null || true
            wait "$pid" 2>/dev/null || true
        fi
    done
    [ -n "$SERVER_DIR" ] && rm -rf "$SERVER_DIR"
    [ -n "$CHAOS_DIR" ] && rm -rf "$CHAOS_DIR"
}
trap cleanup EXIT

port_in_use() {
    (command -v nc &>/dev/null && nc -z 127.0.0.1 "$1" 2>/dev/null) \
        || (echo >/dev/tcp/127.0.0.1/"$1") 2>/dev/null
}

if port_in_use "$PORT"; then
    echo -e "${RED}Error: port $PORT is already in use; pass --port to use another${NC}"
    exit 1
fi
if port_in_use "$CHAOS_PORT"; then
    echo -e "${RED}Error: port $CHAOS_PORT is already in use; pass --chaos-port to use another${NC}"
    exit 1
fi

# start_chaos builds mcpchaos and starts its fleet. The servers it boots with are
# only placeholders: every test re-adds the servers it uses, which resets their
# tools, faults, auth and counters.
start_chaos() {
    echo -e "${YELLOW}Building mcpchaos...${NC}"
    CHAOS_DIR="$(mktemp -d)"
    # GOWORK=off: the mock has its own module and is not part of the repo workspace.
    ( cd "$REPO_ROOT/tests/cmd/mcpconsole" && GOWORK=off go build -o "$CHAOS_DIR/mcpchaos" ./cmd/mcpchaos ) || {
        echo -e "${RED}Error: failed to build mcpchaos${NC}"; exit 1; }
    local log="$API_DIR/$REPORT_DIR/mcpchaos.log"
    "$CHAOS_DIR/mcpchaos" -addr "127.0.0.1:$CHAOS_PORT" -servers alpha,beta > "$log" 2>&1 &
    CHAOS_PID=$!
    local waited=0
    while [ $waited -lt 40 ]; do
        port_in_use "$CHAOS_PORT" && break
        if ! kill -0 "$CHAOS_PID" 2>/dev/null; then
            echo -e "${RED}mcpchaos exited during startup${NC}"; cat "$log"; exit 1
        fi
        sleep 0.5; waited=$((waited + 1))
    done
    if ! port_in_use "$CHAOS_PORT"; then
        echo -e "${RED}mcpchaos did not become ready${NC}"; cat "$log"; exit 1
    fi
    echo -e "${GREEN}mcpchaos ready on :$CHAOS_PORT${NC}"
}

write_config() {
    local dir="$1"
    cat > "$dir/mcp-library.json" <<'EOF'
{"servers": [{"name": "Harness Catalog Server", "description": "Mock catalog entry for the MCP harness", "connection_type": "http", "connection_url": "https://mcp.example.invalid/mcp", "auth_type": "none"}]}
EOF
    cat > "$dir/config.json" <<EOF
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
  "config_store": { "enabled": true, "type": "sqlite", "config": { "path": "$dir/config.db" } },
  "logs_store": { "enabled": true, "type": "sqlite", "config": { "path": "$dir/logs.db" } },
  "framework": { "pricing": { "mcp_library_url": "file://$dir/mcp-library.json" } }
}
EOF
}

start_bifrost() {
    SERVER_DIR="$(mktemp -d)"
    write_config "$SERVER_DIR"
    local log="$SERVER_DIR/server.log"
    # The setup token is what lets enable_dashboard_auth create the first admin account.
    BIFROST_SETUP_TOKEN="$SETUP_TOKEN" "$BIFROST_BINARY" --app-dir "$SERVER_DIR" --port "$PORT" --log-level info > "$log" 2>&1 &
    SERVER_PID=$!
    local elapsed=0
    while [ $elapsed -lt 60 ]; do
        grep -q "successfully started bifrost" "$log" 2>/dev/null && break
        if ! kill -0 "$SERVER_PID" 2>/dev/null; then
            echo -e "${RED}Server exited before becoming ready${NC}"; cat "$log"; exit 1
        fi
        sleep 1; elapsed=$((elapsed + 1))
    done
    if [ $elapsed -ge 60 ]; then
        echo -e "${RED}Server did not start within 60s${NC}"; cat "$log"; exit 1
    fi
    cp "$log" "$API_DIR/$REPORT_DIR/server-boot.log"
    echo -e "${GREEN}Bifrost ready on :$PORT${NC}"
}

# enable_dashboard_auth creates the admin account and turns dashboard auth on,
# so every test runs against a server that requires admin credentials.
enable_dashboard_auth() {
    BIFROST_E2E_BASE_URL="http://localhost:$PORT" \
        BIFROST_E2E_SETUP_TOKEN="$SETUP_TOKEN" \
        BIFROST_E2E_ADMIN_USERNAME="$ADMIN_USERNAME" \
        BIFROST_E2E_ADMIN_PASSWORD="$ADMIN_PASSWORD" \
        node "$API_DIR/runners/set-auth-config.mjs" enable || {
            echo -e "${RED}Error: could not enable dashboard auth${NC}"; exit 1; }
}

start_chaos
start_bifrost
enable_dashboard_auth

ADMIN_AUTH_HEADER="Basic $(printf '%s:%s' "$ADMIN_USERNAME" "$ADMIN_PASSWORD" | base64 | tr -d '\n')"

cmd=(newman run "$COLLECTION"
    --env-var "base_url=http://localhost:$PORT"
    --env-var "admin_auth_header=$ADMIN_AUTH_HEADER"
    --env-var "chaos_url=http://127.0.0.1:$CHAOS_PORT"
    --env-var "chaos_bin=$CHAOS_DIR/mcpchaos"
    --timeout-script 60000 --timeout-request 120000
    -r "$REPORTERS")
[ -n "$FOLDER" ] && cmd+=(--folder "$FOLDER")
[[ "$REPORTERS" == *"html"* ]] && cmd+=(--reporter-html-export "${REPORT_DIR}/report.html")
[[ "$REPORTERS" == *"json"* ]] && cmd+=(--reporter-json-export "${REPORT_DIR}/report.json")
[ -n "$VERBOSE" ] && cmd+=("$VERBOSE")
[ -n "$BAIL" ] && cmd+=("$BAIL")

set +e
"${cmd[@]}"
EXIT_CODE=$?
set -e

echo ""
if [ $EXIT_CODE -eq 0 ]; then
    echo -e "${GREEN}✓ All MCP harness tests passed!${NC}"
else
    echo -e "${RED}✗ Some MCP harness tests failed${NC}"
    cp "$SERVER_DIR/server.log" "$API_DIR/$REPORT_DIR/server.log" 2>/dev/null || true
    echo -e "Server log saved to: ${YELLOW}$REPORT_DIR/server.log${NC}"
fi

exit $EXIT_CODE
