/**
 * SSE frame parsing for Odin, kept separate from the React hook so it can be
 * tested without a DOM or a network.
 */

export type OdinEventType = "start" | "delta" | "tool_call_start" | "tool_call_end" | "question" | "error" | "done";

/** A structured question Odin poses when it cannot safely guess. */
export interface OdinQuestion {
	question: string;
	kind?: "time_range" | "scope" | "other";
	options: OdinQuestionOption[];
	/** Whether typing a different answer makes sense. */
	allow_other?: boolean;
}

/** Tokens and spend for one exchange. */
export interface OdinUsage {
	prompt_tokens?: number;
	completion_tokens?: number;
	total_tokens?: number;
	cost?: { total_cost?: number };
}

export interface OdinQuestionOption {
	label: string;
	/** The value Odin wants back, e.g. "-7d". Falls back to the label. */
	hint?: string;
}

export interface OdinEvent {
	type: OdinEventType;
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
	question?: OdinQuestion;
	/** The thread this turn was filed under. Echoed on done, including for a thread the server just created. */
	conversation_id?: string;
	finish_reason?: string;
	usage?: OdinUsage;
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
export function splitOdinFrames(buffer: string): { frames: string[]; rest: string } {
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
export function parseOdinFrame(frame: string): OdinEvent | null {
	const dataLines = frame
		.split("\n")
		.filter((line) => line.startsWith("data: "))
		.map((line) => line.slice(6));
	if (dataLines.length === 0) return null;

	const payload = dataLines.join("\n");
	if (payload === "[DONE]") return null;

	try {
		const parsed = JSON.parse(payload) as OdinEvent;
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
export function odinToolLabel(name: string, isRunning = false): string {
	const label = ODIN_TOOL_LABELS[name];
	// An unknown tool falls back to its raw name rather than something invented.
	// A wrong-but-friendly label for a step nobody recognises is worse than a
	// technical one, because it hides that the tool set has moved on.
	if (!label) return name;
	return isRunning ? label.running : label.done;
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
const ODIN_TOOL_LABELS: Record<string, { running: string; done: string }> = {
	count_logs: { running: "Checking log volume", done: "Checked log volume" },
	query_logs: { running: "Searching request logs", done: "Searched request logs" },
	get_log_detail: { running: "Opening a request", done: "Opened a request" },
	query_metrics: { running: "Querying metrics", done: "Queried metrics" },
	query_user_usage: { running: "Ranking users by usage", done: "Ranked users by usage" },
	query_virtual_key_usage: { running: "Ranking virtual keys by usage", done: "Ranked virtual keys by usage" },
	query_model_performance: { running: "Comparing models and providers", done: "Compared models and providers" },
	describe_filter_space: { running: "Checking available values", done: "Checked available values" },
	describe_scope: { running: "Validating scope", done: "Validated scope" },
	ask_user: { running: "Asking a question", done: "Asked a question" },
};

/**
 * Message shown for a terminal error code.
 *
 * `max_iterations` and `timeout` are phrased as something the user can act on,
 * because they usually mean the question was too broad rather than that
 * anything is broken.
 */
export function odinErrorMessage(code: string | undefined, message: string | undefined): string {
	return odinErrorDetail(code, message).summary;
}

/** A failure explained: what happened, and what to do about it. */
export interface OdinErrorDetail {
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
 * A bare "Odin could not settle on an answer" tells someone that it failed but
 * not what to do, so the only move left is to retype the same question and hope.
 * Each case below names the likely cause and the specific things that change the
 * outcome.
 */
export function odinErrorDetail(code: string | undefined, message: string | undefined): OdinErrorDetail {
	const raw = message && message.trim() !== "" ? message : undefined;

	switch (code) {
		case "not_configured":
			return {
				summary: "Odin is not configured yet.",
				cause: "No provider and model are set, or Odin is switched off in settings.",
				suggestions: ["Open Odin settings and choose a provider and model.", "Make sure Enable Odin is switched on."],
				raw,
			};
		case "max_iterations":
			return {
				summary: "Odin could not settle on an answer.",
				cause:
					"Odin ran its full budget of research steps without reaching a conclusion. That usually means the question was broad enough that each query raised another, so it kept looking instead of answering.",
				suggestions: [
					"Ask for one thing at a time: a single metric, one time range, one scope.",
					"Name the window explicitly, for example 'in the last 24 hours'.",
					"Name whose traffic you mean - a team, a customer, or all of them.",
					"Raise Max Iterations in Odin settings if the question genuinely needs more steps.",
				],
				raw,
			};
		case "timeout":
			return {
				summary: "That took too long.",
				cause:
					"The whole request passed its time budget before Odin finished. Long time ranges and wide scopes make every query slower, and Odin runs several.",
				suggestions: [
					"Try a shorter time range.",
					"Narrow to one team, customer or virtual key.",
					"Raise Request Timeout in Odin settings if your model is simply slow.",
				],
				raw,
			};
		case "upstream_error":
			return {
				summary: "Odin's model could not be reached.",
				cause: "The provider rejected the request or was unreachable. This is about Odin's own model, not the traffic you asked about.",
				suggestions: [
					"Check the provider, model and key in Odin settings.",
					"Confirm the Base URL is right - it defaults to this Bifrost.",
					"Try the same model from the playground to see whether it answers at all.",
				],
				raw,
			};
		case "tool_error":
			return {
				summary: "A query failed.",
				cause: "One of Odin's data queries returned an error, and it could not recover within its remaining steps.",
				suggestions: ["Try a narrower time range.", "Check that the model, key or team you named actually exists."],
				raw,
			};
		case "cancelled":
			return { summary: "Stopped.", cause: "The request was cancelled before it finished.", suggestions: [], raw };
		default:
			return {
				summary: raw ?? "Something went wrong.",
				cause: "Odin returned an error without a recognised code.",
				suggestions: ["Try the question again.", "If it keeps happening, report it with the details below."],
				raw,
			};
	}
}

/**
 * The fenced block Odin ends a data answer with, naming what the numbers cover.
 *
 * A fence rather than a heuristic on the prose: guessing which trailing lines
 * are provenance would occasionally eat a sentence of the actual answer, and
 * getting that wrong silently is worse than showing the block inline.
 */
const ODIN_PROVENANCE_FENCE = /\n?```odin-scope\n([\s\S]*?)```\s*$/;

export interface OdinAnswerParts {
	/** The answer itself, with the provenance block removed. */
	answer: string;
	/** What the numbers cover, or undefined when Odin did not say. */
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
export function splitOdinAnswer(content: string): OdinAnswerParts {
	const match = content.match(ODIN_PROVENANCE_FENCE);
	if (!match) return { answer: content };

	const provenance = match[1].trim();
	if (provenance === "") return { answer: content };
	return { answer: content.slice(0, match.index).trimEnd(), provenance };
}

/**
 * Formats a turn's usage for the transcript.
 *
 * Odin runs on a model chosen separately from the traffic Bifrost serves, and
 * its own calls do not appear in the logs it reads - so this line is the only
 * place its cost is visible. Returns null when there is nothing to report, since
 * a "0 tokens" label is worse than none.
 */
export function formatOdinUsage(usage: OdinUsage | undefined): string | null {
	if (!usage) return null;

	const parts: string[] = [];
	const total = usage.total_tokens ?? (usage.prompt_tokens ?? 0) + (usage.completion_tokens ?? 0);
	if (total > 0) parts.push(`${total.toLocaleString()} tokens`);

	const cost = usage.cost?.total_cost;
	if (typeof cost === "number" && cost > 0) {
		// Sub-cent answers are the common case, so a plain 2dp would render as
		// "$0.00" and read as free. Four places keeps it honest.
		parts.push(cost < 0.01 ? `$${cost.toFixed(4)}` : `$${cost.toFixed(2)}`);
	}

	return parts.length > 0 ? parts.join(" · ") : null;
}