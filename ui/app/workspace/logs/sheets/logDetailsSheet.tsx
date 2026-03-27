"use client";

import { Button } from "@/components/ui/button";
import { Sheet, SheetContent } from "@/components/ui/sheet";
import { useLazyGetLogByIdQuery } from "@/lib/store/apis/logsApi";
import type { LogEntry } from "@/lib/types/logs";
import { Loader2 } from "lucide-react";
import { useEffect } from "react";
import { LogDetailView } from "./logDetailView";

interface LogDetailSheetProps {
	log: LogEntry | null;
	open: boolean;
	onOpenChange: (open: boolean) => void;
	handleDelete: (log: LogEntry) => void;
	onViewSession?: (sessionId: string, logId: string) => void;
	onFilterByParentRequestId?: (parentRequestId: string) => void;
}

export function LogDetailSheet({ log, open, onOpenChange, handleDelete, onViewSession, onFilterByParentRequestId }: LogDetailSheetProps) {
	const [fetchLog, { data: fullLog, isFetching }] = useLazyGetLogByIdQuery();

	useEffect(() => {
		if (open && log?.id) {
			fetchLog(log.id);
		}
	}, [open, log?.id, fetchLog]);

	if (!log) return null;

	const isFullDataReady = fullLog?.id === log.id && !isFetching;
	const displayLog = isFullDataReady ? fullLog : log;

	return (
		<Sheet open={open} onOpenChange={onOpenChange}>
			<SheetContent className="flex w-full flex-col gap-4 overflow-x-hidden p-8 sm:max-w-[60%]">
				{!isFullDataReady ? (
					<div className="flex h-full items-center justify-center">
						<Loader2 className="text-muted-foreground h-6 w-6 animate-spin" />
					</div>
				) : (
					<LogDetailView
						log={displayLog}
						handleDelete={handleDelete}
						onClose={() => onOpenChange(false)}
						onFilterByParentRequestId={onFilterByParentRequestId}
						headerAction={
							displayLog.parent_request_id && onViewSession ? (
								<Button
									variant="outline"
									size="sm"
									onClick={() => onViewSession(displayLog.parent_request_id as string, displayLog.id)}
								>
									View Session
								</Button>
							) : null
						}
					/>
				)}
			</SheetContent>
		</Sheet>
	);
}
