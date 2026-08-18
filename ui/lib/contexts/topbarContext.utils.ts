/**
 * Ownership bookkeeping for the topbar title.
 *
 * A page names itself in the topbar on mount and gives the name back on
 * unmount. React runs those out of order across a route change: the incoming
 * page's effect fires *before* the outgoing page's cleanup, so a release that
 * did not check ownership would wipe the title the new page just set.
 *
 * Ownership is tracked with an opaque per-caller token rather than the title
 * text, because two routes are free to use the same title string and text
 * comparison cannot tell "the page I replaced" from "the page that replaced
 * me".
 */
export interface TopbarTitleEntry {
	/** null means "fall back to the route-derived title". */
	value: string | null;
	/** Opaque token identifying the caller that set `value`. */
	owner: symbol | null;
}

export const EMPTY_TITLE_ENTRY: TopbarTitleEntry = { value: null, owner: null };

/** A caller takes the title, becoming its owner. */
export function claimTitle(current: TopbarTitleEntry, owner: symbol, value: string | null): TopbarTitleEntry {
	if (current.value === value && current.owner === owner) return current;
	return { value, owner };
}

/** A caller gives the title back. A no-op unless that caller still owns it. */
export function releaseTitle(current: TopbarTitleEntry, owner: symbol): TopbarTitleEntry {
	return current.owner === owner ? EMPTY_TITLE_ENTRY : current;
}