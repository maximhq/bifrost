#!/bin/bash

# Bifrost Warp Newman Test Runner
#
# Boots a fresh Bifrost with the "warp" feature flag on, backed by a throwaway
# Postgres database and a Weaviate vector store, seeds it with
# tests/cmd/seed/warpseed, and runs
# collections/bifrost-v1-warp.postman_collection.json against it.
#
# Warp answers questions about every row in the logs table, so the suite never
# shares a database: -reset-db drops and recreates it before the server boots,
# and nothing else writes to it except Warp's own model calls.
#
# Live and paid: Warp's agent loop and its embeddings call OpenAI, through
# env.OPENAI_API_KEY or - with WARP_UPSTREAM_BIFROST=http://localhost:8080 -
# through an already-running Bifrost that holds its own OpenAI key. The
# collection is generated - edit runners/build-warp-collection.mjs, not the JSON.
#
# Requires: a built bifrost-http binary, newman, go, and reachable Postgres and
# Weaviate (tests/docker-compose.yml provides both).

set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
API_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
REPO_ROOT="$(cd "$API_DIR/../../.." && pwd)"
cd "$API_DIR"

COLLECTION="collections/bifrost-v1-warp.postman_collection.json"
REPORT_DIR="newman-reports/warp"
SEED_DIR="$REPO_ROOT/tests/cmd/seed"

GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m'

BIFROST_BINARY=""
PORT="8093"
# gpt-4.1-mini, the first default, misread the tools on the incident chain in
# four of four runs (error_codes for an error type, alias for a model); gpt-5.4
# and gpt-5.6-luna both passed it first try, luna at about a fifth of the cost.
WARP_MODEL="${WARP_MODEL:-gpt-5.6-luna}"
WARP_EMBEDDING_MODEL="${WARP_EMBEDDING_MODEL:-text-embedding-3-small}"
WARP_SEED="${WARP_SEED:-42}"
WEAVIATE_HOST="${WEAVIATE_HOST:-localhost:9000}"
# gRPC is optional to Bifrost's Weaviate client, and CI's compose file exposes
# only the HTTP port, so it is left out unless asked for.
WEAVIATE_GRPC_HOST="${WEAVIATE_GRPC_HOST:-}"
POSTGRES_HOST="${POSTGRES_HOST:-localhost}"
POSTGRES_PORT="${POSTGRES_PORT:-5432}"
POSTGRES_USER="${POSTGRES_USER:-bifrost}"
POSTGRES_PASSWORD="${POSTGRES_PASSWORD:-bifrost_password}"
POSTGRES_SSLMODE="${POSTGRES_SSLMODE:-disable}"
WARP_DB="${WARP_DB:-bifrost_warp_e2e}"
# Another Bifrost to send Warp's OpenAI calls through, so the key stays in that
# instance's config: the test server's openai provider points at its /openai
# route with a placeholder key, which that instance replaces with its own. It
# never forwards a caller's key unless allow_direct_keys is on and the request
# sends x-bf-direct-key, which this server does not.
WARP_UPSTREAM_BIFROST="${WARP_UPSTREAM_BIFROST%/}"
REPORTERS="cli"
VERBOSE=""
BAIL=""
FOLDERS=()

while [[ $# -gt 0 ]]; do
    case "$1" in
        --binary) BIFROST_BINARY="$2"; shift 2 ;;
        --port) PORT="$2"; shift 2 ;;
        --folder) FOLDERS+=("$2"); shift 2 ;;
        --html) REPORTERS="${REPORTERS},html"; shift ;;
        --json) REPORTERS="${REPORTERS},json"; shift ;;
        --verbose) VERBOSE="--verbose"; shift ;;
        --bail) BAIL="--bail"; shift ;;
        --help)
            echo "Usage: $0 --binary <path-to-bifrost-http> [OPTIONS]"
            echo ""
            echo "Options:"
            echo "  --binary <path>   Path to a built bifrost-http binary (required)"
            echo "  --port <port>     Port to boot the server on (default: 8093)"
            echo "  --folder <name>   Run only this collection folder (repeatable). Setup always runs."
            echo "  --html            Generate HTML report"
            echo "  --json            Generate JSON report"
            echo "  --verbose         Show detailed Newman output"
            echo "  --bail            Stop on first failure"
            echo "  --help            Show this help message"
            echo ""
            echo "Environment:"
            echo "  OPENAI_API_KEY                       Warp's model and embeddings; required unless WARP_UPSTREAM_BIFROST is set"
            echo "  WARP_UPSTREAM_BIFROST                route OpenAI calls through a running Bifrost that holds the key, e.g. http://localhost:8080"
            echo "  WARP_MODEL / WARP_EMBEDDING_MODEL    default gpt-5.6-luna / text-embedding-3-small"
            echo "  WARP_SEED                            seeder rng seed (default 42)"
            echo "  POSTGRES_HOST/PORT/USER/PASSWORD/SSLMODE   server to create \$WARP_DB on"
            echo "  WARP_DB                              database to drop and recreate (default bifrost_warp_e2e)"
            echo "  WEAVIATE_HOST                        default localhost:9000"
            echo "  WEAVIATE_GRPC_HOST                   optional, e.g. localhost:50051 (default: HTTP only)"
            exit 0 ;;
        *) echo -e "${RED}Unknown option: $1${NC}"; exit 1 ;;
    esac
