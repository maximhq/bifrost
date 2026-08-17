import WarpPanel from "@/components/warp/warpPanel";
import { Sheet, SheetContent } from "@/components/ui/sheet";
import { useIsMobile } from "@/hooks/use-mobile";
import { useWarp } from "@/lib/contexts/warpContext";

/**
 * Wraps the app's content column so Warp can dock beside it.
 *
 * The dock narrows the page rather than floating over it, which is the whole
 * point: you ask Warp about the chart you are looking at, so the chart has to
 * stay readable. The topbar sits inside the left column and narrows with the
 * content.
 *
 * The width is fixed rather than draggable. A resizable split was tried first
 * and opened as an unusable ~80px sliver: the panel group sizes in percentages
 * of a parent whose width is itself established by the sidebar's flex layout,
 * and the two did not agree on the available space. A fixed column has no such
 * dependency, and a chat panel has one sensible width anyway.
 *
 * This sits below the topbar, not around it, so opening the dock never changes
 * the topbar's width. Only the content region splits, and page height is
 * untouched - so --app-topbar-height and --app-content-viewport stay correct.
 */
export default function WarpDock({ children }: { children: React.ReactNode }) {
	const warp = useWarp();
	const isMobile = useIsMobile();

	if (!warp?.isOpen) {
		return <>{children}</>;
	}

	// Below the mobile breakpoint there is no room to sit beside the content, so
	// the dock becomes a full-width sheet over it instead.
	if (isMobile) {
		return (
			<>
				{children}
				<Sheet open onOpenChange={(open) => !open && warp.close()}>
					<SheetContent side="right" className="w-full p-0 sm:max-w-md" data-testid="warp-dock-sheet">
						<WarpPanel />
					</SheetContent>
				</Sheet>
			</>
		);
	}

	return (
		// Fills the region below the topbar. min-h-0 lets the row shrink inside the
		// column flex parent; without it the content card cannot scroll internally
		// and pushes the layout taller than the viewport.
		<div className="flex min-h-0 w-full min-w-0 flex-1" data-testid="warp-dock">
			{/* min-w-0 is what lets the content column actually shrink. Without it a
			    flex child refuses to go below its content's intrinsic width, and the
			    dock would push the page off-screen instead of narrowing it. */}
			<div className="flex min-h-0 min-w-0 flex-1 flex-col">{children}</div>
			<aside
				// The slide is transform-only. Animating width would reflow the content
				// column on every frame, and anything inside it that measures itself -
				// charts, tables, virtualised lists - would re-run its observer for the
				// whole animation. This way the layout settles once.
				className="animate-in slide-in-from-right-4 fade-in-0 flex min-h-0 w-[400px] shrink-0 flex-col duration-200 ease-out will-change-transform xl:w-[460px]"
				data-testid="warp-dock-panel"
			>
				{/* The same card treatment the page content and the logs/dashboard filter
				    rail use: surface, radius, border and the mb-2/mr-2 gutter. Every
				    panel in the app is an outlined card, so Warp is one too.
				    overflow-hidden rather than the content card's overflow-auto - the
				    panel scrolls its own transcript and must not also scroll as a whole. */}
				<div className="dark:bg-card min-h-0 flex-1 overflow-hidden border border-gray-200 bg-white md:mr-2 md:mb-2 md:rounded-md dark:border-zinc-800">
					<WarpPanel />
				</div>
			</aside>
		</div>
	);
}