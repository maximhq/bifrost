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
  const preamble = lines.slice(0, lines.findIndex((l) => /^cleanup\(\) \{/.test(l))).filter((l) => /^[A-Z0-9_]+=/.test(l)).join("\n");
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
  let failure;
  try {
    execFileSync("bash", ["-c", script, "bash", log], { stdio: ["ignore", "pipe", "pipe"] });
    assert.fail("the failed ip6tables insert must stop the script");
  } catch (err) {
    if (err instanceof assert.AssertionError) throw err;
    failure = err;
  }
  // cleanup must run to its end and exit with the failed insert's status, not die on an
  // unset flag partway through.
  assert.doesNotMatch(String(failure.stderr), /STRICT_RULE_V[46]_ADDED: unbound variable/, `cleanup did not finish: ${failure.stderr}`);
  assert.strictEqual(failure.status, 1, `cleanup exited ${failure.status}, not the failed insert's status: ${failure.stderr}`);
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
  const helpers = ["set_cell_result", "get_cell_result", "run_cell", "collect_cell_artifacts", "await_cell_routes"].map(shellFunction).join("\n");
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
cell_proxy_hosts() { :; }
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
cell_proxy_hosts() { :; }
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

// Every pillar runs in every cell, so a credential the pillar's provider reads from the
// environment and the release job does not pass fails the release gate on auth, not egress.
await test("the test-proxy-e2e job passes every env credential the proxy pillars read", () => {
  const manifest = JSON.parse(readFileSync(path.join(REPO_ROOT, "tests/e2e/api/collections/proxy-smoke-manifest.json"), "utf8"));
  const config = JSON.parse(readFileSync(path.join(REPO_ROOT, "tests/integrations/python/config.json"), "utf8"));
  const workflow = readFileSync(path.join(REPO_ROOT, ".github/workflows/release-pipeline.yml"), "utf8");
  const job = workflow.match(/^  test-proxy-e2e:\n[\s\S]*?(?=^  [A-Za-z0-9_-]+:\n)/m);
  assert.ok(job, "test-proxy-e2e job not found in release-pipeline.yml");
  const passedEnv = new Set([...job[0].matchAll(/^\s+([A-Z0-9_]+):\s*\$\{\{/gm)].map((m) => m[1]));
  const envRefs = (node, out) => {
    if (typeof node === "string") {
      const ref = node.match(/^env\.([A-Z0-9_]+)$/);
      if (ref) out.add(ref[1]);
    } else if (node && typeof node === "object") {
      for (const value of Object.values(node)) envRefs(value, out);
    }
    return out;
  };
  const missing = [];
  for (const pillar of manifest.pillars) {
    const provider = config.providers[pillar.key];
    if (!provider) continue;
    for (const name of envRefs(provider, new Set())) {
      if (!passedEnv.has(name)) missing.push(`${pillar.key}: ${name}`);
    }
  }
  assert.deepStrictEqual(missing, [], `test-proxy-e2e does not pass: ${missing.join(", ")}`);
});

// The fresh gateway boots the python profile, which sets setup_token and has no admin
// account, so SetupLockMiddleware answers every /api call without the token with 401.
await test("api_curl sends the setup token on management calls", () => {
  const fn = shellFunction("api_curl");
  assert.ok(fn, "api_curl() not found");
  const script = `set -euo pipefail
curl() { printf '%s\\n' "$@"; }
${fn}
api_curl http://127.0.0.1:1/api/proxy-config
`;
  const out = execFileSync("bash", ["-c", script], { encoding: "utf8", stdio: ["ignore", "pipe", "pipe"], env: { ...process.env, BIFROST_SETUP_TOKEN: "" } });
  assert.match(out, /^X-Bifrost-Setup-Token: bifrost-e2e-setup-token$/m, `api_curl sent no setup token: ${out}`);
});

// Squid writes a CONNECT record when the tunnel closes, and the gateway keeps idle
// provider connections open, so the routes can only be read once the gateway is gone
// and squid has written the records of the tunnels it closed.
await test("run_cell stops the gateway before reading the proxy logs, and waits for late records", () => {
  const run = shellFunction("run_cell");
  assert.ok(run, "run_cell() not found");
  const helpers = ["set_cell_result", "get_cell_result", "collect_cell_artifacts", "await_cell_routes"].map(shellFunction).join("\n");
  const dir = mkdtempSync(path.join(tmpdir(), "proxy-matrix-order-"));
  const script = `set -euo pipefail
WORK_DIR="$1"; APP_DIR="$1/app"; REPO_ROOT="$1"; PROXY_DIR="$1/proxies"; PORT=0; RERUN_ATTEMPTS=0; OVERALL=0
mkdir -p "$REPO_ROOT/tmp" "$PROXY_DIR"
calls="$1/calls"; : > "$calls"
proxy_reset_logs() { :; }
seed_cell() { :; }
harness_start_gateway() { : > "$3"; }
enable_global_proxy() { :; }
reset_cell_artifacts() { :; }
run_harness() { :; }
item_failure_count() { echo 0; }
sleep() { :; }
harness_stop_gateway() { echo stop >> "$calls"; }
cell_proxy_hosts() { :; }
assert_cell_routes() {
  echo assert >> "$calls"
  grep -q stop "$calls" || return 1
  [ "$(grep -c assert "$calls")" -ge 3 ]
}
${helpers}
${run}
run_cell env-http
echo "result=$(get_cell_result env-http)"
echo "calls=$(tr '\\n' ' ' < "$calls")"
`;
  let out;
  try {
    out = execFileSync("bash", ["-c", script, "bash", dir], { encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] });
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
  assert.match(out, /^calls=stop assert/m, `the routes were read before the gateway stopped: ${out}`);
  assert.match(out, /^result=pass$/m, `a record written after the first read failed the cell: ${out}`);
});

// workflow-lint runs check-egress-allowlist.sh, which requires every host the harness
// sources name to be allowlisted (or exempted) in each job that runs the harness.
await test("the test-proxy-e2e job passes the egress allowlist check", () => {
  let stderr = "";
  try {
    execFileSync("bash", [path.join(REPO_ROOT, ".github/workflows/scripts/check-egress-allowlist.sh"), path.join(REPO_ROOT, ".github/workflows/release-pipeline.yml")], { encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] });
  } catch (err) {
    stderr = String(err.stderr);
  }
  const line = stderr.split("\n").find((l) => l.includes("job 'test-proxy-e2e'"));
  assert.ok(!line, line);
});

// The manifest's hosts are regular expressions (api\.openai\.com), read into bash and
// matched with grep -E, so they must reach grep unescaped: one concrete host per pattern
// in the proxy's log must satisfy every pillar.
await test("assert_cell_routes matches the manifest's host patterns against the proxy log", () => {
  const fn = ["cell_proxy_hosts", "assert_cell_routes"].map(shellFunction).join("\n");
  assert.ok(shellFunction("assert_cell_routes"), "assert_cell_routes() not found");
  const manifest = JSON.parse(readFileSync(path.join(REPO_ROOT, "tests/e2e/api/collections/proxy-smoke-manifest.json"), "utf8"));
  const concrete = (pattern) =>
    pattern.replace(/^\\\./, "gw.").replace(/\(([^|)]+)\|[^)]*\)/g, "$1").replace(/\[a-z0-9-\]\+/g, "us-east-1").replace(/\\\./g, ".");
  const log = [...new Set(manifest.pillars.flatMap((p) => [...p.hosts, ...(p.token_hosts || [])].map(concrete)))].map((h) => `${h}:443`).join("\n");
  const dir = mkdtempSync(path.join(tmpdir(), "proxy-matrix-routes-"));
  writeFileSync(path.join(dir, "seen"), log + "\n");
  const script = `set -euo pipefail
MANIFEST="${path.join(REPO_ROOT, "tests/e2e/api/collections/proxy-smoke-manifest.json")}"
SEEN="$1/seen"; PROXY_HTTP_PORT=3128; PROXY_INFRA_PORT=3129
proxy_hosts_on_port() { if [ "$1" = 3128 ]; then cat "$SEEN"; fi; }
proxy_hosts_on_port_since() { :; }
${fn}
assert_cell_routes global http
`;
  let result = { status: 0, stderr: "" };
  try {
    execFileSync("bash", ["-c", script, "bash", dir], { encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] });
  } catch (err) {
    result = { status: err.status, stderr: String(err.stderr) };
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
  assert.strictEqual(result.status, 0, `every manifest host is in the proxy log, yet the routes failed: ${result.stderr}`);
});

