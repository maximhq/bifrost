/**
 * The minimum an element has to look like for the check below.
 *
 * Structural rather than `Element` so the rule can be tested: this repo has no
 * DOM environment for vitest, and a keyboard rule that cannot be asserted is
 * one that quietly stops holding. A real `Element` satisfies this shape.
 */
export interface KeyEventTarget {
	tagName?: string;
	getAttribute?(name: string): string | null;
	parentElement?: KeyEventTarget | null;
}

const INTERACTIVE_TAGS = new Set(["BUTTON", "SELECT", "A"]);

/**
 * Says whether a keydown came from something that handles its own keys.
 *
 * The question card listens on the document, so it sees every keystroke in the
 * panel. Text inputs were already excluded, but buttons and links were not -
 * with Skip focused, Enter reached the card's handler and picked the
 * highlighted option instead of pressing the button under the cursor.
 *
 * It walks up rather than checking the target alone, because focus usually sits
 * on a child of the control: the span inside a button is what the event names.
 */
export function isInteractiveTarget(target: KeyEventTarget | null | undefined): boolean {
	for (let node = target; node; node = node.parentElement ?? null) {
		const tag = node.tagName?.toUpperCase();
		if (tag === "A") {
			// A bare anchor with no href is not focusable and takes no keys.
			if (node.getAttribute?.("href") != null) return true;
			continue;
		}
		if (tag && INTERACTIVE_TAGS.has(tag)) return true;
		if (node.getAttribute?.("role") === "button") return true;
	}
	return false;
}