import { describe, expect, test } from "vitest";

import type { BifrostError, MCPToolLogEntry } from "@/lib/types/logs";
import {
	getMCPLogError,
	getMCPLogPillTone,
	getMCPLogPresentation,
	getMCPLogTimeline,
	getMCPReadKind,
	splitMCPErrorMessage,
} from "./mcpLogPresentation";

const TIMESTAMP = "2026-09-14T10:00:00.000Z";

function entry(overrides: Partial<MCPToolLogEntry> = {}): MCPToolLogEntry {
	return {
		id: "log-1",
		request_id: "req-1",
		tool_name: "read_file",
		status: "processing",
		timestamp: TIMESTAMP,
		...overrides,
	} as MCPToolLogEntry;
}

// A pre-execution entry whose policy check has not decided yet.
function pendingPolicyEntry(overrides: Partial<MCPToolLogEntry> = {}): MCPToolLogEntry {
	return entry({ metadata: { inspection_phase: "pre_execution" }, ...overrides });
}

describe("getMCPLogPillTone", () => {
	test("an allowed policy check reads as approved", () => {
		const log = pendingPolicyEntry({ decision: "allow" });
		expect(getMCPLogPillTone(log, getMCPLogPresentation(log))).toBe("approved");
	});

	test("a denied policy check reads as an error", () => {
		const log = pendingPolicyEntry({ decision: "deny", status: "success" });
		expect(getMCPLogPillTone(log, getMCPLogPresentation(log))).toBe("error");
	});

	test("a policy check with no decision keeps its status tone", () => {
		for (const [status, tone] of [
			["processing", "processing"],
			["success", "success"],
			["unknown", "neutral"],
		] as const) {
			const log = pendingPolicyEntry({ status });
			expect(getMCPLogPresentation(log).label).not.toBe("Policy blocked");
			expect(getMCPLogPillTone(log, getMCPLogPresentation(log))).toBe(tone);
		}
	});

	test("a failed policy check still reads as an error", () => {
		const log = pendingPolicyEntry({ status: "error" });
		expect(getMCPLogPillTone(log, getMCPLogPresentation(log))).toBe("error");
	});

	test("non-policy entries follow their execution status", () => {
		expect(getMCPLogPillTone(entry({ status: "success" }), getMCPLogPresentation(entry({ status: "success" })))).toBe("success");
		expect(getMCPLogPillTone(entry({ status: "cancelled" }), getMCPLogPresentation(entry({ status: "cancelled" })))).toBe("neutral");
	});

	test("a round that asked the caller for input has its own label, description and tone", () => {
		const log = entry({ status: "input_required" });
		const presentation = getMCPLogPresentation(log);
		expect(presentation.label).toBe("Needs input");
		expect(presentation.description).toContain("asked the caller for input");
		expect(getMCPLogPillTone(log, presentation)).toBe("input");
	});

	test("an ordinary execution has no description, so nothing generic is shown for it", () => {
		for (const status of ["success", "error", "cancelled", "processing"] as const) {
			expect(getMCPLogPresentation(entry({ status })).description).toBeUndefined();
		}
		expect(getMCPLogPresentation(pendingPolicyEntry({ decision: "deny" })).description).toBe("Blocked before execution.");
		expect(getMCPLogPresentation(entry({ status: "unknown" })).description).toContain("outcome is not known");
	});

	test("a retry-later round says it asked for no input", () => {
		const log = entry({ status: "input_required", metadata: { retry_later: true } });
		const presentation = getMCPLogPresentation(log);
		expect(presentation.label).toBe("Retry later");
		expect(presentation.description).toContain("without asking for input");
		expect(getMCPLogPillTone(log, presentation)).toBe("input");
	});
});

