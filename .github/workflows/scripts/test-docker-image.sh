#!/bin/bash
set -e

# Test Docker image by building, starting with docker-compose, and running E2E API tests
# Usage: ./test-docker-image.sh <platform>
# Example: ./test-docker-image.sh linux/amd64

# Get the absolute path of the script directory
if command -v readlink >/dev/null 2>&1 && readlink -f "$0" >/dev/null 2>&1; then
  SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
else
  SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd -P)"
fi

# Repository root (3 levels up from .github/workflows/scripts)
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd -P)"


# Setup Go workspace for CI (go.work is gitignored, must be regenerated)
# shellcheck disable=SC1091 # Resolved relative to this script at runtime.
source "$SCRIPT_DIR/setup-go-workspace.sh"

PLATFORM=${1:-linux/amd64}
ARCH=$(echo "$PLATFORM" | cut -d'/' -f2)
IMAGE_TAG="bifrost-test:ci-${GITHUB_SHA:-local}-${ARCH}"
CONTAINER_NAME="bifrost-test-${ARCH}"
PRIVDROP_CONTAINER_NAME="${CONTAINER_NAME}-privdrop"
RO_CONFIG_CONTAINER_NAME="${CONTAINER_NAME}-ro-config"
PRIVDROP_VOLUME_NAME="${CONTAINER_NAME}-root-volume"
TEST_PORT=8080
PRIVDROP_TEST_PORT=8081
DOCKER_COMPOSE_FILE="$REPO_ROOT/tests/docker-compose.yml"
TEMP_DIR=$(mktemp -d)
CONFIG_FILE="$TEMP_DIR/config.json"

echo "=== Testing Docker image for ${PLATFORM} ==="

# Cleanup function
cleanup() {
  local exit_code=$?
  echo ""
  echo "=== Cleaning up ==="
  
  # Stop and remove Bifrost container
  echo "Stopping Bifrost container..."
  docker stop "${CONTAINER_NAME}" > /dev/null 2>&1 || true
  docker rm "${CONTAINER_NAME}" > /dev/null 2>&1 || true
  docker stop "${PRIVDROP_CONTAINER_NAME}" > /dev/null 2>&1 || true
  docker rm "${PRIVDROP_CONTAINER_NAME}" > /dev/null 2>&1 || true
  docker stop "${RO_CONFIG_CONTAINER_NAME}" > /dev/null 2>&1 || true
  docker rm "${RO_CONFIG_CONTAINER_NAME}" > /dev/null 2>&1 || true
  docker volume rm "${PRIVDROP_VOLUME_NAME}" > /dev/null 2>&1 || true
  
  # Stop docker-compose services
  echo "Stopping docker-compose services..."
  docker compose -f "$DOCKER_COMPOSE_FILE" down -v > /dev/null 2>&1 || true
  
  # Remove test image
  echo "Removing test image..."
  docker rmi "${IMAGE_TAG}" > /dev/null 2>&1 || true
  
  # Remove temp directory
  rm -rf "$TEMP_DIR"
  
  exit $exit_code
}
trap cleanup EXIT

# Build the image using local module sources (pre-release CI builds)
echo "Building Docker image (local modules)..."
docker build \
  --platform "${PLATFORM}" \
  -f transports/Dockerfile.local \
  -t "${IMAGE_TAG}" \
  .

echo "Build complete: ${IMAGE_TAG}"

echo ""
echo "=== Testing entrypoint runtime contract ==="

# Each negative check asserts the entrypoint rejects an invalid configuration and
# exits. If a regression lets the configuration through, the entrypoint execs the
# long-running server (see the trailing exec in transports/docker-entrypoint.sh) and
# `docker run` never returns, so every invocation is bounded by `timeout`. Without it
# the job hangs until GitHub's default 360-minute job timeout instead of failing with
# the diagnostic below.
ENTRYPOINT_CHECK_TIMEOUT=60
entrypoint_check_count=0

# GNU coreutils `timeout` is present on the ubuntu runners this script targets. Keep a
# local macOS run working (Homebrew coreutils installs it as `gtimeout`) rather than
# failing every check with "command not found".
if command -v timeout > /dev/null 2>&1; then
  TIMEOUT_BIN=timeout
