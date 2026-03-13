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
DB_VERIFY=""
DB_URL="${BIFROST_DB_URL:-}"
LOGS_DB_URL="${BIFROST_LOGS_DB_URL:-}"
DB_CONFIG_PATH=""

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
        --db-verify)
            DB_VERIFY="1"
            shift
            ;;
        --db-url)
            DB_URL="$2"
            shift 2
            ;;
        --logs-db-url)
            LOGS_DB_URL="$2"
            shift 2
            ;;
        --config-path)
            DB_CONFIG_PATH="$2"
            shift 2
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
            echo "  --db-verify         Enable DB verification reporter (PostgreSQL or SQLite)"
            echo "  --db-url <dsn>      Explicit main DB connection string (overrides auto-detection)"
            echo "  --logs-db-url <dsn> Explicit logs DB url (also reads BIFROST_LOGS_DB_URL; auto-detected)"
            echo "                      PostgreSQL: postgresql://user:pass@host:port/db"
            echo "                      SQLite:     sqlite:///path/to/file.db"
            echo "  --config-path <p>   Path to Bifrost config.json for auto DB detection"
            echo "                      (default: ./config.json; also reads BIFROST_CONFIG_PATH env)"
            echo "  --help              Show this help message"
            echo ""
            echo "Examples:"
            echo "  $0                  # Run API management tests"
            echo "  $0 --html           # Run with HTML report"
            echo "  $0 --verbose        # Run with verbose output"
            echo "  $0 --db-verify      # Run with DB verification"
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
if [ -n "$DB_VERIFY" ]; then
    if [ -n "$DB_URL" ]; then
        echo -e "  DB Verify:  ${YELLOW}enabled (url: $DB_URL)${NC}"
    elif [ -n "$DB_CONFIG_PATH" ]; then
        echo -e "  DB Verify:  ${YELLOW}enabled (config: $DB_CONFIG_PATH)${NC}"
    else
        echo -e "  DB Verify:  ${YELLOW}enabled (auto-detect from ./config.json)${NC}"
    fi
else
    echo -e "  DB Verify:  ${YELLOW}disabled${NC}"
fi
# Repo root (tests/e2e/api -> ../../..)
BIFROST_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
PLUGIN_DIR="$BIFROST_ROOT/examples/plugins/hello-world"
PLUGIN_SO="$PLUGIN_DIR/build/hello-world.so"

# Build hello-world plugin and resolve absolute path for plugin_path (before any test infra)
if [ -d "$PLUGIN_DIR" ] && [ -f "$PLUGIN_DIR/Makefile" ]; then
    echo "Building hello-world plugin..."
    (cd "$PLUGIN_DIR" && make build) 2>/dev/null || (cd "$PLUGIN_DIR" && make dev) 2>/dev/null || true
    if [ -f "$PLUGIN_SO" ]; then
        PLUGIN_PATH_ABS="$(cd "$(dirname "$PLUGIN_SO")" && pwd)/$(basename "$PLUGIN_SO")"
        echo "  Plugin: $PLUGIN_PATH_ABS"
    else
        PLUGIN_PATH_ABS=""
    fi
else
    PLUGIN_PATH_ABS=""
fi

# ── http-no-ping-server (MCP HTTP server on :3001) ───────────────────────────
HTTP_SERVER_DIR="$BIFROST_ROOT/examples/mcps/http-no-ping-server"
HTTP_SERVER_BIN="$HTTP_SERVER_DIR/http-server"
HTTP_SERVER_PID=""

