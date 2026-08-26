import { describe, expect, it } from "vitest";

import type { ResponsesMessage } from "@/lib/types/logs";

import { extractResponsesItemPayload, summarizeResponsesToolCall } from "./responsesItems";

const item = (value: Record<string, unknown>) => value as unknown as ResponsesMessage;

describe("extractResponsesItemPayload", () => {
	// One representative item per type that keeps its payload outside the fields the timeline
	// renders itself. Every entry showed an empty row before these fields were surfaced; a new
	// item type needs no entry here to render, this only pins the shapes providers send today.
	const payloadCarryingItems: [string, Record<string, unknown>, Record<string, unknown>][] = [
		[
			"web_search_call",
			{ id: "ws_1", type: "web_search_call", status: "completed", action: { type: "search", query: "bifrost" } },
			{ action: { type: "search", query: "bifrost" } },
		],
		[
			"web_fetch_call",
			{ type: "web_fetch_call", action: { type: "fetch", url: "https://example.com" }, web_fetch_result_url: "https://example.com" },
			{ action: { type: "fetch", url: "https://example.com" }, web_fetch_result_url: "https://example.com" },
		],
		["computer_call", { type: "computer_call", action: { type: "click", x: 12, y: 8 } }, { action: { type: "click", x: 12, y: 8 } }],
		[
			"local_shell_call",
			{ type: "local_shell_call", action: { type: "exec", command: ["ls", "-la"] } },
			{ action: { type: "exec", command: ["ls", "-la"] } },
		],
		["file_search_call", { type: "file_search_call", queries: ["invoice"], results: null }, { queries: ["invoice"] }],
		[
			"code_interpreter_call",
			{ type: "code_interpreter_call", container_id: "c_1", code: "print(1)", outputs: null },
			{ container_id: "c_1", code: "print(1)" },
		],
		["image_generation_call", { type: "image_generation_call", result: "iVBORw0KGgo=" }, { result: "iVBORw0KGgo=" }],
		[
			"custom_tool_call",
			{ type: "custom_tool_call", call_id: "c_1", name: "apply_patch", input: "*** Begin Patch" },
			{ input: "*** Begin Patch" },
		],
		[
			"tool_search_call",
			{ type: "tool_search_call", call_id: "c_1", execution: "client", arguments: { query: "web search", limit: 8 } },
			{ execution: "client", arguments: { query: "web search", limit: 8 } },
		],
		["mcp_call", { type: "mcp_call", name: "ask", server_label: "deepwiki", arguments: '{"q":1}' }, { server_label: "deepwiki" }],
		[
			"mcp_approval_request",
			{ type: "mcp_approval_request", action: { type: "mcp_approval_request", name: "ask", server_label: "deepwiki" } },
			{ action: { type: "mcp_approval_request", name: "ask", server_label: "deepwiki" } },
		],
		[
			"mcp_approval_responses",
			{ type: "mcp_approval_responses", approve: false, approval_request_id: "ar_1" },
			{ approve: false, approval_request_id: "ar_1" },
		],
		[
			"advisor_call",
			{ type: "advisor_call", result_type: "advisor_result", advisor_text: "consider X" },
			{ result_type: "advisor_result", advisor_text: "consider X" },
		],
		["compaction", { type: "compaction", encrypted_content: "gAAAA" }, { encrypted_content: "gAAAA" }],
	];

	it.each(payloadCarryingItems)("surfaces the payload of %s", (_type, source, expected) => {
		expect(extractResponsesItemPayload(item(source))).toEqual(expected);
	});

	it("leaves fields the row renders itself to the row", () => {
		// Shown as the row text.
		expect(extractResponsesItemPayload(item({ type: "function_call", name: "get_weather", arguments: '{"city":"SF"}' }))).toBeUndefined();
		// Shown by the output branch.
		expect(extractResponsesItemPayload(item({ type: "function_call_output", call_id: "c_1", output: "72F" }))).toBeUndefined();
		// Shown by the declared-tools branch.
		expect(extractResponsesItemPayload(item({ type: "additional_tools", tools: [{ name: "shell" }] }))).toBeUndefined();
		// Shown by the reasoning branch, but only on reasoning items.
		expect(
			extractResponsesItemPayload(item({ type: "reasoning", summary: [{ text: "thinking" }], encrypted_content: "gAAAA" })),
		).toBeUndefined();
		// Shown as the message body.
		expect(
			extractResponsesItemPayload(item({ id: "m_1", type: "message", role: "assistant", phase: "final_answer", content: [] })),
		).toBeUndefined();
	});

	it("ignores zero values a provider emits for unused fields, but keeps false", () => {
		expect(extractResponsesItemPayload(item({ type: "web_search_call", action: null, error: "", queries: [] }))).toBeUndefined();
		expect(extractResponsesItemPayload(item({ type: "mcp_approval_responses", approve: false }))).toEqual({ approve: false });
	});
});

