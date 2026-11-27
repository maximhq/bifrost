import type { AgentLogSummary, DisplayAgentLogSummary } from "@/lib/types/agentLogs";
import { createTable, getCoreRowModel } from "@tanstack/react-table";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import { createAgentColumns } from "./columns";

const root: AgentLogSummary = {
	id: "root",
	timestamp: "2026-10-12T00:00:00Z",
	record_kind: "request",
	operation: "SendMessage",
	status: "success",
	agent_name: "test-agent",
	request_id: "request-1",
	context_id: "context-1",
	session_child_count: 2,
};

function renderCell(sessions?: Parameters<typeof createAgentColumns>[1], log: DisplayAgentLogSummary = root, columnId = "expand") {
	const table = createTable({
		data: [log],
		columns: createAgentColumns(undefined, sessions),
		getCoreRowModel: getCoreRowModel(),
		state: {},
		onStateChange: () => {},
		renderFallbackValue: null,
	});
	const cell = table
		.getRowModel()
		.rows[0].getAllCells()
		.find((cell) => cell.column.id === columnId);
	if (!cell) return "";
	if (typeof cell.column.columnDef.cell !== "function") throw new Error("Cell missing");
	return renderToStaticMarkup(createElement(cell.column.columnDef.cell, cell.getContext()));
}

describe("agent context grouping cells", () => {
	it("keeps flat rows free of expansion controls", () => {
		expect(renderCell()).not.toContain("agent-logs-session-expand-btn");
	});
	it("shows the context operation count and expansion state", () => {
		const sessions = { expandedIds: new Set([root.id]), loadingIds: new Set<string>(), onToggle: vi.fn() };
		const html = renderCell(sessions);
		expect(html).toContain("3 ops");
		expect(html).toContain('aria-expanded="true"');
		expect(html).toContain("agent-logs-session-expand-btn");
		expect(renderCell(sessions, root, "context")).not.toContain("agent-logs-session-expand-btn");
		expect(createAgentColumns(undefined, sessions)[0].id).toBe("expand");
	});
	it("indents members and leaves single-operation contexts unexpandable", () => {
		const member = { ...root, id: "member", session_child_count: 0, __sessionTurn: 2, __isLast: true };
		const html = renderCell({ expandedIds: new Set<string>(), loadingIds: new Set<string>(), onToggle: vi.fn() }, member);
		expect(html).toContain("operation 2");
		expect(html).toContain("h-1/2");
		expect(html).not.toContain("agent-logs-session-expand-btn");
	});
});