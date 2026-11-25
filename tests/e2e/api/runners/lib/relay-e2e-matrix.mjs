// Relay e2e cases for folder 285 of provider-harness.json, expanded by augment-provider-harness.mjs.
//
// Two jobs, both run before the chained-var guards and the setup-token rewrite so those passes see
// the finished scripts:
//
//   1. Shared helpers. Postman runs every script in its own scope, so a function defined in a
//      collection or folder script is not visible to a request's script. Relay test scripts start
//      with HELPERS_MARKER instead of a copy of the helpers, and injectRelayHelpers swaps the marker
//      for HELPERS, the same way setup-token.mjs prepends its __bfSend helper.
//   2. Table-shaped cases. Groups where every row is the same check with different inputs (the
//      validation rules, usage previews, pricing measures, routing globs, body override types) live
//      here as tables. spliceRelayMatrix inserts each block into its subfolder of folder 285 right
//      before a named static case (or at the end), so the run order is the one written below.
//
// Not to be confused with relay-matrix.mjs, the pure half of run-relay-matrix.mjs.

export const RELAY_FOLDER_PREFIX = "285. Relays";
export const SUFFIX = " - relays-e2e";
export const HELPERS_MARKER = "// relay-e2e helpers (relayLog, upstreamHits, api, jobRows, metricLines, ...) are prepended by augment-provider-harness.mjs from lib/relay-e2e-matrix.mjs.";

// Helpers every relay test script may call. ES5 only (Postman sandbox).
export const HELPERS = [
  "var BASE = pm.variables.get('baseUrl');",
  "var UPSTREAM = pm.variables.get('relayUpstreamUrl') || 'http://127.0.0.1:8783';",
  "var ID = pm.variables.get('relayReqId');",
  "function near(a, b) { return typeof a === 'number' && Math.abs(a - b) < 1e-9; }",
  "function expectStatus(code, label) { pm.test(label + ': HTTP ' + code, function () { pm.expect(pm.response.code, 'expected ' + code + ', got: ' + pm.response.text()).to.eql(code); }); }",
  "function json() { try { return pm.response.json(); } catch (e) { return {}; } }",
  "function errMsg() { var j = json(); return ((j && j.error) || {}).message || ''; }",
  "// The fixture's echo of what the upstream received; headers are lower-cased.",
  "function upHeaders() { return json().headers || {}; }",
  "function sentBody() { var b = json().body; try { return b ? JSON.parse(b) : {}; } catch (e) { return {}; } }",
  "// The call row (never a job row) for this case's x-request-id; Relay Logs are written in batches, so poll.",
  "function relayLog(check, attempt) {",
  "  attempt = attempt || 0;",
  "  pm.sendRequest({ url: BASE + '/api/relay-logs?request_id=' + encodeURIComponent(ID) + '&limit=5', method: 'GET' }, function (err, res) {",
  "    if (err || res.code !== 200) { return pm.test('Relay Logs lookup for request ' + ID + ' answers 200', function () { pm.expect.fail(err ? String(err) : res.code + ' ' + res.text()); }); }",
  "    var calls = (res.json().logs || []).filter(function (l) { return l.kind !== 'job'; });",
  "    if (calls.length > 1) { return pm.test('one call row for request ' + ID, function () { pm.expect.fail(calls.length + ' call rows: ' + res.text()); }); }",
  "    if (calls.length) { return check(calls[0]); }",
  "    if (attempt < 20) { setTimeout(function () { relayLog(check, attempt + 1); }, 500); return; }",
  "    pm.test('Relay Logs row for request ' + ID + ' was written', function () { pm.expect.fail(res.code + ' ' + res.text()); });",
  "  });",
  "}",
  "// Waits past the log batch window, then hands over every row carrying this case's request id.",
  "function rowsAfterWait(cb) {",
  "  setTimeout(function () {",
  "    pm.sendRequest({ url: BASE + '/api/relay-logs?request_id=' + encodeURIComponent(ID) + '&limit=5', method: 'GET' }, function (err, res) {",
  "      if (err || res.code !== 200) { return pm.test('Relay Logs lookup answers 200', function () { pm.expect.fail(err ? String(err) : res.code + ' ' + res.text()); }); }",
  "      cb(res.json().logs || []);",
  "    });",
  "  }, 3000);",
  "}",
  "// How many requests carrying this case's tag reached the local upstream fixture.",
  "function upstreamHits(cb) {",
  "  pm.sendRequest({ url: UPSTREAM + '/__relay/hits?tag=' + encodeURIComponent(ID), method: 'GET' }, function (err, res) {",
  "    var ok = !err && res && res.code === 200;",
  "    pm.test('the relay upstream fixture answers on ' + UPSTREAM, function () { pm.expect(ok, 'start it with: node tests/e2e/api/runners/relay-upstream-fixture.mjs (' + (err ? String(err) : res.code) + ')').to.be.true; });",
  "    cb(ok ? res.json().hits : -1);",
  "  });",
  "}",
  "function neverRelayed(label) { upstreamHits(function (hits) { pm.test(label + ': the upstream was never called', function () { pm.expect(hits).to.eql(0); }); }); }",
  "// A management or log API call on this gateway; body is an object or a raw string.",
  "function api(method, path, body, cb) {",
  "  var req = { url: BASE + path, method: method, header: { 'Content-Type': 'application/json' } };",
  "  if (body !== undefined) { req.body = { mode: 'raw', raw: typeof body === 'string' ? body : JSON.stringify(body) }; }",
  "  pm.sendRequest(req, function (err, res) { cb(err, res, (function () { try { return res.json(); } catch (e) { return {}; } })()); });",
  "}",
  "// Job rows for one upstream job id, polled until at least want exist or the attempts run out.",
  "function jobRows(jobId, want, check, attempt) {",
  "  attempt = attempt || 0;",
  "  pm.sendRequest({ url: BASE + '/api/relay-logs?kinds=job&job_id=' + encodeURIComponent(jobId) + '&limit=10', method: 'GET' }, function (err, res) {",
  "    if (err || res.code !== 200) { return pm.test('job rows lookup answers 200', function () { pm.expect.fail(err ? String(err) : res.code + ' ' + res.text()); }); }",
  "    var logs = res.json().logs || [];",
  "    if (logs.length >= want || attempt >= 45) { return check(logs); }",
  "    setTimeout(function () { jobRows(jobId, want, check, attempt + 1); }, 2000);",
  "  });",
  "}",
  "// This gateway's /metrics lines (no buckets) that contain match, filtered by the fixture so a long-lived gateway's multi-MB exposition stays out of the report.",
  "function metricLines(match, cb) {",
  "  var spec = { url: BASE + '/metrics', token: pm.variables.get('setupToken') || 'bifrost-e2e-setup-token', match: match };",
  "  pm.sendRequest({ url: UPSTREAM + '/__relay/metrics', method: 'POST', header: { 'Content-Type': 'application/json' }, body: { mode: 'raw', raw: JSON.stringify(spec) } }, function (err, res) {",
  "    var out = (!err && res.code === 200) ? res.json() : {};",
  "    pm.test('GET /metrics answers 200', function () { pm.expect(out.status, err ? String(err) : res.text()).to.eql(200); });",
  "    cb(out.lines || []);",
  "  });",
  "}",
  "// The fixture's own counters and inspection routes.",
  "function fixture(path, cb) { pm.sendRequest({ url: UPSTREAM + path, method: 'GET' }, function (err, res) { cb(err || res.code !== 200 ? {} : res.json()); }); }",
];

