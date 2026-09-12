// Unit tests for the non-streaming abort verdict. Run directly:
// `node nonstream-cancel-verdict.test.mjs`. No test framework needed (the
// tests/e2e/api dir has no test runner configured).
import assert from "node:assert";
import { evaluateNonStreamCancel } from "./nonstream-cancel-verdict.mjs";

let passed = 0;
function test(name, fn) {
  fn();
  passed++;
  console.log(`  ok - ${name}`);
}

// The dropped-hook signature before the transport cancelled contexts on socket
// close: the send won the worker's delivery select, the caller was gone, and
// nothing ever wrote the row (maximhq/bifrost#6972).
test("fails when no log row exists for an aborted request", () => {
  const v = evaluateNonStreamCancel({ row: null, aborted: true });
  assert.strictEqual(v.verdict, "FAIL");
  assert.match(v.detail, /no log row/);
});

// The same signature after #7106: the logging plugin inserted the row as
// `processing` at pre-hook and the terminal hook never ran to move it on.
test("fails when the row is stuck in processing", () => {
  const v = evaluateNonStreamCancel({ row: { status: "processing" }, aborted: true });
  assert.strictEqual(v.verdict, "FAIL");
  assert.match(v.detail, /status=processing/);
});

for (const status of ["cancelled", "error", "success"]) {
  test(`passes on terminal status ${status}`, () => {
    const v = evaluateNonStreamCancel({ row: { status }, aborted: true });
    assert.strictEqual(v.verdict, "PASS");
    assert.strictEqual(v.detail, `status=${status}`);
  });
}

// A trial whose response beat the abort never exercised a disconnect. Reporting
// it as SKIP hid the race for as long as the runner did one trial per provider;
// the trial has to be re-tuned, so it fails loudly.
test("fails, not skips, when the response raced the abort", () => {
  const v = evaluateNonStreamCancel({ row: { status: "success" }, racedToCompletion: true, aborted: false });
  assert.strictEqual(v.verdict, "FAIL");
  assert.match(v.detail, /before the abort fired/);
});

test("skips when the abort never fired", () => {
  const v = evaluateNonStreamCancel({ row: null, aborted: false });
  assert.strictEqual(v.verdict, "SKIP");
});

// racedToCompletion wins over the row: a completed request may well have a
// terminal row, and that must not read as a pass.
test("racedToCompletion takes precedence over a terminal row", () => {
  const v = evaluateNonStreamCancel({ row: { status: "success" }, racedToCompletion: true, aborted: true });
  assert.strictEqual(v.verdict, "FAIL");
});

console.log(`${passed} passed`);
