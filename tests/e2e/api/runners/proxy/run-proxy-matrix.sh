#!/usr/bin/env bash
set -euo pipefail

# Proxy e2e: real provider calls through real proxies, for every way Bifrost can be
# told to use one.
#
# It starts squid (http://), stunnel in front of squid (https://, TLS to the proxy) and
# microsocks (socks5://), all requiring credentials, then runs one cell per
# (mode, proxy):
#
#   global-http  global-https  global-socks5         PUT /api/proxy-config, Inference + API on
#   provider-http  provider-https  provider-socks5   proxy_config on every provider
#   env-http  env-https  env-socks5                  proxy_config type environment + HTTPS_PROXY
#
# Each cell boots a fresh gateway (harness-gateway.sh), runs the provider harness on
# proxy-smoke-manifest.json (one upstream-reaching row per provider), and then requires
# every provider host in the manifest to appear in the log of that cell's proxy. The
# gateway's own HTTPS_PROXY points at a separate squid port (the infra proxy) in the
# global and provider cells, so traffic that fell back to the environment shows up on
# the wrong port and fails the cell instead of passing it.
#
# Strict egress (PROXY_E2E_STRICT_EGRESS=1, Linux + sudo): the gateway runs in its own
# group and iptables rejects anything it sends off loopback, so a connection that skips
# the proxy fails the request outright. CI turns this on.
#
# Environment:
#   PROXY_CELLS               cells to run (default: all nine)
#   PORT                      gateway port (default 8080; must be free)
#   SKIP_GATEWAY_BUILD=1      reuse tmp/bifrost-http (or HARNESS_BINARY)
#   HARNESS_BINARY            gateway binary to boot (bifrost-enterprise passes its own)
#   PROXY_E2E_STRICT_EGRESS   1 to confine the gateway with iptables (Linux only)
#   PROXY_E2E_ADMIN_AUTH      Authorization header value for /api calls, when the
#                             gateway enforces auth
#   RERUN_ATTEMPTS            re-runs of only the failed items per cell (default 1)
#
# Provider secrets come from the environment, as for `make run-provider-harness-test`;
# `make run-e2e-api PROXY=1` loads them from Infisical or .env first.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../../../.." && pwd -P)"
cd "$REPO_ROOT"

PORT="${PORT:-8080}"
BASE_URL="http://localhost:$PORT"
WORK_DIR_REL="tmp/proxy-e2e"
WORK_DIR="$REPO_ROOT/$WORK_DIR_REL"
APP_DIR_REL="$WORK_DIR_REL/app"
APP_DIR="$REPO_ROOT/$APP_DIR_REL"
MANIFEST="tests/e2e/api/collections/proxy-smoke-manifest.json"
ALL_CELLS="global-http global-https global-socks5 provider-http provider-https provider-socks5 env-http env-https env-socks5"
PROXY_CELLS="${PROXY_CELLS:-$ALL_CELLS}"
RERUN_ATTEMPTS="${RERUN_ATTEMPTS:-1}"
EGRESS_GROUP="bifrost-egress"

for tool in jq curl openssl newman node; do
  command -v "$tool" >/dev/null 2>&1 || { echo "❌ $tool is required" >&2; exit 1; }
done
for cell in $PROXY_CELLS; do
  case " $ALL_CELLS " in *" $cell "*) ;; *) echo "❌ unknown cell '$cell' (known: $ALL_CELLS)" >&2; exit 1 ;; esac
done
if (echo >"/dev/tcp/127.0.0.1/$PORT") >/dev/null 2>&1; then
  echo "❌ Port $PORT is already in use. The harness would test whatever answers there, not this build." >&2
  exit 1
fi

# shellcheck source=../../../../../.github/workflows/scripts/harness-gateway.sh
source "$REPO_ROOT/.github/workflows/scripts/harness-gateway.sh"
# shellcheck source=./proxy-lib.sh
source "$SCRIPT_DIR/proxy-lib.sh"

