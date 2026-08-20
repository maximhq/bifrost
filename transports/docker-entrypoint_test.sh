#!/bin/sh
set -eu

SCRIPT_DIR=$(cd -- "$(dirname -- "$0")" && pwd)
ENTRYPOINT="$SCRIPT_DIR/docker-entrypoint.sh"

fail() {
    echo "FAIL: $1" >&2
    exit 1
}

# Appending a non-newline sentinel before command substitution preserves any
# trailing newlines in the file, unlike a bare $(cat ...), while avoiding cmp:
# UBI Minimal does not include that utility in the runtime image.
file_content_matches() {
    [ "$(cat "$1"; printf x)" = "${2}x" ]
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
file_content_matches "$CONFIG_DIR/config.json" '{"source_of_truth":"split"}' || fail "materialized config content changed"
[ "$(stat -c '%a' "$CONFIG_DIR/config.json" 2>/dev/null || stat -f '%Lp' "$CONFIG_DIR/config.json")" = "600" ] || fail "materialized config mode is not 0600"

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

PATH_CONFIG_DIR="$TEST_ROOT/path-config"
mkdir -p "$PATH_CONFIG_DIR"
set +e
APP_DIR="$PATH_CONFIG_DIR" \
APP_PORT=8080 \
APP_HOST=127.0.0.1 \
LOG_LEVEL=info \
LOG_STYLE=json \
BIFROST_CONFIG=/etc/bifrost/config.json \
    sh "$ENTRYPOINT" >"$TEST_ROOT/path-config.out" 2>&1
PATH_CONFIG_EXIT=$?
set -e

[ "$PATH_CONFIG_EXIT" -ne 0 ] || fail "a path-valued BIFROST_CONFIG was accepted"
[ -f "$PATH_CONFIG_DIR/config.json" ] && fail "a path-valued BIFROST_CONFIG was written to config.json"
grep -q "must hold a complete inline config.json document" "$TEST_ROOT/path-config.out" || fail "path-valued BIFROST_CONFIG did not fail clearly"
grep -q "looks like a filesystem path" "$TEST_ROOT/path-config.out" || fail "path-valued BIFROST_CONFIG did not name the mistake"

GARBAGE_CONFIG_DIR="$TEST_ROOT/garbage-config"
mkdir -p "$GARBAGE_CONFIG_DIR"
set +e
APP_DIR="$GARBAGE_CONFIG_DIR" \
APP_PORT=8080 \
APP_HOST=127.0.0.1 \
LOG_LEVEL=info \
LOG_STYLE=json \
BIFROST_CONFIG='not json at all' \
    sh "$ENTRYPOINT" >"$TEST_ROOT/garbage-config.out" 2>&1
GARBAGE_CONFIG_EXIT=$?
set -e

[ "$GARBAGE_CONFIG_EXIT" -ne 0 ] || fail "a non-JSON BIFROST_CONFIG was accepted"
grep -q "must hold a complete inline config.json document" "$TEST_ROOT/garbage-config.out" || fail "non-JSON BIFROST_CONFIG did not fail clearly"

# A JSON document may be indented or start on a later line; only the first
# non-blank character decides.
INDENTED_CONFIG_DIR="$TEST_ROOT/indented-config"
mkdir -p "$INDENTED_CONFIG_DIR"
INDENTED_CONFIG='
  {"source_of_truth":"split"}
'
set +e
APP_DIR="$INDENTED_CONFIG_DIR" \
APP_PORT=8080 \
APP_HOST=127.0.0.1 \
LOG_LEVEL=info \
LOG_STYLE=json \
BIFROST_CONFIG="$INDENTED_CONFIG" \
    sh "$ENTRYPOINT" >"$TEST_ROOT/indented-config.out" 2>&1
set -e

# Every case here ends in `exec /app/main`, which exists only inside the image,
# so no run in this suite exits zero and no assertion may ask for it. Assert
# what a rejection would change instead: the leading newline and the indentation
# did not stop the document being materialized whole, and the diagnostic the
# reject path prints never appeared.
[ -f "$INDENTED_CONFIG_DIR/config.json" ] || fail "an indented BIFROST_CONFIG was rejected"
file_content_matches "$INDENTED_CONFIG_DIR/config.json" "$INDENTED_CONFIG" || fail "indented config content changed"
! grep -q "must hold a complete inline config.json document" "$TEST_ROOT/indented-config.out" || fail "an indented BIFROST_CONFIG was rejected"

# APP_DIR is the root of every anchored repair path. A trailing slash must not
# hide that APP_DIR itself is a symlink, or the final logs chmod and any
# root-started ownership repair would operate through it.
APP_DIR_LINK_TARGET="$TEST_ROOT/app-dir-link-target"
mkdir -p "$APP_DIR_LINK_TARGET/logs"
chmod 700 "$APP_DIR_LINK_TARGET/logs"
APP_DIR_LINK="$TEST_ROOT/app-dir-link"
ln -s "$APP_DIR_LINK_TARGET" "$APP_DIR_LINK"
set +e
APP_DIR="$APP_DIR_LINK/" \
APP_PORT=8080 \
APP_HOST=127.0.0.1 \
LOG_LEVEL=info \
LOG_STYLE=json \
    sh "$ENTRYPOINT" >"$TEST_ROOT/app-dir-link.out" 2>&1
APP_DIR_LINK_EXIT=$?
set -e

[ "$APP_DIR_LINK_EXIT" -ne 0 ] || fail "a symlinked APP_DIR was accepted"
grep -q "APP_DIR must not be a symlink" "$TEST_ROOT/app-dir-link.out" || fail "a symlinked APP_DIR did not fail clearly"
APP_DIR_LINK_LOGS_MODE=$(stat -c '%a' "$APP_DIR_LINK_TARGET/logs" 2>/dev/null || stat -f '%Lp' "$APP_DIR_LINK_TARGET/logs")
[ "$APP_DIR_LINK_LOGS_MODE" = "700" ] || fail "a symlinked APP_DIR changed an out-of-tree logs directory (now $APP_DIR_LINK_LOGS_MODE)"

# A database left behind by an earlier root-owned run sits inside an APP_DIR
# whose own ownership is already correct, so the create-directory probe passes
# and only a per-file check can catch it.
UNUSABLE_DIR="$TEST_ROOT/unusable-db"
mkdir -p "$UNUSABLE_DIR"
: >"$UNUSABLE_DIR/config.db"
chmod 000 "$UNUSABLE_DIR/config.db"
set +e
APP_DIR="$UNUSABLE_DIR" \
APP_PORT=8080 \
APP_HOST=127.0.0.1 \
LOG_LEVEL=info \
LOG_STYLE=json \
    sh "$ENTRYPOINT" >"$TEST_ROOT/unusable-db.out" 2>&1
UNUSABLE_EXIT=$?
set -e
chmod 600 "$UNUSABLE_DIR/config.db"

[ "$UNUSABLE_EXIT" -ne 0 ] || fail "an unusable config.db was accepted"
grep -q "$UNUSABLE_DIR/config.db is not usable" "$TEST_ROOT/unusable-db.out" || fail "unusable config.db was not named"

# config.json is only read, and mounting it read-only is supported: it must not
# be treated as an unusable path.
READONLY_CONFIG_DIR="$TEST_ROOT/readonly-config"
mkdir -p "$READONLY_CONFIG_DIR"
printf '%s' '{"source_of_truth":"split"}' >"$READONLY_CONFIG_DIR/config.json"
chmod 444 "$READONLY_CONFIG_DIR/config.json"
set +e
APP_DIR="$READONLY_CONFIG_DIR" \
APP_PORT=8080 \
APP_HOST=127.0.0.1 \
LOG_LEVEL=info \
LOG_STYLE=json \
    sh "$ENTRYPOINT" >"$TEST_ROOT/readonly-config.out" 2>&1
set -e
chmod 644 "$READONLY_CONFIG_DIR/config.json"

! grep -q "is not usable" "$TEST_ROOT/readonly-config.out" || fail "a read-only config.json was rejected"

# Read and write permission is not enough for a directory: without search
# permission Bifrost cannot create the log database inside logs/, and the probe
# cannot even stat what the directory already holds.
NO_TRAVERSE_DIR="$TEST_ROOT/no-traverse-logs"
mkdir -p "$NO_TRAVERSE_DIR/logs"
chmod 600 "$NO_TRAVERSE_DIR/logs"
set +e
APP_DIR="$NO_TRAVERSE_DIR" \
APP_PORT=8080 \
APP_HOST=127.0.0.1 \
LOG_LEVEL=info \
LOG_STYLE=json \
    sh "$ENTRYPOINT" >"$TEST_ROOT/no-traverse-logs.out" 2>&1
NO_TRAVERSE_EXIT=$?
set -e
chmod 700 "$NO_TRAVERSE_DIR/logs"

[ "$NO_TRAVERSE_EXIT" -ne 0 ] || fail "a logs directory without search permission was accepted"
grep -q "$NO_TRAVERSE_DIR/logs is not usable" "$TEST_ROOT/no-traverse-logs.out" || fail "logs directory without search permission was not named"

# SQLite opens its databases in WAL mode, so it leaves -wal and -shm sidecars
# beside them. A sidecar the target identity cannot open stops Bifrost exactly
# like an unusable database does, and it can outlive a correctly owned database:
# a root-owned run killed mid-flight leaves the sidecar behind, and repairing
# only the paths with fixed names would never look at it.
#
# These cases exercise the probe. The ownership repair reads the same glob list,
# but only runs as root with CAP_CHOWN, which this suite does not assume.
SIDECAR_DIR="$TEST_ROOT/unusable-sidecar"
mkdir -p "$SIDECAR_DIR"
: >"$SIDECAR_DIR/config.db"
: >"$SIDECAR_DIR/config.db-wal"
chmod 000 "$SIDECAR_DIR/config.db-wal"
set +e
APP_DIR="$SIDECAR_DIR" \
APP_PORT=8080 \
APP_HOST=127.0.0.1 \
LOG_LEVEL=info \
LOG_STYLE=json \
    sh "$ENTRYPOINT" >"$TEST_ROOT/unusable-sidecar.out" 2>&1
SIDECAR_EXIT=$?
set -e
chmod 600 "$SIDECAR_DIR/config.db-wal"

[ "$SIDECAR_EXIT" -ne 0 ] || fail "an unusable config.db-wal was accepted"
grep -q "$SIDECAR_DIR/config.db-wal is not usable" "$TEST_ROOT/unusable-sidecar.out" || fail "unusable config.db-wal was not named"

LOG_SIDECAR_DIR="$TEST_ROOT/unusable-log-sidecar"
mkdir -p "$LOG_SIDECAR_DIR"
: >"$LOG_SIDECAR_DIR/logs.db"
: >"$LOG_SIDECAR_DIR/logs.db-shm"
chmod 000 "$LOG_SIDECAR_DIR/logs.db-shm"
set +e
APP_DIR="$LOG_SIDECAR_DIR" \
APP_PORT=8080 \
APP_HOST=127.0.0.1 \
LOG_LEVEL=info \
LOG_STYLE=json \
    sh "$ENTRYPOINT" >"$TEST_ROOT/unusable-log-sidecar.out" 2>&1
LOG_SIDECAR_EXIT=$?
set -e
chmod 600 "$LOG_SIDECAR_DIR/logs.db-shm"

[ "$LOG_SIDECAR_EXIT" -ne 0 ] || fail "an unusable logs.db-shm was accepted"
grep -q "$LOG_SIDECAR_DIR/logs.db-shm is not usable" "$TEST_ROOT/unusable-log-sidecar.out" || fail "unusable logs.db-shm was not named"

# A hot rollback journal can predate the configured WAL mode. SQLite must read
# it during recovery before opening the database, so it is part of the same
# exact startup write set without widening the match to arbitrary backups.
JOURNAL_DIR="$TEST_ROOT/unusable-journal"
mkdir -p "$JOURNAL_DIR"
: >"$JOURNAL_DIR/config.db-journal"
chmod 000 "$JOURNAL_DIR/config.db-journal"
set +e
APP_DIR="$JOURNAL_DIR" \
APP_PORT=8080 \
APP_HOST=127.0.0.1 \
LOG_LEVEL=info \
LOG_STYLE=json \
    sh "$ENTRYPOINT" >"$TEST_ROOT/unusable-journal.out" 2>&1
JOURNAL_EXIT=$?
set -e
chmod 600 "$JOURNAL_DIR/config.db-journal"

[ "$JOURNAL_EXIT" -ne 0 ] || fail "an unusable config.db-journal was accepted"
grep -q "$JOURNAL_DIR/config.db-journal is not usable" "$TEST_ROOT/unusable-journal.out" || fail "unusable config.db-journal was not named"

LOG_JOURNAL_DIR="$TEST_ROOT/unusable-log-journal"
mkdir -p "$LOG_JOURNAL_DIR"
: >"$LOG_JOURNAL_DIR/logs.db-journal"
chmod 000 "$LOG_JOURNAL_DIR/logs.db-journal"
set +e
APP_DIR="$LOG_JOURNAL_DIR" \
APP_PORT=8080 \
APP_HOST=127.0.0.1 \
LOG_LEVEL=info \
LOG_STYLE=json \
    sh "$ENTRYPOINT" >"$TEST_ROOT/unusable-log-journal.out" 2>&1
LOG_JOURNAL_EXIT=$?
set -e
chmod 600 "$LOG_JOURNAL_DIR/logs.db-journal"

[ "$LOG_JOURNAL_EXIT" -ne 0 ] || fail "an unusable logs.db-journal was accepted"
grep -q "$LOG_JOURNAL_DIR/logs.db-journal is not usable" "$TEST_ROOT/unusable-log-journal.out" || fail "unusable logs.db-journal was not named"

# The counterpart to the glob: a volume may carry entries Bifrost never opens.
# ext4-backed volumes mount a root-owned lost+found, and requiring it to be
# usable would refuse a perfectly good disk on every platform that provides one.
UNRELATED_DIR="$TEST_ROOT/unrelated-entry"
mkdir -p "$UNRELATED_DIR/lost+found"
chmod 000 "$UNRELATED_DIR/lost+found"
set +e
APP_DIR="$UNRELATED_DIR" \
APP_PORT=8080 \
APP_HOST=127.0.0.1 \
LOG_LEVEL=info \
LOG_STYLE=json \
    sh "$ENTRYPOINT" >"$TEST_ROOT/unrelated-entry.out" 2>&1
set -e
chmod 700 "$UNRELATED_DIR/lost+found"

! grep -q "is not usable" "$TEST_ROOT/unrelated-entry.out" || fail "an unrelated unusable entry was rejected"

# The ownership repair only runs as root with CAP_CHOWN, which this suite does
# not assume, so exercise its path selection directly: lift the repair functions
# and the lists they walk out of the entrypoint, shadow chown and chmod with
# recorders, and check which paths they are asked for. This is the counterpart
# of the lost+found case above — an entry Bifrost never opens must not be
# refused at startup, and must not be handed to the target identity either.
REPAIR_SOURCE="$TEST_ROOT/repair-source.sh"
sed -n \
    -e '/^DATA_WRITE_ENTRIES=/p' \
    -e '/^DATA_WRITE_GLOBS=/p' \
    -e '/^repair_data_path() {/,/^}$/p' \
    -e '/^repair_data_paths() {/,/^}$/p' \
    "$ENTRYPOINT" >"$REPAIR_SOURCE"
grep -q '^repair_data_paths() {' "$REPAIR_SOURCE" || fail "the repair functions could not be lifted from the entrypoint"

# record_repair_calls <app_dir> <calls_file> <cwd>: run the lifted repair over
# <app_dir> from <cwd> and record every path chown and chmod are asked for. The
# working directory is a parameter because the glob list expands relative to it
# unless it is split with pathname expansion off.
record_repair_calls() {
    : >"$2"
    # shellcheck disable=SC2016 # The inner shell expands its own environment.
    APP_DIR="$1" REPAIR_SOURCE="$REPAIR_SOURCE" REPAIR_CALLS="$2" sh -c '
        set -e
        cd "$1" || exit 1
        TARGET_UID=1000
        TARGET_GID=0
        record() {
            for _arg in "$@"; do
                case "$_arg" in
                    -R|u+rwX|1000:0) continue ;;
                esac
                printf "%s\n" "$_arg" >>"$REPAIR_CALLS"
            done
        }
        chown() { record "$@"; }
        chmod() { record "$@"; }
        . "$REPAIR_SOURCE"
        repair_data_paths
    ' sh "$3"
}

