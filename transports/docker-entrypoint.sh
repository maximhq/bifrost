#!/bin/sh
set -e

APP_DIR=${APP_DIR:-/app/data}

app_dir_writable() {
    PROBE_DIR="$APP_DIR/.bifrost-write-test.$$"
    if [ -e "$PROBE_DIR" ]; then
        PROBE_DIR="$PROBE_DIR.$(date +%s)"
    fi

    if mkdir "$PROBE_DIR" 2>/dev/null; then
        rmdir "$PROBE_DIR" 2>/dev/null || true
        return 0
    fi

    return 1
}

# Ensure APP_DIR exists when possible, but do not require CAP_CHOWN at startup.
ensure_app_dir() {
    mkdir -p "$APP_DIR" 2>/dev/null || true

    if [ ! -d "$APP_DIR" ]; then
        echo "Error: Could not create APP_DIR at $APP_DIR"
        echo "  Ensure the path exists or the parent directory is writable by the container user."
        exit 1
    fi

    CURRENT_UID=$(id -u)
    CURRENT_GID=$(id -g)

    # Ownership repair only works as root (needs CAP_CHOWN). Stat the data dir
    # here, inside the branch that actually uses the values.
    if [ "$CURRENT_UID" = "0" ]; then
        DATA_UID=$(stat -c '%u' "$APP_DIR" 2>/dev/null)
        DATA_GID=$(stat -c '%g' "$APP_DIR" 2>/dev/null)
        if [ "$DATA_UID:$DATA_GID" != "$CURRENT_UID:$CURRENT_GID" ]; then
            echo "Fixing permissions on $APP_DIR (was $DATA_UID:$DATA_GID, setting to $CURRENT_UID:$CURRENT_GID)"
            if chown -R "$CURRENT_UID:$CURRENT_GID" "$APP_DIR" 2>/dev/null && chmod -R g=rwX "$APP_DIR" 2>/dev/null; then
                echo "Successfully updated permissions on $APP_DIR"
            else
                echo "Warning: Could not update permissions on $APP_DIR"
            fi
        fi
    fi

    if ! app_dir_writable; then
        DATA_UID=$(stat -c '%u' "$APP_DIR" 2>/dev/null)
        DATA_GID=$(stat -c '%g' "$APP_DIR" 2>/dev/null)
        if [ "$BIFROST_SKIP_WRITE_CHECK" = "1" ]; then
            echo "Warning: $APP_DIR is not writable by UID:GID $CURRENT_UID:$CURRENT_GID (owned by $DATA_UID:$DATA_GID)"
            echo "  BIFROST_SKIP_WRITE_CHECK=1 set; continuing without a writable APP_DIR."
            echo "  Only safe for read-only deployments backed by external stores (e.g. Postgres)."
        else
            echo "Error: $APP_DIR is not writable by UID:GID $CURRENT_UID:$CURRENT_GID (owned by $DATA_UID:$DATA_GID)"
            echo "  Bifrost needs a writable APP_DIR for config.db and logs.db before startup."
            echo "  On vanilla Kubernetes, set podSecurityContext.fsGroup (for example, 1000)."
            echo "  On OpenShift (restricted-v2), leave fsGroup unset/null so the SCC assigns an in-range GID."
            echo "  Or mount a volume writable by GID 0, matching the image's group-0 ownership."
            echo "  Set BIFROST_SKIP_WRITE_CHECK=1 to bypass for read-only deployments with external stores."
            exit 1
        fi
    fi

    mkdir -p "$APP_DIR/logs" 2>/dev/null || true
    chmod g+rwX "$APP_DIR/logs" 2>/dev/null || true
}

# Prepare the app directory before starting the application
ensure_app_dir

# Parse command line arguments and set environment variables
parse_args() {
    while [ $# -gt 0 ]; do
        case $1 in
            --port|-port)
                if [ -n "$2" ]; then
                    export APP_PORT="$2"
                    shift 2
                else
                    echo "Error: --port requires a value"
                    exit 1
                fi
                ;;
            --host|-host)
                if [ -n "$2" ]; then
                    export APP_HOST="$2"
                    shift 2
                else
                    echo "Error: --host requires a value"
                    exit 1
                fi
                ;;
            --migrate-only|-migrate-only)
                export BIFROST_MIGRATE_ONLY=1
                shift
                ;;
            --no-migrate|-no-migrate)
                export BIFROST_NO_MIGRATE=1
                shift
                ;;
            --matview-refresh-only|-matview-refresh-only)
                export BIFROST_MATVIEW_REFRESH_ONLY=1
                shift
                ;;
            *)
                # Ignore anything else (including the default CMD /app/main):
                # the exec below passes an explicit, fixed argument list, so
                # unrecognized args were never forwarded. The previous
                # rotate-and-shift (`set -- "$@" "$1"; shift`) kept $# constant
                # and looped forever on any unrecognized argument.
                shift
                ;;
        esac
    done
}

# Parse arguments if any are provided
if [ $# -gt 0 ]; then
    parse_args "$@"
fi

# Migration flags via env vars so k8s Jobs/Deployments can toggle them
# without overriding the container command.
EXTRA_ARGS=""
case "$BIFROST_MIGRATE_ONLY" in
    1|true|TRUE|True) EXTRA_ARGS="$EXTRA_ARGS --migrate-only" ;;
esac
case "$BIFROST_NO_MIGRATE" in
    1|true|TRUE|True) EXTRA_ARGS="$EXTRA_ARGS --no-migrate" ;;
esac
case "$BIFROST_MATVIEW_REFRESH_ONLY" in
    1|true|TRUE|True) EXTRA_ARGS="$EXTRA_ARGS --matview-refresh-only" ;;
esac

# Build the command with environment variables and standard arguments
exec /app/main -app-dir "$APP_DIR" -port "$APP_PORT" -host "$APP_HOST" -log-level "$LOG_LEVEL" -log-style "$LOG_STYLE" $EXTRA_ARGS
