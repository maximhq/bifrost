#!/usr/bin/env node
// Generate the MCP harness Postman collection (collections/mcp-harness.json).
//
// One folder per test case, named by its ID and grouped by section. runners/individual/run-newman-mcp-harness-tests.sh
// generates the collection at the start of each run, boots Bifrost and runs it. The collection is not committed: edit
// this file.
//
//   node tests/e2e/api/runners/build-mcp-harness.mjs [--out <path>]
//   TESTS=ID,ID node tests/e2e/api/runners/build-mcp-harness.mjs --out <path>   # only those tests
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { resolveOutPath, writeCollection } from "./lib/collection-builder.mjs";

const HERE = dirname(fileURLToPath(import.meta.url));
const OUT = resolveOutPath(join(HERE, "..", "collections", "mcp-harness.json"));

const AUTH = { key: "Authorization", value: "{{admin_auth_header}}" };
const JSONCT = { key: "Content-Type", value: "application/json" };
const lit = (v) => JSON.stringify(v);

// assertion builders: each returns ES5 lines
const A = {
  status: (code) => ({ title: `status is ${code}`, body: [`pm.expect(pm.response.code, 'body: ' + pm.response.text()).to.equal(${code});`] }),
  msgEquals: (m) => ({ title: `error message is ${lit(m)}`, body: [`var b = pm.response.json();`, `pm.expect(b.error && b.error.message, 'body: ' + pm.response.text()).to.equal(${lit(m)});`] }),
  msgContains: (m) => ({ title: `error message contains ${lit(m)}`, body: [`var b = pm.response.json();`, `pm.expect(b.error && b.error.message, 'body: ' + pm.response.text()).to.include(${lit(m)});`] }),
  msgStartsWith: (m) => ({ title: `error message starts with ${lit(m)}`, body: [`var b = pm.response.json();`, `pm.expect(b.error && b.error.message, 'body: ' + pm.response.text()).to.be.a('string');`, `pm.expect(b.error.message.indexOf(${lit(m)}), 'message: ' + b.error.message).to.equal(0);`] }),
  jsonEquals: (field, v) => ({ title: `${field} is ${lit(v)}`, body: [`var b = pm.response.json();`, `pm.expect(b[${lit(field)}], 'body: ' + pm.response.text()).to.equal(${lit(v)});`] }),
  jsonMessage: (m) => ({ title: `message is ${lit(m)}`, body: [`var b = pm.response.json();`, `pm.expect(b.message, 'body: ' + pm.response.text()).to.equal(${lit(m)});`] }),
  custom: (title, lines) => ({ title, body: lines }),
  // skip marks an assertion as skipped (see request).
  skip: (a) => ({ ...a, skip: true }),
};

function request(id, name, method, path, { raw, body, asserts = [], pre = [], extraTests = [], noAuth = false, noRedirect = false, base = "{{base_url}}", headers = [], noAccept = false, noCookies = false } = {}) {
  const full = `${id} ${name}`;
  const exec = [];
  for (const a of asserts) {
    // A skipped assertion states behaviour a known issue still breaks; it is reported as skipped until the fix lands.
    exec.push(`${a.skip ? "pm.test.skip" : "pm.test"}(${lit(`${full}: ${a.title}${a.skip ? " (skipped: known issue)" : ""}`)}, function () {`);
    for (const l of a.body) exec.push(`  ${l}`);
    exec.push(`});`);
  }
  exec.push(...extraTests);
  const event = [];
  if (pre.length) event.push({ listen: "prerequest", script: { type: "text/javascript", exec: pre } });
  event.push({ listen: "test", script: { type: "text/javascript", exec } });
  const header = noAuth ? [] : [AUTH];
  const req = { method, header, url: `${base}${path}` };
  const rawBody = raw !== undefined ? raw : body !== undefined ? JSON.stringify(body) : undefined;
  if (rawBody !== undefined) {
    if (!headers.some((h) => h.key.toLowerCase() === "content-type")) header.push(JSONCT);
    req.body = { mode: "raw", raw: rawBody };
  }
  header.push(...headers);
  const item = { name: full, event, request: req };
  const behavior = {};
  // Redirect tests assert the 302 itself, so Newman must not follow it.
  if (noRedirect) behavior.followRedirects = false;
  // Tests about a missing Accept header stop Newman from adding its own.
  if (noAccept) behavior.disabledSystemHeaders = { accept: true };
  // Tests that handle a session cookie keep it out of Newman's shared cookie jar.
  if (noCookies) behavior.disableCookies = true;
  if (Object.keys(behavior).length) item.protocolProfileBehavior = behavior;
  return item;
}

const folder = (name, description, item) => ({ name, description, item });

// ---------------------------------------------------------------- ADM
const clientBody = (over) => ({ name: "adm_probe", connection_type: "http", connection_string: "http://127.0.0.1:9/mcp", auth_type: "none", ...over });

const ADM05 = folder("ADM-05 Create rejects a bad body and connection_type",
  "Setup: dashboard auth on, admin credentials.", [
  request("ADM-05", "setup: record the client count", "GET", "/api/mcp/clients", {
    asserts: [A.status(200)],
    extraTests: ["pm.collectionVariables.set('adm05_total', pm.response.json().total_count);"],
  }),
  request("ADM-05", "body \"x\" is rejected", "POST", "/api/mcp/client", { raw: "x", asserts: [A.status(400), A.msgEquals("Invalid request payload")] }),
  ...["streamable", "inprocess", ""].map((ct) =>
    request("ADM-05", `connection_type ${lit(ct)} is rejected`, "POST", "/api/mcp/client", {
      body: clientBody({ name: "adm05_client", connection_type: ct }),
      asserts: [A.status(400), A.msgContains("must be one of http, sse, stdio, openapi")],
    })),
  request("ADM-05", "no client was added", "GET", "/api/mcp/clients", {
    asserts: [A.status(200), A.custom("total_count is unchanged", [
      "var before = Number(pm.collectionVariables.get('adm05_total'));",
      "pm.expect(pm.response.json().total_count, 'body: ' + pm.response.text()).to.equal(before);",
    ])],
  }),
]);

const ADM06 = folder("ADM-06 Create rejects invalid names",
  "Setup: dashboard auth on, admin credentials; an otherwise valid http body.",
  [["", "name is required"], ["my-srv", "cannot contain hyphens"], ["my srv", "cannot contain spaces"], ["9srv", "cannot start with a number"], ["srvé", "only ASCII characters"]].map(([name, reason]) =>
    request("ADM-06", `name ${lit(name)} is rejected`, "POST", "/api/mcp/client", {
      body: clientBody({ name }),
      asserts: [A.status(400), A.msgStartsWith("Invalid client name: "), A.msgContains(reason)],
    })));