REPAIR_DIR="$TEST_ROOT/repair-scope"
mkdir -p "$REPAIR_DIR/logs" "$REPAIR_DIR/lost+found"
: >"$REPAIR_DIR/config.db"
: >"$REPAIR_DIR/config.db-wal"
: >"$REPAIR_DIR/config.db-shm"
: >"$REPAIR_DIR/config.db-journal"
: >"$REPAIR_DIR/config.db-backup"
: >"$REPAIR_DIR/logs.db"
: >"$REPAIR_DIR/logs.db-wal"
: >"$REPAIR_DIR/logs.db-shm"
: >"$REPAIR_DIR/logs.db-journal"
: >"$REPAIR_DIR/logs.db-backup"
: >"$REPAIR_DIR/logs/2026-01-01.db"
: >"$REPAIR_DIR/config.json"
: >"$REPAIR_DIR/lost+found/#12345"
# A symlink inside logs/ is matched by the glob, so it would reach chmod as an
# operand — and chmod follows an operand symlink even under -R, where it skips
# the ones it meets during the walk. Following it would put a path outside
# APP_DIR under a recursive mode change.
OUTSIDE_DIR="$TEST_ROOT/outside-the-data-dir"
mkdir -p "$OUTSIDE_DIR"
: >"$OUTSIDE_DIR/out-of-tree"
ln -s "$OUTSIDE_DIR" "$REPAIR_DIR/logs/foreign-link"

