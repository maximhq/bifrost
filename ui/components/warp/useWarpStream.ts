import { parseWarpFrame, splitWarpFrames, type WarpEvent, type WarpQuestion, type WarpUsage } from "@/components/warp/warpStream.utils";
import type { WarpTurn, WarpTurnToolCall } from "@/lib/contexts/warpContext";
import { getApiBaseUrl } from "@/lib/utils/port";
import { useCallback, useRef, useState } from "react";

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
	/** Terminal error for the in-flight turn, if it failed. */
	error: string | null;
	/** Set when Warp ended its turn by asking something. */
	question: WarpQuestion | null;
	clearQuestion: () => void;
	send: (history: WarpTurn[], question: string) => Promise<void>;
	stop: () => void;
	/** Forgets the current thread, so the next question opens a new one. */
	resetConversation: () => void;
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
	const [question, setQuestion] = useState<WarpQuestion | null>(null);
	const abortRef = useRef<AbortController | null>(null);
	// The thread every message in this chat belongs to. A ref rather than state
	// because nothing renders from it and the next send has to read it
	// synchronously - a state setter would still be pending, and the follow-up
	// question would open a second thread.
	//
	// It also travels upstream as a log label, so the model calls behind one
	// conversation can be grouped in the LLM Logs view instead of appearing as
	// unrelated requests.
	const conversationRef = useRef<string>("");

	const stop = useCallback(() => {
		abortRef.current?.abort();
		abortRef.current = null;
	}, []);

	const send = useCallback(
		async (history: WarpTurn[], question: string) => {
			stop();
			const controller = new AbortController();
			abortRef.current = controller;

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
			let posed: WarpQuestion | null = null;
			let usage: WarpUsage | undefined;

			const applyEvent = (event: WarpEvent) => {
				switch (event.type) {
					case "delta":
						text += event.delta ?? "";
						setStreamingText(text);
						break;
					case "tool_call_start":
						toolCalls = [...toolCalls, { id: event.tool_id ?? "", name: event.tool_name ?? "" }];
						setStreamingToolCalls(toolCalls);
						break;
					case "tool_call_end":
						toolCalls = toolCalls.map((call) =>
							call.id === event.tool_id ? { ...call, durationMs: event.duration_ms, failed: event.failed, error: event.tool_error } : call,
						);
						setStreamingToolCalls(toolCalls);
						break;
					case "done":
						usage = event.usage;
						// The server mints the id when a thread is new, so this is the only
						// place the client learns it.
						if (event.conversation_id) conversationRef.current = event.conversation_id;
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
						terminalError = event.code ? `${event.code}:${event.message ?? ""}` : (event.message ?? "error");
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
							...history.filter((turn) => turn.content.trim() !== "").map((turn) => ({ role: turn.role, content: turn.content })),
							{ role: "user", content: question },
						],
						// Omitted on the first message of a chat, which is what tells the
						// server to open a thread rather than append to one.
						conversation_id: conversationRef.current || undefined,
						stream: true,
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
					throw new Error(reason ? `${reason}:` : `Warp request failed (${response.status})`);
				}

				const reader = response.body?.getReader();
				if (!reader) throw new Error("Warp returned no response body");

				const decoder = new TextDecoder();
				let buffer = "";
				for (;;) {
					const { done, value } = await reader.read();
					if (done) break;
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
					terminalError = caught instanceof Error ? caught.message : "Warp request failed";
				}
			} finally {
				setIsStreaming(false);
				abortRef.current = null;
				onTurnComplete({
					role: "assistant",
					content: text,
					toolCalls: toolCalls.length > 0 ? toolCalls : undefined,
					error: terminalError ?? undefined,
					// Recorded on the turn so a reopened thread shows the question that
					// was asked, not just the gap where an answer would be.
					question: posed ?? undefined,
					usage,
				});
				setStreamingText("");
				setStreamingToolCalls([]);
			}
		},
		[onTurnComplete, stop],
	);

	const clearQuestion = useCallback(() => setQuestion(null), []);

	const resetConversation = useCallback(() => {
		conversationRef.current = "";
	}, []);

	return { streamingText, streamingToolCalls, isStreaming, error, question, clearQuestion, send, stop, resetConversation };
}