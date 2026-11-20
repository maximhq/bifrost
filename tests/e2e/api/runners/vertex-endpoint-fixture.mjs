import crypto from "node:crypto";
import fs from "node:fs";
import path from "node:path";
import { argValue, createInterceptProxy } from "./lib/tls-intercept-proxy.mjs";

// Local stand-in for Vertex AI, used to observe which host, path and headers the gateway sends.
//
// Vertex hardcodes https://<host>/... with no base-URL override, so the fixture works as the
// gateway's HTTP proxy instead (see lib/tls-intercept-proxy.mjs): it terminates TLS for the
// requested host, records the decrypted request, and replies with a canned Gemini, Claude or OpenAI body, or a finished Veo operation.
//
//   node vertex-endpoint-fixture.mjs --app-dir <dir> [--port 8792]
//
// It writes <dir>/config.json (one Vertex key per region, a service account whose token_uri points
// back here, and the proxy plus the CA the gateway must trust), then starts listening. Point a
// gateway at <dir> with -app-dir. GET /__connects reads what was recorded, POST /__reset clears it.
// Pass ENV_FILE=<dir>/harness-environment.json to make run-provider-harness-test to enable the cases.

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
// A prompt carrying NULL_PART_MARKER gets Gemini replies whose candidate parts hold a JSON
// null beside the text, the shape that used to crash the gateway's response converters.
// Every other request keeps the plain bodies.
const NULL_PART_MARKER = "null-part-fixture";
const geminiUsage = { promptTokenCount: 1, candidatesTokenCount: 1, totalTokenCount: 2 };
const geminiNullPartBody = JSON.stringify({
	candidates: [{ content: { role: "model", parts: [null, { text: "hello" }] }, finishReason: "STOP", index: 0 }],
	usageMetadata: geminiUsage,
	modelVersion: "gemini-2.5-flash",
});
const geminiNullPartStream = [
	{ data: `data: ${JSON.stringify({ candidates: [{ content: { role: "model", parts: [null, { text: "hello" }] }, index: 0 }], modelVersion: "gemini-2.5-flash" })}\r\n\r\n` },
	{ data: `data: ${JSON.stringify({ candidates: [{ content: { role: "model", parts: [null] }, finishReason: "STOP", index: 0 }], usageMetadata: geminiUsage, modelVersion: "gemini-2.5-flash" })}\r\n\r\n` },
];
// A Gemini request sent with the Priority PayGo header is answered as served on Priority, so a case can
// read the served tier back. THROTTLED_MODEL stands for a model whose Standard pool is exhausted: it
// answers 429 RESOURCE_EXHAUSTED unless the request asks for Priority, which is what a routing-rule
// fallback to a priority alias has to produce.
const THROTTLED_MODEL = "gemini-4-carbon";
const geminiPriorityBody = JSON.stringify({
	candidates: [{ content: { role: "model", parts: [{ text: "hello" }] }, finishReason: "STOP", index: 0 }],
	usageMetadata: { ...geminiUsage, trafficType: "ON_DEMAND_PRIORITY" },
	modelVersion: "gemini-2.5-flash",
});
const standardThrottled = JSON.stringify({ error: { code: 429, message: "Resource exhausted. Please try again later.", status: "RESOURCE_EXHAUSTED" } });
const claudeBody = JSON.stringify({
	id: "msg_fixture",
	type: "message",
	role: "assistant",
	model: "claude-opus-4-7",
	content: [{ type: "text", text: "hello" }],
	stop_reason: "end_turn",
	usage: { input_tokens: 1, output_tokens: 1 },
});

