// Unit tests for the shared pricing-entry resolver. Run directly: `node pricing.test.js`.
// No test framework needed (the tests/e2e/api dir has no test runner configured).
const assert = require('node:assert');
const { resolvePricingEntry, normalizePricingProvider, tierRates, expectedCostFromRow } = require('./pricing');

let passed = 0;
function test(name, fn) {
  fn();
  passed++;
  console.log(`  ok - ${name}`);
}

// The regression this de-duplication fixes: Bifrost reports provider "vertex",
// but the datasheet keys Vertex-hosted Gemini under "vertex_ai". Without alias
// normalization the lookup fails. (The runner's old copy lacked this.)
test('resolves vertex model against vertex_ai datasheet key', () => {
  const sheet = {
    'vertex_ai/gemini-2.5-flash': { provider: 'vertex_ai', input_cost_per_token: 1 },
  };
  const entry = resolvePricingEntry(sheet, 'gemini-2.5-flash', 'vertex');
  assert.ok(entry, 'expected a pricing entry for vertex → vertex_ai');
  assert.strictEqual(entry.input_cost_per_token, 1);
});

test('resolves a plain bare-model key with matching provider', () => {
  const sheet = { 'gpt-4o': { provider: 'openai', input_cost_per_token: 2.5e-6 } };
  const entry = resolvePricingEntry(sheet, 'gpt-4o', 'openai');
  assert.ok(entry);
  assert.strictEqual(entry.input_cost_per_token, 2.5e-6);
});

// Datasheet keys are lowercase-prefixed (litellm style). Provider casing from
// callers must not cause a false miss against an only-prefixed key.
test('resolves prefixed key when provider casing differs (OpenAI → openai/<model>)', () => {
  const sheet = { 'openai/gpt-4o': { provider: 'openai', input_cost_per_token: 5 } };
  const entry = resolvePricingEntry(sheet, 'gpt-4o', 'OpenAI');
  assert.ok(entry, 'expected case-insensitive prefixed match');
  assert.strictEqual(entry.input_cost_per_token, 5);
});

test('returns null when model is missing', () => {
  assert.strictEqual(resolvePricingEntry({}, null, 'openai'), null);
});

test('does not match when provider guard disagrees', () => {
  const sheet = { 'gpt-4o': { provider: 'azure' } };
  assert.strictEqual(resolvePricingEntry(sheet, 'gpt-4o', 'openai'), null);
});

test('normalizePricingProvider maps vertex → vertex_ai, passes others through', () => {
  assert.strictEqual(normalizePricingProvider('vertex'), 'vertex_ai');
  assert.strictEqual(normalizePricingProvider('OpenAI'), 'openai');
  assert.strictEqual(normalizePricingProvider(''), '');
});

// Live openai/gpt-6-astra shape: OpenAI's Fast tier (ex-Priority) is stored under
// the _priority columns, $20/$2/$25/$100 per 1M vs $10/$1/$12.5/$50 standard.
const astra = {
  provider: 'openai',
  input_cost_per_token: 0.00001, input_cost_per_token_priority: 0.00002,
  output_cost_per_token: 0.00005, output_cost_per_token_priority: 0.0001,
  cache_read_input_token_cost: 0.000001, cache_read_input_token_cost_priority: 0.000002,
  cache_creation_input_token_cost: 0.0000125, cache_creation_input_token_cost_priority: 0.000025,
};

test('tierRates: "fast" and "priority" both select the _priority columns', () => {
  for (const tier of ['fast', 'priority', 'FAST']) {
    const r = tierRates(astra, tier);
    assert.deepStrictEqual(r, { input: 0.00002, output: 0.0001, cacheRead: 0.000002, cacheWrite: 0.000025 }, tier);
  }
});

test('tierRates: default/auto/missing stay on the base columns', () => {
  for (const tier of ['default', 'auto', '', null, undefined]) {
    const r = tierRates(astra, tier);
    assert.deepStrictEqual(r, { input: 0.00001, output: 0.00005, cacheRead: 0.000001, cacheWrite: 0.0000125 }, String(tier));
  }
});

test('tierRates: a tier column the datasheet lacks falls back to base, per column', () => {
  const partial = { input_cost_per_token: 1, output_cost_per_token: 2, input_cost_per_token_flex: 0.5 };
  assert.deepStrictEqual(tierRates(partial, 'flex'), { input: 0.5, output: 2, cacheRead: 0, cacheWrite: 0 });
});

// The customer-reported row: 7283 prompt (7280 cached), 31 completion, served fast.
// Standard would be $0.00886; Fast is 3x$20/M + 7280x$2/M + 31x$100/M = $0.01772.
test('expectedCostFromRow: served fast row bills at Fast rates', () => {
  const row = { service_tier: 'fast', prompt_tokens: 7283, completion_tokens: 31, cached_read_tokens: 7280 };
  const got = expectedCostFromRow(astra, row);
  assert.ok(Math.abs(got - 0.01772) < 1e-9, `expected 0.01772, got ${got}`);
  const downgraded = expectedCostFromRow(astra, { ...row, service_tier: 'default' });
  assert.ok(Math.abs(downgraded - 0.00886) < 1e-9, `expected 0.00886, got ${downgraded}`);
});

test('expectedCostFromRow: cached tokens from token_usage blob and clamping', () => {
  const row = { service_tier: 'fast', prompt_tokens: 100, completion_tokens: 10, cached_read_tokens: 0,
    token_usage: JSON.stringify({ prompt_tokens_details: { cached_read_tokens: 40, cached_write_tokens: 500 } }) };
  // cachedWrite clamps to prompt - cachedRead = 60; nonCached = 0.
  const want = 40 * 0.000002 + 60 * 0.000025 + 10 * 0.0001;
  assert.ok(Math.abs(expectedCostFromRow(astra, row) - want) < 1e-12);
});

console.log(`\npricing.test.js: ${passed} passed`);
