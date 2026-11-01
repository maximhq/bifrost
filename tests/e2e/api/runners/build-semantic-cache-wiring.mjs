#!/usr/bin/env node
// Generate the semantic cache wiring Postman collection.
//
// The collection drives the semantic_cache plugin end to end against a clean server whose
// vector store is Redis or Weaviate; run-newman-semantic-cache-wiring-tests.sh boots one server
// per store and runs this same collection against each. Folders, in order:
//
//   P  preconditions and the providers every later folder uses
//   M  plugin create, read, update, disable, delete, invalid configs, dimension mismatch
//   D  direct cache on chat completions: keys, defaults, params, tools, normalization
//   X  every other cacheable request type, and the OpenAI and Anthropic SDK routes
//   T  how a stream is written and replayed
//   H  per-request headers: ttl, no-store, cache type, threshold, default key
//   C  the two cache clear endpoints
//   K  x-bifrost-cache-* response headers on every inference route
//   G  identity scoping by virtual key, on and off
//   S  semantic mode
//   O  logs: cache_debug on the row, filters, stats
//
// Every request asserts through the sandbox library in lib/semantic-cache-wiring.mjs: its test
// script evals the library and hands it a declarative spec (outcome, cache_id comparisons, saved
// values, stream replay checks). A read that must hit is polled with backoff and carries
// x-bf-cache-no-store, so a read that misses can never write the entry it is waiting for. A read
// that must miss is issued only after the entry it is isolated from was seen to hit.
//
// Output is machine-generated - edit this script and re-run:
//
//   node build-semantic-cache-wiring.mjs [--out path.json]

import { deflateSync } from "node:zlib";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { url, events, item, buildCollection, writeCollection, resolveOutPath } from "./lib/collection-builder.mjs";
import { scwLibSource, scwNamespaceSuffix } from "./lib/semantic-cache-wiring.mjs";

const HERE = dirname(fileURLToPath(import.meta.url));
const DEFAULT_OUT = join(HERE, "..", "collections", "bifrost-semantic-cache-wiring.postman_collection.json");

// --------------------------------------------------------------------------- //
// Providers, models, namespaces, plugin configs
// --------------------------------------------------------------------------- //

const MODEL = "openai/gpt-4o-mini";
const MODEL_ALT = "openai/gpt-4o";
// OpenAI has retired every model on /v1/completions, so text completions go to gpt-4o-mini
// through OpenRouter, which still serves that endpoint.
const TEXT_MODEL = "openrouter/openai/gpt-4o-mini";
const EMBED_MODEL = "text-embedding-3-small";
const EMBED = "scw-embed-{{run_id}}";
const ALT = "scw-alt-{{run_id}}";
const BADEMBED = "scw-badembed-{{run_id}}";

// Standard providers recreated after the clear-providers setup folder. Keys resolve through
// Bifrost's env.<NAME> lookup, so no credential is written into the collection.
const STANDARD_PROVIDERS = [
  { name: "openai", env: "OPENAI_API_KEY" },
  { name: "anthropic", env: "ANTHROPIC_API_KEY" },
  { name: "gemini", env: "GEMINI_API_KEY" },
  { name: "cohere", env: "COHERE_API_KEY" },
  { name: "openrouter", env: "OPENROUTER_API_KEY" },
];
// Custom providers on the OpenAI base: the embedding provider, a second provider for the
// cache_by_provider rows, and an embedding provider whose key is invalid (S26).
const CUSTOM_PROVIDERS = [
  { name: EMBED, value: "env.OPENAI_API_KEY" },
  { name: ALT, value: "env.OPENAI_API_KEY" },
  { name: BADEMBED, value: "sk-deadbeef-invalid-000" },
];
const BEDROCK_KEY = {
  name: "scw-bedrock-{{run_id}}",
  models: ["*"],
  weight: 1,
  aliases: { "claude-haiku-4-5": "us.anthropic.claude-haiku-4-5-20251001-v1:0" },
  bedrock_key_config: { access_key: "env.AWS_ACCESS_KEY_ID", secret_key: "env.AWS_SECRET_ACCESS_KEY", region: "env.AWS_REGION" },
};

// Namespaces are a capitalized prefix plus the run's alphanumeric suffix ({{ns}}), so every run
// writes to fresh indexes and Weaviate accepts the class names.
const NS = {
  direct: "ScwDirect{{ns}}",
  semantic: "ScwSem{{ns}}",
  semanticAlt: "ScwSemAlt{{ns}}",
  dim: "ScwDim{{ns}}",
  dimFresh: "ScwDimFresh{{ns}}",
  m20direct: "ScwModeDirect{{ns}}",
  m20semantic: "ScwModeSem{{ns}}",
  identity: "ScwIdSem{{ns}}",
};

// PUT merges config keys over the stored row, so a baseline names every field: null clears
// whatever an earlier folder set.
const RESET = {
  provider: null,
  embedding_model: null,
  threshold: null,
  default_cache_key: null,
  conversation_history_threshold: null,
  cache_by_model: null,
  cache_by_provider: null,
  exclude_system_prompt: null,
  cache_tool_call_responses: null,
  disable_identity_scoping: null,
};
const DIRECT = (over = {}) => ({ ...RESET, dimension: 1, ttl: "10m", vector_store_namespace: NS.direct, ...over });
const SEMANTIC = (over = {}) => ({
  ...RESET,
  provider: EMBED,
  embedding_model: EMBED_MODEL,
  dimension: 1536,
  threshold: 0.85,
  ttl: "10m",
  vector_store_namespace: NS.semantic,
  ...over,
});

// Hand-curated pairs from tests/semanticcache: each paraphrase sits at or above 0.9 cosine to
// its canonical prompt and each unrelated prompt well below 0.5, so 0.85 is never borderline.
const PAIRS = [
  { canonical: "What is the capital city of France in modern times?", paraphrase: "Tell me the capital city of France in modern times.", unrelated: "Explain how a transistor works at the silicon level." },
  { canonical: "At what temperature does pure water boil at sea level?", paraphrase: "What is the boiling point of pure water at sea level?", unrelated: "Recommend a well-known jazz album recorded in the 1960s." },
  { canonical: "How do I make a basic vinaigrette salad dressing at home?", paraphrase: "What are the steps to make a basic vinaigrette salad dressing at home?", unrelated: "Describe quantum entanglement in a single paragraph for a beginner." },
  { canonical: "Name a famous Italian opera composer from the nineteenth century.", paraphrase: "Tell me one famous Italian opera composer from the nineteenth century.", unrelated: "What is the average distance from Earth to the planet Mars?" },
  { canonical: "Briefly explain how photosynthesis works in green plants.", paraphrase: "In a few sentences, describe how photosynthesis works in green plants.", unrelated: "How do you knit a basic scarf using stockinette stitch?" },
];
const IMAGE_PAIR = {
  canonical: "A bright red apple sitting on a wooden kitchen table in daylight.",
  paraphrase: "A vivid red apple resting on a wooden kitchen table in daylight.",
};

// --------------------------------------------------------------------------- //
// Request bodies
// --------------------------------------------------------------------------- //

const chat = (prompt, extra = {}) => ({ model: MODEL, messages: [{ role: "user", content: prompt }], max_tokens: 8, temperature: 0, ...extra });
const chatSystem = (system, prompt, extra = {}) => ({
  model: MODEL,
  messages: [
    { role: "system", content: system },
    { role: "user", content: prompt },
  ],
  max_tokens: 8,
  temperature: 0,
  ...extra,
});
const chatMessages = (messages, extra = {}) => ({ model: MODEL, messages, max_tokens: 8, temperature: 0, ...extra });
const chatStream = (prompt, extra = {}) => chat(prompt, { max_tokens: 12, stream: true, ...extra });
const responses = (input, extra = {}) => ({ model: MODEL, input, max_output_tokens: 16, temperature: 0, ...extra });
const text = (prompt, extra = {}) => ({ model: TEXT_MODEL, prompt, max_tokens: 8, temperature: 0, ...extra });
const embedding = (input, extra = {}) => ({ model: `openai/${EMBED_MODEL}`, input, ...extra });
const image = (prompt) => ({ model: "openai/gpt-image-1.5", prompt, size: "1024x1024", quality: "low", n: 1 });

// Two tiny solid-color PNGs as data URLs, so the image-attachment rows never depend on a remote
// fetch by the provider.
function crc32(buf) {
  let c = ~0;
  for (const b of buf) {
    c ^= b;
    for (let k = 0; k < 8; k++) c = c & 1 ? (c >>> 1) ^ 0xedb88320 : c >>> 1;
  }
  return ~c >>> 0;
}
function pngChunk(type, data) {
  const len = Buffer.alloc(4);
  len.writeUInt32BE(data.length);
  const body = Buffer.concat([Buffer.from(type, "ascii"), data]);
  const crc = Buffer.alloc(4);
  crc.writeUInt32BE(crc32(body));
  return Buffer.concat([len, body, crc]);
}
function solidPng(r, g, b, size = 32) {
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(size, 0);
  ihdr.writeUInt32BE(size, 4);
  ihdr[8] = 8; // bit depth
  ihdr[9] = 2; // truecolor
  const row = Buffer.alloc(1 + size * 3);
  for (let x = 0; x < size; x++) row.set([r, g, b], 1 + x * 3);
  const raw = Buffer.concat(Array.from({ length: size }, () => row));
  const png = Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    pngChunk("IHDR", ihdr),
    pngChunk("IDAT", deflateSync(raw)),
    pngChunk("IEND", Buffer.alloc(0)),
  ]);
  return "data:image/png;base64," + png.toString("base64");
}
const RED_PNG = solidPng(220, 30, 30);
const BLUE_PNG = solidPng(30, 60, 220);
const withImage = (prompt, dataUrl) =>
  chatMessages([
    {
      role: "user",
      content: [
        { type: "text", text: prompt },
        { type: "image_url", image_url: { url: dataUrl } },
      ],
    },
  ]);

const fn = (name, description, props) => ({
  type: "function",
  function: { name, description, parameters: { type: "object", properties: props, required: Object.keys(props) } },
});
const WEATHER = fn("get_weather", "Get the current weather for a city.", { city: { type: "string" } });
const TIME = fn("get_time", "Get the current local time for a city.", { city: { type: "string" } });
const FORECAST = fn("get_forecast", "Get the current weather for a city.", { city: { type: "string" } });
const FORCE_WEATHER = { type: "function", function: { name: "get_weather" } };

// --------------------------------------------------------------------------- //
// Item builders
// --------------------------------------------------------------------------- //

const ROUTES = {
  chat: ["v1", "chat", "completions"],
  responses: ["v1", "responses"],
  text: ["v1", "completions"],
  embed: ["v1", "embeddings"],
  speech: ["v1", "audio", "speech"],
  transcribe: ["v1", "audio", "transcriptions"],
  image: ["v1", "images", "generations"],
  inputTokens: ["v1", "responses", "input_tokens"],
  openai: ["openai", "v1", "chat", "completions"],
  anthropic: ["anthropic", "v1", "messages"],
  genai: ["genai", "v1beta", "models", "gemini-2.5-flash:generateContent"],
  cohere: ["cohere", "v2", "chat"],
  bedrock: ["bedrock", "model", "claude-haiku-4-5", "converse"],
};

