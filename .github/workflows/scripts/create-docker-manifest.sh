# Validate input argument
if [ "${1:-}" = "" ]; then
  echo "Usage: $0 <version>" >&2
  exit 1
fi

VERSION="$1"
REGISTRY="docker.io"
ACCOUNT="maximhq"
IMAGE_NAME="bifrost"
IMAGE="${REGISTRY}/${ACCOUNT}/${IMAGE_NAME}"

# Get the actual image digests from the platform-specific builds
AMD64_DIGEST=$(docker manifest inspect ${IMAGE}:v${VERSION}-amd64 | jq -r '.manifests[0].digest')
ARM64_DIGEST=$(docker manifest inspect ${IMAGE}:v${VERSION}-arm64 | jq -r '.manifests[0].digest')

echo "AMD64 digest: ${AMD64_DIGEST}"
echo "ARM64 digest: ${ARM64_DIGEST}"

# Create manifest for versioned tag using digests
docker manifest create \
    ${IMAGE}:v${VERSION} \
    ${IMAGE}@${AMD64_DIGEST} \
    ${IMAGE}@${ARM64_DIGEST}

docker manifest push ${IMAGE}:v${VERSION}

# Create latest manifest only for stable versions
if [[ "$VERSION" != *-* ]]; then
    docker manifest create \
        ${IMAGE}:latest \
        ${IMAGE}@${AMD64_DIGEST} \
        ${IMAGE}@${ARM64_DIGEST}
    
    docker manifest push ${IMAGE}:latest
fi