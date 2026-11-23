import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import fs from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
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

function failures(row, body, code = 200) {
  const errors = [];
  const pm = {
    response: {
      code,
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

test("Rapid-MLX management URL checks inherit dev's genuine setup-token authentication", () => {
  // The default harness profile has no dashboard account. Like the Azure
  // endpoint-update rows, these requests authenticate through the collection's
  // injected setup token; adding a separate login would change that contract.
  const scratch = fs.mkdtempSync(join(tmpdir(), "bifrost-rapidmlx-harness-"));
  try {
    const out = scratch + "/augmented.json";
    execFileSync(process.execPath, [
      fileURLToPath(new URL("../augment-provider-harness.mjs", import.meta.url)),
      "--source", fileURLToPath(new URL("../../collections/provider-harness.json", import.meta.url)),
      "--out", out,
    ], { stdio: "pipe" });
    const filtered = scratch + "/filtered.json";
    execFileSync(process.execPath, [
      fileURLToPath(new URL("../filter-collection.mjs", import.meta.url)),
      "--source", out, "--out", filtered, "--feature", "rapidmlx local provider",
    ], { stdio: "pipe" });
    const augmented = JSON.parse(fs.readFileSync(filtered, "utf8"));
    const augmentedFolder = augmented.item.find((item) => item.name === folder.name);
    const management = augmentedFolder.item.filter((row) => row.request.url === "{{baseUrl}}/api/providers/rapidmlx/keys");
    assert.equal(management.length, 3);
    assert.deepEqual(augmentedFolder, folder);
    for (const [index, component] of ["userinfo", "query", "fragment"].entries()) {
      const row = management[index];
      const headers = new Map(row.request.header.map(({ key, value }) => [key, value]));
      const variables = new Map((augmented.variable || []).map(({ key, value }) => [key, value]));
      assert.ok(variables.get("setupToken"), "the default harness must supply a setup token");
      variables.set("setupToken", "rapidmlx-test-setup-token");
      const pm = {
        info: { requestName: row.name },
        environment: { get: (key) => key === "include_preview" ? "1" : undefined },
        variables: { get: (key) => variables.get(key) },
        collectionVariables: { get: (key) => variables.get(key), set: (key, value) => variables.set(key, value) },
        request: { headers: { upsert: ({ key, value }) => headers.set(key, value) } },
      };
      for (const event of [...augmented.event, ...(augmentedFolder.event || []), ...row.event]) {
        if (event.listen === "prerequest") new Function("pm", event.script.exec.join("\n"))(pm);
      }
      assert.equal(headers.get("X-Bifrost-Setup-Token"), "rapidmlx-test-setup-token");
      assert.match(row.name, /\[EXPECT-400\]/);
      assert.equal(failures(row, "Invalid key URL: " + component, 400).length, 0);
      assert.ok(failures(row, "Invalid key URL: " + component, 403).length > 0);
      assert.ok(failures(row, "Invalid key URL", 400).length > 0);
    }
  } finally {
    fs.rmSync(scratch, { recursive: true, force: true });
  }
});

test("all seven Rapid-MLX requests skip unless include_preview is explicitly enabled", () => {
  assert.equal(folder.item.length, 7);
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
