import test from "node:test";
import assert from "node:assert/strict";
import { mkdtempSync, readFileSync, writeFileSync, rmSync, statSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";

const runner = fileURLToPath(new URL("./prepare-ripgrep-test.mjs", import.meta.url));
const fixture = fileURLToPath(new URL("../fixtures/ripgrep", import.meta.url));
const hasRg = spawnSync("rg", ["--version"], { stdio: "ignore" }).status === 0;

// withApp gives each setup test a private directory and removes it afterwards.
function withApp(fn) {
  const app = mkdtempSync(join(tmpdir(), "ripgrep-setup-test-"));
  try { fn(app); } finally { rmSync(app, { recursive: true, force: true }); }
}

// prepare runs the setup as a CLI, exercising its actual argument and environment handling.
function prepare(app, env = process.env) {
  return spawnSync(process.execPath, [runner, app], { encoding: "utf8", env });
}

test("creates an absolute fixture root with a private setup token and preserves reruns", { skip: !hasRg }, () => withApp((app) => {
  const first = prepare(app);
  assert.equal(first.status, 0, first.stderr);
  const configPath = join(app, "config.json");
  const original = readFileSync(configPath, "utf8");
  const config = JSON.parse(original);
  assert.equal(config.mcp.tool_manager_config.code_mode_ripgrep_root, fixture);
  assert.match(config.setup_token, /^[a-f0-9]{64}$/);
  assert.ok(!first.stdout.includes(config.setup_token));
  if (process.platform !== "win32") assert.equal(statSync(configPath).mode & 0o777, 0o600);
  assert.equal(prepare(app).status, 0);
  assert.equal(readFileSync(configPath, "utf8"), original);
}));

test("refuses a different search root without changing existing config", { skip: !hasRg }, () => withApp((app) => {
  const original = JSON.stringify({ mcp: { tool_manager_config: { code_mode_ripgrep_root: "/other" } } });
  writeFileSync(join(app, "config.json"), original);
  const result = prepare(app);
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /Refusing to overwrite/);
  assert.equal(readFileSync(join(app, "config.json"), "utf8"), original);
}));

test("fails clearly when rg is unavailable", () => withApp((app) => {
  const result = prepare(app, { ...process.env, PATH: app });
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /Install ripgrep/);
}));
