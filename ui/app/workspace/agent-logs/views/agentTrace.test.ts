import type { AgentLogOperation } from "@/lib/types/agentLogs";
import type { LogEntry, MCPToolLogEntry } from "@/lib/types/logs";
import { describe, expect, it } from "vitest";
import { buildTrace, createLimiter, mergeOperations, operationPrompt, operationReply, type TraceData } from "./agentTrace";

const at = (offsetMs: number) => new Date(Date.UTC(2026, 10, 11, 10, 42, 18) + offsetMs).toISOString();

function operation(
	id: string,
	agent: string,
	taskId: string,
	offset: number,
	latency: number,
	extra: Partial<AgentLogOperation> = {},
): AgentLogOperation {
	return {
		id,
		timestamp: at(offset),
		record_kind: "request",
		operation: "SendMessage",
		status: "success",
		agent_name: agent,
		request_id: `req-${id}`,
		task_id: taskId,
		context_id: "ctx-1",
		latency,
		events: [],
		...extra,
	};
}

function llm(id: string, offset: number, tokens: number, cost: number, status = "success"): LogEntry {
	return {
		id,
		timestamp: at(offset),
		provider: "openai",
		model: "gpt-4o-mini",
		object: "chat.completion",
		latency: 500,
		status,
		cost,
		token_usage: { total_tokens: tokens },
	} as LogEntry;
}

function mcp(id: string, offset: number, status = "success"): MCPToolLogEntry {
	return { id, timestamp: at(offset), tool_name: "Pylon-search_issues", server_label: "Pylon", latency: 300, status } as MCPToolLogEntry;
}

const artifactEvent = {
	id: "ev-pdf",
	timestamp: at(30_000),
	record_kind: "event" as const,
	operation: "SendMessage",
	status: "success" as const,
	agent_name: "report-writer",
	request_id: "req-writer",
	event_type: "artifact_update",
	event_body: JSON.stringify({ artifactUpdate: { artifact: { artifactId: "art-pdf", name: "report.pdf", parts: [] } } }),
};

function trace(): TraceData {
	return {
		operations: [
			operation("root", "revops-copilot", "task-root", 0, 40_000),
			operation("analyst", "support-analyst", "task-analyst", 2_000, 10_000, { caller_correlation_id: "task-root" }),
			// A GetTask poll on the analyst's task folds into the analyst node.
			operation("analyst-poll", "support-analyst", "task-analyst", 9_000, 50, { caller_correlation_id: "task-root", operation: "GetTask" }),
			operation("writer", "report-writer", "task-writer", 14_000, 18_000, { caller_correlation_id: "task-root", events: [artifactEvent] }),
		],
		llm: [
			{ taskId: "task-root", entry: llm("llm-root", 300, 1_900, 0.0003) },
			{ taskId: "task-analyst", entry: llm("llm-analyst", 2_100, 10_000, 0.0016) },
			{ taskId: "task-writer", entry: llm("llm-writer", 14_100, 8_400, 0.0019, "error") },
		],
		mcp: [{ taskId: "task-analyst", entry: mcp("mcp-analyst", 3_000) }],
		loading: false,
		partial: false,
	};
}

