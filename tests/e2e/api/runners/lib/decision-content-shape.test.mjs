import assert from 'node:assert/strict';
import fs from 'node:fs';
import test from 'node:test';

const collection = JSON.parse(fs.readFileSync(new URL('../../collections/provider-harness.json', import.meta.url), 'utf8'));
const collectionScript = collection.event.find(event => event.listen === 'test').script.exec.join('\n');
const contentScript = collectionScript.slice(
  collectionScript.indexOf('// Only register the content-shape test'),
  collectionScript.indexOf('// Routed-identity response headers'),
);

function contentFailures(body, { url = 'http://localhost:8080/v1/decisions', format, contentType = 'application/json' } = {}) {
  const failures = [];
  const expect = value => ({ to: {
    be: {
      get true() { assert.equal(value, true); },
      above: minimum => assert.ok(value > minimum),
    },
    match: pattern => assert.match(value, pattern),
  } });
  expect.fail = message => assert.fail(message);
  // The excerpt references the request name initialized by the full root script.
  new Function('pm', 'var __rn = "";\n' + contentScript)({
    request: {
      url: { toString: () => url },
      body: format === undefined ? undefined : {
        mode: 'formdata',
        formdata: { each: fn => fn({ key: 'response_format', value: format }) },
      },
    },
    response: {
      code: 200,
      headers: { get: () => contentType },
      json: () => typeof body === 'string' ? JSON.parse(body) : body,
      text: () => typeof body === 'string' ? body : JSON.stringify(body),
    },
    variables: { replaceIn: value => value },
    test: (_name, fn) => { try { fn(); } catch (error) { failures.push(error); } },
    expect,
  });
  return failures;
}

test('decision answers count as response content', () => {
  assert.equal(contentFailures({ answers: { category: { kind: 'choice', value: 'billing' } } }).length, 0);
});

test('empty decision answers still fail the content check', () => {
  assert.equal(contentFailures({ answers: {} }).length, 1);
});

const transcriptionUrl = 'http://localhost:8080/openai/v1/audio/transcriptions';

test('plain transcription formats count as content without JSON decoding', () => {
  for (const [format, contentType, body] of [
    ['text', 'text/plain; charset=utf-8', 'Hello.\n'],
    ['srt', 'application/x-subrip', '1\n00:00:00,000 --> 00:00:01,000\nHello.\n'],
    ['vtt', 'text/vtt', 'WEBVTT\n\n00:00.000 --> 00:01.000\nHello.\n'],
  ]) {
    assert.equal(contentFailures(body, { url: transcriptionUrl, format, contentType }).length, 0);
  }
});

test('plain transcription responses still require nonempty output', () => {
  assert.equal(contentFailures(' \n', { url: transcriptionUrl, format: 'text', contentType: 'text/plain' }).length, 1);
});

test('plain transcription formats do not accept a JSON response MIME type', () => {
  assert.equal(contentFailures({ text: 'Hello.' }, { url: transcriptionUrl, format: 'text' }).length, 1);
});

test('plain transcription formats reject HTML MIME types', () => {
  assert.equal(contentFailures('<html>Error</html>', { url: transcriptionUrl, format: 'text', contentType: 'text/html' }).length, 1);
});

test('JSON transcription responses retain JSON content validation', () => {
  assert.equal(contentFailures({ text: 'Hello.' }, { url: transcriptionUrl, format: 'json' }).length, 0);
  assert.equal(contentFailures('Not JSON', { url: transcriptionUrl, format: 'json' }).length, 1);
  assert.equal(contentFailures('Not JSON', { url: transcriptionUrl }).length, 1);
});

test('non-transcription routes do not bypass JSON validation', () => {
  assert.equal(contentFailures('Not JSON', { url: 'http://localhost:8080/v1/chat/completions', format: 'text', contentType: 'text/plain' }).length, 1);
});
