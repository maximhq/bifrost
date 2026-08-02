#!/bin/bash

# Bifrost API Management & Health Tests
# This script runs tests for /api/* and /health endpoints

set -e
set -o pipefail

# Fail fast on Bash <4.0: declare -A seed_env_values, the seed env parser that
# populates it from $SEED_ENV_PATH, and add_env_var_if_set all rely on
# associative arrays, which were added in Bash 4.0. macOS still ships Bash 3.2
# by default, so a stale /bin/bash would otherwise fail with a cryptic
# "declare: -A: invalid option" partway through setup.
if [ "${BASH_VERSINFO[0]:-0}" -lt 4 ]; then
    echo "Error: this script requires Bash 4.0+ (uses 'declare -A seed_env_values', SEED_ENV_PATH parsing, and add_env_var_if_set)." >&2
    echo "Detected Bash version: ${BASH_VERSION:-unknown}" >&2
    echo "On macOS, install a newer bash (e.g. 'brew install bash') and run the script with that interpreter." >&2
    exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
API_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

# Configuration
COLLECTION="$API_DIR/collections/bifrost-api-management.postman_collection.json"
REPORT_DIR="$API_DIR/newman-reports/api-management"

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
    echo "Install it with: npm install -g newman newman-reporter-htmlextra"
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
SEED_ENV_PATH="${BIFROST_E2E_SEED_ENV:-}"
EXPECTED_PATH="${BIFROST_E2E_SEED_EXPECTED:-}"
EXTRA_COLLECTIONS=()
BASE_URL="${BIFROST_E2E_BASE_URL:-${BIFROST_BASE_URL:-http://localhost:8080}}"
ADMIN_USERNAME="${BIFROST_E2E_ADMIN_USERNAME:-admin}"
ADMIN_PASSWORD="${BIFROST_E2E_ADMIN_PASSWORD:-Bifrost-E2E-Admin-Pass1!}"
ADMIN_AUTH_HEADER="Bearer $(printf '%s:%s' "$ADMIN_USERNAME" "$ADMIN_PASSWORD" | base64 | tr -d '\n')"

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
            REPORTERS="${REPORTERS},htmlextra"
            shift
            ;;
        --json)
            REPORTERS="${REPORTERS},json"
            shift
            ;;
        --all-reports)
            REPORTERS="cli,htmlextra,json"
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
            if [ $# -lt 2 ] || [[ "$2" == --* ]]; then
                echo -e "${RED}Error: --db-url requires a value${NC}"
                exit 1
            fi
            DB_URL="$2"
            shift 2
            ;;
        --logs-db-url)
            if [ $# -lt 2 ] || [[ "$2" == --* ]]; then
                echo -e "${RED}Error: --logs-db-url requires a value${NC}"
                exit 1
            fi
            LOGS_DB_URL="$2"
            shift 2
            ;;
        --config-path)
            if [ $# -lt 2 ] || [[ "$2" == --* ]]; then
                echo -e "${RED}Error: --config-path requires a value${NC}"
                exit 1
            fi
            DB_CONFIG_PATH="$2"
            shift 2
            ;;
        --extra-collection)
            if [ $# -lt 2 ] || [[ "$2" == --* ]]; then
                echo -e "${RED}Error: --extra-collection requires a path${NC}"
                exit 1
            fi
            EXTRA_COLLECTIONS+=("$2")
            shift 2
            ;;
        --seed-env)
            if [ $# -lt 2 ] || [[ "$2" == --* ]]; then
                echo -e "${RED}Error: --seed-env requires a path${NC}"
                exit 1
            fi
            SEED_ENV_PATH="$2"
            shift 2
            ;;
        --expected)
            if [ $# -lt 2 ] || [[ "$2" == --* ]]; then
                echo -e "${RED}Error: --expected requires a path${NC}"
                exit 1
            fi
            EXPECTED_PATH="$2"
            shift 2
            ;;
        --help)
            echo "Usage: $0 [OPTIONS]"
            echo ""
            echo "Options:"
            echo "  --verbose           Show detailed output (enabled by default)"
            echo "  --no-verbose        Disable verbose output"
            echo "  --html              Generate HTML report using newman-reporter-htmlextra"
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
            echo "  --extra-collection <p>"
            echo "                      Merge an additional Postman collection into this API run."
            echo "                      Intended for API e2e coverage maintained in another repo."
            echo "  --seed-env <p>      Load generated seed dotenv values and pass them to Newman."
            echo "  --expected <p>      Load generated DAC expected-manifest JSON for assertions."
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