const H = {
  key: (k) => ({ key: "x-bf-cache-key", value: `${k}-{{run_id}}` }),
  rawKey: (v) => ({ key: "x-bf-cache-key", value: v }),
  noStore: (v = "true") => ({ key: "x-bf-cache-no-store", value: v }),
  ttl: (v) => ({ key: "x-bf-cache-ttl", value: v }),
  type: (v) => ({ key: "x-bf-cache-type", value: v }),
  threshold: (v) => ({ key: "x-bf-cache-threshold", value: v }),
  vk: (who) => ({ key: "x-bf-vk", value: `{{vk${who}_value}}` }),
  anthropicVersion: () => ({ key: "anthropic-version", value: "2023-06-01" }),
};

const EVAL = "eval(pm.collectionVariables.get('scw_lib'));";
const usedNames = new Set();
let itemCounter = 0;

// One request. spec is what scw.run asserts; opts.wait busy-waits before the first attempt.
function call(name, method, segs, body, headers, spec, opts = {}) {
  if (usedNames.has(name)) throw new Error(`duplicate request name: ${name}`);
  usedNames.add(name);
  const req = { method, header: [...headers], url: url(segs, opts.query) };
  if (opts.formdata) {
    req.body = { mode: "formdata", formdata: opts.formdata };
  } else if (body !== undefined && body !== null) {
    req.header.push({ key: "Content-Type", value: "application/json" });
    req.body = { mode: "raw", raw: JSON.stringify(body) };
  }
  const pre = opts.wait ? [EVAL, `scw.waitFirst(${opts.wait});`] : null;
  const test = [EVAL, ...`scw.run(${JSON.stringify({ name, ...spec }, null, 2)});`.split("\n")];
  const id = `scw-${String(++itemCounter).padStart(3, "0")}`;
  return item(id, name, req, events(pre, test));
}

const route = (r) => (Array.isArray(r) ? r : ROUTES[r]);
const infer = (name, r, body, headers, spec, opts) => call(name, "POST", route(r), body, headers, { status: 200, ...spec }, opts);
// A request that writes: a plain keyed request that must miss.
const write = (name, r, body, headers, spec = {}, opts) => infer(name, r, body, headers, { outcome: "miss", ...spec }, opts);
// A read that must hit: polled, and no-store so a miss can never satisfy a later attempt.
const hit = (name, r, body, headers, spec = {}, opts) => infer(name, r, body, [...headers, H.noStore()], { outcome: "direct", poll: true, ...spec }, opts);
// A read that must miss: no-store, so the isolation probe leaves nothing behind.
const miss = (name, r, body, headers, spec = {}, opts) => infer(name, r, body, [...headers, H.noStore()], { outcome: "miss", ...spec }, opts);

const api = (name, method, segs, body, spec, opts) => call(name, method, segs, body, [], spec, opts);
const PLUGIN = ["api", "plugins", "semantic_cache"];
const putPlugin = (name, config, spec = {}) =>
  api(name, "PUT", PLUGIN, { enabled: true, config }, { status: 200, fields: [{ path: "plugin.status.status", eq: "active" }], ...spec });
const getPlugin = (name, fields, spec = {}) => api(name, "GET", PLUGIN, null, { status: 200, fields, ...spec });
const clearId = (name, cid, spec = {}) =>
  api(name, "DELETE", ["api", "cache", "clear", `{{cid_${cid}}}`], null, { status: 200, fields: [{ path: "message", eq: "Cache cleared successfully" }], ...spec });
const clearKey = (name, keySeg, spec = {}) =>
  api(name, "DELETE", ["api", "cache", "clear-by-key", keySeg], null, { status: 200, fields: [{ path: "message", eq: "Cache cleared successfully" }], ...spec });
const logRow = (name, rid, spec) => api(name, "GET", ["api", "logs", `{{rid_${rid}}}`], null, { status: 200, poll: true, source: "log", ...spec });

const folder = (name, items) => ({ name, item: items.flat() });

// --------------------------------------------------------------------------- //
// P: preconditions and providers
// --------------------------------------------------------------------------- //

function folderP() {
  const items = [
    api("P1 health pings the vector store", "GET", ["health"], null, {
      status: 200,
      fields: [
        { path: "status", eq: "ok" },
        { path: "components.db_pings", eq: "ok" },
      ],
    }),
    api("P2 config reports the cache store connected", "GET", ["api", "config"], null, { status: 200, fields: [{ path: "is_cache_connected", eq: true }] }),
    api("P3 a fresh server has no semantic_cache plugin", "GET", PLUGIN, null, { status: 404 }),
  ];
  for (const p of STANDARD_PROVIDERS) {
    items.push(api(`P4 create provider ${p.name}`, "POST", ["api", "providers"], { provider: p.name }, { status: [200, 201] }));
    items.push(
      api(`P4 add key to ${p.name}`, "POST", ["api", "providers", p.name, "keys"], { name: `scw-${p.name}-{{run_id}}`, value: `env.${p.env}`, models: ["*"], weight: 1 }, { status: [200, 201] })
    );
  }
  items.push(api("P4 create provider bedrock", "POST", ["api", "providers"], { provider: "bedrock" }, { status: [200, 201] }));
  items.push(api("P4 add key to bedrock", "POST", ["api", "providers", "bedrock", "keys"], BEDROCK_KEY, { status: [200, 201] }));
  for (const p of CUSTOM_PROVIDERS) {
    items.push(
      api(`P4 create provider ${p.name}`, "POST", ["api", "providers"], { provider: p.name, custom_provider_config: { base_provider_type: "openai", is_key_less: false } }, { status: [200, 201] })
    );
    items.push(api(`P4 add key to ${p.name}`, "POST", ["api", "providers", p.name, "keys"], { name: `${p.name}-key`, value: p.value, models: ["*"], weight: 1 }, { status: [200, 201] }));
  }
  const names = [...STANDARD_PROVIDERS.map((p) => p.name), "bedrock", ...CUSTOM_PROVIDERS.map((p) => p.name)];
  items.push(api("P4 providers are listed", "GET", ["api", "providers"], null, { status: 200, fields: names.map((n) => ({ path: "providers", containsName: n })) }));
  return folder("P: preconditions and providers", items);
}

// --------------------------------------------------------------------------- //
// M: plugin management
// --------------------------------------------------------------------------- //

function folderM() {
  const p = PAIRS[0];
  const notLoaded = { status: 400, message: "semantic_cache plugin is not loaded" };
  const invalid = (id, label, config, message) => [
    api(`${id} rejects ${label}`, "POST", ["api", "plugins"], { name: "semantic_cache", enabled: true, config }, { status: 400, ...(message ? { message } : {}) }),
    api(`${id} leaves no plugin behind`, "GET", PLUGIN, null, { status: 404 }),
    // A create that was wrongly accepted is removed so the next row still starts from nothing.
    api(`${id} cleanup`, "DELETE", PLUGIN, null, { status: [200, 404] }),
  ];
  return folder("M: plugin management", [
    api("M1 create direct-only plugin", "POST", ["api", "plugins"], { name: "semantic_cache", enabled: true, config: DIRECT() }, {
      status: 201,
      fields: [
        { path: "plugin.enabled", eq: true },
        { path: "plugin.status.status", eq: "active" },
        { path: "plugin.status.types", contains: "llm" },
      ],
    }),
    api("M2 creating it again conflicts", "POST", ["api", "plugins"], { name: "semantic_cache", enabled: true, config: DIRECT() }, { status: 409, message: "Plugin already exists" }),
    getPlugin("M3 config is echoed", [
      { path: "config.dimension", eq: 1 },
      { path: "config.ttl", eq: "10m" },
      { path: "config.vector_store_namespace", eq: NS.direct },
      { path: "config.disable_identity_scoping", absentOrFalse: true },
    ]),
    api("M4 listed in /api/plugins", "GET", ["api", "plugins"], null, { status: 200, fields: [{ path: "plugins", containsName: "semantic_cache" }] }),
    api("M4 listed in /api/plugins/loaded", "GET", ["api", "plugins", "loaded"], null, { status: 200, fields: [{ path: "plugins", contains: "semantic_cache" }] }),
    api("M4 listed in /api/plugins/builtins", "GET", ["api", "plugins", "builtins"], null, { status: 200, fields: [{ path: "plugins", contains: "semantic_cache" }] }),
    api("M5 PUT with only ttl", "PUT", PLUGIN, { enabled: true, config: { ttl: "9m" } }, { status: 200 }),
    getPlugin("M5 ttl changes and the rest is kept", [
      { path: "config.ttl", eq: "9m" },
      { path: "config.dimension", eq: 1 },
      { path: "config.vector_store_namespace", eq: NS.direct },
    ]),
    putPlugin("M5 restore baseline", DIRECT()),
    write("M6 write an entry", "chat", chat("M6: reply with one word: ready."), [H.key("m6")], { save: "m6" }),
    hit("M6 the entry hits", "chat", chat("M6: reply with one word: ready."), [H.key("m6")], { same: "m6" }),
    api("M6 disable the plugin", "PUT", PLUGIN, { enabled: false, config: DIRECT() }, { status: 200, fields: [{ path: "plugin.enabled", eq: false }] }),
    getPlugin("M6 status is disabled", [
      { path: "enabled", eq: false },
      { path: "status.status", eq: "disabled" },
    ]),
    infer("M6 keyed inference is not cached", "chat", chat("M6: reply with one word: ready."), [H.key("m6"), H.noStore()], { outcome: "none" }),
    api("M6 clear by id is refused", "DELETE", ["api", "cache", "clear", "{{cid_m6}}"], null, notLoaded),
    api("M6 clear by key is refused", "DELETE", ["api", "cache", "clear-by-key", "m6-{{run_id}}"], null, notLoaded),
    putPlugin("M7 enable the plugin", DIRECT()),
    hit("M7 the entry written before the disable hits", "chat", chat("M6: reply with one word: ready."), [H.key("m6")], { same: "m6" }),
    api("M8 delete the plugin", "DELETE", PLUGIN, null, { status: 200 }),
    api("M8 the plugin is gone", "GET", PLUGIN, null, { status: 404 }),
    infer("M8 keyed inference is not cached", "chat", chat("M6: reply with one word: ready."), [H.key("m6"), H.noStore()], { outcome: "none" }),
    api("M8 clear by id is refused", "DELETE", ["api", "cache", "clear", "{{cid_m6}}"], null, notLoaded),
    api("M8 clear by key is refused", "DELETE", ["api", "cache", "clear-by-key", "m6-{{run_id}}"], null, notLoaded),
    api("M8 deleting again is a 404", "DELETE", PLUGIN, null, { status: 404 }),
    invalid("M10", "an empty config", {}, "requires 'provider' for semantic mode"),
    invalid("M11", "dimension 0", { dimension: 0, vector_store_namespace: "ScwM11{{ns}}" }, "'dimension' must be >= 1"),
    invalid("M12", "a provider with dimension 1", { provider: EMBED, dimension: 1, vector_store_namespace: "ScwM12{{ns}}" }, "requires 'dimension' > 1 when 'provider' is set"),
    invalid("M13", "a provider without embedding_model", { provider: EMBED, dimension: 1536, vector_store_namespace: "ScwM13{{ns}}" }, "requires 'embedding_model'"),
    invalid(
      "M14",
      "an empty embedding_model",
      { provider: EMBED, embedding_model: "", dimension: 1536, vector_store_namespace: "ScwM14{{ns}}" },
      "non-empty 'embedding_model'"
    ),
    invalid(
      "M15",
      "a provider that is not configured",
      { provider: "scw-missing-{{run_id}}", embedding_model: EMBED_MODEL, dimension: 1536, vector_store_namespace: "ScwM15{{ns}}" },
      "failed to get provider config"
    ),
    invalid("M16", "ttl abc", { dimension: 1, ttl: "abc", vector_store_namespace: "ScwM16{{ns}}" }),
    invalid("M17", "a negative ttl", { dimension: 1, ttl: "-5s", vector_store_namespace: "ScwM17{{ns}}" }, "TTL must be non-negative"),
    api("M9 re-create the plugin", "POST", ["api", "plugins"], { name: "semantic_cache", enabled: true, config: DIRECT() }, {
      status: 201,
      fields: [{ path: "plugin.status.status", eq: "active" }],
    }),
    hit("M9 the entry written before the delete hits", "chat", chat("M6: reply with one word: ready."), [H.key("m6")], { same: "m6" }),
    putPlugin("M18 semantic plugin at dimension 1536", SEMANTIC({ vector_store_namespace: NS.dim })),
    write("M18 write an entry", "chat", chat(p.canonical), [H.key("m18")], { save: "m18", embedding: true }),
    hit("M18 the entry hits", "chat", chat(p.canonical), [H.key("m18")], { same: "m18" }),
    api("M18 PUT dimension 3072 on the 1536 namespace", "PUT", PLUGIN, { enabled: true, config: SEMANTIC({ vector_store_namespace: NS.dim, dimension: 3072 }) }, {
      status: 400,
      message: "already exists with dimension 1536 but config requires 3072",
    }),
    getPlugin("M18 the saved config is restored", [
      { path: "config.dimension", eq: 1536 },
      { path: "status.status", eq: "active" },
    ]),
    hit("M18 the entry still hits", "chat", chat(p.canonical), [H.key("m18")], { same: "m18" }),
    api("M18 delete the plugin", "DELETE", PLUGIN, null, { status: 200 }),
    api("M18 POST dimension 3072 on the 1536 namespace", "POST", ["api", "plugins"], { name: "semantic_cache", enabled: true, config: SEMANTIC({ vector_store_namespace: NS.dim, dimension: 3072 }) }, {
      status: 400,
      message: "already exists with dimension 1536 but config requires 3072",
    }),
    api("M18 the failed create leaves no plugin", "GET", PLUGIN, null, { status: 404 }),
    putPlugin("M18 recover on a fresh namespace", SEMANTIC({ vector_store_namespace: NS.dimFresh, dimension: 3072 })),
    putPlugin("M19 disable identity scoping", DIRECT({ disable_identity_scoping: true })),
    getPlugin("M19 GET echoes true", [{ path: "config.disable_identity_scoping", eq: true }]),
    putPlugin("M19 enable identity scoping", DIRECT({ disable_identity_scoping: false })),
    getPlugin("M19 GET echoes false", [{ path: "config.disable_identity_scoping", eq: false }]),
    putPlugin("M20 switch to direct", DIRECT({ vector_store_namespace: NS.m20direct })),
    getPlugin("M20 direct mode", [
      { path: "config.provider", absentOrFalse: true },
      { path: "config.dimension", eq: 1 },
    ]),
    write("M20 direct mode caches: write", "chat", chat("M20 direct: reply with one word: ready."), [H.key("m20")], { save: "m20a" }),
    hit("M20 direct mode caches: read", "chat", chat("M20 direct: reply with one word: ready."), [H.key("m20")], { same: "m20a" }),
    putPlugin("M20 switch to semantic", SEMANTIC({ vector_store_namespace: NS.m20semantic })),
    getPlugin("M20 semantic mode", [
      { path: "config.provider", eq: EMBED },
      { path: "config.dimension", eq: 1536 },
    ]),
    write("M20 semantic mode caches: write", "chat", chat(PAIRS[1].canonical), [H.key("m20")], { save: "m20b", embedding: true }),
    hit("M20 semantic mode caches: read", "chat", chat(PAIRS[1].canonical), [H.key("m20")], { same: "m20b" }),
    putPlugin("M20 switch back to direct", DIRECT({ vector_store_namespace: NS.m20direct })),
    write("M20 direct again caches: write", "chat", chat("M20 direct again: reply with one word: ready."), [H.key("m20")], { save: "m20c" }),
    hit("M20 direct again caches: read", "chat", chat("M20 direct again: reply with one word: ready."), [H.key("m20")], { same: "m20c" }),
    putPlugin("M cleanup: direct baseline", DIRECT()),
  ]);
}

