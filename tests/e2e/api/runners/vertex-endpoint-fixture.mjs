import crypto from "node:crypto";
import fs from "node:fs";
import path from "node:path";
import { argValue, createInterceptProxy } from "./lib/tls-intercept-proxy.mjs";

// Local stand-in for Vertex AI, used to observe which host, path and headers the gateway sends.
//
// Vertex hardcodes https://<host>/... with no base-URL override, so the fixture works as the
// gateway's HTTP proxy instead (see lib/tls-intercept-proxy.mjs): it terminates TLS for the
// requested host, records the decrypted request, and replies with a canned Gemini or Claude body.
//
//   node vertex-endpoint-fixture.mjs --app-dir <dir> [--port 8792]
//
// It writes <dir>/config.json (one Vertex key per region, a service account whose token_uri points
// back here, and the proxy plus the CA the gateway must trust), then starts listening. Point a
// gateway at <dir> with -app-dir. GET /__connects reads what was recorded, POST /__reset clears it.

const args = process.argv.slice(2);
const appDir = argValue(args, "--app-dir", "");
const port = Number(argValue(args, "--port", process.env.VERTEX_FIXTURE_PORT || "8792"));
if (!appDir) {
	console.error("usage: node vertex-endpoint-fixture.mjs --app-dir <dir> [--port 8792]");
	process.exit(2);
}

const geminiBody = JSON.stringify({
	candidates: [{ content: { role: "model", parts: [{ text: "hello" }] }, finishReason: "STOP", index: 0 }],
	usageMetadata: { promptTokenCount: 1, candidatesTokenCount: 1, totalTokenCount: 2 },
	modelVersion: "gemini-2.5-flash",
});
const claudeBody = JSON.stringify({
	id: "msg_fixture",
	type: "message",
	role: "assistant",
	model: "claude-opus-4-7",
	content: [{ type: "text", text: "hello" }],
	stop_reason: "end_turn",
	usage: { input_tokens: 1, output_tokens: 1 },
});

// A fresh gateway downloads a pricing datasheet and a model-parameters datasheet on first start, and the model
// catalog drives behaviour these cases assert (a model flagged vertex_multi_region_only is promoted to the multi-region
// pool). Serving both from the control port keeps the run offline and the result independent of a remote file.
const proxy = createInterceptProxy({
	port,
	staticJson: {
		"/pricing.json": {},
		"/model-parameters.json": { "claude-opus-4-7": { provider: "vertex", vertex_multi_region_only: true } },
	},
	// The path decides the body shape.
	respond: (req) => {
		if (/:generateContent/.test(req.url)) return { status: 200, body: geminiBody };
		if (/:rawPredict/.test(req.url)) return { status: 200, body: claudeBody };
		return { status: 404, body: JSON.stringify({ error: { code: 404, message: "fixture does not serve " + req.url, status: "NOT_FOUND" } }) };
	},
	// OAuth token endpoint for the service account below, so no call goes to Google.
	route: (url, req, res) => {
		if (url.pathname !== "/token") return false;
		req.resume();
		req.on("end", () => {
			res.writeHead(200, { "Content-Type": "application/json" });
			res.end(JSON.stringify({ access_token: "fixture-access-token", token_type: "Bearer", expires_in: 3600 }));
		});
		return true;
	},
});

// A service account the OAuth library will accept: a real RSA key to sign the JWT, and a token_uri
// that points at this fixture.
const { privateKey } = crypto.generateKeyPairSync("rsa", { modulusLength: 2048, privateKeyEncoding: { type: "pkcs8", format: "pem" }, publicKeyEncoding: { type: "spki", format: "pem" } });
const serviceAccount = JSON.stringify({
	type: "service_account",
	project_id: "fixture-project",
	private_key_id: "fixture-key-id",
	private_key: privateKey,
	client_email: "fixture@fixture-project.iam.gserviceaccount.com",
	client_id: "1",
	token_uri: `http://127.0.0.1:${port}/token`,
});

const regions = {
	"vertex-global": { region: "global" },
	"vertex-us": { region: "us" },
	"vertex-eu": { region: "eu" },
	"vertex-us-east5": { region: "us-east5" },
	"vertex-us-central1": { region: "us-central1" },
	"vertex-us-central1-pinned": { region: "us-central1", force_single_region: true },
};
const config = {
	framework: { pricing: { pricing_url: `http://127.0.0.1:${port}/pricing.json`, model_parameters_url: `http://127.0.0.1:${port}/model-parameters.json`, mcp_library_sync_interval: 0 } },
	providers: {
		vertex: {
			keys: Object.entries(regions).map(([name, r]) => ({
				name,
				value: "fixture-api-key",
				models: ["*"],
				weight: 1,
				vertex_key_config: { project_id: "fixture-project", region: r.region, auth_credentials: serviceAccount, ...(r.force_single_region ? { force_single_region: true } : {}) },
			})),
			network_config: { max_retries: 0, default_request_timeout_in_seconds: 15 },
			proxy_config: { type: "http", url: `http://127.0.0.1:${port}`, ca_cert_pem: proxy.caPem },
		},
	},
};
fs.mkdirSync(appDir, { recursive: true });
fs.writeFileSync(path.join(appDir, "config.json"), JSON.stringify(config, null, 2));

proxy.listen(() => {
	console.log(`vertex-endpoint-fixture listening on http://127.0.0.1:${port} (config written to ${path.join(appDir, "config.json")})`);
});
