#!/usr/bin/env node
// Relay permutation matrix against a live gateway: every relay setting a caller or an operator
// can combine, checked on the wire, in the Relay Logs API, in the raw logs database, and, where the
// same setting reaches LLM traffic, against an LLM row sent the same way.
//
// Suites (all by default; pick with --suites a,b or RELAY_MATRIX_SUITES):
//   pricing     every usage mapping: source x measure x rate x fee x block x minimum x reported
//               cost x ignore_upstream_cost, each through an inherit, override and waive route,
//               with token paths and model source rotating across cases
//   streaming   route stream flag x streaming.paths x upstream content type x measure
//   billing     method x billable flag x upstream status
//   routes      path globs, header matchers, operator headers, order, disabled routes, skip_route_check
//   auth        bearer, header, query, basic, none, aws_sigv4, extra headers, credential stripping
//   governance  grants, inactive and unknown keys, inference auth, attribution parity, budgets and
//               request limits at key, team and customer level, shared LLM/relay counters
//   settings    logging_headers, x-bf-lh-*, x-bf-dim-*, key metadata, session ids, user agent and
//               app, hide_deleted_virtual_keys_in_filters, query-string redaction (LLM vs relay)
//   logsapi     every shared filter, stats, histogram, sort, paging and delete (LLM vs relay)
//   network     timeouts, stream idle timeout, redirects, TLS (CA and skip-verify), unreachable
//               upstream, a reply past the old 64 MiB cap, a caller that hangs up mid-stream
//   proxy       a relay's proxy_config (http, socks5, none, credentials, an https upstream through
//               a CONNECT tunnel) and the inherited global proxy, against local proxies that
//               record every target; proxy/run-proxy-matrix.sh covers squid/stunnel/microsocks
//   hybrid      object storage offload, previews, hydration, hidden retention, exclusions and
//               deletes (LLM vs relay); runs only when the gateway has object storage
//
// Every upstream is local: a programmable relay upstream, an echo server that answers both the LLM
// provider and an echo relay, and an HTTPS twin for the TLS cases. No credentials, no paid calls.
//
// Environment:
//   BIFROST_E2E_BASE_URL            gateway (default http://localhost:8080)
//   BIFROST_E2E_AUTH_HEADER         admin Authorization header, when dashboard auth is on
//   BIFROST_E2E_SETUP_TOKEN         setup token (default bifrost-e2e-setup-token)
//   BIFROST_LOGS_DB_URL             logs database for raw-row checks (see lib/logs-db.mjs)
//   BIFROST_E2E_FAKE_S3_URL         a lib/fake-s3.mjs instance the gateway's logs_store offloads to;
//                                   enables the hybrid suite
//   BIFROST_E2E_OBJECT_STORAGE_PREFIX            object prefix (default bifrost)
//   BIFROST_E2E_OBJECT_STORAGE_EXCLUDE_TYPES     the gateway's object_storage_exclude_request_types
//   BIFROST_E2E_OBJECT_STORAGE_EXCLUDE_FIELDS    the gateway's object_storage_exclude_fields
//   RELAY_MATRIX_LARGE_MB           size of the past-the-old-cap reply (default 65)
//
// The run restores the client config it changes and deletes every relay, key, team, customer and
// provider it creates. It exits non-zero on any FAIL, on a STALE known gap, or on an error.

import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import http from "node:http";
import https from "node:https";
import { gunzipSync } from "node:zlib";
import { execFileSync } from "node:child_process";
import { randomBytes } from "node:crypto";
import {
	billingCases,
	closeTo,
	expectedUsage,
	globalProxyForInference,
	globalProxyRestorable,
	pricingCases,
	pricingRelayConfig,
	pricingRequestBody,
	pricingResponseBody,
	resolveUsage,
	ROUTE_VARIANTS,
	routeMatchCases,
	streamBody,
	streamingCases,
	verdict,
	KNOWN_GAPS,
} from "./lib/relay-matrix.mjs";
import { addEchoProvider, addEchoRelay, createEchoUpstream, ECHO_FAIL_MODEL, ECHO_MODEL } from "./lib/echo-upstream.mjs";
import { closeServer, gatewayClient, listen, mapLimit, poll, readBody } from "./lib/gateway-api.mjs";
import { openLogsDb } from "./lib/logs-db.mjs";
import { startHttpProxy, startSocks5Proxy } from "./lib/local-proxies.mjs";

const ALL_SUITES = ["pricing", "streaming", "billing", "routes", "auth", "governance", "settings", "logsapi", "network", "proxy", "hybrid"];

const baseURL = (process.env.BIFROST_E2E_BASE_URL || process.env.BIFROST_BASE_URL || "http://localhost:8080").replace(/\/+$/, "");
const api = gatewayClient({
	baseURL,
	authHeader: process.env.BIFROST_E2E_AUTH_HEADER || "",
	setupToken: (process.env.BIFROST_E2E_SETUP_TOKEN || process.env.BIFROST_SETUP_TOKEN || "bifrost-e2e-setup-token").trim(),
});
const runTag = `rmx${process.pid}${Date.now().toString(36)}`;
const fakeS3URL = (process.env.BIFROST_E2E_FAKE_S3_URL || "").replace(/\/+$/, "");
const objectPrefix = process.env.BIFROST_E2E_OBJECT_STORAGE_PREFIX || "bifrost";
const excludedTypes = new Set((process.env.BIFROST_E2E_OBJECT_STORAGE_EXCLUDE_TYPES || "").split(",").map((s) => s.trim()).filter(Boolean));
const excludedFields = new Set((process.env.BIFROST_E2E_OBJECT_STORAGE_EXCLUDE_FIELDS || "").split(",").map((s) => s.trim()).filter(Boolean));
const largeMB = Number(process.env.RELAY_MATRIX_LARGE_MB || "65");

function selectedSuites() {
	const arg = process.argv.find((a) => a.startsWith("--suites="))?.slice("--suites=".length) ?? process.env.RELAY_MATRIX_SUITES ?? "";
	const picked = arg.split(",").map((s) => s.trim()).filter(Boolean);
	if (picked.length === 0) return ALL_SUITES;
	for (const s of picked) if (!ALL_SUITES.includes(s)) throw new Error(`unknown suite ${s}; have ${ALL_SUITES.join(", ")}`);
	return picked;
}

// --- Reporting -----------------------------------------------------------------------------------

const results = [];

function record(suite, id, failures, gapKey) {
	let kind;
	if (gapKey) kind = verdict(gapKey, failures.length === 0);
	else kind = failures.length === 0 ? "ok" : "FAIL";
	results.push({ suite, id, kind, failures, gapKey });
	const label = kind === "ok" ? "ok   " : kind.padEnd(5);
	const note = kind === "GAP" ? ` (known gap: ${KNOWN_GAPS[gapKey]})` : kind === "STALE" ? ` (listed gap ${gapKey} no longer reproduces; remove it from KNOWN_GAPS)` : "";
	const detail = failures.length && kind !== "GAP" ? ` -> ${failures.join("; ")}` : "";
	console.log(`  ${label} ${suite}/${id}${note}${detail}`);
}

function expectEqual(failures, label, got, want) {
	const g = JSON.stringify(got ?? null);
	const w = JSON.stringify(want ?? null);
	if (g !== w) failures.push(`${label}=${g}, want ${w}`);
}

function expectClose(failures, label, got, want) {
	if (!closeTo(got, want)) failures.push(`${label}=${JSON.stringify(got ?? null)}, want ${JSON.stringify(want ?? null)}`);
}

// --- Cleanup registry ----------------------------------------------------------------------------

const created = { relays: [], keys: [], teams: [], customers: [], providers: [] };

async function cleanup() {
	const errors = [];
	const del = async (what, path) => {
		const res = await api.request("DELETE", path).catch((e) => ({ ok: false, status: 0, text: e.message }));
		if (!res.ok && res.status !== 404) errors.push(`delete ${what}: ${res.status} ${res.text.slice(0, 200)}`);
	};
	await mapLimit(created.keys, 8, (id) => del(`virtual key ${id}`, `/api/governance/virtual-keys/${encodeURIComponent(id)}`));
	await mapLimit(created.teams, 8, (id) => del(`team ${id}`, `/api/governance/teams/${encodeURIComponent(id)}`));
	await mapLimit(created.customers, 8, (id) => del(`customer ${id}`, `/api/governance/customers/${encodeURIComponent(id)}`));
	await mapLimit(created.relays, 8, (name) => del(`relay ${name}`, `/api/relays/${encodeURIComponent(name)}`));
	await mapLimit(created.providers, 4, (name) => del(`provider ${name}`, `/api/providers/${encodeURIComponent(name)}`));
	return errors;
}

// --- Gateway helpers -----------------------------------------------------------------------------

async function createRelay(cfg) {
	await api.mustRequest("POST", "/api/relays", cfg);
	created.relays.push(cfg.name);
}

function entityOf(json, key) {
	return json?.[key] ?? json;
}

async function createCustomer(body) {
	const res = await api.mustRequest("POST", "/api/governance/customers", { ...body, name: `${runTag}-${body.name}` });
	const c = entityOf(res.json, "customer");
	created.customers.push(c.id);
	return c;
}

async function createTeam(body) {
	const res = await api.mustRequest("POST", "/api/governance/teams", { ...body, name: `${runTag}-${body.name}` });
	const t = entityOf(res.json, "team");
	created.teams.push(t.id);
	return t;
}

async function createKey(body) {
	const res = await api.mustRequest("POST", "/api/governance/virtual-keys", { is_active: true, ...body, name: `${runTag}-${body.name}` });
	const vk = entityOf(res.json, "virtual_key");
	if (!vk?.id || !vk?.value) throw new Error(`virtual key create returned no id/value: ${res.text}`);
	created.keys.push(vk.id);
	return vk;
}

// relayRequest calls /relay/<name><path> with a raw body, so the bytes the upstream receives (and
// any request-side quantity) are exactly the ones the case computed.
async function relayRequest(name, method, path, { body, headers = {}, contentType = "application/json" } = {}) {
	const h = { ...headers };
	if (body !== undefined && !Object.keys(h).some((k) => k.toLowerCase() === "content-type")) h["content-type"] = contentType;
	return api.request(method, `/relay/${name}${path}`, body, h, { raw: true });
}

// relayRows lists every relay row this run wrote, keyed by request_id. Rows are written by an
// asynchronous batch writer, so callers poll for the ids they expect.
async function relayRowsByRequestID() {
	const rows = new Map();
	for (let offset = 0; ; offset += 1000) {
		const res = await api.mustRequest("GET", `/api/relay-logs?search=${encodeURIComponent(runTag)}&limit=1000&offset=${offset}`);
		for (const r of res.json?.logs ?? []) if (r.request_id) rows.set(r.request_id, r);
		if ((res.json?.logs ?? []).length < 1000) break;
	}
	return rows;
}

