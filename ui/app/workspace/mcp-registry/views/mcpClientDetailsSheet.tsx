import { getExternalBaseUrl } from "@/app/workspace/mcp-registry/views/mcpUsageGuide/utils";
import { SheetNavigationButtons } from "@/components/sheetNavigationButtons";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { CodeEditor } from "@/components/ui/codeEditor";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { useCopyToClipboard } from "@/hooks/useCopyToClipboard";
import { useSheetNavigation } from "@/hooks/useSheetNavigation";
import { MCP_CREDENTIAL_STATUS_COLORS, MCP_STATUS_COLORS } from "@/lib/constants/config";
import { useGetCoreConfigQuery, useGetMCPSessionsQuery, useGetVirtualMCPsQuery } from "@/lib/store";
import { MCPClient, MCPClientTool } from "@/lib/types/mcp";
import { MCPSessionStatus } from "@/lib/types/mcpSessions";
import { VirtualMCP } from "@/lib/types/virtualMcps";
import { cn } from "@/lib/utils";
import { formatRelativePast } from "@/lib/utils/mcpCredential";
import { perUserDiscoveryOn, toolSubtitle } from "@/lib/utils/mcpPerUserDiscovery";
import { titleCaseFromSnakeCase } from "@/lib/utils/strings";
import { Link } from "@tanstack/react-router";
import { ArrowUpRight, Check, ChevronDown, ChevronRight, CircleAlert, Copy, Info } from "lucide-react";
import { Fragment, ReactNode, useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { DefinitionList, DefinitionRow, MCPClientCredentialSection } from "./mcpClientCredentialSection";
import {
	accessStat,
	alertFor,
	allowedToolsBadges,
	authRows,
	bindingLabel,
	callersState,
	callersSummary,
	callerStatuses,
	heroChips,
	heroRows,
	mcpGlobalSettings,
	schemaParams,
	schemaSummary,
	sessionKinds,
	settingsRows,
	stateDescription,
	toolAccess,
	toolHints,
	toolStat,
	type MCPClientAlertActionKind,
	type MCPClientDetailRow,
} from "./mcpClientDetailsSheet.utils";
import { authScopeLabel } from "./mcpClientFormFields";
import { summarizeSpecSource } from "./mcpClientOpenApiUtils";
import { canReconnectMCPClient } from "./mcpClientsTable.utils";
import { MCPExposeList } from "./mcpExposeList";
import { promptExposeItems, resourceExposeItems } from "./mcpExposeList.utils";
import { MCPToolIconImage } from "./mcpToolIcon";
import { SectionHeader } from "./sectionHeader";

type DetailsTab = "overview" | "tools" | "callers" | "prompts" | "resources" | "access";
type ToolFilter = "all" | "enabled" | "auto" | "off";

interface MCPClientDetailsSheetProps {
	mcpClient: MCPClient;
	onClose: () => void;
	onNavigate?: (direction: "prev" | "next") => void;
	hasPrev?: boolean;
	hasNext?: boolean;
	actions?: ReactNode; // The server's actions menu, owned by the table so it runs the table's handlers
	onAlertAction?: (kind: MCPClientAlertActionKind) => void; // Runs the menu handler behind the alert's button; omitted, the alert has no button
	alertActionBusy?: boolean;
}

const OFF_BADGE = MCP_CREDENTIAL_STATUS_COLORS.unknown;
const SESSION_STATUS_KEYS = {
	active: "sessions.status.active",
	needs_reauth: "sessions.status.needsReauth",
	needs_update: "sessions.status.needsUpdate",
	pending: "sessions.status.pending",
	orphaned: "sessions.status.orphaned",
} as const;
const STATUS_INK: Partial<Record<MCPSessionStatus, string>> = {
	active: "text-chart-success-ink",
	needs_reauth: "text-chart-error-ink",
	needs_update: "text-chart-error-ink",
	orphaned: "text-yellow-800 dark:text-yellow-300",
};
const CALLER_ROWS = 5;
const HERO_COPY_LABELS = {
	endpoint: "registry.sheet.copyEndpointAria",
	calls: "registry.view.copyBaseUrl",
	command: "registry.view.copyCommand",
	url: "registry.view.copyUrl",
} as const;

/**
 * MCPClientDetailsSheet is the read-only view of one MCP server, opened by
 * clicking its row. It leads with what the server is doing (its state, how it
 * is reached, its tools and callers) and keeps configuration in label/value
 * lists. Every change goes through the edit sheet, opened from the row's
 * actions menu; the menu shown here has no Edit item.
 */
export default function MCPClientDetailsSheet({
	mcpClient,
	onClose,
	onNavigate,
	hasPrev = false,
	hasNext = false,
	actions,
	onAlertAction,
	alertActionBusy = false,
}: MCPClientDetailsSheetProps) {
	const { t } = useTranslation("mcp");
	const config = mcpClient.config;
	const [tab, setTab] = useState<DetailsTab>("overview");
	const [toolFilter, setToolFilter] = useState<ToolFilter>("all");
	const [expandedTool, setExpandedTool] = useState<string | null>(null);
	const [callerFilter, setCallerFilter] = useState<MCPSessionStatus | null>(null);

	// Prev/next shows another server: start it on Overview, unfiltered and collapsed.
	useEffect(() => {
		setTab("overview");
		setToolFilter("all");
		setExpandedTool(null);
		setCallerFilter(null);
	}, [config.client_id]);

	const { prev: prevKeys, next: nextKeys } = useSheetNavigation({
		enabled: true,
		hasPrev,
		hasNext,
		onNavigate: (direction) => onNavigate?.(direction),
	});

	const { data: bifrostConfig } = useGetCoreConfigQuery({ fromDB: true });
	const clientConfig = bifrostConfig?.client_config;
	const globals = mcpGlobalSettings(clientConfig);
	const baseUrl = getExternalBaseUrl(clientConfig);

	const scopeLabel = authScopeLabel(config.auth_type);
	const isPerUserAuth =
		config.auth_type === "per_user_oauth" || config.auth_type === "per_user_headers" || config.auth_type === "token_exchange";
	const perUserDiscovery = perUserDiscoveryOn(config);
	const alert = alertFor(mcpClient, canReconnectMCPClient(config));
	const alertAction = alert?.action;
	// The credential block, except where the alert already covers it (pending)
	// or there is none to show (Per-Caller Discovery keeps no admin credential).
	const showCredential = !perUserDiscovery && mcpClient.state !== "pending_verification";

	const tools = mcpClient.tools ?? [];
	const promptItems = useMemo(() => promptExposeItems(mcpClient), [mcpClient]);
	const resourceItems = useMemo(() => resourceExposeItems(mcpClient), [mcpClient]);

	// Reverse lookup for the Access tab: which Virtual MCPs bundle this server's
	// tools. There's no dedicated endpoint, but the list response already carries
	// every vMCP's tools[].mcp_client_id, so membership is derived client-side.
	const { data: virtualMcpsData, isLoading: virtualMcpsLoading, isError: virtualMcpsError } = useGetVirtualMCPsQuery({ limit: 1000 });
	const memberVirtualMcps = useMemo(() => {
		const out: { vmcp: VirtualMCP; toolNames: string[] }[] = [];
		for (const vmcp of virtualMcpsData?.virtual_mcps ?? []) {
			const spec = vmcp.tools.find((t) => t.mcp_client_id === config.client_id);
			if (spec) out.push({ vmcp, toolNames: spec.tool_names });
		}
		return out;
	}, [virtualMcpsData, config.client_id]);
	const vkConfigs = mcpClient.vk_configs ?? [];

	// Callers: per-user OAuth and per-user headers servers store a row per
	// caller. One one-row page per status gives the counts; a short page gives
	// the latest rows. Every other server skips all five queries.
	const kinds = sessionKinds(config.auth_type);
	const hasCallers = kinds.length > 0;
	const statuses = callerStatuses(config.auth_type);
	const countArgs = (status: MCPSessionStatus) => ({ mcp_client_id: [config.client_id], kind: kinds, status: [status], limit: 1 });
	const skipCallers = { skip: !hasCallers };
	const count0 = useGetMCPSessionsQuery(countArgs(statuses[0].status), skipCallers);
	const count1 = useGetMCPSessionsQuery(countArgs(statuses[1].status), skipCallers);
	const count2 = useGetMCPSessionsQuery(countArgs(statuses[2].status), skipCallers);
	const count3 = useGetMCPSessionsQuery(countArgs(statuses[3].status), skipCallers);
	const callerCounts: Partial<Record<MCPSessionStatus, number>> = {
		[statuses[0].status]: count0.currentData?.total_count ?? 0,
		[statuses[1].status]: count1.currentData?.total_count ?? 0,
		[statuses[2].status]: count2.currentData?.total_count ?? 0,
		[statuses[3].status]: count3.currentData?.total_count ?? 0,
	};
	const callerPage = useGetMCPSessionsQuery(
		{ mcp_client_id: [config.client_id], kind: kinds, status: callerFilter ? [callerFilter] : undefined, limit: CALLER_ROWS },
		skipCallers,
	);
	const callerQueries = [count0, count1, count2, count3, callerPage];
	// A failed or unfinished query must not read as nobody having connected.
	const callerState = callersState(callerQueries);
	const callers = callersSummary(callerCounts, callerState);
	const callerRows = callerPage.currentData?.sessions ?? [];
	const retryCallers = () => callerQueries.forEach((query) => query.refetch());

	const toolsStat = toolStat(mcpClient);
	const stats = [
		{ key: "tools", label: t("registry.sheet.tabTools"), ...toolsStat },
		{ key: "access", label: t("registry.columns.access"), ...accessStat(config, vkConfigs.length, memberVirtualMcps.length) },
		...(hasCallers ? [{ key: "callers", label: t("registry.view.callers"), value: callers.value, sub: callers.sub }] : []),
	];

	const toolFilters: { key: ToolFilter; label: string }[] = [
		{ key: "all", label: t("registry.view.filterAll") },
		{ key: "enabled", label: t("common.enabled") },
		{ key: "auto", label: t("registry.sheet.autoExecute") },
		{ key: "off", label: t("registry.view.off") },
	];
	const keepTool = (tool: MCPClientTool, filter: ToolFilter) => {
		const access = toolAccess(config, tool.name);
		return (
			filter === "all" ||
			(filter === "enabled" && access.enabled) ||
			(filter === "auto" && access.autoExecute) ||
			(filter === "off" && !access.enabled)
		);
	};
	const visibleTools = tools.filter((tool) => keepTool(tool, toolFilter));

	return (
		<Sheet open onOpenChange={(open) => !open && onClose()}>
			<SheetContent className="flex w-full flex-col overflow-hidden! pt-4 sm:max-w-[60%]" data-testid="mcpclient-view-sheet">
				<SheetHeader className="w-full p-0 py-4" showCloseButton={false} headerClassName="mb-0 sticky -top-4 bg-card z-10 px-4 md:px-8">
					<div className="flex w-full items-start justify-between gap-4">
						<div className="min-w-0 space-y-2">
							<SheetTitle className="flex w-fit items-center gap-2 font-medium" data-testid="mcpclient-view-title">
								{config.name}
								<Badge className={MCP_STATUS_COLORS[mcpClient.state]} data-testid="mcpclient-view-state">
									{titleCaseFromSnakeCase(mcpClient.state)}
								</Badge>
							</SheetTitle>
							{/* The alert below explains a server that isn't healthy; the sentence would only repeat it. */}
							<SheetDescription className={alert ? "sr-only" : undefined}>
								{alert ? alert.title : stateDescription(mcpClient, isPerUserAuth)}
							</SheetDescription>
						</div>
						<div className="flex shrink-0 items-center gap-1">
							{actions}
							<SheetNavigationButtons
								hasPrev={hasPrev}
								hasNext={hasNext}
								onNavigate={(dir) => onNavigate?.(dir)}
								prevKeys={prevKeys}
								nextKeys={nextKeys}
								entityLabel={t("entities.server", { ns: "common" })}
							/>
						</div>
					</div>
				</SheetHeader>

				<div className="min-h-0 flex-1 space-y-4 overflow-y-auto px-4 pb-10 md:px-8">
					{alert && (
						<Alert
							variant={alert.variant}
							className="has-[>svg]:grid-cols-[calc(var(--spacing)*4)_1fr_auto]"
							data-testid="mcpclient-view-alert"
						>
							{alert.variant === "info" || alert.variant === "default" ? <Info /> : <CircleAlert />}
							<AlertTitle>{alert.title}</AlertTitle>
							<AlertDescription>
								{alert.lines.map((line) => (
									<p key={line}>{line}</p>
								))}
							</AlertDescription>
							{alertAction && onAlertAction && (
								<Button
									type="button"
									variant="outline"
									size="sm"
									className="col-start-3 row-span-2 row-start-1 self-center"
									disabled={alertActionBusy}
									onClick={() => onAlertAction(alertAction.kind)}
									data-testid="mcpclient-view-alert-action"
								>
									{alertAction.label}
								</Button>
							)}
						</Alert>
					)}

					<div className="border-border rounded-sm border" data-testid="mcpclient-view-summary">
						<div className="px-5 pt-5 pb-4">
							<div className="flex flex-wrap items-center gap-2">
								{heroChips(config, scopeLabel).map((chip) => (
									<Badge key={chip.label} variant={chip.variant}>
										{chip.label}
									</Badge>
								))}
							</div>
							<div className="mt-3 space-y-1">
								{heroRows(config, baseUrl, summarizeSpecSource(config.openapi_config)).map((row) => (
									<div key={row.key} className="flex min-w-0 items-center gap-2">
										<div className="text-muted-foreground w-24 shrink-0 text-[10.5px] font-semibold tracking-wider uppercase">
											{row.label}
										</div>
										<code
											className="text-foreground truncate font-mono text-[13px]"
											title={row.value}
											data-testid={row.key === "spec" ? "mcp-client-sheet-openapi-summary" : `mcpclient-view-${row.key}`}
										>
											{row.value}
										</code>
										{row.copy && row.key !== "spec" && (
											<CopyButton
												text={row.copy}
												label={t(HERO_COPY_LABELS[row.key])}
												testId={row.key === "endpoint" ? `mcp-client-sheet-endpoint-copy-${config.endpoint_slug}` : undefined}
											/>
										)}
									</div>
								))}
							</div>
						</div>
						<div className={cn("border-border grid grid-cols-1 border-t", stats.length === 3 ? "sm:grid-cols-3" : "sm:grid-cols-2")}>
							{stats.map((stat, i) => (
								<div
									key={stat.key}
									className={cn("border-border/70 min-w-0 border-b px-5 py-3 sm:border-b-0", i < stats.length - 1 && "sm:border-r")}
									data-testid={`mcpclient-view-stat-${stat.key}`}
								>
									<div className="text-muted-foreground truncate text-[10.5px] font-semibold tracking-wider uppercase">{stat.label}</div>
									<div className="mt-0.5 truncate text-[18px] font-semibold tabular-nums">{stat.value}</div>
									<div className="text-muted-foreground mt-0.5 truncate text-[11px]">{stat.sub}</div>
								</div>
							))}
						</div>
					</div>

					<Tabs value={tab} onValueChange={(value) => setTab(value as DetailsTab)} className="w-full">
						<div className="bg-card sticky top-0 z-10 pb-2">
							<TabsList>
								<TabsTrigger value="overview" data-testid="mcpclient-view-tab-overview">
									{t("registry.view.overview")}
								</TabsTrigger>
								<TabsTrigger value="tools" data-testid="mcpclient-view-tab-tools">
									{t("registry.sheet.tabTools")} <TabCount n={tools.length} />
								</TabsTrigger>
								{hasCallers && (
									<TabsTrigger value="callers" data-testid="mcpclient-view-tab-callers">
										{t("registry.view.callers")} <TabCount n={callers.total} />
									</TabsTrigger>
								)}
								<TabsTrigger value="prompts" data-testid="mcpclient-view-tab-prompts">
									{t("registry.sheet.promptsTitle")} <TabCount n={promptItems.length} />
								</TabsTrigger>
								<TabsTrigger value="resources" data-testid="mcpclient-view-tab-resources">
									{t("registry.sheet.resourcesTitle")} <TabCount n={resourceItems.length} />
								</TabsTrigger>
								<TabsTrigger value="access" data-testid="mcpclient-view-tab-access">
									{t("registry.columns.access")} <TabCount n={vkConfigs.length + memberVirtualMcps.length} />
								</TabsTrigger>
							</TabsList>
						</div>

						<TabsContent value="overview" className="space-y-6">
							<div className="space-y-4">
								<SectionHeader title={t("registry.view.configuration")} description={t("registry.view.configurationDesc")} />
								<DetailRows rows={settingsRows(config, globals)} />
							</div>
							<div className="space-y-4">
								<SectionHeader title={t("registry.sheet.tabAuthentication")} description={t("registry.view.authenticationDesc")} />
								<DetailRows rows={authRows(config, scopeLabel)} />
							</div>
							{showCredential && <MCPClientCredentialSection mcpClient={mcpClient} />}
						</TabsContent>

						<TabsContent value="tools" className="space-y-4">
							<div className="flex items-end justify-between gap-4">
								<SectionHeader
									title={
										perUserDiscovery
											? t("registry.sheet.toolsSeen", { count: tools.length })
											: t("registry.sheet.availableTools", { count: tools.length })
									}
									description={perUserDiscovery ? t("registry.view.toolsSeenDesc") : t("registry.view.toolsDesc")}
								/>
								{tools.length > 0 && (
									<Tabs value={toolFilter} onValueChange={(value) => setToolFilter(value as ToolFilter)}>
										<TabsList className="h-8">
											{toolFilters.map((filter) => (
												<TabsTrigger
													key={filter.key}
													value={filter.key}
													className="text-xs"
													data-testid={`mcpclient-view-tools-filter-${filter.key}`}
												>
													{filter.label} <TabCount n={tools.filter((tool) => keepTool(tool, filter.key)).length} />
												</TabsTrigger>
											))}
										</TabsList>
									</Tabs>
								)}
							</div>
							{visibleTools.length > 0 ? (
								<div className="rounded-md border">
									<Table>
										<TableHeader>
											<TableRow>
												<TableHead className="w-10"></TableHead>
												<TableHead>{t("selectors.tool")}</TableHead>
												<TableHead className="w-24 text-center">{t("common.enabled")}</TableHead>
												<TableHead className="w-28 text-center">{t("registry.sheet.autoExecute")}</TableHead>
												<TableHead className="w-28 text-center">{t("registry.view.priceUsd")}</TableHead>
											</TableRow>
										</TableHeader>
										<TableBody>
											{visibleTools.map((tool) => (
												<ToolRows
													key={tool.name}
													mcpClient={mcpClient}
													tool={tool}
													expanded={expandedTool === tool.name}
													onToggle={() => setExpandedTool(expandedTool === tool.name ? null : tool.name)}
												/>
											))}
										</TableBody>
									</Table>
								</div>
							) : (
								<div className="text-muted-foreground rounded-sm border p-6 text-center text-sm">
									{tools.length > 0
										? t("registry.view.noToolsMatch")
										: perUserDiscovery
											? t("registry.sheet.noToolsSeen")
											: t("registry.sheet.noTools")}
								</div>
							)}
						</TabsContent>

						{hasCallers && (
							<TabsContent value="callers" className="space-y-4">
								{callerState === "loading" ? (
									<EmptyBox>{t("registry.view.loadingCallers")}</EmptyBox>
								) : callerState === "error" ? (
									<div
										className="text-muted-foreground flex flex-col items-center gap-3 rounded-md border border-dashed p-6 text-center text-sm"
										data-testid="mcpclient-view-callers-error"
									>
										{t("registry.view.callersLoadFailed")}
										<Button type="button" variant="outline" size="sm" onClick={retryCallers} data-testid="mcpclient-view-callers-retry">
											{t("common.retry")}
										</Button>
									</div>
								) : (
									<>
										<div className="grid grid-cols-2 rounded-sm border sm:grid-cols-4">
											{statuses.map((s, i) => {
												const n = callerCounts[s.status] ?? 0;
												return (
													<button
														key={s.status}
														type="button"
														onClick={() => setCallerFilter(callerFilter === s.status ? null : s.status)}
														aria-pressed={callerFilter === s.status}
														className={cn(
															"border-border/70 min-w-0 cursor-pointer px-5 py-3 text-left transition-colors",
															i < statuses.length - 1 && "sm:border-r",
															callerFilter === s.status ? "bg-muted" : "hover:bg-muted/50",
														)}
														data-testid={`mcpclient-view-callers-filter-${s.status}`}
													>
														<div className="text-muted-foreground truncate text-[10.5px] font-semibold tracking-wider uppercase">
															{s.label}
														</div>
														<div
															className={cn(
																"mt-0.5 text-[18px] font-semibold tabular-nums",
																n === 0 ? "text-muted-foreground" : STATUS_INK[s.status],
															)}
														>
															{n}
														</div>
													</button>
												);
											})}
										</div>
										<SectionHeader
											title={
												config.auth_type === "per_user_oauth" ? t("registry.view.latestSessions") : t("registry.view.latestSubmissions")
											}
											description={
												config.auth_type === "per_user_oauth"
													? t("registry.credential.userSessionsDesc")
													: t("registry.credential.userSubmissionsDesc")
											}
											action={
												<Button variant="link" size="sm" className="h-auto px-0" asChild>
													<Link
														to="/workspace/mcp-sessions"
														search={{ mcp_client_id: [config.client_id], kind: kinds }}
														data-testid="mcpclient-view-sessions-link"
													>
														{t("registry.view.openInAuthSessions")}
														<ArrowUpRight className="size-3.5" />
													</Link>
												</Button>
											}
										/>
										<div className="rounded-md border">
											<Table>
												<TableHeader>
													<TableRow>
														<TableHead>{t("sessions.columns.boundTo")}</TableHead>
														<TableHead className="w-36">{t("common.status")}</TableHead>
														<TableHead className="w-36">{t("registry.view.updated")}</TableHead>
													</TableRow>
												</TableHeader>
												<TableBody>
													{callerRows.length === 0 ? (
														<TableRow className="hover:bg-transparent">
															<TableCell colSpan={3} className="text-muted-foreground h-20 text-center">
																{config.auth_type === "per_user_oauth"
																	? t("registry.view.noSessionsYet")
																	: t("registry.view.noSubmissionsYet")}
															</TableCell>
														</TableRow>
													) : (
														callerRows.map((row) => {
															const binding = bindingLabel(row);
															const updated =
																(config.auth_type === "per_user_oauth" ? row.last_refreshed_at : row.updated_at) || row.created_at;
															return (
																<TableRow key={`${row.kind}-${row.id}`} data-testid={`mcpclient-view-caller-${row.id}`}>
																	<TableCell>
																		<div className={cn("truncate", row.auth_mode === "session" && "font-mono text-xs")}>{binding.name}</div>
																		<div className="text-muted-foreground text-xs">{binding.kind}</div>
																	</TableCell>
																	<TableCell>
																		<Badge className={MCP_CREDENTIAL_STATUS_COLORS[row.status] ?? MCP_CREDENTIAL_STATUS_COLORS.unknown}>
																			{row.status in SESSION_STATUS_KEYS
																				? t(SESSION_STATUS_KEYS[row.status as keyof typeof SESSION_STATUS_KEYS])
																				: titleCaseFromSnakeCase(row.status)}
																		</Badge>
																	</TableCell>
																	<TableCell className="text-muted-foreground">{updated ? formatRelativePast(updated) : "-"}</TableCell>
																</TableRow>
															);
														})
													)}
												</TableBody>
											</Table>
										</div>
										<p className="text-muted-foreground text-xs">
											{t("registry.view.latestOf", {
												shown: callerRows.length,
												total: callerFilter ? (callerCounts[callerFilter] ?? 0) : (callers.total ?? 0),
											})}
										</p>
									</>
								)}
							</TabsContent>
						)}

						<TabsContent value="prompts">
							<MCPExposeList
								kind="prompts"
								title={t("registry.sheet.promptsTitle")}
								description={t("registry.sheet.promptsDesc")}
								items={promptItems}
								value={config.prompts_to_expose}
								readOnly
								extraColumn={{
									header: t("registry.view.arguments"),
									render: (item) =>
										item.arguments && item.arguments.length > 0 ? (
											<span className="flex flex-wrap gap-1">
												{item.arguments.map((arg) => (
													<Badge key={arg.name} variant="outline" className="font-mono text-xs font-normal">
														{arg.name}
														{arg.required && <span className="text-destructive">*</span>}
													</Badge>
												))}
											</span>
										) : (
											<span className="text-muted-foreground">-</span>
										),
								}}
							/>
						</TabsContent>

						<TabsContent value="resources">
							<MCPExposeList
								kind="resources"
								title={t("registry.sheet.resourcesTitle")}
								description={t("registry.sheet.resourcesDesc")}
								items={resourceItems}
								value={config.resources_to_expose}
								readOnly
								extraColumn={{
									header: t("common.type"),
									render: (item) => <span className="text-muted-foreground font-mono text-xs">{item.mimeType || "-"}</span>,
								}}
							/>
						</TabsContent>

						<TabsContent value="access" className="space-y-6">
							<div className="flex items-start justify-between gap-4 rounded-md border p-4" data-testid="mcpclient-view-allow-by-default">
								<SectionHeader
									title={t("registry.sheet.allowByDefault")}
									description={config.allow_by_default ? t("registry.view.allowByDefaultOnDesc") : t("registry.view.allowByDefaultOffDesc")}
								/>
								<Badge className={config.allow_by_default ? MCP_STATUS_COLORS.healthy : OFF_BADGE}>
									{config.allow_by_default ? t("registry.view.on") : t("registry.view.off")}
								</Badge>
							</div>

							<div className="space-y-3">
								<SectionHeader title={t("registry.sheet.vkAssignments")} description={t("registry.view.vkAssignmentsDesc")} />
								{vkConfigs.length > 0 ? (
									<div className="rounded-md border">
										<Table>
											<TableHeader>
												<TableRow>
													<TableHead className="w-56">{t("registry.sheet.virtualKey")}</TableHead>
													<TableHead>{t("registry.sheet.allowedTools")}</TableHead>
												</TableRow>
											</TableHeader>
											<TableBody>
												{vkConfigs.map((vc) => (
													<TableRow key={vc.virtual_key_id} data-testid={`mcpclient-view-virtualkey-${vc.virtual_key_id}`}>
														<TableCell className="font-medium">{vc.virtual_key_name || vc.virtual_key_id}</TableCell>
														<TableCell className="whitespace-normal">
															<ToolBadges tools={vc.tools_to_execute} />
														</TableCell>
													</TableRow>
												))}
											</TableBody>
										</Table>
									</div>
								) : (
									<EmptyBox>{config.allow_by_default ? t("registry.view.noVkOverrides") : t("registry.sheet.noVkAccess")}</EmptyBox>
								)}
							</div>

							<div className="space-y-3">
								<SectionHeader title={t("registry.sheet.virtualMcpsSection")} description={t("registry.sheet.virtualMcpsSectionDesc")} />
								{virtualMcpsLoading ? (
									<EmptyBox>{t("registry.sheet.loadingVirtualMcps")}</EmptyBox>
								) : virtualMcpsError ? (
									<EmptyBox>{t("registry.sheet.couldNotLoadVirtualMcps")}</EmptyBox>
								) : memberVirtualMcps.length > 0 ? (
									<div className="rounded-md border">
										<Table>
											<TableHeader>
												<TableRow>
													<TableHead className="w-56">{t("virtualMcps.entity")}</TableHead>
													<TableHead className="w-48">{t("registry.columns.endpoint")}</TableHead>
													<TableHead>{t("registry.sheet.exposedTools")}</TableHead>
												</TableRow>
											</TableHeader>
											<TableBody>
												{memberVirtualMcps.map(({ vmcp, toolNames }) => (
													<TableRow key={vmcp.id}>
														<TableCell className="font-medium">
															<div className="flex items-center gap-2">
																{vmcp.name}
																{!vmcp.enabled && (
																	<Badge variant="secondary" className="text-xs font-normal">
																		{t("common.disabled")}
																	</Badge>
																)}
															</div>
														</TableCell>
														<TableCell className="text-muted-foreground font-mono text-xs">/mcp/{vmcp.endpoint_slug}</TableCell>
														<TableCell className="whitespace-normal">
															<ToolBadges tools={toolNames} />
														</TableCell>
													</TableRow>
												))}
											</TableBody>
										</Table>
									</div>
								) : (
									<EmptyBox>{t("registry.sheet.notPartOfVirtualMcp")}</EmptyBox>
								)}
							</div>
						</TabsContent>
					</Tabs>
				</div>
			</SheetContent>
		</Sheet>
	);
}

function TabCount({ n }: { n?: number }) {
	if (n === undefined) return null;
	return <span className="text-muted-foreground text-xs tabular-nums">{n}</span>;
}

function EmptyBox({ children }: { children: ReactNode }) {
	return <div className="text-muted-foreground rounded-md border border-dashed p-6 text-center text-sm">{children}</div>;
}

function CopyButton({ text, label, testId }: { text: string; label: string; testId?: string }) {
	const { t } = useTranslation("mcp");
	const { copy, copied } = useCopyToClipboard({ successMessage: t("common.copied") });
	return (
		<button
			type="button"
			onClick={() => copy(text)}
			className="text-muted-foreground hover:bg-muted hover:text-foreground inline-flex h-6 w-6 shrink-0 cursor-pointer items-center justify-center rounded-sm transition"
			aria-label={label}
			data-testid={testId}
		>
			{copied ? <Check className="h-3.5 w-3.5" /> : <Copy className="h-3.5 w-3.5" />}
		</button>
	);
}

function ToolBadges({ tools }: { tools: string[] }) {
	return (
		<div className="flex flex-wrap gap-1">
			{allowedToolsBadges(tools).map((badge) => (
				<Badge key={badge.label} variant={badge.variant} className="text-xs">
					{badge.label}
				</Badge>
			))}
		</div>
	);
}

function DetailRows({ rows }: { rows: MCPClientDetailRow[] }) {
	return (
		<DefinitionList>
			{rows.map((row) => (
				<DefinitionRow key={row.key} label={row.label}>
					<span className="break-words" data-testid={`mcpclient-view-row-${row.key}`}>
						{row.chips ? (
							<span className="flex flex-wrap gap-1">
								{row.chips.map((chip) => (
									<Badge key={chip} variant="outline" className="font-mono text-xs font-normal">
										{chip}
									</Badge>
								))}
							</span>
						) : (
							row.value
						)}
						{row.note && <span className="text-muted-foreground"> · {row.note}</span>}
					</span>
					{row.hint && <span className="text-muted-foreground mt-0.5 block text-xs">{row.hint}</span>}
				</DefinitionRow>
			))}
		</DefinitionList>
	);
}

const HINT_BADGE: Record<string, string> = {
	read: "border-chart-info/30 bg-chart-info/10 text-chart-info-ink",
	danger: "border-chart-error/30 bg-chart-error/10 text-chart-error-ink",
	plain: "text-muted-foreground border-border bg-transparent",
};

function ToolRows({
	mcpClient,
	tool,
	expanded,
	onToggle,
}: {
	mcpClient: MCPClient;
	tool: MCPClientTool;
	expanded: boolean;
	onToggle: () => void;
}) {
	const { t } = useTranslation("mcp");
	const [showJson, setShowJson] = useState(false);
	const access = toolAccess(mcpClient.config, tool.name);
	const price = mcpClient.config.tool_pricing?.[tool.name];
	const title = tool.title || tool.annotations?.title || tool.name;
	const subtitle = toolSubtitle(mcpClient, tool);
	const hints = toolHints(tool.annotations);
	const perUserDiscovery = perUserDiscoveryOn(mcpClient.config);
	const params = schemaParams(tool.parameters);
	const output = schemaSummary(tool.output_schema);

	return (
		<Fragment>
			<TableRow data-testid={`mcpclient-view-tool-${tool.name}`}>
				<TableCell className="p-2">
					<button
						type="button"
						className="hover:bg-muted flex h-8 w-8 cursor-pointer items-center justify-center rounded-md transition-colors"
						onClick={onToggle}
						aria-label={
							expanded ? t("registry.view.hideSchemaAria", { name: tool.name }) : t("registry.view.showSchemaAria", { name: tool.name })
						}
						aria-expanded={expanded}
						data-testid={`mcp-tool-expand-${tool.name}`}
					>
						{expanded ? <ChevronDown className="h-4 w-4" /> : <ChevronRight className="h-4 w-4" />}
					</button>
				</TableCell>
				<TableCell className="max-w-[340px] whitespace-normal">
					<div className="flex min-w-0 items-start gap-2">
						<MCPToolIconImage tool={tool} />
						<div className="min-w-0">
							<div className="text-foreground truncate text-sm font-medium" data-testid={`mcp-tool-title-${tool.name}`}>
								{title}
							</div>
							{title !== tool.name && <div className="text-muted-foreground truncate font-mono text-xs">{tool.name}</div>}
							{subtitle && <p className="text-muted-foreground mt-0.5 truncate text-xs">{subtitle}</p>}
							{hints.length > 0 && (
								<div className="mt-1.5 flex flex-wrap gap-1">
									{hints.map((hint) => (
										<Badge key={hint.label} className={cn("px-1.5 py-0 text-[11px]", HINT_BADGE[hint.tone])}>
											{hint.label}
										</Badge>
									))}
								</div>
							)}
						</div>
					</div>
				</TableCell>
				<TableCell className="text-center">
					<Badge
						className={access.enabled ? MCP_STATUS_COLORS.healthy : OFF_BADGE}
						data-testid={`mcpclient-view-tool-enabled-${tool.name}`}
					>
						{access.enabled ? t("registry.view.on") : t("registry.view.off")}
					</Badge>
				</TableCell>
				<TableCell className="text-center">
					<Badge
						className={access.autoExecute ? MCP_STATUS_COLORS.healthy : OFF_BADGE}
						data-testid={`mcpclient-view-tool-auto-${tool.name}`}
					>
						{access.autoExecute ? t("registry.view.on") : t("registry.view.off")}
					</Badge>
				</TableCell>
				<TableCell className={cn("text-center tabular-nums", price === undefined && "text-muted-foreground")}>{price ?? "-"}</TableCell>
			</TableRow>
			{expanded && (
				<TableRow className="hover:bg-transparent">
					<TableCell colSpan={5} className="bg-muted/30 p-0 whitespace-normal">
						<div className="space-y-3 px-4 py-3 pl-14">
							{perUserDiscovery ? (
								<p className="text-muted-foreground text-sm">{t("registry.sheet.schemasDiffer")}</p>
							) : (
								<>
									<div className="space-y-2">
										<div className="text-muted-foreground text-xs font-medium">{t("registry.view.parameters")}</div>
										{params.length > 0 ? (
											<div className="bg-card rounded-sm border text-xs">
												{params.map((param) => (
													<div
														key={param.name}
														className="grid grid-cols-[140px_120px_minmax(0,1fr)] items-baseline gap-3 border-t px-3 py-1.5 first:border-t-0"
													>
														<span className="truncate font-mono">
															{param.name}
															{param.required && <span className="text-destructive"> *</span>}
														</span>
														<span className="text-muted-foreground truncate font-mono">{param.type}</span>
														<span className="text-muted-foreground">{param.description || "-"}</span>
													</div>
												))}
											</div>
										) : (
											<div className="text-muted-foreground text-sm">{t("registry.sheet.noParameters")}</div>
										)}
									</div>
									{output && (
										<div className="space-y-1" data-testid={`mcp-tool-output-schema-${tool.name}`}>
											<div className="text-muted-foreground text-xs font-medium">{t("registry.view.returns")}</div>
											<div className="font-mono text-xs">{output}</div>
										</div>
									)}
									<Button
										type="button"
										variant="link"
										className="h-auto p-0"
										onClick={() => setShowJson(!showJson)}
										data-testid={`mcp-tool-json-${tool.name}`}
									>
										{showJson ? t("registry.view.hideJsonSchema") : t("registry.view.viewJsonSchema")}
									</Button>
									{showJson && (
										<CodeEditor
											className="z-0 w-full rounded-sm border"
											shouldAdjustInitialHeight={true}
											maxHeight={300}
											wrap={true}
											code={JSON.stringify(
												tool.output_schema ? { parameters: tool.parameters ?? null, output: tool.output_schema } : (tool.parameters ?? {}),
												null,
												2,
											)}
											lang="json"
											readonly={true}
											options={{ scrollBeyondLastLine: false, collapsibleBlocks: true, lineNumbers: "off", alwaysConsumeMouseWheel: false }}
										/>
									)}
								</>
							)}
						</div>
					</TableCell>
				</TableRow>
			)}
		</Fragment>
	);
}