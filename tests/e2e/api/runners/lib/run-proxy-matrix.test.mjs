// Unit tests for tests/e2e/api/runners/proxy/run-proxy-matrix.sh.
// Run directly: `node run-proxy-matrix.test.mjs`. No proxies, no gateway, no network: the
// script's pure shell helpers are extracted and run on their own.
import assert from "node:assert";
import { execFileSync } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const REPO_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../../../../..");
const SCRIPT = path.join(REPO_ROOT, "tests/e2e/api/runners/proxy/run-proxy-matrix.sh");
const source = readFileSync(SCRIPT, "utf8");

let passed = 0;
async function test(name, fn) {
  await fn();
  passed++;
  console.log(`  ok - ${name}`);
}

// The script documents local macOS runs (brew hints, the global-https cell's macOS
// branch), and macOS ships Bash 3.2 as /bin/bash, which `#!/usr/bin/env bash` resolves
// to by default. Bash 4-only syntax would stop the script under `set -e` after the
// gateway is built and the proxies are up, with no cell run.
await test("run-proxy-matrix.sh uses no Bash 4-only syntax", () => {
  const bash4Only = [
    { re: /^\s*declare\s+-[a-zA-Z]*A/m, what: "declare -A (associative arrays)" },
    { re: /^\s*(mapfile|readarray)\b/m, what: "mapfile/readarray" },
    { re: /\$\{[A-Za-z_][A-Za-z0-9_]*(,,|\^\^)\}/m, what: "${var,,} / ${var^^} case conversion" },
    { re: /&>>/m, what: "&>> append redirection" },
  ];
  for (const { re, what } of bash4Only) {
    assert.ok(!re.test(source), `${what} needs Bash 4, which macOS /bin/bash (3.2) lacks`);
  }
});

// Cell results are kept in one plain variable per cell, so the summary table and the
// per-cell failure line read the value that was set for that cell, and a cell that never
// ran reports as such.
await test("cell results round-trip through set_cell_result / get_cell_result", () => {
  const defs = source
    .split("\n")
    .filter((line) => /^(set|get)_cell_result\(\)/.test(line))
    .join("\n");
  const script = `${defs}
set_cell_result global-https pass
set_cell_result env-socks5 'fail (harness exit 1, routes ok)'
get_cell_result global-https; echo
get_cell_result env-socks5; echo
get_cell_result env-http; echo`;
  const out = execFileSync("bash", ["-c", script], { encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] });
  assert.deepStrictEqual(out.split("\n"), ["pass", "fail (harness exit 1, routes ok)", "not run", ""]);
});

