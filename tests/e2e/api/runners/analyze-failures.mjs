#!/usr/bin/env node
// Categorizes failures from a newman provider-harness run and emits a markdown
// report suitable for CI artifact upload or eyeball triage.
//
// Categories:
//   provider_not_configured | model_not_found | auth_invalid | rate_limit
//   request_shape_mismatch  | network_or_timeout | unknown
//
// Usage:
//   node analyze-failures.mjs --report tmp/newman-report.json \
//     --bifrost-log tmp/bifrost-dev.log --out tmp/harness-failures.md

import { readFileSync, writeFileSync, existsSync } from "node:fs";

const args = Object.fromEntries(
  process.argv.slice(2).reduce((acc, cur, i, arr) => {
    if (cur.startsWith("--")) acc.push([cur.slice(2), arr[i + 1]]);
    return acc;
  }, [])
);
const REPORT = args.report || "tmp/newman-report.json";
const BIFROST_LOG = args["bifrost-log"] || "tmp/bifrost-dev.log";
const OUT = args.out || "tmp/harness-failures.md";

if (!existsSync(REPORT)) {
  console.error(`[analyze-failures] report not found: ${REPORT}`);
  process.exit(1);
}

const FIX_HINTS = {
  provider_not_configured: "Add the provider in `tests/integrations/python/config.json` (the APP_DIR config used by the harness) with a valid key/region. Restart `make dev` so Bifrost picks it up.",
  model_not_found: "Update the model identifier in the collection - check the upstream provider's `/models` endpoint or docs for the canonical id at the time of testing. Common cause: model names in the harness are 2026-vintage placeholders that may not be deployed in your provider account yet.",
  auth_invalid: "Re-source secrets via `INFISICAL=1` or update `.env`; verify the key matches the project/path your Bifrost config references. If the key is correct, check the provider account's quota/restrictions.",
  rate_limit: "Backoff/retry; not a harness bug. Either reduce concurrency, lift quota, or rerun later.",
  request_shape_mismatch: "Inspect the response body for the schema error and adjust the request JSON in the collection. Bedrock Converse and GenAI generateContent schemas are strict.",
  network_or_timeout: "Check Bifrost is still up (`curl /health`) and that the upstream provider isn't blocking egress. Also possible: the upstream provider is unreachable from your network.",
  unknown: "Open a bug; attach the bifrost-dev.log excerpt below. Check the response body for a more specific error string.",
};

const bufToString = (b) => {
  if (!b) return "";
  if (typeof b === "string") return b;
  if (b.type === "Buffer" && Array.isArray(b.data)) return Buffer.from(b.data).toString("utf8");
  return String(b);
};

const truncate = (s, n) => {
  if (!s) return "";
  return s.length > n ? s.slice(0, n) + "...(truncated)" : s;
};

const categorize = (code, body, bifrostLines) => {
  // Check body content first - Bifrost's "failed to get config for provider" can manifest as
  // 400 OR 500 depending on the integration, so the body string is more reliable than the status.
  const bodyLower = (body || "").toLowerCase();
  if (
    bodyLower.includes("failed to get config for provider") ||
    bodyLower.includes("no provider config") ||
    bodyLower.includes("provider not configured") ||
    bodyLower.includes("provider is not registered") ||
    bodyLower.includes("missing api key") ||
    bodyLower.includes("no api key configured")
  ) {
    return "provider_not_configured";
  }

  if (code === 0 || code == null) return "network_or_timeout";
  if (code === 401 || code === 403) return "auth_invalid";
  if (code === 429) return "rate_limit";

  // 404 from Anthropic ("type":"not_found_error" with model: ...) is a real model-name miss.
  if (code === 404) {
    if (bodyLower.includes("not_found_error") || bodyLower.includes("model")) {
      return "model_not_found";
    }
    return "model_not_found";
  }

  if (code === 400) {
    if (bodyLower.includes("does not exist") || bodyLower.includes("invalid model") || bodyLower.includes("unsupported model")) {
      return "model_not_found";
    }
    return "request_shape_mismatch";
  }

  if (code >= 500 && code < 600) return "unknown";
  return "unknown";
};

const reconstructUrl = (urlObj) => {
  if (!urlObj) return "";
  if (typeof urlObj === "string") return urlObj;
  if (urlObj.raw) return urlObj.raw;
  const protocol = urlObj.protocol || "http";
  const host = Array.isArray(urlObj.host) ? urlObj.host.join(".") : urlObj.host || "";
  const port = urlObj.port ? `:${urlObj.port}` : "";
  const path = Array.isArray(urlObj.path) ? "/" + urlObj.path.join("/") : urlObj.path || "";
  const query = Array.isArray(urlObj.query) && urlObj.query.length
    ? "?" + urlObj.query.filter((q) => !q.disabled).map((q) => `${q.key}=${q.value}`).join("&")
    : "";
  return `${protocol}://${host}${port}${path}${query}`;
};

