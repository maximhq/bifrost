// Unit tests for the shared pricing-entry resolver. Run directly: `node pricing.test.js`.
// No test framework needed (the tests/e2e/api dir has no test runner configured).
const assert = require('node:assert');
const { resolvePricingEntry, normalizePricingProvider } = require('./pricing');

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

console.log(`\npricing.test.js: ${passed} passed`);
