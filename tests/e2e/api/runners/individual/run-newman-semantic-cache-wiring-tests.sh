#!/bin/bash

# Bifrost Semantic Cache Wiring Test Runner
#
# Boots one clean Bifrost per vector store (Redis, Weaviate, pgvector, or several at once), each on
# its own temp app dir with a SQLite config store, and runs
# collections/bifrost-semantic-cache-wiring.postman_collection.json against it. The logs store is
# SQLite too, except for pgvector, which keeps its vectors in the logs store's Postgres database:
# that run gets a fresh database, created on start and dropped on exit. With --store both (Redis
# and Weaviate) or all (all three) the newman runs go concurrently and the runner fails if any does.
#
# Live and paid: the collection calls OpenAI (chat, responses, embeddings, speech, whisper,
# gpt-image-1.5), Anthropic, Gemini, Cohere, OpenRouter and Bedrock. Keys are never written into the
# collection: every provider key is env.<NAME>, resolved from this process's environment by the
# server it boots. The collection is generated - edit runners/build-semantic-cache-wiring.mjs,
# not the JSON.
#
# Requires: a built bifrost-http binary, newman, and reachable Redis Stack, Weaviate and/or
# Postgres with the pgvector extension available (tests/docker-compose.yml provides all three),
# plus psql for pgvector.

set -e

CALLER_DIR="$PWD"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
API_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
cd "$API_DIR"

COLLECTION="collections/bifrost-semantic-cache-wiring.postman_collection.json"
REPORT_ROOT="newman-reports"

GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m'

BIFROST_BINARY=""
PORT="8096"
STORE="both"
REDIS_ADDR="${REDIS_ADDR:-localhost:6379}"
WEAVIATE_HOST="${WEAVIATE_HOST:-localhost:9000}"
PGVECTOR_HOST="${PGVECTOR_HOST:-localhost}"
PGVECTOR_PORT="${PGVECTOR_PORT:-5432}"
PGVECTOR_USER="${PGVECTOR_USER:-bifrost}"
PGVECTOR_PASSWORD="${PGVECTOR_PASSWORD:-bifrost_password}"
PGVECTOR_ADMIN_DB="${PGVECTOR_ADMIN_DB:-bifrost}"
REPORTERS="cli"
VERBOSE=""
FOLDERS=()
# The folders whose saved request ids and responses the O folder reads.
O_INPUT_FOLDERS=("D: direct cache on chat completions" "X: every other request type" "S: semantic mode")

folder_selected() {
    local want="$1" f
    for f in "${FOLDERS[@]}"; do
        [ "$f" = "$want" ] && return 0
    done
    return 1
}

# Every provider the collection creates a key for. A missing one fails the run up front rather
# than letting the rows that need it fail one by one.
REQUIRED_ENV=(OPENAI_API_KEY ANTHROPIC_API_KEY GEMINI_API_KEY COHERE_API_KEY OPENROUTER_API_KEY AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY AWS_REGION)

while [[ $# -gt 0 ]]; do
    case "$1" in
        --binary) BIFROST_BINARY="$2"; shift 2 ;;
        --port) PORT="$2"; shift 2 ;;
        --store) STORE="$2"; shift 2 ;;
        --folder) FOLDERS+=("$2"); shift 2 ;;
        --html) REPORTERS="${REPORTERS},htmlextra"; shift ;;
        --json) REPORTERS="${REPORTERS},json"; shift ;;
        --verbose) VERBOSE="--verbose"; shift ;;
        --help)
            echo "Usage: $0 --binary <path-to-bifrost-http> [OPTIONS]"
            echo ""
            echo "Options:"
            echo "  --binary <path>   Path to a built bifrost-http binary (required)"
            echo "  --port <port>     First port to boot on (default: 8096). Each store takes the next one, in the"
            echo "                    order redis, weaviate, pgvector"
            echo "  --store <name>    redis, weaviate, pgvector, both (redis and weaviate) or all"
            echo "                    (default: both; several stores run concurrently)"
            echo "  --folder <name>   Run only this collection folder (repeatable). Setup and P (providers) always run;"
            echo "                    O (logs) also runs D, X and S, whose requests it reads"
            echo "  --html            Generate HTML reports"
            echo "  --json            Generate JSON reports"
            echo "  --verbose         Show detailed Newman output"
            echo "  --help            Show this help message"
            echo ""
            echo "Environment:"
            echo "  ${REQUIRED_ENV[*]}"
            echo "                    provider credentials, all required"
            echo "  REDIS_ADDR        default localhost:6379 (Redis Stack, for the search module)"
            echo "  WEAVIATE_HOST     default localhost:9000 (HTTP only)"
            echo "  PGVECTOR_HOST, PGVECTOR_PORT, PGVECTOR_USER, PGVECTOR_PASSWORD"
            echo "                    default localhost, 5432, bifrost, bifrost_password: a Postgres with"
            echo "                    pgvector 0.8.0 or later available, where the user may create databases"
            echo "  PGVECTOR_ADMIN_DB default bifrost: the database psql connects to for CREATE/DROP DATABASE"
            exit 0 ;;
        *) echo -e "${RED}Unknown option: $1${NC}"; exit 1 ;;
    esac
