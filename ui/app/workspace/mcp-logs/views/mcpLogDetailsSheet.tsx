import { formatLatency } from "@/app/workspace/dashboard/utils/chartUtils";
import { getMCPLogPillTone, getMCPLogPresentation, getMCPLogTimeline, type MCPLogPillTone } from "@/lib/utils/mcpLogPresentation";
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
import LogEntryDetailsView from "@/app/workspace/logs/views/logEntryDetailsView";
import BlockHeader from "@/app/workspace/logs/views/blockHeader";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
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
import { Switch } from "@/components/ui/switch";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { useCopyToClipboard } from "@/hooks/useCopyToClipboard";
import { OsIcon } from "@enterprise/lib/constants/edgeApps";
import { useGetDeviceQuery } from "@enterprise/lib/store/apis/edgeControlApi";
import { mapAppToClientApp, mapUserAgentToApp } from "@/lib/constants/logs";
import { cn } from "@/lib/utils";
import { useGetMCPLogByIdQuery, useGetUserAgentMappingsQuery } from "@/lib/store";
import type { MCPToolLogEntry } from "@/lib/types/logs";
import { downloadAsJson } from "@/lib/utils/browser-download";
import { applyRedactionMappingToValue, hasRedactionMappingEntries, mergeRedactionMappings } from "@/lib/utils/redaction";
import PluginLogsView from "@/app/workspace/logs/views/pluginLogsView";
import { Link } from "@tanstack/react-router";
import { format, isValid } from "date-fns";
import { SheetNavigationButtons } from "@/components/sheetNavigationButtons";
import { useSheetNavigation } from "@/hooks/useSheetNavigation";
import { ChevronDown, Clipboard, Download, Loader2, MoreVertical, Trash2 } from "lucide-react";
import { useEffect, useState, type ReactNode } from "react";
import { toast } from "sonner";

interface MCPLogDetailSheetProps {
	log: MCPToolLogEntry | null;
	open: boolean;
	onOpenChange: (open: boolean) => void;
	handleDelete?: (log: MCPToolLogEntry) => Promise<void>;
	canReveal?: boolean;
	onNavigate?: (direction: "prev" | "next") => void;
	hasPrev?: boolean;
	hasNext?: boolean;
}

const pillStyles: Record<MCPLogPillTone, string> = {
	success: "border-chart-success/30 bg-chart-success/10 text-chart-success-ink",
	error: "border-chart-error/30 bg-chart-error/10 text-chart-error-ink",
	processing: "bg-blue-50 text-blue-700 border-blue-200 dark:bg-blue-950/40 dark:text-blue-400 dark:border-blue-900",
	approved: "bg-blue-50 text-blue-700 border-blue-200 dark:bg-blue-950/40 dark:text-blue-400 dark:border-blue-900",
	neutral: "bg-gray-50 text-gray-700 border-gray-200 dark:bg-gray-900/40 dark:text-gray-400 dark:border-gray-800",
};

const pillDotStyles: Record<MCPLogPillTone, string> = {
	success: "bg-chart-success",
	error: "bg-chart-error",
	processing: "bg-blue-500",
	approved: "bg-blue-500",
	neutral: "bg-gray-400",
};

function StatusPill({ label, tone }: { label: string; tone: MCPLogPillTone }) {
	return (
		<span
			className={cn("inline-flex items-center gap-1.5 rounded-sm border px-2 py-0.5 text-[11px] font-semibold uppercase", pillStyles[tone])}
		>
			<span className={cn("h-1.5 w-1.5 rounded-sm", pillDotStyles[tone])} />
			{label}
		</span>
	);
}

