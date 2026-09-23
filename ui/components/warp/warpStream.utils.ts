import type { WarpTurn } from "@/lib/contexts/warpContext";
import type { WarpLogIndexStatus, WarpStoredMessage } from "@/lib/types/warp";
/**
 * SSE frame parsing for Warp, kept separate from the React hook so it can be
 * tested without a DOM or a network.
 */

export type WarpEventType = "start" | "delta" | "tool_call_start" | "tool_call_end" | "question" | "error" | "done";

/** A structured question Warp poses when it cannot safely guess. */
export interface WarpQuestion {
	question: string;
	kind?: "time_range" | "scope" | "other";
	options: WarpQuestionOption[];
	/** Whether typing a different answer makes sense. */
	allow_other?: boolean;
}

/** Tokens and spend for one exchange. */
export interface WarpUsage {
	prompt_tokens?: number;
	completion_tokens?: number;
	total_tokens?: number;
	cost?: { total_cost?: number };
}

export interface WarpQuestionOption {
	label: string;
	/** The value Warp wants back, e.g. "-7d". Falls back to the label. */
	hint?: string;
}

export interface WarpEvent {
	type: WarpEventType;
	delta?: string;
	tool_id?: string;
	tool_name?: string;
	arguments?: string;
	iteration?: number;
	duration_ms?: number;
	failed?: boolean;
	tool_error?: string;
	code?: string;
	message?: string;
	question?: WarpQuestion;
	/** The thread this turn was filed under. Echoed on done, including for a thread the server just created. */
	conversation_id?: string;
	finish_reason?: string;
	usage?: WarpUsage;
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
export function historyForRequest<T extends { content: string }>(history: T[]): T[] {
	return history.filter((turn) => turn.content.trim() !== "");
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
export function warpToolLabel(name: string, isRunning = false): string {
	const label = WARP_TOOL_LABELS[name];
	// An unknown tool falls back to its raw name rather than something invented.
	// A wrong-but-friendly label for a step nobody recognises is worse than a
	// technical one, because it hides that the tool set has moved on.
	if (!label) return name;
	return isRunning ? label.running : label.done;
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
 * What each tool is called in the transcript, in both tenses.
 *
 * Two forms because a row is read in two states: shimmering while it runs, and
 * ticked once it is done. One tense has to be wrong in one of them - "Queried
 * metrics" beside a spinner reads as already finished, "Checking log volume"
 * beside a tick reads as still going - and these rows are the only thing making
 * a multi-second research pause legible, so it is worth the extra string.
 */
const WARP_TOOL_LABELS: Record<string, { running: string; done: string }> = {
	semantic_search_logs: { running: "Performing vector search", done: "Performed vector search" },
	count_logs: { running: "Checking log volume", done: "Checked log volume" },
	query_logs: { running: "Searching request logs", done: "Searched request logs" },
	get_log_detail: { running: "Opening a request", done: "Opened a request" },
	query_metrics: { running: "Querying metrics", done: "Queried metrics" },
	query_usage_by: { running: "Ranking usage", done: "Ranked usage" },
	query_model_performance: { running: "Comparing models and providers", done: "Compared models and providers" },
	describe_filter_space: { running: "Checking available values", done: "Checked available values" },
	describe_scope: { running: "Validating scope", done: "Validated scope" },
	ask_user: { running: "Asking a question", done: "Asked a question" },
};

/**
 * Whether a link in an answer points inside the dashboard.
 *
 * Warp's tools hand the model root-relative paths into the Logs view. Those are
 * followed with the router so the tray stays open beside the page they open.
 * A protocol-relative "//host" is not internal, whatever it looks like.
 */
export function isInternalWarpLink(href: string | undefined): boolean {
	if (!href || !href.startsWith("/")) return false;
	// The second character decides. WHATWG URL parsing folds a backslash into a
	// forward slash for special schemes, so "/\\host" resolves exactly as
	// "//host" does - and handing that to the router navigates the current tab
	// to another origin.
	const second = href[1];
	return second !== "/" && second !== "\\";
}

/**
 * Rebuilds transcript turns from a stored thread, so a reopened conversation
 * looks the way it did live: same tool rows, same error card, same partial
 * note, same cost line.
 */
export function turnsFromStoredMessages(messages: WarpStoredMessage[]): WarpTurn[] {
	return messages.map((message, index) => {
		if (message.role === "user") {
			return { role: "user", content: message.content };
		}
		const turn: WarpTurn = { role: "assistant", content: message.content };
		if (message.tool_calls && message.tool_calls.length > 0) {
			turn.toolCalls = message.tool_calls.map((call, callIndex) => ({
				// Stored calls have no id; a stable synthetic one keeps React keys
				// and the row lookup honest.
				id: `stored-${index}-${callIndex}`,
				name: call.name,
				durationMs: call.duration_ms,
				failed: call.failed,
			}));
		}
		if (message.error) turn.error = message.error;
		if (isPartialAnswer(message.finish_reason)) turn.partial = true;
		// A stored assistant turn that asked rather than answered has to come back
		// marked. send() serialises turn.question as `question: true`, which is how
		// the server knows not to count the reply as a fresh question - without it
		// a reopened thread replays a clarification as an answer and the repeated
		// question limit stops applying.
		if (isWarpQuestionFinish(message.finish_reason)) {
			// The stored structured question restores the same selectable card the
			// live turn showed, hints included. A row without one (or a blob that
			// failed to decode server-side) still comes back as a question, so the
			// reply is not misfiled as a fresh answer - just without shortcuts.
			if (message.question) {
				turn.question = {
					question: message.question.question || message.content,
					options: (message.question.options ?? []).map((option) => ({ label: option.label, hint: option.hint })),
					allow_other: message.question.allow_other,
					kind: message.question.kind as WarpQuestion["kind"],
				};
			} else {
				turn.question = { question: message.content, options: [] };
			}
		}
		if ((message.total_tokens ?? 0) > 0 || (message.cost ?? 0) > 0) {
			turn.usage = { total_tokens: message.total_tokens, cost: { total_cost: message.cost } };
		}
		return turn;
	});
}

/** What the tray's index chip says, and how loudly. */
export interface IndexStatusLabel {
	label: string;
	tone: "ok" | "busy" | "error" | "muted";
	/** A cause worth showing on hover, such as the last backfill error. */
	detail?: string;
}

/**
 * Folds the index status into one chip. Progress is shown while a backfill is
 * running because that is the one moment the number changes; every other state
 * is a word.
 */
export function indexStatusLabel(status: WarpLogIndexStatus): IndexStatusLabel {
	// The idle response is an id-less zeroed body. Narrowing it away here is what
	// stops "0 / 0 scanned" being read as a job and rendered as 0% progress.
	const backfill = status.backfill?.status === "idle" ? undefined : status.backfill;
	switch (status.state) {
		case "unavailable":
			return { label: "No vector store", tone: "error" };
		case "not_configured":
			return { label: "Search not set up", tone: "muted" };
		case "failed": {
			const detail = backfill?.last_error;
			return detail ? { label: "Indexing failed", tone: "error", detail } : { label: "Indexing failed", tone: "error" };
		}
		case "indexing": {
			const total = backfill?.total ?? 0;
			const scanned = backfill?.scanned ?? 0;
			if (total > 0) {
				return { label: `Indexing ${Math.min(100, Math.floor((scanned / total) * 100))}%`, tone: "busy" };
			}
			return { label: "Indexing", tone: "busy" };
		}
		default:
			return { label: "Index ready", tone: "ok" };
	}
}

/**
 * Whether a keystroke aimed at this element is someone typing.
 *
 * The question card binds its shortcuts on the document because the composer
 * holds focus when the card appears. An empty composer is not typing, so the
 * letters, arrows and Enter still pick an option there. The moment it holds
 * text the person has chosen to write their own answer and every key is theirs.
 * Any other input is always typing: the card should never eat a keystroke
 * meant for a search box elsewhere on the page.
 */
export function isTypingInto(
	target: { tagName: string; value?: string; isContentEditable?: boolean; dataset?: { testid?: string } } | null | undefined,
): boolean {
	if (!target) return false;
	// contenteditable is typing too. It was covered by the inline tagName check
	// this helper replaced, and dropping it meant a rich-text field anywhere on
	// the page lost keystrokes to the question shortcuts - the same loss the
	// TEXTAREA and INPUT cases exist to prevent, in the one place it is hardest
	// to notice because the element is a DIV.
	if (target.isContentEditable) return true;
	if (target.tagName !== "TEXTAREA") return target.tagName === "INPUT";
	// Only Warp's own composer is treated as "empty means the shortcuts apply".
	// Any other textarea on the page is someone else's, and eating a keystroke
	// there because it happens to match an option letter loses a character they
	// were typing into something unrelated.
	if (target.dataset?.testid !== WARP_COMPOSER_TESTID) return true;
	return (target.value ?? "").trim() !== "";
}

/** data-testid of Warp's composer, the one textarea the question shortcuts own. */
export const WARP_COMPOSER_TESTID = "warp-composer-input";

/**
 * Whether the next queued message should be sent now.
 *
 * Exactly one message goes out per finished turn: on the transition from
 * streaming to idle. Checking the transition rather than the idle state is
 * what stops two queued messages from being sent back to back - after the
 * first is dequeued the panel is still idle for a render, and an idle check
 * would fire again before the request had a chance to start streaming.
 */
export function shouldDrainQueue(wasStreaming: boolean, isStreaming: boolean, queued: number, questionPending = false): boolean {
	// A question also ends streaming. Draining into it made the next queued
	// follow-up the answer to a clarification it has nothing to do with - Warp
	// asks "which provider?" and receives "what did this cost last week".
	if (questionPending) return false;
	return wasStreaming && !isStreaming && queued > 0;
}

/** The finish reason the server sends when Warp asked instead of answering. */
export const WARP_FINISH_QUESTION = "question";

/** Whether a stored turn ended by posing a question. */
export function isWarpQuestionFinish(finishReason: string | undefined): boolean {
	return finishReason === WARP_FINISH_QUESTION;
}

/**
 * Whether a click should be handled by the router instead of the browser.
 *
 * Intercepting every click took Command-click, Ctrl-click, Shift-click and
 * middle-click with it, so an answer's link could not be opened in a new tab or
 * window at all - the one thing someone reading a cited figure most wants to do.
 */
export function isPlainLeftClick(event: {
	button?: number;
	metaKey?: boolean;
	ctrlKey?: boolean;
	shiftKey?: boolean;
	altKey?: boolean;
}): boolean {
	return (event.button ?? 0) === 0 && !event.metaKey && !event.ctrlKey && !event.shiftKey && !event.altKey;
}

/** The finish reason the server sends when Warp answered on its last research step. */
export const WARP_FINISH_PARTIAL = "partial";

/**
 * Whether a done frame's finish reason marks the answer as partial.
 *
 * Partial means the model was cut off: it had used every research step and was
 * told to answer from what it had. The text is still worth reading, but the
 * transcript has to say so, or a half-checked figure reads as a settled one.
 */
export function isPartialAnswer(finishReason: string | undefined): boolean {
	return finishReason === WARP_FINISH_PARTIAL;
}

/**
 * Message shown for a terminal error code.
 *
 * `max_iterations` and `timeout` are phrased as something the user can act on,
 * because they usually mean the question was too broad rather than that
 * anything is broken.
 */
export function errorMessage(code: string | undefined, message: string | undefined): string {
	return warpErrorDetail(code, message).summary;
}

/** A failure explained: what happened, and what to do about it. */
export interface WarpErrorDetail {
	/** One line, always shown. */
	summary: string;
	/** What actually went wrong, shown when the card is expanded. */
	cause: string;
	/** Concrete next steps, in the order worth trying. */
	suggestions: string[];
	/** The raw server message, when it says more than the summary does. */
	raw?: string;
}

/**
 * Turns a terminal error into something actionable.
 *
 * A bare "Warp could not settle on an answer" tells someone that it failed but
 * not what to do, so the only move left is to retype the same question and hope.
 * Each case below names the likely cause and the specific things that change the
 * outcome.
 */
export function warpErrorDetail(code: string | undefined, message: string | undefined): WarpErrorDetail {
	const raw = message && message.trim() !== "" ? message : undefined;

	switch (code) {
		case "not_configured":
			return {
				summary: "Warp is not configured yet.",
				cause: "No provider and model are set, or Warp is switched off in settings.",
				suggestions: ["Open Warp settings and choose a provider and model.", "Make sure Enable Warp is switched on."],
				raw,
			};
		case "max_iterations":
			return {
				summary: "Warp could not settle on an answer.",
				cause:
					"Warp ran its full budget of research steps without reaching a conclusion. That usually means the question was broad enough that each query raised another, so it kept looking instead of answering.",
				suggestions: [
					"Ask for one thing at a time: a single metric, one time range, one scope.",
					"Name the window explicitly, for example 'in the last 24 hours'.",
					"Name whose traffic you mean - a team, a customer, or all of them.",
					"Raise Max Iterations in Warp settings if the question genuinely needs more steps.",
				],
				raw,
			};
		case "timeout":
			return {
				summary: "That took too long.",
				cause:
					"The whole request passed its time budget before Warp finished. Long time ranges and wide scopes make every query slower, and Warp runs several.",
				suggestions: [
					"Try a shorter time range.",
					"Narrow to one team, customer or virtual key.",
					"Raise Request Timeout in Warp settings if your model is simply slow.",
				],
				raw,
			};
		case "upstream_error":
			return {
				summary: "Warp's model could not be reached.",
				cause: "The provider rejected the request or was unreachable. This is about Warp's own model, not the traffic you asked about.",
				suggestions: [
					"Check the provider, model and key in Warp settings.",
					"Confirm the Base URL is right - it defaults to this Bifrost.",
					"Try the same model from the playground to see whether it answers at all.",
				],
				raw,
			};
		case "tool_error":
			return {
				summary: "A query failed.",
				cause: "One of Warp's data queries returned an error, and it could not recover within its remaining steps.",
				suggestions: ["Try a narrower time range.", "Check that the model, key or team you named actually exists."],
				raw,
			};
		case "cancelled":
			return { summary: "Stopped.", cause: "The request was cancelled before it finished.", suggestions: [], raw };
		default:
			return {
				summary: raw ?? "Something went wrong.",
				cause: "Warp returned an error without a recognised code.",
				suggestions: ["Try the question again.", "If it keeps happening, report it with the details below."],
				raw,
			};
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

/**
 * The fenced block Warp ends a data answer with, naming what the numbers cover.
 *
 * A fence rather than a heuristic on the prose: guessing which trailing lines
 * are provenance would occasionally eat a sentence of the actual answer, and
 * getting that wrong silently is worse than showing the block inline.
 */
const WARP_PROVENANCE_FENCE = /\n?```warp-scope\n([\s\S]*?)```\s*$/;

export interface WarpAnswerParts {
	/** The answer itself, with the provenance block removed. */
	answer: string;
	/** What the numbers cover, or undefined when Warp did not say. */
	provenance?: string;
}

/**
 * Splits an answer from its provenance block.
 *
 * The window, scope and filters matter - they are what make a number checkable -
 * but they are reference material, not the answer. Left inline they push the
 * next question off the screen and are re-read every time someone scrolls past.
 * Lifted out, they are one click away when someone doubts a figure.
 */
export function splitWarpAnswer(content: string): WarpAnswerParts {
	const match = content.match(WARP_PROVENANCE_FENCE);
	if (!match) return { answer: content };

	const provenance = match[1].trim();
	if (provenance === "") return { answer: content };
	return { answer: content.slice(0, match.index).trimEnd(), provenance };
}

/**
 * Formats a turn's usage for the transcript.
 *
 * Warp runs on a model chosen separately from the traffic Bifrost serves, and
 * its own calls do not appear in the logs it reads - so this line is the only
 * place its cost is visible. Returns null when there is nothing to report, since
 * a "0 tokens" label is worse than none.
 */
export function formatWarpUsage(usage: WarpUsage | undefined): string | null {
	if (!usage) return null;

	const parts: string[] = [];
	const total = usage.total_tokens ?? (usage.prompt_tokens ?? 0) + (usage.completion_tokens ?? 0);
	if (total > 0) parts.push(`${total.toLocaleString()} tokens`);

	const cost = usage.cost?.total_cost;
	if (typeof cost === "number" && cost > 0) {
		// Sub-cent answers are the common case, so a plain 2dp would render as
		// "$0.00" and read as free. Four places keeps it honest - and below what
		// four places can express, say so rather than rounding a real charge down
		// to "$0.0000", which reads as free just the same.
		if (cost < 0.0001) {
			parts.push("<$0.0001");
		} else {
			parts.push(cost < 0.01 ? `$${cost.toFixed(4)}` : `$${cost.toFixed(2)}`);
		}
	}

	return parts.length > 0 ? parts.join(" · ") : null;
}