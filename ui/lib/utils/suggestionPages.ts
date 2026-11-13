import type { Suggestion, SuggestionsList } from "@/lib/types/complexityRouter";

// SUGGESTIONS_PAGE_SIZE is how many suggestions the list loads at a time.
export const SUGGESTIONS_PAGE_SIZE = 100;

// nextSuggestionsOffset is the offset of the page after lastPage, or
// undefined when lastPage reached the total. An empty page ends the list too:
// rows that left since the total was counted would otherwise ask forever.
export function nextSuggestionsOffset(lastPage: SuggestionsList, lastOffset: number): number | undefined {
	const loaded = lastPage.suggestions.length;
	if (loaded === 0 || typeof lastPage.total !== "number") return undefined;
	const next = lastOffset + loaded;
	return next < lastPage.total ? next : undefined;
}

// mergeSuggestionPages joins loaded pages into one list. A row can appear on
// two pages when rows before it left between their loads; its first copy is
// kept.
export function mergeSuggestionPages(pages: SuggestionsList[] | undefined): Suggestion[] {
	const seen = new Set<string>();
	const rows: Suggestion[] = [];
	for (const page of pages ?? []) {
		for (const row of page.suggestions) {
			if (seen.has(row.id)) continue;
			seen.add(row.id);
			rows.push(row);
		}
	}
	return rows;
}