#!/usr/bin/env node
// Generate the model-catalog wiring Postman collection.
//
// Each scenario stands up an isolated, run-namespaced custom provider backed by
// a real upstream (OpenAI, Anthropic, or Gemini), drives provider/key mutations
// through the management API, and asserts the catalog read endpoints reflect
// each mutation. The live-model cache is populated asynchronously by the key
// hooks, so every post-mutation read polls with exponential backoff. Output is
// machine-generated — edit this script and re-run it; do not hand-edit the JSON.
//
//   node build-model-catalog-wiring.mjs [--out path.json]

import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import {
  url,
  events,
  request,
  item,
  folderPrerequest,
  pollPrerequest,
  pollTest,
  mutationTest,
  cleanupTest,
  buildCollection,
  writeCollection,
  resolveOutPath,
} from "./lib/collection-builder.mjs";

const HERE = dirname(fileURLToPath(import.meta.url));
const DEFAULT_OUT = join(HERE, "..", "collections", "bifrost-model-catalog-wiring.postman_collection.json");

// Each entry generates the full six-scenario suite against a custom provider
// whose base_provider_type is `type`. Key values use Bifrost's `env.<NAME>`
// resolution: the credential is read from the Bifrost process env at request
// time, so the collection carries no secret and the runner injects nothing.
// base_url is omitted — the base provider type's default applies.
const PROVIDERS = [
  { type: "openai", keyEnv: "OPENAI_API_KEY", modelA: "gpt-4o", modelB: "gpt-4o-mini", inferenceModel: "gpt-4o-mini" },
  { type: "anthropic", keyEnv: "ANTHROPIC_API_KEY", modelA: "claude-sonnet-4-5", modelB: "claude-haiku-4-5", inferenceModel: "claude-haiku-4-5" },
  { type: "gemini", keyEnv: "GEMINI_API_KEY", modelA: "gemini-2.5-flash", modelB: "gemini-2.5-flash-lite", inferenceModel: "gemini-2.5-flash-lite" },
];

// --------------------------------------------------------------------------- //
// Scenario spec helpers
// --------------------------------------------------------------------------- //

// A key carried through add/update. Updates resend the full intended state
// (value, models, enabled, aliases) because a key PUT is a full-field replace
// and upstreams reject an empty credential value.
function key({ id, models = [], blacklisted = [], enabled = true, aliases = {} }) {
  return { id, models, blacklisted, enabled, aliases };
}

// Key names and ids must be unique across all providers, so namespace by
// scenario (which embeds the provider type) + key id + run id. Two same-named
// keys (e.g. an empty name) collide otherwise.
function keyBody(k, sid, name, keyEnv) {
  const out = {
    id: keyId(sid, k.id),
    name,
    value: `env.${keyEnv}`,
    models: [...k.models],
    enabled: k.enabled,
  };
  if (k.blacklisted.length) out.blacklisted_models = [...k.blacklisted];
  if (Object.keys(k.aliases).length) out.aliases = { ...k.aliases };
  return out;
}

const keyId = (sid, kid) => `${sid}-${kid}-{{run_id}}`;
const providerSeg = (sid) => `catwiring-${sid}-{{run_id}}`;
const jsProviderName = (sid) => `'catwiring-${sid}-' + pm.variables.get('run_id')`;

// --------------------------------------------------------------------------- //
// Assertion line builders
// --------------------------------------------------------------------------- //

function listModelsAssertLines(sid, { subset = [], superset = [], absent = [], empty = false }) {
  return [
    `var providerName = ${jsProviderName(sid)};`,
    `var expectSubset = ${JSON.stringify(subset)};`,
    `var expectSuperset = ${JSON.stringify(superset)};`,
    `var expectAbsent = ${JSON.stringify(absent)};`,
    `var expectEmpty = ${empty ? "true" : "false"};`,
    "if (pm.response.code !== 200) { throw new Error('list models status ' + pm.response.code); }",
    "var body = pm.response.json();",
    "var names = (body.models || []).filter(function (m) { return m.provider === providerName; })",
    "  .map(function (m) { return m.name; });",
    "if (expectEmpty && names.length !== 0) { throw new Error('expected no models for ' + providerName + ' but got ' + JSON.stringify(names)); }",
    "expectSubset.concat(expectSuperset).forEach(function (m) {",
    "  if (names.indexOf(m) < 0) { throw new Error('expected model ' + m + ' in ' + JSON.stringify(names)); }",
    "});",
    "expectAbsent.forEach(function (m) {",
    "  if (names.indexOf(m) >= 0) { throw new Error('expected model ' + m + ' absent but found in ' + JSON.stringify(names)); }",
    "});",
  ];
}