done

echo -e "${GREEN}==============================================${NC}"
echo -e "${GREEN}Bifrost Semantic Cache Wiring Test Runner${NC}"
echo -e "${GREEN}==============================================${NC}"
echo ""

case "$STORE" in
    redis) STORES=(redis) ;;
    weaviate) STORES=(weaviate) ;;
    pgvector) STORES=(pgvector) ;;
    both) STORES=(redis weaviate) ;;
    all) STORES=(redis weaviate pgvector) ;;
    *) echo -e "${RED}Error: --store must be redis, weaviate, pgvector, both or all${NC}"; exit 1 ;;
esac
uses_pgvector() { [[ " ${STORES[*]} " == *" pgvector "* ]]; }

if ! command -v newman &>/dev/null; then
    echo -e "${RED}Error: Newman is not installed${NC}"
    echo "Install it with: npm install -g newman newman-reporter-htmlextra"
    exit 1
fi
if [ -n "$BIFROST_BINARY" ] && [[ "$BIFROST_BINARY" != /* ]]; then
    BIFROST_BINARY="$CALLER_DIR/$BIFROST_BINARY"
fi
if [ -z "$BIFROST_BINARY" ] || [ ! -x "$BIFROST_BINARY" ]; then
    echo -e "${RED}Error: --binary must point to an executable bifrost-http binary${NC}"
    exit 1
fi
if uses_pgvector && ! command -v psql &>/dev/null; then
    echo -e "${RED}Error: --store $STORE needs psql, to create and drop the pgvector run's database${NC}"
    exit 1
fi
if [ ! -f "$COLLECTION" ]; then
    echo -e "${RED}Error: Collection file not found: $COLLECTION${NC}"
    exit 1
fi
missing=()
for name in "${REQUIRED_ENV[@]}"; do
    [ -z "${!name:-}" ] && missing+=("$name")
done
if [ ${#missing[@]} -gt 0 ]; then
    echo -e "${RED}Error: missing provider credentials: ${missing[*]}${NC}"
    exit 1
fi

port_in_use() {
    (command -v nc &>/dev/null && nc -z 127.0.0.1 "$1" 2>/dev/null) \
        || (echo >/dev/tcp/127.0.0.1/"$1") 2>/dev/null
}

WORK_ROOT="$(mktemp -d)"
SERVER_PIDS=()
NEWMAN_PIDS=()
# A prefix per runner invocation: every run id, and so every vector store namespace, starts with
# it, which is how cleanup finds this run's Weaviate classes. The PID keeps two runners started
# in the same second from sharing it, and so from deleting each other's classes.
RUN_SEED="scw$(date +%s)$$"
SETUP_TOKEN="scw-setup-$(date +%s)-$RANDOM"
# The pgvector run's own database, so its logs and vector tables start empty like the SQLite runs.
PGVECTOR_DB="${RUN_SEED}_pgvector"
PGVECTOR_DB_CREATED=""

# with_deadline runs a command and kills it if it is still running after $1 seconds, returning
# non-zero then. A connect timeout alone does not bound a session that connected and then stopped
# answering, and macOS has no timeout(1).
with_deadline() {
    local secs="$1" pid watcher rc
    shift
    "$@" &
    pid=$!
    ( sleep "$secs"; kill "$pid" 2>/dev/null ) &
    watcher=$!
    wait "$pid" 2>/dev/null
    rc=$?
    kill "$watcher" 2>/dev/null || true
    wait "$watcher" 2>/dev/null || true
    return "$rc"
}

# pg_admin runs one statement against the admin database, giving up after 5s to connect and 15s
# overall, so an unresponsive Postgres cannot hang the runner on start or exit.
pg_admin() {
    with_deadline 15 env PGPASSWORD="$PGVECTOR_PASSWORD" PGCONNECT_TIMEOUT=5 psql -h "$PGVECTOR_HOST" \
        -p "$PGVECTOR_PORT" -U "$PGVECTOR_USER" -d "$PGVECTOR_ADMIN_DB" -v ON_ERROR_STOP=1 -qAtc "$1"
}

cleanup() {
    # Stop the newman runs before their servers. Bash starts background jobs ignoring SIGINT when
    # job control is off, so after a Ctrl-C they would otherwise outlive the runner. A run already
    # waited for has its entry cleared, so its reused PID is never signalled.
    for pid in "${NEWMAN_PIDS[@]}"; do
        [ -n "$pid" ] || continue
        kill "$pid" 2>/dev/null || true
        wait "$pid" 2>/dev/null || true
    done
    for pid in "${SERVER_PIDS[@]}"; do
        if kill -0 "$pid" 2>/dev/null; then
            kill "$pid" 2>/dev/null || true
            wait "$pid" 2>/dev/null || true
        fi
    done
    # Weaviate keeps a class per namespace; drop the ones this run created. Best effort, and
    # time-limited so an unresponsive Weaviate cannot hold the exit up.
    if [[ " ${STORES[*]} " == *" weaviate "* ]] && command -v jq &>/dev/null; then
        for class in $(curl -s --connect-timeout 5 --max-time 15 "http://$WEAVIATE_HOST/v1/schema" 2>/dev/null | jq -r '.classes[]?.class' 2>/dev/null | grep -i "$RUN_SEED" || true); do
            curl -s --connect-timeout 5 --max-time 15 -o /dev/null -X DELETE "http://$WEAVIATE_HOST/v1/schema/$class" 2>/dev/null || true
        done
    fi
    # Drop the pgvector run's database once its server is gone. FORCE ends any connection left over.
    # Best effort and time-limited, like the Weaviate cleanup above.
    if [ -n "$PGVECTOR_DB_CREATED" ]; then
        pg_admin "DROP DATABASE IF EXISTS \"$PGVECTOR_DB\" WITH (FORCE)" >/dev/null 2>&1 || true
    fi
    rm -rf "$WORK_ROOT"
}
trap cleanup EXIT

# json_str prints its argument as a JSON string literal, escaping backslashes, quotes and the
# control characters a shell value can carry, so a password with " or \ stays valid JSON and
# decodes to itself.
json_str() {
    local s="$1"
    s="${s//\\/\\\\}"
    s="${s//\"/\\\"}"
    s="${s//$'\n'/\\n}"
    s="${s//$'\r'/\\r}"
    s="${s//$'\t'/\\t}"
    printf '"%s"' "$s"
}

vector_store_block() {
    case "$1" in
        redis) printf '%s\n' "{ \"enabled\": true, \"type\": \"redis\", \"config\": { \"addr\": $(json_str "$REDIS_ADDR") } }" ;;
        weaviate) printf '%s\n' "{ \"enabled\": true, \"type\": \"weaviate\", \"config\": { \"scheme\": \"http\", \"host\": $(json_str "$WEAVIATE_HOST") } }" ;;
        pgvector) printf '%s\n' "{ \"enabled\": true, \"type\": \"pgvector\" }" ;;
    esac
}

# pgvector keeps its vectors in the logs store's database, so its run logs to Postgres.
logs_store_block() {
    local dir="$1" store="$2"
    if [ "$store" = "pgvector" ]; then
        printf '%s\n' "{ \"enabled\": true, \"type\": \"postgres\", \"config\": { \"host\": $(json_str "$PGVECTOR_HOST"), \"port\": $(json_str "$PGVECTOR_PORT"), \"user\": $(json_str "$PGVECTOR_USER"), \"password\": $(json_str "$PGVECTOR_PASSWORD"), \"db_name\": $(json_str "$PGVECTOR_DB"), \"ssl_mode\": \"disable\" } }"
    else
        printf '%s\n' "{ \"enabled\": true, \"type\": \"sqlite\", \"config\": { \"path\": $(json_str "$dir/logs.db") } }"
    fi
}

write_config() {
    local dir="$1" store="$2"
    cat > "$dir/config.json" <<EOF
{
  "\$schema": "https://www.getbifrost.ai/schema",
  "client": {
    "drop_excess_requests": false,
    "initial_pool_size": 50,
    "allowed_origins": ["*"],
    "enable_logging": true,
    "enforce_auth_on_inference": false,
    "max_request_body_size_mb": 100
  },
  "config_store": { "enabled": true, "type": "sqlite", "config": { "path": "$dir/config.db" } },
  "logs_store": $(logs_store_block "$dir" "$store"),
  "vector_store": $(vector_store_block "$store")
}
EOF
}

start_bifrost() {
    local store="$1" port="$2"
    local dir="$WORK_ROOT/$store"
    mkdir -p "$dir"
    if [ "$store" = "pgvector" ]; then
        if ! pg_admin "CREATE DATABASE \"$PGVECTOR_DB\""; then
            echo -e "${RED}[$store] could not create database $PGVECTOR_DB on $PGVECTOR_HOST:$PGVECTOR_PORT${NC}"; exit 1
        fi
        PGVECTOR_DB_CREATED=1
    fi
    write_config "$dir" "$store"
    if port_in_use "$port"; then
        echo -e "${RED}Error: port $port is already in use; pass --port to use another${NC}"
        exit 1
    fi
    # Inference runs without auth on live provider keys, so the server must not inherit a wider
    # BIFROST_HOST (such as 0.0.0.0) from the caller's environment.
    BIFROST_SETUP_TOKEN="$SETUP_TOKEN" "$BIFROST_BINARY" --app-dir "$dir" --host 127.0.0.1 --port "$port" --log-level info > "$dir/server.log" 2>&1 &
    local pid=$!
    SERVER_PIDS+=("$pid")
    local elapsed=0
    while [ $elapsed -lt 90 ]; do
        grep -q "successfully started bifrost" "$dir/server.log" 2>/dev/null && break
        if ! kill -0 "$pid" 2>/dev/null; then
            echo -e "${RED}[$store] server exited before becoming ready${NC}"; cat "$dir/server.log"; exit 1
        fi
        sleep 1; elapsed=$((elapsed + 1))
    done
    if [ $elapsed -ge 90 ]; then
        echo -e "${RED}[$store] server did not start within 90s${NC}"; cat "$dir/server.log"; exit 1
    fi
    echo -e "${GREEN}[$store] Bifrost ready on :$port${NC}"
}

run_newman() {
    local store="$1" port="$2"
    local report_dir="$REPORT_ROOT/semantic-cache-wiring-$store"
    mkdir -p "$report_dir"
    local cmd=(newman run "$COLLECTION"
        --env-var "base_url=http://localhost:$port"
        --env-var "setup_token=$SETUP_TOKEN"
        --env-var "e2e_seed_prefix=$RUN_SEED-$store"
        --timeout-script 120000 --timeout-request 600000
        -r "$REPORTERS")
    if [ ${#FOLDERS[@]} -gt 0 ]; then
        cmd+=(--folder "Setup: clear providers" --folder "P: preconditions and providers")
        for f in "${FOLDERS[@]}"; do
            # O reads the request ids and responses D, X and S saved, so it brings them along.
            if [ "$f" = "O: logs" ]; then
                for input in "${O_INPUT_FOLDERS[@]}"; do
                    folder_selected "$input" || cmd+=(--folder "$input")
                done
            fi
            cmd+=(--folder "$f")
        done
    fi
    [[ "$REPORTERS" == *"htmlextra"* ]] && cmd+=(--reporter-htmlextra-export "$report_dir/report.html" --reporter-htmlextra-title "Bifrost Semantic Cache Wiring ($store)")
    [[ "$REPORTERS" == *"json"* ]] && cmd+=(--reporter-json-export "$report_dir/report.json")
    [ -n "$VERBOSE" ] && cmd+=("$VERBOSE")
    # exec, so the PID the runner records for this run is newman's own and cleanup can stop it.
    exec "${cmd[@]}" > "$report_dir/newman.log" 2>&1
}

for i in "${!STORES[@]}"; do
    start_bifrost "${STORES[$i]}" "$((PORT + i))"
done

echo ""
echo -e "${GREEN}Running newman against: ${STORES[*]}${NC}"
for i in "${!STORES[@]}"; do
    run_newman "${STORES[$i]}" "$((PORT + i))" &
    NEWMAN_PIDS+=("$!")
done

EXIT_CODE=0
for i in "${!STORES[@]}"; do
    store="${STORES[$i]}"
    report_dir="$REPORT_ROOT/semantic-cache-wiring-$store"
    set +e
    wait "${NEWMAN_PIDS[$i]}"
    rc=$?
    NEWMAN_PIDS[$i]=""
    set -e
    echo ""
    echo -e "${YELLOW}========== $store ==========${NC}"
    cat "$report_dir/newman.log"
    if [ $rc -eq 0 ]; then
        echo -e "${GREEN}✓ [$store] all semantic cache checks passed${NC}"
    else
        EXIT_CODE=1
        cp "$WORK_ROOT/$store/server.log" "$report_dir/server.log" 2>/dev/null || true
        echo -e "${RED}✗ [$store] some semantic cache checks failed${NC}"
        echo -e "Server log saved to: ${YELLOW}$report_dir/server.log${NC}"
    fi
done

exit $EXIT_CODE
