// Unit tests for the size-safe newman report reader. Run directly:
// `node read-report.test.mjs`. No test framework needed (the tests/e2e/api dir has
// no test runner configured). Requires jq, exactly like the code under test.
import assert from "node:assert";
import { mkdtempSync, writeFileSync, readFileSync, statSync, rmSync } from "node:fs";
import { execFileSync } from "node:child_process";
import { tmpdir } from "node:os";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";
import { sanitizeTo, readReport } from "./read-report.mjs";

const MERGE_PROGRAM = join(dirname(fileURLToPath(import.meta.url)), "newman-merge.jq");

let passed = 0;
function test(name, fn) {
  fn();
  passed++;
  console.log(`  ok - ${name}`);
}

// A single execution whose response is an image-sized Buffer: 30000 elements, past the
// merge program's 20000-element cap. This is the shape that blew a full sweep's merged
// report to 686MB - not because of the byte count, but because jq pretty-prints each
// element on its own indented line ("        255,\n", ~13 bytes) instead of "255," (4).
const STREAM_LEN = 30000;
const fixture = {
  collection: { info: { name: "fixture" }, item: [] },
  environment: {},
  run: {
    executions: [
      {
        id: "exec-1",
        item: { id: "item-1", name: "image gen" },
        request: { method: "POST", url: { path: ["v1", "images"] }, body: { mode: "raw", raw: "{}" } },
        response: {
          code: 200,
          status: "OK",
          stream: { type: "Buffer", data: Array.from({ length: STREAM_LEN }, (_, i) => i % 256) },
        },
        assertions: [{ assertion: "status 200" }],
      },
    ],
    failures: [],
    stats: { requests: { total: 1, failed: 0 } },
    timings: {},
  },
};

const dir = mkdtempSync(join(tmpdir(), "read-report-test-"));
const src = join(dir, "newman-report.json");
const dest = join(dir, ".newman-report.slim.json");
writeFileSync(src, JSON.stringify(fixture));

try {
  test("sanitizeTo writes the slimmed report as compact JSON (one line)", () => {
    sanitizeTo(src, dest);
    const out = readFileSync(dest, "utf8");
    // jq's compact mode ends the document with a single trailing newline; anything
    // beyond that means pretty-printing crept back in.
    const lines = out.trimEnd().split("\n").length;
    assert.strictEqual(lines, 1, `expected one line of compact JSON, got ${lines} lines`);
  });

  test("sanitizeTo keeps the existing stream cap (head + tail, truncated flag)", () => {
    const parsed = JSON.parse(readFileSync(dest, "utf8"));
    const stream = parsed.run.executions[0].response.stream;
    assert.strictEqual(stream.type, "Buffer");
    assert.strictEqual(stream.data.length, 20000);
    assert.strictEqual(stream.truncated, true);
    assert.strictEqual(stream.truncatedMiddle, true);
    // Head is the first 12000 elements, tail the last 8000 - both ends must survive.
    assert.strictEqual(stream.data[0], 0);
    assert.strictEqual(stream.data[11999], 11999 % 256);
    assert.strictEqual(stream.data[12000], (STREAM_LEN - 8000) % 256);
    assert.strictEqual(stream.data[19999], (STREAM_LEN - 1) % 256);
  });

  test("compact output is materially smaller than the same program pretty-printed", () => {
    // The same merge program without -c: this is what every writer used to do, and what
    // pushed a 235MB report to 686MB on disk.
    const pretty = execFileSync("jq", ["-s", "-f", MERGE_PROGRAM, src], { maxBuffer: 64 * 1024 * 1024 });
    const compactSize = statSync(dest).size;
    assert.ok(
      compactSize * 2 < pretty.length,
      `compact ${compactSize}B should be well under half of pretty ${pretty.length}B`
    );
  });

  test("readReport parses a report that fits in a string without slimming", () => {
    const report = readReport(src);
    assert.strictEqual(report.run.executions.length, 1);
    assert.strictEqual(report.run.executions[0].response.stream.data.length, STREAM_LEN);
  });
} finally {
  rmSync(dir, { recursive: true, force: true });
}

console.log(`\n${passed} tests passed`);
