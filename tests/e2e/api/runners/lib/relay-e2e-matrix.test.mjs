// Unit tests for the relay e2e matrix and helper prelude. Run: `node --test relay-e2e-matrix.test.mjs`.
import assert from "node:assert/strict";
import fs from "node:fs";
import test from "node:test";
import { routeScriptCallsThroughSetupToken } from "./setup-token.mjs";
import { HELPERS, HELPERS_MARKER, PREVIEW_CASES, RELAY_FOLDER_PREFIX, RELAY_MATRIX_BLOCKS, SUFFIX, VALIDATION_RULES, buildBlock, injectRelayHelpers, spliceRelayMatrix } from "./relay-e2e-matrix.mjs";

const SOURCE = new URL("../../collections/provider-harness.json", import.meta.url);
const load = () => JSON.parse(fs.readFileSync(SOURCE, "utf8"));
const relaysOf = (c) => c.item.find((i) => i.name.startsWith(RELAY_FOLDER_PREFIX));
const requests = (items, out = []) => {
  for (const i of items) i.item ? requests(i.item, out) : out.push(i);
  return out;
};
const testScript = (req) => req.event.find((e) => e.listen === "test").script.exec;

test("every block's subfolder and anchor exist in the checked-in collection", () => {
  const relays = relaysOf(load());
  for (const b of RELAY_MATRIX_BLOCKS) {
    const sub = relays.item.find((s) => s.name === b.folder);
    assert.ok(sub, `subfolder ${b.folder}`);
    if (b.before !== null) assert.ok(sub.item.some((i) => i.name === b.before + SUFFIX), `anchor ${b.before}`);
  }
});

test("generated rows are named like the static ones and do not collide with them", () => {
  const relays = relaysOf(load());
  const staticNames = new Set(requests(relays.item).map((r) => r.name));
  const names = RELAY_MATRIX_BLOCKS.flatMap((b) => buildBlock(b).map((r) => r.name));
  for (const n of names) {
    assert.ok(n.endsWith(SUFFIX), n);
    assert.ok(!staticNames.has(n), `generated row also checked in: ${n}`);
  }
  assert.equal(new Set(names).size, names.length);
});

test("splicing lands each block in place and running it twice changes nothing", () => {
  const c = load();
  const before = requests(relaysOf(c).item).length;
  const added = spliceRelayMatrix(c);
  assert.equal(added, RELAY_MATRIX_BLOCKS.reduce((n, b) => n + b.rows.length, 0));
  const once = JSON.stringify(c);
  assert.equal(spliceRelayMatrix(c), added);
  assert.equal(JSON.stringify(c), once);
  const relays = relaysOf(c);
  assert.equal(requests(relays.item).length, before + added);
  const sub = (name) => relays.item.find((s) => s.name === name).item.map((i) => i.name);
  assert.equal(sub(RELAY_MATRIX_BLOCKS.find((b) => b.tag === "invalid").folder).length, VALIDATION_RULES.length);
  assert.equal(sub(RELAY_MATRIX_BLOCKS.find((b) => b.tag === "preview").folder).length, PREVIEW_CASES.length);
  // An anchored block sits right before its anchor.
  for (const b of RELAY_MATRIX_BLOCKS.filter((x) => x.before !== null)) {
    const names = sub(b.folder);
    const last = buildBlock(b).at(-1).name;
    assert.equal(names[names.indexOf(last) + 1], b.before + SUFFIX);
  }
});

test("every relay script either carries the marker or no helper copy at all", () => {
  for (const req of requests(relaysOf(load()).item)) {
    for (const e of req.event || []) {
      const exec = e.script.exec;
      assert.ok(!HELPERS.slice(3).every((h) => exec.includes(h)), `full helper copy left in ${req.name}`);
    }
  }
});

test("the marker is replaced by the helpers, scripts without it are left alone, and a rerun changes nothing", () => {
  const marked = { listen: "test", script: { exec: [HELPERS_MARKER, "relayLog(function () {});"] } };
  const plain = { listen: "test", script: { exec: ["pm.test('x', function () {});"] } };
  const c = { item: [{ name: "f", item: [{ name: "a", event: [marked] }, { name: "b", event: [plain] }] }] };
  assert.equal(injectRelayHelpers(c), 1);
  assert.deepEqual(marked.script.exec, [...HELPERS, "relayLog(function () {});"]);
  assert.deepEqual(plain.script.exec, ["pm.test('x', function () {});"]);
  assert.equal(injectRelayHelpers(c), 0);
});

test("the injected helpers' own calls still get the setup token", () => {
  const c = { item: [{ name: "a", event: [{ listen: "test", script: { exec: [HELPERS_MARKER, "relayLog(function () {});"] } }] }] };
  injectRelayHelpers(c);
  routeScriptCallsThroughSetupToken(c, "setupToken");
  const src = c.item[0].event[0].script.exec.join("\n");
  assert.ok(!/(^|[^.\w])pm\.sendRequest\(\{ url: BASE/m.test(src), "helper calls were not routed");
  assert.match(src, /__bfSend\(\{ url: BASE \+ '\/api\/relay-logs/);
});

test("every generated script parses, and none skips its assertions on an error status", () => {
  for (const b of RELAY_MATRIX_BLOCKS) {
    for (const req of buildBlock(b)) {
      const exec = testScript(req);
      assert.equal(exec[0], HELPERS_MARKER, req.name);
      const src = [...HELPERS, ...exec.slice(1)].join("\n");
      assert.doesNotThrow(() => new Function("pm", src), req.name);
      assert.ok(!/if \([^)]*pm\.response\.code[^)]*\)\s*\{\s*return/.test(src), `early return in ${req.name}`);
      // A fixture-client row checks the relay's status inside its callback.
      assert.match(src, /expectStatus\(\d{3}, |pm\.expect\(out\.status, .*?\)\.to\.eql\(200\)/, req.name);
    }
  }
});

test("a row's [EXPECT-NNN] tag matches the status it asserts", () => {
  for (const b of RELAY_MATRIX_BLOCKS) {
    for (const req of buildBlock(b)) {
      const tag = req.name.match(/^\[EXPECT-(\d{3})\]/);
      const asserted = testScript(req).join("\n").match(/expectStatus\((\d{3}), /);
      if (!asserted) continue; // a fixture-client row asserts inside its callback
      assert.equal(tag ? tag[1] : "200", asserted[1], req.name);
    }
  }
});

test("each validation rule posts JSON that differs from the valid base", () => {
  const rows = buildBlock(RELAY_MATRIX_BLOCKS.find((b) => b.tag === "invalid"));
  const bodies = rows.map((r) => r.request.body.raw);
  for (const raw of bodies) assert.doesNotThrow(() => JSON.parse(raw.replace(/\b1e400\b/, "1")));
  assert.equal(new Set(bodies).size, bodies.length);
  assert.ok(bodies.some((b) => /"n": 1e400/.test(b)), "the out-of-range number is written bare");
});
