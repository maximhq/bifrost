#!/bin/bash

# Bifrost MCP Code Mode Newman Test Runner
#
# Boots a fresh Bifrost server with one Code Mode MCP client and runs
# collections/bifrost-v1-mcp-codemode.postman_collection.json against it.
# Fully hermetic: no provider API keys, no paid requests.
#
# Two local upstreams are started by this runner:
#   - examples/mcps/http-no-ping-server (tools echo, add, greet), registered as
#     the Code Mode client "codemcp" with
#       tools_to_execute      = ["echo", "add"]   (greet is not executable)
#       tools_to_auto_execute = ["echo"]          (add needs approval)
#   - runners/codemode-llm-fixture.mjs, a scripted OpenAI-compatible model that
#     answers with executeToolCode calls, so /v1/chat/completions drives the real
#     agent loop (the unattended path) deterministically.
#
# The collection pins both execution paths:
#   - /v1/mcp/tool/execute is the approved / application-driven path: nested tool
#     calls are bound by tools_to_execute only.
#   - /v1/chat/completions is the unattended agent path: nested tool calls are
#     also bound by tools_to_auto_execute, however the code reaches the tool.
#
# Requires a built bifrost-http binary; this runner boots its own server so the
# allow-lists above are exactly what the assertions expect.

set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
API_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
REPO_ROOT="$(cd "$API_DIR/../../.." && pwd)"
cd "$API_DIR"

COLLECTION="collections/bifrost-v1-mcp-codemode.postman_collection.json"
REPORT_DIR="newman-reports/mcp-codemode"
MCP_UPSTREAM_DIR="$REPO_ROOT/examples/mcps/http-no-ping-server"
LLM_FIXTURE="$API_DIR/runners/codemode-llm-fixture.mjs"

GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m'

BIFROST_BINARY=""
PORT="8092"
MCP_UPSTREAM_PORT="3002"
LLM_FIXTURE_PORT="8791"
REPORTERS="cli"
VERBOSE=""
BAIL=""

while [[ $# -gt 0 ]]; do
    case "$1" in
        --binary) BIFROST_BINARY="$2"; shift 2 ;;
        --port) PORT="$2"; shift 2 ;;
        --mcp-port) MCP_UPSTREAM_PORT="$2"; shift 2 ;;
        --llm-port) LLM_FIXTURE_PORT="$2"; shift 2 ;;
        --html) REPORTERS="${REPORTERS},html"; shift ;;
        --json) REPORTERS="${REPORTERS},json"; shift ;;
        --verbose) VERBOSE="--verbose"; shift ;;
        --bail) BAIL="--bail"; shift ;;
        --help)
            echo "Usage: $0 --binary <path-to-bifrost-http> [OPTIONS]"
            echo ""
            echo "Options:"
            echo "  --binary <path>   Path to a built bifrost-http binary (required)"
            echo "  --port <port>     Port to boot the server on (default: 8092)"
            echo "  --mcp-port <port> Port for the upstream MCP test server (default: 3002)"
            echo "  --llm-port <port> Port for the scripted LLM fixture (default: 8791)"
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
echo -e "${GREEN}Bifrost MCP Code Mode Test Runner${NC}"
echo -e "${GREEN}==============================================${NC}"
echo ""

if ! command -v newman &>/dev/null; then
    echo -e "${RED}Error: Newman is not installed${NC}"
    echo "Install it with: npm install -g newman"
    exit 1
fi
if ! command -v node &>/dev/null; then
    echo -e "${RED}Error: node is required for the LLM fixture${NC}"
    exit 1
fi
if [ -z "$BIFROST_BINARY" ] || [ ! -x "$BIFROST_BINARY" ]; then
    echo -e "${RED}Error: --binary must point to an executable bifrost-http binary${NC}"
    exit 1
fi
if [ ! -f "$COLLECTION" ]; then
    echo -e "${RED}Error: Collection file not found: $COLLECTION${NC}"
    exit 1
fi

mkdir -p "$REPORT_DIR"

SERVER_PID=""
SERVER_DIR=""
MCP_UPSTREAM_PID=""
MCP_UPSTREAM_TMP=""
LLM_FIXTURE_PID=""

cleanup() {
    for pid in "$SERVER_PID" "$LLM_FIXTURE_PID" "$MCP_UPSTREAM_PID"; do
        if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
            kill "$pid" 2>/dev/null || true
            wait "$pid" 2>/dev/null || true
        fi
    done
    [ -n "$SERVER_DIR" ] && rm -rf "$SERVER_DIR"
    [ -n "$MCP_UPSTREAM_TMP" ] && rm -rf "$MCP_UPSTREAM_TMP"
}
trap cleanup EXIT

port_in_use() {
    (command -v nc &>/dev/null && nc -z 127.0.0.1 "$1" 2>/dev/null) \
        || (echo >/dev/tcp/127.0.0.1/"$1") 2>/dev/null
}

