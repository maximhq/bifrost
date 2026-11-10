import type { Run, RunEntry, RunResult } from "@/lib/types/evalRuns";
import { describe, expect, it } from "vitest";
import {
	barShare,
	entryScore,
	formatDuration,
	formatScore,
	isRunActive,
	resultTone,
	runDurationMs,
	runPassRate,
	runProgress,
	runTrend,
	scoreDelta,
	scoreRange,
	signed,
	toggleSelected,
} from "./runUtils";

const run = (over: Partial<Run>): Run =>
	({
		id: "r1",
		name: "run",
		status: "completed",
		total: 10,
		counts: {},
		passed: 0,
		evaluators: [],
		created_at: "2026-01-01T00:00:00Z",
		...over,
	}) as Run;
const result = (over: Partial<RunResult>): RunResult =>
	({ id: "x", entry_id: "e", evaluator_id: "ev", status: "completed", latency_ms: 0, total_tokens: 0, ...over }) as RunResult;

describe("run progress and pass rate", () => {
	it("counts entries that are finished, whatever way they ended", () => {
		expect(runProgress(run({ total: 10, counts: { completed: 4, failed: 1, stopped: 1, running: 2, queued: 2 } }))).toEqual({
			done: 6,
			total: 10,
			percent: 60,
		});
		expect(runProgress(run({ total: 0, counts: {} }))).toEqual({ done: 0, total: 0, percent: 0 });
	});

	it("gives the share of completed entries that passed, and nothing before any completed", () => {
		expect(runPassRate(run({ passed: 3, counts: { completed: 4 } }))).toBe(75);
		expect(runPassRate(run({ passed: 0, counts: { failed: 2 } }))).toBeNull();
	});

	it("knows which runs still have work to do", () => {
		expect(isRunActive("queued")).toBe(true);
		expect(isRunActive("running")).toBe(true);
		expect(isRunActive("stopped")).toBe(false);
	});
});

describe("durations", () => {
	it("measures an active run up to now and a finished run up to its end", () => {
		const now = Date.parse("2026-01-01T00:02:05Z");
		expect(runDurationMs(run({ started_at: "2026-01-01T00:00:00Z" }), now)).toBe(125000);
		expect(runDurationMs(run({ started_at: "2026-01-01T00:00:00Z", ended_at: "2026-01-01T00:00:03Z" }), now)).toBe(3000);
		expect(runDurationMs(run({}), now)).toBeNull();
	});

	it("formats milliseconds, seconds and minutes", () => {
		expect(formatDuration(850)).toBe("850ms");
		expect(formatDuration(1400)).toBe("1.4s");
		expect(formatDuration(125000)).toBe("2m 05s");
		expect(formatDuration(null)).toBe("");
	});
});

describe("scores", () => {
	it("formats numbers, booleans and labels", () => {
		expect(formatScore({ status: "ok", number: 4 })).toBe("4");
		expect(formatScore({ status: "ok", number: 0.9333 })).toBe("0.93");
		expect(formatScore({ status: "ok", bool: true })).toBe("true");
		expect(formatScore({ status: "ok", string: "polite" })).toBe("polite");
		expect(formatScore(undefined)).toBe("");
	});

	it("tones a result by its state", () => {
		expect(resultTone(result({ pass: true }))).toBe("pass");
		expect(resultTone(result({ pass: false }))).toBe("fail");
		expect(resultTone(result({ status: "failed" }))).toBe("fail");
		expect(resultTone(result({ status: "awaiting_review" }))).toBe("warn");
		expect(resultTone(result({ status: "not_applicable" }))).toBe("warn");
		expect(resultTone(result({ status: "queued" }))).toBe("none");
		expect(resultTone(result({}))).toBe("none");
		expect(resultTone(undefined)).toBe("none");
	});

	it("reads an entry's numeric score for an evaluator, with yes and no as 1 and 0", () => {
		const entry = {
			results: [
				result({ evaluator_id: "a", score: { status: "ok", number: 3 } }),
				result({ evaluator_id: "b", score: { status: "ok", bool: false } }),
			],
		} as RunEntry;
		expect(entryScore(entry, "a")).toBe(3);
		expect(entryScore(entry, "b")).toBe(0);
		expect(entryScore(entry, "c")).toBeNull();
		expect(entryScore(null, "a")).toBeNull();
	});

	it("sizes a bar within the range of the scores on screen", () => {
		expect(scoreRange([1, 5, null, 3])).toEqual({ min: 0, max: 5 });
		expect(scoreRange([null])).toEqual({ min: 0, max: 1 });
		expect(scoreRange([-2, 4])).toEqual({ min: -2, max: 4 });
		expect(barShare(2.5, { min: 0, max: 5 })).toBe(50);
		expect(barShare(9, { min: 0, max: 5 })).toBe(100);
		expect(barShare(null, { min: 0, max: 5 })).toBe(0);
	});
});

