// Unit tests for the stream-cancellation payload variable resolver. Run
// directly: `node resolve-variables.test.mjs`. No test framework needed (the
// tests/e2e/api dir has no test runner configured).
import assert from "node:assert";
import { resolveVariables } from "./resolve-variables.mjs";

let passed = 0;
function test(name, fn) {
  fn();
  passed++;
  console.log(`  ok - ${name}`);
}

// The reason AGENTS.md bans marshalling payloads through an intermediate map:
// field order reaches the provider and matters for backend validation and
// snapshot comparison. Pinned here so the resolver cannot regress into a shape
// that reorders, whichever rebuild mechanism it uses.
test("preserves payload key order", () => {
  const payload = { model: "m", messages: [{ role: "user", content: "hi" }], stream: true, max_tokens: 16 };
  assert.deepStrictEqual(Object.keys(resolveVariables(payload)), Object.keys(payload));
});

test("preserves key order in nested objects", () => {
  const payload = { model: "m", options: { temperature: 0, top_p: 1, seed: 7 } };
  const resolved = resolveVariables(payload);
  assert.deepStrictEqual(Object.keys(resolved), ["model", "options"]);
  assert.deepStrictEqual(Object.keys(resolved.options), ["temperature", "top_p", "seed"]);
});

test("preserves key order inside arrays of objects", () => {
  const payload = { messages: [{ role: "user", content: "hi" }] };
  assert.deepStrictEqual(Object.keys(resolveVariables(payload).messages[0]), ["role", "content"]);
});

test("substitutes the azure deployment placeholder", () => {
  const prev = process.env.AZURE_DEPLOYMENT;
  process.env.AZURE_DEPLOYMENT = "my-deployment";
  try {
    assert.strictEqual(resolveVariables("{{azureDeployment}}"), "my-deployment");
    assert.strictEqual(resolveVariables({ model: "{{azureDeployment}}" }).model, "my-deployment");
  } finally {
    if (prev === undefined) delete process.env.AZURE_DEPLOYMENT;
    else process.env.AZURE_DEPLOYMENT = prev;
  }
});

test("leaves non-string scalars alone", () => {
  assert.strictEqual(resolveVariables(16), 16);
  assert.strictEqual(resolveVariables(true), true);
  assert.strictEqual(resolveVariables(null), null);
});

test("does not mutate the input payload", () => {
  const payload = { model: "m", nested: { a: 1 } };
  const resolved = resolveVariables(payload);
  resolved.nested.a = 2;
  assert.strictEqual(payload.nested.a, 1, "resolveVariables must return a fresh object graph");
});

console.log(`\n${passed} passed`);
