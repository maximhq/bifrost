#!/usr/bin/env bash
# Prove the release checksum asset is built from bytes on disk, never clobbered, and
# refuses stale sidecars, symlinks, and malformed tags.
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
script="$SCRIPT_DIR/../workflows/scripts/publish-release-checksums.sh"
fixture_dir="$(mktemp -d)"
trap 'rm -rf "$fixture_dir"' EXIT

# Fake gh records uploads and serves an existing asset when FIXTURE_PUBLISHED is set.
cat > "$fixture_dir/gh" <<'SH'
#!/bin/sh
printf '%s\n' "$*" >> "$CALLS"
case "$2" in
  view) if [ -n "${FIXTURE_PUBLISHED:-}" ]; then echo checksums.txt; fi ;;
  download) out="$7"; printf '%s\n' "$FIXTURE_PUBLISHED" > "$out" ;;
  upload) ;;
esac
SH
chmod +x "$fixture_dir/gh"
export CALLS="$fixture_dir/calls" PATH="$fixture_dir:$PATH"

dist="$fixture_dir/dist"
mkdir -p "$dist/linux/amd64" "$dist/darwin/arm64"
printf 'linux bytes' > "$dist/linux/amd64/bifrost-http"
printf 'darwin bytes' > "$dist/darwin/arm64/bifrost-http"
for b in linux/amd64 darwin/arm64; do
  (cd "$dist/$b" && shasum -a 256 bifrost-http > bifrost-http.sha256)
done
expected_linux="$(shasum -a 256 "$dist/linux/amd64/bifrost-http" | cut -d' ' -f1)"

bash "$script" transports/v1.2.3 "$dist" > "$fixture_dir/out"
grep -q '^release upload transports/v1.2.3 ' "$CALLS"
printf '%s  darwin/arm64/bifrost-http\n%s  linux/amd64/bifrost-http' \
  "$(shasum -a 256 "$dist/darwin/arm64/bifrost-http" | cut -d' ' -f1)" "$expected_linux" > "$fixture_dir/want"

# Identical content already attached: no upload, success.
: > "$CALLS"
FIXTURE_PUBLISHED="$(cat "$fixture_dir/want")" bash "$script" transports/v1.2.3 "$dist" > "$fixture_dir/out"
if grep -q 'release upload' "$CALLS"; then echo "re-uploaded an identical asset" >&2; exit 1; fi

# Different content already attached: refuse.
if FIXTURE_PUBLISHED="deadbeef  linux/amd64/bifrost-http" bash "$script" transports/v1.2.3 "$dist" > "$fixture_dir/out" 2>&1; then
  echo "replaced a published checksum asset" >&2; exit 1
fi

# Stale sidecar: refuse.
printf 'changed' > "$dist/linux/amd64/bifrost-http"
if bash "$script" transports/v1.2.3 "$dist" > "$fixture_dir/out" 2>&1; then
  echo "published a stale checksum" >&2; exit 1
fi
(cd "$dist/linux/amd64" && shasum -a 256 bifrost-http > bifrost-http.sha256)

# Symlinked binary: refuse.
mv "$dist/darwin/arm64/bifrost-http" "$fixture_dir/elsewhere"
ln -s "$fixture_dir/elsewhere" "$dist/darwin/arm64/bifrost-http"
if bash "$script" transports/v1.2.3 "$dist" > "$fixture_dir/out" 2>&1; then
  echo "published a symlinked binary" >&2; exit 1
fi
rm "$dist/darwin/arm64/bifrost-http"; mv "$fixture_dir/elsewhere" "$dist/darwin/arm64/bifrost-http"

# Malformed tag and empty dist: refuse.
if bash "$script" '../evil' "$dist" > "$fixture_dir/out" 2>&1; then echo "accepted a bad tag" >&2; exit 1; fi
mkdir "$fixture_dir/empty"
if bash "$script" transports/v1.2.3 "$fixture_dir/empty" > "$fixture_dir/out" 2>&1; then echo "accepted an empty dist" >&2; exit 1; fi

echo 'Release checksum asset tests passed'