// Walk the collection tree to build a request-id -> folder-path map.
// Falls back to request-name -> folder-path if id is missing.
const buildFolderMap = (collection) => {
  const byId = new Map();
  const byName = new Map();
  const walk = (items, trail) => {
    for (const item of items || []) {
      if (item.item) {
        walk(item.item, [...trail, item.name]);
      } else {
        const folder = trail.join(" / ");
        if (item.id) byId.set(item.id, folder);
        if (item.name) byName.set(item.name, folder);
      }
    }
  };
  walk(collection?.item || [], []);
  return { byId, byName };
};

const grepBifrostForUrl = (logText, url) => {
  if (!logText || !url) return [];
  let path = url;
  try { path = new URL(url).pathname; } catch { /* not a parseable URL, leave as-is */ }
  if (!path) return [];
  const errorIndicators = /"level":"(error|warn)"|http\.status_code":[45]\d\d|"error"|failed|panic/i;
  const lines = logText.split(/\r?\n/);
  const matched = [];
  for (const line of lines) {
    if (line.includes(path) && errorIndicators.test(line)) {
      matched.push(line);
      if (matched.length >= 3) break;
    }
  }
  return matched;
};

// Providers tracked in the coverage matrix (column order is preserved).
const COVERAGE_PROVIDERS = ["OpenAI", "Anthropic", "Bedrock", "Gemini", "Vertex", "Azure"];

// Routes tracked in the coverage matrix:
//   "drop-in"     — provider-native shape via /openai, /anthropic, /bedrock, /genai
//   "cross-model" — unified /v1/chat/completions or /v1/responses with provider/model prefix
//   "passthrough" — /*_passthrough/* catch-all forwarding
const COVERAGE_ROUTES = ["drop-in", "cross-model", "passthrough"];

// Comprehensive feature list, derived from Anthropic's Messages API surface.
// Every row is something at least one provider could support; "—" cells are
// genuine coverage gaps. Order is roughly: core -> tools -> beta features -> meta.
const COVERAGE_FEATURES = [
  // Core conversation
  "Basic Chat",
  "System Message",
  "Multi-turn",
  "Streaming",
  "Stop Sequences",
  "Sampling Params (temperature/top_p)",
  // Tools
  "Function Calling",
  "Tool Choice (forced)",
  "Parallel Tool Calls",
  "Tool Search",
  "MCP Toolset",
  // Server tools
  "Web Search (basic)",
  "Web Search (dynamic filtering)",
  "Web Search (domain filter)",
  "Web Search (user location)",
  "Web Fetch",
  "Code Execution",
  "Code Interpreter",
  "File Search",
  "Computer Use",
  "Text Editor Tool",
  "Bash Tool",
  "Memory Tool",
  // Multimodal
  "Vision (image)",
  "PDF Input",
  "Audio Input",
  "Citations",
  // Reasoning / thinking
  "Extended Thinking (budget_tokens)",
  "Adaptive Thinking",
  "Reasoning Effort (OpenAI)",
  "Thinking Budget (Gemini)",
  // Output controls
  "Structured Output (json_schema)",
  "Response Format (mime_type)",
  // Caching / efficiency
  "Prompt Caching (ephemeral)",
  "Prompt Caching (persistent/1h)",
  "Eager Input Streaming",
  // Beta / advanced
  "Anthropic Beta Header (any)",
  "Defer Loading",
  "Allowed Callers",
  "Interleaved Thinking",
  "Context Management",
  "Skills / Container",
  "Service Tier",
  "Safety Settings (Gemini)",
  // Endpoints / batch
  "Token Counting",
  "Batch API",
  "Files API",
];

