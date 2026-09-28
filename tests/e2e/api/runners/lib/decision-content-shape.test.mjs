import assert from 'node:assert/strict';
import fs from 'node:fs';
import test from 'node:test';

const collection = JSON.parse(fs.readFileSync(new URL('../../collections/provider-harness.json', import.meta.url), 'utf8'));
const collectionScript = collection.event.find(event => event.listen === 'test').script.exec.join('\n');
const contentScript = collectionScript.slice(
  collectionScript.indexOf('// Only register the content-shape test'),
  collectionScript.indexOf('// Routed-identity response headers'),
);

function contentFailures(body) {
  const failures = [];
  new Function('pm', contentScript)({
    request: { url: { toString: () => 'http://localhost:8080/v1/decisions' } },
    response: {
      code: 200,
      headers: { get: () => 'application/json' },
      json: () => body,
    },
    test: (_name, fn) => { try { fn(); } catch (error) { failures.push(error); } },
    expect: (value) => ({ to: { be: { get true() { assert.equal(value, true); } } } }),
  });
  return failures;
}

test('decision answers count as response content', () => {
  assert.equal(contentFailures({ answers: { category: { kind: 'choice', value: 'billing' } } }).length, 0);
});

test('empty decision answers still fail the content check', () => {
  assert.equal(contentFailures({ answers: {} }).length, 1);
});
