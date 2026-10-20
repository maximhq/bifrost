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