async function waitForRelayRows(ids, timeoutMs = 90000) {
	let rows = new Map();
	try {
		await poll(`${ids.length} relay rows`, timeoutMs, async () => {
			rows = await relayRowsByRequestID();
			return ids.every((id) => rows.has(id));
		}, 1000);
	} catch {
		// Return what arrived; each case reports its own missing row.
	}
	return rows;
}

async function relayRow(requestID, timeoutMs = 60000) {
	const listed = await poll(`relay row ${requestID}`, timeoutMs, async () => {
		const res = await api.mustRequest("GET", `/api/relay-logs?request_id=${encodeURIComponent(requestID)}`);
		return res.json?.logs?.[0] ?? null;
	});
	return (await api.mustRequest("GET", `/api/relay-logs/${encodeURIComponent(listed.id)}`)).json;
}

// relayRowWhere re-reads a relay row until pred holds. In hybrid mode a row is readable before its
// bodies finish uploading, and until then the API serves the database preview (as for LLM rows),
// so a check on the full body waits for the upload.
async function relayRowWhere(requestID, pred, timeoutMs = 60000) {
	let last = null;
	try {
		await poll(`relay row ${requestID} to settle`, timeoutMs, async () => {
			last = await relayRow(requestID, timeoutMs);
			return pred(last);
		}, 1000);
	} catch {
		// Return the last read; the caller reports what it lacks.
	}
	return last ?? relayRow(requestID, timeoutMs);
}

async function llmRow(requestID, timeoutMs = 60000) {
	return poll(`LLM row ${requestID}`, timeoutMs, async () => {
		const res = await api.request("GET", `/api/logs/${encodeURIComponent(requestID)}`);
		if (res.status === 404) return null;
		if (!res.ok) throw new Error(`GET /api/logs/${requestID}: ${res.status} ${res.text}`);
		return res.json;
	});
}

function rid(...parts) {
	return [runTag, ...parts].join("-");
}

// --- Programmable relay upstream -----------------------------------------------------------------

// Each relay a case creates points at /<scenario>/ on this server; the first path segment picks the
// scenario handler, which sees the rest of the path, the query, the headers and the raw body.
const scenarios = new Map();
const seen = new Map(); // scenario -> [{method, path, query, headers, body}]

function scenario(id, handler) {
	scenarios.set(id, handler);
	return id;
}

function upstreamHandler() {
	return async (req, res) => {
		const body = await readBody(req);
		const url = new URL(req.url || "/", "http://upstream");
		const [, id, ...rest] = url.pathname.split("/");
		const handler = scenarios.get(id);
		const view = { method: req.method, path: `/${rest.join("/")}`, query: Object.fromEntries(url.searchParams), headers: req.headers, body };
		if (!seen.has(id)) seen.set(id, []);
		seen.get(id).push(view);
		if (!handler) {
			res.writeHead(404, { "content-type": "application/json" });
			res.end(JSON.stringify({ error: `no scenario ${id}` }));
			return;
		}
		try {
			await handler(view, res, req);
		} catch (err) {
			if (!res.headersSent) res.writeHead(500, { "content-type": "application/json" });
			res.end(JSON.stringify({ error: err.message }));
		}
	};
}

function sendJSON(res, status, obj) {
	const body = typeof obj === "string" ? obj : JSON.stringify(obj);
	res.writeHead(status, { "content-type": "application/json", "content-length": Buffer.byteLength(body) });
	res.end(body);
}

const echoScenario = (view, res) => sendJSON(res, 200, { method: view.method, path: view.path, query: view.query, headers: view.headers, body: view.body.toString("utf8") });
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// --- Suite: pricing ------------------------------------------------------------------------------

async function suitePricing(ctx) {
	const cases = pricingCases();
	console.log(`pricing: ${cases.length} relays x ${Object.keys(ROUTE_VARIANTS).length} routes`);
	for (const c of cases) {
		const body = pricingResponseBody(c);
		scenario(`${runTag}${c.id}`, (view, res) => sendJSON(res, 200, body));
	}
	await mapLimit(cases, 6, (c) => createRelay(pricingRelayConfig(`${runTag}-${c.id}`, `${ctx.upstreamURL}/${runTag}${c.id}`, c)));
	const calls = [];
	for (const c of cases) for (const variant of Object.keys(ROUTE_VARIANTS)) calls.push({ c, variant, requestID: rid(c.id, variant) });
	const sent = await mapLimit(calls, 16, async (call) => {
		const { c, variant, requestID } = call;
		const query = c.modelSource === "query" ? `?model=q-${c.id}` : "";
		const res = await relayRequest(`${runTag}-${c.id}`, "POST", `/${variant}${query}`, { body: pricingRequestBody(`m-${c.id}`), headers: { "x-request-id": requestID } });
		return { ...call, status: res.status, text: res.text };
	});
	const rows = await waitForRelayRows(calls.map((c) => c.requestID));
	for (const s of sent) {
		const { c, variant, requestID } = s;
		const failures = [];
		if (s.status !== 200) failures.push(`caller got ${s.status}: ${s.text.slice(0, 200)}`);
		const row = rows.get(requestID);
		if (!row) {
			record("pricing", `${c.id}/${variant}`, [...failures, "no relay row"]);
			continue;
		}
		const usage = resolveUsage(c.usage, ROUTE_VARIANTS[variant]);
		const exp = expectedUsage({ usage, billable: true, status: 200, requestBody: pricingRequestBody(`m-${c.id}`), responseBody: pricingResponseBody(c), ignoreUpstreamCost: c.ignore });
		expectClose(failures, "cost", row.cost, exp.cost);
		expectEqual(failures, "units", row.units ?? null, exp.units);
		expectEqual(failures, "billed_units", row.billed_units ?? null, exp.billedUnits);
		expectEqual(failures, "extracted_cost", row.extracted_cost ?? null, exp.extractedCost);
		expectEqual(failures, "tokens", row.total_tokens ? { input: row.input_tokens ?? 0, output: row.output_tokens ?? 0, total: row.total_tokens } : null, exp.tokens);
		expectEqual(failures, "model", row.model ?? null, c.modelSource === "query" ? `q-${c.id}` : `m-${c.id}`);
		expectEqual(failures, "route_path", row.route_path, `/${variant}`);
		expectEqual(failures, "billable", row.billable, true);
		expectEqual(failures, "streamed", row.streamed, false);
		expectEqual(failures, "status", row.status, "success");
		expectClose(failures, "cost_per_request", row.cost_per_request ?? 0, usage.cost_per_request);
		expectClose(failures, "cost_per_unit", row.cost_per_unit ?? 0, usage.cost_per_unit);
		const describe = `${c.id}/${variant} ${c.source}.${c.measure} rate=${c.perUnit} fee=${c.perRequest ?? "-"} block=${c.block ?? "-"} min=${c.minimum ?? "-"} cost=${c.costShape}${c.ignore ? "(ignored)" : ""}`;
		record("pricing", describe, failures);
	}
}

// --- Suite: streaming ----------------------------------------------------------------------------

async function suiteStreaming(ctx) {
	const cases = streamingCases();
	for (const c of cases) {
		const { contentType, body } = streamBody(c.kind);
		scenario(`${runTag}${c.id}`, async (view, res) => {
			res.writeHead(200, { "content-type": contentType });
			if (c.kind === "sse") {
				// One event per write, so a streamed relay sees them arrive separately.
				for (const event of body.toString("utf8").split(/(?<=\n\n)/)) {
					res.write(event);
					await sleep(20);
				}
				res.end();
				return;
			}
			res.end(body);
		});
		const usage = { cost_per_request: 0.001, cost_per_unit: 0.01, cost_path: "usage.cost" };
		if (c.measureName === "value") usage.units_path = "usage.n";
		else usage.units_measure = "body_bytes";
		const route = { method: "POST", path: "/stream" };
		if (c.flag !== "unset") route.stream = c.flag === "on";
		await createRelay({
			name: `${runTag}-${c.id}`,
			base_url: `${ctx.upstreamURL}/${runTag}${c.id}`,
			auth: { type: "none" },
			usage,
			streaming: { paths: c.pathListed ? ["/stream"] : [] },
			routes: [route],
		});
	}
	const sent = await mapLimit(cases, 8, async (c) => {
		const requestID = rid(c.id);
		const res = await relayRequest(`${runTag}-${c.id}`, "POST", "/stream", { body: "{}", headers: { "x-request-id": requestID } });
		return { c, requestID, res };
	});
	const rows = await waitForRelayRows(sent.map((s) => s.requestID));
	for (const { c, requestID, res } of sent) {
		const failures = [];
		const { body } = streamBody(c.kind);
		if (res.status !== 200) failures.push(`caller got ${res.status}`);
		if (!res.buf.equals(body)) failures.push(`caller got ${res.buf.length} bytes that differ from the upstream's ${body.length}`);
		const row = rows.get(requestID);
		if (!row) {
			record("streaming", c.id, [...failures, "no relay row"]);
			continue;
		}
		expectEqual(failures, "streamed", row.streamed, c.streamed);
		expectClose(failures, "cost", row.cost, c.expected.cost);
		expectEqual(failures, "units", row.units ?? null, c.expected.units);
		expectEqual(failures, "extracted_cost", row.extracted_cost ?? null, c.expected.extractedCost);
		expectEqual(failures, "bytes_out", row.bytes_out, body.length);
		record("streaming", c.id, failures);
	}
}

// --- Suite: billing ------------------------------------------------------------------------------

async function suiteBilling(ctx) {
	const id = scenario(`${runTag}billing`, (view, res) => {
		const status = Number(view.query.status || "200");
		if (view.method === "HEAD") {
			res.writeHead(status, { "content-type": "application/json" });
			res.end();
			return;
		}
		sendJSON(res, status, { status });
	});
	const relays = new Map();
	for (const c of billingCases()) {
		const key = `${c.method}-${c.flag}`;
		if (relays.has(key)) continue;
		const route = { method: c.method, path: "/b" };
		if (c.flag !== "unset") route.billable = c.flag === "true";
		const name = `${runTag}-bill-${key}`.toLowerCase();
		relays.set(key, name);
		await createRelay({ name, base_url: `${ctx.upstreamURL}/${id}`, auth: { type: "none" }, usage: { cost_per_request: 0.001 }, routes: [route] });
	}
	const sent = await mapLimit(billingCases(), 12, async (c) => {
		const requestID = rid(c.id);
		const res = await relayRequest(relays.get(`${c.method}-${c.flag}`), c.method, `/b?status=${c.status}`, {
			body: c.method === "GET" || c.method === "HEAD" ? undefined : "{}",
			headers: { "x-request-id": requestID },
		});
		return { c, requestID, res };
	});
	const rows = await waitForRelayRows(sent.map((s) => s.requestID));
	for (const { c, requestID, res } of sent) {
		const failures = [];
		expectEqual(failures, "caller status", res.status, c.status);
		const row = rows.get(requestID);
		if (!row) {
			record("billing", c.id, [...failures, "no relay row"]);
			continue;
		}
		expectEqual(failures, "status", row.status, c.rowStatus);
		expectEqual(failures, "status_code", row.status_code, c.status);
		expectEqual(failures, "billable", row.billable, c.billable);
		expectEqual(failures, "charged", Boolean(row.cost), c.counted);
		if (c.counted) expectClose(failures, "cost", row.cost, 0.001);
		record("billing", c.id, failures);
	}
}

