#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
VENDOR_HASH_FILE="$SCRIPT_DIR/vendorHash"
NPM_DEPS_HASH_FILE="$SCRIPT_DIR/npmDepsHash"

SOURCE_VERSION="$(tr -d '[:space:]' < "$REPO_ROOT/transports/version")"
TAG="${RELEASE_TAG:-}"
if [ -n "$TAG" ]; then
  VERSION="${TAG#transports/v}"
  if [ "$VERSION" != "$SOURCE_VERSION" ]; then
    echo "ERROR: RELEASE_TAG ($VERSION) does not match transports/version ($SOURCE_VERSION)" >&2
    exit 1
  fi
else
  VERSION="$SOURCE_VERSION"
fi

update_hash() {
  local hash_file="$1"
  local build_target="$2"
  local label="$3"

  echo "==> Updating $label..."

  # Write a fake hash to trigger a mismatch error from nix
  echo -n "sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=" > "$hash_file"

  local build_log
  build_log="$(mktemp)"

  set +e
  nix build -L "$REPO_ROOT#$build_target" 2>&1 | tee "$build_log"
  local build_status=${PIPESTATUS[0]}
  set -e

  if [ "$build_status" -eq 0 ]; then
    echo "ERROR: build succeeded with fake hash — something is wrong" >&2
    exit 1
  fi

  local got
  got=$(sed -n 's/.*got:[[:space:]]*\(sha256-[^[:space:]]*\).*/\1/p' "$build_log" | head -n1)
  if [ -z "${got:-}" ]; then
    echo "ERROR: could not extract $label from build output" >&2
    cat "$build_log" >&2
    exit 1
  fi

  echo -n "$got" > "$hash_file"
  echo "==> $label updated to: $got"

  # Return the hash via a variable name
  eval "${4}=\$got"
}

new_npm_hash=""
new_vendor_hash=""

# Update npmDepsHash first (bifrost-ui has no dependency on bifrost-http)
update_hash "$NPM_DEPS_HASH_FILE" "bifrost-ui" "npmDepsHash" new_npm_hash

# Then update vendorHash (go-modules FOD doesn't need bifrost-ui)
update_hash "$VENDOR_HASH_FILE" "bifrost-http" "vendorHash" new_vendor_hash

echo ""
echo "==> Done!"
echo "    version:     $VERSION"
echo "    vendorHash:  $new_vendor_hash"
echo "    npmDepsHash: $new_npm_hash"

# Set GitHub Actions outputs if running in CI
if [ -n "${GITHUB_OUTPUT:-}" ]; then
  {
    echo "version=$VERSION"
    echo "vendorHash=$new_vendor_hash"
    echo "npmDepsHash=$new_npm_hash"
  } >> "$GITHUB_OUTPUT"
fi
