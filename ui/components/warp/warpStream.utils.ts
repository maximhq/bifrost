import type { WarpTurn, WarpTurnToolCall } from "@/lib/contexts/warpContext";
import type { WarpLogIndexStatus, WarpStoredMessage } from "@/lib/types/warp";
/** SSE frame parsing for Warp, kept out of the hook so it is testable without a DOM or network. */

export type WarpEventType = "start" | "delta" | "tool_call_start" | "tool_call_end" | "question" | "error" | "done";

/** A structured question Warp poses when it cannot safely guess. */
export interface WarpQuestion {
	question: string;
	kind?: "time_range" | "scope" | "other";
	options: WarpQuestionOption[];
	allow_other?: boolean;
}

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
	/** Echoed on done, including for a thread the server just created. */
	conversation_id?: string;
	finish_reason?: string;
	usage?: WarpUsage;
	iterations?: number;
	model?: string;
	provider?: string;
}

/** Splits a buffer into complete SSE frames; `rest` must be fed back in, since a chunk can end mid-frame. */
export function splitWarpFrames(buffer: string): { frames: string[]; rest: string } {
	// SSE allows CRLF and lone CR; without normalising, "\n\n" never matches and the answer is silently lost.
	let pending = buffer;
	let carry = "";
	if (pending.endsWith("\r")) {
		// A trailing CR may be half of a CRLF, so hold it back, unless it already completes a delimiter.
		const previous = pending.at(-2);
		if (previous !== "\r" && previous !== "\n") {
			pending = pending.slice(0, -1);
			carry = "\r";
		}
	}
	const parts = pending.replace(/\r\n/g, "\n").replace(/\r/g, "\n").split("\n\n");
	const rest = (parts.pop() ?? "") + carry;
	return { frames: parts.filter((part) => part.trim() !== ""), rest };
}

/** Drops empty (errored) turns: replayed as empty assistant content they poison the thread, and Anthropic rejects them. */
export function historyForRequest<T extends { content: string }>(history: T[]): T[] {
	return history.filter((turn) => turn.content.trim() !== "");
}

/** Parses one SSE frame, or null for heartbeats and junk so the caller can skip it without tearing down the stream. */
export function parseWarpFrame(frame: string): WarpEvent | null {
	// The space after `data:` is optional in SSE; strip at most one, since further whitespace is part of the value.
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

/** Rejects frames with mistyped fields, e.g. a non-string tool_name would crash the panel when rendered. */
export function isUsableWarpEvent(event: WarpEvent | null | undefined): event is WarpEvent {
	if (!event) return false;
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
			return typeof raw.tool_name === "string" && raw.tool_name !== "";
		case "tool_call_end":
			return typeof raw.tool_id === "string" && (raw.failed === undefined || typeof raw.failed === "boolean");
		case "delta":
			return typeof raw.delta === "string";
		default:
			return true;
	}
}

export function warpToolLabel(name: string, isRunning = false): string {
	const label = WARP_TOOL_LABELS[name];
	// Unknown tools show their raw name, so a new server-side tool stays legible instead of mislabelled.
	if (!label) return name;
	return isRunning ? label.running : label.done;
}

/** Screen-reader text for a tool row's status, which the icons convey only by shape and color. */
export function warpToolStatusLabel(call: { durationMs?: number; failed?: boolean }): string {
	if (call.durationMs === undefined) return "In progress";
	return call.failed ? "Failed" : "Completed";
}

const WARP_TOOL_LABELS: Record<string, { running: string; done: string }> = {
	semantic_search_logs: { running: "Performing vector search", done: "Performed vector search" },
	count_logs: { running: "Checking log volume", done: "Checked log volume" },
	query_logs: { running: "Searching request logs", done: "Searched request logs" },
	get_log_detail: { running: "Opening a request", done: "Opened a request" },
	get_request_trace: { running: "Tracing what happened", done: "Traced what happened" },
	query_metrics: { running: "Querying metrics", done: "Queried metrics" },
	query_usage_by: { running: "Ranking usage", done: "Ranked usage" },
	query_model_performance: { running: "Comparing models and providers", done: "Compared models and providers" },
	render_chart: { running: "Drawing a chart", done: "Drew a chart" },
	describe_filter_space: { running: "Checking available values", done: "Checked available values" },
	describe_virtual_key: { running: "Checking virtual key limits", done: "Checked virtual key limits" },
	list_topics: { running: "Listing topics", done: "Listed topics" },
	ask_user: { running: "Asking a question", done: "Asked a question" },
};

/** Whether a link in an answer is a root-relative dashboard path to follow with the router. */
export function isInternalWarpLink(href: string | undefined): boolean {
	if (!href || !href.startsWith("/")) return false;
	// URL parsing treats "/\host" like "//host", so both would navigate to another origin.
	const second = href[1];
	return second !== "/" && second !== "\\";
}

