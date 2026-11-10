import { argValue, createInterceptProxy } from "./lib/tls-intercept-proxy.mjs";

// Local stand-in for a Databricks workspace's OAuth token endpoint, so the harness can pin how a
// rejected service principal surfaces without depending on a live host's answer.
//
// Databricks builds its token URL as https://<workspace>/oidc/v1/token with no override, so the
// fixture works as the gateway's HTTP proxy (see lib/tls-intercept-proxy.mjs): it terminates TLS for
// whatever workspace host the gateway CONNECTs to and answers the token request with 401
// invalid_client, a permanent credential fault that must block fallbacks. Every other path is 404.
//
// One host is different: SCOPED_HOST stands in for a workspace whose service principal secret was
// created with only the model-serving-inference scope. Its token endpoint refuses any requested
// scope outside that set with the 403 access_denied Databricks sends, and mints a token
// otherwise; its chat completions endpoint answers a canned reply for that token. A gateway
// that asks for all-apis and stops at the 403 fails the harness row; one that falls back to
// model-serving-inference is served.
//
//   node databricks-token-fixture.mjs [--port 8795]
//
// The harness row configures the proxy itself: it reads the CA from GET /__ca on this port, then
// creates the databricks provider with proxy_config pointing here and trusting that CA.
// GET /__connects reads what was recorded, POST /__reset clears it.

const args = process.argv.slice(2);
const port = Number(argValue(args, "--port", process.env.DATABRICKS_TOKEN_FIXTURE_PORT || "8795"));

const SCOPED_HOST = "dbx-scoped-workspace.invalid";
const SCOPED_GRANTED = ["model-serving-inference"];
const SCOPED_TOKEN = "fixture-scoped-token";

const respondScoped = (req, body, path) => {
	if (req.method === "POST" && path === "/oidc/v1/token") {
		const requested = (new URLSearchParams(body).get("scope") || "").split(" ").filter(Boolean);
		const missing = requested.filter((s) => !SCOPED_GRANTED.includes(s));
		if (requested.length === 0 || missing.length > 0) {
			return {
				status: 403,
				body: JSON.stringify({
					error: "access_denied",
					// The shape a live workspace sent for this secret: no list of the scopes it has.
					error_description: `Scopes '${missing.join(" ")}' are not assigned to the client harness-service-principal`,
				}),
			};
		}
		return { status: 200, body: JSON.stringify({ access_token: SCOPED_TOKEN, token_type: "Bearer", expires_in: 3600, scope: requested.join(" ") }) };
	}
	if (req.method === "POST" && path === "/serving-endpoints/chat/completions") {
		if (req.headers.authorization !== "Bearer " + SCOPED_TOKEN) {
			return { status: 403, body: JSON.stringify({ error_code: "PERMISSION_DENIED", message: "fixture expects the scoped token" }) };
		}
		return {
			status: 200,
			body: JSON.stringify({
				id: "chatcmpl-fixture",
				object: "chat.completion",
				created: 1700000000,
				model: "databricks-claude-sonnet-4-5",
				choices: [{ index: 0, message: { role: "assistant", content: "OK from the scoped fixture" }, finish_reason: "stop" }],
				usage: { prompt_tokens: 3, completion_tokens: 5, total_tokens: 8 },
			}),
		};
	}
	return null;
};

const proxy = createInterceptProxy({
	port,
	respond: (req, body) => {
		const path = new URL(req.url, "https://fixture").pathname;
		if (req.socket.fixtureHost === SCOPED_HOST) {
			const scoped = respondScoped(req, body, path);
			if (scoped) return scoped;
		}
		if (req.method === "POST" && path === "/oidc/v1/token") {
			return { status: 401, body: JSON.stringify({ error: "invalid_client", error_description: "fixture rejects every client" }) };
		}
		return { status: 404, body: JSON.stringify({ error: "not_found", error_description: "fixture does not serve " + req.url }) };
	},
	route: (url, req, res) => {
		if (url.pathname !== "/__ca") return false;
		req.resume();
		res.writeHead(200, { "Content-Type": "application/x-pem-file" });
		res.end(proxy.caPem);
		return true;
	},
});

proxy.listen(() => {
	console.log(`databricks token fixture listening on http://127.0.0.1:${port} (proxy; CA at /__ca)`);
});
