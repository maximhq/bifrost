#!/usr/bin/env node
// Content-logging permutation check against a live gateway, for LLM rows and relay rows alike.
//
// Drives every combination in lib/content-logging-matrix.mjs (virtual key inherit/on/off, client
// flag on/off, OTel connector flag on/off, per-request overrides allowed/blocked, request header
// absent/on/off). Each case sends an LLM request and a relay request on the same key and header and
// checks, per request, whether its content reached the log store, and for the LLM request whether it
// reached the OTel collector. Relay rows follow the same log-store decision as LLM rows, so a relay
// row that keeps or drops content differently from its LLM twin fails the case.
//
// Everything upstream is local: one echo server answers the LLM provider's chat completions and the
// relay's calls with a per-request marker, and a mock OTLP/HTTP collector takes the exports. No
// provider credentials, no paid calls.
//
// The log store is checked twice per request: through the API (GET /api/logs/{id},
// GET /api/relay-logs?request_id=), and straight from the logs database, because the API never
// serves content for a content_hidden row and so cannot prove the row itself holds none. The
// database is BIFROST_LOGS_DB_URL (sqlite:// or postgresql://) or, when unset, the logs_store of the
// gateway's config.json (BIFROST_E2E_CONFIG_PATH, default tests/integrations/python/config.json;
// sqlite paths resolve from BIFROST_E2E_SERVER_CWD, default the repo root). A logs database the
// runner cannot reach fails the run.
//
// The gateway dials the echo server and the collector, so both default to loopback, which needs the
// gateway on the runner's host. For a gateway in another container or host, set
// BIFROST_E2E_CALLBACK_HOST to an address it can reach the runner on (e.g. host.docker.internal);
// the mocks then listen on every interface.
//
// The gateway's client config and OTel plugin are restored afterwards, and the provider, relay and
// virtual keys this run creates are deleted.
//
//   node tests/e2e/api/runners/run-content-logging-matrix.mjs   (also runs inside make run-e2e-api / run-newman-api-tests.sh)
//   BIFROST_E2E_BASE_URL=http://localhost:8080 BIFROST_E2E_AUTH_HEADER="Bearer ..." node ...

import http from "node:http";
import { randomBytes } from "node:crypto";
import {
	allCases,
	callbackEndpoints,
	groupByConfig,
	headerValue,
	logRowFailures,
	normaliseRetentionDays,
	relayRowFailures,
	vkDisableValue,
	VK_MODES,
} from "./lib/content-logging-matrix.mjs";
import { addEchoProvider, addEchoRelay, createEchoUpstream, ECHO_MODEL } from "./lib/echo-upstream.mjs";
import { closeServer, gatewayClient, listen, poll, readBody } from "./lib/gateway-api.mjs";
import { openLogsDb } from "./lib/logs-db.mjs";

const baseURL = (process.env.BIFROST_E2E_BASE_URL || process.env.BIFROST_BASE_URL || "http://localhost:8080").replace(/\/+$/, "");
// OSS setup lock: while dashboard auth is not active, /api needs the setup token. The matrix runs
// after the runner turns auth back off, so every management call it makes depends on this.
const api = gatewayClient({
	baseURL,
	authHeader: process.env.BIFROST_E2E_AUTH_HEADER || "",
	setupToken: (process.env.BIFROST_E2E_SETUP_TOKEN || process.env.BIFROST_SETUP_TOKEN || "bifrost-e2e-setup-token").trim(),
});
const { request, mustRequest } = api;
const runTag = `clmx${process.pid}${Date.now().toString(36)}`;
const providerName = `clmx-provider-${runTag}`;
const relayName = `clmx-relay-${runTag}`;
const requestedModel = `${providerName}/${ECHO_MODEL}`;

const otelTraceBodies = [];

