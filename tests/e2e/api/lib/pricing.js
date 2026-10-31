// Shared pricing-datasheet entry resolver, used by both the newman dbverify
// reporter and the stream-cancellation runner. Kept in one place so the provider
// alias normalization can never drift between the two consumers again.
//
// Datasheet keys are model ids — bare ("gpt-4o", "claude-haiku-4-5") or prefixed
// ("anthropic.claude-...", "azure/gpt-..."). We try a few normalizations and
// return null when there's no confident match so callers skip cost-accuracy.
//
// Bifrost provider names → datasheet (LiteLLM) provider names. The datasheet keys
// Vertex-hosted Gemini under the bare model id with provider "vertex_ai" (vs the
// AI-Studio variant "gemini/<model>" with provider "gemini"), so a Bifrost
// "vertex" row would otherwise fail the provider guard below.
const PRICING_PROVIDER_ALIASES = { vertex: 'vertex_ai' };

function normalizePricingProvider(p) {
  if (!p) return '';
  const lp = String(p).toLowerCase();
  return PRICING_PROVIDER_ALIASES[lp] || lp;
}

function resolvePricingEntry(sheet, model, provider) {
  if (!sheet || !model) return null;
  const m = String(model);
  const bare = m.includes('/') ? m.split('/').pop() : m;
  // Datasheet keys are lowercase-prefixed (litellm style), so lower-case the
  // provider before building the prefixed candidate to avoid case-only misses.
  const lowerProvider = provider ? String(provider).toLowerCase() : '';
  const np = normalizePricingProvider(provider);
  const candidates = [m, bare,
    lowerProvider ? `${lowerProvider}/${bare}` : null,
    np && np !== lowerProvider ? `${np}/${bare}` : null,
  ].filter(Boolean);
  for (const key of candidates) {
    const e = sheet[key];
    if (e && (!e.provider || !np || normalizePricingProvider(e.provider) === np)) {
      return e;
    }
  }
  return null;
}

// Datasheet column suffix for each served OpenAI service_tier. "fast" is the
// Priority tier renamed on 2026-07-30 (OpenAI accepts both spellings and bills
// them identically), and the datasheet keeps those rates under the _priority
// columns. Anything else (default/auto/empty) bills on the base columns.
const TIER_SUFFIX = { priority: '_priority', fast: '_priority', flex: '_flex', ultrafast: '_ultrafast' };

// Pick the per-token rates for the tier the provider actually served, mirroring
// datasheet/cost.go for requests at or below 128k tokens: the tier column wins
// when the datasheet publishes it, otherwise the base column applies.
function tierRates(entry, serviceTier) {
  const sfx = TIER_SUFFIX[String(serviceTier || '').toLowerCase()] || '';
  const pick = (base) => {
    const tiered = sfx ? entry[base + sfx] : undefined;
    return (tiered != null ? tiered : entry[base]) || 0;
  };
  return {
    input: pick('input_cost_per_token'),
    output: pick('output_cost_per_token'),
    cacheRead: pick('cache_read_input_token_cost'),
    cacheWrite: pick('cache_creation_input_token_cost'),
  };
}

// Recompute the expected cost of a logs row from its datasheet entry, mirroring
// datasheet/cost.go computeTextCost: nonCachedPrompt x input + cachedRead x
// cacheRead + cachedWrite x cacheCreation + completion x output, at the rates of
// the served tier recorded in the row's service_tier column.
function expectedCostFromRow(entry, row) {
  const r = tierRates(entry, row.service_tier);
  const prompt = Number(row.prompt_tokens || 0);
  const completion = Number(row.completion_tokens || 0);
  let cachedRead = Number(row.cached_read_tokens || 0);
  let cachedWrite = 0;
  if (row.token_usage) {
    try {
      const d = JSON.parse(row.token_usage)?.prompt_tokens_details;
      if (d) {
        if (cachedRead === 0 && d.cached_read_tokens) cachedRead = Number(d.cached_read_tokens);
        if (d.cached_write_tokens) cachedWrite = Number(d.cached_write_tokens);
      }
    } catch (_) { /* ignore malformed usage blob */ }
  }
  // Clamp exactly like cost.go.
  cachedRead = Math.min(cachedRead, prompt);
  cachedWrite = Math.min(cachedWrite, Math.max(0, prompt - cachedRead));
  const nonCachedPrompt = Math.max(0, prompt - cachedRead - cachedWrite);
  return nonCachedPrompt * r.input + cachedRead * r.cacheRead + cachedWrite * r.cacheWrite + completion * r.output;
}

module.exports = { PRICING_PROVIDER_ALIASES, normalizePricingProvider, resolvePricingEntry, tierRates, expectedCostFromRow };
