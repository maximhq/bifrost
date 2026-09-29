/** Structural rather than `Element` so it can be tested without a DOM environment. */
export interface KeyEventTarget {
	tagName?: string;
	getAttribute?(name: string): string | null;
	parentElement?: KeyEventTarget | null;
}

const INTERACTIVE_TAGS = new Set(["BUTTON", "SELECT", "A"]);

/** Walks up because the event often names a child of the control, such as the span inside a button. */
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