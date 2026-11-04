// Checks for mcp-harness.mjs: the shapes other collection builders rely on.
//   node --test tests/e2e/api/runners/lib/mcp-harness.test.mjs

import { test } from "node:test";
import assert from "node:assert/strict";
import { A, AUTH, CHAOS, MCP_ACCEPT, folder, mockPost, request, rpc, vkCreate } from "./mcp-harness.mjs";

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