describe("buildTrace guardrails", () => {
	it("adds one guardrail span per checked rule under its LLM call, the strongest outcome winning", () => {
		const data = trace();
		const guarded = llm("llm-guarded", 300, 1_900, 0.0003);
		guarded.guardrail_debug = {
			judge_calls: [
				{
					phase: "input",
					rule_id: 1,
					rule_name: "prompt-scan",
					guardrail_name: "Model Armor",
					guardrail_provider: "google",
					action: "allow",
					judge_model: "gemini-2.5-flash",
				},
				{ phase: "output", rule_id: 2, rule_name: "pii", guardrail_name: "PII redaction", guardrail_provider: "bifrost" },
			],
			detections: [{ phase: "output", rule_id: 2, rule_name: "pii", guardrail_name: "PII redaction", action: "redact", enforced: true }],
			interventions: [{ phase: "input", rule_id: 3, rule_name: "jailbreak", guardrail_name: "Model Armor", reason: "prompt injection" }],
		};
		data.llm[0] = { taskId: "task-root", entry: guarded };
		const { nodes } = buildTrace("root", data);
		const llmNode = nodes.get("llm:llm-guarded")!;
		const guards = llmNode.children.map((id) => nodes.get(id)!);
		expect(guards.map((node) => [node.kind, node.name, node.guard?.phase, node.guard?.outcome, node.status])).toEqual([
			["guard", "Model Armor", "input", "allow", "success"],
			["guard", "Model Armor", "input", "blocked", "error"],
			["guard", "PII redaction", "output", "redact", "success"],
		]);
		// Input checks run before the call and output checks after it.
		expect(guards[0].start).toBe(llmNode.start);
		expect(guards[2].start).toBe(llmNode.end);
		expect(nodes.get("agent:task-root")!.errors).toBeGreaterThanOrEqual(1);
	});
});

