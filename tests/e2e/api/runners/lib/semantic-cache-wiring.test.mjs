// Unit tests for the semantic cache wiring sandbox library. Run directly:
// `node semantic-cache-wiring.test.mjs`. No test framework needed (the tests/e2e/api dir has no
// test runner configured).
import assert from "node:assert";
import { readFileSync } from "node:fs";
import { scwLib, scwLibSource, scwNamespaceSuffix } from "./semantic-cache-wiring.mjs";

let passed = 0;
function test(name, fn) {
  fn();
  passed++;
  console.log(`  ok - ${name}`);
}

// A Postman sandbox stand-in: one response, collection variables, and the recorded pm.test calls.
function fakePm({ code = 200, body = "", headers = {}, requestName = "req", vars = {} } = {}) {
  const store = new Map(Object.entries(vars));
  const tests = [];
  let next = null;
  const lower = {};
  for (const [k, v] of Object.entries(headers)) lower[k.toLowerCase()] = v;
  const pm = {
    response: { code, text: () => body, headers: { get: (n) => lower[String(n).toLowerCase()] } },
    collectionVariables: { get: (k) => store.get(k), set: (k, v) => store.set(k, v) },
    variables: { replaceIn: (s) => s.replace(/\{\{(\w+)\}\}/g, (_, k) => store.get(k) ?? "") },
    info: { requestName },
    execution: { setNextRequest: (n) => (next = n) },
    test: (name, fn) => {
      try {
        fn();
        tests.push({ name, ok: true });
      } catch (e) {
        tests.push({ name, ok: false, error: e.message });
      }
    },
  };
  return { pm, store, tests, next: () => next };
}

const JSON_CT = { "content-type": "application/json" };
const SSE_CT = { "content-type": "text/event-stream" };
const MISS = { cache_hit: false, cache_id: "id-1" };
const DIRECT = { cache_hit: true, cache_id: "id-1", hit_type: "direct", requested_provider: "openai", requested_model: "gpt-4o-mini", cache_hit_latency: 3 };
const SEMANTIC = {
  cache_hit: true,
  cache_id: "id-1",
  hit_type: "semantic",
  requested_provider: "openai",
  requested_model: "gpt-4o-mini",
  provider_used: "scw-embed",
  model_used: "text-embedding-3-small",
  input_tokens: 13,
  threshold: 0.85,
  similarity: 0.9190652966499,
  cache_hit_latency: 491,
};

function chatBody(cd) {
  return JSON.stringify({ choices: [{ message: { content: "hi" } }], extra_fields: cd ? { cache_debug: cd } : {} });
}
function cacheHeaders(cd) {
  const h = { "x-bifrost-cache-hit": String(cd.cache_hit), "x-bifrost-cache-id": cd.cache_id };
  if (cd.hit_type) h["x-bifrost-cache-hit-type"] = cd.hit_type;
  if (cd.cache_hit_latency !== undefined) h["x-bifrost-cache-hit-latency-ms"] = String(cd.cache_hit_latency);
  if (cd.requested_provider) h["x-bifrost-cache-requested-provider"] = cd.requested_provider;
  if (cd.requested_model) h["x-bifrost-cache-requested-model"] = cd.requested_model;
  if (cd.similarity !== undefined) h["x-bifrost-cache-similarity"] = String(cd.similarity);
  if (cd.threshold !== undefined) h["x-bifrost-cache-threshold"] = String(cd.threshold);
  if (cd.provider_used) h["x-bifrost-cache-provider-used"] = cd.provider_used;
  if (cd.model_used) h["x-bifrost-cache-model-used"] = cd.model_used;
  if (cd.input_tokens !== undefined) h["x-bifrost-cache-input-tokens"] = String(cd.input_tokens);
  return h;
}
function chatStream(cd, deltas = ["One", ", two"]) {
  const frames = deltas.map((d) => `data: ${JSON.stringify({ choices: [{ delta: { content: d } }], extra_fields: {} })}`);
  frames.push(`data: ${JSON.stringify({ choices: [{ delta: {}, finish_reason: "stop" }], extra_fields: { cache_debug: cd } })}`);
  frames.push("data: [DONE]");
  return frames.join("\n\n") + "\n\n";
}

