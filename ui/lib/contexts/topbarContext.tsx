import { createContext, useContext, useEffect, useMemo, useRef, useState, type Dispatch, type SetStateAction } from "react";
import { EMPTY_TITLE_ENTRY, claimTitle, releaseTitle, type TopbarTitleEntry } from "./topbarContext.utils";

interface TopbarContextValue {
	/** Page-supplied title override; null means "fall back to the route-derived title". */
	title: string | null;
	/** Write side, ownership-aware. Prefer useSetTopbarTitle over calling this directly. */
	setTitleEntry: Dispatch<SetStateAction<TopbarTitleEntry>>;
	/**
	 * DOM node the topbar exposes next to the title. Page descriptions are
	 * portalled into it rather than lifted through state, because a description
	 * is arbitrary JSX (links, <code>, conditional spans) whose identity changes
	 * every render — storing that in context would loop.
	 */
	descriptionSlot: HTMLElement | null;
	setDescriptionSlot: Dispatch<SetStateAction<HTMLElement | null>>;
	/** Mobile-only anchor for a page's collapsed filter-sidebar trigger. */
	mobileFilterSlot: HTMLElement | null;
	setMobileFilterSlot: Dispatch<SetStateAction<HTMLElement | null>>;
}

const TopbarContext = createContext<TopbarContextValue | null>(null);

export function TopbarProvider({ children }: { children: React.ReactNode }) {
	const [titleEntry, setTitleEntry] = useState<TopbarTitleEntry>(EMPTY_TITLE_ENTRY);
	const [descriptionSlot, setDescriptionSlot] = useState<HTMLElement | null>(null);
	const [mobileFilterSlot, setMobileFilterSlot] = useState<HTMLElement | null>(null);
	const value = useMemo(
		() => ({ title: titleEntry.value, setTitleEntry, descriptionSlot, setDescriptionSlot, mobileFilterSlot, setMobileFilterSlot }),
		[titleEntry.value, descriptionSlot, mobileFilterSlot],
	);
	return <TopbarContext.Provider value={value}>{children}</TopbarContext.Provider>;
}

/** Read side — used by <Topbar>. Returns null outside a provider so the topbar still renders. */
export function useTopbarTitle(): string | null {
	return useContext(TopbarContext)?.title ?? null;
}

/** Registers the topbar's description anchor. Called by <Topbar> via a ref callback. */
export function useDescriptionSlotRef() {
	return useContext(TopbarContext)?.setDescriptionSlot;
}

/** Read side for <PageTitle>, which portals its description into this node. */
export function useDescriptionSlot(): HTMLElement | null {
	return useContext(TopbarContext)?.descriptionSlot ?? null;
}

/** Registers the mobile filter-trigger anchor exposed by <Topbar>. */
export function useMobileFilterSlotRef() {
	return useContext(TopbarContext)?.setMobileFilterSlot;
}

/** Read side for filter sidebars, which portal their mobile trigger here. */
export function useMobileFilterSlot(): HTMLElement | null {
	return useContext(TopbarContext)?.mobileFilterSlot ?? null;
}

/**
 * Write side — a page calls this to name itself in the topbar instead of
 * rendering its own <h1>:
 *
 *   useSetTopbarTitle("Budgets & Limits");
 *
 * Pass undefined/null to leave the route-derived fallback in place.
 *
 * The title is cleared on unmount, but only if this caller still owns it:
 * during a route transition the incoming page mounts and sets its title before
 * the outgoing page's cleanup runs, so an unconditional reset would wipe the
 * new title. Ownership is an opaque per-caller token rather than the title
 * text, because two routes may legitimately share a title string — see
 * topbarContext.utils.ts.
 */
export function useSetTopbarTitle(title: string | null | undefined) {
	const setTitleEntry = useContext(TopbarContext)?.setTitleEntry;
	const resolved = title ?? null;
	// One token per hook instance, stable for its whole lifetime.
	const ownerRef = useRef<symbol | null>(null);
	if (ownerRef.current === null) ownerRef.current = Symbol("topbar-title");
	const owner = ownerRef.current;

	useEffect(() => {
		if (!setTitleEntry) return;
		setTitleEntry((current) => claimTitle(current, owner, resolved));
	}, [setTitleEntry, owner, resolved]);

	useEffect(() => {
		if (!setTitleEntry) return;
		return () => setTitleEntry((current) => releaseTitle(current, owner));
	}, [setTitleEntry, owner]);
}