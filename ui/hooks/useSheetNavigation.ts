import { type LucideIcon, ArrowUp, ArrowDown } from "lucide-react";
import { useHotkeys } from "react-hotkeys-hook";

interface UseSheetNavigationOptions {
	enabled: boolean;
	hasPrev: boolean;
	hasNext: boolean;
	onNavigate: (direction: "prev" | "next") => void;
}

export interface ShortcutKey {
	icon?: LucideIcon;
	label?: string;
}

export interface SheetNavigationShortcuts {
	prev: ShortcutKey[];
	next: ShortcutKey[];
}

export function useSheetNavigation({ enabled, hasPrev, hasNext, onNavigate }: UseSheetNavigationOptions): SheetNavigationShortcuts {
	useHotkeys("up,k", () => onNavigate("prev"), {
		enabled: enabled && hasPrev,
		preventDefault: true,
	});
	useHotkeys("down,j", () => onNavigate("next"), {
		enabled: enabled && hasNext,
		preventDefault: true,
	});

	return {
		prev: [{ icon: ArrowUp }, { label: "K" }],
		next: [{ icon: ArrowDown }, { label: "J" }],
	};
}