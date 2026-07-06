#!/usr/bin/env bash
set -euo pipefail

if [ "${1:-}" = "" ]; then
  echo "Usage: $0 <version>" >&2
  exit 1
fi

VERSION="$1"
REGISTRY="docker.io"
ACCOUNT="maximhq"
IMAGE_NAME="bifrost"
IMAGE="${REGISTRY}/${ACCOUNT}/${IMAGE_NAME}"

# Filter by platform.architecture rather than positional [0]:
# `docker/build-push-action` with default provenance produces an OCI image
# index containing the platform image manifest AND a provenance attestation
# manifest. Selecting by architecture survives any buildx ordering change.
AMD64_DIGEST=$(docker manifest inspect "${IMAGE}:v${VERSION}-ubi9-amd64" | jq -er '.manifests[] | select(.platform.architecture == "amd64") | .digest')
ARM64_DIGEST=$(docker manifest inspect "${IMAGE}:v${VERSION}-ubi9-arm64" | jq -er '.manifests[] | select(.platform.architecture == "arm64") | .digest')

echo "UBI9 AMD64 digest: ${AMD64_DIGEST}"
echo "UBI9 ARM64 digest: ${ARM64_DIGEST}"

docker manifest create \
    "${IMAGE}:v${VERSION}-ubi9" \
    "${IMAGE}@${AMD64_DIGEST}" \
    "${IMAGE}@${ARM64_DIGEST}"

docker manifest push "${IMAGE}:v${VERSION}-ubi9"

if [[ "$VERSION" != *-* ]]; then
    docker manifest create \
        "${IMAGE}:latest-ubi9" \
        "${IMAGE}@${AMD64_DIGEST}" \
        "${IMAGE}@${ARM64_DIGEST}"

    docker manifest push "${IMAGE}:latest-ubi9"
fi

# Additionally mirror the UBI9 multi-arch manifest to GitHub Container Registry (ghcr.io).
# This is purely additive and does not affect the Docker Hub tags above. New GHCR
# packages are PRIVATE by default — see create-docker-manifest.sh header for the
# one-time visibility flip required for anonymous pulls.
if [ -n "${GITHUB_REPOSITORY:-}" ]; then
  (
    set -e
    GHCR_IMAGE="ghcr.io/$(echo "${GITHUB_REPOSITORY}" | tr '[:upper:]' '[:lower:]')"

    GHCR_AMD64_DIGEST=$(docker manifest inspect "${GHCR_IMAGE}:v${VERSION}-ubi9-amd64" | jq -er '.manifests[] | select(.platform.architecture == "amd64") | .digest')
    GHCR_ARM64_DIGEST=$(docker manifest inspect "${GHCR_IMAGE}:v${VERSION}-ubi9-arm64" | jq -er '.manifests[] | select(.platform.architecture == "arm64") | .digest')

    echo "GHCR UBI9 AMD64 digest: ${GHCR_AMD64_DIGEST}"
    echo "GHCR UBI9 ARM64 digest: ${GHCR_ARM64_DIGEST}"

    docker manifest create \
        "${GHCR_IMAGE}:v${VERSION}-ubi9" \
        "${GHCR_IMAGE}@${GHCR_AMD64_DIGEST}" \
        "${GHCR_IMAGE}@${GHCR_ARM64_DIGEST}"

    docker manifest push "${GHCR_IMAGE}:v${VERSION}-ubi9"

    if [[ "$VERSION" != *-* ]]; then
        docker manifest create \
            "${GHCR_IMAGE}:latest-ubi9" \
            "${GHCR_IMAGE}@${GHCR_AMD64_DIGEST}" \
            "${GHCR_IMAGE}@${GHCR_ARM64_DIGEST}"

        docker manifest push "${GHCR_IMAGE}:latest-ubi9"
    fi
  ) || echo "::warning::GHCR mirroring failed; Docker Hub publish unaffected"
fi
