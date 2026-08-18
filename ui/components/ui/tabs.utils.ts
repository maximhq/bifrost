/**
 * Splits a tab strip into the triggers that fit on one row and the ones that
 * have to move into the trailing overflow dropdown.
 *
 * A horizontally scrolling strip hides tabs behind an affordance most people
 * never find - especially inside a sheet, where a trackpad's horizontal gesture
 * fights the sheet's own scroll. Collapsing the remainder into a dropdown keeps
 * every tab reachable with a normal click.
 */
export interface TabsOverflowInput {
	/** Measured width of each trigger, in DOM order. */
	itemWidths: number[];
	/** Width available to the row. */
	containerWidth: number;
	/** Width the "more" dropdown trigger occupies once it is shown. */
	moreButtonWidth: number;
	/** Index of the active trigger, or -1 when nothing is selected. */
	activeIndex: number;
}

export interface TabsOverflowResult {
	/** Indices rendered inline, in DOM order. */
	visible: number[];
	/** Indices rendered inside the dropdown, in DOM order. */
	overflow: number[];
}

export function partitionTabs({ itemWidths, containerWidth, moreButtonWidth, activeIndex }: TabsOverflowInput): TabsOverflowResult {
	const allVisible = () => ({ visible: itemWidths.map((_, index) => index), overflow: [] as number[] });

	if (itemWidths.length === 0) return { visible: [], overflow: [] };
	// Width is 0 until the first layout pass. Collapsing on that reading would
	// flash a dropdown that vanishes a frame later.
	if (containerWidth <= 0) return allVisible();

	const total = itemWidths.reduce((sum, width) => sum + width, 0);
	if (total <= containerWidth) return allVisible();

	// Overflowing, so the dropdown trigger is on screen and takes its own room.
	const available = containerWidth - moreButtonWidth;
	const hasActive = activeIndex >= 0 && activeIndex < itemWidths.length;

	// The selected tab is the one being read, so it claims its width before
	// anything else and never hides in the dropdown.
	const activeFits = hasActive && itemWidths[activeIndex] <= available;
	let used = activeFits ? itemWidths[activeIndex] : 0;

	const visible: number[] = [];
	for (let index = 0; index < itemWidths.length; index++) {
		if (hasActive && index === activeIndex) continue;
		// `continue`, not `break`: one unusually wide tab must not strand the row.
		// Stopping at it pushes every narrower tab behind it into the dropdown and
		// leaves the freed space visibly empty. Skipping it keeps the remaining
		// tabs inline, still in DOM order - only the tab that genuinely cannot fit
		// moves into the menu.
		if (used + itemWidths[index] > available) continue;
		visible.push(index);
		used += itemWidths[index];
	}

	if (activeFits) {
		visible.push(activeIndex);
		visible.sort((a, b) => a - b);
	} else if (hasActive && visible.length === 0) {
		// Nothing fits at all - show the selected tab rather than an empty row.
		visible.push(activeIndex);
	}

	const inVisible = new Set(visible);
	const overflow = itemWidths.map((_, index) => index).filter((index) => !inVisible.has(index));
	return { visible, overflow };
}