// --- Suite: routes -------------------------------------------------------------------------------

async function suiteRoutes(ctx) {
	const id = scenario(`${runTag}routes`, echoScenario);
	const sent = [];
	for (const c of routeMatchCases()) {
		const name = `${runTag}-rt-${c.id}`;
		const cfg = { name, base_url: `${ctx.upstreamURL}/${id}`, auth: { type: "none" }, usage: { cost_per_request: 0.001 }, routes: c.routes, skip_route_check: Boolean(c.skipRouteCheck) };
		if (c.extraHeaders) cfg.extra_headers = c.extraHeaders;
		await createRelay(cfg);
		const requestID = rid("rt", c.id);
		const res = await relayRequest(name, c.method, c.path, { body: c.method === "GET" ? undefined : "{}", headers: { "x-request-id": requestID, ...(c.headers ?? {}) } });
		sent.push({ c, requestID, res });
	}
	const relayed = sent.filter((s) => s.c.want.status === 200);
	const rows = await waitForRelayRows(relayed.map((s) => s.requestID));
	// A refused call is answered before the hooks and writes no row; by now every relayed row has
	// landed, so a refused one would have too.
	await sleep(1500);
	const after = await relayRowsByRequestID();
	for (const { c, requestID, res } of sent) {
		const failures = [];
		expectEqual(failures, "caller status", res.status, c.want.status);
		if (c.want.status !== 200) {
			if (after.has(requestID)) failures.push("a refused call wrote a relay row");
			record("routes", c.id, failures);
			continue;
		}
		const row = rows.get(requestID);
		if (!row) {
			record("routes", c.id, [...failures, "no relay row"]);
			continue;
		}
		expectEqual(failures, "route_path", row.route_path ?? "", c.want.route);
		if ("cost" in c.want) expectEqual(failures, "cost", row.cost ? Number(row.cost.toFixed(6)) : null, c.want.cost);
		// Matched header names are stored in their canonical lower-case form.
		if (c.want.matched) expectEqual(failures, "request_headers", row.request_headers, c.want.matched);
		record("routes", c.id, failures);
	}
}

// --- Suite: auth and forwarding ------------------------------------------------------------------

async function suiteAuth(ctx) {
	const id = scenario(`${runTag}auth`, echoScenario);
	const upstreamSecret = `up-${randomBytes(6).toString("hex")}`;
	const callerSecret = `caller-${randomBytes(6).toString("hex")}`;
	const variants = [
		{ id: "bearer", auth: { type: "bearer" }, credential: { value: upstreamSecret }, check: (h) => (h.authorization === `Bearer ${upstreamSecret}` ? null : `authorization=${h.authorization}`) },
		{
			id: "header",
			auth: { type: "header", header_name: "X-Api-Token", value_prefix: "Tok " },
			credential: { value: upstreamSecret },
			check: (h) => (h["x-api-token"] === `Tok ${upstreamSecret}` && !h.authorization ? null : `x-api-token=${h["x-api-token"]} authorization=${h.authorization}`),
		},
		{
			id: "query",
			auth: { type: "query", query_param: "key" },
			credential: { value: upstreamSecret },
			query: `?key=${callerSecret}&keep=1`,
			check: (h, q) => (q.key === upstreamSecret && q.keep === "1" && !h.authorization ? null : `query=${JSON.stringify(q)}`),
		},
		{
			id: "basic",
			auth: { type: "basic" },
			credential: { value: "relay-user:relay-pass" },
			check: (h) => (h.authorization === `Basic ${Buffer.from("relay-user:relay-pass").toString("base64")}` ? null : `authorization=${h.authorization}`),
		},
		{ id: "none", auth: { type: "none" }, check: (h) => (!h.authorization ? null : `authorization=${h.authorization}`) },
		{
			id: "aws_sigv4",
			auth: { type: "aws_sigv4", aws: { service: "textract" } },
			credential: { aws: { access_key: "AKIDRELAYMATRIX", secret_key: "relay-matrix-secret-key", region: "us-east-1" } },
			callerHeaders: { "x-amz-date": "19700101T000000Z", "x-amz-target": "Textract.AnalyzeDocument" },
			check: (h) => {
				const auth = String(h.authorization || "");
				if (!auth.startsWith("AWS4-HMAC-SHA256 Credential=AKIDRELAYMATRIX/") || !auth.includes("/us-east-1/textract/aws4_request")) return `authorization=${auth}`;
				if (!h["x-amz-date"] || h["x-amz-date"] === "19700101T000000Z") return `x-amz-date=${h["x-amz-date"]} (the caller's must be replaced)`;
				if (h["x-amz-target"] !== "Textract.AnalyzeDocument") return "x-amz-target was not forwarded";
				return null;
			},
		},
		{
			id: "extra-headers",
			auth: { type: "none" },
			// As on a provider passthrough call: the operator's header is sent when the caller sends
			// none (x-tenant), and the caller's own header replaces it (x-custom).
			extraHeaders: { "X-Custom": "operator", "X-Tenant": "acme" },
			check: (h) => (h["x-custom"] === "caller" && h["x-tenant"] === "acme" ? null : `x-custom=${h["x-custom"]} x-tenant=${h["x-tenant"]}`),
		},
	];
	for (const v of variants) {
		const name = `${runTag}-auth-${v.id}`.replace(/_/g, "-");
		const cfg = { name, base_url: `${ctx.upstreamURL}/${id}`, auth: v.auth, routes: [{ method: "POST", path: "/**" }] };
		if (v.credential) cfg.credential = v.credential;
		if (v.extraHeaders) cfg.extra_headers = v.extraHeaders;
		await createRelay(cfg);
		const requestID = rid("auth", v.id);
		const res = await relayRequest(name, "POST", `/echo${v.query ?? ""}`, {
			body: "{}",
			headers: {
				"x-request-id": requestID,
				authorization: `Bearer ${callerSecret}`,
				"x-api-key": callerSecret,
				cookie: `session=${callerSecret}`,
				"x-bf-relay-matrix": "1",
				"x-custom": "caller",
				"x-forwarded-app": "kept",
				...(v.callerHeaders ?? {}),
			},
		});
		const failures = [];
		if (res.status !== 200) {
			record("auth", v.id, [`caller got ${res.status}: ${res.text.slice(0, 200)}`]);
			continue;
		}
		const h = res.json.headers;
		const problem = v.check(h, res.json.query);
		if (problem) failures.push(problem);
		if (h["x-api-key"] || h.cookie) failures.push("a caller credential header reached the upstream");
		if (h["x-bf-relay-matrix"]) failures.push("an x-bf-* header reached the upstream");
		if (h["x-forwarded-app"] !== "kept") failures.push("an ordinary caller header was not forwarded");
		if (JSON.stringify(res.json).includes(callerSecret)) failures.push("the caller's secret reached the upstream");
		const row = await relayRow(requestID);
		// The echo upstream answers with the headers it received, so its reply (the logged response
		// body) carries the relay credential by the upstream's own choice. Everything else Bifrost
		// writes on the row must not.
		const rowText = JSON.stringify({ ...row, response_body: undefined });
		if (rowText.includes(upstreamSecret)) failures.push("the relay credential is on the log row");
		if (rowText.includes(callerSecret)) failures.push("the caller's secret is on the log row");
		if (v.id === "query" && !String(row.path).includes("keep=1")) failures.push(`row path ${row.path} lost the ordinary query`);
		// The management API never returns the credential.
		const stored = await api.mustRequest("GET", `/api/relays/${encodeURIComponent(name)}`);
		if (stored.text.includes(upstreamSecret) || stored.text.includes("relay-matrix-secret-key")) failures.push("GET /api/relays/{name} returned the credential");
		record("auth", v.id, failures);
	}
}

// --- Shared LLM + relay fixtures -----------------------------------------------------------------

// ensureEchoPair returns an LLM provider and a relay that both answer from the echo upstream. A
// suite that counts rows asks for a pair of its own (label), so other suites' traffic stays out.
async function ensureEchoPair(ctx, label = "echo") {
	ctx.echoPairs ??= {};
	if (ctx.echoPairs[label]) return ctx.echoPairs[label];
	const provider = `${runTag}-${label}`;
	const relay = `${runTag}-${label}-relay`;
	await addEchoProvider(api, provider, ctx.echoURL);
	created.providers.push(provider);
	await addEchoRelay(api, relay, ctx.echoURL);
	created.relays.push(relay);
	ctx.echoPairs[label] = { provider, relay, model: `${provider}/${ECHO_MODEL}` };
	return ctx.echoPairs[label];
}

function pairKeyBody(echo, extra = {}) {
	return {
		provider_configs: [{ provider: echo.provider, weight: 1, allowed_models: ["*"], key_ids: ["*"] }],
		relay_grants: [{ relay_name: echo.relay }],
		...extra,
	};
}

async function llmCall(echo, requestID, vkValue, headers = {}, content = "prompt-none") {
	const h = { "x-request-id": requestID, ...headers };
	if (vkValue) h["x-bf-vk"] = vkValue;
	return api.request("POST", "/v1/chat/completions", { model: echo.model, messages: [{ role: "user", content }] }, h);
}

async function echoRelayCall(echo, requestID, vkValue, headers = {}, query = "", prompt = "prompt-none") {
	const h = { "x-request-id": requestID, ...headers };
	if (vkValue) h["x-bf-vk"] = vkValue;
	return relayRequest(echo.relay, "POST", `/call${query}`, { body: JSON.stringify({ prompt }), headers: h });
}

// --- Suite: governance ---------------------------------------------------------------------------

