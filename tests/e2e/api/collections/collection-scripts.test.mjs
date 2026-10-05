// Unit tests for the inline scripts in bifrost-api-management.postman_collection.json.
// Run directly: `node collection-scripts.test.mjs`.
// No test framework needed (the tests/e2e/api dir has no test runner configured).
//
// These cover the failure paths Newman hits when a request answers with an SPA
// fallback, a 401/403, or any other non-JSON body: a script that parses without a
// guard throws a script error instead of failing a named assertion, which aborts
// the capture and leaves every dependent request stranded.
import assert from "node:assert";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

const here = dirname(fileURLToPath(import.meta.url));
const collection = JSON.parse(
  readFileSync(join(here, "bifrost-api-management.postman_collection.json"), "utf8"),
);

// Locate a request by "Folder / Request Name" and return one of its scripts.
function scriptFor(path, listen) {
  const wanted = path.split(" / ");
  function walk(items, trail) {
    for (const item of items) {
      const here = [...trail, item.name];
      if (item.item) {
        const found = walk(item.item, here);
        if (found) return found;
        continue;
      }
      if (here.join(" / ") !== wanted.join(" / ")) continue;
      const event = (item.event || []).find((e) => e.listen === listen);
      assert.ok(event, `${path} has no ${listen} script`);
      return event.script.exec.join("\n");
    }
    return null;
  }
  const src = walk(collection.item, []);
  assert.ok(src, `request not found: ${path}`);
  return src;
}

// A no-op chai-style chain, so pm.expect(...) assertions neither throw nor pass
// judgement - these tests are about the code *outside* pm.test.
function expectChain() {
  const noop = () => chain;
  const chain = new Proxy(noop, {
    get: () => chain,
    apply: () => chain,
  });
  return chain;
}

// Minimal Newman-alike sandbox. pm.test swallows assertion failures the way the
// real runner does (a failed assertion is reported, it does not abort the script).
function sandbox({ responseBody = "", variables = {} } = {}) {
  const vars = { ...variables };
  const state = { skipped: false, tests: [] };
  const pm = {
    collectionVariables: {
      get: (key) => (key in vars ? vars[key] : undefined),
      set: (key, value) => {
        vars[key] = value;
      },
    },
    variables: { get: (key) => (key in vars ? vars[key] : undefined) },
    response: {
      text: () => responseBody,
      json: () => JSON.parse(responseBody),
    },
    request: { body: { mode: "raw", raw: "{}" } },
    test: (name, fn) => {
      try {
        fn();
        state.tests.push({ name, passed: true });
      } catch (err) {
        state.tests.push({ name, passed: false, err });
      }
    },
    expect: expectChain,
    execution: {
      skipRequest: () => {
        state.skipped = true;
      },
    },
  };
  return { pm, vars, state };
}

function run(src, options) {
  const ctx = sandbox(options);
  // eslint-disable-next-line no-new-func
  new Function("pm", src)(ctx.pm);
  return ctx;
}

let passed = 0;
let failed = 0;
function test(name, fn) {
  try {
    fn();
    passed++;
    console.log(`  ok - ${name}`);
  } catch (err) {
    failed++;
    console.log(`  FAIL - ${name}\n    ${err.message}`);
  }
}

const SPA_FALLBACK = "<!doctype html><html><body>bifrost</body></html>";

// ── Governance - Complexity Analyzer / Update Complexity Analyzer Config ──────
const complexityPrerequest = scriptFor(
  "Governance - Complexity Analyzer / Update Complexity Analyzer Config",
  "prerequest",
);

test("complexity prerequest skips when the config variable was never captured", () => {
  const ctx = run(complexityPrerequest, { variables: { complexity_config_json: "" } });
  assert.strictEqual(ctx.state.skipped, true, "expected skipRequest()");
});

test("complexity prerequest skips when the captured body is not JSON", () => {
  const ctx = run(complexityPrerequest, {
    variables: { complexity_config_json: SPA_FALLBACK },
  });
  assert.strictEqual(ctx.state.skipped, true, "expected skipRequest()");
});

test("complexity prerequest skips when the config has no keywords object", () => {
  const ctx = run(complexityPrerequest, {
    variables: { complexity_config_json: JSON.stringify({ tier_boundaries: {} }) },
  });
  assert.strictEqual(ctx.state.skipped, true, "expected skipRequest()");
});