REPAIR_CALLS="$TEST_ROOT/repair-calls.txt"
record_repair_calls "$REPAIR_DIR" "$REPAIR_CALLS" "$TEST_ROOT" \
    || fail "the repair reported a failure on a directory it could fully repair"

# The paths handed over directly. What sits inside logs/ must ride along with
# the recursive repair of logs itself; handing a child over separately repeats
# a recursive chown and chmod for every direct entry.
for REPAIRED in "" /config.db /config.db-wal /config.db-shm /config.db-journal /logs.db /logs.db-wal /logs.db-shm /logs.db-journal /logs; do
    grep -qx "$REPAIR_DIR$REPAIRED" "$REPAIR_CALLS" || fail "the repair skipped $REPAIR_DIR$REPAIRED"
done
! grep -q 'lost+found' "$REPAIR_CALLS" || fail "the repair reached an entry Bifrost never opens"
! grep -qx "$REPAIR_DIR/config.json" "$REPAIR_CALLS" || fail "the repair reached a config.json a deployment may mount read-only"
! grep -qx "$REPAIR_DIR/config.db-backup" "$REPAIR_CALLS" || fail "the repair treated config.db-backup as a SQLite sidecar"
! grep -qx "$REPAIR_DIR/logs.db-backup" "$REPAIR_CALLS" || fail "the repair treated logs.db-backup as a SQLite sidecar"
! grep -qx "$REPAIR_DIR/logs/foreign-link" "$REPAIR_CALLS" || fail "the repair handed a symlink to a recursive chown and chmod"
! grep -qx "$REPAIR_DIR/logs/2026-01-01.db" "$REPAIR_CALLS" || fail "the repair traversed a log file twice instead of relying on the recursive logs repair"

