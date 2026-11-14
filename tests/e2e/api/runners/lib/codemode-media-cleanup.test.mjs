// Runs the code-mode-media folder's setup and cleanup scripts from provider-harness.json against a stub pm:
// the run-scoped MCP client must be deleted even when the readiness check fails before it confirms the id.
// Run directly: `node codemode-media-cleanup.test.mjs`.
import assert from 'node:assert/strict';
import fs from 'node:fs';
import test from 'node:test';

const collection = JSON.parse(fs.readFileSync(new URL('../../collections/provider-harness.json', import.meta.url), 'utf8'));
const folder = collection.item.find(item => item.name.includes('(code-mode-media)'));
assert.ok(folder, 'the code-mode-media folder is missing');
function row(fragment) {
  const item = folder.item.find(i => i.name.includes(fragment));
  assert.ok(item, `no code-mode-media row named like "${fragment}"`);
  return item;
}
function script(item, listen) {
  const event = (item.event || []).find(e => e.listen === listen);
  assert.ok(event, `${item.name} has no ${listen} script`);
  return event.script.exec.join('\n');
}
const create = row('provision a run-scoped');
const readiness = row('fixture readiness');
const cleanup = row('delete the run-scoped client');

function expect(value, message) {
  const typed = type => {
    assert.ok(type === 'array' ? Array.isArray(value) : typeof value === type, `${message}: expected ${type}`);
    return { and: { not: { get empty() { assert.ok(value.length > 0, `${message}: expected non-empty`); return true; } } } };
  };
  return { to: {
    equal: expected => assert.equal(value, expected, message),
    eql: expected => assert.deepEqual(value, expected, message),
    be: { a: typed, an: typed },
    have: { lengthOf: n => assert.equal(value && value.length, n, message) },
  } };
}

// One run shares the collection variables across rows, as newman does.
function harness(guids) {
  const vars = new Map();
  const state = { failures: [], skipped: false, lookups: [] };
  const pm = {
    collectionVariables: { get: k => vars.get(k), set: (k, v) => vars.set(k, v), unset: k => vars.delete(k) },
    environment: { get: () => undefined },
    variables: { replaceIn: s => s.replace('{{$guid}}', () => guids.shift()).replace('{{baseUrl}}', 'http://bifrost') },
    execution: { skipRequest: () => { state.skipped = true; } },
    test: (name, fn) => { try { fn(); } catch (e) { state.failures.push(`${name}: ${e.message}`); } },
    expect,
    sendRequest: () => assert.fail('unexpected sendRequest'),
  };
  const run = (src, extra = {}) => new Function('pm', src)(Object.assign(pm, extra));
  return { vars, state, run };
}
const response = (code, body) => ({ code, json: () => body, text: () => JSON.stringify(body) });
const listing = (clients) => response(200, { clients });
const client = (name, id) => ({ config: { name, client_id: id, is_code_mode_client: true, tools_to_execute: ['capture_media'], tools_to_auto_execute: ['capture_media'] }, tools: [{ name: 'capture_media' }] });

test('a client whose readiness check fails is still deleted by the id it was created with', () => {
  const { vars, state, run } = harness(['aaaa-bbbb', 'client-id-1']);
  run(script(create, 'prerequest'));
  const name = vars.get('codeModeMediaClient');
  assert.equal(name, 'harness_codemode_media_aaaabbbb');
  assert.match(create.request.body.raw, /"client_id": "\{\{codeModeMediaClientNewId\}\}"/);
  assert.equal(vars.get('codeModeMediaClientNewId'), 'client-id-1');
  run(script(create, 'test'), { response: response(200, { status: 'success' }) });
  assert.deepEqual(state.failures, []);
  run(script(readiness, 'test'), { response: response(500, { error: 'listing failed' }) });
  assert.equal(state.failures.length, 1, 'the readiness check itself must still fail');
  run(script(cleanup, 'prerequest'));
  assert.equal(state.skipped, false);
  assert.equal(vars.get('codeModeMediaClientId'), 'client-id-1');
  assert.equal(cleanup.request.url, '{{baseUrl}}/api/mcp/client/{{codeModeMediaClientId}}');
});

test('the readiness check fails when the listed id differs from the created one', () => {
  const { vars, state, run } = harness(['aaaa-bbbb', 'client-id-1']);
  run(script(create, 'prerequest'));
  run(script(create, 'test'), { response: response(200, { status: 'success' }) });
  run(script(readiness, 'test'), { response: listing([client(vars.get('codeModeMediaClient'), 'other-id')]) });
  assert.equal(state.failures.length, 1);
  run(script(readiness, 'test'), { response: listing([client(vars.get('codeModeMediaClient'), 'client-id-1')]) });
  assert.equal(state.failures.length, 1, 'a matching listing passes');
});

test('without a stored id, cleanup finds the client by its run-scoped name and deletes it', () => {
  const { vars, state, run } = harness(['aaaa-bbbb', 'client-id-1']);
  run(script(create, 'prerequest'));
  run(script(create, 'test'), { response: response(502, { error: 'bad gateway' }) });
  assert.equal(vars.get('codeModeMediaClientId'), undefined);
  const name = vars.get('codeModeMediaClient');
  run(script(cleanup, 'prerequest'), { sendRequest: (req, cb) => {
    state.lookups.push(req.url);
    cb(null, listing([client(name + '_other', 'x'), client(name, 'client-id-1')]));
  } });
  assert.deepEqual(state.lookups, ['http://bifrost/api/mcp/clients?search=' + name]);
  assert.equal(state.skipped, false);
  assert.equal(vars.get('codeModeMediaClientId'), 'client-id-1');
});

test('cleanup is skipped when nothing was registered under the run-scoped name', () => {
  const { vars, state, run } = harness(['aaaa-bbbb', 'client-id-1']);
  run(script(create, 'prerequest'));
  run(script(cleanup, 'prerequest'), { sendRequest: (req, cb) => cb(null, listing([])) });
  assert.equal(state.skipped, true);
  assert.deepEqual(state.failures, []);
  assert.equal(vars.get('codeModeMediaClient'), undefined);
});

test('a failed lookup fails cleanup by name instead of passing silently', () => {
  const { state, run } = harness(['aaaa-bbbb', 'client-id-1']);
  run(script(create, 'prerequest'));
  run(script(cleanup, 'prerequest'), { sendRequest: (req, cb) => cb(null, response(500, { error: 'down' })) });
  assert.equal(state.skipped, true);
  assert.equal(state.failures.length, 1);
});

test('cleanup is skipped when the folder never ran its setup', () => {
  const { state, run } = harness([]);
  run(script(cleanup, 'prerequest'));
  assert.equal(state.skipped, true);
});