elif command -v gtimeout > /dev/null 2>&1; then
  TIMEOUT_BIN=gtimeout
else
  TIMEOUT_BIN=""
  echo "WARNING: no timeout(1) found - entrypoint checks will run unbounded"
fi

# Usage: assert_entrypoint_rejects <description> <expected-substring> [docker run args...]
assert_entrypoint_rejects() {
  local description="$1"
  local expected="$2"
  shift 2

  entrypoint_check_count=$((entrypoint_check_count + 1))
  local name="${CONTAINER_NAME}-reject-${entrypoint_check_count}"
  local output status=0

  # `timeout` signals the docker CLI, not the container, so name the container and
  # force-remove it here: the cleanup trap only knows the fixed container names.
  local -a runner=()
  if [ -n "$TIMEOUT_BIN" ]; then
    runner=("$TIMEOUT_BIN" "${ENTRYPOINT_CHECK_TIMEOUT}")
  fi
  output=$("${runner[@]}" docker run --rm \
    --name "${name}" \
    --platform "${PLATFORM}" \
    "$@" \
    "${IMAGE_TAG}" 2>&1) || status=$?
  docker rm -f "${name}" > /dev/null 2>&1 || true

  if [ "$status" -eq 124 ]; then
    echo "ERROR: ${description}"
    echo "       container did not exit within ${ENTRYPOINT_CHECK_TIMEOUT}s - the entrypoint"
    echo "       accepted the configuration and started serving instead of rejecting it"
    echo "$output"
    exit 1
  fi
  if [ "$status" -eq 0 ]; then
    echo "ERROR: ${description}"
    exit 1
  fi
  if ! grep -q "$expected" <<<"$output"; then
    echo "ERROR: ${description} - did not fail clearly (exit ${status})"
    echo "$output"
    exit 1
  fi
}

assert_entrypoint_rejects \
  "image accepted an incomplete UID/GID configuration" \
  "BIFROST_RUN_AS_UID and BIFROST_RUN_AS_GID must be set together" \
  -e BIFROST_RUN_AS_UID=1000

assert_entrypoint_rejects \
  "image accepted an invalid UID/GID configuration" \
  "must be non-negative integers" \
  -e BIFROST_RUN_AS_UID='1000:0' \
  -e BIFROST_RUN_AS_GID=0

assert_entrypoint_rejects \
  "image accepted root as the privilege-drop target" \
  "BIFROST_RUN_AS_UID must be a non-zero UID" \
  --user 0:0 \
  -e BIFROST_RUN_AS_UID=0 \
  -e BIFROST_RUN_AS_GID=0

assert_entrypoint_rejects \
  "image accepted privilege dropping without a root entrypoint" \
  "require the entrypoint to start as root" \
  -e BIFROST_RUN_AS_UID=1000 \
  -e BIFROST_RUN_AS_GID=0

docker volume create "${PRIVDROP_VOLUME_NAME}" >/dev/null
docker run --rm \
  --platform "${PLATFORM}" \
  --user 0:0 \
  --entrypoint sh \
  -v "${PRIVDROP_VOLUME_NAME}:/app/data" \
  "${IMAGE_TAG}" \
  -c 'chown -R 0:0 /app/data && chmod 0700 /app/data'

# shellcheck disable=SC2016 # Literal JSON schema key and env references.
INLINE_CONFIG='{"$schema":"https://www.getbifrost.ai/schema","version":2,"source_of_truth":"split","encryption_key":"env.BIFROST_ENCRYPTION_KEY","client":{"enforce_auth_on_inference":true},"providers":{"openai":{"keys":[{"name":"CI OpenAI key","value":"env.OPENAI_API_KEY","weight":1}]}},"governance":{"auth_config":{"admin_username":"admin","admin_password":"env.BIFROST_ADMIN_PASSWORD","is_enabled":true}}}'
docker run -d \
  --name "${PRIVDROP_CONTAINER_NAME}" \
  --platform "${PLATFORM}" \
  --user 0:0 \
  -p ${PRIVDROP_TEST_PORT}:8080 \
  -e APP_PORT=8080 \
  -e APP_HOST=0.0.0.0 \
  -e BIFROST_CONFIG="${INLINE_CONFIG}" \
  -e BIFROST_ENCRYPTION_KEY=ci-test-encryption-key-32-bytes \
  -e BIFROST_ADMIN_PASSWORD=ci-test-admin-password \
  -e OPENAI_API_KEY=ci-test-provider-key \
  -e BIFROST_RUN_AS_UID=1000 \
  -e BIFROST_RUN_AS_GID=0 \
  -v "${PRIVDROP_VOLUME_NAME}:/app/data" \
  "${IMAGE_TAG}" >/dev/null

