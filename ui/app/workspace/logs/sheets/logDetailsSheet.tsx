import { Button } from "@/components/ui/button";
import { Sheet, SheetContent, SheetTitle } from "@/components/ui/sheet";
import { useGetLogByIdQuery } from "@/lib/store/apis/logsApi";
import { useGetPromptQuery } from "@/lib/store/apis/promptsApi";
import type { LogEntry } from "@/lib/types/logs";
import { ChevronDown, ChevronUp, Loader2 } from "lucide-react";
import { useEffect, useState } from "react";
import { useHotkeys } from "react-hotkeys-hook";
import { LogDetailView } from "./logDetailView";

interface LogDetailSheetProps {
	log: LogEntry | null;
	open: boolean;
	onOpenChange: (open: boolean) => void;
	handleDelete: (log: LogEntry) => void;
	onNavigate?: (direction: "prev" | "next") => void;
	hasPrev?: boolean;
	hasNext?: boolean;
	onViewSession?: (sessionId: string, logId: string) => void;
	onFilterByParentRequestId?: (parentRequestId: string) => void;
}

export function LogDetailSheet({
	log,
	open,
	onOpenChange,
	handleDelete,
	onNavigate,
	hasPrev = false,
	hasNext = false,
	onViewSession,
	onFilterByParentRequestId,
}: LogDetailSheetProps) {
	const [pollingInterval, setPollingInterval] = useState(0);
	const {
		data: fullLog,
		isLoading,
		isError,
	} = useGetLogByIdQuery(log?.id ?? "", {
		skip: !open || !log?.id,
		pollingInterval,
	});

	const shouldPoll = isError || fullLog?.status === "processing";

	const isFullDataReady = log != null && (isError || (fullLog?.id === log.id && !isLoading));
	// Prefer full log when loaded; otherwise list row — enables prompt fetch in parallel with getLogById
	const selectedPromptId = log ? (fullLog?.id === log.id ? fullLog : log).selected_prompt_id : undefined;
	const { data: selectedPromptData } = useGetPromptQuery(selectedPromptId ?? "", {
		skip: !open || !selectedPromptId,
	});

	useEffect(() => {
		setPollingInterval(shouldPoll ? 2000 : 0);
	}, [shouldPoll]);

	// Keyboard navigation: arrow up/down to navigate between logs
	useHotkeys("up", () => onNavigate?.("prev"), { enabled: open && hasPrev, preventDefault: true });
	useHotkeys("down", () => onNavigate?.("next"), { enabled: open && hasNext, preventDefault: true });

	if (!log) return null;


	// Show a loader only on the initial fetch, not during background polling refetches.
	const displayLog: LogEntry = isFullDataReady && fullLog ? fullLog : log;
	const resolvedSelectedPromptName = selectedPromptData?.prompt?.name ?? displayLog.selected_prompt_name ?? "";

	return (
		<Sheet open={open} onOpenChange={onOpenChange}>
			<SheetContent className="flex w-full flex-col gap-4 overflow-x-hidden p-8 sm:max-w-[60%]">
				{!isFullDataReady ? (
					<div className="flex h-full items-center justify-center">
						<SheetTitle className="sr-only">Loading log details</SheetTitle>
						<Loader2 className="text-muted-foreground h-6 w-6 animate-spin" />
					</div>
				) : (
					<LogDetailView
						log={displayLog}
						resolvedSelectedPromptName={resolvedSelectedPromptName}
						handleDelete={handleDelete}
						onClose={() => onOpenChange(false)}
						onFilterByParentRequestId={onFilterByParentRequestId}
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
								<div className="flex items-center">
									<Button
										variant="ghost"
										className="size-8"
										disabled={!hasPrev}
										onClick={() => onNavigate?.("prev")}
										aria-label="Previous log"
										data-testid="logdetails-prev-button"
										type="button"
									>
										<ChevronUp className="size-4" />
									</Button>
									<Button
										variant="ghost"
										className="size-8"
										disabled={!hasNext}
										onClick={() => onNavigate?.("next")}
										aria-label="Next log"
										data-testid="logdetails-next-button"
										type="button"
									>
										<ChevronDown className="size-4" />
									</Button>
								</div>
							</>
						}
					/>
				)}
			</SheetContent>
		</Sheet>
	);
}