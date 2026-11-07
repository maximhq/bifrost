#!/usr/bin/env bash
set -euo pipefail

# Exercise docker-entrypoint.sh's config bootstrap offline. The only edit to the
# script under test is the binary it execs, which is pointed at a stub that
# records its arguments. Everything else (APP_DIR probing, BIFROST_CONFIG_JSON /
# BIFROST_CONFIG_B64 handling) runs unchanged.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
mkdir -p "$WORK/bin"

sed "s#exec /app/main#exec \"$WORK/bin/main\"#" "$SCRIPT_DIR/docker-entrypoint.sh" > "$WORK/entrypoint.sh"
grep -q "$WORK/bin/main" "$WORK/entrypoint.sh" || { echo "FAIL: could not point the entrypoint at the stub binary" >&2; exit 1; }

cat > "$WORK/bin/main" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$@" > "$MAIN_ARGS"
STUB
chmod +x "$WORK/bin/main"

FAILED=0
fail() { echo "FAIL: $*" >&2; FAILED=1; }

file_mode() {
  if stat -c '%a' "$1" >/dev/null 2>&1; then stat -c '%a' "$1"; else stat -f '%Lp' "$1"; fi
}

# leftovers <dir> <glob>: names in <dir> matching <glob>, empty when none.
leftovers() {
  local f out=""
  for f in "$1"/$2; do
    [[ -e "$f" ]] && out="$out ${f##*/}"
  done
  printf '%s' "$out"
}

# run <name> [VAR=value ...]: fresh APP_DIR per case unless PRESEED is set.
run() {
  local name=$1; shift
  APP_DIR="$WORK/data-$name"
  mkdir -p "$APP_DIR"
  export APP_DIR MAIN_ARGS="$WORK/args-$name"
  rm -f "$MAIN_ARGS"
  local rc=0
  env -u BIFROST_CONFIG_JSON -u BIFROST_CONFIG_B64 "$@" sh "$WORK/entrypoint.sh" > "$WORK/log-$name" 2>&1 || rc=$?
  return $rc
}

JSON='{"client":{"drop_excess_requests":false},"encryption_key":"env.BIFROST_ENCRYPTION_KEY"}'
OTHER='{"marker":"from-b64"}'
B64="$(printf '%s' "$OTHER" | base64 | tr -d '\n')"

# 1. BIFROST_CONFIG_JSON is written verbatim, private to the container user, and main starts.
if run json BIFROST_CONFIG_JSON="$JSON"; then
  [[ "$(cat "$APP_DIR/config.json")" == "$JSON" ]] || fail "JSON case: config.json content differs"
  [[ "$(file_mode "$APP_DIR/config.json")" == "600" ]] || fail "JSON case: config.json mode is $(file_mode "$APP_DIR/config.json"), expected 600"
  [[ -f "$MAIN_ARGS" ]] || fail "JSON case: main was not started"
  grep -q -- "-app-dir" "$MAIN_ARGS" || fail "JSON case: main did not receive -app-dir"
  [[ -z "$(leftovers "$APP_DIR" 'config.json.*')" ]] || fail "JSON case: temp file left behind:$(leftovers "$APP_DIR" 'config.json.*')"
else
  cat "$WORK/log-json" >&2; fail "JSON case: entrypoint exited non-zero"
fi

# 2. An existing config.json is never overwritten.
APP_DIR="$WORK/data-keep"; mkdir -p "$APP_DIR"; printf '%s' '{"kept":true}' > "$APP_DIR/config.json"
if run keep BIFROST_CONFIG_JSON="$JSON"; then
  [[ "$(cat "$APP_DIR/config.json")" == '{"kept":true}' ]] || fail "keep case: existing config.json was overwritten"
else
  cat "$WORK/log-keep" >&2; fail "keep case: entrypoint exited non-zero"
fi

# 3. BIFROST_CONFIG_JSON wins when both are set.
if run both BIFROST_CONFIG_JSON="$JSON" BIFROST_CONFIG_B64="$B64"; then
  [[ "$(cat "$APP_DIR/config.json")" == "$JSON" ]] || fail "precedence case: B64 document won over JSON"