const ADM62 = folder("ADM-62 List query validation",
  "Setup: dashboard auth on, admin credentials.", [
  request("ADM-62", "limit=abc is rejected", "GET", "/api/mcp/clients?limit=abc", { asserts: [A.status(400)] }),
  request("ADM-62", "limit=-1 is rejected", "GET", "/api/mcp/clients?limit=-1", { asserts: [A.status(400), A.msgContains("must be non-negative")] }),
  request("ADM-62", "offset=-1 is rejected", "GET", "/api/mcp/clients?offset=-1", { asserts: [A.status(400), A.msgContains("must be non-negative")] }),
  request("ADM-62", "state=bogus is rejected", "GET", "/api/mcp/clients?state=bogus", { asserts: [A.status(400), A.msgContains("must be 'healthy' or 'unstable'")] }),
  request("ADM-62", "code_mode=maybe is rejected", "GET", "/api/mcp/clients?code_mode=maybe", { asserts: [A.status(400)] }),
  request("ADM-62", "disabled=maybe is rejected", "GET", "/api/mcp/clients?disabled=maybe", { asserts: [A.status(400)] }),
  request("ADM-62", "limit=1000 gives 100", "GET", "/api/mcp/clients?limit=1000", { asserts: [A.status(200), A.jsonEquals("limit", 100)] }),
  request("ADM-62", "limit=0 gives 25", "GET", "/api/mcp/clients?limit=0", { asserts: [A.status(200), A.jsonEquals("limit", 25)] }),
]);

const ADM73 = folder("ADM-73 Sessions list validation",
  "Setup: dashboard auth on, admin credentials.", [
  request("ADM-73", "limit=0 is rejected", "GET", "/api/mcp/sessions?limit=0", { asserts: [A.status(400), A.msgContains("must be greater than zero")] }),
  request("ADM-73", "limit=abc is rejected", "GET", "/api/mcp/sessions?limit=abc", { asserts: [A.status(400)] }),
  request("ADM-73", "offset=-1 is rejected", "GET", "/api/mcp/sessions?offset=-1", { asserts: [A.status(400)] }),
  request("ADM-73", "limit=600 gives 500", "GET", "/api/mcp/sessions?limit=600", { asserts: [A.status(200), A.jsonEquals("limit", 500)] }),
  request("ADM-73", "defaults are limit 50, offset 0", "GET", "/api/mcp/sessions", { asserts: [A.status(200), A.jsonEquals("limit", 50), A.jsonEquals("offset", 0)] }),
]);

// ---------------------------------------------------------------- LOG
const LOG27 = folder("LOG-27 Invalid list params", "Setup: none (dashboard auth on, admin credentials).", [
  request("LOG-27", "limit=0 is rejected", "GET", "/api/mcp-logs?limit=0", { asserts: [A.status(400)] }),
  request("LOG-27", "limit=1001 is rejected", "GET", "/api/mcp-logs?limit=1001", { asserts: [A.status(400), A.msgEquals("limit cannot exceed 1000")] }),
  request("LOG-27", "limit=x is rejected", "GET", "/api/mcp-logs?limit=x", { asserts: [A.status(400)] }),
  request("LOG-27", "offset=-1 is rejected", "GET", "/api/mcp-logs?offset=-1", { asserts: [A.status(400)] }),
  request("LOG-27", "sort_by=tokens is rejected", "GET", "/api/mcp-logs?sort_by=tokens", { asserts: [A.status(400), A.msgContains("must be 'timestamp', 'latency' or 'cost'")] }),
  request("LOG-27", "order=up is rejected", "GET", "/api/mcp-logs?order=up", { asserts: [A.status(400)] }),
]);

const LOG28 = folder("LOG-28 Invalid time and latency params", "Setup: none (dashboard auth on, admin credentials).", [
  request("LOG-28", "start_time=bad is rejected", "GET", "/api/mcp-logs?start_time=bad", { asserts: [A.status(400), A.msgContains("invalid start_time format")] }),
  request("LOG-28", "min_latency=x is rejected", "GET", "/api/mcp-logs?min_latency=x", { asserts: [A.status(400)] }),
  request("LOG-28", "start_time=bad with period=1h is accepted", "GET", "/api/mcp-logs?start_time=bad&period=1h", { asserts: [A.status(200)] }),
]);

const LOG29 = folder("LOG-29 Unknown log id", "Setup: none (dashboard auth on, admin credentials).", [
  request("LOG-29", "an unknown id is not found", "GET", "/api/mcp-logs/mcp-harness-no-such-log", { asserts: [A.status(404), A.msgEquals("MCP log not found")] }),
]);

const LOG37 = folder("LOG-37 Histogram bad time", "Setup: none (dashboard auth on, admin credentials).", [
  request("LOG-37", "start_time=bad is rejected", "GET", "/api/mcp-logs/histogram?start_time=bad", { asserts: [A.status(400), A.msgContains("invalid start_time format")] }),
]);

const LOG40 = folder("LOG-40 Delete body validation", "Setup: authenticated (dashboard auth on, admin credentials).", [
  request("LOG-40", "an empty body is rejected", "DELETE", "/api/mcp-logs", { body: {}, asserts: [A.status(400), A.msgEquals("No log IDs provided")] }),
  request("LOG-40", "malformed JSON is rejected", "DELETE", "/api/mcp-logs", { raw: "{", asserts: [A.status(400), A.msgEquals("Invalid JSON")] }),
  request("LOG-40", "unknown ids are accepted", "DELETE", "/api/mcp-logs", { body: { ids: ["mcp-harness-no-such-log"] }, asserts: [A.status(200)] }),
]);

// ---------------------------------------------------------------- LIB
const LIB06 = folder("LIB-06 Bad paging values", "Setup: none (dashboard auth on, admin credentials).", [
  request("LIB-06", "limit=abc is rejected", "GET", "/api/mcp/library?limit=abc", { asserts: [A.status(400), A.msgEquals("Invalid limit parameter: must be a number")] }),
  request("LIB-06", "limit=-1 is rejected", "GET", "/api/mcp/library?limit=-1", { asserts: [A.status(400), A.msgContains("must be non-negative")] }),
  request("LIB-06", "offset=abc is rejected", "GET", "/api/mcp/library?offset=abc", { asserts: [A.status(400), A.msgEquals("Invalid offset parameter: must be a number")] }),
  request("LIB-06", "offset=-1 is rejected", "GET", "/api/mcp/library?offset=-1", { asserts: [A.status(400), A.msgContains("must be non-negative")] }),
]);

