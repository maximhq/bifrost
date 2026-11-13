import type { Run, RunEntry, RunResult, RunScore, RunStatus } from "@/lib/types/evalRuns";
import { getApiBaseUrl } from "@/lib/utils/port";

// The most runs a comparison takes, as the API allows.
export const MAX_COMPARED_RUNS = 5;

// A run that still has work to do. The screens refresh while one is on screen.
export function isRunActive(status: RunStatus): boolean {
	return status === "queued" || status === "running";
}

// runProgress counts the entries that are finished, whatever way they ended.
export function runProgress(run: Pick<Run, "total" | "counts">): { done: number; total: number; percent: number } {
	const done = (run.counts.completed ?? 0) + (run.counts.failed ?? 0) + (run.counts.stopped ?? 0);
	return { done, total: run.total, percent: run.total > 0 ? Math.round((done / run.total) * 100) : 0 };
}

// runPassRate is the percent of completed entries that passed. Null until one completed.
export function runPassRate(run: Pick<Run, "passed" | "counts">): number | null {
	const completed = run.counts.completed ?? 0;
	return completed > 0 ? Math.round((run.passed / completed) * 100) : null;
}

// runDurationMs is how long a run has taken: up to its end, or up to now while it is active.
export function runDurationMs(run: Pick<Run, "started_at" | "ended_at">, now: number): number | null {
	if (!run.started_at) return null;
	const end = run.ended_at ? Date.parse(run.ended_at) : now;
	return Math.max(0, end - Date.parse(run.started_at));
}

export function formatDuration(ms: number | null): string {
	if (ms === null) return "";
	if (ms < 1000) return `${Math.round(ms)}ms`;
	if (ms < 60000) return `${(ms / 1000).toFixed(1)}s`;
	const seconds = Math.floor(ms / 1000);
	return `${Math.floor(seconds / 60)}m ${String(seconds % 60).padStart(2, "0")}s`;
}

// formatScore renders a score: numbers to two decimals at most, booleans and labels as they are.
export function formatScore(score: RunScore | undefined): string {
	if (!score) return "";
	if (score.number !== undefined) return String(Math.round(score.number * 100) / 100);
	if (score.bool !== undefined) return String(score.bool);
	return score.string ?? "";
}

export type ResultTone = "pass" | "fail" | "warn" | "none";

// resultTone is the colour a result's cell takes: green for pass, red for fail, yellow
// for one that waits for a reviewer or could not apply, and none otherwise.
export function resultTone(result: RunResult | undefined): ResultTone {
	if (!result) return "none";
	if (result.status === "failed") return "fail";
	if (result.status === "awaiting_review" || result.status === "not_applicable") return "warn";
	if (result.status !== "completed" || result.pass === undefined) return "none";
	return result.pass ? "pass" : "fail";
}

export const toneClass: Record<ResultTone, string> = {
	pass: "bg-emerald-50 text-emerald-800 dark:bg-emerald-950/40 dark:text-emerald-300",
	fail: "bg-red-50 text-red-800 dark:bg-red-950/40 dark:text-red-300",
	warn: "bg-amber-50 text-amber-900 dark:bg-amber-950/40 dark:text-amber-300",
	none: "",
};

export function resultOf(entry: RunEntry | null | undefined, evaluatorId: string): RunResult | undefined {
	return entry?.results.find((result) => result.evaluator_id === evaluatorId);
}

// entryScore is an entry's score for an evaluator as a number: the number itself, or
// 1 and 0 for yes and no. Null when there is none.
export function entryScore(entry: RunEntry | null | undefined, evaluatorId: string): number | null {
	const score = resultOf(entry, evaluatorId)?.score;
	if (!score || score.status !== "ok") return null;
	if (score.number !== undefined) return score.number;
	if (score.bool !== undefined) return score.bool ? 1 : 0;
	return null;
}