test("complexity prerequest still injects e2eprobe into a valid config", () => {
  const ctx = run(complexityPrerequest, {
    variables: {
      complexity_config_json: JSON.stringify({
        tier_boundaries: { simple: 1 },
        keywords: { simple_keywords: ["hi"] },
      }),
    },
  });
  assert.strictEqual(ctx.state.skipped, false, "should not skip a valid config");
  const body = JSON.parse(ctx.pm.request.body.raw);
  assert.deepStrictEqual(body.keywords.simple_keywords, ["hi", "e2eprobe"]);
  assert.deepStrictEqual(body.tier_boundaries, { simple: 1 });
});

// ── Skills / Upload Skill File ────────────────────────────────────────────────
const uploadTest = scriptFor("Skills / Upload Skill File", "test");

test("upload capture defaults both keys to empty on a non-JSON response", () => {
  const ctx = run(uploadTest, { responseBody: SPA_FALLBACK });
  assert.strictEqual(ctx.vars.skill_upload_storage_key, "");
  assert.strictEqual(ctx.vars.skill_upload_blob_id, "");
});

test("upload capture still records a successful response", () => {
  const ctx = run(uploadTest, {
    responseBody: JSON.stringify({ upload_id: "u1", storage_key: "sk", blob_id: "bl" }),
  });
  assert.strictEqual(ctx.vars.skill_upload_storage_key, "sk");
  assert.strictEqual(ctx.vars.skill_upload_blob_id, "bl");
});

// ── Skills / Create Skill ─────────────────────────────────────────────────────
const createSkillTest = scriptFor("Skills / Create Skill", "test");

test("skill capture leaves skill_id unset on a non-JSON response", () => {
  const ctx = run(createSkillTest, { responseBody: SPA_FALLBACK });
  assert.ok(!ctx.vars.skill_id, "skill_id must stay unset");
});

test("skill capture leaves skill_id unset when the body has no skill", () => {
  const ctx = run(createSkillTest, {
    responseBody: JSON.stringify({ error: { message: "unauthorized" } }),
  });
  assert.ok(!ctx.vars.skill_id, "skill_id must stay unset");
});

test("skill capture still records the id on success", () => {
  const ctx = run(createSkillTest, {
    responseBody: JSON.stringify({ skill: { id: "sk-1", latest_version: "1.0.0" } }),
  });
  assert.strictEqual(ctx.vars.skill_id, "sk-1");
});

// ── Webhooks / Create Webhook Endpoint ────────────────────────────────────────
const createWebhookTest = scriptFor("Webhooks / Create Webhook Endpoint", "test");

test("webhook capture leaves webhook_id unset on a non-JSON response", () => {
  const ctx = run(createWebhookTest, { responseBody: SPA_FALLBACK });
  assert.ok(!ctx.vars.webhook_id, "webhook_id must stay unset");
});

test("webhook capture leaves webhook_id unset when the body has no endpoint", () => {
  const ctx = run(createWebhookTest, {
    responseBody: JSON.stringify({ error: { message: "forbidden" } }),
  });
  assert.ok(!ctx.vars.webhook_id, "webhook_id must stay unset");
});

test("webhook capture still records the id on success", () => {
  const ctx = run(createWebhookTest, {
    responseBody: JSON.stringify({ endpoint: { id: "wh-1" }, secret: "s3cret" }),
  });
  assert.strictEqual(ctx.vars.webhook_id, "wh-1");
});

// ── Governance / Virtual key budget override ──────────────────────────────────
// These two requests mutate a real budget, so they must not run against fallback
// placeholder ids. A non-empty collection-variable default defeats a `!get(...)`
// skip guard, which would send the override to a made-up budget instead of skipping.
function collectionVariable(key) {
  const entry = (collection.variable || []).find((v) => v.key === key);
  assert.ok(entry, `collection variable not declared: ${key}`);
  return entry.value;
}

for (const key of ["vk_budget_id", "vk_budget_max_limit"]) {
  test(`${key} defaults to empty so the skip guard can fire`, () => {
    assert.strictEqual(
      collectionVariable(key),
      "",
      "a non-empty default makes the guard's truthiness check always pass",
    );
  });
}

const OVERRIDE_REQUESTS = [
  "Governance - Virtual Keys / Set Virtual Key Budget Override",
  "Governance - Virtual Keys / Remove Virtual Key Budget Override",
];

