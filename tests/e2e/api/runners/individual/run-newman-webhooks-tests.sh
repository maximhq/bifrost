#!/bin/bash

# Bifrost V1 Async Webhooks Newman Test Runner
#
# Runs collections/bifrost-v1-async-webhooks.postman_collection.json against a
# running Bifrost server. Requires LogsStore and the governance plugin (async
# inference), plus at least one working provider key for the chosen --env.
#
# A local capture receiver (tests/e2e/api/webhook-receiver) is built and
# started by this runner; Bifrost delivers webhooks to it and the collection
# asserts on what it captured (headers, signatures, payload shapes).

set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
API_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
cd "$API_DIR"

COLLECTION="collections/bifrost-v1-async-webhooks.postman_collection.json"
REPORT_DIR="newman-reports/webhooks"
PROVIDER_CONFIG_DIR="provider_config"
RECEIVER_DIR="$API_DIR/webhook-receiver"
RECEIVER_PORT="${WEBHOOK_RECEIVER_PORT:-3005}"

GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

PROVIDER_ENV_FILE=""
VERBOSE=""
REPORTERS="cli"
BAIL=""
while [[ $# -gt 0 ]]; do
    case "$1" in
        --env)
            if [[ -z "${2:-}" || "${2:-}" == --* ]]; then
                echo -e "${RED}Error: --env requires a value${NC}"
                exit 1
            fi
            PROVIDER_ENV_FILE="$2"
            shift 2
            ;;
        --receiver-port) RECEIVER_PORT="$2"; shift 2 ;;
        --verbose) VERBOSE="--verbose"; shift ;;
        --html) REPORTERS="${REPORTERS},html"; shift ;;
        --json) REPORTERS="${REPORTERS},json"; shift ;;
        --bail) BAIL="--bail"; shift ;;
        --help)
            echo "Usage: $0 [OPTIONS]"
            echo ""
            echo "Options:"
            echo "  --env <provider>       Postman env path or provider name"
            echo "  --receiver-port <p>    Capture receiver port (default: 3005)"
            echo "  --verbose              Show detailed output"
            echo "  --html                 Generate HTML report"
            echo "  --json                 Generate JSON report"
            echo "  --bail                 Stop on first failure"
            echo "  --help                 Show this help message"
            echo ""
            echo "Prerequisites: LogsStore and governance plugin must be configured."
            echo "Environment Variables:"
            echo "  BIFROST_BASE_URL       Override base URL (default: http://localhost:8080)"
            echo "  WEBHOOK_RECEIVER_PORT  Override receiver port (default: 3005)"
            exit 0
            ;;
        *) echo -e "${RED}Unknown option: $1${NC}"; exit 1 ;;
    esac
done

echo -e "${GREEN}==============================================${NC}"
echo -e "${GREEN}Bifrost V1 Async Webhooks Test Runner${NC}"
echo -e "${GREEN}==============================================${NC}"
echo ""

if ! command -v newman &> /dev/null; then
    echo -e "${RED}Error: Newman is not installed${NC}"
    echo "Install it with: npm install -g newman"
    exit 1
fi

if ! command -v curl &> /dev/null; then
    echo -e "${RED}Error: curl is required for the receiver health check${NC}"
    exit 1
fi

if [ ! -f "$COLLECTION" ]; then
    echo -e "${RED}Error: Collection file not found: $COLLECTION${NC}"
    exit 1
fi

mkdir -p "$REPORT_DIR"

# --- capture receiver sidecar ---------------------------------------------

RECEIVER_PID=""
GLOBALS_TMP=""
RECEIVER_BUILD_DIR=""

cleanup() {
    if [ -n "$RECEIVER_PID" ] && kill -0 "$RECEIVER_PID" 2>/dev/null; then
        kill "$RECEIVER_PID" 2>/dev/null || true
        wait "$RECEIVER_PID" 2>/dev/null || true
    fi
    [ -n "$GLOBALS_TMP" ] && rm -f "$GLOBALS_TMP"
    # Only set when this run built its own receiver; a reused receiver leaves it
    # empty so we never remove someone else's build.
    [ -n "$RECEIVER_BUILD_DIR" ] && rm -rf "$RECEIVER_BUILD_DIR"
}
trap cleanup EXIT

port_in_use() {
    (exec 3<>"/dev/tcp/127.0.0.1/$1") 2>/dev/null && { exec 3>&-; return 0; } || return 1
}

receiver_healthy() {
    # Confirm the process on the port is actually our receiver — it answers
    # /healthz — rather than an unrelated listener we'd otherwise send capture
    # and mode traffic to.
    curl -sf -o /dev/null "http://127.0.0.1:$1/healthz"
}

