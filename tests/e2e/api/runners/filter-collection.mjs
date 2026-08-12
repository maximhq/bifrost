#!/usr/bin/env node
// Filters a Postman collection by provider, feature keyword(s), or "rerun failed"
// from a prior newman report. Writes the filtered collection to --out.
//
// Usage:
//   node filter-collection.mjs --source path.json --out /tmp/x.json --provider anthropic
//   node filter-collection.mjs --source path.json --out /tmp/x.json --feature "web search"
//   node filter-collection.mjs --source path.json --out /tmp/x.json --feature "cross-cut,structured output"   # multi-keyword AND
//
// Structural keyword: "cross-cut" matches by route shape (unified /v1/chat/completions
// with a provider/model body), not just by name substring. Lets the AND filter find
// every cross-cut row without renaming 100+ items to add a literal "Cross-cut:" prefix.
//   node filter-collection.mjs --source path.json --out /tmp/x.json --rerun-failed --report tmp/newman-report.json
//   node filter-collection.mjs --source path.json --out /tmp/x.json --provider openai --class reasoning --shard 2/4

import { readFileSync, writeFileSync, existsSync } from "node:fs";
import { readReport } from "./lib/read-report.mjs";
import { buildHaystack } from "./lib/haystack.mjs";
import { walkRequests, buildProducerIndex, chainedDependencies } from "./lib/chained-vars.mjs";
import { rateLimitedNames } from "./lib/rate-limit-retry.mjs";

const args = Object.fromEntries(
  process.argv.slice(2).reduce((acc, cur, i, arr) => {
    if (cur.startsWith("--")) {
      const key = cur.slice(2);
      const next = arr[i + 1];
      acc.push([key, next && !next.startsWith("--") ? next : "true"]);
    }
    return acc;
  }, [])
);

const SOURCE = args.source;
const OUT = args.out;
const PROVIDER = (args.provider || "").toLowerCase();
const FEATURE_PARTS = (args.feature || "").toLowerCase().split(",").map((s) => s.trim()).filter(Boolean);
// --feature-any is the OR-of-keywords counterpart of --feature (which ANDs). Item passes
// if it matches at least one keyword. Combines with --feature/--provider via AND.
const FEATURE_ANY_PARTS = (args["feature-any"] || "").toLowerCase().split(",").map((s) => s.trim()).filter(Boolean);
// --exclude-feature-any is the negation of --feature-any: an item matching ANY of these keywords
// is dropped. It exists because the harness defers some row groups (cache-parity) to a separate
// sequential pass, and the main pass needs to carve them out WITHOUT restating everything else as
// a positive keyword list - the collection holds plenty of rows (management APIs, auth matrix,
// ...) that match no modality keyword at all, so an "everything except X" expressed positively
// would silently drop them. Applied after the positive predicates, so it always wins.
const EXCLUDE_FEATURE_ANY_PARTS = (args["exclude-feature-any"] || "")
  .toLowerCase()
  .split(",")
  .map((s) => s.trim())
  .filter(Boolean);