if [ -n "${BIFROST_API_EXTRA_COLLECTION:-}" ]; then
    EXTRA_COLLECTIONS+=("$BIFROST_API_EXTRA_COLLECTION")
fi

if [ -z "$SEED_ENV_PATH" ] && [ -f "$API_DIR/generated/seed.env" ]; then
    SEED_ENV_PATH="$API_DIR/generated/seed.env"
fi

if [ -z "$EXPECTED_PATH" ] && [ -f "$API_DIR/generated/dac-expected.json" ]; then
    EXPECTED_PATH="$API_DIR/generated/dac-expected.json"
fi

if { [ "${GITHUB_ACTIONS:-}" = "true" ] || [ "${CI:-0}" = "1" ]; } && [[ "$REPORTERS" != *"htmlextra"* ]]; then
    REPORTERS="${REPORTERS},htmlextra"
fi

# Make globally-installed npm packages (e.g. newman-reporter-htmlextra from
# `npm install -g`) visible to Node's module resolver. Node 20+ no longer falls
# back to the global npm prefix automatically, so without this NODE_PATH addition
# the require.resolve check below fails even when the package is installed.
if NPM_GLOBAL_ROOT="$(npm root -g 2>/dev/null)" && [ -n "$NPM_GLOBAL_ROOT" ]; then
    export NODE_PATH="$NPM_GLOBAL_ROOT${NODE_PATH:+:$NODE_PATH}"
fi

# Validate optional reporters are resolvable before invoking newman so we fail
# fast with a clear message instead of a cryptic mid-run newman error. node's
# require.resolve uses the same module-resolution path newman uses (including
# the NODE_PATH set above for the global npm root and below for newman-reporter-dbverify).
if [[ "$REPORTERS" == *"htmlextra"* ]]; then
    if ! node -e 'require.resolve("newman-reporter-htmlextra")' >/dev/null 2>&1; then
        echo -e "${RED}Error: newman-reporter-htmlextra is not installed${NC}"
        echo "Install it with: npm install -g newman-reporter-htmlextra"
        exit 1
    fi
fi