describe("getMCPLogError", () => {
	const failure = (error: BifrostError["error"]): BifrostError => ({
		is_bifrost_error: false,
		error,
		extra_fields: { raw_response: "ignored" },
	});

	test("an inner error that repeats the message adds no details", () => {
		expect(getMCPLogError(failure({ message: "Authentication required", error: "Authentication required" }))).toEqual({
			message: "Authentication required",
		});
	});

	test("an inner error that says something else goes under details", () => {
		expect(getMCPLogError(failure({ message: "Tool call failed", error: "connection reset by peer" }))).toEqual({
			message: "Tool call failed",
			details: "connection reset by peer",
		});
	});

	test("a structured inner error is pretty-printed under details", () => {
		expect(getMCPLogError(failure({ message: "MCP server returned 502", error: { code: -32603 } }))).toEqual({
			message: "MCP server returned 502",
			details: JSON.stringify({ code: -32603 }, null, 2),
		});
	});

	test("a string inner error stands in for a missing message", () => {
		expect(getMCPLogError(failure({ message: "", error: "timeout after 30s" }))).toEqual({ message: "timeout after 30s" });
	});

	test("an error with neither leaves the message to the caller's fallback", () => {
		expect(getMCPLogError(failure({ message: "" }))).toEqual({});
	});
});

describe("splitMCPErrorMessage", () => {
	const zodIssues =
		'[\n  {\n    "expected": "record",\n    "code": "invalid_type",\n    "path": [\n      "params",\n      "arguments"\n    ],\n    "message": "Invalid input: expected record, received null"\n  }\n]';

	test("drops Bifrost's wrapper and pulls the upstream JSON out of the message", () => {
		const message = `MCP tool call failed for Notion-notion-get-users: invalid params: Invalid tools/call request: ${zodIssues}: mcp tool call failed`;
		expect(splitMCPErrorMessage(message)).toEqual({
			text: "invalid params: Invalid tools/call request:",
			json: JSON.stringify(JSON.parse(zodIssues), null, 2),
		});
	});

	test("keeps the text that follows the JSON", () => {
		expect(splitMCPErrorMessage('upstream said {"code": 7, "reason": "busy"} and gave up')).toEqual({
			text: "upstream said",
			json: JSON.stringify({ code: 7, reason: "busy" }, null, 2),
			textAfter: "and gave up",
		});
	});

	test("a timeout keeps its own words and loses only the trailing sentinel", () => {
		expect(splitMCPErrorMessage("MCP tool call timed out after 30s: Notion-search: mcp tool call timed out")).toEqual({
			text: "MCP tool call timed out after 30s: Notion-search",
		});
	});

	test("brackets that are not JSON, and JSON with nothing in it, stay as text", () => {
		for (const message of ["[WARN] server busy", "expected one of [1, 2]", "empty {} body", "unbalanced { here"]) {
			expect(splitMCPErrorMessage(message)).toEqual({ text: message });
		}
	});

	test("a message that is only JSON leaves no text", () => {
		expect(splitMCPErrorMessage('{"error": "denied"}')).toEqual({ text: "", json: JSON.stringify({ error: "denied" }, null, 2) });
	});

	// The message comes from the upstream MCP server, so its brackets are untrusted: the scan has to stay
	// linear in the message length. The budget is ~100x what the scan needs, so it only trips on a regression.
	test("brackets that never close are scanned once, not once per bracket", () => {
		const message = `${"[".repeat(99_000)} {"code": 1}`;
		const started = performance.now();
		const result = splitMCPErrorMessage(message);
		expect(performance.now() - started).toBeLessThan(500);
		expect(result.json).toBe(JSON.stringify({ code: 1 }, null, 2));
	});

	test("deeply nested brackets leave the message as text instead of overflowing the stack", () => {
		const message = `${"[".repeat(30_000)}${"]".repeat(30_000)}`;
		expect(() => splitMCPErrorMessage(message)).not.toThrow();
		expect(splitMCPErrorMessage(message).json).toBeUndefined();
	});
});

