import type { RedactionMapping } from "@/lib/types/logs";

// hasRedactionMappingEntries reports whether a detail response contains anything the UI can reveal.
export function hasRedactionMappingEntries(mapping?: RedactionMapping): boolean {
	return Boolean(mapping && (Object.keys(mapping.input ?? {}).length > 0 || Object.keys(mapping.output ?? {}).length > 0));
}

// applyRedactionMapping replaces reversible placeholders in display text without mutating source data.
export function applyRedactionMapping(text: string | undefined, mapping?: Record<string, string>): string {
	if (!text || !mapping) return text || "";
	return text.replace(/\[([^\]]+)\]/g, (placeholder, key: string) =>
		Object.prototype.hasOwnProperty.call(mapping, key) ? mapping[key] : placeholder,
	);
}

// mergeRedactionMappings combines phase maps for fields, such as errors, that can contain input and output content.
export function mergeRedactionMappings(mapping?: RedactionMapping): Record<string, string> | undefined {
	if (!mapping) return undefined;
	const merged = { ...mapping.input };
	for (const [key, value] of Object.entries(mapping.output ?? {})) {
		if (Object.prototype.hasOwnProperty.call(merged, key) && merged[key] !== value) {
			delete merged[key];
			continue;
		}
		merged[key] = value;
	}
	return Object.keys(merged).length > 0 ? merged : undefined;
}

// applyRedactionMappingToValue recursively reveals JSON-like display values while preserving the fetched object.
export function applyRedactionMappingToValue<T>(value: T, mapping?: Record<string, string>): T {
	if (!mapping || value == null) return value;
	if (typeof value === "string") return applyRedactionMapping(value, mapping) as T;
	if (Array.isArray(value)) return value.map((item) => applyRedactionMappingToValue(item, mapping)) as T;
	if (typeof value === "object") {
		return Object.fromEntries(
			Object.entries(value).map(([key, item]) => [applyRedactionMapping(key, mapping), applyRedactionMappingToValue(item, mapping)]),
		) as T;
	}
	return value;
}