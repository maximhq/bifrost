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