const libURL = "http://127.0.0.1:9/mcp";
const LIB10 = folder("LIB-10 Publish validation", "Setup: none (dashboard auth on, admin credentials).", [
  request("LIB-10", "bad JSON is rejected", "POST", "/api/mcp/library", { raw: "{", asserts: [A.status(400), A.msgEquals("Invalid request payload")] }),
  request("LIB-10", "a blank name is rejected", "POST", "/api/mcp/library", { body: { name: "  ", connection_type: "http", connection_url: libURL }, asserts: [A.status(400), A.msgEquals("name is required")] }),
  request("LIB-10", "http without a URL is rejected", "POST", "/api/mcp/library", { body: { name: "LIB-10 http", connection_type: "http" }, asserts: [A.status(400), A.msgEquals("connection_url is required for http/sse connection types")] }),
  request("LIB-10", "stdio_config without a command is rejected", "POST", "/api/mcp/library", { body: { name: "LIB-10 stdio", connection_type: "stdio", stdio_config: { args: ["x"] } }, asserts: [A.status(400), A.msgEquals("stdio_config.command is required for stdio connection type")] }),
  request("LIB-10", "connection_type openapi is rejected", "POST", "/api/mcp/library", { body: { name: "LIB-10 openapi", connection_type: "openapi", connection_url: libURL }, asserts: [A.status(400), A.msgEquals("connection_type must be one of: http, stdio, sse")] }),
  request("LIB-10", "an unknown auth_type is rejected", "POST", "/api/mcp/library", { body: { name: "LIB-10 auth", connection_type: "http", connection_url: libURL, auth_type: "bogus" }, asserts: [A.status(400), A.msgEquals("invalid auth_type")] }),
  request("LIB-10", "name \"!!!\" is rejected", "POST", "/api/mcp/library", { body: { name: "!!!", connection_type: "http", connection_url: libURL }, asserts: [A.status(400), A.msgEquals("name must contain at least one alphanumeric character")] }),
]);

const LIB16 = folder("LIB-16 Delete with bad ids", "Setup: none (dashboard auth on, admin credentials). The repeat-delete case publishes and deletes its own custom entry first.", [
  request("LIB-16", "a non-numeric id is rejected", "DELETE", "/api/mcp/library/abc", { asserts: [A.status(400), A.msgEquals("id must be a positive integer")] }),
  request("LIB-16", "an unknown id is not found", "DELETE", "/api/mcp/library/999999", { asserts: [A.status(404), A.msgEquals("MCP library entry not found")] }),
  request("LIB-16", "setup: publish a custom entry", "POST", "/api/mcp/library", {
    body: { name: "LIB-16 entry", connection_type: "http", connection_url: libURL },
    asserts: [A.status(200)],
    extraTests: ["pm.collectionVariables.set('lib16_id', pm.response.json().entry.id);"],
  }),
  request("LIB-16", "setup: delete the entry", "DELETE", "/api/mcp/library/{{lib16_id}}", { asserts: [A.status(200)] }),
  request("LIB-16", "a repeat delete is not found", "DELETE", "/api/mcp/library/{{lib16_id}}", { asserts: [A.status(404), A.msgEquals("MCP library entry not found")] }),
]);

// ---------------------------------------------------------------- config validation tests (EP-09, INF-23, INF-56, OAS-36)
// readConfig stores the current client_config; putConfig sends it back with one change.
const readConfig = (id) => request(id, "setup: read the current client config", "GET", "/api/config", {
  asserts: [A.status(200)],
  extraTests: ["pm.collectionVariables.set('client_config', JSON.stringify(pm.response.json().client_config));"],
});
// mutate is ES5 source operating on `cc`.
const putConfig = (id, name, mutate, asserts) => request(id, name, "PUT", "/api/config", {
  raw: "{{config_body}}",
  pre: [
    "var cc = JSON.parse(pm.collectionVariables.get('client_config'));",
    ...mutate,
    "pm.variables.set('config_body', JSON.stringify({ client_config: cc }));",
  ],
  asserts,
});
const restoreConfig = (id) => putConfig(id, "teardown: restore the original config", [], [A.status(200)]);

const EP09 = folder("EP-09 Instruction cap validation", "Setup: PUT /api/config (dashboard auth on, admin credentials).", [
  readConfig("EP-09"),
  putConfig("EP-09", "per-client cap above the total is rejected", ["cc.mcp_max_instructions_per_client = 500;", "cc.mcp_max_instructions_total = 100;"], [A.status(400), A.msgContains("must not exceed mcp_max_instructions_total")]),
  putConfig("EP-09", "a negative cap is rejected", ["cc.mcp_max_instructions_per_client = -1;"], [A.status(400), A.msgContains("must not be negative")]),
  putConfig("EP-09", "a cap of 1048577 is rejected", ["cc.mcp_max_instructions_per_client = 1048577;"], [A.status(400), A.msgContains("must not exceed 1048576 bytes")]),
  restoreConfig("EP-09"),
]);

const INF23 = folder("INF-23 Instruction cap validation", "Setup: PUT /api/config (dashboard auth on, admin credentials).", [
  readConfig("INF-23"),
  putConfig("INF-23", "a negative cap is rejected", ["cc.mcp_max_instructions_per_client = -1;"], [A.status(400), A.msgContains("must not be negative")]),
  putConfig("INF-23", "per-client 5000 with total 4000 is rejected", ["cc.mcp_max_instructions_per_client = 5000;", "cc.mcp_max_instructions_total = 4000;"], [A.status(400), A.msgContains("must not exceed mcp_max_instructions_total (4000)")]),
]);

const INF56 = folder("INF-56 Code mode limits validation", "Setup: PUT /api/config (dashboard auth on, admin credentials).", [
  readConfig("INF-56"),
  putConfig("INF-56", "max_steps -1 is rejected", ["cc.mcp_code_mode_limits = { max_steps: -1 };"], [A.status(400), A.msgContains("must not be negative")]),
  putConfig("INF-56", "max_value_bytes 100 is rejected", ["cc.mcp_code_mode_limits = { max_value_bytes: 100 };"], [A.status(400), A.msgContains("must be at least 1024")]),
  putConfig("INF-56", "max_nesting_depth 1001 is rejected", ["cc.mcp_code_mode_limits = { max_nesting_depth: 1001 };"], [A.status(400), A.msgContains("must be at most 1000")]),
]);

// ---------------------------------------------------------------- OAS
const OAS31 = folder("OAS-31 Sessions list validation", "Setup: admin (dashboard auth on, admin credentials).", [
  request("OAS-31", "bf_mode=bogus is rejected", "GET", "/api/oauth2/sessions?bf_mode=bogus", { asserts: [A.status(400)] }),
  request("OAS-31", "limit=0 is rejected", "GET", "/api/oauth2/sessions?limit=0", { asserts: [A.status(400)] }),
  request("OAS-31", "limit=abc is rejected", "GET", "/api/oauth2/sessions?limit=abc", { asserts: [A.status(400)] }),
  request("OAS-31", "offset=-1 is rejected", "GET", "/api/oauth2/sessions?offset=-1", { asserts: [A.status(400)] }),
  request("OAS-31", "limit=1000 gives 500", "GET", "/api/oauth2/sessions?limit=1000", { asserts: [A.status(200), A.jsonEquals("limit", 500)] }),
]);

