#!/usr/bin/env bash
set -euo pipefail

# Run the GPT Live e2e suite (tests/live) against a gateway built from this checkout and
# started from the live profile, which points the OpenAI provider at the suite's fake upstream.
# Same path as locally: `make dev APP_DIR=$(pwd)/tests/live` then `make test-live`.
# Usage: ./test-live.sh [fake|long]   fake: make test-live (~2 min)
#                                    long: make test-live-long (two 17-minute calls)
# LIVE_PREBUILT=1 runs the tmp/bifrost-http the release pipeline already built instead of building.

# Setup Go workspace for CI
source "$(dirname "$0")/setup-go-workspace.sh"

MODE="${1:-fake}"
PORT="${LIVE_GATEWAY_PORT:-8080}"
mkdir -p tmp

if [ "${LIVE_PREBUILT:-}" = "1" ] && [ -x tmp/bifrost-http ]; then
  echo "📦 Using the prebuilt tmp/bifrost-http"
else
  echo "🔨 Building bifrost-http..."
  (cd transports && go build -o ../tmp/bifrost-http ./bifrost-http)
fi

# The suite would otherwise run against whatever already answers on the port, as a stale
# local gateway does, and fail on the wrong profile.
if curl -sf "http://127.0.0.1:$PORT/health" > /dev/null 2>&1; then
  echo "❌ something already answers on 127.0.0.1:$PORT; stop it or set LIVE_GATEWAY_PORT"
  exit 1
fi

echo "🚀 Starting the live-profile gateway on 127.0.0.1:$PORT..."
./tmp/bifrost-http -host 127.0.0.1 -port "$PORT" -log-style json -log-level info -app-dir "$PWD/tests/live" > tmp/live-gateway.log 2>&1 &
GATEWAY_PID=$!
cleanup() {
  status=$?
  kill "$GATEWAY_PID" 2>/dev/null || true
  if [ "$status" -ne 0 ]; then
    echo "--- last gateway log lines ---"
    tail -n 40 tmp/live-gateway.log || true
  fi
  exit "$status"
}
trap cleanup EXIT

for _ in $(seq 1 60); do
  curl -sf "http://127.0.0.1:$PORT/health" > /dev/null && break
  sleep 1
done
curl -sf "http://127.0.0.1:$PORT/health" > /dev/null || { echo "❌ gateway did not become healthy"; exit 1; }

export BIFROST_BASE_URL="http://127.0.0.1:$PORT"
case "$MODE" in
  fake) echo "🧪 Running GPT Live e2e tests (fake upstream)..."; make test-live ;;
  long) echo "🧪 Running long GPT Live sessions (fake upstream)..."; make test-live-long ;;
  *) echo "unknown mode: $MODE (fake|long)"; exit 2 ;;
esac
