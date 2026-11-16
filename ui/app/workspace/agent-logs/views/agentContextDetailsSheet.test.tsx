import "@/lib/i18n";
import type { AgentLogOperation } from "@/lib/types/agentLogs";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import { A2AOperationConversation } from "./agentContextDetailsSheet";

// The editor mounts Monaco after hydration; expose its input in server-rendered assertions.
vi.mock("@/components/ui/codeEditor", () => ({
	CodeEditor: ({ code }: { code: string }) => <pre>{code}</pre>,
}));

const question = { messageId: "question", role: "ROLE_USER", parts: [{ text: "give me number of issues raised yday" }] };
const task = {
	id: "task-1",
	contextId: "context-1",
	status: { state: "TASK_STATE_COMPLETED", timestamp: "2026-11-11T10:08:59.168192Z" },
	history: [question, { messageId: "progress", role: "ROLE_AGENT", parts: [{ text: "answer ready" }] }],
	artifacts: [
		{ artifactId: "artifact-1", name: "revops-copilot-result", parts: [{ text: "A total of 8 issues were raised on 2026-11-10." }] },
	],
};

function operation(bodies: unknown[], response?: unknown): AgentLogOperation {
	return {
		id: "request-1",
		timestamp: "2026-11-11T10:08:38.503Z",
		record_kind: "request",
		operation: "SendStreamingMessage",
		status: "success",
		agent_name: "revops-copilot",
		request_id: "request-1",
		task_id: task.id,
		request_body: JSON.stringify({ message: question }),
		response_body: response ? JSON.stringify(response) : undefined,
		events: bodies.map((body, index) => ({
			id: `event-${index}`,
			timestamp: "2026-11-11T10:08:59.307Z",
			record_kind: "event",
			operation: "SendStreamingMessage",
			status: "success",
			agent_name: "revops-copilot",
			request_id: "request-1",
			task_id: task.id,
			event_body: JSON.stringify(body),
		})),
	};
}

function render(log: AgentLogOperation) {
	return renderToStaticMarkup(<A2AOperationConversation log={log} />);
}

describe("Task snapshots in agent conversations", () => {
	it("renders the completed streaming Task artifact when there is no response body or status message", () => {
		const html = render(operation([task]));
		expect(html).toContain("revops-copilot-result");
		expect(html).toContain("A total of 8 issues");
	});
	it("renders history from a streaming Task snapshot", () => {
		const html = render(operation([task]));
		expect(html).toContain("Conversation history");
		expect(html).toContain("answer ready");
	});
	it("uses the latest snapshot history without repeating cumulative snapshots", () => {
		const html = render(operation([task, { ...task, history: [...task.history, { messageId: "final", parts: [{ text: "finished" }] }] }]));
		expect(html.match(/answer ready/g)).toHaveLength(1);
		expect(html).toContain("finished");
		expect(html.match(/A total of 8 issues/g)).toHaveLength(1);
	});
	it("preserves unary Task artifacts and history", () => {
		const html = render({ ...operation([], task), operation: "SendMessage" });
		expect(html).toContain("A total of 8 issues");
		expect(html).toContain("answer ready");
	});
	it("preserves bare status updates with a context ID", () => {
		const html = render(
			operation([
				{
					taskId: task.id,
					contextId: task.contextId,
					status: { state: "TASK_STATE_WORKING", message: { parts: [{ text: "working now" }] } },
				},
			]),
		);
		expect(html).toContain("working now");
		expect(html).not.toContain("Conversation history");
	});
	it("preserves wrapped status and artifact updates, including append", () => {
		const html = render(
			operation([
				{ statusUpdate: { taskId: task.id, status: { message: { parts: [{ text: "working now" }] } } } },
				{ artifactUpdate: { taskId: task.id, artifact: { artifactId: "a", parts: [{ text: "first chunk" }] } } },
				{ artifactUpdate: { taskId: task.id, append: true, artifact: { artifactId: "a", parts: [{ text: "second chunk" }] } } },
			]),
		);
		expect(html).toContain("working now");
		expect(html).toContain("first chunk");
		expect(html).toContain("second chunk");
	});
	it("keeps every history message, status message, and artifact part", () => {
		const snapshot = {
			...task,
			status: { ...task.status, message: { messageId: "status", parts: [{ text: "report completed" }] } },
			history: [
				...task.history,
				{
					messageId: "tool",
					role: "ROLE_AGENT",
					parts: [{ text: "searched issues" }, { data: { progress: { detail: "10 tools visible" } } }],
				},
			],
			artifacts: [
				...task.artifacts,
				{ artifactId: "data", parts: [{ data: { steps: [{ agent: "support-analyst" }] } }, { text: "second artifact text" }] },
			],
		};
		const html = render(operation([snapshot]));
		for (const text of [
			"answer ready",
			"report completed",
			"searched issues",
			"10 tools visible",
			"support-analyst",
			"second artifact text",
			"A total of 8 issues",
		]) {
			expect(html).toContain(text);
		}
	});
	it("reads a standalone event record as well as correlated events", () => {
		const html = render({ ...operation([]), event_body: JSON.stringify(task) });
		expect(html).toContain("A total of 8 issues");
		expect(html).toContain("answer ready");
	});
	it("keeps snapshot history after a later status-only event", () => {
		const html = render(operation([task, { taskId: task.id, contextId: task.contextId, status: { state: "TASK_STATE_COMPLETED" } }]));
		expect(html).toContain("answer ready");
	});
	for (const source of ["standalone snapshot", "correlated snapshot", "artifact update"] as const) {
		it(`keeps final response artifacts authoritative over an earlier ${source}`, () => {
			const earlier = { ...task, artifacts: [{ ...task.artifacts[0], parts: [{ text: "Still counting issues" }] }] };
			const event = source === "artifact update" ? { taskId: task.id, contextId: task.contextId, artifact: earlier.artifacts[0] } : earlier;
			const log =
				source === "standalone snapshot" ? { ...operation([], task), event_body: JSON.stringify(event) } : operation([event], task);
			const html = render(log);
			expect(html).toContain("A total of 8 issues");
			expect(html).not.toContain("Still counting issues");
		});
	}
	it("uses the final response history instead of an earlier snapshot", () => {
		const html = render(operation([task], { ...task, history: [{ messageId: "final", parts: [{ text: "final response history" }] }] }));
		expect(html).toContain("final response history");
		expect(html).not.toContain("answer ready");
	});
	it("preserves bare artifact updates", () => {
		const html = render(operation([{ taskId: task.id, contextId: task.contextId, artifact: task.artifacts[0] }]));
		expect(html).toContain("A total of 8 issues");
	});
});