test("namespace suffix keeps only alphanumerics", () => {
  assert.strictEqual(scwNamespaceSuffix("ci-redis-lz3k9-4f2a"), "ciredislz3k94f2a");
  assert.strictEqual(scwNamespaceSuffix(""), "");
});

test("the serialized library evaluates to the same API", () => {
  const { pm } = fakePm();
  const scw = new Function("pm", scwLibSource() + "\nreturn scw;")(pm);
  assert.strictEqual(typeof scw.run, "function");
  assert.strictEqual(typeof scw.waitFirst, "function");
});

test("parseSse reads data frames, event names and the [DONE] terminator", () => {
  const scw = scwLib(fakePm().pm);
  const chat = scw.parseSse(chatStream(MISS));
  assert.strictEqual(chat.events.length, 3);
  assert.strictEqual(chat.done, true);
  const responses = scw.parseSse('event: response.created\ndata: {"type":"response.created"}\n\nevent: response.completed\ndata: {"type":"response.completed","extra_fields":{"cache_debug":{"cache_hit":false,"cache_id":"x"}}}\n\n');
  assert.deepStrictEqual(responses.types, ["response.created", "response.completed"]);
  assert.strictEqual(responses.done, false);
});

test("a miss saves its cache_id and request id", () => {
  const f = fakePm({ body: chatBody(MISS), headers: { ...JSON_CT, ...cacheHeaders(MISS), "x-bifrost-request-id": "req-1" } });
  scwLib(f.pm).run({ name: "A", status: 200, outcome: "miss", save: "d1", saveReq: "d1a" });
  assert.deepStrictEqual(f.tests, [{ name: "A", ok: true }]);
  assert.strictEqual(f.store.get("cid_d1"), "id-1");
  assert.strictEqual(f.store.get("rid_d1a"), "req-1");
});

test("a direct hit passes against the saved id and fails against a different one", () => {
  const ok = fakePm({ body: chatBody(DIRECT), headers: { ...JSON_CT, ...cacheHeaders(DIRECT) }, vars: { cid_d1: "id-1" } });
  scwLib(ok.pm).run({ name: "B", status: 200, outcome: "direct", same: "d1" });
  assert.strictEqual(ok.tests[0].ok, true, ok.tests[0].error);
  const bad = fakePm({ body: chatBody(DIRECT), headers: { ...JSON_CT, ...cacheHeaders(DIRECT) }, vars: { cid_d1: "id-1" } });
  scwLib(bad.pm).run({ name: "B", status: 200, outcome: "direct", distinct: ["d1"] });
  assert.strictEqual(bad.tests[0].ok, false);
  assert.match(bad.tests[0].error, /should differ from d1/);
});

test("a direct hit carrying embedding fields fails", () => {
  const cd = { ...DIRECT, provider_used: "scw-embed" };
  const f = fakePm({ body: chatBody(cd), headers: { ...JSON_CT, ...cacheHeaders(cd) } });
  scwLib(f.pm).run({ name: "B", outcome: "direct" });
  assert.match(f.tests[0].error, /expected no provider_used/);
});

test("a semantic hit needs similarity at or above threshold and the embedding fields", () => {
  const f = fakePm({ body: chatBody(SEMANTIC), headers: { ...JSON_CT, ...cacheHeaders(SEMANTIC) } });
  scwLib(f.pm).run({ name: "S2", outcome: "semantic", threshold: 0.85, headersMatch: "all" });
  assert.strictEqual(f.tests[0].ok, true, f.tests[0].error);
  const low = { ...SEMANTIC, similarity: 0.5 };
  const g = fakePm({ body: chatBody(low), headers: { ...JSON_CT, ...cacheHeaders(low) } });
  scwLib(g.pm).run({ name: "S2", outcome: "semantic" });
  assert.match(g.tests[0].error, /below threshold/);
});

test("headers that disagree with the body fail the default check", () => {
  const f = fakePm({ body: chatBody(DIRECT), headers: { ...JSON_CT, ...cacheHeaders({ ...DIRECT, cache_id: "other" }) } });
  scwLib(f.pm).run({ name: "B", outcome: "direct" });
  assert.match(f.tests[0].error, /x-bifrost-cache-id other disagrees/);
});