# The recorder proves the symlink never reaches chmod. It cannot prove why that
# matters, because it never runs the real chmod — so run it. The repair target
# here is this user, so chown succeeds and chmod actually executes. GNU
# coreutils follows the operand and would take the out-of-tree file from 000 to
# 600; BSD chmod defaults to -P and leaves it alone either way, so this case
# does its real work on the Linux runner CI uses.
LINK_REPAIR_DIR="$TEST_ROOT/link-repair"
mkdir -p "$LINK_REPAIR_DIR/logs"
LINK_TARGET_DIR="$TEST_ROOT/link-target"
mkdir -p "$LINK_TARGET_DIR"
: >"$LINK_TARGET_DIR/out-of-tree"
chmod 000 "$LINK_TARGET_DIR/out-of-tree"
ln -s "$LINK_TARGET_DIR" "$LINK_REPAIR_DIR/logs/foreign-link"

# shellcheck disable=SC2016 # The inner shell expands its own environment.
APP_DIR="$LINK_REPAIR_DIR" REPAIR_SOURCE="$REPAIR_SOURCE" sh -c '
    set -e
    TARGET_UID=$(id -u)
    TARGET_GID=$(id -g)
    . "$REPAIR_SOURCE"
    repair_data_paths
' || fail "the repair reported a failure on a directory it could fully repair"