const fixtureVideo = Buffer.concat([Buffer.from([0x1a, 0x45, 0xdf, 0xa3]), Buffer.from("fixture-video")]).toString("base64");

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
	respond: (req, body) => {
		if (/\/endpoints\/(2222222222|3333333333)(:|\/)/.test(req.url)) {
			const input = JSON.parse(body);
			const model = req.url.includes("2222222222") ? "1234567890" : "claude-custom";
			if (req.headers.authorization !== "Bearer fixture-access-token" || input.model !== model || !Array.isArray(input.messages) || input.contents || input.anthropic_version) {
				return { status: 400, body: JSON.stringify({ error: { message: "custom endpoint requires OAuth and OpenAI messages with the served model name" } }) };
			}
			const base = { id: "chatcmpl-vertex-custom", created: 1, model };
			const usage = { prompt_tokens: 2, completion_tokens: 1, total_tokens: 3 };
			if (input.stream) {
				return {
					status: 200, headers: { "Content-Type": "text/event-stream" },
					chunks: [
						{ data: `data: ${JSON.stringify({ ...base, object: "chat.completion.chunk", choices: [{ index: 0, delta: { role: "assistant", content: "hello" }, finish_reason: null }] })}\n\n` },
						{ data: `data: ${JSON.stringify({ ...base, object: "chat.completion.chunk", choices: [{ index: 0, delta: {}, finish_reason: "stop" }] })}\n\n` },
						{ data: `data: ${JSON.stringify({ ...base, object: "chat.completion.chunk", choices: [], usage })}\n\ndata: [DONE]\n\n` },
					],
				};
			}
			return { status: 200, body: JSON.stringify({ ...base, object: "chat.completion", choices: [{ index: 0, message: { role: "assistant", content: "hello" }, finish_reason: "stop" }], usage }) };
		}
		if (body.includes(NULL_PART_MARKER)) {
			if (/:streamGenerateContent/.test(req.url)) return { status: 200, headers: { "Content-Type": "text/event-stream" }, chunks: geminiNullPartStream };
			if (/:generateContent/.test(req.url)) return { status: 200, body: geminiNullPartBody };
		}
		if (/:(generateContent|streamGenerateContent)/.test(req.url)) {
			const priority = req.headers["x-vertex-ai-llm-shared-request-type"] === "priority";
			if (!priority && req.url.includes(`/models/${THROTTLED_MODEL}:`)) return { status: 429, body: standardThrottled };
			if (/:streamGenerateContent/.test(req.url)) return { status: 200, headers: { "Content-Type": "text/event-stream" }, chunks: [{ data: `data: ${priority ? geminiPriorityBody : geminiBody}\r\n\r\n` }] };
			return { status: 200, body: priority ? geminiPriorityBody : geminiBody };
		}
		if (/:rawPredict/.test(req.url)) return { status: 200, body: claudeBody };
		// The video is inline so download needs no second fetch; the WebM magic marks it binary to the harness shape check.
		if (/:fetchPredictOperation/.test(req.url)) return { status: 200, body: JSON.stringify({ name: (body.match(/"operationName":"([^"]+)"/) || [])[1], done: true, response: { videos: [{ bytesBase64Encoded: fixtureVideo, mimeType: "video/webm" }] } }) };
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
// Keys scoped to gemini-4 models, which no other case uses: one carries a key-level tier, and a
// standard/priority pair backs the routing rule below, whose fallback moves a throttled
// gemini-4-carbon request to the priority alias.
const tierKeys = {
	"vertex-us-tiered": { models: ["gemini-4-argon"], service_tier: "priority" },
	"vertex-us-carbon-standard": { models: [THROTTLED_MODEL] },
	"vertex-us-carbon-priority": { models: [`${THROTTLED_MODEL}-priority`], aliases: { [`${THROTTLED_MODEL}-priority`]: { model_id: THROTTLED_MODEL, service_tier: "priority" } } },
};
const config = {
	// The harness runner unlocks /api with this token (its default) before a run.
	setup_token: "bifrost-e2e-setup-token",
	framework: { pricing: { pricing_url: `http://127.0.0.1:${port}/pricing.json`, model_parameters_url: `http://127.0.0.1:${port}/model-parameters.json`, mcp_library_sync_interval: 0 } },
	providers: {
		vertex: {
			keys: Object.entries(regions).map(([name, r]) => ({
				name,
				value: "fixture-api-key",
				models: ["*"],
				weight: 1,
				...(name === "vertex-us-central1" ? {
					aliases: {
						"vertex-openai-raw": { model_id: "1234567890", model_family: "gemini", project_id: "custom-project", region: "us-west1", vertex_endpoint: { endpoint_id: "2222222222", format: "openai_chat", method: "raw_predict" } },
						"vertex-openai-chat": { model_id: "claude-custom", model_family: "anthropic", project_id: "custom-project", region: "us-west1", vertex_endpoint: { endpoint_id: "3333333333", format: "openai_chat", method: "chat_completions" } },
					},
				} : {}),
				vertex_key_config: { project_id: "fixture-project", region: r.region, auth_credentials: serviceAccount, ...(r.force_single_region ? { force_single_region: true } : {}) },
			})).concat(Object.entries(tierKeys).map(([name, k]) => ({
				name,
				value: "fixture-api-key",
				weight: 1,
				...k,
				vertex_key_config: { project_id: "fixture-project", region: "us", auth_credentials: serviceAccount },
			}))),
			// The customer's retry budget: a throttled Standard request is retried this many times before
			// its routing-rule fallback runs. Backoff stays short so a run is quick; only THROTTLED_MODEL is retried.
			network_config: { max_retries: 10, retry_backoff_initial: 10, retry_backoff_max: 50, default_request_timeout_in_seconds: 15 },
			proxy_config: { type: "http", url: `http://127.0.0.1:${port}`, ca_cert_pem: proxy.caPem },
		},
	},
	// The routing rule needs the governance section, which would otherwise require a virtual key on every request.
	client: { enforce_auth_on_inference: false },
	governance: {
		routing_rules: [{
			name: "carbon-priority-overflow",
			cel_expression: `model == "${THROTTLED_MODEL}"`,
			targets: [{ provider: "vertex", model: THROTTLED_MODEL, provider_key_name: "vertex-us-carbon-standard", weight: 1 }],
			fallbacks: [{ provider: "vertex", model: `${THROTTLED_MODEL}-priority`, provider_key_name: "vertex-us-carbon-priority" }],
			scope: "global",
			priority: 0,
		}],
	},
};
fs.mkdirSync(appDir, { recursive: true });
fs.writeFileSync(path.join(appDir, "config.json"), JSON.stringify(config, null, 2));
fs.writeFileSync(path.join(appDir, "harness-environment.json"), JSON.stringify({
	name: "Vertex endpoint fixture",
	values: [{ key: "vertexEndpointFixture", value: "1", enabled: true }],
	_postman_variable_scope: "environment",
}, null, 2));

proxy.listen(() => {
	console.log(`vertex-endpoint-fixture listening on http://127.0.0.1:${port} (config written to ${path.join(appDir, "config.json")})`);
});
