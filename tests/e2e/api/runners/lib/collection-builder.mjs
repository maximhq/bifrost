// Shared builders for the wiring/routing Postman collection generators.
//
// These emit the run-namespacing, exponential-backoff polling, cleanup-folder,
// and skip-on-missing-credential scaffolding that both the model-catalog and
// routing generators rely on. Everything here produces plain Postman v2.1 JSON;
// the generators add their scenario-specific request bodies and assertions.

import { writeFileSync } from "node:fs";

export const MAX_POLL_ATTEMPTS = 8;

// --------------------------------------------------------------------------- //
// Collection / folder / request-level event scripts (arrays of JS source lines)
// --------------------------------------------------------------------------- //

// Build a single run id for the whole run so every created resource is
// namespaced and parallel runs never collide. Runs before every request, so it
// only sets the id once.
export function collectionPrerequest() {
  return [
    "if (!pm.collectionVariables.get('run_id')) {",
    "  var seed = pm.variables.get('e2e_seed_prefix') || pm.environment.get('e2e_seed_prefix') || 'local';",
    "  var nonce = Date.now().toString(36) + '-' + Math.floor(Math.random() * 1000000).toString(36);",
    "  pm.collectionVariables.set('run_id', seed + '-' + nonce);",
    "  console.log('run_id = ' + pm.collectionVariables.get('run_id'));",
    "}",
  ];
}

// Skip the scenario when its required credentials are absent so a developer can
// run with whatever providers they have configured.
export function folderPrerequest(requiredVars) {
  return [
    `var required = ${JSON.stringify(requiredVars)};`,
    "for (var i = 0; i < required.length; i++) {",
    "  var v = required[i];",
    "  if (!pm.variables.get(v) && !pm.environment.get(v)) {",
    "    console.log('SKIP: missing ' + v);",
    "    pm.execution.skipRequest();",
    "    return;",
    "  }",
    "}",
  ];
}

// Prelude for a polling request: track the attempt counter and (on the first
// attempt) optionally settle for waitSeconds before the read.
export function pollPrerequest(waitSeconds) {
  const waitMs = Math.round((waitSeconds || 0) * 1000);
  const lines = [
    "var pollKey = '__poll_' + pm.info.requestName;",
    "var attempt = parseInt(pm.collectionVariables.get(pollKey) || '0', 10);",
    "pm.collectionVariables.set('__cur_poll_key', pollKey);",
    "pm.collectionVariables.set('__cur_poll_attempt', String(attempt));",
  ];
  if (waitMs > 0) {
    lines.push(
      `if (attempt === 0) { var __ws = Date.now(); while (Date.now() - __ws < ${waitMs}) {} }`
    );
  }
  return lines;
}

// Wrap a throwing assertion in the exponential-backoff retry loop. Only the
// terminal attempt records a pm.test, so Newman's exit code reflects the real
// outcome while intermediate retries stay silent. On terminal failure it jumps
// to the scenario cleanup so a wedged scenario tears down instead of burning
// retries on every remaining step.
export function pollTest(testname, assertLines, cleanupName) {
  return [
    `var maxAttempts = ${MAX_POLL_ATTEMPTS};`,
    "var pollKey = pm.collectionVariables.get('__cur_poll_key');",
    "var attempt = parseInt(pm.collectionVariables.get('__cur_poll_attempt') || '0', 10);",
    `var cleanupReq = ${JSON.stringify(cleanupName)};`,
    "function assertNow() {",
    ...assertLines.map((l) => "  " + l),
    "}",
    "var ok = true, errMsg = '';",
    "try { assertNow(); } catch (e) { ok = false; errMsg = e.message; }",
    "if (ok) {",
    "  pm.collectionVariables.set(pollKey, '0');",
    `  pm.test(${JSON.stringify(testname)}, function () { pm.expect(true, 'assertion satisfied').to.be.true; });`,
    "} else if (attempt < maxAttempts) {",
    "  pm.collectionVariables.set(pollKey, String(attempt + 1));",
    "  var sleepMs = Math.min(250 * Math.pow(2, attempt), 4000);",
    "  var start = Date.now(); while (Date.now() - start < sleepMs) {}",
    "  pm.execution.setNextRequest(pm.info.requestName);",
    "} else {",
    "  pm.collectionVariables.set(pollKey, '0');",
    `  pm.test(${JSON.stringify(testname)}, function () { throw new Error(errMsg); });`,
    "  pm.execution.setNextRequest(cleanupReq);",
    "}",
  ];
}

// A non-polling mutation assertion: status must be in `acceptable`, else jump
// to cleanup so downstream steps don't run against inconsistent state.
export function mutationTest(testname, acceptable, cleanupName) {
  return [
    `var acceptable = ${JSON.stringify(acceptable)};`,
    `var cleanupReq = ${JSON.stringify(cleanupName)};`,
    `pm.test(${JSON.stringify(testname)}, function () {`,
    "  pm.expect(acceptable, 'status ' + pm.response.code + ' body ' + pm.response.text()).to.include(pm.response.code);",
    "});",
    "if (acceptable.indexOf(pm.response.code) < 0) { pm.execution.setNextRequest(cleanupReq); }",
  ];
}

// Cleanup steps accept already-deleted (404) so they pass whether the scenario
// reached its own delete or jumped here mid-flight.
export function cleanupTest(testname) {
  return [
    `pm.test(${JSON.stringify(testname)}, function () {`,
    "  pm.expect([200, 204, 404], 'cleanup status ' + pm.response.code).to.include(pm.response.code);",
    "});",
  ];
}

