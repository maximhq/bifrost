import { WarpIcon } from "@/components/ui/icons";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { useWarp } from "@/lib/contexts/warpContext";
import { cn } from "@/lib/utils";
import { useHotkeys } from "react-hotkeys-hook";

/**
 * Topbar button that opens and closes the Warp dock.
 *
 * The size-8 box is not cosmetic. Every topbar trigger shares one box because
 * Radix measures its menu offset from the trigger's bounding box, so an
 * odd-sized trigger opens its neighbours' surfaces off the shared line and
 * breaks the row's horizontal rhythm. Warp opens no Radix surface of its own,
 * but it still has to match.
 */
export default function WarpLauncher() {
	const warp = useWarp();

	// Cmd/Ctrl+I toggles the dock. enableOnFormTags is off by default in
	// react-hotkeys-hook, which is what we want: the shortcut must not fire while
	// someone is typing into a filter box or, especially, into Warp's own composer.
	useHotkeys(
		"mod+i",
		(event) => {
			event.preventDefault();
			warp?.toggle();
		},
		{ enabled: !!warp },
		[warp],
	);

	// Rendered only where an WarpProvider is mounted, which excludes the minimal
	// shells that have no dock to open.
	if (!warp) return null;

	// Hidden while the dock is open. The panel has its own close button, and two
	// controls for one thing sitting inches apart is just a second way to get it
	// wrong. The keyboard shortcut above still toggles either way, which is why
	// the hotkey is registered before this return.
	if (warp.isOpen) return null;

	return (
		<Tooltip>
			<TooltipTrigger asChild>
				<button
					type="button"
					aria-label="Ask Warp"
					aria-pressed={warp.isOpen}
					data-testid="topbar-warp-btn"
					onClick={warp.toggle}
					className={cn(
						"flex size-8 shrink-0 cursor-pointer items-center justify-center rounded-md transition-colors",
						warp.isOpen ? "bg-accent text-accent-foreground" : "text-muted-foreground hover:bg-accent hover:text-accent-foreground",
					)}
				>
					<WarpIcon className="size-5" />
				</button>
			</TooltipTrigger>
			<TooltipContent sideOffset={8}>
				<span className="flex items-center gap-2">
					Ask Warp
					<kbd className="bg-muted text-muted-foreground rounded px-1 py-0.5 font-mono text-[10px]">⌘I</kbd>
				</span>
			</TooltipContent>
		</Tooltip>
	);
}