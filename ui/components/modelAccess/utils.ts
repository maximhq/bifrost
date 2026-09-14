import { validateRegexPattern } from "@/lib/utils/celConverterRouting";

/**
 * Model access is one list (`allowed_models` / `blacklisted_models` / `models`)
 * holding model names, "*" alone to mean every model, or entries prefixed with
 * `regex:` that the gateway matches as a case-insensitive full RE2 match. The
 * editor shows names and patterns on separate tabs; the wire stays one list.
 */
export const MODEL_WILDCARD = "*";
export const REGEX_ENTRY_PREFIX = "regex:";

export type ModelAccessMode = "allow" | "block";

const NAMED_GROUP = /\(\?P?<[A-Za-z0-9_]+>/g;
const NAMED_BACKREF = /(?:^|[^\\])(?:\\\\)*\\k</;

export function isWildcardEntry(entry: string): boolean {
	return entry === MODEL_WILDCARD;
}

export function isWildcardList(list: readonly string[] | undefined | null): boolean {
	return !!list && list.includes(MODEL_WILDCARD);
}

export function isRegexEntry(entry: string): boolean {
	return entry.startsWith(REGEX_ENTRY_PREFIX);
}

/** The raw pattern behind a `regex:` entry; other entries come back unchanged. */
export function regexEntryPattern(entry: string): string {
	return isRegexEntry(entry) ? entry.slice(REGEX_ENTRY_PREFIX.length) : entry;
}

export function toRegexEntry(pattern: string): string {
	return REGEX_ENTRY_PREFIX + pattern;
}

/** Splits one list into its exact entries (names and "*") and its raw patterns. */
export function splitModelAccess(list: readonly string[] | undefined | null): { models: string[]; patterns: string[] } {
	const models: string[] = [];
	const patterns: string[] = [];
	for (const entry of list ?? []) {
		if (isRegexEntry(entry)) patterns.push(regexEntryPattern(entry));
		else models.push(entry);
	}
	return { models, patterns };
}

/**
 * Validates a raw pattern the way the backend will: non-empty, not the
 * wildcard, RE2-compatible. Returns null when valid, an error message otherwise.
 */
export function validateModelRegex(pattern: string): string | null {
	const trimmed = pattern.trim();
	if (trimmed === "") {
		return "Pattern cannot be empty";
	}
	if (trimmed === MODEL_WILDCARD) {
		return 'Use the model list to allow all models; "*" is not a pattern';
	}
	if (NAMED_BACKREF.test(trimmed)) {
		return "RE2 incompatible: named backreferences (\\k<name>) are not supported";
	}
	// Go accepts (?P<name>...) and names that start with a digit, JavaScript neither; the name is irrelevant to the syntax check.
	return validateRegexPattern(trimmed.replace(NAMED_GROUP, "("));
}

/**
 * Shared wildcard-selection rule for the model pickers: selecting "*" collapses
 * to just ["*"]; adding anything else while "*" is present drops the "*"; any
 * other selection passes through unchanged.
 */
export function resolveWildcardSelection(current: readonly string[], next: readonly string[]): string[] {
	const hadStar = current.includes(MODEL_WILDCARD);
	const hasStar = next.includes(MODEL_WILDCARD);
	if (!hadStar && hasStar) return [MODEL_WILDCARD];
	if (hadStar && hasStar && next.length > 1) return next.filter((v) => v !== MODEL_WILDCARD);
	return [...next];
}

/**
 * Applies a new exact selection to the list while keeping its patterns. The
 * wildcard stands alone on the wire, so picking it also clears the patterns.
 */
export function replaceModels(list: readonly string[], nextModels: readonly string[]): string[] {
	const { models, patterns } = splitModelAccess(list);
	const resolved = resolveWildcardSelection(models, nextModels);
	if (resolved.includes(MODEL_WILDCARD)) return [MODEL_WILDCARD];
	return [...resolved, ...patterns.map(toRegexEntry)];
}

/** Appends a trimmed pattern as a `regex:` entry, ignoring a duplicate and dropping a lone "*". */
export function addPattern(list: readonly string[], pattern: string): string[] {
	const entry = toRegexEntry(pattern.trim());
	if (list.includes(entry)) return [...list];
	return [...list.filter((e) => !isWildcardEntry(e)), entry];
}

export function removePattern(list: readonly string[], pattern: string): string[] {
	const entry = toRegexEntry(pattern);
	return list.filter((e) => e !== entry);
}

/** Short collapsed-header summary such as "All models", "Deny all", "3 models, 1 pattern". */
export function summarizeModelAccess(list: readonly string[] | undefined | null, mode: ModelAccessMode): string {
	const { models, patterns } = splitModelAccess(list);
	const names = models.filter((e) => !isWildcardEntry(e));
	if (isWildcardList(models)) return mode === "allow" ? "All models" : "All models blocked";
	if (names.length === 0 && patterns.length === 0) return mode === "allow" ? "Deny all" : "No blocked models";
	const parts: string[] = [];
	if (names.length > 0) parts.push(`${names.length} model${names.length > 1 ? "s" : ""}`);
	if (patterns.length > 0) parts.push(`${patterns.length} pattern${patterns.length > 1 ? "s" : ""}`);
	return parts.join(", ");
}

/** Placeholder for the picker control, mirroring the wording each surface used before. */
export function modelAccessPlaceholder(list: readonly string[] | undefined | null, mode: ModelAccessMode): string {
	const { models, patterns } = splitModelAccess(list);
	if (models.includes(MODEL_WILDCARD)) return mode === "allow" ? "All models allowed" : "All models blocked";
	if (models.length === 0 && patterns.length === 0) return mode === "allow" ? "No models (deny all)" : "No blocked models";
	return mode === "allow" ? "Add model…" : "Search models...";
}