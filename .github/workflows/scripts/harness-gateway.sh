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
# The sg process a group-confined gateway was launched through, reaped on stop.
HARNESS_SG_PID=""
# Overridable so a caller can boot a binary it built itself (bifrost-enterprise's
# licensed build) with SKIP_GATEWAY_BUILD=1.
HARNESS_BINARY="${HARNESS_BINARY:-$REPO_ROOT/tmp/bifrost-http}"
# Every harness config derives from the same source of truth the local
# `make dev` app dir uses, so CI and laptop runs exercise identical wiring.
HARNESS_SOURCE_CONFIG="$REPO_ROOT/tests/integrations/python/config.json"

harness_build_gateway() {
  # CI's build-gateway job builds the UI + binary once and hands every consuming
  # job the same artifact, so the harness jobs skip a ~3.5 minute rebuild. Local
  # runs never set SKIP_GATEWAY_BUILD and always build from source.
  if [ "${SKIP_GATEWAY_BUILD:-0}" = "1" ]; then
    if [ ! -x "$HARNESS_BINARY" ]; then
      echo "❌ SKIP_GATEWAY_BUILD=1 but no executable binary at $HARNESS_BINARY" >&2
      return 1
    fi
    echo "⏭️  Using prebuilt bifrost-http binary at $HARNESS_BINARY"
    return 0
  fi

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
#
# Optional, for callers that need to shape the gateway process alone:
#   HARNESS_GATEWAY_ENV    space-separated KEY=VALUE words set only in the gateway's
#                          environment (e.g. HTTPS_PROXY), never in the caller's, so
#                          newman and make keep their own.
#   HARNESS_GATEWAY_GROUP  run the gateway with this primary group (via sg), so an
#                          iptables --gid-owner rule can confine its egress.
harness_start_gateway() {
  local app_dir="$1" port="$2" log_file="$3"
  local base_url="http://localhost:$port"
  local cmd=("$HARNESS_BINARY" --app-dir "$app_dir" --port "$port" --log-level info)
  if [ -n "${HARNESS_GATEWAY_ENV:-}" ]; then
    # shellcheck disable=SC2206 # KEY=VALUE words, split on purpose
    cmd=(env $HARNESS_GATEWAY_ENV "${cmd[@]}")
  fi

  echo "🚀 Starting bifrost-http on port $port..."
  if [ -n "${HARNESS_GATEWAY_GROUP:-}" ]; then
    # sg forks and waits for the command when SYSLOG_SG_ENAB is on (the Ubuntu
    # default), so $! would be sg and signalling it leaves the gateway running. The
    # command records its own PID before exec'ing the gateway in place, and that PID
    # is what the helper tracks.
    local pid_file="$log_file.pid"
    rm -f "$pid_file"
    sg "$HARNESS_GATEWAY_GROUP" -c "echo \$\$ > $(printf '%q' "$pid_file"); exec $(printf '%q ' "${cmd[@]}")" > "$log_file" 2>&1 &
    HARNESS_SG_PID=$!
    local waited=0
    while [ ! -s "$pid_file" ] && [ $waited -lt 50 ]; do
      sleep 0.2
      waited=$((waited + 1))
    done
    if [ ! -s "$pid_file" ]; then
      echo "❌ the gateway launched through sg never reported its PID"
      cat "$log_file"
      return 1
    fi
    HARNESS_BIFROST_PID="$(cat "$pid_file")"
  else
    "${cmd[@]}" > "$log_file" 2>&1 &
    HARNESS_BIFROST_PID=$!
  fi

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
  local pid="${HARNESS_BIFROST_PID:-}"
  if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
    echo "🧹 Stopping bifrost (PID $pid)..."
    kill "$pid" 2>/dev/null || true
    # A gateway launched through sg is not this shell's child, so `wait` cannot
    # block on it: poll until it is gone, so the next start never finds the port
    # still held, and force it after 10s.
    local waited=0
    while kill -0 "$pid" 2>/dev/null && [ $waited -lt 50 ]; do
      sleep 0.2
      waited=$((waited + 1))
    done
    kill -9 "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
  fi
  if [ -n "${HARNESS_SG_PID:-}" ]; then
    wait "$HARNESS_SG_PID" 2>/dev/null || true
  fi
  HARNESS_BIFROST_PID=""
  HARNESS_SG_PID=""
}
