// Provider and model label rules (metadata and tags), mirrored from the server
// (framework/configstore/tables/metadata.go) so forms can flag a bad entry before the save
// round-trip. The server stays the authority.

export const MAX_METADATA_ENTRIES = 50;
export const MAX_METADATA_KEY_LENGTH = 256;
export const MAX_METADATA_VALUE_LENGTH = 512;
export const MAX_TAGS = 50;
export const MAX_TAG_LENGTH = 64;

const LABEL_PATTERN = /^[a-zA-Z0-9._-]+$/;

/** Returns why a metadata key is not accepted, or undefined when it is. */
export function metadataKeyError(key: string): string | undefined {
	if (key === "" || key.length > MAX_METADATA_KEY_LENGTH || !LABEL_PATTERN.test(key)) {
		return `Invalid key "${key}": use 1-${MAX_METADATA_KEY_LENGTH} letters, digits, ".", "_" or "-"`;
	}
	return undefined;
}

/** Returns the first problem with a metadata map, or undefined when the server would accept it. */
export function validateMetadata(metadata: Record<string, string> | undefined): string | undefined {
	const entries = Object.entries(metadata ?? {});
	if (entries.length > MAX_METADATA_ENTRIES) {
		return `At most ${MAX_METADATA_ENTRIES} metadata entries are allowed`;
	}
	for (const [key, value] of entries) {
		const keyError = metadataKeyError(key);
		if (keyError) return keyError;
		// The server counts characters (runes), not UTF-16 code units.
		if ([...value].length > MAX_METADATA_VALUE_LENGTH) {
			return `Value for "${key}" is longer than ${MAX_METADATA_VALUE_LENGTH} characters`;
		}
	}
	return undefined;
}

/** Returns why a tag is not accepted (after trimming), or undefined when it is. */
export function tagError(tag: string): string | undefined {
	const trimmed = tag.trim();
	if (trimmed === "" || trimmed.length > MAX_TAG_LENGTH || !LABEL_PATTERN.test(trimmed)) {
		return `Invalid tag "${tag}": use 1-${MAX_TAG_LENGTH} letters, digits, ".", "_" or "-"`;
	}
	return undefined;
}

/**
 * Normalizes tags the way the server stores them: trimmed, de-duplicated and sorted.
 * Returns the first problem instead when a tag is invalid or there are too many.
 */
export function normalizeTags(tags: string[] | undefined): { tags: string[]; error?: string } {
	// The server caps the list as sent, duplicates included, before de-duplicating.
	if ((tags ?? []).length > MAX_TAGS) {
		return { tags: [], error: `At most ${MAX_TAGS} tags are allowed` };
	}
	const out = new Set<string>();
	for (const tag of tags ?? []) {
		const error = tagError(tag);
		if (error) return { tags: [], error };
		out.add(tag.trim());
	}
	// Plain code-point order, the same order Go's sort.Strings produces.
	return { tags: [...out].sort((a, b) => (a < b ? -1 : a > b ? 1 : 0)) };
}

/** Reports whether `have` carries every tag in `want` (an empty `want` matches everything). */
export function hasAllTags(have: string[] | undefined, want: string[]): boolean {
	return want.every((tag) => (have ?? []).includes(tag));
}

/** Collects the distinct tags across items, sorted, for building a tag filter. */
export function collectTags(items: { tags?: string[] }[] | undefined): string[] {
	const out = new Set<string>();
	for (const item of items ?? []) {
		for (const tag of item.tags ?? []) out.add(tag);
	}
	return [...out].sort((a, b) => (a < b ? -1 : a > b ? 1 : 0));
}

/** The tags=a,b query value the list endpoints filter on, or undefined for no filter. */
export function tagsQueryParam(tags: string[] | undefined): string | undefined {
	return tags && tags.length > 0 ? tags.join(",") : undefined;
}