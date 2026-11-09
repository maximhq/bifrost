#!/usr/bin/env bash
set -euo pipefail

# Execute the HEALTHCHECK command of each image variant with stubbed probes and
# check which port it hits: the entrypoint's recorded port when present,
# otherwise PORT, otherwise APP_PORT.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
mkdir -p "$WORK/bin"
for probe in wget curl-minimal; do
  cat > "$WORK/bin/$probe" <<'STUB'
#!/usr/bin/env bash
for a in "$@"; do case "$a" in http://*) printf '%s\n' "$a" >> "$PROBED" ;; esac; done
STUB
  chmod +x "$WORK/bin/$probe"
done

FAILED=0
fail() { echo "FAIL: $*" >&2; FAILED=1; }

# healthcheck_cmd <Dockerfile>: the shell command after "CMD" on the line following HEALTHCHECK.
healthcheck_cmd() {
  awk '/HEALTHCHECK/ {getline; sub(/^[ \t]*CMD[ \t]+/, ""); print; exit}' "$1"
}

# run <dockerfile> <case> [VAR=value ...]: prints the probed URL.
run() {
  local df=$1 name=$2; shift 2
  local cmd; cmd="$(healthcheck_cmd "$df")"
  [[ -n "$cmd" ]] || { fail "$df: no HEALTHCHECK CMD found"; return; }
  : > "$WORK/probed-$name"
  env -u PORT -u APP_PORT PROBED="$WORK/probed-$name" PATH="$WORK/bin:$PATH" BIFROST_PORT_FILE="$WORK/portfile-$name" "$@" sh -c "$cmd" >/dev/null 2>&1 || true
  cat "$WORK/probed-$name"
}

for df in "$SCRIPT_DIR/Dockerfile" "$SCRIPT_DIR/Dockerfile.local" "$SCRIPT_DIR/Dockerfile.redhat"; do
  short="${df##*/}"
  rm -f "$WORK"/portfile-*
  [[ "$(run "$df" a APP_PORT=8080)" == "http://127.0.0.1:8080/health" ]] || fail "$short: APP_PORT alone must be probed, got '$(cat "$WORK/probed-a")'"
  [[ "$(run "$df" b APP_PORT=8080 PORT=)" == "http://127.0.0.1:8080/health" ]] || fail "$short: an empty PORT must fall back to APP_PORT, got '$(cat "$WORK/probed-b")'"
  [[ "$(run "$df" c APP_PORT=8080 PORT=10000)" == "http://127.0.0.1:10000/health" ]] || fail "$short: PORT must win over APP_PORT, got '$(cat "$WORK/probed-c")'"
  printf '7000' > "$WORK/portfile-d"
  [[ "$(run "$df" d APP_PORT=8080 PORT=10000)" == "http://127.0.0.1:7000/health" ]] || fail "$short: the recorded port (from --port) must win over PORT, got '$(cat "$WORK/probed-d")'"
done

if [[ $FAILED -ne 0 ]]; then
  exit 1
fi
echo "PASS: image HEALTHCHECK probes the effective port"
