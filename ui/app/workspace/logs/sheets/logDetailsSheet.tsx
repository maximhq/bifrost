"use client";

import { useEffect, useState, useMemo } from "react";
import { useLazyGetLogByIdQuery, useLazyGetTraceByIdQuery } from "@/lib/store/apis/logsApi";
import type { SpanEntry } from "@/lib/types/logs";
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
import { ProviderIconType, RenderProviderIcon, RoutingEngineUsedIcons } from "@/lib/constants/icons";
import {
	RequestTypeColors,
	RequestTypeLabels,
	RoutingEngineUsedColors,
	RoutingEngineUsedLabels,
	Status,
	StatusColors,
} from "@/lib/constants/logs";
import { LogEntry } from "@/lib/types/logs";
import { Clipboard, MoreVertical, Trash2 } from "lucide-react";
import moment from "moment";
import { toast } from "sonner";
import BlockHeader from "../views/blockHeader";
import CollapsibleBox from "../views/collapsibleBox";
import ImageView from "../views/imageView";
import LogChatMessageView from "../views/logChatMessageView";
import LogEntryDetailsView from "../views/logEntryDetailsView";
import LogResponsesMessageView from "../views/logResponsesMessageView";
import SpeechView from "../views/speechView";
import TranscriptionView from "../views/transcriptionView";
import VideoView from "../views/videoView";
import { CodeEditor } from "@/components/ui/codeEditor";

const formatJsonSafe = (str: string | undefined): string => {
	try {
		return JSON.stringify(JSON.parse(str || ""), null, 2);
	} catch {
		return str || "";
	}
};

interface LogDetailSheetProps {
	log: LogEntry | null;
	open: boolean;
	onOpenChange: (open: boolean) => void;
	handleDelete: (log: LogEntry) => void;
}

// Helper to detect passthrough operations
const isPassthroughOperation = (object: string) =>
	object === "passthrough" || object === "passthrough_stream";

// Helper to detect container operations (for hiding irrelevant fields like Model/Tokens)
const isContainerOperation = (object: string) => {
	const containerTypes = [
		"container_create",
		"container_list",
		"container_retrieve",
		"container_delete",
		"container_file_create",
		"container_file_list",
		"container_file_retrieve",
		"container_file_content",
		"container_file_delete",
	];
	return containerTypes.includes(object?.toLowerCase());
};

// Icons for span kinds
const spanKindIcons: Record<string, string> = {
	"trace": "T",
	"llm.call": "L",
	"plugin": "P",
	"mcp.tool": "M",
	"event": "E",
	"routing": "R",
	"retry": "r",
	"fallback": "F",
	"internal": "i",
};

const spanKindColors: Record<string, string> = {
	"trace": "bg-blue-100 text-blue-800 dark:bg-blue-900 dark:text-blue-200",
	"llm.call": "bg-green-100 text-green-800 dark:bg-green-900 dark:text-green-200",
	"plugin": "bg-purple-100 text-purple-800 dark:bg-purple-900 dark:text-purple-200",
	"mcp.tool": "bg-orange-100 text-orange-800 dark:bg-orange-900 dark:text-orange-200",
	"event": "bg-gray-100 text-gray-800 dark:bg-gray-900 dark:text-gray-200",
	"routing": "bg-cyan-100 text-cyan-800 dark:bg-cyan-900 dark:text-cyan-200",
	"retry": "bg-yellow-100 text-yellow-800 dark:bg-yellow-900 dark:text-yellow-200",
	"fallback": "bg-red-100 text-red-800 dark:bg-red-900 dark:text-red-200",
	"internal": "bg-gray-100 text-gray-600 dark:bg-gray-800 dark:text-gray-400",
};

// Build a tree from flat span list
function buildSpanTree(spans: SpanEntry[]): SpanEntry[] {
	const map = new Map<string, SpanEntry>();
	const roots: SpanEntry[] = [];

	for (const span of spans) {
		map.set(span.id, { ...span, children: [] });
	}

	for (const span of spans) {
		const node = map.get(span.id)!;
		if (span.parent_span_id && map.has(span.parent_span_id)) {
			map.get(span.parent_span_id)!.children!.push(node);
		} else {
			roots.push(node);
		}
	}

	return roots;
}

