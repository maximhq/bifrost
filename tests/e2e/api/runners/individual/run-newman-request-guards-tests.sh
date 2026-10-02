#!/bin/bash

# Bifrost V1 Request Guards Newman Test Runner
# Runs the hermetic request-guard suite (collections/bifrost-v1-request-guards)
# against an already-running gateway: catalog URL validation on PUT /api/config,
# MCP client registration refusals, proxy / provider / provider-key endpoint
# changes that need an admin session, OAuth2 issuance availability, passthrough
# path validation, request body limits, the webhook test-delivery gate and
# auth_config updates that must prove the stored admin password. No provider is
# ever contacted and nothing is paid for. Wall clock is a few seconds.
#
# The gateway must run with dashboard auth DISABLED. The last folder (Dashboard
# auth update) additionally needs a stored admin account whose password this
# runner knows; it only runs when BIFROST_E2E_ADMIN_EXISTS=1, re-enables auth
# with that password and disables it again, leaving the gateway as it found it.

set -e
# Everything this runner writes (the secret environment file, Newman's report exports)
# is owner-only from the moment it is created: Newman writes reports under the process
# umask before they can be redacted, so a permissive umask would expose them mid-run.
umask 077

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
API_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
# newman resolves the zstd fixture (body.mode file) relative to its cwd.
cd "$API_DIR"

# Configuration
COLLECTION="collections/bifrost-v1-request-guards.postman_collection.json"
REPORT_DIR="newman-reports/request-guards"

# Colors for output
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

# Parse arguments
VERBOSE=""
REPORTERS="cli"
BAIL=""
while [[ $# -gt 0 ]]; do
    case "$1" in
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
        --help)
            echo "Usage: $0 [OPTIONS]"
            echo ""
            echo "Options:"
            echo "  --verbose           Show detailed output"
            echo "  --html              Generate HTML report"
            echo "  --json              Generate JSON report"
            echo "  --bail              Stop on first failure"
            echo "  --help              Show this help message"
            echo ""
            echo "Environment Variables:"
            echo "  BIFROST_BASE_URL              Gateway base URL (default: http://localhost:8080); dashboard auth must be disabled"
            echo "  BIFROST_E2E_ADMIN_EXISTS      Set to 1 when a stored admin account exists whose password is"
            echo "                                BIFROST_E2E_ADMIN_PASSWORD; enables the Dashboard auth update folder (default: 0)"
            echo "  BIFROST_E2E_ADMIN_USERNAME    Stored admin username (default: admin)"
            echo "  BIFROST_E2E_ADMIN_PASSWORD    Stored admin password (default: Bifrost-E2E-Admin-Pass1!)"
            echo ""
            echo "Examples:"
            echo "  BIFROST_BASE_URL=http://localhost:8080 $0"
            echo "  BIFROST_E2E_ADMIN_EXISTS=1 BIFROST_E2E_ADMIN_PASSWORD='...' $0 --json"
            exit 0
            ;;
        *)
            echo -e "${RED}Unknown option: $1${NC}"
            exit 1
            ;;
    esac
done

# Print banner
echo -e "${GREEN}==============================================${NC}"
echo -e "${GREEN}Bifrost V1 Request Guards Test Runner${NC}"
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

# Create the report directory owner-only, and lock down any report a previous run left
# behind before Newman overwrites it.
mkdir -p "$REPORT_DIR"
chmod 700 "$REPORT_DIR"
chmod 600 "$REPORT_DIR"/report.json "$REPORT_DIR"/report.html 2>/dev/null || true

# Base URL and admin credentials. The collection reads admin_* through
# pm.variables, so passing them as --env-var overrides the collection defaults.
base_url="${BIFROST_BASE_URL:-http://localhost:8080}"
admin_exists="${BIFROST_E2E_ADMIN_EXISTS:-0}"
admin_username="${BIFROST_E2E_ADMIN_USERNAME:-admin}"
admin_password="${BIFROST_E2E_ADMIN_PASSWORD:-Bifrost-E2E-Admin-Pass1!}"
admin_auth_header="Bearer $(printf '%s:%s' "$admin_username" "$admin_password" | base64 | tr -d '\n')"

# The admin password and the derived Authorization header are secrets: they go to Newman
# through a 0600 temporary environment file, never as command-line arguments, which any
# local user could read from /proc/<pid>/cmdline while Newman runs. The same applies to
# the jq call that writes the file: the values reach it through its environment (readable
# only by the same user), not as --arg values on its command line. The file is removed by
# the exit trap below.
if ! command -v jq &>/dev/null; then
    echo -e "${RED}Error: jq is required to write the Newman environment file${NC}"
    exit 1
