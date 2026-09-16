const assert = require('node:assert/strict');
const { mkdtemp, writeFile, rm } = require('node:fs/promises');
const { createRequire } = require('node:module');
const { tmpdir } = require('node:os');
const { join } = require('node:path');
const { test } = require('node:test');
const { promisify } = require('node:util');

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
