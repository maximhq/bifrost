#!/usr/bin/env node
// Content-logging permutation check against a live gateway, for LLM rows and relay rows alike.
//
// Drives every combination in lib/content-logging-matrix.mjs (team, virtual key and provider key
// each inherit/on/off, client flag on/off, OTel connector flag on/off, per-request overrides
// allowed/blocked, request header absent/on/off). Each case sends an LLM request and a relay request
// on the same virtual key and header and checks, per request, whether its content reached the log
// store, and for the LLM request whether it reached the OTel collector. A relay picks no provider
// key, so a relay row follows the decision its LLM twin would get with the provider key layer at
// inherit (team and virtual key still apply); a relay row that keeps or drops content differently
// from that fails the case. A last group sends requests that fail on a provider key set to off and
// retry onto one set to on, which must stay off.
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
// The gateway's client config and OTel plugin are restored afterwards, and the providers, relay,
// teams and virtual keys this run creates are deleted.
//
//   node tests/e2e/api/runners/run-content-logging-matrix.mjs   (also runs inside make run-e2e-api / run-newman-api-tests.sh)
//   BIFROST_E2E_BASE_URL=http://localhost:8080 BIFROST_E2E_AUTH_HEADER="Bearer ..." node ...

import http from "node:http";
import { randomBytes } from "node:crypto";
import { Readable } from "node:stream";
import {
	allCases,
	callbackEndpoints,
	expectedOutcome,
	groupByConfig,
	headerValue,
	LAYER_MODES,
	layerDisableValue,
	logRowFailures,
	normaliseRetentionDays,
	relayRowFailures,
} from "./lib/content-logging-matrix.mjs";
import { addEchoRelay, createEchoUpstream, ECHO_MODEL } from "./lib/echo-upstream.mjs";
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
const retryProviderName = `clmx-retry-${runTag}`;
const retryModel = `${retryProviderName}/${ECHO_MODEL}`;
// Requests sent through the retry provider. Its "off" key is weighted to be picked first most of the
// time and always fails, so nearly every request retries onto the "on" key; enough are sent that
// the off-then-on path is all but certain to run, and the run fails if it never did.
const RETRY_REQUESTS = 6;

// Provider key values by mode. The echo upstream sees the one each attempt used in Authorization.
const providerKeyValue = (mode) => `sk-${runTag}-${mode}`;
const retryKeyValue = (mode) => `sk-${runTag}-retry-${mode}`;

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

// createKeyedEchoUpstream wraps the shared echo upstream with what the provider-key axis needs: it
// records the provider key each chat attempt arrived with, per marker, and answers 401 to any attempt
// whose key is in failingKeyValues, to drive a retry onto another key. It must be a per-key status: a
// 5xx is transient and Bifrost retries it on the same key, so the request would never reach another.
// Every other request (and every chat attempt it lets through) is replayed into the shared upstream
// unchanged, so relay calls and chat replies keep the shared shape.
function createKeyedEchoUpstream({ failingKeyValues = new Set() } = {}) {
	const echo = createEchoUpstream();
	const keysSeenByMarker = new Map();
	const server = http.createServer(async (req, res) => {
		const body = await readBody(req);
		if (req.method === "POST" && new URL(req.url || "/", "http://echo").pathname === "/v1/chat/completions") {
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
				res.writeHead(401, { "content-type": "application/json" });
				res.end(JSON.stringify({ error: { message: "echo upstream refuses this key", type: "authentication_error" } }));
				return;
			}
		}
		const replay = Object.assign(Readable.from(body.length ? [body] : []), { method: req.method, url: req.url, headers: req.headers });
		echo.emit("request", replay, res);
	});
	return { server, keysSeenByMarker };
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

