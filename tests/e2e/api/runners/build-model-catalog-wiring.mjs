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
// Updates resend this same name: a PUT with no name clears it to "", and empty
// names collide with each other across providers (names are globally unique).
const keyName = (sid, kid) => `catwiring-mc-${sid}-${kid}-{{run_id}}`;
const providerSeg = (sid) => `catwiring-${sid}-{{run_id}}`;
const jsProviderName = (sid) => `'catwiring-${sid}-' + pm.variables.get('run_id')`;

// --------------------------------------------------------------------------- //
// Assertion line builders
// --------------------------------------------------------------------------- //

function listModelsAssertLines(sid, { subset = [], superset = [], absent = [], absentVars = [], empty = false, nonEmpty = false }) {
  return [
    `var providerName = ${jsProviderName(sid)};`,
    `var expectSubset = ${JSON.stringify(subset)};`,
    `var expectSuperset = ${JSON.stringify(superset)};`,
    `var expectAbsent = ${JSON.stringify(absent)};`,
    `var expectAbsentVars = ${JSON.stringify(absentVars)};`,
    `var expectEmpty = ${empty ? "true" : "false"};`,
    `var expectNonEmpty = ${nonEmpty ? "true" : "false"};`,
    "if (pm.response.code !== 200) { throw new Error('list models status ' + pm.response.code); }",
    "var body = pm.response.json();",
    "var names = (body.models || []).filter(function (m) { return m.provider === providerName; })",
    "  .map(function (m) { return m.name; });",
    "if (expectEmpty && names.length !== 0) { throw new Error('expected no models for ' + providerName + ' but got ' + JSON.stringify(names)); }",
    "if (expectNonEmpty && names.length === 0) { throw new Error('expected a non-empty catalog for ' + providerName); }",
    "expectSubset.concat(expectSuperset).forEach(function (m) {",
    "  if (names.indexOf(m) < 0) { throw new Error('expected model ' + m + ' in ' + JSON.stringify(names)); }",
    "});",
    "expectAbsentVars.forEach(function (v) {",
    "  var captured = pm.variables.get(v);",
    "  if (!captured) { throw new Error('captured variable ' + v + ' is empty'); }",
    "  expectAbsent = expectAbsent.concat([captured]);",
    "});",
    "expectAbsent.forEach(function (m) {",
    "  if (names.indexOf(m) >= 0) { throw new Error('expected model ' + m + ' absent but found in ' + JSON.stringify(names)); }",
    "});",
  ];
}

// Capture the first live model whose name starts with `prefix` into a
// collection variable, so later steps can mutate and assert against whatever
// the upstream actually reported (some upstreams only list dated ids, e.g.
// claude-sonnet-4-5-20250929, so static names can't be relied on here).
function captureModelAssertLines(sid, prefix, varName) {
  return [
    `var providerName = ${jsProviderName(sid)};`,
    `var prefix = ${JSON.stringify(prefix)};`,
    "if (pm.response.code !== 200) { throw new Error('list models status ' + pm.response.code); }",
    "var body = pm.response.json();",
    "var names = (body.models || []).filter(function (m) { return m.provider === providerName; })",
    "  .map(function (m) { return m.name; });",
    "var hit = null;",
    "for (var i = 0; i < names.length; i++) {",
    "  if (names[i].indexOf(prefix) === 0) { hit = names[i]; break; }",
    "}",
    "if (!hit) { throw new Error('no live model with prefix ' + prefix + ' in ' + JSON.stringify(names.slice(0, 20))); }",
    `pm.collectionVariables.set(${JSON.stringify(varName)}, hit);`,
  ];
}

function providersAssertLines(sid) {
  return [
    `var providerName = ${jsProviderName(sid)};`,
    "if (pm.response.code !== 200) { throw new Error('list providers status ' + pm.response.code); }",
    "var body = pm.response.json();",
    "var names = (body.providers || []).map(function (p) { return p.name; });",
    "if (names.indexOf(providerName) < 0) { throw new Error('expected provider ' + providerName + ' in providers list'); }",
  ];
}

