#!/usr/bin/env bash
set -euo pipefail

release_tag=${1:-v1.6.12}
repository=${BIFROST_IMAGE_REPOSITORY:-docker.io/maximhq/bifrost}
github_repository=${BIFROST_GITHUB_REPOSITORY:-maximhq/bifrost}
release_image="${repository}:${release_tag}"
latest_image="${repository}:latest"
github_release_tag="transports/${release_tag}"

if ! github_release=$(gh api "repos/${github_repository}/releases/tags/${github_release_tag}"); then
  echo "ERROR: required public GitHub release does not exist: ${github_repository}@${github_release_tag}" >&2
  exit 1
fi
if ! jq -e \
  --arg expected "$github_release_tag" \
  '.tag_name == $expected and .draft == false and .prerelease == false' \
  <<<"$github_release" >/dev/null; then
  echo "ERROR: ${github_repository}@${github_release_tag} is not a final public release" >&2
  exit 1
fi

if ! release_manifest=$(docker buildx imagetools inspect "$release_image" --format '{{json .Manifest}}'); then
  echo "ERROR: required deployment release does not exist: $release_image" >&2
  exit 1
fi
if ! latest_manifest=$(docker buildx imagetools inspect "$latest_image" --format '{{json .Manifest}}'); then
  echo "ERROR: could not inspect deployment image: $latest_image" >&2
  exit 1
fi

release_digest=$(jq -er '.digest' <<<"$release_manifest")
latest_digest=$(jq -er '.digest' <<<"$latest_manifest")
if [[ "$release_digest" != "$latest_digest" ]]; then
  echo "ERROR: $latest_image resolves to $latest_digest, not $release_image at $release_digest" >&2
  exit 1
fi

platforms=$(jq -r '
  .manifests[]
  | select(.platform.os == "linux")
  | [.platform.os, .platform.architecture]
  | join("/")
' <<<"$release_manifest" | sort -u)

for required_platform in linux/amd64 linux/arm64; do
  if ! grep -qx "$required_platform" <<<"$platforms"; then
    echo "ERROR: $release_image does not include $required_platform" >&2
    exit 1
  fi
done

echo "$latest_image and $release_image resolve to $release_digest with linux/amd64 and linux/arm64"
