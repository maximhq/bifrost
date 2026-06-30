#!/bin/bash

# Bifrost MCP Auth Newman Test Runner
#
# Boots a fresh Bifrost server (sqlite, pre-seeded virtual keys + an upstream MCP
# client) once per inbound /mcp authentication mode:
#   - client.mcp_server_auth_mode = headers   (default; header credentials only, discovery off)
#   - client.mcp_server_auth_mode = both      (header credentials AND issued JWTs, discovery on)
#   - client.mcp_server_auth_mode = oauth     (issued JWTs only, header credentials rejected)
# and runs collections/bifrost-v1-mcp-auth.postman_collection.json against each.
#
# The collection's test scripts branch on the auth_mode env var, so a single
# collection encodes the full accept/reject matrix. The central guarantee: in
# headers mode every existing virtual-key path connects exactly as before and the
# OAuth surface is invisible (discovery 404s); enabling both only ADDS JWT
# acceptance without changing any header-credential outcome.
#
# An upstream MCP server (examples/mcps/http-no-ping-server on port 3001) is built
# and started by this runner so /mcp exposes real tools. It is pre-seeded as an
# MCP client in each boot config, so it is connected before the server reports
# ready — no post-boot registration race.
#
# Requires a built bifrost-http binary; this runner boots its own servers (each
# mode needs a different boot config, so the shared e2e server is not reused).

set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
API_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
REPO_ROOT="$(cd "$API_DIR/../../.." && pwd)"
cd "$API_DIR"

COLLECTION="collections/bifrost-v1-mcp-auth.postman_collection.json"
REPORT_DIR="newman-reports/mcp-auth"
VK_VALUE="sk-bf-mcp-test-key"
VK_INACTIVE_VALUE="sk-bf-mcp-inactive-key"
MCP_UPSTREAM_DIR="$REPO_ROOT/examples/mcps/http-no-ping-server"
MCP_UPSTREAM_PORT="3001"

GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m'

BIFROST_BINARY=""
PORT="8090"
REPORTERS="cli"
VERBOSE=""
BAIL=""

while [[ $# -gt 0 ]]; do
    case "$1" in
        --binary) BIFROST_BINARY="$2"; shift 2 ;;
        --port) PORT="$2"; shift 2 ;;
        --mcp-port) MCP_UPSTREAM_PORT="$2"; shift 2 ;;
        --html) REPORTERS="${REPORTERS},html"; shift ;;
        --json) REPORTERS="${REPORTERS},json"; shift ;;
        --verbose) VERBOSE="--verbose"; shift ;;
        --bail) BAIL="--bail"; shift ;;
        --help)
            echo "Usage: $0 --binary <path-to-bifrost-http> [OPTIONS]"
            echo ""
            echo "Options:"
            echo "  --binary <path>   Path to a built bifrost-http binary (required)"
            echo "  --port <port>     Port to boot each server on (default: 8090)"
            echo "  --mcp-port <port> Port for the upstream MCP test server (default: 3001)"
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
echo -e "${GREEN}Bifrost MCP Auth Test Runner${NC}"
echo -e "${GREEN}==============================================${NC}"
echo ""

if ! command -v newman &>/dev/null; then
    echo -e "${RED}Error: Newman is not installed${NC}"
    echo "Install it with: npm install -g newman"
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

# Tracks the running servers + temp dirs so the trap can always clean up.
CURRENT_PID=""
CURRENT_DIR=""
MCP_UPSTREAM_PID=""
MCP_UPSTREAM_TMP=""
OVERALL_EXIT=0

cleanup() {
    if [ -n "$CURRENT_PID" ] && kill -0 "$CURRENT_PID" 2>/dev/null; then
        kill "$CURRENT_PID" 2>/dev/null || true
        wait "$CURRENT_PID" 2>/dev/null || true
    fi
    [ -n "$CURRENT_DIR" ] && rm -rf "$CURRENT_DIR"
    [ -n "$MCP_UPSTREAM_TMP" ] && rm -rf "$MCP_UPSTREAM_TMP"
    if [ -n "$MCP_UPSTREAM_PID" ] && kill -0 "$MCP_UPSTREAM_PID" 2>/dev/null; then
        kill "$MCP_UPSTREAM_PID" 2>/dev/null || true
        wait "$MCP_UPSTREAM_PID" 2>/dev/null || true
    fi
}
trap cleanup EXIT

