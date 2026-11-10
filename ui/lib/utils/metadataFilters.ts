// Metadata filters on the logs pages map a metadata key to the values a row may hold for it: any
// listed value matches, and every key must match. Links written before multiple selection carry one
// string per key, so everything that reads the filter accepts that form too.

export type MetadataFilters = Record<string, string[]>;

/**
 * normalizeMetadataFilters turns a parsed metadata_filters object into key to values, accepting a
 * string or an array of strings per key. Empty values and keys with none left are dropped, and
 * undefined is returned when nothing remains.
 */
export function normalizeMetadataFilters(raw: unknown): MetadataFilters | undefined {
	if (!raw || typeof raw !== "object" || Array.isArray(raw)) return undefined;
	const out: MetadataFilters = {};
	for (const [key, value] of Object.entries(raw as Record<string, unknown>)) {
		const items = Array.isArray(value) ? value : [value];
		const values = [...new Set(items.filter((item): item is string => typeof item === "string" && item !== ""))];
		if (key !== "" && values.length > 0) out[key] = values;
	}
	return Object.keys(out).length > 0 ? out : undefined;
}

/** parseMetadataFiltersParam reads the metadata_filters search param, or undefined when it is empty or malformed. */
export function parseMetadataFiltersParam(param: string | null | undefined): MetadataFilters | undefined {
	if (!param) return undefined;
	try {
		return normalizeMetadataFilters(JSON.parse(param));
	} catch {
		return undefined;
	}
}

/** toggleMetadataFilterValue adds value to key's selection, or removes it when already selected. */
export function toggleMetadataFilterValue(filters: MetadataFilters | undefined, key: string, value: string): MetadataFilters | undefined {
	const normalized = normalizeMetadataFilters(filters) ?? {};
	const current = normalized[key] ?? [];
	const next = current.includes(value) ? current.filter((v) => v !== value) : [...current, value];
	return normalizeMetadataFilters({ ...normalized, [key]: next });
}

/**
 * metadataFilterQueryValue is the metadata_<key> query param for a key's values. A single value is
 * sent as itself, as before; several, or one that would read as a JSON array, go as a JSON array.
 */
export function metadataFilterQueryValue(values: string[]): string {
	if (values.length === 1 && !values[0].trimStart().startsWith("[")) return values[0];
	return JSON.stringify(values);
}

/** metadataFilterValueCount is how many values are selected across every key, for the active filter badge. */
export function metadataFilterValueCount(filters: MetadataFilters | undefined): number {
	return Object.values(normalizeMetadataFilters(filters) ?? {}).reduce((count, values) => count + values.length, 0);
}

/** Keep selected keys and values removable even when the current facet scope has no matches. */
export function mergeMetadataOptions(available: Record<string, string[]>, selected: MetadataFilters | undefined): Record<string, string[]> {
	const merged = { ...available };
	for (const [key, values] of Object.entries(normalizeMetadataFilters(selected) ?? {})) {
		merged[key] = [...new Set([...(Object.hasOwn(available, key) ? available[key] : []), ...values])];
	}
	return merged;
}