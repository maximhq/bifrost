#!/bin/bash

# Bifrost V1 Extra Fields Placement Newman Test Runner
# Runs client.extra_fields_placement tests: in body mode extra_fields stays in JSON
# bodies and SSE chunks; in headers mode it leaves every body, chunk and error body
# and rides on x-bifrost-* headers, across /v1/chat/completions, /v1/responses and
# the /openai integration, unary and streaming. Makes about ten small live
# completions against the configured provider; restores the client config at the end.

set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
API_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
cd "$API_DIR"

# Configuration
COLLECTION="collections/bifrost-v1-extra-fields-placement.postman_collection.json"
REPORT_DIR="newman-reports/extra-fields-placement"
PROVIDER_CONFIG_DIR="provider_config"
PROVIDER_CAPABILITIES_JSON="provider-capabilities.json"

# Colors for output
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

# Parse arguments
PROVIDER_ENV_FILE=""
ARGS=()
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
        --help)
            echo "Usage: $0 [OPTIONS]"
            echo ""
            echo "Options:"
            echo "  --env <provider>    Postman env path or provider name (e.g. openai or provider_config/bifrost-v1-openai.postman_environment.json)"
            echo "  --verbose           Show detailed output"
            echo "  --html              Generate HTML report"
            echo "  --json              Generate JSON report"
            echo "  --bail              Stop on first failure"
            echo "  --help              Show this help message"
            echo ""
            echo "Environment Variables:"
            echo "  BIFROST_BASE_URL    Override base URL (default: http://localhost:8080)"
            echo ""
            echo "Examples:"
            echo "  $0 --env openai     # Run with OpenAI provider"
            exit 0
            ;;
        *)
            ARGS+=("$1")
            shift
            ;;
    esac
done
set -- "${ARGS[@]}"

# Print banner
echo -e "${GREEN}==============================================${NC}"
echo -e "${GREEN}Bifrost V1 Extra Fields Placement Test Runner${NC}"
echo -e "${GREEN}==============================================${NC}"
echo ""

# Check if Newman is installed
if ! command -v newman &> /dev/null; then
    echo -e "${RED}Error: Newman is not installed${NC}"
    echo "Install it with: npm install -g newman"
    exit 1
fi

# The cleanup trap below needs curl and jq to put extra_fields_placement back when newman
# stops early; without them a stopped run would leave the shared gateway in headers mode.
for tool in curl jq; do
    if ! command -v "$tool" &> /dev/null; then
        echo -e "${RED}Error: $tool is required to restore extra_fields_placement if the run stops early${NC}"
        exit 1
    fi
done

# Check if collection exists
if [ ! -f "$COLLECTION" ]; then
    echo -e "${RED}Error: Collection file not found: $COLLECTION${NC}"
    exit 1
fi

# Create report directory
mkdir -p "$REPORT_DIR"

# Load provider capabilities into globals (for consistency with v1 runner)
GLOBALS_TMP=""
if [ -f "$PROVIDER_CAPABILITIES_JSON" ] && command -v jq &>/dev/null; then
    GLOBALS_TMP=$(mktemp)
    jq -n --rawfile cap "$PROVIDER_CAPABILITIES_JSON" '{id: "bifrost-provider-capabilities", name: "Provider capabilities", values: [{key: "provider_capabilities", value: $cap, type: "default", enabled: true}]}' > "$GLOBALS_TMP"
fi

# Parse remaining options
VERBOSE=""
REPORTERS="cli"
BAIL=""
while [[ $# -gt 0 ]]; do
    case $1 in
        --verbose)
            VERBOSE="--verbose"
            shift
            ;;
        --html)
            REPORTERS="${REPORTERS},html"
            shift
            ;;
        --json)
            REPORTERS="${REPORTERS},json"
            shift
            ;;
        --bail)
            BAIL="--bail"
            shift
            ;;
        *)
            echo -e "${RED}Unknown option: $1${NC}"
            exit 1
            ;;
    esac
done

# Resolve provider env file
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
        echo "Searched:"
        echo "  - $PROVIDER_ENV_FILE"
        echo "  - $PROVIDER_CONFIG_DIR/$PROVIDER_ENV_FILE"
        echo "  - $PROVIDER_CONFIG_DIR/bifrost-v1-${PROVIDER_ENV_FILE}.postman_environment.json"
        exit 1
    fi
fi

# Default to openai if no env specified
if [ -z "$SINGLE_JSON_ENV" ]; then
    if [ -f "$PROVIDER_CONFIG_DIR/bifrost-v1-openai.postman_environment.json" ]; then
        SINGLE_JSON_ENV="$PROVIDER_CONFIG_DIR/bifrost-v1-openai.postman_environment.json"
        echo -e "${YELLOW}No --env specified, using openai${NC}"
    fi
