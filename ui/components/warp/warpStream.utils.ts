/**
 * SSE frame parsing for Warp, kept separate from the React hook so it can be
 * tested without a DOM or a network.
 */

export type WarpEventType = "start" | "delta" | "tool_call_start" | "tool_call_end" | "error" | "done";

export interface WarpEvent {
	type: WarpEventType;
	delta?: string;
	tool_id?: string;
	tool_name?: string;
	arguments?: string;
	iteration?: number;
	duration_ms?: number;
	failed?: boolean;
	code?: string;
	message?: string;
	finish_reason?: string;
	iterations?: number;
	model?: string;
	provider?: string;
}

/**
 * Splits a byte-stream buffer into complete SSE frames.
 *
 * Returns the frames it could complete plus whatever is left over, because a
 * chunk boundary can land mid-frame. Feeding the remainder back in on the next
 * read is what stops a delta from being silently dropped when the network splits
 * a message in an inconvenient place.
 */
export function splitWarpFrames(buffer: string): { frames: string[]; rest: string } {
	const parts = buffer.split("\n\n");
	// The final part has no terminator yet, so it may be incomplete.
	const rest = parts.pop() ?? "";
	return { frames: parts.filter((part) => part.trim() !== ""), rest };
}

/**
 * Parses one SSE frame into an event.
 *
 * The `event:` line is ignored in favour of the `type` field inside the JSON.
 * They always agree, and trusting the payload means one source of truth rather
 * than two that can drift.
 *
 * Returns null for anything unparseable - heartbeat comments, blank frames, a
 * truncated write - so the caller can skip rather than tear down a stream that
 * is otherwise healthy.
 */
export function parseWarpFrame(frame: string): WarpEvent | null {
	const dataLines = frame
		.split("\n")
		.filter((line) => line.startsWith("data: "))
		.map((line) => line.slice(6));
	if (dataLines.length === 0) return null;

	const payload = dataLines.join("\n");
	if (payload === "[DONE]") return null;

	try {
		const parsed = JSON.parse(payload) as WarpEvent;
		return parsed.type ? parsed : null;
	} catch {
		return null;
	}
}

/**
 * Human-readable label for a tool, used on the collapsed row in the transcript.
 *
 * Falling back to the raw name keeps a newly added server-side tool legible
 * instead of rendering as blank until the UI catches up.
 */
export function warpToolLabel(name: string): string {
	const labels: Record<string, string> = {
		query_logs: "Searched request logs",
		get_log_detail: "Opened a request",
		query_metrics: "Queried metrics",
		query_user_usage: "Ranked users by usage",
		query_virtual_key_usage: "Ranked virtual keys by usage",
		query_model_performance: "Compared models and providers",
		describe_filter_space: "Checked available values",
	};
	return labels[name] ?? name;
}

/**
 * Message shown for a terminal error code.
 *
 * `max_iterations` and `timeout` are phrased as something the user can act on,
 * because they usually mean the question was too broad rather than that
 * anything is broken.
 */
export function errorMessage(code: string | undefined, message: string | undefined): string {
	switch (code) {
		case "not_configured":
			return "Warp is not configured yet.";
		case "max_iterations":
			return "Warp could not settle on an answer. Try a narrower question.";
		case "timeout":
			return "That took too long. Try a shorter time range.";
		case "cancelled":
			return "Stopped.";
		default:
			return message || "Something went wrong.";
	}
}