// scoreRange is the range a column's bars are drawn in. It starts at zero unless a
// score is negative, so a bar's length reads as the score itself.
export function scoreRange(values: (number | null)[]): { min: number; max: number } {
	const numbers = values.filter((value): value is number => value !== null);
	if (numbers.length === 0) return { min: 0, max: 1 };
	const max = Math.max(...numbers);
	return { min: Math.min(0, ...numbers), max: max > 0 ? max : 1 };
}

// barShare is how much of the column's width a score's bar takes, in percent.
export function barShare(value: number | null, range: { min: number; max: number }): number {
	if (value === null || range.max === range.min) return 0;
	return Math.min(100, Math.max(0, ((value - range.min) / (range.max - range.min)) * 100));
}

// toggleSelected adds an ID to a selection or takes it out. A full selection stays as it is.
export function toggleSelected(selected: string[], id: string, limit: number): string[] {
	if (selected.includes(id)) return selected.filter((item) => item !== id);
	return selected.length >= limit ? selected : [...selected, id];
}

export function runExportUrl(id: string): string {
	return `${getApiBaseUrl()}/evals/runs/${id}/export`;
}

// The colour of each run in a comparison: the baseline is neutral, the others take the chart palette.
export const RUN_SERIES_COLORS = ["var(--chart-neutral)", "var(--chart-info)", "#8b5cf6", "#f97316", "#14b8a6"];
// One run on the trend chart: its share of rows passed, and each evaluator's pass
// rate under the evaluator's ID. An evaluator the run did not score has no value.
export interface RunTrendPoint {
	id: string;
	name: string;
	rows: number | null;
	[evaluatorId: string]: string | number | null | undefined;
}

export interface RunTrendSeries {
	id: string;
	name: string;
	// The evaluator's pass rate, averaged over the runs that scored with it.
	average: number;
}

// runTrend lines runs up oldest first, to show how pass rates moved from run to run.
// Pass rate is used because it means the same for every evaluator; raw scores do not
// share a scale. Runs with nothing scored are left out, and only the evaluators used
// by the most runs get a series.
export function runTrend(runs: Run[], maxSeries: number): { points: RunTrendPoint[]; series: RunTrendSeries[] } {
	const ordered = [...runs].sort((a, b) => Date.parse(a.created_at) - Date.parse(b.created_at));
	const points: RunTrendPoint[] = [];
	const seen = new Map<string, { name: string; rates: number[]; first: number }>();
	for (const run of ordered) {
		const scored = (run.summary?.evaluators ?? []).filter((evaluator) => evaluator.scored > 0);
		const rows = runPassRate(run);
		if (scored.length === 0 && rows === null) continue;
		const point: RunTrendPoint = { id: run.id, name: run.name, rows };
		for (const evaluator of scored) {
			point[evaluator.evaluator_id] = evaluator.pass_rate;
			const entry = seen.get(evaluator.evaluator_id) ?? { name: evaluator.name, rates: [], first: seen.size };
			entry.rates.push(evaluator.pass_rate);
			seen.set(evaluator.evaluator_id, entry);
		}
		points.push(point);
	}
	const series = [...seen.entries()]
		.sort((a, b) => b[1].rates.length - a[1].rates.length || a[1].first - b[1].first)
		.slice(0, maxSeries)
		.map(([id, entry]) => ({ id, name: entry.name, average: entry.rates.reduce((sum, rate) => sum + rate, 0) / entry.rates.length }));
	return { points, series };
}

// scoreDelta is how far an entry's score for an evaluator moved from the baseline
// entry's score: positive when it scored higher. Null when either has no numeric score.
export function scoreDelta(entry: RunEntry | null | undefined, baseline: RunEntry | null | undefined, evaluatorId: string): number | null {
	const now = entryScore(entry, evaluatorId);
	const before = entryScore(baseline, evaluatorId);
	return now === null || before === null ? null : now - before;
}

// signed writes a change with its sign, rounded to two places.
export function signed(delta: number): string {
	const rounded = Math.round(delta * 100) / 100;
	return rounded > 0 ? `+${rounded}` : String(rounded);
}