// applyGroup puts the gateway into one matrix group's configuration. Object-storage retention would
// keep disabled content as hidden rows, so it is pinned off: a disabled request always leaves a
// content-free row.
async function applyGroup(configSnapshot, group, collectorURL) {
	await api.putClientConfig(configSnapshot, {
		disable_content_logging: group.global === "off",
		allow_per_request_content_storage_override: group.override === "allowed",
		retain_content_in_object_storage: false,
	});
	await setOtel(collectorURL, group.connector === "off");
}

// addKeyedEchoProvider registers the echo upstream as an OpenAI-compatible custom provider that
// takes keys, unlike the shared keyless addEchoProvider: the provider-key axis needs real keys that
// carry a content-logging mode, and the retry group needs retries.
async function addKeyedEchoProvider(name, providerBaseURL, maxRetries) {
	await mustRequest("POST", "/api/providers", {
		provider: name,
		custom_provider_config: { base_provider_type: "openai", is_key_less: false, allowed_requests: { chat_completion: true } },
		network_config: {
			base_url: providerBaseURL,
			allow_private_network: true,
			default_request_timeout_in_seconds: 10,
			max_retries: maxRetries,
			retry_backoff_initial: 100,
			retry_backoff_max: 500,
		},
		concurrency_and_buffer_size: { concurrency: 20, buffer_size: 200 },
		keys: [],
	});
}

// checkStored fails when a created entity did not store the decision it was created with.
function checkStored(what, stored, decision) {
	if ((decision === undefined && stored != null) || (decision !== undefined && stored !== decision)) {
		throw new Error(`${what} stored disable_content_logging=${JSON.stringify(stored)}, want ${JSON.stringify(decision)}`);
	}
}

// addProviderKey adds a key carrying a content-logging mode and checks the stored decision.
async function addProviderKey(provider, name, value, mode, weight) {
	const body = { name, value, models: ["*"], weight };
	const decision = layerDisableValue(mode);
	if (decision !== undefined) body.disable_content_logging = decision;
	const res = await mustRequest("POST", `/api/providers/${encodeURIComponent(provider)}/keys`, body);
	const key = res.json?.key ?? res.json;
	if (!key?.id) throw new Error(`provider key create returned no id: ${res.text}`);
	checkStored(`provider key ${name}`, key.disable_content_logging, decision);
	return key.id;
}

// createTeam and createVirtualKey record the new id on the caller's cleanup list before they check
// the stored decision, so a run that fails that check still deletes what it created.
async function createTeam(teamMode, createdTeams) {
	const body = { name: `${runTag}-team-${teamMode}` };
	const decision = layerDisableValue(teamMode);
	if (decision !== undefined) body.disable_content_logging = decision;
	const res = await mustRequest("POST", "/api/governance/teams", body);
	const team = res.json?.team;
	if (!team?.id) throw new Error(`team create returned no id: ${res.text}`);
	createdTeams.push(team.id);
	checkStored(`team ${teamMode}`, team.disable_content_logging, decision);
	return team.id;
}

async function createVirtualKey(vkMode, { teamMode, teamID, provider = providerName }, createdKeys) {
	// Pinned to one echo provider alone rather than allow_all_providers. A provider added through the
	// management API only becomes a parseable model prefix once its first request reaches it, so until
	// then "<provider>/echo-model" is load-balanced as a bare model across every provider the key may
	// use, and any provider whose key claims all models could take it. One candidate removes that.
	// The key is granted the run's relay, so the same key drives both kinds of request.
	const body = {
		name: `${runTag}-vk-${teamMode ?? "noteam"}-${vkMode}-${provider === providerName ? "matrix" : "retry"}`,
		is_active: true,
		provider_configs: [{ provider, weight: 1, allowed_models: ["*"], key_ids: ["*"] }],
		relay_grants: [{ relay_name: relayName }],
	};
	if (teamID) body.team_id = teamID;
	const decision = layerDisableValue(vkMode);
	if (decision !== undefined) body.disable_content_logging = decision;
	const res = await mustRequest("POST", "/api/governance/virtual-keys", body);
	const vk = res.json?.virtual_key ?? res.json;
	if (!vk?.id || !vk?.value) throw new Error(`virtual key create returned no id/value: ${res.text}`);
	createdKeys.push(vk.id);
	checkStored(`virtual key ${vkMode}`, vk.disable_content_logging, decision);
	return { id: vk.id, value: vk.value };
}

