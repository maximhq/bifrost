import type { LogEntry } from "@/lib/types/logs";
import { describe, expect, it } from "vitest";
import { buildFallbackChain, fallbackChainOutcome, fallbackChainRootId } from "./logDetailView.utils";

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

	it("ignores rows that share a session id as parent_request_id", () => {
		const turn1 = entry("t1", 0, "success", "conv");
		const turn2 = entry("t2", 0, "success", "conv");
		const turn3 = entry("t3", 0, "success", "conv");
		expect(buildFallbackChain(turn2, undefined, [turn1, turn2, turn3]).map((e) => e.id)).toEqual(["t2"]);
	});

	it("ignores linked children that are not fallback attempts", () => {
		const session = entry("s", 0, "success");
		const unit = entry("u", 0, "success", "s");
		expect(buildFallbackChain(session, undefined, [unit]).map((e) => e.id)).toEqual(["s"]);
		expect(buildFallbackChain(unit, session, [unit]).map((e) => e.id)).toEqual(["u"]);
	});

	it("keeps the attempts when the root row has not loaded", () => {
		expect(buildFallbackChain(b, undefined, [b, c]).map((e) => e.id)).toEqual(["b", "c"]);
	});

	it("keeps the fallbacks of a primary request that carries a session id", () => {
		const primary = entry("p", 0, "error", "conv");
		const fallback = entry("f", 1, "success", "p");
		expect(buildFallbackChain(primary, undefined, [fallback]).map((e) => e.id)).toEqual(["p", "f"]);
		expect(buildFallbackChain(fallback, primary, [fallback]).map((e) => e.id)).toEqual(["p", "f"]);
	});
});

describe("fallbackChainRootId", () => {
	it("roots index-zero rows at themselves and fallbacks at their parent", () => {
		expect(fallbackChainRootId(entry("p", 0, "error", "conv"))).toBe("p");
		expect(fallbackChainRootId(entry("f", 1, "success", "p"))).toBe("p");
		expect(fallbackChainRootId(entry("a", 0, "success"))).toBe("a");
	});
});

describe("fallbackChainOutcome", () => {
	it("is decided by the last attempt", () => {
		expect(fallbackChainOutcome([entry("a", 0, "error"), entry("b", 1, "success", "a")])).toBe("succeeded");
		expect(fallbackChainOutcome([entry("a", 0, "error"), entry("b", 1, "error", "a")])).toBe("failed");
		expect(fallbackChainOutcome([entry("a", 0, "error"), entry("b", 1, "processing", "a")])).toBe("in_progress");
	});
});