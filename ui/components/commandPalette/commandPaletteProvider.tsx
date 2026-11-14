import { createContext, lazy, Suspense, useCallback, useContext, useEffect, useMemo, useState } from "react";

import { isInsideCodeEditor, isPaletteShortcut } from "./commandPalette.utils";

// The dialog lives in its own chunk so it stays out of the initial bundle.
const loadCommandPalette = () => import("./commandPalette");
const CommandPalette = lazy(loadCommandPalette);

interface CommandPaletteContextValue {
	open: boolean;
	setOpen: (open: boolean) => void;
	toggle: () => void;
}

const CommandPaletteContext = createContext<CommandPaletteContextValue | null>(null);

export function useCommandPalette(): CommandPaletteContextValue {
	const ctx = useContext(CommandPaletteContext);
	if (!ctx) throw new Error("useCommandPalette must be used inside CommandPaletteProvider");
	return ctx;
}

/**
 * Owns the palette's open state and the global ⌘K / Ctrl+K shortcut. Mount once,
 * inside the router so the palette can navigate.
 */
export function CommandPaletteProvider({ children }: { children: React.ReactNode }) {
	const [open, setOpen] = useState(false);
	const [mounted, setMounted] = useState(false);
	const toggle = useCallback(() => setOpen((o) => !o), []);

	if (open && !mounted) setMounted(true);

	// Load and mount the palette (closed) once the app is idle. Fetching the chunk alone is not
	// enough: lazy() still suspends on its first render, so mounting it ahead of time is what
	// makes the first ⌘K open on the same frame as the keystroke.
	useEffect(() => {
		const warm = () => void loadCommandPalette().then(() => setMounted(true));
		if (typeof window.requestIdleCallback === "function") {
			const id = window.requestIdleCallback(warm, { timeout: 5000 });
			return () => window.cancelIdleCallback(id);
		}
		const id = window.setTimeout(warm, 2000);
		return () => window.clearTimeout(id);
	}, []);

	useEffect(() => {
		const onKeyDown = (event: KeyboardEvent) => {
			if (!isPaletteShortcut(event)) return;
			if (event.defaultPrevented || isInsideCodeEditor(event.target)) return;
			event.preventDefault();
			toggle();
		};
		window.addEventListener("keydown", onKeyDown);
		return () => window.removeEventListener("keydown", onKeyDown);
	}, [toggle]);

	const value = useMemo(() => ({ open, setOpen, toggle }), [open, toggle]);

	return (
		<CommandPaletteContext.Provider value={value}>
			{children}
			{mounted && (
				<Suspense fallback={null}>
					<CommandPalette open={open} onOpenChange={setOpen} />
				</Suspense>
			)}
		</CommandPaletteContext.Provider>
	);
}