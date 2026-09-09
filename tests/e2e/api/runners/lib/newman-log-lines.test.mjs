// Unit tests for the newman CLI log line shapes. Run directly:
// `node newman-log-lines.test.mjs`. No test framework needed (the tests/e2e/api
// dir has no test runner configured).
import assert from "node:assert";
import {
  RE_FOLDER,
  RE_REQUEST,
  RE_METHOD_URL,
  RE_REQUEST_DONE,
  RE_REQUEST_ERRORED,
  RE_ASSERT_FAIL,
  RE_PREFIX,
  classifyLine,
} from "./newman-log-lines.mjs";

let passed = 0;
function test(name, fn) {
  fn();
  passed++;
  console.log(`  ok - ${name}`);
}

// ----- the request-done summary ----------------------------------------------

// Every one of these was sampled out of tmp/newman-cli-*.log from a real sweep.
const REAL_SUMMARIES = [
  "[200 OK, 1.09kB, 116ms]",
  "[200 OK, 1.87kB, 4.9s]",
  "[200 OK, 10.02kB, 714ms]",
  "[400 Bad Request, 1.24kB, 340ms]",
  "[401 Unauthorized, 1.1kB, 88ms]",
  "[403 Forbidden, 1.1kB, 92ms]",
];

test("matches every status summary newman actually emits", () => {
  for (const s of REAL_SUMMARIES) {
    assert.ok(RE_REQUEST_DONE.test(s), `expected a match for ${s}`);
  }
});

// The regression this module exists for. `(?:\s+[A-Za-z]+)?` matched at most one
// word after the status number, so "400 Bad Request" fell through every rule and
// the request was counted as neither a pass nor a fail.
test("matches a multi-word status text, not just a single word", () => {
  assert.ok(RE_REQUEST_DONE.test("[400 Bad Request, 1.24kB, 340ms]"));
  assert.ok(RE_REQUEST_DONE.test("[500 Internal Server Error, 2kB, 10ms]"));
  assert.ok(RE_REQUEST_DONE.test("[503 Service Unavailable, 2kB, 10ms]"));
});

test("still matches with no status text at all", () => {
  assert.ok(RE_REQUEST_DONE.test("[204, 0B, 12ms]"));
});

test("accepts second- and millisecond durations and sized bodies", () => {
  assert.ok(RE_REQUEST_DONE.test("[200 OK, 512B, 9ms]"));
  assert.ok(RE_REQUEST_DONE.test("[200 OK, 1.5MB, 12.5s]"));
});

test("does not match prose that merely contains brackets", () => {
  assert.ok(!RE_REQUEST_DONE.test("[errored]"));
  assert.ok(!RE_REQUEST_DONE.test("expected [200] to equal [400]"));
});

// ----- the other shapes -------------------------------------------------------

test("reads a folder heading and its name", () => {
  assert.strictEqual("❏ 4. Bedrock (via /bedrock)".match(RE_FOLDER)[1], "4. Bedrock (via /bedrock)");
});

test("reads a request heading and its name", () => {
  assert.strictEqual(
    "↳ Prompt caching (cache_control: ephemeral)".match(RE_REQUEST)[1],
    "Prompt caching (cache_control: ephemeral)"
  );
});

test("recognises a method+url line for any verb", () => {
  assert.ok(RE_METHOD_URL.test("POST http://localhost:8080/openai/v1/chat/completions"));
  assert.ok(RE_METHOD_URL.test("delete https://example.test/v1/files/abc"));
  assert.ok(!RE_METHOD_URL.test("POSTMAN http://localhost:8080/x"));
});

test("recognises an errored request", () => {
  assert.ok(RE_REQUEST_ERRORED.test("  POST http://localhost:8080/x [errored]"));
});

test("reads a numbered assertion failure", () => {
  assert.strictEqual(
    "  1. expected 400 to be within 200..299".match(RE_ASSERT_FAIL)[1],
    "expected 400 to be within 200..299"
  );
});

test("splits a provider fork prefix from the line body", () => {
  const m = "[openai] ↳ chat".match(RE_PREFIX);
  assert.strictEqual(m[1], "openai");
  assert.strictEqual(m[2], "↳ chat");
});

// ----- classifyLine -----------------------------------------------------------

test("classifies each line shape", () => {
  assert.strictEqual(classifyLine("❏ 1. OpenAI"), "folder");
  assert.strictEqual(classifyLine("↳ chat"), "request");
  assert.strictEqual(classifyLine("  POST http://x/y [200 OK, 1kB, 10ms]"), "done");
  assert.strictEqual(classifyLine("  POST http://x/y [errored]"), "errored");
  assert.strictEqual(classifyLine("  POST http://x/y"), "method-url");
  assert.strictEqual(classifyLine("  1. expected 400 to be within 200..299"), "assert-fail");
  assert.strictEqual(classifyLine("  ✓  status is 200"), "other");
});

// A failing request's summary line is indented and starts with a status number,
// which is also the shape of a numbered assertion failure. Getting this order
// wrong would mark the request failed but never done, so it would count as
// neither a pass nor a fail.
test("reads a failing summary as done, not as an assertion failure", () => {
  assert.strictEqual(
    classifyLine("  POST http://x/y [400 Bad Request, 1.24kB, 340ms]"),
    "done"
  );
});

console.log(`\n${passed} passed`);
