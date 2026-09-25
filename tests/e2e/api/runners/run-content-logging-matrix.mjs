#!/usr/bin/env node
// Content-logging permutation check against a live gateway.
//
// Drives every combination in lib/content-logging-matrix.mjs (team, virtual key and provider key
// each inherit/on/off, client flag on/off, OTel connector flag on/off, per-request overrides
// allowed/blocked, request header absent/on/off) and checks, per request, whether its content
// reached the log store and whether it reached the OTel collector. A last group sends requests
// that fail on a provider key set to off and retry onto one set to on, which must stay off.
// Everything upstream is local: a mock OpenAI-compatible provider that echoes a per-request marker,
// and a mock OTLP/HTTP collector. No provider credentials, no paid calls.
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
// The gateway's client config and OTel plugin are restored afterwards, and the providers, teams and
// virtual keys this run creates are deleted. The live plumbing is shared with the enterprise runner
// through lib/content-logging-live.mjs.
//
//   node tests/e2e/api/runners/run-content-logging-matrix.mjs   (also runs inside make run-e2e-api / run-newman-api-tests.sh)
//   BIFROST_E2E_BASE_URL=http://localhost:8080 BIFROST_E2E_AUTH_HEADER="Bearer ..." node ...

import { allCases, callbackEndpoints, groupByConfig, headerValue, LAYER_MODES, layerDisableValue } from "./lib/content-logging-matrix.mjs";
import {
	chatSender,
	close,
	createChecker,
	createClient,
	createEchoProvider,
	createOtelReceiver,
	ECHO_MODEL,
	gatewayConfig,
	listen,
	marker,
	openLogsDb,
	poll,
} from "./lib/content-logging-live.mjs";

const baseURL = (process.env.BIFROST_E2E_BASE_URL || process.env.BIFROST_BASE_URL || "http://localhost:8080").replace(/\/+$/, "");
const adminAuthHeader = process.env.BIFROST_E2E_AUTH_HEADER || "";
const runTag = `clmx${process.pid}${Date.now().toString(36)}`;
const providerName = `clmx-provider-${runTag}`;
const requestedModel = `${providerName}/${ECHO_MODEL}`;
const retryProviderName = `clmx-retry-${runTag}`;
const retryModel = `${retryProviderName}/${ECHO_MODEL}`;
// Requests sent through the retry provider. Its "off" key is weighted to be picked first most of the
// time and always fails, so nearly every request retries onto the "on" key; enough are sent that
// the off-then-on path is all but certain to run, and the run fails if it never did.
const RETRY_REQUESTS = 6;

// Provider key values by mode. The echo provider sees the one each attempt used in Authorization.
const providerKeyValue = (mode) => `sk-${runTag}-${mode}`;
const retryKeyValue = (mode) => `sk-${runTag}-retry-${mode}`;

const client = createClient({ baseURL, adminAuthHeader });
const chat = chatSender(client);

