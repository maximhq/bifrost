import http from "node:http";

// Scripted OpenAI-compatible upstream for the MCP Code Mode e2e suite
// (runners/individual/run-newman-mcp-codemode-tests.sh).
//
// The agent loop only runs when the model answers with tool calls, so a real
// model would make the unattended Code Mode path both paid and nondeterministic.
// This fixture answers the first turn of a conversation with one scripted
// executeToolCode call, chosen by a "[scenario:<name>]" marker in the user
// message, and answers any turn that already carries a tool result with plain
// text so the loop ends.
//
// Every chat request body is recorded per scenario and served back at
// GET /__requests?scenario=<name>&nonce=<run nonce>, so a test can assert what
// Bifrost fed the model after running the code (for example, that a nested tool
// call was refused). The nonce filter keeps reruns against one fixture process
// from counting each other's requests.

const port = Number(process.env.CODEMODE_LLM_FIXTURE_PORT || "8791");

// Code the fake model asks Bifrost to run, per scenario. The Code Mode client is
// named "codemcp"; see write_config in the runner for its allow-lists.
const scenarios = {
	// echo is on tools_to_auto_execute: passes the pre-flight scan and runs.
	agent_auto_echo: 'result = codemcp.echo(message="agent-auto")',
	// add is executable but not auto-executable: the scan sends it back for approval.
	agent_direct_add: "result = codemcp.add(a=2, b=3)",
	// getattr hides add from the scan, so the code runs unattended and only the
	// invocation-time check can refuse the nested call.
	agent_getattr_add: 'result = getattr(codemcp, "add")(a=2, b=3)',
	// The scan reads the whole source, so a call inside a def is still caught.
	agent_def_add: "def helper():\n    return codemcp.add(a=2, b=3)\n\nresult = helper()",
};

const recorded = new Map();

function userText(messages) {
	return (messages || [])
		.filter((m) => m.role === "user")
		.map((m) => (typeof m.content === "string" ? m.content : JSON.stringify(m.content)))
		.join("\n");
}

function scenarioOf(messages) {
	const match = /\[scenario:([a-z_]+)\]/.exec(userText(messages));
	return match ? match[1] : "";
}

function completion(id, message, finishReason) {
	return {
		id,
		object: "chat.completion",
		created: Math.floor(Date.now() / 1000),
		model: "codemode-fixture",
		choices: [{ index: 0, message, finish_reason: finishReason }],
		usage: { prompt_tokens: 10, completion_tokens: 5, total_tokens: 15 },
	};
}

function send(res, status, body) {
	res.writeHead(status, { "content-type": "application/json" });
	res.end(JSON.stringify(body));
}

const server = http.createServer((req, res) => {
	const url = new URL(req.url, `http://localhost:${port}`);

	if (req.method === "GET" && url.pathname === "/__requests") {
		const nonce = url.searchParams.get("nonce") || "";
		const requests = (recorded.get(url.searchParams.get("scenario") || "") || []).filter(
			(body) => nonce === "" || userText(body.messages).includes(nonce),
		);
		send(res, 200, { requests });
		return;
	}

	if (req.method !== "POST" || !url.pathname.endsWith("/chat/completions")) {
		send(res, 404, { error: { message: `codemode fixture: unhandled ${req.method} ${url.pathname}` } });
		return;
	}

	let raw = "";
	req.on("data", (chunk) => {
		raw += chunk;
	});
	req.on("end", () => {
		let body;
		try {
			body = JSON.parse(raw);
		} catch {
			send(res, 400, { error: { message: "codemode fixture: invalid JSON body" } });
			return;
		}

		const scenario = scenarioOf(body.messages);
		if (!recorded.has(scenario)) recorded.set(scenario, []);
		recorded.get(scenario).push(body);

		const code = scenarios[scenario];
		if (!code) {
			send(res, 400, { error: { message: `codemode fixture: unknown scenario "${scenario}"` } });
			return;
		}

		// A turn that already carries a tool result ends the loop.
		if ((body.messages || []).some((m) => m.role === "tool")) {
			send(res, 200, completion(`fixture-${scenario}-final`, { role: "assistant", content: `done:${scenario}` }, "stop"));
			return;
		}

		send(
			res,
			200,
			completion(
				`fixture-${scenario}-call`,
				{
					role: "assistant",
					content: null,
					tool_calls: [
						{
							id: `call_${scenario}`,
							type: "function",
							function: { name: "executeToolCode", arguments: JSON.stringify({ code }) },
						},
					],
				},
				"tool_calls",
			),
		);
	});
});

server.listen(port, "127.0.0.1", () => {
	console.log(`codemode LLM fixture listening on http://127.0.0.1:${port}`);
});