// --------------------------------------------------------------------------- //
// D: direct cache on chat completions
// --------------------------------------------------------------------------- //

function folderD() {
  const d1 = chat("D1: reply with one word: ready.");
  const params = [
    ["temperature", { temperature: 0.7 }],
    ["top_p", { top_p: 0.5 }],
    ["max_tokens", { max_tokens: 9 }],
    ["stop", { stop: ["\n\n"] }],
    ["presence_penalty", { presence_penalty: 0.5 }],
    ["response_format", { response_format: { type: "text" } }],
    ["user", { user: "scw-user-1" }],
    ["top_logprobs", { top_logprobs: 2 }],
    ["service_tier", { service_tier: "default" }],
    ["store", { store: false }],
    ["prompt_cache_key", { prompt_cache_key: "scw-pck-1" }],
  ];
  // logprobs stays on in every D17 request so top_logprobs can be added on its own.
  const d17 = (extra = {}) => chat("D17: reply with one word: ready.", { logprobs: true, ...extra });
  const d17Items = [write("D17 base write", "chat", d17(), [H.key("d17")], { save: "d17" }), hit("D17 base hits", "chat", d17(), [H.key("d17")], { same: "d17" })];
  const seen = ["d17"];
  for (const [label, extra] of params) {
    const v = `d17_${label}`;
    d17Items.push(miss(`D17 ${label} changed misses`, "chat", d17(extra), [H.key("d17")], { distinct: [...seen], save: v }));
    seen.push(v);
  }
  const long = "D15: summarize in one word. " + "The quick brown fox jumps over the lazy dog near the riverbank. ".repeat(160);
  const four = [
    { role: "user", content: "My name is Ana." },
    { role: "assistant", content: "Nice to meet you, Ana." },
    { role: "user", content: "I live in Lisbon." },
    { role: "user", content: "Reply with one word: ok." },
  ];
  const three = four.slice(1);
  const tools = (list, choice = "none") => chat("D19: what is the weather in Paris?", { tools: list, tool_choice: choice });
  const forced = (extra = {}) => chat("What is the weather in Paris right now?", { tools: [WEATHER], tool_choice: FORCE_WEATHER, max_tokens: 20, ...extra });

  return folder("D: direct cache on chat completions", [
    putPlugin("D baseline", DIRECT()),
    write("D1 A misses with a cache_id", "chat", d1, [H.key("d1")], {
      save: "d1",
      saveCd: "d1a",
      saveReq: "d1a",
      fields: [
        { path: "choices.0.message.content", save: "d1content" },
        { path: "model", save: "d1model" },
        { path: "usage", save: "d1usage" },
      ],
    }),
    hit("D1 B is a direct hit and D25 replays A", "chat", d1, [H.key("d1")], {
      same: "d1",
      saveCd: "d1b",
      saveReq: "d1b",
      fields: [
        { path: "choices.0.message.content", equals: "d1content" },
        { path: "model", equals: "d1model" },
        { path: "usage", equals: "d1usage" },
      ],
    }),
    miss("D2 a second cache key misses", "chat", d1, [H.key("d2")], { distinct: ["d1"] }),
    putPlugin("D3 set default_cache_key", DIRECT({ default_cache_key: "dflt-{{run_id}}" })),
    write("D3 no header writes under the default key", "chat", chat("D3: reply with one word: ready."), [], { save: "d3" }),
    hit("D3 no header hits under the default key", "chat", chat("D3: reply with one word: ready."), [], { same: "d3" }),
    miss("D3 an explicit key lands in its own bucket", "chat", chat("D3: reply with one word: ready."), [H.key("d3")], { distinct: ["d3"] }),
    putPlugin("D3 restore baseline", DIRECT()),
    infer("D4 no key and no default: A not cached", "chat", chat("D4: reply with one word: ready."), [], { outcome: "none" }),
    infer("D4 no key and no default: B not cached", "chat", chat("D4: reply with one word: ready."), [], { outcome: "none" }),
    write("D5 write on gpt-4o-mini", "chat", chat("D5: reply with one word: ready."), [H.key("d5")], { save: "d5" }),
    hit("D5 gpt-4o-mini hits", "chat", chat("D5: reply with one word: ready."), [H.key("d5")], { same: "d5" }),
    miss("D5 gpt-4o misses", "chat", chat("D5: reply with one word: ready.", { model: MODEL_ALT }), [H.key("d5")], { distinct: ["d5"] }),
    putPlugin("D6 cache_by_model false", DIRECT({ cache_by_model: false })),
    write("D6 write on gpt-4o-mini", "chat", chat("D6: reply with one word: ready."), [H.key("d6")], { save: "d6" }),
    hit("D6 gpt-4o-mini hits", "chat", chat("D6: reply with one word: ready."), [H.key("d6")], { same: "d6" }),
    hit("D6 gpt-4o hits the gpt-4o-mini entry", "chat", chat("D6: reply with one word: ready.", { model: MODEL_ALT }), [H.key("d6")], { same: "d6" }),
    putPlugin("D6 restore baseline", DIRECT()),
    write("D7 write on openai", "chat", chat("D7: reply with one word: ready."), [H.key("d7")], { save: "d7" }),
    hit("D7 openai hits", "chat", chat("D7: reply with one word: ready."), [H.key("d7")], { same: "d7" }),
    miss("D7 another provider misses", "chat", chat("D7: reply with one word: ready.", { model: `${ALT}/gpt-4o-mini` }), [H.key("d7")], { distinct: ["d7"] }),
    putPlugin("D8 cache_by_provider false", DIRECT({ cache_by_provider: false })),
    write("D8 write on openai", "chat", chat("D8: reply with one word: ready."), [H.key("d8")], { save: "d8" }),
    hit("D8 openai hits", "chat", chat("D8: reply with one word: ready."), [H.key("d8")], { same: "d8" }),
    hit("D8 another provider hits the openai entry", "chat", chat("D8: reply with one word: ready.", { model: `${ALT}/gpt-4o-mini` }), [H.key("d8")], { same: "d8" }),
    putPlugin("D8 restore baseline", DIRECT()),
    write("D9 write with one system prompt", "chat", chatSystem("You are terse.", "D9: reply with one word: ready."), [H.key("d9")], { save: "d9" }),
    hit("D9 same system prompt hits", "chat", chatSystem("You are terse.", "D9: reply with one word: ready."), [H.key("d9")], { same: "d9" }),
    miss("D9 another system prompt misses", "chat", chatSystem("You are verbose.", "D9: reply with one word: ready."), [H.key("d9")], { distinct: ["d9"] }),
    putPlugin("D10 exclude_system_prompt true", DIRECT({ exclude_system_prompt: true })),
    write("D10 write with one system prompt", "chat", chatSystem("You are terse.", "D10: reply with one word: ready."), [H.key("d10")], { save: "d10" }),
    hit("D10 same system prompt hits", "chat", chatSystem("You are terse.", "D10: reply with one word: ready."), [H.key("d10")], { same: "d10" }),
    hit("D10 another system prompt hits", "chat", chatSystem("You are verbose.", "D10: reply with one word: ready."), [H.key("d10")], { same: "d10" }),
    putPlugin("D10 restore baseline", DIRECT()),
    infer("D11 four messages: A not cached", "chat", chatMessages(four), [H.key("d11")], { outcome: "none" }),
    infer("D11 four messages: B not cached", "chat", chatMessages(four), [H.key("d11")], { outcome: "none" }),
    write("D11 three messages write", "chat", chatMessages(three), [H.key("d11")], { save: "d11" }),
    hit("D11 three messages hit", "chat", chatMessages(three), [H.key("d11")], { same: "d11" }),
    putPlugin("D12 conversation_history_threshold 1", DIRECT({ conversation_history_threshold: 1 })),
    infer("D12 two messages: A not cached", "chat", chatMessages(four.slice(2)), [H.key("d12")], { outcome: "none" }),
    infer("D12 two messages: B not cached", "chat", chatMessages(four.slice(2)), [H.key("d12")], { outcome: "none" }),
    write("D12 one message write", "chat", chat("D12: reply with one word: ready."), [H.key("d12")], { save: "d12" }),
    hit("D12 one message hits", "chat", chat("D12: reply with one word: ready."), [H.key("d12")], { same: "d12" }),
    putPlugin("D12 restore baseline", DIRECT()),
    write("D13 write mixed case", "chat", chat("D13: What Is The Capital Of Spain?"), [H.key("d13")], { save: "d13" }),
    hit("D13 lowercase hits", "chat", chat("d13: what is the capital of spain?"), [H.key("d13")], { same: "d13" }),
    hit("D13 surrounding whitespace hits", "chat", chat("   D13: What Is The Capital Of Spain?   "), [H.key("d13")], { same: "d13" }),
    write("D14 unicode write", "chat", chat("D14: 日本の首都はどこですか？ 🗼 Ünïcödé"), [H.key("d14")], { save: "d14" }),
    hit("D14 unicode hits", "chat", chat("D14: 日本の首都はどこですか？ 🗼 Ünïcödé"), [H.key("d14")], { same: "d14" }),
    write("D15 10 KB prompt write", "chat", chat(long), [H.key("d15")], { save: "d15" }),
    hit("D15 10 KB prompt hits", "chat", chat(long), [H.key("d15")], { same: "d15" }),
    write("D16 image attachment write", "chat", withImage("D16: what color is this image? One word.", RED_PNG), [H.key("d16")], { save: "d16" }),
    hit("D16 same image hits", "chat", withImage("D16: what color is this image? One word.", RED_PNG), [H.key("d16")], { same: "d16" }),
    miss("D16 another image misses", "chat", withImage("D16: what color is this image? One word.", BLUE_PNG), [H.key("d16")], { distinct: ["d16"] }),
    d17Items,
    write("D18 write with seed 7", "chat", chat("D18: reply with one word: ready.", { seed: 7 }), [H.key("d18")], { save: "d18" }),
    hit("D18 seed 7 hits", "chat", chat("D18: reply with one word: ready.", { seed: 7 }), [H.key("d18")], { same: "d18" }),
    miss("D18 seed 8 misses", "chat", chat("D18: reply with one word: ready.", { seed: 8 }), [H.key("d18")], { distinct: ["d18"] }),
    write("D19 write with two tools", "chat", tools([WEATHER, TIME]), [H.key("d19")], { save: "d19" }),
    hit("D19 tools reordered hit", "chat", tools([TIME, WEATHER]), [H.key("d19")], { same: "d19" }),
    miss("D19 a renamed tool misses", "chat", tools([FORECAST, TIME]), [H.key("d19")], { distinct: ["d19"] }),
    miss("D19 another tool_choice misses", "chat", tools([WEATHER, TIME], "auto"), [H.key("d19")], { distinct: ["d19"] }),
    write("D20 forced tool call: A misses", "chat", forced(), [H.key("d20")], { save: "d20" }),
    infer("D20 forced tool call: B still misses, nothing was written", "chat", forced(), [H.key("d20")], { outcome: "miss", same: "d20" }, { wait: 2 }),
    putPlugin("D20 cache_tool_call_responses true", DIRECT({ cache_tool_call_responses: true })),
    write("D20 forced tool call is written", "chat", forced(), [H.key("d20b")], {
      save: "d20b",
      fields: [{ path: "choices.0.message.tool_calls.0.function", save: "d20tool" }],
    }),
    hit("D20 forced tool call replays its tool_calls", "chat", forced(), [H.key("d20b")], {
      same: "d20b",
      fields: [{ path: "choices.0.message.tool_calls.0.function", equals: "d20tool" }],
    }),
    putPlugin("D20 restore baseline", DIRECT()),
    write("D21 stream write", "chat", chatStream("D21: count to three."), [H.key("d21")], { save: "d21" }),
    hit("D21 stream hits", "chat", chatStream("D21: count to three."), [H.key("d21")], { same: "d21" }),
    miss("D21 the non-stream request misses", "chat", chat("D21: count to three.", { max_tokens: 12 }), [H.key("d21")], { distinct: ["d21"] }),
    write("D22 chat completions write", "chat", chat("D22: reply with one word: ready."), [H.key("d22")], { save: "d22" }),
    hit("D22 chat completions hits", "chat", chat("D22: reply with one word: ready."), [H.key("d22")], { same: "d22" }),
    miss("D22 the same text on /v1/responses misses", "responses", responses("D22: reply with one word: ready."), [H.key("d22")], { distinct: ["d22"] }),
    infer("D23 unknown model: A is an error", "chat", chat("D23: reply.", { model: "openai/gpt-scw-does-not-exist" }), [H.key("d23")], { status: "4xx", outcome: "none" }),
    infer("D23 unknown model: B is still an error", "chat", chat("D23: reply.", { model: "openai/gpt-scw-does-not-exist" }), [H.key("d23")], { status: "4xx", outcome: "none" }, { wait: 2 }),
    infer("D24 input_tokens is not cached", "inputTokens", { model: MODEL, input: "D24: hello there" }, [H.key("d24")], { outcome: "none" }),
    putPlugin("D cleanup: direct baseline", DIRECT()),
  ]);
}

