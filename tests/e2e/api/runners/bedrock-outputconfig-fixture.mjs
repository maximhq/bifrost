import fs from "node:fs";
import path from "node:path";
import { argValue, createTlsEndpoints } from "./lib/tls-intercept-proxy.mjs";

// Local wire witness for the native Converse outputConfig conversion. This is a serializer
// regression fixture, not an implementation of AWS schema validation or model enforcement.
// Every service endpoint and the startup datasheets are local; the credentials are throwaway.
//   node bedrock-outputconfig-fixture.mjs --app-dir <dir> [--port 8804]
// The control endpoint is <port>; the Bedrock TLS endpoint is <port>+2.

const args = process.argv.slice(2);
const appDir = argValue(args, "--app-dir", "");
const controlPort = Number(argValue(args, "--port", "8804"));
const runtimePort = controlPort + 2;
if (!appDir) {
	console.error("usage: node bedrock-outputconfig-fixture.mjs --app-dir <dir> [--port 8804]");
	process.exit(2);
}

// AWS EventStream validates both the prelude and full-message CRC32.
function crc32(bytes) {
	let crc = 0xffffffff;
	for (const byte of bytes) {
		crc ^= byte;
		for (let bit = 0; bit < 8; bit++) crc = (crc >>> 1) ^ ((crc & 1) ? 0xedb88320 : 0);
	}
	return (crc ^ 0xffffffff) >>> 0;
}
function frame(type, payload) {
	const headers = Buffer.concat([
		[":message-type", "event"], [":event-type", type], [":content-type", "application/json"],
	].map(([name, value]) => {
		const key = Buffer.from(name);
		const text = Buffer.from(value);
		const header = Buffer.alloc(1 + key.length + 3 + text.length);
		header[0] = key.length;
		key.copy(header, 1);
		header[1 + key.length] = 7;
		header.writeUInt16BE(text.length, 2 + key.length);
		text.copy(header, 4 + key.length);
		return header;
	}));
	const body = Buffer.from(JSON.stringify(payload));
	const bytes = Buffer.alloc(16 + headers.length + body.length);
	bytes.writeUInt32BE(bytes.length);
	bytes.writeUInt32BE(headers.length, 4);
	bytes.writeUInt32BE(crc32(bytes.subarray(0, 8)), 8);
	headers.copy(bytes, 12);
	body.copy(bytes, 12 + headers.length);
	bytes.writeUInt32BE(crc32(bytes.subarray(0, -4)), bytes.length - 4);
	return bytes;
}
const stream = Buffer.concat([
	frame("messageStart", { role: "assistant" }),
	frame("contentBlockDelta", { contentBlockIndex: 0, delta: { text: "hello" } }),
	frame("contentBlockStop", { contentBlockIndex: 0 }),
	frame("messageStop", { stopReason: "end_turn" }),
	frame("metadata", { usage: { inputTokens: 1, outputTokens: 1, totalTokens: 2 }, metrics: { latencyMs: 1 } }),
]);
const endpoints = createTlsEndpoints({
	staticJson: { "/pricing.json": {}, "/model-parameters.json": {} },
	respond: (req, body) => {
		if (req.method === "GET" && req.url === "/foundation-models") {
			return { status: 200, body: JSON.stringify({ modelSummaries: [] }) };
		}
		if (req.method !== "POST" || !/^\/model\/output-config-fixture\/converse(-stream)?$/.test(req.url || "")) {
			return { status: 404, body: JSON.stringify({ message: "outputConfig fixture does not serve " + req.url }) };
		}
		let request;
		try { request = JSON.parse(body); } catch {
			return { status: 400, body: JSON.stringify({ message: "fixture received invalid JSON" }) };
		}
		if (request.outputConfig && request.outputConfig.textFormat && request.outputConfig.textFormat.type !== "json_schema") {
			return {
				status: 400,
				headers: { "X-Amzn-Errortype": "ValidationException", "X-Amzn-Requestid": "output-config-invalid" },
				body: JSON.stringify({ message: "outputConfig.textFormat.type must be json_schema (fixture validation)" }),
			};
		}
		if ((req.url || "").endsWith("/converse-stream")) {
			return { status: 200, headers: { "Content-Type": "application/vnd.amazon.eventstream" }, body: stream };
		}
		return {
			status: 200,
			body: JSON.stringify({
				output: { message: { role: "assistant", content: [{ text: "hello" }] } },
				stopReason: "end_turn",
				usage: { inputTokens: 1, outputTokens: 1, totalTokens: 2 },
				metrics: { latencyMs: 1 },
			}),
		};
	},
});
const runtimeHost = "localhost:" + runtimePort;
const config = {
	setup_token: "bifrost-e2e-setup-token",
	framework: { pricing: { pricing_url: "http://127.0.0.1:" + controlPort + "/pricing.json", model_parameters_url: "http://127.0.0.1:" + controlPort + "/model-parameters.json", mcp_library_sync_interval: 0 } },
	providers: {
		bedrock: {
			keys: [{
				name: "bedrock-output-config-fixture", models: ["*"], weight: 1,
				bedrock_key_config: {
					access_key: "AKIAFIXTUREEXAMPLE0", secret_key: "fixture-secret-key-not-real", region: "us-west-2",
					endpoints: { runtime: runtimeHost, control_plane: runtimeHost, mantle: runtimeHost, s3: "bucket." + runtimeHost, agent_runtime: runtimeHost },
				},
			}],
			network_config: { max_retries: 0, default_request_timeout_in_seconds: 3, ca_cert_pem: endpoints.caPem },
		},
	},
};
fs.mkdirSync(appDir, { recursive: true });
fs.writeFileSync(path.join(appDir, "config.json"), JSON.stringify(config, null, 2));
fs.writeFileSync(path.join(appDir, "environment.json"), JSON.stringify({
	name: "Bedrock outputConfig local fixture",
	values: [
		{ key: "bedrockOutputConfigFixture", value: "1", enabled: true },
		{ key: "bedrockOutputConfigControlUrl", value: "http://127.0.0.1:" + controlPort, enabled: true },
	],
}, null, 2));
let ready = 0;
const up = () => {
	if (++ready === 2) console.log("bedrock-outputconfig-fixture listening: control http://127.0.0.1:" + controlPort + ", runtime https://" + runtimeHost);
};
endpoints.listenEndpoint(runtimePort, up);
endpoints.listenControl(controlPort, up);