test("headersMatch all compares every field, and stream mode skips the latency", () => {
  const off = { ...cacheHeaders(DIRECT), "x-bifrost-cache-hit-latency-ms": "9" };
  const all = fakePm({ body: chatBody(DIRECT), headers: { ...JSON_CT, ...off } });
  scwLib(all.pm).run({ name: "K1", outcome: "direct", headersMatch: "all" });
  assert.match(all.tests[0].error, /hit-latency-ms = 9/);
  const stream = fakePm({ body: chatStream(DIRECT), headers: { ...SSE_CT, ...off } });
  scwLib(stream.pm).run({ name: "K2", outcome: "direct", headersMatch: "stream" });
  assert.strictEqual(stream.tests[0].ok, true, stream.tests[0].error);
  const missing = { ...cacheHeaders(DIRECT) };
  delete missing["x-bifrost-cache-requested-model"];
  const m = fakePm({ body: chatBody(DIRECT), headers: { ...JSON_CT, ...missing } });
  scwLib(m.pm).run({ name: "K1", outcome: "direct", headersMatch: "all" });
  assert.match(m.tests[0].error, /requested_model .* header .* is missing/);
});

test("outcome none fails on a stray cache header", () => {
  const f = fakePm({ body: chatBody(null), headers: { ...JSON_CT, "x-bifrost-cache-hit": "false" } });
  scwLib(f.pm).run({ name: "D4", outcome: "none" });
  assert.match(f.tests[0].error, /no x-bifrost-cache-\* headers/);
});

test("header-sourced cache_debug parses types", () => {
  const f = fakePm({ body: "{}", headers: { ...JSON_CT, ...cacheHeaders(SEMANTIC) } });
  const cd = scwLib(f.pm).debugFromHeaders();
  assert.deepStrictEqual(cd, SEMANTIC);
});

test("a stream takes cache_debug from its final chunk, and finalOnly rejects an early one", () => {
  const f = fakePm({ body: chatStream(MISS), headers: { ...SSE_CT, ...cacheHeaders(MISS) } });
  scwLib(f.pm).run({ name: "T1", outcome: "miss", save: "t1", stream: { done: true, minChunks: 2, finalOnly: true, save: "t1" } });
  assert.strictEqual(f.tests[0].ok, true, f.tests[0].error);
  assert.deepStrictEqual(JSON.parse(f.store.get("st_t1")).texts, ["One", ", two", ""]);
  const early = chatStream(MISS).replace('"extra_fields":{}', `"extra_fields":{"cache_debug":${JSON.stringify(MISS)}}`);
  const g = fakePm({ body: early, headers: { ...SSE_CT, ...cacheHeaders(MISS) } });
  scwLib(g.pm).run({ name: "T1", outcome: "miss", stream: { finalOnly: true } });
  assert.match(g.tests[0].error, /chunk 0 carries cache_debug/);
});

test("a stream replay must match the saved chunks one by one", () => {
  const saved = JSON.stringify({ count: 3, types: ["", "", ""], texts: ["One", ", two", ""], shapes: ["{}", "{}", '{"finish_reason":"stop"}'] });
  const ok = fakePm({ body: chatStream(DIRECT), headers: { ...SSE_CT, ...cacheHeaders(DIRECT) }, vars: { st_t1: saved, cid_t1: "id-1" } });
  scwLib(ok.pm).run({ name: "T2", outcome: "direct", same: "t1", stream: { done: true, compare: "t1" } });
  assert.strictEqual(ok.tests[0].ok, true, ok.tests[0].error);
  const bad = fakePm({ body: chatStream(DIRECT, ["One", ", three"]), headers: { ...SSE_CT, ...cacheHeaders(DIRECT) }, vars: { st_t1: saved } });
  scwLib(bad.pm).run({ name: "T2", outcome: "direct", stream: { compare: "t1" } });
  assert.match(bad.tests[0].error, /chunk contents differ/);
});