// Span tree item component
function SpanTreeItem({
	span,
	depth,
	selectedSpanId,
	onSelect,
}: {
	span: SpanEntry;
	depth: number;
	selectedSpanId: string | null;
	onSelect: (span: SpanEntry) => void;
}) {
	const [expanded, setExpanded] = useState(true);
	const hasChildren = span.children && span.children.length > 0;
	const isSelected = selectedSpanId === span.id;
	const durationMs = span.latency != null ? `${span.latency.toFixed(0)}ms` : "-";

	return (
		<div>
			<div
				className={`flex cursor-pointer items-center gap-1.5 rounded px-2 py-1 text-xs hover:bg-muted ${isSelected ? "bg-muted ring-1 ring-primary" : ""}`}
				style={{ paddingLeft: `${depth * 16 + 8}px` }}
				onClick={() => onSelect(span)}
			>
				{hasChildren ? (
					<button
						className="flex h-4 w-4 shrink-0 items-center justify-center text-muted-foreground"
						onClick={(e) => {
							e.stopPropagation();
							setExpanded(!expanded);
						}}
					>
						{expanded ? "v" : ">"}
					</button>
				) : (
					<span className="h-4 w-4 shrink-0" />
				)}
				<span
					className={`inline-flex h-5 w-5 shrink-0 items-center justify-center rounded text-[10px] font-bold ${spanKindColors[span.kind] || spanKindColors["internal"]}`}
					title={span.kind}
				>
					{spanKindIcons[span.kind] || "?"}
				</span>
				<span className="truncate font-medium">{span.name}</span>
				<span className={`ml-auto shrink-0 font-mono ${span.status === "error" ? "text-red-500" : "text-muted-foreground"}`}>
					{durationMs}
				</span>
				{span.status === "error" && (
					<span className="shrink-0 rounded bg-red-100 px-1 text-[10px] text-red-700 dark:bg-red-900 dark:text-red-300">ERR</span>
				)}
				{span.status === "processing" && (
					<span className="shrink-0 rounded bg-yellow-100 px-1 text-[10px] text-yellow-700 dark:bg-yellow-900 dark:text-yellow-300">...</span>
				)}
			</div>
			{expanded && hasChildren && (
				<div>
					{span.children!.map((child) => (
						<SpanTreeItem key={child.id} span={child} depth={depth + 1} selectedSpanId={selectedSpanId} onSelect={onSelect} />
					))}
				</div>
			)}
		</div>
	);
}

