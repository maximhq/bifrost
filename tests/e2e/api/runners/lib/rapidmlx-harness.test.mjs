import assert from "node:assert/strict";
import fs from "node:fs";
import test from "node:test";

const collection = JSON.parse(
  fs.readFileSync(new URL("../../collections/provider-harness.json", import.meta.url), "utf8"),
);
const folder = collection.item.find((item) => item.name.includes("Rapid-MLX local provider"));
const previewScript = collection.event
  .find((event) => event.listen === "prerequest")
  .script.exec.join("\n")
  .split("// Skip [SKIP]")[0];

// The subset of Postman's assertion API used by these collection scripts.
function expect(value) {
  const chain = {
    eql: (expected) => assert.deepEqual(value, expected),
    include: (expected) => assert.ok(value.includes(expected)),
    match: (pattern) => assert.match(value, pattern),
    a: (type) => {
      assert.equal(Array.isArray(value) ? "array" : typeof value, type);
      return chain;
    },
  };
  chain.an = chain.a;
  for (const word of ["to", "be", "that", "is"]) chain[word] = chain;
  chain.not = {
    get empty() {
      assert.ok(value.length > 0);
    },
  };
  return chain;
}

function failures(row, body) {
  const errors = [];
  const pm = {
    response: {
      code: 200,
      json: () => body,
      text: () => (typeof body === "string" ? body : JSON.stringify(body)),
    },
    test: (_name, fn) => {
      try {
        fn();
      } catch (error) {
        errors.push(error);
      }
    },
    expect,
  };
  for (const event of row.event.filter((event) => event.listen === "test")) {
    new Function("pm", event.script.exec.join("\n"))(pm);
  }
  return errors;
}

const unary = folder.item.find((row) => row.name.includes("responses unary"));
const streaming = folder.item.find((row) => row.name.includes("responses streaming"));
const sse = (...events) =>
  events.map((event) => "data: " + JSON.stringify(event) + "\n\n").join("");
const completed = { type: "response.completed" };

test("all five Rapid-MLX requests skip unless include_preview is explicitly enabled", () => {
  assert.equal(folder.item.length, 5);
  for (const row of folder.item) {
    for (const flag of [undefined, "", "0", "true", "1"]) {
      let skipped = false;
      new Function("pm", previewScript)({
        info: { requestName: row.name },
        environment: { get: () => flag },
        variables: { get: () => undefined },
        execution: {
          skipRequest: () => {
            skipped = true;
          },
        },
      });
      assert.equal(skipped, flag !== "1", row.name + ": " + flag);
    }
  }
});

test("Rapid-MLX unary Responses require nonempty message output_text", () => {
  const answer = { type: "message", content: [{ type: "output_text", text: "Hello" }] };
  assert.equal(failures(unary, { output: [{ type: "reasoning", summary: [] }, answer] }).length, 0);
  for (const output of [
    [],
    [{ type: "reasoning", summary: [] }],
    [{ type: "message", content: [] }],
    [{ type: "message", content: [{ type: "output_text", text: "" }] }],
    [{ type: "message", content: [{ type: "reasoning_text", text: "Thinking" }] }],
    [{ type: "reasoning", content: [{ type: "output_text", text: "Thinking" }] }],
    [{ type: "message", content: [{ type: "output_text", text: 42 }] }],
  ]) {
    assert.ok(failures(unary, { output }).length > 0, JSON.stringify(output));
  }
});

test("Rapid-MLX Responses SSE requires answer-text deltas and completion events", () => {
  const answer = { type: "response.output_text.delta", delta: "Hello" };
  assert.equal(failures(streaming, sse(answer, completed)).length, 0);
  assert.equal(failures(streaming, sse(answer, completed).replaceAll("\n", "\r\n")).length, 0);
  for (const events of [
    [completed],
    [answer],
    [{ type: "response.reasoning_text.delta", delta: "Thinking" }, completed],
    [{ type: "response.output_text.delta", delta: "" }, completed],
    [{ type: "response.output_text.delta", delta: 42 }, completed],
    [answer, { type: "response.output_text.done", text: "response.completed" }],
  ]) {
    assert.ok(failures(streaming, sse(...events)).length > 0, JSON.stringify(events));
  }
});