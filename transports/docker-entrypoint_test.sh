#!/bin/sh
set -eu

SCRIPT_DIR=$(cd -- "$(dirname -- "$0")" && pwd)
ENTRYPOINT="$SCRIPT_DIR/docker-entrypoint.sh"

fail() {
    echo "FAIL: $1" >&2
    exit 1
}

TEST_ROOT=$(mktemp -d)
trap 'rm -rf "$TEST_ROOT"' EXIT

CONFIG_DIR="$TEST_ROOT/config"
mkdir -p "$CONFIG_DIR"

set +e
APP_DIR="$CONFIG_DIR" \
APP_PORT=8080 \
APP_HOST=127.0.0.1 \
LOG_LEVEL=info \
LOG_STYLE=json \
BIFROST_CONFIG='{"source_of_truth":"split"}' \
    sh "$ENTRYPOINT" >"$TEST_ROOT/materialize.out" 2>&1
set -e

[ -f "$CONFIG_DIR/config.json" ] || fail "BIFROST_CONFIG was not materialized"
[ "$(cat "$CONFIG_DIR/config.json")" = '{"source_of_truth":"split"}' ] || fail "materialized config content changed"
[ "$(stat -f '%Lp' "$CONFIG_DIR/config.json" 2>/dev/null || stat -c '%a' "$CONFIG_DIR/config.json")" = "600" ] || fail "materialized config mode is not 0600"

PAIR_DIR="$TEST_ROOT/pair"
mkdir -p "$PAIR_DIR"
set +e
APP_DIR="$PAIR_DIR" \
APP_PORT=8080 \
APP_HOST=127.0.0.1 \
LOG_LEVEL=info \
LOG_STYLE=json \
BIFROST_RUN_AS_UID=1000 \
    sh "$ENTRYPOINT" >"$TEST_ROOT/pair.out" 2>&1
PAIR_EXIT=$?
set -e

[ "$PAIR_EXIT" -ne 0 ] || fail "unpaired run-as setting was accepted"
grep -q "BIFROST_RUN_AS_UID and BIFROST_RUN_AS_GID must be set together" "$TEST_ROOT/pair.out" || fail "unpaired run-as setting did not fail clearly"

INVALID_DIR="$TEST_ROOT/invalid"
mkdir -p "$INVALID_DIR"
set +e
APP_DIR="$INVALID_DIR" \
APP_PORT=8080 \
APP_HOST=127.0.0.1 \
LOG_LEVEL=info \
LOG_STYLE=json \
BIFROST_RUN_AS_UID='1000:0' \
BIFROST_RUN_AS_GID=0 \
    sh "$ENTRYPOINT" >"$TEST_ROOT/invalid.out" 2>&1
INVALID_EXIT=$?
set -e

[ "$INVALID_EXIT" -ne 0 ] || fail "invalid run-as setting was accepted"
grep -q "must be non-negative integers" "$TEST_ROOT/invalid.out" || fail "invalid run-as setting did not fail clearly"

ROOT_UID_DIR="$TEST_ROOT/root-uid"
mkdir -p "$ROOT_UID_DIR"
set +e
APP_DIR="$ROOT_UID_DIR" \
APP_PORT=8080 \
APP_HOST=127.0.0.1 \
LOG_LEVEL=info \
LOG_STYLE=json \
BIFROST_RUN_AS_UID=0 \
BIFROST_RUN_AS_GID=0 \
    sh "$ENTRYPOINT" >"$TEST_ROOT/root-uid.out" 2>&1
ROOT_UID_EXIT=$?
set -e

[ "$ROOT_UID_EXIT" -ne 0 ] || fail "root run-as UID was accepted"
grep -q "BIFROST_RUN_AS_UID must be a non-zero UID" "$TEST_ROOT/root-uid.out" || fail "root run-as UID did not fail clearly"

echo "docker-entrypoint tests passed"
