import { LogsFilterSidebar } from "@/components/filters/logsFilterSidebar";
import { DateTimePickerWithRange } from "@/components/ui/datePickerWithRange";
import { ScrollArea } from "@/components/ui/scrollArea";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { useTimezonePreference } from "@/lib/hooks/useTimezonePreference";
import { parseAsSafeArrayOf } from "@/lib/queryParamsParser";
import { useGetMCPAvailableFilterDataQuery } from "@/lib/store";
import type { LogFilters, MCPToolLogFilters } from "@/lib/types/logs";
import { dateUtils } from "@/lib/types/logs";
import { getRangeForPeriod, TIME_PERIODS } from "@/lib/utils/timeRange";
import { useLocation } from "@tanstack/react-router";
import { parseAsBoolean, parseAsInteger, parseAsString, useQueryStates } from "nuqs";
import { useCallback, useMemo, useRef, useState } from "react";
import { type ChartType } from "./components/charts/chartTypeToggle";
import { ModelFilterSelect } from "./components/charts/modelFilterSelect";
import { ExportPopover } from "./components/exportPopover";
import { type DimensionRankingsTabViewHandle, DimensionRankingsTabView } from "./components/tabViews/dimensionRankingsTabView";
import { type MCPTabViewHandle, MCPTabView } from "./components/tabViews/mcpTabView";
import { type ModelRankingsTabViewHandle, ModelRankingsTabView } from "./components/tabViews/modelRankingsTabView";
import { type OverviewTabViewHandle, OverviewTabView } from "./components/tabViews/overviewTabView";
import { type ProviderUsageTabViewHandle, ProviderUsageTabView } from "./components/tabViews/providerUsageTabView";
import type { DashboardData } from "./utils/exportUtils";

const toChartType = (value: string): ChartType => (value === "line" ? "line" : "bar");

