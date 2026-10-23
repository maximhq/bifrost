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
# Requires a built bifrost-http binary; this runner boots its own server so the
# state each test starts from is exactly what its assertions expect.

set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
API_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
cd "$API_DIR"

COLLECTION="collections/mcp-harness.json"
REPORT_DIR="newman-reports/mcp-harness"

GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m'

BIFROST_BINARY=""
PORT="8093"
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

cleanup() {
    if [ -n "$SERVER_PID" ] && kill -0 "$SERVER_PID" 2>/dev/null; then
        kill "$SERVER_PID" 2>/dev/null || true
        wait "$SERVER_PID" 2>/dev/null || true
    fi
    [ -n "$SERVER_DIR" ] && rm -rf "$SERVER_DIR"
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

write_config() {
    local dir="$1"
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
  "logs_store": { "enabled": true, "type": "sqlite", "config": { "path": "$dir/logs.db" } }
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

start_bifrost
enable_dashboard_auth

ADMIN_AUTH_HEADER="Basic $(printf '%s:%s' "$ADMIN_USERNAME" "$ADMIN_PASSWORD" | base64 | tr -d '\n')"

cmd=(newman run "$COLLECTION"
    --env-var "base_url=http://localhost:$PORT"
    --env-var "admin_auth_header=$ADMIN_AUTH_HEADER"
    --timeout-script 60000 --timeout 120000
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