// Replaces the marker line in every script that carries it with HELPERS, and returns how many
// scripts it changed. Running it again changes nothing.
export function injectRelayHelpers(collection) {
  let injected = 0;
  const visit = (events) => {
    for (const event of events || []) {
      const exec = event && event.script && event.script.exec;
      if (!Array.isArray(exec) || !exec.includes(HELPERS_MARKER)) continue;
      event.script.exec = exec.flatMap((line) => (line === HELPERS_MARKER ? HELPERS : [line]));
      injected++;
    }
  };
  const walk = (items) => {
    for (const item of items || []) {
      visit(item.event);
      walk(item.item);
    }
  };
  visit(collection.event);
  walk(collection.item);
  return injected;
}

// ---- Request builders (the same shapes the static relay cases use) ----

// A fresh id per request, sent as x-request-id and as ?tag=.
export const mint = (suffix) => [
  "// A fresh id per request: sent as x-request-id (the Relay Logs row id) and as ?tag= (the upstream fixture's hit counter).",
  `pm.variables.set('relayReqId', 'relay-' + Date.now() + '-' + Math.floor(Math.random() * 1e6) + '-${suffix}');`,
];

const headerList = (h) => Object.entries(h || {}).map(([key, value]) => ({ key, value }));

// Splits "{{baseUrl}}/a/b?x=1&y" into a Postman url object.
export function urlObject(raw) {
  const [pathPart, query] = raw.split(/\?(.*)/s);
  const rest = pathPart.replace(/^\{\{baseUrl\}\}/, "");
  const url = { raw, host: ["{{baseUrl}}"], path: rest.split("/").filter((s, i) => !(i === 0 && s === "")) };
  if (query !== undefined && query !== "") {
    url.query = query.split("&").map((part) => {
      const i = part.indexOf("=");
      return i < 0 ? { key: part, value: null } : { key: part.slice(0, i), value: part.slice(i + 1) };
    });
  }
  return url;
}

function item({ name, method, url, headers, body, pre, test, noRedirect }) {
  const event = [];
  if (pre && pre.length) event.push({ listen: "prerequest", script: { type: "text/javascript", exec: pre } });
  event.push({ listen: "test", script: { type: "text/javascript", exec: [HELPERS_MARKER, ...test] } });
  const request = { method, header: headerList(headers), url: urlObject(url) };
  if (body !== undefined) request.body = { mode: "raw", raw: typeof body === "string" ? body : JSON.stringify(body, null, 2) };
  const out = { name: name + SUFFIX, event, request };
  if (noRedirect) out.protocolProfileBehavior = { followRedirects: false };
  return out;
}

// A call through a relay; x-request-id and ?tag= carry this case's id.
function relayCall({ name, tag, method = "POST", relay, path, query, headers = {}, body, test, pre = [], noRedirect }) {
  const q = [query, "tag={{relayReqId}}"].filter(Boolean).join("&");
  const url = `{{baseUrl}}/relay/${relay}${path}?${q}`;
  return item({ name, method, url, headers: { "x-request-id": "{{relayReqId}}", ...headers }, body, pre: [...mint(tag), ...pre], test, noRedirect });
}

// A management API call on this gateway.
function apiCall({ name, method = "GET", path, body, test, pre }) {
  return item({ name, method, url: `{{baseUrl}}${path}`, headers: { "Content-Type": "application/json" }, body, pre, test });
}

// A check that sends nothing of its own: GET /health carries the test script.
function check({ name, test }) {
  return item({ name, method: "GET", url: "{{baseUrl}}/health", headers: {}, test });
}

const J = { "Content-Type": "application/json" };
// The harness's shared content check reads a data array, so an object reply always carries one.
const withData = (r) => (r.body && typeof r.body === "object" && !Array.isArray(r.body) && !("data" in r.body) ? { ...r, body: { ...r.body, data: [] } } : r);
// A request body carrying the fixture's __reply program, plus any top-level fields.
const reply = (r, extra = {}) => JSON.stringify({ ...extra, __reply: withData(r) });
const sq = (s) => `'${s.replace(/\\/g, "\\\\").replace(/'/g, "\\'")}'`;
const refusedLines = (label, text) => [`pm.test('${label}: the refusal explains why', function () { pm.expect(errMsg(), pm.response.text()).to.include(${JSON.stringify(text)}); });`, `neverRelayed('${label}');`];

// ---- Management API: one refused create per validation rule ----

const job = (extra = {}) => ({ id_path: "id", poll: { method: "GET", path: "/v1/jobs2/check/{{job.id}}" }, status_path: "status", succeeded: ["done"], interval_seconds: 2, timeout_seconds: 60, ...extra });
const jobRoute = (extra = {}, route = {}) => ({ method: "POST", path: "/v1/jobs2/start", job: job(extra), ...route });