MAX_WAIT=60
ELAPSED=0
while [ $ELAPSED -lt $MAX_WAIT ]; do
  if curl -sf "http://localhost:${PRIVDROP_TEST_PORT}/health" >/dev/null 2>&1; then
    break
  fi
  sleep 2
  ELAPSED=$((ELAPSED + 2))
done
if [ $ELAPSED -ge $MAX_WAIT ]; then
  echo "ERROR: privilege-drop container did not become healthy"
  docker logs "${PRIVDROP_CONTAINER_NAME}" 2>&1 | tail -100 || true
  exit 1
fi

LOGIN_RESPONSE="$TEMP_DIR/privdrop-login.json"
LOGIN_COOKIES="$TEMP_DIR/privdrop-login.cookies"
LOGIN_STATUS=$(curl -sS -o "$LOGIN_RESPONSE" -w '%{http_code}' \
  "http://localhost:${PRIVDROP_TEST_PORT}/api/session/login" \
  -c "$LOGIN_COOKIES" \
  -H 'Content-Type: application/json' \
  --data '{"username":"admin","password":"ci-test-admin-password"}')
if [ "$LOGIN_STATUS" != "200" ] || ! jq -e '.message == "Login successful"' "$LOGIN_RESPONSE" >/dev/null; then
  echo "ERROR: generated administrator credentials failed with HTTP ${LOGIN_STATUS}"
  cat "$LOGIN_RESPONSE"
  exit 1
fi

VIRTUAL_KEY_RESPONSE="$TEMP_DIR/privdrop-virtual-key.json"
VIRTUAL_KEY_STATUS=$(curl -sS -o "$VIRTUAL_KEY_RESPONSE" -w '%{http_code}' \
  "http://localhost:${PRIVDROP_TEST_PORT}/api/governance/virtual-keys" \
  -b "$LOGIN_COOKIES" \
  -H 'Content-Type: application/json' \
  --data '{"name":"CI deployment key"}')
if [ "$VIRTUAL_KEY_STATUS" != "200" ] || ! jq -e '.message == "Virtual key created successfully"' "$VIRTUAL_KEY_RESPONSE" >/dev/null; then
  echo "ERROR: authenticated virtual-key creation failed with HTTP ${VIRTUAL_KEY_STATUS}"
  cat "$VIRTUAL_KEY_RESPONSE"
  exit 1
fi
VIRTUAL_KEY_ID=$(jq -er '.virtual_key.id' "$VIRTUAL_KEY_RESPONSE")

ANONYMOUS_STATUS=$(curl -sS -o /dev/null -w '%{http_code}' \
  "http://localhost:${PRIVDROP_TEST_PORT}/v1/chat/completions" \
  -H 'Content-Type: application/json' \
  --data '{"model":"openai/gpt-4o-mini","messages":[{"role":"user","content":"hello"}]}')
if [ "$ANONYMOUS_STATUS" != "401" ]; then
  echo "ERROR: anonymous inference returned HTTP ${ANONYMOUS_STATUS}, expected 401"
  exit 1
fi

PID_UID=$(docker exec --user 0:0 "${PRIVDROP_CONTAINER_NAME}" awk '/^Uid:/{print $2}' /proc/1/status)
if [ "$PID_UID" != "1000" ]; then
  echo "ERROR: PID 1 runs as UID ${PID_UID}, expected 1000"
  exit 1
fi

CONFIG_METADATA=$(docker exec --user 0:0 "${PRIVDROP_CONTAINER_NAME}" stat -c '%u:%g %a' /app/data/config.json)
if [ "$CONFIG_METADATA" != "1000:0 600" ]; then
  echo "ERROR: materialized config metadata is '${CONFIG_METADATA}', expected '1000:0 600'"
  exit 1