describe("buildTrace", () => {
	it("nests delegated agents under their caller and calls under the agent that made them", () => {
		const { nodes, rootKey } = buildTrace("root", trace());
		expect(rootKey).toBe("task-root");
		const root = nodes.get("agent:task-root")!;
		expect(root.parentId).toBeNull();
		expect(root.children).toEqual(["llm:llm-root", "agent:task-analyst", "agent:task-writer"]);
		expect(nodes.get("agent:task-analyst")!.children).toEqual(["llm:llm-analyst", "mcp:mcp-analyst"]);
		expect(nodes.get("agent:task-writer")!.children).toEqual(["llm:llm-writer", "artifact:task-writer:art-pdf"]);
	});

	it("folds a caller's task-less GetAgentCard lookup into that agent's node instead of adding an empty one", () => {
		const data = trace();
		data.operations.push(
			operation("analyst-card", "support-analyst", "", 1_900, 20, {
				caller_correlation_id: "task-root",
				operation: "GetAgentCard",
				task_id: undefined,
			}),
			// A card lookup for an agent that never got a task stays visible on its own.
			operation("orphan-card", "publisher", "", 33_000, 20, {
				caller_correlation_id: "task-root",
				operation: "GetAgentCard",
				task_id: undefined,
			}),
		);
		const { nodes } = buildTrace("root", data);
		expect(nodes.has("agent:analyst-card")).toBe(false);
		expect(nodes.get("agent:task-analyst")!.followUps).toBe(2);
		expect(nodes.get("agent:task-analyst")!.agent?.id).toBe("analyst");
		expect(nodes.get("agent:orphan-card")!.parentId).toBe("agent:task-root");
	});

	it("in session mode, roots the tree at the session with each top-level turn numbered in time order", () => {
		const data = trace();
		// The CLI starts every turn with its own correlation id, which no loaded agent owns.
		data.operations[0] = { ...data.operations[0], caller_correlation_id: "cli-1" };
		data.operations.push(operation("turn2", "revops-copilot", "task-turn2", 60_000, 5_000, { caller_correlation_id: "cli-2" }));
		data.llm.push({ taskId: "task-turn2", entry: llm("llm-turn2", 60_100, 1_000, 0.0001) });
		const { nodes, rootNodeId } = buildTrace("root", data, { sessionId: "ctx-1" });
		expect(rootNodeId).toBe("session:ctx-1");
		const session = nodes.get("session:ctx-1")!;
		expect(session).toMatchObject({ kind: "session", parentId: null });
		expect(session.children).toEqual(["agent:task-root", "agent:task-turn2"]);
		expect(nodes.get("agent:task-root")!.turn).toBe(1);
		expect(nodes.get("agent:task-turn2")!.turn).toBe(2);
		expect(nodes.get("agent:task-analyst")!.parentId).toBe("agent:task-root");
		expect(session.tokens).toBe(1_900 + 10_000 + 8_400 + 1_000);
		expect(session.start).toBe(nodes.get("agent:task-root")!.start);
		expect(session.end).toBe(nodes.get("agent:task-turn2")!.end);
	});

	it("nests a grandchild agent under the subagent that delegated it", () => {
		const data = trace();
		data.operations.push(operation("deep", "pylon-specialist", "task-deep", 4_000, 2_000, { caller_correlation_id: "task-analyst" }));
		const { nodes } = buildTrace("root", data);
		expect(nodes.get("agent:task-deep")!.parentId).toBe("agent:task-analyst");
		expect(nodes.get("agent:task-analyst")!.children).toContain("agent:task-deep");
	});

	it("folds follow-up operations on the same task into one agent node", () => {
		const { nodes } = buildTrace("root", trace());
		const analyst = nodes.get("agent:task-analyst")!;
		expect(analyst.agent?.id).toBe("analyst");
		expect(analyst.followUps).toBe(1);
	});

	it("rolls tokens, cost, errors and span counts up to every ancestor", () => {
		const { nodes } = buildTrace("root", trace());
		const root = nodes.get("agent:task-root")!;
		expect(root.tokens).toBe(1_900 + 10_000 + 8_400);
		expect(root.cost).toBeCloseTo(0.0003 + 0.0016 + 0.0019, 6);
		expect(root.errors).toBe(1);
		expect(nodes.get("agent:task-writer")!.errors).toBe(1);
		// 2 agents + 3 LLM + 1 MCP below the root; the artifact is not a span.
		expect(root.spanCount).toBe(6);
	});

	it("records the artifact's producer and emit time", () => {
		const { nodes } = buildTrace("root", trace());
		const artifact = nodes.get("artifact:task-writer:art-pdf")!;
		expect(artifact).toMatchObject({ kind: "artifact", name: "report.pdf", producer: "report-writer" });
		expect(artifact.start).toBe(new Date(at(30_000)).getTime());
	});

	it("lets a later snapshot of an artifact replace the earlier one", () => {
		const data = trace();
		const partial = {
			...artifactEvent,
			id: "ev-partial",
			event_body: JSON.stringify({ artifactUpdate: { artifact: { artifactId: "art-pdf", parts: [{ text: "draft" }] } } }),
		};
		data.operations[3] = { ...data.operations[3], events: [partial] };
		// The GetTask poll after the stream returns the finished artifact under the same id.
		data.operations.push(
			operation("writer-poll", "report-writer", "task-writer", 33_000, 50, {
				operation: "GetTask",
				caller_correlation_id: "task-root",
				response_body: JSON.stringify({
					result: { task: { artifacts: [{ artifactId: "art-pdf", name: "report.pdf", parts: [{ text: "final" }] }] } },
				}),
			}),
		);
		const { nodes } = buildTrace("root", data);
		expect(nodes.get("artifact:task-writer:art-pdf")!.artifact!.artifact.parts).toEqual([{ text: "final" }]);
	});

	it("never links two agents that name each other as caller into a cycle", () => {
		const data = trace();
		data.operations.push(
			operation("loop-a", "agent-a", "task-a", 20_000, 100, { caller_correlation_id: "task-b" }),
			operation("loop-b", "agent-b", "task-b", 21_000, 100, { caller_correlation_id: "task-a" }),
		);
		const { nodes } = buildTrace("root", data);
		for (const id of ["agent:task-a", "agent:task-b"]) {
			const seen = new Set<string>();
			let parent: string | null = id;
			while (parent && !seen.has(parent)) {
				seen.add(parent);
				parent = nodes.get(parent)?.parentId ?? null;
			}
			// The walk reaches the root (null) instead of revisiting a node.
			expect(parent).toBeNull();
		}
	});

	it("takes the reply from a follow-up that finished the task, not the working first response", () => {
		const data = trace();
		const working = JSON.stringify({
			result: { task: { status: { state: "TASK_STATE_WORKING", message: { parts: [{ text: "working on it" }] } } } },
		});
		const done = JSON.stringify({
			result: { task: { status: { state: "TASK_STATE_COMPLETED", message: { parts: [{ text: "212 issues in Q3." }] } } } },
		});
		data.operations[1] = { ...data.operations[1], response_body: working };
		data.operations[2] = { ...data.operations[2], response_body: done };
		const { nodes } = buildTrace("root", data);
		const analyst = nodes.get("agent:task-analyst")!;
		expect(analyst.agent?.id).toBe("analyst"); // the prompt and detail still come from the first operation
		expect(analyst.reply).toBe("212 issues in Q3.");
	});

	it("attaches an agent whose caller was not loaded to the root rather than dropping it", () => {
		const data = trace();
		data.operations.push(operation("orphan", "publisher", "task-orphan", 33_000, 6_000, { caller_correlation_id: "task-missing" }));
		const { nodes } = buildTrace("root", data);
		expect(nodes.get("agent:task-orphan")!.parentId).toBe("agent:task-root");
	});
});
describe("artifact kind labels", () => {
	const locales = ["de", "en", "es", "fr", "it", "ja", "ko", "pt", "ru", "zh-CN", "zh-TW"];

	it("names the kind; only a file's media type is shown as-is", async () => {
		const { artifactType } = await import("./agentTrace");
		expect(artifactType({ parts: [{ text: "hi" }, { data: { a: 1 } }] })).toEqual({ kind: "data" });
		expect(artifactType({ parts: [{ text: "hi" }] })).toEqual({ kind: "text" });
		expect(artifactType({ parts: [{ file: { mediaType: "application/pdf", name: "r.pdf" } }] })).toEqual({
			kind: "file",
			mediaType: "application/pdf",
		});
	});

	it("translates the generated kind labels in every locale", async () => {
		const { readFileSync } = await import("node:fs");
		for (const locale of locales) {
			const trace = JSON.parse(readFileSync(`locales/${locale}/observability.json`, "utf8")).agentLogs.trace;
			expect(trace.artifactKind?.data, `${locale} artifactKind.data`).toBeTruthy();
			expect(trace.artifactKind?.text, `${locale} artifactKind.text`).toBeTruthy();
		}
	});
});