export default function DashboardPage() {
	// MCP filter data
	const { data: mcpFilterData } = useGetMCPAvailableFilterDataQuery();

	const defaultTimeRange = useMemo(() => dateUtils.getDefaultTimeRange(), []);

	const [timezone, setTimezone] = useTimezonePreference();

	const { search } = useLocation();
	const hasExplicitTimeRange = (search as Record<string, unknown>)?.start_time && (search as Record<string, unknown>)?.end_time;

	const [urlState, setUrlState] = useQueryStates(
		{
			period: parseAsString.withDefault(hasExplicitTimeRange ? "" : "1h").withOptions({ clearOnDefault: false }),
			start_time: parseAsInteger.withDefault(defaultTimeRange.startTime),
			end_time: parseAsInteger.withDefault(defaultTimeRange.endTime),
			tab: parseAsString.withDefault("overview"),
			virtual_key_ids: parseAsSafeArrayOf.withDefault([]),
			providers: parseAsSafeArrayOf.withDefault([]),
			models: parseAsSafeArrayOf.withDefault([]),
			selected_key_ids: parseAsSafeArrayOf.withDefault([]),
			objects: parseAsSafeArrayOf.withDefault([]),
			status: parseAsSafeArrayOf.withDefault([]),
			routing_rule_ids: parseAsSafeArrayOf.withDefault([]),
			routing_engine_used: parseAsSafeArrayOf.withDefault([]),
			stop_reasons: parseAsSafeArrayOf.withDefault([]),
			cache_hit_types: parseAsSafeArrayOf.withDefault([]),
			missing_cost_only: parseAsBoolean.withDefault(false),
			metadata_filters: parseAsString.withDefault(""),
			volume_chart: parseAsString.withDefault("bar"),
			token_chart: parseAsString.withDefault("bar"),
			cost_chart: parseAsString.withDefault("bar"),
			model_chart: parseAsString.withDefault("bar"),
			latency_chart: parseAsString.withDefault("bar"),
			throughput_chart: parseAsString.withDefault("bar"),
			cost_model: parseAsString.withDefault("all"),
			usage_model: parseAsString.withDefault("all"),
			provider_cost_chart: parseAsString.withDefault("bar"),
			provider_token_chart: parseAsString.withDefault("bar"),
			provider_latency_chart: parseAsString.withDefault("bar"),
			provider_throughput_chart: parseAsString.withDefault("bar"),
			provider_cost_provider: parseAsString.withDefault("all"),
			provider_token_provider: parseAsString.withDefault("all"),
			provider_latency_provider: parseAsString.withDefault("all"),
			provider_throughput_provider: parseAsString.withDefault("all"),
			mcp_volume_chart: parseAsString.withDefault("bar"),
			mcp_cost_chart: parseAsString.withDefault("bar"),
			mcp_tool_names: parseAsString.withDefault(""),
			mcp_server_labels: parseAsString.withDefault(""),
			parent_request_id: parseAsString.withDefault(""),
			user_ids: parseAsSafeArrayOf.withDefault([]),
			team_ids: parseAsSafeArrayOf.withDefault([]),
			customer_ids: parseAsSafeArrayOf.withDefault([]),
			business_unit_ids: parseAsSafeArrayOf.withDefault([]),
			aliases: parseAsSafeArrayOf.withDefault([]),
		},
		{
			history: "push",
			shallow: false,
		},
	);

	// Parse string-backed MCP filter values from URL state
	const selectedMcpToolNames = useMemo(() => (urlState.mcp_tool_names ? [urlState.mcp_tool_names] : []), [urlState.mcp_tool_names]);
	const selectedMcpServerLabels = useMemo(
		() => (urlState.mcp_server_labels ? [urlState.mcp_server_labels] : []),
		[urlState.mcp_server_labels],
	);

	const metadataFilters = useMemo(() => {
		if (!urlState.metadata_filters) return undefined;
		try {
			return JSON.parse(urlState.metadata_filters) as Record<string, string>;
		} catch {
			return undefined;
		}
	}, [urlState.metadata_filters]);

	const filters: LogFilters = useMemo(
		() => ({
			...(urlState.period
				? { period: urlState.period }
				: {
						start_time: dateUtils.toISOString(urlState.start_time),
						end_time: dateUtils.toISOString(urlState.end_time),
					}),
			...(urlState.providers.length > 0 && { providers: urlState.providers }),
			...(urlState.models.length > 0 && { models: urlState.models }),
			...(urlState.selected_key_ids.length > 0 && { selected_key_ids: urlState.selected_key_ids }),
			...(urlState.virtual_key_ids.length > 0 && {
				virtual_key_ids: urlState.virtual_key_ids,
			}),
			...(urlState.objects.length > 0 && { objects: urlState.objects }),
			...(urlState.status.length > 0 && { status: urlState.status }),
			...(urlState.routing_rule_ids.length > 0 && {
				routing_rule_ids: urlState.routing_rule_ids,
			}),
			...(urlState.routing_engine_used.length > 0 && {
				routing_engine_used: urlState.routing_engine_used,
			}),
			...(urlState.stop_reasons.length > 0 && { stop_reasons: urlState.stop_reasons }),
			...(urlState.cache_hit_types.length > 0 && { cache_hit_types: urlState.cache_hit_types }),
			...(urlState.missing_cost_only && { missing_cost_only: true }),
			...(metadataFilters &&
				Object.keys(metadataFilters).length > 0 && {
					metadata_filters: metadataFilters,
				}),
			...(urlState.parent_request_id && { parent_request_id: urlState.parent_request_id }),
			...(urlState.user_ids.length > 0 && { user_ids: urlState.user_ids }),
			...(urlState.team_ids.length > 0 && { team_ids: urlState.team_ids }),
			...(urlState.customer_ids.length > 0 && { customer_ids: urlState.customer_ids }),
			...(urlState.business_unit_ids.length > 0 && { business_unit_ids: urlState.business_unit_ids }),
			...(urlState.aliases.length > 0 && { aliases: urlState.aliases }),
		}),
		[
			urlState.period,
			urlState.start_time,
			urlState.end_time,
			urlState.parent_request_id,
			urlState.providers,
			urlState.models,
			urlState.selected_key_ids,
			urlState.virtual_key_ids,
			urlState.objects,
			urlState.status,
			urlState.routing_rule_ids,
			urlState.routing_engine_used,
			urlState.stop_reasons,
			urlState.cache_hit_types,
			urlState.missing_cost_only,
			metadataFilters,
			urlState.user_ids,
			urlState.team_ids,
			urlState.customer_ids,
			urlState.business_unit_ids,
			urlState.aliases,
		],
	);

	const mcpFilters: MCPToolLogFilters = useMemo(
		() => ({
			...(urlState.period
				? { period: urlState.period }
				: {
						start_time: dateUtils.toISOString(urlState.start_time),
						end_time: dateUtils.toISOString(urlState.end_time),
					}),
			...(selectedMcpToolNames.length > 0 && {
				tool_names: selectedMcpToolNames,
			}),
			...(selectedMcpServerLabels.length > 0 && {
				server_labels: selectedMcpServerLabels,
			}),
			...(urlState.status.length > 0 && { status: urlState.status }),
			...(urlState.virtual_key_ids.length > 0 && {
				virtual_key_ids: urlState.virtual_key_ids,
			}),
		}),
		[
			urlState.period,
			urlState.start_time,
			urlState.end_time,
			selectedMcpToolNames,
			selectedMcpServerLabels,
			urlState.status,
			urlState.virtual_key_ids,
		],
	);

	// Tab view refs for export data aggregation
	const overviewRef = useRef<OverviewTabViewHandle>(null);
	const providerRef = useRef<ProviderUsageTabViewHandle>(null);
	const mcpRef = useRef<MCPTabViewHandle>(null);
	const modelRankingsRef = useRef<ModelRankingsTabViewHandle>(null);
	const teamRankingsRef = useRef<DimensionRankingsTabViewHandle>(null);
	const customerRankingsRef = useRef<DimensionRankingsTabViewHandle>(null);
	const buRankingsRef = useRef<DimensionRankingsTabViewHandle>(null);
	const userRankingsRef = useRef<DimensionRankingsTabViewHandle>(null);
	const virtualKeyRankingsRef = useRef<DimensionRankingsTabViewHandle>(null);

	const allRefs = [
		overviewRef,
		providerRef,
		mcpRef,
		modelRankingsRef,
		teamRankingsRef,
		customerRankingsRef,
		buRankingsRef,
		userRankingsRef,
		virtualKeyRankingsRef,
	];

	const getDashboardData = useCallback((): DashboardData => {
		const merged: Partial<DashboardData> = {};
		for (const r of allRefs) {
			if (r.current) Object.assign(merged, r.current.getData());
		}
		return {
			histogramData: null,
			tokenData: null,
			costData: null,
			modelData: null,
			latencyData: null,
			providerCostData: null,
			providerTokenData: null,
			providerLatencyData: null,
			rankingsData: null,
			teamRankingsData: null,
			customerRankingsData: null,
			buRankingsData: null,
			userRankingsData: null,
			virtualKeyRankingsData: null,
			mcpHistogramData: null,
			mcpCostData: null,
			mcpTopToolsData: null,
			...merged,
		};
	}, []);

	const handlePreloadData = useCallback(async () => {
		await Promise.all(allRefs.map((r) => r.current?.loadData()));
	}, []);

	// Tab change handler
	const handleTabChange = useCallback(
		(tab: string) => {
			setUrlState({ tab });
		},
		[setUrlState],
	);

	// Chart type toggles
	const handleVolumeChartToggle = useCallback((type: ChartType) => setUrlState({ volume_chart: type }), [setUrlState]);
	const handleTokenChartToggle = useCallback((type: ChartType) => setUrlState({ token_chart: type }), [setUrlState]);
	const handleCostChartToggle = useCallback((type: ChartType) => setUrlState({ cost_chart: type }), [setUrlState]);
	const handleModelChartToggle = useCallback((type: ChartType) => setUrlState({ model_chart: type }), [setUrlState]);
	const handleLatencyChartToggle = useCallback((type: ChartType) => setUrlState({ latency_chart: type }), [setUrlState]);
	const handleThroughputChartToggle = useCallback((type: ChartType) => setUrlState({ throughput_chart: type }), [setUrlState]);
	const handleProviderCostChartToggle = useCallback((type: ChartType) => setUrlState({ provider_cost_chart: type }), [setUrlState]);
	const handleProviderTokenChartToggle = useCallback((type: ChartType) => setUrlState({ provider_token_chart: type }), [setUrlState]);
	const handleProviderLatencyChartToggle = useCallback((type: ChartType) => setUrlState({ provider_latency_chart: type }), [setUrlState]);
	const handleProviderThroughputChartToggle = useCallback((type: ChartType) => setUrlState({ provider_throughput_chart: type }), [setUrlState]);
	const handleMcpVolumeChartToggle = useCallback((type: ChartType) => setUrlState({ mcp_volume_chart: type }), [setUrlState]);
	const handleMcpCostChartToggle = useCallback((type: ChartType) => setUrlState({ mcp_cost_chart: type }), [setUrlState]);

	// Model / provider filter changes
	const handleCostModelChange = useCallback((model: string) => setUrlState({ cost_model: model }), [setUrlState]);
	const handleUsageModelChange = useCallback((model: string) => setUrlState({ usage_model: model }), [setUrlState]);
	const handleProviderCostProviderChange = useCallback(
		(provider: string) => setUrlState({ provider_cost_provider: provider }),
		[setUrlState],
	);
	const handleProviderTokenProviderChange = useCallback(
		(provider: string) => setUrlState({ provider_token_provider: provider }),
		[setUrlState],
	);
	const handleProviderLatencyProviderChange = useCallback(
		(provider: string) => setUrlState({ provider_latency_provider: provider }),
		[setUrlState],
	);
	const handleProviderThroughputProviderChange = useCallback(
		(provider: string) => setUrlState({ provider_throughput_provider: provider }),
		[setUrlState],
	);

	// Adapter: converts a full LogFilters object to dashboard URL state
	const setFilters = useCallback(
		(newFilters: LogFilters) => {
			// The sidebar/header only manage dimension filters, never the time range: in
			// period mode `newFilters` carries no start/end, so only touch time when an
			// explicit range is actually provided — otherwise we'd clear the active period.
			const hasExplicitTime = !!newFilters.start_time && !!newFilters.end_time;
			const newStartTime = hasExplicitTime ? dateUtils.toUnixTimestamp(new Date(newFilters.start_time!)) : undefined;
			const newEndTime = hasExplicitTime ? dateUtils.toUnixTimestamp(new Date(newFilters.end_time!)) : undefined;
			const timeChanged = hasExplicitTime && (newStartTime !== urlState.start_time || newEndTime !== urlState.end_time);
			setUrlState({
				...(timeChanged && { period: "", start_time: newStartTime, end_time: newEndTime }),
				providers: newFilters.providers || [],
				models: newFilters.models || [],
				selected_key_ids: newFilters.selected_key_ids || [],
				virtual_key_ids: newFilters.virtual_key_ids || [],
				objects: newFilters.objects || [],
				status: newFilters.status || [],
				routing_rule_ids: newFilters.routing_rule_ids || [],
				routing_engine_used: newFilters.routing_engine_used || [],
				stop_reasons: newFilters.stop_reasons || [],
				cache_hit_types: newFilters.cache_hit_types || [],
				missing_cost_only: newFilters.missing_cost_only ?? false,
				metadata_filters:
					newFilters.metadata_filters && Object.keys(newFilters.metadata_filters).length > 0
						? JSON.stringify(newFilters.metadata_filters)
						: "",
				parent_request_id: newFilters.parent_request_id || "",
				user_ids: newFilters.user_ids || [],
				team_ids: newFilters.team_ids || [],
				customer_ids: newFilters.customer_ids || [],
				business_unit_ids: newFilters.business_unit_ids || [],
				aliases: newFilters.aliases || [],
			});
		},
		[setUrlState, urlState.start_time, urlState.end_time],
	);

	// Date range for picker
	const dateRange = useMemo(
		() => ({
			from: dateUtils.fromUnixTimestamp(urlState.start_time),
			to: dateUtils.fromUnixTimestamp(urlState.end_time),
		}),
		[urlState.start_time, urlState.end_time],
	);

	const handlePeriodChange = useCallback(
		(period: string | undefined) => {
			if (!period) return;
			const { from, to } = getRangeForPeriod(period);
			setUrlState({
				period,
				start_time: Math.floor(from.getTime() / 1000),
				end_time: Math.floor(to.getTime() / 1000),
			});
		},
		[setUrlState],
	);

	const handleDateRangeChange = useCallback(
		(range: { from?: Date; to?: Date }) => {
			if (!range.from || !range.to) return;
			setUrlState({
				period: "",
				start_time: dateUtils.toUnixTimestamp(range.from),
				end_time: dateUtils.toUnixTimestamp(range.to),
			});
		},
		[setUrlState],
	);

	// PDF export mode
	const [pdfMode, setPdfMode] = useState(false);
	const dashboardMinHeightRef = useRef<string>("");
	const hiddenTabsRef = useRef<HTMLElement[]>([]);

	const handlePdfExport = useCallback(async (): Promise<HTMLElement[]> => {
		await handlePreloadData();
		setPdfMode(true);

		await new Promise<void>((resolve) => {
			requestAnimationFrame(() => {
				requestAnimationFrame(() => resolve());
			});
		});

		const hiddenTabs = document.querySelectorAll<HTMLElement>('[data-slot="tabs-content"][hidden]');
		hiddenTabsRef.current = Array.from(hiddenTabs);
		for (const tab of hiddenTabs) {
			tab.removeAttribute("hidden");
			tab.style.display = "block";
		}

		const dashboardEl = document.getElementById("dashboard-root");
		if (dashboardEl) {
			dashboardMinHeightRef.current = dashboardEl.style.minHeight;
			dashboardEl.style.minHeight = "0";
		}

		window.dispatchEvent(new Event("resize"));
		await new Promise<void>((resolve) => {
			requestAnimationFrame(() => {
				requestAnimationFrame(() => resolve());
			});
		});

		const ids = [
			"dashboard-section-overview",
			"dashboard-section-provider-usage",
			"dashboard-section-rankings",
			"dashboard-section-mcp",
			"dashboard-section-team-rankings",
			"dashboard-section-customer-rankings",
			"dashboard-section-bu-rankings",
			"dashboard-section-user-rankings",
			"dashboard-section-virtual-key-rankings",
		];
		return ids.map((id) => document.getElementById(id)).filter(Boolean) as HTMLElement[];
	}, [handlePreloadData]);

	const handlePdfExportDone = useCallback(() => {
		const dashboardEl = document.getElementById("dashboard-root");
		if (dashboardEl) {
			dashboardEl.style.minHeight = dashboardMinHeightRef.current;
		}

		for (const tab of hiddenTabsRef.current) {
			tab.setAttribute("hidden", "");
			tab.style.display = "";
		}
		hiddenTabsRef.current = [];

		setPdfMode(false);
	}, []);

	const activeTab = urlState.tab || "overview";

	return (
		<div id="dashboard-root" className="no-padding-parent no-border-parent bg-background flex h-[calc(100vh_-_16px)] w-full gap-3">
			{/* Sidebar Filters */}
			<LogsFilterSidebar filters={filters} onFiltersChange={setFilters} />

			{/* Main Content */}
			<ScrollArea className="bg-card flex min-w-0 flex-1 flex-col gap-4 rounded-l-md" viewportClassName="no-table">
				{/* Header */}
				<div className="flex items-center justify-between p-4">
					<div className="flex items-center gap-2">
						<h1 className="text-lg font-semibold">Dashboard</h1>
					</div>
					<div className="flex items-center gap-2">
						<ExportPopover
							getData={getDashboardData}
							onPreloadData={handlePreloadData}
							onPdfExport={handlePdfExport}
							onPdfExportDone={handlePdfExportDone}
						/>
						{activeTab === "mcp" && mcpFilterData && (
							<div className="flex items-center gap-1">
								{(mcpFilterData.tool_names?.length ?? 0) > 0 && (
									<ModelFilterSelect
										models={mcpFilterData.tool_names ?? []}
										selectedModel={selectedMcpToolNames.length === 1 ? selectedMcpToolNames[0] : "all"}
										onModelChange={(value) => {
											if (value === "all") {
												setUrlState({ mcp_tool_names: "" });
											} else {
												setUrlState({ mcp_tool_names: value });
											}
										}}
										placeholder="All Tools"
										data-testid="dashboard-mcp-tool-filter"
									/>
								)}
								{(mcpFilterData.server_labels?.length ?? 0) > 0 && (
									<ModelFilterSelect
										models={mcpFilterData.server_labels ?? []}
										selectedModel={selectedMcpServerLabels.length === 1 ? selectedMcpServerLabels[0] : "all"}
										onModelChange={(value) => {
											if (value === "all") {
												setUrlState({ mcp_server_labels: "" });
											} else {
												setUrlState({ mcp_server_labels: value });
											}
										}}
										placeholder="All Servers"
										data-testid="dashboard-mcp-server-filter"
									/>
								)}
							</div>
						)}
						<DateTimePickerWithRange
							dateTime={dateRange}
							onDateTimeUpdate={handleDateRangeChange}
							preDefinedPeriods={TIME_PERIODS}
							predefinedPeriod={urlState.period || undefined}
							onPredefinedPeriodChange={handlePeriodChange}
							triggerTestId="dashboard-filter-daterange"
							popupAlignment="end"
							showTimezone
							timezone={timezone}
							onTimezoneChange={setTimezone}
						/>
					</div>
				</div>

				<div className="p-4">
					{/* Tabs */}
					<Tabs value={activeTab} onValueChange={handleTabChange}>
						<div className="mb-2 max-w-full overflow-x-auto">
							<TabsList className="w-max min-w-max">
								<TabsTrigger className="shrink-0" value="overview" data-testid="dashboard-tab-overview">
									Overview
								</TabsTrigger>
								<TabsTrigger className="shrink-0" value="provider-usage" data-testid="dashboard-tab-provider-usage">
									Provider Usage
								</TabsTrigger>
								<TabsTrigger className="shrink-0" value="rankings" data-testid="dashboard-tab-rankings">
									Model Rankings
								</TabsTrigger>
								<TabsTrigger className="shrink-0" value="mcp" data-testid="dashboard-tab-mcp">
									MCP usage
								</TabsTrigger>
								<TabsTrigger className="shrink-0" value="team-rankings" data-testid="dashboard-tab-team-rankings">
									Team Rankings
								</TabsTrigger>
								<TabsTrigger className="shrink-0" value="user-rankings" data-testid="dashboard-tab-user-rankings">
									User Rankings
								</TabsTrigger>
								<TabsTrigger className="shrink-0" value="virtual-key-rankings" data-testid="dashboard-tab-virtual-key-rankings">
									Virtual Key Rankings
								</TabsTrigger>
								<TabsTrigger className="shrink-0" value="customer-rankings" data-testid="dashboard-tab-customer-rankings">
									Customer Rankings
								</TabsTrigger>
								<TabsTrigger className="shrink-0" value="bu-rankings" data-testid="dashboard-tab-bu-rankings">
									BU Rankings
								</TabsTrigger>
							</TabsList>
						</div>

						{/* Overview Tab */}
						<TabsContent value="overview" {...(pdfMode && { forceMount: true })}>
							<div id="dashboard-section-overview">
								<OverviewTabView
									ref={overviewRef}
									filters={filters}
									active={activeTab === "overview" || pdfMode}
									startTime={urlState.start_time}
									endTime={urlState.end_time}
									volumeChartType={toChartType(urlState.volume_chart)}
									tokenChartType={toChartType(urlState.token_chart)}
									costChartType={toChartType(urlState.cost_chart)}
									modelChartType={toChartType(urlState.model_chart)}
									latencyChartType={toChartType(urlState.latency_chart)}
									throughputChartType={toChartType(urlState.throughput_chart)}
									costModel={urlState.cost_model}
									usageModel={urlState.usage_model}
									onVolumeChartToggle={handleVolumeChartToggle}
									onTokenChartToggle={handleTokenChartToggle}
									onCostChartToggle={handleCostChartToggle}
									onModelChartToggle={handleModelChartToggle}
									onLatencyChartToggle={handleLatencyChartToggle}
									onThroughputChartToggle={handleThroughputChartToggle}
									onCostModelChange={handleCostModelChange}
									onUsageModelChange={handleUsageModelChange}
								/>
							</div>
						</TabsContent>

						{/* Provider Usage Tab */}
						<TabsContent value="provider-usage" {...(pdfMode && { forceMount: true })}>
							<div id="dashboard-section-provider-usage">
								<ProviderUsageTabView
									ref={providerRef}
									filters={filters}
									active={activeTab === "provider-usage" || pdfMode}
									startTime={urlState.start_time}
									endTime={urlState.end_time}
									providerCostChartType={toChartType(urlState.provider_cost_chart)}
									providerTokenChartType={toChartType(urlState.provider_token_chart)}
									providerLatencyChartType={toChartType(urlState.provider_latency_chart)}
									providerThroughputChartType={toChartType(urlState.provider_throughput_chart)}
									providerCostProvider={urlState.provider_cost_provider}
									providerTokenProvider={urlState.provider_token_provider}
									providerLatencyProvider={urlState.provider_latency_provider}
									providerThroughputProvider={urlState.provider_throughput_provider}
									onProviderCostChartToggle={handleProviderCostChartToggle}
									onProviderTokenChartToggle={handleProviderTokenChartToggle}
									onProviderLatencyChartToggle={handleProviderLatencyChartToggle}
									onProviderThroughputChartToggle={handleProviderThroughputChartToggle}
									onProviderCostProviderChange={handleProviderCostProviderChange}
									onProviderTokenProviderChange={handleProviderTokenProviderChange}
									onProviderLatencyProviderChange={handleProviderLatencyProviderChange}
									onProviderThroughputProviderChange={handleProviderThroughputProviderChange}
								/>
							</div>
						</TabsContent>

						{/* Model Rankings Tab */}
						<TabsContent value="rankings" {...(pdfMode && { forceMount: true })}>
							<div id="dashboard-section-rankings">
								<ModelRankingsTabView
									ref={modelRankingsRef}
									filters={filters}
									active={activeTab === "rankings" || pdfMode}
									startTime={urlState.start_time}
									endTime={urlState.end_time}
								/>
							</div>
						</TabsContent>

						{/* MCP Tab */}
						<TabsContent value="mcp" {...(pdfMode && { forceMount: true })}>
							<div id="dashboard-section-mcp">
								<MCPTabView
									ref={mcpRef}
									filters={mcpFilters}
									active={activeTab === "mcp" || pdfMode}
									startTime={urlState.start_time}
									endTime={urlState.end_time}
									mcpVolumeChartType={toChartType(urlState.mcp_volume_chart)}
									mcpCostChartType={toChartType(urlState.mcp_cost_chart)}
									onMcpVolumeChartToggle={handleMcpVolumeChartToggle}
									onMcpCostChartToggle={handleMcpCostChartToggle}
								/>
							</div>
						</TabsContent>

						{/* Team Rankings Tab */}
						<TabsContent value="team-rankings" {...(pdfMode && { forceMount: true })}>
							<div id="dashboard-section-team-rankings">
								<DimensionRankingsTabView
									ref={teamRankingsRef}
									filters={filters}
									active={activeTab === "team-rankings" || pdfMode}
									dimension="team"
									dimensionLabel="Team"
									testIdPrefix="dashboard-team-rankings"
									dataKey="teamRankingsData"
								/>
							</div>
						</TabsContent>

						{/* Customer Rankings Tab */}
						<TabsContent value="customer-rankings" {...(pdfMode && { forceMount: true })}>
							<div id="dashboard-section-customer-rankings">
								<DimensionRankingsTabView
									ref={customerRankingsRef}
									filters={filters}
									active={activeTab === "customer-rankings" || pdfMode}
									dimension="customer"
									dimensionLabel="Customer"
									testIdPrefix="dashboard-customer-rankings"
									dataKey="customerRankingsData"
								/>
							</div>
						</TabsContent>

						{/* Business Unit Rankings Tab */}
						<TabsContent value="bu-rankings" {...(pdfMode && { forceMount: true })}>
							<div id="dashboard-section-bu-rankings">
								<DimensionRankingsTabView
									ref={buRankingsRef}
									filters={filters}
									active={activeTab === "bu-rankings" || pdfMode}
									dimension="business_unit"
									dimensionLabel="Business Unit"
									testIdPrefix="dashboard-bu-rankings"
									dataKey="buRankingsData"
								/>
							</div>
						</TabsContent>

						{/* User Rankings Tab */}
						<TabsContent value="user-rankings" {...(pdfMode && { forceMount: true })}>
							<div id="dashboard-section-user-rankings">
								<DimensionRankingsTabView
									ref={userRankingsRef}
									filters={filters}
									active={activeTab === "user-rankings" || pdfMode}
									dimension="user"
									dimensionLabel="User"
									testIdPrefix="dashboard-user-rankings"
									dataKey="userRankingsData"
								/>
							</div>
						</TabsContent>

						{/* Virtual Key Rankings Tab */}
						<TabsContent value="virtual-key-rankings" {...(pdfMode && { forceMount: true })}>
							<div id="dashboard-section-virtual-key-rankings">
								<DimensionRankingsTabView
									ref={virtualKeyRankingsRef}
									filters={filters}
									active={activeTab === "virtual-key-rankings" || pdfMode}
									dimension="virtual_key"
									dimensionLabel="Virtual Key"
									testIdPrefix="dashboard-virtual-key-rankings"
									dataKey="virtualKeyRankingsData"
								/>
							</div>
						</TabsContent>
					</Tabs>
				</div>
			</ScrollArea>
		</div>
	);
}
