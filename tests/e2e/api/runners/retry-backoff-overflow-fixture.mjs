import http from "node:http";
import { createHash } from "node:crypto";
import { performance } from "node:perf_hooks";
import { pathToFileURL } from "node:url";

const FINAL_ATTEMPT = 42;
const FIRST_CHECKED_ATTEMPT = 39;
const MIN_GAP_MS = 60;
const ENTRY_TTL_MS = 60000;
const MAX_ENTRIES = 128;
const MAX_BODY_BYTES = 32768;

// Export the handler so its attempt boundaries can be tested without opening a socket.
export function createRetryBackoffOverflowFixture({ now = () => performance.now() } = {}) {
	const attempts = new Map();
	const error = (res, status, message, type = "invalid_request_error") => {
		res.writeHead(status, { "Content-Type": "application/json" });
		res.end(JSON.stringify({ error: { message, type } }));
	};
	return async (req, res) => {
		if (req.method !== "POST" || req.url !== "/v1/chat/completions") {
			error(res, 404, "unknown fixture route");
			return;
		}
		try {
			let raw = "";
			let bytes = 0;
			for await (const chunk of req) {
				bytes += Buffer.byteLength(chunk);
				if (bytes > MAX_BODY_BYTES) {
					error(res, 400, "fixture request too large");
					return;
				}
				raw += chunk;
			}
			const { model, messages, stream } = JSON.parse(raw);
			const isStream = model === "retry-backoff-overflow-stream";
			// Unary conversion omits the optional stream field; streaming sets it true.
			const validStream = isStream ? stream === true : stream === undefined || stream === false;
			if (!["retry-backoff-overflow-json", "retry-backoff-overflow-stream"].includes(model)
				|| !validStream || req.headers.authorization !== "Bearer fixture-only"
				|| typeof messages?.[0]?.content !== "string" || !messages[0].content) {
				error(res, 400, "invalid fixture model, stream flag, credential or nonce");
				return;
			}
			const receivedAt = now();
			for (const [id, entry] of attempts) {
				if (receivedAt - entry.created > ENTRY_TTL_MS) attempts.delete(id);
			}
			const id = createHash("sha256").update(raw).digest("hex");
			let entry = attempts.get(id);
			if (!entry) {
				if (attempts.size >= MAX_ENTRIES) {
					error(res, 400, "fixture active request limit reached");
					return;
				}
				entry = { created: receivedAt, count: 0, lastReplyAt: receivedAt };
				attempts.set(id, entry);
			}
			entry.count++;
			// Call 39 is calculateBackoff(37): 100ms * 2^37 wraps negative before capping.
			// Correct capped jitter is >=80ms. Check late retries individually, allowing 20ms
			// of tolerance rather than asserting an accumulated wall-clock duration.
			if (entry.count >= FIRST_CHECKED_ATTEMPT && receivedAt - entry.lastReplyAt < MIN_GAP_MS) {
				attempts.delete(id);
				error(res, 400, "retry-backoff-overflow-early");
				return;
			}
			if (entry.count < FINAL_ATTEMPT) {
				entry.lastReplyAt = now();
				// No Retry-After hint: this pins exponential backoff, not hint handling.
				error(res, 503, "fixture transient server failure", "server_error");
				return;
			}
			attempts.delete(id);
			const content = "backoff-overflow-ok:42";
			const base = { id: "backoff-overflow-42", created: 1, model };
			if (isStream) {
				res.writeHead(200, { "Content-Type": "text/event-stream" });
				res.write(`data: ${JSON.stringify({ ...base, object: "chat.completion.chunk", choices: [{ index: 0, delta: { role: "assistant", content }, finish_reason: null }] })}\n\n`);
				res.write(`data: ${JSON.stringify({ ...base, object: "chat.completion.chunk", choices: [{ index: 0, delta: {}, finish_reason: "stop" }], usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 } })}\n\n`);
				res.end("data: [DONE]\n\n");
			} else {
				res.writeHead(200, { "Content-Type": "application/json" });
				res.end(JSON.stringify({ ...base, object: "chat.completion", choices: [{ index: 0, message: { role: "assistant", content }, finish_reason: "stop" }], usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 } }));
			}
		} catch {
			error(res, 400, "invalid fixture request");
		}
	};
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
	const port = Number(process.env.RETRY_BACKOFF_OVERFLOW_FIXTURE_PORT || "8791");
	if (!Number.isInteger(port) || port < 1 || port > 65535) throw new Error("invalid fixture port");
	const server = http.createServer(createRetryBackoffOverflowFixture());
	server.listen(port, "127.0.0.1", () => console.log(`Retry backoff overflow fixture: http://127.0.0.1:${port}`));
	for (const signal of ["SIGINT", "SIGTERM"]) {
		process.on(signal, () => server.close(() => process.exit(0)));
	}
}
