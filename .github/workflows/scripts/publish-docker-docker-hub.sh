#!/bin/bash
set -euo pipefail

# ----------- CONFIG -----------

REGISTRY="docker.io"
ACCOUNT="maximhq"
IMAGE_NAME="bifrost"
IMAGE="${REGISTRY}/${ACCOUNT}/${IMAGE_NAME}"
DOCKERFILE="transports/Dockerfile"
CONTEXT_DIR="."
CACHE_DIR=".buildx-cache"
BUILDER_NAME="multiarch-builder"
PLATFORMS="linux/amd64,linux/arm64"

# ----------- AUTH -----------

DOCKER_USERNAME="${DOCKER_USERNAME:-}"
DOCKER_PASSWORD="${DOCKER_PASSWORD:-}"

if [[ -z "$DOCKER_USERNAME" ]]; then
  read -rp "🔑 Docker Hub username: " DOCKER_USERNAME
fi

if [[ -z "$DOCKER_PASSWORD" ]]; then
  read -rsp "🔐 Docker Hub password: " DOCKER_PASSWORD
  echo
fi

echo "🔐 Logging into Docker Hub..."
echo "$DOCKER_PASSWORD" | docker login --username "$DOCKER_USERNAME" --password-stdin

# ----------- INSTALL QEMU & BUILDX -----------

echo "🔧 Installing QEMU and ensuring Buildx is ready..."

docker run --privileged --rm tonistiigi/binfmt --install all

if ! docker buildx version >/dev/null 2>&1; then
  echo "❌ Docker Buildx is not available. Please upgrade Docker."
  exit 1
fi

if ! docker buildx inspect "$BUILDER_NAME" >/dev/null 2>&1; then
  docker buildx create --use --name "$BUILDER_NAME"
else
  docker buildx use "$BUILDER_NAME"
fi

docker buildx inspect --bootstrap

# ----------- VERSION -----------

RAW_VERSION=${1:-$(git describe --tags --abbrev=0 2>/dev/null || echo "transports/v0.0.0")}
VERSION_ONLY="${RAW_VERSION#transports/}"
VERSION_ONLY="${VERSION_ONLY#v}"
VERSION="v${VERSION_ONLY}"

TAGS=(
  "${IMAGE}:${VERSION}"
  "${IMAGE}:latest"
)

LABELS=(
  "org.opencontainers.image.title=Bifrost LLM Gateway (HTTP)"
  "org.opencontainers.image.description=The fastest LLM gateway written in Go. Learn more here: https://github.com/maximhq/bifrost"
  "org.opencontainers.image.source=https://github.com/maximhq/bifrost"
  "org.opencontainers.image.version=${VERSION}"
  "org.opencontainers.image.created=$(date -u +'%Y-%m-%dT%H:%M:%SZ')"
  "org.opencontainers.image.revision=$(git rev-parse HEAD)"
)

# ----------- BUILD -----------

mkdir -p "$CACHE_DIR"

echo "🚀 Building and pushing Docker image: ${IMAGE}:${VERSION}"

BUILD_ARGS=()

for tag in "${TAGS[@]}"; do
  BUILD_ARGS+=(--tag "$tag")
done

for label in "${LABELS[@]}"; do
  BUILD_ARGS+=(--label "$label")
done

docker buildx build \
  --platform "$PLATFORMS" \
  --file "$DOCKERFILE" \
  --push \
  --cache-from=type=local,src="$CACHE_DIR" \
  --cache-to=type=local,dest="$CACHE_DIR",mode=max \
  "${BUILD_ARGS[@]}" \
  "$CONTEXT_DIR"


# ----------- CLEANUP -----------

echo "🧼 Cleanup: Pruning Buildx cache (non-destructive)..."
docker buildx prune --force

echo "👋 Logging out of Docker Hub..."
docker logout "$REGISTRY"

echo "✅ Done."