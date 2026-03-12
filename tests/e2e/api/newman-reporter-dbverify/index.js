'use strict';

/**
 * Newman DB Verifier Reporter
 *
 * After each 2xx API response, fires SQL queries to verify that CRUD operations
 * are correctly reflected in the database (PostgreSQL or SQLite).
 *
 * Main DB connection is resolved in this order:
 *   1. --reporter-dbverify-db-url  (explicit DSN)
 *   2. BIFROST_DB_URL env var       (explicit DSN)
 *   3. --reporter-dbverify-config   (path to Bifrost config.json; auto-detects type + DSN)
 *   4. ./config.json                (auto-discovered in cwd)
 *
 * Logs DB connection (for logs/mcp-logs endpoints) is resolved in this order:
 *   1. --reporter-dbverify-logs-db-url  (explicit DSN)
 *   2. BIFROST_LOGS_DB_URL env var
 *   3. Same config.json as above (reads logs_store section)
 *
 * Supported DSN formats:
 *   postgresql://user:pass@host:port/db[?sslmode=...]
 *   sqlite:///absolute/path/to/file.db
 *   sqlite://relative/path/to/file.db
 *   /absolute/path/to/file.db  (bare path → treated as SQLite)
 *
 * Other options:
 *   --reporter-dbverify-silent  Suppress per-request log lines
 */

const fs   = require('fs');
const path = require('path');

// ─── Bifrost config.json reader ───────────────────────────────────────────────

/**
 * Resolve an EnvVar field from Bifrost config.
 * Values can be a plain string, an "env.KEY" reference, or an explicit
 * {"value": "...", "env_var": "..."} object.
 */
function resolveEnvVar(val) {
  if (val == null) return '';
  if (typeof val === 'object') {
    const v = val.value || '';
    if (v.startsWith('env.')) return process.env[v.slice(4)] || '';
    return v;
  }
  const s = String(val);
  if (s.startsWith('env.')) return process.env[s.slice(4)] || '';
  return s;
}

function sqliteUrlFromPath(filePath, configPath) {
  const resolved = path.isAbsolute(filePath)
    ? filePath
    : path.resolve(path.dirname(configPath), filePath);
  return `sqlite://${resolved}`;
}

function postgresUrlFromConfig(c) {
  const host     = resolveEnvVar(c.host)     || 'localhost';
  const port     = resolveEnvVar(c.port)     || '5432';
  const user     = resolveEnvVar(c.user)     || 'bifrost';
  const password = resolveEnvVar(c.password) || '';
  const dbName   = resolveEnvVar(c.db_name)  || 'bifrost';
  const sslMode  = resolveEnvVar(c.ssl_mode) || 'disable';
  return `postgresql://${user}:${encodeURIComponent(password)}@${host}:${port}/${dbName}?sslmode=${sslMode}`;
}

/**
 * Read a Bifrost config.json and return a DB connection URL for the main
 * config_store, or null if not enabled / unreadable.
 */
function dbUrlFromBifrostConfig(configPath) {
  let raw;
  try { raw = fs.readFileSync(configPath, 'utf8'); }
  catch (_) { return null; }

  let cfg;
  try { cfg = JSON.parse(raw); }
  catch (_) { return null; }

  const cs = cfg.config_store;
  if (!cs || !cs.enabled) return null;

  if (cs.type === 'sqlite') {
    const filePath = resolveEnvVar(cs.config && cs.config.path);
    if (!filePath) return null;
    return sqliteUrlFromPath(filePath, configPath);
  }

  if (cs.type === 'postgres') {
    return postgresUrlFromConfig(cs.config || {});
  }

  return null;
}

/**
 * Read a Bifrost config.json and return a DB connection URL for the logs_store,
 * or null if not enabled / unreadable.
 */
function logsDbUrlFromBifrostConfig(configPath) {
  let raw;
  try { raw = fs.readFileSync(configPath, 'utf8'); }
  catch (_) { return null; }

  let cfg;
  try { cfg = JSON.parse(raw); }
  catch (_) { return null; }

  const ls = cfg.logs_store;
  if (!ls || !ls.enabled) return null;

  if (ls.type === 'sqlite') {
    const filePath = resolveEnvVar(ls.config && ls.config.path);
    if (!filePath) return null;
    return sqliteUrlFromPath(filePath, configPath);
  }

  if (ls.type === 'postgres') {
    return postgresUrlFromConfig(ls.config || {});
  }

  return null;
}

// ─── DB type detection ────────────────────────────────────────────────────────

