import { useCallback, useEffect, useRef, useState } from "react";

/** How close to the bottom still counts as "following the answer", in pixels. */
const PIN_THRESHOLD_PX = 48;

interface UseWarpAutoScrollResult {
	/** Attach to the element wrapping the ScrollArea. */
	containerRef: (node: HTMLDivElement | null) => void;
	/** Attach to the scrolled content inside the viewport. */
	contentRef: (node: HTMLDivElement | null) => void;
	/** False once the reader scrolls up to look at something. */
	isPinned: boolean;
	/** Jump to the newest message and start following again. */
	scrollToBottom: () => void;
}

/**
 * Keeps the transcript pinned to its newest content.
 *
 * The obvious version - a sentinel div and scrollIntoView in an effect keyed on
 * the message count - does not work here, and the reason is worth stating: the
 * effect runs when React commits, but almost nothing in an Warp turn has its
 * final height by then. Tool rows appear one at a time as steps finish, the
 * markdown renderer is lazy-loaded behind Suspense so an answer commits as a
 * fallback and grows when Shiki arrives, and tables reflow after that. Scrolling
 * to the bottom of a body that has not finished growing leaves the newest
 * content off-screen - which looks exactly like the auto-scroll not firing.
 *
 * So the trigger is size, not renders. A ResizeObserver on the content watches
 * the transcript's real height and re-pins on every change, whoever caused it -
 * which covers each tool row as it lands, not just completed messages.
 *
 * The nodes arrive through callback refs rather than ref objects. The panel
 * renders a "not configured" placeholder while its config query is in flight, so
 * on first commit there is no transcript to observe at all; an effect holding
 * ref objects would find nothing, bail, and never run again once the real
 * transcript replaced the placeholder. Callback refs put the nodes in state, so
 * the effect re-runs the moment they exist.
 *
 * The pin releases as soon as the reader scrolls up. Dragging someone back down
 * mid-sentence because a token arrived is worse than not following at all, so
 * following is a mode they stay in until they leave it, and re-enter by
 * returning to the bottom.
 */
export function useWarpAutoScroll(): UseWarpAutoScrollResult {
	const [container, setContainer] = useState<HTMLDivElement | null>(null);
	const [content, setContent] = useState<HTMLDivElement | null>(null);
	// A ref as well as state: the ResizeObserver callback closes over the render
	// that created it, so reading the state variable there would see a stale value
	// for the rest of the turn.
	const pinnedRef = useRef(true);
	const [isPinned, setIsPinned] = useState(true);

	// Radix does not expose its viewport node as a prop, and adding one would put
	// a component every page uses into this diff for one panel's benefit. The
	// data-slot attribute it already sets is the seam.
	const viewportOf = (node: HTMLDivElement | null) => node?.querySelector<HTMLElement>('[data-slot="scroll-area-viewport"]') ?? null;

	const scrollToBottom = useCallback(() => {
		const viewport = viewportOf(container);
		pinnedRef.current = true;
		setIsPinned(true);
		if (viewport) viewport.scrollTop = viewport.scrollHeight;
	}, [container]);

	useEffect(() => {
		const viewport = viewportOf(container);
		if (!viewport || !content) return;

		const onScroll = () => {
			const atBottom = viewport.scrollHeight - viewport.scrollTop - viewport.clientHeight <= PIN_THRESHOLD_PX;
			if (atBottom === pinnedRef.current) return;
			pinnedRef.current = atBottom;
			setIsPinned(atBottom);
		};

		const observer = new ResizeObserver(() => {
			if (pinnedRef.current) viewport.scrollTop = viewport.scrollHeight;
		});
		observer.observe(content);
		viewport.addEventListener("scroll", onScroll, { passive: true });

		return () => {
			observer.disconnect();
			viewport.removeEventListener("scroll", onScroll);
		};
	}, [container, content]);

	return { containerRef: setContainer, contentRef: setContent, isPinned, scrollToBottom };
}