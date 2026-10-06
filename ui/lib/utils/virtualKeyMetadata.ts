// Virtual key metadata rules, mirrored from the server (framework/configstore/tables/virtualkeymetadata.go)
// so the sheet can flag a bad entry before the save round-trip. The server stays the authority.

export const MAX_VIRTUAL_KEY_METADATA_ENTRIES = 50;
export const MAX_VIRTUAL_KEY_METADATA_KEY_LENGTH = 256;
export const MAX_VIRTUAL_KEY_METADATA_VALUE_LENGTH = 512;

const METADATA_KEY_PATTERN = /^[a-zA-Z0-9._-]+$/;
// Log metadata keys the system writes itself; a virtual key's metadata is merged into the same map.
const RESERVED_METADATA_KEYS = new Set(["isAsyncRequest"]);
const RESERVED_METADATA_KEY_PREFIX = "bifrost_alb_";

/** Returns why a metadata key is not accepted, or undefined when it is. */
export function virtualKeyMetadataKeyError(key: string): string | undefined {
	if (key === "" || key.length > MAX_VIRTUAL_KEY_METADATA_KEY_LENGTH || !METADATA_KEY_PATTERN.test(key)) {
		return `Invalid key "${key}": use 1-${MAX_VIRTUAL_KEY_METADATA_KEY_LENGTH} letters, digits, ".", "_" or "-"`;
	}
	if (RESERVED_METADATA_KEYS.has(key) || key.startsWith(RESERVED_METADATA_KEY_PREFIX)) {
		return `Invalid key "${key}": reserved for system metadata`;
	}
	return undefined;
}

/** Reports whether key is accepted as a metadata filter key (metadata_<key>), the server's filter rule. */
export function isValidVirtualKeyMetadataFilterKey(key: string): boolean {
	return key !== "" && key.length <= MAX_VIRTUAL_KEY_METADATA_KEY_LENGTH && METADATA_KEY_PATTERN.test(key);
}

/** Returns the first problem with a metadata map, or undefined when the server would accept it. */
export function validateVirtualKeyMetadata(metadata: Record<string, string> | undefined): string | undefined {
	const entries = Object.entries(metadata ?? {});
	if (entries.length > MAX_VIRTUAL_KEY_METADATA_ENTRIES) {
		return `At most ${MAX_VIRTUAL_KEY_METADATA_ENTRIES} metadata entries are allowed`;
	}
	for (const [key, value] of entries) {
		const keyError = virtualKeyMetadataKeyError(key);
		if (keyError) return keyError;
		// The server counts characters (runes), not UTF-16 code units.
		if ([...value].length > MAX_VIRTUAL_KEY_METADATA_VALUE_LENGTH) {
			return `Value for "${key}" is longer than ${MAX_VIRTUAL_KEY_METADATA_VALUE_LENGTH} characters`;
		}
	}
	return undefined;
}

/**
 * The metadata part of an update request: the edited map when it differs from the one the form
 * opened with, otherwise nothing. The server replaces metadata as a whole and leaves it alone when
 * omitted, so a save that did not touch metadata must not resend the form's copy, which may be
 * older than what is stored now. Compared by content rather than react-hook-form's dirtyFields,
 * which tracks a record per entry and can stay set after an edit is undone.
 */
export function virtualKeyMetadataUpdate(
	initial: Record<string, string | undefined> | undefined,
	current: Record<string, string> | undefined,
): { metadata?: Record<string, string> } {
	const before = initial ?? {};
	const after = current ?? {};
	const keys = Object.keys(after);
	const unchanged =
		keys.length === Object.keys(before).length &&
		keys.every((key) => Object.prototype.hasOwnProperty.call(before, key) && before[key] === after[key]);
	return unchanged ? {} : { metadata: after };
}

/** Builds the metadata_<key>=<value> query params the virtual key list endpoint filters on. */
export function virtualKeyMetadataQueryParams(metadata: Record<string, string> | undefined): Record<string, string> {
	const params: Record<string, string> = {};
	for (const [key, value] of Object.entries(metadata ?? {})) {
		if (key !== "") params[`metadata_${key}`] = value;
	}
	return params;
}

/** The logs page's metadata_filters search param for one metadata entry. */
export function virtualKeyMetadataLogsFilter(key: string, value: string): string {
	return JSON.stringify({ [key]: value });
}