// Unit tests for the pool-health probe's verdict. Run directly:
// `node stream-probe-verdict.test.mjs`. No test framework needed (the
// tests/e2e/api dir has no test runner configured).
import assert from "node:assert";
import { evaluateProbeStream } from "./stream-probe-verdict.mjs";

let passed = 0;
function test(name, fn) {
  fn();
  passed++;
  console.log(`  ok - ${name}`);
}

const SSE_BODY = 'data: {"id":"1"}\n\ndata: [DONE]\n\n';
const NON_SSE_BODY = '{"error":{"message":"provider returned non-SSE response"}}';
// What TextDecoder makes of binary event-stream frames: replacement characters
// around the framing bytes, with no SSE field name anywhere in it.
const BINARY_BODY = "�� event�chunk�";

test("accepts well-formed SSE", () => {
  const verdict = evaluateProbeStream({
    contentType: "text/event-stream",
    bytesRead: SSE_BODY.length,
    text: SSE_BODY,
  });
  assert.strictEqual(verdict.ok, true);
  assert.strictEqual(verdict.error, undefined);
});

// The regression signature this probe exists to catch: a 200 whose body is not
// SSE, because the reader was handed a connection another request had already
// consumed from (maximhq/bifrost#6143).
test("rejects a 200 that is not a stream at all", () => {
  const verdict = evaluateProbeStream({
    contentType: "application/json",
    bytesRead: NON_SSE_BODY.length,
    text: NON_SSE_BODY,
  });
  assert.strictEqual(verdict.ok, false);
});

// Losing the body from the failure message would defeat the probe: the whole
// point is to say WHAT came back instead of SSE, not just that it was wrong.
test("keeps the body sample in the error for a non-streaming response", () => {
  const verdict = evaluateProbeStream({
    contentType: "application/json",
    bytesRead: NON_SSE_BODY.length,
    text: NON_SSE_BODY,
  });
  assert.match(verdict.error, /provider returned non-SSE response/);
  assert.match(verdict.error, /content-type=application\/json/);
});

test("rejects SSE-typed content with no SSE markers", () => {
  const verdict = evaluateProbeStream({
    contentType: "text/event-stream",
    bytesRead: 14,
    text: "not sse at all",
  });
  assert.strictEqual(verdict.ok, false);
});

// streamingType accepts these two, so requiring SSE text markers of them made
// them unpassable: both carry binary frames, and a healthy response decodes to
// text with no `data:` in it. That recorded a false probe failure and exited the
// runner unsuccessfully.
for (const contentType of [
  "application/vnd.amazon.eventstream",
  "application/octet-stream",
]) {
  test(`accepts a non-empty binary stream (${contentType})`, () => {
    const verdict = evaluateProbeStream({
      contentType,
      bytesRead: 512,
      text: BINARY_BODY,
    });
    assert.strictEqual(
      verdict.ok,
      true,
      `binary streaming types must not be held to SSE markers: ${verdict.error}`
    );
  });

  // Dropping the SSE requirement must not turn the probe into a rubber stamp: a
  // stream that yields nothing is still a failure, which is what bytesRead > 0
  // holds on to.
  test(`rejects an empty binary stream (${contentType})`, () => {
    const verdict = evaluateProbeStream({ contentType, bytesRead: 0, text: "" });
    assert.strictEqual(verdict.ok, false);
  });
}

test("rejects an empty SSE-typed stream", () => {
  const verdict = evaluateProbeStream({
    contentType: "text/event-stream",
    bytesRead: 0,
    text: "",
  });
  assert.strictEqual(verdict.ok, false);
});

test("reports an empty content-type readably", () => {
  const verdict = evaluateProbeStream({ contentType: "", bytesRead: 0, text: "" });
  assert.strictEqual(verdict.ok, false);
  assert.match(verdict.error, /content-type=<empty>/);
});

console.log(`\n${passed} passed`);
