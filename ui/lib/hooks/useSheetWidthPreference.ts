import { useCallback, useEffect, useState } from "react";

/** Narrowest a right-side sheet can be dragged to, in px. */
export const SHEET_MIN_WIDTH = 360;

/** Space kept free on the left of a resized sheet, in px, so the page behind stays visible. */
export const SHEET_VIEWPORT_GUTTER = 16;

/** Gap between a right-side sheet and the right edge of the viewport (the `right-2` class). */
export const SHEET_RIGHT_INSET = 8;

/** How far one arrow-key press moves the resize handle, in px. */
export const SHEET_KEYBOARD_STEP = 16;

const STORAGE_PREFIX = "bifrost.sheet.width.";

/** localStorage key holding the width of the sheet identified by `id`. */
export function sheetWidthStorageKey(id: string): string {
	return `${STORAGE_PREFIX}${id}`;
}

/** Widest a sheet can be at the given viewport width. Never below the minimum. */
export function maxSheetWidth(viewportWidth: number): number {
	return Math.max(SHEET_MIN_WIDTH, Math.floor(viewportWidth - SHEET_VIEWPORT_GUTTER));
}

/** Rounds `width` and clamps it between the minimum and the viewport-relative maximum. */
export function clampSheetWidth(width: number, viewportWidth: number): number {
	return Math.min(Math.max(Math.round(width), SHEET_MIN_WIDTH), maxSheetWidth(viewportWidth));
}

/**
 * Width the sheet is actually rendered at, which is what the resize handle sits on.
 * A saved width can be wider than the current viewport allows (it was saved on a wider
 * screen, or the window shrank), and the CSS max-width caps the sheet in that case,
 * so the override is capped the same way here. Without an override, the measured width is used.
 */
export function effectiveSheetWidth(override: number | null, measured: number | null, viewportWidth: number): number | null {
	if (override === null) return measured;
	return Math.min(override, maxSheetWidth(viewportWidth));
}

/**
 * Width a right-anchored sheet gets when its left edge is dragged to `pointerX`.
 * The sheet's right edge stays fixed at `SHEET_RIGHT_INSET` from the viewport edge.
 */
export function sheetWidthFromPointer(pointerX: number, viewportWidth: number): number {
	return clampSheetWidth(viewportWidth - SHEET_RIGHT_INSET - pointerX, viewportWidth);
}

/**
 * Width change for a key press on the resize handle. The handle sits on the sheet's
 * left edge, so ArrowLeft widens the sheet and ArrowRight narrows it. Returns null
 * for keys the handle does not use.
 */
export function sheetWidthKeyboardDelta(key: string): number | null {
	if (key === "ArrowLeft") return SHEET_KEYBOARD_STEP;
	if (key === "ArrowRight") return -SHEET_KEYBOARD_STEP;
	return null;
}

/**
 * Reads a stored width. Anything that is not a finite number at or above the minimum
 * is ignored, so a hand-edited or corrupt entry falls back to the sheet's default width.
 * The upper bound is applied at drag time and by the CSS max-width, not here, because
 * the viewport can change between visits.
 */
export function parseStoredSheetWidth(raw: string | null): number | null {
	if (!raw) return null;
	const parsed = Number(raw);
	if (!Number.isFinite(parsed) || parsed < SHEET_MIN_WIDTH) return null;
	return Math.round(parsed);
}

function readStoredWidth(storageKey: string | undefined): number | null {
	if (!storageKey) return null;
	try {
		return parseStoredSheetWidth(localStorage.getItem(storageKey));
	} catch {
		return null;
	}
}

/**
 * Width override for a resizable sheet, persisted in localStorage under `storageKey`.
 *
 * Returns `[width, setWidth, resetWidth]`. `width` is null until the user resizes the
 * sheet, and while it is null the sheet keeps its own CSS width. `setWidth(px)` updates
 * the width without saving it, which is what a drag does on every pointer move;
 * `setWidth(px, true)` also saves it. `resetWidth()` drops the override and the stored
 * entry. With no `storageKey` the override still works but lasts only while the
 * component is mounted.
 */
export function useSheetWidthPreference(
	storageKey: string | undefined,
): [number | null, (width: number, persist?: boolean) => void, () => void] {
	const [width, setWidthState] = useState<number | null>(() => readStoredWidth(storageKey));

	useEffect(() => {
		setWidthState(readStoredWidth(storageKey));
	}, [storageKey]);

	const setWidth = useCallback(
		(value: number, persist = false) => {
			setWidthState(value);
			if (!persist || !storageKey) return;
			try {
				localStorage.setItem(storageKey, String(value));
			} catch {
				// localStorage unavailable: the width applies for this session only.
			}
		},
		[storageKey],
	);

	const resetWidth = useCallback(() => {
		setWidthState(null);
		if (!storageKey) return;
		try {
			localStorage.removeItem(storageKey);
		} catch {
			// localStorage unavailable: nothing was stored.
		}
	}, [storageKey]);

	return [width, setWidth, resetWidth];
}