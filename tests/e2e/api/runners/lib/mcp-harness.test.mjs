// Checks for mcp-harness.mjs: the shapes other collection builders rely on.
//   node --test tests/e2e/api/runners/lib/mcp-harness.test.mjs

import { test } from "node:test";
import assert from "node:assert/strict";
import { A, AUTH, CHAOS, MCP_ACCEPT, PU_FLOWS, consentAs, flowStart, folder, identityCalls, mockPost, puConnect, request, rpc, schemasLack, vkCreate } from "./mcp-harness.mjs";

test("request defaults to the base URL, the admin header and JSON bodies", () => {
  const item = request("T-01", "create a thing", "POST", "/api/things", { body: { a: 1 }, asserts: [A.status(200)] });
  assert.equal(item.name, "T-01 create a thing");
  assert.equal(item.request.url, "{{base_url}}/api/things");
  assert.deepEqual(item.request.header, [AUTH, { key: "Content-Type", value: "application/json" }]);
  assert.equal(item.request.body.raw, '{"a":1}');
  const exec = item.event.find((e) => e.listen === "test").script.exec;
  assert.equal(exec[0], 'pm.test("T-01 create a thing: status is 200", function () {');
  assert.equal(item.protocolProfileBehavior, undefined);
});

test("request takes another base, no auth, raw bodies, a prerequest and request behaviour", () => {
  const item = request("T-02", "probe", "GET", "/mcp", { base: "{{other_url}}", noAuth: true, raw: "{{body}}", pre: ["var x = 1;"], noRedirect: true, noCookies: true, headers: [MCP_ACCEPT] });
  assert.equal(item.request.url, "{{other_url}}/mcp");
  assert.deepEqual(item.request.header, [{ key: "Content-Type", value: "application/json" }, MCP_ACCEPT]);
  assert.equal(item.request.body.raw, "{{body}}");
  assert.deepEqual(item.event[0], { listen: "prerequest", script: { type: "text/javascript", exec: ["var x = 1;"] } });
  assert.deepEqual(item.protocolProfileBehavior, { followRedirects: false, disableCookies: true });
});

test("a skipped assertion is reported as skipped", () => {
  const exec = request("T-03", "known issue", "GET", "/x", { asserts: [A.skip(A.status(200))] }).event[0].script.exec;
  assert.equal(exec[0], 'pm.test.skip("T-03 known issue: status is 200 (skipped: known issue)", function () {');
});

test("rpc, mockPost and vkCreate address the gateway, the fleet and governance", () => {
  const r = rpc("T-04", "list tools", { jsonrpc: "2.0", id: 1, method: "tools/list" });
  assert.equal(r.request.url, "{{base_url}}/mcp");
  assert.ok(!r.request.header.includes(AUTH));
  const m = mockPost("T-04", "alpha", "faults", "slow alpha", { faults: { latency: "5s" } });
  assert.equal(m.request.url, `${CHAOS}/control/alpha/faults`);
  assert.ok(!m.request.header.includes(AUTH));
  const v = vkCreate("T-04", "create a key", { name: "k" }, "t04_key");
  const exec = v.event[0].script.exec.join("\n");
  assert.ok(exec.includes(`pm.collectionVariables.set("t04_key_id", pm.response.json().virtual_key.id);`));
  assert.ok(exec.includes(`pm.collectionVariables.set("t04_key", pm.response.json().virtual_key.value);`));
});

test("folder groups items under a name and description", () => {
  assert.deepEqual(folder("T-05 a test", "Setup: none.", []), { name: "T-05 a test", description: "Setup: none.", item: [] });
});

test("the upstream OAuth helpers address the mock's consent, Bifrost's per-user flows and the identity counters", () => {
  const c = consentAs("T-06", "consent as alice", "srv", "alice");
  assert.equal(c.request.url, "{{srv_au}}&login_hint=alice");
  assert.deepEqual(c.protocolProfileBehavior, { followRedirects: false, disableCookies: true });
  assert.ok(c.event[0].script.exec.includes("pm.collectionVariables.set('srv_cb', pm.response.headers.get('Location'));"));
  const f = flowStart("T-06", "start the flow", "f1");
  assert.equal(f.request.url, `{{base_url}}${PU_FLOWS}{{f1}}/start`);
  const connect = puConnect("T-06", "srv");
  assert.deepEqual(connect.map((i) => i.name.replace("T-06 ", "")).slice(-4), [
    "setup: create the per-user OAuth client",
    "setup: consent for the per-user OAuth client as admin",
    "setup: follow the callback for the per-user OAuth client",
    "setup: complete the per-user OAuth client",
  ]);
  assert.equal(identityCalls("alice", 2).title, "calls_by_identity[alice] is 2");
});

// runAssertion runs an assertion's ES5 lines against a tools/list answer, with an expect that supports the
// chains these lines use (property, an, not.property); a failed expectation throws.
const runAssertion = (assertion, tools) => {
  const expect = (value, msg) => {
    const fail = (what) => { throw new Error(`${msg}: ${what}`); };
    const has = (k) => value !== null && typeof value === "object" && Object.prototype.hasOwnProperty.call(value, k);
    return { to: {
      have: { property: (k) => { if (!has(k)) fail(`missing ${k}`); } },
      be: { an: (type) => { if (type !== "object" || value === null || typeof value !== "object" || Array.isArray(value)) fail(`not an ${type}`); } },
      not: { have: { property: (k) => { if (has(k)) fail(`has ${k}`); } } },
    } };
  };
  const pm = { expect, response: { json: () => ({ result: { tools } }) } };
  new Function("pm", assertion.body.join("\n"))(pm);
};

test("schemasLack fails when a named tool or its schema is missing, not only when it takes the argument", () => {
  const check = schemasLack(["chat", "responses"], ["stream", "stream_options"]);
  const tool = (name, properties) => ({ name, inputSchema: { type: "object", properties } });
  runAssertion(check, [tool("chat", { model: {} }), tool("responses", undefined)]);
  assert.throws(() => runAssertion(check, [tool("chat", { model: {} })]), /responses.*missing responses/);
  assert.throws(() => runAssertion(check, [tool("chat", { model: {} }), { name: "responses" }]), /responses inputSchema: not an object/);
  assert.throws(() => runAssertion(check, [tool("chat", { stream: {} }), tool("responses", {})]), /chat: has stream/);
  assert.throws(() => runAssertion(check, [tool("chat", {}), tool("responses", { stream_options: {} })]), /responses: has stream_options/);
});