// The cells boot the python profile, whose vector store is a Weaviate on localhost:9000.
// The job starts no Weaviate and the gateway exits when it cannot reach one, so every
// cell must boot with the vector store off: no proxy pillar uses it.
await test("seed_cell turns off the profile's vector store in every mode", () => {
  const fn = shellFunction("seed_cell");
  assert.ok(fn, "seed_cell() not found");
  const profile = path.join(REPO_ROOT, "tests/integrations/python/config.json");
  assert.strictEqual(JSON.parse(readFileSync(profile, "utf8")).vector_store?.enabled, true, "precondition: the profile enables a vector store");
  for (const mode of ["global", "provider", "env"]) {
    const dir = mkdtempSync(path.join(tmpdir(), "proxy-matrix-seed-"));
    const script = `set -euo pipefail
APP_DIR="$1/app"; PROXY_DIR="$1"; NO_PROXY_LIST=localhost; INFRA_PROXY=http://127.0.0.1:3129; CA_BUNDLE=""
PROXY_USER=u; PROXY_PASS=p
harness_seed_app_dir() { mkdir -p "$1"; cp "${profile}" "$1/config.json"; }
cell_proxy_url() { echo "http://127.0.0.1:3128"; }
${fn}
seed_cell ${mode} http
jq -c '.vector_store.enabled' "$APP_DIR/config.json"
`;
    let out;
    try {
      out = execFileSync("bash", ["-c", script, "bash", dir], { encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] });
    } finally {
      rmSync(dir, { recursive: true, force: true });
    }
    assert.strictEqual(out.trim(), "false", `${mode} cell boots with the vector store on`);
  }
});