// [what is wrong, change to a valid relay config, text the 400 message must contain]
export const VALIDATION_RULES = [
  ["name with a space", (c) => ({ ...c, name: c.name + " x" }), "must match"],
  ["name ..", (c) => ({ ...c, name: ".." }), "must match"],
  ["name longer than 64 characters", (c) => ({ ...c, name: "n".repeat(65) }), "must match"],
  ["base_url that is not http(s)", (c) => ({ ...c, base_url: "ftp://127.0.0.1:8783" }), "must be an absolute http(s) URL"],
  ["base_url with a query string", (c) => ({ ...c, base_url: "http://127.0.0.1:8783/v1?x=1" }), "must not carry a query, fragment or user info"],
  ["base_url with user info", (c) => ({ ...c, base_url: "http://u:p@127.0.0.1:8783" }), "must not carry a query, fragment or user info"],
  ["extra_headers naming one header under two spellings", (c) => ({ ...c, extra_headers: { "X-Team": "a", "x-team": "b" } }), "under two spellings"],
  ["an extra header name that is not a valid header name", (c) => ({ ...c, extra_headers: { "Bad Header": "a" } }), "is not a valid header name"],
  ["bearer auth with no credential", (c) => { const o = { ...c }; delete o.credential; return o; }, "credential.value is required for auth.type bearer"],
  ["aws_sigv4 auth with no region", (c) => ({ ...c, auth: { type: "aws_sigv4", aws: { service: "textract" } }, credential: { aws: {} } }), "credential.aws.region is required"],
  ["aws_sigv4 auth with an invalid service name", (c) => ({ ...c, auth: { type: "aws_sigv4", aws: { service: "Text Ract" } }, credential: { aws: { region: "us-east-1" } } }), "must match [a-z0-9-]+"],
  ["an unsupported auth type", (c) => ({ ...c, auth: { type: "digest" } }), "auth.type: unsupported value"],
  ["header auth with no header_name", (c) => ({ ...c, auth: { type: "header" } }), "auth.header_name is required"],
  ["query auth with no query_param", (c) => ({ ...c, auth: { type: "query" } }), "auth.query_param is required"],
  ["a negative network timeout", (c) => ({ ...c, network: { timeout_seconds: -1 } }), "must not be negative"],
  ["a CA certificate that does not parse", (c) => ({ ...c, network: { ca_cert_pem: "not a certificate" } }), "ca_cert_pem"],
  ["a negative cost_per_request", (c) => ({ ...c, usage: { cost_per_request: -0.01 } }), "usage.cost_per_request must not be negative"],
  ["a negative cost_per_unit", (c) => ({ ...c, usage: { units_path: "n", cost_per_unit: -1 } }), "usage.cost_per_unit must not be negative"],
  ["units_block_size of zero", (c) => ({ ...c, usage: { units_path: "n", cost_per_unit: 1, units_block_size: 0 } }), "units_block_size must be above zero"],
  ["a negative units_minimum", (c) => ({ ...c, usage: { units_path: "n", cost_per_unit: 1, units_minimum: -1 } }), "units_minimum must not be negative"],
  ["cost_per_unit with no units_path", (c) => ({ ...c, usage: { cost_per_unit: 0.01 } }), "units_path is required with cost_per_unit"],
  ["a route that switches to a path measure without a path while the relay bills a body measure", (c) => ({ ...c, usage: { units_measure: "body_bytes", cost_per_unit: 0.01 }, routes: [{ method: "POST", path: "/v1/echo", usage: { units_measure: "chars" } }] }), "routes[0].usage.units_path is required"],
  ["an unsupported units_measure", (c) => ({ ...c, usage: { units_measure: "words" } }), "units_measure: unsupported value"],
  ["an unsupported units_source", (c) => ({ ...c, usage: { units_source: "both" } }), "units_source: unsupported value"],
  ["an unsupported model_source", (c) => ({ ...c, usage: { model_source: "header" } }), "model_source: unsupported value"],
  ["a route with an unsupported method", (c) => ({ ...c, routes: [{ method: "TRACE", path: "/v1/echo" }] }), "unsupported method"],
  ["a route path that does not start with /", (c) => ({ ...c, routes: [{ method: "POST", path: "v1/echo" }] }), "must start with /"],
  ["a route path with two **", (c) => ({ ...c, routes: [{ method: "POST", path: "/**/x/**" }] }), "** is only allowed once"],
  ["a route path with a malformed glob", (c) => ({ ...c, routes: [{ method: "POST", path: "/v1/[abc" }] }), "syntax error in pattern"],
  ["a route header matcher named twice under different case", (c) => ({ ...c, routes: [{ method: "POST", path: "/v1/echo", headers: { "X-Op": "a", "x-op": "b" } }] }), "duplicates a header name"],
  ["a route header matcher with an empty value", (c) => ({ ...c, routes: [{ method: "POST", path: "/v1/echo", headers: { "X-Op": " " } }] }), "a value glob is required"],
  ["a route header matcher with a malformed glob", (c) => ({ ...c, routes: [{ method: "POST", path: "/v1/echo", headers: { "X-Op": "[a" } }] }), "syntax error in pattern"],
  ["two identical routes", (c) => ({ ...c, routes: [{ method: "POST", path: "/v1/echo" }, { method: "post", path: "/v1/echo" }] }), "routes[1] repeats routes[0]"],
  ["a streaming path that does not start with /", (c) => ({ ...c, streaming: { paths: ["stream/*"] } }), "streaming.paths[0]"],
  ["body_overrides naming one parent under two letter cases", (c) => ({ ...c, body_overrides: { "options.depth": 1, "Options.mode": 2 } }), "name one parent field under different letter case"],
  ["body_overrides naming one field under two letter cases", (c) => ({ ...c, body_overrides: { mode: 1, Mode: 2 } }), "name one field under different letter case"],
  ["body_overrides setting a field inside another forced field", (c) => ({ ...c, body_overrides: { options: {}, "options.depth": 1 } }), "sets a field inside"],
  // __RAW__ is swapped for the bare number, which JSON.stringify cannot write.
  ["body_overrides with a number out of range", (c) => ({ ...c, body_overrides: { n: "__RAW__1e400" } }), "too large or too small"],
  ["body_overrides with a key that is not a plain object key", (c) => ({ ...c, body_overrides: { "a b": 1 } }), "is not a path of object keys"],
  ["a route body override that clashes in case with the relay one", (c) => ({ ...c, body_overrides: { "opts.a": 1 }, routes: [{ method: "POST", path: "/v1/echo", body_overrides: { "Opts.b": 2 } }] }), "(with the endpoint's)"],
  ["a job whose interval is not shorter than its timeout", (c) => ({ ...c, routes: [jobRoute({ interval_seconds: 60, timeout_seconds: 60 })] }), "must be shorter than the job"],
  ["a job whose default interval is not shorter than its timeout", (c) => ({ ...c, routes: [jobRoute({ interval_seconds: 0, timeout_seconds: 20 })] }), "the default"],
  ["a job timeout longer than 7 days", (c) => ({ ...c, routes: [jobRoute({ timeout_seconds: 7 * 24 * 3600 + 1 })] }), "at most 604800"],
  ["a job with negative interval", (c) => ({ ...c, routes: [jobRoute({ interval_seconds: -1 })] }), "must not be negative"],
  ["a job with no id_path", (c) => ({ ...c, routes: [jobRoute({ id_path: " " })] }), "id_path is required"],
  ["a job with no status_path", (c) => ({ ...c, routes: [jobRoute({ status_path: "" })] }), "status_path is required"],
  ["a job with no succeeded status", (c) => ({ ...c, routes: [jobRoute({ succeeded: [] })] }), "succeeded must name at least one status"],
  ["a job status listed as both succeeded and failed", (c) => ({ ...c, routes: [jobRoute({ succeeded: ["done"], failed: ["DONE"] })] }), "listed as both succeeded and failed"],
  ["a job status padded with spaces", (c) => ({ ...c, routes: [jobRoute({ succeeded: [" done"] })] }), "no surrounding spaces"],
  ["a job route that streams", (c) => ({ ...c, routes: [jobRoute({}, { stream: true })] }), "stream: true does not apply"],
  ["a job route that is not billable", (c) => ({ ...c, routes: [jobRoute({}, { billable: false })] }), "must be billable"],
  ["a GET job route without billable true", (c) => ({ ...c, routes: [jobRoute({}, { method: "GET" })] }), "must set billable: true"],
  ["a job poll path that does not start with /", (c) => ({ ...c, routes: [jobRoute({ poll: { method: "GET", path: "v1/jobs/{{job.id}}" } })] }), "must start with /"],
  ["a job poll with an unsupported method", (c) => ({ ...c, routes: [jobRoute({ poll: { method: "TRACE", path: "/v1/jobs/{{job.id}}" } })] }), "poll.method: unsupported method"],
  ["a job poll body naming the job id in a JSON key", (c) => ({ ...c, routes: [jobRoute({ poll: { method: "POST", path: "/v1/jobs", body: '{"{{job.id}}":1}' } })] }), "cannot be part of a JSON key"],
  ["a job poll naming one header under two spellings", (c) => ({ ...c, routes: [jobRoute({ poll: { method: "GET", path: "/v1/jobs/{{job.id}}", headers: { "X-Op": "a", "x-op": "b" } } })] }), "are the same header"],
  ["a job poll header the relay never sends", (c) => ({ ...c, routes: [jobRoute({ poll: { method: "GET", path: "/v1/jobs", headers: { Host: "{{job.id}}" } } })] }), "is never sent"],
  ["a job poll header with no value", (c) => ({ ...c, routes: [jobRoute({ poll: { method: "GET", path: "/v1/jobs/{{job.id}}", headers: { "X-Op": " " } } })] }), "has no value"],
  ["a job poll that sends the relay's custom auth header", (c) => ({ ...c, auth: { type: "header", header_name: "X-Upstream-Key" }, routes: [jobRoute({ poll: { method: "GET", path: "/v1/jobs/{{job.id}}", headers: { "X-Upstream-Key": "k" } } })] }), "carries a credential"],
  ["a job poll query that names the relay's query auth parameter", (c) => ({ ...c, auth: { type: "query", query_param: "license" }, routes: [jobRoute({ poll: { method: "GET", path: "/v1/jobs/{{job.id}}?license=x" } })] }), "carries a credential"],
  ["a job that bills request-side units at settlement", (c) => ({ ...c, routes: [jobRoute({ usage: { units_source: "request", units_path: "n", cost_per_unit: 0.01 } })] }), "request-side units are not billed at settlement"],
  ["a job unit price with no units path anywhere", (c) => ({ ...c, routes: [jobRoute({ usage: { cost_per_unit: 0.01 } })] }), "units_path is required"],
];

