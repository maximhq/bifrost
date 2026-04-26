import { LogsFilterSidebar } from "@/components/filters/logsFilterSidebar";
import { DateTimePickerWithRange } from "@/components/ui/datePickerWithRange";
import { ScrollArea } from "@/components/ui/scrollArea";
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
import { getRangeForPeriod, TIME_PERIODS } from "@/lib/utils/timeRange";
import UserRankingsTab from "@enterprise/components/user-rankings/userRankingsTab";
import { useLocation } from "@tanstack/react-router";
import { parseAsInteger, parseAsString, useQueryStates } from "nuqs";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { type ChartType } from "./components/charts/chartTypeToggle";
import { ModelFilterSelect } from "./components/charts/modelFilterSelect";
import { ExportPopover } from "./components/exportPopover";
import { MCPTab } from "./components/mcpTab";
import { ModelRankingsTab } from "./components/modelRankingsTab";
import { OverviewTab } from "./components/overviewTab";
import { ProviderUsageTab } from "./components/providerUsageTab";

// Type-safe parser for chart type URL state
const toChartType = (value: string): ChartType => (value === "line" ? "line" : "bar");

const parseCsvParam = (value: string): string[] => (value ? value.split(",").filter(Boolean) : []);
const sanitizeSeriesLabels = (values?: string[]): string[] => {
	if (!values) return [];
	const trimmedValues = values.map((value) => value.trim()).filter((value) => value.length > 0);

	return [...new Set(trimmedValues)];
};

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

	// Memoize default time range to prevent recalculation on every render
	// This is crucial to avoid triggering refetches when the sheet opens/closes
	const defaultTimeRange = useMemo(() => dateUtils.getDefaultTimeRange(), []);

	const { search } = useLocation();
	const hasExplicitTimeRange = (search as Record<string, unknown>)?.start_time && (search as Record<string, unknown>)?.end_time;
	// URL state management
	const [urlState, setUrlState] = useQueryStates(
		{
			period: parseAsString.withDefault(hasExplicitTimeRange ? "" : "1h").withOptions({ clearOnDefault: false }),
			start_time: parseAsInteger.withDefault(defaultTimeRange.startTime),
			end_time: parseAsInteger.withDefault(defaultTimeRange.endTime),
			tab: parseAsString.withDefault("overview"),
			virtual_key_ids: parseAsString.withDefault(""),
			providers: parseAsString.withDefault(""),
			models: parseAsString.withDefault(""),
			selected_key_ids: parseAsString.withDefault(""),
			objects: parseAsString.withDefault(""),
			status: parseAsString.withDefault(""),
			routing_rule_ids: parseAsString.withDefault(""),
			routing_engine_used: parseAsString.withDefault(""),
			stop_reasons: parseAsString.withDefault(""),
			missing_cost_only: parseAsString.withDefault("false"),
			metadata_filters: parseAsString.withDefault(""),
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
	const selectedStopReasons = useMemo(() => parseCsvParam(urlState.stop_reasons), [urlState.stop_reasons]);
	const missingCostOnly = useMemo(() => urlState.missing_cost_only === "true", [urlState.missing_cost_only]);
	const metadataFilters = useMemo(() => {
		if (!urlState.metadata_filters) return undefined;
		try {
			return JSON.parse(urlState.metadata_filters) as Record<string, string>;
		} catch {
			return undefined;
		}
	}, [urlState.metadata_filters]);

	// MCP filter arrays
	const selectedMcpToolNames = useMemo(() => parseCsvParam(urlState.mcp_tool_names), [urlState.mcp_tool_names]);
	const selectedMcpServerLabels = useMemo(() => parseCsvParam(urlState.mcp_server_labels), [urlState.mcp_server_labels]);

	// Derived filter for API calls.
	// When period is set, send it so the backend computes the window fresh on every request.
	// For custom absolute ranges (period === "") use the stored URL timestamps.
	const filters: LogFilters = useMemo(
		() => ({
			...(urlState.period
				? { period: urlState.period }
				: {
					start_time: dateUtils.toISOString(urlState.start_time),
					end_time: dateUtils.toISOString(urlState.end_time),
				}),
			...(selectedProviders.length > 0 && { providers: selectedProviders }),
			...(selectedModels.length > 0 && { models: selectedModels }),
			...(selectedKeyIds.length > 0 && { selected_key_ids: selectedKeyIds }),
			...(selectedVirtualKeyIds.length > 0 && {
				virtual_key_ids: selectedVirtualKeyIds,
			}),
			...(selectedTypes.length > 0 && { objects: selectedTypes }),
			...(selectedStatuses.length > 0 && { status: selectedStatuses }),
			...(selectedRoutingRuleIds.length > 0 && {
				routing_rule_ids: selectedRoutingRuleIds,
			}),
			...(selectedRoutingEngines.length > 0 && {
				routing_engine_used: selectedRoutingEngines,
			}),
			...(selectedStopReasons.length > 0 && { stop_reasons: selectedStopReasons }),
			...(missingCostOnly && { missing_cost_only: true }),
			...(metadataFilters &&
				Object.keys(metadataFilters).length > 0 && {
				metadata_filters: metadataFilters,
			}),
		}),
		[
			urlState.period,
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
			selectedStopReasons,
			missingCostOnly,
			metadataFilters,
		],
	);

	// MCP filters — same period-first logic as filters above.
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
		}),
		[urlState.period, urlState.start_time, urlState.end_time, selectedMcpToolNames, selectedMcpServerLabels],
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

		const [histogramResult, tokenResult, costResult, modelResult, latencyResult] = await Promise.all([
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
	}, [filters, triggerHistogram, triggerTokens, triggerCost, triggerModels, triggerLatency]);

	// Fetch Provider Usage tab data (3 calls)
	const fetchProviderData = useCallback(async () => {
		setLoadingProviderCost(true);
		setLoadingProviderTokens(true);
		setLoadingProviderLatency(true);

		const fetchFilters = { filters };

		const [providerCostResult, providerTokenResult, providerLatencyResult] = await Promise.all([
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
	}, [filters, triggerProviderCost, triggerProviderTokens, triggerProviderLatency]);

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
	const overviewPromiseRef = useRef<Promise<void> | null>(null);

	const providerFetchedRef = useRef(false);
	const providerLoadingRef = useRef(false);
	const providerGenRef = useRef(0);
	const providerPromiseRef = useRef<Promise<void> | null>(null);

	const mcpFetchedRef = useRef(false);
	const mcpLoadingRef = useRef(false);
	const mcpGenRef = useRef(0);
	const mcpPromiseRef = useRef<Promise<void> | null>(null);

	const rankingsFetchedRef = useRef(false);
	const rankingsLoadingRef = useRef(false);
	const rankingsGenRef = useRef(0);
	const rankingsPromiseRef = useRef<Promise<void> | null>(null);

	const ensureOverviewDataLoaded = useCallback(async () => {
		if (overviewFetchedRef.current) return;
		if (overviewLoadingRef.current) return overviewPromiseRef.current ?? undefined;
		const gen = overviewGenRef.current;
		overviewLoadingRef.current = true;
		const promise = fetchOverviewData()
			.then(() => {
				if (gen === overviewGenRef.current) overviewFetchedRef.current = true;
			})
			.finally(() => {
				if (gen === overviewGenRef.current) {
					overviewLoadingRef.current = false;
					overviewPromiseRef.current = null;
				}
			});
		overviewPromiseRef.current = promise;
		return promise;
	}, [fetchOverviewData]);

	const ensureProviderDataLoaded = useCallback(async () => {
		if (providerFetchedRef.current) return;
		if (providerLoadingRef.current) return providerPromiseRef.current ?? undefined;
		const gen = providerGenRef.current;
		providerLoadingRef.current = true;
		const promise = fetchProviderData()
			.then(() => {
				if (gen === providerGenRef.current) providerFetchedRef.current = true;
			})
			.finally(() => {
				if (gen === providerGenRef.current) {
					providerLoadingRef.current = false;
					providerPromiseRef.current = null;
				}
			});
		providerPromiseRef.current = promise;
		return promise;
	}, [fetchProviderData]);

	const ensureMcpDataLoaded = useCallback(async () => {
		if (mcpFetchedRef.current) return;
		if (mcpLoadingRef.current) return mcpPromiseRef.current ?? undefined;
		const gen = mcpGenRef.current;
		mcpLoadingRef.current = true;
		const promise = fetchMcpData()
			.then(() => {
				if (gen === mcpGenRef.current) mcpFetchedRef.current = true;
			})
			.finally(() => {
				if (gen === mcpGenRef.current) {
					mcpLoadingRef.current = false;
					mcpPromiseRef.current = null;
				}
			});
		mcpPromiseRef.current = promise;
		return promise;
	}, [fetchMcpData]);

	const ensureRankingsDataLoaded = useCallback(async () => {
		if (rankingsFetchedRef.current) return;
		if (rankingsLoadingRef.current) return rankingsPromiseRef.current ?? undefined;
		const gen = rankingsGenRef.current;
		rankingsLoadingRef.current = true;
		const promise = fetchRankingsData()
			.then(() => {
				if (gen === rankingsGenRef.current) rankingsFetchedRef.current = true;
			})
			.finally(() => {
				if (gen === rankingsGenRef.current) {
					rankingsLoadingRef.current = false;
					rankingsPromiseRef.current = null;
				}
			});
		rankingsPromiseRef.current = promise;
		return promise;
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

	// Adapter: converts a full LogFilters object to dashboard's CSV-based URL state
	const setFilters = useCallback(
		(newFilters: LogFilters) => {
			const newStartTime = newFilters.start_time ? dateUtils.toUnixTimestamp(new Date(newFilters.start_time)) : undefined;
			const newEndTime = newFilters.end_time ? dateUtils.toUnixTimestamp(new Date(newFilters.end_time)) : undefined;
			const timeChanged = newStartTime !== urlState.start_time || newEndTime !== urlState.end_time;
			setUrlState({
				...(timeChanged && { period: "" }),
				start_time: newStartTime,
				end_time: newEndTime,
				period: urlState.period,
				providers: (newFilters.providers || []).join(","),
				models: (newFilters.models || []).join(","),
				selected_key_ids: (newFilters.selected_key_ids || []).join(","),
				virtual_key_ids: (newFilters.virtual_key_ids || []).join(","),
				objects: (newFilters.objects || []).join(","),
				status: (newFilters.status || []).join(","),
				routing_rule_ids: (newFilters.routing_rule_ids || []).join(","),
				routing_engine_used: (newFilters.routing_engine_used || []).join(","),
				stop_reasons: (newFilters.stop_reasons || []).join(","),
				missing_cost_only: String(newFilters.missing_cost_only ?? false),
				metadata_filters:
					newFilters.metadata_filters && Object.keys(newFilters.metadata_filters).length > 0
						? JSON.stringify(newFilters.metadata_filters)
						: "",
			});
		},
		[setUrlState, urlState.start_time, urlState.end_time, urlState.period],
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

	// Aggregate data object for export
	const dashboardData = useMemo(
		() => ({
			histogramData,
			tokenData,
			costData,
			modelData,
			latencyData,
			providerCostData,
			providerTokenData,
			providerLatencyData,
			rankingsData,
			mcpHistogramData,
			mcpCostData,
			mcpTopToolsData,
		}),
		[
			histogramData,
			tokenData,
			costData,
			modelData,
			latencyData,
			providerCostData,
			providerTokenData,
			providerLatencyData,
			rankingsData,
			mcpHistogramData,
			mcpCostData,
			mcpTopToolsData,
		],
	);

	// Keep a ref in sync so export callbacks always read the latest data
	const dashboardDataRef = useRef(dashboardData);
	dashboardDataRef.current = dashboardData;
	const getDashboardData = useCallback(() => dashboardDataRef.current, []);

	// Preload all tab data (used by CSV and PDF export)
	const handlePreloadData = useCallback(async () => {
		await Promise.all([ensureOverviewDataLoaded(), ensureProviderDataLoaded(), ensureRankingsDataLoaded(), ensureMcpDataLoaded()]);
	}, [ensureOverviewDataLoaded, ensureProviderDataLoaded, ensureRankingsDataLoaded, ensureMcpDataLoaded]);

	// PDF export mode — when true, all TabsContent are force-mounted so
	// html2canvas can capture every tab.
	const [pdfMode, setPdfMode] = useState(false);
	const dashboardMinHeightRef = useRef<string>("");
	const hiddenTabsRef = useRef<HTMLElement[]>([]);

	// Called by ExportPopover. Loads all data, force-mounts all tabs,
	// unhides inactive tabs so html2canvas can capture them, then returns
	// the 4 section DOM elements. Caller must invoke the returned cleanup
	// function when done capturing.
	const handlePdfExport = useCallback(async (): Promise<HTMLElement[]> => {
		// Ensure every tab's data is loaded
		await handlePreloadData();

		setPdfMode(true);

		// Wait for React to render the force-mounted tabs
		await new Promise<void>((resolve) => {
			requestAnimationFrame(() => {
				requestAnimationFrame(() => resolve());
			});
		});

		// Radix sets `hidden` on inactive force-mounted TabsContent.
		// Temporarily remove it so html2canvas can capture them.
		const hiddenTabs = document.querySelectorAll<HTMLElement>('[data-slot="tabs-content"][hidden]');
		hiddenTabsRef.current = Array.from(hiddenTabs);
		for (const tab of hiddenTabs) {
			tab.removeAttribute("hidden");
			tab.style.display = "block";
		}

		// Collapse min-height on the dashboard container so captured
		// sections wrap tightly around their content (no extra whitespace).
		const dashboardEl = document.getElementById("dashboard-root");
		if (dashboardEl) {
			dashboardMinHeightRef.current = dashboardEl.style.minHeight;
			dashboardEl.style.minHeight = "0";
		}

		// Let ResizeObserver-based charts (meter gauge) re-measure
		window.dispatchEvent(new Event("resize"));
		await new Promise<void>((resolve) => {
			requestAnimationFrame(() => {
				requestAnimationFrame(() => resolve());
			});
		});

		const ids = ["dashboard-section-overview", "dashboard-section-provider-usage", "dashboard-section-rankings", "dashboard-section-mcp"];
		return ids.map((id) => document.getElementById(id)).filter(Boolean) as HTMLElement[];
	}, [handlePreloadData]);

	// Cleanup after PDF capture is complete
	const handlePdfExportDone = useCallback(() => {
		// Restore minHeight on dashboard container
		const dashboardEl = document.getElementById("dashboard-root");
		if (dashboardEl) {
			dashboardEl.style.minHeight = dashboardMinHeightRef.current;
		}

		// Re-hide tabs that were temporarily shown for capture
		for (const tab of hiddenTabsRef.current) {
			tab.setAttribute("hidden", "");
			tab.style.display = "";
		}
		hiddenTabsRef.current = [];

		setPdfMode(false);
	}, []);

	return (
		<div id="dashboard-root" className="no-padding-parent no-border-parent bg-background flex h-[calc(100vh_-_16px)] w-full gap-3">
			{/* Sidebar Filters */}
			<LogsFilterSidebar filters={filters} onFiltersChange={setFilters} />

			{/* Main Content */}
			<ScrollArea className="bg-card flex min-w-0 flex-1 flex-col gap-4 rounded-l-md">
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

				<div className="p-4">
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
							<TabsTrigger value="user-rankings" data-testid="dashboard-tab-user-rankings">
								User Rankings
							</TabsTrigger>
						</TabsList>

						{/* Overview Tab */}
						<TabsContent value="overview" {...(pdfMode && { forceMount: true })}>
							<div id="dashboard-section-overview">
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
							</div>
						</TabsContent>

						{/* Provider Usage Tab */}
						<TabsContent value="provider-usage" {...(pdfMode && { forceMount: true })}>
							<div id="dashboard-section-provider-usage">
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
							</div>
						</TabsContent>

						{/* Model Rankings Tab */}
						<TabsContent value="rankings" {...(pdfMode && { forceMount: true })}>
							<div id="dashboard-section-rankings">
								<ModelRankingsTab
									rankingsData={rankingsData}
									loading={loadingRankings}
									modelData={modelData}
									loadingModels={loadingModels}
									startTime={urlState.start_time}
									endTime={urlState.end_time}
								/>
							</div>
						</TabsContent>

						{/* MCP Tab */}
						<TabsContent value="mcp" {...(pdfMode && { forceMount: true })}>
							<div id="dashboard-section-mcp">
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
							</div>
						</TabsContent>

						{/* User Rankings Tab (Enterprise) */}
						<TabsContent value="user-rankings">
							<UserRankingsTab />
						</TabsContent>
					</Tabs>
				</div>
			</ScrollArea>
		</div>
	);
}