// A cell that fails setup keeps the newman outputs in its artifacts, so they must be its
// own: the harness outputs are cleared when the cell starts, not only after setup.
await test("a cell that fails setup does not carry the previous cell's newman reports", () => {
  const run = shellFunction("run_cell");
  const helpers = ["set_cell_result", "get_cell_result", "collect_cell_artifacts", "reset_cell_artifacts"].map(shellFunction).join("\n");
  const dir = mkdtempSync(path.join(tmpdir(), "proxy-matrix-reports-"));
  const script = `set -euo pipefail
WORK_DIR="$1"; APP_DIR="$1/app"; REPO_ROOT="$1"; PROXY_DIR="$1/proxies"; PORT=0; RERUN_ATTEMPTS=0; OVERALL=0
mkdir -p "$REPO_ROOT/tmp" "$PROXY_DIR"
echo "previous cell report" > "$REPO_ROOT/tmp/newman-report.json"
proxy_reset_logs() { :; }
seed_cell() { :; }
harness_start_gateway() { : > "$3"; return 1; }
enable_global_proxy() { :; }
run_harness() { :; }
item_failure_count() { echo 0; }
assert_cell_routes() { :; }
cell_proxy_hosts() { :; }
harness_stop_gateway() { :; }
${helpers}
${run}
run_cell env-http
cat "$WORK_DIR/cells/env-http/newman-report.json" 2>/dev/null || true
`;
  let out;
  try {
    out = execFileSync("bash", ["-c", script, "bash", dir], { encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] });
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
  assert.ok(!out.includes("previous cell report"), `the failed cell's artifacts carry the previous cell's newman report: ${JSON.stringify(out)}`);
});