// The name is unique per run, and a relay that is wrongly saved is deleted again so it cannot leak.
function validationCase([desc, patch, want], i) {
  const base = { name: "{{relayGapInvalidName}}", base_url: "http://127.0.0.1:8783", auth: { type: "bearer" }, credential: { value: "sk-x" }, routes: [{ method: "POST", path: "/v1/echo" }] };
  return apiCall({
    name: `[EXPECT-400] refused: ${desc}`,
    method: "POST",
    path: "/api/relays",
    pre: [`pm.variables.set('relayGapInvalidName', 'harness-relay-gap-invalid-${i}-' + Date.now());`],
    body: JSON.stringify(patch(base), null, 2).replace('"__RAW__1e400"', "1e400"),
    test: [
      "if (pm.response.code === 201) { api('DELETE', '/api/relays/' + encodeURIComponent(json().name), undefined, function () {}); }",
      "expectStatus(400, 'refused');",
      `pm.test('refused: the message names the rule', function () { pm.expect(errMsg(), pm.response.text()).to.include(${JSON.stringify(want)}); });`,
    ],
  });
}

// ---- Usage preview of an unsaved config ----

const PREVIEW_CFG = {
  name: "harness-relay-gap-preview-unsaved",
  base_url: "http://127.0.0.1:8783",
  credential: { value: "sk-harness-relay-preview" },
  usage: { cost_per_request: 0.001, units_path: "usage.credits", cost_per_unit: 0.008 },
  routes: [
    { method: "POST", path: "/search", headers: { "x-tier": "premium" }, usage: { cost_per_request: 0.05 } },
    { method: "POST", path: "/search" },
    { method: "POST", path: "/off", enabled: false },
    { method: "POST", path: "/tts", usage: { cost_per_request: 0, units_source: "request", units_measure: "chars", units_path: "Text", cost_per_unit: 0.01, units_block_size: 100, units_minimum: 3 } },
    { method: "POST", path: "/sse" },
  ],
};

// Each row POSTs {config, sample} (config PREVIEW_CFG unless set; null sends none) and asserts on
// the preview `out` (dump is its text). A row with body sends that body and checks only the status.
export const PREVIEW_CASES = [
  { name: "a header matcher in the sample selects its route and price on an unsaved config", sample: { method: "POST", path: "/search", headers: { "X-Tier": "premium" }, response_body: '{"usage":{"credits":4}}' }, expect: "pm.test('header route: route 0 at $0.05 + 4 x $0.008', function () { pm.expect(out.route_index, dump).to.eql(0); pm.expect(near(out.cost, 0.082), dump).to.be.true; pm.expect(out.counted, dump).to.eql(true); });" },
  { name: "without the header the sample falls to the generic route", sample: { method: "POST", path: "/search", response_body: '{"usage":{"credits":4}}' }, expect: "pm.test('generic route: route 1 at $0.033', function () { pm.expect(out.route_index, dump).to.eql(1); pm.expect(near(out.cost, 0.033), dump).to.be.true; });" },
  { name: "a sample no route takes is not allowed and costs nothing", sample: { method: "POST", path: "/nothing", response_body: "{}" }, expect: "pm.test('no route: not allowed, $0', function () { pm.expect(out.route_index, dump).to.eql(null); pm.expect(out.route_allowed, dump).to.eql(false); pm.expect(out.cost, dump).to.eql(0); pm.expect(out.notes.length, dump).to.be.above(0); });" },
  { name: "a sample on a disabled route names the route and is not allowed", sample: { method: "POST", path: "/off", response_body: "{}" }, expect: "pm.test('disabled: route 2, not allowed, $0', function () { pm.expect(out.route_index, dump).to.eql(2); pm.expect(out.route_allowed, dump).to.eql(false); pm.expect(out.cost, dump).to.eql(0); });" },
  { name: "a non-2xx sample is not counted and costs nothing", sample: { method: "POST", path: "/search", status_code: 500, response_body: '{"usage":{"credits":4}}' }, expect: "pm.test('non-2xx: not counted, $0', function () { pm.expect(out.counted, dump).to.eql(false); pm.expect(out.cost, dump).to.eql(0); });" },
  { name: "request-side characters bill per started block with a minimum: 250 characters are 3 blocks", sample: { method: "POST", path: "/tts", request_body: JSON.stringify({ Text: "x".repeat(250) }), response_body: "{}" }, expect: "pm.test('blocks: 250 units, 3 billed, $0.03', function () { pm.expect(out.units, dump).to.eql(250); pm.expect(out.units_billed, dump).to.eql(3); pm.expect(near(out.cost, 0.03), dump).to.be.true; });" },
  { name: "request-side characters above the minimum: 1,250 characters are 13 blocks", sample: { method: "POST", path: "/tts", request_body: JSON.stringify({ Text: "x".repeat(1250) }), response_body: "{}" }, expect: "pm.test('blocks: 13 billed, $0.13', function () { pm.expect(out.units_billed, dump).to.eql(13); pm.expect(near(out.cost, 0.13), dump).to.be.true; });" },
  { name: "an SSE sample is priced from its last event that carries usage", sample: { method: "POST", path: "/sse", response_content_type: "text/event-stream", response_body: 'data: {"usage":{"credits":1}}\n\ndata: {"usage":{"credits":3}}\n\n' }, expect: "pm.test('sse: streams, 3 units, $0.025', function () { pm.expect(out.streams, dump).to.eql(true); pm.expect(out.units, dump).to.eql(3); pm.expect(near(out.cost, 0.025), dump).to.be.true; });" },
  { name: "a reply with no units still costs the fee, and a note says why the units are empty", sample: { method: "POST", path: "/search", response_body: "{}" }, expect: "pm.test('no units: $0.001 with a note', function () { pm.expect(near(out.cost, 0.001), dump).to.be.true; pm.expect(out.units, dump).to.eql(null); pm.expect(out.notes.join(' '), dump).to.include('no quantity could be read'); });" },
  { name: "an invalid config is refused with its validation error", status: 400, config: { ...PREVIEW_CFG, usage: { cost_per_unit: 1 } }, sample: { method: "POST", path: "/search" }, expect: "pm.test('invalid config: names the rule', function () { pm.expect(errMsg(), dump).to.include('units_path is required with cost_per_unit'); });" },
  { name: "a sample with neither a config nor a saved relay's name is refused", status: 400, config: null, sample: { method: "POST", path: "/x" }, expect: "pm.test('nothing to price: the message says what is missing', function () { pm.expect(errMsg(), dump).to.include('config or the name of a saved endpoint is required'); });" },
  { name: "a preview of a saved relay that does not exist answers not found", status: 404, body: { name: "harness-relay-gap-preview-missing", sample: { method: "POST", path: "/x" } } },
  { name: "a non-JSON response body with no content type is refused", status: 400, sample: { method: "POST", path: "/search", response_body: "<xml/>" }, expect: "pm.test('content type: the message asks for one', function () { pm.expect(errMsg(), dump).to.include('response_content_type is required'); });" },
];