function inferenceAssertLines(resolvedModel) {
  return [
    "if (pm.response.code !== 200) { throw new Error('inference status ' + pm.response.code + ' body ' + pm.response.text()); }",
    "var body = pm.response.json();",
    "if (!body.choices || body.choices.length === 0) { throw new Error('inference returned no choices'); }",
    "var used = body.model || '';",
    `if (used.indexOf(${JSON.stringify(resolvedModel)}) < 0) { throw new Error('expected resolved model ${resolvedModel} in response.model=' + used); }`,
  ];
}

// --------------------------------------------------------------------------- //
// Step expansion
// --------------------------------------------------------------------------- //

function expandScenario(sc) {
  const sid = sc.id;
  const provider = sc.provider;
  const seg = providerSeg(sid);
  const cleanupName = `cleanup: delete provider [${sid}]`;
  const items = [];
  let counter = 0;
  let ordinal = 0;
  const nextId = (tag) => `catwiring-${sid}-${String(++counter).padStart(2, "0")}-${tag}`;
  const uniq = (label) => `${String(++ordinal).padStart(2, "0")}. ${label} [${sid}]`;

  const addKey = (k) => {
    const name = uniq("add key " + k.id);
    const keyName = `catwiring-mc-${sid}-${k.id}-{{run_id}}`;
    return item(
      nextId("add-key"),
      name,
      request("POST", url(["api", "providers", seg, "keys"]), keyBody(k, sid, keyName, provider.keyEnv)),
      events(null, mutationTest(name, [200, 201], cleanupName))
    );
  };

  for (const step of sc.steps) {
    switch (step.type) {
      case "addProvider": {
        const body = {
          provider: seg,
          custom_provider_config: { base_provider_type: provider.type, is_key_less: false },
        };
        const name = uniq("add provider");
        items.push(item(nextId("add-provider"), name, request("POST", url(["api", "providers"]), body),
          events(null, mutationTest(name, [200, 201], cleanupName))));
        for (const k of step.keys || []) items.push(addKey(k));
        break;
      }
      case "addKey":
        items.push(addKey(step.key));
        break;
      case "updateKey": {
        const name = uniq("update key " + step.key.id);
        items.push(item(nextId("update-key"), name,
          request("PUT", url(["api", "providers", seg, "keys", keyId(sid, step.key.id)]), keyBody(step.key, sid, undefined, provider.keyEnv)),
          events(null, mutationTest(name, [200], cleanupName))));
        break;
      }
      case "deleteKey": {
        const name = uniq("delete key " + step.id);
        items.push(item(nextId("delete-key"), name,
          request("DELETE", url(["api", "providers", seg, "keys", keyId(sid, step.id)]), null),
          events(null, mutationTest(name, [200, 204], cleanupName))));
        break;
      }
      case "deleteProvider": {
        const name = uniq("delete provider");
        items.push(item(nextId("delete-provider"), name,
          request("DELETE", url(["api", "providers", seg]), null),
          events(null, mutationTest(name, [200, 204], cleanupName))));
        break;
      }
      case "assertModels": {
        const name = uniq(step.label);
        const query = [{ key: "provider", value: seg }, { key: "limit", value: "1000" }];
        items.push(item(nextId("assert-models"), name, request("GET", url(["api", "models"], query), null),
          events(pollPrerequest(step.waitSeconds), pollTest(name, listModelsAssertLines(sid, step), cleanupName))));
        break;
      }
      case "assertInference": {
        const name = uniq(step.label);
        const body = {
          model: `${seg}/${step.requestModel}`,
          messages: [{ role: "user", content: "Reply with the single word: ok." }],
          max_tokens: 5,
        };
        items.push(item(nextId("assert-inference"), name, request("POST", url(["v1", "chat", "completions"]), body),
          events(pollPrerequest(step.waitSeconds), pollTest(name, inferenceAssertLines(step.expectResolved), cleanupName))));
        break;
      }
      case "cleanup":
        break; // teardown folder is always appended
      default:
        throw new Error("unknown step type: " + step.type);
    }
  }

  const cleanupItem = item(
    `catwiring-${sid}-cleanup`,
    cleanupName,
    request("DELETE", url(["api", "providers", seg]), null),
    events(null, cleanupTest(cleanupName))
  );

  return {
    name: sc.title,
    description: sc.description,
    item: [...items, { name: "Cleanup", item: [cleanupItem] }],
    event: events(folderPrerequest([]), null),
  };
}

// --------------------------------------------------------------------------- //
// Scenarios — six canonical wiring contracts, generated per provider
// --------------------------------------------------------------------------- //

