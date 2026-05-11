#!/bin/sh
set -e

APP_DIR=${APP_DIR:-/app/data}

# Function to fix permissions on mounted volumes
fix_permissions() {
    # Ensure runtime APP_DIR overrides exist before ownership checks
    mkdir -p "$APP_DIR" 2>/dev/null || true

    # Check if APP_DIR exists and fix ownership if needed
    if [ -d "$APP_DIR" ]; then
        # Get current user info
        CURRENT_UID=$(id -u)
        CURRENT_GID=$(id -g)
        
        # Get directory ownership
        DATA_UID=$(stat -c '%u' "$APP_DIR" 2>/dev/null || echo "0")
        DATA_GID=$(stat -c '%g' "$APP_DIR" 2>/dev/null || echo "0")
        
        # If ownership doesn't match current user, try to fix it
        if [ "$DATA_UID" != "$CURRENT_UID" ] || [ "$DATA_GID" != "$CURRENT_GID" ]; then
            echo "Fixing permissions on $APP_DIR (was $DATA_UID:$DATA_GID, setting to $CURRENT_UID:$CURRENT_GID)"
            
            # Try to change ownership (will work if running as root or if user has permission)
            if chown -R "$CURRENT_UID:$CURRENT_GID" "$APP_DIR" 2>/dev/null; then
                echo "Successfully updated permissions on $APP_DIR"
            else
                echo "Warning: Could not change ownership of $APP_DIR. You may need to run:"
                echo "  docker run --user \$(id -u):\$(id -g) ..."
                echo "  or ensure the host directory is owned by UID:GID $CURRENT_UID:$CURRENT_GID"
            fi
        fi
        
        # Ensure logs subdirectory exists with correct permissions
        mkdir -p "$APP_DIR/logs"
        chmod 755 "$APP_DIR/logs" 2>/dev/null || true
    fi
}

# Fix permissions before starting the application
fix_permissions

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