function detectDbType(url) {
  if (/^postgres(ql)?:\/\//i.test(url)) return 'postgres';
  return 'sqlite';
}

function resolveSqlitePath(url) {
  return url.replace(/^sqlite:\/\//i, '');
}

// ─── DB backend abstraction ───────────────────────────────────────────────────

async function createDbClient(url) {
  const type = detectDbType(url);

  if (type === 'postgres') {
    let pg;
    try { pg = require('pg'); }
    catch (_) {
      console.warn('[dbverify] pg module not found. Run: npm install in tests/e2e/api/');
      return null;
    }
    const pgClient = new pg.Client({ connectionString: url });
    await pgClient.connect();
    return {
      type: 'postgres',
      query: async (sql, params) => {
        const res = await pgClient.query(sql, params);
        return { rows: res.rows, rowCount: res.rowCount };
      },
      close: () => pgClient.end().catch(() => {}),
    };
  }

  // SQLite
  let Database;
  try { Database = require('better-sqlite3'); }
  catch (_) {
    console.warn('[dbverify] better-sqlite3 not found. Run: npm install in tests/e2e/api/');
    return null;
  }
  const filePath = resolveSqlitePath(url);
  const db = new Database(filePath, { readonly: true });
  return {
    type: 'sqlite',
    query: async (sql, params) => {
      const rows = db.prepare(sql.replace(/\$\d+/g, '?')).all(...params);
      return { rows, rowCount: rows.length };
    },
    close: () => { try { db.close(); } catch (_) {} },
  };
}

// ─── URL → Table mapping ──────────────────────────────────────────────────────
//
// logsDb: true  →  query is routed to the logs DB (logs_store) instead of the
//                  main config DB (config_store).

const URL_TABLE_MAP = [
  // ── Specific (id-bearing) patterns — matched before collection patterns ────

  {
    pattern: /\/api\/governance\/customers\/([^/?#]+)/,
    table: 'governance_customers', idParam: 1, idColumn: 'id',
    verifyFields: ['id', 'name'],
    bodyId:     (b) => b && (b.id || (b.customer && b.customer.id)),
    bodyFields: (b) => b && (b.customer || b),
  },
  {
    pattern: /\/api\/governance\/teams\/([^/?#]+)/,
    table: 'governance_teams', idParam: 1, idColumn: 'id',
    verifyFields: ['id', 'name', 'customer_id'],
    bodyId:     (b) => b && (b.id || (b.team && b.team.id)),
    bodyFields: (b) => b && (b.team || b),
  },
  {
    pattern: /\/api\/governance\/virtual-keys\/([^/?#]+)/,
    table: 'governance_virtual_keys', idParam: 1, idColumn: 'id',
    verifyFields: ['id', 'name', 'is_active'],
    bodyId:     (b) => b && (b.id || (b.virtual_key && b.virtual_key.id)),
    bodyFields: (b) => b && (b.virtual_key || b),
  },
  {
    pattern: /\/api\/governance\/routing-rules\/([^/?#]+)/,
    table: 'routing_rules', idParam: 1, idColumn: 'id',
    verifyFields: ['id', 'name', 'enabled', 'provider', 'scope'],
    bodyId:     (b) => b && (b.id || (b.rule && b.rule.id)),
    bodyFields: (b) => b && (b.rule || b),
  },
  {
    pattern: /\/api\/governance\/model-configs\/([^/?#]+)/,
    table: 'governance_model_configs', idParam: 1, idColumn: 'id',
    verifyFields: ['id', 'model_name', 'provider'],
    bodyId:     (b) => b && (b.id || (b.model_config && b.model_config.id)),
    bodyFields: (b) => b && (b.model_config || b),
  },
  {
    pattern: /\/api\/governance\/providers\/([^/?#]+)/,
    table: 'config_providers', idParam: 1, idColumn: 'name',
    verifyFields: ['name'],
    bodyId:     (b) => b && (b.provider && (b.provider.Provider || b.provider.name)),
    bodyFields: (b) => b && b.provider && { name: b.provider.Provider || b.provider.name },
    deleteVerifiesExists: true,
  },
  {
    pattern: /\/api\/providers\/([^/?#]+)/,
    table: 'config_providers', idParam: 1, idColumn: 'name',
    verifyFields: ['name', 'status', 'send_back_raw_request', 'send_back_raw_response'],
    bodyId:     (b) => b && (b.name || (b.provider && b.provider.name)),
    bodyFields: (b) => b && (b.provider || b),
  },
  {
    pattern: /\/api\/mcp\/client\/([^/?#]+)/,
    table: 'config_mcp_clients', idParam: 1, idColumn: 'client_id',
    verifyFields: ['client_id', 'name', 'connection_type'],
    bodyId:     (b) => b && (b.client_id || (b.mcp_client && b.mcp_client.client_id)),
    bodyFields: (b) => b && (b.mcp_client || b),
  },
  {
    pattern: /\/api\/logs\/([^/?#]+)/,
    table: 'logs', idParam: 1, idColumn: 'id', logsDb: true,
    verifyFields: ['id'],
    bodyId:     (b) => b && (b.id || (b.log && b.log.id)),
    bodyFields: (b) => b && (b.log || b),
  },
  {
    pattern: /\/api\/mcp-logs\/([^/?#]+)/,
    table: 'mcp_tool_logs', idParam: 1, idColumn: 'id', logsDb: true,
    verifyFields: ['id'],
    bodyId:     (b) => b && (b.id || (b.log && b.log.id)),
    bodyFields: (b) => b && (b.log || b),
  },
  {
    pattern: /\/api\/plugins\/([^/?#]+)/,
    table: 'config_plugins', idParam: 1, idColumn: 'name',
    verifyFields: ['name', 'enabled', 'path'],
    bodyId:     (b) => b && (b.name || (b.plugin && b.plugin.name)),
    bodyFields: (b) => b && (b.plugin || b),
  },

  // ── Collection / aggregate endpoints ──────────────────────────────────────

  {
    pattern: /\/api\/governance\/customers$/,
    table: 'governance_customers', idParam: null, idColumn: 'id',
    verifyFields: ['id', 'name'],
    bodyId:     (b) => b && (b.id || (b.customer && b.customer.id)),
    bodyFields: (b) => b && (b.customer || b),
  },
  {
    pattern: /\/api\/governance\/teams$/,
    table: 'governance_teams', idParam: null, idColumn: 'id',
    verifyFields: ['id', 'name', 'customer_id'],
    bodyId:     (b) => b && (b.id || (b.team && b.team.id)),
    bodyFields: (b) => b && (b.team || b),
  },
  {
    pattern: /\/api\/governance\/virtual-keys$/,
    table: 'governance_virtual_keys', idParam: null, idColumn: 'id',
    verifyFields: ['id', 'name', 'is_active'],
    bodyId:     (b) => b && (b.id || (b.virtual_key && b.virtual_key.id)),
    bodyFields: (b) => b && (b.virtual_key || b),
  },
  {
    pattern: /\/api\/governance\/routing-rules$/,
    table: 'routing_rules', idParam: null, idColumn: 'id',
    verifyFields: ['id', 'name', 'enabled', 'provider', 'scope'],
    bodyId:     (b) => b && (b.id || (b.rule && b.rule.id)),
    bodyFields: (b) => b && (b.rule || b),
  },
  {
    pattern: /\/api\/governance\/model-configs$/,
    table: 'governance_model_configs', idParam: null, idColumn: 'id',
    verifyFields: ['id', 'model_name', 'provider'],
    bodyId:     (b) => b && (b.id || (b.model_config && b.model_config.id)),
    bodyFields: (b) => b && (b.model_config || b),
  },
  {
    pattern: /\/api\/providers$/,
    table: 'config_providers', idParam: null, idColumn: 'name',
    verifyFields: ['name', 'status', 'send_back_raw_request', 'send_back_raw_response'],
    bodyId:     (b) => b && (b.name || (b.provider && b.provider.name)),
    bodyFields: (b) => b && (b.provider || b),
  },
  {
    pattern: /\/api\/plugins$/,
    table: 'config_plugins', idParam: null, idColumn: 'name',
    verifyFields: ['name', 'enabled', 'path'],
    bodyId:     (b) => b && (b.name || (b.plugin && b.plugin.name)),
    bodyFields: (b) => b && (b.plugin || b),
  },
  {
    pattern: /\/api\/mcp\/client$/,
    table: 'config_mcp_clients', idParam: null, idColumn: 'client_id',
    verifyFields: ['client_id', 'name', 'connection_type'],
    bodyId:     (b) => b && (b.client_id || (b.mcp_client && b.mcp_client.client_id)),
    bodyFields: (b) => b && (b.mcp_client || b),
  },
  {
    pattern: /\/api\/mcp\/clients$/,
    table: 'config_mcp_clients', idParam: null, idColumn: 'client_id',
    verifyFields: ['client_id', 'name', 'connection_type'],
    bodyId:     (b) => b && (b.client_id || (b.mcp_client && b.mcp_client.client_id)),
    bodyFields: (b) => b && (b.mcp_client || b),
  },
  // Proxy config — stored as a JSON blob in governance_config.value under key "proxy_config"
  // PUT response is {"status":"success",...} so we compare the request body against the DB blob.
  // GET response is the proxy config object directly, which also works with jsonBlobColumn.
  {
    pattern: /\/api\/proxy-config$/,
    table: 'governance_config', idParam: null, idColumn: 'key',
    jsonBlobColumn: 'value',
    useRequestBody: true,
    verifyFields: ['enabled', 'type', 'url', 'timeout', 'enable_for_inference', 'enable_for_api', 'enable_for_scim'],
    bodyId:     () => 'proxy_config',
    bodyFields: (b) => b,
  },

  // Config — client_config in config_client, framework_config in framework_configs (multi-table)
  {
    pattern: /\/api\/config$/,
    multiTable: [
      {
        table: 'config_client',
        idColumn: 'id',
        verifyFields: ['drop_excess_requests', 'log_retention_days', 'mcp_agent_depth', 'mcp_tool_execution_timeout'],
        bodyFields: (b) => b && b.client_config,
      },
      {
        table: 'framework_configs',
        idColumn: 'id',
        verifyFields: ['pricing_url', 'pricing_sync_interval'],
        bodyFields: (b) => b && b.framework_config,
      },
    ],
    useRequestBody: true,
    bodyId: () => null,
    bodyFields: () => null,
  },

  // Version — build-time constant, not stored in DB
  {
    pattern: /\/api\/version$/,
    skipReason: 'version is build-time constant, not stored in DB',
  },

  // Read-only table-accessible endpoints (COUNT check)
  { pattern: /\/api\/keys$/,                    table: 'config_keys', idParam: null, idColumn: 'id', verifyFields: [], bodyId: () => null, bodyFields: () => null },
  { pattern: /\/api\/models$/,                  table: 'config_providers', idParam: null, idColumn: 'name', verifyFields: [], bodyId: () => null, bodyFields: () => null },
  { pattern: /\/api\/models\/base$/,           table: 'config_providers', idParam: null, idColumn: 'name', verifyFields: [], bodyId: () => null, bodyFields: () => null },
  { pattern: /\/api\/governance\/budgets$/,     table: 'governance_budgets', idParam: null, idColumn: 'id', verifyFields: [], bodyId: () => null, bodyFields: () => null },
  { pattern: /\/api\/governance\/rate-limits$/, table: 'governance_rate_limits', idParam: null, idColumn: 'id', verifyFields: [], bodyId: () => null, bodyFields: () => null },
  { pattern: /\/api\/governance\/providers$/,   table: 'config_providers', idParam: null, idColumn: 'name', verifyFields: [], bodyId: () => null, bodyFields: () => null },

  // Logs aggregate endpoints — verify the table is accessible (COUNT check)
  { pattern: /\/api\/logs\/stats$/,                    table: 'logs', idParam: null, idColumn: 'id', logsDb: true, verifyFields: [], bodyId: () => null, bodyFields: () => null },
  { pattern: /\/api\/logs\/histogram$/,                table: 'logs', idParam: null, idColumn: 'id', logsDb: true, verifyFields: [], bodyId: () => null, bodyFields: () => null },
  { pattern: /\/api\/logs\/histogram\/tokens$/,        table: 'logs', idParam: null, idColumn: 'id', logsDb: true, verifyFields: [], bodyId: () => null, bodyFields: () => null },
  { pattern: /\/api\/logs\/histogram\/cost$/,          table: 'logs', idParam: null, idColumn: 'id', logsDb: true, verifyFields: [], bodyId: () => null, bodyFields: () => null },
  { pattern: /\/api\/logs\/histogram\/models$/,        table: 'logs', idParam: null, idColumn: 'id', logsDb: true, verifyFields: [], bodyId: () => null, bodyFields: () => null },
  { pattern: /\/api\/logs\/histogram\/latency$/,       table: 'logs', idParam: null, idColumn: 'id', logsDb: true, verifyFields: [], bodyId: () => null, bodyFields: () => null },
  { pattern: /\/api\/logs\/histogram\/cost\/by-provider$/,     table: 'logs', idParam: null, idColumn: 'id', logsDb: true, verifyFields: [], bodyId: () => null, bodyFields: () => null },
  { pattern: /\/api\/logs\/histogram\/tokens\/by-provider$/,   table: 'logs', idParam: null, idColumn: 'id', logsDb: true, verifyFields: [], bodyId: () => null, bodyFields: () => null },
  { pattern: /\/api\/logs\/histogram\/latency\/by-provider$/,  table: 'logs', idParam: null, idColumn: 'id', logsDb: true, verifyFields: [], bodyId: () => null, bodyFields: () => null },
  { pattern: /\/api\/logs\/filterdata$/,               table: 'logs', idParam: null, idColumn: 'id', logsDb: true, verifyFields: [], bodyId: () => null, bodyFields: () => null },
  { pattern: /\/api\/logs\/dropped$/,          table: 'logs', idParam: null, idColumn: 'id', logsDb: true, verifyFields: [], bodyId: () => null, bodyFields: () => null },
  { pattern: /\/api\/logs\/recalculate-cost$/, table: 'logs', idParam: null, idColumn: 'id', logsDb: true, verifyFields: [], bodyId: () => null, bodyFields: () => null },
  { pattern: /\/api\/logs$/,                   table: 'logs', idParam: null, idColumn: 'id', logsDb: true, verifyFields: [], bodyId: () => null, bodyFields: () => null },
  // MCP logs aggregate endpoints
  { pattern: /\/api\/mcp-logs\/stats$/,      table: 'mcp_tool_logs', idParam: null, idColumn: 'id', logsDb: true, verifyFields: [], bodyId: () => null, bodyFields: () => null },
  { pattern: /\/api\/mcp-logs\/filterdata$/, table: 'mcp_tool_logs', idParam: null, idColumn: 'id', logsDb: true, verifyFields: [], bodyId: () => null, bodyFields: () => null },
  { pattern: /\/api\/mcp-logs$/,             table: 'mcp_tool_logs', idParam: null, idColumn: 'id', logsDb: true, verifyFields: [], bodyId: () => null, bodyFields: () => null },
];

function matchMapping(urlPath) {
  // Sort priority (highest first):
  //   1. Literal patterns with no capturing groups  – e.g. /api/mcp-logs/stats$
  //      These are more specific than wildcard captures and must be tried first.
  //   2. Wildcard id-bearing patterns               – e.g. /api/mcp-logs/([^/?#]+)
  //   3. Collection / aggregate patterns            – e.g. /api/mcp-logs$
  const sorted = [...URL_TABLE_MAP].sort((a, b) => {
    const aHasCapture = /\([^)]+\)/.test(a.pattern.source);
    const bHasCapture = /\([^)]+\)/.test(b.pattern.source);
    if (aHasCapture !== bHasCapture) return aHasCapture ? 1 : -1;
    return (b.idParam !== null ? 1 : 0) - (a.idParam !== null ? 1 : 0);
  });
  for (const mapping of sorted) {
    const m = urlPath.match(mapping.pattern);
    if (m) return { mapping, urlId: mapping.idParam !== null ? m[mapping.idParam] : null };
  }
  return null;
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

function parseBody(response) {
  try { return JSON.parse(response.stream ? response.stream.toString() : ''); }
  catch (_) { return null; }
}

function parseRequestBody(request) {
  try {
    const raw = request && request.body && request.body.raw;
    if (!raw) return null;
    const str = typeof raw === 'string' ? raw : (raw && raw.toString ? raw.toString() : '');
    return str ? JSON.parse(str) : null;
  } catch (_) {
    return null;
  }
}

/** Normalize DB/JSON values for comparison. SQLite/Postgres return booleans as 0/1; API returns false/true. */
function valuesEqual(dbVal, respVal) {
  if (dbVal === respVal) return true;
  if (String(dbVal) === String(respVal)) return true;
  // Boolean: 0/1 (DB) vs false/true (JSON)
  const dbBool = dbVal === 1 || dbVal === true || (typeof dbVal === 'string' && /^true|1$/i.test(dbVal));
  const respBool = respVal === 1 || respVal === true || (typeof respVal === 'string' && /^true|1$/i.test(respVal));
  const dbIsBoolLike = dbVal === 0 || dbVal === 1 || dbVal === true || dbVal === false || (typeof dbVal === 'string' && /^true|false|0|1$/i.test(dbVal));
  const respIsBoolLike = respVal === 0 || respVal === 1 || respVal === true || respVal === false || (typeof respVal === 'string' && /^true|false|0|1$/i.test(respVal));
  if (dbIsBoolLike && respIsBoolLike) return dbBool === respBool;
  return false;
}

function checkFieldMismatches(dbRow, respFields, verifyFields) {
  if (!respFields || typeof respFields !== 'object') return [];
  return verifyFields
    .filter(f => f in respFields)
    .filter(f => !valuesEqual(dbRow[f], respFields[f]))
    .map(f => `${f}: db=${dbRow[f]} resp=${respFields[f]}`);
}

/**
 * Like checkFieldMismatches but the dbRow has a single JSON blob column.
 * Parses dbRow[jsonBlobColumn] as JSON and compares fields against respFields.
 */
function checkJsonBlobMismatches(dbRow, jsonBlobColumn, respFields, verifyFields) {
  if (!respFields || typeof respFields !== 'object') return [];
  let blob = {};
  try { blob = JSON.parse(dbRow[jsonBlobColumn] || '{}'); } catch (_) {}
  return verifyFields
    .filter(f => f in respFields)
    .filter(f => !valuesEqual(blob[f], respFields[f]))
    .map(f => `${f}: db=${blob[f]} resp=${respFields[f]}`);
}

function pad(str, len) {
  str = String(str || '');
  return str.length >= len ? str : str + ' '.repeat(len - str.length);
}

// ─── Verification handlers ────────────────────────────────────────────────────

async function verifyCreated(db, m, id, body, name) {
  if (!id) return { name, result: 'SKIP', detail: 'No record ID in response' };
  const selectCols = m.jsonBlobColumn ? m.jsonBlobColumn
    : (m.verifyFields.length ? m.verifyFields.join(', ') : m.idColumn);
  const { rows } = await db.query(
    `SELECT ${selectCols} FROM ${m.table} WHERE ${m.idColumn} = $1`, [id]);
  if (!rows.length) return { name, result: 'FAIL', detail: `Row NOT found in ${m.table} where ${m.idColumn}=${id}` };
  const mm = m.jsonBlobColumn
    ? checkJsonBlobMismatches(rows[0], m.jsonBlobColumn, m.bodyFields(body), m.verifyFields)
    : checkFieldMismatches(rows[0], m.bodyFields(body), m.verifyFields);
  if (mm.length) return { name, result: 'FAIL', detail: `Field mismatch: ${mm.join(', ')}` };
  return { name, result: 'PASS', detail: `Row created in ${m.table}: ${m.idColumn}=${id}` };
}

async function verifyExists(db, m, id, body, name) {
  if (!id) {
    const { rows } = await db.query(`SELECT COUNT(*) AS cnt FROM ${m.table}`, []);
    const cnt = rows[0].cnt !== undefined ? rows[0].cnt : rows[0].count;
    return { name, result: 'PASS', detail: `${m.table} accessible, ${cnt} rows` };
  }
  const selectCols = m.jsonBlobColumn ? m.jsonBlobColumn
    : (m.verifyFields.length ? m.verifyFields.join(', ') : m.idColumn);
  const { rows } = await db.query(
    `SELECT ${selectCols} FROM ${m.table} WHERE ${m.idColumn} = $1`, [id]);
  if (!rows.length) return { name, result: 'FAIL', detail: `Row NOT found in ${m.table} where ${m.idColumn}=${id}` };
  const mm = m.jsonBlobColumn
    ? checkJsonBlobMismatches(rows[0], m.jsonBlobColumn, m.bodyFields(body), m.verifyFields)
    : checkFieldMismatches(rows[0], m.bodyFields(body), m.verifyFields);
  if (mm.length) return { name, result: 'FAIL', detail: `Field mismatch: ${mm.join(', ')}` };
  return { name, result: 'PASS', detail: `Record verified in ${m.table}: ${m.idColumn}=${id}` };
}

async function verifyUpdated(db, m, id, body, name) {
  if (!id) return { name, result: 'SKIP', detail: 'No record ID in response' };
  const selectCols = m.jsonBlobColumn ? m.jsonBlobColumn
    : (m.verifyFields.length ? m.verifyFields.join(', ') : m.idColumn);
  const { rows } = await db.query(
    `SELECT ${selectCols} FROM ${m.table} WHERE ${m.idColumn} = $1`, [id]);
  if (!rows.length) return { name, result: 'FAIL', detail: `Row NOT found in ${m.table} where ${m.idColumn}=${id}` };
  const mm = m.jsonBlobColumn
    ? checkJsonBlobMismatches(rows[0], m.jsonBlobColumn, m.bodyFields(body), m.verifyFields)
    : checkFieldMismatches(rows[0], m.bodyFields(body), m.verifyFields);
  if (mm.length) return { name, result: 'FAIL', detail: `Update NOT reflected: ${mm.join(', ')}` };
  return { name, result: 'PASS', detail: `Record updated in ${m.table}: ${m.idColumn}=${id}` };
}

/** Verify a single-row table was updated (no id in URL). SELECT LIMIT 1 and compare. */
async function verifyUpdatedSingleRow(db, table, idColumn, verifyFields, bodyFields, body, name) {
  const respFields = bodyFields && bodyFields(body);
  const selectCols = verifyFields.length ? verifyFields.join(', ') : idColumn;
  const { rows } = await db.query(
    `SELECT ${selectCols} FROM ${table} LIMIT 1`, []);
  if (!rows.length) return { name, result: 'FAIL', detail: `No row in ${table}` };
  const mm = checkFieldMismatches(rows[0], respFields, verifyFields);
  if (mm.length) return { name, result: 'FAIL', detail: `Update NOT reflected in ${table}: ${mm.join(', ')}` };
  return { name, result: 'PASS', detail: `Record updated in ${table}` };
}

async function verifyDeleted(db, m, id, name) {
  if (!id) return { name, result: 'SKIP', detail: 'No record ID extractable from DELETE URL' };
  const { rows } = await db.query(
    `SELECT COUNT(*) AS cnt FROM ${m.table} WHERE ${m.idColumn} = $1`, [id]);
  const cnt = parseInt(rows[0].cnt !== undefined ? rows[0].cnt : rows[0].count, 10);
  if (cnt > 0) return { name, result: 'FAIL', detail: `Row still exists in ${m.table}: ${m.idColumn}=${id}` };
  return { name, result: 'PASS', detail: `Row removed from ${m.table}: ${m.idColumn}=${id}` };
}

async function runVerification(db, method, mapping, id, body, name) {
  switch (method) {
    case 'POST':   return verifyCreated(db, mapping, id, body, name);
    case 'GET':    return verifyExists(db, mapping, id, body, name);
    case 'PUT':
    case 'PATCH':  return verifyUpdated(db, mapping, id, body, name);
    case 'DELETE':
      if (mapping.deleteVerifiesExists) return verifyExists(db, mapping, id, body, name);
      return verifyDeleted(db, mapping, id, name);
    default:       return { name, result: 'SKIP', detail: `Method ${method} not verified` };
  }
}

/** Run verification for multi-table mappings (e.g. /api/config). */
async function runMultiTableVerification(db, method, mapping, body, name) {
  const tables = mapping.multiTable;
  const results = [];
  for (const t of tables) {
    if (method === 'GET') {
      const syntheticMapping = { table: t.table, idParam: null, idColumn: t.idColumn, verifyFields: [], bodyId: () => null, bodyFields: () => null };
      const r = await verifyExists(db, syntheticMapping, null, null, name);
      results.push(r);
    } else if (method === 'PUT' || method === 'PATCH') {
      const r = await verifyUpdatedSingleRow(db, t.table, t.idColumn, t.verifyFields || [], t.bodyFields, body, name);
      results.push(r);
    } else {
      results.push({ name, result: 'SKIP', detail: `Method ${method} not verified for multi-table` });
    }
  }
  const failed = results.filter((r) => r.result === 'FAIL');
  const passed = results.filter((r) => r.result === 'PASS');
  if (failed.length > 0) return { name, result: 'FAIL', detail: failed.map((f) => f.detail).join('; ') };
  if (passed.length === 0) return results[0] || { name, result: 'SKIP', detail: 'No verifications run' };
  const tableNames = tables.map((t) => t.table).join(', ');
  return { name, result: 'PASS', detail: `${tableNames} verified` };
}

/**
 * Process a single request's DB verification (immediate or from queue).
 * Handles bulk DELETE, tracks promises, pushes results.
 */
function processRequestVerification(opts) {
  const {
    activeDb, method, mapping, urlId, responseBody, name, request,
    pendingVerifications, results, silent,
  } = opts;

  // When useRequestBody is set, prefer the parsed request body for field comparison
  // (e.g. PUT endpoints that return a generic success response rather than the resource).
  // For GET requests the body is null so it naturally falls back to responseBody.
  const verifyBody = (mapping.useRequestBody && parseRequestBody(request)) || responseBody;

  // Multi-table verification (e.g. /api/config → config_client + framework_configs)
  if (mapping.multiTable) {
    const p = runMultiTableVerification(activeDb, method, mapping, verifyBody, name);
    pendingVerifications.push(p);
    p.then((r) => {
      results.push(r);
      if (!silent) {
        const icon = r.result === 'PASS' ? '✓' : r.result === 'SKIP' ? '~' : '✗';
        console.log(`[dbverify] ${icon} ${r.name}: ${r.detail}`);
      }
    }).catch((e) => {
      const r = { name, result: 'FAIL', detail: `Query error: ${e.message}` };
      results.push(r);
      if (!silent) console.log(`[dbverify] ✗ ${name}: ${r.detail}`);
    });
    return;
  }

  let recordId = urlId || (verifyBody && mapping.bodyId(verifyBody));

  // Bulk DELETE: extract ids from request body
  if (method === 'DELETE' && !recordId) {
    const reqBody = parseRequestBody(request);
    const ids     = (reqBody && Array.isArray(reqBody.ids) && reqBody.ids.length > 0) ? reqBody.ids : null;
    if (ids) {
      ids.forEach((id, i) => {
        const p = runVerification(activeDb, method, mapping, id, verifyBody, ids.length > 1 ? `${name} [id=${id}]` : name);
        pendingVerifications.push(p);
        p.then((r) => {
          results.push(r);
          if (!silent) {
            const icon = r.result === 'PASS' ? '✓' : r.result === 'SKIP' ? '~' : '✗';
            console.log(`[dbverify] ${icon} ${r.name}: ${r.detail}`);
          }
        }).catch((e) => {
          const r = { name, result: 'FAIL', detail: `Query error: ${e.message}` };
          results.push(r);
          if (!silent) console.log(`[dbverify] ✗ ${name}: ${r.detail}`);
        });
      });
      return;
    }
  }

  const p = runVerification(activeDb, method, mapping, recordId, verifyBody, name);
  pendingVerifications.push(p);
  p.then((r) => {
    results.push(r);
    if (!silent) {
      const icon = r.result === 'PASS' ? '✓' : r.result === 'SKIP' ? '~' : '✗';
      console.log(`[dbverify] ${icon} ${r.name}: ${r.detail}`);
    }
  }).catch((e) => {
    const r = { name, result: 'FAIL', detail: `Query error: ${e.message}` };
    results.push(r);
    if (!silent) console.log(`[dbverify] ✗ ${name}: ${r.detail}`);
  });
}

// ─── Summary ──────────────────────────────────────────────────────────────────

function printSummary(results, dbType) {
  const passed  = results.filter(r => r.result === 'PASS').length;
  const failed  = results.filter(r => r.result === 'FAIL').length;
  const skipped = results.filter(r => r.result === 'SKIP').length;

  const nameW   = Math.max(20, ...results.map(r => (r.name || '').length));
  const resultW = 6;
  const detailW = Math.max(52, ...results.map(r => (r.detail || '').length));
  const totalW  = nameW + resultW + detailW + 7;

  const hline = '─'.repeat(totalW);
  const dline = '═'.repeat(totalW);

  console.log('');
  console.log('╔' + dline + '╗');
  console.log('║' + pad(` DB Verification Results (${dbType})`, totalW) + '║');
  console.log('╠' + hline + '╣');
  console.log('║ ' + pad('Request', nameW) + ' │ ' + pad('Result', resultW) + ' │ ' + pad('Detail', detailW) + ' ║');
  console.log('╠' + hline + '╣');
  for (const r of results) {
    console.log(
      '║ ' + pad(r.name || '', nameW) +
      ' │ ' + pad(r.result, resultW) +
      ' │ ' + pad(r.detail || '', detailW) + ' ║'
    );
  }
  console.log('╚' + dline + '╝');
  console.log(`DB Checks: ${passed} passed, ${failed} failed, ${skipped} skipped (non-2xx or unmapped)`);
  console.log('');
  if (failed > 0) console.warn(`[dbverify] WARNING: ${failed} DB verification(s) FAILED`);
}

// ─── Reporter entry point ─────────────────────────────────────────────────────

module.exports = function (newman, options) {
  const silent = !!(options && options['silent']);

  const configPath = (options && options['config'])
    || process.env.BIFROST_CONFIG_PATH
    || path.resolve(process.cwd(), 'config.json');

  // Main DB (config_store)
  let dbUrl = (options && options['db-url']) || process.env.BIFROST_DB_URL || null;
  if (!dbUrl) {
    dbUrl = dbUrlFromBifrostConfig(configPath);
    if (dbUrl && !silent) console.log(`[dbverify] Auto-detected main DB from config: ${configPath}`);
  }
  if (!dbUrl) {
    console.warn('[dbverify] No main DB URL found. Provide --reporter-dbverify-db-url, BIFROST_DB_URL, or --reporter-dbverify-config. Skipping DB checks.');
  }

  // Logs DB (logs_store)
  let logsDbUrl = (options && options['logs-db-url']) || process.env.BIFROST_LOGS_DB_URL || null;
  if (!logsDbUrl) {
    logsDbUrl = logsDbUrlFromBifrostConfig(configPath);
    if (logsDbUrl && !silent) console.log(`[dbverify] Auto-detected logs DB from config: ${configPath}`);
  }

  const dbType     = dbUrl      ? detectDbType(dbUrl)      : 'unknown';
  const results    = [];
  const pendingVerifications = [];
  const earlyMainDbQueue  = [];
  const earlyLogsDbQueue  = [];
  let   db         = null;
  let   logsDb     = null;
  let   dbReady    = false;
  let   logsDbReady = false;

  function drainQueue(queue, activeDb) {
    while (queue.length > 0) {
      const item = queue.shift();
      processRequestVerification({
        activeDb, method: item.method, mapping: item.mapping, urlId: item.urlId,
        responseBody: item.responseBody, name: item.name, request: item.request,
        pendingVerifications, results, silent,
      });
    }
  }

  newman.on('start', function (err) {
    if (err) return;

    if (dbUrl) {
      const safeUrl = dbUrl.replace(/:([^:@]+)@/, ':***@');
      createDbClient(dbUrl)
        .then((client) => {
          db      = client;
          dbReady = !!client;
          if (dbReady && !silent) console.log(`[dbverify] Connected to ${dbType} DB: ${safeUrl}`);
          if (dbReady && db) drainQueue(earlyMainDbQueue, db);
        })
        .catch((e) => {
          dbReady = false;
          console.warn(`[dbverify] Main DB not reachable, skipping DB checks: ${e.message}`);
          earlyMainDbQueue.forEach((item) => results.push({ name: item.name, result: 'SKIP', detail: 'Main DB not connected' }));
          earlyMainDbQueue.length = 0;
        });
    }

    if (logsDbUrl) {
      const safeLogsUrl = logsDbUrl.replace(/:([^:@]+)@/, ':***@');
      createDbClient(logsDbUrl)
        .then((client) => {
          logsDb      = client;
          logsDbReady = !!client;
          if (logsDbReady && !silent) console.log(`[dbverify] Connected to logs DB (${detectDbType(logsDbUrl)}): ${safeLogsUrl}`);
          if (logsDbReady && logsDb) drainQueue(earlyLogsDbQueue, logsDb);
        })
        .catch((e) => {
          logsDbReady = false;
          console.warn(`[dbverify] Logs DB not reachable, skipping logs DB checks: ${e.message}`);
          earlyLogsDbQueue.forEach((item) => results.push({ name: item.name, result: 'SKIP', detail: 'Logs DB not connected' }));
          earlyLogsDbQueue.length = 0;
        });
    }
  });

  newman.on('request', function (err, args) {
    if (err) return;

    const response   = args.response;
    const request    = args.request;
    const name       = (args.item && args.item.name) || 'Unknown Request';
    const statusCode = response && response.code;

    if (!statusCode || statusCode < 200 || statusCode > 299) {
      results.push({ name, result: 'SKIP', detail: `HTTP ${statusCode || '?'} (non-2xx)` });
      return;
    }

    const method  = request.method.toUpperCase();
    const urlPath = request.url.toString()
      .replace(/\?.*$/, '')
      .replace(/^https?:\/\/[^/]+/, '');

    const match = matchMapping(urlPath);
    if (!match) {
      results.push({ name, result: 'SKIP', detail: 'URL not mapped to DB table' });
      return;
    }

    const { mapping, urlId } = match;

    if (mapping.skipReason) {
      results.push({ name, result: 'SKIP', detail: mapping.skipReason });
      return;
    }

    // Pick the right DB client
    const isLogsTable = !!mapping.logsDb;
    const activeDb    = isLogsTable ? logsDb    : db;
    const activeReady = isLogsTable ? logsDbReady : dbReady;

    const responseBody = parseBody(response);

    if (!activeReady || !activeDb) {
      const queue = isLogsTable ? earlyLogsDbQueue : earlyMainDbQueue;
      queue.push({
        method, mapping, urlId, responseBody, name, request,
      });
      return;
    }

    processRequestVerification({
      activeDb, method, mapping, urlId, responseBody, name, request,
      pendingVerifications, results, silent,
    });
  });

  newman.on('done', function () {
    Promise.allSettled(pendingVerifications).then(() => {
      if (db)     db.close();
      if (logsDb) logsDb.close();
      if (results.length > 0) printSummary(results, dbType);
    });
  });
};