// A setup folder that deletes every provider before any scenario runs, so the
// suite starts from a clean slate. Standard (non-custom) providers are global
// singletons keyed by name — a leftover `openai`/`azure`/... from a prior or
// interrupted run makes a scenario's create fail with 409 "already exists".
// Clearing up front removes that whole class of cross-run collisions. Scenarios
// recreate every provider they need (keys resolve via Bifrost's env.<NAME>, not
// from any pre-existing provider), so nothing depends on what was there before.
//
// Implemented as a two-request loop: "list" reads all provider names into a
// queue and seeds the first target; "delete" removes the current target, then
// re-points the queue's next entry at itself until the queue drains.
export function clearProvidersFolder() {
  const LIST = "setup: list providers to clear";
  const DEL = "setup: delete provider";
  const listTest = [
    "pm.test('setup: list providers', function () { pm.expect(pm.response.code, pm.response.text()).to.equal(200); });",
    "if (pm.response.code !== 200) { return; }",
    "var provs = ((pm.response.json() || {}).providers || []).map(function (p) { return p.name; }).filter(Boolean);",
    "if (provs.length === 0) { pm.collectionVariables.set('__purge_target', ''); pm.collectionVariables.set('__purge_queue', ''); return; }",
    "pm.collectionVariables.set('__purge_target', provs[0]);",
    "pm.collectionVariables.set('__purge_queue', provs.slice(1).join(','));",
    `pm.execution.setNextRequest(${JSON.stringify(DEL)});`,
  ];
  const delPre = [
    // No target → nothing to clear (empty instance, or the queue just drained):
    // skip this request and fall through to the first scenario.
    "if (!pm.collectionVariables.get('__purge_target')) { pm.execution.skipRequest(); }",
  ];
  const delTest = [
    "pm.test('setup: delete provider', function () { pm.expect([200, 204, 404], 'status ' + pm.response.code + ' body ' + pm.response.text()).to.include(pm.response.code); });",
    "var queue = (pm.collectionVariables.get('__purge_queue') || '').split(',').filter(Boolean);",
    "if (queue.length) {",
    "  pm.collectionVariables.set('__purge_target', queue[0]);",
    "  pm.collectionVariables.set('__purge_queue', queue.slice(1).join(','));",
    `  pm.execution.setNextRequest(${JSON.stringify(DEL)});`,
    "} else {",
    "  pm.collectionVariables.set('__purge_target', '');",
    "}",
  ];
  return {
    id: "setup-clear-providers",
    name: "Setup: clear providers",
    item: [
      item("setup-list-providers", LIST, request("GET", url(["api", "providers"]), null), events(null, listTest)),
      item("setup-delete-provider", DEL, request("DELETE", url(["api", "providers", "{{__purge_target}}"]), null), events(delPre, delTest)),
    ],
  };
}

// --------------------------------------------------------------------------- //
// Postman item primitives
// --------------------------------------------------------------------------- //

export function url(pathSegments, query) {
  let raw = "{{base_url}}/" + pathSegments.join("/");
  if (query && query.length) {
    raw += "?" + query.map((q) => `${q.key}=${q.value}`).join("&");
  }
  const out = { raw, host: ["{{base_url}}"], path: [...pathSegments] };
  if (query && query.length) out.query = query;
  return out;
}

export function events(prerequest, test) {
  const out = [];
  if (prerequest) out.push({ listen: "prerequest", script: { type: "text/javascript", exec: prerequest } });
  if (test) out.push({ listen: "test", script: { type: "text/javascript", exec: test } });
  return out;
}

export function request(method, urlObj, body, headers) {
  const req = { method, header: [...(headers || [])], url: urlObj };
  if (body !== undefined && body !== null) {
    req.header.push({ key: "Content-Type", value: "application/json" });
    req.body = { mode: "raw", raw: JSON.stringify(body) };
  }
  return req;
}

export function item(id, name, req, evts) {
  return { id, name, event: evts, request: req };
}

// --------------------------------------------------------------------------- //
// Assembly + output
// --------------------------------------------------------------------------- //

export function buildCollection({ id, name, description, expandedScenarios, extraVariables }) {
  return {
    info: {
      _postman_id: id,
      name,
      description,
      schema: "https://schema.getpostman.com/json/collection/v2.1.0/collection.json",
    },
    variable: [
      { key: "base_url", value: "http://localhost:8080", type: "string" },
      { key: "run_id", value: "", type: "string" },
      { key: "__purge_target", value: "", type: "string" },
      { key: "__purge_queue", value: "", type: "string" },
      ...(extraVariables || []),
    ],
    event: events(collectionPrerequest(), null),
    // The clear-providers setup folder always runs first so every run starts
    // from a clean provider slate.
    item: [clearProvidersFolder(), ...expandedScenarios],
  };
}

// Resolve --out from argv, falling back to the generator's default path.
export function resolveOutPath(defaultPath) {
  const argv = process.argv.slice(2);
  const idx = argv.indexOf("--out");
  if (idx >= 0 && argv[idx + 1]) return argv[idx + 1];
  return defaultPath;
}

export function writeCollection(outPath, collection, scenarioIds) {
  writeFileSync(outPath, JSON.stringify(collection, null, 2) + "\n", "utf8");
  console.log("wrote " + outPath);
  console.log("scenarios: " + scenarioIds.length);
  for (const sid of scenarioIds) console.log("  - " + sid);
}
