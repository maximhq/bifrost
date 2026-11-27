// Unit tests for the gateway lifecycle in .github/workflows/scripts/harness-gateway.sh.
// Run directly: `node harness-gateway.test.mjs`. No Bifrost, no network beyond loopback:
// a fake gateway answers /health, and a fake `sg` stands in for shadow-utils.
import assert from "node:assert";
import { execFileSync } from "node:child_process";
import { chmodSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import net from "node:net";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const REPO_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../../../../..");

let passed = 0;
async function test(name, fn) {
  await fn();
  passed++;
  console.log(`  ok - ${name}`);
}

function freePort() {
  return new Promise((resolve, reject) => {
    const server = net.createServer();
    server.once("error", reject);
    server.listen(0, "127.0.0.1", () => {
      const { port } = server.address();
      server.close(() => resolve(port));
    });
  });
}

function portOpen(port) {
  return new Promise((resolve) => {
    const socket = net.connect(port, "127.0.0.1");
    socket.once("connect", () => {
      socket.destroy();
      resolve(true);
    });
    socket.once("error", () => resolve(false));
  });
}

// scratch builds a fake gateway binary and, in bin/, a fake `sg` that forks the command
// and waits for it, the way shadow-utils sg does when SYSLOG_SG_ENAB is on (the Ubuntu
// default). Signalling that sg does not reach the command.
function scratch() {
  const dir = mkdtempSync(path.join(tmpdir(), "harness-gateway-"));
  const server = path.join(dir, "server.mjs");
  writeFileSync(
    server,
    `import http from "node:http";
const port = Number(process.argv[process.argv.indexOf("--port") + 1]);
http.createServer((req, res) => { res.statusCode = req.url === "/health" ? 200 : 404; res.end(); }).listen(port, "127.0.0.1");
`,
  );
  const binary = path.join(dir, "bifrost-http");
  writeFileSync(binary, `#!/usr/bin/env bash\nexec node ${JSON.stringify(server)} "$@"\n`);
  chmodSync(binary, 0o755);
  const bin = path.join(dir, "bin");
  execFileSync("mkdir", ["-p", bin]);
  writeFileSync(path.join(bin, "sg"), `#!/usr/bin/env bash\n# sg GROUP -c COMMAND, forking like SYSLOG_SG_ENAB\nbash -c "$3" &\nwait $!\n`);
  chmodSync(path.join(bin, "sg"), 0o755);
  return { dir, binary, bin };
}

// runLifecycle starts the gateway through the helper, stops it, and reports whether its
// port was open while running and after the stop returned.
async function runLifecycle({ group }) {
  const { dir, binary, bin } = scratch();
  const port = await freePort();
  try {
    const script = `
set -euo pipefail
source "$REPO_ROOT/.github/workflows/scripts/harness-gateway.sh"
harness_start_gateway "$APP_DIR" "$PORT" "$LOG" >/dev/null
echo started
harness_stop_gateway >/dev/null
echo stopped
`;
    const env = {
      ...process.env,
      REPO_ROOT,
      HARNESS_BINARY: binary,
      APP_DIR: path.join(dir, "app"),
      PORT: String(port),
      LOG: path.join(dir, "gateway.log"),
      PATH: `${bin}:${process.env.PATH}`,
      HARNESS_GATEWAY_GROUP: group,
    };
    const out = execFileSync("bash", ["-c", script], { env, encoding: "utf8", timeout: 30000 });
    assert.match(out, /started[\s\S]*stopped/);
    return { openAfterStop: await portOpen(port) };
  } finally {
    // A gateway the helper failed to stop would outlive the test; end it by port.
    try {
      execFileSync("bash", ["-c", `lsof -ti tcp:${port} -sTCP:LISTEN | xargs kill 2>/dev/null || true`]);
    } catch {}
    rmSync(dir, { recursive: true, force: true });
  }
}

await test("stop ends the gateway when it runs directly", async () => {
  const { openAfterStop } = await runLifecycle({ group: "" });
  assert.equal(openAfterStop, false, "the gateway still holds its port after harness_stop_gateway");
});

await test("stop ends the gateway when a forking sg launched it", async () => {
  const { openAfterStop } = await runLifecycle({ group: "bifrost-egress" });
  assert.equal(
    openAfterStop,
    false,
    "the gateway still holds its port after harness_stop_gateway: the helper signalled sg, not the gateway, so the next cell would test this gateway",
  );
});

console.log(`harness-gateway: ${passed} passed`);