// --------------------------------------------------------------------------- //
// X: every other request type, and the OpenAI and Anthropic SDK routes
// --------------------------------------------------------------------------- //

function folderX() {
  const transcribe = (language) => [
    { key: "file", type: "file", src: "fixtures/sample.mp3" },
    { key: "model", value: "openai/whisper-1", type: "text" },
    { key: "language", value: language, type: "text" },
  ];
  const speech = { model: "openai/tts-1", input: "Semantic cache wiring.", voice: "alloy" };
  const anthropicBody = { model: MODEL, max_tokens: 8, temperature: 0, messages: [{ role: "user", content: "X12: reply with one word: ready." }] };
  return folder("X: every other request type", [
    putPlugin("X baseline", DIRECT()),
    write("X1 text completion write", "text", text("X1: The capital of France is"), [H.key("x1")], { save: "x1", fields: [{ path: "choices.0.text", save: "x1text" }] }),
    hit("X1 text completion hits with the same text", "text", text("X1: The capital of France is"), [H.key("x1")], { same: "x1", fields: [{ path: "choices.0.text", equals: "x1text" }] }),
    write("X2 and T5 text completion stream write", "text", text("X2: Count to three:", { stream: true }), [H.key("x2")], {
      save: "x2",
      stream: { done: true, minChunks: 2, finalOnly: true, save: "x2" },
    }),
    hit("X2 and T5 text completion stream replays", "text", text("X2: Count to three:", { stream: true }), [H.key("x2")], {
      same: "x2",
      stream: { done: true, finalOnly: true, compare: "x2" },
    }),
    write("X3 responses write", "responses", responses("X3: reply with one word: ready."), [H.key("x3")], { save: "x3", fields: [{ path: "output.0.content.0.text", save: "x3text" }] }),
    hit("X3 responses hits with the same output", "responses", responses("X3: reply with one word: ready."), [H.key("x3")], {
      same: "x3",
      fields: [{ path: "output.0.content.0.text", equals: "x3text" }],
    }),
    write("X4 and T4 responses stream write", "responses", responses("X4: count to three.", { stream: true }), [H.key("x4")], {
      save: "x4",
      stream: { minChunks: 2, finalOnly: true, save: "x4" },
    }),
    hit("X4 and T4 responses stream replays the same events", "responses", responses("X4: count to three.", { stream: true }), [H.key("x4")], {
      same: "x4",
      stream: { finalOnly: true, compare: "x4" },
    }),
    infer("X5 seed a response to chain from", "responses", responses("X5 seed: reply with one word: ready."), [], { fields: [{ path: "id", saveAs: "x5_prev", nonEmpty: true }] }),
    write("X5 write without previous_response_id", "responses", responses("X5: reply with one word: ready."), [H.key("x5")], { save: "x5" }),
    hit("X5 the same request hits", "responses", responses("X5: reply with one word: ready."), [H.key("x5")], { same: "x5" }),
    miss("X5 a previous_response_id misses", "responses", responses("X5: reply with one word: ready.", { previous_response_id: "{{x5_prev}}" }), [H.key("x5")], { distinct: ["x5"] }),
    write("X6 embeddings write", "embed", embedding("X6: semantic cache wiring"), [H.key("x6")], { save: "x6", fingerprint: [{ path: "data.0.embedding", save: "x6" }] }),
    hit("X6 embeddings hit with the same vector", "embed", embedding("X6: semantic cache wiring"), [H.key("x6")], { same: "x6", fingerprint: [{ path: "data.0.embedding", equals: "x6" }] }),
    write("X7 embeddings write", "embed", embedding("X7: semantic cache wiring"), [H.key("x7")], { save: "x7" }),
    hit("X7 embeddings hit", "embed", embedding("X7: semantic cache wiring"), [H.key("x7")], { same: "x7" }),
    miss("X7 dimensions changed misses", "embed", embedding("X7: semantic cache wiring", { dimensions: 256 }), [H.key("x7")], { distinct: ["x7"] }),
    miss("X7 encoding_format changed misses", "embed", embedding("X7: semantic cache wiring", { encoding_format: "base64" }), [H.key("x7")], { distinct: ["x7"] }),
    write("X8 speech write", "speech", speech, [H.key("x8")], { source: "headers", contentType: "audio/mpeg", save: "x8", saveReq: "x8a", fingerprint: [{ save: "x8" }] }),
    hit("X8 speech hits with the same audio", "speech", speech, [H.key("x8")], {
      source: "headers",
      contentType: "audio/mpeg",
      same: "x8",
      saveReq: "x8b",
      fingerprint: [{ equals: "x8" }],
    }),
    logRow("X8 the write's log row is a miss", "x8a", { outcome: "miss", same: "x8" }),
    logRow("X8 the read's log row is a direct hit", "x8b", { outcome: "direct", same: "x8" }),
    infer("X9 transcription write", "transcribe", null, [H.key("x9")], { outcome: "miss", save: "x9", fields: [{ path: "text", save: "x9text" }] }, { formdata: transcribe("en") }),
    infer("X9 transcription hits with the same text", "transcribe", null, [H.key("x9"), H.noStore()], { outcome: "direct", poll: true, same: "x9", fields: [{ path: "text", equals: "x9text" }] }, { formdata: transcribe("en") }),
    infer("X9 another language misses", "transcribe", null, [H.key("x9"), H.noStore()], { outcome: "miss", distinct: ["x9"] }, { formdata: transcribe("fr") }),
    write("X10 image generation write", "image", image(IMAGE_PAIR.canonical), [H.key("x10")], { save: "x10", fingerprint: [{ path: "data.0.b64_json", save: "x10" }] }),
    hit("X10 image generation hits with the same image", "image", image(IMAGE_PAIR.canonical), [H.key("x10")], { same: "x10", fingerprint: [{ path: "data.0.b64_json", equals: "x10" }] }),
    write("X11 OpenAI SDK route write", "openai", chat("X11: reply with one word: ready.", { model: "gpt-4o-mini" }), [H.key("x11")], { save: "x11" }),
    hit("X11 OpenAI SDK route hits", "openai", chat("X11: reply with one word: ready.", { model: "gpt-4o-mini" }), [H.key("x11")], { same: "x11" }),
    write("X12 Anthropic SDK route write", "anthropic", anthropicBody, [H.key("x12"), H.anthropicVersion()], { source: "headers", noBodyCacheDebug: true, save: "x12", saveReq: "x12a" }),
    hit("X12 Anthropic SDK route hits", "anthropic", anthropicBody, [H.key("x12"), H.anthropicVersion()], { source: "headers", noBodyCacheDebug: true, same: "x12", saveReq: "x12b" }),
    logRow("X12 the write's log row is a miss", "x12a", { outcome: "miss", same: "x12" }),
    logRow("X12 the read's log row is a direct hit", "x12b", { outcome: "direct", same: "x12" }),
    putPlugin("X cleanup: direct baseline", DIRECT()),
  ]);
}