describe("operationPrompt", () => {
	it("pulls the user's text out of a JSON-RPC A2A request body", () => {
		const body = JSON.stringify({
			jsonrpc: "2.0",
			method: "SendStreamingMessage",
			params: { configuration: { blocking: false }, message: { role: "ROLE_USER", parts: [{ text: "Prepare the Q3 report" }] } },
		});
		expect(operationPrompt(body)).toBe("Prepare the Q3 report");
	});

	it("reads the text from a preview cut off mid-JSON", () => {
		const preview =
			'{"configuration":{"blocking":false},"message":{"parts":[{"text":"Which accounts breached \\"SLA\\"?"}],"role":"ROLE_US';
		expect(operationPrompt(preview)).toBe('Which accounts breached "SLA"?');
	});

	it("keeps plain text and returns undefined for nothing", () => {
		expect(operationPrompt("hello")).toBe("hello");
		expect(operationPrompt(undefined)).toBeUndefined();
	});
});
describe("mergeOperations", () => {
	it("adds new operations and lets a hydrated operation replace its summary, never the reverse", () => {
		const summary = operation("op-1", "revops-copilot", "task-1", 0, 100);
		const hydrated = { ...summary, events: [{ ...summary, id: "ev-1", record_kind: "event" as const }] };
		let merged = mergeOperations(new Map(), [summary], false);
		expect(merged.get("op-1")?.events).toEqual([]);
		merged = mergeOperations(merged, [hydrated], true);
		expect(merged.get("op-1")?.events).toHaveLength(1);
		merged = mergeOperations(merged, [summary, operation("op-2", "publisher", "task-2", 5, 10)], false);
		expect(merged.get("op-1")?.events).toHaveLength(1);
		expect([...merged.keys()]).toEqual(["op-1", "op-2"]);
	});
});

