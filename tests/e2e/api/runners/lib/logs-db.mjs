// Raw reads from a gateway's logs database, shared by the runners that must prove what a row holds
// beyond what the API serves (a content_hidden row is never served with content, so only the
// database can show it holds none).
//
// The database is BIFROST_LOGS_DB_URL (sqlite:// or postgresql://) or, when unset, the logs_store
// of the gateway's config.json. A relative sqlite path resolves against the server's working
// directory, which differs by how the server was started (repo root in CI, transports/bifrost-http
// under make dev), so every candidate is opened and the first to return a row is pinned.
import fs from "node:fs";
import path from "node:path";
import { createRequire } from "node:module";
import { fileURLToPath } from "node:url";

const require = createRequire(import.meta.url);
const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../../../../..");
const { readLogsDbUrl } = require("../../lib/logs-db-url.js");

const IDENT = /^[a-z_][a-z0-9_]*$/;

function candidateUrls() {
	if (process.env.BIFROST_LOGS_DB_URL) return [process.env.BIFROST_LOGS_DB_URL];
	const configPath = process.env.BIFROST_E2E_CONFIG_PATH || path.join(repoRoot, "tests/integrations/python/config.json");
	const serverCwds = process.env.BIFROST_E2E_SERVER_CWD ? [process.env.BIFROST_E2E_SERVER_CWD] : [repoRoot, path.join(repoRoot, "transports/bifrost-http")];
	const urls = [...new Set(serverCwds.map((cwd) => readLogsDbUrl(configPath, cwd)).filter(Boolean))];
	if (urls.length === 0) {
		throw new Error(`cannot locate the logs database: set BIFROST_LOGS_DB_URL or point BIFROST_E2E_CONFIG_PATH at the gateway's config.json (tried ${configPath})`);
	}
	return urls;
}

// openLogsDb returns { label, read(table, column, value), all(table, column, value), close }.
// read returns the first matching row or null; all returns every matching row.
export async function openLogsDb() {
	const urls = candidateUrls();
	const url = urls[0];
	if (url.startsWith("sqlite://")) {
		const Database = require("better-sqlite3");
		const candidates = urls
			.map((u) => u.slice("sqlite://".length))
			.filter((file) => fs.existsSync(file))
			.map((file) => {
				const db = new Database(file, { readonly: true, fileMustExist: true });
				db.pragma("busy_timeout = 5000");
				return { file, db, stmts: new Map() };
			});
		if (candidates.length === 0) {
			throw new Error(`cannot locate the logs database: none of ${urls.join(", ")} exists; set BIFROST_E2E_SERVER_CWD to the gateway's working directory`);
		}
		let pinned = candidates.length === 1 ? candidates[0] : null;
		const stmt = (candidate, table, column) => {
			if (!IDENT.test(table) || !IDENT.test(column)) throw new Error(`bad identifier ${table}.${column}`);
			const key = `${table}.${column}`;
			if (!candidate.stmts.has(key)) candidate.stmts.set(key, candidate.db.prepare(`SELECT * FROM ${table} WHERE ${column} = ?`));
			return candidate.stmts.get(key);
		};
		const all = async (table, column, value) => {
			for (const candidate of pinned ? [pinned] : candidates) {
				const rows = stmt(candidate, table, column).all(value);
				if (rows.length > 0) {
					pinned = candidate;
					return rows;
				}
			}
			return [];
		};
		return {
			label: candidates.map((c) => `sqlite://${c.file}`).join(" | "),
			all,
			read: async (table, column, value) => (await all(table, column, value))[0] ?? null,
			close: async () => {
				for (const candidate of candidates) candidate.db.close();
			},
		};
	}
	if (url.startsWith("postgres://") || url.startsWith("postgresql://")) {
		const { Client } = require("pg");
		const client = new Client({ connectionString: url });
		await client.connect();
		const all = async (table, column, value) => {
			if (!IDENT.test(table) || !IDENT.test(column)) throw new Error(`bad identifier ${table}.${column}`);
			return (await client.query(`SELECT * FROM ${table} WHERE ${column} = $1`, [value])).rows;
		};
		return {
			label: url.replace(/:([^:@/]+)@/, ":***@"),
			all,
			read: async (table, column, value) => (await all(table, column, value))[0] ?? null,
			close: async () => client.end(),
		};
	}
	throw new Error(`unsupported logs database url: ${url}`);
}