// --------------------------------------------------------------------------- //
// T: streams
// --------------------------------------------------------------------------- //

function folderT() {
  const forced = () => chat("What is the weather in Paris right now?", { tools: [WEATHER], tool_choice: FORCE_WEATHER, max_tokens: 20, stream: true });
  return folder("T: streams", [
    putPlugin("T baseline", DIRECT()),
    write("T1 chat stream write", "chat", chatStream("T1: count to three."), [H.key("t1")], {
      save: "t1",
      stream: { done: true, minChunks: 2, finalOnly: true, save: "t1" },
    }),
    hit("T2 chat stream replays chunk by chunk", "chat", chatStream("T1: count to three."), [H.key("t1")], {
      same: "t1",
      saveReq: "t2",
      stream: { done: true, finalOnly: true, compare: "t1" },
    }),
    infer("T3 no-store stream misses", "chat", chatStream("T3: count to three."), [H.key("t3"), H.noStore()], { outcome: "miss", save: "t3" }),
    infer("T3 the repeat still misses", "chat", chatStream("T3: count to three."), [H.key("t3"), H.noStore()], { outcome: "miss", same: "t3" }, { wait: 2 }),
    write("T6 streamed forced tool call: A misses", "chat", forced(), [H.key("t6")], { save: "t6" }),
    infer("T6 streamed forced tool call: B still misses", "chat", forced(), [H.key("t6")], { outcome: "miss", same: "t6" }, { wait: 2 }),
    putPlugin("T6 cache_tool_call_responses true", DIRECT({ cache_tool_call_responses: true })),
    write("T6 streamed forced tool call is written", "chat", forced(), [H.key("t6b")], { save: "t6b", stream: { finalOnly: true, save: "t6b" } }),
    hit("T6 streamed forced tool call replays its tool_calls deltas", "chat", forced(), [H.key("t6b")], { same: "t6b", stream: { finalOnly: true, compare: "t6b" } }),
    putPlugin("T6 restore baseline", DIRECT()),
    logRow("T7 the stream hit's log row", "t2", { outcome: "direct", same: "t1", fields: [{ path: "status", eq: "success" }] }),
  ]);
}

// --------------------------------------------------------------------------- //
// H: per-request headers
// --------------------------------------------------------------------------- //

function folderH() {
  const body = (id) => chat(`${id}: reply with one word: ready.`);
  const ttlRows = [
    ["H1", "3s", "miss"],
    ["H2", "3", "miss"],
    ["H3", "abc", "direct"],
    ["H4", "0", "direct"],
  ];
  const items = [putPlugin("H baseline", DIRECT())];
  for (const [id, ttl] of ttlRows) {
    const v = id.toLowerCase();
    items.push(write(`${id} write with x-bf-cache-ttl ${ttl}`, "chat", body(id), [H.key(v), H.ttl(ttl)], { save: v }));
    items.push(hit(`${id} hits at once`, "chat", body(id), [H.key(v)], { same: v }));
  }
  ttlRows.forEach(([id, ttl, after], i) => {
    const v = id.toLowerCase();
    const label = after === "miss" ? `${id} ttl ${ttl} has expired` : `${id} ttl ${ttl} falls back to the plugin ttl`;
    const spec = after === "miss" ? { same: v } : { same: v, poll: false };
    const fnFor = after === "miss" ? miss : hit;
    items.push(fnFor(label, "chat", body(id), [H.key(v)], spec, i === 0 ? { wait: 5 } : {}));
  });
  const casing = (id, value, spec, opts) => infer(`${id} no-store ${value}`, "chat", body("H7"), [H.key("h7"), H.noStore(value)], { outcome: "miss", ...spec }, opts);
  items.push(
    putPlugin("H5 plugin ttl 5s", DIRECT({ ttl: "5s" })),
    write("H5 write", "chat", body("H5"), [H.key("h5")], { save: "h5" }),
    hit("H5 hits at once", "chat", body("H5"), [H.key("h5")], { same: "h5" }),
    miss("H5 the plugin ttl has expired", "chat", body("H5"), [H.key("h5")], { same: "h5" }, { wait: 7 }),
    putPlugin("H5 restore baseline", DIRECT()),
    infer("H6 no-store A misses", "chat", body("H6"), [H.key("h6"), H.noStore()], { outcome: "miss", save: "h6" }),
    infer("H6 no-store B still misses", "chat", body("H6"), [H.key("h6"), H.noStore()], { outcome: "miss", same: "h6" }, { wait: 2 }),
    write("H6 a plain request writes", "chat", body("H6"), [H.key("h6")], { same: "h6" }),
    hit("H6 a no-store read hits", "chat", body("H6"), [H.key("h6")], { same: "h6" }),
    casing("H7", "TRUE", { save: "h7" }),
    casing("H7", "True", { same: "h7" }, { wait: 2 }),
    casing("H7", "true", { same: "h7" }, { wait: 2 }),
    infer("H7 no-store false writes", "chat", body("H7 false"), [H.key("h7b"), H.noStore("false")], { outcome: "miss", save: "h7b" }),
    hit("H7 the no-store false write hits", "chat", body("H7 false"), [H.key("h7b")], { same: "h7b" }),
    write("H8 cache type direct writes", "chat", body("H8"), [H.key("h8"), H.type("direct")], { save: "h8" }),
    hit("H8 cache type direct hits", "chat", body("H8"), [H.key("h8"), H.type("direct")], { same: "h8" }),
    infer("H9 cache type semantic in direct-only mode: A skipped", "chat", body("H9"), [H.key("h9"), H.type("semantic")], { outcome: "none" }),
    infer("H9 cache type semantic in direct-only mode: B skipped", "chat", body("H9"), [H.key("h9"), H.type("semantic")], { outcome: "none" }),
    miss("H9 nothing was written", "chat", body("H9"), [H.key("h9")], {}, { wait: 2 }),
    infer("H10 cache type bogus: A skipped", "chat", body("H10"), [H.key("h10"), H.type("bogus")], { outcome: "none" }),
    infer("H10 cache type bogus: B skipped", "chat", body("H10"), [H.key("h10"), H.type("bogus")], { outcome: "none" }),
    miss("H10 nothing was written", "chat", body("H10"), [H.key("h10")], {}, { wait: 2 }),
    write("H11 write", "chat", body("H11"), [H.key("h11")], { save: "h11" }),
    hit("H11 threshold 0 still hits", "chat", body("H11"), [H.key("h11"), H.threshold("0")], { same: "h11" }),
    hit("H11 threshold 0.99 still hits", "chat", body("H11"), [H.key("h11"), H.threshold("0.99")], { same: "h11" }),
    putPlugin("H12 set default_cache_key", DIRECT({ default_cache_key: "hdef-{{run_id}}" })),
    write("H12 the header key writes", "chat", body("H12"), [H.key("h12")], { save: "h12" }),
    hit("H12 the header key hits", "chat", body("H12"), [H.key("h12")], { same: "h12" }),
    miss("H12 the default bucket is untouched", "chat", body("H12"), [], { distinct: ["h12"] }),
    putPlugin("H cleanup: direct baseline", DIRECT())
  );
  return folder("H: per-request headers", items);
}

// --------------------------------------------------------------------------- //
// C: cache clear endpoints
// --------------------------------------------------------------------------- //

function folderC() {
  const body = (s) => chat(`${s}: reply with one word: ready.`);
  const c5Key = "c5 key:with:colons {{run_id}}";
  return folder("C: cache clear endpoints", [
    putPlugin("C baseline", DIRECT()),
    write("C1 write", "chat", body("C1"), [H.key("c1")], { save: "c1" }),
    hit("C1 hits", "chat", body("C1"), [H.key("c1")], { same: "c1" }),
    clearId("C1 clear by cache_id", "c1"),
    miss("C1 the next read misses", "chat", body("C1"), [H.key("c1")], { same: "c1", poll: true }),
    write("C1 rewrite", "chat", body("C1"), [H.key("c1")], { same: "c1" }),
    hit("C1 hits again with the same cache_id", "chat", body("C1"), [H.key("c1")], { same: "c1" }),
    write("C2 first entry under the key", "chat", body("C2 one"), [H.key("c2")], { save: "c2a" }),
    write("C2 second entry under the key", "chat", body("C2 two"), [H.key("c2")], { save: "c2b" }),
    write("C2 entry under another key", "chat", body("C2 one"), [H.key("c2x")], { save: "c2x" }),
    hit("C2 first entry hits", "chat", body("C2 one"), [H.key("c2")], { same: "c2a" }),
    hit("C2 second entry hits", "chat", body("C2 two"), [H.key("c2")], { same: "c2b" }),
    hit("C2 other key hits", "chat", body("C2 one"), [H.key("c2x")], { same: "c2x" }),
    clearKey("C2 clear by key", "c2-{{run_id}}"),
    miss("C2 first entry is gone", "chat", body("C2 one"), [H.key("c2")], { same: "c2a", poll: true }),
    miss("C2 second entry is gone", "chat", body("C2 two"), [H.key("c2")], { same: "c2b", poll: true }),
    hit("C2 the other key still hits", "chat", body("C2 one"), [H.key("c2x")], { same: "c2x" }),
    api("C3 clearing an unknown cache_id succeeds", "DELETE", ["api", "cache", "clear", "00000000-0000-4000-8000-000000000000"], null, {
      status: 200,
      fields: [{ path: "message", eq: "Cache cleared successfully" }],
    }),
    clearKey("C4 clearing a key with no entries succeeds", "c4-empty-{{run_id}}"),
    write("C5 write under a key with spaces and colons", "chat", body("C5"), [H.rawKey(c5Key)], { save: "c5" }),
    hit("C5 hits", "chat", body("C5"), [H.rawKey(c5Key)], { same: "c5" }),
    clearKey("C5 clear by the URL-encoded key", "c5%20key%3Awith%3Acolons%20{{run_id}}"),
    miss("C5 the entry is gone", "chat", body("C5"), [H.rawKey(c5Key)], { same: "c5", poll: true }),
  ]);
}

