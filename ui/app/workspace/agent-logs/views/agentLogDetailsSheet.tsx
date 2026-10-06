import { formatLatency } from "@/app/workspace/dashboard/utils/chartUtils";
import BlockHeader from "@/app/workspace/logs/views/blockHeader";
import LogEntryDetailsView from "@/app/workspace/logs/views/logEntryDetailsView";
import PluginLogsView from "@/app/workspace/logs/views/pluginLogsView";
import { SheetNavigationButtons } from "@/components/sheetNavigationButtons";
import {
	AlertDialog,
	AlertDialogAction,
	AlertDialogCancel,
	AlertDialogContent,
	AlertDialogDescription,
	AlertDialogFooter,
	AlertDialogHeader,
	AlertDialogTitle,
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
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { useCopyToClipboard } from "@/hooks/useCopyToClipboard";
import { useSheetNavigation } from "@/hooks/useSheetNavigation";
import { useGetAgentLogOperationByIdQuery } from "@/lib/store/apis/agentLogsApi";
import type { AgentLogDetail, AgentLogFilters, AgentLogStatus, AgentLogSummary } from "@/lib/types/agentLogs";
import { OverheadBreakdown } from "@/components/logs/overheadBreakdown";
import { cn } from "@/lib/utils";
import { downloadAsJson } from "@/lib/utils/browser-download";
import { Link } from "@tanstack/react-router";
import { format, isValid } from "date-fns";
import { ChevronDown, Download, Loader2, MoreVertical, Trash2 } from "lucide-react";
import { useEffect, useMemo, useState, type ReactNode } from "react";
import { toast } from "sonner";
import {
	A2AConversationHistory,
	A2AOperationConversation,
	isA2AConversationOperation,
	isA2APushConversationOperation,
} from "./agentContextDetailsSheet";
import { A2APayloadView } from "./a2aPayloadView";
import { CorrelatedLogsContent, useCorrelatedLogCounts } from "./correlatedLogs";

interface AgentLogDetailSheetProps {
	log: AgentLogSummary | null;
	open: boolean;
	onOpenChange: (open: boolean) => void;
	/** Narrow the page to operations matching one stored field. */
	onFilter?: (filters: FilterTarget) => void;
	handleDelete?: (log: AgentLogSummary) => Promise<void>;
	onViewContext?: (contextId: string, logId: string) => void;
	onNavigate?: (direction: "prev" | "next") => void;
	hasPrev?: boolean;
	hasNext?: boolean;
}

const pillStyles: Record<AgentLogStatus, string> = {
	success: "border-chart-success/30 bg-chart-success/10 text-chart-success-ink",
	error: "border-chart-error/30 bg-chart-error/10 text-chart-error-ink",
	processing: "bg-blue-50 text-blue-700 border-blue-200 dark:bg-blue-950/40 dark:text-blue-400 dark:border-blue-900",
};

const pillDotStyles: Record<AgentLogStatus, string> = {
	success: "bg-chart-success",
	error: "bg-chart-error",
	processing: "bg-blue-500",
};

function StatusPill({ status }: { status: AgentLogStatus }) {
	const tone: AgentLogStatus = status in pillStyles ? status : "processing";
	return (
		<span
			className={cn("inline-flex items-center gap-1.5 rounded-sm border px-2 py-0.5 text-[11px] font-semibold uppercase", pillStyles[tone])}
		>
			<span className={cn("h-1.5 w-1.5 rounded-sm", pillDotStyles[tone])} />
			{status}
		</span>
	);
}

type FilterTarget = Partial<Omit<AgentLogFilters, "period" | "start_time" | "end_time">>;

/** Values filter on left-click and copy on right-click. */
function InteractiveValue({
	value,
	label,
	filter,
	testId,
	className,
	onFilter,
}: {
	value: string;
	label: string;
	filter?: FilterTarget;
	onFilter?: (filters: FilterTarget) => void;
	testId?: string;
	className?: string;
}) {
	const { copy } = useCopyToClipboard({ successMessage: "Copied" });
	return (
		<Tooltip>
			<TooltipTrigger asChild>
				<button
					type="button"
					onClick={(event) => {
						event.stopPropagation();
						if (filter) onFilter?.(filter);
					}}
					onContextMenu={(event) => {
						event.preventDefault();
						event.stopPropagation();
						copy(value);
					}}
					className={cn(
						"focus-visible:ring-ring min-w-0 max-w-full truncate text-left underline-offset-2 transition hover:underline focus-visible:ring-2 focus-visible:outline-none",
						filter ? "cursor-pointer" : "cursor-context-menu",
						className,
					)}
					aria-label={filter ? `Filter by ${label}` : label}
					data-testid={testId}
				>
					{value}
				</button>
			</TooltipTrigger>
			<TooltipContent>{filter ? "Click to filter · Right-click to copy" : "Right-click to copy"}</TooltipContent>
		</Tooltip>
	);
}

function IdRow({
	label,
	value,
	filter,
	onFilter,
	testId,
}: {
	label: string;
	value?: string;
	filter?: FilterTarget;
	onFilter?: (filters: FilterTarget) => void;
	testId?: string;
}) {
	if (!value) return null;
	return (
		<div className="mt-1 flex items-center gap-2 first:mt-0">
			<div className="text-muted-foreground w-24 shrink-0 text-[10.5px] font-semibold tracking-wider uppercase">{label}</div>
			<InteractiveValue
				value={value}
				label={label}
				filter={filter}
				onFilter={onFilter}
				testId={testId}
				className="text-foreground font-mono text-[13px]"
			/>
		</div>
	);
}

function HeroStat({
	label,
	value,
	sub,
	mono = false,
	valueClass,
	hasRightBorder = false,
}: {
	label: string;
	value: ReactNode;
	sub?: ReactNode;
	mono?: boolean;
	valueClass?: string;
	hasRightBorder?: boolean;
}) {
	return (
		<div className={cn("border-border/70 min-w-0 border-b px-5 py-3 md:border-b-0", hasRightBorder && "md:border-r")}>
			<div className="text-muted-foreground text-[10.5px] font-semibold tracking-wider uppercase">{label}</div>
			<div className={cn("mt-0.5 truncate text-[18px] font-semibold tabular-nums", mono && "font-mono text-[15px]", valueClass)}>
				{value}
			</div>
			{sub ? <div className="text-muted-foreground mt-0.5 truncate text-[11px]">{sub}</div> : null}
		</div>
	);
}

// Bodies are JSON strings; the Raw JSON tab shows the record verbatim.
function PayloadBlock({ title, code }: { title: string; code: string }) {
	return (
		<div className="w-full rounded-sm border">
			<div className="border-b px-4 py-2 text-sm font-medium md:px-6">{title}</div>
			<CodeEditor
				className="z-0 w-full"
				shouldAdjustInitialHeight={true}
				maxHeight={350}
				wrap={true}
				code={code}
				lang="json"
				readonly={true}
				options={{
					scrollBeyondLastLine: false,
					collapsibleBlocks: true,
					lineNumbers: "off",
					alwaysConsumeMouseWheel: false,
				}}
			/>
		</div>
	);
}

function getPluginLogCount(pluginLogs?: string): number {
	if (!pluginLogs) return 0;
	try {
		const parsed: unknown = JSON.parse(pluginLogs);
		if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return 0;
		return Object.values(parsed).reduce<number>((count, entries) => count + (Array.isArray(entries) ? entries.length : 0), 0);
	} catch {
		return 0;
	}
}

function eventLabel(row: AgentLogSummary): string {
	if (row.event_type === "artifact_update") return "Artifact updated";
	if (row.event_type === "status_update" && row.task_state)
		return row.task_state
			.replace(/^TASK_STATE_/, "")
			.replaceAll("_", " ")
			.toLowerCase();
	if (row.event_type === "task" && row.task_state)
		return `Task ${row.task_state
			.replace(/^TASK_STATE_/, "")
			.replaceAll("_", " ")
			.toLowerCase()}`;
	if (row.event_type === "message") return "Message received";
	return (row.event_type || "Event").replaceAll("_", " ");
}

function eventExplanation(row: AgentLogSummary): string {
	if (row.event_type === "artifact_update") return "The agent emitted or updated output for this task.";
	if (row.event_type === "status_update") return "The agent reported a task state change.";
	if (row.event_type === "task") return "The agent returned the current task snapshot.";
	if (row.event_type === "message") return "The agent emitted a message without creating or updating a task.";
	return "A protocol event observed while this operation was active.";
}

function EventDetailValue({
	label,
	value,
	filter,
	onFilter,
}: {
	label: string;
	value?: string | number;
	filter?: FilterTarget;
	onFilter?: (filters: FilterTarget) => void;
}) {
	if (value === undefined || value === "") return null;
	const rendered = String(value);
	return (
		<div className="min-w-0 space-y-1">
			<div className="text-muted-foreground text-[10.5px] font-semibold tracking-wider uppercase">{label}</div>
			<InteractiveValue
				value={rendered}
				label={label}
				filter={filter}
				onFilter={onFilter}
				className="text-foreground block max-w-full font-mono text-xs"
			/>
		</div>
	);
}

function FormattedEventDetail({ detail, onFilter }: { detail: AgentLogDetail; onFilter?: (filters: FilterTarget) => void }) {
	const timestamp = detail.timestamp ? new Date(detail.timestamp) : null;
	const fields: [string, string | number | undefined, FilterTarget | undefined][] = [
		["Event ID", detail.id, { search: detail.id }],
		["Sequence", detail.event_sequence, undefined],
		["Event type", detail.event_type, undefined],
		["Task state", detail.task_state, detail.task_state ? { task_state: [detail.task_state] } : undefined],
		["Task ID", detail.task_id, detail.task_id ? { task_id: detail.task_id } : undefined],
		["Context ID", detail.context_id, detail.context_id ? { context_id: detail.context_id } : undefined],
		["Message ID", detail.message_id, detail.message_id ? { search: detail.message_id } : undefined],
		["Artifact ID", detail.artifact_id, detail.artifact_id ? { search: detail.artifact_id } : undefined],
		["Request ID", detail.request_id, { request_id: detail.request_id }],
		["Trace ID", detail.trace_id, detail.trace_id ? { trace_id: detail.trace_id } : undefined],
		["Agent", detail.agent_name, { agent_name: [detail.agent_name] }],
		["Operation", detail.operation, { operation: [detail.operation] }],
		["Status", detail.status, { status: [detail.status] }],
		["Content type", detail.content_type, undefined],
		["User ID", detail.user_id, detail.user_id ? { user_id: [detail.user_id] } : undefined],
		["Virtual key ID", detail.virtual_key_id, detail.virtual_key_id ? { virtual_key_id: [detail.virtual_key_id] } : undefined],
		["Push config ID", detail.push_config_id, detail.push_config_id ? { push_config_id: detail.push_config_id } : undefined],
		["Delivery ID", detail.delivery_id, detail.delivery_id ? { delivery_id: detail.delivery_id } : undefined],
		["Attempt ID", detail.attempt_id, detail.attempt_id ? { attempt_id: detail.attempt_id } : undefined],
	];

	return (
		<div className="space-y-3">
			<details className="group rounded-sm border">
				<summary className="hover:bg-muted/30 flex cursor-pointer items-center justify-between px-3 py-2 text-sm transition">
					<span className="font-medium">Event details</span>
					<span className="text-muted-foreground flex items-center gap-2 text-xs">
						IDs, timing, and correlation
						<ChevronDown className="size-3.5 transition-transform group-open:rotate-180" />
					</span>
				</summary>
				<div className="grid grid-cols-1 gap-x-5 gap-y-3 border-t p-3 sm:grid-cols-2">
					<EventDetailValue
						label="Timestamp"
						value={timestamp && isValid(timestamp) ? format(timestamp, "yyyy-MM-dd HH:mm:ss.SSS") : detail.timestamp}
					/>
					{fields.map(([label, value, filter]) => (
						<EventDetailValue key={label} label={label} value={value} filter={filter} onFilter={onFilter} />
					))}
				</div>
			</details>
			<A2APayloadView eventBody={detail.event_body} />
		</div>
	);
}

function rawEventRecord(detail: AgentLogDetail): string {
	if (!detail.event_body) return JSON.stringify(detail, null, 2);
	try {
		return JSON.stringify({ ...detail, event_body: JSON.parse(detail.event_body) }, null, 2);
	} catch {
		return JSON.stringify(detail, null, 2);
	}
}

export function AgentLogDetailSheet({
	log,
	open,
	onOpenChange,
	onFilter,
	handleDelete,
	onViewContext,
	onNavigate,
	hasPrev = false,
	hasNext = false,
}: AgentLogDetailSheetProps) {
	const {
		data: fullLog,
		isLoading,
		isError,
	} = useGetAgentLogOperationByIdQuery(log?.id ?? "", {
		skip: !open || !log?.id,
	});

	const correlatedRows = useMemo(() => {
		const rows = fullLog?.events ?? [];
		return [...rows].sort((a, b) => {
			if (a.event_sequence != null && b.event_sequence != null) return a.event_sequence - b.event_sequence;
			return new Date(a.timestamp).getTime() - new Date(b.timestamp).getTime();
		});
	}, [fullLog?.events]);
	const totalCorrelatedEvents = correlatedRows.length;

	const [expandedEventIds, setExpandedEventIds] = useState<Set<string>>(new Set());
	const [rawEventIds, setRawEventIds] = useState<Set<string>>(new Set());

	useEffect(() => {
		setExpandedEventIds(new Set());
		setRawEventIds(new Set());
	}, [log?.id]);

	const toggleEvent = (row: AgentLogSummary) => {
		setExpandedEventIds((current) => {
			const next = new Set(current);
			if (next.has(row.id)) next.delete(row.id);
			else next.add(row.id);
			return next;
		});
	};

	const toggleEventRaw = (eventID: string) => {
		setRawEventIds((current) => {
			const next = new Set(current);
			if (next.has(eventID)) next.delete(eventID);
			else next.add(eventID);
			return next;
		});
	};

	// Keyboard navigation: arrow up/down to navigate between operations
	const { prev: prevKeys, next: nextKeys } = useSheetNavigation({
		enabled: open,
		hasPrev,
		hasNext,
		onNavigate: (direction) => onNavigate?.(direction),
	});

	const [dropdownOpen, setDropdownOpen] = useState(false);
	const [deleteDialogOpen, setDeleteDialogOpen] = useState(false);
	const [activeTab, setActiveTab] = useState({ correlationKey: "", value: "" });

	const isSelectedOperation = Boolean(log && fullLog && (fullLog.id === log.id || fullLog.request_id === log.request_id));
	const isFullDataReady = Boolean(log) && (isError || (isSelectedOperation && !isLoading));
	const displayLog = log ? (isFullDataReady && fullLog ? fullLog : log) : null;
	const isCorrelatedConversation = displayLog ? isA2AConversationOperation(displayLog.operation) : false;
	const logCorrelation = useMemo(
		() => (isCorrelatedConversation && displayLog?.task_id ? { agent_correlation_id: displayLog.task_id } : null),
		[displayLog?.task_id, isCorrelatedConversation],
	);
	const correlationKey = logCorrelation?.agent_correlation_id ?? "";
	const { llmCount, mcpCount } = useCorrelatedLogCounts(logCorrelation, open);

	if (!log || !displayLog) return null;

	if (!isFullDataReady) {
		return (
			<Sheet open={open} onOpenChange={onOpenChange}>
				<SheetContent className="border-secondary flex w-full flex-col gap-4 overflow-x-hidden border p-4 sm:max-w-[60%] md:p-8">
					<div className="flex h-full items-center justify-center">
						<SheetTitle className="sr-only">Loading A2A log details</SheetTitle>
						<Loader2 className="text-muted-foreground h-6 w-6 animate-spin" />
					</div>
				</SheetContent>
			</Sheet>
		);
	}

	const detail = isFullDataReady && isSelectedOperation ? fullLog : undefined;
	const timestampDate = displayLog.timestamp ? new Date(displayLog.timestamp) : null;
	const hasValidTimestamp = Boolean(timestampDate && isValid(timestampDate));
	const hasValidLatency = displayLog.latency != null && !isNaN(displayLog.latency);
	const endTimestamp = hasValidTimestamp && hasValidLatency ? new Date(timestampDate!.getTime() + displayLog.latency!) : null;
	// Only request-kind rows carry the latency breakdown; events have no timings beyond their timestamp.
	const hasTimings =
		hasValidLatency ||
		displayLog.upstream_latency != null ||
		displayLog.overhead_latency != null ||
		(displayLog.overhead_breakdown?.length ?? 0) > 0;
	const pluginLogCount = getPluginLogCount(detail?.plugin_logs);
	const isConversationOperation = isA2AConversationOperation(displayLog.operation);
	const isPushConversationOperation = isA2APushConversationOperation(displayLog.operation);
	const conversationLog = (isConversationOperation || isPushConversationOperation) && detail ? detail : null;
	const hasConversation = Boolean(
		conversationLog && (conversationLog.request_body || conversationLog.response_body || conversationLog.error_details),
	);
	const hasPayload = Boolean(detail && (detail.request_body || detail.response_body || detail.error_details));
	const defaultTab = hasConversation
		? "conversation"
		: hasPayload
			? "payload"
			: correlatedRows.length > 0
				? "events"
				: pluginLogCount > 0
					? "plugins"
					: "raw";
	const isPushRelated = Boolean(displayLog.push_config_id || displayLog.delivery_id || displayLog.attempt_id);
	const governanceLinks = (
		[
			["User", "Users", "/workspace/governance/users", "selected_user", displayLog.user_name, displayLog.user_id, undefined, undefined],
			[
				"Team",
				"Teams",
				"/workspace/governance/teams",
				"selected_team",
				displayLog.team_name,
				displayLog.team_id,
				displayLog.team_names,
				displayLog.team_ids,
			],
			[
				"Customer",
				"Customers",
				"/workspace/governance/customers",
				"selected_customer",
				displayLog.customer_name,
				displayLog.customer_id,
				displayLog.customer_names,
				displayLog.customer_ids,
			],
			[
				"Business Unit",
				"Business Units",
				"/workspace/governance/business-units",
				"selected_business_unit",
				displayLog.business_unit_name,
				displayLog.business_unit_id,
				displayLog.business_unit_names,
				displayLog.business_unit_ids,
			],
			[
				"Project",
				"Projects",
				"/workspace/governance/projects",
				"selected_project",
				displayLog.project_name,
				displayLog.project_id,
				undefined,
				undefined,
			],
		] as const
	)
		.map(([label, pluralLabel, route, searchKey, name, id, names, ids]) => {
			const items = ids?.length
				? ids.map((itemID, index) => ({ id: itemID, name: names?.[index] || itemID }))
				: id
					? [{ id, name: name || id }]
					: [];
			return { label, pluralLabel, route, searchKey, items };
		})
		.filter(({ items }) => items.length > 0);

	const diagnosticSections = (
		[
			{
				title: "Record identifiers",
				fields: [
					["Record ID", displayLog.id, { search: displayLog.id }],
					["Request ID", displayLog.request_id, { request_id: displayLog.request_id }],
					["Trace ID", displayLog.trace_id, displayLog.trace_id ? { trace_id: displayLog.trace_id } : undefined],
				],
			},
			{
				title: "Protocol correlation",
				fields: [
					["Task ID", displayLog.task_id, displayLog.task_id ? { task_id: displayLog.task_id } : undefined],
					["Context ID", displayLog.context_id, displayLog.context_id ? { context_id: displayLog.context_id } : undefined],
					["Message ID", displayLog.message_id, displayLog.message_id ? { search: displayLog.message_id } : undefined],
					[
						"Request Message ID",
						displayLog.request_message_id,
						displayLog.request_message_id ? { search: displayLog.request_message_id } : undefined,
					],
					[
						"Response Message ID",
						displayLog.response_message_id,
						displayLog.response_message_id ? { search: displayLog.response_message_id } : undefined,
					],
					["Artifact ID", displayLog.artifact_id, displayLog.artifact_id ? { search: displayLog.artifact_id } : undefined],
					["Event Sequence", displayLog.event_sequence, undefined],
					["Event Type", displayLog.event_type, undefined],
					["Task State", displayLog.task_state, displayLog.task_state ? { task_state: [displayLog.task_state] } : undefined],
				],
			},
			{
				title: "Push correlation",
				fields: [
					[
						"Push Config ID",
						displayLog.push_config_id,
						displayLog.push_config_id ? { push_config_id: displayLog.push_config_id } : undefined,
					],
					["Delivery ID", displayLog.delivery_id, displayLog.delivery_id ? { delivery_id: displayLog.delivery_id } : undefined],
					["Attempt ID", displayLog.attempt_id, displayLog.attempt_id ? { attempt_id: displayLog.attempt_id } : undefined],
				],
			},
		] satisfies { title: string; fields: [string, string | number | undefined, FilterTarget | undefined][] }[]
	).map((section) => ({
		...section,
		fields: section.fields.filter(([, value]) => value !== undefined && value !== ""),
	}));

	return (
		<Sheet open={open} onOpenChange={onOpenChange}>
			<SheetContent className="border-secondary flex w-full flex-col gap-4 overflow-x-hidden border p-4 sm:max-w-[60%] md:p-8">
				<SheetHeader className="flex flex-row items-center px-0" headerClassName="mb-0">
					<div className="flex w-full items-center gap-2">
						<SheetNavigationButtons
							hasPrev={hasPrev}
							hasNext={hasNext}
							onNavigate={(dir) => onNavigate?.(dir)}
							prevKeys={prevKeys}
							nextKeys={nextKeys}
							entityLabel="operation"
						/>
						<SheetTitle className="flex w-fit items-center gap-2 font-medium">
							<span className="text-foreground text-sm font-medium">Agent operation</span>
						</SheetTitle>
						{displayLog.context_id && onViewContext ? (
							<Button
								variant="outline"
								size="sm"
								data-testid="a2alogdetails-view-context"
								onClick={() => onViewContext(displayLog.context_id!, displayLog.id)}
							>
								View session
							</Button>
						) : null}
					</div>
					<AlertDialog open={deleteDialogOpen} onOpenChange={setDeleteDialogOpen}>
						<DropdownMenu open={dropdownOpen} onOpenChange={setDropdownOpen}>
							<DropdownMenuTrigger asChild>
								<Button variant="ghost" className="size-8" type="button" aria-label="Log actions">
									<MoreVertical className="h-3 w-3" />
								</Button>
							</DropdownMenuTrigger>
							<DropdownMenuContent align="end">
								<DropdownMenuItem
									data-testid="a2alogdetails-export-log-json"
									onSelect={(e) => {
										e.preventDefault();
										downloadAsJson(displayLog, `agent-log-${displayLog.id ?? "export"}.json`);
										setDropdownOpen(false);
									}}
								>
									<Download className="h-4 w-4" />
									Export as JSON
								</DropdownMenuItem>
								{handleDelete ? (
									<>
										<DropdownMenuSeparator />
										<DropdownMenuItem
											variant="destructive"
											onSelect={(e) => {
												e.preventDefault();
												setDeleteDialogOpen(true);
												setDropdownOpen(false);
											}}
										>
											<Trash2 className="h-4 w-4" />
											Delete log
										</DropdownMenuItem>
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

				<div className="border-border rounded-sm border">
					<div className="flex items-start justify-between gap-6 px-5 pt-5 pb-4">
						<div className="min-w-0 flex-1">
							<div className="flex flex-wrap items-center gap-2">
								<StatusPill status={displayLog.status} />
								{isPushRelated && (
									<Badge variant="outline" className="text-muted-foreground rounded-sm px-2 py-0.5 font-normal">
										push notification
									</Badge>
								)}
							</div>
							<div className="mt-3">
								<IdRow
									label="Agent"
									value={displayLog.agent_name}
									filter={{ agent_name: [displayLog.agent_name] }}
									onFilter={onFilter}
									testId="a2alogdetails-copy-agent-button"
								/>
								<IdRow
									label="Request"
									value={displayLog.request_id}
									filter={{ request_id: displayLog.request_id }}
									onFilter={onFilter}
									testId="a2alogdetails-copy-request-id-button"
								/>
								{displayLog.push_config_id && (
									<IdRow
										label="Push Config"
										value={displayLog.push_config_id}
										filter={{ push_config_id: displayLog.push_config_id }}
										onFilter={onFilter}
										testId="a2alogdetails-copy-push-config-id-button"
									/>
								)}
								{displayLog.task_id && (
									<div className="mt-1 flex max-w-full items-center gap-2">
										<span className="text-muted-foreground w-24 shrink-0 text-[10.5px] font-semibold tracking-wider uppercase">Task</span>
										<InteractiveValue
											value={displayLog.task_id}
											label="Task"
											filter={{ task_id: displayLog.task_id }}
											onFilter={onFilter}
											className="font-mono text-[13px]"
										/>
									</div>
								)}
								{displayLog.context_id && (
									<div className="mt-1 flex max-w-full items-center gap-2">
										<span className="text-muted-foreground w-24 shrink-0 text-[10.5px] font-semibold tracking-wider uppercase">
											Context
										</span>
										<InteractiveValue
											value={displayLog.context_id}
											label="Context"
											filter={{ context_id: displayLog.context_id }}
											onFilter={onFilter}
											className="font-mono text-[13px]"
										/>
									</div>
								)}
								{displayLog.virtual_key_id && (
									<div className="mt-1 flex items-center gap-2">
										<span className="text-muted-foreground w-24 shrink-0 text-[10.5px] font-semibold tracking-wider uppercase">
											Virtual key
										</span>
										<Link
											to="/workspace/governance/virtual-keys"
											search={{ selected_vk: displayLog.virtual_key_id }}
											className="truncate font-mono text-[13px] text-blue-600 hover:underline dark:text-blue-400"
										>
											{displayLog.virtual_key_name || displayLog.virtual_key_id}
										</Link>
									</div>
								)}
							</div>
						</div>
					</div>
					<div
						className={cn(
							"border-border grid grid-cols-1 border-t sm:grid-cols-2",
							correlatedRows.length > 0 ? "md:grid-cols-5" : "md:grid-cols-4",
						)}
					>
						<HeroStat
							label="Latency"
							valueClass="text-primary"
							value={hasValidLatency ? formatLatency(displayLog.latency!) : "—"}
							sub={
								hasValidTimestamp
									? `${format(timestampDate!, "HH:mm:ss")} → ${endTimestamp ? format(endTimestamp, "HH:mm:ss") : "—"}`
									: undefined
							}
							hasRightBorder
						/>
						<HeroStat label="Downstream" value={displayLog.downstream_transport || "—"} valueClass="font-mono text-[15px]" hasRightBorder />
						<HeroStat label="Upstream" value={displayLog.upstream_transport || "—"} valueClass="font-mono text-[15px]" hasRightBorder />
						<HeroStat
							label="Operation"
							value={displayLog.operation || "—"}
							valueClass="whitespace-normal overflow-visible break-all font-mono text-[15px]"
							hasRightBorder
						/>
						{correlatedRows.length > 0 && <HeroStat label="Events" value={String(totalCorrelatedEvents)} />}
					</div>
				</div>

				<details className="group bg-card rounded-sm border" open={false}>
					<summary className="hover:bg-muted/30 flex cursor-pointer items-center justify-between px-4 py-2.5 text-sm transition">
						<span className="text-foreground font-medium">More details</span>
						<span className="text-muted-foreground flex items-center gap-2 text-xs">
							<span className="hidden md:inline">
								{hasTimings ? "timings, identifiers and correlation" : "identifiers and correlation"}
							</span>
							<ChevronDown className="h-3.5 w-3.5 transition-transform group-open:rotate-180" />
						</span>
					</summary>
					<div className="space-y-4 border-t px-4 py-4 md:px-6">
						{hasTimings && (
							<>
								<div className="space-y-4">
									<BlockHeader title="Timings" />
									<div className="grid w-full grid-cols-1 items-center justify-between gap-4 md:grid-cols-3">
										{hasValidTimestamp && (
											<LogEntryDetailsView
												className="w-full"
												label="Start Timestamp"
												value={format(timestampDate!, "yyyy-MM-dd hh:mm:ss aa")}
											/>
										)}
										{endTimestamp && (
											<LogEntryDetailsView
												className="w-full"
												label="End Timestamp"
												value={format(endTimestamp, "yyyy-MM-dd hh:mm:ss aa")}
											/>
										)}
										{hasValidLatency && (
											<LogEntryDetailsView
												className="w-full"
												label="Latency"
												tooltip="Total end-to-end request time: upstream plus Bifrost overhead."
												value={<div>{displayLog.latency!.toFixed(2)}ms</div>}
											/>
										)}
										{displayLog.upstream_latency != null && !isNaN(displayLog.upstream_latency) && (
											<LogEntryDetailsView
												className="w-full"
												label="Upstream Latency"
												tooltip="Time spent waiting on the upstream agent."
												value={<div>{displayLog.upstream_latency.toFixed(2)}ms</div>}
											/>
										)}
										{displayLog.overhead_latency != null && !isNaN(displayLog.overhead_latency) && (
											<LogEntryDetailsView
												className="w-full"
												label="Bifrost Overhead"
												tooltip="Time added by Bifrost itself: routing, plugins, and processing."
												value={<div>{displayLog.overhead_latency.toFixed(2)}ms</div>}
											/>
										)}
									</div>
									{displayLog.overhead_breakdown && displayLog.overhead_breakdown.length > 0 ? (
										<OverheadBreakdown buckets={displayLog.overhead_breakdown} overheadMs={displayLog.overhead_latency} />
									) : null}
								</div>
								<DottedSeparator />
							</>
						)}
						<div className="space-y-4">
							<BlockHeader title="Request details" />
							<div className="grid w-full grid-cols-1 items-start gap-4 md:grid-cols-3">
								<LogEntryDetailsView className="w-full" label="Agent" value={<span className="font-mono">{displayLog.agent_name}</span>} />
								<LogEntryDetailsView
									className="w-full"
									label="Operation"
									value={<span className="font-mono">{displayLog.operation}</span>}
								/>
								<LogEntryDetailsView className="w-full" label="Status" value={displayLog.status} />
								{displayLog.content_type && <LogEntryDetailsView className="w-full" label="Content type" value={displayLog.content_type} />}
								{displayLog.virtual_key_id && (
									<LogEntryDetailsView
										className="w-full"
										label="Virtual key"
										value={
											<Link
												to="/workspace/governance/virtual-keys"
												search={{ selected_vk: displayLog.virtual_key_id }}
												className="text-sm text-blue-600 underline-offset-2 hover:underline dark:text-blue-400"
											>
												{displayLog.virtual_key_name || displayLog.virtual_key_id}
											</Link>
										}
									/>
								)}
								{governanceLinks.map(({ label, pluralLabel, route, searchKey, items }) => (
									<LogEntryDetailsView
										key={label}
										className="w-full"
										label={items.length > 1 ? pluralLabel : label}
										value={
											<span className="inline-flex flex-wrap gap-x-1">
												{items.map((item, index) => (
													<Tooltip key={item.id}>
														<TooltipTrigger asChild>
															<Link
																to={route}
																search={{ [searchKey]: item.id }}
																className="text-sm text-blue-600 underline-offset-2 hover:underline dark:text-blue-400"
															>
																{item.name}
																{index < items.length - 1 ? "," : ""}
															</Link>
														</TooltipTrigger>
														<TooltipContent sideOffset={6}>{item.id}</TooltipContent>
													</Tooltip>
												))}
											</span>
										}
									/>
								))}
							</div>
						</div>
						{diagnosticSections
							.filter(({ fields }) => fields.length > 0)
							.map(({ title, fields }) => (
								<div key={title} className="space-y-4">
									<DottedSeparator />
									<BlockHeader title={title} />
									<div className="grid w-full grid-cols-1 items-start gap-4 md:grid-cols-3">
										{fields.map(([label, value, filter]) => (
											<LogEntryDetailsView
												key={label}
												className="w-full"
												label={label}
												value={
													<span className="inline-flex max-w-full items-center gap-1">
														{typeof value === "string" ? (
															<InteractiveValue
																value={value}
																label={label}
																filter={filter}
																onFilter={onFilter}
																className="font-mono text-xs"
															/>
														) : (
															<span className="truncate font-mono text-xs">{String(value)}</span>
														)}
													</span>
												}
											/>
										))}
									</div>
								</div>
							))}
					</div>
				</details>

				<Tabs
					key={`${displayLog.id}:${defaultTab}`}
					defaultValue={defaultTab}
					onValueChange={(value) => setActiveTab({ correlationKey, value })}
					className="gap-2"
				>
					<TabsList className="bg-muted/60 h-10 w-fit">
						{hasConversation && (
							<TabsTrigger value="conversation" className="px-3">
								Conversation
							</TabsTrigger>
						)}
						{!isConversationOperation && !isPushConversationOperation && hasPayload && (
							<TabsTrigger value="payload" className="px-3">
								Payload
							</TabsTrigger>
						)}
						{correlatedRows.length > 0 && (
							<TabsTrigger value="events" className="px-3">
								Events
								{correlatedRows.length > 0 ? (
									<span className="bg-background text-muted-foreground ml-1.5 rounded-sm border px-2 py-0.5 text-[10px] tabular-nums">
										{totalCorrelatedEvents}
									</span>
								) : null}
							</TabsTrigger>
						)}
						{pluginLogCount > 0 && (
							<TabsTrigger value="plugins" className="px-3">
								Plugin Logs
								<span className="bg-background text-muted-foreground ml-1.5 rounded-sm border px-2 py-0.5 text-[10px] tabular-nums">
									{pluginLogCount}
								</span>
							</TabsTrigger>
						)}
						{logCorrelation && llmCount > 0 ? (
							<TabsTrigger value="llm-logs" className="px-3">
								LLM Logs{" "}
								<span className="bg-background text-muted-foreground ml-1.5 rounded-sm border px-2 py-0.5 text-[10px] tabular-nums">
									{llmCount}
								</span>
							</TabsTrigger>
						) : null}
						{logCorrelation && mcpCount > 0 ? (
							<TabsTrigger value="mcp-logs" className="px-3">
								MCP Logs{" "}
								<span className="bg-background text-muted-foreground ml-1.5 rounded-sm border px-2 py-0.5 text-[10px] tabular-nums">
									{mcpCount}
								</span>
							</TabsTrigger>
						) : null}
						<TabsTrigger value="raw" className="px-3">
							Raw JSON
						</TabsTrigger>
					</TabsList>

					{hasConversation && conversationLog && (
						<TabsContent value="conversation" className="space-y-4 px-1 py-2">
							<A2AOperationConversation log={conversationLog} allowPushConversation={isPushConversationOperation} />
							<A2AConversationHistory responseBody={conversationLog.response_body} />
						</TabsContent>
					)}

					{!isConversationOperation && !isPushConversationOperation && hasPayload && detail && (
						<TabsContent value="payload" className="px-1 py-2">
							<A2APayloadView
								operation={detail.operation}
								status={detail.status}
								requestBody={detail.request_body}
								responseBody={detail.response_body}
								errorDetails={detail.error_details}
							/>
						</TabsContent>
					)}

					{correlatedRows.length > 0 && (
						<TabsContent value="events" className="space-y-3">
							<div className="text-muted-foreground text-sm">
								Events show the ordered updates returned by the agent during this operation. Expand an event to inspect its data.
							</div>
							<div className="space-y-2">
								{correlatedRows.map((row) => {
									const rowDate = row.timestamp ? new Date(row.timestamp) : null;
									const isExpanded = expandedEventIds.has(row.id);
									const showRaw = rawEventIds.has(row.id);
									const eventDetail: AgentLogDetail = row;
									return (
										<div key={row.id} className={cn("rounded-sm border", isExpanded && "border-primary/50 bg-muted/20")}>
											<div className="hover:bg-muted/50 flex w-full items-start gap-3 px-3 py-3">
												<button
													type="button"
													onClick={() => void toggleEvent(row)}
													className="flex min-w-0 flex-1 cursor-pointer items-start gap-3 text-left"
													data-testid={`a2alogdetails-event-row-${row.id}`}
												>
													<span className="bg-muted text-muted-foreground mt-0.5 flex size-6 shrink-0 items-center justify-center rounded-full font-mono text-[10px]">
														{row.event_sequence ?? "·"}
													</span>
													<div className="min-w-0 flex-1">
														<div className="flex flex-wrap items-center gap-2">
															<span className="text-sm font-medium capitalize">{eventLabel(row)}</span>
															{row.task_state && (
																<Badge variant="outline" className="rounded-sm text-[10px]">
																	{row.task_state}
																</Badge>
															)}
														</div>
														<div className="text-muted-foreground mt-0.5 text-xs">{eventExplanation(row)}</div>
													</div>
												</button>
												<div className="flex shrink-0 flex-col items-end gap-2">
													<span className="text-muted-foreground font-mono text-[11px] tabular-nums">
														{rowDate && isValid(rowDate) ? format(rowDate, "HH:mm:ss.SSS") : ""}
													</span>
													{isExpanded && eventDetail && (
														<div className="bg-muted/60 inline-flex rounded-sm border p-0.5">
															<Button
																type="button"
																variant="ghost"
																className={cn(
																	"h-7 rounded-sm px-2.5 text-xs",
																	!showRaw && "bg-background text-foreground shadow-sm hover:bg-background",
																)}
																onClick={() => showRaw && toggleEventRaw(row.id)}
															>
																Formatted
															</Button>
															<Button
																type="button"
																variant="ghost"
																className={cn(
																	"h-7 rounded-sm px-2.5 text-xs",
																	showRaw && "bg-background text-foreground shadow-sm hover:bg-background",
																)}
																onClick={() => !showRaw && toggleEventRaw(row.id)}
															>
																Raw JSON
															</Button>
														</div>
													)}
												</div>
											</div>
											{isExpanded && (
												<div className="border-t p-3">
													{showRaw ? (
														<PayloadBlock title="Raw event record" code={rawEventRecord(eventDetail)} />
													) : (
														<FormattedEventDetail detail={eventDetail} onFilter={onFilter} />
													)}
												</div>
											)}
										</div>
									);
								})}
							</div>
						</TabsContent>
					)}

					{logCorrelation && llmCount > 0 ? (
						<CorrelatedLogsContent
							key={`llm:${correlationKey}`}
							kind="llm"
							correlation={logCorrelation}
							active={activeTab.correlationKey === correlationKey && activeTab.value === "llm-logs"}
						/>
					) : null}
					{logCorrelation && mcpCount > 0 ? (
						<CorrelatedLogsContent
							key={`mcp:${correlationKey}`}
							kind="mcp"
							correlation={logCorrelation}
							active={activeTab.correlationKey === correlationKey && activeTab.value === "mcp-logs"}
						/>
					) : null}

					<TabsContent value="raw" className="space-y-4">
						<PayloadBlock title="Raw record" code={JSON.stringify(displayLog, null, 2)} />
					</TabsContent>

					{pluginLogCount > 0 && detail?.plugin_logs && (
						<TabsContent value="plugins" className="space-y-3">
							<PluginLogsView pluginLogs={detail.plugin_logs} />
						</TabsContent>
					)}
				</Tabs>
			</SheetContent>
		</Sheet>
	);
}