# wait_for_port <port> <pid> <name> <log>
wait_for_port() {
    local port="$1" pid="$2" name="$3" log="$4" waited=0
    while [ $waited -lt 40 ]; do
        if port_in_use "$port"; then
            echo -e "${GREEN}$name ready on :$port${NC}"
            return
        fi
        if ! kill -0 "$pid" 2>/dev/null; then
            echo -e "${RED}$name exited during startup${NC}"
            cat "$log"
            exit 1
        fi
        sleep 0.5; waited=$((waited + 1))
    done
    echo -e "${RED}$name did not become ready${NC}"; cat "$log"; exit 1
}

for p in "$PORT" "$MCP_UPSTREAM_PORT" "$LLM_FIXTURE_PORT"; do
    if port_in_use "$p"; then
        echo -e "${RED}Error: port $p is already in use; pass --port / --mcp-port / --llm-port to use another${NC}"
        exit 1
    fi
done

start_upstream_mcp() {
    if [ ! -d "$MCP_UPSTREAM_DIR" ]; then
        echo -e "${RED}Error: upstream MCP server source not found: $MCP_UPSTREAM_DIR${NC}"
        exit 1
    fi
    echo -e "${YELLOW}Building upstream MCP server...${NC}"
    # GOWORK=off: the example has its own module and is not part of the repo workspace.
    MCP_UPSTREAM_TMP="$(mktemp -d)"
    ( cd "$MCP_UPSTREAM_DIR" && GOWORK=off go build -o "$MCP_UPSTREAM_TMP/http-no-ping-server" . ) || {
        echo -e "${RED}Error: failed to build upstream MCP server${NC}"; exit 1; }
    local log="$API_DIR/$REPORT_DIR/upstream-mcp.log"
    MCP_SERVER_PORT="$MCP_UPSTREAM_PORT" "$MCP_UPSTREAM_TMP/http-no-ping-server" > "$log" 2>&1 &
    MCP_UPSTREAM_PID=$!
    wait_for_port "$MCP_UPSTREAM_PORT" "$MCP_UPSTREAM_PID" "Upstream MCP server" "$log"
}

start_llm_fixture() {
    local log="$API_DIR/$REPORT_DIR/llm-fixture.log"
    CODEMODE_LLM_FIXTURE_PORT="$LLM_FIXTURE_PORT" node "$LLM_FIXTURE" > "$log" 2>&1 &
    LLM_FIXTURE_PID=$!
    wait_for_port "$LLM_FIXTURE_PORT" "$LLM_FIXTURE_PID" "LLM fixture" "$log"
}

write_config() {
    local dir="$1"
    cat > "$dir/config.json" <<EOF
{
  "\$schema": "https://www.getbifrost.ai/schema",
  "client": {
    "drop_excess_requests": false,
    "initial_pool_size": 50,
    "allowed_origins": ["*"],
    "enable_logging": false,
    "enforce_auth_on_inference": false,
    "max_request_body_size_mb": 100
  },
  "config_store": { "enabled": true, "type": "sqlite", "config": { "path": "$dir/config.db" } },
  "logs_store": { "enabled": false },
  "providers": {
    "openai": {
      "keys": [{ "name": "codemode-fixture", "value": "sk-codemode-fixture", "models": ["*"], "weight": 1 }],
      "network_config": { "base_url": "http://127.0.0.1:$LLM_FIXTURE_PORT", "default_request_timeout_in_seconds": 30 }
    }
  },
  "mcp": {
    "client_configs": [
      {
        "client_id": "codemcp",
        "name": "codemcp",
        "connection_type": "http",
        "connection_string": "http://localhost:$MCP_UPSTREAM_PORT/",
        "auth_type": "none",
        "is_ping_available": false,
        "is_code_mode_client": true,
        "tools_to_execute": ["echo", "add"],
        "tools_to_auto_execute": ["echo"]
      }
    ]
  }
}
EOF
}

start_bifrost() {
    SERVER_DIR="$(mktemp -d)"
    write_config "$SERVER_DIR"
    local log="$SERVER_DIR/server.log"
    "$BIFROST_BINARY" --app-dir "$SERVER_DIR" --port "$PORT" --log-level info > "$log" 2>&1 &
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

start_upstream_mcp
start_llm_fixture
start_bifrost

cmd=(newman run "$COLLECTION"
    --env-var "base_url=http://localhost:$PORT"
    --env-var "llm_fixture_url=http://127.0.0.1:$LLM_FIXTURE_PORT"
    --timeout-script 60000 --timeout 120000
    -r "$REPORTERS")
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
    echo -e "${GREEN}✓ All MCP Code Mode checks passed!${NC}"
else
    echo -e "${RED}✗ Some MCP Code Mode checks failed${NC}"
    cp "$SERVER_DIR/server.log" "$API_DIR/$REPORT_DIR/server.log" 2>/dev/null || true
    echo -e "Server log saved to: ${YELLOW}$REPORT_DIR/server.log${NC}"
fi

exit $EXIT_CODE