for (const path of OVERRIDE_REQUESTS) {
  const prerequest = scriptFor(path, "prerequest");
  const captured = {
    vk_id: "vk-real",
    vk_budget_id: "budget-real",
    vk_budget_max_limit: "100",
  };

  for (const missing of ["vk_id", "vk_budget_id", "vk_budget_max_limit"]) {
    test(`${path.split(" / ")[1]} skips when ${missing} was not captured`, () => {
      const ctx = run(prerequest, {
        variables: { ...captured, [missing]: "" },
      });
      assert.strictEqual(ctx.state.skipped, true, `expected skipRequest() with no ${missing}`);
    });
  }

  test(`${path.split(" / ")[1]} runs when every id was captured`, () => {
    const ctx = run(prerequest, { variables: { ...captured } });
    assert.strictEqual(ctx.state.skipped, false, "should not skip a fully captured run");
  });
}

test("Set Virtual Key Budget Override still builds its request body", () => {
  const ctx = run(scriptFor(OVERRIDE_REQUESTS[0], "prerequest"), {
    variables: { vk_id: "vk-real", vk_budget_id: "budget-real", vk_budget_max_limit: "100" },
  });
  assert.deepStrictEqual(JSON.parse(ctx.pm.request.body.raw), { amount: 7.5, mode: "forever" });
  assert.strictEqual(ctx.vars.vk_override_amount, "7.5");
});

// ── Collection-level status gate ──────────────────────────────────────────────
// README.md documents which statuses this gate lets through, including the named
// requests that are allowed a non-2xx without a "(Coverage Probe)" suffix. Pin
// that documented behaviour here so the prose cannot drift away from the script.
//
// Only the status-gate region is evaluated: the surrounding script logs the
// request and then runs ~45k of per-handler structure assertions that are not
// what the README paragraph describes.
function statusGateSource() {
  const src = collection.event.find((e) => e.listen === "test").script.exec.join("\n");
  const start = src.indexOf("var code = pm.response.code;");
  const endAnchor = "pm.test('Status is 2xx or expected 4xx'";
  const end = src.indexOf(endAnchor);
  assert.ok(start !== -1 && end > start, "status gate region not found in collection script");
  // requestName is declared above the logging block the slice skips over.
  const nameDecl = src.match(/^var requestName = .*$/m);
  assert.ok(nameDecl, "requestName declaration not found in collection script");
  return `${nameDecl[0]}\n${src.slice(start, src.indexOf("\n", end))}`;
}
const gateSource = statusGateSource();

// The sliced region asserts exactly once, as `pm.expect(pass).to.be.true`.
function gateExpect(value) {
  return {
    to: {
      be: {
        get true() {
          assert.strictEqual(value, true, "gate rejected the response");
          return true;
        },
      },
    },
  };
}

// Returns true when the gate would let this response through.
function runGate({ requestName, code, body = "", variables = {} }) {
  let accepted = null;
  const pm = {
    info: { requestName },
    request: { method: "GET", url: { toString: () => "http://local/api/x" }, body: null },
    response: {
      code,
      status: String(code),
      text: () => body,
      json: () => JSON.parse(body),
    },
    variables: { get: (key) => variables[key] },
    expect: gateExpect,
    test: (name, fn) => {
      try {
        fn();
        accepted = true;
      } catch {
        accepted = false;
      }
    },
  };
  // eslint-disable-next-line no-new-func
  new Function("pm", gateSource)(pm);
  assert.notStrictEqual(accepted, null, "gate never ran its status assertion");
  return accepted;
}

test("gate accepts any 2xx", () => {
  assert.strictEqual(runGate({ requestName: "List Providers", code: 200 }), true);
  assert.strictEqual(runGate({ requestName: "Create Provider", code: 201 }), true);
  assert.strictEqual(runGate({ requestName: "Delete Provider", code: 204 }), true);
});

test("gate rejects a plain 4xx/5xx on an ordinary request", () => {
  assert.strictEqual(runGate({ requestName: "List Providers", code: 404, body: "{}" }), false);
  assert.strictEqual(runGate({ requestName: "List Providers", code: 500, body: "{}" }), false);
});

