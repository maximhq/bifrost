// Unit tests for the chained-request dependency graph. Run directly: `node chained-vars.test.mjs`.
// No test framework needed (the tests/e2e/api dir has no test runner configured), same shape as
// ci-interval.test.mjs next door.
import assert from "node:assert";
import { walkRequests, buildProducerIndex, chainedDependencies, injectChainedVarGuards, varsSetBy } from "./chained-vars.mjs";

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

// Mirrors how filter-collection.mjs turns a dependency into a request object:
// use a direct reference when the dependency carries one, and otherwise fall
// back to the by-name lookup, where the first request claiming a name wins.
const resolveProducer = (dep, entries) => {
  if (dep.producerItem) return dep.producerItem;
  const byName = new Map();
  for (const { item } of entries) if (!byName.has(item.name)) byName.set(item.name, item);
  return byName.get(dep.producer);
};

const guardOf = (item) =>
  (item.event || []).find((e) => e.listen === "prerequest")?.script.exec.join("\n") || "";

test("walkRequests flattens folders and keeps collection order", () => {
  assert.deepStrictEqual(
    walkRequests(chain().item).map(({ item }) => item.name),
    ["r1", "r2", "r3", "unrelated"]
  );
});

// A request whose only body is a URL segment. The Responses lifecycle rows are shaped this
// way: create POSTs and captures resp_..., then retrieve/delete are GET/DELETE with NO body
// at all and the id sitting in the path. Postman stores both a pre-joined `raw` and the
// exploded host/path/query, and hand-written rows in the collection carry either or both.
const urlReq = (name, url, testScript) => ({
  name,
  ...(testScript ? { event: [{ listen: "test", script: { type: "text/javascript", exec: [testScript] } }] } : {}),
  request: { method: "GET", url },
});

// The exact shape that shipped 6 harness failures: the producer POSTs and captures the id,
// the three consumers reference it from the path only.
const lifecycle = () => ({
  item: [
    {
      name: "lifecycle",
      item: [
        req("create", '{"model": "gpt-4o-mini"}', 'pm.collectionVariables.set("lcRespId", "resp_1");'),
        // Object URL with both raw and exploded path, as the collection writes it.
        urlReq("retrieve", {
          raw: "{{baseUrl}}/openai/v1/responses/{{lcRespId}}",
          host: ["{{baseUrl}}"],
          path: ["openai", "v1", "responses", "{{lcRespId}}"],
        }),
        // Query-string carrier, plus an env var in the path that must stay unclaimed.
        urlReq("stream retrieve", {
          raw: "{{baseUrl}}/openai/v1/responses/{{lcRespId}}?stream=true&starting_after=0",
          host: ["{{baseUrl}}"],
          path: ["openai", "v1", "responses", "{{lcRespId}}"],
          query: [
            { key: "stream", value: "true" },
            { key: "after", value: "{{lcRespId}}" },
          ],
        }),
        // String URL - Postman's other legal form, and what a hand-edited row often uses.
        urlReq("delete", "{{baseUrl}}/openai/v1/responses/{{lcRespId}}"),
      ],
    },
  ],
});

test("a {{var}} in an object URL links to its producer even with no request body", () => {
  const entries = walkRequests(lifecycle().item);
  const index = buildProducerIndex(entries);
  const byName = Object.fromEntries(entries.map(({ item }) => [item.name, item]));
  const deps = chainedDependencies(byName.retrieve, index);
  assert.deepStrictEqual(
    deps.map(({ variable, producer }) => ({ variable, producer })),
    [{ variable: "lcRespId", producer: "create" }]
  );
  assert.strictEqual(deps[0].producerItem, byName.create);
});

test("a {{var}} in a string URL links to its producer", () => {
  const entries = walkRequests(lifecycle().item);
  const byName = Object.fromEntries(entries.map(({ item }) => [item.name, item]));
  assert.deepStrictEqual(
    chainedDependencies(byName.delete, buildProducerIndex(entries)).map((d) => d.variable),
    ["lcRespId"]
  );
});

// url.raw is not load-bearing: a row built programmatically may carry only the exploded
// parts. Scanning raw alone would silently drop those.
test("a {{var}} in an exploded path or query is found without url.raw", () => {
  const noRaw = {
    item: [
      req("create", "{}", 'pm.collectionVariables.set("fileId", "f_1");'),
      urlReq("in path", { host: ["{{baseUrl}}"], path: ["v1", "files", "{{fileId}}"] }),
      urlReq("in query", { host: ["{{baseUrl}}"], path: ["v1", "files"], query: [{ key: "id", value: "{{fileId}}" }] }),
    ],
  };
  const entries = walkRequests(noRaw.item);
  const index = buildProducerIndex(entries);
  for (const { item } of entries.slice(1)) {
    assert.deepStrictEqual(
      chainedDependencies(item, index).map((d) => d.variable),
      ["fileId"],
      `${item.name} lost its URL dependency`
    );
  }
});

