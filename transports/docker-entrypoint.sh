#!/bin/sh
set -e

APP_DIR=${APP_DIR:-/app/data}
RUN_AS_UID=${BIFROST_RUN_AS_UID:-}
RUN_AS_GID=${BIFROST_RUN_AS_GID:-}

validate_run_as_config() {
    if { [ -n "$RUN_AS_UID" ] && [ -z "$RUN_AS_GID" ]; } || { [ -z "$RUN_AS_UID" ] && [ -n "$RUN_AS_GID" ]; }; then
        echo "Error: BIFROST_RUN_AS_UID and BIFROST_RUN_AS_GID must be set together"
        exit 1
    fi

    if [ -z "$RUN_AS_UID" ]; then
        return
    fi

    case "$RUN_AS_UID" in
        *[!0-9]*)
            echo "Error: BIFROST_RUN_AS_UID and BIFROST_RUN_AS_GID must be non-negative integers"
            exit 1
            ;;
    esac
    case "$RUN_AS_GID" in
        *[!0-9]*)
            echo "Error: BIFROST_RUN_AS_UID and BIFROST_RUN_AS_GID must be non-negative integers"
            exit 1
            ;;
    esac

    if [ "$RUN_AS_UID" = "0" ]; then
        echo "Error: BIFROST_RUN_AS_UID must be a non-zero UID"
        exit 1
    fi

    if [ "$(id -u)" != "0" ]; then
        echo "Error: BIFROST_RUN_AS_UID and BIFROST_RUN_AS_GID require the entrypoint to start as root"
        exit 1
    fi

    if ! command -v su-exec >/dev/null 2>&1; then
        echo "Error: su-exec is required when BIFROST_RUN_AS_UID and BIFROST_RUN_AS_GID are set"
        exit 1
    fi
}

materialize_inline_config() {
    if [ "${BIFROST_CONFIG+x}" != "x" ]; then
        return
    fi
    if [ -z "$BIFROST_CONFIG" ]; then
        echo "Error: BIFROST_CONFIG is set but empty"
        exit 1
    fi

    CONFIG_PATH="$APP_DIR/config.json"
    if ! CONFIG_TMP=$(umask 077 && mktemp "$APP_DIR/.config.json.tmp.XXXXXX"); then
        echo "Error: Could not create a temporary config file in $APP_DIR"
        exit 1
    fi
    if ! printf '%s' "$BIFROST_CONFIG" > "$CONFIG_TMP" || ! chmod 0600 "$CONFIG_TMP"; then
        rm -f "$CONFIG_TMP"
        echo "Error: Could not materialize BIFROST_CONFIG at $CONFIG_PATH"
        exit 1
    fi
    if [ "$(id -u)" = "0" ] && [ -n "$RUN_AS_UID" ]; then
        if ! chown "$RUN_AS_UID:$RUN_AS_GID" "$CONFIG_TMP"; then
            rm -f "$CONFIG_TMP"
            echo "Error: Could not set BIFROST_CONFIG ownership to $RUN_AS_UID:$RUN_AS_GID"
            exit 1
        fi
    fi
    if ! mv -f "$CONFIG_TMP" "$CONFIG_PATH"; then
        rm -f "$CONFIG_TMP"
        echo "Error: Could not materialize BIFROST_CONFIG at $CONFIG_PATH"
        exit 1
    fi
    unset BIFROST_CONFIG
}

app_dir_writable() {
    if [ -n "$RUN_AS_UID" ]; then
        # shellcheck disable=SC2016 # The inner shell expands its own positional parameters.
        su-exec "$RUN_AS_UID:$RUN_AS_GID" sh -c '
            probe_dir="$1/.bifrost-write-test.$$"
            if [ -e "$probe_dir" ]; then
                probe_dir="$probe_dir.$(date +%s)"
            fi
            if mkdir "$probe_dir" 2>/dev/null; then
                rmdir "$probe_dir" 2>/dev/null || true
                exit 0
            fi
            exit 1
        ' sh "$APP_DIR"
        return
    fi

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
    TARGET_UID=${RUN_AS_UID:-$CURRENT_UID}
    TARGET_GID=${RUN_AS_GID:-$CURRENT_GID}

    materialize_inline_config
    mkdir -p "$APP_DIR/logs" 2>/dev/null || true
    if [ "$(id -u)" = "0" ] && [ -n "$RUN_AS_UID" ]; then
        chown "$RUN_AS_UID:$RUN_AS_GID" "$APP_DIR/logs" 2>/dev/null || true
    fi

    # Ownership repair only works as root (needs CAP_CHOWN). Stat the data dir
    # here, inside the branch that actually uses the values.
    if [ "$CURRENT_UID" = "0" ]; then
        DATA_UID=$(stat -c '%u' "$APP_DIR" 2>/dev/null)
        DATA_GID=$(stat -c '%g' "$APP_DIR" 2>/dev/null)
        if [ "$DATA_UID:$DATA_GID" != "$TARGET_UID:$TARGET_GID" ]; then
            echo "Fixing permissions on $APP_DIR (was $DATA_UID:$DATA_GID, setting to $TARGET_UID:$TARGET_GID)"
            if chown -R "$TARGET_UID:$TARGET_GID" "$APP_DIR" 2>/dev/null && chmod -R u+rwX "$APP_DIR" 2>/dev/null; then
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
            echo "Error: $APP_DIR is not writable by UID:GID $TARGET_UID:$TARGET_GID (owned by $DATA_UID:$DATA_GID)"
            echo "  Bifrost needs a writable APP_DIR for config.db and logs.db before startup."
            echo "  On vanilla Kubernetes, set podSecurityContext.fsGroup (for example, 1000)."
            echo "  On OpenShift (restricted-v2), leave fsGroup unset/null so the SCC assigns an in-range GID."
            echo "  Or mount a volume writable by GID 0, matching the image's group-0 ownership."
            echo "  Set BIFROST_SKIP_WRITE_CHECK=1 to bypass for read-only deployments with external stores."
            exit 1
        fi
    fi

    chmod g+rwX "$APP_DIR/logs" 2>/dev/null || true
}

# Prepare the app directory before starting the application
validate_run_as_config
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
            *)
                # Keep other arguments for the main application
                set -- "$@" "$1"
                shift
                ;;
        esac
    done
}

# Parse arguments if any are provided
if [ $# -gt 1 ]; then
    parse_args "$@"
fi

# Build the command with environment variables and standard arguments.
if [ -n "$RUN_AS_UID" ]; then
    exec su-exec "$RUN_AS_UID:$RUN_AS_GID" /app/main -app-dir "$APP_DIR" -port "$APP_PORT" -host "$APP_HOST" -log-level "$LOG_LEVEL" -log-style "$LOG_STYLE"
fi

exec /app/main -app-dir "$APP_DIR" -port "$APP_PORT" -host "$APP_HOST" -log-level "$LOG_LEVEL" -log-style "$LOG_STYLE"
