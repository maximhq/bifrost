'use strict';

const assert = require('node:assert/strict');
const { EventEmitter } = require('node:events');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const vm = require('node:vm');

// Exercise the reporter's event handlers without database services or pricing downloads.
async function runCostingChecks(type) {
  const emitter = new EventEmitter();
  const output = [];
  const queries = [];
  const row = {
    id: 'first-log', cost: 0.1, total_tokens: 10, model: 'gpt-4o-mini', provider: 'openai', status: 'cancelled',
    metadata: { 'x-request-id': 'reused-id', 'dbverify-request-id': 'first-send' },
  };
  let close;
  const closed = new Promise((resolve) => { close = resolve; });
  function query(sql, params) {
    queries.push({ sql, params: Array.from(params) });
    const matches = sql.includes('WHERE id =') ? row.id === params[0]
      : row.metadata[sql.includes('dbverify-request-id') ? 'dbverify-request-id' : 'x-request-id'] === params[0];
    return matches ? [row] : [];
  }
  const modules = {
    pg: { Client: class {
      async connect() {}
      async query(sql, params) { return { rows: query(sql, params) }; }
      async end() { close(); }
    } },
    'better-sqlite3': class {
      prepare(sql) { return { all: (...params) => query(sql, params) }; }
      close() { close(); }
    },
    https: { get() { throw new Error('offline pricing fixture'); } },
  };
  const loaded = { exports: {} };
  vm.runInNewContext(fs.readFileSync(path.join(__dirname, 'index.js'), 'utf8'), {
    module: loaded,
    require: (name) => modules[name] || require(name),
    process: { env: {}, cwd: () => __dirname },
    console: { log: (line) => output.push(line), warn() {} },
    setTimeout: (fn) => fn(),
  });
  loaded.exports(emitter, { silent: true, config: path.join(__dirname, 'absent-config.json'),
    logsDbUrl: type === 'postgres' ? 'postgresql://fixture/logs' : 'sqlite:///fixture/logs.db' });
  emitter.emit('start', null);

  const requests = [];
  for (const [httpRequestId, logId] of [['first-send', null], ['second-send', null], ['third-send', 'first-log']]) {
    const headers = new Map([['x-bf-expect-cost', 'true'], ['x-request-id', 'reused-id']]);
    headers.upsert = ({ key, value }) => headers.set(key, value);
    const request = { headers };
    requests.push(request);
    emitter.emit('beforeRequest', null, { request, cursor: { httpRequestId } });
    emitter.emit('request', logId ? null : new Error('cancelled before headers'), {
      request, item: { name: httpRequestId }, response: logId ? { headers: new Map([['x-bifrost-request-id', logId]]) } : undefined,
    });
  }
  const ordinary = { headers: new Map([['x-request-id', 'ordinary-id']]) };
  emitter.emit('beforeRequest', null, { request: ordinary, cursor: { httpRequestId: 'ordinary-send' } });
  emitter.emit('done');
  await closed;
  return { output, queries, requests, ordinary };
}

for (const type of ['postgres', 'sqlite']) {
  test(`${type}: pre-header costing cannot reuse another request's row`, async () => {
    const { output, queries, requests, ordinary } = await runCostingChecks(type);
    assert.ok(output.some((line) => line.includes('DB Checks: 2 passed, 1 failed, 0 skipped')), output.join('\n'));
    assert.ok(output.some((line) => line.includes('No log row found for request_id=second-send')));
    assert.equal(requests[0].headers.get('x-bf-lh-dbverify-request-id'), 'first-send');
    assert.equal(requests[1].headers.get('x-bf-lh-dbverify-request-id'), 'second-send');
    assert.equal(requests[1].headers.get('x-request-id'), 'reused-id');
    assert.equal(ordinary.headers.get('x-bf-lh-dbverify-request-id'), undefined);
    const canonical = queries.find(({ sql }) => sql.includes('WHERE id ='));
    assert.deepEqual(canonical.params, ['first-log']);
    const fallback = queries.filter(({ sql }) => !sql.includes('WHERE id ='));
    assert.ok(fallback.every(({ sql }) => sql.includes('dbverify-request-id')));
    assert.ok(fallback.some(({ params }) => params[0] === 'second-send'));
  });
}
