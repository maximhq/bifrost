import WarpPanel from "@/components/warp/warpPanel";
import { Sheet, SheetContent, SheetDescription, SheetTitle } from "@/components/ui/sheet";
import { useIsNarrowerThan } from "@/hooks/use-mobile";
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
/**
 * Narrowest viewport that can show the dock beside the page.
 *
 * 240px sidebar + 400px dock + enough left over for a readable content column.
 */
const WARP_DOCK_MIN_WIDTH = 1024;

export default function WarpDock({ children }: { children: React.ReactNode }) {
	const warp = useWarp();
	// Not useIsMobile's 768px. The sidebar is 240px and the dock a fixed 400px,
	// so at 768 the page itself is left with barely 100px - narrower than the
	// content it is meant to keep readable, which is the whole reason the dock
	// narrows the page rather than floating over it. Below this width the panel
	// is a sheet instead.
	const isMobile = useIsNarrowerThan(WARP_DOCK_MIN_WIDTH);
	const isOpen = !!warp?.isOpen;

	return (
		// Fills the region below the topbar. min-h-0 lets the row shrink inside the
		// column flex parent; without it the content card cannot scroll internally
		// and pushes the layout taller than the viewport.
		// Both wrappers stay mounted whether or not the dock is open, and at every
		// viewport width. Swapping the root between a fragment and this div moved
		// children to a different position in the tree, so React remounted the
		// entire workspace - losing page state, form input and scroll position.
		<div className="flex min-h-0 w-full min-w-0 flex-1" data-testid="warp-dock">
			{/* min-w-0 is what lets the content column actually shrink. Without it a
			    flex child refuses to go below its content's intrinsic width, and the
			    dock would push the page off-screen instead of narrowing it. */}
			<div className="flex min-h-0 min-w-0 flex-1 flex-col">{children}</div>
			{/* Only the presentation switches at the breakpoint - never the wrappers
			    above. Returning a fragment on mobile and this div on desktop moved
			    children to a different position in the tree, so React remounted the
			    whole workspace on a resize or a device rotation and took form input
			    and scroll position with it.

			    Below WARP_DOCK_MIN_WIDTH there is no room to sit beside the content,
			    so the panel becomes a full-width sheet over it. The Sheet stays
			    mounted and is driven by `open` rather than rendered conditionally,
			    for the same reason. */}
			{isMobile ? (
				<Sheet open={isOpen} onOpenChange={(open) => !open && warp?.close()}>
					{/* sm:w-full sm:max-w-none because the two breakpoints disagree:
					    SheetContent's own `sm:w-3/4` starts at 640px, so without these
					    the sheet would sit at three-quarter width through the range it
					    is supposed to cover edge to edge. */}
					<SheetContent side="right" className="w-full p-0 sm:w-full sm:max-w-none" data-testid="warp-dock-sheet">
						{/* Radix takes the dialog's accessible name from SheetTitle, not
						    from whatever heading the content happens to render - the
						    panel's own <h2> is not registered, so without these the sheet
						    is announced as an unnamed dialog. Visually hidden because the
						    panel already shows its own header. */}
						<SheetTitle className="sr-only">Warp</SheetTitle>
						<SheetDescription className="sr-only">Ask Warp questions about this deployment&apos;s logs, usage and spend.</SheetDescription>
						<WarpPanel />
					</SheetContent>
				</Sheet>
			) : (
				isOpen && (
					<aside
						// The slide is transform-only. Animating width would reflow the content
						// column on every frame, and anything inside it that measures itself -
						// charts, tables, virtualised lists - would re-run its observer for the
						// whole animation. This way the layout settles once.
						// hidden min-[1024px]:flex as well as the isMobile branch, because
						// useIsNarrowerThan starts at undefined and reads as false until its
						// effect registers the listener - so a load or a resize below 1024px
						// painted this fixed 400px column for a frame before the state
						// caught up. CSS has no such gap.
						className="animate-in slide-in-from-right-4 fade-in-0 hidden min-h-0 w-[400px] shrink-0 flex-col duration-200 ease-out will-change-transform min-[1024px]:flex xl:w-[460px]"
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
				)
			)}
		</div>
	);
}