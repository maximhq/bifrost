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

const guardsCollection = JSON.parse(
  readFileSync(join(here, "bifrost-v1-request-guards.postman_collection.json"), "utf8"),
);
const rateLimitCollection = JSON.parse(
  readFileSync(join(here, "bifrost-v1-rate-limit.postman_collection.json"), "utf8"),
);

// Locate a request by "Folder / Request Name" and return one of its scripts.
function scriptFor(path, listen, source = collection) {
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
  const src = walk(source.item, []);
  assert.ok(src, `request not found: ${path}`);
  return src;
}

// Same lookup inside the request-guards collection.
function guardsScriptFor(path, listen) {
  return scriptFor(path, listen, guardsCollection);
}

// Same lookup inside the rate-limit collection.
function rateLimitScriptFor(path, listen) {
  return scriptFor(path, listen, rateLimitCollection);
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
function sandbox({ responseBody = "", responseCode = 200, variables = {} } = {}) {
  const vars = { ...variables };
  const state = { skipped: false, tests: [] };
  const pm = {
    collectionVariables: {
      get: (key) => (key in vars ? vars[key] : undefined),
      set: (key, value) => {
        vars[key] = value;
      },
      unset: (key) => {
        delete vars[key];
      },
    },
    variables: { get: (key) => (key in vars ? vars[key] : undefined) },
    response: {
      code: responseCode,
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

// ── Providers / Add Provider and its dependents ───────────────────────────────
// The create sets a base URL, which the server only accepts from an admin
// session: the unauthenticated pass gets a 403 and the dependents must skip.
const addProviderTest = scriptFor("Providers / Add Provider", "test");

test("provider capture records provider_created only on a 2xx", () => {
  const refused = run(addProviderTest, {
    responseCode: 403,
    responseBody: JSON.stringify({ error: { message: "requires an authenticated admin session" } }),
  });
  assert.ok(!refused.vars.provider_created, "provider_created must stay unset on a 403");
  const created = run(addProviderTest, {
    responseCode: 200,
    responseBody: JSON.stringify({ name: "custom-123" }),
    variables: { admin_auth_header: "Bearer x" },
  });
  assert.strictEqual(created.vars.provider_created, "custom-123");
});

test("provider capture clears a stale provider_created before judging the response", () => {
  const ctx = run(addProviderTest, {
    responseCode: 403,
    responseBody: "{}",
    variables: { provider_created: "custom-123" },
  });
  assert.ok(!ctx.vars.provider_created, "a value from an earlier run must not survive a refusal");
});

for (const name of ["Get Provider (After Create)", "Update Provider", "Delete Provider"]) {
  const prerequest = scriptFor(`Providers / ${name}`, "prerequest");
  test(`${name} skips when the create was refused`, () => {
    assert.strictEqual(run(prerequest, { variables: { provider_created: "" } }).state.skipped, true);
    assert.strictEqual(run(prerequest, {}).state.skipped, true);
  });
  test(`${name} runs when the create succeeded`, () => {
    assert.strictEqual(run(prerequest, { variables: { provider_created: "custom-123" } }).state.skipped, false);
  });
}

// ── Proxy / Update Proxy Config and Get Proxy Config ──────────────────────────
const updateProxyPrerequest = scriptFor("Proxy / Update Proxy Config", "prerequest");

test("unauthenticated proxy update attempts a loopback URL with a port stamped per run and records it", () => {
  const ctx = run(updateProxyPrerequest, {});
  const body = JSON.parse(ctx.pm.request.body.raw);
  assert.match(body.url, /^http:\/\/127\.0\.0\.1:2\d{4}$/);
  assert.notStrictEqual(body.url, "http://127.0.0.1:38080", "must differ from the fixed authenticated URL");
  assert.strictEqual(ctx.vars.proxy_attempted_url, body.url);
  assert.strictEqual(body.enabled, true);
});

test("authenticated proxy update sends the fixed URL the seeded-value check expects", () => {
  const ctx = run(updateProxyPrerequest, { variables: { admin_auth_header: "Bearer x" } });
  const body = JSON.parse(ctx.pm.request.body.raw);
  assert.strictEqual(body.url, "http://127.0.0.1:38080");
  assert.strictEqual(ctx.vars.proxy_attempted_url, body.url);
});

test("Get Proxy Config survives a non-JSON body without a script error", () => {
  const getProxyTest = scriptFor("Proxy / Get Proxy Config", "test");
  const ctx = run(getProxyTest, {
    responseBody: SPA_FALLBACK,
    variables: { proxy_attempted_url: "http://127.0.0.1:20001" },
  });
  assert.ok(ctx.state.tests.length > 0, "the script must reach its named assertions");
});

// ── MCP Clients / Add MCP Client and its dependents ───────────────────────────
const addMCPClientTest = scriptFor("MCP Clients / Add MCP Client", "test");

test("MCP client capture records mcp_client_registered only on a 2xx", () => {
  const refused = run(addMCPClientTest, {
    responseCode: 403,
    responseBody: JSON.stringify({ error: { message: "loopback, private-network, or link-local" } }),
    variables: { mcp_client_id: "test_mcp_123", mcp_client_registered: "stale" },
  });
  assert.ok(!refused.vars.mcp_client_registered, "must stay unset on a 403 (and clear a stale value)");
  const connected = run(addMCPClientTest, {
    responseCode: 200,
    responseBody: JSON.stringify({ message: "MCP client connected successfully", status: "success" }),
    variables: { mcp_client_id: "test_mcp_123", admin_auth_header: "Bearer x" },
  });
  assert.strictEqual(connected.vars.mcp_client_registered, "test_mcp_123");
});

for (const name of ["Reconnect MCP Client", "Update MCP Client", "Delete MCP Client"]) {
  const prerequest = scriptFor(`MCP Clients / ${name}`, "prerequest");
  test(`${name} skips when the registration was refused and runs when it succeeded`, () => {
    assert.strictEqual(run(prerequest, {}).state.skipped, true);
    assert.strictEqual(run(prerequest, { variables: { mcp_client_registered: "test_mcp_123" } }).state.skipped, false);
  });
}

// ── Virtual MCPs folder prerequest ────────────────────────────────────────────
// Folder-level scripts are not reachable through scriptFor (it walks requests).
function folderScript(name, listen) {
  const f = collection.item.find((i) => i.name === name);
  assert.ok(f, `folder not found: ${name}`);
  const event = (f.event || []).find((e) => e.listen === listen);
  assert.ok(event, `${name} has no folder-level ${listen} script`);
  return event.script.exec.join("\n");
}

test("Virtual MCPs skips only the requests that need the setup client", () => {
  const src = folderScript("Virtual MCPs", "prerequest");
  const runNamed = (requestName, variables) => {
    const ctx = sandbox({ variables });
    ctx.pm.info = { requestName };
    // eslint-disable-next-line no-new-func
    new Function("pm", src)(ctx.pm);
    return ctx.state.skipped;
  };
  assert.strictEqual(runNamed("Create MCP Client (vMCP setup)", {}), false, "the setup request itself always runs");
  assert.strictEqual(runNamed("Get Virtual MCP Invalid Id (Coverage Probe)", {}), false, "independent probes run unauthenticated");
  assert.strictEqual(runNamed("Create Virtual MCP", {}), true, "a dependent skips when the setup client was refused");
  assert.strictEqual(runNamed("Delete MCP Client (vMCP cleanup)", {}), true);
  assert.strictEqual(runNamed("Create Virtual MCP", { vmcp_client_registered: "vmcp_client_1" }), false, "dependents run once the client exists");
});

test("vMCP setup capture records vmcp_client_registered only on a 2xx", () => {
  const setupTest = scriptFor("Virtual MCPs / Create MCP Client (vMCP setup)", "test");
  const refused = run(setupTest, {
    responseCode: 403,
    responseBody: SPA_FALLBACK,
    variables: { vmcp_client_id: "vmcp_client_1", vmcp_client_registered: "stale" },
  });
  assert.ok(!refused.vars.vmcp_client_registered, "must stay unset on a refusal, even with a non-JSON body");
  const connected = run(setupTest, {
    responseCode: 200,
    responseBody: JSON.stringify({ message: "MCP client connected successfully" }),
    variables: { vmcp_client_id: "vmcp_client_1", admin_auth_header: "Bearer x" },
  });
  assert.strictEqual(connected.vars.vmcp_client_registered, "vmcp_client_1");
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

test("dial-target writes are allowed a 403 only in the unauthenticated pass", () => {
  const refusal = JSON.stringify({
    error: { message: "Setting a provider's base URL or allow_private_network requires an authenticated admin session; dashboard auth is currently disabled or unconfigured." },
  });
  for (const name of ["Add Provider", "Update Proxy Config"]) {
    assert.strictEqual(runGate({ requestName: name, code: 403, body: refusal }), true, name);
    assert.strictEqual(
      runGate({ requestName: name, code: 403, body: refusal, variables: { admin_auth_header: "Bearer x" } }),
      false,
      `${name}: with admin auth configured the 403 is a real failure`,
    );
    assert.strictEqual(
      runGate({ requestName: name, code: 403, body: JSON.stringify({ error: { message: "forbidden" } }) }),
      false,
      `${name}: a 403 without the admin-session wording must still fail`,
    );
    assert.strictEqual(
      runGate({ requestName: name, code: 401, body: refusal }),
      false,
      `${name}: only the 403 refusal is expected`,
    );
  }
  assert.strictEqual(
    runGate({ requestName: "Update Provider", code: 403, body: refusal }),
    false,
    "the allowance is per named request, not a blanket 403 rule",
  );
});

test("MCP client registrations are allowed a 403 only in the unauthenticated pass", () => {
  const loopback = JSON.stringify({
    error: { message: "unauthenticated callers cannot register MCP clients that connect to loopback, private-network, or link-local addresses; set an admin password to allow this" },
  });
  const unresolvable = JSON.stringify({
    error: { message: 'could not resolve MCP target host "mcp.e2e-unresolvable.invalid"; unauthenticated callers can only register MCP clients whose target resolves to a public address' },
  });
  for (const [name, body] of [
    ["Add MCP Client", loopback],
    ["Create MCP Client (vMCP setup)", loopback],
    ["Add MCP Client (unresolvable host)", unresolvable],
  ]) {
    assert.strictEqual(runGate({ requestName: name, code: 403, body }), true, name);
    assert.strictEqual(
      runGate({ requestName: name, code: 403, body, variables: { admin_auth_header: "Bearer x" } }),
      false,
      `${name}: with admin auth configured the 403 is a real failure`,
    );
  }
  assert.strictEqual(
    runGate({ requestName: "Add MCP Client", code: 403, body: unresolvable }),
    false,
    "the loopback request must carry the loopback wording, not any 403",
  );
});

test("MCP registration input errors are allowed exactly as the server reports them", () => {
  assert.strictEqual(
    runGate({ requestName: "Add MCP Client (unsupported connection_type)", code: 400, body: "{}" }),
    true,
  );
  assert.strictEqual(
    runGate({ requestName: "Add MCP Client (unsupported connection_type)", code: 403, body: "{}" }),
    false,
    "only the 400 validation answer is expected",
  );
  const connectFailure = JSON.stringify({ error: { message: "Failed to connect MCP client: context deadline exceeded" } });
  assert.strictEqual(
    runGate({ requestName: "Add MCP Client (unresolvable host)", code: 500, body: connectFailure, variables: { admin_auth_header: "Bearer x" } }),
    true,
    "an admin may register the target; the connect failure is the expected outcome",
  );
  assert.strictEqual(
    runGate({ requestName: "Add MCP Client (unresolvable host)", code: 500, body: connectFailure }),
    false,
    "unauthenticated the registration must be refused, never attempted",
  );
  assert.strictEqual(
    runGate({ requestName: "Add MCP Client", code: 500, body: connectFailure, variables: { admin_auth_header: "Bearer x" } }),
    false,
    "the 500 allowance is only for the unresolvable-host request",
  );
});

test("the webhook test delivery is allowed a 403 only in the unauthenticated pass", () => {
  const refusal = JSON.stringify({
    error: { message: "unauthenticated callers cannot test webhook endpoints that allow private-network delivery; set an admin password to allow this" },
  });
  assert.strictEqual(runGate({ requestName: "Test Webhook Endpoint", code: 403, body: refusal }), true);
  assert.strictEqual(
    runGate({ requestName: "Test Webhook Endpoint", code: 403, body: refusal, variables: { admin_auth_header: "Bearer x" } }),
    false,
    "with admin auth configured the 403 is a real failure",
  );
  assert.strictEqual(
    runGate({ requestName: "Test Webhook Endpoint", code: 502, body: "{}" }),
    false,
    "without the probe suffix a receiver error is no longer tolerated",
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
      get: (name) => name === "secret_redaction_fixture" ? JSON.stringify(fixture) : name === "admin_auth_header" && auth ? "Bearer synthetic-admin" : undefined,
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

// ── Request guards - restore steps for a stored rejected value ───────────────
// The guards assert that a refused proxy change or catalog URL was not persisted. If a
// guard regresses and the value is stored, the check fails loudly and a skip-gated PUT
// must put the earlier config back, the same way the leaked MCP client / provider / key
// cleanups do, so the suites after this one do not run against a changed gateway.
const catalogRead = guardsScriptFor("Catalog URL validation / Read the stored catalog URLs", "test");
const catalogCheck = guardsScriptFor("Catalog URL validation / The stored catalog URLs are unchanged", "test");
const catalogRestorePre = guardsScriptFor("Catalog URL validation / Cleanup: restore the stored catalog URLs", "prerequest");
const catalogBefore = JSON.stringify({
  client_config: { log_retention_days: 0, compat: { should_drop_params: true } },
  framework_config: {
    pricing_url: "https://example.com/pricing.json",
    model_parameters_url: "https://example.com/params.json",
    mcp_library_url: "https://example.com/mcp",
    pricing_sync_interval: 86400,
  },
});

test("catalog read stores a restore payload holding exactly the three earlier catalog URLs", () => {
  const { vars } = run(catalogRead, { responseBody: catalogBefore });
  // client_config rides along verbatim (with the write-rejected log_retention_days 0 patched to 1)
  // so the handler sees an unchanged compat block and never reloads or removes the compat plugin.
  assert.strictEqual(
    vars.rg_catalog_restore_raw,
    '{"client_config":{"log_retention_days":1,"compat":{"should_drop_params":true}},"framework_config":{"pricing_url":"https://example.com/pricing.json","model_parameters_url":"https://example.com/params.json","mcp_library_url":"https://example.com/mcp"}}',
  );
});

test("catalog read leaves the restore payload empty when the config read has no client_config object", () => {
  const { vars } = run(catalogRead, { responseBody: catalogBefore.replace('"client_config":{"log_retention_days":0,"compat":{"should_drop_params":true}},', "") });
  assert.strictEqual(vars.rg_catalog_restore_raw, "");
});

test("catalog read leaves the restore payload empty on a non-200 so the restore can never send a placeholder", () => {
  const { vars } = run(catalogRead, { responseBody: SPA_FALLBACK, responseCode: 503, variables: { rg_catalog_restore_raw: "stale" } });
  assert.strictEqual(vars.rg_catalog_restore_raw, "");
});

test("catalog check flags a leak only when a stored catalog URL differs from the earlier value", () => {
  const before = {
    rg_pricing_url: "https://example.com/pricing.json",
    rg_model_parameters_url: "https://example.com/params.json",
    rg_mcp_library_url: "https://example.com/mcp",
  };
  const same = run(catalogCheck, { responseBody: catalogBefore, variables: { ...before, rg_catalog_leaked: "1" } });
  assert.strictEqual(same.vars.rg_catalog_leaked, "", "unchanged URLs clear a stale flag");
  const leakedBody = catalogBefore.replace("https://example.com/mcp", "file:///etc/hostname");
  const leaked = run(catalogCheck, { responseBody: leakedBody, variables: before });
  assert.strictEqual(leaked.vars.rg_catalog_leaked, "1", "a stored file:// URL sets the flag");
});

test("catalog restore skips unless a leak was flagged and a restore payload exists", () => {
  assert.strictEqual(run(catalogRestorePre, { variables: { rg_catalog_leaked: "", rg_catalog_restore_raw: "{}" } }).state.skipped, true);
  assert.strictEqual(run(catalogRestorePre, { variables: { rg_catalog_leaked: "1", rg_catalog_restore_raw: "" } }).state.skipped, true);
  const live = run(catalogRestorePre, { variables: { rg_catalog_leaked: "1", rg_catalog_restore_raw: '{"framework_config":{"pricing_url":"x"}}' } });
  assert.strictEqual(live.state.skipped, false);
  assert.strictEqual(live.pm.request.body.raw, '{"framework_config":{"pricing_url":"x"}}', "the earlier URLs are resent verbatim");
});

const proxyRead = guardsScriptFor("Provider endpoint changes / Read the stored proxy config", "test");
const proxyCheck = guardsScriptFor("Provider endpoint changes / The proxy config is unchanged", "test");
const proxyRestorePre = guardsScriptFor("Provider endpoint changes / Cleanup: restore the stored proxy config", "prerequest");
const proxyBefore = '{"enabled":false,"type":"http","url":"","timeout":0,"enable_for_scim":false,"enable_for_inference":false,"enable_for_api":false}';

test("proxy read stores the raw response text for a verbatim restore", () => {
  const { vars } = run(proxyRead, { responseBody: proxyBefore });
  assert.strictEqual(vars.rg_proxy_before_raw, proxyBefore);
  const bad = run(proxyRead, { responseBody: SPA_FALLBACK, responseCode: 503, variables: { rg_proxy_before_raw: "stale" } });
  assert.strictEqual(bad.vars.rg_proxy_before_raw, "", "a failed read never leaves a stale snapshot behind");
});

test("proxy check flags a leak only when enabled, type or url moved from the earlier value", () => {
  const before = { rg_proxy_before: JSON.stringify({ enabled: false, type: "http", url: "" }) };
  const same = run(proxyCheck, { responseBody: proxyBefore, variables: { ...before, rg_proxy_leaked: "1" } });
  assert.strictEqual(same.vars.rg_proxy_leaked, "", "an unchanged config clears a stale flag");
  const leakedBody = proxyBefore.replace('"enabled":false,"type":"http","url":""', '"enabled":true,"type":"http","url":"http://proxy.example.com:3128"');
  const leaked = run(proxyCheck, { responseBody: leakedBody, variables: before });
  assert.strictEqual(leaked.vars.rg_proxy_leaked, "1", "a stored rejected URL sets the flag");
});

test("proxy restore skips unless a leak was flagged and a raw snapshot exists", () => {
  assert.strictEqual(run(proxyRestorePre, { variables: { rg_proxy_leaked: "", rg_proxy_before_raw: proxyBefore } }).state.skipped, true);
  assert.strictEqual(run(proxyRestorePre, { variables: { rg_proxy_leaked: "1", rg_proxy_before_raw: "" } }).state.skipped, true);
  const live = run(proxyRestorePre, { variables: { rg_proxy_leaked: "1", rg_proxy_before_raw: proxyBefore } });
  assert.strictEqual(live.state.skipped, false);
  assert.strictEqual(live.pm.request.body.raw, proxyBefore, "the earlier config is resent byte for byte");
});

// ── Rate limit - Model config spelling: skip guards and the sibling check ─────
// The folder's counter-dependent assertions must be skip-guarded on priming success like
// the budget folder's, the dated-alias case must only run when the alias really aliases
// the configured model, and the sibling check must not accept a governance refusal.
const MS = "Model config spelling / ";
const msSetupPre = rateLimitScriptFor(MS + "Setup - Create VK for model config spelling", "prerequest");
const msPrimeTest = rateLimitScriptFor(MS + "Model config request 1 with the configured spelling succeeds", "test");
const msUsagePre = rateLimitScriptFor(MS + "Model config usage is recorded", "prerequest");
const msUsageTest = rateLimitScriptFor(MS + "Model config usage is recorded", "test");
const msUpperPre = rateLimitScriptFor(MS + "Prefixed upper-case spelling is blocked with 402 budget_exceeded", "prerequest");
const msAliasPre = rateLimitScriptFor(MS + "Dated alias spelling is blocked with 402 budget_exceeded", "prerequest");
const msSiblingTest = rateLimitScriptFor(MS + "Sibling model is not blocked by the model config budget", "test");

test("model config setup ties the dated-alias case to the configured model", () => {
  const defaults = run(msSetupPre, { variables: { chat_model: "gpt-4o", chat_model_alias: "gpt-4o-2024-08-06" } });
  assert.strictEqual(defaults.vars.ms_alias_ok, "1", "the default pair is an alias of the configured model");
  const overridden = run(msSetupPre, { variables: { chat_model: "claude-3-5-sonnet-latest", chat_model_alias: "gpt-4o-2024-08-06" } });
  assert.strictEqual(overridden.vars.ms_alias_ok, "", "a model override without a matching alias disables the alias case");
  assert.strictEqual(overridden.vars.ms_priming_ok, "", "priming and usage flags start cleared");
  assert.strictEqual(overridden.vars.ms_usage_ok, "");
});

test("model config priming records success only on a 2xx", () => {
  assert.strictEqual(run(msPrimeTest, { responseBody: "{}", responseCode: 200 }).vars.ms_priming_ok, "1");
  const down = run(msPrimeTest, { responseBody: SPA_FALLBACK, responseCode: 502 });
  assert.strictEqual(down.vars.ms_priming_ok, "");
  assert.ok(down.state.tests.every((x) => x.passed), "a failed priming is reported as skipped, not failed");
});

test("model config usage poll and 402 checks skip without priming or recorded usage", () => {
  assert.strictEqual(run(msUsagePre, { variables: { ms_priming_ok: "" } }).state.skipped, true);
  assert.strictEqual(run(msUsagePre, { variables: { ms_priming_ok: "1", ms_poll_retry: "0" } }).state.skipped, false);
  const skipped = run(msUsageTest, { responseBody: "{}", responseCode: 200, variables: { ms_priming_ok: "" } });
  assert.ok(skipped.state.tests.every((x) => x.passed), "the usage poll reports skipped when priming failed");
  assert.strictEqual(run(msUpperPre, { variables: { ms_usage_ok: "" } }).state.skipped, true);
  assert.strictEqual(run(msUpperPre, { variables: { ms_usage_ok: "1" } }).state.skipped, false);
  assert.strictEqual(run(msAliasPre, { variables: { ms_usage_ok: "1", ms_alias_ok: "" } }).state.skipped, true, "alias case skips when the alias is not tied to the model");
  assert.strictEqual(run(msAliasPre, { variables: { ms_usage_ok: "", ms_alias_ok: "1" } }).state.skipped, true, "alias case skips without recorded usage");
  assert.strictEqual(run(msAliasPre, { variables: { ms_usage_ok: "1", ms_alias_ok: "1" } }).state.skipped, false);
});

test("sibling check treats a governance refusal as a failure, not only budget_exceeded", () => {
  const blocked = run(msSiblingTest, { responseBody: JSON.stringify({ error: { type: "model_blocked", message: "model not allowed" } }), responseCode: 403 });
  assert.strictEqual(blocked.vars.ms_sibling_refused, "1", "a 403 model_blocked is a governance refusal");
  const provider = run(msSiblingTest, { responseBody: JSON.stringify({ error: { message: "upstream unavailable" } }), responseCode: 502 });
  assert.strictEqual(provider.vars.ms_sibling_refused, "", "a provider-side error is not a governance refusal");
  const ok = run(msSiblingTest, { responseBody: JSON.stringify({ choices: [] }), responseCode: 200 });
  assert.strictEqual(ok.vars.ms_sibling_refused, "");
});

console.log(`\n${passed} passed, ${failed} failed`);
process.exit(failed === 0 ? 0 : 1);
