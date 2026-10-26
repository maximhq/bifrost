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
# The MCP library syncs at boot from a five-entry mock catalog the mcpchaos fleet
# serves over HTTP (server library, GET /catalog.json), not from the public
# catalog, so library tests start from known entries and can change the catalog
# while the run goes on. A test that changes it resets the library server, which
# restores this catalog.
#
# The openai, anthropic, gemini and bedrock providers point at the fleet's LLM
# surface (server llm), which answers model requests with replies each test
# scripts and records what it was sent, and the pricing and model-parameter
# datasheets come from routes on that server, so a run calls nothing outside
# this machine. Bedrock reaches it through the fleet's TLS listener on its own
# port, since Bedrock clients only speak https to a bare host; the provider
# skips verification of its self-signed certificate.
#
# config.json also declares a few MCP clients, because a client that boots pending
# verification (an OAuth or per-user headers client nobody has verified yet) can
# only come from config.json. Each belongs to one test, which its id names, and
# points at a fleet server of the same name that the test sets up.
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
CHAOS_TLS_PORT=""
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
        --chaos-tls-port) CHAOS_TLS_PORT="$2"; shift 2 ;;
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
            echo "  --chaos-tls-port <p>  Port for the fleet's TLS listener (default: the fleet's port + 1)"
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
CHAOS_TLS_PORT="${CHAOS_TLS_PORT:-$((CHAOS_PORT + 1))}"
if port_in_use "$CHAOS_TLS_PORT"; then
    echo -e "${RED}Error: port $CHAOS_TLS_PORT is already in use; pass --chaos-tls-port to use another${NC}"
    exit 1
fi

# write_chaos_seed writes the fleet mcpchaos boots with: alpha and beta, which
# are only placeholders, since every test re-adds the servers it uses (resetting
# their tools, faults, auth and counters); and library, which serves the boot
# catalog; and llm, the model provider every configured provider points at, which
# also serves the datasheets. The catalog's array order differs from name order,
# it has no stdio entry, and its URLs sit under mcp.example.invalid, so no test
# client can mark an entry installed. The pricing datasheet prices mock-model on
# each provider, so a model request has a known cost.
write_chaos_seed() {
    cat > "$1" <<'EOF'
{"servers": {
  "alpha": {"tools": [{"name": "echo_tool"}, {"name": "add_tool"}]},
  "beta": {"tools": [{"name": "echo_tool"}, {"name": "add_tool"}]},
  "library": {"tools": [], "routes": [{"method": "GET", "path": "/catalog.json", "json": {"servers": [
    {"name": "Harness Catalog Server", "description": "Mock catalog entry for the MCP harness", "category": "testing", "connection_type": "http", "connection_url": "https://mcp.example.invalid/harness/mcp", "auth_type": "none", "publisher": "Harness Labs", "tags": ["x"]},
    {"name": "Delta Files", "description": "File access, maintained by Acme", "category": "files", "connection_type": "sse", "connection_url": "https://mcp.example.invalid/delta/sse", "auth_type": "headers", "required_header_keys": ["X-Api-Key"], "publisher": "Delta Org", "tags": ["y"]},
    {"name": "Bravo Search", "description": "Web search", "category": "search", "connection_type": "http", "connection_url": "https://mcp.example.invalid/bravo/mcp", "auth_type": "oauth", "publisher": "ACME Corp", "tags": ["x", "y"]},
    {"name": "Acme Notes", "description": "Notes", "category": "files", "connection_type": "http", "connection_url": "https://mcp.example.invalid/acme/mcp", "auth_type": "none", "publisher": "Notes Inc"},
    {"name": "Echo Tickets", "description": "", "category": "", "connection_type": "sse", "connection_url": "https://mcp.example.invalid/echo/sse", "auth_type": "headers", "publisher": "", "tags": [""]}
  ]}}]},
  "llm": {"tools": [], "llm_models": ["mock-model"], "routes": [
    {"method": "GET", "path": "/pricing.json", "json": {
      "openai/mock-model": {"provider": "openai", "mode": "chat", "input_cost_per_token": 0.000001, "output_cost_per_token": 0.000002},
      "anthropic/mock-model": {"provider": "anthropic", "mode": "chat", "input_cost_per_token": 0.000001, "output_cost_per_token": 0.000002},
      "gemini/mock-model": {"provider": "gemini", "mode": "chat", "input_cost_per_token": 0.000001, "output_cost_per_token": 0.000002},
      "bedrock/mock-model": {"provider": "bedrock", "mode": "chat", "input_cost_per_token": 0.000001, "output_cost_per_token": 0.000002}}},
    {"method": "GET", "path": "/model-parameters.json", "json": {}}
  ]}
}}
EOF
}

