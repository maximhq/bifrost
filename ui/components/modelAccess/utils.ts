import { validateRegexPattern } from "@/lib/utils/celConverterRouting";

/**
 * Marker that turns an allowed_models / blacklisted_models / models entry into
 * a regex. Mirrors schemas.ModelRegexPrefix on the backend: the entry
 * "regex:<pattern>" is compiled as RE2 and matched case-insensitively as a full
 * match against the model name (and "provider/model"). Any other entry is an
 * exact, case-insensitive model name. "*" alone means all models.
 */
export const MODEL_REGEX_PREFIX = "regex:";

export const MODEL_WILDCARD = "*";

export type ModelAccessMode = "allow" | "block";

export function isRegexEntry(entry: string): boolean {
	return entry.startsWith(MODEL_REGEX_PREFIX);
}

export function isWildcardEntry(entry: string): boolean {
	return entry === MODEL_WILDCARD;
}

/** Wraps a raw pattern into a list entry. */
export function toRegexEntry(pattern: string): string {
	return MODEL_REGEX_PREFIX + pattern;
}

/** Returns the raw pattern of a regex entry; non-regex entries come back unchanged. */
export function stripRegexPrefix(entry: string): string {
	return isRegexEntry(entry) ? entry.slice(MODEL_REGEX_PREFIX.length) : entry;
}

export function isWildcardList(list: readonly string[] | undefined | null): boolean {
	return !!list && list.includes(MODEL_WILDCARD);
}

/**
 * Validates a raw pattern (without the prefix) the way the backend will:
 * RE2-compatible and non-empty. Returns null when valid, an error message
 * otherwise.
 */
export function validateModelRegex(pattern: string): string | null {
	if (pattern.trim() === "") {
		return "Pattern cannot be empty";
	}
	return validateRegexPattern(pattern);
}

/** Validates a full list entry: literal names always pass, regex entries must compile. */
export function validateModelEntry(entry: string): string | null {
	if (!isRegexEntry(entry)) return null;
	return validateModelRegex(stripRegexPrefix(entry));
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

/** Appends a regex entry, applying the same wildcard rule as adding a model. */
export function addRegexEntry(current: readonly string[], pattern: string): string[] {
	const entry = toRegexEntry(pattern.trim());
	if (current.includes(entry)) return [...current];
	return resolveWildcardSelection(current, [...current, entry]);
}

export interface SplitModelEntries {
	wildcard: boolean;
	models: string[];
	regexes: string[];
}

export function splitModelEntries(list: readonly string[] | undefined | null): SplitModelEntries {
	const entries = list ?? [];
	return {
		wildcard: entries.includes(MODEL_WILDCARD),
		models: entries.filter((e) => !isWildcardEntry(e) && !isRegexEntry(e)),
		regexes: entries.filter(isRegexEntry),
	};
}

/** Short collapsed-header summary such as "All models", "Deny all", "3 models, 1 pattern". */
export function summarizeModelList(list: readonly string[] | undefined | null, mode: ModelAccessMode): string {
	const { wildcard, models, regexes } = splitModelEntries(list);
	if (wildcard) return mode === "allow" ? "All models" : "All models blocked";
	if (models.length === 0 && regexes.length === 0) return mode === "allow" ? "Deny all" : "No blocked models";
	const parts: string[] = [];
	if (models.length > 0) parts.push(`${models.length} model${models.length > 1 ? "s" : ""}`);
	if (regexes.length > 0) parts.push(`${regexes.length} pattern${regexes.length > 1 ? "s" : ""}`);
	return parts.join(", ");
}

/** Placeholder for the picker control, mirroring the wording each surface used before. */
export function modelAccessPlaceholder(list: readonly string[] | undefined | null, mode: ModelAccessMode): string {
	const entries = list ?? [];
	if (entries.includes(MODEL_WILDCARD)) return mode === "allow" ? "All models allowed" : "All models blocked";
	if (entries.length === 0) return mode === "allow" ? "No models (deny all)" : "No blocked models";
	return mode === "allow" ? "Add model…" : "Search models...";
}