import http from "node:http";

// Local upstream for the Relays harness folder (relays-e2e). The shared profile's relays
// (tests/integrations/python/config.json, harness-relay-*) point here, so every relay case runs
// against a deterministic local API and never reaches a third party.
//
// Every reply is JSON carrying a `data` array (the harness's generic content check reads it) and,
// where a route is priced, a `usage` block with fixed numbers. Each request's `tag` query parameter
// is counted, so a case can prove a refused call never reached the upstream:
//
//   GET /__relay/hits?tag=<tag>   {"tag": "...", "hits": <n>, "data": []}  (never relayed: no route)
//   GET /__relay/health           {"ok": true, "fixture": "relay-upstream", "data": []}  (identifies a reusable listener)
//
// Routes (any other path echoes the request, which the skip_route_check relay relies on):
//   POST /v1/search      usage.credits 4, usage.input_tokens 50, usage.output_tokens 20
//   POST /v1/extract     usage.credits 2
//   GET  /v1/status      usage.credits 9 (relayed by a route that is not billable)
//   POST /v1/free        usage.credits 5 (relayed by a route with billable: false)
//   POST /v1/fail        HTTP 500 with usage.credits 5 (a non-2xx reply is never charged)
//   POST /v1/stream      SSE: credits 1 on the first event, none on the second, 3 on the last
//   POST /v1/pages       usage.chars = the request body's "chars" (block and minimum pricing)
//   POST /v1/reported    usage.cost 0.005 (a provider-reported cost)
//   *    /v1/echo, other the method, path, query and headers the upstream received
const port = Number(process.env.RELAY_UPSTREAM_FIXTURE_PORT || "8783");

const hits = new Map();

const send = (res, status, body, headers = {}) => {
	res.writeHead(status, { "Content-Type": "application/json", ...headers });
	res.end(JSON.stringify(body));
};

const readBody = (req) =>
	new Promise((resolve) => {
		const chunks = [];
		req.on("data", (c) => chunks.push(c));
		req.on("end", () => resolve(Buffer.concat(chunks).toString("utf8")));
		req.on("error", () => resolve(""));
	});

const server = http.createServer(async (req, res) => {
	const url = new URL(req.url || "/", "http://127.0.0.1");
	const path = url.pathname;
	const raw = await readBody(req);

	if (path === "/__relay/hits") {
		const tag = url.searchParams.get("tag") || "";
		send(res, 200, { tag, hits: hits.get(tag) || 0, data: [] });
		return;
	}
	if (path === "/__relay/health") {
		send(res, 200, { ok: true, fixture: "relay-upstream", data: [] });
		return;
	}

	const tag = url.searchParams.get("tag");
	if (tag) hits.set(tag, (hits.get(tag) || 0) + 1);

	let body = {};
	try {
		body = raw ? JSON.parse(raw) : {};
	} catch {
		body = {};
	}

	switch (`${req.method} ${path}`) {
		case "POST /v1/search":
			send(res, 200, { data: [{ title: "bifrost" }, { title: "relay" }], usage: { credits: 4, input_tokens: 50, output_tokens: 20 } });
			return;
		case "POST /v1/extract":
			send(res, 200, { data: [{ text: "extracted" }], usage: { credits: 2 } });
			return;
		case "GET /v1/status":
			send(res, 200, { data: [], ok: true, usage: { credits: 9 } });
			return;
		case "POST /v1/free":
			send(res, 200, { data: [], usage: { credits: 5 } });
			return;
		case "POST /v1/fail":
			send(res, 500, { error: { message: "upstream failure" }, usage: { credits: 5 } });
			return;
		case "POST /v1/stream":
			res.writeHead(200, { "Content-Type": "text/event-stream", "Cache-Control": "no-cache" });
			res.write(`data: ${JSON.stringify({ delta: "a", usage: { credits: 1 } })}\n\n`);
			res.write(`data: ${JSON.stringify({ delta: "b" })}\n\n`);
			res.write(`data: ${JSON.stringify({ delta: "c", usage: { credits: 3 } })}\n\n`);
			res.end();
			return;
		case "POST /v1/pages":
			send(res, 200, { data: [{ page: 1 }], usage: { chars: Number(body.chars) || 0 } });
			return;
		case "POST /v1/reported":
			send(res, 200, { data: [], usage: { cost: 0.005 } });
			return;
		default:
			send(res, 200, {
				data: [],
				method: req.method,
				path,
				query: Object.fromEntries(url.searchParams.entries()),
				headers: req.headers,
				body: raw,
			});
	}
});

server.listen(port, "127.0.0.1", () => {
	console.log(`relay-upstream-fixture listening on http://127.0.0.1:${port}`);
});

const stop = () => server.close(() => process.exit(0));
process.on("SIGTERM", stop);
process.on("SIGINT", stop);
