import { describe, expect, test } from "vitest";
import type { EditableKeywordConfig, Suggestion, SuggestionRun } from "@/lib/types/complexityRouter";
import {
	addPhrases,
	availableWindows,
	canStartRun,
	categoryCounts,
	stagedAdditions,
	filterSuggestions,
	newPhrases,
	proposedTier,
	removePhrases,
	replacePhrase,
	routingSplit,
	runBreakdown,
	runInProgress,
	runProgress,
	runWindowDays,
	stagedPhraseCount,
	stillStaged,
} from "./suggestions.utils";

const bank: EditableKeywordConfig = {
	simple_keywords: ["Where is my order"],
	medium_keywords: ["summarize this thread"],
	complex_keywords: ["design a schema"],
};

function suggestion(id: string, category: Suggestion["category"], phrases: string[], reason = ""): Suggestion {
	return {
		id,
		router_id: "default",
		router_version: "v1",
		stale: false,
		status: "open",
		tier: "SIMPLE",
		phrases,
		category,
		reason,
		request_count: 10,
		coverage: 1,
		nearest_similarity: 0.4,
		example_log_ids: [],
		current_routing: {},
		embedding_signature: "ns",
		run_id: "r",
		last_seen_at: "",
		created_at: "",
		updated_at: "",
	};
}

describe("staging suggestions", () => {
	test("adds only phrases the bank does not hold, in any tier or case", () => {
		expect(newPhrases(bank, ["where is my ORDER ", "reset my password", "Reset my password", "  "])).toEqual(["reset my password"]);
	});

	test("adds, edits and takes back staged phrases in their tier", () => {
		const added = addPhrases(bank, "SIMPLE", ["reset my password"]);
		expect(added.simple_keywords).toEqual(["Where is my order", "reset my password"]);
		expect(added.medium_keywords).toBe(bank.medium_keywords);
		const edited = replacePhrase(added, "SIMPLE", "reset my password", "reset my account password");
		expect(edited.simple_keywords).toEqual(["Where is my order", "reset my account password"]);
		expect(removePhrases(edited, "SIMPLE", ["reset my account password"]).simple_keywords).toEqual(["Where is my order"]);
	});

	test("forgets a staged suggestion whose phrases were removed from the list", () => {
		const staged = [
			{ id: "a", tier: "SIMPLE" as const, phrases: ["reset my password", "change my email"] },
			{ id: "b", tier: "COMPLEX" as const, phrases: ["plan a migration"] },
		];
		const keywords = addPhrases(bank, "SIMPLE", ["reset my password"]);
		expect(stillStaged(staged, keywords)).toEqual([{ id: "a", tier: "SIMPLE", phrases: ["reset my password"] }]);
		expect(stagedPhraseCount(staged)).toBe(3);
	});
});

describe("filtering suggestions", () => {
	const list = [
		suggestion("a", "weak", ["where is my refund"], "refund questions"),
		suggestion("b", "uncovered", ["plan a database migration"]),
		suggestion("c", "weak", ["reset my password"]),
		suggestion("d", "template", ["write an image prompt for a scene"], "one image-prompt scaffold"),
	];

	test("searches phrases and reasons, filters by category, and leaves out staged ones", () => {
		expect(filterSuggestions(list, { query: "REFUND", category: "all" }, new Set()).map((s) => s.id)).toEqual(["a"]);
		expect(filterSuggestions(list, { query: "", category: "weak" }, new Set(["c"])).map((s) => s.id)).toEqual(["a"]);
		expect(filterSuggestions(list, { query: "", category: "template" }, new Set()).map((s) => s.id)).toEqual(["d"]);
		expect(categoryCounts(list, new Set(["c"]))).toEqual({ all: 3, uncovered: 1, weak: 1, misrouted: 0, mixed: 0, template: 1 });
	});
});

