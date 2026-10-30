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
    section("ADM Client admin", "Client admin tests.", [ADM05, ADM06, ADM62, ADM73]),
    section("EP Bifrost MCP endpoint", "Bifrost MCP endpoint tests.", [EP09]),
    section("OAS Bifrost OAuth server", "Bifrost OAuth server tests.", [OAS31, OAS36]),
    section("INF Inference", "Inference tests.", [INF23, INF56]),
    section("LIB MCP library", "MCP library tests.", [LIB06, LIB10, LIB16]),
    section("LOG Logs and telemetry", "Logs and telemetry tests.", [LOG27, LOG28, LOG29, LOG37, LOG40]),
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