function scenariosFor(provider) {
  const p = provider.type;
  const MODEL_A = provider.modelA;
  const MODEL_B = provider.modelB;
  const INFERENCE_MODEL = provider.inferenceModel;
  const ALIAS = `catwiring-alias-${p}-{{run_id}}`;

  return [
    {
      id: `${p}-add-provider-and-key`,
      title: `Add provider and key surfaces models (${p})`,
      description: "A fresh provider plus one gated key surfaces that key's allowed model in the catalog.",
      steps: [
        { type: "addProvider" },
        { type: "addKey", key: key({ id: "k1", models: [MODEL_A] }) },
        { type: "assertModels", subset: [MODEL_A], waitSeconds: 2, label: "key model appears in catalog" },
        { type: "cleanup" },
      ],
    },
    {
      id: `${p}-update-key-models`,
      title: `Updating key model set re-gates the catalog (${p})`,
      description: "Changing a key's allow-list invalidates the stale live entry and re-gates the surfaced models.",
      steps: [
        { type: "addProvider", keys: [key({ id: "k1", models: [MODEL_A] })] },
        { type: "assertModels", subset: [MODEL_A], waitSeconds: 2, label: "initial model gated in" },
        { type: "updateKey", key: key({ id: "k1", models: [MODEL_B] }) },
        { type: "assertModels", subset: [MODEL_B], absent: [MODEL_A], waitSeconds: 2, label: "new model gated in, old gated out" },
        { type: "cleanup" },
      ],
    },
    {
      id: `${p}-disable-reenable-key`,
      title: `Disabled key drops models, re-enable restores (${p})`,
      description: "A disabled key is skipped during aggregation; re-enabling it brings its models back.",
      steps: [
        { type: "addProvider", keys: [key({ id: "k1", models: [MODEL_A] })] },
        { type: "assertModels", subset: [MODEL_A], waitSeconds: 2, label: "model present while enabled" },
        { type: "updateKey", key: key({ id: "k1", models: [MODEL_A], enabled: false }) },
        { type: "assertModels", absent: [MODEL_A], waitSeconds: 2, label: "model gone while disabled" },
        { type: "updateKey", key: key({ id: "k1", models: [MODEL_A], enabled: true }) },
        { type: "assertModels", subset: [MODEL_A], waitSeconds: 2, label: "model returns when re-enabled" },
        { type: "cleanup" },
      ],
    },
    {
      id: `${p}-delete-key-sibling-survives`,
      title: `Deleting one key leaves sibling models intact (${p})`,
      description: "With two keys gating distinct models, deleting one removes only its models; the sibling's survive.",
      steps: [
        { type: "addProvider", keys: [key({ id: "k1", models: [MODEL_A] }), key({ id: "k2", models: [MODEL_B] })] },
        { type: "assertModels", superset: [MODEL_A, MODEL_B], waitSeconds: 2, label: "both keys' models present" },
        { type: "deleteKey", id: "k1" },
        { type: "assertModels", subset: [MODEL_B], absent: [MODEL_A], waitSeconds: 2, label: "sibling model survives delete" },
        { type: "cleanup" },
      ],
    },
    {
      id: `${p}-delete-provider`,
      title: `Deleting provider removes it from the catalog (${p})`,
      description: "Removing the provider drops all of its models from the catalog read endpoints.",
      steps: [
        { type: "addProvider", keys: [key({ id: "k1", models: [MODEL_A] })] },
        { type: "assertModels", subset: [MODEL_A], waitSeconds: 2, label: "model present before delete" },
        { type: "deleteProvider" },
        { type: "assertModels", empty: true, waitSeconds: 1, label: "no models after provider delete" },
        { type: "cleanup" },
      ],
    },
    {
      id: `${p}-alias-resolution`,
      title: `Alias resolves to underlying model at inference (${p})`,
      description: "A key alias routes an inference request to the underlying wire model rather than being rejected.",
      steps: [
        { type: "addProvider", keys: [key({ id: "k1", models: [ALIAS, INFERENCE_MODEL], aliases: { [ALIAS]: INFERENCE_MODEL } })] },
        { type: "assertModels", subset: [INFERENCE_MODEL], waitSeconds: 2, label: "aliased key model present" },
        { type: "assertInference", requestModel: ALIAS, expectResolved: INFERENCE_MODEL, waitSeconds: 2, label: "alias resolves to underlying model" },
        { type: "cleanup" },
      ],
    },
  ].map((sc) => ({ ...sc, provider }));
}

const SCENARIOS = PROVIDERS.flatMap(scenariosFor);

const collection = buildCollection({
  id: "bifrost-model-catalog-wiring",
  name: "Bifrost Model Catalog Wiring",
  description:
    "End-to-end wiring tests between the management API and the model catalog. Each scenario stands up " +
    "an isolated, run-namespaced custom provider backed by a real upstream (OpenAI, Anthropic, or Gemini), " +
    "mutates its providers/keys, and asserts the catalog read endpoints reflect each mutation. Reads that " +
    "depend on the asynchronously populated live-model cache poll with exponential backoff. " +
    "Machine-generated by runners/build-model-catalog-wiring.mjs — do not hand-edit.",
  expandedScenarios: SCENARIOS.map(expandScenario),
});

writeCollection(resolveOutPath(DEFAULT_OUT), collection, SCENARIOS.map((s) => s.id));