start_http_mcp_server() {
    # Skip if something is already listening on 3001
    if lsof -ti tcp:3001 &>/dev/null 2>&1; then
        echo "  http-no-ping-server: port 3001 already in use, skipping start"
        return 0
    fi

    if [ ! -d "$HTTP_SERVER_DIR" ]; then
        echo "  http-no-ping-server: directory not found ($HTTP_SERVER_DIR), skipping"
        return 0
    fi

    # Build binary if missing
    if [ ! -f "$HTTP_SERVER_BIN" ]; then
        echo "  Building http-no-ping-server..."
        (cd "$HTTP_SERVER_DIR" && CGO_ENABLED=0 go build -o http-server main.go) || {
            echo "  http-no-ping-server: build failed, skipping"
            return 0
        }
    fi

    echo "  Starting http-no-ping-server on port 3001..."
    "$HTTP_SERVER_BIN" &
    HTTP_SERVER_PID=$!

    # Wait up to 10 s for it to accept connections
    for i in $(seq 1 10); do
        sleep 1
        if lsof -ti tcp:3001 &>/dev/null 2>&1; then
            echo "  http-no-ping-server ready (PID $HTTP_SERVER_PID)"
            return 0
        fi
    done

    echo "  WARNING: http-no-ping-server did not become ready in time"
}

stop_http_mcp_server() {
    if [ -n "$HTTP_SERVER_PID" ] && kill -0 "$HTTP_SERVER_PID" 2>/dev/null; then
        echo "Stopping http-no-ping-server (PID $HTTP_SERVER_PID)..."
        kill "$HTTP_SERVER_PID" 2>/dev/null || true
    fi
}

# Register teardown so the server is stopped even if the script exits early
trap stop_http_mcp_server EXIT

echo "Setting up MCP test servers..."
start_http_mcp_server
echo ""
echo ""
echo -e "${GREEN}Running tests...${NC}"
echo ""

# Add dbverify reporter if requested
if [ -n "$DB_VERIFY" ]; then
    REPORTERS="$REPORTERS,dbverify"
    # Install dependencies for the dbverify reporter if not already present
    if [ ! -d "$SCRIPT_DIR/node_modules" ]; then
        echo "Installing DB verify reporter dependencies..."
        (cd "$SCRIPT_DIR" && npm install --silent)
    fi
    # Newman (global) resolves reporters via Node's module search. Prepend the
    # local node_modules so it can find newman-reporter-dbverify without a
    # global install.
    export NODE_PATH="$SCRIPT_DIR/node_modules${NODE_PATH:+:$NODE_PATH}"
fi

# Build Newman command
cmd=(newman run "$COLLECTION" --timeout-script 120000 --timeout 900000 -r "$REPORTERS")

# Override plugin_path with resolved absolute path so Create Plugin / Get Plugin use the built .so
# env-var takes precedence over collection variables in Newman's resolution order
if [ -n "$PLUGIN_PATH_ABS" ]; then
    cmd+=(--env-var "plugin_path=$PLUGIN_PATH_ABS")
fi

if [[ "$REPORTERS" == *"html"* ]]; then
    cmd+=(--reporter-html-export "$REPORT_DIR/report.html")
fi

if [[ "$REPORTERS" == *"json"* ]]; then
    cmd+=(--reporter-json-export "$REPORT_DIR/report.json")
fi

if [ -n "$DB_VERIFY" ]; then
    [ -n "$DB_URL" ]      && cmd+=(--reporter-dbverify-db-url "$DB_URL")
    [ -n "$LOGS_DB_URL" ] && cmd+=(--reporter-dbverify-logs-db-url "$LOGS_DB_URL")
    [ -n "$DB_CONFIG_PATH" ] && cmd+=(--reporter-dbverify-config "$DB_CONFIG_PATH")
fi

[ -n "$VERBOSE" ] && cmd+=("$VERBOSE")
[ -n "$BAIL" ] && cmd+=("$BAIL")

# Run Newman and save output to log file while displaying to console (using tee)
LOG_FILE="$LOG_DIR/api-management.log"

# Write resolved plugin path to log before running tests
if [ -n "$PLUGIN_PATH_ABS" ]; then
    echo "[setup] plugin_path resolved to: $PLUGIN_PATH_ABS" | tee "$LOG_FILE"
else
    echo "[setup] plugin_path not resolved (build may have failed)" | tee "$LOG_FILE"
fi

set +e
"${cmd[@]}" 2>&1 | tee -a "$LOG_FILE"
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
