#!/usr/bin/env bash
# Sourceable helpers for the proxy e2e flow (run-proxy-matrix.sh): generate a CA and
# a proxy certificate, start the three off-the-shelf proxies, and read their logs.
#
#   squid       HTTP forward proxy with basic auth. One process, three ports:
#                 PROXY_HTTP_PORT   the http:// proxy a cell names
#                 PROXY_TLS_BACKEND the port stunnel forwards to (https:// cells)
#                 PROXY_INFRA_PORT  the gateway's own HTTPS_PROXY for traffic no cell
#                                   governs (datasheet sync at boot, etc.)
#               Every access-log line carries the port it arrived on (%lp), so a cell
#               can tell its own proxy's traffic from the infra proxy's.
#   stunnel     TLS in front of squid: the https:// proxy (PROXY_TLS_PORT).
#   microsocks  SOCKS5 with RFC 1929 username/password (PROXY_SOCKS_PORT).
#
# Usage:
#   REPO_ROOT=... source proxy-lib.sh
#   proxy_setup "$WORK_DIR"      # certs, htpasswd, configs
#   proxy_start                  # all three proxies, waits until they listen
#   trap proxy_stop EXIT
#   proxy_reset_logs             # before a cell
#   proxy_hosts_on_port 3128     # CONNECT targets squid saw on a port
#   proxy_socks_hosts            # CONNECT targets microsocks saw

: "${REPO_ROOT:?REPO_ROOT must be set before sourcing proxy-lib.sh}"

PROXY_HTTP_PORT="${PROXY_HTTP_PORT:-3128}"
PROXY_TLS_PORT="${PROXY_TLS_PORT:-3129}"
PROXY_INFRA_PORT="${PROXY_INFRA_PORT:-3130}"
PROXY_TLS_BACKEND="${PROXY_TLS_BACKEND:-3131}"
PROXY_SOCKS_PORT="${PROXY_SOCKS_PORT:-1080}"
PROXY_USER="${PROXY_USER:-bifrost-e2e}"
# Random per run: the password only has to match between the proxies and the config
# this run seeds, and it never leaves the runner.
PROXY_PASS="${PROXY_PASS:-$(openssl rand -hex 12)}"

PROXY_DIR=""
PROXY_PIDS=()

# proxy_find_bin <name...> prints the first executable found on PATH or in the sbin
# directories apt and Homebrew install proxies into.
proxy_find_bin() {
  local name dir
  for name in "$@"; do
    if command -v "$name" >/dev/null 2>&1; then
      command -v "$name"
      return 0
    fi
    for dir in /usr/sbin /usr/local/sbin /opt/homebrew/sbin /usr/lib/squid /usr/lib64/squid; do
      if [ -x "$dir/$name" ]; then
        echo "$dir/$name"
        return 0
      fi
    done
  done
  return 1
}

proxy_squid_auth_helper() {
  local candidate
  for candidate in \
    /usr/lib/squid/basic_ncsa_auth \
    /usr/lib64/squid/basic_ncsa_auth \
    "$(brew --prefix squid 2>/dev/null)/libexec/basic_ncsa_auth"; do
    if [ -x "$candidate" ]; then
      echo "$candidate"
      return 0
    fi
  done
  return 1
}

