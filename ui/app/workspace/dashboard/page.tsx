"use client";

import { FilterPopover } from "@/components/filters/filterPopover";
import { DateTimePickerWithRange } from "@/components/ui/datePickerWithRange";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import {
	useGetMCPAvailableFilterDataQuery,
	useLazyGetLogsCostHistogramQuery,
	useLazyGetLogsHistogramQuery,
	useLazyGetLogsLatencyHistogramQuery,
	useLazyGetLogsModelHistogramQuery,
	useLazyGetLogsProviderCostHistogramQuery,
	useLazyGetLogsProviderLatencyHistogramQuery,
	useLazyGetLogsProviderTokenHistogramQuery,
	useLazyGetLogsTokenHistogramQuery,
	useLazyGetMCPCostHistogramQuery,
	useLazyGetMCPHistogramQuery,
	useLazyGetMCPTopToolsQuery,
	useLazyGetModelRankingsQuery,
} from "@/lib/store";
import type {
	CostHistogramResponse,
	LatencyHistogramResponse,
	LogFilters,
	LogsHistogramResponse,
	MCPCostHistogramResponse,
	MCPHistogramResponse,
	MCPToolLogFilters,
	MCPTopToolsResponse,
	ModelHistogramResponse,
	ModelRankingsResponse,
	ProviderCostHistogramResponse,
	ProviderLatencyHistogramResponse,
	ProviderTokenHistogramResponse,
	TokenHistogramResponse,
} from "@/lib/types/logs";
import { dateUtils } from "@/lib/types/logs";
import { parseAsInteger, parseAsString, useQueryStates } from "nuqs";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { type ChartType } from "./components/charts/chartTypeToggle";
import { ModelFilterSelect } from "./components/charts/modelFilterSelect";
import { MCPTab } from "./components/mcpTab";
import { ModelRankingsTab } from "./components/modelRankingsTab";
import { OverviewTab } from "./components/overviewTab";
import { ProviderUsageTab } from "./components/providerUsageTab";

// Type-safe parser for chart type URL state
const toChartType = (value: string): ChartType => (value === "line" ? "line" : "bar");

// Calculate default timestamps once at module level
const DEFAULT_END_TIME = Math.floor(Date.now() / 1000);
const DEFAULT_START_TIME = (() => {
	const date = new Date();
	date.setHours(date.getHours() - 24);
	return Math.floor(date.getTime() / 1000);
})();

// Predefined time periods
const TIME_PERIODS = [
	{ label: "Last hour", value: "1h" },
	{ label: "Last 6 hours", value: "6h" },
	{ label: "Last 24 hours", value: "24h" },
	{ label: "Last 7 days", value: "7d" },
	{ label: "Last 30 days", value: "30d" },
];

const parseCsvParam = (value: string): string[] => (value ? value.split(",").filter(Boolean) : []);
const sanitizeSeriesLabels = (values?: string[]): string[] => {
	if (!values) return [];
	const trimmedValues = values.map((value) => value.trim()).filter((value) => value.length > 0);

	return [...new Set(trimmedValues)];
};

function getTimeRangeFromPeriod(period: string): { start: number; end: number } {
	const now = Math.floor(Date.now() / 1000);
	switch (period) {
		case "1h":
			return { start: now - 3600, end: now };
		case "6h":
			return { start: now - 6 * 3600, end: now };
		case "24h":
			return { start: now - 24 * 3600, end: now };
		case "7d":
			return { start: now - 7 * 24 * 3600, end: now };
		case "30d":
			return { start: now - 30 * 24 * 3600, end: now };
		default:
			return { start: now - 24 * 3600, end: now };
	}
}

