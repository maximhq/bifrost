import { Column, ColumnPinningState } from "@tanstack/react-table";
import { CSSProperties, useLayoutEffect, useRef, useState } from "react";

/**
 * Measures actual header cell DOM widths and computes pixel-perfect
 * sticky offsets for pinned columns.
 */
export function usePinOffsets(
	headerCellRefs: React.MutableRefObject<Map<string, HTMLTableCellElement>>,
	columnPinning: ColumnPinningState,
) {
	const [offsets, setOffsets] = useState<Map<string, number>>(new Map());

	// Serialize pinning arrays to stable strings so the effect only fires
	// when the actual pinned column IDs change, not on every render.
	const leftKey = (columnPinning.left ?? []).join(",");
	const rightKey = (columnPinning.right ?? []).join(",");

	useLayoutEffect(() => {
		const leftPinned = leftKey ? leftKey.split(",") : [];
		const rightPinned = rightKey ? rightKey.split(",") : [];
		const next = new Map<string, number>();

		let left = 0;
		for (const id of leftPinned) {
			next.set(id, left);
			const el = headerCellRefs.current.get(id);
			if (el) left += el.getBoundingClientRect().width;
		}

		let right = 0;
		for (let i = rightPinned.length - 1; i >= 0; i--) {
			const id = rightPinned[i];
			next.set(id, right);
			const el = headerCellRefs.current.get(id);
			if (el) right += el.getBoundingClientRect().width;
		}

		// Only update if offsets actually changed to avoid infinite re-render loops
		setOffsets((prev) => {
			if (prev.size === next.size) {
				let same = true;
				for (const [k, v] of next) {
					if (prev.get(k) !== v) {
						same = false;
						break;
					}
				}
				if (same) return prev;
			}
			return next;
		});
	}, [leftKey, rightKey, headerCellRefs]);

	return offsets;
}

/**
 * Returns a ref callback setter and the refs map for header cell measurement.
 */
export function useHeaderCellRefs() {
	const refs = useRef<Map<string, HTMLTableCellElement>>(new Map());

	const setRef = (columnId: string) => (el: HTMLTableCellElement | null) => {
		if (el) refs.current.set(columnId, el);
		else refs.current.delete(columnId);
	};

	return { headerCellRefs: refs, setHeaderCellRef: setRef };
}

/**
 * Builds a CSS style object for a pinned column using measured offsets.
 */
export function buildPinStyle<T>(column: Column<T>, offsets: Map<string, number>): CSSProperties {
	const pinned = column.getIsPinned();
	if (!pinned) return {};
	const px = offsets.get(column.id) ?? 0;
	return {
		position: "sticky",
		left: pinned === "left" ? `${px}px` : undefined,
		right: pinned === "right" ? `${px}px` : undefined,
		zIndex: 1,
	};
}

/**
 * CSS class for the shadow on the last left-pinned or first right-pinned column.
 * Uses an `after` pseudo-element so it isn't clipped by overflow on the table container.
 */
export const PIN_SHADOW_LEFT =
	"after:pointer-events-none after:absolute after:top-0 after:-right-6 after:h-full after:w-6 after:shadow-[inset_6px_0_6px_-6px_rgba(0,0,0,0.15)] dark:after:shadow-[inset_6px_0_6px_-6px_rgba(0,0,0,0.5)]";
export const PIN_SHADOW_RIGHT =
	"before:pointer-events-none before:absolute before:top-0 before:-left-6 before:h-full before:w-6 before:shadow-[inset_-6px_0_6px_-6px_rgba(0,0,0,0.15)] dark:before:shadow-[inset_-6px_0_6px_-6px_rgba(0,0,0,0.5)]";