async function suiteGovernance(ctx) {
	const echo = await ensureEchoPair(ctx);
	const snapshot = await api.configSnapshot();

	// Attribution: the same key on both kinds of traffic stamps the same entities on both rows.
	const customer = await createCustomer({ name: "cust" });
	const team = await createTeam({ name: "team", customer_id: customer.id });
	const keys = {
		plain: await createKey(pairKeyBody(echo, { name: "plain" })),
		team: await createKey(pairKeyBody(echo, { name: "team", team_id: team.id })),
		customer: await createKey(pairKeyBody(echo, { name: "customer", customer_id: customer.id })),
	};
	const attributionFields = ["virtual_key_id", "virtual_key_name", "team_id", "team_name", "customer_id", "customer_name", "team_ids", "customer_ids", "budget_ids", "rate_limit_ids"];
	for (const [label, vk] of Object.entries(keys)) {
		const lid = rid("gov", "attr", label, "llm");
		const relayID = rid("gov", "attr", label, "relay");
		const a = await llmCall(echo, lid, vk.value);
		const b = await echoRelayCall(echo, relayID, vk.value);
		const failures = [];
		if (a.status !== 200) failures.push(`LLM call got ${a.status}: ${a.text.slice(0, 200)}`);
		if (b.status !== 200) failures.push(`relay call got ${b.status}: ${b.text.slice(0, 200)}`);
		if (failures.length === 0) {
			const llm = await llmRow(lid);
			const relay = await relayRow(relayID);
			for (const f of attributionFields) {
				const l = JSON.stringify(llm[f] ?? null);
				const r = JSON.stringify(relay[f] ?? null);
				if (l !== r) failures.push(`${f}: LLM ${l} vs relay ${r}`);
			}
			expectEqual(failures, "virtual_key_id", relay.virtual_key_id, vk.id);
			if (label === "team") expectEqual(failures, "team_id", relay.team_id, team.id);
			if (label !== "plain") expectEqual(failures, "customer_id", relay.customer_id, customer.id);
		}
		record("governance", `attribution-${label}`, failures);
	}

	// Refusals before any upstream effect.
	const ungranted = await createKey({ name: "ungranted", provider_configs: [{ provider: echo.provider, weight: 1, allowed_models: ["*"], key_ids: ["*"] }] });
	const inactive = await createKey(pairKeyBody(echo, { name: "inactive", is_active: false }));
	const refusals = [
		{ id: "ungranted-relay", vk: ungranted.value, relay: 403, llm: 200, relayType: "relay_blocked" },
		{ id: "inactive", vk: inactive.value, relay: 403, llm: 403 },
		{ id: "unknown-key", vk: "sk-bf-relay-matrix-unknown", relay: 401, llm: 401 },
	];
	for (const r of refusals) {
		const a = await llmCall(echo, rid("gov", r.id, "llm"), r.vk);
		const b = await echoRelayCall(echo, rid("gov", r.id, "relay"), r.vk);
		const failures = [];
		expectEqual(failures, "LLM status", a.status, r.llm);
		expectEqual(failures, "relay status", b.status, r.relay);
		if (r.relayType) expectEqual(failures, "relay error type", b.json?.type, r.relayType);
		record("governance", r.id, failures);
	}

	// Inference auth: with enforcement on, a call with no key is refused on both kinds of traffic;
	// with it off, both go through.
	for (const enforce of [true, false]) {
		await api.putClientConfig(snapshot, { enforce_auth_on_inference: enforce });
		const want = enforce ? 401 : 200;
		await poll(`enforce_auth_on_inference=${enforce} to take effect`, 15000, async () => (await echoRelayCall(echo, rid("gov", "probe", String(enforce), randomBytes(3).toString("hex")), "")).status === want);
		const a = await llmCall(echo, rid("gov", "noauth", String(enforce), "llm"), "");
		const b = await echoRelayCall(echo, rid("gov", "noauth", String(enforce), "relay"), "");
		const failures = [];
		expectEqual(failures, "LLM status", a.status, want);
		expectEqual(failures, "relay status", b.status, want);
		record("governance", `no-key-enforce-${enforce}`, failures);
	}
	await api.putClientConfig(snapshot, {});

	// Budgets at each level: the relay fee is 0.001 a call. A budget is checked before a call and
	// charged after it, so a 0.0025 budget admits three calls and refuses the fourth; a 0.0015
	// budget admits two.
	const budgetCustomer = await createCustomer({ name: "budget-cust", budgets: [{ max_limit: 0.0015, reset_duration: "1h" }] });
	const budgetTeam = await createTeam({ name: "budget-team", budgets: [{ max_limit: 0.0015, reset_duration: "1h" }] });
	const budgetCases = [
		{ id: "budget-key", vk: await createKey(pairKeyBody(echo, { name: "budget-key", budgets: [{ max_limit: 0.0025, reset_duration: "1h" }] })), want: [200, 200, 200, 402] },
		{ id: "budget-team", vk: await createKey(pairKeyBody(echo, { name: "budget-team-key", team_id: budgetTeam.id })), want: [200, 200, 402] },
		{ id: "budget-customer", vk: await createKey(pairKeyBody(echo, { name: "budget-cust-key", customer_id: budgetCustomer.id })), want: [200, 200, 402] },
	];
	for (const bc of budgetCases) {
		const got = [];
		for (let i = 0; i < bc.want.length; i++) {
			const res = await echoRelayCall(echo, rid("gov", bc.id, String(i)), bc.vk.value);
			got.push(res.status);
			await sleep(300);
		}
		const failures = [];
		expectEqual(failures, "statuses", got, bc.want);
		const refused = await relayRow(rid("gov", bc.id, String(bc.want.length - 1))).catch(() => null);
		if (!refused) failures.push("the refused call wrote no relay row");
		else {
			expectEqual(failures, "refused row status", refused.status, "error");
			if (refused.cost) failures.push(`the refused call was charged ${refused.cost}`);
		}
		record("governance", bc.id, failures);
	}

	// Request limits at each level, and the counter LLM and relay traffic share.
	const rlCustomer = await createCustomer({ name: "rl-cust", rate_limit: { request_max_limit: 2, request_reset_duration: "1h" } });
	const rlTeam = await createTeam({ name: "rl-team", rate_limit: { request_max_limit: 2, request_reset_duration: "1h" } });
	const rl = (extra) => ({ rate_limit: { request_max_limit: 2, request_reset_duration: "1h" }, ...extra });
	const limitCases = [
		{ id: "limit-key", vk: await createKey(pairKeyBody(echo, rl({ name: "rl-key" }))), seq: ["relay", "relay", "relay"], want: [200, 200, 429] },
		{ id: "limit-team", vk: await createKey(pairKeyBody(echo, { name: "rl-team-key", team_id: rlTeam.id })), seq: ["relay", "relay", "relay"], want: [200, 200, 429] },
		{ id: "limit-customer", vk: await createKey(pairKeyBody(echo, { name: "rl-cust-key", customer_id: rlCustomer.id })), seq: ["relay", "relay", "relay"], want: [200, 200, 429] },
		{ id: "limit-shared-llm-relay", vk: await createKey(pairKeyBody(echo, rl({ name: "rl-shared" }))), seq: ["llm", "relay", "llm", "relay"], want: [200, 200, 429, 429] },
	];
	for (const lc of limitCases) {
		const got = [];
		for (let i = 0; i < lc.seq.length; i++) {
			const id = rid("gov", lc.id, String(i));
			const res = lc.seq[i] === "llm" ? await llmCall(echo, id, lc.vk.value) : await echoRelayCall(echo, id, lc.vk.value);
			got.push(res.status);
			await sleep(200);
		}
		const failures = [];
		expectEqual(failures, "statuses", got, lc.want);
		record("governance", lc.id, failures);
	}

	// What counts against a request limit: any reply (a non-billable GET, a 500) counts; a call that
	// never got a reply (unreachable upstream) does not.
	const deadPort = await (async () => {
		const s = http.createServer();
		const p = await listen(s);
		await closeServer(s);
		return p;
	})();
	const failRelay = `${runTag}-gov-dead`;
	await createRelay({ name: failRelay, base_url: `http://127.0.0.1:${deadPort}`, auth: { type: "none" }, usage: { cost_per_request: 0.001 }, routes: [{ method: "POST", path: "/**" }] });
	const freeID = scenario(`${runTag}gov-free`, (view, res) => sendJSON(res, view.query.fail ? 500 : 200, { ok: true }));
	const freeRelay = `${runTag}-gov-free`;
	await createRelay({ name: freeRelay, base_url: `${ctx.upstreamURL}/${freeID}`, auth: { type: "none" }, usage: { cost_per_request: 0.001 }, routes: [{ method: "GET", path: "/**" }, { method: "POST", path: "/**" }] });
	const countVK = await createKey({ name: "rl-count", rate_limit: { request_max_limit: 3, request_reset_duration: "1h" }, relay_grants: [{ relay_name: failRelay }, { relay_name: freeRelay }] });
	const countSeq = [
		{ what: "unreachable (not counted)", call: () => relayRequest(failRelay, "POST", "/x", { body: "{}", headers: { "x-bf-vk": countVK.value } }), want: 502 },
		{ what: "non-billable GET (counted)", call: () => relayRequest(freeRelay, "GET", "/x", { headers: { "x-bf-vk": countVK.value } }), want: 200 },
		{ what: "upstream 500 (counted)", call: () => relayRequest(freeRelay, "POST", "/x?fail=1", { body: "{}", headers: { "x-bf-vk": countVK.value } }), want: 500 },
		{ what: "billable POST (counted)", call: () => relayRequest(freeRelay, "POST", "/x", { body: "{}", headers: { "x-bf-vk": countVK.value } }), want: 200 },
		{ what: "over the limit", call: () => relayRequest(freeRelay, "POST", "/x", { body: "{}", headers: { "x-bf-vk": countVK.value } }), want: 429 },
	];
	const countFailures = [];
	for (const step of countSeq) {
		const res = await step.call();
		const ok = step.want === 502 ? res.status >= 500 : res.status === step.want;
		if (!ok) countFailures.push(`${step.what}: got ${res.status}, want ${step.want === 502 ? "a 5xx transport error" : step.want}`);
		await sleep(200);
	}
	record("governance", "limit-counts-replies-not-failures", countFailures);

	// Budget usage on the key equals the sum of its rows' costs.
	const usageVK = await createKey(pairKeyBody(echo, { name: "usage", budgets: [{ max_limit: 100, reset_duration: "1h" }] }));
	for (let i = 0; i < 3; i++) await echoRelayCall(echo, rid("gov", "usage", String(i)), usageVK.value);
	// Usage is charged in memory at once and written to the database every 10s (the governance
	// tracker's dump), which is what the management API reads.
	const usageFailures = [];
	let spent = 0;
	await poll("budget usage to reach the database", 35000, async () => {
		const vkNow = (await api.mustRequest("GET", `/api/governance/virtual-keys/${usageVK.id}`)).json;
		spent = (entityOf(vkNow, "virtual_key").budgets ?? []).map((b) => b.current_usage ?? 0).reduce((a, b) => a + b, 0);
		return closeTo(spent, 0.003);
	}, 1000).catch(() => {});
	expectClose(usageFailures, "budget current_usage", spent, 0.003);
	record("governance", "budget-usage-equals-row-costs", usageFailures);
}

// --- Suite: logging settings (LLM vs relay) ------------------------------------------------------