// --folder is a pre-filter mirroring newman's own --folder flag, so a provider fork with zero
// items inside the target folder gets skipped cleanly (existing "no items - skipping" logic in
// the run-provider-harness-test Makefile target) instead of being forked and only then failing
// with newman's "Unable to find a folder or request" once it tries to apply --folder itself.
const FOLDER = (args.folder || "").toLowerCase();
// --class assigns every item to exactly ONE modality class (see CLASS_ORDER) and keeps only the
// requested one. It exists so a provider fork can be sharded further without any item running
// twice: --feature-any cannot do this job because the classes overlap heavily (a streaming vision
// tool call matches three of them), so an OR-based shard set would re-run that item once per shard
// while the rows matching no modality keyword at all would be dropped entirely. First-match-wins
// over a fixed priority order plus the "other" catch-all makes the shards a true partition, i.e.
// the union of every --class shard is exactly the unsharded set.
const CLASS = (args.class || "").toLowerCase();
// --shard <k>/<n> splits whatever the other predicates selected into n equal slices and keeps the
// k-th (1-based). It exists because --class alone cannot flatten the sweep's tail: the class
// partition is fixed at ten buckets, and the slow ones are slow per REQUEST (a reasoning row costs
// ~8s against a chat row's ~1s), so "reasoning" stays a ~20 minute serial run however many jobs are
// free beside it. Splitting is by position in the selected list rather than by name hash: position
// is stable for a given source collection and set of filters, which keeps the slices a partition,
// and the round-robin below spreads a contiguous block of expensive rows (the criss-cross grids are
// emitted in provider order) across every slice instead of dropping it whole into one.
const SHARD = (args.shard || "").trim();
let SHARD_INDEX = 0;
let SHARD_COUNT = 0;
if (SHARD && SHARD !== "true") {
  const m = SHARD.match(/^(\d+)\s*\/\s*(\d+)$/);
  if (!m) {
    console.error(`[filter-collection] --shard must look like "2/4", got "${SHARD}"`);
    process.exit(2);
  }
  SHARD_INDEX = Number(m[1]);
  SHARD_COUNT = Number(m[2]);
  if (SHARD_COUNT < 1 || SHARD_INDEX < 1 || SHARD_INDEX > SHARD_COUNT) {
    console.error(`[filter-collection] --shard "${SHARD}" is out of range: need 1 <= k <= n and n >= 1`);
    process.exit(2);
  }
}
const RERUN_FAILED = args["rerun-failed"] === "true";
const RERUN_RATE_LIMITED = args["rerun-rate-limited"] === "true";
const REPORT = args.report || "tmp/newman-report.json";
// --smoke is a curated selection: a manifest of request names that make up the
// smoke set. It exists because the rows a smoke run most needs - the generated
// cache-parity matrices - are emitted by augment-provider-harness.mjs and never
// appear in provider-harness.json, so they cannot be tagged in place the way
// [PREVIEW] / [SKIP] rows are. Matching by name against the augmented collection
// is the only mechanism that can reach them.
const SMOKE = args.smoke && args.smoke !== "true" ? args.smoke : "";

if (!SOURCE || !OUT) {
  console.error("[filter-collection] --source and --out are required");
  process.exit(2);
}
if (!PROVIDER && !FEATURE_PARTS.length && !FEATURE_ANY_PARTS.length && !EXCLUDE_FEATURE_ANY_PARTS.length && !FOLDER && !CLASS && !SHARD_COUNT && !RERUN_FAILED && !RERUN_RATE_LIMITED && !SMOKE) {
  console.error(
    "[filter-collection] need at least one of: --provider, --feature, --feature-any, --exclude-feature-any, --folder, --class, --shard, --rerun-failed, --rerun-rate-limited, --smoke"
  );
  process.exit(2);
}

const PROVIDER_KEYWORDS = {
  openai: ["openai", "/openai", "gpt-", "o3", "o1"],
  anthropic: ["anthropic", "claude-"],
  bedrock: ["bedrock", "/bedrock"],
  bedrock_mantle: ["bedrock_mantle", "bedrock-mantle"],
  gemini: ["gemini", "/genai", "googlesearch"],
  vertex: ["vertex", "/genai/v1beta/models/{{vertexModel}}"],
  azure: ["azure", "deployments"],
  passthrough: ["_passthrough"],
  openrouter: ["openrouter"],
  replicate: ["replicate", "/replicate", "flux", "black-forest-labs"],
};