OUT_OF_TREE_MODE=$(stat -c '%a' "$LINK_TARGET_DIR/out-of-tree" 2>/dev/null || stat -f '%Lp' "$LINK_TARGET_DIR/out-of-tree")
chmod 600 "$LINK_TARGET_DIR/out-of-tree"
[ "$OUT_OF_TREE_MODE" = "0" ] || fail "the repair followed a symlink and changed the mode of a path outside APP_DIR (now $OUT_OF_TREE_MODE)"
[ -L "$LINK_REPAIR_DIR/logs/foreign-link" ] || fail "the repair replaced a symlink inside logs/ instead of leaving it alone"

# A final-component symlink guard is not enough for logs/*: pathname expansion
# follows a symlink in the logs position and hands its ordinary descendants to
# repair_data_path. Prove the repair never expands through that parent symlink.
PARENT_LINK_REPAIR_DIR="$TEST_ROOT/parent-link-repair"
mkdir -p "$PARENT_LINK_REPAIR_DIR"
PARENT_LINK_TARGET_DIR="$TEST_ROOT/parent-link-target"
mkdir -p "$PARENT_LINK_TARGET_DIR"
: >"$PARENT_LINK_TARGET_DIR/out-of-tree"
chmod 000 "$PARENT_LINK_TARGET_DIR/out-of-tree"
ln -s "$PARENT_LINK_TARGET_DIR" "$PARENT_LINK_REPAIR_DIR/logs"

