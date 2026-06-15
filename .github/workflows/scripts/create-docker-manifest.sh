#!/usr/bin/env bash
set -euo pipefail

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

# Get the actual image digests from the platform-specific builds.
# Filter by platform.architecture rather than relying on positional [0]:
# `docker/build-push-action` with default provenance creates an OCI image
# index containing the platform image manifest AND a provenance attestation
# manifest, and the ordering is not guaranteed. Selecting by architecture
# is robust to buildx changing the layout.
AMD64_DIGEST=$(docker manifest inspect "${IMAGE}:v${VERSION}-amd64" | jq -er '.manifests[] | select(.platform.architecture == "amd64") | .digest')
ARM64_DIGEST=$(docker manifest inspect "${IMAGE}:v${VERSION}-arm64" | jq -er '.manifests[] | select(.platform.architecture == "arm64") | .digest')

echo "AMD64 digest: ${AMD64_DIGEST}"
echo "ARM64 digest: ${ARM64_DIGEST}"

# Create manifest for versioned tag using digests
docker manifest create \
    "${IMAGE}:v${VERSION}" \
    "${IMAGE}@${AMD64_DIGEST}" \
    "${IMAGE}@${ARM64_DIGEST}"

docker manifest push "${IMAGE}:v${VERSION}"

# Create latest manifest only for stable versions
if [[ "$VERSION" != *-* ]]; then
    docker manifest create \
        "${IMAGE}:latest" \
        "${IMAGE}@${AMD64_DIGEST}" \
        "${IMAGE}@${ARM64_DIGEST}"

    docker manifest push "${IMAGE}:latest"
fi

# Additionally mirror the multi-arch manifest to GitHub Container Registry (ghcr.io).
# This is purely additive and does not affect the Docker Hub tags above.
#
# NOTE on first run / package visibility: GHCR creates new container packages as
# PRIVATE by default. Until a maintainer flips visibility for the package
# (`https://github.com/orgs/<owner>/packages/container/<repo>/settings` →
# "Change visibility" → Public) anonymous pulls from `ghcr.io/<owner>/<repo>`
# return 403/404 even after a successful push here. This is one-time per
# package and not something this script can fix automatically — the GHCR
# REST API does not currently expose visibility-PATCH for container packages.
if [ -n "${GITHUB_REPOSITORY:-}" ]; then
  (
    set -e
    GHCR_IMAGE="ghcr.io/$(echo "${GITHUB_REPOSITORY}" | tr '[:upper:]' '[:lower:]')"

    GHCR_AMD64_DIGEST=$(docker manifest inspect "${GHCR_IMAGE}:v${VERSION}-amd64" | jq -er '.manifests[] | select(.platform.architecture == "amd64") | .digest')
    GHCR_ARM64_DIGEST=$(docker manifest inspect "${GHCR_IMAGE}:v${VERSION}-arm64" | jq -er '.manifests[] | select(.platform.architecture == "arm64") | .digest')

    echo "GHCR AMD64 digest: ${GHCR_AMD64_DIGEST}"
    echo "GHCR ARM64 digest: ${GHCR_ARM64_DIGEST}"

    docker manifest create \
        "${GHCR_IMAGE}:v${VERSION}" \
        "${GHCR_IMAGE}@${GHCR_AMD64_DIGEST}" \
        "${GHCR_IMAGE}@${GHCR_ARM64_DIGEST}"

    docker manifest push "${GHCR_IMAGE}:v${VERSION}"

    if [[ "$VERSION" != *-* ]]; then
        docker manifest create \
            "${GHCR_IMAGE}:latest" \
            "${GHCR_IMAGE}@${GHCR_AMD64_DIGEST}" \
            "${GHCR_IMAGE}@${GHCR_ARM64_DIGEST}"

        docker manifest push "${GHCR_IMAGE}:latest"
    fi
  ) || echo "::warning::GHCR mirroring failed; Docker Hub publish unaffected"
fi