// Haystack = item JSON + ancestor folder names. Folder names encode the harness
// taxonomy ("Structured Output cross-cut", "Vertex Features", ...) so PROVIDER and
// FEATURE filters need to see them, otherwise a row named "openai/gpt-4o-mini" inside
// folder "Structured Output cross-cut" is invisible to FEATURE="cross-cut".
// Strips long base64-alphabet runs (40+ chars) before matching - embedded media payloads
// (base64 images/PDFs/audio/video, e.g. in the Token Parity Matrix) are long enough that a
// short PROVIDER_KEYWORDS substring like "o1" or "gpt-" can appear in them by pure chance,
// causing an item to be spuriously claimed by the wrong provider partition. Real searchable
// text (model names, prompts, folder names) never runs 40+ contiguous base64-alphabet chars.
const stripBase64Blobs = (s) => s.replace(/[A-Za-z0-9+/]{40,}={0,2}/g, "");



// Structural keywords - matched against route shape, not name substring. Lets users
// say FEATURE="cross-cut,structured output" and have it work for every row routed via
// unified /v1/chat/completions with a provider/model prefix, regardless of how the
// row is named or which folder it lives in.
const STRUCTURAL_KEYWORDS = {
  "cross-cut": (item) => {
    const req = item.request || {};
    const url = (typeof req.url === "string" ? req.url : req.url?.raw) || "";
    const body = req.body?.raw || "";
    const isUnified = /\/v1\/chat\/completions(\?|$)/.test(url) &&
      !/\/(openai|anthropic|bedrock|genai|azure)\/v1/.test(url) &&
      !/_passthrough/.test(url);
    const hasProviderPrefix = /"model"\s*:\s*"(openai|anthropic|bedrock|gemini|vertex|azure)\//.test(body);
    return isUnified && hasProviderPrefix;
  },
  crosscut: (item) => STRUCTURAL_KEYWORDS["cross-cut"](item),
};

const FEATURE_ALIASES = {
  chat: ["chat", "messages", "responses"],
  streaming: ["streaming", "\"stream\": true", "streamgeneratecontent", "converse-stream", "alt=sse"],
  embeddings: ["embeddings", "embedding"],
  audio: ["audio", "speech", "transcription"],
  "image-gen": ["image-gen", "image generation", "image gen", "images/generations"],
  tools: ["tools", "\"tools\"", "tool use", "tool_choice", "function calling", "functiondeclarations", "function_calling"],
  vision: ["vision", "image_url", "\"type\":\"image\"", "\"type\": \"image\"", "inline_data", "filedata"],
  json: ["json_schema", "json object", "structured output", "responseschema", "response_schema", "responsemimetype", "response mime"],
  reasoning: ["reasoning", "thinking", "reasoning_effort", "budget_tokens", "thinkingbudget", "thinking_budget"],
  // Comparison matrices, not modalities. These match on generated folder names rather than body
  // fields because the rows are built programmatically and their request bodies look like any
  // other chat call - only the ancestor folder identifies them (buildHaystack folds ancestor
  // names in, which is what makes folder-name aliases work here).
  "token-parity": ["token parity"],
  "cache-parity": [
    "cache-anchor",
    "prompt-cache matrix",
    "prompt-cache parity",
    "prompt caching",
    "cache_control",
    "cachepoint",
    "cached_tokens",
  ],
};

// Priority order for --class. First match wins, so the order decides where an overlapping item
// lands: the narrow modalities come first (an audio or image-gen row is that row's whole point),
// then the content modalities, and the broad "chat" alias last because nearly every request in the
// collection contains "messages" or "responses" somewhere and would otherwise swallow the grid.
// Anything matching none of them falls to "other" - 108 rows today (management APIs, auth matrix,
// governance), which must still run somewhere or a sharded sweep would quietly test less than an
// unsharded one. Deliberately excludes the token-parity / cache-parity aliases: those are folder-
// name matchers for comparison matrices, not modalities, and cache-parity is already carved out
// into its own deferred sequential pass by --exclude-feature-any.
const CLASS_ORDER = [
  "audio",
  "image-gen",
  "embeddings",
  "vision",
  "reasoning",
  "json",
  "tools",
  "streaming",
  "chat",
];
const CLASS_OTHER = "other";
const ALL_CLASSES = [...CLASS_ORDER, CLASS_OTHER];