function CopyInlineButton({ text, testId }: { text: string; testId?: string }) {
	const { copy } = useCopyToClipboard({ successMessage: "Copied" });
	return (
		<button
			type="button"
			onClick={(e) => {
				e.stopPropagation();
				copy(text);
			}}
			className="text-muted-foreground hover:bg-muted hover:text-foreground inline-flex h-6 w-6 items-center justify-center rounded-sm transition"
			aria-label="Copy"
			data-testid={testId}
		>
			<Clipboard className="h-3.5 w-3.5" />
		</button>
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

export function MCPLogDetailSheet({
	log,
	open,
	onOpenChange,
	handleDelete,
	canReveal = false,
	onNavigate,
	hasPrev = false,
	hasNext = false,
}: MCPLogDetailSheetProps) {
	const [deleteDialogOpen, setDeleteDialogOpen] = useState(false);
	const [dropdownOpen, setDropdownOpen] = useState(false);
	const { data: userAgentMappings } = useGetUserAgentMappingsQuery();
	const [showRevealedValues, setShowRevealedValues] = useState(false);
	const {
		data: fullLog,
		isLoading,
		isError,
	} = useGetMCPLogByIdQuery(log?.id ?? "", {
		skip: !open || !log?.id,
	});

	// Device metadata (hostname, OS) comes from the Edge device registry; fall back to the raw id when absent.
	const deviceId = fullLog?.device_id ?? log?.device_id ?? "";
	const { data: deviceData } = useGetDeviceQuery(deviceId, { skip: !open || !deviceId });
	const device = deviceData?.device;

	// Keyboard navigation: arrow up/down to navigate between logs
	const { prev: prevKeys, next: nextKeys } = useSheetNavigation({
		enabled: open,
		hasPrev,
		hasNext,
		onNavigate: (direction) => onNavigate?.(direction),
	});

	const isFullDataReady = Boolean(log) && (isError || (fullLog?.id === log?.id && !isLoading));
	const displayLog = log ? (isFullDataReady && fullLog ? fullLog : log) : null;
	const revealMapping = displayLog?.redaction_mapping;
	const revealAvailable = canReveal && hasRedactionMappingEntries(revealMapping);
	const revealEnabled = revealAvailable && showRevealedValues;
	const inputRevealMapping = revealEnabled ? revealMapping?.input : undefined;
	const outputRevealMapping = revealEnabled ? revealMapping?.output : undefined;
	const mixedRevealMapping = revealEnabled ? mergeRedactionMappings(revealMapping) : undefined;

	useEffect(() => {
		setShowRevealedValues(false);
	}, [displayLog?.id, revealAvailable]);

	if (!log || !displayLog) return null;

	if (!isFullDataReady) {
		return (
			<Sheet open={open} onOpenChange={onOpenChange}>
				<SheetContent className="border-secondary flex w-full flex-col gap-4 overflow-x-hidden border p-4 sm:max-w-[60%] md:p-8">
					<div className="flex h-full items-center justify-center">
						<SheetTitle className="sr-only">Loading MCP log details</SheetTitle>
						<Loader2 className="text-muted-foreground h-6 w-6 animate-spin" />
					</div>
				</SheetContent>
			</Sheet>
		);
	}

	const appKey = displayLog.app || displayLog.app_key;
	const app = appKey ? mapAppToClientApp(appKey) : mapUserAgentToApp(displayLog.user_agent);
	const mapping = userAgentMappings?.mappings.find((item) => item.app === appKey && item.logo && item.logo_mime);
	const appIcon = mapping ? `data:${mapping.logo_mime};base64,${mapping.logo}` : app.icon;
	const displayedArguments = applyRedactionMappingToValue(displayLog.arguments, inputRevealMapping);
	const displayedResult = applyRedactionMappingToValue(displayLog.result, outputRevealMapping);
	const displayedErrorDetails = applyRedactionMappingToValue(displayLog.error_details, mixedRevealMapping);
	const pluginLogCount = getPluginLogCount(displayLog.plugin_logs);
	const presentation = getMCPLogPresentation(displayLog);
	const { durationMs, startTimestamp, endTimestamp } = getMCPLogTimeline(displayLog, presentation);
	const durationLabel = presentation.durationLabel;
	const endLabel = presentation.policy ? "Policy Check Timestamp" : displayLog.source === "native" ? "Observed Timestamp" : "End Timestamp";
	const pillTone = getMCPLogPillTone(displayLog, presentation);
	const requestId = displayLog.request_id || displayLog.id;
	const durationSub = (() => {
		const startStr = startTimestamp && isValid(startTimestamp) ? format(startTimestamp, "HH:mm:ss") : null;
		const endStr = endTimestamp && isValid(endTimestamp) ? format(endTimestamp, "HH:mm:ss") : null;
		if (startStr && endStr && startStr !== endStr) return `${startStr} → ${endStr}`;
		return startStr ?? endStr ?? "";
	})();
	// Team, customer and business unit can hold more than one value for a request
	// (a user in more than one team, say), so each carries its plural columns
	// alongside the scalar. User, project and device stay scalar-only; their
	// plural slots are just left empty. filterKey ships without its "_ids"
	// suffix here so the pluralized filter always applies to every id, single
	// value included.
	const scopeLinks = (
		[
			["User", "Users", "user_id", displayLog.user_name, displayLog.user_id, undefined, undefined],
			["Team", "Teams", "team_id", displayLog.team_name, displayLog.team_id, displayLog.team_names, displayLog.team_ids],
			[
				"Customer",
				"Customers",
				"customer_id",
				displayLog.customer_name,
				displayLog.customer_id,
				displayLog.customer_names,
				displayLog.customer_ids,
			],
			[
				"Business Unit",
				"Business Units",
				"business_unit_id",
				displayLog.business_unit_name,
				displayLog.business_unit_id,
				displayLog.business_unit_names,
				displayLog.business_unit_ids,
			],
			["Project", "Projects", "project_id", displayLog.project_name, displayLog.project_id, undefined, undefined],
			["Device", "Devices", "device_id", null, displayLog.device_id, undefined, undefined],
		] as const
	)
		.map(([label, pluralLabel, idKey, name, id, names, ids]) => {
			const items = ids?.length
				? ids.map((entryId, i) => ({ id: entryId, name: names?.[i] || entryId }))
				: id
					? [{ id, name: name || id }]
					: [];
			return { label, pluralLabel, idKey, items };
		})
		.filter(({ items }) => items.length > 0);
	const metadataEntries = Object.entries(displayLog.metadata ?? {});

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
							entityLabel="log"
						/>
						<SheetTitle className="flex w-fit items-center gap-2 font-medium">
							<span className="text-foreground text-sm font-medium">Request details</span>
						</SheetTitle>
					</div>
					{revealAvailable && (
						<div className="flex items-center gap-2 whitespace-nowrap">
							<label htmlFor="mcplogdetails-reveal-toggle" className="text-muted-foreground text-[11px] font-medium">
								Show original values
							</label>
							<Switch
								id="mcplogdetails-reveal-toggle"
								checked={revealEnabled}
								onCheckedChange={(checked) => setShowRevealedValues(checked && revealAvailable)}
								data-testid="mcplogdetails-reveal-toggle"
							/>
						</div>
					)}
					<AlertDialog open={deleteDialogOpen} onOpenChange={setDeleteDialogOpen}>
						<DropdownMenu open={dropdownOpen} onOpenChange={setDropdownOpen}>
							<DropdownMenuTrigger asChild>
								<Button variant="ghost" className="size-8" type="button">
									<MoreVertical className="h-3 w-3" />
								</Button>
							</DropdownMenuTrigger>
							<DropdownMenuContent align="end">
								<DropdownMenuItem
									data-testid="export-log-json"
									onSelect={(e) => {
										e.preventDefault();
										downloadAsJson(displayLog, `mcp-log-${displayLog.id ?? "export"}.json`);
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
								<Badge variant="outline" className="rounded-sm px-2 py-0.5 font-medium">
									{displayLog.source === "native" ? "Native Tool" : "MCP Tool"}
								</Badge>
								<StatusPill label={presentation.label} tone={pillTone} />
								{presentation.policy ? (
									<Badge variant="outline" className="bg-card text-muted-foreground rounded-sm px-2 py-0.5 font-normal">
										pre-execution check
									</Badge>
								) : null}
							</div>
							<div className="mt-3 flex items-center gap-2">
								<div className="text-muted-foreground w-24 shrink-0 text-[10.5px] font-semibold tracking-wider uppercase">Tool</div>
								<Link
									to="/workspace/mcp-logs"
									search={{ tool_names: [displayLog.tool_name] }}
									className="truncate font-mono text-[13px] font-medium text-blue-600 hover:underline dark:text-blue-400"
									title={displayLog.tool_name}
									data-testid="mcplogdetails-tool-name-link"
								>
									{displayLog.tool_name}
								</Link>
								<CopyInlineButton text={displayLog.tool_name} testId="mcplogdetails-copy-tool-name-button" />
							</div>
							<div className="mt-1 flex items-center gap-2">
								<div className="text-muted-foreground w-24 shrink-0 text-[10.5px] font-semibold tracking-wider uppercase">Request</div>
								<code className="text-foreground truncate font-mono text-[13px]">{requestId || "—"}</code>
								{requestId ? <CopyInlineButton text={requestId} testId="mcplogdetails-copy-request-id-button" /> : null}
							</div>
							{displayLog.llm_request_id && (
								<div className="mt-1 flex items-center gap-2">
									<div className="text-muted-foreground w-24 shrink-0 text-[10.5px] font-semibold tracking-wider uppercase">
										LLM Request
									</div>
									<Link
										to="/workspace/logs"
										search={{ selected_log: displayLog.llm_request_id }}
										className="truncate font-mono text-[13px] text-blue-600 hover:underline dark:text-blue-400"
										data-testid="mcplogdetails-llm-request-id-link"
									>
										{displayLog.llm_request_id}
									</Link>
									<CopyInlineButton text={displayLog.llm_request_id} testId="mcplogdetails-copy-llm-request-id-button" />
								</div>
							)}
							{(displayLog.virtual_key || displayLog.virtual_key_id) && (
								<div className="mt-1 flex items-center gap-2">
									<div className="text-muted-foreground w-24 shrink-0 text-[10.5px] font-semibold tracking-wider uppercase">Key</div>
									<Link
										to="/workspace/governance/virtual-keys"
										search={{ selected_vk: displayLog.virtual_key?.id || displayLog.virtual_key_id! }}
										className="truncate font-mono text-[13px] text-blue-600 hover:underline dark:text-blue-400"
										data-testid="mcplogdetails-virtual-key-link"
									>
										{displayLog.virtual_key?.name || displayLog.virtual_key_name || displayLog.virtual_key_id}
									</Link>
								</div>
							)}
						</div>
						<div className="flex shrink-0 items-center gap-1.5 rounded-sm border bg-white px-2 py-1 text-[12px] font-medium dark:bg-zinc-900">
							{appIcon && <img src={appIcon} alt={app.name} width={14} height={14} />}
							<span>{app.name}</span>
						</div>
					</div>
					<div className="border-border grid grid-cols-1 border-t sm:grid-cols-2 md:grid-cols-4">
						<HeroStat
							label={durationLabel}
							valueClass="text-primary"
							value={durationMs == null || isNaN(durationMs) ? "—" : formatLatency(durationMs)}
							sub={durationSub}
							hasRightBorder
						/>
						<HeroStat
							label="Server"
							mono
							value={displayLog.source === "native" ? "Local" : displayLog.server_label || "—"}
							sub={displayLog.source === "native" ? "native" : "mcp"}
							valueClass="whitespace-normal overflow-visible break-all"
							hasRightBorder
						/>
						<HeroStat
							label="User"
							value={displayLog.user_name || displayLog.user_id || "—"}
							sub={displayLog.team_name || displayLog.team_id || ""}
							valueClass={displayLog.user_name ? undefined : "font-mono text-[15px]"}
							hasRightBorder
						/>
						<HeroStat
							label="Device"
							mono={!device?.hostname}
							value={
								displayLog.device_id ? (
									<Link
										to="/workspace/mcp-logs"
										search={(prev) => ({ ...prev, offset: 0, selected_log: "", device_ids: [displayLog.device_id!] })}
										className="inline-flex max-w-full items-center gap-1.5 text-blue-600 hover:underline dark:text-blue-400"
										title={displayLog.device_id}
										data-testid="mcplogdetails-device-tile-link"
									>
										<OsIcon platform={device?.platform} className="h-4 w-4 shrink-0" />
										<span className="truncate">{device?.hostname || displayLog.device_id}</span>
									</Link>
								) : (
									"—"
								)
							}
							sub={device ? [device.platform, device.os_version, device.arch].filter(Boolean).join(" · ") : ""}
						/>
					</div>
				</div>
				<details className="group bg-card rounded-sm border" open={false}>
					<summary className="hover:bg-muted/30 flex cursor-pointer items-center justify-between px-4 py-2.5 text-sm transition">
						<span className="text-foreground font-medium">More details</span>
						<span className="text-muted-foreground flex items-center gap-2 text-xs">
							<span className="hidden md:inline">timings, request meta{metadataEntries.length > 0 ? ", metadata" : ""}</span>
							<ChevronDown className="h-3.5 w-3.5 transition-transform group-open:rotate-180" />
						</span>
					</summary>
					<div className="space-y-4 border-t px-4 py-4 md:px-6">
						<p className="text-muted-foreground text-sm">{presentation.description}</p>
						<DottedSeparator />
						<div className="space-y-4">
							<BlockHeader title="Timings" />
							<div className="grid w-full grid-cols-1 items-center justify-between gap-4 md:grid-cols-3">
								<LogEntryDetailsView
									className="w-full"
									label="Start Timestamp"
									value={startTimestamp && isValid(startTimestamp) ? format(startTimestamp, "yyyy-MM-dd hh:mm:ss aa") : "N/A"}
								/>
								<LogEntryDetailsView
									className="w-full"
									label={endLabel}
									value={endTimestamp && isValid(endTimestamp) ? format(endTimestamp, "yyyy-MM-dd hh:mm:ss aa") : "N/A"}
								/>
								<LogEntryDetailsView
									className="w-full"
									label={durationLabel}
									value={durationMs == null || isNaN(durationMs) ? "Not recorded" : `${durationMs.toFixed(2)}ms`}
								/>
							</div>
						</div>
						<DottedSeparator />
						<div className="space-y-4">
							<BlockHeader title="Request Details" />
							<div className="grid w-full grid-cols-1 items-start justify-between gap-4 md:grid-cols-3">
								<LogEntryDetailsView className="col-span-3 w-full" label="Request ID" value={requestId} />
								<LogEntryDetailsView
									className="w-full"
									label="App"
									value={
										<div className="flex items-center gap-2">
											{appIcon && <img src={appIcon} alt={app.name} width={14} height={14} />}
											<span>{app.name}</span>
										</div>
									}
								/>
								<LogEntryDetailsView
									className="w-full"
									label="Tool Name"
									value={<span className="font-mono">{displayLog.tool_name}</span>}
								/>
								<LogEntryDetailsView
									className="w-full"
									label="Server"
									value={
										displayLog.source === "native" ? (
											<Badge variant="secondary">Local · Native</Badge>
										) : displayLog.server_label ? (
											<Link
												to="/workspace/mcp-logs"
												search={{ server_labels: [displayLog.server_label] }}
												data-testid="mcplogdetails-server-link"
											>
												<Badge variant="secondary" className="font-mono hover:underline">
													{displayLog.server_label}
												</Badge>
											</Link>
										) : (
											"-"
										)
									}
								/>
								{scopeLinks.map(({ label, pluralLabel, idKey, items }) => (
									<LogEntryDetailsView
										key={idKey}
										className="w-full"
										label={items.length > 1 ? pluralLabel : label}
										value={
											<span className="inline-flex flex-wrap gap-x-1">
												{items.map((item, i) => (
													<Tooltip key={item.id}>
														<TooltipTrigger asChild>
															<Link
																to="/workspace/mcp-logs"
																search={(prev) => ({ ...prev, offset: 0, selected_log: "", [`${idKey}s`]: [item.id] })}
																className={`text-sm font-normal text-blue-600 underline-offset-2 hover:underline dark:text-blue-400${item.name !== item.id ? "" : " font-mono"}`}
																data-testid={`mcplogdetails-${idKey.replace("_id", "").replaceAll("_", "-")}-link-${item.id}`}
															>
																{item.name}
																{i < items.length - 1 ? "," : ""}
															</Link>
														</TooltipTrigger>
														<TooltipContent sideOffset={6}>
															{item.name !== item.id ? item.id : `Filter by ${label.toLowerCase()}`}
														</TooltipContent>
													</Tooltip>
												))}
											</span>
										}
									/>
								))}
								{(displayLog.virtual_key || displayLog.virtual_key_id) && (
									<LogEntryDetailsView
										className="w-full"
										label="Virtual Key"
										value={displayLog.virtual_key?.name || displayLog.virtual_key_name || displayLog.virtual_key_id}
									/>
								)}
								{displayLog.decision && <LogEntryDetailsView className="w-full" label="Decision" value={displayLog.decision} />}
								{displayLog.llm_request_id && (
									<LogEntryDetailsView
										className="col-span-3 w-full"
										label="LLM Request ID"
										value={<span className="font-mono text-xs">{displayLog.llm_request_id}</span>}
									/>
								)}
							</div>
						</div>
						{metadataEntries.length > 0 && (
							<>
								<DottedSeparator />
								<div className="space-y-4">
									<BlockHeader title="Metadata" />
									<div className="grid w-full grid-cols-1 items-start justify-between gap-4 md:grid-cols-3">
										{metadataEntries.map(([key, value]) => (
											<LogEntryDetailsView key={key} className="w-full" label={key} value={String(value)} />
										))}
									</div>
								</div>
							</>
						)}
					</div>
				</details>

				<Tabs key={displayLog.id} defaultValue="execution" className="gap-2">
					<TabsList className="bg-muted/60 h-10 w-fit">
						<TabsTrigger value="execution" className="px-3">
							Execution
						</TabsTrigger>
						<TabsTrigger value="plugins" className="px-3">
							Plugin Logs
							{pluginLogCount > 0 ? (
								<span className="bg-background text-muted-foreground ml-1.5 rounded-sm border px-2 py-0.5 text-[10px] tabular-nums">
									{pluginLogCount}
								</span>
							) : null}
						</TabsTrigger>
					</TabsList>

					<TabsContent value="execution" className="space-y-4">
						{/* Arguments */}
						{displayedArguments && (
							<div className="w-full rounded-sm border">
								<div className="border-b px-4 py-2 text-sm font-medium md:px-6">Arguments</div>
								<CodeEditor
									className="z-0 w-full"
									shouldAdjustInitialHeight={true}
									maxHeight={250}
									wrap={true}
									code={typeof displayedArguments === "string" ? displayedArguments : JSON.stringify(displayedArguments, null, 2)}
									lang="json"
									readonly={true}
									options={{ scrollBeyondLastLine: false, collapsibleBlocks: true, lineNumbers: "off", alwaysConsumeMouseWheel: false }}
								/>
							</div>
						)}

						{/* Result */}
						{displayedResult && displayLog.status !== "processing" && (
							<div className="w-full rounded-sm border">
								<div className="border-b px-4 py-2 text-sm font-medium md:px-6">Result</div>
								<CodeEditor
									className="z-0 w-full"
									shouldAdjustInitialHeight={true}
									maxHeight={350}
									wrap={true}
									code={typeof displayedResult === "string" ? displayedResult : JSON.stringify(displayedResult, null, 2)}
									lang="json"
									readonly={true}
									options={{ scrollBeyondLastLine: false, collapsibleBlocks: true, lineNumbers: "off", alwaysConsumeMouseWheel: false }}
								/>
							</div>
						)}

						{/* Error Details */}
						{displayedErrorDetails && (
							<div className="border-destructive/50 w-full rounded-sm border">
								<div className="border-destructive/50 text-destructive border-b px-4 py-2 text-sm font-medium md:px-6">Error Details</div>
								<CodeEditor
									className="z-0 w-full"
									shouldAdjustInitialHeight={true}
									maxHeight={250}
									wrap={true}
									code={JSON.stringify(displayedErrorDetails, null, 2)}
									lang="json"
									readonly={true}
									options={{ scrollBeyondLastLine: false, collapsibleBlocks: true, lineNumbers: "off", alwaysConsumeMouseWheel: false }}
								/>
							</div>
						)}
					</TabsContent>

					<TabsContent value="plugins" className="space-y-3">
						{displayLog.plugin_logs ? (
							<PluginLogsView pluginLogs={displayLog.plugin_logs} />
						) : (
							<div className="text-muted-foreground rounded-sm border border-dashed p-5 text-center text-sm">
								No plugin logs for this request.
							</div>
						)}
					</TabsContent>
				</Tabs>
			</SheetContent>
		</Sheet>
	);
}