function previewCase({ name, status = 200, config = PREVIEW_CFG, sample, expect, body }) {
  const label = body ? "missing relay" : "preview";
  return apiCall({
    name: (status === 200 ? "" : `[EXPECT-${status}] `) + name,
    method: "POST",
    path: "/api/relays/usage-preview",
    body: body || (config === null ? { sample } : { config, sample }),
    test: body ? [`expectStatus(${status}, '${label}');`] : [`expectStatus(${status}, '${label}');`, "var out = json(); var dump = pm.response.text();", expect],
  });
}

// ---- Pricing: one call on the run-scoped pricing relay and the Relay Logs row it produces ----

const PRICE = "{{relayGapPrice}}";
const SSML = { "Content-Type": "application/ssml+xml" };

// Row fields: reply (the fixture's __reply, plus replyExtra top-level fields) or a raw body;
// cost/units assert the row (null asserts none, undefined skips the row); rowMore runs inside the
// row check; refused asserts a 400 naming the reason before any upstream call; also runs first.
export const PRICING_BEFORE_CHARS = [
  { name: "the number at units_path is billed: 3 units x $0.01 plus the fee", label: "value", path: "/value", reply: { body: { usage: { n: 3 } } }, cost: 0.031, units: 3, rowMore: ["pm.expect(near(row.billed_units, 3), dump).to.be.true;"] },
  { name: "a negative count is no quantity, so only the fee is charged", label: "negative", path: "/value", reply: { body: { usage: { n: -3 } } }, cost: 0.001, units: null },
  { name: "a JSON reply labelled text/plain is not read for units", label: "text/plain", path: "/value", reply: { body: { usage: { n: 3 } }, content_type: "text/plain" }, cost: 0.001, units: null },
  { name: "a reply with no content type that parses as JSON is read for units", label: "no content type", path: "/value", reply: { body: { usage: { n: 3 } }, content_type: "" }, cost: 0.031, units: 3 },
  { name: "[EXPECT-201] a 201 reply is charged like a 200", label: "201", status: 201, path: "/value", reply: { status: 201, body: { usage: { n: 3 } } }, cost: 0.031, units: 3 },
  { name: "[EXPECT-404] an upstream 404 is passed back as is, logged as an error and not charged", label: "404", status: 404, path: "/value", reply: { status: 404, body: { usage: { n: 3 } } }, also: ["pm.test('404: the upstream body came back unchanged', function () { pm.expect(json().usage.n, pm.response.text()).to.eql(3); });"], cost: null, units: null, rowMore: ["pm.expect(row.status, dump).to.eql('error');", "pm.expect(row.status_code, dump).to.eql(404);"] },
  { name: "[EXPECT-302] a 3xx reply is passed back and not charged", label: "302", status: 302, path: "/value", noRedirect: true, reply: { status: 302, body: { usage: { n: 3 } } }, cost: null, units: null, rowMore: ["pm.expect(row.status_code, dump).to.eql(302);"] },
];

export const PRICING_AFTER_CHARS = [
  { name: "body_bytes measures the whole response: 10 bytes x $0.0001 plus the fee", label: "response bytes", path: "/rbytes", reply: { body: "0123456789", content_type: "audio/mpeg" }, also: ["pm.test('response bytes: the reply is the 10 bytes asked for', function () { pm.expect(pm.response.text()).to.eql('0123456789'); });"], cost: 0.002, units: 10 },
  { name: "body_chars measures the request in any format: 20 characters of SSML, with é counted once", label: "request chars", path: "/qchars", headers: SSML, body: "<speak>héllo</speak>", cost: 0.201, units: 20 },
  { name: "body_bytes measures the request's bytes, so é counts twice: 21 bytes", label: "request bytes", path: "/qbytes", headers: SSML, body: "<speak>héllo</speak>", cost: 0.211, units: 21 },
  { name: "a #.text batch path sums the characters of every item: ab plus cdé is 5", label: "batch", path: "/qbatch", body: '[{"text": "ab"}, {"text": "cdé"}]', cost: 0.051, units: 5 },
  { name: "a number at a request path is billed: n 7", label: "request value", path: "/qvalue", body: '{"n": 7}', cost: 0.071, units: 7 },
  { name: "a JSON request body sent as text/plain is measured all the same, so a label cannot dodge the charge", label: "mislabelled request", path: "/qvalue", headers: { "Content-Type": "text/plain" }, body: '{"n": 7}', cost: 0.071, units: 7 },
  { name: "[EXPECT-400] a request body naming the units key only in another letter case is refused", label: "case variant", status: 400, path: "/qvalue", body: '{"N": 7}', refused: "with different letter case" },
  { name: "[EXPECT-400] a request body naming the units key twice is refused", label: "duplicate key", status: 400, path: "/qvalue", body: '{"n": 7, "n": 1}', refused: "names a key more than once" },
  { name: "[EXPECT-400] a request body that is not JSON is refused on a route billing a request path", label: "not JSON", status: 400, path: "/qvalue", headers: { "Content-Type": "text/plain" }, body: "n=7", refused: "which must be JSON" },
  { name: "a reported cost list is summed, skipping negatives, on top of the fee", label: "cost list", path: "/cost", reply: { body: { usage: { cost: [0.001, 0.002, -1] } } }, cost: 0.004, units: null, rowMore: ["pm.expect(near(row.extracted_cost, 0.003), dump).to.be.true;"] },
  { name: "ignore_upstream_cost drops the reported cost and keeps the fee", label: "ignore cost", relay: "{{relayGapPriceIgnore}}", path: "/cost", reply: { body: { usage: { cost: 0.5 } } }, cost: 0.001, units: null, rowMore: ["pm.expect(row.extracted_cost === undefined || row.extracted_cost === null, dump).to.be.true;"] },
  { name: "a model read from the body and a total_tokens_path are recorded but never priced", label: "model", path: "/model", reply: { body: { usage: { total: 42 } } }, replyExtra: { model: "nova-3" }, cost: 0.001, units: null, rowMore: ["pm.expect(row.model, dump).to.eql('nova-3');", "pm.expect(row.total_tokens, dump).to.eql(42);"] },
  { name: "a model read from a query parameter is recorded", label: "query model", path: "/qmodel", query: "model=nova-3", body: "{}", cost: 0.001, units: null, rowMore: ["pm.expect(row.model, dump).to.eql('nova-3');"] },
  { name: "[EXPECT-400] a body naming the model key twice in different case is refused", label: "model twice", status: 400, path: "/model", body: '{"model": "a", "Model": "b"}', refused: "must carry the model key at most once" },
  { name: "[EXPECT-400] a query naming the model parameter twice is refused", label: "query model twice", status: 400, path: "/qmodel", query: "model=a&model=b", body: "{}", refused: "must carry the model parameter at most once" },
  { name: "units_minimum applies only once a quantity was read: a reply without one costs the fee", label: "minimum, no units", path: "/min", reply: { body: { nothing: 1 } }, cost: 0.001, units: null },
  { name: "units_minimum lifts 1 unit read to 3 billed", label: "minimum", path: "/min", reply: { body: { usage: { n: 1 } } }, cost: 0.031, units: 1, rowMore: ["pm.expect(near(row.billed_units, 3), dump).to.be.true;"] },
  { name: "a GET route marked billable is charged", label: "billable GET", method: "GET", path: "/get", query: "reply=%7B%22body%22%3A%7B%22usage%22%3A%7B%22n%22%3A2%7D%2C%22data%22%3A%5B%5D%7D%7D", cost: 0.021, units: 2, rowMore: ["pm.expect(row.billable, dump).to.eql(true);"] },
  { name: "a PUT route left at its method default is relayed but not charged", label: "default PUT", method: "PUT", path: "/put", reply: { body: { usage: { n: 2 } } }, cost: null, units: null, rowMore: ["pm.expect(row.billable, dump).to.eql(false);"] },
  { name: "a DELETE route marked billable is charged", label: "billable DELETE", method: "DELETE", path: "/del", reply: { body: { usage: { n: 2 } } }, cost: 0.021, units: 2 },
  { name: "a gzip-compressed upstream reply is decoded, priced and returned uncompressed", label: "gzip reply", path: "/value", reply: { gzip: true, body: { usage: { n: 4 }, data: [] } }, also: ["pm.test('gzip reply: the caller gets plain JSON with no Content-Encoding', function () { pm.expect(pm.response.headers.get('Content-Encoding'), 'content-encoding').to.equal(undefined); pm.expect(json().usage.n, pm.response.text()).to.eql(4); });"], cost: 0.041, units: 4 },
];