const OAS36 = folder("OAS-36 OAuth config validation", "Setup: PUT /api/config (dashboard auth on, admin credentials).", [
  readConfig("OAS-36"),
  putConfig("OAS-36", "mcp_server_auth_mode bogus is rejected", ["cc.mcp_server_auth_mode = 'bogus';"], [A.status(400), A.msgContains("must be one of: headers, both, oauth")]),
  putConfig("OAS-36", "both without issuer_url is rejected", ["cc.mcp_server_auth_mode = 'both';", "cc.oauth2_server_config = {};"], [A.status(400), A.msgContains("issuer_url must be set")]),
  putConfig("OAS-36", "auth_code_ttl 901 is rejected", ["cc.oauth2_server_config = { auth_code_ttl: 901 };"], [A.status(400), A.msgContains("must not exceed 900 seconds")]),
  putConfig("OAS-36", "disable_vk_identity in both mode is rejected", ["cc.mcp_server_auth_mode = 'both';", "cc.oauth2_server_config = { issuer_url: pm.variables.get('base_url'), disable_vk_identity: true };"], [A.status(400), A.msgContains("only valid when mcp_server_auth_mode is oauth")]),
  putConfig("OAS-36", "a partial wildcard redirect is rejected", ["cc.oauth2_server_config = { allowed_redirect_uris: ['https://x/*'] };"], [A.status(400), A.msgContains("supports only a bare")]),
  putConfig("OAS-36", "an ftp redirect is rejected", ["cc.oauth2_server_config = { allowed_redirect_uris: ['ftp://x'] };"], [A.status(400), A.msgContains("invalid callback URI")]),
  restoreConfig("OAS-36"),
]);

// ================================================================ OAuth server, auth gate and library tests

// OAuth endpoints answer {error, error_description}.
const O = {
  error: (code) => A.custom(`error is ${lit(code)}`, ["var b = pm.response.json();", `pm.expect(b.error, 'body: ' + pm.response.text()).to.equal(${lit(code)});`]),
  desc: (text) => A.custom(`error_description contains ${lit(text)}`, ["var b = pm.response.json();", `pm.expect(b.error_description, 'body: ' + pm.response.text()).to.include(${lit(text)});`]),
  emptyBody: () => A.custom("body is empty", ["pm.expect(pm.response.text(), 'body: ' + pm.response.text()).to.equal('');"]),
  noLocation: () => A.custom("there is no Location header", ["pm.expect(pm.response.headers.has('Location'), 'Location: ' + pm.response.headers.get('Location')).to.equal(false);"]),
  descEquals: (text) => A.custom(`error_description is ${lit(text)}`, ["var b = pm.response.json();", `pm.expect(b.error_description, 'body: ' + pm.response.text()).to.equal(${lit(text)});`]),
  noStore: () => A.custom("Cache-Control is no-store", ["pm.expect(pm.response.headers.get('Cache-Control')).to.equal('no-store');"]),
};

// ---------------------------------------------------------------- ADM-01
const noCredRoutes = [
  ["POST", "/api/mcp/client", clientBody({ name: "adm01_client" })],
  ["GET", "/api/mcp/clients"],
  ["PUT", "/api/mcp/client/adm01-id", {}],
  ["DELETE", "/api/mcp/client/adm01-id"],
  ["POST", "/api/mcp/client/adm01-id/reconnect"],
  ["POST", "/api/mcp/client/adm01-id/refresh-tools"],
  ["POST", "/api/mcp/client/adm01-id/reauthorize"],
  ["POST", "/api/mcp/client/adm01-id/reregister"],
  ["POST", "/api/mcp/client/adm01-id/complete-oauth"],
  ["POST", "/api/mcp/client/adm01-id/initiate-verification"],
  ["POST", "/api/mcp/client/adm01-id/verify-headers", {}],
  ["POST", "/api/mcp/client/adm01-id/verify-exchange", {}],
  ["GET", "/api/mcp/client/adm01-id/openapi-spec"],
  ["POST", "/api/mcp/openapi/preview", {}],
  ["GET", "/api/mcp/sessions"],
  ["DELETE", "/api/mcp/sessions/adm01-id"],
  ["POST", "/api/mcp/sessions/adm01-id/reauth"],
  ["GET", "/api/mcp/per-user-headers/flows/adm01-id"],
  ["PUT", "/api/mcp/per-user-headers/flows/adm01-id", {}],
  ["DELETE", "/api/mcp/per-user-headers/credential/adm01-id"],
  ["GET", "/api/oauth/per-user/flows/adm01-id"],
  ["GET", "/api/oauth/per-user/flows/adm01-id/start"],
  ["GET", "/api/oauth/config/adm01-id/status"],
  ["DELETE", "/api/oauth/config/adm01-id"],
];
const ADM01 = folder("ADM-01 Every MCP admin route without a credential",
  "Setup: dashboard auth on; requests carry no Authorization header or cookie.", [
  request("ADM-01", "setup: record the client count", "GET", "/api/mcp/clients", {
    asserts: [A.status(200)],
    extraTests: ["pm.collectionVariables.set('adm01_total', pm.response.json().total_count);"],
  }),
  ...noCredRoutes.map(([method, path, body]) =>
    request("ADM-01", `${method} ${path} without a credential`, method, path, { body, noAuth: true, asserts: [A.status(401), A.msgEquals("Unauthorized")] })),
  request("ADM-01", "GET /api/oauth/callback is public", "GET", "/api/oauth/callback", { noAuth: true, asserts: [A.status(400), A.msgEquals("Missing required parameters: state and code")] }),
  request("ADM-01", "nothing changed", "GET", "/api/mcp/clients", {
    asserts: [A.status(200), A.custom("total_count is unchanged", [
      "var before = Number(pm.collectionVariables.get('adm01_total'));",
      "pm.expect(pm.response.json().total_count, 'body: ' + pm.response.text()).to.equal(before);",
    ])],
  }),
]);

// ---------------------------------------------------------------- LOG-41
const LOG41 = folder("LOG-41 Read endpoints need auth", "Setup: dashboard auth on, no credential.",
  ["/api/mcp-logs", "/api/mcp-logs/stats", "/api/mcp-logs/filterdata", "/api/mcp-logs/histogram"].map((p) =>
    request("LOG-41", `GET ${p} without a credential`, "GET", p, { noAuth: true, asserts: [A.status(401), A.msgEquals("Unauthorized")] })));

