// Live plumbing for the content-logging matrix runners: the local mocks a gateway is pointed at, the
// management API client, the logs-database reader, and the per-request check against a live
// gateway. Shared by the OSS runner (run-content-logging-matrix.mjs) and the enterprise one, so both
// judge a request the same way; each runner only builds the entities its own axes need.
//
// Everything here talks to a gateway or listens on a port. The pure contract (the case table and the
// expected outcome) stays in content-logging-matrix.mjs.

import http from "node:http";
import path from "node:path";
import { randomBytes } from "node:crypto";
import { createRequire } from "node:module";
import { fileURLToPath } from "node:url";
import { logRowFailures, normaliseRetentionDays } from "./content-logging-matrix.mjs";

const require = createRequire(import.meta.url);
const ossRepoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../../../../..");
const { readLogsDbUrl } = require("../../lib/logs-db-url.js");

export const ECHO_MODEL = "echo-model";

// Markers are random hex with no shared prefix with anything else in a request or span, so a marker
// found in an export can only have come from message content.
export function marker() {
	return `m${randomBytes(8).toString("hex")}`;
}

export function listen(server, host) {
	return new Promise((resolve, reject) => {
		server.once("error", reject);
		server.listen(0, host, () => {
			server.off("error", reject);
			resolve(server.address().port);
		});
	});
}

