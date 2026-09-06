import type { WarpUsage } from "@/components/warp/warpStream.utils";
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
	/** Set when the turn ended by asking something rather than answering. */
	question?: unknown;
}

export interface WarpTurnToolCall {
	id: string;
	name: string;
	durationMs?: number;
	failed?: boolean;
	/** Why it failed, kept so a red tick can account for itself. */
	error?: string;
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
 */
export function WarpProvider({ children }: { children: React.ReactNode }) {
	const [isOpen, setIsOpen] = useState(false);
	const [turns, setTurns] = useState<WarpTurn[]>([]);

	const open = useCallback(() => setIsOpen(true), []);
	const close = useCallback(() => setIsOpen(false), []);
	const toggle = useCallback(() => setIsOpen((current) => !current), []);
	const appendTurn = useCallback((turn: WarpTurn) => setTurns((current) => [...current, turn]), []);
	const replaceTurns = useCallback((next: WarpTurn[]) => setTurns(next), []);
	const clear = useCallback(() => setTurns([]), []);

	const value = useMemo(
		() => ({ isOpen, open, close, toggle, turns, appendTurn, replaceTurns, clear }),
		[isOpen, open, close, toggle, turns, appendTurn, replaceTurns, clear],
	);
	return <WarpContext.Provider value={value}>{children}</WarpContext.Provider>;
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