# One flag per installed rule, each set right after its insert, so a failed second
# insert (set -e) still leaves the first one for cleanup to remove.
STRICT_RULE_V4_ADDED=0
STRICT_RULE_V6_ADDED=0
cleanup() {
  local exit_code=$?
  harness_stop_gateway
  proxy_stop
  if [ "$STRICT_RULE_V4_ADDED" = "1" ]; then
    sudo iptables -D OUTPUT -m owner --gid-owner "$EGRESS_GROUP" ! -o lo -j REJECT 2>/dev/null || true
  fi
  if [ "$STRICT_RULE_V6_ADDED" = "1" ]; then
    sudo ip6tables -D OUTPUT -m owner --gid-owner "$EGRESS_GROUP" ! -o lo -j REJECT 2>/dev/null || true
  fi
  exit "$exit_code"
}
trap cleanup EXIT

if [ -f "$REPO_ROOT/.github/workflows/scripts/setup-go-workspace.sh" ] && [ "${SKIP_GATEWAY_BUILD:-0}" != "1" ]; then
  source "$REPO_ROOT/.github/workflows/scripts/setup-go-workspace.sh"
fi
harness_build_gateway

proxy_setup "$WORK_DIR/proxies"
proxy_start

# The system roots plus the proxy CA. Go honours SSL_CERT_FILE on Linux, which is how
# the global-https cell trusts its proxy there (the global proxy has no CA field).
CA_BUNDLE="$WORK_DIR/ca-bundle.pem"
: > "$CA_BUNDLE"
for system_bundle in /etc/ssl/certs/ca-certificates.crt /etc/pki/tls/certs/ca-bundle.crt /etc/ssl/cert.pem; do
  if [ -f "$system_bundle" ]; then
    cat "$system_bundle" >> "$CA_BUNDLE"
    break
  fi
done
cat "$PROXY_DIR/ca.pem" >> "$CA_BUNDLE"

if [ "${PROXY_E2E_STRICT_EGRESS:-0}" = "1" ]; then
  if [ "$(uname -s)" != "Linux" ]; then
    echo "❌ PROXY_E2E_STRICT_EGRESS=1 needs Linux (iptables owner match)" >&2
    exit 1
  fi
  echo "🔒 Confining the gateway: group $EGRESS_GROUP may only reach loopback..."
  sudo groupadd -f "$EGRESS_GROUP"
  sudo usermod -aG "$EGRESS_GROUP" "$(id -un)"
  sudo iptables -I OUTPUT -m owner --gid-owner "$EGRESS_GROUP" ! -o lo -j REJECT
  STRICT_RULE_V4_ADDED=1
  sudo ip6tables -I OUTPUT -m owner --gid-owner "$EGRESS_GROUP" ! -o lo -j REJECT
  STRICT_RULE_V6_ADDED=1
  # Prove the rule bites before trusting it: a direct call from the group must fail
  # and the same call through the proxy must succeed.
  if sg "$EGRESS_GROUP" -c "curl -s -o /dev/null --max-time 5 https://api.openai.com/v1/models"; then
    echo "❌ strict egress is not in force: a direct call from $EGRESS_GROUP succeeded" >&2
    exit 1
  fi
  if ! sg "$EGRESS_GROUP" -c "curl -s -o /dev/null --noproxy \"\" --max-time 10 -x http://$PROXY_USER:$PROXY_PASS@127.0.0.1:$PROXY_HTTP_PORT https://api.openai.com/v1/models"; then
    echo "❌ strict egress blocks the proxy path too" >&2
    exit 1
  fi
  echo "✅ Direct egress refused, proxied egress allowed"
  export HARNESS_GATEWAY_GROUP="$EGRESS_GROUP"
fi

# cell_proxy_url <kind> [with-credentials] prints the proxy URL a cell names.
cell_proxy_url() {
  local kind="$1" creds="${2:-}" userinfo=""
  [ -n "$creds" ] && userinfo="$PROXY_USER:$PROXY_PASS@"
  case "$kind" in
    http) echo "http://${userinfo}127.0.0.1:$PROXY_HTTP_PORT" ;;
    https) echo "https://${userinfo}127.0.0.1:$PROXY_TLS_PORT" ;;
    socks5) echo "socks5://${userinfo}127.0.0.1:$PROXY_SOCKS_PORT" ;;
  esac
}