test("a failing polled spec retries silently, then records the failure on the last attempt", () => {
  const f = fakePm({ body: chatBody(MISS), headers: { ...JSON_CT, ...cacheHeaders(MISS) }, requestName: "D1 B" });
  scwLib(f.pm).run({ name: "D1 B", poll: true, outcome: "direct" });
  assert.deepStrictEqual(f.tests, []);
  assert.strictEqual(f.next(), "D1 B");
  assert.strictEqual(f.store.get("__poll_D1 B"), "1");
  const last = fakePm({ body: chatBody(MISS), headers: { ...JSON_CT, ...cacheHeaders(MISS) }, requestName: "D1 B", vars: { "__poll_D1 B": "8" } });
  scwLib(last.pm).run({ name: "D1 B", poll: true, outcome: "direct" });
  assert.strictEqual(last.tests[0].ok, false);
  assert.strictEqual(last.next(), null);
  assert.strictEqual(last.store.get("__poll_D1 B"), "0");
});

test("saves happen only when the spec passes", () => {
  const f = fakePm({ body: chatBody(MISS), headers: { ...JSON_CT, ...cacheHeaders(MISS) }, vars: { "__poll_req": "8" } });
  scwLib(f.pm).run({ name: "A", poll: true, outcome: "direct", save: "x" });
  assert.strictEqual(f.store.get("cid_x"), undefined);
});

test("status, message and field checks", () => {
  const body = JSON.stringify({ error: { message: "Plugin already exists" }, plugin: { status: { types: ["llm", "http"] } } });
  const f = fakePm({ code: 409, body, headers: JSON_CT });
  scwLib(f.pm).run({ name: "M2", status: 409, message: "already exists", fields: [{ path: "plugin.status.types", contains: "llm" }] });
  assert.strictEqual(f.tests[0].ok, true, f.tests[0].error);
  const g = fakePm({ code: 500, body, headers: JSON_CT });
  scwLib(g.pm).run({ name: "M2", status: "4xx" });
  assert.match(g.tests[0].error, /status 500/);
});

test("saved fields and fingerprints compare by value", () => {
  const a = fakePm({ body: JSON.stringify({ data: [{ embedding: [0.1, 0.2] }], model: "m" }), headers: JSON_CT });
  scwLib(a.pm).run({ name: "X6 A", fields: [{ path: "model", save: "x6model" }], fingerprint: [{ path: "data.0.embedding", save: "x6" }] });
  const vars = Object.fromEntries(a.store);
  const b = fakePm({ body: JSON.stringify({ data: [{ embedding: [0.1, 0.2] }], model: "m" }), headers: JSON_CT, vars });
  scwLib(b.pm).run({ name: "X6 B", fields: [{ path: "model", equals: "x6model" }], fingerprint: [{ path: "data.0.embedding", equals: "x6" }] });
  assert.strictEqual(b.tests[0].ok, true, b.tests[0].error);
  const c = fakePm({ body: JSON.stringify({ data: [{ embedding: [0.1, 0.3] }] }), headers: JSON_CT, vars });
  scwLib(c.pm).run({ name: "X6 B", fingerprint: [{ path: "data.0.embedding", equals: "x6" }] });
  assert.match(c.tests[0].error, /fingerprint/);
});

// A missing payload is not a payload: an original and a replay that both lack the image must not
// compare equal, and nothing is saved for a field that is not there.
test("a fingerprint of a missing field fails instead of matching", () => {
  const noImage = JSON.stringify({ data: [{ revised_prompt: "an apple" }] });
  const save = fakePm({ body: noImage, headers: JSON_CT });
  scwLib(save.pm).run({ name: "X10 A", fingerprint: [{ path: "data.0.b64_json", save: "x10" }] });
  assert.strictEqual(save.tests[0].ok, false, "saving a fingerprint of a missing field passed");
  assert.strictEqual(save.store.get("fp_x10"), undefined);

  const vars = { fp_x10: scwLib(fakePm().pm).fingerprint(undefined) };
  const compare = fakePm({ body: noImage, headers: JSON_CT, vars });
  scwLib(compare.pm).run({ name: "X10 B", fingerprint: [{ path: "data.0.b64_json", equals: "x10" }] });
  assert.strictEqual(compare.tests[0].ok, false, "comparing fingerprints of a missing field passed");
  assert.match(compare.tests[0].error, /data\.0\.b64_json is missing/);
});