export function close(server) {
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

// createOtelReceiver is a mock OTLP/HTTP collector that keeps every trace body it receives.
export function createOtelReceiver() {
	const bodies = [];
	const server = http.createServer(async (req, res) => {
		const body = await readBody(req);
		if (req.method === "POST" && req.url === "/v1/traces") {
			bodies.push(body);
		}
		res.writeHead(req.url === "/v1/traces" || req.url === "/v1/metrics" ? 200 : 404, { "content-type": "application/x-protobuf" });
		res.end("");
	});
	const contains = (value) => {
		const needle = Buffer.from(value);
		return bodies.some((b) => b.includes(needle));
	};
	return { server, contains };
}

// createEchoProvider is a mock OpenAI-compatible provider. It answers with "reply-<marker>" where
// <marker> is whatever follows "prompt-" in the last user message, so the output side of every
// request carries its own marker too. It records the key each attempt arrived with, per marker, and
// refuses with a 500 any attempt whose key is in failingKeyValues, to drive a retry onto another key.
export function createEchoProvider({ failingKeyValues = new Set() } = {}) {
	const keysSeenByMarker = new Map();
	const server = http.createServer(async (req, res) => {
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
		const keyValue = String(req.headers.authorization || "").replace(/^Bearer\s+/i, "");
		if (!keysSeenByMarker.has(echoed)) keysSeenByMarker.set(echoed, []);
		keysSeenByMarker.get(echoed).push(keyValue);
		if (failingKeyValues.has(keyValue)) {
			res.writeHead(500, { "content-type": "application/json" });
			res.end(JSON.stringify({ error: { message: "echo provider refuses this key", type: "server_error" } }));
			return;
		}
		const now = Math.floor(Date.now() / 1000);
		res.writeHead(200, { "content-type": "application/json" });
		res.end(
			JSON.stringify({
				id: `chatcmpl-${now}`,
				object: "chat.completion",
				created: now,
				model: ECHO_MODEL,
				choices: [{ index: 0, message: { role: "assistant", content: `reply-${echoed}` }, finish_reason: "stop" }],
				usage: { prompt_tokens: 5, completion_tokens: 3, total_tokens: 8 },
			}),
		);
	});
	return { server, keysSeenByMarker, failingKeyValues };
}

// createClient is a management and inference API client for one gateway.
export function createClient({ baseURL, adminAuthHeader = "" }) {
	async function request(method, urlPath, body, headers = {}) {
		const requestHeaders = adminAuthHeader ? { Authorization: adminAuthHeader, ...headers } : { ...headers };
		if (body !== undefined) requestHeaders["content-type"] = "application/json";
		const res = await fetch(`${baseURL}${urlPath}`, {
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
	async function mustRequest(method, urlPath, body, headers = {}) {
		const res = await request(method, urlPath, body, headers);
		if (!res.ok) throw new Error(`${method} ${urlPath} failed with ${res.status}: ${res.text}`);
		return res;
	}
	return { request, mustRequest };
}

export async function poll(name, timeoutMs, fn) {
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

// openLogsDb connects to the gateway's logs database and returns a row reader. It throws when the
// database cannot be located or reached: without it the matrix cannot tell a content-free row from
// a hidden row that still holds content. The database is BIFROST_LOGS_DB_URL or, when unset, the
// logs_store of the gateway's config.json (BIFROST_E2E_CONFIG_PATH, default the OSS profile).
export async function openLogsDb() {
	const configPath = process.env.BIFROST_E2E_CONFIG_PATH || path.join(ossRepoRoot, "tests/integrations/python/config.json");
	const serverCwd = process.env.BIFROST_E2E_SERVER_CWD || ossRepoRoot;
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

// gatewayConfig reconfigures and restores the gateway settings the matrix varies: the client flags
// and the OTel connector.
export function gatewayConfig(client) {
	async function getPlugin(name) {
		const res = await client.request("GET", `/api/plugins/${encodeURIComponent(name)}`);
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
	// client_config must normalise it. auth_config is omitted so admin credentials are never resent.
	async function putClientConfig(snapshot, overrides) {
		const clientConfig = { ...snapshot.client_config, ...overrides };
		clientConfig.log_retention_days = normaliseRetentionDays(clientConfig.log_retention_days);
		await client.mustRequest("PUT", "/api/config", { client_config: clientConfig, framework_config: snapshot.framework_config });
	}
	async function setOtel(collectorURL, connectorDisablesContent) {
		await client.mustRequest("PUT", "/api/plugins/otel", {
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
	// applyGroup puts the gateway into one matrix group's configuration. Object-storage retention
	// would keep disabled content as hidden rows, so it is pinned off: a disabled request always
	// leaves a content-free row.
	async function applyGroup(snapshot, group, collectorURL) {
		await putClientConfig(snapshot, {
			disable_content_logging: group.global === "off",
			allow_per_request_content_storage_override: group.override === "allowed",
			retain_content_in_object_storage: false,
		});
		await setOtel(collectorURL, group.connector === "off");
	}
	async function restore(snapshot, originalOtel) {
		const errors = [];
		const otelRes = originalOtel
			? await client.request("PUT", "/api/plugins/otel", pluginUpdatePayload(originalOtel)).catch((e) => ({ ok: false, text: e.message }))
			: await client.request("DELETE", "/api/plugins/otel").catch((e) => ({ ok: false, text: e.message }));
		if (!otelRes.ok) errors.push(`restore otel plugin: ${otelRes.text}`);
		try {
			await putClientConfig(snapshot, {});
		} catch (err) {
			errors.push(`restore client config: ${err.message}`);
		}
		const originalRetention = snapshot.client_config.log_retention_days;
		if (!(originalRetention >= 1)) {
			// PUT /api/config cannot store "unset"; the closest restore is the cleaner's own default.
			console.warn(
				`WARNING: log_retention_days was unset (${JSON.stringify(originalRetention)}); restored as ${normaliseRetentionDays(originalRetention)}, the log cleaner's default, so effective retention is unchanged`,
			);
		}
		return errors;
	}
	return { getPlugin, applyGroup, restore };
}

// createChecker judges one request against its expected outcome: through the logs API, straight from
// the logs database row (the API never serves content for a content_hidden row, so only the row can
// prove it holds none), and in the OTel export.
export function createChecker({ client, logsDb, otel }) {
	async function readLog(requestID) {
		return poll(`log ${requestID}`, 30000, async () => {
			const res = await client.request("GET", `/api/logs/${encodeURIComponent(requestID)}`);
			if (res.status === 404) return null;
			if (!res.ok) throw new Error(`GET /api/logs/${requestID} failed with ${res.status}: ${res.text}`);
			return res.json;
		});
	}
	return async function checkCase(requestID, promptMarker, expected) {
		const failures = [];
		const log = await readLog(requestID);
		const logInput = JSON.stringify(log.input_history ?? null);
		const logOutput = JSON.stringify(log.output_message ?? null);
		const logHasContent = logInput.includes(promptMarker) || logOutput.includes(promptMarker);
		if (logHasContent !== expected.logStoresContent) {
			failures.push(`log store ${logHasContent ? "kept" : "dropped"} content, want ${expected.logStoresContent ? "kept" : "dropped"}`);
		}
		if (log.content_hidden !== !expected.logStoresContent) {
			failures.push(`content_hidden=${JSON.stringify(log.content_hidden)}, want ${!expected.logStoresContent}`);
		}
		const rawRow = await poll(`raw log row ${requestID}`, 30000, () => logsDb.read(requestID));
		failures.push(...logRowFailures(rawRow, promptMarker, expected.logStoresContent));
		await poll(`OTel export of ${requestID}`, 30000, () => otel.contains(requestID));
		const exported = otel.contains(promptMarker) || otel.contains(`reply-${promptMarker}`);
		if (exported !== expected.connectorExportsContent) {
			failures.push(`OTel ${exported ? "exported" : "stripped"} content, want ${expected.connectorExportsContent ? "exported" : "stripped"}`);
		}
		return failures;
	};
}

// chatRequest sends one chat completion carrying a marker and checks the echo came back.
export function chatSender(client) {
	return async function chat(requestID, { vkValue, promptMarker, header, providerKeyID, model }) {
		const headers = { "x-request-id": requestID, "x-bf-vk": vkValue };
		if (header !== undefined) headers["x-bf-disable-content-logging"] = header;
		// Pins the provider key the request is served by, so its decision is the one under test.
		if (providerKeyID) headers["x-bf-api-key-id"] = providerKeyID;
		const res = await client.request("POST", "/v1/chat/completions", { model, messages: [{ role: "user", content: `prompt-${promptMarker}` }] }, headers);
		if (res.status !== 200) throw new Error(`chat ${requestID} returned ${res.status}: ${res.text}`);
		const content = res.json?.choices?.[0]?.message?.content;
		if (content !== `reply-${promptMarker}`) throw new Error(`chat ${requestID} got ${JSON.stringify(content)} from the echo provider: ${res.text.slice(0, 400)}`);
	};
}
