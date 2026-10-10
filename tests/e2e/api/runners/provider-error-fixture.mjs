import http from "node:http";

const port = Number(process.env.PROVIDER_ERROR_FIXTURE_PORT || "8791");

// Per-model behaviour for POST /v1/chat/completions. The model name in the request
// body selects the response, so one fixture serves every case.
const errorBody = (message, type, code) => JSON.stringify({ error: { message, type, code } });

const behaviours = {
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
		try {
			model = JSON.parse(Buffer.concat(chunks).toString("utf8")).model || "";
		} catch {}
		hits[model] = (hits[model] || 0) + 1;
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
		if (path === "/v1/responses") {
			let body = {};
			try {
				body = JSON.parse(Buffer.concat(chunks).toString("utf8"));
			} catch {}
			const r = responsesFor(body, (k) => { hits[k] = (hits[k] || 0) + 1; });
			return send(r.status, r.headers, r.body);
		}
		// The model comes from the request body, so only an own entry may be looked up: "constructor" or
		// "toString" must be an unknown model, not an inherited function.
		const behaviour = model === "azure-filtered" ? azureFiltered : Object.hasOwn(behaviours, model) ? behaviours[model] : undefined;
		if (!behaviour) {
			return send(404, {}, errorBody(`unknown fixture model ${model}`, "invalid_request_error", "model_not_found"));
		}
		const r = behaviour();
		send(r.status, r.headers, r.body);
	});
});

server.listen(port, "127.0.0.1", () => {
	console.log(`provider-error-fixture listening on http://127.0.0.1:${port}`);
});