async function suiteSettings(ctx) {
	const echo = await ensureEchoPair(ctx);
	const snapshot = await api.configSnapshot();
	const vk = await createKey(pairKeyBody(echo, { name: "settings", metadata: { cost_center: "cc-42", owner: "relay-matrix" } }));
	try {
		await api.putClientConfig(snapshot, { logging_headers: ["x-tenant", "x-trace-*"] });
		await sleep(500);
		const sessionID = `sess-${runTag}`;
		const headers = {
			"x-tenant": "acme",
			"x-trace-id": "trace-1",
			"x-bf-lh-env": "prod",
			"x-bf-dim-region": "eu-west",
			"x-bf-session-id": sessionID,
			"user-agent": "claude-cli/2.1.0 (external, cli)",
		};
		const lid = rid("set", "llm");
		const relayID = rid("set", "relay");
		const a = await llmCall(echo, lid, vk.value, headers);
		const b = await echoRelayCall(echo, relayID, vk.value, headers);
		if (a.status !== 200 || b.status !== 200) {
			record("settings", "metadata-parity", [`calls got LLM ${a.status} relay ${b.status}`]);
		} else {
			const llm = await llmRow(lid);
			const relay = await relayRow(relayID);
			for (const [field, label] of [
				["session_id", "session id"],
				["user_agent", "user agent"],
				["app", "detected app"],
			]) {
				const failures = [];
				expectEqual(failures, `relay ${field} vs LLM`, relay[field] ?? null, llm[field] ?? null);
				if (field === "session_id") expectEqual(failures, "relay session_id", relay.session_id, sessionID);
				if (field === "app") expectEqual(failures, "relay app", relay.app, "Claude Code");
				record("settings", label, failures);
			}
			for (const [key, label] of [
				["x-tenant", "logging_headers exact name"],
				["x-trace-id", "logging_headers wildcard"],
				["env", "x-bf-lh-* label"],
				["region", "x-bf-dim-* dimension"],
				["cost_center", "virtual key metadata"],
			]) {
				const failures = [];
				const l = llm.metadata?.[key];
				const r = relay.metadata?.[key];
				if (l === undefined) failures.push(`LLM metadata lacks ${key}: ${JSON.stringify(llm.metadata)}`);
				if (JSON.stringify(r) !== JSON.stringify(l)) failures.push(`relay metadata ${key}=${JSON.stringify(r)}, LLM ${JSON.stringify(l)}`);
				record("settings", label, failures);
			}
		}

		// A harness session header (not x-bf-*) also names the session, on both kinds of rows.
		{
			const lidH = rid("set", "harness", "llm");
			const ridH = rid("set", "harness", "relay");
			await llmCall(echo, lidH, vk.value, { "x-session-id": `h-${runTag}` });
			await echoRelayCall(echo, ridH, vk.value, { "x-session-id": `h-${runTag}` });
			const failures = [];
			const llm = await llmRow(lidH);
			const relay = await relayRow(ridH);
			expectEqual(failures, "relay session_id vs LLM", relay.session_id ?? null, llm.session_id ?? null);
			record("settings", "harness session header", failures);
		}

		// Query strings that carry credentials are redacted on the row even with content on.
		{
			const id = rid("set", "redact");
			await echoRelayCall(echo, id, vk.value, {}, "?api_key=s3cr3t-relay-matrix&token=tok-relay-matrix&q=kept");
			const row = await relayRow(id);
			const failures = [];
			if (String(row.path).includes("s3cr3t-relay-matrix") || String(row.path).includes("tok-relay-matrix")) failures.push(`row path ${row.path} kept a credential`);
			if (!String(row.path).includes("q=kept")) failures.push(`row path ${row.path} lost an ordinary parameter`);
			record("settings", "query credential redaction", failures);
		}

		// hide_deleted_virtual_keys_in_filters: a deleted key is dropped from both filter lists when on,
		// and labelled in both when off.
		{
			const doomed = await createKey(pairKeyBody(echo, { name: "doomed" }));
			await llmCall(echo, rid("set", "doomed", "llm"), doomed.value);
			await echoRelayCall(echo, rid("set", "doomed", "relay"), doomed.value);
			await llmRow(rid("set", "doomed", "llm"));
			await relayRow(rid("set", "doomed", "relay"));
			const find = (fd) => (fd?.virtual_keys ?? []).find((k) => k.id === doomed.id) ?? null;
			const filterData = async () => ({
				llm: find((await api.mustRequest("GET", "/api/logs/filterdata?dimensions=virtual_keys")).json),
				relay: find((await api.mustRequest("GET", "/api/relay-logs/filterdata?dimensions=virtual_keys")).json),
			});
			// LLM filter data on Postgres is a materialized view refreshed about once a minute and
			// cached for 30s, so the key is listed only once both have caught up.
			await poll("both filter lists to show the key", 150000, async () => {
				const fd = await filterData();
				return fd.llm && fd.relay;
			}, 2000).catch(() => {});
			await api.mustRequest("DELETE", `/api/governance/virtual-keys/${doomed.id}`);
			created.keys = created.keys.filter((k) => k !== doomed.id);
			for (const hide of [true, false]) {
				await api.putClientConfig(snapshot, { logging_headers: ["x-tenant", "x-trace-*"], hide_deleted_virtual_keys_in_filters: hide });
				const check = (fd) => {
					const failures = [];
					if (hide) {
						if (fd.llm) failures.push("LLM filter data still lists the deleted key");
						if (fd.relay) failures.push("relay filter data still lists the deleted key");
					} else {
						if (!fd.llm) failures.push("LLM filter data dropped the deleted key");
						if (!fd.relay) failures.push("relay filter data dropped the deleted key");
						if (fd.llm && fd.relay && fd.llm.name !== fd.relay.name) failures.push(`labels differ: LLM ${JSON.stringify(fd.llm.name)} vs relay ${JSON.stringify(fd.relay.name)}`);
					}
					return failures;
				};
				// The LLM list is cached for 30s, so a stale answer is retried until it expires.
				let failures = [];
				await poll(`filter data with hide_deleted_virtual_keys_in_filters=${hide}`, 45000, async () => {
					failures = check(await filterData());
					return failures.length === 0;
				}, 2000).catch(() => {});
				record("settings", `hide_deleted_virtual_keys_in_filters=${hide}`, failures);
			}
		}
	} finally {
		await api.putClientConfig(snapshot, {});
	}
}

// --- Suite: logs API (LLM vs relay) --------------------------------------------------------------

async function suiteLogsAPI(ctx) {
	const echo = await ensureEchoPair(ctx, "api");
	const customer = await createCustomer({ name: "api-cust" });
	const team = await createTeam({ name: "api-team" });
	const v1 = await createKey(pairKeyBody(echo, { name: "api-v1", team_id: team.id }));
	const v2 = await createKey(pairKeyBody(echo, { name: "api-v2", customer_id: customer.id }));
	const failID = scenario(`${runTag}api-fail`, (view, res) => sendJSON(res, 500, { error: "boom" }));
	const failRelay = `${runTag}-api-fail`;
	await createRelay({ name: failRelay, base_url: `${ctx.upstreamURL}/${failID}`, auth: { type: "none" }, routes: [{ method: "POST", path: "/**" }] });
	await api.mustRequest("PUT", `/api/governance/virtual-keys/${v1.id}`, { relay_grants: [{ relay_name: echo.relay }, { relay_name: failRelay }] });

	// Each logical call is made on both kinds of traffic: id suffix -llm / -relay.
	const plan = [
		{ id: "a", vk: v1, session: `s1-${runTag}` },
		{ id: "b", vk: v1, session: `s1-${runTag}` },
		{ id: "c", vk: v2, session: `s2-${runTag}` },
	];
	// A user agent and a metadata header on the planned calls (not on the errors), so the user_agents
	// and metadata filters have rows to select.
	const userAgent = `relay-matrix/${runTag}`;
	for (const p of plan) {
		const headers = { "x-bf-session-id": p.session, "user-agent": userAgent, "x-bf-lh-env": "prod" };
		await llmCall(echo, rid("api", p.id, "llm"), p.vk.value, headers);
		await echoRelayCall(echo, rid("api", p.id, "relay"), p.vk.value, headers);
	}
	// One error on each side.
	await api.request("POST", "/v1/chat/completions", { model: `${echo.provider}/${ECHO_FAIL_MODEL}`, messages: [{ role: "user", content: "x" }] }, { "x-request-id": rid("api", "err", "llm"), "x-bf-vk": v1.value });
	await relayRequest(failRelay, "POST", "/x", { body: "{}", headers: { "x-request-id": rid("api", "err", "relay"), "x-bf-vk": v1.value } });
	for (const p of plan) {
		await llmRow(rid("api", p.id, "llm"));
		await relayRow(rid("api", p.id, "relay"));
	}
	await relayRow(rid("api", "err", "relay"));
	const llmErr = await llmRow(rid("api", "err", "llm")).catch(() => null);

	const llmScope = `providers=${encodeURIComponent(echo.provider)}`;
	const relayScope = `relay_names=${encodeURIComponent(echo.relay)}&relay_names=${encodeURIComponent(failRelay)}`;
	const runIDs = (rows, kind) => rows.map((r) => r.request_id ?? r.id).filter((id) => id && id.startsWith(rid("api")) && id.endsWith(`-${kind}`)).map((id) => id.split("-").slice(-2, -1)[0]).sort();
	const query = async (kind, params) => {
		const base = kind === "llm" ? `/api/logs?${llmScope}` : `/api/relay-logs?${relayScope}`;
		const res = await api.mustRequest("GET", `${base}&limit=1000&${params}`);
		return runIDs(res.json?.logs ?? [], kind);
	};
	const filters = [
		{ id: "virtual_key_ids", llm: `virtual_key_ids=${v2.id}`, relay: `virtual_key_ids=${v2.id}`, want: ["c"] },
		{ id: "team_ids", llm: `team_ids=${team.id}`, relay: `team_ids=${team.id}`, want: ["a", "b", "err"] },
		{ id: "customer_ids", llm: `customer_ids=${customer.id}`, relay: `customer_ids=${customer.id}`, want: ["c"] },
		{ id: "session_id", llm: `session_id=s1-${runTag}`, relay: `session_id=s1-${runTag}`, want: ["a", "b"] },
		{ id: "status=error", llm: "status=error", relay: "status=error", want: ["err"] },
		{ id: "status=success", llm: "status=success", relay: "status=success", want: ["a", "b", "c"] },
		{ id: "period=1h", llm: "period=1h", relay: "period=1h", want: ["a", "b", "c", "err"] },
		{ id: "app", llm: "apps=Other", relay: "apps=Other", want: ["a", "b", "c", "err"] },
		{ id: "min_latency", llm: "min_latency=0", relay: "min_latency=0", want: ["a", "b", "c", "err"] },
		{ id: "max_latency", llm: "max_latency=0.000001", relay: "max_latency=0.000001", want: [] },
		// Known gaps: relay logs have neither filter, so the relay query ignores it and returns the
		// whole scope while the LLM query narrows to the planned calls.
		{ id: "user_agents", llm: `user_agents=${encodeURIComponent(userAgent)}`, relay: `user_agents=${encodeURIComponent(userAgent)}`, want: ["a", "b", "c"], gap: "filter:user_agents" },
		{ id: "metadata", llm: "metadata_env=prod", relay: "metadata_env=prod", want: ["a", "b", "c"], gap: "filter:metadata" },
	];
	for (const f of filters) {
		const failures = [];
		const l = await query("llm", f.llm);
		const r = await query("relay", f.relay);
		if (!llmErr) failures.push("the failing LLM call wrote no row");
		expectEqual(failures, "LLM rows", l, f.want);
		if (!f.gap) {
			expectEqual(failures, "relay rows", r, f.want);
			record("logsapi", `filter ${f.id}`, failures, undefined);
			continue;
		}
		// A gap is the LLM and relay results differing: GAP while they do, STALE once they match. The
		// LLM side is checked on its own, so a broken LLM filter still fails.
		record("logsapi", `filter ${f.id} (LLM side)`, failures);
		const parity = [];
		expectEqual(parity, "relay rows vs LLM rows", r, l);
		record("logsapi", `filter ${f.id}`, parity, f.gap);
	}

	// Stats over the same scope.
	{
		const relayStats = (await api.mustRequest("GET", `/api/relay-logs/stats?${relayScope}&period=1h`)).json;
		const failures = [];
		expectEqual(failures, "relay total_requests", relayStats.total_requests, 4);
		expectEqual(failures, "relay success_count", relayStats.success_count, 3);
		expectEqual(failures, "relay error_count", relayStats.error_count, 1);
		expectEqual(failures, "relay cancelled_count", relayStats.cancelled_count ?? 0, 0);
		expectClose(failures, "relay success_rate", relayStats.success_rate, 75);
		const llmStats = (await api.mustRequest("GET", `/api/logs/stats?${llmScope}&period=1h`)).json;
		const llmSuccess = llmStats.success_count ?? null;
		const llmTotal = llmStats.total_requests ?? null;
		if (llmTotal !== null && llmSuccess !== null && llmTotal > 0) {
			const llmRate = llmStats.success_rate;
			const expected = (llmSuccess / llmTotal) * 100;
			if (!closeTo(llmRate, expected, 1e-6)) failures.push(`LLM success_rate ${llmRate} is not success/finished (${expected}); relay and LLM must measure it the same way`);
		}
		record("logsapi", "stats", failures);
	}

	// Histogram bucket sizes follow the same window rule on both.
	for (const [window, params] of [
		["1h", "period=1h"],
		["7d", "period=7d"],
	]) {
		const failures = [];
		const l = (await api.mustRequest("GET", `/api/logs/histogram?${llmScope}&${params}`)).json;
		const r = (await api.mustRequest("GET", `/api/relay-logs/histogram?${relayScope}&${params}`)).json;
		expectEqual(failures, "bucket size (relay vs LLM)", r.bucket_size_seconds, l.bucket_size_seconds);
		record("logsapi", `histogram bucket ${window}`, failures);
	}

	// Sort and paging.
	{
		const failures = [];
		const asc = (await api.mustRequest("GET", `/api/relay-logs?${relayScope}&sort_by=latency&order=asc&limit=1000`)).json.logs.map((r) => r.latency ?? 0);
		for (let i = 1; i < asc.length; i++) if (asc[i] < asc[i - 1]) failures.push(`latency order broken at ${i}`);
		const page1 = (await api.mustRequest("GET", `/api/relay-logs?${relayScope}&limit=2&offset=0`)).json;
		const page2 = (await api.mustRequest("GET", `/api/relay-logs?${relayScope}&limit=2&offset=2`)).json;
		expectEqual(failures, "total_count", page1.pagination?.total_count, 4);
		const ids = [...page1.logs, ...page2.logs].map((r) => r.id);
		if (new Set(ids).size !== 4) failures.push(`pages overlap or miss rows: ${ids.length} ids, ${new Set(ids).size} distinct`);
		record("logsapi", "sort and paging", failures);
	}

	// Delete, on both.
	{
		const failures = [];
		const relay = await relayRow(rid("api", "c", "relay"));
		await api.mustRequest("DELETE", "/api/relay-logs", { ids: [relay.id] });
		const gone = await api.request("GET", `/api/relay-logs/${relay.id}`);
		expectEqual(failures, "relay row after delete", gone.status, 404);
		await api.mustRequest("DELETE", "/api/logs", { ids: [rid("api", "c", "llm")] });
		const goneLLM = await api.request("GET", `/api/logs/${encodeURIComponent(rid("api", "c", "llm"))}`);
		expectEqual(failures, "LLM row after delete", goneLLM.status, 404);
		record("logsapi", "delete", failures);
	}
}