# shellcheck disable=SC2016 # The inner shell expands its own environment.
APP_DIR="$PARENT_LINK_REPAIR_DIR" REPAIR_SOURCE="$REPAIR_SOURCE" sh -c '
    set -e
    TARGET_UID=$(id -u)
    TARGET_GID=$(id -g)
    . "$REPAIR_SOURCE"
    repair_data_paths
' || fail "the repair reported a failure while skipping a top-level logs symlink"

PARENT_LINK_TARGET_MODE=$(stat -c '%a' "$PARENT_LINK_TARGET_DIR/out-of-tree" 2>/dev/null || stat -f '%Lp' "$PARENT_LINK_TARGET_DIR/out-of-tree")
chmod 600 "$PARENT_LINK_TARGET_DIR/out-of-tree"
[ "$PARENT_LINK_TARGET_MODE" = "0" ] || fail "the repair expanded through the logs symlink and changed a path outside APP_DIR (now $PARENT_LINK_TARGET_MODE)"
[ -L "$PARENT_LINK_REPAIR_DIR/logs" ] || fail "the repair replaced the top-level logs symlink instead of leaving it alone"

# repair_data_paths is not the only place that changes permissions: ensure_app_dir
# applies a final group-write chmod to logs/. Run the full entrypoint so that
# bootstrap path is covered too, and prove a top-level logs symlink cannot carry
# that chmod to a directory outside APP_DIR.
ENSURE_LINK_DIR="$TEST_ROOT/link-ensure"
mkdir -p "$ENSURE_LINK_DIR"
ENSURE_LINK_TARGET_DIR="$TEST_ROOT/link-ensure-target"
mkdir -p "$ENSURE_LINK_TARGET_DIR"
chmod 700 "$ENSURE_LINK_TARGET_DIR"
ln -s "$ENSURE_LINK_TARGET_DIR" "$ENSURE_LINK_DIR/logs"

set +e
APP_DIR="$ENSURE_LINK_DIR" \
APP_PORT=8080 \
APP_HOST=127.0.0.1 \
LOG_LEVEL=info \
LOG_STYLE=json \
    sh "$ENTRYPOINT" >"$TEST_ROOT/link-ensure.out" 2>&1
set -e

