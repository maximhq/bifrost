import { OdinIcon } from "@/components/ui/icons";
import { useOdin } from "@/lib/contexts/odinContext";
import { X } from "lucide-react";

/**
 * The dock's contents.
 *
 * This PR ships the shell only - header, surface, empty state - so the layout
 * change can be reviewed on its own. The layout touches clientLayout.tsx, which
 * wraps every workspace route, so a regression there hits all of them; keeping
 * the chat code out of that diff is deliberate. The message list, composer and
 * streaming client land next.
 */
export default function OdinPanel() {
	const odin = useOdin();
	if (!odin) return null;

	return (
		// The same surface treatment as the content card, so the two read as one
		// shell rather than a panel pasted onto the app.
		// No border or surface of its own: OdinDock wraps this in the same card
		// treatment the page content uses, so the two read as a matched pair.
		<div className="flex h-full min-h-0 w-full flex-col" data-testid="odin-panel">
			{/* h-13 matches the topbar, so the panel header lines up with the page
			    title across the divider. */}
			<header className="flex h-13 shrink-0 items-center justify-between gap-2 border-b px-4">
				<div className="flex min-w-0 items-center gap-2">
					<OdinIcon className="text-muted-foreground size-4 shrink-0" />
					<h2 className="truncate text-sm font-semibold">Odin</h2>
				</div>
				<button
					type="button"
					aria-label="Close Odin"
					data-testid="odin-close-btn"
					onClick={odin.close}
					className="text-muted-foreground hover:bg-accent hover:text-accent-foreground flex size-7 shrink-0 cursor-pointer items-center justify-center rounded-md transition-colors"
				>
					<X className="size-4" />
				</button>
			</header>

			<div className="flex min-h-0 flex-1 flex-col items-center justify-center gap-2 px-6 text-center">
				<span className="bg-muted text-muted-foreground flex size-9 items-center justify-center rounded-full">
					<OdinIcon className="size-4" />
				</span>
				<p className="text-sm font-medium">Ask about your gateway data</p>
				<p className="text-muted-foreground text-xs">Spend, latency, models, users and virtual keys.</p>
			</div>
		</div>
	);
}