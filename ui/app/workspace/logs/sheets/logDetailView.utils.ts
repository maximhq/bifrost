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
// A Responses item that asks for a tool to run (function_call, custom_tool_call,
// web_search_call, ...), as opposed to the *_call_output that carries its result.
// Both render with the tool tone, but only the output is a result: labelling a call
// "Tool Result" put its arguments - often just `{}` - where a result was expected.
export function isResponsesToolCallItem(type: string | undefined): boolean {
	return !!type && type.endsWith("_call");
}

// Calls the caller runs itself. Their output is not in this response - the caller
// executes the tool and sends the result as input to its next request. Server-side
// calls (web_search_call, mcp_call, ...) are resolved within the same response.
// Computer use and local shell run on the caller's machine as well.
const CLIENT_TOOL_CALL_TYPES = new Set(["function_call", "custom_tool_call", "computer_call", "local_shell_call"]);

export function isClientToolCallItem(type: string | undefined): boolean {
	return !!type && CLIENT_TOOL_CALL_TYPES.has(type);
}

// Whether a call's arguments name nothing: empty, or a JSON object with no keys.
// Anything else - including arguments that are not valid JSON - is shown as sent.
export function hasNoToolArguments(args: unknown): boolean {
	if (typeof args !== "string") return false;
	const trimmed = args.trim();
	if (!trimmed) return true;
	try {
		const parsed: unknown = JSON.parse(trimmed);
		return parsed !== null && typeof parsed === "object" && !Array.isArray(parsed) && Object.keys(parsed).length === 0;
	} catch {
		return false;
	}
}

// A log timestamp as whole milliseconds plus the nanoseconds below them. Logs are
// stored to the microsecond, finer than a JavaScript date, and two requests in the
// same millisecond must still order. A timestamp that is not RFC3339 falls back to
// Date.parse; undefined when that fails too.
const RFC3339_PARTS = /^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})(?:\.(\d{1,9}))?(Z|[+-]\d{2}:\d{2})$/i;

function parseLogTimestamp(timestamp: string): { ms: number; subMsNanos: number } | undefined {
	const parts = RFC3339_PARTS.exec(timestamp);
	const fraction = (parts?.[2] ?? "").padEnd(9, "0");
	const ms = parts ? Date.parse(`${parts[1]}.${fraction.slice(0, 3)}${parts[3]}`) : Date.parse(timestamp);
	if (Number.isNaN(ms)) return undefined;
	return { ms, subMsNanos: parts ? Number(fraction.slice(3)) : 0 };
}

function isLaterTimestamp(a: { ms: number; subMsNanos: number }, b: { ms: number; subMsNanos: number }): boolean {
	return a.ms > b.ms || (a.ms === b.ms && a.subMsNanos > b.subMsNanos);
}

// Where the next-request lookup starts: one nanosecond - the finest a log timestamp
// carries - after `timestamp`. The next request cannot start before this response has
// returned, so it is always strictly later, and starting just after skips every row
// logged at the same instant - which otherwise filled a small page and hid the next
// request. Written in UTC with the full fraction, which the API parses.
export function nextSessionLookupStart(timestamp: string): string {
	const at = parseLogTimestamp(timestamp);
	if (!at) return timestamp;
	let { ms, subMsNanos } = at;
	subMsNanos += 1;
	if (subMsNanos >= 1_000_000) {
		ms += 1;
		subMsNanos -= 1_000_000;
	}
	const iso = new Date(ms).toISOString();
	const fraction = `${iso.slice(20, 23)}${String(subMsNanos).padStart(6, "0")}`.replace(/0+$/, "");
	return `${iso.slice(0, 19)}${fraction ? `.${fraction}` : ""}Z`;
}

// The first log after `current` in a timestamp-ascending page of its session. That is
// the request that carries the result of a tool call `current` ended on, since the
// caller cannot send it until this response is back. The page may include `current`
// itself, so it is skipped by id and by time. A timestamp that cannot be parsed is
// never later.
export function pickNextSessionLog<T extends { id: string; timestamp: string }>(
	logs: readonly T[],
	current: { id: string; timestamp: string },
): T | undefined {
	const after = parseLogTimestamp(current.timestamp);
	if (!after) return undefined;
	return logs.find((entry) => {
		if (entry.id === current.id) return false;
		const at = parseLogTimestamp(entry.timestamp);
		return !!at && isLaterTimestamp(at, after);
	});
}