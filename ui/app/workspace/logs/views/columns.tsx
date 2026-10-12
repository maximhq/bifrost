import { formatCost, formatLatency } from "@/app/workspace/dashboard/utils/chartUtils";
import { AttributionCell } from "@/components/logAttributionCell";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdownMenu";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { TruncatedLabel } from "@/components/ui/truncatedLabel";
import { ProviderIconType, RenderProviderIcon } from "@/lib/constants/icons";
import {
	getProviderLabel,
	logAppDisplayName,
	mapAppToClientApp,
	mapUserAgentToApp,
	ProviderName,
	RequestTypeColors,
	RequestTypeLabels,
	Status,
	StatusBarColors,
} from "@/lib/constants/logs";
import { ChatMessageContent, DisplayLogEntry, LLMUsage, LogEntry, ResponsesMessageContentBlock } from "@/lib/types/logs";
import { cn } from "@/lib/utils";
import { formatCompactNumber } from "@/lib/utils/numbers";
import { ColumnDef } from "@tanstack/react-table";
import { format, formatDistanceToNow } from "date-fns";
import { ArrowUpDown, ChevronRight, Loader2, MoreHorizontal, Trash2 } from "lucide-react";
import { type ReactNode, useState } from "react";

// Passed to useReactTable({ meta }) by the logs page so the expander column can
// read/toggle chain expansion without threading props through column factories.
export interface LogsTableMeta {
	expandedChainIds: Set<string>;
	loadingChainIds: Set<string>;
	onToggleChain: (log: LogEntry) => void;
	expandedSessionIds: Set<string>;
	loadingSessionIds: Set<string>;
	onToggleSession: (log: LogEntry) => void;
}

function batchAccountingDisplay(log: LogEntry): { model: string; usage: LLMUsage } | null {
	const breakdowns = log.batch_debug?.accounting?.model_breakdowns;
	if (!breakdowns) {
		return null;
	}
	const entries = Object.values(breakdowns);
	if (entries.length === 0) {
		return null;
	}
	const model = entries.length === 1 ? entries[0].model : "mixed";
	const usage: LLMUsage = { prompt_tokens: 0, completion_tokens: 0, total_tokens: 0 };
	for (const entry of entries) {
		usage.prompt_tokens = (usage.prompt_tokens ?? 0) + (entry.usage?.prompt_tokens ?? 0);
		usage.completion_tokens = (usage.completion_tokens ?? 0) + (entry.usage?.completion_tokens ?? 0);
		usage.total_tokens = (usage.total_tokens ?? 0) + (entry.usage?.total_tokens ?? 0);
	}
	if ((usage.total_tokens ?? 0) === 0) {
		return null;
	}
	return { model, usage };
}

function LogActionsMenu({ log, onDelete }: { log: LogEntry; onDelete: (log: LogEntry) => void }) {
	const [isOpen, setIsOpen] = useState(false);

	return (
		<DropdownMenu open={isOpen} onOpenChange={setIsOpen}>
			<DropdownMenuTrigger asChild onClick={(event) => event.stopPropagation()}>
				<Button variant="ghost" size="icon" data-testid="log-actions-btn" aria-label="Log actions" className="h-7 w-7">
					<MoreHorizontal className="h-4 w-4" />
				</Button>
			</DropdownMenuTrigger>
			<DropdownMenuContent align="end">
				<DropdownMenuItem
					variant="destructive"
					className="cursor-pointer"
					data-testid="log-delete-btn"
					onSelect={(e) => {
						e.preventDefault();
						onDelete(log);
						setIsOpen(false);
					}}
				>
					<Trash2 className="h-4 w-4" />
					Delete
				</DropdownMenuItem>
			</DropdownMenuContent>
		</DropdownMenu>
	);
}

function getAssistantToolCallSummary(log?: LogEntry): string {
	const toolCalls = log?.output_message?.tool_calls || [];
	if (toolCalls.length === 0) {
		// Hybrid list rows carry only the denormalized names; the full calls live in the offloaded payload.
		return (log?.tool_call_names || []).join("\n");
	}
	return toolCalls
		.map((toolCall) => {
			const name = toolCall?.function?.name;
			if (!name) {
				return "";
			}
			const argumentsText = toolCall?.function?.arguments?.trim();
			return argumentsText ? `${name}(${argumentsText})` : name;
		})
		.filter(Boolean)
		.join("\n");
}