# Build and start the upstream MCP server so /mcp has real tools to expose.
start_upstream_mcp() {
    if [ ! -d "$MCP_UPSTREAM_DIR" ]; then
        echo -e "${RED}Error: upstream MCP server source not found: $MCP_UPSTREAM_DIR${NC}"
        exit 1
    fi
    # Fail fast with a clear message if the chosen port is already taken, rather
    # than letting the server fail to bind and surfacing as a readiness timeout.
    if (command -v nc &>/dev/null && nc -z 127.0.0.1 "$MCP_UPSTREAM_PORT" 2>/dev/null) \
       || (echo >/dev/tcp/127.0.0.1/"$MCP_UPSTREAM_PORT") 2>/dev/null; then
        echo -e "${RED}Error: port $MCP_UPSTREAM_PORT is already in use; pass --mcp-port <port> to use another${NC}"
        exit 1
    fi
    echo -e "${YELLOW}Building upstream MCP server...${NC}"
    # Build into a temp dir (cleaned up on exit) so no binary is left in the
    # example's source tree. GOWORK=off: the example has its own module and is
    # not part of the repo workspace, so building it in-workspace would fail.
    MCP_UPSTREAM_TMP="$(mktemp -d)"
    ( cd "$MCP_UPSTREAM_DIR" && GOWORK=off go build -o "$MCP_UPSTREAM_TMP/http-no-ping-server" . ) || {
        echo -e "${RED}Error: failed to build upstream MCP server${NC}"; exit 1; }
    MCP_SERVER_PORT="$MCP_UPSTREAM_PORT" "$MCP_UPSTREAM_TMP/http-no-ping-server" > "$API_DIR/$REPORT_DIR/upstream-mcp.log" 2>&1 &
    MCP_UPSTREAM_PID=$!
    local waited=0
    while [ $waited -lt 20 ]; do
        if (command -v nc &>/dev/null && nc -z 127.0.0.1 "$MCP_UPSTREAM_PORT" 2>/dev/null) \
           || (echo >/dev/tcp/127.0.0.1/"$MCP_UPSTREAM_PORT") 2>/dev/null; then
            echo -e "${GREEN}Upstream MCP server ready on :$MCP_UPSTREAM_PORT${NC}"
            return
        fi
        if ! kill -0 "$MCP_UPSTREAM_PID" 2>/dev/null; then
            echo -e "${RED}Upstream MCP server exited during startup${NC}"
            cat "$API_DIR/$REPORT_DIR/upstream-mcp.log"
            exit 1
        fi
        sleep 0.5; waited=$((waited + 1))
    done
    echo -e "${RED}Upstream MCP server did not become ready${NC}"; exit 1
}

# write_config <dir> <mode headers|both|oauth>
write_config() {
    local dir="$1" mode="$2"
    local oauth_block=""
    # Discovery + JWT issuance only apply in both/oauth. A stable issuer_url keeps
    # discovery docs and minted-token claims deterministic across the collection.
    if [ "$mode" != "headers" ]; then
        oauth_block="\"oauth2_server_config\": { \"issuer_url\": \"http://localhost:$PORT\", \"auth_code_ttl\": 600, \"access_token_ttl\": 600 },"
    fi
    cat > "$dir/config.json" <<EOF
{
  "\$schema": "https://www.getbifrost.ai/schema",
  "client": {
    "drop_excess_requests": false,
    "initial_pool_size": 50,
    "allowed_origins": ["*"],
    "enable_logging": false,
    "enforce_auth_on_inference": false,
    "mcp_server_auth_mode": "$mode",
    $oauth_block
    "max_request_body_size_mb": 100
  },
  "config_store": { "enabled": true, "type": "sqlite", "config": { "path": "$dir/config.db" } },
  "logs_store": { "enabled": false },
  "providers": {
    "openai": {
      "keys": [{ "name": "mcp-dummy", "value": "sk-mcp-dummy", "weight": 1 }],
      "network_config": { "base_url": "http://127.0.0.1:1", "default_request_timeout_in_seconds": 5 }
    }
  },
  "governance": {
    "virtual_keys": [
      {
        "name": "MCP Test VK", "id": "vk-mcp", "value": "$VK_VALUE", "is_active": true,
        "provider_configs": [{ "provider": "openai", "allowed_models": ["*"], "key_ids": ["*"], "weight": 1.0 }]
      },
      {
        "name": "MCP Inactive VK", "id": "vk-mcp-inactive", "value": "$VK_INACTIVE_VALUE", "is_active": false,
        "provider_configs": [{ "provider": "openai", "allowed_models": ["*"], "key_ids": ["*"], "weight": 1.0 }]
      }
    ]
  },
  "mcp": {
    "client_configs": [
      {
        "client_id": "test-mcp",
        "name": "Test MCP",
        "connection_type": "http",
        "connection_string": "http://localhost:$MCP_UPSTREAM_PORT/",
        "auth_type": "none",
        "is_ping_available": false
      }
    ]
  }
}
EOF
}

