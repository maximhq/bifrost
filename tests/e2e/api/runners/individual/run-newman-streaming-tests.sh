#!/bin/bash

# Bifrost V1 Streaming Newman Test Runner
# Runs streaming SSE tests for chat completions and responses.

set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
API_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
cd "$API_DIR"

COLLECTION="collections/bifrost-v1-streaming.postman_collection.json"
REPORT_DIR="newman-reports/streaming"
PROVIDER_CONFIG_DIR="provider_config"
PROVIDER_CAPABILITIES_JSON="provider-capabilities.json"

GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

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
            echo "  --env <provider>    Postman env path or provider name"
            echo "  --verbose           Show detailed output"
            echo "  --html              Generate HTML report"
            echo "  --json              Generate JSON report"
            echo "  --bail              Stop on first failure"
            echo "  --help              Show this help message"
            echo ""
            echo "Environment Variables:"
            echo "  BIFROST_BASE_URL    Override base URL (default: http://localhost:8080)"
            exit 0
            ;;
        *)
            ARGS+=("$1")
            shift
            ;;
    esac
done
set -- "${ARGS[@]}"

echo -e "${GREEN}==============================================${NC}"
echo -e "${GREEN}Bifrost V1 Streaming Test Runner${NC}"
echo -e "${GREEN}==============================================${NC}"
echo ""

if ! command -v newman &> /dev/null; then
    echo -e "${RED}Error: Newman is not installed${NC}"
    echo "Install it with: npm install -g newman"
    exit 1
fi

if [ ! -f "$COLLECTION" ]; then
    echo -e "${RED}Error: Collection file not found: $COLLECTION${NC}"
    exit 1
fi

mkdir -p "$REPORT_DIR"

GLOBALS_TMP=""
if [ -f "$PROVIDER_CAPABILITIES_JSON" ] && command -v jq &>/dev/null; then
    GLOBALS_TMP=$(mktemp)
    trap 'rm -f "$GLOBALS_TMP"' EXIT
    jq -n --rawfile cap "$PROVIDER_CAPABILITIES_JSON" '{id: "bifrost-provider-capabilities", name: "Provider capabilities", values: [{key: "provider_capabilities", value: $cap, type: "default", enabled: true}]}' > "$GLOBALS_TMP"
fi

VERBOSE=""
REPORTERS="cli"
BAIL=""
while [[ $# -gt 0 ]]; do
    case $1 in
        --verbose) VERBOSE="--verbose"; shift ;;
        --html) REPORTERS="cli,html"; shift ;;
        --json) REPORTERS="cli,json"; shift ;;
        --bail) BAIL="--bail"; shift ;;
        *) echo -e "${RED}Unknown option: $1${NC}"; exit 1 ;;
    esac
done

SINGLE_JSON_ENV=""
if [ -n "$PROVIDER_ENV_FILE" ]; then
    if [ -f "$PROVIDER_ENV_FILE" ]; then
        SINGLE_JSON_ENV="$PROVIDER_ENV_FILE"
    elif [ -f "$PROVIDER_CONFIG_DIR/$PROVIDER_ENV_FILE" ]; then
        SINGLE_JSON_ENV="$PROVIDER_CONFIG_DIR/$PROVIDER_ENV_FILE"
    elif [ -f "$PROVIDER_CONFIG_DIR/bifrost-v1-${PROVIDER_ENV_FILE}.postman_environment.json" ]; then
        SINGLE_JSON_ENV="$PROVIDER_CONFIG_DIR/bifrost-v1-${PROVIDER_ENV_FILE}.postman_environment.json"
    fi
fi

if [ -z "$SINGLE_JSON_ENV" ]; then
    if [ -f "$PROVIDER_CONFIG_DIR/bifrost-v1-openai.postman_environment.json" ]; then
        SINGLE_JSON_ENV="$PROVIDER_CONFIG_DIR/bifrost-v1-openai.postman_environment.json"
        echo -e "${YELLOW}No --env specified, using openai${NC}"
    fi
fi

cmd=(newman run "$COLLECTION")
[ -n "$GLOBALS_TMP" ] && [ -f "$GLOBALS_TMP" ] && cmd+=(-g "$GLOBALS_TMP")
[ -n "$SINGLE_JSON_ENV" ] && [ -f "$SINGLE_JSON_ENV" ] && cmd+=(-e "$SINGLE_JSON_ENV")
base_url="${BIFROST_BASE_URL:-http://localhost:8080}"
cmd+=(--env-var "base_url=$base_url")
cmd+=(--timeout-script 120000 --timeout 900000)
cmd+=(-r "$REPORTERS")
[[ "$REPORTERS" == *"html"* ]] && cmd+=(--reporter-html-export "$REPORT_DIR/report.html")
[[ "$REPORTERS" == *"json"* ]] && cmd+=(--reporter-json-export "$REPORT_DIR/report.json")
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
    echo -e "${GREEN}✓ All streaming tests passed!${NC}"
else
    echo -e "${RED}✗ Some tests failed${NC}"
fi
if [[ "$REPORTERS" == *"html"* ]] || [[ "$REPORTERS" == *"json"* ]]; then
    echo ""
    echo -e "Reports saved to: ${YELLOW}$REPORT_DIR${NC}"
    ls -lh "$REPORT_DIR" 2>/dev/null | tail -n +2
fi

exit $EXIT_CODE