test("each URL consumer is guarded, and the producer is not", () => {
  const c = lifecycle();
  assert.strictEqual(injectChainedVarGuards(c), 3);
  const byName = Object.fromEntries(walkRequests(c.item).map(({ item }) => [item.name, item]));
  assert.strictEqual(guardOf(byName.create), "");
  assert.match(guardOf(byName.retrieve), /lcRespId <- \\"create\\"/);
  // {{baseUrl}} is an environment variable no request produces; guarding on it would make
  // every row in the collection skip itself.
  assert.doesNotMatch(guardOf(byName["stream retrieve"]), /baseUrl/);
});

test("bodyDependencies links a {{var}} body to the request whose script sets it", () => {
  const entries = walkRequests(chain().item);
  const index = buildProducerIndex(entries);
  const byName = Object.fromEntries(entries.map(({ item }) => [item.name, item]));
  const named = (item) => chainedDependencies(item, index).map(({ variable, producer }) => ({ variable, producer }));
  assert.deepStrictEqual(named(byName.r2), [{ variable: "r2body", producer: "r1" }]);
  assert.deepStrictEqual(named(byName.r3), [{ variable: "r3body", producer: "r2" }]);
  // The name is for the log line; producerItem is what callers act on, so it
  // has to point at the request itself and not merely agree by name.
  assert.strictEqual(chainedDependencies(byName.r2, index)[0].producerItem, byName.r1);
  assert.strictEqual(chainedDependencies(byName.r3, index)[0].producerItem, byName.r2);
});

test("a {{var}} nobody sets is not a dependency", () => {
  // {{baseUrl}} comes from the environment, not from another request - pulling in a producer
  // for it (or guarding on it) would be wrong.
  const entries = walkRequests(chain().item);
  const byName = Object.fromEntries(entries.map(({ item }) => [item.name, item]));
  assert.deepStrictEqual(chainedDependencies(byName.unrelated, buildProducerIndex(entries)), []);
});

test("a request that sets and reads the same variable does not depend on itself", () => {
  const c = { item: [req("self", '{"t": {{v}}}', 'pm.collectionVariables.set("v", "1");')] };
  const entries = walkRequests(c.item);
  assert.deepStrictEqual(chainedDependencies(entries[0].item, buildProducerIndex(entries)), []);
});

// Request names are not unique - provider-harness.json carries 107 duplicated
// names across its 1280 requests, because a name is usually just
// "<provider>/<model>". Resolving a producer through a name therefore picks
// whichever request happened to claim that name first, which need not be the
// one whose script sets the variable. The consumer then has a prerequisite
// pulled in that produces nothing, while the real producer is left out - the
// exact breakage expandWithProducers exists to prevent, reintroduced silently.
test("a duplicated request name resolves to the request that actually sets the variable", () => {
  const dup = {
    item: [
      req("openai/gpt-4o-mini", '{"turns": [1]}'),
      req("openai/gpt-4o-mini", '{"turns": [2]}', 'pm.collectionVariables.set("nextBody", "[]");'),
      req("consumer", '{"turns": {{nextBody}}}'),
    ],
  };
  const entries = walkRequests(dup.item);
  const consumer = entries[2].item;
  const [dep] = chainedDependencies(consumer, buildProducerIndex(entries));

  assert.ok(dep, "consumer lost its dependency entirely");
  const resolved = resolveProducer(dep, entries);
  assert.ok(
    varsSetBy(resolved).has("nextBody"),
    `resolved producer ${JSON.stringify(resolved.request.body.raw)} does not set nextBody`
  );
  assert.strictEqual(resolved, entries[1].item);
});

test("guards exactly the consumers, naming the producer", () => {
  const c = chain();
  assert.strictEqual(injectChainedVarGuards(c), 2);
  const byName = Object.fromEntries(walkRequests(c.item).map(({ item }) => [item.name, item]));
  assert.strictEqual(guardOf(byName.r1), "");
  assert.strictEqual(guardOf(byName.unrelated), "");
  assert.match(guardOf(byName.r2), /r2body/);
  assert.match(guardOf(byName.r2), /produced by: " \+ "r2body <- \\"r1\\"/);
  assert.match(guardOf(byName.r2), /skipRequest/);
});

test("re-injecting replaces the guard instead of stacking a second copy", () => {
  const c = chain();
  injectChainedVarGuards(c);
  injectChainedVarGuards(c);
  const r2 = walkRequests(c.item).find(({ item }) => item.name === "r2").item;
  assert.strictEqual(guardOf(r2).match(/chained-var-guard/g).length, 1);
  assert.strictEqual(r2.event.filter((e) => e.listen === "prerequest").length, 1);
});

test("an existing pre-request script is kept, with the guard ahead of it", () => {
  const c = chain();
  const r2 = c.item[0].item[1];
  r2.event.unshift({ listen: "prerequest", script: { type: "text/javascript", exec: ["var setup = 1;"] } });
  injectChainedVarGuards(c);
  const exec = r2.event.find((e) => e.listen === "prerequest").script.exec;
  assert.ok(exec.indexOf("// [chained-var-guard]") < exec.indexOf("var setup = 1;"));
  assert.strictEqual(r2.event.filter((e) => e.listen === "prerequest").length, 1);
});

console.log(`\n${passed} passed`);
