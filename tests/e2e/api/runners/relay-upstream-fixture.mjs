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
//   *    /v1/gated/**     Tavily-shaped: the echo below, plus usage.credits 3 only when the body
//                        carries "include_usage": true (the harness-relay-forced relay forces it);
//                        /v1/gated/stream answers the same as SSE, with credits 1 on the first event
//   POST /v1/textract    Textract-shaped async jobs (harness-relay-jobs), by X-Amz-Target:
//                        Textract.StartDocumentAnalysis {Scenario, ClientRequestToken} -> {JobId}; a
//                        repeated token returns the same id. Textract.GetDocumentAnalysis {JobId}
//                        walks the scenario: ok (IN_PROGRESS twice, then SUCCEEDED with 3 pages), failed,
//                        throttle (one 400 ThrottlingException, then SUCCEEDED with 2 pages),
//                        nousage (SUCCEEDED with no page count)
//   POST /v1/rest/jobs   {"id": "<id>"} -> echoes that id as the job id, to try ids a relay must refuse
//   GET  /__relay/jobs?id=<id>  {"id", "polls", "scenario", "data": []}
//   GET  /__relay/escaped       {"hits": <n>}: requests whose raw path left /v1/rest/jobs/
//   *    /v1/echo, other the method, path, query and headers the upstream received
const port = Number(process.env.RELAY_UPSTREAM_FIXTURE_PORT || "8783");
// Loopback by default. The proxy matrix binds every interface, because its relays address the
// fixture by the host's own IPv4: a loopback target never goes through an environment proxy.
const host = process.env.RELAY_UPSTREAM_FIXTURE_HOST || "127.0.0.1";

const hits = new Map();
const jobs = new Map();
const tokens = new Map();
let jobSeq = 0;
let escaped = 0;

// One poll of a Textract-shaped job, advancing its scenario.
const pollJob = (job) => {
	job.polls++;
	switch (job.scenario) {
		case "failed":
			return [200, { JobStatus: "FAILED", StatusMessage: "unsupported document" }];
		case "nousage":
			return [200, { JobStatus: "SUCCEEDED" }];
		case "throttle":
			if (job.polls === 1) return [400, { __type: "ThrottlingException", message: "Rate exceeded" }];
			return [200, { JobStatus: "SUCCEEDED", DocumentMetadata: { Pages: 2 } }];
		default:
			if (job.polls < 3) return [200, { JobStatus: "IN_PROGRESS" }];
			return [200, { JobStatus: "SUCCEEDED", DocumentMetadata: { Pages: 3 } }];
	}
};

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

	// The raw request target, before URL parsing drops dot segments, so a job id that climbs out is
	// caught even when it lands on an inspection route. Only a direct read of the counter is exempt.
	const target = req.url || "";
	if (/(^|\/)\.\.(\/|\?|$)/.test(target.split("?")[0]) || (target.includes("__relay/escaped") && !target.startsWith("/__relay/escaped"))) {
		escaped++;
		send(res, 404, { error: "escaped" });
		return;
	}

	if (path === "/__relay/hits") {
		const tag = url.searchParams.get("tag") || "";
		send(res, 200, { tag, hits: hits.get(tag) || 0, data: [] });
		return;
	}
	if (path === "/__relay/jobs") {
		const id = url.searchParams.get("id") || "";
		const job = jobs.get(id);
		send(res, 200, { id, polls: job ? job.polls : 0, scenario: job ? job.scenario : "", data: [] });
		return;
	}
	if (path === "/__relay/escaped") {
		send(res, 200, { hits: escaped, data: [] });
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

	if (req.method === "POST" && path === "/v1/textract") {
		const target = String(req.headers["x-amz-target"] || "");
		if (target === "Textract.StartDocumentAnalysis") {
			const token = body.ClientRequestToken;
			let id = token && tokens.get(token);
			if (!id) {
				id = `job-${body.Scenario || "ok"}-${Date.now()}-${++jobSeq}`;
				jobs.set(id, { scenario: body.Scenario || "ok", polls: 0 });
				if (token) tokens.set(token, id);
			}
			send(res, 200, { JobId: id, data: [] });
			return;
		}
		if (target === "Textract.GetDocumentAnalysis") {
			const job = jobs.get(body.JobId);
			if (!job) {
				send(res, 400, { __type: "InvalidJobIdException", message: "unknown job" });
				return;
			}
			const [status, reply] = pollJob(job);
			send(res, status, { ...reply, data: [] });
			return;
		}
		send(res, 400, { __type: "UnknownOperationException" });
		return;
	}
	if (req.method === "POST" && path === "/v1/rest/jobs") {
		send(res, 200, { id: body.id, data: [] });
		return;
	}

	if (path.startsWith("/v1/gated/")) {
		const echo = { data: [], method: req.method, path, headers: req.headers, body: raw };
		const usage = body?.include_usage === true ? { credits: 3 } : undefined;
		if (path === "/v1/gated/stream") {
			res.writeHead(200, { "Content-Type": "text/event-stream", "Cache-Control": "no-cache" });
			// An earlier, smaller count, so a relay billing the first value is caught.
			res.write(`data: ${JSON.stringify(usage ? { ...echo, usage: { credits: 1 } } : echo)}\n\n`);
			res.write(`data: ${JSON.stringify(usage ? { usage } : { done: true })}\n\n`);
			res.end();
			return;
		}
		send(res, 200, usage ? { ...echo, usage } : echo);
		return;
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

server.listen(port, host, () => {
	console.log(`relay-upstream-fixture listening on http://${host}:${port}`);
});

const stop = () => server.close(() => process.exit(0));
process.on("SIGTERM", stop);
process.on("SIGINT", stop);
