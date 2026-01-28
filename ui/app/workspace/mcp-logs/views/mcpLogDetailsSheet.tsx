"use client";

import { CodeEditor } from "@/app/workspace/logs/views/codeEditor";
import {
	AlertDialog,
	AlertDialogAction,
	AlertDialogCancel,
	AlertDialogContent,
	AlertDialogDescription,
	AlertDialogFooter,
	AlertDialogHeader,
	AlertDialogTitle,
	AlertDialogTrigger,
} from "@/components/ui/alertDialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdownMenu";
import { DottedSeparator } from "@/components/ui/separator";
import { Sheet, SheetContent, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Status, StatusColors, Statuses } from "@/lib/constants/logs";
import type { MCPToolLogEntry } from "@/lib/types/logs";
import { MoreVertical, Trash2 } from "lucide-react";
import moment from "moment";
import type { ReactNode } from "react";
import { toast } from "sonner";

interface MCPLogDetailSheetProps {
	log: MCPToolLogEntry | null;
	open: boolean;
	onOpenChange: (open: boolean) => void;
	handleDelete: (log: MCPToolLogEntry) => Promise<void>;
}

const LogEntryDetailsView = ({ label, value, className }: { label: string; value: React.ReactNode; className?: string }) => (
	<div className={className}>
		<div className="text-muted-foreground text-xs">{label}</div>
		<div className="text-sm font-medium">{value}</div>
	</div>
);

const BlockHeader = ({ title, icon }: { title: string; icon?: ReactNode }) => {
	return (
		<div className="flex items-center gap-2">
			{icon}
			<div className="text-sm font-medium">{title}</div>
		</div>
	);
};

// Helper function to validate status and return a safe Status value
const getValidatedStatus = (status: string): Status => {
	// Check if status is a valid Status by checking against Statuses array
	if (Statuses.includes(status as Status)) {
		return status as Status;
	}
	// Fallback to "processing" for unknown statuses
	return "processing";
};