// --------------------------------------------------------------------------- //
// K: x-bifrost-cache-* response headers on every inference route
// --------------------------------------------------------------------------- //

function folderK() {
  const say = (id) => `${id}: reply with one word: ready.`;
  const anthropic = (id, extra = {}) => ({ model: "claude-haiku-4-5", max_tokens: 8, temperature: 0, messages: [{ role: "user", content: say(id) }], ...extra });
  const genai = { contents: [{ role: "user", parts: [{ text: say("K4") }] }], generationConfig: { maxOutputTokens: 8, temperature: 0, thinkingConfig: { thinkingBudget: 0 } } };
  const cohere = { model: "command-r7b-12-2024", max_tokens: 8, temperature: 0, messages: [{ role: "user", content: say("K5") }] };
  const bedrock = { messages: [{ role: "user", content: [{ text: say("K6") }] }], inferenceConfig: { maxTokens: 8, temperature: 0 } };
  const sdk = (id, label, r, body, headers = []) => [
    write(`${id} ${label} write`, r, body, [H.key(id.toLowerCase()), ...headers], { source: "headers", noBodyCacheDebug: true, save: id.toLowerCase() }),
    hit(`${id} ${label} hits`, r, body, [H.key(id.toLowerCase()), ...headers], { source: "headers", noBodyCacheDebug: true, same: id.toLowerCase() }),
  ];
  return folder("K: cache response headers", [
    putPlugin("K baseline", DIRECT()),
    write("K1 chat write: headers equal cache_debug", "chat", chat(say("K1")), [H.key("k1")], { save: "k1", headersMatch: "all" }),
    hit("K1 chat hit: headers equal cache_debug", "chat", chat(say("K1")), [H.key("k1")], { same: "k1", headersMatch: "all" }),
    write("K2 chat stream write: headers equal the final chunk", "chat", chatStream(say("K2")), [H.key("k2")], { save: "k2", headersMatch: "stream" }),
    hit("K2 chat stream hit: headers equal the final chunk", "chat", chatStream(say("K2")), [H.key("k2")], { same: "k2", headersMatch: "stream" }),
    sdk("K3", "Anthropic messages", "anthropic", anthropic("K3"), [H.anthropicVersion()]),
    write("K3 Anthropic messages stream write", "anthropic", anthropic("K3 stream", { stream: true }), [H.key("k3s"), H.anthropicVersion()], { source: "headers", noBodyCacheDebug: true, save: "k3s" }),
    hit("K3 Anthropic messages stream hits", "anthropic", anthropic("K3 stream", { stream: true }), [H.key("k3s"), H.anthropicVersion()], { source: "headers", noBodyCacheDebug: true, same: "k3s" }),
    sdk("K4", "GenAI generateContent", "genai", genai),
    sdk("K5", "Cohere v2 chat", "cohere", cohere),
    sdk("K6", "Bedrock Converse", "bedrock", bedrock),
    infer("K8 chat without a cache key sends no cache headers", "chat", chat(say("K8")), [], { outcome: "none" }),
    infer("K8 Anthropic without a cache key sends no cache headers", "anthropic", anthropic("K8"), [H.anthropicVersion()], { source: "headers", outcome: "none" }),
    infer("K8 a skipped lookup sends no cache headers", "chat", chat(say("K8 skipped")), [H.key("k8"), H.type("semantic")], { outcome: "none" }),
  ]);
}

// --------------------------------------------------------------------------- //
// G: identity scoping
// --------------------------------------------------------------------------- //

function folderG() {
  const vk = (who) =>
    api(`G create virtual key ${who.toUpperCase()}`, "POST", ["api", "governance", "virtual-keys"], {
      name: `scw-vk${who}-{{run_id}}`,
      is_active: true,
      provider_configs: [{ provider: "openai", key_ids: ["*"], allowed_models: ["*"], weight: 1 }],
    }, {
      status: [200, 201],
      fields: [
        { path: "virtual_key.value", saveAs: `vk${who}_value`, nonEmpty: true },
        { path: "virtual_key.id", saveAs: `vk${who}_id`, nonEmpty: true },
      ],
    });
  const g1 = chat("G1: reply with one word: ready.");
  const g4 = chat("G4: reply with one word: ready.");
  const g5 = chat("G5: reply with one word: ready.");
  const g7 = chat("G7: reply with one word: ready.");
  const g9 = chat("G9: reply with one word: ready.");
  const g10 = chatStream("G10: count to three.");
  const p = PAIRS[3];
  return folder("G: identity scoping", [
    putPlugin("G baseline", DIRECT()),
    vk("a"),
    vk("b"),
    write("G1 VK-A writes", "chat", g1, [H.key("g1"), H.vk("a")], { save: "g1a" }),
    hit("G1 VK-A hits", "chat", g1, [H.key("g1"), H.vk("a")], { same: "g1a" }),
    miss("G1 VK-B misses", "chat", g1, [H.key("g1"), H.vk("b")], { distinct: ["g1a"], save: "g1b" }),
    miss("G1 anonymous misses", "chat", g1, [H.key("g1")], { distinct: ["g1a", "g1b"], save: "g1n" }),
    hit("G1 VK-A still hits", "chat", g1, [H.key("g1"), H.vk("a")], { same: "g1a" }),
    write("G3 VK-B writes", "chat", g1, [H.key("g1"), H.vk("b")], { same: "g1b" }),
    hit("G3 VK-B hits", "chat", g1, [H.key("g1"), H.vk("b")], { same: "g1b" }),
    write("G3 anonymous writes", "chat", g1, [H.key("g1")], { same: "g1n" }),
    hit("G3 anonymous hits", "chat", g1, [H.key("g1")], { same: "g1n" }),
    clearKey("G3 clear by the raw key", "g1-{{run_id}}"),
    miss("G3 VK-A's entry is gone", "chat", g1, [H.key("g1"), H.vk("a")], { same: "g1a", poll: true }),
    miss("G3 VK-B's entry is gone", "chat", g1, [H.key("g1"), H.vk("b")], { same: "g1b", poll: true }),
    miss("G3 anonymous entry is gone", "chat", g1, [H.key("g1")], { same: "g1n", poll: true }),
    write("G4 VK-A writes", "chat", g4, [H.key("g4"), H.vk("a")], { save: "g4a" }),
    write("G4 VK-B writes", "chat", g4, [H.key("g4"), H.vk("b")], { save: "g4b" }),
    hit("G4 VK-A hits", "chat", g4, [H.key("g4"), H.vk("a")], { same: "g4a" }),
    hit("G4 VK-B hits", "chat", g4, [H.key("g4"), H.vk("b")], { same: "g4b" }),
    clearId("G4 clear VK-A's entry by cache_id", "g4a"),
    miss("G4 VK-A's entry is gone", "chat", g4, [H.key("g4"), H.vk("a")], { same: "g4a", poll: true }),
    hit("G4 VK-B's entry still hits", "chat", g4, [H.key("g4"), H.vk("b")], { same: "g4b" }),
    putPlugin("G5 set default_cache_key", DIRECT({ default_cache_key: "gdef-{{run_id}}" })),
    write("G5 VK-A writes under the default key", "chat", g5, [H.vk("a")], { save: "g5" }),
    hit("G5 VK-A hits under the default key", "chat", g5, [H.vk("a")], { same: "g5" }),
    miss("G5 VK-B misses under the default key", "chat", g5, [H.vk("b")], { distinct: ["g5"] }),
    putPlugin("G6 semantic mode", SEMANTIC({ vector_store_namespace: NS.identity })),
    write("G6 VK-A writes", "chat", chat(p.canonical), [H.key("g6"), H.vk("a")], { save: "g6", embedding: true }),
    hit("G6 VK-A hits", "chat", chat(p.canonical), [H.key("g6"), H.vk("a")], { same: "g6" }),
    miss("G6 VK-B's paraphrase misses", "chat", chat(p.paraphrase), [H.key("g6"), H.vk("b")], { distinct: ["g6"], embedding: true }),
    hit("G6 VK-A's paraphrase is a semantic hit", "chat", chat(p.paraphrase), [H.key("g6"), H.vk("a")], { outcome: "semantic", same: "g6" }),
    putPlugin("G7 disable identity scoping", DIRECT({ disable_identity_scoping: true })),
    getPlugin("G7 GET echoes true", [{ path: "config.disable_identity_scoping", eq: true }]),
    write("G7 VK-A writes", "chat", g7, [H.key("g7"), H.vk("a")], { save: "g7" }),
    hit("G7 VK-A hits", "chat", g7, [H.key("g7"), H.vk("a")], { same: "g7" }),
    hit("G7 VK-B hits the shared entry", "chat", g7, [H.key("g7"), H.vk("b")], { same: "g7" }),
    hit("G7 anonymous hits the shared entry", "chat", g7, [H.key("g7")], { same: "g7" }),
    putPlugin("G8 enable identity scoping", DIRECT({ disable_identity_scoping: false })),
    miss("G8 VK-B no longer sees the shared entry", "chat", g7, [H.key("g7"), H.vk("b")], { distinct: ["g7"] }),
    write("G8 VK-A writes its own entry", "chat", g7, [H.key("g7"), H.vk("a")], { distinct: ["g7"], save: "g8" }),
    hit("G8 VK-A hits its own entry", "chat", g7, [H.key("g7"), H.vk("a")], { same: "g8" }),
    write("G9 a key shaped like vk:something:key writes", "chat", g9, [H.rawKey("vk:something:key-{{run_id}}")], { save: "g9" }),
    hit("G9 that key hits like any other", "chat", g9, [H.rawKey("vk:something:key-{{run_id}}")], { same: "g9" }),
    write("G10 VK-A stream writes", "chat", g10, [H.key("g10"), H.vk("a")], { save: "g10", stream: { finalOnly: true, save: "g10" } }),
    hit("G10 VK-A stream replays", "chat", g10, [H.key("g10"), H.vk("a")], { same: "g10", stream: { finalOnly: true, compare: "g10" } }),
    miss("G10 VK-B stream misses", "chat", g10, [H.key("g10"), H.vk("b")], { distinct: ["g10"] }),
    putPlugin("G cleanup: direct baseline", DIRECT()),
  ]);
}

