import assert from 'node:assert/strict';
import fs from 'node:fs';
import test from 'node:test';
import vm from 'node:vm';

const collection = JSON.parse(fs.readFileSync(new URL('../../collections/provider-harness.json', import.meta.url), 'utf8'));
const folder = collection.item.find(item => item.name.startsWith('393. Responses-to-Chat streamed tool calls'));
const parallel = folder.item.find(item => item.name.includes('parallel streamed tools'));
const single = folder.item.find(item => item.name.includes('single streamed tools'));
const mixed = folder.item.find(item => item.name.includes('mixed streamed tools'));

// failuresFor runs the Postman assertions against synthetic SSE and returns their errors.
function failuresFor(parts, item = parallel) {
  const failures = [];
  const script = item.event.find(event => event.listen === 'test').script.exec.join('\n');
  const frames = [{ extra_fields: { converted_request_type: 'responses' }, choices: [{ delta: { tool_calls: parts } }] }];
  const text = frames.map(frame => 'data: ' + JSON.stringify(frame) + '\n\n').join('') + 'data: [DONE]\n\n';
  const expect = (value, message) => ({ to: {
    eql: expected => assert.deepEqual(value, expected, message),
    include: expected => assert.ok(value.includes(expected), message),
    get exist() { assert.ok(value != null, message); },
    be: {
      above: minimum => assert.ok(value > minimum, message),
      a: type => {
        assert.equal(typeof value, type, message);
        return { and: { not: { get empty() { assert.ok(value.length > 0, message); } } } };
      },
      get true() { assert.equal(value, true, message); },
      get undefined() { assert.equal(value, undefined, message); },
    },
  } });
  vm.runInNewContext(script, { pm: {
    response: { code: 200, text: () => text, headers: { get: () => 'text/event-stream' } },
    expect,
    test: (_name, fn) => { try { fn(); } catch (error) { failures.push(error); } },
  } });
  return failures;
}

const read = { index: 0, id: 'call-read', function: { name: 'read', arguments: '{"filePath":"/tmp/example.txt"}' } };
const glob = { index: 1, id: 'call-glob', function: { name: 'glob', arguments: '{"pattern":"*.go"}' } };

test('parallel-enabled stream accepts one read call', () => assert.deepEqual(failuresFor([read]), []));
test('parallel-enabled stream accepts one glob call', () => assert.deepEqual(failuresFor([{ ...glob, index: 0 }]), []));
test('parallel-enabled stream accepts two calls', () => assert.deepEqual(failuresFor([read, glob]), []));
test('parallel-enabled stream accepts repeated tool names with different IDs', () => {
  assert.deepEqual(failuresFor([read, { ...read, index: 1, id: 'call-read-2' }]), []);
});
test('parallel-enabled stream accepts other valid argument values', () => {
  const call = { ...read, function: { ...read.function, arguments: '{"filePath":"/tmp/another.txt"}' } };
  assert.deepEqual(failuresFor([call]), []);
});
test('stream rejects no tool calls', () => assert.ok(failuresFor([]).length));
test('stream rejects colliding indexes', () => {
  const failures = failuresFor([read, { ...glob, index: 0 }]);
  assert.ok(failures.some(error => error.message.includes('two call IDs share index 0')));
});
test('stream rejects non-contiguous indexes', () => assert.ok(failuresFor([read, { ...glob, index: 2 }]).length));
test('stream rejects duplicate IDs', () => assert.ok(failuresFor([read, { ...glob, id: read.id }]).length));
test('stream rejects missing arguments', () => assert.ok(failuresFor([{ ...read, function: { ...read.function, arguments: '{}' } }]).length));
test('stream rejects empty argument values', () => assert.ok(failuresFor([{ ...read, function: { ...read.function, arguments: '{"filePath":""}' } }]).length));
test('stream rejects malformed arguments', () => assert.ok(failuresFor([{ ...read, function: { ...read.function, arguments: '{' } }]).length));
test('stream rejects unexpected tool names', () => assert.ok(failuresFor([{ ...read, function: { ...read.function, name: 'other' } }]).length));
test('single-tool stream accepts other valid argument values', () => {
  const call = { ...read, function: { ...read.function, arguments: '{"filePath":"/tmp/another.txt"}' } };
  assert.deepEqual(failuresFor([call], single), []);
});
test('single-tool stream rejects missing arguments', () => assert.ok(failuresFor([{ ...read, function: { ...read.function, arguments: '{}' } }], single).length));

const mixedRead = { ...read, type: 'function' };
const custom = { index: 1, id: 'call-custom', type: 'custom', custom: { name: 'code_exec', input: 'print(1)' } };
test('mixed stream accepts separate function and custom calls', () => assert.deepEqual(failuresFor([mixedRead, custom], mixed), []));
test('mixed stream accepts a single custom call', () => assert.deepEqual(failuresFor([{ ...custom, index: 0 }], mixed), []));
test('mixed stream rejects colliding indexes', () => {
  const failures = failuresFor([mixedRead, { ...custom, index: 0 }], mixed);
  assert.ok(failures.some(error => error.message.includes('two call IDs share index 0')));
});
test('mixed stream rejects an empty custom input', () => {
  const failures = failuresFor([{ ...custom, index: 0, custom: { ...custom.custom, input: '' } }], mixed);
  assert.ok(failures.some(error => error.message.includes('custom input missing')));
});
