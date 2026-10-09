import http from "node:http";

const port = Number(process.env.PROVIDER_ERROR_FIXTURE_PORT || "8791");

// Per-model behaviour for POST /v1/chat/completions. The model name in the request
// body selects the response, so one fixture serves every case. A model may carry a
// "~tag" suffix ("upstream-500~ps02-a-run-1"): the part before "~" selects the
// behaviour and hits are counted under the full name, so a case that sends its own
// tag (through a key alias) counts its own attempts without resetting anyone else's.
const errorBody = (message, type, code) => JSON.stringify({ error: { message, type, code } });

// How long upstream-slow waits before it answers, long enough that a 500ms first-token
// deadline always fires first and short enough to stay inside any request timeout.
const SLOW_MS = 2000;

const behaviours = {
	"upstream-500": () => ({ status: 500, headers: {}, body: errorBody("internal server error", "server_error", "internal_error") }),
	"upstream-503": () => ({ status: 503, headers: {}, body: errorBody("service unavailable", "server_error", "service_unavailable") }),
	"upstream-429": () => ({ status: 429, headers: { "Retry-After": "7" }, body: errorBody("rate limit exceeded", "rate_limit_error", "rate_limit_exceeded") }),
	"upstream-429-ms": () => ({ status: 429, headers: { "retry-after-ms": "2500" }, body: errorBody("rate limit exceeded", "rate_limit_error", "rate_limit_exceeded") }),
	"upstream-ok": () => ({
		status: 200,
		headers: {},
		body: JSON.stringify({
			id: "chatcmpl-upstream-ok",
			object: "chat.completion",
			created: 1,
			model: "upstream-ok",
			choices: [{ index: 0, message: { role: "assistant", content: "hello" }, finish_reason: "stop" }],
			usage: { prompt_tokens: 10, completion_tokens: 1, total_tokens: 11 },
		}),
	}),
};
// upstream-slow answers as upstream-ok does, after SLOW_MS.
behaviours["upstream-slow"] = () => ({ ...behaviours["upstream-ok"](), delayMs: SLOW_MS });
// upstream-cut starts answering as upstream-ok does and drops the connection partway: a stream gets its first
// content chunk and then a closed socket, a non-streamed request a closed socket.
behaviours["upstream-cut"] = () => ({ ...behaviours["upstream-ok"](), cut: true });

// A streamed chat request (stream: true) that a behaviour answers with a completion gets the same
// answer as server-sent chunks: the role, the content, the finish reason, the usage when
// stream_options.include_usage asks for it, then [DONE].
const streamChunks = (completion, includeUsage) => {
	const base = { id: completion.id, object: "chat.completion.chunk", created: completion.created, model: completion.model };
	const choice = completion.choices[0];
	const chunks = [
		{ ...base, choices: [{ index: 0, delta: { role: "assistant", content: "" }, finish_reason: null }] },
		{ ...base, choices: [{ index: 0, delta: { content: choice.message.content }, finish_reason: null }] },
		{ ...base, choices: [{ index: 0, delta: {}, finish_reason: choice.finish_reason }] },
	];
	if (includeUsage) chunks.push({ ...base, choices: [], usage: completion.usage });
	return chunks.map((c) => `data: ${JSON.stringify(c)}\n\n`).join("") + "data: [DONE]\n\n";
};

// Hit counts per model let a case prove a failover really tried the primary once and the
// fallback once. The count endpoints answer with {data: [...]} so the harness's generic
// response-shape check accepts them.
// Azure serves chat under /openai/v1 and decorates the response with content-filter
// annotations: a prompt_filter_results array and a per-choice content_filter_results object.
const filterOk = { hate: { filtered: false, severity: "safe" }, violence: { filtered: false, severity: "safe" } };
const azureFiltered = () => ({
	status: 200,
	headers: {},
	body: JSON.stringify({
		id: "chatcmpl-azure-filtered",
		object: "chat.completion",
		created: 1,
		model: "azure-filtered",
		prompt_filter_results: [{ prompt_index: 0, content_filter_results: filterOk }],
		choices: [{ index: 0, finish_reason: "stop", message: { role: "assistant", content: "hello" }, content_filter_results: filterOk }],
		usage: { prompt_tokens: 10, completion_tokens: 1, total_tokens: 11 },
	}),
});

// OpenAI pairs a reasoning item's id with its encrypted_content and rejects a replay whose id does not
// match the token. The fixture issues one reasoning item and enforces that pairing on replay, which is
// how a gateway that mints a fresh id on replay shows up as a 400.
const REASONING_ID = "rs_fixture_original";
const REASONING_TOKEN = "enc-token-fixture";
const responsesFor = (body, count) => {
	const input = Array.isArray(body.input) ? body.input : [];
	for (const item of input) {
		if (item && item.type === "reasoning" && item.encrypted_content === REASONING_TOKEN) {
			if (item.id !== REASONING_ID) {
				count("replay-id-mismatch");
				return { status: 400, headers: {}, body: errorBody(`Item '${item.id}' of type 'reasoning' was provided without its required encrypted_content pairing`, "invalid_request_error", "invalid_encrypted_content") };
			}
			count("replay-id-ok");
		}
	}
	return {
		status: 200,
		headers: {},
		body: JSON.stringify({
			id: "resp_fixture_1",
			object: "response",
			created_at: 1,
			status: "completed",
			model: "gpt-5-pro",
			output: [
				{ id: REASONING_ID, type: "reasoning", summary: [{ type: "summary_text", text: "thought it through" }], encrypted_content: REASONING_TOKEN },
				{ id: "msg_fixture_1", type: "message", role: "assistant", status: "completed", content: [{ type: "output_text", text: "hello", annotations: [] }] },
			],
			usage: { input_tokens: 10, output_tokens: 5, total_tokens: 15 },
		}),
	};
};

