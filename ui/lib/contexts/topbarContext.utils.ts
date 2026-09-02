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
/** One hop in a breadcrumb trail. `to` makes it a link; the last crumb (the
 * current page) normally has none. */
export interface Breadcrumb {
	label: string;
	to?: string;
}

/**
 * A page names itself either with a plain title or with a breadcrumb trail.
 * The trail is data, not JSX, so the topbar keeps full control of the styling
 * and no page can smuggle its own markup into the shell.
 */
export type TopbarTitleValue = string | Breadcrumb[];

export interface TopbarTitleEntry {
	/** null means "fall back to the route-derived title". */
	value: TopbarTitleValue | null;
	/** Opaque token identifying the caller that set `value`. */
	owner: symbol | null;
}

/**
 * Structural equality, because a breadcrumb trail is almost always a fresh
 * array literal on every render. Identity comparison would make claimTitle's
 * "nothing changed" guard never fire and set state on every render.
 */
export function sameTitle(a: TopbarTitleValue | null, b: TopbarTitleValue | null): boolean {
	if (a === b) return true;
	if (!Array.isArray(a) || !Array.isArray(b)) return false;
	return a.length === b.length && a.every((crumb, i) => crumb.label === b[i].label && crumb.to === b[i].to);
}

export const EMPTY_TITLE_ENTRY: TopbarTitleEntry = { value: null, owner: null };

/** A caller takes the title, becoming its owner. */
export function claimTitle(current: TopbarTitleEntry, owner: symbol, value: TopbarTitleValue | null): TopbarTitleEntry {
	if (sameTitle(current.value, value) && current.owner === owner) return current;
	return { value, owner };
}

/** A caller gives the title back. A no-op unless that caller still owns it. */
export function releaseTitle(current: TopbarTitleEntry, owner: symbol): TopbarTitleEntry {
	return current.owner === owner ? EMPTY_TITLE_ENTRY : current;
}