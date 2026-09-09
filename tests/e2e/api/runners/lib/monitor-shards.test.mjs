// Unit tests for the monitor's per-shard accounting.
//
// A provider fork is sharded by modality class (HARNESS_CLASSES in the Makefile), so several
// concurrent newman processes write into ONE provider row of the status table. Two things have to
// hold for that table to stay truthful, and neither is visible from the shape of the code:
//
//   1. Every newman-cli-<provider>--<class>.log is tailed, not just newman-cli-<provider>.log.
//      Miss this and a sharded run renders a table of zeroes while the sweep is passing fine.
//   2. The "which request am I mid-way through" cursor is per STREAM, not per provider. Two shards
//      of one provider interleave, so a shared cursor lets shard B's "↳" commit shard A's
//      half-read request: the run then reports fewer passes than it actually had, and the loss
//      is silent because the totals still look plausible.
//
// harness-monitor.mjs has top-level side effects (arg validation, timers, alt-screen escapes) and
// cannot be imported, so this drives the real binary as a subprocess in --ci mode and asserts on
// the heartbeat line it prints - the same technique monitor-lifecycle.test.mjs uses.
//
// Run directly: `node monitor-shards.test.mjs`.
import assert from "node:assert";
import { spawn } from "node:child_process";
import { mkdtempSync, mkdirSync, writeFileSync, appendFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, resolve, join } from "node:path";
import { tmpdir } from "node:os";

let passed = 0;
const results = [];
function check(name, fn) {
  fn();
  passed++;
  results.push(name);
}

const HERE = dirname(fileURLToPath(import.meta.url));
const MONITOR = resolve(HERE, "../harness-monitor.mjs");

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// Newman's cli reporter line shapes, already prefixed by the Makefile's `sed s/^/[<provider>] /`.
const start = (p, name) => `[${p}] ↳ ${name}\n`;
const ok = (p) => `[${p}] POST http://h/v1/chat/completions [200 OK, 1.2kB, 340ms]\n`;
const bad = (p) =>
  `[${p}] POST http://h/v1/chat/completions [400 Bad Request, 1.2kB, 340ms]\n` +
  `[${p}]   1. expected 400 to be within 200..299\n`;

// Runs the monitor over a tmp dir while `drive` writes logs into it on a timer, and returns the
// last heartbeat line plus the final table. The monitor self-exits once every launched shard has
// reported into the status file and the logs go idle.
// ci:true (the default) is the append-only path, where per-shard lines are emitted as they happen.
// ci:false is the interactive alt-screen path, where they are rendered inside the frame instead.
// Both are real modes of the monitor and the shard reporting differs between them, so the tests
// exercise each rather than assuming one stands in for the other.
async function runMonitor({ providers, launched, drive, extraArgs = [], ci = true }) {
  const dir = mkdtempSync(join(tmpdir(), "monitor-shards-"));
  const tmp = join(dir, "tmp");
  mkdirSync(tmp);
  writeFileSync(join(tmp, "parallel-status"), "");

  const proc = spawn(
    process.execPath,
    [
      MONITOR,
      ...(ci ? ["--ci"] : []),
      "--mode", "parallel",
      "--providers", providers,
      "--tmp-dir", tmp,
      "--status-file", join(tmp, "parallel-status"),
      "--launched", String(launched),
      ...extraArgs,
    ],
    { stdio: ["ignore", "pipe", "pipe"] }
  );
  let out = "";
  proc.stdout.on("data", (d) => (out += d.toString()));
  proc.stderr.on("data", (d) => (out += d.toString()));

  await sleep(300); // let the monitor register its pass before the first bytes land
  await drive(tmp);
  await new Promise((r) => proc.on("close", r));

  return { out };
}

