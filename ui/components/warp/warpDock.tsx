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
	const isOpen = !!warp?.isOpen;

	// One tree, always. An earlier version returned a bare fragment when closed
	// and a wrapped split when open - React sees different element types at the
	// same position, unmounts the entire page subtree and mounts it again on
	// every toggle. That remount re-runs every query on the page, re-measures
	// every chart, and briefly paints the old and new trees together.
	//
	// So the wrapper is unconditional and only the panel is conditional. Children
	// keep their identity, and opening Warp costs one reflow instead of a full
	// remount.
	return (
		// overflow-x-clip contains the panel's slide-in. The animation is a
		// translateX, so for its 200ms the panel sits to the right of its final
		// position and pokes past this row's edge - which is real overflow, and the
		// browser answers it with a horizontal scrollbar that appears and vanishes
		// on every open.
		//
		// clip rather than hidden: hidden would make this a scroll container, which
		// changes how sticky headers and nested scroll areas inside the page behave.
		// clip suppresses the overflow without any of that.
		<div className="flex min-h-0 w-full min-w-0 flex-1 overflow-x-clip" data-testid="warp-dock">
			{/* min-w-0 is what lets the content column actually shrink. Without it a
			    flex child refuses to go below its content's intrinsic width, and the
			    dock would push the page off-screen instead of narrowing it. */}
			<div className="flex min-h-0 min-w-0 flex-1 flex-col">{children}</div>

			{isOpen && !isMobile && (
				<aside
					// The slide is transform-only. Animating width would reflow the
					// content column on every frame, and anything inside it that measures
					// itself - charts, tables, virtualised lists - would re-run its
					// observer for the whole animation.
					className="animate-in slide-in-from-right-4 fade-in-0 flex min-h-0 w-[400px] shrink-0 flex-col duration-200 ease-out will-change-transform xl:w-[460px]"
					data-testid="warp-dock-panel"
				>
					{/* The same card treatment the page content and the logs/dashboard
					    filter rail use: surface, radius, border and the mb-2/mr-2 gutter.
					    overflow-hidden rather than the content card's overflow-auto - the
					    panel scrolls its own transcript and must not also scroll as a
					    whole. */}
					<div className="dark:bg-card min-h-0 flex-1 overflow-hidden border border-gray-200 bg-white md:mr-2 md:mb-2 md:rounded-md dark:border-zinc-800">
						<WarpPanel />
					</div>
				</aside>
			)}

			{/* Below the mobile breakpoint there is no room to sit beside the content,
			    so the panel becomes a sheet over it. Still inside the same wrapper, so
			    the content column is untouched either way. */}
			{isOpen && isMobile && (
				<Sheet open onOpenChange={(open) => !open && warp?.close()}>
					<SheetContent side="right" className="w-full p-0 sm:max-w-md" data-testid="warp-dock-sheet">
						<WarpPanel />
					</SheetContent>
				</Sheet>
			)}
		</div>
	);
}