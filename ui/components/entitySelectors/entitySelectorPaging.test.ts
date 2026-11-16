import { describe, expect, it } from "vitest";

import {
	EMPTY_ENTITY_PAGES,
	hasMoreEntities,
	loadMoreNeedsRefetch,
	mergeEntityPage,
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