function baseModelsAssertLines(target) {
  return [
    `var target = ${JSON.stringify(target)};`,
    "if (pm.response.code !== 200) { throw new Error('list base models status ' + pm.response.code); }",
    "var body = pm.response.json();",
    "var names = body.models || [];",
    "if (names.indexOf(target) < 0) { throw new Error('expected base model ' + target + ' in ' + JSON.stringify(names)); }",
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
    return item(
      nextId("add-key"),
      name,
      request("POST", url(["api", "providers", seg, "keys"]), keyBody(k, sid, keyName(sid, k.id), provider.keyEnv)),
      events(null, mutationTest(name, [200, 201], cleanupName))
    );
  };

  for (const step of sc.steps) {
    switch (step.type) {
      case "addProvider": {
        const customProviderConfig = { base_provider_type: provider.type, is_key_less: !!step.keyless };
        if (step.allowedRequests) customProviderConfig.allowed_requests = step.allowedRequests;
        const body = { provider: seg, custom_provider_config: customProviderConfig };
        if (step.baseUrl) body.network_config = { base_url: step.baseUrl };
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
          request("PUT", url(["api", "providers", seg, "keys", keyId(sid, step.key.id)]), keyBody(step.key, sid, keyName(sid, step.key.id), provider.keyEnv)),
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
      case "captureModel": {
        const name = uniq(step.label);
        const query = [{ key: "provider", value: seg }, { key: "limit", value: "2000" }];
        items.push(item(nextId("capture-model"), name, request("GET", url(["api", "models"], query), null),
          events(pollPrerequest(step.waitSeconds), pollTest(name, captureModelAssertLines(sid, step.prefix, step.varName), cleanupName))));
        break;
      }
      case "assertModelDetails": {
        const name = uniq(step.label);
        const query = [{ key: "provider", value: seg }, { key: "limit", value: "1000" }];
        items.push(item(nextId("assert-model-details"), name, request("GET", url(["api", "models", "details"], query), null),
          events(pollPrerequest(step.waitSeconds), pollTest(name, listModelsAssertLines(sid, step), cleanupName))));
        break;
      }
      case "assertProviders": {
        const name = uniq(step.label);
        items.push(item(nextId("assert-providers"), name, request("GET", url(["api", "providers"]), null),
          events(pollPrerequest(step.waitSeconds), pollTest(name, providersAssertLines(sid), cleanupName))));
        break;
      }
      case "assertBaseModels": {
        const name = uniq(step.label);
        const query = [{ key: "query", value: step.model }, { key: "limit", value: "1000" }];
        items.push(item(nextId("assert-base-models"), name, request("GET", url(["api", "models", "base"], query), null),
          events(pollPrerequest(step.waitSeconds), pollTest(name, baseModelsAssertLines(step.model), cleanupName))));
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
  // Collection variable holding the live model captured for the blacklist
  // re-gate scenario; unique per provider so suites never cross-read.
  const BL_VAR = `bl_target_${p}`;

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
    {
      id: `${p}-blacklist-regate`,
      title: `Blacklisting a model re-gates a wildcard catalog (${p})`,
      description:
        "A wildcard key surfaces the upstream's live model list; adding one of those models to the key's " +
        "blacklist drops it from the catalog while the rest of the list stays. The target model is captured " +
        "from the live list at run time because some upstreams only report dated ids.",
      steps: [
        { type: "addProvider", keys: [key({ id: "k1", models: ["*"] })] },
        { type: "captureModel", prefix: MODEL_A, varName: BL_VAR, waitSeconds: 2, label: "wildcard catalog serves live models" },
        { type: "updateKey", key: key({ id: "k1", models: ["*"], blacklisted: [`{{${BL_VAR}}}`] }) },
        { type: "assertModels", absentVars: [BL_VAR], nonEmpty: true, waitSeconds: 2, label: "blacklisted model gated out, catalog still populated" },
        { type: "cleanup" },
      ],
    },
    {
      id: `${p}-multi-key-union`,
      title: `Catalog aggregates the union across enabled keys (${p})`,
      description: "Two keys gating distinct models both contribute: the catalog lists the union of their allow-lists.",
      steps: [
        { type: "addProvider", keys: [key({ id: "k1", models: [MODEL_A] }), key({ id: "k2", models: [MODEL_B] })] },
        { type: "assertModels", superset: [MODEL_A, MODEL_B], waitSeconds: 2, label: "catalog lists union of both keys' models" },
        { type: "cleanup" },
      ],
    },
    {
      id: `${p}-model-details-gating`,
      title: `Model details endpoint respects the key gate (${p})`,
      description: "/api/models/details lists only the models the provider's keys allow, like the plain list endpoint.",
      steps: [
        { type: "addProvider", keys: [key({ id: "k1", models: [MODEL_A] })] },
        { type: "assertModelDetails", subset: [MODEL_A], absent: [MODEL_B], waitSeconds: 2, label: "details list gated to key models" },
        { type: "cleanup" },
      ],
    },
    {
      id: `${p}-base-models-stable`,
      title: `Base model list is unaffected by key changes (${p})`,
      description:
        "/api/models/base reflects the datasheet's distinct base names; removing a model from a key's " +
        "allow-list must not remove its base name from that list.",
      steps: [
        { type: "addProvider", keys: [key({ id: "k1", models: [MODEL_A] })] },
        { type: "assertBaseModels", model: MODEL_A, waitSeconds: 1, label: "base name listed while key allows it" },
        { type: "updateKey", key: key({ id: "k1", models: [MODEL_B] }) },
        { type: "assertBaseModels", model: MODEL_A, waitSeconds: 1, label: "base name still listed after key change" },
        { type: "cleanup" },
      ],
    },
  ].map((sc) => ({ ...sc, provider }));
}

// Scenarios whose wiring is base-type independent run once instead of per
// provider; they all use the first provider entry as an arbitrary base type.
const GLOBAL_SCENARIOS = [
  // A keyless custom provider is gate-unrestricted at request time but
  // contributes no rows to the catalog read: there is no live discovery
  // without keys, and datasheet rows are keyed by standard provider names only.
  {
    id: "keyless-provider",
    title: "Keyless provider is listed but contributes no catalog rows",
    description:
      "A custom provider created with is_key_less=true appears in the providers list; its catalog read " +
      "succeeds and is empty (no live discovery without keys, no datasheet rows under the custom name).",
    provider: PROVIDERS[0],
    steps: [
      { type: "addProvider", keyless: true },
      { type: "assertProviders", waitSeconds: 1, label: "keyless provider appears in providers list" },
      { type: "assertModels", empty: true, waitSeconds: 1, label: "keyless provider has no catalog rows" },
      { type: "cleanup" },
    ],
  },
  // Discovery failure must degrade, not blank: when the upstream's list-models
  // endpoint is unreachable, the catalog still serves the models aggregated
  // from the keys' explicit allow-lists. Port 9 (discard) is never listening,
  // so the failure is deterministic and no request leaves the host.
  {
    id: "list-models-failing",
    title: "Unreachable list-models upstream keeps key allow-list models",
    description:
      "A provider whose base_url points at a dead port cannot complete live model discovery; the catalog " +
      "still surfaces the models explicitly allowed by its keys.",
    provider: PROVIDERS[0],
    steps: [
      { type: "addProvider", baseUrl: "http://127.0.0.1:9", keys: [key({ id: "k1", models: [PROVIDERS[0].modelA] })] },
      { type: "assertModels", subset: [PROVIDERS[0].modelA], waitSeconds: 2, label: "key allow-list survives discovery failure" },
      { type: "cleanup" },
    ],
  },
  // With list_models excluded from allowed_requests, discovery never runs: a
  // wildcard key has no live list to expand against and surfaces nothing,
  // while an explicit allow-list still surfaces through the key aggregates.
  // The wait before the empty read is deliberately longer than discovery
  // normally takes, so a wrongly-attempted discovery would be caught.
  {
    id: "list-models-disabled",
    title: "Disallowed list-models blocks discovery but not explicit allow-lists",
    description:
      "A provider whose allowed_requests excludes list_models performs no live discovery: a wildcard key " +
      "surfaces no catalog rows, while a key with an explicit allow-list still surfaces its models.",
    provider: PROVIDERS[0],
    steps: [
      {
        type: "addProvider",
        allowedRequests: { chat_completion: true, chat_completion_stream: true },
        keys: [key({ id: "k1", models: ["*"] })],
      },
      { type: "assertModels", empty: true, waitSeconds: 4, label: "wildcard key surfaces nothing without discovery" },
      { type: "addKey", key: key({ id: "k2", models: [PROVIDERS[0].modelB] }) },
      { type: "assertModels", subset: [PROVIDERS[0].modelB], waitSeconds: 2, label: "explicit allow-list still surfaces" },
      { type: "cleanup" },
    ],
  },
];

const SCENARIOS = [...PROVIDERS.flatMap(scenariosFor), ...GLOBAL_SCENARIOS];

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