fi

if ! PROCESS_ENV=$(docker exec --privileged --user 0:0 "${PRIVDROP_CONTAINER_NAME}" sh -c \
  'tr "\0" "\n" </proc/1/environ'); then
  echo "ERROR: could not inspect the Bifrost process environment"
  exit 1
fi
if grep -q '^BIFROST_CONFIG=' <<<"$PROCESS_ENV"; then
  echo "ERROR: BIFROST_CONFIG leaked into the Bifrost process environment"
  exit 1
fi

docker restart "${PRIVDROP_CONTAINER_NAME}" >/dev/null
ELAPSED=0
while [ $ELAPSED -lt $MAX_WAIT ]; do
  if curl -sf "http://localhost:${PRIVDROP_TEST_PORT}/health" >/dev/null 2>&1; then
    break
  fi
  sleep 2
  ELAPSED=$((ELAPSED + 2))
done
if [ $ELAPSED -ge $MAX_WAIT ]; then
  echo "ERROR: privilege-drop container did not become healthy after restart"
  docker logs "${PRIVDROP_CONTAINER_NAME}" 2>&1 | tail -100 || true
  exit 1
fi

PERSISTED_KEY_STATUS=$(curl -sS -o /dev/null -w '%{http_code}' \
  "http://localhost:${PRIVDROP_TEST_PORT}/api/governance/virtual-keys/${VIRTUAL_KEY_ID}" \
  -b "$LOGIN_COOKIES")
if [ "$PERSISTED_KEY_STATUS" != "200" ]; then
  echo "ERROR: virtual key did not survive container restart (HTTP ${PERSISTED_KEY_STATUS})"
  exit 1
fi

CONFIG_METADATA=$(docker exec --user 0:0 "${PRIVDROP_CONTAINER_NAME}" stat -c '%u:%g %a' /app/data/config.json)
if [ "$CONFIG_METADATA" != "1000:0 600" ]; then
  echo "ERROR: restarted config metadata is '${CONFIG_METADATA}', expected '1000:0 600'"
  exit 1
fi