// microsocks logs a connection when it opens, and gateway startup lists Vertex models,
// which exchanges the OAuth token the smoke request then reuses from the provider's
// cache. A token exchange made only at startup is still the provider's traffic through
// the cell's proxy and must satisfy its pillar after the post-setup log reset.
await test("a token exchange made only at gateway startup still counts for its pillar", () => {
  const manifest = JSON.parse(readFileSync(path.join(REPO_ROOT, "tests/e2e/api/collections/proxy-smoke-manifest.json"), "utf8"));
  const concrete = (pattern) =>
    pattern.replace(/^\\\./, "gw.").replace(/\(([^|)]+)\|[^)]*\)/g, "$1").replace(/\[a-z0-9-\]\+/g, "us-east-1").replace(/\\\./g, ".");
  const token = "oauth2.googleapis.com:443";
  const inference = [...new Set(manifest.pillars.flatMap((p) => [...p.hosts, ...(p.token_hosts || [])].map(concrete)))]
    .map((h) => `${h}:443`)
    .filter((h) => h !== token);
  const helpers = ["set_cell_result", "get_cell_result", "run_cell", "collect_cell_artifacts", "await_cell_routes", "assert_cell_routes", "cell_proxy_hosts"]
    .map(shellFunction)
    .join("\n");
  const dir = mkdtempSync(path.join(tmpdir(), "proxy-matrix-startup-"));
  writeFileSync(path.join(dir, "inference"), inference.join("\n") + "\n");
  const script = `set -euo pipefail
WORK_DIR="$1"; APP_DIR="$1/app"; REPO_ROOT="$1"; PROXY_DIR="$1/proxies"; PORT=0; RERUN_ATTEMPTS=0
MANIFEST="${path.join(REPO_ROOT, "tests/e2e/api/collections/proxy-smoke-manifest.json")}"
PROXY_HTTP_PORT=3128; PROXY_INFRA_PORT=3129; PROXY_LOG_SETTLE_SECONDS=1
mkdir -p "$REPO_ROOT/tmp" "$PROXY_DIR"
SOCKS_LOG="$PROXY_DIR/microsocks.log"
seed_cell() { :; }
harness_start_gateway() { echo "${token}" >> "$SOCKS_LOG"; : > "$3"; }
enable_global_proxy() { :; }
proxy_reset_logs() { : > "$SOCKS_LOG"; }
reset_cell_artifacts() { :; }
run_harness() { cat "$WORK_DIR/inference" >> "$SOCKS_LOG"; }
item_failure_count() { echo 0; }
harness_stop_gateway() { :; }
proxy_socks_hosts() { cat "$SOCKS_LOG"; }
proxy_hosts_on_port() { :; }
proxy_hosts_on_port_since() { :; }
proxy_tls_sessions() { echo 1; }
${helpers}
run_cell provider-socks5
echo "result=$(get_cell_result provider-socks5)"
`;
  let out = "";
  try {
    out = execFileSync("bash", ["-c", script, "bash", dir], { encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] });
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
  assert.match(out, /^result=pass$/m, `routing was right but the cell failed: ${out}`);
});