// Reads a provider's row out of the final table rather than the heartbeat line: heartbeats are
// throttled to CI_INTERVAL_MS (5s floor), so a short run prints only the initial all-zero one and
// asserting on it would pass for the wrong reason. The table is always printed exactly once, at
// teardown, with the committed numbers.
//
//   │ ✗ │ openai    │     0 │    1 │      1 │
const row = (out, provider) => {
  // ANSI has to come off first: the CI path prints a stripAnsi'd table, but the interactive frame
  // keeps its colour codes, and they sit between the row's cell separators.
  const plain = out.replace(/\x1b\[[0-9;?]*[A-Za-z]/g, "");
  // LAST match, not first: the interactive path redraws the frame every second, so the capture
  // holds dozens of frames and the earliest ones are still all zeroes. Only the final frame
  // carries the committed numbers.
  const re = new RegExp(`│\\s*(\\S?)\\s*│\\s*${provider}\\s*│\\s*(\\d+)\\s*│\\s*(\\d+)\\s*│\\s*(\\d+)\\s*│`, "g");
  const all = [...plain.matchAll(re)];
  const m = all[all.length - 1];
  assert.ok(m, `no table row for ${provider} in:\n${out}`);
  return { mark: m[1], total: +m[2], pass: +m[3], fail: +m[4] };
};

console.log("harness-monitor shard accounting");

// ---------------------------------------------------------------------------
// Two shards of ONE provider, interleaved so that a shared cursor would drop a request.
// Timeline: both shards open a request, THEN both close - so shard B's "↳" is read while
// shard A's request is still open.
// ---------------------------------------------------------------------------
{
  const r = await runMonitor({
    providers: "openai anthropic",
    launched: 2,
    drive: async (tmp) => {
      const tools = join(tmp, "newman-cli-openai--tools.log");
      const chat = join(tmp, "newman-cli-openai--chat.log");
      writeFileSync(tools, start("openai", "request A"));
      writeFileSync(chat, start("openai", "request B"));
      await sleep(1200);
      appendFileSync(tools, bad("openai"));
      appendFileSync(chat, ok("openai"));
      await sleep(1200);
      appendFileSync(join(tmp, "parallel-status"), "openai--tools:fail\nopenai--chat:pass\n");
    },
  });
  const openai = row(r.out, "openai");

  check("both class shards of a provider are tailed", () => {
    // The pre-shard monitor only ever opened newman-cli-<provider>.log, so this whole run
    // would have counted zero.
    assert.notStrictEqual(openai.pass + openai.fail, 0, "no shard log was tailed at all");
  });

  check("interleaved shards do not commit each other's in-flight request", () => {
    // The failing shard must NOT swallow the passing one. A shared per-provider cursor
    // yields pass=0 here, because shard B's "↳" resets the cursor before shard A reports.
    assert.strictEqual(openai.pass, 1, "the passing shard's request was lost");
    assert.strictEqual(openai.fail, 1, "the failing shard's request was lost");
  });

  check("a provider with one failing shard is marked failed", () => {
    assert.strictEqual(openai.mark, "✗");
  });

  check("a provider with no shards stays untouched", () => {
    assert.deepStrictEqual(row(r.out, "anthropic"), { mark: "-", total: 0, pass: 0, fail: 0 });
  });
}

// ---------------------------------------------------------------------------
// CLASS_SHARDS=0 keeps the unsharded newman-cli-<provider>.log name. The shard-aware
// tailing must not regress that path.
// ---------------------------------------------------------------------------
{
  const r = await runMonitor({
    providers: "gemini",
    launched: 1,
    drive: async (tmp) => {
      const log = join(tmp, "newman-cli-gemini.log");
      writeFileSync(log, start("gemini", "request A") + ok("gemini"));
      await sleep(1200);
      appendFileSync(join(tmp, "parallel-status"), "gemini:pass\n");
    },
  });

  const gemini = row(r.out, "gemini");

  check("an unsharded provider log is still tailed (CLASS_SHARDS=0)", () => {
    assert.strictEqual(gemini.pass, 1);
    assert.strictEqual(gemini.fail, 0);
  });

  check("a plain provider status line still marks the provider passed", () => {
    assert.strictEqual(gemini.mark, "✓");
  });
}

// ---------------------------------------------------------------------------
// Per-shard reporting. Each shard reports its OWN totals when it finishes, which is the only view
// that says which of ~80 slices is in trouble - the provider row aggregates that away.
// ---------------------------------------------------------------------------
{
  const r = await runMonitor({
    providers: "openai bedrock",
    launched: 2,
    drive: async (tmp) => {
      const a = join(tmp, "newman-cli-openai--tools.log");
      const b = join(tmp, "newman-cli-bedrock--vision.log");
      writeFileSync(a, start("openai", "row A"));
      writeFileSync(b, start("bedrock", "row B"));
      await sleep(1200);
      appendFileSync(a, ok("openai"));
      appendFileSync(b, bad("bedrock"));
      await sleep(1200);
      appendFileSync(join(tmp, "parallel-status"), "openai--tools:pass\nbedrock--vision:fail\n");
    },
  });

  // Escapes the FULL metacharacter set, not just "-": outside a character class "-" is an
  // ordinary literal and needed no escaping, so the old `replace(/[-]/g, "\\-")` did nothing
  // while looking like it sanitised the input. Shard ids are "<provider>--<class>", so today
  // nothing here is a metacharacter - this keeps a future provider or class name containing
  // one (a ".", a "+") from silently turning into a pattern.
  const escapeRe = (s) => s.replace(/[.*+?^${}()|[\]\\-]/g, "\\$&");

  const shardLine = (shard) => {
    const m = r.out.match(
      new RegExp(`\\[${escapeRe(shard)}\\]\\s+(\\d+) total\\s+(\\d+) pass\\s+(\\d+) fail`)
    );
    assert.ok(m, `no completion line for ${shard} in:\n${r.out}`);
    return { total: +m[1], pass: +m[2], fail: +m[3] };
  };

  check("each shard reports its own totals on completion", () => {
    assert.deepStrictEqual(shardLine("openai--tools"), { total: 1, pass: 1, fail: 0 });
    assert.deepStrictEqual(shardLine("bedrock--vision"), { total: 1, pass: 0, fail: 1 });
  });

  check("a shard's verdict comes from the committed state, not the response summary", () => {
    // bad() emits "[400 Bad Request...]" and only THEN the numbered assertion failure, so counting
    // at the summary line would have had to guess the verdict before it was known.
    assert.strictEqual(shardLine("bedrock--vision").pass, 0);
  });

  check("shard totals are not double counted into the provider row", () => {
    // The provider row and the shard line are computed from the same commits by different paths;
    // if the shard tally were also added to the provider counters they would disagree.
    assert.strictEqual(row(r.out, "openai").pass, shardLine("openai--tools").pass);
    assert.strictEqual(row(r.out, "bedrock").fail, shardLine("bedrock--vision").fail);
  });
}

// ---------------------------------------------------------------------------
// Interactively the shard lines live INSIDE the frame, because the table is redrawn by homing the
// cursor over the alt-screen buffer and anything printed beside it would be wiped on the next
// redraw. The table must still be there - the lines accompany it, they do not replace it.
// ---------------------------------------------------------------------------
{
  const r = await runMonitor({
    providers: "openai bedrock",
    launched: 2,
    ci: false,
    drive: async (tmp) => {
      const a = join(tmp, "newman-cli-openai--tools.log");
      const b = join(tmp, "newman-cli-bedrock--vision.log");
      writeFileSync(a, start("openai", "row A") + ok("openai"));
      writeFileSync(b, start("bedrock", "row B") + bad("bedrock"));
      await sleep(1200);
      appendFileSync(join(tmp, "parallel-status"), "openai--tools:pass\nbedrock--vision:fail\n");
    },
  });

  check("interactively the shard lines render inside the frame, alongside the table", () => {
    assert.match(r.out, /Completed shards \(2\)/);
    assert.match(r.out, /\[openai--tools\]\s+\d+ total/);
    assert.strictEqual(row(r.out, "openai").pass, 1);
    assert.strictEqual(row(r.out, "bedrock").fail, 1);
  });
}

// ---------------------------------------------------------------------------
// Denominators. A provider's fork is several files (one per class shard), so its Total is their
// SUM. Reading only the unsharded harness-filtered-<provider>.json left every total at 0, which
// surfaced as "187/0 done" with an ETA that never resolved.
// ---------------------------------------------------------------------------
{
  const collectionOf = (n) => ({
    item: [
      {
        name: "folder",
        item: Array.from({ length: n }, (_, i) => ({ name: `req ${i}`, request: { url: "http://h/v1" } })),
      },
    ],
  });

  const r = await runMonitor({
    providers: "openai",
    launched: 2,
    drive: async (tmp) => {
      writeFileSync(join(tmp, "harness-filtered-openai--tools.json"), JSON.stringify(collectionOf(7)));
      writeFileSync(join(tmp, "harness-filtered-openai--chat.json"), JSON.stringify(collectionOf(5)));
      // A retry collection replays rows already counted in its shard's file. Counting it too
      // would inflate the denominator to 15 and make the ETA chase work that does not exist.
      writeFileSync(join(tmp, "harness-filtered-openai--tools-retry1.json"), JSON.stringify(collectionOf(3)));
      await sleep(1300);
      const a = join(tmp, "newman-cli-openai--tools.log");
      writeFileSync(a, start("openai", "row A") + ok("openai"));
      await sleep(1200);
      appendFileSync(join(tmp, "parallel-status"), "openai--tools:pass\nopenai--chat:pass\n");
    },
  });

  check("a provider's total is the sum of its class shards", () => {
    assert.strictEqual(row(r.out, "openai").total, 12, "expected 7 + 5, not one shard or zero");
  });

  check("retry collections do not inflate the denominator", () => {
    assert.notStrictEqual(row(r.out, "openai").total, 15);
  });

  check("a real denominator makes the done ratio meaningful", () => {
    // The reported symptom was "187/0 done": a zero here is what breaks the ETA.
    assert.notStrictEqual(row(r.out, "openai").total, 0);
  });
}

// ---------------------------------------------------------------------------
// A 429 retry replays rows the main pass already counted. Its log has to be tailed - that keeps
// the run from looking idle mid-retry - but counting its requests again would push pass+fail above
// the shard's real total, showing a recovered row as an extra pass while its original fail stays.
// ---------------------------------------------------------------------------
{
  const r = await runMonitor({
    providers: "openai",
    launched: 1,
    drive: async (tmp) => {
      const main = join(tmp, "newman-cli-openai--tools.log");
      writeFileSync(main, start("openai", "throttled row") + bad("openai"));
      await sleep(1200);
      // The same row, replayed by the retry pass and passing this time.
      writeFileSync(
        join(tmp, "newman-cli-openai--tools-retry1.log"),
        start("openai", "throttled row") + ok("openai")
      );
      await sleep(1200);
      appendFileSync(join(tmp, "parallel-status"), "openai--tools:pass\n");
    },
  });

  check("a retry pass does not double count into the provider row", () => {
    const openai = row(r.out, "openai");
    assert.strictEqual(
      openai.pass + openai.fail,
      1,
      "the replayed row must be counted once, not once per attempt"
    );
  });
}

// ---------------------------------------------------------------------------
// SHARD_LINES=0 must leave the plain table exactly as it was.
// ---------------------------------------------------------------------------
{
  const r = await runMonitor({
    providers: "openai",
    launched: 1,
    extraArgs: ["--shard-lines", "0"],
    drive: async (tmp) => {
      writeFileSync(join(tmp, "newman-cli-openai--tools.log"), start("openai", "row A") + ok("openai"));
      await sleep(1200);
      appendFileSync(join(tmp, "parallel-status"), "openai--tools:pass\n");
    },
  });

  check("--shard-lines 0 suppresses the per-shard section", () => {
    assert.doesNotMatch(r.out, /Completed shards/);
    assert.strictEqual(row(r.out, "openai").pass, 1);
  });
}

for (const name of results) console.log(`  ok - ${name}`);
console.log(`\n${passed} test(s) passed`);