INFRA_PROXY="http://$PROXY_USER:$PROXY_PASS@127.0.0.1:$PROXY_INFRA_PORT"
NO_PROXY_LIST="localhost,127.0.0.1,::1"

# seed_cell <mode> <kind> writes the cell's config.json and sets HARNESS_GATEWAY_ENV.
seed_cell() {
  local mode="$1" kind="$2" config="$APP_DIR/config.json" ca=""
  harness_seed_app_dir "$APP_DIR"
  [ "$kind" = "https" ] && ca="$(cat "$PROXY_DIR/ca.pem")"
  local gateway_env="NO_PROXY=$NO_PROXY_LIST no_proxy=$NO_PROXY_LIST"
  case "$mode" in
    global)
      jq 'del(.providers[].proxy_config)' "$config" > "$config.tmp"
      gateway_env="$gateway_env HTTPS_PROXY=$INFRA_PROXY HTTP_PROXY=$INFRA_PROXY"
      if [ "$kind" = "https" ] && [ "$(uname -s)" = "Linux" ]; then
        gateway_env="$gateway_env SSL_CERT_FILE=$CA_BUNDLE"
      fi
      ;;
    provider)
      jq --arg type "$([ "$kind" = socks5 ] && echo socks5 || echo http)" \
        --arg url "$(cell_proxy_url "$kind")" --arg user "$PROXY_USER" --arg pass "$PROXY_PASS" --arg ca "$ca" \
        '.providers |= map_values(.proxy_config = ({type: $type, url: $url, username: $user, password: $pass} + (if $ca != "" then {ca_cert_pem: $ca} else {} end)))' \
        "$config" > "$config.tmp"
      gateway_env="$gateway_env HTTPS_PROXY=$INFRA_PROXY HTTP_PROXY=$INFRA_PROXY"
      ;;
    env)
      jq --arg ca "$ca" \
        '.providers |= map_values(.proxy_config = ({type: "environment"} + (if $ca != "" then {ca_cert_pem: $ca} else {} end)))' \
        "$config" > "$config.tmp"
      local url
      url="$(cell_proxy_url "$kind" creds)"
      gateway_env="$gateway_env HTTPS_PROXY=$url HTTP_PROXY=$url"
      ;;
  esac
  mv "$config.tmp" "$config"
  export HARNESS_GATEWAY_ENV="$gateway_env"
}

api_curl() {
  local auth=()
  [ -n "${PROXY_E2E_ADMIN_AUTH:-}" ] && auth=(-H "Authorization: $PROXY_E2E_ADMIN_AUTH")
  curl -fsS --max-time 30 ${auth[@]+"${auth[@]}"} "$@"
}

# enable_global_proxy <kind> turns the global proxy on for Inference and API traffic.
enable_global_proxy() {
  local kind="$1" skip_verify=false
  if [ "$kind" = "https" ] && [ "$(uname -s)" != "Linux" ]; then
    # Go ignores SSL_CERT_FILE on macOS and the global proxy has no CA field, so a
    # local run can only trust the private proxy CA by skipping verification.
    echo "⚠️  global-https on $(uname -s): skip_tls_verify=true (Linux runs verify the proxy through SSL_CERT_FILE)"
    skip_verify=true
  fi
  # run_cell calls this inside "if !", where errexit is off: check every step.
  local payload
  if ! payload="$(jq -n --arg type "$([ "$kind" = socks5 ] && echo socks5 || echo http)" \
    --arg url "$(cell_proxy_url "$kind")" --arg user "$PROXY_USER" --arg pass "$PROXY_PASS" \
    --arg no_proxy "$NO_PROXY_LIST" --argjson skip "$skip_verify" \
    '{enabled: true, type: $type, url: $url, username: $user, password: $pass, no_proxy: $no_proxy, timeout: 30, skip_tls_verify: $skip, enable_for_scim: false, enable_for_inference: true, enable_for_api: true}')"; then
    echo "❌ could not build the global proxy payload" >&2
    return 1
  fi
  if ! api_curl -X PUT -H "Content-Type: application/json" --data "$payload" "$BASE_URL/api/proxy-config" >/dev/null; then
    echo "❌ PUT /api/proxy-config failed" >&2
    return 1
  fi
  local stored
  if ! stored="$(api_curl "$BASE_URL/api/proxy-config")"; then
    echo "❌ GET /api/proxy-config failed" >&2
    return 1
  fi
  if [ "$(jq -r '.enabled and .enable_for_inference' <<<"$stored")" != "true" ]; then
    echo "❌ the global proxy did not stick: $stored" >&2
    return 1
  fi
}