// Markers are random hex with no shared prefix with anything else in a request or span, so a marker
// found in an export can only have come from message content.
function marker() {
	return `m${randomBytes(8).toString("hex")}`;
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

async function createVirtualKey(vkMode) {
	// Pinned to the echo provider alone rather than allow_all_providers. A provider added through the
	// management API only becomes a parseable model prefix once its first request reaches it, so until
	// then "<provider>/echo-model" is load-balanced as a bare model across every provider the key may
	// use, and any provider whose key claims all models could take it. One candidate removes that.
	// The key is granted the run's relay, so the same key drives both kinds of request.
	const body = {
		name: `${runTag}-vk-${vkMode}`,
		is_active: true,
		provider_configs: [{ provider: providerName, weight: 1, allowed_models: ["*"], key_ids: ["*"] }],
		relay_grants: [{ relay_name: relayName }],
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

async function relayCall(requestID, vkValue, promptMarker, header) {
	const headers = { "x-request-id": requestID, "x-bf-vk": vkValue };
	if (header !== undefined) headers["x-bf-disable-content-logging"] = header;
	const res = await request("POST", `/relay/${relayName}/call`, { prompt: `prompt-${promptMarker}` }, headers);
	if (res.status !== 200) throw new Error(`relay ${requestID} returned ${res.status}: ${res.text}`);
	if (res.json?.reply !== `reply-${promptMarker}`) throw new Error(`relay ${requestID} got ${res.text.slice(0, 400)} from the echo upstream`);
}

async function readLog(requestID) {
	return poll(`log ${requestID}`, 30000, async () => {
		const res = await request("GET", `/api/logs/${encodeURIComponent(requestID)}`);
		if (res.status === 404) return null;
		if (!res.ok) throw new Error(`GET /api/logs/${requestID} failed with ${res.status}: ${res.text}`);
		return res.json;
	});
}

// readRelayLog finds the relay row by request id, then reads it by id so a hybrid store hydrates it.
async function readRelayLog(requestID) {
	const listed = await poll(`relay log ${requestID}`, 30000, async () => {
		const res = await request("GET", `/api/relay-logs?request_id=${encodeURIComponent(requestID)}`);
		if (!res.ok) throw new Error(`GET /api/relay-logs failed with ${res.status}: ${res.text}`);
		return res.json?.logs?.[0] ?? null;
	});
	const res = await mustRequest("GET", `/api/relay-logs/${encodeURIComponent(listed.id)}`);
	return res.json;
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

async function runGroup(group, keys, logsDb) {
	const results = [];
	const sent = [];
	for (const c of group.cases) {
		const requestID = `${runTag}-${c.id}`;
		const relayRequestID = `${requestID}-relay`;
		const promptMarker = marker();
		const relayMarker = marker();
		await chat(requestID, keys[c.vk].value, promptMarker, headerValue(c.header));
		await relayCall(relayRequestID, keys[c.vk].value, relayMarker, headerValue(c.header));
		sent.push({ c, requestID, relayRequestID, promptMarker, relayMarker });
	}
	for (const { c, requestID, relayRequestID, promptMarker, relayMarker } of sent) {
		const failures = [];
		const want = c.expected.logStoresContent;
		const log = await readLog(requestID);
		const logInput = JSON.stringify(log.input_history ?? null);
		const logOutput = JSON.stringify(log.output_message ?? null);
		const logHasContent = logInput.includes(promptMarker) || logOutput.includes(promptMarker);
		if (logHasContent !== want) {
			failures.push(`log store ${logHasContent ? "kept" : "dropped"} content, want ${want ? "kept" : "dropped"}`);
		}
		if (log.content_hidden !== !want) {
			failures.push(`content_hidden=${JSON.stringify(log.content_hidden)}, want ${!want}`);
		}
		const rawRow = await poll(`raw log row ${requestID}`, 30000, () => logsDb.read("logs", "id", requestID));
		failures.push(...logRowFailures(rawRow, promptMarker, want));

		// The relay twin: same key, same header, same decision.
		const relayLog = await readRelayLog(relayRequestID);
		const relayHasContent = String(relayLog.request_body ?? "").includes(relayMarker) && String(relayLog.response_body ?? "").includes(relayMarker);
		const relayLeaked = JSON.stringify(relayLog).includes(relayMarker);
		if (want ? !relayHasContent : relayLeaked) {
			failures.push(`relay row ${want ? "dropped" : "kept"} content, want ${want ? "kept" : "dropped"} like its LLM twin`);
		}
		if (relayLog.content_hidden !== !want) {
			failures.push(`relay content_hidden=${JSON.stringify(relayLog.content_hidden)}, want ${!want} like its LLM twin`);
		}
		const rawRelayRow = await poll(`raw relay row ${relayRequestID}`, 30000, () => logsDb.read("relay_logs", "request_id", relayRequestID));
		failures.push(...relayRowFailures(rawRelayRow, relayMarker, want));

		await poll(`OTel export of ${requestID}`, 30000, () => exportsContain(requestID));
		const exported = exportsContain(promptMarker) || exportsContain(`reply-${promptMarker}`);
		if (exported !== c.expected.connectorExportsContent) {
			failures.push(`OTel ${exported ? "exported" : "stripped"} content, want ${c.expected.connectorExportsContent ? "exported" : "stripped"}`);
		}
		// A connector that disables content, or a key that does, must keep relay bodies out of the
		// export too.
		if (!c.expected.connectorExportsContent && exportsContain(relayMarker)) {
			failures.push("OTel exported relay content, want stripped");
		}
		results.push({ c, failures });
	}
	return results;
}

async function main() {
	console.log("Running content-logging permutation matrix (LLM and relay rows)...");
	console.log(`  Bifrost: ${baseURL}`);

	const configSnapshot = await api.configSnapshot();
	const originalOtel = await getPlugin("otel");
	const logsDb = await openLogsDb();
	console.log(`  Logs DB: ${logsDb.label}`);

	const otelReceiver = createOtelReceiver();
	const echo = createEchoUpstream();
	const callbackHost = process.env.BIFROST_E2E_CALLBACK_HOST || "";
	const { listenHost } = callbackEndpoints({ callbackHost, providerPort: 0, collectorPort: 0 });
	const otelPort = await listen(otelReceiver, listenHost);
	const mockPort = await listen(echo, listenHost);
	const endpoints = callbackEndpoints({ callbackHost, providerPort: mockPort, collectorPort: otelPort });
	console.log(`  Echo upstream: ${endpoints.providerBaseURL} (listening on ${listenHost})`);
	console.log(`  OTel collector: ${endpoints.collectorURL}`);
	const createdKeys = [];
	let providerAdded = false;
	let relayAdded = false;
	const results = [];

	try {
		await addEchoProvider(api, providerName, endpoints.providerBaseURL);
		providerAdded = true;
		await addEchoRelay(api, relayName, endpoints.providerBaseURL);
		relayAdded = true;
		const keys = {};
		for (const vkMode of VK_MODES) {
			keys[vkMode] = await createVirtualKey(vkMode);
			createdKeys.push(keys[vkMode].id);
		}

		for (const group of groupByConfig(allCases())) {
			await api.putClientConfig(configSnapshot, {
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
		if (relayAdded) {
			const res = await request("DELETE", `/api/relays/${encodeURIComponent(relayName)}`).catch((e) => ({ ok: false, text: e.message }));
			if (!res.ok) cleanupErrors.push(`delete relay: ${res.text}`);
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
			await api.putClientConfig(configSnapshot, {});
		} catch (err) {
			cleanupErrors.push(`restore client config: ${err.message}`);
		}
		await Promise.all([closeServer(otelReceiver), closeServer(echo), logsDb.close().catch(() => {})]);
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