// Startup lists every provider's models, and a smoke request can reuse that pooled
// connection. microsocks logs a connection when it opens, so in a SOCKS5 cell the reused
// tunnel's only record is from startup. squid logs a CONNECT when the tunnel closes, after
// the reset, so an http cell whose API host was seen only at startup still fails.
await test("a provider connection opened at startup and reused counts only in a SOCKS5 cell", () => {
  const manifest = JSON.parse(readFileSync(path.join(REPO_ROOT, "tests/e2e/api/collections/proxy-smoke-manifest.json"), "utf8"));
  const concrete = (pattern) =>
    pattern.replace(/^\\\./, "gw.").replace(/\(([^|)]+)\|[^)]*\)/g, "$1").replace(/\[a-z0-9-\]\+/g, "us-east-1").replace(/\\\./g, ".");
  const reused = "api.openai.com:443";
  const smoke = [...new Set(manifest.pillars.flatMap((p) => [...p.hosts, ...(p.token_hosts || [])].map(concrete)))]
    .map((h) => `${h}:443`)
    .filter((h) => h !== reused);
  const helpers = ["set_cell_result", "get_cell_result", "run_cell", "collect_cell_artifacts", "await_cell_routes", "assert_cell_routes", "cell_proxy_hosts"]
    .map(shellFunction)
    .join("\n");
  const runCell = (cell) => {
    const dir = mkdtempSync(path.join(tmpdir(), "proxy-matrix-reuse-"));
    writeFileSync(path.join(dir, "smoke"), smoke.join("\n") + "\n");
    const script = `set -euo pipefail
WORK_DIR="$1"; APP_DIR="$1/app"; REPO_ROOT="$1"; PROXY_DIR="$1/proxies"; PORT=0; RERUN_ATTEMPTS=0; OVERALL=0
MANIFEST="${path.join(REPO_ROOT, "tests/e2e/api/collections/proxy-smoke-manifest.json")}"
PROXY_HTTP_PORT=3128; PROXY_INFRA_PORT=3129; PROXY_LOG_SETTLE_SECONDS=1
mkdir -p "$REPO_ROOT/tmp" "$PROXY_DIR"
LOG="$PROXY_DIR/cell.log"
seed_cell() { :; }
harness_start_gateway() { echo "${reused}" >> "$LOG"; : > "$3"; }
enable_global_proxy() { :; }
proxy_reset_logs() { : > "$LOG"; }
reset_cell_artifacts() { :; }
run_harness() { cat "$WORK_DIR/smoke" >> "$LOG"; }
item_failure_count() { echo 0; }
harness_stop_gateway() { :; }
proxy_socks_hosts() { cat "$LOG"; }
proxy_hosts_on_port() { [ "$1" = "$PROXY_HTTP_PORT" ] && cat "$LOG" || true; }
proxy_hosts_on_port_since() { :; }
proxy_tls_sessions() { echo 1; }
${helpers}
run_cell ${cell} 2>&1
echo "result=$(get_cell_result ${cell})"
`;
    try {
      return execFileSync("bash", ["-c", script, "bash", dir], { encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] });
    } finally {
      rmSync(dir, { recursive: true, force: true });
    }
  };
  const socks = runCell("provider-socks5");
  assert.match(socks, /^result=pass$/m, `the reused SOCKS5 tunnel was not counted: ${socks}`);
  const http = runCell("provider-http");
  assert.doesNotMatch(http, /^result=pass$/m, `an http cell passed on startup traffic alone: ${http}`);
  assert.match(http, /missing from the cell's proxy:.*openai\(api\\\.openai\\\.com\)/, http);
});

// The profile has no version, so an empty key allowlist denies every model: key selection
// skips the key, and list-models returns nothing after reaching the upstream. Either way
// the pillar fails in every cell although the routing is right.
await test("every key a proxy pillar uses allows models", () => {
  const manifest = JSON.parse(readFileSync(path.join(REPO_ROOT, "tests/e2e/api/collections/proxy-smoke-manifest.json"), "utf8"));
  const config = JSON.parse(readFileSync(path.join(REPO_ROOT, "tests/integrations/python/config.json"), "utf8"));
  const denied = manifest.pillars
    .filter((pillar) => config.providers[pillar.key])
    .flatMap((pillar) => (config.providers[pillar.key].keys || []).filter((key) => !(key.models || []).length).map((key) => `${pillar.key} (${key.name})`));
  assert.deepStrictEqual(denied, [], `profile keys with no models allowlist: ${denied.join(", ")}`);
});

// --skip-core-test drops every job gated on it, and the release log must say which.
await test("the --skip-core-test warning names every job the flag skips", () => {
  const workflow = readFileSync(path.join(REPO_ROOT, ".github/workflows/release-pipeline.yml"), "utf8");
  const jobs = [...workflow.matchAll(/^  ([A-Za-z0-9_-]+):\n[\s\S]*?(?=^  [A-Za-z0-9_-]+:\n|(?![\s\S]))/gm)]
    .filter((m) => /^\s+(?:if:.*|)needs\.check-skip\.outputs\.skip-core-test != 'true'/m.test(m[0]) || /^\s+if:.*skip-core-test != 'true'/m.test(m[0]))
    .map((m) => m[1]);
  assert.ok(jobs.includes("test-proxy-e2e"), `no job gated on skip-core-test found: ${jobs}`);
  const warning = workflow.split("\n").find((l) => l.includes("::warning::--skip-core-test"));
  assert.ok(warning, "--skip-core-test warning not found");
  const unnamed = jobs.filter((j) => !new RegExp(`\\b${j}\\b`).test(warning));
  assert.deepStrictEqual(unnamed, [], `the warning does not name: ${unnamed.join(", ")}`);
});