export function LogDetailSheet({ log, open, onOpenChange, handleDelete }: LogDetailSheetProps) {
	const [fetchLog, { data: fullLog }] = useLazyGetLogByIdQuery();
	const [fetchTrace, { data: traceData }] = useLazyGetTraceByIdQuery();
	const [selectedSpan, setSelectedSpan] = useState<SpanEntry | null>(null);

	useEffect(() => {
		if (open && log?.id) {
			fetchLog(log.id);
			fetchTrace(log.id);
		}
	}, [open, log?.id, fetchLog, fetchTrace]);

	// Reset selected span when sheet closes or log changes
	useEffect(() => {
		if (!open) {
			setSelectedSpan(null);
		}
	}, [open, log?.id]);

	// Build span tree from trace data — always show trace root with children nested
	const spanTree = useMemo(() => {
		if (!traceData?.trace) return null;
		const root: SpanEntry = { ...traceData.trace, children: [] };
		if (traceData.spans && traceData.spans.length > 0) {
			root.children = buildSpanTree(traceData.spans);
		}
		return [root];
	}, [traceData]);

	if (!log) return null;

	// Merge raw fields from the dedicated single-log fetch (list query omits them for performance).
	// Guard against stale data: only use fullLog if it belongs to the currently opened log entry.
	const rawRequest = fullLog?.id === log.id ? fullLog.raw_request : log.raw_request;
	const rawResponse = fullLog?.id === log.id ? fullLog.raw_response : log.raw_response;
	const passthroughRequestBody = fullLog?.id === log.id ? fullLog.passthrough_request_body : log.passthrough_request_body;
	const passthroughResponseBody = fullLog?.id === log.id ? fullLog.passthrough_response_body : log.passthrough_response_body;

	const isContainer = isContainerOperation(log.object);
	const isPassthrough = isPassthroughOperation(log.object);
	const passthroughParams = isPassthrough
		? (log.params as {
			method?: string;
			path?: string;
			raw_query?: string;
			status_code?: number;
		})
		: null;

	// Taking out tool call
	let toolsParameter = null;
	if (log.params?.tools) {
		try {
			toolsParameter = JSON.stringify(log.params.tools, null, 2);
		} catch (ignored) { }
	}

	// Extract audio format from request params
	// Format can be in params.audio?.format or params.extra_params?.audio?.format
	const audioFormat = (log.params as any)?.audio?.format || (log.params as any)?.extra_params?.audio?.format || undefined;
	const videoOutput = log.video_generation_output || log.video_retrieve_output || log.video_download_output;
	const videoListOutput = log.video_list_output;

	return (
		<Sheet open={open} onOpenChange={onOpenChange}>
			<SheetContent className="dark:bg-card flex w-full flex-col gap-0 overflow-hidden bg-white p-0 sm:max-w-[75%]">
				{/* Header */}
				<SheetHeader className="flex flex-row items-center border-b px-6 py-4">
					<div className="flex w-full items-center justify-between">
						<SheetTitle className="flex w-fit items-center gap-2 font-medium">
							{log.id && (
								<p className="text-md max-w-full truncate">
									Trace:{" "}
									<code
										className="text-normal cursor-pointer"
										onClick={() => {
											navigator.clipboard
												.writeText(log.id)
												.then(() => toast.success("Trace ID copied"))
												.catch(() => toast.error("Failed to copy"));
										}}
									>
										{log.id}
									</code>
								</p>
							)}
							<Badge variant="outline" className={`${StatusColors[log.status as Status]} uppercase`}>
								{log.status}
							</Badge>
							{log.metadata?.isAsyncRequest ? (
								<Badge variant="outline" className="bg-teal-100 text-teal-800 uppercase dark:bg-teal-900 dark:text-teal-200">
									Async
								</Badge>
							) : null}
							{(log.is_large_payload_request || log.is_large_payload_response) && (
								<Badge
									variant="outline"
									className="border-amber-300 bg-amber-50 text-amber-700 dark:border-amber-600 dark:bg-amber-950 dark:text-amber-400"
								>
									Large Payload
								</Badge>
							)}
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
								<DropdownMenuItem onClick={() => copyRequestBody(log)} data-testid="logdetails-copy-request-body-button">
									<Clipboard className="h-4 w-4" />
									Copy request body
								</DropdownMenuItem>
								<AlertDialogTrigger asChild>
									<DropdownMenuItem variant="destructive">
										<Trash2 className="h-4 w-4" />
										Delete trace
									</DropdownMenuItem>
								</AlertDialogTrigger>
							</DropdownMenuContent>
						</DropdownMenu>
						<AlertDialogContent>
							<AlertDialogHeader>
								<AlertDialogTitle>Are you sure you want to delete this trace?</AlertDialogTitle>
								<AlertDialogDescription>This action cannot be undone. This will permanently delete the trace and all its spans.</AlertDialogDescription>
							</AlertDialogHeader>
							<AlertDialogFooter>
								<AlertDialogCancel>Cancel</AlertDialogCancel>
								<AlertDialogAction
									onClick={() => {
										handleDelete(log);
										onOpenChange(false);
									}}
								>
									Delete
								</AlertDialogAction>
							</AlertDialogFooter>
						</AlertDialogContent>
					</AlertDialog>
				</SheetHeader>

				{/* Two-panel layout: span tree on left, details on right */}
				<div className="flex min-h-0 flex-1">
					{/* Left panel: Span tree (shown when trace has spans) */}
					{spanTree && spanTree.length > 0 && (
						<div className="flex w-[300px] shrink-0 flex-col border-r">
							<div className="border-b px-4 py-2">
								<div className="text-sm font-medium">Trace ({(traceData?.spans?.length || 0) + 1} spans)</div>
							</div>
							<div className="flex-1 overflow-y-auto px-1 py-2">
								{spanTree.map((span) => (
									<SpanTreeItem
										key={span.id}
										span={span}
										depth={0}
										selectedSpanId={selectedSpan?.id || null}
										onSelect={setSelectedSpan}
									/>
								))}
							</div>
						</div>
					)}

					{/* Right panel: Detail content */}
					<div className="flex-1 overflow-y-auto px-6 py-4">
						{/* Selected span indicator */}
						{selectedSpan && (
							<div className="mb-4 flex items-center gap-2 rounded-sm border border-primary/20 bg-primary/5 px-4 py-2">
								<span className="text-xs text-muted-foreground">Viewing span:</span>
								<Badge variant="outline" className={spanKindColors[selectedSpan.kind] || ""}>
									{selectedSpan.kind}
								</Badge>
								<span className="text-sm font-medium">{selectedSpan.name}</span>
								<button
									className="ml-auto text-xs text-muted-foreground hover:text-foreground"
									onClick={() => setSelectedSpan(null)}
								>
									Clear
								</button>
							</div>
						)}

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
							<LogEntryDetailsView
								className="w-full"
								label="Latency"
								value={isNaN(log.latency || 0) ? "NA" : <div>{(log.latency || 0)?.toFixed(2)}ms</div>}
							/>
						</div>
					</div>
					<DottedSeparator />
					<div className="space-y-4">
						<BlockHeader title="Request Details" />
						<div className="grid w-full grid-cols-3 items-start justify-between gap-4">
							<LogEntryDetailsView
								className="w-full"
								label="Provider"
								value={
									<Badge variant="secondary" className={`uppercase`}>
										<RenderProviderIcon provider={log.provider as ProviderIconType} size="sm" />
										{log.provider}
									</Badge>
								}
							/>
							{!isContainer && <LogEntryDetailsView className="w-full" label="Model" value={log.model} />}
							<LogEntryDetailsView
								className="w-full"
								label="Type"
								value={
									<div
										className={`${RequestTypeColors[log.object as keyof typeof RequestTypeColors] ?? "bg-gray-100 text-gray-800"
											} rounded-sm px-3 py-1`}
									>
										{RequestTypeLabels[log.object as keyof typeof RequestTypeLabels] ?? log.object ?? "unknown"}
									</div>
								}
							/>
							{log.selected_key && <LogEntryDetailsView className="w-full" label="Selected Key" value={log.selected_key.name} />}
							{log.number_of_retries > 0 && (
								<LogEntryDetailsView className="w-full" label="Number of Retries" value={log.number_of_retries} />
							)}
							{log.fallback_index > 0 && <LogEntryDetailsView className="w-full" label="Fallback Index" value={log.fallback_index} />}
							{log.virtual_key && <LogEntryDetailsView className="w-full" label="Virtual Key" value={log.virtual_key.name} />}
							{log.routing_engines_used && log.routing_engines_used.length > 0 && (
								<LogEntryDetailsView
									className="w-full"
									label="Routing Engines Used"
									value={
										<div className="flex flex-wrap gap-2">
											{log.routing_engines_used.map((engine) => (
												<Badge
													key={engine}
													className={RoutingEngineUsedColors[engine as keyof typeof RoutingEngineUsedColors] ?? "bg-gray-100 text-gray-800"}
												>
													<div className="flex items-center gap-2">
														{RoutingEngineUsedIcons[engine as keyof typeof RoutingEngineUsedIcons]?.()}
														<span>{RoutingEngineUsedLabels[engine as keyof typeof RoutingEngineUsedLabels] ?? engine}</span>
													</div>
												</Badge>
											))}
										</div>
									}
								/>
							)}
							{log.routing_rule && <LogEntryDetailsView className="w-full" label="Routing Rule" value={log.routing_rule.name} />}

							{/* Display audio params if present */}
							{(log.params as any)?.audio && (
								<>
									{(log.params as any).audio.format && (
										<LogEntryDetailsView className="w-full" label="Audio Format" value={(log.params as any).audio.format} />
									)}
									{(log.params as any).audio.voice && (
										<LogEntryDetailsView className="w-full" label="Audio Voice" value={(log.params as any).audio.voice} />
									)}
								</>
							)}

							{/* Display passthrough params (method, path, raw_query, status_code) */}
							{passthroughParams && (
								<>
									{passthroughParams.method && (
										<LogEntryDetailsView className="w-full" label="Method" value={passthroughParams.method} />
									)}
									{passthroughParams.path && (
										<LogEntryDetailsView className="w-full" label="Path" value={passthroughParams.path} />
									)}
									{passthroughParams.raw_query && (
										<LogEntryDetailsView className="w-full" label="Query" value={passthroughParams.raw_query} />
									)}
									{(passthroughParams.status_code ?? 0) !== 0 && (
										<LogEntryDetailsView
											className="w-full"
											label="Status Code"
											value={passthroughParams.status_code}
										/>
									)}
								</>
							)}

							{log.params &&
								Object.keys(log.params).length > 0 &&
								Object.entries(log.params)
									.filter(([key]) => {
										const passthroughKeys = ["method", "path", "raw_query", "status_code"];
										return (
											key !== "tools" &&
											key !== "instructions" &&
											key !== "audio" &&
											!(isPassthrough && passthroughKeys.includes(key))
										);
									})
									.filter(([_, value]) => typeof value === "boolean" || typeof value === "number" || typeof value === "string")
									.map(([key, value]) => <LogEntryDetailsView key={key} className="w-full" label={key} value={value} />)}
						</div>
					</div>
					{log.status === "success" && !isContainer && !isPassthrough && (
						<>
							<DottedSeparator />
							<div className="space-y-4">
								<BlockHeader title="Tokens" />
								<div className="grid w-full grid-cols-3 items-center justify-between gap-4">
									<LogEntryDetailsView className="w-full" label="Input Tokens" value={log.token_usage?.prompt_tokens || "-"} />
									<LogEntryDetailsView className="w-full" label="Output Tokens" value={log.token_usage?.completion_tokens || "-"} />
									<LogEntryDetailsView className="w-full" label="Total Tokens" value={log.token_usage?.total_tokens || "-"} />
									<LogEntryDetailsView className="w-full" label="Cost" value={log.cost != null ? `$${parseFloat(log.cost.toFixed(6))}` : "-"} />
									{log.token_usage?.prompt_tokens_details && (
										<>
											{(log.token_usage.prompt_tokens_details.cached_read_tokens) && (
												<LogEntryDetailsView
													className="w-full"
													label="Cache Read Tokens"
													value={
														(log.token_usage.prompt_tokens_details.cached_read_tokens ?? 0)
													}
												/>
											)}
											{(log.token_usage.prompt_tokens_details.cached_write_tokens) && (
												<LogEntryDetailsView
													className="w-full"
													label="Cache Write Tokens"
													value={
														(log.token_usage.prompt_tokens_details.cached_write_tokens ?? 0)
													}
												/>
											)}
											{log.token_usage.prompt_tokens_details.audio_tokens && (
												<LogEntryDetailsView
													className="w-full"
													label="Input Audio Tokens"
													value={log.token_usage.prompt_tokens_details.audio_tokens || "-"}
												/>
											)}
										</>
									)}
									{log.token_usage?.completion_tokens_details && (
										<>
											{log.token_usage.completion_tokens_details.reasoning_tokens && (
												<LogEntryDetailsView
													className="w-full"
													label="Reasoning Tokens"
													value={log.token_usage.completion_tokens_details.reasoning_tokens || "-"}
												/>
											)}
											{log.token_usage.completion_tokens_details.audio_tokens && (
												<LogEntryDetailsView
													className="w-full"
													label="Output Audio Tokens"
													value={log.token_usage.completion_tokens_details.audio_tokens || "-"}
												/>
											)}
											{log.token_usage.completion_tokens_details.accepted_prediction_tokens && (
												<LogEntryDetailsView
													className="w-full"
													label="Accepted Prediction Tokens"
													value={log.token_usage.completion_tokens_details.accepted_prediction_tokens || "-"}
												/>
											)}
											{log.token_usage.completion_tokens_details.rejected_prediction_tokens && (
												<LogEntryDetailsView
													className="w-full"
													label="Rejected Prediction Tokens"
													value={log.token_usage.completion_tokens_details.rejected_prediction_tokens || "-"}
												/>
											)}
										</>
									)}
								</div>
							</div>
							{(() => {
								const params = log.params as any;
								const reasoning = params?.reasoning;
								if (!reasoning || typeof reasoning !== "object" || Object.keys(reasoning).length === 0) {
									return null;
								}
								return (
									<>
										<DottedSeparator />
										<div className="space-y-4">
											<BlockHeader title="Reasoning Parameters" />
											<div className="grid w-full grid-cols-3 items-center justify-between gap-4">
												{reasoning.effort && (
													<LogEntryDetailsView
														className="w-full"
														label="Effort"
														value={
															<Badge variant="secondary" className="uppercase">
																{reasoning.effort}
															</Badge>
														}
													/>
												)}
												{reasoning.summary && (
													<LogEntryDetailsView
														className="w-full"
														label="Summary"
														value={
															<Badge variant="secondary" className="uppercase">
																{reasoning.summary}
															</Badge>
														}
													/>
												)}
												{reasoning.generate_summary && (
													<LogEntryDetailsView
														className="w-full"
														label="Generate Summary"
														value={
															<Badge variant="secondary" className="uppercase">
																{reasoning.generate_summary}
															</Badge>
														}
													/>
												)}
												{reasoning.max_tokens && <LogEntryDetailsView className="w-full" label="Max Tokens" value={reasoning.max_tokens} />}
											</div>
										</div>
									</>
								);
							})()}
							{log.cache_debug && (
								<>
									<DottedSeparator />
									<div className="space-y-4">
										<BlockHeader title={`Caching Details (${log.cache_debug.cache_hit ? "Hit" : "Miss"})`} />
										<div className="grid w-full grid-cols-3 items-center justify-between gap-4">
											{log.cache_debug.cache_hit ? (
												<>
													<LogEntryDetailsView
														className="w-full"
														label="Cache Type"
														value={
															<Badge variant="secondary" className={`uppercase`}>
																{log.cache_debug.hit_type}
															</Badge>
														}
													/>
													{/* <LogEntryDetailsView className="w-full" label="Cache ID" value={log.cache_debug.cache_id} /> */}
													{log.cache_debug.hit_type === "semantic" && (
														<>
															{log.cache_debug.provider_used && (
																<LogEntryDetailsView
																	className="w-full"
																	label="Embedding Provider"
																	value={
																		<Badge variant="secondary" className={`uppercase`}>
																			{log.cache_debug.provider_used}
																		</Badge>
																	}
																/>
															)}
															{log.cache_debug.model_used && (
																<LogEntryDetailsView className="w-full" label="Embedding Model" value={log.cache_debug.model_used} />
															)}
															{log.cache_debug.threshold && (
																<LogEntryDetailsView className="w-full" label="Threshold" value={log.cache_debug.threshold || "-"} />
															)}
															{log.cache_debug.similarity && (
																<LogEntryDetailsView
																	className="w-full"
																	label="Similarity Score"
																	value={log.cache_debug.similarity?.toFixed(2) || "-"}
																/>
															)}
															{log.cache_debug.input_tokens && (
																<LogEntryDetailsView
																	className="w-full"
																	label="Embedding Input Tokens"
																	value={log.cache_debug.input_tokens}
																/>
															)}
														</>
													)}
												</>
											) : (
												<>
													{log.cache_debug.provider_used && (
														<LogEntryDetailsView
															className="w-full"
															label="Embedding Provider"
															value={
																<Badge variant="secondary" className={`uppercase`}>
																	{log.cache_debug.provider_used}
																</Badge>
															}
														/>
													)}
													{log.cache_debug.model_used && (
														<LogEntryDetailsView className="w-full" label="Embedding Model" value={log.cache_debug.model_used} />
													)}
													{log.cache_debug.input_tokens && (
														<LogEntryDetailsView className="w-full" label="Embedding Input Tokens" value={log.cache_debug.input_tokens} />
													)}
												</>
											)}
										</div>
									</div>
								</>
							)}
							{log.metadata && Object.keys(log.metadata).filter((k) => k !== "isAsyncRequest").length > 0 && (
								<>
									<DottedSeparator />
									<div className="space-y-4">
										<BlockHeader title="Metadata" />
										<div className="grid w-full grid-cols-3 items-start justify-between gap-4">
											{Object.entries(log.metadata)
												.filter(([key]) => key !== "isAsyncRequest")
												.map(([key, value]) => (
													<LogEntryDetailsView key={key} className="w-full" label={key} value={String(value)} />
												))}
										</div>
									</div>
								</>
							)}
						</>
					)}
				</div>
				{log.routing_engine_logs && (
					<CollapsibleBox title="Routing Decision Logs" onCopy={() => log.routing_engine_logs || ""}>
						<div className="custom-scrollbar max-h-[400px] overflow-y-auto px-6 py-2 font-mono text-xs break-words whitespace-pre-wrap">
							{log.routing_engine_logs}
						</div>
					</CollapsibleBox>
				)}
				{toolsParameter && (
					<CollapsibleBox title={`Tools (${log.params?.tools?.length || 0})`} onCopy={() => toolsParameter}>
						<CodeEditor
							className="z-0 w-full"
							shouldAdjustInitialHeight={true}
							maxHeight={450}
							wrap={true}
							code={toolsParameter}
							lang="json"
							readonly={true}
							options={{ scrollBeyondLastLine: false, lineNumbers: "off", alwaysConsumeMouseWheel: false }}
						/>
					</CollapsibleBox>
				)}
				{log.params?.instructions && (
					<CollapsibleBox title="Instructions" onCopy={() => log.params?.instructions || ""}>
						<div className="custom-scrollbar max-h-[400px] overflow-y-auto px-6 py-2 font-mono text-xs break-words whitespace-pre-wrap">
							{log.params.instructions}
						</div>
					</CollapsibleBox>
				)}

				{/* Speech and Transcription Views */}
				{(log.speech_input || log.speech_output) && (
					<SpeechView speechInput={log.speech_input} speechOutput={log.speech_output} isStreaming={log.stream} />
				)}

				{(log.transcription_input || log.transcription_output) && (
					<TranscriptionView
						transcriptionInput={log.transcription_input}
						transcriptionOutput={log.transcription_output}
						isStreaming={log.stream}
					/>
				)}

				{(log.image_generation_input || log.image_generation_output) && (
					<ImageView imageInput={log.image_generation_input} imageOutput={log.image_generation_output} requestType={log.object} />
				)}

				{(log.video_generation_input || videoOutput || videoListOutput) && (
					<VideoView
						videoInput={log.video_generation_input}
						videoOutput={videoOutput}
						videoListOutput={videoListOutput}
						requestType={log.object}
					/>
				)}

				{log.list_models_output && (
					<CollapsibleBox
						title={`List Models Output (${log.list_models_output.length})`}
						onCopy={() => JSON.stringify(log.list_models_output, null, 2)}
					>
						<CodeEditor
							className="z-0 w-full"
							shouldAdjustInitialHeight={true}
							maxHeight={450}
							wrap={true}
							code={JSON.stringify(log.list_models_output, null, 2)}
							lang="json"
							readonly={true}
							options={{ scrollBeyondLastLine: false, lineNumbers: "off", alwaysConsumeMouseWheel: false }}
						/>
					</CollapsibleBox>
				)}

				{/* Passthrough request body */}
				{isPassthrough && passthroughRequestBody && (() => {
					return (
						<CollapsibleBox title="Request Body" onCopy={() => {
							try {
								return JSON.stringify(JSON.parse(passthroughRequestBody || ""), null, 2);
							} catch {
								return passthroughRequestBody || "";
							}
						}}>
							<CodeEditor
								className="z-0 w-full"
								shouldAdjustInitialHeight={true}
								maxHeight={450}
								wrap={true}
								code={(() => {
									try {
										return JSON.stringify(JSON.parse(passthroughRequestBody || ""), null, 2);
									} catch {
										return passthroughRequestBody || "";
									}
								})()}
								lang="json"
								readonly={true}
								options={{ scrollBeyondLastLine: false, lineNumbers: "off", alwaysConsumeMouseWheel: false }}
							/>
						</CollapsibleBox>
					);
				})()}

				{/* Show conversation history for chat/text completions */}
				{log.input_history && log.input_history.length > 1 && (
					<>
						<div className="mt-4 w-full text-left text-sm font-medium">Conversation History</div>
						{log.input_history.slice(0, -1).map((message, index) => (
							<LogChatMessageView key={index} message={message} audioFormat={audioFormat} />
						))}
					</>
				)}

				{/* Show input for chat/text completions */}
				{log.input_history && log.input_history.length > 0 && (
					<>
						<div className="mt-4 w-full text-left text-sm font-medium">Input</div>
						<LogChatMessageView message={log.input_history[log.input_history.length - 1]} audioFormat={audioFormat} />
					</>
				)}

				{/* Show input history for responses API */}
				{log.responses_input_history && log.responses_input_history.length > 0 && (
					<>
						<div className="mt-4 w-full text-left text-sm font-medium">Input</div>
						<LogResponsesMessageView messages={log.responses_input_history} />
					</>
				)}

				{log.is_large_payload_request && !log.input_history?.length && !log.responses_input_history?.length && (
					<div className="mt-4 rounded-md border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-800 dark:border-amber-800 dark:bg-amber-950/50 dark:text-amber-300">
						Large payload request — input content was streamed directly to the provider and is not available for display.
						{log.raw_request && " A truncated preview is available in the Raw Request section below."}
					</div>
				)}

				{log.is_large_payload_response && !log.output_message && !log.responses_output?.length && log.status !== "processing" && (
					<div className="mt-4 rounded-md border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-800 dark:border-amber-800 dark:bg-amber-950/50 dark:text-amber-300">
						Large payload response — response content was streamed directly to the client and is not available for display.
						{log.raw_response && " A truncated preview is available in the Raw Response section below."}
					</div>
				)}

				{log.status !== "processing" && (
					<>
						{log.output_message && !log.error_details?.error.message && (
							<>
								<div className="mt-4 flex w-full items-center gap-2">
									<div className="text-sm font-medium">Response</div>
								</div>
								<LogChatMessageView message={log.output_message} audioFormat={audioFormat} />
							</>
						)}
						{log.responses_output && log.responses_output.length > 0 && !log.error_details?.error.message && (
							<>
								<div className="mt-4 w-full text-left text-sm font-medium">Response</div>
								<LogResponsesMessageView messages={log.responses_output} />
							</>
						)}
						{log.embedding_output && log.embedding_output.length > 0 && !log.error_details?.error.message && (
							<>
								<div className="mt-4 w-full text-left text-sm font-medium">Embedding</div>
								<LogChatMessageView
									message={{
										role: "assistant",
										content: JSON.stringify(
											log.embedding_output.map((embedding) => embedding.embedding),
											null,
											2,
										),
									}}
								/>
							</>
						)}
						{log.rerank_output && !log.error_details?.error.message && (
							<>
								<CollapsibleBox
									title={`Rerank Output (${log.rerank_output.length})`}
									onCopy={() => JSON.stringify(log.rerank_output, null, 2)}
								>
									<CodeEditor
										className="z-0 w-full"
										shouldAdjustInitialHeight={true}
										maxHeight={450}
										wrap={true}
										code={JSON.stringify(log.rerank_output, null, 2)}
										lang="json"
										readonly={true}
										options={{ scrollBeyondLastLine: false, lineNumbers: "off", alwaysConsumeMouseWheel: false }}
									/>
								</CollapsibleBox>
							</>
						)}
						{/* Passthrough response body */}
						{isPassthrough && passthroughResponseBody && (
							<CollapsibleBox
								title="Response Body"
								onCopy={() => {
									try {
										return JSON.stringify(JSON.parse(passthroughResponseBody || ""), null, 2);
									} catch {
										return passthroughResponseBody || "";
									}
								}}
							>
								<CodeEditor
									className="z-0 w-full"
									shouldAdjustInitialHeight={true}
									maxHeight={450}
									wrap={true}
									code={(() => {
										try {
											return JSON.stringify(JSON.parse(passthroughResponseBody || ""), null, 2);
										} catch {
											return passthroughResponseBody || "";
										}
									})()}
									lang="json"
									readonly={true}
									options={{ scrollBeyondLastLine: false, lineNumbers: "off", alwaysConsumeMouseWheel: false }}
								/>
							</CollapsibleBox>
						)}
						{rawRequest && (
							<>
								<div className="mt-4 w-full text-left text-sm font-medium">
									Raw Request sent to <span className="font-medium capitalize">{log.provider}</span>
									{log.is_large_payload_request && (
										<span className="ml-2 text-xs font-normal text-amber-600 dark:text-amber-400">(truncated preview)</span>
									)}
								</div>
								<CollapsibleBox
									title={log.is_large_payload_request ? "Raw Request (Truncated)" : "Raw Request"}
									onCopy={() => formatJsonSafe(rawRequest)}
								>
									<CodeEditor
										className="z-0 w-full"
										shouldAdjustInitialHeight={true}
										maxHeight={450}
										wrap={true}
										code={formatJsonSafe(rawRequest)}
										lang="json"
										readonly={true}
										options={{ scrollBeyondLastLine: false, lineNumbers: "off", alwaysConsumeMouseWheel: false }}
									/>
								</CollapsibleBox>
							</>
						)}
						{rawResponse && (
							<>
								<div className="mt-4 w-full text-left text-sm font-medium">
									Raw Response from <span className="font-medium capitalize">{log.provider}</span>
									{log.is_large_payload_response && (
										<span className="ml-2 text-xs font-normal text-amber-600 dark:text-amber-400">(truncated preview)</span>
									)}
								</div>
								<CollapsibleBox
									title={log.is_large_payload_response ? "Raw Response (Truncated)" : "Raw Response"}
									onCopy={() => formatJsonSafe(rawResponse)}
								>
									<CodeEditor
										className="z-0 w-full"
										shouldAdjustInitialHeight={true}
										maxHeight={450}
										wrap={true}
										code={formatJsonSafe(rawResponse)}
										lang="json"
										readonly={true}
										options={{ scrollBeyondLastLine: false, lineNumbers: "off", alwaysConsumeMouseWheel: false }}
									/>
								</CollapsibleBox>
							</>
						)}
						{log.error_details?.error.message && (
							<>
								<div className="mt-4 w-full text-left text-sm font-medium">Error</div>
								<CollapsibleBox title="Error" onCopy={() => log.error_details?.error.message || ""}>
									<div className="custom-scrollbar max-h-[400px] overflow-y-auto px-6 py-2 font-mono text-xs break-words whitespace-pre-wrap">
										{log.error_details.error.message}
									</div>
								</CollapsibleBox>
							</>
						)}
						{log.error_details?.error.error && (
							<>
								<div className="mt-4 w-full text-left text-sm font-medium">Error Details</div>
								<CollapsibleBox
									title="Details"
									onCopy={() =>
										typeof log.error_details?.error.error === "string"
											? log.error_details.error.error
											: JSON.stringify(log.error_details?.error.error, null, 2)
									}
								>
									<div className="custom-scrollbar max-h-[400px] overflow-y-auto px-6 py-2 font-mono text-xs break-words whitespace-pre-wrap">
										{typeof log.error_details?.error.error === "string"
											? log.error_details.error.error
											: JSON.stringify(log.error_details?.error.error, null, 2)}
									</div>
								</CollapsibleBox>
							</>
						)}
					</>
				)}
					</div> {/* close right panel */}
				</div> {/* close two-panel flex */}
			</SheetContent>
		</Sheet>
	);
}

