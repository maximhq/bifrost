import type { LogEntry } from "@/lib/types/logs";
import { describe, expect, it } from "vitest";
import { buildFallbackChain, fallbackChainOutcome } from "./logDetailView.utils";

const entry = (id: string, fallback_index: number, status: string, parent?: string): LogEntry =>
	({ id, fallback_index, status, parent_request_id: parent, timestamp: `2026-01-01T00:00:0${fallback_index}Z` }) as LogEntry;

describe("buildFallbackChain", () => {
	const root = entry("a", 0, "error");
	const b = entry("b", 1, "error", "a");
	const c = entry("c", 2, "success", "a");

	it("builds the same chain from the root or from any child", () => {
		expect(buildFallbackChain(root, undefined, [c, b]).map((e) => e.id)).toEqual(["a", "b", "c"]);
		expect(buildFallbackChain(b, root, [b, c]).map((e) => e.id)).toEqual(["a", "b", "c"]);
	});

	it("is a single row when there are no fallbacks", () => {
		expect(buildFallbackChain(root, undefined, [])).toHaveLength(1);
	});
});

describe("fallbackChainOutcome", () => {
	it("is decided by the last attempt", () => {
		expect(fallbackChainOutcome([entry("a", 0, "error"), entry("b", 1, "success", "a")])).toBe("succeeded");
		expect(fallbackChainOutcome([entry("a", 0, "error"), entry("b", 1, "error", "a")])).toBe("failed");
		expect(fallbackChainOutcome([entry("a", 0, "error"), entry("b", 1, "processing", "a")])).toBe("in_progress");
	});
});