describe("createLimiter", () => {
	it("never runs more than the limit at once and runs every task", async () => {
		const limit = createLimiter(2);
		let running = 0;
		let peak = 0;
		const results = await Promise.all(
			[1, 2, 3, 4, 5].map((value) =>
				limit(async () => {
					running++;
					peak = Math.max(peak, running);
					await new Promise((resolve) => setTimeout(resolve, 5));
					running--;
					return value * 10;
				}),
			),
		);
		expect(results).toEqual([10, 20, 30, 40, 50]);
		expect(peak).toBe(2);
	});

	it("keeps going after a task fails", async () => {
		const limit = createLimiter(1);
		await expect(limit(() => Promise.reject(new Error("boom")))).rejects.toThrow("boom");
		await expect(limit(() => Promise.resolve("ok"))).resolves.toBe("ok");
	});
});

describe("operationReply", () => {
	const op = operation("op-r", "revops-copilot", "task-r", 0, 100);
	const event = (seq: number, type: string, body: unknown) => ({
		...op,
		id: `ev-${seq}`,
		record_kind: "event" as const,
		event_sequence: seq,
		event_type: type,
		event_body: JSON.stringify(body),
	});

	it("prefers the agent's final status message", () => {
		const events = [
			event(1, "status_update", {
				status: { state: "TASK_STATE_WORKING", message: { role: "ROLE_AGENT", parts: [{ text: "open /mcp" }] } },
			}),
			event(2, "status_update", {
				status: { state: "TASK_STATE_COMPLETED", message: { role: "ROLE_AGENT", parts: [{ text: "All done." }] } },
			}),
		];
		expect(operationReply({ ...op, events })).toBe("All done.");
	});

	it("falls back to the text parts of the last artifact", () => {
		const events = [
			event(1, "status_update", {
				status: { state: "TASK_STATE_WORKING", message: { role: "ROLE_AGENT", parts: [{ text: "open /mcp" }] } },
			}),
			event(2, "artifact_update", { artifact: { parts: [{ text: "The weather in Berlin is 72°F." }, { data: { steps: [] } }] } }),
			event(3, "status_update", { status: { state: "TASK_STATE_COMPLETED" } }),
		];
		expect(operationReply({ ...op, events })).toBe("The weather in Berlin is 72°F.");
	});

	it("reads a non-streaming response's final Task", () => {
		const response_body = JSON.stringify({
			result: { task: { status: { state: "TASK_STATE_COMPLETED", message: { parts: [{ text: "391" }] } }, artifacts: [] } },
		});
		expect(operationReply({ ...op, events: [], response_body })).toBe("391");
		expect(operationReply({ ...op, events: [] })).toBeUndefined();
	});

	it("joins an artifact's appended chunks before reading the answer", () => {
		const events = [
			event(1, "artifact_update", { artifactUpdate: { artifact: { artifactId: "answer", parts: [{ text: "Part one" }] } } }),
			event(2, "artifact_update", { artifactUpdate: { append: true, artifact: { artifactId: "answer", parts: [{ text: "Part two" }] } } }),
		];
		expect(operationReply({ ...op, events })).toBe("Part one\nPart two");
	});

	it("never takes a working status from a non-streaming response as the answer", () => {
		const response_body = JSON.stringify({
			result: { task: { status: { state: "TASK_STATE_WORKING", message: { parts: [{ text: "working on it" }] } } } },
		});
		expect(operationReply({ ...op, events: [], response_body })).toBeUndefined();
	});
});