// detectProvider returns the canonical provider name for a request based on its
// folder hierarchy, URL path, and model field. Returns "" if undetectable.
const detectProvider = (folder, url, body) => {
  const haystack = (folder + " " + url + " " + body).toLowerCase();
  // Folder-based detection (most reliable). Vertex must come BEFORE Gemini
  // because Vertex feature variations live under "/genai" but their folder
  // name contains "Vertex" - we want that to win over the loose "genai" match.
  if (/azure/i.test(folder)) return "Azure";
  if (/vertex/i.test(folder)) return "Vertex";
  if (/openai features|openai drop-in|^.*\/ openai|\bopenai\b/i.test(folder)) return "OpenAI";
  if (/anthropic features|anthropic drop-in|anthropic \(/i.test(folder)) return "Anthropic";
  if (/bedrock features|bedrock drop-in|bedrock \(/i.test(folder)) return "Bedrock";
  if (/gemini.*genai features|google genai drop-in|gemini\b/i.test(folder)) return "Gemini";
  // URL-based detection
  if (url.includes("/openai_passthrough/") || url.includes("/openai/openai/deployments/")) {
    return url.includes("deployments") ? "Azure" : "OpenAI";
  }
  if (url.includes("/anthropic_passthrough/") || url.includes("/anthropic/")) return "Anthropic";
  if (url.includes("/bedrock/")) return "Bedrock";
  if (url.includes("/azure_passthrough/")) return "Azure";
  if (url.includes("/genai_passthrough/") || url.includes("/genai/")) {
    // genai endpoint: classify as Vertex if model name suggests it, else Gemini
    if (/vertex|claude-/.test(haystack) && !/gemini/.test(haystack)) return "Vertex";
    return "Gemini";
  }
  if (url.includes("/openai/")) return "OpenAI";
  // Body model-prefix detection (cross-model section)
  const m = body.match(/"model"\s*:\s*"([^/]+)\//);
  if (m) {
    const prefix = m[1].toLowerCase();
    if (prefix === "openai") return "OpenAI";
    if (prefix === "anthropic") return "Anthropic";
    if (prefix === "bedrock") return "Bedrock";
    if (prefix === "gemini") return "Gemini";
    if (prefix === "vertex") return "Vertex";
    if (prefix === "azure") return "Azure";
  }
  return "";
};

// PROVIDER_FEATURE_COMPAT marks which features each provider can plausibly
// support. A feature missing from the map is treated as "all providers". A
// feature listed here restricts the matrix's "missing" calculation: cells
// outside the listed providers render as "N/A" instead of "—" so the missing
// counts only reflect gaps for features the provider actually has.
const PROVIDER_FEATURE_COMPAT = {
  "Reasoning Effort (OpenAI)": ["OpenAI", "Azure"],
  "Thinking Budget (Gemini)": ["Gemini", "Vertex"],
  "Adaptive Thinking": ["Anthropic", "Bedrock", "Vertex"],
  "Extended Thinking (budget_tokens)": ["Anthropic", "Bedrock", "Vertex"],
  "Anthropic Beta Header (any)": ["Anthropic", "Bedrock", "Vertex"],
  "Defer Loading": ["Anthropic", "Bedrock", "Vertex"],
  "Allowed Callers": ["Anthropic", "Bedrock", "Vertex"],
  "Interleaved Thinking": ["Anthropic", "Bedrock", "Vertex"],
  "Skills / Container": ["Anthropic", "OpenAI", "Azure"],
  "Service Tier": ["Anthropic", "OpenAI", "Azure", "Bedrock", "Vertex"],
  "Eager Input Streaming": ["Anthropic"],
  "Context Management": ["Anthropic", "Bedrock", "Vertex"],
  "Computer Use": ["Anthropic", "Bedrock", "Vertex", "OpenAI"],
  "Text Editor Tool": ["Anthropic", "Bedrock", "Vertex"],
  "Bash Tool": ["Anthropic", "Bedrock", "Vertex"],
  "Memory Tool": ["Anthropic", "Bedrock", "Vertex"],
  "Tool Search": ["Anthropic", "Bedrock", "Vertex"],
  "Web Fetch": ["Anthropic", "Bedrock", "Vertex"],
  "Web Search (dynamic filtering)": ["Anthropic", "Bedrock", "Vertex"],
  "Web Search (domain filter)": ["Anthropic", "Bedrock", "Vertex"],
  "Web Search (user location)": ["Anthropic", "Bedrock", "Vertex"],
  "Web Search (basic)": ["OpenAI", "Anthropic", "Bedrock", "Vertex", "Gemini"],
  "Code Execution": ["Anthropic", "Bedrock", "Vertex", "Gemini"],
  "Code Interpreter": ["OpenAI", "Azure"],
  "File Search": ["OpenAI", "Azure"],
  "Citations": ["Anthropic", "Bedrock", "Vertex"],
  "Prompt Caching (ephemeral)": ["Anthropic", "Bedrock", "Vertex", "OpenAI", "Gemini"],
  "Prompt Caching (persistent/1h)": ["Anthropic", "Bedrock", "Vertex"],
  "MCP Toolset": ["Anthropic", "OpenAI", "Bedrock", "Vertex"],
  "PDF Input": ["Anthropic", "Bedrock", "Vertex", "Gemini", "OpenAI"],
  "Audio Input": ["OpenAI", "Gemini", "Vertex", "Azure"],
  "Safety Settings (Gemini)": ["Gemini", "Vertex"],
  "Response Format (mime_type)": ["Gemini", "Vertex"],
  "Structured Output (json_schema)": ["OpenAI", "Anthropic", "Bedrock", "Vertex", "Azure", "Gemini"],
  "Token Counting": ["Anthropic", "Gemini", "OpenAI"],
  "Batch API": ["Anthropic", "OpenAI", "Bedrock"],
  "Files API": ["Anthropic", "OpenAI", "Gemini"],
};

const isFeatureApplicable = (provider, feature) => {
  const list = PROVIDER_FEATURE_COMPAT[feature];
  if (!list) return true;
  return list.includes(provider);
};

// detectModel extracts the model identifier from a request — checks the JSON
// body's "model" field, then falls back to URL path parameters used by
// Bedrock (/bedrock/model/{id}/...), Gemini/Vertex (/genai/v1beta/models/{id}:...),
// and Azure (/deployments/{name}/...). Returns "(unknown)" if undetectable.
const detectModel = (url, body) => {
  const bm = body.match(/"model"\s*:\s*"([^"]+)"/);
  if (bm) return bm[1];
  const bedrock = url.match(/\/bedrock\/model\/([^/]+)/);
  if (bedrock) return `bedrock/${decodeURIComponent(bedrock[1])}`;
  const genai = url.match(/\/genai(?:_passthrough)?\/v1[^/]*\/models\/([^:?/]+)[:?]/);
  if (genai) return decodeURIComponent(genai[1]);
  const azure = url.match(/\/deployments\/([^/]+)\//);
  if (azure) return `azure/${azure[1]}`;
  return "(unknown)";
};

// detectRoute classifies the transport path used:
//   drop-in     — provider-shape native endpoints
//   cross-model — Bifrost's unified /v1/chat/completions or /v1/responses
//   passthrough — /*_passthrough/* catch-all forwarding
const detectRoute = (url) => {
  if (/_passthrough\//.test(url)) return "passthrough";
  // Cross-model uses /v1/chat/completions or /v1/responses without a /openai|/anthropic|/bedrock|/genai prefix.
  if (/\/(v1\/chat\/completions|v1\/responses)(?!\/)/.test(url) && !/\/(openai|anthropic|bedrock|genai|azure)\//.test(url)) {
    return "cross-model";
  }
  return "drop-in";
};

// detectFeature returns ALL features a request exercises. Multi-tag is
// intentional: a "Function Calling + Streaming" request shows up in both
// columns of the matrix so each feature's coverage count reflects real usage.
// Returns an array of feature names (subset of COVERAGE_FEATURES).
const detectFeatures = (folder, name, body, headers) => {
  const tags = new Set();
  const folderLow = folder.toLowerCase();
  const nameLow = name.toLowerCase();
  const hay = (folderLow + " " + nameLow + " " + body).toLowerCase();
  const headerHay = (headers || []).map((h) => `${h.key}:${h.value}`).join(" ").toLowerCase();

  // Tools (server-side)
  if (/computer_2025[01]124/.test(hay) || /\bcomputer use\b/.test(hay)) tags.add("Computer Use");
  if (/text_editor_2025\d+|str_replace_based_edit_tool|str_replace_editor/.test(hay)) tags.add("Text Editor Tool");
  if (/\bbash_2025\d+/.test(hay) || /\bbash tool\b/.test(hay)) tags.add("Bash Tool");
  if (/memory_2025\d+|\bmemory tool\b/.test(hay)) tags.add("Memory Tool");
  if (/tool_search_/.test(hay)) tags.add("Tool Search");
  if (/mcp_toolset|mcp_servers|"servers":/.test(hay)) tags.add("MCP Toolset");

  // Web search variants
  if (/web_search_20260209|dynamic filtering/.test(hay)) tags.add("Web Search (dynamic filtering)");
  if (/web_search_20250305|web_search_preview|googlesearch|"google_search"/.test(hay)) tags.add("Web Search (basic)");
  if (/allowed_domains|blocked_domains/.test(hay)) tags.add("Web Search (domain filter)");
  if (/user_location/.test(hay)) tags.add("Web Search (user location)");
  if (/web_fetch_/.test(hay)) tags.add("Web Fetch");

  // Code-related tools
  if (/codeexecution|code_execution_|"code_execution"/.test(hay)) tags.add("Code Execution");
  if (/code_interpreter|code interpreter/.test(hay)) tags.add("Code Interpreter");
  if (/"file_search"|file search/.test(hay)) tags.add("File Search");

  // Multimodal
  if (/image_url|"type":\s*"image"|inline_data|filedata|\bvision\b/.test(hay)) tags.add("Vision (image)");
  if (/"type":\s*"document"|application\/pdf|\bpdf\b/.test(hay)) tags.add("PDF Input");
  if (/audio.input|"input_audio"|"type":\s*"audio"/.test(hay)) tags.add("Audio Input");
  if (/citations|cited_text/.test(hay)) tags.add("Citations");

  // Reasoning / thinking
  if (/"thinking":\s*\{[^}]*"type":\s*"enabled"|budget_tokens/.test(hay)) tags.add("Extended Thinking (budget_tokens)");
  if (/"thinking":\s*\{[^}]*"type":\s*"adaptive"|adaptive thinking/.test(hay)) tags.add("Adaptive Thinking");
  if (/reasoning_effort|reasoning effort/.test(hay)) tags.add("Reasoning Effort (OpenAI)");
  if (/thinkingbudget|thinking_budget/.test(hay)) tags.add("Thinking Budget (Gemini)");

  // Output shape
  if (/"json_schema"|"response_format":\s*\{[^}]*json_schema/.test(hay)) tags.add("Structured Output (json_schema)");
  if (/responsemimetype|response_mime_type|responseschema|response_schema/.test(hay)) tags.add("Response Format (mime_type)");

  // Caching
  if (/cache_control[^}]*"ephemeral"|"type":\s*"ephemeral"/.test(hay)) tags.add("Prompt Caching (ephemeral)");
  if (/cache_control[^}]*"persistent"|"ttl":\s*"1h"|cache.*1.hour/.test(hay)) tags.add("Prompt Caching (persistent/1h)");

  // Streaming
  if (/"stream":\s*true|streamgeneratecontent|"alt":\s*"sse"/.test(hay)) tags.add("Streaming");
  if (/eager_input_streaming|eager.input.streaming/.test(hay)) tags.add("Eager Input Streaming");

  // Tool-use mechanics
  if (/"type":\s*"function"|functiondeclarations|function_declaration|"toolconfig":/.test(hay)) tags.add("Function Calling");
  if (/"tool_choice":\s*\{|"tool_choice":\s*"required"|"tool_choice":\s*"any"|forced/.test(hay)) tags.add("Tool Choice (forced)");
  if (/parallel_tool_calls/.test(hay)) tags.add("Parallel Tool Calls");

  // Sampling params
  if (/"temperature":|"top_p":|"top_k":/.test(hay)) tags.add("Sampling Params (temperature/top_p)");
  if (/stop_sequences|"stop":/.test(hay)) tags.add("Stop Sequences");

  // Anthropic beta / advanced
  if (/anthropic-beta/.test(headerHay) || /"betas":\s*\[/.test(hay)) tags.add("Anthropic Beta Header (any)");
  if (/defer_loading/.test(hay)) tags.add("Defer Loading");
  if (/allowed_callers/.test(hay)) tags.add("Allowed Callers");
  if (/interleaved.thinking|interleaved-thinking/.test(hay)) tags.add("Interleaved Thinking");
  if (/context.management|context-management|context-1m|compact-/.test(hay)) tags.add("Context Management");
  if (/"skill":|skills.*container|"container":\s*\{/.test(hay)) tags.add("Skills / Container");
  if (/"service_tier"|priority/.test(hay)) tags.add("Service Tier");

  // Provider-specific
  if (/safetysettings|safety_settings|harm_category/.test(hay)) tags.add("Safety Settings (Gemini)");

  // Endpoints / batches / files
  if (/count_tokens|input_tokens.*endpoint/.test(hay)) tags.add("Token Counting");
  if (/\/batches|batch.api/.test(hay)) tags.add("Batch API");
  if (/\/files|files.api/.test(hay)) tags.add("Files API");

  // Conversation structure
  if (/system|"role":\s*"system"|systeminstruction/.test(hay)) tags.add("System Message");
  if (/multi.turn|"role":\s*"assistant"/.test(hay)) tags.add("Multi-turn");

  // Ensure every request gets at least one tag — falls through to Basic Chat.
  if (tags.size === 0) tags.add("Basic Chat");
  return [...tags];
};

// buildCoverageMatrix walks every execution and tags it with (provider, route, features...).
// Each request can map to multiple features (multi-tag), so cell totals can exceed request count.
// Returns:
//   byProvider: {provider: {feature: {total, passed, failed}}}
//   byRoute:    {route:    {feature: {total, passed, failed}}}
//   untagged:   array of items where provider couldn't be determined
const buildCoverageMatrix = (execs, folderMap) => {
  const byProvider = {};
  for (const p of COVERAGE_PROVIDERS) {
    byProvider[p] = {};
    for (const f of COVERAGE_FEATURES) byProvider[p][f] = { total: 0, passed: 0, failed: 0 };
  }
  const byRoute = {};
  for (const r of COVERAGE_ROUTES) {
    byRoute[r] = {};
    for (const f of COVERAGE_FEATURES) byRoute[r][f] = { total: 0, passed: 0, failed: 0 };
  }
  // byModel: { "Provider / model-id": { feature: {total, passed, failed} } }
  // Lazily initialized as we discover models so we don't list 100s of empty rows.
  const byModel = {};
  const untagged = [];
  for (const e of execs) {
    const itemName = e.item?.name || "";
    const itemId = e.item?.id;
    const folder = (itemId && folderMap.byId.get(itemId)) || folderMap.byName.get(itemName) || "";
    const url = reconstructUrl(e.request?.url);
    const body = e.request?.body?.raw || bufToString(e.request?.body) || "";
    const headers = (e.request?.header || []).map((h) => ({ key: h.key, value: h.value }));
    const provider = detectProvider(folder, url, body);
    const route = detectRoute(url);
    const features = detectFeatures(folder, itemName, body, headers);
    const model = detectModel(url, body);
    const code = e.response?.code ?? 0;
    const assertFails = (e.assertions || []).filter((a) => !!a.error);
    const isFail = assertFails.length > 0 || code === 0 || code >= 400 || !e.response;

    if (!provider || !byProvider[provider]) {
      untagged.push({ name: itemName, folder, url });
    } else {
      for (const f of features) {
        if (!byProvider[provider][f]) continue;
        byProvider[provider][f].total++;
        if (isFail) byProvider[provider][f].failed++;
        else byProvider[provider][f].passed++;
      }
    }
    for (const f of features) {
      if (!byRoute[route] || !byRoute[route][f]) continue;
      byRoute[route][f].total++;
      if (isFail) byRoute[route][f].failed++;
      else byRoute[route][f].passed++;
    }

    // Per-(provider, model) tracking - lazily init each model bucket.
    const modelKey = `${provider || "?"} / ${model}`;
    if (!byModel[modelKey]) {
      byModel[modelKey] = {};
      for (const f of COVERAGE_FEATURES) byModel[modelKey][f] = { total: 0, passed: 0, failed: 0 };
    }
    for (const f of features) {
      if (!byModel[modelKey][f]) continue;
      byModel[modelKey][f].total++;
      if (isFail) byModel[modelKey][f].failed++;
      else byModel[modelKey][f].passed++;
    }
  }
  return { byProvider, byRoute, byModel, untagged };
};

// formatCell renders a single coverage cell.
const formatCell = (c) => {
  if (c.total === 0) return "—";
  if (c.failed === 0) return `✓ ${c.passed}/${c.total}`;
  if (c.passed === 0) return `✗ 0/${c.total}`;
  return `${c.passed}/${c.total}`;
};

// renderFeatureRowsTable renders a Feature × <axis> matrix where the axis
// columns are either providers or routes. Features are rows so the table is
// taller than wide. When isProviderAxis is true, cells in PROVIDER_FEATURE_COMPAT
// outside the supported list render as "N/A" so the matrix distinguishes
// provider-incompatible features from genuine coverage gaps.
const renderFeatureRowsTable = (matrixByCol, columnNames, isProviderAxis = false) => {
  const out = [];
  out.push("| Feature | " + columnNames.join(" | ") + " |");
  out.push("|" + Array(columnNames.length + 1).fill("---").join("|") + "|");
  for (const f of COVERAGE_FEATURES) {
    const cells = [f];
    for (const col of columnNames) {
      const c = matrixByCol[col]?.[f];
      // For provider axis: mark N/A when the provider can't support this feature.
      if (isProviderAxis && !isFeatureApplicable(col, f)) {
        cells.push("N/A");
        continue;
      }
      if (c && c.total > 0) cells.push(formatCell(c));
      else cells.push("—");
    }
    out.push("| " + cells.join(" | ") + " |");
  }
  return out.join("\n");
};

// renderPerModelCoverage emits one row per (provider, model) tuple — sorted
// by provider then model — listing how many features were exercised for that
// specific model and which ones. Compact bullet-list format because a full
// matrix at model granularity would be ~60 rows × 47 cols.
const renderPerModelCoverage = (byModel) => {
  const out = [];
  const keys = Object.keys(byModel).sort();
  out.push(`| Provider / Model | Tested | Passed | Failed | Features tested |`);
  out.push(`|---|---|---|---|---|`);
  for (const key of keys) {
    const cells = byModel[key];
    const tested = COVERAGE_FEATURES.filter((f) => cells[f].total > 0);
    if (tested.length === 0) continue; // skip models with no tracked features (shouldn't happen — all hit Basic Chat at least)
    const totalReqs = tested.reduce((acc, f) => acc + cells[f].total, 0);
    const passedReqs = tested.reduce((acc, f) => acc + cells[f].passed, 0);
    const failedReqs = tested.reduce((acc, f) => acc + cells[f].failed, 0);
    const featureTags = tested.map((f) => {
      const c = cells[f];
      if (c.failed === 0) return `${f}`;
      if (c.passed === 0) return `~~${f}~~`; // strikethrough for all-failed
      return `${f}*`;
    }).join(", ");
    out.push(`| \`${key}\` | ${tested.length} | ${passedReqs} | ${failedReqs} | ${featureTags} |`);
  }
  return out.join("\n");
};

// renderMissingPerModel emits per-model untested-feature lists. Trimmed to
// the most useful subset: only models that have at least one passing test
// (so we know they're reachable) AND have at least one missing feature.
const renderMissingPerModel = (byModel) => {
  const out = [];
  const keys = Object.keys(byModel).sort();
  for (const key of keys) {
    const cells = byModel[key];
    const provider = key.split(" / ")[0];
    const applicable = COVERAGE_PROVIDERS.includes(provider)
      ? COVERAGE_FEATURES.filter((f) => isFeatureApplicable(provider, f))
      : COVERAGE_FEATURES;
    const tested = applicable.filter((f) => cells[f].total > 0);
    const missing = applicable.filter((f) => cells[f].total === 0);
    const anyPassed = tested.some((f) => cells[f].passed > 0);
    if (!anyPassed) continue; // model never returned 2xx — skip (covered in failures section)
    if (missing.length === 0) {
      out.push(`- \`${key}\` — full coverage.`);
    } else {
      // Trim missing list to top 8 to keep the section readable; full breadth is in the matrix.
      const shown = missing.slice(0, 8).map((m) => "`" + m + "`").join(", ");
      const rest = missing.length > 8 ? ` _(+${missing.length - 8} more)_` : "";
      out.push(`- \`${key}\` — ${tested.length} tested, ${missing.length} missing: ${shown}${rest}`);
    }
  }
  return out.join("\n");
};

// renderMissingPerProvider emits one bullet per provider listing untested
// features that ARE applicable to that provider (skips N/A combinations).
const renderMissingPerProvider = (byProvider) => {
  const out = [];
  for (const p of COVERAGE_PROVIDERS) {
    const applicable = COVERAGE_FEATURES.filter((f) => isFeatureApplicable(p, f));
    const missing = applicable.filter((f) => byProvider[p][f].total === 0);
    const tested = applicable.length - missing.length;
    if (missing.length === 0) {
      out.push(`- **${p}**: full coverage across all ${applicable.length} applicable features.`);
    } else {
      out.push(`- **${p}** — ${tested}/${applicable.length} applicable tested, ${missing.length} missing: ${missing.map((m) => "`" + m + "`").join(", ")}`);
    }
  }
  return out.join("\n");
};

// renderMissingPerRoute emits which features are not covered via each transport route.
const renderMissingPerRoute = (byRoute) => {
  const out = [];
  for (const r of COVERAGE_ROUTES) {
    const missing = COVERAGE_FEATURES.filter((f) => byRoute[r][f].total === 0);
    const tested = COVERAGE_FEATURES.length - missing.length;
    if (missing.length === 0) {
      out.push(`- **${r}**: full coverage across all ${COVERAGE_FEATURES.length} tracked features.`);
    } else {
      out.push(`- **${r}** — ${tested}/${COVERAGE_FEATURES.length} tested, ${missing.length} missing: ${missing.map((m) => "`" + m + "`").join(", ")}`);
    }
  }
  return out.join("\n");
};

const main = () => {
  const raw = JSON.parse(readFileSync(REPORT, "utf8"));
  const execs = raw.run?.executions || [];
  const stats = raw.run?.stats || {};
  const logText = existsSync(BIFROST_LOG) ? readFileSync(BIFROST_LOG, "utf8") : "";
  const folderMap = buildFolderMap(raw.collection || {});

  const failed = [];
  for (const e of execs) {
    const code = e.response?.code ?? 0;
    const body = bufToString(e.response?.stream);
    const assertions = e.assertions || [];
    const assertFails = assertions.filter((a) => !!a.error);
    const isFail = assertFails.length > 0 || code === 0 || code >= 400 || !e.response;
    if (!isFail) continue;
    const url = reconstructUrl(e.request?.url);
    const bifrostLines = grepBifrostForUrl(logText, url);
    const category = categorize(code, body, bifrostLines);
    const itemName = e.item?.name || "(unnamed)";
    const itemId = e.item?.id;
    const folder = (itemId && folderMap.byId.get(itemId)) || folderMap.byName.get(itemName) || "(root)";
    failed.push({
      name: itemName,
      folder,
      method: e.request?.method || "GET",
      url,
      code,
      body,
      bifrostLines,
      assertFails: assertFails.map((a) => a.error?.message || a.assertion),
      category,
    });
  }

  const total = execs.length;
  const failCount = failed.length;
  const passCount = total - failCount;

  const grouped = {};
  for (const f of failed) {
    (grouped[f.category] ||= []).push(f);
  }
  const orderedCats = ["provider_not_configured", "auth_invalid", "model_not_found", "request_shape_mismatch", "rate_limit", "network_or_timeout", "unknown"];

  const lines = [];
  lines.push(`# Bifrost Provider Harness - Failure Report`);
  lines.push("");
  lines.push(`Generated: ${new Date().toISOString()}`);
  lines.push(`Source report: \`${REPORT}\``);
  lines.push(`Bifrost log: \`${BIFROST_LOG}\` (${logText ? logText.split("\n").length + " lines" : "empty/missing"})`);
  lines.push("");
  lines.push(`**Total: ${total} | Passed: ${passCount} | Failed: ${failCount}**`);
  lines.push("");

  // Coverage matrices - rendered before pass/fail details so they're the
  // first thing reviewers see when scanning the report.
  const { byProvider, byRoute, byModel, untagged } = buildCoverageMatrix(execs, folderMap);

  lines.push(`## Coverage matrices`);
  lines.push("");
  lines.push(`Cell legend: \`✓ P/T\` all P passed; \`✗ 0/T\` all failed; \`P/T\` mixed; \`—\` no test for this combination (gap). Multi-tag detection: a single request can exercise multiple features (e.g. "Function Calling" + "Streaming"), so cell totals can exceed request count.`);
  lines.push("");

  lines.push(`### Feature × Provider (drop-in routes + cross-model + passthrough)`);
  lines.push("");
  lines.push(`Cells marked \`N/A\` mean the feature is not part of that provider's API surface (e.g., "Reasoning Effort" is OpenAI-only, "Adaptive Thinking" is Anthropic-only). Only \`—\` cells are real coverage gaps.`);
  lines.push("");
  lines.push(renderFeatureRowsTable(byProvider, COVERAGE_PROVIDERS, true));
  lines.push("");

  lines.push(`### Feature × Route (which transport surfaces exercise each feature)`);
  lines.push("");
  lines.push(renderFeatureRowsTable(byRoute, COVERAGE_ROUTES, false));
  lines.push("");

  lines.push(`### Per-model coverage (every distinct provider/model exercised)`);
  lines.push("");
  lines.push(`Tags with \`*\` mean some requests passed and some failed for that feature; \`~~feature~~\` means all requests for that feature failed on this model.`);
  lines.push("");
  lines.push(renderPerModelCoverage(byModel));
  lines.push("");

  lines.push(`### Missing coverage — per provider`);
  lines.push("");
  lines.push(renderMissingPerProvider(byProvider));
  lines.push("");

  lines.push(`### Missing coverage — per route`);
  lines.push("");
  lines.push(renderMissingPerRoute(byRoute));
  lines.push("");

  lines.push(`### Missing coverage — per model (top 8 gaps each)`);
  lines.push("");
  lines.push(renderMissingPerModel(byModel));
  lines.push("");

  if (untagged.length) {
    lines.push(`> ${untagged.length} request(s) could not be auto-classified by provider; not counted in the per-provider matrix above. Examples: ${untagged.slice(0, 3).map((u) => "`" + (u.name || u.url) + "`").join(", ")}.`);
    lines.push("");
  }

  if (failCount === 0) {
    lines.push(`All requests passed. No fix actions needed.`);
    writeFileSync(OUT, lines.join("\n") + "\n");
    console.log(`[analyze-failures] no failures - wrote summary + coverage to ${OUT}`);
    return;
  }

  lines.push(`## Summary by category`);
  lines.push("");
  lines.push(`| category | count | suggested fix |`);
  lines.push(`|---|---|---|`);
  for (const cat of orderedCats) {
    const c = grouped[cat]?.length || 0;
    if (c > 0) {
      const firstSentence = FIX_HINTS[cat].split(/\.\s/)[0] + ".";
      lines.push(`| \`${cat}\` | ${c} | ${firstSentence} |`);
    }
  }
  lines.push("");
  lines.push(`**Recommended fix order**: \`provider_not_configured\` -> \`auth_invalid\` -> \`model_not_found\` -> \`request_shape_mismatch\` -> others. Fixing config + auth first usually collapses cascading downstream failures.`);
  lines.push("");

  for (const cat of orderedCats) {
    const items = grouped[cat] || [];
    if (items.length === 0) continue;
    lines.push(`## \`${cat}\` (${items.length})`);
    lines.push("");
    lines.push(`> ${FIX_HINTS[cat]}`);
    lines.push("");
    for (const f of items) {
      // Avoid "POST POST /bedrock/..." when the request name already starts with the method.
      const displayName = new RegExp(`^${f.method}\\b`, "i").test(f.name) ? f.name : `${f.method} ${f.name}`;
      lines.push(`### ${displayName} - status ${f.code || "n/a"}`);
      lines.push(`Folder: \`${f.folder || "(root)"}\``);
      lines.push(`URL: \`${f.url}\``);
      if (f.assertFails.length) {
        lines.push("");
        lines.push(`Assertion failures:`);
        for (const m of f.assertFails) lines.push(`- ${m}`);
      }
      lines.push("");
      lines.push(`Response body (first 400 chars):`);
      lines.push("```");
      lines.push(truncate(f.body, 400) || "(empty)");
      lines.push("```");
      if (f.bifrostLines.length) {
        lines.push("");
        lines.push(`Matching Bifrost log lines:`);
        lines.push("```");
        for (const l of f.bifrostLines) lines.push(truncate(l, 240));
        lines.push("```");
      }
      lines.push("");
    }
  }

  writeFileSync(OUT, lines.join("\n") + "\n");
  console.log(`[analyze-failures] wrote ${OUT} - ${failCount} failures across ${Object.keys(grouped).length} categories`);
};

main();