function getMessageFromContent(content?: ChatMessageContent): string {
	if (content == undefined) {
		return "";
	}
	if (typeof content === "string") {
		return content;
	}
	let lastTextContentBlock = "";
	for (const block of content) {
		if ((block.type === "text" || block.type === "input_text" || block.type === "output_text") && block.text) {
			lastTextContentBlock = block.text;
		}
	}
	return lastTextContentBlock;
}

export function getRealtimeTurnMessages(log?: LogEntry): {
	tool?: string;
	user?: string;
	assistant?: string;
	assistantToolCall?: string;
} {
	const toolMessages = log?.input_history?.filter((message) => message.role === "tool") || [];
	const userMessages = log?.input_history?.filter((message) => message.role === "user") || [];
	return {
		tool:
			toolMessages
				.map((m) => getMessageFromContent(m.content))
				.filter(Boolean)
				.join("\n") || "",
		user:
			userMessages
				.map((m) => getMessageFromContent(m.content))
				.filter(Boolean)
				.join("\n") || "",
		assistant: log?.output_message ? getMessageFromContent(log.output_message.content) : "",
		assistantToolCall: getAssistantToolCallSummary(log),
	};
}

export function getMessage(log?: LogEntry) {
	if (log?.object === "list_models") {
		return "N/A";
	}
	// A metadata lookup has no message body; keep the error summary when it failed.
	if (log?.object === "model_retrieve") {
		return log.content_summary || "N/A";
	}
	if (log?.object === "realtime.turn") {
		const messages = getRealtimeTurnMessages(log);
		const parts = [
			messages.tool ? `Tool Result: ${messages.tool}` : "",
			messages.user ? `User: ${messages.user}` : "",
			messages.assistantToolCall ? `Assistant Tool Call: ${messages.assistantToolCall}` : "",
			messages.assistant ? `Assistant: ${messages.assistant}` : "",
		].filter(Boolean);
		if (parts.length > 0) {
			return parts.join("\n");
		}
		return "";
	}
	if (log?.input_history && log.input_history.length > 0) {
		const lastInput = log.input_history[log.input_history.length - 1];
		return getMessageFromContent(lastInput?.content);
	} else if (log?.responses_input_history && log.responses_input_history.length > 0) {
		let lastMessage = log.responses_input_history[log.responses_input_history.length - 1];
		let lastMessageContent = lastMessage?.content;
		if (!lastMessage) {
			return "";
		}
		if (typeof lastMessageContent === "string") {
			return lastMessageContent;
		}
		let lastTextContentBlock = "";
		for (const block of (lastMessageContent ?? []) as ResponsesMessageContentBlock[]) {
			if (block.text && block.text !== "") {
				lastTextContentBlock = block.text;
			}
		}
		// If no content found in content field, check output field for Responses API
		if (!lastTextContentBlock && lastMessage.output) {
			// Handle output field - it could be a string, an array of content blocks, or a computer tool call output data
			if (typeof lastMessage.output === "string") {
				return lastMessage.output;
			} else if (Array.isArray(lastMessage.output)) {
				return lastMessage.output.map((block) => block.text).join("\n");
			} else if (lastMessage.output.type && lastMessage.output.type === "computer_screenshot") {
				return lastMessage.output.image_url;
			}
		}
		return lastTextContentBlock ?? "";
	} else if (log?.output_message) {
		return getMessageFromContent(log.output_message.content);
	} else if (log?.speech_input) {
		return log.speech_input.input;
	} else if (log?.transcription_input) {
		return "Audio file";
	} else if (log?.image_generation_input?.prompt) {
		return log.image_generation_input.prompt;
	}
	const obj = log?.object as string | undefined;
	if (obj === "image_edit" || obj === "image_edit_stream" || obj === "image_variation") {
		return "Image file";
	}
	if (log?.content_summary) {
		return log.content_summary;
	}
	return "";
}

