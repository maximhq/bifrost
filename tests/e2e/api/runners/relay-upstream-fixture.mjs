import http from "node:http";
import zlib from "node:zlib";

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
//   POST /v1/stream-oversized  SSE: credits 1, then one event past 10 MiB that carries credits 3
//   POST /v1/pages       usage.chars = the request body's "chars" (block and minimum pricing)
//   POST /v1/reported    usage.cost 0.005 (a provider-reported cost)
//   *    /v1/echo, other the method, path, query and headers the upstream received
//
// Request headers that shape the reply (the relay forwards them like any caller header):
//   x-fixture-encoding: br | deflate | zstd | gzip   encode the reply (JSON or SSE) with that coding and
//                       name it in Content-Encoding; an SSE reply is encoded incrementally, flushed
//                       per event. invalid-br sends "Content-Encoding: br" over bytes that are not
//                       brotli. zstd needs a Node with zlib zstd (22.15+); without it the reply is a 501.
//   x-fixture-pause-ms: <n>   on /v1/stream, wait n ms after the first event, so the gateway's
//                       SSE heartbeat (1s) would fire while the upstream is quiet
const port = Number(process.env.RELAY_UPSTREAM_FIXTURE_PORT || "8783");
// Loopback by default. The proxy matrix binds every interface, because its relays address the
// fixture by the host's own IPv4: a loopback target never goes through an environment proxy.
const host = process.env.RELAY_UPSTREAM_FIXTURE_HOST || "127.0.0.1";

const hits = new Map();

const createEncoder = (coding) => {
	switch (coding) {
		case "br":
			return zlib.createBrotliCompress();
		case "deflate":
			return zlib.createDeflate();
		case "gzip":
			return zlib.createGzip();
		case "zstd":
			return typeof zlib.createZstdCompress === "function" ? zlib.createZstdCompress() : null;
		default:
			return null;
	}
};

const encodeWhole = (coding, buf) => {
	switch (coding) {
		case "br":
			return zlib.brotliCompressSync(buf);
		case "deflate":
			return zlib.deflateSync(buf);
		case "gzip":
			return zlib.gzipSync(buf);
		case "zstd":
			return typeof zlib.zstdCompressSync === "function" ? zlib.zstdCompressSync(buf) : null;
		case "invalid-br":
			return Buffer.from("not brotli at all");
		default:
			return null;
	}
};

const unsupportedEncoding = (res, coding) => {
	res.writeHead(501, { "Content-Type": "application/json" });
	res.end(JSON.stringify({ data: [], error: { message: `relay-upstream-fixture cannot encode ${coding} on Node ${process.version}` } }));
};

// encoding is the request's x-fixture-encoding, "" for a plain reply.
const send = (res, status, body, headers = {}, encoding = "") => {
	const json = Buffer.from(JSON.stringify(body));
	if (!encoding) {
		res.writeHead(status, { "Content-Type": "application/json", ...headers });
		res.end(json);
		return;
	}
	const encoded = encodeWhole(encoding, json);
	if (!encoded) {
		unsupportedEncoding(res, encoding);
		return;
	}
	res.writeHead(status, { "Content-Type": "application/json", "Content-Encoding": encoding === "invalid-br" ? "br" : encoding, ...headers });
	res.end(encoded);
};

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

// Writes each event as it would arrive from a live upstream: encoded and flushed per event when an
// encoding is asked for, with an optional quiet period after the first one.
const streamEvents = async (res, events, encoding, pauseMs) => {
	const head = { "Content-Type": "text/event-stream", "Cache-Control": "no-cache" };
	let sink = res;
	if (encoding) {
		const encoder = createEncoder(encoding);
		if (!encoder) {
			unsupportedEncoding(res, encoding);
			return;
		}
		res.writeHead(200, { ...head, "Content-Encoding": encoding });
		encoder.pipe(res);
		sink = encoder;
	} else {
		res.writeHead(200, head);
	}
	for (let i = 0; i < events.length; i++) {
		sink.write(events[i]);
		if (encoding) await new Promise((resolve) => sink.flush(resolve));
		if (i === 0 && pauseMs > 0) await sleep(pauseMs);
	}
	sink.end();
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
	const encoding = String(req.headers["x-fixture-encoding"] || "").trim().toLowerCase();
	const pauseMs = Number(req.headers["x-fixture-pause-ms"]) || 0;

	let body = {};
	try {
		body = raw ? JSON.parse(raw) : {};
	} catch {
		body = {};
	}

	switch (`${req.method} ${path}`) {
		case "POST /v1/search":
			send(res, 200, { data: [{ title: "bifrost" }, { title: "relay" }], usage: { credits: 4, input_tokens: 50, output_tokens: 20 } }, {}, encoding);
			return;
		case "POST /v1/extract":
			send(res, 200, { data: [{ text: "extracted" }], usage: { credits: 2 } }, {}, encoding);
			return;
		case "GET /v1/status":
			send(res, 200, { data: [], ok: true, usage: { credits: 9 } }, {}, encoding);
			return;
		case "POST /v1/free":
			send(res, 200, { data: [], usage: { credits: 5 } }, {}, encoding);
			return;
		case "POST /v1/fail":
			send(res, 500, { error: { message: "upstream failure" }, usage: { credits: 5 } }, {}, encoding);
			return;
		case "POST /v1/stream":
			await streamEvents(
				res,
				[
					`data: ${JSON.stringify({ delta: "a", usage: { credits: 1 } })}\n\n`,
					`data: ${JSON.stringify({ delta: "b" })}\n\n`,
					`data: ${JSON.stringify({ delta: "c", usage: { credits: 3 } })}\n\n`,
				],
				encoding,
				pauseMs,
			);
			return;
		case "POST /v1/stream-oversized":
			// A usage event, then one event past 10 MiB that carries the final usage: relays enforce no
			// size cap, so the event must reach the caller whole and its credits must be the ones billed.
			res.writeHead(200, { "Content-Type": "text/event-stream", "Cache-Control": "no-cache" });
			res.write(`data: ${JSON.stringify({ delta: "a", usage: { credits: 1 } })}\n\n`);
			res.write(`data: ${JSON.stringify({ delta: "big", pad: "x".repeat(11 * 1024 * 1024), usage: { credits: 3 } })}\n\n`);
			res.end();
			return;
		case "POST /v1/pages":
			send(res, 200, { data: [{ page: 1 }], usage: { chars: Number(body.chars) || 0 } }, {}, encoding);
			return;
		case "POST /v1/reported":
			send(res, 200, { data: [], usage: { cost: 0.005 } }, {}, encoding);
			return;
		default:
			send(
				res,
				200,
				{
					data: [],
					method: req.method,
					path,
					query: Object.fromEntries(url.searchParams.entries()),
					headers: req.headers,
					body: raw,
				},
				{},
				encoding,
			);
	}
});

server.listen(port, host, () => {
	console.log(`relay-upstream-fixture listening on http://${host}:${port}`);
});

const stop = () => server.close(() => process.exit(0));
process.on("SIGTERM", stop);
process.on("SIGINT", stop);
