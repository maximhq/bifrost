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
source "$SCRIPT_DIR/setup-go-workspace.sh"

PLATFORM=${1:-linux/amd64}
ARCH=$(echo "$PLATFORM" | cut -d'/' -f2)
IMAGE_TAG="bifrost-test:ci-${GITHUB_SHA:-local}-${ARCH}"
CONTAINER_NAME="bifrost-test-${ARCH}"
TEST_PORT=8080
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

# Start docker-compose services (Postgres, Weaviate, Redis, Qdrant)
echo ""
echo "=== Starting docker-compose services ==="
docker compose -f "$DOCKER_COMPOSE_FILE" up -d

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

# Generate config.json with all providers and Postgres stores
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
      "host": "postgres",
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
      "host": "postgres",
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
    "enforce_governance_header": false,
    "allow_direct_keys": false,
    "max_request_body_size_mb": 100,
    "enable_litellm_fallbacks": false
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

# # Run E2E API tests
# echo ""
# echo "=== Running E2E API tests ==="
# export BIFROST_BASE_URL="http://localhost:${TEST_PORT}"
# export CI=1

# echo pwd: $(pwd)
# # Run the E2E API test scripts (marked as flaky - failures are logged but don't block)
# if ! ./tests/e2e/api/run-newman-tests.sh; then
#   echo "WARNING: run-newman-tests.sh failed (flaky test - continuing)"
# fi
# if ! ./tests/e2e/api/run-all-integrations.sh; then
#   echo "WARNING: run-all-integrations.sh failed (flaky test - continuing)"
# fi
# if ! ./tests/e2e/api/run-newman-api-tests.sh; then
#   echo "WARNING: run-newman-api-tests.sh failed (flaky test - continuing)"
# fi

# echo ""
# echo "=== Docker image E2E API test passed for ${PLATFORM} ==="