// --- Suite: network ------------------------------------------------------------------------------

function selfSignedCert() {
	const dir = fs.mkdtempSync(path.join(os.tmpdir(), "relay-matrix-tls-"));
	const key = path.join(dir, "key.pem");
	const cert = path.join(dir, "cert.pem");
	execFileSync("openssl", ["req", "-x509", "-newkey", "rsa:2048", "-nodes", "-keyout", key, "-out", cert, "-days", "1", "-subj", "/CN=127.0.0.1", "-addext", "subjectAltName=IP:127.0.0.1,DNS:localhost"], { stdio: "ignore" });
	const out = { key: fs.readFileSync(key, "utf8"), cert: fs.readFileSync(cert, "utf8") };
	fs.rmSync(dir, { recursive: true, force: true });
	return out;
}

async function suiteNetwork(ctx) {
	const slowID = scenario(`${runTag}net-slow`, async (view, res) => {
		await sleep(2500);
		sendJSON(res, 200, { ok: true });
	});
	const stallID = scenario(`${runTag}net-stall`, async (view, res) => {
		res.writeHead(200, { "content-type": "text/event-stream" });
		res.write('data: {"usage":{"n":1}}\n\n');
		await sleep(2500);
		res.end('data: {"done":true}\n\n');
	});
	const redirectID = scenario(`${runTag}net-redirect`, (view, res) => {
		res.writeHead(302, { location: "/elsewhere", "content-type": "application/json" });
		res.end("{}");
	});
	const largeBytes = Math.round(largeMB * 1024 * 1024);
	const largeID = scenario(`${runTag}net-large`, (view, res) => {
		res.writeHead(200, { "content-type": "text/plain", "content-length": largeBytes });
		const chunk = Buffer.alloc(1024 * 1024, "a");
		let left = largeBytes;
		const pump = () => {
			while (left > 0) {
				const n = Math.min(left, chunk.length);
				left -= n;
				if (!res.write(n === chunk.length ? chunk : chunk.subarray(0, n))) {
					res.once("drain", pump);
					return;
				}
			}
			res.end();
		};
		pump();
	});
	const slowStreamID = scenario(`${runTag}net-slowstream`, async (view, res) => {
		res.writeHead(200, { "content-type": "text/event-stream" });
		// res.destroyed, not req.destroyed: Node destroys a request once its body has been read.
		for (let i = 0; i < 50 && !res.destroyed; i++) {
			res.write(`data: {"usage":{"n":${i}}}\n\n`);
			await sleep(200);
		}
		res.end();
	});
	const mk = (name, id, extra = {}) =>
		createRelay({ name, base_url: `${ctx.upstreamURL}/${id}`, auth: { type: "none" }, usage: { cost_per_request: 0.001, units_path: "usage.n", cost_per_unit: 0.01 }, routes: [{ method: "POST", path: "/**" }], ...extra });

	// Buffered call past network.timeout_seconds.
	{
		const name = `${runTag}-net-timeout`;
		await mk(name, slowID, { network: { timeout_seconds: 1 } });
		const id = rid("net", "timeout");
		const res = await relayRequest(name, "POST", "/x", { body: "{}", headers: { "x-request-id": id } });
		const failures = [];
		if (res.status < 500) failures.push(`caller got ${res.status}, want a timeout error`);
		const row = await relayRow(id);
		expectEqual(failures, "status", row.status, "error");
		if (row.cost) failures.push(`a call with no reply was charged ${row.cost}`);
		record("network", "timeout_seconds", failures);
	}
	// A stream that stalls past stream_idle_timeout_in_seconds.
	{
		const name = `${runTag}-net-idle`;
		await mk(name, stallID, { network: { stream_idle_timeout_in_seconds: 1 } });
		const id = rid("net", "idle");
		await relayRequest(name, "POST", "/x", { body: "{}", headers: { "x-request-id": id } }).catch(() => null);
		const row = await relayRow(id);
		const failures = [];
		expectEqual(failures, "status", row.status, "error");
		const errText = JSON.stringify(row.error_details ?? "");
		if (!/time(d)?\s*out|timeout/i.test(errText)) failures.push(`error is not a timeout: ${errText.slice(0, 200)}`);
		record("network", "stream_idle_timeout_in_seconds", failures);
	}
	// Redirects are returned, not followed.
	{
		const name = `${runTag}-net-redirect`;
		await mk(name, redirectID);
		const id = rid("net", "redirect");
		const res = await relayRequest(name, "POST", "/x", { body: "{}", headers: { "x-request-id": id } });
		const failures = [];
		expectEqual(failures, "caller status", res.status, 302);
		if (!String(res.headers.get("location") || "").includes("/elsewhere")) failures.push(`location=${res.headers.get("location")}`);
		const row = await relayRow(id);
		expectEqual(failures, "status_code", row.status_code, 302);
		if ((seen.get(redirectID) ?? []).length !== 1) failures.push(`the upstream saw ${(seen.get(redirectID) ?? []).length} requests, want 1 (redirect not followed)`);
		record("network", "redirect returned", failures);
	}
	// TLS: a private CA is refused by default, trusted through ca_cert_pem, and skipped through
	// insecure_skip_verify.
	{
		let tls;
		try {
			tls = selfSignedCert();
		} catch (err) {
			record("network", "tls", [`openssl could not make a test certificate: ${err.message}`]);
		}
		if (tls) {
			const server = https.createServer({ key: tls.key, cert: tls.cert }, upstreamHandler());
			const port = await listen(server);
			const tlsID = scenario(`${runTag}net-tls`, (view, res) => sendJSON(res, 200, { tls: true }));
			try {
				for (const [label, network, want] of [
					["untrusted", undefined, "error"],
					["ca_cert_pem", { ca_cert_pem: tls.cert }, 200],
					["insecure_skip_verify", { insecure_skip_verify: true }, 200],
				]) {
					const name = `${runTag}-net-tls-${label}`.replace(/_/g, "-");
					await createRelay({ name, base_url: `https://127.0.0.1:${port}/${tlsID}`, auth: { type: "none" }, routes: [{ method: "POST", path: "/**" }], ...(network ? { network } : {}) });
					const res = await relayRequest(name, "POST", "/x", { body: "{}" });
					const failures = [];
					if (want === 200) expectEqual(failures, "caller status", res.status, 200);
					else if (res.status < 500) failures.push(`an untrusted certificate was accepted (${res.status})`);
					record("network", `tls ${label}`, failures);
				}
			} finally {
				await closeServer(server);
			}
		}
	}
	// An unreachable upstream.
	{
		const s = http.createServer();
		const port = await listen(s);
		await closeServer(s);
		const name = `${runTag}-net-dead`;
		await createRelay({ name, base_url: `http://127.0.0.1:${port}`, auth: { type: "none" }, usage: { cost_per_request: 0.001 }, routes: [{ method: "POST", path: "/**" }] });
		const id = rid("net", "dead");
		const res = await relayRequest(name, "POST", "/x", { body: "{}", headers: { "x-request-id": id } });
		const failures = [];
		if (res.status < 500) failures.push(`caller got ${res.status}`);
		const row = await relayRow(id);
		expectEqual(failures, "status", row.status, "error");
		if (row.cost) failures.push(`charged ${row.cost} for no reply`);
		record("network", "unreachable upstream", failures);
	}
	// A reply past the relay's old 64 MiB buffered cap is relayed and logged whole.
	{
		const name = `${runTag}-net-large`;
		await createRelay({ name, base_url: `${ctx.upstreamURL}/${largeID}`, auth: { type: "none" }, usage: { cost_per_request: 0.001 }, routes: [{ method: "POST", path: "/**", stream: false }] });
		const id = rid("net", "large");
		const res = await relayRequest(name, "POST", "/x", { body: "{}", headers: { "x-request-id": id } });
		const failures = [];
		expectEqual(failures, "caller status", res.status, 200);
		expectEqual(failures, "caller bytes", res.buf.length, largeBytes);
		const row = await relayRowWhere(id, (r) => String(r.response_body ?? "").length === largeBytes, 120000);
		expectEqual(failures, "bytes_out", row.bytes_out, largeBytes);
		expectEqual(failures, "logged response_body length", String(row.response_body ?? "").length, largeBytes);
		record("network", `buffered reply of ${largeMB} MiB`, failures);
	}
	// A caller that hangs up mid-stream leaves a cancelled row that is still charged what it saw.
	{
		const name = `${runTag}-net-cancel`;
		await mk(name, slowStreamID, { routes: [{ method: "POST", path: "/**", stream: true }] });
		const id = rid("net", "cancel");
		const ac = new AbortController();
		const res = await fetch(`${baseURL}/relay/${name}/x`, { method: "POST", body: "{}", headers: { "content-type": "application/json", "x-request-id": id }, signal: ac.signal });
		const reader = res.body.getReader();
		await reader.read();
		await sleep(500);
		ac.abort();
		const row = await relayRow(id);
		const failures = [];
		expectEqual(failures, "status", row.status, "cancelled");
		if (!(row.cost > 0)) failures.push(`cost=${row.cost}, want the observed usage charged`);
		if (failures.length) failures.push(`(row latency ${Math.round(row.latency)}ms, bytes_out ${row.bytes_out})`);
		record("network", "caller hangs up mid-stream", failures);
	}
}