async function addEchoProvider(name, providerBaseURL, maxRetries) {
	await client.mustRequest("POST", "/api/providers", {
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
	const res = await client.mustRequest("POST", `/api/providers/${encodeURIComponent(provider)}/keys`, body);
	const key = res.json?.key ?? res.json;
	if (!key?.id) throw new Error(`provider key create returned no id: ${res.text}`);
	checkStored(`provider key ${name}`, key.disable_content_logging, decision);
	return key.id;
}

async function createTeam(teamMode) {
	const body = { name: `${runTag}-team-${teamMode}` };
	const decision = layerDisableValue(teamMode);
	if (decision !== undefined) body.disable_content_logging = decision;
	const res = await client.mustRequest("POST", "/api/governance/teams", body);
	const team = res.json?.team;
	if (!team?.id) throw new Error(`team create returned no id: ${res.text}`);
	checkStored(`team ${teamMode}`, team.disable_content_logging, decision);
	return team.id;
}

async function createVirtualKey(vkMode, { teamMode, teamID, provider = providerName }) {
	// Pinned to one echo provider alone rather than allow_all_providers. A provider added through the
	// management API only becomes a parseable model prefix once its first request reaches it, so until
	// then "<provider>/echo-model" is load-balanced as a bare model across every provider the key may
	// use, and any provider whose key claims all models could take it. One candidate removes that.
	const body = {
		name: `${runTag}-vk-${teamMode ?? "noteam"}-${vkMode}-${provider === providerName ? "matrix" : "retry"}`,
		is_active: true,
		provider_configs: [{ provider, weight: 1, allowed_models: ["*"], key_ids: ["*"] }],
	};
	if (teamID) body.team_id = teamID;
	const decision = layerDisableValue(vkMode);
	if (decision !== undefined) body.disable_content_logging = decision;
	const res = await client.mustRequest("POST", "/api/governance/virtual-keys", body);
	const vk = res.json?.virtual_key ?? res.json;
	if (!vk?.id || !vk?.value) throw new Error(`virtual key create returned no id/value: ${res.text}`);
	checkStored(`virtual key ${vkMode}`, vk.disable_content_logging, decision);
	return { id: vk.id, value: vk.value };
}

// waitForConfig proves a reconfiguration is live before the group's cases run: a probe through the
// inheriting key must export (or not) exactly as the new connector flag says. The plugin reload and
// the exporter both run asynchronously, so this retries a fresh probe until the new state answers.
async function waitForConfig(group, otel, inheritKey, inheritProviderKeyID) {
	const wantExported = group.connector === "on";
	await poll(`config ${group.global}/${group.connector}/${group.override} to take effect`, 45000, async () => {
		const probeID = `${runTag}-probe-${marker()}`;
		const probeMarker = marker();
		await chat(probeID, { vkValue: inheritKey.value, promptMarker: probeMarker, providerKeyID: inheritProviderKeyID, model: requestedModel });
		await poll(`probe ${probeID} export`, 10000, () => otel.contains(probeID));
		return otel.contains(probeMarker) === wantExported;
	});
}

function describe(c) {
	return `team=${c.team.padEnd(7)} vk=${c.vk.padEnd(7)} providerKey=${c.providerKey.padEnd(7)} global=${c.global.padEnd(3)} connector=${c.connector.padEnd(3)} override=${c.override.padEnd(7)} header=${c.header}`;
}

async function runGroup(group, virtualKeys, providerKeyIDs, checkCase) {
	const sent = [];
	for (const c of group.cases) {
		const requestID = `${runTag}-${c.id}`;
		const promptMarker = marker();
		await chat(requestID, {
			vkValue: virtualKeys[c.team][c.vk].value,
			promptMarker,
			header: headerValue(c.header),
			providerKeyID: providerKeyIDs[c.providerKey],
			model: requestedModel,
		});
		sent.push({ c, requestID, promptMarker });
	}
	const results = [];
	for (const { c, requestID, promptMarker } of sent) {
		results.push({ label: describe(c), failures: await checkCase(requestID, promptMarker, c.expected) });
	}
	return results;
}

// runRetryGroup sends requests through the retry provider, whose "off" key always fails. Each
// request is checked against the keys it actually reached: once one of its attempts went out on the
// "off" key, content stays off even though the "on" key served the response.
async function runRetryGroup(retryVirtualKey, keysSeenByMarker, checkCase) {
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
		const failures = await checkCase(requestID, promptMarker, expected);
		results.push({ label: `retry #${i} keys=${seen.map((k) => (k === retryKeyValue("off") ? "off" : "on")).join(">")}`, failures });
	}
	if (retriedFromOff === 0) {
		results.push({ label: "retry path", failures: [`none of ${RETRY_REQUESTS} requests retried from the off key onto the on key`] });
	}
	return results;
}

async function main() {
	console.log("Running content-logging permutation matrix...");
	console.log(`  Bifrost: ${baseURL}`);

	const configSnapshot = (await client.mustRequest("GET", "/api/config")).json;
	if (!configSnapshot?.client_config) throw new Error("GET /api/config returned no client_config");
	const gateway = gatewayConfig(client);
	const originalOtel = await gateway.getPlugin("otel");
	const logsDb = await openLogsDb();
	console.log(`  Logs DB: ${logsDb.label}`);

	const otel = createOtelReceiver();
	const echo = createEchoProvider({ failingKeyValues: new Set([retryKeyValue("off")]) });
	const callbackHost = process.env.BIFROST_E2E_CALLBACK_HOST || "";
	const { listenHost } = callbackEndpoints({ callbackHost, providerPort: 0, collectorPort: 0 });
	const otelPort = await listen(otel.server, listenHost);
	const mockPort = await listen(echo.server, listenHost);
	const endpoints = callbackEndpoints({ callbackHost, providerPort: mockPort, collectorPort: otelPort });
	console.log(`  Echo provider: ${endpoints.providerBaseURL} (listening on ${listenHost})`);
	console.log(`  OTel collector: ${endpoints.collectorURL}`);
	const checkCase = createChecker({ client, logsDb, otel });
	const createdKeys = [];
	const createdTeams = [];
	const addedProviders = [];
	const results = [];

	try {
		await addEchoProvider(providerName, endpoints.providerBaseURL, 0);
		addedProviders.push(providerName);
		const providerKeyIDs = {};
		for (const mode of LAYER_MODES) {
			providerKeyIDs[mode] = await addProviderKey(providerName, `${runTag}-key-${mode}`, providerKeyValue(mode), mode, 1);
		}
		const virtualKeys = {};
		for (const teamMode of LAYER_MODES) {
			const teamID = await createTeam(teamMode);
			createdTeams.push(teamID);
			virtualKeys[teamMode] = {};
			for (const vkMode of LAYER_MODES) {
				virtualKeys[teamMode][vkMode] = await createVirtualKey(vkMode, { teamMode, teamID });
				createdKeys.push(virtualKeys[teamMode][vkMode].id);
			}
		}

		const report = (groupResults) => {
			for (const r of groupResults) {
				console.log(`  ${r.failures.length === 0 ? "ok  " : "FAIL"} ${r.label}${r.failures.length ? ` -> ${r.failures.join("; ")}` : ""}`);
			}
			results.push(...groupResults);
		};

		for (const group of groupByConfig(allCases())) {
			await gateway.applyGroup(configSnapshot, group, endpoints.collectorURL);
			await waitForConfig(group, otel, virtualKeys.inherit.inherit, providerKeyIDs.inherit);
			report(await runGroup(group, virtualKeys, providerKeyIDs, checkCase));
		}

		// Sticky off across a retry onto another key: content on everywhere else, so only the provider
		// key the request was sent on can turn it off.
		await addEchoProvider(retryProviderName, endpoints.providerBaseURL, 2);
		addedProviders.push(retryProviderName);
		await addProviderKey(retryProviderName, `${runTag}-retry-key-off`, retryKeyValue("off"), "off", 1);
		await addProviderKey(retryProviderName, `${runTag}-retry-key-on`, retryKeyValue("on"), "on", 0.1);
		const retryVirtualKey = await createVirtualKey("inherit", { provider: retryProviderName });
		createdKeys.push(retryVirtualKey.id);
		const openGroup = { global: "on", connector: "on", override: "blocked" };
		await gateway.applyGroup(configSnapshot, openGroup, endpoints.collectorURL);
		await waitForConfig(openGroup, otel, virtualKeys.inherit.inherit, providerKeyIDs.inherit);
		report(await runRetryGroup(retryVirtualKey, echo.keysSeenByMarker, checkCase));
	} finally {
		const cleanupErrors = [];
		for (const id of createdKeys) {
			const res = await client.request("DELETE", `/api/governance/virtual-keys/${encodeURIComponent(id)}`).catch((e) => ({ ok: false, text: e.message }));
			if (!res.ok) cleanupErrors.push(`delete virtual key ${id}: ${res.text}`);
		}
		for (const id of createdTeams) {
			const res = await client.request("DELETE", `/api/governance/teams/${encodeURIComponent(id)}`).catch((e) => ({ ok: false, text: e.message }));
			if (!res.ok) cleanupErrors.push(`delete team ${id}: ${res.text}`);
		}
		for (const name of addedProviders) {
			const res = await client.request("DELETE", `/api/providers/${encodeURIComponent(name)}`).catch((e) => ({ ok: false, text: e.message }));
			if (!res.ok) cleanupErrors.push(`delete provider ${name}: ${res.text}`);
		}
		cleanupErrors.push(...(await gateway.restore(configSnapshot, originalOtel)));
		await Promise.all([close(otel.server), close(echo.server), logsDb.close().catch(() => {})]);
		for (const e of cleanupErrors) console.warn(`WARNING: cleanup: ${e}`);
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
