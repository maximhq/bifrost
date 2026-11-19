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
/** Whether the card's option keys (arrows, Enter, letters) may pick an answer. A confirmation card runs a write, so it takes a click. */
export function acceptsOptionShortcuts(question: { kind?: string }): boolean {
	return question.kind !== "confirm";
}
/** Turns an operation id into words: "createMCPClient" reads "Create MCP client". */
export function humanizeToolName(name: string): string {
	const words = name
		.replace(/[_-]+/g, " ")
		.replace(/([a-z0-9])([A-Z])/g, "$1 $2")
		.replace(/([A-Z]+)([A-Z][a-z])/g, "$1 $2")
		.trim()
		.split(/\s+/)
		.filter(Boolean)
		.map((word, index) => {
			if (word.length > 1 && word === word.toUpperCase()) return word;
			const lower = word.toLowerCase();
			return index === 0 ? lower.charAt(0).toUpperCase() + lower.slice(1) : lower;
		});
	return words.join(" ") || name;
}

export interface ConfirmArgumentRow {
	key: string;
	value: string;
	/** An object or array, shown as compact JSON. */
	structured: boolean;
	empty: boolean;
}

/** One row per top-level argument, or null when the arguments are not a JSON object and must show raw. */
export function confirmArgumentRows(argumentsJSON: string): ConfirmArgumentRow[] | null {
	let parsed: unknown;
	try {
		parsed = JSON.parse(argumentsJSON);
	} catch {
		return null;
	}
	if (parsed === null || typeof parsed !== "object" || Array.isArray(parsed)) return null;
	return Object.entries(parsed as Record<string, unknown>).map(([key, value]) => {
		if (value === null || value === undefined) return { key, value: "null", structured: false, empty: true };
		if (typeof value === "object") return { key, value: JSON.stringify(value), structured: true, empty: false };
		const text = String(value);
		return { key, value: text === "" ? '""' : text, structured: false, empty: text === "" };
	});
}

const questionKeys = new WeakMap<object, number>();
let nextQuestionKey = 0;

/**
 * A React key for a question card, one per question object. Questions carry no id, and two confirmations can
 * ask for the same tool and arguments, so equal content must still get a fresh card with fresh state.
 */
export function questionCardKey(question: object): number {
	let key = questionKeys.get(question);
	if (key === undefined) {
		key = ++nextQuestionKey;
		questionKeys.set(question, key);
	}
	return key;
}