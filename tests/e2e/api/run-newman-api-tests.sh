#!/bin/bash

# Bifrost API Management & Health Tests
# This script runs tests for /api/* and /health endpoints

set -e
set -o pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Configuration
COLLECTION="$SCRIPT_DIR/bifrost-api-management.postman_collection.json"
REPORT_DIR="$SCRIPT_DIR/newman-reports/api-management"

# Colors for output
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

# Print banner
echo -e "${GREEN}========================================${NC}"
echo -e "${GREEN}Bifrost API Management & Health Tests${NC}"
echo -e "${GREEN}========================================${NC}"
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

# Create report directory and log directory
mkdir -p "$REPORT_DIR"
LOG_DIR="$REPORT_DIR/parallel_logs"
mkdir -p "$LOG_DIR"

# Parse command line arguments
VERBOSE="--verbose"
REPORTERS="cli"
BAIL=""

while [[ $# -gt 0 ]]; do
    case $1 in
        --verbose)
            VERBOSE="--verbose"
            shift
            ;;
        --no-verbose)
            VERBOSE=""
            shift
            ;;
        --html)
            REPORTERS="cli,html"
            shift
            ;;
        --json)
            REPORTERS="cli,json"
            shift
            ;;
        --all-reports)
            REPORTERS="cli,html,json"
            shift
            ;;
        --bail)
            BAIL="--bail"
            shift
            ;;
        --help)
            echo "Usage: $0 [OPTIONS]"
            echo ""
            echo "Options:"
            echo "  --verbose           Show detailed output (enabled by default)"
            echo "  --no-verbose        Disable verbose output"
            echo "  --html              Generate HTML report"
            echo "  --json              Generate JSON report"
            echo "  --all-reports       Generate all report types"
            echo "  --bail              Stop on first failure"
            echo "  --help              Show this help message"
            echo ""
            echo "Examples:"
            echo "  $0                  # Run API management tests"
            echo "  $0 --html           # Run with HTML report"
            echo "  $0 --verbose        # Run with verbose output"
            exit 0
            ;;
        *)
            echo -e "${RED}Unknown option: $1${NC}"
            echo "Use --help for usage information"
            exit 1
            ;;
    esac
done

echo -e "Configuration:"
echo -e "  Collection: ${YELLOW}$COLLECTION${NC}"
echo -e "  Reports:    ${YELLOW}$REPORT_DIR${NC}"
echo -e "  Verbose:    ${YELLOW}$([ -n "$VERBOSE" ] && echo "enabled" || echo "disabled")${NC}"
echo ""
echo -e "${GREEN}Running tests...${NC}"
echo ""

# Setup test infrastructure (plugin + MCP server for API Management tests)
RUNNER_DIR="$SCRIPT_DIR"
if [ -f "$RUNNER_DIR/setup-plugin.sh" ]; then
    echo "Setting up test infrastructure..."
    "$RUNNER_DIR/setup-plugin.sh" 2>/dev/null || echo "Plugin setup skipped"
fi
if [ -f "$RUNNER_DIR/setup-mcp.sh" ]; then
    "$RUNNER_DIR/setup-mcp.sh" 2>/dev/null || echo "MCP setup skipped"
fi
echo ""

# Build Newman command
cmd=(newman run "$COLLECTION" --timeout-script 300000 -r "$REPORTERS")

if [[ "$REPORTERS" == *"html"* ]]; then
    cmd+=(--reporter-html-export "$REPORT_DIR/report.html")
fi

if [[ "$REPORTERS" == *"json"* ]]; then
    cmd+=(--reporter-json-export "$REPORT_DIR/report.json")
fi

[ -n "$VERBOSE" ] && cmd+=("$VERBOSE")
[ -n "$BAIL" ] && cmd+=("$BAIL")

# Run Newman and save output to log file while displaying to console (using tee)
LOG_FILE="$LOG_DIR/api-management.log"
set +e
"${cmd[@]}" 2>&1 | tee "$LOG_FILE"
EXIT_CODE=${PIPESTATUS[0]}
set -e

echo ""
if [ $EXIT_CODE -eq 0 ]; then
    echo -e "${GREEN}✓ All tests passed!${NC}"
else
    echo -e "${RED}✗ Some tests failed${NC}"
fi

if [[ "$REPORTERS" == *"html"* ]] || [[ "$REPORTERS" == *"json"* ]]; then
    echo ""
    echo -e "Reports saved to: ${YELLOW}$REPORT_DIR${NC}"
    ls -lh "$REPORT_DIR" 2>/dev/null | tail -n +2
fi
echo -e "Log saved to: ${YELLOW}$LOG_FILE${NC}"

exit $EXIT_CODE