// The harness builds Go modules that require the transports module's version, so an older
// setup-go version makes every run download a second toolchain. go.work is gitignored, so
// the requirement is read from the tracked go.mod.
await test("the test-proxy-e2e job sets up the Go version transports/go.mod requires", () => {
  const goMod = readFileSync(path.join(REPO_ROOT, "transports/go.mod"), "utf8").match(/^go (\S+)$/m)[1];
  const workflow = readFileSync(path.join(REPO_ROOT, ".github/workflows/release-pipeline.yml"), "utf8");
  const job = workflow.match(/^  test-proxy-e2e:\n[\s\S]*?(?=^  [A-Za-z0-9_-]+:\n)/m);
  assert.ok(job, "test-proxy-e2e job not found in release-pipeline.yml");
  const versions = [...job[0].matchAll(/go-version:\s*"([^"]+)"/g)].map((m) => m[1]);
  assert.ok(versions.length > 0, "test-proxy-e2e has no setup-go step");
  assert.deepStrictEqual(versions, versions.map(() => goMod), `test-proxy-e2e sets up Go ${versions.join(", ")}, transports/go.mod requires ${goMod}`);
});

// The job runs the harness with provider secrets and never uses git after checkout, so the
// checkout must not leave the GITHUB_TOKEN where those steps can read it.
await test("the test-proxy-e2e checkout does not persist git credentials", () => {
  const workflow = readFileSync(path.join(REPO_ROOT, ".github/workflows/release-pipeline.yml"), "utf8");
  const job = workflow.match(/^  test-proxy-e2e:\n[\s\S]*?(?=^  [A-Za-z0-9_-]+:\n)/m);
  assert.ok(job, "test-proxy-e2e job not found in release-pipeline.yml");
  const checkouts = job[0].split(/\n(?=\s+- (?:name|uses):)/).filter((step) => /uses:\s*actions\/checkout@/.test(step));
  assert.ok(checkouts.length > 0, "test-proxy-e2e has no checkout step");
  for (const step of checkouts) {
    assert.match(step, /^\s+persist-credentials:\s*false\s*$/m, `checkout keeps git credentials:\n${step}`);
  }
});

// The first harness run in a cell also runs the stream-cancellation probes, and a retry
// (RERUN_FAILED=1) skips them. A probe failure in the first run must still fail the cell
// when the Newman retry goes green, while a Newman-only failure that a retry fixes passes.
await test("a stream-cancellation failure survives a green Newman retry", () => {
  const helpers = ["set_cell_result", "get_cell_result", "run_cell", "collect_cell_artifacts", "await_cell_routes", "reset_cell_artifacts", "stream_cancel_failed"]
    .map(shellFunction)
    .join("\n");
  const runCell = (probe) => {
    const dir = mkdtempSync(path.join(tmpdir(), "proxy-matrix-cancel-"));
    const script = `set -euo pipefail
WORK_DIR="$1"; APP_DIR="$1/app"; REPO_ROOT="$1"; PROXY_DIR="$1/proxies"; PORT=0; RERUN_ATTEMPTS=1; OVERALL=0
mkdir -p "$REPO_ROOT/tmp" "$PROXY_DIR"
seed_cell() { :; }
harness_start_gateway() { : > "$3"; }
enable_global_proxy() { :; }
proxy_reset_logs() { :; }
cell_proxy_hosts() { :; }
assert_cell_routes() { :; }
item_failure_count() { echo 1; }
harness_stop_gateway() { :; }
run_harness() {
  if [ -z "$1" ]; then
    echo "[stream-cancel] ${probe}" > "$REPO_ROOT/tmp/stream-cancel-cli.log"
    return 2
  fi
  return 0
}
${helpers}
run_cell provider-http
echo "result=$(get_cell_result provider-http)"
`;
    try {
      return execFileSync("bash", ["-c", script, "bash", dir], { encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] });
    } finally {
      rmSync(dir, { recursive: true, force: true });
    }
  };
  const failed = runCell("1/4 stream case(s) failed, 0 cost check(s) failed, 0/1 pool-health probe(s) failed; report: tmp/stream-cancel-report.json");
  assert.doesNotMatch(failed, /^result=pass$/m, `the first run's cancellation failure was lost on retry: ${failed}`);
  const passedProbes = runCell("all 4 case(s) passed (incl. cost checks); report: tmp/stream-cancel-report.json");
  assert.match(passedProbes, /^result=pass$/m, `a Newman-only failure fixed by the retry failed the cell: ${passedProbes}`);
});

