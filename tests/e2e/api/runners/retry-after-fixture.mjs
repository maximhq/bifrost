import http from "node:http";
import { createHash } from "node:crypto";

const port = Number(process.env.RETRY_AFTER_FIXTURE_PORT || "8790");
const attempts = new Map();
const server = http.createServer(async (req, res) => {
	if (req.method !== "POST" || req.url !== "/v1/chat/completions") {
		res.writeHead(404).end();
		return;
	}
	try {
		let body = "";
		for await (const chunk of req) body += chunk;
		const { model, stream } = JSON.parse(body);
		if (!["retry-after-seconds", "retry-after-date", "retry-after-cap"].includes(model)) {
			res.writeHead(400).end("unknown fixture model");
			return;
		}
		const key = createHash("sha256").update(body).digest("hex");
		const now = Date.now();
		// Keep a bounded history even when the gateway correctly stops without retrying.
		for (const [id, entry] of attempts) {
			if (now - entry.created > 120000) attempts.delete(id);
		}
		if (!attempts.has(key)) {
			const delay = model === "retry-after-cap" ? 60 : 2;
			const until = model === "retry-after-date"
				? Math.floor((now + delay * 1000) / 1000) * 1000
				: now + delay * 1000;
			attempts.set(key, { created: now, until });
			const hint = model === "retry-after-date" ? new Date(until).toUTCString() : String(delay);
			res.writeHead(429, { "Content-Type": "application/json", "Retry-After": hint });
			res.end(JSON.stringify({ error: { message: "fixture rate limit", type: "rate_limit_error" } }));
			return;
		}
		if (now < attempts.get(key).until) {
			// Non-retryable: an early attempt must fail loudly rather than eventually succeed.
			res.writeHead(400, { "Content-Type": "application/json" });
			res.end(JSON.stringify({ error: { message: "retry-before-retry-after", type: "invalid_request_error" } }));
			return;
		}
		attempts.delete(key);
		const base = { id: "retry-after-ok", created: 1, model };
		if (stream) {
			res.writeHead(200, { "Content-Type": "text/event-stream" });
			res.write(`data: ${JSON.stringify({ ...base, object: "chat.completion.chunk", choices: [{ index: 0, delta: { role: "assistant", content: "hello" }, finish_reason: null }] })}\n\n`);
			res.write(`data: ${JSON.stringify({ ...base, object: "chat.completion.chunk", choices: [{ index: 0, delta: {}, finish_reason: "stop" }] })}\n\n`);
			res.end("data: [DONE]\n\n");
		} else {
			res.writeHead(200, { "Content-Type": "application/json" });
			res.end(JSON.stringify({ ...base, object: "chat.completion", choices: [{ index: 0, message: { role: "assistant", content: "hello" }, finish_reason: "stop" }], usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 } }));
		}
	} catch {
		res.writeHead(400).end("invalid fixture request");
	}
});

server.listen(port, "127.0.0.1", () => console.log(`Retry-After fixture: http://127.0.0.1:${port}/v1`));
for (const signal of ["SIGINT", "SIGTERM"]) {
	process.on(signal, () => server.close(() => process.exit(0)));
}
