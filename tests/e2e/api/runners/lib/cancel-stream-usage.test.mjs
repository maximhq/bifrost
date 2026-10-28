import test from "node:test";
import assert from "node:assert/strict";
import { generatedTextChars, cancelledUsageFailure } from "./cancel-stream-usage.mjs";

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
