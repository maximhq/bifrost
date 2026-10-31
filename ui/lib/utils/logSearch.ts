import type { LogFilters } from "@/lib/types/logs";

/** Explicit prefix that forces an ID lookup for request IDs that aren't UUID-shaped. */
const ID_PREFIX = "id:";

const UUID_REGEX = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export type LogSearchTerms = Pick<LogFilters, "request_id" | "content_search">;

/**
 * How the search box interprets what was typed. `auto` sniffs the input (the
 * default); the other two are the user pinning one mode from the dropdown so a
 * non-UUID id or a UUID-shaped content string isn't guessed wrong.
 */
export type LogSearchMode = "auto" | "request_id" | "content";

export const LOG_SEARCH_MODE_LABELS: Record<LogSearchMode, string> = {
	auto: "Auto",
	request_id: "Request ID",
	content: "Content",
};

/**
 * Splits what was typed in the logs search box into the two mutually exclusive
 * search modes. A log row's primary key *is* its request ID, so a pasted ID is
 * an exact PK lookup rather than a content-summary text scan.
 *
 * Request IDs are usually UUIDs, but a caller can supply any string via the
 * `x-request-id` header — hence the explicit `id:` prefix as an escape hatch,
 * and the explicit mode override.
 */
export function parseLogSearchInput(raw: string, mode: LogSearchMode = "auto"): LogSearchTerms {
	const value = raw.trim();
	if (!value) return {};

	// Pinned modes take the input verbatim: no sniffing, no prefix magic, so any
	// string at all can be looked up as an id or searched as content.
	if (mode === "request_id") return { request_id: value };
	if (mode === "content") return { content_search: value };

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
 * value from the URL-backed filters. In `auto` the `id:` prefix is re-added only
 * for non-UUID ids, so a pasted UUID round-trips unchanged; a pinned mode needs
 * no prefix because the mode itself carries the intent.
 */
export function formatLogSearchInput(filters: LogSearchTerms, mode: LogSearchMode = "auto"): string {
	if (mode === "request_id") return filters.request_id || "";
	if (mode === "content") return filters.content_search || "";
	if (filters.request_id) {
		return UUID_REGEX.test(filters.request_id) ? filters.request_id : `${ID_PREFIX}${filters.request_id}`;
	}
	return filters.content_search || "";
}

/** True when the current input targets a request ID — drives the "ID" badge. */
export function isLogIdSearch(raw: string, mode: LogSearchMode = "auto"): boolean {
	return !!parseLogSearchInput(raw, mode).request_id;
}