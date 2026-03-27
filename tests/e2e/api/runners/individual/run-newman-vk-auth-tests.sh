#!/bin/bash

# Bifrost V1 Virtual Key Auth Newman Test Runner
# Runs VK auth tests: creates VK, runs inference with/without VK, tests rejection cases, cleans up.

set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
API_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
cd "$API_DIR"

# Configuration
COLLECTION="collections/bifrost-v1-vk-auth.postman_collection.json"
REPORT_DIR="newman-reports/vk-auth"
PROVIDER_CONFIG_DIR="provider_config"
PROVIDER_CAPABILITIES_JSON="provider-capabilities.json"

# Colors for output
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

# Parse arguments
PROVIDER_ENV_FILE=""
ENFORCE_AUTH=""
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
        --enforce-auth)
            ENFORCE_AUTH="1"
            shift
            ;;
        --help)
            echo "Usage: $0 [OPTIONS]"
            echo ""
            echo "Options:"
            echo "  --env <provider>    Postman env path or provider name (e.g. openai or provider_config/bifrost-v1-openai.postman_environment.json)"
            echo "  --enforce-auth       Enable auth enforcement mode (without-VK requests expect 401)"
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
            echo "  $0 --env openai              # Run with OpenAI provider"
            echo "  $0 --env openai --enforce-auth  # Run with auth enforcement"
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
echo -e "${GREEN}Bifrost V1 Virtual Key Auth Test Runner${NC}"
echo -e "${GREEN}==============================================${NC}"
echo ""

# Check if Newman is installed
if ! command -v newman &> /dev/null; then
    echo -e "${RED}Error: Newman is not installed${NC}"
    echo "Install it with: npm install -g newman"
    exit 1
fi

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
    trap 'rm -f "$GLOBALS_TMP"' EXIT
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
cmd=(newman run "$COLLECTION")
[ -n "$GLOBALS_TMP" ] && [ -f "$GLOBALS_TMP" ] && cmd+=(-g "$GLOBALS_TMP")
[ -n "$SINGLE_JSON_ENV" ] && [ -f "$SINGLE_JSON_ENV" ] && cmd+=(-e "$SINGLE_JSON_ENV")

# Pass enforce_auth when --enforce-auth was set
if [ -n "$ENFORCE_AUTH" ]; then
    cmd+=(--env-var "enforce_auth=1")
    echo -e "Mode: ${YELLOW}enforce_auth=1${NC} (without-VK requests expect 401)"
fi

# Base URL override
base_url="${BIFROST_BASE_URL:-http://localhost:8080}"
cmd+=(--env-var "base_url=$base_url")

cmd+=(--timeout-script 120000 --timeout 900000)
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
    echo -e "${GREEN}✓ All VK auth tests passed!${NC}"
else
    echo -e "${RED}✗ Some tests failed${NC}"
fi
if [[ "$REPORTERS" == *"html"* ]] || [[ "$REPORTERS" == *"json"* ]]; then
    echo ""
    echo -e "Reports saved to: ${YELLOW}$REPORT_DIR${NC}"
    ls -lh "$REPORT_DIR" 2>/dev/null | tail -n +2
fi

exit $EXIT_CODE