// --------------------------------------------------------------------------- //
// S: semantic mode
// --------------------------------------------------------------------------- //

function folderS() {
  const [p1, p2, p3, p4, p5] = PAIRS;
  const embedded = { embedding: true, providerUsed: EMBED, modelUsed: EMBED_MODEL };
  const semanticHit = (same, extra = {}) => ({ outcome: "semantic", same, threshold: 0.85, providerUsed: EMBED, modelUsed: EMBED_MODEL, ...extra });
  const tools = (prompt, list) => chat(prompt, { tools: list, tool_choice: "none" });
  const anthropic = (prompt) => ({ model: "claude-haiku-4-5", max_tokens: 8, temperature: 0, messages: [{ role: "user", content: prompt }] });
  // With the system prompt embedded, "You are terse." against this one scores about 0.71 on the
  // S17 pair (0.88 against "You are verbose.", still a hit); without it the pair scores 0.92.
  const HISTORIAN =
    "You are a medieval historian. Answer every question with a detailed account of fourteenth-century European trade routes, guild politics and maritime law.";
  const NEAR_MISS = "What is the capital city of Germany in modern times?";
  return folder("S: semantic mode", [
    putPlugin("S0 semantic baseline", SEMANTIC()),
    getPlugin("S0 config is echoed", [
      { path: "config.provider", eq: EMBED },
      { path: "config.embedding_model", eq: EMBED_MODEL },
      { path: "config.dimension", eq: 1536 },
    ]),
    write("S1 canonical write embeds", "chat", chat(p1.canonical), [H.key("s1")], { save: "s1", saveCd: "s1a", saveReq: "s1a", ...embedded }),
    hit("S1 exact repeat is a direct hit", "chat", chat(p1.canonical), [H.key("s1")], { same: "s1" }),
    hit("S2 paraphrase is a semantic hit", "chat", chat(p1.paraphrase), [H.key("s1")], semanticHit("s1", { saveCd: "s2", saveReq: "s2", headersMatch: "all" })),
    miss("S3 unrelated prompt misses", "chat", chat(p1.unrelated), [H.key("s1")], embedded),
    miss("S4 threshold 0 cannot loosen the gate", "chat", chat(p1.unrelated), [H.key("s1"), H.threshold("0")], embedded),
    miss("S5 threshold 0.99 misses the paraphrase", "chat", chat(p1.paraphrase), [H.key("s1"), H.threshold("0.99")], embedded),
    hit("S5 without the header the paraphrase hits", "chat", chat(p1.paraphrase), [H.key("s1")], semanticHit("s1")),
    miss("S6 threshold 1.5 is capped at 1", "chat", chat(p1.paraphrase), [H.key("s1"), H.threshold("1.5")], embedded),
    hit("S6 threshold -1 falls back to the configured threshold", "chat", chat(p1.paraphrase), [H.key("s1"), H.threshold("-1")], semanticHit("s1")),
    hit("S7 cache type semantic on the exact body", "chat", chat(p1.canonical), [H.key("s1"), H.type("semantic")], semanticHit("s1")),
    miss("S7 cache type direct on a paraphrase", "chat", chat(p1.paraphrase), [H.key("s1"), H.type("direct")], { embedding: false }),
    miss("S8 paraphrase under another key misses", "chat", chat(p1.paraphrase), [H.key("s8")], { distinct: ["s1"], ...embedded }),
    write("S9 write on gpt-4o-mini", "chat", chat(p2.canonical), [H.key("s9")], { save: "s9", ...embedded }),
    hit("S9 gpt-4o-mini hits", "chat", chat(p2.canonical), [H.key("s9")], { same: "s9" }),
    miss("S9 paraphrase on gpt-4o misses", "chat", chat(p2.paraphrase, { model: MODEL_ALT }), [H.key("s9")], { distinct: ["s9"], ...embedded }),
    putPlugin("S9 cache_by_model false", SEMANTIC({ cache_by_model: false })),
    write("S9 write on gpt-4o-mini with cache_by_model false", "chat", chat(p2.canonical), [H.key("s9b")], { save: "s9b", ...embedded }),
    hit("S9 gpt-4o-mini hits with cache_by_model false", "chat", chat(p2.canonical), [H.key("s9b")], { same: "s9b" }),
    hit("S9 paraphrase on gpt-4o is a semantic hit", "chat", chat(p2.paraphrase, { model: MODEL_ALT }), [H.key("s9b")], semanticHit("s9b")),
    putPlugin("S10 cache_by_provider and cache_by_model false", SEMANTIC({ cache_by_model: false, cache_by_provider: false })),
    write("S10 write on openai", "chat", chat(p3.canonical), [H.key("s10")], {
      save: "s10",
      ...embedded,
      fields: [
        { path: "choices.0.message.content", save: "s10content" },
        { path: "model", save: "s10model" },
      ],
    }),
    hit("S10 openai hits", "chat", chat(p3.canonical), [H.key("s10")], { same: "s10" }),
    hit("S10 anthropic paraphrase replays the OpenAI response", "chat", chat(p3.paraphrase, { model: "anthropic/claude-haiku-4-5" }), [H.key("s10")], {
      ...semanticHit("s10"),
      fields: [
        { path: "choices.0.message.content", equals: "s10content" },
        { path: "model", equals: "s10model" },
        { path: "object", eq: "chat.completion" },
      ],
    }),
    putPlugin("S10 restore baseline", SEMANTIC()),
    write("S11 stream write", "chat", chatStream(p4.canonical), [H.key("s11")], { save: "s11", ...embedded, stream: { finalOnly: true, save: "s11" } }),
    hit("S11 exact stream hits", "chat", chatStream(p4.canonical), [H.key("s11")], { same: "s11", stream: { finalOnly: true, compare: "s11" } }),
    hit("S11 paraphrase stream is a semantic hit", "chat", chatStream(p4.paraphrase), [H.key("s11")], {
      ...semanticHit("s11", { headersMatch: "stream" }),
      stream: { done: true, finalOnly: true, sameCount: "s11" },
    }),
    write("S12 a paraphrase written first", "chat", chat(p5.paraphrase), [H.key("s12")], { save: "s12", ...embedded }),
    hit("S12 the same paraphrase is a direct hit", "chat", chat(p5.paraphrase), [H.key("s12")], { same: "s12" }),
    write("S13 write", "chat", chat(p1.canonical), [H.key("s13")], { save: "s13", ...embedded }),
    hit("S13 hits", "chat", chat(p1.canonical), [H.key("s13")], { same: "s13" }),
    clearId("S13 clear by cache_id", "s13"),
    miss("S13 the paraphrase misses after the clear", "chat", chat(p1.paraphrase), [H.key("s13")], { poll: true, ...embedded }),
    write("S14 write", "chat", chat(p2.canonical), [H.key("s14")], { save: "s14", ...embedded }),
    hit("S14 hits", "chat", chat(p2.canonical), [H.key("s14")], { same: "s14" }),
    clearKey("S14 clear by key", "s14-{{run_id}}"),
    miss("S14 the paraphrase misses after the clear", "chat", chat(p2.paraphrase), [H.key("s14")], { poll: true, ...embedded }),
    write("S15 write with ttl 3s", "chat", chat(p3.canonical), [H.key("s15"), H.ttl("3s")], { save: "s15", ...embedded }),
    hit("S15 hits at once", "chat", chat(p3.canonical), [H.key("s15")], { same: "s15" }),
    miss("S15 the paraphrase misses after the ttl", "chat", chat(p3.paraphrase), [H.key("s15")], embedded, { wait: 5 }),
    miss("S16 no-store paraphrase misses", "chat", chat(p4.paraphrase), [H.key("s16")], { save: "s16", ...embedded }),
    miss("S16 nothing was written", "chat", chat(p4.paraphrase), [H.key("s16")], { same: "s16", ...embedded }, { wait: 2 }),
    putPlugin("S17 exclude_system_prompt true", SEMANTIC({ exclude_system_prompt: true })),
    write("S17 write with one system prompt", "chat", chatSystem("You are terse.", p5.canonical), [H.key("s17")], { save: "s17", ...embedded }),
    hit("S17 hits", "chat", chatSystem("You are terse.", p5.canonical), [H.key("s17")], { same: "s17" }),
    hit("S17 an unrelated system prompt and a paraphrase hit", "chat", chatSystem(HISTORIAN, p5.paraphrase), [H.key("s17")], semanticHit("s17")),
    putPlugin("S17 exclude_system_prompt false", SEMANTIC()),
    write("S17 write with the system prompt included", "chat", chatSystem("You are terse.", p5.canonical), [H.key("s17b")], { save: "s17b", ...embedded }),
    hit("S17 hits with the system prompt included", "chat", chatSystem("You are terse.", p5.canonical), [H.key("s17b")], { same: "s17b" }),
    miss("S17 an unrelated system prompt and a paraphrase miss", "chat", chatSystem(HISTORIAN, p5.paraphrase), [H.key("s17b")], { distinct: ["s17b"] }),
    infer("S18 over the conversation threshold is not cached", "chat", chatMessages([
      { role: "user", content: "My name is Ana." },
      { role: "assistant", content: "Nice to meet you, Ana." },
      { role: "user", content: "I live in Lisbon." },
      { role: "user", content: p1.canonical },
    ]), [H.key("s18")], { outcome: "none" }),
    write("S19 write with an image", "chat", withImage(p1.canonical, RED_PNG), [H.key("s19")], { save: "s19", ...embedded }),
    hit("S19 the same image hits", "chat", withImage(p1.canonical, RED_PNG), [H.key("s19")], { same: "s19" }),
    miss("S19 a paraphrase with another image misses", "chat", withImage(p1.paraphrase, BLUE_PNG), [H.key("s19")], { distinct: ["s19"] }),
    write("S20 embeddings write takes the direct path", "embed", embedding("S20: semantic cache wiring"), [H.key("s20")], { save: "s20", embedding: false }),
    hit("S20 embeddings hit direct", "embed", embedding("S20: semantic cache wiring"), [H.key("s20")], { same: "s20" }),
    write("S21 responses write", "responses", responses(p2.canonical), [H.key("s21")], { save: "s21", ...embedded }),
    hit("S21 responses hits", "responses", responses(p2.canonical), [H.key("s21")], { same: "s21" }),
    hit("S21 responses paraphrase is a semantic hit", "responses", responses(p2.paraphrase), [H.key("s21")], semanticHit("s21")),
    write("S21 text completion write", "text", text(p3.canonical), [H.key("s21c")], { save: "s21c", ...embedded }),
    hit("S21 text completion hits", "text", text(p3.canonical), [H.key("s21c")], { same: "s21c" }),
    hit("S21 text completion paraphrase is a semantic hit", "text", text(p3.paraphrase), [H.key("s21c")], semanticHit("s21c")),
    write("S22 write with two tools", "chat", tools(p4.canonical, [WEATHER, TIME]), [H.key("s22")], { save: "s22", ...embedded }),
    hit("S22 hits", "chat", tools(p4.canonical, [WEATHER, TIME]), [H.key("s22")], { same: "s22" }),
    hit("S22 paraphrase with tools reordered is a semantic hit", "chat", tools(p4.paraphrase, [TIME, WEATHER]), [H.key("s22")], semanticHit("s22")),
    miss("S22 paraphrase with a renamed tool misses", "chat", tools(p4.paraphrase, [FORECAST, TIME]), [H.key("s22")], { distinct: ["s22"] }),
    write("S23 write at temperature 0", "chat", chat(p5.canonical), [H.key("s23")], { save: "s23", ...embedded }),
    hit("S23 hits", "chat", chat(p5.canonical), [H.key("s23")], { same: "s23" }),
    miss("S23 paraphrase at temperature 0.5 misses", "chat", chat(p5.paraphrase, { temperature: 0.5 }), [H.key("s23")], { distinct: ["s23"] }),
    // Similarity is cosine on every store. This pair scores about 0.62, under the plugin's default
    // threshold of 0.8 on any store that compares cosine; Weaviate's certainty, (1 + cosine) / 2,
    // puts the same pair at about 0.81, which would serve France's answer to Germany's question.
    putPlugin("S29 plugin default threshold", SEMANTIC({ threshold: null })),
    write("S29 write", "chat", chat(p1.canonical), [H.key("s29")], { save: "s29", ...embedded }),
    hit("S29 hits", "chat", chat(p1.canonical), [H.key("s29")], { same: "s29" }),
    miss("S29 the same question about another country misses", "chat", chat(NEAR_MISS), [H.key("s29")], { distinct: ["s29"], ...embedded }),
    putPlugin("S29 restore baseline", SEMANTIC()),
    putPlugin("S24 switch to a new namespace", SEMANTIC({ vector_store_namespace: NS.semanticAlt })),
    miss("S24 the paraphrase misses in the new namespace", "chat", chat(p1.paraphrase), [H.key("s1")], embedded),
    putPlugin("S24 switch back", SEMANTIC()),
    hit("S24 the paraphrase hits in the old namespace", "chat", chat(p1.paraphrase), [H.key("s1")], semanticHit("s1")),
    write("S25 image generation write", "image", image(IMAGE_PAIR.canonical), [H.key("s25")], { save: "s25", ...embedded, fingerprint: [{ path: "data.0.b64_json", save: "s25" }] }),
    hit("S25 image generation hits", "image", image(IMAGE_PAIR.canonical), [H.key("s25")], { same: "s25" }),
    hit("S25 image paraphrase is a semantic hit with the same image", "image", image(IMAGE_PAIR.paraphrase), [H.key("s25")], {
      ...semanticHit("s25"),
      fingerprint: [{ path: "data.0.b64_json", equals: "s25" }],
    }),
    putPlugin("S26 embedding provider with an invalid key", SEMANTIC({ provider: BADEMBED })),
    write("S26 the request still succeeds and writes", "chat", chat(p1.canonical), [H.key("s26")], { save: "s26", saveReq: "s26a", embedding: false }),
    hit("S26 the exact repeat hits direct", "chat", chat(p1.canonical), [H.key("s26")], { same: "s26", saveReq: "s26b" }),
    logRow("S26 the write's log row has the plugin warning", "s26a", {
      outcome: "miss",
      same: "s26",
      pluginLogs: { plugin: "semantic_cache", level: "warn", includes: "failed to generate embedding" },
    }),
    logRow("S26 the hit's log row has no warning", "s26b", { outcome: "direct", same: "s26", pluginLogs: { plugin: "semantic_cache", level: "warn", absent: true } }),
    putPlugin("S27 semantic baseline", SEMANTIC()),
    api("S27 delete the embedding provider", "DELETE", ["api", "providers", EMBED], null, { status: [200, 204] }),
    write("S27 the request still succeeds and writes", "chat", chat(p2.canonical), [H.key("s27")], { save: "s27", saveReq: "s27a", embedding: false }),
    hit("S27 the exact repeat hits direct", "chat", chat(p2.canonical), [H.key("s27")], { same: "s27" }),
    logRow("S27 the write's log row has the plugin warning", "s27a", {
      outcome: "miss",
      same: "s27",
      pluginLogs: { plugin: "semantic_cache", level: "warn", includes: "failed to generate embedding" },
    }),
    api("S27 recreate the embedding provider", "POST", ["api", "providers"], { provider: EMBED, custom_provider_config: { base_provider_type: "openai", is_key_less: false } }, { status: [200, 201] }),
    api("S27 recreate the embedding key", "POST", ["api", "providers", EMBED, "keys"], { name: `${EMBED}-key`, value: "env.OPENAI_API_KEY", models: ["*"], weight: 1 }, { status: [200, 201] }),
    write("S27 write after the provider is back", "chat", chat(p3.canonical), [H.key("s27b")], { save: "s27b", ...embedded }),
    hit("S27 hits after the provider is back", "chat", chat(p3.canonical), [H.key("s27b")], { same: "s27b" }),
    hit("S27 paraphrase is a semantic hit again", "chat", chat(p3.paraphrase), [H.key("s27b")], semanticHit("s27b", { saveReq: "s27c" })),
    logRow("S27 the semantic hit's log row has no warning", "s27c", { outcome: "semantic", same: "s27b", pluginLogs: { plugin: "semantic_cache", level: "warn", absent: true } }),
    logRow("S28 the semantic miss's log row", "s1a", { outcome: "miss", same: "s1", ...embedded }),
    logRow("S28 the semantic hit's log row", "s2", semanticHit("s1")),
    write("K7 Anthropic messages write in semantic mode", "anthropic", anthropic(p5.canonical), [H.key("k7"), H.anthropicVersion()], {
      source: "headers",
      noBodyCacheDebug: true,
      save: "k7",
      ...embedded,
    }),
    hit("K7 Anthropic messages exact repeat", "anthropic", anthropic(p5.canonical), [H.key("k7"), H.anthropicVersion()], { source: "headers", same: "k7" }),
    hit("K7 Anthropic messages paraphrase carries every semantic header", "anthropic", anthropic(p5.paraphrase), [H.key("k7"), H.anthropicVersion()], {
      source: "headers",
      noBodyCacheDebug: true,
      ...semanticHit("k7"),
    }),
    putPlugin("S cleanup: semantic baseline", SEMANTIC()),
  ]);
}

