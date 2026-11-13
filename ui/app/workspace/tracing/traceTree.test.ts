import type { TraceSpan } from "@/lib/types/traces";
import { describe, expect, it } from "vitest";
import { buildThread, buildTraceTree, spanUsage } from "./traceTree";

function span(
	id: string,
	parent: string | undefined,
	startMs: number,
	endMs: number | undefined,
	extra: Partial<TraceSpan> = {},
): TraceSpan {
	const base = Date.UTC(2026, 10, 12, 10, 0, 0);
	return {
		project_id: "p1",
		trace_id: "t1",
		span_id: id,
		parent_span_id: parent,
		source: "otel",
		name: id,
		kind: "task",
		partial: false,
		start_time: new Date(base + startMs).toISOString(),
		end_time: endMs === undefined ? undefined : new Date(base + endMs).toISOString(),
		...extra,
	};
}

describe("buildTraceTree", () => {
	it("nests spans under their parents, siblings by start time", () => {
		const tree = buildTraceTree([
			span("b", "root", 500, 900),
			span("root", undefined, 0, 1000),
			span("a", "root", 100, 400),
			span("a1", "a", 150, 200),
		]);
		expect(tree.rows.map((node) => `${node.depth}:${node.span.span_id}`)).toEqual(["0:root", "1:a", "2:a1", "1:b"]);
		expect(tree.durationMs).toBe(1000);
		const a = tree.rows[1];
		expect(a.offset).toBeCloseTo(0.1);
		expect(a.width).toBeCloseTo(0.3);
	});

	it("hides HTTP client spans and lifts their children to the model call", () => {
		const spans = [
			span("llm", undefined, 0, 1000, { kind: "llm" }),
			span("http", "llm", 10, 990, { kind: "http" }),
			span("inner", "http", 20, 30),
		];
		expect(buildTraceTree(spans).rows.map((node) => `${node.depth}:${node.span.span_id}`)).toEqual(["0:llm", "1:inner"]);
		expect(buildTraceTree(spans, true).rows.map((node) => node.span.span_id)).toEqual(["llm", "http", "inner"]);
	});

	it("keeps a span whose parent has not arrived, at the top level", () => {
		const tree = buildTraceTree([span("child", "not-here-yet", 0, 100)]);
		expect(tree.rows).toHaveLength(1);
		expect(tree.rows[0].orphan).toBe(true);
	});

	it("does not hang on a cycle in parent ids", () => {
		const tree = buildTraceTree([span("a", "b", 0, 10), span("b", "a", 0, 10)]);
		expect(tree.rows.length).toBeLessThanOrEqual(2);
	});

	it("gives a span with no end a zero width instead of NaN", () => {
		const tree = buildTraceTree([span("root", undefined, 0, 1000), span("running", "root", 400, undefined)]);
		expect(tree.rows[1].width).toBe(0);
		expect(buildTraceTree([]).rows).toEqual([]);
	});
});

describe("buildThread", () => {
	it("reads the conversation once, though each call repeats the history", () => {
		const first = span("c1", undefined, 0, 100, {
			kind: "llm",
			input: JSON.stringify([
				{ role: "system", content: "be brief" },
				{ role: "user", content: "hi" },
			]),
			output: JSON.stringify([{ message: { role: "assistant", content: "hello" } }]),
		});
		const second = span("c2", undefined, 200, 300, {
			kind: "llm",
			input: JSON.stringify({
				messages: [
					{ role: "system", content: "be brief" },
					{ role: "user", content: "hi" },
					{ role: "assistant", content: "hello" },
					{ role: "user", content: [{ type: "text", text: "bye" }] },
				],
			}),
			output: "see you",
		});
		expect(buildThread([second, first]).map((m) => `${m.role}:${m.text}`)).toEqual([
			"system:be brief",
			"user:hi",
			"assistant:hello",
			"user:bye",
			"assistant:see you",
		]);
	});

	it("ignores spans that are not model calls and input that is not a message list", () => {
		expect(
			buildThread([span("task", undefined, 0, 10, { input: "plain" }), span("llm", undefined, 0, 10, { kind: "llm", input: "{broken" })]),
		).toEqual([]);
	});
});

describe("spanUsage", () => {
	it("counts a linked span with the gateway's figures, not the SDK's", () => {
		const usage = spanUsage(
			span("llm", undefined, 0, 10, {
				total_tokens: 100,
				cost: 9,
				link_snapshot: { total_tokens: 120, prompt_tokens: 100, cached_read_tokens: 25, cost: 0.004 },
			}),
		);
		expect(usage).toEqual({ tokens: 120, cost: 0.004, estimated: false, cachedShare: 0.25, linked: true });
	});

	it("marks a catalog-priced cost as estimated", () => {
		const usage = spanUsage(span("llm", undefined, 0, 10, { total_tokens: 50, cost: 0.01, cost_estimated: true }));
		expect(usage.estimated).toBe(true);
		expect(usage.linked).toBe(false);
	});
});