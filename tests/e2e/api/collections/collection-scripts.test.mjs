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

console.log(`\n${passed} passed, ${failed} failed`);
process.exit(failed === 0 ? 0 : 1);