// Normalize log.object to canonical underscore form (handles dotted backend names like chat.completion, audio.speech)
const normalizeObjectForCopy = (object: string | undefined): string => {
	const normalized = (object?.toLowerCase() || "").replace(/\./g, "_").replace(/_chunk$/, "_stream");
	const mapping: Record<string, string> = {
		response: "responses",
		response_completion_stream: "responses_stream",
		audio_speech: "speech",
		audio_speech_stream: "speech_stream",
		audio_transcription: "transcription",
		audio_transcription_stream: "transcription_stream",
	};
	return mapping[normalized] ?? normalized;
};

const copyRequestBody = async (log: LogEntry) => {
	try {
		// Check if request is for responses, chat, speech, text completion, or embedding (exclude transcriptions)
		const object = normalizeObjectForCopy(log.object);
		const isChat = object === "chat_completion" || object === "chat_completion_stream";
		const isResponses = object === "responses" || object === "responses_stream";
		const isSpeech = object === "speech" || object === "speech_stream";
		const isTextCompletion = object === "text_completion" || object === "text_completion_stream";
		const isEmbedding = object === "embedding";
		const isTranscription = object === "transcription" || object === "transcription_stream";

		// Skip if transcription
		if (isTranscription) {
			toast.error("Copy request body is not available for transcription requests");
			return;
		}

		// Skip if not a supported request type
		if (!isChat && !isResponses && !isSpeech && !isTextCompletion && !isEmbedding) {
			toast.error("Copy request body is only available for chat, responses, speech, text completion, and embedding requests");
			return;
		}

		// Helper function to extract text content from ChatMessage
		const extractTextFromMessage = (message: any): string => {
			if (!message || !message.content) {
				return "";
			}
			if (typeof message.content === "string") {
				return message.content;
			}
			if (Array.isArray(message.content)) {
				return message.content
					.filter((block: any) => block && block.type === "text" && block.text)
					.map((block: any) => block.text || "")
					.join("");
			}
			return "";
		};

		// Helper function to extract texts from ChatMessage content blocks (for embeddings)
		const extractTextsFromMessage = (message: any): string[] => {
			if (!message || !message.content) {
				return [];
			}
			if (typeof message.content === "string") {
				return message.content ? [message.content] : [];
			}
			if (Array.isArray(message.content)) {
				return message.content.filter((block: any) => block && block.type === "text" && block.text).map((block: any) => block.text);
			}
			return [];
		};

		// Build request body following OpenAI schema
		const requestBody: any = {
			model: log.provider && log.model ? `${log.provider}/${log.model}` : log.model || "",
		};

		// Add messages/input/prompt based on request type
		if (isChat && log.input_history && log.input_history.length > 0) {
			requestBody.messages = log.input_history;
		} else if (isResponses && log.responses_input_history && log.responses_input_history.length > 0) {
			requestBody.input = log.responses_input_history;
		} else if (isSpeech && log.speech_input) {
			requestBody.input = log.speech_input.input;
		} else if (isTextCompletion && log.input_history && log.input_history.length > 0) {
			// For text completions, extract prompt from input_history
			const firstMessage = log.input_history[0];
			const prompt = extractTextFromMessage(firstMessage);
			if (prompt) {
				requestBody.prompt = prompt;
			}
		} else if (isEmbedding && log.input_history && log.input_history.length > 0) {
			// For embeddings, extract all texts from input_history
			const texts: string[] = [];
			for (const message of log.input_history) {
				const messageTexts = extractTextsFromMessage(message);
				texts.push(...messageTexts);
			}
			if (texts.length > 0) {
				// Use single string if only one text, otherwise use array
				requestBody.input = texts.length === 1 ? texts[0] : texts;
			}
		}

		// Add params (excluding tools and instructions as they're handled separately in OpenAI schema)
		if (log.params) {
			const paramsCopy = { ...log.params };
			// Remove tools and instructions from params as they're typically top-level in OpenAI schema
			// Keep all other params (temperature, max_tokens, voice, etc.)
			delete paramsCopy.tools;
			delete paramsCopy.instructions;

			// Merge remaining params into request body
			Object.assign(requestBody, paramsCopy);
		}

		// Add tools if they exist (for chat and responses) - OpenAI schema has tools at top level
		if ((isChat || isResponses) && log.params?.tools && Array.isArray(log.params.tools) && log.params.tools.length > 0) {
			requestBody.tools = log.params.tools;
		}

		// Add instructions if they exist (for responses) - OpenAI schema has instructions at top level
		if (isResponses && log.params?.instructions) {
			requestBody.instructions = log.params.instructions;
		}

		const requestBodyJson = JSON.stringify(requestBody, null, 2);
		navigator.clipboard
			.writeText(requestBodyJson)
			.then(() => {
				toast.success("Request body copied to clipboard");
			})
			.catch((error) => {
				toast.error("Failed to copy request body");
			});
	} catch (error) {
		toast.error("Failed to copy request body");
	}
};
