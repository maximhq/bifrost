import { validateRegexPattern } from "@/lib/utils/celConverterRouting";

/**
 * Model access is expressed as two kinds of list that live side by side:
 *
 *  - an exact list (`allowed_models` / `blacklisted_models` / `models`) holding
 *    model names, or "*" alone to mean every model;
 *  - a pattern list (`allowed_models_patterns` / `blacklisted_models_patterns` /
 *    `models_patterns`) holding raw RE2 patterns, matched case-insensitively as
 *    a full match against the model name and "provider/model".
 *
 * Exact entries are never interpreted as patterns and patterns never appear in
 * the exact list. "*" is not a valid pattern.
 */
export const MODEL_WILDCARD = "*";

export type ModelAccessMode = "allow" | "block";

export function isWildcardEntry(entry: string): boolean {
	return entry === MODEL_WILDCARD;
}

export function isWildcardList(list: readonly string[] | undefined | null): boolean {
	return !!list && list.includes(MODEL_WILDCARD);
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
	return validateRegexPattern(trimmed);
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

/** Appends a trimmed pattern to the pattern list, ignoring an exact duplicate. */
export function addPattern(current: readonly string[], pattern: string): string[] {
	const trimmed = pattern.trim();
	if (current.includes(trimmed)) return [...current];
	return [...current, trimmed];
}

/** Short collapsed-header summary such as "All models", "Deny all", "3 models, 1 pattern". */
export function summarizeModelAccess(
	models: readonly string[] | undefined | null,
	patterns: readonly string[] | undefined | null,
	mode: ModelAccessMode,
): string {
	const names = (models ?? []).filter((e) => !isWildcardEntry(e));
	const wildcard = isWildcardList(models);
	const patternCount = (patterns ?? []).length;
	if (wildcard) return mode === "allow" ? "All models" : "All models blocked";
	if (names.length === 0 && patternCount === 0) return mode === "allow" ? "Deny all" : "No blocked models";
	const parts: string[] = [];
	if (names.length > 0) parts.push(`${names.length} model${names.length > 1 ? "s" : ""}`);
	if (patternCount > 0) parts.push(`${patternCount} pattern${patternCount > 1 ? "s" : ""}`);
	return parts.join(", ");
}

/** Placeholder for the picker control, mirroring the wording each surface used before. */
export function modelAccessPlaceholder(
	models: readonly string[] | undefined | null,
	mode: ModelAccessMode,
	patterns?: readonly string[] | null,
): string {
	const entries = models ?? [];
	if (entries.includes(MODEL_WILDCARD)) return mode === "allow" ? "All models allowed" : "All models blocked";
	if (entries.length === 0 && (patterns ?? []).length === 0) return mode === "allow" ? "No models (deny all)" : "No blocked models";
	return mode === "allow" ? "Add model…" : "Search models...";
}
