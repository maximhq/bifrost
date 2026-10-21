#!/usr/bin/env bash
# Synthetic identities prove lookalikes, transferred names, and changed bytes fail closed.
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
fixture_dir="$(mktemp -d)"
trap 'rm -rf "$fixture_dir"' EXIT
cat > "$fixture_dir/git" <<'SH'
#!/bin/sh
case "$1" in
  rev-parse) echo "${FIXTURE_HEAD:-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa}" ;;
  remote) echo "${FIXTURE_ORIGIN:-https://github.com/maximhq/bifrost}" ;;
  status) if [ "${FIXTURE_STATUS_FAILURE:-}" = true ]; then exit 1; fi; printf '%s' "${FIXTURE_DIRTY:-}" ;;
  *) exit 99 ;;
esac
SH
chmod +x "$fixture_dir/git"
export PATH="$fixture_dir:$PATH"
export GITHUB_REPOSITORY=maximhq/bifrost GITHUB_REPOSITORY_ID=951115072 GITHUB_REPOSITORY_OWNER_ID=139708451
export GITHUB_EVENT_NAME=push GITHUB_REF=refs/heads/main GITHUB_SHA=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
script="$SCRIPT_DIR/../workflows/scripts/verify-release-source.sh"
bash "$script"
for changed in GITHUB_REPOSITORY=maxim-hq/bifrost GITHUB_REPOSITORY_ID=999 GITHUB_REPOSITORY_OWNER_ID=999 \
  GITHUB_EVENT_NAME=pull_request GITHUB_REF=refs/heads/attacker GITHUB_SHA=bad \
  FIXTURE_HEAD=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb FIXTURE_ORIGIN=https://github.com/maxim-hq/bifrost FIXTURE_DIRTY=modified FIXTURE_STATUS_FAILURE=true; do
  if env "$changed" bash "$script" > "$fixture_dir/output" 2>&1; then
    echo "Accepted invalid source fixture: $changed" >&2; exit 1
  fi
done
echo 'Release source rejection tests passed'