# proxy_setup <work_dir> writes the CA, the proxy certificate (valid for 127.0.0.1 and
# localhost), the htpasswd file and the squid and stunnel configs.
proxy_setup() {
  PROXY_DIR="$1"
  rm -rf "$PROXY_DIR"
  mkdir -p "$PROXY_DIR"

  SQUID_BIN="$(proxy_find_bin squid)" || { echo "❌ squid not found (apt install squid / brew install squid)" >&2; return 1; }
  STUNNEL_BIN="$(proxy_find_bin stunnel4 stunnel)" || { echo "❌ stunnel not found (apt install stunnel4 / brew install stunnel)" >&2; return 1; }
  MICROSOCKS_BIN="$(proxy_find_bin microsocks)" || { echo "❌ microsocks not found (apt install microsocks / brew install microsocks)" >&2; return 1; }
  local auth_helper
  auth_helper="$(proxy_squid_auth_helper)" || { echo "❌ squid basic_ncsa_auth helper not found" >&2; return 1; }

  echo "🔐 Generating the proxy CA and certificate..."
  openssl req -x509 -newkey rsa:2048 -nodes -days 2 -subj "/CN=Bifrost proxy e2e CA" \
    -keyout "$PROXY_DIR/ca.key" -out "$PROXY_DIR/ca.pem" >/dev/null 2>&1
  openssl req -newkey rsa:2048 -nodes -subj "/CN=127.0.0.1" \
    -keyout "$PROXY_DIR/proxy.key" -out "$PROXY_DIR/proxy.csr" >/dev/null 2>&1
  printf 'subjectAltName=IP:127.0.0.1,DNS:localhost\nextendedKeyUsage=serverAuth\n' > "$PROXY_DIR/proxy.ext"
  openssl x509 -req -in "$PROXY_DIR/proxy.csr" -CA "$PROXY_DIR/ca.pem" -CAkey "$PROXY_DIR/ca.key" \
    -CAcreateserial -days 2 -extfile "$PROXY_DIR/proxy.ext" -out "$PROXY_DIR/proxy.pem" >/dev/null 2>&1

  printf '%s:%s\n' "$PROXY_USER" "$(openssl passwd -apr1 "$PROXY_PASS")" > "$PROXY_DIR/htpasswd"

  # CONNECT only to 443: every provider upstream is TLS, so a CONNECT anywhere else
  # means a stack sent something it should not have.
  cat > "$PROXY_DIR/squid.conf" <<EOF
http_port 127.0.0.1:$PROXY_HTTP_PORT
http_port 127.0.0.1:$PROXY_TLS_BACKEND
http_port 127.0.0.1:$PROXY_INFRA_PORT
auth_param basic program $auth_helper $PROXY_DIR/htpasswd
auth_param basic realm bifrost-proxy-e2e
acl authenticated proxy_auth REQUIRED
acl SSL_ports port 443
acl CONNECT method CONNECT
http_access deny CONNECT !SSL_ports
http_access allow authenticated
http_access deny all
logformat bifrost %lp %>a %rm %ru %>Hs %un
access_log stdio:$PROXY_DIR/squid-access.log bifrost
cache_log $PROXY_DIR/squid-cache.log
pid_filename $PROXY_DIR/squid.pid
coredump_dir $PROXY_DIR
cache deny all
cache_mem 8 MB
shutdown_lifetime 1 seconds
EOF

  cat > "$PROXY_DIR/stunnel.conf" <<EOF
foreground = yes
pid =
output = $PROXY_DIR/stunnel.log
debug = notice

[https-proxy]
accept = 127.0.0.1:$PROXY_TLS_PORT
connect = 127.0.0.1:$PROXY_TLS_BACKEND
cert = $PROXY_DIR/proxy.pem
key = $PROXY_DIR/proxy.key
EOF
}

proxy_wait_port() {
  local port="$1" name="$2" i
  for i in $(seq 1 50); do
    if (echo >"/dev/tcp/127.0.0.1/$port") >/dev/null 2>&1; then
      return 0
    fi
    sleep 0.2
  done
  echo "❌ $name did not start listening on 127.0.0.1:$port" >&2
  return 1
}

proxy_start() {
  echo "🧱 Starting squid (:$PROXY_HTTP_PORT http, :$PROXY_INFRA_PORT infra, :$PROXY_TLS_BACKEND tls backend)..."
  "$SQUID_BIN" -f "$PROXY_DIR/squid.conf" -N -d 1 > "$PROXY_DIR/squid-stdout.log" 2>&1 &
  PROXY_PIDS+=($!)
  echo "🧱 Starting stunnel (:$PROXY_TLS_PORT https -> :$PROXY_TLS_BACKEND)..."
  "$STUNNEL_BIN" "$PROXY_DIR/stunnel.conf" > "$PROXY_DIR/stunnel-stdout.log" 2>&1 &
  PROXY_PIDS+=($!)
  echo "🧱 Starting microsocks (:$PROXY_SOCKS_PORT socks5)..."
  "$MICROSOCKS_BIN" -i 127.0.0.1 -p "$PROXY_SOCKS_PORT" -u "$PROXY_USER" -P "$PROXY_PASS" > "$PROXY_DIR/microsocks.log" 2>&1 &
  PROXY_PIDS+=($!)

  proxy_wait_port "$PROXY_HTTP_PORT" squid || { cat "$PROXY_DIR/squid-stdout.log" "$PROXY_DIR/squid-cache.log" 2>/dev/null; return 1; }
  proxy_wait_port "$PROXY_INFRA_PORT" squid || return 1
  proxy_wait_port "$PROXY_TLS_BACKEND" squid || return 1
  proxy_wait_port "$PROXY_TLS_PORT" stunnel || { cat "$PROXY_DIR/stunnel.log" 2>/dev/null; return 1; }
  proxy_wait_port "$PROXY_SOCKS_PORT" microsocks || { cat "$PROXY_DIR/microsocks.log"; return 1; }
  proxy_self_check
}