describe("summarizeResponsesToolCall", () => {
	it("names the first query and counts the rest", () => {
		expect(
			summarizeResponsesToolCall(
				item({ type: "web_search_call", action: { type: "search", queries: ["bifrost routing rules", "bifrost CEL", "bifrost MCP"] } }),
			),
		).toBe("search · bifrost routing rules +2 more");
	});

	it("falls back to a single query, url, or shell command", () => {
		expect(summarizeResponsesToolCall(item({ type: "web_search_call", action: { type: "search", query: "latest news" } }))).toBe(
			"search · latest news",
		);
		expect(summarizeResponsesToolCall(item({ type: "web_fetch_call", action: { type: "fetch", url: "https://example.com" } }))).toBe(
			"fetch · https://example.com",
		);
		expect(summarizeResponsesToolCall(item({ type: "local_shell_call", action: { type: "exec", command: ["ls", "-la"] } }))).toBe(
			"exec · ls -la",
		);
	});

	it("uses the action type alone when it carries no detail", () => {
		expect(summarizeResponsesToolCall(item({ type: "computer_call", action: { type: "screenshot" } }))).toBe("screenshot");
	});

	it("reads queries and server labels off items that have no action", () => {
		expect(summarizeResponsesToolCall(item({ type: "file_search_call", queries: ["invoice"] }))).toBe("invoice");
		expect(summarizeResponsesToolCall(item({ type: "mcp_call", server_label: "deepwiki" }))).toBe("deepwiki");
		expect(summarizeResponsesToolCall(item({ type: "code_interpreter_call", container_id: "c_1" }))).toBe("c_1");
	});

	it("truncates long queries so the meta line stays on one row", () => {
		expect(summarizeResponsesToolCall(item({ type: "web_search_call", action: { type: "search", query: "a".repeat(100) } }))).toBe(
			`search · ${"a".repeat(71)}…`,
		);
	});

	it("reveals redacted values so the meta line agrees with the payload below it", () => {
		const msg = item({ type: "web_search_call", action: { type: "search", queries: ["contact [EMAIL-1]", "second query"] } });

		expect(summarizeResponsesToolCall(msg, { "EMAIL-1": "someone@example.com" })).toBe("search · contact someone@example.com +1 more");
		expect(summarizeResponsesToolCall(msg)).toBe("search · contact [EMAIL-1] +1 more");
	});

	it("reveals before truncating, so a placeholder spanning the cut is not left redacted", () => {
		// 73 chars redacted (past the 72 limit) but 71 revealed. Truncating first would split
		// "[EMAIL-1]" into a partial token that applyRedactionMapping can no longer match.
		const query = `look up ${"y".repeat(56)}[EMAIL-1]`;

		expect(summarizeResponsesToolCall(item({ type: "web_search_call", action: { type: "search", query } }), { "EMAIL-1": "a@b.com" })).toBe(
			`search · look up ${"y".repeat(56)}a@b.com`,
		);
	});

	it("returns nothing for items that carry no identifying detail", () => {
		expect(summarizeResponsesToolCall(item({ type: "function_call", name: "get_weather", arguments: "{}" }))).toBeUndefined();
		expect(summarizeResponsesToolCall(item({ type: "message", role: "assistant" }))).toBeUndefined();
	});
});