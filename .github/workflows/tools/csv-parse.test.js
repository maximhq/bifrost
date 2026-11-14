const assert = require('node:assert/strict');
const { mkdtemp, writeFile, rm } = require('node:fs/promises');
const { createRequire } = require('node:module');
const { tmpdir } = require('node:os');
const { join } = require('node:path');
const { test } = require('node:test');
const { promisify } = require('node:util');
const { readFileSync } = require('node:fs');
const { generateKeyPairSync, createPrivateKey } = require('node:crypto');
const { createServer } = require('node:http');

const newmanRequire = createRequire(require.resolve('newman'));
const parse = promisify(newmanRequire('csv-parse'));
const loadOptions = promisify(require('newman/lib/run/options'));

test('duplicate __proto__ columns remain own data without replacing the prototype', async () => {
  const [row] = await parse('__proto__,__proto__,name\na,b,safe\n', {
    columns: true,
    group_columns_by_name: true,
  });
  assert.equal(Object.getPrototypeOf(row), Object.prototype);
  assert.equal(Object.hasOwn(row, '__proto__'), true);
  assert.deepEqual(row.__proto__, ['a', 'b']);
  assert.equal(row.name, 'safe');
});

test('Newman loads CSV iteration data with its legacy parser options', async (t) => {
  const dir = await mkdtemp(join(tmpdir(), 'newman-csv-'));
  t.after(() => rm(dir, { recursive: true, force: true }));
  const csv = join(dir, 'iterations.csv');
  await writeFile(csv, '\ufeffname,count,quoted,relaxed\r\n"hello, world",42,"007",a"b\r\nshort,3\r\n');
  const result = await loadOptions({
    collection: { info: { name: 'CSV compatibility' }, item: [] },
    iterationData: csv,
  });
  assert.deepEqual(result.iterationData, [
    { name: 'hello, world', count: 42, quoted: '007', relaxed: 'a"b' },
    { name: 'short', count: 3 },
  ]);
});

test('malformed CSV still returns a parser error through the callback', async () => {
  await assert.rejects(parse('name\n"unterminated', { columns: true }), {
    code: 'CSV_QUOTE_NOT_CLOSED',
  });
});

test('CI dependency tree excludes the unpatched node-forge and braces packages', () => {
  const lock = JSON.parse(readFileSync(join(__dirname, 'package-lock.json'), 'utf8'));
  for (const name of ['node-forge', 'braces']) {
    assert.equal(Object.entries(lock.packages).some(([path, entry]) =>
      path.endsWith(`node_modules/${name}`) && !entry.link), false,
      `${name} must be replaced rather than installed from the registry`);
  }
});

test('reporter glob helpers preserve exclusions, brace patterns, and matching options', () => {
  const helpersRequire = createRequire(require.resolve('@budibase/handlebars-helpers'));
  const match = helpersRequire('micromatch');
  const files = ['a.js', 'b.ts', 'c.md', '.hidden.js', 'nested/a.js'];
  assert.deepEqual(match(files, ['**/*.{js,ts}', '!**/b.ts']), ['a.js', 'nested/a.js']);
  assert.deepEqual(match(files, ['!**/*.md']), ['a.js', 'b.ts', '.hidden.js', 'nested/a.js']);
  assert.deepEqual(match(files, ['*.js', '!a.js', 'a.js']), ['a.js']);
  assert.equal(match.isMatch('TEST.JS', '*.js', { nocase: true }), true);
  assert.equal(match.matcher('*.js', { dot: true })('.hidden.js'), true);
  assert.equal(match.isMatch('a.ts', ['*.js', '*.ts']), true);
  assert.deepEqual(match(['a1.js', 'a2.js', 'a3.js'], 'a{1..2}.js'), ['a1.js', 'a2.js']);
  assert.throws(() => match(files, '*.missing', { failglob: true }), /No matches found/);
  assert.deepEqual(match(files, '*.missing', { nonull: true }), ['*.missing']);
});

