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
	// Normalise line endings first. The SSE spec allows CRLF and lone CR, and a
	// proxy that rewrites them is entirely legal - but splitting on "\n\n" alone
	// then finds no frame boundary at all, so the whole answer is silently
	// dropped and the chat completes empty with nothing to explain it.
	//
	// A CR at the very end is held back rather than normalised, because only the
	// next read says what it is: converting it eagerly turns the LF that follows
	// into a second newline, and a frame whose data spans two lines is torn in
	// half - each piece unparseable, so the delta vanishes with no error.
	let pending = buffer;
	let carry = "";
	if (pending.endsWith("\r")) {
		// Only ambiguous when it could still be the first half of a CRLF. After
		// another line ending it is the second half of a delimiter that is already
		// complete - "\r\r" and "\n\r" both end a frame - and holding it back left
		// that finished frame sitting in `rest`, waiting for a chunk that may never
		// come. A buffer that is nothing but "\r" has no preceding character, so it
		// stays ambiguous and is held.
		const previous = pending.at(-2);
		if (previous !== "\r" && previous !== "\n") {
			pending = pending.slice(0, -1);
			carry = "\r";
		}
	}
	const parts = pending.replace(/\r\n/g, "\n").replace(/\r/g, "\n").split("\n\n");
	// The final part has no terminator yet, so it may be incomplete. The held-back
	// CR rides along with it so the next read sees the pair intact.
	const rest = (parts.pop() ?? "") + carry;
	return { frames: parts.filter((part) => part.trim() !== ""), rest };
}

/**
 * The turns worth replaying to the server.
 *
 * A turn that ended in an error is stored with empty content so the transcript
 * can render the failure, but the wire format carries only role and content -
 * the error does not survive serialization. Replaying it sent
 * `{role: "assistant", content: ""}`, which reads as a normal empty answer:
 * the model is told it once replied with nothing, and Anthropic rejects an
 * empty text block outright, so one failed turn could poison the whole thread.
 */
export function historyForRequest(history: { role: string; content: string }[]): { role: string; content: string }[] {
	return history.filter((turn) => turn.content.trim() !== "").map((turn) => ({ role: turn.role, content: turn.content }));
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
	// The space after `data:` is optional in the SSE spec, so both forms have to
	// be accepted; only one leading space is consumed, because any further
	// whitespace is part of the value.
	const dataLines = frame
		.replace(/\r\n/g, "\n")
		.replace(/\r/g, "\n")
		.split("\n")
		.filter((line) => line.startsWith("data:"))
		.map((line) => {
			const value = line.slice(5);
			return value.startsWith(" ") ? value.slice(1) : value;
		});
	if (dataLines.length === 0) return null;

	const payload = dataLines.join("\n");
	if (payload === "[DONE]") return null;

	try {
		const parsed = JSON.parse(payload) as WarpEvent;
		return isUsableWarpEvent(parsed) ? parsed : null;
	} catch {
		return null;
	}
}

/**
 * Whether a parsed frame carries the fields its own branch will read.
 *
 * A truthy `type` was the only check, so a `tool_call_start` with
 * `tool_name: {}` reached applyEvent, was stored as `call.name`, and then
 * rendered as a React child - which throws, because objects are not valid
 * children, and takes the whole panel down over one malformed frame.
 *
 * Dropping the frame rather than repairing it: the stream is otherwise healthy
 * and the caller already skips nulls, so a bad frame costs one event instead of
 * the conversation.
 */
export function isUsableWarpEvent(event: WarpEvent | null | undefined): event is WarpEvent {
	if (!event) return false;
	// The input is JSON.parse output wearing a WarpEvent cast, so every field is
	// checked as unknown - typing the checks against WarpEvent itself would let
	// the compiler assume exactly what this function exists to establish.
	const raw = event as unknown as Record<string, unknown>;
	if (typeof raw.type !== "string" || raw.type === "") return false;
	const optionalString = (value: unknown) => value === undefined || typeof value === "string";
	const optionalNumber = (value: unknown) => value === undefined || typeof value === "number";
	if (!optionalString(raw.delta) || !optionalString(raw.tool_id)) return false;
	if (!optionalString(raw.code) || !optionalString(raw.message)) return false;
	if (!optionalString(raw.finish_reason)) return false;
	if (!optionalNumber(raw.duration_ms) || !optionalNumber(raw.iteration)) return false;
	switch (raw.type) {
		case "tool_call_start":
			// The one field that is rendered directly, so it must be a string.
			return typeof raw.tool_name === "string" && raw.tool_name !== "";
		case "tool_call_end":
			return typeof raw.tool_id === "string" && (raw.failed === undefined || typeof raw.failed === "boolean");
		case "delta":
			return typeof raw.delta === "string";
		default:
			return true;
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
 * The status a tool-call row communicates, as text.
 *
 * Rendered visually hidden beside the status icon: the icons alone carry the
 * running/failed/completed state only through shape and color, which a screen
 * reader cannot see.
 */
export function warpToolStatusLabel(call: { durationMs?: number; failed?: boolean }): string {
	if (call.durationMs === undefined) return "In progress";
	return call.failed ? "Failed" : "Completed";
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
/**
 * Encodes a turn's terminal error as the `code:message` pair the transcript
 * decodes.
 *
 * The leading colon on a code-less error is load-bearing. Without it the
 * decoder reads the entire message as a code, finds no match, and falls through
 * to the generic "Something went wrong." - throwing away the status line or
 * network error that was the only useful part.
 */
export function encodeTurnError(code: string | undefined, message: string): string {
	return `${code ?? ""}:${message}`;
}

/** The terminal error codes the agent emits, and the only valid code prefixes. */
const WARP_ERROR_CODES = new Set(["not_configured", "upstream_error", "tool_error", "max_iterations", "timeout", "cancelled"]);

/**
 * Whether a string already carries the `code:message` encoding.
 *
 * Checked against the known codes rather than "does it contain a colon":
 * plenty of real error messages do ("connect: connection refused",
 * "TypeError: Failed to fetch"), and treating their first word as a code drops
 * it from what the reader sees.
 */
export function isEncodedTurnError(error: string): boolean {
	const separator = error.indexOf(":");
	if (separator === -1) return false;
	const code = error.slice(0, separator);
	return code === "" || WARP_ERROR_CODES.has(code);
}

/**
 * Splits an encoded turn error back into its parts.
 *
 * Splits on the first colon only, so a message that contains colons of its own
 * ("connect: connection refused") survives intact. A string with no colon is
 * treated as a bare message rather than a code, which keeps errors produced
 * before this encoding existed readable.
 */
export function decodeTurnError(error: string): { code: string; message: string } {
	const separator = error.indexOf(":");
	if (separator === -1) return { code: "", message: error.trim() };
	return { code: error.slice(0, separator).trim(), message: error.slice(separator + 1).trim() };
}