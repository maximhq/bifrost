#! /usr/bin/env bash

set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")"
:> ui-npm-deps-hash.txt

build_log="$(mktemp)"
trap 'rm -f "$build_log"' EXIT

nix build -L ..#bifrost-ui |& tee "$build_log"

hash_value="$(rg -o "got:\s+\S+" "$build_log" | sed -E 's/^got:\s+//' | tail -n 1)"

if [ -z "$hash_value" ]; then
  echo "failed to detect npmDepsHash from build log" >&2
  exit 1
fi

printf '%s\n' "$hash_value" > ui-npm-deps-hash.txt
echo "updated nix/ui-npm-deps-hash.txt: $hash_value"