test("coverage probes are accepted on 4xx and 5xx but not on 3xx", () => {
  assert.strictEqual(runGate({ requestName: "Do Thing (Coverage Probe)", code: 404 }), true);
  assert.strictEqual(runGate({ requestName: "Do Thing (Coverage Probe)", code: 503 }), true);
  assert.strictEqual(
    runGate({ requestName: "Do Thing (Coverage Probe)", code: 302 }),
    false,
    "README claims probes do not pass on a 3xx",
  );
});

test("before-create requests are allowed a 404", () => {
  for (const name of [
    "Get Customer (Before Create)",
    "Get Team (Before Create)",
    "Get Virtual Key (Before Create)",
    "Get Routing Rule (Before Create)",
    "Get Model Config (Before Create)",
    "Get Plugin (Before Create)",
  ]) {
    assert.strictEqual(runGate({ requestName: name, code: 404, body: "{}" }), true, name);
  }
});

test("MCP client requests are allowed a 404", () => {
  for (const name of [
    "Add MCP Client",
    "Reconnect MCP Client",
    "Update MCP Client",
    "Delete MCP Client",
  ]) {
    assert.strictEqual(runGate({ requestName: name, code: 404, body: "{}" }), true, name);
  }
});

test("plugin requests are allowed a 404 only with a plugin-load message", () => {
  const loadFailure = JSON.stringify({ error: { message: "plugin not found on disk" } });
  assert.strictEqual(
    runGate({ requestName: "Create Plugin", code: 404, body: loadFailure }),
    true,
  );
  assert.strictEqual(
    runGate({ requestName: "Create Plugin", code: 404, body: JSON.stringify({ error: { message: "nope" } }) }),
    false,
    "an unrelated 404 must still fail",
  );
});

test("plugin requests are allowed a 403 only in the unauthenticated pass", () => {
  const authError = JSON.stringify({
    error: { message: "this route requires genuine admin authentication" },
  });
  assert.strictEqual(
    runGate({ requestName: "Update Plugin", code: 403, body: authError }),
    true,
  );
  assert.strictEqual(
    runGate({
      requestName: "Update Plugin",
      code: 403,
      body: authError,
      variables: { admin_auth_header: "Bearer x" },
    }),
    false,
    "with admin auth configured the 403 is a real failure",
  );
});

test("the two cache probes are allowed a 405", () => {
  assert.strictEqual(
    runGate({ requestName: "Clear Cache by Cache ID (Coverage Probe)", code: 405 }),
    true,
  );
  assert.strictEqual(
    runGate({ requestName: "Clear Cache by Key (Coverage Probe)", code: 405 }),
    true,
  );
});