# reset_cell_artifacts deletes the harness's shared outputs before a cell runs. They live
# in tmp/ for every cell, so a cell whose harness dies before newman writes would
# otherwise read the previous cell's report: the retry guard would re-run that cell's
# failed items and skip the "infrastructure, not a flaky row" check, and the old
# reports would be filed under this cell.
reset_cell_artifacts() {
  rm -f "$REPO_ROOT/tmp/newman-report.json" "$REPO_ROOT/tmp/harness-failures.md" "$REPO_ROOT/tmp/newman-cli.log" \
    "$REPO_ROOT"/tmp/newman-report-*.json "$REPO_ROOT"/tmp/newman-cli-*.log
}

item_failure_count() {
  local report="$REPO_ROOT/tmp/newman-report.json"
  [ -f "$report" ] || { echo 0; return; }
  jq -r '((.run.stats.assertions.failed // 0) + (.run.stats.requests.failed // 0))' "$report" 2>/dev/null || echo 0
}

run_harness() {
  local rerun="$1"
  make run-provider-harness-test \
    CI=1 \
    USE_INFISICAL=0 \
    PARALLEL=0 \
    SMOKE="$MANIFEST" \
    BASE_URL="$BASE_URL" \
    APP_DIR="$APP_DIR_REL" \
    ${rerun:+RERUN_FAILED=1}
}

# assert_cell_routes <mode> <kind> requires every manifest host in the cell proxy's log,
# and none on the infra proxy when the cell is not the environment.
assert_cell_routes() {
  local mode="$1" kind="$2" seen leaked="" missing="" key host
  case "$kind" in
    http) seen="$(proxy_hosts_on_port "$PROXY_HTTP_PORT")" ;;
    https) seen="$(proxy_hosts_on_port "$PROXY_TLS_BACKEND")" ;;
    socks5) seen="$(proxy_socks_hosts)" ;;
  esac
  if [ "$kind" = "https" ] && [ "$(proxy_tls_sessions)" -eq 0 ]; then
    echo "❌ no TLS session reached stunnel: the https:// proxy was never used" >&2
    return 1
  fi
  local infra=""
  [ "$mode" != "env" ] && infra="$(proxy_hosts_on_port "$PROXY_INFRA_PORT")"
  while IFS=$'\t' read -r key host; do
    if ! grep -Eq "($host):[0-9]+$" <<<"$seen"; then
      missing="$missing $key($host)"
    fi
    if [ -n "$infra" ] && grep -Eq "($host):[0-9]+$" <<<"$infra"; then
      leaked="$leaked $key($host)"
    fi
  done < <(jq -r '.pillars[] | .key as $k | .hosts[] | [$k, .] | @tsv' "$MANIFEST")
  echo "   cell proxy saw: $(sort -u <<<"$seen" | tr '\n' ' ')"
  if [ -n "$missing" ]; then
    echo "❌ provider traffic missing from the cell's proxy:$missing" >&2
  fi
  if [ -n "$leaked" ]; then
    echo "❌ provider traffic went to the environment proxy instead of the cell's:$leaked" >&2
  fi
  [ -z "$missing" ] && [ -z "$leaked" ]
}

