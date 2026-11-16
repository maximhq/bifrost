// Synthetic Anthropic Messages endpoint for folder 127, "Sonnet 5.5 caller
// request surface". It stands in for the provider on loopback so the cases can
// assert the bytes BIFROST FORWARDS without a paid call, a real key, or any
// claim about what the live service accepts.
//
// It is deliberately strict: a request whose reasoning or sampling surface is
// not the one the caller sent is answered 400 with every drifting field named,
// so the harness case is red before the fix and green after rather than passing
// on a 200 it never inspected.
//
// Run it, then point an isolated gateway at it:
//   node tests/e2e/api/runners/anthropic-request-surface-fixture.mjs
// See tests/e2e/api/README.md, "Sonnet 5.5 caller request surface".

import http from "node:http";
import { pathToFileURL } from "node:url";

import { evaluateForwardedRequest } from "./lib/anthropic-request-surface.mjs";

const port = Number(process.env.ANTHROPIC_REQUEST_SURFACE_FIXTURE_PORT || "8791");

const paths = new Set(["/v1/messages", "/v1/messages/count_tokens"]);

function messageResponse(model) {
	return {
		id: "msg_surface_ok",
		type: "message",
		role: "assistant",
		model,
		content: [{ type: "text", text: "surface ok" }],
		stop_reason: "end_turn",
		stop_sequence: null,
		usage: { input_tokens: 10, output_tokens: 2 },
	};
}

function streamEvents(model) {
	return [
		["message_start", {
			type: "message_start",
			message: {
				id: "msg_surface_ok",
				type: "message",
				role: "assistant",
				model,
				content: [],
				stop_reason: null,
				stop_sequence: null,
				usage: { input_tokens: 10, output_tokens: 0 },
			},
		}],
		["content_block_start", { type: "content_block_start", index: 0, content_block: { type: "text", text: "" } }],
		["content_block_delta", { type: "content_block_delta", index: 0, delta: { type: "text_delta", text: "surface ok" } }],
		["content_block_stop", { type: "content_block_stop", index: 0 }],
		["message_delta", {
			type: "message_delta",
			delta: { stop_reason: "end_turn", stop_sequence: null },
			usage: { output_tokens: 2 },
		}],
		["message_stop", { type: "message_stop" }],
	];
}

function reject(res, caseKey, mismatches) {
	const label = caseKey ? `case ${caseKey}` : "unrecognised request";
	const body = JSON.stringify({
		type: "error",
		error: {
			type: "invalid_request_error",
			message: `surface_mismatch (${label}): ${mismatches.join("; ")}`,
		},
	});
	res.writeHead(400, { "Content-Type": "application/json" }).end(body);
}

// requestPath is the whole reason the request target is not parsed inline: a
// malformed one (`//%`, `//[`) makes `new URL` throw ERR_INVALID_URL, and a
// throw from an async listener is an unhandled rejection that answers nothing
// and, on Node's default, takes the fixture down mid-folder. An unparseable
// target is simply not one of the two paths this fixture serves.
function requestPath(target) {
	try {
		return new URL(target ?? "", "http://localhost").pathname;
	} catch {
		return null;
	}
}

// Exported so the offline tests can drive the handler boundary itself (status
// codes for malformed and unknown targets) without binding a port.
export async function handleFixtureRequest(req, res) {
	const path = requestPath(req.url);
	if (req.method !== "POST" || path === null || !paths.has(path)) {
		res.writeHead(404).end();
		return;
	}
	let body;
	try {
		req.setEncoding("utf8");
		let raw = "";
		for await (const chunk of req) raw += chunk;
		body = JSON.parse(raw);
	} catch {
		res.writeHead(400).end("invalid fixture request");
		return;
	}

	const { caseKey, mismatches } = evaluateForwardedRequest({ path, body, headers: req.headers });
	if (mismatches.length > 0) {
		reject(res, caseKey, mismatches);
		return;
	}

	if (path === "/v1/messages/count_tokens") {
		res.writeHead(200, { "Content-Type": "application/json" }).end(JSON.stringify({ input_tokens: 10 }));
		return;
	}
	if (body.stream === true) {
		res.writeHead(200, { "Content-Type": "text/event-stream", "Cache-Control": "no-cache" });
		res.flushHeaders();
		for (const [event, data] of streamEvents(body.model)) {
			res.write(`event: ${event}\ndata: ${JSON.stringify(data)}\n\n`);
		}
		res.end();
		return;
	}
	res.writeHead(200, { "Content-Type": "application/json" }).end(JSON.stringify(messageResponse(body.model)));
}

// Listen only when run as the fixture; importing it (the offline tests do) must
// not bind a port.
if (process.argv[1] && pathToFileURL(process.argv[1]).href === import.meta.url) {
	const server = http.createServer(handleFixtureRequest);
	server.listen(port, "127.0.0.1", () => {
		console.log(`Anthropic request-surface fixture: http://127.0.0.1:${port}`);
	});
	for (const signal of ["SIGINT", "SIGTERM"]) {
		process.on(signal, () => server.close(() => process.exit(0)));
	}
}