// In global cells the gateway starts on the infra proxy (HTTPS_PROXY), and squid logs a
// CONNECT only when its tunnel closes. A startup tunnel that stays idle past the reset
// (Vertex's token exchange) is logged in the smoke window, and must not read as a leak;
// a tunnel the smoke traffic opens on the infra proxy still must.
await test("an infra tunnel opened before the reset is not a leak, one opened after it is", () => {
  const lib = readFileSync(path.join(REPO_ROOT, "tests/e2e/api/runners/proxy/proxy-lib.sh"), "utf8");
  const libFunction = (name) => {
    const m = lib.match(new RegExp(`^${name}\\(\\) \\{[\\s\\S]*?\\n\\}`, "m"));
    return m ? m[0] : "";
  };
  assert.ok(libFunction("proxy_hosts_on_port"), "proxy_hosts_on_port() not found in proxy-lib.sh");
  const manifest = JSON.parse(readFileSync(path.join(REPO_ROOT, "tests/e2e/api/collections/proxy-smoke-manifest.json"), "utf8"));
  const concrete = (pattern) =>
    pattern.replace(/^\\\./, "gw.").replace(/\(([^|)]+)\|[^)]*\)/g, "$1").replace(/\[a-z0-9-\]\+/g, "us-east-1").replace(/\\\./g, ".");
  const smoke = [...new Set(manifest.pillars.flatMap((p) => [...p.hosts, ...(p.token_hosts || [])].map(concrete)))].map((h) => `${h}:443`);
  const helpers = ["set_cell_result", "get_cell_result", "run_cell", "collect_cell_artifacts", "await_cell_routes", "assert_cell_routes", "cell_proxy_hosts"]
    .map(shellFunction)
    .concat(["proxy_hosts_on_port", "proxy_hosts_on_port_since"].map(libFunction))
    .join("\n");
  // durationMs is how long the infra tunnel was open when squid logged it, half a second
  // into the smoke window: a long one opened before the reset, a short one after it.
  const runCell = (durationMs) => {
    const dir = mkdtempSync(path.join(tmpdir(), "proxy-matrix-infra-"));
    writeFileSync(path.join(dir, "smoke"), smoke.join("\n") + "\n");
    const script = `set -euo pipefail
WORK_DIR="$1"; APP_DIR="$1/app"; REPO_ROOT="$1"; PROXY_DIR="$1/proxies"; PORT=0; RERUN_ATTEMPTS=0; OVERALL=0
MANIFEST="${path.join(REPO_ROOT, "tests/e2e/api/collections/proxy-smoke-manifest.json")}"
PROXY_HTTP_PORT=3128; PROXY_INFRA_PORT=3130; PROXY_LOG_SETTLE_SECONDS=1
mkdir -p "$REPO_ROOT/tmp" "$PROXY_DIR"
LOG="$PROXY_DIR/squid-access.log"
seed_cell() { :; }
harness_start_gateway() { : > "$3"; }
enable_global_proxy() { :; }
proxy_reset_logs() { : > "$LOG"; }
reset_cell_artifacts() { :; }
stream_cancel_failed() { return 1; }
run_harness() {
  local end
  end="$(date +%s).500"
  while read -r host; do echo "3128 127.0.0.1 CONNECT $host 200 bifrost $end 10" >> "$LOG"; done < "$WORK_DIR/smoke"
  echo "3130 127.0.0.1 CONNECT oauth2.googleapis.com:443 200 bifrost $end ${durationMs}" >> "$LOG"
}
item_failure_count() { echo 0; }
harness_stop_gateway() { :; }
proxy_tls_sessions() { echo 1; }
${helpers}
run_cell global-http 2>&1
echo "result=$(get_cell_result global-http)"
`;
    try {
      return execFileSync("bash", ["-c", script, "bash", dir], { encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] });
    } finally {
      rmSync(dir, { recursive: true, force: true });
    }
  };
  const startup = runCell(600000);
  assert.match(startup, /^result=pass$/m, `a startup tunnel closed in the smoke window was flagged as a leak: ${startup}`);
  const leak = runCell(10);
  assert.match(leak, /went to the environment proxy instead of the cell's:.*vertex\(oauth2/, `a smoke-time infra tunnel was not flagged: ${leak}`);
});

