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

import { readFileSync, writeFileSync, existsSync } from "node:fs";
import { readReport } from "./lib/read-report.mjs";
import { buildHaystack } from "./lib/haystack.mjs";
import { walkRequests, buildProducerIndex, bodyDependencies } from "./lib/chained-vars.mjs";

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
const RERUN_FAILED = args["rerun-failed"] === "true";
const REPORT = args.report || "tmp/newman-report.json";

if (!SOURCE || !OUT) {
  console.error("[filter-collection] --source and --out are required");
  process.exit(2);
}
if (!PROVIDER && !FEATURE_PARTS.length && !FEATURE_ANY_PARTS.length && !EXCLUDE_FEATURE_ANY_PARTS.length && !FOLDER && !RERUN_FAILED) {
  console.error(
    "[filter-collection] need at least one of: --provider, --feature, --feature-any, --exclude-feature-any, --folder, --rerun-failed"
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
    for (const { producer, producerItem: dep, variable } of bodyDependencies(item, producerIndex)) {
      if (!dep || keep.has(dep)) continue;
      keep.add(dep);
      queue.push(dep);
      pulled.push(`${item.name} <- ${variable} <- ${producer}`);
    }
  }
  if (pulled.length) {
    console.error(`[filter-collection] pulled in ${pulled.length} prerequisite request(s) for chained bodies:`);
    for (const line of pulled) console.error(`  ${line}`);
  }
  return keep;
};

const collection = JSON.parse(readFileSync(SOURCE, "utf8"));
const entries = walkRequests(collection.item || []);
const selected = entries.filter(({ item, ancestors }) => passes(item, ancestors)).map(({ item }) => item);
const keep = expandWithProducers(selected, entries);
const filtered = { ...collection, item: filterTree(collection.item || [], keep) };
const totalAfter = JSON.stringify(filtered).match(/"request":/g)?.length || 0;
writeFileSync(OUT, JSON.stringify(filtered, null, 2));
console.error(`[filter-collection] wrote ${OUT} with ${totalAfter} requests after filter (provider=${PROVIDER || "-"}, feature=${FEATURE_PARTS.join("+") || "-"}, feature-any=${FEATURE_ANY_PARTS.join("|") || "-"}, rerun-failed=${RERUN_FAILED})`);
