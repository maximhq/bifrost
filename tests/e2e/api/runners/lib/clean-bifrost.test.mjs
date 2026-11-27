// Unit tests for clean-bifrost.sh, the clean server the wiring runners boot with --binary. Run
// directly: `node clean-bifrost.test.mjs`. A fake bifrost-http stands in for the server: it records
// the setup token it was started with, says it started, and waits to be stopped.
import assert from "node:assert";
import { execFileSync } from "node:child_process";
import { chmodSync, existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

let passed = 0;
function test(name, fn) {
  fn();
  passed++;
  console.log(`  ok - ${name}`);
}

const SCRIPT = join(dirname(fileURLToPath(import.meta.url)), "clean-bifrost.sh");
const WORK = mkdtempSync(join(tmpdir(), "clean-bifrost-"));
// Removed however the run ends, a failed assertion included.
process.on("exit", () => rmSync(WORK, { recursive: true, force: true }));
const FAKE = join(WORK, "fake-bifrost");
writeFileSync(
  FAKE,
  `#!/bin/bash
while [ $# -gt 0 ]; do case "$1" in --app-dir) dir="$2"; shift ;; esac; shift; done
printf '%s' "\${BIFROST_SETUP_TOKEN-<unset>}" > "$dir/token"
[ -n "\${FAKE_IGNORE_TERM:-}" ] && trap '' TERM
echo "successfully started bifrost"
while :; do sleep 0.2; done
`,
);
chmodSync(FAKE, 0o755);

// Boots the fake server through boot_clean_bifrost in one shell, runs then in that shell, and stops
// it through stop_clean_bifrost into <WORK>/report, the way a runner's EXIT trap does. env is the
// runner's environment; BIFROST_SETUP_TOKEN is left out unless env sets it.
function runClean(then, env = {}, timeout = 30000) {
  const base = { ...process.env };
  delete base.BIFROST_SETUP_TOKEN;
  const port = 20000 + Math.floor(Math.random() * 20000);
  // The fake server is killed however the shell ends, including when a timeout stops it.
  const body = `source ${JSON.stringify(SCRIPT)}
trap 'kill -9 "$CLEAN_BIFROST_PID" 2>/dev/null' EXIT
trap 'exit 143' TERM
boot_clean_bifrost ${JSON.stringify(FAKE)} ${port} >/dev/null || exit 9
${then}`;
  try {
    return { code: 0, out: execFileSync("bash", ["-c", body], { env: { ...base, ...env }, encoding: "utf8", timeout }) };
  } catch (err) {
    if (err.signal) throw new Error(`the runner did not finish within ${timeout}ms (${err.signal})`);
    return { code: err.status, out: String(err.stdout || "") };
  }
}

test("the clean server gets the e2e setup token when the runner has none", () => {
  const { out } = runClean('echo "server=$(cat "$CLEAN_BIFROST_DIR/token") runner=$BIFROST_SETUP_TOKEN"; stop_clean_bifrost');
  assert.match(out, /server=bifrost-e2e-setup-token runner=bifrost-e2e-setup-token/);
});

test("a setup token the runner sets is the one the clean server gets", () => {
  const { out } = runClean('echo "server=$(cat "$CLEAN_BIFROST_DIR/token")"; stop_clean_bifrost', { BIFROST_SETUP_TOKEN: "custom-token" });
  assert.match(out, /server=custom-token/);
});

test("stop_clean_bifrost force-stops a server that ignores SIGTERM and keeps the exit code", () => {
  const report = join(WORK, "report");
  const { code, out } = runClean(
    'pid=$CLEAN_BIFROST_PID; false; stop_clean_bifrost ' + JSON.stringify(report) + '; rc=$?; kill -0 "$pid" 2>/dev/null && echo alive || echo gone; exit $rc',
    { FAKE_IGNORE_TERM: "1", CLEAN_BIFROST_STOP_GRACE: "2" },
  );
  assert.equal(code, 1, "the runner's failing exit code survives the stop");
  assert.match(out, /gone/, "the server is gone after the stop");
  assert.ok(existsSync(join(report, "server.log")), "the failed run's server log was saved");
  assert.match(readFileSync(join(report, "server.log"), "utf8"), /successfully started bifrost/);
});

// A fake server that exits on a failed datasheet download (or with failMessage) until it has been
// started failBoots times, counting its boots in <counter>; after that it starts normally.
function flakyFake(name, failBoots, failMessage) {
  const bin = join(WORK, name);
  const counter = join(WORK, `${name}.boots`);
  writeFileSync(counter, "0");
  writeFileSync(
    bin,
    `#!/bin/bash
n=$(($(cat ${JSON.stringify(counter)}) + 1)); echo "$n" > ${JSON.stringify(counter)}
if [ "$n" -le ${failBoots} ]; then echo '${failMessage}'; exit 1; fi
echo "successfully started bifrost"
while :; do sleep 0.2; done
`,
  );
  chmodSync(bin, 0o755);
  return { bin, boots: () => Number(readFileSync(counter, "utf8").trim()) };
}

function bootFlaky(fake, env = {}) {
  const port = 20000 + Math.floor(Math.random() * 20000);
  const body = `source ${JSON.stringify(SCRIPT)}
trap 'kill -9 "$CLEAN_BIFROST_PID" 2>/dev/null' EXIT
boot_clean_bifrost ${JSON.stringify(fake.bin)} ${port} >/dev/null 2>&1; rc=$?
stop_clean_bifrost >/dev/null 2>&1
exit $rc`;
  try {
    execFileSync("bash", ["-c", body], { env: { ...process.env, BIFROST_BOOT_RETRY_DELAY: "0", BIFROST_BOOT_ATTEMPTS: "4", ...env }, encoding: "utf8", timeout: 30000 });
    return 0;
  } catch (err) {
    return err.status;
  }
}

const DATASHEET_503 = '{"level":"fatal","message":"failed to initialize pricing manager: failed to sync pricing data: failed to download pricing data: HTTP 503"}';
const PARAMS_503 = '{"level":"fatal","message":"failed to sync model parameters data: failed to download model parameters data: HTTP 503"}';

test("a boot stopped by a failed datasheet download is retried until the server starts", () => {
  const fake = flakyFake("flaky-pricing", 2, DATASHEET_503);
  assert.equal(bootFlaky(fake), 0, "the third boot should have started");
  assert.equal(fake.boots(), 3);
});

test("a failed model-parameters download is retried the same way", () => {
  const fake = flakyFake("flaky-params", 1, PARAMS_503);
  assert.equal(bootFlaky(fake), 0);
  assert.equal(fake.boots(), 2);
});

test("datasheet retries stop after BIFROST_BOOT_ATTEMPTS boots", () => {
  const fake = flakyFake("always-503", 99, DATASHEET_503);
  assert.notEqual(bootFlaky(fake, { BIFROST_BOOT_ATTEMPTS: "3" }), 0);
  assert.equal(fake.boots(), 3);
});

test("any other startup failure is not retried", () => {
  const fake = flakyFake("other-fatal", 99, '{"level":"fatal","message":"failed to connect to vector store"}');
  assert.notEqual(bootFlaky(fake), 0);
  assert.equal(fake.boots(), 1);
});

// Both wiring runners take --binary relative to where they were started, though they change into
// tests/e2e/api before booting it. newman is stubbed: only the boot is under test.
test("a wiring runner resolves a relative --binary from the directory it was started in", () => {
  const caller = join(WORK, "caller");
  const bin = join(WORK, "bin");
  execFileSync("mkdir", ["-p", caller, bin]);
  writeFileSync(join(caller, "fake-bifrost"), readFileSync(FAKE));
  chmodSync(join(caller, "fake-bifrost"), 0o755);
  writeFileSync(join(bin, "newman"), "#!/bin/sh\nexit 0\n");
  chmodSync(join(bin, "newman"), 0o755);
  for (const runner of ["run-newman-routing-wiring-tests.sh", "run-newman-model-catalog-wiring-tests.sh"]) {
    const script = join(dirname(SCRIPT), "..", "individual", runner);
    const port = String(20000 + Math.floor(Math.random() * 20000));
    let out;
    try {
      out = execFileSync("bash", [script, "--binary", "./fake-bifrost", "--port", port], {
        cwd: caller,
        env: { ...process.env, PATH: `${bin}:${process.env.PATH}` },
        encoding: "utf8",
        timeout: 60000,
      });
    } catch (err) {
      assert.fail(`${runner} did not boot ./fake-bifrost from ${caller}: ${String(err.stdout || "")}${String(err.stderr || "")}`);
    }
    assert.match(out, /Bifrost ready on/, `${runner} never booted the server`);
  }
});

console.log(`\n${passed} passed`);