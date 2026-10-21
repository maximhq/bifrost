#!/usr/bin/env bash
# Download every binary named by a .sha256 sidecar under <dist> from the versioned R2
# prefix, so publish-release-checksums.sh can prove each sidecar against real bytes
# rather than trusting what another job uploaded.
#
# Usage: fetch-released-binaries.sh <r2-prefix> <dist-dir>
set -euo pipefail

PREFIX="${1:?R2 prefix is required}"
DIST="${2:?dist directory is required}"
: "${R2_ENDPOINT:?R2_ENDPOINT env var is required}"
: "${R2_BUCKET:?R2_BUCKET env var is required}"
R2_ENDPOINT="$(printf '%s' "$R2_ENDPOINT" | tr -d '[:space:]')"
[[ "$PREFIX" =~ ^bifrost/v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || { echo "Unexpected prefix: $PREFIX" >&2; exit 1; }

count=0
while IFS= read -r -d '' sidecar; do
  relative="${sidecar#"$DIST"/}"
  relative="${relative%.sha256}"
  aws s3 cp "s3://$R2_BUCKET/$PREFIX/$relative" "$DIST/$relative" \
    --endpoint-url "$R2_ENDPOINT" --profile "${R2_AWS_PROFILE:-R2}" --no-progress --only-show-errors
  count=$((count + 1))
done < <(find "$DIST" -type f -name '*.sha256' -print0 | sort -z)

[[ "$count" -gt 0 ]] || { echo "No checksum sidecars under $DIST" >&2; exit 1; }
echo "Fetched $count released binaries for verification"
