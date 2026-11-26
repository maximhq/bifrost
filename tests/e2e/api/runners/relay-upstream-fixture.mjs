import http from "node:http";
import { gzipSync } from "node:zlib";

// Local upstream for the Relays harness folder (relays-e2e). The shared profile's relays
// (tests/integrations/python/config.json, harness-relay-*) point here, so every relay case runs
// against a deterministic local API and never reaches a third party.
//
// Every reply is JSON carrying a `data` array (the harness's generic content check reads it) and,
// where a route is priced, a `usage` block with fixed numbers. Each request's `tag` query parameter
// is counted, so a case can prove a refused call never reached the upstream:
//
//   GET /__relay/hits?tag=<tag>   {"tag": "...", "hits": <n>, "last_body": "...", "data": []}  (never relayed: no route);
//                                 last_body is the raw body of the latest tagged request, cut at 4 KB
//   GET /__relay/health           {"ok": true, "fixture": "relay-upstream", "data": []}  (identifies a reusable listener)
//   POST /__relay/release?tag=<tag> {"released": bool}: ends a held /v1/slow?hold=1&tag=<tag> call
//   GET /__relay/inflight?tag=<tag> {"tag": "...", "inflight": <n>, "data": []}: tagged requests not yet answered
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
//                        nousage (SUCCEEDED with no page count), instant (SUCCEEDED on a caller's ?tag= check with
//                        1 page; Bifrost's own polls see IN_PROGRESS until then and 4 pages after)
//   POST /v1/rest/jobs   {"id": "<id>"} -> echoes that id as the job id, to try ids a relay must refuse
//   GET  /__relay/jobs?id=<id>  {"id", "polls", "scenario", "data": []}
//   GET  /__relay/escaped       {"hits": <n>}: requests whose raw path left /v1/rest/jobs/
//   *    /v1/echo, other the method, path, query and headers the upstream received
//
// Programmable routes for the gap cases (nothing above reads them):
//   *    /v1/program/**  replies as told by the request body's "__reply" object, else by ?reply=<JSON>:
//                        status, content_type ("" sends none), headers, delay_ms (before the headers),
//                        body (a string is sent as is, an object as JSON; absent echoes the request with
//                        its raw url), pad_bytes (a "pad" string placed first in an object body),
//                        events [{data, delay_ms}] (SSE, text/event-stream unless content_type is set),
//                        chunks [{data, delay_ms}] (a plain chunked stream), stall_ms (after the last
//                        event or chunk, before the reply ends), gzip (compress the body and send
//                        Content-Encoding: gzip)
//   GET  /v1/redirect    302 to /v1/elsewhere?<the query it received>
//   POST /v1/jobs2/start {id?, scenario, finish_after_ms, pages, cost} -> {id}: ok (done once
//                        finish_after_ms passed), failed, never (always running), nousage (done, no pages).
//                        A caller-chosen id starts a new run of that job.
//   *    /v1/jobs2/check/** the job's status, the id read from the path after check/, ?job=, an
//                        x-job-id header, or a JSON, form or XML body's JobId: {status, pages, cost}
//   GET  /__relay/jobs2?id=<id> {id, polls, caller_polls, runs, data: []}; caller polls carry ?tag=
//   POST /__relay/send   {url, method, headers, json, gzip, blob_bytes, summary} sends json to url as a
//                        client would: gzipped with Content-Encoding: gzip when gzip is true, with no
//                        Content-Type unless headers names one, and with a "blob" of blob_bytes y's
//                        placed first. Answers {status, body}, or with summary {status, bytes, head,
//                        tail}, so a multi-MB exchange stays out of the newman report
//   POST /__relay/metrics {url, token, match} scrapes a gateway's /metrics with that setup token and
//                        answers {status, lines} with only the non-bucket lines containing match, so a
//                        long-lived gateway's multi-MB exposition stays out of the newman report
// RELAY_UPSTREAM_FIXTURE_PORT=0 asks the OS for a free port (the startup line reports it).
const port = Number(process.env.RELAY_UPSTREAM_FIXTURE_PORT || "8783");
// Loopback by default. The proxy matrix binds every interface, because its relays address the
// fixture by the host's own IPv4: a loopback target never goes through an environment proxy.
const host = process.env.RELAY_UPSTREAM_FIXTURE_HOST || "127.0.0.1";