fi
# Installed before the secret file is created, so a failure while writing it still removes
# it. The Dashboard auth folder re-enables auth and disables it again in its last request. A run
# that stops in between (--bail, a crash, Ctrl-C) would leave the gateway with auth enabled, so
# unless the run completed, best-effort restore the disabled state the suite started from.
RUN_COMPLETED=0
restore_dashboard_auth() {
    rm -f "$SECRET_ENV_FILE"
    if [ "$RUN_COMPLETED" = "1" ] || [ "$admin_exists" != "1" ]; then
        return
    fi
    echo -e "${YELLOW}Run did not complete; restoring dashboard auth to disabled...${NC}"
    BIFROST_E2E_AUTH_HEADER="$admin_auth_header" \
    BIFROST_E2E_BASE_URL="$base_url" \
    BIFROST_E2E_ADMIN_USERNAME="$admin_username" \
    BIFROST_E2E_ADMIN_PASSWORD="$admin_password" \
        node runners/set-auth-config.mjs disable >/dev/null 2>&1 || true
}
trap restore_dashboard_auth EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

SECRET_ENV_FILE="$(mktemp "${TMPDIR:-/tmp}/request-guards-secrets.XXXXXX")"
chmod 600 "$SECRET_ENV_FILE"
RG_SECRET_PASSWORD="$admin_password" RG_SECRET_HEADER="$admin_auth_header" jq -n '{
  id: "request-guards-secrets",
  name: "request-guards-secrets",
  values: [
    {key: "admin_password", value: env.RG_SECRET_PASSWORD, type: "secret", enabled: true},
    {key: "admin_auth_header", value: env.RG_SECRET_HEADER, type: "secret", enabled: true}
  ]
}' > "$SECRET_ENV_FILE"

# Newman's JSON summary embeds the environment values and every request's headers, and the
# HTML report shows the same, so an exported report would keep the admin password and the
# Authorization header after the environment file is gone. Reports are rewritten with both
# values redacted and restricted to the owner before they are left on disk. The values are
# handed to node through its environment for the same reason as above.
redact_report_secrets() {
    local report
    for report in "$REPORT_DIR/report.json" "$REPORT_DIR/report.html"; do
        [ -f "$report" ] || continue
        chmod 600 "$report"
        RG_SECRET_PASSWORD="$admin_password" RG_SECRET_HEADER="$admin_auth_header" node -e '
            const fs = require("fs");
            const file = process.argv[1];
            let text = fs.readFileSync(file, "utf8");
            for (const name of ["RG_SECRET_HEADER", "RG_SECRET_PASSWORD"]) {
                const value = process.env[name];
                if (!value) continue;
                for (const form of [value, JSON.stringify(value).slice(1, -1), encodeURIComponent(value)]) {
                    text = text.split(form).join("<redacted>");
                }
            }
            fs.writeFileSync(file, text);
        ' "$report"
    done
}


# Build Newman command
cmd=(newman run "$COLLECTION")
cmd+=(--env-var "base_url=$base_url")
cmd+=(--env-var "admin_exists=$admin_exists")
cmd+=(--env-var "admin_username=$admin_username")
cmd+=(--environment "$SECRET_ENV_FILE")

cmd+=(--timeout-script 60000 --timeout 300000)
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
echo -e "  Collection:   ${YELLOW}$COLLECTION${NC}"
echo -e "  Base URL:     ${YELLOW}$base_url${NC}"
echo -e "  Admin exists: ${YELLOW}$admin_exists${NC} (username: $admin_username)"
echo -e "  Reports:      ${YELLOW}$REPORT_DIR${NC}"
echo ""
echo -e "${GREEN}Running tests...${NC}"
echo ""

set +e
"${cmd[@]}"
EXIT_CODE=$?
set -e
if [ $EXIT_CODE -eq 0 ]; then
    RUN_COMPLETED=1
fi

redact_report_secrets

echo ""
if [ $EXIT_CODE -eq 0 ]; then
    echo -e "${GREEN}✓ All request guard tests passed!${NC}"
else
    echo -e "${RED}✗ Some tests failed${NC}"
fi
if [[ "$REPORTERS" == *"html"* ]] || [[ "$REPORTERS" == *"json"* ]]; then
    echo ""
    echo -e "Reports saved to: ${YELLOW}$REPORT_DIR${NC}"
    ls -lh "$REPORT_DIR" 2>/dev/null | tail -n +2
fi

exit $EXIT_CODE