fi

# Build Newman command
# OSS locks /api behind the setup token while dashboard auth is not active, and
# this suite reads and writes client config through that API.
SETUP_TOKEN="${BIFROST_E2E_SETUP_TOKEN:-${BIFROST_SETUP_TOKEN:-bifrost-e2e-setup-token}}"
SETUP_COLLECTION="$REPORT_DIR/$(basename "$COLLECTION" .postman_collection.json)-setup-token.postman_collection.json"
node "$SCRIPT_DIR/../add-setup-token-header.mjs" "$COLLECTION" "$SETUP_COLLECTION"

cmd=(newman run "$SETUP_COLLECTION")
[ -n "$GLOBALS_TMP" ] && [ -f "$GLOBALS_TMP" ] && cmd+=(-g "$GLOBALS_TMP")
[ -n "$SINGLE_JSON_ENV" ] && [ -f "$SINGLE_JSON_ENV" ] && cmd+=(-e "$SINGLE_JSON_ENV")

# Base URL override
base_url="${BIFROST_BASE_URL:-http://localhost:8080}"
cmd+=(--env-var "base_url=$base_url")

# The collection's Teardown restores extra_fields_placement to body, but newman skips it when
# it stops early (--bail, Ctrl-C). This trap repeats that restore on every exit, so a stopped
# run never leaves the shared gateway in headers mode. It reads the stored config, changes only
# extra_fields_placement (jq keeps the key order) and sends the whole config back, because
# PUT /api/config resets omitted fields. A fresh install's log_retention_days 0 is raised to
# 30, which the PUT requires.
restore_placement() {
    local cfg
    cfg=$(curl -sf -H "X-Bifrost-Setup-Token: $SETUP_TOKEN" "$base_url/api/config?from_db=true") || return 0
    [ "$(jq -r '.client_config.extra_fields_placement // "body"' <<<"$cfg")" = "body" ] && return 0
    echo -e "${YELLOW}Restoring extra_fields_placement to body (newman stopped before Teardown)${NC}"
    jq '{client_config: (.client_config | .extra_fields_placement = "body" | if (.log_retention_days // 0) < 1 then .log_retention_days = 30 else . end)}' <<<"$cfg" |
        curl -sf -X PUT -H "X-Bifrost-Setup-Token: $SETUP_TOKEN" -H "Content-Type: application/json" --data-binary @- "$base_url/api/config" >/dev/null ||
        echo -e "${RED}Could not restore extra_fields_placement; set it back to body by hand${NC}"
}
cleanup() {
    restore_placement
    [ -n "$GLOBALS_TMP" ] && rm -f "$GLOBALS_TMP"
}
trap cleanup EXIT
trap 'exit 130' INT TERM
cmd+=(--env-var "setup_token=$SETUP_TOKEN")

cmd+=(-r "$REPORTERS")

if [[ "$REPORTERS" == *"html"* ]]; then
    cmd+=(--reporter-html-export "$REPORT_DIR/report.html")
fi
if [[ "$REPORTERS" == *"json"* ]]; then
    cmd+=(--reporter-json-export "$REPORT_DIR/report.json")
fi
[ -n "$VERBOSE" ] && cmd+=("$VERBOSE")
[ -n "$BAIL" ] && cmd+=("$BAIL")

echo -e "Configuration:"
echo -e "  Collection: ${YELLOW}$COLLECTION${NC}"
echo -e "  Base URL:   ${YELLOW}$base_url${NC}"
if [ -n "$SINGLE_JSON_ENV" ]; then
    echo -e "  Env:        ${YELLOW}$SINGLE_JSON_ENV${NC}"
fi
echo -e "  Reports:    ${YELLOW}$REPORT_DIR${NC}"
echo ""
echo -e "${GREEN}Running tests...${NC}"
echo ""

set +e
"${cmd[@]}"
EXIT_CODE=$?
set -e

echo ""
if [ $EXIT_CODE -eq 0 ]; then
    echo -e "${GREEN}✓ All extra-fields-placement tests passed!${NC}"
else
    echo -e "${RED}✗ Some tests failed${NC}"
fi
if [[ "$REPORTERS" == *"html"* ]] || [[ "$REPORTERS" == *"json"* ]]; then
    echo ""
    echo -e "Reports saved to: ${YELLOW}$REPORT_DIR${NC}"
    ls -lh "$REPORT_DIR" 2>/dev/null | tail -n +2
fi

exit $EXIT_CODE