// --- Suite: proxy --------------------------------------------------------------------------------

async function suiteProxy(ctx) {
	const user = "relay-matrix";
	const pass = `pw-${randomBytes(4).toString("hex")}`;
	const httpProxy = await startHttpProxy({ user, pass });
	const socks = await startSocks5Proxy({ user, pass });
	const id = scenario(`${runTag}proxy`, echoScenario);
	const upstreamHost = new URL(ctx.upstreamURL).host;
	const globalSnapshot = (await api.mustRequest("GET", "/api/proxy-config")).json;
	const hitsTo = (proxy, host, authorized = true) => proxy.hits.filter((h) => h.target === host && h.authorized === authorized).length;
	// node's http client, not fetch: fetch turns a 407 answer into a network error, and a proxy
	// refusal relayed for an http:// upstream arrives as exactly that.
	const call = (name) =>
		new Promise((resolve) => {
			const u = new URL(`${baseURL}/relay/${name}/x`);
			const req = http.request({ host: u.hostname, port: u.port, path: u.pathname, method: "POST", headers: { "content-type": "application/json" } }, (res) => {
				const chunks = [];
				res.on("data", (c) => chunks.push(c));
				res.on("end", () => {
					const text = Buffer.concat(chunks).toString("utf8");
					let json = null;
					try {
						json = JSON.parse(text);
					} catch {
						json = null;
					}
					resolve({ status: res.statusCode, text, json });
				});
			});
			req.on("error", (err) => resolve({ status: 0, text: err.message, json: null }));
			req.end("{}");
		});
	const make = (suffix, proxyConfig, extra = {}) => {
		const cfg = { name: `${runTag}-px-${suffix}`, base_url: `${ctx.upstreamURL}/${id}`, auth: { type: "none" }, routes: [{ method: "POST", path: "/**" }], ...extra };
		if (proxyConfig !== undefined) cfg.proxy_config = proxyConfig;
		return createRelay(cfg).then(() => cfg.name);
	};
	const putGlobal = (body) => api.mustRequest("PUT", "/api/proxy-config", body);
	let changedGlobal = false;
	try {
		// A relay's own proxy, each type.
		const cases = [
			{ id: "http with credentials", proxy: { type: "http", url: httpProxy.url, username: user, password: pass }, via: httpProxy, want: 200 },
			{ id: "http wrong password", proxy: { type: "http", url: httpProxy.url, username: user, password: "wrong" }, via: httpProxy, refused: true },
			{ id: "socks5 with credentials", proxy: { type: "socks5", url: socks.url, username: user, password: pass }, via: socks, want: 200 },
			{ id: "socks5 wrong password", proxy: { type: "socks5", url: socks.url, username: user, password: "wrong" }, via: socks, refused: true },
			{ id: "none with no global proxy goes direct", proxy: { type: "none" }, want: 200, direct: true },
		];
		for (const c of cases) {
			// On a gateway whose global proxy is on for inference, a none relay inherits it (as the
			// global proxy cases below assert), so it cannot be checked for going direct there.
			if (c.direct && globalProxyForInference(globalSnapshot)) {
				console.log(`  proxy: "${c.id}" skipped (the gateway's global proxy is on for inference)`);
				continue;
			}
			const name = await make(c.id.replace(/[^a-z0-9]+/g, "-"), c.proxy);
			const before = { http: hitsTo(httpProxy, upstreamHost), socks: hitsTo(socks, upstreamHost), seen: (seen.get(id) ?? []).length };
			const res = await call(name);
			const failures = [];
			const reached = (seen.get(id) ?? []).length - before.seen;
			if (c.refused) {
				if (res.status === 200) failures.push("a proxy that refused the credentials still relayed the call");
				if (reached) failures.push("the upstream was reached past a refusing proxy");
				const refusedHits = c.via === httpProxy ? httpProxy.hits.filter((h) => !h.authorized).length : socks.hits.filter((h) => !h.authorized).length;
				if (!refusedHits) failures.push("the proxy never saw the refused attempt");
			} else {
				expectEqual(failures, "caller status", res.status, c.want);
				if (reached !== 1) failures.push(`the upstream saw ${reached} requests, want 1`);
				if (c.direct) {
					if (hitsTo(httpProxy, upstreamHost) !== before.http || hitsTo(socks, upstreamHost) !== before.socks) failures.push("a type none relay went through a proxy");
				} else {
					const after = c.via === httpProxy ? hitsTo(httpProxy, upstreamHost) - before.http : hitsTo(socks, upstreamHost) - before.socks;
					if (after < 1) failures.push(`the call did not go through the ${c.via === httpProxy ? "HTTP" : "SOCKS5"} proxy`);
					if (c.via === httpProxy && res.json?.headers?.via !== "1.1 relay-matrix-proxy") failures.push(`upstream saw via=${res.json?.headers?.via}`);
				}
			}
			record("proxy", c.id, failures);
		}

		// An https upstream through the HTTP proxy is a CONNECT tunnel; the relay still verifies the
		// upstream's certificate with its own ca_cert_pem.
		{
			let tls;
			try {
				tls = selfSignedCert();
			} catch (err) {
				record("proxy", "https upstream through a CONNECT tunnel", [`openssl could not make a test certificate: ${err.message}`]);
			}
			if (tls) {
				const server = https.createServer({ key: tls.key, cert: tls.cert }, upstreamHandler());
				const port = await listen(server);
				try {
					const name = `${runTag}-px-tls`;
					await createRelay({
						name,
						base_url: `https://127.0.0.1:${port}/${id}`,
						auth: { type: "none" },
						routes: [{ method: "POST", path: "/**" }],
						network: { ca_cert_pem: tls.cert },
						proxy_config: { type: "http", url: httpProxy.url, username: user, password: pass },
					});
					const res = await call(name);
					const failures = [];
					expectEqual(failures, "caller status", res.status, 200);
					if (!httpProxy.hits.some((h) => h.kind === "connect" && h.target === `127.0.0.1:${port}` && h.authorized)) failures.push("no authorized CONNECT to the https upstream");
					record("proxy", "https upstream through a CONNECT tunnel", failures);
				} finally {
					await closeServer(server);
				}
			}
		}

		// The global proxy cases replace the gateway's global proxy, which is only safe when it can be
		// put back as it was; one with a password cannot (globalProxyRestorable), so it is left alone.
		if (!globalProxyRestorable(globalSnapshot)) {
			console.log("  proxy: global proxy cases skipped (the gateway's global proxy has a password, which this run could not restore)");
			return;
		}
		// The global proxy: a relay with no proxy_config inherits it once it is on for inference; a
		// relay's own proxy_config (including none) wins over it; off for inference, nothing inherits.
		const inheriting = await make("inherit", undefined);
		const ownSocks = await make("own-socks", { type: "socks5", url: socks.url, username: user, password: pass });
		const ownNone = await make("own-none", { type: "none" });
		const globalBody = (enableForInference) => ({
			enabled: true,
			type: "http",
			url: httpProxy.url,
			username: user,
			password: pass,
			no_proxy: "",
			timeout: 30,
			skip_tls_verify: false,
			enable_for_scim: false,
			enable_for_inference: enableForInference,
			enable_for_api: false,
		});
		changedGlobal = true;
		await putGlobal(globalBody(true));
		{
			const failures = [];
			const before = hitsTo(httpProxy, upstreamHost);
			let inheritedOK = false;
			await poll("relays to rebuild on the global proxy", 20000, async () => {
				const res = await call(inheriting);
				inheritedOK = res.status === 200 && hitsTo(httpProxy, upstreamHost) > before;
				return inheritedOK;
			}, 1000).catch(() => {});
			if (!inheritedOK) failures.push("a relay with no proxy_config did not go through the global proxy");
			record("proxy", "global proxy inherited by a relay with no proxy_config", failures);
		}
		{
			const failures = [];
			const beforeHTTP = hitsTo(httpProxy, upstreamHost);
			const beforeSocks = hitsTo(socks, upstreamHost);
			const res = await call(ownSocks);
			expectEqual(failures, "caller status", res.status, 200);
			if (hitsTo(socks, upstreamHost) <= beforeSocks) failures.push("the relay's own SOCKS5 proxy was not used");
			if (hitsTo(httpProxy, upstreamHost) !== beforeHTTP) failures.push("the global proxy was used instead of the relay's own");
			record("proxy", "a relay's own proxy wins over the global proxy", failures);
		}
		{
			// "none" names no proxy of the relay's own, as for providers, so it inherits the global one.
			const failures = [];
			const before = hitsTo(httpProxy, upstreamHost);
			const res = await call(ownNone);
			expectEqual(failures, "caller status", res.status, 200);
			if (hitsTo(httpProxy, upstreamHost) <= before) failures.push("a type none relay did not inherit the global proxy");
			record("proxy", "a relay set to none inherits the global proxy, as a provider does", failures);
		}
		await putGlobal(globalBody(false));
		{
			const failures = [];
			let directOK = false;
			await poll("relays to rebuild without the global proxy", 20000, async () => {
				const before = hitsTo(httpProxy, upstreamHost);
				const res = await call(inheriting);
				directOK = res.status === 200 && hitsTo(httpProxy, upstreamHost) === before;
				return directOK;
			}, 1000).catch(() => {});
			if (!directOK) failures.push("a relay still used the global proxy after enable_for_inference was turned off");
			record("proxy", "global proxy off for inference is not inherited", failures);
		}
	} finally {
		// Restore the global proxy as it was. A failed restore fails the run: the gateway is left on
		// this suite's proxy, which closes below.
		if (changedGlobal) {
			const restore = { ...(globalSnapshot ?? {}) };
			await putGlobal(restore.enabled ? restore : { ...restore, enabled: false }).catch((e) =>
				record("proxy", "restore global proxy", [`restore global proxy: ${e.message}`]),
			);
		}
		await httpProxy.close();
		await socks.close();
	}
}

