import assert from 'node:assert/strict';
import fs from 'node:fs';
import vm from 'node:vm';
import test from 'node:test';
const collection = JSON.parse(fs.readFileSync(new URL('../../collections/provider-harness.json', import.meta.url), 'utf8'));
function find(items) {
  for (const item of items) {
    if (item.name === '47.6.2.G /bedrock converse -> openai/o3-mini') return item;
    if (item.item) { const match = find(item.item); if (match) return match; }
  }
}
const script = find(collection.item).event.find(e => e.listen === 'test').script.exec.join('\n');
function frame(type, payload) {
  const key = Buffer.from(':event-type'), value = Buffer.from(type);
  const headers = Buffer.concat([Buffer.from([key.length]), key, Buffer.from([7, 0, value.length]), value]);
  const body = Buffer.from(JSON.stringify(payload));
  const bytes = Buffer.alloc(16 + headers.length + body.length);
  bytes.writeUInt32BE(bytes.length); bytes.writeUInt32BE(headers.length, 4);
  headers.copy(bytes, 12); body.copy(bytes, 12 + headers.length);
  return bytes;
}
const answer = frame('contentBlockDelta', { contentBlockIndex: 0, delta: { text: '44 sheep.' } });
const stop = frame('messageStop', { stopReason: 'end_turn' });
const usage = frame('metadata', { usage: { inputTokens: 67, outputTokens: 1084, totalTokens: 1151 } });
function run(stream, code = 200) {
  const failures = [], skipped = [];
  const testFn = (name, fn) => { try { fn(); } catch (e) { failures.push(name + ': ' + e.message); } };
  testFn.skip = name => skipped.push(name);
  vm.runInNewContext('(function(){' + script + '})()', { Buffer, pm: {
    response: { code, stream, text: () => stream.toString('utf8') }, test: testFn,
    expect: (value, message) => ({ to: {
      match: regex => assert.match(value, regex, message),
      eql: expected => assert.equal(value, expected, message),
      be: { below: limit => assert.ok(value < limit, message), above: limit => assert.ok(value > limit, message) },
    } }),
  } });
  return { failures, skipped };
}
test('hidden reasoning does not fail a completed o3-mini answer', () => {
  const result = run(Buffer.concat([answer, stop, usage]));
  assert.deepEqual(result.failures, []);
  assert.equal(result.skipped.length, 1, 'reasoning visibility must be explicitly unobservable');
});
for (const [name, body, code] of [
  ['missing answer', Buffer.concat([stop, usage]), 200],
  ['missing completion', Buffer.concat([answer, usage]), 200],
  ['truncated frame', Buffer.concat([answer, stop, usage]).subarray(0, -1), 200],
  ['provider error', Buffer.from('{"error":"bad request"}'), 400],
]) test(name + ' still fails', () => assert.ok(run(body, code).failures.length > 0));
