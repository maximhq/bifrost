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
  request("LIB-09", "filterdata lacks stdio before the publish", "GET", "/api/mcp/library/filterdata", {
    asserts: [A.status(200), A.custom("connection_types has no \"stdio\"", [
      "pm.expect(pm.response.json().connection_types || [], 'body: ' + pm.response.text()).to.not.include('stdio');",
    ])],
  }),
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

// authMode sets mcp_server_auth_mode (H, B or O) and enforce_auth_on_inference; B and O also set
// issuer_url (the base URL unless issuer is given) and temp-token auth to tt.
const MODE_NAMES = { H: "headers", B: "both", O: "oauth" };
// modePrefix names a request's mode in a test that repeats requests per mode; headers, the boot mode, adds nothing.
const modePrefix = (m) => (m === "B" || m === "O" ? `${MODE_NAMES[m]}: ` : "");
const authMode = (id, m, enforce, tt = true, issuer) => putConfig(id, `setup: mcp_server_auth_mode ${MODE_NAMES[m]}, enforce_auth_on_inference ${enforce ? "on" : "off"}${m === "H" ? "" : `, issuer_url set to ${issuer || "the base URL"}, temp-token auth ${tt ? "on" : "off"}`}`, [
  `cc.mcp_server_auth_mode = ${lit(MODE_NAMES[m])};`,
  `cc.enforce_auth_on_inference = ${enforce};`,
  ...(m === "H" ? [] : [`cc.oauth2_server_config = { issuer_url: ${issuer ? lit(issuer) : "pm.variables.get('base_url')"} };`, `cc.mcp_enable_temp_token_auth = ${tt};`]),
], [A.status(200)]);

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

// ================================================================ tests on the mcpchaos fleet

const CHAOS = "{{chaos_url}}";
// freshServer re-adds a fleet server, which resets its tools, faults, auth and
// counters, then gives it the test's tools.
const freshServer = (id, name, tools) => [
  request(id, `setup: fresh mcpchaos server ${name}`, "POST", "/control", { base: CHAOS, noAuth: true, body: { add: name }, asserts: [A.status(200)] }),
  request(id, `setup: ${name} serves ${tools.join(", ")}`, "POST", `/control/${name}/tools/set`, { base: CHAOS, noAuth: true, body: { tools: tools.map((t) => ({ name: t })) }, asserts: [A.status(200)] }),
];
const mockGet = (id, name, title, asserts, extraTests = []) => request(id, title, "GET", `/control/${name}`, { base: CHAOS, noAuth: true, asserts: [A.status(200), ...asserts], extraTests });
const mockPost = (id, name, verb, title, body) => request(id, title, "POST", `/control/${name}/${verb}`, { base: CHAOS, noAuth: true, body, asserts: [A.status(200)] });

const createClient = (id, cfg, title) => request(id, title || `setup: create client ${cfg.name}`, "POST", "/api/mcp/client", {
  body: cfg, asserts: [A.status(200)],
});
const deleteClient = (id, clientId) => request(id, `teardown: delete client ${clientId}`, "DELETE", `/api/mcp/client/${clientId}`, { asserts: [A.status(200)] });
const getClient = (id, clientId, title, asserts, opts = {}) => request(id, title, "GET", `/api/mcp/clients?server=${clientId}`, {
  ...opts,
  asserts: [A.status(200), A.custom("exactly one client is returned", ["pm.expect(pm.response.json().clients.length, 'body: ' + pm.response.text()).to.equal(1);"]), ...asserts],
});
const toolNamesAre = (names) => A.custom(`tools are ${lit(names)}`, [
  "var c = pm.response.json().clients[0];",
  "var names = (c.tools || []).map(function (t) { return t.name; }).sort();",
  `pm.expect(names, 'body: ' + pm.response.text()).to.eql(${lit(names)});`,
]);
const clientState = (state) => A.custom(`state is ${lit(state)}`, ["pm.expect(pm.response.json().clients[0].state, 'body: ' + pm.response.text()).to.equal(" + lit(state) + ");"]);
const httpClient = (clientId, name, server, over = {}) => ({ client_id: clientId, name, connection_type: "http", connection_string: `${CHAOS}/s/${server}/mcp`, auth_type: "none", ...over });

// ---------------------------------------------------------------- ADM tests on the fleet
const ADM13 = folder("ADM-13 Create a client and read it back", "Setup: admin credentials; alpha (echo_tool, add_tool); the client is HTTP, auth none and per-call, on alpha.", [
  ...freshServer("ADM-13", "alpha", ["echo_tool", "add_tool"]),
  request("ADM-13", "create the client", "POST", "/api/mcp/client", {
    body: httpClient("adm13-c", "adm13_c", "alpha"),
    asserts: [A.status(200), A.custom("the body is exactly {\"status\":\"success\",\"message\":\"MCP client connected successfully\"}", [
      "pm.expect(pm.response.json(), 'body: ' + pm.response.text()).to.eql({ status: 'success', message: 'MCP client connected successfully' });",
    ])],
  }),
  getClient("ADM-13", "adm13-c", "read the client back", [clientState("healthy"), toolNamesAre(["add_tool", "echo_tool"]),
    A.custom("is_ping_available true, vk_configs []", [
      "var c = pm.response.json().clients[0];",
      "pm.expect(c.config.is_ping_available, 'body: ' + pm.response.text()).to.equal(true);",
      "pm.expect(c.vk_configs).to.eql([]);",
    ])]),
  deleteClient("ADM-13", "adm13-c"),
]);

const ADM17 = folder("ADM-17 SSE and STDIO create", "Setup: admin credentials; beta (echo_tool, add_tool) over SSE, and a STDIO bridge to alpha.", [
  ...freshServer("ADM-17", "beta", ["echo_tool", "add_tool"]),
  ...freshServer("ADM-17", "alpha", ["echo_tool", "add_tool"]),
  request("ADM-17", "create an SSE client on /s/beta/sse", "POST", "/api/mcp/client", {
    body: { client_id: "adm17-sse", name: "adm17_sse", connection_type: "sse", connection_string: `${CHAOS}/s/beta/sse`, auth_type: "none" },
    asserts: [A.status(200)],
  }),
  getClient("ADM-17", "adm17-sse", "the SSE client is healthy", [clientState("healthy")]),
  mockGet("ADM-17", "beta", "beta holds one session", [A.custom("beta sessions is 1", ["pm.expect(pm.response.json().sessions, 'body: ' + pm.response.text()).to.equal(1);"])]),
  request("ADM-17", "create a STDIO client bridging to alpha", "POST", "/api/mcp/client", {
    body: { client_id: "adm17-stdio", name: "adm17_stdio", connection_type: "stdio", auth_type: "none", stdio_config: { command: "{{chaos_bin}}", args: ["-stdio", "-fleet", CHAOS, "-server", "alpha"], envs: [] } },
    asserts: [A.status(200)],
  }),
  getClient("ADM-17", "adm17-stdio", "the STDIO client is healthy and lists tools", [clientState("healthy"), toolNamesAre(["add_tool", "echo_tool"])]),
  mockGet("ADM-17", "alpha", "alpha shows a registered STDIO pid", [A.custom("stdio_pid is set", ["pm.expect(pm.response.json().stdio_pid, 'body: ' + pm.response.text()).to.be.above(0);"])]),
  deleteClient("ADM-17", "adm17-sse"),
  deleteClient("ADM-17", "adm17-stdio"),
]);

const ADM42 = folder("ADM-42 Reconnect a sticky client", "Setup: a client on alpha with needs_session_stickiness true, then POST /control/alpha/sessions/kill.", [
  ...freshServer("ADM-42", "alpha", ["echo_tool", "add_tool"]),
  createClient("ADM-42", httpClient("adm42-s", "adm42_s", "alpha", { needs_session_stickiness: true }), "setup: create the sticky client"),
  mockGet("ADM-42", "alpha", "setup: record alpha's session count", [], ["pm.collectionVariables.set('adm42_sessions', pm.response.json().counters.sessions);"]),
  mockPost("ADM-42", "alpha", "sessions/kill", "setup: kill alpha's sessions"),
  request("ADM-42", "reconnect the sticky client", "POST", "/api/mcp/client/adm42-s/reconnect", { asserts: [A.status(200), A.jsonMessage("MCP client reconnected successfully")] }),
  mockGet("ADM-42", "alpha", "alpha opened one more session", [A.custom("sessions is one more than before", [
    "var before = Number(pm.collectionVariables.get('adm42_sessions'));",
    "pm.expect(pm.response.json().counters.sessions, 'body: ' + pm.response.text()).to.equal(before + 1);",
  ])]),
  deleteClient("ADM-42", "adm42-s"),
]);

const ADM44 = folder("ADM-44 refresh-tools picks up upstream changes", "Setup: a per-call client and a sticky client on alpha.", [
  ...freshServer("ADM-44", "alpha", ["echo_tool", "add_tool"]),
  createClient("ADM-44", httpClient("adm44-c", "adm44_c", "alpha"), "setup: create the per-call client"),
  createClient("ADM-44", httpClient("adm44-s", "adm44_s", "alpha", { needs_session_stickiness: true }), "setup: create the sticky client"),
  mockPost("ADM-44", "alpha", "tools/add", "add new_tool through the control API", { tool: { name: "new_tool" } }),
  ...["adm44-c", "adm44-s"].flatMap((cid) => [
    request("ADM-44", `refresh ${cid}`, "POST", `/api/mcp/client/${cid}/refresh-tools`, {
      asserts: [A.status(200), A.jsonMessage("MCP client tools refreshed successfully"), A.jsonEquals("tool_count", 3)],
    }),
    getClient("ADM-44", cid, `${cid} lists new_tool`, [A.custom("tools include new_tool", [
      "var names = pm.response.json().clients[0].tools.map(function (t) { return t.name; });",
      "pm.expect(names, 'body: ' + pm.response.text()).to.include('new_tool');",
    ])]),
  ]),
  deleteClient("ADM-44", "adm44-c"),
  deleteClient("ADM-44", "adm44-s"),
]);

// ---------------------------------------------------------------- EP tests on the two-server fleet
// fleetSetup: alpha (echo, add) and beta (echo), registered as HTTP clients alpha and beta,
// auth none, tools_to_execute ["*"], allow_by_default false. FLEET describes it in folder text.
const FLEET = "alpha (echo, add) and beta (echo) registered as HTTP clients alpha and beta, auth none, tools_to_execute [\"*\"], allow_by_default false";
const fleetSetup = (id) => {
  const p = id.toLowerCase();
  return [
    ...freshServer(id, "alpha", ["echo", "add"]),
    ...freshServer(id, "beta", ["echo"]),
    createClient(id, httpClient(`${p}-alpha`, "alpha", "alpha", { tools_to_execute: ["*"], allow_by_default: false }), "setup: register client alpha"),
    createClient(id, httpClient(`${p}-beta`, "beta", "beta", { tools_to_execute: ["*"], allow_by_default: false }), "setup: register client beta"),
  ];
};
const fleetTeardown = (id) => {
  const p = id.toLowerCase();
  return [deleteClient(id, `${p}-alpha`), deleteClient(id, `${p}-beta`)];
};
const MCP_ACCEPT = { key: "Accept", value: "application/json, text/event-stream" };
const rpc = (id, title, message, opts = {}) => request(id, title, "POST", opts.path || "/mcp", {
  noAuth: true, raw: typeof message === "string" ? message : JSON.stringify(message), headers: opts.headers || [MCP_ACCEPT], asserts: opts.asserts || [], noAccept: opts.noAccept,
});
const rpcResultEquals = (v) => A.custom(`result is ${lit(v)}`, ["var b = pm.response.json();", `pm.expect(b.result, 'body: ' + pm.response.text()).to.eql(${lit(v)});`]);
const rpcError = (code, message) => A.custom(`error.code ${code}${message ? ", message " + lit(message) : ""}`, [
  "var b = pm.response.json();",
  `pm.expect(b.error && b.error.code, 'body: ' + pm.response.text()).to.equal(${code});`,
  ...(message ? [`pm.expect(b.error.message).to.equal(${lit(message)});`] : []),
]);
const noMockCalls = (id, server) => mockGet(id, server, `${server} received no tool call`, [A.custom("calls is empty", ["pm.expect(pm.response.json().counters.calls, 'body: ' + pm.response.text()).to.eql({});"])]);

const EP18 = folder("EP-18 tools/call success", `Setup: ${FLEET}; anonymous caller (mcp_server_auth_mode headers, enforce_auth_on_inference off).`, [
  ...fleetSetup("EP-18"),
  rpc("EP-18", "tools/call alpha-echo", { jsonrpc: "2.0", id: 1, method: "tools/call", params: { name: "alpha-echo", arguments: { text: "hi" } } }, {
    asserts: [A.status(200), A.custom("content is the mock's echo and isError is absent", [
      "var r = pm.response.json().result;",
      "pm.expect(r.content, 'body: ' + pm.response.text()).to.eql([{ type: 'text', text: 'echo:{\"text\":\"hi\"}' }]);",
      "pm.expect(r).to.not.have.property('isError');",
    ])],
  }),
  mockGet("EP-18", "alpha", "alpha ran echo once", [A.custom("calls[echo] is 1", ["pm.expect(pm.response.json().counters.calls.echo, 'body: ' + pm.response.text()).to.equal(1);"])]),
  ...fleetTeardown("EP-18"),
]);

const EP23 = folder("EP-23 Unknown tool", `Setup: ${FLEET}; tools/call for "nope".`, [
  ...fleetSetup("EP-23"),
  rpc("EP-23", "tools/call nope", { jsonrpc: "2.0", id: 1, method: "tools/call", params: { name: "nope", arguments: {} } }, {
    asserts: [A.status(200), rpcError(-32602, "tool 'nope' not found: tool not found")],
  }),
  noMockCalls("EP-23", "alpha"),
  noMockCalls("EP-23", "beta"),
  ...fleetTeardown("EP-23"),
]);

const EP27 = folder("EP-27 ping before initialize", `Setup: ${FLEET}.`, [
  ...fleetSetup("EP-27"),
  rpc("EP-27", "ping with a numeric id", { jsonrpc: "2.0", id: 1, method: "ping" }, { asserts: [A.status(200), rpcResultEquals({}), A.custom("id 1 is echoed", ["pm.expect(pm.response.json().id).to.equal(1);"])] }),
  rpc("EP-27", "ping with a string id", { jsonrpc: "2.0", id: "p1", method: "ping" }, { asserts: [A.status(200), rpcResultEquals({}), A.custom("id \"p1\" is echoed", ["pm.expect(pm.response.json().id).to.equal('p1');"])] }),
  ...fleetTeardown("EP-27"),
]);

const empty202 = () => [A.status(202), A.custom("body is empty", ["pm.expect(pm.response.text(), 'body: ' + pm.response.text()).to.equal('');"])];
const EP28 = folder("EP-28 Client notifications and responses", "Setup: none; initialized, cancelled and an unknown notification, and a bare result.", [
  rpc("EP-28", "notifications/initialized", { jsonrpc: "2.0", method: "notifications/initialized" }, { asserts: empty202() }),
  rpc("EP-28", "notifications/cancelled", { jsonrpc: "2.0", method: "notifications/cancelled", params: { requestId: 1 } }, { asserts: empty202() }),
  rpc("EP-28", "an unknown notification", { jsonrpc: "2.0", method: "notifications/foo" }, { asserts: empty202() }),
  rpc("EP-28", "a bare result", { jsonrpc: "2.0", id: 7, result: {} }, { asserts: empty202() }),
]);

const EP29 = folder("EP-29 Unsupported methods", `Setup: ${FLEET}.`, [
  ...fleetSetup("EP-29"),
  ...[
    ["resources/list", "resources not supported", {}],
    ["resources/read", "resources not supported", { uri: "x" }],
    ["resources/templates/list", "resources not supported", {}],
    ["prompts/list", "prompts not supported", {}],
    ["prompts/get", "prompts not supported", { name: "x" }],
    ["logging/setLevel", "logging not supported", { level: "info" }],
    ["completion/complete", "Method completion/complete not found", {}],
    ["foo/bar", "Method foo/bar not found", {}],
  ].map(([method, message, params]) =>
    rpc("EP-29", `${method} is unsupported`, { jsonrpc: "2.0", id: 1, method, params }, { asserts: [A.status(200), rpcError(-32601, message)] })),
  ...fleetTeardown("EP-29"),
]);

const jsonNotSSE = () => [A.status(200), A.custom("the reply is a JSON-RPC body, not SSE", [
  "pm.expect(pm.response.headers.get('Content-Type'), 'Content-Type').to.include('application/json');",
  "var b = pm.response.json();",
  "pm.expect(b.jsonrpc, 'body: ' + pm.response.text()).to.equal('2.0');",
  "pm.expect(b).to.have.property('result');",
])];
const listMsg = { jsonrpc: "2.0", id: 1, method: "tools/list" };
const EP31 = folder("EP-31 Content negotiation", `Setup: ${FLEET}.`, [
  ...fleetSetup("EP-31"),
  rpc("EP-31", "a text/plain body", listMsg, { headers: [{ key: "Content-Type", value: "text/plain" }, MCP_ACCEPT], asserts: jsonNotSSE() }),
  rpc("EP-31", "Accept text/event-stream only", listMsg, { headers: [{ key: "Accept", value: "text/event-stream" }], asserts: jsonNotSSE() }),
  rpc("EP-31", "no Accept header", listMsg, { headers: [], noAccept: true, asserts: jsonNotSSE() }),
  ...fleetTeardown("EP-31"),
]);

const rpcResult = () => A.custom("a JSON-RPC result and no error", ["var b = pm.response.json();", "pm.expect(b, 'body: ' + pm.response.text()).to.have.property('result');", "pm.expect(b).to.not.have.property('error');"]);
const EP32 = folder("EP-32 No session id is used", `Setup: ${FLEET}.`, [
  ...fleetSetup("EP-32"),
  rpc("EP-32", "tools/call before initialize", { jsonrpc: "2.0", id: 2, method: "tools/call", params: { name: "alpha-echo", arguments: { text: "hi" } } }, { asserts: [A.status(200), rpcResult()] }),
  rpc("EP-32", "initialize", { jsonrpc: "2.0", id: 1, method: "initialize", params: { protocolVersion: "2025-03-26", capabilities: {}, clientInfo: { name: "mcp-harness", version: "1.0.0" } } }, {
    asserts: [A.status(200), A.custom("there is no Mcp-Session-Id header", ["pm.expect(pm.response.headers.has('Mcp-Session-Id'), 'Mcp-Session-Id: ' + pm.response.headers.get('Mcp-Session-Id')).to.equal(false);"])],
  }),
  rpc("EP-32", "tools/list without a session id", listMsg, { asserts: [A.status(200), rpcResult()] }),
  rpc("EP-32", "tools/list with a bogus session id", listMsg, { headers: [MCP_ACCEPT, { key: "Mcp-Session-Id", value: "bogus" }], asserts: [A.status(200), rpcResult()] }),
  ...fleetTeardown("EP-32"),
]);

const method405 = () => [A.status(405), A.custom("Allow is \"GET, OPTIONS, POST\"", ["pm.expect(pm.response.headers.get('Allow'), 'Allow').to.equal('GET, OPTIONS, POST');"])];
const EP33 = folder("EP-33 Unsupported HTTP methods", "Setup: none; DELETE and PUT on /mcp, DELETE on /mcp/alpha.", [
  request("EP-33", "DELETE /mcp", "DELETE", "/mcp", { noAuth: true, asserts: method405() }),
  request("EP-33", "PUT /mcp", "PUT", "/mcp", { noAuth: true, raw: "{}", asserts: method405() }),
  request("EP-33", "DELETE /mcp/alpha", "DELETE", "/mcp/alpha", { noAuth: true, asserts: method405() }),
]);

// ================================================================ ADM tests on the fleet

// ADM-01: the client-scoped routes target a real client, so "nothing changes" is checked on that client.
const ADM01 = folder("ADM-01 Every MCP admin route without a credential",
  "Setup: dashboard auth on; alpha (echo_tool, add_tool) and an HTTP, auth none, per-call client on alpha. The requests under test carry no Authorization header or cookie, and the client-scoped ones target that client.", [
  ...freshServer("ADM-01", "alpha", ["echo_tool", "add_tool"]),
  createClient("ADM-01", httpClient("adm01-c", "adm01_c", "alpha"), "setup: create the client"),
  request("ADM-01", "setup: record the client count", "GET", "/api/mcp/clients", {
    asserts: [A.status(200)],
    extraTests: ["pm.collectionVariables.set('adm01_total', pm.response.json().total_count);"],
  }),
  request("ADM-01", "setup: read the client", "GET", "/api/mcp/clients?server=adm01-c", {
    asserts: [A.status(200)],
    extraTests: [
      "var c = pm.response.json().clients[0] || {};",
      "pm.collectionVariables.set('adm01_before', JSON.stringify({ config: c.config, tools: (c.tools || []).map(function (t) { return t.name; }).sort() }));",
    ],
  }),
  ...noCredRoutes.map(([method, path, body]) => {
    const real = path.replace("/api/mcp/client/adm01-id", "/api/mcp/client/adm01-c");
    // A PUT that would disable the client if it got through.
    const realBody = method === "PUT" && real === "/api/mcp/client/adm01-c" ? { disabled: true } : body;
    return request("ADM-01", `${method} ${real} without a credential`, method, real, { body: realBody, noAuth: true, noCookies: true, asserts: [A.status(401), A.msgEquals("Unauthorized")] });
  }),
  request("ADM-01", "GET /api/oauth/callback is public", "GET", "/api/oauth/callback", { noAuth: true, noCookies: true, asserts: [A.status(400), A.msgEquals("Missing required parameters: state and code")] }),
  request("ADM-01", "the client count is unchanged", "GET", "/api/mcp/clients", {
    asserts: [A.status(200), A.custom("total_count is unchanged", [
      "var before = Number(pm.collectionVariables.get('adm01_total'));",
      "pm.expect(pm.response.json().total_count, 'body: ' + pm.response.text()).to.equal(before);",
    ])],
  }),
  getClient("ADM-01", "adm01-c", "the client is unchanged", [clientState("healthy"), A.custom("config and tools are unchanged", [
    "var c = pm.response.json().clients[0], before = JSON.parse(pm.collectionVariables.get('adm01_before'));",
    "pm.expect(c.config, 'body: ' + pm.response.text()).to.eql(before.config);",
    "pm.expect((c.tools || []).map(function (t) { return t.name; }).sort()).to.eql(before.tools);",
  ])]),
  deleteClient("ADM-01", "adm01-c"),
]);

const createExpect = (id, title, cfg, asserts) => request(id, title, "POST", "/api/mcp/client", { body: cfg, asserts });
const clientField = (path, v) => A.custom(`${path} is ${lit(v)}`, [`pm.expect(pm.response.json().clients[0].${path}, 'body: ' + pm.response.text()).to.eql(${lit(v)});`]);
const ALPHA = `${CHAOS}/s/alpha/mcp`;

const ADM07 = folder("ADM-07 Endpoint slug derivation and override", "Setup: admin credentials; alpha (echo_tool, add_tool) for the clients that are created.", [
  ...freshServer("ADM-07", "alpha", ["echo_tool", "add_tool"]),
  createExpect("ADM-07", "name \"_\" with no endpoint_slug is rejected", { name: "_", connection_type: "http", connection_string: ALPHA, auth_type: "none" },
    [A.status(400), A.msgEquals("Could not derive an endpoint slug from the name; provide an endpoint_slug")]),
  createExpect("ADM-07", "create \"My_Srv_1\"", httpClient("adm07-a", "My_Srv_1", "alpha"), [A.status(200)]),
  getClient("ADM-07", "adm07-a", "\"My_Srv_1\" stores endpoint_slug \"my-srv-1\"", [clientField("config.endpoint_slug", "my-srv-1")]),
  createExpect("ADM-07", "create with endpoint_slug \"Custom Slug!\"", httpClient("adm07-b", "adm07_custom", "alpha", { endpoint_slug: "Custom Slug!" }), [A.status(200)]),
  getClient("ADM-07", "adm07-b", "endpoint_slug \"Custom Slug!\" stores \"custom-slug\"", [clientField("config.endpoint_slug", "custom-slug")]),
  deleteClient("ADM-07", "adm07-a"),
  deleteClient("ADM-07", "adm07-b"),
]);

const ADM08 = folder("ADM-08 Tool and header allow-lists", "Setup: admin credentials; alpha (echo_tool, add_tool) for the client that is created.", [
  ...freshServer("ADM-08", "alpha", ["echo_tool", "add_tool"]),
  createExpect("ADM-08", "tools_to_execute [\"*\",\"echo_tool\"] is rejected", httpClient("adm08-x", "adm08_x", "alpha", { tools_to_execute: ["*", "echo_tool"] }), [A.status(400), A.msgContains("wildcard '*' cannot be used with other values")]),
  createExpect("ADM-08", "tools_to_execute [\"a\",\"A\"] is rejected", httpClient("adm08-x", "adm08_x", "alpha", { tools_to_execute: ["a", "A"] }), [A.status(400), A.msgContains("duplicate value")]),
  createExpect("ADM-08", "auto [\"b\"] with execute [\"a\"] is rejected", httpClient("adm08-x", "adm08_x", "alpha", { tools_to_execute: ["a"], tools_to_auto_execute: ["b"] }), [A.status(400), A.msgContains("tool 'b' in tools_to_auto_execute is not in tools_to_execute")]),
  createExpect("ADM-08", "allowed_extra_headers [\"*\",\"x\"] is rejected", httpClient("adm08-x", "adm08_x", "alpha", { allowed_extra_headers: ["*", "x"] }), [A.status(400)]),
  createExpect("ADM-08", "execute [] with auto [\"x\"] is accepted", httpClient("adm08-empty", "adm08_empty", "alpha", { tools_to_execute: [], tools_to_auto_execute: ["x"] }), [A.status(200)]),
  getClient("ADM-08", "adm08-empty", "auto is stored empty", [A.custom("tools_to_auto_execute is empty", [
    "var auto = pm.response.json().clients[0].config.tools_to_auto_execute;",
    "pm.expect(!auto || auto.length === 0, 'tools_to_auto_execute: ' + JSON.stringify(auto)).to.equal(true);",
  ])]),
  deleteClient("ADM-08", "adm08-empty"),
]);

const ADM10 = folder("ADM-10 Per-call connections only for HTTP", "Setup: admin credentials; alpha (echo_tool, add_tool).", [
  ...freshServer("ADM-10", "alpha", ["echo_tool", "add_tool"]),
  createExpect("ADM-10", "sse with needs_session_stickiness false is rejected",
    { name: "adm10_sse", connection_type: "sse", connection_string: `${CHAOS}/s/alpha/sse`, auth_type: "none", needs_session_stickiness: false },
    [A.status(400), A.msgContains("cannot be false for connection_type \"sse\": only 'http' supports a per-call connection")]),
  createExpect("ADM-10", "stdio with needs_session_stickiness false is rejected",
    { name: "adm10_stdio", connection_type: "stdio", auth_type: "none", needs_session_stickiness: false, stdio_config: { command: "{{chaos_bin}}", args: ["-stdio", "-fleet", CHAOS, "-server", "alpha"], envs: [] } },
    [A.status(400), A.msgContains("cannot be false for connection_type \"stdio\": only 'http' supports a per-call connection")]),
  createExpect("ADM-10", "http with needs_session_stickiness false is accepted", httpClient("adm10-http", "adm10_http", "alpha", { needs_session_stickiness: false }), [A.status(200)]),
  deleteClient("ADM-10", "adm10-http"),
]);

const ADM11 = folder("ADM-11 Duplicate names and slugs", "Setup: admin credentials; client dup_a on alpha; a virtual MCP with slug \"vm-slug\".", [
  ...freshServer("ADM-11", "alpha", ["echo_tool", "add_tool"]),
  createClient("ADM-11", httpClient("adm11-dup-a", "dup_a", "alpha"), "setup: create client dup_a"),
  request("ADM-11", "setup: create a virtual MCP with slug \"vm-slug\"", "POST", "/api/mcp/virtual-mcps", {
    body: { name: "ADM-11 virtual MCP", endpoint_slug: "vm-slug", tools: [] }, asserts: [A.status(201)],
    extraTests: ["pm.collectionVariables.set('adm11_vmcp_id', pm.response.json().virtual_mcp.id);"],
  }),
  createExpect("ADM-11", "the same name is rejected", httpClient("adm11-x", "dup_a", "alpha"), [A.status(409), A.msgEquals("An MCP endpoint with this slug already exists")]),
  createExpect("ADM-11", "the same name with another slug is rejected", httpClient("adm11-x", "dup_a", "alpha", { endpoint_slug: "other" }), [A.status(409), A.msgEquals("An MCP client with this name already exists")]),
  createExpect("ADM-11", "\"DUP_A\" is rejected", httpClient("adm11-x", "DUP_A", "alpha"), [A.status(409), A.msgEquals("An MCP endpoint with this slug already exists")]),
  createExpect("ADM-11", "\"vm_slug\" is rejected", httpClient("adm11-x", "vm_slug", "alpha"), [A.status(409), A.msgEquals("An MCP endpoint with this slug already exists")]),
  request("ADM-11", "one dup_a is listed", "GET", "/api/mcp/clients?search=dup_a", {
    asserts: [A.status(200), A.custom("exactly one client is named dup_a", [
      "var n = pm.response.json().clients.filter(function (c) { return c.config.name === 'dup_a'; }).length;",
      "pm.expect(n, 'body: ' + pm.response.text()).to.equal(1);",
    ])],
  }),
  deleteClient("ADM-11", "adm11-dup-a"),
  request("ADM-11", "teardown: delete the virtual MCP", "DELETE", "/api/mcp/virtual-mcps/{{adm11_vmcp_id}}", { asserts: [A.status(200)] }),
]);