// Asserts the call row's cost and units (null for none).
const rowCheck = (label, cost, units, more = []) => [
  "relayLog(function (row) {",
  `  pm.test('${label}: Relay Logs row', function () {`,
  "    var dump = JSON.stringify(row);",
  cost === null ? "    pm.expect(row.cost === undefined || row.cost === null, 'no cost: ' + dump).to.be.true;" : `    pm.expect(near(row.cost, ${cost}), 'cost should be ${cost}: ' + dump).to.be.true;`,
  units === null ? "    pm.expect(row.units === undefined || row.units === null, 'no units: ' + dump).to.be.true;" : `    pm.expect(near(row.units, ${units}), 'units should be ${units}: ' + dump).to.be.true;`,
  ...more.map((l) => "    " + l),
  "  });",
  "});",
];

// A body or reply means a JSON call unless the row names its own headers.
const callBody = (r) => (r.reply ? reply(r.reply, r.replyExtra) : r.body);
const callHeaders = (r) => r.headers || (callBody(r) !== undefined ? J : undefined);

function pricingCase(r, tag) {
  return relayCall({
    name: r.name,
    tag,
    method: r.method,
    relay: r.relay || PRICE,
    path: r.path,
    query: r.query,
    headers: callHeaders(r),
    body: callBody(r),
    noRedirect: r.noRedirect,
    test: [`expectStatus(${r.status || 200}, '${r.label}');`, ...(r.also || []), ...(r.refused ? refusedLines(r.label, r.refused) : []), ...(r.cost !== undefined ? rowCheck(r.label, r.cost, r.units, r.rowMore) : [])],
  });
}

// ---- Routing: which route of the run-scoped routing relay takes each call ----

const ROUTES = "{{relayGapRoutes}}";
const TEXT = { "Content-Type": "text/plain" };

// A row with route asserts that route priced the call at cost (and, with upstreamPath, the path
// the upstream saw). A row without one is refused: error is [test title, message text], and the
// upstream must not be called unless relayed is set. also runs last.
export const ROUTING_CASES = [
  { name: "an exact path takes the call ahead of the * and ** routes listed before it", label: "exact", method: "GET", path: "/r/latest", route: "/r/latest", cost: 0.002 },
  { name: "a * route takes one segment", label: "single star", method: "GET", path: "/r/abc", route: "/r/*", cost: 0.004 },
  { name: "a trailing ** takes deeper paths", label: "deep", method: "GET", path: "/r/abc/def", route: "/r/**", cost: 0.009 },
  { name: "a trailing ** also takes the bare prefix", label: "bare prefix", method: "GET", path: "/r", route: "/r/**", cost: 0.009 },
  { name: "a trailing slash is an extra segment, which only a trailing ** takes", label: "trailing slash", method: "GET", path: "/r/latest/", route: "/r/**", cost: 0.009 },
  { name: "? and [12] in a pattern match one character as in a shell", label: "char class", method: "GET", path: "/v1/items", route: "/v[12]/item?", cost: 0.001 },
  { name: "[EXPECT-404] a path outside the character class matches no route, reaches no upstream and writes no Relay Logs row", label: "no route", status: 404, method: "GET", path: "/v3/items", error: ["the message names the call", "no route for GET /v3/items"], also: ["rowsAfterWait(function (rows) { pm.test('no route: no Relay Logs row was written', function () { pm.expect(rows.length, JSON.stringify(rows)).to.eql(0); }); });"] },
  { name: "an encoded slash is decoded before matching, so /files/a%2Fb takes the two-segment route", label: "encoded slash", method: "GET", path: "/files/a%2Fb", route: "/files/*/*", cost: 0.003, upstreamPath: "/v1/program/files/a/b" },
  { name: "an encoded ? and # stay inside the path segment and never become a query or a fragment", label: "encoded delimiters", method: "GET", path: "/enc/a%3Fb%23c", route: "/enc/*", cost: 0.001, also: ["pm.test('encoded delimiters: the upstream path kept them encoded and saw only the tag in its query', function () { var j = json(); pm.expect(j.path, pm.response.text()).to.eql('/v1/program/enc/a%3Fb%23c'); pm.expect(Object.keys(j.query || {}), pm.response.text()).to.eql(['tag']); });"] },
  { name: "dot segments are resolved before routing, so /r/x/../latest takes the exact /r/latest route", label: "dot segments", method: "GET", path: "/r/x/../latest", route: "/r/latest", cost: 0.002, upstreamPath: "/v1/program/r/latest" },
  { name: "[EXPECT-404] a disabled /** catch-all refuses POST paths no narrower route takes", label: "disabled catch-all", status: 404, path: "/anything/else", headers: J, body: "{}", error: ["the refusal says the route is disabled", "is disabled"] },
  { name: "a narrower route under a disabled /** catch-all still answers", label: "under catch-all", path: "/open", headers: J, body: "{}", route: "/open", cost: 0.001 },
  { name: "[EXPECT-404] a doubled slash that tidies to a disabled route is refused as that route", label: "doubled slash", status: 404, method: "GET", path: "//hidden", error: ["the refusal names the disabled route, not a missing one", "is disabled"] },
  { name: "a header glob List* selects its route, and the header name ignores case", label: "header glob", path: "/op", headers: { "X-OP": "ListThings", ...TEXT }, body: "{}", route: "/op", cost: 0.005 },
  { name: "header values are case-sensitive, so listThings falls through to the route without matchers", label: "value case", path: "/op", headers: { "x-op": "listThings", ...TEXT }, body: "{}", route: "/op", cost: 0.001 },
  { name: "a * in a header glob spans the / in application/x-amz-json-1.1", label: "slash in value", path: "/op", headers: { "x-format": "application/x-amz-json-1.1", ...TEXT }, body: "{}", route: "/op", cost: 0.006 },
  { name: "an x-bf-eh copy of a header selects a route, and the row records the value that matched", label: "x-bf-eh matcher", path: "/op", headers: { "x-bf-eh-x-op": "ListViaEh", ...TEXT }, body: "{}", route: "/op", cost: 0.005, also: ["pm.test('x-bf-eh matcher: the upstream got the header under its own name', function () { pm.expect(upHeaders()['x-op'], pm.response.text()).to.eql('ListViaEh'); });", "relayLog(function (row) { pm.test('x-bf-eh matcher: request_headers keep the matched value', function () { pm.expect((row.request_headers || {})['x-op'], JSON.stringify(row)).to.eql('ListViaEh'); }); });"] },
  { name: "a caller's own header wins over its x-bf-eh copy, for routing and for the upstream", label: "own header wins", path: "/op", headers: { "x-op": "Other", "x-bf-eh-x-op": "ListViaEh", ...TEXT }, body: "{}", route: "/op", cost: 0.001, also: ["pm.test('own header wins: the upstream got the caller\\'s own value', function () { pm.expect(upHeaders()['x-op'], pm.response.text()).to.eql('Other'); });"] },
  { name: "[EXPECT-404] HEAD on a path only a GET route lists is an unlisted method", label: "head", status: 404, method: "HEAD", path: "/r/latest" },
  { name: "[EXPECT-404] a relay name nobody configured answers relay not found", label: "unknown relay", status: 404, relay: "harness-relay-gap-missing", path: "/v1/echo", headers: J, body: "{}", error: ["the message names it", 'relay "harness-relay-gap-missing" not found'], relayed: true },
];

