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
  isRetryable,
  retryAfterSeconds,
  backoffSeconds,
  retryableNames,
  rateLimitedNames,
  shouldRetry,
  RETRYABLE_CODES,
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

// isRateLimited stays 429-ONLY even though 503 is now retried alongside it. The two are different
// facts about a run and analyze-failures.mjs reports them differently: a 429 means the sweep asked
// for more than the account allows (reduce concurrency, lift quota), while a 503 means the provider
// was down. Collapsing them would hand someone a quota fix hint for an upstream outage.
test("only 429 counts as rate limited", () => {
  assert.strictEqual(isRateLimited(exec({ code: 429 })), true);
  assert.strictEqual(isRateLimited(exec({ code: 503 })), false);
  assert.strictEqual(isRateLimited(exec({ code: 200 })), false);
});

// ----- overload codes: retried on the same path as 429 --------------------------------------------

// A 503 is the provider saying "not now", exactly like a 429, and ~80 concurrent shards make it an
// expected outcome rather than an exotic one. Leaving it un-retried fails the whole sweep on an
// upstream blip that a single replay would have cleared.
//
// 529 is here because it is Anthropic's WHOLE equivalent of 503 - its published error table has no
// 503 entry at all ("529 - overloaded_error: The API is temporarily overloaded",
// platform.claude.com/docs/en/api/errors). A 503-only set would cover OpenAI and silently miss the
// anthropic and bedrock forks, two of the sweep's largest. Bifrost core groups the same pair in
// transientServerStatusCodes (core/utils.go).
test("the overload codes are retryable alongside 429", () => {
  assert.strictEqual(isRetryable(exec({ code: 429 })), true);
  assert.strictEqual(isRetryable(exec({ code: 503 })), true);
  assert.strictEqual(isRetryable(exec({ code: 529 })), true);
});

// Deliberately narrower than core's transient set. 500/502/504 are ambiguous between "upstream
// blipped" and "Bifrost has a defect", and finding the second kind is what this sweep is FOR:
// replaying them would turn a deterministic failure into a flaky-looking pass.
test("other 5xx codes are not retryable", () => {
  for (const code of [500, 502, 504, 400, 401, 404, 200]) {
    assert.strictEqual(isRetryable(exec({ code })), false, `${code} was treated as retryable`);
  }
});

test("the retryable set is exactly 429, 503 and 529", () => {
  assert.deepStrictEqual([...RETRYABLE_CODES].sort((a, b) => a - b), [429, 503, 529]);
});

test("a shard that failed only on an overload code is worth retrying", () => {
  assert.strictEqual(shouldRetry({ run: { executions: [exec({ code: 503 })] } }), true);
  assert.strictEqual(shouldRetry({ run: { executions: [exec({ code: 529 })] } }), true);
  assert.strictEqual(shouldRetry({ run: { executions: [exec({ code: 500 })] } }), false);
});

test("retryableNames selects the overload rows as well as the 429 rows", () => {
  const report = {
    run: {
      executions: [
        exec({ code: 429, name: "throttled" }),
        exec({ code: 503, name: "unavailable" }),
        exec({ code: 529, name: "overloaded" }),
        exec({ code: 500, name: "broken" }),
        exec({ code: 200, name: "fine" }),
      ],
    },
  };
  assert.deepStrictEqual([...retryableNames(report)].sort(), ["overloaded", "throttled", "unavailable"]);
  // The old export stays 429-only, so nothing that genuinely wanted rate limits changed meaning.
  assert.deepStrictEqual([...rateLimitedNames(report)], ["throttled"]);
});

// backoffSeconds returning 0 is what the Makefile reads as "do not retry this shard". If it kept
// filtering on 429 alone, every 503/529 shard would compute a wait of 0 and lose its retry no
// matter what the rest of the module said - so this is the assertion that makes the change real.
test("an overload-only shard gets a non-zero wait rather than being skipped", () => {
  assert.ok(backoffSeconds([exec({ code: 503 })], 1) > 0);
  assert.ok(backoffSeconds([exec({ code: 529 })], 1) > 0);
  assert.strictEqual(backoffSeconds([exec({ code: 500 })], 1), 0, "a 500-only shard must not be retried");
});

// An overloaded provider usually sends no Retry-After, so the exponential term carries it - but
// the header path still applies when one is present.
test("an overload code honours Retry-After, and falls back exponentially without it", () => {
  assert.strictEqual(backoffSeconds([exec({ code: 503, headers: { "retry-after": 17 } })], 1), 17);
  assert.strictEqual(backoffSeconds([exec({ code: 503 })], 1), DEFAULT_POLICY.baseSeconds);
  assert.strictEqual(backoffSeconds([exec({ code: 529 })], 1), DEFAULT_POLICY.baseSeconds);
});

test("a mixed 429/503 shard waits long enough for whichever asked for more", () => {
  const execs = [exec({ code: 429, headers: { "retry-after": 4 } }), exec({ code: 503, headers: { "retry-after": 30 } })];
  assert.strictEqual(backoffSeconds(execs, 1), 30);
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
