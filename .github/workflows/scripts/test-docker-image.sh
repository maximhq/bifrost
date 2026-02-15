#!/bin/bash
set -e

# Test Docker image by building and running health check
# Usage: ./test-docker-image.sh <platform>
# Example: ./test-docker-image.sh linux/amd64

PLATFORM=${1:-linux/amd64}
ARCH=$(echo "$PLATFORM" | cut -d'/' -f2)
IMAGE_TAG="bifrost-test:ci-${GITHUB_SHA:-local}-${ARCH}"
CONTAINER_NAME="bifrost-test-${ARCH}"
TEST_PORT=18080

echo "=== Testing Docker image for ${PLATFORM} ==="

# Build the image
echo "Building Docker image..."
docker build \
  --platform "${PLATFORM}" \
  -f transports/Dockerfile \
  -t "${IMAGE_TAG}" \
  .

echo "Build complete: ${IMAGE_TAG}"

# Run the container
echo "Starting container..."
docker run -d \
  --name "${CONTAINER_NAME}" \
  --platform "${PLATFORM}" \
  -p ${TEST_PORT}:8080 \
  -e APP_PORT=8080 \
  -e APP_HOST=0.0.0.0 \
  "${IMAGE_TAG}"

# Wait for container to be ready
echo "Waiting for container to start..."
sleep 5

# Health check with retries
HEALTH_OK=0
for i in $(seq 1 15); do
  if curl -sf "http://localhost:${TEST_PORT}/health" > /dev/null 2>&1; then
    echo "Health check passed (attempt ${i})"
    HEALTH_OK=1
    break
  fi
  echo "Waiting for health endpoint (attempt ${i}/15)..."
  sleep 2
done

# Check result and cleanup
if [ ${HEALTH_OK} -eq 0 ]; then
  echo "ERROR: Health check failed!"
  echo "Container logs:"
  docker logs "${CONTAINER_NAME}" 2>&1 | tail -100 || true
  echo ""
  echo "Cleaning up failed test resources..."
  docker stop "${CONTAINER_NAME}" > /dev/null 2>&1 || true
  docker rm "${CONTAINER_NAME}" > /dev/null 2>&1 || true
  docker rmi "${IMAGE_TAG}" > /dev/null 2>&1 || true
  exit 1
fi

# Success - cleanup container and image
echo "Stopping container..."
docker stop "${CONTAINER_NAME}" > /dev/null 2>&1 || true
docker rm "${CONTAINER_NAME}" > /dev/null 2>&1 || true
echo "Cleaning up test image..."
docker rmi "${IMAGE_TAG}" > /dev/null 2>&1 || true

echo ""
echo "=== Docker image test passed for ${PLATFORM} ==="