// --- Suite: hybrid -------------------------------------------------------------------------------

async function fakeObjects(prefix) {
	const res = await fetch(`${fakeS3URL}/__fake-s3/objects?prefix=${encodeURIComponent(prefix)}`);
	if (!res.ok) throw new Error(`fake S3 inspection failed with ${res.status}`);
	return (await res.json()).objects.map((o) => {
		let body = Buffer.from(o.body, "base64");
		if (body[0] === 0x1f && body[1] === 0x8b) body = gunzipSync(body);
		return { ...o, text: body.toString("utf8") };
	});
}

async function objectFor(kind, id, timeoutMs = 30000) {
	const dir = kind === "relay" ? "relay-logs" : "logs";
	return poll(`${kind} object for ${id}`, timeoutMs, async () => (await fakeObjects(`${objectPrefix}/${dir}/`)).find((o) => o.key.includes(`/${id}.`)) ?? null);
}

async function noObjectFor(kind, id) {
	await sleep(4000);
	const dir = kind === "relay" ? "relay-logs" : "logs";
	return !(await fakeObjects(`${objectPrefix}/${dir}/`)).some((o) => o.key.includes(`/${id}.`));
}

async function suiteHybrid(ctx) {
	if (!fakeS3URL) {
		console.log("hybrid: skipped (no BIFROST_E2E_FAKE_S3_URL; the gateway has no object storage this run can inspect)");
		return;
	}
	const echo = await ensureEchoPair(ctx);
	const snapshot = await api.configSnapshot();
	const vk = await createKey(pairKeyBody(echo, { name: "hybrid" }));
	const long = `prompt-${"x".repeat(5000)}`;
	const relayExcluded = excludedTypes.has("relay");
	const llmExcluded = excludedTypes.has("chat_completion");

	// Offload and hydrate.
	{
		const lid = rid("hy", "llm");
		const relayID = rid("hy", "relay");
		await llmCall(echo, lid, vk.value, {}, long);
		await echoRelayCall(echo, relayID, vk.value, {}, "", long);
		const relay = await relayRowWhere(relayID, (r) => String(r.request_body ?? "").includes(long) && String(r.response_body ?? "").includes("x".repeat(5000)));
		const failures = [];
		const raw = await ctx.logsDb.read("relay_logs", "id", relay.id);
		if (!String(relay.request_body ?? "").includes(long)) failures.push("API did not return the full relay request body");
		if (!String(relay.response_body ?? "").includes("x".repeat(5000))) failures.push("API did not return the full relay response body");
		if (relayExcluded) {
			if (!(await noObjectFor("relay", relay.id))) failures.push("relay is excluded from object storage, but an object was written");
			if (!String(raw?.request_body ?? "").includes(long)) failures.push("excluded relay row does not keep its full body in the database");
		} else {
			const obj = await objectFor("relay", relay.id).catch(() => null);
			if (!obj) failures.push("no relay object was offloaded");
			else {
				if (!obj.text.includes(long)) failures.push("the relay object lacks the full request body");
				if (obj.tags?.relay_name !== echo.relay) failures.push(`object tags ${JSON.stringify(obj.tags)} lack relay_name`);
			}
			for (const field of ["request_body", "response_body"]) {
				const len = [...String(raw?.[field] ?? "")].length;
				if (excludedFields.has(field)) {
					if (len < 5000) failures.push(`${field} is excluded from offload but the database keeps ${len} characters`);
				} else if (len > 2048) failures.push(`database ${field} keeps ${len} characters, want a preview of at most 2048`);
			}
		}
		record("hybrid", "relay offload and hydrate", failures);

		const lf = [];
		const llm = await llmRow(lid);
		if (!JSON.stringify(llm.input_history ?? "").includes("x".repeat(5000))) lf.push("API did not return the full LLM input");
		if (llmExcluded) {
			if (!(await noObjectFor("llm", lid))) lf.push("chat_completion is excluded, but an LLM object was written");
		} else if (!(await objectFor("llm", lid).catch(() => null))) lf.push("no LLM object was offloaded");
		record("hybrid", "LLM offload and hydrate (parity twin)", lf);
	}

	// Hidden retention: content logging off, retained in object storage. Both rows are hidden, keep
	// no body in the database, and still have an object.
	try {
		await api.putClientConfig(snapshot, { disable_content_logging: true, retain_content_in_object_storage: true });
		await sleep(500);
		const lid = rid("hy", "hidden", "llm");
		const relayID = rid("hy", "hidden", "relay");
		await llmCall(echo, lid, vk.value, {}, `prompt-hidden-${runTag}`);
		await echoRelayCall(echo, relayID, vk.value, {}, "", `prompt-hidden-${runTag}`);
		const relay = await relayRow(relayID);
		const llm = await llmRow(lid);
		const failures = [];
		expectEqual(failures, "relay content_hidden", relay.content_hidden, true);
		expectEqual(failures, "LLM content_hidden", llm.content_hidden, true);
		if (JSON.stringify(relay).includes(`hidden-${runTag}`)) failures.push("API served hidden relay content");
		const raw = await ctx.logsDb.read("relay_logs", "id", relay.id);
		if (JSON.stringify(raw).includes(`hidden-${runTag}`)) failures.push("the relay database row holds hidden content");
		const relayObj = await objectFor("relay", relay.id).catch(() => null);
		const llmObj = await objectFor("llm", lid).catch(() => null);
		if (!relayObj) failures.push("no relay object retained");
		else if (!relayObj.text.includes(`hidden-${runTag}`)) failures.push("the retained relay object lacks the content");
		if (!llmObj) failures.push("no LLM object retained (parity twin)");
		record("hybrid", "hidden content retained in object storage", failures);
	} finally {
		await api.putClientConfig(snapshot, {});
	}

	// Deleting a row deletes its object, on both.
	{
		const lid = rid("hy", "del", "llm");
		const relayID = rid("hy", "del", "relay");
		await llmCall(echo, lid, vk.value, {}, long);
		await echoRelayCall(echo, relayID, vk.value, {}, "", long);
		const relay = await relayRow(relayID);
		await llmRow(lid);
		const failures = [];
		if (!relayExcluded) await objectFor("relay", relay.id).catch(() => failures.push("no relay object before delete"));
		if (!llmExcluded) await objectFor("llm", lid).catch(() => failures.push("no LLM object before delete"));
		await api.mustRequest("DELETE", "/api/relay-logs", { ids: [relay.id] });
		await api.mustRequest("DELETE", "/api/logs", { ids: [lid] });
		if (!(await noObjectFor("relay", relay.id))) failures.push("the relay object outlived its row");
		if (!(await noObjectFor("llm", lid))) failures.push("the LLM object outlived its row");
		record("hybrid", "delete removes objects", failures);
	}
}

// --- Main ----------------------------------------------------------------------------------------

async function main() {
	const suites = selectedSuites();
	console.log("Running relay permutation matrix...");
	console.log(`  Bifrost: ${baseURL}`);
	console.log(`  Suites: ${suites.join(", ")}`);
	const upstream = http.createServer(upstreamHandler());
	const echoServer = createEchoUpstream();
	const ctx = {
		upstreamURL: `http://127.0.0.1:${await listen(upstream)}`,
		echoURL: `http://127.0.0.1:${await listen(echoServer)}`,
		logsDb: null,
	};
	console.log(`  Relay upstream: ${ctx.upstreamURL}`);
	console.log(`  Echo upstream: ${ctx.echoURL}`);
	if (fakeS3URL) console.log(`  Object storage: ${fakeS3URL} (prefix ${objectPrefix}; excluded types [${[...excludedTypes]}], fields [${[...excludedFields]}])`);
	const snapshot = await api.configSnapshot();
	let fatal = null;
	try {
		if (suites.includes("hybrid") && fakeS3URL) {
			ctx.logsDb = await openLogsDb();
			console.log(`  Logs DB: ${ctx.logsDb.label}`);
		}
		// Feature suites call without a key, which inference auth would refuse.
		await api.putClientConfig(snapshot, { enforce_auth_on_inference: false });
		const run = { pricing: suitePricing, streaming: suiteStreaming, billing: suiteBilling, routes: suiteRoutes, auth: suiteAuth, governance: suiteGovernance, settings: suiteSettings, logsapi: suiteLogsAPI, network: suiteNetwork, proxy: suiteProxy, hybrid: suiteHybrid };
		for (const s of suites) {
			console.log(`\n== ${s}`);
			try {
				await run[s](ctx);
			} catch (err) {
				record(s, "suite", [`suite aborted: ${err.stack || err.message}`]);
			}
		}
	} catch (err) {
		fatal = err;
	} finally {
		// A failed restore or cleanup fails the run: a standalone run leaves inference auth off, or
		// its keys and relays behind, on a gateway it does not own.
		await api.putClientConfig(snapshot, {}).catch((e) => record("setup", "restore client config", [`restore client config: ${e.message}`]));
		const cleanupErrors = await cleanup();
		if (cleanupErrors.length) record("cleanup", "delete what the run created", cleanupErrors);
		await Promise.all([closeServer(upstream), closeServer(echoServer), ctx.logsDb?.close().catch(() => {})]);
	}
	if (fatal) throw fatal;

	const by = (k) => results.filter((r) => r.kind === k);
	console.log(`\nRelay matrix: ${by("ok").length} ok, ${by("GAP").length} known gaps, ${by("FAIL").length} failed, ${by("STALE").length} stale gaps (${results.length} checks).`);
	if (by("FAIL").length || by("STALE").length) {
		console.log("Failures:");
		for (const r of [...by("FAIL"), ...by("STALE")]) console.log(`  ${r.suite}/${r.id}: ${r.failures.join("; ") || r.gapKey}`);
		process.exit(1);
	}
}

main().catch((err) => {
	console.error(`Relay matrix failed: ${err.stack || err.message}`);
	process.exit(1);
});
