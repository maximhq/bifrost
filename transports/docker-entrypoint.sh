#!/bin/sh
set -e

APP_DIR=${APP_DIR:-/app/data}

# Ensure APP_DIR exists when possible, but do not require CAP_CHOWN at startup.
ensure_app_dir() {
    mkdir -p "$APP_DIR" 2>/dev/null || true

    if [ ! -d "$APP_DIR" ]; then
        return
    fi

    CURRENT_UID=$(id -u)
    CURRENT_GID=$(id -g)
    DATA_UID=$(stat -c '%u' "$APP_DIR" 2>/dev/null || echo "0")
    DATA_GID=$(stat -c '%g' "$APP_DIR" 2>/dev/null || echo "0")

    if [ "$CURRENT_UID" = "0" ]; then
        # Running as root, `test -w` always succeeds (the kernel bypasses DAC),
        # so gate the repair on ownership instead: chown whenever the directory
        # is not already owned by our UID.
        if [ "$DATA_UID" != "$CURRENT_UID" ]; then
            echo "Fixing permissions on $APP_DIR (was $DATA_UID:$DATA_GID, setting to $CURRENT_UID:$CURRENT_GID)"
            if chown -R "$CURRENT_UID:$CURRENT_GID" "$APP_DIR" 2>/dev/null && chmod -R g=rwX "$APP_DIR" 2>/dev/null; then
                echo "Successfully updated permissions on $APP_DIR"
            else
                echo "Warning: Could not update permissions on $APP_DIR"
            fi
        fi
    elif [ ! -w "$APP_DIR" ]; then
        # Non-root without CAP_CHOWN cannot repair ownership. Fail fast with
        # actionable guidance rather than crashing later while writing config.db.
        echo "Error: $APP_DIR is not writable by UID:GID $CURRENT_UID:$CURRENT_GID (owned by $DATA_UID:$DATA_GID)"
        echo "  Ensure the directory is owned by this UID or group-writable for a supplemental group such as 0."
        echo "  On Kubernetes, set podSecurityContext.fsGroup (e.g. 1000) so the volume is group-writable."
        exit 1
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

# Build the command with environment variables and standard arguments
exec /app/main -app-dir "$APP_DIR" -port "$APP_PORT" -host "$APP_HOST" -log-level "$LOG_LEVEL" -log-style "$LOG_STYLE"
