#!/usr/bin/env node
// Filters a Postman collection by provider, feature keyword, or "rerun failed"
// from a prior newman report. Writes the filtered collection to --out.
//
// Usage:
//   node filter-collection.mjs --source path.json --out /tmp/x.json --provider anthropic
//   node filter-collection.mjs --source path.json --out /tmp/x.json --feature "web search"
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
const FEATURE = (args.feature || "").toLowerCase();
const RERUN_FAILED = args["rerun-failed"] === "true";
const REPORT = args.report || "tmp/newman-report.json";

if (!SOURCE || !OUT) {
  console.error("[filter-collection] --source and --out are required");
  process.exit(2);
}
if (!PROVIDER && !FEATURE && !RERUN_FAILED) {
  console.error("[filter-collection] need at least one of: --provider, --feature, --rerun-failed");
  process.exit(2);
}

const PROVIDER_KEYWORDS = {
  openai: ["openai", "/openai", "gpt-", "o3", "o1"],
  anthropic: ["anthropic", "claude-"],
  bedrock: ["bedrock", "/bedrock"],
  gemini: ["gemini", "/genai", "googlesearch"],
  vertex: ["vertex", "/genai/v1beta/models/{{vertexModel}}"],
  azure: ["azure", "deployments"],
  passthrough: ["_passthrough"],
};

const itemMatchesProvider = (item) => {
  if (!PROVIDER) return true;
  const keywords = PROVIDER_KEYWORDS[PROVIDER] || [PROVIDER];
  const haystack = JSON.stringify(item).toLowerCase();
  return keywords.some((k) => haystack.includes(k));
};

const itemMatchesFeature = (item) => {
  if (!FEATURE) return true;
  const haystack = JSON.stringify(item).toLowerCase();
  return haystack.includes(FEATURE);
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

const passes = (item) => {
  if (!item.request) return true; // folders pass; we filter their items below
  return itemMatchesProvider(item) && itemMatchesFeature(item) && itemMatchesRerunFailed(item);
};

const filterTree = (items) => {
  const out = [];
  for (const item of items) {
    if (Array.isArray(item.item)) {
      const kids = filterTree(item.item);
      if (kids.length > 0) out.push({ ...item, item: kids });
    } else if (passes(item)) {
      out.push(item);
    }
  }
  return out;
};

const collection = JSON.parse(readFileSync(SOURCE, "utf8"));
const filtered = { ...collection, item: filterTree(collection.item || []) };
const totalAfter = JSON.stringify(filtered).match(/"request":/g)?.length || 0;
writeFileSync(OUT, JSON.stringify(filtered, null, 2));
console.error(`[filter-collection] wrote ${OUT} with ${totalAfter} requests after filter (provider=${PROVIDER || "-"}, feature=${FEATURE || "-"}, rerun-failed=${RERUN_FAILED})`);
