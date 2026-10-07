// Offline lifecycle tests: fake gateway/Newman processes, no provider calls.
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { access, mkdtemp, readFile, readdir, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { setTimeout as delay } from "node:timers/promises";
import { fileURLToPath } from "node:url";
import { withDisposableGateway } from "./run-builtin-plugin-ordering.mjs";

const testDir = await mkdtemp(path.join(tmpdir(), "plugin-ordering-runner-test-"));
let passed = 0;
async function test(name, fn) {
  await fn();
  passed++;
  console.log(`ok - ${name}`);
}
async function fakeGateway(mode = "ok") {
  const marker = path.join(testDir, `marker-${passed}.json`);
  const binary = path.join(testDir, `gateway-${passed}`);
  await writeFile(binary, `#!${process.execPath}
const fs = require('node:fs');
const http = require('node:http');
const args = process.argv.slice(2);
const appDir = args[args.indexOf('-app-dir') + 1];
const port = Number(args[args.indexOf('-port') + 1]);
const config = JSON.parse(fs.readFileSync(appDir + '/config.json', 'utf8'));
fs.writeFileSync(${JSON.stringify(marker)}, JSON.stringify({ appDir, pid: process.pid }));
if (${JSON.stringify(mode)} === 'exit') process.exit(2);
http.createServer((req, res) => {
  if (${JSON.stringify(mode)} === 'hang') return;
  if (${JSON.stringify(mode)} === 'unauthorized' || req.headers['x-bifrost-setup-token'] !== config.setup_token) {
    res.writeHead(403); res.end('{}'); return;
  }
  res.setHeader('content-type', 'application/json');
  res.end(JSON.stringify({ plugins: ${JSON.stringify(mode)} === 'dirty' ? [{ name: 'telemetry' }] : [] }));
}).listen(port, '127.0.0.1');
`, { mode: 0o700 });
  return { binary, marker };
}
async function assertDiscarded(marker) {
  const { appDir, pid } = JSON.parse(await readFile(marker, "utf8"));
  await assert.rejects(access(appDir), { code: "ENOENT" });
  assert.throws(() => process.kill(pid, 0), { code: "ESRCH" }, "fixture process must exit before removing its store");
}

try {
  await test("each run gets a fresh store; completed runs discard every artifact", async () => {
    const { binary, marker } = await fakeGateway();
    const dirs = new Set();
    for (let i = 0; i < 2; i++) {
      await withDisposableGateway(binary, async ({ appDir, baseUrl, setupToken }) => {
        assert.ok(!dirs.has(appDir));
        dirs.add(appDir);
        assert.match(baseUrl, /^http:\/\/127\.0\.0\.1:\d+$/);
        const config = JSON.parse(await readFile(path.join(appDir, "config.json"), "utf8"));
        assert.equal(config.setup_token, setupToken);
        assert.equal(config.source_of_truth, "config.json");
        assert.deepEqual(config.providers, {}, "do not auto-detect real provider keys from the caller's environment");
        assert.equal(config.config_store.config.path, path.join(appDir, "config.db"));
        assert.equal(config.logs_store.enabled, false);
        assert.equal(config.plugins, undefined, "telemetry must not be preseeded");
        for (const key of ["pricing_url", "model_parameters_url"]) {
          assert.equal(fileURLToPath(config.framework.pricing[key]), path.join(appDir, "empty-datasheet.json"));
        }
        await assert.rejects(access(config.config_store.config.path), { code: "ENOENT" });
        await writeFile(config.config_store.config.path, "simulated persisted telemetry");
      });
      await assertDiscarded(marker);
    }
  });
  await test("a failing regression still stops its gateway and discards the store", async () => {
    const { binary, marker } = await fakeGateway();
    await assert.rejects(withDisposableGateway(binary, async () => { throw new Error("regression failed"); }), /regression failed/);
    await assertDiscarded(marker);
  });
  for (const [mode, message] of [["exit", /exited/], ["hang", /timed out/], ["unauthorized", /setup token/], ["dirty", /no persisted telemetry/]]) {
    await test(`startup ${mode} fails closed and cleans up`, async () => {
      const { binary, marker } = await fakeGateway(mode);
      await assert.rejects(withDisposableGateway(binary, async () => { assert.fail("must not run Newman"); }, { startupTimeoutMs: 1200 }), message);
      await assertDiscarded(marker);
    });
  }
  await test("interrupting startup cancels readiness and cleans up", async () => {
    const { binary, marker } = await fakeGateway("hang");
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(new Error("test interruption")), 500);
    try {
      await assert.rejects(withDisposableGateway(binary, async () => assert.fail(), { signal: controller.signal }), /test interruption/);
    } finally { clearTimeout(timer); }
    await assertDiscarded(marker);
  });
  await test("a missing binary does not leave a temporary directory", async () => {
    const before = (await readdir(tmpdir())).filter((name) => name.startsWith("bifrost-plugin-ordering-")).sort();
    await assert.rejects(withDisposableGateway(path.join(testDir, "missing"), async () => assert.fail()), /exited/);
    assert.deepEqual((await readdir(tmpdir())).filter((name) => name.startsWith("bifrost-plugin-ordering-")).sort(), before);
  });
  for (const newmanExit of [0, 1]) {
    await test(`CLI propagates Newman exit ${newmanExit} and discards the store`, async () => {
      const { binary, marker } = await fakeGateway();
      await writeFile(path.join(testDir, "newman"), `#!${process.execPath}
const fs = require('node:fs');
const assert = require('node:assert/strict');
const args = process.argv.slice(2);
assert.equal(args[0], 'run');
const collection = JSON.parse(fs.readFileSync(args[1], 'utf8'));
assert.equal(collection.item.length, 1);
assert.equal(collection.item[0].item.length, 4);
assert.equal(collection.event, undefined);
const environment = JSON.parse(fs.readFileSync(args[args.indexOf('-e') + 1], 'utf8'));
assert.equal(environment.values.find(row => row.key === 'builtinPluginOrderingFixture').value, '1');
assert.ok(args.includes('--bail'));
process.exit(${newmanExit});
`, { mode: 0o700 });
      const child = spawn(process.execPath, [fileURLToPath(new URL("./run-builtin-plugin-ordering.mjs", import.meta.url)), "--binary", binary], {
        env: { ...process.env, PATH: `${testDir}:${process.env.PATH}`, BASE_URL: "http://shared.invalid", APP_DIR: testDir }, stdio: "pipe",
      });
      let stderr = "";
      child.stderr.on("data", (chunk) => { stderr += chunk; });
      child.stdout.resume();
      const code = await new Promise((resolve, reject) => { child.once("error", reject); child.once("close", resolve); });
      assert.equal(code, newmanExit, stderr);
      await assertDiscarded(marker);
      await access(path.join(testDir, "newman")); // Shared APP_DIR was never discarded.
    });
  }
  await test("SIGTERM during Newman shuts down both children and discards the store", async () => {
    const { binary, marker } = await fakeGateway();
    const newmanMarker = path.join(testDir, "newman-pid");
    await writeFile(path.join(testDir, "newman"), `#!${process.execPath}
require('node:fs').writeFileSync(${JSON.stringify(newmanMarker)}, String(process.pid));
setInterval(() => {}, 1000);
`, { mode: 0o700 });
    const child = spawn(process.execPath, [fileURLToPath(new URL("./run-builtin-plugin-ordering.mjs", import.meta.url)), "--binary", binary], {
      env: { ...process.env, PATH: `${testDir}:${process.env.PATH}` }, stdio: "ignore",
    });
    const done = new Promise((resolve, reject) => { child.once("error", reject); child.once("close", resolve); });
    const timeout = setTimeout(() => child.kill("SIGKILL"), 10000);
    try {
      let newmanPID;
      for (let i = 0; i < 100; i++) {
        try { newmanPID = Number(await readFile(newmanMarker, "utf8")); break; } catch (error) {
          if (error.code !== "ENOENT") throw error;
        }
        await delay(50);
      }
      assert.ok(newmanPID, "fake Newman must start before interruption");
      child.kill("SIGTERM");
      assert.equal(await done, 1);
      assert.throws(() => process.kill(newmanPID, 0), { code: "ESRCH" });
      await assertDiscarded(marker);
    } finally {
      child.kill("SIGTERM");
      await done;
      clearTimeout(timeout);
    }
  });
  console.log(`${passed} lifecycle tests passed`);
} finally {
  await rm(testDir, { recursive: true, force: true });
}