// ── Provider Secret Redaction ────────────────────────────────────────────────
const redactionFolder = collection.item.find((item) => item.name === "Provider Secret Redaction");
assert.ok(redactionFolder, "provider secret redaction coverage is missing");
const redactionScript = redactionFolder.event.find((e) => e.listen === "test").script.exec.join("\n");
function redactionFixture(item) {
  let fixture;
  const pm = { variables: { set(name, value) {
    assert.strictEqual(name, "secret_redaction_fixture");
    fixture = JSON.parse(value);
  } } };
  new Function("pm", item.event.find((e) => e.listen === "prerequest").script.exec.join("\n"))(pm);
  return fixture;
}
const redactionFixtures = redactionFolder.item.map(redactionFixture);
const schemaSource = readFileSync(join(here, "../../../../core/schemas/bifrost.go"), "utf8");
const accountSource = readFileSync(join(here, "../../../../core/schemas/account.go"), "utf8");
const providerConstants = new Map([...schemaSource.matchAll(/(\w+)\s+ModelProvider\s*=\s*"([^"]+)"/g)].map((m) => [m[1], m[2]]));
const standardBlock = schemaSource.match(/var StandardProviders = \[\]ModelProvider\{([\s\S]*?)\n\}/)[1];
const standardProviders = [...standardBlock.matchAll(/^\s*(\w+),/gm)].map((m) => providerConstants.get(m[1]));
const redactionRef = "env.BIFROST_SECRET_REDACTION_CANARY";
const redactionCanary = "synthetic-secretvar-harness-canary-0123456789";
const redactionMask = "synt" + "*".repeat(24) + "6789";

test("redaction folder authenticates the initial request as well as callback requests", () => {
  let header;
  const pm = {
    variables: { get: () => "Bearer synthetic-admin" },
    request: { headers: { upsert: (value) => { header = value; } } },
  };
  const source = redactionFolder.event.find((e) => e.listen === "prerequest").script.exec.join("\n");
  new Function("pm", source)(pm);
  assert.deepStrictEqual(header, { key: "Authorization", value: "Bearer synthetic-admin" });
});

test("redaction matrix covers every standard provider exactly once", () => {
  assert.deepStrictEqual(redactionFixtures.map((f) => f.provider).sort(), [...standardProviders].sort());
  for (const fixture of redactionFixtures) {
    assert.strictEqual(fixture.key.enabled, false);
    assert.strictEqual(fixture.key.value, redactionRef);
    assert.strictEqual(fixture.key.aliases.probe.region, redactionRef);
    assert.strictEqual(fixture.key.aliases.probe.project_id, redactionRef);
  }
});

function structBody(name) {
  const match = accountSource.match(new RegExp("type " + name + " struct \\{([\\s\\S]*?)\\n\\}"));
  assert.ok(match, `schema ${name} not found`);
  return match[1];
}
function secretFields(name) {
  return [...structBody(name).matchAll(/\w+\s+\*?SecretVar\s+`json:"([^",]+)[^"]*"`/g)]
    .map((match) => match[1]).filter((field) => field !== "-");
}

test("redaction matrix covers every provider-specific SecretVar field", () => {
  for (const match of structBody("Key").matchAll(/\w+\s+\*(\w+KeyConfig)\s+`json:"([^",]+)[^"]*"`/g)) {
    const [, type, property] = match;
    const fields = secretFields(type);
    if (!fields.length) continue; // Replicate has no secret-bearing key config.
    const fixture = redactionFixtures.find((f) => f.key[property]);
    assert.ok(fixture, `no fixture covers ${type}`);
    for (const field of fields) assert.strictEqual(fixture.key[property][field], redactionRef, `${type}.${field}`);
    if (structBody(type).includes("*BedrockEndpoints")) {
      for (const field of secretFields("BedrockEndpoints")) {
        assert.strictEqual(fixture.key[property].endpoints[field], redactionRef, `${type}.endpoints.${field}`);
      }
    }
  }
  for (const [provider, type] of [["azure", "AzureAliasCfg"], ["vertex", "VertexAliasCfg"], ["bedrock", "BedrockAliasCfg"]]) {
    const fixture = redactionFixtures.find((f) => f.provider === provider);
    for (const field of secretFields(type)) assert.strictEqual(fixture.key.aliases.probe[field], redactionRef, `${type}.${field}`);
  }
});

// Unlike the generic script sandbox above, these assertions must really fail:
// otherwise a broken no-leak assertion could manufacture a passing regression.
function redactionExpect(actual, message) {
  function chain(negated = false) {
    const c = {};
    c.to = c.be = c;
    Object.defineProperty(c, "not", { get: () => chain(!negated) });
    c.equal = (expected) => negated ? assert.notStrictEqual(actual, expected, message) : assert.strictEqual(actual, expected, message);
    c.include = (expected) => assert.strictEqual(actual.includes(expected), !negated, message);
    c.an = (type) => assert.strictEqual(type === "array" ? Array.isArray(actual) : actual !== null && typeof actual === type, true, message);
    return c;
  }
  return chain();
}
function simulateRedaction(fixture, mode = "masked", existing = false, auth = false) {
  let savedKey = null;
  let providerExists = existing;
  const state = { failures: [], assertions: [], calls: [] };
  const response = (body, code = 200) => ({ code, json: () => body, text: () => JSON.stringify(body) });
  const providerResponse = () => ({ name: fixture.provider });
  function wire(value) {
    if (value === redactionRef || (value && value.ref === redactionRef)) {
      return { ref: redactionRef, type: "env", value: redactionMask };
    }
    if (Array.isArray(value)) return value.map(wire);
    if (value && typeof value === "object") return Object.fromEntries(Object.entries(value).map(([key, val]) => [key, wire(val)]));
    return value;
  }
  function keyResponse() {
    const body = wire(savedKey);
    body.aliases.literal.region = { value: "us-east-1", type: "plain_text" };
    body.aliases.literal.project_id = { value: "literal-project", type: "plain_text" };
    return body;
  }
  const pm = {
    variables: {
      get: (name) => name === "secret_redaction_fixture" ? JSON.stringify(fixture) : name === "admin_auth_header" && auth ? "Bearer synthetic-admin" : name === "setup_token" && !auth ? "synthetic-setup-token" : undefined,
      replaceIn: (value) => value === "{{$guid}}" ? "unique-test-id" : "http://local",
    },
    response: response({ providers: existing ? [providerResponse()] : [] }),
    expect: redactionExpect,
    test: (name, fn) => {
      state.assertions.push(name);
      try { fn(); } catch (err) { state.failures.push({ name, message: err.message }); }
    },
    sendRequest: (req, callback) => {
      state.calls.push(req);
      if (auth) assert.strictEqual(req.header.Authorization, "Bearer synthetic-admin");
      // pm.sendRequest skips the collection-level prerequest, so the script must add the setup token itself.
      else assert.strictEqual(req.header["X-Bifrost-Setup-Token"], "synthetic-setup-token");
      const path = req.url.slice("http://local".length);
      const providerPath = "/api/providers/" + fixture.provider;
      if (req.method === "POST" && path === "/api/providers") {
        providerExists = true;
        callback(null, response(providerResponse()));
      } else if (req.method === "POST" && path.endsWith("/keys")) {
        savedKey = JSON.parse(req.body.raw);
        callback(null, response(keyResponse()));
      } else if (req.method === "PUT") {
        const update = JSON.parse(req.body.raw);
        assert.strictEqual(update.value.value, redactionMask, "update must echo the masked wire response");
        assert.strictEqual(update.value.ref, redactionRef);
        savedKey = update;
        callback(null, response(keyResponse()));
      } else if (req.method === "DELETE" && path.includes("/keys/")) {
        if (mode === "cleanup-error") { callback(null, response({}, 500)); return; }
        const deleted = keyResponse(); savedKey = null;
        callback(null, response(deleted));
      } else if (req.method === "DELETE") {
        assert.strictEqual(existing, false, "must never delete an existing provider");
        providerExists = false;
        callback(null, response({}));
      } else if (path.includes("/keys/")) {
        if (!savedKey) { callback(null, response({}, 404)); return; }
        if (mode === "http-error") { callback(null, response({}, 403)); return; }
        if (mode === "transport-error") { callback(new Error("connection failed")); return; }
        if (mode === "non-json") { callback(null, { code: 200, json() { throw new Error("invalid JSON"); }, text: () => SPA_FALLBACK }); return; }
        const body = keyResponse();
        if (mode === "leaked") body.aliases.probe.region.value = redactionCanary;
        if (mode === "unset") body.aliases.probe.region.value = "";
        if (mode === "missing") delete body.aliases.probe.region;
        if (mode === "missing-ref") delete body.aliases.probe.region.ref;
        callback(null, response(body));
      } else if (path.endsWith("/keys")) {
        callback(null, response({ keys: [keyResponse()] }));
      } else if (path === "/api/providers") {
        callback(null, response({ providers: [providerResponse()] }));
      } else {
        callback(null, response(providerExists ? providerResponse() : {}, providerExists ? 200 : 404));
      }
    },
  };
  new Function("pm", redactionScript)(pm);
  assert.ok(state.calls.some((r) => r.method === "DELETE" && r.url.includes("/keys/")), "must attempt key cleanup");
  if (mode !== "cleanup-error") assert.strictEqual(savedKey, null, "test key must be removed");
  assert.strictEqual(providerExists, existing, "original provider state must be preserved");
  return state;
}

for (const fixture of redactionFixtures) {
  test(`${fixture.provider}: masked API responses pass with new and existing providers, with and without auth`, () => {
    for (const existing of [false, true]) {
      for (const auth of [false, true]) {
        const state = simulateRedaction(fixture, "masked", existing, auth);
        assert.deepStrictEqual(state.failures, []);
        for (const operation of ["key create", "key get", "key list", "key update", "key delete", "provider get", "provider list"]) {
          assert.ok(state.assertions.some((name) => name.includes(operation)), `missing ${operation} assertion`);
        }
      }
    }
  });
}
for (const mode of ["leaked", "unset", "missing", "missing-ref", "http-error", "transport-error", "non-json", "cleanup-error"]) {
  test(`redaction assertions reject ${mode} and still attempt cleanup`, () => {
    const state = simulateRedaction(redactionFixtures[0], mode);
    assert.ok(state.failures.length > 0, `${mode} was falsely accepted`);
  });
}

console.log(`\n${passed} passed, ${failed} failed`);
process.exit(failed === 0 ? 0 : 1);
