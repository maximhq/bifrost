#!/bin/sh
# Runs oxfmt on the files changed against HEAD (plus untracked ones) under the current directory.
# Pass --check to verify instead of rewriting. Formatting the whole tree rewrites unrelated files.
oxfmt="$(cd "$(dirname "$0")/.." && pwd)/node_modules/.bin/oxfmt"
list=$(mktemp)
trap 'rm -f "$list"' EXIT
# NUL-separated so names with spaces survive.
{ git diff -z --name-only --relative --diff-filter=d HEAD; git ls-files -z --others --exclude-standard; } | grep -zE '\.(ts|tsx|js|jsx|mjs|cjs|css)$' >"$list"
[ -s "$list" ] || exit 0
xargs -0 "$oxfmt" "$@" <"$list"
