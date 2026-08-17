import { TOPBAR_MENU_SIDE_OFFSET } from "@/components/topbar.utils";
import { OdinIcon } from "@/components/ui/icons";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { useOdin } from "@/lib/contexts/odinContext";
import { cn } from "@/lib/utils";
import { useHotkeys } from "react-hotkeys-hook";

/**
 * Topbar button that opens and closes the Odin dock.
 *
 * The size-8 box is not cosmetic. Every topbar trigger shares one box because
 * Radix measures its menu offset from the trigger's bounding box, so an
 * odd-sized trigger opens its neighbours' surfaces off the shared line and
 * breaks the row's horizontal rhythm. Odin opens no Radix surface of its own,
 * but it still has to match.
 */
export default function OdinLauncher() {
	const odin = useOdin();

	// Cmd/Ctrl+I toggles the dock. enableOnFormTags is off by default in
	// react-hotkeys-hook, which is what we want: the shortcut must not fire while
	// someone is typing into a filter box or, especially, into Odin's own composer.
	useHotkeys(
		"mod+i",
		(event) => {
			event.preventDefault();
			odin?.toggle();
		},
		{ enabled: !!odin },
		[odin],
	);

	// Rendered only where an OdinProvider is mounted, which excludes the minimal
	// shells that have no dock to open.
	if (!odin) return null;

	// Hidden while the dock is open. The panel has its own close button, and two
	// controls for one thing sitting inches apart is just a second way to get it
	// wrong. The keyboard shortcut above still toggles either way, which is why
	// the hotkey is registered before this return.
	if (odin.isOpen) return null;

	return (
		<Tooltip>
			<TooltipTrigger asChild>
				<button
					type="button"
					aria-label="Ask Odin"
					aria-pressed={odin.isOpen}
					data-testid="topbar-odin-btn"
					onClick={odin.toggle}
					className={cn(
						"flex size-8 shrink-0 cursor-pointer items-center justify-center rounded-md transition-colors",
						odin.isOpen ? "bg-accent text-accent-foreground" : "text-muted-foreground hover:bg-accent hover:text-accent-foreground",
					)}
				>
					<OdinIcon className="size-5" />
				</button>
			</TooltipTrigger>
			<TooltipContent sideOffset={TOPBAR_MENU_SIDE_OFFSET}>
				<span className="flex items-center gap-2">
					Ask Odin
					<kbd className="bg-muted text-muted-foreground rounded px-1 py-0.5 font-mono text-[10px]">⌘I</kbd>
				</span>
			</TooltipContent>
		</Tooltip>
	);
}