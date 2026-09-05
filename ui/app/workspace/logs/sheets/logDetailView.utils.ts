import { isLogLevel, type LogLevel } from "@/lib/utils/logLevel";

/**
 * Which message the Raw JSON tab shows when a log row carries no raw payload.
 *
 * - `loading`         — the provider setting is still being fetched; committing to a
 *                       message now would flash the wrong one.
 * - `storage-disabled` — the provider is explicitly configured not to persist raw
 *                       request/response payloads, so we can explain *why* it is empty.
 * - `unknown`         — we cannot attribute the empty tab to the provider setting
 *                       (no provider-read permission, the fetch failed, the provider is
 *                       not in the list, or storage is on and the row simply failed
 *                       before reaching the provider). Falls back to neutral copy.
 */
export type RawJsonNoticeState = "loading" | "storage-disabled" | "unknown";

export function resolveRawJsonNoticeState({
	hasProvidersAccess,
	isProvidersLoading,
	isProvidersError,
	providers,
	provider,
}: {
	hasProvidersAccess: boolean;
	isProvidersLoading: boolean;
	isProvidersError: boolean;
	providers: { name: string; store_raw_request_response?: boolean }[] | undefined;
	provider: string;
}): RawJsonNoticeState {
	// The query is skipped without provider-read permission, and a failed fetch never
	// delivers data - in both cases waiting would strand the tab on a spinner forever.
	if (!hasProvidersAccess || isProvidersError) return "unknown";
	// Otherwise an absent `providers` means the request is still in flight: hold the
	// message back rather than flashing "No raw JSON available." before the setting is known.
	if (isProvidersLoading || !providers) return "loading";
	const match = providers.find((p) => p.name === provider);
	return match && match.store_raw_request_response === false ? "storage-disabled" : "unknown";
}

export interface RoutingDecisionLine {
	timestamp: number | null;
	engine: string | null;
	level: LogLevel | null;
	message: string;
}

// The logging plugin writes each routing entry as `[unix-ms] [engine] [level] - message`.
// Rows stored before the level was recorded read `[unix-ms] [engine] - message`, so the
// level group is optional and those lines parse with a null level.
const ROUTING_LINE_PATTERN = /^\[(\d+)\]\s+\[([^\]]+)\](?:\s+\[([^\]]+)\])?\s+-\s+(.*)$/;

export function parseRoutingDecisionLine(line: string): RoutingDecisionLine {
	const match = line.match(ROUTING_LINE_PATTERN);
	if (!match) return { timestamp: null, engine: null, level: null, message: line };
	const level = match[3]?.toLowerCase();
	return { timestamp: Number(match[1]), engine: match[2], level: isLogLevel(level) ? level : null, message: match[4] };
}