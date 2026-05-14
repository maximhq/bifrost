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
import { CodeEditor } from "@/components/ui/codeEditor";
import {
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuItem,
	DropdownMenuSeparator,
	DropdownMenuTrigger,
} from "@/components/ui/dropdownMenu";
import { DottedSeparator } from "@/components/ui/separator";
import { Sheet, SheetContent, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Status, StatusColors, Statuses } from "@/lib/constants/logs";
import { useGetMCPLogByIdQuery } from "@/lib/store";
import type { MCPToolLogEntry } from "@/lib/types/logs";
import { downloadAsJson } from "@/lib/utils/browser-download";
import { addMilliseconds, format, isValid } from "date-fns";
import { ChevronDown, ChevronUp, Download, Loader2, MoreVertical, Trash2 } from "lucide-react";
import { useState, type ReactNode } from "react";
import { useHotkeys } from "react-hotkeys-hook";
import { toast } from "sonner";

interface MCPLogDetailSheetProps {
	log: MCPToolLogEntry | null;
	open: boolean;
	onOpenChange: (open: boolean) => void;
	handleDelete?: (log: MCPToolLogEntry) => Promise<void>;
	onNavigate?: (direction: "prev" | "next") => void;
	hasPrev?: boolean;
	hasNext?: boolean;
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

export function MCPLogDetailSheet({
	log,
	open,
	onOpenChange,
	handleDelete,
	onNavigate,
	hasPrev = false,
	hasNext = false,
}: MCPLogDetailSheetProps) {
	const [deleteDialogOpen, setDeleteDialogOpen] = useState(false);
	const {
		data: fullLog,
		isLoading,
		isError,
	} = useGetMCPLogByIdQuery(log?.id ?? "", {
		skip: !open || !log?.id,
	});

	// Keyboard navigation: arrow up/down to navigate between logs
	useHotkeys("up", () => onNavigate?.("prev"), { enabled: open && hasPrev, preventDefault: true });
	useHotkeys("down", () => onNavigate?.("next"), { enabled: open && hasNext, preventDefault: true });

	if (!log) return null;

	const isFullDataReady = isError || (fullLog?.id === log.id && !isLoading);
	const displayLog = isFullDataReady && fullLog ? fullLog : log;

	if (!isFullDataReady) {
		return (
			<Sheet open={open} onOpenChange={onOpenChange}>
				<SheetContent className="flex w-full flex-col gap-4 overflow-x-hidden p-8 sm:max-w-[60%]">
					<div className="flex h-full items-center justify-center">
						<SheetTitle className="sr-only">Loading MCP log details</SheetTitle>
						<Loader2 className="text-muted-foreground h-6 w-6 animate-spin" />
					</div>
				</SheetContent>
			</Sheet>
		);
	}

	return (
		<Sheet open={open} onOpenChange={onOpenChange}>
			<SheetContent className="flex w-full flex-col gap-4 overflow-x-hidden p-8 sm:max-w-[60%]">
				<SheetHeader className="flex flex-row items-center px-0">
					<div className="flex w-full items-center justify-between">
						<SheetTitle className="flex w-fit items-center gap-2 font-medium">
							{displayLog.id && <p className="text-md max-w-full truncate">Request ID: {displayLog.id}</p>}
							<Badge variant="outline" className={`${StatusColors[getValidatedStatus(displayLog.status)]} uppercase`}>
								{displayLog.status}
							</Badge>
						</SheetTitle>
					</div>
					<div className="flex items-center">
						<Button
							variant="ghost"
							className="size-8"
							disabled={!hasPrev}
							onClick={() => onNavigate?.("prev")}
							aria-label="Previous log"
							data-testid="mcp-log-nav-prev"
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
							data-testid="mcp-log-nav-next"
							type="button"
						>
							<ChevronDown className="size-4" />
						</Button>
					</div>
					<AlertDialog open={deleteDialogOpen} onOpenChange={setDeleteDialogOpen}>
						<DropdownMenu>
							<DropdownMenuTrigger asChild>
								<Button variant="ghost" className="size-8" type="button">
									<MoreVertical className="h-3 w-3" />
								</Button>
							</DropdownMenuTrigger>
							<DropdownMenuContent align="end">
								<DropdownMenuItem
									data-testid="export-log-json"
									onClick={() => downloadAsJson(displayLog, `mcp-log-${displayLog.id ?? "export"}.json`)}
								>
									<Download className="h-4 w-4" />
									Export as JSON
								</DropdownMenuItem>
								{handleDelete ? (
									<>
										<DropdownMenuSeparator />
										<AlertDialogTrigger asChild>
											<DropdownMenuItem variant="destructive">
												<Trash2 className="h-4 w-4" />
												Delete log
											</DropdownMenuItem>
										</AlertDialogTrigger>
									</>
								) : null}
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
									onClick={async (e) => {
										e.preventDefault();
										if (!handleDelete) return;
										try {
											await handleDelete(displayLog);
											setDeleteDialogOpen(false);
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
								value={
									isValid(new Date(displayLog.timestamp))
										? format(new Date(displayLog.timestamp), "yyyy-MM-dd hh:mm:ss aa")
										: "Invalid date"
								}
							/>
							<LogEntryDetailsView
								className="w-full"
								label="End Timestamp"
								value={
									isValid(new Date(displayLog.timestamp))
										? format(addMilliseconds(new Date(displayLog.timestamp), displayLog.latency || 0), "yyyy-MM-dd hh:mm:ss aa")
										: "Invalid date"
								}
							/>
							<LogEntryDetailsView
								className="w-full"
								label="Latency"
								value={displayLog.latency ? `${displayLog.latency.toFixed(2)}ms` : "NA"}
							/>
						</div>
					</div>
					<DottedSeparator />
					<div className="space-y-4">
						<BlockHeader title="Request Details" />
						<div className="grid w-full grid-cols-3 items-start justify-between gap-4">
							<LogEntryDetailsView
								className="col-span-2 w-full"
								label="Tool Name"
								value={<span className="font-mono text-sm">{displayLog.tool_name}</span>}
							/>
							<LogEntryDetailsView
								className="w-full"
								label="Server"
								value={
									displayLog.server_label ? (
										<Badge variant="secondary" className="font-mono">
											{displayLog.server_label}
										</Badge>
									) : (
										"-"
									)
								}
							/>
							{displayLog.virtual_key && <LogEntryDetailsView className="w-full" label="Virtual Key" value={displayLog.virtual_key.name} />}
							{displayLog.llm_request_id && (
								<LogEntryDetailsView
									className="col-span-3 w-full"
									label="LLM Request ID"
									value={<span className="font-mono text-xs">{displayLog.llm_request_id}</span>}
								/>
							)}
						</div>
					</div>
				</div>

				{/* Arguments */}
				{displayLog.arguments && (
					<div className="w-full rounded-sm border">
						<div className="border-b px-6 py-2 text-sm font-medium">Arguments</div>
						<CodeEditor
							className="z-0 w-full"
							shouldAdjustInitialHeight={true}
							maxHeight={250}
							wrap={true}
							code={
								typeof displayLog.arguments === "string"
									? displayLog.arguments
									: JSON.stringify(displayLog.arguments as Record<string, unknown>, null, 2)
							}
							lang="json"
							readonly={true}
							options={{ scrollBeyondLastLine: false, collapsibleBlocks: true, lineNumbers: "off", alwaysConsumeMouseWheel: false }}
						/>
					</div>
				)}

				{/* Result */}
				{displayLog.result && displayLog.status !== "processing" && (
					<div className="w-full rounded-sm border">
						<div className="border-b px-6 py-2 text-sm font-medium">Result</div>
						<CodeEditor
							className="z-0 w-full"
							shouldAdjustInitialHeight={true}
							maxHeight={350}
							wrap={true}
							code={typeof displayLog.result === "string" ? displayLog.result : JSON.stringify(displayLog.result, null, 2)}
							lang="json"
							readonly={true}
							options={{ scrollBeyondLastLine: false, collapsibleBlocks: true, lineNumbers: "off", alwaysConsumeMouseWheel: false }}
						/>
					</div>
				)}

				{/* Metadata */}
				{displayLog.metadata && Object.keys(displayLog.metadata).length > 0 && (
					<div className="space-y-4 rounded-sm border px-6 py-4">
						<BlockHeader title="Metadata" />
						<div className="grid w-full grid-cols-3 items-start justify-between gap-4">
							{Object.entries(displayLog.metadata).map(([key, value]) => (
								<LogEntryDetailsView key={key} className="w-full" label={key} value={String(value)} />
							))}
						</div>
					</div>
				)}

				{/* Error Details */}
				{displayLog.error_details && (
					<div className="border-destructive/50 w-full rounded-sm border">
						<div className="border-destructive/50 text-destructive border-b px-6 py-2 text-sm font-medium">Error Details</div>
						<CodeEditor
							className="z-0 w-full"
							shouldAdjustInitialHeight={true}
							maxHeight={250}
							wrap={true}
							code={JSON.stringify(displayLog.error_details, null, 2)}
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