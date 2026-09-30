import {
	encodeTurnError,
	historyForRequest,
	isEncodedTurnError,
	isPartialAnswer,
	parseWarpFrame,
	splitWarpFrames,
	warpTextLength,
	type WarpEvent,
	type WarpQuestion,
	type WarpUsage,
} from "@/components/warp/warpStream.utils";
import type { WarpTurn, WarpTurnToolCall } from "@/lib/contexts/warpContext";
import { getApiBaseUrl } from "@/lib/utils/port";

/** What the panel renders while an answer is in flight, plus the last turn's error. */
export interface WarpStreamSnapshot {
	text: string;
	toolCalls: WarpTurnToolCall[];
	isStreaming: boolean;
	/** Last finished turn's error; reset on every send so it never outlives its turn. */
	error: string | null;
}

/** Where a finished turn goes. Owned by the provider, so it outlives the panel. */
export interface WarpStreamSink {
	commitTurn: (turn: WarpTurn) => void;
	/** The server minted or confirmed the thread id on the done frame. */
	setConversationId: (id: string) => void;
	setQuestion: (question: WarpQuestion | null) => void;
	/** Test seams. Production uses the globals. */
	fetch?: typeof fetch;
	baseUrl?: () => string;
}

export const IDLE_WARP_STREAM: WarpStreamSnapshot = { text: "", toolCalls: [], isStreaming: false, error: null };

/**
 * One Warp request at a time, owned by the provider rather than the panel.
 *
 * Closing the dock unmounts WarpPanel. When the fetch lived in a hook there,
 * unmounting aborted it, so closing the dock mid-answer silently cancelled the
 * answer and left only the question in the thread - while the thread itself,
 * the conversation id and any pending question were all deliberately kept
 * across a close. The request now lives here, created once by the provider:
 * the panel subscribes for token updates and unsubscribes on unmount, and the
 * stream runs on and files its turn through the sink either way.
 *
 * An external store rather than provider state, because the token-by-token
 * text must not go through context: a context update re-renders every
 * consumer, including the topbar button, and doing that on every streamed
 * chunk repaints the dashboard chrome dozens of times a second. Only the panel
 * subscribes here, so only the panel re-renders per token.
 */
export class WarpStreamSession {
	private snapshot: WarpStreamSnapshot = IDLE_WARP_STREAM;
	private readonly listeners = new Set<() => void>();
	private abortController: AbortController | null = null;
	// Lets a superseded request's finally block tell it no longer owns the state.
	private requestId = 0;
	/** The thread the next question is filed under. Empty opens a new one. */
	conversationId = "";

	constructor(private readonly sink: WarpStreamSink) {}

	/** Arrow properties so useSyncExternalStore sees a stable identity. */
	subscribe = (listener: () => void): (() => void) => {
		this.listeners.add(listener);
		return () => {
			this.listeners.delete(listener);
		};
	};

	getSnapshot = (): WarpStreamSnapshot => this.snapshot;

	private patch(next: Partial<WarpStreamSnapshot>) {
		this.snapshot = { ...this.snapshot, ...next };
		for (const listener of this.listeners) listener();
	}

	/** Sets the thread on both sides at once, so send() never reads a stale id. */
	setConversationId = (id: string) => {
		this.conversationId = id;
		this.sink.setConversationId(id);
	};

	/** Aborts and keeps whatever the answer had produced so far. */
	stop = () => {
		this.abortController?.abort();
		this.abortController = null;
	};

	/** Unlike stop(), drops the partial turn: it belongs to the thread being left. */
	discard = () => {
		this.requestId++;
		this.abortController?.abort();
		this.abortController = null;
		this.patch({ text: "", toolCalls: [], isStreaming: false });
	};

