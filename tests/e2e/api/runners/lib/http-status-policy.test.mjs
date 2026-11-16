import assert from 'node:assert/strict';
import fs from 'node:fs';
import test from 'node:test';

const collection = JSON.parse(fs.readFileSync(new URL('../../collections/provider-harness.json', import.meta.url), 'utf8'));
const statusScript = collection.event.find(event => event.listen === 'test').script.exec
  .join('\n').split('// Only register the content-shape test')[0];

function statusFailures(code, requestName = 'ordinary request') {
  const failures = [];
  new Function('pm', statusScript)({
    info: { requestName },
    response: { code, text: () => 'response body' },
    test: (name, fn) => { try { fn(); } catch (error) { failures.push(error); } },
    expect: value => ({ to: {
      equal: expected => assert.equal(value, expected),
      be: { within: (min, max) => assert.ok(value >= min && value <= max) },
    } }),
  });
  return failures.length;
}

test('ordinary requests accept only HTTP 200', () => {
  for (let code = 100; code <= 599; code++) {
    assert.equal(statusFailures(code), code === 200 ? 0 : 1, `HTTP ${code}`);
  }
});

test('explicit status expectations accept only the expected response', () => {
  for (let code = 100; code <= 599; code++) {
    assert.equal(statusFailures(code, '[EXPECT-202] async submit'), code === 202 ? 0 : 1, `async HTTP ${code}`);
    assert.equal(statusFailures(code, '[EXPECT-4XX] rejection'), code >= 400 && code <= 499 ? 0 : 1, `rejection HTTP ${code}`);
  }
});

// A row that asserts a non-200 status in its own script still runs the collection gate
// above, which requires 200 unless the row name carries the matching tag. Without the tag
// the row fails on the reply it asked for.
test('Baseten /v1/decisions rows that assert a non-200 status carry the matching tag', () => {
  const rows = [];
  const walk = items => items.forEach(item => (item.item ? walk(item.item) : rows.push(item)));
  walk(collection.item);
  const decisions = rows.filter(row => row.name.startsWith('/v1/decisions') && row.name.includes('baseten/'));
  assert.ok(decisions.length > 0, 'no Baseten /v1/decisions rows found');
  let rejections = 0;
  for (const row of decisions) {
    const own = (row.event || []).filter(event => event.listen === 'test').flatMap(event => event.script.exec).join('\n');
    const asserted = own.match(/pm\.expect\(pm\.response\.code\b.*\)\.to\.eql\((\d{3})\)/);
    if (!asserted || asserted[1] === '200') continue;
    rejections++;
    assert.equal(statusFailures(Number(asserted[1]), row.name), 0, `"${row.name}" asserts HTTP ${asserted[1]} but the collection gate rejects it`);
  }
  assert.ok(rejections > 0, 'no Baseten /v1/decisions rejection row found');
});

// The report-side classifiers (analyze-failures, rate-limit-backoff) must agree with
// the collection gate above about which statuses a tagged row expects, or a passing
// [EXPECT-400] row shows up as a failure in the report (seen live with #7307 rows).
test('isExpectedStatus mirrors the collection gate for every tag shape', async () => {
  const { isExpectedStatus } = await import('./expected-status.mjs');
  for (let code = 100; code <= 599; code++) {
    assert.equal(isExpectedStatus('[EXPECT-400] traversal rejection', code), code === 400, `400-tag HTTP ${code}`);
    assert.equal(isExpectedStatus('[EXPECT-202] async submit', code), code === 202, `202-tag HTTP ${code}`);
    assert.equal(isExpectedStatus('[EXPECT-4XX] rejection', code), code >= 400 && code <= 499, `4XX-tag HTTP ${code}`);
    assert.equal(isExpectedStatus('ordinary request', code), false, `untagged HTTP ${code}`);
  }
  assert.equal(isExpectedStatus(undefined, 400), false, 'missing name');
});
