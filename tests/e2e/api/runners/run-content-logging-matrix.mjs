#!/usr/bin/env node
// Content-logging permutation check against a live gateway.
//
// Drives every combination in lib/content-logging-matrix.mjs (virtual key inherit/on/off, client
// flag on/off, OTel connector flag on/off, per-request overrides allowed/blocked, request header
// absent/on/off) and checks, per request, whether its content reached the log store and whether it
// reached the OTel collector. Everything upstream is local: a mock OpenAI-compatible provider
// that echoes a per-request marker, and a mock OTLP/HTTP collector. No provider credentials, no
// paid calls.
//
// The log store is checked twice per request: through GET /api/logs/{id}, and straight from the logs
// database, because the API never serves content for a content_hidden row and so cannot prove the
// row itself holds none. The database is BIFROST_LOGS_DB_URL (sqlite:// or postgresql://) or, when
// unset, the logs_store of the gateway's config.json (BIFROST_E2E_CONFIG_PATH, default
// tests/integrations/python/config.json; sqlite paths resolve from BIFROST_E2E_SERVER_CWD, default
// the repo root). A logs database the runner cannot reach fails the run.
//
// The gateway dials the echo provider and the collector, so both default to loopback, which needs the
// gateway on the runner's host. For a gateway in another container or host, set
// BIFROST_E2E_CALLBACK_HOST to an address it can reach the runner on (e.g. host.docker.internal);
// the mocks then listen on every interface.
//
// The gateway's client config and OTel plugin are restored afterwards, and the provider and virtual
// keys this run creates are deleted.
//
//   node tests/e2e/api/runners/run-content-logging-matrix.mjs   (also runs inside make run-e2e-api / run-newman-api-tests.sh)
//   BIFROST_E2E_BASE_URL=http://localhost:8080 BIFROST_E2E_AUTH_HEADER="Bearer ..." node ...

import http from "node:http";
import path from "node:path";
import { randomBytes } from "node:crypto";
import { createRequire } from "node:module";
import { fileURLToPath } from "node:url";
import { allCases, callbackEndpoints, groupByConfig, headerValue, logRowFailures, normaliseRetentionDays, vkDisableValue, VK_MODES } from "./lib/content-logging-matrix.mjs";

const require = createRequire(import.meta.url);
const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../../../..");
const { readLogsDbUrl } = require("../lib/logs-db-url.js");

const baseURL = (process.env.BIFROST_E2E_BASE_URL || process.env.BIFROST_BASE_URL || "http://localhost:8080").replace(/\/+$/, "");
const adminAuthHeader = process.env.BIFROST_E2E_AUTH_HEADER || "";
const runTag = `clmx${process.pid}${Date.now().toString(36)}`;
const providerName = `clmx-provider-${runTag}`;
const modelName = "echo-model";
const requestedModel = `${providerName}/${modelName}`;

const otelTraceBodies = [];

// Markers are random hex with no shared prefix with anything else in a request or span, so a marker
// found in an export can only have come from message content.
function marker() {
	return `m${randomBytes(8).toString("hex")}`;
}

function listen(server, host) {
	return new Promise((resolve, reject) => {
		server.once("error", reject);
		server.listen(0, host, () => {
			server.off("error", reject);
			resolve(server.address().port);
		});
	});
}

function close(server) {
	return new Promise((resolve) => server.close(() => resolve()));
}

function readBody(req) {
	return new Promise((resolve, reject) => {
		const chunks = [];
		req.on("data", (chunk) => chunks.push(chunk));
		req.on("end", () => resolve(Buffer.concat(chunks)));
		req.on("error", reject);
	});
}

function createOtelReceiver() {
	return http.createServer(async (req, res) => {
		const body = await readBody(req);
		if (req.method === "POST" && req.url === "/v1/traces") {
			otelTraceBodies.push(body);
		}
		res.writeHead(req.url === "/v1/traces" || req.url === "/v1/metrics" ? 200 : 404, { "content-type": "application/x-protobuf" });
		res.end("");
	});
}

