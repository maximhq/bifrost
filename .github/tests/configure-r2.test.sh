#!/usr/bin/env bash
# Prove publisher setup never installs packages or prints credentials.
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
fixture_dir="$(mktemp -d)"
trap 'rm -rf "$fixture_dir"' EXIT
for tool in pip pip3 npm curl; do
  cat > "$fixture_dir/$tool" <<'SH'
#!/bin/sh
echo forbidden >> "$CALLS"
exit 99
SH
  chmod +x "$fixture_dir/$tool"
done
cat > "$fixture_dir/aws" <<'SH'
#!/bin/sh
printf '%s\n' "$*" >> "$CALLS"
if [ "$1" = --version ]; then echo "${AWS_VERSION:-aws-cli/2.31.0}"; fi
SH
chmod +x "$fixture_dir/aws"
export CALLS="$fixture_dir/calls"
export PATH="$fixture_dir:/usr/bin:/bin"
export R2_ENDPOINT=https://example.r2.cloudflarestorage.com
export R2_ACCESS_KEY_ID=fixture-id R2_SECRET_ACCESS_KEY=fixture-secret R2_BUCKET=fixture-bucket
bash "$SCRIPT_DIR/../workflows/scripts/configure-r2.sh" > "$fixture_dir/output" 2>&1
if grep -q forbidden "$CALLS"; then exit 1; fi
if grep -q fixture-secret "$fixture_dir/output"; then exit 1; fi
grep -q 'head-bucket --bucket fixture-bucket' "$CALLS"
# A secret stored with surrounding whitespace must still configure, trimmed.
R2_ENDPOINT=$' https://example.r2.cloudflarestorage.com\n' R2_ACCESS_KEY_ID=$'fixture-id\n' \
  bash "$SCRIPT_DIR/../workflows/scripts/configure-r2.sh" > "$fixture_dir/output" 2>&1
for invalid in v1 endpoint bucket; do
  case "$invalid" in
    v1) export AWS_VERSION=aws-cli/1.0.0 ;;
    endpoint) unset AWS_VERSION; export R2_ENDPOINT=http://attacker.invalid ;;
    bucket) export R2_ENDPOINT=https://example.r2.cloudflarestorage.com R2_BUCKET='bad bucket' ;;
  esac
  if bash "$SCRIPT_DIR/../workflows/scripts/configure-r2.sh" > "$fixture_dir/output" 2>&1; then
    echo "Expected invalid $invalid configuration to fail" >&2
    exit 1
  fi
done
echo 'R2 publisher bootstrap tests passed'
