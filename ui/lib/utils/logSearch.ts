import type { LogFilters } from "@/lib/types/logs";

/** Explicit prefix that forces an ID lookup for request IDs that aren't UUID-shaped. */
const ID_PREFIX = "id:";

const UUID_REGEX = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export type LogSearchTerms = Pick<LogFilters, "request_id" | "content_search">;

/**
 * Splits what was typed in the logs search box into the two mutually exclusive
 * search modes. A log row's primary key *is* its request ID, so a pasted ID is
 * an exact PK lookup rather than a content-summary text scan.
 *
 * Request IDs are usually UUIDs, but a caller can supply any string via the
 * `x-request-id` header — hence the explicit `id:` prefix as an escape hatch.
 */
export function parseLogSearchInput(raw: string): LogSearchTerms {
	const value = raw.trim();
	if (!value) return {};

	if (value.toLowerCase().startsWith(ID_PREFIX)) {
		const id = value.slice(ID_PREFIX.length).trim();
		// A bare "id:" is still being typed — search nothing rather than everything.
		return id ? { request_id: id } : {};
	}

	if (UUID_REGEX.test(value)) return { request_id: value };

	return { content_search: value };
}

/**
 * Inverse of {@link parseLogSearchInput}, used to rebuild the input's display
 * value from the URL-backed filters. The `id:` prefix is re-added only for
 * non-UUID ids, so a pasted UUID round-trips unchanged.
 */
export function formatLogSearchInput(filters: LogSearchTerms): string {
	if (filters.request_id) {
		return UUID_REGEX.test(filters.request_id) ? filters.request_id : `${ID_PREFIX}${filters.request_id}`;
	}
	return filters.content_search || "";
}

/** True when the current input targets a request ID — drives the "ID" badge. */
export function isLogIdSearch(raw: string): boolean {
	return !!parseLogSearchInput(raw).request_id;
}