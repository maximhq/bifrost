import i18n from "@/lib/i18n";
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
// Go inline flag groups such as (?i) or (?s-i:...). The escape alternative comes first so "\(?i\)" is left alone.
const GO_FLAG_GROUP = /\\[\s\S]|\(\?(?:[imsU]+(?:-[imsU]+)?|-[imsU]+)([:)])/g;

/** Replaces Go's \Q...\E quoted text with the same text escaped for JavaScript; an open quote runs to the end. */
function unquoteGoLiterals(pattern: string): string {
	let out = "";
	for (let i = 0; i < pattern.length; i++) {
		if (pattern[i] !== "\\" || i + 1 >= pattern.length) {
			out += pattern[i];
			continue;
		}
		if (pattern[i + 1] !== "Q") {
			out += pattern[i] + pattern[i + 1];
			i++;
			continue;
		}
		const end = pattern.indexOf("\\E", i + 2);
		const stop = end < 0 ? pattern.length : end;
		out += pattern.slice(i + 2, stop).replace(/[\\^$.*+?()[\]{}|]/g, "\\$&");
		i = stop + 1;
	}
	return out;
}

/** Index of the "]" that closes the character class opening at start, by Go's rules; the pattern length when it never closes. */
function classEnd(src: string, start: number): number {
	let i = start + 1;
	if (src[i] === "^") i++;
	// A leading "]" is a literal member of the class.
	if (src[i] === "]") i++;
	for (; i < src.length; i++) {
		if (src[i] === "\\") {
			i++;
			continue;
		}
		if (src[i] === "[" && src[i + 1] === ":") {
			const close = src.indexOf(":]", i + 2);
			if (close >= 0) {
				i = close + 1;
				continue;
			}
		}
		if (src[i] === "]") return i;
	}
	return src.length;
}

/** Names of the capture groups an RE2 pattern defines, skipping escapes, quoted text and character classes. */
function namedGroups(pattern: string): Set<string> {
	const names = new Set<string>();
	const src = unquoteGoLiterals(pattern);
	for (let i = 0; i < src.length; i++) {
		if (src[i] === "\\") {
			i++;
			continue;
		}
		if (src[i] === "[") {
			i = classEnd(src, i);
			continue;
		}
		if (src[i] !== "(") continue;
		const m = /^\(\?P?<([A-Za-z0-9_]+)>/.exec(src.slice(i));
		if (m) names.add(m[1]);
	}
	return names;
}

/** Rewrites Go-only syntax into what JavaScript parses, for syntax checks and group counting only. */
function toJsSyntax(pattern: string): string {
	return unquoteGoLiterals(pattern)
		.replace(NAMED_GROUP, "(")
		.replace(GO_FLAG_GROUP, (match: string, end?: string) => (end === ":" ? "(?:" : end === ")" ? "" : match));
}
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
		return i18n.t("modelAccess.patternEmpty");
	}
	if (trimmed === MODEL_WILDCARD) {
		return i18n.t("modelAccess.patternWildcard");
	}
	if (NAMED_BACKREF.test(unquoteGoLiterals(trimmed))) {
		return i18n.t("modelAccess.patternNamedBackref");
	}
	// Go accepts (?P<name>...), names that start with a digit, inline flags and \Q...\E, JavaScript none of them; they are irrelevant to the syntax check.
	return validateRegexPattern(toJsSyntax(trimmed));
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
	if (isWildcardList(models))
		return mode === "allow" ? i18n.t("modelAccess.allModelsSentence") : i18n.t("modelAccess.allModelsBlockedSentence");
	if (names.length === 0 && patterns.length === 0)
		return mode === "allow" ? i18n.t("modelAccess.denyAll") : i18n.t("modelAccess.noBlockedModels");
	const parts: string[] = [];
	if (names.length > 0) parts.push(i18n.t("modelAccess.modelCount", { count: names.length }));
	if (patterns.length > 0) parts.push(i18n.t("modelAccess.patternCount", { count: patterns.length }));
	return parts.join(", ");
}

