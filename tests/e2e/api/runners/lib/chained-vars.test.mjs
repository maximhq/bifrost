// Unit tests for the chained-request dependency graph. Run directly: `node chained-vars.test.mjs`.
// No test framework needed (the tests/e2e/api dir has no test runner configured), same shape as
// ci-interval.test.mjs next door.
import assert from "node:assert";
import { walkRequests, buildProducerIndex, bodyDependencies, injectChainedBodyGuards } from "./chained-vars.mjs";

let passed = 0;
function test(name, fn) {
  fn();
  passed++;
  console.log(`  ok - ${name}`);
}

const req = (name, raw, testScript) => ({
  name,
  ...(testScript ? { event: [{ listen: "test", script: { type: "text/javascript", exec: [testScript] } }] } : {}),
  request: { method: "POST", body: { mode: "raw", raw }, url: { raw: "http://x/y" } },
});

// Mirrors the token-parity matrix: r1 computes r2's turns, r2 computes r3's, and each later
// round's body is a bare {{var}} drop-in. The setter is gated on the response code, exactly as
// the generator emits it, so a failing round leaves the variable unset.
const chain = () => ({
  item: [
    {
      name: "folder",
      item: [
        req("r1", '{"turns": [1]}', 'if (pm.response.code < 400) { pm.collectionVariables.set("r2body", "[]"); }'),
        req("r2", '{"turns": {{r2body}}}', 'if (pm.response.code < 400) { pm.collectionVariables.set("r3body", "[]"); }'),
        req("r3", '{"turns": {{r3body}}}'),
        req("unrelated", '{"turns": [{{baseUrl}}]}'),
      ],
    },
  ],
});

const guardOf = (item) =>
  (item.event || []).find((e) => e.listen === "prerequest")?.script.exec.join("\n") || "";

test("walkRequests flattens folders and keeps collection order", () => {
  assert.deepStrictEqual(
    walkRequests(chain().item).map(({ item }) => item.name),
    ["r1", "r2", "r3", "unrelated"]
  );
});

test("bodyDependencies links a {{var}} body to the request whose script sets it", () => {
  const entries = walkRequests(chain().item);
  const index = buildProducerIndex(entries);
  const byName = Object.fromEntries(entries.map(({ item }) => [item.name, item]));
  assert.deepStrictEqual(bodyDependencies(byName.r2, index), [{ variable: "r2body", producer: "r1" }]);
  assert.deepStrictEqual(bodyDependencies(byName.r3, index), [{ variable: "r3body", producer: "r2" }]);
});

test("a {{var}} nobody sets is not a dependency", () => {
  // {{baseUrl}} comes from the environment, not from another request - pulling in a producer
  // for it (or guarding on it) would be wrong.
  const entries = walkRequests(chain().item);
  const byName = Object.fromEntries(entries.map(({ item }) => [item.name, item]));
  assert.deepStrictEqual(bodyDependencies(byName.unrelated, buildProducerIndex(entries)), []);
});

test("a request that sets and reads the same variable does not depend on itself", () => {
  const c = { item: [req("self", '{"t": {{v}}}', 'pm.collectionVariables.set("v", "1");')] };
  const entries = walkRequests(c.item);
  assert.deepStrictEqual(bodyDependencies(entries[0].item, buildProducerIndex(entries)), []);
});

test("guards exactly the consumers, naming the producer", () => {
  const c = chain();
  assert.strictEqual(injectChainedBodyGuards(c), 2);
  const byName = Object.fromEntries(walkRequests(c.item).map(({ item }) => [item.name, item]));
  assert.strictEqual(guardOf(byName.r1), "");
  assert.strictEqual(guardOf(byName.unrelated), "");
  assert.match(guardOf(byName.r2), /r2body/);
  assert.match(guardOf(byName.r2), /produced by: " \+ "r2body <- \\"r1\\"/);
  assert.match(guardOf(byName.r2), /skipRequest/);
});

test("re-injecting replaces the guard instead of stacking a second copy", () => {
  const c = chain();
  injectChainedBodyGuards(c);
  injectChainedBodyGuards(c);
  const r2 = walkRequests(c.item).find(({ item }) => item.name === "r2").item;
  assert.strictEqual(guardOf(r2).match(/chained-body-guard/g).length, 1);
  assert.strictEqual(r2.event.filter((e) => e.listen === "prerequest").length, 1);
});

test("an existing pre-request script is kept, with the guard ahead of it", () => {
  const c = chain();
  const r2 = c.item[0].item[1];
  r2.event.unshift({ listen: "prerequest", script: { type: "text/javascript", exec: ["var setup = 1;"] } });
  injectChainedBodyGuards(c);
  const exec = r2.event.find((e) => e.listen === "prerequest").script.exec;
  assert.ok(exec.indexOf("// [chained-body-guard]") < exec.indexOf("var setup = 1;"));
  assert.strictEqual(r2.event.filter((e) => e.listen === "prerequest").length, 1);
});

console.log(`\n${passed} passed`);
