import fs from "node:fs";
import path from "node:path";
import { argValue, createTlsEndpoints } from "./lib/tls-intercept-proxy.mjs";

// Local stand-in for the AWS Bedrock services, used to observe what the gateway sends for the
// operations it forwards through /bedrock_passthrough: InvokeAgent and knowledge-base Retrieve
// (bedrock-agent-runtime) and ApplyGuardrail (bedrock-runtime).
//
// The Bedrock provider honours per-service endpoint overrides and a trusted CA but not
// proxy_config, so the fixture is a TLS server the gateway is pointed at directly (see
// lib/tls-intercept-proxy.mjs). Every Bedrock service endpoint is overridden - not only the two
// the cases use - so nothing, not even a startup model listing, can reach AWS. Run the gateway with
// HTTPS_PROXY set to a dead port as a tripwire: loopback is exempt from proxying, so only an
// accidental external call would fail.
//
//   node bedrock-endpoint-fixture.mjs --app-dir <dir> [--port 8794]
//
// Ports: <port> is the plain-HTTP control endpoint (GET /__connects, POST /__reset); <port>+2 is the
// bedrock-agent-runtime TLS endpoint and <port>+4 is every other Bedrock service (bedrock-runtime,
// control plane, mantle, S3), so a case can tell which service the gateway chose from the port.

const args = process.argv.slice(2);
const appDir = argValue(args, "--app-dir", "");
const controlPort = Number(argValue(args, "--port", process.env.BEDROCK_FIXTURE_PORT || "8794"));
const agentPort = controlPort + 2;
const runtimePort = controlPort + 4;
if (!appDir) {
	console.error("usage: node bedrock-endpoint-fixture.mjs --app-dir <dir> [--port 8794]");
	process.exit(2);
}

// A real event stream starts with a big-endian 4-byte message length, so the first byte is a
// control character; the fixture answers InvokeAgent with bytes shaped like that.
const eventStream = Buffer.concat([Buffer.from([0, 0, 0, 24, 0, 0, 0, 0, 0, 0, 0, 0]), Buffer.from("fixture-event")]);

// The gateway needs a pricing datasheet on first start; serving an empty one from the control port keeps
// the whole run offline, so the HTTPS_PROXY tripwire can stay armed.
const endpoints = createTlsEndpoints({
	staticJson: { "/pricing.json": {}, "/model-parameters.json": {} },
	respond: (req) => {
		const url = req.url || "";
		if (/\/agents\/AGENTMISSING1\//.test(url)) {
			return { status: 404, headers: { "X-Amzn-Errortype": "ResourceNotFoundException:http://internal.amazon.com/coral/", "X-Amzn-Requestid": "req-missing" }, body: JSON.stringify({ message: "agent not found" }) };
		}
		// A slow agent run: five event-stream pieces one second apart, 5s in all. The gateway below is configured
		// with a 3s default request timeout, so only a path that streams and has no overall deadline gets through.
		if (/\/agents\/AGENTSLOW001\//.test(url)) {
			return { status: 200, headers: { "Content-Type": "application/vnd.amazon.eventstream", "X-Amzn-Requestid": "req-slow" }, chunks: [1, 2, 3, 4, 5].map((n) => ({ delayMs: n === 1 ? 0 : 1000, data: Buffer.concat([Buffer.from([0, 0, 0, 24]), Buffer.from(`slow-chunk-${n}|`)]) })) };
		}
		if (/\/agents\/[^/]+\/agentAliases\/[^/]+\/sessions\/[^/]+\/text$/.test(url)) {
			return { status: 200, headers: { "Content-Type": "application/vnd.amazon.eventstream", "X-Amzn-Requestid": "req-agent" }, body: eventStream };
		}
		// A slow Retrieve on a knowledge base whose id contains "stream": 4s in all, longer than the 3s default request
		// timeout. Retrieve is a plain request/response call, so that timeout must cut it off; a router fooled by the
		// word in the id would hand it to the streaming path, which has no overall deadline, and let it through.
		if (/\/knowledgebases\/STREAMSLOWKB\/retrieve$/.test(url)) {
			return { status: 200, headers: { "Content-Type": "application/json", "X-Amzn-Requestid": "req-kb-slow" }, chunks: [{ delayMs: 0, data: Buffer.from('{"retrievalResults":[') }, { delayMs: 4000, data: Buffer.from("]}") }] };
		}
		if (/\/knowledgebases\/[^/]+\/retrieve$/.test(url)) {
			return { status: 200, headers: { "X-Amzn-Requestid": "req-kb" }, body: JSON.stringify({ retrievalResults: [{ content: { text: "fixture chunk" }, score: 0.9 }] }) };
		}
		if (/\/guardrail\/[^/]+\/version\/[^/]+\/apply$/.test(url)) {
			return { status: 200, headers: { "X-Amzn-Requestid": "req-guardrail" }, body: JSON.stringify({ action: "NONE", outputs: [], assessments: [] }) };
		}
		return { status: 404, body: JSON.stringify({ message: "fixture does not serve " + url }) };
	},
});

const agentHost = `localhost:${agentPort}`;
const runtimeHost = `localhost:${runtimePort}`;
// The S3 endpoint value must carry the literal "bucket." prefix (see endpoints.s3 in config.schema.json): Bifrost
// builds https://<bucket>.<value>/<key>, so this resolves to <bucket>.bucket.localhost, covered by the wildcard
// names on the fixture certificate.
const s3Host = `bucket.${runtimeHost}`;
const config = {
	framework: { pricing: { pricing_url: `http://127.0.0.1:${controlPort}/pricing.json`, model_parameters_url: `http://127.0.0.1:${controlPort}/model-parameters.json`, mcp_library_sync_interval: 0 } },
	providers: {
		bedrock: {
			keys: [
				{
					name: "bedrock-fixture",
					models: ["*"],
					weight: 1,
					bedrock_key_config: {
						access_key: "AKIAFIXTUREEXAMPLE0",
						secret_key: "fixture-secret-key-not-real",
						region: "us-west-2",
						endpoints: { runtime: runtimeHost, control_plane: runtimeHost, mantle: runtimeHost, s3: s3Host, agent_runtime: agentHost },
					},
				},
				// An API-key-only key, for the operations AWS does not accept API keys on. Weight 0 keeps it out of random key
				// selection; a case picks it by name with x-bf-api-key.
				{
					name: "bedrock-api-key-only",
					value: "fixture-bedrock-api-key",
					models: ["*"],
					weight: 0,
					bedrock_key_config: {
						region: "us-west-2",
						endpoints: { runtime: runtimeHost, control_plane: runtimeHost, mantle: runtimeHost, s3: s3Host, agent_runtime: agentHost },
					},
				},
			],
			network_config: { max_retries: 0, default_request_timeout_in_seconds: 3, ca_cert_pem: endpoints.caPem },
		},
	},
};
fs.mkdirSync(appDir, { recursive: true });
fs.writeFileSync(path.join(appDir, "config.json"), JSON.stringify(config, null, 2));

let ready = 0;
const up = () => {
	if (++ready === 3) console.log(`bedrock-endpoint-fixture listening: control http://127.0.0.1:${controlPort}, agent-runtime https://${agentHost}, runtime https://${runtimeHost} (config written to ${path.join(appDir, "config.json")})`);
};
endpoints.listenEndpoint(agentPort, up);
endpoints.listenEndpoint(runtimePort, up);
endpoints.listenControl(controlPort, up);
