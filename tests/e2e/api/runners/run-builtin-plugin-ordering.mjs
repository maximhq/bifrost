#!/usr/bin/env node
// Dedicated, disposable gateway for folder 195. Never reuses BASE_URL or APP_DIR.
import { spawn } from "node:child_process";
import { randomUUID } from "node:crypto";
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import net from "node:net";
import { tmpdir } from "node:os";
import path from "node:path";
import { setTimeout as delay } from "node:timers/promises";
import { fileURLToPath, pathToFileURL } from "node:url";

function start(command, args, options) {
  const child = spawn(command, args, options);
  const process = { child, result: null };
  process.done = new Promise((resolve) => {
    const finish = (result) => { process.result = result; resolve(result); };
    child.once("error", (error) => finish({ error }));
    child.once("close", (code, signal) => finish({ code, signal }));
  });
  return process;
}

async function stop(process) {
  if (!process || process.result) return;
  process.child.kill("SIGTERM");
  const timer = setTimeout(() => process.child.kill("SIGKILL"), 5000);
  try { await process.done; } finally { clearTimeout(timer); }
}

async function unusedPort() {
  const server = net.createServer();
  await new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", resolve);
  });
  const port = server.address().port;
  await new Promise((resolve, reject) => server.close((error) => error ? reject(error) : resolve()));
  return port;
}

export async function withDisposableGateway(binary, run, { signal, startupTimeoutMs = 30000 } = {}) {
  const appDir = await mkdtemp(path.join(tmpdir(), "bifrost-plugin-ordering-"));
  let gateway;
  try {
    const port = await unusedPort();
    const baseUrl = `http://127.0.0.1:${port}`;
    const setupToken = randomUUID();
    const datasheet = path.join(appDir, "empty-datasheet.json");
    await writeFile(datasheet, "{}\n");
    await writeFile(path.join(appDir, "config.json"), JSON.stringify({
      // An authoritative empty provider section disables environment-key auto-detection.
      source_of_truth: "config.json",
      providers: {},
      setup_token: setupToken,
      config_store: { enabled: true, type: "sqlite", config: { path: path.join(appDir, "config.db") } },
      logs_store: { enabled: false },
      framework: { pricing: {
        pricing_url: pathToFileURL(datasheet).href,
        model_parameters_url: pathToFileURL(datasheet).href,
        mcp_library_sync_interval: 0,
      } },
    }), { mode: 0o600 });
    signal?.throwIfAborted();
    // The cwd is temporary too, so even relative runtime artifacts are discarded.
    gateway = start(path.resolve(binary), ["-app-dir", appDir, "-host", "127.0.0.1", "-port", String(port)], {
      cwd: appDir, stdio: "inherit", env: { ...process.env, BIFROST_SETUP_TOKEN: setupToken },
    });
    const deadline = Date.now() + startupTimeoutMs;
    while (true) {
      signal?.throwIfAborted();
      if (gateway.result) throw new Error(`Fixture gateway exited before readiness: ${JSON.stringify(gateway.result)}`);
      if (Date.now() >= deadline) throw new Error("Fixture gateway readiness timed out");
      let response;
      try {
        response = await fetch(`${baseUrl}/api/plugins`, {
          headers: { "X-Bifrost-Setup-Token": setupToken },
          signal: signal ? AbortSignal.any([signal, AbortSignal.timeout(1000)]) : AbortSignal.timeout(1000),
        });
      } catch {
        signal?.throwIfAborted();
        await delay(100, undefined, { signal });
        continue;
      }
      // A unique setup token prevents a port-reuse race from selecting another gateway.
      if (response.status !== 200) throw new Error(`Fixture gateway rejected its setup token: HTTP ${response.status}`);
      const body = await response.json();
      if (!Array.isArray(body.plugins) || body.plugins.some((plugin) => plugin.name === "telemetry")) {
        throw new Error("Fixture must start with no persisted telemetry row");
      }
      break;
    }
    return await run({ appDir, baseUrl, setupToken });
  } finally {
    await stop(gateway);
    await rm(appDir, { recursive: true, force: true });
  }
}

async function main() {
  const args = process.argv.slice(2);
  if (args.length !== 2 || args[0] !== "--binary" || !args[1]) {
    throw new Error("Usage: node run-builtin-plugin-ordering.mjs --binary /path/to/bifrost-http (run inside your dev container)");
  }
  const controller = new AbortController();
  const interrupt = () => controller.abort(new Error("Plugin ordering run interrupted"));
  process.once("SIGINT", interrupt);
  process.once("SIGTERM", interrupt);
  try {
    await withDisposableGateway(args[1], async ({ appDir, baseUrl, setupToken }) => {
      const source = JSON.parse(await readFile(new URL("../collections/provider-harness.json", import.meta.url), "utf8"));
      const folder = source.item.find((item) => item.name === "195. Builtin plugin ordering (#7157 / PR #7158)");
      if (!folder || folder.item.length !== 4) throw new Error("Expected all four plugin-ordering cases");
      // Run just this folder sequentially, with no shared harness scripts or provider calls.
      const collectionPath = path.join(appDir, "collection.json");
      await writeFile(collectionPath, JSON.stringify({ info: source.info, item: [folder] }));
      const environmentPath = path.join(appDir, "environment.json");
      await writeFile(environmentPath, JSON.stringify({ name: "Disposable plugin ordering", values: [
        { key: "builtinPluginOrderingFixture", value: "1", enabled: true },
        { key: "builtinPluginOrderingBaseUrl", value: baseUrl, enabled: true },
        { key: "builtinPluginOrderingSetupToken", value: setupToken, enabled: true },
      ] }), { mode: 0o600 });
      controller.signal.throwIfAborted();
      const newman = start("newman", ["run", collectionPath, "-e", environmentPath, "--bail", "--timeout", "60000"], { stdio: "inherit" });
      const abortNewman = () => newman.child.kill("SIGTERM");
      controller.signal.addEventListener("abort", abortNewman, { once: true });
      try {
        // Polling also lets interruption reach cleanup if a child ignores SIGTERM.
        while (!newman.result) {
          controller.signal.throwIfAborted();
          await delay(100, undefined, { signal: controller.signal });
        }
        controller.signal.throwIfAborted();
        if (newman.result.error || newman.result.code !== 0) throw new Error(`Newman failed: ${JSON.stringify(newman.result)}`);
      } finally {
        controller.signal.removeEventListener("abort", abortNewman);
        await stop(newman);
      }
    }, { signal: controller.signal });
  } finally {
    process.removeListener("SIGINT", interrupt);
    process.removeListener("SIGTERM", interrupt);
  }
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main().catch((error) => { console.error(error.message); process.exitCode = 1; });
}
