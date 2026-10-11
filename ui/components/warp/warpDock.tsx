import { ResizableHandle, ResizablePanel, ResizablePanelGroup } from "@/components/ui/resizable";
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

	// The group is unconditional so toggling Warp never remounts the page; only the handle and
	// the Warp panel come and go.
	return (
		<div className="flex min-h-0 w-full min-w-0 flex-1" data-testid="warp-dock">
			<ResizablePanelGroup direction="horizontal" className="min-h-0 min-w-0">
				<ResizablePanel id="warp-content" minSize="360px" className="flex min-h-0 min-w-0 flex-col">
					{children}
				</ResizablePanel>

				{isOpen && !isMobile && (
					<>
						<ResizableHandle aria-label="Resize Warp panel" className="bg-transparent md:-translate-x-1.5" data-testid="warp-dock-resize-handle" />
						<ResizablePanel
							id="warp-panel"
							defaultSize="400px"
							minSize="320px"
							maxSize="50%"
							className="min-h-0"
							data-testid="warp-dock-panel"
						>
							<div className="dark:bg-card bg-card h-full min-h-0 overflow-hidden border border-gray-200 md:mr-2 md:mb-3 md:rounded-md dark:border-zinc-800">
								<WarpPanel />
							</div>
						</ResizablePanel>
					</>
				)}
			</ResizablePanelGroup>

			{isOpen && isMobile && (
				<Sheet open onOpenChange={(open) => !open && warp?.close()}>
					{/* Override SheetContent's sm:w-3/4 so the sheet stays full width. */}
					<SheetContent side="right" className="w-[calc(100%_-_16px)] p-0 sm:w-full sm:max-w-none" data-testid="warp-dock-sheet">
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