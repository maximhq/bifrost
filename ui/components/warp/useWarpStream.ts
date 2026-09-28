import {
	encodeTurnError,
	historyForRequest,
	isEncodedTurnError,
	isUsableWarpEvent,
	parseWarpFrame,
	splitWarpFrames,
	type WarpEvent,
	type WarpQuestion,
	type WarpUsage,
	isPartialAnswer,
	warpTextLength,
} from "@/components/warp/warpStream.utils";
import { useWarp, type WarpTurn, type WarpTurnToolCall } from "@/lib/contexts/warpContext";
import { getApiBaseUrl } from "@/lib/utils/port";
import { useCallback, useEffect, useRef, useState } from "react";

interface UseWarpStreamOptions {
	/** Called once the turn is complete, so the finished turn can join the conversation. */
	onTurnComplete: (turn: WarpTurn) => void;
}

interface UseWarpStreamResult {
	/** Text streamed so far for the in-flight answer. */
	streamingText: string;
	/** Tool calls made during the in-flight answer, in order. */
	streamingToolCalls: WarpTurnToolCall[];
	isStreaming: boolean;
	/**
	 * Terminal error from the most recently finished turn, if it failed. Reset
	 * to null at the start of every send, so it never outlives the turn it
	 * belongs to.
	 */
	error: string | null;
	/** Set when Warp ended its turn by asking something. */
	question: WarpQuestion | null;
	clearQuestion: () => void;
	send: (history: WarpTurn[], question: string) => Promise<void>;
	stop: () => void;
	/** Abort and drop whatever the aborted request produced. */
	discard: () => void;
	/** Forgets the current thread, so the next question opens a new one. */
	resetConversation: () => void;
	/** Continues a stored thread: the next question is filed under it. */
	openConversation: (id: string) => void;
}

/**
 * Drives one Warp request and exposes the in-flight answer.
 *
 * The streaming text lives here rather than in WarpProvider on purpose: a
 * context update re-renders every consumer, and pushing a state change per token
 * would repaint the topbar and the whole dashboard chrome dozens of times a
 * second. Only the finished turn is handed upward.
 */