function routingCase(r, tag) {
  const test = r.route
    ? [
        `expectStatus(200, '${r.label}');`,
        `relayLog(function (row) { pm.test('${r.label}: the ${r.route} route priced the call', function () { var dump = JSON.stringify(row); pm.expect(row.route_path, dump).to.eql(${JSON.stringify(r.route)}); pm.expect(near(row.cost, ${r.cost}), 'cost should be ${r.cost}: ' + dump).to.be.true; }); });`,
        ...(r.upstreamPath ? [`pm.test('${r.label}: the upstream received ${r.upstreamPath}', function () { pm.expect(json().path, pm.response.text()).to.eql(${JSON.stringify(r.upstreamPath)}); });`] : []),
      ]
    : [
        `expectStatus(${r.status}, '${r.label}');`,
        ...(r.error ? [`pm.test('${r.label}: ${r.error[0]}', function () { pm.expect(errMsg(), pm.response.text()).to.include(${sq(r.error[1])}); });`] : []),
        ...(r.relayed ? [] : [`neverRelayed('${r.label}');`]),
      ];
  return relayCall({ name: r.name, tag, method: r.method, relay: r.relay || ROUTES, path: r.path, headers: r.headers, body: r.body, test: [...test, ...(r.also || [])] });
}

// ---- Body overrides: the forced fields across JSON types, methods and content types ----

const BODY = "{{relayGapBody}}";
const FORCED = { s: "forced", num: 5, flt: 1.5, f: false, nul: null, obj: { a: 1 }, arr: [1, "x"] };

// kind forced: every forced value reaches the upstream with its JSON type (extra is one more line
// inside that test). kind client: sent through the fixture's /__relay/send, which can leave out
// Content-Type or gzip the body; lines see out (the fixture reply), echo and b (the body sent).
// kind refused: a 400 naming the reason before any upstream call.
const BODY_TYPES = [{ kind: "forced", name: "every JSON type and the nested path are set next to the caller's own fields", label: "types", headers: J, body: '{"q": 1}', extra: "  pm.expect(b.q).to.eql(1);" }];

const BODY_EMPTY = [
  { kind: "forced", name: "an empty PUT gets a created JSON body sent as application/json", label: "empty PUT", method: "PUT", extra: "  pm.expect(upHeaders()['content-type']).to.eql('application/json');" },
  { kind: "forced", name: "an empty PATCH keeps the caller's x-amz-json content type on the created body", label: "empty PATCH", method: "PATCH", headers: { "Content-Type": "application/x-amz-json-1.1" }, extra: "  pm.expect(upHeaders()['content-type']).to.eql('application/x-amz-json-1.1');" },
];

const BODY_SHAPES = [
  { kind: "forced", name: "a DELETE that carries a JSON body gets the forced fields", label: "DELETE body", method: "DELETE", headers: J, body: '{"q": 1}' },
  { kind: "forced", name: "a +json content type is accepted and kept", label: "+json", headers: { "Content-Type": "application/vnd.api+json" }, body: '{"q": 1}', extra: "  pm.expect(upHeaders()['content-type']).to.eql('application/vnd.api+json');" },
  { kind: "forced", name: "a -json content type with parameters is accepted", label: "-json", headers: { "Content-Type": "application/x-amz-json-1.0; charset=utf-8" }, body: '{"q": 1}' },
  { kind: "client", name: "a body sent with no content type is read as JSON and gets the forced fields", label: "no content type", spec: { method: "POST", json: { q: 1 } }, lines: ["pm.test('no content type: relayed with the forced fields and still no content type', function () { pm.expect(out.status, out.body).to.eql(200); pm.expect(b.q, out.body).to.eql(1); pm.expect(b.s, out.body).to.eql('forced'); pm.expect((echo.headers || {})['content-type'], out.body).to.equal(undefined); });"] },
  { kind: "client", name: "a gzip-compressed body is decompressed before the forced fields are set", label: "gzip body", spec: { method: "POST", gzip: true, headers: { "Content-Type": "application/json" }, json: { q: "zipped" } }, lines: ["pm.test('gzip body: the upstream got the decompressed body with the forced fields', function () { pm.expect(out.status, out.body).to.eql(200); pm.expect(b.q, out.body).to.eql('zipped'); pm.expect(b.num, out.body).to.eql(5); pm.expect((echo.headers || {})['content-encoding'], out.body).to.equal(undefined); });"] },
  { kind: "client", name: "a 2 MB body gets the forced fields and reaches the upstream whole", label: "large body", spec: { method: "POST", headers: { "Content-Type": "application/json" }, blob_bytes: 2097152, json: { q: 1 }, summary: true }, lines: ["pm.test('large body: the upstream echoed more than 2 MB ending in the forced fields', function () { pm.expect(out.status, JSON.stringify(out)).to.eql(200); pm.expect(out.bytes).to.be.above(2097152); pm.expect(out.tail, out.tail).to.include('forced'); pm.expect(out.tail, out.tail).to.include('leaf'); });"] },
  { kind: "refused", name: "[EXPECT-400] application/jsonp is not a JSON type the relay can set fields in", label: "jsonp", headers: { "Content-Type": "application/jsonp" }, body: '{"q": 1}', refused: "must be a JSON object sent as Content-Type: application/json" },
  { kind: "refused", name: "[EXPECT-400] a JSON list is refused, since fields can only be set in an object", label: "list", headers: J, body: "[1, 2]", refused: "must be a JSON object" },
  { kind: "refused", name: "[EXPECT-400] a non-object where the forced path continues is refused", label: "non-object on path", headers: J, body: '{"deep": 5}', refused: "holds a non-object at deep" },
  { kind: "refused", name: "[EXPECT-400] a parent of a forced path in another letter case is refused", label: "case-variant parent", headers: J, body: '{"Deep": {}}', refused: "names deep with different letter case" },
  { kind: "refused", name: "[EXPECT-400] a body nested 1001 levels is refused on a route with forced fields", label: "depth", headers: J, pre: ["pm.variables.set('relayGapDeepBody', new Array(1002).join('{\"a\":') + '1' + new Array(1002).join('}'));"], body: "{{relayGapDeepBody}}", refused: "nests deeper than 1000 levels" },
];

