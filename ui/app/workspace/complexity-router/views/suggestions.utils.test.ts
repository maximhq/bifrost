import { describe, expect, test } from "vitest";
import { isSuggestionRunInProgress, type EditableKeywordConfig, type Suggestion, type SuggestionRun } from "@/lib/types/complexityRouter";
import {
	addPhrases,
	categoryCounts,
	filterSuggestions,
	newPhrases,
	removePhrases,
	replacePhrase,
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
		status: "open",
		tier: "SIMPLE",
		phrases,
		category,
		reason,
		request_count: 10,
		coverage: 1,
		nearest_similarity: 0.4,
		example_log_ids: [],
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
	];

	test("searches phrases and reasons, filters by category, and leaves out staged ones", () => {
		expect(filterSuggestions(list, { query: "REFUND", category: "all" }, new Set()).map((s) => s.id)).toEqual(["a"]);
		expect(filterSuggestions(list, { query: "", category: "weak" }, new Set(["c"])).map((s) => s.id)).toEqual(["a"]);
		expect(categoryCounts(list, new Set(["c"]))).toEqual({ all: 2, uncovered: 1, weak: 1, misrouted: 0, mixed: 0 });
	});
});

describe("isSuggestionRunInProgress", () => {
	test("a queued run is in progress until it ends, as a running one is", () => {
		const run = (status: SuggestionRun["status"]) => ({ status }) as SuggestionRun;
		expect(isSuggestionRunInProgress(run("queued"))).toBe(true);
		expect(isSuggestionRunInProgress(run("running"))).toBe(true);
		for (const status of ["succeeded", "failed", "skipped", "cancelled"] as const) {
			expect(isSuggestionRunInProgress(run(status)), status).toBe(false);
		}
		expect(isSuggestionRunInProgress(undefined)).toBe(false);
	});
});