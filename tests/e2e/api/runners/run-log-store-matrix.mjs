#!/usr/bin/env node
// Log-store matrix: boots one gateway per log-store configuration and runs the content-logging
// matrix (LLM and relay rows) and the relay matrix against each, because the log store, object
// storage and its exclusions are read at boot only.
//
// Cells:
//   sqlite                         RDB store, no object storage
//   postgres                       RDB store, no object storage            (needs a Postgres server)
//   sqlite+s3                      hybrid store over a fake S3
//   postgres+s3                    hybrid store over a fake S3             (needs a Postgres server)
//   sqlite+s3-exclude-types        hybrid, relay and chat_completion excluded from offload (object_storage_exclude_request_types)
//   sqlite+s3-exclude-fields       hybrid, request_body kept resident (object_storage_exclude_fields)
//
// Object storage is lib/fake-s3.mjs: an S3-compatible bucket on loopback, so the hybrid store is
// exercised end to end (uploads, previews, hydration, deletes) with no docker and no cloud bucket.
//
// Usage:
//   node tests/e2e/api/runners/run-log-store-matrix.mjs --binary tmp/bifrost-http
//     [--cells sqlite,sqlite+s3] [--suites pricing,settings] [--skip-content-matrix] [--parallel 3]
//
// Cells are independent (each has its own gateway, port, database and bucket), so --parallel runs
// several at once. Each cell's runner output then goes to tmp/log-store-matrix/<cell>/runner.log
// and is printed when the cell ends.
//
// Environment:
//   BIFROST_E2E_POSTGRES_URL   admin connection to a Postgres server (postgresql://user:pass@host:port/postgres);
//                              each postgres cell gets a fresh database on it. Unset: postgres cells are skipped.
//   RELAY_MATRIX_LARGE_MB      size of the network suite's large reply (default 65)
//
// Exit code is non-zero when any cell fails. Each failed cell's server log is copied to
// tmp/log-store-matrix/<cell>/server.log.
import fs from "node:fs";
import path from "node:path";
import { spawn } from "node:child_process";
import { createRequire } from "node:module";
import { fileURLToPath } from "node:url";
import { bootGateway, DEFAULT_SETUP_TOKEN } from "./lib/gateway-boot.mjs";
import { s3ObjectStorageConfig, startFakeS3 } from "./lib/fake-s3.mjs";

const require = createRequire(import.meta.url);
const here = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(here, "../../../..");
const OBJECT_PREFIX = "bifrost";

export const CELLS = [
	{ id: "sqlite", store: "sqlite" },
	{ id: "postgres", store: "postgres" },
	{ id: "sqlite+s3", store: "sqlite", s3: true },
	{ id: "postgres+s3", store: "postgres", s3: true },
	{ id: "sqlite+s3-exclude-types", store: "sqlite", s3: true, excludeTypes: ["relay", "chat_completion"] },
	{ id: "sqlite+s3-exclude-fields", store: "sqlite", s3: true, excludeFields: ["request_body"] },
];

function arg(name) {
	const i = process.argv.indexOf(`--${name}`);
	if (i >= 0) return process.argv[i + 1];
	const eq = process.argv.find((a) => a.startsWith(`--${name}=`));
	return eq ? eq.slice(name.length + 3) : undefined;
}

async function freshPostgresDB(adminURL, name) {
	const { Client } = require("pg");
	const admin = new Client({ connectionString: adminURL });
	await admin.connect();
	try {
		await admin.query(`DROP DATABASE IF EXISTS "${name}" WITH (FORCE)`);
		await admin.query(`CREATE DATABASE "${name}"`);
	} finally {
		await admin.end();
	}
	const u = new URL(adminURL);
	u.pathname = `/${name}`;
	return { url: u.toString(), host: u.hostname, port: u.port || "5432", user: decodeURIComponent(u.username), password: decodeURIComponent(u.password), db: name };
}

async function dropPostgresDB(adminURL, name) {
	const { Client } = require("pg");
	const admin = new Client({ connectionString: adminURL });
	await admin.connect();
	try {
		await admin.query(`DROP DATABASE IF EXISTS "${name}" WITH (FORCE)`);
	} finally {
		await admin.end();
	}
}

function runNode(script, env, extraArgs = [], logFile = null) {
	return new Promise((resolve) => {
		const out = logFile ? fs.openSync(logFile, "a") : "inherit";
		const child = spawn(process.execPath, [path.join(here, script), ...extraArgs], { stdio: ["ignore", out, out], env: { ...process.env, ...env } });
		child.on("exit", (code) => {
			if (logFile) fs.closeSync(out);
			resolve(code ?? 1);
		});
	});
}

