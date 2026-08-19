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

module.exports = { PRICING_PROVIDER_ALIASES, normalizePricingProvider, resolvePricingEntry };