else
  cat "$WORK/log-both" >&2; fail "precedence case: entrypoint exited non-zero"
fi

# 4. BIFROST_CONFIG_B64 is decoded, also mode 600.
if run b64 BIFROST_CONFIG_B64="$B64"; then
  [[ "$(cat "$APP_DIR/config.json")" == "$OTHER" ]] || fail "B64 case: decoded content differs"
  [[ "$(file_mode "$APP_DIR/config.json")" == "600" ]] || fail "B64 case: config.json mode is $(file_mode "$APP_DIR/config.json"), expected 600"
else
  cat "$WORK/log-b64" >&2; fail "B64 case: entrypoint exited non-zero"
fi

# 5. Invalid base64 fails the start and leaves no config.json for the next boot to keep.
if run badb64 BIFROST_CONFIG_B64='%%%not-base64%%%'; then
  fail "bad base64 case: entrypoint should exit non-zero"
else
  [[ ! -e "$APP_DIR/config.json" ]] || fail "bad base64 case: config.json was left behind"
  [[ ! -f "$MAIN_ARGS" ]] || fail "bad base64 case: main was started"
  [[ -z "$(leftovers "$APP_DIR" 'config.json*')" ]] || fail "bad base64 case: partial file left behind:$(leftovers "$APP_DIR" 'config.json*')"
fi

# 6. A decoder that emits partial output and then fails must not leave that output as config.json.
mkdir -p "$WORK/badbin"
cat > "$WORK/badbin/base64" <<'STUB'
#!/usr/bin/env bash
printf '{"partial":'
exit 1
STUB
chmod +x "$WORK/badbin/base64"
if run partial PATH="$WORK/badbin:$PATH" BIFROST_CONFIG_B64="$B64"; then
  fail "partial write case: entrypoint should exit non-zero"
else
  [[ -z "$(leftovers "$APP_DIR" 'config.json*')" ]] || fail "partial write case: partial config left behind:$(leftovers "$APP_DIR" 'config.json*')"
fi

# 6b. A filesystem write failure stops the start and leaves no config.json.
# The failure comes from RLIMIT_FSIZE=0, under which every write to a regular
# file fails (EFBIG, or SIGXFSZ for the writing subshell) whatever the uid; a
# directory with mode 555 would not do, because root's CAP_DAC_OVERRIDE ignores
# it. The mkdir write probe is unaffected, so the first failing write is the
# config temp file. Output goes through a pipe, which the limit does not cover.
APP_DIR="$WORK/data-rofs"; mkdir -p "$APP_DIR"
export APP_DIR MAIN_ARGS="$WORK/args-rofs"; rm -f "$MAIN_ARGS"
if ( ulimit -f 0; exec env -u BIFROST_CONFIG_B64 -u PORT BIFROST_PORT_FILE="$WORK/port-rofs" BIFROST_CONFIG_JSON="$JSON" sh "$WORK/entrypoint.sh" ) 2>&1 | cat > "$WORK/log-rofs"; then
  fail "write-failure case: entrypoint should exit non-zero when the temp file cannot be written"
else
  [[ ! -e "$APP_DIR/config.json" ]] || fail "write-failure case: config.json appeared despite the failed write"
  [[ -z "$(leftovers "$APP_DIR" 'config.json*')" ]] || fail "write-failure case: partial file left behind:$(leftovers "$APP_DIR" 'config.json*')"
  [[ ! -f "$MAIN_ARGS" ]] || fail "write-failure case: main was started"
  grep -q 'Could not write' "$WORK/log-rofs" || fail "write-failure case: no write error reported, got: $(tail -3 "$WORK/log-rofs")"
fi

# 7. Nothing set: no file is written and main still starts.
if run none; then
  [[ ! -e "$APP_DIR/config.json" ]] || fail "no-env case: config.json appeared from nowhere"
  [[ -f "$MAIN_ARGS" ]] || fail "no-env case: main was not started"
else
  cat "$WORK/log-none" >&2; fail "no-env case: entrypoint exited non-zero"
fi

if [[ $FAILED -ne 0 ]]; then
  exit 1
fi
echo "PASS: docker-entrypoint.sh config bootstrap"
