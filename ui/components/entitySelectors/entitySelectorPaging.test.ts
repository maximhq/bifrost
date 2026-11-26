import { describe, expect, it } from "vitest";

import {
	accumulatePages,
	chipsAfterBackspace,
	EMPTY_ENTITY_PAGES,
	hasMoreEntities,
	loadMoreNeedsRefetch,
	mergeEntityPage,
	multiListState,
	shouldAutoLoadMore,
	type EntityPageEntry,
} from "./entitySelectorPaging";

function rows(from: number, to: number): EntityPageEntry[] {
	return Array.from({ length: to - from }, (_, i) => ({ value: `id-${from + i}`, label: `Server ${from + i}` }));
}

describe("mergeEntityPage", () => {
	it("reports more rows when the first page is short of the total", () => {
		const pages = mergeEntityPage(EMPTY_ENTITY_PAGES, { search: "", offset: 0, entries: rows(0, 20), total: 32 });
		expect(pages.entries).toHaveLength(20);
		expect(pages.nextOffset).toBe(20);
		expect(hasMoreEntities(pages)).toBe(true);
	});

	it("appends the next page so rows past the first page are reachable", () => {
		let pages = mergeEntityPage(EMPTY_ENTITY_PAGES, { search: "", offset: 0, entries: rows(0, 20), total: 32 });
		pages = mergeEntityPage(pages, { search: "", offset: 20, entries: rows(20, 32), total: 32 });
		expect(pages.entries.map((entry) => entry.value)).toEqual(rows(0, 32).map((entry) => entry.value));
		expect(hasMoreEntities(pages)).toBe(false);
	});

	it("ignores a page it has already merged", () => {
		let pages = mergeEntityPage(EMPTY_ENTITY_PAGES, { search: "", offset: 0, entries: rows(0, 20), total: 32 });
		pages = mergeEntityPage(pages, { search: "", offset: 20, entries: rows(20, 32), total: 32 });
		expect(mergeEntityPage(pages, { search: "", offset: 20, entries: rows(20, 32), total: 32 })).toBe(pages);
	});

	it("starts over on a first page, dropping rows from an earlier search", () => {
		let pages = mergeEntityPage(EMPTY_ENTITY_PAGES, { search: "", offset: 0, entries: rows(0, 20), total: 32 });
		pages = mergeEntityPage(pages, { search: "neon", offset: 0, entries: rows(40, 42), total: 2 });
		expect(pages.search).toBe("neon");
		expect(pages.entries.map((entry) => entry.value)).toEqual(["id-40", "id-41"]);
		expect(hasMoreEntities(pages)).toBe(false);
	});

	it("drops a later page that belongs to another search", () => {
		const pages = mergeEntityPage(EMPTY_ENTITY_PAGES, { search: "neon", offset: 0, entries: rows(0, 20), total: 32 });
		expect(mergeEntityPage(pages, { search: "", offset: 20, entries: rows(20, 32), total: 32 })).toBe(pages);
	});

	it("skips rows repeated across pages but keeps the server offset", () => {
		let pages = mergeEntityPage(EMPTY_ENTITY_PAGES, { search: "", offset: 0, entries: rows(0, 20), total: 40 });
		pages = mergeEntityPage(pages, { search: "", offset: 20, entries: rows(19, 39), total: 40 });
		expect(pages.entries).toHaveLength(39);
		expect(pages.nextOffset).toBe(40);
	});

	it("stops asking when a later page comes back empty", () => {
		let pages = mergeEntityPage(EMPTY_ENTITY_PAGES, { search: "", offset: 0, entries: rows(0, 20), total: 25 });
		pages = mergeEntityPage(pages, { search: "", offset: 20, entries: [], total: 25 });
		expect(hasMoreEntities(pages)).toBe(false);
	});
});

describe("shouldAutoLoadMore", () => {
	const idle = { open: true, hasMore: true, isFetching: false, isError: false, visibleCount: 5, pageSize: 20 };

	it("keeps fetching while an open picker shows less than a page", () => {
		expect(shouldAutoLoadMore(idle)).toBe(true);
	});

	it("stops after a later page fails instead of leaving a silent partial list", () => {
		expect(shouldAutoLoadMore({ ...idle, isError: true })).toBe(false);
	});
});

