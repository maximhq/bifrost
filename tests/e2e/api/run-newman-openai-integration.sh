#!/bin/bash

# Bifrost OpenAI Integration API Newman Test Runner
# This script runs the OpenAI integration API test suite using Newman

set -e

# Run from script directory so paths to collection and provider-capabilities.json work
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$SCRIPT_DIR"

# Configuration
COLLECTION="bifrost-openai-integration.postman_collection.json"
ENVIRONMENT="bifrost-v1.postman_environment.json"
REPORT_DIR="newman-reports/openai-integration"
PROVIDER_CONFIG_DIR="provider_config"
PROVIDER_CAPABILITIES_JSON="provider-capabilities.json"

# Colors for output
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

# Detect if --env was passed (so we run single provider vs all providers)
PROVIDER_ENV_FILE=""
ARGS=()
while [[ $# -gt 0 ]]; do
    if [[ "$1" == "--env" ]]; then
        if [[ -z "${2:-}" || "${2:-}" == --* ]]; then
            echo -e "${RED}Error: --env requires a value${NC}"
            exit 1
        fi
        PROVIDER_ENV_FILE="$2"
        shift 2
    else
        ARGS+=("$1")
        shift
    fi
done
set -- "${ARGS[@]}"

# Normalize CI for retry logic (accept 1 or true, case-insensitive)
ci_normalized="$(printf '%s' "${CI:-}" | tr '[:upper:]' '[:lower:]')"

# Print banner
echo -e "${GREEN}========================================${NC}"
if [ "$ci_normalized" = "1" ] || [ "$ci_normalized" = "true" ]; then
    echo -e "${GREEN}Bifrost OpenAI Integration API Test Runner with retries: 10${NC}"
else
    echo -e "${GREEN}Bifrost OpenAI Integration API Test Runner${NC}"
fi
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

# Check if environment exists
if [ ! -f "$ENVIRONMENT" ]; then
    echo -e "${YELLOW}Warning: Environment file not found: $ENVIRONMENT${NC}"
    echo "Using collection variables only"
    ENV_FLAG=""
else
    ENV_FLAG="-e $ENVIRONMENT"
fi

# Create report directory
mkdir -p "$REPORT_DIR"

# Load provider capabilities from provider-capabilities.json (single source of truth) into a Newman globals file
if [ ! -f "$PROVIDER_CAPABILITIES_JSON" ]; then
    echo -e "${RED}Error: $PROVIDER_CAPABILITIES_JSON not found${NC}"
    exit 1
fi
if ! command -v jq &>/dev/null; then
    echo -e "${RED}Error: jq is required to load $PROVIDER_CAPABILITIES_JSON${NC}"
    exit 1
fi
GLOBALS_TMP=$(mktemp)
trap 'rm -f "$GLOBALS_TMP"' EXIT
jq -n --rawfile cap "$PROVIDER_CAPABILITIES_JSON" '{id: "bifrost-provider-capabilities", name: "Provider capabilities", values: [{key: "provider_capabilities", value: $cap, type: "default", enabled: true}]}' > "$GLOBALS_TMP"

# When no --env: resolve list of provider Postman env .json files (sorted), excluding sgl and ollama
EXCLUDED_PROVIDERS="sgl ollama"
if [ -z "$PROVIDER_ENV_FILE" ] && [ -d "$PROVIDER_CONFIG_DIR" ]; then
    PROVIDER_JSON_FILES=()
    while IFS= read -r -d '' f; do
        # basename: bifrost-v1-openai.postman_environment.json -> openai
        name="${f##*/}"
        name="${name#bifrost-v1-}"
        name="${name%.postman_environment.json}"
        skip=""
        for ex in $EXCLUDED_PROVIDERS; do
            if [ "$name" = "$ex" ]; then skip=1; break; fi
        done
        [ -z "$skip" ] && PROVIDER_JSON_FILES+=("$f")
    done < <(find "$PROVIDER_CONFIG_DIR" -maxdepth 1 -name "bifrost-v1-*.postman_environment.json" -print0 2>/dev/null | sort -z)
fi

# Parse command line arguments
FOLDER=""
VERBOSE="--verbose"  # Enable verbose by default to capture console.log statements
REPORTERS="cli"
BAIL=""

while [[ $# -gt 0 ]]; do
    case $1 in
        --folder)
            if [[ -z "${2:-}" || "${2:-}" == --* ]]; then
                echo -e "${RED}Error: --folder requires a value${NC}"
                exit 1
            fi
            FOLDER="$2"
            shift 2
            ;;
        --verbose)
            VERBOSE="--verbose"
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
            echo "  --folder <name>     Run only tests in specified folder"
            echo "  --verbose           Show detailed output"
            echo "  --html              Generate HTML report"
            echo "  --json              Generate JSON report"
            echo "  --all-reports       Generate all report types"
            echo "  --bail              Stop on first failure"
            echo "  --env <path>        Postman env .json path or provider name (e.g. provider_config/bifrost-v1-openai.postman_environment.json or openai)"
            echo "  --help              Show this help message"
            echo ""
            echo "Environment Variables:"
            echo "  BIFROST_BASE_URL    Override base URL (default: http://localhost:8080)"
            echo "  BIFROST_PROVIDER    Override provider (default: openai)"
            echo "  BIFROST_MODEL       Override model name (default: gpt-4o)"
            echo "  BIFROST_RESPONSES_MODEL  Override Responses API model (default: BIFROST_MODEL)"
            echo "  BIFROST_EMBEDDING_MODEL    Override embedding model (default: text-embedding-3-small)"
            echo "  BIFROST_SPEECH_MODEL       Override speech model (default: tts-1)"
            echo "  BIFROST_TRANSCRIPTION_MODEL  Override transcription model (default: whisper-1)"
            echo "  BIFROST_IMAGE_MODEL        Override image model (default: dall-e-3)"
            echo ""
            echo "Examples:"
            echo "  $0                                    # Run collection for all providers (each provider_config/bifrost-v1-*.postman_environment.json)"
            echo "  $0 --env openai                       # Run once with OpenAI provider only"
            echo "  $0 --folder \"Chat Completions\"       # Run specific folder"
            echo "  $0 --html --verbose                   # Verbose with HTML report"
            echo "  BIFROST_BASE_URL=http://api:8080 $0  # Custom base URL"
            exit 0
            ;;
        *)
            echo -e "${RED}Unknown option: $1${NC}"
            echo "Use --help for usage information"
            exit 1
            ;;
    esac
done

# Build and run Newman once.
# Optional second arg: path to Postman env .json file (e.g. provider_config/bifrost-v1-openai.postman_environment.json).
# When given, uses only that env file; otherwise uses default env and BIFROST_* overrides.
run_newman() {
    local -a cmd=(newman run "$COLLECTION" -g "$GLOBALS_TMP")
    if [ -n "${2:-}" ] && [ -f "${2}" ]; then
        cmd+=(-e "${2}")
    else
        local base_url="${BIFROST_BASE_URL:-http://localhost:8080}"
        local provider="${BIFROST_PROVIDER:-openai}"
        local model="${BIFROST_MODEL:-gpt-4o}"
        local responses_model="${BIFROST_RESPONSES_MODEL:-$model}"
        local embedding_model="${BIFROST_EMBEDDING_MODEL:-text-embedding-3-small}"
        local speech_model="${BIFROST_SPEECH_MODEL:-tts-1}"
        local transcription_model="${BIFROST_TRANSCRIPTION_MODEL:-whisper-1}"
        local image_model="${BIFROST_IMAGE_MODEL:-dall-e-3}"
        if [ -n "$ENV_FLAG" ]; then
            cmd+=(-e "$ENVIRONMENT")
        fi
        cmd+=(--env-var "base_url=$base_url" --env-var "provider=$provider" --env-var "model=$model" --env-var "responses_model=$responses_model" --env-var "embedding_model=$embedding_model" --env-var "speech_model=$speech_model" --env-var "transcription_model=$transcription_model" --env-var "image_model=$image_model")
    fi
    [ -n "$FOLDER" ] && cmd+=(--folder "$FOLDER")
    cmd+=(--timeout-script 300000)
    cmd+=(-r "$REPORTERS")
    if [[ "$REPORTERS" == *"html"* ]]; then
        cmd+=(--reporter-html-export "$REPORT_DIR/report_${1:-run}.html")
    fi
    if [[ "$REPORTERS" == *"json"* ]]; then
        cmd+=(--reporter-json-export "$REPORT_DIR/report_${1:-run}.json")
    fi
    [ -n "$VERBOSE" ] && cmd+=("$VERBOSE")
    [ -n "$BAIL" ] && cmd+=("$BAIL")
    if [ "$ci_normalized" = "1" ] || [ "$ci_normalized" = "true" ]; then
        cmd+=(--env-var "CI=1")
    fi

    "${cmd[@]}"
}

# Post-process log file to pretty-print JSON blocks using jq
post_process_log() {
    local input_file="$1"
    local output_file="$2"
    
    if [ ! -f "$input_file" ]; then
        return 1
    fi
    if ! command -v python3 >/dev/null 2>&1; then
        cp "$input_file" "$output_file"
        return 0
    fi
    
    python3 - "$input_file" "$output_file" << 'PYTHON_SCRIPT'
import sys
import json
import subprocess
import shutil

def format_json_with_jq(json_text):
    """Format JSON using jq if available, otherwise use Python's json module"""
    if shutil.which('jq'):
        try:
            process = subprocess.Popen(
                ['jq', '.'],
                stdin=subprocess.PIPE,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                text=True
            )
            stdout, stderr = process.communicate(input=json_text)
            if process.returncode == 0:
                return stdout
        except Exception:
            pass
    # Fallback to Python's json module
    try:
        parsed = json.loads(json_text)
        return json.dumps(parsed, indent=2)
    except (json.JSONDecodeError, ValueError):
        return json_text

def process_log_file(input_file, output_file):
    """Process log file and format JSON blocks"""
    with open(input_file, 'r', encoding='utf-8', errors='ignore') as f_in:
        with open(output_file, 'w', encoding='utf-8') as f_out:
            in_json_block = False
            json_lines = []
            
            for line in f_in:
                # Check if we're entering a JSON block
                if 'REQUEST BODY:' in line or 'RESPONSE BODY:' in line:
                    if in_json_block and json_lines:
                        # Format previous JSON block
                        json_text = ''.join(json_lines).strip()
                        formatted = format_json_with_jq(json_text)
                        f_out.write(formatted)
                        if not formatted.endswith('\n'):
                            f_out.write('\n')
                        json_lines = []
                    in_json_block = True
                    f_out.write(line)
                    continue
                
                # Check if we're exiting a JSON block
                if in_json_block:
                    stripped = line.strip()
                    if not stripped or stripped.startswith('=') or line.startswith('REQUEST:') or line.startswith('RESPONSE:'):
                        # End of JSON block, format and write
                        if json_lines:
                            json_text = ''.join(json_lines).strip()
                            formatted = format_json_with_jq(json_text)
                            f_out.write(formatted)
                            if not formatted.endswith('\n'):
                                f_out.write('\n')
                        json_lines = []
                        in_json_block = False
                    else:
                        json_lines.append(line)
                        continue
                
                f_out.write(line)
            
            # Handle case where file ends in a JSON block
            if json_lines:
                json_text = ''.join(json_lines).strip()
                formatted = format_json_with_jq(json_text)
                f_out.write(formatted)
                if not formatted.endswith('\n'):
                    f_out.write('\n')

if __name__ == '__main__':
    if len(sys.argv) < 3:
        print(f"Error: Expected 2 arguments, got {len(sys.argv) - 1}", file=sys.stderr)
        sys.exit(1)
    try:
        process_log_file(sys.argv[1], sys.argv[2])
    except Exception as e:
        print(f"Error processing log file: {e}", file=sys.stderr)
        sys.exit(1)
PYTHON_SCRIPT
}

# Run for a single provider (--env was passed: path to .json env or provider name)
if [ -n "$PROVIDER_ENV_FILE" ]; then
    SINGLE_JSON_ENV=""
    if [ -f "$PROVIDER_ENV_FILE" ]; then
        SINGLE_JSON_ENV="$PROVIDER_ENV_FILE"
    elif [ -f "$PROVIDER_CONFIG_DIR/$PROVIDER_ENV_FILE" ]; then
        SINGLE_JSON_ENV="$PROVIDER_CONFIG_DIR/$PROVIDER_ENV_FILE"
    elif [ -f "$PROVIDER_CONFIG_DIR/bifrost-v1-${PROVIDER_ENV_FILE}.postman_environment.json" ]; then
        SINGLE_JSON_ENV="$PROVIDER_CONFIG_DIR/bifrost-v1-${PROVIDER_ENV_FILE}.postman_environment.json"
    fi
    if [ -z "$SINGLE_JSON_ENV" ]; then
        echo -e "${RED}Error: Env file not found: $PROVIDER_ENV_FILE${NC}"
        echo "Use a path to a .json env (e.g. provider_config/bifrost-v1-openai.postman_environment.json) or provider name (e.g. openai)"
        exit 1
    fi
    SINGLE_PROVIDER_NAME="${SINGLE_JSON_ENV##*/}"
    SINGLE_PROVIDER_NAME="${SINGLE_PROVIDER_NAME#bifrost-v1-}"
    SINGLE_PROVIDER_NAME="${SINGLE_PROVIDER_NAME%.postman_environment.json}"
    echo -e "Configuration: ${YELLOW}$SINGLE_JSON_ENV${NC}"
    echo -e "  Reports:  ${YELLOW}$REPORT_DIR${NC}"
    echo ""
    echo -e "${GREEN}Running tests...${NC}"
    echo ""
    TEMP_LOG="$REPORT_DIR/${SINGLE_PROVIDER_NAME}.log.tmp"
    set +e
    run_newman "$SINGLE_PROVIDER_NAME" "$SINGLE_JSON_ENV" > "$TEMP_LOG" 2>&1
    EXIT_CODE=$?
    set -e
    LOG_FILE="$REPORT_DIR/${SINGLE_PROVIDER_NAME}.log"
    post_process_log "$TEMP_LOG" "$LOG_FILE" || cp "$TEMP_LOG" "$LOG_FILE"
    rm -f "$TEMP_LOG"
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
    exit $EXIT_CODE
fi

# Run for all providers (no --env)
if [ -z "${PROVIDER_JSON_FILES+x}" ] || [ ${#PROVIDER_JSON_FILES[@]} -eq 0 ]; then
    echo -e "${YELLOW}No provider env .json files found in $PROVIDER_CONFIG_DIR/. Using default (openai).${NC}"
    echo -e "Configuration:"
    echo -e "  Base URL: ${YELLOW}${BIFROST_BASE_URL:-http://localhost:8080}${NC}"
    echo -e "  Provider: ${YELLOW}${BIFROST_PROVIDER:-openai}${NC}"
    echo -e "  Reports:  ${YELLOW}$REPORT_DIR${NC}"
    echo ""
    echo -e "${GREEN}Running tests...${NC}"
    echo ""
    TEMP_LOG="$REPORT_DIR/default.log.tmp"
    set +e
    run_newman > "$TEMP_LOG" 2>&1
    EXIT_CODE=$?
    set -e
    LOG_FILE="$REPORT_DIR/default.log"
    post_process_log "$TEMP_LOG" "$LOG_FILE" || cp "$TEMP_LOG" "$LOG_FILE"
    rm -f "$TEMP_LOG"
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
    exit $EXIT_CODE
fi

PARALLEL_LOGS_DIR="$REPORT_DIR/parallel_logs"
mkdir -p "$PARALLEL_LOGS_DIR"

# Print a one-line report for a provider from its Newman log and exit code
print_provider_report() {
    local name="$1"
    local logfile="$2"
    local exitcode="$3"
    local failed_count=""
    local failed_tests=""
    if [ -f "$logfile" ]; then
        # Parse Newman summary table: assertions row, third column = failed count
        failed_count=$(grep "assertions" "$logfile" 2>/dev/null | awk -F'│' '{gsub(/^ *| *$/,"",$4); print $4}' | head -1)
        # Lines with "  ✗  " are failed assertions; strip to get test name
        failed_tests=$(grep "  ✗  " "$logfile" 2>/dev/null | sed 's/.*✗  */  - /' | sed 's/^ *//' | tr '\n' ' ' | sed 's/ $//')
    fi
    if [ "$exitcode" -eq 0 ]; then
        echo -e "${GREEN}  ✓ $name: PASS${NC}"
    else
        echo -e "${RED}  ✗ $name: FAIL${NC}"
        if [ -n "$failed_count" ] && [ "$failed_count" -gt 0 ] 2>/dev/null; then
            echo -e "    ${RED}${failed_count} assertion(s) failed${NC}"
        fi
        if [ -n "$failed_tests" ]; then
            echo -e "    Failed: $failed_tests"
        fi
    fi
}

# Draw the provider status table (TABLE_LINES lines). Use after moving cursor up TABLE_LINES to refresh.
draw_table() {
    printf '\033[2K%-16s  %s\n' "Provider" "Status"
    for i in "${!NAMES[@]}"; do
        printf '\033[2K%-16s  %b\n' "${NAMES[$i]}" "${STATUS[$i]}"
    done
}

echo -e "Running tests for ${#PROVIDER_JSON_FILES[@]} provider(s) ${GREEN}in parallel${NC}. Reports: ${YELLOW}$REPORT_DIR${NC}"
echo ""

# Run each provider in a background subshell; capture PID and log path per provider
PIDS=()
NAMES=()
LOG_FILES=()
for jsonfile in "${PROVIDER_JSON_FILES[@]}"; do
    name="${jsonfile##*/}"
    name="${name#bifrost-v1-}"
    name="${name%.postman_environment.json}"
    logfile="$PARALLEL_LOGS_DIR/${name}.log"
    temp_logfile="${logfile}.tmp"
    LOG_FILES+=("$logfile")
    NAMES+=("$name")
    ( set +e; run_newman "$name" "$jsonfile" > "$temp_logfile" 2>&1; ec=$?; set -e; post_process_log "$temp_logfile" "$logfile" || cp "$temp_logfile" "$logfile"; rm -f "$temp_logfile"; exit $ec ) &
    PIDS+=($!)
done

# Status for each provider: Pending, ✓ PASS, or ✗ FAIL (with color)
STATUS=()
for i in "${!PIDS[@]}"; do STATUS[$i]="${YELLOW}Pending${NC}"; done
TABLE_LINES=$((${#NAMES[@]} + 1))

# Initial table
draw_table

# Track which we've reaped (0 = pending, 1 = done)
REAPED=()
for i in "${!PIDS[@]}"; do REAPED[$i]=0; done

OVERALL_FAILED=0
FAILED_NAMES=()

# As each provider finishes, update status and redraw table
while true; do
    all_done=1
    for i in "${!PIDS[@]}"; do
        [ "${REAPED[$i]:-0}" -eq 1 ] && continue
        all_done=0
        if ! kill -0 "${PIDS[$i]}" 2>/dev/null; then
            exitcode=0; wait "${PIDS[$i]}" || exitcode=$?
            REAPED[$i]=1
            if [ "$exitcode" -eq 0 ]; then
                STATUS[$i]="${GREEN}✓ PASS${NC}"
            else
                OVERALL_FAILED=1
                FAILED_NAMES+=("${NAMES[$i]}")
                STATUS[$i]="${RED}✗ FAIL${NC}"
            fi
            # Move cursor up and redraw table
            printf '\033[%dA' "$TABLE_LINES"
            draw_table
        fi
    done
    [ "$all_done" -eq 1 ] && break
    sleep 0.3
done

echo -e "${GREEN}========================================${NC}"
if [ $OVERALL_FAILED -eq 0 ]; then
    echo -e "${GREEN}✓ All providers passed!${NC}"
else
    echo -e "${RED}✗ One or more providers had failures: ${FAILED_NAMES[*]}${NC}"
fi
if [[ "$REPORTERS" == *"html"* ]] || [[ "$REPORTERS" == *"json"* ]]; then
    echo ""
    echo -e "Reports saved to: ${YELLOW}$REPORT_DIR${NC}"
    ls -lh "$REPORT_DIR" 2>/dev/null | tail -n +2
fi
# Parallel logs persist in $PARALLEL_LOGS_DIR (overwritten per provider on each run)
exit $OVERALL_FAILED