// ---------------------------------------------------------------- LIB-08, LIB-09, LIB-13
const libDelete = (id, varName) => request(id, "teardown: delete the entry", "DELETE", `/api/mcp/library/{{${varName}}}`, { asserts: [A.status(200)] });
const LIB08 = folder("LIB-08 Publish an HTTP custom entry", "Setup: name \"Chaos Alpha\", connection_url http://127.0.0.1:9/mcp. The folder deletes its entry at the end.", [
  request("LIB-08", "publish \"Chaos Alpha\"", "POST", "/api/mcp/library", {
    body: { name: "Chaos Alpha", connection_type: "http", connection_url: libURL },
    asserts: [A.status(200), A.jsonMessage("MCP library server published successfully"),
      A.custom("entry source, slug and auth_type", [
        "var e = pm.response.json().entry;",
        "pm.expect(e.source, 'body: ' + pm.response.text()).to.equal('custom');",
        "pm.expect(e.slug).to.equal('chaos-alpha');",
        "pm.expect(e.auth_type).to.equal('none');",
      ])],
    extraTests: ["pm.collectionVariables.set('lib08_id', pm.response.json().entry.id);"],
  }),
  request("LIB-08", "the entry is listed", "GET", "/api/mcp/library?search=Chaos%20Alpha", {
    asserts: [A.status(200), A.custom("the list contains the entry", [
      "var id = Number(pm.collectionVariables.get('lib08_id'));",
      "var found = (pm.response.json().servers || []).filter(function (s) { return s.id === id; });",
      "pm.expect(found.length, 'body: ' + pm.response.text()).to.equal(1);",
    ])],
  }),
  libDelete("LIB-08", "lib08_id"),
]);

const stdioConfig = { command: "mcp-harness-stdio", args: ["--flag", "value"], envs: ["MCP_HARNESS_ENV"] };
const LIB09 = folder("LIB-09 Publish a STDIO custom entry", "Setup: stdio_config with command, args, envs. The folder deletes its entry at the end.", [
  request("LIB-09", "publish a stdio entry", "POST", "/api/mcp/library", {
    body: { name: "LIB-09 stdio entry", connection_type: "stdio", stdio_config: stdioConfig },
    asserts: [A.status(200), A.custom("stdio_config is echoed", [
      "pm.expect(pm.response.json().entry.stdio_config, 'body: ' + pm.response.text()).to.eql(" + lit(stdioConfig) + ");",
    ])],
    extraTests: ["pm.collectionVariables.set('lib09_id', pm.response.json().entry.id);"],
  }),
  request("LIB-09", "filterdata includes stdio", "GET", "/api/mcp/library/filterdata", {
    asserts: [A.status(200), A.custom("connection_types includes \"stdio\"", [
      "pm.expect(pm.response.json().connection_types, 'body: ' + pm.response.text()).to.include('stdio');",
    ])],
  }),
  libDelete("LIB-09", "lib09_id"),
]);

const LIB13 = folder("LIB-13 Delete a custom entry and republish", "Setup: a custom entry. The folder deletes the republished entry at the end.", [
  request("LIB-13", "setup: publish a custom entry", "POST", "/api/mcp/library", {
    body: { name: "LIB-13 entry", connection_type: "http", connection_url: libURL },
    asserts: [A.status(200)],
    extraTests: ["pm.collectionVariables.set('lib13_id', pm.response.json().entry.id);"],
  }),
  request("LIB-13", "the entry is listed before the delete", "GET", "/api/mcp/library?search=LIB-13%20entry", {
    asserts: [A.status(200), A.custom("the list contains the entry", [
      "var id = Number(pm.collectionVariables.get('lib13_id'));",
      "var found = (pm.response.json().servers || []).filter(function (s) { return s.id === id; });",
      "pm.expect(found.length, 'body: ' + pm.response.text()).to.equal(1);",
    ])],
  }),
  request("LIB-13", "delete the entry", "DELETE", "/api/mcp/library/{{lib13_id}}", { asserts: [A.status(200), A.jsonMessage("MCP library server removed successfully")] }),
  request("LIB-13", "the entry is not listed", "GET", "/api/mcp/library?search=LIB-13%20entry", {
    asserts: [A.status(200), A.custom("the list does not contain the entry", [
      "var id = Number(pm.collectionVariables.get('lib13_id'));",
      "var found = (pm.response.json().servers || []).filter(function (s) { return s.id === id; });",
      "pm.expect(found.length, 'body: ' + pm.response.text()).to.equal(0);",
    ])],
  }),
  request("LIB-13", "republish the same name", "POST", "/api/mcp/library", {
    body: { name: "LIB-13 entry", connection_type: "http", connection_url: libURL },
    asserts: [A.status(200)],
    extraTests: ["pm.collectionVariables.set('lib13_republished_id', pm.response.json().entry.id);"],
  }),
  libDelete("LIB-13", "lib13_republished_id"),
]);

// ---------------------------------------------------------------- OAS tests that need mode both or oauth
// setMode switches mcp_server_auth_mode with issuer_url = base_url, or the given issuer; restoreHeaders puts it back.
const setMode = (id, mode, issuer) => putConfig(id, `setup: switch mcp_server_auth_mode to ${mode} with issuer_url set to ${issuer || "the base URL"}`, [
  `cc.mcp_server_auth_mode = ${lit(mode)};`,
  `cc.oauth2_server_config = { issuer_url: ${issuer ? lit(issuer) : "pm.variables.get('base_url')"} };`,
], [A.status(200)]);
const restoreHeaders = (id) => putConfig(id, "teardown: restore mcp_server_auth_mode headers", ["cc.mcp_server_auth_mode = 'headers';"], [A.status(200)]);
const withMode = (id, mode, items, issuer) => [readConfig(id), setMode(id, mode, issuer), ...items, restoreHeaders(id)];
// OTHER_ISS is an issuer_url on another origin than the one requests go to, so a value taken from the request's
// Host instead of issuer_url cannot match it.
const OTHER_ISS = "https://issuer.mcp-harness.example";