// With PROXY_E2E_STRICT_EGRESS=1 the script installs an IPv4 and an IPv6 REJECT rule
// on the host. If the second insert fails, set -e exits; the EXIT trap must still remove
// the IPv4 rule that did go in, and must not try to remove the IPv6 one that did not.
await test("strict-egress cleanup removes every rule that was installed, even after a failed insert", () => {
  const cleanup = source.match(/^cleanup\(\) \{[\s\S]*?\n\}/m);
  assert.ok(cleanup, "cleanup() not found in run-proxy-matrix.sh");
  const lines = source.split("\n");
  const start = lines.findIndex((l) => /^\s*sudo iptables -I OUTPUT/.test(l));
  const end = lines.findIndex((l, i) => i > start && /# Prove the rule bites/.test(l));
  assert.ok(start >= 0 && end > start, "strict-egress insert block not found");
  const inserts = lines.slice(start, end).join("\n");
  const preamble = lines.slice(0, lines.findIndex((l) => /^cleanup\(\) \{/.test(l))).filter((l) => /^[A-Z_]+=/.test(l)).join("\n");
  const script = `set -euo pipefail
LOG="$1"
EGRESS_GROUP=bifrost-egress
harness_stop_gateway() { :; }
proxy_stop() { :; }
sudo() { echo "$*" >> "$LOG"; case "$*" in "ip6tables -I"*) return 1 ;; esac; }
${preamble}
${cleanup[0]}
trap cleanup EXIT
${inserts}
`;
  const dir = mkdtempSync(path.join(tmpdir(), "strict-egress-"));
  const log = path.join(dir, "sudo.log");
  writeFileSync(log, "");
  try {
    execFileSync("bash", ["-c", script, "bash", log], { stdio: ["ignore", "pipe", "pipe"] });
    assert.fail("the failed ip6tables insert must stop the script");
  } catch (err) {
    if (err instanceof assert.AssertionError) throw err;
  }
  const ran = readFileSync(log, "utf8").trim().split("\n");
  assert.ok(ran.some((c) => c.startsWith("iptables -D OUTPUT")), `the installed IPv4 rule was left on the host; sudo ran: ${JSON.stringify(ran)}`);
  assert.ok(!ran.some((c) => c.startsWith("ip6tables -D OUTPUT")), `cleanup tried to remove an IPv6 rule that was never installed; sudo ran: ${JSON.stringify(ran)}`);
  rmSync(dir, { recursive: true, force: true });
});

// shellFunction returns the source of a top-level bash function in the script, or "".
function shellFunction(name) {
  const m = source.match(new RegExp(`^${name}\\(\\) \\{[\\s\\S]*?\\n\\}`, "m"));
  return m ? m[0] : "";
}

// A setup failure in one cell (the gateway never comes up, or the global proxy cannot
// be configured) must be recorded for that cell, keep its artifacts, and let the matrix
// go on to the next cell. Under set -e an unguarded failure would end the whole run
// before any result or the summary was written.
await test("a cell whose gateway fails to start is recorded and the matrix continues", () => {
  const loopStart = source.indexOf("\nOVERALL=0\n");
  const loopEnd = source.indexOf("\ndone\n", loopStart);
  assert.ok(loopStart >= 0 && loopEnd > loopStart, "cell loop not found");
  const loop = source.slice(loopStart, loopEnd + "\ndone\n".length);
  const helpers = ["set_cell_result", "get_cell_result", "run_cell", "collect_cell_artifacts"].map(shellFunction).join("\n");
  const dir = mkdtempSync(path.join(tmpdir(), "proxy-matrix-cells-"));
  const script = `set -euo pipefail
WORK_DIR="$1"; APP_DIR="$1/app"; REPO_ROOT="$1"; PROXY_DIR="$1/proxies"; PORT=0; RERUN_ATTEMPTS=0
mkdir -p "$REPO_ROOT/tmp" "$PROXY_DIR"
PROXY_CELLS="env-http global-https"
seed_cell() { :; }
harness_start_gateway() { if [ "$mode" = env ]; then echo "gateway never became healthy" > "$3"; return 1; fi; : > "$3"; }
enable_global_proxy() { :; }
proxy_reset_logs() { :; }
reset_cell_artifacts() { :; }
run_harness() { :; }
item_failure_count() { echo 0; }
assert_cell_routes() { :; }
harness_stop_gateway() { :; }
${helpers}
${loop}
echo "env-http=$(get_cell_result env-http)"
echo "global-https=$(get_cell_result global-https)"
echo "OVERALL=$OVERALL"
`;
  let out = "";
  try {
    out = execFileSync("bash", ["-c", script, "bash", dir], { encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] });
  } catch (err) {
    assert.fail(`the matrix stopped at the failed cell instead of recording it: exit ${err.status}, stdout: ${err.stdout}`);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
  assert.match(out, /^env-http=fail \(gateway start\)$/m, out);
  assert.match(out, /^global-https=pass$/m, out);
  assert.match(out, /^OVERALL=1$/m, out);
});

// A cell that fails setup keeps the proxy logs in its artifacts, so they must be its own:
// the logs are reset when the cell starts, not only after setup succeeds.
await test("a cell that fails setup does not carry the previous cell's proxy logs", () => {
  const run = shellFunction("run_cell");
  assert.ok(run, "run_cell() not found");
  const helpers = ["set_cell_result", "get_cell_result", "collect_cell_artifacts"].map(shellFunction).join("\n");
  const dir = mkdtempSync(path.join(tmpdir(), "proxy-matrix-logs-"));
  const script = `set -euo pipefail
WORK_DIR="$1"; APP_DIR="$1/app"; REPO_ROOT="$1"; PROXY_DIR="$1/proxies"; PORT=0; RERUN_ATTEMPTS=0; OVERALL=0
mkdir -p "$REPO_ROOT/tmp" "$PROXY_DIR"
echo "previous cell traffic" > "$PROXY_DIR/squid-access.log"
proxy_reset_logs() { : > "$PROXY_DIR/squid-access.log"; }
seed_cell() { :; }
harness_start_gateway() { : > "$3"; return 1; }
enable_global_proxy() { :; }
reset_cell_artifacts() { :; }
run_harness() { :; }
item_failure_count() { echo 0; }
assert_cell_routes() { :; }
harness_stop_gateway() { :; }
${helpers}
${run}
run_cell env-http
cat "$WORK_DIR/cells/env-http/squid-access.log"
`;
  let out;
  try {
    out = execFileSync("bash", ["-c", script, "bash", dir], { encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] });
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
  assert.ok(!out.includes("previous cell traffic"), `the failed cell's artifacts carry the previous cell's proxy log: ${JSON.stringify(out)}`);
});

// enable_global_proxy runs inside "if !" in the cell loop, where bash turns errexit off
// for the whole function, so it must check each API call itself.
await test("enable_global_proxy fails when the proxy-config PUT fails, even with errexit off", () => {
  const fn = shellFunction("enable_global_proxy");
  assert.ok(fn, "enable_global_proxy() not found");
  const script = `set -euo pipefail
BASE_URL=http://127.0.0.1:1; PROXY_USER=u; PROXY_PASS=p; NO_PROXY_LIST=""
cell_proxy_url() { echo "http://127.0.0.1:3128"; }
api_curl() { case "$*" in *"-X PUT"*) return 22 ;; esac; echo '{"enabled":true,"enable_for_inference":true}'; }
${fn}
if ! enable_global_proxy http; then echo refused; else echo accepted; fi
`;
  const out = execFileSync("bash", ["-c", script], { encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] });
  assert.match(out, /^refused$/m, `a failed PUT was treated as a configured proxy: ${out}`);
});

console.log(`\n${passed} passed`);
