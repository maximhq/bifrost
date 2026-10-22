import { addMilliseconds } from "date-fns";
import type { TFunction } from "i18next";

import type { MCPToolLogEntry } from "@/lib/types/logs";

// Policy approval confirms permission, never successful execution.
export function getMCPLogPresentation(log: MCPToolLogEntry, t?: TFunction) {
	const policy = log.metadata?.inspection_phase === "pre_execution";
	const approved = policy && log.decision === "allow";
	const blocked = policy && log.decision === "deny";
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
						: log.status === "cancelled"
							? (t?.("mcpLogs.presentation.cancelled") ?? "Cancelled")
							: (t?.("mcpLogs.presentation.processing") ?? "Processing");
	const description = approved
		? (t?.("mcpLogs.presentation.approvedDescription") ?? "Allowed before execution. No execution result is recorded in this entry.")
		: blocked
			? (t?.("mcpLogs.presentation.blockedDescription") ?? "Blocked before execution.")
			: log.status === "unknown"
				? (t?.("mcpLogs.presentation.observedDescription") ?? "Tool activity was captured, but its execution outcome is not known.")
				: (t?.("mcpLogs.presentation.executionDescription") ?? "Recorded tool execution outcome.");
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

export type MCPLogPillTone = "success" | "error" | "processing" | "approved" | "neutral";

// Maps the presentation label (policy outcome or execution status) onto a pill tone.
export function getMCPLogPillTone(log: MCPToolLogEntry, presentation: ReturnType<typeof getMCPLogPresentation>): MCPLogPillTone {
	if (presentation.approved) return "approved";
	// Only a recorded denial is an error. A pre-execution entry still awaiting a
	// decision keeps the tone of its execution status.
	if (presentation.policy && log.decision === "deny") return "error";
	if (log.status === "success") return "success";
	if (log.status === "error") return "error";
	if (log.status === "processing") return "processing";
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