const expectForced = (label, extra = "") => [
  `pm.test('${label}: every forced value reached the upstream with its JSON type', function () {`,
  "  var b = sentBody();",
  `  var want = ${JSON.stringify(FORCED)};`,
  "  Object.keys(want).forEach(function (k) { pm.expect(b[k], k + ': ' + pm.response.text()).to.eql(want[k]); });",
  "  pm.expect(b.deep && b.deep.er && b.deep.er.leaf, 'deep.er.leaf: ' + pm.response.text()).to.eql(true);",
  extra,
  "});",
];

const viaClient = (label, spec, lines) => [
  `var spec = ${JSON.stringify(spec)};`,
  "spec.url = BASE + '/relay/' + pm.collectionVariables.get('relayGapBody') + '/v1/echo';",
  "pm.sendRequest({ url: UPSTREAM + '/__relay/send', method: 'POST', header: { 'Content-Type': 'application/json' }, body: { mode: 'raw', raw: JSON.stringify(spec) } }, function (err, res) {",
  `  pm.test('${label}: the fixture client reached the relay', function () { pm.expect(err ? String(err) : res.code, err ? '' : res.text()).to.eql(200); });`,
  "  var out = res.json(); var echo = {}; try { echo = JSON.parse(out.body); } catch (e) {}",
  "  var b = {}; try { b = JSON.parse(echo.body || '{}'); } catch (e) {}",
  ...lines.map((l) => "  " + l),
  "});",
];

function bodyCase(r, tag) {
  if (r.kind === "client") return check({ name: r.name, test: viaClient(r.label, r.spec, r.lines) });
  const test = r.kind === "forced" ? [`expectStatus(200, '${r.label}');`, ...expectForced(r.label, r.extra)] : [`expectStatus(400, '${r.label}');`, ...refusedLines(r.label, r.refused)];
  return relayCall({ name: r.name, tag, method: r.method, relay: BODY, path: "/v1/echo", headers: r.headers, body: r.body, pre: r.pre, test });
}

// ---- Where each block lands in folder 285 ----

const F_PRICING = "Pricing: response and request measures, reported cost policy, model and tokens, non-2xx replies and method defaults";
const F_ROUTING = "Routing: specificity, globs, encoded and dot-segment paths, disabled catch-alls, header globs and x-bf-eh matchers";
const F_BODY = "Body overrides: every JSON type, nested paths, methods, content types, compressed and large bodies";
const F_VALIDATION = "Management API: every validation rule refuses a bad relay with 400";
const F_PREVIEW = "Usage preview: unsaved configs, header matchers, disabled and unmatched routes, blocks, SSE samples and refusals";

// before names the static case (without the suffix) the block goes in front of; null appends.
// tag prefixes the per-request id so a Relay Logs row can be traced back to its row here.
export const RELAY_MATRIX_BLOCKS = [
  { folder: F_ROUTING, before: "[EXPECT-204] cleanup: delete the run-scoped routing relay", tag: "route", rows: ROUTING_CASES, build: routingCase },
  { folder: F_PRICING, before: "chars measures the characters of a response string: the echoed request body", tag: "price", rows: PRICING_BEFORE_CHARS, build: pricingCase },
  { folder: F_PRICING, before: "[EXPECT-204] cleanup: delete the run-scoped pricing relays", tag: "price-b", rows: PRICING_AFTER_CHARS, build: pricingCase },
  { folder: F_BODY, before: "a nested override merges into an object the caller sent, keeping its other keys", tag: "body", rows: BODY_TYPES, build: bodyCase },
  { folder: F_BODY, before: "an empty DELETE is sent with no body", tag: "body-empty", rows: BODY_EMPTY, build: bodyCase },
  { folder: F_BODY, before: "the forced field is what request-side pricing measures and what the Relay Logs row records", tag: "body-shape", rows: BODY_SHAPES, build: bodyCase },
  { folder: F_VALIDATION, before: null, tag: "invalid", rows: VALIDATION_RULES, build: (r, _tag, i) => validationCase(r, i) },
  { folder: F_PREVIEW, before: null, tag: "preview", rows: PREVIEW_CASES, build: previewCase },
];

// The requests of one block, in order.
export function buildBlock(block) {
  return block.rows.map((r, i) => block.build(r, `${block.tag}${i + 1}`, i));
}

// Inserts every block into its subfolder of folder 285 and returns how many requests it added.
// Rows a previous pass inserted are dropped first, so running it again changes nothing.
export function spliceRelayMatrix(collection) {
  const relays = (collection.item || []).find((entry) => (entry.name || "").startsWith(RELAY_FOLDER_PREFIX));
  if (!relays) throw new Error(`relay-e2e-matrix: folder "${RELAY_FOLDER_PREFIX}..." not found`);
  const built = RELAY_MATRIX_BLOCKS.map((block) => ({ block, items: buildBlock(block) }));
  const generated = new Set(built.flatMap(({ items }) => items.map((i) => i.name)));
  for (const sub of relays.item) if (Array.isArray(sub.item)) sub.item = sub.item.filter((i) => !generated.has(i.name));
  let added = 0;
  for (const { block, items } of built) {
    const sub = relays.item.find((entry) => entry.name === block.folder && Array.isArray(entry.item));
    if (!sub) throw new Error(`relay-e2e-matrix: subfolder "${block.folder}" not found`);
    let at = sub.item.length;
    if (block.before !== null) {
      at = sub.item.findIndex((i) => i.name === block.before + SUFFIX);
      if (at < 0) throw new Error(`relay-e2e-matrix: "${block.before}" not found in "${block.folder}"`);
    }
    sub.item.splice(at, 0, ...items);
    added += items.length;
  }
  return added;
}
