#!/usr/bin/env bash
set -euo pipefail

# Installs the tools the arm-64-2cpu-8gb runner image no longer ships.
#
# Since late 2026-10 that label boots a bare Ubuntu 24.04 (noble) base image
# instead of GitHub's ubuntu-24.04-arm runner image, so Docker (with buildx and
# compose), jq, gh, pip/awscli, a C toolchain, and libatomic (which the official
# linux-arm64 Node binary links against) are all missing. Each component is skipped when it
# is already present, so on a full runner image this is a no-op.
#
# Runs after harden-runner like every other step. apt-sources-https.sh moves the
# Ubuntu sources to HTTPS before the first apt-get update, so callers allowlist
# ports.ubuntu.com:443, plus download.docker.com:443 when they ask for `docker`;
# check-egress-allowlist.sh enforces both. Docker's apt repo is trusted only
# through a key whose primary fingerprint matches DOCKER_KEY_FINGERPRINT, and
# apt then verifies every package against that key.
#
# Usage: ./install-arm64-runner-deps.sh <component>...
#   node    libatomic1, for the official linux-arm64 Node binary
#   docker  Docker CE, containerd, buildx and compose plugins
#   build   make, gcc, g++ and libc headers for native CGO builds
#   jq, gh  the tools of the same name
#   aws     AWS CLI v1 (pinned) in a venv put on GITHUB_PATH; noble's PEP 668
#           blocks a system pip install. Needs pypi.org + files.pythonhosted.org.

DOCKER_KEY_FINGERPRINT="9DC858229FC7DD38854AE2D88D81803C0EBFCD88"
AWSCLI_VERSION="1.46.2"
OS_RELEASE="${OS_RELEASE:-/etc/os-release}"
# ${BASH_SOURCE%/*}, not dirname: the installer test runs with a minimal PATH.
APT_SOURCES_HTTPS="${BASH_SOURCE%/*}/apt-sources-https.sh"

if [[ $# -eq 0 ]]; then
  echo "Usage: $0 <node|docker|build|jq|gh|aws>..." >&2
  exit 1
fi

has() { command -v "$1" >/dev/null 2>&1; }

# Both install paths call apt-get update; the sources switch to HTTPS once, before the first.
sources_https=0
apt_update() {
  if [[ $sources_https -eq 0 ]]; then
    "$BASH" "$APT_SOURCES_HTTPS"
    sources_https=1
  fi
  sudo apt-get update
}

packages=()
want_docker=0
want_aws=0
for component in "$@"; do
  case "$component" in
    # grep without -q: an early exit would SIGPIPE ldconfig and fail the pipeline.
    node) ldconfig -p | grep 'libatomic\.so\.1' >/dev/null || packages+=(libatomic1) ;;
    build) { has make && has gcc && has g++; } || packages+=(make gcc g++ libc6-dev) ;;
    jq | gh) has "$component" || packages+=("$component") ;;
    aws) has aws || { want_aws=1; packages+=(python3-venv); } ;;
    docker)
      if ! { has docker && docker buildx version >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; }; then
        want_docker=1
        packages+=(ca-certificates)
        has curl || packages+=(curl)
        has gpg || packages+=(gnupg)
      fi
      ;;
    *) echo "::error::unknown component: $component" >&2; exit 1 ;;
  esac
done

if [[ ${#packages[@]} -gt 0 ]]; then
  echo "Installing from Ubuntu: ${packages[*]}"
  apt_update
  sudo apt-get install -y --no-install-recommends "${packages[@]}"
fi

if [[ $want_aws -eq 1 ]]; then
  venv="${RUNNER_TEMP:?}/awscli-venv"
  python3 -m venv "$venv"
  "$venv/bin/pip" install --no-cache-dir "awscli==${AWSCLI_VERSION}"
  # pip rides along on PATH too, so configure-r2.sh's own `pip install awscli` is a no-op.
  echo "$venv/bin" >> "${GITHUB_PATH:?}"
fi

if [[ $want_docker -eq 1 ]]; then
  KEY="$(mktemp)"
  trap 'rm -f "$KEY"' EXIT
  curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o "$KEY"

  # Primary keys only: subkeys carry their own fpr lines, and a served file with
  # an extra primary key must fail rather than ride along with the pinned one.
  primaries="$(gpg --with-colons --show-keys "$KEY" | awk -F: '$1 == "pub" { p = 1; next } $1 == "fpr" && p { print $10; p = 0 }')"
  if [[ "$primaries" != "$DOCKER_KEY_FINGERPRINT" ]]; then
    echo "::error::Docker apt key primary fingerprint(s) [${primaries//$'\n'/ }] do not match ${DOCKER_KEY_FINGERPRINT}" >&2
    exit 1
  fi

  # shellcheck source=/dev/null
  codename="$(. "$OS_RELEASE" && echo "$VERSION_CODENAME")"
  sudo install -D -m 0644 "$KEY" /etc/apt/keyrings/docker.asc
  echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu ${codename} stable" \
    | sudo tee /etc/apt/sources.list.d/docker.list >/dev/null
  apt_update
  sudo apt-get install -y --no-install-recommends \
    docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin

  # The runner process started before the docker group existed, so later steps
  # never pick up that membership; open the socket on this throwaway VM instead.
  sudo chmod 666 /var/run/docker.sock
  docker version
fi
