#!/usr/bin/env bash
set -euo pipefail

# Exercise deploy/fly/deploy.sh offline with a stubbed fly CLI that keeps
# state between invocations: first deployment, a rerun that reuses the apps
# and keeps the encryption key, and a failed deploy followed by a rerun that
# still hands the operator a usable setup token.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
mkdir -p "$WORK/bin" "$WORK/state" "$WORK/repo/deploy/fly" "$WORK/repo/deploy"
cp "$SCRIPT_DIR/deploy.sh" "$SCRIPT_DIR/fly.toml" "$WORK/repo/deploy/fly/"
cp "$SCRIPT_DIR/../config.postgres.json" "$WORK/repo/deploy/config.postgres.json"

cat > "$WORK/bin/fly" <<'STUB'
#!/usr/bin/env bash
# Minimal flyctl: records every call and keeps apps and secret names in $STATE.
set -euo pipefail
echo "fly $*" >> "$STATE/calls"
case "$1 ${2:-}" in
  "auth whoami") echo dev@example.com ;;
  "apps list")
    echo "["; [ -f "$STATE/apps" ] && while read -r a; do echo "  {\"Name\": \"$a\"},"; done < "$STATE/apps"; echo "  {\"Name\": \"-\"}]" ;;
  "apps create") echo "$3" >> "$STATE/apps" ;;
  "postgres create")
    shift 2; while [ $# -gt 0 ]; do case "$1" in --name) echo "$2" >> "$STATE/apps"; shift 2 ;; *) shift ;; esac; done ;;
  "postgres attach")
    app=""; shift 2; while [ $# -gt 0 ]; do case "$1" in --app) app="$2"; shift 2 ;; *) shift ;; esac; done
    echo "DATABASE_URL" >> "$STATE/secrets.$app" ;;
  "secrets list")
    app=""; shift 2; while [ $# -gt 0 ]; do case "$1" in --app) app="$2"; shift 2 ;; *) shift ;; esac; done
    echo "NAME DIGEST CREATED"; [ -f "$STATE/secrets.$app" ] && awk '{print $1" abcd 1m ago"}' "$STATE/secrets.$app" || true ;;
  "secrets set")
    app=""; shift 2; while [ $# -gt 0 ]; do case "$1" in --app) app="$2"; shift 2 ;; --stage) shift ;; *) echo "${1%%=*}" >> "$STATE/secrets.$app"; echo "$1" >> "$STATE/values.$app"; shift ;; esac; done ;;
  "deploy "*|"deploy")
    if [ "${TEST_DEPLOY_FAIL:-0}" = 1 ]; then echo "Error: deploy failed" >&2; exit 1; fi ;;
  *) echo "unexpected: fly $*" >&2; exit 1 ;;
esac
STUB
chmod +x "$WORK/bin/fly"

FAILED=0
fail() { echo "FAIL: $*" >&2; FAILED=1; }

run() {
  : > "$WORK/state/calls"
  PATH="$WORK/bin:$PATH" STATE="$WORK/state" FLY_APP_NAME=acme-bifrost FLY_ORG=acme \
    bash "$WORK/repo/deploy/fly/deploy.sh" > "$WORK/log" 2>&1
}
calls() { awk -v p="$1" 'index($0,p)==1' "$WORK/state/calls" | wc -l | tr -d ' '; }
staged_value() { awk -F= -v k="$1" '$1==k {sub(/^[^=]*=/,""); v=$0} END {print v}' "$WORK/state/values.acme-bifrost"; }
printed_token() { awk '/^Setup token:/ {print $3}' "$WORK/log"; }

# 1. First run: apps and Postgres created, attached, three secrets staged, deploy runs, token printed.
if run; then
  [[ "$(calls 'fly apps create acme-bifrost')" == 1 ]] || fail "first run: app not created"
  [[ "$(calls 'fly postgres create')" == 1 ]] || fail "first run: postgres not created"
  [[ "$(calls 'fly postgres attach acme-bifrost-db')" == 1 ]] || fail "first run: postgres not attached"
  [[ "$(calls 'fly deploy')" == 1 ]] || fail "first run: deploy not called"
  [[ -n "$(staged_value BIFROST_ENCRYPTION_KEY)" ]] || fail "first run: encryption key not staged"
  [[ "$(printed_token)" == "$(staged_value BIFROST_SETUP_TOKEN)" ]] || fail "first run: printed token '$(printed_token)' differs from staged '$(staged_value BIFROST_SETUP_TOKEN)'"
else
  cat "$WORK/log" >&2; fail "first run: script exited non-zero"
fi
FIRST_KEY="$(staged_value BIFROST_ENCRYPTION_KEY)"
FIRST_TOKEN="$(staged_value BIFROST_SETUP_TOKEN)"

# 2. Rerun: apps reused, encryption key kept, a fresh setup token staged and printed.
if run; then
  [[ "$(calls 'fly apps create')" == 0 ]] || fail "rerun: app created again"
  [[ "$(calls 'fly postgres attach')" == 0 ]] || fail "rerun: postgres attached again"
  [[ "$(staged_value BIFROST_ENCRYPTION_KEY)" == "$FIRST_KEY" ]] || fail "rerun: encryption key changed"
  [[ -n "$(printed_token)" && "$(printed_token)" != "$FIRST_TOKEN" ]] || fail "rerun: expected a fresh setup token to be printed, got '$(printed_token)'"
  [[ "$(printed_token)" == "$(staged_value BIFROST_SETUP_TOKEN)" ]] || fail "rerun: printed token is not the staged one"
else
  cat "$WORK/log" >&2; fail "rerun: script exited non-zero"
fi

# 3. A failed deploy exits non-zero; the following rerun still prints a usable token.
if TEST_DEPLOY_FAIL=1 run; then
  fail "failed deploy: script should exit non-zero"
fi
if run; then
  [[ -n "$(printed_token)" && "$(printed_token)" == "$(staged_value BIFROST_SETUP_TOKEN)" ]] || fail "recovery rerun: no usable setup token printed"
  [[ "$(staged_value BIFROST_ENCRYPTION_KEY)" == "$FIRST_KEY" ]] || fail "recovery rerun: encryption key changed"
else
  cat "$WORK/log" >&2; fail "recovery rerun: script exited non-zero"
fi

if [[ $FAILED -ne 0 ]]; then
  exit 1
fi
echo "PASS: deploy/fly/deploy.sh"
