import { createContext, useContext, useEffect, useMemo, useRef, useState, type Dispatch, type SetStateAction } from "react";
import {
	EMPTY_TITLE_ENTRY,
	claimTitle,
	releaseTitle,
	titleKey,
	type Breadcrumb,
	type TopbarTitleEntry,
	type TopbarTitleValue,
} from "./topbarContext.utils";

interface TopbarContextValue {
	/** Page-supplied title override; null means "fall back to the route-derived title". */
	title: TopbarTitleValue | null;
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
export function useTopbarTitle(): TopbarTitleValue | null {
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
 * It also accepts a breadcrumb trail for a page nested under another:
 *
 *   useSetTopbarTitle([{ label: "Webhooks", to: "/workspace/webhooks" }, { label: "Deliveries" }]);
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
export function useSetTopbarTitle(title: TopbarTitleValue | null | undefined) {
	const setTitleEntry = useContext(TopbarContext)?.setTitleEntry;
	const resolved = title ?? null;
	// A breadcrumb trail is a fresh array literal each render, so the effect
	// keys off its content rather than its identity — otherwise it would refire
	// on every parent render.
	const key = titleKey(resolved);
	const resolvedRef = useRef(resolved);
	resolvedRef.current = resolved;

	// What actually gets parked in context. A crumb's onSelect is a fresh closure
	// every render and the equality guard ignores its identity, so parking the
	// caller's own closure would leave the topbar invoking the one from whichever
	// render happened to claim the title — captured state and all. Each handler
	// is replaced by a forwarder that re-reads the caller's newest closure out of
	// resolvedRef when the crumb is actually clicked, which keeps the guard cheap
	// and the callback current at the same time.
	const forwarding = useMemo(() => {
		if (!Array.isArray(resolved)) return resolved;
		return resolved.map(
			(crumb, index): Breadcrumb =>
				crumb.onSelect
					? {
							...crumb,
							onSelect: () => {
								const latest = resolvedRef.current;
								if (Array.isArray(latest)) latest[index]?.onSelect?.();
							},
						}
					: crumb,
		);
		// Rebuilt only when the trail itself changes; see titleKey.
		// eslint-disable-next-line react-hooks/exhaustive-deps
	}, [key]);
	const forwardingRef = useRef(forwarding);
	forwardingRef.current = forwarding;
	// One token per hook instance, stable for its whole lifetime.
	const ownerRef = useRef<symbol | null>(null);
	if (ownerRef.current === null) ownerRef.current = Symbol("topbar-title");
	const owner = ownerRef.current;

	useEffect(() => {
		if (!setTitleEntry) return;
		setTitleEntry((current) => claimTitle(current, owner, forwardingRef.current));
	}, [setTitleEntry, owner, key]);

	useEffect(() => {
		if (!setTitleEntry) return;
		return () => setTitleEntry((current) => releaseTitle(current, owner));
	}, [setTitleEntry, owner]);
}