// The mock answers with "reply-<marker>" where <marker> is whatever follows "prompt-" in the last user
// message, so the output side of every request carries its own marker too.
function createEchoProvider() {
	return http.createServer(async (req, res) => {
		const body = await readBody(req);
		if (req.method !== "POST" || req.url !== "/v1/chat/completions") {
			res.writeHead(404);
			res.end("not found");
			return;
		}
		let prompt = "";
		try {
			const parsed = JSON.parse(body.toString("utf8"));
			const last = [...(parsed.messages || [])].reverse().find((m) => m.role === "user");
			prompt = typeof last?.content === "string" ? last.content : "";
		} catch {
			prompt = "";
		}
		const echoed = prompt.startsWith("prompt-") ? prompt.slice("prompt-".length) : "none";
		const now = Math.floor(Date.now() / 1000);
		res.writeHead(200, { "content-type": "application/json" });
		res.end(
			JSON.stringify({
				id: `chatcmpl-${now}`,
				object: "chat.completion",
				created: now,
				model: modelName,
				choices: [{ index: 0, message: { role: "assistant", content: `reply-${echoed}` }, finish_reason: "stop" }],
				usage: { prompt_tokens: 5, completion_tokens: 3, total_tokens: 8 },
			}),
		);
	});
}

async function request(method, path, body, headers = {}) {
	const requestHeaders = adminAuthHeader ? { Authorization: adminAuthHeader, ...headers } : { ...headers };
	if (body !== undefined) requestHeaders["content-type"] = "application/json";
	const res = await fetch(`${baseURL}${path}`, {
		method,
		headers: requestHeaders,
		body: body === undefined ? undefined : JSON.stringify(body),
	});
	const text = await res.text();
	let json = null;
	try {
		json = text ? JSON.parse(text) : null;
	} catch {
		json = null;
	}
	return { ok: res.ok, status: res.status, text, json, headers: res.headers };
}

async function mustRequest(method, path, body, headers = {}) {
	const res = await request(method, path, body, headers);
	if (!res.ok) throw new Error(`${method} ${path} failed with ${res.status}: ${res.text}`);
	return res;
}

async function poll(name, timeoutMs, fn) {
	const started = Date.now();
	let lastError;
	while (Date.now() - started < timeoutMs) {
		try {
			const result = await fn();
			if (result) return result;
		} catch (err) {
			lastError = err;
		}
		await new Promise((resolve) => setTimeout(resolve, 500));
	}
	throw new Error(`${name} timed out${lastError ? `: ${lastError.message}` : ""}`);
}

function exportsContain(value) {
	const needle = Buffer.from(value);
	return otelTraceBodies.some((b) => b.includes(needle));
}

async function getPlugin(name) {
	const res = await request("GET", `/api/plugins/${encodeURIComponent(name)}`);
	if (res.status === 404) return null;
	if (!res.ok) throw new Error(`GET /api/plugins/${name} failed with ${res.status}: ${res.text}`);
	return res.json?.plugin ?? res.json ?? null;
}

function pluginUpdatePayload(plugin) {
	return {
		enabled: Boolean(plugin.enabled),
		path: plugin.path ?? null,
		config: plugin.config ?? {},
		placement: plugin.placement ?? undefined,
		order: plugin.order ?? undefined,
	};
}

// The server reports log_retention_days:0 by default but rejects it on write, so a round-trip of
// client_config must normalise it (same as set-raw-override-config.mjs). auth_config is omitted so
// admin credentials are never resent.
async function putClientConfig(snapshot, overrides) {
	const clientConfig = { ...snapshot.client_config, ...overrides };
	clientConfig.log_retention_days = normaliseRetentionDays(clientConfig.log_retention_days);
	await mustRequest("PUT", "/api/config", { client_config: clientConfig, framework_config: snapshot.framework_config });
}