async function runCell(cell, opts) {
	const keepDir = path.join(repoRoot, "tmp/log-store-matrix", cell.id);
	fs.mkdirSync(keepDir, { recursive: true });
	const runnerLog = opts.parallel > 1 ? path.join(keepDir, "runner.log") : null;
	if (runnerLog) fs.writeFileSync(runnerLog, "");
	const say = (line) => (runnerLog ? fs.appendFileSync(runnerLog, `${line}\n`) : console.log(line));
	say(`\n================ cell ${cell.id} ================`);
	let fake = null;
	let pg = null;
	let gw = null;
	const pgName = `bifrost_logstore_matrix_${cell.id.replace(/[^a-z0-9]/g, "_")}_${process.pid}`;
	try {
		if (cell.s3) fake = await startFakeS3({ bucket: "bifrost-logstore-matrix" });
		if (cell.store === "postgres") pg = await freshPostgresDB(opts.postgresURL, pgName);
		const logsStore = (dir) => {
			const store =
				cell.store === "postgres"
					? { type: "postgres", config: { host: pg.host, port: pg.port, user: pg.user, password: pg.password, db_name: pg.db, ssl_mode: "disable" } }
					: { type: "sqlite", config: { path: path.join(dir, "logs.db") } };
			if (fake) {
				store.object_storage = s3ObjectStorageConfig(fake, OBJECT_PREFIX);
				if (cell.excludeTypes) store.object_storage_exclude_request_types = cell.excludeTypes;
				if (cell.excludeFields) store.object_storage_exclude_fields = cell.excludeFields;
			}
			return store;
		};
		gw = await bootGateway({ binary: opts.binary, logsStore });
		say(`gateway ${gw.baseURL} (app dir ${gw.dir})`);
		const env = {
			BIFROST_E2E_BASE_URL: gw.baseURL,
			BIFROST_E2E_SETUP_TOKEN: process.env.BIFROST_SETUP_TOKEN || DEFAULT_SETUP_TOKEN,
			BIFROST_LOGS_DB_URL: cell.store === "postgres" ? `${pg.url}${pg.url.includes("?") ? "&" : "?"}sslmode=disable` : `sqlite://${path.join(gw.dir, "logs.db")}`,
			BIFROST_E2E_FAKE_S3_URL: fake ? fake.url : "",
			BIFROST_E2E_OBJECT_STORAGE_PREFIX: OBJECT_PREFIX,
			BIFROST_E2E_OBJECT_STORAGE_EXCLUDE_TYPES: (cell.excludeTypes ?? []).join(","),
			BIFROST_E2E_OBJECT_STORAGE_EXCLUDE_FIELDS: (cell.excludeFields ?? []).join(","),
		};
		const codes = {};
		if (!opts.skipContentMatrix) codes.content = await runNode("run-content-logging-matrix.mjs", env, [], runnerLog);
		codes.relay = await runNode("run-relay-matrix.mjs", env, opts.suites ? [`--suites=${opts.suites}`] : [], runnerLog);
		const ok = Object.values(codes).every((c) => c === 0);
		return { cell: cell.id, ok, codes, logPath: gw.logPath };
	} catch (err) {
		say(`cell ${cell.id} failed to run: ${err.message}`);
		return { cell: cell.id, ok: false, codes: { setup: 1 }, error: err.message, logPath: gw?.logPath };
	} finally {
		if (gw) {
			try {
				fs.copyFileSync(gw.logPath, path.join(keepDir, "server.log"));
			} catch {
				// The log is best effort.
			}
			await gw.stop();
		}
		if (fake) await fake.close();
		if (pg) await dropPostgresDB(opts.postgresURL, pgName).catch((e) => console.warn(`WARNING: drop ${pgName}: ${e.message}`));
	}
}

async function main() {
	const binary = path.resolve(arg("binary") || process.env.BIFROST_BINARY || path.join(repoRoot, "tmp/bifrost-http"));
	const postgresURL = process.env.BIFROST_E2E_POSTGRES_URL || "";
	const wanted = (arg("cells") || "").split(",").map((s) => s.trim()).filter(Boolean);
	for (const c of wanted) if (!CELLS.some((x) => x.id === c)) throw new Error(`unknown cell ${c}; have ${CELLS.map((x) => x.id).join(", ")}`);
	const cells = CELLS.filter((c) => wanted.length === 0 || wanted.includes(c.id));
	const parallel = Math.max(1, Number(arg("parallel") || process.env.LOG_STORE_MATRIX_PARALLEL || "1"));
	const opts = { binary, postgresURL, parallel, suites: arg("suites"), skipContentMatrix: process.argv.includes("--skip-content-matrix") };
	const results = new Array(cells.length);
	let next = 0;
	const worker = async () => {
		while (next < cells.length) {
			const i = next++;
			const cell = cells[i];
			if (cell.store === "postgres" && !postgresURL) {
				console.log(`\nSkipping cell ${cell.id}: set BIFROST_E2E_POSTGRES_URL to run the Postgres cells.`);
				results[i] = { cell: cell.id, ok: true, skipped: true };
				continue;
			}
			if (parallel > 1) console.log(`started cell ${cell.id}`);
			results[i] = await runCell(cell, opts);
			if (parallel > 1) {
				const log = path.join(repoRoot, "tmp/log-store-matrix", cell.id, "runner.log");
				console.log(fs.readFileSync(log, "utf8"));
			}
		}
	};
	await Promise.all(Array.from({ length: Math.min(parallel, cells.length) }, worker));
	console.log("\nLog-store matrix:");
	for (const r of results) {
		const status = r.skipped ? "skipped" : r.ok ? "ok" : `FAIL ${JSON.stringify(r.codes)}${r.error ? ` (${r.error})` : ""}`;
		console.log(`  ${r.cell.padEnd(26)} ${status}`);
	}
	if (results.some((r) => !r.ok)) process.exit(1);
	if (results.every((r) => r.skipped)) {
		console.error("every cell was skipped");
		process.exit(1);
	}
}

main().catch((err) => {
	console.error(`Log-store matrix failed: ${err.stack || err.message}`);
	process.exit(1);
});
