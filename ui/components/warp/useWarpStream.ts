import { IDLE_WARP_STREAM, type WarpStreamSnapshot } from "@/components/warp/warpStreamSession";
import type { WarpQuestion } from "@/components/warp/warpStream.utils";
import { useWarp, type WarpTurn, type WarpTurnToolCall } from "@/lib/contexts/warpContext";
import { useCallback, useSyncExternalStore } from "react";

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

// Outside a provider there is nothing to stream; these keep the hook order stable.
const subscribeToNothing = () => () => {};
const getIdleSnapshot = (): WarpStreamSnapshot => IDLE_WARP_STREAM;

/**
 * The panel's view of the provider-owned stream (see WarpStreamSession).
 *
 * Nothing here owns the request: mounting subscribes to the session and
 * unmounting only unsubscribes, so closing the dock mid-answer lets the answer
 * finish and land in the thread. Only this hook's subscribers re-render per
 * token; the provider's context value never changes for one.
 */
export function useWarpStream(): UseWarpStreamResult {
	const warp = useWarp();
	const session = warp?.stream ?? null;
	const snapshot = useSyncExternalStore(
		session?.subscribe ?? subscribeToNothing,
		session?.getSnapshot ?? getIdleSnapshot,
		session?.getSnapshot ?? getIdleSnapshot,
	);
	const question = warp?.question ?? null;
	const setQuestion = warp?.setQuestion;

	const send = useCallback((history: WarpTurn[], text: string) => session?.send(history, text) ?? Promise.resolve(), [session]);
	const stop = useCallback(() => session?.stop(), [session]);
	const discard = useCallback(() => session?.discard(), [session]);
	const clearQuestion = useCallback(() => setQuestion?.(null), [setQuestion]);

	const resetConversation = useCallback(() => {
		// Discard first, or an in-flight request writes its turn into the just-cleared transcript.
		session?.discard();
		session?.setConversationId("");
	}, [session]);

	const openConversation = useCallback(
		(id: string) => {
			session?.setConversationId(id);
			// The pending question belongs to the thread being left; its answer would be misfiled.
			setQuestion?.(null);
		},
		[session, setQuestion],
	);

	return {
		discard,
		streamingText: snapshot.text,
		streamingToolCalls: snapshot.toolCalls,
		isStreaming: snapshot.isStreaming,
		error: snapshot.error,
		question,
		clearQuestion,
		send,
		stop,
		resetConversation,
		openConversation,
	};
}