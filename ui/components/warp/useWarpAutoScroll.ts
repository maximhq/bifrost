import { useCallback, useEffect, useRef, useState } from "react";

const PIN_THRESHOLD_PX = 48;
// The gap to the bottom shrinks by ~63% every FOLLOW_TAU_MS, so a glide reads as a decelerating flick.
const FOLLOW_TAU_MS = 90;
// Longest frame step the follow trusts; a backgrounded tab would otherwise land in one jump.
const MAX_FRAME_MS = 64;

interface UseWarpAutoScrollResult {
	/** Attach to the element wrapping the ScrollArea. */
	containerRef: (node: HTMLDivElement | null) => void;
	/** Attach to the scrolled content inside the viewport. */
	contentRef: (node: HTMLDivElement | null) => void;
	isPinned: boolean;
	scrollToBottom: () => void;
}

/** One frame of the follow, independent of frame rate. Lands exactly on target once within half a pixel. */
export function followStep(current: number, target: number, dtMs: number, tauMs = FOLLOW_TAU_MS): number {
	const gap = target - current;
	if (Math.abs(gap) <= 0.5) return target;
	return current + gap * (1 - Math.exp(-Math.min(dtMs, MAX_FRAME_MS) / tauMs));
}

/** Where a glide starts: from more than two viewports away it skips to one viewport short, so the motion stays short. */
export function glideStart(current: number, target: number, viewportHeight: number): number {
	if (Math.abs(target - current) <= viewportHeight * 2) return current;
	return target - Math.sign(target - current) * viewportHeight;
}

/**
 * Pins on content size via ResizeObserver, since turns keep growing after commit (lazy markdown, tool rows).
 * Callback refs, not ref objects: the transcript mounts later, after the panel's config placeholder.
 * Pinned growth glides rather than jumps, so a streamed answer slides up like a chat thread instead of stepping a line at a time.
 */
export function useWarpAutoScroll(): UseWarpAutoScrollResult {
	const [container, setContainer] = useState<HTMLDivElement | null>(null);
	const [content, setContent] = useState<HTMLDivElement | null>(null);
	// Ref as well as state: the ResizeObserver callback would otherwise read a stale value.
	const pinnedRef = useRef(true);
	const [isPinned, setIsPinned] = useState(true);
	// Set by the effect, which owns the viewport and the animation frame.
	const glideRef = useRef<(() => void) | null>(null);

	// Radix does not expose its viewport node, so find it by the data-slot it sets.
	const viewportOf = (node: HTMLDivElement | null) => node?.querySelector<HTMLElement>('[data-slot="scroll-area-viewport"]') ?? null;

	const scrollToBottom = useCallback(() => {
		pinnedRef.current = true;
		setIsPinned(true);
		glideRef.current?.();
	}, []);

	useEffect(() => {
		const viewport = viewportOf(container);
		if (!container || !viewport || !content) return;

		const reducedMotion = window.matchMedia?.("(prefers-reduced-motion: reduce)");
		const bottom = () => viewport.scrollHeight - viewport.clientHeight;
		let frame = 0;
		// Kept as a float: scrollTop rounds to device pixels, which would stall the last few steps.
		let position = 0;
		let lastFrameAt = 0;

		const tick = (now: number) => {
			const target = bottom();
			position = followStep(position, target, now - lastFrameAt);
			lastFrameAt = now;
			viewport.scrollTop = position;
			frame = position === target ? 0 : requestAnimationFrame(tick);
		};

		// A running glide rereads the bottom every frame, so later growth only retargets it.
		const glide = () => {
			if (reducedMotion?.matches) {
				viewport.scrollTop = bottom();
				return;
			}
			if (frame) return;
			position = glideStart(viewport.scrollTop, bottom(), viewport.clientHeight);
			lastFrameAt = performance.now();
			frame = requestAnimationFrame(tick);
		};
		glideRef.current = glide;

		const cancelGlide = () => {
			cancelAnimationFrame(frame);
			frame = 0;
		};

		const onScroll = () => {
			// The glide's own writes pass through the middle of the transcript; they must not unpin it.
			if (frame) return;
			const atBottom = viewport.scrollHeight - viewport.scrollTop - viewport.clientHeight <= PIN_THRESHOLD_PX;
			if (atBottom === pinnedRef.current) return;
			pinnedRef.current = atBottom;
			setIsPinned(atBottom);
		};

		// The first observation is the transcript arriving (panel open, stored thread), which lands at the bottom without a glide.
		let observed = false;
		const onResize = () => {
			if (!pinnedRef.current) return;
			if (!observed) {
				observed = true;
				viewport.scrollTop = bottom();
				return;
			}
			glide();
		};

		// Without ResizeObserver, lose growth-following rather than throw after mount.
		const observer = typeof ResizeObserver === "undefined" ? null : new ResizeObserver(onResize);
		observer?.observe(content);
		viewport.addEventListener("scroll", onScroll, { passive: true });
		// Any sign of the user taking the scroll hands it back to them, scrollbar drags included.
		const userIntent = ["wheel", "touchstart", "pointerdown", "keydown"] as const;
		for (const type of userIntent) container.addEventListener(type, cancelGlide, { passive: true });

		return () => {
			cancelGlide();
			glideRef.current = null;
			observer?.disconnect();
			viewport.removeEventListener("scroll", onScroll);
			for (const type of userIntent) container.removeEventListener(type, cancelGlide);
		};
	}, [container, content]);

	return { containerRef: setContainer, contentRef: setContent, isPinned, scrollToBottom };
}