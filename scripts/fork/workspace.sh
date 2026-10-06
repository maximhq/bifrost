#!/usr/bin/env bash
# Creates go.work over the local modules so core, framework, transports and plugins build
# against this fork's sources instead of the published upstream modules.
#
# Unlike `make setup-workspace`, this never runs `go work sync`: that rewrites the go.mod/go.sum
# of every module, and those edits would conflict with every upstream sync.
set -euo pipefail

root="$(git rev-parse --show-toplevel)"
cd "$root"

rm -f go.work go.work.sum
go work init ./core ./framework ./transports
for dir in ./plugins/*/; do
  if [ -f "${dir}go.mod" ]; then
    go work use "${dir%/}"
  fi
done

# transports/bifrost-http embeds ./ui; give it a placeholder until the real UI is built.
ui_dir="transports/bifrost-http/ui"
mkdir -p "$ui_dir"
if [ -z "$(ls -A "$ui_dir")" ]; then
  touch "$ui_dir/.tmp"
fi

echo "go.work ready ($(grep -c '^\s*\./' go.work) modules)"