describe("toggleSelected", () => {
	it("adds and removes, and never goes past the limit", () => {
		expect(toggleSelected(["a"], "b", 5)).toEqual(["a", "b"]);
		expect(toggleSelected(["a", "b"], "a", 5)).toEqual(["b"]);
		expect(toggleSelected(["a", "b"], "c", 2)).toEqual(["a", "b"]);
	});
});

describe("runTrend", () => {
	const scored = (id: string, name: string, pass_rate: number, total = 4) =>
		({ evaluator_id: id, name, pass_rate, scored: total }) as never;
	const finished = (id: string, created_at: string, passed: number, evaluators: never[]) =>
		run({ id, name: id, created_at, passed, counts: { completed: 4 }, summary: { evaluators, totals: {} as never } });

	it("orders runs oldest first and gives each evaluator a series", () => {
		const trend = runTrend(
			[
				finished("new", "2026-01-03T00:00:00Z", 4, [scored("a", "Clarity", 100), scored("b", "Tone", 50)]),
				finished("old", "2026-01-01T00:00:00Z", 2, [scored("a", "Clarity", 25)]),
			],
			5,
		);
		expect(trend.points.map((point) => point.name)).toEqual(["old", "new"]);
		expect(trend.series).toEqual([
			{ id: "a", name: "Clarity", average: 62.5 },
			{ id: "b", name: "Tone", average: 50 },
		]);
		expect(trend.points[0]).toMatchObject({ rows: 50, a: 25 });
		expect(trend.points[0].b).toBeUndefined();
		expect(trend.points[1]).toMatchObject({ rows: 100, a: 100, b: 50 });
	});

	it("leaves out runs with no scores and keeps the most used evaluators", () => {
		const trend = runTrend(
			[
				run({ id: "active", status: "running" }),
				finished("one", "2026-01-01T00:00:00Z", 1, [scored("a", "A", 10), scored("b", "B", 20)]),
				finished("two", "2026-01-02T00:00:00Z", 1, [scored("b", "B", 40), scored("c", "C", 0, 0)]),
			],
			1,
		);
		expect(trend.points).toHaveLength(2);
		expect(trend.series).toEqual([{ id: "b", name: "B", average: 30 }]);
	});
});

describe("scoreDelta", () => {
	const scored = (evaluator: string, score: RunResult["score"]): RunEntry =>
		({ results: [{ evaluator_id: evaluator, status: "completed", score }] }) as unknown as RunEntry;

	it("is how far an entry's score moved from the baseline's", () => {
		expect(scoreDelta(scored("e", { status: "ok", number: 4 }), scored("e", { status: "ok", number: 2.5 }), "e")).toBe(1.5);
		expect(scoreDelta(scored("e", { status: "ok", bool: false }), scored("e", { status: "ok", bool: true }), "e")).toBe(-1);
		expect(scoreDelta(scored("e", { status: "ok", bool: true }), scored("e", { status: "ok", bool: true }), "e")).toBe(0);
	});

	it("is null when either side has no numeric score", () => {
		expect(scoreDelta(scored("e", { status: "ok", number: 4 }), undefined, "e")).toBeNull();
		expect(scoreDelta(scored("e", { status: "ok", string: "good" }), scored("e", { status: "ok", number: 1 }), "e")).toBeNull();
		expect(scoreDelta(scored("e", { status: "ok", number: 4 }), scored("other", { status: "ok", number: 1 }), "e")).toBeNull();
	});
});

describe("signed", () => {
	it("writes a change with its sign, to two places", () => {
		expect(signed(1.5)).toBe("+1.5");
		expect(signed(-0.333)).toBe("-0.33");
		expect(signed(0)).toBe("0");
	});
});