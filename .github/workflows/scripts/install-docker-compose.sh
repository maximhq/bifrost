#!/usr/bin/env bash
set -euo pipefail

# Installs a pinned, checksum-verified Docker Compose binary.
#
# The workflows only reach this when the runner image has no `docker compose`
# plugin. It used to download releases/latest with no verification, so whatever
# that URL served ran as root on a runner holding provider secrets. Bump
# COMPOSE_VERSION and both digests together, from the release's checksums.txt.
#
# Usage: ./install-docker-compose.sh <destination-path>
#   e.g. /usr/local/bin/docker-compose, or $DOCKER_CONFIG/cli-plugins/docker-compose

COMPOSE_VERSION="v5.6.1"

if [[ $# -ne 1 ]]; then
  echo "Usage: $0 <destination-path>" >&2
  exit 1
fi
DEST="$1"

case "$(uname -s)-$(uname -m)" in
  Linux-x86_64) ASSET="docker-compose-linux-x86_64"; SHA256="47737f4236e6d3ccea7291995203f8f018058783b4c3a9469119811a0b5db8c0" ;;
  Linux-aarch64) ASSET="docker-compose-linux-aarch64"; SHA256="42cfa75f51ad0ccf290bad37899e0038a8c10a0069e85233ecc1d53e3ff1347d" ;;
  *) echo "::error::no pinned Docker Compose digest for $(uname -s)-$(uname -m)" >&2; exit 1 ;;
esac

TMP="$(mktemp)"
trap 'rm -f "$TMP"' EXIT

curl -fsSL "https://github.com/docker/compose/releases/download/${COMPOSE_VERSION}/${ASSET}" -o "$TMP"
echo "${SHA256}  ${TMP}" | sha256sum -c -

mkdir -p "$(dirname "$DEST")" 2>/dev/null || sudo mkdir -p "$(dirname "$DEST")"
if [[ -w "$(dirname "$DEST")" ]]; then
  install -m 0755 "$TMP" "$DEST"
else
  sudo install -m 0755 "$TMP" "$DEST"
fi
echo "Installed Docker Compose ${COMPOSE_VERSION} at ${DEST}"
