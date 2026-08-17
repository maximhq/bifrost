import { createContext, useCallback, useContext, useMemo, useState } from "react";

/** One turn in an Odin conversation. */
export interface OdinTurn {
	role: "user" | "assistant";
	content: string;
	/** Tool calls Odin made while producing this turn. Assistant turns only. */
	toolCalls?: OdinTurnToolCall[];
	/** Set when the turn ended in an error, so the UI can render it differently. */
	error?: string;
}

export interface OdinTurnToolCall {
	id: string;
	name: string;
	durationMs?: number;
	failed?: boolean;
}

interface OdinContextValue {
	isOpen: boolean;
	open: () => void;
	close: () => void;
	toggle: () => void;
	/** Completed turns. The in-flight answer lives in the panel, not here. */
	turns: OdinTurn[];
	appendTurn: (turn: OdinTurn) => void;
	replaceTurns: (turns: OdinTurn[]) => void;
	clear: () => void;
}

const OdinContext = createContext<OdinContextValue | null>(null);

/**
 * Holds Odin's cross-view state: whether the dock is open, and the conversation
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
export function OdinProvider({ children }: { children: React.ReactNode }) {
	const [isOpen, setIsOpen] = useState(false);
	const [turns, setTurns] = useState<OdinTurn[]>([]);

	const open = useCallback(() => setIsOpen(true), []);
	const close = useCallback(() => setIsOpen(false), []);
	const toggle = useCallback(() => setIsOpen((current) => !current), []);
	const appendTurn = useCallback((turn: OdinTurn) => setTurns((current) => [...current, turn]), []);
	const replaceTurns = useCallback((next: OdinTurn[]) => setTurns(next), []);
	const clear = useCallback(() => setTurns([]), []);

	const value = useMemo(
		() => ({ isOpen, open, close, toggle, turns, appendTurn, replaceTurns, clear }),
		[isOpen, open, close, toggle, turns, appendTurn, replaceTurns, clear],
	);
	return <OdinContext.Provider value={value}>{children}</OdinContext.Provider>;
}

/**
 * Read/write access to the dock.
 *
 * Returns null outside a provider rather than throwing, so the topbar still
 * renders on the minimal shells (login, temp-token pages) that deliberately do
 * not mount Odin.
 */
export function useOdin(): OdinContextValue | null {
	return useContext(OdinContext);
}