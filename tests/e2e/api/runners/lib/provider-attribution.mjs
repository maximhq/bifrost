// Which provider does a harness row belong to?
//
// This has to answer the same question twice from two different views of the
// same request, and the two answers must agree:
//
//   denominator - the collection on disk, where URLs still carry {{variables}}
//   numerator   - newman's log, where those variables have been substituted
//
// A signal that reads differently in those two views silently splits a provider
// in half: its rows are counted under one name and its passes recorded under
// another, so the table shows a provider stuck below its own total forever
// while another reports more passes than it has requests.
//
// Extracted from harness-monitor.mjs so it can be tested directly; the keyword
// table mirrors filter-collection.mjs.

export const PROVIDER_KEYWORDS = {
  openai: ["openai", "/openai", "gpt-", "o3", "o1"],
  anthropic: ["anthropic", "claude-"],
  bedrock: ["bedrock", "/bedrock"],
  bedrock_mantle: ["bedrock_mantle", "bedrock-mantle"],
  gemini: ["gemini", "/genai", "googlesearch"],
  vertex: ["vertex", "/genai/v1beta/models/{{vertexmodel}}"],
  azure: ["azure", "deployments"],
  passthrough: ["_passthrough", "passthrough"],
  openrouter: ["openrouter"],
  replicate: ["replicate", "/replicate", "flux", "black-forest-labs"],
};

// filter-collection asks "does this item match provider P?" once per fork, so an
// item may legitimately land in several partitions. Sequential attribution has to
// pick exactly ONE owner, so keyword order becomes load-bearing: match most
// specific first, or a vertex row (whose URL contains "/genai") is claimed by
// gemini, and a bedrock_mantle row by bedrock.
export const MATCH_ORDER = [
  "passthrough",
  "openrouter",
  "replicate",
  "vertex",
  "azure",
  "bedrock_mantle",
  "bedrock",
  "gemini",
  "anthropic",
  "openai",
];

// Long base64 runs are media payloads, not searchable text; a 2-char keyword like
// "o1" hits inside them by chance and mis-assigns the row. Same guard as
// filter-collection.mjs buildHaystack.
export const stripBase64Blobs = (s) => s.replace(/[A-Za-z0-9+/]{40,}={0,2}/g, "");

// makeMatchProvider builds a keyword matcher restricted to the providers this
// run actually tracks, so a keyword cannot assign a row to a provider that has
// no column in the table.
export function makeMatchProvider(isActive) {
  return function matchProvider(haystack) {
    for (const p of MATCH_ORDER) {
      if (!isActive(p)) continue;
      for (const k of PROVIDER_KEYWORDS[p] || [p]) if (haystack.includes(k)) return p;
    }
    return null;
  };
}

// providerOfFolder attributes from a folder heading.
//
// This is the one signal that reads identically on both sides: newman echoes
// folder names verbatim on its "❏ <name>" lines, and the collection carries the
// same string. Nothing in a folder name is variable-substituted, so it cannot
// drift the way a URL does. Only the LEAF name is used, because a leaf is all
// newman prints - matching the full ancestor path here would reintroduce the
// very asymmetry this exists to remove.
export function providerOfFolder(folderName, matchProvider) {
  if (!folderName) return null;
  return matchProvider(String(folderName).toLowerCase());
}

// providerOfItem attributes a collection leaf (the denominator side).
//
// Folder first, for the reason above. The URL is only a fallback now: it is the
// signal that disagrees across substitution, and a Vertex row is exactly where
// that bites - its raw URL matches the vertex keyword through the literal
// {{vertexModel}}, while the substituted URL newman logs matches gemini through
// "/genai" instead.
//
// The request name is tried next: cross-cut rows route through a unified
// /v1/chat/completions that names no provider, and the name
// ("openai/gpt-4o-mini ...") is the only other thing the log prints.
//
// Item JSON + ancestor folder names is the last resort - rows the log cannot
// attribute at all. Those inflate a denominator the numerator can never reach,
// which is the safe direction to be wrong in: the table under-reports progress
// rather than over-reporting passes.
export function providerOfItem(item, ancestors, matchProvider) {
  const req = item.request || {};
  const url = (typeof req.url === "string" ? req.url : req.url?.raw) || "";
  const leafFolder = ancestors.length ? ancestors[ancestors.length - 1] : "";
  return (
    providerOfFolder(leafFolder, matchProvider) ||
    matchProvider(url.toLowerCase()) ||
    matchProvider(String(item.name || "").toLowerCase()) ||
    matchProvider(stripBase64Blobs(JSON.stringify(item) + " " + ancestors.join(" ")).toLowerCase())
  );
}

// providerOfLogLine attributes a newman output line (the numerator side).
//
// currentFolder is the last "❏ <name>" heading seen. When it resolves it wins,
// which is what keeps this in step with providerOfItem above; the line's own
// text is the fallback for rows that sit under an unattributable heading.
export function providerOfLogLine(line, currentFolder, matchProvider) {
  return providerOfFolder(currentFolder, matchProvider) || matchProvider(line.toLowerCase());
}
