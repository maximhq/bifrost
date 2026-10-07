#!/usr/bin/env node
// Local upstream for the provider harness's "gateway correlation headers" cases (#7592).
// This starts only a fixture: it never contacts Bifrost or a paid provider.
// With dashboard auth already enabled on the test gateway:
// BIFROST_E2E_AUTH_HEADER='Bearer ...' node tests/e2e/api/runners/run-correlation-header-mock.mjs --env-out tmp/correlation-header-mock.postman_environment.json
// Pass that file as ENV_FILE and set INCLUDE_PREVIEW=1 in the scoped harness run.

import http from "node:http";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

export function createCorrelationHeaderMock() {
	const server = http.createServer((req, res) => {
		if (req.method === "GET" && req.url === "/__health") {
			res.writeHead(200, { "content-type": "application/json" });
			res.end(JSON.stringify({ fixture: "bifrost-correlation-7592", version: 1 }));
			return;
		}
		if (req.method !== "POST" || req.url !== "/v1/chat/completions") {
			res.writeHead(404);
			res.end("correlation fixture: unknown route");
			return;
		}
		let raw = "";
		req.setTimeout(10000, () => req.destroy());
		req.on("error", () => res.destroy());
		req.on("data", (chunk) => {
			raw += chunk;
			if (Buffer.byteLength(raw) > 65536) req.destroy();
		});
		req.on("end", () => {
			let body;
			try { body = JSON.parse(raw); } catch {
				res.writeHead(400);
				res.end("correlation fixture: invalid JSON");
				return;
			}
			const marker = Array.isArray(body?.messages) && body.messages.find((message) => message?.role === "user")?.content;
			const match = typeof marker === "string" && /^correlation-header:(success|error|stream|stream-error):([a-zA-Z0-9_-]+)$/.exec(marker);
			if (!match || body.model !== "correlation-fixture") {
				res.writeHead(400);
				res.end("correlation fixture: invalid scenario or model");
				return;
			}
			const [, scenario, nonce] = match;
			const streaming = scenario === "stream" || scenario === "stream-error";
			if (Boolean(body.stream) !== streaming) {
				res.writeHead(400);
				res.end("correlation fixture: scenario/stream mismatch");
				return;
			}
			const headers = {
				"X-ReQuEsT-Id": `provider-request-${nonce}`,
				"X-BiFrOsT-TrAcE-Id": `provider-trace-${nonce}`,
				"Request-Id": `upstream-request-${nonce}`,
				"X-Correlation-Mock": nonce,
				"content-type": scenario === "stream" ? "text/event-stream" : "application/json",
			};
			if (scenario === "error" || scenario === "stream-error") {
				res.writeHead(404, headers);
				res.end(JSON.stringify({ error: { message: `correlation mock error:${nonce}`, type: "invalid_request_error", code: "model_not_found" } }));
				return;
			}
			const common = { id: `chatcmpl-${nonce}`, created: Math.floor(Date.now() / 1000), model: body.model };
			res.writeHead(200, headers);
			if (scenario === "stream") {
				res.write(`data: ${JSON.stringify({ ...common, object: "chat.completion.chunk", choices: [{ index: 0, delta: { role: "assistant", content: `correlation mock stream:${nonce}` }, finish_reason: null }] })}\n\n`);
				res.write(`data: ${JSON.stringify({ ...common, object: "chat.completion.chunk", choices: [{ index: 0, delta: {}, finish_reason: "stop" }], usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 } })}\n\n`);
				res.end("data: [DONE]\n\n");
			} else {
				res.end(JSON.stringify({ ...common, object: "chat.completion", choices: [{ index: 0, message: { role: "assistant", content: `correlation mock success:${nonce}` }, finish_reason: "stop" }], usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 } }));
			}
		});
	});
	server.requestTimeout = 10000;
	server.headersTimeout = 10000;
	server.keepAliveTimeout = 1000;
	return server;
}

async function main() {
	const args = process.argv.slice(2);
	if (args.length && (args.length !== 2 || args[0] !== "--env-out" || !args[1])) {
		throw new Error("Usage: run-correlation-header-mock.mjs [--env-out path.json]");
	}
	const port = Number(process.env.CORRELATION_HEADER_MOCK_PORT || 0);
	if (!Number.isInteger(port) || port < 0 || port > 65535) throw new Error("CORRELATION_HEADER_MOCK_PORT must be 0..65535");
	const server = createCorrelationHeaderMock();
	await new Promise((resolve, reject) => {
		server.once("error", reject);
		server.listen(port, "127.0.0.1", resolve);
	});
	const baseURL = `http://127.0.0.1:${server.address().port}`;
	const envPath = args[1] && path.resolve(args[1]);
	let ownedEnvironment;
	try {
		if (envPath) {
			ownedEnvironment = JSON.stringify({ name: "Bifrost correlation header local fixture", values: [
				{ key: "correlationMockBaseUrl", value: baseURL, enabled: true },
				{ key: "correlationAdminAuth", value: process.env.BIFROST_E2E_AUTH_HEADER || "", enabled: true },
			] }, null, 2) + "\n";
			fs.mkdirSync(path.dirname(envPath), { recursive: true });
			fs.writeFileSync(envPath, ownedEnvironment, { flag: "wx", mode: 0o600 });
		}
	} catch (error) {
		server.close();
		throw error;
	}
	console.log(`correlation header mock listening on ${baseURL}; gateway and fixture must run on the same host`);
	if (envPath) console.log(`Postman environment: ${envPath}`);
	let closing = false;
	const shutdown = () => {
		if (closing) return;
		closing = true;
		if (envPath && ownedEnvironment) {
			try { if (fs.readFileSync(envPath, "utf8") === ownedEnvironment) fs.unlinkSync(envPath); } catch (error) {
				if (error.code !== "ENOENT") console.error(`Could not remove owned environment file: ${error.code}`);
			}
		}
		server.close();
		server.closeAllConnections();
	};
	process.once("SIGINT", shutdown);
	process.once("SIGTERM", shutdown);
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
	main().catch((error) => { console.error(error.message); process.exitCode = 1; });
}
