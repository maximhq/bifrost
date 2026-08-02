#!/bin/bash

# Bifrost Auth Matrix Newman Test Runner
#
# Boots a fresh Bifrost server (sqlite, pre-seeded virtual key, unreachable dummy
# provider) once per combination of:
#   - client.enforce_auth_on_inference        (enforce VK on inference)
#   - governance.auth_config.is_enabled        (admin password on /api/*)
# and runs collections/bifrost-v1-auth-matrix.postman_collection.json against each.
#
# Guards the separation: inference auth is owned by governance (virtual key),
# admin password guards only /api/* — a VK-authenticated inference request must
# never be rejected by the admin middleware, in any combination.
#
# Requires a built bifrost-http binary; this runner boots its own servers (it does
# not reuse the shared e2e server, since each combination needs a different boot
# config).

set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
API_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
cd "$API_DIR"

COLLECTION="collections/bifrost-v1-auth-matrix.postman_collection.json"
REPORT_DIR="newman-reports/auth-matrix"
ADMIN_USER="admin"
ADMIN_PASS="Matrix-Admin-Pass1!"
VK_VALUE="sk-bf-matrix-test-key"

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
        --binary)
            BIFROST_BINARY="$2"; shift 2 ;;
        --port)
            PORT="$2"; shift 2 ;;
        --html)
            REPORTERS="${REPORTERS},html"; shift ;;
        --json)
            REPORTERS="${REPORTERS},json"; shift ;;
        --verbose)
            VERBOSE="--verbose"; shift ;;
        --bail)
            BAIL="--bail"; shift ;;
        --help)
            echo "Usage: $0 --binary <path-to-bifrost-http> [OPTIONS]"
            echo ""
            echo "Options:"
            echo "  --binary <path>   Path to a built bifrost-http binary (required)"
            echo "  --port <port>     Port to boot each server on (default: 8090)"
            echo "  --html            Generate HTML report"
            echo "  --json            Generate JSON report"
            echo "  --verbose         Show detailed Newman output"
            echo "  --bail            Stop on first failure"
            echo "  --help            Show this help message"
            exit 0 ;;
        *)
            echo -e "${RED}Unknown option: $1${NC}"; exit 1 ;;
    esac
done

echo -e "${GREEN}==============================================${NC}"
echo -e "${GREEN}Bifrost Auth Matrix Test Runner${NC}"
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

ADMIN_HEADER="Basic $(printf '%s:%s' "$ADMIN_USER" "$ADMIN_PASS" | base64 | tr -d '\n')"

# Tracks the running server + its temp dir so the trap can always clean up.
CURRENT_PID=""
CURRENT_DIR=""
OVERALL_EXIT=0

cleanup() {
    if [ -n "$CURRENT_PID" ] && kill -0 "$CURRENT_PID" 2>/dev/null; then
        kill "$CURRENT_PID" 2>/dev/null || true
        wait "$CURRENT_PID" 2>/dev/null || true
    fi
    [ -n "$CURRENT_DIR" ] && rm -rf "$CURRENT_DIR"
}
trap cleanup EXIT

# write_config <dir> <enforce_bool true|false> <admin 1|"">
write_config() {
    local dir="$1" enforce="$2" admin="$3"
    local auth_block=""
    if [ "$admin" = "1" ]; then
        auth_block="\"auth_config\": { \"is_enabled\": true, \"admin_username\": \"$ADMIN_USER\", \"admin_password\": \"$ADMIN_PASS\" },"
    fi
    cat > "$dir/config.json" <<EOF
{
  "\$schema": "https://www.getbifrost.ai/schema",
  "client": {
    "drop_excess_requests": false,
    "initial_pool_size": 50,
    "allowed_origins": ["*"],
    "enable_logging": false,
    "enforce_auth_on_inference": $enforce,
    "max_request_body_size_mb": 100
  },
  "config_store": { "enabled": true, "type": "sqlite", "config": { "path": "$dir/config.db" } },
  "logs_store": { "enabled": false },
  "providers": {
    "openai": {
      "keys": [{ "name": "matrix-dummy", "value": "sk-matrix-dummy", "weight": 1 }],
      "network_config": { "base_url": "http://127.0.0.1:1", "default_request_timeout_in_seconds": 5 }
    }
  },
  "governance": {
    $auth_block
    "virtual_keys": [{
      "name": "Matrix VK",
      "id": "vk-matrix",
      "value": "$VK_VALUE",
      "is_active": true,
      "provider_configs": [{ "provider": "openai", "allowed_models": ["*"], "key_ids": ["*"], "weight": 1.0 }]
    }]
  }
}
EOF
}

