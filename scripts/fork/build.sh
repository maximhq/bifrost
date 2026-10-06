#!/usr/bin/env bash
# Builds the fork's bifrost-http binary (UI included) into ./tmp/bifrost-http.
#
# Usage: scripts/fork/build.sh [--skip-ui] [--output PATH]
set -euo pipefail

skip_ui=false
output="tmp/bifrost-http"
while [ $# -gt 0 ]; do
  case "$1" in
    --skip-ui) skip_ui=true ;;
    --output) output="$2"; shift ;;
    -h|--help) sed -n '2,5p' "$0"; exit 0 ;;
    *) echo "unknown option: $1" >&2; exit 2 ;;
  esac
  shift
done

root="$(git rev-parse --show-toplevel)"
cd "$root"

scripts/fork/workspace.sh

if [ "$skip_ui" = false ]; then
  # `npm run build` builds the UI and copies it into transports/bifrost-http/ui.
  (cd ui && npm ci --no-audit --no-fund && npm run build)
fi

case "$output" in
  /*) out_path="$output" ;;
  *) out_path="$root/$output" ;;
esac
mkdir -p "$(dirname "$out_path")"
(cd transports/bifrost-http && CGO_ENABLED=1 go build -tags sqlite_static -o "$out_path" .)
echo "built $out_path"