const BOTH = "mcp_server_auth_mode both with issuer_url set to the base URL";
const LOOPBACK_CB = "http://127.0.0.1:9999/cb";
const CHALLENGE = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM";
// name, when given, also titles the request so tests that register two clients stay distinct;
// grantTypes, when given, is sent as grant_types.
const register = (id, varName, redirect = LOOPBACK_CB, name, grantTypes) => request(id, name ? `setup: register the OAuth client "${name}"` : "setup: register an OAuth client", "POST", "/oauth2/register", {
  noAuth: true, body: { client_name: name || `${id} client`, redirect_uris: [redirect], ...(grantTypes ? { grant_types: grantTypes } : {}) }, asserts: [A.status(201)],
  extraTests: [`pm.collectionVariables.set(${lit(varName)}, pm.response.json().client_id);`],
});
// authorizePath builds /oauth2/authorize; values are query-safe as written.
const authorizePath = (params) => "/oauth2/authorize?" + Object.entries(params).filter(([, v]) => v !== undefined).map(([k, v]) => `${k}=${v}`).join("&");
const goodAuthorize = (clientVar, over = {}) => authorizePath({
  response_type: "code", client_id: `{{${clientVar}}}`, redirect_uri: LOOPBACK_CB, state: "s1",
  code_challenge: CHALLENGE, code_challenge_method: "S256", resource: "{{base_url}}/mcp", scope: "mcp", ...over,
});
const consentLocation = (issuer) => A.custom(`Location is ${issuer ? "the issuer" : "<base URL>"}/oauth/consent?flow=<id>#t=<token>`, [
  "var loc = pm.response.headers.get('Location') || '';",
  `var iss = ${issuer ? lit(issuer) : "pm.variables.get('base_url')"};`,
  "pm.expect(loc.indexOf(iss + '/oauth/consent?flow='), 'Location: ' + loc).to.equal(0);",
  "pm.expect(/\\/oauth\\/consent\\?flow=[^#&]+#t=.+$/.test(loc), 'Location: ' + loc).to.equal(true);",
]);
const errorRedirect = (code, desc) => A.custom(`Location carries error ${code || "(any)"}${desc ? ", error_description " + lit(desc) : ""} and state s1`, [
  "var loc = pm.response.headers.get('Location') || '';",
  `pm.expect(loc.indexOf(${lit(LOOPBACK_CB + "?")}), 'Location: ' + loc).to.equal(0);`,
  "var q = {}; loc.split('?')[1].split('&').forEach(function (kv) { var p = kv.split('='); q[decodeURIComponent(p[0])] = decodeURIComponent((p[1] || '').replace(/\\+/g, ' ')); });",
  code ? `pm.expect(q.error, 'Location: ' + loc).to.equal(${lit(code)});` : "pm.expect(q.error, 'Location: ' + loc).to.be.a('string').and.not.empty;",
  desc ? `pm.expect(q.error_description, 'Location: ' + loc).to.equal(${lit(desc)});` : "pm.expect(q.error_description, 'Location: ' + loc).to.be.a('string').and.not.empty;",
  "pm.expect(q.state, 'Location: ' + loc).to.equal('s1');",
]);

const OAS01 = folder("OAS-01 OAuth server hidden in headers mode", "Setup: mcp_server_auth_mode headers, set explicitly and restored at the end.", [
  readConfig("OAS-01"),
  putConfig("OAS-01", "setup: mcp_server_auth_mode headers", ["cc.mcp_server_auth_mode = 'headers';"], [A.status(200)]),
  ...["/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp", "/.well-known/oauth-authorization-server", "/.well-known/oauth-authorization-server/mcp", "/.well-known/jwks.json"].map((p) =>
    request("OAS-01", `GET ${p} is not found`, "GET", p, { noAuth: true, asserts: [A.status(404), O.emptyBody()] })),
  request("OAS-01", "POST /oauth2/register is not found", "POST", "/oauth2/register", { noAuth: true, body: { redirect_uris: [LOOPBACK_CB] }, asserts: [A.status(404), O.emptyBody()] }),
  request("OAS-01", "GET /oauth2/authorize is not found", "GET", "/oauth2/authorize", { noAuth: true, noRedirect: true, asserts: [A.status(404), O.emptyBody()] }),
  request("OAS-01", "POST /oauth2/token is not found", "POST", "/oauth2/token", { noAuth: true, asserts: [A.status(404), O.emptyBody()] }),
  restoreConfig("OAS-01"),
]);

const prmChecks = () => [A.status(200), O.noStore(), A.custom("resource, authorization_servers, scopes and bearer methods", [
  `var b = pm.response.json(); var iss = ${lit(OTHER_ISS)};`,
  "pm.expect(b.resource, 'body: ' + pm.response.text()).to.equal(iss + '/mcp');",
  "pm.expect(b.authorization_servers).to.eql([iss]);",
  "pm.expect(b.scopes_supported).to.eql(['mcp']);",
  "pm.expect(b.bearer_methods_supported).to.eql(['header']);",
])];
const OAS02 = folder("OAS-02 Protected resource metadata", `Setup: mcp_server_auth_mode both, then oauth, each with issuer_url ${OTHER_ISS} (another origin than the requests); plain, /mcp and /mcp/team suffixes.`,
  ["both", "oauth"].flatMap((mode) => withMode("OAS-02", mode,
    ["", "/mcp", "/mcp/team"].map((suffix) => request("OAS-02", `${mode}: GET /.well-known/oauth-protected-resource${suffix}`, "GET", `/.well-known/oauth-protected-resource${suffix}`, { noAuth: true, asserts: prmChecks() })), OTHER_ISS)));

const OAS03 = folder("OAS-03 Authorization server metadata", `Setup: mcp_server_auth_mode both with issuer_url ${OTHER_ISS} (another origin than the requests).`, withMode("OAS-03", "both", [
  request("OAS-03", "GET /.well-known/oauth-authorization-server", "GET", "/.well-known/oauth-authorization-server", {
    noAuth: true,
    asserts: [A.status(200), O.noStore(), A.custom("issuer, endpoints and supported values", [
      `var b = pm.response.json(); var iss = ${lit(OTHER_ISS)};`,
      "pm.expect(b.issuer, 'body: ' + pm.response.text()).to.equal(iss);",
      "pm.expect(b.authorization_endpoint).to.equal(iss + '/oauth2/authorize');",
      "pm.expect(b.token_endpoint).to.equal(iss + '/oauth2/token');",
      "pm.expect(b.registration_endpoint).to.equal(iss + '/oauth2/register');",
      "pm.expect(b.jwks_uri).to.equal(iss + '/.well-known/jwks.json');",
      "pm.expect(b.response_types_supported).to.eql(['code']);",
      "pm.expect(b.grant_types_supported).to.eql(['authorization_code', 'refresh_token']);",
      "pm.expect(b.code_challenge_methods_supported).to.eql(['S256']);",
      "pm.expect(b.token_endpoint_auth_methods_supported).to.eql(['none']);",
      "pm.expect(b.authorization_response_iss_parameter_supported).to.equal(true);",
    ])],
  }),
], OTHER_ISS));

// oas06Checks are OAS-06's requests in one mode; clientVar keeps the client registered there.
const oas06Checks = (p, clientVar) => [
  request("OAS-06", `${p}control: an admin route without credentials is refused`, "GET", "/api/oauth2/sessions", { noAuth: true, noCookies: true, asserts: [A.status(401)] }),
  request("OAS-06", `${p}register without credentials`, "POST", "/oauth2/register", {
    noAuth: true, body: { client_name: "OAS-06 client", redirect_uris: [LOOPBACK_CB] }, asserts: [A.status(201)],
    extraTests: [`pm.collectionVariables.set('${clientVar}', pm.response.json().client_id);`],
  }),
  request("OAS-06", `${p}authorize without credentials`, "GET", goodAuthorize(clientVar), { noAuth: true, noRedirect: true, asserts: [A.status(302)] }),
  request("OAS-06", `${p}an empty token request`, "POST", "/oauth2/token", { noAuth: true, asserts: [A.status(400), O.error("unsupported_grant_type")] }),
  request("OAS-06", `${p}protected resource metadata without credentials`, "GET", "/.well-known/oauth-protected-resource", { noAuth: true, asserts: [A.status(200)] }),
  request("OAS-06", `${p}authorization server metadata without credentials`, "GET", "/.well-known/oauth-authorization-server", { noAuth: true, asserts: [A.status(200)] }),
];
const OAS06 = folder("OAS-06 Issuance endpoints are public", `Setup: ${BOTH}, dashboard auth on; the issuance and discovery requests carry no credentials.`, [
  ...withMode("OAS-06", "both", oas06Checks("", "oas06_client")),
]);

