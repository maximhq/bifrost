#!/usr/bin/env bash
set -euo pipefail

# Agent Gateway end-to-end test.
# Builds bifrost-http, starts it with private push callbacks enabled and SQLite
# stores, runs tests/agentgateway against it, and always stops the server.
# Usage: ./test-agentgateway-e2e.sh

source "$(dirname "$0")/setup-go-workspace.sh"

PORT="${AGENTGATEWAY_PORT:-18080}"
WORK_DIR="$(mktemp -d)"
SERVER_LOG="${WORK_DIR}/server.log"
APP_DIR="${WORK_DIR}/app"
SERVER_PID=""
ADMIN_PASSWORD="e2e-$(head -c 12 /dev/urandom | base64 | tr -dc A-Za-z0-9)"

cleanup() {
  if [ -n "${SERVER_PID}" ]; then
    kill "${SERVER_PID}" 2>/dev/null || true
    wait "${SERVER_PID}" 2>/dev/null || true
  fi
  if [ "${KEEP_LOG:-}" = "1" ] || [ "${TEST_FAILED:-0}" = "1" ]; then
    echo "--- server log ---"
    tail -n 100 "${SERVER_LOG}" 2>/dev/null || true
  fi
  rm -rf "${WORK_DIR}"
}
trap cleanup EXIT

# CI's build-gateway job supplies tmp/bifrost-http; build it locally otherwise.
if [ "${SKIP_GATEWAY_BUILD:-0}" = "1" ]; then
  if [ ! -x tmp/bifrost-http ]; then
    echo "SKIP_GATEWAY_BUILD=1 but no executable binary at tmp/bifrost-http" >&2
    exit 1
  fi
  cp tmp/bifrost-http "${WORK_DIR}/bifrost-http"
else
  echo "Building bifrost-http..."
  (cd transports/bifrost-http && go build -o "${WORK_DIR}/bifrost-http" .)
fi

mkdir -p "${APP_DIR}"
cat > "${APP_DIR}/config.json" <<JSON
{
  "\$schema": "https://www.getbifrost.ai/schema",
  "server": { "a2a_allow_private_push_callbacks": true },
  "client": { "a2a_external_client_url": "http://127.0.0.1:${PORT}", "enforce_auth_on_inference": false },
  "governance": {
    "auth_config": {
      "admin_username": "e2e-admin",
      "admin_password": "${ADMIN_PASSWORD}",
      "is_enabled": true,
      "disable_auth_on_inference": true
    }
  },
  "config_store": { "enabled": true, "type": "sqlite", "config": { "path": "${WORK_DIR}/config.db" } },
  "logs_store": { "enabled": true, "type": "sqlite", "config": { "path": "${WORK_DIR}/logs.db" } }
}
JSON

echo "Starting bifrost-http on :${PORT}..."
"${WORK_DIR}/bifrost-http" -app-dir "${APP_DIR}" -port "${PORT}" -host 127.0.0.1 >"${SERVER_LOG}" 2>&1 &
SERVER_PID=$!

for _ in $(seq 1 60); do
  if curl -fsS -o /dev/null "http://127.0.0.1:${PORT}/health"; then
    READY=1
    break
  fi
  if ! kill -0 "${SERVER_PID}" 2>/dev/null; then
    break
  fi
  sleep 1
done
if [ "${READY:-0}" != "1" ]; then
  TEST_FAILED=1
  echo "bifrost-http did not become healthy"
  exit 1
fi

echo "Running Agent Gateway E2E..."
if ! (cd tests/agentgateway && AGENTGATEWAY_URL="http://127.0.0.1:${PORT}" AGENTGATEWAY_ADMIN_USER=e2e-admin AGENTGATEWAY_ADMIN_PASS="${ADMIN_PASSWORD}" GOWORK=off go test ./... -count=1 -v -timeout 5m); then
  TEST_FAILED=1
  exit 1
fi