// A failed cancellation probe fails the cell, the next cell's reset deletes the probe's
// files from tmp/, and CI uploads only the cell directories: they must be copied there.
await test("collect_cell_artifacts keeps the stream-cancellation log and report", () => {
  const dir = mkdtempSync(path.join(tmpdir(), "proxy-matrix-artifacts-"));
  const script = `set -euo pipefail
REPO_ROOT="$1"; PROXY_DIR="$1/proxies"
mkdir -p "$REPO_ROOT/tmp" "$PROXY_DIR" "$1/cell"
for f in newman-report.json harness-failures.md newman-cli.log stream-cancel-cli.log stream-cancel-report.json; do echo "$f" > "$REPO_ROOT/tmp/$f"; done
${shellFunction("collect_cell_artifacts")}
collect_cell_artifacts "$1/cell"
ls "$1/cell"
`;
  let out;
  try {
    out = execFileSync("bash", ["-c", script, "bash", dir], { encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] });
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
  const kept = out.trim().split("\n");
  for (const f of ["stream-cancel-cli.log", "stream-cancel-report.json", "newman-report.json"]) {
    assert.ok(kept.includes(f), `${f} was not copied into the cell directory: ${kept.join(", ")}`);
  }
});

// proxy_reset_logs truncates microsocks.log while microsocks keeps it open. A writer that
// does not append keeps its offset and pads the file with NUL bytes up to it, so the log
// must be opened in append mode. A stand-in writer runs through proxy_start's exact line.
await test("microsocks.log has no NUL padding after proxy_reset_logs", () => {
  const lib = readFileSync(path.join(REPO_ROOT, "tests/e2e/api/runners/proxy/proxy-lib.sh"), "utf8");
  const launch = lib.split("\n").find((l) => /^\s*"\$MICROSOCKS_BIN" /.test(l));
  assert.ok(launch, "microsocks launch line not found in proxy-lib.sh");
  const reset = lib.match(/^proxy_reset_logs\(\) \{[\s\S]*?\n\}/m);
  assert.ok(reset, "proxy_reset_logs() not found in proxy-lib.sh");
  const dir = mkdtempSync(path.join(tmpdir(), "proxy-matrix-socks-log-"));
  const writer = path.join(dir, "fake-microsocks");
  writeFileSync(writer, '#!/usr/bin/env bash\nfor i in $(seq 1 40); do echo "client[$i] 127.0.0.1: connected to api.openai.com:443"; sleep 0.05; done\n', { mode: 0o755 });
  const script = `set -euo pipefail
PROXY_DIR="$1/proxies"; mkdir -p "$PROXY_DIR"
MICROSOCKS_BIN="$1/fake-microsocks"; PROXY_SOCKS_PORT=0; PROXY_USER=u; PROXY_PASS=p
${reset[0]}
${launch.trim()}
pid=$!
sleep 0.6
proxy_reset_logs
sleep 0.6
kill "$pid" 2>/dev/null || true
wait "$pid" 2>/dev/null || true
`;
  let log;
  try {
    execFileSync("bash", ["-c", script, "bash", dir], { stdio: ["ignore", "pipe", "pipe"] });
    log = readFileSync(path.join(dir, "proxies", "microsocks.log"), "utf8");
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
  assert.ok(!log.includes("\0"), `microsocks.log has ${log.split("\0").length - 1} NUL bytes after the reset`);
  assert.match(log, /^client\[\d+\] 127\.0\.0\.1: connected to api\.openai\.com:443$/m, `no record after the reset: ${JSON.stringify(log)}`);
});

console.log(`\n${passed} passed`);