test("log rows: cache_debug, its equality with the response, and plugin_logs entries", () => {
  const pluginLogs = JSON.stringify({ semantic_cache: [{ level: "warn", message: "semantic search skipped: failed to generate embedding: 401" }] });
  const row = JSON.stringify({ status: "success", cache_debug: MISS, plugin_logs: pluginLogs });
  const f = fakePm({ body: row, headers: JSON_CT, vars: { cd_s26: JSON.stringify(MISS) } });
  scwLib(f.pm).run({
    name: "S26 log",
    source: "log",
    outcome: "miss",
    cdEquals: "s26",
    pluginLogs: { plugin: "semantic_cache", level: "warn", includes: "failed to generate embedding" },
  });
  assert.strictEqual(f.tests[0].ok, true, f.tests[0].error);
  const g = fakePm({ body: row, headers: JSON_CT });
  scwLib(g.pm).run({ name: "S26 repeat log", source: "log", pluginLogs: { plugin: "semantic_cache", level: "warn", absent: true } });
  assert.match(g.tests[0].error, /expected no semantic_cache warn entry/);
});

test("ids include and exclude saved request ids", () => {
  const body = JSON.stringify({ logs: [{ id: "r-hit" }, { id: "r-other" }] });
  const f = fakePm({ body, headers: JSON_CT, vars: { rid_hit: "r-hit", rid_miss: "r-miss" } });
  scwLib(f.pm).run({ name: "O2", ids: { path: "logs", include: ["hit"], exclude: ["miss"] } });
  assert.strictEqual(f.tests[0].ok, true, f.tests[0].error);
  const g = fakePm({ body, headers: JSON_CT, vars: { rid_hit: "r-miss", rid_miss: "r-hit" } });
  scwLib(g.pm).run({ name: "O2", ids: { path: "logs", include: ["hit"] } });
  assert.match(g.tests[0].error, /expected request hit/);
});

test("spec strings resolve {{variables}} before they are compared", () => {
  const body = JSON.stringify({ config: { provider: "scw-embed-run1" } });
  const f = fakePm({ body, headers: JSON_CT, vars: { run_id: "run1" } });
  scwLib(f.pm).run({ name: "S0", fields: [{ path: "config.provider", eq: "scw-embed-{{run_id}}" }] });
  assert.strictEqual(f.tests[0].ok, true, f.tests[0].error);
});

test("contentType checks the response media type", () => {
  const f = fakePm({ body: "ID3", headers: { "content-type": "audio/mpeg", ...cacheHeaders(MISS) } });
  scwLib(f.pm).run({ name: "X8 A", source: "headers", outcome: "miss", contentType: "audio/mpeg" });
  assert.strictEqual(f.tests[0].ok, true, f.tests[0].error);
  const g = fakePm({ body: "{}", headers: JSON_CT });
  scwLib(g.pm).run({ name: "X8 A", contentType: "audio/mpeg" });
  assert.match(g.tests[0].error, /content-type application\/json/);
});

test("headersMatch catches a missing or wrong semantic header", () => {
  const missing = { ...cacheHeaders(SEMANTIC) };
  delete missing["x-bifrost-cache-similarity"];
  const m = fakePm({ body: chatBody(SEMANTIC), headers: { ...JSON_CT, ...missing } });
  scwLib(m.pm).run({ name: "S2", outcome: "semantic", headersMatch: "all" });
  assert.match(m.tests[0].error, /similarity .* header x-bifrost-cache-similarity is missing/);
  const wrong = { ...cacheHeaders(SEMANTIC), "x-bifrost-cache-provider-used": "another-provider" };
  const w = fakePm({ body: chatBody(SEMANTIC), headers: { ...JSON_CT, ...wrong } });
  scwLib(w.pm).run({ name: "S2", outcome: "semantic", headersMatch: "all" });
  assert.match(w.tests[0].error, /x-bifrost-cache-provider-used = another-provider but cache_debug\.provider_used = scw-embed/);
});