# start_chaos builds mcpchaos and starts its fleet from the seed above, with the
# TLS listener that serves llm's Bedrock surface.
start_chaos() {
    echo -e "${YELLOW}Building mcpchaos...${NC}"
    CHAOS_DIR="$(mktemp -d)"
    # GOWORK=off: the mock has its own module and is not part of the repo workspace.
    ( cd "$REPO_ROOT/tests/cmd/mcpconsole" && GOWORK=off go build -o "$CHAOS_DIR/mcpchaos" ./cmd/mcpchaos ) || {
        echo -e "${RED}Error: failed to build mcpchaos${NC}"; exit 1; }
    local log="$API_DIR/$REPORT_DIR/mcpchaos.log"
    write_chaos_seed "$CHAOS_DIR/seed.json"
    "$CHAOS_DIR/mcpchaos" -addr "127.0.0.1:$CHAOS_PORT" -seed "$CHAOS_DIR/seed.json" \
        -tls-addr "127.0.0.1:$CHAOS_TLS_PORT" -tls-server llm > "$log" 2>&1 &
    CHAOS_PID=$!
    local waited=0
    while [ $waited -lt 40 ]; do
        port_in_use "$CHAOS_PORT" && port_in_use "$CHAOS_TLS_PORT" && break
        if ! kill -0 "$CHAOS_PID" 2>/dev/null; then
            echo -e "${RED}mcpchaos exited during startup${NC}"; cat "$log"; exit 1
        fi
        sleep 0.5; waited=$((waited + 1))
    done
    if ! port_in_use "$CHAOS_PORT" || ! port_in_use "$CHAOS_TLS_PORT"; then
        echo -e "${RED}mcpchaos did not become ready${NC}"; cat "$log"; exit 1
    fi
    echo -e "${GREEN}mcpchaos ready on :$CHAOS_PORT (TLS :$CHAOS_TLS_PORT)${NC}"
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
    "enable_logging": true,
    "enforce_auth_on_inference": false,
    "max_request_body_size_mb": 100
  },
  "config_store": { "enabled": true, "type": "sqlite", "config": { "path": "$dir/config.db" } },
  "logs_store": { "enabled": true, "type": "sqlite", "config": { "path": "$dir/logs.db" } },
  "mcp": { "client_configs": [
    { "client_id": "adm30-p", "name": "adm30_p", "connection_type": "http", "connection_string": "http://127.0.0.1:$CHAOS_PORT/s/adm30/mcp", "auth_type": "oauth", "tools_to_execute": ["*"] },
    { "client_id": "adm35-p", "name": "adm35_p", "connection_type": "http", "connection_string": "http://127.0.0.1:$CHAOS_PORT/s/adm35/mcp", "auth_type": "oauth", "tools_to_execute": ["*"] },
    { "client_id": "adm37-p", "name": "adm37_p", "connection_type": "http", "connection_string": "http://127.0.0.1:$CHAOS_PORT/s/adm37/mcp", "auth_type": "oauth", "tools_to_execute": ["*"] },
    { "client_id": "adm43-p", "name": "adm43_p", "connection_type": "http", "connection_string": "http://127.0.0.1:$CHAOS_PORT/s/adm43/mcp", "auth_type": "oauth", "needs_session_stickiness": true, "tools_to_execute": ["*"] },
    { "client_id": "adm45-h", "name": "adm45_h", "connection_type": "http", "connection_string": "http://127.0.0.1:$CHAOS_PORT/s/adm45/mcp", "auth_type": "per_user_headers", "per_user_header_keys": ["x-key"], "tools_to_execute": ["*"] },
    { "client_id": "adm68-h", "name": "adm68_h", "connection_type": "http", "connection_string": "http://127.0.0.1:$CHAOS_PORT/s/adm68/mcp", "auth_type": "per_user_headers", "per_user_header_keys": ["x-key"], "tools_to_execute": ["*"] }
  ] },
  "providers": {
    "openai": { "keys": [{ "name": "openai-mock", "value": "sk-mock", "models": ["*"], "weight": 1 }],
      "network_config": { "base_url": "http://127.0.0.1:$CHAOS_PORT/s/llm/llm/openai", "max_retries": 0, "default_request_timeout_in_seconds": 30 } },
    "anthropic": { "keys": [{ "name": "anthropic-mock", "value": "sk-ant-mock", "models": ["*"], "weight": 1 }],
      "network_config": { "base_url": "http://127.0.0.1:$CHAOS_PORT/s/llm/llm/anthropic", "max_retries": 0, "default_request_timeout_in_seconds": 30 } },
    "gemini": { "keys": [{ "name": "gemini-mock", "value": "mock-gemini-key", "models": ["*"], "weight": 1 }],
      "network_config": { "base_url": "http://127.0.0.1:$CHAOS_PORT/s/llm/llm/genai/v1beta", "max_retries": 0, "default_request_timeout_in_seconds": 30 } },
    "bedrock": { "keys": [{ "name": "bedrock-mock", "value": "mock-bedrock-token", "models": ["*"], "weight": 1,
        "bedrock_key_config": { "region": "us-east-1", "endpoints": { "runtime": "127.0.0.1:$CHAOS_TLS_PORT",
          "control_plane": "127.0.0.1:$CHAOS_TLS_PORT", "mantle": "127.0.0.1:$CHAOS_TLS_PORT" } } }],
      "network_config": { "insecure_skip_verify": true, "max_retries": 0, "default_request_timeout_in_seconds": 30 } }
  },
  "framework": { "pricing": {
    "mcp_library_url": "http://127.0.0.1:$CHAOS_PORT/s/library/rest/catalog.json",
    "pricing_url": "http://127.0.0.1:$CHAOS_PORT/s/llm/rest/pricing.json",
    "model_parameters_url": "http://127.0.0.1:$CHAOS_PORT/s/llm/rest/model-parameters.json"
  } }
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
    --env-var "setup_token=$SETUP_TOKEN"
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
