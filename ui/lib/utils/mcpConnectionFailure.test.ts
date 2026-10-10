import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import type { MCPInstanceState } from "@/lib/types/mcp";
import { foldInstanceStates, formatDurationSince, formatFailureTiming, hasStateReason, stateReasonTitle } from "./mcpConnectionFailure";

describe("mcpConnectionFailure", () => {
	const now = new Date("2026-09-03T12:00:00Z");

	beforeEach(() => {
		vi.useFakeTimers();
		vi.setSystemTime(now);
	});

	afterEach(() => {
		vi.useRealTimers();
	});

	test("formatDurationSince keeps seconds under a minute", () => {
		expect(formatDurationSince("2026-09-03T11:59:53Z")).toBe("7s");
		expect(formatDurationSince("2026-09-03T11:56:00Z")).toBe("4m");
		expect(formatDurationSince("2026-09-03T09:00:00Z")).toBe("3h");
		expect(formatDurationSince("2026-08-30T12:00:00Z")).toBe("4d");
		expect(formatDurationSince("not a date")).toBe("not a date");
	});

	test("formatFailureTiming omits the run when it is a single attempt", () => {
		const at = "2026-09-03T11:59:48Z";
		expect(formatFailureTiming({ stage: "connect", message: "x", at, since: at }, "unstable")).toBe("Last failed 12s ago");
		expect(formatFailureTiming({ stage: "connect", message: "x", at, since: "2026-09-03T11:56:00Z" }, "unstable")).toBe(
			"Last failed 12s ago · unstable for 4m",
		);
		expect(formatFailureTiming({ stage: "credential", message: "x", at, since: "2026-09-03T10:00:00Z" }, "needs_reauth")).toBe(
			"Last failed 12s ago · needs reauth for 2h",
		);
	});

	test("foldInstanceStates groups identical failures, newest at and oldest since", () => {
		const nodes: Record<string, MCPInstanceState> = {
			a: {
				state: "unstable",
				last_failure: {
					stage: "list_tools",
					message: "context deadline exceeded",
					at: "2026-09-03T11:59:48Z",
					since: "2026-09-03T11:56:00Z",
				},
			},
			b: {
				state: "unstable",
				last_failure: {
					stage: "list_tools",
					message: "context deadline exceeded",
					at: "2026-09-03T11:59:53Z",
					since: "2026-09-03T11:55:30Z",
				},
			},
			c: {
				state: "unstable",
				last_failure: { stage: "ping", message: "i/o timeout", at: "2026-09-03T11:59:51Z", since: "2026-09-03T11:59:51Z" },
			},
			d: { state: "healthy" },
		};
		const groups = foldInstanceStates(nodes);
		expect(groups.map((g) => [g.state, g.count, g.last_failure?.message])).toEqual([
			["unstable", 2, "context deadline exceeded"],
			["unstable", 1, "i/o timeout"],
			["healthy", 1, undefined],
		]);
		expect(groups[0].last_failure?.at).toBe("2026-09-03T11:59:53Z");
		expect(groups[0].last_failure?.since).toBe("2026-09-03T11:55:30Z");
		// The fold never mutates the input records.
		expect(nodes.a.last_failure?.at).toBe("2026-09-03T11:59:48Z");
	});

	test("foldInstanceStates puts unhealthy groups first", () => {
		const groups = foldInstanceStates({
			a: { state: "healthy" },
			b: {
				state: "needs_reauth",
				last_failure: { stage: "credential", message: "invalid_grant", at: "2026-09-03T10:00:00Z", since: "2026-09-03T10:00:00Z" },
			},
		});
		expect(groups.map((g) => g.state)).toEqual(["needs_reauth", "healthy"]);
	});

	test("hasStateReason and stateReasonTitle", () => {
		expect(hasStateReason({})).toBe(false);
		expect(hasStateReason({ node_states: {} })).toBe(false);
		expect(hasStateReason({ last_failure: { stage: "ping", message: "x", at: "", since: "" } })).toBe(true);
		expect(stateReasonTitle("unstable", false)).toBe("Last connection check failed");
		expect(stateReasonTitle("unstable", true)).toBe("Last connection check failed on every instance");
		expect(stateReasonTitle("degraded", true)).toBe("Instances disagree about this server's state");
		expect(stateReasonTitle("needs_reauth", false)).toBe("Reauthorization required");
	});
});