/**
 * Collapsed summary for a grant's allow and block sides together. The block list
 * wins over the allow list, so a blocked "*" denies everything, and an empty
 * allow list already denies everything whatever is blocked.
 */
export function summarizeGrantModelAccess(
	allowed: readonly string[] | undefined | null,
	blocked: readonly string[] | undefined | null,
): string {
	if (isWildcardList(blocked)) return summarizeModelAccess(blocked, "block");
	const allowSummary = summarizeModelAccess(allowed, "allow");
	if ((allowed ?? []).length === 0 || (blocked ?? []).length === 0) return allowSummary;
	return i18n.t("modelAccess.grantSummaryBlocked", { allowed: allowSummary, blocked: summarizeModelAccess(blocked, "block") });
}

/** Placeholder for the picker control, mirroring the wording each surface used before. */
export function modelAccessPlaceholder(list: readonly string[] | undefined | null, mode: ModelAccessMode): string {
	const { models, patterns } = splitModelAccess(list);
	if (models.includes(MODEL_WILDCARD))
		return mode === "allow" ? i18n.t("modelAccess.allModelsAllowed") : i18n.t("modelAccess.allModelsBlockedSentence");
	if (models.length === 0 && patterns.length === 0)
		return mode === "allow" ? i18n.t("modelAccess.noModelsDenyAll") : i18n.t("modelAccess.noBlockedModels");
	return mode === "allow" ? i18n.t("modelAccess.addModel") : i18n.t("modelAccess.searchModels");
}

// A bare $name runs over Unicode letters, digits and underscore, as in Go. Built from a string because the TS target rejects \p{...} in a literal.
const TEMPLATE_REF = new RegExp("\\$(?:\\$|\\{([^}]*)\\}|([\\p{L}\\p{Nd}_]+))", "gu");
// Go reads a reference as a group number only when it is plain digits with no leading zero, at most nine of them.
const GROUP_NUMBER = /^(?:0|[1-9]\d{0,8})$/;

/** Group names and numbers a Go regexp.Expand template references ($1, ${1}, $name, ${name}); $$ is a literal dollar. */
export function templateGroupRefs(template: string): string[] {
	const refs: string[] = [];
	for (const m of template.matchAll(TEMPLATE_REF)) {
		const ref = m[1] ?? m[2];
		if (ref !== undefined) refs.push(ref);
	}
	return refs;
}

/** Capture group count and names of an RE2 pattern, or null when it does not parse in JavaScript. */
function captureGroups(pattern: string): { count: number; names: Set<string> } | null {
	const names = namedGroups(pattern);
	try {
		const match = new RegExp(`(?:${toJsSyntax(pattern)})|`).exec("");
		return match ? { count: match.length - 1, names } : null;
	} catch {
		return null;
	}
}

/**
 * Validates a `regex:` deployment name and the model ID / canonical name templates
 * that read its capture groups. Returns null for a plain name or a valid pattern row.
 * It reports only what holds in any regex dialect: an empty pattern, a bare wildcard,
 * and a template that names a capture group the pattern lacks.
 */
export function validatePatternAlias(name: string, modelId: string, modelName?: string): string | null {
	if (!isRegexEntry(name)) return null;
	const pattern = regexEntryPattern(name);
	const trimmed = pattern.trim();
	if (trimmed === "") return i18n.t("modelAccess.patternEmpty");
	if (trimmed === MODEL_WILDCARD) return i18n.t("modelAccess.patternWildcard");
	// JavaScript cannot judge Go's regex syntax, so a pattern it fails to read is left to the gateway, which answers with Go's own error.
	const groups = captureGroups(pattern);
	if (!groups) return null;
	for (const template of [modelId, modelName ?? ""]) {
		for (const ref of templateGroupRefs(template)) {
			const known = GROUP_NUMBER.test(ref) ? Number(ref) <= groups.count : groups.names.has(ref);
			if (!known) return i18n.t("modelAccess.templateUnknownGroup", { group: `$${ref}` });
		}
	}
	return null;
}