export function LogMessageCell({
	log,
	contentClassName = "max-w-full",
	compact = false,
}: {
	log: LogEntry;
	contentClassName?: string;
	/** Table rows are a fixed height, so a realtime turn's lines tighten to fit two of them instead of being cut mid-line. */
	compact?: boolean;
}) {
	const input = getMessage(log);
	const isLargePayload = log.is_large_payload_request || log.is_large_payload_response;
	const realtimeMessages = log.object === "realtime.turn" ? getRealtimeTurnMessages(log) : null;

	return (
		<div className="flex items-center gap-1.5">
			{isLargePayload && (
				<span
					className="shrink-0 rounded bg-amber-100 px-1.5 py-0.5 text-[10px] font-medium text-amber-700 dark:bg-amber-900/50 dark:text-amber-400"
					title="Large payload - streamed directly to provider"
				>
					LP
				</span>
			)}
			{realtimeMessages &&
			(realtimeMessages.tool || realtimeMessages.user || realtimeMessages.assistantToolCall || realtimeMessages.assistant) ? (
				<div
					className={cn(
						contentClassName,
						"font-mono font-normal",
						compact ? "max-h-[30px] overflow-hidden text-[11px] leading-[15px]" : "text-sm leading-5",
					)}
				>
					{realtimeMessages.tool ? <div className="truncate">Tool Result: {realtimeMessages.tool}</div> : null}
					{realtimeMessages.user ? <div className="truncate">User: {realtimeMessages.user}</div> : null}
					{realtimeMessages.assistantToolCall ? (
						<div className="truncate">Assistant Tool Call: {realtimeMessages.assistantToolCall}</div>
					) : null}
					{realtimeMessages.assistant ? <div className="truncate">Assistant: {realtimeMessages.assistant}</div> : null}
				</div>
			) : (
				<div className={cn(contentClassName, "truncate font-mono text-[12px] font-normal")}>
					{input ||
						(isLargePayload
							? `Large payload ${log.is_large_payload_request && log.is_large_payload_response ? "request & response" : log.is_large_payload_request ? "request" : "response"}`
							: "-")}
				</div>
			)}
		</div>
	);
}

// The grouped view's first column says what each group is in words, since the
// two groupings look alike but mean different things: a session is separate
// requests sharing a session_id (counted as turns), a chain is one request's
// fallback attempts linked by parent_request_id. A settled batch or video nests
// its cost row the same way, so a chain under one is "linked", not "fallbacks".
function chainSummary(log: LogEntry, count: number): string {
	if (log.batch_debug || log.video_debug) return `${count} linked`;
	return `${count} fallback${count === 1 ? "" : "s"}`;
}

function nestedRowLabel(log: DisplayLogEntry): string {
	if (log.__rowKind === "session-member") return `turn ${log.__turn ?? ""}`.trim();
	if (log.batch_debug?.accounting || log.video_debug?.accounting) return "settled cost";
	return log.fallback_index > 0 ? `fallback ${log.fallback_index}` : "linked";
}

// Tree geometry, in px from the cell's left edge. The trunk sits under the
// top-level chevron; a session member's own chain hangs from a second trunk
// under that member's chevron.
const TREE_TRUNK_X = [11, 22] as const;

// Draws the branch for a nested row: the trunk at its depth (cut at the middle
// on the last sibling), a tick out to the label, and, under a session member
// that is not last, the outer trunk carried through. Positioned against the
// cell, which the logs table makes relative, so lines meet across rows.
function TreeBranch({ log, opensChildren }: { log: DisplayLogEntry; opensChildren: boolean }) {
	const depth = log.__depth ?? 1;
	const x = TREE_TRUNK_X[depth - 1];
	const line = "bg-border absolute";
	return (
		<>
			{depth === 2 && !log.__parentIsLast && <span className={cn(line, "inset-y-0 w-px")} style={{ left: TREE_TRUNK_X[0] }} />}
			<span className={cn(line, "top-0 w-px", log.__isLast ? "h-1/2" : "bottom-0")} style={{ left: x }} />
			<span className={cn(line, "top-1/2 h-px w-1.5")} style={{ left: x }} />
			{opensChildren && <span className={cn(line, "bottom-0 w-px")} style={{ left: TREE_TRUNK_X[1], top: "calc(50% + 9px)" }} />}
		</>
	);
}

