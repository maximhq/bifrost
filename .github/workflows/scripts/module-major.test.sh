#!/usr/bin/env bash
set -uo pipefail

# Checks that every released Go module's path matches the major version in its
# version file.
#
# Go's semantic import versioning only resolves a vN tag (N >= 2) for a module
# whose path ends in /vN. A module tagged <dir>/v2.x.y whose go.mod still says
# `module .../<dir>` cannot be fetched with `go get` at all, so consumers fall
# back to pseudo-versions. Modules below v2 must not carry a /vN suffix.
#
# Usage: ./module-major.test.sh [repo-root]

REPO_ROOT="${1:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)}"

passed=0
failed=0

for dir in "$REPO_ROOT"/core "$REPO_ROOT"/framework "$REPO_ROOT"/transports "$REPO_ROOT"/cli "$REPO_ROOT"/plugins/*; do
  [ -f "$dir/go.mod" ] && [ -f "$dir/version" ] || continue
  rel="${dir#"$REPO_ROOT"/}"
  version="$(tr -d '\n\r' < "$dir/version")"
  major="${version%%.*}"
  module="$(awk '$1 == "module" { print $2; exit }' "$dir/go.mod")"

  if ! [[ "$major" =~ ^[0-9]+$ ]]; then
    echo "  FAIL - $rel: version '$version' has no numeric major"
    failed=$((failed + 1))
    continue
  fi

  if [ "$major" -ge 2 ]; then
    expected_suffix="/v${major}"
    if [[ "$module" == *"$expected_suffix" ]]; then
      echo "  ok - $rel: $module matches v$version"
      passed=$((passed + 1))
    else
      echo "  FAIL - $rel: version v$version needs module path ending in $expected_suffix, go.mod says $module"
      failed=$((failed + 1))
    fi
  else
    if [[ "$module" =~ /v[0-9]+$ ]]; then
      echo "  FAIL - $rel: version v$version must not carry a /vN suffix, go.mod says $module"
      failed=$((failed + 1))
    else
      echo "  ok - $rel: $module matches v$version"
      passed=$((passed + 1))
    fi
  fi
done

echo "module-major: $passed passed, $failed failed"
[ "$failed" -eq 0 ]
