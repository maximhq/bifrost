import { Sheet, SheetContent, SheetDescription, SheetTitle } from "@/components/ui/sheet";
import WarpPanel from "@/components/warp/warpPanel";
import { useIsNarrowerThan } from "@/hooks/use-mobile";
import { useWarp } from "@/lib/contexts/warpContext";

// Docks Warp beside the content column so the page narrows instead of being covered.
// 240px sidebar + 400px dock + a readable content column; below this Warp opens as a sheet.
const WARP_DOCK_MIN_WIDTH = 1024;

export default function WarpDock({ children }: { children: React.ReactNode }) {
	const warp = useWarp();
	const isMobile = useIsNarrowerThan(WARP_DOCK_MIN_WIDTH);
	const isOpen = !!warp?.isOpen;

	// Wrapper is unconditional so toggling Warp never remounts the page.
	return (
		// clip, not hidden: contains the slide-in without making this a scroll container.
		<div className="flex min-h-0 w-full min-w-0 flex-1 overflow-x-clip" data-testid="warp-dock">
			<div className="flex min-h-0 min-w-0 flex-1 flex-col">{children}</div>

			{isOpen && !isMobile && (
				<aside
					// CSS hidden below 1024px covers the frame before useIsNarrowerThan resolves.
					className="animate-in slide-in-from-right-4 fade-in-0 hidden min-h-0 w-[400px] shrink-0 flex-col duration-200 ease-out will-change-transform motion-reduce:animate-none min-[1024px]:flex xl:w-[460px]"
					data-testid="warp-dock-panel"
				>
					<div className="dark:bg-card min-h-0 flex-1 overflow-hidden border border-gray-200 bg-card md:mr-2 md:mb-3 md:rounded-md dark:border-zinc-800">
						<WarpPanel />
					</div>
				</aside>
			)}

			{isOpen && isMobile && (
				<Sheet open onOpenChange={(open) => !open && warp?.close()}>
					{/* Override SheetContent's sm:w-3/4 so the sheet stays full width. */}
					<SheetContent side="right" className="p-0 sm:w-full sm:max-w-none w-[calc(100%_-_16px)]" data-testid="warp-dock-sheet">
						{/* Radix names the dialog from SheetTitle, not the panel's own heading. */}
						<SheetTitle className="sr-only">Warp</SheetTitle>
						<SheetDescription className="sr-only">Ask Warp questions about this deployment&apos;s logs, usage and spend.</SheetDescription>
						<WarpPanel />
					</SheetContent>
				</Sheet>
			)}
		</div>
	);
}