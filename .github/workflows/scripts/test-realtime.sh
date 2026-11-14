#!/usr/bin/env bash
set -euo pipefail

# Run the Gemini, Vertex, ElevenLabs and Soniox tests of the realtime e2e suite (tests/realtime) against a
# gateway built from this checkout. Same path as locally: start a gateway with `make dev APP_DIR=...`, then
# `make test-realtime`.
# Usage: ./test-realtime.sh [fake|real|long]
#   fake  fake Gemini Live, Soniox and ElevenLabs upstreams in the test process and the gateway on
#         tests/realtime/fake; needs no provider keys (about a minute). Vertex has no fake, so its
#         cases are skipped here and run in the real mode.
#   real  the real providers on tests/realtime; needs GEMINI_API_KEY, VERTEX_PROJECT_ID, VERTEX_CREDENTIALS,
#         ELEVENLABS_API_KEY and SONIOX_API_KEY in the environment
#   long  only the 11-minute Gemini and Vertex sessions that cross Google's connection limit (REALTIME_LONG=1);
#         needs the Google credentials, not ELEVENLABS_API_KEY or SONIOX_API_KEY
# REALTIME_PATTERN picks the tests (a regular expression); the default is every test that runs in both modes.
# REALTIME_PREBUILT=1 runs the tmp/bifrost-http a previous step already built instead of building.

# Setup Go workspace for CI
source "$(dirname "$0")/setup-go-workspace.sh"

MODE="${1:-fake}"
PORT="${REALTIME_GATEWAY_PORT:-8187}"
PATTERN="${REALTIME_PATTERN:-TestLive|TestSoniox|TestElevenlabs|TestSpeech}"
ROOT="$PWD"
mkdir -p tmp

case "$MODE" in
  fake) APP_DIR="$ROOT/tests/realtime/fake"; export REALTIME_UPSTREAM=fake ;;
  real) APP_DIR="$ROOT/tests/realtime"; export REALTIME_UPSTREAM=real ;;
  long) APP_DIR="$ROOT/tests/realtime"; export REALTIME_UPSTREAM=real REALTIME_LONG=1 ;;
  *) echo "unknown mode: $MODE (fake|real|long)"; exit 2 ;;
esac

if [ "$MODE" != "fake" ]; then
  required="GEMINI_API_KEY VERTEX_PROJECT_ID VERTEX_CREDENTIALS"
  if [ "$MODE" = "real" ]; then
    required="$required ELEVENLABS_API_KEY SONIOX_API_KEY"
  fi
  for name in $required; do
    [ -n "${!name:-}" ] || { echo "❌ $name is not set; the $MODE mode talks to the real providers"; exit 1; }
  done
fi

if [ "${REALTIME_PREBUILT:-}" = "1" ] && [ -x tmp/bifrost-http ]; then
  echo "📦 Using the prebuilt tmp/bifrost-http"
else
  echo "🔨 Building bifrost-http..."
  # The binary embeds transports/bifrost-http/ui, which a fresh checkout does not have (the UI is built elsewhere).
  mkdir -p transports/bifrost-http/ui
  touch transports/bifrost-http/ui/.gitkeep
  (cd transports && go build -o ../tmp/bifrost-http ./bifrost-http)
fi

# The suite would otherwise run against whatever already answers on the port, as a stale
# local gateway does, and fail on the wrong profile.
if curl -sf "http://127.0.0.1:$PORT/health" > /dev/null 2>&1; then
  echo "❌ something already answers on 127.0.0.1:$PORT; stop it or set REALTIME_GATEWAY_PORT"
  exit 1
fi

# The profiles name their databases relative to transports/bifrost-http, where `make dev` runs the gateway.
# A database left by an earlier run would keep the profile it was created from.
find "$APP_DIR/data" -name '*.db*' -delete 2>/dev/null || true

echo "🚀 Starting the $MODE-profile gateway on 127.0.0.1:$PORT..."
(cd transports/bifrost-http && exec "$ROOT/tmp/bifrost-http" -host 127.0.0.1 -port "$PORT" -log-style json -log-level info -app-dir "$APP_DIR") > tmp/realtime-gateway.log 2>&1 &
GATEWAY_PID=$!
cleanup() {
  status=$?
  kill "$GATEWAY_PID" 2>/dev/null || true
  # Wait for the shutdown so the port is free for the next run; a hung one is cut off after 10 s.
  for _ in $(seq 1 100); do
    kill -0 "$GATEWAY_PID" 2>/dev/null || break
    sleep 0.1
  done
  kill -9 "$GATEWAY_PID" 2>/dev/null || true
  wait "$GATEWAY_PID" 2>/dev/null || true
  if [ "$status" -ne 0 ]; then
    echo "--- last gateway log lines ---"
    tail -n 40 tmp/realtime-gateway.log || true
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
  fake) echo "🧪 Running the realtime e2e tests (fake upstreams)..."; make test-realtime PATTERN="$PATTERN" ;;
  real) echo "🧪 Running the realtime e2e tests (real providers)..."; make test-realtime PATTERN="$PATTERN" ;;
  long) echo "🧪 Running the long Gemini and Vertex Live sessions (real providers)..."; make test-realtime-long ;;
esac
