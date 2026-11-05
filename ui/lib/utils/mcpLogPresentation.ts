import { addMilliseconds } from "date-fns";
import type { TFunction } from "i18next";

import type { BifrostError, MCPToolLogEntry } from "@/lib/types/logs";

// Policy approval confirms permission, never successful execution.
export function getMCPLogPresentation(log: MCPToolLogEntry, t?: TFunction<"observability">) {
	const policy = log.metadata?.inspection_phase === "pre_execution";
	const approved = policy && log.decision === "allow";
	const blocked = policy && log.decision === "deny";
	const retryLater = log.status === "input_required" && log.metadata?.retry_later === true;
	const label = approved
		? (t?.("mcpLogs.presentation.policyApproved") ?? "Policy approved")
		: blocked
			? (t?.("mcpLogs.presentation.policyBlocked") ?? "Policy blocked")
			: log.status === "unknown"
				? (t?.("mcpLogs.presentation.observed") ?? "Observed")
				: log.status === "success"
					? (t?.("mcpLogs.presentation.succeeded") ?? "Succeeded")
					: log.status === "error"
						? (t?.("mcpLogs.presentation.failed") ?? "Failed")
						: retryLater
							? (t?.("mcpLogs.presentation.retryLater") ?? "Retry later")
							: log.status === "input_required"
								? (t?.("mcpLogs.presentation.inputRequired") ?? "Needs input")
								: log.status === "cancelled"
									? (t?.("mcpLogs.presentation.cancelled") ?? "Cancelled")
									: (t?.("mcpLogs.presentation.processing") ?? "Processing");
	// An ordinary execution (succeeded, failed, cancelled, still running) has nothing to add to its
	// status label, so it carries no description and the sheet and tooltips show none.
	const description: string | undefined = approved
		? (t?.("mcpLogs.presentation.approvedDescription") ?? "Allowed before execution. No execution result is recorded in this entry.")
		: blocked
			? (t?.("mcpLogs.presentation.blockedDescription") ?? "Blocked before execution.")
			: log.status === "unknown"
				? (t?.("mcpLogs.presentation.observedDescription") ?? "Tool activity was captured, but its execution outcome is not known.")
				: retryLater
					? (t?.("mcpLogs.presentation.retryLaterDescription") ??
						"The MCP server asked the caller to retry later, without asking for input. The retry is a separate entry.")
					: log.status === "input_required"
						? (t?.("mcpLogs.presentation.inputRequiredDescription") ??
							"The MCP server asked the caller for input before completing. The retry that answers it is a separate entry.")
						: undefined;
	const rawDuration = log.metadata?.inspection_duration_ms;
	const duration = rawDuration == null || (typeof rawDuration === "string" && rawDuration.trim() === "") ? undefined : Number(rawDuration);
	const inspectionDuration = duration != null && Number.isFinite(duration) && duration >= 0 ? duration : undefined;
	const rawObserved = log.metadata?.observed_latency_ms;
	const observed = rawObserved == null || (typeof rawObserved === "string" && rawObserved.trim() === "") ? undefined : Number(rawObserved);
	const observedDuration =
		!policy && log.source === "native" && observed != null && Number.isFinite(observed) && observed >= 0 && observed <= 86400000
			? observed
			: undefined;
	const durationLabel = policy
		? (t?.("mcpLogs.policyCheck") ?? "Policy check")
		: log.latency == null && observedDuration != null
			? (t?.("mcpLogs.observedRoundTrip") ?? "Observed round trip")
			: (t?.("mcpLogs.executionTime") ?? "Execution time");
	return { policy, approved, label, description, inspectionDuration, observedDuration, durationLabel };
}

// The error card's message and Details text for a failed entry. The inner error goes under Details
// only when it says something the message does not; extra_fields is never shown.
export function getMCPLogError(errorDetails: BifrostError): { message?: string; details?: string } {
	const message = errorDetails.error?.message || undefined;
	const inner = errorDetails.error?.error;
	if (inner == null || inner === "") return message ? { message } : {};
	if (typeof inner !== "string") return { ...(message ? { message } : {}), details: JSON.stringify(inner, null, 2) };
	if (!message) return { message: inner };
	return inner.trim() === message.trim() ? { message } : { message, details: inner };
}

// Bifrost's own wrapping around a tool failure (core/mcp/toolmanager.go): the sheet already names
// the tool and says it failed, so the card shows only what the upstream server said.
const MCP_TOOL_ERROR_PREFIX = /^MCP tool call failed for [^:\n]+: /;
const MCP_TOOL_ERROR_SENTINEL = /: mcp tool call (failed|timed out)$/;
// Longer messages stay plain text, which bounds the JSON.parse work each render of the sheet does.
const MAX_JSON_SCAN_LENGTH = 100_000;

// The upstream error text for the error card, with the first JSON object or array inside it pulled
// out and pretty-printed. The copy button keeps the full stored message.
export function splitMCPErrorMessage(message: string): { text: string; json?: string; textAfter?: string } {
	const unwrapped = message.replace(MCP_TOOL_ERROR_PREFIX, "").replace(MCP_TOOL_ERROR_SENTINEL, "");
	const found = unwrapped.length <= MAX_JSON_SCAN_LENGTH ? findEmbeddedJSON(unwrapped) : undefined;
	if (!found) return { text: unwrapped };
	const textAfter = unwrapped
		.slice(found.end)
		.replace(/^[\s:]+/, "")
		.trimEnd();
	return {
		text: unwrapped.slice(0, found.start).trimEnd(),
		json: JSON.stringify(found.value, null, 2),
		...(textAfter ? { textAfter } : {}),
	};
}

