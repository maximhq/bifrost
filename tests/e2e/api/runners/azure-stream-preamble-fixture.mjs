import http from "node:http";

const port = Number(process.env.AZURE_STREAM_FIXTURE_PORT || "8789");

const chatFailure = [
	'{"choices":[],"prompt_filter_results":[]}',
	'{"id":"azure-preamble-failed","choices":[{"index":0,"delta":{"role":"assistant"}}]}',
	'{"error":{"message":"rate limit exceeded","type":"rate_limit_error"}}',
];

const chatSuccess = [
	'{"id":"azure-preamble-success","object":"chat.completion.chunk","model":"preamble-success","choices":[{"index":0,"delta":{"role":"assistant","content":"hello"}}]}',
	'{"id":"azure-preamble-success","object":"chat.completion.chunk","model":"preamble-success","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":1,"total_tokens":11}}',
];

const responsesFailure = [
	'{"type":"response.created","sequence_number":0,"response":{"id":"azure-preamble-failed","status":"in_progress","output":[]}}',
	'{"type":"response.in_progress","sequence_number":1,"response":{"id":"azure-preamble-failed","status":"in_progress","output":[]}}',
	'{"type":"response.output_item.added","sequence_number":2,"output_index":0,"item":{"id":"failed-item","type":"message","role":"assistant","status":"in_progress","content":[]}}',
	'{"type":"response.content_part.added","sequence_number":3,"item_id":"failed-item","output_index":0,"content_index":0,"part":{"type":"output_text","text":"","annotations":[]}}',
	'{"type":"response.failed","sequence_number":4,"response":{"id":"azure-preamble-failed","error":{"code":"rate_limit_exceeded","message":"rate limit exceeded"}}}',
];

const responsesSuccess = [
	'{"type":"response.created","sequence_number":0,"response":{"id":"azure-preamble-success","object":"response","status":"in_progress","model":"preamble-success","output":[]}}',
	'{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"id":"success-item","type":"message","role":"assistant","status":"in_progress","content":[]}}',
	'{"type":"response.content_part.added","sequence_number":2,"item_id":"success-item","output_index":0,"content_index":0,"part":{"type":"output_text","text":"","annotations":[]}}',
	'{"type":"response.output_text.delta","sequence_number":3,"item_id":"success-item","output_index":0,"content_index":0,"delta":"hello"}',
	'{"type":"response.output_text.done","sequence_number":4,"item_id":"success-item","output_index":0,"content_index":0,"text":"hello"}',
	'{"type":"response.content_part.done","sequence_number":5,"item_id":"success-item","output_index":0,"content_index":0,"part":{"type":"output_text","text":"hello","annotations":[]}}',
	'{"type":"response.output_item.done","sequence_number":6,"output_index":0,"item":{"id":"success-item","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"hello","annotations":[]}]}}',
	'{"type":"response.completed","sequence_number":7,"response":{"id":"azure-preamble-success","object":"response","status":"completed","model":"preamble-success","output":[{"id":"success-item","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"hello","annotations":[]}]}],"usage":{"input_tokens":10,"output_tokens":1,"total_tokens":11}}}',
];

const server = http.createServer(async (req, res) => {
	const path = new URL(req.url, "http://localhost").pathname;
	const responses = path === "/openai/v1/responses";
	if (req.method !== "POST" || (!responses && path !== "/openai/v1/chat/completions")) {
		res.writeHead(404).end();
		return;
	}
	try {
		req.setEncoding("utf8");
		let body = "";
		for await (const chunk of req) body += chunk;
		const { model } = JSON.parse(body);
		if (model !== "preamble-error" && model !== "preamble-success") {
			res.writeHead(400).end("unknown fixture model");
			return;
		}
		const failed = model === "preamble-error";
		const events = responses
			? (failed ? responsesFailure : responsesSuccess)
			: (failed ? chatFailure : chatSuccess);
		res.writeHead(200, { "Content-Type": "text/event-stream" });
		res.flushHeaders();
		for (const event of events) res.write(`data: ${event}\n\n`);
		res.end("data: [DONE]\n\n");
	} catch {
		res.writeHead(400).end("invalid fixture request");
	}
});

server.listen(port, "127.0.0.1", () => {
	console.log(`Azure streaming fixture: http://127.0.0.1:${port}`);
});
for (const signal of ["SIGINT", "SIGTERM"]) {
	process.on(signal, () => server.close(() => process.exit(0)));
}