// --------------------------------------------------------------------------- //
// O: logs integration
// --------------------------------------------------------------------------- //

function folderO() {
  const p = PAIRS[1];
  const stats = (name, spec) => api(name, "GET", ["api", "logs", "stats"], null, { status: 200, ...spec });
  const list = (name, type, spec) =>
    api(name, "GET", ["api", "logs"], null, { status: 200, ...spec }, {
      query: [
        { key: "cache_hit_types", value: type },
        { key: "limit", value: "1000" },
      ],
    });
  return folder("O: logs", [
    logRow("O1 a direct miss's log row equals the response", "d1a", { outcome: "miss", cdEquals: "d1a" }),
    logRow("O1 a direct hit's log row equals the response", "d1b", { outcome: "direct", cdEquals: "d1b" }),
    logRow("O1 a semantic miss's log row equals the response", "s1a", { outcome: "miss", cdEquals: "s1a" }),
    logRow("O1 a semantic hit's log row equals the response", "s2", { outcome: "semantic", cdEquals: "s2" }),
    list("O2 the direct filter lists the hit and not the miss", "direct", { ids: { path: "logs", include: ["d1b"], exclude: ["d1a"] } }),
    list("O2 the semantic filter lists the hit and not the miss", "semantic", { ids: { path: "logs", include: ["s2"], exclude: ["s1a"] } }),
    stats("O3 stats before", {
      fields: [
        { path: "direct_cache_hits", save: "o3direct" },
        { path: "semantic_cache_hits", save: "o3semantic" },
      ],
    }),
    putPlugin("O3 semantic baseline", SEMANTIC()),
    write("O3 write", "chat", chat(p.canonical), [H.key("o3")], { save: "o3", embedding: true }),
    hit("O3 a direct hit", "chat", chat(p.canonical), [H.key("o3")], { same: "o3" }),
    hit("O3 a semantic hit", "chat", chat(p.paraphrase), [H.key("o3")], { outcome: "semantic", same: "o3" }),
    stats("O3 stats count both hits", {
      poll: true,
      fields: [
        { path: "direct_cache_hits", atLeastSavedPlus: ["o3direct", 1] },
        { path: "semantic_cache_hits", atLeastSavedPlus: ["o3semantic", 1] },
      ],
    }),
    logRow("O4 the speech hit's log row", "x8b", { outcome: "direct", same: "x8" }),
  ]);
}

// --------------------------------------------------------------------------- //
// Teardown
// --------------------------------------------------------------------------- //

function folderTeardown() {
  const gone = { status: [200, 204, 404] };
  const providers = [...STANDARD_PROVIDERS.map((p) => p.name), "bedrock", ...CUSTOM_PROVIDERS.map((p) => p.name)];
  return folder("Teardown", [
    api("Teardown: delete the plugin", "DELETE", PLUGIN, null, gone),
    api("Teardown: delete virtual key A", "DELETE", ["api", "governance", "virtual-keys", "{{vka_id}}"], null, gone),
    api("Teardown: delete virtual key B", "DELETE", ["api", "governance", "virtual-keys", "{{vkb_id}}"], null, gone),
    ...providers.map((n) => api(`Teardown: delete provider ${n}`, "DELETE", ["api", "providers", n], null, gone)),
  ]);
}

// --------------------------------------------------------------------------- //
// Assembly
// --------------------------------------------------------------------------- //

const folders = [folderP(), folderM(), folderD(), folderX(), folderT(), folderH(), folderC(), folderK(), folderG(), folderS(), folderO(), folderTeardown()];

const collection = buildCollection({
  id: "bifrost-semantic-cache-wiring",
  name: "Bifrost Semantic Cache Wiring",
  description:
    "End-to-end semantic_cache plugin coverage against one vector store (Redis or Weaviate): plugin " +
    "management, direct and semantic caching across every cacheable request type, streams, per-request " +
    "headers, clear endpoints, x-bifrost-cache-* response headers, identity scoping and the logs. " +
    "Machine-generated by runners/build-semantic-cache-wiring.mjs - do not hand-edit.",
  expandedScenarios: folders,
  extraVariables: [
    { key: "ns", value: "", type: "string" },
    { key: "setup_token", value: "", type: "string" },
    { key: "scw_lib", value: scwLibSource(), type: "string" },
  ],
});

// After the builder's run id: derive the namespace suffix once, and send the setup token on every
// /api call, which the OSS setup lock refuses without it while dashboard auth is off.
collection.event[0].script.exec.push(
  "if (!pm.collectionVariables.get('ns')) {",
  `  pm.collectionVariables.set('ns', (${scwNamespaceSuffix.toString()})(pm.collectionVariables.get('run_id')));`,
  "}",
  "var setupToken = pm.variables.get('setup_token');",
  "if (setupToken && pm.request.url.getPath().indexOf('/api/') === 0) {",
  "  pm.request.headers.upsert({ key: 'X-Bifrost-Setup-Token', value: setupToken });",
  "}"
);

writeCollection(
  resolveOutPath(DEFAULT_OUT),
  collection,
  folders.map((f) => `${f.name} (${f.item.length} requests)`)
);