# Mixed ownership: /app/data itself is already 1000:0 from the first start, but
# a database inside it is root-owned, exactly as an upgrade from a root-only
# release leaves it. Repairing only on a top-level mismatch would skip these and
# leave Bifrost to fail opening them, and the create-directory probe cannot see
# them either.
MISOWNED_FILES=$(docker exec --user 0:0 "${PRIVDROP_CONTAINER_NAME}" sh -c '
  files=$(find /app/data -maxdepth 1 -type f ! -name config.json)
  [ -n "$files" ] || exit 1
  for file in $files; do
    chown 0:0 "$file" || exit 1
    chmod 0600 "$file" || exit 1
  done
  printf %s "$files"
')
if [ -z "$MISOWNED_FILES" ]; then
  echo "ERROR: no data files to misown; the mixed-ownership repair case is untested"
  exit 1
fi
echo "Misowned data files for the repair check:"
echo "${MISOWNED_FILES}"

DATA_DIR_OWNER=$(docker exec --user 0:0 "${PRIVDROP_CONTAINER_NAME}" stat -c '%u:%g' /app/data)
if [ "$DATA_DIR_OWNER" != "1000:0" ]; then
  echo "ERROR: /app/data is ${DATA_DIR_OWNER}; the mixed-ownership case needs a correctly owned parent"
  exit 1
fi

docker restart "${PRIVDROP_CONTAINER_NAME}" >/dev/null
ELAPSED=0
while [ $ELAPSED -lt $MAX_WAIT ]; do
  if curl -sf "http://localhost:${PRIVDROP_TEST_PORT}/health" >/dev/null 2>&1; then
    break
  fi
  sleep 2
  ELAPSED=$((ELAPSED + 2))
done
if [ $ELAPSED -ge $MAX_WAIT ]; then
  echo "ERROR: container did not recover from root-owned data files inside a correctly owned /app/data"
  docker logs "${PRIVDROP_CONTAINER_NAME}" 2>&1 | tail -100 || true
  exit 1
fi

while IFS= read -r misowned_file; do
  [ -n "$misowned_file" ] || continue
  FILE_OWNER=$(docker exec --user 0:0 "${PRIVDROP_CONTAINER_NAME}" stat -c '%u:%g' "$misowned_file")
  if [ "$FILE_OWNER" != "1000:0" ]; then
    echo "ERROR: ${misowned_file} is owned by ${FILE_OWNER} after the repair, expected 1000:0"
    exit 1
  fi
done <<<"$MISOWNED_FILES"

docker stop "${PRIVDROP_CONTAINER_NAME}" >/dev/null
docker rm "${PRIVDROP_CONTAINER_NAME}" >/dev/null

# Same repair, but with config.json mounted read-only — the shape a platform
# produces when it projects the document out of a secret store. config.json is
# only ever read, so the repair must reach the writable data paths without
# recursing over it: chown fails on a read-only mount, and a repair that fixed
# everything it was supposed to would then announce itself as failed and send an
# operator hunting a problem that does not exist.
MOUNTED_CONFIG="$TEMP_DIR/mounted-config.json"
printf '%s' "${INLINE_CONFIG}" > "$MOUNTED_CONFIG"
chmod 0444 "$MOUNTED_CONFIG"

MISOWNED_FILES=$(docker run --rm \
  --platform "${PLATFORM}" \
  --user 0:0 \
  --entrypoint sh \
  -v "${PRIVDROP_VOLUME_NAME}:/app/data" \
  "${IMAGE_TAG}" \
  -c '
    files=$(find /app/data -maxdepth 1 -type f ! -name config.json)
    [ -n "$files" ] || exit 1
    for file in $files; do
      chown 0:0 "$file" || exit 1
    done
    printf %s "$files"
  ')
if [ -z "$MISOWNED_FILES" ]; then
  echo "ERROR: no data files to misown; the read-only config.json repair case is untested"
  exit 1
fi

docker run -d \
  --name "${RO_CONFIG_CONTAINER_NAME}" \
  --platform "${PLATFORM}" \
  --user 0:0 \
  -e APP_PORT=8080 \
  -e APP_HOST=0.0.0.0 \
  -e BIFROST_ENCRYPTION_KEY=ci-test-encryption-key-32-bytes \
  -e BIFROST_ADMIN_PASSWORD=ci-test-admin-password \
  -e OPENAI_API_KEY=ci-test-provider-key \
  -e BIFROST_RUN_AS_UID=1000 \
  -e BIFROST_RUN_AS_GID=0 \
  -v "${PRIVDROP_VOLUME_NAME}:/app/data" \
  -v "${MOUNTED_CONFIG}:/app/data/config.json:ro" \
  "${IMAGE_TAG}" >/dev/null

REPAIR_LOGS=""
ELAPSED=0
while [ $ELAPSED -lt $MAX_WAIT ]; do
  REPAIR_LOGS=$(docker logs "${RO_CONFIG_CONTAINER_NAME}" 2>&1)
  if grep -qE '(Successfully updated|Could not update) permissions on /app/data' <<<"$REPAIR_LOGS"; then
    break
  fi
  sleep 2
  ELAPSED=$((ELAPSED + 2))
done

if ! grep -q 'Successfully updated permissions on /app/data' <<<"$REPAIR_LOGS"; then
  echo "ERROR: the ownership repair did not report success alongside a read-only config.json"
  echo "${REPAIR_LOGS}" | tail -50
  exit 1
fi

while IFS= read -r misowned_file; do
  [ -n "$misowned_file" ] || continue
  FILE_OWNER=$(docker run --rm \
    --platform "${PLATFORM}" \
    --user 0:0 \
    --entrypoint sh \
    -v "${PRIVDROP_VOLUME_NAME}:/app/data" \
    "${IMAGE_TAG}" \
    -c "stat -c '%u:%g' '${misowned_file}'")
  if [ "$FILE_OWNER" != "1000:0" ]; then
    echo "ERROR: ${misowned_file} is owned by ${FILE_OWNER} after the repair, expected 1000:0"
    echo "${REPAIR_LOGS}" | tail -50
    exit 1
  fi
done <<<"$MISOWNED_FILES"

docker stop "${RO_CONFIG_CONTAINER_NAME}" >/dev/null
docker rm "${RO_CONFIG_CONTAINER_NAME}" >/dev/null
docker volume rm "${PRIVDROP_VOLUME_NAME}" >/dev/null

echo "Entrypoint runtime contract passed"

# Start the PostgreSQL dependency used by this image smoke test. The broader
# framework suite owns the remaining services in tests/docker-compose.yml.
echo ""
echo "=== Starting PostgreSQL ==="
docker compose -f "$DOCKER_COMPOSE_FILE" up -d postgres

# Wait for Postgres to be ready
echo "Waiting for Postgres to be ready..."
MAX_WAIT=60
ELAPSED=0
while [ $ELAPSED -lt $MAX_WAIT ]; do
  if docker compose -f "$DOCKER_COMPOSE_FILE" exec -T postgres pg_isready -U bifrost -d bifrost > /dev/null 2>&1; then
    echo "Postgres is ready"
    break
  fi
  sleep 2
  ELAPSED=$((ELAPSED + 2))
done

if [ $ELAPSED -ge $MAX_WAIT ]; then
  echo "ERROR: Postgres did not become ready within ${MAX_WAIT}s"
  docker compose -f "$DOCKER_COMPOSE_FILE" logs postgres
  exit 1
fi

# Get the docker network name
NETWORK_NAME=$(docker compose -f "$DOCKER_COMPOSE_FILE" ps --format json | head -1 | jq -r '.Networks' 2>/dev/null || echo "tests_bifrost_network")
if [ -z "$NETWORK_NAME" ] || [ "$NETWORK_NAME" = "null" ]; then
  NETWORK_NAME="tests_bifrost_network"
fi

# Generate config.json with all providers and Postgres stores.
# NOTE: postgres host is hard-coded to 172.28.0.16 (the pinned bridge IP from
# tests/docker-compose.yml) instead of the hostname "postgres" because
# harden-runner interferes with Docker's embedded DNS at 127.0.0.11:53 even
# when that endpoint is in allowed-endpoints. Using the IP directly bypasses
# DNS and goes through the iptables forward chain where harden-runner's
# allow-list (which includes 172.28.0.16:5432) works correctly.
echo ""
echo "=== Generating config.json ==="
cat > "$CONFIG_FILE" << 'CONFIGEOF'
{
  "$schema": "https://www.getbifrost.ai/schema",
  "providers": {
    "openai": {
      "keys": [{ "name": "OpenAI API Key", "value": "env.OPENAI_API_KEY", "weight": 1, "use_for_batch_api": true }],
      "network_config": { "default_request_timeout_in_seconds": 300 }
    },
    "elevenlabs": {
      "keys": [{ "name": "ElevenLabs API Key", "value": "env.ELEVENLABS_API_KEY", "weight": 1, "use_for_batch_api": true }],
      "network_config": { "default_request_timeout_in_seconds": 300 }
    },
    "xai": {
      "keys": [{ "name": "Xai API Key", "value": "env.XAI_API_KEY", "weight": 1, "use_for_batch_api": true }],
      "network_config": { "default_request_timeout_in_seconds": 300 }
    },
    "huggingface": {
      "keys": [{ "name": "Hugging Face API Key", "value": "env.HUGGING_FACE_API_KEY", "weight": 1, "use_for_batch_api": true }],
      "network_config": { "default_request_timeout_in_seconds": 300 }
    },
    "anthropic": {
      "keys": [{ "name": "Anthropic API Key", "value": "env.ANTHROPIC_API_KEY", "weight": 1, "use_for_batch_api": true }],
      "network_config": { "default_request_timeout_in_seconds": 300 }
    },
    "gemini": {
      "keys": [{ "value": "env.GEMINI_API_KEY", "weight": 1, "use_for_batch_api": true, "name": "Gemini API Key" }],
      "network_config": { "default_request_timeout_in_seconds": 300 }
    },
    "vertex": {
      "keys": [{ "name": "Vertex API Key", "vertex_key_config": { "project_id": "env.VERTEX_PROJECT_ID", "region": "env.GOOGLE_LOCATION", "auth_credentials": "env.VERTEX_CREDENTIALS" }, "weight": 1 }],
      "network_config": { "default_request_timeout_in_seconds": 300 }
    },
    "mistral": {
      "keys": [{ "name": "Mistral API Key", "value": "env.MISTRAL_API_KEY", "weight": 1 }],
      "network_config": { "default_request_timeout_in_seconds": 300 }
    },
    "cohere": {
      "keys": [{ "name": "Cohere API Key", "value": "env.COHERE_API_KEY", "weight": 1 }],
      "network_config": { "default_request_timeout_in_seconds": 300 }
    },
    "groq": {
      "keys": [{ "name": "Groq API Key", "value": "env.GROQ_API_KEY", "weight": 1 }],
      "network_config": { "default_request_timeout_in_seconds": 300 }
    },
    "perplexity": {
      "keys": [{ "name": "Perplexity API Key", "value": "env.PERPLEXITY_API_KEY", "weight": 1 }],
      "network_config": { "default_request_timeout_in_seconds": 300 }
    },
    "cerebras": {
      "keys": [{ "name": "Cerebras API Key", "value": "env.CEREBRAS_API_KEY", "weight": 1 }],
      "network_config": { "default_request_timeout_in_seconds": 300 }
    },
    "openrouter": {
      "keys": [{ "name": "OpenRouter API Key", "value": "env.OPENROUTER_API_KEY", "weight": 1 }],
      "network_config": { "default_request_timeout_in_seconds": 300 }
    },
    "parasail": {
      "keys": [{ "name": "Parasail API Key", "value": "env.PARASAIL_API_KEY", "weight": 1 }],
      "network_config": { "default_request_timeout_in_seconds": 300 }
    },
    "azure": {
      "keys": [{ "name": "Azure API Key", "value": "env.AZURE_API_KEY", "azure_key_config": { "endpoint": "env.AZURE_ENDPOINT", "api_version": "env.AZURE_API_VERSION" }, "weight": 1 }],
      "network_config": { "default_request_timeout_in_seconds": 300 }
    },
    "bedrock": {
      "keys": [{ "name": "Bedrock API Key", "bedrock_key_config": { "access_key": "env.AWS_ACCESS_KEY_ID", "secret_key": "env.AWS_SECRET_ACCESS_KEY", "region": "env.AWS_REGION", "arn": "env.AWS_ARN" }, "weight": 1, "use_for_batch_api": true }],
      "network_config": { "default_request_timeout_in_seconds": 300 }
    },
    "replicate": {
      "keys": [{ "name": "Replicate API KEY", "value": "env.REPLICATE_API_KEY", "weight": 1.0, "use_for_batch_api": true }]
    }
  },
  "config_store": {
    "enabled": true,
    "type": "postgres",
    "config": {
      "host": "172.28.0.16",
      "port": "5432",
      "user": "bifrost",
      "password": "bifrost_password",
      "db_name": "bifrost",
      "ssl_mode": "disable"
    }
  },
  "logs_store": {
    "enabled": true,
    "type": "postgres",
    "config": {
      "host": "172.28.0.16",
      "port": "5432",
      "user": "bifrost",
      "password": "bifrost_password",
      "db_name": "bifrost",
      "ssl_mode": "disable"
    }
  },
  "governance": {
    "virtual_keys": [
      {
        "id": "vk-test",
        "value": "sk-bf-test-key",
        "is_active": true,
        "name": "vk-test"
      }
    ]
  },
  "client": {
    "drop_excess_requests": false,
    "initial_pool_size": 300,
    "allowed_origins": ["http://localhost:3000", "https://localhost:3000"],
    "enable_logging": true,
    "enforce_auth_on_inference": false,
    "max_request_body_size_mb": 100
  },
  "encryption_key": ""
}
CONFIGEOF

echo "Config file created at: $CONFIG_FILE"

# Run the Bifrost container connected to the docker-compose network
echo ""
echo "=== Starting Bifrost container ==="
docker run -d \
  --name "${CONTAINER_NAME}" \
  --platform "${PLATFORM}" \
  --network "${NETWORK_NAME}" \
  -p ${TEST_PORT}:8080 \
  -e APP_PORT=8080 \
  -e APP_HOST=0.0.0.0 \
  -e OPENAI_API_KEY="${OPENAI_API_KEY:-}" \
  -e ELEVENLABS_API_KEY="${ELEVENLABS_API_KEY:-}" \
  -e XAI_API_KEY="${XAI_API_KEY:-}" \
  -e HUGGING_FACE_API_KEY="${HUGGING_FACE_API_KEY:-}" \
  -e ANTHROPIC_API_KEY="${ANTHROPIC_API_KEY:-}" \
  -e GEMINI_API_KEY="${GEMINI_API_KEY:-}" \
  -e VERTEX_PROJECT_ID="${VERTEX_PROJECT_ID:-}" \
  -e VERTEX_CREDENTIALS="${VERTEX_CREDENTIALS:-}" \
  -e GOOGLE_LOCATION="${GOOGLE_LOCATION:-us-central1}" \
  -e MISTRAL_API_KEY="${MISTRAL_API_KEY:-}" \
  -e COHERE_API_KEY="${COHERE_API_KEY:-}" \
  -e GROQ_API_KEY="${GROQ_API_KEY:-}" \
  -e PERPLEXITY_API_KEY="${PERPLEXITY_API_KEY:-}" \
  -e CEREBRAS_API_KEY="${CEREBRAS_API_KEY:-}" \
  -e OPENROUTER_API_KEY="${OPENROUTER_API_KEY:-}" \
  -e PARASAIL_API_KEY="${PARASAIL_API_KEY:-}" \
  -e AZURE_API_KEY="${AZURE_API_KEY:-}" \
  -e AZURE_ENDPOINT="${AZURE_ENDPOINT:-}" \
  -e AZURE_API_VERSION="${AZURE_API_VERSION:-}" \
  -e AWS_ACCESS_KEY_ID="${AWS_ACCESS_KEY_ID:-}" \
  -e AWS_SECRET_ACCESS_KEY="${AWS_SECRET_ACCESS_KEY:-}" \
  -e AWS_REGION="${AWS_REGION:-us-east-1}" \
  -e AWS_ARN="${AWS_ARN:-}" \
  -e REPLICATE_API_KEY="${REPLICATE_API_KEY:-}" \
  -v "$CONFIG_FILE:/app/data/config.json:ro" \
  "${IMAGE_TAG}"

# Wait for Bifrost to be ready
echo "Waiting for Bifrost to start..."
MAX_WAIT=60
ELAPSED=0
HEALTH_OK=0
while [ $ELAPSED -lt $MAX_WAIT ]; do
  if curl -sf "http://localhost:${TEST_PORT}/health" > /dev/null 2>&1; then
    echo "Bifrost health check passed (attempt $((ELAPSED/2 + 1)))"
    HEALTH_OK=1
    break
  fi
  sleep 2
  ELAPSED=$((ELAPSED + 2))
done

if [ $HEALTH_OK -eq 0 ]; then
  echo "ERROR: Bifrost health check failed!"
  echo "Container logs:"
  docker logs "${CONTAINER_NAME}" 2>&1 | tail -100 || true
  exit 1
fi

DEFAULT_PID_UID=$(docker exec "${CONTAINER_NAME}" awk '/^Uid:/{print $2}' /proc/1/status)
if [ "$DEFAULT_PID_UID" != "1000" ]; then
  echo "ERROR: default PID 1 runs as UID ${DEFAULT_PID_UID}, expected 1000"
  exit 1
fi

# # Run E2E API tests
# echo ""
# echo "=== Running E2E API tests ==="
# export BIFROST_BASE_URL="http://localhost:${TEST_PORT}"
# export CI=1

# echo pwd: $(pwd)
# # Run the E2E API test scripts (marked as flaky - failures are logged but don't block)
# if ! ./tests/e2e/api/runners/run-newman-inference-tests.sh; then
#   echo "WARNING: runners/run-newman-inference-tests.sh failed (flaky test - continuing)"
# fi
# if ! ./tests/e2e/api/run-all-integrations.sh; then
#   echo "WARNING: run-all-integrations.sh failed (flaky test - continuing)"
# fi
# if ! ./tests/e2e/api/runners/run-newman-api-tests.sh; then
#   echo "WARNING: run-newman-api-tests.sh failed (flaky test - continuing)"
# fi

# echo ""
# echo "=== Docker image E2E API test passed for ${PLATFORM} ==="