// The first object or array in text that parses as JSON and holds something worth a block of its own.
// The text comes from the upstream MCP server, so its brackets are untrusted and the work has to stay
// linear in its length: one pass pairs every bracket with a stack, and only the outermost pairs, which
// never overlap, are handed to JSON.parse. Quotes count only inside brackets, where they delimit JSON
// strings, and a closing bracket of the wrong kind drops the brackets still open.
function findEmbeddedJSON(text: string): { start: number; end: number; value: unknown } | undefined {
	const open: number[] = [];
	const pairs: Array<[number, number]> = [];
	let inString = false;
	for (let i = 0; i < text.length; i++) {
		const ch = text[i];
		if (inString) {
			if (ch === "\\") i++;
			else if (ch === '"') inString = false;
			continue;
		}
		if (ch === "{" || ch === "[") {
			open.push(i);
		} else if (ch === "}" || ch === "]") {
			const start = open.pop();
			if (start === undefined) continue;
			if (text[start] !== (ch === "}" ? "{" : "[")) {
				open.length = 0;
				continue;
			}
			pairs.push([start, i]);
		} else if (ch === '"' && open.length > 0) {
			inString = true;
		}
	}
	// Pairs are recorded innermost first; ordered by where they open, a pair inside the last outermost
	// one is skipped.
	pairs.sort((a, b) => a[0] - b[0]);
	let coveredUntil = -1;
	for (const [start, close] of pairs) {
		if (start <= coveredUntil) continue;
		coveredUntil = close;
		try {
			const value: unknown = JSON.parse(text.slice(start, close + 1));
			if (isStructured(value)) return { start, end: close + 1, value };
		} catch {
			// Brackets that are not JSON, such as "[WARN]", or JSON nested too deeply to walk: keep scanning.
		}
	}
	return undefined;
}

// A non-empty object, or an array holding at least one, so "[1, 2]" or "{}" stay in the sentence.
function isStructured(value: unknown): boolean {
	if (Array.isArray(value)) return value.some((item) => isStructured(item));
	return value !== null && typeof value === "object" && Object.keys(value).length > 0;
}

export type MCPLogPillTone = "success" | "error" | "processing" | "approved" | "input" | "neutral";

// Maps the presentation label (policy outcome or execution status) onto a pill tone.
export function getMCPLogPillTone(log: MCPToolLogEntry, presentation: ReturnType<typeof getMCPLogPresentation>): MCPLogPillTone {
	if (presentation.approved) return "approved";
	// Only a recorded denial is an error. A pre-execution entry still awaiting a
	// decision keeps the tone of its execution status.
	if (presentation.policy && log.decision === "deny") return "error";
	if (log.status === "success") return "success";
	if (log.status === "error") return "error";
	if (log.status === "processing") return "processing";
	if (log.status === "input_required") return "input";
	return "neutral";
}

// Start and end boundaries of the entry, alongside the duration actually shown.
export function getMCPLogTimeline(log: MCPToolLogEntry, presentation: ReturnType<typeof getMCPLogPresentation>) {
	const timestamp = new Date(log.timestamp);
	// Derived from the duration actually displayed: a policy entry carries its
	// inspection time and usually no latency, so reading log.latency here would
	// leave its far boundary unset and render the timestamp as N/A.
	const durationMs = presentation.policy ? presentation.inspectionDuration : (log.latency ?? presentation.observedDuration);
	const observedOnly = !presentation.policy && log.latency == null && presentation.observedDuration != null;
	const startTimestamp = observedOnly
		? null
		: log.source === "native"
			? durationMs == null
				? null
				: addMilliseconds(timestamp, -durationMs)
			: timestamp;
	const endTimestamp = log.source === "native" ? timestamp : durationMs == null ? null : addMilliseconds(timestamp, durationMs);
	return { durationMs, startTimestamp, endTimestamp };
}

// Use only the arguments already returned by the API; never reveal redacted values.
export function getMCPArgumentPreview(log: MCPToolLogEntry): string | undefined {
	let args: unknown = log.arguments;
	if (typeof args === "string") {
		const text = args;
		try {
			args = JSON.parse(text);
		} catch {
			return text.trim().replace(/\s+/g, " ").slice(0, 160) || undefined;
		}
	}
	if (!args || typeof args !== "object" || Array.isArray(args)) return undefined;
	const values = args as Record<string, unknown>;
	for (const key of ["command", "cmd", "file_path", "path", "query", "code", "url"]) {
		const value = values[key];
		if (typeof value === "string" && value.trim()) return value.trim().replace(/\s+/g, " ").slice(0, 160);
	}
	return undefined;
}

// getMCPReadKind names the kind of read a row records, or undefined for a tool call.
export function getMCPReadKind(log: Pick<MCPToolLogEntry, "method">): "Prompt" | "Resource" | undefined {
	switch (log.method) {
		case "prompts/get":
			return "Prompt";
		case "resources/read":
			return "Resource";
	}
	return undefined;
}