describe("getMCPLogTimeline", () => {
	test("a policy entry with no latency still gets both boundaries from the inspection time", () => {
		const log = pendingPolicyEntry({ decision: "allow", metadata: { inspection_phase: "pre_execution", inspection_duration_ms: "40" } });
		const { durationMs, startTimestamp, endTimestamp } = getMCPLogTimeline(log, getMCPLogPresentation(log));
		expect(durationMs).toBe(40);
		expect(startTimestamp?.toISOString()).toBe(TIMESTAMP);
		expect(endTimestamp?.toISOString()).toBe("2026-09-14T10:00:00.040Z");
	});

	test("a whitespace-only inspection duration leaves the policy entry without boundaries", () => {
		const log = pendingPolicyEntry({ decision: "allow", metadata: { inspection_phase: "pre_execution", inspection_duration_ms: "   " } });
		const presentation = getMCPLogPresentation(log);
		expect(presentation.inspectionDuration).toBeUndefined();
		expect(getMCPLogTimeline(log, presentation).durationMs).toBeUndefined();
	});

	test("a non-native entry ends one duration after its timestamp", () => {
		const log = entry({ latency: 250, status: "success" });
		const { durationMs, startTimestamp, endTimestamp } = getMCPLogTimeline(log, getMCPLogPresentation(log));
		expect(durationMs).toBe(250);
		expect(startTimestamp?.toISOString()).toBe(TIMESTAMP);
		expect(endTimestamp?.toISOString()).toBe("2026-09-14T10:00:00.250Z");
	});

	test("a native entry starts one duration before its timestamp", () => {
		const log = entry({ source: "native", latency: 250, status: "success" });
		const { startTimestamp, endTimestamp } = getMCPLogTimeline(log, getMCPLogPresentation(log));
		expect(startTimestamp?.toISOString()).toBe("2026-09-14T09:59:59.750Z");
		expect(endTimestamp?.toISOString()).toBe(TIMESTAMP);
	});

	test("an entry with no duration at all has no derived boundary", () => {
		const log = entry({ source: "native", status: "unknown" });
		const { durationMs, startTimestamp, endTimestamp } = getMCPLogTimeline(log, getMCPLogPresentation(log));
		expect(durationMs).toBeUndefined();
		expect(startTimestamp).toBeNull();
		expect(endTimestamp?.toISOString()).toBe(TIMESTAMP);
	});
});

describe("observed tool timing", () => {
	test("observed duration stays separate from execution and does not invent a start timestamp", () => {
		const log = entry({ source: "native", metadata: { observed_latency_ms: "12.5" } });
		const presentation = getMCPLogPresentation(log);
		expect(presentation.durationLabel).toBe("Observed round trip");
		expect(getMCPLogTimeline(log, presentation).durationMs).toBe(12.5);
		expect(getMCPLogTimeline(log, presentation).startTimestamp).toBeNull();
	});
	test("execution wins when both measurements are present", () => {
		const log = entry({ source: "native", latency: 0, metadata: { observed_latency_ms: "12" } });
		const presentation = getMCPLogPresentation(log);
		expect(presentation.durationLabel).toBe("Execution time");
		expect(getMCPLogTimeline(log, presentation).durationMs).toBe(0);
	});
	test("invalid and policy observed durations are ignored", () => {
		for (const value of ["", "   ", "NaN", "-1", "86400001"]) {
			expect(getMCPLogPresentation(entry({ source: "native", metadata: { observed_latency_ms: value } })).observedDuration).toBeUndefined();
		}
		expect(
			getMCPLogPresentation(
				pendingPolicyEntry({ source: "native", metadata: { inspection_phase: "pre_execution", observed_latency_ms: "20" } }),
			).observedDuration,
		).toBeUndefined();
	});
});

describe("read kind", () => {
	test("prompt and resource reads are named; tool calls are not", () => {
		expect(getMCPReadKind({ method: "prompts/get" })).toBe("Prompt");
		expect(getMCPReadKind({ method: "resources/read" })).toBe("Resource");
		expect(getMCPReadKind({})).toBeUndefined();
	});
});