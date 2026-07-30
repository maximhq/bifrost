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
const RERUN_FAILED = args["rerun-failed"] === "true";
const REPORT = args.report || "tmp/newman-report.json";

if (!SOURCE || !OUT) {
  console.error("[filter-collection] --source and --out are required");
  process.exit(2);
}
if (!PROVIDER && !FEATURE_PARTS.length && !FEATURE_ANY_PARTS.length && !RERUN_FAILED) {
  console.error("[filter-collection] need at least one of: --provider, --feature, --feature-any, --rerun-failed");
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
const buildHaystack = (item, ancestorNames) =>
  (JSON.stringify(item) + " " + ancestorNames.join(" ")).toLowerCase();

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

let failedNames = null;
const itemMatchesRerunFailed = (item) => {
  if (!RERUN_FAILED) return true;
  if (failedNames === null) {
    if (!existsSync(REPORT)) {
      console.error(`[filter-collection] --rerun-failed requires ${REPORT}`);
      process.exit(2);
    }
    const r = JSON.parse(readFileSync(REPORT, "utf8"));
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

const passes = (item, ancestorNames) => {
  if (!item.request) return true; // folders pass; we filter their items below
  return itemMatchesProvider(item, ancestorNames) &&
    itemMatchesFeature(item, ancestorNames) &&
    itemMatchesFeatureAny(item, ancestorNames) &&
    itemMatchesRerunFailed(item);
};

const filterTree = (items, ancestorNames = []) => {
  const out = [];
  for (const item of items) {
    if (Array.isArray(item.item)) {
      const kids = filterTree(item.item, [...ancestorNames, item.name || ""]);
      if (kids.length > 0) out.push({ ...item, item: kids });
    } else if (passes(item, ancestorNames)) {
      out.push(item);
    }
  }
  return out;
};

const collection = JSON.parse(readFileSync(SOURCE, "utf8"));
const filtered = { ...collection, item: filterTree(collection.item || []) };
const totalAfter = JSON.stringify(filtered).match(/"request":/g)?.length || 0;
writeFileSync(OUT, JSON.stringify(filtered, null, 2));
console.error(`[filter-collection] wrote ${OUT} with ${totalAfter} requests after filter (provider=${PROVIDER || "-"}, feature=${FEATURE_PARTS.join("+") || "-"}, feature-any=${FEATURE_ANY_PARTS.join("|") || "-"}, rerun-failed=${RERUN_FAILED})`);