// The generated semantic hits must compare every cache header with the body, not only hit and
// id: run the real S2 and S11 specs against a semantic hit whose similarity header is missing.
test("the generated semantic hits compare every cache header", () => {
  const collection = JSON.parse(readFileSync(new URL("../../collections/bifrost-semantic-cache-wiring.postman_collection.json", import.meta.url), "utf8"));
  const specOf = (name) => {
    const walk = (items) => {
      for (const it of items) {
        if (it.item) {
          const found = walk(it.item);
          if (found) return found;
        } else if (it.name === name) {
          const source = it.event.find((e) => e.listen === "test").script.exec.join("\n");
          return JSON.parse(source.slice(source.indexOf("scw.run(") + 8, source.lastIndexOf(");")));
        }
      }
      return null;
    };
    const spec = walk(collection.item);
    assert.ok(spec, `no request named ${name}`);
    return spec;
  };
  const cd = { ...SEMANTIC, provider_used: "scw-embed-r1" };
  const headers = { ...cacheHeaders(cd) };
  delete headers["x-bifrost-cache-similarity"];
  const vars = { run_id: "r1", cid_s1: cd.cache_id, cid_s11: cd.cache_id, st_s11: JSON.stringify({ count: 3, types: ["", "", ""], texts: ["One", ", two", ""] }) };

  const s2 = fakePm({ body: chatBody(cd), headers: { ...JSON_CT, ...headers }, vars, requestName: "S2" });
  scwLib(s2.pm).run({ ...specOf("S2 paraphrase is a semantic hit"), poll: false });
  assert.strictEqual(s2.tests[0].ok, false, "S2 passed with the similarity header missing");

  const s11 = fakePm({ body: chatStream(cd), headers: { ...SSE_CT, ...headers }, vars, requestName: "S11" });
  scwLib(s11.pm).run({ ...specOf("S11 paraphrase stream is a semantic hit"), poll: false });
  assert.strictEqual(s11.tests[0].ok, false, "S11 passed with the similarity header missing");
});

test("a stream replay that loses a tool call id, a finish_reason or usage fails", () => {
  const toolStream = (id, finish, usage) =>
    [
      `data: ${JSON.stringify({ choices: [{ delta: { tool_calls: [{ index: 0, id, type: "function", function: { name: "get_weather", arguments: "" } }] } }], extra_fields: {} })}`,
      `data: ${JSON.stringify({ choices: [{ delta: { tool_calls: [{ index: 0, function: { arguments: '{"city":"Paris"}' } }] } }], extra_fields: {} })}`,
      `data: ${JSON.stringify({ choices: [{ delta: {}, finish_reason: finish }], usage, extra_fields: { cache_debug: DIRECT } })}`,
      "data: [DONE]",
    ].join("\n\n") + "\n\n";
  const usage = { prompt_tokens: 9, completion_tokens: 5, total_tokens: 14 };
  const original = fakePm({ body: toolStream("call_1", "tool_calls", usage), headers: { ...SSE_CT, ...cacheHeaders(DIRECT) } });
  scwLib(original.pm).run({ name: "T6 A", stream: { save: "t6" } });
  const saved = original.store.get("st_t6");

  const replays = {
    "a lost tool call id": toolStream("", "tool_calls", usage),
    "another finish_reason": toolStream("call_1", "stop", usage),
    "lost usage": toolStream("call_1", "tool_calls", undefined),
  };
  for (const [what, body] of Object.entries(replays)) {
    const f = fakePm({ body, headers: { ...SSE_CT, ...cacheHeaders(DIRECT) }, vars: { st_t6: saved } });
    scwLib(f.pm).run({ name: "T6 B", stream: { compare: "t6" } });
    assert.strictEqual(f.tests[0].ok, false, `a replay with ${what} passed`);
  }
  const same = fakePm({ body: toolStream("call_1", "tool_calls", usage), headers: { ...SSE_CT, ...cacheHeaders(DIRECT) }, vars: { st_t6: saved } });
  scwLib(same.pm).run({ name: "T6 B", stream: { compare: "t6" } });
  assert.strictEqual(same.tests[0].ok, true, same.tests[0].error);
});

test("noBodyCacheDebug rejects a body that carries cache_debug", () => {
  const f = fakePm({ body: chatBody(MISS), headers: { ...JSON_CT, ...cacheHeaders(MISS) } });
  scwLib(f.pm).run({ name: "K3", source: "headers", outcome: "miss", noBodyCacheDebug: true });
  assert.match(f.tests[0].error, /must carry no cache_debug/);
});

console.log(`\n${passed} passed`);
