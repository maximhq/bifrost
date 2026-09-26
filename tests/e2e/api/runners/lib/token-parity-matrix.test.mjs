import assert from "node:assert/strict";
import test from "node:test";
import vm from "node:vm";
import { buildTokenParityMatrix } from "./token-parity-matrix.mjs";

// Feed real generated Postman scripts a metadata frame, rather than reimplementing
// their usage arithmetic. The decoder ignores CRCs; padding reserves their bytes.
function metadataFrame(usage) {
  const name = Buffer.from(":event-type");
  const value = Buffer.from("metadata");
  const headers = Buffer.concat([Buffer.from([name.length]), name, Buffer.from([7, 0, value.length]), value]);
  const payload = Buffer.from(JSON.stringify({ usage }));
  const frame = Buffer.alloc(16 + headers.length + payload.length);
  frame.writeUInt32BE(frame.length, 0);
  frame.writeUInt32BE(headers.length, 4);
  headers.copy(frame, 12);
  payload.copy(frame, 12 + headers.length);
  return frame;
}

const items = buildTokenParityMatrix().item;
function extractUsage(backend, leg, model, streaming, usage) {
  const modality = streaming ? "text_streaming" : "text";
  const item = items.find(item => item.name === `Token parity: ${backend}/${modality} - ${leg} r1`);
  assert.ok(item, "generated Bedrock case must exist");
  const script = item.event.find(event => event.listen === "test").script.exec.join("\n");
  const stored = new Map();
  vm.runInNewContext(script, {
    Buffer,
    pm: {
      response: { code: 200, stream: metadataFrame(usage), json: () => ({ usage }), text: () => "fixture" },
      test: () => {},
      variables: { get: () => model },
      collectionVariables: { set: (key, value) => stored.set(key, value) },
    },
  });
  return JSON.parse(stored.get(`pt_${backend}_${modality}_${leg}_r1`));
}

for (const leg of ["direct", "bifrost"]) {
  for (const [label, model, backend] of [
    ["Claude", "global.anthropic.claude-sonnet-4-6", "bedrock"],
    ["Nova", "amazon.nova-pro-v1:0", "bedrock"],
    ["older OpenAI", "openai.gpt-oss-120b-1:0", "bedrock_openai"],
  ]) {
    for (const cacheField of ["cacheReadInputTokens", "cacheWriteInputTokens"]) {
      for (const input of [2000, 4096, 5000]) {
        test(`${leg} ${label} ${cacheField}, uncached=${input}: stream agrees with unary`, () => {
          const usage = { inputTokens: input, outputTokens: 12, [cacheField]: 4096, totalTokens: input + 4096 + 12 };
          for (const stream of [false, true]) {
            const result = extractUsage(backend, leg, model, stream, usage);
            assert.equal(result.prompt, input + 4096);
            assert.equal(result.completion, 12);
            assert.equal(result.cached, cacheField === "cacheReadInputTokens" ? 4096 : 0);
          }
        });
      }
    }
  }
  test(`${leg} GPT-5.6 retains its inclusive streaming-input workaround`, () => {
    const result = extractUsage("bedrock_openai", leg, "us.openai.gpt-5.6-sol", true,
      { inputTokens: 4995, outputTokens: 19, cacheWriteInputTokens: 4993, totalTokens: 10007 });
    assert.equal(result.prompt, 4995);
  });
  test(`${leg} uncached streaming input stays unchanged`, () => {
    assert.equal(extractUsage("bedrock", leg, "global.anthropic.claude-sonnet-4-6", true,
      { inputTokens: 5000, outputTokens: 12 }).prompt, 5000);
  });
}
