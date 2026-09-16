import { SheetNavigationButtons } from "@/components/sheetNavigationButtons";
import { Button } from "@/components/ui/button";
import { Sheet, SheetContent, SheetTitle } from "@/components/ui/sheet";
import { useGetLogByIdQuery } from "@/lib/store/apis/logsApi";
import { useGetPromptQuery } from "@/lib/store/apis/promptsApi";
import type { LogEntry } from "@/lib/types/logs";
import { useSheetNavigation } from "@/hooks/useSheetNavigation";
import { Loader2 } from "lucide-react";
import { useEffect, useState } from "react";
import { LogDetailView } from "./logDetailView";

interface LogDetailSheetProps {
	log: LogEntry | null;
	open: boolean;
	onOpenChange: (open: boolean) => void;
	handleDelete?: (log: LogEntry) => void;
	onNavigate?: (direction: "prev" | "next") => void;
	hasPrev?: boolean;
	hasNext?: boolean;
	canReveal?: boolean;
	onViewSession?: (sessionId: string, logId: string) => void;
	onFilterByParentRequestId?: (parentRequestId: string) => void;
	onFilterBySessionId?: (sessionId: string) => void;
}

export function LogDetailSheet({
	log,
	open,
	onOpenChange,
	handleDelete,
	onNavigate,
	hasPrev = false,
	hasNext = false,
	canReveal = false,
	onViewSession,
	onFilterByParentRequestId,
	onFilterBySessionId,
}: LogDetailSheetProps) {
	const [pollingInterval, setPollingInterval] = useState(0);
	const {
		currentData: fullLog,
		isLoading,
		isError,
		isFetching,
		refetch,
	} = useGetLogByIdQuery(log?.id ?? "", {
		skip: !open || !log?.id,
		pollingInterval,
	});

	const shouldPoll = isError || fullLog?.status === "processing";

	// A list row is only a preview. Failed hydration must never enable export,
	// including when RTK Query retains cached data after a failed refresh.
	const displayLog = !isError && !isLoading && fullLog?.id === log?.id ? fullLog : undefined;
	// Prefer full log when loaded; otherwise list row — enables prompt fetch in parallel with getLogById
	const selectedPromptId = log ? (fullLog?.id === log.id ? fullLog : log).selected_prompt_id : undefined;
	const { data: selectedPromptData } = useGetPromptQuery(selectedPromptId ?? "", {
		skip: !open || !selectedPromptId,
	});

	useEffect(() => {
		setPollingInterval(shouldPoll ? 2000 : 0);
	}, [shouldPoll]);

	// Keyboard navigation: arrow up/down to navigate between logs
	const { prev: prevKeys, next: nextKeys } = useSheetNavigation({
		enabled: open,
		hasPrev,
		hasNext,
		onNavigate: (direction) => onNavigate?.(direction),
	});

	if (!log) return null;

	const resolvedSelectedPromptName = selectedPromptData?.prompt?.name ?? displayLog?.selected_prompt_name ?? "";

	return (
		<Sheet open={open} onOpenChange={onOpenChange}>
			<SheetContent className="border-secondary flex w-full flex-col gap-4 overflow-x-hidden border p-4 sm:max-w-[60%] md:p-8">
				{isError ? (
					<div role="alert" className="flex h-full flex-col items-center justify-center gap-4" data-testid="logdetails-load-error">
						<SheetTitle>Unable to load complete log details</SheetTitle>
						<p className="text-muted-foreground text-sm">Details and export are unavailable until the complete log can be loaded.</p>
						<Button variant="outline" disabled={isFetching} onClick={() => refetch()} data-testid="logdetails-retry-button">
							{isFetching ? "Retrying..." : "Retry"}
						</Button>
					</div>
				) : !displayLog ? (
					<div className="flex h-full items-center justify-center">
						<SheetTitle className="sr-only">Loading log details</SheetTitle>
						<Loader2 className="text-muted-foreground h-6 w-6 animate-spin" />
					</div>
				) : (
					<LogDetailView
						log={displayLog}
						resolvedSelectedPromptName={resolvedSelectedPromptName}
						handleDelete={handleDelete}
						canReveal={canReveal}
						onClose={() => onOpenChange(false)}
						onFilterByParentRequestId={onFilterByParentRequestId}
						onFilterBySessionId={onFilterBySessionId}
						headerAction={
							<>
								{displayLog.parent_request_id && onViewSession ? (
									<Button
										variant="outline"
										size="sm"
										data-testid="session-button-view"
										onClick={() => onViewSession(displayLog.parent_request_id as string, displayLog.id)}
									>
										View Session
									</Button>
								) : null}
								<SheetNavigationButtons
									hasPrev={hasPrev}
									hasNext={hasNext}
									onNavigate={(dir) => onNavigate?.(dir)}
									prevKeys={prevKeys}
									nextKeys={nextKeys}
									entityLabel="log"
								/>
							</>
						}
					/>
				)}
			</SheetContent>
		</Sheet>
	);
}