// Unit tests for logs-db.mjs. Run directly: `node --test logs-db.test.mjs`. The Postgres refresh is
// driven through a stub client that records its queries; the SQLite path opens a real temp file.
import assert from "node:assert";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { DatabaseSync } from "node:sqlite";
import { test } from "node:test";
import { openLogsDb, refreshPgViews } from "./logs-db.mjs";

// stubClient answers the readiness query with ready[view] and records every statement it is sent.
function stubClient(ready) {
	const sent = [];
	return {
		sent,
		async query(sql, params = []) {
			sent.push(sql.trim().split(/\s+/).slice(0, 4).join(" "));
			if (/^SELECT EXISTS/.test(sql.trim())) return { rows: [{ ready: !!ready[params[0]] }] };
			return { rows: [] };
		},
	};
}

const refreshes = (client) => client.sent.filter((s) => s.startsWith("REFRESH"));

test("a view with a valid unique index is refreshed concurrently", async () => {
	const client = stubClient({ mv_filter_virtual_keys: true });
	await refreshPgViews(client, ["mv_filter_virtual_keys"]);
	assert.deepStrictEqual(refreshes(client), ["REFRESH MATERIALIZED VIEW CONCURRENTLY"]);
});

test("a view that does not exist, or has no valid unique index yet, is skipped without a refresh", async () => {
	const client = stubClient({});
	await refreshPgViews(client, ["mv_filter_virtual_keys"]);
	assert.strictEqual(client.sent.length, 1, "only the readiness check should run");
	assert.deepStrictEqual(refreshes(client), []);
});

test("only the ready views in a list are refreshed", async () => {
	const client = stubClient({ mv_ready: true });
	await refreshPgViews(client, ["mv_missing", "mv_ready", "mv_building"]);
	assert.strictEqual(refreshes(client).length, 1);
	assert.strictEqual(client.sent.length, 4, "three readiness checks and one refresh");
});

test("an invalid view name is rejected before any statement is sent", async () => {
	const client = stubClient({});
	await assert.rejects(refreshPgViews(client, ["mv_ok; DROP TABLE logs"]), /bad view name/);
	assert.deepStrictEqual(client.sent, []);
});

test("refreshViews is a no-op on a SQLite logs database", async () => {
	const dir = mkdtempSync(join(tmpdir(), "logs-db-test-"));
	const file = join(dir, "logs.db");
	const seed = new DatabaseSync(file);
	seed.exec("CREATE TABLE logs (id TEXT PRIMARY KEY)");
	seed.close();
	const saved = process.env.BIFROST_LOGS_DB_URL;
	process.env.BIFROST_LOGS_DB_URL = `sqlite://${file}`;
	try {
		const db = await openLogsDb();
		try {
			await db.refreshViews(["mv_filter_virtual_keys"]);
		} finally {
			await db.close();
		}
	} finally {
		if (saved === undefined) delete process.env.BIFROST_LOGS_DB_URL;
		else process.env.BIFROST_LOGS_DB_URL = saved;
		rmSync(dir, { recursive: true, force: true });
	}
});