function GroupToggle({
	testId,
	label,
	ariaLabel,
	tooltip,
	isExpanded,
	isLoading,
	onToggle,
	className,
	stubX,
}: {
	stubX?: number;
	testId: string;
	label: ReactNode;
	ariaLabel: string;
	tooltip: string;
	isExpanded: boolean;
	isLoading: boolean;
	onToggle: () => void;
	className?: string;
}) {
	return (
		<>
			{/* Stub from under a top-level chevron down to its first child's branch. */}
			{stubX != null && isExpanded && !isLoading && (
				<span className="bg-border absolute bottom-0 w-px" style={{ left: stubX, top: "calc(50% + 9px)" }} />
			)}
			<Tooltip>
				<TooltipTrigger asChild>
					<button
						type="button"
						data-testid={testId}
						aria-label={ariaLabel}
						aria-expanded={isExpanded}
						className={cn(
							"hover:text-foreground relative flex h-full w-full cursor-pointer items-center gap-1 overflow-hidden text-[11px] whitespace-nowrap transition-colors",
							isExpanded ? "text-foreground" : "text-muted-foreground",
							className,
						)}
						onClick={(event) => {
							event.stopPropagation();
							onToggle();
						}}
					>
						{isLoading ? (
							<Loader2 className="size-3.5 shrink-0 animate-spin" />
						) : (
							<ChevronRight className={cn("size-3.5 shrink-0 transition-transform duration-150", isExpanded && "rotate-90")} />
						)}
						{label}
					</button>
				</TooltipTrigger>
				<TooltipContent>{tooltip}</TooltipContent>
			</Tooltip>
		</>
	);
}

