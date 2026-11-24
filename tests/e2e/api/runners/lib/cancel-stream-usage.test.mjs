import test from "node:test";
import assert from "node:assert/strict";
import { generatedTextChars, generatedCharsInData, cancelledUsageFailure, charThresholdFor } from "./cancel-stream-usage.mjs";

test("counts deltas across neutral and native routes", () => {
  assert.equal(generatedTextChars({choices:[{delta:{content:"hello"}}]}),5);
  assert.equal(generatedTextChars({type:"content_block_delta",delta:{thinking:"think"}}),5);
  assert.equal(generatedTextChars({type:"response.output_text.delta",delta:"hello"}),5);
  assert.equal(generatedTextChars({candidates:[{content:{parts:[{text:"hello"}]}}]}),5);
  assert.equal(generatedTextChars({type:"message_start",message:{usage:{output_tokens:4}}}),0);
  assert.equal(generatedTextChars({type:"response.output_text.done",text:"hello"}),0);
});
test("positive cost alone cannot hide missing or stale output counts", () => {
  const row={cost:0.01,prompt_tokens:26,completion_tokens:0,total_tokens:26};
  assert.ok(cancelledUsageFailure(row,"openai"));
  assert.ok(cancelledUsageFailure({...row,completion_tokens:4,total_tokens:30},"anthropic"));
  assert.equal(cancelledUsageFailure({...row,completion_tokens:80,total_tokens:106},"anthropic"),undefined);
  assert.ok(cancelledUsageFailure({...row,completion_tokens:80,total_tokens:999},"openai"));
});
test("the character threshold applies only to SSE responses", () => {
  assert.equal(charThresholdFor("text/event-stream", 128), 128);
  assert.equal(charThresholdFor("text/event-stream; charset=utf-8", 128), 128);
  // Binary event streams carry no data: lines to count, so only the byte threshold applies.
  assert.equal(charThresholdFor("application/vnd.amazon.eventstream", 128), 0);
  assert.equal(charThresholdFor("application/octet-stream", 128), 0);
  assert.equal(charThresholdFor("", 128), 0);
  assert.equal(charThresholdFor(undefined, 128), 0);
  assert.equal(charThresholdFor("text/event-stream", 0), 0);
});
test("a data line that is not a JSON event counts no generated text", () => {
  assert.equal(generatedCharsInData('{"choices":[{"delta":{"content":"hello"}}]}'), 5);
  assert.equal(generatedCharsInData('{"type":"content_block_delta","delta":{"text":"hi"}}'), 2);
  // Keepalive text, a truncated event and JSON scalars must not abort the probe.
  assert.equal(generatedCharsInData("keepalive"), 0);
  assert.equal(generatedCharsInData('{"choices":[{"delta":{"content":"hel'), 0);
  assert.equal(generatedCharsInData("null"), 0);
  assert.equal(generatedCharsInData("42"), 0);
  assert.equal(generatedCharsInData('"text"'), 0);
});