// openLogsDb connects to the gateway's logs database and returns a row reader. It throws when the
// database cannot be located or reached: without it the matrix cannot tell a content-free row from
// a hidden row that still holds content.
async function openLogsDb() {
	const configPath = process.env.BIFROST_E2E_CONFIG_PATH || path.join(repoRoot, "tests/integrations/python/config.json");
	const serverCwd = process.env.BIFROST_E2E_SERVER_CWD || repoRoot;
	const url = readLogsDbUrl(configPath, serverCwd);
	if (!url) {
		throw new Error(`cannot locate the logs database: set BIFROST_LOGS_DB_URL or point BIFROST_E2E_CONFIG_PATH at the gateway's config.json (tried ${configPath})`);
	}
	if (url.startsWith("sqlite://")) {
		const Database = require("better-sqlite3");
		const db = new Database(url.slice("sqlite://".length), { readonly: true, fileMustExist: true });
		db.pragma("busy_timeout = 5000");
		const stmt = db.prepare("SELECT * FROM logs WHERE id = ?");
		return { label: url, read: async (id) => stmt.get(id) ?? null, close: async () => db.close() };
	}
	if (url.startsWith("postgres://") || url.startsWith("postgresql://")) {
		const { Client } = require("pg");
		const client = new Client({ connectionString: url });
		await client.connect();
		return {
			label: url.replace(/:([^:@/]+)@/, ":***@"),
			read: async (id) => (await client.query("SELECT * FROM logs WHERE id = $1", [id])).rows[0] ?? null,
			close: async () => client.end(),
		};
	}
	throw new Error(`unsupported logs database url: ${url}`);
}

async function setOtel(collectorURL, connectorDisablesContent) {
	await mustRequest("PUT", "/api/plugins/otel", {
		enabled: true,
		path: null,
		config: {
			profiles: [
				{
					enabled: true,
					service_name: "bifrost-content-logging-matrix",
					collector_url: collectorURL,
					trace_type: "genai_extension",
					protocol: "http",
					insecure: true,
					disable_content_logging: connectorDisablesContent,
				},
			],
		},
	});
}

async function addEchoProvider(providerBaseURL) {
	await mustRequest("POST", "/api/providers", {
		provider: providerName,
		custom_provider_config: { base_provider_type: "openai", is_key_less: true, allowed_requests: { chat_completion: true } },
		network_config: {
			base_url: providerBaseURL,
			allow_private_network: true,
			default_request_timeout_in_seconds: 10,
			max_retries: 0,
			retry_backoff_initial: 500,
			retry_backoff_max: 5000,
		},
		concurrency_and_buffer_size: { concurrency: 20, buffer_size: 200 },
		keys: [],
	});
}

async function createVirtualKey(vkMode) {
	// Pinned to the echo provider alone rather than allow_all_providers. A provider added through the
	// management API only becomes a parseable model prefix once its first request reaches it, so until
	// then "<provider>/echo-model" is load-balanced as a bare model across every provider the key may
	// use, and any provider whose key claims all models could take it. One candidate removes that.
	const body = {
		name: `${runTag}-vk-${vkMode}`,
		is_active: true,
		provider_configs: [{ provider: providerName, weight: 1, allowed_models: ["*"], key_ids: ["*"] }],
	};
	const decision = vkDisableValue(vkMode);
	if (decision !== undefined) body.disable_content_logging = decision;
	const res = await mustRequest("POST", "/api/governance/virtual-keys", body);
	const vk = res.json?.virtual_key ?? res.json;
	if (!vk?.id || !vk?.value) throw new Error(`virtual key create returned no id/value: ${res.text}`);
	const stored = vk.disable_content_logging;
	if ((decision === undefined && stored != null) || (decision !== undefined && stored !== decision)) {
		throw new Error(`virtual key ${vkMode} stored disable_content_logging=${JSON.stringify(stored)}, want ${JSON.stringify(decision)}`);
	}
	return { id: vk.id, value: vk.value };
}

