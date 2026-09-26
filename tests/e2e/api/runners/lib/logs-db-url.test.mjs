import assert from 'node:assert/strict';
import test from 'node:test';
import { createRequire } from 'node:module';
const require = createRequire(import.meta.url);
const { logsDbUrlFromConfig, readLogsDbUrl } = require('../../lib/logs-db-url.js');

test('relative SQLite paths resolve from the server working directory', () => {
  assert.equal(logsDbUrlFromConfig({logs_store: {enabled: true, type: 'sqlite', config: {path: 'tests/integrations/python/logs.db'}}}, '/repo/transports/bifrost-http'),
    'sqlite:///repo/transports/bifrost-http/tests/integrations/python/logs.db');
});

test('absolute SQLite paths and env paths are preserved', () => {
  const config = path => ({logs_store: {enabled: true, type: 'sqlite', config: {path}}});
  assert.equal(logsDbUrlFromConfig(config('/data/logs.db'), '/repo'), 'sqlite:///data/logs.db');
  assert.equal(logsDbUrlFromConfig(config('env.LOG_PATH'), '/repo', {LOG_PATH: '/data/logs.db'}), 'sqlite:///data/logs.db');
});

test('Postgres configuration resolves env values and escapes credentials', () => {
  assert.equal(logsDbUrlFromConfig({logs_store: {enabled: true, type: 'postgres', config: {host: 'db', user: 'tester', password: 'env.PASS', db_name: 'logs'}}}, '/repo', {PASS: 'a@b'}),
    'postgresql://tester:a%40b@db:5432/logs?sslmode=disable');
});

test('disabled or absent log stores do not invent a SQLite database', () => {
  assert.equal(logsDbUrlFromConfig({}, '/repo'), '');
  assert.equal(logsDbUrlFromConfig({logs_store: {enabled: false}}, '/repo'), '');
});


test('explicit database URL takes priority even when config is unavailable', () => {
  assert.equal(readLogsDbUrl('/missing/config.json', '/repo', {BIFROST_LOGS_DB_URL: 'sqlite:///override/logs.db'}), 'sqlite:///override/logs.db');
});
