// Unit tests for tests/e2e/api/runners/proxy/run-proxy-matrix.sh.
// Run directly: `node run-proxy-matrix.test.mjs`. No proxies, no gateway, no network: the
// script's pure shell helpers are extracted and run on their own.
import assert from "node:assert";
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
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

console.log(`\n${passed} passed`);
