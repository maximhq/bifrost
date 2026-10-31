#!/usr/bin/env bash
set -euo pipefail

# Warp e2e test: brings up the compose stack's Postgres and Weaviate and runs
# tests/e2e/api/runners/individual/run-newman-warp-tests.sh against the prebuilt
# bifrost-http binary. CI runs this as its own job, beside test-api-integrations
# (which skips Warp under SKIP_WARP=1): the collection is a live, serial LLM
# evaluation of about 25 minutes, so chaining it after the other suites doubled
# that job's wall clock.

if command -v readlink >/dev/null 2>&1 && readlink -f "$0" >/dev/null 2>&1; then
  SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
else
  SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd -P)"
fi
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd -P)"

COMPOSE_FILE="$REPO_ROOT/.github/workflows/configs/docker-compose.yml"
BIFROST_BINARY="$REPO_ROOT/tmp/bifrost-http"
WARP_RUNNER="$REPO_ROOT/tests/e2e/api/runners/individual/run-newman-warp-tests.sh"
PORT="${PORT:-8088}"

if [ ! -x "$BIFROST_BINARY" ]; then
  echo "❌ No executable bifrost-http binary at $BIFROST_BINARY" >&2
  exit 1
fi
if [ -z "${OPENAI_API_KEY:-}" ] && [ -z "${WARP_UPSTREAM_BIFROST:-}" ]; then
  echo "❌ OPENAI_API_KEY (or WARP_UPSTREAM_BIFROST) is required: Warp's agent and embeddings call OpenAI" >&2
  exit 1
fi

# The runner builds tests/cmd/seed/warpseed inside the workspace.
cd "$REPO_ROOT"
# shellcheck source=/dev/null
source "$SCRIPT_DIR/setup-go-workspace.sh"

cleanup() {
  local exit_code=$?
  echo "🧹 Stopping Docker services..."
  docker compose -f "$COMPOSE_FILE" down 2>/dev/null || true
  exit $exit_code
}
trap cleanup EXIT

# Only what Warp reads: Postgres for its config and logs stores, Weaviate for the
# log index and topics. Redis and Qdrant serve other suites.
echo "🐳 Starting Postgres and Weaviate..."
docker compose -f "$COMPOSE_FILE" up -d postgres weaviate

echo "⏳ Waiting for Postgres..."
ELAPSED=0
until [ "$(docker inspect -f '{{.State.Health.Status}}' "$(docker compose -f "$COMPOSE_FILE" ps -q postgres)" 2>/dev/null)" = "healthy" ]; do
  if [ $ELAPSED -ge 120 ]; then
    echo "❌ Postgres did not become healthy within 120s"
    docker compose -f "$COMPOSE_FILE" ps
    exit 1
  fi
  sleep 2
  ELAPSED=$((ELAPSED + 2))
done
echo "✅ Postgres ready (${ELAPSED}s)"

echo "⏳ Waiting for Weaviate..."
ELAPSED=0
until curl -fsS -o /dev/null "http://localhost:9000/v1/.well-known/ready"; do
  if [ $ELAPSED -ge 120 ]; then
    echo "❌ Weaviate failed readiness check within 120s"
    docker compose -f "$COMPOSE_FILE" logs weaviate | tail -50
    exit 1
  fi
  sleep 2
  ELAPSED=$((ELAPSED + 2))
done
echo "✅ Weaviate ready (${ELAPSED}s)"

# --json, not --html: the html reporter is not installed, and the JSON report
# keeps every response body, tool-call arguments included, for diagnosing misses.
echo ""
echo "🧪 Running Warp newman collection..."
"$WARP_RUNNER" --binary "$BIFROST_BINARY" --port "$PORT" --json
