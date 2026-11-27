// Unit tests for the pre-header stream abort verdict. Run directly:
// `node prestream-cancel-verdict.test.mjs`. No test framework needed (the
// tests/e2e/api dir has no test runner configured).
import assert from "node:assert";
import { evaluatePreStreamCancel } from "./prestream-cancel-verdict.mjs";

let passed = 0;
function test(name, fn) {
  fn();
  passed++;
  console.log(`  ok - ${name}`);
}

const base = { method: "responses_stream", trials: 4, abortedBeforeHeaders: 4 };

// The leak this probe exists for: each abandoned stream kept its slot.
test("fails when the gauge rose after pre-header aborts", () => {
  const v = evaluatePreStreamCancel({ ...base, gaugeBefore: 0, gaugeAfter: 4 });
  assert.strictEqual(v.verdict, "FAIL");
  assert.match(v.detail, /rose by 4/);
});

// The series only exists once something incremented it, so a fresh gateway has no baseline.
test("treats a missing baseline as zero", () => {
  assert.strictEqual(evaluatePreStreamCancel({ ...base, gaugeBefore: undefined, gaugeAfter: 0 }).verdict, "PASS");
  assert.strictEqual(evaluatePreStreamCancel({ ...base, gaugeBefore: undefined, gaugeAfter: 4 }).verdict, "FAIL");
});

test("passes when the gauge returns to idle", () => {
  const v = evaluatePreStreamCancel({ ...base, gaugeBefore: 0, gaugeAfter: 0 });
  assert.strictEqual(v.verdict, "PASS");
});

// A slot held by other traffic across the baseline and released during the trials
// cancels out a trial's leak, so a non-idle baseline cannot be trusted.
test("fails when the baseline was not idle, even if the gauge returned to it", () => {
  const v = evaluatePreStreamCancel({ ...base, gaugeBefore: 1, gaugeAfter: 1 });
  assert.strictEqual(v.verdict, "FAIL");
  assert.match(v.detail, /not idle/);
});

// A double release (#7005) must not pass as balanced.
test("fails when the gauge fell below idle", () => {
  const v = evaluatePreStreamCancel({ ...base, gaugeBefore: 0, gaugeAfter: -2 });
  assert.strictEqual(v.verdict, "FAIL");
  assert.match(v.detail, /fell by 2/);
});

// Headers beat every abort: the path under test never ran, so it must not pass.
test("fails when no trial aborted before headers", () => {
  const v = evaluatePreStreamCancel({ ...base, abortedBeforeHeaders: 0, gaugeBefore: 0, gaugeAfter: 0 });
  assert.strictEqual(v.verdict, "FAIL");
  assert.match(v.detail, /--prestream-abort-ms/);
});

test("fails when the gauge series is missing after requests ran", () => {
  const v = evaluatePreStreamCancel({ ...base, gaugeBefore: undefined, gaugeAfter: undefined });
  assert.strictEqual(v.verdict, "FAIL");
  assert.match(v.detail, /telemetry/);
});

console.log(`prestream-cancel-verdict: ${passed} passed`);