describe("loadMoreNeedsRefetch", () => {
	// The failed page was never merged, so its offset is still the next one and
	// asking for it again would not change the query args.
	it("refetches when the requested page failed", () => {
		const pages = mergeEntityPage(EMPTY_ENTITY_PAGES, { search: "", offset: 0, entries: rows(0, 20), total: 40 });
		expect(loadMoreNeedsRefetch(pages, 20, true)).toBe(true);
	});

	it("advances normally when the last page loaded", () => {
		const pages = mergeEntityPage(EMPTY_ENTITY_PAGES, { search: "", offset: 0, entries: rows(0, 20), total: 40 });
		expect(loadMoreNeedsRefetch(pages, 0, false)).toBe(false);
	});
});

describe("multiListState", () => {
	const idle = { isError: false, isBusy: false, rowCount: 0, pinnedCount: 0, rowsAreCurrent: true };

	it("shows the empty message when nothing loaded and nothing is selected", () => {
		expect(multiListState(idle)).toEqual({ body: "empty", laterPageRetry: false, showRows: true, rowsSelectable: true });
	});

	it("shows the spinner while the first page loads", () => {
		expect(multiListState({ ...idle, isBusy: true })).toEqual({
			body: "loading",
			laterPageRetry: false,
			showRows: true,
			rowsSelectable: true,
		});
	});

	it("shows the first-page error when the first request fails", () => {
		expect(multiListState({ ...idle, isError: true })).toEqual({
			body: "firstPageError",
			laterPageRetry: false,
			showRows: true,
			rowsSelectable: true,
		});
	});

	// Selected entities outside the page are pinned above the list; they must not hide the failure or its retry.
	it("still shows the first-page error when selected entities are pinned", () => {
		expect(multiListState({ ...idle, isError: true, pinnedCount: 1 })).toEqual({
			body: "firstPageError",
			laterPageRetry: false,
			showRows: true,
			rowsSelectable: true,
		});
	});

	it("keeps loaded rows and offers a retry under them when a later page fails", () => {
		expect(multiListState({ ...idle, isError: true, rowCount: 20, pinnedCount: 1 })).toEqual({
			body: "rows",
			laterPageRetry: true,
			showRows: true,
			rowsSelectable: true,
		});
	});

	// The rows still on screen belong to the previous search; the new search never loaded a page.
	it("drops the previous search's rows when the new search's first page fails", () => {
		expect(multiListState({ ...idle, isError: true, rowCount: 20, rowsAreCurrent: false })).toEqual({
			body: "firstPageError",
			laterPageRetry: false,
			showRows: false,
			rowsSelectable: false,
		});
	});

	it("keeps the previous search's rows on screen while the new search loads", () => {
		expect(multiListState({ ...idle, isBusy: true, rowCount: 20, rowsAreCurrent: false })).toEqual({
			body: "rows",
			laterPageRetry: false,
			showRows: true,
			rowsSelectable: false,
		});
	});

	it("keeps rows selectable while a later page of the same search loads", () => {
		expect(multiListState({ ...idle, isBusy: true, rowCount: 20 })).toEqual({
			body: "rows",
			laterPageRetry: false,
			showRows: true,
			rowsSelectable: true,
		});
	});

	it("lists pinned selections while the first page is still loading", () => {
		expect(multiListState({ ...idle, isBusy: true, pinnedCount: 2 })).toEqual({
			body: "rows",
			laterPageRetry: false,
			showRows: true,
			rowsSelectable: true,
		});
	});
});

describe("chipsAfterBackspace", () => {
	it("drops only the last chip when the search is empty", () => {
		expect(chipsAfterBackspace(["a", "b", "c"], "")).toEqual(["a", "b"]);
	});

	it("leaves chips alone while the search has text, so Backspace edits it", () => {
		expect(chipsAfterBackspace(["a", "b"], "te")).toBeNull();
	});

	it("does nothing with no chips", () => {
		expect(chipsAfterBackspace([], "")).toBeNull();
	});
});

describe("accumulatePages", () => {
	const loaded = mergeEntityPage(EMPTY_ENTITY_PAGES, { search: "", offset: 0, entries: rows(0, 20), total: 40 });

	it("merges the page that just landed", () => {
		const pages = accumulatePages(EMPTY_ENTITY_PAGES, { open: true, search: "", offset: 0, entries: rows(0, 20), total: 40 });
		expect(pages).toEqual(loaded);
	});

	it("keeps what was loaded while the next page is in flight", () => {
		expect(accumulatePages(loaded, { open: true, search: "", offset: 20, entries: undefined, total: undefined })).toBe(loaded);
	});

	// Otherwise a reopened picker whose first request fails shows the last opening's rows as a later-page failure.
	it("forgets the rows once the picker closes", () => {
		expect(accumulatePages(loaded, { open: false, search: "", offset: 0, entries: undefined, total: undefined })).toEqual(
			EMPTY_ENTITY_PAGES,
		);
	});
});