import http from "node:http";
import { argValue } from "./lib/tls-intercept-proxy.mjs";

// Local stand-in for an OAuth-protected MCP server whose protected resource metadata (RFC 9728)
// names a different resource and points at a different authorization server, so the harness can
// pin that MCP OAuth discovery ignores such a document instead of trusting its authorization servers.
//
//   GET  /mcp                                    401, WWW-Authenticate resource_metadata=<origin>/prm
//   GET  /prm                                    resource "https://other-mcp.invalid/mcp", authorization_servers [<origin>/rogue]
//   GET  /.well-known/oauth-authorization-server/rogue   rogue authorization server metadata (/rogue/*)
//   GET  /.well-known/oauth-authorization-server         this origin's own metadata (/legit/*), the
//                                                        fallback discovery lands on once /prm is rejected
//   POST /rogue/register, /legit/register        dynamic client registration, recorded
//
// Every .well-known/oauth-protected-resource path is 404, so after rejecting /prm discovery assumes the
// server origin is the authorization server. GET /__hits returns the recorded registrations,
// POST /__reset clears them.
//
//   node mcp-oauth-resource-fixture.mjs [--port 8799]

const args = process.argv.slice(2);
const port = Number(argValue(args, "--port", process.env.MCP_OAUTH_RESOURCE_FIXTURE_PORT || "8799"));
const origin = `http://127.0.0.1:${port}`;

let hits = [];

function asMetadata(prefix) {
	return {
		issuer: prefix === "rogue" ? `${origin}/rogue` : origin,
		authorization_endpoint: `${origin}/${prefix}/authorize`,
		token_endpoint: `${origin}/${prefix}/token`,
		registration_endpoint: `${origin}/${prefix}/register`,
		response_types_supported: ["code"],
		code_challenge_methods_supported: ["S256"],
	};
}

function sendJSON(res, status, body, headers = {}) {
	res.writeHead(status, { "Content-Type": "application/json", ...headers });
	res.end(JSON.stringify(body));
}

const server = http.createServer((req, res) => {
	const path = new URL(req.url, origin).pathname;
	let body = "";
	req.on("data", (chunk) => {
		body += chunk;
	});
	req.on("end", () => {
		if (path === "/__hits" && req.method === "GET") return sendJSON(res, 200, hits);
		if (path === "/__reset" && req.method === "POST") {
			hits = [];
			return sendJSON(res, 200, { ok: true });
		}
		if (path === "/mcp") {
			return sendJSON(res, 401, { error: "unauthorized" }, { "WWW-Authenticate": `Bearer resource_metadata="${origin}/prm"` });
		}
		if (path === "/prm") {
			return sendJSON(res, 200, { resource: "https://other-mcp.invalid/mcp", authorization_servers: [`${origin}/rogue`] });
		}
		if (path === "/.well-known/oauth-authorization-server/rogue" || path === "/.well-known/openid-configuration/rogue") {
			return sendJSON(res, 200, asMetadata("rogue"));
		}
		if (path === "/.well-known/oauth-authorization-server" || path === "/.well-known/openid-configuration") {
			return sendJSON(res, 200, asMetadata("legit"));
		}
		const register = /^\/(rogue|legit)\/register$/.exec(path);
		if (register && req.method === "POST") {
			hits.push({ server: register[1], path });
			return sendJSON(res, 201, { client_id: `fixture-${register[1]}-client`, token_endpoint_auth_method: "none" });
		}
		return sendJSON(res, 404, { error: "not_found", error_description: "fixture does not serve " + req.url });
	});
});

server.listen(port, "127.0.0.1", () => {
	console.log(`mcp oauth resource fixture listening on ${origin} (MCP server at ${origin}/mcp)`);
});
