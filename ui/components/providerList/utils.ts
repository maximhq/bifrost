// How many chips of a wrapped row fit before the "+N" chip has to take over.

interface FitCountArgs {
	/** Measured width of each chip, in order. */
	widths: number[];
	/** Width of the box the chips wrap inside. */
	containerWidth: number;
	/** Rows of chips allowed before the overflow chip. */
	lines: number;
	/** Measured width of the overflow chip at its widest. */
	overflowWidth: number;
	/** Horizontal gap between chips. */
	gap: number;
}

// Packs chips greedily into `lines` rows, then walks back off the last row until the overflow chip
// fits beside them. Returns how many chips to render; the rest go behind the "+N".
export function fitCount({ widths, containerWidth, lines, overflowWidth, gap }: FitCountArgs): number {
	if (widths.length === 0) return 0;
	if (containerWidth <= 0) return widths.length;

	// Row each chip lands on, and the row width up to and including it.
	const rowOf: number[] = [];
	const widthUpTo: number[] = [];
	let row = 0;
	let used = 0;

	for (let i = 0; i < widths.length; i++) {
		const next = used === 0 ? widths[i] : used + gap + widths[i];
		if (used > 0 && next > containerWidth) {
			row++;
			used = widths[i];
		} else {
			used = next;
		}
		rowOf[i] = row;
		widthUpTo[i] = used;
	}

	if (row < lines) return widths.length;

	// widthUpTo[i] only depends on the chips before i, so it stays valid as we walk back.
	let visible = rowOf.filter((r) => r < lines).length;
	while (visible > 1 && widthUpTo[visible - 1] + gap + overflowWidth > containerWidth) {
		visible--;
	}
	return Math.max(1, visible);
}