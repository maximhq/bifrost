// Run the collection's real scripts without a gateway: node --test model-parameters-sync.test.mjs.
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const collection = JSON.parse(readFileSync(new URL("../../collections/provider-harness.json", import.meta.url), "utf8"));
const folder = collection.item.find((item) => item.name.includes("(model-parameters-sync)"));
const [health, provider, sync] = folder.item;
const fixtureURL = "http://127.0.0.1:8797";
const identity = { fixture: "model-parameters-sync-8073" };
const ownedProvider = {
  name: "custom-anthropic-sync",
  network_config: { base_url: fixtureURL },
  custom_provider_config: { base_provider_type: "anthropic" },
};

// Postman records failed tests and keeps running; throwing out of pm.test would hide this bug.
function runGate({ healthCode = 200, healthBody = identity, providerCode = 200,
  providerBody = ownedProvider, transportFailure = false, stale = false } = {}) {
  const values = new Map(stale ? [["modelParamsFixtureHealthy", "1"], ["modelParamsFixtureReady", "1"]] : []);
  const failures = [];
  let skipped = false;
  const pm = {
    collectionVariables: { get: (key) => values.get(key), set: (key, value) => values.set(key, value), unset: (key) => values.delete(key) },
    variables: { get: () => fixtureURL },
    execution: { skipRequest: () => { skipped = true; } },
    test: (name, fn) => { try { fn(); } catch (error) { failures.push({ name, error }); } },
    expect: (actual, message) => ({ to: {
      equal: (expected) => assert.equal(actual, expected, message),
      deep: { equal: (expected) => assert.deepEqual(actual, expected, message) },
    } }),
  };
  const execute = (item, phase) => {
    for (const event of item.event || []) {
      if (event.listen === phase) new Function("pm", event.script.exec.join("\n"))(pm);
    }
  };
  const response = (code, body) => ({ code, json: () => JSON.parse(JSON.stringify(body)), text: () => JSON.stringify(body) });
  execute(health, "prerequest");
  if (!transportFailure) {
    pm.response = response(healthCode, healthBody);
    execute(health, "test");
  }
  pm.response = response(providerCode, providerBody);
  execute(provider, "test");
  execute(sync, "prerequest");
  return { skipped, failures };
}

test("successful fixture and provider checks allow force-sync", () => {
  const result = runGate();
  assert.equal(result.skipped, false);
  assert.equal(result.failures.length, 0);
});

for (const [name, input] of [
  ["wrong fixture identity", { healthBody: { fixture: "other-fixture" } }],
  ["fixture HTTP failure", { healthCode: 500 }],
  ["failed health request with stale success flags", { transportFailure: true, stale: true }],
  ["provider HTTP failure", { providerCode: 500 }],
  ["wrong provider identity", { providerBody: { ...ownedProvider, name: "other-provider" } }],
]) {
  test(`${name} prevents force-sync and reports a failed assertion`, () => {
    const result = runGate(input);
    assert.equal(result.skipped, true);
    assert.ok(result.failures.length > 0);
  });
}