	send = async (history: WarpTurn[], question: string): Promise<void> => {
		this.stop();
		const controller = new AbortController();
		this.abortController = controller;
		const requestId = ++this.requestId;
		const isCurrent = () => this.requestId === requestId;

		this.patch({ text: "", toolCalls: [], error: null, isStreaming: true });
		this.sink.setQuestion(null);

		// Tracked locally too: the snapshot is replaced per event, so completion reads these.
		let text = "";
		let toolCalls: WarpTurnToolCall[] = [];
		let terminalError: string | null = null;
		// A clean EOF without a terminal frame means the connection dropped mid-answer.
		let sawTerminal = false;
		let posed: WarpQuestion | null = null;
		let usage: WarpUsage | undefined;
		let partial = false;

		const applyEvent = (event: WarpEvent) => {
			// A read() that resolved before discard() can still deliver frames here.
			if (!isCurrent()) return;
			switch (event.type) {
				case "delta":
					text += event.delta ?? "";
					this.patch({ text });
					break;
				case "tool_call_start":
					// Offset lets the transcript interleave narration and tool calls in order.
					toolCalls = [...toolCalls, { id: event.tool_id ?? "", name: event.tool_name ?? "", textOffset: warpTextLength(text) }];
					this.patch({ toolCalls });
					break;
				case "tool_call_end":
					toolCalls = toolCalls.map((call) =>
						call.id === event.tool_id ? { ...call, durationMs: event.duration_ms, failed: event.failed, error: event.tool_error } : call,
					);
					this.patch({ toolCalls });
					break;
				case "done":
					sawTerminal = true;
					usage = event.usage;
					partial = isPartialAnswer(event.finish_reason);
					// The server mints the id for a new thread; this is the only place the client learns it.
					if (event.conversation_id) this.setConversationId(event.conversation_id);
					break;
				case "question":
					posed = event.question ?? null;
					this.sink.setQuestion(posed);
					break;
				case "error":
					// Terminal. Always encoded so a colon in a code-less message isn't read as a code.
					terminalError = encodeTurnError(event.code, event.message ?? (event.code ? "" : "error"));
					sawTerminal = true;
					break;
				default:
					break;
			}
		};

		const doFetch = this.sink.fetch ?? fetch;
		const baseUrl = (this.sink.baseUrl ?? getApiBaseUrl)();
		try {
			const response = await doFetch(`${baseUrl}/warp/chat`, {
				method: "POST",
				credentials: "include",
				headers: { "Content-Type": "application/json" },
				signal: controller.signal,
				body: JSON.stringify({
					// Drop empty failed turns: Anthropic rejects empty text content blocks.
					messages: [
						...historyForRequest(history)
							// Lets the server cap how many times in a row Warp asks instead of answering.
							.map((turn) => ({
								role: turn.role,
								content: turn.content,
								...(turn.role === "assistant" && turn.question ? { question: true } : {}),
							})),
						{ role: "user", content: question },
					],
					// Omitted on a chat's first message so the server opens a new thread.
					conversation_id: this.conversationId || undefined,
					stream: true,
					// Sent every turn: named dates need the IANA zone, since DST can differ from today's offset.
					timezone: Intl.DateTimeFormat().resolvedOptions().timeZone,
					// Only labels the current time; named dates resolve against timezone.
					utc_offset_minutes: -new Date().getTimezoneOffset(),
				}),
			});

			if (!response.ok) {
				let reason = "";
				try {
					reason = ((await response.json()) as { reason?: string }).reason ?? "";
				} catch {
					reason = "";
				}
				throw new Error(encodeTurnError(reason || undefined, reason ? "" : `Warp request failed (${response.status})`));
			}

			const reader = response.body?.getReader();
			if (!reader) throw new Error(encodeTurnError(undefined, "Warp returned no response body"));

			const decoder = new TextDecoder();
			let buffer = "";
			for (;;) {
				const { done, value } = await reader.read();
				if (done) {
					if (!sawTerminal) {
						throw new Error(encodeTurnError("upstream_error", "The connection closed before Warp finished answering."));
					}
					break;
				}
				buffer += decoder.decode(value, { stream: true });
				const { frames, rest } = splitWarpFrames(buffer);
				buffer = rest;
				for (const frame of frames) {
					const event = parseWarpFrame(frame);
					if (event) applyEvent(event);
				}
			}
		} catch (caught) {
			// An abort is the user pressing stop: keep the partial answer.
			if (!(caught instanceof DOMException && caught.name === "AbortError")) {
				const raw = caught instanceof Error ? caught.message : "Warp request failed";
				// Checked against known codes, not a colon: "TypeError: Failed to fetch" is not encoded.
				terminalError = isEncodedTurnError(raw) ? raw : encodeTurnError(undefined, raw);
			}
		} finally {
			// Guard on request id, not the controller: discard() bumps the id before aborting.
			if (isCurrent()) {
				this.abortController = null;
				// A question turn often has no text; fall back to the question so history keeps it.
				// Cast: TS narrows the closure-assigned `posed` to never here.
				const content = text.trim() !== "" ? text : ((posed as WarpQuestion | null)?.question ?? "");
				this.sink.commitTurn({
					role: "assistant",
					content,
					toolCalls: toolCalls.length > 0 ? toolCalls : undefined,
					error: terminalError ?? undefined,
					partial: partial || undefined,
					question: posed ?? undefined,
					usage,
				});
				this.patch({ text: "", toolCalls: [], isStreaming: false, error: terminalError });
			}
		}
	};
}