async function chat(requestID, vkValue, promptMarker, header) {
	const headers = { "x-request-id": requestID, "x-bf-vk": vkValue };
	if (header !== undefined) headers["x-bf-disable-content-logging"] = header;
	const res = await request("POST", "/v1/chat/completions", { model: requestedModel, messages: [{ role: "user", content: `prompt-${promptMarker}` }] }, headers);
	if (res.status !== 200) throw new Error(`chat ${requestID} returned ${res.status}: ${res.text}`);
	const content = res.json?.choices?.[0]?.message?.content;
	if (content !== `reply-${promptMarker}`) throw new Error(`chat ${requestID} got ${JSON.stringify(content)} from the echo provider: ${res.text.slice(0, 400)}`);
}

async function readLog(requestID) {
	return poll(`log ${requestID}`, 30000, async () => {
		const res = await request("GET", `/api/logs/${encodeURIComponent(requestID)}`);
		if (res.status === 404) return null;
		if (!res.ok) throw new Error(`GET /api/logs/${requestID} failed with ${res.status}: ${res.text}`);
		return res.json;
	});
}

// waitForConfig proves a reconfiguration is live before the group's cases run: a probe through the
// inheriting key must export (or not) exactly as the new connector flag says. The plugin reload and
// the exporter both run asynchronously, so this retries a fresh probe until the new state answers.
async function waitForConfig(group, inheritKey) {
	const wantExported = group.connector === "on";
	await poll(`config ${group.global}/${group.connector}/${group.override} to take effect`, 45000, async () => {
		const probeID = `${runTag}-probe-${randomBytes(4).toString("hex")}`;
		const probeMarker = marker();
		await chat(probeID, inheritKey.value, probeMarker, undefined);
		await poll(`probe ${probeID} export`, 10000, () => exportsContain(probeID));
		return exportsContain(probeMarker) === wantExported;
	});
}

function describe(c) {
	return `vk=${c.vk.padEnd(7)} global=${c.global.padEnd(3)} connector=${c.connector.padEnd(3)} override=${c.override.padEnd(7)} header=${c.header}`;
}

async function readRawLogRow(logsDb, requestID) {
	return poll(`raw log row ${requestID}`, 30000, () => logsDb.read(requestID));
}

async function runGroup(group, keys, logsDb) {
	const results = [];
	const sent = [];
	for (const c of group.cases) {
		const requestID = `${runTag}-${c.id}`;
		const promptMarker = marker();
		await chat(requestID, keys[c.vk].value, promptMarker, headerValue(c.header));
		sent.push({ c, requestID, promptMarker });
	}
	for (const { c, requestID, promptMarker } of sent) {
		const failures = [];
		const log = await readLog(requestID);
		const logInput = JSON.stringify(log.input_history ?? null);
		const logOutput = JSON.stringify(log.output_message ?? null);
		const logHasContent = logInput.includes(promptMarker) || logOutput.includes(promptMarker);
		if (logHasContent !== c.expected.logStoresContent) {
			failures.push(`log store ${logHasContent ? "kept" : "dropped"} content, want ${c.expected.logStoresContent ? "kept" : "dropped"}`);
		}
		if (log.content_hidden !== !c.expected.logStoresContent) {
			failures.push(`content_hidden=${JSON.stringify(log.content_hidden)}, want ${!c.expected.logStoresContent}`);
		}
		const rawRow = await readRawLogRow(logsDb, requestID);
		failures.push(...logRowFailures(rawRow, promptMarker, c.expected.logStoresContent));
		await poll(`OTel export of ${requestID}`, 30000, () => exportsContain(requestID));
		const exported = exportsContain(promptMarker) || exportsContain(`reply-${promptMarker}`);
		if (exported !== c.expected.connectorExportsContent) {
			failures.push(`OTel ${exported ? "exported" : "stripped"} content, want ${c.expected.connectorExportsContent ? "exported" : "stripped"}`);
		}
		results.push({ c, failures });
	}
	return results;
}