echo -e "Configuration:"
echo -e "  Collection: ${YELLOW}$COLLECTION${NC}"
echo -e "  Base URL:   ${YELLOW}$BASE_URL${NC}"
echo -e "  Reports:    ${YELLOW}$REPORT_DIR${NC}"
echo -e "  Verbose:    ${YELLOW}$([ -n "$VERBOSE" ] && echo "enabled" || echo "disabled")${NC}"
echo -e "  Auth Pass:  ${YELLOW}enabled (username: $ADMIN_USERNAME)${NC}"
if [ ${#EXTRA_COLLECTIONS[@]} -gt 0 ]; then
    echo -e "  Extensions: ${YELLOW}${EXTRA_COLLECTIONS[*]}${NC}"
fi
if [ -n "$SEED_ENV_PATH" ]; then
    echo -e "  Seed Env:   ${YELLOW}$SEED_ENV_PATH${NC}"
fi
if [ -n "$EXPECTED_PATH" ]; then
    echo -e "  Expected:   ${YELLOW}$EXPECTED_PATH${NC}"
fi
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
BIFROST_ROOT="$(cd "$API_DIR/../../.." && pwd)"
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
AUTH_ENABLED_BY_RUN=""

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

cleanup() {
    if [ "$AUTH_ENABLED_BY_RUN" = "1" ]; then
        echo "Restoring dashboard auth to disabled..."
        BIFROST_E2E_AUTH_HEADER="$ADMIN_AUTH_HEADER" \
        BIFROST_E2E_BASE_URL="$BASE_URL" \
        BIFROST_E2E_ADMIN_USERNAME="$ADMIN_USERNAME" \
        BIFROST_E2E_ADMIN_PASSWORD="$ADMIN_PASSWORD" \
            node "$SCRIPT_DIR/set-auth-config.mjs" disable >/dev/null 2>&1 || true
    fi
    stop_http_mcp_server
}

# Register teardown so auth and the server are restored even if the script exits early
trap cleanup EXIT

echo "Setting up MCP test servers..."
start_http_mcp_server
echo ""
echo ""
echo -e "${GREEN}Running tests...${NC}"
echo ""

if [ ${#EXTRA_COLLECTIONS[@]} -gt 0 ]; then
    MERGED_COLLECTION="$REPORT_DIR/api-management-with-extensions.postman_collection.json"
    merge_cmd=(node "$SCRIPT_DIR/merge-collections.mjs" --source "$COLLECTION" --out "$MERGED_COLLECTION")
    for extra_collection in "${EXTRA_COLLECTIONS[@]}"; do
        if [ ! -f "$extra_collection" ]; then
            echo -e "${RED}Error: Extra collection file not found: $extra_collection${NC}"
            exit 1
        fi
        merge_cmd+=(--extra "$extra_collection")
    done
    "${merge_cmd[@]}"
    COLLECTION="$MERGED_COLLECTION"
fi

# Add dbverify reporter if requested
if [ -n "$DB_VERIFY" ]; then
    REPORTERS="$REPORTERS,dbverify"
    # Install dependencies for the dbverify reporter if not already present
    if [ ! -d "$API_DIR/node_modules" ]; then
        echo "Installing DB verify reporter dependencies..."
        (cd "$API_DIR" && npm install --silent)
    fi
    # Newman (global) resolves reporters via Node's module search. Prepend the
    # local node_modules so it can find newman-reporter-dbverify without a
    # global install.
    export NODE_PATH="$API_DIR/node_modules${NODE_PATH:+:$NODE_PATH}"
fi

# Build shared Newman arguments. The collection and reporter export paths are
# supplied per pass so the unauthenticated and authenticated runs keep separate
# reports while using identical environment/config inputs.
newman_args=(--timeout-script 120000 --timeout 900000 -r "$REPORTERS" --env-var "base_url=$BASE_URL")

# Parsed seed env entries live here, not in the runner's shell namespace.
# Decoupling the data keyspace from the script's own variables (PATH, COLLECTION,
# NODE_OPTIONS, etc.) prevents a seed file entry from silently overriding them.
declare -A seed_env_values=()

if [ -n "$SEED_ENV_PATH" ]; then
    if [ ! -f "$SEED_ENV_PATH" ]; then
        echo -e "${RED}Error: seed env file not found: $SEED_ENV_PATH${NC}"
        exit 1
    fi
    # Parse seed env file as data, never source it. Sourcing would execute
    # any RHS command substitution (e.g. FOO=$(id)); see WriteEnvFile in
    # framework/e2eseed/seed.go which single-quotes values via quoteEnv.
    while IFS= read -r line || [ -n "$line" ]; do
        [[ "$line" =~ ^[[:space:]]*# ]] && continue
        [[ -z "${line//[[:space:]]/}" ]] && continue
        if [[ "$line" != *=* ]]; then
            echo -e "${RED}Error: invalid seed env line (missing '='): $line${NC}"
            exit 1
        fi
        key="${line%%=*}"
        value="${line#*=}"
        if [[ ! "$key" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]]; then
            echo -e "${RED}Error: invalid variable name in seed env: $key${NC}"
            exit 1
        fi
        # Unwrap outer single quotes written by quoteEnv and undo its '\''-escape.
        if [[ "$value" == \'*\' ]]; then
            value="${value:1:${#value}-2}"
            value="${value//\'\"\'\"\'/\'}"
        fi
        seed_env_values["$key"]="$value"
    done < "$SEED_ENV_PATH"
fi

add_env_var_if_set() {
    local name="$1"
    local value="${seed_env_values[$name]:-}"
    if [ -n "$value" ]; then
        newman_args+=(--env-var "$name=$value")
    fi
}

for seed_var in \
    e2e_seed_prefix \
    enterprise_dac_model \
    enterprise_dac_visible_virtual_key \
    enterprise_dac_hidden_virtual_key \
    enterprise_dac_reader_api_key \
    enterprise_dac_all_api_key \
    enterprise_dac_own_api_key \
    enterprise_dac_outside_api_key \
    e2e_seed_team_tiggings \
    e2e_seed_team_outside \
    e2e_seed_user_tiggings \
    e2e_seed_user_outside \
    e2e_seed_vk_user_team \
    e2e_seed_vk_outside \
    e2e_seed_enterprise_users \
    e2e_seed_access_profiles \
    e2e_seed_access_profile_budgets; do
    add_env_var_if_set "$seed_var"
done

if [ -n "$EXPECTED_PATH" ]; then
    if [ ! -f "$EXPECTED_PATH" ]; then
        echo -e "${RED}Error: expected manifest file not found: $EXPECTED_PATH${NC}"
        exit 1
    fi
    EXPECTED_JSON="$(node -e 'const fs=require("fs"); const p=process.argv[1]; process.stdout.write(JSON.stringify(JSON.parse(fs.readFileSync(p,"utf8"))));' "$EXPECTED_PATH")"
    newman_args+=(--env-var "e2e_seed_expected=$EXPECTED_JSON")
fi

# Override plugin_path with resolved absolute path so Create Plugin / Get Plugin use the built .so
# env-var takes precedence over collection variables in Newman's resolution order
if [ -n "$PLUGIN_PATH_ABS" ]; then
    newman_args+=(--env-var "plugin_path=$PLUGIN_PATH_ABS")
fi

if [ -n "$DB_VERIFY" ]; then
    [ -n "$DB_URL" ]      && newman_args+=(--reporter-dbverify-db-url "$DB_URL")
    [ -n "$LOGS_DB_URL" ] && newman_args+=(--reporter-dbverify-logs-db-url "$LOGS_DB_URL")
    [ -n "$DB_CONFIG_PATH" ] && newman_args+=(--reporter-dbverify-config "$DB_CONFIG_PATH")
fi

[ -n "$VERBOSE" ] && newman_args+=("$VERBOSE")
[ -n "$BAIL" ] && newman_args+=("$BAIL")

build_newman_cmd() {
    local collection_path="$1"
    local report_suffix="$2"
    cmd=(newman run "$collection_path" "${newman_args[@]}")

    if [[ "$REPORTERS" == *"htmlextra"* ]]; then
        cmd+=(--reporter-htmlextra-export "$REPORT_DIR/report${report_suffix}.html")
        cmd+=(--reporter-htmlextra-title "Bifrost API Management & Health${report_suffix}")
        cmd+=(--reporter-htmlextra-darkTheme)
    fi

    if [[ "$REPORTERS" == *"json"* ]]; then
        cmd+=(--reporter-json-export "$REPORT_DIR/report${report_suffix}.json")
    fi
}

# Run Newman and save output to log file while displaying to console (using tee)
LOG_FILE="$LOG_DIR/api-management.log"

# Write resolved plugin path to log before running tests
if [ -n "$PLUGIN_PATH_ABS" ]; then
    echo "[setup] plugin_path resolved to: $PLUGIN_PATH_ABS" | tee "$LOG_FILE"
else
    echo "[setup] plugin_path not resolved (build may have failed)" | tee "$LOG_FILE"
fi

run_newman_pass() {
    local label="$1"
    local collection_path="$2"
    local report_suffix="$3"
    local with_auth="$4"

    echo "" | tee -a "$LOG_FILE"
    echo -e "${GREEN}${label}${NC}" | tee -a "$LOG_FILE"

    build_newman_cmd "$collection_path" "$report_suffix"
    if [ "$with_auth" = "1" ]; then
        cmd+=(--env-var "admin_auth_header=$ADMIN_AUTH_HEADER")
        cmd+=(--env-var "admin_username=$ADMIN_USERNAME")
        cmd+=(--env-var "admin_password=$ADMIN_PASSWORD")
    fi

    set +e
    "${cmd[@]}" 2>&1 | tee -a "$LOG_FILE"
    local pass_exit=${PIPESTATUS[0]}
    set -e
    return $pass_exit
}

run_newman_pass "Running unauthenticated API management tests..." "$COLLECTION" "" ""
EXIT_CODE=$?

AUTH_COLLECTION="$REPORT_DIR/api-management-auth.postman_collection.json"
if [ $EXIT_CODE -eq 0 ]; then
    echo "" | tee -a "$LOG_FILE"
    echo -e "${GREEN}Enabling dashboard auth for authenticated API management tests...${NC}" | tee -a "$LOG_FILE"
    set +e
    BIFROST_E2E_BASE_URL="$BASE_URL" \
    BIFROST_E2E_ADMIN_USERNAME="$ADMIN_USERNAME" \
    BIFROST_E2E_ADMIN_PASSWORD="$ADMIN_PASSWORD" \
        node "$SCRIPT_DIR/set-auth-config.mjs" enable 2>&1 | tee -a "$LOG_FILE"
    AUTH_SETUP_EXIT=${PIPESTATUS[0]}
    set -e
    if [ $AUTH_SETUP_EXIT -ne 0 ]; then
        EXIT_CODE=$AUTH_SETUP_EXIT
    else
        AUTH_ENABLED_BY_RUN="1"
        node "$SCRIPT_DIR/add-auth-header.mjs" "$COLLECTION" "$AUTH_COLLECTION"
        run_newman_pass "Running authenticated API management tests..." "$AUTH_COLLECTION" "-auth" "1"
        EXIT_CODE=$?
    fi
fi

if [ $EXIT_CODE -eq 0 ] && [ "${BIFROST_E2E_SKIP_OBSERVABILITY:-0}" != "1" ]; then
    echo "" | tee -a "$LOG_FILE"
    echo -e "${GREEN}Running authenticated local OTEL and Prometheus observability checks...${NC}" | tee -a "$LOG_FILE"
    set +e
    BIFROST_E2E_BASE_URL="$BASE_URL" \
    BIFROST_E2E_AUTH_HEADER="$ADMIN_AUTH_HEADER" \
        node "$SCRIPT_DIR/run-observability-local.mjs" 2>&1 | tee -a "$LOG_FILE"
    OBS_EXIT_CODE=${PIPESTATUS[0]}
    set -e
    if [ $OBS_EXIT_CODE -ne 0 ]; then
        EXIT_CODE=$OBS_EXIT_CODE
    fi
elif [ $EXIT_CODE -eq 0 ]; then
    echo "" | tee -a "$LOG_FILE"
    echo -e "${YELLOW}Skipping local OTEL and Prometheus observability checks (BIFROST_E2E_SKIP_OBSERVABILITY=1).${NC}" | tee -a "$LOG_FILE"
fi

if [ "$AUTH_ENABLED_BY_RUN" = "1" ]; then
    echo "" | tee -a "$LOG_FILE"
    echo -e "${GREEN}Restoring dashboard auth to disabled...${NC}" | tee -a "$LOG_FILE"
    set +e
    BIFROST_E2E_AUTH_HEADER="$ADMIN_AUTH_HEADER" \
    BIFROST_E2E_BASE_URL="$BASE_URL" \
    BIFROST_E2E_ADMIN_USERNAME="$ADMIN_USERNAME" \
    BIFROST_E2E_ADMIN_PASSWORD="$ADMIN_PASSWORD" \
        node "$SCRIPT_DIR/set-auth-config.mjs" disable 2>&1 | tee -a "$LOG_FILE"
    AUTH_RESTORE_EXIT=${PIPESTATUS[0]}
    set -e
    AUTH_ENABLED_BY_RUN=""
    if [ $EXIT_CODE -eq 0 ] && [ $AUTH_RESTORE_EXIT -ne 0 ]; then
        EXIT_CODE=$AUTH_RESTORE_EXIT
    fi
fi

echo ""
if [ $EXIT_CODE -eq 0 ]; then
    echo -e "${GREEN}✓ All tests passed!${NC}"
else
    echo -e "${RED}✗ Some tests failed${NC}"
fi

if [[ "$REPORTERS" == *"htmlextra"* ]] || [[ "$REPORTERS" == *"json"* ]]; then
    echo ""
    echo -e "Reports saved to: ${YELLOW}$REPORT_DIR${NC}"
    ls -lh "$REPORT_DIR" 2>/dev/null | tail -n +2
fi
if [[ "$REPORTERS" == *"htmlextra"* ]]; then
    echo -e "HTML report: ${YELLOW}$REPORT_DIR/report.html${NC}"
    [ -f "$REPORT_DIR/report-auth.html" ] && echo -e "Auth HTML report: ${YELLOW}$REPORT_DIR/report-auth.html${NC}"
fi
echo -e "Log saved to: ${YELLOW}$LOG_FILE${NC}"

exit $EXIT_CODE
