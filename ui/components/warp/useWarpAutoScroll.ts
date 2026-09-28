import { useCallback, useEffect, useRef, useState } from "react";

const PIN_THRESHOLD_PX = 48;

interface UseWarpAutoScrollResult {
	/** Attach to the element wrapping the ScrollArea. */
	containerRef: (node: HTMLDivElement | null) => void;
	/** Attach to the scrolled content inside the viewport. */
	contentRef: (node: HTMLDivElement | null) => void;
	isPinned: boolean;
	scrollToBottom: () => void;
}

/**
 * Pins on content size via ResizeObserver, since turns keep growing after commit (lazy markdown, tool rows).
 * Callback refs, not ref objects: the transcript mounts later, after the panel's config placeholder.
 */
export function useWarpAutoScroll(): UseWarpAutoScrollResult {
	const [container, setContainer] = useState<HTMLDivElement | null>(null);
	const [content, setContent] = useState<HTMLDivElement | null>(null);
	// Ref as well as state: the ResizeObserver callback would otherwise read a stale value.
	const pinnedRef = useRef(true);
	const [isPinned, setIsPinned] = useState(true);

	// Radix does not expose its viewport node, so find it by the data-slot it sets.
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

		// Without ResizeObserver, lose growth-following rather than throw after mount.
		const observer =
			typeof ResizeObserver === "undefined"
				? null
				: new ResizeObserver(() => {
						if (pinnedRef.current) viewport.scrollTop = viewport.scrollHeight;
					});
		observer?.observe(content);
		viewport.addEventListener("scroll", onScroll, { passive: true });

		return () => {
			observer?.disconnect();
			viewport.removeEventListener("scroll", onScroll);
		};
	}, [container, content]);

	return { containerRef: setContainer, contentRef: setContent, isPinned, scrollToBottom };
}