if (CLASS && !ALL_CLASSES.includes(CLASS)) {
  console.error(`[filter-collection] unknown --class "${CLASS}". Expected one of: ${ALL_CLASSES.join(", ")}`);
  process.exit(2);
}

// Resolved against the same haystack every other predicate uses, so folder names count. Returns
// exactly one class per item, which is what makes the shard set a partition rather than an overlap.
const classifyItem = (item, ancestorNames) => {
  const haystack = buildHaystack(item, ancestorNames);
  for (const cls of CLASS_ORDER) {
    const aliases = FEATURE_ALIASES[cls] || [cls];
    if (aliases.some((alias) => haystack.includes(alias))) return cls;
  }
  return CLASS_OTHER;
};

const itemMatchesClass = (item, ancestorNames) => {
  if (!CLASS) return true;
  return classifyItem(item, ancestorNames) === CLASS;
};

const matchesKeyword = (item, ancestorNames, haystack, keyword) => {
  const structural = STRUCTURAL_KEYWORDS[keyword];
  if (structural && (structural(item) || haystack.includes(keyword))) return true;
  const aliases = FEATURE_ALIASES[keyword] || [keyword];
  return aliases.some((alias) => haystack.includes(alias));
};

const itemMatchesProvider = (item, ancestorNames) => {
  if (!PROVIDER) return true;
  const keywords = PROVIDER_KEYWORDS[PROVIDER] || [PROVIDER];
  const haystack = buildHaystack(item, ancestorNames);
  // OpenRouter rows (model "openrouter/<vendor>/<model>") embed vendor substrings like
  // gpt-/claude-/gemini, so they'd otherwise be claimed by the openai/anthropic/gemini
  // partitions too. Route them exclusively to the openrouter partition.
  const isOpenRouter = haystack.includes("openrouter");
  if (PROVIDER === "openrouter") return isOpenRouter;
  if (isOpenRouter) return false;
  // Bedrock Mantle rows (model "bedrock_mantle/...") contain the substring "bedrock", so they'd
  // otherwise be claimed by the bedrock partition too. Route them exclusively to bedrock_mantle.
  const isMantle = haystack.includes("bedrock_mantle") || haystack.includes("bedrock-mantle");
  if (PROVIDER === "bedrock_mantle") return isMantle;
  if (isMantle) return false;
  // Vertex rows run Gemini models (model "vertex/gemini-..."), so they'd otherwise be claimed
  // by the gemini partition too - same collision class as openrouter/bedrock_mantle above.
  // Route them exclusively to vertex.
  const isVertex = PROVIDER_KEYWORDS.vertex.some((k) => haystack.includes(k));
  if (PROVIDER === "vertex") return isVertex;
  if (isVertex && (PROVIDER === "gemini" || PROVIDER === "anthropic")) return false;
  // bedrock_openai rows (token-parity-matrix.mjs's "one more model per provider" addition -
  // gpt-oss-family models on Bedrock) contain "openai" in the backend key/model, so they'd
  // otherwise be claimed by the openai partition too - same collision class as above. Route
  // them exclusively to bedrock (they already match PROVIDER_KEYWORDS.bedrock's "bedrock"
  // keyword with no extra logic needed for that direction).
  const isBedrockOpenai = haystack.includes("bedrock_openai");
  if (isBedrockOpenai && PROVIDER === "openai") return false;
  return keywords.some((k) => haystack.includes(k));
};

const itemMatchesFeature = (item, ancestorNames) => {
	if (!FEATURE_PARTS.length) return true;
	const haystack = buildHaystack(item, ancestorNames);
	return FEATURE_PARTS.every((p) => matchesKeyword(item, ancestorNames, haystack, p));
};

const itemMatchesFeatureAny = (item, ancestorNames) => {
	if (!FEATURE_ANY_PARTS.length) return true;
	const haystack = buildHaystack(item, ancestorNames);
	return FEATURE_ANY_PARTS.some((p) => matchesKeyword(item, ancestorNames, haystack, p));
};

