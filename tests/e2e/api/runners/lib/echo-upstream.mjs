// Local echo upstream shared by the runners that compare LLM rows with relay rows. One server
// answers both kinds of traffic, so a pair of requests can carry the same marker:
//
//   POST /v1/chat/completions   an OpenAI-compatible chat reply "reply-<marker>", where <marker> is
//                               whatever follows "prompt-" in the last user message; model
//                               "echo-fail" answers 500 instead
//   *    /relay-echo/...        {"echo": <request body>, "reply": "reply-<marker>", "path", "query"}
//                               with the marker taken from the body's "prompt" field
//
// No provider credentials and no paid calls: the gateway reaches it on loopback.
import http from "node:http";
import { readBody } from "./gateway-api.mjs";

export const ECHO_MODEL = "echo-model";
// ECHO_FAIL_MODEL makes the chat endpoint answer 500, for an LLM error row.
export const ECHO_FAIL_MODEL = "echo-fail";

export function createEchoUpstream() {
	return http.createServer(async (req, res) => {
		const body = await readBody(req);
		const url = new URL(req.url || "/", "http://echo");
		if (url.pathname.startsWith("/relay-echo")) {
			let prompt = "";
			try {
				prompt = String(JSON.parse(body.toString("utf8")).prompt ?? "");
			} catch {
				prompt = "";
			}
			const echoed = prompt.startsWith("prompt-") ? prompt.slice("prompt-".length) : "none";
			res.writeHead(200, { "content-type": "application/json" });
			res.end(JSON.stringify({ echo: body.toString("utf8"), reply: `reply-${echoed}`, path: url.pathname, query: Object.fromEntries(url.searchParams) }));
			return;
		}
		if (req.method !== "POST" || url.pathname !== "/v1/chat/completions") {
			res.writeHead(404);
			res.end("not found");
			return;
		}
		let prompt = "";
		let model = "";
		try {
			const parsed = JSON.parse(body.toString("utf8"));
			const last = [...(parsed.messages || [])].reverse().find((m) => m.role === "user");
			prompt = typeof last?.content === "string" ? last.content : "";
			model = String(parsed.model || "");
		} catch {
			prompt = "";
		}
		if (model === ECHO_FAIL_MODEL) {
			res.writeHead(500, { "content-type": "application/json" });
			res.end(JSON.stringify({ error: { message: "echo upstream failure", type: "server_error" } }));
			return;
		}
		const echoed = prompt.startsWith("prompt-") ? prompt.slice("prompt-".length) : "none";
		const now = Math.floor(Date.now() / 1000);
		res.writeHead(200, { "content-type": "application/json" });
		res.end(
			JSON.stringify({
				id: `chatcmpl-${now}`,
				object: "chat.completion",
				created: now,
				model: ECHO_MODEL,
				choices: [{ index: 0, message: { role: "assistant", content: `reply-${echoed}` }, finish_reason: "stop" }],
				usage: { prompt_tokens: 5, completion_tokens: 3, total_tokens: 8 },
			}),
		);
	});
}

// addEchoProvider registers the echo upstream as a keyless OpenAI-compatible custom provider.
export async function addEchoProvider(api, providerName, providerBaseURL) {
	await api.mustRequest("POST", "/api/providers", {
		provider: providerName,
		custom_provider_config: { base_provider_type: "openai", is_key_less: true, allowed_requests: { chat_completion: true } },
		network_config: {
			base_url: providerBaseURL,
			allow_private_network: true,
			default_request_timeout_in_seconds: 10,
			max_retries: 0,
			retry_backoff_initial: 500,
			retry_backoff_max: 5000,
		},
		concurrency_and_buffer_size: { concurrency: 20, buffer_size: 200 },
		keys: [],
	});
}

// addEchoRelay registers a relay that forwards /relay/<name>/... to the echo upstream's
// /relay-echo/..., with a per-call fee so its rows are counted.
export async function addEchoRelay(api, relayName, upstreamBaseURL) {
	await api.mustRequest("POST", "/api/relays", {
		name: relayName,
		base_url: `${upstreamBaseURL}/relay-echo`,
		auth: { type: "none" },
		usage: { cost_per_request: 0.001 },
		routes: [{ method: "POST", path: "/**" }],
	});
}
