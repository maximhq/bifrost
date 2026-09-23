import { WarpIcon } from "@/components/ui/icons";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { useWarp } from "@/lib/contexts/warpContext";
import { useHotkeys } from "react-hotkeys-hook";
import { useEffect, useRef } from "react";
import { useTranslation } from "react-i18next";

/**
 * Topbar button that opens and closes the Warp dock.
 *
 * The size-8 box is not cosmetic. Every topbar trigger shares one box because
 * Radix measures its menu offset from the trigger's bounding box, so an
 * odd-sized trigger opens its neighbours' surfaces off the shared line and
 * breaks the row's horizontal rhythm. Warp opens no Radix surface of its own,
 * but it still has to match.
 */
/** Whether to label the modifier as Cmd rather than Ctrl. */
function isAppleDevice(): boolean {
	if (typeof navigator === "undefined") return false;
	return /Mac|iPhone|iPad|iPod/.test(navigator.platform || navigator.userAgent);
}

export default function WarpLauncher() {
	const { t } = useTranslation("shell");
	const warp = useWarp();
	const buttonRef = useRef<HTMLButtonElement>(null);
	// Tracks whether this launcher was the thing that got hidden, so focus is
	// only pulled back after a real close - not on first mount, where stealing
	// focus would yank it away from whatever the page put it on.
	const wasOpen = useRef(false);

	useEffect(() => {
		if (warp?.isOpen) {
			wasOpen.current = true;
			return;
		}
		if (wasOpen.current) {
			wasOpen.current = false;
			buttonRef.current?.focus();
		}
	}, [warp?.isOpen]);

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

	// Stays visible while the dock is open and shows as selected, like the other
	// topbar triggers. data-state is set by hand so this button can share their
	// Radix data-[state=open] classes, even though it opens no Radix surface.
	return (
		<Tooltip>
			<TooltipTrigger asChild>
				<button
					ref={buttonRef}
					type="button"
					aria-label={t("warp.ask")}
					aria-pressed={warp.isOpen}
					data-state={warp.isOpen ? "open" : "closed"}
					data-testid="topbar-warp-btn"
					onClick={warp.toggle}
					className="text-muted-foreground hover:bg-accent hover:text-accent-foreground data-[state=open]:bg-card data-[state=open]:text-accent-foreground flex size-8 shrink-0 cursor-pointer items-center justify-center rounded-md transition-colors data-[state=open]:border"
				>
					<WarpIcon className="size-5" />
				</button>
			</TooltipTrigger>
			<TooltipContent sideOffset={8}>
				<span className="flex items-center gap-2">
					{t("warp.ask")}
					{/* mod+i binds Cmd on a Mac and Ctrl everywhere else, so the label
					    has to follow the platform rather than always claiming ⌘. */}
					<kbd className="bg-muted text-muted-foreground rounded px-1 py-0.5 font-mono text-[10px]">
						{isAppleDevice() ? "⌘I" : "Ctrl+I"}
					</kbd>
				</span>
			</TooltipContent>
		</Tooltip>
	);
}