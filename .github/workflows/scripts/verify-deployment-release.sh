#!/usr/bin/env bash
set -euo pipefail

script_dir=$(CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
repo_root=$(CDPATH='' cd -- "${script_dir}/../../.." && pwd -P)

repository=${BIFROST_IMAGE_REPOSITORY:-docker.io/maximhq/bifrost}
github_repository=${BIFROST_GITHUB_REPOSITORY:-maximhq/bifrost}
# The release that first carried the deployment runtime contract the one-click
# templates depend on. It is a floor, not a pin: the gate resolves the newest
# published transports release so it keeps passing as releases advance, but it
# must never accept a release older than the contract.
#
# Read from deploy/runtime-contract.json rather than written here, because
# validate-deployment-templates.py enforces the same floor on the verifications
# recorded for the public buttons. Two copies would let a bump raise the bar for
# the image while the recorded verifications still pointed below it.
minimum_release_tag=${BIFROST_MINIMUM_RELEASE_TAG:-$(jq -er '.minimum_release_tag' "${repo_root}/deploy/runtime-contract.json")}

# resolve_latest_release_tag prints the newest final transports/v* release tag,
# without its module prefix. The repository publishes one release per module, so
# core/framework/plugins tags are filtered out here.
#
# The listing is reverse-chronological across every module, and plugin releases
# dominate it, so one page of 100 need not contain a transports release at all.
# Pages are walked until one does, then that whole page is ranked: a full
# --paginate walk would cost one request per 100 of the repository's thousands
# of releases on every run, for a window that already spans several transports
# releases. release_page_limit bounds the walk so a repository that stops
# publishing transports releases fails the gate instead of scanning forever.
release_page_limit=${BIFROST_RELEASE_PAGE_LIMIT:-10}
resolve_latest_release_tag() {
  local page releases tags tag
  for ((page = 1; page <= release_page_limit; page++)); do
    if ! releases=$(gh api "repos/${github_repository}/releases?per_page=100&page=${page}" \
      --jq '.[] | select(.draft == false and .prerelease == false) | .tag_name'); then
      echo "ERROR: could not list releases for ${github_repository}" >&2
      return 1
    fi
    if [[ -z "$releases" ]]; then
      break
    fi
    tags=$(printf '%s\n' "$releases" \
      | sed -n 's|^transports/\(v[0-9][0-9]*\.[0-9][0-9]*\.[0-9][0-9]*\)$|\1|p')
    if [[ -n "$tags" ]]; then
      tag=$(printf '%s\n' "$tags" | sort -V | tail -n 1)
      printf '%s' "$tag"
      return 0
    fi
  done
  echo "ERROR: ${github_repository} publishes no final transports/v* release in the newest ${release_page_limit} pages of its release listing" >&2
  return 1
}

if [[ $# -gt 0 && -n "$1" ]]; then
  release_tag=$1
  release_source="requested release"
else
  release_tag=$(resolve_latest_release_tag)
  release_source="newest public release"
fi

# The floor applies to both selection paths. An explicitly named tag is a way to
# say which release to verify, not a way to bless one that predates the runtime
# contract the one-click templates depend on.
if [[ "$(printf '%s\n%s\n' "${minimum_release_tag#v}" "${release_tag#v}" | sort -V | head -n 1)" != "${minimum_release_tag#v}" ]]; then
  echo "ERROR: ${release_source} ${release_tag} predates ${minimum_release_tag}, the release that carries the deployment runtime contract" >&2
  exit 1
fi

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

# A single-platform release publishes no manifest list, so default the missing
# key to an empty list and let the required-platform check below report it.
platforms=$(jq -r '
  (.manifests // [])[]
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

# Digest equality proves latest and the release tag are the same artifact; it
# does not prove that artifact is the one the deployment tests exercised. The
# release image is built from transports/Dockerfile with GOWORK=off against the
# published modules, while the smoke tests build Dockerfile.local from the
# workspace — different builds of different module graphs. Test the digest that
# actually ships before letting a public button consume it.
#
# The manifest-list digest above is not that artifact: `docker run` resolves it
# to whichever platform the host is, so this gate on an amd64 runner has never
# executed an arm64 byte. The two platforms are separately built on their own
# native runners and merged into the list, so an arm64 runtime stage can break
# on its own. Test the per-platform digest, and let each required platform be
# covered by its own run of this gate.
host_arch=$(docker version --format '{{.Server.Arch}}')
smoke_platform="linux/${host_arch}"

# A caller that states the platform it expects to cover gets an error rather
# than silent coverage loss: a matrix entry retargeted to a different runner
# would otherwise re-test the platform its sibling already did, and the gate
# would still report success for both.
expected_platform=${BIFROST_SMOKE_PLATFORM:-}
if [[ -n "$expected_platform" && "$expected_platform" != "$smoke_platform" ]]; then
  echo "ERROR: BIFROST_SMOKE_PLATFORM asks for $expected_platform but this host runs $smoke_platform." >&2
  echo "  A smoke test only covers the platform it executes natively; run this gate on a $expected_platform runner." >&2
  exit 1
fi

if ! platform_digest=$(jq -er --arg platform "$smoke_platform" '
  (.manifests // [])[]
  | select((.platform.os + "/" + .platform.architecture) == $platform)
  | .digest
' <<<"$release_manifest" | head -n 1) || [[ -z "$platform_digest" ]]; then
  echo "ERROR: $release_image publishes no $smoke_platform manifest for this runner to smoke-test" >&2
  exit 1
fi

bash "${script_dir}/smoke-test-published-image.sh" "${repository}@${platform_digest}" "$smoke_platform"
echo "Release gate passed on $smoke_platform. Each required platform is covered only by its own run of this gate."