async function main() {
	console.log("Running content-logging permutation matrix...");
	console.log(`  Bifrost: ${baseURL}`);

	const configSnapshot = (await mustRequest("GET", "/api/config")).json;
	if (!configSnapshot?.client_config) throw new Error("GET /api/config returned no client_config");
	const originalOtel = await getPlugin("otel");
	const logsDb = await openLogsDb();
	console.log(`  Logs DB: ${logsDb.label}`);

	const otelReceiver = createOtelReceiver();
	const echoProvider = createEchoProvider();
	const callbackHost = process.env.BIFROST_E2E_CALLBACK_HOST || "";
	const { listenHost } = callbackEndpoints({ callbackHost, providerPort: 0, collectorPort: 0 });
	const otelPort = await listen(otelReceiver, listenHost);
	const mockPort = await listen(echoProvider, listenHost);
	const endpoints = callbackEndpoints({ callbackHost, providerPort: mockPort, collectorPort: otelPort });
	console.log(`  Echo provider: ${endpoints.providerBaseURL} (listening on ${listenHost})`);
	console.log(`  OTel collector: ${endpoints.collectorURL}`);
	const createdKeys = [];
	let providerAdded = false;
	const results = [];

	try {
		await addEchoProvider(endpoints.providerBaseURL);
		providerAdded = true;
		const keys = {};
		for (const vkMode of VK_MODES) {
			keys[vkMode] = await createVirtualKey(vkMode);
			createdKeys.push(keys[vkMode].id);
		}

		for (const group of groupByConfig(allCases())) {
			await putClientConfig(configSnapshot, {
				disable_content_logging: group.global === "off",
				allow_per_request_content_storage_override: group.override === "allowed",
				// Object-storage retention would keep disabled content as hidden rows; pin it off so a
				// disabled request always leaves a content-free row.
				retain_content_in_object_storage: false,
			});
			await setOtel(endpoints.collectorURL, group.connector === "off");
			await waitForConfig(group, keys.inherit);
			const groupResults = await runGroup(group, keys, logsDb);
			for (const r of groupResults) {
				console.log(`  ${r.failures.length === 0 ? "ok  " : "FAIL"} ${describe(r.c)}${r.failures.length ? ` -> ${r.failures.join("; ")}` : ""}`);
			}
			results.push(...groupResults);
		}
	} finally {
		const cleanupErrors = [];
		for (const id of createdKeys) {
			const res = await request("DELETE", `/api/governance/virtual-keys/${encodeURIComponent(id)}`).catch((e) => ({ ok: false, text: e.message }));
			if (!res.ok) cleanupErrors.push(`delete virtual key ${id}: ${res.text}`);
		}
		if (providerAdded) {
			const res = await request("DELETE", `/api/providers/${encodeURIComponent(providerName)}`).catch((e) => ({ ok: false, text: e.message }));
			if (!res.ok) cleanupErrors.push(`delete provider: ${res.text}`);
		}
		const otelRes = originalOtel
			? await request("PUT", "/api/plugins/otel", pluginUpdatePayload(originalOtel)).catch((e) => ({ ok: false, text: e.message }))
			: await request("DELETE", "/api/plugins/otel").catch((e) => ({ ok: false, text: e.message }));
		if (!otelRes.ok) cleanupErrors.push(`restore otel plugin: ${otelRes.text}`);
		try {
			await putClientConfig(configSnapshot, {});
		} catch (err) {
			cleanupErrors.push(`restore client config: ${err.message}`);
		}
		await Promise.all([close(otelReceiver), close(echoProvider), logsDb.close().catch(() => {})]);
		for (const e of cleanupErrors) console.warn(`WARNING: cleanup: ${e}`);
		const originalRetention = configSnapshot.client_config.log_retention_days;
		if (!(originalRetention >= 1)) {
			// PUT /api/config cannot store "unset"; the closest restore is the cleaner's own default.
			console.warn(
				`WARNING: log_retention_days was unset (${JSON.stringify(originalRetention)}); restored as ${normaliseRetentionDays(originalRetention)}, the log cleaner's default, so effective retention is unchanged`,
			);
		}
	}

	const failed = results.filter((r) => r.failures.length > 0);
	console.log(`Content-logging matrix: ${results.length - failed.length}/${results.length} cases passed.`);
	if (failed.length > 0 || results.length !== allCases().length) {
		process.exit(1);
	}
}

main().catch((err) => {
	console.error(`Content-logging matrix failed: ${err.message}`);
	process.exit(1);
});