start_receiver() {
    if port_in_use "$RECEIVER_PORT"; then
        if receiver_healthy "$RECEIVER_PORT"; then
            echo -e "${YELLOW}Port $RECEIVER_PORT already serving a healthy receiver; reusing it${NC}"
            return 0
        fi
        echo -e "${RED}Error: port $RECEIVER_PORT is in use but not by a webhook receiver (no /healthz response).${NC}"
        echo -e "${RED}Refusing to send test traffic to an unrelated process. Free the port or pass --receiver-port.${NC}"
        return 1
    fi
    RECEIVER_BUILD_DIR=$(mktemp -d)
    echo -e "Building webhook receiver..."
    (cd "$RECEIVER_DIR" && GOWORK=off go build -o "$RECEIVER_BUILD_DIR/webhook-receiver" .) || {
        echo -e "${RED}Error: failed to build webhook receiver${NC}"
        return 1
    }
    WEBHOOK_RECEIVER_PORT="$RECEIVER_PORT" "$RECEIVER_BUILD_DIR/webhook-receiver" > "$REPORT_DIR/webhook-receiver.log" 2>&1 &
    RECEIVER_PID=$!
    for _ in $(seq 1 20); do
        if receiver_healthy "$RECEIVER_PORT"; then
            echo -e "${GREEN}Webhook receiver ready on port $RECEIVER_PORT (pid $RECEIVER_PID)${NC}"
            return 0
        fi
        sleep 0.5
    done
    echo -e "${RED}Error: webhook receiver did not become ready${NC}"
    return 1
}

start_receiver

# --- provider environment ---------------------------------------------------

SINGLE_JSON_ENV=""
if [ -n "$PROVIDER_ENV_FILE" ]; then
    if [ -f "$PROVIDER_ENV_FILE" ]; then
        SINGLE_JSON_ENV="$PROVIDER_ENV_FILE"
    elif [ -f "$PROVIDER_CONFIG_DIR/$PROVIDER_ENV_FILE" ]; then
        SINGLE_JSON_ENV="$PROVIDER_CONFIG_DIR/$PROVIDER_ENV_FILE"
    elif [ -f "$PROVIDER_CONFIG_DIR/bifrost-v1-${PROVIDER_ENV_FILE}.postman_environment.json" ]; then
        SINGLE_JSON_ENV="$PROVIDER_CONFIG_DIR/bifrost-v1-${PROVIDER_ENV_FILE}.postman_environment.json"
    else
        echo -e "${RED}Error: Could not find environment file for: $PROVIDER_ENV_FILE${NC}"
        exit 1
    fi
fi

if [ -z "$SINGLE_JSON_ENV" ]; then
    if [ -f "$PROVIDER_CONFIG_DIR/bifrost-v1-openai.postman_environment.json" ]; then
        SINGLE_JSON_ENV="$PROVIDER_CONFIG_DIR/bifrost-v1-openai.postman_environment.json"
        echo -e "${YELLOW}No --env specified, using openai${NC}"
    fi
fi

# --- run ---------------------------------------------------------------------

cmd=(newman run "$COLLECTION")
[ -n "$SINGLE_JSON_ENV" ] && [ -f "$SINGLE_JSON_ENV" ] && cmd+=(-e "$SINGLE_JSON_ENV")
base_url="${BIFROST_BASE_URL:-http://localhost:8080}"
cmd+=(--env-var "base_url=$base_url")
cmd+=(--env-var "receiver_url=http://localhost:$RECEIVER_PORT")
cmd+=(--timeout-script 120000 --timeout 900000)
cmd+=(-r "$REPORTERS")
[[ "$REPORTERS" == *"html"* ]] && cmd+=(--reporter-html-export "$REPORT_DIR/report.html")
[[ "$REPORTERS" == *"json"* ]] && cmd+=(--reporter-json-export "$REPORT_DIR/report.json")
[ -n "$VERBOSE" ] && cmd+=("$VERBOSE")
[ -n "$BAIL" ] && cmd+=("$BAIL")

echo -e "Configuration:"
echo -e "  Collection: ${YELLOW}$COLLECTION${NC}"
echo -e "  Base URL:   ${YELLOW}$base_url${NC}"
echo -e "  Receiver:   ${YELLOW}http://localhost:$RECEIVER_PORT${NC}"
if [ -n "$SINGLE_JSON_ENV" ]; then
    echo -e "  Env:        ${YELLOW}$SINGLE_JSON_ENV${NC}"
fi
echo -e "  Reports:    ${YELLOW}$REPORT_DIR${NC}"
echo ""

"${cmd[@]}"