ENSURE_LINK_TARGET_MODE=$(stat -c '%a' "$ENSURE_LINK_TARGET_DIR" 2>/dev/null || stat -f '%Lp' "$ENSURE_LINK_TARGET_DIR")
chmod 700 "$ENSURE_LINK_TARGET_DIR"
[ "$ENSURE_LINK_TARGET_MODE" = "700" ] || fail "ensure_app_dir followed the logs symlink and changed a directory outside APP_DIR (now $ENSURE_LINK_TARGET_MODE)"
[ -L "$ENSURE_LINK_DIR/logs" ] || fail "ensure_app_dir replaced the logs symlink instead of leaving it alone"

# The symlink guard must not remove the group-write setup for a real logs
# directory; arbitrary-UID images rely on that permission surviving restarts.
ENSURE_REAL_DIR="$TEST_ROOT/real-logs-ensure"
mkdir -p "$ENSURE_REAL_DIR/logs"
chmod 700 "$ENSURE_REAL_DIR/logs"
set +e
APP_DIR="$ENSURE_REAL_DIR" \
APP_PORT=8080 \
APP_HOST=127.0.0.1 \
LOG_LEVEL=info \
LOG_STYLE=json \
    sh "$ENTRYPOINT" >"$TEST_ROOT/real-logs-ensure.out" 2>&1
set -e

ENSURE_REAL_MODE=$(stat -c '%a' "$ENSURE_REAL_DIR/logs" 2>/dev/null || stat -f '%Lp' "$ENSURE_REAL_DIR/logs")
[ "$ENSURE_REAL_MODE" = "770" ] || fail "ensure_app_dir did not preserve group-write setup for a real logs directory (now $ENSURE_REAL_MODE)"

# The pattern list contains logs/*. Split with pathname expansion on, that word
# expands against the working directory before anything anchors it to APP_DIR:
# a logs/ directory beside the process quietly becomes the thing "logs/*"
# refers to. Exercise both the probe and repair from exactly such a directory.
DECOY_CWD="$TEST_ROOT/decoy-cwd"
mkdir -p "$DECOY_CWD/logs"
: >"$DECOY_CWD/logs/decoy.db"

DECOY_APP_DIR="$TEST_ROOT/decoy-app-dir"
mkdir -p "$DECOY_APP_DIR/logs"
: >"$DECOY_APP_DIR/logs/2026-01-01.db"
chmod 000 "$DECOY_APP_DIR/logs/2026-01-01.db"
set +e
(
    cd "$DECOY_CWD" || exit 1
    APP_DIR="$DECOY_APP_DIR" \
    APP_PORT=8080 \
    APP_HOST=127.0.0.1 \
    LOG_LEVEL=info \
    LOG_STYLE=json \
        sh "$ENTRYPOINT"
) >"$TEST_ROOT/decoy-cwd.out" 2>&1
DECOY_EXIT=$?
set -e
chmod 600 "$DECOY_APP_DIR/logs/2026-01-01.db"

[ "$DECOY_EXIT" -ne 0 ] || fail "an unusable log database was accepted because the working directory held a logs/ of its own"
grep -q "$DECOY_APP_DIR/logs/2026-01-01.db is not usable" "$TEST_ROOT/decoy-cwd.out" || fail "the unusable log database was not named"

# The repair reads the same list. The exact sidecar must still be included, and
# the logs/* word must remain intact long enough for repair_data_paths to skip
# it in favor of the single recursive logs repair. If it expands while being
# split, logs/decoy.db becomes an ordinary-looking pattern and is repaired a
# second time when that same name exists under APP_DIR.
: >"$REPAIR_DIR/logs/decoy.db"
DECOY_REPAIR_CALLS="$TEST_ROOT/decoy-repair-calls.txt"
record_repair_calls "$REPAIR_DIR" "$DECOY_REPAIR_CALLS" "$DECOY_CWD" \
    || fail "the repair reported a failure on a directory it could fully repair"
grep -qx "$REPAIR_DIR/config.db-wal" "$DECOY_REPAIR_CALLS" || fail "the repair skipped an exact SQLite sidecar"
! grep -qx "$REPAIR_DIR/logs/decoy.db" "$DECOY_REPAIR_CALLS" || fail "the repair expanded logs/* against the working directory and repaired a log twice"

echo "docker-entrypoint tests passed"
