// Shared management-API client for the node runners that drive a live gateway
// (run-content-logging-matrix.mjs, run-relay-matrix.mjs, run-log-store-matrix.mjs).
//
// Every call carries the setup token (the OSS setup lock refuses /api without it while dashboard
// auth is off) and, when given, an admin Authorization header.
import { normaliseRetentionDays } from "./content-logging-matrix.mjs";

export function gatewayClient({ baseURL, authHeader = "", setupToken = "" }) {
	const base = baseURL.replace(/\/+$/, "");

	async function request(method, path, body, headers = {}, { raw = false, signal } = {}) {
		const requestHeaders = authHeader ? { Authorization: authHeader, ...headers } : { ...headers };
		if (setupToken) requestHeaders["X-Bifrost-Setup-Token"] = setupToken;
		let payload;
		if (body !== undefined) {
			if (raw) {
				payload = body;
			} else {
				payload = JSON.stringify(body);
				if (!Object.keys(requestHeaders).some((h) => h.toLowerCase() === "content-type")) requestHeaders["content-type"] = "application/json";
			}
		}
		const res = await fetch(`${base}${path}`, { method, headers: requestHeaders, body: payload, redirect: "manual", signal });
		const buf = Buffer.from(await res.arrayBuffer());
		const text = buf.toString("utf8");
		let json = null;
		try {
			json = text ? JSON.parse(text) : null;
		} catch {
			json = null;
		}
		return { ok: res.ok, status: res.status, text, buf, json, headers: res.headers };
	}

	async function mustRequest(method, path, body, headers = {}, opts = {}) {
		const res = await request(method, path, body, headers, opts);
		if (!res.ok) throw new Error(`${method} ${path} failed with ${res.status}: ${res.text.slice(0, 600)}`);
		return res;
	}

	// putClientConfig writes client_config back with overrides. GET reports an unset
	// log_retention_days as 0, which PUT rejects, so it is normalised (see content-logging-matrix).
	// auth_config is never resent.
	async function putClientConfig(snapshot, overrides) {
		const clientConfig = { ...snapshot.client_config, ...overrides };
		clientConfig.log_retention_days = normaliseRetentionDays(clientConfig.log_retention_days);
		await mustRequest("PUT", "/api/config", { client_config: clientConfig, framework_config: snapshot.framework_config });
	}

	async function configSnapshot() {
		const snapshot = (await mustRequest("GET", "/api/config")).json;
		if (!snapshot?.client_config) throw new Error("GET /api/config returned no client_config");
		return snapshot;
	}

	return { baseURL: base, request, mustRequest, putClientConfig, configSnapshot };
}

// poll retries fn every intervalMs until it returns a truthy value or timeoutMs passes.
export async function poll(name, timeoutMs, fn, intervalMs = 500) {
	const started = Date.now();
	let lastError;
	while (Date.now() - started < timeoutMs) {
		try {
			const result = await fn();
			if (result) return result;
		} catch (err) {
			lastError = err;
		}
		await new Promise((resolve) => setTimeout(resolve, intervalMs));
	}
	throw new Error(`${name} timed out${lastError ? `: ${lastError.message}` : ""}`);
}

// mapLimit runs fn over items with at most limit in flight, keeping result order.
export async function mapLimit(items, limit, fn) {
	const results = new Array(items.length);
	let next = 0;
	async function worker() {
		while (next < items.length) {
			const i = next++;
			results[i] = await fn(items[i], i);
		}
	}
	await Promise.all(Array.from({ length: Math.min(limit, items.length) }, worker));
	return results;
}

export function listen(server, host = "127.0.0.1") {
	return new Promise((resolve, reject) => {
		server.once("error", reject);
		server.listen(0, host, () => {
			server.off("error", reject);
			resolve(server.address().port);
		});
	});
}

export function closeServer(server) {
	return new Promise((resolve) => {
		server.closeAllConnections?.();
		server.close(() => resolve());
	});
}

export function readBody(req) {
	return new Promise((resolve, reject) => {
		const chunks = [];
		req.on("data", (chunk) => chunks.push(chunk));
		req.on("end", () => resolve(Buffer.concat(chunks)));
		req.on("error", reject);
	});
}
