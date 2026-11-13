#!/usr/bin/env bash
set -euo pipefail

# Tracing SDK e2e: starts a gateway with no providers, points the Braintrust, Langfuse, Phoenix,
# Arize AX and OpenTelemetry SDKs at it, and checks what the Traces API returns
# (tests/integrations/tracing). No vendor and no provider is called, so it needs no secrets.
#
# The suite runs twice: with dashboard auth on (SDKs send the dashboard username and password as
# their API key) and with it off (SDKs send no key). Each run gets a fresh app dir.
#
# Usage: ./test-tracing-sdks.sh
#   TRACING_PREBUILT=1  run the tmp/bifrost-http the pipeline already built instead of building
#   TRACING_GATEWAY_PORT / TRACING_GRPC_PORT  default 8080 / 4317

source "$(dirname "$0")/setup-go-workspace.sh"

PORT="${TRACING_GATEWAY_PORT:-8080}"
GRPC_PORT="${TRACING_GRPC_PORT:-4317}"
mkdir -p tmp

if [ "${TRACING_PREBUILT:-}" = "1" ] && [ -x tmp/bifrost-http ]; then
  echo "📦 Using the prebuilt tmp/bifrost-http"
else
  echo "🔨 Building bifrost-http..."
  (cd transports && go build -o ../tmp/bifrost-http ./bifrost-http)
fi

for port in "$PORT" "$GRPC_PORT"; do
  if nc -z 127.0.0.1 "$port" 2>/dev/null; then
    echo "❌ something already listens on 127.0.0.1:$port; stop it or set TRACING_GATEWAY_PORT / TRACING_GRPC_PORT"
    exit 1
  fi
done

APP_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/bifrost-tracing.XXXXXX")"
GATEWAY_PID=""
LOG_FILE=""

stop_gateway() {
  if [ -n "$GATEWAY_PID" ]; then
    kill "$GATEWAY_PID" 2>/dev/null || true
    wait "$GATEWAY_PID" 2>/dev/null || true
    GATEWAY_PID=""
  fi
}

cleanup() {
  status=$?
  stop_gateway
  rm -rf "$APP_ROOT"
  exit "$status"
}
trap cleanup EXIT

# write_config <app dir> <auth: on|off> <admin password>
# A heredoc rather than jq: the release pipeline's test-live runner does not install jq. The values are
# a mktemp path and a hex password, so neither needs JSON escaping.
write_config() {
  local dir="$1" auth="$2" password="$3"
  local governance='{}'
  if [ "$auth" = "on" ]; then
    governance="{\"auth_config\": {\"admin_username\": \"admin\", \"admin_password\": \"$password\", \"is_enabled\": true}}"
  fi
  cat > "$dir/config.json" <<JSON
{
  "\$schema": "https://www.getbifrost.ai/schema",
  "client": {"enable_logging": true, "allowed_origins": ["*"]},
  "config_store": {"enabled": true, "type": "sqlite", "config": {"path": "$dir/config.db"}},
  "logs_store": {"enabled": true, "type": "sqlite", "config": {"path": "$dir/logs.db"}},
  "governance": $governance,
  "setup_token": "tracing-e2e-setup-token"
}
JSON
}

run_suite() {
  local auth="$1"
  local dir="$APP_ROOT/$auth"
  local password
  password="$(od -An -tx1 -N16 /dev/urandom | tr -d ' \n')"
  mkdir -p "$dir"
  write_config "$dir" "$auth" "$password"
  LOG_FILE="tmp/tracing-gateway-$auth.log"

  echo ""
  echo "🚀 Starting the gateway (dashboard auth $auth) on 127.0.0.1:$PORT, OTLP gRPC on $GRPC_PORT..."
  BIFROST_TRACING_GRPC_PORT="$GRPC_PORT" ./tmp/bifrost-http -host 127.0.0.1 -port "$PORT" -log-style json -log-level info \
    -app-dir "$dir" > "$LOG_FILE" 2>&1 &
  GATEWAY_PID=$!

  for _ in $(seq 1 60); do
    curl -sf "http://127.0.0.1:$PORT/health" > /dev/null 2>&1 && break
    kill -0 "$GATEWAY_PID" 2>/dev/null || break
    sleep 1
  done
  if ! curl -sf "http://127.0.0.1:$PORT/health" > /dev/null 2>&1; then
    echo "❌ gateway did not become healthy"
    tail -n 40 "$LOG_FILE" || true
    return 1
  fi
  for _ in $(seq 1 20); do
    nc -z 127.0.0.1 "$GRPC_PORT" 2>/dev/null && break
    sleep 0.5
  done

  local status=0
  if [ "$auth" = "on" ]; then
    BIFROST_ADMIN_USERNAME=admin BIFROST_ADMIN_PASSWORD="$password" \
      BIFROST_BASE_URL="http://127.0.0.1:$PORT" BIFROST_TRACING_GRPC_ENDPOINT="127.0.0.1:$GRPC_PORT" \
      TRACING_REPORT_SUFFIX="auth-on" make test-tracing-sdks || status=1
  else
    BIFROST_SETUP_TOKEN="tracing-e2e-setup-token" \
      BIFROST_BASE_URL="http://127.0.0.1:$PORT" BIFROST_TRACING_GRPC_ENDPOINT="127.0.0.1:$GRPC_PORT" \
      TRACING_REPORT_SUFFIX="auth-off" make test-tracing-sdks || status=1
  fi
  if [ "$status" -ne 0 ]; then
    echo "--- last gateway log lines (dashboard auth $auth) ---"
    tail -n 40 "$LOG_FILE" || true
  fi
  stop_gateway
  return "$status"
}

FAILED=0
run_suite on || FAILED=1
run_suite off || FAILED=1
if [ "$FAILED" -ne 0 ]; then
  echo "❌ tracing SDK e2e tests failed"
  exit 1
fi
echo "✅ tracing SDK e2e tests passed"
