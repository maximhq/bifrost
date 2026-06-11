#!/bin/bash

# Bifrost Routing Wiring Test Runner
# Drives the model-catalog wiring collection against a running Bifrost instance.
# Each scenario stands up an isolated, run-namespaced custom provider backed by a
# real upstream, mutates its providers/keys, and asserts the catalog read
# endpoints reflect every mutation.

set -e
set -o pipefail

# Associative arrays (declare -A), SEED_ENV_PATH parsing, and add_env_var_if_set
# all require Bash 4.0+. macOS still ships Bash 3.2 as /bin/bash, which would
# fail partway through with a cryptic "declare: -A: invalid option".
if [ "${BASH_VERSINFO[0]:-0}" -lt 4 ]; then
    echo "Error: this script requires Bash 4.0+ (uses 'declare -A seed_env_values' and SEED_ENV_PATH parsing)." >&2
    echo "Detected Bash version: ${BASH_VERSION:-unknown}" >&2
    echo "On macOS, install a newer bash (e.g. 'brew install bash') and run the script with that interpreter." >&2
    exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
API_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
cd "$API_DIR"

COLLECTION="collections/bifrost-routing-wiring.postman_collection.json"
ENVIRONMENT="bifrost-v1.postman_environment.json"
REPORT_DIR="newman-reports/routing-wiring"

GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m'

echo -e "${GREEN}========================================${NC}"
echo -e "${GREEN}Bifrost Routing Wiring Tests${NC}"
echo -e "${GREEN}========================================${NC}"
echo ""

if ! command -v newman &> /dev/null; then
    echo -e "${RED}Error: Newman is not installed${NC}"
    echo "Install it with: npm install -g newman newman-reporter-htmlextra"
    exit 1
fi

if [ ! -f "$COLLECTION" ]; then
    echo -e "${RED}Error: Collection file not found: $COLLECTION${NC}"
    exit 1
fi

ENV_FLAG=()
if [ -f "$ENVIRONMENT" ]; then
    ENV_FLAG=(-e "$ENVIRONMENT")
else
    echo -e "${YELLOW}Warning: Environment file not found: $ENVIRONMENT (using collection variables only)${NC}"
fi

# Seed env loading: prefer an explicit BIFROST_E2E_SEED_ENV, else the standard
# generated/seed.env that CI writes.
SEED_ENV_PATH="${BIFROST_E2E_SEED_ENV:-}"
if [ -z "$SEED_ENV_PATH" ] && [ -f "$API_DIR/generated/seed.env" ]; then
    SEED_ENV_PATH="$API_DIR/generated/seed.env"
fi

declare -A seed_env_values
if [ -n "$SEED_ENV_PATH" ] && [ -f "$SEED_ENV_PATH" ]; then
    # Parse the seed env as data, never source it — values may contain command
    # substitution that sourcing would execute.
    while IFS= read -r line || [ -n "$line" ]; do
        [[ "$line" =~ ^[[:space:]]*# ]] && continue
        [[ -z "${line//[[:space:]]/}" ]] && continue
        [[ "$line" != *=* ]] && continue
        key="${line%%=*}"
        value="${line#*=}"
        [[ ! "$key" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]] && continue
        # Unwrap outer single quotes written by the seed writer and undo its
        # '\''-escape; also tolerate plain double-quoted values.
        if [[ "$value" == \'*\' ]]; then
            value="${value:1:${#value}-2}"
            value="${value//\'\"\'\"\'/\'}"
        elif [[ "$value" == \"*\" ]]; then
            value="${value:1:${#value}-2}"
        fi
        seed_env_values["$key"]="$value"
    done < "$SEED_ENV_PATH"
fi

mkdir -p "$REPORT_DIR"

cmd=(newman run "$COLLECTION" "${ENV_FLAG[@]}" -r cli,htmlextra)

# Forward a seed-env value if present, falling back to the process environment
# so local runs work with credentials exported in the shell.
add_env_var_if_set() {
    local name="$1"
    local value="${seed_env_values[$name]:-}"
    if [ -z "$value" ]; then
        value="${!name:-}"
    fi
    if [ -n "$value" ]; then
        cmd+=(--env-var "$name=$value")
    fi
}

# Forward the run-id prefix only. Provider credentials are NOT injected here:
# the collection's keys use Bifrost's `env.<NAME>` resolution, so each key reads
# its credential from the Bifrost process env at request time (the Bifrost server
# must have the provider env vars, e.g. OPENAI_API_KEY/ANTHROPIC_API_KEY/...).
for v in e2e_seed_prefix; do
    add_env_var_if_set "$v"
done

# The live-model cache populates with one upstream round-trip's latency, and the
# in-collection poll loop can busy-wait up to ~4s per attempt. Give scripts and
# requests generous ceilings.
cmd+=(--timeout-script 120000 --timeout 900000)

# In CI keep going past failures so every scenario's cleanup folder runs.
ci_normalized="$(printf '%s' "${CI:-}" | tr '[:upper:]' '[:lower:]')"
if [ "$ci_normalized" = "1" ] || [ "$ci_normalized" = "true" ]; then
    cmd+=(--reporter-cli-no-failures false)
fi

echo -e "Collection: ${YELLOW}$COLLECTION${NC}"
echo -e "Reports:    ${YELLOW}$REPORT_DIR${NC}"
if [ -n "$SEED_ENV_PATH" ]; then
    echo -e "Seed env:   ${YELLOW}$SEED_ENV_PATH${NC}"
fi
echo ""
echo -e "${GREEN}Running tests...${NC}"
echo ""

set +e
"${cmd[@]}" --reporter-htmlextra-export "$REPORT_DIR/report.html" --reporter-htmlextra-title "Bifrost Routing Wiring"
EXIT_CODE=$?
set -e

echo ""
if [ $EXIT_CODE -eq 0 ]; then
    echo -e "${GREEN}✓ All tests passed!${NC}"
else
    echo -e "${RED}✗ Some tests failed${NC}"
fi
echo -e "Report: ${YELLOW}$REPORT_DIR/report.html${NC}"
exit $EXIT_CODE
