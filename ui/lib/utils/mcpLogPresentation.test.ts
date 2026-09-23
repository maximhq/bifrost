import { describe, expect, test } from "vitest";

import type { MCPToolLogEntry } from "@/lib/types/logs";
import { getMCPLogPillTone, getMCPLogPresentation, getMCPLogTimeline } from "./mcpLogPresentation";

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