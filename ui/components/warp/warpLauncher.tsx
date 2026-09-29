import { WarpIcon } from "@/components/ui/icons";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { useWarp } from "@/lib/contexts/warpContext";
import { useHotkeys } from "react-hotkeys-hook";
import { useEffect, useRef } from "react";

function isAppleDevice(): boolean {
	if (typeof navigator === "undefined") return false;
	return /Mac|iPhone|iPad|iPod/.test(navigator.platform || navigator.userAgent);
}

export default function WarpLauncher() {
	const warp = useWarp();
	const buttonRef = useRef<HTMLButtonElement>(null);
	// Refocus only after a real close, not on first mount.
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

	// enableOnFormTags stays off so the shortcut never fires while typing, including in the composer.
	useHotkeys(
		"mod+i",
		(event) => {
			event.preventDefault();
			warp?.toggle();
		},
		{ enabled: !!warp },
		[warp],
	);

	if (!warp) return null;

	// data-state is set by hand to reuse the other topbar triggers' Radix data-[state=open] styles.
	return (
		<Tooltip>
			<TooltipTrigger asChild>
				<button
					ref={buttonRef}
					type="button"
					aria-label="Ask Warp"
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
					Ask Warp
					<kbd className="bg-muted text-muted-foreground rounded px-1 py-0.5 font-mono text-[10px]">
						{isAppleDevice() ? "⌘I" : "Ctrl+I"}
					</kbd>
				</span>
			</TooltipContent>
		</Tooltip>
	);
}