/** Rebuilds transcript turns from a stored thread so a reopened conversation looks as it did live. */
export function turnsFromStoredMessages(messages: WarpStoredMessage[]): WarpTurn[] {
	return messages.map((message, index) => {
		if (message.role === "user") {
			return { role: "user", content: message.content };
		}
		const turn: WarpTurn = { role: "assistant", content: message.content };
		if (message.tool_calls && message.tool_calls.length > 0) {
			turn.toolCalls = message.tool_calls.map((call, callIndex) => ({
				id: `stored-${index}-${callIndex}`,
				name: call.name,
				durationMs: call.duration_ms,
				failed: call.failed,
				textOffset: call.text_offset,
			}));
		}
		if (message.error) turn.error = message.error;
		if (isPartialAnswer(message.finish_reason)) turn.partial = true;
		// Must be marked as a question, or the server counts the reply as a fresh question on replay.
		if (isWarpQuestionFinish(message.finish_reason)) {
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

/** One stretch of a turn; `final` marks the trailing text, the answer as opposed to narration. */
export type WarpTimelineItem = { kind: "text"; text: string; final: boolean } | { kind: "tools"; calls: WarpTurnToolCall[] };

/** Length in code points, matching how the server counts tool-call offsets (not UTF-16 units). */
export function warpTextLength(text: string): number {
	return Array.from(text).length;
}

/** Interleaves a turn's text and tool calls by each call's text offset, clamped to stay monotonic and in range. */
export function warpTimeline(content: string, toolCalls: WarpTurnToolCall[] | undefined): WarpTimelineItem[] {
	const chars = Array.from(content);
	const items: WarpTimelineItem[] = [];
	let cursor = 0;
	const pushTextUpTo = (end: number) => {
		const text = chars.slice(cursor, end).join("").trim();
		if (text) items.push({ kind: "text", text, final: false });
		cursor = end;
	};
	for (const call of toolCalls ?? []) {
		pushTextUpTo(Math.min(chars.length, Math.max(cursor, call.textOffset ?? 0)));
		const last = items[items.length - 1];
		if (last?.kind === "tools") last.calls.push(call);
		else items.push({ kind: "tools", calls: [call] });
	}
	pushTextUpTo(chars.length);
	const last = items[items.length - 1];
	if (last?.kind === "text") last.final = true;
	return items;
}

/** The question the thread still awaits: only the last turn counts, and only if it has options to pick. */
export function pendingWarpQuestion(turns: WarpTurn[]): WarpQuestion | null {
	const last = turns[turns.length - 1];
	if (!last || last.role !== "assistant" || !last.question || last.question.options.length === 0) return null;
	return last.question;
}

export interface IndexStatusLabel {
	label: string;
	shortLabel?: string;
	tone: "ok" | "busy" | "error" | "muted";
	detail?: string;
}

export function indexStatusLabel(status: WarpLogIndexStatus): IndexStatusLabel {
	// The idle response is a zeroed body; treating it as a job would render 0% progress.
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
			return { label: "Index ready", shortLabel: "Ready", tone: "ok" };
	}
}

/** Whether a key event is typing; an empty Warp composer is not, so question shortcuts still apply there. */
export function isTypingInto(
	target: { tagName: string; value?: string; isContentEditable?: boolean; dataset?: { testid?: string } } | null | undefined,
): boolean {
	if (!target) return false;
	if (target.isContentEditable) return true;
	if (target.tagName !== "TEXTAREA") return target.tagName === "INPUT";
	if (target.dataset?.testid !== WARP_COMPOSER_TESTID) return true;
	return (target.value ?? "").trim() !== "";
}

export const WARP_COMPOSER_TESTID = "warp-composer-input";

/** Fires on the streaming-to-idle edge only, so one message goes per turn; held while a question or error is showing. */
export function shouldDrainQueue(
	wasStreaming: boolean,
	isStreaming: boolean,
	queued: number,
	questionPending = false,
	lastTurnFailed = false,
): boolean {
	if (questionPending) return false;
	if (lastTurnFailed) return false;
	return wasStreaming && !isStreaming && queued > 0;
}

export const WARP_FINISH_QUESTION = "question";

export function isWarpQuestionFinish(finishReason: string | undefined): boolean {
	return finishReason === WARP_FINISH_QUESTION;
}

/** Modifier and middle clicks go to the browser so links can open in a new tab. */
export function isPlainLeftClick(event: {
	button?: number;
	metaKey?: boolean;
	ctrlKey?: boolean;
	shiftKey?: boolean;
	altKey?: boolean;
}): boolean {
	return (event.button ?? 0) === 0 && !event.metaKey && !event.ctrlKey && !event.shiftKey && !event.altKey;
}

/** Sent when Warp ran out of research steps and answered from what it had. */
export const WARP_FINISH_PARTIAL = "partial";

export function isPartialAnswer(finishReason: string | undefined): boolean {
	return finishReason === WARP_FINISH_PARTIAL;
}

export function errorMessage(code: string | undefined, message: string | undefined): string {
	return warpErrorDetail(code, message).summary;
}

export interface WarpErrorDetail {
	summary: string;
	cause: string;
	suggestions: string[];
	raw?: string;
}

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
					"Try the same model from the playground to see whether it answers at all.",
				],
				raw,
			};
		case "access_denied":
			return {
				summary: "Your account doesn't have access to Warp's model.",
				cause:
					"This deployment's governance rules refused the request before it reached the provider. Warp's model calls count as yours, so they need the same access any of your requests would.",
				suggestions: [
					"Ask an administrator to give your account model access, such as an access profile that allows Warp's model.",
					"If you do have access, the details below say which budget, rate limit or rule refused it.",
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
/** Encodes as `code:message`; the leading colon on a code-less error stops the decoder reading it as a code. */
export function encodeTurnError(code: string | undefined, message: string): string {
	return `${code ?? ""}:${message}`;
}

const WARP_ERROR_CODES = new Set([
	"not_configured",
	"upstream_error",
	"access_denied",
	"tool_error",
	"max_iterations",
	"timeout",
	"cancelled",
]);

/** Checks against known codes, since plain messages often contain colons ("TypeError: Failed to fetch"). */
export function isEncodedTurnError(error: string): boolean {
	const separator = error.indexOf(":");
	if (separator === -1) return false;
	const code = error.slice(0, separator);
	return code === "" || WARP_ERROR_CODES.has(code);
}

/** Splits on the first colon only; a string with no colon is a bare message, not a code. */
export function decodeTurnError(error: string): { code: string; message: string } {
	const separator = error.indexOf(":");
	if (separator === -1) return { code: "", message: error.trim() };
	return { code: error.slice(0, separator).trim(), message: error.slice(separator + 1).trim() };
}

// An explicit fence, because guessing which trailing prose is provenance could eat part of the answer.
const WARP_PROVENANCE_FENCE = /\n?```warp-scope\n([\s\S]*?)```\s*$/;

export interface WarpAnswerParts {
	answer: string;
	provenance?: string;
}

export function splitWarpAnswer(content: string): WarpAnswerParts {
	const match = content.match(WARP_PROVENANCE_FENCE);
	if (!match) return { answer: content };

	const provenance = match[1].trim();
	if (provenance === "") return { answer: content };
	return { answer: content.slice(0, match.index).trimEnd(), provenance };
}

/** Formats a turn's token and cost usage, or null when there is nothing to report. */
export function formatWarpUsage(usage: WarpUsage | undefined): string | null {
	if (!usage) return null;

	const parts: string[] = [];
	const total = usage.total_tokens ?? (usage.prompt_tokens ?? 0) + (usage.completion_tokens ?? 0);
	if (total > 0) parts.push(`${total.toLocaleString()} tokens`);

	const cost = usage.cost?.total_cost;
	if (typeof cost === "number" && cost > 0) {
		// Sub-cent costs are common; never round a real charge down to something that reads as free.
		if (cost < 0.0001) {
			parts.push("<$0.0001");
		} else {
			parts.push(cost < 0.01 ? `$${cost.toFixed(4)}` : `$${cost.toFixed(2)}`);
		}
	}

	return parts.length > 0 ? parts.join(" · ") : null;
}
export interface WarpChartPoint {
	x: string;
	/** Display name when x is an id (a team or key id). */
	label?: string;
	y: number;
}

/** A chart from render_chart; the server fills in the spec, so every point is tool-read data, never model-typed. */
export interface WarpChartSpec {
	id: string;
	kind: "line" | "bar";
	title: string;
	metric: string;
	unit: "count" | "usd" | "tokens" | "ms" | "percent";
	interval?: "hour" | "day" | "week";
	group?: string;
	points: WarpChartPoint[];
	window?: { start?: string; end?: string };
	link?: string;
}

export type WarpAnswerSegment =
	| { kind: "text"; text: string }
	| { kind: "chart"; spec: WarpChartSpec }
	| { kind: "chart-pending" }
	| { kind: "chart-invalid" };

// The closing fence must start its own line, since a title can contain triple backticks.
const WARP_CHART_FENCE = /```warp-chart[ \t]*\n([\s\S]*?)^```/gm;
const WARP_CHART_OPEN = "```warp-chart";
const WARP_CHART_UNITS = new Set(["count", "usd", "tokens", "ms", "percent"]);

/** Validates field by field, so a malformed block renders "chart unavailable" instead of crashing the message. */
export function parseWarpChartSpec(raw: string): WarpChartSpec | null {
	let value: unknown;
	try {
		value = JSON.parse(raw);
	} catch {
		return null;
	}
	if (!value || typeof value !== "object") return null;
	const spec = value as Record<string, unknown>;
	if (spec.kind !== "line" && spec.kind !== "bar") return null;
	if (typeof spec.title !== "string" || typeof spec.id !== "string" || typeof spec.metric !== "string") return null;
	if (typeof spec.unit !== "string" || !WARP_CHART_UNITS.has(spec.unit)) return null;
	if (!Array.isArray(spec.points)) return null;
	const points: WarpChartPoint[] = [];
	for (const point of spec.points) {
		if (!point || typeof point !== "object") return null;
		const { x, y, label } = point as Record<string, unknown>;
		if (typeof x !== "string" || typeof y !== "number" || !Number.isFinite(y)) return null;
		points.push(typeof label === "string" && label ? { x, y, label } : { x, y });
	}
	return {
		id: spec.id,
		kind: spec.kind,
		title: spec.title,
		metric: spec.metric,
		unit: spec.unit as WarpChartSpec["unit"],
		interval: spec.interval === "hour" || spec.interval === "day" || spec.interval === "week" ? spec.interval : undefined,
		group: typeof spec.group === "string" ? spec.group : undefined,
		points,
		window: spec.window && typeof spec.window === "object" ? (spec.window as WarpChartSpec["window"]) : undefined,
		link: typeof spec.link === "string" ? spec.link : undefined,
	};
}

/** Splits answer text around chart blocks; an unclosed block is pending while streaming, invalid once finished. */
export function splitWarpCharts(text: string, isStreaming: boolean): WarpAnswerSegment[] {
	const segments: WarpAnswerSegment[] = [];
	const pushText = (value: string) => {
		if (value.trim()) segments.push({ kind: "text", text: value });
	};
	let cursor = 0;
	for (const match of text.matchAll(WARP_CHART_FENCE)) {
		pushText(text.slice(cursor, match.index));
		const spec = parseWarpChartSpec(match[1].trim());
		segments.push(spec ? { kind: "chart", spec } : { kind: "chart-invalid" });
		cursor = (match.index ?? 0) + match[0].length;
	}
	const rest = text.slice(cursor);
	const open = rest.indexOf(WARP_CHART_OPEN);
	if (open === -1) {
		pushText(rest);
	} else if (isStreaming) {
		pushText(rest.slice(0, open));
		segments.push({ kind: "chart-pending" });
	} else {
		pushText(rest.slice(0, open));
		segments.push({ kind: "chart-invalid" });
		// Drop only the stray fence line so any prose after it still shows.
		const lineEnd = rest.indexOf("\n", open);
		if (lineEnd !== -1) pushText(rest.slice(lineEnd + 1));
	}
	return segments;
}

export function formatWarpChartValue(unit: WarpChartSpec["unit"], value: number): string {
	switch (unit) {
		case "usd":
			if (value === 0) return "$0";
			// As in formatWarpUsage: "$0.0000" would read a real cost as free.
			if (value > 0 && value < 0.0001) return "<$0.0001";
			return value < 0.01 ? `$${value.toFixed(4)}` : `$${value.toFixed(2)}`;
		case "ms":
			return value >= 1000 ? `${(value / 1000).toFixed(2)}s` : `${value.toFixed(0)}ms`;
		case "percent":
			return `${Number(value.toFixed(2))}%`;
		default:
			return new Intl.NumberFormat("en-US", { notation: "compact", maximumFractionDigits: 1 }).format(value);
	}
}

/** Time buckets are UTC, so they are labelled in UTC; a local label would file traffic under the wrong hour. */
export function formatWarpChartX(spec: Pick<WarpChartSpec, "kind" | "interval">, point: WarpChartPoint): string {
	if (!spec.interval) return point.label || point.x;
	const date = new Date(point.x);
	if (Number.isNaN(date.getTime())) return point.x;
	if (spec.interval === "week") {
		return `Wk of ${new Intl.DateTimeFormat("en-US", { month: "short", day: "numeric", timeZone: "UTC" }).format(date)}`;
	}
	const options: Intl.DateTimeFormatOptions =
		spec.interval === "hour"
			? { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit", hourCycle: "h23", timeZone: "UTC" }
			: { month: "short", day: "numeric", timeZone: "UTC" };
	return new Intl.DateTimeFormat("en-US", options).format(date);
}