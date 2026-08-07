#!/usr/bin/env bash
# Sourceable helpers shared by the CI harness runners (test-provider-harness.sh,
# test-cli-harness.sh). Builds the bifrost-http binary, seeds a throwaway app
# dir with sqlite stores, and boots the gateway.
#
# Usage:
#   REPO_ROOT=... source harness-gateway.sh
#   harness_build_gateway
#   harness_seed_app_dir "$APP_DIR"
#   harness_start_gateway "$APP_DIR" 8080 "$LOG"
#   trap harness_stop_gateway EXIT

: "${REPO_ROOT:?REPO_ROOT must be set before sourcing harness-gateway.sh}"

HARNESS_BIFROST_PID=""
HARNESS_BINARY="$REPO_ROOT/tmp/bifrost-http"
# Every harness config derives from the same source of truth the local
# `make dev` app dir uses, so CI and laptop runs exercise identical wiring.
HARNESS_SOURCE_CONFIG="$REPO_ROOT/tests/integrations/python/config.json"

harness_build_gateway() {
  echo "🎨 Building UI..."
  (cd "$REPO_ROOT" && make build-ui)

  echo "🔨 Building bifrost-http binary..."
  mkdir -p "$REPO_ROOT/tmp"
  (cd "$REPO_ROOT/transports/bifrost-http" && go build -o "$HARNESS_BINARY" .)
}

# harness_seed_app_dir <app_dir>
harness_seed_app_dir() {
  local app_dir="$1"
  if [ ! -f "$HARNESS_SOURCE_CONFIG" ]; then
    echo "❌ Harness config not found: $HARNESS_SOURCE_CONFIG" >&2
    return 1
  fi
  echo "📝 Seeding harness app dir at $app_dir..."
  rm -rf "$app_dir"
  mkdir -p "$app_dir"
  # The source config points its sqlite stores at the checked-in config.db /
  # logs.db (25MB of local state). Rewrite both to fresh files inside the
  # throwaway app dir so CI always starts from a clean seed.
  jq --arg cfg "$app_dir/config.db" --arg logs "$app_dir/logs.db" \
    '.config_store.config.path = $cfg | .logs_store.config.path = $logs' \
    "$HARNESS_SOURCE_CONFIG" > "$app_dir/config.json"
}

# harness_start_gateway <app_dir> <port> <log_file>
harness_start_gateway() {
  local app_dir="$1" port="$2" log_file="$3"
  local base_url="http://localhost:$port"

  echo "🚀 Starting bifrost-http on port $port..."
  "$HARNESS_BINARY" --app-dir "$app_dir" --port "$port" --log-level info > "$log_file" 2>&1 &
  HARNESS_BIFROST_PID=$!

  local max_wait=120 elapsed=0
  while [ $elapsed -lt $max_wait ]; do
    if curl -fsS --max-time 2 "$base_url/health" >/dev/null 2>&1; then
      echo "✅ Bifrost healthy (PID $HARNESS_BIFROST_PID, ${elapsed}s)"
      return 0
    fi
    if ! kill -0 "$HARNESS_BIFROST_PID" 2>/dev/null; then
      echo "❌ Bifrost exited during startup"
      cat "$log_file"
      return 1
    fi
    sleep 2
    elapsed=$((elapsed + 2))
  done

  echo "❌ Bifrost did not become healthy within ${max_wait}s"
  cat "$log_file"
  return 1
}

harness_stop_gateway() {
  if [ -n "${HARNESS_BIFROST_PID:-}" ] && kill -0 "$HARNESS_BIFROST_PID" 2>/dev/null; then
    echo "🧹 Stopping bifrost (PID $HARNESS_BIFROST_PID)..."
    kill "$HARNESS_BIFROST_PID" 2>/dev/null || true
    wait "$HARNESS_BIFROST_PID" 2>/dev/null || true
  fi
  HARNESS_BIFROST_PID=""
}
