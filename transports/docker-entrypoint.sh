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

# Materialize config.json from the environment when the platform cannot mount
# a file (Railway, Render, one-click templates). BIFROST_CONFIG_JSON holds the
# raw document; BIFROST_CONFIG_B64 holds the same document base64-encoded for
# platforms whose variable editors mangle quotes or newlines. JSON wins when
# both are set. An existing config.json is never overwritten, so a persisted
# APP_DIR keeps whatever the dashboard has written since first boot.
write_config_from_env() {
    CONFIG_FILE="$APP_DIR/config.json"
    if [ -f "$CONFIG_FILE" ]; then
        return 0
    fi
    if [ -z "$BIFROST_CONFIG_JSON" ] && [ -z "$BIFROST_CONFIG_B64" ]; then
        return 0
    fi

    # Stage the document in a temp file created with mode 0600 (it may carry
    # literal secrets even though env. references are recommended), then move
    # it into place. A failed decode or a partial write therefore never leaves
    # a config.json behind for the next boot to keep.
    CONFIG_TMP="$CONFIG_FILE.tmp.$$"
    if [ -n "$BIFROST_CONFIG_JSON" ]; then
        CONFIG_SOURCE=BIFROST_CONFIG_JSON
        (umask 077 && printf '%s\n' "$BIFROST_CONFIG_JSON" > "$CONFIG_TMP") || {
            rm -f "$CONFIG_TMP"
            echo "Error: Could not write $CONFIG_FILE from BIFROST_CONFIG_JSON"
            exit 1
        }
    else
        CONFIG_SOURCE=BIFROST_CONFIG_B64
        (umask 077 && printf '%s' "$BIFROST_CONFIG_B64" | base64 -d > "$CONFIG_TMP") || {
            rm -f "$CONFIG_TMP"
            echo "Error: Could not decode BIFROST_CONFIG_B64 into $CONFIG_FILE"
            exit 1
        }
    fi
    mv -f "$CONFIG_TMP" "$CONFIG_FILE" || {
        rm -f "$CONFIG_TMP"
        echo "Error: Could not move the decoded configuration into $CONFIG_FILE"
        exit 1
    }
    echo "Wrote $CONFIG_FILE from $CONFIG_SOURCE"
}

write_config_from_env

# Hosted platforms (Fly.io, Heroku, Railway, Render) attach a database as one
# DATABASE_URL. The reference config.json reads PG_HOST, PG_PORT, PG_USER,
# PG_PASSWORD, PG_DATABASE and PG_SSL_MODE, so derive whichever of those is not
# already set from the URL. Explicit PG_* values always win.
# Percent-decode one URL component. Pure POSIX sh: dash and busybox printf do
# not understand \x escapes in %b, so each %HH is turned into an octal escape.
url_decode() {
    _in="$1"
    _out=""
    while [ -n "$_in" ]; do
        case "$_in" in
            %[0-9A-Fa-f][0-9A-Fa-f]*)
                _hex="${_in#%}"
                _hex="${_hex%"${_hex#??}"}"
                # shellcheck disable=SC2059 # the octal escape is the format on purpose
                _out="$_out$(printf "\\$(printf '%03o' "0x$_hex")")"
                _in="${_in#???}"
                ;;
            *)
                _out="$_out${_in%"${_in#?}"}"
                _in="${_in#?}"
                ;;
        esac
    done
    printf '%s' "$_out"
}

derive_pg_env_from_database_url() {
    [ -n "$DATABASE_URL" ] || return 0
    case "$DATABASE_URL" in
        postgres://*|postgresql://*) ;;
        *) return 0 ;;
    esac

    rest="${DATABASE_URL#*://}"
    # Fragment and query first, then cut the authority at the first "/": an
    # "@" or ":" in the path must never be read as credentials or a port.
    rest="${rest%%#*}"
    query=""
    case "$rest" in
        *\?*) query="${rest#*\?}"; rest="${rest%%\?*}" ;;
    esac

    authority="${rest%%/*}"
    dbname=""
    case "$rest" in
        */*) dbname="${rest#*/}" ;;
    esac

    userinfo=""
    hostport="$authority"
    case "$authority" in
        *@*) userinfo="${authority%@*}"; hostport="${authority##*@}" ;;
    esac

    user="$userinfo"
    pass=""
    case "$userinfo" in
        *:*) user="${userinfo%%:*}"; pass="${userinfo#*:}" ;;
    esac

    # Bracketed IPv6 literals carry colons of their own; strip the brackets
    # and only read a port that follows the closing bracket.
    host="$hostport"
    port="5432"
    case "$hostport" in
        \[*\]:*) host="${hostport%%\]:*}"; host="${host#\[}"; port="${hostport##*\]:}" ;;
        \[*\])   host="${hostport#\[}"; host="${host%\]}" ;;
        *:*)     host="${hostport%%:*}"; port="${hostport##*:}" ;;
    esac
    # "host:" and "?sslmode=" are valid URL forms with an empty value; they
    # take the same defaults as a missing one.
    [ -n "$port" ] || port="5432"

    sslmode="prefer"
    case "$query" in
        *sslmode=*) sslmode="${query#*sslmode=}"; sslmode="${sslmode%%&*}" ;;
    esac
    [ -n "$sslmode" ] || sslmode="prefer"

    [ -n "$PG_HOST" ]     || export PG_HOST="$host"
    [ -n "$PG_PORT" ]     || export PG_PORT="$port"
    if [ -z "$PG_USER" ]; then
        PG_USER=$(url_decode "$user")
        export PG_USER
    fi
    if [ -z "$PG_PASSWORD" ]; then
        PG_PASSWORD=$(url_decode "$pass")
        export PG_PASSWORD
    fi
    if [ -z "$PG_DATABASE" ]; then
        PG_DATABASE=$(url_decode "$dbname")
        export PG_DATABASE
    fi
    [ -n "$PG_SSL_MODE" ] || export PG_SSL_MODE="$sslmode"
    echo "Derived PG_* connection settings from DATABASE_URL (host=$PG_HOST db=$PG_DATABASE sslmode=$PG_SSL_MODE)"
}

derive_pg_env_from_database_url

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