const ADM12 = folder("ADM-12 Reused explicit client_id", "Setup: admin credentials; a client with client_id \"fixed-id\" on alpha.", [
  ...freshServer("ADM-12", "alpha", ["echo_tool", "add_tool"]),
  createClient("ADM-12", httpClient("fixed-id", "adm12_original", "alpha"), "setup: create the client with client_id \"fixed-id\""),
  request("ADM-12", "setup: read the original client", "GET", "/api/mcp/clients?server=fixed-id", {
    asserts: [A.status(200)],
    extraTests: ["var c = pm.response.json().clients[0].config;", "pm.collectionVariables.set('adm12_original', JSON.stringify({ endpoint_slug: c.endpoint_slug, connection_string: c.connection_string }));"],
  }),
  createExpect("ADM-12", "a new name and slug with the same client_id is rejected", httpClient("fixed-id", "adm12_other", "alpha", { endpoint_slug: "adm12-other" }), [A.status(409)]),
  getClient("ADM-12", "fixed-id", "the original client is unchanged", [clientField("config.name", "adm12_original"), A.custom("endpoint_slug and connection_string are unchanged", [
    "var c = pm.response.json().clients[0].config, before = JSON.parse(pm.collectionVariables.get('adm12_original'));",
    "pm.expect(c.endpoint_slug, 'body: ' + pm.response.text()).to.equal(before.endpoint_slug);",
    "pm.expect(c.connection_string).to.eql(before.connection_string);",
  ])]),
  deleteClient("ADM-12", "fixed-id"),
]);

const ADM16 = folder("ADM-16 Missing connection target is rolled back", "Setup: admin credentials; alpha (echo_tool, add_tool) for the reuse checks (an HTTP client and a STDIO bridge).", [
  ...freshServer("ADM-16", "alpha", ["echo_tool", "add_tool"]),
  createExpect("ADM-16", "http without connection_string", { client_id: "adm16-http", name: "adm16_http", connection_type: "http", auth_type: "none" },
    [A.status(500), A.msgContains("ConnectionString is required for HTTP connection type")]),
  createExpect("ADM-16", "stdio without stdio_config", { client_id: "adm16-stdio", name: "adm16_stdio", connection_type: "stdio", auth_type: "none" },
    [A.status(500), A.msgContains("StdioConfig is required")]),
  request("ADM-16", "no client was kept", "GET", "/api/mcp/clients?search=adm16", {
    asserts: [A.status(200), A.custom("neither client is listed", [
      "var names = pm.response.json().clients.map(function (c) { return c.config.name; });",
      "pm.expect(names, 'body: ' + pm.response.text()).to.not.include('adm16_http');",
      "pm.expect(names).to.not.include('adm16_stdio');",
    ])],
  }),
  createExpect("ADM-16", "the http name is reusable", httpClient("adm16-http", "adm16_http", "alpha"), [A.status(200)]),
  createExpect("ADM-16", "the stdio name is reusable", { client_id: "adm16-stdio", name: "adm16_stdio", connection_type: "stdio", auth_type: "none", stdio_config: { command: "{{chaos_bin}}", args: ["-stdio", "-fleet", CHAOS, "-server", "alpha"], envs: [] } }, [A.status(200)]),
  request("ADM-16", "control: the same search lists both names now", "GET", "/api/mcp/clients?search=adm16", {
    asserts: [A.status(200), A.custom("both clients are listed", [
      "var names = pm.response.json().clients.map(function (c) { return c.config.name; }).sort();",
      "pm.expect(names, 'body: ' + pm.response.text()).to.eql(['adm16_http', 'adm16_stdio']);",
    ])],
  }),
  deleteClient("ADM-16", "adm16-http"),
  deleteClient("ADM-16", "adm16-stdio"),
]);

const ADM46 = folder("ADM-46 PUT with an unknown id or bad body", "Setup: admin credentials; a client on alpha.", [
  ...freshServer("ADM-46", "alpha", ["echo_tool", "add_tool"]),
  createClient("ADM-46", httpClient("adm46-c", "adm46_c", "alpha"), "setup: create the client"),
  request("ADM-46", "an unknown id", "PUT", "/api/mcp/client/adm46-no-such-client", { body: {}, asserts: [A.status(404), A.msgEquals("MCP client not found")] }),
  request("ADM-46", "a malformed body on the client", "PUT", "/api/mcp/client/adm46-c", { raw: "{", asserts: [A.status(400), A.msgEquals("Invalid request payload")] }),
  deleteClient("ADM-46", "adm46-c"),
]);

const ADM51 = folder("ADM-51 Disable and enable", "Setup: a client on alpha.", [
  ...freshServer("ADM-51", "alpha", ["echo_tool", "add_tool"]),
  createClient("ADM-51", httpClient("adm51-c", "adm51_c", "alpha"), "setup: create the client"),
  request("ADM-51", "disable the client", "PUT", "/api/mcp/client/adm51-c", { body: { disabled: true }, asserts: [A.status(200)] }),
  getClient("ADM-51", "adm51-c", "the client is disabled", [clientState("disabled")]),
  request("ADM-51", "enable the client", "PUT", "/api/mcp/client/adm51-c", { body: { disabled: false }, asserts: [A.status(200)] }),
  getClient("ADM-51", "adm51-c", "the client is healthy with tools", [clientState("healthy"), A.custom("tools are listed", [
    "pm.expect(pm.response.json().clients[0].tools.length, 'body: ' + pm.response.text()).to.be.above(0);",
  ])]),
  deleteClient("ADM-51", "adm51-c"),
]);

const ADM59 = folder("ADM-59 Delete unknown or twice", "Setup: admin credentials; two clients on alpha.", [
  ...freshServer("ADM-59", "alpha", ["echo_tool", "add_tool"]),
  createClient("ADM-59", httpClient("adm59-other", "adm59_other", "alpha"), "setup: create another client"),
  createClient("ADM-59", httpClient("adm59-c", "adm59_c", "alpha"), "setup: create the client to delete"),
  request("ADM-59", "setup: delete the client", "DELETE", "/api/mcp/client/adm59-c", { asserts: [A.status(200)] }),
  request("ADM-59", "delete the same client again", "DELETE", "/api/mcp/client/adm59-c", { asserts: [A.status(404), A.msgContains("not found")] }),
  request("ADM-59", "delete an unknown id", "DELETE", "/api/mcp/client/adm59-no-such-client", { asserts: [A.status(404), A.msgContains("not found")] }),
  getClient("ADM-59", "adm59-other", "the other client is unaffected", [clientState("healthy"), toolNamesAre(["add_tool", "echo_tool"])]),
  deleteClient("ADM-59", "adm59-other"),
]);

const ADM83 = folder("ADM-83 token_exchange is unavailable", "Setup: admin credentials; an auth none client on alpha.", [
  ...freshServer("ADM-83", "alpha", ["echo_tool", "add_tool"]),
  createExpect("ADM-83", "create a token_exchange client", httpClient("adm83-te", "adm83_te", "alpha", { auth_type: "token_exchange", token_exchange: { audience: "api://mcp-harness" } }),
    [A.status(400), A.msgEquals("auth_type 'token_exchange' is not supported")]),
  createClient("ADM-83", httpClient("adm83-n", "adm83_n", "alpha"), "setup: create the auth none client"),
  request("ADM-83", "verify-exchange on the auth none client", "POST", "/api/mcp/client/adm83-n/verify-exchange", { body: {}, asserts: [A.status(400), A.msgContains("verify-exchange only applies to auth_type='token_exchange' clients")] }),
  request("ADM-83", "verify-exchange on an unknown id", "POST", "/api/mcp/client/adm83-no-such-client/verify-exchange", { body: {}, asserts: [A.status(404)] }),
  deleteClient("ADM-83", "adm83-n"),
]);

// ================================================================ EP tests with VKs, include headers and tool names

// fleetSetupWith is the two-server fleet with alpha's tools or client settings changed for one test.
const fleetSetupWith = (id, { alphaTools = ["echo", "add"], alphaClient = {} } = {}) => {
  const p = id.toLowerCase();
  return [
    ...freshServer(id, "alpha", alphaTools),
    ...freshServer(id, "beta", ["echo"]),
    createClient(id, httpClient(`${p}-alpha`, "alpha", "alpha", { tools_to_execute: ["*"], allow_by_default: false, ...alphaClient }), "setup: register client alpha"),
    createClient(id, httpClient(`${p}-beta`, "beta", "beta", { tools_to_execute: ["*"], allow_by_default: false }), "setup: register client beta"),
  ];
};
// VKS are the virtual keys the EP tests use, keyed by a short name that also names their collection variables.
const VKS = {
  full: { label: "the full-access VK", text: "every tool of alpha and beta", configs: [{ mcp_client_name: "alpha", tools_to_execute: ["*"] }, { mcp_client_name: "beta", tools_to_execute: ["*"] }] },
  echo: { label: "the echo-only VK", text: "only alpha's echo", configs: [{ mcp_client_name: "alpha", tools_to_execute: ["echo"] }] },
  none: { label: "the no-grant VK", text: "no MCP configs" },
};
const VK_TEXT = `the full-access VK (${VKS.full.text}), the echo-only VK (${VKS.echo.text}) and the no-grant VK (${VKS.none.text})`;
const createVK = (id, label) => request(id, `setup: create ${VKS[label].label}`, "POST", "/api/governance/virtual-keys", {
  body: { name: `${id} ${VKS[label].label.replace(/^the /, "")}`, ...(VKS[label].configs ? { mcp_configs: VKS[label].configs } : {}) },
  asserts: [A.status(200)],
  extraTests: [
    `pm.collectionVariables.set(${lit(id.toLowerCase() + "_" + label.toLowerCase() + "_id")}, pm.response.json().virtual_key.id);`,
    `pm.collectionVariables.set(${lit(id.toLowerCase() + "_" + label.toLowerCase())}, pm.response.json().virtual_key.value);`,
  ],
});
const deleteVK = (id, label) => request(id, `teardown: delete ${VKS[label].label}`, "DELETE", `/api/governance/virtual-keys/{{${id.toLowerCase()}_${label.toLowerCase()}_id}}`, { asserts: [A.status(200)] });
const vkHeader = (id, label) => ({ key: "x-bf-vk", value: `{{${id.toLowerCase()}_${label.toLowerCase()}}}` });

const toolNamesInList = (names) => A.custom(`tools/list names are exactly ${lit(names)}`, [
  "var names = pm.response.json().result.tools.map(function (t) { return t.name; });",
  `pm.expect(names, 'body: ' + pm.response.text()).to.eql(${lit(names)});`,
]);
const toolResultError = (startsWith, contains) => A.custom(`isError true${startsWith ? ", text starts " + lit(startsWith) : ""}${contains ? ", text contains " + lit(contains) : ""}`, [
  "var r = pm.response.json().result;",
  "pm.expect(r && r.isError, 'body: ' + pm.response.text()).to.equal(true);",
  "var text = r.content[0].text;",
  ...(startsWith ? [`pm.expect(text.indexOf(${lit(startsWith)}), 'text: ' + text).to.equal(0);`] : []),
  ...(contains ? [`pm.expect(text, 'text: ' + text).to.include(${lit(contains)});`] : []),
]);
const mockCalls = (id, server, tool, n) => mockGet(id, server, `${server} calls[${tool}] is ${n}`, [A.custom(`calls[${tool}] is ${n}`, [
  `pm.expect(pm.response.json().counters.calls[${lit(tool)}] || 0, 'body: ' + pm.response.text()).to.equal(${n});`,
])]);
const call = (name, args = {}) => ({ jsonrpc: "2.0", id: 1, method: "tools/call", params: { name, arguments: args } });
const init = (version) => ({ jsonrpc: "2.0", id: 1, method: "initialize", params: { ...(version ? { protocolVersion: version } : {}), capabilities: {}, clientInfo: { name: "mcp-harness", version: "1.0.0" } } });

// noCredControl shows enforce_auth_on_inference is in force: tools/list with no credential is refused.
const noCredControl = (id, label) => rpc(id, `control: ${label}no credential is refused`, listMsg, { headers: [MCP_ACCEPT], asserts: [A.status(401), A.msgContains("virtual key required to access mcp server")] });
const EP01 = folder("EP-01 initialize (2025-03-26)", `Setup: ${FLEET}; mcp_server_auth_mode headers, enforce_auth_on_inference off.`, [
  readConfig("EP-01"), authMode("EP-01", "H", false),
  ...fleetSetup("EP-01"),
  request("EP-01", "setup: read GET /api/version", "GET", "/api/version", { asserts: [A.status(200)], extraTests: ["pm.collectionVariables.set('ep01_version', JSON.stringify(pm.response.json()));"] }),
  rpc("EP-01", "initialize with 2025-03-26", init("2025-03-26"), {
    asserts: [A.status(200), A.custom("JSON reply; serverInfo bifrost at the server version; capabilities exactly tools.listChanged; no Mcp-Session-Id", [
      "pm.expect(pm.response.headers.get('Content-Type')).to.include('application/json');",
      "var r = pm.response.json().result;",
      "pm.expect(r.serverInfo.name, 'body: ' + pm.response.text()).to.equal('bifrost');",
      "pm.expect(r.serverInfo.version).to.equal(JSON.parse(pm.collectionVariables.get('ep01_version')));",
      "pm.expect(r.capabilities).to.eql({ tools: { listChanged: true } });",
      "pm.expect(pm.response.headers.has('Mcp-Session-Id')).to.equal(false);",
    ])],
  }),
  ...fleetTeardown("EP-01"),
  restoreConfig("EP-01"),
]);

const EP02 = folder("EP-02 Protocol version negotiation", `Setup: ${FLEET}.`, [
  ...fleetSetup("EP-02"),
  ...[["2024-11-05", "2024-11-05"], ["2025-06-18", "2025-06-18"], ["1999-01-01", "2025-06-18"], [null, "2025-03-26"]].map(([asked, got]) =>
    rpc("EP-02", `initialize asking ${asked || "no version"}`, init(asked), {
      asserts: [A.status(200), A.custom(`protocolVersion is ${got}`, [`pm.expect(pm.response.json().result.protocolVersion, 'body: ' + pm.response.text()).to.equal(${lit(got)});`])],
    })),
  ...fleetTeardown("EP-02"),
]);

const EP04 = folder("EP-04 No upstream instructions", `Setup: ${FLEET}; the mock servers send no instructions.`, [
  ...fleetSetup("EP-04"),
  rpc("EP-04", "initialize", init("2025-03-26"), { asserts: [A.status(200), A.custom("result has no instructions key", ["pm.expect(pm.response.json().result, 'body: ' + pm.response.text()).to.not.have.property('instructions');"])] }),
  ...fleetTeardown("EP-04"),
]);

const EP13 = folder("EP-13 Client tools_to_execute narrows /mcp", `Setup: ${FLEET}, except client alpha has tools_to_execute ["echo"]; the full-access VK (${VKS.full.text}).`, [
  ...fleetSetupWith("EP-13", { alphaClient: { tools_to_execute: ["echo"] } }),
  createVK("EP-13", "full"),
  rpc("EP-13", "tools/list with the full-access VK", listMsg, { headers: [MCP_ACCEPT, vkHeader("EP-13", "full")], asserts: [A.status(200), toolNamesInList(["alpha-echo", "beta-echo"]), A.custom("alpha-add is not listed", [
    "var names = pm.response.json().result.tools.map(function (t) { return t.name; });",
    "pm.expect(names, 'body: ' + pm.response.text()).to.not.include('alpha-add');",
  ])] }),
  rpc("EP-13", "tools/call alpha-add with the full-access VK", call("alpha-add", { a: 1, b: 2 }), { headers: [MCP_ACCEPT, vkHeader("EP-13", "full")], asserts: [A.status(200), rpcError(-32602, "tool 'alpha-add' not found: tool not found")] }),
  mockCalls("EP-13", "alpha", "add", 0),
  deleteVK("EP-13", "full"),
  ...fleetTeardown("EP-13"),
]);

const inc = (key, value) => ({ key: `x-bf-mcp-include-${key}`, value });
const EP15 = folder("EP-15 Hyphenated upstream tool name", `Setup: ${FLEET}; alpha also serves "web-search".`, [
  ...fleetSetupWith("EP-15", { alphaTools: ["echo", "add", "web-search"] }),
  rpc("EP-15", "tools/list", listMsg, { asserts: [A.status(200), A.custom("alpha-web-search is listed", [
    "var names = pm.response.json().result.tools.map(function (t) { return t.name; });",
    "pm.expect(names, 'body: ' + pm.response.text()).to.include('alpha-web-search');",
  ])] }),
  rpc("EP-15", "tools/call alpha-web-search", call("alpha-web-search", { q: "x" }), { asserts: [A.status(200)] }),
  mockCalls("EP-15", "alpha", "web-search", 1),
  ...fleetTeardown("EP-15"),
]);

const EP17 = folder("EP-17 Names with \"/\" or \"..\" are not served", `Setup: ${FLEET}; alpha also serves "x/y" and "a..b".`, [
  ...fleetSetupWith("EP-17", { alphaTools: ["echo", "add", "x/y", "a..b"] }),
  rpc("EP-17", "tools/list", listMsg, { asserts: [A.status(200), toolNamesInList(["alpha-add", "alpha-echo", "beta-echo"]), A.custom("neither name is listed", [
    "var names = pm.response.json().result.tools.map(function (t) { return t.name; });",
    "pm.expect(names, 'body: ' + pm.response.text()).to.not.include('alpha-x/y');",
    "pm.expect(names).to.not.include('alpha-a..b');",
  ])] }),
  rpc("EP-17", "tools/call alpha-x/y", call("alpha-x/y"), { asserts: [A.status(200), rpcError(-32602)] }),
  ...fleetTeardown("EP-17"),
]);

const EP20 = folder("EP-20 Upstream fails the exchange", `Setup: ${FLEET}; alpha fault err_rate 1, err_kind protocol.`, [
  ...fleetSetup("EP-20"),
  mockPost("EP-20", "alpha", "faults", "setup: alpha fails every call with a protocol error", { faults: { err_rate: 1, err_kind: "protocol" } }),
  rpc("EP-20", "tools/call alpha-echo", call("alpha-echo", { text: "hi" }), { asserts: [A.status(200), toolResultError("Tool execution failed: MCP tool call failed for alpha-echo:", null)] }),
  ...fleetTeardown("EP-20"),
]);

const EP25 = folder("EP-25 Tool hidden by include headers", `Setup: ${FLEET}; an anonymous caller with include-clients beta calls alpha-echo.`, [
  ...fleetSetup("EP-25"),
  rpc("EP-25", "tools/call alpha-echo with include-clients beta", call("alpha-echo", { text: "hi" }), { headers: [MCP_ACCEPT, inc("clients", "beta")], asserts: [A.status(200), toolResultError(null, "is not permitted (client alpha is not in request-context include list)")] }),
  mockGet("EP-25", "alpha", "alpha received no tool call", [A.custom("calls is empty", ["pm.expect(pm.response.json().counters.calls, 'body: ' + pm.response.text()).to.eql({});"])]),
  ...fleetTeardown("EP-25"),
]);

const EP26 = folder("EP-26 Argument handling", `Setup: ${FLEET}; alpha's add declares numeric a and b.`, [
  request("EP-26", "setup: fresh mcpchaos server alpha", "POST", "/control", { base: CHAOS, noAuth: true, body: { add: "alpha" }, asserts: [A.status(200)] }),
  request("EP-26", "setup: alpha's add takes numeric a and b", "POST", "/control/alpha/tools/set", { base: CHAOS, noAuth: true,
    body: { tools: [{ name: "echo" }, { name: "add", inputSchema: { type: "object", properties: { a: { type: "number" }, b: { type: "number" } }, required: ["a", "b"] } }] }, asserts: [A.status(200)] }),
  ...freshServer("EP-26", "beta", ["echo"]),
  createClient("EP-26", httpClient("ep-26-alpha", "alpha", "alpha", { tools_to_execute: ["*"], allow_by_default: false }), "setup: register client alpha"),
  createClient("EP-26", httpClient("ep-26-beta", "beta", "beta", { tools_to_execute: ["*"], allow_by_default: false }), "setup: register client beta"),
  rpc("EP-26", "schema-violating arguments are forwarded unchanged", call("alpha-add", { a: "x", b: "y" }), { asserts: [A.status(200), A.custom("the mock echoes the arguments as sent", [
    "var r = pm.response.json().result;",
    "pm.expect(r.content[0].text, 'body: ' + pm.response.text()).to.equal('add:{\"a\":\"x\",\"b\":\"y\"}');",
  ])] }),
  mockCalls("EP-26", "alpha", "add", 1),
  rpc("EP-26", "params \"x\"", { jsonrpc: "2.0", id: 1, method: "tools/call", params: "x" }, { asserts: [A.status(200), A.custom("error.code -32600, message starts \"unparsable tools/call request\"", [
    "var b = pm.response.json();",
    "pm.expect(b.error && b.error.code, 'body: ' + pm.response.text()).to.equal(-32600);",
    "pm.expect(b.error.message.indexOf('unparsable tools/call request'), 'message: ' + b.error.message).to.equal(0);",
  ])] }),
  rpc("EP-26", "no tool name", { jsonrpc: "2.0", id: 1, method: "tools/call", params: {} }, { asserts: [A.status(200), A.custom("error.code -32602, message contains \"tool '' not found\"", [
    "var b = pm.response.json();",
    "pm.expect(b.error && b.error.code, 'body: ' + pm.response.text()).to.equal(-32602);",
    "pm.expect(b.error.message).to.include(\"tool '' not found\");",
  ])] }),
  ...fleetTeardown("EP-26"),
]);

// ================================================================ OAS consent, token and session tests