export function MCPLogDetailSheet({ log, open, onOpenChange, handleDelete }: MCPLogDetailSheetProps) {
	if (!log) return null;

	return (
		<Sheet open={open} onOpenChange={onOpenChange}>
			<SheetContent className="dark:bg-card flex w-full flex-col gap-4 overflow-x-hidden bg-white p-8 sm:max-w-[60%]">
				<SheetHeader className="flex flex-row items-center px-0">
					<div className="flex w-full items-center justify-between">
						<SheetTitle className="flex w-fit items-center gap-2 font-medium">
							{log.id && <p className="text-md max-w-full truncate">Request ID: {log.id}</p>}
							<Badge variant="outline" className={`${StatusColors[getValidatedStatus(log.status)]} uppercase`}>
								{log.status}
							</Badge>
						</SheetTitle>
					</div>
					<AlertDialog>
						<DropdownMenu>
							<DropdownMenuTrigger asChild>
								<Button variant="ghost" size="icon">
									<MoreVertical className="h-3 w-3" />
								</Button>
							</DropdownMenuTrigger>
							<DropdownMenuContent align="end">
								<AlertDialogTrigger asChild>
									<DropdownMenuItem variant="destructive">
										<Trash2 className="h-4 w-4" />
										Delete log
									</DropdownMenuItem>
								</AlertDialogTrigger>
							</DropdownMenuContent>
						</DropdownMenu>
						<AlertDialogContent>
							<AlertDialogHeader>
								<AlertDialogTitle>Are you sure you want to delete this log?</AlertDialogTitle>
								<AlertDialogDescription>This action cannot be undone. This will permanently delete the log entry.</AlertDialogDescription>
							</AlertDialogHeader>
							<AlertDialogFooter>
								<AlertDialogCancel>Cancel</AlertDialogCancel>
								<AlertDialogAction
									onClick={async () => {
										try {
											await handleDelete(log);
											onOpenChange(false);
										} catch (err) {
											const errorMessage = err instanceof Error ? err.message : "Failed to delete log";
											toast.error(errorMessage);
											// Keep dialog open on error so user can see the error and retry
										}
									}}
								>
									Delete
								</AlertDialogAction>
							</AlertDialogFooter>
						</AlertDialogContent>
					</AlertDialog>
				</SheetHeader>
				<div className="space-y-4 rounded-sm border px-6 py-4">
					<div className="space-y-4">
						<BlockHeader title="Timings" />
						<div className="grid w-full grid-cols-3 items-center justify-between gap-4">
							<LogEntryDetailsView
								className="w-full"
								label="Start Timestamp"
								value={moment(log.timestamp).format("YYYY-MM-DD HH:mm:ss A")}
							/>
							<LogEntryDetailsView
								className="w-full"
								label="End Timestamp"
								value={moment(log.timestamp)
									.add(log.latency || 0, "ms")
									.format("YYYY-MM-DD HH:mm:ss A")}
							/>
							<LogEntryDetailsView className="w-full" label="Latency" value={log.latency ? `${log.latency.toFixed(2)}ms` : "NA"} />
						</div>
					</div>
					<DottedSeparator />
					<div className="space-y-4">
						<BlockHeader title="Request Details" />
						<div className="grid w-full grid-cols-3 items-start justify-between gap-4">
							<LogEntryDetailsView
								className="col-span-2 w-full"
								label="Tool Name"
								value={<span className="font-mono text-sm">{log.tool_name}</span>}
							/>
							<LogEntryDetailsView
								className="w-full"
								label="Server"
								value={
									log.server_label ? (
										<Badge variant="secondary" className="font-mono">
											{log.server_label}
										</Badge>
									) : (
										"-"
									)
								}
							/>
							{log.virtual_key && <LogEntryDetailsView className="w-full" label="Virtual Key" value={log.virtual_key.name} />}
							{log.llm_request_id && (
								<LogEntryDetailsView
									className="col-span-3 w-full"
									label="LLM Request ID"
									value={<span className="font-mono text-xs">{log.llm_request_id}</span>}
								/>
							)}
						</div>
					</div>
				</div>

				{/* Arguments */}
				{log.arguments && (
					<div className="w-full rounded-sm border">
						<div className="border-b px-6 py-2 text-sm font-medium">Arguments</div>
						<CodeEditor
							className="z-0 w-full"
							shouldAdjustInitialHeight={true}
							maxHeight={250}
							wrap={true}
							code={typeof log.arguments === "string" ? log.arguments : JSON.stringify(log.arguments as Record<string, unknown>, null, 2)}
							lang="json"
							readonly={true}
							options={{ scrollBeyondLastLine: false, collapsibleBlocks: true, lineNumbers: "off", alwaysConsumeMouseWheel: false }}
						/>
					</div>
				)}

				{/* Result */}
				{log.result && log.status !== "processing" && (
					<div className="w-full rounded-sm border">
						<div className="border-b px-6 py-2 text-sm font-medium">Result</div>
						<CodeEditor
							className="z-0 w-full"
							shouldAdjustInitialHeight={true}
							maxHeight={350}
							wrap={true}
							code={typeof log.result === "string" ? log.result : JSON.stringify(log.result, null, 2)}
							lang="json"
							readonly={true}
							options={{ scrollBeyondLastLine: false, collapsibleBlocks: true, lineNumbers: "off", alwaysConsumeMouseWheel: false }}
						/>
					</div>
				)}

				{/* Error Details */}
				{log.error_details && (
					<div className="border-destructive/50 w-full rounded-sm border">
						<div className="border-destructive/50 text-destructive border-b px-6 py-2 text-sm font-medium">Error Details</div>
						<CodeEditor
							className="z-0 w-full"
							shouldAdjustInitialHeight={true}
							maxHeight={250}
							wrap={true}
							code={JSON.stringify(log.error_details, null, 2)}
							lang="json"
							readonly={true}
							options={{ scrollBeyondLastLine: false, collapsibleBlocks: true, lineNumbers: "off", alwaysConsumeMouseWheel: false }}
						/>
					</div>
				)}
			</SheetContent>
		</Sheet>
	);
}
