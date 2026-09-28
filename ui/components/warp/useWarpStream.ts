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
	onTurnComplete: (turn: WarpTurn) => void;
}

interface UseWarpStreamResult {
	streamingText: string;
	streamingToolCalls: WarpTurnToolCall[];
	isStreaming: boolean;
	/** Last finished turn's error; reset on every send so it never outlives its turn. */
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

/** Streaming state stays here, not in WarpProvider: per-token context updates would repaint the dashboard. */
export function useWarpStream({ onTurnComplete }: UseWarpStreamOptions): UseWarpStreamResult {
	const [streamingText, setStreamingText] = useState("");
	const [streamingToolCalls, setStreamingToolCalls] = useState<WarpTurnToolCall[]>([]);
	const [isStreaming, setIsStreaming] = useState(false);
	const [error, setError] = useState<string | null>(null);
	const abortRef = useRef<AbortController | null>(null);
	// Lets a superseded request's finally block tell it no longer owns the stream state.
	const requestIdRef = useRef(0);
	// Ref mirror of the context value so send() reads it synchronously; context survives panel unmount.
	const warp = useWarp();
	const conversationRef = useRef<string>(warp?.conversationId ?? "");
	useEffect(() => {
		conversationRef.current = warp?.conversationId ?? "";
	}, [warp?.conversationId]);
	const setConversationID = warp?.setConversationId;
	// Held by the provider so a pending question survives the dock closing and unmounting this hook.
	const question = warp?.question ?? null;
	const setQuestion = useCallback((next: WarpQuestion | null) => warp?.setQuestion(next), [warp]);

	// Bump the id before aborting so a stale request can't commit a turn after unmount.
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

	// Unlike stop(), discard drops the partial turn: it belongs to the thread being left.
	const discard = useCallback(() => {
		requestIdRef.current++;
		abortRef.current?.abort();
		abortRef.current = null;
		setStreamingText("");
		setStreamingToolCalls([]);
		setIsStreaming(false);
	}, []);

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

			// Tracked locally too: state setters are async, so completion can't read them back.
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
						setStreamingText(text);
						break;
					case "tool_call_start":
						// Offset lets the transcript interleave narration and tool calls in order.
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
						sawTerminal = true;
						usage = event.usage;
						partial = isPartialAnswer(event.finish_reason);
						// The server mints the id for a new thread; this is the only place the client learns it.
						if (event.conversation_id) {
							conversationRef.current = event.conversation_id;
							setConversationID?.(event.conversation_id);
						}
						break;
					case "question":
						posed = event.question ?? null;
						setQuestion(posed);
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

			try {
				const response = await fetch(`${getApiBaseUrl()}/warp/chat`, {
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
						conversation_id: conversationRef.current || undefined,
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
				// Guard on request id, not abortRef: discard() and unmount bump the id before aborting.
				if (isCurrent()) {
					setIsStreaming(false);
					abortRef.current = null;
					setError(terminalError);
					// A question turn often has no text; fall back to the question so history keeps it.
					// Cast: TS narrows the closure-assigned `posed` to never here.
					const content = text.trim() !== "" ? text : ((posed as WarpQuestion | null)?.question ?? "");
					onTurnComplete({
						role: "assistant",
						content,
						toolCalls: toolCalls.length > 0 ? toolCalls : undefined,
						error: terminalError ?? undefined,
						partial: partial || undefined,
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
		// Discard first, or an in-flight request writes its turn into the just-cleared transcript.
		discard();
		conversationRef.current = "";
		setConversationID?.("");
	}, [discard, setConversationID]);

	const openConversation = useCallback(
		(id: string) => {
			conversationRef.current = id;
			setConversationID?.(id);
			// The pending question belongs to the thread being left; its answer would be misfiled.
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