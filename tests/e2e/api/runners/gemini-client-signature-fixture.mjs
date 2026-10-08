import fs from "node:fs";
import path from "node:path";
import { pathToFileURL } from "node:url";
import { argValue, createInterceptProxy } from "./lib/tls-intercept-proxy.mjs";

// Record native Chat tool-call replay after Gemini/Vertex conversion. Responses are
// independent of signatures: only the recorder assertions establish byte retention.
// This fixture does not validate Google's signatures or emulate model reasoning.
export function replyToRecordedGeminiRequest(req, body) {
	const host = req.socket.fixtureHost;
	const url = new URL(req.url, "https://" + host);
	const operation = url.pathname.match(/:(generateContent|streamGenerateContent)$/);
	const modelPath = url.pathname.replace(/:(generateContent|streamGenerateContent)$/, "");
	const permittedModel = (host === "generativelanguage.googleapis.com" && modelPath === "/v1beta/models/gemini-2.5-flash") ||
		(host === "us-central1-aiplatform.googleapis.com" && modelPath === "/v1/projects/fixture-project/locations/us-central1/publishers/google/models/gemini-2.5-flash");
	if (req.method === "GET" && host === "generativelanguage.googleapis.com" && url.pathname === "/v1beta/models") {
		return { status: 200, body: JSON.stringify({ models: [{ name: "models/gemini-2.5-flash", supportedGenerationMethods: ["generateContent"] }] }) };
	}
	if (req.method !== "POST" || !permittedModel || !operation) {
		return { status: 404, body: JSON.stringify({ error: { code: 404, message: "signature fixture does not serve " + host + url.pathname, status: "NOT_FOUND" } }) };
	}
	try { JSON.parse(body); } catch {
		return { status: 400, body: JSON.stringify({ error: { code: 400, message: "fixture received invalid JSON", status: "INVALID_ARGUMENT" } }) };
	}
	const response = {
		candidates: [{ content: { role: "model", parts: [{ text: "fixture completion" }] }, finishReason: "STOP", index: 0 }],
		usageMetadata: { promptTokenCount: 1, candidatesTokenCount: 1, totalTokenCount: 2 },
		modelVersion: "gemini-2.5-flash",
	};
	if (operation[1] === "streamGenerateContent") {
		return { status: 200, headers: { "Content-Type": "text/event-stream" }, body: "data: " + JSON.stringify(response) + "\n\n" };
	}
	return { status: 200, body: JSON.stringify(response) };
}

function start() {
	const args = process.argv.slice(2);
	const appDir = argValue(args, "--app-dir", "");
	const port = Number(argValue(args, "--port", "8810"));
	if (!appDir || !Number.isInteger(port) || port < 1024 || port > 65535) {
		console.error("usage: node gemini-client-signature-fixture.mjs --app-dir <dir> [--port 8810]");
		process.exit(2);
	}
	const controlUrl = "http://127.0.0.1:" + port;
	const proxy = createInterceptProxy({
		port,
		staticJson: { "/pricing.json": {}, "/model-parameters.json": {} },
		respond: replyToRecordedGeminiRequest,
	});
	const network = { max_retries: 0, default_request_timeout_in_seconds: 5, allow_private_network: true };
	const proxyConfig = { type: "http", url: controlUrl, ca_cert_pem: proxy.caPem };
	const config = {
		setup_token: "bifrost-e2e-setup-token",
		framework: { pricing: { pricing_url: controlUrl + "/pricing.json", model_parameters_url: controlUrl + "/model-parameters.json", mcp_library_sync_interval: 0 } },
		providers: {
			gemini: {
				keys: [{ name: "gemini-client-signature-fixture", value: "fixture-api-key-not-real", models: ["gemini-2.5-flash"], weight: 1 }],
				network_config: network,
				proxy_config: proxyConfig,
			},
			vertex: {
				keys: [{ name: "vertex-client-signature-fixture", value: "fixture-api-key-not-real", models: ["gemini-2.5-flash"], weight: 1, vertex_key_config: { project_id: "fixture-project", region: "us-central1" } }],
				network_config: network,
				proxy_config: proxyConfig,
			},
		},
	};
	fs.mkdirSync(appDir, { recursive: true });
	fs.writeFileSync(path.join(appDir, "config.json"), JSON.stringify(config, null, 2));
	fs.writeFileSync(path.join(appDir, "environment.json"), JSON.stringify({
		name: "Gemini client signature local recorder",
		values: [
			{ key: "clientThoughtSignatureFixture", value: "1", enabled: true },
			{ key: "clientThoughtSignatureControlUrl", value: controlUrl, enabled: true },
		],
	}, null, 2));
	proxy.listen(() => console.log("client signature fixture listening on " + controlUrl));
}

if (process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) start();
