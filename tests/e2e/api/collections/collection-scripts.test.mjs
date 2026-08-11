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

console.log(`\n${passed} passed, ${failed} failed`);
process.exit(failed === 0 ? 0 : 1);
