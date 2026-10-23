import http from "node:http";

// Local-only Streamable HTTP MCP fixture. Each operation commits before its
// first response is replaced with a 503. Counts are independent of JSON-RPC IDs:
// assigning a new request ID is not a deduplication mechanism.
const port = Number(process.env.MCP_REPLAY_FIXTURE_PORT || "8793");
const policies = {
	unknown: undefined,
	additive: { readOnlyHint: false, destructiveHint: false, idempotentHint: false },
	readonly: { readOnlyHint: true },
	idempotent: { readOnlyHint: false, destructiveHint: true, idempotentHint: true },
};
const operations = new Map();
const json = (res, status, body) => {
	res.writeHead(status, { "Content-Type": "application/json" });
	res.end(JSON.stringify(body));
};
const server = http.createServer(async (req, res) => {
	const url = new URL(req.url, "http://127.0.0.1");
	if (url.pathname === "/stats") {
		if (req.method === "DELETE") operations.clear();
		else if (req.method !== "GET") return json(res, 405, { error: "method not allowed" });
		return json(res, 200, { data: [...operations.values()] });
	}
	// Keep the isolated gateway's catalog bootstrap local, with no providers.
	if (url.pathname === "/pricing.json" || url.pathname === "/model-parameters.json") {
		return json(res, 200, {});
	}
	if (url.pathname !== "/mcp") return json(res, 404, { error: "not found" });
	if (req.method === "GET") return res.writeHead(405).end(); // JSON responses, no SSE listener
	if (req.method === "DELETE") return res.writeHead(200).end();
	if (req.method !== "POST") return res.writeHead(405).end();
	try {
		let raw = "";
		for await (const chunk of req) {
			raw += chunk;
			if (raw.length > 65536) return json(res, 413, { error: "request too large" });
		}
		const rpc = JSON.parse(raw);
		if (rpc.id === undefined) return res.writeHead(202).end();
		let result;
		switch (rpc.method) {
			case "initialize":
				result = { protocolVersion: rpc.params.protocolVersion, capabilities: { tools: {} }, serverInfo: { name: "mcp-replay-fixture", version: "1" } };
				break;
			case "tools/list":
				result = { tools: Object.entries(policies).map(([name, annotations]) => ({ name, description: `Local ${name} replay control`, inputSchema: { type: "object", properties: { operation: { type: "string" } }, required: ["operation"] }, ...(annotations ? { annotations } : {}) })) };
				break;
			case "ping": result = {}; break;
			case "tools/call": {
				const { name, arguments: args } = rpc.params;
				if (!Object.hasOwn(policies, name) || typeof args?.operation !== "string") return json(res, 400, { error: "unknown tool or missing operation" });
				const key = `${name}:${args.operation}`;
				if (!operations.has(key)) {
					if (operations.size >= 128) return json(res, 429, { error: "fixture full; DELETE /stats before a run" });
					operations.set(key, { tool: name, operation: args.operation, invocations: 0, commits: 0, request_ids: [] });
				}
				const entry = operations.get(key);
				entry.invocations++;
				entry.request_ids.push(rpc.id);
				if (name !== "readonly" && (name !== "idempotent" || entry.commits === 0)) entry.commits++;
				console.log(JSON.stringify(entry));
				if (entry.invocations === 1) {
					res.writeHead(503, { "Content-Type": "text/plain" });
					return res.end("upstream response unavailable after commit");
				}
				result = { content: [{ type: "text", text: `operation=${args.operation} commits=${entry.commits}` }], isError: false };
				break;
			}
			default: return json(res, 200, { jsonrpc: "2.0", id: rpc.id, error: { code: -32601, message: "method not found" } });
		}
		json(res, 200, { jsonrpc: "2.0", id: rpc.id, result });
	} catch (err) {
		json(res, 400, { error: err.message });
	}
});
server.listen(port, "127.0.0.1", () => console.log(`MCP replay fixture listening on http://127.0.0.1:${port}`));
for (const signal of ["SIGINT", "SIGTERM"]) process.on(signal, () => server.close());