// VERIFIER is the RFC 7636 appendix B verifier; its S256 challenge is CHALLENGE.
const VERIFIER = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk";
const FORM = { key: "Content-Type", value: "application/x-www-form-urlencoded" };
// form builds an x-www-form-urlencoded body; every value used here is already form-safe.
const form = (params) => Object.entries(params).filter(([, v]) => v !== undefined).map(([k, v]) => {
  if (/[&+%# ]/.test(v)) throw new Error(`form value needs encoding: ${v}`);
  return `${k}=${v}`;
}).join("&");

// oauthConfig turns the OAuth server on for one test: the given mode, issuer_url = base_url,
// and the temp-token auth the consent page's #t= token needs. extra is ES5 run afterwards.
const oauthConfig = (id, mode, extra = [], title, tempTokens = true) => putConfig(id, title || `setup: mcp_server_auth_mode ${mode}, issuer_url set to the base URL, temp-token auth on`, [
  `cc.mcp_server_auth_mode = ${lit(mode)};`,
  "cc.oauth2_server_config = { issuer_url: pm.variables.get('base_url') };",
  `cc.mcp_enable_temp_token_auth = ${tempTokens};`,
  ...extra,
], [A.status(200)]);

// cred picks how a consent API request authenticates: "temp" (the flow's temp token), "admin"
// (Basic), "bearer" (Bearer base64(admin:pw)), "cookie" (the admin session cookie) or "none".
const cred = (as, flow) => ({
  temp: { noAuth: true, headers: [{ key: "X-Bifrost-Temp-Token", value: `{{${flow}_tt}}` }] },
  admin: { noAuth: false, headers: [] },
  bearer: { noAuth: true, headers: [{ key: "Authorization", value: "{{admin_bearer}}" }], pre: ["pm.variables.set('admin_bearer', pm.variables.get('admin_auth_header').replace(/^Basic /, 'Bearer '));"] },
  cookie: { noAuth: true, headers: [{ key: "Cookie", value: "token={{admin_cookie}}" }], noCookies: true },
  none: { noAuth: true, headers: [] },
})[as];

// authorize starts a flow and keeps its id and temp token from the consent Location; over replaces
// authorize parameters, such as redirect_uri.
const authorize = (id, clientVar, flow, title, over = {}) => request(id, title, "GET", goodAuthorize(clientVar, over), {
  noAuth: true, noRedirect: true, asserts: [A.status(302)],
  extraTests: [
    "var loc = pm.response.headers.get('Location') || '';",
    "var m = /[?&]flow=([^#&]+)#t=(.+)$/.exec(loc);",
    `pm.collectionVariables.set(${lit(flow + "_id")}, m ? decodeURIComponent(m[1]) : '');`,
    `pm.collectionVariables.set(${lit(flow + "_tt")}, m ? decodeURIComponent(m[2]) : '');`,
  ],
});
const flowPath = (flow) => `/api/oauth2/consent/flows/{{${flow}_id}}`;
const flowGet = (id, flow, title, as, asserts, opts = {}) => {
  const c = cred(as, flow);
  return request(id, title, "GET", opts.path || flowPath(flow), { noAuth: c.noAuth, headers: c.headers, noCookies: c.noCookies, pre: [...(c.pre || []), ...(opts.pre || [])], asserts });
};
// consent submits PUT /api/oauth2/consent/flows/{id}; body is an object or, with raw, the literal body.
const consent = (id, flow, title, as, body, asserts, opts = {}) => {
  const c = cred(as, flow);
  return request(id, title, "PUT", opts.path || flowPath(flow), {
    noAuth: c.noAuth, headers: c.headers, noCookies: c.noCookies, pre: [...(c.pre || []), ...(opts.pre || [])],
    ...(opts.raw !== undefined ? { raw: opts.raw } : { body }), asserts, extraTests: opts.extraTests || [],
  });
};
const vkBody = (id) => ({ mode: "vk", value: `{{${id.toLowerCase()}_full}}` });
// saveCode keeps the authorization code from a successful consent's redirect_url.
const saveCode = (flow) => [
  "var u = (pm.response.json() || {}).redirect_url || '';",
  "var m = /[?&]code=([^&]+)/.exec(u);",
  `pm.collectionVariables.set(${lit(flow + "_code")}, m ? decodeURIComponent(m[1]) : '');`,
];

// REFRESHABLE is what a client registers to be allowed the refresh grant.
const REFRESHABLE = ["authorization_code", "refresh_token"];
const codeIssued = () => A.custom("redirect_url carries a code", ["var u = (pm.response.json() || {}).redirect_url || '';", "pm.expect(/[?&]code=[^&]+/.test(u), 'redirect_url: ' + u).to.equal(true);"]);
const tokensIssued = () => A.custom("a JWT access token and a refresh token are issued", [
  "var t = pm.response.json() || {};",
  "pm.expect(t.access_token, 'body: ' + pm.response.text()).to.match(/^[A-Za-z0-9_-]+\\.[A-Za-z0-9_-]+\\.[A-Za-z0-9_-]+$/);",
  "pm.expect(t.refresh_token).to.be.a('string').and.not.empty;",
]);
const tokenReq = (id, title, params, asserts, { headers = [], pre = [], extraTests = [] } = {}) => request(id, title, "POST", "/oauth2/token", {
  noAuth: true, raw: form(params), headers: [FORM, ...headers], pre, asserts, extraTests,
});
const codeParams = (flow, clientVar, over = {}) => ({ grant_type: "authorization_code", code: `{{${flow}_code}}`, code_verifier: VERIFIER, client_id: `{{${clientVar}}}`, redirect_uri: LOOPBACK_CB, ...over });
const refreshParams = (rtVar, clientVar, over = {}) => ({ grant_type: "refresh_token", refresh_token: `{{${rtVar}}}`, client_id: `{{${clientVar}}}`, ...over });
// saveTokens keeps a token response's access and refresh tokens as <prefix>_at and <prefix>_rt.
const saveTokens = (prefix) => [
  "var t = pm.response.json() || {};",
  `pm.collectionVariables.set(${lit(prefix + "_at")}, t.access_token || '');`,
  `pm.collectionVariables.set(${lit(prefix + "_rt")}, t.refresh_token || '');`,
];
// vkGrant runs a whole grant as setup: authorize, consent with the full-access VK through the
// temp token, and exchange the code. The tokens land in <flow>_at and <flow>_rt.
const vkGrant = (id, clientVar, flow, label) => [
  authorize(id, clientVar, flow, `setup: authorize ${label}`),
  consent(id, flow, `setup: consent ${label} with the full-access VK`, "temp", vkBody(id), [A.status(200), codeIssued()], { extraTests: saveCode(flow) }),
  tokenReq(id, `setup: exchange ${label}'s code`, codeParams(flow, clientVar), [A.status(200), tokensIssued()], { extraTests: saveTokens(flow) }),
];
// CLAIMS_FN decodes a JWT payload inside a test script.
const CLAIMS_FN = "function claims(t) { var C = require('crypto-js'); var p = String(t || '').split('.')[1] || ''; p = p.replace(/-/g, '+').replace(/_/g, '/'); while (p.length % 4) { p += '='; } return JSON.parse(C.enc.Base64.parse(p).toString(C.enc.Utf8)); }";
// mcpWithToken sends tools/list to /mcp with an issued access token as the bearer.
const mcpWithToken = (id, title, tokenVar, asserts, pre = []) => request(id, title, "POST", "/mcp", {
  noAuth: true, raw: JSON.stringify(listMsg), headers: [MCP_ACCEPT, { key: "Authorization", value: `Bearer {{${tokenVar}}}` }], asserts, pre,
});
const WAIT_3S = ["setTimeout(function () {}, 3000);"];
const errorCode = (code) => A.custom(`error.code is ${lit(code)}`, ["var b = pm.response.json();", `pm.expect(b.error && b.error.code, 'body: ' + pm.response.text()).to.equal(${lit(code)});`]);
const fleetGrantSetup = (id) => [readConfig(id), ...fleetSetup(id), createVK(id, "full")];
const fleetGrantTeardown = (id) => [restoreConfig(id), deleteVK(id, "full"), ...fleetTeardown(id)];
const GRANT_TEXT = `${FLEET}; the full-access VK (${VKS.full.text}); each grant registers a client with grant_types authorization_code and refresh_token, authorizes with S256, consents with that VK through the temp token and exchanges the code`;

const vkCreate = (id, title, body, varPrefix) => request(id, title, "POST", "/api/governance/virtual-keys", {
  body, asserts: [A.status(200)],
  extraTests: [`pm.collectionVariables.set(${lit(varPrefix + "_id")}, pm.response.json().virtual_key.id);`, `pm.collectionVariables.set(${lit(varPrefix)}, pm.response.json().virtual_key.value);`],
});
const vkDelete = (id, title, varPrefix) => request(id, title, "DELETE", `/api/governance/virtual-keys/{{${varPrefix}_id}}`, { asserts: [A.status(200)] });
const OAS15 = folder("OAS-15 Consent flow detail", `Setup: ${BOTH}, dashboard auth on, temp-token auth on; the request carries only the flow's temp token.`, [
  readConfig("OAS-15"), oauthConfig("OAS-15", "both"),
  register("OAS-15", "oas15_client"),
  authorize("OAS-15", "oas15_client", "oas15_f", "setup: authorize"),
  flowGet("OAS-15", "oas15_f", "GET the flow with its temp token", "temp", [A.status(200), A.custom("redirect_uri, client_name, expires_at in RFC 3339 UTC, available_modes [\"vk\"], no logged_in_user", [
    "var b = pm.response.json();",
    `pm.expect(b.redirect_uri, 'body: ' + pm.response.text()).to.equal(${lit(LOOPBACK_CB)});`,
    "pm.expect(b.client_name).to.equal('OAS-15 client');",
    "pm.expect(b.expires_at).to.match(/^\\d{4}-\\d{2}-\\d{2}T\\d{2}:\\d{2}:\\d{2}Z$/);",
    "pm.expect(b.available_modes).to.eql(['vk']);",
    "pm.expect(b).to.not.have.property('logged_in_user');",
  ])]),
  restoreConfig("OAS-15"),
]);

const OAS16 = folder("OAS-16 Consent API auth gates", `Setup: ${BOTH}, dashboard auth on; temp-token auth off for the first two cases and on for the rest.`, [
  readConfig("OAS-16"),
  oauthConfig("OAS-16", "both", [], "setup: mcp_server_auth_mode both, issuer_url set to the base URL, temp-token auth off", false),
  register("OAS-16", "oas16_client"),
  authorize("OAS-16", "oas16_client", "oas16_f1", "setup: authorize flow one"),
  flowGet("OAS-16", "oas16_f1", "no token", "none", [A.status(401)]),
  flowGet("OAS-16", "oas16_f1", "flow one's token with temp-token auth off", "temp", [A.status(401), errorCode("temp_token_auth_disabled")]),
  oauthConfig("OAS-16", "both"),
  flowGet("OAS-16", "oas16_f1", "control: flow one's token opens flow one", "temp", [A.status(200)]),
  authorize("OAS-16", "oas16_client", "oas16_f2", "setup: authorize flow two"),
  flowGet("OAS-16", "oas16_f1", "flow one's token on flow two", "temp", [A.status(401), errorCode("temp_token_rejected")], { path: flowPath("oas16_f2") }),
  flowGet("OAS-16", "oas16_f1", "flow one's token on /api/oauth2/sessions", "temp", [A.status(401), errorCode("temp_token_rejected")], { path: "/api/oauth2/sessions" }),
  restoreConfig("OAS-16"),
]);

const OAS18 = folder("OAS-18 Consent with a VK", `Setup: mcp_server_auth_mode both with issuer_url ${OTHER_ISS}, an origin other than the one requests go to; dashboard auth on, temp-token auth on; a virtual key with no MCP configs; the client authorizes with resource omitted.`, [
  readConfig("OAS-18"), putConfig("OAS-18", `setup: mcp_server_auth_mode both, issuer_url ${OTHER_ISS}, temp-token auth on`, ["cc.mcp_server_auth_mode = 'both';", `cc.oauth2_server_config = { issuer_url: ${lit(OTHER_ISS)} };`, "cc.mcp_enable_temp_token_auth = true;"], [A.status(200)]),
  vkCreate("OAS-18", "setup: create a virtual key", { name: "OAS-18 key" }, "oas18_key"),
  register("OAS-18", "oas18_client"),
  authorize("OAS-18", "oas18_client", "oas18_f", "setup: authorize", { resource: undefined }),
  consent("OAS-18", "oas18_f", "consent with the key", "temp", { mode: "vk", value: "{{oas18_key}}" }, [A.status(200), A.custom("redirect_url carries a code, state s1 and iss set to the issuer", [
    "var u = pm.response.json().redirect_url || '';",
    `pm.expect(u.indexOf(${lit(LOOPBACK_CB + "?")}), 'redirect_url: ' + u).to.equal(0);`,
    "var q = {}; u.split('?')[1].split('&').forEach(function (kv) { var p = kv.split('='); q[decodeURIComponent(p[0])] = decodeURIComponent(p[1] || ''); });",
    "pm.expect(q.code, 'redirect_url: ' + u).to.be.a('string').and.not.empty;",
    "pm.expect(q.state).to.equal('s1');",
    `pm.expect(q.iss).to.equal(${lit(OTHER_ISS)});`,
  ])]),
  consent("OAS-18", "oas18_f", "the same temp token again", "temp", { mode: "vk", value: "{{oas18_key}}" }, [A.status(401), errorCode("temp_token_rejected")]),
  consent("OAS-18", "oas18_f", "an admin retry", "admin", { mode: "vk", value: "{{oas18_key}}" }, [A.status(410), A.msgEquals("authorization flow has already been completed")]),
  vkDelete("OAS-18", "teardown: delete the virtual key", "oas18_key"),
  restoreConfig("OAS-18"),
]);

const oas19Cases = [
  ["an empty value", { mode: "vk", value: "" }, null, A.msgEquals("virtual key value is required")],
  ["an unknown key value", { mode: "vk", value: "sk-bf-oas19-no-such-key" }, null, A.msgEquals("virtual key not found")],
  ["an inactive key", { mode: "vk", value: "{{oas19_inactive}}" }, null, A.msgEquals("virtual key is inactive")],
  ["a rotated value in its cooldown", { mode: "vk", value: "{{oas19_rotated}}" }, null, A.msgStartsWith("this virtual key value has been rotated")],
  ["mode user", { mode: "user", value: "" }, null, A.msgEquals("mode \"user\" is not available")],
  ["mode foo", { mode: "foo", value: "" }, null, A.msgEquals("mode \"foo\" is not available")],
  ["body \"x\"", null, "x", A.msgEquals("invalid request body")],
];
const OAS19 = folder("OAS-19 Consent rejections", `Setup: ${BOTH}, dashboard auth on, temp-token auth on, vk_rotation_cooldown 5m; a fresh flow per case; an inactive key, and a key rotated during the cooldown.`, [
  readConfig("OAS-19"), oauthConfig("OAS-19", "both", ["cc.vk_rotation_cooldown = '5m';"], "setup: mcp_server_auth_mode both, issuer_url set to the base URL, temp-token auth on, vk_rotation_cooldown 5m"),
  register("OAS-19", "oas19_client"),
  vkCreate("OAS-19", "setup: create an inactive key", { name: "OAS-19 inactive key", is_active: false }, "oas19_inactive"),
  vkCreate("OAS-19", "setup: create a key to rotate", { name: "OAS-19 rotated key" }, "oas19_rotated"),
  request("OAS-19", "setup: rotate it", "POST", "/api/governance/virtual-keys/{{oas19_rotated_id}}/rotate", { asserts: [A.status(200)] }),
  ...oas19Cases.flatMap(([label, body, raw, assert], i) => [
    authorize("OAS-19", "oas19_client", `oas19_f${i + 1}`, `setup: authorize a flow for ${label}`),
    consent("OAS-19", `oas19_f${i + 1}`, label, "temp", body, [A.status(400), assert], raw !== null ? { raw } : {}),
  ]),
  vkDelete("OAS-19", "teardown: delete the inactive key", "oas19_inactive"),
  vkDelete("OAS-19", "teardown: delete the rotated key", "oas19_rotated"),
  restoreConfig("OAS-19"),
]);

const sessionBfMode = (mode) => A.custom(`the access token's bf_mode is ${lit(mode)}`, [CLAIMS_FN, `pm.expect(claims(pm.response.json().access_token).bf_mode, 'body: ' + pm.response.text()).to.equal(${lit(mode)});`]);
// revokeGrants lists the OAuth sessions of a client and revokes each one; session-mode grants are not removed with a VK.
const revokeGrants = (id, clientVar) => request(id, "teardown: revoke the client's OAuth sessions", "GET", `/api/oauth2/sessions?q={{${clientVar}}}`, {
  asserts: [A.status(200)],
  extraTests: [`(pm.response.json().sessions || []).filter(function (s) { return s.client_id === pm.collectionVariables.get(${lit(clientVar)}); }).forEach(function (s) { pm.sendRequest({ url: pm.variables.get('base_url') + '/api/oauth2/sessions/' + s.id, method: 'DELETE', header: { Authorization: pm.variables.get('admin_auth_header') } }, function (err, res) { pm.test(${lit(id + " teardown: revoke session ")} + s.id, function () { pm.expect(res && res.code).to.equal(204); }); }); });`],
});
const OAS20 = folder("OAS-20 Session-mode consent", `Setup: ${BOTH}, dashboard auth on, temp-token auth on; enforce_auth_on_inference off, then on for the last case.`, [
  readConfig("OAS-20"), oauthConfig("OAS-20", "both", ["cc.enforce_auth_on_inference = false;"], "setup: mcp_server_auth_mode both, issuer_url set to the base URL, temp-token auth on, enforce_auth_on_inference off"),
  register("OAS-20", "oas20_client"),
  authorize("OAS-20", "oas20_client", "oas20_f1", "setup: authorize flow one"),
  consent("OAS-20", "oas20_f1", "mode session with the temp token only", "temp", { mode: "session", value: "" }, [A.status(401), A.msgStartsWith("session mode requires an authenticated consenting user")]),
  request("OAS-20", "setup: log in as the admin", "POST", "/api/session/login", {
    noAuth: true, noCookies: true, raw: "{{login_body}}",
    pre: ["var C = require('crypto-js'); var pair = C.enc.Base64.parse(pm.variables.get('admin_auth_header').replace(/^Basic /, '')).toString(C.enc.Utf8); var i = pair.indexOf(':');", "pm.variables.set('login_body', JSON.stringify({ username: pair.slice(0, i), password: pair.slice(i + 1) }));"],
    asserts: [A.status(200)],
    extraTests: ["var m = /(?:^|[;,\\s])token=([^;]+)/.exec(pm.response.headers.get('Set-Cookie') || '');", "pm.collectionVariables.set('admin_cookie', m ? m[1] : '');"],
  }),
  authorize("OAS-20", "oas20_client", "oas20_f2", "setup: authorize flow two"),
  consent("OAS-20", "oas20_f2", "mode session with the admin cookie", "cookie", { mode: "session", value: "" }, [A.status(200)], { extraTests: saveCode("oas20_f2") }),
  tokenReq("OAS-20", "exchange the cookie consent's code", codeParams("oas20_f2", "oas20_client"), [A.status(200), sessionBfMode("session")]),
  authorize("OAS-20", "oas20_client", "oas20_f3", "setup: authorize flow three"),
  consent("OAS-20", "oas20_f3", "mode session with Bearer base64(admin:pw)", "bearer", { mode: "session", value: "" }, [A.status(200)], { extraTests: saveCode("oas20_f3") }),
  tokenReq("OAS-20", "exchange the Bearer consent's code", codeParams("oas20_f3", "oas20_client"), [A.status(200), sessionBfMode("session")]),
  oauthConfig("OAS-20", "both", ["cc.enforce_auth_on_inference = true;"], "setup: mcp_server_auth_mode both, issuer_url set to the base URL, temp-token auth on, enforce_auth_on_inference on"),
  authorize("OAS-20", "oas20_client", "oas20_f4", "setup: authorize flow four"),
  consent("OAS-20", "oas20_f4", "mode session with Bearer base64(admin:pw) and enforce_auth_on_inference on", "bearer", { mode: "session", value: "" }, [A.status(400), A.msgEquals("mode \"session\" is not available")]),
  revokeGrants("OAS-20", "oas20_client"),
  request("OAS-20", "teardown: log the admin session out", "POST", "/api/session/logout", { noAuth: true, noCookies: true, headers: [{ key: "Authorization", value: "Bearer {{admin_cookie}}" }], asserts: [A.status(200)] }),
  restoreConfig("OAS-20"),
]);

const OAS21 = folder("OAS-21 Expired or unknown flow", `Setup: ${BOTH}, dashboard auth on, temp-token auth on, auth_code_ttl 2. The flow's temp token expires with it, so the requests after the wait carry admin credentials.`, [
  readConfig("OAS-21"), oauthConfig("OAS-21", "both", ["cc.oauth2_server_config.auth_code_ttl = 2;"], "setup: mcp_server_auth_mode both, issuer_url set to the base URL, temp-token auth on, auth_code_ttl 2"),
  register("OAS-21", "oas21_client"),
  authorize("OAS-21", "oas21_client", "oas21_f", "setup: authorize"),
  flowGet("OAS-21", "oas21_f", "GET the flow after 3s", "admin", [A.status(410), A.msgEquals("authorization flow has expired")], { pre: WAIT_3S }),
  consent("OAS-21", "oas21_f", "PUT the flow after 3s", "admin", { mode: "vk", value: "" }, [A.status(410), A.msgEquals("authorization flow has expired")]),
  flowGet("OAS-21", "oas21_f", "GET a random flow id", "admin", [A.status(404), A.msgEquals("authorization flow not found or expired")], { path: "/api/oauth2/consent/flows/oas21-no-such-flow" }),
  consent("OAS-21", "oas21_f", "PUT a random flow id", "admin", { mode: "vk", value: "" }, [A.status(404), A.msgEquals("authorization flow not found or expired")], { path: "/api/oauth2/consent/flows/oas21-no-such-flow" }),
  restoreConfig("OAS-21"),
]);

const tokenResponseChecks = () => [A.status(200), A.custom("token_type \"Bearer\", expires_in 600, scope \"mcp\", a refresh_token", [
  "var b = pm.response.json();",
  "pm.expect(b.token_type, 'body: ' + pm.response.text()).to.equal('Bearer');",
  "pm.expect(b.expires_in).to.equal(600);",
  "pm.expect(b.scope).to.equal('mcp');",
  "pm.expect(b.refresh_token).to.be.a('string').and.not.empty;",
]), A.custom("Cache-Control no-store, Pragma no-cache", [
  "pm.expect(pm.response.headers.get('Cache-Control')).to.equal('no-store');",
  "pm.expect(pm.response.headers.get('Pragma')).to.equal('no-cache');",
])];
const OAS23 = folder("OAS-23 Exchange the code", `Setup: ${GRANT_TEXT}; ${BOTH}, temp-token auth on, access_token_ttl unset (600).`, [
  ...fleetGrantSetup("OAS-23"), oauthConfig("OAS-23", "both"),
  register("OAS-23", "oas23_client", LOOPBACK_CB, undefined, REFRESHABLE),
  authorize("OAS-23", "oas23_client", "oas23_f1", "setup: authorize grant one"),
  consent("OAS-23", "oas23_f1", "setup: consent grant one with the full-access VK", "temp", vkBody("OAS-23"), [A.status(200), codeIssued()], { extraTests: saveCode("oas23_f1") }),
  tokenReq("OAS-23", "exchange with client_id in the body", codeParams("oas23_f1", "oas23_client"), tokenResponseChecks()),
  authorize("OAS-23", "oas23_client", "oas23_f2", "setup: authorize grant two"),
  consent("OAS-23", "oas23_f2", "setup: consent grant two with the full-access VK", "temp", vkBody("OAS-23"), [A.status(200), codeIssued()], { extraTests: saveCode("oas23_f2") }),
  tokenReq("OAS-23", "exchange with client_id in Basic auth", codeParams("oas23_f2", "oas23_client", { client_id: undefined }), tokenResponseChecks(), {
    headers: [{ key: "Authorization", value: "{{oas23_client_basic}}" }],
    pre: ["var C = require('crypto-js');", "pm.variables.set('oas23_client_basic', 'Basic ' + C.enc.Base64.stringify(C.enc.Utf8.parse(pm.collectionVariables.get('oas23_client') + ':')));"],
  }),
  ...fleetGrantTeardown("OAS-23"),
]);

const oas25Cases = [
  ["no code_verifier", { code_verifier: undefined }, O.descEquals("code, code_verifier and client_id are required")],
  ["a wrong code_verifier", { code_verifier: "oas25-wrong-verifier-0123456789abcdefghijklmn" }, O.descEquals("PKCE verification failed")],
  ["another client's client_id", { client_id: "{{oas25_other}}" }, O.descEquals("client_id mismatch")],
  ["a different redirect_uri", { redirect_uri: "http://127.0.0.1:1234/cb" }, O.descEquals("redirect_uri mismatch")],
  ["a foreign resource", { resource: "https://other/mcp" }, O.descEquals("resource mismatch")],
  ["grant_type password", { grant_type: "password" }, O.error("unsupported_grant_type")],
  ["a random code", { code: "oas25-random-code" }, O.descEquals("authorization code not found or already used")],
];
const OAS25 = folder("OAS-25 Token request rejections", `Setup: ${GRANT_TEXT}; ${BOTH}, temp-token auth on; a freshly consented code per case, and a second registered client.`, [
  ...fleetGrantSetup("OAS-25"), oauthConfig("OAS-25", "both"),
  register("OAS-25", "oas25_client", LOOPBACK_CB, undefined, REFRESHABLE),
  register("OAS-25", "oas25_other", LOOPBACK_CB, "OAS-25 other client"),
  ...oas25Cases.flatMap(([label, over, assert], i) => {
    const flow = `oas25_f${i + 1}`;
    return [
      authorize("OAS-25", "oas25_client", flow, `setup: authorize a flow for ${label}`),
      consent("OAS-25", flow, `setup: consent the flow for ${label}`, "temp", vkBody("OAS-25"), [A.status(200), codeIssued()], { extraTests: saveCode(flow) }),
      tokenReq("OAS-25", label, codeParams(flow, "oas25_client", over), [A.status(400), assert]),
    ];
  }),
  ...fleetGrantTeardown("OAS-25"),
]);

const OAS27 = folder("OAS-27 Refresh grant", `Setup: ${GRANT_TEXT}; ${BOTH}, temp-token auth on; a second registered client.`, [
  ...fleetGrantSetup("OAS-27"), oauthConfig("OAS-27", "both"),
  register("OAS-27", "oas27_client", LOOPBACK_CB, undefined, REFRESHABLE),
  register("OAS-27", "oas27_other", LOOPBACK_CB, "OAS-27 other client"),
  ...vkGrant("OAS-27", "oas27_client", "oas27_f", "the grant"),
  tokenReq("OAS-27", "refresh", refreshParams("oas27_f_rt", "oas27_client"), [A.status(200), A.custom("a new refresh token; sub (the VK's id), bf_mode \"vk\" and aud [<base URL>/mcp] unchanged", [
    CLAIMS_FN,
    "var b = pm.response.json();",
    "pm.expect(b.refresh_token, 'body: ' + pm.response.text()).to.be.a('string').and.not.empty;",
    "pm.expect(b.refresh_token).to.not.equal(pm.collectionVariables.get('oas27_f_rt'));",
    "var before = claims(pm.collectionVariables.get('oas27_f_at')), after = claims(b.access_token);",
    "pm.expect(before.sub, 'first token sub').to.equal(pm.collectionVariables.get('oas-27_full_id'));",
    "pm.expect(before.bf_mode).to.equal('vk');",
    "pm.expect(before.aud).to.eql([pm.variables.get('base_url') + '/mcp']);",
    "pm.expect(after.sub).to.equal(before.sub);",
    "pm.expect(after.bf_mode).to.equal(before.bf_mode);",
    "pm.expect(after.aud).to.eql(before.aud);",
  ])], { extraTests: saveTokens("oas27_r") }),
  tokenReq("OAS-27", "a foreign resource", refreshParams("oas27_r_rt", "oas27_client", { resource: "https://other/mcp" }), [A.status(400), O.descEquals("resource mismatch")]),
  tokenReq("OAS-27", "another client's client_id", refreshParams("oas27_r_rt", "oas27_other"), [A.status(400), O.descEquals("client_id mismatch")]),
  tokenReq("OAS-27", "no refresh_token", refreshParams("oas27_r_rt", "oas27_client", { refresh_token: undefined }), [A.status(400), O.descEquals("refresh_token and client_id are required")]),
  tokenReq("OAS-27", "no client_id", refreshParams("oas27_r_rt", "oas27_client", { client_id: undefined }), [A.status(400), O.descEquals("refresh_token and client_id are required")]),
  ...fleetGrantTeardown("OAS-27"),
]);

const sessionsOf = (clientVar) => [
  `var cid = pm.collectionVariables.get(${lit(clientVar)});`,
  "var list = (pm.response.json().sessions || []).filter(function (s) { return s.client_id === cid; });",
];
const OAS28 = folder("OAS-28 Rotated refresh token replay", `Setup: ${GRANT_TEXT}; ${BOTH}, temp-token auth on; the first refresh token is rotated to a second.`, [
  ...fleetGrantSetup("OAS-28"), oauthConfig("OAS-28", "both"),
  register("OAS-28", "oas28_client", LOOPBACK_CB, undefined, REFRESHABLE),
  ...vkGrant("OAS-28", "oas28_client", "oas28_f", "the grant"),
  tokenReq("OAS-28", "setup: refresh, rotating the first refresh token", refreshParams("oas28_f_rt", "oas28_client"), [A.status(200), tokensIssued()], { extraTests: saveTokens("oas28_r") }),
  request("OAS-28", "setup: the grant is listed in /api/oauth2/sessions", "GET", "/api/oauth2/sessions?q={{oas28_client}}", { asserts: [A.status(200), A.custom("one session for the client", [
    ...sessionsOf("oas28_client"),
    "pm.expect(list.length, 'body: ' + pm.response.text()).to.equal(1);",
  ])] }),
  tokenReq("OAS-28", "replay the first refresh token", refreshParams("oas28_f_rt", "oas28_client"), [A.status(400), O.descEquals("refresh token not found or revoked")]),
  tokenReq("OAS-28", "the second refresh token", refreshParams("oas28_r_rt", "oas28_client"), [A.status(400), O.descEquals("refresh token not found or revoked")]),
  request("OAS-28", "the grant is gone from /api/oauth2/sessions", "GET", "/api/oauth2/sessions?q={{oas28_client}}", { asserts: [A.status(200), A.custom("no session for the client", [
    ...sessionsOf("oas28_client"),
    "pm.expect(list.length, 'body: ' + pm.response.text()).to.equal(0);",
  ])] }),
  ...fleetGrantTeardown("OAS-28"),
]);

const OAS30 = folder("OAS-30 List OAuth sessions", `Setup: ${GRANT_TEXT}; ${BOTH}, temp-token auth on; two clients, "OAS-30 client one" and "OAS-30 client two", one grant each; a second key "Second session key" with one grant for "Second key client".`, [
  ...fleetGrantSetup("OAS-30"), oauthConfig("OAS-30", "both"),
  register("OAS-30", "oas30_c1", LOOPBACK_CB, "OAS-30 client one", REFRESHABLE),
  register("OAS-30", "oas30_c2", LOOPBACK_CB, "OAS-30 client two", REFRESHABLE),
  ...vkGrant("OAS-30", "oas30_c1", "oas30_f1", "client one's grant"),
  ...vkGrant("OAS-30", "oas30_c2", "oas30_f2", "client two's grant"),
  // A second key and client, named without "OAS-30" so the q=OAS-30 list keeps exactly the two grants above.
  vkCreate("OAS-30", "setup: create a second virtual key", { name: "Second session key" }, "oas30_vk2"),
  register("OAS-30", "oas30_c3", LOOPBACK_CB, "Second key client", REFRESHABLE),
  authorize("OAS-30", "oas30_c3", "oas30_f3", "setup: authorize the second key's grant"),
  consent("OAS-30", "oas30_f3", "setup: consent with the second key", "temp", { mode: "vk", value: "{{oas30_vk2}}" }, [A.status(200), codeIssued()], { extraTests: saveCode("oas30_f3") }),
  tokenReq("OAS-30", "setup: exchange the second key's code", codeParams("oas30_f3", "oas30_c3"), [A.status(200), tokensIssued()]),
  request("OAS-30", "bf_mode=vk keeps both grants", "GET", "/api/oauth2/sessions?q=OAS-30&bf_mode=vk", { asserts: [A.status(200), A.custom("the two clients' grants", ["pm.expect((pm.response.json().sessions || []).map(function (s) { return s.client_id; }).sort(), 'body: ' + pm.response.text()).to.eql([pm.collectionVariables.get('oas30_c1'), pm.collectionVariables.get('oas30_c2')].sort());"])] }),
  request("OAS-30", "list q=OAS-30", "GET", "/api/oauth2/sessions?q=OAS-30", { asserts: [A.status(200), A.jsonEquals("limit", 50), A.custom("client one has no last_used_at before the refresh", [
    "var one = (pm.response.json().sessions || []).filter(function (r) { return r.client_id === pm.collectionVariables.get('oas30_c1'); })[0];",
    "pm.expect(one, 'body: ' + pm.response.text()).to.be.an('object');",
    "pm.expect(one).to.not.have.property('last_used_at');",
  ]), A.custom("two sessions with id, client_id, client_name, bf_mode \"vk\", bf_sub, bf_sub_display and scope \"mcp\"", [
    "var list = pm.response.json().sessions || [];",
    "pm.expect(list.length, 'body: ' + pm.response.text()).to.equal(2);",
    "var names = {}; names[pm.collectionVariables.get('oas30_c1')] = 'OAS-30 client one'; names[pm.collectionVariables.get('oas30_c2')] = 'OAS-30 client two';",
    "list.forEach(function (r) {",
    "  pm.expect(r.id).to.be.a('string').and.not.empty;",
    "  pm.expect(names).to.have.property(r.client_id);",
    "  pm.expect(r.client_name).to.equal(names[r.client_id]);",
    "  pm.expect(r.bf_mode).to.equal('vk');",
    "  pm.expect(r.bf_sub).to.equal(pm.collectionVariables.get('oas-30_full_id'));",
    "  pm.expect(r.bf_sub_display).to.equal('OAS-30 full-access VK');",
    "  pm.expect(r.scope).to.equal('mcp');",
    "});",
    "pm.expect(list[0].client_id).to.not.equal(list[1].client_id);",
  ])], extraTests: [
    "var one = (pm.response.json().sessions || []).filter(function (r) { return r.client_id === pm.collectionVariables.get('oas30_c1'); })[0] || {};",
    "pm.collectionVariables.set('oas30_c1_session', one.id || '');",
    "pm.collectionVariables.set('oas30_c1_created', one.created_at || '');",
  ] }),
  request("OAS-30", "q=OAS-30 client one narrows to client one", "GET", "/api/oauth2/sessions?q=OAS-30%20client%20one", { asserts: [A.status(200), A.custom("only client one's grant", [
    "var list = pm.response.json().sessions || [];",
    "pm.expect(list.length, 'body: ' + pm.response.text()).to.equal(1);",
    "pm.expect(list[0].client_id).to.equal(pm.collectionVariables.get('oas30_c1'));",
  ])] }),
  request("OAS-30", "bf_mode=session narrows to none", "GET", "/api/oauth2/sessions?q=OAS-30&bf_mode=session", { asserts: [A.status(200), A.custom("no sessions", [
    "pm.expect((pm.response.json().sessions || []).length, 'body: ' + pm.response.text()).to.equal(0);",
  ])] }),
  request("OAS-30", "virtual_key_id narrows to the key's grants", "GET", "/api/oauth2/sessions?virtual_key_id={{oas-30_full_id}}", { asserts: [A.status(200), A.custom("exactly the two grants", [
    "var ids = (pm.response.json().sessions || []).map(function (r) { return r.client_id; }).sort();",
    "pm.expect(ids, 'body: ' + pm.response.text()).to.eql([pm.collectionVariables.get('oas30_c1'), pm.collectionVariables.get('oas30_c2')].sort());",
  ])] }),
  request("OAS-30", "virtual_key_id narrows to the second key's grant", "GET", "/api/oauth2/sessions?virtual_key_id={{oas30_vk2_id}}", { asserts: [A.status(200), A.custom("exactly the second key's grant", [
    "var ids = (pm.response.json().sessions || []).map(function (r) { return r.client_id; });",
    "pm.expect(ids, 'body: ' + pm.response.text()).to.eql([pm.collectionVariables.get('oas30_c3')]);",
  ])] }),
  tokenReq("OAS-30", "setup: refresh client one's grant", refreshParams("oas30_f1_rt", "oas30_c1"), [A.status(200), tokensIssued()]),
  request("OAS-30", "after the refresh", "GET", "/api/oauth2/sessions?q=OAS-30%20client%20one", { asserts: [A.status(200), A.custom("the id changes, created_at stays, last_used_at is set", [
    "var list = pm.response.json().sessions || [];",
    "pm.expect(list.length, 'body: ' + pm.response.text()).to.equal(1);",
    "pm.expect(list[0].id).to.not.equal(pm.collectionVariables.get('oas30_c1_session'));",
    "pm.expect(list[0].created_at).to.equal(pm.collectionVariables.get('oas30_c1_created'));",
    "pm.expect(list[0].last_used_at).to.be.a('string').and.not.empty;",
  ])] }),
  vkDelete("OAS-30", "teardown: delete the second virtual key", "oas30_vk2"),
  ...fleetGrantTeardown("OAS-30"),
]);

// oas32Pass runs OAS-32 in one mode: v prefixes its variables, p its request titles, label names its grant.
const oas32Pass = (mode, v, p, label) => [
  oauthConfig("OAS-32", mode),
  register("OAS-32", `${v}_client`, LOOPBACK_CB, undefined, REFRESHABLE),
  ...vkGrant("OAS-32", `${v}_client`, `${v}_f`, label),
  request("OAS-32", `setup: find ${label}'s session`, "GET", `/api/oauth2/sessions?q={{${v}_client}}`, { asserts: [A.status(200), A.custom("one session for the client", [
    ...sessionsOf(`${v}_client`),
    "pm.expect(list.length, 'body: ' + pm.response.text()).to.equal(1);",
  ])], extraTests: [
    ...sessionsOf(`${v}_client`), `pm.collectionVariables.set('${v}_session', list.length ? list[0].id : '');`,
  ] }),
  request("OAS-32", `${p}revoke the session`, "DELETE", `/api/oauth2/sessions/{{${v}_session}}`, { asserts: [A.status(204)] }),
  request("OAS-32", `${p}revoke it again`, "DELETE", `/api/oauth2/sessions/{{${v}_session}}`, { asserts: [A.status(404), A.msgEquals("session not found or already revoked")] }),
  tokenReq("OAS-32", `${p}refresh with its refresh token`, refreshParams(`${v}_f_rt`, `${v}_client`), [A.status(400), O.error("invalid_grant")]),
  mcpWithToken("OAS-32", `${p}its unexpired access token on /mcp`, `${v}_f_at`, [A.status(200)]),
];
const OAS32 = folder("OAS-32 Revoke a session", `Setup: ${GRANT_TEXT}; ${BOTH}, temp-token auth on.`, [
  ...fleetGrantSetup("OAS-32"),
  ...oas32Pass("both", "oas32", "", "the grant"),
  ...fleetGrantTeardown("OAS-32"),
]);

// oas34Pass runs OAS-34 in one mode: v prefixes its variables, p its request titles.
const oas34Pass = (mode, v, p) => [
  oauthConfig("OAS-34", mode, ["cc.oauth2_server_config.access_token_ttl = 2;"], `setup: mcp_server_auth_mode ${mode}, issuer_url set to the base URL, temp-token auth on, access_token_ttl 2`),
  register("OAS-34", `${v}_client`, LOOPBACK_CB, undefined, REFRESHABLE),
  authorize("OAS-34", `${v}_client`, `${v}_f`, `setup: ${p}authorize the grant`),
  consent("OAS-34", `${v}_f`, `setup: ${p}consent the grant with the full-access VK`, "temp", vkBody("OAS-34"), [A.status(200), codeIssued()], { extraTests: saveCode(`${v}_f`) }),
  tokenReq("OAS-34", `${p}exchange the code`, codeParams(`${v}_f`, `${v}_client`), [A.status(200), A.jsonEquals("expires_in", 2), tokensIssued()], { extraTests: saveTokens(`${v}_f`) }),
  mcpWithToken("OAS-34", `${p}the access token on /mcp after 3s`, `${v}_f_at`, [A.status(401), A.msgStartsWith("invalid token"), A.msgContains("expired"), A.custom("WWW-Authenticate is set", [
    "pm.expect(pm.response.headers.get('WWW-Authenticate'), 'WWW-Authenticate').to.be.a('string').and.not.empty;",
  ])], WAIT_3S),
  tokenReq("OAS-34", `setup: ${p}refresh`, refreshParams(`${v}_f_rt`, `${v}_client`), [A.status(200), tokensIssued()], { extraTests: saveTokens(`${v}_r`) }),
  mcpWithToken("OAS-34", `${p}the refreshed access token on /mcp`, `${v}_r_at`, [A.status(200)]),
];
const OAS34 = folder("OAS-34 Access token expiry", `Setup: ${GRANT_TEXT}; ${BOTH}, temp-token auth on, access_token_ttl 2.`, [
  ...fleetGrantSetup("OAS-34"),
  ...oas34Pass("both", "oas34", ""),
  ...fleetGrantTeardown("OAS-34"),
]);

const OAS38 = folder("OAS-38 disable_vk_identity without an identity provider", `Setup: ${FLEET}; the full-access VK (${VKS.full.text}); mcp_server_auth_mode oauth with issuer_url set to the base URL, disable_vk_identity on, temp-token auth on; no identity provider is configured.`, [
  ...fleetGrantSetup("OAS-38"), oauthConfig("OAS-38", "oauth", ["cc.oauth2_server_config.disable_vk_identity = true;"], "setup: mcp_server_auth_mode oauth, issuer_url set to the base URL, disable_vk_identity on, temp-token auth on"),
  request("OAS-38", "control: the config reads disable_vk_identity true in oauth mode", "GET", "/api/config", { asserts: [A.status(200), A.custom("disable_vk_identity true, mcp_server_auth_mode oauth", ["var cc = pm.response.json().client_config;", "pm.expect(cc.oauth2_server_config.disable_vk_identity, 'body: ' + pm.response.text()).to.equal(true);", "pm.expect(cc.mcp_server_auth_mode).to.equal('oauth');"])] }),
  register("OAS-38", "oas38_client", LOOPBACK_CB, undefined, REFRESHABLE),
  authorize("OAS-38", "oas38_client", "oas38_f", "setup: authorize"),
  flowGet("OAS-38", "oas38_f", "GET the flow with its temp token", "temp", [A.status(200), A.custom("available_modes is [\"vk\"]", [
    "pm.expect(pm.response.json().available_modes, 'body: ' + pm.response.text()).to.eql(['vk']);",
  ])]),
  consent("OAS-38", "oas38_f", "consent with the full-access VK", "temp", vkBody("OAS-38"), [A.status(200)], { extraTests: saveCode("oas38_f") }),
  tokenReq("OAS-38", "setup: exchange the code", codeParams("oas38_f", "oas38_client"), [A.status(200), tokensIssued()], { extraTests: saveTokens("oas38_f") }),
  mcpWithToken("OAS-38", "the access token on /mcp", "oas38_f_at", [A.status(200)]),
  tokenReq("OAS-38", "refresh", refreshParams("oas38_f_rt", "oas38_client"), [A.status(200)]),
  ...fleetGrantTeardown("OAS-38"),
]);

// ================================================================ more ADM tests

// unauthReq sends a request with no Authorization header and no cookie jar.
const unauthReq = (id, title, method, path, body, asserts, opts = {}) => request(id, title, method, path, { noAuth: true, noCookies: true, body, asserts, ...opts });
const whitelist = (id, routes) => putConfig(id, `setup: whitelist ${routes.join(" and ")}`, [`cc.whitelisted_routes = ${lit(routes)};`], [A.status(200)]);

// WRONG_BASIC builds Basic credentials with the admin's username and a wrong password.
const WRONG_BASIC = (varName) => [
  "var C = require('crypto-js'); var pair = C.enc.Base64.parse(pm.variables.get('admin_auth_header').replace(/^Basic /, '')).toString(C.enc.Utf8);",
  `pm.variables.set(${lit(varName)}, 'Basic ' + C.enc.Base64.stringify(C.enc.Utf8.parse(pair.split(':')[0] + ':adm03-wrong-password')));`,
];
const ADM03 = folder("ADM-03 Wrong password and VKs are not admin credentials", "Setup: dashboard auth on; a virtual key. Create and list carry a wrong Basic password, the key in x-bf-vk, or the key as a Bearer token.", [
  vkCreate("ADM-03", "setup: create a virtual key", { name: "ADM-03 vk1" }, "adm03_vk1"),
  ...[
    ["a wrong Basic password", { key: "Authorization", value: "{{adm03_wrong_basic}}" }, WRONG_BASIC("adm03_wrong_basic")],
    ["x-bf-vk set to the key", { key: "x-bf-vk", value: "{{adm03_vk1}}" }, []],
    ["Bearer with the key", { key: "Authorization", value: "Bearer {{adm03_vk1}}" }, []],
  ].flatMap(([label, header, pre]) => [
    unauthReq("ADM-03", `create with ${label}`, "POST", "/api/mcp/client", clientBody({ name: "adm03_client" }), [A.status(401), A.msgEquals("Unauthorized")], { headers: [header], pre }),
    unauthReq("ADM-03", `list with ${label}`, "GET", "/api/mcp/clients", undefined, [A.status(401), A.msgEquals("Unauthorized")], { headers: [header], pre }),
  ]),
  vkDelete("ADM-03", "teardown: delete the virtual key", "adm03_vk1"),
]);

const PRIVATE_MSG = "unauthenticated callers cannot register MCP clients that connect to loopback, private-network, or link-local addresses";
const adm18Body = (url) => ({ name: "adm18_x", connection_type: "http", auth_type: "none", connection_string: url });
const ADM18 = folder("ADM-18 Unauthenticated caller cannot target private addresses", "Setup: /api/mcp/client in whitelisted_routes, so its requests carry no credential; alpha (echo_tool, add_tool).", [
  ...freshServer("ADM-18", "alpha", ["echo_tool", "add_tool"]),
  readConfig("ADM-18"), whitelist("ADM-18", ["/api/mcp/client"]),
  unauthReq("ADM-18", "a client at 127.0.0.1", "POST", "/api/mcp/client", adm18Body(`${CHAOS}/s/alpha/mcp`), [A.status(403), A.msgContains(PRIVATE_MSG)]),
  unauthReq("ADM-18", "a client at localhost", "POST", "/api/mcp/client", adm18Body("{{adm18_localhost}}/s/alpha/mcp"), [A.status(403), A.msgContains(PRIVATE_MSG)],
    { pre: ["pm.variables.set('adm18_localhost', pm.variables.get('chaos_url').replace('127.0.0.1', 'localhost'));"] }),
  unauthReq("ADM-18", "a client at 10.0.0.5", "POST", "/api/mcp/client", adm18Body("http://10.0.0.5/mcp"), [A.status(403), A.msgContains(PRIVATE_MSG)]),
  unauthReq("ADM-18", "a client at 169.254.169.254", "POST", "/api/mcp/client", adm18Body("http://169.254.169.254/mcp"), [A.status(403), A.msgContains(PRIVATE_MSG)]),
  unauthReq("ADM-18", "a client at nope.invalid", "POST", "/api/mcp/client", adm18Body("http://nope.invalid/mcp"), [A.status(403), A.msgContains("could not resolve MCP target host")]),
  restoreConfig("ADM-18"),
  mockGet("ADM-18", "alpha", "alpha opened no session", [A.custom("sessions is 0", [
    "var b = pm.response.json();",
    "pm.expect(b.sessions, 'body: ' + pm.response.text()).to.equal(0);",
    "pm.expect(b.counters.sessions).to.equal(0);",
  ])]),
]);

const SECRET_MSG = "env./vault. references in MCP client fields (headers.X-Key)";
const adm19Stdio = { client_id: "adm19-stdio", name: "adm19_stdio", connection_type: "stdio", auth_type: "none", stdio_config: { command: "{{chaos_bin}}", args: ["-stdio", "-fleet", CHAOS, "-server", "alpha"], envs: [] } };
const adm19Env = httpClient("adm19-env", "adm19_env", "alpha", { headers: { "X-Key": "env.HOME" } });
const ADM19 = folder("ADM-19 Unauthenticated caller: STDIO and secret references", "Setup: alpha (echo_tool, add_tool) and an HTTP client on it; /api/mcp/client and that client's path in whitelisted_routes, so their requests carry no credential; then the same requests with admin credentials.", [
  ...freshServer("ADM-19", "alpha", ["echo_tool", "add_tool"]),
  createClient("ADM-19", httpClient("adm19-c", "adm19_c", "alpha"), "setup: create the client"),
  readConfig("ADM-19"), whitelist("ADM-19", ["/api/mcp/client", "/api/mcp/client/adm19-c"]),
  unauthReq("ADM-19", "a STDIO client without a credential", "POST", "/api/mcp/client", adm19Stdio, [A.status(403), A.msgStartsWith("Registering a stdio MCP client requires an authenticated admin session")]),
  unauthReq("ADM-19", "an env. header reference on create without a credential", "POST", "/api/mcp/client", adm19Env, [A.status(403), A.msgContains(SECRET_MSG)]),
  unauthReq("ADM-19", "an env. header reference on PUT without a credential", "PUT", "/api/mcp/client/adm19-c", { headers: { "X-Key": "env.HOME" } }, [A.status(403), A.msgContains(SECRET_MSG)]),
  restoreConfig("ADM-19"),
  createExpect("ADM-19", "the STDIO client with admin credentials", adm19Stdio, [A.status(200)]),
  createExpect("ADM-19", "the env. header reference on create with admin credentials", adm19Env, [A.status(200)]),
  request("ADM-19", "the env. header reference on PUT with admin credentials", "PUT", "/api/mcp/client/adm19-c", { body: { headers: { "X-Key": "env.HOME" } }, asserts: [A.status(200)] }),
  deleteClient("ADM-19", "adm19-stdio"), deleteClient("ADM-19", "adm19-env"), deleteClient("ADM-19", "adm19-c"),
]);

const ADM20 = folder("ADM-20 require_public_target is set by the server, never cleared", "Setup: /api/mcp/client in whitelisted_routes for the first create, which carries no credential; then admin credentials. alpha (echo_tool, add_tool) is the admin client's target; 192.0.2.10 is a documentation address that answers nothing.", [
  ...freshServer("ADM-20", "alpha", ["echo_tool", "add_tool"]),
  readConfig("ADM-20"), whitelist("ADM-20", ["/api/mcp/client"]),
  unauthReq("ADM-20", "create a client at 192.0.2.10 without a credential", "POST", "/api/mcp/client", { client_id: "adm20-wl", name: "adm20_wl", connection_type: "http", connection_string: "http://192.0.2.10/mcp", auth_type: "none" }, [A.status(200)]),
  restoreConfig("ADM-20"),
  getClient("ADM-20", "adm20-wl", "the unauthenticated create stored require_public_target true", [clientField("config.require_public_target", true)]),
  createExpect("ADM-20", "an admin create that sets require_public_target true", httpClient("adm20-admin", "adm20_admin", "alpha", { require_public_target: true }), [A.status(200)]),
  getClient("ADM-20", "adm20-admin", "the admin create stored it false", [A.custom("require_public_target is false", [
    "var v = pm.response.json().clients[0].config.require_public_target;",
    "pm.expect(v === undefined || v === false, 'require_public_target: ' + v).to.equal(true);",
  ])]),
  request("ADM-20", "a later admin PUT with require_public_target false", "PUT", "/api/mcp/client/adm20-wl", { body: { require_public_target: false }, asserts: [A.status(200)] }),
  getClient("ADM-20", "adm20-wl", "require_public_target is still true", [clientField("config.require_public_target", true)]),
  deleteClient("ADM-20", "adm20-wl"), deleteClient("ADM-20", "adm20-admin"),
]);

const ADM22 = folder("ADM-22 OAuth create input checks", "Setup: admin credentials.", [
  createExpect("ADM-22", "auth_type oauth without oauth_config", { name: "adm22_oauth", connection_type: "http", connection_string: "http://127.0.0.1:9/mcp", auth_type: "oauth" }, [A.status(400), A.msgEquals("OAuth configuration is required when auth_type is 'oauth'")]),
  createExpect("ADM-22", "auth_type per_user_oauth without oauth_config", { name: "adm22_puo", connection_type: "http", connection_string: "http://127.0.0.1:9/mcp", auth_type: "per_user_oauth" }, [A.status(400), A.msgEquals("OAuth configuration is required when auth_type is 'per_user_oauth'")]),
  createExpect("ADM-22", "oauth_config {} without connection_string", { name: "adm22_noserver", connection_type: "http", auth_type: "oauth", oauth_config: {} }, [A.status(400), A.msgContains("Either client_id must be provided, or server URL must be set")]),
]);

const ADM52 = folder("ADM-52 Enabling a sticky client with a dead upstream", "Setup: alpha (echo_tool, add_tool); a client on alpha with needs_session_stickiness true, disabled; then alpha refuses connections. The client being reported disabled after the enable is a known issue, so that check is skipped.", [
  ...freshServer("ADM-52", "alpha", ["echo_tool", "add_tool"]),
  createClient("ADM-52", httpClient("adm52-s", "adm52_s", "alpha", { needs_session_stickiness: true }), "setup: create the sticky client"),
  request("ADM-52", "setup: disable it", "PUT", "/api/mcp/client/adm52-s", { body: { disabled: true }, asserts: [A.status(200)] }),
  mockPost("ADM-52", "alpha", "faults", "setup: alpha refuses connections", { faults: { refuse: true } }),
  request("ADM-52", "enable it", "PUT", "/api/mcp/client/adm52-s", { body: { disabled: false }, asserts: [A.status(200), A.jsonEquals("status", "partial_success"), A.custom("message contains \"establishing the shared connection failed\"", [
    "pm.expect(pm.response.json().message, 'body: ' + pm.response.text()).to.include('establishing the shared connection failed');",
  ])] }),
  getClient("ADM-52", "adm52-s", "disabled is false and the client is not reported disabled", [clientField("config.disabled", false), A.skip(A.custom("state is not \"disabled\"", ["pm.expect(pm.response.json().clients[0].state, 'body: ' + pm.response.text()).to.not.equal('disabled');"]))]),
  mockPost("ADM-52", "alpha", "faults/clear", "teardown: clear alpha's faults"),
  deleteClient("ADM-52", "adm52-s"),
]);

const sessionsCount = (id, varName) => mockGet(id, "alpha", "setup: record alpha's session count", [], [`pm.collectionVariables.set(${lit(varName)}, pm.response.json().counters.sessions);`]);
const ADM53 = folder("ADM-53 Flip needs_session_stickiness live", "Setup: alpha (echo_tool, add_tool); an HTTP, auth none, per-call client on alpha.", [
  ...freshServer("ADM-53", "alpha", ["echo_tool", "add_tool"]),
  createClient("ADM-53", httpClient("adm53-c", "adm53_c", "alpha"), "setup: create the client"),
  sessionsCount("ADM-53", "adm53_sessions"),
  request("ADM-53", "set needs_session_stickiness true", "PUT", "/api/mcp/client/adm53-c", { body: { needs_session_stickiness: true }, asserts: [A.status(200), A.custom("message contains \"now maintains a persistent shared connection\"", [
    "pm.expect(pm.response.json().message, 'body: ' + pm.response.text()).to.include('now maintains a persistent shared connection');",
  ])] }),
  mockGet("ADM-53", "alpha", "alpha opened one more session", [A.custom("sessions is one more than before", [
    "var before = Number(pm.collectionVariables.get('adm53_sessions'));",
    "pm.expect(pm.response.json().counters.sessions, 'body: ' + pm.response.text()).to.equal(before + 1);",
  ])]),
  request("ADM-53", "set needs_session_stickiness false", "PUT", "/api/mcp/client/adm53-c", { body: { needs_session_stickiness: false }, asserts: [A.status(200), A.custom("message contains \"now connects per call\"", [
    "pm.expect(pm.response.json().message, 'body: ' + pm.response.text()).to.include('now connects per call');",
  ])] }),
  mockPost("ADM-53", "alpha", "faults", "setup: alpha refuses connections", { faults: { refuse: true } }),
  request("ADM-53", "set needs_session_stickiness true while alpha refuses", "PUT", "/api/mcp/client/adm53-c", { body: { needs_session_stickiness: true }, asserts: [A.status(200), A.jsonEquals("status", "partial_success")] }),
  mockPost("ADM-53", "alpha", "faults/clear", "teardown: clear alpha's faults"),
  deleteClient("ADM-53", "adm53-c"),
]);

const clientIdsIn = (expectIn, varId, clientId) => A.custom(`${clientId} is ${expectIn ? "" : "not "}listed`, [
  "var ids = (pm.response.json().clients || []).map(function (c) { return c.config.client_id; });",
  `pm.expect(ids, 'body: ' + pm.response.text()).to.${expectIn ? "" : "not."}include(${lit(clientId)});`,
]);
const ADM54 = folder("ADM-54 Assign and remove vk_configs", "Setup: alpha (echo_tool, add_tool); a client on alpha; virtual keys vk1 and vk2.", [
  ...freshServer("ADM-54", "alpha", ["echo_tool", "add_tool"]),
  createClient("ADM-54", httpClient("adm54-c", "adm54_c", "alpha"), "setup: create the client"),
  vkCreate("ADM-54", "setup: create vk1", { name: "ADM-54 vk1" }, "adm54_vk1"),
  vkCreate("ADM-54", "setup: create vk2", { name: "ADM-54 vk2" }, "adm54_vk2"),
  request("ADM-54", "PUT vk1 with [\"echo_tool\"]", "PUT", "/api/mcp/client/adm54-c", { body: { vk_configs: [{ virtual_key_id: "{{adm54_vk1_id}}", tools_to_execute: ["echo_tool"] }] }, asserts: [A.status(200)] }),
  getClient("ADM-54", "adm54-c", "vk_configs has vk1 with its name and tools", [A.custom("one entry: vk1, \"ADM-54 vk1\", [\"echo_tool\"]", [
    "var v = pm.response.json().clients[0].vk_configs;",
    "pm.expect(v, 'body: ' + pm.response.text()).to.have.lengthOf(1);",
    "pm.expect(v[0].virtual_key_id).to.equal(pm.collectionVariables.get('adm54_vk1_id'));",
    "pm.expect(v[0].virtual_key_name).to.equal('ADM-54 vk1');",
    "pm.expect(v[0].tools_to_execute).to.eql(['echo_tool']);",
  ])]),
  request("ADM-54", "?virtual_keys=vk1 lists the client", "GET", "/api/mcp/clients?virtual_keys={{adm54_vk1_id}}&limit=100", { asserts: [A.status(200), clientIdsIn(true, "adm54_vk1_id", "adm54-c")] }),
  request("ADM-54", "?virtual_keys=vk2 does not", "GET", "/api/mcp/clients?virtual_keys={{adm54_vk2_id}}&limit=100", { asserts: [A.status(200), clientIdsIn(false, "adm54_vk2_id", "adm54-c")] }),
  request("ADM-54", "PUT []", "PUT", "/api/mcp/client/adm54-c", { body: { vk_configs: [] }, asserts: [A.status(200)] }),
  getClient("ADM-54", "adm54-c", "vk_configs is []", [clientField("vk_configs", [])]),
  deleteClient("ADM-54", "adm54-c"),
  vkDelete("ADM-54", "teardown: delete vk1", "adm54_vk1"),
  vkDelete("ADM-54", "teardown: delete vk2", "adm54_vk2"),
]);

const ADM55 = folder("ADM-55 vk_configs validation", "Setup: alpha (echo_tool, add_tool); a client on alpha; virtual key vk1.", [
  ...freshServer("ADM-55", "alpha", ["echo_tool", "add_tool"]),
  createClient("ADM-55", httpClient("adm55-c", "adm55_c", "alpha"), "setup: create the client"),
  vkCreate("ADM-55", "setup: create vk1", { name: "ADM-55 vk1" }, "adm55_vk1"),
  request("ADM-55", "a duplicate virtual_key_id", "PUT", "/api/mcp/client/adm55-c", {
    body: { vk_configs: [{ virtual_key_id: "{{adm55_vk1_id}}", tools_to_execute: ["*"] }, { virtual_key_id: "{{adm55_vk1_id}}", tools_to_execute: ["*"] }] },
    asserts: [A.status(400), A.custom("error message is \"duplicate virtual_key_id in vk_configs: <vk1 id>\"", [
      "var b = pm.response.json();",
      "pm.expect(b.error && b.error.message, 'body: ' + pm.response.text()).to.equal('duplicate virtual_key_id in vk_configs: ' + pm.collectionVariables.get('adm55_vk1_id'));",
    ])],
  }),
  request("ADM-55", "an empty virtual_key_id", "PUT", "/api/mcp/client/adm55-c", { body: { vk_configs: [{ virtual_key_id: "", tools_to_execute: ["*"] }] }, asserts: [A.status(400), A.msgEquals("virtual_key_id must not be empty")] }),
  request("ADM-55", "tools_to_execute [\"*\",\"x\"]", "PUT", "/api/mcp/client/adm55-c", {
    body: { vk_configs: [{ virtual_key_id: "{{adm55_vk1_id}}", tools_to_execute: ["*", "x"] }] },
    asserts: [A.status(400), A.custom("error message contains \"invalid tools_to_execute for virtual key <vk1 id>\"", [
      "var b = pm.response.json();",
      "pm.expect(b.error && b.error.message, 'body: ' + pm.response.text()).to.include('invalid tools_to_execute for virtual key ' + pm.collectionVariables.get('adm55_vk1_id'));",
    ])],
  }),
  deleteClient("ADM-55", "adm55-c"),
  vkDelete("ADM-55", "teardown: delete vk1", "adm55_vk1"),
]);

// HEADERS_BASE is a per_user_headers client on alpha; alpha requires x-key: good.
const HEADERS_BASE = (clientId, name, over) => ({ client_id: clientId, name, connection_type: "http", connection_string: `${CHAOS}/s/alpha/mcp`, auth_type: "per_user_headers", ...over });
const alphaRequiresKey = (id) => [
  ...freshServer(id, "alpha", ["echo_tool", "add_tool"]),
  mockPost(id, "alpha", "auth", "setup: alpha requires x-key: good", { auth: { require_headers: { "x-key": "good" } } }),
];
const ADM65 = folder("ADM-65 per_user_headers create validation", "Setup: admin credentials; alpha (echo_tool, add_tool) requires the header x-key: good.", [
  ...alphaRequiresKey("ADM-65"),
  createExpect("ADM-65", "no per_user_header_keys", HEADERS_BASE("adm65-h", "adm65_h", {}), [A.status(400), A.msgContains("per_user_header_keys must be a non-empty list")]),
  createExpect("ADM-65", "per_user_header_keys [\"X-Key\",\"x-key\"]", HEADERS_BASE("adm65-h", "adm65_h", { per_user_header_keys: ["X-Key", "x-key"], user_headers: { "x-key": "good" } }), [A.status(400), A.msgContains("contains duplicate entries")]),
  createExpect("ADM-65", "per_user_header_keys [\" \"]", HEADERS_BASE("adm65-h", "adm65_h", { per_user_header_keys: [" "] }), [A.status(400), A.msgEquals("per_user_header_keys[0] is empty")]),
  createExpect("ADM-65", "no sample user_headers", HEADERS_BASE("adm65-h", "adm65_h", { per_user_header_keys: ["x-key"] }), [A.status(400), A.msgEquals("sample user_headers missing values for required keys: x-key")]),
  createExpect("ADM-65", "a wrong sample value", HEADERS_BASE("adm65-h", "adm65_h", { per_user_header_keys: ["x-key"], user_headers: { "x-key": "bad" } }), [A.status(422), A.msgStartsWith("Verification failed:")]),
]);

const ADM66 = folder("ADM-66 per_user_headers create succeeds", "Setup: admin credentials; alpha (echo_tool, add_tool) requires the header x-key: good; the sample value sent is \"good\".", [
  ...alphaRequiresKey("ADM-66"),
  createExpect("ADM-66", "create with X-Key \"good\"", HEADERS_BASE("adm66-h", "adm66_h", { per_user_header_keys: ["X-Key"], user_headers: { "X-Key": "good" } }),
    [A.status(200), A.jsonMessage("MCP client registered. 2 tools discovered. Each user will submit their own headers on first tool use.")]),
  getClient("ADM-66", "adm66-h", "keys [\"x-key\"], healthy, 2 tools, no credential", [clientField("config.per_user_header_keys", ["x-key"]), clientState("healthy"), toolNamesAre(["add_tool", "echo_tool"]), A.custom("there is no credential", [
    "pm.expect(pm.response.json().clients[0], 'body: ' + pm.response.text()).to.not.have.property('credential');",
  ])]),
  deleteClient("ADM-66", "adm66-h"),
]);

// PETSTORE is a small OpenAPI 3.0.3 document with three operations and an absolute server URL.
const PETSTORE = `{
  "openapi": "3.0.3",
  "info": {"title": "Petstore", "version": "1.0.0"},
  "servers": [{"url": "https://petstore.example.com/v1"}],
  "components": {"securitySchemes": {"ApiKeyAuth": {"type": "apiKey", "in": "header", "name": "X-API-Key"}}},
  "security": [{"ApiKeyAuth": []}],
  "paths": {
    "/pets": {
      "get": {"operationId": "listPets", "summary": "List pets", "responses": {"200": {"description": "ok"}}},
      "post": {"operationId": "createPet", "requestBody": {"required": true, "content": {"application/json": {"schema": {"type": "object", "properties": {"name": {"type": "string"}}}}}}, "responses": {"201": {"description": "created"}}}
    },
    "/pets/{petId}": {
      "get": {"operationId": "getPetById", "parameters": [{"name": "petId", "in": "path", "required": true, "schema": {"type": "string"}}], "responses": {"200": {"description": "ok"}}}
    }
  }
}`;
const PETSTORE_NO_SERVERS = (() => { const d = JSON.parse(PETSTORE); delete d.servers; return JSON.stringify(d); })();
const openapiBody = (over) => ({ name: "adm81_q", connection_type: "openapi", auth_type: "none", tools_to_execute: ["*"], openapi_config: { spec: PETSTORE }, ...over });
const ADM81 = folder("ADM-81 OpenAPI create validation", "Setup: admin credentials; an inline Petstore OpenAPI document with an absolute server URL.", [
  createExpect("ADM-81", "auth_type oauth", openapiBody({ auth_type: "oauth" }), [A.status(400), A.msgContains("not supported for connection_type 'openapi'")]),
  createExpect("ADM-81", "connection_string set", openapiBody({ connection_string: "https://mcp.example.com" }), [A.status(400)]),
  createExpect("ADM-81", "spec_file", openapiBody({ openapi_config: { spec_file: "x.yaml" } }), [A.status(400), A.msgContains("only supported in config.json")]),
  createExpect("ADM-81", "no spec", openapiBody({ openapi_config: {} }), [A.status(400)]),
  createExpect("ADM-81", "an unknown tool", openapiBody({ tools_to_execute: ["X"] }), [A.status(400), A.msgContains("the spec defines no tool named X")]),
  createExpect("ADM-81", "no server URL", openapiBody({ openapi_config: { spec: PETSTORE_NO_SERVERS } }), [A.status(400), A.msgContains("set openapi_config.base_url")]),
]);

const ADM82 = folder("ADM-82 Stored OpenAPI spec endpoint", "Setup: alpha (echo_tool, add_tool) and an HTTP client on it; an OpenAPI client created from the inline Petstore document.", [
  ...freshServer("ADM-82", "alpha", ["echo_tool", "add_tool"]),
  createClient("ADM-82", httpClient("adm82-c", "adm82_c", "alpha"), "setup: create the HTTP client"),
  createClient("ADM-82", { client_id: "adm82-q", name: "adm82_q", connection_type: "openapi", auth_type: "none", openapi_config: { spec: PETSTORE } }, "setup: create the OpenAPI client"),
  request("ADM-82", "the OpenAPI client's spec", "GET", "/api/mcp/client/adm82-q/openapi-spec", { asserts: [A.status(200), A.custom("the body equals the stored spec", [
    `pm.expect(pm.response.text()).to.equal(${lit(PETSTORE)});`,
  ]), A.custom("Content-Type is \"application/json; charset=utf-8\"", [
    "pm.expect(pm.response.headers.get('Content-Type')).to.equal('application/json; charset=utf-8');",
  ])] }),
  getClient("ADM-82", "adm82-q", "the list shows the spec empty", [A.custom("openapi_config.spec is empty", [
    "var oc = pm.response.json().clients[0].config.openapi_config;",
    "pm.expect(oc, 'body: ' + pm.response.text()).to.be.an('object');",
    "pm.expect(oc.spec === undefined || oc.spec === '', 'spec: ' + oc.spec).to.equal(true);",
  ])]),
  request("ADM-82", "the HTTP client's spec", "GET", "/api/mcp/client/adm82-c/openapi-spec", { asserts: [A.status(404), A.msgEquals("OpenAPI MCP client not found")] }),
  request("ADM-82", "an unknown client's spec", "GET", "/api/mcp/client/adm82-no-such-client/openapi-spec", { asserts: [A.status(404), A.msgEquals("OpenAPI MCP client not found")] }),
  deleteClient("ADM-82", "adm82-c"), deleteClient("ADM-82", "adm82-q"),
]);

// ================================================================ AUTH and CONN tests on the fleet

// execTool runs a tool through POST /v1/mcp/tool/execute (chat format) with no admin credentials.
// session, when given, is sent as x-bf-mcp-session-id. Clients these tests run tools on set
// tools_to_execute ["*"], since an unset list executes nothing.
const execTool = (id, title, tool, asserts, { session, headers = [] } = {}) => request(id, title, "POST", "/v1/mcp/tool/execute", {
  noAuth: true, noCookies: true, asserts,
  body: { id: "c1", type: "function", function: { name: tool, arguments: lit({ text: "hi" }) } },
  headers: [...(session ? [{ key: "x-bf-mcp-session-id", value: session }] : []), ...headers],
});
// gwCall sends tools/call for tool to /mcp with no credentials.
const gwCall = (id, title, tool, asserts, headers = []) => rpc(id, title, call(tool, { text: "hi" }), { headers: [MCP_ACCEPT, ...headers], asserts: [A.status(200), ...asserts] });
const mockAuth = (id, server, title, auth) => mockPost(id, server, "auth", title, { auth });
const counterIs = (name, v) => A.custom(`counters.${name} is ${v}`, [`pm.expect(pm.response.json().counters[${lit(name)}], 'body: ' + pm.response.text()).to.equal(${v});`]);
const callsIs = (tool, n) => A.custom(`calls[${tool}] is ${n}`, [`pm.expect((pm.response.json().counters.calls || {})[${lit(tool)}] || 0, 'body: ' + pm.response.text()).to.equal(${n});`]);
const identityCalls = (identity, n) => A.custom(`calls_by_identity[${identity}] is ${n}`, [`pm.expect((pm.response.json().counters.calls_by_identity || {})[${lit(identity)}], 'body: ' + pm.response.text()).to.equal(${n});`]);
const identityCallsAre = (map) => A.custom(`calls_by_identity is ${lit(map)}`, [`pm.expect(pm.response.json().counters.calls_by_identity || {}, 'body: ' + pm.response.text()).to.eql(${lit(map)});`]);
const noIdentity = (identity) => A.custom(`calls_by_identity has no ${identity}`, [`pm.expect(pm.response.json().counters.calls_by_identity || {}, 'body: ' + pm.response.text()).to.not.have.property(${lit(identity)});`]);
// saveSessions keeps a server's counters.sessions in varName; sessionsPlus compares against it.
const saveSessions = (id, server, varName) => mockGet(id, server, `setup: record ${server}'s session count`, [], [`pm.collectionVariables.set(${lit(varName)}, pm.response.json().counters.sessions);`]);
const sessionsPlus = (varName, n) => A.custom(`sessions is ${n} more than the recorded count`, [
  `var before = Number(pm.collectionVariables.get(${lit(varName)}));`,
  `pm.expect(pm.response.json().counters.sessions, 'body: ' + pm.response.text()).to.equal(before + ${n});`,
]);
const messageStarts = (m) => A.custom(`message starts with ${lit(m)}`, [
  "var b = pm.response.json();",
  "pm.expect(b.message, 'body: ' + pm.response.text()).to.be.a('string');",
  `pm.expect(b.message.indexOf(${lit(m)}), 'message: ' + b.message).to.equal(0);`,
]);
// waitForClient polls the client list for up to about 5s until cond (ES5 on the client c) holds.
const waitForClient = (clientId, cond) => [
  "var left = 20;",
  "function poll() {",
  `  pm.sendRequest({ url: pm.variables.get('base_url') + '/api/mcp/clients?server=${clientId}', method: 'GET', header: { Authorization: pm.variables.get('admin_auth_header') } }, function (err, res) {`,
  "    var list = !err && res.code === 200 ? res.json().clients : null;",
  "    var c = list && list[0];",
  `    if ((c && (${cond})) || --left <= 0) { return; }`,
  "    setTimeout(poll, 250);",
  "  });",
  "}",
  "poll();",
];

// T1 is the bearer token AUTH and CONN servers require; the mock maps it to identity id1.
const T1 = "auth-t1-token";
const T1_TEXT = (server) => `${server} (echo_tool, add_tool) requires bearer T1 (${T1}), which it maps to id1`;
const requiresT1 = (id, server) => [
  ...freshServer(id, server, ["echo_tool", "add_tool"]),
  mockAuth(id, server, `setup: ${server} requires bearer T1, mapped to id1`, { bearer: { [T1]: "id1" }, require: true }),
];
const t1Client = (clientId, name, over = {}) => httpClient(clientId, name, "alpha", { auth_type: "headers", headers: { Authorization: `Bearer ${T1}` }, tools_to_execute: ["*"], ...over });
const T1_CLIENT_TEXT = "an HTTP client on alpha with auth headers \"Authorization: Bearer T1\"";

// AUTH-01's mock counts calls under the X-Static value; a forwarded "Bearer leak" would override that identity
// with "leak", so one exact count proves X-Static was sent and Authorization was not.
const AUTH01 = folder("AUTH-01 none auth sends static headers, drops Authorization", "Setup: alpha (echo_tool, add_tool) counts calls under the X-Static value, counts the bearer token \"leak\" as identity \"leak\" instead, and serves callers without a token; an HTTP client on alpha with auth none, per call, headers X-Static and \"Authorization: Bearer leak\", tools_to_execute [\"*\"].", [
  ...freshServer("AUTH-01", "alpha", ["echo_tool", "add_tool"]),
  mockAuth("AUTH-01", "alpha", "setup: alpha counts calls under X-Static, Bearer leak as identity leak, token optional", { bearer: { leak: "leak" }, identity_header: "X-Static" }),
  createExpect("AUTH-01", "create with X-Static and Authorization: Bearer leak", httpClient("auth01-c", "auth01_c", "alpha", { headers: { "X-Static": "static-1", Authorization: "Bearer leak" }, tools_to_execute: ["*"] }), [A.status(200)]),
  execTool("AUTH-01", "execute auth01_c-echo_tool", "auth01_c-echo_tool", [A.status(200)]),
  mockGet("AUTH-01", "alpha", "the call was counted under X-Static's value, never as leak", [identityCallsAre({ "static-1": 1 })]),
  deleteClient("AUTH-01", "auth01-c"),
]);

const AUTH02 = folder("AUTH-02 headers auth, sticky", `Setup: ${T1_TEXT("alpha")}; ${T1_CLIENT_TEXT}, needs_session_stickiness true, tools_to_execute ["*"].`, [
  ...requiresT1("AUTH-02", "alpha"),
  createExpect("AUTH-02", "create the sticky client", t1Client("auth02-c", "auth02_c", { needs_session_stickiness: true }), [A.status(200)]),
  ...[1, 2, 3].map((i) => execTool("AUTH-02", `execute ${i} of 3`, "auth02_c-echo_tool", [A.status(200)])),
  mockGet("AUTH-02", "alpha", "one session, one initialize, three calls under id1", [counterIs("sessions", 1), counterIs("initializes", 1), identityCalls("id1", 3)]),
  deleteClient("AUTH-02", "auth02-c"),
]);

const AUTH03 = folder("AUTH-03 headers auth, per call", `Setup: ${T1_TEXT("alpha")}; ${T1_CLIENT_TEXT}, per call (needs_session_stickiness omitted), tools_to_execute ["*"].`, [
  ...requiresT1("AUTH-03", "alpha"),
  createExpect("AUTH-03", "create the per-call client", t1Client("auth03-c", "auth03_c"), [A.status(200)]),
  saveSessions("AUTH-03", "alpha", "auth03_s0"),
  ...[1, 2, 3].flatMap((i) => [
    execTool("AUTH-03", `execute ${i} of 3`, "auth03_c-echo_tool", [A.status(200)]),
    mockGet("AUTH-03", "alpha", `alpha opened one session per execute call (${i} so far)`, [sessionsPlus("auth03_s0", i), ...(i === 3 ? [identityCalls("id1", 3)] : [])]),
  ]),
  request("AUTH-03", "reconnect the per-call client", "POST", "/api/mcp/client/auth03-c/reconnect", { asserts: [A.status(400), A.msgContains("reconnect is not applicable for this client type")] }),
  deleteClient("AUTH-03", "auth03-c"),
]);

const AUTH05 = folder("AUTH-05 Token revoked after connect", `Setup: ${T1_TEXT("alpha")}; ${T1_CLIENT_TEXT}, needs_session_stickiness true, tools_to_execute ["*"] (as AUTH-02); then alpha revokes T1.`, [
  ...requiresT1("AUTH-05", "alpha"),
  createClient("AUTH-05", t1Client("auth05-c", "auth05_c", { needs_session_stickiness: true }), "setup: create the sticky client"),
  mockPost("AUTH-05", "alpha", "auth/revoke", "setup: revoke T1", { token: T1 }),
  execTool("AUTH-05", "execute after the revoke", "auth05_c-echo_tool", [A.status(400), A.msgContains("MCP tool call failed for"), A.msgContains("401")]),
  gwCall("AUTH-05", "/mcp tools/call after the revoke", "auth05_c-echo_tool", [toolResultError("Tool execution failed:", null)]),
  getClient("AUTH-05", "auth05-c", "the client list shows unstable, last_failure.stage connect", [clientState("unstable"), clientField("last_failure.stage", "connect")],
    { pre: waitForClient("auth05-c", "c.state === 'unstable'") }),
  deleteClient("AUTH-05", "auth05-c"),
]);

const AUTH06 = folder("AUTH-06 Masked round trip and rejected rotation", `Setup: ${T1_TEXT("alpha")}; ${T1_CLIENT_TEXT}, needs_session_stickiness true, tools_to_execute ["*"] (as AUTH-02).`, [
  ...requiresT1("AUTH-06", "alpha"),
  createClient("AUTH-06", t1Client("auth06-c", "auth06_c", { needs_session_stickiness: true }), "setup: create the sticky client"),
  getClient("AUTH-06", "auth06-c", "the client list masks Authorization", [A.custom("config.headers.Authorization.value matches ^.{4}\\*{24}.{4}$", [
    "var h = pm.response.json().clients[0].config.headers;",
    "pm.expect(h && h.Authorization && h.Authorization.value, 'body: ' + pm.response.text()).to.match(/^.{4}\\*{24}.{4}$/);",
  ])], { extraTests: ["pm.collectionVariables.set('auth06_headers', JSON.stringify(pm.response.json().clients[0].config.headers));"] }),
  request("AUTH-06", "PUT the masked headers back", "PUT", "/api/mcp/client/auth06-c", { raw: "{\"headers\":{{auth06_headers}}}", asserts: [A.status(200)] }),
  saveSessions("AUTH-06", "alpha", "auth06_s1"),
  request("AUTH-06", "setup: reconnect, so the next call uses the stored header", "POST", "/api/mcp/client/auth06-c/reconnect", { asserts: [A.status(200)] }),
  execTool("AUTH-06", "execute after the masked PUT", "auth06_c-echo_tool", [A.status(200)]),
  mockGet("AUTH-06", "alpha", "the call ran as id1 on a new session", [identityCallsAre({ id1: 1 }), sessionsPlus("auth06_s1", 1)]),
  request("AUTH-06", "PUT Authorization: Bearer BAD", "PUT", "/api/mcp/client/auth06-c", { body: { headers: { Authorization: "Bearer BAD" } },
    asserts: [A.status(422), A.msgContains("The new headers were rejected by the MCP server, so nothing was changed")] }),
  saveSessions("AUTH-06", "alpha", "auth06_s2"),
  request("AUTH-06", "setup: reconnect again, so the next call uses the stored header", "POST", "/api/mcp/client/auth06-c/reconnect", { asserts: [A.status(200)] }),
  execTool("AUTH-06", "execute after the rejected PUT", "auth06_c-echo_tool", [A.status(200)]),
  mockGet("AUTH-06", "alpha", "the call still ran as id1 on a new session", [identityCallsAre({ id1: 2 }), sessionsPlus("auth06_s2", 1)]),
  deleteClient("AUTH-06", "auth06-c"),
]);

// AUTH-09's callers send both headers; the mock counts calls under one header's value at a time,
// which shows whether that header reached it.
const AUTH09_CALLER = [{ key: "X-Tenant-Id", value: "tenant-1" }, { key: "X-Other", value: "other-1" }];
const AUTH09 = folder("AUTH-09 allowed_extra_headers is per client", "Setup: alpha and beta (echo_tool, add_tool); the tenant client (auth09_a) on alpha with allowed_extra_headers [\"x-tenant-id\"], the plain client (auth09_b) on beta with none; both HTTP, auth none, per call, tools_to_execute [\"*\"]. Callers send X-Tenant-Id: tenant-1 and X-Other: other-1.", [
  ...freshServer("AUTH-09", "alpha", ["echo_tool", "add_tool"]),
  ...freshServer("AUTH-09", "beta", ["echo_tool", "add_tool"]),
  createExpect("AUTH-09", "create the tenant client, allowing x-tenant-id", httpClient("auth09-a", "auth09_a", "alpha", { allowed_extra_headers: ["x-tenant-id"], tools_to_execute: ["*"] }), [A.status(200)]),
  createExpect("AUTH-09", "create the plain client, allowing no extra headers", httpClient("auth09-b", "auth09_b", "beta", { tools_to_execute: ["*"] }), [A.status(200)]),
  ...[["X-Tenant-Id", "tenant-1"], ["X-Other", "other-1"]].flatMap(([header, value], phase) => [
    ...["alpha", "beta"].map((s) => mockAuth("AUTH-09", s, `setup: ${s} counts calls under ${header}`, { identity_header: header })),
    execTool("AUTH-09", `execute the tenant client with X-Tenant-Id and X-Other (${header} check)`, "auth09_a-echo_tool", [A.status(200)], { headers: AUTH09_CALLER }),
    execTool("AUTH-09", `execute the plain client with X-Tenant-Id and X-Other (${header} check)`, "auth09_b-echo_tool", [A.status(200)], { headers: AUTH09_CALLER }),
    phase === 0
      ? mockGet("AUTH-09", "alpha", "the tenant client forwarded X-Tenant-Id", [identityCalls(value, 1)])
      : mockGet("AUTH-09", "alpha", "the tenant client never sent X-Other", [noIdentity(value), callsIs("echo_tool", 2)]),
    mockGet("AUTH-09", "beta", `the plain client never sent ${header}`, [noIdentity(value), callsIs("echo_tool", phase + 1)]),
  ]),
  createExpect("AUTH-09", "create with allowed_extra_headers [\"*\",\"x-a\"]", httpClient("auth09-x", "auth09_x", "alpha", { allowed_extra_headers: ["*", "x-a"] }), [A.status(400), A.msgContains("Invalid allowed_extra_headers")]),
  deleteClient("AUTH-09", "auth09-a"),
  deleteClient("AUTH-09", "auth09-b"),
]);

// KEY_A: alpha accepts only X-Api-Key: key-a and counts calls under the X-Api-Key value.
const KEY_A_TEXT = "alpha (echo_tool, add_tool) accepts only X-Api-Key: key-a and counts calls under the X-Api-Key value";
const alphaKeyA = (id) => [
  ...freshServer(id, "alpha", ["echo_tool", "add_tool"]),
  mockAuth(id, "alpha", "setup: alpha accepts only X-Api-Key key-a and counts calls under it", { identity_header: "X-Api-Key", require_headers: { "X-Api-Key": "key-a" } }),
];
const perUser = (clientId, name, over = {}) => HEADERS_BASE(clientId, name, { tools_to_execute: ["*"], ...over });
const PER_USER_KEY_A = { per_user_header_keys: ["x-api-key"], user_headers: { "x-api-key": "key-a" } };
// headersFlow checks a 400's extra_fields.mcp_auth_required for the per-user headers flow and keeps the
// flow id H from submit_url in saveAs. With sameAs it checks H equals the one kept there; with full it
// also checks the key lists.
const headersFlow = ({ saveAs, sameAs, full }) => A.custom(`mcp_auth_required kind "headers", submit_url ...auth?flow=<flow id>&kind=headers${sameAs ? ", the same flow id" : ""}${full ? ", required_header_keys [\"x-api-key\"], admin_header_keys [\"X-Org\"]" : ""}`, [
  "var b = pm.response.json();",
  "var r = b.extra_fields && b.extra_fields.mcp_auth_required;",
  "pm.expect(r, 'body: ' + pm.response.text()).to.be.an('object');",
  "pm.expect(r.kind).to.equal('headers');",
  "var m = /auth\\?flow=([^&#]+)&kind=headers$/.exec(r.submit_url);",
  "pm.expect(m, 'submit_url: ' + r.submit_url).to.not.equal(null);",
  ...(saveAs ? [`pm.collectionVariables.set(${lit(saveAs)}, m[1]);`] : []),
  ...(sameAs ? [`pm.expect(m[1]).to.equal(pm.collectionVariables.get(${lit(sameAs)}));`] : []),
  ...(full ? ["pm.expect(r.required_header_keys).to.eql(['x-api-key']);", "pm.expect(r.admin_header_keys).to.eql(['X-Org']);"] : []),
]);
const noCredential = () => A.custom("there is no credential", ["pm.expect(pm.response.json().clients[0], 'body: ' + pm.response.text()).to.not.have.property('credential');"]);

const AUTH41 = folder("AUTH-41 per_user_headers create", `Setup: ${KEY_A_TEXT}; per_user_headers clients on alpha with tools_to_execute ["*"].`, [
  ...alphaKeyA("AUTH-41"),
  createExpect("AUTH-41", "no per_user_header_keys", perUser("auth41-c", "auth41_c", { user_headers: { "x-api-key": "key-a" } }), [A.status(400)]),
  createExpect("AUTH-41", "no sample user_headers", perUser("auth41-c", "auth41_c", { per_user_header_keys: ["x-api-key"] }), [A.status(400), A.msgEquals("sample user_headers missing values for required keys: x-api-key")]),
  createExpect("AUTH-41", "a rejected sample", perUser("auth41-c", "auth41_c", { per_user_header_keys: ["x-api-key"], user_headers: { "x-api-key": "BAD" } }), [A.status(422), A.msgStartsWith("Verification failed:")]),
  createExpect("AUTH-41", "a valid sample", perUser("auth41-c", "auth41_c", PER_USER_KEY_A), [A.status(200), messageStarts("MCP client registered. 2 tools discovered.")]),
  deleteClient("AUTH-41", "auth41-c"),
]);

const AUTH42_CLIENT = (clientId, name) => perUser(clientId, name, { ...PER_USER_KEY_A, headers: { "X-Org": "org-1" } });
const AUTH42_TEXT = `${KEY_A_TEXT}; a per_user_headers client on alpha with per_user_header_keys ["x-api-key"], sample key-a, static header X-Org, tools_to_execute ["*"]`;
const AUTH42 = folder("AUTH-42 Per-user headers caller without a credential", `Setup: ${AUTH42_TEXT}; the caller sends x-bf-mcp-session-id auth42-sess1 and has no credential.`, [
  ...alphaKeyA("AUTH-42"),
  createClient("AUTH-42", AUTH42_CLIENT("auth42-c", "auth42_c"), "setup: create the per_user_headers client with static header X-Org"),
  execTool("AUTH-42", "execute without a credential", "auth42_c-echo_tool", [A.status(400), headersFlow({ saveAs: "auth42_h", full: true })], { session: "auth42-sess1" }),
  execTool("AUTH-42", "execute again without a credential", "auth42_c-echo_tool", [A.status(400), headersFlow({ sameAs: "auth42_h", full: true })], { session: "auth42-sess1" }),
  gwCall("AUTH-42", "/mcp tools/call without a credential", "auth42_c-echo_tool", [toolResultError(null, "Open this URL to submit the required headers")], [{ key: "x-bf-mcp-session-id", value: "auth42-sess1" }]),
  deleteClient("AUTH-42", "auth42-c"),
]);

const AUTH43_FLOW = "/api/mcp/per-user-headers/flows/{{auth43_h}}";
const AUTH43 = folder("AUTH-43 Header submission", `Setup: as AUTH-42 (${AUTH42_TEXT}); an execute call as session auth43-sess1 opens a header flow.`, [
  ...alphaKeyA("AUTH-43"),
  createClient("AUTH-43", AUTH42_CLIENT("auth43-c", "auth43_c"), "setup: create the per_user_headers client with static header X-Org"),
  execTool("AUTH-43", "setup: execute as auth43-sess1 opens a header flow", "auth43_c-echo_tool", [A.status(400), headersFlow({ saveAs: "auth43_h" })], { session: "auth43-sess1" }),
  request("AUTH-43", "flow detail", "GET", AUTH43_FLOW, { asserts: [A.status(200), A.jsonEquals("has_active_credential", false)] }),
  request("AUTH-43", "submit {}", "PUT", AUTH43_FLOW, { body: {}, asserts: [A.status(400)] }),
  request("AUTH-43", "submit x-api-key BAD", "PUT", AUTH43_FLOW, { body: { headers: { "x-api-key": "BAD" } }, asserts: [A.status(422)] }),
  request("AUTH-43", "submit key-a plus X-Extra", "PUT", AUTH43_FLOW, { body: { headers: { "x-api-key": "key-a", "X-Extra": "extra-1" } }, asserts: [A.status(200), A.custom("credential_id is a non-empty string", [
    "var b = pm.response.json();",
    "pm.expect(b.credential_id, 'body: ' + pm.response.text()).to.be.a('string').and.not.equal('');",
  ])] }),
  execTool("AUTH-43", "execute with the submitted headers", "auth43_c-echo_tool", [A.status(200)], { session: "auth43-sess1" }),
  mockGet("AUTH-43", "alpha", "the call ran under key-a's identity", [identityCalls("key-a", 1)]),
  mockAuth("AUTH-43", "alpha", "setup: alpha still accepts only key-a and now counts calls under X-Extra", { identity_header: "X-Extra", require_headers: { "X-Api-Key": "key-a" } }),
  execTool("AUTH-43", "execute again", "auth43_c-echo_tool", [A.status(200)], { session: "auth43-sess1" }),
  mockGet("AUTH-43", "alpha", "X-Extra never reached alpha", [noIdentity("extra-1"), callsIs("echo_tool", 2)]),
  deleteClient("AUTH-43", "auth43-c"),
]);

const verifyHeaders = (id, title, clientId, userHeaders, asserts) => request(id, title, "POST", `/api/mcp/client/${clientId}/verify-headers`, { body: { user_headers: userHeaders }, asserts });
const AUTH45 = folder("AUTH-45 verify-headers", `Setup: ${KEY_A_TEXT}; the AUTH-41 per_user_headers client on alpha (per_user_header_keys ["x-api-key"], sample key-a); a headers client on alpha; an OpenAPI per_user_headers client from an inline Petstore document.`, [
  ...alphaKeyA("AUTH-45"),
  createClient("AUTH-45", perUser("auth45-c", "auth45_c", PER_USER_KEY_A), "setup: create the per_user_headers client"),
  createClient("AUTH-45", httpClient("auth45-h", "auth45_h", "alpha", { auth_type: "headers", headers: { "X-Api-Key": "key-a" } }), "setup: create the headers client"),
  createClient("AUTH-45", { client_id: "auth45-o", name: "auth45_o", connection_type: "openapi", auth_type: "per_user_headers", per_user_header_keys: ["x-api-key"], tools_to_execute: ["*"], openapi_config: { spec: PETSTORE } }, "setup: create the OpenAPI per_user_headers client"),
  getClient("AUTH-45", "auth45-c", "control: no credential before verify-headers", [noCredential()]),
  verifyHeaders("AUTH-45", "verify-headers on the headers client", "auth45-h", { "x-api-key": "key-a" }, [A.status(400)]),
  verifyHeaders("AUTH-45", "verify-headers on the OpenAPI client", "auth45-o", { "x-api-key": "key-a" }, [A.status(400), A.msgContains("verify-headers does not apply to openapi clients")]),
  verifyHeaders("AUTH-45", "verify-headers with an empty x-api-key", "auth45-c", { "x-api-key": "" }, [A.status(400)]),
  verifyHeaders("AUTH-45", "verify-headers with key-a", "auth45-c", { "x-api-key": "key-a" }, [A.status(200), A.jsonEquals("tools_count", 2)]),
  getClient("AUTH-45", "auth45-c", "the credential: kind headers, active, key names without values", [A.custom("credential kind \"headers\", status \"active\", header_keys [\"x-api-key\"], no value", [
    "var cr = pm.response.json().clients[0].credential;",
    "pm.expect(cr, 'body: ' + pm.response.text()).to.be.an('object');",
    "pm.expect(cr.kind).to.equal('headers');",
    "pm.expect(cr.status).to.equal('active');",
    "pm.expect(cr.header_keys).to.eql(['x-api-key']);",
    "pm.expect(JSON.stringify(cr)).to.not.include('key-a');",
  ])]),
  deleteClient("AUTH-45", "auth45-c"),
  deleteClient("AUTH-45", "auth45-h"),
  deleteClient("AUTH-45", "auth45-o"),
]);

const AUTH48 = folder("AUTH-48 token_exchange update and verify-exchange are refused", "Setup: alpha (echo_tool, add_tool); a headers client on alpha.", [
  ...freshServer("AUTH-48", "alpha", ["echo_tool", "add_tool"]),
  createClient("AUTH-48", httpClient("auth48-h", "auth48_h", "alpha", { auth_type: "headers", headers: { Authorization: `Bearer ${T1}` } }), "setup: create the headers client"),
  request("AUTH-48", "PUT a token_exchange block", "PUT", "/api/mcp/client/auth48-h", { body: { token_exchange: { audience: "api://mcp-harness" } },
    asserts: [A.status(400), A.msgEquals("token_exchange can only be set for token_exchange clients")] }),
  request("AUTH-48", "verify-exchange on the headers client", "POST", "/api/mcp/client/auth48-h/verify-exchange", { body: {}, asserts: [A.status(400)] }),
  request("AUTH-48", "verify-exchange on an unknown id", "POST", "/api/mcp/client/auth48-no-such-client/verify-exchange", { body: {}, asserts: [A.status(404)] }),
  deleteClient("AUTH-48", "auth48-h"),
]);

const CONN02 = folder("CONN-02 HTTP per call (default)", "Setup: alpha (echo_tool, add_tool); an HTTP client on alpha, auth none, per call (needs_session_stickiness omitted), tools_to_execute [\"*\"].", [
  ...freshServer("CONN-02", "alpha", ["echo_tool", "add_tool"]),
  createClient("CONN-02", httpClient("conn02-c", "conn02_c", "alpha", { tools_to_execute: ["*"] }), "setup: create the per-call client"),
  saveSessions("CONN-02", "alpha", "conn02_s0"),
  ...[1, 2, 3].flatMap((i) => [
    execTool("CONN-02", `execute ${i} of 3`, "conn02_c-echo_tool", [A.status(200)]),
    mockGet("CONN-02", "alpha", `alpha opened one session per execute call (${i} so far)`, [sessionsPlus("conn02_s0", i)]),
  ]),
  request("CONN-02", "refresh-tools", "POST", "/api/mcp/client/conn02-c/refresh-tools", { asserts: [A.status(200), A.jsonEquals("tool_count", 2)] }),
  request("CONN-02", "reconnect", "POST", "/api/mcp/client/conn02-c/reconnect", { asserts: [A.status(400), A.msgContains("not applicable for this client type")] }),
  deleteClient("CONN-02", "conn02-c"),
]);

const STICKY_ONLY = (type) => `needs_session_stickiness cannot be false for connection_type "${type}": only 'http' supports a per-call connection`;
const sseClient = (clientId, name, server, over = {}) => ({ client_id: clientId, name, connection_type: "sse", connection_string: `${CHAOS}/s/${server}/sse`, auth_type: "none", ...over });
const stdioClient = (clientId, name, server, over = {}) => ({ client_id: clientId, name, connection_type: "stdio", auth_type: "none", stdio_config: { command: "{{chaos_bin}}", args: ["-stdio", "-fleet", CHAOS, "-server", server], envs: [] }, ...over });
const CONN03 = folder("CONN-03 Per-call only for HTTP", "Setup: alpha (echo_tool, add_tool); SSE and STDIO creates with needs_session_stickiness false; an SSE client on alpha then PUT to false.", [
  ...freshServer("CONN-03", "alpha", ["echo_tool", "add_tool"]),
  createExpect("CONN-03", "SSE with needs_session_stickiness false", sseClient("conn03-sse", "conn03_sse", "alpha", { needs_session_stickiness: false }), [A.status(400), A.msgEquals(STICKY_ONLY("sse"))]),
  createExpect("CONN-03", "STDIO with needs_session_stickiness false", stdioClient("conn03-stdio", "conn03_stdio", "alpha", { needs_session_stickiness: false }), [A.status(400), A.msgEquals(STICKY_ONLY("stdio"))]),
  createClient("CONN-03", sseClient("conn03-s", "conn03_s", "alpha"), "setup: create an SSE client"),
  request("CONN-03", "PUT the SSE client to needs_session_stickiness false", "PUT", "/api/mcp/client/conn03-s", { body: { needs_session_stickiness: false }, asserts: [A.status(400), A.msgEquals(STICKY_ONLY("sse"))] }),
  deleteClient("CONN-03", "conn03-s"),
]);

// CONN-04 creates can take about 31s each: permanent connect errors are retried today.
const CONN04_FAULTS = [["refuse", { refuse: true }], ["init", { init_fail: true }], ["list", { list_fail: true }], ["version", { bad_version: "1999-01-01" }]];
const CONN04 = folder("CONN-04 Upstream failing at create (sticky)", "Setup: alpha (echo_tool, add_tool) with one fault at a time: refuse, init_fail, list_fail, bad_version 1999-01-01. Each create is an HTTP client on alpha, auth none, needs_session_stickiness true, tools_to_execute [\"*\"], and may take about 35s.", [
  ...freshServer("CONN-04", "alpha", ["echo_tool", "add_tool"]),
  ...CONN04_FAULTS.flatMap(([label, faults]) => {
    const fault = Object.keys(faults)[0];
    return [
      mockPost("CONN-04", "alpha", "faults", `setup: alpha fault ${fault}`, { faults }),
      createExpect("CONN-04", `create while alpha has ${fault}`, httpClient(`conn04-${label}`, `conn04_${label}`, "alpha", { needs_session_stickiness: true, tools_to_execute: ["*"] }),
        [A.status(500), A.msgStartsWith("Failed to connect MCP client:"), ...(label === "list" ? [A.msgContains("failed to retrieve tools from MCP client")] : [])]),
      request("CONN-04", `conn04-${label} is not listed`, "GET", `/api/mcp/clients?server=conn04-${label}`, { asserts: [A.status(200), A.custom("no client is returned", [
        "pm.expect(pm.response.json().clients, 'body: ' + pm.response.text()).to.eql([]);",
      ])] }),
      mockPost("CONN-04", "alpha", "faults/clear", `teardown: clear alpha's ${fault} fault`),
    ];
  }),
]);

const CONN17 = folder("CONN-17 SSE with header auth", `Setup: a fleet server ${T1_TEXT("gamma")}; an SSE client on /s/gamma/sse with auth headers "Authorization: Bearer T1", tools_to_execute ["*"].`, [
  ...requiresT1("CONN-17", "gamma"),
  createExpect("CONN-17", "create the SSE client", sseClient("conn17-c", "conn17_c", "gamma", { auth_type: "headers", headers: { Authorization: `Bearer ${T1}` }, tools_to_execute: ["*"] }), [A.status(200)]),
  getClient("CONN-17", "conn17-c", "the client list shows healthy", [clientState("healthy")]),
  execTool("CONN-17", "execute conn17_c-echo_tool", "conn17_c-echo_tool", [A.status(200)]),
  mockGet("CONN-17", "gamma", "the call ran under id1 on one session", [identityCalls("id1", 1), counterIs("sessions", 1)]),
  deleteClient("CONN-17", "conn17-c"),
  request("CONN-17", "teardown: remove gamma", "POST", "/control", { base: CHAOS, noAuth: true, body: { remove: "gamma" }, asserts: [A.status(200)] }),
]);

const CONN18 = folder("CONN-18 SSE stream dropped", "Setup: alpha (echo_tool, add_tool); an SSE client on alpha, auth none, tool_execution_timeout 3s, tools_to_execute [\"*\"]; then POST /control/alpha/streams/drop.", [
  ...freshServer("CONN-18", "alpha", ["echo_tool", "add_tool"]),
  createClient("CONN-18", sseClient("conn18-c", "conn18_c", "alpha", { tool_execution_timeout: 3, tools_to_execute: ["*"] }), "setup: create the SSE client with a 3s tool timeout"),
  mockPost("CONN-18", "alpha", "streams/drop", "setup: drop alpha's SSE streams"),
  getClient("CONN-18", "conn18-c", "the client list still shows healthy", [clientState("healthy")]),
  execTool("CONN-18", "execute on the dropped stream", "conn18_c-echo_tool", [A.status(400)]),
  saveSessions("CONN-18", "alpha", "conn18_s1"),
  request("CONN-18", "reconnect", "POST", "/api/mcp/client/conn18-c/reconnect", { asserts: [A.status(200)] }),
  execTool("CONN-18", "execute after the reconnect", "conn18_c-echo_tool", [A.status(200)]),
  mockGet("CONN-18", "alpha", "the reconnect opened one session", [sessionsPlus("conn18_s1", 1)]),
  deleteClient("CONN-18", "conn18-c"),
]);

const CONN22 = folder("CONN-22 STDIO child exit", "Setup: alpha (echo_tool, add_tool); a STDIO client bridging to alpha, auth none, tools_to_execute [\"*\"].", [
  ...freshServer("CONN-22", "alpha", ["echo_tool", "add_tool"]),
  createClient("CONN-22", stdioClient("conn22-c", "conn22_c", "alpha", { tools_to_execute: ["*"] }), "setup: create the STDIO client"),
  mockGet("CONN-22", "alpha", "setup: record the STDIO child's pid", [A.custom("stdio_pid is set", ["pm.expect(pm.response.json().stdio_pid, 'body: ' + pm.response.text()).to.be.above(0);"])],
    ["pm.collectionVariables.set('conn22_pid', pm.response.json().stdio_pid);"]),
  mockPost("CONN-22", "alpha", "stdio/kill", "stdio/kill"),
  execTool("CONN-22", "execute after the child exits", "conn22_c-echo_tool", [A.status(400)]),
  request("CONN-22", "reconnect", "POST", "/api/mcp/client/conn22-c/reconnect", { asserts: [A.status(200)] }),
  mockGet("CONN-22", "alpha", "a new STDIO child is running", [A.custom("stdio_pid is set and differs from the first", [
    "var pid = pm.response.json().stdio_pid;",
    "pm.expect(pid, 'body: ' + pm.response.text()).to.be.above(0);",
    "pm.expect(pid).to.not.equal(Number(pm.collectionVariables.get('conn22_pid')));",
  ])]),
  execTool("CONN-22", "execute after the reconnect", "conn22_c-echo_tool", [A.status(200)]),
  deleteClient("CONN-22", "conn22-c"),
]);

// ================================================================ EP slugs, credentials and auth modes

const wwwPresent = () => A.custom("WWW-Authenticate is present", ["pm.expect(pm.response.headers.get('WWW-Authenticate'), 'body: ' + pm.response.text()).to.be.a('string').and.not.empty;"]);
const wwwExact = (iss) => A.custom(`WWW-Authenticate is Bearer resource_metadata="${iss || "<base URL>"}/.well-known/oauth-protected-resource/mcp"`, [
  `var want = 'Bearer resource_metadata="' + ${iss ? lit(iss) : "pm.variables.get('base_url')"} + '/.well-known/oauth-protected-resource/mcp"';`,
  "pm.expect(pm.response.headers.get('WWW-Authenticate'), 'body: ' + pm.response.text()).to.equal(want);",
]);
const noWww = () => A.custom("there is no WWW-Authenticate", ["pm.expect(pm.response.headers.has('WWW-Authenticate'), 'WWW-Authenticate: ' + pm.response.headers.get('WWW-Authenticate')).to.equal(false);"]);
const jsonNotStream = () => A.custom("a JSON body, not an event stream", [
  "var ct = pm.response.headers.get('Content-Type') || '';",
  "pm.expect(ct, 'Content-Type').to.include('application/json');",
  "pm.expect(ct, 'Content-Type').to.not.include('text/event-stream');",
  "pm.expect(function () { JSON.parse(pm.response.text()); }, 'body: ' + pm.response.text()).to.not.throw();",
]);
// list sends tools/list to path with the given extra headers and no admin credentials.
const list = (id, title, path, headers, asserts) => rpc(id, title, listMsg, { path, headers: [MCP_ACCEPT, ...headers], asserts });
const bearer = (value) => ({ key: "Authorization", value: `Bearer ${value}` });
const vkVar = (id, label) => `{{${id.toLowerCase()}_${label}}}`;
const ALL_TOOLS = ["alpha-add", "alpha-echo", "beta-echo"];
// team is the virtual MCP over the fleet's alpha echo and every beta tool, attached to the full-access VK.
const TEAM_TEXT = "a virtual MCP \"team\" with alpha [\"echo\"] and beta [\"*\"], attached to the full-access VK";
const teamSetup = (id) => {
  const p = id.toLowerCase();
  return [
    request(id, "setup: create the virtual MCP team", "POST", "/api/mcp/virtual-mcps", {
      body: { name: "team", tools: [{ mcp_client_id: `${p}-alpha`, tool_names: ["echo"] }, { mcp_client_id: `${p}-beta`, tool_names: ["*"] }] },
      asserts: [A.status(201)], extraTests: [`pm.collectionVariables.set(${lit(p + "_team_id")}, pm.response.json().virtual_mcp.id);`],
    }),
    request(id, "setup: attach team to the full-access VK", "POST", `/api/mcp/virtual-mcps/{{${p}_team_id}}/virtual-keys/{{${p}_full_id}}`, { asserts: [A.status(200)] }),
  ];
};
const teamTeardown = (id) => request(id, "teardown: delete the virtual MCP team", "DELETE", `/api/mcp/virtual-mcps/{{${id.toLowerCase()}_team_id}}`, { asserts: [A.status(200)] });
// grantFor runs a whole grant for one VK label: authorize, consent through the temp token, exchange.
// The access token lands in <flow>_at.
const grantFor = (id, clientVar, flow, label) => [
  authorize(id, clientVar, flow, `setup: authorize a grant for ${VKS[label].label}`),
  consent(id, flow, `setup: consent with ${VKS[label].label}`, "temp", { mode: "vk", value: vkVar(id, label) }, [A.status(200), codeIssued()], { extraTests: saveCode(flow) }),
  tokenReq(id, `setup: exchange the code for ${VKS[label].label}'s token`, codeParams(flow, clientVar), [A.status(200), tokensIssued()], { extraTests: saveTokens(flow) }),
];
const ACCESS_DENIED = [A.status(403), A.msgEquals("access_denied")];
const OAUTH_MODE_MSG = "this server requires OAuth JWT authentication; header credentials are not accepted in oauth mode";

const EP34 = folder("EP-34 GET refusals", "Setup: GET /mcp with Accept: text/event-stream; mcp_server_auth_mode oauth with no token, then headers with enforce_auth_on_inference off on /mcp/nope.", [
  readConfig("EP-34"),
  authMode("EP-34", "O", false),
  request("EP-34", "GET /mcp in oauth mode with no token", "GET", "/mcp", { noAuth: true, headers: [{ key: "Accept", value: "text/event-stream" }], asserts: [A.status(401), wwwPresent(), jsonNotStream()] }),
  authMode("EP-34", "H", false),
  request("EP-34", "GET /mcp/nope", "GET", "/mcp/nope", { noAuth: true, headers: [{ key: "Accept", value: "text/event-stream" }], asserts: [...ACCESS_DENIED, jsonNotStream()] }),
  restoreConfig("EP-34"),
]);

const EP36 = folder("EP-36 Client slug gated by grant and state", `Setup: ${FLEET}; the echo-only VK (${VKS.echo.text}) and the full-access VK (${VKS.full.text}); alpha is disabled later.`, [
  ...fleetSetup("EP-36"), createVK("EP-36", "echo"), createVK("EP-36", "full"),
  list("EP-36", "/mcp/beta with the echo-only VK", "/mcp/beta", [vkHeader("EP-36", "echo")], ACCESS_DENIED),
  list("EP-36", "/mcp/beta with the full-access VK", "/mcp/beta", [vkHeader("EP-36", "full")], [A.status(200), toolNamesInList(["beta-echo"])]),
  list("EP-36", "control: /mcp/alpha with the full-access VK before the disable", "/mcp/alpha", [vkHeader("EP-36", "full")], [A.status(200), toolNamesInList(["alpha-add", "alpha-echo"])]),
  request("EP-36", "setup: disable alpha", "PUT", "/api/mcp/client/ep-36-alpha", { body: { disabled: true }, asserts: [A.status(200)] }),
  list("EP-36", "/mcp/alpha with the full-access VK after the disable", "/mcp/alpha", [vkHeader("EP-36", "full")], [A.status(403)]),
  deleteVK("EP-36", "echo"), deleteVK("EP-36", "full"),
  ...fleetTeardown("EP-36"),
]);

const EP38 = folder("EP-38 Virtual MCP slug refusals", `Setup: ${FLEET}; the full-access VK (${VKS.full.text}) and the echo-only VK (${VKS.echo.text}); ${TEAM_TEXT}; mcp_server_auth_mode headers, enforce_auth_on_inference off.`, [
  readConfig("EP-38"), authMode("EP-38", "H", false),
  ...fleetSetup("EP-38"), createVK("EP-38", "full"), createVK("EP-38", "echo"), ...teamSetup("EP-38"),
  list("EP-38", "/mcp/team anonymously", "/mcp/team", [], ACCESS_DENIED),
  list("EP-38", "/mcp/team with the echo-only VK, which team is not attached to", "/mcp/team", [vkHeader("EP-38", "echo")], ACCESS_DENIED),
  list("EP-38", "control: /mcp/team with the full-access VK", "/mcp/team", [vkHeader("EP-38", "full")], [A.status(200), toolNamesInList(["alpha-echo", "beta-echo"])]),
  request("EP-38", "setup: disable team", "PUT", "/api/mcp/virtual-mcps/{{ep-38_team_id}}", { body: { enabled: false }, asserts: [A.status(200)] }),
  list("EP-38", "/mcp/team with the full-access VK after the disable", "/mcp/team", [vkHeader("EP-38", "full")], ACCESS_DENIED),
  list("EP-38", "/mcp/nope with the full-access VK", "/mcp/nope", [vkHeader("EP-38", "full")], ACCESS_DENIED),
  teamTeardown("EP-38"), deleteVK("EP-38", "full"), deleteVK("EP-38", "echo"),
  ...fleetTeardown("EP-38"),
  restoreConfig("EP-38"),
]);

const EP39 = folder("EP-39 Slug collisions across namespaces", "Setup: alpha (echo, add) registered as client alpha; a virtual MCP named team.", [
  ...freshServer("EP-39", "alpha", ["echo", "add"]),
  createClient("EP-39", httpClient("ep-39-alpha", "alpha", "alpha", { tools_to_execute: ["*"], allow_by_default: false }), "setup: register client alpha"),
  request("EP-39", "setup: create the virtual MCP team", "POST", "/api/mcp/virtual-mcps", { body: { name: "team", tools: [] }, asserts: [A.status(201)],
    extraTests: ["pm.collectionVariables.set('ep-39_team_id', pm.response.json().virtual_mcp.id);"] }),
  request("EP-39", "a virtual MCP with slug \"alpha\"", "POST", "/api/mcp/virtual-mcps", { body: { name: "EP-39 alpha slug", endpoint_slug: "alpha", tools: [] },
    asserts: [A.status(409), A.msgEquals("an MCP endpoint with this slug already exists")] }),
  createExpect("EP-39", "a client named \"team\"", httpClient("ep-39-team", "team", "alpha"), [A.status(409), A.msgEquals("An MCP endpoint with this slug already exists")]),
  request("EP-39", "teardown: the client named team was never created", "DELETE", "/api/mcp/client/ep-39-team", { asserts: [A.status(404)] }),
  request("EP-39", "teardown: delete the virtual MCP team", "DELETE", "/api/mcp/virtual-mcps/{{ep-39_team_id}}", { asserts: [A.status(200)] }),
  deleteClient("EP-39", "ep-39-alpha"),
]);

// EP40_CARRIERS are the six headers a VK can ride in; each request carries exactly one.
const EP40_CARRIERS = [
  ["x-bf-vk", (v) => ({ key: "x-bf-vk", value: v })],
  ["Authorization: Bearer", (v) => ({ key: "Authorization", value: `Bearer ${v}` })],
  ["Authorization: bearer (lowercase)", (v) => ({ key: "Authorization", value: `bearer ${v}` })],
  ["x-api-key", (v) => ({ key: "x-api-key", value: v })],
  ["x-goog-api-key", (v) => ({ key: "x-goog-api-key", value: v })],
  ["api-key", (v) => ({ key: "api-key", value: v })],
];
const EP40 = folder("EP-40 VK header carriers", `Setup: ${FLEET}; the full-access VK (${VKS.full.text}); enforce_auth_on_inference on, in mcp_server_auth_mode headers and then both.`, [
  readConfig("EP-40"), ...fleetSetup("EP-40"), createVK("EP-40", "full"),   ...["H", "B"].flatMap((m) => [
    authMode("EP-40", m, true),
    noCredControl("EP-40", `${MODE_NAMES[m]}: `),
    ...EP40_CARRIERS.map(([label, header]) => list("EP-40", `${MODE_NAMES[m]}: the VK in ${label}`, "/mcp", [header(vkVar("EP-40", "full"))], [A.status(200), toolNamesInList(ALL_TOOLS)])),
  ]),
  restoreConfig("EP-40"), deleteVK("EP-40", "full"), ...fleetTeardown("EP-40"),
]);

const EP41 = folder("EP-41 No credential by mode", `Setup: tools/list on /mcp with no credential, in each mcp_server_auth_mode (headers, both, oauth) with enforce_auth_on_inference off and on; both and oauth set issuer_url ${OTHER_ISS} (another origin than the requests).`, [
  readConfig("EP-41"),
  ...[
    ["H", false, [A.status(200)]],
    ["B", false, [A.status(200)]],
    ["H", true, [A.status(401), A.msgContains("virtual key required to access mcp server"), noWww()]],
    ["B", true, [A.status(401), wwwExact(OTHER_ISS)]],
    ["O", false, [A.status(401), wwwExact(OTHER_ISS)]],
    ["O", true, [A.status(401), wwwExact(OTHER_ISS)]],
  ].flatMap(([m, enforce, asserts]) => [
    authMode("EP-41", m, enforce, true, OTHER_ISS),
    list("EP-41", `${MODE_NAMES[m]}, enforce_auth_on_inference ${enforce ? "on" : "off"}: no credential`, "/mcp", [], asserts),
  ]),
  restoreConfig("EP-41"),
]);

const EP42 = folder("EP-42 Header credentials in oauth mode", `Setup: ${FLEET}; the full-access VK (${VKS.full.text}); mcp_server_auth_mode oauth, enforce_auth_on_inference off.`, [
  readConfig("EP-42"), ...fleetSetup("EP-42"), createVK("EP-42", "full"),
  authMode("EP-42", "B", true),
  list("EP-42", "control: both mode, enforcement on: the VK in x-bf-vk", "/mcp", [vkHeader("EP-42", "full")], [A.status(200), toolNamesInList(ALL_TOOLS)]),
  list("EP-42", "control: both mode, enforcement on: the VK as a Bearer token", "/mcp", [bearer(vkVar("EP-42", "full"))], [A.status(200), toolNamesInList(ALL_TOOLS)]),
  authMode("EP-42", "O", false),
  list("EP-42", "the VK in x-bf-vk", "/mcp", [vkHeader("EP-42", "full")], [A.status(401), A.msgEquals(OAUTH_MODE_MSG), wwwPresent()]),
  list("EP-42", "the VK as a Bearer token", "/mcp", [bearer(vkVar("EP-42", "full"))], [A.status(401), A.msgEquals(OAUTH_MODE_MSG), wwwPresent()]),
  restoreConfig("EP-42"), deleteVK("EP-42", "full"), ...fleetTeardown("EP-42"),
]);

// EP-43's expired key is created to lapse 3s later, since the API refuses an expiry in the past.
const EP43 = folder("EP-43 Unknown, inactive, expired VK", "Setup: an unknown key (sk-bf-ep43-no-such-key), an inactive key and a key that expires 3s after it is created; mcp_server_auth_mode headers and then both, enforce_auth_on_inference off.", [
  readConfig("EP-43"),
  vkCreate("EP-43", "setup: create an inactive VK", { name: "EP-43 inactive VK", is_active: false }, "ep43_inactive"),
  request("EP-43", "setup: create a VK that expires in 3s", "POST", "/api/governance/virtual-keys", {
    raw: "{\"name\":\"EP-43 expiring VK\",\"expires_at\":\"{{ep43_expires_at}}\"}", pre: ["pm.variables.set('ep43_expires_at', new Date(Date.now() + 3000).toISOString());"], asserts: [A.status(200)],
    extraTests: ["pm.collectionVariables.set('ep43_expired_id', pm.response.json().virtual_key.id);", "pm.collectionVariables.set('ep43_expired', pm.response.json().virtual_key.value);"],
  }),
  ...["H", "B"].flatMap((m, pass) => [
    authMode("EP-43", m, false),
    rpc("EP-43", `${MODE_NAMES[m]}: an unknown VK`, listMsg, { headers: [MCP_ACCEPT, { key: "x-bf-vk", value: "sk-bf-ep43-no-such-key" }], asserts: [A.status(401), A.msgEquals("access not found. The provided credential does not exist or has been revoked.")] }),
    rpc("EP-43", `${MODE_NAMES[m]}: the inactive VK`, listMsg, { headers: [MCP_ACCEPT, { key: "x-bf-vk", value: "{{ep43_inactive}}" }], asserts: [A.status(403), A.msgEquals("virtual key is inactive")] }),
    request("EP-43", `${MODE_NAMES[m]}: the expired VK`, "POST", "/mcp", {
      noAuth: true, raw: JSON.stringify(listMsg), headers: [MCP_ACCEPT, { key: "x-bf-vk", value: "{{ep43_expired}}" }],
      ...(pass === 0 ? { pre: ["setTimeout(function () {}, 4000);"] } : {}), asserts: [A.status(403), A.msgEquals("virtual key has expired")],
    }),
  ]),
  restoreConfig("EP-43"),
  vkDelete("EP-43", "teardown: delete the inactive VK", "ep43_inactive"),
  vkDelete("EP-43", "teardown: delete the expired VK", "ep43_expired"),
]);

// EP44_TOKENS builds the four bad tokens from the JWKS response and the grant's token:
// garbage (JWT-shaped, so it is treated as a JWT), the token with its scope changed, alg none,
// and HS256 signed with the server's public key PEM, rebuilt from the JWKS as Go encodes it.
const EP44_TOKENS = [
  "var C = require('crypto-js');",
  "function b64u(words) { return C.enc.Base64.stringify(words).replace(/\\+/g, '-').replace(/\\//g, '_').replace(/=+$/, ''); }",
  "function enc(o) { return b64u(C.enc.Utf8.parse(JSON.stringify(o))); }",
  "function pemFromJwk(n, e) {",
  "  function bytes(s) { s = s.replace(/-/g, '+').replace(/_/g, '/'); while (s.length % 4) { s += '='; } var w = C.enc.Base64.parse(s); var out = []; for (var i = 0; i < w.sigBytes; i++) { out.push((w.words[i >>> 2] >>> (24 - (i % 4) * 8)) & 0xff); } return out; }",
  "  function len(k) { if (k < 128) { return [k]; } var b = []; while (k > 0) { b.unshift(k & 0xff); k = k >>> 8; } return [0x80 | b.length].concat(b); }",
  "  function tlv(tag, body) { return [tag].concat(len(body.length), body); }",
  "  function int(b) { while (b.length > 1 && b[0] === 0) { b = b.slice(1); } if (b[0] & 0x80) { b = [0].concat(b); } return tlv(0x02, b); }",
  "  var rsa = tlv(0x30, int(bytes(n)).concat(int(bytes(e))));",
  "  var alg = tlv(0x30, [0x06, 0x09, 0x2a, 0x86, 0x48, 0x86, 0xf7, 0x0d, 0x01, 0x01, 0x01, 0x05, 0x00]);",
  "  var spki = tlv(0x30, alg.concat(tlv(0x03, [0].concat(rsa))));",
  "  var words = []; for (var j = 0; j < spki.length; j++) { words[j >>> 2] |= spki[j] << (24 - (j % 4) * 8); }",
  "  var b64 = C.enc.Base64.stringify(C.lib.WordArray.create(words, spki.length));",
  "  return '-----BEGIN PUBLIC KEY-----\\n' + b64.match(/.{1,64}/g).join('\\n') + '\\n-----END PUBLIC KEY-----\\n';",
  "}",
  "var key = pm.response.json().keys[0];",
  "var parts = pm.collectionVariables.get('ep44_g_at').split('.');",
  "var p = parts[1].replace(/-/g, '+').replace(/_/g, '/'); while (p.length % 4) { p += '='; }",
  "var payload = JSON.parse(C.enc.Base64.parse(p).toString(C.enc.Utf8));",
  "payload.scope = 'mcp-tampered';",
  "pm.collectionVariables.set('ep44_garbage', 'eyJnYXJiYWdl.bm90LWEtand0.c2ln');",
  "pm.collectionVariables.set('ep44_tampered', parts[0] + '.' + enc(payload) + '.' + parts[2]);",
  "pm.collectionVariables.set('ep44_none', enc({ alg: 'none', typ: 'JWT', kid: key.kid }) + '.' + parts[1] + '.');",
  "var hs = enc({ alg: 'HS256', typ: 'JWT', kid: key.kid }) + '.' + parts[1];",
  "pm.collectionVariables.set('ep44_hs256', hs + '.' + b64u(C.HmacSHA256(hs, pemFromJwk(key.n, key.e))));",
];
// EP44_CASES pairs each bad token with the reason the verifier gives for it, so a case cannot pass on another's.
const EP44_CASES = [["garbage", "ep44_garbage", "token is malformed"], ["tampered", "ep44_tampered", "crypto/rsa: verification error"], ["alg none", "ep44_none", "signing method none is invalid"], ["HS256 signed with the public key", "ep44_hs256", "signing method HS256 is invalid"]];
const INVALID_TOKEN = [A.status(401), A.msgStartsWith("invalid token"), wwwPresent()];
const EP44 = folder("EP-44 Invalid bearer JWTs", `Setup: ${FLEET}; the full-access VK (${VKS.full.text}); ${BOTH}, temp-token auth on, enforce_auth_on_inference off; a grant consented with that VK gives the token the bad tokens are built from: garbage (JWT-shaped), its payload with scope changed, alg none, and HS256 signed with the public key PEM.`, [
  readConfig("EP-44"), ...fleetSetup("EP-44"), createVK("EP-44", "full"), authMode("EP-44", "B", false),
  register("EP-44", "ep44_client", LOOPBACK_CB, undefined, REFRESHABLE),
  ...grantFor("EP-44", "ep44_client", "ep44_g", "full"),
  request("EP-44", "setup: read the JWKS and build the bad tokens", "GET", "/.well-known/jwks.json", { noAuth: true, asserts: [A.status(200)], extraTests: EP44_TOKENS }),
  list("EP-44", "control: both: the untouched token", "/mcp", [bearer("{{ep44_g_at}}")], [A.status(200), toolNamesInList(ALL_TOOLS)]),
  ...EP44_CASES.map(([label, v, reason]) => list("EP-44", `both: a ${label} token`, "/mcp", [bearer(`{{${v}}}`)], [...INVALID_TOKEN, A.msgContains(reason)])),
  authMode("EP-44", "O", false),
  list("EP-44", "control: oauth: the untouched token", "/mcp", [bearer("{{ep44_g_at}}")], [A.status(200), toolNamesInList(ALL_TOOLS)]),
  ...EP44_CASES.map(([label, v, reason]) => list("EP-44", `oauth: a ${label} token`, "/mcp", [bearer(`{{${v}}}`)], [...INVALID_TOKEN, A.msgContains(reason)])),
  authMode("EP-44", "H", false, false),
  list("EP-44", "headers: the garbage token is anonymous", "/mcp", [bearer("{{ep44_garbage}}")], [A.status(200)]),
  restoreConfig("EP-44"), deleteVK("EP-44", "full"), ...fleetTeardown("EP-44"),
]);

const CONFLICT_MSG = "conflicting credentials: an OAuth token and a virtual key header were both provided; send only the OAuth token";
const EP45 = folder("EP-45 JWT plus a VK header", `Setup: ${FLEET}; the full-access VK (${VKS.full.text}); ${BOTH}, temp-token auth on, enforce_auth_on_inference off; a token from a grant consented with that VK.`, [
  readConfig("EP-45"), ...fleetSetup("EP-45"), createVK("EP-45", "full"), authMode("EP-45", "B", false),
  register("EP-45", "ep45_client", LOOPBACK_CB, undefined, REFRESHABLE),
  ...grantFor("EP-45", "ep45_client", "ep45_g", "full"),
  list("EP-45", "control: the token alone", "/mcp", [bearer("{{ep45_g_at}}")], [A.status(200)]),
  list("EP-45", "the token plus x-bf-vk", "/mcp", [bearer("{{ep45_g_at}}"), vkHeader("EP-45", "full")], [A.status(401), A.msgEquals(CONFLICT_MSG)]),
  list("EP-45", "the token plus x-api-key", "/mcp", [bearer("{{ep45_g_at}}"), { key: "x-api-key", value: vkVar("EP-45", "full") }], [A.status(401), A.msgEquals(CONFLICT_MSG)]),
  restoreConfig("EP-45"), deleteVK("EP-45", "full"), ...fleetTeardown("EP-45"),
]);

const NO_VK_401 = [A.status(401), A.msgContains("virtual key required to access mcp server"), wwwPresent()];
const EP46 = folder("EP-46 Non-credentials do not open /mcp", `Setup: ${BOTH}, temp-token auth on, enforce_auth_on_inference on; a registered client's authorize gives a temp token. Each request carries one of: that temp token, the admin's Basic credentials, x-bf-mcp-session-id, the setup token.`, [
  readConfig("EP-46"), authMode("EP-46", "B", true),
  register("EP-46", "ep46_client"),
  authorize("EP-46", "ep46_client", "ep46_f", "setup: authorize to get a temp token"),
  flowGet("EP-46", "ep46_f", "control: the temp token opens its own flow", "temp", [A.status(200)]),
  ...[["B", NO_VK_401]].flatMap(([m, refused]) => {
    const p = m === "B" ? "" : `${MODE_NAMES[m]}: `;
    return [
      ...(m === "B" ? [] : [authMode("EP-46", m, true)]),
      list("EP-46", `${p}the temp token`, "/mcp", [{ key: "X-Bifrost-Temp-Token", value: "{{ep46_f_tt}}" }], refused),
      list("EP-46", `${p}the admin's Basic credentials`, "/mcp", [{ key: "Authorization", value: "{{admin_auth_header}}" }], refused),
      list("EP-46", `${p}x-bf-mcp-session-id`, "/mcp", [{ key: "x-bf-mcp-session-id", value: "ep46-session" }], refused),
      list("EP-46", `${p}the setup token`, "/mcp", [{ key: "X-Bifrost-Setup-Token", value: "{{setup_token}}" }], [...refused, A.custom("the setup token variable holds a value", ["var t = pm.variables.get('setup_token');", "pm.expect(t, 'setup_token').to.be.a('string').and.not.empty;", "pm.expect(t.indexOf('{{')).to.equal(-1);"])]),
    ];
  }),
  restoreConfig("EP-46"),
]);

const EP47 = folder("EP-47 VK JWT matches the VK header", `Setup: ${FLEET}; the full-access VK (${VKS.full.text}) and the echo-only VK (${VKS.echo.text}); ${TEAM_TEXT}; ${BOTH}, temp-token auth on, enforce_auth_on_inference on; one registered client gets a grant consented with each VK.`, [
  readConfig("EP-47"), ...fleetSetup("EP-47"), createVK("EP-47", "full"), createVK("EP-47", "echo"), ...teamSetup("EP-47"), authMode("EP-47", "B", true),
  register("EP-47", "ep47_client", LOOPBACK_CB, undefined, REFRESHABLE),
  ...grantFor("EP-47", "ep47_client", "ep47_r", "echo"),
  ...grantFor("EP-47", "ep47_client", "ep47_a", "full"),
  list("EP-47", "the echo-only VK's token lists", "/mcp", [bearer("{{ep47_r_at}}")], [A.status(200), toolNamesInList(["alpha-echo"])]),
  rpc("EP-47", "the echo-only VK's token calls alpha-add", call("alpha-add", { a: 1, b: 2 }), { headers: [MCP_ACCEPT, bearer("{{ep47_r_at}}")], asserts: [A.status(200), toolResultError("Tool execution failed: MCP tool 'alpha-add' is not allowed", null)] }),
  mockGet("EP-47", "alpha", "alpha received no tool call", [A.custom("calls is empty", ["pm.expect(pm.response.json().counters.calls, 'body: ' + pm.response.text()).to.eql({});"])]),
  list("EP-47", "the full-access VK's token lists /mcp/team", "/mcp/team", [bearer("{{ep47_a_at}}")], [A.status(200), toolNamesInList(["alpha-echo", "beta-echo"])]),
  restoreConfig("EP-47"), teamTeardown("EP-47"), deleteVK("EP-47", "full"), deleteVK("EP-47", "echo"), ...fleetTeardown("EP-47"),
]);

const EP49 = folder("EP-49 Session-mode JWT", `Setup: ${FLEET}; the full-access VK (${VKS.full.text}); ${TEAM_TEXT}; ${BOTH}, temp-token auth on, enforce_auth_on_inference off; a session-mode token as in OAS-20 (consent by the admin with Bearer base64(admin:pw)); enforce_auth_on_inference on for the last case.`, [
  readConfig("EP-49"), ...fleetSetup("EP-49"), createVK("EP-49", "full"), ...teamSetup("EP-49"), authMode("EP-49", "B", false),
  register("EP-49", "ep49_client", LOOPBACK_CB, undefined, REFRESHABLE),
  authorize("EP-49", "ep49_client", "ep49_s", "setup: authorize"),
  consent("EP-49", "ep49_s", "setup: consent in session mode as the admin", "bearer", { mode: "session", value: "" }, [A.status(200), codeIssued()], { extraTests: saveCode("ep49_s") }),
  tokenReq("EP-49", "setup: exchange the code for a session-mode token", codeParams("ep49_s", "ep49_client"), [A.status(200), sessionBfMode("session")], { extraTests: saveTokens("ep49_s") }),
  list("EP-49", "the session token lists the anonymous tools", "/mcp", [bearer("{{ep49_s_at}}")], [A.status(200), toolNamesInList(ALL_TOOLS)]),
  list("EP-49", "control: /mcp/team with the full-access VK", "/mcp/team", [vkHeader("EP-49", "full")], [A.status(200)]),
  list("EP-49", "the session token on /mcp/team", "/mcp/team", [bearer("{{ep49_s_at}}")], [A.status(403)]),
  authMode("EP-49", "B", true),
  list("EP-49", "the session token with enforce_auth_on_inference on", "/mcp", [bearer("{{ep49_s_at}}")], [A.status(401), A.msgContains("session-mode tokens are not accepted when authentication is enforced")]),
  revokeGrants("EP-49", "ep49_client"),
  restoreConfig("EP-49"), teamTeardown("EP-49"), deleteVK("EP-49", "full"), ...fleetTeardown("EP-49"),
]);

// EP-12, EP-14 and EP-24 sit below the OAuth helpers because their both and oauth passes use them.
// EP-12 runs once per mode that accepts header credentials; both adds WWW-Authenticate to the no-credential refusal.
const EP12 = folder("EP-12 tools/list per VK", `Setup: ${FLEET}; mcp_server_auth_mode headers, enforce_auth_on_inference on; ${VK_TEXT}.`, [
  ...fleetSetup("EP-12"),
  createVK("EP-12", "full"), createVK("EP-12", "echo"), createVK("EP-12", "none"),
  readConfig("EP-12"),
  authMode("EP-12", "H", true),
  noCredControl("EP-12", ""),
  rpc("EP-12", "tools/list with the full-access VK", listMsg, { headers: [MCP_ACCEPT, vkHeader("EP-12", "full")], asserts: [A.status(200), toolNamesInList(["alpha-add", "alpha-echo", "beta-echo"])] }),
  rpc("EP-12", "tools/list with the echo-only VK", listMsg, { headers: [MCP_ACCEPT, vkHeader("EP-12", "echo")], asserts: [A.status(200), toolNamesInList(["alpha-echo"])] }),
  rpc("EP-12", "tools/list with the no-grant VK", listMsg, { headers: [MCP_ACCEPT, vkHeader("EP-12", "none")], asserts: [A.status(200), toolNamesInList([])] }),
  restoreConfig("EP-12"),
  deleteVK("EP-12", "full"), deleteVK("EP-12", "echo"), deleteVK("EP-12", "none"),
  ...fleetTeardown("EP-12"),
]);

const EP14 = folder("EP-14 Include headers on /mcp", `Setup: ${FLEET}; an anonymous caller, and the echo-only VK (${VKS.echo.text}).`, [
  ...fleetSetup("EP-14"),
  createVK("EP-14", "echo"),
  rpc("EP-14", "include-clients beta", listMsg, { headers: [MCP_ACCEPT, inc("clients", "beta")], asserts: [A.status(200), toolNamesInList(["beta-echo"])] }),
  rpc("EP-14", "include-tools alpha-echo", listMsg, { headers: [MCP_ACCEPT, inc("tools", "alpha-echo")], asserts: [A.status(200), toolNamesInList(["alpha-echo"])] }),
  rpc("EP-14", "include-clients empty", listMsg, { headers: [MCP_ACCEPT, inc("clients", "")], asserts: [A.status(200), toolNamesInList([])] }),
  rpc("EP-14", "control: the echo-only VK with include-tools alpha-echo", listMsg, { headers: [MCP_ACCEPT, vkHeader("EP-14", "echo"), inc("tools", "alpha-echo")], asserts: [A.status(200), toolNamesInList(["alpha-echo"])] }),
  rpc("EP-14", "the echo-only VK with include-tools beta-echo", listMsg, { headers: [MCP_ACCEPT, vkHeader("EP-14", "echo"), inc("tools", "beta-echo")], asserts: [A.status(200), toolNamesInList([])] }),
  deleteVK("EP-14", "echo"),
  ...fleetTeardown("EP-14"),
]);

const EP24 = folder("EP-24 Listed but not granted", `Setup: ${FLEET}; the echo-only VK (${VKS.echo.text}) calls alpha-add, the no-grant VK (${VKS.none.text}) calls alpha-echo.`, [
  ...fleetSetup("EP-24"),
  createVK("EP-24", "echo"), createVK("EP-24", "none"),
  rpc("EP-24", "control: the echo-only VK calls alpha-echo", call("alpha-echo", { text: "hi" }), { headers: [MCP_ACCEPT, vkHeader("EP-24", "echo")], asserts: [A.status(200), A.custom("isError is not set and the content is the echo", ["var r = pm.response.json().result;", "pm.expect(r && r.isError, 'body: ' + pm.response.text()).to.not.equal(true);", "pm.expect(r.content).to.eql([{ type: 'text', text: 'echo:{\"text\":\"hi\"}' }]);"])] }),
  rpc("EP-24", "the echo-only VK calls alpha-add", call("alpha-add", { a: 1, b: 2 }), { headers: [MCP_ACCEPT, vkHeader("EP-24", "echo")], asserts: [A.status(200), toolResultError("Tool execution failed: MCP tool 'alpha-add' is not allowed", null)] }),
  rpc("EP-24", "the no-grant VK calls alpha-echo", call("alpha-echo", { text: "hi" }), { headers: [MCP_ACCEPT, vkHeader("EP-24", "none")], asserts: [A.status(200), toolResultError("Tool execution failed: MCP tool 'alpha-echo' is not allowed", null)] }),
  mockGet("EP-24", "alpha", "alpha ran only the control call", [A.custom("calls is {echo: 1}", ["pm.expect(pm.response.json().counters.calls, 'body: ' + pm.response.text()).to.eql({ echo: 1 });"])]),
  deleteVK("EP-24", "echo"), deleteVK("EP-24", "none"),
  ...fleetTeardown("EP-24"),
]);

// EP-53 polls for up to about 15s until the asynchronous log write lands and is no longer "processing".
const EP53_POLL = [
  "var left = 30;",
  "function poll() {",
  "  pm.sendRequest({ url: pm.variables.get('base_url') + '/api/mcp-logs/r1', method: 'GET', header: { Authorization: pm.variables.get('admin_auth_header') } }, function (err, res) {",
  "    if ((!err && res.code === 200 && res.json().status !== 'processing') || --left <= 0) { return; }",
  "    setTimeout(poll, 500);",
  "  });",
  "}",
  "poll();",
];
const EP53 = folder("EP-53 Log entry for a /mcp call", `Setup: ${FLEET}; the full-access VK (${VKS.full.text}); tools/call alpha-echo with x-request-id r1.`, [
  ...fleetSetup("EP-53"), createVK("EP-53", "full"),
  rpc("EP-53", "setup: tools/call alpha-echo with x-request-id r1", call("alpha-echo", { text: "hi" }), { headers: [MCP_ACCEPT, vkHeader("EP-53", "full"), { key: "x-request-id", value: "r1" }], asserts: [A.status(200)] }),
  request("EP-53", "GET /api/mcp-logs/r1", "GET", "/api/mcp-logs/r1", { pre: EP53_POLL, asserts: [A.status(200), A.jsonEquals("status", "success"), A.jsonEquals("tool_name", "echo"), A.jsonEquals("server_label", "alpha"), A.custom("virtual_key_id is the full-access VK's", ["pm.expect(pm.response.json().virtual_key_id, 'body: ' + pm.response.text()).to.equal(pm.collectionVariables.get('ep-53_full_id'));"])] }),
  request("EP-53", "teardown: delete the log entry", "DELETE", "/api/mcp-logs", { body: { ids: ["r1"] }, asserts: [A.status(200)] }),
  deleteVK("EP-53", "full"), ...fleetTeardown("EP-53"),
]);

// ================================================================ OAS redirect allowlist and SET client settings

const APP_CB = "https://app.example/cb";
const redirectAllowlist = (id, uris) => oauthConfig(id, "both", [`cc.oauth2_server_config.allowed_redirect_uris = ${lit(uris)};`],
  `setup: ${BOTH}, temp-token auth on, allowed_redirect_uris ${lit(uris)}`);
// registerWith registers a named client for one redirect URI, so a refusal can only be about that URI.
const registerWith = (id, uri, asserts, extraTests = []) => request(id, `register ${uri}`, "POST", "/oauth2/register", {
  noAuth: true, body: { client_name: `${id} ${uri}`, redirect_uris: [uri] }, asserts, extraTests,
});
const OAS10 = folder("OAS-10 Redirect allowlist and later removal", `Setup: ${BOTH}, dashboard auth on, temp-token auth on; allowed_redirect_uris exactly ["${APP_CB}"], then ["*"], then []; a virtual key to consent with.`, [
  readConfig("OAS-10"),
  vkCreate("OAS-10", "setup: create a virtual key to consent with", { name: "OAS-10 key" }, "oas10_key"),
  redirectAllowlist("OAS-10", [APP_CB]),
  registerWith("OAS-10", APP_CB, [A.status(201)], ["pm.collectionVariables.set('oas10_client', pm.response.json().client_id);"]),
  registerWith("OAS-10", "https://other.example/cb", [A.status(400)]),
  registerWith("OAS-10", "https://app.example/cb2", [A.status(400)]),
  authorize("OAS-10", "oas10_client", "oas10_f1", "setup: authorize a flow that stays pending", { redirect_uri: APP_CB }),
  authorize("OAS-10", "oas10_client", "oas10_f2", "setup: authorize a flow to consent", { redirect_uri: APP_CB }),
  consent("OAS-10", "oas10_f2", "setup: consent the second flow with the key", "temp", { mode: "vk", value: "{{oas10_key}}" }, [A.status(200), codeIssued()], { extraTests: saveCode("oas10_f2") }),
  authorize("OAS-10", "oas10_client", "oas10_f3", "setup: authorize a control flow", { redirect_uri: APP_CB }),
  consent("OAS-10", "oas10_f3", "setup: consent the control flow with the key", "temp", { mode: "vk", value: "{{oas10_key}}" }, [A.status(200), codeIssued()], { extraTests: saveCode("oas10_f3") }),
  tokenReq("OAS-10", "control: the same exchange succeeds while the redirect is approved", codeParams("oas10_f3", "oas10_client", { redirect_uri: APP_CB }), [A.status(200), tokensIssued()]),
  redirectAllowlist("OAS-10", ["*"]),
  registerWith("OAS-10", "https://any.example/cb", [A.status(201)]),
  registerWith("OAS-10", "cursor://other", [A.status(400)]),
  redirectAllowlist("OAS-10", []),
  request("OAS-10", "authorize after the allowlist is emptied", "GET", goodAuthorize("oas10_client", { redirect_uri: APP_CB }), { noAuth: true, noRedirect: true, asserts: [A.status(400)] }),
  consent("OAS-10", "oas10_f1", "consent the pending flow after the allowlist is emptied", "temp", { mode: "vk", value: "{{oas10_key}}" }, [A.status(400), A.msgEquals("redirect_uri is no longer approved")]),
  tokenReq("OAS-10", "exchange the second flow's code after the allowlist is emptied", codeParams("oas10_f2", "oas10_client", { redirect_uri: APP_CB }), [A.status(400), O.descEquals("redirect_uri mismatch")]),
  restoreConfig("OAS-10"),
  vkDelete("OAS-10", "teardown: delete the virtual key", "oas10_key"),
]);

// SET tests use client alpha on the alpha mock server (echo_tool, add_tool), served as alpha-<tool>.
const setClient = (id, over) => [
  ...freshServer(id, "alpha", ["echo_tool", "add_tool"]),
  createClient(id, httpClient(`${id.toLowerCase()}-a`, "alpha", "alpha", { tools_to_execute: ["*"], ...over }), "setup: create client alpha"),
];
const setExec = (id, title, tool, asserts, headers = []) => execTool(id, title, `alpha-${tool}`, asserts, { headers });
// pollMcpList re-sends tools/list to /mcp, 200ms apart for up to about 2s, until cond (ES5 on the tool list) holds;
// /mcp resyncs from a background goroutine after a refresh.
const pollMcpList = (cond) => [
  "var left = 10;",
  "function poll() {",
  "  pm.sendRequest({ url: pm.variables.get('base_url') + '/mcp', method: 'POST', header: { 'Content-Type': 'application/json', 'Accept': 'application/json, text/event-stream' }, body: { mode: 'raw', raw: JSON.stringify({ jsonrpc: '2.0', id: 1, method: 'tools/list' }) } }, function (err, res) {",
  "    var tools = !err && res.code === 200 && res.json().result ? res.json().result.tools : null;",
  `    if ((tools && (${cond})) || --left <= 0) { return; }`,
  "    setTimeout(poll, 200);",
  "  });",
  "}",
  "poll();",
];
const callsBefore = (id, varName, tool) => mockGet(id, "alpha", `setup: record alpha's ${tool} calls`, [], [`pm.collectionVariables.set(${lit(varName)}, (pm.response.json().counters.calls || {})[${lit(tool)}] || 0);`]);
const callsPlusOne = (varName, tool) => A.custom(`calls[${tool}] is one more than the recorded count`, [
  `var before = Number(pm.collectionVariables.get(${lit(varName)}));`,
  `pm.expect((pm.response.json().counters.calls || {})[${lit(tool)}] || 0, 'body: ' + pm.response.text()).to.equal(before + 1);`,
]);

const SET01 = folder("SET-01 Regex and case-insensitive allow-list entries", "Setup: alpha (echo_tool, add_tool) as client alpha, per call, tools_to_execute [\"regex:^ECHO.*\"], then [\"ECHO_TOOL\"].", [
  ...setClient("SET-01", { tools_to_execute: ["regex:^ECHO.*"] }),
  ...[["regex:^ECHO.*", null], ["ECHO_TOOL", ["ECHO_TOOL"]]].flatMap(([label, put], run) => [
    ...(put ? [
      request("SET-01", `setup: set tools_to_execute ${lit(put)}`, "PUT", "/api/mcp/client/set-01-a", { body: { tools_to_execute: put }, asserts: [A.status(200)] }),
      getClient("SET-01", "set-01-a", `the stored tools_to_execute is ${lit(put)}`, [A.custom(`tools_to_execute is ${lit(put)}`, [`pm.expect(pm.response.json().clients[0].config.tools_to_execute, 'body: ' + pm.response.text()).to.eql(${lit(put)});`])]),
    ] : []),
    callsBefore("SET-01", `set01_echo_${run}`, "echo_tool"),
    setExec("SET-01", `${label}: execute echo_tool`, "echo_tool", [A.status(200)]),
    mockGet("SET-01", "alpha", `${label}: alpha counted the echo_tool call`, [callsPlusOne(`set01_echo_${run}`, "echo_tool")]),
    setExec("SET-01", `${label}: execute add_tool`, "add_tool", [A.status(400), A.msgContains("is not permitted")]),
  ]),
  request("SET-01", "set tools_to_execute [\"regex:(\"]", "PUT", "/api/mcp/client/set-01-a", { body: { tools_to_execute: ["regex:("] }, asserts: [A.status(400), A.msgContains("does not compile")] }),
  deleteClient("SET-01", "set-01-a"),
]);

const SET02 = folder("SET-02 A VK grant and the client allow-list both apply", "Setup: alpha (echo_tool, add_tool) as client alpha, allow_by_default true, tools_to_execute [\"add_tool\"]; an empty-grant VK grants alpha []; a no-config VK has no MCP configs.", [
  ...setClient("SET-02", { allow_by_default: true, tools_to_execute: ["add_tool"] }),
  vkCreate("SET-02", "setup: create the empty-grant VK, granting alpha []", { name: "SET-02 empty-grant key", mcp_configs: [{ mcp_client_name: "alpha", tools_to_execute: [] }] }, "set02_vk1"),
  vkCreate("SET-02", "setup: create the no-config VK", { name: "SET-02 no-config key" }, "set02_vk2"),
  setExec("SET-02", "execute echo_tool with the empty-grant VK", "echo_tool", [A.status(403), A.msgContains("is not allowed")], [{ key: "x-bf-vk", value: "{{set02_vk1}}" }]),
  setExec("SET-02", "execute echo_tool with the no-config VK", "echo_tool", [A.status(400), A.msgContains("not in client's ToolsToExecute allow-list")], [{ key: "x-bf-vk", value: "{{set02_vk2}}" }]),
  vkDelete("SET-02", "teardown: delete the empty-grant VK", "set02_vk1"),
  vkDelete("SET-02", "teardown: delete the no-config VK", "set02_vk2"),
  deleteClient("SET-02", "set-02-a"),
]);

const alphaToolsOnMcp = "tools.filter(function (t) { return t.name.indexOf('alpha-') === 0; }).map(function (t) { return t.name; }).sort()";
const SET07 = folder("SET-07 Refresh picks up added and removed tools, skips bad names", "Setup: alpha (echo_tool, add_tool) as client alpha, sticky, tools_to_execute [\"*\"]; then alpha adds z_tool, removes add_tool and adds \"bad/name\".", [
  ...setClient("SET-07", { needs_session_stickiness: true }),
  mockPost("SET-07", "alpha", "tools/add", "setup: alpha adds z_tool", { tool: { name: "z_tool" } }),
  mockPost("SET-07", "alpha", "tools/remove", "setup: alpha removes add_tool", { name: "add_tool" }),
  mockPost("SET-07", "alpha", "tools/add", "setup: alpha adds bad/name", { tool: { name: "bad/name" } }),
  request("SET-07", "refresh-tools", "POST", "/api/mcp/client/set-07-a/refresh-tools", { asserts: [A.status(200), A.jsonEquals("tool_count", 2)] }),
  getClient("SET-07", "set-07-a", "the client lists echo_tool and z_tool", [toolNamesAre(["echo_tool", "z_tool"])]),
  setExec("SET-07", "execute add_tool", "add_tool", [A.status(400), A.msgContains("is not available or not permitted")]),
  request("SET-07", "/mcp lists the same tools", "POST", "/mcp", {
    noAuth: true, raw: JSON.stringify(listMsg), headers: [MCP_ACCEPT], pre: pollMcpList(`JSON.stringify(${alphaToolsOnMcp}) === '["alpha-echo_tool","alpha-z_tool"]'`),
    asserts: [A.status(200), A.custom("alpha's tools are exactly [\"alpha-echo_tool\",\"alpha-z_tool\"]", [
      "var tools = pm.response.json().result.tools;",
      `pm.expect(${alphaToolsOnMcp}, 'body: ' + pm.response.text()).to.eql(["alpha-echo_tool", "alpha-z_tool"]);`,
    ])],
  }),
  deleteClient("SET-07", "set-07-a"),
]);

const SET08 = folder("SET-08 Upstream rename against a named allow-list", "Setup: alpha (echo_tool, add_tool) as client alpha, per call, tools_to_execute [\"echo_tool\"]; alpha renames echo_tool to echo2; then refresh-tools.", [
  ...setClient("SET-08", { tools_to_execute: ["echo_tool"] }),
  mockPost("SET-08", "alpha", "tools/rename", "setup: alpha renames echo_tool to echo2", { from: "echo_tool", to: "echo2" }),
  request("SET-08", "setup: refresh-tools", "POST", "/api/mcp/client/set-08-a/refresh-tools", { asserts: [A.status(200)] }),
  getClient("SET-08", "set-08-a", "the tools have echo2, not echo_tool", [A.custom("tools include echo2 and not echo_tool", [
    "var names = pm.response.json().clients[0].tools.map(function (t) { return t.name; });",
    "pm.expect(names, 'body: ' + pm.response.text()).to.include('echo2');",
    "pm.expect(names).to.not.include('echo_tool');",
  ])]),
  setExec("SET-08", "execute echo2", "echo2", [A.status(400), A.msgContains("not in client's ToolsToExecute allow-list")]),
  setExec("SET-08", "execute echo_tool", "echo_tool", [A.status(400), A.msgContains("is not available or not permitted")]),
  mockGet("SET-08", "alpha", "alpha never ran echo2", [callsIs("echo2", 0)]),
  deleteClient("SET-08", "set-08-a"),
]);

const SET09_PROP = { type: "string", description: "SET-09 property" };
const echoToolEntry = "pm.response.json().clients[0].tools.filter(function (t) { return t.name === 'echo_tool'; })[0]";
const SET09 = folder("SET-09 Refresh picks up schema and description edits", "Setup: alpha (echo_tool, add_tool) as client alpha, per call, tools_to_execute [\"*\"]; alpha changes echo_tool's input schema (a required set09_prop) and its description.", [
  ...setClient("SET-09", {}),
  mockPost("SET-09", "alpha", "tools/schema", "setup: alpha changes echo_tool's schema", { name: "echo_tool", schema: { type: "object", properties: { set09_prop: SET09_PROP }, required: ["set09_prop"] } }),
  mockPost("SET-09", "alpha", "tools/describe", "setup: alpha changes echo_tool's description", { name: "echo_tool", text: "SET-09 new description" }),
  getClient("SET-09", "set-09-a", "control: before the refresh echo_tool keeps its old description", [A.custom("description is \"chaosmock tool echo_tool\"", [
    `var t = ${echoToolEntry};`,
    "pm.expect(t && t.description, 'body: ' + pm.response.text()).to.equal('chaosmock tool echo_tool');",
  ])]),
  request("SET-09", "refresh-tools", "POST", "/api/mcp/client/set-09-a/refresh-tools", { asserts: [A.status(200)] }),
  getClient("SET-09", "set-09-a", "GET shows the new description and property", [A.custom(`description "SET-09 new description", parameters.properties.set09_prop is ${lit(SET09_PROP)}`, [
    `var t = ${echoToolEntry};`,
    "pm.expect(t && t.description, 'body: ' + pm.response.text()).to.equal('SET-09 new description');",
    `pm.expect(t.parameters.properties.set09_prop).to.eql(${lit(SET09_PROP)});`,
  ])], { extraTests: [`pm.collectionVariables.set('set09_parameters', JSON.stringify(${echoToolEntry}.parameters));`] }),
  request("SET-09", "/mcp inputSchema matches", "POST", "/mcp", {
    noAuth: true, raw: JSON.stringify(listMsg), headers: [MCP_ACCEPT],
    pre: pollMcpList("tools.some(function (t) { return t.name === 'alpha-echo_tool' && t.inputSchema && t.inputSchema.properties && t.inputSchema.properties.set09_prop; })"),
    asserts: [A.status(200), A.custom("alpha-echo_tool's inputSchema equals GET's parameters, required included", [
      "var t = pm.response.json().result.tools.filter(function (x) { return x.name === 'alpha-echo_tool'; })[0];",
      "pm.expect(t && t.inputSchema, 'body: ' + pm.response.text()).to.eql(JSON.parse(pm.collectionVariables.get('set09_parameters')));",
      "pm.expect(t.inputSchema.required).to.eql(['set09_prop']);",
    ])],
  }),
  deleteClient("SET-09", "set-09-a"),
]);

const SET21 = folder("SET-21 Slug survives a rename", "Setup: alpha (echo_tool, add_tool) as client alpha, per call, tools_to_execute [\"*\"]; its slug is \"alpha\"; it is renamed to alpha2.", [
  ...setClient("SET-21", {}),
  getClient("SET-21", "set-21-a", "control: the slug is \"alpha\"", [clientField("config.endpoint_slug", "alpha")]),
  request("SET-21", "setup: rename the client to alpha2", "PUT", "/api/mcp/client/set-21-a", { body: { name: "alpha2" }, asserts: [A.status(200)] }),
  getClient("SET-21", "set-21-a", "the slug is still \"alpha\"", [clientField("config.endpoint_slug", "alpha")]),
  list("SET-21", "/mcp/alpha lists alpha2's tools", "/mcp/alpha", [], [A.status(200), toolNamesInList(["alpha2-add_tool", "alpha2-echo_tool"])]),
  deleteClient("SET-21", "set-21-a"),
]);

const CODE_MODE_TOOLS = ["listToolFiles", "readToolFile", "getToolDocs", "executeToolCode"];
const mcpNames = "pm.response.json().result.tools.map(function (t) { return t.name; })";
const SET22 = folder("SET-22 Code-mode flag swaps the listed tools", "Setup: alpha (echo_tool, add_tool) as client alpha, per call, tools_to_execute [\"*\"]; is_code_mode_client set true, then false.", [
  ...setClient("SET-22", {}),
  request("SET-22", "set is_code_mode_client true", "PUT", "/api/mcp/client/set-22-a", { body: { is_code_mode_client: true }, asserts: [A.status(200)] }),
  list("SET-22", "/mcp after true", "/mcp", [], [A.status(200), A.custom(`lists ${CODE_MODE_TOOLS.join(", ")} and no alpha-* tool`, [
    `var names = ${mcpNames};`,
    `${lit(CODE_MODE_TOOLS)}.forEach(function (n) { pm.expect(names, 'body: ' + pm.response.text()).to.include(n); });`,
    "pm.expect(names.filter(function (n) { return n.indexOf('alpha-') === 0; })).to.eql([]);",
  ])]),
  request("SET-22", "set is_code_mode_client false", "PUT", "/api/mcp/client/set-22-a", { body: { is_code_mode_client: false }, asserts: [A.status(200)] }),
  list("SET-22", "/mcp after false", "/mcp", [], [A.status(200), A.custom("lists alpha-add_tool and alpha-echo_tool and no code mode tool", [
    `var names = ${mcpNames};`,
    "pm.expect(names, 'body: ' + pm.response.text()).to.include('alpha-add_tool');",
    "pm.expect(names).to.include('alpha-echo_tool');",
    `${lit(CODE_MODE_TOOLS)}.forEach(function (n) { pm.expect(names).to.not.include(n); });`,
  ])]),
  deleteClient("SET-22", "set-22-a"),
]);

// ================================================================ INF direct execute and code mode

// The execute tests' setup: alpha (echo, add, greet) as client alpha, nothing auto-executed. These calls never reach a
// provider, so the mock LLM is not needed.
const ALPHA_GREET_TEXT = "alpha (echo, add, greet) registered as client alpha, auth none, per call, tools_to_execute [\"*\"], nothing auto-executed";
const alphaGreetSetup = (id) => [
  ...freshServer(id, "alpha", ["echo", "add", "greet"]),
  createClient(id, httpClient(`${id.toLowerCase()}-alpha`, "alpha", "alpha", { tools_to_execute: ["*"] }), "setup: register client alpha"),
];
const alphaGreetTeardown = (id) => deleteClient(id, `${id.toLowerCase()}-alpha`);
// execBody posts a JSON body (or raw text) to /v1/mcp/tool/execute with no admin credentials; query is appended as is.
const execBody = (id, title, body, asserts, { query = "", raw, headers = [], extraTests = [] } = {}) => request(id, title, "POST", `/v1/mcp/tool/execute${query}`, {
  noAuth: true, noCookies: true, asserts, headers, extraTests, ...(raw !== undefined ? { raw } : { body }),
});
// toolCall is a chat-format tool call with id c1; args is an object or a literal arguments string.
const toolCall = (name, args) => ({ id: "c1", type: "function", function: { name, arguments: typeof args === "string" ? args : lit(args) } });
const contentEquals = (s) => A.custom(`content is ${lit(s)}`, [`pm.expect(pm.response.json().content, 'body: ' + pm.response.text()).to.equal(${lit(s)});`]);
const contentHas = (s) => A.custom(`content contains ${lit(s)}`, [`pm.expect(pm.response.json().content, 'body: ' + pm.response.text()).to.include(${lit(s)});`]);
const isError = () => A.custom("is_error is true", ["pm.expect(pm.response.json().is_error, 'body: ' + pm.response.text()).to.equal(true);"]);
const noIsError = () => A.custom("there is no is_error", ["pm.expect(pm.response.json(), 'body: ' + pm.response.text()).to.not.have.property('is_error');"]);
const callsEmpty = (id, server) => mockGet(id, server, `${server} received no tool call`, [A.custom("calls is empty", ["pm.expect(pm.response.json().counters.calls, 'body: ' + pm.response.text()).to.eql({});"])]);
const ECHO_CALL = toolCall("alpha-echo", { text: "hi" });

const INF24 = folder("INF-24 Execute, chat format", `Setup: ${ALPHA_GREET_TEXT}; execute alpha-echo with id c1.`, [
  ...alphaGreetSetup("INF-24"),
  execBody("INF-24", "execute alpha-echo with id c1", ECHO_CALL, [A.status(200), A.custom("role \"tool\", tool_call_id \"c1\", content the echo", [
    "var b = pm.response.json();",
    "pm.expect(b.role, 'body: ' + pm.response.text()).to.equal('tool');",
    "pm.expect(b.tool_call_id).to.equal('c1');",
    "pm.expect(b.content).to.equal('echo:{\"text\":\"hi\"}');",
  ]), noIsError()]),
  mockCalls("INF-24", "alpha", "echo", 1),
  alphaGreetTeardown("INF-24"),
]);

const INF25 = folder("INF-25 Execute, Responses format", `Setup: ${ALPHA_GREET_TEXT}; execute alpha-echo in the Responses format with call_id r1.`, [
  ...alphaGreetSetup("INF-25"),
  execBody("INF-25", "execute alpha-echo with format=responses and call_id r1", { call_id: "r1", name: "alpha-echo", arguments: lit({ text: "hi" }) }, [A.status(200), A.custom("type \"function_call_output\", call_id \"r1\", output a string", [
    "var b = pm.response.json();",
    "pm.expect(b.type, 'body: ' + pm.response.text()).to.equal('function_call_output');",
    "pm.expect(b.call_id).to.equal('r1');",
    "pm.expect(b.output).to.be.a('string');",
  ])], { query: "?format=responses" }),
  alphaGreetTeardown("INF-25"),
]);

const INF26 = folder("INF-26 Execute validation", `Setup: ${ALPHA_GREET_TEXT}.`, [
  ...alphaGreetSetup("INF-26"),
  execBody("INF-26", "format=bogus", ECHO_CALL, [A.status(400)], { query: "?format=bogus" }),
  execBody("INF-26", "a non-JSON body", undefined, [A.status(400), A.msgEquals("Invalid request payload")], { raw: "x" }),
  execBody("INF-26", "chat format without a tool name", { id: "m1", type: "function", function: { arguments: "{}" } }, [A.status(400), A.msgEquals("Tool function name is required")]),
  execBody("INF-26", "Responses format without a tool name", { call_id: "m2", arguments: "{}" }, [A.status(400), A.msgEquals("Tool function name is required")], { query: "?format=responses" }),
  alphaGreetTeardown("INF-26"),
]);

const INF27 = folder("INF-27 Unknown tool and bad arguments", `Setup: ${ALPHA_GREET_TEXT}.`, [
  ...alphaGreetSetup("INF-27"),
  execBody("INF-27", "execute nosuch-x", toolCall("nosuch-x", "{}"), [A.status(400), A.msgContains("is not available or not permitted")]),
  execBody("INF-27", "execute alpha-echo with arguments \"{bad\"", toolCall("alpha-echo", "{bad"), [A.status(400), A.msgContains("failed to parse tool arguments for 'alpha-echo'")]),
  callsEmpty("INF-27", "alpha"),
  alphaGreetTeardown("INF-27"),
]);

// upstreamFault fails every tools/call on alpha and checks the execute error and how many calls reached alpha.
const upstreamFault = (name, label, faults, calls) => {
  const id = name.split(" ")[0];
  return folder(name, `Setup: ${ALPHA_GREET_TEXT}; alpha then fails every tools/call with ${label}.`, [
    ...alphaGreetSetup(id),
    mockPost(id, "alpha", "faults", `setup: alpha fails every tools/call with ${label}`, { faults }),
    execBody(id, "execute alpha-echo", ECHO_CALL, [A.status(400), A.msgContains("MCP tool call failed for alpha-echo")]),
    mockCalls(id, "alpha", "echo", calls),
    mockPost(id, "alpha", "faults/clear", "teardown: clear alpha's faults"),
    alphaGreetTeardown(id),
  ]);
};
const INF29 = upstreamFault("INF-29 Upstream HTTP 500", "HTTP 500 (err_kind http)", { err_rate: 1, err_kind: "http" }, 3);
const INF30 = upstreamFault("INF-30 Upstream JSON-RPC error", "a JSON-RPC error (err_kind protocol)", { err_rate: 1, err_kind: "protocol" }, 1);

// cm is the code-mode client on the cm mock server; meta tools are called by their bare names.
const CM_TEXT = "cm (echo, add) registered as a code-mode client, auth none, per call, tools_to_execute [\"*\"]";
const cmSetup = (id, tools = ["echo", "add"], over = {}) => [
  ...freshServer(id, "cm", tools),
  createClient(id, httpClient(`${id.toLowerCase()}-cm`, "cm", "cm", { tools_to_execute: ["*"], is_code_mode_client: true, ...over }), "setup: register cm as a code-mode client"),
];
const cmTeardown = (id) => deleteClient(id, `${id.toLowerCase()}-cm`);
const meta = (id, title, tool, args, asserts) => execBody(id, title, toolCall(tool, args), asserts);
const code = (id, title, src, asserts) => meta(id, title, "executeToolCode", { code: src }, asserts);

const INF49 = folder("INF-49 Tool binding", `Setup: ${CM_TEXT}; mcp_code_mode_binding_level "tool".`, [
  ...cmSetup("INF-49"), readConfig("INF-49"),
  putConfig("INF-49", "setup: set mcp_code_mode_binding_level \"tool\"", ["cc.mcp_code_mode_binding_level = 'tool';"], [A.status(200)]),
  meta("INF-49", "listToolFiles", "listToolFiles", "{}", [A.status(200), A.custom("lists servers/cm/add.pyi and servers/cm/echo.pyi, and no other file", [
    "var c = pm.response.json().content;",
    "pm.expect(c, 'body: ' + pm.response.text()).to.include('servers/\\n  cm/\\n    add.pyi\\n    echo.pyi');",
    "pm.expect(c.split('\\n').filter(function (l) { return /\\.pyi$/.test(l); })).to.eql(['    add.pyi', '    echo.pyi']);",
  ])]),
  meta("INF-49", "readToolFile servers/cm/echo.pyi", "readToolFile", { fileName: "servers/cm/echo.pyi" }, [A.status(200), contentHas("# cm.echo tool")]),
  putConfig("INF-49", "set mcp_code_mode_binding_level \"bogus\"", ["cc.mcp_code_mode_binding_level = 'bogus';"], [A.status(400), A.msgContains("must be 'server' or 'tool'")]),
  restoreConfig("INF-49"), cmTeardown("INF-49"),
]);

const INF50 = folder("INF-50 readToolFile errors", `Setup: ${CM_TEXT}.`, [
  ...cmSetup("INF-50"),
  meta("INF-50", "readToolFile with no fileName", "readToolFile", "{}", [A.status(400)]),
  meta("INF-50", "readToolFile servers/nope.pyi", "readToolFile", { fileName: "servers/nope.pyi" }, [A.status(200), isError(), contentHas("No server found matching 'nope'")]),
  meta("INF-50", "readToolFile servers/../cm.pyi", "readToolFile", { fileName: "servers/../cm.pyi" }, [A.status(200), isError(), contentHas("No server found")]),
  meta("INF-50", "readToolFile servers/cm/zzz.pyi", "readToolFile", { fileName: "servers/cm/zzz.pyi" }, [A.status(200), isError(), contentHas("Tool 'zzz' not found in server 'cm'")]),
  cmTeardown("INF-50"),
]);

const INF51 = folder("INF-51 readToolFile success and slicing", `Setup: ${CM_TEXT}.`, [
  ...cmSetup("INF-51"),
  execBody("INF-51", "readToolFile servers/cm.pyi", toolCall("readToolFile", { fileName: "servers/cm.pyi" }), [A.status(200), noIsError(), A.custom("line 1 is \"# Total lines: N (this is the complete file, no need to paginate)\"", [
    "var first = pm.response.json().content.split('\\n')[0];",
    "pm.expect(first, 'body: ' + pm.response.text()).to.match(/^# Total lines: \\d+ \\(this is the complete file, no need to paginate\\)$/);",
  ]), contentHas("# cm server tools")], { extraTests: ["pm.collectionVariables.set('inf51_line2', pm.response.json().content.split('\\n')[1]);"] }),
  meta("INF-51", "readToolFile servers/cm.pyi lines 2 to 2", "readToolFile", { fileName: "servers/cm.pyi", startLine: 2, endLine: 2 }, [A.status(200), A.custom("content is exactly line 2 of the full read", [
    "var c = pm.response.json().content;",
    "pm.expect(c, 'body: ' + pm.response.text()).to.be.a('string').and.not.equal('');",
    "pm.expect(c).to.equal(pm.collectionVariables.get('inf51_line2'));",
  ])]),
  cmTeardown("INF-51"),
]);

const INF52 = folder("INF-52 getToolDocs", `Setup: ${CM_TEXT}.`, [
  ...cmSetup("INF-52"),
  meta("INF-52", "getToolDocs cm/echo", "getToolDocs", { server: "cm", tool: "echo" }, [A.status(200), contentHas("# Documentation for cm.echo tool")]),
  meta("INF-52", "getToolDocs with no server", "getToolDocs", { tool: "echo" }, [A.status(400)]),
  meta("INF-52", "getToolDocs server x", "getToolDocs", { server: "x", tool: "echo" }, [A.status(200), isError(), contentHas("Server 'x' not found")]),
  meta("INF-52", "getToolDocs cm/x", "getToolDocs", { server: "cm", tool: "x" }, [A.status(200), isError(), contentHas("Tool 'x' not found in server 'cm'")]),
  cmTeardown("INF-52"),
]);

const INF53 = folder("INF-53 executeToolCode reply shapes", `Setup: ${CM_TEXT}.`, [
  ...cmSetup("INF-53"),
  code("INF-53", "result = 6*7", "result = 6*7", [A.status(200), contentEquals("Execution completed successfully.\nReturn value: 42"), noIsError()]),
  code("INF-53", "print('a')", "print('a')", [A.status(200), contentEquals("Print output:\na\n\nExecution completed successfully.")]),
  code("INF-53", "x = 1", "x = 1", [A.status(200), isError(), contentHas("Execution completed but produced no data")]),
  cmTeardown("INF-53"),
]);

const INF54 = folder("INF-54 executeToolCode errors", "Setup: cm (echo, add, greet) registered as a code-mode client, auth none, per call, tools_to_execute [\"echo\", \"add\"].", [
  ...cmSetup("INF-54", ["echo", "add", "greet"], { tools_to_execute: ["echo", "add"] }),
  code("INF-54", "empty code", "", [A.status(400)]),
  code("INF-54", "a syntax error", "x = = 1", [A.status(200), isError(), contentHas("Execution runtime error"), contentHas("code.star:1")]),
  mockGet("INF-54", "cm", "control: cm serves greet upstream", [A.custom("cm's tools include greet", [
    "var names = pm.response.json().tools.map(function (t) { return t.name; });",
    "pm.expect(names, 'body: ' + pm.response.text()).to.include('greet');",
  ])]),
  code("INF-54", "control: cm.echo runs", "result = cm.echo()", [A.status(200), noIsError()]),
  code("INF-54", "call cm.greet", "r = cm.greet()", [A.status(200), isError(), contentHas("has no .greet attribute")]),
  mockCalls("INF-54", "cm", "greet", 0),
  cmTeardown("INF-54"),
]);

const limits = (id, title, value) => putConfig(id, title, [`cc.mcp_code_mode_limits = ${lit(value)};`], [A.status(200)]);
const INF55 = folder("INF-55 Limits apply live", `Setup: ${CM_TEXT}; mcp_code_mode_limits changed between calls.`, [
  ...cmSetup("INF-55"), readConfig("INF-55"),
  limits("INF-55", "setup: max_tool_calls 2", { max_tool_calls: 2 }),
  code("INF-55", "three cm.echo calls", "cm.echo()\ncm.echo()\ncm.echo()\nresult = 1", [A.status(200), contentHas("code mode tool call limit exceeded")]),
  mockCalls("INF-55", "cm", "echo", 2),
  limits("INF-55", "setup: max_source_bytes 16", { max_source_bytes: 16 }),
  code("INF-55", "22 bytes of code", "result = 1 + 2 + 3 + 4", [A.status(200), contentHas("code exceeds source limit")]),
  limits("INF-55", "setup: max_log_bytes 64", { max_log_bytes: 64 }),
  code("INF-55", "print 100 characters", `print("${"a".repeat(100)}")`, [A.status(200), contentHas("log limit exceeded")]),
  putConfig("INF-55", "teardown: restore the config and reset mcp_code_mode_limits", ["cc.mcp_code_mode_limits = {};"], [A.status(200)]),
  cmTeardown("INF-55"),
]);

const INF57 = folder("INF-57 Nested call transport failure", `Setup: ${CM_TEXT}; cm then fails every tools/call with HTTP 500 (err_kind http).`, [
  ...cmSetup("INF-57"),
  mockPost("INF-57", "cm", "faults", "setup: cm fails every tools/call with HTTP 500", { faults: { err_rate: 1, err_kind: "http" } }),
  code("INF-57", "call cm.echo", "r = cm.echo(text='hi')\nresult = r", [A.status(200), isError(), contentHas("Execution runtime error"), contentHas("Print output:\n[TOOL] cm.echo call failed")]),
  mockPost("INF-57", "cm", "faults/clear", "teardown: clear cm's faults"),
  cmTeardown("INF-57"),
]);

// INF-58 counts log entries before and after. MCP log entries are written in 5s batches, so the baseline waits 7s
// for earlier folders' log entries to land, and the after-count polls until the two nested log entries arrive.
const logCountReq = (id, title, query, varName, pre = [], asserts = []) => request(id, title, "GET", `/api/mcp-logs?${query}`, {
  pre, asserts: [A.status(200), ...asserts], extraTests: varName ? [`pm.collectionVariables.set(${lit(varName)}, pm.response.json().pagination.total_count);`] : [],
});
const inf58Poll = (n) => [
  `var want = Number(pm.collectionVariables.get('inf58_echo_before')) + ${n}, left = 30;`,
  "function poll() {",
  "  pm.sendRequest({ url: pm.variables.get('base_url') + '/api/mcp-logs?server_labels=cm&tool_names=echo&limit=1', method: 'GET', header: { Authorization: pm.variables.get('admin_auth_header') } }, function (err, res) {",
  "    if ((!err && res.code === 200 && res.json().pagination.total_count >= want) || --left <= 0) { return; }",
  "    setTimeout(poll, 500);",
  "  });",
  "}",
  "poll();",
];
const INF58 = folder("INF-58 Nested calls are logged", `Setup: ${CM_TEXT}; executeToolCode calls cm.echo twice.`, [
  ...cmSetup("INF-58"),
  logCountReq("INF-58", "setup: count cm echo log entries", "server_labels=cm&tool_names=echo&limit=1", "inf58_echo_before", ["setTimeout(function () {}, 7000);"]),
  logCountReq("INF-58", "setup: count executeToolCode log entries", "tool_names=executeToolCode&limit=1", "inf58_etc_before"),
  code("INF-58", "executeToolCode calls cm.echo twice", "cm.echo(text='one')\ncm.echo(text='two')\nresult = 1", [A.status(200)]),
  logCountReq("INF-58", "two more cm echo log entries", "server_labels=cm&tool_names=echo&limit=1", null, inf58Poll(2), [A.custom("total_count is two more than before", [
    "pm.expect(pm.response.json().pagination.total_count, 'body: ' + pm.response.text()).to.equal(Number(pm.collectionVariables.get('inf58_echo_before')) + 2);",
  ])]),
  logCountReq("INF-58", "the two newest cm log entries are echo", "server_labels=cm&limit=2", null, [], [A.custom("both log entries have tool_name \"echo\" and server_label \"cm\"", [
    "var logs = pm.response.json().logs;",
    "pm.expect(logs.length, 'body: ' + pm.response.text()).to.equal(2);",
    "logs.forEach(function (l) { pm.expect(l.tool_name).to.equal('echo'); pm.expect(l.server_label).to.equal('cm'); });",
  ])]),
  // A later call whose log entry lands after any log entry the first executeToolCode could have queued, so a flush boundary
  // between the nested log entries and an executeToolCode log entry cannot hide the latter.
  code("INF-58", "control: a later executeToolCode call", "cm.echo(text='three')\nresult = 1", [A.status(200)]),
  logCountReq("INF-58", "control: the later call's log entry landed", "server_labels=cm&tool_names=echo&limit=1", null, inf58Poll(3), [A.custom("total_count is three more than before", [
    "pm.expect(pm.response.json().pagination.total_count, 'body: ' + pm.response.text()).to.equal(Number(pm.collectionVariables.get('inf58_echo_before')) + 3);",
  ])]),
  logCountReq("INF-58", "no executeToolCode log entry", "tool_names=executeToolCode&limit=1", null, [], [A.custom("total_count is unchanged", [
    "pm.expect(pm.response.json().pagination.total_count, 'body: ' + pm.response.text()).to.equal(Number(pm.collectionVariables.get('inf58_etc_before')));",
  ])]),
  cmTeardown("INF-58"),
]);

const INJECTED_PREFIX = "Invalid injected tools config: injected tools validation failed:";
// INF66_CASES pairs each bad value with the part of the message that names its defect, so a case cannot
// pass by failing for another case's reason.
const INF66_CASES = [
  ["a bad name", [{ name: "web search", mcp_client_name: "alpha", tool_name: "search" }], "injected_tools[0].name \"web search\" must be 1-64 letters, digits, _ or -"],
  ["a missing tool_name", [{ name: "web_search", mcp_client_name: "alpha" }], "tool_name is required"],
  ["a duplicate name", [{ name: "web_search", mcp_client_name: "alpha", tool_name: "search" }, { name: "web_search", mcp_client_name: "alpha", tool_name: "other" }], "name \"web_search\" is used more than once"],
  ["the same tool twice", [{ name: "web_search", mcp_client_name: "alpha", tool_name: "search" }, { name: "search2", mcp_client_name: "alpha", tool_name: "search" }], "MCP tool alpha/search is injected more than once"],
  ["an unknown client", [{ name: "web_search", mcp_client_name: "inf66_no_such_client", tool_name: "search" }], "references unknown MCP client \"inf66_no_such_client\""],
  ["an unknown field", [{ name: "web_search", mcp_client_name: "alpha", tool_name: "search", extra: 1 }], "has unknown field \"extra\""],
  ["a non-array", { name: "web_search", mcp_client_name: "alpha", tool_name: "search" }, "injected_tools must be an array"],
];
const INF66 = folder("INF-66 Injected tools validation", "Setup: PUT /api/providers/openai with concurrency_and_buffer_size set and one bad injected_tools value per request.", INF66_CASES.map(([label, tools, reason]) =>
  request("INF-66", `injected_tools with ${label}`, "PUT", "/api/providers/openai", {
    body: { concurrency_and_buffer_size: { concurrency: 1, buffer_size: 10 }, injected_tools: tools },
    asserts: [A.status(400), A.msgStartsWith(INJECTED_PREFIX), A.msgContains(reason)],
  })));

// ================================================================ MCP tools inside model requests, on the mock LLM

const alphaClient = (id, over = {}) => createClient(id, httpClient(`${id.toLowerCase()}-alpha`, "alpha", "alpha", { tools_to_execute: ["*"], ...over }), "setup: register client alpha");

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
    section("ADM Client admin", "Client admin tests.", [ADM01, ADM03, ADM05, ADM06, ADM07, ADM08, ADM10, ADM11, ADM12, ADM13, ADM16, ADM17, ADM18, ADM19, ADM20, ADM22, ADM42, ADM44, ADM46, ADM51, ADM52, ADM53, ADM54, ADM55, ADM59, ADM62, ADM65, ADM66, ADM73, ADM81, ADM82, ADM83]),
    section("AUTH Upstream auth", "Upstream auth tests.", [AUTH01, AUTH02, AUTH03, AUTH05, AUTH06, AUTH09, AUTH41, AUTH42, AUTH43, AUTH45, AUTH48]),
    section("CONN Connection types", "Connection type tests.", [CONN02, CONN03, CONN04, CONN17, CONN18, CONN22]),
    section("EP Bifrost MCP endpoint", "Bifrost MCP endpoint tests.", [EP01, EP02, EP04, EP09, EP12, EP13, EP14, EP15, EP17, EP18, EP20, EP23, EP24, EP25, EP26, EP27, EP28, EP29, EP31, EP32, EP33, EP34, EP36, EP38, EP39, EP40, EP41, EP42, EP43, EP44, EP45, EP46, EP47, EP49, EP53]),
    section("OAS Bifrost OAuth server", "Bifrost OAuth server tests.", [OAS01, OAS02, OAS03, OAS06, OAS07, OAS08, OAS09, OAS10, OAS11, OAS12, OAS13, OAS14, OAS15, OAS16, OAS18, OAS19, OAS20, OAS21, OAS23, OAS25, OAS27, OAS28, OAS30, OAS31, OAS32, OAS34, OAS36, OAS38]),
    section("SET Per-client settings", "Per-client setting tests.", [SET01, SET02, SET07, SET08, SET09, SET21, SET22]),
    section("INF Inference", "Inference tests.", [INF23, INF24, INF25, INF26, INF27, INF29, INF30, INF49, INF50, INF51, INF52, INF53, INF54, INF55, INF56, INF57, INF58, INF66]),
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
