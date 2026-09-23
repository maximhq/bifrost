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

// Pulls a human-readable failure reason out of a provider error body. A provider whose
// error shape does not match what its parser expects lands with an empty
// error_details.error.message (Bedrock's invoke path answering AWS's {"message":...} through
// the Anthropic parser was one such case), and the body is then the only place the reason
// survives. Handles both the flat {"message":...} shape and the nested {"error":{"message":...}}
// envelope, and accepts either a JSON string or an already-parsed object.
export function extractProviderErrorMessage(raw: unknown): string | null {
	if (raw == null) return null;

	let value: unknown = raw;
	if (typeof value === "string") {
		const trimmed = value.trim();
		if (!trimmed) return null;
		try {
			value = JSON.parse(trimmed);
		} catch {
			return trimmed;
		}
	}
	if (typeof value === "string") return value.trim() || null;
	if (value == null || typeof value !== "object") return null;

	const obj = value as Record<string, unknown>;
	const nested = obj.error;
	if (nested && typeof nested === "object") {
		const nestedMessage = (nested as Record<string, unknown>).message;
		if (typeof nestedMessage === "string" && nestedMessage.trim()) return nestedMessage.trim();
	}
	if (typeof nested === "string" && nested.trim()) return nested.trim();

	for (const key of ["message", "Message", "error_message", "detail"]) {
		const candidate = obj[key];
		if (typeof candidate === "string" && candidate.trim()) return candidate.trim();
	}
	return null;
}