describe("run windows", () => {
	test("offers only windows the lookback still holds", () => {
		expect(availableWindows(7)).toEqual(["24h", "7d"]);
		expect(availableWindows(30)).toEqual(["24h", "7d", "14d", "30d"]);
		expect(availableWindows(1)).toEqual(["24h"]);
	});

	test("starts a run on requests in the window, or on a backfill that could not be counted", () => {
		const estimate = { requests: 0, clustered: 0, assigned: 0, skipped: 0, max_model_calls: 0 };
		expect(canStartRun(estimate)).toBe(false);
		expect(canStartRun({ ...estimate, requests: 3 })).toBe(true);
		expect(canStartRun({ ...estimate, backfill_unknown: true })).toBe(true);
	});
});
function run(overrides: Partial<SuggestionRun>): SuggestionRun {
	return {
		id: "r",
		router_id: "default",
		router_version: "v1",
		window_start: "2026-11-03T03:00:00Z",
		triggered_by: "manual",
		status: "succeeded",
		requests_read: 0,
		requests_clustered: 0,
		requests_assigned: 0,
		requests_skipped: 0,
		requests_grouped: 0,
		clusters: 0,
		clusters_decided: 0,
		clusters_over_limit: 0,
		clusters_asked: 0,
		clusters_done: 0,
		clusters_failed: 0,
		clusters_covered: 0,
		clusters_incoherent: 0,
		clusters_unreadable: 0,
		created: 0,
		refreshed: 0,
		superseded: 0,
		cost: 0,
		started_at: "2026-11-10T03:00:00Z",
		...overrides,
	};
}

describe("proposals and routing", () => {
	test("a low-confidence tier is no proposal", () => {
		expect(proposedTier({ tier: "MEDIUM", confidence: "high" })).toBe("MEDIUM");
		expect(proposedTier({ tier: "MEDIUM", confidence: undefined })).toBe("MEDIUM");
		expect(proposedTier({ tier: "MEDIUM", confidence: "Low" })).toBeNull();
	});

	test("the routing split lists the busiest tier first and no match last", () => {
		expect(routingSplit({ none: 300, MEDIUM: 20, COMPLEX: 172, SIMPLE: 0 })).toEqual([
			{ tier: "COMPLEX", count: 172 },
			{ tier: "MEDIUM", count: 20 },
			{ tier: "none", count: 300 },
		]);
		expect(routingSplit(undefined)).toEqual([]);
	});
});

describe("runs", () => {
	test("progress follows the stage, and the suggesting stage its groups", () => {
		expect(runInProgress(run({ status: "queued" }))).toBe(true);
		expect(runInProgress(run({ status: "cancelled" }))).toBe(false);
		expect(runProgress(run({ status: "queued" }))).toBe(0);
		expect(runProgress(run({ status: "running", stage: "backfilling" }))).toBeGreaterThan(0);
		expect(runProgress(run({ status: "running", stage: "backfilling" }))).toBeLessThan(
			runProgress(run({ status: "running", stage: "reading" })),
		);
		expect(runProgress(run({ status: "running", stage: "reading" }))).toBeCloseTo(0.05);
		expect(runProgress(run({ status: "running", stage: "suggesting", clusters_asked: 10, clusters_done: 5 }))).toBeCloseTo(0.6);
		expect(runProgress(run({ status: "running", stage: "suggesting", clusters_asked: 0, clusters_done: 0 }))).toBeCloseTo(0.3);
		expect(runProgress(run({ status: "running", stage: "saving" }))).toBeCloseTo(0.95);
	});

	test("the window is whole days, at least one", () => {
		expect(runWindowDays(run({}))).toBe(7);
		expect(runWindowDays(run({ window_start: "2026-11-10T02:00:00Z" }))).toBe(1);
	});

	test("the breakdown narrows requests to groups and leaves out steps that took nothing", () => {
		const rows = runBreakdown(
			run({
				requests_read: 1000,
				requests_clustered: 600,
				requests_assigned: 380,
				requests_skipped: 20,
				requests_grouped: 900,
				clusters: 12,
				clusters_decided: 2,
				clusters_asked: 10,
				clusters_covered: 3,
				clusters_over_limit: 1,
				created: 4,
				refreshed: 3,
			}),
		);
		expect(rows.map((row) => [row.step, row.value, row.kind])).toEqual([
			["window", 1000, "total"],
			["skipped", 20, "minus"],
			["ungrouped", 80, "minus"],
			["grouped", 12, "result"],
			["decided", 2, "minus"],
			["overLimit", 1, "minus"],
			["covered", 3, "minus"],
			["shown", 7, "result"],
		]);
	});
});

describe("staged additions", () => {
	test("the staged phrases by tier, trimmed, blanks left out", () => {
		expect(
			stagedAdditions([
				{ id: "s1", tier: "SIMPLE", phrases: ["a", " "] },
				{ id: "s2", tier: "COMPLEX", phrases: [" c "] },
				{ id: "s3", tier: "SIMPLE", phrases: ["b"] },
			]),
		).toEqual({ SIMPLE: ["a", "b"], COMPLEX: ["c"] });
	});
});