export default function DashboardPage() {
	// Data states - Overview
	const [histogramData, setHistogramData] = useState<LogsHistogramResponse | null>(null);
	const [tokenData, setTokenData] = useState<TokenHistogramResponse | null>(null);
	const [costData, setCostData] = useState<CostHistogramResponse | null>(null);
	const [modelData, setModelData] = useState<ModelHistogramResponse | null>(null);
	const [latencyData, setLatencyData] = useState<LatencyHistogramResponse | null>(null);
	const [providerCostData, setProviderCostData] = useState<ProviderCostHistogramResponse | null>(null);
	const [providerTokenData, setProviderTokenData] = useState<ProviderTokenHistogramResponse | null>(null);
	const [providerLatencyData, setProviderLatencyData] = useState<ProviderLatencyHistogramResponse | null>(null);

	// Data states - MCP
	const [mcpHistogramData, setMcpHistogramData] = useState<MCPHistogramResponse | null>(null);
	const [mcpCostData, setMcpCostData] = useState<MCPCostHistogramResponse | null>(null);
	const [mcpTopToolsData, setMcpTopToolsData] = useState<MCPTopToolsResponse | null>(null);

	// Data states - Rankings
	const [rankingsData, setRankingsData] = useState<ModelRankingsResponse | null>(null);

	// Loading states - Overview
	const [loadingHistogram, setLoadingHistogram] = useState(true);
	const [loadingTokens, setLoadingTokens] = useState(true);
	const [loadingCost, setLoadingCost] = useState(true);
	const [loadingModels, setLoadingModels] = useState(true);
	const [loadingLatency, setLoadingLatency] = useState(true);
	const [loadingProviderCost, setLoadingProviderCost] = useState(true);
	const [loadingProviderTokens, setLoadingProviderTokens] = useState(true);
	const [loadingProviderLatency, setLoadingProviderLatency] = useState(true);

	// Loading states - MCP
	const [loadingMcpHistogram, setLoadingMcpHistogram] = useState(true);
	const [loadingMcpCost, setLoadingMcpCost] = useState(true);
	const [loadingMcpTopTools, setLoadingMcpTopTools] = useState(true);

	// Loading states - Rankings
	const [loadingRankings, setLoadingRankings] = useState(true);

	// RTK Query lazy hooks - Overview
	const [triggerHistogram] = useLazyGetLogsHistogramQuery({});
	const [triggerTokens] = useLazyGetLogsTokenHistogramQuery();
	const [triggerCost] = useLazyGetLogsCostHistogramQuery();
	const [triggerModels] = useLazyGetLogsModelHistogramQuery();
	const [triggerLatency] = useLazyGetLogsLatencyHistogramQuery();
	const [triggerProviderCost] = useLazyGetLogsProviderCostHistogramQuery();
	const [triggerProviderTokens] = useLazyGetLogsProviderTokenHistogramQuery();
	const [triggerProviderLatency] = useLazyGetLogsProviderLatencyHistogramQuery();

	// RTK Query lazy hooks - MCP
	const [triggerMcpHistogram] = useLazyGetMCPHistogramQuery();
	const [triggerMcpCost] = useLazyGetMCPCostHistogramQuery();
	const [triggerMcpTopTools] = useLazyGetMCPTopToolsQuery();

	// RTK Query lazy hooks - Rankings
	const [triggerRankings] = useLazyGetModelRankingsQuery();

	// MCP filter data
	const { data: mcpFilterData } = useGetMCPAvailableFilterDataQuery();

	// URL state management
	const [urlState, setUrlState] = useQueryStates(
		{
			start_time: parseAsInteger.withDefault(DEFAULT_START_TIME),
			end_time: parseAsInteger.withDefault(DEFAULT_END_TIME),
			period: parseAsString.withDefault("24h"),
			tab: parseAsString.withDefault("overview"),
			virtual_key_ids: parseAsString.withDefault(""),
			providers: parseAsString.withDefault(""),
			models: parseAsString.withDefault(""),
			selected_key_ids: parseAsString.withDefault(""),
			objects: parseAsString.withDefault(""),
			status: parseAsString.withDefault(""),
			routing_rule_ids: parseAsString.withDefault(""),
			routing_engine_used: parseAsString.withDefault(""),
			missing_cost_only: parseAsString.withDefault("false"),
			volume_chart: parseAsString.withDefault("bar"),
			token_chart: parseAsString.withDefault("bar"),
			cost_chart: parseAsString.withDefault("bar"),
			model_chart: parseAsString.withDefault("bar"),
			latency_chart: parseAsString.withDefault("bar"),
			cost_model: parseAsString.withDefault("all"),
			usage_model: parseAsString.withDefault("all"),
			provider_cost_chart: parseAsString.withDefault("bar"),
			provider_token_chart: parseAsString.withDefault("bar"),
			provider_latency_chart: parseAsString.withDefault("bar"),
			provider_cost_provider: parseAsString.withDefault("all"),
			provider_token_provider: parseAsString.withDefault("all"),
			provider_latency_provider: parseAsString.withDefault("all"),
			mcp_volume_chart: parseAsString.withDefault("bar"),
			mcp_cost_chart: parseAsString.withDefault("bar"),
			mcp_tool_names: parseAsString.withDefault(""),
			mcp_server_labels: parseAsString.withDefault(""),
		},
		{
			history: "push",
			shallow: false,
		},
	);

	// Parse filter arrays from URL state
	const selectedProviders = useMemo(() => parseCsvParam(urlState.providers), [urlState.providers]);
	const selectedModels = useMemo(() => parseCsvParam(urlState.models), [urlState.models]);
	const selectedKeyIds = useMemo(() => parseCsvParam(urlState.selected_key_ids), [urlState.selected_key_ids]);
	const selectedVirtualKeyIds = useMemo(() => parseCsvParam(urlState.virtual_key_ids), [urlState.virtual_key_ids]);
	const selectedTypes = useMemo(() => parseCsvParam(urlState.objects), [urlState.objects]);
	const selectedStatuses = useMemo(() => parseCsvParam(urlState.status), [urlState.status]);
	const selectedRoutingRuleIds = useMemo(() => parseCsvParam(urlState.routing_rule_ids), [urlState.routing_rule_ids]);
	const selectedRoutingEngines = useMemo(() => parseCsvParam(urlState.routing_engine_used), [urlState.routing_engine_used]);
	const missingCostOnly = useMemo(() => urlState.missing_cost_only === "true", [urlState.missing_cost_only]);

	// MCP filter arrays
	const selectedMcpToolNames = useMemo(() => parseCsvParam(urlState.mcp_tool_names), [urlState.mcp_tool_names]);
	const selectedMcpServerLabels = useMemo(() => parseCsvParam(urlState.mcp_server_labels), [urlState.mcp_server_labels]);

	// Derived filter for API calls
	const filters: LogFilters = useMemo(
		() => ({
			start_time: dateUtils.toISOString(urlState.start_time),
			end_time: dateUtils.toISOString(urlState.end_time),
			...(selectedProviders.length > 0 && { providers: selectedProviders }),
			...(selectedModels.length > 0 && { models: selectedModels }),
			...(selectedKeyIds.length > 0 && { selected_key_ids: selectedKeyIds }),
			...(selectedVirtualKeyIds.length > 0 && { virtual_key_ids: selectedVirtualKeyIds }),
			...(selectedTypes.length > 0 && { objects: selectedTypes }),
			...(selectedStatuses.length > 0 && { status: selectedStatuses }),
			...(selectedRoutingRuleIds.length > 0 && { routing_rule_ids: selectedRoutingRuleIds }),
			...(selectedRoutingEngines.length > 0 && { routing_engine_used: selectedRoutingEngines }),
			...(missingCostOnly && { missing_cost_only: true }),
		}),
		[
			urlState.start_time,
			urlState.end_time,
			selectedProviders,
			selectedModels,
			selectedKeyIds,
			selectedVirtualKeyIds,
			selectedTypes,
			selectedStatuses,
			selectedRoutingRuleIds,
			selectedRoutingEngines,
			missingCostOnly,
		],
	);

	// MCP filters
	const mcpFilters: MCPToolLogFilters = useMemo(
		() => ({
			start_time: dateUtils.toISOString(urlState.start_time),
			end_time: dateUtils.toISOString(urlState.end_time),
			...(selectedMcpToolNames.length > 0 && { tool_names: selectedMcpToolNames }),
			...(selectedMcpServerLabels.length > 0 && { server_labels: selectedMcpServerLabels }),
		}),
		[urlState.start_time, urlState.end_time, selectedMcpToolNames, selectedMcpServerLabels],
	);

	// Date range for picker
	const dateRange = useMemo(
		() => ({
			from: dateUtils.fromUnixTimestamp(urlState.start_time),
			to: dateUtils.fromUnixTimestamp(urlState.end_time),
		}),
		[urlState.start_time, urlState.end_time],
	);

	// Model lists for each chart's legend (must match what the chart component actually renders)
	const costModels = useMemo(() => sanitizeSeriesLabels(costData?.models), [costData?.models]);
	const usageModels = useMemo(() => sanitizeSeriesLabels(modelData?.models), [modelData?.models]);

	// Available models for filter dropdowns (union of both sources)
	const availableModels = useMemo(() => {
		return sanitizeSeriesLabels([...(costData?.models ?? []), ...(modelData?.models ?? [])]);
	}, [costData?.models, modelData?.models]);

	// Available providers for provider chart filter dropdowns
	const availableProviders = useMemo(() => {
		return sanitizeSeriesLabels([
			...(providerCostData?.providers ?? []),
			...(providerTokenData?.providers ?? []),
			...(providerLatencyData?.providers ?? []),
		]);
	}, [providerCostData?.providers, providerTokenData?.providers, providerLatencyData?.providers]);

	// Provider lists for each chart's legend
	const providerCostProviders = useMemo(() => sanitizeSeriesLabels(providerCostData?.providers), [providerCostData?.providers]);
	const providerTokenProviders = useMemo(() => sanitizeSeriesLabels(providerTokenData?.providers), [providerTokenData?.providers]);
	const providerLatencyProviders = useMemo(() => sanitizeSeriesLabels(providerLatencyData?.providers), [providerLatencyData?.providers]);

	// Fetch Overview tab data (5 calls)
	const fetchOverviewData = useCallback(async () => {
		setLoadingHistogram(true);
		setLoadingTokens(true);
		setLoadingCost(true);
		setLoadingModels(true);
		setLoadingLatency(true);

		const fetchFilters = { filters };

		const [
			histogramResult,
			tokenResult,
			costResult,
			modelResult,
			latencyResult,
		] = await Promise.all([
			triggerHistogram(fetchFilters, false),
			triggerTokens(fetchFilters, false),
			triggerCost(fetchFilters, false),
			triggerModels(fetchFilters, false),
			triggerLatency(fetchFilters, false),
		]);

		setHistogramData(histogramResult.data ?? null);
		setLoadingHistogram(false);
		setTokenData(tokenResult.data ?? null);
		setLoadingTokens(false);
		setCostData(costResult.data ?? null);
		setLoadingCost(false);
		setModelData(modelResult.data ?? null);
		setLoadingModels(false);
		setLatencyData(latencyResult.data ?? null);
		setLoadingLatency(false);
	}, [
		filters,
		triggerHistogram,
		triggerTokens,
		triggerCost,
		triggerModels,
		triggerLatency,
	]);

	// Fetch Provider Usage tab data (3 calls)
	const fetchProviderData = useCallback(async () => {
		setLoadingProviderCost(true);
		setLoadingProviderTokens(true);
		setLoadingProviderLatency(true);

		const fetchFilters = { filters };

		const [
			providerCostResult,
			providerTokenResult,
			providerLatencyResult,
		] = await Promise.all([
			triggerProviderCost(fetchFilters, false),
			triggerProviderTokens(fetchFilters, false),
			triggerProviderLatency(fetchFilters, false),
		]);

		setProviderCostData(providerCostResult.data ?? null);
		setLoadingProviderCost(false);
		setProviderTokenData(providerTokenResult.data ?? null);
		setLoadingProviderTokens(false);
		setProviderLatencyData(providerLatencyResult.data ?? null);
		setLoadingProviderLatency(false);
	}, [
		filters,
		triggerProviderCost,
		triggerProviderTokens,
		triggerProviderLatency,
	]);

	// Fetch MCP data
	const fetchMcpData = useCallback(async () => {
		setLoadingMcpHistogram(true);
		setLoadingMcpCost(true);
		setLoadingMcpTopTools(true);

		const fetchFilters = { filters: mcpFilters };

		const [mcpHistResult, mcpCostResult, mcpTopToolsResult] = await Promise.all([
			triggerMcpHistogram(fetchFilters, false),
			triggerMcpCost(fetchFilters, false),
			triggerMcpTopTools(fetchFilters, false),
		]);

		setMcpHistogramData(mcpHistResult.data ?? null);
		setLoadingMcpHistogram(false);
		setMcpCostData(mcpCostResult.data ?? null);
		setLoadingMcpCost(false);
		setMcpTopToolsData(mcpTopToolsResult.data ?? null);
		setLoadingMcpTopTools(false);
	}, [mcpFilters, triggerMcpHistogram, triggerMcpCost, triggerMcpTopTools]);

	// Fetch Rankings data
	const fetchRankingsData = useCallback(async () => {
		setLoadingRankings(true);
		const result = await triggerRankings({ filters }, false);
		setRankingsData(result.data ?? null);
		setLoadingRankings(false);
	}, [filters, triggerRankings]);

	// --- Lazy-load refs: each tab fetches only once per filter change ---
	const overviewFetchedRef = useRef(false);
	const overviewLoadingRef = useRef(false);
	const overviewGenRef = useRef(0);

	const providerFetchedRef = useRef(false);
	const providerLoadingRef = useRef(false);
	const providerGenRef = useRef(0);

	const mcpFetchedRef = useRef(false);
	const mcpLoadingRef = useRef(false);
	const mcpGenRef = useRef(0);

	const rankingsFetchedRef = useRef(false);
	const rankingsLoadingRef = useRef(false);
	const rankingsGenRef = useRef(0);

	const ensureOverviewDataLoaded = useCallback(async () => {
		if (overviewFetchedRef.current || overviewLoadingRef.current) return;
		const gen = overviewGenRef.current;
		overviewLoadingRef.current = true;
		try {
			await fetchOverviewData();
			if (gen === overviewGenRef.current) overviewFetchedRef.current = true;
		} finally {
			if (gen === overviewGenRef.current) overviewLoadingRef.current = false;
		}
	}, [fetchOverviewData]);

	const ensureProviderDataLoaded = useCallback(async () => {
		if (providerFetchedRef.current || providerLoadingRef.current) return;
		const gen = providerGenRef.current;
		providerLoadingRef.current = true;
		try {
			await fetchProviderData();
			if (gen === providerGenRef.current) providerFetchedRef.current = true;
		} finally {
			if (gen === providerGenRef.current) providerLoadingRef.current = false;
		}
	}, [fetchProviderData]);

	const ensureMcpDataLoaded = useCallback(async () => {
		if (mcpFetchedRef.current || mcpLoadingRef.current) return;
		const gen = mcpGenRef.current;
		mcpLoadingRef.current = true;
		try {
			await fetchMcpData();
			if (gen === mcpGenRef.current) mcpFetchedRef.current = true;
		} finally {
			if (gen === mcpGenRef.current) mcpLoadingRef.current = false;
		}
	}, [fetchMcpData]);

	const ensureRankingsDataLoaded = useCallback(async () => {
		if (rankingsFetchedRef.current || rankingsLoadingRef.current) return;
		const gen = rankingsGenRef.current;
		rankingsLoadingRef.current = true;
		try {
			await fetchRankingsData();
			if (gen === rankingsGenRef.current) rankingsFetchedRef.current = true;
		} finally {
			if (gen === rankingsGenRef.current) rankingsLoadingRef.current = false;
		}
	}, [fetchRankingsData]);

	// Reset all lazy-load flags when filters change (not on tab switch)
	useEffect(() => {
		overviewFetchedRef.current = false;
		overviewLoadingRef.current = false;
		overviewGenRef.current += 1;
		providerFetchedRef.current = false;
		providerLoadingRef.current = false;
		providerGenRef.current += 1;
		rankingsFetchedRef.current = false;
		rankingsLoadingRef.current = false;
		rankingsGenRef.current += 1;
	}, [filters]);

	useEffect(() => {
		mcpFetchedRef.current = false;
		mcpLoadingRef.current = false;
		mcpGenRef.current += 1;
	}, [mcpFilters]);

	// Fetch current tab's data when filters change or tab switches
	// The ensure* functions are no-ops if data is already loaded for the current filters
	useEffect(() => {
		const tab = urlState.tab || "overview";
		if (tab === "overview") void ensureOverviewDataLoaded();
		else if (tab === "provider-usage") void ensureProviderDataLoaded();
		else if (tab === "rankings") void ensureRankingsDataLoaded();
		else if (tab === "mcp") void ensureMcpDataLoaded();
	}, [urlState.tab, ensureOverviewDataLoaded, ensureProviderDataLoaded, ensureRankingsDataLoaded, ensureMcpDataLoaded]);

	// Warm other tabs in the background after 150ms
	useEffect(() => {
		const tab = urlState.tab || "overview";
		const timeoutId = window.setTimeout(() => {
			if (tab !== "overview") void ensureOverviewDataLoaded();
			if (tab !== "provider-usage") void ensureProviderDataLoaded();
			if (tab !== "mcp") void ensureMcpDataLoaded();
			if (tab !== "rankings") void ensureRankingsDataLoaded();
		}, 150);
		return () => window.clearTimeout(timeoutId);
	}, [urlState.tab, ensureOverviewDataLoaded, ensureProviderDataLoaded, ensureMcpDataLoaded, ensureRankingsDataLoaded]);

	// Handle time period change
	const handlePeriodChange = useCallback(
		(period: string | undefined) => {
			if (!period) return;
			const { start, end } = getTimeRangeFromPeriod(period);
			setUrlState({
				start_time: start,
				end_time: end,
				period,
			});
		},
		[setUrlState],
	);

	// Handle custom date range change
	const handleDateRangeChange = useCallback(
		(range: { from?: Date; to?: Date }) => {
			if (!range.from || !range.to) return;
			setUrlState({
				start_time: dateUtils.toUnixTimestamp(range.from),
				end_time: dateUtils.toUnixTimestamp(range.to),
				period: "", // Clear period when custom range is selected
			});
		},
		[setUrlState],
	);

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

	// Filter change handler for FilterPopover
	const handleFilterChange = useCallback(
		(key: keyof LogFilters, values: string[] | boolean | string) => {
			const urlKeyMap: Partial<Record<keyof LogFilters, string>> = {
				providers: "providers",
				models: "models",
				selected_key_ids: "selected_key_ids",
				virtual_key_ids: "virtual_key_ids",
				objects: "objects",
				status: "status",
				routing_rule_ids: "routing_rule_ids",
				routing_engine_used: "routing_engine_used",
				missing_cost_only: "missing_cost_only",
			};
			const urlKey = urlKeyMap[key];
			if (!urlKey) return;
			if (typeof values === "boolean") {
				setUrlState({ [urlKey]: String(values) });
			} else if (typeof values === "string") {
				setUrlState({ [urlKey]: values });
			} else {
				setUrlState({ [urlKey]: values.join(",") });
			}
		},
		[setUrlState],
	);

	const handleProviderCostChartToggle = useCallback((type: ChartType) => setUrlState({ provider_cost_chart: type }), [setUrlState]);
	const handleProviderTokenChartToggle = useCallback((type: ChartType) => setUrlState({ provider_token_chart: type }), [setUrlState]);
	const handleProviderLatencyChartToggle = useCallback((type: ChartType) => setUrlState({ provider_latency_chart: type }), [setUrlState]);

	// MCP chart type toggles
	const handleMcpVolumeChartToggle = useCallback((type: ChartType) => setUrlState({ mcp_volume_chart: type }), [setUrlState]);
	const handleMcpCostChartToggle = useCallback((type: ChartType) => setUrlState({ mcp_cost_chart: type }), [setUrlState]);

	// Model filter changes
	const handleCostModelChange = useCallback((model: string) => setUrlState({ cost_model: model }), [setUrlState]);
	const handleUsageModelChange = useCallback((model: string) => setUrlState({ usage_model: model }), [setUrlState]);

	// Provider filter changes
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

	// MCP filter change handlers
	const handleMcpToolNameChange = useCallback(
		(toolName: string) => {
			const current = parseCsvParam(urlState.mcp_tool_names);
			const updated = current.includes(toolName) ? current.filter((t) => t !== toolName) : [...current, toolName];
			setUrlState({ mcp_tool_names: updated.join(",") });
		},
		[urlState.mcp_tool_names, setUrlState],
	);

	const handleMcpServerLabelChange = useCallback(
		(label: string) => {
			const current = parseCsvParam(urlState.mcp_server_labels);
			const updated = current.includes(label) ? current.filter((l) => l !== label) : [...current, label];
			setUrlState({ mcp_server_labels: updated.join(",") });
		},
		[urlState.mcp_server_labels, setUrlState],
	);

	return (
		<div className="mx-auto flex h-full min-h-[calc(100vh-100px)] w-full flex-col gap-4">
			{/* Header with time filter */}
			<div className="flex items-center justify-between">
				<div className="flex items-center gap-2">
					<h1 className="text-lg font-semibold">Dashboard</h1>
				</div>
				<div className="flex items-center gap-2">
					{(urlState.tab === "overview" || urlState.tab === "provider-usage" || urlState.tab === "rankings") && (
						<FilterPopover filters={filters} onFilterChange={handleFilterChange} showParentRequestIdFilter={false} />
					)}
					{urlState.tab === "mcp" && mcpFilterData && (
						<div className="flex items-center gap-1">
							{mcpFilterData.tool_names?.length > 0 && (
								<ModelFilterSelect
									models={mcpFilterData.tool_names}
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
							{mcpFilterData.server_labels?.length > 0 && (
								<ModelFilterSelect
									models={mcpFilterData.server_labels}
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
					/>
				</div>
			</div>

			{/* Tabs */}
			<Tabs value={urlState.tab} onValueChange={handleTabChange}>
				<TabsList className="mb-2">
					<TabsTrigger value="overview" data-testid="dashboard-tab-overview">
						Overview
					</TabsTrigger>
					<TabsTrigger value="provider-usage" data-testid="dashboard-tab-provider-usage">
						Provider Usage
					</TabsTrigger>
					<TabsTrigger value="rankings" data-testid="dashboard-tab-rankings">
						Model Rankings
					</TabsTrigger>
					<TabsTrigger value="mcp" data-testid="dashboard-tab-mcp">
						MCP usage
					</TabsTrigger>
				</TabsList>

				{/* Overview Tab */}
				<TabsContent value="overview">
					<OverviewTab
						histogramData={histogramData}
						tokenData={tokenData}
						costData={costData}
						modelData={modelData}
						latencyData={latencyData}
						loadingHistogram={loadingHistogram}
						loadingTokens={loadingTokens}
						loadingCost={loadingCost}
						loadingModels={loadingModels}
						loadingLatency={loadingLatency}
						startTime={urlState.start_time}
						endTime={urlState.end_time}
						volumeChartType={toChartType(urlState.volume_chart)}
						tokenChartType={toChartType(urlState.token_chart)}
						costChartType={toChartType(urlState.cost_chart)}
						modelChartType={toChartType(urlState.model_chart)}
						latencyChartType={toChartType(urlState.latency_chart)}
						costModel={urlState.cost_model}
						usageModel={urlState.usage_model}
						costModels={costModels}
						usageModels={usageModels}
						availableModels={availableModels}
						onVolumeChartToggle={handleVolumeChartToggle}
						onTokenChartToggle={handleTokenChartToggle}
						onCostChartToggle={handleCostChartToggle}
						onModelChartToggle={handleModelChartToggle}
						onLatencyChartToggle={handleLatencyChartToggle}
						onCostModelChange={handleCostModelChange}
						onUsageModelChange={handleUsageModelChange}
					/>
				</TabsContent>

				{/* Provider Usage Tab */}
				<TabsContent value="provider-usage">
					<ProviderUsageTab
						providerCostData={providerCostData}
						providerTokenData={providerTokenData}
						providerLatencyData={providerLatencyData}
						loadingProviderCost={loadingProviderCost}
						loadingProviderTokens={loadingProviderTokens}
						loadingProviderLatency={loadingProviderLatency}
						startTime={urlState.start_time}
						endTime={urlState.end_time}
						providerCostChartType={toChartType(urlState.provider_cost_chart)}
						providerTokenChartType={toChartType(urlState.provider_token_chart)}
						providerLatencyChartType={toChartType(urlState.provider_latency_chart)}
						providerCostProvider={urlState.provider_cost_provider}
						providerTokenProvider={urlState.provider_token_provider}
						providerLatencyProvider={urlState.provider_latency_provider}
						availableProviders={availableProviders}
						providerCostProviders={providerCostProviders}
						providerTokenProviders={providerTokenProviders}
						providerLatencyProviders={providerLatencyProviders}
						onProviderCostChartToggle={handleProviderCostChartToggle}
						onProviderTokenChartToggle={handleProviderTokenChartToggle}
						onProviderLatencyChartToggle={handleProviderLatencyChartToggle}
						onProviderCostProviderChange={handleProviderCostProviderChange}
						onProviderTokenProviderChange={handleProviderTokenProviderChange}
						onProviderLatencyProviderChange={handleProviderLatencyProviderChange}
					/>
				</TabsContent>

				{/* Model Rankings Tab */}
				<TabsContent value="rankings">
					<ModelRankingsTab
						rankingsData={rankingsData}
						loading={loadingRankings}
						modelData={modelData}
						loadingModels={loadingModels}
						startTime={urlState.start_time}
						endTime={urlState.end_time}
					/>
				</TabsContent>

				{/* MCP Tab */}
				<TabsContent value="mcp">
					<MCPTab
						mcpHistogramData={mcpHistogramData}
						mcpCostData={mcpCostData}
						mcpTopToolsData={mcpTopToolsData}
						loadingMcpHistogram={loadingMcpHistogram}
						loadingMcpCost={loadingMcpCost}
						loadingMcpTopTools={loadingMcpTopTools}
						startTime={urlState.start_time}
						endTime={urlState.end_time}
						mcpVolumeChartType={toChartType(urlState.mcp_volume_chart)}
						mcpCostChartType={toChartType(urlState.mcp_cost_chart)}
						onMcpVolumeChartToggle={handleMcpVolumeChartToggle}
						onMcpCostChartToggle={handleMcpCostChartToggle}
					/>
				</TabsContent>
			</Tabs>
		</div>
	);
}
