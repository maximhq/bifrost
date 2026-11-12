#!/usr/bin/env bash
# Proves that a Heimdall replay runs no real tool, using evidence from outside Heimdall.
#
# It starts mcpchaos, an MCP server that counts every tool call it receives, and runs
# TestHermeticProof in two phases, reading the server's own counters between them:
#
#   control  a client that CAN execute tools runs an MCP tool and an injected tool.
#            The counters must move, which shows they can see a tool run.
#   replay   the replay engine replays a recorded session against a model that calls the
#            recorded tool, an unrecorded destructive tool and an injected tool.
#            The server must see no tool call and no MCP traffic at all.
#
# Covers S1 (recorded destructive call returns the recording), S4 (injected tools do not
# fire) and S8 (every replay call reached the provider). Run it from anywhere; it needs
# go and python3 on PATH.
#
#   MCPCHAOS_PORT   port for the fleet (default 8411)
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
port="${MCPCHAOS_PORT:-8411}"
base="http://127.0.0.1:${port}"
work="$(mktemp -d)"
pid=""

cleanup() {
  [ -n "$pid" ] && kill "$pid" 2>/dev/null || true
  rm -rf "$work"
}
trap cleanup EXIT

command -v go >/dev/null || { echo "go is not on PATH" >&2; exit 2; }
command -v python3 >/dev/null || { echo "python3 is not on PATH" >&2; exit 2; }
if curl -sf -m 2 "${base}/control" >/dev/null 2>&1; then
  echo "something already answers on ${base}; set MCPCHAOS_PORT" >&2
  exit 2
fi

echo "building mcpchaos"
(cd "${root}/tests/cmd/mcpconsole" && go build -o "${work}/mcpchaos" ./cmd/mcpchaos)
"${work}/mcpchaos" -addr "127.0.0.1:${port}" -servers shop -tools lookup_order,issue_refund >"${work}/mcpchaos.log" 2>&1 &
pid=$!
for _ in $(seq 1 50); do
  curl -sf -m 2 "${base}/control" >/dev/null 2>&1 && break
  sleep 0.2
done
curl -sf -m 2 "${base}/control" >/dev/null || { echo "mcpchaos did not start:" >&2; cat "${work}/mcpchaos.log" >&2; exit 2; }

# counter NAME prints one field of the shop server's counters; "calls" prints the per-tool tally as JSON.
counter() {
  curl -sf "${base}/control" | python3 -I -c "
import json, sys
c = json.load(sys.stdin)['servers']['shop']['counters']
v = c['$1']
print(json.dumps(v, sort_keys=True) if isinstance(v, dict) else v)"
}
reset() { curl -sf -X POST "${base}/control/shop/counters/reset" >/dev/null; }
phase() { (cd "${root}/plugins/heimdall" && HERMETIC_PROOF_URL="${base}" HERMETIC_PHASE="$1" go test -count=1 -run '^TestHermeticProof$' -v ./... 2>&1 | grep -v 'sqlite\|^ *[0-9]* *|\|\^' ); }
fail() { echo "FAIL: $*" >&2; exit 1; }

echo "== control: a client that can run tools"
reset
control_out="$(phase control || true)"
echo "${control_out}" | grep -E '^(--- |ok|FAIL|panic|    )' || true
calls="$(counter calls)"
echo "   mcpchaos tool calls: ${calls}"
[ "${calls}" = '{"lookup_order": 2}' ] || [ "${calls}" = '{"lookup_order":2}' ] || fail "the control run should have made 2 lookup_order calls, saw ${calls}. The counters cannot be trusted."

echo "== replay: the replay engine"
reset
out="$(phase replay || true)"
echo "${out}" | grep -E '^(--- |ok|FAIL|panic|    )' || true
calls="$(counter calls)"
requests="$(counter requests)"
initializes="$(counter initializes)"
echo "   mcpchaos tool calls: ${calls}, requests: ${requests}, initializes: ${initializes}"
[ "${calls}" = '{}' ] || fail "a tool ran during the replay: ${calls}"
echo "${out}" | grep -q '^ok' || fail "the replay phase did not pass"
[ "${requests}" = "0" ] || fail "the MCP server saw ${requests} requests during the replay"
[ "${initializes}" = "0" ] || fail "the replay client opened ${initializes} MCP sessions"

echo
echo "PASS: the control run executed tools (2 calls); the replay executed none and never touched the MCP server."
