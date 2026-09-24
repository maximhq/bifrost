import type { WarpQuestion, WarpUsage } from "@/components/warp/warpStream.utils";
import { useFeatureFlag } from "@/hooks/useFeatureFlag";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { FEATURE_FLAGS } from "@/lib/constants/featureFlags";
import { createContext, useCallback, useContext, useMemo, useState } from "react";

/** One turn in an Warp conversation. */
export interface WarpTurn {
	role: "user" | "assistant";
	content: string;
	/** Tool calls Warp made while producing this turn. Assistant turns only. */
	toolCalls?: WarpTurnToolCall[];
	/** Set when the turn ended in an error, so the UI can render it differently. */
	error?: string;
	/**
	 * Set when Warp answered on its last research step without settling. The
	 * answer is shown, labelled as partial, so what it could not check is read
	 * as a gap rather than as a finding.
	 */
	partial?: boolean;
	/**
	 * On a user turn, the question this message answers.
	 *
	 * Kept so a bare "-7d" in the transcript stays legible: on its own it reads
	 * as a non sequitur, and the question that prompted it has already scrolled
	 * out of the composer.
	 */
	answeredQuestion?: string;
	/**
	 * What to show instead of `content`.
	 *
	 * Picking an option sends the hint Warp needs ("-30d") but that is not what
	 * anyone chose - they chose "Last 30 days". Showing the wire value makes the
	 * transcript read like a machine log of your own conversation.
	 */
	displayContent?: string;
	/**
	 * Tokens and spend for this exchange.
	 *
	 * Warp's own calls never appear in the logs it reads - deliberately, so it
	 * does not corrupt the numbers it reports - so this is the only place its
	 * cost is visible at all.
	 */
	usage?: WarpUsage;
	/**
	 * Set when the turn ended by asking something rather than answering.
	 *
	 * WarpQuestion rather than unknown: send() replays this as `question: true`
	 * so the server does not count the reply as a fresh question, and an
	 * unchecked field is how thread reconstruction came to omit it entirely.
	 */
	question?: WarpQuestion;
}

export interface WarpTurnToolCall {
	id: string;
	name: string;
	durationMs?: number;
	failed?: boolean;
	/** Why it failed, kept so a red tick can account for itself. */
	error?: string;
	/**
	 * How much of the answer had been written when this call started, in
	 * Unicode code points - the unit the server counts in too. It is what places
	 * the call in the transcript; absent, the call sorts ahead of all the text.
	 */
	textOffset?: number;
}

interface WarpContextValue {
	isOpen: boolean;
	open: () => void;
	close: () => void;
	toggle: () => void;
	/** Completed turns. The in-flight answer lives in the panel, not here. */
	turns: WarpTurn[];
	appendTurn: (turn: WarpTurn) => void;
	replaceTurns: (turns: WarpTurn[]) => void;
	clear: () => void;
	/**
	 * The server-side thread these turns belong to.
	 *
	 * It lives here rather than in the streaming hook because closing the dock
	 * unmounts the panel: a ref in the hook died with it, so reopening replayed
	 * the transcript but opened a second thread server-side, and one
	 * conversation ended up filed as two.
	 */
	conversationId: string;
	/**
	 * The question Warp is waiting on, if any.
	 *
	 * Provider state rather than panel state for the same reason the thread id
	 * is: closing the dock unmounts WarpPanel, and a question held in the hook
	 * died with it - so reopening showed a thread that had visibly asked
	 * something with no way left to answer it.
	 */
	question: WarpQuestion | null;
	setQuestion: (question: WarpQuestion | null) => void;
	setConversationId: (id: string) => void;
}

const WarpContext = createContext<WarpContextValue | null>(null);

/**
 * Holds Warp's cross-view state: whether the dock is open, and the conversation
 * so far.
 *
 * It deliberately holds only slow-moving values. The token-by-token answer stays
 * in local state inside the panel, because a context update re-renders every
 * consumer — including the topbar button — and doing that on every streamed
 * chunk would make the whole dashboard chrome repaint dozens of times a second.
 *
 * The conversation is in memory only. It survives navigation between views,
 * which is the point of the dock, but not a reload. Server-side persistence is a
 * separate feature with its own storage and retention questions.
 *
 * While the "warp" feature flag is off, or the caller lacks WarpSession View or
 * Warp View, the provider publishes null, which every consumer already treats
 * as "no Warp here": the launcher, the ⌘I shortcut and the dock all stand down.
 * The provider itself stays mounted either way - swapping it for a bare
 * fragment would change the element type above the whole dashboard and remount
 * every page when the flag query resolves.
 */
export function WarpProvider({ children }: { children: React.ReactNode }) {
	const isWarpFlagOn = useFeatureFlag(FEATURE_FLAGS.warp);
	const hasWarpSessionAccess = useRbac(RbacResource.WarpSession, RbacOperation.View);
	// The panel reads GET /api/warp/config to tell "not set up" from "ready", which is gated on Warp View.
	const hasWarpConfigReadAccess = useRbac(RbacResource.Warp, RbacOperation.View);
	const isWarpEnabled = isWarpFlagOn && hasWarpSessionAccess && hasWarpConfigReadAccess;
	const [isOpen, setIsOpen] = useState(false);
	const [turns, setTurns] = useState<WarpTurn[]>([]);
	const [conversationId, setConversationId] = useState("");
	const [question, setQuestion] = useState<WarpQuestion | null>(null);

	const open = useCallback(() => setIsOpen(true), []);
	const close = useCallback(() => setIsOpen(false), []);
	const toggle = useCallback(() => setIsOpen((current) => !current), []);
	const appendTurn = useCallback((turn: WarpTurn) => setTurns((current) => [...current, turn]), []);
	const replaceTurns = useCallback((next: WarpTurn[]) => setTurns(next), []);
	// Clearing starts a new thread as well as a new transcript, or the next
	// question would be appended to the conversation just discarded.
	const clear = useCallback(() => {
		setTurns([]);
		setConversationId("");
		// The pending question goes with the transcript that produced it. Leaving
		// it set kept the question card on screen above an empty thread, and
		// answering it sent the reply into a new conversation without any of the
		// context that made it a sensible question.
		setQuestion(null);
	}, []);

	const value = useMemo(
		() => ({
			isOpen,
			open,
			close,
			toggle,
			turns,
			appendTurn,
			replaceTurns,
			clear,
			conversationId,
			setConversationId,
			question,
			setQuestion,
		}),
		[isOpen, open, close, toggle, turns, appendTurn, replaceTurns, clear, conversationId, question],
	);
	return <WarpContext.Provider value={isWarpEnabled ? value : null}>{children}</WarpContext.Provider>;
}

/**
 * Read/write access to the dock.
 *
 * Returns null outside a provider rather than throwing, so the topbar still
 * renders on the minimal shells (login, temp-token pages) that deliberately do
 * not mount Warp.
 */
export function useWarp(): WarpContextValue | null {
	return useContext(WarpContext);
}