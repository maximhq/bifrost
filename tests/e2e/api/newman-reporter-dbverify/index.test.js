// Unit tests for the logs-API cost-audit helpers. Run directly: `node index.test.js`.
// No test framework needed (matches lib/pricing.test.js style).
const assert = require('node:assert');
const { isBillableInferenceURL, classifyLogCost } = require('./index');

let passed = 0;
function test(name, fn) {
  fn();
  passed++;
  console.log(`  ok - ${name}`);
}

// ── isBillableInferenceURL: billable inference endpoints (must be checked) ──
for (const p of [
  '/v1/chat/completions',
  '/openai/deployments/gpt-4o/chat/completions',
  '/v1/messages',
  '/v1/responses',
  '/v1/responses/compact',
  '/openai/deployments/gpt-4o/responses',
  '/v1beta/models/gemini-2.5-flash:generateContent',
  '/v1beta/models/claude-haiku-4-5:streamGenerateContent',
  '/anthropic/claude-haiku-4-5:generateContent',
  '/cohere/v2/chat',
  '/v1/embeddings',
  '/v1beta/models/text-embedding-005:embedContent',
  '/v1/images/generations',
  '/v1/audio/speech',
  '/v1/audio/transcriptions',
]) {
  test(`billable: ${p}`, () => assert.strictEqual(isBillableInferenceURL(p), true));
}

// ── isBillableInferenceURL: utility / management endpoints (must be excluded) ──
for (const p of [
  '/v1beta/models/gemini-2.5-flash:countTokens',
  '/v1/messages/count_tokens',
  '/v1/responses/input_tokens',
  '/v1/models',
  '/v1/files',
  '/v1/files/file_123',
  '/v1/batches',
  '/v1/messages/batches',
  '/api/logs',        // management endpoint, not inference
  '/api/providers',
  '',
]) {
  test(`not billable: ${p || '(empty)'}`, () => assert.strictEqual(isBillableInferenceURL(p), false));
}

// A generateContent path must not be excluded just because it lives under /v1beta/models
test('gemini generateContent under /v1beta/models is not caught by the models exclusion', () => {
  assert.strictEqual(isBillableInferenceURL('/v1beta/models/gemini-2.5-flash:generateContent'), true);
});

// ── classifyLogCost ──
test('cost populated → PASS', () => {
  const r = classifyLogCost({ cost: 0.0042, provider: 'anthropic', model: 'claude-sonnet-4-6', status: 'success' });
  assert.strictEqual(r.result, 'PASS');
  assert.match(r.detail, /cost populated: \$0\.0042/);
});

test('cost zero → WARN (missing)', () => {
  const r = classifyLogCost({ cost: 0, provider: 'openai', model: 'gpt-4o', status: 'success' });
  assert.strictEqual(r.result, 'WARN');
  assert.match(r.detail, /cost MISSING/);
});

test('cost absent → WARN (missing)', () => {
  const r = classifyLogCost({ status: 'success' });
  assert.strictEqual(r.result, 'WARN');
  assert.match(r.detail, /cost MISSING/);
});

console.log(`\nindex.test.js: ${passed} passed`);