// Hit counters are keyed by the request body's model, so they must not inherit Object.prototype names.
const hits = Object.create(null);

const server = http.createServer((req, res) => {
	const path = new URL(req.url, "http://localhost").pathname;
	const send = (status, headers, body) => {
		res.writeHead(status, { "Content-Type": "application/json", ...headers });
		res.end(body);
	};
	// A fresh gateway downloads a pricing datasheet and a model-parameters datasheet on first start, and the model
	// catalog decides which request types a model supports (gpt-5-pro is Responses-only, so a chat request to it is
	// converted). Serving both from here keeps the run offline and the result independent of a remote file.
	if (req.method === "GET" && path === "/pricing.json") {
		return send(200, {}, JSON.stringify({}));
	}
	if (req.method === "GET" && path === "/model-parameters.json") {
		// supported_endpoints/mode feed the catalog's per-model request types: gpt-5-pro answers on /v1/responses only.
		return send(200, {}, JSON.stringify({ "gpt-5-pro": { provider: "openai", mode: "responses", supported_endpoints: ["/v1/responses"] } }));
	}
	if (req.method === "POST" && path === "/__reset") {
		for (const k of Object.keys(hits)) delete hits[k];
		return send(200, {}, JSON.stringify({ data: [{ reset: true }] }));
	}
	if (req.method === "GET" && path === "/__hits") {
		return send(200, {}, JSON.stringify({ data: [{ hits: { ...hits } }] }));
	}
	if (req.method !== "POST" || (path !== "/v1/chat/completions" && path !== "/openai/v1/chat/completions" && path !== "/v1/responses")) {
		return send(404, {}, errorBody("not found", "invalid_request_error", "not_found"));
	}
	const chunks = [];
	req.on("data", (c) => chunks.push(c));
	req.on("end", () => {
		let model = "";
		let parsed = {};
		try {
			parsed = JSON.parse(Buffer.concat(chunks).toString("utf8")) || {};
			model = parsed.model || "";
		} catch {}
		hits[model] = (hits[model] || 0) + 1;
		// test-key is the only credential the fixture accepts. A request bearing any other key is refused
		// with 401 before its model is looked at, which is how a provider refuses a caller's direct key.
		const auth = req.headers.authorization;
		if (auth !== undefined && auth !== "Bearer test-key") {
			return send(401, {}, errorBody("Incorrect API key provided", "invalid_request_error", "invalid_api_key"));
		}
		if (model === "upstream-fixed-length-short" || model === "upstream-fixed-length-ok") {
			// finish_reason makes this semantically complete even without [DONE].
			// Only the transport's Content-Length check can reject the short body.
			const payload = [
				{ id: "chatcmpl-fixed-length", object: "chat.completion.chunk", model, choices: [{ index: 0, delta: { role: "assistant", content: "hello" } }] },
				{ id: "chatcmpl-fixed-length", object: "chat.completion.chunk", model, choices: [{ index: 0, delta: {}, finish_reason: "stop" }], usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 } },
			].map((chunk) => `data: ${JSON.stringify(chunk)}\n\n`).join("");
			const truncated = model === "upstream-fixed-length-short";
			const socket = res.socket;
			res.writeHead(200, {
				"Content-Type": "text/event-stream",
				"Content-Length": Buffer.byteLength(payload) + (truncated ? 100 : 0),
			});
			return res.end(payload, () => {
				if (truncated) socket.end(); // Graceful EOF without a Connection: close header.
			});
		}
		// The model's behaviour picks the response, so only an own entry may be looked up: "constructor" or
		// "toString" must be an unknown model, not an inherited function.
		const name = model.split("~")[0];
		if (path === "/v1/responses") {
			// A Responses request whose model names an error behaviour gets that error; any other model gets the
			// fixed reasoning response below.
			const failing = Object.hasOwn(behaviours, name) ? behaviours[name]() : undefined;
			if (failing && failing.status !== 200) {
				return send(failing.status, failing.headers, failing.body);
			}
			let body = {};
			try {
				body = JSON.parse(Buffer.concat(chunks).toString("utf8"));
			} catch {}
			const r = responsesFor(body, (k) => { hits[k] = (hits[k] || 0) + 1; });
			return send(r.status, r.headers, r.body);
		}
		const behaviour = name === "azure-filtered" ? azureFiltered : Object.hasOwn(behaviours, name) ? behaviours[name] : undefined;
		if (!behaviour) {
			return send(404, {}, errorBody(`unknown fixture model ${model}`, "invalid_request_error", "model_not_found"));
		}
		const r = behaviour();
		const answer = () => {
			if (r.cut) {
				if (parsed.stream !== true) {
					return req.socket.destroy();
				}
				const first = streamChunks(JSON.parse(r.body), false).split("\n\n").slice(0, 2).join("\n\n") + "\n\n";
				res.writeHead(200, { "Content-Type": "text/event-stream", "Cache-Control": "no-cache" });
				res.write(first);
				return setTimeout(() => req.socket.destroy(), 100);
			}
			if (parsed.stream === true && r.status === 200 && path !== "/v1/responses") {
				const completion = JSON.parse(r.body);
				res.writeHead(200, { "Content-Type": "text/event-stream", "Cache-Control": "no-cache", ...r.headers });
				return res.end(streamChunks(completion, !!(parsed.stream_options && parsed.stream_options.include_usage)));
			}
			send(r.status, r.headers, r.body);
		};
		if (r.delayMs) {
			return setTimeout(answer, r.delayMs);
		}
		answer();
	});
});

server.listen(port, "127.0.0.1", () => {
	console.log(`provider-error-fixture listening on http://127.0.0.1:${port}`);
});
