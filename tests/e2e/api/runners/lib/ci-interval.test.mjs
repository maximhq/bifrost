// Unit tests for the --ci-interval resolver. Run directly: `node ci-interval.test.mjs`.
// No test framework needed (the tests/e2e/api dir has no test runner configured).
import assert from "node:assert";
import {
  resolveCiIntervalMs,
  CI_INTERVAL_DEFAULT_SECONDS,
  CI_INTERVAL_MIN_SECONDS,
  CI_INTERVAL_MAX_SECONDS,
} from "./ci-interval.mjs";

let passed = 0;
function test(name, fn) {
  fn();
  passed++;
  console.log(`  ok - ${name}`);
}

test("uses the default when the flag is absent", () => {
  assert.strictEqual(
    resolveCiIntervalMs(undefined),
    CI_INTERVAL_DEFAULT_SECONDS * 1000
  );
});

// The harness prints nothing but the status table, so the interval is the only
// thing controlling how current that table is. Pinned rather than left to the
// constant so a bump back to a coarse 30s snapshot cadence is a deliberate edit.
test("the default is a 5s table refresh", () => {
  assert.strictEqual(CI_INTERVAL_DEFAULT_SECONDS, 5);
});

test("honours a valid interval", () => {
  assert.strictEqual(resolveCiIntervalMs("45"), 45_000);
});

test("clamps below-minimum intervals up to 5s", () => {
  assert.strictEqual(resolveCiIntervalMs("1"), CI_INTERVAL_MIN_SECONDS * 1000);
});

// The regression this guards: Makefile:2132 forwards MONITOR_INTERVAL straight
// into --ci-interval, so a non-numeric value reaches parseInt. Math.max(5, NaN)
// is NaN - not 5 - and setInterval(fn, NaN) is treated as ~1ms in Node, which
// floods the Actions log with progress snapshots.
test("falls back to the default for a non-numeric interval", () => {
  const ms = resolveCiIntervalMs("not-a-number");
  assert.ok(Number.isFinite(ms), `expected a finite delay, got ${ms}`);
  assert.strictEqual(ms, CI_INTERVAL_DEFAULT_SECONDS * 1000);
});

test("falls back to the default for an empty flag value", () => {
  const ms = resolveCiIntervalMs("");
  assert.ok(Number.isFinite(ms), `expected a finite delay, got ${ms}`);
  assert.strictEqual(ms, CI_INTERVAL_DEFAULT_SECONDS * 1000);
});

// The mirror image of the NaN case, and the same 1ms log flood. Node stores a
// timer delay in a 32-bit signed int, so anything over 2_147_483_647ms is reset
// to 1ms with a TimeoutOverflowWarning. The cap is set well below that bound at
// 45 minutes: a progress snapshot slower than that is useless in a job whose own
// timeout-minutes is 90, so an interval that large is a mistake either way.
test("clamps an oversized interval down to the maximum", () => {
  const ms = resolveCiIntervalMs("2147484");
  assert.strictEqual(ms, CI_INTERVAL_MAX_SECONDS * 1000);
  assert.ok(
    ms <= 2_147_483_647,
    `delay must fit a 32-bit signed int or Node resets it to 1ms, got ${ms}`
  );
});

test("clamps a merely-too-large interval to the maximum", () => {
  assert.strictEqual(
    resolveCiIntervalMs(String(CI_INTERVAL_MAX_SECONDS + 1)),
    CI_INTERVAL_MAX_SECONDS * 1000
  );
});

test("honours an interval exactly at the maximum", () => {
  assert.strictEqual(
    resolveCiIntervalMs(String(CI_INTERVAL_MAX_SECONDS)),
    CI_INTERVAL_MAX_SECONDS * 1000
  );
});

test("the maximum is 45 minutes", () => {
  assert.strictEqual(CI_INTERVAL_MAX_SECONDS, 45 * 60);
});

console.log(`\n${passed} passed`);