done

echo -e "${GREEN}==============================================${NC}"
echo -e "${GREEN}Bifrost Warp Test Runner${NC}"
echo -e "${GREEN}==============================================${NC}"
echo ""

if ! command -v newman &>/dev/null; then
    echo -e "${RED}Error: Newman is not installed${NC}"
    echo "Install it with: npm install -g newman"
    exit 1
fi
if ! command -v go &>/dev/null; then
    echo -e "${RED}Error: go is required to build the seeder${NC}"
    exit 1
fi
if [ -z "$BIFROST_BINARY" ] || [ ! -x "$BIFROST_BINARY" ]; then
    echo -e "${RED}Error: --binary must point to an executable bifrost-http binary${NC}"
    exit 1
fi
if [ -n "$WARP_UPSTREAM_BIFROST" ]; then
    if ! curl -sf -o /dev/null "$WARP_UPSTREAM_BIFROST/health"; then
        echo -e "${RED}Error: WARP_UPSTREAM_BIFROST=$WARP_UPSTREAM_BIFROST does not answer /health${NC}"
        exit 1
    fi
    echo -e "${YELLOW}Routing Warp's OpenAI calls through $WARP_UPSTREAM_BIFROST (its own key is used)${NC}"
elif [ -z "${OPENAI_API_KEY:-}" ]; then
    echo -e "${RED}Error: set OPENAI_API_KEY, or WARP_UPSTREAM_BIFROST to use a running Bifrost's key${NC}"
    exit 1
fi
if [ ! -f "$COLLECTION" ]; then
    echo -e "${RED}Error: Collection file not found: $COLLECTION${NC}"
    exit 1
fi

mkdir -p "$REPORT_DIR"

SERVER_PID=""
WORK_DIR="$(mktemp -d)"
# A fresh namespace per run: Weaviate outlives the database, and vectors left by
# an earlier run point at log ids this run's database does not have.
WARP_NAMESPACE="WarpE2e$(date +%s)"

cleanup() {
    if [ -n "$SERVER_PID" ] && kill -0 "$SERVER_PID" 2>/dev/null; then
        kill "$SERVER_PID" 2>/dev/null || true
        wait "$SERVER_PID" 2>/dev/null || true
    fi
    curl -s -o /dev/null -X DELETE "http://$WEAVIATE_HOST/v1/schema/$WARP_NAMESPACE" 2>/dev/null || true
    rm -rf "$WORK_DIR"
}
trap cleanup EXIT

port_in_use() {
    (command -v nc &>/dev/null && nc -z 127.0.0.1 "$1" 2>/dev/null) \
        || (echo >/dev/tcp/127.0.0.1/"$1") 2>/dev/null
}

if port_in_use "$PORT"; then
    echo -e "${RED}Error: port $PORT is already in use; pass --port to use another${NC}"
    exit 1
fi

DSN="postgres://$POSTGRES_USER:$POSTGRES_PASSWORD@$POSTGRES_HOST:$POSTGRES_PORT/$WARP_DB?sslmode=$POSTGRES_SSLMODE"

echo -e "${YELLOW}Building seeder...${NC}"
( cd "$SEED_DIR" && go build -o "$WORK_DIR/warpseed" ./warpseed ) || {
    echo -e "${RED}Error: failed to build tests/cmd/seed/warpseed${NC}"; exit 1; }

echo -e "${YELLOW}Recreating database $WARP_DB...${NC}"
"$WORK_DIR/warpseed" -dsn "$DSN" -reset-db > "$WORK_DIR/reset.log" 2>&1 || {
    echo -e "${RED}Error: could not recreate $WARP_DB${NC}"; cat "$WORK_DIR/reset.log"; exit 1; }