test('reporter rejects excessively nested glob patterns before recursive parsing', () => {
  const helpersRequire = createRequire(require.resolve('@budibase/handlebars-helpers'));
  const match = helpersRequire('micromatch');
  const pattern = '{'.repeat(128) + 'a,b' + '}'.repeat(128);
  for (const run of [() => match(['a'], pattern), () => match.matcher(pattern), () => match.isMatch('a', pattern)]) {
    assert.throws(run, { name: 'RangeError', message: /nesting limit/ });
  }
});

test('Newman ASAP signs and verifies tokens with PEM and DER data URI private keys', async () => {
  const { Request } = require('postman-collection');
  const { jwtVerify } = require('jose');
  const asap = require('postman-runtime/lib/authorizer/asap');
  const { privateKey, publicKey } = generateKeyPairSync('rsa', { modulusLength: 2048 });
  const pkcs8 = privateKey.export({ type: 'pkcs8', format: 'pem' });
  for (const key of [pkcs8, ...['pkcs1', 'pkcs8'].map((type) =>
    `data:application/pkcs8;kid=test-key;base64,${privateKey.export({ type, format: 'der' }).toString('base64')}`)]) {
    const request = new Request({ url: 'https://example.test' });
    await new Promise((resolve, reject) => asap.sign({ get: () => ({
      kid: 'test-key', iss: 'ci', aud: 'test', privateKey: key,
    }) }, request, (error) => error ? reject(error) : resolve()));
    const token = request.headers.get('Authorization').replace(/^Bearer /, '');
    const verified = await jwtVerify(token, publicKey, { issuer: 'ci', audience: 'test' });
    assert.equal(verified.protectedHeader.kid, 'test-key');
  }
  // Native key parsing must preserve the original RSA key material.
  const forge = createRequire(require.resolve('postman-runtime'))('node-forge');
  const key = forge.pki.privateKeyFromAsn1(forge.asn1.fromDer(forge.util.decode64(
    privateKey.export({ type: 'pkcs8', format: 'der' }).toString('base64'))));
  assert.deepEqual(createPrivateKey(forge.pki.privateKeyToPem(key)).export({ type: 'pkcs8', format: 'der' }),
    privateKey.export({ type: 'pkcs8', format: 'der' }));
  const request = new Request({ url: 'https://example.test' });
  await assert.rejects(new Promise((resolve, reject) => asap.sign({ get: () => ({
    kid: 'test-key', iss: 'ci', aud: 'test', privateKey: 'data:application/pkcs8;kid=test-key;base64,AAAA',
  }) }, request, (error) => error ? reject(error) : resolve())), /Failed to parse private key/);
  assert.equal(request.headers.get('Authorization'), undefined);
});

test('Newman produces an HTML report with the compatibility dependencies', async (t) => {
  const dir = await mkdtemp(join(tmpdir(), 'newman-report-'));
  t.after(() => rm(dir, { recursive: true, force: true }));
  const server = createServer((request, response) => {
    response.setHeader('Content-Type', 'application/json');
    response.end('{"ok":true}');
  });
  await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
  t.after(() => new Promise((resolve) => server.close(resolve)));
  const report = join(dir, 'report.html');
  const summary = await promisify(require('newman').run)({
    collection: {
      info: { name: 'CI dependency compatibility', schema: 'https://schema.getpostman.com/json/collection/v2.1.0/collection.json' },
      item: [{ name: 'Local smoke request', request: `http://127.0.0.1:${server.address().port}/`,
        event: [{ listen: 'test', script: { exec: ['pm.test("response is successful", () => pm.response.to.have.status(200));'] } }] }],
    },
    reporters: ['htmlextra', 'json'],
    reporter: { htmlextra: { export: report }, json: { export: join(dir, 'report.json') } },
  });
  assert.equal(summary.run.failures.length, 0);
  const html = readFileSync(report, 'utf8');
  assert.match(html, /CI dependency compatibility/);
  assert.match(html, /Local smoke request/);
  assert.match(html, /response is successful/);
});