const hits = new Map();
const lastBodies = new Map();
const inflight = new Map();
const held = new Map(); // tag -> release for a /v1/slow?hold=1 call
const jobs = new Map();
const jobs2 = new Map();
const tokens = new Map();
let jobSeq = 0;
let escaped = 0;

// One poll of a Textract-shaped job, advancing its scenario.
const pollJob = (job, tagged) => {
	job.polls++;
	switch (job.scenario) {
		case "failed":
			return [200, { JobStatus: "FAILED", StatusMessage: "unsupported document" }];
		case "nousage":
			return [200, { JobStatus: "SUCCEEDED" }];
		case "instant":
			// A caller's tagged check is told 1 page and Bifrost's own poll 4, so the charge shows whose reply priced it.
			// Bifrost's polls see it running until the caller has checked, so the sweeper never settles it first.
			if (tagged) {
				job.callerChecked = true;
				return [200, { JobStatus: "SUCCEEDED", DocumentMetadata: { Pages: 1 } }];
			}
			if (!job.callerChecked) return [200, { JobStatus: "IN_PROGRESS" }];
			return [200, { JobStatus: "SUCCEEDED", DocumentMetadata: { Pages: 4 } }];
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

const wait = (ms) => new Promise((resolve) => setTimeout(resolve, Math.max(0, Number(ms) || 0)));

// The reply a /v1/program call asks for: the body's __reply object, else ?reply=<JSON>.
const programConfig = (url, body) => {
	if (body && typeof body.__reply === "object" && body.__reply) return body.__reply;
	try {
		const cfg = JSON.parse(url.searchParams.get("reply") || "{}");
		// null or a non-object reads as no config, so program() never dereferences null.
		return cfg && typeof cfg === "object" && !Array.isArray(cfg) ? cfg : {};
	} catch {
		return {};
	}
};

const program = async (req, res, url, path, raw, body) => {
	const cfg = programConfig(url, body);
	await wait(cfg.delay_ms);
	if (res.destroyed) return;
	const status = Number(cfg.status) || 200;
	const headers = { ...(cfg.headers || {}) };
	const events = Array.isArray(cfg.events) ? cfg.events : null;
	const chunks = Array.isArray(cfg.chunks) ? cfg.chunks : null;
	const contentType = cfg.content_type !== undefined ? cfg.content_type : events ? "text/event-stream" : "application/json";
	if (contentType) headers["Content-Type"] = contentType;
	if (events || chunks) {
		res.writeHead(status, headers);
		// Sent now, so a delayed first part or a stall delays only the body, not the headers.
		res.flushHeaders();
		for (const part of events || chunks) {
			await wait(part.delay_ms);
			if (res.destroyed) return;
			const data = typeof part.data === "string" ? part.data : JSON.stringify(part.data);
			res.write(events ? `data: ${data}\n\n` : data);
		}
		await wait(cfg.stall_ms);
		if (!res.destroyed) res.end();
		return;
	}
	let out;
	if (typeof cfg.body === "string") {
		out = cfg.body;
	} else {
		const obj = cfg.body && typeof cfg.body === "object" ? cfg.body : { data: [], method: req.method, path, url: req.url, query: Object.fromEntries(url.searchParams.entries()), headers: req.headers, body: raw };
		out = JSON.stringify(cfg.pad_bytes ? { pad: "x".repeat(Number(cfg.pad_bytes)), ...obj } : obj);
	}
	if (cfg.gzip) {
		headers["Content-Encoding"] = "gzip";
		out = gzipSync(Buffer.from(out));
	}
	res.writeHead(status, headers);
	// Sent now, so a stall delays only the body, not the headers.
	if (cfg.stall_ms) res.flushHeaders();
	await wait(cfg.stall_ms);
	if (!res.destroyed) res.end(out);
};

// The job id a /v1/jobs2/check call names: the path after check/, ?job=, x-job-id, or a body JobId.
const jobs2ID = (req, url, path, raw, body) => {
	const rest = path.slice("/v1/jobs2/check".length).replace(/^\/+/, "");
	// The status endpoints (status, status-h, status-j, ...) read their id elsewhere; any other
	// path segment, an id such as status-1 included, is the id.
	if (rest && !/^status(-[a-z])?$/.test(rest)) return decodeURIComponent(rest);
	if (url.searchParams.get("job")) return url.searchParams.get("job");
	if (req.headers["x-job-id"]) return String(req.headers["x-job-id"]);
	if (body && typeof body.JobId === "string") return body.JobId;
	const form = new URLSearchParams(raw);
	if (form.get("JobId")) return form.get("JobId");
	const xml = /<JobId>([^<]*)<\/JobId>/.exec(raw);
	// One pass, so &amp;lt; decodes to the literal &lt;, not to <.
	const entities = { "&amp;": "&", "&lt;": "<", "&gt;": ">", "&quot;": '"', "&apos;": "'" };
	return xml ? xml[1].replace(/&(amp|lt|gt|quot|apos);/g, (m) => entities[m]) : "";
};

const jobs2Status = (job) => {
	const finished = Date.now() - job.started >= job.finishAfter;
	switch (job.scenario) {
		case "never":
			return { status: "running" };
		case "failed":
			return finished ? { status: "failed" } : { status: "running" };
		case "nousage":
			return finished ? { status: "done" } : { status: "running" };
		default:
			if (!finished) return { status: "running" };
			return job.cost === undefined ? { status: "done", pages: job.pages } : { status: "done", pages: job.pages, cost: job.cost };
	}
};

// Sends {url, method, headers, json, gzip} as a client would, optionally compressing the body.
const sendAsClient = async (raw, res) => {
	let spec = {};
	try {
		spec = JSON.parse(raw);
	} catch {
		send(res, 400, { error: "send takes a JSON spec", data: [] });
		return;
	}
	try {
		const json = spec.blob_bytes ? { blob: "y".repeat(Number(spec.blob_bytes)), ...(spec.json || {}) } : spec.json || {};
		const payload = Buffer.from(JSON.stringify(json));
		const reply = await fetch(spec.url, {
			method: spec.method || "POST",
			headers: spec.gzip ? { ...(spec.headers || {}), "Content-Encoding": "gzip" } : spec.headers || {},
			body: spec.gzip ? gzipSync(payload) : payload,
		});
		const text = await reply.text();
		if (spec.summary) {
			send(res, 200, { status: reply.status, bytes: Buffer.byteLength(text), head: text.slice(0, 300), tail: text.slice(-300), data: [] });
			return;
		}
		send(res, 200, { status: reply.status, body: text, data: [] });
	} catch (err) {
		send(res, 502, { error: String(err), data: [] });
	}
};

// Scrapes {url} with the setup token and keeps the non-bucket lines containing {match}.
const grepMetrics = async (raw, res) => {
	try {
		const spec = JSON.parse(raw);
		const reply = await fetch(spec.url, { headers: spec.token ? { "X-Bifrost-Setup-Token": spec.token } : {} });
		const text = await reply.text();
		const lines = text.split("\n").filter((l) => l.includes(spec.match || "") && !l.startsWith("#") && !/_bucket\{/.test(l));
		send(res, 200, { status: reply.status, lines, data: [] });
	} catch (err) {
		send(res, 502, { error: String(err), data: [] });
	}
};

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
		send(res, 200, { tag, hits: hits.get(tag) || 0, last_body: lastBodies.get(tag) || "", data: [] });
		return;
	}
	if (path === "/__relay/jobs") {
		const id = url.searchParams.get("id") || "";
		const job = jobs.get(id);
		send(res, 200, { id, polls: job ? job.polls : 0, scenario: job ? job.scenario : "", data: [] });
		return;
	}
	if (path === "/__relay/release" && req.method === "POST") {
		const release = held.get(url.searchParams.get("tag") || "");
		if (release) release();
		send(res, 200, { released: Boolean(release), data: [] });
		return;
	}
	if (path === "/__relay/inflight") {
		const tag = url.searchParams.get("tag") || "";
		send(res, 200, { tag, inflight: inflight.get(tag) || 0, data: [] });
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
	if (path === "/__relay/jobs2") {
		const id = url.searchParams.get("id") || "";
		const job = jobs2.get(id);
		send(res, 200, { id, polls: job ? job.polls : 0, caller_polls: job ? job.callerPolls : 0, runs: job ? job.runs : 0, data: [] });
		return;
	}
	if ((path === "/__relay/metrics" || path === "/__relay/send") && req.method === "POST") {
		// These fetch a caller-given URL, so only a local caller may use them, and only toward a local gateway.
		const refused = helperRefusal(req, raw);
		if (refused) {
			send(res, 403, { error: refused, data: [] });
			return;
		}
		if (path === "/__relay/metrics") await grepMetrics(raw, res);
		else await sendAsClient(raw, res);
		return;
	}

	const tag = url.searchParams.get("tag");
	if (tag) {
		hits.set(tag, (hits.get(tag) || 0) + 1);
		// Cut short so a multi-MB upload never stays in memory.
		lastBodies.set(tag, raw.slice(0, 4096));
		// Counted until the reply is done or the socket drops, so a case can wait for a call to be open here.
		inflight.set(tag, (inflight.get(tag) || 0) + 1);
		res.once("close", () => inflight.set(tag, inflight.get(tag) - 1));
	}

	// Answers only after ?ms= milliseconds (default 3000, at most 10000), for a relay timeout case.
	// With ?hold=1 it waits for POST /__relay/release?tag= instead (or the ms cap), so a test controls when it ends.
	if (path.startsWith("/v1/slow")) {
		const ms = Math.min(Number(url.searchParams.get("ms")) || 3000, 10000);
		const tag = url.searchParams.get("tag") || "";
		await new Promise((resolve) => {
			const timer = setTimeout(resolve, url.searchParams.get("hold") === "1" ? 10000 : ms);
			if (url.searchParams.get("hold") === "1") held.set(tag, () => (clearTimeout(timer), resolve()));
		});
		held.delete(tag);
		if (!res.destroyed) send(res, 200, { slow: true, ms, data: [] });
		return;
	}

	let body = {};
	try {
		body = raw ? JSON.parse(raw) : {};
	} catch {
		body = {};
	}

	if (path === "/v1/program" || path.startsWith("/v1/program/")) {
		await program(req, res, url, path, raw, body);
		return;
	}
	if (path === "/v1/redirect") {
		res.writeHead(302, { Location: `/v1/elsewhere?${url.search.slice(1)}`, "Content-Type": "application/json" });
		res.end(JSON.stringify({ redirected: true, data: [] }));
		return;
	}
	if (req.method === "POST" && path === "/v1/jobs2/start") {
		body = body || {};
		const id = typeof body.id === "string" && body.id ? body.id : `j2-${body.scenario || "ok"}-${Date.now()}-${++jobSeq}`;
		const prior = jobs2.get(id);
		jobs2.set(id, {
			scenario: body.scenario || "ok",
			started: Date.now(),
			finishAfter: Number(body.finish_after_ms) || 0,
			pages: body.pages === undefined ? 3 : Number(body.pages),
			cost: body.cost,
			polls: prior ? prior.polls : 0,
			callerPolls: prior ? prior.callerPolls : 0,
			runs: (prior ? prior.runs : 0) + 1,
		});
		send(res, 200, { id, data: [] });
		return;
	}
	if (path.startsWith("/v1/jobs2/check")) {
		const id = jobs2ID(req, url, path, raw, body);
		const job = jobs2.get(id);
		if (!job) {
			send(res, 404, { error: "unknown job", id, data: [] });
			return;
		}
		if (url.searchParams.get("tag")) job.callerPolls++;
		else job.polls++;
		send(res, 200, { id, ...jobs2Status(job), data: [] });
		return;
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
			const [status, reply] = pollJob(job, Boolean(url.searchParams.get("tag")));
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

// Port 0 binds a free port; the line names the bound one so a caller can read it back.
const LOOPBACK = new Set(["127.0.0.1", "::1", "::ffff:127.0.0.1"]);
const LOCAL_HOSTS = new Set(["localhost", "127.0.0.1", "[::1]"]);

// helperRefusal says why a fetch helper call is refused, or "" when the caller and target are local.
function helperRefusal(req, raw) {
	if (!LOOPBACK.has(req.socket.remoteAddress || "")) return "fetch helpers answer local callers only";
	let target;
	try {
		target = new URL(JSON.parse(raw.toString("utf8") || "{}").url);
	} catch {
		return "url must be an absolute URL";
	}
	if (!LOCAL_HOSTS.has(target.hostname)) return "url must point at a local gateway";
	return "";
}

server.listen(port, host, () => {
	console.log(`relay-upstream-fixture listening on http://${host}:${server.address().port}`);
});

const stop = () => server.close(() => process.exit(0));
process.on("SIGTERM", stop);
process.on("SIGINT", stop);
