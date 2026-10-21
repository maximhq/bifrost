#!/usr/bin/env bash
# Attach checksums.txt to a GitHub Release so launchers have a second checksum
# source that the object-store key cannot forge.
#
# Usage: publish-release-checksums.sh <tag> <dist-dir>
#
# dist-dir holds <os>/<arch>/<binary> with a sibling <binary>.sha256 written by
# the build. The asset lists one `<sha256>  <os>/<arch>/<binary>` line per
# binary, recomputed from the bytes on disk and cross-checked against the
# sibling file so a stale .sha256 cannot be published. The release must already
# exist; the asset is uploaded exactly once and never clobbered.
set -euo pipefail

TAG="${1:?release tag is required}"
DIST="${2:?dist directory is required}"
ASSET="checksums.txt"

[[ "$TAG" =~ ^[a-z-]+/v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || { echo "Unexpected release tag: $TAG" >&2; exit 1; }
[[ -d "$DIST" && ! -L "$DIST" ]] || { echo "dist directory missing: $DIST" >&2; exit 1; }

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

count=0
while IFS= read -r -d '' sidecar; do
  binary="${sidecar%.sha256}"
  relative="${binary#"$DIST"/}"
  [[ -f "$binary" && ! -L "$binary" ]] || { echo "Checksum without a regular binary: $sidecar" >&2; exit 1; }
  recorded="$(cut -d' ' -f1 < "$sidecar" | tr -d '[:space:]')"
  actual="$(shasum -a 256 "$binary" | cut -d' ' -f1)"
  [[ "$recorded" == "$actual" ]] || { echo "Checksum sidecar is stale for $relative" >&2; exit 1; }
  printf '%s  %s\n' "$actual" "$relative" >> "$work/$ASSET"
  count=$((count + 1))
done < <(find "$DIST" -type f -name '*.sha256' -print0 | sort -z)

[[ "$count" -gt 0 ]] || { echo "No checksums found under $DIST" >&2; exit 1; }
sort -k2 -o "$work/$ASSET" "$work/$ASSET"

if gh release view "$TAG" --json assets --jq '.assets[].name' | grep -qx "$ASSET"; then
  gh release download "$TAG" --pattern "$ASSET" --output "$work/published.txt"
  if diff -q <(sed -e '$a\' "$work/$ASSET") <(sed -e '$a\' "$work/published.txt") >/dev/null; then
    echo "checksums.txt already attached to $TAG with identical content"
    exit 0
  fi
  echo "checksums.txt on $TAG differs from this build; refusing to replace it" >&2
  exit 1
fi

gh release upload "$TAG" "$work/$ASSET"
echo "Attached $ASSET ($count binaries) to $TAG"
