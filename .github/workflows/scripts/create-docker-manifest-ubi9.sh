if [ "${1:-}" = "" ]; then
  echo "Usage: $0 <version>" >&2
  exit 1
fi

VERSION="$1"
REGISTRY="docker.io"
ACCOUNT="maximhq"
IMAGE_NAME="bifrost"
IMAGE="${REGISTRY}/${ACCOUNT}/${IMAGE_NAME}"

AMD64_DIGEST=$(docker manifest inspect ${IMAGE}:v${VERSION}-ubi9-amd64 | jq -r '.manifests[0].digest')
ARM64_DIGEST=$(docker manifest inspect ${IMAGE}:v${VERSION}-ubi9-arm64 | jq -r '.manifests[0].digest')

echo "UBI9 AMD64 digest: ${AMD64_DIGEST}"
echo "UBI9 ARM64 digest: ${ARM64_DIGEST}"

docker manifest create \
    ${IMAGE}:v${VERSION}-ubi9 \
    ${IMAGE}@${AMD64_DIGEST} \
    ${IMAGE}@${ARM64_DIGEST}

docker manifest push ${IMAGE}:v${VERSION}-ubi9

if [[ "$VERSION" != *-* ]]; then
    docker manifest create \
        ${IMAGE}:latest-ubi9 \
        ${IMAGE}@${AMD64_DIGEST} \
        ${IMAGE}@${ARM64_DIGEST}

    docker manifest push ${IMAGE}:latest-ubi9
fi