export function useWarpStream({ onTurnComplete }: UseWarpStreamOptions): UseWarpStreamResult {
	const [streamingText, setStreamingText] = useState("");
	const [streamingToolCalls, setStreamingToolCalls] = useState<WarpTurnToolCall[]>([]);
	const [isStreaming, setIsStreaming] = useState(false);
	const [error, setError] = useState<string | null>(null);
	const abortRef = useRef<AbortController | null>(null);
	// Identifies the request currently allowed to own the stream state. A second
	// send aborts the first, but the first still runs its finally block after
	// that - and without this guard it would null out the *new* controller
	// (leaving the live request unstoppable), flip isStreaming off underneath it,
	// and commit its own abandoned turn into the transcript.
	const requestIdRef = useRef(0);
	// The thread every message in this chat belongs to. A ref rather than state
	// because nothing renders from it and the next send has to read it
	// synchronously - a state setter would still be pending, and the follow-up
	// question would open a second thread.
	//
	// It also travels upstream as a log label, so the model calls behind one
	// conversation can be grouped in the LLM Logs view instead of appearing as
	// unrelated requests.
	// Mirrors the context value into a ref so send() can read it synchronously
	// without re-creating the callback on every turn. The context is the source
	// of truth, because it outlives this hook: the panel unmounts when the dock
	// closes, and a ref alone lost the thread every time.
	const warp = useWarp();
	const conversationRef = useRef<string>(warp?.conversationId ?? "");
	useEffect(() => {
		conversationRef.current = warp?.conversationId ?? "";
	}, [warp?.conversationId]);
	const setConversationID = warp?.setConversationId;
	// Held by the provider, not here: closing the dock unmounts this hook, and a
	// question that died with it left a thread that had visibly asked something
	// with no way left to answer.
	const question = warp?.question ?? null;
	const setQuestion = useCallback((next: WarpQuestion | null) => warp?.setQuestion(next), [warp]);

	// Unmounting must invalidate and abort. Without this the fetch kept running
	// after the sheet closed and its completion path could append a turn to a
	// hook nobody was reading any more - and the next open builds a fresh hook
	// whose request guard knows nothing about the old request.
	useEffect(() => {
		return () => {
			requestIdRef.current++;
			abortRef.current?.abort();
			abortRef.current = null;
		};
	}, []);

	const stop = useCallback(() => {
		abortRef.current?.abort();
		abortRef.current = null;
	}, []);

	// discard is stop plus "and do not keep what it produced".
	//
	// The composer's Stop wants the opposite: a half-written answer is usually
	// still worth reading, so stop() leaves the request current and its finally
	// block files the partial turn. Switching or deleting a thread is the case
	// where that turn must not land - it belongs to the conversation being left,
	// and committing it files someone's answer under whichever thread is
	// selected by the time the abort unwinds.
	const discard = useCallback(() => {
		requestIdRef.current++;
		abortRef.current?.abort();
		abortRef.current = null;
		setStreamingText("");
		setStreamingToolCalls([]);
		setIsStreaming(false);
	}, []);

	// Unmount is the one exit nothing else covers. Closing the desktop dock
	// unmounts WarpPanel with a request still in flight, and no parent cancels
	// it - so it ran to its finally, passed isCurrent(), and called
	// onTurnComplete on a component that no longer exists.
	//
	// The id is bumped before the abort, for the same reason discard() does it:
	// that is what makes every later guard on the request see a stale id and
	// decline to write, rather than racing the abort.
	useEffect(() => {
		return () => {
			requestIdRef.current++;
			abortRef.current?.abort();
			abortRef.current = null;
		};
	}, []);

	const send = useCallback(
		async (history: WarpTurn[], question: string) => {
			stop();
			const controller = new AbortController();
			abortRef.current = controller;
			const requestId = ++requestIdRef.current;
			const isCurrent = () => requestIdRef.current === requestId;

			setStreamingText("");
			setStreamingToolCalls([]);
			setError(null);
			setQuestion(null);
			setIsStreaming(true);

			// Accumulated locally as well as in state: the state setters are async,
			// so the completion handler cannot read them back to build the turn.
			let text = "";
			let toolCalls: WarpTurnToolCall[] = [];
			let terminalError: string | null = null;
			// Whether the server said it was finished. A clean EOF is not the same
			// thing: a proxy or a server that drops the connection mid-answer closes
			// the body just as tidily, and reading that as success committed a
			// truncated answer - or silently dropped the turn when nothing had
			// arrived yet - with nothing to tell the reader either happened.
			let sawTerminal = false;
			let posed: WarpQuestion | null = null;
			let usage: WarpUsage | undefined;
			let partial = false;

			const applyEvent = (event: WarpEvent) => {
				// A read() that resolved before discard() can still deliver its frames
				// into this loop, and every branch below writes state the live request
				// owns - so a superseded stream could repopulate the transcript after
				// it was cleared, or overwrite the answer now being streamed.
				if (!isCurrent()) return;
				switch (event.type) {
					case "delta":
						text += event.delta ?? "";
						setStreamingText(text);
						break;
					case "tool_call_start":
						// Where in the answer this call fell, so the transcript can show
						// narration and lookups in the order they happened.
						toolCalls = [...toolCalls, { id: event.tool_id ?? "", name: event.tool_name ?? "", textOffset: warpTextLength(text) }];
						setStreamingToolCalls(toolCalls);
						break;
					case "tool_call_end":
						toolCalls = toolCalls.map((call) =>
							call.id === event.tool_id ? { ...call, durationMs: event.duration_ms, failed: event.failed, error: event.tool_error } : call,
						);
						setStreamingToolCalls(toolCalls);
						break;
					case "done":
						// Terminal, and set here rather than in a second `case "done"`:
						// a duplicate case later in the same switch is unreachable, so
						// sawTerminal stayed false after every successful stream and the
						// EOF check then threw "the connection closed before Warp
						// finished answering" over a turn that had finished perfectly.
						sawTerminal = true;
						usage = event.usage;
						partial = isPartialAnswer(event.finish_reason);
						// The server mints the id when a thread is new, so this is the only
						// place the client learns it.
						if (event.conversation_id) {
							conversationRef.current = event.conversation_id;
							setConversationID?.(event.conversation_id);
						}
						break;
					case "question":
						// The turn ends here; the answer goes back as an ordinary next
						// message, so nothing needs to stay open waiting.
						posed = event.question ?? null;
						setQuestion(posed);
						break;
					case "error":
						// An error frame is terminal on the server side and never followed
						// by done, so this is the end of the turn.
						// Encoded either way. A code-less frame whose message contains a
						// colon would otherwise have its first word read back as a code.
						terminalError = encodeTurnError(event.code, event.message ?? (event.code ? "" : "error"));
						sawTerminal = true;
						break;
					default:
						break;
				}
			};

			try {
				const response = await fetch(`${getApiBaseUrl()}/warp/chat`, {
					method: "POST",
					credentials: "include",
					headers: { "Content-Type": "application/json" },
					signal: controller.signal,
					body: JSON.stringify({
						// Failed turns are kept in the transcript so the error card stays
						// visible, but they carry no text - and replaying an empty
						// assistant message is what Anthropic rejects with "text content
						// blocks must be non-empty". The server drops these too; filtering
						// here keeps them out of the request body in the first place.
						messages: [
							...historyForRequest(history)
								// question marks an assistant turn that asked rather than
								// answered. The server counts these to cap how many times in a
								// row Warp may ask instead of answering, and it has no other
								// way to tell the two apart once the turn is replayed as text.
								.map((turn) => ({
									role: turn.role,
									content: turn.content,
									...(turn.role === "assistant" && turn.question ? { question: true } : {}),
								})),
							{ role: "user", content: question },
						],
						// Omitted on the first message of a chat, which is what tells the
						// server to open a thread rather than append to one.
						conversation_id: conversationRef.current || undefined,
						stream: true,
						// The asker's IANA zone (e.g. "Asia/Kolkata"), sent every turn rather
						// than once per thread - a laptop can cross timezones mid
						// conversation. A named date ("on sept 3rd") needs the zone's
						// identity, not just today's numeric offset: daylight saving can put
						// that date at a different offset than right now, and only the zone
						// name lets the server work that out per-date instead of reusing a
						// stale snapshot.
						timezone: Intl.DateTimeFormat().resolvedOptions().timeZone,
						// Minutes east of UTC right now (e.g. +330 for IST). Kept only so the
						// server can label "the current time is ..." without a tzdata lookup;
						// the timezone above, not this, is what a named date is resolved
						// against.
						utc_offset_minutes: -new Date().getTimezoneOffset(),
					}),
				});

				if (!response.ok) {
					// 503 carries a machine-readable reason so the panel can distinguish
					// "not set up yet" from a real failure.
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
				// An abort is the user pressing stop, not a failure. The partial answer
				// is kept, because a half-written answer is often still useful.
				if (!(caught instanceof DOMException && caught.name === "AbortError")) {
					const raw = caught instanceof Error ? caught.message : "Warp request failed";
					// Encoded unless it already is. Checked against the known codes
					// rather than "has a colon", because a raw fetch failure reads
					// "TypeError: Failed to fetch" and its first word is not a code.
					terminalError = isEncodedTurnError(raw) ? raw : encodeTurnError(undefined, raw);
				}
			} finally {
				// A superseded request must leave the live one alone: no clearing its
				// controller, no flipping its streaming flag, and above all no
				// committing an abandoned turn into a transcript that has moved on.
				// The guard is the request id rather than a comparison against
				// abortRef.current: discard() and the unmount effect both bump the id
				// before aborting, precisely so every later guard on the request sees
				// a stale id and declines to write, and a controller comparison cannot
				// see either of those exits. A plain `if` rather than an early return,
				// since returning from a finally block silently discards whatever the
				// try/catch above was doing.
				if (isCurrent()) {
					setIsStreaming(false);
					abortRef.current = null;
					// The hook advertises `error` in its result, and only ever set it to
					// null - so a failed turn reached onTurnComplete but left the hook
					// reporting no error at all. Set on the current request's completion
					// path only, for the same reason everything else here is: a
					// superseded request must not write into state the live one owns.
					setError(terminalError);
					// A turn that ended by asking usually carries no narration text - Warp
					// is told to ask about one thing and stop - so falling back to the
					// question itself is what the server already does when it persists
					// this same turn (see recordTurn in history.go). Without this, the
					// turn's content stays empty: the next request's history filters it
					// out entirely (see the messages mapping earlier in this function on
					// the next send()), and the model sees
					// its own answer arrive with no question in between - two bare user
					// turns, and no way to tell what it had asked.
					//
					// The cast is a TypeScript control-flow quirk, not a real type gap:
					// `posed` is reassigned inside applyEvent, a closure invoked from the
					// try block above, and TS narrows it to `never` in this finally block
					// as a result - asserting the declared type back is the standard,
					// compile-time-only fix.
					//
					// Whitespace-only text falls back the same as empty text: a model that
					// streamed nothing but a stray space or newline before asking has still
					// carried no real narration, and treating that as "real" text would skip
					// the question fallback above for the same empty-turn failure it exists
					// to prevent.
					const content = text.trim() !== "" ? text : ((posed as WarpQuestion | null)?.question ?? "");
					onTurnComplete({
						role: "assistant",
						content,
						toolCalls: toolCalls.length > 0 ? toolCalls : undefined,
						error: terminalError ?? undefined,
						partial: partial || undefined,
						// Recorded on the turn so a reopened thread shows the question that
						// was asked, not just the gap where an answer would be.
						question: posed ?? undefined,
						usage,
					});
					setStreamingText("");
					setStreamingToolCalls([]);
				}
			}
		},
		[onTurnComplete, stop],
	);

	const clearQuestion = useCallback(() => setQuestion(null), []);

	const resetConversation = useCallback(() => {
		// Discards first. Starting a fresh thread means abandoning whatever the
		// old one was producing: without this, an in-flight request survives the
		// reset, its finally block still passes isCurrent(), and appendTurn writes
		// the abandoned assistant turn into the transcript that was just cleared.
		// Doing it here rather than only at the New Chat button covers Clear too.
		discard();
		conversationRef.current = "";
		setConversationID?.("");
	}, [discard, setConversationID]);

	const openConversation = useCallback(
		(id: string) => {
			conversationRef.current = id;
			setConversationID?.(id);
			// The pending question belongs to the thread being left. Leaving it on
			// screen means its answer is sent with the newly selected conversation
			// id, filing a reply under a thread that never asked.
			setQuestion(null);
		},
		[setConversationID],
	);

	return {
		discard,
		streamingText,
		streamingToolCalls,
		isStreaming,
		error,
		question,
		clearQuestion,
		send,
		stop,
		resetConversation,
		openConversation,
	};
}