// Matched against ancestor folder names specifically (not the whole item body) so a request
// whose prompt text happens to mention a folder's name doesn't get pulled in from elsewhere.
const itemMatchesFolder = (item, ancestorNames) => {
	if (!FOLDER) return true;
	return ancestorNames.some((name) => name.toLowerCase().includes(FOLDER));
};

// Keyed on "<immediate parent folder> <request name>", never on the name
// alone: the criss-cross rows are named after their model, so bare
// "gemini/gemini-2.5-flash" appears 25 times across the collection and a
// name-only match would drag in two dozen unintended requests per pick.
let smokeKeys = null;
const smokeKey = (folder, name) => `${folder || ""} ${name}`;
const itemMatchesSmoke = (item, ancestorNames) => {
  if (!SMOKE) return true;
  if (smokeKeys === null) {
    if (!existsSync(SMOKE)) {
      console.error(`[filter-collection] --smoke manifest not found: ${SMOKE}`);
      process.exit(2);
    }
    const manifest = JSON.parse(readFileSync(SMOKE, "utf8"));
    smokeKeys = new Set();
    for (const pillar of manifest.pillars || []) {
      for (const req of pillar.requests || []) smokeKeys.add(smokeKey(req.folder, req.name));
    }
    console.error(`[filter-collection] smoke: ${smokeKeys.size} folder+name key(s) from ${SMOKE}`);
  }
  return smokeKeys.has(smokeKey(ancestorNames[ancestorNames.length - 1], item.name));
};

// --rerun-rate-limited selects ONLY the items whose prior execution came back 429. It is the
// retry pass's selector, and is deliberately narrower than --rerun-failed: replaying a shard's
// assertion failures would burn quota re-confirming a real defect and would turn a deterministic
// failure into a flaky-looking one. Pairs with rate-limit-backoff.mjs, which decides the wait.
let rateLimitedNameSet = null;
const itemMatchesRateLimited = (item) => {
  if (!RERUN_RATE_LIMITED) return true;
  if (rateLimitedNameSet === null) {
    if (!existsSync(REPORT)) {
      console.error(`[filter-collection] --rerun-rate-limited requires ${REPORT}`);
      process.exit(2);
    }
    rateLimitedNameSet = rateLimitedNames(readReport(REPORT));
    console.error(
      `[filter-collection] rerun-rate-limited: ${rateLimitedNameSet.size} rate-limited item(s) from ${REPORT}`
    );
  }
  return rateLimitedNameSet.has(item.name);
};

let failedNames = null;
const itemMatchesRerunFailed = (item) => {
  if (!RERUN_FAILED) return true;
  if (failedNames === null) {
    if (!existsSync(REPORT)) {
      console.error(`[filter-collection] --rerun-failed requires ${REPORT}`);
      process.exit(2);
    }
    const r = readReport(REPORT);
    failedNames = new Set();
    for (const e of r.run?.executions || []) {
      const code = e.response?.code ?? 0;
      const failed = (e.assertions || []).some((a) => !!a.error) || code === 0 || code >= 400 || !e.response;
      if (failed && e.item?.name) failedNames.add(e.item.name);
    }
    console.error(`[filter-collection] rerun-failed: ${failedNames.size} failed item(s) from prior run`);
  }
  return failedNames.has(item.name);
};

const itemIsExcluded = (item, ancestorNames) => {
  if (!EXCLUDE_FEATURE_ANY_PARTS.length) return false;
  const haystack = buildHaystack(item, ancestorNames);
  return EXCLUDE_FEATURE_ANY_PARTS.some((p) => matchesKeyword(item, ancestorNames, haystack, p));
};

