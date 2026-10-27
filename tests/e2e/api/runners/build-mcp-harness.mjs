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

// asMetadata checks the authorization server metadata served for issuer iss.
const asMetadata = (iss) => A.custom("issuer, endpoints and supported values", [
  `var b = pm.response.json(); var iss = ${lit(iss)};`,
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
]);
const jwksOneKey = () => A.custom("one RSA signing key, use sig, alg RS256", [
  "var keys = pm.response.json().keys || [];",
  "pm.expect(keys.length, 'body: ' + pm.response.text()).to.equal(1);",
  "pm.expect(keys[0].kty).to.equal('RSA');",
  "pm.expect(keys[0].use).to.equal('sig');",
  "pm.expect(keys[0].alg).to.equal('RS256');",
]);
const OAS03 = folder("OAS-03 Authorization server metadata", `Setup: mcp_server_auth_mode both, then oauth, each with issuer_url ${OTHER_ISS} (another origin than the requests) and read back from /api/config.`, [
  readConfig("OAS-03"),
  ...["both", "oauth"].flatMap((mode) => [
    setMode("OAS-03", mode, OTHER_ISS),
    request("OAS-03", `${mode}: /api/config reads back mcp_server_auth_mode ${mode}`, "GET", "/api/config", { asserts: [A.status(200), A.custom(`mcp_server_auth_mode is ${lit(mode)}`, [`pm.expect(pm.response.json().client_config.mcp_server_auth_mode, 'body: ' + pm.response.text()).to.equal(${lit(mode)});`])] }),
    request("OAS-03", `${mode}: GET /.well-known/oauth-authorization-server`, "GET", "/.well-known/oauth-authorization-server", { noAuth: true, asserts: [A.status(200), O.noStore(), asMetadata(OTHER_ISS)] }),
    request("OAS-03", `${mode}: GET /.well-known/jwks.json`, "GET", "/.well-known/jwks.json", { noAuth: true, asserts: [A.status(200), O.noStore(), jwksOneKey()] }),
  ]),
  restoreHeaders("OAS-03"),
]);

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
const OAS06 = folder("OAS-06 Issuance endpoints are public", `Setup: ${BOTH}, dashboard auth on; the issuance and discovery requests carry no credentials; the requests run in both and then again in oauth.`, [
  ...withMode("OAS-06", "both", oas06Checks("", "oas06_client")),
  ...withMode("OAS-06", "oauth", oas06Checks("oauth: ", "oas06o_client")),
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
const setEnforce = (id, on) => putConfig(id, `${on ? "setup" : "teardown"}: set enforce_auth_on_inference ${on}`, [`cc.enforce_auth_on_inference = ${on};`], [A.status(200)]);

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

const EP16 = folder("EP-16 \"a-b\" and \"a_b\" stay distinct", `Setup: ${FLEET}; alpha also lists "a_b" and "a-b".`, [
  ...fleetSetupWith("EP-16", { alphaTools: ["echo", "add", "a_b", "a-b"] }),
  rpc("EP-16", "tools/list", listMsg, { asserts: [A.status(200), A.custom("alpha-a_b and alpha-a-b are both listed", [
    "var names = pm.response.json().result.tools.map(function (t) { return t.name; });",
    "pm.expect(names, 'body: ' + pm.response.text()).to.include('alpha-a_b');",
    "pm.expect(names).to.include('alpha-a-b');",
  ])] }),
  rpc("EP-16", "tools/call alpha-a_b", call("alpha-a_b", { q: "x" }), { asserts: [A.status(200)] }),
  mockCalls("EP-16", "alpha", "a_b", 1),
  mockCalls("EP-16", "alpha", "a-b", 0),
  ...fleetTeardown("EP-16"),
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
const OAS32 = folder("OAS-32 Revoke a session", `Setup: ${GRANT_TEXT}; ${BOTH}, temp-token auth on; then the same in mcp_server_auth_mode oauth with a second grant.`, [
  ...fleetGrantSetup("OAS-32"),
  ...oas32Pass("both", "oas32", "", "the grant"),
  ...oas32Pass("oauth", "oas32o", "oauth: ", "the oauth-mode grant"),
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
const OAS34 = folder("OAS-34 Access token expiry", `Setup: ${GRANT_TEXT}; ${BOTH}, temp-token auth on, access_token_ttl 2; then the same in mcp_server_auth_mode oauth with a second grant.`, [
  ...fleetGrantSetup("OAS-34"),
  ...oas34Pass("both", "oas34", ""),
  ...oas34Pass("oauth", "oas34o", "oauth: "),
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

// toolPrice sets tool_pricing with PUT, the only endpoint that takes it. Priced folders clear it again before
// deleting the client, or the next client with the same name inherits the price.
const toolPrice = (id, clientId, title, pricing) => request(id, title, "PUT", `/api/mcp/client/${clientId}`, { body: { tool_pricing: pricing }, asserts: [A.status(200)] });
const ADM14 = folder("ADM-14 Stored fields come back in read encodings", "Setup: admin credentials; alpha (echo_tool, add_tool) as the client's target; the client's pricing is set with PUT.", [
  ...freshServer("ADM-14", "alpha", ["echo_tool", "add_tool"]),
  createClient("ADM-14", httpClient("adm14-c", "adm14_c", "alpha", { headers: { "X-Key": "abcdefghijkl" }, tool_sync_interval: 5, tool_execution_timeout: 30, allow_by_default: true }), "create with headers, tool_sync_interval 5, tool_execution_timeout 30 and allow_by_default true"),
  toolPrice("ADM-14", "adm14-c", "PUT tool_pricing echo_tool 0.25", { echo_tool: 0.25 }),
  getClient("ADM-14", "adm14-c", "read the client back", [A.custom("headers redacted, tool_sync_interval 300000000000, tool_execution_timeout \"30s\", allow_on_all_virtual_keys true, pricing echoed", [
    "var c = pm.response.json().clients[0].config;",
    "pm.expect(c.headers['X-Key'].value, 'body: ' + pm.response.text()).to.equal('abcd' + new Array(25).join('*') + 'ijkl');",
    "pm.expect(c.tool_sync_interval).to.equal(300000000000);",
    "pm.expect(c.tool_execution_timeout).to.equal('30s');",
    "pm.expect(c.allow_on_all_virtual_keys).to.equal(true);",
    "pm.expect(c.tool_pricing).to.eql({ echo_tool: 0.25 });",
  ])]),
  toolPrice("ADM-14", "adm14-c", "teardown: clear tool_pricing", {}),
  deleteClient("ADM-14", "adm14-c"),
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

const ADM47 = folder("ADM-47 PUT is a patch and ignores immutable fields", "Setup: alpha (echo_tool, add_tool); a client on alpha with headers and tool lists, then pricing set with PUT.", [
  ...freshServer("ADM-47", "alpha", ["echo_tool", "add_tool"]),
  createClient("ADM-47", httpClient("adm47-c", "adm47_c", "alpha", { headers: { "X-Key": "abcdefghijkl" }, tools_to_execute: ["*"], tools_to_auto_execute: ["echo_tool"] }), "setup: create the client with headers and tool lists"),
  toolPrice("ADM-47", "adm47-c", "setup: PUT tool_pricing echo_tool 0.5", { echo_tool: 0.5 }),
  request("ADM-47", "setup: read the client", "GET", "/api/mcp/clients?server=adm47-c", { asserts: [A.status(200)], extraTests: ["pm.collectionVariables.set('adm47_before', JSON.stringify(pm.response.json().clients[0].config));"] }),
  request("ADM-47", "PUT name, connection_string, auth_type and endpoint_slug", "PUT", "/api/mcp/client/adm47-c", {
    body: { name: "adm47_renamed", connection_string: "http://127.0.0.1:9/mcp", auth_type: "headers", endpoint_slug: "adm47-other" },
    asserts: [A.status(200), A.jsonMessage("MCP client edited successfully")],
  }),
  getClient("ADM-47", "adm47-c", "only the name changed and the tools are still listed", [A.custom("config equals the earlier read apart from name", [
    "var c = pm.response.json().clients[0].config, before = JSON.parse(pm.collectionVariables.get('adm47_before'));",
    "pm.expect(c.name, 'body: ' + pm.response.text()).to.equal('adm47_renamed');",
    "delete c.name; delete before.name;",
    "pm.expect(c).to.eql(before);",
  ]), toolNamesAre(["add_tool", "echo_tool"])]),
  toolPrice("ADM-47", "adm47-c", "teardown: clear tool_pricing", {}),
  deleteClient("ADM-47", "adm47-c"),
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
  getClient("ADM-66", "adm66-h", "keys [\"x-key\"], healthy, 2 tools, an active headers credential for x-key", [clientField("config.per_user_header_keys", ["x-key"]), clientState("healthy"), toolNamesAre(["add_tool", "echo_tool"]),
    A.custom("credential kind \"headers\", status \"active\", header_keys [\"x-key\"], and no value", [
      "var cr = pm.response.json().clients[0].credential || {};",
      "pm.expect(cr.kind, 'body: ' + pm.response.text()).to.equal('headers');",
      "pm.expect(cr.status).to.equal('active');",
      "pm.expect(cr.header_keys).to.eql(['x-key']);",
      "pm.expect(JSON.stringify(cr)).to.not.include('good');",
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
  getClient("AUTH-45", "auth45-c", "control: the create already kept an active headers credential", [A.custom("credential kind \"headers\", status \"active\"", [
    "var cr = pm.response.json().clients[0].credential || {};",
    "pm.expect(cr.kind, 'body: ' + pm.response.text()).to.equal('headers');",
    "pm.expect(cr.status).to.equal('active');",
  ])], { extraTests: ["pm.collectionVariables.set('auth45_cred_at', (pm.response.json().clients[0].credential || {}).updated_at || '');"] }),
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
  ]), A.custom("verify-headers rewrote it: updated_at is later than after the create", [
    "var before = pm.collectionVariables.get('auth45_cred_at');",
    "var after = pm.response.json().clients[0].credential.updated_at;",
    "pm.expect(Date.parse(after), 'before ' + before + ', after ' + after).to.be.above(Date.parse(before));",
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
// grantWith is grantFor for a VK kept in any variable: the consent sends {{vkVarName}}.
const grantWith = (id, clientVar, flow, vkVarName, label = "the VK") => [
  authorize(id, clientVar, flow, `setup: authorize a grant for ${label}`),
  consent(id, flow, `setup: consent with ${label}`, "temp", { mode: "vk", value: `{{${vkVarName}}}` }, [A.status(200), codeIssued()], { extraTests: saveCode(flow) }),
  tokenReq(id, `setup: exchange the code for ${label}'s token`, codeParams(flow, clientVar), [A.status(200), tokensIssued()], { extraTests: saveTokens(flow) }),
];
const ACCESS_DENIED = [A.status(403), A.msgEquals("access_denied")];
const OAUTH_MODE_MSG = "this server requires OAuth JWT authentication; header credentials are not accepted in oauth mode";
const NO_VK_MSG = "virtual key required to access mcp server";

// getMcp sends GET (the SSE stream request) to path with no admin credentials. Only refusals are sent this way:
// an admitted GET opens a stream that never ends.
const getMcp = (id, title, path, headers, asserts) => request(id, title, "GET", path, { noAuth: true, headers: [{ key: "Accept", value: "text/event-stream" }, ...headers], asserts });
const EP34 = folder("EP-34 GET refusals", "Setup: GET with Accept: text/event-stream and no admin credentials; a VK with no MCP configs. mcp_server_auth_mode oauth with no token and then with the VK in x-bf-vk; headers and then both with enforce_auth_on_inference on and no credential; /mcp/nope in headers and then both with enforce_auth_on_inference off.", [
  readConfig("EP-34"),
  vkCreate("EP-34", "setup: create a VK with no MCP configs", { name: "EP-34 key" }, "ep34_vk"),
  authMode("EP-34", "O", false),
  request("EP-34", "GET /mcp in oauth mode with no token", "GET", "/mcp", { noAuth: true, headers: [{ key: "Accept", value: "text/event-stream" }], asserts: [A.status(401), wwwPresent(), jsonNotStream()] }),
  getMcp("EP-34", "GET /mcp in oauth mode with the VK in x-bf-vk", "/mcp", [{ key: "x-bf-vk", value: "{{ep34_vk}}" }], [A.status(401), A.msgEquals(OAUTH_MODE_MSG), wwwPresent(), jsonNotStream()]),
  authMode("EP-34", "H", true),
  getMcp("EP-34", "headers, enforce_auth_on_inference on: GET /mcp with no credential", "/mcp", [], [A.status(401), A.msgContains(NO_VK_MSG), noWww(), jsonNotStream()]),
  authMode("EP-34", "B", true),
  list("EP-34", "control: both, enforce_auth_on_inference on: POST /mcp with the VK in x-bf-vk", "/mcp", [{ key: "x-bf-vk", value: "{{ep34_vk}}" }], [A.status(200)]),
  getMcp("EP-34", "both, enforce_auth_on_inference on: GET /mcp with no credential", "/mcp", [], [A.status(401), A.msgContains(NO_VK_MSG), wwwPresent(), jsonNotStream()]),
  authMode("EP-34", "H", false),
  request("EP-34", "GET /mcp/nope", "GET", "/mcp/nope", { noAuth: true, headers: [{ key: "Accept", value: "text/event-stream" }], asserts: [...ACCESS_DENIED, jsonNotStream()] }),
  authMode("EP-34", "B", false),
  getMcp("EP-34", "both: GET /mcp/nope", "/mcp/nope", [], [...ACCESS_DENIED, jsonNotStream()]),
  restoreConfig("EP-34"),
  vkDelete("EP-34", "teardown: delete the VK", "ep34_vk"),
]);

const EP36 = folder("EP-36 Client slug gated by grant and state", `Setup: ${FLEET}; the echo-only VK (${VKS.echo.text}) and the full-access VK (${VKS.full.text}); alpha is disabled later; each check runs in mcp_server_auth_mode headers and in both, enforce_auth_on_inference off.`, [
  ...fleetSetup("EP-36"), createVK("EP-36", "echo"), createVK("EP-36", "full"),
  readConfig("EP-36"),
  ...["H", "B"].flatMap((m) => [
    authMode("EP-36", m, false),
    list("EP-36", `${modePrefix(m)}/mcp/beta with the echo-only VK`, "/mcp/beta", [vkHeader("EP-36", "echo")], ACCESS_DENIED),
    list("EP-36", `${modePrefix(m)}/mcp/beta with the full-access VK`, "/mcp/beta", [vkHeader("EP-36", "full")], [A.status(200), toolNamesInList(["beta-echo"])]),
    list("EP-36", `${modePrefix(m)}control: /mcp/alpha with the full-access VK before the disable`, "/mcp/alpha", [vkHeader("EP-36", "full")], [A.status(200), toolNamesInList(["alpha-add", "alpha-echo"])]),
  ]),
  request("EP-36", "setup: disable alpha", "PUT", "/api/mcp/client/ep-36-alpha", { body: { disabled: true }, asserts: [A.status(200)] }),
  ...["B", "H"].flatMap((m, i) => [
    ...(i > 0 ? [authMode("EP-36", m, false)] : []),
    list("EP-36", `${modePrefix(m)}/mcp/alpha with the full-access VK after the disable`, "/mcp/alpha", [vkHeader("EP-36", "full")], [A.status(403)]),
  ]),
  restoreConfig("EP-36"),
  deleteVK("EP-36", "echo"), deleteVK("EP-36", "full"),
  ...fleetTeardown("EP-36"),
]);

const EP38 = folder("EP-38 Virtual MCP slug refusals", `Setup: ${FLEET}; the full-access VK (${VKS.full.text}) and the echo-only VK (${VKS.echo.text}); ${TEAM_TEXT}; mcp_server_auth_mode headers, and each check again in both, enforce_auth_on_inference off.`, [
  readConfig("EP-38"), authMode("EP-38", "H", false),
  ...fleetSetup("EP-38"), createVK("EP-38", "full"), createVK("EP-38", "echo"), ...teamSetup("EP-38"),
  ...["H", "B"].flatMap((m) => [
    ...(m === "B" ? [authMode("EP-38", m, false)] : []),
    list("EP-38", `${modePrefix(m)}/mcp/team anonymously`, "/mcp/team", [], ACCESS_DENIED),
    list("EP-38", `${modePrefix(m)}/mcp/team with the echo-only VK, which team is not attached to`, "/mcp/team", [vkHeader("EP-38", "echo")], ACCESS_DENIED),
    list("EP-38", `${modePrefix(m)}control: /mcp/team with the full-access VK`, "/mcp/team", [vkHeader("EP-38", "full")], [A.status(200), toolNamesInList(["alpha-echo", "beta-echo"])]),
  ]),
  request("EP-38", "setup: disable team", "PUT", "/api/mcp/virtual-mcps/{{ep-38_team_id}}", { body: { enabled: false }, asserts: [A.status(200)] }),
  ...["B", "H"].flatMap((m, i) => [
    ...(i > 0 ? [authMode("EP-38", m, false)] : []),
    list("EP-38", `${modePrefix(m)}/mcp/team with the full-access VK after the disable`, "/mcp/team", [vkHeader("EP-38", "full")], ACCESS_DENIED),
    list("EP-38", `${modePrefix(m)}/mcp/nope with the full-access VK`, "/mcp/nope", [vkHeader("EP-38", "full")], ACCESS_DENIED),
  ]),
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
// EP40_PAIRS are adjacent carriers in the documented order (x-bf-vk, Bearer, x-api-key, x-goog-api-key, api-key), higher first.
// The higher one carries the echo-only VK and must win in either header order; today the request context keeps the last VK
// header sent, so the higher-first order is a known issue.
const EP40_PAIRS = [[0, 1], [1, 3], [3, 4], [4, 5]].map(([h, l]) => [EP40_CARRIERS[h], EP40_CARRIERS[l]]);
const EP40 = folder("EP-40 VK header carriers", `Setup: ${FLEET}; the full-access VK (${VKS.full.text}) and the echo-only VK (${VKS.echo.text}); enforce_auth_on_inference on, in mcp_server_auth_mode headers and then both. Then each pair of adjacent carriers in the order x-bf-vk, Authorization: Bearer, x-api-key, x-goog-api-key, api-key carries the echo-only VK in the higher one and the full-access VK in the lower one, sent in both header orders.`, [
  readConfig("EP-40"), ...fleetSetup("EP-40"), createVK("EP-40", "full"), createVK("EP-40", "echo"),
  ...["H", "B"].flatMap((m) => [
    authMode("EP-40", m, true),
    noCredControl("EP-40", `${MODE_NAMES[m]}: `),
    ...EP40_CARRIERS.map(([label, header]) => list("EP-40", `${MODE_NAMES[m]}: the VK in ${label}`, "/mcp", [header(vkVar("EP-40", "full"))], [A.status(200), toolNamesInList(ALL_TOOLS)])),
    ...EP40_PAIRS.flatMap(([[hi, hiH], [lo, loH]]) => [
      list("EP-40", `${MODE_NAMES[m]}: ${hi} (the echo-only VK) sent before ${lo} (the full-access VK)`, "/mcp", [hiH(vkVar("EP-40", "echo")), loH(vkVar("EP-40", "full"))], [A.status(200), A.skip(toolNamesInList(["alpha-echo"]))]),
      list("EP-40", `${MODE_NAMES[m]}: ${lo} (the full-access VK) sent before ${hi} (the echo-only VK)`, "/mcp", [loH(vkVar("EP-40", "full")), hiH(vkVar("EP-40", "echo"))], [A.status(200), toolNamesInList(["alpha-echo"])]),
    ]),
  ]),
  restoreConfig("EP-40"), deleteVK("EP-40", "full"), deleteVK("EP-40", "echo"), ...fleetTeardown("EP-40"),
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
  list("EP-42", "control: both mode, enforcement on: the VK in x-api-key", "/mcp", [{ key: "x-api-key", value: vkVar("EP-42", "full") }], [A.status(200), toolNamesInList(ALL_TOOLS)]),
  authMode("EP-42", "O", false),
  list("EP-42", "the VK in x-bf-vk", "/mcp", [vkHeader("EP-42", "full")], [A.status(401), A.msgEquals(OAUTH_MODE_MSG), wwwPresent()]),
  list("EP-42", "the VK as a Bearer token", "/mcp", [bearer(vkVar("EP-42", "full"))], [A.status(401), A.msgEquals(OAUTH_MODE_MSG), wwwPresent()]),
  list("EP-42", "the VK in x-api-key", "/mcp", [{ key: "x-api-key", value: vkVar("EP-42", "full") }], [A.status(401), A.msgEquals(OAUTH_MODE_MSG), wwwPresent()]),
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
    rpc("EP-43", `${MODE_NAMES[m]}: an unknown VK`, listMsg, { headers: [MCP_ACCEPT, { key: "x-bf-vk", value: "sk-bf-ep43-no-such-key" }], asserts: [A.status(401), A.msgEquals("access not found. The provided credential does not exist or has been revoked."), m === "H" ? noWww() : wwwPresent()] }),
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
const EP45 = folder("EP-45 JWT plus a VK header", `Setup: ${FLEET}; the full-access VK (${VKS.full.text}); ${BOTH} and then oauth, temp-token auth on, enforce_auth_on_inference off; a token from a grant consented with that VK.`, [
  readConfig("EP-45"), ...fleetSetup("EP-45"), createVK("EP-45", "full"), authMode("EP-45", "B", false),
  register("EP-45", "ep45_client", LOOPBACK_CB, undefined, REFRESHABLE),
  ...grantFor("EP-45", "ep45_client", "ep45_g", "full"),
  ...["B", "O"].flatMap((m) => {
    const p = m === "O" ? "oauth: " : "";
    const conflict = [A.status(401), A.msgEquals(CONFLICT_MSG), wwwPresent()];
    return [
      ...(m === "O" ? [authMode("EP-45", m, false)] : []),
      list("EP-45", `${p}control: the token alone`, "/mcp", [bearer("{{ep45_g_at}}")], [A.status(200)]),
      list("EP-45", `${p}the token plus x-bf-vk`, "/mcp", [bearer("{{ep45_g_at}}"), vkHeader("EP-45", "full")], conflict),
      list("EP-45", `${p}the token plus x-api-key`, "/mcp", [bearer("{{ep45_g_at}}"), { key: "x-api-key", value: vkVar("EP-45", "full") }], conflict),
    ];
  }),
  restoreConfig("EP-45"), deleteVK("EP-45", "full"), ...fleetTeardown("EP-45"),
]);

const NO_VK_401 = [A.status(401), A.msgContains("virtual key required to access mcp server"), wwwPresent()];
const EP46 = folder("EP-46 Non-credentials do not open /mcp", `Setup: ${BOTH}, temp-token auth on, enforce_auth_on_inference on; a registered client's authorize gives a temp token. Each request carries one of: that temp token, the admin's Basic credentials, x-bf-mcp-session-id, the setup token. The four requests run in both, then in oauth, then in headers (the temp token kept from both).`, [
  readConfig("EP-46"), authMode("EP-46", "B", true),
  register("EP-46", "ep46_client"),
  authorize("EP-46", "ep46_client", "ep46_f", "setup: authorize to get a temp token"),
  flowGet("EP-46", "ep46_f", "control: the temp token opens its own flow", "temp", [A.status(200)]),
  ...[
    ["B", NO_VK_401],
    ["O", [A.status(401), A.msgEquals(OAUTH_MODE_MSG), wwwPresent()]],
    ["H", [A.status(401), A.msgContains(NO_VK_MSG), noWww()]],
  ].flatMap(([m, refused]) => {
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

const EP47 = folder("EP-47 VK JWT matches the VK header", `Setup: ${FLEET}; the full-access VK (${VKS.full.text}) and the echo-only VK (${VKS.echo.text}); ${TEAM_TEXT}; ${BOTH} and then oauth, temp-token auth on, enforce_auth_on_inference on; one registered client gets a grant consented with each VK.`, [
  readConfig("EP-47"), ...fleetSetup("EP-47"), createVK("EP-47", "full"), createVK("EP-47", "echo"), ...teamSetup("EP-47"), authMode("EP-47", "B", true),
  register("EP-47", "ep47_client", LOOPBACK_CB, undefined, REFRESHABLE),
  ...grantFor("EP-47", "ep47_client", "ep47_r", "echo"),
  ...grantFor("EP-47", "ep47_client", "ep47_a", "full"),
  ...["B", "O"].flatMap((m) => {
    const p = m === "O" ? "oauth: " : "";
    return [
      ...(m === "O" ? [authMode("EP-47", m, true)] : []),
      list("EP-47", `${p}the echo-only VK's token lists`, "/mcp", [bearer("{{ep47_r_at}}")], [A.status(200), toolNamesInList(["alpha-echo"])]),
      rpc("EP-47", `${p}the echo-only VK's token calls alpha-add`, call("alpha-add", { a: 1, b: 2 }), { headers: [MCP_ACCEPT, bearer("{{ep47_r_at}}")], asserts: [A.status(200), toolResultError("Tool execution failed: MCP tool 'alpha-add' is not allowed", null)] }),
      mockGet("EP-47", "alpha", `${p}alpha received no tool call`, [A.custom("calls is empty", ["pm.expect(pm.response.json().counters.calls, 'body: ' + pm.response.text()).to.eql({});"])]),
      list("EP-47", `${p}the full-access VK's token lists /mcp/team`, "/mcp/team", [bearer("{{ep47_a_at}}")], [A.status(200), toolNamesInList(["alpha-echo", "beta-echo"])]),
    ];
  }),
  restoreConfig("EP-47"), teamTeardown("EP-47"), deleteVK("EP-47", "full"), deleteVK("EP-47", "echo"), ...fleetTeardown("EP-47"),
]);

const EP49 = folder("EP-49 Session-mode JWT", `Setup: ${FLEET}; the full-access VK (${VKS.full.text}); ${TEAM_TEXT}; ${BOTH}, temp-token auth on, enforce_auth_on_inference off; a session-mode token as in OAS-20 (consent by the admin with Bearer base64(admin:pw)); enforce_auth_on_inference on for the last case. The checks then repeat in oauth, with a token from a grant consented with the full-access VK as the /mcp/team control.`, [
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
  ...grantFor("EP-49", "ep49_client", "ep49_a", "full"),
  authMode("EP-49", "O", false),
  list("EP-49", "oauth: the session token lists the anonymous tools", "/mcp", [bearer("{{ep49_s_at}}")], [A.status(200), toolNamesInList(ALL_TOOLS)]),
  list("EP-49", "oauth: control: /mcp/team with the full-access VK's token", "/mcp/team", [bearer("{{ep49_a_at}}")], [A.status(200)]),
  list("EP-49", "oauth: the session token on /mcp/team", "/mcp/team", [bearer("{{ep49_s_at}}")], [A.status(403)]),
  authMode("EP-49", "O", true),
  list("EP-49", "oauth: the session token with enforce_auth_on_inference on", "/mcp", [bearer("{{ep49_s_at}}")], [A.status(401), A.msgContains("session-mode tokens are not accepted when authentication is enforced")]),
  revokeGrants("EP-49", "ep49_client"),
  restoreConfig("EP-49"), teamTeardown("EP-49"), deleteVK("EP-49", "full"), ...fleetTeardown("EP-49"),
]);

// EP-12, EP-14 and EP-24 sit below the OAuth helpers because their both and oauth passes use them.
// EP-12 runs once per mode that accepts header credentials; both adds WWW-Authenticate to the no-credential refusal.
const EP12 = folder("EP-12 tools/list per VK", `Setup: ${FLEET}; mcp_server_auth_mode headers and then both, enforce_auth_on_inference on; ${VK_TEXT}.`, [
  ...fleetSetup("EP-12"),
  createVK("EP-12", "full"), createVK("EP-12", "echo"), createVK("EP-12", "none"),
  readConfig("EP-12"),
  ...["H", "B"].flatMap((m) => {
    const p = modePrefix(m);
    return [
      authMode("EP-12", m, true),
      m === "B" ? list("EP-12", `${p}control: no credential is refused`, "/mcp", [], NO_VK_401)
        : list("EP-12", "control: no credential is refused", "/mcp", [], [A.status(401), A.msgContains(NO_VK_MSG), noWww()]),
      rpc("EP-12", `${p}tools/list with the full-access VK`, listMsg, { headers: [MCP_ACCEPT, vkHeader("EP-12", "full")], asserts: [A.status(200), toolNamesInList(["alpha-add", "alpha-echo", "beta-echo"])] }),
      rpc("EP-12", `${p}tools/list with the echo-only VK`, listMsg, { headers: [MCP_ACCEPT, vkHeader("EP-12", "echo")], asserts: [A.status(200), toolNamesInList(["alpha-echo"])] }),
      rpc("EP-12", `${p}tools/list with the no-grant VK`, listMsg, { headers: [MCP_ACCEPT, vkHeader("EP-12", "none")], asserts: [A.status(200), toolNamesInList([])] }),
    ];
  }),
  restoreConfig("EP-12"),
  deleteVK("EP-12", "full"), deleteVK("EP-12", "echo"), deleteVK("EP-12", "none"),
  ...fleetTeardown("EP-12"),
]);

const EP14 = folder("EP-14 Include headers on /mcp", `Setup: ${FLEET}; an anonymous caller, and the echo-only VK (${VKS.echo.text}); mcp_server_auth_mode headers and then both, enforce_auth_on_inference off; then, in both and in oauth, a token from a grant consented with the echo-only VK.`, [
  ...fleetSetup("EP-14"),
  createVK("EP-14", "echo"),
  readConfig("EP-14"),
  ...["H", "B"].flatMap((m) => {
    const p = modePrefix(m);
    return [
      authMode("EP-14", m, false),
      rpc("EP-14", `${p}include-clients beta`, listMsg, { headers: [MCP_ACCEPT, inc("clients", "beta")], asserts: [A.status(200), toolNamesInList(["beta-echo"])] }),
      rpc("EP-14", `${p}include-tools alpha-echo`, listMsg, { headers: [MCP_ACCEPT, inc("tools", "alpha-echo")], asserts: [A.status(200), toolNamesInList(["alpha-echo"])] }),
      rpc("EP-14", `${p}include-clients empty`, listMsg, { headers: [MCP_ACCEPT, inc("clients", "")], asserts: [A.status(200), toolNamesInList([])] }),
      rpc("EP-14", `${p}control: the echo-only VK with include-tools alpha-echo`, listMsg, { headers: [MCP_ACCEPT, vkHeader("EP-14", "echo"), inc("tools", "alpha-echo")], asserts: [A.status(200), toolNamesInList(["alpha-echo"])] }),
      rpc("EP-14", `${p}the echo-only VK with include-tools beta-echo`, listMsg, { headers: [MCP_ACCEPT, vkHeader("EP-14", "echo"), inc("tools", "beta-echo")], asserts: [A.status(200), toolNamesInList([])] }),
    ];
  }),
  register("EP-14", "ep14_client"),
  ...grantFor("EP-14", "ep14_client", "ep14_r", "echo"),
  ...["B", "O"].flatMap((m) => [
    ...(m === "O" ? [authMode("EP-14", m, false)] : []),
    list("EP-14", `${modePrefix(m)}control: the echo-only VK's token with include-tools alpha-echo`, "/mcp", [bearer("{{ep14_r_at}}"), inc("tools", "alpha-echo")], [A.status(200), toolNamesInList(["alpha-echo"])]),
    list("EP-14", `${modePrefix(m)}the echo-only VK's token with include-tools beta-echo`, "/mcp", [bearer("{{ep14_r_at}}"), inc("tools", "beta-echo")], [A.status(200), toolNamesInList([])]),
  ]),
  restoreConfig("EP-14"),
  deleteVK("EP-14", "echo"),
  ...fleetTeardown("EP-14"),
]);

const EP24 = folder("EP-24 Listed but not granted", `Setup: ${FLEET}; the echo-only VK (${VKS.echo.text}) calls alpha-add, the no-grant VK (${VKS.none.text}) calls alpha-echo; mcp_server_auth_mode headers and then both, enforce_auth_on_inference off.`, [
  ...fleetSetup("EP-24"),
  createVK("EP-24", "echo"), createVK("EP-24", "none"),
  readConfig("EP-24"),
  ...["H", "B"].flatMap((m, pass) => {
    const p = modePrefix(m);
    return [
      authMode("EP-24", m, false),
      rpc("EP-24", `${p}control: the echo-only VK calls alpha-echo`, call("alpha-echo", { text: "hi" }), { headers: [MCP_ACCEPT, vkHeader("EP-24", "echo")], asserts: [A.status(200), A.custom("isError is not set and the content is the echo", ["var r = pm.response.json().result;", "pm.expect(r && r.isError, 'body: ' + pm.response.text()).to.not.equal(true);", "pm.expect(r.content).to.eql([{ type: 'text', text: 'echo:{\"text\":\"hi\"}' }]);"])] }),
      rpc("EP-24", `${p}the echo-only VK calls alpha-add`, call("alpha-add", { a: 1, b: 2 }), { headers: [MCP_ACCEPT, vkHeader("EP-24", "echo")], asserts: [A.status(200), toolResultError("Tool execution failed: MCP tool 'alpha-add' is not allowed", null)] }),
      rpc("EP-24", `${p}the no-grant VK calls alpha-echo`, call("alpha-echo", { text: "hi" }), { headers: [MCP_ACCEPT, vkHeader("EP-24", "none")], asserts: [A.status(200), toolResultError("Tool execution failed: MCP tool 'alpha-echo' is not allowed", null)] }),
      mockGet("EP-24", "alpha", `${p}alpha ran only the control call${pass ? "s" : ""}`, [A.custom(`calls is {echo: ${pass + 1}}`, [`pm.expect(pm.response.json().counters.calls, 'body: ' + pm.response.text()).to.eql({ echo: ${pass + 1} });`])]),
    ];
  }),
  restoreConfig("EP-24"),
  deleteVK("EP-24", "echo"), deleteVK("EP-24", "none"),
  ...fleetTeardown("EP-24"),
]);

// EP-48 checks a token whose key changes after it was issued: deactivated, then deleted.
const EP48_GONE = "virtual key not found or inactive";
const EP48 = folder("EP-48 VK changed after the JWT was issued", `Setup: ${FLEET}; the full-access VK (${VKS.full.text}); ${BOTH}, temp-token auth on, enforce_auth_on_inference off; a token from a grant consented with that VK. The VK is then deactivated, and then deleted; each state is checked in both and in oauth.`, [
  readConfig("EP-48"), ...fleetSetup("EP-48"), createVK("EP-48", "full"), authMode("EP-48", "B", false),
  register("EP-48", "ep48_client"),
  ...grantFor("EP-48", "ep48_client", "ep48_g", "full"),
  list("EP-48", "control: the token lists every tool", "/mcp", [bearer("{{ep48_g_at}}")], [A.status(200), toolNamesInList(ALL_TOOLS)]),
  request("EP-48", "setup: deactivate the VK", "PUT", "/api/governance/virtual-keys/{{ep-48_full_id}}", { body: { is_active: false }, asserts: [A.status(200)] }),
  list("EP-48", "the token after the deactivation", "/mcp", [bearer("{{ep48_g_at}}")], [A.status(403), A.msgEquals("virtual key is inactive")]),
  authMode("EP-48", "O", false),
  list("EP-48", "oauth: the token after the deactivation", "/mcp", [bearer("{{ep48_g_at}}")], [A.status(403), A.msgEquals("virtual key is inactive")]),
  request("EP-48", "setup: delete the VK", "DELETE", "/api/governance/virtual-keys/{{ep-48_full_id}}", { asserts: [A.status(200)] }),
  list("EP-48", "oauth: the token after the delete", "/mcp", [bearer("{{ep48_g_at}}")], [A.status(401), A.msgEquals(EP48_GONE), wwwPresent()]),
  authMode("EP-48", "B", false),
  list("EP-48", "the token after the delete", "/mcp", [bearer("{{ep48_g_at}}")], [A.status(401), A.msgEquals(EP48_GONE), wwwPresent()]),
  restoreConfig("EP-48"), ...fleetTeardown("EP-48"),
]);

// EP-54 checks keys that are not virtual keys in the VK headers. Bearer, x-api-key, x-goog-api-key and api-key carry a VK only
// with the sk-bf- prefix, so a provider key there is no credential at all; x-bf-vk is taken as written and looked up.
const EP54_PROVIDER_KEY = "sk-ant-ep54-provider-key";
const EP54_CARRIERS = [
  ["Authorization Bearer", (v) => bearer(v)],
  ["x-api-key", (v) => ({ key: "x-api-key", value: v })],
  ["x-goog-api-key", (v) => ({ key: "x-goog-api-key", value: v })],
  ["api-key", (v) => ({ key: "api-key", value: v })],
];
const EP54_UNKNOWN = "access not found. The provided credential does not exist or has been revoked.";
const EP54 = folder("EP-54 Non-VK keys in VK headers", `Setup: ${FLEET}; a provider key "${EP54_PROVIDER_KEY}" without the sk-bf- prefix. mcp_server_auth_mode headers and then both, each with enforce_auth_on_inference off and then on; then oauth.`, [
  readConfig("EP-54"), ...fleetSetup("EP-54"),
  ...["H", "B"].flatMap((m) => {
    const p = `${MODE_NAMES[m]}: `;
    const www = m === "H" ? noWww() : wwwPresent();
    return [
      authMode("EP-54", m, false),
      ...EP54_CARRIERS.map(([name, h]) => list("EP-54", `${p}enforcement off: the provider key in ${name} is anonymous`, "/mcp", [h(EP54_PROVIDER_KEY)], [A.status(200), toolNamesInList(ALL_TOOLS)])),
      list("EP-54", `${p}enforcement off: x-bf-vk without the sk-bf- prefix is looked up as a VK`, "/mcp", [{ key: "x-bf-vk", value: "ep54-not-a-key" }], [A.status(401), A.msgEquals(EP54_UNKNOWN), www]),
      authMode("EP-54", m, true),
      ...EP54_CARRIERS.map(([name, h]) => list("EP-54", `${p}enforcement on: the provider key in ${name} is refused`, "/mcp", [h(EP54_PROVIDER_KEY)], [A.status(401), A.msgContains(NO_VK_MSG), www])),
    ];
  }),
  authMode("EP-54", "O", false),
  list("EP-54", "oauth: the provider key as a Bearer token", "/mcp", [bearer(EP54_PROVIDER_KEY)], [A.status(401), A.msgEquals(OAUTH_MODE_MSG), wwwPresent()]),
  list("EP-54", "oauth: the provider key in x-api-key", "/mcp", [{ key: "x-api-key", value: EP54_PROVIDER_KEY }], [A.status(401), A.msgEquals(OAUTH_MODE_MSG), wwwPresent()]),
  restoreConfig("EP-54"), ...fleetTeardown("EP-54"),
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

// ================================================================ GOV client grants, VK refusals and virtual MCP admin

const GOV06 = folder("GOV-06 Legacy name allow_on_all_virtual_keys", `Setup: ${ALPHA_GREET_TEXT}; the client is updated with the legacy field name, and two more clients on alpha are created with it.`, [
  ...alphaGreetSetup("GOV-06"),
  request("GOV-06", "PUT allow_on_all_virtual_keys true", "PUT", "/api/mcp/client/gov-06-alpha", { body: { allow_on_all_virtual_keys: true }, asserts: [A.status(200)] }),
  getClient("GOV-06", "gov-06-alpha", "true sets both fields", [clientField("config.allow_by_default", true), clientField("config.allow_on_all_virtual_keys", true)]),
  request("GOV-06", "PUT allow_by_default false with allow_on_all_virtual_keys true", "PUT", "/api/mcp/client/gov-06-alpha", { body: { allow_by_default: false, allow_on_all_virtual_keys: true }, asserts: [A.status(200)] }),
  getClient("GOV-06", "gov-06-alpha", "false wins", [clientField("config.allow_by_default", false), clientField("config.allow_on_all_virtual_keys", false)]),
  createClient("GOV-06", httpClient("gov-06-c2", "gov06_c2", "alpha", { allow_on_all_virtual_keys: true }), "create with allow_on_all_virtual_keys true"),
  getClient("GOV-06", "gov-06-c2", "on create, true sets both fields", [clientField("config.allow_by_default", true), clientField("config.allow_on_all_virtual_keys", true)]),
  createClient("GOV-06", httpClient("gov-06-c3", "gov06_c3", "alpha", { allow_by_default: false, allow_on_all_virtual_keys: true }), "create with allow_by_default false and allow_on_all_virtual_keys true"),
  getClient("GOV-06", "gov-06-c3", "on create, false wins", [clientField("config.allow_by_default", false), clientField("config.allow_on_all_virtual_keys", false)]),
  deleteClient("GOV-06", "gov-06-c2"), deleteClient("GOV-06", "gov-06-c3"),
  alphaGreetTeardown("GOV-06"),
]);

const GOV11_VK = [{ key: "x-bf-vk", value: "{{gov11_vk}}" }];
const GOV11 = folder("GOV-11 Grants edited from the client side", `Setup: ${ALPHA_GREET_TEXT}; a VK with no MCP configs; grants set through the client's vk_configs.`, [
  ...alphaGreetSetup("GOV-11"),
  vkCreate("GOV-11", "setup: create a VK with no MCP configs", { name: "GOV-11 client-side VK" }, "gov11_vk"),
  execBody("GOV-11", "control: before the edit the VK cannot run alpha-add", toolCall("alpha-add", "{}"), [A.status(403)], { headers: GOV11_VK }),
  request("GOV-11", "PUT vk_configs granting the VK [\"add\"]", "PUT", "/api/mcp/client/gov-11-alpha", { body: { vk_configs: [{ virtual_key_id: "{{gov11_vk_id}}", tools_to_execute: ["add"] }] }, asserts: [A.status(200)] }),
  execBody("GOV-11", "the VK runs alpha-add", toolCall("alpha-add", "{}"), [A.status(200)], { headers: GOV11_VK }),
  execBody("GOV-11", "the VK runs alpha-echo", { id: "c2", type: "function", function: { name: "alpha-echo", arguments: "{}" } }, [A.status(403)], { headers: GOV11_VK }),
  request("GOV-11", "PUT vk_configs with an empty virtual_key_id", "PUT", "/api/mcp/client/gov-11-alpha", { body: { vk_configs: [{ virtual_key_id: "", tools_to_execute: ["add"] }] }, asserts: [A.status(400)] }),
  request("GOV-11", "PUT vk_configs with a repeated virtual_key_id", "PUT", "/api/mcp/client/gov-11-alpha", { body: { vk_configs: [{ virtual_key_id: "{{gov11_vk_id}}", tools_to_execute: ["add"] }, { virtual_key_id: "{{gov11_vk_id}}", tools_to_execute: ["echo"] }] }, asserts: [A.status(400)] }),
  vkDelete("GOV-11", "teardown: delete the VK", "gov11_vk"),
  alphaGreetTeardown("GOV-11"),
]);

const UNKNOWN_VK = "sk-bf-doesnotexist";
const GOV16 = folder("GOV-16 Unknown VK value", `Setup: ${ALPHA_GREET_TEXT}; the caller sends x-bf-vk ${UNKNOWN_VK}.`, [
  ...alphaGreetSetup("GOV-16"),
  execBody("GOV-16", "execute alpha-echo with the unknown VK", ECHO_CALL, [A.status(401), A.jsonEquals("type", "access_not_found"),
    A.msgEquals("access not found. The provided credential does not exist or has been revoked.")], { headers: [{ key: "x-bf-vk", value: UNKNOWN_VK }] }),
  rpc("GOV-16", "/mcp initialize with the unknown VK", init("2025-03-26"), { headers: [MCP_ACCEPT, { key: "x-bf-vk", value: UNKNOWN_VK }], asserts: [A.status(401)] }),
  alphaGreetTeardown("GOV-16"),
]);

const GOV17 = folder("GOV-17 VK required", `Setup: ${ALPHA_GREET_TEXT}; enforce_auth_on_inference true; the caller sends no credential.`, [
  ...alphaGreetSetup("GOV-17"), readConfig("GOV-17"), setEnforce("GOV-17", true),
  execBody("GOV-17", "execute alpha-echo with no credential", ECHO_CALL, [A.status(401), A.jsonEquals("type", "virtual_key_required"), A.msgEquals("virtual key is required. Provide a virtual key via the x-bf-vk header.")]),
  restoreConfig("GOV-17"),
  alphaGreetTeardown("GOV-17"),
]);

// V is the virtual MCP "Tools A" (slug tools-a) over alpha's echo, keyed by the client's client_id.
const V_BODY = (id) => ({ name: "Tools A", tools: [{ mcp_client_id: `${id.toLowerCase()}-alpha`, tool_names: ["echo"] }] });
const vmcpPath = (varName, rest = "") => `/api/mcp/virtual-mcps/{{${varName}}}${rest}`;
const createV = (id, varName) => request(id, "setup: create the virtual MCP Tools A over alpha's echo", "POST", "/api/mcp/virtual-mcps", {
  body: V_BODY(id), asserts: [A.status(201)], extraTests: [`pm.collectionVariables.set(${lit(varName)}, pm.response.json().virtual_mcp.id);`],
});
const deleteV = (id, varName) => request(id, "teardown: delete the virtual MCP", "DELETE", vmcpPath(varName), { asserts: [A.status(200)] });
const vmcpField = (path, v) => A.custom(`virtual_mcp.${path} is ${lit(v)}`, [`pm.expect(pm.response.json().virtual_mcp.${path}, 'body: ' + pm.response.text()).to.eql(${lit(v)});`]);
const V_TEXT = "the virtual MCP Tools A (slug tools-a) over alpha's echo";

const GOV29 = folder("GOV-29 Virtual MCP create", `Setup: ${ALPHA_GREET_TEXT}.`, [
  ...alphaGreetSetup("GOV-29"),
  request("GOV-29", "create Tools A over alpha's echo", "POST", "/api/mcp/virtual-mcps", { body: V_BODY("GOV-29"),
    asserts: [A.status(201), vmcpField("endpoint_slug", "tools-a"), vmcpField("enabled", true), vmcpField("virtual_key_ids", [])],
    extraTests: ["pm.collectionVariables.set('gov29_vmcp_id', pm.response.json().virtual_mcp.id);"] }),
  request("GOV-29", "no name", "POST", "/api/mcp/virtual-mcps", { body: { tools: [{ mcp_client_id: "gov-29-alpha", tool_names: ["echo"] }] }, asserts: [A.status(400), A.msgEquals("name is required")] }),
  request("GOV-29", "bad JSON", "POST", "/api/mcp/virtual-mcps", { raw: "{", asserts: [A.status(400)] }),
  request("GOV-29", "client id x", "POST", "/api/mcp/virtual-mcps", { body: { name: "GOV-29 bad client", tools: [{ mcp_client_id: "x", tool_names: ["echo"] }] }, asserts: [A.status(400), A.msgEquals("invalid MCP client IDs: x")] }),
  deleteV("GOV-29", "gov29_vmcp_id"),
  alphaGreetTeardown("GOV-29"),
]);

const GOV31 = folder("GOV-31 Virtual MCP update and lookup", `Setup: ${ALPHA_GREET_TEXT}; ${V_TEXT}.`, [
  ...alphaGreetSetup("GOV-31"), createV("GOV-31", "gov31_vmcp_id"),
  request("GOV-31", "PUT a new slug", "PUT", vmcpPath("gov31_vmcp_id"), { body: { endpoint_slug: "tools-b" }, asserts: [A.status(200), vmcpField("endpoint_slug", "tools-a")] }),
  request("GOV-31", "PUT without tools", "PUT", vmcpPath("gov31_vmcp_id"), { body: { description: "GOV-31" }, asserts: [A.status(200), vmcpField("tools", V_BODY("GOV-31").tools)] }),
  request("GOV-31", "PUT enabled false", "PUT", vmcpPath("gov31_vmcp_id"), { body: { enabled: false }, asserts: [A.status(200)] }),
  request("GOV-31", "enabled false persists", "GET", vmcpPath("gov31_vmcp_id"), { asserts: [A.status(200), vmcpField("enabled", false), vmcpField("endpoint_slug", "tools-a"), vmcpField("tools", V_BODY("GOV-31").tools)] }),
  request("GOV-31", "GET /abc", "GET", "/api/mcp/virtual-mcps/abc", { asserts: [A.status(400), A.msgEquals("invalid virtual MCP id")] }),
  request("GOV-31", "GET /999999", "GET", "/api/mcp/virtual-mcps/999999", { asserts: [A.status(404), A.msgEquals("virtual MCP not found")] }),
  deleteV("GOV-31", "gov31_vmcp_id"),
  alphaGreetTeardown("GOV-31"),
]);

const GOV32 = folder("GOV-32 Attach and detach VKs", `Setup: ${ALPHA_GREET_TEXT}; ${V_TEXT}; a VK with no MCP configs.`, [
  ...alphaGreetSetup("GOV-32"), createV("GOV-32", "gov32_vmcp_id"),
  vkCreate("GOV-32", "setup: create a VK", { name: "GOV-32 VK" }, "gov32_vk"),
  request("GOV-32", "attach an unknown VK", "POST", vmcpPath("gov32_vmcp_id", "/virtual-keys/gov32-no-such-vk"), { asserts: [A.status(404), A.msgEquals("virtual key not found")] }),
  request("GOV-32", "detach the VK, which was never attached", "DELETE", vmcpPath("gov32_vmcp_id", "/virtual-keys/{{gov32_vk_id}}"), { asserts: [A.status(200)] }),
  request("GOV-32", "attach the VK", "POST", vmcpPath("gov32_vmcp_id", "/virtual-keys/{{gov32_vk_id}}"), { asserts: [A.status(200)] }),
  request("GOV-32", "attach the VK again", "POST", vmcpPath("gov32_vmcp_id", "/virtual-keys/{{gov32_vk_id}}"), { asserts: [A.status(200)] }),
  request("GOV-32", "the VK is listed once", "GET", vmcpPath("gov32_vmcp_id"), { asserts: [A.status(200), A.custom("virtual_mcp.virtual_key_ids is [the VK's id]", [
    "pm.expect(pm.response.json().virtual_mcp.virtual_key_ids, 'body: ' + pm.response.text()).to.eql([pm.collectionVariables.get('gov32_vk_id')]);",
  ])] }),
  request("GOV-32", "attach to an unknown virtual MCP", "POST", "/api/mcp/virtual-mcps/999999/virtual-keys/{{gov32_vk_id}}", { asserts: [A.status(404)] }),
  deleteV("GOV-32", "gov32_vmcp_id"),
  vkDelete("GOV-32", "teardown: delete the VK", "gov32_vk"),
  alphaGreetTeardown("GOV-32"),
]);

// ================================================================ LIB installed flags and URL checks, FAIL log entries

// libPublish publishes a custom entry and keeps its id in varName.
const libPublish = (id, title, body, varName) => request(id, title, "POST", "/api/mcp/library", {
  body, asserts: [A.status(200)], extraTests: [`pm.collectionVariables.set(${lit(varName)}, pm.response.json().entry.id);`],
});
// libInstalled searches the library and checks the saved entry's installed flag.
const libInstalled = (id, title, search, varName, installed) => request(id, title, "GET", `/api/mcp/library?search=${encodeURIComponent(search)}`, {
  asserts: [A.status(200), A.custom(`the entry has installed ${installed}`, [
    `var id = Number(pm.collectionVariables.get(${lit(varName)}));`,
    "var found = (pm.response.json().servers || []).filter(function (s) { return s.id === id; });",
    "pm.expect(found.length, 'body: ' + pm.response.text()).to.equal(1);",
    `pm.expect(found[0].installed).to.equal(${installed});`,
  ])],
});
const LIB_DUP_MSG = "an MCP library server with this name already exists";
const LIB11_URL = "http://127.0.0.1:9/lib11/mcp";
const LIB11 = folder("LIB-11 Duplicate names and slugs", "Setup: a custom entry \"My Server\"; the boot catalog's remote entry \"Harness Catalog Server\".", [
  libPublish("LIB-11", "setup: publish \"My Server\"", { name: "My Server", connection_type: "http", connection_url: LIB11_URL }, "lib11_id"),
  request("LIB-11", "control: the remote entry is live", "GET", "/api/mcp/library?search=Harness%20Catalog%20Server", { asserts: [A.status(200), A.custom("one remote entry named \"Harness Catalog Server\"", [
    "var found = (pm.response.json().servers || []).filter(function (s) { return s.name === 'Harness Catalog Server'; });",
    "pm.expect(found.length, 'body: ' + pm.response.text()).to.equal(1);",
    "pm.expect(found[0].source).to.equal('remote');",
  ])] }),
  ...["My Server", "my-server", "Harness Catalog Server"].map((name) => request("LIB-11", `publish ${lit(name)}`, "POST", "/api/mcp/library", {
    body: { name, connection_type: "http", connection_url: LIB11_URL }, asserts: [A.status(409), A.msgEquals(LIB_DUP_MSG)],
  })),
  libDelete("LIB-11", "lib11_id"),
]);

const LIB18_URL = "http://127.0.0.1:9/lib18/mcp";
const LIB18 = folder("LIB-18 Installed flag by URL", `Setup: a custom entry with URL ${LIB18_URL}; then a client at that URL plus a trailing "/" under another name.`, [
  libPublish("LIB-18", "setup: publish the entry", { name: "LIB-18 entry", connection_type: "http", connection_url: LIB18_URL }, "lib18_id"),
  libInstalled("LIB-18", "before the client: installed false", "LIB-18 entry", "lib18_id", false),
  createClient("LIB-18", { client_id: "lib18-c", name: "lib18_other", connection_type: "http", connection_string: `${LIB18_URL}/`, auth_type: "none" }, "setup: create a client at the entry's URL plus \"/\" named lib18_other"),
  libInstalled("LIB-18", "after the client: installed true", "LIB-18 entry", "lib18_id", true),
  deleteClient("LIB-18", "lib18-c"),
  libDelete("LIB-18", "lib18_id"),
]);

const LIB19 = folder("LIB-19 Installed flag by name, rename, delete", "Setup: alpha (echo_tool, add_tool); a custom entry \"Chaos Alpha\" at another URL; a client chaos_alpha on alpha.", [
  ...freshServer("LIB-19", "alpha", ["echo_tool", "add_tool"]),
  libPublish("LIB-19", "setup: publish \"Chaos Alpha\"", { name: "Chaos Alpha", connection_type: "http", connection_url: "http://127.0.0.1:9/lib19/mcp" }, "lib19_id"),
  libInstalled("LIB-19", "control: installed false before the client", "Chaos Alpha", "lib19_id", false),
  createClient("LIB-19", httpClient("lib19-c", "chaos_alpha", "alpha"), "create client chaos_alpha"),
  libInstalled("LIB-19", "after create: installed true", "Chaos Alpha", "lib19_id", true),
  getClient("LIB-19", "lib19-c", "the client's slug is \"chaos-alpha\"", [clientField("config.endpoint_slug", "chaos-alpha")]),
  request("LIB-19", "rename the client to lib19_renamed", "PUT", "/api/mcp/client/lib19-c", { body: { name: "lib19_renamed" }, asserts: [A.status(200)] }),
  getClient("LIB-19", "lib19-c", "renamed, slug unchanged", [clientField("config.name", "lib19_renamed"), clientField("config.endpoint_slug", "chaos-alpha")]),
  libInstalled("LIB-19", "after the rename: installed true", "Chaos Alpha", "lib19_id", true),
  deleteClient("LIB-19", "lib19-c"),
  libInstalled("LIB-19", "after the delete: installed false", "Chaos Alpha", "lib19_id", false),
  libDelete("LIB-19", "lib19_id"),
]);

const LIB20 = folder("LIB-20 Installed flag for STDIO", "Setup: alpha and beta (echo_tool); a STDIO entry bridging to alpha; STDIO clients, one at a time, with the same args and with different args.", [
  ...freshServer("LIB-20", "alpha", ["echo_tool"]),
  ...freshServer("LIB-20", "beta", ["echo_tool"]),
  libPublish("LIB-20", "setup: publish the STDIO entry", { name: "LIB-20 stdio entry", connection_type: "stdio", stdio_config: stdioClient("x", "x", "alpha").stdio_config }, "lib20_id"),
  libInstalled("LIB-20", "control: installed false before any client", "LIB-20 stdio entry", "lib20_id", false),
  createClient("LIB-20", stdioClient("lib20-same", "lib20_same", "alpha"), "create a STDIO client with the same args"),
  libInstalled("LIB-20", "same args: installed true", "LIB-20 stdio entry", "lib20_id", true),
  deleteClient("LIB-20", "lib20-same"),
  createClient("LIB-20", stdioClient("lib20-diff", "lib20_diff", "beta"), "create a STDIO client with different args"),
  libInstalled("LIB-20", "different args: installed false", "LIB-20 stdio entry", "lib20_id", false),
  deleteClient("LIB-20", "lib20-diff"),
  libDelete("LIB-20", "lib20_id"),
]);

// frameworkPut sends the saved client_config back with one framework_config field.
const frameworkPut = (id, title, field, valueSrc, asserts) => request(id, title, "PUT", "/api/config", {
  raw: "{{config_body}}", asserts,
  pre: ["var cc = JSON.parse(pm.collectionVariables.get('client_config'));", `pm.variables.set('config_body', JSON.stringify({ client_config: cc, framework_config: { ${field}: ${valueSrc} } }));`],
});
const LIB32 = folder("LIB-32 file:// URL over the API", "Setup: PUT /api/config with framework_config.mcp_library_url set to a file:// URL.", [
  readConfig("LIB-32"),
  request("LIB-32", "setup: read the stored mcp_library_url", "GET", "/api/config?from_db=true", { asserts: [A.status(200)], extraTests: ["pm.collectionVariables.set('lib32_url', pm.response.json().framework_config.mcp_library_url);"] }),
  frameworkPut("LIB-32", "PUT a file:// mcp_library_url", "mcp_library_url", lit("file:///tmp/lib32-other-catalog.json"), [A.status(400), A.msgContains("config.json")]),
  request("LIB-32", "nothing was stored", "GET", "/api/config?from_db=true", { asserts: [A.status(200), A.custom("mcp_library_url is unchanged", [
    "pm.expect(pm.response.json().framework_config.mcp_library_url, 'body: ' + pm.response.text()).to.equal(pm.collectionVariables.get('lib32_url'));",
  ])] }),
]);

const LIB33 = folder("LIB-33 ftp:// URL over the API", "Setup: PUT /api/config with framework_config.mcp_library_url set to an ftp:// URL.", [
  readConfig("LIB-33"),
  frameworkPut("LIB-33", "PUT an ftp:// mcp_library_url", "mcp_library_url", lit("ftp://example.com/catalog.json"), [A.status(400), A.msgContains("only https and http schemes are allowed, got: ftp")]),
]);

const LIB37 = folder("LIB-37 Valid interval", "Setup: PUT /api/config with framework_config.mcp_library_sync_interval 7200.", [
  readConfig("LIB-37"),
  request("LIB-37", "setup: read the current mcp_library_sync_interval", "GET", "/api/config", { asserts: [A.status(200)], extraTests: ["pm.collectionVariables.set('lib37_interval', JSON.stringify(pm.response.json().framework_config.mcp_library_sync_interval));"] }),
  frameworkPut("LIB-37", "PUT mcp_library_sync_interval 7200", "mcp_library_sync_interval", "7200", [A.status(200)]),
  request("LIB-37", "it reads 7200", "GET", "/api/config", { asserts: [A.status(200), A.custom("framework_config.mcp_library_sync_interval is 7200", [
    "pm.expect(pm.response.json().framework_config.mcp_library_sync_interval, 'body: ' + pm.response.text()).to.equal(7200);",
  ])] }),
  frameworkPut("LIB-37", "teardown: restore mcp_library_sync_interval", "mcp_library_sync_interval", "JSON.parse(pm.collectionVariables.get('lib37_interval'))", [A.status(200)]),
]);

const responseTime = (min, max) => A.custom(`the response took ${min !== null ? `at least ${min / 1000}s and ` : ""}under ${max / 1000}s`, [
  ...(min !== null ? [`pm.expect(pm.response.responseTime, 'ms').to.be.at.least(${min});`] : []),
  `pm.expect(pm.response.responseTime, 'ms').to.be.below(${max});`,
]);
const FAIL02 = folder("FAIL-02 SSE create against a closed port", "Setup: an SSE client named alpha at 127.0.0.1:9, where nothing listens.", [
  createExpect("FAIL-02", "create the SSE client", { client_id: "fail02-a", name: "alpha", connection_type: "sse", connection_string: "http://127.0.0.1:9/s/alpha/sse", auth_type: "none", tools_to_execute: ["*"] },
    [A.status(500), A.msgContains("failed to start MCP client transport after 5 retries"), responseTime(30000, 40000)]),
]);

const FAIL14 = folder("FAIL-14 refresh-tools during a list failure", "Setup: alpha (echo_tool, add_tool) as client alpha, sticky, tools_to_execute [\"*\"]; then alpha fails tools/list.", [
  ...setClient("FAIL-14", { needs_session_stickiness: true }),
  mockPost("FAIL-14", "alpha", "faults", "setup: alpha fails tools/list", { faults: { list_fail: true } }),
  request("FAIL-14", "refresh-tools", "POST", "/api/mcp/client/fail-14-a/refresh-tools", { asserts: [A.status(500), A.msgStartsWith("Failed to refresh MCP client tools: failed to list tools for MCP client alpha"), responseTime(null, 7000)] }),
  getClient("FAIL-14", "fail-14-a", "the state stays healthy", [clientState("healthy")]),
  mockPost("FAIL-14", "alpha", "faults/clear", "teardown: clear alpha's faults"),
  deleteClient("FAIL-14", "fail-14-a"),
]);

const FAIL16 = folder("FAIL-16 Per-call clients ignore session kills", "Setup: alpha (echo_tool, add_tool) as client alpha, per call, tools_to_execute [\"*\"]; then POST /control/alpha/sessions/kill.", [
  ...setClient("FAIL-16", {}),
  mockPost("FAIL-16", "alpha", "sessions/kill", "setup: kill alpha's sessions"),
  saveSessions("FAIL-16", "alpha", "fail16_sessions"),
  setExec("FAIL-16", "execute echo_tool", "echo_tool", [A.status(200)]),
  mockGet("FAIL-16", "alpha", "the call opened one session", [sessionsPlus("fail16_sessions", 1)]),
  deleteClient("FAIL-16", "fail-16-a"),
]);

// FAIL-18 puts the malformed reply on echo_tool alone; add_tool still answers, which the control shows.
const toolsCallMethods = "((pm.response.json().counters.methods || {})['tools/call'] || 0)";
const FAIL18 = folder("FAIL-18 Malformed tools/call reply", "Setup: alpha (echo_tool, add_tool) as client alpha, sticky, tools_to_execute [\"*\"]; then alpha answers echo_tool's tools/call with a malformed body.", [
  ...setClient("FAIL-18", { needs_session_stickiness: true }),
  mockPost("FAIL-18", "alpha", "tools/set", "setup: echo_tool answers with a malformed body", { tools: [{ name: "echo_tool", fault: { malformed: true } }, { name: "add_tool" }] }),
  mockGet("FAIL-18", "alpha", "setup: record alpha's tools/call count", [], [`pm.collectionVariables.set('fail18_calls', ${toolsCallMethods});`]),
  setExec("FAIL-18", "execute echo_tool", "echo_tool", [A.status(400), A.msgContains("MCP tool call failed for alpha-echo_tool")]),
  mockGet("FAIL-18", "alpha", "alpha received one tools/call", [A.custom("tools/call is one more than the recorded count", [
    `pm.expect(${toolsCallMethods}, 'body: ' + pm.response.text()).to.equal(Number(pm.collectionVariables.get('fail18_calls')) + 1);`,
  ])]),
  setExec("FAIL-18", "control: execute add_tool", "add_tool", [A.status(200)]),
  deleteClient("FAIL-18", "fail-18-a"),
]);

// ================================================================ LOG log entries written by tool calls

// LOG tests run on setClient's alpha (echo_tool, add_tool) as client alpha, per call, and tag each call with a
// folder-unique x-request-id R, which becomes the MCP log entry's id.
const ALPHA_TOOLS_TEXT = "alpha (echo_tool, add_tool) registered as client alpha, auth none, per call, tools_to_execute [\"*\"]";
const reqId = (R) => ({ key: "x-request-id", value: R });
const execR = (id, title, R, tool, args, asserts, { query = "", headers = [] } = {}) => execBody(id, title,
  { id: `${R}-call`, type: "function", function: { name: tool, arguments: lit(args) } }, asserts, { query, headers: [reqId(R), ...headers] });
const rpcR = (id, title, path, R, tok, asserts, headers = []) => rpc(id, title, call("alpha-echo_tool", { text: tok }), { path, headers: [MCP_ACCEPT, reqId(R), ...headers], asserts });
// logRow polls GET /api/mcp-logs/R for up to about 15s (log entries are written in 5s batches), then asserts on it.
const pollLogRow = (R) => [
  "var left = 30;",
  "function poll() {",
  `  pm.sendRequest({ url: pm.variables.get('base_url') + '/api/mcp-logs/${R}', method: 'GET', header: { Authorization: pm.variables.get('admin_auth_header') } }, function (err, res) {`,
  "    if ((!err && res.code === 200 && res.json().status !== 'processing') || --left <= 0) { return; }",
  "    setTimeout(poll, 500);",
  "  });",
  "}",
  "poll();",
];
const logRow = (id, title, R, asserts) => request(id, title, "GET", `/api/mcp-logs/${R}`, { pre: pollLogRow(R), asserts: [A.status(200), ...asserts] });
const noRowYet = (id, title, R) => request(id, title, "GET", `/api/mcp-logs/${R}`, { asserts: [A.status(404), A.msgEquals("MCP log not found")] });
const rowIs = (field, v) => A.custom(`${field} is ${lit(v)}`, [`pm.expect(pm.response.json()[${lit(field)}], 'body: ' + pm.response.text()).to.eql(${lit(v)});`]);
const rowLacks = (...fields) => A.custom(`no ${fields.join(" or ")}`, fields.map((f) => `pm.expect(pm.response.json(), 'body: ' + pm.response.text()).to.not.have.property(${lit(f)});`));
const rowLatency = () => A.custom("latency is a number", ["pm.expect(pm.response.json().latency, 'body: ' + pm.response.text()).to.be.a('number');"]);
const rowErrorMsg = (how, m) => A.custom(`error_details.error.message ${how === "equals" ? "is" : "contains"} ${lit(m)}`, [
  "var e = pm.response.json().error_details;",
  `pm.expect(e && e.error && e.error.message, 'body: ' + pm.response.text()).to.${how === "equals" ? "equal" : "include"}(${lit(m)});`,
]);
const ECHO_ROW = [rowIs("server_label", "alpha"), rowIs("tool_name", "echo_tool"), rowIs("status", "success")];
const logVK = (id, varPrefix, extra = {}) => vkCreate(id, "setup: create a VK granting alpha", { name: `${id} vk`, mcp_configs: [{ mcp_client_name: "alpha", tools_to_execute: ["*"] }], ...extra }, varPrefix);
const vkH = (varPrefix) => ({ key: "x-bf-vk", value: `{{${varPrefix}}}` });
const logsCount = (id, title, query, n, pre = []) => request(id, title, "GET", `/api/mcp-logs?${query}`, { pre, asserts: [A.status(200), A.custom(`total_count is ${n}`, [
  `pm.expect(pm.response.json().pagination.total_count, 'body: ' + pm.response.text()).to.equal(${n});`,
])] });
const CLAUDE_UA = "claude-cli/2.1.0 (external, cli)";
const UA_H = { key: "User-Agent", value: CLAUDE_UA };
const PROTOCOL_FAULT = { faults: { err_rate: 1, err_kind: "protocol" } };

const LOG01 = folder("LOG-01 POST /v1/mcp/tool/execute (chat) writes a log entry", `Setup: ${ALPHA_TOOLS_TEXT}; the call carries x-request-id log01-r and User-Agent "${CLAUDE_UA}".`, [
  ...setClient("LOG-01", {}),
  callsBefore("LOG-01", "log01_calls", "echo_tool"),
  execR("LOG-01", "execute alpha-echo_tool", "log01-r", "alpha-echo_tool", { text: "log01" }, [A.status(200)], { headers: [UA_H] }),
  logRow("LOG-01", "the log entry", "log01-r", [...ECHO_ROW, rowLatency(), rowIs("arguments", lit({ text: "log01" })), A.custom("result is present", [
    "pm.expect(pm.response.json().result, 'body: ' + pm.response.text()).to.not.equal(undefined);",
    "pm.expect(pm.response.json().result).to.not.equal(null);",
  ]), rowIs("user_agent", CLAUDE_UA), rowIs("app", "Claude Code"), rowLacks("llm_request_id")]),
  mockGet("LOG-01", "alpha", "alpha counted the call", [callsPlusOne("log01_calls", "echo_tool")]),
  deleteClient("LOG-01", "log-01-a"),
]);

const LOG02 = folder("LOG-02 Responses format", `Setup: ${ALPHA_TOOLS_TEXT}; execute with ?format=responses and x-request-id log02-r.`, [
  ...setClient("LOG-02", {}),
  execBody("LOG-02", "execute alpha-echo_tool in the Responses format", { call_id: "log02-call", name: "alpha-echo_tool", arguments: lit({ text: "log02" }) },
    [A.status(200), A.jsonEquals("type", "function_call_output")], { query: "?format=responses", headers: [reqId("log02-r")] }),
  logRow("LOG-02", "the log entry", "log02-r", [rowIs("status", "success"), rowIs("tool_name", "echo_tool")]),
  deleteClient("LOG-02", "log-02-a"),
]);

const LOG03 = folder("LOG-03 Unknown format", `Setup: ${ALPHA_TOOLS_TEXT}; execute with ?format=xml and x-request-id log03-r.`, [
  ...setClient("LOG-03", {}),
  execR("LOG-03", "execute with format=xml", "log03-r", "alpha-echo_tool", {}, [A.status(400), A.msgEquals("Invalid format value, must be 'chat' or 'responses'")], { query: "?format=xml" }),
  execR("LOG-03", "control: a valid call after it", "log03-ctl", "alpha-echo_tool", {}, [A.status(200)]),
  logRow("LOG-03", "control: the valid call's log entry was written", "log03-ctl", []),
  noRowYet("LOG-03", "no log entry for log03-r", "log03-r"),
  deleteClient("LOG-03", "log-03-a"),
]);

const LOG04 = folder("LOG-04 /mcp tools/call writes a log entry", `Setup: ${ALPHA_TOOLS_TEXT}; tools/call on /mcp with x-request-id log04-r.`, [
  ...setClient("LOG-04", {}),
  rpcR("LOG-04", "tools/call alpha-echo_tool on /mcp", "/mcp", "log04-r", "log04", [A.status(200), A.custom("result.content[0].text is the echo", [
    "pm.expect(pm.response.json().result.content[0].text, 'body: ' + pm.response.text()).to.equal('echo_tool:{\"text\":\"log04\"}');",
  ])]),
  logRow("LOG-04", "the log entry", "log04-r", ECHO_ROW),
  deleteClient("LOG-04", "log-04-a"),
]);

const LOG05 = folder("LOG-05 /mcp/{client slug} tools/call", `Setup: ${ALPHA_TOOLS_TEXT} (slug "alpha"); tools/call on /mcp/alpha with x-request-id log05-r.`, [
  ...setClient("LOG-05", {}),
  rpcR("LOG-05", "tools/call alpha-echo_tool on /mcp/alpha", "/mcp/alpha", "log05-r", "log05", [A.status(200)]),
  logRow("LOG-05", "the log entry", "log05-r", ECHO_ROW),
  deleteClient("LOG-05", "log-05-a"),
]);

const LOG09_POLL = [
  "var left = 30;",
  "function poll() {",
  "  pm.sendRequest({ url: pm.variables.get('base_url') + '/api/mcp-logs?tool_names=echo_tool&content_search=log09-', method: 'GET', header: { Authorization: pm.variables.get('admin_auth_header') } }, function (err, res) {",
  "    if ((!err && res.code === 200 && res.json().pagination.total_count >= 2) || --left <= 0) { return; }",
  "    setTimeout(poll, 500);",
  "  });",
  "}",
  "poll();",
];
// pollLogCount re-reads /api/mcp-logs?<query> for up to about 15s until total_count reaches n.
const pollLogCount = (query, n) => [
  "var left = 30;",
  "function poll() {",
  `  pm.sendRequest({ url: pm.variables.get('base_url') + '/api/mcp-logs?${query}', method: 'GET', header: { Authorization: pm.variables.get('admin_auth_header') } }, function (err, res) {`,
  `    if ((!err && res.code === 200 && res.json().pagination.total_count >= ${n}) || --left <= 0) { return; }`,
  "    setTimeout(poll, 500);",
  "  });",
  "}",
  "poll();",
];
const LOG09 = folder("LOG-09 Code mode logs the nested calls only", "Setup: alpha (echo_tool, add_tool) registered as client alpha, a code-mode client, auth none, per call, tools_to_execute [\"*\"]; listToolFiles, readToolFile and getToolDocs are each called once; executeToolCode calls echo_tool twice.", [
  ...setClient("LOG-09", { is_code_mode_client: true }),
  execR("LOG-09", "listToolFiles", "log09-l", "listToolFiles", {}, [A.status(200)]),
  execR("LOG-09", "readToolFile servers/alpha.pyi", "log09-f", "readToolFile", { fileName: "servers/alpha.pyi" }, [A.status(200)]),
  execR("LOG-09", "getToolDocs alpha/echo_tool", "log09-d", "getToolDocs", { server: "alpha", tool: "echo_tool" }, [A.status(200)]),
  execR("LOG-09", "executeToolCode calls echo_tool twice", "log09-r", "executeToolCode", { code: "a = alpha.echo_tool(text=\"log09-a\")\nb = alpha.echo_tool(text=\"log09-b\")\nresult = [a, b]" }, [A.status(200)]),
  request("LOG-09", "two echo_tool log entries with distinct ids", "GET", "/api/mcp-logs?tool_names=echo_tool&content_search=log09-", { pre: LOG09_POLL, asserts: [A.status(200), A.custom("total_count is 2 and the ids differ", [
    "var b = pm.response.json();",
    "pm.expect(b.pagination.total_count, 'body: ' + pm.response.text()).to.equal(2);",
    "pm.expect(b.logs[0].id).to.not.equal(b.logs[1].id);",
  ])] }),
  // A later call whose nested log entry lands after any log entry the meta tool calls could have queued.
  execR("LOG-09", "control: a later executeToolCode call", "log09-c", "executeToolCode", { code: "alpha.echo_tool(text=\"log09ctl\")\nresult = 1" }, [A.status(200)]),
  logsCount("LOG-09", "control: the later call's nested log entry landed", "tool_names=echo_tool&content_search=log09ctl", 1, pollLogCount("tool_names=echo_tool&content_search=log09ctl", 1)),
  noRowYet("LOG-09", "no log entry for the executeToolCode call", "log09-r"),
  noRowYet("LOG-09", "no log entry for the listToolFiles call", "log09-l"),
  noRowYet("LOG-09", "no log entry for the readToolFile call", "log09-f"),
  noRowYet("LOG-09", "no log entry for the getToolDocs call", "log09-d"),
  logsCount("LOG-09", "no log entries for the meta tools", "tool_names=executeToolCode,listToolFiles,readToolFile,getToolDocs", 0),
  deleteClient("LOG-09", "log-09-a"),
]);

const LOG11 = folder("LOG-11 Unknown tool", `Setup: ${ALPHA_TOOLS_TEXT}; execute alpha-nope with x-request-id log11-r.`, [
  ...setClient("LOG-11", {}),
  execR("LOG-11", "execute alpha-nope", "log11-r", "alpha-nope", {}, [A.status(400), A.msgEquals("tool 'alpha-nope' is not available or not permitted")]),
  logRow("LOG-11", "the log entry", "log11-r", [rowIs("status", "error"), rowIs("tool_name", "nope"), rowErrorMsg("equals", "tool 'alpha-nope' is not available or not permitted"), rowLacks("latency", "cost")]),
  callsEmpty("LOG-11", "alpha"),
  deleteClient("LOG-11", "log-11-a"),
]);

const LOG12 = folder("LOG-12 Tool outside tools_to_execute", "Setup: alpha (echo_tool, add_tool) registered as client alpha, auth none, per call, tools_to_execute [\"add_tool\"]; execute alpha-echo_tool with x-request-id log12-r.", [
  ...setClient("LOG-12", { tools_to_execute: ["add_tool"] }),
  execR("LOG-12", "execute alpha-echo_tool", "log12-r", "alpha-echo_tool", {}, [A.status(400), A.msgContains("is not permitted (not in client's ToolsToExecute allow-list)")]),
  logRow("LOG-12", "the log entry", "log12-r", [rowIs("status", "error")]),
  deleteClient("LOG-12", "log-12-a"),
]);

// protocolFault is the LOG-13 and LOG-16 setup: alpha fails every tools/call with a JSON-RPC error.
const protocolFault = (id) => mockPost(id, "alpha", "faults", "setup: alpha fails every tools/call with a JSON-RPC error", PROTOCOL_FAULT);
const clearFaults = (id) => mockPost(id, "alpha", "faults/clear", "teardown: clear alpha's faults");
const LOG13 = folder("LOG-13 Upstream protocol failure", `Setup: ${ALPHA_TOOLS_TEXT}; alpha fails every tools/call with a JSON-RPC error (err_rate 1, err_kind protocol).`, [
  ...setClient("LOG-13", {}), protocolFault("LOG-13"),
  execR("LOG-13", "execute alpha-echo_tool", "log13-r", "alpha-echo_tool", {}, [A.status(400), A.msgContains("MCP tool call failed for alpha-echo_tool")]),
  logRow("LOG-13", "the log entry", "log13-r", [rowIs("status", "error"), rowErrorMsg("contains", "MCP tool call failed for alpha-echo_tool")]),
  clearFaults("LOG-13"), deleteClient("LOG-13", "log-13-a"),
]);

const LOG14 = folder("LOG-14 Upstream timeout", "Setup: alpha (echo_tool, add_tool) registered as client alpha, auth none, per call, tools_to_execute [\"*\"], tool_execution_timeout 2; alpha hangs 3s on every request.", [
  ...setClient("LOG-14", { tool_execution_timeout: 2 }),
  mockPost("LOG-14", "alpha", "faults", "setup: alpha hangs 3s", { faults: { hang: "3s" } }),
  execR("LOG-14", "execute alpha-echo_tool", "log14-r", "alpha-echo_tool", {}, [A.status(400), A.msgContains("MCP tool call timed out after 2s: alpha-echo_tool")]),
  logRow("LOG-14", "the log entry", "log14-r", [rowIs("status", "error")]),
  clearFaults("LOG-14"), deleteClient("LOG-14", "log-14-a"),
]);

const LOG16 = folder("LOG-16 Tool error over /mcp", `Setup: as LOG-13 (${ALPHA_TOOLS_TEXT}; alpha fails every tools/call with a JSON-RPC error).`, [
  ...setClient("LOG-16", {}), protocolFault("LOG-16"),
  rpcR("LOG-16", "tools/call alpha-echo_tool on /mcp", "/mcp", "log16-r", "log16", [A.status(200), toolResultError("Tool execution failed: ", null)]),
  logRow("LOG-16", "the log entry", "log16-r", [rowIs("status", "error")]),
  clearFaults("LOG-16"), deleteClient("LOG-16", "log-16-a"),
]);

const LOG17 = folder("LOG-17 Global content logging off", `Setup: ${ALPHA_TOOLS_TEXT}; disable_content_logging true.`, [
  readConfig("LOG-17"), ...setClient("LOG-17", {}),
  execR("LOG-17", "control: a call before the change", "log17-ctl", "alpha-echo_tool", { text: "log17-on" }, [A.status(200)]),
  putConfig("LOG-17", "setup: disable_content_logging true", ["cc.disable_content_logging = true;"], [A.status(200)]),
  execR("LOG-17", "execute alpha-echo_tool", "log17-r", "alpha-echo_tool", { text: "log17-off" }, [A.status(200)]),
  logRow("LOG-17", "the log entry has no content", "log17-r", [rowLacks("arguments", "result"), rowIs("status", "success"), rowLatency(), rowIs("tool_name", "echo_tool")]),
  logRow("LOG-17", "control: the earlier log entry was written", "log17-ctl", []),
  logsCount("LOG-17", "control: content search finds the earlier log entry", "content_search=log17-on", 1),
  logsCount("LOG-17", "content search finds nothing for the new log entry", "content_search=log17-off", 0),
  restoreConfig("LOG-17"), deleteClient("LOG-17", "log-17-a"),
]);

const NO_CONTENT_H = { key: "x-bf-disable-content-logging", value: "true" };
const LOG18 = folder("LOG-18 Per-request override header", `Setup: ${ALPHA_TOOLS_TEXT}; calls send x-bf-disable-content-logging: true with allow_per_request_content_storage_override on, then off.`, [
  readConfig("LOG-18"), ...setClient("LOG-18", {}),
  putConfig("LOG-18", "setup: allow_per_request_content_storage_override true", ["cc.allow_per_request_content_storage_override = true;"], [A.status(200)]),
  execR("LOG-18", "execute with the header, override on", "log18-on", "alpha-echo_tool", { text: "log18-on" }, [A.status(200)], { headers: [NO_CONTENT_H] }),
  putConfig("LOG-18", "setup: allow_per_request_content_storage_override false", ["cc.allow_per_request_content_storage_override = false;"], [A.status(200)]),
  execR("LOG-18", "execute with the header, override off", "log18-off", "alpha-echo_tool", { text: "log18-off" }, [A.status(200)], { headers: [NO_CONTENT_H] }),
  logRow("LOG-18", "override on: no content", "log18-on", [rowLacks("arguments", "result")]),
  logRow("LOG-18", "override off: content stored", "log18-off", [rowIs("arguments", lit({ text: "log18-off" })), A.custom("result is present", ["pm.expect(pm.response.json(), 'body: ' + pm.response.text()).to.have.property('result');"])]),
  restoreConfig("LOG-18"), deleteClient("LOG-18", "log-18-a"),
]);

const rowVK = (varPrefix) => A.custom("virtual_key_id is the VK's id", [`pm.expect(pm.response.json().virtual_key_id, 'body: ' + pm.response.text()).to.equal(pm.collectionVariables.get(${lit(varPrefix + "_id")}));`]);
const LOG19 = folder("LOG-19 VK turns content logging off", `Setup: ${ALPHA_TOOLS_TEXT}; a VK granting alpha with disable_content_logging true.`, [
  ...setClient("LOG-19", {}), logVK("LOG-19", "log19_vk", { disable_content_logging: true }),
  execR("LOG-19", "execute with the VK", "log19-r", "alpha-echo_tool", { text: "log19" }, [A.status(200)], { headers: [vkH("log19_vk")] }),
  logRow("LOG-19", "the VK's log entry has no content", "log19-r", [rowVK("log19_vk"), rowLacks("arguments", "result")]),
  execR("LOG-19", "control: a call without the VK", "log19-ctl", "alpha-echo_tool", { text: "log19-ctl" }, [A.status(200)]),
  logRow("LOG-19", "control: the call without the VK stores content", "log19-ctl", [rowIs("arguments", lit({ text: "log19-ctl" })), A.custom("result is present", ["pm.expect(pm.response.json(), 'body: ' + pm.response.text()).to.have.property('result');"])]),
  vkDelete("LOG-19", "teardown: delete the VK", "log19_vk"), deleteClient("LOG-19", "log-19-a"),
]);

const LOG20 = folder("LOG-20 VK attribution", `Setup: ${ALPHA_TOOLS_TEXT}; a VK "LOG-20 vk" granting alpha; the call carries x-bf-vk.`, [
  ...setClient("LOG-20", {}), logVK("LOG-20", "log20_vk"),
  execR("LOG-20", "execute with the VK", "log20-r", "alpha-echo_tool", {}, [A.status(200)], { headers: [vkH("log20_vk")] }),
  logRow("LOG-20", "the log entry names the VK", "log20-r", [rowVK("log20_vk"), rowIs("virtual_key_name", "LOG-20 vk")]),
  request("LOG-20", "?virtual_key_ids= finds the log entry", "GET", "/api/mcp-logs?virtual_key_ids={{log20_vk_id}}", { asserts: [A.status(200), A.custom("exactly one log entry, log20-r, and it carries the VK id", [
    "var b = pm.response.json();",
    "pm.expect(b.pagination.total_count, 'body: ' + pm.response.text()).to.equal(1);",
    "pm.expect(b.logs.map(function (l) { return l.id; })).to.eql(['log20-r']);",
    "b.logs.forEach(function (l) { pm.expect(l.virtual_key_id).to.equal(pm.collectionVariables.get('log20_vk_id')); });",
  ])] }),
  vkDelete("LOG-20", "teardown: delete the VK", "log20_vk"), deleteClient("LOG-20", "log-20-a"),
]);

const clearPrice = (id, clientId) => toolPrice(id, clientId, "teardown: clear tool_pricing", {});
const priceEcho = (id, clientId) => toolPrice(id, clientId, "setup: PUT tool_pricing echo_tool 0.25", { echo_tool: 0.25 });
const pricedCalls = (id, p, setupPrefix = "") => [
  execR(id, `${setupPrefix}a successful call`, `${p}-ok`, "alpha-echo_tool", {}, [A.status(200)]),
  protocolFault(id),
  execR(id, `${setupPrefix}a failing call`, `${p}-err`, "alpha-echo_tool", {}, [A.status(400)]),
];
const LOG21 = folder("LOG-21 Cost from tool_pricing", `Setup: ${ALPHA_TOOLS_TEXT}; echo_tool priced 0.25 with PUT; one successful call, then one failing call (JSON-RPC error).`, [
  ...setClient("LOG-21", {}), priceEcho("LOG-21", "log-21-a"),
  ...pricedCalls("LOG-21", "log21"),
  logRow("LOG-21", "the successful log entry costs 0.25", "log21-ok", [rowIs("cost", 0.25)]),
  logRow("LOG-21", "the failing log entry has no cost", "log21-err", [rowIs("status", "error"), rowLacks("cost")]),
  clearFaults("LOG-21"), clearPrice("LOG-21", "log-21-a"), deleteClient("LOG-21", "log-21-a"),
]);

const LOG22 = folder("LOG-22 Cost survives a rename", `Setup: as LOG-21 (${ALPHA_TOOLS_TEXT}; echo_tool priced 0.25 with PUT; one successful and one failing call); then alpha is renamed alpha2.`, [
  ...setClient("LOG-22", {}), priceEcho("LOG-22", "log-22-a"),
  ...pricedCalls("LOG-22", "log22", "setup: "),
  clearFaults("LOG-22"),
  request("LOG-22", "setup: rename alpha to alpha2", "PUT", "/api/mcp/client/log-22-a", { body: { name: "alpha2" }, asserts: [A.status(200)] }),
  execR("LOG-22", "execute alpha2-echo_tool", "log22-new", "alpha2-echo_tool", {}, [A.status(200)]),
  logRow("LOG-22", "the new log entry costs 0.25 under alpha2", "log22-new", [rowIs("cost", 0.25), rowIs("server_label", "alpha2")]),
  clearPrice("LOG-22", "log-22-a"), deleteClient("LOG-22", "log-22-a"),
]);

const LOG31 = folder("LOG-31 Filter data", `Setup: ${ALPHA_TOOLS_TEXT}; a VK "LOG-31 vk" granting alpha; one call with User-Agent "${CLAUDE_UA}" (as LOG-01) and one with the VK (as LOG-20).`, [
  ...setClient("LOG-31", {}), logVK("LOG-31", "log31_vk"),
  execR("LOG-31", "setup: a call with the Claude Code user agent", "log31-ua", "alpha-echo_tool", {}, [A.status(200)], { headers: [UA_H] }),
  execR("LOG-31", "setup: a call with the VK", "log31-vk", "alpha-echo_tool", {}, [A.status(200)], { headers: [vkH("log31_vk")] }),
  logRow("LOG-31", "setup: the user agent log entry was written", "log31-ua", []),
  logRow("LOG-31", "setup: the VK log entry was written", "log31-vk", []),
  request("LOG-31", "GET filterdata", "GET", "/api/mcp-logs/filterdata", { asserts: [A.status(200), A.custom(`tool_names has echo_tool; server_labels alpha; apps "Claude Code"; user_agents "${CLAUDE_UA}"; virtual_keys the VK`, [
    "var b = pm.response.json();",
    "pm.expect(b.tool_names, 'body: ' + pm.response.text()).to.include('echo_tool');",
    "pm.expect(b.server_labels).to.include('alpha');",
    "pm.expect(b.apps).to.include('Claude Code');",
    `pm.expect(b.user_agents).to.include(${lit(CLAUDE_UA)});`,
    "var vk = (b.virtual_keys || []).filter(function (v) { return v.id === pm.collectionVariables.get('log31_vk_id'); });",
    "pm.expect(vk.length).to.equal(1);",
    "pm.expect(vk[0].name).to.equal('LOG-31 vk');",
  ])] }),
  vkDelete("LOG-31", "teardown: delete the VK", "log31_vk"), deleteClient("LOG-31", "log-31-a"),
]);

const vkInFilter = (present, name, varPrefix = "log33_vk") => A.custom(present ? `virtual_keys has the VK named ${lit(name)}` : "virtual_keys does not have the deleted VK", [
  `var vk = (pm.response.json().virtual_keys || []).filter(function (v) { return v.id === pm.collectionVariables.get(${lit(varPrefix + "_id")}); });`,
  ...(present ? ["pm.expect(vk.length, 'body: ' + pm.response.text()).to.equal(1);", `pm.expect(vk[0].name).to.equal(${lit(name)});`] : ["pm.expect(vk.length, 'body: ' + pm.response.text()).to.equal(0);"]),
]);
const LOG33 = folder("LOG-33 Deleted VK in filter data", `Setup: ${ALPHA_TOOLS_TEXT}; a VK "LOG-33 vk" granting alpha, used once and then deleted; a second VK "LOG-33 live vk", used once and kept; hide_deleted_virtual_keys_in_filters off, then on.`, [
  readConfig("LOG-33"),
  putConfig("LOG-33", "setup: hide_deleted_virtual_keys_in_filters false", ["cc.hide_deleted_virtual_keys_in_filters = false;"], [A.status(200)]),
  ...setClient("LOG-33", {}), logVK("LOG-33", "log33_vk"),
  execR("LOG-33", "setup: a call with the VK", "log33-r", "alpha-echo_tool", {}, [A.status(200)], { headers: [vkH("log33_vk")] }),
  logRow("LOG-33", "setup: the log entry was written", "log33-r", []),
  vkCreate("LOG-33", "setup: create a second VK that stays", { name: "LOG-33 live vk", mcp_configs: [{ mcp_client_name: "alpha", tools_to_execute: ["*"] }] }, "log33_live"),
  execR("LOG-33", "setup: a call with the second VK", "log33-live", "alpha-echo_tool", {}, [A.status(200)], { headers: [vkH("log33_live")] }),
  logRow("LOG-33", "setup: the second VK's log entry was written", "log33-live", []),
  vkDelete("LOG-33", "setup: delete the VK", "log33_vk"),
  request("LOG-33", "setting off: filterdata shows the VK as deleted", "GET", "/api/mcp-logs/filterdata?dimensions=virtual_keys", { asserts: [A.status(200), vkInFilter(true, "LOG-33 vk (deleted)")] }),
  putConfig("LOG-33", "setup: hide_deleted_virtual_keys_in_filters true", ["cc.hide_deleted_virtual_keys_in_filters = true;"], [A.status(200)]),
  request("LOG-33", "setting on: filterdata leaves the deleted VK out and keeps the live one", "GET", "/api/mcp-logs/filterdata?dimensions=virtual_keys", { asserts: [A.status(200), vkInFilter(false), vkInFilter(true, "LOG-33 live vk", "log33_live")] }),
  restoreConfig("LOG-33"), vkDelete("LOG-33", "teardown: delete the second VK", "log33_live"), deleteClient("LOG-33", "log-33-a"),
]);

const LOG38 = folder("LOG-38 Delete logs", `Setup: ${ALPHA_TOOLS_TEXT}; authenticated; one call with x-request-id log38-r.`, [
  ...setClient("LOG-38", {}),
  execR("LOG-38", "setup: execute alpha-echo_tool", "log38-r", "alpha-echo_tool", {}, [A.status(200)]),
  logRow("LOG-38", "control: the log entry exists", "log38-r", []),
  request("LOG-38", "delete the log entry", "DELETE", "/api/mcp-logs", { body: { ids: ["log38-r"] }, asserts: [A.status(200), A.jsonMessage("MCP tool logs deleted successfully")] }),
  noRowYet("LOG-38", "the log entry is gone", "log38-r"),
  deleteClient("LOG-38", "log-38-a"),
]);

// LOG43_SERIES finds the duration-count series for the VK's calls and checks its labels and value.
const LOG43_SERIES = String.raw`var vk = pm.collectionVariables.get('log43_vk_id');
var lines = pm.response.text().split('\n').filter(function (l) { return l.indexOf('bifrost_mcp_client_operation_duration_seconds_count{') === 0 && l.indexOf('virtual_key_id="' + vk + '"') >= 0; });
pm.expect(lines.length, 'matching series: ' + lines.join(' | ')).to.equal(1);
var m = /^[^{]+\{(.*)\}\s+(\S+)$/.exec(lines[0]);
var labels = {}, re = /(\w+)="((?:[^"\\]|\\.)*)"/g, x;
while ((x = re.exec(m[1]))) { labels[x[1]] = x[2]; }
pm.expect(labels.mcp_client, lines[0]).to.equal('alpha');
pm.expect(labels.mcp_tool_name, lines[0]).to.equal('echo_tool');
pm.expect(labels.mcp_method, lines[0]).to.equal('tools/call');
pm.expect(labels.error_type || '', lines[0]).to.equal('');
pm.expect(Number(m[2]), lines[0]).to.equal(1);`.split("\n");
const LOG43_POLL = [
  "var left = 20;",
  "function poll() {",
  "  pm.sendRequest({ url: pm.variables.get('base_url') + '/metrics', method: 'GET', header: { Authorization: pm.variables.get('admin_auth_header') } }, function (err, res) {",
  "    var vk = pm.collectionVariables.get('log43_vk_id');",
  "    if ((!err && res.code === 200 && res.text().indexOf('virtual_key_id=\"' + vk + '\"') >= 0) || --left <= 0) { return; }",
  "    setTimeout(poll, 250);",
  "  });",
  "}",
  "poll();",
];
const LOG43 = folder("LOG-43 Prometheus metric on success", `Setup: ${ALPHA_TOOLS_TEXT}; telemetry on (the default); a VK granting alpha; one call with the VK, then GET /metrics.`, [
  ...setClient("LOG-43", {}), logVK("LOG-43", "log43_vk"),
  execR("LOG-43", "execute with the VK", "log43-r", "alpha-echo_tool", {}, [A.status(200)], { headers: [vkH("log43_vk")] }),
  request("LOG-43", "GET /metrics", "GET", "/metrics", { pre: LOG43_POLL, asserts: [A.status(200), A.custom("bifrost_mcp_client_operation_duration_seconds_count for the VK: mcp_client alpha, mcp_tool_name echo_tool, mcp_method tools/call, error_type \"\", value 1", LOG43_SERIES)] }),
  vkDelete("LOG-43", "teardown: delete the VK", "log43_vk"), deleteClient("LOG-43", "log-43-a"),
]);

// ================================================================ tests on the mock's instructions, annotations, replies and scoped faults

// serveTools re-adds a fleet server and gives it full tool definitions (annotations, replies, faults).
const serveTools = (id, name, tools, title) => [
  request(id, `setup: fresh mcpchaos server ${name}`, "POST", "/control", { base: CHAOS, noAuth: true, body: { add: name }, asserts: [A.status(200)] }),
  request(id, `setup: ${name} serves ${title}`, "POST", `/control/${name}/tools/set`, { base: CHAOS, noAuth: true, body: { tools }, asserts: [A.status(200)] }),
];
const setInstructions = (id, server, text, label) => mockPost(id, server, "instructions", `setup: ${server} sends ${label} as its instructions`, { text });
// digits is n bytes of 0-9 repeated, so a cut point is visible in the text itself.
const digits = (n) => Array.from({ length: n }, (_, i) => String(i % 10)).join("");
// block is one server's tagged section of the aggregated instructions.
const block = (name, body) => `<mcp_server name="${name}">\n${body}\n</mcp_server>`;
const instructionsEqual = (v, label) => A.custom(`instructions are ${label || lit(v)}`, [
  `pm.expect(pm.response.json().result.instructions, 'body: ' + pm.response.text()).to.equal(${lit(v)});`,
]);
// fleetInstructions is the two-server fleet with instructions on alpha and/or beta, set before the clients are created
// (a per-call client reads them when it is created). betaFirst registers beta first, so name order is not creation order.
const fleetInstructions = (id, { alpha, beta, alphaClient = {}, betaFirst = false } = {}) => {
  const p = id.toLowerCase();
  const alphaReq = createClient(id, httpClient(`${p}-alpha`, "alpha", "alpha", { tools_to_execute: ["*"], allow_by_default: false, ...alphaClient }), "setup: register client alpha");
  const betaReq = createClient(id, httpClient(`${p}-beta`, "beta", "beta", { tools_to_execute: ["*"], allow_by_default: false }), "setup: register client beta");
  return [
    ...freshServer(id, "alpha", ["echo", "add"]),
    ...freshServer(id, "beta", ["echo"]),
    ...(alpha ? [setInstructions(id, "alpha", alpha[0], alpha[1])] : []),
    ...(beta ? [setInstructions(id, "beta", beta[0], beta[1])] : []),
    ...(betaFirst ? [betaReq, alphaReq] : [alphaReq, betaReq]),
  ];
};
const initialize = (id, title, asserts, opts = {}) => rpc(id, title, init("2025-03-26"), { ...opts, asserts: [A.status(200), ...asserts] });

const EP03 = folder("EP-03 Instructions aggregate across clients", `Setup: ${FLEET}; alpha sends instructions "A1" and beta "B1"; beta is registered before alpha.`, [
  ...fleetInstructions("EP-03", { alpha: ["A1", "\"A1\""], beta: ["B1", "\"B1\""], betaFirst: true }),
  initialize("EP-03", "initialize", [instructionsEqual(block("alpha", "A1") + "\n\n" + block("beta", "B1"))]),
  ...fleetTeardown("EP-03"),
]);

const EP05 = folder("EP-05 Instructions follow caller visibility", `Setup: ${FLEET}; alpha sends instructions "A1" and beta "B1"; the echo-only VK (${VKS.echo.text}) and the full-access VK (${VKS.full.text}); finally client alpha's tools_to_execute is set to [].`, [
  ...fleetInstructions("EP-05", { alpha: ["A1", "\"A1\""], beta: ["B1", "\"B1\""] }),
  createVK("EP-05", "echo"), createVK("EP-05", "full"),
  initialize("EP-05", "control: an anonymous initialize shows both", [instructionsEqual(block("alpha", "A1") + "\n\n" + block("beta", "B1"))]),
  initialize("EP-05", "initialize with the echo-only VK", [instructionsEqual(block("alpha", "A1"), "alpha's block only")], { headers: [MCP_ACCEPT, vkHeader("EP-05", "echo")] }),
  initialize("EP-05", "initialize with include-clients beta", [instructionsEqual(block("beta", "B1"), "beta's block only")], { headers: [MCP_ACCEPT, inc("clients", "beta")] }),
  initialize("EP-05", "initialize on /mcp/beta with the full-access VK", [instructionsEqual(block("beta", "B1"), "beta's block only")], { path: "/mcp/beta", headers: [MCP_ACCEPT, vkHeader("EP-05", "full")] }),
  request("EP-05", "setup: set client alpha's tools_to_execute to []", "PUT", "/api/mcp/client/ep-05-alpha", { body: { tools_to_execute: [] }, asserts: [A.status(200)] }),
  initialize("EP-05", "initialize after alpha's tools_to_execute is []", [instructionsEqual(block("beta", "B1"), "beta's block only, alpha absent")]),
  deleteVK("EP-05", "echo"), deleteVK("EP-05", "full"),
  ...fleetTeardown("EP-05"),
]);

const EP06 = folder("EP-06 Per-client instruction cap", `Setup: ${FLEET}; mcp_max_instructions_per_client 100; alpha sends 300 bytes of instructions (the digits 0-9 repeated), beta sends none.`, [
  readConfig("EP-06"),
  putConfig("EP-06", "setup: mcp_max_instructions_per_client 100", ["cc.mcp_max_instructions_per_client = 100;"], [A.status(200)]),
  ...fleetInstructions("EP-06", { alpha: [digits(300), "300 bytes"] }),
  initialize("EP-06", "initialize", [instructionsEqual(block("alpha", digits(100) + "\n[truncated: 200 bytes omitted]"),
    "alpha's block holding its first 100 bytes plus \"\\n[truncated: 200 bytes omitted]\"")]),
  ...fleetTeardown("EP-06"),
  restoreConfig("EP-06"),
]);

const EP07 = folder("EP-07 Total instruction cap", `Setup: ${FLEET}; mcp_max_instructions_total 200; alpha and beta each send 150 bytes of instructions (the digits 0-9 repeated).`, [
  readConfig("EP-07"),
  putConfig("EP-07", "setup: mcp_max_instructions_total 200", ["cc.mcp_max_instructions_total = 200;"], [A.status(200)]),
  ...fleetInstructions("EP-07", { alpha: [digits(150), "150 bytes"], beta: [digits(150), "150 bytes"] }),
  initialize("EP-07", "initialize", [A.custom("alpha's block contains \"[truncated: \"; the output ends \"[1 more server(s) omitted: instruction size limit reached]\"; there is no beta tag", [
    "var s = pm.response.json().result.instructions;",
    "var start = s.indexOf('<mcp_server name=\"alpha\">');",
    "var end = s.indexOf('</mcp_server>', start);",
    "pm.expect(start, 'instructions: ' + s).to.equal(0);",
    "pm.expect(s.slice(start, end), 'instructions: ' + s).to.include('[truncated: ');",
    "var tail = '[1 more server(s) omitted: instruction size limit reached]';",
    "pm.expect(s.slice(-tail.length), 'instructions: ' + s).to.equal(tail);",
    "pm.expect(s, 'instructions: ' + s).to.not.include('<mcp_server name=\"beta\">');",
  ])]),
  ...fleetTeardown("EP-07"),
  restoreConfig("EP-07"),
]);

const EP08 = folder("EP-08 Per-client max_instructions_length", `Setup: ${FLEET}, except client alpha has max_instructions_length 50; mcp_max_instructions_per_client 100; alpha and beta each send 300 bytes of instructions (the digits 0-9 repeated).`, [
  readConfig("EP-08"),
  putConfig("EP-08", "setup: mcp_max_instructions_per_client 100", ["cc.mcp_max_instructions_per_client = 100;"], [A.status(200)]),
  ...fleetInstructions("EP-08", { alpha: [digits(300), "300 bytes"], beta: [digits(300), "300 bytes"], alphaClient: { max_instructions_length: 50 } }),
  initialize("EP-08", "initialize", [instructionsEqual(
    block("alpha", digits(50) + "\n[truncated: 250 bytes omitted]") + "\n\n" + block("beta", digits(100) + "\n[truncated: 200 bytes omitted]"),
    "alpha's block ending \"[truncated: 250 bytes omitted]\" and beta's cut at the global 100, \"[truncated: 200 bytes omitted]\"")]),
  ...fleetTeardown("EP-08"),
  restoreConfig("EP-08"),
]);

const EP11 = folder("EP-11 tools/list, anonymous", `Setup: ${FLEET}, alpha's echo annotated readOnlyHint true; mcp_server_auth_mode headers, enforce_auth_on_inference off; tools/list with no credential.`, [
  readConfig("EP-11"), authMode("EP-11", "H", false),
  ...serveTools("EP-11", "alpha", [{ name: "echo", annotations: { readOnlyHint: true } }, { name: "add" }], "echo (readOnlyHint true) and add"),
  ...freshServer("EP-11", "beta", ["echo"]),
  createClient("EP-11", httpClient("ep-11-alpha", "alpha", "alpha", { tools_to_execute: ["*"], allow_by_default: false }), "setup: register client alpha"),
  createClient("EP-11", httpClient("ep-11-beta", "beta", "beta", { tools_to_execute: ["*"], allow_by_default: false }), "setup: register client beta"),
  mockGet("EP-11", "alpha", "setup: record alpha's tool descriptions", [], ["var d = {}; pm.response.json().tools.forEach(function (t) { d['alpha-' + t.name] = t.description; }); pm.collectionVariables.set('ep11_alpha_desc', JSON.stringify(d));"]),
  mockGet("EP-11", "beta", "setup: record beta's tool descriptions", [], ["var d = {}; pm.response.json().tools.forEach(function (t) { d['beta-' + t.name] = t.description; }); pm.collectionVariables.set('ep11_beta_desc', JSON.stringify(d));"]),
  list("EP-11", "tools/list with no credential", "/mcp", [], [A.status(200), toolNamesInList(ALL_TOOLS),
    A.custom("every inputSchema has type \"object\"", ["pm.response.json().result.tools.forEach(function (t) { pm.expect(t.inputSchema && t.inputSchema.type, t.name).to.equal('object'); });"]),
    A.custom("descriptions match the mock's", [
      "var d = Object.assign({}, JSON.parse(pm.collectionVariables.get('ep11_alpha_desc')), JSON.parse(pm.collectionVariables.get('ep11_beta_desc')));",
      "pm.response.json().result.tools.forEach(function (t) { pm.expect(t.description, t.name).to.equal(d[t.name]); });",
    ]),
    A.custom("alpha-echo keeps readOnlyHint true", [
      "var echo = pm.response.json().result.tools.filter(function (t) { return t.name === 'alpha-echo'; })[0];",
      "pm.expect(echo.annotations && echo.annotations.readOnlyHint, 'body: ' + pm.response.text()).to.equal(true);",
    ]),
    A.custom("there is no nextCursor", ["pm.expect(pm.response.json().result, 'body: ' + pm.response.text()).to.not.have.property('nextCursor');"]),
  ]),
  ...fleetTeardown("EP-11"),
  restoreConfig("EP-11"),
]);

const EP37 = folder("EP-37 /mcp/{virtual MCP slug}", `Setup: ${FLEET}, plus gamma (echo) registered the same way; alpha, beta and gamma send instructions "A1", "B1" and "G1"; a VK granting every tool of alpha, beta and gamma; ${TEAM_TEXT}; the checks run in mcp_server_auth_mode headers and again in both, enforce_auth_on_inference off.`, [
  ...fleetInstructions("EP-37", { alpha: ["A1", "\"A1\""], beta: ["B1", "\"B1\""] }),
  ...freshServer("EP-37", "gamma", ["echo"]),
  setInstructions("EP-37", "gamma", "G1", "\"G1\""),
  createClient("EP-37", httpClient("ep-37-gamma", "gamma", "gamma", { tools_to_execute: ["*"], allow_by_default: false }), "setup: register client gamma"),
  vkCreate("EP-37", "setup: create the VK granting every tool of alpha, beta and gamma", { name: "EP-37 three-client key", mcp_configs: [{ mcp_client_name: "alpha", tools_to_execute: ["*"] }, { mcp_client_name: "beta", tools_to_execute: ["*"] }, { mcp_client_name: "gamma", tools_to_execute: ["*"] }] }, "ep-37_full"), ...teamSetup("EP-37"),
  initialize("EP-37", "control: initialize on /mcp with the VK shows all three", [instructionsEqual(block("alpha", "A1") + "\n\n" + block("beta", "B1") + "\n\n" + block("gamma", "G1"))], { headers: [MCP_ACCEPT, vkHeader("EP-37", "full")] }),
  initialize("EP-37", "control: an anonymous initialize on /mcp shows all three", [instructionsEqual(block("alpha", "A1") + "\n\n" + block("beta", "B1") + "\n\n" + block("gamma", "G1"))]),
  list("EP-37", "tools/list on /mcp/team with the VK", "/mcp/team", [vkHeader("EP-37", "full")], [A.status(200), toolNamesInList(["alpha-echo", "beta-echo"])]),
  rpc("EP-37", "tools/call alpha-add on /mcp/team with the VK", call("alpha-add", { a: 1, b: 2 }), { path: "/mcp/team", headers: [MCP_ACCEPT, vkHeader("EP-37", "full")], asserts: [A.status(200), toolResultError(null, "filtered by request context")] }),
  mockCalls("EP-37", "alpha", "add", 0),
  initialize("EP-37", "initialize on /mcp/team with the VK", [instructionsEqual(block("alpha", "A1") + "\n\n" + block("beta", "B1"), "alpha's and beta's blocks only")], { path: "/mcp/team", headers: [MCP_ACCEPT, vkHeader("EP-37", "full")] }),
  readConfig("EP-37"), authMode("EP-37", "B", false),
  initialize("EP-37", "both: control: initialize on /mcp with the VK shows all three", [instructionsEqual(block("alpha", "A1") + "\n\n" + block("beta", "B1") + "\n\n" + block("gamma", "G1"))], { headers: [MCP_ACCEPT, vkHeader("EP-37", "full")] }),
  list("EP-37", "both: tools/list on /mcp/team with the VK", "/mcp/team", [vkHeader("EP-37", "full")], [A.status(200), toolNamesInList(["alpha-echo", "beta-echo"])]),
  rpc("EP-37", "both: tools/call alpha-add on /mcp/team with the VK", call("alpha-add", { a: 1, b: 2 }), { path: "/mcp/team", headers: [MCP_ACCEPT, vkHeader("EP-37", "full")], asserts: [A.status(200), toolResultError(null, "filtered by request context")] }),
  mockCalls("EP-37", "alpha", "add", 0),
  initialize("EP-37", "both: initialize on /mcp/team with the VK", [instructionsEqual(block("alpha", "A1") + "\n\n" + block("beta", "B1"), "alpha's and beta's blocks only")], { path: "/mcp/team", headers: [MCP_ACCEPT, vkHeader("EP-37", "full")] }),
  restoreConfig("EP-37"),
  teamTeardown("EP-37"), deleteVK("EP-37", "full"),
  ...fleetTeardown("EP-37"), deleteClient("EP-37", "ep-37-gamma"),
]);

// HI_REPLY is a tool result whose only content is the text "hi".
const HI_REPLY = { content: [{ type: "text", text: "hi" }] };
const READ_ONLY_TOOL = { name: "echo_ro", annotations: { readOnlyHint: true } };

const CONN06 = folder("CONN-06 Responses-format round trip", "Setup: alpha (echo_tool replying \"hi\", add_tool); an HTTP client on alpha named conn06, auth none, needs_session_stickiness true, tools_to_execute [\"*\"]; execute in the Responses format with call_id c1.", [
  ...serveTools("CONN-06", "alpha", [{ name: "echo_tool", reply: HI_REPLY }, { name: "add_tool" }], "echo_tool (replying \"hi\") and add_tool"),
  createClient("CONN-06", httpClient("conn06-c", "conn06", "alpha", { needs_session_stickiness: true, tools_to_execute: ["*"] })),
  execBody("CONN-06", "execute conn06-echo_tool in the Responses format", { call_id: "c1", name: "conn06-echo_tool", arguments: "{}" }, [A.status(200), A.custom("the body is {type \"function_call_output\", call_id \"c1\", output \"hi\"}", [
    "pm.expect(pm.response.json(), 'body: ' + pm.response.text()).to.eql({ type: 'function_call_output', call_id: 'c1', output: 'hi' });",
  ])], { query: "?format=responses" }),
  deleteClient("CONN-06", "conn06-c"),
]);

const CONN09 = folder("CONN-09 Protocol errors and 5xx", "Setup: alpha (echo_tool, add_tool); an HTTP client on alpha named conn09, auth none, per call, tools_to_execute [\"*\"]. Each case resets alpha's counters and scopes its fault to tools/call: err_rate 1 with err_kind protocol; status 500 always; status 503 once.", [
  ...freshServer("CONN-09", "alpha", ["echo_tool", "add_tool"]),
  createClient("CONN-09", httpClient("conn09-c", "conn09", "alpha", { tools_to_execute: ["*"] })),
  mockPost("CONN-09", "alpha", "faults", "setup: alpha's tools/call fails with a JSON-RPC error", { faults: { err_rate: 1, err_kind: "protocol", methods: ["tools/call"] } }),
  mockPost("CONN-09", "alpha", "counters/reset", "setup: reset alpha's counters"),
  execTool("CONN-09", "execute under err_kind protocol", "conn09-echo_tool", [A.status(400), A.msgContains("MCP tool call failed for conn09-echo_tool")]),
  mockGet("CONN-09", "alpha", "the tool was called once", [callsIs("echo_tool", 1)]),
  mockPost("CONN-09", "alpha", "faults", "setup: alpha answers tools/call with 500 always", { faults: { status: 500, methods: ["tools/call"] } }),
  mockPost("CONN-09", "alpha", "counters/reset", "setup: reset alpha's counters"),
  execTool("CONN-09", "execute under a 500 always", "conn09-echo_tool", [A.status(400)]),
  mockGet("CONN-09", "alpha", "three requests were faulted (1 plus 2 retries)", [counterIs("faulted", 3)]),
  mockPost("CONN-09", "alpha", "faults", "setup: alpha answers tools/call with 503 once", { faults: { status: 503, status_once: true, methods: ["tools/call"] } }),
  mockPost("CONN-09", "alpha", "counters/reset", "setup: reset alpha's counters"),
  execTool("CONN-09", "execute under a 503 once", "conn09-echo_tool", [A.status(200)]),
  mockGet("CONN-09", "alpha", "one request was faulted", [counterIs("faulted", 1)]),
  mockPost("CONN-09", "alpha", "faults/clear", "teardown: clear alpha's faults"),
  deleteClient("CONN-09", "conn09-c"),
]);

const CONN10 = folder("CONN-10 Tool execution timeout", "Setup: alpha (echo_tool, add_tool); HTTP clients on alpha, auth none, per call, tools_to_execute [\"*\"]: conn10o with tool_execution_timeout 2s, then conn10d with the default timeout. alpha's faults are scoped to tools/call: latency 5s for the override, hang 35s for the default.", [
  ...freshServer("CONN-10", "alpha", ["echo_tool", "add_tool"]),
  createClient("CONN-10", httpClient("conn10-o", "conn10o", "alpha", { tool_execution_timeout: 2, tools_to_execute: ["*"] }), "setup: create conn10o with a 2s tool timeout"),
  mockPost("CONN-10", "alpha", "faults", "setup: alpha answers tools/call after 5s", { faults: { latency: "5s", methods: ["tools/call"] } }),
  execTool("CONN-10", "execute with the 2s override", "conn10o-echo_tool", [A.status(400), A.msgContains("MCP tool call timed out after 2s: conn10o-echo_tool"), responseTime(1500, 4500)]),
  createClient("CONN-10", httpClient("conn10-d", "conn10d", "alpha", { tools_to_execute: ["*"] }), "setup: create conn10d with the default tool timeout"),
  mockPost("CONN-10", "alpha", "faults", "setup: alpha hangs 35s on tools/call", { faults: { hang: "35s", methods: ["tools/call"] } }),
  execTool("CONN-10", "execute with the default timeout", "conn10d-echo_tool", [A.status(400), A.msgContains("timed out after 30s")]),
  mockPost("CONN-10", "alpha", "faults/clear", "teardown: clear alpha's faults"),
  deleteClient("CONN-10", "conn10-o"), deleteClient("CONN-10", "conn10-d"),
]);

// waitForMock polls a fleet server's state, 250ms apart for up to about 10s, until cond (ES5 on the state s) holds.
const waitForMock = (server, cond) => [
  "var left = 40;",
  "function poll() {",
  `  pm.sendRequest({ url: pm.variables.get('chaos_url') + '/control/${server}', method: 'GET' }, function (err, res) {`,
  "    var s = !err && res.code === 200 ? res.json() : null;",
  `    if ((s && (${cond})) || --left <= 0) { return; }`,
  "    setTimeout(poll, 250);",
  "  });",
  "}",
  "poll();",
];

// execToolAfter is execTool with a pre-request script, for a call that must wait for something first.
const execToolAfter = (id, title, tool, asserts, pre) => request(id, title, "POST", "/v1/mcp/tool/execute", {
  noAuth: true, noCookies: true, asserts, pre,
  body: { id: "c1", type: "function", function: { name: tool, arguments: lit({ text: "hi" }) } },
});

const CONN12 = folder("CONN-12 Dead session (sticky)", "Setup: alpha (echo_ro annotated readOnlyHint true, echo_tool unannotated); an HTTP client on alpha named conn12, auth none, needs_session_stickiness true, tools_to_execute [\"*\"]. alpha's sessions are killed before the read-only call and again before the two unannotated calls; the second unannotated call waits up to 10s for Bifrost's background reconnect to open a new session.", [
  ...serveTools("CONN-12", "alpha", [READ_ONLY_TOOL, { name: "echo_tool" }], "echo_ro (readOnlyHint true) and echo_tool"),
  createClient("CONN-12", httpClient("conn12-c", "conn12", "alpha", { needs_session_stickiness: true, tools_to_execute: ["*"] })),
  saveSessions("CONN-12", "alpha", "conn12_s1"),
  mockPost("CONN-12", "alpha", "sessions/kill", "setup: kill alpha's sessions"),
  execTool("CONN-12", "execute the read-only tool", "conn12-echo_ro", [A.status(200)]),
  mockGet("CONN-12", "alpha", "the read-only call opened one new session", [sessionsPlus("conn12_s1", 1)]),
  mockPost("CONN-12", "alpha", "sessions/kill", "setup: kill alpha's sessions again"),
  saveSessions("CONN-12", "alpha", "conn12_s2"),
  execTool("CONN-12", "first execute call of the unannotated tool", "conn12-echo_tool", [A.status(400), A.msgContains("session terminated")]),
  execToolAfter("CONN-12", "second execute call of the unannotated tool, once the background reconnect has opened a session", "conn12-echo_tool", [A.status(200)],
    waitForMock("alpha", "s.counters.sessions > Number(pm.collectionVariables.get('conn12_s2'))")),
  deleteClient("CONN-12", "conn12-c"),
]);

const CONN20 = folder("CONN-20 STDIO spawn, args and env", "Setup: a fleet server delta serving env_tool, which answers with CHAOS_TAG from the environment its STDIO bridge registers; a STDIO client named delta whose command is the mcpchaos bridge for delta (-stdio -fleet <fleet URL> -server delta), envs [\"CHAOS_TAG=t1\"], auth none, tools_to_execute [\"*\"].", [
  ...serveTools("CONN-20", "delta", [{ name: "env_tool", reply_env: "CHAOS_TAG" }], "env_tool (answering with CHAOS_TAG)"),
  createClient("CONN-20", stdioClient("conn20-d", "delta", "delta", { tools_to_execute: ["*"], stdio_config: { command: "{{chaos_bin}}", args: ["-stdio", "-fleet", CHAOS, "-server", "delta"], envs: ["CHAOS_TAG=t1"] } }), "setup: create the STDIO client delta"),
  getClient("CONN-20", "conn20-d", "the client list shows healthy", [clientState("healthy")]),
  mockGet("CONN-20", "delta", "delta has a registered pid", [A.custom("stdio_pid is above 0", ["pm.expect(pm.response.json().stdio_pid, 'body: ' + pm.response.text()).to.be.above(0);"])]),
  execTool("CONN-20", "execute delta-env_tool", "delta-env_tool", [A.status(200), contentEquals("t1")]),
  deleteClient("CONN-20", "conn20-d"),
  request("CONN-20", "teardown: remove delta", "POST", "/control", { base: CHAOS, noAuth: true, body: { remove: "delta" }, asserts: [A.status(200)] }),
]);

// Per-method request counts, read just before a step; methodsPlus checks the change since.
const initializeMethods = "((pm.response.json().counters.methods || {}).initialize || 0)";
const saveMethods = (id, server, prefix) => mockGet(id, server, `setup: record ${server}'s initialize and tools/call counts`, [], [
  `pm.collectionVariables.set(${lit(prefix + "_init")}, ${initializeMethods});`,
  `pm.collectionVariables.set(${lit(prefix + "_call")}, ${toolsCallMethods});`,
]);
const methodsPlus = (prefix, init, toolsCall) => A.custom(`initialize +${init}${toolsCall !== undefined ? `, tools/call +${toolsCall}` : ""} since the recorded counts`, [
  `pm.expect(${initializeMethods}, 'body: ' + pm.response.text()).to.equal(Number(pm.collectionVariables.get(${lit(prefix + "_init")})) + ${init});`,
  ...(toolsCall !== undefined ? [`pm.expect(${toolsCallMethods}, 'body: ' + pm.response.text()).to.equal(Number(pm.collectionVariables.get(${lit(prefix + "_call")})) + ${toolsCall});`] : []),
]);

const FAIL04 = folder("FAIL-04 503 on the per-call connect", "Setup: alpha (echo_tool, add_tool) as client alpha, per call, tools_to_execute [\"*\"]; alpha answers with 503 once, then always. Counts are alpha's per-method request counts, read just before each step.", [
  ...setClient("FAIL-04", {}),
  saveMethods("FAIL-04", "alpha", "fail04_a"),
  mockPost("FAIL-04", "alpha", "faults", "setup: alpha answers the next request with 503", { faults: { status: 503, status_once: true } }),
  setExec("FAIL-04", "execute echo_tool after a 503 once", "echo_tool", [A.status(200)]),
  mockGet("FAIL-04", "alpha", "initialize +2", [methodsPlus("fail04_a", 2)]),
  saveMethods("FAIL-04", "alpha", "fail04_b"),
  mockPost("FAIL-04", "alpha", "faults", "setup: alpha answers every request with 503", { faults: { status: 503 } }),
  setExec("FAIL-04", "execute echo_tool under a 503 always", "echo_tool", [A.status(400), A.msgContains("failed to establish ephemeral MCP connection"), A.msgContains("503")]),
  mockGet("FAIL-04", "alpha", "initialize +4, tools/call +0", [methodsPlus("fail04_b", 4, 0)]),
  mockPost("FAIL-04", "alpha", "faults/clear", "teardown: clear alpha's faults"),
  deleteClient("FAIL-04", "fail-04-a"),
]);

// fail05Watch polls the client list and alpha for the whole 5s after the call, recording every state the client list
// showed and how long it took until the client was unstable with one more initialize at alpha.
const fail05Watch = [
  "var t0 = Date.now(); var seen = []; var first = null;",
  "var before = Number(pm.collectionVariables.get('fail05_init'));",
  "function poll() {",
  "  pm.sendRequest({ url: pm.variables.get('base_url') + '/api/mcp/clients?server=fail-05-a', method: 'GET', header: { Authorization: pm.variables.get('admin_auth_header') } }, function (err, res) {",
  "    var c = !err && res.code === 200 ? res.json().clients[0] : null;",
  "    if (c) { seen.push(c.state); }",
  "    pm.sendRequest({ url: pm.variables.get('chaos_url') + '/control/alpha', method: 'GET' }, function (e2, r2) {",
  "      var init = !e2 && r2.code === 200 ? ((r2.json().counters.methods || {}).initialize || 0) : 0;",
  "      if (first === null && c && c.state === 'unstable' && init >= before + 1) { first = Date.now() - t0; }",
  "      if (Date.now() - t0 >= 5000) { pm.variables.set('fail05_seen', JSON.stringify(seen)); pm.variables.set('fail05_first', first === null ? '' : String(first)); return; }",
  "      setTimeout(poll, 250);",
  "    });",
  "  });",
  "}",
  "poll();",
];

const FAIL05 = folder("FAIL-05 401 with WWW-Authenticate on an unannotated tool", "Setup: alpha (echo_tool, add_tool) as client alpha, sticky, tools_to_execute [\"*\"]; alpha answers every request with 401 and WWW-Authenticate: Bearer realm=\"chaosmock\". After the call, up to 5s is watched for Bifrost's reconnect.", [
  ...setClient("FAIL-05", { needs_session_stickiness: true }),
  saveMethods("FAIL-05", "alpha", "fail05"),
  mockPost("FAIL-05", "alpha", "faults", "setup: alpha answers 401 with WWW-Authenticate", { faults: { status: 401, www_auth: "Bearer realm=\"chaosmock\"" } }),
  setExec("FAIL-05", "execute echo_tool", "echo_tool", [A.status(400), A.msgContains("status 401")]),
  getClient("FAIL-05", "fail-05-a", "within 5s the client is unstable at stage connect, and never needs_reauth", [
    clientState("unstable"),
    A.custom("last_failure.stage is \"connect\"", ["pm.expect((pm.response.json().clients[0].last_failure || {}).stage, 'body: ' + pm.response.text()).to.equal('connect');"]),
    A.custom("unstable with one more initialize within 5000ms", ["var first = pm.variables.get('fail05_first');", "pm.expect(first, 'states seen: ' + pm.variables.get('fail05_seen')).to.not.equal('');", "pm.expect(Number(first)).to.be.at.most(5000);"]),
    A.custom("no state seen in the 5s was \"needs_reauth\"", ["pm.expect(JSON.parse(pm.variables.get('fail05_seen') || '[]'), 'states seen').to.not.include('needs_reauth');"]),
  ], { pre: fail05Watch }),
  mockGet("FAIL-05", "alpha", "tools/call +1 and initialize +1", [methodsPlus("fail05", 1, 1)]),
  mockPost("FAIL-05", "alpha", "faults/clear", "teardown: clear alpha's faults"),
  deleteClient("FAIL-05", "fail-05-a"),
]);

const FAIL06 = folder("FAIL-06 401 once on a read-only tool", "Setup: alpha (echo_tool, add_tool, and echo_ro annotated readOnlyHint true) as client alpha, sticky, tools_to_execute [\"*\"]; alpha answers the next request with 401 once.", [
  ...serveTools("FAIL-06", "alpha", [{ name: "echo_tool" }, { name: "add_tool" }, READ_ONLY_TOOL], "echo_tool, add_tool and echo_ro (readOnlyHint true)"),
  createClient("FAIL-06", httpClient("fail-06-a", "alpha", "alpha", { needs_session_stickiness: true, tools_to_execute: ["*"] }), "setup: create client alpha"),
  mockGet("FAIL-06", "alpha", "setup: record alpha's echo_ro calls, tools/call count and sessions", [], [
    "var c = pm.response.json().counters;",
    "pm.collectionVariables.set('fail06_ro', (c.calls || {}).echo_ro || 0);",
    `pm.collectionVariables.set('fail06_call', ${toolsCallMethods});`,
    "pm.collectionVariables.set('fail06_s', c.sessions);",
  ]),
  mockPost("FAIL-06", "alpha", "faults", "setup: alpha answers the next request with 401", { faults: { status: 401, status_once: true } }),
  setExec("FAIL-06", "execute echo_ro", "echo_ro", [A.status(200)]),
  mockGet("FAIL-06", "alpha", "calls[echo_ro] +1, tools/call +2, sessions +1", [A.custom("calls[echo_ro] +1, tools/call +2, sessions +1 since the recorded counts", [
    "var c = pm.response.json().counters;",
    "pm.expect((c.calls || {}).echo_ro || 0, 'body: ' + pm.response.text()).to.equal(Number(pm.collectionVariables.get('fail06_ro')) + 1);",
    `pm.expect(${toolsCallMethods}, 'body: ' + pm.response.text()).to.equal(Number(pm.collectionVariables.get('fail06_call')) + 2);`,
    "pm.expect(c.sessions, 'body: ' + pm.response.text()).to.equal(Number(pm.collectionVariables.get('fail06_s')) + 1);",
  ])]),
  mockPost("FAIL-06", "alpha", "faults/clear", "teardown: clear alpha's faults"),
  deleteClient("FAIL-06", "fail-06-a"),
]);

const FAIL07 = folder("FAIL-07 401 at create is not retried", "Setup: alpha (echo_tool, add_tool) answering every request with 401; create client alpha, sticky, tools_to_execute [\"*\"].", [
  ...freshServer("FAIL-07", "alpha", ["echo_tool", "add_tool"]),
  saveMethods("FAIL-07", "alpha", "fail07"),
  mockPost("FAIL-07", "alpha", "faults", "setup: alpha answers every request with 401", { faults: { status: 401 } }),
  createExpect("FAIL-07", "create the sticky client", httpClient("fail-07-a", "alpha", "alpha", { needs_session_stickiness: true, tools_to_execute: ["*"] }), [A.status(500), A.msgContains("401"), responseTime(null, 5000)]),
  mockGet("FAIL-07", "alpha", "initialize +1", [methodsPlus("fail07", 1)]),
  mockPost("FAIL-07", "alpha", "faults/clear", "teardown: clear alpha's faults"),
]);

// ================================================================ shared upstream OAuth against the mock's authorization server

// Every test here runs with mcp_oauth_private_allowlist ["127.0.0.1"] (Bifrost dials the mock's authorization server on
// loopback) and restores the config at the end. Each test has its own fleet server, so its authorization server starts empty.
const ALLOWLIST_TEXT = "mcp_oauth_private_allowlist [\"127.0.0.1\"]";
const allowlistOn = (id) => [readConfig(id), putConfig(id, `setup: ${ALLOWLIST_TEXT}`, ["cc.mcp_oauth_private_allowlist = ['127.0.0.1'];"], [A.status(200)])];
// asServer re-adds a fleet server (echo_tool and add_tool unless tools are given) and makes it an OAuth protected
// resource with its own authorization server configured by cfg.
const asServer = (id, name, cfg = {}, tools) => [
  ...(tools ? serveTools(id, name, tools.list, tools.title) : freshServer(id, name, ["echo_tool", "add_tool"])),
  mockAuth(id, name, `setup: ${name} is an OAuth protected resource with its own authorization server${Object.keys(cfg).length ? " (" + lit(cfg) + ")" : ""}`, { oauth: cfg }),
];
const oauthBody = (clientId, name, server, over = {}) => ({ client_id: clientId, name, connection_type: "http", connection_string: `${CHAOS}/s/${server}/mcp`, auth_type: "oauth", oauth_config: {}, tools_to_execute: ["*"], ...over });
// createOAuth creates an OAuth client and keeps its oauth_config_id and authorize_url in <p>_oc and <p>_au.
const createOAuth = (id, title, body, p, asserts = []) => request(id, title, "POST", "/api/mcp/client", {
  body, asserts: [A.status(200), ...asserts],
  extraTests: [`pm.collectionVariables.set('${p}_oc', pm.response.json().oauth_config_id);`, `pm.collectionVariables.set('${p}_au', pm.response.json().authorize_url);`],
});
// consentAs follows <p>_au on the mock without following the redirect, approving as identity (login_hint), and keeps
// the Location, Bifrost's callback, in <p>_cb.
const consentAs = (id, title, p, identity, asserts = [A.status(302)]) => request(id, title, "GET", identity ? `&login_hint=${identity}` : "", {
  base: `{{${p}_au}}`, noAuth: true, noRedirect: true, noCookies: true, asserts,
  extraTests: [`pm.collectionVariables.set('${p}_cb', pm.response.headers.get('Location'));`],
});
const followCallback = (id, title, p, asserts) => request(id, title, "GET", "", { base: `{{${p}_cb}}`, noAuth: true, noRedirect: true, noCookies: true, asserts });
const completeOAuth = (id, title, p, asserts) => request(id, title, "POST", `/api/mcp/client/{{${p}_oc}}/complete-oauth`, { asserts });
const oauthStatus = (id, title, p, query, asserts, extraTests = []) => request(id, title, "GET", `/api/oauth/config/{{${p}_oc}}/status${query}`, { asserts: [A.status(200), ...asserts], extraTests });
const locationEnds = (s) => A.custom(`Location ends ${lit(s)}`, [
  "var loc = pm.response.headers.get('Location') || '';",
  `pm.expect(loc.slice(-${s.length}), 'Location: ' + loc).to.equal(${lit(s)});`,
]);
const SUCCESS_CB = "/workspace/mcp-registry/oauth-callback?status=success";
const callbackSuccess = () => [A.status(302), locationEnds(SUCCESS_CB)];
// connectOAuth creates an OAuth client and takes it through consent, callback and complete.
const connectOAuth = (id, body, p, identity, label) => [
  createOAuth(id, `setup: create ${label}`, body, p),
  consentAs(id, `setup: consent for ${label}${identity ? " as " + identity : ""}`, p, identity),
  followCallback(id, `setup: follow the callback for ${label}`, p, callbackSuccess()),
  completeOAuth(id, `setup: complete ${label}`, p, [A.status(200)]),
];
// QS_FN parses a URL's query string into an object (ES5, for test scripts).
const QS_FN = "function qs(u) { var o = {}; (String(u).split('?')[1] || '').split('#')[0].split('&').forEach(function (kv) { var i = kv.indexOf('='); if (i > 0) { o[decodeURIComponent(kv.slice(0, i))] = decodeURIComponent(kv.slice(i + 1).replace(/\\+/g, ' ')); } }); return o; }";
const statusIs = (v) => A.custom(`status is ${lit(v)}`, [`pm.expect(pm.response.json().status, 'body: ' + pm.response.text()).to.equal(${lit(v)});`]);
const credStatus = (v) => A.custom(`credential status is ${lit(v)}`, [`pm.expect((pm.response.json().clients[0].credential || {}).status, 'body: ' + pm.response.text()).to.equal(${lit(v)});`]);
const notListed = (id, clientId) => request(id, `${clientId} is not listed`, "GET", `/api/mcp/clients?server=${clientId}`, { asserts: [A.status(200), A.custom("no client is returned", ["pm.expect(pm.response.json().clients, 'body: ' + pm.response.text()).to.eql([]);"])] });
const oauthCounters = (id, server, title, expected) => mockGet(id, server, title, [A.custom(`oauth counters include ${lit(expected)}`, [
  "var c = pm.response.json().oauth.counters;",
  ...Object.entries(expected).map(([k, v]) => `pm.expect(c[${lit(k)}], 'counters: ' + JSON.stringify(c)).to.equal(${v});`),
])]);
const execAfterWait = (id, title, tool, asserts, ms) => execToolAfter(id, title, tool, asserts, [`setTimeout(function () {}, ${ms});`]);
// mockGetAfter and mockPostAfter are mockGet and mockPost with a pre-request script, for a step that must wait first.
const mockGetAfter = (id, name, title, asserts, pre) => request(id, title, "GET", `/control/${name}`, { base: CHAOS, noAuth: true, asserts: [A.status(200), ...asserts], pre });
const mockPostAfter = (id, name, verb, title, body, pre) => request(id, title, "POST", `/control/${name}/${verb}`, { base: CHAOS, noAuth: true, body, asserts: [A.status(200)], pre });
// saveOAuthCounts keeps a server's refresh count and session count in <p>_rf and <p>_ss.
const saveOAuthCounts = (id, server, p) => mockGet(id, server, `setup: record ${server}'s refresh and session counts`, [], [
  `pm.collectionVariables.set('${p}_rf', pm.response.json().oauth.counters.token_refresh);`,
  `pm.collectionVariables.set('${p}_ss', pm.response.json().counters.sessions);`,
]);
const refreshPlus = (p, n) => `pm.expect(pm.response.json().oauth.counters.token_refresh, 'body: ' + pm.response.text()).to.equal(Number(pm.collectionVariables.get('${p}_rf')) + ${n});`;
const sessionsPlusP = (p, n) => `pm.expect(pm.response.json().counters.sessions, 'body: ' + pm.response.text()).to.equal(Number(pm.collectionVariables.get('${p}_ss')) + ${n});`;

const AUTH11 = folder("AUTH-11 OAuth create with discovery and DCR", `Setup: ${ALLOWLIST_TEXT}; a fleet server auth11 (echo_tool, add_tool) as an OAuth protected resource whose 401 names its resource metadata and scope "mcp.read", with an authorization server that supports dynamic registration; an OAuth client created on it with oauth_config {}.`, [
  ...allowlistOn("AUTH-11"),
  ...asServer("AUTH-11", "auth11", { challenge_scope: "mcp.read" }),
  request("AUTH-11", "control: an anonymous GET on auth11 is a 401 naming resource_metadata and scope", "GET", "/s/auth11/mcp", { base: CHAOS, noAuth: true, asserts: [A.status(401), A.custom("WWW-Authenticate names resource_metadata and scope=\"mcp.read\"", [
    "var w = pm.response.headers.get('WWW-Authenticate') || '';",
    "pm.expect(w, 'WWW-Authenticate').to.include('resource_metadata=');",
    "pm.expect(w, 'WWW-Authenticate').to.include('scope=\"mcp.read\"');",
  ])] }),
  createOAuth("AUTH-11", "create the OAuth client", oauthBody("auth11-c", "auth11", "auth11"), "auth11", [
    statusIs("pending_oauth"),
    A.custom("oauth_config_id, complete_url, status_url and expires_at are set", ["var b = pm.response.json();", "['oauth_config_id', 'complete_url', 'status_url', 'expires_at'].forEach(function (k) { pm.expect(b[k], k + ' in ' + pm.response.text()).to.be.a('string').and.not.empty; });"]),
    A.custom("authorize_url has response_type code, the callback redirect, S256, scope mcp.read and a state", [QS_FN,
      "var q = qs(pm.response.json().authorize_url);",
      "pm.expect(q.response_type, 'query').to.equal('code');",
      "pm.expect(/\\/api\\/oauth\\/callback$/.test(q.redirect_uri || ''), 'redirect_uri ' + q.redirect_uri).to.equal(true);",
      "pm.expect(q.code_challenge_method, 'query').to.equal('S256');",
      "pm.expect(q.code_challenge, 'query').to.be.a('string').and.not.empty;",
      "pm.expect(q.scope, 'query').to.equal('mcp.read');",
      "pm.expect(q.state, 'query').to.be.a('string').and.not.empty;",
    ]),
  ]),
  mockGet("AUTH-11", "auth11", "one registration, auth method \"none\", whose client_id is the authorize_url's", [A.custom("one registered client with token_endpoint_auth_method none, matching authorize_url's client_id", [QS_FN,
    "var o = pm.response.json().oauth;",
    "pm.expect(o.counters.register, 'oauth: ' + JSON.stringify(o)).to.equal(1);",
    "pm.expect(o.clients.length).to.equal(1);",
    "pm.expect(o.clients[0].token_endpoint_auth_method).to.equal('none');",
    "pm.expect(qs(pm.collectionVariables.get('auth11_au')).client_id).to.equal(o.clients[0].client_id);",
  ])]),
  restoreConfig("AUTH-11"),
]);

const R1 = "https://r1.mcp-harness.example/mcp";
const AUTH12 = folder("AUTH-12 Discovery fallbacks", `Setup: ${ALLOWLIST_TEXT}; a fleet server auth12 (echo_tool, add_tool) as an OAuth protected resource whose 401 is a bare Bearer challenge, with its metadata only at the path-inserted URL, publishing resource ${R1}; an OAuth client created on it with oauth_config {}.`, [
  ...allowlistOn("AUTH-12"),
  ...asServer("AUTH-12", "auth12", { challenge: "bare", resource: R1 }),
  request("AUTH-12", "control: the 401 challenge is bare", "GET", "/s/auth12/mcp", { base: CHAOS, noAuth: true, asserts: [A.status(401), A.custom("WWW-Authenticate is Bearer realm=\"chaosmock\"", ["pm.expect(pm.response.headers.get('WWW-Authenticate')).to.equal('Bearer realm=\"chaosmock\"');"])] }),
  request("AUTH-12", "control: there is no root resource metadata", "GET", "/.well-known/oauth-protected-resource", { base: CHAOS, noAuth: true, asserts: [A.status(404)] }),
  request("AUTH-12", `control: the path-inserted resource metadata publishes ${R1}`, "GET", "/.well-known/oauth-protected-resource/s/auth12/mcp", { base: CHAOS, noAuth: true, asserts: [A.status(200), A.custom(`resource is ${R1}`, [`pm.expect(pm.response.json().resource).to.equal(${lit(R1)});`])] }),
  createOAuth("AUTH-12", "create the OAuth client", oauthBody("auth12-c", "auth12", "auth12"), "auth12", [statusIs("pending_oauth"),
    A.custom(`authorize_url includes resource=${R1}`, [QS_FN, `pm.expect(qs(pm.response.json().authorize_url).resource, 'body: ' + pm.response.text()).to.equal(${lit(R1)});`])]),
  restoreConfig("AUTH-12"),
]);

const R2 = "https://r2.mcp-harness.example/mcp";
const AUTH14 = folder("AUTH-14 Pre-registered client skips discovery", `Setup: ${ALLOWLIST_TEXT}; a fleet server auth14 (echo_tool, add_tool) as an OAuth protected resource whose authorization server knows client C1 with secret S1; an OAuth client on it with oauth_config client_id C1, client_secret S1, the authorization server's authorize and token URLs, scopes ["a","b"] and resource ${R2}; consent and callback run so the token request is made.`, [
  ...allowlistOn("AUTH-14"),
  ...asServer("AUTH-14", "auth14", { clients: [{ client_id: "C1", client_secret: "S1" }] }),
  createOAuth("AUTH-14", "create the OAuth client", oauthBody("auth14-c", "auth14", "auth14", { oauth_config: { client_id: "C1", client_secret: "S1", authorize_url: `${CHAOS}/s/auth14/as/authorize`, token_url: `${CHAOS}/s/auth14/as/token`, scopes: ["a", "b"], resource: R2 } }), "auth14", [
    A.custom(`authorize_url has client_id C1, scope "a b" and resource ${R2}`, [QS_FN, "var q = qs(pm.response.json().authorize_url);", "pm.expect(q.client_id, 'query').to.equal('C1');", "pm.expect(q.scope, 'query').to.equal('a b');", `pm.expect(q.resource, 'query').to.equal(${lit(R2)});`]),
  ]),
  oauthCounters("AUTH-14", "auth14", "no registration and no metadata fetch", { register: 0, metadata: 0, prm: 0 }),
  consentAs("AUTH-14", "consent", "auth14", null),
  followCallback("AUTH-14", "follow the callback", "auth14", callbackSuccess()),
  mockGet("AUTH-14", "auth14", `the token request carried client_secret S1, resource ${R2} and a code_verifier`, [A.custom(`one authorization_code request with client_secret S1, resource ${R2} and a verified code_verifier`, [
    "var t = pm.response.json().oauth.log.filter(function (e) { return e.endpoint === 'token'; });",
    "pm.expect(t.length, 'token log: ' + JSON.stringify(t)).to.equal(1);",
    "pm.expect(t[0].grant_type).to.equal('authorization_code');",
    "pm.expect(t[0].client_secret).to.equal('S1');",
    `pm.expect(t[0].resource).to.equal(${lit(R2)});`,
    "pm.expect(t[0].pkce).to.equal('ok');",
  ])]),
  oauthCounters("AUTH-14", "auth14", "still no registration and no metadata fetch after the exchange", { register: 0, metadata: 0, prm: 0 }),
  restoreConfig("AUTH-14"),
]);

const AUTH15 = folder("AUTH-15 OAuth create failures", `Setup: ${ALLOWLIST_TEXT}. Fleet server auth15a's authorization server has no registration endpoint; auth15b's answers the next registration with 400 invalid_client_metadata. Both creates use oauth_config {}; the last two creates have no oauth_config, and no client_id with no URL.`, [
  ...allowlistOn("AUTH-15"),
  ...asServer("AUTH-15", "auth15a", { registration: false }),
  request("AUTH-15", "create against an authorization server with no registration endpoint", "POST", "/api/mcp/client", { body: oauthBody("auth15-a", "auth15a", "auth15a"), asserts: [A.msgContains("client_id is required when the OAuth provider does not support dynamic client registration")] }),
  ...asServer("AUTH-15", "auth15b"),
  mockPost("AUTH-15", "auth15b", "oauth/fail", "setup: auth15b answers the next registration with 400", { fail: { endpoint: "register", status: 400, error: "invalid_client_metadata" } }),
  request("AUTH-15", "create when registration answers 400", "POST", "/api/mcp/client", { body: oauthBody("auth15-b", "auth15b", "auth15b"), asserts: [A.msgContains("dynamic client registration failed")] }),
  request("AUTH-15", "create with no oauth_config", "POST", "/api/mcp/client", { body: (({ oauth_config, ...rest }) => rest)(oauthBody("auth15-c", "auth15c", "auth15a")), asserts: [A.status(400)] }),
  request("AUTH-15", "create with no client_id and no URL", "POST", "/api/mcp/client", { body: { client_id: "auth15-d", name: "auth15d", connection_type: "http", auth_type: "oauth", oauth_config: {} }, asserts: [A.status(400)] }),
  restoreConfig("AUTH-15"),
]);

const AUTH16 = folder("AUTH-16 Pending to connected", `Setup: ${ALLOWLIST_TEXT}; the AUTH-11 setup on fleet server auth16 (scope "mcp.read", dynamic registration); its authorization server verifies S256; consent approves as id1.`, [
  ...allowlistOn("AUTH-16"),
  ...asServer("AUTH-16", "auth16", { challenge_scope: "mcp.read" }),
  createOAuth("AUTH-16", "setup: create the OAuth client", oauthBody("auth16-c", "auth16", "auth16"), "auth16"),
  oauthStatus("AUTH-16", "status before consent", "auth16", "", [statusIs("pending")]),
  completeOAuth("AUTH-16", "complete before consent", "auth16", [A.status(400), A.msgContains("OAuth not authorized yet")]),
  notListed("AUTH-16", "auth16-c"),
  consentAs("AUTH-16", "consent as id1", "auth16", "id1"),
  followCallback("AUTH-16", "follow the callback", "auth16", callbackSuccess()),
  oauthStatus("AUTH-16", "status after the callback", "auth16", "", [statusIs("authorized")]),
  completeOAuth("AUTH-16", "complete", "auth16", [A.status(200), A.jsonMessage("MCP client connected successfully with OAuth")]),
  mockGet("AUTH-16", "auth16", "the code exchange passed S256", [A.custom("the authorization_code request's pkce is ok", ["var t = pm.response.json().oauth.log.filter(function (e) { return e.grant_type === 'authorization_code'; });", "pm.expect(t.length).to.equal(1);", "pm.expect(t[0].pkce).to.equal('ok');"])]),
  execTool("AUTH-16", "execute auth16-echo_tool", "auth16-echo_tool", [A.status(200)]),
  mockGet("AUTH-16", "auth16", "the call is counted under id1", [identityCallsAre({ id1: 1 })]),
  deleteClient("AUTH-16", "auth16-c"),
  restoreConfig("AUTH-16"),
]);

const AUTH17 = folder("AUTH-17 Callback errors", `Setup: ${ALLOWLIST_TEXT}; the callback with no code, then with an unknown state; fleet server auth17 whose authorization server denies, then (deny off) answers the next code exchange with 400 invalid_grant.`, [
  ...allowlistOn("AUTH-17"),
  request("AUTH-17", "callback with no code", "GET", "/api/oauth/callback", { noAuth: true, noRedirect: true, noCookies: true, asserts: [A.status(400), A.msgEquals("Missing required parameters: state and code")] }),
  request("AUTH-17", "callback with an unknown state", "GET", "/api/oauth/callback?state=auth17-unknown&code=x", { noAuth: true, noRedirect: true, noCookies: true, asserts: [A.status(302), A.custom("Location carries status=failed", ["pm.expect(pm.response.headers.get('Location'), 'Location').to.include('status=failed');"])] }),
  ...asServer("AUTH-17", "auth17", { deny: true }),
  createOAuth("AUTH-17", "setup: create the first OAuth client", oauthBody("auth17-a", "auth17a", "auth17"), "auth17a"),
  consentAs("AUTH-17", "the authorization server denies", "auth17a", null, [A.status(302), A.custom("Location carries error=access_denied", ["pm.expect(pm.response.headers.get('Location'), 'Location').to.include('error=access_denied');"])]),
  followCallback("AUTH-17", "callback with access_denied", "auth17a", [A.status(302), A.custom("Location carries status=failed", ["pm.expect(pm.response.headers.get('Location'), 'Location').to.include('status=failed');"])]),
  mockAuth("AUTH-17", "auth17", "setup: auth17 stops denying", { oauth: {} }),
  createOAuth("AUTH-17", "setup: create the second OAuth client", oauthBody("auth17-b", "auth17b", "auth17"), "auth17b"),
  mockPost("AUTH-17", "auth17", "oauth/fail", "setup: auth17 rejects the next code exchange", { fail: { endpoint: "token", grant_type: "authorization_code", status: 400, error: "invalid_grant" } }),
  consentAs("AUTH-17", "consent", "auth17b", null),
  followCallback("AUTH-17", "callback whose exchange is rejected", "auth17b", [A.status(302), A.custom("Location carries status=failed", ["pm.expect(pm.response.headers.get('Location'), 'Location').to.include('status=failed');"])]),
  mockGet("AUTH-17", "auth17", "the code exchange reached auth17 and was rejected with invalid_grant", [A.custom("one authorization_code token request, answered 400 invalid_grant", ["var t = pm.response.json().oauth.log.filter(function (e) { return e.endpoint === 'token' && e.grant_type === 'authorization_code'; });", "pm.expect(t.length, 'token log: ' + JSON.stringify(t)).to.equal(1);", "pm.expect(t[0].status).to.equal(400);", "pm.expect(t[0].error).to.equal('invalid_grant');"])]),
  oauthStatus("AUTH-17", "the status is failed", "auth17b", "", [statusIs("failed")]),
  completeOAuth("AUTH-17", "complete after the rejected exchange", "auth17b", [A.status(400), A.msgContains("Current status: failed")]),
  restoreConfig("AUTH-17"),
]);

const AUTH18 = folder("AUTH-18 Shared OAuth, per call: refresh with rotation", `Setup: ${ALLOWLIST_TEXT}; fleet server auth18 whose authorization server issues 5s access tokens and rotates refresh tokens; an OAuth client on it, per call, connected as id1. Each execute call waits 6s first, so each finds an expired token.`, [
  ...allowlistOn("AUTH-18"),
  ...asServer("AUTH-18", "auth18", { access_ttl: "5s", rotate_refresh: true }),
  ...connectOAuth("AUTH-18", oauthBody("auth18-c", "auth18", "auth18"), "auth18", "id1", "the OAuth client"),
  execAfterWait("AUTH-18", "first execute call, 6s later", "auth18-echo_tool", [A.status(200)], 6000),
  execAfterWait("AUTH-18", "second execute call, 6s after the first", "auth18-echo_tool", [A.status(200)], 6000),
  mockGet("AUTH-18", "auth18", "two refresh grants, the second presenting the first's new token", [A.custom("2 refresh_token requests, both 200, the second's refresh_token the first's issued one", [
    "var r = pm.response.json().oauth.log.filter(function (e) { return e.grant_type === 'refresh_token'; });",
    "pm.expect(r.length, 'refreshes: ' + JSON.stringify(r)).to.equal(2);",
    "pm.expect(r[0].status).to.equal(200);", "pm.expect(r[1].status).to.equal(200);",
    "pm.expect(r[1].refresh_token).to.equal(r[0].issued_refresh_token);",
  ])]),
  getClient("AUTH-18", "auth18-c", "the credential is active with last_refreshed_at set", [credStatus("active"), A.custom("last_refreshed_at is set", ["pm.expect(pm.response.json().clients[0].credential.last_refreshed_at, 'body: ' + pm.response.text()).to.be.a('string').and.not.empty;"])]),
  deleteClient("AUTH-18", "auth18-c"),
  restoreConfig("AUTH-18"),
]);

const AUTH19 = folder("AUTH-19 Sticky: upstream rejects an unexpired token", `Setup: ${ALLOWLIST_TEXT}; fleet server auth19 (echo_ro annotated readOnlyHint true, echo_tool unannotated) as an OAuth protected resource; an OAuth client on it, needs_session_stickiness true, connected as id1. Before each case the authorization server expires id1's access token, which Bifrost still believes is good.`, [
  ...allowlistOn("AUTH-19"),
  ...asServer("AUTH-19", "auth19", {}, { list: [READ_ONLY_TOOL, { name: "echo_tool" }], title: "echo_ro (readOnlyHint true) and echo_tool" }),
  ...connectOAuth("AUTH-19", oauthBody("auth19-c", "auth19", "auth19", { needs_session_stickiness: true }), "auth19", "id1", "the sticky OAuth client"),
  saveOAuthCounts("AUTH-19", "auth19", "auth19a"),
  mockPost("AUTH-19", "auth19", "oauth/expire", "setup: expire id1's access token", { identity: "id1" }),
  execTool("AUTH-19", "execute the read-only tool", "auth19-echo_ro", [A.status(200)]),
  mockGet("AUTH-19", "auth19", "one refresh and one new session", [A.custom("refresh +1, sessions +1", [refreshPlus("auth19a", 1), sessionsPlusP("auth19a", 1)])]),
  saveOAuthCounts("AUTH-19", "auth19", "auth19b"),
  mockPost("AUTH-19", "auth19", "oauth/expire", "setup: expire id1's access token again", { identity: "id1" }),
  execTool("AUTH-19", "first execute call of the unannotated tool", "auth19-echo_tool", [A.status(400), A.msgContains("401")]),
  mockGetAfter("AUTH-19", "auth19", "one refresh after the first unannotated call, once the detached refresh has run", [A.custom("refresh +1", [refreshPlus("auth19b", 1)])],
    waitForMock("auth19", "s.oauth.counters.token_refresh > Number(pm.collectionVariables.get('auth19b_rf'))")),
  execToolAfter("AUTH-19", "second execute call of the unannotated tool, once the background reconnect has opened a session", "auth19-echo_tool", [A.status(200)],
    waitForMock("auth19", "s.counters.sessions > Number(pm.collectionVariables.get('auth19b_ss'))")),
  deleteClient("AUTH-19", "auth19-c"),
  restoreConfig("AUTH-19"),
]);

const AUTH20 = folder("AUTH-20 Refresh token revoked", `Setup: ${ALLOWLIST_TEXT}; fleet server auth20 (echo_tool, add_tool) as an OAuth protected resource; an OAuth client on it, needs_session_stickiness true, connected as id1; then the authorization server revokes the grant, so the next refresh gets invalid_grant.`, [
  ...allowlistOn("AUTH-20"),
  ...asServer("AUTH-20", "auth20"),
  ...connectOAuth("AUTH-20", oauthBody("auth20-c", "auth20", "auth20", { needs_session_stickiness: true }), "auth20", "id1", "the sticky OAuth client"),
  mockPost("AUTH-20", "auth20", "oauth/revoke", "setup: revoke the grant", {}),
  execTool("AUTH-20", "execute", "auth20-echo_tool", [A.status(400)]),
  getClient("AUTH-20", "auth20-c", "the client list shows needs_reauth from the rejected refresh, at stage credential", [
    clientState("needs_reauth"),
    A.custom("status_reason starts \"provider rejected the refresh (HTTP 400, invalid_grant\"", ["var r = (pm.response.json().clients[0].credential || {}).status_reason || '';", "pm.expect(r.indexOf('provider rejected the refresh (HTTP 400, invalid_grant'), 'status_reason: ' + r).to.equal(0);"]),
    A.custom("last_failure.stage is \"credential\"", ["pm.expect((pm.response.json().clients[0].last_failure || {}).stage, 'body: ' + pm.response.text()).to.equal('credential');"]),
  ], { pre: waitForClient("auth20-c", "c.state === 'needs_reauth'") }),
  execTool("AUTH-20", "next execute call", "auth20-echo_tool", [A.status(400), A.msgContains("needs re-authorization")]),
  rpc("AUTH-20", "/mcp tools/call", call("auth20-echo_tool", { text: "hi" }), { headers: [MCP_ACCEPT], asserts: [A.status(200), toolResultError(null, "needs re-authorization")] }),
  deleteClient("AUTH-20", "auth20-c"),
  restoreConfig("AUTH-20"),
]);

const refreshStatuses = (v) => A.custom(`refresh_token requests answered ${lit(v)}`, [`pm.expect(pm.response.json().oauth.log.filter(function (e) { return e.grant_type === 'refresh_token'; }).map(function (e) { return e.status; }), 'body: ' + pm.response.text()).to.eql(${lit(v)});`]);
const AUTH21 = folder("AUTH-21 Transient refresh failure", `Setup: ${ALLOWLIST_TEXT}; fleet server auth21 whose authorization server issues 5s access tokens; an OAuth client on it, per call, connected as id1. Each case waits 6s so the token has expired, resets the server's OAuth log, and queues 503s for refresh: one, then three.`, [
  ...allowlistOn("AUTH-21"),
  ...asServer("AUTH-21", "auth21", { access_ttl: "5s" }),
  ...connectOAuth("AUTH-21", oauthBody("auth21-c", "auth21", "auth21"), "auth21", "id1", "the OAuth client"),
  mockPost("AUTH-21", "auth21", "counters/reset", "setup: reset auth21's counters and OAuth log"),
  mockPost("AUTH-21", "auth21", "oauth/fail", "setup: fail the next refresh with 503", { fail: { endpoint: "token", grant_type: "refresh_token", status: 503 } }),
  execAfterWait("AUTH-21", "execute 6s later, with one refresh failure", "auth21-echo_tool", [A.status(200)], 6000),
  mockGet("AUTH-21", "auth21", "two refresh attempts, 503 then 200", [refreshStatuses([503, 200])]),
  getClient("AUTH-21", "auth21-c", "the credential is still active", [credStatus("active")]),
  mockPost("AUTH-21", "auth21", "counters/reset", "setup: reset auth21's counters and OAuth log"),
  mockPostAfter("AUTH-21", "auth21", "oauth/fail", "setup: 6s later, fail the next three refreshes with 503", { fail: { endpoint: "token", grant_type: "refresh_token", status: 503, count: 3 } }, ["setTimeout(function () {}, 6000);"]),
  execTool("AUTH-21", "execute with three refresh failures", "auth21-echo_tool", [A.status(400), A.msgContains("token expired and refresh failed")]),
  getClient("AUTH-21", "auth21-c", "the credential is still active", [credStatus("active")]),
  deleteClient("AUTH-21", "auth21-c"),
  restoreConfig("AUTH-21"),
]);

const AUTH23 = folder("AUTH-23 Reauthorize a shared OAuth client", `Setup: ${ALLOWLIST_TEXT}; fleet servers auth23s and auth23p as OAuth protected resources; an OAuth client on each, needs_session_stickiness true on auth23s and per call on auth23p, connected as id1. Each is reauthorized, and the authorization server approves as id2.`, [
  ...allowlistOn("AUTH-23"),
  ...[["s", "sticky", { needs_session_stickiness: true }, "re-authorized and reconnected"], ["p", "per call", {}, "tools refreshed"]].flatMap(([k, mode, over, msg]) => [
    ...asServer("AUTH-23", `auth23${k}`),
    ...connectOAuth("AUTH-23", oauthBody(`auth23-${k}`, `auth23${k}`, `auth23${k}`, over), `auth23${k}`, "id1", `the ${mode} OAuth client`),
    request("AUTH-23", `reauthorize the ${mode} client`, "POST", `/api/mcp/client/auth23-${k}/reauthorize`, { asserts: [A.status(200), statusIs("pending_oauth"), A.custom("flow_id is set", ["pm.expect(pm.response.json().flow_id, 'body: ' + pm.response.text()).to.be.a('string').and.not.empty;"])],
      extraTests: [`pm.collectionVariables.set('auth23${k}_au', pm.response.json().authorize_url);`] }),
    completeOAuth("AUTH-23", `early complete for the ${mode} client`, `auth23${k}`, [A.status(409), A.msgContains("Authorization has not completed yet")]),
    consentAs("AUTH-23", `consent as id2 for the ${mode} client`, `auth23${k}`, "id2"),
    followCallback("AUTH-23", `follow the callback for the ${mode} client`, `auth23${k}`, callbackSuccess()),
    completeOAuth("AUTH-23", `complete for the ${mode} client`, `auth23${k}`, [A.status(200), A.custom(`message contains ${lit(msg)}`, [`pm.expect(pm.response.json().message, 'body: ' + pm.response.text()).to.include(${lit(msg)});`])]),
    execTool("AUTH-23", `execute on the ${mode} client`, `auth23${k}-echo_tool`, [A.status(200)]),
    mockGet("AUTH-23", `auth23${k}`, `the ${mode} client's call ran under id2`, [identityCallsAre({ id2: 1 })]),
    deleteClient("AUTH-23", `auth23-${k}`),
  ]),
  restoreConfig("AUTH-23"),
]);

const AUTH24 = folder("AUTH-24 Reauthorize, reregister and initiate guards", `Setup: ${ALLOWLIST_TEXT}; a headers client on fleet server alpha; an unknown client id; fleet server auth24 whose authorization server has no registration endpoint and knows client C1 (secret S1), with two OAuth clients on it using C1 and its authorize and token URLs: one connected, one left pending. The connected client is reauthorized, and its flow id is then asked of the pending client's OAuth config.`, [
  ...allowlistOn("AUTH-24"),
  ...freshServer("AUTH-24", "alpha", ["echo_tool", "add_tool"]),
  createClient("AUTH-24", httpClient("auth24-h", "auth24h", "alpha", { auth_type: "headers", headers: { "X-Key": "k1" } }), "setup: create the headers client"),
  request("AUTH-24", "reauthorize the headers client", "POST", "/api/mcp/client/auth24-h/reauthorize", { asserts: [A.status(400), A.msgContains("reauthorize only applies to OAuth-based clients")] }),
  request("AUTH-24", "reauthorize an unknown id", "POST", "/api/mcp/client/auth24-nope/reauthorize", { asserts: [A.status(404), A.msgEquals("MCP client 'auth24-nope' not found")] }),
  ...asServer("AUTH-24", "auth24", { registration: false, clients: [{ client_id: "C1", client_secret: "S1" }] }),
  ...connectOAuth("AUTH-24", oauthBody("auth24-c", "auth24c", "auth24", { oauth_config: { client_id: "C1", client_secret: "S1", authorize_url: `${CHAOS}/s/auth24/as/authorize`, token_url: `${CHAOS}/s/auth24/as/token` } }), "auth24c", null, "the pre-registered client"),
  request("AUTH-24", "reregister without a registration endpoint", "POST", "/api/mcp/client/auth24-c/reregister", { asserts: [A.status(400), A.msgContains("has no registration endpoint")] }),
  request("AUTH-24", "initiate-verification on the headers client", "POST", "/api/mcp/client/auth24-h/initiate-verification", { asserts: [A.status(400)] }),
  request("AUTH-24", "initiate-verification on the connected OAuth client", "POST", "/api/mcp/client/auth24-c/initiate-verification", { asserts: [A.status(400)] }),
  createOAuth("AUTH-24", "setup: create the pending OAuth client", oauthBody("auth24-p", "auth24p", "auth24", { oauth_config: { client_id: "C1", client_secret: "S1", authorize_url: `${CHAOS}/s/auth24/as/authorize`, token_url: `${CHAOS}/s/auth24/as/token` } }), "auth24p"),
  request("AUTH-24", "setup: reauthorize the connected client", "POST", "/api/mcp/client/auth24-c/reauthorize", { asserts: [A.status(200)], extraTests: ["pm.collectionVariables.set('auth24_flow', pm.response.json().flow_id);"] }),
  request("AUTH-24", "the connected client's flow id on the pending client's OAuth config", "GET", "/api/oauth/config/{{auth24p_oc}}/status?flow_id={{auth24_flow}}", { asserts: [A.status(404)] }),
  deleteClient("AUTH-24", "auth24-h"), deleteClient("AUTH-24", "auth24-c"),
  restoreConfig("AUTH-24"),
]);

const AUTH25 = folder("AUTH-25 Reregister through DCR", `Setup: ${ALLOWLIST_TEXT}; fleet server auth25 as an OAuth protected resource with dynamic registration; an OAuth client on it, needs_session_stickiness true, connected as id1; then reregistered.`, [
  ...allowlistOn("AUTH-25"),
  ...asServer("AUTH-25", "auth25"),
  ...connectOAuth("AUTH-25", oauthBody("auth25-c", "auth25", "auth25", { needs_session_stickiness: true }), "auth25", "id1", "the sticky OAuth client"),
  mockGet("AUTH-25", "auth25", "setup: record auth25's registrations", [A.custom("one registered client", ["pm.expect(pm.response.json().oauth.clients.length, 'body: ' + pm.response.text()).to.equal(1);"])], ["pm.collectionVariables.set('auth25_reg', pm.response.json().oauth.counters.register);", "pm.collectionVariables.set('auth25_dcr1', pm.response.json().oauth.clients[0].client_id);"]),
  request("AUTH-25", "reregister", "POST", "/api/mcp/client/auth25-c/reregister", { asserts: [A.status(200), A.custom("previous_client_id is the first registration and registered_client_id is new", ["var b = pm.response.json();", "pm.expect(b.previous_client_id, 'body: ' + pm.response.text()).to.equal(pm.collectionVariables.get('auth25_dcr1'));", "pm.expect(b.registered_client_id).to.be.a('string').and.not.empty;", "pm.expect(b.registered_client_id).to.not.equal(b.previous_client_id);"])],
    extraTests: ["pm.collectionVariables.set('auth25_au', pm.response.json().authorize_url);"] }),
  mockGet("AUTH-25", "auth25", "one more registration", [A.custom("register +1", ["pm.expect(pm.response.json().oauth.counters.register, 'body: ' + pm.response.text()).to.equal(Number(pm.collectionVariables.get('auth25_reg')) + 1);"])]),
  getClient("AUTH-25", "auth25-c", "the client list shows needs_reauth because the credential was rotated", [clientState("needs_reauth"), A.custom("last_failure.message is \"credential was rotated and must be reauthorized\"", ["pm.expect((pm.response.json().clients[0].last_failure || {}).message, 'body: ' + pm.response.text()).to.equal('credential was rotated and must be reauthorized');"])]),
  execTool("AUTH-25", "execute", "auth25-echo_tool", [A.status(400), A.msgContains("needs re-authorization")]),
  consentAs("AUTH-25", "consent as id1 on the new registration", "auth25", "id1"),
  followCallback("AUTH-25", "follow the callback", "auth25", callbackSuccess()),
  completeOAuth("AUTH-25", "complete", "auth25", [A.status(200)]),
  getClient("AUTH-25", "auth25-c", "the client list shows healthy", [clientState("healthy")]),
  deleteClient("AUTH-25", "auth25-c"),
  restoreConfig("AUTH-25"),
]);

const ADM21 = folder("ADM-21 OAuth on a loopback authorization server follows the private allowlist", "Setup: dashboard auth on, admin credentials; fleet server adm21 as an OAuth protected resource on 127.0.0.1 with dynamic registration. Creates use oauth_config {}: with no mcp_oauth_private_allowlist, with it set to [\"127.0.0.1\"], and with /api/mcp/client whitelisted and no credential. With no allowlist the create returns 500 where 400 is intended, a known issue, so that status check is skipped.", [
  readConfig("ADM-21"),
  putConfig("ADM-21", "setup: no mcp_oauth_private_allowlist", ["cc.mcp_oauth_private_allowlist = [];"], [A.status(200)]),
  ...asServer("ADM-21", "adm21"),
  request("ADM-21", "create with no allowlist", "POST", "/api/mcp/client", { body: oauthBody("adm21-a", "adm21a", "adm21"), asserts: [A.skip(A.status(400)), A.msgContains("blocked connection to non-public address")] }),
  putConfig("ADM-21", "setup: mcp_oauth_private_allowlist [\"127.0.0.1\"]", ["cc.mcp_oauth_private_allowlist = ['127.0.0.1'];"], [A.status(200)]),
  createOAuth("ADM-21", "create with the allowlist set", oauthBody("adm21-b", "adm21b", "adm21"), "adm21b", [statusIs("pending_oauth")]),
  putConfig("ADM-21", "setup: whitelist /api/mcp/client", ["cc.mcp_oauth_private_allowlist = ['127.0.0.1'];", "cc.whitelisted_routes = ['/api/mcp/client'];"], [A.status(200)]),
  request("ADM-21", "create with no credential on the whitelisted route", "POST", "/api/mcp/client", { noAuth: true, noCookies: true, body: oauthBody("adm21-c", "adm21c", "adm21"), asserts: [A.status(403)] }),
  restoreConfig("ADM-21"),
]);

const OAUTH_SERVER_TEXT = `${ALLOWLIST_TEXT}; a fleet server as an OAuth protected resource with dynamic registration`;
const ADM23 = folder("ADM-23 Shared OAuth create returns a pending flow", `Setup: dashboard auth on, admin credentials; ${OAUTH_SERVER_TEXT} (adm23); an OAuth client created on it with oauth_config {}.`, [
  ...allowlistOn("ADM-23"),
  ...asServer("ADM-23", "adm23"),
  createOAuth("ADM-23", "create the OAuth client", oauthBody("adm23-c", "adm23", "adm23"), "adm23", [statusIs("pending_oauth"),
    A.custom("authorize_url has code_challenge_method S256, a state and a redirect_uri ending /api/oauth/callback", [QS_FN, "var q = qs(pm.response.json().authorize_url);", "pm.expect(q.code_challenge_method).to.equal('S256');", "pm.expect(q.state).to.be.a('string').and.not.empty;", "pm.expect(/\\/api\\/oauth\\/callback$/.test(q.redirect_uri || ''), 'redirect_uri ' + q.redirect_uri).to.equal(true);"]),
    A.custom("complete_url is /api/mcp/client/{oauth_config_id}/complete-oauth", ["var b = pm.response.json();", "pm.expect(b.complete_url).to.equal('/api/mcp/client/' + b.oauth_config_id + '/complete-oauth');"]),
    A.custom("3 next_steps", ["pm.expect(pm.response.json().next_steps, 'body: ' + pm.response.text()).to.have.lengthOf(3);"]),
  ]),
  notListed("ADM-23", "adm23-c"),
  oauthStatus("ADM-23", "status is pending", "adm23", "", [statusIs("pending")]),
  restoreConfig("ADM-23"),
]);

const ADM24 = folder("ADM-24 complete-oauth too early or unknown", `Setup: dashboard auth on, admin credentials; ${OAUTH_SERVER_TEXT} (adm24); an OAuth client created on it and left pending; a random oauth_config_id.`, [
  ...allowlistOn("ADM-24"),
  ...asServer("ADM-24", "adm24"),
  createOAuth("ADM-24", "setup: create the OAuth client", oauthBody("adm24-c", "adm24", "adm24"), "adm24"),
  completeOAuth("ADM-24", "complete while pending", "adm24", [A.status(400), A.msgEquals("OAuth not authorized yet. Current status: pending")]),
  request("ADM-24", "complete a random id", "POST", "/api/mcp/client/7f3e2a10-0000-4000-8000-000000000024/complete-oauth", { asserts: [A.status(404), A.msgEquals("OAuth config not found")] }),
  restoreConfig("ADM-24"),
]);

const ADM25 = folder("ADM-25 Callback plus complete activates a shared OAuth client", `Setup: dashboard auth on, admin credentials; ${OAUTH_SERVER_TEXT} (adm25); the ADM-23 create, then its authorize_url followed (consent).`, [
  ...allowlistOn("ADM-25"),
  ...asServer("ADM-25", "adm25"),
  createOAuth("ADM-25", "setup: create the OAuth client", oauthBody("adm25-c", "adm25", "adm25"), "adm25"),
  consentAs("ADM-25", "follow authorize_url", "adm25", null),
  followCallback("ADM-25", "the callback", "adm25", callbackSuccess()),
  oauthStatus("ADM-25", "status is authorized with a token_id", "adm25", "", [statusIs("authorized"), A.custom("token_id is set", ["pm.expect(pm.response.json().token_id, 'body: ' + pm.response.text()).to.be.a('string').and.not.empty;"])]),
  completeOAuth("ADM-25", "complete", "adm25", [A.status(200), A.jsonMessage("MCP client connected successfully with OAuth")]),
  getClient("ADM-25", "adm25-c", "GET shows healthy with an active OAuth credential that has a refresh token", [clientState("healthy"), A.custom("credential kind \"oauth\", status \"active\", has_refresh_token true", [
    "var c = pm.response.json().clients[0].credential || {};",
    "pm.expect(c.kind, 'body: ' + pm.response.text()).to.equal('oauth');", "pm.expect(c.status).to.equal('active');", "pm.expect(c.has_refresh_token).to.equal(true);",
  ])]),
  deleteClient("ADM-25", "adm25-c"),
  restoreConfig("ADM-25"),
]);

const ADM29 = folder("ADM-29 OAuth name conflict appears at completion", `Setup: dashboard auth on, admin credentials; a client named taken_name (HTTP, auth none, on fleet server alpha); ${OAUTH_SERVER_TEXT} (adm29); an OAuth client named taken_name created on it, then consent and callback.`, [
  ...allowlistOn("ADM-29"),
  ...freshServer("ADM-29", "alpha", ["echo_tool", "add_tool"]),
  createClient("ADM-29", httpClient("adm29-t", "taken_name", "alpha"), "setup: create the client taken_name"),
  ...asServer("ADM-29", "adm29"),
  createOAuth("ADM-29", "create the OAuth client named taken_name", oauthBody("adm29-o", "taken_name", "adm29"), "adm29", [statusIs("pending_oauth")]),
  consentAs("ADM-29", "consent", "adm29", null),
  followCallback("ADM-29", "the callback", "adm29", callbackSuccess()),
  completeOAuth("ADM-29", "complete", "adm29", [A.status(409), A.msgEquals("An MCP endpoint with this slug already exists")]),
  request("ADM-29", "there is one taken_name", "GET", "/api/mcp/clients?search=taken_name", { asserts: [A.status(200), A.custom("exactly one client is named taken_name", ["pm.expect(pm.response.json().clients.filter(function (c) { return c.config.name === 'taken_name'; }).length, 'body: ' + pm.response.text()).to.equal(1);"])] }),
  deleteClient("ADM-29", "adm29-t"),
  restoreConfig("ADM-29"),
]);

// connectedO is the shared OAuth client the ADM tests call O: created per call on its own fleet server, connected.
const connectedO = (id, p) => [...asServer(id, p), ...connectOAuth(id, oauthBody(`${p}-o`, p, p), p, null, "the shared OAuth client")];

const ADM31 = folder("ADM-31 Shared reauthorize is flow-scoped", `Setup: dashboard auth on, admin credentials; ${OAUTH_SERVER_TEXT} (adm31); the ADM-25 client on it, connected; then reauthorized.`, [
  ...allowlistOn("ADM-31"),
  ...connectedO("ADM-31", "adm31"),
  oauthStatus("ADM-31", "setup: record the token_id", "adm31", "", [A.custom("token_id is set", ["pm.expect(pm.response.json().token_id, 'body: ' + pm.response.text()).to.be.a('string').and.not.empty;"])], ["pm.collectionVariables.set('adm31_tok', pm.response.json().token_id);"]),
  request("ADM-31", "reauthorize", "POST", "/api/mcp/client/adm31-o/reauthorize", { asserts: [A.status(200), A.custom("flow_id is set and status_url ends ?flow_id=<flow_id>", [
    "var b = pm.response.json();", "pm.expect(b.flow_id, 'body: ' + pm.response.text()).to.be.a('string').and.not.empty;",
    "var tail = '?flow_id=' + b.flow_id;", "pm.expect(b.status_url.slice(-tail.length)).to.equal(tail);",
  ])], extraTests: ["pm.collectionVariables.set('adm31_au', pm.response.json().authorize_url);", "pm.collectionVariables.set('adm31_flow', pm.response.json().flow_id);"] }),
  oauthStatus("ADM-31", "the flow's status is pending", "adm31", "?flow_id={{adm31_flow}}", [A.custom("flow_status is \"pending\"", ["pm.expect(pm.response.json().flow_status, 'body: ' + pm.response.text()).to.equal('pending');"])]),
  oauthStatus("ADM-31", "the bare status stays authorized", "adm31", "", [statusIs("authorized")]),
  completeOAuth("ADM-31", "early complete", "adm31", [A.status(409), A.msgContains("Authorization has not completed yet")]),
  consentAs("ADM-31", "consent", "adm31", null),
  followCallback("ADM-31", "the callback", "adm31", callbackSuccess()),
  completeOAuth("ADM-31", "complete", "adm31", [A.status(200), A.custom("message contains \"re-authorized\"", ["pm.expect(pm.response.json().message, 'body: ' + pm.response.text()).to.include('re-authorized');"])]),
  oauthStatus("ADM-31", "a new token_id", "adm31", "", [A.custom("token_id is set", ["pm.expect(pm.response.json().token_id, 'body: ' + pm.response.text()).to.be.a('string').and.not.empty;"]), A.custom("token_id differs from the recorded one", ["pm.expect(pm.response.json().token_id, 'body: ' + pm.response.text()).to.not.equal(pm.collectionVariables.get('adm31_tok'));"])]),
  deleteClient("ADM-31", "adm31-o"),
  restoreConfig("ADM-31"),
]);

const DENIED_CB = "/workspace/mcp-registry/oauth-callback?status=failed&error=Authentication+was+denied+or+failed.+Please+try+again.";
const ADM32 = folder("ADM-32 Upstream denial during reauthorize", `Setup: dashboard auth on, admin credentials; ${OAUTH_SERVER_TEXT} (adm32); the ADM-31 client on it, reauthorized and pending; then the authorization server denies.`, [
  ...allowlistOn("ADM-32"),
  ...connectedO("ADM-32", "adm32"),
  request("ADM-32", "setup: reauthorize", "POST", "/api/mcp/client/adm32-o/reauthorize", { asserts: [A.status(200)], extraTests: ["pm.collectionVariables.set('adm32_au', pm.response.json().authorize_url);", "pm.collectionVariables.set('adm32_flow', pm.response.json().flow_id);"] }),
  mockAuth("ADM-32", "adm32", "setup: adm32's authorization server denies", { oauth: { deny: true } }),
  consentAs("ADM-32", "the authorization server redirects with error=access_denied", "adm32", null, [A.status(302), A.custom("Location carries error=access_denied", ["pm.expect(pm.response.headers.get('Location'), 'Location').to.include('error=access_denied');"])]),
  followCallback("ADM-32", "callback with error=access_denied", "adm32", [A.status(302), locationEnds(DENIED_CB)]),
  oauthStatus("ADM-32", "status and flow_status are failed", "adm32", "?flow_id={{adm32_flow}}", [statusIs("failed"), A.custom("flow_status is \"failed\"", ["pm.expect(pm.response.json().flow_status, 'body: ' + pm.response.text()).to.equal('failed');"])]),
  deleteClient("ADM-32", "adm32-o"),
  restoreConfig("ADM-32"),
]);

const ADM33 = folder("ADM-33 reregister rotates a DCR client", `Setup: dashboard auth on, admin credentials; ${OAUTH_SERVER_TEXT} (adm33); the ADM-25 client on it, connected; then reregistered. Last, fleet server adm33b whose authorization server has no registration endpoint and knows client C1 (secret S1), with an OAuth client on it using C1, connected.`, [
  ...allowlistOn("ADM-33"),
  ...connectedO("ADM-33", "adm33"),
  request("ADM-33", "reregister", "POST", "/api/mcp/client/adm33-o/reregister", { asserts: [A.status(200), A.custom("registered_client_id differs from previous_client_id", ["var b = pm.response.json();", "pm.expect(b.registered_client_id, 'body: ' + pm.response.text()).to.be.a('string').and.not.empty;", "pm.expect(b.previous_client_id).to.be.a('string').and.not.empty;", "pm.expect(b.registered_client_id).to.not.equal(b.previous_client_id);"])],
    extraTests: ["pm.collectionVariables.set('adm33_au', pm.response.json().authorize_url);"] }),
  getClient("ADM-33", "adm33-o", "credential and state are needs_reauth", [clientState("needs_reauth"), credStatus("needs_reauth")]),
  consentAs("ADM-33", "consent on the new registration", "adm33", null),
  followCallback("ADM-33", "the callback", "adm33", callbackSuccess()),
  completeOAuth("ADM-33", "complete", "adm33", [A.status(200)]),
  getClient("ADM-33", "adm33-o", "credential active, state healthy", [clientState("healthy"), credStatus("active")]),
  ...asServer("ADM-33", "adm33b", { registration: false, clients: [{ client_id: "C1", client_secret: "S1" }] }),
  ...connectOAuth("ADM-33", oauthBody("adm33-p", "adm33p", "adm33b", { oauth_config: { client_id: "C1", client_secret: "S1", authorize_url: `${CHAOS}/s/adm33b/as/authorize`, token_url: `${CHAOS}/s/adm33b/as/token` } }), "adm33p", null, "the pre-registered client"),
  request("ADM-33", "reregister without a registration endpoint", "POST", "/api/mcp/client/adm33-p/reregister", { asserts: [A.status(400), A.msgContains("has no registration endpoint")] }),
  deleteClient("ADM-33", "adm33-o"), deleteClient("ADM-33", "adm33-p"),
  restoreConfig("ADM-33"),
]);

const ADM38 = folder("ADM-38 Rotation only on a real change", `Setup: dashboard auth on, admin credentials; ${OAUTH_SERVER_TEXT} (adm38); a shared OAuth client on it, per call, connected. PUT sends back GET's masked OAuth fields, then the same with client_id "new-id".`, [
  ...allowlistOn("ADM-38"),
  ...connectedO("ADM-38", "adm38"),
  getClient("ADM-38", "adm38-o", "setup: read the masked OAuth fields", [], { extraTests: [
    "var c = pm.response.json().clients[0].config;",
    "pm.collectionVariables.set('adm38_oauth', JSON.stringify({ client_id: c.oauth_client_id, client_secret: c.oauth_client_secret, authorize_url: c.oauth_authorize_url, token_url: c.oauth_token_url, registration_url: c.oauth_registration_url, resource: c.oauth_resource }));",
  ] }),
  request("ADM-38", "PUT the masked OAuth fields back", "PUT", "/api/mcp/client/adm38-o", { raw: "{{adm38_body}}", pre: ["pm.variables.set('adm38_body', JSON.stringify({ oauth_config: JSON.parse(pm.collectionVariables.get('adm38_oauth')) }));"], asserts: [A.status(200)] }),
  getClient("ADM-38", "adm38-o", "the credential stays active", [credStatus("active")]),
  request("ADM-38", "PUT them with client_id \"new-id\"", "PUT", "/api/mcp/client/adm38-o", { raw: "{{adm38_body}}", pre: ["var o = JSON.parse(pm.collectionVariables.get('adm38_oauth')); o.client_id = 'new-id';", "pm.variables.set('adm38_body', JSON.stringify({ oauth_config: o }));"], asserts: [A.status(200)] }),
  getClient("ADM-38", "adm38-o", "credential and state are needs_reauth", [credStatus("needs_reauth"), clientState("needs_reauth")]),
  deleteClient("ADM-38", "adm38-o"),
  restoreConfig("ADM-38"),
]);

const ADM39 = folder("ADM-39 OAuth config status endpoint", `Setup: dashboard auth on, admin credentials; ${OAUTH_SERVER_TEXT} (adm39); a shared OAuth client on it, connected and authorized; a second shared OAuth client on fleet server adm39b, connected and then reauthorized, so its config has a pending flow.`, [
  ...allowlistOn("ADM-39"),
  ...connectedO("ADM-39", "adm39"),
  ...connectedO("ADM-39", "adm39b"),
  request("ADM-39", "setup: reauthorize the second client", "POST", "/api/mcp/client/adm39b-o/reauthorize", { asserts: [A.status(200)], extraTests: ["pm.collectionVariables.set('adm39b_flow', pm.response.json().flow_id);"] }),
  request("ADM-39", "status of an unknown id", "GET", "/api/oauth/config/7f3e2a10-0000-4000-8000-000000000039/status", { asserts: [A.status(404), A.msgEquals("OAuth config not found")] }),
  request("ADM-39", "the first client's status with the second config's flow_id", "GET", "/api/oauth/config/{{adm39_oc}}/status?flow_id={{adm39b_flow}}", { asserts: [A.status(404), A.msgEquals("OAuth flow not found for this OAuth config")] }),
  oauthStatus("ADM-39", "the first client's status with an unknown flow_id", "adm39", "?flow_id=adm39-no-such-flow", [statusIs("authorized"), A.custom("flow_id is echoed", ["pm.expect(pm.response.json().flow_id, 'body: ' + pm.response.text()).to.equal('adm39-no-such-flow');"])]),
  deleteClient("ADM-39", "adm39-o"), deleteClient("ADM-39", "adm39b-o"),
  restoreConfig("ADM-39"),
]);

const ADM40 = folder("ADM-40 Deleting an OAuth config revokes the shared token", `Setup: dashboard auth on, admin credentials; ${OAUTH_SERVER_TEXT} (adm40); a shared OAuth client on it, connected; and another OAuth client created on it and left pending, as in ADM-23.`, [
  ...allowlistOn("ADM-40"),
  ...connectedO("ADM-40", "adm40"),
  createOAuth("ADM-40", "setup: create the pending OAuth client", oauthBody("adm40-p", "adm40p", "adm40"), "adm40p"),
  oauthStatus("ADM-40", "control: the connected client's status has a token_id", "adm40", "", [A.custom("token_id is set", ["pm.expect(pm.response.json().token_id, 'body: ' + pm.response.text()).to.be.a('string').and.not.empty;"])]),
  getClient("ADM-40", "adm40-o", "control: the connected client's credential is active", [credStatus("active")]),
  request("ADM-40", "delete the connected client's OAuth config", "DELETE", "/api/oauth/config/{{adm40_oc}}", { asserts: [A.status(200), A.jsonMessage("OAuth token revoked successfully")] }),
  oauthStatus("ADM-40", "the connected client's status has no token_id", "adm40", "", [A.custom("there is no token_id", ["pm.expect(pm.response.json(), 'body: ' + pm.response.text()).to.not.have.property('token_id');"])]),
  getClient("ADM-40", "adm40-o", "GET the connected client: no credential", [A.custom("there is no credential", ["pm.expect(pm.response.json().clients[0].credential == null, 'body: ' + pm.response.text()).to.equal(true);"])]),
  request("ADM-40", "delete the pending OAuth config", "DELETE", "/api/oauth/config/{{adm40p_oc}}", { asserts: [A.status(500), A.msgContains("no token linked to oauth config")] }),
  deleteClient("ADM-40", "adm40-o"),
  restoreConfig("ADM-40"),
]);

// ================================================================ per-user upstream OAuth against the mock's authorization server

// Every test here sets mcp_oauth_private_allowlist ["127.0.0.1"] and temp-token auth as it states, and restores the
// config at the end. Each test has its own fleet server. A per_user_oauth client is verified at create time by an
// admin consent (login_hint admin); callers then consent as alice or bob through the per-user flow Bifrost opens.
const PU_TEXT = (tempTokens) => `${ALLOWLIST_TEXT}, mcp_enable_temp_token_auth ${tempTokens}`;
const puConfig = (id, tempTokens) => [readConfig(id), putConfig(id, `setup: ${PU_TEXT(tempTokens)}`, ["cc.mcp_oauth_private_allowlist = ['127.0.0.1'];", `cc.mcp_enable_temp_token_auth = ${tempTokens};`], [A.status(200)])];
const PU_SERVER_TEXT = (p, cfgText = "") => `a fleet server ${p} (echo_tool, add_tool) as an OAuth protected resource with dynamic registration${cfgText}; a per_user_oauth client ${p} on it with tools_to_execute ["*"]`;
const puBody = (p) => oauthBody(`${p}-u`, p, p, { auth_type: "per_user_oauth" });
// puConnect creates a per_user_oauth client (with over merged into its config) and verifies it by consenting as admin.
const puConnect = (id, p, cfg = {}, over = {}) => [
  ...asServer(id, p, cfg),
  createOAuth(id, "setup: create the per-user OAuth client", { ...puBody(p), ...over }, p),
  consentAs(id, "setup: consent for the per-user OAuth client as admin", p, "admin"),
  followCallback(id, "setup: follow the callback for the per-user OAuth client", p, callbackSuccess()),
  completeOAuth(id, "setup: complete the per-user OAuth client", p, [A.status(200)]),
];
const PU_FLOWS = "/api/oauth/per-user/flows/";
const PU_SUCCESS = "/workspace/mcp-sessions/auth-success";
const flowDetail = (id, title, v, asserts, opts = {}) => request(id, title, "GET", `${PU_FLOWS}{{${v}}}`, { ...opts, asserts });
// flowStart keeps the authorize_url it returns in <v>_au, which consentAs follows.
const flowStart = (id, title, v, asserts = [A.status(200)], opts = {}) => request(id, title, "GET", `${PU_FLOWS}{{${v}}}/start`, {
  ...opts, asserts, extraTests: [`pm.collectionVariables.set('${v}_au', pm.response.json().authorize_url || '');`],
});
// puFinish takes flow v through start, consent as identity and the callback, which lands on the public success page.
const puFinish = (id, v, identity, label) => [
  flowStart(id, `setup: start ${label}`, v),
  consentAs(id, `setup: consent for ${label} as ${identity}`, v, identity),
  followCallback(id, `setup: follow the callback for ${label}`, v, [A.status(302), locationEnds(PU_SUCCESS)]),
];
// TT sends only flow v's temp token, kept in <v>_tt by authFlow.
const TT = (v) => ({ noAuth: true, noCookies: true, headers: [{ key: "X-Bifrost-Temp-Token", value: `{{${v}_tt}}` }] });
// keepFlow keeps the flow id of a 400's mcp_auth_required in v; with otherThan it also checks the flow is a new one.
const keepFlow = (v, otherThan) => A.custom(`mcp_auth_required kind "oauth" names a flow${otherThan ? ", a new one" : ""}`, [
  "var r = (pm.response.json().extra_fields || {}).mcp_auth_required;",
  "pm.expect(r && r.kind, 'body: ' + pm.response.text()).to.equal('oauth');",
  "pm.expect(r.session_id, 'body: ' + pm.response.text()).to.be.a('string').and.not.empty;",
  ...(otherThan ? [`pm.expect(r.session_id).to.not.equal(pm.collectionVariables.get(${lit(otherThan)}));`] : []),
  `pm.collectionVariables.set(${lit(v)}, r.session_id);`,
]);
// authFlow checks a 400's mcp_auth_required in full: kind "oauth", authorize_url ending /workspace/mcp-sessions/auth?flow=<flow id>
// and session_id equal to that flow id. Without temp tokens: no #t= and the exact message. With them: a #t= token, kept in <saveAs>_tt, and
// the required-fragment notice. saveAs keeps F; sameAs checks F against a kept one.
const authFlow = (name, { saveAs, sameAs, tempToken = false }) => A.custom(`mcp_auth_required kind "oauth", authorize_url .../workspace/mcp-sessions/auth?flow=<flow id>${tempToken ? "#t=<token>" : " with no #t="}, session_id is the flow id${sameAs ? ", the same flow id" : ""}, ${tempToken ? "message has \"IMPORTANT: this link includes a required fragment\"" : `message "Authentication required for ${name}. Visit <url> to connect your account."`}`, [
  "var r = (pm.response.json().extra_fields || {}).mcp_auth_required;",
  "pm.expect(r, 'body: ' + pm.response.text()).to.be.an('object');",
  "pm.expect(r.kind).to.equal('oauth');",
  "var m = /\\/workspace\\/mcp-sessions\\/auth\\?flow=([^&#]+)(#t=(.*))?$/.exec(r.authorize_url);",
  "pm.expect(m, 'authorize_url: ' + r.authorize_url).to.not.equal(null);",
  "pm.expect(r.session_id).to.equal(m[1]);",
  ...(tempToken ? [
    "pm.expect(m[3], 'authorize_url: ' + r.authorize_url).to.be.a('string').and.not.empty;",
    "pm.expect(r.message).to.include('IMPORTANT: this link includes a required fragment');",
    ...(saveAs ? [`pm.collectionVariables.set(${lit(saveAs + "_tt")}, m[3]);`] : []),
  ] : [
    "pm.expect(r.authorize_url, 'authorize_url').to.not.include('#t=');",
    `pm.expect(r.message).to.equal(${lit(`Authentication required for ${name}. Visit `)} + r.authorize_url + ' to connect your account.');`,
  ]),
  ...(saveAs ? [`pm.collectionVariables.set(${lit(saveAs)}, m[1]);`] : []),
  ...(sameAs ? [`pm.expect(m[1]).to.equal(pm.collectionVariables.get(${lit(sameAs)}));`] : []),
]);
// startUrl checks a flow start's authorize_url: server p's authorize endpoint with a client_id, S256, a state and
// Bifrost's callback as redirect_uri.
const startUrl = (p) => A.custom(`authorize_url is ${p}'s authorize endpoint with client_id, S256, a state and the callback redirect_uri`, [QS_FN,
  "var u = pm.response.json().authorize_url;",
  `pm.expect(u, 'body: ' + pm.response.text()).to.include(${lit(`/s/${p}/as/authorize?`)});`,
  "var q = qs(u);",
  "pm.expect(q.client_id, 'query').to.be.a('string').and.not.empty;",
  "pm.expect(q.code_challenge_method, 'query').to.equal('S256');",
  "pm.expect(q.code_challenge, 'query').to.be.a('string').and.not.empty;",
  "pm.expect(q.state, 'query').to.be.a('string').and.not.empty;",
  "pm.expect(/\\/api\\/oauth\\/callback$/.test(q.redirect_uri || ''), 'redirect_uri ' + q.redirect_uri).to.equal(true);",
]);
// The sessions list is read whole (limit 500) and filtered here by client and an ES5 predicate on the entry s.
const SESSIONS = "/api/mcp/sessions?limit=500";
const sessionRows = (clientId, pred) => `var found = (pm.response.json().sessions || []).filter(function (s) { return s.mcp_client && s.mcp_client.client_id === ${lit(clientId)} && (${pred}); });`;
const saveSession = (id, title, clientId, pred, v) => request(id, title, "GET", SESSIONS, {
  asserts: [A.status(200), A.custom("exactly one matching entry", [sessionRows(clientId, pred), "pm.expect(found.length, 'body: ' + pm.response.text()).to.equal(1);"])],
  extraTests: [sessionRows(clientId, pred), `pm.collectionVariables.set(${lit(v)}, found.length ? found[0].id : '');`],
});
const sessionsHas = (id, title, clientId, pred, what) => request(id, title, "GET", SESSIONS, {
  asserts: [A.status(200), A.custom(what, [sessionRows(clientId, pred), "pm.expect(found.length, 'body: ' + pm.response.text()).to.equal(1);"])],
});
const vkPred = (v) => `s.kind === 'token' && s.virtual_key && s.virtual_key.id === pm.collectionVariables.get(${lit(v + "_id")})`;
const sessionPred = (sid) => `s.kind === 'token' && s.session_id === ${lit(sid)}`;
// puVK creates a virtual key granted every tool of client p, keeping its id and value in v_id and v.
const puVK = (id, label, p, v) => vkCreate(id, `setup: create the ${label} VK, granted every tool of ${p}`, { name: `${id} ${label} key`, mcp_configs: [{ mcp_client_name: p, tools_to_execute: ["*"] }] }, v);
const vkExec = (v) => ({ headers: [{ key: "x-bf-vk", value: `{{${v}}}` }] });
const execAfterS = (id, title, tool, session, ms, asserts) => request(id, title, "POST", "/v1/mcp/tool/execute", {
  noAuth: true, noCookies: true, asserts, pre: [`setTimeout(function () {}, ${ms});`],
  body: { id: "c1", type: "function", function: { name: tool, arguments: lit({ text: "hi" }) } },
  headers: [{ key: "x-bf-mcp-session-id", value: session }],
});
const credKind = (v) => A.custom(`credential kind is ${lit(v)}`, [`pm.expect((pm.response.json().clients[0].credential || {}).kind, 'body: ' + pm.response.text()).to.equal(${lit(v)});`]);
const methodIs = (m, n) => A.custom(`${m} count is ${n}`, [`pm.expect((pm.response.json().counters.methods || {})[${lit(m)}] || 0, 'body: ' + pm.response.text()).to.equal(${n});`]);
const adminLogin = (id) => request(id, "setup: log in as the admin", "POST", "/api/session/login", {
  noAuth: true, noCookies: true, raw: "{{login_body}}",
  pre: ["var C = require('crypto-js'); var pair = C.enc.Base64.parse(pm.variables.get('admin_auth_header').replace(/^Basic /, '')).toString(C.enc.Utf8); var i = pair.indexOf(':');", "pm.variables.set('login_body', JSON.stringify({ username: pair.slice(0, i), password: pair.slice(i + 1) }));"],
  asserts: [A.status(200)],
  extraTests: ["var m = /(?:^|[;,\\s])token=([^;]+)/.exec(pm.response.headers.get('Set-Cookie') || '');", "pm.collectionVariables.set('admin_cookie', m ? m[1] : '');"],
});
const adminLogout = (id) => request(id, "teardown: log the admin session out", "POST", "/api/session/logout", { noAuth: true, noCookies: true, headers: [{ key: "Authorization", value: "Bearer {{admin_cookie}}" }], asserts: [A.status(200)] });
// A per_user_headers client on fleet server srv, which accepts only X-Api-Key key-a.
const PH_TEXT = (srv) => `a fleet server ${srv} (echo_tool, add_tool) that accepts only X-Api-Key key-a; a per_user_headers client ${srv} on it with per_user_header_keys ["x-api-key"], sample key-a, tools_to_execute ["*"]`;
const phClient = (id, srv) => [
  ...freshServer(id, srv, ["echo_tool", "add_tool"]),
  mockAuth(id, srv, `setup: ${srv} accepts only X-Api-Key key-a`, { identity_header: "X-Api-Key", require_headers: { "X-Api-Key": "key-a" } }),
  createClient(id, { client_id: `${srv}-c`, name: srv, connection_type: "http", connection_string: `${CHAOS}/s/${srv}/mcp`, auth_type: "per_user_headers", per_user_header_keys: ["x-api-key"], user_headers: { "x-api-key": "key-a" }, tools_to_execute: ["*"] }, "setup: create the per_user_headers client"),
];
// phCredential opens a header flow for session sid, submits key-a and keeps the credential id in v.
const phCredential = (id, srv, sid, v) => [
  execTool(id, `setup: execute as ${sid} opens a header flow`, `${srv}-echo_tool`, [A.status(400), headersFlow({ saveAs: `${v}_flow` })], { session: sid }),
  request(id, "setup: submit x-api-key key-a", "PUT", `/api/mcp/per-user-headers/flows/{{${v}_flow}}`, { body: { headers: { "x-api-key": "key-a" } }, asserts: [A.status(200)], extraTests: [`pm.collectionVariables.set(${lit(v)}, pm.response.json().credential_id);`] }),
];
const NO_SESSION = "MCP session not found";
const NO_FLOW = "OAuth flow not found";

const AUTH27 = folder("AUTH-27 per_user_oauth create with admin verification", `Setup: ${PU_TEXT(false)}; ${PU_SERVER_TEXT("auth27")}, created and verified by consenting as admin.`, [
  ...puConfig("AUTH-27", false),
  ...asServer("AUTH-27", "auth27"),
  createOAuth("AUTH-27", "create the per-user OAuth client", puBody("auth27"), "auth27", [statusIs("pending_oauth"), messageStarts("Test OAuth configuration"), A.custom("the message does not say the login \"will not be saved\"", ["pm.expect(pm.response.json().message, 'body: ' + pm.response.text()).to.not.include('will not be saved');"]), A.custom("the message says the login is kept as the admin credential for periodic tool discovery and never used for a user's tool calls", ["var msg = pm.response.json().message;", "pm.expect(msg, 'body: ' + pm.response.text()).to.include(\"kept as the client's admin credential for periodic tool discovery\");", "pm.expect(msg, 'body: ' + pm.response.text()).to.include(\"never used for a user's tool calls\");"])]),
  consentAs("AUTH-27", "consent as admin", "auth27", "admin"),
  followCallback("AUTH-27", "follow the callback", "auth27", callbackSuccess()),
  completeOAuth("AUTH-27", "complete", "auth27", [A.status(200), messageStarts("OAuth configuration verified successfully. 2 tools discovered")]),
  getClient("AUTH-27", "auth27-u", "the credential is oauth and active", [credKind("oauth"), credStatus("active")]),
  completeOAuth("AUTH-27", "complete again", "auth27", [A.status(409)]),
  deleteClient("AUTH-27", "auth27-u"),
  restoreConfig("AUTH-27"),
]);

const AUTH28 = folder("AUTH-28 per_user_oauth verification fails", `Setup: ${PU_TEXT(false)}; ${PU_SERVER_TEXT("auth28")}. After the admin consent and callback, the authorization server expires admin's token, so auth28 rejects it.`, [
  ...puConfig("AUTH-28", false),
  ...asServer("AUTH-28", "auth28"),
  createOAuth("AUTH-28", "setup: create the per-user OAuth client", puBody("auth28"), "auth28"),
  consentAs("AUTH-28", "setup: consent as admin", "auth28", "admin"),
  followCallback("AUTH-28", "setup: follow the callback", "auth28", callbackSuccess()),
  mockPost("AUTH-28", "auth28", "oauth/expire", "setup: expire admin's token", { identity: "admin" }),
  completeOAuth("AUTH-28", "complete", "auth28", [A.status(500), A.msgStartsWith("OAuth configuration test failed:")]),
  notListed("AUTH-28", "auth28-u"),
  restoreConfig("AUTH-28"),
]);

const AUTH29_MSG = "per-user OAuth for auth29 requires an identity: send a Virtual Key (x-bf-vk)";
const AUTH29 = folder("AUTH-29 Per-user client with no caller identity", `Setup: ${PU_TEXT(false)}; ${PU_SERVER_TEXT("auth29")}, verified as admin. The caller sends no VK and no session id.`, [
  ...puConfig("AUTH-29", false),
  ...puConnect("AUTH-29", "auth29"),
  execTool("AUTH-29", "execute with no identity", "auth29-echo_tool", [A.status(400), A.msgStartsWith(AUTH29_MSG)]),
  gwCall("AUTH-29", "/mcp tools/call with no identity", "auth29-echo_tool", [toolResultError(null, AUTH29_MSG)]),
  deleteClient("AUTH-29", "auth29-u"),
  restoreConfig("AUTH-29"),
]);

const AUTH30 = folder("AUTH-30 Identity but no token yet", `Setup: ${PU_TEXT(false)}; ${PU_SERVER_TEXT("auth30")}, verified as admin. The caller sends x-bf-mcp-session-id auth30-sess1 and has no token.`, [
  ...puConfig("AUTH-30", false),
  ...puConnect("AUTH-30", "auth30"),
  execTool("AUTH-30", "first execute call as auth30-sess1", "auth30-echo_tool", [A.status(400), authFlow("auth30", { saveAs: "auth30_f" })], { session: "auth30-sess1" }),
  execTool("AUTH-30", "second execute call as auth30-sess1", "auth30-echo_tool", [A.status(400), authFlow("auth30", { sameAs: "auth30_f" })], { session: "auth30-sess1" }),
  mockGet("AUTH-30", "auth30", "no tools/call reached auth30", [methodIs("tools/call", 0)]),
  deleteClient("AUTH-30", "auth30-u"),
  restoreConfig("AUTH-30"),
]);

const AUTH31 = folder("AUTH-31 Temp-token link", `Setup: ${PU_TEXT(true)}; ${PU_SERVER_TEXT("auth31")}, verified as admin; a VK granted every tool of auth31. One caller sends x-bf-mcp-session-id auth31-sess1, the other the VK; neither has a token.`, [
  ...puConfig("AUTH-31", true),
  ...puConnect("AUTH-31", "auth31"),
  puVK("AUTH-31", "first", "auth31", "auth31_vk1"),
  execTool("AUTH-31", "execute as auth31-sess1", "auth31-echo_tool", [A.status(400), authFlow("auth31", { tempToken: true })], { session: "auth31-sess1" }),
  execTool("AUTH-31", "execute with the VK", "auth31-echo_tool", [A.status(400), authFlow("auth31", { tempToken: true })], vkExec("auth31_vk1")),
  deleteClient("AUTH-31", "auth31-u"),
  vkDelete("AUTH-31", "teardown: delete the VK", "auth31_vk1"),
  restoreConfig("AUTH-31"),
]);

const AUTH32 = folder("AUTH-32 Flow detail and start", `Setup: ${PU_TEXT(false)}; ${PU_SERVER_TEXT("auth32")}, verified as admin; an execute call as auth32-sess1 opens a flow.`, [
  ...puConfig("AUTH-32", false),
  ...puConnect("AUTH-32", "auth32"),
  execTool("AUTH-32", "setup: execute as auth32-sess1 opens a flow", "auth32-echo_tool", [A.status(400), keepFlow("auth32_f")], { session: "auth32-sess1" }),
  flowDetail("AUTH-32", "flow detail", "auth32_f", [A.status(200), A.jsonEquals("flow_mode", "session"), A.jsonEquals("status", "pending"), A.jsonEquals("session_id", "auth32-sess1")]),
  flowStart("AUTH-32", "start", "auth32_f", [A.status(200), startUrl("auth32")]),
  request("AUTH-32", "detail of an unknown flow", "GET", `${PU_FLOWS}7f3e2a10-0000-4000-8000-000000000032`, { asserts: [A.status(404), A.msgEquals(NO_FLOW)] }),
  request("AUTH-32", "start of an unknown flow", "GET", `${PU_FLOWS}7f3e2a10-0000-4000-8000-000000000032/start`, { asserts: [A.status(404), A.msgEquals(NO_FLOW)] }),
  deleteClient("AUTH-32", "auth32-u"),
  restoreConfig("AUTH-32"),
]);

const AUTH33 = folder("AUTH-33 Caller completes per-user OAuth", `Setup: ${PU_TEXT(false)}; ${PU_SERVER_TEXT("auth33")}, verified as admin; an execute call as auth33-sess1 opens a flow; the authorization server approves as alice.`, [
  ...puConfig("AUTH-33", false),
  ...puConnect("AUTH-33", "auth33"),
  execTool("AUTH-33", "setup: execute as auth33-sess1 opens a flow", "auth33-echo_tool", [A.status(400), keepFlow("auth33_f")], { session: "auth33-sess1" }),
  flowStart("AUTH-33", "setup: start the flow", "auth33_f"),
  consentAs("AUTH-33", "setup: consent as alice", "auth33_f", "alice"),
  followCallback("AUTH-33", "the callback", "auth33_f", [A.status(302), locationEnds(PU_SUCCESS)]),
  execTool("AUTH-33", "execute as auth33-sess1", "auth33-echo_tool", [A.status(200)], { session: "auth33-sess1" }),
  mockGet("AUTH-33", "auth33", "the call is counted under alice", [identityCalls("alice", 1)]),
  flowStart("AUTH-33", "start the completed flow again", "auth33_f", [A.status(404)]),
  deleteClient("AUTH-33", "auth33-u"),
  restoreConfig("AUTH-33"),
]);

const AUTH34 = folder("AUTH-34 Two callers stay isolated", `Setup: as AUTH-33 (${PU_TEXT(false)}; ${PU_SERVER_TEXT("auth34")}, verified as admin; auth34-sess1 completes as alice and makes one call). Then auth34-sess2 calls, completes its own flow as bob, and the two sessions call again.`, [
  ...puConfig("AUTH-34", false),
  ...puConnect("AUTH-34", "auth34"),
  execTool("AUTH-34", "setup: execute as auth34-sess1 opens flow A", "auth34-echo_tool", [A.status(400), keepFlow("auth34_fa")], { session: "auth34-sess1" }),
  ...puFinish("AUTH-34", "auth34_fa", "alice", "flow A"),
  execTool("AUTH-34", "setup: execute as auth34-sess1", "auth34-echo_tool", [A.status(200)], { session: "auth34-sess1" }),
  execTool("AUTH-34", "first execute call as auth34-sess2", "auth34-echo_tool", [A.status(400), keepFlow("auth34_fb", "auth34_fa")], { session: "auth34-sess2" }),
  mockGet("AUTH-34", "auth34", "auth34-sess2's call did not reach auth34", [identityCallsAre({ alice: 1 }), A.custom("tools/call reached auth34 once, alice's", ["pm.expect((pm.response.json().counters.methods || {})['tools/call'] || 0, 'body: ' + pm.response.text()).to.equal(1);"])]),
  ...puFinish("AUTH-34", "auth34_fb", "bob", "flow B"),
  execTool("AUTH-34", "execute as auth34-sess1", "auth34-echo_tool", [A.status(200)], { session: "auth34-sess1" }),
  execTool("AUTH-34", "execute as auth34-sess1 again", "auth34-echo_tool", [A.status(200)], { session: "auth34-sess1" }),
  execTool("AUTH-34", "execute as auth34-sess2", "auth34-echo_tool", [A.status(200)], { session: "auth34-sess2" }),
  mockGet("AUTH-34", "auth34", "alice 3, bob 1, admin never counted", [identityCallsAre({ alice: 3, bob: 1 }), noIdentity("admin")]),
  deleteClient("AUTH-34", "auth34-u"),
  restoreConfig("AUTH-34"),
]);

const AUTH35 = folder("AUTH-35 VK-mode isolation and revoke", `Setup: ${PU_TEXT(false)}; ${PU_SERVER_TEXT("auth35")}, verified as admin; two VKs granted every tool of auth35. The first VK completes as alice, the second as bob, each calls once, then the first VK's session is deleted.`, [
  ...puConfig("AUTH-35", false),
  ...puConnect("AUTH-35", "auth35"),
  puVK("AUTH-35", "first", "auth35", "auth35_vk1"),
  puVK("AUTH-35", "second", "auth35", "auth35_vk2"),
  execTool("AUTH-35", "setup: execute with the first VK opens its flow", "auth35-echo_tool", [A.status(400), keepFlow("auth35_f1")], vkExec("auth35_vk1")),
  ...puFinish("AUTH-35", "auth35_f1", "alice", "the first VK's flow"),
  execTool("AUTH-35", "setup: execute with the second VK opens its flow", "auth35-echo_tool", [A.status(400), keepFlow("auth35_f2")], vkExec("auth35_vk2")),
  ...puFinish("AUTH-35", "auth35_f2", "bob", "the second VK's flow"),
  execTool("AUTH-35", "setup: execute with the first VK", "auth35-echo_tool", [A.status(200)], vkExec("auth35_vk1")),
  execTool("AUTH-35", "setup: execute with the second VK", "auth35-echo_tool", [A.status(200)], vkExec("auth35_vk2")),
  saveSession("AUTH-35", "setup: find the first VK's session", "auth35-u", vkPred("auth35_vk1"), "auth35_s1"),
  request("AUTH-35", "delete the first VK's session", "DELETE", "/api/mcp/sessions/{{auth35_s1}}", { asserts: [A.status(204)] }),
  execTool("AUTH-35", "execute with the first VK", "auth35-echo_tool", [A.status(400), keepFlow("auth35_f3")], vkExec("auth35_vk1")),
  execTool("AUTH-35", "execute with the second VK", "auth35-echo_tool", [A.status(200)], vkExec("auth35_vk2")),
  mockGet("AUTH-35", "auth35", "the second VK's call ran under bob and alice is unchanged", [identityCallsAre({ alice: 1, bob: 2 })]),
  deleteClient("AUTH-35", "auth35-u"),
  vkDelete("AUTH-35", "teardown: delete the first VK", "auth35_vk1"),
  vkDelete("AUTH-35", "teardown: delete the second VK", "auth35_vk2"),
  restoreConfig("AUTH-35"),
]);

const AUTH36 = folder("AUTH-36 Per-user lazy refresh", `Setup: ${PU_TEXT(false)}; ${PU_SERVER_TEXT("auth36", " whose authorization server issues 5s access tokens and rotates refresh tokens")}, verified as admin; auth36-sess1 completes as alice. Each execute call waits 6s first, so each finds an expired token.`, [
  ...puConfig("AUTH-36", false),
  ...puConnect("AUTH-36", "auth36", { access_ttl: "5s", rotate_refresh: true }),
  execTool("AUTH-36", "setup: execute as auth36-sess1 opens a flow", "auth36-echo_tool", [A.status(400), keepFlow("auth36_f")], { session: "auth36-sess1" }),
  ...puFinish("AUTH-36", "auth36_f", "alice", "the flow"),
  execAfterS("AUTH-36", "first execute call as auth36-sess1, 6s later", "auth36-echo_tool", "auth36-sess1", 6000, [A.status(200)]),
  execAfterS("AUTH-36", "second execute call as auth36-sess1, 6s after the first", "auth36-echo_tool", "auth36-sess1", 6000, [A.status(200)]),
  mockGet("AUTH-36", "auth36", "two refresh grants for alice, the second presenting the first's new token", [A.custom("2 refresh_token requests for alice, both 200, the second's refresh_token the first's issued one", [
    "var r = pm.response.json().oauth.log.filter(function (e) { return e.grant_type === 'refresh_token' && e.identity === 'alice'; });",
    "pm.expect(r.length, 'refreshes: ' + JSON.stringify(pm.response.json().oauth.log)).to.equal(2);",
    "pm.expect(r[0].status).to.equal(200);", "pm.expect(r[1].status).to.equal(200);",
    "pm.expect(r[1].refresh_token).to.equal(r[0].issued_refresh_token);",
  ]), identityCalls("alice", 2)]),
  deleteClient("AUTH-36", "auth36-u"),
  restoreConfig("AUTH-36"),
]);

const gwFlow = (id, title, tool, vk, v, withToken) => request(id, title, "POST", "/mcp", {
  noAuth: true, raw: JSON.stringify(call(tool, { text: "hi" })), headers: [MCP_ACCEPT, { key: "x-bf-vk", value: `{{${vk}}}` }],
  asserts: [A.status(200), A.custom(`isError true and the text links a flow${withToken ? " with a #t= temp token" : ""}`, [
    "var r = pm.response.json().result;",
    "pm.expect(r && r.isError, 'body: ' + pm.response.text()).to.equal(true);",
    "var m = /\\/workspace\\/mcp-sessions\\/auth\\?flow=([^&#\\s]+)(#t=(\\S+))?/.exec(r.content[0].text);",
    "pm.expect(m, 'text: ' + r.content[0].text).to.not.equal(null);",
    ...(withToken ? ["pm.expect(m[3], 'text: ' + r.content[0].text).to.be.a('string').and.not.empty;"] : []),
  ])],
  extraTests: [
    "var mm = /\\/workspace\\/mcp-sessions\\/auth\\?flow=([^&#\\s]+)(#t=(\\S+))?/.exec(pm.response.json().result.content[0].text) || [];",
    `pm.collectionVariables.set(${lit(v)}, mm[1]);`,
    ...(withToken ? [`pm.collectionVariables.set(${lit(v + "_tt")}, mm[3]);`] : []),
  ],
});
const ADM04 = folder("ADM-04 A temp token reaches only its own flow", `Setup: dashboard auth on; ${PU_TEXT(true)}; ${PU_SERVER_TEXT("adm04")}, verified as admin; two VKs granted every tool of adm04, each opening its own flow with a tools/call on /mcp. The first flow's temp token comes from the #t= fragment of the link in that reply; the requests that use it carry nothing else.`, [
  ...puConfig("ADM-04", true),
  ...puConnect("ADM-04", "adm04"),
  puVK("ADM-04", "first", "adm04", "adm04_vk1"),
  puVK("ADM-04", "second", "adm04", "adm04_vk2"),
  gwFlow("ADM-04", "setup: /mcp tools/call with the first VK opens the first flow and its temp token", "adm04-echo_tool", "adm04_vk1", "adm04_f1", true),
  gwFlow("ADM-04", "setup: /mcp tools/call with the second VK opens the second flow", "adm04-echo_tool", "adm04_vk2", "adm04_f2", false),
  flowDetail("ADM-04", "the first flow's detail with its temp token", "adm04_f1", [A.status(200)], TT("adm04_f1")),
  flowStart("ADM-04", "the first flow's start with its temp token", "adm04_f1", [A.status(200)], TT("adm04_f1")),
  flowDetail("ADM-04", "control: the first flow's temp token still opens its own flow after the start", "adm04_f1", [A.status(200)], TT("adm04_f1")),
  flowDetail("ADM-04", "the second flow's detail with the first flow's temp token", "adm04_f2", [A.status(401), errorCode("temp_token_rejected")], TT("adm04_f1")),
  request("ADM-04", "GET /api/mcp/clients with the first flow's temp token", "GET", "/api/mcp/clients", { ...TT("adm04_f1"), asserts: [A.status(401), errorCode("temp_token_rejected")] }),
  putConfig("ADM-04", `setup: ${PU_TEXT(false)}`, ["cc.mcp_oauth_private_allowlist = ['127.0.0.1'];", "cc.mcp_enable_temp_token_auth = false;"], [A.status(200)]),
  flowDetail("ADM-04", "the first flow's detail with its temp token, temp-token auth off", "adm04_f1", [A.status(401), errorCode("temp_token_auth_disabled")], TT("adm04_f1")),
  deleteClient("ADM-04", "adm04-u"),
  vkDelete("ADM-04", "teardown: delete the first VK", "adm04_vk1"),
  vkDelete("ADM-04", "teardown: delete the second VK", "adm04_vk2"),
  restoreConfig("ADM-04"),
]);

const ADM27 = folder("ADM-27 per_user_oauth create and complete", `Setup: dashboard auth on, admin credentials; ${PU_TEXT(false)}; ${PU_SERVER_TEXT("adm27")}, created and verified by consenting as admin.`, [
  ...puConfig("ADM-27", false),
  ...asServer("ADM-27", "adm27"),
  createOAuth("ADM-27", "create", puBody("adm27"), "adm27", [statusIs("pending_oauth"), messageStarts("Test OAuth configuration")]),
  consentAs("ADM-27", "setup: consent as admin", "adm27", "admin"),
  followCallback("ADM-27", "setup: follow the callback", "adm27", callbackSuccess()),
  completeOAuth("ADM-27", "complete", "adm27", [A.status(200), messageStarts("OAuth configuration verified successfully. 2 tools discovered."), A.jsonEquals("tools_count", 2)]),
  getClient("ADM-27", "adm27-u", "the credential is oauth and active", [credKind("oauth"), credStatus("active")]),
  request("ADM-27", "there is no sessions entry for adm27-u", "GET", SESSIONS, { asserts: [A.status(200), A.custom("no entry for adm27-u", [sessionRows("adm27-u", "true"), "pm.expect(found, 'body: ' + pm.response.text()).to.eql([]);"])] }),
  completeOAuth("ADM-27", "replay complete", "adm27", [A.status(409), A.msgEquals("OAuth flow has already been completed for this MCP client")]),
  deleteClient("ADM-27", "adm27-u"),
  restoreConfig("ADM-27"),
]);

const ADM28 = folder("ADM-28 per_user_oauth verification failure", `Setup: dashboard auth on, admin credentials; as ADM-27 (${PU_TEXT(false)}; ${PU_SERVER_TEXT("adm28")}). After the admin consent and callback, the authorization server expires admin's token, so adm28 rejects it.`, [
  ...puConfig("ADM-28", false),
  ...asServer("ADM-28", "adm28"),
  createOAuth("ADM-28", "setup: create", puBody("adm28"), "adm28"),
  consentAs("ADM-28", "setup: consent as admin", "adm28", "admin"),
  followCallback("ADM-28", "setup: follow the callback", "adm28", callbackSuccess()),
  oauthStatus("ADM-28", "control: after the callback the status has a token_id", "adm28", "", [A.custom("token_id is set", ["pm.expect(pm.response.json().token_id, 'body: ' + pm.response.text()).to.be.a('string').and.not.empty;"])]),
  mockPost("ADM-28", "adm28", "oauth/expire", "setup: expire admin's token", { identity: "admin" }),
  completeOAuth("ADM-28", "complete", "adm28", [A.status(500), A.msgContains("OAuth configuration test failed")]),
  notListed("ADM-28", "adm28-u"),
  oauthStatus("ADM-28", "the status has no token_id", "adm28", "", [A.custom("there is no token_id", ["pm.expect(pm.response.json(), 'body: ' + pm.response.text()).to.not.have.property('token_id');"])]),
  restoreConfig("ADM-28"),
]);

const ADM34 = folder("ADM-34 per_user_oauth reauthorize repairs only the admin credential", `Setup: dashboard auth on, admin credentials; as ADM-27 (${PU_TEXT(false)}; ${PU_SERVER_TEXT("adm34")}, verified as admin); a VK granted every tool of adm34 whose caller completed as alice, so it has a token entry. The client is then reauthorized and verified as admin again.`, [
  ...puConfig("ADM-34", false),
  ...puConnect("ADM-34", "adm34"),
  puVK("ADM-34", "first", "adm34", "adm34_vk1"),
  execTool("ADM-34", "setup: execute with the VK opens its flow", "adm34-echo_tool", [A.status(400), keepFlow("adm34_f")], vkExec("adm34_vk1")),
  ...puFinish("ADM-34", "adm34_f", "alice", "the VK's flow"),
  saveSession("ADM-34", "setup: find the VK's token entry", "adm34-u", vkPred("adm34_vk1"), "adm34_t"),
  request("ADM-34", "reauthorize", "POST", "/api/mcp/client/adm34-u/reauthorize", { asserts: [A.status(200)], extraTests: ["pm.collectionVariables.set('adm34_au', pm.response.json().authorize_url);"] }),
  consentAs("ADM-34", "consent as admin", "adm34", "admin"),
  followCallback("ADM-34", "the callback", "adm34", callbackSuccess()),
  completeOAuth("ADM-34", "complete", "adm34", [A.status(200), A.jsonMessage("Admin discovery credential repaired successfully. 2 tools discovered.")]),
  request("ADM-34", "the VK's token entry keeps its id and is active", "GET", SESSIONS, { asserts: [A.status(200), A.custom("one entry for the VK, the recorded id, status \"active\"", [
    sessionRows("adm34-u", vkPred("adm34_vk1")),
    "pm.expect(found.length, 'body: ' + pm.response.text()).to.equal(1);",
    "pm.expect(found[0].id).to.equal(pm.collectionVariables.get('adm34_t'));",
    "pm.expect(found[0].status).to.equal('active');",
  ])] }),
  deleteClient("ADM-34", "adm34-u"),
  vkDelete("ADM-34", "teardown: delete the VK", "adm34_vk1"),
  restoreConfig("ADM-34"),
]);

const ADM75 = folder("ADM-75 Sessions reauth", `Setup: dashboard auth on, admin credentials; ${PU_TEXT(false)}; ${PH_TEXT("adm75h")}, where adm75-hsess1 submitted key-a, giving a header credential; ${PU_SERVER_TEXT("adm75")}, verified as admin, where adm75-sess1 completed as alice, giving an OAuth token.`, [
  ...puConfig("ADM-75", false),
  ...phClient("ADM-75", "adm75h"),
  ...phCredential("ADM-75", "adm75h", "adm75-hsess1", "adm75_hc"),
  ...puConnect("ADM-75", "adm75"),
  execTool("ADM-75", "setup: execute as adm75-sess1 opens a flow", "adm75-echo_tool", [A.status(400), keepFlow("adm75_f")], { session: "adm75-sess1" }),
  ...puFinish("ADM-75", "adm75_f", "alice", "the flow"),
  saveSession("ADM-75", "setup: find the OAuth token's entry", "adm75-u", sessionPred("adm75-sess1"), "adm75_t"),
  request("ADM-75", "reauth the header credential", "POST", "/api/mcp/sessions/{{adm75_hc}}/reauth", {
    asserts: [A.status(200), A.jsonEquals("kind", "headers"), A.custom("session_id is set", ["pm.expect(pm.response.json().session_id, 'body: ' + pm.response.text()).to.be.a('string').and.not.empty;"]), A.custom("authorize_url contains /workspace/mcp-sessions/auth?flow= and &kind=headers", [
      "var u = pm.response.json().authorize_url;",
      "pm.expect(u, 'body: ' + pm.response.text()).to.include('/workspace/mcp-sessions/auth?flow=');",
      "pm.expect(u).to.include('&kind=headers');",
    ])],
    extraTests: ["pm.collectionVariables.set('adm75_hc_flow', pm.response.json().session_id);"],
  }),
  request("ADM-75", "reauth the header credential again", "POST", "/api/mcp/sessions/{{adm75_hc}}/reauth", { asserts: [A.status(200), A.custom("the same session_id", ["pm.expect(pm.response.json().session_id, 'body: ' + pm.response.text()).to.equal(pm.collectionVariables.get('adm75_hc_flow'));"])] }),
  request("ADM-75", "reauth the OAuth token", "POST", "/api/mcp/sessions/{{adm75_t}}/reauth", {
    asserts: [A.status(200), A.custom("authorize_url contains the session_id", ["var b = pm.response.json();", "pm.expect(b.session_id, 'body: ' + pm.response.text()).to.be.a('string').and.not.empty;", "pm.expect(b.authorize_url).to.include(b.session_id);"])],
    extraTests: ["pm.collectionVariables.set('adm75_t_flow', pm.response.json().session_id);"],
  }),
  request("ADM-75", "reauth an unknown id", "POST", "/api/mcp/sessions/7f3e2a10-0000-4000-8000-000000000075/reauth", { asserts: [A.status(404), A.msgEquals(NO_SESSION)] }),
  request("ADM-75", "reauth the header credential's flow id", "POST", "/api/mcp/sessions/{{adm75_hc_flow}}/reauth", { asserts: [A.status(404), A.msgEquals(NO_SESSION)] }),
  request("ADM-75", "reauth the OAuth token's flow id", "POST", "/api/mcp/sessions/{{adm75_t_flow}}/reauth", { asserts: [A.status(404), A.msgEquals(NO_SESSION)] }),
  deleteClient("ADM-75", "adm75h-c"),
  deleteClient("ADM-75", "adm75-u"),
  restoreConfig("ADM-75"),
]);

const ADM76 = folder("ADM-76 Sessions revoke", `Setup: dashboard auth on, admin credentials; ${PU_TEXT(false)}; ${PH_TEXT("adm76h")}, with a header credential (adm76-hsess1 submitted key-a, then reauth opened a flow for it) and a pending header flow (adm76-hsess2); ${PU_SERVER_TEXT("adm76")}, verified as admin, with an OAuth token (adm76-sess1 completed as alice, then reauth opened a pending flow for it); and a shared OAuth client on fleet server adm76s, connected.`, [
  ...puConfig("ADM-76", false),
  ...phClient("ADM-76", "adm76h"),
  ...phCredential("ADM-76", "adm76h", "adm76-hsess1", "adm76_hc"),
  request("ADM-76", "setup: reauth the header credential, opening its flow", "POST", "/api/mcp/sessions/{{adm76_hc}}/reauth", { asserts: [A.status(200), A.custom("session_id is set", ["pm.expect(pm.response.json().session_id, 'body: ' + pm.response.text()).to.be.a('string').and.not.empty;"])], extraTests: ["pm.collectionVariables.set('adm76_hc_flow', pm.response.json().session_id);"] }),
  execTool("ADM-76", "setup: execute as adm76-hsess2 opens a pending header flow", "adm76h-echo_tool", [A.status(400), headersFlow({ saveAs: "adm76_hf" })], { session: "adm76-hsess2" }),
  ...puConnect("ADM-76", "adm76"),
  execTool("ADM-76", "setup: execute as adm76-sess1 opens a flow", "adm76-echo_tool", [A.status(400), keepFlow("adm76_f")], { session: "adm76-sess1" }),
  ...puFinish("ADM-76", "adm76_f", "alice", "the flow"),
  saveSession("ADM-76", "setup: find the OAuth token's entry", "adm76-u", sessionPred("adm76-sess1"), "adm76_t"),
  request("ADM-76", "setup: reauth the OAuth token, opening a pending flow", "POST", "/api/mcp/sessions/{{adm76_t}}/reauth", { asserts: [A.status(200), A.custom("session_id is set", ["pm.expect(pm.response.json().session_id, 'body: ' + pm.response.text()).to.be.a('string').and.not.empty;"])], extraTests: ["pm.collectionVariables.set('adm76_t_flow', pm.response.json().session_id);"] }),
  ...connectedO("ADM-76", "adm76s"),
  oauthStatus("ADM-76", "setup: record the shared token_id", "adm76s", "", [A.custom("token_id is set", ["pm.expect(pm.response.json().token_id, 'body: ' + pm.response.text()).to.be.a('string').and.not.empty;"])], ["pm.collectionVariables.set('adm76s_tok', pm.response.json().token_id);"]),
  request("ADM-76", "control: the header credential's flow is open", "GET", "/api/mcp/per-user-headers/flows/{{adm76_hc_flow}}", { asserts: [A.status(200)] }),
  flowDetail("ADM-76", "control: the OAuth token's pending flow is open", "adm76_t_flow", [A.status(200)]),
  request("ADM-76", "revoke the header credential", "DELETE", "/api/mcp/sessions/{{adm76_hc}}", { asserts: [A.status(204)] }),
  request("ADM-76", "the header credential's flow is gone", "GET", "/api/mcp/per-user-headers/flows/{{adm76_hc_flow}}", { asserts: [A.status(404)] }),
  request("ADM-76", "revoke the pending header flow", "DELETE", "/api/mcp/sessions/{{adm76_hf}}", { asserts: [A.status(204)] }),
  request("ADM-76", "revoke the OAuth token", "DELETE", "/api/mcp/sessions/{{adm76_t}}", { asserts: [A.status(204)] }),
  flowDetail("ADM-76", "the OAuth token's pending flow is gone", "adm76_t_flow", [A.status(404), A.msgEquals(NO_FLOW)]),
  request("ADM-76", "revoke the shared token id", "DELETE", "/api/mcp/sessions/{{adm76s_tok}}", { asserts: [A.status(404), A.msgEquals(NO_SESSION)] }),
  deleteClient("ADM-76", "adm76h-c"),
  deleteClient("ADM-76", "adm76-u"),
  deleteClient("ADM-76", "adm76s-o"),
  restoreConfig("ADM-76"),
]);

const ADM77 = folder("ADM-77 Per-user OAuth flow detail", `Setup: dashboard auth on, admin credentials; ${PU_TEXT(false)}; ${PU_SERVER_TEXT("adm77")}, verified as admin; a VK granted every tool of adm77, whose execute call opens a flow; then the client is reauthorized, opening an admin flow.`, [
  ...puConfig("ADM-77", false),
  ...puConnect("ADM-77", "adm77"),
  puVK("ADM-77", "first", "adm77", "adm77_vk1"),
  execTool("ADM-77", "setup: execute with the VK opens a flow", "adm77-echo_tool", [A.status(400), keepFlow("adm77_f")], vkExec("adm77_vk1")),
  flowDetail("ADM-77", "flow detail", "adm77_f", [A.status(200), A.jsonEquals("flow_mode", "vk"), A.jsonEquals("status", "pending"), A.jsonEquals("has_active_token", false), A.custom("oauth_config_id is the client's and virtual_key is the VK", [
    "var b = pm.response.json();",
    "pm.expect(b.oauth_config_id, 'body: ' + pm.response.text()).to.equal(pm.collectionVariables.get('adm77_oc'));",
    "pm.expect(b.virtual_key && b.virtual_key.id).to.equal(pm.collectionVariables.get('adm77_vk1_id'));",
    "pm.expect(b.virtual_key.name).to.equal('ADM-77 first key');",
  ])]),
  request("ADM-77", "setup: reauthorize, opening an admin flow", "POST", "/api/mcp/client/adm77-u/reauthorize", { asserts: [A.status(200), A.custom("flow_id is set", ["pm.expect(pm.response.json().flow_id, 'body: ' + pm.response.text()).to.be.a('string').and.not.empty;"])], extraTests: ["pm.collectionVariables.set('adm77_af', pm.response.json().flow_id);"] }),
  flowDetail("ADM-77", "the admin flow's id on the per-user flow endpoint", "adm77_af", [A.status(404), A.msgEquals(NO_FLOW)]),
  deleteClient("ADM-77", "adm77-u"),
  vkDelete("ADM-77", "teardown: delete the VK", "adm77_vk1"),
  restoreConfig("ADM-77"),
]);

const ADM78 = folder("ADM-78 Per-user OAuth start and callback", `Setup: dashboard auth on, admin credentials; ${PU_TEXT(false)}; ${PU_SERVER_TEXT("adm78")}, verified as admin. adm78-sess1's flow completes as alice through a callback with no cookie; adm78-sess2's completes as bob through a callback carrying the admin's dashboard session cookie.`, [
  ...puConfig("ADM-78", false),
  ...puConnect("ADM-78", "adm78"),
  execTool("ADM-78", "setup: execute as adm78-sess1 opens the first flow", "adm78-echo_tool", [A.status(400), keepFlow("adm78_f1")], { session: "adm78-sess1" }),
  flowStart("ADM-78", "start the first flow", "adm78_f1", [A.status(200), startUrl("adm78")]),
  consentAs("ADM-78", "setup: consent for the first flow as alice", "adm78_f1", "alice"),
  followCallback("ADM-78", "the callback without a cookie", "adm78_f1", [A.status(302), locationEnds(PU_SUCCESS)]),
  sessionsHas("ADM-78", "a token entry for adm78-sess1", "adm78-u", sessionPred("adm78-sess1"), "one token entry for adm78-sess1"),
  flowDetail("ADM-78", "the first flow is gone", "adm78_f1", [A.status(404), A.msgEquals(NO_FLOW)]),
  adminLogin("ADM-78"),
  execTool("ADM-78", "setup: execute as adm78-sess2 opens the second flow", "adm78-echo_tool", [A.status(400), keepFlow("adm78_f2")], { session: "adm78-sess2" }),
  flowStart("ADM-78", "setup: start the second flow", "adm78_f2"),
  consentAs("ADM-78", "setup: consent for the second flow as bob", "adm78_f2", "bob"),
  request("ADM-78", "the callback with a dashboard cookie", "GET", "", { base: "{{adm78_f2_cb}}", noAuth: true, noRedirect: true, noCookies: true, headers: [{ key: "Cookie", value: "token={{admin_cookie}}" }], asserts: [A.status(302), locationEnds("/workspace/mcp-sessions?completed=1")] }),
  sessionsHas("ADM-78", "a token entry for adm78-sess2", "adm78-u", sessionPred("adm78-sess2"), "one token entry for adm78-sess2"),
  flowDetail("ADM-78", "the second flow is gone", "adm78_f2", [A.status(404), A.msgEquals(NO_FLOW)]),
  adminLogout("ADM-78"),
  deleteClient("ADM-78", "adm78-u"),
  restoreConfig("ADM-78"),
]);

// ================================================================ OpenAPI clients against the mock's REST surface

// PETS is an OpenAPI document with GET /pets/{petId}, GET /pets?limit and POST /pets and no server URL; each test's
// client points openapi_config.base_url at a fleet server's REST surface, which answers an unrouted request with an
// echo of it ({"body","method","path","query"}, keys sorted) and logs every request with its headers.
const PETS = JSON.stringify({ openapi: "3.0.3", info: { title: "Pets", version: "1.0.0" }, paths: {
  "/pets": {
    get: { operationId: "listPets", parameters: [{ name: "limit", in: "query", schema: { type: "integer" } }], responses: { 200: { description: "ok" } } },
    post: { operationId: "createPet", requestBody: { required: true, content: { "application/json": { schema: { type: "object", properties: { name: { type: "string" } } } } } }, responses: { 201: { description: "created" } } },
  },
  "/pets/{petId}": { get: { operationId: "getPetById", parameters: [{ name: "petId", in: "path", required: true, schema: { type: "string" } }], responses: { 200: { description: "ok" } } } },
} });
const PETS_TEXT = "an inline OpenAPI document with GET /pets/{petId}, GET /pets?limit and POST /pets";
// PETS_KEY adds an apiKey scheme KeyAuth, sent in header X-Key, required by every operation.
const PETS_KEY = (() => { const d = JSON.parse(PETS); d.components = { securitySchemes: { KeyAuth: { type: "apiKey", in: "header", name: "X-Key" } } }; d.security = [{ KeyAuth: [] }]; return JSON.stringify(d); })();
const REST = (srv) => `${CHAOS}/s/${srv}/rest`;
const oasClient = (clientId, name, srv, over = {}, cfg = {}) => ({ client_id: clientId, name, connection_type: "openapi", auth_type: "none", tools_to_execute: ["*"], ...over, openapi_config: { spec: PETS, base_url: REST(srv), ...cfg } });
const execArgs = (id, title, tool, args, asserts, { session } = {}) => request(id, title, "POST", "/v1/mcp/tool/execute", {
  noAuth: true, noCookies: true, asserts,
  body: { id: "c1", type: "function", function: { name: tool, arguments: lit(args) } },
  headers: session ? [{ key: "x-bf-mcp-session-id", value: session }] : [],
});
const echoOf = (method, path, query, body) => JSON.stringify({ body, method, path, query });
const toolContent = (text) => A.custom(`content is ${lit(text)}`, [`pm.expect(pm.response.json().content, 'body: ' + pm.response.text()).to.equal(${lit(text)});`]);
const toolErrorFlag = (on) => A.custom(on ? "is_error is true" : "is_error is not set", [on ? "pm.expect(pm.response.json().is_error, 'body: ' + pm.response.text()).to.equal(true);" : "pm.expect(pm.response.json().is_error, 'body: ' + pm.response.text()).to.not.equal(true);"]);
const REST_CALLS = "var calls = ((pm.response.json().rest || {}).calls || []);";
const noRestCalls = () => A.custom("the REST log is empty", [REST_CALLS, "pm.expect(calls, 'body: ' + pm.response.text()).to.eql([]);"]);
// lastHeaders checks the latest REST call's headers (canonical names) against want.
const lastHeaders = (want) => A.custom(`the latest REST call carries ${Object.entries(want).map(([k, v]) => `${k}: ${v}`).join(" and ")}`, [
  REST_CALLS,
  "pm.expect(calls.length, 'body: ' + pm.response.text()).to.be.above(0);",
  "var h = calls[calls.length - 1].headers;",
  ...Object.entries(want).map(([k, v]) => `pm.expect(h[${lit(k)}], 'headers: ' + JSON.stringify(h)).to.equal(${lit(v)});`),
]);
// A 68-byte 1x1 PNG.
const PNG_B64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAAC0lEQVR4nGNgAAIAAAUAAXpeqz8AAAAASUVORK5CYII=";
const PREVIEW = "/api/mcp/openapi/preview";

const ADM80 = folder("ADM-80 OpenAPI preview", `Setup: dashboard auth on, admin credentials; alpha serves ${PETS_TEXT} at GET /openapi.json on its REST surface. The unauthenticated case whitelists ${PREVIEW} and sends no credential; the config is restored before the admin case.`, [
  readConfig("ADM-80"),
  ...freshServer("ADM-80", "alpha", ["echo_tool", "add_tool"]),
  mockPost("ADM-80", "alpha", "routes/add", "setup: alpha serves the document at GET /openapi.json", { route: { method: "GET", path: "/openapi.json", json: JSON.parse(PETS) } }),
  request("ADM-80", "body \"x\"", "POST", PREVIEW, { raw: "x", asserts: [A.status(400)] }),
  request("ADM-80", "body {}", "POST", PREVIEW, { body: {}, asserts: [A.status(400), A.msgEquals("spec or spec_url is required")] }),
  request("ADM-80", "a document that is not OpenAPI", "POST", PREVIEW, { body: { spec: "{\"foo\":1}" }, asserts: [A.status(400), A.msgStartsWith("Invalid OpenAPI spec:")] }),
  whitelist("ADM-80", [PREVIEW]),
  request("ADM-80", "loopback spec_url with no credential", "POST", PREVIEW, { noAuth: true, noCookies: true, body: { spec_url: `${REST("alpha")}/openapi.json` }, asserts: [A.status(403)] }),
  mockGet("ADM-80", "alpha", "control: alpha served nothing", [noRestCalls()]),
  restoreConfig("ADM-80"),
  request("ADM-80", "loopback spec_url with admin credentials", "POST", PREVIEW, { body: { spec_url: `${REST("alpha")}/openapi.json` }, asserts: [A.status(200)] }),
  mockGet("ADM-80", "alpha", "control: alpha served one GET /openapi.json to Bifrost", [A.custom("one REST call, GET /openapi.json, User-Agent Bifrost-OpenAPI-MCP/1.0", [
    REST_CALLS,
    "pm.expect(calls.length, 'body: ' + pm.response.text()).to.equal(1);",
    "pm.expect(calls[0].method).to.equal('GET');",
    "pm.expect(calls[0].path).to.equal('/openapi.json');",
    "pm.expect(calls[0].headers['User-Agent']).to.equal('Bifrost-OpenAPI-MCP/1.0');",
  ])]),
]);

const CONN23 = folder("CONN-23 OpenAPI request mapping", `Setup: fleet server conn23, whose REST surface echoes every request; an OpenAPI client conn23 from ${PETS_TEXT}, base_url conn23's REST surface, tools_to_execute ["*"].`, [
  ...freshServer("CONN-23", "conn23", ["echo_tool"]),
  createClient("CONN-23", oasClient("conn23-c", "conn23", "conn23"), "setup: create the OpenAPI client"),
  getClient("CONN-23", "conn23-c", "tools listed, no spec, operation_count 3", [A.custom("tools createPet, getPetById and listPets; no spec; operation_count 3", [
    "var c = pm.response.json().clients[0];",
    "pm.expect(c.tools.map(function (t) { return t.name; }).sort(), 'body: ' + pm.response.text()).to.eql(['createPet', 'getPetById', 'listPets']);",
    "pm.expect(c.config.openapi_config).to.not.have.property('spec');",
    "pm.expect(c.config.openapi_config.operation_count).to.equal(3);",
  ])]),
  execArgs("CONN-23", "execute getPetById with petId 7", "conn23-getPetById", { petId: "7" }, [A.status(200), toolContent(echoOf("GET", "/pets/7", "", ""))]),
  execArgs("CONN-23", "execute listPets with limit 2", "conn23-listPets", { limit: 2 }, [A.status(200), toolContent(echoOf("GET", "/pets", "limit=2", ""))]),
  execArgs("CONN-23", "execute createPet with name x", "conn23-createPet", { name: "x" }, [A.status(200), toolContent(echoOf("POST", "/pets", "", "{\"name\":\"x\"}"))]),
  mockGet("CONN-23", "conn23", "the REST log shows the three requests", [A.custom("GET /pets/7, GET /pets?limit=2, POST /pets with {\"name\":\"x\"}", [
    REST_CALLS,
    "pm.expect(calls.map(function (x) { return [x.method, x.path, x.query, x.body]; }), 'body: ' + pm.response.text()).to.eql([['GET', '/pets/7', '', ''], ['GET', '/pets', 'limit=2', ''], ['POST', '/pets', '', '{\"name\":\"x\"}']]);",
  ])]),
  deleteClient("CONN-23", "conn23-c"),
]);

const CONN24 = folder("CONN-24 OpenAPI responses", `Setup: fleet server conn24, whose REST surface answers GET /pets/404 with 404 {"e":1}, GET /pets/png with a 68-byte image/png and GET /pets/big with the 20-byte text 0123456789ABCDEFGHIJ; two OpenAPI clients from ${PETS_TEXT} with base_url conn24's REST surface: conn24 with the default response cap and conn24_t with max_response_bytes 10.`, [
  ...freshServer("CONN-24", "conn24", ["echo_tool"]),
  mockPost("CONN-24", "conn24", "routes/set", "setup: conn24 serves the 404, the PNG and the 20-byte text", { routes: [
    { method: "GET", path: "/pets/404", status: 404, content_type: "application/json", body: "{\"e\":1}" },
    { method: "GET", path: "/pets/png", content_type: "image/png", body_base64: PNG_B64 },
    { method: "GET", path: "/pets/big", content_type: "text/plain", body: "0123456789ABCDEFGHIJ" },
  ] }),
  createClient("CONN-24", oasClient("conn24-c", "conn24", "conn24"), "setup: create the OpenAPI client with the default cap"),
  createClient("CONN-24", oasClient("conn24-t", "conn24_t", "conn24", {}, { max_response_bytes: 10 }), "setup: create the OpenAPI client with max_response_bytes 10"),
  execArgs("CONN-24", "execute the 404", "conn24-getPetById", { petId: "404" }, [A.status(200), toolErrorFlag(true), toolContent("HTTP 404 Not Found\n{\"e\":1}")]),
  execArgs("CONN-24", "execute the PNG", "conn24-getPetById", { petId: "png" }, [A.status(200), toolContent("[binary response: image/png, 68 bytes]")]),
  execArgs("CONN-24", "execute the oversized body with the 10-byte cap", "conn24_t-getPetById", { petId: "big" }, [A.status(200), A.custom("content ends \"[response truncated at 10 bytes]\"", [
    "var t = pm.response.json().content;",
    "pm.expect(t, 'body: ' + pm.response.text()).to.be.a('string');",
    "pm.expect(t.slice(-'[response truncated at 10 bytes]'.length)).to.equal('[response truncated at 10 bytes]');",
  ])]),
  mockGet("CONN-24", "conn24", "setup: record conn24's REST call count", [], ["pm.collectionVariables.set('conn24_calls', ((pm.response.json().rest || {}).calls || []).length);"]),
  execArgs("CONN-24", "execute getPetById without petId", "conn24-getPetById", {}, [A.status(200), toolErrorFlag(true), toolContent("missing required path parameter \"petId\"")]),
  mockGet("CONN-24", "conn24", "control: no REST call for the missing path parameter", [A.custom("the REST call count is unchanged", [REST_CALLS, "pm.expect(calls.length, 'body: ' + pm.response.text()).to.equal(Number(pm.collectionVariables.get('conn24_calls')));"])]),
  deleteClient("CONN-24", "conn24-c"),
  deleteClient("CONN-24", "conn24-t"),
]);

const CONN25 = folder("CONN-25 OpenAPI credentials", `Setup: admin credentials; fleet servers conn25h, conn25k and conn25u, each with a REST surface that echoes and logs every request; OpenAPI clients from ${PETS_TEXT}: conn25h with auth_type headers and headers Authorization "Bearer T1" and X-Static "s"; conn25k from the same document with an apiKey scheme sent in header X-Key, credential k-1; conn25u with auth_type per_user_headers and per_user_header_keys ["x-user"], called as session conn25-sess1. Each points base_url at its own server's REST surface.`, [
  ...freshServer("CONN-25", "conn25h", ["echo_tool"]),
  ...freshServer("CONN-25", "conn25k", ["echo_tool"]),
  ...freshServer("CONN-25", "conn25u", ["echo_tool"]),
  createClient("CONN-25", oasClient("conn25h-c", "conn25h", "conn25h", { auth_type: "headers", headers: { Authorization: "Bearer T1", "X-Static": "s" } }), "setup: create the headers OpenAPI client"),
  createClient("CONN-25", oasClient("conn25k-c", "conn25k", "conn25k", {}, { spec: PETS_KEY, security_credentials: { KeyAuth: { value: "k-1" } } }), "setup: create the apiKey OpenAPI client"),
  createClient("CONN-25", oasClient("conn25u-c", "conn25u", "conn25u", { auth_type: "per_user_headers", per_user_header_keys: ["x-user"] }), "setup: create the per_user_headers OpenAPI client"),
  execArgs("CONN-25", "execute on the headers client", "conn25h-getPetById", { petId: "1" }, [A.status(200)]),
  mockGet("CONN-25", "conn25h", "the REST log shows Bearer T1 and X-Static", [lastHeaders({ Authorization: "Bearer T1", "X-Static": "s" })]),
  execArgs("CONN-25", "execute on the apiKey client", "conn25k-getPetById", { petId: "1" }, [A.status(200)]),
  mockGet("CONN-25", "conn25k", "the REST log shows the X-Key value", [lastHeaders({ "X-Key": "k-1" })]),
  execArgs("CONN-25", "execute on the per_user_headers client without a credential", "conn25u-getPetById", { petId: "1" }, [A.status(400), headersFlow({ saveAs: "conn25_hf" })], { session: "conn25-sess1" }),
  mockGet("CONN-25", "conn25u", "control: no REST call without a credential", [noRestCalls()]),
  request("CONN-25", "setup: submit x-user u-1", "PUT", "/api/mcp/per-user-headers/flows/{{conn25_hf}}", { body: { headers: { "x-user": "u-1" } }, asserts: [A.status(200)] }),
  execArgs("CONN-25", "execute on the per_user_headers client with a credential", "conn25u-getPetById", { petId: "1" }, [A.status(200)], { session: "conn25-sess1" }),
  mockGet("CONN-25", "conn25u", "the REST log shows the caller's value", [lastHeaders({ "X-User": "u-1" })]),
  createExpect("CONN-25", "create an OpenAPI client with auth_type oauth", oasClient("conn25o-c", "conn25o", "conn25h", { auth_type: "oauth", oauth_config: {} }), [A.status(400), A.msgContains("not supported for connection_type 'openapi'")]),
  deleteClient("CONN-25", "conn25h-c"),
  deleteClient("CONN-25", "conn25k-c"),
  deleteClient("CONN-25", "conn25u-c"),
]);

const CONN27_CRED = { KeyAuth: { value: "k-admin" } };
// deleteIfPresent removes a client that a known issue may have let through; once it is fixed there is nothing to delete.
const deleteIfPresent = (id, clientId) => request(id, `teardown: delete client ${clientId} if it exists`, "DELETE", `/api/mcp/client/${clientId}`, {
  asserts: [A.custom("status is 200 or 404", ["pm.expect(pm.response.code, 'body: ' + pm.response.text()).to.be.oneOf([200, 404]);"])],
});
const CONN27 = folder("CONN-27 OpenAPI per_user_headers with spec credentials is refused", `Setup: admin credentials; fleet server conn27 with a REST surface; ${PETS_TEXT}, with an apiKey scheme KeyAuth sent in header X-Key, and the credential k-admin for it.`, [
  ...freshServer("CONN-27", "conn27", ["echo_tool"]),
  createExpect("CONN-27", "create a per_user_headers OpenAPI client with security_credentials", oasClient("conn27u-c", "conn27u", "conn27", { auth_type: "per_user_headers", per_user_header_keys: ["x-key"] }, { spec: PETS_KEY, security_credentials: CONN27_CRED }), [A.status(400)]),
  request("CONN-27", "no per_user_headers client with security_credentials was created", "GET", "/api/mcp/clients?server=conn27u-c", { asserts: [A.status(200), A.custom("no client is returned", ["pm.expect(pm.response.json().clients, 'body: ' + pm.response.text()).to.eql([]);"])] }),
  deleteIfPresent("CONN-27", "conn27u-c"),
  createExpect("CONN-27", "control: create an auth_type none OpenAPI client with the same security_credentials", oasClient("conn27n-c", "conn27n", "conn27", {}, { spec: PETS_KEY, security_credentials: CONN27_CRED }), [A.status(200)]),
  request("CONN-27", "control: PUT security_credentials onto the auth_type none client", "PUT", "/api/mcp/client/conn27n-c", { body: { openapi_config: { security_credentials: CONN27_CRED } }, asserts: [A.status(200)] }),
  createClient("CONN-27", oasClient("conn27p-c", "conn27p", "conn27", { auth_type: "per_user_headers", per_user_header_keys: ["x-key"] }, { spec: PETS_KEY }), "setup: create a per_user_headers OpenAPI client without security_credentials"),
  request("CONN-27", "control: PUT openapi_config with only its base_url onto the per_user_headers client", "PUT", "/api/mcp/client/conn27p-c", { body: { openapi_config: { base_url: REST("conn27") } }, asserts: [A.status(200)] }),
  request("CONN-27", "PUT security_credentials onto the per_user_headers client", "PUT", "/api/mcp/client/conn27p-c", { body: { openapi_config: { security_credentials: CONN27_CRED } }, asserts: [A.status(400)] }),
  getClient("CONN-27", "conn27p-c", "the per_user_headers client keeps no security_credentials", [A.custom("openapi_config has no security_credentials", ["var oc = pm.response.json().clients[0].config.openapi_config || {};", "pm.expect(Object.keys(oc.security_credentials || {}), 'body: ' + pm.response.text()).to.eql([]);"])]),
  createExpect("CONN-27", "control: create a per_user_headers OpenAPI client storing KeyAuth while callers supply x-other", oasClient("conn27m-c", "conn27m", "conn27", { auth_type: "per_user_headers", per_user_header_keys: ["x-other"] }, { spec: PETS_KEY, security_credentials: CONN27_CRED }), [A.status(200)]),
  request("CONN-27", "PUT per_user_header_keys adding x-key onto the client storing KeyAuth", "PUT", "/api/mcp/client/conn27m-c", { body: { per_user_header_keys: ["x-other", "x-key"] }, asserts: [A.status(400)] }),
  getClient("CONN-27", "conn27m-c", "that client keeps per_user_header_keys [\"x-other\"]", [A.custom("per_user_header_keys is [\"x-other\"]", ["pm.expect(pm.response.json().clients[0].config.per_user_header_keys, 'body: ' + pm.response.text()).to.eql(['x-other']);"])]),
  deleteClient("CONN-27", "conn27n-c"),
  deleteClient("CONN-27", "conn27p-c"),
  deleteClient("CONN-27", "conn27m-c"),
]);

// ================================================================ the MCP library against the mock's HTTP catalog

// The runner boots Bifrost with mcp_library_url at fleet server library's GET /catalog.json, which serves a five-entry
// catalog (Harness Catalog Server, Delta Files, Bravo Search, Acme Notes, Echo Tickets, in that order). A test that
// changes the catalog reads it back from library first, so the boot catalog lives in one place, and at the end resets
// library to it and deletes the remote entries it synced (a delete hides them for good), which leaves the five boot
// entries as the visible library.
const LIB_BOOT = ["Harness Catalog Server", "Delta Files", "Bravo Search", "Acme Notes", "Echo Tickets"];
const LIB_BOOT_TEXT = `the boot catalog (${LIB_BOOT.join(", ")}, in that order)`;
const LIBRARY = "/api/mcp/library";
const libEntry = (name, slug, over = {}) => ({ name, description: "", category: "", connection_type: "http", connection_url: `https://mcp.example.invalid/${slug}/mcp`, auth_type: "none", ...over });
const readBoot = (id) => mockGet(id, "library", "setup: read the boot catalog", [], ["pm.collectionVariables.set('lib_boot', JSON.stringify(pm.response.json().rest.routes[0].json.servers));"]);
// setCatalog serves the boot catalog plus extra at library's GET /catalog.json, or extra alone with alone set.
const setCatalog = (id, title, extra, { alone = false } = {}) => request(id, title, "POST", "/control/library/routes/add", {
  base: CHAOS, noAuth: true, raw: "{{catalog_body}}", asserts: [A.status(200)],
  pre: [`var servers = ${alone ? "[]" : "JSON.parse(pm.collectionVariables.get('lib_boot'))"}.concat(${lit(extra)});`, "pm.variables.set('catalog_body', JSON.stringify({ route: { method: 'GET', path: '/catalog.json', json: { servers: servers } } }));"],
});
const resetLibrary = (id) => request(id, "teardown: reset library to the boot catalog", "POST", "/control", { base: CHAOS, noAuth: true, body: { reset: "library" }, asserts: [A.status(200)] });
const forceSync = (id, title, n) => request(id, title, "POST", `${LIBRARY}/force-sync`, { asserts: [A.status(200), A.jsonMessage(`MCP library sync completed, ${n} entries synced`)] });
// byName finds the listed entries named name (ES5, on the list response).
const byName = (name) => `var found = (pm.response.json().servers || []).filter(function (s) { return s.name === ${lit(name)}; });`;
const libSearch = (name) => `${LIBRARY}?search=${encodeURIComponent(name)}`;
// saveLibEntry finds the one entry named name and keeps its id in v and the entry itself in v_entry.
const saveLibEntry = (id, title, name, v, asserts = []) => request(id, title, "GET", libSearch(name), {
  asserts: [A.status(200), A.custom(`one entry named ${lit(name)}`, [byName(name), "pm.expect(found.length, 'body: ' + pm.response.text()).to.equal(1);"]), ...asserts],
  extraTests: [byName(name), `if (found.length) { pm.collectionVariables.set(${lit(v)}, found[0].id); pm.collectionVariables.set(${lit(v + "_entry")}, JSON.stringify(found[0])); }`],
});
const notListedLib = (id, title, name) => request(id, title, "GET", libSearch(name), { asserts: [A.status(200), A.custom(`no entry named ${lit(name)}`, [byName(name), "pm.expect(found, 'body: ' + pm.response.text()).to.eql([]);"])] });
const libDeleteAs = (id, title, v) => request(id, title, "DELETE", `${LIBRARY}/{{${v}}}`, { asserts: [A.status(200)] });
// namesAre checks the listed names, in order.
const namesAre = (names) => A.custom(`servers are ${names.join(", ")}`, [`pm.expect((pm.response.json().servers || []).map(function (s) { return s.name; }), 'body: ' + pm.response.text()).to.eql(${lit(names)});`]);
const SORTED = [...LIB_BOOT].sort();

const LIB01 = folder("LIB-01 GET /api/mcp/library after the boot sync", `Setup: ${LIB_BOOT_TEXT}, synced at boot into a fresh database.`, [
  request("LIB-01", "list the library", "GET", LIBRARY, { asserts: [A.status(200), A.jsonEquals("total_count", 5), A.jsonEquals("limit", 25), A.jsonEquals("offset", 0),
    A.custom("count equals the number of servers", ["var b = pm.response.json();", "pm.expect(b.count, 'body: ' + pm.response.text()).to.equal(b.servers.length);"]),
    namesAre(SORTED),
    A.custom("each entry has source \"remote\" and installed false", ["(pm.response.json().servers || []).forEach(function (s) { pm.expect(s.source, s.name).to.equal('remote'); pm.expect(s.installed, s.name).to.equal(false); });"]),
  ] }),
]);

const LIB02 = folder("LIB-02 Search", `Setup: as LIB-01 (${LIB_BOOT_TEXT}). "aCmE" matches Acme Notes by name, Delta Files by description ("maintained by Acme") and Bravo Search by publisher ("ACME Corp").`, [
  request("LIB-02", "search aCmE", "GET", `${LIBRARY}?search=aCmE`, { asserts: [A.status(200), A.jsonEquals("total_count", 3), namesAre(["Acme Notes", "Bravo Search", "Delta Files"])] }),
]);

// LIB-03 compares id sets: it keeps every boot entry's id by name first.
const lib03Ids = (names) => A.custom(`ids are exactly those of ${names.join(", ")}`, [
  "var ids = JSON.parse(pm.collectionVariables.get('lib03_ids'));",
  `var want = ${lit(names)}.map(function (n) { return ids[n]; }).sort(function (a, b) { return a - b; });`,
  "var got = (pm.response.json().servers || []).map(function (s) { return s.id; }).sort(function (a, b) { return a - b; });",
  "pm.expect(got, 'body: ' + pm.response.text()).to.eql(want);",
]);
const LIB03 = folder("LIB-03 Category, connection type and auth filters", `Setup: ${LIB_BOOT_TEXT}: categories testing, files, search, files and none; connection types http, sse, http, http, sse; auth types none, headers, oauth, none, headers.`, [
  request("LIB-03", "setup: keep each entry's id", "GET", `${LIBRARY}?limit=100`, { asserts: [A.status(200)], extraTests: ["var ids = {}; (pm.response.json().servers || []).forEach(function (s) { ids[s.name] = s.id; });", "pm.collectionVariables.set('lib03_ids', JSON.stringify(ids));"] }),
  request("LIB-03", "category=files,search", "GET", `${LIBRARY}?category=files,search`, { asserts: [A.status(200), lib03Ids(["Delta Files", "Bravo Search", "Acme Notes"])] }),
  request("LIB-03", "connection_type=sse", "GET", `${LIBRARY}?connection_type=sse`, { asserts: [A.status(200), lib03Ids(["Delta Files", "Echo Tickets"])] }),
  request("LIB-03", "auth_type=none,oauth", "GET", `${LIBRARY}?auth_type=none,oauth`, { asserts: [A.status(200), lib03Ids(["Harness Catalog Server", "Bravo Search", "Acme Notes"])] }),
  request("LIB-03", "category=files and connection_type=http", "GET", `${LIBRARY}?category=files&connection_type=http`, { asserts: [A.status(200), lib03Ids(["Acme Notes"])] }),
]);

const LIB05 = folder("LIB-05 Pagination and sort", `Setup: ${LIB_BOOT_TEXT}; name order is Acme Notes, Bravo Search, Delta Files, Echo Tickets, Harness Catalog Server.`, [
  request("LIB-05", "limit=2&offset=2", "GET", `${LIBRARY}?limit=2&offset=2`, { asserts: [A.status(200), namesAre(SORTED.slice(2, 4))] }),
  request("LIB-05", "limit=500", "GET", `${LIBRARY}?limit=500`, { asserts: [A.status(200), A.jsonEquals("limit", 100)] }),
  request("LIB-05", "limit=0", "GET", `${LIBRARY}?limit=0`, { asserts: [A.status(200), A.jsonEquals("limit", 25)] }),
  request("LIB-05", "sort_by=created_at&order=desc", "GET", `${LIBRARY}?sort_by=created_at&order=desc`, { asserts: [A.status(200), namesAre([...LIB_BOOT].reverse())] }),
  request("LIB-05", "an unknown sort_by", "GET", `${LIBRARY}?sort_by=bogus`, { asserts: [A.status(200), namesAre(SORTED)] }),
]);

const LIB07_D = libEntry("LIB-07 deleted entry", "lib07", { category: "lib07-deleted", tags: ["lib07-deleted"] });
const LIB07 = folder("LIB-07 Filter data", `Setup: ${LIB_BOOT_TEXT}, which has category "files" twice, an empty category and an empty tag; plus remote entry "LIB-07 deleted entry" with category and tag "lib07-deleted", synced and then deleted.`, [
  readBoot("LIB-07"),
  setCatalog("LIB-07", "setup: the catalog adds LIB-07 deleted entry", [LIB07_D]),
  forceSync("LIB-07", "setup: force-sync", 6),
  saveLibEntry("LIB-07", "setup: find LIB-07 deleted entry", "LIB-07 deleted entry", "lib07_d"),
  request("LIB-07", "control: filterdata lists the entry while it is live", "GET", `${LIBRARY}/filterdata`, { asserts: [A.status(200), A.custom("categories and tags include \"lib07-deleted\"", ["var b = pm.response.json();", "pm.expect(b.categories, 'body: ' + pm.response.text()).to.include('lib07-deleted');", "pm.expect(b.tags).to.include('lib07-deleted');"])] }),
  libDeleteAs("LIB-07", "setup: delete LIB-07 deleted entry", "lib07_d"),
  request("LIB-07", "filterdata", "GET", `${LIBRARY}/filterdata`, { asserts: [A.status(200), A.custom("sorted, deduplicated, no empty strings, the deleted entry's values excluded", [
    "var b = pm.response.json();",
    "pm.expect(b.categories, 'body: ' + pm.response.text()).to.eql(['files', 'search', 'testing']);",
    "pm.expect(b.connection_types).to.eql(['http', 'sse']);",
    "pm.expect(b.auth_types).to.eql(['headers', 'none', 'oauth']);",
    "pm.expect(b.tags).to.eql(['x', 'y']);",
  ])] }),
  resetLibrary("LIB-07"),
]);

const LIB14_R = libEntry("LIB-14 entry", "lib14", { category: "lib14-r", tags: ["lib14-r"] });
const LIB14 = folder("LIB-14 Deleted remote entry stays deleted after sync", `Setup: ${LIB_BOOT_TEXT} plus remote entry "LIB-14 entry" (category and tag "lib14-r"), synced; the entry is deleted while the catalog still lists it.`, [
  readBoot("LIB-14"),
  setCatalog("LIB-14", "setup: the catalog adds LIB-14 entry", [LIB14_R]),
  forceSync("LIB-14", "setup: force-sync", 6),
  saveLibEntry("LIB-14", "control: the entry is listed", "LIB-14 entry", "lib14_r"),
  request("LIB-14", "control: filterdata lists the entry while it is live", "GET", `${LIBRARY}/filterdata`, { asserts: [A.status(200), A.custom("categories and tags include \"lib14-r\"", ["var b = pm.response.json();", "pm.expect(b.categories, 'body: ' + pm.response.text()).to.include('lib14-r');", "pm.expect(b.tags).to.include('lib14-r');"])] }),
  libDeleteAs("LIB-14", "delete the entry", "lib14_r"),
  forceSync("LIB-14", "force-sync with the entry still in the catalog", 5),
  notListedLib("LIB-14", "the entry is not listed", "LIB-14 entry"),
  request("LIB-14", "filterdata lacks the entry's category and tag", "GET", `${LIBRARY}/filterdata`, { asserts: [A.status(200), A.custom("no \"lib14-r\" category or tag", [
    "var b = pm.response.json();",
    "pm.expect(b.categories, 'body: ' + pm.response.text()).to.not.include('lib14-r');",
    "pm.expect(b.tags).to.not.include('lib14-r');",
  ])] }),
  resetLibrary("LIB-14"),
]);

const LIB21 = folder("LIB-21 Force-sync adds new entries", `Setup: ${LIB_BOOT_TEXT} plus two new entries, "LIB-21 first" and "LIB-21 second".`, [
  readBoot("LIB-21"),
  setCatalog("LIB-21", "setup: the catalog adds LIB-21 first and LIB-21 second", [libEntry("LIB-21 first", "lib21a"), libEntry("LIB-21 second", "lib21b")]),
  forceSync("LIB-21", "force-sync", 7),
  saveLibEntry("LIB-21", "LIB-21 first is remote", "LIB-21 first", "lib21_a", [A.custom("source is \"remote\"", [byName("LIB-21 first"), "pm.expect(found[0].source).to.equal('remote');"])]),
  saveLibEntry("LIB-21", "LIB-21 second is remote", "LIB-21 second", "lib21_b", [A.custom("source is \"remote\"", [byName("LIB-21 second"), "pm.expect(found[0].source).to.equal('remote');"])]),
  resetLibrary("LIB-21"),
  libDeleteAs("LIB-21", "teardown: delete LIB-21 first", "lib21_a"),
  libDeleteAs("LIB-21", "teardown: delete LIB-21 second", "lib21_b"),
]);

const LIB22 = folder("LIB-22 Force-sync applies changes", `Setup: ${LIB_BOOT_TEXT} plus entry "LIB-22 entry" (description "before", URL .../lib22v1/mcp), synced; then the catalog changes its description to "after" and its URL to .../lib22v2/mcp.`, [
  readBoot("LIB-22"),
  setCatalog("LIB-22", "setup: the catalog adds the entry", [libEntry("LIB-22 entry", "lib22v1", { description: "before" })]),
  forceSync("LIB-22", "setup: force-sync", 6),
  saveLibEntry("LIB-22", "setup: read the entry", "LIB-22 entry", "lib22_e"),
  setCatalog("LIB-22", "setup: the catalog changes the entry's description and URL", [libEntry("LIB-22 entry", "lib22v2", { description: "after" })]),
  forceSync("LIB-22", "force-sync", 6),
  request("LIB-22", "the entry is updated, keeping its id and created_at", "GET", libSearch("LIB-22 entry"), { asserts: [A.status(200), A.custom("description and URL changed; id and created_at unchanged; updated_at later", [
    byName("LIB-22 entry"),
    "var before = JSON.parse(pm.collectionVariables.get('lib22_e_entry'));",
    "pm.expect(found.length, 'body: ' + pm.response.text()).to.equal(1);",
    "var e = found[0];",
    "pm.expect(e.description).to.equal('after');",
    "pm.expect(e.connection_url).to.equal('https://mcp.example.invalid/lib22v2/mcp');",
    "pm.expect(e.id).to.equal(before.id);",
    "pm.expect(e.created_at).to.equal(before.created_at);",
    "pm.expect(Date.parse(e.updated_at), e.updated_at + ' vs ' + before.updated_at).to.be.above(Date.parse(before.updated_at));",
  ])] }),
  resetLibrary("LIB-22"),
  libDeleteAs("LIB-22", "teardown: delete the entry", "lib22_e"),
]);

const LIB23 = folder("LIB-23 Entry removed from the catalog", `Setup: ${LIB_BOOT_TEXT} plus entry "LIB-23 entry", synced; then the entry is dropped from the catalog.`, [
  readBoot("LIB-23"),
  setCatalog("LIB-23", "setup: the catalog adds the entry", [libEntry("LIB-23 entry", "lib23")]),
  forceSync("LIB-23", "setup: force-sync", 6),
  request("LIB-23", "setup: the catalog drops the entry", "POST", "/control", { base: CHAOS, noAuth: true, body: { reset: "library" }, asserts: [A.status(200)] }),
  forceSync("LIB-23", "force-sync without the entry", 5),
  saveLibEntry("LIB-23", "the entry is still listed as remote", "LIB-23 entry", "lib23_e", [A.custom("source is \"remote\"", [byName("LIB-23 entry"), "pm.expect(found[0].source).to.equal('remote');"])]),
  libDeleteAs("LIB-23", "teardown: delete the entry", "lib23_e"),
]);

const LIB24 = folder("LIB-24 Catalog name clashes with a custom entry", `Setup: a custom entry "LIB-24 custom" (description "custom", URL .../lib24custom/mcp); ${LIB_BOOT_TEXT} plus a catalog entry with the same name, another description and URL. The catalog is reset before the custom entry is deleted.`, [
  request("LIB-24", "setup: publish the custom entry", "POST", LIBRARY, { body: { name: "LIB-24 custom", description: "custom", connection_type: "http", connection_url: "https://mcp.example.invalid/lib24custom/mcp" }, asserts: [A.status(200)], extraTests: ["pm.collectionVariables.set('lib24_c', pm.response.json().entry.id);"] }),
  readBoot("LIB-24"),
  setCatalog("LIB-24", "setup: the catalog adds an entry with the same name", [libEntry("LIB-24 custom", "lib24catalog", { description: "from the catalog" })]),
  forceSync("LIB-24", "force-sync", 5),
  request("LIB-24", "the custom entry is unchanged", "GET", libSearch("LIB-24 custom"), { asserts: [A.status(200), A.custom("one entry named LIB-24 custom, the custom one, unchanged", [
    byName("LIB-24 custom"),
    "pm.expect(found.length, 'body: ' + pm.response.text()).to.equal(1);",
    "pm.expect(found[0].id).to.equal(Number(pm.collectionVariables.get('lib24_c')));",
    "pm.expect(found[0].source).to.equal('custom');",
    "pm.expect(found[0].description).to.equal('custom');",
    "pm.expect(found[0].connection_url).to.equal('https://mcp.example.invalid/lib24custom/mcp');",
  ])] }),
  resetLibrary("LIB-24"),
  libDeleteAs("LIB-24", "teardown: delete the custom entry", "lib24_c"),
]);

const LIB25_NAMES = A.custom("the names are the five boot entries and LIB-25 Dup", ["pm.expect(pm.response.json().servers.map(function (s) { return s.name; }).sort(), 'body: ' + pm.response.text()).to.eql(['Acme Notes', 'Bravo Search', 'Delta Files', 'Echo Tickets', 'Harness Catalog Server', 'LIB-25 Dup']);"]);
const LIB25 = folder("LIB-25 Payload edge cases", `Setup: ${LIB_BOOT_TEXT} plus "LIB-25 Dup" twice (descriptions "first" and "second"), an entry with an empty name and one named "!!!"; then a catalog of {"servers":[]}.`, [
  readBoot("LIB-25"),
  setCatalog("LIB-25", "setup: the catalog adds the duplicates and the bad names", [libEntry("LIB-25 Dup", "lib25a", { description: "first" }), libEntry("LIB-25 Dup", "lib25b", { description: "second" }), libEntry("", "lib25c"), libEntry("!!!", "lib25d")]),
  forceSync("LIB-25", "force-sync", 6),
  saveLibEntry("LIB-25", "the duplicates became one entry", "LIB-25 Dup", "lib25_dup"),
  request("LIB-25", "the library has the five boot entries and LIB-25 Dup", "GET", `${LIBRARY}?limit=100`, { asserts: [A.status(200), A.jsonEquals("total_count", 6), LIB25_NAMES] }),
  setCatalog("LIB-25", "setup: the catalog is {\"servers\":[]}", [], { alone: true }),
  forceSync("LIB-25", "force-sync the empty catalog", 0),
  request("LIB-25", "the entries are kept", "GET", `${LIBRARY}?limit=100`, { asserts: [A.status(200), A.jsonEquals("total_count", 6), LIB25_NAMES] }),
  resetLibrary("LIB-25"),
  libDeleteAs("LIB-25", "teardown: delete LIB-25 Dup", "lib25_dup"),
]);

const LIB26 = folder("LIB-26 Catalog returns HTTP 500", "Setup: library answers every request with 500; its request log is cleared first.", [
  mockPost("LIB-26", "library", "counters/reset", "setup: clear library's request log", {}),
  mockPost("LIB-26", "library", "faults", "setup: library answers 500", { faults: { status: 500 } }),
  request("LIB-26", "force-sync", "POST", `${LIBRARY}/force-sync`, { asserts: [A.status(500), A.custom("error message matches \"failed to sync MCP library: ... HTTP 500\"", ["pm.expect(pm.response.json().error.message, 'body: ' + pm.response.text()).to.match(/^failed to sync MCP library: .* HTTP 500$/);"])] }),
  mockGet("LIB-26", "library", "the catalog was hit 4 times", [A.custom("4 requests for /catalog.json, each answered 500", [
    "var calls = ((pm.response.json().rest || {}).calls || []).filter(function (c) { return c.path === '/catalog.json'; });",
    "pm.expect(calls.length, 'body: ' + pm.response.text()).to.equal(4);",
    "calls.forEach(function (c) { pm.expect(c.status).to.equal(500); });",
  ])]),
  resetLibrary("LIB-26"),
]);

const LIB34 = folder("LIB-34 Loopback URL over the API", "Setup: PUT /api/config with framework_config.mcp_library_url set to the mock catalog's URL spelled with a query (?lib34=1), so it differs from the stored URL; library's request log is cleared first.", [
  readConfig("LIB-34"),
  request("LIB-34", "control: the URL answers 200", "GET", "/s/library/rest/catalog.json?lib34=1", { base: CHAOS, noAuth: true, asserts: [A.status(200)] }),
  mockPost("LIB-34", "library", "counters/reset", "setup: clear library's request log", {}),
  frameworkPut("LIB-34", "PUT the mock catalog URL", "mcp_library_url", "pm.variables.get('chaos_url') + '/s/library/rest/catalog.json?lib34=1'", [A.status(400), A.msgContains("url is not reachable")]),
  mockGet("LIB-34", "library", "control: the PUT never reached the catalog", [A.custom("library's request log is empty", ["pm.expect(((pm.response.json().rest || {}).calls || []), 'body: ' + pm.response.text()).to.eql([]);"])]),
]);

const LIB36 = folder("LIB-36 Interval validation over the API", "Setup: PUT /api/config with framework_config.mcp_library_sync_interval 0, then -1.", [
  readConfig("LIB-36"),
  frameworkPut("LIB-36", "PUT mcp_library_sync_interval 0", "mcp_library_sync_interval", "0", [A.status(400), A.msgEquals("MCP library sync interval must be greater than 0")]),
  frameworkPut("LIB-36", "PUT mcp_library_sync_interval -1", "mcp_library_sync_interval", "-1", [A.status(400), A.msgEquals("MCP library sync interval must be greater than 0")]),
]);

// ================================================================ MCP tools inside model requests, on the mock LLM

// The runner points the openai, anthropic and gemini providers at fleet server llm, which answers each model request
// with the turns a test scripts for its scenario and logs what it was sent. Every request a test makes carries
// "[scenario:<key>]" in its first user message; the key is the lowercased test ID, plus a leg when a test needs two.
const LLM_CTL = "/control/llm/llm";
const MODEL = "openai/mock-model";
const LLM_TEXT = "the openai, anthropic and gemini providers point at the fleet's mock LLM (model mock-model), which answers each request with the turns scripted for its scenario and logs what it was sent";
const marker = (key) => `[scenario:${key}]`;
// llmScript replaces a scenario's turns (turns, then) on the mock LLM.
const llmScript = (id, key, script, title) => request(id, title || `setup: script the mock LLM for ${key}`, "POST", `${LLM_CTL}/script`, {
  base: CHAOS, noAuth: true, body: { scenario: key, ...script }, asserts: [A.status(200)],
});
const TEXT_OK = { then: { text: "ok" } };
// llmCaptured reads a scenario's logged requests; asserts see them as `reqs` (ES5).
const REQS = "var reqs = pm.response.json().requests || [];";
const llmCaptured = (id, title, key, asserts) => request(id, title, "GET", `${LLM_CTL}/requests?scenario=${key}`, { base: CHAOS, noAuth: true, asserts: [A.status(200), ...asserts] });
const reqCount = (n) => A.custom(`${n} model request${n === 1 ? "" : "s"} logged`, [REQS, `pm.expect(reqs.length, 'body: ' + pm.response.text()).to.equal(${n});`]);
// toolsAre checks the declared tool names of request i, in order.
const toolsAre = (i, names) => A.custom(`request ${i + 1} declares exactly ${lit(names)}`, [REQS, `pm.expect(reqs[${i}] && reqs[${i}].tools, 'body: ' + pm.response.text()).to.eql(${lit(names)});`]);
const toolsInclude = (i, name) => A.custom(`request ${i + 1} declares ${name}`, [REQS, `pm.expect(reqs[${i}] && reqs[${i}].tools, 'body: ' + pm.response.text()).to.include(${lit(name)});`]);
// MCP_BLOCKS counts the instruction blocks in a request's parsed body; the raw body escapes "<" as <.
const MCP_TEXT = "function mcpText(r) { return JSON.stringify(r.json); }";
const noInstructions = (i) => A.custom(`request ${i + 1} carries no <mcp_server block`, [REQS, MCP_TEXT, `pm.expect(mcpText(reqs[${i}]), 'request').to.not.include('<mcp_server');`]);
const ALPHA_GREET_LLM = `${ALPHA_GREET_TEXT}; ${LLM_TEXT}`;
const userMsg = (key, text = "hi") => ({ role: "user", content: `${marker(key)} ${text}` });
// infChat sends a chat completion through route path (default /v1/chat/completions) with no admin credentials.
const infChat = (id, title, key, asserts, { path = "/v1/chat/completions", body = {}, headers = [], messages, model = MODEL } = {}) => request(id, title, "POST", path, {
  noAuth: true, noCookies: true, headers, asserts,
  body: { model, messages: messages || [userMsg(key)], ...body },
});
const infResponses = (id, title, key, asserts, { body = {}, headers = [] } = {}) => request(id, title, "POST", "/v1/responses", {
  noAuth: true, noCookies: true, headers, asserts, body: { model: MODEL, input: `${marker(key)} hi`, ...body },
});
const ALPHA3 = ["alpha-add", "alpha-echo", "alpha-greet"];
const alphaClient = (id, over = {}) => createClient(id, httpClient(`${id.toLowerCase()}-alpha`, "alpha", "alpha", { tools_to_execute: ["*"], ...over }), "setup: register client alpha");
const betaSetup = (id) => [
  ...freshServer(id, "beta", ["zz", "aa"]),
  createClient(id, httpClient(`${id.toLowerCase()}-beta`, "beta", "beta", { tools_to_execute: ["*"] }), "setup: register client beta"),
];
const betaTeardown = (id) => deleteClient(id, `${id.toLowerCase()}-beta`);

const INF01 = folder("INF-01 Chat injection, stream and not", `Setup: ${ALPHA_GREET_LLM}.`, [
  ...alphaGreetSetup("INF-01"),
  llmScript("INF-01", "inf01", TEXT_OK),
  infChat("INF-01", "chat completion", "inf01", [A.status(200)]),
  infChat("INF-01", "streamed chat completion", "inf01", [A.status(200), A.custom("the stream ends with [DONE]", ["pm.expect(pm.response.text().trim().slice(-12), 'stream').to.equal('data: [DONE]');"])], { body: { stream: true } }),
  llmCaptured("INF-01", "both requests declare exactly the alpha tools, type function", "inf01", [reqCount(2), toolsAre(0, ALPHA3), toolsAre(1, ALPHA3), A.custom("every declared tool has type function", [REQS, "reqs.forEach(function (r) { r.json.tools.forEach(function (t) { pm.expect(t.type, JSON.stringify(t)).to.equal('function'); }); });"])]),
  alphaGreetTeardown("INF-01"),
]);

const MY_TOOL = (desc) => ({ type: "function", function: { name: "my_tool", ...(desc ? { description: desc } : {}), parameters: { type: "object", properties: {} } } });
const INF02 = folder("INF-02 Responses injection", `Setup: ${ALPHA_GREET_LLM}; the caller sends its own function tool my_tool.`, [
  ...alphaGreetSetup("INF-02"),
  llmScript("INF-02", "inf02", TEXT_OK),
  infResponses("INF-02", "Responses request with my_tool", "inf02", [A.status(200)], { body: { tools: [{ type: "function", name: "my_tool", parameters: { type: "object", properties: {} } }] } }),
  llmCaptured("INF-02", "the alpha function tools follow the caller's", "inf02", [reqCount(1), toolsAre(0, ["my_tool", ...ALPHA3]), A.custom("every declared tool has type function", [REQS, "reqs[0].json.tools.forEach(function (t) { pm.expect(t.type, JSON.stringify(t)).to.equal('function'); });"])]),
  alphaGreetTeardown("INF-02"),
]);

const INF03 = folder("INF-03 Anthropic route", `Setup: ${ALPHA_GREET_LLM}; /anthropic/v1/messages with model mock-model and no Claude Code user agent; the mock LLM answers with a tool_use for alpha-echo.`, [
  ...alphaGreetSetup("INF-03"),
  llmScript("INF-03", "inf03", { then: { tool_calls: [{ id: "tu1", name: "alpha-echo", arguments: { text: "hi" } }] } }),
  request("INF-03", "Anthropic messages request", "POST", "/anthropic/v1/messages", { noAuth: true, noCookies: true, body: { model: "mock-model", max_tokens: 64, messages: [userMsg("inf03")] }, asserts: [A.status(200), A.custom("the client gets a tool_use named alpha-echo", [
    "var c = (pm.response.json().content || []).filter(function (b) { return b.type === 'tool_use'; });",
    "pm.expect(c.length, 'body: ' + pm.response.text()).to.equal(1);",
    "pm.expect(c[0].name).to.equal('alpha-echo');",
  ])] }),
  llmCaptured("INF-03", "the request declares alpha-echo", "inf03", [reqCount(1), toolsInclude(0, "alpha-echo"), A.custom("it reached the anthropic format", [REQS, "pm.expect(reqs[0].format).to.equal('anthropic.messages');"])]),
  alphaGreetTeardown("INF-03"),
]);

const GENAI_BODY = (key) => ({ contents: [{ role: "user", parts: [{ text: `${marker(key)} hi` }] }] });
const INF04 = folder("INF-04 GenAI route", `Setup: ${ALPHA_GREET_LLM}; generateContent and streamGenerateContent on /genai with the bare model mock-model; the mock LLM answers with a functionCall for alpha-echo.`, [
  ...alphaGreetSetup("INF-04"),
  llmScript("INF-04", "inf04", { then: { tool_calls: [{ id: "fc1", name: "alpha-echo", arguments: { text: "hi" } }] } }),
  request("INF-04", "generateContent", "POST", "/genai/v1beta/models/mock-model:generateContent", { noAuth: true, noCookies: true, body: GENAI_BODY("inf04"), asserts: [A.status(200), A.custom("the client gets a functionCall named alpha-echo", [
    "var parts = ((pm.response.json().candidates || [])[0] || {}).content.parts || [];",
    "var calls = parts.filter(function (p) { return p.functionCall; });",
    "pm.expect(calls.length, 'body: ' + pm.response.text()).to.equal(1);",
    "pm.expect(calls[0].functionCall.name).to.equal('alpha-echo');",
  ])] }),
  request("INF-04", "streamGenerateContent", "POST", "/genai/v1beta/models/mock-model:streamGenerateContent?alt=sse", { noAuth: true, noCookies: true, body: GENAI_BODY("inf04"), asserts: [A.status(200), A.custom("the stream carries a functionCall named alpha-echo", [
    "var names = [];",
    "pm.response.text().split('\\n').forEach(function (l) { if (l.indexOf('data: ') !== 0) { return; } var d = JSON.parse(l.slice(6)); ((((d.candidates || [])[0] || {}).content || {}).parts || []).forEach(function (p) { if (p.functionCall) { names.push(p.functionCall.name); } }); });",
    "pm.expect(names, 'stream: ' + pm.response.text()).to.eql(['alpha-echo']);",
  ])] }),
  llmCaptured("INF-04", "both requests declare alpha-echo", "inf04", [reqCount(2), toolsInclude(0, "alpha-echo"), toolsInclude(1, "alpha-echo"), A.custom("one plain and one streamed genai request", [REQS, "pm.expect(reqs.map(function (r) { return r.format + ':' + r.stream; })).to.eql(['genai:false', 'genai:true']);"])]),
  alphaGreetTeardown("INF-04"),
]);

const ALIAS_ROUTES = ["/openai/v1/chat/completions", "/litellm/v1/chat/completions", "/langchain/v1/chat/completions", "/pydanticai/v1/chat/completions"];
const INF06 = folder("INF-06 Alias routes", `Setup: ${ALPHA_GREET_LLM}; a chat request with model openai/mock-model on /openai, /litellm, /langchain and /pydanticai, and a Cohere v2 chat on /cohere/v2/chat.`, [
  ...alphaGreetSetup("INF-06"),
  llmScript("INF-06", "inf06", TEXT_OK),
  ...ALIAS_ROUTES.map((p) => infChat("INF-06", `chat on ${p.split("/")[1]}`, "inf06", [A.status(200)], { path: p })),
  infChat("INF-06", "Cohere v2 chat", "inf06", [A.status(200)], { path: "/cohere/v2/chat" }),
  llmCaptured("INF-06", "every request carries the three alpha tools", "inf06", [reqCount(5), A.custom("each of the 5 requests declares exactly the alpha tools", [REQS, `reqs.forEach(function (r) { pm.expect(r.tools, r.path).to.eql(${lit(ALPHA3)}); });`])]),
  alphaGreetTeardown("INF-06"),
]);

const INF07_BODY = `{"model":"mock-model","messages":[{"role":"user","content":"${marker("inf07")} hi"}]}`;
const INF07 = folder("INF-07 Passthrough does not inject", `Setup: ${ALPHA_GREET_LLM}; /openai_passthrough/v1/chat/completions with a raw body naming model mock-model.`, [
  ...freshServer("INF-07", "alpha", ["echo", "add", "greet"]),
  setInstructions("INF-07", "alpha", "Alpha instructions", lit("Alpha instructions")),
  alphaClient("INF-07"),
  llmScript("INF-07", "inf07", TEXT_OK),
  request("INF-07", "passthrough chat completion", "POST", "/openai_passthrough/v1/chat/completions", { noAuth: true, noCookies: true, raw: INF07_BODY, asserts: [A.status(200)] }),
  llmCaptured("INF-07", "the upstream got the sent body byte for byte", "inf07", [reqCount(1), A.custom("body equals the sent body: no alpha tool, no <mcp_server text", [REQS,
    `pm.expect(reqs[0].body).to.equal(${lit(INF07_BODY)});`,
    "pm.expect(reqs[0].tools).to.eql([]);",
  ]), noInstructions(0)]),
  alphaGreetTeardown("INF-07"),
]);

const INF09 = folder("INF-09 Caller tools merge with injected tools", `Setup: ${ALPHA_GREET_LLM}; the caller sends my_tool and its own alpha-echo with description "CALLER".`, [
  ...alphaGreetSetup("INF-09"),
  llmScript("INF-09", "inf09", TEXT_OK),
  infChat("INF-09", "chat with the caller's tools", "inf09", [A.status(200)], { body: { tools: [MY_TOOL(), { type: "function", function: { name: "alpha-echo", description: "CALLER", parameters: { type: "object", properties: {} } } }] } }),
  llmCaptured("INF-09", "my_tool, the caller's alpha-echo once, then alpha-add and alpha-greet", "inf09", [reqCount(1), toolsAre(0, ["my_tool", "alpha-echo", "alpha-add", "alpha-greet"]), A.custom("alpha-echo is the caller's, described \"CALLER\"", [REQS, "pm.expect(reqs[0].json.tools[1].function.description).to.equal('CALLER');"])]),
  alphaGreetTeardown("INF-09"),
]);

const INF10 = folder("INF-10 Deterministic tool order", `Setup: ${ALPHA_GREET_LLM}; plus beta (zz, aa) registered as client beta with tools_to_execute ["*"], registered before alpha; two identical chat requests.`, [
  ...betaSetup("INF-10"),
  ...alphaGreetSetup("INF-10"),
  llmScript("INF-10", "inf10", TEXT_OK),
  infChat("INF-10", "first chat", "inf10", [A.status(200)]),
  infChat("INF-10", "second chat", "inf10", [A.status(200)]),
  llmCaptured("INF-10", "both requests declare the tools in the same order", "inf10", [reqCount(2), toolsAre(0, [...ALPHA3, "beta-aa", "beta-zz"]), toolsAre(1, [...ALPHA3, "beta-aa", "beta-zz"])]),
  betaTeardown("INF-10"),
  alphaGreetTeardown("INF-10"),
]);

const INF11 = folder("INF-11 Unsafe upstream names dropped", `Setup: ${ALPHA_GREET_LLM}; alpha then also serves "fresh", "bad/name" and "a..b", and the client's tools are refreshed.`, [
  ...alphaGreetSetup("INF-11"),
  mockPost("INF-11", "alpha", "tools/set", "setup: alpha also serves fresh, bad/name and a..b", { tools: ["echo", "add", "greet", "fresh", "bad/name", "a..b"].map((name) => ({ name })) }),
  request("INF-11", "setup: refresh the client's tools", "POST", "/api/mcp/client/inf-11-alpha/refresh-tools", { asserts: [A.status(200)] }),
  getClient("INF-11", "inf-11-alpha", "fresh is listed and neither unsafe name is", [A.custom("the listed tools are add, echo, fresh, greet", ["pm.expect(pm.response.json().clients[0].tools.map(function (t) { return t.name; }).sort(), 'body: ' + pm.response.text()).to.eql(['add', 'echo', 'fresh', 'greet']);"])]),
  llmScript("INF-11", "inf11", TEXT_OK),
  infChat("INF-11", "chat", "inf11", [A.status(200)]),
  llmCaptured("INF-11", "alpha-fresh is injected and neither unsafe name is", "inf11", [reqCount(1), toolsAre(0, ["alpha-add", "alpha-echo", "alpha-fresh", "alpha-greet"])]),
  execTool("INF-11", "execute alpha-bad/name", "alpha-bad/name", [A.status(400), A.msgContains("is not available or not permitted")]),
  alphaGreetTeardown("INF-11"),
]);

const ccTool = (name) => ({ type: "function", function: { name, parameters: { type: "object", properties: {} } } });
const INF12 = folder("INF-12 Claude Code and Codex dedupe", `Setup: ${ALPHA_GREET_LLM}; alpha also serves get-time; on /openai, user agent "claude-cli/2.0" sending mcp__bifrost__alpha-echo, then user agent "codex-cli/1.0" sending mcp__bifrost__alpha_get_time.`, [
  ...freshServer("INF-12", "alpha", ["echo", "add", "greet", "get-time"]),
  alphaClient("INF-12"),
  llmScript("INF-12", "inf12", TEXT_OK),
  infChat("INF-12", "Claude Code request", "inf12", [A.status(200)], { path: "/openai/v1/chat/completions", headers: [{ key: "User-Agent", value: "claude-cli/2.0" }], body: { tools: [ccTool("mcp__bifrost__alpha-echo")] } }),
  infChat("INF-12", "Codex request", "inf12", [A.status(200)], { path: "/openai/v1/chat/completions", headers: [{ key: "User-Agent", value: "codex-cli/1.0" }], body: { tools: [ccTool("mcp__bifrost__alpha_get_time")] } }),
  llmCaptured("INF-12", "no duplicate alpha-echo; alpha-get-time not injected for Codex", "inf12", [reqCount(2),
    toolsAre(0, ["mcp__bifrost__alpha-echo", "alpha-add", "alpha-get-time", "alpha-greet"]),
    toolsAre(1, ["mcp__bifrost__alpha_get_time", "alpha-add", "alpha-echo", "alpha-greet"]),
  ]),
  deleteClient("INF-12", "inf-12-alpha"),
]);

const INF14 = folder("INF-14 Auto-inject off", `Setup: ${ALPHA_GREET_LLM}; alpha has instructions; mcp_disable_auto_tool_inject true.`, [
  readConfig("INF-14"),
  ...freshServer("INF-14", "alpha", ["echo", "add", "greet"]),
  setInstructions("INF-14", "alpha", "Alpha instructions", lit("Alpha instructions")),
  alphaClient("INF-14"),
  putConfig("INF-14", "setup: mcp_disable_auto_tool_inject true", ["cc.mcp_disable_auto_tool_inject = true;"], [A.status(200)]),
  request("INF-14", "the config reads true", "GET", "/api/config", { asserts: [A.status(200), A.custom("client_config.mcp_disable_auto_tool_inject is true", ["pm.expect(pm.response.json().client_config.mcp_disable_auto_tool_inject, 'body').to.equal(true);"])] }),
  llmScript("INF-14", "inf14", TEXT_OK),
  infChat("INF-14", "chat", "inf14", [A.status(200)]),
  infResponses("INF-14", "Responses request", "inf14", [A.status(200)]),
  llmCaptured("INF-14", "no alpha tool and no instructions in either", "inf14", [reqCount(2), toolsAre(0, []), toolsAre(1, []), noInstructions(0), noInstructions(1)]),
  alphaGreetTeardown("INF-14"),
  restoreConfig("INF-14"),
]);

const systemBlock = (i, name, text) => A.custom(`request ${i + 1} opens with ${name}'s instructions block as a system message`, [REQS, `pm.expect(reqs[${i}].json.messages[0]).to.eql({ role: 'system', content: ${lit(`<mcp_server name="${name}">\n${text}\n</mcp_server>`)} });`]);
const INF15 = folder("INF-15 Opt-in headers with auto-inject off", `Setup: ${ALPHA_GREET_LLM}; alpha has instructions; mcp_disable_auto_tool_inject true; one chat with x-bf-mcp-include-clients alpha, then one with x-bf-mcp-include-tools alpha-echo.`, [
  readConfig("INF-15"),
  ...freshServer("INF-15", "alpha", ["echo", "add", "greet"]),
  setInstructions("INF-15", "alpha", "Alpha instructions", lit("Alpha instructions")),
  alphaClient("INF-15", {}),
  putConfig("INF-15", "setup: mcp_disable_auto_tool_inject true", ["cc.mcp_disable_auto_tool_inject = true;"], [A.status(200)]),
  request("INF-15", "control: the config reads mcp_disable_auto_tool_inject true", "GET", "/api/config", { asserts: [A.status(200), A.custom("client_config.mcp_disable_auto_tool_inject is true", ["pm.expect(pm.response.json().client_config.mcp_disable_auto_tool_inject, 'body: ' + pm.response.text()).to.equal(true);"])] }),
  llmScript("INF-15", "inf15", TEXT_OK),
  infChat("INF-15", "chat with include-clients alpha", "inf15", [A.status(200)], { headers: [inc("clients", "alpha")] }),
  infChat("INF-15", "chat with include-tools alpha-echo", "inf15", [A.status(200)], { headers: [inc("tools", "alpha-echo")] }),
  infChat("INF-15", "control: chat with no include header", "inf15", [A.status(200)]),
  llmCaptured("INF-15", "all alpha tools plus instructions, then only alpha-echo; nothing without a header", "inf15", [reqCount(3), toolsAre(0, ALPHA3), systemBlock(0, "alpha", "Alpha instructions"), toolsAre(1, ["alpha-echo"]), toolsAre(2, []), noInstructions(2)]),
  alphaGreetTeardown("INF-15"),
  restoreConfig("INF-15"),
]);

const INF16 = folder("INF-16 Header narrowing with auto-inject on", `Setup: ${ALPHA_GREET_LLM}; plus beta (zz, aa) as client beta; alpha and beta both have instructions; chats with x-bf-mcp-include-clients beta, x-bf-mcp-include-tools alpha-*, and x-bf-mcp-include-clients empty.`, [
  ...freshServer("INF-16", "alpha", ["echo", "add", "greet"]),
  setInstructions("INF-16", "alpha", "Alpha instructions", lit("Alpha instructions")),
  alphaClient("INF-16"),
  ...freshServer("INF-16", "beta", ["zz", "aa"]),
  setInstructions("INF-16", "beta", "Beta instructions", lit("Beta instructions")),
  createClient("INF-16", httpClient("inf-16-beta", "beta", "beta", { tools_to_execute: ["*"] }), "setup: register client beta"),
  llmScript("INF-16", "inf16", TEXT_OK),
  infChat("INF-16", "chat with include-clients beta", "inf16", [A.status(200)], { headers: [inc("clients", "beta")] }),
  infChat("INF-16", "chat with include-tools alpha-*", "inf16", [A.status(200)], { headers: [inc("tools", "alpha-*")] }),
  infChat("INF-16", "chat with include-clients empty", "inf16", [A.status(200)], { headers: [inc("clients", "")] }),
  infChat("INF-16", "control: chat with no include header", "inf16", [A.status(200)]),
  llmCaptured("INF-16", "beta only; all alpha; nothing; everything without a header", "inf16", [reqCount(4),
    toolsAre(0, ["beta-aa", "beta-zz"]), systemBlock(0, "beta", "Beta instructions"),
    toolsAre(1, ALPHA3), systemBlock(1, "alpha", "Alpha instructions"),
    toolsAre(2, []), noInstructions(2),
    toolsAre(3, [...ALPHA3, "beta-aa", "beta-zz"]),
    A.custom("request 4 opens with both blocks, alpha then beta", [REQS, `pm.expect(reqs[3].json.messages[0]).to.eql({ role: 'system', content: ${lit(block("alpha", "Alpha instructions") + "\n\n" + block("beta", "Beta instructions"))} });`]),
  ]),
  deleteClient("INF-16", "inf-16-beta"),
  alphaGreetTeardown("INF-16"),
]);

const INF17 = folder("INF-17 Headers cannot widen the allow-list", `Setup: ${LLM_TEXT}; alpha (echo, add, greet) registered as client alpha with tools_to_execute ["echo"]; x-bf-mcp-include-tools alpha-greet.`, [
  ...freshServer("INF-17", "alpha", ["echo", "add", "greet"]),
  alphaClient("INF-17", { tools_to_execute: ["echo"] }),
  llmScript("INF-17", "inf17", TEXT_OK),
  infChat("INF-17", "chat with include-tools alpha-greet", "inf17", [A.status(200)], { headers: [inc("tools", "alpha-greet")] }),
  llmCaptured("INF-17", "nothing is injected", "inf17", [reqCount(1), toolsAre(0, [])]),
  execTool("INF-17", "execute alpha-greet with include-tools alpha-greet", "alpha-greet", [A.status(400), A.msgContains("not in client's ToolsToExecute allow-list")], { headers: [inc("tools", "alpha-greet")] }),
  deleteClient("INF-17", "inf-17-alpha"),
]);

const INF18 = folder("INF-18 No tools_to_execute exposes nothing", `Setup: ${LLM_TEXT}; alpha (echo, add, greet) registered as client alpha with tools_to_execute omitted.`, [
  ...freshServer("INF-18", "alpha", ["echo", "add", "greet"]),
  createClient("INF-18", httpClient("inf-18-alpha", "alpha", "alpha"), "setup: register client alpha without tools_to_execute"),
  llmScript("INF-18", "inf18", TEXT_OK),
  infChat("INF-18", "chat", "inf18", [A.status(200)]),
  llmCaptured("INF-18", "nothing is injected", "inf18", [reqCount(1), toolsAre(0, [])]),
  execTool("INF-18", "execute alpha-echo", "alpha-echo", [A.status(400), A.msgContains("not in client's ToolsToExecute allow-list")]),
  deleteClient("INF-18", "inf-18-alpha"),
]);

const INF19 = folder("INF-19 Instructions as a chat system message", `Setup: ${ALPHA_GREET_LLM}; alpha's instructions are "Call echo first"; the caller sends system message "S".`, [
  ...freshServer("INF-19", "alpha", ["echo", "add", "greet"]),
  setInstructions("INF-19", "alpha", "Call echo first", lit("Call echo first")),
  alphaClient("INF-19"),
  llmScript("INF-19", "inf19", TEXT_OK),
  infChat("INF-19", "chat with system S", "inf19", [A.status(200)], { messages: [{ role: "system", content: "S" }, userMsg("inf19")] }),
  llmCaptured("INF-19", "messages[0] is alpha's block, messages[1] the caller's S", "inf19", [reqCount(1), systemBlock(0, "alpha", "Call echo first"),
    A.custom("messages[1] is the caller's system \"S\"", [REQS, "pm.expect(reqs[0].json.messages[1]).to.eql({ role: 'system', content: 'S' });"])]),
  alphaGreetTeardown("INF-19"),
]);

const ALPHA_BLOCK = (text) => `<mcp_server name="alpha">\n${text}\n</mcp_server>`;
const INF20 = folder("INF-20 Instructions on Responses and Anthropic", `Setup: ${ALPHA_GREET_LLM}; alpha has instructions; a Responses request with instructions "X", and an Anthropic request on /anthropic with system "X".`, [
  ...freshServer("INF-20", "alpha", ["echo", "add", "greet"]),
  setInstructions("INF-20", "alpha", "Alpha instructions", lit("Alpha instructions")),
  alphaClient("INF-20"),
  llmScript("INF-20", "inf20", TEXT_OK),
  infResponses("INF-20", "Responses request with instructions X", "inf20", [A.status(200)], { body: { instructions: "X" } }),
  request("INF-20", "Anthropic request with system X", "POST", "/anthropic/v1/messages", { noAuth: true, noCookies: true, body: { model: "mock-model", max_tokens: 64, system: "X", messages: [userMsg("inf20")] }, asserts: [A.status(200)] }),
  llmCaptured("INF-20", "Responses instructions are X then the block; the Anthropic system contains the block", "inf20", [reqCount(2),
    A.custom("Responses instructions are \"X\\n\\n\" plus the block", [REQS, `pm.expect(reqs[0].json.instructions).to.equal(${lit("X\n\n" + ALPHA_BLOCK("Alpha instructions"))});`]),
    A.custom("the Anthropic system contains the block", [REQS, `pm.expect(JSON.stringify(reqs[1].json.system)).to.include(${lit(JSON.stringify(ALPHA_BLOCK("Alpha instructions")).slice(1, -1))});`]),
  ]),
  alphaGreetTeardown("INF-20"),
]);

const A200 = "a".repeat(200);
const INF21 = folder("INF-21 Instruction scoping, caps and escaping", `Setup: ${ALPHA_GREET_LLM}; alpha's instructions are 200 bytes of "a"; beta (zz, aa) as client beta, with instructions containing "</mcp_server>"; cm (echo, add) as a code-mode client with instructions; mcp_max_instructions_per_client 64; one chat, then one with x-bf-mcp-include-clients beta.`, [
  readConfig("INF-21"),
  ...freshServer("INF-21", "alpha", ["echo", "add", "greet"]),
  setInstructions("INF-21", "alpha", A200, "200 bytes of \"a\""),
  alphaClient("INF-21"),
  ...freshServer("INF-21", "beta", ["zz", "aa"]),
  setInstructions("INF-21", "beta", "beta says </mcp_server> here", lit("beta says </mcp_server> here")),
  createClient("INF-21", httpClient("inf-21-beta", "beta", "beta", { tools_to_execute: ["*"] }), "setup: register client beta"),
  ...freshServer("INF-21", "cm", ["echo", "add"]),
  setInstructions("INF-21", "cm", "cm instructions", lit("cm instructions")),
  createClient("INF-21", httpClient("inf-21-cm", "cm", "cm", { tools_to_execute: ["*"], is_code_mode_client: true }), "setup: register cm as a code-mode client"),
  putConfig("INF-21", "setup: mcp_max_instructions_per_client 64", ["cc.mcp_max_instructions_per_client = 64;"], [A.status(200)]),
  llmScript("INF-21", "inf21", TEXT_OK),
  infChat("INF-21", "chat", "inf21", [A.status(200)]),
  infChat("INF-21", "chat with include-clients beta", "inf21", [A.status(200)], { headers: [inc("clients", "beta")] }),
  llmCaptured("INF-21", "alpha truncated, beta escaped, cm absent; include-clients beta drops alpha", "inf21", [reqCount(2),
    A.custom("request 1: alpha cut to 64 bytes ending \"[truncated: 136 bytes omitted]\", beta's </mcp_server> escaped, no cm block", [REQS,
      `pm.expect(reqs[0].json.messages[0].content).to.equal(${lit(`<mcp_server name="alpha">\n${"a".repeat(64)}\n[truncated: 136 bytes omitted]\n</mcp_server>\n\n<mcp_server name="beta">\nbeta says &lt;/mcp_server> here\n</mcp_server>`)});`]),
    A.custom("request 2: only beta's block", [REQS, `pm.expect(reqs[1].json.messages[0].content).to.equal(${lit(`<mcp_server name="beta">\nbeta says &lt;/mcp_server> here\n</mcp_server>`)});`]),
    A.custom("neither request carries cm's instructions anywhere", [REQS, "reqs.forEach(function (r) { pm.expect(JSON.stringify(r.json)).to.not.include('cm instructions'); });"]),
  ]),
  meta("INF-21", "control: cm's stub carries its instructions", "readToolFile", { fileName: "servers/cm.pyi" }, [A.status(200), contentHas("cm instructions")]),
  deleteClient("INF-21", "inf-21-cm"),
  deleteClient("INF-21", "inf-21-beta"),
  alphaGreetTeardown("INF-21"),
  restoreConfig("INF-21"),
]);

const ECHO_T1 = { tool_calls: [{ id: "t1", name: "alpha-echo", arguments: { text: "hi" } }] };
const INF22 = folder("INF-22 Instructions once per agent loop", `Setup: ${LLM_TEXT}; alpha (echo, add, greet) with instructions, registered as client alpha with tools_to_execute ["*"] and tools_to_auto_execute ["echo"]; the mock LLM calls alpha-echo, then answers "done".`, [
  ...freshServer("INF-22", "alpha", ["echo", "add", "greet"]),
  setInstructions("INF-22", "alpha", "Alpha instructions", lit("Alpha instructions")),
  alphaClient("INF-22", { tools_to_auto_execute: ["echo"] }),
  llmScript("INF-22", "inf22", { turns: [ECHO_T1, { text: "done" }] }),
  infChat("INF-22", "chat", "inf22", [A.status(200)]),
  llmCaptured("INF-22", "turn 2 has exactly one alpha block", "inf22", [reqCount(2), A.custom("one <mcp_server name=\"alpha\"> block in turn 2's messages", [REQS,
    "var n = 0; reqs[1].json.messages.forEach(function (m) { var c = typeof m.content === 'string' ? m.content : JSON.stringify(m.content || ''); n += c.split('<mcp_server name=\"alpha\">').length - 1; });",
    "pm.expect(n, JSON.stringify(reqs[1].json.messages)).to.equal(1);",
  ])]),
  deleteClient("INF-22", "inf-22-alpha"),
]);

const INF31 = folder("INF-31 Per-client timeout", "Setup: alpha (echo, add, greet) registered as client alpha, per call, tools_to_execute [\"*\"], tool_execution_timeout 1; alpha delays every tools/call by 5s (the fault is scoped to tools/call, so the per-call handshake is fast).", [
  ...freshServer("INF-31", "alpha", ["echo", "add", "greet"]),
  alphaClient("INF-31", { tool_execution_timeout: 1 }),
  mockPost("INF-31", "alpha", "faults", "setup: alpha delays tools/call by 5s", { faults: { latency: "5s", methods: ["tools/call"] } }),
  execTool("INF-31", "execute alpha-echo", "alpha-echo", [A.status(400), A.msgStartsWith("MCP tool call timed out after 1s: alpha-echo"), responseTime(null, 2500)]),
  mockPost("INF-31", "alpha", "faults/clear", "teardown: clear alpha's faults", {}),
  deleteClient("INF-31", "inf-31-alpha"),
]);

const INF33 = folder("INF-33 Headers filter execution", `Setup: ${ALPHA_GREET_TEXT}; plus beta (zz, aa) as client beta.`, [
  ...alphaGreetSetup("INF-33"),
  ...betaSetup("INF-33"),
  execTool("INF-33", "execute alpha-echo with include-clients beta", "alpha-echo", [A.status(400), A.msgContains("(client alpha is not in request-context include list)")], { headers: [inc("clients", "beta")] }),
  execTool("INF-33", "execute alpha-echo with include-tools alpha-add", "alpha-echo", [A.status(400), A.msgContains("(filtered by request context)")], { headers: [inc("tools", "alpha-add")] }),
  betaTeardown("INF-33"),
  alphaGreetTeardown("INF-33"),
]);

const INF47 = folder("INF-47 Code-mode client injects meta tools only", `Setup: ${ALPHA_GREET_LLM}; plus cm (echo, add) as a code-mode client.`, [
  ...alphaGreetSetup("INF-47"),
  ...cmSetup("INF-47"),
  llmScript("INF-47", "inf47", TEXT_OK),
  infChat("INF-47", "chat", "inf47", [A.status(200)]),
  llmCaptured("INF-47", "the four meta tools and alpha's tools, no cm tool", "inf47", [reqCount(1), A.custom("declared tools are exactly listToolFiles, readToolFile, getToolDocs, executeToolCode and alpha-*", [REQS,
    `pm.expect(reqs[0].tools.slice().sort()).to.eql(${lit([...ALPHA3, "executeToolCode", "getToolDocs", "listToolFiles", "readToolFile"].sort())});`,
    "pm.expect(reqs[0].tools.filter(function (t) { return t.indexOf('cm-') === 0; })).to.eql([]);",
  ])]),
  cmTeardown("INF-47"),
  alphaGreetTeardown("INF-47"),
]);

// ================================================================ agent mode on the mock LLM

// Agent mode runs when a client auto-executes tools: Bifrost runs the model's calls itself and sends the results back
// to the model until it stops calling. Each test scripts the model's turns on the mock LLM and reads both what the
// caller got and every request the model saw.
const AGENT_TEXT = (auto) => `alpha (echo, add, greet) registered as client alpha, per call, tools_to_execute ["*"], tools_to_auto_execute ${lit(auto)}; ${LLM_TEXT}`;
const agentSetup = (id, auto) => [...freshServer(id, "alpha", ["echo", "add", "greet"]), alphaClient(id, { tools_to_auto_execute: auto })];
const agentTeardown = (id) => deleteClient(id, `${id.toLowerCase()}-alpha`);
const llmCall = (callId, name, args = { text: "hi" }) => ({ id: callId, name, arguments: args });
const ECHO_RESULT = (text) => `echo:${JSON.stringify({ text })}`;
// infPost sends a body to an inference route with no admin credentials.
const infPost = (id, title, path, body, asserts, { headers = [], extraTests = [], pre = [] } = {}) => request(id, title, "POST", path, { noAuth: true, noCookies: true, body, headers, asserts, extraTests, pre });
const chatBody = (key, over = {}) => ({ model: MODEL, messages: [userMsg(key)], ...over });
const finishIs = (v) => A.custom(`finish_reason is ${lit(v)}`, [`pm.expect(pm.response.json().choices[0].finish_reason, 'body: ' + pm.response.text()).to.equal(${lit(v)});`]);
const chatContent = (v) => A.custom(`content is ${lit(v)}`, [`pm.expect(pm.response.json().choices[0].message.content, 'body: ' + pm.response.text()).to.equal(${lit(v)});`]);
const toolCallNames = (names) => A.custom(names.length ? `tool_calls are ${lit(names)}` : "no tool_calls", [`pm.expect((pm.response.json().choices[0].message.tool_calls || []).map(function (c) { return c.function.name; }), 'body: ' + pm.response.text()).to.eql(${lit(names)});`]);
const resetCounters = (id, server) => mockPost(id, server, "counters/reset", `setup: reset ${server}'s counters`, {});
// toolMessages reads the tool messages of logged request i as {tool_call_id: content}.
const TOOL_MSGS = (i) => `var tm = {}; (reqs[${i}].json.messages || []).forEach(function (m) { if (m.role === 'tool') { tm[m.tool_call_id] = m.content; } });`;

// pollAsync re-reads an async job for up to about 10s until it leaves pending and processing.
const pollAsync = (path) => [
  "var left = 20;",
  "function poll() {",
  `  pm.sendRequest({ url: pm.variables.get('base_url') + '${path}', method: 'GET' }, function (err, res) {`,
  "    var st = (!err && res.code === 200) ? res.json().status : '';",
  "    if ((st && st !== 'pending' && st !== 'processing') || --left <= 0) { return; }",
  "    setTimeout(poll, 500);",
  "  });",
  "}",
  "poll();",
];
const INF08 = folder("INF-08 Async chat", `Setup: ${AGENT_TEXT(["echo"])}; turn 1 calls alpha-echo, turn 2 answers "done"; the chat is submitted to /v1/async/chat/completions.`, [
  ...agentSetup("INF-08", ["echo"]),
  llmScript("INF-08", "inf08", { turns: [{ tool_calls: [llmCall("t1", "alpha-echo")] }, { text: "done" }] }),
  infPost("INF-08", "submit the async chat", "/v1/async/chat/completions", chatBody("inf08"), [A.status(202)], { extraTests: ["pm.collectionVariables.set('inf08_job', pm.response.json().id);"] }),
  request("INF-08", "the job completes with \"done\"", "GET", "/v1/async/chat/completions/{{inf08_job}}", { noAuth: true, noCookies: true, pre: pollAsync("/v1/async/chat/completions/' + pm.collectionVariables.get('inf08_job') + '"), asserts: [A.status(200), A.custom("status \"completed\", content \"done\"", [
    "var b = pm.response.json();",
    "pm.expect(b.status, 'body: ' + pm.response.text()).to.equal('completed');",
    "pm.expect(b.result.choices[0].message.content).to.equal('done');",
  ])] }),
  mockGet("INF-08", "alpha", "alpha ran echo once", [callsIs("echo", 1)]),
  agentTeardown("INF-08"),
]);

const INF34 = folder("INF-34 Agent mode, chat", `Setup: ${AGENT_TEXT(["echo"])}; turn 1 calls alpha-echo with id t1, turn 2 answers "done".`, [
  ...agentSetup("INF-34", ["echo"]),
  llmScript("INF-34", "inf34", { turns: [{ tool_calls: [llmCall("t1", "alpha-echo")] }, { text: "done" }] }),
  infPost("INF-34", "chat", "/v1/chat/completions", chatBody("inf34"), [A.status(200), chatContent("done"), toolCallNames([])]),
  llmCaptured("INF-34", "two model requests; turn 2 has t1's echo", "inf34", [reqCount(2), A.custom("turn 2 has a tool message for t1 with the echo", [REQS, TOOL_MSGS(1), `pm.expect(tm.t1, JSON.stringify(reqs[1].json.messages)).to.equal(${lit(ECHO_RESULT("hi"))});`])]),
  mockGet("INF-34", "alpha", "alpha ran echo once", [callsIs("echo", 1)]),
  agentTeardown("INF-34"),
]);

const INF35 = folder("INF-35 Agent mode, Responses", `Setup: ${AGENT_TEXT(["echo"])}; on /v1/responses, turn 1 calls alpha-echo with id t1, turn 2 answers "done".`, [
  ...agentSetup("INF-35", ["echo"]),
  llmScript("INF-35", "inf35", { turns: [{ tool_calls: [llmCall("t1", "alpha-echo")] }, { text: "done" }] }),
  infResponses("INF-35", "Responses request", "inf35", [A.status(200), A.custom("the output is the final message and no function_call", [
    "var out = pm.response.json().output || [];",
    "pm.expect(out.map(function (o) { return o.type; }), 'body: ' + pm.response.text()).to.eql(['message']);",
    "pm.expect(out[0].content[0].text).to.equal('done');",
  ])]),
  llmCaptured("INF-35", "turn 2's input has the function_call_output for t1", "inf35", [reqCount(2), A.custom("a function_call_output with call_id t1", [REQS,
    "var outs = (reqs[1].json.input || []).filter(function (i) { return i.type === 'function_call_output'; });",
    "pm.expect(outs.map(function (o) { return o.call_id; }), JSON.stringify(reqs[1].json.input)).to.eql(['t1']);",
  ])]),
  agentTeardown("INF-35"),
]);

const INF36 = folder("INF-36 Nothing auto-executable", `Setup: ${AGENT_TEXT([])}; the model calls alpha-echo, then (in a second chat that declares the caller's own my_tool) my_tool.`, [
  ...agentSetup("INF-36", []),
  llmScript("INF-36", "inf36-mcp", { then: { tool_calls: [llmCall("t1", "alpha-echo")] } }),
  infPost("INF-36", "chat where the model calls alpha-echo", "/v1/chat/completions", chatBody("inf36-mcp"), [A.status(200), finishIs("tool_calls"), toolCallNames(["alpha-echo"])]),
  llmCaptured("INF-36", "one model request for the alpha-echo chat", "inf36-mcp", [reqCount(1)]),
  llmScript("INF-36", "inf36-own", { then: { tool_calls: [llmCall("m1", "my_tool", {})] } }),
  infPost("INF-36", "chat where the model calls my_tool", "/v1/chat/completions", chatBody("inf36-own", { tools: [MY_TOOL()] }), [A.status(200), finishIs("tool_calls"), toolCallNames(["my_tool"])]),
  llmCaptured("INF-36", "one model request for the my_tool chat", "inf36-own", [reqCount(1)]),
  mockGet("INF-36", "alpha", "alpha ran nothing", [A.custom("calls is empty", ["pm.expect(pm.response.json().counters.calls || {}, 'body: ' + pm.response.text()).to.eql({});"])]),
  agentTeardown("INF-36"),
]);

const MIXED_PREFIX = "The Output from allowed tools calls is - {\"alpha-echo\":";
const MIXED_SUFFIX = "Now I shall call these tools next...";
const MIXED_TURN = { tool_calls: [llmCall("e1", "alpha-echo"), llmCall("a1", "alpha-add", { a: 1, b: 2 })] };
const INF37 = folder("INF-37 Mixed auto and non-auto, chat", `Setup: ${AGENT_TEXT(["echo"])}; the model calls alpha-echo and alpha-add in one turn.`, [
  ...agentSetup("INF-37", ["echo"]),
  llmScript("INF-37", "inf37", { turns: [MIXED_TURN] }),
  infPost("INF-37", "chat", "/v1/chat/completions", chatBody("inf37"), [A.status(200), finishIs("stop"), toolCallNames(["alpha-add"]), A.custom("content reports the echo output and the pending call", [
    "var c = pm.response.json().choices[0].message.content;",
    `pm.expect(c.indexOf(${lit(MIXED_PREFIX)}), 'content: ' + c).to.equal(0);`,
    `pm.expect(c.slice(-${MIXED_SUFFIX.length}), 'content: ' + c).to.equal(${lit(MIXED_SUFFIX)});`,
  ])]),
  llmCaptured("INF-37", "one model request", "inf37", [reqCount(1)]),
  agentTeardown("INF-37"),
]);

const INF38 = folder("INF-38 Mixed calls, Responses", `Setup: ${AGENT_TEXT(["echo"])}; on /v1/responses the model calls alpha-echo and alpha-add (call_id a1) in one turn.`, [
  ...agentSetup("INF-38", ["echo"]),
  llmScript("INF-38", "inf38", { turns: [MIXED_TURN] }),
  infResponses("INF-38", "Responses request", "inf38", [A.status(200), A.custom("output[0] is the message, output[1] the alpha-add call a1, and no echo call", [
    "var out = pm.response.json().output || [];",
    "pm.expect(out.length, 'body: ' + pm.response.text()).to.equal(2);",
    "pm.expect(out[0].type).to.equal('message');",
    "var t = out[0].content[0].text;",
    `pm.expect(t.indexOf(${lit(MIXED_PREFIX)}), t).to.equal(0);`,
    `pm.expect(t.slice(-${MIXED_SUFFIX.length}), t).to.equal(${lit(MIXED_SUFFIX)});`,
    "pm.expect(out[1].type).to.equal('function_call');",
    "pm.expect(out[1].name).to.equal('alpha-add');",
    "pm.expect(out[1].call_id).to.equal('a1');",
  ])]),
  agentTeardown("INF-38"),
]);

const INF39 = folder("INF-39 Max depth", `Setup: ${AGENT_TEXT(["echo"])}; mcp_agent_depth 2; the model always calls alpha-echo, reporting 10 input and 5 output tokens each turn.`, [
  readConfig("INF-39"),
  ...agentSetup("INF-39", ["echo"]),
  putConfig("INF-39", "setup: mcp_agent_depth 2", ["cc.mcp_agent_depth = 2;"], [A.status(200)]),
  llmScript("INF-39", "inf39", { then: { tool_calls: [llmCall("d1", "alpha-echo")], usage: { input: 10, output: 5 } } }),
  infPost("INF-39", "chat", "/v1/chat/completions", chatBody("inf39"), [A.status(200), toolCallNames(["alpha-echo"]), A.custom("usage sums the three turns", ["var u = pm.response.json().usage;", "pm.expect(u.prompt_tokens, 'body: ' + pm.response.text()).to.equal(30);", "pm.expect(u.completion_tokens).to.equal(15);", "pm.expect(u.total_tokens).to.equal(45);"])]),
  llmCaptured("INF-39", "three model requests", "inf39", [reqCount(3)]),
  mockGet("INF-39", "alpha", "alpha ran echo twice", [callsIs("echo", 2)]),
  agentTeardown("INF-39"),
  restoreConfig("INF-39"),
]);

const INF40 = folder("INF-40 Parallel calls in one turn", `Setup: ${AGENT_TEXT(["echo"])}; turn 1 calls alpha-echo twice (p1 with "a", p2 with "b"), turn 2 answers "done".`, [
  ...agentSetup("INF-40", ["echo"]),
  llmScript("INF-40", "inf40", { turns: [{ tool_calls: [llmCall("p1", "alpha-echo", { text: "a" }), llmCall("p2", "alpha-echo", { text: "b" })] }, { text: "done" }] }),
  infPost("INF-40", "chat", "/v1/chat/completions", chatBody("inf40"), [A.status(200), chatContent("done")]),
  llmCaptured("INF-40", "turn 2 has tool messages for p1 and p2", "inf40", [reqCount(2), A.custom("tool messages p1 and p2 with their echoes", [REQS, TOOL_MSGS(1),
    `pm.expect(tm, JSON.stringify(reqs[1].json.messages)).to.eql({ p1: ${lit(ECHO_RESULT("a"))}, p2: ${lit(ECHO_RESULT("b"))} });`])]),
  mockGet("INF-40", "alpha", "alpha ran echo twice", [callsIs("echo", 2)]),
  agentTeardown("INF-40"),
]);

const TOOL_FAILED = "Error executing tool alpha-echo: tool execution failed:";
const INF41 = folder("INF-41 Tool failure inside the loop", `Setup: ${LLM_TEXT}; alpha (echo, add, greet) with echo failing every call with a JSON-RPC error (err_kind protocol), registered as client alpha, per call, tools_to_execute ["*"], tools_to_auto_execute ["echo"]; on chat and on /anthropic, turn 1 calls alpha-echo, turn 2 answers "final".`, [
  request("INF-41", "setup: fresh mcpchaos server alpha", "POST", "/control", { base: CHAOS, noAuth: true, body: { add: "alpha" }, asserts: [A.status(200)] }),
  mockPost("INF-41", "alpha", "tools/set", "setup: alpha serves echo (failing with a JSON-RPC error), add and greet", { tools: [{ name: "echo", fault: { err_rate: 1, err_kind: "protocol" } }, { name: "add" }, { name: "greet" }] }),
  alphaClient("INF-41", { tools_to_auto_execute: ["echo"] }),
  llmScript("INF-41", "inf41-chat", { turns: [{ tool_calls: [llmCall("f1", "alpha-echo")] }, { text: "final" }] }),
  infPost("INF-41", "chat", "/v1/chat/completions", chatBody("inf41-chat"), [A.status(200), chatContent("final")]),
  llmCaptured("INF-41", "turn 2's tool message reports the failure", "inf41-chat", [reqCount(2), A.custom(`the tool content starts ${lit(TOOL_FAILED)}`, [REQS, TOOL_MSGS(1), `pm.expect((tm.f1 || '').indexOf(${lit(TOOL_FAILED)}), JSON.stringify(reqs[1].json.messages)).to.equal(0);`])]),
  llmScript("INF-41", "inf41-anth", { turns: [{ tool_calls: [llmCall("f2", "alpha-echo")] }, { text: "final" }] }),
  request("INF-41", "Anthropic request", "POST", "/anthropic/v1/messages", { noAuth: true, noCookies: true, body: { model: "mock-model", max_tokens: 64, messages: [userMsg("inf41-anth")] }, asserts: [A.status(200), A.custom("the client gets the final text", ["pm.expect(pm.response.json().content, 'body: ' + pm.response.text()).to.eql([{ type: 'text', text: 'final' }]);"])] }),
  llmCaptured("INF-41", "turn 2's tool_result is marked is_error", "inf41-anth", [reqCount(2), A.custom("tool_result for f2 has is_error true", [REQS,
    "var results = []; (reqs[1].json.messages || []).forEach(function (m) { (Array.isArray(m.content) ? m.content : []).forEach(function (b) { if (b.type === 'tool_result') { results.push(b); } }); });",
    "pm.expect(results.length, JSON.stringify(reqs[1].json.messages)).to.equal(1);",
    "pm.expect(results[0].tool_use_id).to.equal('f2');",
    "pm.expect(results[0].is_error).to.equal(true);",
  ])]),
  agentTeardown("INF-41"),
]);

const INF42 = folder("INF-42 Streaming does not run the agent loop", `Setup: ${AGENT_TEXT(["echo"])}; streamed chat and streamed Responses requests; the model calls alpha-echo.`, [
  ...agentSetup("INF-42", ["echo"]),
  llmScript("INF-42", "inf42-chat", { then: { tool_calls: [llmCall("s1", "alpha-echo")] } }),
  infPost("INF-42", "streamed chat", "/v1/chat/completions", chatBody("inf42-chat", { stream: true }), [A.status(200), A.custom("the stream carries the alpha-echo call and finish_reason tool_calls", [
    "var names = [], finish = [];",
    "pm.response.text().split('\\n').forEach(function (l) { if (l.indexOf('data: {') !== 0) { return; } var d = JSON.parse(l.slice(6)); ((d.choices || [])[0] || {}).delta && (((d.choices[0].delta.tool_calls) || []).forEach(function (c) { if (c.function && c.function.name) { names.push(c.function.name); } })); if (d.choices && d.choices[0] && d.choices[0].finish_reason) { finish.push(d.choices[0].finish_reason); } });",
    "pm.expect(names, pm.response.text()).to.eql(['alpha-echo']);",
    "pm.expect(finish).to.eql(['tool_calls']);",
  ])]),
  llmCaptured("INF-42", "one model request for the streamed chat", "inf42-chat", [reqCount(1)]),
  llmScript("INF-42", "inf42-resp", { then: { tool_calls: [llmCall("s2", "alpha-echo")] } }),
  infPost("INF-42", "streamed Responses request", "/v1/responses", { model: MODEL, stream: true, input: `${marker("inf42-resp")} hi` }, [A.status(200), A.custom("the stream completes with the alpha-echo function call", [
    "var calls = [], completed = false;",
    "pm.response.text().split('\\n').forEach(function (l) { if (l.indexOf('data: {') !== 0) { return; } var d = JSON.parse(l.slice(6)); if (d.type === 'response.output_item.done' && d.item.type === 'function_call') { calls.push(d.item.name); } if (d.type === 'response.completed') { completed = true; } });",
    "pm.expect(calls, pm.response.text()).to.eql(['alpha-echo']);",
    "pm.expect(completed).to.equal(true);",
  ])]),
  llmCaptured("INF-42", "one model request for the streamed Responses request", "inf42-resp", [reqCount(1)]),
  mockGet("INF-42", "alpha", "alpha ran nothing", [A.custom("calls is empty", ["pm.expect(pm.response.json().counters.calls || {}, 'body: ' + pm.response.text()).to.eql({});"])]),
  agentTeardown("INF-42"),
]);

const INF43 = folder("INF-43 Continuation keeps text, usage and route", `Setup: ${AGENT_TEXT(["echo"])}; the chat names model openai/inf43-a with fallback anthropic/inf43-b; the first model request fails with 500, so the fallback serves turn 1 ("Checking" plus a call to alpha-echo, 10 and 5 tokens) and turn 2 ("Final", 20 and 7 tokens).`, [
  ...agentSetup("INF-43", ["echo"]),
  llmScript("INF-43", "inf43", { turns: [{ error: { status: 500, message: "primary down" } }, { text: "Checking", tool_calls: [llmCall("k1", "alpha-echo")], usage: { input: 10, output: 5 } }, { text: "Final", usage: { input: 20, output: 7 } }] }),
  infPost("INF-43", "chat with a fallback", "/v1/chat/completions", { model: "openai/inf43-a", fallbacks: ["anthropic/inf43-b"], messages: [userMsg("inf43")] }, [A.status(200), chatContent("Checking\n\nFinal"), A.custom("usage is the sum of the two served turns", ["var u = pm.response.json().usage;", "pm.expect(u.prompt_tokens, 'body: ' + pm.response.text()).to.equal(30);", "pm.expect(u.completion_tokens).to.equal(12);", "pm.expect(u.total_tokens).to.equal(42);"])]),
  llmCaptured("INF-43", "turn 2 goes to turn 1's provider and model", "inf43", [reqCount(3), A.custom("openai inf43-a (500), then anthropic inf43-b twice", [REQS, "pm.expect(reqs.map(function (r) { return r.format + ':' + r.model + ':' + r.status; })).to.eql(['openai.chat:inf43-a:500', 'anthropic.messages:inf43-b:200', 'anthropic.messages:inf43-b:200']);"])]),
  agentTeardown("INF-43"),
]);

const INF44 = folder("INF-44 Agent on Anthropic and GenAI", `Setup: ${AGENT_TEXT(["echo"])}; on /anthropic and on /genai (bare model mock-model), turn 1 calls alpha-echo, turn 2 answers "done".`, [
  ...agentSetup("INF-44", ["echo"]),
  llmScript("INF-44", "inf44-anth", { turns: [{ tool_calls: [llmCall("x1", "alpha-echo")] }, { text: "done" }] }),
  request("INF-44", "Anthropic request", "POST", "/anthropic/v1/messages", { noAuth: true, noCookies: true, body: { model: "mock-model", max_tokens: 64, messages: [userMsg("inf44-anth")] }, asserts: [A.status(200), A.custom("the client gets only the final text", ["pm.expect(pm.response.json().content, 'body: ' + pm.response.text()).to.eql([{ type: 'text', text: 'done' }]);"])] }),
  llmCaptured("INF-44", "two model requests on Anthropic", "inf44-anth", [reqCount(2)]),
  mockGet("INF-44", "alpha", "alpha ran echo once for Anthropic", [callsIs("echo", 1)]),
  resetCounters("INF-44", "alpha"),
  llmScript("INF-44", "inf44-gen", { turns: [{ tool_calls: [llmCall("x2", "alpha-echo")] }, { text: "done" }] }),
  request("INF-44", "generateContent", "POST", "/genai/v1beta/models/mock-model:generateContent", { noAuth: true, noCookies: true, body: GENAI_BODY("inf44-gen"), asserts: [A.status(200), A.custom("the client gets only the final text", ["pm.expect(pm.response.json().candidates[0].content.parts, 'body: ' + pm.response.text()).to.eql([{ text: 'done' }]);"])] }),
  llmCaptured("INF-44", "two model requests on GenAI", "inf44-gen", [reqCount(2)]),
  mockGet("INF-44", "alpha", "alpha ran echo once for GenAI", [callsIs("echo", 1)]),
  agentTeardown("INF-44"),
]);

const INF45 = folder("INF-45 Per-user auth stops the loop", `Setup: ${LLM_TEXT}; fleet server u (echo) that accepts only X-Api-Key key-a; a per_user_headers client u on it with per_user_header_keys ["x-api-key"], sample key-a, tools_to_execute ["*"], tools_to_auto_execute ["*"]; the caller (x-bf-mcp-session-id inf45-s) has submitted no headers; the model calls u-echo.`, [
  ...freshServer("INF-45", "u", ["echo"]),
  mockAuth("INF-45", "u", "setup: u accepts only X-Api-Key key-a", { identity_header: "X-Api-Key", require_headers: { "X-Api-Key": "key-a" } }),
  createClient("INF-45", { client_id: "inf-45-u", name: "u", connection_type: "http", connection_string: `${CHAOS}/s/u/mcp`, auth_type: "per_user_headers", per_user_header_keys: ["x-api-key"], user_headers: { "x-api-key": "key-a" }, tools_to_execute: ["*"], tools_to_auto_execute: ["*"] }, "setup: create the per_user_headers client u"),
  llmScript("INF-45", "inf45", { then: { tool_calls: [llmCall("u1", "u-echo")] } }),
  infPost("INF-45", "chat", "/v1/chat/completions", chatBody("inf45"), [A.status(401), A.custom("error.type mcp_auth_required naming client u", [
    "var b = pm.response.json();",
    "pm.expect(b.error && b.error.type, 'body: ' + pm.response.text()).to.equal('mcp_auth_required');",
    "pm.expect(b.extra_fields.mcp_auth_required.mcp_client_name).to.equal('u');",
  ])], { headers: [{ key: "x-bf-mcp-session-id", value: "inf45-s" }] }),
  deleteClient("INF-45", "inf-45-u"),
]);

// agentLogs reads the MCP log entries of model request R once count of them have landed.
const agentLogs = (id, title, R, count, asserts) => request(id, title, "GET", `/api/mcp-logs?llm_request_ids=${R}`, { pre: pollLogCount(`llm_request_ids=${R}`, count), asserts: [A.status(200), ...asserts] });
const LOG_LIST = "var logs = pm.response.json().logs || [];";
const INF46 = folder("INF-46 Log entry for an agent call", `Setup: as INF-34 (${AGENT_TEXT(["echo"])}; turn 1 calls alpha-echo, turn 2 answers "done"), sent with x-request-id inf46-r.`, [
  ...agentSetup("INF-46", ["echo"]),
  llmScript("INF-46", "inf46", { turns: [{ tool_calls: [llmCall("t1", "alpha-echo")] }, { text: "done" }] }),
  infPost("INF-46", "chat with x-request-id inf46-r", "/v1/chat/completions", chatBody("inf46"), [A.status(200)], { headers: [{ key: "x-request-id", value: "inf46-r" }] }),
  agentLogs("INF-46", "the call's log entry", "inf46-r", 1, [A.custom("one entry: echo on alpha, success, llm_request_id inf46-r", [LOG_LIST,
    "pm.expect(logs.length, 'body: ' + pm.response.text()).to.equal(1);",
    "pm.expect(logs[0].tool_name).to.equal('echo');",
    "pm.expect(logs[0].server_label).to.equal('alpha');",
    "pm.expect(logs[0].status).to.equal('success');",
    "pm.expect(logs[0].llm_request_id).to.equal('inf46-r');",
  ])]),
  agentTeardown("INF-46"),
]);

const PARALLEL_TURN = { tool_calls: [llmCall("e1", "alpha-echo"), llmCall("a1", "alpha-add", { a: 1, b: 2 })] };
const parallelEntries = (R) => A.custom(`2 entries, each llm_request_id ${R}, with distinct ids that are not ${R}`, [LOG_LIST,
  "pm.expect(pm.response.json().pagination.total_count, 'body: ' + pm.response.text()).to.equal(2);",
  `logs.forEach(function (r) { pm.expect(r.llm_request_id).to.equal(${lit(R)}); pm.expect(r.id).to.not.equal(${lit(R)}); });`,
  "pm.expect(logs[0].id).to.not.equal(logs[1].id);",
]);
const LOG06 = folder("LOG-06 Agent mode on chat: one log entry per tool call", `Setup: ${AGENT_TEXT(["*"])}; the model calls alpha-echo and alpha-add in parallel, then answers "done"; the chat is sent with x-request-id log06-r.`, [
  ...agentSetup("LOG-06", ["*"]),
  llmScript("LOG-06", "log06", { turns: [PARALLEL_TURN, { text: "done" }] }),
  infPost("LOG-06", "chat with x-request-id log06-r", "/v1/chat/completions", chatBody("log06"), [A.status(200)], { headers: [{ key: "x-request-id", value: "log06-r" }] }),
  agentLogs("LOG-06", "the log entries for log06-r", "log06-r", 2, [parallelEntries("log06-r")]),
  agentTeardown("LOG-06"),
]);

const LOG07 = folder("LOG-07 Agent mode on Responses", `Setup: as LOG-06 (${AGENT_TEXT(["*"])}; parallel alpha-echo and alpha-add, then "done") on /v1/responses with x-request-id log07-r.`, [
  ...agentSetup("LOG-07", ["*"]),
  llmScript("LOG-07", "log07", { turns: [PARALLEL_TURN, { text: "done" }] }),
  infPost("LOG-07", "Responses request with x-request-id log07-r", "/v1/responses", { model: MODEL, input: `${marker("log07")} hi` }, [A.status(200)], { headers: [{ key: "x-request-id", value: "log07-r" }] }),
  agentLogs("LOG-07", "the log entries for log07-r", "log07-r", 2, [parallelEntries("log07-r")]),
  agentTeardown("LOG-07"),
]);

const LOG08 = folder("LOG-08 Agent mode over two turns", `Setup: ${AGENT_TEXT(["*"])}; the model calls alpha-echo with "one", then alpha-echo with "two", then answers "done"; the chat is sent with x-request-id log08-r.`, [
  ...agentSetup("LOG-08", ["*"]),
  llmScript("LOG-08", "log08", { turns: [{ tool_calls: [llmCall("e1", "alpha-echo", { text: "one" })] }, { tool_calls: [llmCall("e2", "alpha-echo", { text: "two" })] }, { text: "done" }] }),
  infPost("LOG-08", "chat with x-request-id log08-r", "/v1/chat/completions", chatBody("log08"), [A.status(200)], { headers: [{ key: "x-request-id", value: "log08-r" }] }),
  agentLogs("LOG-08", "the log entries for log08-r", "log08-r", 2, [A.custom("both entries carry llm_request_id log08-r; the second turn's entry has a new request_id", [LOG_LIST,
    "pm.expect(logs.length, 'body: ' + pm.response.text()).to.equal(2);",
    "logs.forEach(function (r) { pm.expect(r.llm_request_id).to.equal('log08-r'); });",
    "var second = logs.filter(function (r) { return JSON.stringify(r.arguments).indexOf('two') >= 0; });",
    "pm.expect(second.length, 'logs: ' + pm.response.text()).to.equal(1);",
    "pm.expect(second[0].request_id).to.be.a('string').and.not.equal('log08-r');",
    "pm.expect(second[0].request_id).to.not.equal('');",
  ])]),
  agentTeardown("LOG-08"),
]);

const LOG10 = folder("LOG-10 Code mode inside agent mode", `Setup: ${AGENT_TEXT(["*"])}, alpha a code-mode client; the model calls executeToolCode, which calls alpha.echo twice, then answers "done".`, [
  ...freshServer("LOG-10", "alpha", ["echo", "add", "greet"]),
  alphaClient("LOG-10", { tools_to_auto_execute: ["*"], is_code_mode_client: true }),
  llmScript("LOG-10", "log10", { turns: [{ tool_calls: [llmCall("c1", "executeToolCode", { code: "a = alpha.echo(text=\"log10-a\")\nb = alpha.echo(text=\"log10-b\")\nresult = [a, b]" })] }, { text: "done" }] }),
  infPost("LOG-10", "chat", "/v1/chat/completions", chatBody("log10"), [A.status(200), chatContent("done")]),
  mockCalls("LOG-10", "alpha", "echo", 2),
  request("LOG-10", "two echo log entries with distinct ids", "GET", "/api/mcp-logs?tool_names=echo&content_search=log10-", { pre: pollLogCount("tool_names=echo&content_search=log10-", 2), asserts: [A.status(200), A.custom("total_count is 2 and the ids differ", [
    "var b = pm.response.json();",
    "pm.expect(b.pagination.total_count, 'body: ' + pm.response.text()).to.equal(2);",
    "pm.expect(b.logs[0].id).to.not.equal(b.logs[1].id);",
  ])] }),
  agentTeardown("LOG-10"),
]);

// ================================================================ provider-injected tools on the mock LLM

// A provider's injected_tools expose one MCP tool to the model under a name of the provider's choosing, and Bifrost
// runs the model's calls to it itself. Each test sets openai's injected_tools by reading the provider and writing it
// back with them, and clears them the same way at the end.
const WEB_SEARCH = [{ name: "web_search", mcp_client_name: "alpha", tool_name: "search" }];
const INJ_TEXT = `${LLM_TEXT}; alpha (search, echo) registered as client alpha, per call, tools_to_execute ["*"]; openai's injected_tools expose alpha's search as web_search`;
const readProvider = (id) => request(id, "setup: read the openai provider", "GET", "/api/providers/openai", { asserts: [A.status(200)], extraTests: ["var p = pm.response.json(); pm.collectionVariables.set('openai_provider', JSON.stringify({ network_config: p.network_config, concurrency_and_buffer_size: p.concurrency_and_buffer_size }));"] });
// putInjected writes the provider back with injected_tools set to tools.
const putInjected = (id, title, tools) => request(id, title, "PUT", "/api/providers/openai", {
  raw: "{{provider_body}}", asserts: [A.status(200)],
  pre: ["var p = JSON.parse(pm.collectionVariables.get('openai_provider'));", `p.injected_tools = ${lit(tools)};`, "pm.variables.set('provider_body', JSON.stringify(p));"],
});
const injectedSetup = (id, tools = ["search", "echo"], over = {}) => [
  readProvider(id),
  ...freshServer(id, "alpha", tools),
  alphaClient(id, over),
  putInjected(id, "setup: openai injects alpha's search as web_search", WEB_SEARCH),
];
const injectedTeardown = (id) => [putInjected(id, "teardown: clear openai's injected_tools", []), agentTeardown(id)];
const SEARCH_CALL = { tool_calls: [llmCall("w1", "web_search", { text: "q" })] };
const SEARCH_RESULT = `search:${JSON.stringify({ text: "q" })}`;

const INF60 = folder("INF-60 Provider-injected tool, chat loop", `Setup: ${INJ_TEXT}; turn 1 calls web_search, turn 2 answers "answer".`, [
  ...injectedSetup("INF-60"),
  llmScript("INF-60", "inf60", { turns: [SEARCH_CALL, { text: "answer" }] }),
  infPost("INF-60", "chat", "/v1/chat/completions", chatBody("inf60", {}), [A.status(200), chatContent("answer"), toolCallNames([]), A.custom("the client never sees web_search", ["pm.expect(pm.response.text()).to.not.include('web_search');"])]),
  llmCaptured("INF-60", "turn 1 has function web_search and not alpha-search; turn 2 has the result", "inf60", [reqCount(2),
    A.custom("turn 1 declares function web_search and no alpha-search", [REQS,
      "var t = reqs[0].json.tools.filter(function (x) { return x.function && x.function.name === 'web_search'; });",
      "pm.expect(t.length, JSON.stringify(reqs[0].json.tools)).to.equal(1);",
      "pm.expect(t[0].type).to.equal('function');",
      "pm.expect(reqs[0].tools).to.not.include('alpha-search');",
    ]),
    A.custom("turn 2 has the search result for w1", [REQS, TOOL_MSGS(1), `pm.expect(tm.w1, JSON.stringify(reqs[1].json.messages)).to.equal(${lit(SEARCH_RESULT)});`]),
  ]),
  mockGet("INF-60", "alpha", "alpha ran search once", [callsIs("search", 1)]),
  ...injectedTeardown("INF-60"),
]);

const INF61 = folder("INF-61 Injected loop depth cap", `Setup: ${INJ_TEXT}; mcp_agent_depth 2; the model always calls web_search.`, [
  readConfig("INF-61"),
  ...injectedSetup("INF-61"),
  putConfig("INF-61", "setup: mcp_agent_depth 2", ["cc.mcp_agent_depth = 2;"], [A.status(200)]),
  llmScript("INF-61", "inf61", { then: SEARCH_CALL }),
  infPost("INF-61", "chat", "/v1/chat/completions", chatBody("inf61"), [A.status(200), finishIs("stop"), toolCallNames([])]),
  llmCaptured("INF-61", "two model requests", "inf61", [reqCount(2)]),
  mockGet("INF-61", "alpha", "alpha ran search once", [callsIs("search", 1)]),
  ...injectedTeardown("INF-61"),
  restoreConfig("INF-61"),
]);

const INF62 = folder("INF-62 Injected tool failure goes to the model", `Setup: ${INJ_TEXT}; alpha's search fails every call with a JSON-RPC error (err_kind protocol); turn 1 calls web_search, turn 2 answers "answer".`, [
  readProvider("INF-62"),
  request("INF-62", "setup: fresh mcpchaos server alpha", "POST", "/control", { base: CHAOS, noAuth: true, body: { add: "alpha" }, asserts: [A.status(200)] }),
  mockPost("INF-62", "alpha", "tools/set", "setup: alpha serves search (failing with a JSON-RPC error) and echo", { tools: [{ name: "search", fault: { err_rate: 1, err_kind: "protocol" } }, { name: "echo" }] }),
  alphaClient("INF-62"),
  putInjected("INF-62", "setup: openai injects alpha's search as web_search", WEB_SEARCH),
  llmScript("INF-62", "inf62", { turns: [SEARCH_CALL, { text: "answer" }] }),
  infPost("INF-62", "chat", "/v1/chat/completions", chatBody("inf62"), [A.status(200), chatContent("answer")]),
  llmCaptured("INF-62", "turn 2's tool content reports the failure", "inf62", [reqCount(2), A.custom("the tool content contains \"MCP tool call failed\"", [REQS, TOOL_MSGS(1), "pm.expect(tm.w1 || '', JSON.stringify(reqs[1].json.messages)).to.include('MCP tool call failed');"])]),
  ...injectedTeardown("INF-62"),
]);

const INF63 = folder("INF-63 Injected tool fails open", `Setup: ${INJ_TEXT}; alpha is then disabled; a Responses request declares the native web_search tool.`, [
  ...injectedSetup("INF-63"),
  llmScript("INF-63", "inf63-on", TEXT_OK),
  infResponses("INF-63", "control: with alpha enabled, a Responses request with native web_search", "inf63-on", [A.status(200)], { body: { tools: [{ type: "web_search" }] } }),
  llmCaptured("INF-63", "control: the injected function replaces the native tool", "inf63-on", [reqCount(1), toolsInclude(0, "web_search")]),
  request("INF-63", "setup: disable alpha", "PUT", "/api/mcp/client/inf-63-alpha", { body: { disabled: true }, asserts: [A.status(200)] }),
  llmScript("INF-63", "inf63", TEXT_OK),
  infResponses("INF-63", "Responses request with native web_search", "inf63", [A.status(200)], { body: { tools: [{ type: "web_search" }] } }),
  llmCaptured("INF-63", "no function web_search; the native web_search is kept", "inf63", [reqCount(1), toolsAre(0, ["type:web_search"])]),
  ...injectedTeardown("INF-63"),
]);

const INF64 = folder("INF-64 Injected tools on streams", `Setup: ${INJ_TEXT}; a streamed chat with stream_options.include_usage; turn 1 calls web_search (10 and 5 tokens), turn 2 answers "answer" (20 and 7 tokens).`, [
  ...injectedSetup("INF-64"),
  llmScript("INF-64", "inf64", { turns: [{ ...SEARCH_CALL, usage: { input: 10, output: 5 } }, { text: "answer", usage: { input: 20, output: 7 } }] }),
  infPost("INF-64", "streamed chat", "/v1/chat/completions", chatBody("inf64", { stream: true, stream_options: { include_usage: true }, ...{} }), [A.status(200), A.custom("one stream: no web_search delta, one terminal chunk, usage for both turns", [
    "var text = pm.response.text();",
    "pm.expect(text, 'stream').to.not.include('web_search');",
    "var finals = 0, usages = [];",
    "text.split('\\n').forEach(function (l) { if (l.indexOf('data: {') !== 0) { return; } var d = JSON.parse(l.slice(6)); if (d.choices && d.choices[0] && d.choices[0].finish_reason) { finals++; } if (d.usage) { usages.push(d.usage); } });",
    "pm.expect(finals, text).to.equal(1);",
    "pm.expect(usages.length, text).to.equal(1);",
    "pm.expect(usages[0].prompt_tokens).to.equal(30);",
    "pm.expect(usages[0].completion_tokens).to.equal(12);",
    "pm.expect(usages[0].total_tokens).to.equal(42);",
  ])]),
  mockGet("INF-64", "alpha", "alpha ran search once", [callsIs("search", 1)]),
  ...injectedTeardown("INF-64"),
]);

const INF65 = folder("INF-65 Injected tool and client allow-lists", `Setup: ${LLM_TEXT}; alpha (search, echo) registered as client alpha, per call, tools_to_execute []; beta (zz) as client beta; openai's injected_tools expose alpha's search as web_search; the chat sends x-bf-mcp-include-clients beta; turn 1 calls web_search, turn 2 answers "answer".`, [
  ...injectedSetup("INF-65", ["search", "echo"], { tools_to_execute: [] }),
  ...freshServer("INF-65", "beta", ["zz"]),
  createClient("INF-65", httpClient("inf-65-beta", "beta", "beta", { tools_to_execute: ["*"] }), "setup: register client beta"),
  llmScript("INF-65", "inf65", { turns: [SEARCH_CALL, { text: "answer" }] }),
  infPost("INF-65", "chat with include-clients beta", "/v1/chat/completions", chatBody("inf65"), [A.status(200), chatContent("answer")], { headers: [inc("clients", "beta")] }),
  mockGet("INF-65", "alpha", "the chat still ran search", [callsIs("search", 1)]),
  execTool("INF-65", "execute alpha-search directly", "alpha-search", [A.status(400), A.msgContains("not in client's ToolsToExecute allow-list")]),
  deleteClient("INF-65", "inf-65-beta"),
  ...injectedTeardown("INF-65"),
]);

const ONLY = process.env.TESTS ? new Set(process.env.TESTS.split(",").map((r) => r.trim())) : null;
const pick = (tests) => (ONLY ? tests.filter((t) => ONLY.has(t.name.split(" ")[0])) : tests);

const section = (name, description, tests) => folder(name, description, pick(tests));

// ================================================================ virtual key grants on model requests, on the mock LLM

// Each test's virtual key may use the openai provider with any model and key, so its chats reach the mock LLM; what
// its MCP grant allows is then read from the tools the model was sent, from execute, and from /mcp.
const OPENAI_GRANT = [{ provider: "openai", allowed_models: ["*"], key_ids: ["*"], weight: 1 }];
const govVK = (id, mcpConfigs, over = {}) => vkCreate(id, `setup: create the virtual key "${id} key"${mcpConfigs ? `, mcp_configs ${lit(mcpConfigs)}` : ", no mcp_configs"}`, { name: `${id} key`, provider_configs: OPENAI_GRANT, ...(mcpConfigs ? { mcp_configs: mcpConfigs } : {}), ...over }, `${id.toLowerCase().replace("-", "")}_vk`);
const vkVarOf = (id) => `${id.toLowerCase().replace("-", "")}_vk`;
const withVK = (id) => [{ key: "x-bf-vk", value: `{{${vkVarOf(id)}}}` }];
const govTeardownVK = (id) => vkDelete(id, "teardown: delete the virtual key", vkVarOf(id));
const GOV_TEXT = (grant) => `${LLM_TEXT}; alpha (echo, add, greet) registered as client alpha, per call, tools_to_execute ["*"]; a virtual key allowed openai with any model and key, ${grant}`;
const govChat = (id, key, asserts, headers = []) => infPost(id, `chat with the virtual key${headers.length ? " and " + headers.map((h) => `${h.key} ${h.value}`).join(", ") : ""}`, "/v1/chat/completions", chatBody(key), asserts, { headers: [...withVK(id), ...headers] });
const govExec = (id, tool, asserts) => execTool(id, `execute ${tool} with the virtual key`, tool, asserts, { headers: withVK(id) });
const blocked = (id, tool) => [A.status(403), A.custom("type mcp_tool_blocked", [`pm.expect(pm.response.json().type, 'body: ' + pm.response.text()).to.equal('mcp_tool_blocked');`]), A.msgEquals(`MCP tool '${tool}' is not allowed for virtual key '${id} key'`)];
const govList = (id, title, names) => rpc(id, title, listMsg, { headers: [MCP_ACCEPT, ...withVK(id)], asserts: [A.status(200), toolNamesInList(names)] });

const GOV01 = folder("GOV-01 VK limited to one tool", `Setup: ${GOV_TEXT("mcp_configs alpha [\"echo\"]")}.`, [
  ...freshServer("GOV-01", "alpha", ["echo", "add", "greet"]),
  alphaClient("GOV-01"),
  govVK("GOV-01", [{ mcp_client_name: "alpha", tools_to_execute: ["echo"] }]),
  llmScript("GOV-01", "gov01", TEXT_OK),
  govChat("GOV-01", "gov01", [A.status(200)]),
  llmCaptured("GOV-01", "only alpha-echo is injected", "gov01", [reqCount(1), toolsAre(0, ["alpha-echo"])]),
  govExec("GOV-01", "alpha-echo", [A.status(200)]),
  execTool("GOV-01", "execute alpha-add with the virtual key and x-request-id gov01-add", "alpha-add", blocked("GOV-01", "alpha-add"), { headers: [...withVK("GOV-01"), { key: "x-request-id", value: "gov01-add" }] }),
  request("GOV-01", "the blocked call's log entry", "GET", "/api/mcp-logs/gov01-add", {
    pre: ["var left = 30;", "function poll() { pm.sendRequest({ url: pm.variables.get('base_url') + '/api/mcp-logs/gov01-add', method: 'GET', header: { Authorization: pm.variables.get('admin_auth_header') } }, function (err, res) { if ((!err && res.code === 200 && res.json().status !== 'processing') || --left <= 0) { return; } setTimeout(poll, 500); }); }", "poll();"],
    asserts: [A.status(200), A.custom("status \"error\", tool add", ["var b = pm.response.json();", "pm.expect(b.status, 'body: ' + pm.response.text()).to.equal('error');", "pm.expect(b.tool_name).to.equal('add');"])],
  }),
  request("GOV-01", "teardown: delete the log entry", "DELETE", "/api/mcp-logs", { body: { ids: ["gov01-add"] }, asserts: [A.status(200)] }),
  govTeardownVK("GOV-01"),
  deleteClient("GOV-01", "gov-01-alpha"),
]);

const GOV02 = folder("GOV-02 VK allow-all for one client", `Setup: ${GOV_TEXT("mcp_configs alpha [\"*\"]")}; beta (zz, aa) as client beta, not allow-by-default.`, [
  ...freshServer("GOV-02", "alpha", ["echo", "add", "greet"]),
  alphaClient("GOV-02"),
  ...freshServer("GOV-02", "beta", ["zz", "aa"]),
  createClient("GOV-02", httpClient("gov-02-beta", "beta", "beta", { tools_to_execute: ["*"] }), "setup: register client beta"),
  getClient("GOV-02", "gov-02-beta", "control: beta's tools aa and zz were discovered", [toolNamesAre(["aa", "zz"])]),
  govVK("GOV-02", [{ mcp_client_name: "alpha", tools_to_execute: ["*"] }]),
  llmScript("GOV-02", "gov02", TEXT_OK),
  govChat("GOV-02", "gov02", [A.status(200)]),
  llmCaptured("GOV-02", "all alpha tools are injected and beta's are not", "gov02", [reqCount(1), toolsAre(0, ALPHA3)]),
  ...["alpha-add", "alpha-echo", "alpha-greet"].map((t) => govExec("GOV-02", t, [A.status(200)])),
  govExec("GOV-02", "beta-aa", blocked("GOV-02", "beta-aa")),
  govTeardownVK("GOV-02"),
  deleteClient("GOV-02", "gov-02-beta"),
  deleteClient("GOV-02", "gov-02-alpha"),
]);

const GOV03 = folder("GOV-03 VK with no MCP grant", `Setup: ${GOV_TEXT("no mcp_configs")}; alpha has instructions.`, [
  ...freshServer("GOV-03", "alpha", ["echo", "add", "greet"]),
  setInstructions("GOV-03", "alpha", "Alpha instructions", lit("Alpha instructions")),
  alphaClient("GOV-03"),
  govVK("GOV-03", null),
  llmScript("GOV-03", "gov03", TEXT_OK),
  govChat("GOV-03", "gov03", [A.status(200)]),
  llmCaptured("GOV-03", "no MCP tool and no instructions", "gov03", [reqCount(1), toolsAre(0, []), noInstructions(0)]),
  llmScript("GOV-03", "gov03-open", TEXT_OK),
  infPost("GOV-03", "control: chat with no virtual key", "/v1/chat/completions", chatBody("gov03-open"), [A.status(200)]),
  llmCaptured("GOV-03", "control: without a VK, alpha's tools and instructions reach the model", "gov03-open", [reqCount(1), toolsAre(0, ALPHA3), systemBlock(0, "alpha", "Alpha instructions")]),
  govExec("GOV-03", "alpha-echo", blocked("GOV-03", "alpha-echo")),
  govTeardownVK("GOV-03"),
  deleteClient("GOV-03", "gov-03-alpha"),
]);

const GOV04 = folder("GOV-04 An explicit empty grant denies", `Setup: ${GOV_TEXT("mcp_configs alpha []")}; client alpha has allow_by_default true.`, [
  ...freshServer("GOV-04", "alpha", ["echo", "add", "greet"]),
  alphaClient("GOV-04", { allow_by_default: true }),
  govVK("GOV-04", [{ mcp_client_name: "alpha", tools_to_execute: [] }]),
  llmScript("GOV-04", "gov04", TEXT_OK),
  govChat("GOV-04", "gov04", [A.status(200)]),
  llmCaptured("GOV-04", "alpha is absent", "gov04", [reqCount(1), toolsAre(0, [])]),
  govExec("GOV-04", "alpha-echo", blocked("GOV-04", "alpha-echo")),
  vkCreate("GOV-04", "control: create a second virtual key with no mcp_configs", { name: "GOV-04 open key", provider_configs: OPENAI_GRANT }, "gov04_open_vk"),
  execTool("GOV-04", "control: execute alpha-echo with the second virtual key", "alpha-echo", [A.status(200)], { headers: [{ key: "x-bf-vk", value: "{{gov04_open_vk}}" }] }),
  vkDelete("GOV-04", "teardown: delete the second virtual key", "gov04_open_vk"),
  govTeardownVK("GOV-04"),
  deleteClient("GOV-04", "gov-04-alpha"),
]);

const GOV05 = folder("GOV-05 allow_by_default toggles live", `Setup: ${GOV_TEXT("no mcp_configs")}; client alpha has allow_by_default true, then false, with no edit to the virtual key in between.`, [
  ...freshServer("GOV-05", "alpha", ["echo", "add", "greet"]),
  alphaClient("GOV-05", { allow_by_default: true }),
  govVK("GOV-05", null),
  llmScript("GOV-05", "gov05", TEXT_OK),
  govChat("GOV-05", "gov05", [A.status(200)]),
  govList("GOV-05", "/mcp tools/list with the virtual key, allow_by_default true", ALPHA3),
  govExec("GOV-05", "alpha-echo", [A.status(200)]),
  request("GOV-05", "setup: set allow_by_default false on client alpha", "PUT", "/api/mcp/client/gov-05-alpha", { body: { allow_by_default: false }, asserts: [A.status(200)] }),
  govChat("GOV-05", "gov05", [A.status(200)]),
  llmCaptured("GOV-05", "injected while true, absent once false", "gov05", [reqCount(2), toolsAre(0, ALPHA3), toolsAre(1, [])]),
  govList("GOV-05", "/mcp tools/list with the virtual key, allow_by_default false", []),
  govExec("GOV-05", "alpha-echo", blocked("GOV-05", "alpha-echo")),
  govTeardownVK("GOV-05"),
  deleteClient("GOV-05", "gov-05-alpha"),
]);

const GOV07 = folder("GOV-07 No VK means no restriction", `Setup: ${ALPHA_GREET_LLM}; enforce_auth_on_inference off; the caller sends no virtual key.`, [
  readConfig("GOV-07"),
  putConfig("GOV-07", "setup: enforce_auth_on_inference off", ["cc.enforce_auth_on_inference = false;"], [A.status(200)]),
  ...alphaGreetSetup("GOV-07"),
  llmScript("GOV-07", "gov07", TEXT_OK),
  infPost("GOV-07", "chat with no virtual key", "/v1/chat/completions", chatBody("gov07"), [A.status(200)]),
  llmCaptured("GOV-07", "all alpha tools are injected", "gov07", [reqCount(1), toolsAre(0, ALPHA3)]),
  execTool("GOV-07", "execute alpha-add with no virtual key", "alpha-add", [A.status(200)]),
  alphaGreetTeardown("GOV-07"),
  restoreConfig("GOV-07"),
]);

const GOV08 = folder("GOV-08 Headers only narrow a VK grant", `Setup: ${GOV_TEXT("mcp_configs alpha [\"echo\"]")}; beta (zz, aa) as client beta.`, [
  ...freshServer("GOV-08", "alpha", ["echo", "add", "greet"]),
  alphaClient("GOV-08"),
  ...freshServer("GOV-08", "beta", ["zz", "aa"]),
  createClient("GOV-08", httpClient("gov-08-beta", "beta", "beta", { tools_to_execute: ["*"] }), "setup: register client beta"),
  getClient("GOV-08", "gov-08-beta", "control: beta's tools aa and zz were discovered", [toolNamesAre(["aa", "zz"])]),
  govVK("GOV-08", [{ mcp_client_name: "alpha", tools_to_execute: ["echo"] }]),
  llmScript("GOV-08", "gov08", TEXT_OK),
  govChat("GOV-08", "gov08", [A.status(200)], [inc("tools", "alpha-*")]),
  govChat("GOV-08", "gov08", [A.status(200)], [inc("tools", "alpha-add")]),
  govChat("GOV-08", "gov08", [A.status(200)], [inc("clients", "alpha,beta")]),
  llmCaptured("GOV-08", "alpha-echo only; nothing; alpha-echo only", "gov08", [reqCount(3), toolsAre(0, ["alpha-echo"]), toolsAre(1, []), toolsAre(2, ["alpha-echo"])]),
  govTeardownVK("GOV-08"),
  deleteClient("GOV-08", "gov-08-beta"),
  deleteClient("GOV-08", "gov-08-alpha"),
]);

const GOV12 = folder("GOV-12 Rename carries grants", `Setup: ${GOV_TEXT("mcp_configs alpha [\"echo\"]")}; client alpha is then renamed alpha2.`, [
  ...freshServer("GOV-12", "alpha", ["echo", "add", "greet"]),
  alphaClient("GOV-12"),
  govVK("GOV-12", [{ mcp_client_name: "alpha", tools_to_execute: ["echo"] }]),
  request("GOV-12", "setup: rename client alpha to alpha2", "PUT", "/api/mcp/client/gov-12-alpha", { body: { name: "alpha2" }, asserts: [A.status(200)] }),
  llmScript("GOV-12", "gov12", TEXT_OK),
  govChat("GOV-12", "gov12", [A.status(200)]),
  llmCaptured("GOV-12", "alpha2-echo is injected", "gov12", [reqCount(1), toolsAre(0, ["alpha2-echo"])]),
  govExec("GOV-12", "alpha2-echo", [A.status(200)]),
  govExec("GOV-12", "alpha-echo", [A.status(403)]),
  govTeardownVK("GOV-12"),
  deleteClient("GOV-12", "gov-12-alpha"),
]);

const GOV15 = folder("GOV-15 Expired VK", `Setup: ${LLM_TEXT}; alpha (echo, add, greet) registered as client alpha, per call, tools_to_execute ["*"]; a virtual key allowed openai with any model and key and mcp_configs alpha ["*"], expiring 5s after it is created; it is later also deactivated.`, [
  ...freshServer("GOV-15", "alpha", ["echo", "add", "greet"]),
  alphaClient("GOV-15"),
  request("GOV-15", "setup: create the virtual key, expiring in 5s", "POST", "/api/governance/virtual-keys", {
    raw: "{{gov15_body}}", asserts: [A.status(200)],
    pre: [`pm.variables.set('gov15_body', JSON.stringify({ name: 'GOV-15 key', provider_configs: ${lit(OPENAI_GRANT)}, mcp_configs: [{ mcp_client_name: 'alpha', tools_to_execute: ['*'] }], expires_at: new Date(Date.now() + 5000).toISOString() }));`],
    extraTests: ["pm.collectionVariables.set('gov15_vk_id', pm.response.json().virtual_key.id);", "pm.collectionVariables.set('gov15_vk', pm.response.json().virtual_key.value);"],
  }),
  llmScript("GOV-15", "gov15", TEXT_OK),
  infPost("GOV-15", "chat with the expired key, 6s later", "/v1/chat/completions", chatBody("gov15"), [A.status(403), A.msgEquals("virtual key has expired")], { headers: withVK("GOV-15"), pre: ["setTimeout(function () {}, 6000);"] }),
  govExec("GOV-15", "alpha-echo", [A.status(403), A.msgEquals("virtual key has expired")]),
  rpc("GOV-15", "/mcp initialize with the expired key", init("2025-03-26"), { headers: [MCP_ACCEPT, ...withVK("GOV-15")], asserts: [A.status(403), A.msgEquals("virtual key has expired")] }),
  request("GOV-15", "setup: deactivate the expired key", "PUT", "/api/governance/virtual-keys/{{gov15_vk_id}}", { body: { is_active: false }, asserts: [A.status(200)] }),
  infPost("GOV-15", "chat with the inactive, expired key", "/v1/chat/completions", chatBody("gov15"), [A.status(403), A.msgEquals("virtual key is inactive")], { headers: withVK("GOV-15") }),
  govExec("GOV-15", "alpha-echo", [A.status(403), A.msgEquals("virtual key is inactive")]),
  rpc("GOV-15", "/mcp initialize with the inactive, expired key", init("2025-03-26"), { headers: [MCP_ACCEPT, ...withVK("GOV-15")], asserts: [A.status(403), A.msgEquals("virtual key is inactive")] }),
  llmCaptured("GOV-15", "control: neither chat reached the model", "gov15", [reqCount(0)]),
  govTeardownVK("GOV-15"),
  deleteClient("GOV-15", "gov-15-alpha"),
]);

// typeIs checks the refusal's top-level error type.
const typeIs = (t) => A.custom(`type is ${lit(t)}`, [`pm.expect(pm.response.json().type, 'body: ' + pm.response.text()).to.equal(${lit(t)});`]);
const INACTIVE = [A.status(403), typeIs("access_blocked"), A.msgEquals("virtual key is inactive")];
const GOV14 = folder("GOV-14 Inactive VK", `Setup: ${LLM_TEXT}; alpha (echo, add, greet) registered as client alpha, per call, tools_to_execute ["*"]; an inactive virtual key allowed openai with any model and key and mcp_configs alpha ["*"].`, [
  ...freshServer("GOV-14", "alpha", ["echo", "add", "greet"]),
  alphaClient("GOV-14"),
  request("GOV-14", "setup: create the inactive virtual key", "POST", "/api/governance/virtual-keys", {
    body: { name: "GOV-14 key", provider_configs: OPENAI_GRANT, mcp_configs: [{ mcp_client_name: "alpha", tools_to_execute: ["*"] }], is_active: false }, asserts: [A.status(200)],
    extraTests: ["pm.collectionVariables.set('gov14_vk_id', pm.response.json().virtual_key.id);", "pm.collectionVariables.set('gov14_vk', pm.response.json().virtual_key.value);"],
  }),
  llmScript("GOV-14", "gov14", TEXT_OK),
  govExec("GOV-14", "alpha-echo", INACTIVE),
  rpc("GOV-14", "/mcp initialize with the inactive key", init("2025-03-26"), { headers: [MCP_ACCEPT, ...withVK("GOV-14")], asserts: INACTIVE }),
  infPost("GOV-14", "chat with the inactive key", "/v1/chat/completions", chatBody("gov14"), INACTIVE, { headers: withVK("GOV-14") }),
  llmCaptured("GOV-14", "control: the chat never reached the model", "gov14", [reqCount(0)]),
  govTeardownVK("GOV-14"),
  deleteClient("GOV-14", "gov-14-alpha"),
]);

const GOV20 = folder("GOV-20 Agent mode respects the VK", `Setup: ${LLM_TEXT}; alpha (echo, add, greet) registered as client alpha, per call, tools_to_execute ["*"], tools_to_auto_execute ["*"]; a virtual key allowed openai with any model and key, mcp_configs alpha ["echo"]; turn 1 calls alpha-add, turn 2 answers "done".`, [
  ...agentSetup("GOV-20", ["*"]),
  govVK("GOV-20", [{ mcp_client_name: "alpha", tools_to_execute: ["echo"] }]),
  llmScript("GOV-20", "gov20", { turns: [{ tool_calls: [llmCall("g1", "alpha-add", { a: 1 })] }, { text: "done" }] }),
  govChat("GOV-20", "gov20", [A.status(200), chatContent("done")]),
  llmCaptured("GOV-20", "turn 2's tool content reports the grant", "gov20", [reqCount(2), A.custom("the tool content names the virtual key", [REQS, TOOL_MSGS(1), `pm.expect(tm.g1, JSON.stringify(reqs[1].json.messages)).to.equal(${lit("Error executing tool alpha-add: tool execution failed: MCP tool 'alpha-add' is not allowed for virtual key 'GOV-20 key'")});`])]),
  mockGet("GOV-20", "alpha", "alpha never ran add", [callsIs("add", 0)]),
  govTeardownVK("GOV-20"),
  agentTeardown("GOV-20"),
]);

const GOV21 = folder("GOV-21 Provider-injected tools ignore VK grants", `Setup: as INF-60 (${INJ_TEXT}); a virtual key allowed openai with any model and key and no mcp_configs; turn 1 calls web_search, turn 2 answers "answer".`, [
  ...injectedSetup("GOV-21"),
  govVK("GOV-21", null),
  llmScript("GOV-21", "gov21", { turns: [SEARCH_CALL, { text: "answer" }] }),
  govChat("GOV-21", "gov21", [A.status(200), chatContent("answer")]),
  mockGet("GOV-21", "alpha", "the chat ran search", [callsIs("search", 1)]),
  govExec("GOV-21", "alpha-search", [A.status(403)]),
  govTeardownVK("GOV-21"),
  ...injectedTeardown("GOV-21"),
]);

const GOV33 = folder("GOV-33 A virtual MCP grants on every surface", `Setup: ${GOV_TEXT("no mcp_configs")}; a virtual MCP "Tools A" over alpha's echo, with the virtual key attached.`, [
  ...freshServer("GOV-33", "alpha", ["echo", "add", "greet"]),
  alphaClient("GOV-33"),
  govVK("GOV-33", null),
  request("GOV-33", "setup: create the virtual MCP Tools A over alpha's echo", "POST", "/api/mcp/virtual-mcps", { body: { name: "Tools A", tools: [{ mcp_client_id: "gov-33-alpha", tool_names: ["echo"] }] }, asserts: [A.status(201)], extraTests: ["pm.collectionVariables.set('gov33_vmcp', pm.response.json().virtual_mcp.id);"] }),
  request("GOV-33", "setup: attach the virtual key", "POST", "/api/mcp/virtual-mcps/{{gov33_vmcp}}/virtual-keys/{{gov33_vk_id}}", { asserts: [A.status(200)] }),
  llmScript("GOV-33", "gov33", TEXT_OK),
  govChat("GOV-33", "gov33", [A.status(200)]),
  llmCaptured("GOV-33", "only alpha-echo is injected", "gov33", [reqCount(1), toolsAre(0, ["alpha-echo"])]),
  govExec("GOV-33", "alpha-echo", [A.status(200)]),
  govExec("GOV-33", "alpha-add", [A.status(403)]),
  govList("GOV-33", "/mcp tools/list with the virtual key", ["alpha-echo"]),
  request("GOV-33", "teardown: delete the virtual MCP", "DELETE", "/api/mcp/virtual-mcps/{{gov33_vmcp}}", { asserts: [A.status(200)] }),
  govTeardownVK("GOV-33"),
  deleteClient("GOV-33", "gov-33-alpha"),
]);

// ================================================================ a per-user OAuth caller with no token, on the mock LLM

const AUTH39_SA = { key: "x-bf-mcp-session-id", value: "auth39-sess1" };
const AUTH39_MSG = "Authentication required for auth39. Open this URL to connect your account:";
const AUTH39 = folder("AUTH-39 No-token caller on every surface", `Setup: ${PU_TEXT(false)}; ${PU_SERVER_TEXT("auth39")} and tools_to_auto_execute ["*"], verified as admin; ${LLM_TEXT}; openai's injected_tools expose auth39's echo_tool as web_search. Every call sends x-bf-mcp-session-id auth39-sess1, which has no token.`, [
  ...puConfig("AUTH-39", false),
  ...puConnect("AUTH-39", "auth39", {}, { tools_to_auto_execute: ["*"] }),
  readProvider("AUTH-39"),
  putInjected("AUTH-39", "setup: openai injects auth39's echo_tool as web_search", [{ name: "web_search", mcp_client_name: "auth39", tool_name: "echo_tool" }]),
  gwCall("AUTH-39", "/mcp tools/call as auth39-sess1", "auth39-echo_tool", [toolResultError(null, AUTH39_MSG)], [AUTH39_SA]),
  llmScript("AUTH-39", "auth39-agent", { turns: [{ tool_calls: [llmCall("a1", "auth39-echo_tool")] }, { text: "done" }] }),
  infPost("AUTH-39", "agent chat as auth39-sess1; the model calls auth39-echo_tool", "/v1/chat/completions", chatBody("auth39-agent"), [A.status(401), A.custom("error.type is \"mcp_auth_required\"", ["var b = pm.response.json();", "pm.expect(b.error && b.error.type, 'body: ' + pm.response.text()).to.equal('mcp_auth_required');"])], { headers: [AUTH39_SA] }),
  llmScript("AUTH-39", "auth39-inj", { turns: [{ tool_calls: [llmCall("w1", "web_search")] }, { text: "answer" }] }),
  infPost("AUTH-39", "chat as auth39-sess1; the model calls web_search", "/v1/chat/completions", chatBody("auth39-inj"), [A.status(200), chatContent("answer")], { headers: [AUTH39_SA] }),
  llmCaptured("AUTH-39", "the model's web_search result starts \"Authentication required for\"", "auth39-inj", [reqCount(2), A.custom("w1's tool content starts \"Authentication required for\"", [REQS, TOOL_MSGS(1), "pm.expect((tm.w1 || '').indexOf('Authentication required for'), JSON.stringify(reqs[1].json.messages)).to.equal(0);"])]),
  putInjected("AUTH-39", "teardown: clear openai's injected_tools", []),
  deleteClient("AUTH-39", "auth39-u"),
  restoreConfig("AUTH-39"),
]);

// ================================================================ tool failures inside agent mode, on the mock LLM

const FAIL19_TEXT = "echo is broken upstream";
const FAIL19_TIMEOUT = "Error executing tool alpha-slow_tool: tool execution failed: MCP tool call timed out after 2s";
const FAIL19 = folder("FAIL-19 Agent mode with a tool-level error and a timeout", `Setup: ${LLM_TEXT}; alpha (echo, slow_tool), with echo answering every call with an isError result (err_kind tool) whose text is ${lit(FAIL19_TEXT)} and slow_tool hanging for 10s, registered as client alpha, per call, tools_to_execute ["*"], tools_to_auto_execute ["*"], tool_execution_timeout 2; each chat's turn 1 calls one tool and turn 2 answers "final".`, [
  request("FAIL-19", "setup: fresh mcpchaos server alpha", "POST", "/control", { base: CHAOS, noAuth: true, body: { add: "alpha" }, asserts: [A.status(200)] }),
  mockPost("FAIL-19", "alpha", "tools/set", "setup: alpha serves echo (an isError result every call) and slow_tool (hanging 10s)", { tools: [{ name: "echo", fault: { err_rate: 1, err_kind: "tool", message: FAIL19_TEXT } }, { name: "slow_tool", fault: { hang: "10s" } }] }),
  alphaClient("FAIL-19", { tools_to_auto_execute: ["*"], tool_execution_timeout: 2 }),
  llmScript("FAIL-19", "fail19-err", { turns: [{ tool_calls: [llmCall("e1", "alpha-echo")] }, { text: "final" }] }),
  infPost("FAIL-19", "chat; the model calls alpha-echo", "/v1/chat/completions", chatBody("fail19-err"), [A.status(200), chatContent("final")]),
  llmCaptured("FAIL-19", "turn 2's tool content is the mock's text", "fail19-err", [reqCount(2), A.custom(`the tool content is ${lit(FAIL19_TEXT)}`, [REQS, TOOL_MSGS(1), `pm.expect(tm.e1, JSON.stringify(reqs[1].json.messages)).to.equal(${lit(FAIL19_TEXT)});`])]),
  llmScript("FAIL-19", "fail19-hang", { turns: [{ tool_calls: [llmCall("h1", "alpha-slow_tool")] }, { text: "final" }] }),
  infPost("FAIL-19", "chat; the model calls alpha-slow_tool", "/v1/chat/completions", chatBody("fail19-hang"), [A.status(200), chatContent("final")]),
  llmCaptured("FAIL-19", "turn 2's tool content reports the timeout", "fail19-hang", [reqCount(2), A.custom(`the tool content starts ${lit(FAIL19_TIMEOUT)}`, [REQS, TOOL_MSGS(1), `pm.expect((tm.h1 || '').indexOf(${lit(FAIL19_TIMEOUT)}), JSON.stringify(reqs[1].json.messages)).to.equal(0);`])]),
  agentTeardown("FAIL-19"),
]);

// ================================================================ client admin refusals, validation and lifecycle

// BOOT_TEXT names a client the runner's config.json declares; auth types that need an admin step boot it pending verification.
const BOOT_TEXT = (clientId, kind, server) => `client ${clientId}, declared in config.json as ${kind} client on fleet server ${server} and still pending verification`;
const clientPost = (id, title, clientId, verb, asserts, opts = {}) => request(id, title, "POST", `/api/mcp/client/${clientId}/${verb}`, { asserts, ...opts });
const clientPut = (id, title, clientId, body, asserts) => request(id, title, "PUT", `/api/mcp/client/${clientId}`, { body, asserts });
const listClients = (id, title, query, asserts) => request(id, title, "GET", `/api/mcp/clients?${query}`, { asserts: [A.status(200), ...asserts] });
// clientIdsAre asserts the listed client ids, in any order.
const clientIdsAre = (ids) => A.custom(`client ids are ${lit(ids)}`, [`pm.expect((pm.response.json().clients || []).map(function (c) { return c.config.client_id; }).sort(), 'body: ' + pm.response.text()).to.eql(${lit([...ids].sort())});`]);
const configField = (field, v) => A.custom(`${field} is ${lit(v)}`, [`pm.expect(pm.response.json().clients[0].config[${lit(field)}], 'body: ' + pm.response.text()).to.eql(${lit(v)});`]);

const ADM30 = folder("ADM-30 reauthorize and reregister refusals", `Setup: admin credentials; client adm30-n, auth none, per call on alpha; ${BOOT_TEXT("adm30-p", "an oauth", "adm30")}.`, [
  ...freshServer("ADM-30", "alpha", ["echo_tool", "add_tool"]),
  createClient("ADM-30", httpClient("adm30-n", "adm30_n", "alpha"), "setup: create the none client"),
  ...["reauthorize", "reregister"].flatMap((verb) => [
    clientPost("ADM-30", `${verb} the none client`, "adm30-n", verb, [A.status(400), A.msgEquals("reauthorize only applies to OAuth-based clients (oauth, per_user_oauth), got \"none\"")]),
    clientPost("ADM-30", `${verb} the pending client`, "adm30-p", verb, [A.status(400), A.msgContains("has not completed initial OAuth authorization yet; use initiate-verification instead")]),
    clientPost("ADM-30", `${verb} an unknown id`, "adm30-nosuch", verb, [A.status(404), A.msgEquals("MCP client 'adm30-nosuch' not found")]),
  ]),
  deleteClient("ADM-30", "adm30-n"),
]);

// initiateSaves keeps the oauth_config_id an initiate-verification answer carries in v.
const initiateSaves = (v) => A.custom("status pending_oauth, an oauth_config_id and no flow_id", [
  "var b = pm.response.json();",
  "pm.expect(b.status, 'body: ' + pm.response.text()).to.equal('pending_oauth');",
  "pm.expect(b.oauth_config_id).to.be.a('string').and.not.equal('');",
  "pm.expect(b).to.not.have.property('flow_id');",
  ...(v ? [`pm.collectionVariables.set(${lit(v)}, b.oauth_config_id);`] : []),
]);
const ADM35 = folder("ADM-35 initiate-verification for a config.json OAuth client", `Setup: admin credentials; ${OAUTH_SERVER_TEXT} (adm35); ${BOOT_TEXT("adm35-p", "an oauth", "adm35")}; client adm35-n, auth none on alpha; an OAuth client adm35-o created through the API on adm35 and completed.`, [
  ...allowlistOn("ADM-35"),
  ...asServer("ADM-35", "adm35"),
  clientPost("ADM-35", "initiate-verification on the config.json client", "adm35-p", "initiate-verification", [A.status(200), initiateSaves("adm35_oc1")]),
  getClient("ADM-35", "adm35-p", "it holds that oauth_config_id and is still pending_verification", [clientState("pending_verification"), A.custom("oauth_config_id is the one returned", ["pm.expect(pm.response.json().clients[0].config.oauth_config_id).to.equal(pm.collectionVariables.get('adm35_oc1'));"])]),
  clientPost("ADM-35", "initiate-verification again", "adm35-p", "initiate-verification", [A.status(200), initiateSaves(), A.custom("a new oauth_config_id", ["pm.expect(pm.response.json().oauth_config_id).to.not.equal(pm.collectionVariables.get('adm35_oc1'));"])]),
  ...freshServer("ADM-35", "alpha", ["echo_tool", "add_tool"]),
  createClient("ADM-35", httpClient("adm35-n", "adm35_n", "alpha"), "setup: create the none client"),
  clientPost("ADM-35", "initiate-verification on the none client", "adm35-n", "initiate-verification", [A.status(400)]),
  ...connectOAuth("ADM-35", oauthBody("adm35-o", "adm35_o", "adm35"), "adm35o", null, "the API-created OAuth client"),
  clientPost("ADM-35", "initiate-verification on the API-created OAuth client", "adm35-o", "initiate-verification", [A.status(400), A.msgEquals("MCP client has no pending OAuth bootstrap config to initiate from")]),
  deleteClient("ADM-35", "adm35-o"),
  deleteClient("ADM-35", "adm35-n"),
  deleteClient("ADM-35", "adm35-p"),
  restoreConfig("ADM-35"),
]);

const ADM37 = folder("ADM-37 oauth_config rotation guards", `Setup: admin credentials; ${OAUTH_SERVER_TEXT} (adm37); a shared OAuth client adm37-o on adm37, completed; client adm37-n, auth none on alpha; ${BOOT_TEXT("adm37-p", "an oauth", "adm37")}.`, [
  ...allowlistOn("ADM-37"),
  ...asServer("ADM-37", "adm37"),
  ...connectOAuth("ADM-37", oauthBody("adm37-o", "adm37_o", "adm37"), "adm37o", null, "the shared OAuth client"),
  ...freshServer("ADM-37", "alpha", ["echo_tool", "add_tool"]),
  createClient("ADM-37", httpClient("adm37-n", "adm37_n", "alpha"), "setup: create the none client"),
  clientPut("ADM-37", "oauth_config on the none client", "adm37-n", { oauth_config: { client_id: "adm37-new" } }, [A.status(400), A.msgEquals("oauth_config can only be updated for MCP clients using auth_type 'oauth' or 'per_user_oauth'")]),
  clientPut("ADM-37", "oauth_config on the pending client", "adm37-p", { oauth_config: { client_id: "adm37-new" } }, [A.status(409), A.msgContains("cannot be updated before this MCP client's OAuth verification is completed")]),
  clientPut("ADM-37", "token_url change without client_secret", "adm37-o", { oauth_config: { token_url: "http://127.0.0.1:9/adm37/token" } }, [A.status(400), A.msgContains("client_secret must be resent explicitly")]),
  clientPut("ADM-37", "rotation plus disabled true", "adm37-o", { oauth_config: { client_id: "adm37-new" }, disabled: true }, [A.status(400), A.msgContains("cannot be rotated while disabling")]),
  deleteClient("ADM-37", "adm37-o"),
  deleteClient("ADM-37", "adm37-n"),
  restoreConfig("ADM-37"),
]);

const locationHas = (s) => A.custom(`Location contains ${lit(s)}`, ["var loc = pm.response.headers.get('Location') || '';", `pm.expect(loc, 'Location: ' + loc).to.include(${lit(s)});`]);
const callback = (id, title, query, asserts) => request(id, title, "GET", `/api/oauth/callback?${query}`, { noAuth: true, noRedirect: true, noCookies: true, asserts });
const ADM41 = folder("ADM-41 Callback edge cases", `Setup: no credential on the callbacks; ${OAUTH_SERVER_TEXT} (adm41) and a shared OAuth client on it taken through consent and its callback.`, [
  callback("ADM-41", "bogus state and code", "state=adm41-bogus&code=adm41-bogus", [A.status(302), locationEnds("oauth-callback?status=failed&error=OAuth+authentication+failed.")]),
  callback("ADM-41", "an error with no state", "error=access_denied", [A.status(302), locationHas("/workspace/mcp-sessions/auth-failed?")]),
  ...allowlistOn("ADM-41"),
  ...asServer("ADM-41", "adm41"),
  createOAuth("ADM-41", "setup: create the shared OAuth client", oauthBody("adm41-o", "adm41_o", "adm41"), "adm41o"),
  consentAs("ADM-41", "setup: consent", "adm41o", null),
  followCallback("ADM-41", "setup: follow the callback", "adm41o", callbackSuccess()),
  followCallback("ADM-41", "replay the same callback", "adm41o", [A.status(302), locationHas("status=failed")]),
  completeOAuth("ADM-41", "teardown: complete the client", "adm41o", [A.status(200)]),
  deleteClient("ADM-41", "adm41-o"),
  restoreConfig("ADM-41"),
]);

const ADM43 = folder("ADM-43 Reconnect refusals", `Setup: admin credentials; client adm43-c, auth none, per call on alpha; client adm43-s, sticky on alpha, disabled; ${BOOT_TEXT("adm43-p", "a sticky oauth", "adm43")}.`, [
  ...freshServer("ADM-43", "alpha", ["echo_tool", "add_tool"]),
  createClient("ADM-43", httpClient("adm43-c", "adm43_c", "alpha"), "setup: create the per-call client"),
  createClient("ADM-43", httpClient("adm43-s", "adm43_s", "alpha", { needs_session_stickiness: true }), "setup: create the sticky client"),
  clientPut("ADM-43", "setup: disable the sticky client", "adm43-s", { disabled: true }, [A.status(200)]),
  clientPost("ADM-43", "reconnect the per-call client", "adm43-c", "reconnect", [A.status(400), A.msgContains("client uses per-call connections; there is no persistent connection to reconnect")]),
  clientPost("ADM-43", "reconnect the disabled client", "adm43-s", "reconnect", [A.status(400), A.msgEquals("cannot reconnect a disabled MCP client: enable the client first")]),
  clientPost("ADM-43", "reconnect the pending client", "adm43-p", "reconnect", [A.status(400), A.msgContains("awaiting admin OAuth authorization")]),
  deleteClient("ADM-43", "adm43-s"),
  deleteClient("ADM-43", "adm43-c"),
]);

const ADM45 = folder("ADM-45 refresh-tools refusals and failure", `Setup: admin credentials; client adm45-c, auth none, per call on alpha; ${BOOT_TEXT("adm45-h", "a per_user_headers", "adm45")}; then alpha fails every tools/list.`, [
  ...freshServer("ADM-45", "alpha", ["echo_tool", "add_tool"]),
  createClient("ADM-45", httpClient("adm45-c", "adm45_c", "alpha", { tools_to_execute: ["*"] }), "setup: create the client"),
  clientPost("ADM-45", "refresh-tools on an unknown id", "adm45-nosuch", "refresh-tools", [A.status(404), A.msgContains("mcp client not found")]),
  clientPut("ADM-45", "setup: disable the client", "adm45-c", { disabled: true }, [A.status(200)]),
  clientPost("ADM-45", "refresh-tools on the disabled client", "adm45-c", "refresh-tools", [A.status(400), A.msgContains("cannot refresh tools for a disabled MCP client")]),
  clientPut("ADM-45", "setup: enable the client", "adm45-c", { disabled: false }, [A.status(200)]),
  clientPost("ADM-45", "refresh-tools on the pending per_user_headers client", "adm45-h", "refresh-tools", [A.status(400), A.msgContains("awaiting admin verification")]),
  mockPost("ADM-45", "alpha", "faults", "setup: alpha fails every tools/list", { faults: { list_fail: true } }),
  clientPost("ADM-45", "refresh-tools while tools/list fails", "adm45-c", "refresh-tools", [A.status(500), A.msgContains("Failed to refresh MCP client tools")]),
  mockPost("ADM-45", "alpha", "faults/clear", "teardown: clear alpha's faults", {}),
  deleteClient("ADM-45", "adm45-c"),
]);

const ADM50 = folder("ADM-50 Header edits on a shared HTTP client are verified first", "Setup: admin credentials; client adm50-k, auth headers with X-Key old, per call on alpha, which counts calls under X-Key; alpha then requires X-Key new.", [
  ...freshServer("ADM-50", "alpha", ["echo_tool", "add_tool"]),
  mockAuth("ADM-50", "alpha", "setup: alpha requires X-Key old", { identity_header: "X-Key", require_headers: { "X-Key": "old" } }),
  createClient("ADM-50", httpClient("adm50-k", "adm50_k", "alpha", { auth_type: "headers", headers: { "X-Key": "old" }, tools_to_execute: ["*"] }), "setup: create the headers client"),
  mockAuth("ADM-50", "alpha", "setup: alpha now requires X-Key new", { identity_header: "X-Key", require_headers: { "X-Key": "new" } }),
  clientPut("ADM-50", "headers X-Key bad", "adm50-k", { headers: { "X-Key": "bad" } }, [A.status(422), A.msgStartsWith("The new headers were rejected by the MCP server, so nothing was changed")]),
  mockAuth("ADM-50", "alpha", "setup: alpha accepts X-Key old again", { identity_header: "X-Key", require_headers: { "X-Key": "old" } }),
  execTool("ADM-50", "control: a call still sends the old value", "adm50_k-echo_tool", [A.status(200)]),
  mockGet("ADM-50", "alpha", "the call carried X-Key old", [identityCalls("old", 1)]),
  mockAuth("ADM-50", "alpha", "setup: alpha requires X-Key new", { identity_header: "X-Key", require_headers: { "X-Key": "new" } }),
  clientPut("ADM-50", "headers X-Key new", "adm50-k", { headers: { "X-Key": "new" } }, [A.status(200)]),
  execTool("ADM-50", "a call after the edit", "adm50_k-echo_tool", [A.status(200)]),
  mockGet("ADM-50", "alpha", "alpha's next request carried X-Key new", [identityCalls("new", 1)]),
  getClient("ADM-50", "adm50-k", "setup: read the masked headers", [], { extraTests: ["pm.collectionVariables.set('adm50_masked', JSON.stringify({ headers: pm.response.json().clients[0].config.headers }));"] }),
  saveSessions("ADM-50", "alpha", "adm50_sessions"),
  request("ADM-50", "PUT the masked headers back", "PUT", "/api/mcp/client/adm50-k", { raw: "{{adm50_masked}}", asserts: [A.status(200)] }),
  mockGet("ADM-50", "alpha", "no new alpha session", [sessionsPlus("adm50_sessions", 0)]),
  deleteClient("ADM-50", "adm50-k"),
]);

const ADM56 = folder("ADM-56 allow_by_default and its legacy alias", "Setup: admin credentials; clients adm56-a (allow_on_all_virtual_keys true) and adm56-b (allow_on_all_virtual_keys true, allow_by_default false), auth none on alpha; then the same two cases as updates on adm56-b.", [
  ...freshServer("ADM-56", "alpha", ["echo_tool", "add_tool"]),
  createClient("ADM-56", httpClient("adm56-a", "adm56_a", "alpha", { allow_on_all_virtual_keys: true }), "create with allow_on_all_virtual_keys true"),
  getClient("ADM-56", "adm56-a", "both fields are true", [configField("allow_by_default", true), configField("allow_on_all_virtual_keys", true)]),
  createClient("ADM-56", httpClient("adm56-b", "adm56_b", "alpha", { allow_on_all_virtual_keys: true, allow_by_default: false }), "create with both fields"),
  getClient("ADM-56", "adm56-b", "allow_by_default false wins", [configField("allow_by_default", false), configField("allow_on_all_virtual_keys", false)]),
  listClients("ADM-56", "?allowed_by_default=true", "allowed_by_default=true&search=adm56", [clientIdsAre(["adm56-a"])]),
  listClients("ADM-56", "?all_virtual_keys=true", "all_virtual_keys=true&search=adm56", [clientIdsAre(["adm56-a"])]),
  request("ADM-56", "?allowed_by_default=x", "GET", "/api/mcp/clients?allowed_by_default=x", { asserts: [A.status(400)] }),
  request("ADM-56", "?all_virtual_keys=x", "GET", "/api/mcp/clients?all_virtual_keys=x", { asserts: [A.status(400)] }),
  clientPut("ADM-56", "update adm56-b with allow_on_all_virtual_keys true", "adm56-b", { allow_on_all_virtual_keys: true }, [A.status(200)]),
  getClient("ADM-56", "adm56-b", "after the update both fields are true", [configField("allow_by_default", true), configField("allow_on_all_virtual_keys", true)]),
  clientPut("ADM-56", "update adm56-b with both fields", "adm56-b", { allow_on_all_virtual_keys: true, allow_by_default: false }, [A.status(200)]),
  getClient("ADM-56", "adm56-b", "after the update allow_by_default false wins", [configField("allow_by_default", false), configField("allow_on_all_virtual_keys", false)]),
  deleteClient("ADM-56", "adm56-b"),
  deleteClient("ADM-56", "adm56-a"),
]);

const ADM58 = folder("ADM-58 Delete removes the client everywhere", "Setup: admin credentials; client adm58-c, auth none, per call on alpha, tools_to_execute [\"*\"]; a VK granted every tool of it.", [
  ...freshServer("ADM-58", "alpha", ["echo_tool", "add_tool"]),
  createClient("ADM-58", httpClient("adm58-c", "adm58_c", "alpha", { tools_to_execute: ["*"] }), "setup: create the client"),
  vkCreate("ADM-58", "setup: create the VK, granted every tool of adm58_c", { name: "ADM-58 key", mcp_configs: [{ mcp_client_name: "adm58_c", tools_to_execute: ["*"] }] }, "adm58_vk"),
  rpc("ADM-58", "control: /mcp tools/list with the VK", listMsg, { headers: [MCP_ACCEPT, { key: "x-bf-vk", value: "{{adm58_vk}}" }], asserts: [A.status(200), toolNamesInList(["adm58_c-add_tool", "adm58_c-echo_tool"])] }),
  rpc("ADM-58", "control: /mcp/adm58-c with the VK", listMsg, { path: "/mcp/adm58-c", headers: [MCP_ACCEPT, { key: "x-bf-vk", value: "{{adm58_vk}}" }], asserts: [A.status(200), toolNamesInList(["adm58_c-add_tool", "adm58_c-echo_tool"])] }),
  execTool("ADM-58", "control: execute its tool", "adm58_c-echo_tool", [A.status(200)]),
  request("ADM-58", "delete the client", "DELETE", "/api/mcp/client/adm58-c", { asserts: [A.status(200), A.jsonMessage("MCP client removed successfully")] }),
  listClients("ADM-58", "it is not listed", "server=adm58-c", [clientIdsAre([])]),
  rpc("ADM-58", "/mcp tools/list with the VK lacks its tools", listMsg, { headers: [MCP_ACCEPT, { key: "x-bf-vk", value: "{{adm58_vk}}" }], asserts: [A.status(200), toolNamesInList([])] }),
  rpc("ADM-58", "/mcp/adm58-c with the VK", listMsg, { path: "/mcp/adm58-c", headers: [MCP_ACCEPT, { key: "x-bf-vk", value: "{{adm58_vk}}" }], asserts: [A.status(403)] }),
  execTool("ADM-58", "execute its tool", "adm58_c-echo_tool", [A.status(400), A.msgContains("is not available or not permitted")]),
  createClient("ADM-58", httpClient("adm58-c", "adm58_c", "alpha"), "the name can be used again"),
  deleteClient("ADM-58", "adm58-c"),
  vkDelete("ADM-58", "teardown: delete the VK", "adm58_vk"),
]);

const ADM63 = folder("ADM-63 List filters", "Setup: admin credentials; on alpha: adm63_c (http, auth none), adm63_e (sse), adm63_h (per_user_headers), adm63_d (disabled) and adm63_m (code mode). Each filter is combined with search adm63.", [
  ...freshServer("ADM-63", "alpha", ["echo_tool", "add_tool"]),
  createClient("ADM-63", httpClient("adm63-c", "adm63_c", "alpha"), "setup: create adm63_c"),
  createClient("ADM-63", { client_id: "adm63-e", name: "adm63_e", connection_type: "sse", connection_string: `${CHAOS}/s/alpha/sse`, auth_type: "none" }, "setup: create adm63_e"),
  createClient("ADM-63", HEADERS_BASE("adm63-h", "adm63_h", { per_user_header_keys: ["x-key"], user_headers: { "x-key": "good" } }), "setup: create adm63_h"),
  createClient("ADM-63", httpClient("adm63-d", "adm63_d", "alpha"), "setup: create adm63_d"),
  clientPut("ADM-63", "setup: disable adm63_d", "adm63-d", { disabled: true }, [A.status(200)]),
  createClient("ADM-63", httpClient("adm63-m", "adm63_m", "alpha", { is_code_mode_client: true }), "setup: create adm63_m"),
  listClients("ADM-63", "search is case-insensitive", "search=ADM63_C", [clientIdsAre(["adm63-c"])]),
  listClients("ADM-63", "server", "server=adm63-e", [clientIdsAre(["adm63-e"])]),
  listClients("ADM-63", "connection_type sse", "connection_type=sse&search=adm63", [clientIdsAre(["adm63-e"])]),
  listClients("ADM-63", "auth_type per_user_headers", "auth_type=per_user_headers&search=adm63", [clientIdsAre(["adm63-h"])]),
  listClients("ADM-63", "disabled true", "disabled=true&search=adm63", [clientIdsAre(["adm63-d"])]),
  listClients("ADM-63", "code_mode true", "code_mode=true&search=adm63", [clientIdsAre(["adm63-m"])]),
  listClients("ADM-63", "state unstable includes the disabled client", "state=unstable&search=adm63", [A.custom("adm63-d is listed and the healthy adm63-c is not", ["var ids = pm.response.json().clients.map(function (c) { return c.config.client_id; });", "pm.expect(ids, 'body: ' + pm.response.text()).to.include('adm63-d');", "pm.expect(ids).to.not.include('adm63-c');"])]),
  listClients("ADM-63", "total_count ignores limit", "search=adm63&limit=1", [A.custom("one client, total_count 5", ["var b = pm.response.json();", "pm.expect(b.clients.length, 'body: ' + pm.response.text()).to.equal(1);", "pm.expect(b.total_count).to.equal(5);"])]),
  ...["adm63-m", "adm63-d", "adm63-h", "adm63-e", "adm63-c"].map((c) => deleteClient("ADM-63", c)),
]);

const ADM67 = folder("ADM-67 verify-headers refusals", "Setup: admin credentials; alpha (echo_tool, add_tool) requires x-key: good; adm67_h, per_user_headers on alpha with per_user_header_keys [\"x-key\"]; adm67_n, auth none on alpha (created before alpha requires the header); adm67_q, an OpenAPI per_user_headers client from an inline Petstore document.", [
  ...freshServer("ADM-67", "alpha", ["echo_tool", "add_tool"]),
  createClient("ADM-67", httpClient("adm67-n", "adm67_n", "alpha"), "setup: create the none client"),
  mockAuth("ADM-67", "alpha", "setup: alpha requires x-key: good", { require_headers: { "x-key": "good" } }),
  createClient("ADM-67", HEADERS_BASE("adm67-h", "adm67_h", { per_user_header_keys: ["x-key"], user_headers: { "x-key": "good" } }), "setup: create the per_user_headers client"),
  createClient("ADM-67", { client_id: "adm67-q", name: "adm67_q", connection_type: "openapi", auth_type: "per_user_headers", per_user_header_keys: ["x-key"], tools_to_execute: ["*"], openapi_config: { spec: PETSTORE } }, "setup: create the OpenAPI per_user_headers client"),
  verifyHeaders("ADM-67", "an unknown id", "adm67-nosuch", { "x-key": "good" }, [A.status(404)]),
  request("ADM-67", "body \"{\"", "POST", "/api/mcp/client/adm67-h/verify-headers", { raw: "{", asserts: [A.status(400), A.msgContains("Invalid request body")] }),
  verifyHeaders("ADM-67", "the none client", "adm67-n", { "x-key": "good" }, [A.status(400), A.msgContains("verify-headers only applies to auth_type='per_user_headers' clients")]),
  verifyHeaders("ADM-67", "the OpenAPI client", "adm67-q", { "x-key": "good" }, [A.status(400), A.msgContains("verify-headers does not apply to openapi clients")]),
  request("ADM-67", "{}", "POST", "/api/mcp/client/adm67-h/verify-headers", { body: {}, asserts: [A.status(400), A.msgContains("missing values for required keys: x-key")] }),
  verifyHeaders("ADM-67", "a bad value", "adm67-h", { "x-key": "bad" }, [A.status(422)]),
  deleteClient("ADM-67", "adm67-q"),
  deleteClient("ADM-67", "adm67-n"),
  deleteClient("ADM-67", "adm67-h"),
]);

const ADM68 = folder("ADM-68 verify-headers activates the admin credential", `Setup: admin credentials; fleet server adm68 (echo_tool, add_tool) requires x-key: good; ${BOOT_TEXT("adm68-h", "a per_user_headers", "adm68")}, per_user_header_keys ["x-key"].`, [
  ...freshServer("ADM-68", "adm68", ["echo_tool", "add_tool"]),
  mockAuth("ADM-68", "adm68", "setup: adm68 requires x-key: good", { require_headers: { "x-key": "good" } }),
  getClient("ADM-68", "adm68-h", "control: pending_verification with no credential", [clientState("pending_verification"), A.custom("there is no credential", ["pm.expect(pm.response.json().clients[0].credential == null, 'body: ' + pm.response.text()).to.equal(true);"])]),
  clientPost("ADM-68", "control: refresh-tools before verification", "adm68-h", "refresh-tools", [A.status(400), A.msgContains("awaiting admin verification")]),
  verifyHeaders("ADM-68", "verify-headers with x-key good", "adm68-h", { "x-key": "good" }, [A.status(200), A.jsonEquals("status", "success"), A.jsonEquals("tools_count", 2), messageStarts("MCP client verified. 2 tools discovered.")]),
  getClient("ADM-68", "adm68-h", "healthy, with an active headers credential", [clientState("healthy"), credKind("headers"), credStatus("active")]),
  clientPost("ADM-68", "refresh-tools", "adm68-h", "refresh-tools", [A.status(200)]),
  deleteClient("ADM-68", "adm68-h"),
]);

// pollClientTools re-reads one client every 500ms until it lists n tools or a deadline passes: seconds after the time
// (ms) kept in t0Var, or after the poll starts without one. It keeps how long after that time the client first listed n
// tools in <clientId>_seen_ms, or -1 when it never did.
const pollClientTools = (clientId, n, seconds, t0Var) => [
  `var t0 = ${t0Var ? `Number(pm.collectionVariables.get(${lit(t0Var)}))` : "Date.now()"};`,
  `var deadline = t0 + ${seconds * 1000};`,
  `pm.collectionVariables.set(${lit(clientId + "_seen_ms")}, -1);`,
  "function poll() {",
  `  pm.sendRequest({ url: pm.variables.get('base_url') + '/api/mcp/clients?server=${clientId}', method: 'GET', header: { Authorization: pm.variables.get('admin_auth_header') } }, function (err, res) {`,
  "    var c = !err && res.code === 200 ? (res.json().clients || [])[0] : null;",
  `    if (c && (c.tools || []).length >= ${n}) { pm.collectionVariables.set(${lit(clientId + "_seen_ms")}, Date.now() - t0); return; }`,
  "    if (Date.now() >= deadline) { return; }",
  "    setTimeout(poll, 500);",
  "  });",
  "}",
  "poll();",
];
// pollClientsTools is pollClientTools for several clients at once, budgets mapping each client id to its seconds
// from the kept time: every poll starts with this request, so a slow client cannot use up another's budget.
const pollClientsTools = (budgets, n, t0Var) => [
  `var t0 = Number(pm.collectionVariables.get(${lit(t0Var)}));`,
  "function poll(id, deadline) {",
  "  pm.sendRequest({ url: pm.variables.get('base_url') + '/api/mcp/clients?server=' + id, method: 'GET', header: { Authorization: pm.variables.get('admin_auth_header') } }, function (err, res) {",
  "    var c = !err && res.code === 200 ? (res.json().clients || [])[0] : null;",
  `    if (c && (c.tools || []).length >= ${n}) { pm.collectionVariables.set(id + '_seen_ms', Date.now() - t0); return; }`,
  "    if (Date.now() >= deadline) { return; }",
  "    setTimeout(function () { poll(id, deadline); }, 500);",
  "  });",
  "}",
  ...Object.entries(budgets).map(([id, seconds]) => `pm.collectionVariables.set(${lit(id + "_seen_ms")}, -1); poll(${lit(id)}, t0 + ${seconds * 1000});`),
];
// seenWithin checks the client polled by pollClientTools or pollClientsTools listed the tools within seconds of the kept time.
const seenWithin = (clientId, seconds) => A.custom(`it first listed them within ${seconds}s of the add`, [
  `var ms = Number(pm.collectionVariables.get(${lit(clientId + "_seen_ms")}));`,
  "pm.expect(ms, 'ms after the add (-1: never)').to.be.at.least(0);",
  `pm.expect(ms, 'ms after the add').to.be.at.most(${seconds * 1000});`,
]);
const SYNC_5S = configField("tool_sync_interval", 5000000000);
const ADM86_TOOLS = ["add_tool", "echo_tool", "extra_tool"];
const ADM86 = folder("ADM-86 Verifying a pending client starts its tool sync", `Setup: admin credentials; ${OAUTH_SERVER_TEXT} (adm86o); fleet server adm86h (echo_tool, add_tool) requires x-key: good; ${BOOT_TEXT("adm86-h", "a per_user_headers", "adm86h")}, per_user_header_keys ["x-key"]; ${BOOT_TEXT("adm86-o", "a per_user_oauth", "adm86o")}; client adm86-c, declared in config.json as a headers client on adm86h sending x-key: good; all three with tool_sync_interval "5s".`, [
  ...allowlistOn("ADM-86"),
  ...freshServer("ADM-86", "adm86h", ["echo_tool", "add_tool"]),
  mockAuth("ADM-86", "adm86h", "setup: adm86h requires x-key: good", { require_headers: { "x-key": "good" } }),
  ...asServer("ADM-86", "adm86o"),
  getClient("ADM-86", "adm86-h", "control: the per_user_headers client is pending_verification, syncing every 5s", [clientState("pending_verification"), SYNC_5S]),
  getClient("ADM-86", "adm86-o", "control: the per_user_oauth client is pending_verification, syncing every 5s", [clientState("pending_verification"), SYNC_5S]),
  getClient("ADM-86", "adm86-c", "control: the config.json headers client syncs every 5s and first lists add_tool and echo_tool", [clientState("healthy"), toolNamesAre(["add_tool", "echo_tool"]), SYNC_5S], { pre: pollClientTools("adm86-c", 2, 30) }),
  verifyHeaders("ADM-86", "verify-headers with x-key good", "adm86-h", { "x-key": "good" }, [A.status(200), A.jsonEquals("tools_count", 2)]),
  clientPost("ADM-86", "initiate-verification on the per_user_oauth client", "adm86-o", "initiate-verification", [A.status(200), statusIs("pending_oauth")], {
    extraTests: ["pm.collectionVariables.set('adm86o_oc', pm.response.json().oauth_config_id);", "pm.collectionVariables.set('adm86o_au', pm.response.json().authorize_url);"],
  }),
  consentAs("ADM-86", "consent as admin", "adm86o", "admin"),
  followCallback("ADM-86", "follow the callback", "adm86o", callbackSuccess()),
  completeOAuth("ADM-86", "complete", "adm86o", [A.status(200), messageStarts("OAuth credentials updated and verified successfully. 2 tools discovered")]),
  getClient("ADM-86", "adm86-h", "the per_user_headers client is healthy with two tools", [clientState("healthy"), toolNamesAre(["add_tool", "echo_tool"])]),
  getClient("ADM-86", "adm86-o", "the per_user_oauth client is healthy with two tools", [clientState("healthy"), toolNamesAre(["add_tool", "echo_tool"])]),
  request("ADM-86", "adm86h adds extra_tool", "POST", "/control/adm86h/tools/add", { base: CHAOS, noAuth: true, body: { tool: { name: "extra_tool" } }, asserts: [A.status(200)], extraTests: ["pm.collectionVariables.set('adm86_t0', Date.now());"] }),
  mockPost("ADM-86", "adm86o", "tools/add", "adm86o adds extra_tool", { tool: { name: "extra_tool" } }),
  getClient("ADM-86", "adm86-c", "control: the config.json headers client lists extra_tool within 30s (all three clients polled together)", [toolNamesAre(ADM86_TOOLS), seenWithin("adm86-c", 30)], { pre: pollClientsTools({ "adm86-c": 30, "adm86-h": 20, "adm86-o": 20 }, 3, "adm86_t0") }),
  getClient("ADM-86", "adm86-h", "the per_user_headers client lists extra_tool within 20s", [toolNamesAre(ADM86_TOOLS), seenWithin("adm86-h", 20)]),
  getClient("ADM-86", "adm86-o", "the per_user_oauth client lists extra_tool within 20s", [toolNamesAre(ADM86_TOOLS), seenWithin("adm86-o", 20)]),
  deleteClient("ADM-86", "adm86-c"),
  deleteClient("ADM-86", "adm86-h"),
  deleteClient("ADM-86", "adm86-o"),
  restoreConfig("ADM-86"),
]);

// AUTH-46: a per_user_headers client created over the API keeps the admin's sample values as its admin credential, which
// periodic discovery uses to pick up a tool the server adds later.
const AUTH46_TOOLS = ["add_tool", "echo_tool", "extra_tool"];
const AUTH46 = folder("AUTH-46 Periodic discovery of an API-created per_user_headers client", "Setup: admin credentials; fleet server auth46 (echo_tool, add_tool) requires x-key: good; a per_user_headers client on it, created over the API with per_user_header_keys [\"x-key\"], sample x-key good, tools_to_execute [\"*\"] and tool_sync_interval \"5s\"; auth46 then adds extra_tool.", [
  ...freshServer("AUTH-46", "auth46", ["echo_tool", "add_tool"]),
  mockAuth("AUTH-46", "auth46", "setup: auth46 requires x-key: good", { require_headers: { "x-key": "good" } }),
  createClient("AUTH-46", HEADERS_BASE("auth46-h", "auth46_h", { connection_string: `${CHAOS}/s/auth46/mcp`, per_user_header_keys: ["x-key"], user_headers: { "x-key": "good" }, tools_to_execute: ["*"], tool_sync_interval: "5s" }), "setup: create the per_user_headers client, syncing every 5s"),
  getClient("AUTH-46", "auth46-h", "after create: healthy with two tools, syncing every 5s, with an active headers credential", [clientState("healthy"), toolNamesAre(["add_tool", "echo_tool"]), SYNC_5S, credKind("headers"), credStatus("active")]),
  request("AUTH-46", "auth46 adds extra_tool", "POST", "/control/auth46/tools/add", { base: CHAOS, noAuth: true, body: { tool: { name: "extra_tool" } }, asserts: [A.status(200)], extraTests: ["pm.collectionVariables.set('auth46_t0', Date.now());"] }),
  getClient("AUTH-46", "auth46-h", "the client lists extra_tool within 20s and stays healthy", [toolNamesAre(AUTH46_TOOLS), seenWithin("auth46-h", 20), clientState("healthy")], { pre: pollClientTools("auth46-h", 3, 20, "auth46_t0") }),
  deleteClient("AUTH-46", "auth46-h"),
]);

// ================================================================ per-user header credentials, flows and the sessions list

const HDR_FLOWS = "/api/mcp/per-user-headers/flows/";
// hdrSetup re-adds fleet server p requiring x-key: good, creates per_user_headers client <p>-h on it (keys as given,
// sample x-key good, plus extra sample values), and a VK granted every tool of it, kept in <p>_vk1.
const hdrSetup = (id, p, keys = ["x-key"], sample = {}) => [
  ...freshServer(id, p, ["echo_tool", "add_tool"]),
  mockAuth(id, p, `setup: ${p} requires x-key: good`, { require_headers: { "x-key": "good" } }),
  createClient(id, HEADERS_BASE(`${p}-h`, `${p}_h`, { connection_string: `${CHAOS}/s/${p}/mcp`, per_user_header_keys: keys, user_headers: { "x-key": "good", ...sample }, tools_to_execute: ["*"] }), "setup: create the per_user_headers client"),
  vkCreate(id, "setup: create vk1, granted every tool of it", { name: `${id} ${p} vk1`, mcp_configs: [{ mcp_client_name: `${p}_h`, tools_to_execute: ["*"] }] }, `${p}_vk1`),
];
const HDR_TEXT = (p, keys = ["x-key"]) => `admin credentials; fleet server ${p} (echo_tool, add_tool) requires x-key: good; ${p}_h, a per_user_headers client on it with per_user_header_keys ${lit(keys)}, tools_to_execute ["*"]; vk1, a VK granted every tool of ${p}_h`;
const hdrTeardown = (id, p) => [deleteClient(id, `${p}-h`), vkDelete(id, "teardown: delete vk1", `${p}_vk1`)];
// hdrFlow calls <p>_h's echo_tool with vk1 and keeps the header flow it opens in v.
const hdrFlow = (id, title, p, v) => execTool(id, title, `${p}_h-echo_tool`, [A.status(400), headersFlow({ saveAs: v })], vkExec(`${p}_vk1`));
const hdrSubmit = (id, title, v, headers, asserts, extraTests = []) => request(id, title, "PUT", `${HDR_FLOWS}{{${v}}}`, { body: { headers }, asserts, extraTests });
const keepCredential = (v) => `pm.collectionVariables.set(${lit(v)}, pm.response.json().credential_id);`;
const sessionsNone = (id, title, clientId) => request(id, title, "GET", SESSIONS, { asserts: [A.status(200), A.custom(`no entry for ${clientId}`, [sessionRows(clientId, "true"), "pm.expect(found.length, 'body: ' + pm.response.text()).to.equal(0);"])] });

const ADM57 = folder("ADM-57 Adding a header key forces resubmission", `Setup: ${HDR_TEXT("adm57")}, verified with verify-headers; vk1 holds a header credential. A second client adm57k_h (keys ["x-key", "x-other"], verified with both) with its own VK credential has a key removed.`, [
  ...hdrSetup("ADM-57", "adm57"),
  verifyHeaders("ADM-57", "setup: verify-headers", "adm57-h", { "x-key": "good" }, [A.status(200)]),
  hdrFlow("ADM-57", "setup: a call with vk1 opens a header flow", "adm57", "adm57_f"),
  hdrSubmit("ADM-57", "setup: submit x-key good", "adm57_f", { "x-key": "good" }, [A.status(200)]),
  clientPut("ADM-57", "add key x-extra", "adm57-h", { per_user_header_keys: ["x-key", "x-extra"] }, [A.status(200)]),
  getClient("ADM-57", "adm57-h", "the client needs reauth and its credential needs an update", [clientState("needs_reauth"), credStatus("needs_update")]),
  sessionsHas("ADM-57", "vk1's sessions entry needs an update", "adm57-h", "s.kind === 'header' && s.status === 'needs_update'", "one header entry, needs_update"),
  ...hdrSetup("ADM-57", "adm57k", ["x-key", "x-other"], { "x-other": "o1" }),
  verifyHeaders("ADM-57", "setup: verify-headers on adm57k_h", "adm57k-h", { "x-key": "good", "x-other": "o1" }, [A.status(200)]),
  hdrFlow("ADM-57", "setup: a call with adm57k's vk1 opens a header flow", "adm57k", "adm57k_f"),
  hdrSubmit("ADM-57", "setup: submit both keys", "adm57k_f", { "x-key": "good", "x-other": "o1" }, [A.status(200)]),
  clientPut("ADM-57", "remove key x-other only", "adm57k-h", { per_user_header_keys: ["x-key"] }, [A.status(200)]),
  getClient("ADM-57", "adm57k-h", "its credential stays active", [credStatus("active")]),
  sessionsHas("ADM-57", "its sessions entry stays active", "adm57k-h", "s.kind === 'header' && s.status === 'active'", "one header entry, active"),
  ...hdrTeardown("ADM-57", "adm57k"),
  ...hdrTeardown("ADM-57", "adm57"),
]);

const ADM69 = folder("ADM-69 Per-user header flow detail", `Setup: ${HDR_TEXT("adm69")}, verified with verify-headers; a call with vk1 opens a header flow.`, [
  ...hdrSetup("ADM-69", "adm69"),
  verifyHeaders("ADM-69", "setup: verify-headers", "adm69-h", { "x-key": "good" }, [A.status(200)]),
  hdrFlow("ADM-69", "setup: a call with vk1 opens a header flow", "adm69", "adm69_f"),
  request("ADM-69", "flow detail", "GET", `${HDR_FLOWS}{{adm69_f}}`, { asserts: [A.status(200), A.custom("flow_mode vk, pending, client adm69-h, virtual key vk1, required_header_keys [\"x-key\"], no active credential", [
    "var b = pm.response.json();",
    "pm.expect(b.flow_mode, 'body: ' + pm.response.text()).to.equal('vk');",
    "pm.expect(b.status).to.equal('pending');",
    "pm.expect(b.mcp_client && b.mcp_client.client_id).to.equal('adm69-h');",
    "pm.expect(b.virtual_key && b.virtual_key.id).to.equal(pm.collectionVariables.get('adm69_vk1_id'));",
    "pm.expect(b.required_header_keys).to.eql(['x-key']);",
    "pm.expect(b.has_active_credential).to.equal(false);",
  ])] }),
  request("ADM-69", "an unknown flow", "GET", `${HDR_FLOWS}adm69-nosuch`, { asserts: [A.status(404), A.msgEquals("Headers submission flow not found")] }),
  ...hdrTeardown("ADM-69", "adm69"),
]);

const ADM70 = folder("ADM-70 Per-user header flow submit", `Setup: ${HDR_TEXT("adm70")}, verified with verify-headers; a call with vk1 opens a header flow.`, [
  ...hdrSetup("ADM-70", "adm70"),
  verifyHeaders("ADM-70", "setup: verify-headers", "adm70-h", { "x-key": "good" }, [A.status(200)]),
  hdrFlow("ADM-70", "setup: a call with vk1 opens a header flow", "adm70", "adm70_f"),
  hdrSubmit("ADM-70", "submit no values", "adm70_f", {}, [A.status(400), A.msgEquals("missing values for required keys: x-key")]),
  hdrSubmit("ADM-70", "submit x-key bad", "adm70_f", { "x-key": "bad" }, [A.status(422)]),
  hdrSubmit("ADM-70", "submit x-key good plus x-other", "adm70_f", { "x-key": "good", "x-other": "o1" }, [A.status(200), A.custom("credential_id is a non-empty string", ["pm.expect(pm.response.json().credential_id, 'body: ' + pm.response.text()).to.be.a('string').and.not.equal('');"])]),
  request("ADM-70", "the flow is gone", "GET", `${HDR_FLOWS}{{adm70_f}}`, { asserts: [A.status(404)] }),
  sessionsHas("ADM-70", "vk1's sessions entry is active", "adm70-h", "s.kind === 'header' && s.status === 'active' && s.virtual_key && s.virtual_key.id === pm.collectionVariables.get('adm70_vk1_id')", "one header entry for vk1, active"),
  ...hdrTeardown("ADM-70", "adm70"),
]);

const ADM71 = folder("ADM-71 Resubmission merges stored values", `Setup: ${HDR_TEXT("adm71")}, verified with verify-headers; vk1 submits x-key good plus x-other, as in ADM-70; key x-extra is then added and a call with vk1 opens a second flow.`, [
  ...hdrSetup("ADM-71", "adm71"),
  verifyHeaders("ADM-71", "setup: verify-headers", "adm71-h", { "x-key": "good" }, [A.status(200)]),
  hdrFlow("ADM-71", "setup: a call with vk1 opens a header flow", "adm71", "adm71_f"),
  hdrSubmit("ADM-71", "setup: submit x-key good plus x-other", "adm71_f", { "x-key": "good", "x-other": "o1" }, [A.status(200)]),
  clientPut("ADM-71", "setup: add key x-extra", "adm71-h", { per_user_header_keys: ["x-key", "x-extra"] }, [A.status(200)]),
  hdrFlow("ADM-71", "setup: a call with vk1 opens a second flow", "adm71", "adm71_f2"),
  hdrSubmit("ADM-71", "submit only x-extra", "adm71_f2", { "x-extra": "e1" }, [A.status(200)]),
  sessionsHas("ADM-71", "the sessions entry is active", "adm71-h", "s.kind === 'header' && s.status === 'active'", "one header entry, active"),
  mockPost("ADM-71", "adm71", "counters/reset", "setup: reset adm71's counters", {}),
  execTool("ADM-71", "a call with vk1", "adm71_h-echo_tool", [A.status(200)], vkExec("adm71_vk1")),
  mockGet("ADM-71", "adm71", "adm71 ran echo_tool once", [callsIs("echo_tool", 1)]),
  ...hdrTeardown("ADM-71", "adm71"),
]);

const ADM72 = folder("ADM-72 Revoke a header credential", `Setup: ${HDR_TEXT("adm72")}, verified with verify-headers; vk1 submits x-key good plus x-other, as in ADM-70; key x-extra is then added and a call with vk1 opens a second flow, left pending.`, [
  ...hdrSetup("ADM-72", "adm72"),
  verifyHeaders("ADM-72", "setup: verify-headers", "adm72-h", { "x-key": "good" }, [A.status(200)]),
  hdrFlow("ADM-72", "setup: a call with vk1 opens a header flow", "adm72", "adm72_f"),
  hdrSubmit("ADM-72", "setup: submit x-key good plus x-other", "adm72_f", { "x-key": "good", "x-other": "o1" }, [A.status(200), A.custom("credential_id is set", ["pm.expect(pm.response.json().credential_id, 'body: ' + pm.response.text()).to.be.a('string').and.not.empty;"])], [keepCredential("adm72_cred")]),
  clientPut("ADM-72", "setup: add key x-extra", "adm72-h", { per_user_header_keys: ["x-key", "x-extra"] }, [A.status(200)]),
  hdrFlow("ADM-72", "setup: a call with vk1 opens a second flow", "adm72", "adm72_f2"),
  sessionsHas("ADM-72", "control: the header entry needs an update", "adm72-h", "s.kind === 'header' && s.status === 'needs_update'", "one header entry, needs_update"),
  request("ADM-72", "control: the second flow is open", "GET", `${HDR_FLOWS}{{adm72_f2}}`, { asserts: [A.status(200)] }),
  request("ADM-72", "revoke the credential", "DELETE", "/api/mcp/per-user-headers/credential/{{adm72_cred}}", { asserts: [A.status(204), A.custom("the body is empty", ["pm.expect(pm.response.text()).to.equal('');"])] }),
  sessionsNone("ADM-72", "no sessions entry for the client", "adm72-h"),
  request("ADM-72", "the pending second flow is gone", "GET", `${HDR_FLOWS}{{adm72_f2}}`, { asserts: [A.status(404)] }),
  request("ADM-72", "revoke again", "DELETE", "/api/mcp/per-user-headers/credential/{{adm72_cred}}", { asserts: [A.status(404), A.msgEquals("credential not found")] }),
  ...hdrTeardown("ADM-72", "adm72"),
]);

const ADM60 = folder("ADM-60 Delete cascades credentials, flows and OAuth config", `Setup: ${PU_TEXT(false)}; ${PU_SERVER_TEXT("adm60")}, verified as admin; adm60-sess1 completes a flow as alice and adm60-sess2 leaves one pending. ${HDR_TEXT("adm60h")}; vk1 holds a header credential and session adm60-hsess2 leaves a header flow pending.`, [
  ...puConfig("ADM-60", false),
  ...puConnect("ADM-60", "adm60"),
  execTool("ADM-60", "setup: execute as adm60-sess1 opens the first flow", "adm60-echo_tool", [A.status(400), keepFlow("adm60_fa")], { session: "adm60-sess1" }),
  ...puFinish("ADM-60", "adm60_fa", "alice", "the first flow"),
  execTool("ADM-60", "setup: execute as adm60-sess2 opens a second flow, left pending", "adm60-echo_tool", [A.status(400), keepFlow("adm60_fb")], { session: "adm60-sess2" }),
  ...hdrSetup("ADM-60", "adm60h"),
  hdrFlow("ADM-60", "setup: a call with vk1 opens a header flow", "adm60h", "adm60h_f"),
  hdrSubmit("ADM-60", "setup: submit x-key good", "adm60h_f", { "x-key": "good" }, [A.status(200)]),
  execTool("ADM-60", "setup: a call as session adm60-hsess2 opens a header flow, left pending", "adm60h_h-echo_tool", [A.status(400), headersFlow({ saveAs: "adm60h_fb" })], { session: "adm60-hsess2" }),
  oauthStatus("ADM-60", "control: the per_user_oauth client's OAuth config exists", "adm60", "", []),
  flowDetail("ADM-60", "control: the second flow is open", "adm60_fb", [A.status(200)]),
  request("ADM-60", "control: the pending header flow is open", "GET", `${HDR_FLOWS}{{adm60h_fb}}`, { asserts: [A.status(200)] }),
  request("ADM-60", "control: both clients have sessions entries", "GET", SESSIONS, { asserts: [A.status(200), A.custom("entries for adm60-u and adm60h-h", [sessionRows("adm60-u", "true"), "pm.expect(found.length, 'body: ' + pm.response.text()).to.be.above(0);", sessionRows("adm60h-h", "true").replace("var found", "var foundH"), "pm.expect(foundH.length).to.be.above(0);"])] }),
  request("ADM-60", "delete the per_user_oauth client", "DELETE", "/api/mcp/client/adm60-u", { asserts: [A.status(200)] }),
  request("ADM-60", "delete the per_user_headers client", "DELETE", "/api/mcp/client/adm60h-h", { asserts: [A.status(200)] }),
  sessionsNone("ADM-60", "no sessions entry for the per_user_oauth client", "adm60-u"),
  sessionsNone("ADM-60", "no sessions entry for the per_user_headers client", "adm60h-h"),
  request("ADM-60", "its OAuth config status", "GET", "/api/oauth/config/{{adm60_oc}}/status", { asserts: [A.status(404)] }),
  flowDetail("ADM-60", "the completed first flow", "adm60_fa", [A.status(404)]),
  flowDetail("ADM-60", "the second flow", "adm60_fb", [A.status(404)]),
  request("ADM-60", "the pending header flow", "GET", `${HDR_FLOWS}{{adm60h_fb}}`, { asserts: [A.status(404)] }),
  vkDelete("ADM-60", "teardown: delete vk1", "adm60h_vk1"),
  restoreConfig("ADM-60"),
]);

// sessionsOfClients keeps the sessions entries of the given clients as `found`.
const sessionsOfClients = (clientIds) => `var found = (pm.response.json().sessions || []).filter(function (s) { return s.mcp_client && ${lit(clientIds)}.indexOf(s.mcp_client.client_id) !== -1; });`;
const ADM74 = folder("ADM-74 Sessions entry shapes and filters", `Setup: ${PU_TEXT(false)}; ${PU_SERVER_TEXT("adm74")}, verified as admin; a VK granted every tool of adm74 completes a flow as alice; session adm74-sessp leaves a flow pending. ${HDR_TEXT("adm74h")}, verified with verify-headers; vk1 holds a header credential.`, [
  ...puConfig("ADM-74", false),
  ...puConnect("ADM-74", "adm74"),
  puVK("ADM-74", "token", "adm74", "adm74_vk"),
  execTool("ADM-74", "setup: a call with the VK opens the token flow", "adm74-echo_tool", [A.status(400), keepFlow("adm74_ft")], vkExec("adm74_vk")),
  ...puFinish("ADM-74", "adm74_ft", "alice", "the token flow"),
  execTool("ADM-74", "setup: execute as adm74-sessp opens the pending flow", "adm74-echo_tool", [A.status(400), keepFlow("adm74_fp")], { session: "adm74-sessp" }),
  ...hdrSetup("ADM-74", "adm74h"),
  verifyHeaders("ADM-74", "setup: verify-headers", "adm74h-h", { "x-key": "good" }, [A.status(200)]),
  hdrFlow("ADM-74", "setup: a call with vk1 opens a header flow", "adm74h", "adm74h_f"),
  hdrSubmit("ADM-74", "setup: submit x-key good", "adm74h_f", { "x-key": "good" }, [A.status(200)]),
  request("ADM-74", "the sessions list", "GET", SESSIONS, { asserts: [A.status(200),
    A.custom("the VK's token entry: kind token, auth_kind oauth, can_reauth true", [sessionsOfClients(["adm74-u"]), "var t = found.filter(function (s) { return s.kind === 'token'; });", "pm.expect(t.length, 'body: ' + pm.response.text()).to.equal(1);", "pm.expect(t[0].auth_kind).to.equal('oauth');", "pm.expect(t[0].can_reauth).to.equal(true);", "pm.expect(t[0].virtual_key && t[0].virtual_key.id).to.equal(pm.collectionVariables.get('adm74_vk_id'));"]),
    A.custom("the header credential's entry: kind header", [sessionsOfClients(["adm74h-h"]), "pm.expect(found.length, 'body: ' + pm.response.text()).to.equal(1);", "pm.expect(found[0].kind).to.equal('header');"]),
    A.custom("the pending flow's entry: kind flow, can_reauth false", [sessionsOfClients(["adm74-u"]), "var f = found.filter(function (s) { return s.kind === 'flow'; });", "pm.expect(f.length, 'body: ' + pm.response.text()).to.equal(1);", "pm.expect(f[0].id).to.equal(pm.collectionVariables.get('adm74_fp'));", "pm.expect(f[0].can_reauth).to.equal(false);"]),
    A.custom("no admin entries", [sessionsOfClients(["adm74-u", "adm74h-h"]), "pm.expect(found.filter(function (s) { return s.auth_mode === 'admin'; }), 'body: ' + pm.response.text()).to.eql([]);", "pm.expect(found.length).to.equal(3);"]),
  ] }),
  request("ADM-74", "?kind=token", "GET", `${SESSIONS}&kind=token`, { asserts: [A.status(200), A.custom("only OAuth entries: the VK's token and the pending flow, no header entry", ["var s = pm.response.json().sessions || [];", "pm.expect(s.filter(function (x) { return x.auth_kind !== 'oauth'; }), 'body: ' + pm.response.text()).to.eql([]);", sessionsOfClients(["adm74-u", "adm74h-h"]), "pm.expect(found.map(function (x) { return x.kind; }).sort()).to.eql(['flow', 'token']);"])] }),
  request("ADM-74", "?kind=header", "GET", `${SESSIONS}&kind=header`, { asserts: [A.status(200), A.custom("only header entries, including the header credential", ["var s = pm.response.json().sessions || [];", "pm.expect(s.filter(function (x) { return x.auth_kind !== 'headers'; }), 'body: ' + pm.response.text()).to.eql([]);", sessionsOfClients(["adm74-u", "adm74h-h"]), "pm.expect(found.map(function (x) { return x.mcp_client.client_id + ':' + x.kind; })).to.eql(['adm74h-h:header']);"])] }),
  request("ADM-74", "?status=pending", "GET", `${SESSIONS}&status=pending`, { asserts: [A.status(200), A.custom("only pending entries, including the pending flow", ["var s = pm.response.json().sessions || [];", "pm.expect(s.filter(function (x) { return x.status !== 'pending'; }), 'body: ' + pm.response.text()).to.eql([]);", "pm.expect(s.map(function (x) { return x.id; })).to.include(pm.collectionVariables.get('adm74_fp'));"])] }),
  ...hdrTeardown("ADM-74", "adm74h"),
  deleteClient("ADM-74", "adm74-u"),
  vkDelete("ADM-74", "teardown: delete the VK", "adm74_vk"),
  restoreConfig("ADM-74"),
]);

// ================================================================ more upstream auth and connection tests

// TEST_CA_PEM is a throwaway self-signed certificate; tests only check that reads mask it.
const TEST_CA_PEM = "-----BEGIN CERTIFICATE-----\nMIIBkTCCATegAwIBAgIUZROfp1FT/MGjiBFcTf3zhEaKgNMwCgYIKoZIzj0EAwIw\nHjEcMBoGA1UEAwwTbWNwLWhhcm5lc3MtdGVzdC1jYTAeFw0yNjEwMjUyMzA5MDJa\nFw0zNjEwMjIyMzA5MDJaMB4xHDAaBgNVBAMME21jcC1oYXJuZXNzLXRlc3QtY2Ew\nWTATBgcqhkjOPQIBBggqhkjOPQMBBwNCAARbckzlpb3VR09T8jXCPvEDRctwmlBi\ngzkZWU2PqXcLKCMyMEr1s+ij+FfctsklaHvpxlYPhym9I+CQrLbFKhJSo1MwUTAd\nBgNVHQ4EFgQUJNqZG3I02VD1qGgtlL235Z7OGtQwHwYDVR0jBBgwFoAUJNqZG3I0\n2VD1qGgtlL235Z7OGtQwDwYDVR0TAQH/BAUwAwEB/zAKBggqhkjOPQQDAgNIADBF\nAiEAkwBWmRIr8IrZTC+kDv/6BqiadwMu2RjKXQBRKz6VCnECIFPD3vap9hqdyfUT\nkAv7V2nFBQtLErA6WH/+5ADXZrHY\n-----END CERTIFICATE-----\n";

// noRawSecrets asserts the response text contains none of the given raw secret values.
const noRawSecrets = (secrets) => A.custom("no raw secret anywhere in the body", [`${lit(secrets)}.forEach(function (s) { pm.expect(pm.response.text().indexOf(s), 'raw secret in body: ' + s).to.equal(-1); });`]);
const AUTH08_KEY = "abcdefghijklmnopqrstuvwxyz125678";
const AUTH08_OAUTH_ID = "auth08-preregistered-client-id";
const AUTH08_OAUTH_SECRET = "auth08-preregistered-client-secret";
const AUTH08_OAS_SECRET = "auth08-openapi-credential-value";
const AUTH08 = folder("AUTH-08 Secrets are masked on read", `Setup: admin credentials; a headers client on alpha with X-Api-Key ${AUTH08_KEY} (32 characters), X-Short abcde and a TLS CA certificate; ${ALLOWLIST_TEXT}; a fleet server auth08 as an OAuth protected resource whose authorization server knows client ${AUTH08_OAUTH_ID} with secret ${AUTH08_OAUTH_SECRET}, and a completed OAuth client on it with those credentials; an OpenAPI client from a Petstore document with an API key scheme and the credential ${AUTH08_OAS_SECRET}.`, [
  ...freshServer("AUTH-08", "alpha", ["echo_tool", "add_tool"]),
  createClient("AUTH-08", httpClient("auth08-h", "auth08_h", "alpha", { auth_type: "headers", headers: { "X-Api-Key": AUTH08_KEY, "X-Short": "abcde" }, tls_config: { ca_cert_pem: TEST_CA_PEM }, tools_to_execute: ["*"] }), "setup: create the headers client"),
  ...allowlistOn("AUTH-08"),
  ...asServer("AUTH-08", "auth08", { clients: [{ client_id: AUTH08_OAUTH_ID, client_secret: AUTH08_OAUTH_SECRET }] }),
  ...connectOAuth("AUTH-08", oauthBody("auth08-o", "auth08_o", "auth08", { oauth_config: { client_id: AUTH08_OAUTH_ID, client_secret: AUTH08_OAUTH_SECRET, authorize_url: `${CHAOS}/s/auth08/as/authorize`, token_url: `${CHAOS}/s/auth08/as/token` } }), "auth08o", null, "the OAuth client"),
  createClient("AUTH-08", oasClient("auth08-q", "auth08_q", "alpha", {}, { spec: PETS_KEY, security_credentials: { KeyAuth: { value: AUTH08_OAS_SECRET } } }), "setup: create the OpenAPI client"),
  getClient("AUTH-08", "auth08-h", "header values are masked", [A.custom("X-Api-Key is abcd, 24 asterisks and 5678; X-Short is *****", [
    "var h = pm.response.json().clients[0].config.headers;",
    "pm.expect(h['X-Api-Key'].value, 'body: ' + pm.response.text()).to.equal('abcd' + new Array(25).join('*') + '5678');",
    "pm.expect(h['X-Short'].value).to.equal('*****');",
  ])]),
  getClient("AUTH-08", "auth08-q", "no OpenAPI spec in the body; the credential is masked", [A.custom("openapi_config has no spec, and KeyAuth is abcd-style masked", ["var oc = pm.response.json().clients[0].config.openapi_config;", "pm.expect(oc, 'body: ' + pm.response.text()).to.not.have.property('spec');", "pm.expect(oc.security_credentials.KeyAuth.value.value).to.match(/^.{4}\\*{24}.{4}$/);"])]),
  getClient("AUTH-08", "auth08-o", "the OAuth client id and secret are masked", [A.custom("oauth_client_id and oauth_client_secret are four characters, 24 asterisks and four characters", ["var c = pm.response.json().clients[0].config;", "pm.expect(c.oauth_client_id && c.oauth_client_id.value, 'body: ' + pm.response.text()).to.match(/^.{4}\\*{24}.{4}$/);", "pm.expect(c.oauth_client_secret && c.oauth_client_secret.value).to.match(/^.{4}\\*{24}.{4}$/);"])]),
  listClients("AUTH-08", "the full client list", "search=auth08", [A.custom("three clients", ["pm.expect(pm.response.json().clients.length, 'body: ' + pm.response.text()).to.equal(3);"]), noRawSecrets([AUTH08_KEY, "abcde", AUTH08_OAUTH_ID, AUTH08_OAUTH_SECRET, AUTH08_OAS_SECRET, TEST_CA_PEM.split("\n")[1]])]),
  deleteClient("AUTH-08", "auth08-q"),
  deleteClient("AUTH-08", "auth08-o"),
  deleteClient("AUTH-08", "auth08-h"),
  restoreConfig("AUTH-08"),
]);

const AUTH10 = folder("AUTH-10 A wildcard lets the caller's credential through", `Setup: ${T1_TEXT("alpha")}, and maps bearer CALLER to identity caller; ${T1_CLIENT_TEXT}, per call, allowed_extra_headers ["*"], tools_to_execute ["*"]; the caller sends "Authorization: Bearer CALLER".`, [
  ...freshServer("AUTH-10", "alpha", ["echo_tool", "add_tool"]),
  mockAuth("AUTH-10", "alpha", "setup: alpha requires a bearer, mapping T1 to id1 and CALLER to caller", { bearer: { [T1]: "id1", CALLER: "caller" }, require: true }),
  createClient("AUTH-10", t1Client("auth10-c", "auth10_c", { allowed_extra_headers: ["*"] }), "setup: create the client"),
  execTool("AUTH-10", "execute with Authorization: Bearer CALLER", "auth10_c-echo_tool", [A.status(200)], { headers: [{ key: "Authorization", value: "Bearer CALLER" }] }),
  mockGet("AUTH-10", "alpha", "the call is counted under the caller's identity", [identityCallsAre({ caller: 1 })]),
  deleteClient("AUTH-10", "auth10-c"),
]);

const AUTH40 = folder("AUTH-40 Auth-required with extra fields in headers", `Setup: ${KEY_A_TEXT}; a per_user_headers client on alpha with per_user_header_keys ["x-api-key"], sample key-a, tools_to_execute ["*"]; extra_fields_placement "headers". The caller sends x-bf-mcp-session-id auth40-sess1 and has no credential.`, [
  readConfig("AUTH-40"),
  putConfig("AUTH-40", "setup: extra_fields_placement headers", ["cc.extra_fields_placement = 'headers';"], [A.status(200)]),
  ...alphaKeyA("AUTH-40"),
  createClient("AUTH-40", perUser("auth40-c", "auth40_c", PER_USER_KEY_A), "setup: create the per_user_headers client"),
  execTool("AUTH-40", "execute as auth40-sess1", "auth40_c-echo_tool", [A.status(400), A.custom("no extra_fields in the body; error.message has the submission URL", [
    "var b = pm.response.json();",
    "pm.expect(b, 'body: ' + pm.response.text()).to.not.have.property('extra_fields');",
    "pm.expect(b.error.message).to.include('/workspace/mcp-sessions/auth?flow=');",
  ])], { session: "auth40-sess1" }),
  deleteClient("AUTH-40", "auth40-c"),
  restoreConfig("AUTH-40"),
]);

const credentialDelete = (id, title, v, asserts) => request(id, title, "DELETE", `/api/mcp/per-user-headers/credential/{{${v}}}`, { asserts });
const AUTH44 = folder("AUTH-44 Isolation, revoke and a new required key", `Setup: alpha (echo_tool, add_tool) counts calls under the X-Api-Key value; a per_user_headers client on alpha with per_user_header_keys ["x-api-key"], tools_to_execute ["*"]; session auth44-sess1 submits key-1 and auth44-sess2 submits key-2.`, [
  ...freshServer("AUTH-44", "alpha", ["echo_tool", "add_tool"]),
  mockAuth("AUTH-44", "alpha", "setup: alpha counts calls under X-Api-Key", { identity_header: "X-Api-Key" }),
  createClient("AUTH-44", perUser("auth44-c", "auth44_c", { per_user_header_keys: ["x-api-key"], user_headers: { "x-api-key": "sample" } }), "setup: create the per_user_headers client"),
  execTool("AUTH-44", "setup: execute as auth44-sess1 opens a header flow", "auth44_c-echo_tool", [A.status(400), headersFlow({ saveAs: "auth44_fa" })], { session: "auth44-sess1" }),
  request("AUTH-44", "setup: auth44-sess1 submits key-1", "PUT", "/api/mcp/per-user-headers/flows/{{auth44_fa}}", { body: { headers: { "x-api-key": "key-1" } }, asserts: [A.status(200)], extraTests: ["pm.collectionVariables.set('auth44_ca', pm.response.json().credential_id);"] }),
  execTool("AUTH-44", "setup: execute as auth44-sess2 opens a header flow", "auth44_c-echo_tool", [A.status(400), headersFlow({ saveAs: "auth44_fb" })], { session: "auth44-sess2" }),
  request("AUTH-44", "setup: auth44-sess2 submits key-2", "PUT", "/api/mcp/per-user-headers/flows/{{auth44_fb}}", { body: { headers: { "x-api-key": "key-2" } }, asserts: [A.status(200)] }),
  execTool("AUTH-44", "execute as auth44-sess1", "auth44_c-echo_tool", [A.status(200)], { session: "auth44-sess1" }),
  execTool("AUTH-44", "execute as auth44-sess2", "auth44_c-echo_tool", [A.status(200)], { session: "auth44-sess2" }),
  mockGet("AUTH-44", "alpha", "each call is counted under its own key", [identityCallsAre({ "key-1": 1, "key-2": 1 })]),
  credentialDelete("AUTH-44", "delete auth44-sess1's credential", "auth44_ca", [A.status(204)]),
  execTool("AUTH-44", "execute as auth44-sess1 after the delete", "auth44_c-echo_tool", [A.status(400), headersFlow({})], { session: "auth44-sess1" }),
  execTool("AUTH-44", "execute as auth44-sess2 after the delete", "auth44_c-echo_tool", [A.status(200)], { session: "auth44-sess2" }),
  clientPut("AUTH-44", "add key x-org-id", "auth44-c", { per_user_header_keys: ["x-api-key", "x-org-id"] }, [A.status(200)]),
  execTool("AUTH-44", "execute as auth44-sess2 after the new key", "auth44_c-echo_tool", [A.status(400), headersFlow({ saveAs: "auth44_fb2" })], { session: "auth44-sess2" }),
  request("AUTH-44", "auth44-sess2's new flow", "GET", "/api/mcp/per-user-headers/flows/{{auth44_fb2}}", { asserts: [A.status(200), A.custom("submitted_keys [\"x-api-key\"]", ["pm.expect(pm.response.json().submitted_keys, 'body: ' + pm.response.text()).to.eql(['x-api-key']);"])] }),
  deleteClient("AUTH-44", "auth44-c"),
]);

const execRaw = (id, title, body, asserts, query = "") => request(id, title, "POST", `/v1/mcp/tool/execute${query}`, { noAuth: true, noCookies: true, raw: typeof body === "string" ? body : JSON.stringify(body), asserts });
const CONN05 = folder("CONN-05 Execute validation", "Setup: alpha (echo_tool, add_tool); a sticky client conn05_c on alpha, auth none, tools_to_execute [\"echo_tool\"].", [
  ...freshServer("CONN-05", "alpha", ["echo_tool", "add_tool"]),
  createClient("CONN-05", httpClient("conn05-c", "conn05_c", "alpha", { needs_session_stickiness: true, tools_to_execute: ["echo_tool"] }), "setup: create the sticky client"),
  execRaw("CONN-05", "?format=xml", { id: "c1", type: "function", function: { name: "conn05_c-echo_tool", arguments: "{}" } }, [A.status(400)], "?format=xml"),
  execRaw("CONN-05", "no function.name", { id: "c1", type: "function", function: { arguments: "{}" } }, [A.status(400), A.msgEquals("Tool function name is required")]),
  execRaw("CONN-05", "arguments \"not json\"", { id: "c1", type: "function", function: { name: "conn05_c-echo_tool", arguments: "not json" } }, [A.status(400), A.msgContains("failed to parse tool arguments for")]),
  execTool("CONN-05", "an unknown tool", "conn05_c-nope", [A.status(400)]),
  execTool("CONN-05", "add_tool, outside tools_to_execute", "conn05_c-add_tool", [A.status(400), A.msgContains("not in client's ToolsToExecute allow-list")]),
  deleteClient("CONN-05", "conn05-c"),
]);

const contentIs = (v) => A.custom(`content is ${lit(v)}`, [`pm.expect(pm.response.json().content, 'body: ' + pm.response.text()).to.equal(${lit(v)});`]);
const PNG_REPLY = { content: [{ type: "image", data: "QUJD", mimeType: "image/png" }] };
const CONN07 = folder("CONN-07 Result content types on execute", "Setup: alpha serves texts (two text blocks \"a\" and \"b\"), image (a PNG), structured (structuredContent {\"v\": 1} and no content) and empty (content []); a client conn07_c on alpha, auth none, tools_to_execute [\"*\"]. Joining text blocks with a separator and returning structuredContent alone are a known issue, so those checks are skipped.", [
  request("CONN-07", "setup: fresh mcpchaos server alpha", "POST", "/control", { base: CHAOS, noAuth: true, body: { add: "alpha" }, asserts: [A.status(200)] }),
  mockPost("CONN-07", "alpha", "tools/set", "setup: alpha serves texts, image, structured and empty", { tools: [
    { name: "texts", reply: { content: [{ type: "text", text: "a" }, { type: "text", text: "b" }] } },
    { name: "image", reply: PNG_REPLY },
    { name: "structured", reply: { content: [], structuredContent: { v: 1 } } },
    { name: "empty", reply: { content: [] } },
  ] }),
  createClient("CONN-07", httpClient("conn07-c", "conn07_c", "alpha", { tools_to_execute: ["*"] }), "setup: create the client"),
  execTool("CONN-07", "texts", "conn07_c-texts", [A.status(200), A.skip(A.custom("the two blocks are joined with a separator", ["var c = pm.response.json().content;", "pm.expect(c, 'body: ' + pm.response.text()).to.match(/^a[\\s\\S]+b$/);"]))]),
  execTool("CONN-07", "image", "conn07_c-image", [A.status(200), contentIs("[Image Response: QUJD, MIME: image/png]")]),
  execTool("CONN-07", "structured", "conn07_c-structured", [A.status(200), A.skip(A.custom("the structuredContent comes back", ["pm.expect(pm.response.json().content, 'body: ' + pm.response.text()).to.include('\"v\"');"]))]),
  execTool("CONN-07", "empty", "conn07_c-empty", [A.status(200), contentIs("MCP tool 'conn07_c-empty' executed successfully")]),
  deleteClient("CONN-07", "conn07-c"),
]);

const CONN11 = folder("CONN-11 Per-call handshake and the tool timeout", "Setup: alpha (echo_tool, add_tool); a per-call client conn11_c on alpha, auth none, tool_execution_timeout 2, tools_to_execute [\"*\"]; alpha then hangs on initialize only. Each initialize attempt has its own 30s bound and failed attempts are retried, so the call fails after 30s or more.", [
  ...freshServer("CONN-11", "alpha", ["echo_tool", "add_tool"]),
  createClient("CONN-11", httpClient("conn11-c", "conn11_c", "alpha", { tool_execution_timeout: 2, tools_to_execute: ["*"] }), "setup: create the per-call client"),
  mockPost("CONN-11", "alpha", "faults", "setup: alpha hangs on initialize for 60s", { faults: { hang: "60s", methods: ["initialize"] } }),
  execTool("CONN-11", "execute", "conn11_c-echo_tool", [A.status(400), A.msgContains("failed to establish ephemeral MCP connection"), responseTime(30000, 120000)]),
  mockPost("CONN-11", "alpha", "faults/clear", "teardown: clear alpha's faults", {}),
  deleteClient("CONN-11", "conn11-c"),
]);

const CONN16 = folder("CONN-16 Tool list changes", "Setup: alpha (echo_tool, add_tool); a sticky client conn16_c on alpha, auth none, tools_to_execute [\"*\"]. Then alpha adds new_tool and pushes list_changed; the client is refreshed; alpha removes echo_tool; the client is refreshed again.", [
  ...freshServer("CONN-16", "alpha", ["echo_tool", "add_tool"]),
  createClient("CONN-16", httpClient("conn16-c", "conn16_c", "alpha", { needs_session_stickiness: true, tools_to_execute: ["*"] }), "setup: create the sticky client"),
  mockPost("CONN-16", "alpha", "tools/add", "alpha adds new_tool", { tool: { name: "new_tool" } }),
  mockPost("CONN-16", "alpha", "notify/tools-changed", "alpha pushes list_changed", {}),
  getClient("CONN-16", "conn16-c", "the client's tools are unchanged", [toolNamesAre(["add_tool", "echo_tool"])]),
  mockGet("CONN-16", "alpha", "alpha has no open stream", [counterIs("open_streams", 0)]),
  clientPost("CONN-16", "refresh-tools", "conn16-c", "refresh-tools", [A.status(200), A.jsonEquals("tool_count", 3)]),
  execTool("CONN-16", "execute conn16_c-new_tool", "conn16_c-new_tool", [A.status(200)]),
  mockPost("CONN-16", "alpha", "tools/remove", "alpha removes echo_tool", { name: "echo_tool" }),
  clientPost("CONN-16", "refresh-tools again", "conn16-c", "refresh-tools", [A.status(200)]),
  execTool("CONN-16", "execute conn16_c-echo_tool", "conn16_c-echo_tool", [A.status(400), A.msgContains("is not available or not permitted")]),
  deleteClient("CONN-16", "conn16-c"),
]);

// ================================================================ more Bifrost MCP endpoint tests

const fullVK = (id) => vkHeader(id, "full");
// gwTextAssert reads a tools/call result's first text block as `text` and runs lines on it.
const gwText = (title, lines) => A.custom(title, ["var r = pm.response.json().result;", "pm.expect(r, 'body: ' + pm.response.text()).to.be.an('object');", "var text = (r.content && r.content[0] && r.content[0].text) || '';", ...lines]);

const EP21 = folder("EP-21 Tool call timeout", `Setup: ${FLEET}; client alpha has tool_execution_timeout 2; alpha delays every tools/call by 5s (scoped to tools/call, so the per-call handshake is fast); the full-access VK (${VKS.full.text}).`, [
  ...fleetSetup("EP-21"), createVK("EP-21", "full"),
  request("EP-21", "setup: tool_execution_timeout 2 on alpha", "PUT", "/api/mcp/client/ep-21-alpha", { body: { tool_execution_timeout: 2 }, asserts: [A.status(200)] }),
  mockPost("EP-21", "alpha", "faults", "setup: alpha delays tools/call by 5s", { faults: { latency: "5s", methods: ["tools/call"] } }),
  gwCall("EP-21", "tools/call alpha-echo", "alpha-echo", [toolResultError(null, "MCP tool call timed out after 2s: alpha-echo"), responseTime(null, 4000)], [fullVK("EP-21")]),
  mockPost("EP-21", "alpha", "faults/clear", "teardown: clear alpha's faults", {}),
  deleteVK("EP-21", "full"), ...fleetTeardown("EP-21"),
]);

const EP22 = folder("EP-22 Non-text and empty results", `Setup: ${FLEET}, with alpha's echo replying (a) two text blocks "a" and "b", a PNG image block and structuredContent {"v": 1}, then (b) content []; the full-access VK. Passing the content blocks through is a known issue, so those checks are skipped.`, [
  ...fleetSetup("EP-22"), createVK("EP-22", "full"),
  mockPost("EP-22", "alpha", "tools/set", "setup: alpha's echo replies two text blocks, an image and structuredContent", { tools: [{ name: "echo", reply: { content: [{ type: "text", text: "a" }, { type: "text", text: "b" }, { type: "image", data: "QUJD", mimeType: "image/png" }], structuredContent: { v: 1 } } }, { name: "add" }] }),
  request("EP-22", "setup: refresh alpha's tools", "POST", "/api/mcp/client/ep-22-alpha/refresh-tools", { asserts: [A.status(200)] }),
  gwCall("EP-22", "tools/call alpha-echo, mixed content", "alpha-echo", [A.skip(A.custom("two text blocks, the image block and structuredContent pass through", [
    "var r = pm.response.json().result;",
    "pm.expect(r.content, 'body: ' + pm.response.text()).to.eql([{ type: 'text', text: 'a' }, { type: 'text', text: 'b' }, { type: 'image', data: 'QUJD', mimeType: 'image/png' }]);",
    "pm.expect(r.structuredContent).to.eql({ v: 1 });",
  ]))], [fullVK("EP-22")]),
  mockPost("EP-22", "alpha", "tools/set", "setup: alpha's echo replies content []", { tools: [{ name: "echo", reply: { content: [] } }, { name: "add" }] }),
  gwCall("EP-22", "tools/call alpha-echo, empty content", "alpha-echo", [gwText("the text is \"MCP tool 'alpha-echo' executed successfully\"", ["pm.expect(text, 'body: ' + pm.response.text()).to.equal(\"MCP tool 'alpha-echo' executed successfully\");"])], [fullVK("EP-22")]),
  deleteVK("EP-22", "full"), ...fleetTeardown("EP-22"),
]);

const EP30 = folder("EP-30 Malformed JSON-RPC and batches", `Setup: ${FLEET}; no credential (enforce_auth_on_inference off). A batch of two tools/list requests gets -32700.`, [
  ...fleetSetup("EP-30"),
  rpc("EP-30", "body \"{\"", "{", { asserts: [A.status(200), rpcError(-32700, "Failed to parse message"), A.custom("id is null", ["pm.expect(pm.response.json().id, 'body: ' + pm.response.text()).to.equal(null);"])] }),
  rpc("EP-30", "an empty body", "", { asserts: [A.status(200), rpcError(-32700, "Failed to parse message"), A.custom("id is null", ["pm.expect(pm.response.json().id, 'body: ' + pm.response.text()).to.equal(null);"])] }),
  rpc("EP-30", "jsonrpc \"1.0\"", { jsonrpc: "1.0", id: 1, method: "tools/list" }, { asserts: [A.status(200), rpcError(-32600, "Invalid JSON-RPC version")] }),
  rpc("EP-30", "a batch of two tools/list requests", JSON.stringify([listMsg, { ...listMsg, id: 2 }]), { asserts: [A.status(200), rpcError(-32700)] }),
  ...fleetTeardown("EP-30"),
]);

const EP35 = folder("EP-35 `/mcp/{client slug}` serves one client", `Setup: ${FLEET}; plus client my_alpha on alpha, auth none, tools_to_execute ["*"]; no credential (enforce_auth_on_inference off).`, [
  ...fleetSetup("EP-35"),
  createClient("EP-35", httpClient("ep-35-my", "my_alpha", "alpha", { tools_to_execute: ["*"] }), "setup: register client my_alpha"),
  rpc("EP-35", "/mcp/alpha tools/list", listMsg, { path: "/mcp/alpha", asserts: [A.status(200), toolNamesInList(["alpha-add", "alpha-echo"])] }),
  rpc("EP-35", "/mcp/my-alpha tools/list", listMsg, { path: "/mcp/my-alpha", asserts: [A.status(200), toolNamesInList(["my_alpha-add", "my_alpha-echo"])] }),
  rpc("EP-35", "beta-echo through /mcp/alpha", call("beta-echo", { text: "hi" }), { path: "/mcp/alpha", asserts: [A.status(200), toolResultError(null, "filtered by request context")] }),
  mockGet("EP-35", "beta", "beta received no call", [A.custom("calls is empty", ["pm.expect(pm.response.json().counters.calls, 'body: ' + pm.response.text()).to.eql({});"])]),
  deleteClient("EP-35", "ep-35-my"),
  ...fleetTeardown("EP-35"),
]);

// modeReadsBack checks GET /api/config reports mode and, outside headers, issuer_url set to the base URL.
const modeReadsBack = (id, mode) => request(id, `${mode}: /api/config reads back mcp_server_auth_mode ${mode}`, "GET", "/api/config", {
  asserts: [A.status(200), A.custom(`mcp_server_auth_mode is ${lit(mode)}${mode === "headers" ? "" : ", issuer_url is the base URL"}`, [
    "var cc = pm.response.json().client_config || {};",
    `pm.expect(cc.mcp_server_auth_mode, 'body: ' + pm.response.text()).to.equal(${lit(mode)});`,
    ...(mode === "headers" ? [] : [
      "var iss = (cc.oauth2_server_config || {}).issuer_url;",
      "pm.expect(iss !== null && typeof iss === 'object' ? iss.value : iss, 'oauth2_server_config: ' + JSON.stringify(cc.oauth2_server_config)).to.equal(pm.variables.get('base_url'));",
    ]),
  ])],
});
const EP50 = folder("EP-50 Switching `mcp_server_auth_mode` at runtime", `Setup: ${GRANT_TEXT}; ${BOTH}, temp-token auth on, enforce_auth_on_inference off; a grant gives an access token for the full-access VK. The mode then switches to oauth, headers and back to both with no restart.`, [
  ...fleetGrantSetup("EP-50"), oauthConfig("EP-50", "both"),
  register("EP-50", "ep50_client", LOOPBACK_CB, undefined, REFRESHABLE),
  ...vkGrant("EP-50", "ep50_client", "ep50_f", "the grant"),
  authMode("EP-50", "O", false),
  modeReadsBack("EP-50", "oauth"),
  rpc("EP-50", "oauth: tools/list with x-bf-vk", listMsg, { headers: [MCP_ACCEPT, fullVK("EP-50")], asserts: [A.status(401)] }),
  mcpWithToken("EP-50", "oauth: tools/list with the access token", "ep50_f_at", [A.status(200)]),
  authMode("EP-50", "H", false),
  modeReadsBack("EP-50", "headers"),
  mcpWithToken("EP-50", "headers: tools/list with the access token is anonymous", "ep50_f_at", [A.status(200), toolNamesInList(ALL_TOOLS)]),
  authMode("EP-50", "H", true),
  mcpWithToken("EP-50", "headers, enforcement on: the access token is no credential", "ep50_f_at", [A.status(401), A.msgContains("virtual key required to access mcp server")]),
  authMode("EP-50", "H", false),
  request("EP-50", "headers: /.well-known/jwks.json", "GET", "/.well-known/jwks.json", { noAuth: true, asserts: [A.status(404)] }),
  request("EP-50", "headers: /oauth2/register", "POST", "/oauth2/register", { noAuth: true, body: { redirect_uris: [LOOPBACK_CB] }, asserts: [A.status(404)] }),
  request("EP-50", "headers: /oauth2/authorize", "GET", goodAuthorize("ep50_client"), { noAuth: true, noRedirect: true, asserts: [A.status(404)] }),
  request("EP-50", "headers: /.well-known/oauth-authorization-server", "GET", "/.well-known/oauth-authorization-server", { noAuth: true, asserts: [A.status(404)] }),
  request("EP-50", "headers: /.well-known/oauth-protected-resource/mcp", "GET", "/.well-known/oauth-protected-resource/mcp", { noAuth: true, asserts: [A.status(404)] }),
  tokenReq("EP-50", "headers: /oauth2/token", refreshParams("ep50_f_rt", "ep50_client"), [A.status(404)]),
  putConfig("EP-50", "setup: headers, issuer_url cleared", ["cc.mcp_server_auth_mode = 'headers';", "cc.oauth2_server_config = { issuer_url: '' };"], [A.status(200)]),
  request("EP-50", "headers: /api/config reads back no issuer_url", "GET", "/api/config", { asserts: [A.status(200), A.custom("mcp_server_auth_mode is \"headers\" and issuer_url is empty", [
    "var cc = pm.response.json().client_config || {};",
    "pm.expect(cc.mcp_server_auth_mode, 'body: ' + pm.response.text()).to.equal('headers');",
    "var iss = (cc.oauth2_server_config || {}).issuer_url;",
    "pm.expect((iss !== null && typeof iss === 'object' ? iss.value : iss) || '', 'oauth2_server_config: ' + JSON.stringify(cc.oauth2_server_config)).to.equal('');",
  ])] }),
  authMode("EP-50", "B", false),
  mcpWithToken("EP-50", "both: tools/list with the access token", "ep50_f_at", [A.status(200)]),
  modeReadsBack("EP-50", "both"),
  request("EP-50", "both: /.well-known/oauth-protected-resource is served again", "GET", "/.well-known/oauth-protected-resource", { noAuth: true, asserts: [A.status(200)] }),
  rpc("EP-50", "both: tools/list with x-bf-vk", listMsg, { headers: [MCP_ACCEPT, fullVK("EP-50")], asserts: [A.status(200), toolNamesInList(ALL_TOOLS)] }),
  authMode("EP-50", "B", true),
  rpc("EP-50", "both, enforcement on: tools/list with x-bf-vk", listMsg, { headers: [MCP_ACCEPT, fullVK("EP-50")], asserts: [A.status(200), toolNamesInList(ALL_TOOLS)] }),
  rpc("EP-50", "both, enforcement on: control: tools/list with no credential", listMsg, { headers: [MCP_ACCEPT], asserts: [A.status(401), wwwPresent()] }),
  ...fleetGrantTeardown("EP-50"),
]);

// EP51_AUTH_REQUIRED is the tool result EP-51 expects in every mode: the auth-required link for ph's headers.
const EP51_AUTH_REQUIRED = [toolResultError("Authentication required for ph. Open this URL to submit the required headers: "), gwText("the URL has #t= and the fragment reminder follows it", [
    "pm.expect(text, 'text: ' + text).to.include('#t=');",
    "var notice = text.indexOf('IMPORTANT: this link includes a required fragment');",
    "pm.expect(notice, 'text: ' + text).to.be.above(text.indexOf('#t='));",
    `var rem = ${JSON.stringify(" IMPORTANT: this link includes a required fragment after the '#' character (a one-time auth token). Copy and share the ENTIRE URL exactly as given, including everything after the '#' — the link will not work without it.")};`,
    "pm.expect(text.slice(-rem.length), 'text: ' + text).to.equal(rem);",
  ])];
const EP51 = folder("EP-51 Per-user headers tool through `/mcp`", `Setup: ${FLEET}; a fleet server ph (echo_tool, add_tool) that requires X-Token, and a per_user_headers client ph on it with per_user_header_keys ["x-token"], tools_to_execute ["*"]; a VK granted every tool of alpha, beta and ph; temp-token auth on. The caller has no X-Token credential. The call is made with the VK in mcp_server_auth_mode headers and both, and with a token from a grant consented with the VK in both and oauth (enforce_auth_on_inference off).`, [
  ...fleetSetup("EP-51"),
  readConfig("EP-51"),
  putConfig("EP-51", "setup: temp-token auth on", ["cc.mcp_enable_temp_token_auth = true;"], [A.status(200)]),
  ...freshServer("EP-51", "ph", ["echo_tool", "add_tool"]),
  mockAuth("EP-51", "ph", "setup: ph requires X-Token good", { require_headers: { "X-Token": "good" } }),
  createClient("EP-51", HEADERS_BASE("ep-51-ph", "ph", { connection_string: `${CHAOS}/s/ph/mcp`, per_user_header_keys: ["x-token"], user_headers: { "x-token": "good" }, tools_to_execute: ["*"] }), "setup: create the per_user_headers client ph"),
  vkCreate("EP-51", "setup: create the VK, granted alpha, beta and ph", { name: "EP-51 key", mcp_configs: [{ mcp_client_name: "alpha", tools_to_execute: ["*"] }, { mcp_client_name: "beta", tools_to_execute: ["*"] }, { mcp_client_name: "ph", tools_to_execute: ["*"] }] }, "ep51_vk"),
  gwCall("EP-51", "tools/call ph-echo_tool with the VK", "ph-echo_tool", EP51_AUTH_REQUIRED, [{ key: "x-bf-vk", value: "{{ep51_vk}}" }]),
  authMode("EP-51", "B", false),
  gwCall("EP-51", "both: tools/call ph-echo_tool with the VK", "ph-echo_tool", EP51_AUTH_REQUIRED, [{ key: "x-bf-vk", value: "{{ep51_vk}}" }]),
  register("EP-51", "ep51_client"),
  ...grantWith("EP-51", "ep51_client", "ep51_g", "ep51_vk"),
  gwCall("EP-51", "both: tools/call ph-echo_tool with the VK's token", "ph-echo_tool", EP51_AUTH_REQUIRED, [bearer("{{ep51_g_at}}")]),
  authMode("EP-51", "O", false),
  gwCall("EP-51", "oauth: tools/call ph-echo_tool with the VK's token", "ph-echo_tool", EP51_AUTH_REQUIRED, [bearer("{{ep51_g_at}}")]),
  mockGet("EP-51", "ph", "ph received no call", [A.custom("calls is empty", ["pm.expect(pm.response.json().counters.calls, 'body: ' + pm.response.text()).to.eql({});"])]),
  deleteClient("EP-51", "ep-51-ph"),
  vkDelete("EP-51", "teardown: delete the VK", "ep51_vk"),
  restoreConfig("EP-51"),
  ...fleetTeardown("EP-51"),
]);

// EP52_AUTH_REQUIRED is the tool result EP-52 expects in every mode: Bifrost's link for po's per-user OAuth.
const EP52_AUTH_REQUIRED = [toolResultError("Authentication required for po. Open this URL to connect your account: "), gwText("Bifrost's /workspace/mcp-sessions/auth?flow= link follows, not the authorization server's URL", [
    "var rest = text.slice('Authentication required for po. Open this URL to connect your account: '.length);",
    "var prefix = pm.variables.get('base_url') + '/workspace/mcp-sessions/auth?flow=';",
    "pm.expect(rest.indexOf(prefix), 'text: ' + text).to.equal(0);",
    "pm.expect(/^[0-9a-f-]{36}$/.test(rest.slice(prefix.length)), 'text: ' + text).to.equal(true);",
  ])];
// ep52Mode keeps EP-52's per-user OAuth settings while it switches mcp_server_auth_mode; temp tokens are on only for the grant.
const ep52Mode = (m, tt) => putConfig("EP-52", `setup: ${PU_TEXT(tt)}, mcp_server_auth_mode ${MODE_NAMES[m]} with issuer_url set to the base URL, enforce_auth_on_inference off`, [
  "cc.mcp_oauth_private_allowlist = ['127.0.0.1'];", `cc.mcp_enable_temp_token_auth = ${tt};`, `cc.mcp_server_auth_mode = ${lit(MODE_NAMES[m])};`,
  "cc.oauth2_server_config = { issuer_url: pm.variables.get('base_url') };", "cc.enforce_auth_on_inference = false;",
], [A.status(200)]);
const EP52 = folder("EP-52 Per-user OAuth tool through `/mcp`", `Setup: ${FLEET}; ${PU_TEXT(false)}; ${PU_SERVER_TEXT("po")}, verified as admin; a VK granted every tool of alpha, beta and po, with no po token. The call is made with the VK in mcp_server_auth_mode headers and both, and with a token from a grant consented with the VK in both and oauth (enforce_auth_on_inference off; temp-token auth on only for the grant).`, [
  ...fleetSetup("EP-52"),
  ...puConfig("EP-52", false),
  ...puConnect("EP-52", "po"),
  vkCreate("EP-52", "setup: create the VK, granted alpha, beta and po", { name: "EP-52 key", mcp_configs: [{ mcp_client_name: "alpha", tools_to_execute: ["*"] }, { mcp_client_name: "beta", tools_to_execute: ["*"] }, { mcp_client_name: "po", tools_to_execute: ["*"] }] }, "ep52_vk"),
  mockPost("EP-52", "po", "counters/reset", "setup: reset po's counters", {}),
  gwCall("EP-52", "tools/call po-echo_tool with the VK", "po-echo_tool", EP52_AUTH_REQUIRED, [{ key: "x-bf-vk", value: "{{ep52_vk}}" }]),
  ep52Mode("B", false),
  gwCall("EP-52", "both: tools/call po-echo_tool with the VK", "po-echo_tool", EP52_AUTH_REQUIRED, [{ key: "x-bf-vk", value: "{{ep52_vk}}" }]),
  ep52Mode("B", true),
  register("EP-52", "ep52_client"),
  ...grantWith("EP-52", "ep52_client", "ep52_g", "ep52_vk"),
  ep52Mode("B", false),
  gwCall("EP-52", "both: tools/call po-echo_tool with the VK's token", "po-echo_tool", EP52_AUTH_REQUIRED, [bearer("{{ep52_g_at}}")]),
  ep52Mode("O", false),
  gwCall("EP-52", "oauth: tools/call po-echo_tool with the VK's token", "po-echo_tool", EP52_AUTH_REQUIRED, [bearer("{{ep52_g_at}}")]),
  mockGet("EP-52", "po", "po received no call", [A.custom("calls is empty", ["pm.expect(pm.response.json().counters.calls || {}, 'body: ' + pm.response.text()).to.eql({});"])]),
  deleteClient("EP-52", "po-u"),
  vkDelete("EP-52", "teardown: delete the VK", "ep52_vk"),
  restoreConfig("EP-52"),
  ...fleetTeardown("EP-52"),
]);

// ================================================================ the setup lock, Host spoofing and per-client settings

// ADMIN_PAIR reads the admin username and password back out of admin_auth_header (Basic) as `user` and `pass`.
const ADMIN_PAIR = "var C = require('crypto-js'); var pair = C.enc.Base64.parse(pm.variables.get('admin_auth_header').replace(/^Basic /, '')).toString(C.enc.Utf8); var user = pair.slice(0, pair.indexOf(':')), pass = pair.slice(pair.indexOf(':') + 1);";
// authBody reads the live config (with the given header) and keeps a PUT body that sets auth_config to authFields,
// coercing log_retention_days to at least 1 as a save requires.
const authBody = (header, authFields) => [
  ADMIN_PAIR,
  `pm.sendRequest({ url: pm.variables.get('base_url') + '/api/config', method: 'GET', header: ${header} }, function (err, res) { var cc = res.json().client_config; if (!(cc.log_retention_days >= 1)) { cc.log_retention_days = 1; } pm.variables.set('auth_body', JSON.stringify({ client_config: cc, auth_config: ${authFields} })); });`,
];
// authOff turns dashboard auth off, which puts every /api route behind the setup token.
const authOff = (id) => request(id, "setup: turn dashboard auth off, so the setup lock applies", "PUT", "/api/config", {
  raw: "{{auth_body}}", asserts: [A.status(200)],
  pre: authBody("{ Authorization: pm.variables.get('admin_auth_header') }", "{ is_enabled: false, admin_username: user, admin_password: '<redacted>' }"),
});
// authOn turns dashboard auth back on under the setup lock, proving control with the stored admin password.
const authOn = (id) => request(id, "teardown: turn dashboard auth back on", "PUT", "/api/config", {
  noAuth: true, raw: "{{auth_body}}", headers: [{ key: "X-Bifrost-Setup-Token", value: "{{setup_token}}" }], asserts: [A.status(200)],
  pre: authBody("{ 'X-Bifrost-Setup-Token': pm.variables.get('setup_token') }", "{ is_enabled: true, admin_username: user, admin_password: pass, current_password: pass }"),
});
const SETUP_TOKEN = { key: "X-Bifrost-Setup-Token", value: "{{setup_token}}" };
const WRONG_TOKEN = { key: "X-Bifrost-Setup-Token", value: "mcp-harness-wrong-token" };
const LOCK_TEXT = "dashboard auth turned off for the test, with the runner's setup token configured, and turned back on at the end";

const OAS17 = folder("OAS-17 Consent under the setup lock", `Setup: ${FLEET}; the full-access VK; ${BOTH}, temp-token auth on, enforce_auth_on_inference off; a registered OAuth client authorizes, giving a consent flow and its temp token; then ${LOCK_TEXT}.`, [
  ...fleetGrantSetup("OAS-17"), oauthConfig("OAS-17", "both", ["cc.enforce_auth_on_inference = false;"], "setup: mcp_server_auth_mode both, issuer_url set to the base URL, temp-token auth on, enforce_auth_on_inference off"),
  register("OAS-17", "oas17_client"),
  authorize("OAS-17", "oas17_client", "oas17_f", "setup: authorize"),
  authOff("OAS-17"),
  request("OAS-17", "the flow with only its temp token", "GET", flowPath("oas17_f"), { noAuth: true, headers: [{ key: "X-Bifrost-Temp-Token", value: "{{oas17_f_tt}}" }], asserts: [A.status(401), A.msgEquals("dashboard auth is not configured; send the setup token in the X-Bifrost-Setup-Token header")] }),
  request("OAS-17", "the flow with a wrong setup token", "GET", flowPath("oas17_f"), { noAuth: true, headers: [{ key: "X-Bifrost-Temp-Token", value: "{{oas17_f_tt}}" }, WRONG_TOKEN], asserts: [A.status(403)] }),
  request("OAS-17", "the flow with the setup token", "GET", flowPath("oas17_f"), { noAuth: true, headers: [{ key: "X-Bifrost-Temp-Token", value: "{{oas17_f_tt}}" }, SETUP_TOKEN], asserts: [A.status(200), A.custom("available_modes [\"vk\",\"session\"]", ["pm.expect(pm.response.json().available_modes, 'body: ' + pm.response.text()).to.eql(['vk', 'session']);"])] }),
  authOn("OAS-17"),
  ...fleetGrantTeardown("OAS-17"),
]);

const OAS33 = folder("OAS-33 Sessions API needs admin", `Setup: dashboard auth on and no credential; then ${LOCK_TEXT}.`, [
  request("OAS-33", "GET /api/oauth2/sessions with no credential", "GET", "/api/oauth2/sessions", { noAuth: true, noCookies: true, asserts: [A.status(401)] }),
  authOff("OAS-33"),
  request("OAS-33", "GET /api/oauth2/sessions under the setup lock with no token", "GET", "/api/oauth2/sessions", { noAuth: true, noCookies: true, asserts: [A.status(401), A.msgContains("send the setup token in the X-Bifrost-Setup-Token header")] }),
  authOn("OAS-33"),
]);

// hostIs sends a request with Host evil.example; noEvil asserts nothing in the response names it.
const EVIL = { key: "Host", value: "evil.example" };
const noEvil = () => A.custom("no body field, Location or WWW-Authenticate contains evil.example", [
  "pm.expect(pm.response.text(), 'body').to.not.include('evil.example');",
  "pm.expect(pm.response.headers.get('Location') || '').to.not.include('evil.example');",
  "pm.expect(pm.response.headers.get('WWW-Authenticate') || '').to.not.include('evil.example');",
]);
const OAS05 = folder("OAS-05 Spoofed Host", `Setup: ${FLEET}; ${BOTH}, temp-token auth on, enforce_auth_on_inference on (so /mcp answers 401 with WWW-Authenticate); a registered OAuth client. Requests send Host evil.example; they run in both and then again in oauth.`, [
  readConfig("OAS-05"), ...fleetSetup("OAS-05"),
  authMode("OAS-05", "B", true),
  register("OAS-05", "oas05_client"),
  ...["B", "O"].flatMap((m) => {
    const p = m === "O" ? "oauth: " : "";
    return [
      ...(m === "O" ? [authMode("OAS-05", m, true)] : []),
      request("OAS-05", `${p}authorization server metadata`, "GET", "/.well-known/oauth-authorization-server", { noAuth: true, headers: [EVIL], asserts: [A.status(200), noEvil(), A.custom("issuer is the base URL", ["pm.expect(pm.response.json().issuer, 'body: ' + pm.response.text()).to.equal(pm.variables.get('base_url'));"])] }),
      request("OAS-05", `${p}protected resource metadata`, "GET", "/.well-known/oauth-protected-resource/mcp", { noAuth: true, headers: [EVIL], asserts: [A.status(200), noEvil(), A.custom("resource is the base URL plus /mcp", ["pm.expect(pm.response.json().resource, 'body: ' + pm.response.text()).to.equal(pm.variables.get('base_url') + '/mcp');"])] }),
      request("OAS-05", `${p}authorize`, "GET", goodAuthorize("oas05_client"), { noAuth: true, noRedirect: true, headers: [EVIL], asserts: [A.status(302), noEvil(), A.custom("the consent Location starts with the issuer", ["pm.expect((pm.response.headers.get('Location') || '').indexOf(pm.variables.get('base_url')), 'Location: ' + pm.response.headers.get('Location')).to.equal(0);"])] }),
      rpc("OAS-05", `${p}/mcp tools/list with no credential`, listMsg, { headers: [MCP_ACCEPT, EVIL], asserts: [A.status(401), noEvil(), A.custom("WWW-Authenticate points at the base URL's resource metadata", ["pm.expect(pm.response.headers.get('WWW-Authenticate'), 'headers').to.equal('Bearer resource_metadata=\"' + pm.variables.get('base_url') + '/.well-known/oauth-protected-resource/mcp\"');"])] }),
    ];
  }),
  restoreConfig("OAS-05"), ...fleetTeardown("OAS-05"),
]);

// OAS-42 takes one client through issuance with mcp_server_auth_mode oauth, where a token is the only way into /mcp.
const OAS42 = folder("OAS-42 Issuance in oauth mode", `Setup: ${GRANT_TEXT}; mcp_server_auth_mode oauth with issuer_url set to the base URL, temp-token auth on, enforce_auth_on_inference off.`, [
  ...fleetGrantSetup("OAS-42"), oauthConfig("OAS-42", "oauth"),
  list("OAS-42", "control: the full-access VK in x-bf-vk is refused", "/mcp", [vkHeader("OAS-42", "full")], [A.status(401), A.msgEquals(OAUTH_MODE_MSG)]),
  request("OAS-42", "register", "POST", "/oauth2/register", {
    noAuth: true, body: { client_name: "OAS-42 client", redirect_uris: [LOOPBACK_CB], grant_types: REFRESHABLE }, asserts: [A.status(201)],
    extraTests: ["pm.collectionVariables.set('oas42_client', pm.response.json().client_id);"],
  }),
  authorize("OAS-42", "oas42_client", "oas42_f", "authorize"),
  consent("OAS-42", "oas42_f", "consent with the full-access VK through the temp token", "temp", vkBody("OAS-42"), [A.status(200), codeIssued()], { extraTests: saveCode("oas42_f") }),
  tokenReq("OAS-42", "exchange the code", codeParams("oas42_f", "oas42_client"), [A.status(200), tokensIssued(), sessionBfMode("vk")], { extraTests: saveTokens("oas42_f") }),
  mcpWithToken("OAS-42", "the access token lists every tool", "oas42_f_at", [A.status(200), toolNamesInList(ALL_TOOLS)]),
  tokenReq("OAS-42", "refresh", refreshParams("oas42_f_rt", "oas42_client"), [A.status(200), tokensIssued()], { extraTests: saveTokens("oas42_r") }),
  mcpWithToken("OAS-42", "the refreshed access token lists every tool", "oas42_r_at", [A.status(200), toolNamesInList(ALL_TOOLS)]),
  tokenReq("OAS-42", "replay the first refresh token", refreshParams("oas42_f_rt", "oas42_client"), [A.status(400), O.descEquals("refresh token not found or revoked")]),
  tokenReq("OAS-42", "the second refresh token after the replay", refreshParams("oas42_r_rt", "oas42_client"), [A.status(400), O.descEquals("refresh token not found or revoked")]),
  ...fleetGrantTeardown("OAS-42"),
]);

// AUTH-49 and AUTH-50: a VK outranks x-bf-mcp-session-id as the per-user identity, and a session id longer than 255
// characters is no identity at all. SID_255 is exactly 255 characters; one more makes it too long.
const SID_255 = "sid-" + "s".repeat(251);
const noAuthRequired = () => A.custom("there is no mcp_auth_required", ["pm.expect((pm.response.json().extra_fields || {}).mcp_auth_required, 'body: ' + pm.response.text()).to.equal(undefined);"]);
const AUTH49_MSG = "per-user OAuth for auth49 requires an identity: send a Virtual Key (x-bf-vk)";
const AUTH49_VK = { key: "x-bf-vk", value: "{{auth49_vk}}" };
const AUTH49 = folder("AUTH-49 Per-user OAuth identity: VK over session id, session id length", `Setup: ${PU_TEXT(false)}; ${PU_SERVER_TEXT("auth49")}, verified as admin; a VK granted every tool of auth49. The caller sends the VK and x-bf-mcp-session-id auth49-sess1 and completes the flow as alice; then auth49-sess1 alone, and session ids of 255 and 256 characters.`, [
  ...puConfig("AUTH-49", false),
  ...puConnect("AUTH-49", "auth49"),
  puVK("AUTH-49", "caller's", "auth49", "auth49_vk"),
  execTool("AUTH-49", "execute with the VK and auth49-sess1", "auth49-echo_tool", [A.status(400), keepFlow("auth49_f1")], { session: "auth49-sess1", headers: [AUTH49_VK] }),
  flowDetail("AUTH-49", "the flow is keyed to the VK", "auth49_f1", [A.status(200), A.jsonEquals("flow_mode", "vk"), A.custom("virtual_key is the caller's VK and there is no session_id", [
    "var b = pm.response.json();",
    "pm.expect(b.virtual_key && b.virtual_key.id, 'body: ' + pm.response.text()).to.equal(pm.collectionVariables.get('auth49_vk_id'));",
    "pm.expect(b, 'body: ' + pm.response.text()).to.not.have.property('session_id');",
  ])]),
  ...puFinish("AUTH-49", "auth49_f1", "alice", "the flow"),
  execTool("AUTH-49", "execute with the VK alone", "auth49-echo_tool", [A.status(200)], vkExec("auth49_vk")),
  execTool("AUTH-49", "execute with the VK and auth49-sess1", "auth49-echo_tool", [A.status(200)], { session: "auth49-sess1", headers: [AUTH49_VK] }),
  mockGet("AUTH-49", "auth49", "both calls ran under alice", [identityCallsAre({ alice: 2 })]),
  execTool("AUTH-49", "execute with auth49-sess1 alone", "auth49-echo_tool", [A.status(400), keepFlow("auth49_f2", "auth49_f1")], { session: "auth49-sess1" }),
  flowDetail("AUTH-49", "that flow is keyed to auth49-sess1", "auth49_f2", [A.status(200), A.jsonEquals("flow_mode", "session"), A.jsonEquals("session_id", "auth49-sess1")]),
  execTool("AUTH-49", "execute with a 255-character session id", "auth49-echo_tool", [A.status(400), keepFlow("auth49_f3")], { session: SID_255 }),
  flowDetail("AUTH-49", "that flow is keyed to the 255-character session id", "auth49_f3", [A.status(200), A.jsonEquals("flow_mode", "session"), A.jsonEquals("session_id", SID_255)]),
  execTool("AUTH-49", "execute with a 256-character session id", "auth49-echo_tool", [A.status(400), A.msgStartsWith(AUTH49_MSG), noAuthRequired()], { session: SID_255 + "s" }),
  gwCall("AUTH-49", "/mcp tools/call with a 256-character session id", "auth49-echo_tool", [toolResultError(null, AUTH49_MSG)], [{ key: "x-bf-mcp-session-id", value: SID_255 + "s" }]),
  mockGet("AUTH-49", "auth49", "no other call reached auth49", [identityCallsAre({ alice: 2 })]),
  deleteClient("AUTH-49", "auth49-u"),
  vkDelete("AUTH-49", "teardown: delete the VK", "auth49_vk"),
  restoreConfig("AUTH-49"),
]);

const AUTH50_MSG = "per-user headers for auth50_c requires an identity: send a Virtual Key (x-bf-vk)";
const AUTH50_VK = { key: "x-bf-vk", value: "{{auth50_vk}}" };
const AUTH50 = folder("AUTH-50 Per-user headers identity: none, VK over session id, session id length", `Setup: ${KEY_A_TEXT}; a per_user_headers client auth50_c on alpha with per_user_header_keys ["x-api-key"], sample key-a, tools_to_execute ["*"]; a VK granted every tool of auth50_c. A caller with no identity; then the VK with x-bf-mcp-session-id auth50-sess1, which submits key-a; then auth50-sess1 alone, and a 256-character session id.`, [
  ...alphaKeyA("AUTH-50"),
  createClient("AUTH-50", perUser("auth50-c", "auth50_c", PER_USER_KEY_A), "setup: create the per_user_headers client"),
  vkCreate("AUTH-50", "setup: create the VK, granted every tool of auth50_c", { name: "AUTH-50 key", mcp_configs: [{ mcp_client_name: "auth50_c", tools_to_execute: ["*"] }] }, "auth50_vk"),
  execTool("AUTH-50", "execute with no VK and no session id", "auth50_c-echo_tool", [A.status(400), A.msgStartsWith(AUTH50_MSG), noAuthRequired()]),
  gwCall("AUTH-50", "/mcp tools/call with no VK and no session id", "auth50_c-echo_tool", [toolResultError(null, AUTH50_MSG)]),
  execTool("AUTH-50", "execute with the VK and auth50-sess1", "auth50_c-echo_tool", [A.status(400), headersFlow({ saveAs: "auth50_h1" })], { session: "auth50-sess1", headers: [AUTH50_VK] }),
  request("AUTH-50", "the header flow is keyed to the VK", "GET", `${HDR_FLOWS}{{auth50_h1}}`, { asserts: [A.status(200), A.custom("flow_mode vk, virtual key is the caller's VK, no session_id", [
    "var b = pm.response.json();",
    "pm.expect(b.flow_mode, 'body: ' + pm.response.text()).to.equal('vk');",
    "pm.expect(b.virtual_key && b.virtual_key.id).to.equal(pm.collectionVariables.get('auth50_vk_id'));",
    "pm.expect(b).to.not.have.property('session_id');",
  ])] }),
  request("AUTH-50", "setup: submit key-a", "PUT", `${HDR_FLOWS}{{auth50_h1}}`, { body: { headers: { "x-api-key": "key-a" } }, asserts: [A.status(200)] }),
  execTool("AUTH-50", "execute with the VK alone", "auth50_c-echo_tool", [A.status(200)], vkExec("auth50_vk")),
  mockGet("AUTH-50", "alpha", "the call ran under key-a", [identityCallsAre({ "key-a": 1 })]),
  execTool("AUTH-50", "execute with auth50-sess1 alone", "auth50_c-echo_tool", [A.status(400), headersFlow({ saveAs: "auth50_h2" })], { session: "auth50-sess1" }),
  request("AUTH-50", "that header flow is keyed to auth50-sess1", "GET", `${HDR_FLOWS}{{auth50_h2}}`, { asserts: [A.status(200), A.jsonEquals("flow_mode", "session"), A.jsonEquals("session_id", "auth50-sess1")] }),
  execTool("AUTH-50", "execute with a 256-character session id", "auth50_c-echo_tool", [A.status(400), A.msgStartsWith(AUTH50_MSG), noAuthRequired()], { session: SID_255 + "s" }),
  mockGet("AUTH-50", "alpha", "no other call reached alpha", [identityCallsAre({ "key-a": 1 })]),
  deleteClient("AUTH-50", "auth50-c"),
  vkDelete("AUTH-50", "teardown: delete the VK", "auth50_vk"),
]);

const SET14 = folder("SET-14 OAuth private allowlist validation", "Setup: PUT /api/config with admin credentials.", [
  readConfig("SET-14"),
  putConfig("SET-14", "mcp_oauth_private_allowlist [\"169.254.169.254\"]", ["cc.mcp_oauth_private_allowlist = ['169.254.169.254'];"], [A.status(400), A.msgContains("can never be allowlisted")]),
  putConfig("SET-14", "mcp_oauth_private_allowlist [\"0.0.0.0/0\"]", ["cc.mcp_oauth_private_allowlist = ['0.0.0.0/0'];"], [A.status(400), A.msgContains("can never be allowlisted")]),
  restoreConfig("SET-14"),
]);

const SET19 = folder("SET-19 Disabling a per-call client", "Setup: admin credentials; alpha (echo_tool, add_tool) registered as client alpha, auth none, per call, tools_to_execute [\"*\"], then disabled.", [
  ...setClient("SET-19", {}),
  request("SET-19", "setup: disable client alpha", "PUT", "/api/mcp/client/set-19-a", { body: { disabled: true }, asserts: [A.status(200)] }),
  getClient("SET-19", "set-19-a", "its tools are still listed and it is disabled", [toolNamesAre(["add_tool", "echo_tool"]), clientState("disabled")]),
  execTool("SET-19", "execute alpha-echo_tool", "alpha-echo_tool", [A.status(400), A.msgContains("is not permitted (client alpha is disabled)")]),
  request("SET-19", "refresh-tools", "POST", "/api/mcp/client/set-19-a/refresh-tools", { asserts: [A.status(400)] }),
  request("SET-19", "reconnect", "POST", "/api/mcp/client/set-19-a/reconnect", { asserts: [A.status(400), A.msgContains("cannot reconnect a disabled MCP client")] }),
  deleteClient("SET-19", "set-19-a"),
]);

const SET20 = folder("SET-20 `disabled: true` on create", "Setup: admin credentials; alpha (echo_tool, add_tool); a sticky client on alpha created with disabled true. Honouring disabled on create is a known issue, so those checks are skipped.", [
  ...freshServer("SET-20", "alpha", ["echo_tool", "add_tool"]),
  saveSessions("SET-20", "alpha", "set20_sessions"),
  createClient("SET-20", httpClient("set20-a", "set20_a", "alpha", { needs_session_stickiness: true, disabled: true, tools_to_execute: ["*"] }), "create the sticky client with disabled true"),
  getClient("SET-20", "set20-a", "it is stored and reported disabled", [A.skip(configField("disabled", true)), A.skip(clientState("disabled"))]),
  mockGet("SET-20", "alpha", "no session was opened", [A.skip(sessionsPlus("set20_sessions", 0))]),
  deleteClient("SET-20", "set20-a"),
]);

const LIB17_ROUTES = [
  ["GET", "/api/mcp/library", undefined],
  ["GET", "/api/mcp/library/filterdata", undefined],
  ["POST", "/api/mcp/library", {}],
  ["DELETE", "/api/mcp/library/1", undefined],
  ["POST", "/api/mcp/library/force-sync", undefined],
];
const LIB17 = folder("LIB-17 Library routes without credentials", `Setup: dashboard auth on and no credential; then ${LOCK_TEXT}. Every library route is called: list, filterdata, publish, delete and force-sync.`, [
  ...LIB17_ROUTES.map(([method, path, body]) => request("LIB-17", `${method} ${path} with no credential`, method, path, { body, noAuth: true, noCookies: true, asserts: [A.status(401), A.msgEquals("Unauthorized")] })),
  authOff("LIB-17"),
  ...LIB17_ROUTES.map(([method, path, body]) => request("LIB-17", `${method} ${path} under the setup lock with no token`, method, path, { body, noAuth: true, noCookies: true, asserts: [A.status(401), A.msgContains("X-Bifrost-Setup-Token")] })),
  request("LIB-17", "GET /api/mcp/library with a wrong setup token", "GET", "/api/mcp/library", { noAuth: true, noCookies: true, headers: [WRONG_TOKEN], asserts: [A.status(403), A.msgEquals("invalid setup token")] }),
  authOn("LIB-17"),
]);

const LOG39 = folder("LOG-39 Delete without valid auth", `Setup: ${LOCK_TEXT}; then dashboard auth on with no credential. Each delete names an id that does not exist.`, [
  authOff("LOG-39"),
  request("LOG-39", "DELETE /api/mcp-logs under the setup lock with no token", "DELETE", "/api/mcp-logs", { noAuth: true, noCookies: true, body: { ids: ["log39-none"] }, asserts: [A.status(401), A.msgContains("X-Bifrost-Setup-Token")] }),
  request("LOG-39", "DELETE /api/mcp-logs with a wrong setup token", "DELETE", "/api/mcp-logs", { noAuth: true, noCookies: true, body: { ids: ["log39-none"] }, headers: [WRONG_TOKEN], asserts: [A.status(403)] }),
  authOn("LOG-39"),
  request("LOG-39", "DELETE /api/mcp-logs with auth on and no credential", "DELETE", "/api/mcp-logs", { noAuth: true, noCookies: true, body: { ids: ["log39-none"] }, asserts: [A.status(401), A.msgEquals("Unauthorized")] }),
]);

const INF48 = folder("INF-48 listToolFiles, server binding", `Setup: ${CM_TEXT}; plus alpha (echo_tool, add_tool) registered as client alpha, auth none, per call, which is not a code-mode client. cm is then disabled.`, [
  ...cmSetup("INF-48"),
  ...setClient("INF-48", {}),
  meta("INF-48", "listToolFiles", "listToolFiles", {}, [A.status(200), A.custom("starts \"# Workflow: listToolFiles -> readToolFile\" and lists servers/ with cm.pyi only", [
    "var c = pm.response.json().content;",
    "pm.expect(c.indexOf('# Workflow: listToolFiles -> readToolFile'), 'content: ' + c).to.equal(0);",
    "pm.expect(c).to.include('servers/');",
    "pm.expect(c.match(/[\\w.-]+\\.pyi/g)).to.eql(['cm.pyi']);",
  ])]),
  request("INF-48", "setup: disable cm", "PUT", "/api/mcp/client/inf-48-cm", { body: { disabled: true }, asserts: [A.status(200)] }),
  meta("INF-48", "listToolFiles with cm disabled", "listToolFiles", {}, [A.status(200), A.custom("says servers are connected but none are configured for code mode", ["pm.expect(pm.response.json().content, 'body: ' + pm.response.text()).to.include('Servers are connected but none are configured for code mode.');"])]),
  deleteClient("INF-48", "inf-48-a"),
  cmTeardown("INF-48"),
]);

// ================================================================ governance budgets, rate limits, pricing and virtual MCP edits

// govKey creates a VK named "<id> key" granted mcp_configs, plus extra fields (budgets, rate_limit), kept in <v>_id and <v>.
const govKey = (id, v, mcpConfigs, extra = {}, label = "the VK") => vkCreate(id, `setup: create ${label}`, { name: `${id} key`, mcp_configs: mcpConfigs, ...extra }, v);
const ALPHA_ALL = [{ mcp_client_name: "alpha", tools_to_execute: ["*"] }];
const vkUse = (v) => ({ headers: [{ key: "x-bf-vk", value: `{{${v}}}` }] });
// vkMemory reads a VK from memory; asserts see it as `vk`.
const vkMemory = (id, title, v, asserts, extraTests = []) => request(id, title, "GET", `/api/governance/virtual-keys/{{${v}_id}}?from_memory=true`, { asserts: [A.status(200), ...asserts], extraTests });
const VK_READ = "var vk = pm.response.json().virtual_key;";
const keepBudget = (v) => [VK_READ, `pm.collectionVariables.set(${lit(v)}, String(vk.budgets[0].current_usage));`];
const keepRequests = (v) => [VK_READ, `pm.collectionVariables.set(${lit(v)}, String(vk.rate_limit.request_current_usage));`];
const budgetPlus = (v, d) => A.custom(`budget current_usage is ${d} more than recorded`, [VK_READ, `pm.expect(vk.budgets[0].current_usage, 'body: ' + pm.response.text()).to.be.closeTo(Number(pm.collectionVariables.get(${lit(v)})) + ${d}, 1e-9);`]);
const requestsPlus = (v, d) => A.custom(`request_current_usage is ${d} more than recorded`, [VK_READ, `pm.expect(vk.rate_limit.request_current_usage, 'body: ' + pm.response.text()).to.equal(Number(pm.collectionVariables.get(${lit(v)})) + ${d});`]);
// logOf polls GET /api/mcp-logs/<R> until the write lands; asserts see the entry as `e`.
const logOf = (id, title, R, asserts) => request(id, title, "GET", `/api/mcp-logs/${R}`, {
  pre: ["var left = 30;", `function poll() { pm.sendRequest({ url: pm.variables.get('base_url') + '/api/mcp-logs/${R}', method: 'GET', header: { Authorization: pm.variables.get('admin_auth_header') } }, function (err, res) { if ((!err && res.code === 200 && res.json().status !== 'processing') || --left <= 0) { return; } setTimeout(poll, 500); }); }`, "poll();"],
  asserts: [A.status(200), ...asserts],
});
const entryCost = (v) => A.custom(v === null ? "the entry has no cost" : `the entry's cost is ${v}`, ["var e = pm.response.json();", v === null ? "pm.expect(e.cost == null, 'body: ' + pm.response.text()).to.equal(true);" : `pm.expect(e.cost, 'body: ' + pm.response.text()).to.be.closeTo(${v}, 1e-9);`]);
const execR2 = (id, title, tool, R, asserts, opts = {}) => execTool(id, title, tool, asserts, { headers: [{ key: "x-request-id", value: R }, ...(opts.headers || [])] });
const priceAlpha = (id, title, clientId, pricing) => request(id, title, "PUT", `/api/mcp/client/${clientId}`, { body: { tool_pricing: pricing }, asserts: [A.status(200)] });
const deleteLogs = (id, ids) => request(id, "teardown: delete the log entries", "DELETE", "/api/mcp-logs", { body: { ids }, asserts: [A.status(200)] });
// metaWith calls a code-mode meta tool through execute with extra request options (headers).
const metaWith = (id, title, tool, args, asserts, opts) => execBody(id, title, toolCall(tool, args), asserts, opts);
const ALPHA_EA = "alpha (echo, add) registered as client alpha, auth none, per call, tools_to_execute [\"*\"]";
const alphaEA = (id, over = {}) => [...freshServer(id, "alpha", ["echo", "add"]), alphaClient(id, over)];

const GOV19 = folder("GOV-19 Code mode scoped to the VK", `Setup: ${CM_TEXT}, code mode bound per server (the default); a VK with mcp_configs cm ["echo"]. listToolFiles is also called with code mode bound per tool, where it lists one file per tool.`, [
  ...cmSetup("GOV-19"),
  govKey("GOV-19", "gov19_vk", [{ mcp_client_name: "cm", tools_to_execute: ["echo"] }]),
  metaWith("GOV-19", "listToolFiles with the VK", "listToolFiles", {}, [A.status(200), A.custom("lists cm.pyi", ["var c = pm.response.json().content;", "pm.expect(c, 'content: ' + c).to.include('cm.pyi');"])], vkUse("gov19_vk")),
  readConfig("GOV-19"),
  putConfig("GOV-19", "setup: bind code mode per tool", ["cc.mcp_code_mode_binding_level = 'tool';"], [A.status(200)]),
  metaWith("GOV-19", "listToolFiles with the VK, bound per tool", "listToolFiles", {}, [A.status(200), A.custom("the only tool file is echo.pyi", ["var c = pm.response.json().content;", "pm.expect(c.split('\\n').filter(function (l) { return /\\.pyi$/.test(l); }), 'content: ' + c).to.eql(['    echo.pyi']);"])], vkUse("gov19_vk")),
  restoreConfig("GOV-19"),
  metaWith("GOV-19", "readToolFile servers/cm.pyi with the VK", "readToolFile", { fileName: "servers/cm.pyi" }, [A.status(200), A.custom("defines echo and not add", ["var c = pm.response.json().content;", "pm.expect(c, 'content: ' + c).to.match(/def echo\\(/);", "pm.expect(c).to.not.match(/def add\\(/);"])], vkUse("gov19_vk")),
  metaWith("GOV-19", "executeToolCode calling cm.add with the VK", "executeToolCode", { code: "r = cm.add(text=\"x\")\nresult = r" }, [A.status(200), toolErrorFlag(true), A.custom("the error names no .add attribute", ["pm.expect(pm.response.json().content, 'body: ' + pm.response.text()).to.include('no .add attribute');"])], vkUse("gov19_vk")),
  mockGet("GOV-19", "cm", "cm never ran add", [callsIs("add", 0)]),
  vkDelete("GOV-19", "teardown: delete the VK", "gov19_vk"),
  cmTeardown("GOV-19"),
]);

const GOV22 = folder("GOV-22 Tool price recorded and charged", `Setup: ${ALPHA_EA}, echo priced 0.001; a VK with mcp_configs alpha ["*"] and a budget of 1 per 1h.`, [
  ...alphaEA("GOV-22"),
  priceAlpha("GOV-22", "setup: price echo at 0.001", "gov-22-alpha", { echo: 0.001 }),
  govKey("GOV-22", "gov22_vk", ALPHA_ALL, { budgets: [{ max_limit: 1, reset_duration: "1h" }] }),
  vkMemory("GOV-22", "setup: record the budget", "gov22_vk", [], keepBudget("gov22_b0")),
  execR2("GOV-22", "execute alpha-echo with the VK", "alpha-echo", "gov22-r", [A.status(200)], vkUse("gov22_vk")),
  logOf("GOV-22", "its log entry", "gov22-r", [entryCost(0.001), A.custom("virtual_key_id is the VK's", ["pm.expect(pm.response.json().virtual_key_id, 'body: ' + pm.response.text()).to.equal(pm.collectionVariables.get('gov22_vk_id'));"])]),
  vkMemory("GOV-22", "the budget rose by 0.001", "gov22_vk", [budgetPlus("gov22_b0", 0.001)]),
  deleteLogs("GOV-22", ["gov22-r"]),
  vkDelete("GOV-22", "teardown: delete the VK", "gov22_vk"),
  agentTeardown("GOV-22"),
]);

const GOV23 = folder("GOV-23 Budget exhausted by tool cost", `Setup: ${ALPHA_EA}, echo priced 0.001; a VK with mcp_configs alpha ["*"] and a budget of 0.002 per 1h.`, [
  ...alphaEA("GOV-23"),
  priceAlpha("GOV-23", "setup: price echo at 0.001", "gov-23-alpha", { echo: 0.001 }),
  govKey("GOV-23", "gov23_vk", ALPHA_ALL, { budgets: [{ max_limit: 0.002, reset_duration: "1h" }] }),
  execTool("GOV-23", "call 1", "alpha-echo", [A.status(200)], vkUse("gov23_vk")),
  execTool("GOV-23", "call 2", "alpha-echo", [A.status(200)], vkUse("gov23_vk")),
  execTool("GOV-23", "call 3", "alpha-echo", [A.status(402), A.custom("type budget_exceeded", ["pm.expect(pm.response.json().type, 'body: ' + pm.response.text()).to.equal('budget_exceeded');"]), A.msgContains("budget exceeded: 0.0020 >= 0.0020 dollars")], { headers: [{ key: "x-bf-vk", value: "{{gov23_vk}}" }] }),
  mockGet("GOV-23", "alpha", "alpha ran echo twice", [callsIs("echo", 2)]),
  vkDelete("GOV-23", "teardown: delete the VK", "gov23_vk"),
  agentTeardown("GOV-23"),
]);

const GOV24 = folder("GOV-24 Unpriced and failed calls cost nothing", `Setup: alpha (echo, add), with echo failing every call with a JSON-RPC error (err_kind protocol), registered as client alpha, auth none, per call, tools_to_execute ["*"], echo priced 0.001; a VK with mcp_configs alpha ["*"], a budget of 1 per 1h and a request limit of 100 per 1h.`, [
  request("GOV-24", "setup: fresh mcpchaos server alpha", "POST", "/control", { base: CHAOS, noAuth: true, body: { add: "alpha" }, asserts: [A.status(200)] }),
  mockPost("GOV-24", "alpha", "tools/set", "setup: alpha serves echo (failing with a JSON-RPC error) and add", { tools: [{ name: "echo", fault: { err_rate: 1, err_kind: "protocol" } }, { name: "add" }] }),
  alphaClient("GOV-24"),
  priceAlpha("GOV-24", "setup: price echo at 0.001", "gov-24-alpha", { echo: 0.001 }),
  govKey("GOV-24", "gov24_vk", ALPHA_ALL, { budgets: [{ max_limit: 1, reset_duration: "1h" }], rate_limit: { request_max_limit: 100, request_reset_duration: "1h" } }),
  vkMemory("GOV-24", "setup: record the budget and request count", "gov24_vk", [], [...keepBudget("gov24_b0"), ...keepRequests("gov24_r0")]),
  ...[1, 2, 3, 4, 5].map((i) => execTool("GOV-24", `unpriced add call ${i}`, "alpha-add", [A.status(200)], vkUse("gov24_vk"))),
  vkMemory("GOV-24", "the budget is unchanged after the add calls, which were counted", "gov24_vk", [budgetPlus("gov24_b0", 0), requestsPlus("gov24_r0", 5)], [...keepBudget("gov24_b1"), ...keepRequests("gov24_r1")]),
  execTool("GOV-24", "a failed echo call", "alpha-echo", [A.status(400)], vkUse("gov24_vk")),
  vkMemory("GOV-24", "budget and request count are unchanged after the failed echo", "gov24_vk", [budgetPlus("gov24_b1", 0), requestsPlus("gov24_r1", 0)]),
  mockPost("GOV-24", "alpha", "tools/set", "control: echo stops failing", { tools: [{ name: "echo" }, { name: "add" }] }),
  execR2("GOV-24", "control: a successful echo call", "alpha-echo", "gov24-ok", [A.status(200)], vkUse("gov24_vk")),
  logOf("GOV-24", "control: its log entry", "gov24-ok", [entryCost(0.001)]),
  vkMemory("GOV-24", "control: the priced echo is charged and counted", "gov24_vk", [budgetPlus("gov24_b1", 0.001), requestsPlus("gov24_r1", 1)]),
  deleteLogs("GOV-24", ["gov24-ok"]),
  vkDelete("GOV-24", "teardown: delete the VK", "gov24_vk"),
  agentTeardown("GOV-24"),
]);

// govTokenPasses repeats GOV-25's and GOV-38's /mcp check in mcp_server_auth_mode both, with the VK in x-bf-vk and with a token
// from a grant consented with the VK, and in oauth with the token.
const govTokenPasses = (id, vkVarName, asserts) => {
  const p = id.toLowerCase().replace("-", "");
  return [
    readConfig(id), authMode(id, "B", false),
    register(id, `${p}_client`),
    ...grantWith(id, `${p}_client`, `${p}_g`, vkVarName),
    list(id, "both: /mcp tools/list with the VK", "/mcp", [{ key: "x-bf-vk", value: `{{${vkVarName}}}` }], asserts),
    list(id, "both: /mcp tools/list with the VK's token", "/mcp", [bearer(`{{${p}_g_at}}`)], asserts),
    authMode(id, "O", false),
    list(id, "oauth: /mcp tools/list with the VK's token", "/mcp", [bearer(`{{${p}_g_at}}`)], asserts),
    restoreConfig(id),
  ];
};
const GOV_TOKEN_TEXT = ". The /mcp request is then repeated in mcp_server_auth_mode both, with the VK in x-bf-vk and with a token from a grant consented with the VK, and in oauth with the token (enforce_auth_on_inference off)";
const GOV25 = folder("GOV-25 Request rate limit counts tool calls", `Setup: ${ALPHA_EA}; a VK with mcp_configs alpha ["*"] and a request limit of 1 per 1h${GOV_TOKEN_TEXT}.`, [
  ...alphaEA("GOV-25"),
  govKey("GOV-25", "gov25_vk", ALPHA_ALL, { rate_limit: { request_max_limit: 1, request_reset_duration: "1h" } }),
  execTool("GOV-25", "call 1", "alpha-add", [A.status(200)], vkUse("gov25_vk")),
  execTool("GOV-25", "call 2", "alpha-add", [A.status(429), A.custom("type request_limited", ["pm.expect(pm.response.json().type, 'body: ' + pm.response.text()).to.equal('request_limited');"]), A.msgContains("request limit exceeded (1/1, resets every 1h)")], vkUse("gov25_vk")),
  rpc("GOV-25", "/mcp tools/list with the VK", listMsg, { headers: [MCP_ACCEPT, { key: "x-bf-vk", value: "{{gov25_vk}}" }], asserts: [A.status(429)] }),
  ...govTokenPasses("GOV-25", "gov25_vk", [A.status(429)]),
  vkDelete("GOV-25", "teardown: delete the VK", "gov25_vk"),
  agentTeardown("GOV-25"),
]);

const GOV26 = folder("GOV-26 Code mode bills nested calls, not the meta call", `Setup: ${CM_TEXT}, cm's echo priced 0.001; a VK with mcp_configs cm ["*"] and a budget of 1 per 1h; executeToolCode calls cm.echo twice.`, [
  ...cmSetup("GOV-26"),
  priceAlpha("GOV-26", "setup: price cm's echo at 0.001", "gov-26-cm", { echo: 0.001 }),
  govKey("GOV-26", "gov26_vk", [{ mcp_client_name: "cm", tools_to_execute: ["*"] }], { budgets: [{ max_limit: 1, reset_duration: "1h" }] }),
  vkMemory("GOV-26", "setup: record the budget", "gov26_vk", [], keepBudget("gov26_b0")),
  metaWith("GOV-26", "executeToolCode calling cm.echo twice", "executeToolCode", { code: "a = cm.echo(text=\"gov26-a\")\nb = cm.echo(text=\"gov26-b\")\nresult = [a, b]" }, [A.status(200)], vkUse("gov26_vk")),
  request("GOV-26", "two echo entries, each costing 0.001", "GET", "/api/mcp-logs?tool_names=echo&content_search=gov26-", { pre: pollLogCount("tool_names=echo&content_search=gov26-", 2), asserts: [A.status(200), A.custom("total_count 2, each cost 0.001", ["var b = pm.response.json();", "pm.expect(b.pagination.total_count, 'body: ' + pm.response.text()).to.equal(2);", "b.logs.forEach(function (l) { pm.expect(l.cost).to.be.closeTo(0.001, 1e-9); });"])] }),
  vkMemory("GOV-26", "the budget rose by 0.002", "gov26_vk", [budgetPlus("gov26_b0", 0.002)]),
  vkDelete("GOV-26", "teardown: delete the VK", "gov26_vk"),
  cmTeardown("GOV-26"),
]);

const GOV28 = folder("GOV-28 Pricing edits apply live", `Setup: ${ALPHA_EA}, echo priced 0.001. Pricing is then set to 0.005, then to {}, and the client is renamed alpha2.`, [
  ...alphaEA("GOV-28"),
  priceAlpha("GOV-28", "setup: price echo at 0.001", "gov-28-alpha", { echo: 0.001 }),
  priceAlpha("GOV-28", "price echo at 0.005", "gov-28-alpha", { echo: 0.005 }),
  execR2("GOV-28", "execute alpha-echo", "alpha-echo", "gov28-r1", [A.status(200)]),
  logOf("GOV-28", "it logs cost 0.005", "gov28-r1", [entryCost(0.005)]),
  priceAlpha("GOV-28", "set pricing {}", "gov-28-alpha", {}),
  execR2("GOV-28", "execute alpha-echo again", "alpha-echo", "gov28-r2", [A.status(200)]),
  logOf("GOV-28", "it logs no cost", "gov28-r2", [entryCost(null)]),
  request("GOV-28", "rename the client to alpha2", "PUT", "/api/mcp/client/gov-28-alpha", { body: { name: "alpha2" }, asserts: [A.status(200)] }),
  execR2("GOV-28", "execute alpha2-echo", "alpha2-echo", "gov28-r3", [A.status(200)]),
  logOf("GOV-28", "it logs under alpha2", "gov28-r3", [A.jsonEquals("server_label", "alpha2")]),
  deleteLogs("GOV-28", ["gov28-r1", "gov28-r2", "gov28-r3"]),
  agentTeardown("GOV-28"),
]);

const GOV38 = folder("GOV-38 Budget enforced on `/mcp`", `Setup: ${ALPHA_EA}, echo priced 0.001; a VK with mcp_configs alpha ["*"] and a budget of 0.001 per 1h${GOV_TOKEN_TEXT}.`, [
  ...alphaEA("GOV-38"),
  priceAlpha("GOV-38", "setup: price echo at 0.001", "gov-38-alpha", { echo: 0.001 }),
  govKey("GOV-38", "gov38_vk", ALPHA_ALL, { budgets: [{ max_limit: 0.001, reset_duration: "1h" }] }),
  gwCall("GOV-38", "first tools/call alpha-echo", "alpha-echo", [A.custom("isError is not set", ["pm.expect(pm.response.json().result.isError || false, 'body: ' + pm.response.text()).to.equal(false);"])], [{ key: "x-bf-vk", value: "{{gov38_vk}}" }]),
  rpc("GOV-38", "the next /mcp request", listMsg, { headers: [MCP_ACCEPT, { key: "x-bf-vk", value: "{{gov38_vk}}" }], asserts: [A.status(402), A.msgContains("Budget exceeded:")] }),
  ...govTokenPasses("GOV-38", "gov38_vk", [A.status(402), A.msgContains("Budget exceeded:")]),
  vkDelete("GOV-38", "teardown: delete the VK", "gov38_vk"),
  agentTeardown("GOV-38"),
]);

// vmcp is the virtual MCP "Tools A" (slug tools-a) over alpha's echo, kept in gov<n>_vmcp.
const vmcpCreate = (id, v, tools = ["echo"]) => request(id, "setup: create the virtual MCP Tools A over alpha's echo", "POST", "/api/mcp/virtual-mcps", { body: { name: "Tools A", tools: [{ mcp_client_id: `${id.toLowerCase()}-alpha`, tool_names: tools }] }, asserts: [A.status(201)], extraTests: [`pm.collectionVariables.set(${lit(v)}, pm.response.json().virtual_mcp.id);`] });
const vmcpAttach = (id, title, v, vk) => request(id, title, "POST", `/api/mcp/virtual-mcps/{{${v}}}/virtual-keys/{{${vk}_id}}`, { asserts: [A.status(200)] });
const vmcpPut = (id, title, v, body) => request(id, title, "PUT", `/api/mcp/virtual-mcps/{{${v}}}`, { body, asserts: [A.status(200)] });
const slugList = (id, title, vk, asserts) => rpc(id, title, listMsg, { path: "/mcp/tools-a", headers: [MCP_ACCEPT, ...(vk ? [{ key: "x-bf-vk", value: `{{${vk}}}` }] : [])], asserts });
const GOV35 = folder("GOV-35 Virtual MCP tool edits apply live", `Setup: alpha (echo, add, greet) registered as client alpha, auth none, per call, tools_to_execute ["*"]; a VK with no mcp_configs, attached to the virtual MCP Tools A over alpha's echo. Each edit is read through /mcp tools/list with the VK.`, [
  ...freshServer("GOV-35", "alpha", ["echo", "add", "greet"]),
  alphaClient("GOV-35"),
  govKey("GOV-35", "gov35_vk", undefined),
  vmcpCreate("GOV-35", "gov35_vmcp"),
  vmcpAttach("GOV-35", "setup: attach the VK", "gov35_vmcp", "gov35_vk"),
  vmcpPut("GOV-35", "tools [\"add\"]", "gov35_vmcp", { tools: [{ mcp_client_id: "gov-35-alpha", tool_names: ["add"] }] }),
  govList("GOV-35", "/mcp tools/list with the VK", ["alpha-add"]),
  vmcpPut("GOV-35", "tools [\"*\"]", "gov35_vmcp", { tools: [{ mcp_client_id: "gov-35-alpha", tool_names: ["*"] }] }),
  govList("GOV-35", "/mcp tools/list with the VK", ["alpha-add", "alpha-echo", "alpha-greet"]),
  request("GOV-35", "tools [\"nosuch\"] is accepted", "PUT", "/api/mcp/virtual-mcps/{{gov35_vmcp}}", { body: { tools: [{ mcp_client_id: "gov-35-alpha", tool_names: ["nosuch"] }] }, asserts: [A.status(200), A.custom("the stored tools are [nosuch] on alpha", ["pm.expect(pm.response.json().virtual_mcp.tools, 'body: ' + pm.response.text()).to.eql([{ mcp_client_id: 'gov-35-alpha', tool_names: ['nosuch'] }]);"])] }),
  govList("GOV-35", "/mcp tools/list with the VK", []),
  vmcpPut("GOV-35", "setup: tools [\"*\"] again", "gov35_vmcp", { tools: [{ mcp_client_id: "gov-35-alpha", tool_names: ["*"] }] }),
  govList("GOV-35", "control: /mcp tools/list with the VK lists every tool again", ["alpha-add", "alpha-echo", "alpha-greet"]),
  vmcpPut("GOV-35", "tools []", "gov35_vmcp", { tools: [{ mcp_client_id: "gov-35-alpha", tool_names: [] }] }),
  govList("GOV-35", "/mcp tools/list with the VK", []),
  request("GOV-35", "teardown: delete the virtual MCP", "DELETE", "/api/mcp/virtual-mcps/{{gov35_vmcp}}", { asserts: [A.status(200)] }),
  vkDelete("GOV-35", "teardown: delete the VK", "gov35_vk"),
  deleteClient("GOV-35", "gov-35-alpha"),
]);

// gov34Check asserts, after a step, that /mcp/tools-a refuses the VK and that a chat with the VK no longer injects alpha-echo.
const gov34Check = (step, key) => [
  slugList("GOV-34", `after ${step}: /mcp/tools-a with the VK`, "gov34_vk", [A.status(403)]),
  llmScript("GOV-34", key, TEXT_OK),
  infPost("GOV-34", `after ${step}: chat with the VK`, "/v1/chat/completions", chatBody(key), [A.status(200)], { headers: [{ key: "x-bf-vk", value: "{{gov34_vk}}" }] }),
  llmCaptured("GOV-34", `after ${step}: alpha-echo is not injected`, key, [reqCount(1), toolsAre(0, [])]),
];
const GOV34 = folder("GOV-34 Disabling or deleting a virtual MCP", `Setup: ${LLM_TEXT}; alpha (echo, add, greet) registered as client alpha, auth none, per call, tools_to_execute ["*"]; a VK allowed openai with any model and key and no mcp_configs, attached to the virtual MCP Tools A over alpha's echo. Anonymous callers of the slug get 403. Each step is undone before the next: detach, enabled false, then delete.`, [
  ...freshServer("GOV-34", "alpha", ["echo", "add", "greet"]),
  alphaClient("GOV-34"),
  govKey("GOV-34", "gov34_vk", undefined, { provider_configs: OPENAI_GRANT }),
  vmcpCreate("GOV-34", "gov34_vmcp"),
  vmcpAttach("GOV-34", "setup: attach the VK", "gov34_vmcp", "gov34_vk"),
  slugList("GOV-34", "control: /mcp/tools-a with the VK", "gov34_vk", [A.status(200), toolNamesInList(["alpha-echo"])]),
  slugList("GOV-34", "/mcp/tools-a with no credential", null, [A.status(403)]),
  llmScript("GOV-34", "gov34-on", TEXT_OK),
  infPost("GOV-34", "control: chat with the VK", "/v1/chat/completions", chatBody("gov34-on"), [A.status(200)], { headers: [{ key: "x-bf-vk", value: "{{gov34_vk}}" }] }),
  llmCaptured("GOV-34", "control: alpha-echo is injected", "gov34-on", [reqCount(1), toolsAre(0, ["alpha-echo"])]),
  request("GOV-34", "detach the VK", "DELETE", "/api/mcp/virtual-mcps/{{gov34_vmcp}}/virtual-keys/{{gov34_vk_id}}", { asserts: [A.status(200)] }),
  ...gov34Check("the detach", "gov34-detach"),
  vmcpAttach("GOV-34", "setup: attach the VK again", "gov34_vmcp", "gov34_vk"),
  slugList("GOV-34", "control: after attaching again, /mcp/tools-a with the VK", "gov34_vk", [A.status(200), toolNamesInList(["alpha-echo"])]),
  vmcpPut("GOV-34", "set enabled false", "gov34_vmcp", { enabled: false }),
  ...gov34Check("enabled false", "gov34-disabled"),
  vmcpPut("GOV-34", "setup: set enabled true", "gov34_vmcp", { enabled: true }),
  slugList("GOV-34", "control: after enabling, /mcp/tools-a with the VK", "gov34_vk", [A.status(200), toolNamesInList(["alpha-echo"])]),
  request("GOV-34", "delete the virtual MCP", "DELETE", "/api/mcp/virtual-mcps/{{gov34_vmcp}}", { asserts: [A.status(200)] }),
  ...gov34Check("the delete", "gov34-deleted"),
  vkDelete("GOV-34", "teardown: delete the VK", "gov34_vk"),
  deleteClient("GOV-34", "gov-34-alpha"),
]);

// ================================================================ log list paging, stats and histograms, scoped to one client

// Every test shares one logs store, so these queries add server_labels=<this test's client name>, a name no other
// test uses, to make the totals the test's own.
const logClient = (id, name, over = {}) => [
  ...freshServer(id, "alpha", ["echo_tool", "add_tool"]),
  createClient(id, httpClient(`${name}-c`, name, "alpha", { tools_to_execute: ["*"], ...over }), `setup: create client ${name} on alpha`),
];
const LOG_CLIENT_TEXT = (name, extra = "") => `alpha (echo_tool, add_tool); a client named ${name} on alpha, auth none, per call, tools_to_execute ["*"]${extra}. Every query adds server_labels=${name}, because all tests share one logs store`;
const logPrice = (id, name, pricing, title) => request(id, title, "PUT", `/api/mcp/client/${name}-c`, { body: { tool_pricing: pricing }, asserts: [A.status(200)] });
const logsBody = "var b = pm.response.json();";

const LOG26_LATENCY = ["120ms", "40ms", "80ms"];
const LOG26 = folder("LOG-26 Pagination and sort", `Setup: ${LOG_CLIENT_TEXT("log26")}; four echo_tool calls, the first three delayed upstream by 120ms, 40ms and 80ms, and one call to an unknown tool, 5 entries.`, [
  ...logClient("LOG-26", "log26"),
  ...[1, 2, 3, 4].flatMap((i) => [
    i <= 3
      ? mockPost("LOG-26", "alpha", "faults", `setup: alpha delays tools/call by ${LOG26_LATENCY[i - 1]}`, { faults: { latency: LOG26_LATENCY[i - 1], methods: ["tools/call"] } })
      : mockPost("LOG-26", "alpha", "faults/clear", "setup: clear alpha's faults", {}),
    execR("LOG-26", `setup: call ${i}`, `log26-${i}`, "log26-echo_tool", { text: `log26-${i}` }, [A.status(200)]),
  ]),
  execR("LOG-26", "setup: a call to an unknown tool", "log26-5", "log26-nope", { text: "x" }, [A.status(400)]),
  request("LOG-26", "defaults", "GET", "/api/mcp-logs?server_labels=log26", { pre: pollLogCount("server_labels=log26", 5), asserts: [A.status(200), A.custom("total_count 5, limit 50, timestamp desc", [logsBody,
    "pm.expect(b.pagination.total_count, 'body: ' + pm.response.text()).to.equal(5);",
    "pm.expect(b.pagination.limit).to.equal(50);",
    "pm.expect(b.pagination.sort_by).to.equal('timestamp');",
    "pm.expect(b.pagination.order).to.equal('desc');",
    "for (var i = 1; i < b.logs.length; i++) { pm.expect(new Date(b.logs[i - 1].timestamp).getTime()).to.be.at.least(new Date(b.logs[i].timestamp).getTime()); }",
  ])], extraTests: [logsBody, "pm.collectionVariables.set('log26_ids', JSON.stringify(b.logs.map(function (l) { return l.id; })));"] }),
  request("LOG-26", "limit=2&offset=2", "GET", "/api/mcp-logs?server_labels=log26&limit=2&offset=2", { asserts: [A.status(200), A.custom("the third and fourth entries of the default order, offset 2", [logsBody,
    "pm.expect(b.logs.map(function (l) { return l.id; }), 'body: ' + pm.response.text()).to.eql(JSON.parse(pm.collectionVariables.get('log26_ids')).slice(2, 4));",
    "pm.expect(b.pagination.offset).to.equal(2);",
  ])] }),
  request("LOG-26", "latency ascending", "GET", "/api/mcp-logs?server_labels=log26&sort_by=latency&order=asc", { asserts: [A.status(200), A.custom("ids by latency 4, 2, 3, 1, then the entry with no latency", [logsBody,
    "pm.expect(b.logs.map(function (l) { return l.id; }), 'body: ' + pm.response.text()).to.eql(['log26-4', 'log26-2', 'log26-3', 'log26-1', 'log26-5']);",
    "for (var i = 1; i < 4; i++) { pm.expect(b.logs[i].latency).to.be.above(b.logs[i - 1].latency); }",
    "pm.expect(b.logs[4].latency == null).to.equal(true);",
  ])] }),
  mockPost("LOG-26", "alpha", "faults/clear", "teardown: clear alpha's faults", {}),
  deleteLogs("LOG-26", ["log26-1", "log26-2", "log26-3", "log26-4", "log26-5"]),
  deleteClient("LOG-26", "log26-c"),
]);

const LOG30_LATENCY = ["60ms", "120ms", "180ms"];
const LOG30 = folder("LOG-30 Stats", `Setup: ${LOG_CLIENT_TEXT("log30", ", echo_tool priced 0.25")}; three successful echo_tool calls, delayed upstream by 60ms, 120ms and 180ms, and one call to an unknown tool.`, [
  ...logClient("LOG-30", "log30"),
  logPrice("LOG-30", "log30", { echo_tool: 0.25 }, "setup: price echo_tool at 0.25"),
  ...[1, 2, 3].flatMap((i) => [
    mockPost("LOG-30", "alpha", "faults", `setup: alpha delays tools/call by ${LOG30_LATENCY[i - 1]}`, { faults: { latency: LOG30_LATENCY[i - 1], methods: ["tools/call"] } }),
    execR("LOG-30", `setup: call ${i}`, `log30-${i}`, "log30-echo_tool", { text: `log30-${i}` }, [A.status(200)]),
  ]),
  mockPost("LOG-30", "alpha", "faults/clear", "setup: clear alpha's faults", {}),
  execR("LOG-30", "setup: a call to an unknown tool", "log30-4", "log30-nope", { text: "x" }, [A.status(400)]),
  request("LOG-30", "setup: the four entries", "GET", "/api/mcp-logs?server_labels=log30", { pre: pollLogCount("server_labels=log30", 4), asserts: [A.status(200)], extraTests: [logsBody, "var lat = b.logs.filter(function (l) { return l.latency != null; }).map(function (l) { return l.latency; }); pm.collectionVariables.set('log30_avg', String(lat.reduce(function (a, x) { return a + x; }, 0) / lat.length));"] }),
  request("LOG-30", "stats", "GET", "/api/mcp-logs/stats?server_labels=log30", { asserts: [A.status(200), A.custom("total_executions 4, success_rate 75, total_cost 0.75, average_latency over entries with latency", [logsBody,
    "pm.expect(b.total_executions, 'body: ' + pm.response.text()).to.equal(4);",
    "pm.expect(b.success_rate).to.equal(75);",
    "pm.expect(b.total_cost).to.be.closeTo(0.75, 1e-9);",
    "pm.expect(b.average_latency).to.be.above(0);",
    "pm.expect(b.average_latency).to.be.closeTo(Number(pm.collectionVariables.get('log30_avg')), 1e-6);",
  ])] }),
  deleteLogs("LOG-30", ["log30-1", "log30-2", "log30-3", "log30-4"]),
  deleteClient("LOG-30", "log30-c"),
]);

const bucketSum = (field) => `b.buckets.reduce(function (a, x) { return a + (x[${lit(field)}] || 0); }, 0)`;
const LOG34 = folder("LOG-34 Histogram", `Setup: ${LOG_CLIENT_TEXT("log34")}; two successful echo_tool calls and one call to an unknown tool; period=1h.`, [
  ...logClient("LOG-34", "log34"),
  ...[1, 2].map((i) => execR("LOG-34", `setup: call ${i}`, `log34-${i}`, "log34-echo_tool", { text: `log34-${i}` }, [A.status(200)])),
  execR("LOG-34", "setup: a call to an unknown tool", "log34-3", "log34-nope", { text: "x" }, [A.status(400)]),
  request("LOG-34", "histogram", "GET", "/api/mcp-logs/histogram?period=1h&server_labels=log34", { pre: pollLogCount("server_labels=log34", 3), asserts: [A.status(200), A.custom("bucket_size_seconds 60; counts sum to 3, success 2, error 1", [logsBody,
    "pm.expect(b.bucket_size_seconds, 'body: ' + pm.response.text()).to.equal(60);",
    `pm.expect(${bucketSum("count")}).to.equal(3);`,
    `pm.expect(${bucketSum("success")}).to.equal(2);`,
    `pm.expect(${bucketSum("error")}).to.equal(1);`,
  ])] }),
  deleteLogs("LOG-34", ["log34-1", "log34-2", "log34-3"]),
  deleteClient("LOG-34", "log34-c"),
]);

const LOG35 = folder("LOG-35 Cost histogram", `Setup: ${LOG_CLIENT_TEXT("log35", ", echo_tool priced 0.25")}; two successful echo_tool calls; period=24h.`, [
  ...logClient("LOG-35", "log35"),
  logPrice("LOG-35", "log35", { echo_tool: 0.25 }, "setup: price echo_tool at 0.25"),
  ...[1, 2].map((i) => execR("LOG-35", `setup: call ${i}`, `log35-${i}`, "log35-echo_tool", { text: `log35-${i}` }, [A.status(200)])),
  request("LOG-35", "cost histogram", "GET", "/api/mcp-logs/histogram/cost?period=24h&server_labels=log35", { pre: pollLogCount("server_labels=log35", 2), asserts: [A.status(200), A.custom("bucket_size_seconds 3600; total_cost sums to 0.5", [logsBody,
    "pm.expect(b.bucket_size_seconds, 'body: ' + pm.response.text()).to.equal(3600);",
    `pm.expect(${bucketSum("total_cost")}).to.be.closeTo(0.5, 1e-9);`,
  ])] }),
  deleteLogs("LOG-35", ["log35-1", "log35-2"]),
  deleteClient("LOG-35", "log35-c"),
]);

// ================================================================ header rotation, a sticky round trip, a disabled client, filter dimensions and the setup lock on create

const T2 = "auth-t2-token";
const AUTH07 = folder("AUTH-07 Rotation applies to the live connection", `Setup: alpha (echo_tool, add_tool) requires a bearer token and maps T1 (${T1}) to id1 and T2 (${T2}) to id2; ${T1_CLIENT_TEXT}, needs_session_stickiness true, tools_to_execute ["*"] (as AUTH-02); then a PUT sets Authorization: Bearer T2.`, [
  ...freshServer("AUTH-07", "alpha", ["echo_tool", "add_tool"]),
  mockAuth("AUTH-07", "alpha", "setup: alpha requires a bearer token, T1 mapped to id1 and T2 to id2", { bearer: { [T1]: "id1", [T2]: "id2" }, require: true }),
  createClient("AUTH-07", t1Client("auth07-c", "auth07_c", { needs_session_stickiness: true }), "setup: create the sticky client"),
  saveSessions("AUTH-07", "alpha", "auth07_s"),
  request("AUTH-07", "PUT Authorization: Bearer T2", "PUT", "/api/mcp/client/auth07-c", { body: { headers: { Authorization: `Bearer ${T2}` } }, asserts: [A.status(200)] }),
  mockGet("AUTH-07", "alpha", "two more sessions: the new header is verified on its own connection, then the client reconnects", [sessionsPlus("auth07_s", 2)]),
  execTool("AUTH-07", "execute after the rotation", "auth07_c-echo_tool", [A.status(200)]),
  mockGet("AUTH-07", "alpha", "the call was counted under id2", [identityCallsAre({ id2: 1 })]),
  deleteClient("AUTH-07", "auth07-c"),
]);

const CONN01 = folder("CONN-01 HTTP sticky round trip", "Setup: alpha (echo_tool replying \"hi\", add_tool); an HTTP client on alpha named conn01, auth none, needs_session_stickiness true, tools_to_execute [\"*\"].", [
  ...serveTools("CONN-01", "alpha", [{ name: "echo_tool", reply: HI_REPLY }, { name: "add_tool" }], "echo_tool (replying \"hi\") and add_tool"),
  createClient("CONN-01", httpClient("conn01-c", "conn01", "alpha", { needs_session_stickiness: true, tools_to_execute: ["*"] })),
  getClient("CONN-01", "conn01-c", "the client list shows healthy, the server's tool names and no last_failure", [clientState("healthy"), toolNamesAre(["add_tool", "echo_tool"]),
    A.custom("there is no last_failure", ["pm.expect(pm.response.json().clients[0].last_failure == null, 'body: ' + pm.response.text()).to.equal(true);"])]),
  ...[1, 2, 3].map((i) => execTool("CONN-01", `execute ${i} of 3`, "conn01-echo_tool", [A.status(200), A.custom("the body is {role \"tool\", content \"hi\", tool_call_id \"c1\"}", [
    "pm.expect(pm.response.json(), 'body: ' + pm.response.text()).to.eql({ role: 'tool', content: 'hi', tool_call_id: 'c1' });",
  ])])),
  mockGet("CONN-01", "alpha", "one session, three calls to echo_tool", [counterIs("sessions", 1), callsIs("echo_tool", 3)]),
  deleteClient("CONN-01", "conn01-c"),
]);

const INF32 = folder("INF-32 Disabled client", `Setup: ${LLM_TEXT}; alpha (echo, add, greet) registered as client alpha, per call, tools_to_execute ["*"], then disabled with a PUT of disabled true.`, [
  ...freshServer("INF-32", "alpha", ["echo", "add", "greet"]),
  alphaClient("INF-32"),
  request("INF-32", "setup: disable client alpha", "PUT", "/api/mcp/client/inf-32-alpha", { body: { disabled: true }, asserts: [A.status(200)] }),
  llmScript("INF-32", "inf32", TEXT_OK),
  infChat("INF-32", "chat", "inf32", [A.status(200)]),
  llmCaptured("INF-32", "nothing is injected", "inf32", [reqCount(1), toolsAre(0, [])]),
  execTool("INF-32", "execute alpha-echo", "alpha-echo", [A.status(400), A.msgContains("is not permitted (client alpha is disabled)")]),
  mockCalls("INF-32", "alpha", "echo", 0),
  deleteClient("INF-32", "inf-32-alpha"),
]);

const MCP_DIMS = ["apps", "server_labels", "tool_names", "user_agents", "virtual_keys"];
const keysAre = (keys) => A.custom(`the body's keys are ${lit(keys)}`, [`pm.expect(Object.keys(pm.response.json()).sort(), 'body: ' + pm.response.text()).to.eql(${lit(keys)});`]);
const LOG32 = folder("LOG-32 Filter data dimensions", "Setup: none.", [
  request("LOG-32", "dimensions=tool_names", "GET", "/api/mcp-logs/filterdata?dimensions=tool_names", { asserts: [A.status(200), keysAre(["tool_names"])] }),
  request("LOG-32", "dimensions=bogus falls back to all five", "GET", "/api/mcp-logs/filterdata?dimensions=bogus", { asserts: [A.status(200), keysAre(MCP_DIMS)] }),
]);

const ADM02 = folder("ADM-02 Setup-lock gating of `POST /api/mcp/client`", `Setup: alpha (echo_tool, add_tool) on loopback; ${LOCK_TEXT}. Each create is a sticky client on alpha.`, [
  ...freshServer("ADM-02", "alpha", ["echo_tool", "add_tool"]),
  authOff("ADM-02"),
  request("ADM-02", "create with no setup token", "POST", "/api/mcp/client", { noAuth: true, noCookies: true, body: httpClient("adm02-c", "adm02_c", "alpha", { needs_session_stickiness: true }), asserts: [A.status(401), A.msgContains("send the setup token in the X-Bifrost-Setup-Token header")] }),
  request("ADM-02", "create with a wrong setup token", "POST", "/api/mcp/client", { noAuth: true, noCookies: true, headers: [WRONG_TOKEN], body: httpClient("adm02-c", "adm02_c", "alpha", { needs_session_stickiness: true }), asserts: [A.status(403), A.msgEquals("invalid setup token")] }),
  request("ADM-02", "create with the setup token", "POST", "/api/mcp/client", { noAuth: true, noCookies: true, headers: [SETUP_TOKEN], body: httpClient("adm02-c", "adm02_c", "alpha", { needs_session_stickiness: true }), asserts: [A.status(200)] }),
  authOn("ADM-02"),
  deleteClient("ADM-02", "adm02-c"),
]);

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
    section("ADM Client admin", "Client admin tests.", [ADM01, ADM02, ADM03, ADM04, ADM05, ADM06, ADM07, ADM08, ADM10, ADM11, ADM12, ADM13, ADM14, ADM16, ADM17, ADM18, ADM19, ADM20, ADM21, ADM22, ADM23, ADM24, ADM25, ADM27, ADM28, ADM29, ADM30, ADM31, ADM32, ADM33, ADM34, ADM35, ADM37, ADM38, ADM39, ADM40, ADM41, ADM42, ADM43, ADM44, ADM45, ADM46, ADM47, ADM50, ADM51, ADM52, ADM53, ADM54, ADM55, ADM56, ADM57, ADM58, ADM59, ADM60, ADM62, ADM63, ADM65, ADM66, ADM67, ADM68, ADM69, ADM70, ADM71, ADM72, ADM73, ADM74, ADM75, ADM76, ADM77, ADM78, ADM80, ADM81, ADM82, ADM83, ADM86]),
    section("AUTH Upstream auth", "Upstream auth tests.", [AUTH01, AUTH02, AUTH03, AUTH05, AUTH06, AUTH07, AUTH08, AUTH09, AUTH10, AUTH11, AUTH12, AUTH14, AUTH15, AUTH16, AUTH17, AUTH18, AUTH19, AUTH20, AUTH21, AUTH23, AUTH24, AUTH25, AUTH27, AUTH28, AUTH29, AUTH30, AUTH31, AUTH32, AUTH33, AUTH34, AUTH35, AUTH36, AUTH39, AUTH40, AUTH41, AUTH42, AUTH43, AUTH44, AUTH45, AUTH46, AUTH48, AUTH49, AUTH50]),
    section("CONN Connection types", "Connection type tests.", [CONN01, CONN02, CONN03, CONN04, CONN05, CONN06, CONN07, CONN09, CONN10, CONN11, CONN12, CONN16, CONN17, CONN18, CONN20, CONN22, CONN23, CONN24, CONN25, CONN27]),
    section("EP Bifrost MCP endpoint", "Bifrost MCP endpoint tests.", [EP01, EP02, EP03, EP04, EP05, EP06, EP07, EP08, EP09, EP11, EP12, EP13, EP14, EP15, EP16, EP17, EP18, EP20, EP21, EP22, EP23, EP24, EP25, EP26, EP27, EP28, EP29, EP30, EP31, EP32, EP33, EP34, EP35, EP36, EP37, EP38, EP39, EP40, EP41, EP42, EP43, EP44, EP45, EP46, EP47, EP48, EP49, EP50, EP51, EP52, EP53, EP54]),
    section("OAS Bifrost OAuth server", "Bifrost OAuth server tests.", [OAS01, OAS02, OAS03, OAS05, OAS06, OAS07, OAS08, OAS09, OAS10, OAS11, OAS12, OAS13, OAS14, OAS15, OAS16, OAS17, OAS18, OAS19, OAS20, OAS21, OAS23, OAS25, OAS27, OAS28, OAS30, OAS31, OAS32, OAS33, OAS34, OAS36, OAS38, OAS42]),
    section("SET Per-client settings", "Per-client setting tests.", [SET01, SET02, SET07, SET08, SET09, SET14, SET19, SET20, SET21, SET22]),
    section("GOV Governance", "Virtual MCP and virtual key governance tests.", [GOV01, GOV02, GOV03, GOV04, GOV05, GOV06, GOV07, GOV08, GOV11, GOV12, GOV14, GOV15, GOV16, GOV17, GOV19, GOV20, GOV21, GOV22, GOV23, GOV24, GOV25, GOV26, GOV28, GOV29, GOV31, GOV32, GOV33, GOV34, GOV35, GOV38]),
    section("INF Inference", "Inference tests.", [INF01, INF02, INF03, INF04, INF06, INF07, INF08, INF09, INF10, INF11, INF12, INF14, INF15, INF16, INF17, INF18, INF19, INF20, INF21, INF22, INF23, INF24, INF25, INF26, INF27, INF29, INF30, INF31, INF32, INF33, INF34, INF35, INF36, INF37, INF38, INF39, INF40, INF41, INF42, INF43, INF44, INF45, INF46, INF47, INF48, INF49, INF50, INF51, INF52, INF53, INF54, INF55, INF56, INF57, INF58, INF60, INF61, INF62, INF63, INF64, INF65, INF66]),
    section("LIB MCP library", "MCP library tests.", [LIB01, LIB02, LIB03, LIB05, LIB06, LIB07, LIB08, LIB09, LIB10, LIB11, LIB13, LIB14, LIB16, LIB17, LIB18, LIB19, LIB20, LIB21, LIB22, LIB23, LIB24, LIB25, LIB26, LIB32, LIB33, LIB34, LIB36, LIB37]),
    section("LOG Logs and telemetry", "Logs and telemetry tests.", [LOG01, LOG02, LOG03, LOG04, LOG05, LOG06, LOG07, LOG08, LOG09, LOG10, LOG11, LOG12, LOG13, LOG14, LOG16, LOG17, LOG18, LOG19, LOG20, LOG21, LOG22, LOG26, LOG27, LOG28, LOG29, LOG30, LOG31, LOG32, LOG33, LOG34, LOG35, LOG37, LOG38, LOG39, LOG40, LOG41, LOG43]),
    section("FAIL Failure handling", "Failure handling tests.", [FAIL02, FAIL04, FAIL05, FAIL06, FAIL07, FAIL14, FAIL16, FAIL18, FAIL19]),
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
