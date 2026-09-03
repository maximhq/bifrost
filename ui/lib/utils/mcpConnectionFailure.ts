// Helpers shared by the MCP servers table's state badge popover and the
// server sheet's reason block, so both surfaces fold and describe the same
// failure records the same way.

import type { MCPClient, MCPConnectionFailure, MCPConnectionState, MCPInstanceState } from "@/lib/types/mcp";

const STAGE_LABELS: Record<string, string> = {
	connect: "Connecting",
	ping: "Ping",
	list_tools: "Listing tools",
	tool_discovery: "Discovering tools",
	transport_lost: "Connection dropped",
	credential: "Credential",
};

export function failureStageLabel(stage: string): string {
	return STAGE_LABELS[stage] ?? stage;
}

/**
 * formatDurationSince renders how long ago `iso` was with seconds
 * granularity: while a server is unstable the checker retries every ten
 * seconds, so "just now" for anything under a minute would hide whether the
 * last attempt is fresh.
 */
export function formatDurationSince(iso: string): string {
	const t = new Date(iso).getTime();
	if (Number.isNaN(t)) return iso;
	const s = Math.max(0, Math.floor((Date.now() - t) / 1000));
	if (s < 60) return `${s}s`;
	const m = Math.floor(s / 60);
	if (m < 60) return `${m}m`;
	const h = Math.floor(m / 60);
	if (h < 48) return `${h}h`;
	return `${Math.floor(h / 24)}d`;
}

/**
 * formatFailureTiming is the timing line under a failure: when the last
 * attempt failed and, when the run spans more than one attempt, how long the
 * server has been in this state.
 */
export function formatFailureTiming(failure: MCPConnectionFailure, state: MCPConnectionState): string {
	const line = `Last failed ${formatDurationSince(failure.at)} ago`;
	if (!failure.since || failure.since === failure.at) return line;
	const word = state === "needs_reauth" ? "needs reauth" : state;
	return `${line} · ${word} for ${formatDurationSince(failure.since)}`;
}

export interface MCPInstanceGroup {
	state: MCPConnectionState;
	count: number;
	last_failure?: MCPConnectionFailure;
}

const STATE_RANK: Record<string, number> = { needs_reauth: 0, error: 1, unstable: 2, pending_verification: 3, healthy: 9 };

/**
 * foldInstanceStates collapses a per-instance breakdown into groups that
 * read the same: same state, same failed stage, same error text. Instances
 * failing the same way become one row with a count, so a three-node outage
 * with one cause is one line. A group's `at` is the newest across its
 * instances (is this still happening?) and its `since` the oldest (how long
 * has the longest-suffering instance been failing?). Unhealthy groups sort
 * first.
 */
export function foldInstanceStates(nodeStates: Record<string, MCPInstanceState>): MCPInstanceGroup[] {
	const groups = new Map<string, MCPInstanceGroup>();
	for (const instance of Object.values(nodeStates)) {
		const f = instance.last_failure;
		const key = `${instance.state}|${f ? `${f.stage}|${f.message}` : ""}`;
		const existing = groups.get(key);
		if (!existing) {
			groups.set(key, { state: instance.state, count: 1, last_failure: f ? { ...f } : undefined });
			continue;
		}
		existing.count += 1;
		if (f && existing.last_failure) {
			if (Date.parse(f.at) > Date.parse(existing.last_failure.at)) existing.last_failure.at = f.at;
			if (Date.parse(f.since) < Date.parse(existing.last_failure.since)) existing.last_failure.since = f.since;
		}
	}
	return Array.from(groups.values()).sort((a, b) => (STATE_RANK[a.state] ?? 5) - (STATE_RANK[b.state] ?? 5));
}

/** hasStateReason reports whether a server carries anything worth opening a popover for. */
export function hasStateReason(client: Pick<MCPClient, "last_failure" | "node_states">): boolean {
	return !!client.last_failure || (!!client.node_states && Object.keys(client.node_states).length > 0);
}

/** stateReasonTitle is the first line of the popover and the sheet's reason block. */
export function stateReasonTitle(state: MCPConnectionState, hasBreakdown: boolean): string {
	switch (state) {
		case "degraded":
			return "Instances disagree about this server's state";
		case "unstable":
			return hasBreakdown ? "Last connection check failed on every instance" : "Last connection check failed";
		case "needs_reauth":
			return "Reauthorization required";
		case "error":
			return "Not registered in the runtime";
		default:
			return "Connection state";
	}
}