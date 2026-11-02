#!/usr/bin/env node
// Prepare the opt-in ripgrep fixture config without changing the normal harness profile.
import { randomBytes } from "node:crypto";
import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";

const repo = fileURLToPath(new URL("../../../../", import.meta.url));
const fixture = join(repo, "tests/e2e/api/fixtures/ripgrep");
const app = resolve(process.argv[2] || join(repo, "tmp/ripgrep-test"));
const configPath = join(app, "config.json");

if (spawnSync("rg", ["--version"], { stdio: "ignore" }).status !== 0) {
  throw new Error("Install ripgrep (rg) and add it to PATH before preparing these tests.");
}
if (!existsSync(join(fixture, "logs.txt"))) {
  throw new Error("Missing ripgrep logs.txt fixture.");
}
if (existsSync(configPath)) {
  const existing = JSON.parse(readFileSync(configPath, "utf8"));
  if (existing.mcp?.tool_manager_config?.code_mode_ripgrep_root !== fixture) {
    throw new Error(`Refusing to overwrite ${configPath}: its search root differs from the fixture.`);
  }
  console.log(`Existing fixture config preserved: ${configPath}`);
} else {
  mkdirSync(dirname(configPath), { recursive: true });
  const config = {
    setup_token: randomBytes(32).toString("hex"),
    mcp: { tool_manager_config: { code_mode_ripgrep_root: fixture } },
  };
  writeFileSync(configPath, JSON.stringify(config, null, 2) + "\n", { mode: 0o600, flag: "wx" });
  console.log(`Created fixture config: ${configPath}`);
}
console.log("Start the gateway with this APP_DIR, then run make run-ripgrep-test in another terminal.");