const OAS07 = folder("OAS-07 Register a client", `Setup: ${BOTH}.`, withMode("OAS-07", "both", [
  request("OAS-07", "register with a loopback redirect", "POST", "/oauth2/register", {
    noAuth: true, body: { client_name: "OAS-07 client", redirect_uris: [LOOPBACK_CB] },
    asserts: [A.status(201), A.custom("registration fields", [
      "var b = pm.response.json();",
      "pm.expect(b.client_id, 'body: ' + pm.response.text()).to.be.a('string').and.not.empty;",
      "pm.expect(Number.isInteger(b.client_id_issued_at), 'client_id_issued_at: ' + b.client_id_issued_at).to.equal(true);",
      "pm.expect(b.token_endpoint_auth_method).to.equal('none');",
      "pm.expect(b.grant_types).to.eql(['authorization_code']);",
      "pm.expect(b.response_types).to.eql(['code']);",
      "pm.expect(b.scope).to.equal('mcp');",
      `pm.expect(b.redirect_uris).to.eql([${lit(LOOPBACK_CB)}]);`,
    ])],
  }),
]));

const repeatA = (n) => `new Array(${n + 1}).join('a')`;
const regBodyPre = (exprBody) => [`pm.variables.set('reg_body', JSON.stringify(${exprBody}));`];
const regRaw = (id, name, pre, asserts) => request(id, name, "POST", "/oauth2/register", { noAuth: true, raw: "{{reg_body}}", pre, asserts });
const OAS08 = folder("OAS-08 Registration bounds", `Setup: ${BOTH}.`, withMode("OAS-08", "both", [
  regRaw("OAS-08", "a 2049-byte client_name is rejected", regBodyPre(`{ client_name: ${repeatA(2049)}, redirect_uris: [${lit(LOOPBACK_CB)}] }`), [A.status(400), O.error("invalid_client_metadata"), O.descEquals("client_name exceeds 2048 bytes")]),
  regRaw("OAS-08", "a 4097-byte scope is rejected", regBodyPre(`{ scope: ${repeatA(4097)}, redirect_uris: [${lit(LOOPBACK_CB)}] }`), [A.status(400), O.descEquals("scope exceeds 4096 bytes")]),
  regRaw("OAS-08", "33 redirect_uris are rejected", regBodyPre(`{ redirect_uris: (function () { var a = []; for (var i = 0; i < 33; i++) { a.push('http://127.0.0.1:' + (9000 + i) + '/cb'); } return a; })() }`), [A.status(400), O.error("invalid_redirect_uri"), O.desc("at most 32")]),
  regRaw("OAS-08", "a 2049-byte redirect_uri is rejected", regBodyPre(`{ redirect_uris: ['http://127.0.0.1:9999/' + ${repeatA(2049 - "http://127.0.0.1:9999/".length)}] }`), [A.status(400)]),
  regRaw("OAS-08", "empty redirect_uris are rejected", regBodyPre(`{ redirect_uris: [] }`), [A.status(400), O.descEquals("redirect_uris is required")]),
  request("OAS-08", "a malformed body is rejected", "POST", "/oauth2/register", { noAuth: true, raw: "{", asserts: [A.status(400), O.error("invalid_request")] }),
  regRaw("OAS-08", "a 2048-byte client_name is accepted", regBodyPre(`{ client_name: ${repeatA(2048)}, redirect_uris: [${lit(LOOPBACK_CB)}] }`), [A.status(201)]),
]));

const OAS09 = folder("OAS-09 Redirect policy with no allowlist", `Setup: ${BOTH}, no allowed_redirect_uris.`, withMode("OAS-09", "both", [
  ...["https://app.example/cb", "http://example.com/cb", "javascript:alert(1)", "http://localhost:1/cb#frag", "http://user@localhost:1/cb", "cursor:opaque"].map((uri) =>
    request("OAS-09", `redirect ${uri} is rejected`, "POST", "/oauth2/register", {
      noAuth: true, body: { redirect_uris: [uri] },
      asserts: [A.status(400), O.error("invalid_redirect_uri"), O.descEquals("redirect_uri must be a valid local callback or an administrator-approved remote callback")],
    })),
  ...["http://localhost:1/cb", "http://[::1]/cb", "cursor://anysphere.cursor-mcp/oauth/callback"].map((uri) =>
    request("OAS-09", `redirect ${uri} is accepted`, "POST", "/oauth2/register", { noAuth: true, body: { redirect_uris: [uri] }, asserts: [A.status(201)] })),
]));

const OAS11 = folder("OAS-11 Unsupported registration metadata", `Setup: ${BOTH}.`, withMode("OAS-11", "both", [
  request("OAS-11", "client_secret_basic is rejected", "POST", "/oauth2/register", { noAuth: true, body: { redirect_uris: [LOOPBACK_CB], token_endpoint_auth_method: "client_secret_basic" }, asserts: [A.status(400), O.error("invalid_client_metadata"), O.descEquals("only token_endpoint_auth_method=none is supported")] }),
  request("OAS-11", "client_credentials is rejected", "POST", "/oauth2/register", { noAuth: true, body: { redirect_uris: [LOOPBACK_CB], grant_types: ["client_credentials"] }, asserts: [A.status(400), O.error("invalid_client_metadata"), O.descEquals("unsupported grant_type")] }),
  request("OAS-11", "response_types token is rejected", "POST", "/oauth2/register", { noAuth: true, body: { redirect_uris: [LOOPBACK_CB], response_types: ["token"] }, asserts: [A.status(400), O.error("invalid_client_metadata"), O.descEquals("unsupported response_type")] }),
]));

const OAS12 = folder("OAS-12 Authorize success", `Setup: mcp_server_auth_mode both with issuer_url ${OTHER_ISS}, an origin other than the one requests go to; a registered client with a loopback redirect.`, withMode("OAS-12", "both", [
  register("OAS-12", "oas12_client"),
  request("OAS-12", "authorize with every parameter", "GET", goodAuthorize("oas12_client", { resource: `${OTHER_ISS}/mcp` }), { noAuth: true, noRedirect: true, asserts: [A.status(302), consentLocation(OTHER_ISS)] }),
  request("OAS-12", "authorize with resource and scope omitted", "GET", goodAuthorize("oas12_client", { resource: undefined, scope: undefined }), { noAuth: true, noRedirect: true, asserts: [A.status(302), consentLocation(OTHER_ISS)] }),
  request("OAS-12", "authorize from a different loopback port", "GET", goodAuthorize("oas12_client", { resource: `${OTHER_ISS}/mcp`, redirect_uri: "http://127.0.0.1:1234/cb" }), { noAuth: true, noRedirect: true, asserts: [A.status(302), consentLocation(OTHER_ISS)] }),
], OTHER_ISS));

