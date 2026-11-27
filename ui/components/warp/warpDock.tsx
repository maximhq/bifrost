import { ResizableHandle, ResizablePanel, ResizablePanelGroup } from "@/components/ui/resizable";
import { Sheet, SheetContent, SheetDescription, SheetTitle } from "@/components/ui/sheet";
import { useIsNarrowerThan } from "@/hooks/use-mobile";
import { useWarp } from "@/lib/contexts/warpContext";
import { Loader2 } from "lucide-react";
import { lazy, Suspense } from "react";
import { useTranslation } from "react-i18next";

// Lazy: the dock is on every page, and the panel pulls in the markdown renderer. Loading it on
// first open keeps that out of the app bundle; once loaded, answers render without a fallback.
const WarpPanel = lazy(() => import("@/components/warp/warpPanel"));

// Shown while the panel chunk downloads; without it the first open is a blank card.
function WarpPanelFallback() {
	const { t } = useTranslation("shell");
	return (
		<div className="flex h-full w-full items-center justify-center" role="status" data-testid="warp-panel-loading">
			<Loader2 className="text-muted-foreground size-5 animate-spin" aria-hidden="true" />
			<span className="sr-only">{t("warp.panel.loading")}</span>
		</div>
	);
}

// Docks Warp beside the content column so the page narrows instead of being covered.
// 240px sidebar + 400px dock + a readable content column; below this Warp opens as a sheet.
const WARP_DOCK_MIN_WIDTH = 1024;

export default function WarpDock({ children }: { children: React.ReactNode }) {
	const { t } = useTranslation("shell");
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
						<ResizableHandle
							aria-label={t("warp.dock.resize")}
							className="bg-transparent md:-translate-x-1.5"
							data-testid="warp-dock-resize-handle"
						/>
						<ResizablePanel id="warp-panel" defaultSize="400px" minSize="320px" maxSize="50%" className="min-h-0 md:pb-3">
							{/* The test id sits here because Panel does not pass it through to the DOM. */}
							<div
								className="dark:bg-card bg-card h-full min-h-0 overflow-hidden border border-gray-200 md:mr-2 md:rounded-md dark:border-zinc-800"
								data-testid="warp-dock-panel"
							>
								<Suspense fallback={<WarpPanelFallback />}>
									<WarpPanel />
								</Suspense>
							</div>
						</ResizablePanel>
					</>
				)}
			</ResizablePanelGroup>

			{isOpen && isMobile && (
				<Sheet open onOpenChange={(open) => !open && warp?.close()}>
					{/* Override SheetContent's sm:w-3/4 so the sheet stays full width. */}
					<SheetContent
						side="right"
						resizable={false}
						className="w-[calc(100%_-_16px)] p-0 sm:w-full sm:max-w-none"
						data-testid="warp-dock-sheet"
					>
						{/* Radix names the dialog from SheetTitle, not the panel's own heading. */}
						<SheetTitle className="sr-only">{t("nav.warp")}</SheetTitle>
						<SheetDescription className="sr-only">{t("warp.dock.description")}</SheetDescription>
						<Suspense fallback={<WarpPanelFallback />}>
							<WarpPanel />
						</Suspense>
					</SheetContent>
				</Sheet>
			)}
		</div>
	);
}