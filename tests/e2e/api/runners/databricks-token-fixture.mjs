import { argValue, createInterceptProxy } from "./lib/tls-intercept-proxy.mjs";

// Local stand-in for a Databricks workspace's OAuth token endpoint, so the harness can pin how a
// rejected service principal surfaces without depending on a live host's answer.
//
// Databricks builds its token URL as https://<workspace>/oidc/v1/token with no override, so the
// fixture works as the gateway's HTTP proxy (see lib/tls-intercept-proxy.mjs): it terminates TLS for
// whatever workspace host the gateway CONNECTs to and answers the token request with 401
// invalid_client, a permanent credential fault that must block fallbacks. Every other path is 404.
//
//   node databricks-token-fixture.mjs [--port 8795]
//
// The harness row configures the proxy itself: it reads the CA from GET /__ca on this port, then
// creates the databricks provider with proxy_config pointing here and trusting that CA.
// GET /__connects reads what was recorded, POST /__reset clears it.

const args = process.argv.slice(2);
const port = Number(argValue(args, "--port", process.env.DATABRICKS_TOKEN_FIXTURE_PORT || "8795"));

const proxy = createInterceptProxy({
	port,
	respond: (req) => {
		if (req.method === "POST" && new URL(req.url, "https://fixture").pathname === "/oidc/v1/token") {
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