const OAS13 = folder("OAS-13 Authorize errors answered directly", `Setup: ${BOTH}; a registered client.`, withMode("OAS-13", "both", [
  register("OAS-13", "oas13_client"),
  request("OAS-13", "an unknown client_id", "GET", goodAuthorize("oas13_client", { client_id: "oas13-no-such-client" }), { noAuth: true, noRedirect: true, asserts: [A.status(400), O.error("invalid_client"), O.descEquals("unknown client_id"), O.noLocation()] }),
  request("OAS-13", "a foreign redirect", "GET", goodAuthorize("oas13_client", { redirect_uri: "https://evil.example/cb" }), { noAuth: true, noRedirect: true, asserts: [A.status(400), O.error("invalid_redirect_uri"), O.descEquals("redirect_uri not registered for this client"), O.noLocation()] }),
  ...[["state", 8193], ["scope", 4097], ["code_challenge", 129]].map(([param, n]) =>
    request("OAS-13", `an oversized ${param}`, "GET", goodAuthorize("oas13_client", { [param]: "{{oversized}}" }), {
      noAuth: true, noRedirect: true, pre: [`pm.variables.set('oversized', ${repeatA(n)});`],
      asserts: [A.status(400), O.error("invalid_request"), O.descEquals("state, scope or code_challenge exceeds the maximum length"), O.noLocation()],
    })),
]));

const OAS14 = folder("OAS-14 Authorize errors redirected", `Setup: ${BOTH}; a registered client, state s1.`, withMode("OAS-14", "both", [
  register("OAS-14", "oas14_client"),
  request("OAS-14", "code_challenge_method plain", "GET", goodAuthorize("oas14_client", { code_challenge_method: "plain" }), { noAuth: true, noRedirect: true, asserts: [A.status(302), errorRedirect(null, "code_challenge_method must be S256")] }),
  request("OAS-14", "code_challenge_method missing", "GET", goodAuthorize("oas14_client", { code_challenge_method: undefined }), { noAuth: true, noRedirect: true, asserts: [A.status(302), errorRedirect(null, "code_challenge_method must be S256")] }),
  request("OAS-14", "no code_challenge", "GET", goodAuthorize("oas14_client", { code_challenge: undefined }), { noAuth: true, noRedirect: true, asserts: [A.status(302), errorRedirect(null, null)] }),
  request("OAS-14", "response_type token", "GET", goodAuthorize("oas14_client", { response_type: "token" }), { noAuth: true, noRedirect: true, asserts: [A.status(302), errorRedirect("unsupported_response_type", null)] }),
  request("OAS-14", "scope \"mcp admin\"", "GET", goodAuthorize("oas14_client", { scope: "mcp%20admin" }), { noAuth: true, noRedirect: true, asserts: [A.status(302), errorRedirect("invalid_scope", null)] }),
  request("OAS-14", "a foreign resource", "GET", goodAuthorize("oas14_client", { resource: "https://other/mcp" }), { noAuth: true, noRedirect: true, asserts: [A.status(302), errorRedirect("invalid_target", null)] }),
]));

// ================================================================ INF direct execute and code mode

// execBody posts a JSON body (or raw text) to /v1/mcp/tool/execute with no admin credentials; query is appended as is.
const execBody = (id, title, body, asserts, { query = "", raw, headers = [], extraTests = [] } = {}) => request(id, title, "POST", `/v1/mcp/tool/execute${query}`, {
  noAuth: true, noCookies: true, asserts, headers, extraTests, ...(raw !== undefined ? { raw } : { body }),
});
// toolCall is a chat-format tool call with id c1; args is an object or a literal arguments string.
const toolCall = (name, args) => ({ id: "c1", type: "function", function: { name, arguments: typeof args === "string" ? args : lit(args) } });

const meta = (id, title, tool, args, asserts) => execBody(id, title, toolCall(tool, args), asserts);
const code = (id, title, src, asserts) => meta(id, title, "executeToolCode", { code: src }, asserts);

// ================================================================ provider-injected tools on the mock LLM

const ONLY = process.env.TESTS ? new Set(process.env.TESTS.split(",").map((r) => r.trim())) : null;
const pick = (tests) => (ONLY ? tests.filter((t) => ONLY.has(t.name.split(" ")[0])) : tests);

const section = (name, description, tests) => folder(name, description, pick(tests));

const collection = {
  info: {
    _postman_id: "bifrost-mcp-harness",
    name: "Bifrost MCP Harness",
    description:
      "End-to-end MCP tests for Bifrost. Each folder inside a section is one test case, named by its ID, and its requests carry that ID as a prefix. Run with `make run-mcp-harness-test`, which boots its own Bifrost and enables dashboard auth before the run.",
    schema: "https://schema.getpostman.com/json/collection/v2.1.0/collection.json",
  },
  variable: [
    { key: "base_url", value: "http://localhost:8093" },
    { key: "admin_auth_header", value: "" },
  ],
  item: [
    section("ADM Client admin", "Client admin tests.", [ADM01, ADM05, ADM06, ADM62, ADM73]),
    section("EP Bifrost MCP endpoint", "Bifrost MCP endpoint tests.", [EP09]),
    section("OAS Bifrost OAuth server", "Bifrost OAuth server tests.", [OAS01, OAS02, OAS03, OAS06, OAS07, OAS08, OAS09, OAS11, OAS12, OAS13, OAS14, OAS31, OAS36]),
    section("INF Inference", "Inference tests.", [INF23, INF56]),
    section("LIB MCP library", "MCP library tests.", [LIB06, LIB08, LIB09, LIB10, LIB13, LIB16]),
    section("LOG Logs and telemetry", "Logs and telemetry tests.", [LOG27, LOG28, LOG29, LOG37, LOG40, LOG41]),
  ],
};

if (ONLY) {
  // Sections with none of the named tests are left out, so a subset build keeps only the sections it uses.
  collection.item = collection.item.filter((sec) => sec.item.length > 0);
  const seen = new Set(collection.item.flatMap((sec) => sec.item.map((t) => t.name.split(" ")[0])));
  const missing = [...ONLY].filter((r) => !seen.has(r));
  if (missing.length) throw new Error("TESTS names unknown tests: " + missing.join(", "));
}
writeCollection(OUT, collection, collection.item.flatMap((sec) => sec.item.map((t) => t.name.split(" ")[0])));