export const createColumns = (
	onDelete: (log: LogEntry) => void,
	hasDeleteAccess = true,
	metadataKeys: string[] = [],
	customAppIcons: Record<string, string> = {},
	groupedView = false,
	onFilterBySessionId?: (sessionId: string) => void,
): ColumnDef<LogEntry>[] => {
	// Expander for the grouped view. The control fills the cell, and the cell
	// itself toggles rather than opening the sheet (see the logs page's
	// onRowClick), so the whole column reads as one hit target.
	const expandColumn: ColumnDef<LogEntry>[] = groupedView
		? [
				{
					id: "expand",
					header: "",
					size: 90,
					cell: ({ row, table }) => {
						const meta = table.options.meta as LogsTableMeta | undefined;
						const log = row.original as DisplayLogEntry;
						if (log.__chainChild) {
							const isSessionMember = log.__rowKind === "session-member";
							const kindTestId = isSessionMember ? "log-row-kind-session" : "log-row-kind-chain";
							const childCount = log.child_count ?? 0;
							// A session member keeps its own chain toggle, so the tree can be
							// walked a level deeper. Every other nested row is a leaf.
							if (isSessionMember && childCount > 0 && meta) {
								const isExpanded = meta.expandedChainIds.has(log.id);
								return (
									<div data-testid={kindTestId} className="h-full w-full">
										<TreeBranch log={log} opensChildren={isExpanded && !meta.loadingChainIds.has(log.id)} />
										<GroupToggle
											testId="log-chain-expand-btn"
											className="pl-[15px]"
											label={
												<span className="truncate">
													{nestedRowLabel(log)}
													<span className="text-muted-foreground/70 ml-1 tabular-nums">+{childCount}</span>
												</span>
											}
											ariaLabel={isExpanded ? "Collapse linked rows" : `Expand ${chainSummary(log, childCount)} of this turn`}
											tooltip={`${nestedRowLabel(log)} of this session, with ${chainSummary(log, childCount)} (same parent_request_id)`}
											isExpanded={isExpanded}
											isLoading={meta.loadingChainIds.has(log.id)}
											onToggle={() => meta.onToggleChain(log)}
										/>
									</div>
								);
							}
							const depth = log.__depth ?? 1;
							return (
								<div
									data-testid={kindTestId}
									className="text-muted-foreground flex h-full w-full items-center overflow-hidden text-[11px] whitespace-nowrap"
									style={{ paddingLeft: TREE_TRUNK_X[depth - 1] + 8 }}
								>
									<TreeBranch log={log} opensChildren={false} />
									<span className="truncate">{nestedRowLabel(log)}</span>
								</div>
							);
						}
						if (!meta) return null;
						// Session grouping wins on a top-level row: the session toggle
						// lists the session's other requests and this row's own attempts
						// together, so nothing becomes unreachable by taking this branch.
						const sessionCount = log.session_child_count ?? 0;
						if (sessionCount > 0) {
							const isExpanded = meta.expandedSessionIds.has(log.id);
							const others = `${sessionCount} more request${sessionCount === 1 ? "" : "s"}`;
							return (
								<GroupToggle
									testId="log-session-expand-btn"
									className="pl-1"
									stubX={TREE_TRUNK_X[0]}
									label={<span className="truncate tabular-nums">{sessionCount + 1} turns</span>}
									ariaLabel={isExpanded ? "Collapse this session" : `Expand ${others} in this session`}
									tooltip={`Session of ${sessionCount + 1} requests sharing a session_id. Expand to see the other ${others.replace("more ", "")}.`}
									isExpanded={isExpanded}
									isLoading={meta.loadingSessionIds.has(log.id) || meta.loadingChainIds.has(log.id)}
									onToggle={() => meta.onToggleSession(log)}
								/>
							);
						}
						const childCount = log.child_count ?? 0;
						if (!childCount) return null;
						const isExpanded = meta.expandedChainIds.has(log.id);
						const summary = chainSummary(log, childCount);
						return (
							<GroupToggle
								testId="log-chain-expand-btn"
								className="pl-1"
								stubX={TREE_TRUNK_X[0]}
								label={<span className="truncate tabular-nums">{summary}</span>}
								ariaLabel={isExpanded ? "Collapse linked rows" : `Expand ${summary}`}
								tooltip={
									log.batch_debug || log.video_debug
										? `${summary} row${childCount === 1 ? "" : "s"}: the settled cost (same parent_request_id)`
										: `${summary} of this request (same parent_request_id)`
								}
								isExpanded={isExpanded}
								isLoading={meta.loadingChainIds.has(log.id)}
								onToggle={() => meta.onToggleChain(log)}
							/>
						);
					},
				},
			]
		: [];

	const baseColumns: ColumnDef<LogEntry>[] = [
		{
			accessorKey: "status",
			header: "",
			size: 8,
			maxSize: 8,
			cell: ({ row }) => {
				const status = row.original.status as Status;
				return <div className={`h-full min-h-[24px] w-1 rounded-sm ${StatusBarColors[status]}`} />;
			},
		},
		{
			accessorKey: "timestamp",
			header: ({ column }) => (
				<Button variant="ghost" data-testid="logs-time-sort-btn" onClick={() => column.toggleSorting(column.getIsSorted() === "asc")}>
					Time
					<ArrowUpDown className="ml-2 h-4 w-4" />
				</Button>
			),
			size: 150,
			cell: ({ row }) => {
				const timestamp = row.original.timestamp;
				const date = timestamp ? new Date(timestamp) : null;
				const isValid = date && date.toString() !== "Invalid Date";
				if (!isValid) {
					return <div className="truncate text-xs">N/A</div>;
				}
				// The row opens the detail sheet on click, which a keyboard can't do. This
				// button is that entry point: it has no handler of its own, so Enter and
				// Space fire a click that bubbles to the cell's onRowClick.
				return (
					<button
						type="button"
						data-testid="logs-row-open-btn"
						aria-label={`${format(date, "MMM dd HH:mm:ss")} ${formatDistanceToNow(date, { addSuffix: true })}, open log details`}
						className="focus-visible:ring-ring flex cursor-pointer flex-col rounded-sm text-left leading-tight focus-visible:ring-2 focus-visible:outline-none"
					>
						<span className="font-mono text-xs tabular-nums">{format(date, "MMM dd  HH:mm:ss")}</span>
						<span className="text-muted-foreground text-[10.5px] tabular-nums">{formatDistanceToNow(date, { addSuffix: true })}</span>
					</button>
				);
			},
		},
		{
			id: "request_type",
			header: "Type",
			size: 150,
			cell: ({ row }) => {
				return (
					<Badge
						variant="outline"
						className={cn(
							"font-mono text-[11px] py-0.5 px-1.5 uppercase",
							RequestTypeColors[row.original.object as keyof typeof RequestTypeColors],
						)}
					>
						{RequestTypeLabels[row.original.object as keyof typeof RequestTypeLabels]}
					</Badge>
				);
			},
		},
		{
			accessorKey: "input",
			header: "Message",
			size: 350,
			cell: ({ row }) => <LogMessageCell log={row.original} compact />,
		},
		{
			accessorKey: "model",
			header: "Model",
			size: 280,
			cell: ({ row }) => {
				const provider = row.original.provider as ProviderName | undefined;
				const model = row.original.model || batchAccountingDisplay(row.original)?.model;
				const canonicalModel = row.original.canonical_model_name;
				const modelLabel = canonicalModel && canonicalModel !== model ? `${canonicalModel} (${model})` : model;
				return (
					<div className="flex min-w-0 items-center gap-2">
						{provider ? <RenderProviderIcon provider={provider as ProviderIconType} size="xs" /> : null}
						<div className="flex min-w-0 flex-col leading-tight">
							<TruncatedLabel truncateFrom="start" className="font-mono text-[12px]">
								{modelLabel || "N/A"}
							</TruncatedLabel>
							<span className="text-muted-foreground truncate text-[10.5px]">{provider ? getProviderLabel(provider) : "N/A"}</span>
						</div>
					</div>
				);
			},
		},
		{
			id: "app",
			accessorKey: "app",
			header: "App",
			size: 140,
			cell: ({ row }) => {
				const app = row.original.app ? mapAppToClientApp(row.original.app) : mapUserAgentToApp(row.original.user_agent);
				const icon = row.original.app ? customAppIcons[row.original.app] || app.icon : app.icon;
				const label = logAppDisplayName(app, row.original.user_agent);
				return (
					<div className="flex min-w-0 items-center gap-2" title={row.original.user_agent || undefined}>
						{icon ? <img className="rounded-sm" src={icon} alt={label} width={20} height={20} loading="lazy" decoding="async" /> : null}
						<span className="truncate text-[12px]">{label}</span>
					</div>
				);
			},
		},
		{
			accessorKey: "latency",
			header: ({ column }) => (
				<Button variant="ghost" data-testid="logs-latency-sort-btn" onClick={() => column.toggleSorting(column.getIsSorted() === "asc")}>
					Latency
					<ArrowUpDown className="ml-2 h-4 w-4" />
				</Button>
			),
			size: 170,
			cell: ({ row }) => {
				const latency = row.original.latency;
				if (latency === undefined || latency === null) {
					return <div className="pl-4 font-mono text-xs">N/A</div>;
				}
				const tone = latency >= 5000 ? "bg-chart-error" : latency >= 2000 ? "bg-chart-warning" : "bg-chart-success";
				const pct = Math.min(100, (latency / 5000) * 100);
				return (
					<div className="flex items-center gap-2 pl-4">
						<span className="font-mono text-[12px] tabular-nums">{formatLatency(latency)}</span>
						<div className="relative h-1.5 w-[56px] overflow-hidden rounded-sm bg-zinc-200 dark:bg-zinc-700">
							<div className={cn("absolute inset-y-0 left-0 rounded-sm opacity-85", tone)} style={{ width: `${pct}%` }} />
						</div>
					</div>
				);
			},
		},
		{
			accessorKey: "tokens",
			header: ({ column }) => (
				<Button variant="ghost" data-testid="logs-tokens-sort-btn" onClick={() => column.toggleSorting(column.getIsSorted() === "asc")}>
					Tokens
					<ArrowUpDown className="ml-2 h-4 w-4" />
				</Button>
			),
			size: 190,
			cell: ({ row }) => {
				const sessionCount = row.original.session_child_count ?? 0;
				if (sessionCount > 0 && !(row.original as DisplayLogEntry).__chainChild) {
					// Same two-line shape as a normal token cell: a collapsed session row
					// has to be exactly as tall as the rows it expands into, or the table
					// jumps every time one is opened.
					return (
						<Tooltip>
							<TooltipTrigger asChild>
								<div className="flex flex-col items-start gap-0.5 pl-4 leading-tight">
									<span className="font-mono text-[12px] tabular-nums">{formatCompactNumber(row.original.session_total_tokens ?? 0)}</span>
									<span className="text-muted-foreground font-mono text-[10.5px] tabular-nums">{sessionCount + 1} requests</span>
								</div>
							</TooltipTrigger>
							<TooltipContent>Total across {sessionCount + 1} requests in this session. Expand the row to see them.</TooltipContent>
						</Tooltip>
					);
				}
				const tokenUsage = row.original.token_usage ?? batchAccountingDisplay(row.original)?.usage;
				if (!tokenUsage) {
					return <div className="pl-4 font-mono text-xs">N/A</div>;
				}
				const prompt = tokenUsage.prompt_tokens ?? 0;
				const completion = tokenUsage.completion_tokens ?? 0;
				const total = tokenUsage.total_tokens ?? 0;
				const hasSplit = tokenUsage.completion_tokens != null && tokenUsage.prompt_tokens != null;
				const splitBase = prompt + completion || 1;
				const inPct = (prompt / splitBase) * 100;
				return (
					<div className="flex flex-col items-start gap-0.5 pl-4 leading-tight">
						<div className="flex items-center gap-2">
							<span className="font-mono text-[12px] tabular-nums">{formatCompactNumber(total)}</span>
							{hasSplit && (
								<div className="flex h-1.5 w-[64px] overflow-hidden rounded-sm">
									<div className="bg-blue-400" style={{ width: `${inPct}%` }} />
									<div className="flex-1 bg-violet-400" />
								</div>
							)}
						</div>
						{hasSplit && (
							<div className="text-muted-foreground font-mono text-[10.5px] tabular-nums">
								<span className="text-blue-500">{formatCompactNumber(prompt)}</span>
								<span> / </span>
								<span className="text-violet-500">{formatCompactNumber(completion)}</span>
							</div>
						)}
					</div>
				);
			},
		},
		{
			accessorKey: "cost",
			header: ({ column }) => (
				<Button variant="ghost" data-testid="logs-cost-sort-btn" onClick={() => column.toggleSorting(column.getIsSorted() === "asc")}>
					Cost
					<ArrowUpDown className="ml-2 h-4 w-4" />
				</Button>
			),
			size: 120,
			cell: ({ row }) => {
				// A collapsed session stands for every request in it, so the cell
				// reads as the session's total rather than the first turn's cost.
				const sessionCount = row.original.session_child_count ?? 0;
				if (sessionCount > 0 && !(row.original as DisplayLogEntry).__chainChild) {
					return (
						<Tooltip>
							<TooltipTrigger asChild>
								<div className="pl-4 font-mono text-sm tabular-nums">{formatCost(row.original.session_total_cost ?? 0)}</div>
							</TooltipTrigger>
							<TooltipContent>Total across {sessionCount + 1} requests in this session. Expand the row to see them.</TooltipContent>
						</Tooltip>
					);
				}
				if (row.original.cost == null) {
					const batchCost = row.original.batch_debug?.accounting?.cost;
					if (batchCost != null) {
						return (
							<Tooltip>
								<TooltipTrigger asChild>
									<div className="text-muted-foreground pl-4 font-mono text-sm tabular-nums">{formatCost(batchCost)}</div>
								</TooltipTrigger>
								<TooltipContent>Settled cost of this batch, billed once.</TooltipContent>
							</Tooltip>
						);
					}
					// A settled async job writes its cost to a child row rather than back
					// onto the request, so the request itself has no cost of its own.
					// children_cost is that rollup, computed per page.
					const settledCost = row.original.children_cost;
					if (settledCost != null && settledCost > 0) {
						return (
							<Tooltip>
								<TooltipTrigger asChild>
									<div className="text-muted-foreground pl-4 font-mono text-sm tabular-nums">{formatCost(settledCost)}</div>
								</TooltipTrigger>
								{/* The expand chevron only exists in the grouped view, so pointing at
								    it anywhere else sends people looking for a control that is not there. */}
								<TooltipContent>
									{groupedView
										? "Settled after this request completed. Expand the row to see it."
										: "Settled after this request completed, on its own row."}
								</TooltipContent>
							</Tooltip>
						);
					}
					return <div className="pl-4 font-mono text-[12px]">N/A</div>;
				}
				return <div className="pl-4 font-mono text-sm tabular-nums">{formatCost(row.original.cost)}</div>;
			},
		},
	];

	const attributionColumns: ColumnDef<LogEntry>[] = [
		{
			id: "session",
			header: "Session",
			size: 170,
			cell: ({ row }) => {
				const sessionId = row.original.session_id;
				if (!sessionId) return <div className="font-mono text-xs">-</div>;
				if (!onFilterBySessionId) {
					return <TruncatedLabel className="font-mono text-xs">{sessionId}</TruncatedLabel>;
				}
				return (
					<button
						type="button"
						data-testid="log-session-filter-btn"
						className="hover:text-foreground cursor-pointer text-left"
						onClick={(event) => {
							event.stopPropagation();
							onFilterBySessionId(sessionId);
						}}
					>
						<TruncatedLabel className="font-mono text-xs">{sessionId}</TruncatedLabel>
					</button>
				);
			},
		},
		{
			id: "service_tier",
			header: "Service Tier",
			size: 130,
			cell: ({ row }) => {
				const tier = row.original.service_tier;
				if (!tier) {
					return <div className="font-mono text-xs">-</div>;
				}
				return (
					<Badge variant="outline" className="px-1.5 py-0.5 font-mono text-[11px] uppercase">
						{tier}
					</Badge>
				);
			},
		},
		{
			id: "virtual_key",
			header: "Virtual Key",
			size: 170,
			cell: ({ row }) => <AttributionCell name={row.original.virtual_key_name} id={row.original.virtual_key_id} />,
		},
		{
			id: "routing_rule",
			header: "Routing Rule",
			size: 170,
			cell: ({ row }) => <AttributionCell name={row.original.routing_rule_name} id={row.original.routing_rule_id} />,
		},
		{
			id: "team",
			header: "Team",
			size: 150,
			cell: ({ row }) => (
				<AttributionCell
					names={row.original.team_names}
					name={row.original.team_name}
					ids={row.original.team_ids}
					id={row.original.team_id}
				/>
			),
		},
		{
			id: "customer",
			header: "Customer",
			size: 150,
			cell: ({ row }) => (
				<AttributionCell
					names={row.original.customer_names}
					name={row.original.customer_name}
					ids={row.original.customer_ids}
					id={row.original.customer_id}
				/>
			),
		},
		{
			id: "user",
			header: "User",
			size: 150,
			cell: ({ row }) => <AttributionCell name={row.original.user_name} id={row.original.user_id} />,
		},
		{
			id: "business_unit",
			header: "Business Unit",
			size: 150,
			cell: ({ row }) => (
				<AttributionCell
					names={row.original.business_unit_names}
					name={row.original.business_unit_name}
					ids={row.original.business_unit_ids}
					id={row.original.business_unit_id}
				/>
			),
		},
		{
			id: "project",
			header: "Project",
			size: 150,
			cell: ({ row }) => <AttributionCell name={row.original.project_name} id={row.original.project_id} />,
		},
	];

	const metadataColumns: ColumnDef<LogEntry>[] = metadataKeys.map((key) => ({
		id: `metadata_${key}`,
		header: key.charAt(0).toUpperCase() + key.slice(1),
		size: 126,
		cell: ({ row }) => {
			const value = row.original.metadata?.[key];
			return <div className="max-w-[150px] truncate font-mono text-xs">{value ?? "-"}</div>;
		},
	}));

	const actionsColumn: ColumnDef<LogEntry>[] = hasDeleteAccess
		? [
				{
					id: "actions",
					header: "",
					size: 56,
					cell: ({ row }) => {
						const log = row.original;
						return (
							<div className="flex justify-center">
								<LogActionsMenu log={log} onDelete={onDelete} />
							</div>
						);
					},
				},
			]
		: [];

	return [...expandColumn, ...baseColumns, ...attributionColumns, ...metadataColumns, ...actionsColumn];
};