write_config() {
    local dir="$1"
    local pg grpc="" openai
    if [ -n "$WARP_UPSTREAM_BIFROST" ]; then
        openai='"keys": [{ "name": "warp-e2e", "value": "sk-routed-through-upstream-bifrost", "models": ["*"], "weight": 1 }], "network_config": { "base_url": "'"$WARP_UPSTREAM_BIFROST"'/openai", "default_request_timeout_in_seconds": 120 }'
    else
        openai='"keys": [{ "name": "warp-e2e", "value": "env.OPENAI_API_KEY", "models": ["*"], "weight": 1 }]'
    fi
    [ -n "$WEAVIATE_GRPC_HOST" ] && grpc=", \"grpc_config\": { \"host\": \"$WEAVIATE_GRPC_HOST\", \"secured\": false }"
    pg=$(cat <<EOF
{ "host": "$POSTGRES_HOST", "port": "$POSTGRES_PORT", "user": "$POSTGRES_USER", "password": "$POSTGRES_PASSWORD", "db_name": "$WARP_DB", "ssl_mode": "$POSTGRES_SSLMODE" }
EOF
)
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
  "config_store": { "enabled": true, "type": "postgres", "config": $pg },
  "logs_store": { "enabled": true, "type": "postgres", "config": $pg },
  "vector_store": {
    "enabled": true,
    "type": "weaviate",
    "config": { "scheme": "http", "host": "$WEAVIATE_HOST"$grpc }
  },
  "feature_flags": { "flags": { "warp": { "enabled": true } } },
  "providers": {
    "openai": { $openai }
  }
}
EOF
}

start_bifrost() {
    write_config "$WORK_DIR"
    local log="$WORK_DIR/server.log"
    "$BIFROST_BINARY" --app-dir "$WORK_DIR" --port "$PORT" --log-level info > "$log" 2>&1 &
    SERVER_PID=$!
    local elapsed=0
    while [ $elapsed -lt 90 ]; do
        grep -q "successfully started bifrost" "$log" 2>/dev/null && break
        if ! kill -0 "$SERVER_PID" 2>/dev/null; then
            echo -e "${RED}Server exited before becoming ready${NC}"; cat "$log"; exit 1
        fi
        sleep 1; elapsed=$((elapsed + 1))
    done
    if [ $elapsed -ge 90 ]; then
        echo -e "${RED}Server did not start within 90s${NC}"; cat "$log"; exit 1
    fi
    cp "$log" "$API_DIR/$REPORT_DIR/server-boot.log"
    echo -e "${GREEN}Bifrost ready on :$PORT${NC}"
}

start_bifrost

# Seeded after boot so the server's migrations have created the logs table.
echo -e "${YELLOW}Seeding logs...${NC}"
"$WORK_DIR/warpseed" -dsn "$DSN" -seed "$WARP_SEED" -env-out "$WORK_DIR/seed.env" > "$WORK_DIR/seed.log" 2>&1 || {
    echo -e "${RED}Error: seeding failed${NC}"; cat "$WORK_DIR/seed.log"; exit 1; }
grep -E "^(inserted|refreshed|incident window)" "$WORK_DIR/seed.log" || true

cmd=(newman run "$COLLECTION"
    --env-var "base_url=http://localhost:$PORT"
    --env-var "warp_model=$WARP_MODEL"
    --env-var "warp_embedding_model=$WARP_EMBEDDING_MODEL"
    --env-var "warp_namespace=$WARP_NAMESPACE"
    --timeout-script 60000 --timeout-request 600000
    -r "$REPORTERS")
while IFS='=' read -r key value; do
    [ -n "$key" ] && cmd+=(--env-var "$key=$value")
done < "$WORK_DIR/seed.env"
if [ ${#FOLDERS[@]} -gt 0 ]; then
    cmd+=(--folder "Setup")
    for f in "${FOLDERS[@]}"; do cmd+=(--folder "$f"); done
fi
[[ "$REPORTERS" == *"html"* ]] && cmd+=(--reporter-html-export "${REPORT_DIR}/report.html")
[[ "$REPORTERS" == *"json"* ]] && cmd+=(--reporter-json-export "${REPORT_DIR}/report.json")
[ -n "$VERBOSE" ] && cmd+=("$VERBOSE")
[ -n "$BAIL" ] && cmd+=("$BAIL")

set +e
"${cmd[@]}"
EXIT_CODE=$?
set -e

echo ""
if [ $EXIT_CODE -eq 0 ]; then
    echo -e "${GREEN}✓ All Warp checks passed!${NC}"
else
    echo -e "${RED}✗ Some Warp checks failed${NC}"
    cp "$WORK_DIR/server.log" "$API_DIR/$REPORT_DIR/server.log" 2>/dev/null || true
    echo -e "Server log saved to: ${YELLOW}$REPORT_DIR/server.log${NC}"
fi

exit $EXIT_CODE
