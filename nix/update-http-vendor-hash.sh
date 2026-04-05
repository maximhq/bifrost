#! /usr/bin/env bash

set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")"
:> http-vendor-hash.txt

build_log="$(mktemp)"
trap 'rm -f "$build_log"' EXIT

nix build -L ..#bifrost-http |& tee "$build_log"

hash_value="$(rg -o "got:\s+\S+" "$build_log" | sed -E 's/^got:\s+//' | tail -n 1)"

if [ -z "$hash_value" ]; then
  echo "failed to detect vendorHash from build log" >&2
  exit 1
fi

printf '%s\n' "$hash_value" > http-vendor-hash.txt
echo "updated nix/http-vendor-hash.txt: $hash_value"