# run_mode <mode headers|both|oauth>
run_mode() {
    local mode="$1"
    echo -e "${GREEN}----------------------------------------------${NC}"
    echo -e "${GREEN}MCP server auth mode: ${YELLOW}${mode}${NC}"
    echo -e "${GREEN}----------------------------------------------${NC}"

    CURRENT_DIR="$(mktemp -d)"
    write_config "$CURRENT_DIR" "$mode"
    local server_log="$CURRENT_DIR/server.log"

    "$BIFROST_BINARY" --app-dir "$CURRENT_DIR" --port "$PORT" --log-level info > "$server_log" 2>&1 &
    CURRENT_PID=$!

    local elapsed=0
    while [ $elapsed -lt 60 ]; do
        grep -q "successfully started bifrost" "$server_log" 2>/dev/null && break
        if ! kill -0 "$CURRENT_PID" 2>/dev/null; then
            echo -e "${RED}   Server exited before becoming ready${NC}"; cat "$server_log"
            OVERALL_EXIT=1; CURRENT_PID=""; rm -rf "$CURRENT_DIR"; CURRENT_DIR=""; return
        fi
        sleep 1; elapsed=$((elapsed + 1))
    done
    if [ $elapsed -ge 60 ]; then
        echo -e "${RED}   Server did not start within 60s${NC}"; cat "$server_log"; OVERALL_EXIT=1
    else
        local report_prefix="${REPORT_DIR}/${mode}"
        local cmd=(newman run "$COLLECTION"
            --env-var "base_url=http://localhost:$PORT"
            --env-var "auth_mode=$mode"
            --env-var "vk_value=$VK_VALUE"
            --env-var "vk_inactive_value=$VK_INACTIVE_VALUE"
            --env-var "mcp_issuer=http://localhost:$PORT"
            --timeout-script 60000 --timeout 120000
            -r "$REPORTERS")
        [[ "$REPORTERS" == *"html"* ]] && cmd+=(--reporter-html-export "${report_prefix}.html")
        [[ "$REPORTERS" == *"json"* ]] && cmd+=(--reporter-json-export "${report_prefix}.json")
        [ -n "$VERBOSE" ] && cmd+=("$VERBOSE")
        [ -n "$BAIL" ] && cmd+=("$BAIL")

        set +e
        "${cmd[@]}"
        local code=$?
        set -e
        [ $code -ne 0 ] && OVERALL_EXIT=1
    fi

    if [ -n "$CURRENT_PID" ] && kill -0 "$CURRENT_PID" 2>/dev/null; then
        kill "$CURRENT_PID" 2>/dev/null || true
        wait "$CURRENT_PID" 2>/dev/null || true
    fi
    CURRENT_PID=""
    rm -rf "$CURRENT_DIR"; CURRENT_DIR=""
    echo ""
}

start_upstream_mcp
run_mode "headers"
run_mode "both"
run_mode "oauth"

echo ""
if [ $OVERALL_EXIT -eq 0 ]; then
    echo -e "${GREEN}✓ All MCP auth modes passed!${NC}"
else
    echo -e "${RED}✗ Some MCP auth mode checks failed${NC}"
fi
if [[ "$REPORTERS" == *"html"* ]] || [[ "$REPORTERS" == *"json"* ]]; then
    echo ""
    echo -e "Reports saved to: ${YELLOW}$REPORT_DIR${NC}"
    ls -lh "$REPORT_DIR" 2>/dev/null | tail -n +2
fi

exit $OVERALL_EXIT
