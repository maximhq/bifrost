const fs = require('node:fs');
const path = require('node:path');

// SQLite opens config.path relative to the server process cwd, not config.json.
function logsDbUrlFromConfig(config, serverWorkingDir, env = process.env) {
  const ls = config.logs_store;
  if (!ls || !ls.enabled) return '';
  const resolve = value => {
    const text = String((value && typeof value === 'object' ? value.value : value) ?? '');
    return text.startsWith('env.') ? env[text.slice(4)] || '' : text;
  };
  const c = ls.config || {};
  if (ls.type === 'sqlite') {
    const filePath = resolve(c.path);
    return filePath ? 'sqlite://' + path.resolve(serverWorkingDir, filePath) : '';
  }
  if (ls.type === 'postgres') {
    const user = encodeURIComponent(resolve(c.user) || 'bifrost');
    const password = encodeURIComponent(resolve(c.password));
    const host = resolve(c.host) || 'localhost';
    const port = resolve(c.port) || '5432';
    const database = encodeURIComponent(resolve(c.db_name) || 'bifrost');
    const ssl = encodeURIComponent(resolve(c.ssl_mode) || 'disable');
    return `postgresql://${user}:${password}@${host}:${port}/${database}?sslmode=${ssl}`;
  }
  return '';
}

function readLogsDbUrl(configPath, serverWorkingDir, env = process.env) {
  if (env.BIFROST_LOGS_DB_URL) return env.BIFROST_LOGS_DB_URL;
  try {
    return logsDbUrlFromConfig(JSON.parse(fs.readFileSync(configPath, 'utf8')), serverWorkingDir, env);
  } catch {
    return '';
  }
}

module.exports = { logsDbUrlFromConfig, readLogsDbUrl };

if (require.main === module) {
  process.stdout.write(readLogsDbUrl(process.argv[2], process.argv[3] || process.cwd()));
}
