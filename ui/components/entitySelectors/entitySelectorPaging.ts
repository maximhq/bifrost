// Page accumulation for the async entity selectors. Kept free of React so the
// merge rules can be unit tested; the hook that drives it lives in
// entitySelector.tsx.

export interface EntityPageEntry {
	value: string;
	label: string;
	description?: string;
}

/** Everything fetched so far for one search. */
export interface EntityPages {
	search: string;
	entries: EntityPageEntry[];
	/** Offset of the next page to request. Counts raw rows, so it stays in step with the server. */
	nextOffset: number;
	total: number;
}

export interface EntityPage {
	search: string;
	offset: number;
	entries: EntityPageEntry[];
	total: number;
}

export const EMPTY_ENTITY_PAGES: EntityPages = { search: "", entries: [], nextOffset: 0, total: 0 };

/**
 * Folds one fetched page into what has been loaded so far. A first page
 * starts over, so reopening the picker or changing the search never shows rows
 * from before. A later page only lands if it is the next one expected; a
 * repeat or a page from an older search leaves the state untouched.
 */
export function mergeEntityPage(prev: EntityPages, page: EntityPage): EntityPages {
	if (page.offset === 0) {
		return { search: page.search, entries: page.entries, nextOffset: page.entries.length, total: page.total };
	}
	if (page.search !== prev.search || page.offset !== prev.nextOffset) return prev;
	// A row deleted mid-scroll shifts the rest up a slot, so a later page can
	// repeat one already shown.
	const seen = new Set(prev.entries.map((entry) => entry.value));
	const fresh = page.entries.filter((entry) => !seen.has(entry.value));
	return {
		search: page.search,
		entries: fresh.length > 0 ? [...prev.entries, ...fresh] : prev.entries,
		nextOffset: page.offset + page.entries.length,
		// An empty page means the total was stale; trust what was actually
		// returned so the picker stops asking.
		total: page.entries.length === 0 ? page.offset : page.total,
	};
}

/** What the picker has loaded after this render: the page that just landed, if any, merged into the rest. */
export function accumulatePages(
	prev: EntityPages,
	s: { open: boolean; search: string; offset: number; entries: EntityPageEntry[] | undefined; total: number | undefined },
): EntityPages {
	// Closed means the next opening starts over; otherwise a failed first page would show the last opening's rows.
	if (!s.open) return EMPTY_ENTITY_PAGES;
	if (!s.entries) return prev;
	return mergeEntityPage(prev, { search: s.search, offset: s.offset, entries: s.entries, total: s.total ?? s.offset + s.entries.length });
}

export function hasMoreEntities(pages: EntityPages): boolean {
	return pages.nextOffset < pages.total;
}
/**
 * Whether the picker should fetch the next page on its own, to fill a list
 * shorter than a page. Never after a failed page: retrying is left to the user.
 */
export function shouldAutoLoadMore(state: {
	open: boolean;
	hasMore: boolean;
	isFetching: boolean;
	isError: boolean;
	visibleCount: number;
	pageSize: number;
}): boolean {
	return state.open && state.hasMore && !state.isFetching && !state.isError && state.visibleCount < state.pageSize;
}

/**
 * A failed page is never merged, so its offset is still the next one and
 * requesting it again leaves the query args unchanged. Loading more then has
 * to refetch instead of advancing.
 */
export function loadMoreNeedsRefetch(pages: EntityPages, offset: number, isError: boolean): boolean {
	return isError && offset === pages.nextOffset;
}
/** An earlier search's rows stay up while the new one loads, but not once its first page has failed. */
export function showsLoadedRows(s: { rowsAreCurrent: boolean; isBusy: boolean; isError: boolean }): boolean {
	return s.rowsAreCurrent || s.isBusy || !s.isError;
}

/**
 * What the multi picker's list shows besides its rows. `pinnedCount` is the
 * selected entities outside the loaded page, shown above it so they can be
 * unticked; they are not search results.
 */
export function multiListState(s: {
	isError: boolean;
	isBusy: boolean;
	rowCount: number;
	pinnedCount: number;
	/** False while the loaded rows still belong to an earlier search. */
	rowsAreCurrent: boolean;
}): { body: "rows" | "loading" | "empty" | "firstPageError"; laterPageRetry: boolean; showRows: boolean; rowsSelectable: boolean } {
	const showRows = showsLoadedRows(s);
	const rowCount = showRows ? s.rowCount : 0;
	const navigable = s.pinnedCount + rowCount;
	let body: "rows" | "loading" | "empty" | "firstPageError" = "rows";
	// Checked on rows, not navigable: pinned selections must not hide a failed first page.
	if (!s.isBusy && s.isError && rowCount === 0) body = "firstPageError";
	else if (!s.isBusy && navigable === 0) body = "empty";
	else if (s.isBusy && navigable === 0) body = "loading";
	// An earlier search's rows only hold the list's place; picking one would add an entity the input does not match.
	return { body, laterPageRetry: s.isError && rowCount > 0, showRows, rowsSelectable: s.rowsAreCurrent };
}

/** Backspace on an empty search drops the last chip, like a tag input. Null leaves the key to the input. */
export function chipsAfterBackspace(selectedIds: string[], search: string): string[] | null {
	if (search.length > 0 || selectedIds.length === 0) return null;
	return selectedIds.slice(0, -1);
}