# run_combo <label> <enforce true|false> <admin 1|"">
run_combo() {
    local label="$1" enforce="$2" admin="$3"
    local enforce_flag="" admin_flag="" admin_header=""
    [ "$enforce" = "true" ] && enforce_flag="1"
    [ "$admin" = "1" ] && { admin_flag="1"; admin_header="$ADMIN_HEADER"; }

    echo -e "${GREEN}----------------------------------------------${NC}"
    echo -e "${GREEN}Combination: ${label}${NC}"
    echo -e "  enforce_auth_on_inference: ${YELLOW}${enforce}${NC}   admin password: ${YELLOW}$([ "$admin" = "1" ] && echo on || echo off)${NC}"
    echo -e "${GREEN}----------------------------------------------${NC}"

    CURRENT_DIR="$(mktemp -d)"
    write_config "$CURRENT_DIR" "$enforce" "$admin"
    local server_log="$CURRENT_DIR/server.log"

    "$BIFROST_BINARY" --app-dir "$CURRENT_DIR" --port "$PORT" --log-level info > "$server_log" 2>&1 &
    CURRENT_PID=$!

    local elapsed=0
    while [ $elapsed -lt 60 ]; do
        if grep -q "successfully started bifrost" "$server_log" 2>/dev/null; then
            break
        fi
        if ! kill -0 "$CURRENT_PID" 2>/dev/null; then
            echo -e "${RED}   Server exited before becoming ready${NC}"
            cat "$server_log"
            OVERALL_EXIT=1
            CURRENT_PID=""
            rm -rf "$CURRENT_DIR"; CURRENT_DIR=""
            return
        fi
        sleep 1
        elapsed=$((elapsed + 1))
    done
    if [ $elapsed -ge 60 ]; then
        echo -e "${RED}   Server did not start within 60s${NC}"
        cat "$server_log"
        OVERALL_EXIT=1
    else
        local report_prefix="${REPORT_DIR}/${label}"
        local cmd=(newman run "$COLLECTION"
            --env-var "base_url=http://localhost:$PORT"
            --env-var "enforce_auth=$enforce_flag"
            --env-var "admin_auth=$admin_flag"
            --env-var "admin_auth_header=$admin_header"
            --env-var "vk_value=$VK_VALUE"
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

    # Tear down this combination's server before the next boot.
    if [ -n "$CURRENT_PID" ] && kill -0 "$CURRENT_PID" 2>/dev/null; then
        kill "$CURRENT_PID" 2>/dev/null || true
        wait "$CURRENT_PID" 2>/dev/null || true
    fi
    CURRENT_PID=""
    rm -rf "$CURRENT_DIR"; CURRENT_DIR=""
    echo ""
}

run_combo "enforce-off_admin-off" false ""
run_combo "enforce-on_admin-off"  true  ""
run_combo "enforce-off_admin-on"  false 1
run_combo "enforce-on_admin-on"   true  1

echo ""
if [ $OVERALL_EXIT -eq 0 ]; then
    echo -e "${GREEN}✓ All auth matrix combinations passed!${NC}"
else
    echo -e "${RED}✗ Some auth matrix combinations failed${NC}"
fi
if [[ "$REPORTERS" == *"html"* ]] || [[ "$REPORTERS" == *"json"* ]]; then
    echo ""
    echo -e "Reports saved to: ${YELLOW}$REPORT_DIR${NC}"
    ls -lh "$REPORT_DIR" 2>/dev/null | tail -n +2
fi

exit $OVERALL_EXIT
