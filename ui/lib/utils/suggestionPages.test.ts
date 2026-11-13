import type { Suggestion, SuggestionsList } from "@/lib/types/complexityRouter";
import { describe, expect, it } from "vitest";
import { mergeSuggestionPages, nextSuggestionsOffset } from "./suggestionPages";

const row = (id: string) => ({ id }) as Suggestion;
const page = (ids: string[], total: number, offset: number): SuggestionsList =>
	({ suggestions: ids.map(row), total, limit: 2, offset, counts: {}, enabled: true, router_id: "default" }) as SuggestionsList;

describe("nextSuggestionsOffset", () => {
	it("asks for the rows after the last page while the total has more", () => {
		expect(nextSuggestionsOffset(page(["a", "b"], 5, 0), 0)).toBe(2);
		expect(nextSuggestionsOffset(page(["c", "d"], 5, 2), 2)).toBe(4);
	});
	it("stops at the total", () => {
		expect(nextSuggestionsOffset(page(["e"], 5, 4), 4)).toBeUndefined();
		expect(nextSuggestionsOffset(page(["a", "b"], 2, 0), 0)).toBeUndefined();
	});
	it("stops on an empty page, even when the total says more", () => {
		// Rows dismissed since the total was counted leave a short tail.
		expect(nextSuggestionsOffset(page([], 5, 4), 4)).toBeUndefined();
	});
	it("stops when the response carries no total", () => {
		const { total: _total, ...rest } = page(["a", "b"], 5, 0);
		expect(nextSuggestionsOffset(rest as SuggestionsList, 0)).toBeUndefined();
	});
});

describe("mergeSuggestionPages", () => {
	it("joins the pages in order", () => {
		expect(mergeSuggestionPages([page(["a", "b"], 4, 0), page(["c", "d"], 4, 2)]).map((s) => s.id)).toEqual(["a", "b", "c", "d"]);
	});
	it("keeps the first copy of a row that moved to a later page", () => {
		// A row is on two pages when rows before it left between their loads.
		expect(mergeSuggestionPages([page(["a", "b"], 4, 0), page(["b", "c"], 4, 2)]).map((s) => s.id)).toEqual(["a", "b", "c"]);
	});
	it("is empty with no pages", () => {
		expect(mergeSuggestionPages(undefined)).toEqual([]);
	});
});