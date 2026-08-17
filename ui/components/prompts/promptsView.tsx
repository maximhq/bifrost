import FullPageLoader from "@/components/fullPageLoader";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { ResizableHandle, ResizablePanel, ResizablePanelGroup } from "@/components/ui/resizable";
import { useIsMobile } from "@/hooks/use-mobile";
import { AlertCircle, Loader2 } from "lucide-react";
import { PromptSidebar } from "./fragments/sidebar";
import { PlaygroundPanel } from "./fragments/playgroundPanel";
import { SettingsPanel } from "./fragments/settingsPanel";
import { DeleteFolderDialog, DeletePromptDialog } from "./components/alerts";
import { PromptSheets } from "./components/sheets";
import { EmptyState, PromptsEmptyState } from "./components/emptyState";
import PromptsViewHeader from "./components/promptsViewHeader";
import { usePromptContext } from "./context";

export default function PromptsView() {
	const isMobile = useIsMobile();
	const { folders, prompts, foldersLoading, promptsLoading, foldersError, promptsError, isLoadingPlayground, selectedPromptId } =
		usePromptContext();

	if (foldersLoading || promptsLoading) {
		return <FullPageLoader />;
	}

	if (foldersError || promptsError) {
		return (
			<div className="no-padding-parent no-border-parent p-4">
				<Alert variant="destructive">
					<AlertCircle className="h-4 w-4" />
					<AlertDescription>Failed to load prompt repository</AlertDescription>
				</Alert>
			</div>
		);
	}

	if (folders.length === 0 && prompts.length === 0) {
		return (
			<div className="no-padding-parent no-border-parent flex h-[calc(var(--app-content-viewport)_-_18px)] w-full items-center">
				<PromptSheets />
				<PromptsEmptyState />
			</div>
		);
	}

	if (isMobile) {
		return (
			<div className="no-padding-parent no-border-parent bg-background flex h-[calc(var(--app-content-viewport)_-_16px)] w-full flex-col gap-2 overflow-auto">
				<DeleteFolderDialog />
				<DeletePromptDialog />
				<PromptSheets />
				<div className="bg-card max-h-[40dvh] shrink-0 overflow-auto rounded-md">
					<PromptSidebar />
				</div>
				<div className="bg-card min-h-0 flex-1 overflow-auto rounded-md">
					{selectedPromptId ? (
						<div className="flex min-h-full flex-col">
							<PromptsViewHeader />
							{isLoadingPlayground ? (
								<div className="flex flex-1 items-center justify-center">
									<Loader2 className="text-muted-foreground h-5 w-5 animate-spin" />
								</div>
							) : (
								<div className="flex flex-col">
									<div className="min-h-[50dvh]">
										<PlaygroundPanel />
									</div>
									<div className="border-t">
										<SettingsPanel />
									</div>
								</div>
							)}
						</div>
					) : (
						<EmptyState />
					)}
				</div>
			</div>
		);
	}

	return (
		<div className="no-padding-parent no-border-parent bg-background h-[calc(var(--app-content-viewport)_-_16px)] w-full">
			<DeleteFolderDialog />
			<DeletePromptDialog />
			<PromptSheets />

			<ResizablePanelGroup direction="horizontal" className="h-full">
				<ResizablePanel defaultSize={20} className="bg-card mr-1 overflow-hidden rounded-r-md">
					<PromptSidebar />
				</ResizablePanel>

				<ResizableHandle className="mr-1 bg-transparent" />

				<ResizablePanel defaultSize={80} minSize={50} className="bg-card overflow-hidden rounded-md">
					{selectedPromptId ? (
						<div className="flex h-full flex-col">
							<PromptsViewHeader />

							{isLoadingPlayground ? (
								<div className="flex flex-1 items-center justify-center">
									<Loader2 className="text-muted-foreground h-5 w-5 animate-spin" />
								</div>
							) : (
								<ResizablePanelGroup direction="horizontal" className="flex-1">
									<ResizablePanel defaultSize={70} minSize={40}>
										<PlaygroundPanel />
									</ResizablePanel>
									<ResizableHandle />
									<ResizablePanel defaultSize={30} minSize={20}>
										<SettingsPanel />
									</ResizablePanel>
								</ResizablePanelGroup>
							)}
						</div>
					) : (
						<EmptyState />
					)}
				</ResizablePanel>
			</ResizablePanelGroup>
		</div>
	);
}