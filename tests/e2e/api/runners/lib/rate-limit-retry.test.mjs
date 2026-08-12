// Unit tests for the 429 backoff policy.
//
// The numbers here decide how long ~80 concurrent shards sit idle, and how a rate-limited sweep
// resolves, so they are pinned rather than left to inspection.
//
// Run directly: `node rate-limit-retry.test.mjs`.
import assert from "node:assert";
import {
  headerValue,
  isRateLimited,
  retryAfterSeconds,
  backoffSeconds,
  rateLimitedNames,
  shouldRetry,
  DEFAULT_POLICY,
} from "./rate-limit-retry.mjs";

let passed = 0;
function test(name, fn) {
  fn();
  passed++;
  console.log(`  ok - ${name}`);
}

const exec = ({ code = 429, headers = {}, name = "row", assertions = [] } = {}) => ({
  item: { name },
  assertions,
  response: {
    code,
    header: Object.entries(headers).map(([key, value]) => ({ key, value: String(value) })),
  },
});

console.log("rate-limit retry policy");

test("header lookup is case-insensitive", () => {
  // The two providers' own docs spell it differently ("retry-after" vs "Retry-After"), and newman
  // preserves whatever casing the origin sent.
  assert.strictEqual(headerValue(exec({ headers: { "Retry-After": 9 } }), "retry-after"), "9");
  assert.strictEqual(headerValue(exec({ headers: { "retry-after": 9 } }), "Retry-After"), "9");
  assert.strictEqual(headerValue(exec({ headers: {} }), "retry-after"), null);
});

test("only 429 counts as rate limited", () => {
  assert.strictEqual(isRateLimited(exec({ code: 429 })), true);
  assert.strictEqual(isRateLimited(exec({ code: 503 })), false);
  assert.strictEqual(isRateLimited(exec({ code: 200 })), false);
});

test("a non-numeric Retry-After is ignored rather than guessed at", () => {
  // Retry-After also permits an HTTP-date. Mis-parsing one into a huge sleep would stall a shard,
  // so the exponential fallback takes over instead.
  assert.strictEqual(retryAfterSeconds(exec({ headers: { "retry-after": "Wed, 21 Oct 2026 07:28:00 GMT" } })), null);
  assert.strictEqual(retryAfterSeconds(exec({ headers: { "retry-after": "12" } })), 12);
  assert.strictEqual(retryAfterSeconds(exec({ headers: { "retry-after": "-3" } })), null);
});

test("the wait is the MAX Retry-After, not the first or the mean", () => {
  // The shard replays all its rate-limited rows together, so the wait has to satisfy every one.
  const execs = [
    exec({ headers: { "retry-after": 3 } }),
    exec({ headers: { "retry-after": 21 } }),
    exec({ headers: { "retry-after": 8 } }),
  ];
  assert.strictEqual(backoffSeconds(execs, 1), 21);
});

test("exponential fallback when no provider sent a header", () => {
  const execs = [exec(), exec()];
  assert.strictEqual(backoffSeconds(execs, 1), 5);
  assert.strictEqual(backoffSeconds(execs, 2), 10);
  assert.strictEqual(backoffSeconds(execs, 3), 20);
});

test("a headerless execution still gets a sane wait in a mixed shard", () => {
  // OpenAI's docs say Retry-After MAY be present, so one row can ask for 1s while another says
  // nothing at all. Honouring only the header would retry the silent one almost immediately.
  const execs = [exec({ headers: { "retry-after": 1 } }), exec()];
  assert.strictEqual(backoffSeconds(execs, 2), 10, "the exponential term must floor the wait");
});

test("the wait is capped", () => {
  const execs = [exec({ headers: { "retry-after": 4000 } })];
  assert.strictEqual(backoffSeconds(execs, 1), DEFAULT_POLICY.maxSeconds);
});

test("no rate-limited executions means no wait and no retry", () => {
  const execs = [exec({ code: 200 }), exec({ code: 400 })];
  assert.strictEqual(backoffSeconds(execs, 1), 0);
  assert.strictEqual(shouldRetry({ run: { executions: execs } }), false);
});

test("shouldRetry is true only when a 429 is present", () => {
  assert.strictEqual(shouldRetry({ run: { executions: [exec({ code: 429 })] } }), true);
  assert.strictEqual(shouldRetry({}), false);
  assert.strictEqual(shouldRetry({ run: { executions: [] } }), false);
});

test("rateLimitedNames selects only the 429 rows", () => {
  const report = {
    run: {
      executions: [
        exec({ code: 429, name: "throttled one" }),
        exec({ code: 200, name: "fine" }),
        exec({ code: 400, name: "real defect" }),
        exec({ code: 429, name: "throttled two" }),
      ],
    },
  };
  // A real defect must never enter the retry set: replaying it burns quota re-confirming a bug
  // and turns a deterministic failure into a flaky-looking one.
  assert.deepStrictEqual([...rateLimitedNames(report)].sort(), ["throttled one", "throttled two"]);
});

console.log(`\n${passed} test(s) passed`);