# Bash 3.2 (macOS /bin/bash) has no associative arrays: one variable per cell.
set_cell_result() { printf -v "CELL_RESULT_${1//-/_}" '%s' "$2"; }
get_cell_result() { local v="CELL_RESULT_${1//-/_}"; printf '%s' "${!v:-not run}"; }
# collect_cell_artifacts copies the cell's newman reports and proxy logs into its
# directory, whether the cell ran or failed during setup.
collect_cell_artifacts() {
  local cell_dir="$1"
  for artifact in newman-report.json harness-failures.md newman-cli.log; do
    cp "$REPO_ROOT/tmp/$artifact" "$cell_dir/" 2>/dev/null || true
  done
  cp "$PROXY_DIR/squid-access.log" "$PROXY_DIR/microsocks.log" "$PROXY_DIR/stunnel.log" "$cell_dir/" 2>/dev/null || true
}

# run_cell runs one cell and records its result. A setup failure (the gateway never
# comes up, or the global proxy cannot be configured) is recorded as that cell's
# failure with its artifacts kept, and the matrix goes on to the next cell.
run_cell() {
  local cell="$1"
  mode="${cell%%-*}"
  kind="${cell#*-}"
  cell_dir="$WORK_DIR/cells/$cell"
  mkdir -p "$cell_dir"
  echo ""
  echo "════════ cell $cell ════════"

  # Start from empty proxy logs, so a cell that fails setup does not file the previous
  # cell's traffic under its own name. The reset after setup still drops startup traffic.
  proxy_reset_logs
  seed_cell "$mode" "$kind"
  export BIFROST_LOGS_DB_URL="sqlite://$APP_DIR/logs.db"
  local setup_failure=""
  if ! harness_start_gateway "$APP_DIR" "$PORT" "$cell_dir/gateway.log"; then
    setup_failure="gateway start"
  elif [ "$mode" = "global" ] && ! enable_global_proxy "$kind"; then
    setup_failure="global proxy setup"
  fi
  if [ -n "$setup_failure" ]; then
    harness_stop_gateway || true
    collect_cell_artifacts "$cell_dir"
    set_cell_result "$cell" "fail ($setup_failure)"
    OVERALL=1
    echo "❌ $cell: $(get_cell_result "$cell")"
    tail -n 40 "$cell_dir/gateway.log" 2>/dev/null || true
    return 0
  fi
  proxy_reset_logs
  reset_cell_artifacts

  cell_exit=0
  run_harness "" || cell_exit=$?
  attempt=1
  while [ "$cell_exit" -ne 0 ] && [ "$attempt" -le "$RERUN_ATTEMPTS" ]; do
    if [ "$(item_failure_count)" -eq 0 ]; then
      echo "❌ harness exited $cell_exit with no item failures: infrastructure, not a flaky row. Not retrying."
      break
    fi
    echo "🔁 $cell: re-running only the failed items (attempt $attempt of $RERUN_ATTEMPTS)..."
    cell_exit=0
    run_harness 1 || cell_exit=$?
    attempt=$((attempt + 1))
  done

  routes_ok=1
  assert_cell_routes "$mode" "$kind" || routes_ok=0
  harness_stop_gateway
  collect_cell_artifacts "$cell_dir"

  if [ "$cell_exit" -eq 0 ] && [ "$routes_ok" = "1" ]; then
    set_cell_result "$cell" "pass"
    echo "✅ $cell"
  else
    set_cell_result "$cell" "fail (harness exit $cell_exit, routes $([ "$routes_ok" = 1 ] && echo ok || echo wrong))"
    OVERALL=1
    echo "❌ $cell: $(get_cell_result "$cell")"
    tail -n 40 "$cell_dir/gateway.log" || true
  fi
}

OVERALL=0
for cell in $PROXY_CELLS; do
  run_cell "$cell"
done

summary="$(
  echo "## Proxy e2e"
  echo ""
  echo "| Cell | Result |"
  echo "|---|---|"
  for cell in $PROXY_CELLS; do
    echo "| $cell | $(get_cell_result "$cell") |"
  done
  echo ""
  echo "Per-cell reports, gateway logs and proxy logs: \`$WORK_DIR_REL/cells/<cell>/\`."
)"
echo ""
echo "$summary"
if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
  echo "$summary" >> "$GITHUB_STEP_SUMMARY"
fi
exit "$OVERALL"