# proxy_self_check proves each proxy demands its credentials before any cell relies
# on it: a proxy that let anonymous clients through would turn every credential row
# into a no-op.
proxy_self_check() {
  # --noproxy "": each check must go through its proxy even when the runner exports
  # NO_PROXY/no_proxy covering 127.0.0.1, or curl would reach the target directly.
  local code
  code="$(curl -s -o /dev/null -w '%{http_code}' --noproxy "" --max-time 5 -x "http://127.0.0.1:$PROXY_HTTP_PORT" http://proxy-self-check.invalid/ || true)"
  if [ "$code" != "407" ]; then
    echo "❌ squid answered an anonymous request with $code, want 407" >&2
    return 1
  fi
  code="$(curl -s -o /dev/null -w '%{http_code}' --noproxy "" --max-time 5 --proxy-cacert "$PROXY_DIR/ca.pem" -x "https://127.0.0.1:$PROXY_TLS_PORT" http://proxy-self-check.invalid/ || true)"
  if [ "$code" != "407" ]; then
    echo "❌ the https proxy answered an anonymous request with $code, want 407 (check stunnel.log)" >&2
    return 1
  fi
  # Both SOCKS5 checks go to the local squid port, which always answers HTTP, so the only
  # thing deciding the outcome is whether microsocks lets the client through. A target
  # that cannot answer would fail both ways and prove nothing.
  local squid_target="http://127.0.0.1:$PROXY_HTTP_PORT/"
  if curl -s -o /dev/null --noproxy "" --max-time 5 -x "socks5://127.0.0.1:$PROXY_SOCKS_PORT" "$squid_target" 2>/dev/null; then
    echo "❌ microsocks accepted an anonymous client" >&2
    return 1
  fi
  code="$(curl -s -o /dev/null -w '%{http_code}' --noproxy "" --max-time 5 -x "socks5://$PROXY_USER:$PROXY_PASS@127.0.0.1:$PROXY_SOCKS_PORT" "$squid_target" || true)"
  if [ "$code" = "000" ]; then
    echo "❌ microsocks refused its own credentials" >&2
    return 1
  fi
  echo "✅ All three proxies refuse anonymous clients"
}

proxy_stop() {
  local pid
  for pid in "${PROXY_PIDS[@]:-}"; do
    [ -n "$pid" ] && kill "$pid" 2>/dev/null || true
  done
  for pid in "${PROXY_PIDS[@]:-}"; do
    [ -n "$pid" ] && wait "$pid" 2>/dev/null || true
  done
  PROXY_PIDS=()
}

# proxy_reset_logs truncates the logs a cell reads. squid and microsocks keep their
# file offsets, so truncating in place is enough.
proxy_reset_logs() {
  : > "$PROXY_DIR/squid-access.log"
  : > "$PROXY_DIR/microsocks.log"
  : > "$PROXY_DIR/stunnel.log"
}

# proxy_hosts_on_port <port> prints the CONNECT targets (host:port) squid accepted on
# a port, one per line. Only authenticated CONNECTs count: a 407 is a refused client.
proxy_hosts_on_port() {
  awk -v port="$1" '$1 == port && $3 == "CONNECT" && $5 != "407" { print $4 }' "$PROXY_DIR/squid-access.log"
}

proxy_socks_hosts() {
  sed -n 's/.*: connected to \(.*\)$/\1/p' "$PROXY_DIR/microsocks.log"
}

# proxy_tls_sessions counts the TLS sessions stunnel accepted since the last reset.
proxy_tls_sessions() {
  grep -c "accepted connection from" "$PROXY_DIR/stunnel.log" 2>/dev/null || true
}