// chat sends one chat completion carrying a marker and checks the echo came back. providerKeyID pins
// the provider key the request is served by, so its decision is the one under test.
async function chat(requestID, { vkValue, promptMarker, header, providerKeyID, model = requestedModel }) {
	const headers = { "x-request-id": requestID, "x-bf-vk": vkValue };
	if (header !== undefined) headers["x-bf-disable-content-logging"] = header;
	if (providerKeyID) headers["x-bf-api-key-id"] = providerKeyID;
	const res = await request("POST", "/v1/chat/completions", { model, messages: [{ role: "user", content: `prompt-${promptMarker}` }] }, headers);
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

// checkLLM judges one LLM request against its expected outcome: through the logs API, straight from
// the logs database row, and in the OTel export.
async function checkLLM(logsDb, requestID, promptMarker, expected) {
	const failures = [];
	const want = expected.logStoresContent;
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
	await poll(`OTel export of ${requestID}`, 30000, () => exportsContain(requestID));
	const exported = exportsContain(promptMarker) || exportsContain(`reply-${promptMarker}`);
	if (exported !== expected.connectorExportsContent) {
		failures.push(`OTel ${exported ? "exported" : "stripped"} content, want ${expected.connectorExportsContent ? "exported" : "stripped"}`);
	}
	return failures;
}

// checkRelay judges one relay request against the relay's expected outcome: through the relay logs
// API, straight from the relay_logs row, and (when content must be stripped) in the OTel export.
async function checkRelay(logsDb, relayRequestID, relayMarker, expected) {
	const failures = [];
	const want = expected.logStoresContent;
	const relayLog = await readRelayLog(relayRequestID);
	const relayHasContent = String(relayLog.request_body ?? "").includes(relayMarker) && String(relayLog.response_body ?? "").includes(relayMarker);
	const relayLeaked = JSON.stringify(relayLog).includes(relayMarker);
	if (want ? !relayHasContent : relayLeaked) {
		failures.push(`relay row ${want ? "dropped" : "kept"} content, want ${want ? "kept" : "dropped"}`);
	}
	if (relayLog.content_hidden !== !want) {
		failures.push(`relay content_hidden=${JSON.stringify(relayLog.content_hidden)}, want ${!want}`);
	}
	const rawRelayRow = await poll(`raw relay row ${relayRequestID}`, 30000, () => logsDb.read("relay_logs", "request_id", relayRequestID));
	failures.push(...relayRowFailures(rawRelayRow, relayMarker, want));
	// A connector that disables content, or an admin layer that does, must keep relay bodies out of
	// the export too.
	if (!expected.connectorExportsContent && exportsContain(relayMarker)) {
		failures.push("OTel exported relay content, want stripped");
	}
	return failures;
}

// relayExpectedOutcome is the decision a case's relay twin must follow. A relay call picks no
// provider key, so the provider-key layer cannot apply to it: the relay gets what its LLM twin would
// get with providerKey at inherit, which still resolves the team and virtual key layers.
function relayExpectedOutcome(c) {
	return expectedOutcome({ ...c, providerKey: "inherit" });
}

// waitForConfig proves a reconfiguration is live before the group's cases run: a probe through the
// inheriting key must export (or not) exactly as the new connector flag says. The plugin reload and
// the exporter both run asynchronously, so this retries a fresh probe until the new state answers.
async function waitForConfig(group, inheritKey, inheritProviderKeyID) {
	const wantExported = group.connector === "on";
	await poll(`config ${group.global}/${group.connector}/${group.override} to take effect`, 45000, async () => {
		const probeID = `${runTag}-probe-${randomBytes(4).toString("hex")}`;
		const probeMarker = marker();
		await chat(probeID, { vkValue: inheritKey.value, promptMarker: probeMarker, providerKeyID: inheritProviderKeyID });
		await poll(`probe ${probeID} export`, 10000, () => exportsContain(probeID));
		return exportsContain(probeMarker) === wantExported;
	});
}

function describe(c) {
	return `team=${c.team.padEnd(7)} vk=${c.vk.padEnd(7)} providerKey=${c.providerKey.padEnd(7)} global=${c.global.padEnd(3)} connector=${c.connector.padEnd(3)} override=${c.override.padEnd(7)} header=${c.header}`;
}

async function runGroup(group, virtualKeys, providerKeyIDs, logsDb) {
	const sent = [];
	for (const c of group.cases) {
		const requestID = `${runTag}-${c.id}`;
		const relayRequestID = `${requestID}-relay`;
		const promptMarker = marker();
		const relayMarker = marker();
		const vkValue = virtualKeys[c.team][c.vk].value;
		const header = headerValue(c.header);
		await chat(requestID, { vkValue, promptMarker, header, providerKeyID: providerKeyIDs[c.providerKey] });
		await relayCall(relayRequestID, vkValue, relayMarker, header);
		sent.push({ c, requestID, relayRequestID, promptMarker, relayMarker });
	}
	const results = [];
	for (const { c, requestID, relayRequestID, promptMarker, relayMarker } of sent) {
		const failures = await checkLLM(logsDb, requestID, promptMarker, c.expected);
		failures.push(...(await checkRelay(logsDb, relayRequestID, relayMarker, relayExpectedOutcome(c))));
		results.push({ label: describe(c), failures });
	}
	return results;
}

// runRetryGroup sends requests through the retry provider, whose "off" key always fails. Each
// request is checked against the keys it actually reached: once one of its attempts went out on the
// "off" key, content stays off even though the "on" key served the response. No relay twin: a relay
// picks no provider key, so the retry path does not exist for it.
async function runRetryGroup(retryVirtualKey, keysSeenByMarker, logsDb) {
	const results = [];
	let retriedFromOff = 0;
	for (let i = 0; i < RETRY_REQUESTS; i++) {
		const requestID = `${runTag}-retry-${i}`;
		const promptMarker = marker();
		await chat(requestID, { vkValue: retryVirtualKey.value, promptMarker, model: retryModel });
		const seen = keysSeenByMarker.get(promptMarker) ?? [];
		const sentOnOff = seen.includes(retryKeyValue("off"));
		if (sentOnOff && seen.at(-1) === retryKeyValue("on")) retriedFromOff++;
		const expected = { logStoresContent: !sentOnOff, connectorExportsContent: !sentOnOff };
		const failures = await checkLLM(logsDb, requestID, promptMarker, expected);
		results.push({ label: `retry #${i} keys=${seen.map((k) => (k === retryKeyValue("off") ? "off" : "on")).join(">")}`, failures });
	}
	if (retriedFromOff === 0) {
		results.push({ label: "retry path", failures: [`none of ${RETRY_REQUESTS} requests retried from the off key onto the on key`] });
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
	const echo = createKeyedEchoUpstream({ failingKeyValues: new Set([retryKeyValue("off")]) });
	const callbackHost = process.env.BIFROST_E2E_CALLBACK_HOST || "";
	const { listenHost } = callbackEndpoints({ callbackHost, providerPort: 0, collectorPort: 0 });
	const otelPort = await listen(otelReceiver, listenHost);
	const mockPort = await listen(echo.server, listenHost);
	const endpoints = callbackEndpoints({ callbackHost, providerPort: mockPort, collectorPort: otelPort });
	console.log(`  Echo upstream: ${endpoints.providerBaseURL} (listening on ${listenHost})`);
	console.log(`  OTel collector: ${endpoints.collectorURL}`);
	const createdKeys = [];
	const createdTeams = [];
	const addedProviders = [];
	let relayAdded = false;
	const results = [];

	try {
		await addKeyedEchoProvider(providerName, endpoints.providerBaseURL, 0);
		addedProviders.push(providerName);
		await addEchoRelay(api, relayName, endpoints.providerBaseURL);
		relayAdded = true;
		const providerKeyIDs = {};
		for (const mode of LAYER_MODES) {
			providerKeyIDs[mode] = await addProviderKey(providerName, `${runTag}-key-${mode}`, providerKeyValue(mode), mode, 1);
		}
		const virtualKeys = {};
		for (const teamMode of LAYER_MODES) {
			const teamID = await createTeam(teamMode, createdTeams);
			virtualKeys[teamMode] = {};
			for (const vkMode of LAYER_MODES) {
				virtualKeys[teamMode][vkMode] = await createVirtualKey(vkMode, { teamMode, teamID }, createdKeys);
			}
		}

		const report = (groupResults) => {
			for (const r of groupResults) {
				console.log(`  ${r.failures.length === 0 ? "ok  " : "FAIL"} ${r.label}${r.failures.length ? ` -> ${r.failures.join("; ")}` : ""}`);
			}
			results.push(...groupResults);
		};

		for (const group of groupByConfig(allCases())) {
			await applyGroup(configSnapshot, group, endpoints.collectorURL);
			await waitForConfig(group, virtualKeys.inherit.inherit, providerKeyIDs.inherit);
			report(await runGroup(group, virtualKeys, providerKeyIDs, logsDb));
		}

		// Sticky off across a retry onto another key: content on everywhere else, so only the provider
		// key the request was sent on can turn it off.
		await addKeyedEchoProvider(retryProviderName, endpoints.providerBaseURL, 2);
		addedProviders.push(retryProviderName);
		await addProviderKey(retryProviderName, `${runTag}-retry-key-off`, retryKeyValue("off"), "off", 1);
		await addProviderKey(retryProviderName, `${runTag}-retry-key-on`, retryKeyValue("on"), "on", 0.1);
		const retryVirtualKey = await createVirtualKey("inherit", { provider: retryProviderName }, createdKeys);
		const openGroup = { global: "on", connector: "on", override: "blocked" };
		await applyGroup(configSnapshot, openGroup, endpoints.collectorURL);
		await waitForConfig(openGroup, virtualKeys.inherit.inherit, providerKeyIDs.inherit);
		report(await runRetryGroup(retryVirtualKey, echo.keysSeenByMarker, logsDb));
	} finally {
		const cleanupErrors = [];
		for (const id of createdKeys) {
			const res = await request("DELETE", `/api/governance/virtual-keys/${encodeURIComponent(id)}`).catch((e) => ({ ok: false, text: e.message }));
			if (!res.ok) cleanupErrors.push(`delete virtual key ${id}: ${res.text}`);
		}
		for (const id of createdTeams) {
			const res = await request("DELETE", `/api/governance/teams/${encodeURIComponent(id)}`).catch((e) => ({ ok: false, text: e.message }));
			if (!res.ok) cleanupErrors.push(`delete team ${id}: ${res.text}`);
		}
		if (relayAdded) {
			const res = await request("DELETE", `/api/relays/${encodeURIComponent(relayName)}`).catch((e) => ({ ok: false, text: e.message }));
			if (!res.ok) cleanupErrors.push(`delete relay: ${res.text}`);
		}
		for (const name of addedProviders) {
			const res = await request("DELETE", `/api/providers/${encodeURIComponent(name)}`).catch((e) => ({ ok: false, text: e.message }));
			if (!res.ok) cleanupErrors.push(`delete provider ${name}: ${res.text}`);
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
		await Promise.all([closeServer(otelReceiver), closeServer(echo.server), logsDb.close().catch(() => {})]);
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
	if (failed.length > 0 || results.length < allCases().length + RETRY_REQUESTS) {
		process.exit(1);
	}
}

main().catch((err) => {
	console.error(`Content-logging matrix failed: ${err.message}`);
	process.exit(1);
});
