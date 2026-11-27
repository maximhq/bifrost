// Read-only SQLite access for the e2e runners and reporters.
//
// Node's built-in node:sqlite needs no native build, so it works where dependencies are
// installed with --ignore-scripts (CI does, and better-sqlite3 has no prebuilt binary for
// the Node in .nvmrc anyway). Older Node without it falls back to better-sqlite3, which a
// plain `npm install` builds. Rows come back as plain objects with Buffer blobs either way,
// matching what better-sqlite3 returns.
const { Buffer } = require('node:buffer');

function builtinDatabase() {
  try {
    return require('node:sqlite').DatabaseSync;
  } catch (_) {
    return null;
  }
}

// node:sqlite rows have a null prototype and Uint8Array blobs.
function plainRow(row) {
  const out = {};
  for (const [key, value] of Object.entries(row)) {
    out[key] = value instanceof Uint8Array && !Buffer.isBuffer(value) ? Buffer.from(value.buffer, value.byteOffset, value.byteLength) : value;
  }
  return out;
}

// openSqliteReadonly opens an existing database file read-only and returns
// { prepare(sql) -> { all(...params) }, close() }. It throws when the file does not exist.
function openSqliteReadonly(file) {
  const DatabaseSync = builtinDatabase();
  if (DatabaseSync) {
    const db = new DatabaseSync(file, { readOnly: true });
    db.exec('PRAGMA busy_timeout = 5000');
    return {
      prepare: sql => {
        const stmt = db.prepare(sql);
        return { all: (...params) => stmt.all(...params).map(plainRow) };
      },
      close: () => db.close(),
    };
  }
  const Database = require('better-sqlite3');
  const db = new Database(file, { readonly: true, fileMustExist: true });
  db.pragma('busy_timeout = 5000');
  return {
    prepare: sql => db.prepare(sql),
    close: () => db.close(),
  };
}

module.exports = { openSqliteReadonly };
