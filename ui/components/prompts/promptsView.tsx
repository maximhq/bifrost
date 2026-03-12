import FullPageLoader from "@/components/fullPageLoader";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { ResizableHandle, ResizablePanel, ResizablePanelGroup } from "@/components/ui/resizable";
import { AlertCircle, Loader2 } from "lucide-react";
import { PromptSidebar } from "./fragments/sidebar";
import { PlaygroundPanel } from "./fragments/playgroundPanel";
import { SettingsPanel } from "./fragments/settingsPanel";
import { DeleteFolderDialog, DeletePromptDialog } from "./components/alerts";
import { PromptSheets } from "./components/sheets";
import { EmptyState } from "./components/emptyState";
import PromptsViewHeader from "./components/promptsViewHeader";
import { usePromptContext } from "./context";

export default function PromptsView() {
	const { foldersLoading, promptsLoading, foldersError, promptsError, isLoadingPlayground, selectedPromptId } = usePromptContext();

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

	return (
		<div className="no-padding-parent no-border-parent bg-background h-[calc(100dvh_-_18px)] w-full">
			<DeleteFolderDialog />
			<DeletePromptDialog />
			<PromptSheets />

			<ResizablePanelGroup direction="horizontal" className="h-full">
				<ResizablePanel defaultSize={20} className="bg-card mr-1 overflow-hidden rounded-r-md rounded-br-none">
					<PromptSidebar />
				</ResizablePanel>

				<ResizableHandle className="mr-1 bg-transparent" />

				<ResizablePanel defaultSize={80} minSize={50} className="bg-card overflow-hidden rounded-md rounded-bl-none">
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
