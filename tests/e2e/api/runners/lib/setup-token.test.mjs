// Unit tests for routing in-script calls through the setup token. Run directly:
// `node setup-token.test.mjs`. Same shape as chained-vars.test.mjs next door.
import assert from "node:assert";
import { SEND_HELPER, setupTokenHelper, routeScriptCallsThroughSetupToken } from "./setup-token.mjs";

let passed = 0;
function test(name, fn) {
  fn();
  passed++;
  console.log(`  ok - ${name}`);
}

const BASE = "http://localhost:8080";
const script = (...lines) => ({ listen: "test", script: { type: "text/javascript", exec: lines } });

// Runs the helper against a stub pm and returns the request it handed to pm.sendRequest.
function sent(req, vars = { setupToken: "tok", baseUrl: BASE }) {
  let seen = null;
  const pm = { variables: { get: (k) => vars[k] }, sendRequest: (r) => { seen = r; } };
  new Function("pm", "req", `${setupTokenHelper("setupToken").join("\n")}\n${SEND_HELPER}(req, function () {});`)(pm, req);
  return seen;
}

test("a call to this gateway's /api gets the token, whatever shape the request takes", () => {
  assert.deepEqual(sent(`${BASE}/api/providers`).header, { "X-Bifrost-Setup-Token": "tok" });
  assert.deepEqual(sent({ url: `${BASE}/api/providers`, method: "POST", header: { "Content-Type": "application/json" } }).header, { "Content-Type": "application/json", "X-Bifrost-Setup-Token": "tok" });
  assert.deepEqual(sent({ url: `${BASE}/api/logs/1`, header: [{ key: "Accept", value: "*/*" }] }).header, [{ key: "Accept", value: "*/*" }, { key: "X-Bifrost-Setup-Token", value: "tok" }]);
  assert.equal(sent({ url: `${BASE}/api/logs/1`, header: "Accept: */*" }).header, "Accept: */*\nX-Bifrost-Setup-Token: tok");
  assert.deepEqual(sent({ url: { raw: `${BASE}/api/providers` } }).header, { "X-Bifrost-Setup-Token": "tok" });
  assert.deepEqual(sent({ url: "{{baseUrl}}/api/providers" }).header, { "X-Bifrost-Setup-Token": "tok" });
});

test("a token the script already set is kept, in any letter case", () => {
  assert.deepEqual(sent({ url: `${BASE}/api/providers`, header: { "x-bifrost-setup-token": "own" } }).header, { "x-bifrost-setup-token": "own" });
  assert.deepEqual(sent({ url: `${BASE}/api/providers`, header: [{ key: "X-Bifrost-Setup-Token", value: "own" }] }).header, [{ key: "X-Bifrost-Setup-Token", value: "own" }]);
});

test("calls that are not to this gateway's /api, or with no token configured, go out unchanged", () => {
  const inference = { url: `${BASE}/v1/chat/completions`, method: "POST", header: { "Content-Type": "application/json" } };
  assert.equal(sent(inference), inference);
  const fixture = { url: "http://127.0.0.1:8792/__reset", method: "POST" };
  assert.equal(sent(fixture), fixture);
  const elsewhere = { url: "https://example.com/api/thing" };
  assert.equal(sent(elsewhere), elsewhere);
  const noToken = { url: `${BASE}/api/providers` };
  assert.equal(sent(noToken, { baseUrl: BASE }), noToken);
});

test("every script that calls pm.sendRequest is rewritten, at every level", () => {
  const collection = {
    event: [script("pm.sendRequest(BASE + '/api/x', function () {});")],
    item: [
      {
        name: "folder",
        event: [script("pm.sendRequest({ url: BASE + '/api/y' }, cb);")],
        item: [{ name: "row", event: [script("var a = 1;", "pm.sendRequest(u, cb); pm.sendRequest(v, cb);")], request: {} }],
      },
    ],
  };
  assert.equal(routeScriptCallsThroughSetupToken(collection, "setupToken"), 3);
  const row = collection.item[0].item[0].event[0].script.exec;
  assert.deepEqual(row.slice(0, setupTokenHelper("setupToken").length), setupTokenHelper("setupToken"));
  const body = row.slice(setupTokenHelper("setupToken").length).join("\n");
  assert.equal(body, `var a = 1;\n${SEND_HELPER}(u, cb); ${SEND_HELPER}(v, cb);`);
});

test("scripts that make no call, or manage the token themselves, are left alone, and a second pass changes nothing", () => {
  const plain = script("pm.test('x', function () {});");
  const lockout = script("pm.request.headers.remove('X-Bifrost-Setup-Token');", "pm.sendRequest(BASE + '/api/x', cb);");
  const calls = script("pm.sendRequest(BASE + '/api/x', cb);");
  const collection = { item: [{ name: "row", event: [plain, lockout, calls], request: {} }] };
  const before = JSON.parse(JSON.stringify([plain, lockout]));
  assert.equal(routeScriptCallsThroughSetupToken(collection, "setupToken"), 1);
  assert.deepEqual([plain, lockout], before);
  const once = JSON.stringify(collection);
  assert.equal(routeScriptCallsThroughSetupToken(collection, "setupToken"), 0);
  assert.equal(JSON.stringify(collection), once);
});

console.log(`${passed} passed`);