const passes = (item, ancestorNames) => {
  if (!item.request) return true; // folders pass; we filter their items below
  if (itemIsExcluded(item, ancestorNames)) return false;
  return itemMatchesProvider(item, ancestorNames) &&
    itemMatchesFeature(item, ancestorNames) &&
    itemMatchesFeatureAny(item, ancestorNames) &&
    itemMatchesFolder(item, ancestorNames) &&
    itemMatchesClass(item, ancestorNames) &&
    itemMatchesSmoke(item, ancestorNames) &&
    itemMatchesRateLimited(item) &&
    itemMatchesRerunFailed(item);
};

const filterTree = (items, keep) => {
  const out = [];
  for (const item of items) {
    if (Array.isArray(item.item)) {
      const kids = filterTree(item.item, keep);
      if (kids.length > 0) out.push({ ...item, item: kids });
    } else if (keep.has(item)) {
      out.push(item);
    }
  }
  return out;
};

// Chained requests: a body that drops in {{var}} is only valid if the request whose test script
// sets that variable ran EARLIER IN THE SAME newman process - collection variables do not survive
// across invocations. Every filter here can otherwise select a consumer without its producer:
// --rerun-failed most obviously (a failed round 2 is rerun alone, and fails again with 400
// "Invalid JSON" for a reason that has nothing to do with the defect being rerun), but also
// --provider/--feature slicing whenever the two ends of a chain match differently. So after the
// user's predicates decide the initial set, pull in each selected item's producers transitively.
// Collection order is untouched, and producers already precede consumers there.
const expandWithProducers = (selected, entries) => {
  const producerIndex = buildProducerIndex(entries);

  const keep = new Set(selected);
  const queue = [...selected];
  const pulled = [];
  while (queue.length) {
    const item = queue.pop();
    // producerItem, not a by-name lookup: request names repeat throughout this
    // collection, so resolving through the name can hand back a same-named
    // request that sets nothing while the actual producer is never pulled in -
    // leaving the consumer to fail on an unsubstituted {{var}}, which is the
    // failure this whole function exists to prevent.
    for (const { producer, producerItem: dep, variable } of chainedDependencies(item, producerIndex)) {
      if (!dep || keep.has(dep)) continue;
      keep.add(dep);
      queue.push(dep);
      pulled.push(`${item.name} <- ${variable} <- ${producer}`);
    }
  }
  if (pulled.length) {
    console.error(`[filter-collection] pulled in ${pulled.length} prerequisite request(s) for chained variables:`);
    for (const line of pulled) console.error(`  ${line}`);
  }
  return keep;
};

const collection = JSON.parse(readFileSync(SOURCE, "utf8"));
const entries = walkRequests(collection.item || []);
const matched = entries.filter(({ item, ancestors }) => passes(item, ancestors)).map(({ item }) => item);
// Sliced BEFORE expandWithProducers, never after: a chained consumer that lands in slice 3 still
// needs its producer to run inside slice 3's newman process, because collection variables do not
// survive across invocations. Slicing afterwards would cut producers away from the consumers that
// were just given them. The cost is that a producer shared by consumers in two slices runs in
// both, which is the same duplication --class sharding already accepts for the same reason.
const selected = SHARD_COUNT
  ? matched.filter((_, i) => i % SHARD_COUNT === SHARD_INDEX - 1)
  : matched;
const keep = expandWithProducers(selected, entries);
const filtered = { ...collection, item: filterTree(collection.item || [], keep) };
const totalAfter = JSON.stringify(filtered).match(/"request":/g)?.length || 0;
writeFileSync(OUT, JSON.stringify(filtered, null, 2));
console.error(`[filter-collection] wrote ${OUT} with ${totalAfter} requests after filter (provider=${PROVIDER || "-"}, feature=${FEATURE_PARTS.join("+") || "-"}, feature-any=${FEATURE_ANY_PARTS.join("|") || "-"}, class=${CLASS || "-"}, shard=${SHARD_COUNT ? `${SHARD_INDEX}/${SHARD_COUNT} of ${matched.length}` : "-"}, smoke=${SMOKE || "-"}, rerun-failed=${RERUN_FAILED})`);
