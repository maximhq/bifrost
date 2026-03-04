"use client";

import { FilterPopover } from "@/components/filters/filterPopover";
import { DateTimePickerWithRange } from "@/components/ui/datePickerWithRange";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import {
	useLazyGetLogsCostHistogramQuery,
	useLazyGetLogsHistogramQuery,
	useLazyGetLogsLatencyHistogramQuery,
	useLazyGetLogsModelHistogramQuery,
	useLazyGetLogsProviderCostHistogramQuery,
	useLazyGetLogsProviderLatencyHistogramQuery,
	useLazyGetLogsProviderTokenHistogramQuery,
	useLazyGetLogsTokenHistogramQuery,
} from "@/lib/store";
import type {
	CostHistogramResponse,
	LatencyHistogramResponse,
	LogFilters,
	LogsHistogramResponse,
	ModelHistogramResponse,
	ProviderCostHistogramResponse,
	ProviderLatencyHistogramResponse,
	ProviderTokenHistogramResponse,
	TokenHistogramResponse,
} from "@/lib/types/logs";
import { dateUtils } from "@/lib/types/logs";
import { parseAsInteger, parseAsString, useQueryStates } from "nuqs";
import { useCallback, useEffect, useMemo, useState } from "react";
import { ChartCard } from "./components/chartCard";
import { type ChartType, ChartTypeToggle } from "./components/chartTypeToggle";
import { CostChart } from "./components/costChart";
import { LatencyChart } from "./components/latencyChart";
import { LogVolumeChart } from "./components/logVolumeChart";
import { ModelFilterSelect } from "./components/modelFilterSelect";
import { ModelUsageChart } from "./components/modelUsageChart";
import { ProviderCostChart } from "./components/providerCostChart";
import { ProviderFilterSelect } from "./components/providerFilterSelect";
import { ProviderLatencyChart } from "./components/providerLatencyChart";
import { ProviderTokenChart } from "./components/providerTokenChart";
import { TokenUsageChart } from "./components/tokenUsageChart";
import { CHART_COLORS, getModelColor, LATENCY_COLORS } from "./utils/chartUtils";

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

const CHART_HEADER_ACTIONS_CLASS = "flex min-w-0 w-full flex-col-reverse gap-2";
const CHART_HEADER_LEGEND_CLASS = "flex min-w-0 flex-wrap items-center gap-2 pl-2 text-xs";
const CHART_HEADER_CONTROLS_CLASS = "flex items-center justify-end gap-2";
const parseCsvParam = (value: string): string[] => (value ? value.split(",").filter(Boolean) : []);
const sanitizeSeriesLabels = (values?: string[]): string[] => {
	if (!values) return [];
	return values
		.map((value) => value.trim())
		.filter((value) => value.length > 0);
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
	// Data states
	const [histogramData, setHistogramData] = useState<LogsHistogramResponse | null>(null);
	const [tokenData, setTokenData] = useState<TokenHistogramResponse | null>(null);
	const [costData, setCostData] = useState<CostHistogramResponse | null>(null);
	const [modelData, setModelData] = useState<ModelHistogramResponse | null>(null);
	const [latencyData, setLatencyData] = useState<LatencyHistogramResponse | null>(null);
	const [providerCostData, setProviderCostData] = useState<ProviderCostHistogramResponse | null>(null);
	const [providerTokenData, setProviderTokenData] = useState<ProviderTokenHistogramResponse | null>(null);
	const [providerLatencyData, setProviderLatencyData] = useState<ProviderLatencyHistogramResponse | null>(null);

	// Loading states
	const [loadingHistogram, setLoadingHistogram] = useState(true);
	const [loadingTokens, setLoadingTokens] = useState(true);
	const [loadingCost, setLoadingCost] = useState(true);
	const [loadingModels, setLoadingModels] = useState(true);
	const [loadingLatency, setLoadingLatency] = useState(true);
	const [loadingProviderCost, setLoadingProviderCost] = useState(true);
	const [loadingProviderTokens, setLoadingProviderTokens] = useState(true);
	const [loadingProviderLatency, setLoadingProviderLatency] = useState(true);

	// RTK Query lazy hooks
	const [triggerHistogram] = useLazyGetLogsHistogramQuery({});
	const [triggerTokens] = useLazyGetLogsTokenHistogramQuery();
	const [triggerCost] = useLazyGetLogsCostHistogramQuery();
	const [triggerModels] = useLazyGetLogsModelHistogramQuery();
	const [triggerLatency] = useLazyGetLogsLatencyHistogramQuery();
	const [triggerProviderCost] = useLazyGetLogsProviderCostHistogramQuery();
	const [triggerProviderTokens] = useLazyGetLogsProviderTokenHistogramQuery();
	const [triggerProviderLatency] = useLazyGetLogsProviderLatencyHistogramQuery();

	// URL state management
	const [urlState, setUrlState] = useQueryStates(
		{
			start_time: parseAsInteger.withDefault(DEFAULT_START_TIME),
			end_time: parseAsInteger.withDefault(DEFAULT_END_TIME),
			period: parseAsString.withDefault("24h"),
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
		if (costData?.models?.length) return sanitizeSeriesLabels(costData.models);
		return sanitizeSeriesLabels(modelData?.models);
	}, [costData?.models, modelData?.models]);

	// Available providers for provider chart filter dropdowns
	const availableProviders = useMemo(() => {
		if (providerCostData?.providers?.length) return sanitizeSeriesLabels(providerCostData.providers);
		if (providerTokenData?.providers?.length) return sanitizeSeriesLabels(providerTokenData.providers);
		return sanitizeSeriesLabels(providerLatencyData?.providers);
	}, [providerCostData?.providers, providerTokenData?.providers, providerLatencyData?.providers]);

	// Provider lists for each chart's legend
	const providerCostProviders = useMemo(() => sanitizeSeriesLabels(providerCostData?.providers), [providerCostData?.providers]);
	const providerTokenProviders = useMemo(() => sanitizeSeriesLabels(providerTokenData?.providers), [providerTokenData?.providers]);
	const providerLatencyProviders = useMemo(() => sanitizeSeriesLabels(providerLatencyData?.providers), [providerLatencyData?.providers]);

	// Fetch all data
	const fetchAllData = useCallback(async () => {
		setLoadingHistogram(true);
		setLoadingTokens(true);
		setLoadingCost(true);
		setLoadingModels(true);
		setLoadingLatency(true);
		setLoadingProviderCost(true);
		setLoadingProviderTokens(true);
		setLoadingProviderLatency(true);

		const fetchFilters = { filters };

		// Fetch all in parallel, forcing fresh data (preferCacheValue: false bypasses RTK Query cache)
		const [
			histogramResult,
			tokenResult,
			costResult,
			modelResult,
			latencyResult,
			providerCostResult,
			providerTokenResult,
			providerLatencyResult,
		] = await Promise.all([
			triggerHistogram(fetchFilters, false),
			triggerTokens(fetchFilters, false),
			triggerCost(fetchFilters, false),
			triggerModels(fetchFilters, false),
			triggerLatency(fetchFilters, false),
			triggerProviderCost(fetchFilters, false),
			triggerProviderTokens(fetchFilters, false),
			triggerProviderLatency(fetchFilters, false),
		]);

		if (histogramResult.data) {
			setHistogramData(histogramResult.data);
		} else {
			setHistogramData(null);
		}
		setLoadingHistogram(false);

		if (tokenResult.data) {
			setTokenData(tokenResult.data);
		} else {
			setTokenData(null);
		}
		setLoadingTokens(false);

		if (costResult.data) {
			setCostData(costResult.data);
		} else {
			setCostData(null);
		}
		setLoadingCost(false);

		if (modelResult.data) {
			setModelData(modelResult.data);
		} else {
			setModelData(null);
		}
		setLoadingModels(false);

		if (latencyResult.data) {
			setLatencyData(latencyResult.data);
		} else {
			setLatencyData(null);
		}
		setLoadingLatency(false);

		if (providerCostResult.data) {
			setProviderCostData(providerCostResult.data);
		} else {
			setProviderCostData(null);
		}
		setLoadingProviderCost(false);

		if (providerTokenResult.data) {
			setProviderTokenData(providerTokenResult.data);
		} else {
			setProviderTokenData(null);
		}
		setLoadingProviderTokens(false);

		if (providerLatencyResult.data) {
			setProviderLatencyData(providerLatencyResult.data);
		} else {
			setProviderLatencyData(null);
		}
		setLoadingProviderLatency(false);
	}, [
		filters,
		triggerHistogram,
		triggerTokens,
		triggerCost,
		triggerModels,
		triggerLatency,
		triggerProviderCost,
		triggerProviderTokens,
		triggerProviderLatency,
	]);

	// Fetch data on mount and when filters change
	useEffect(() => {
		fetchAllData();
	}, [fetchAllData]);

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

	// Chart type toggles
	const handleVolumeChartToggle = useCallback((type: ChartType) => setUrlState({ volume_chart: type }), [setUrlState]);
	const handleTokenChartToggle = useCallback((type: ChartType) => setUrlState({ token_chart: type }), [setUrlState]);
	const handleCostChartToggle = useCallback((type: ChartType) => setUrlState({ cost_chart: type }), [setUrlState]);
	const handleModelChartToggle = useCallback((type: ChartType) => setUrlState({ model_chart: type }), [setUrlState]);
	const handleLatencyChartToggle = useCallback((type: ChartType) => setUrlState({ latency_chart: type }), [setUrlState]);

	// Filter change handler for FilterPopover
	const handleFilterChange = useCallback(
		(key: keyof LogFilters, values: string[] | boolean) => {
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
			} else {
				setUrlState({ [urlKey]: values.join(",") });
			}
		},
		[setUrlState],
	);

	const handleProviderCostChartToggle = useCallback((type: ChartType) => setUrlState({ provider_cost_chart: type }), [setUrlState]);
	const handleProviderTokenChartToggle = useCallback((type: ChartType) => setUrlState({ provider_token_chart: type }), [setUrlState]);
	const handleProviderLatencyChartToggle = useCallback((type: ChartType) => setUrlState({ provider_latency_chart: type }), [setUrlState]);

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

	return (
		<div className="mx-auto flex h-full min-h-[calc(100vh-100px)] w-full flex-col gap-4">
			{/* Header with time filter */}
			<div className="flex items-center justify-between">
				<div className="flex items-center gap-2">
					<h1 className="text-lg font-semibold">Dashboard </h1>
				</div>
				<div className="flex items-center gap-2">
					<FilterPopover filters={filters} onFilterChange={handleFilterChange} />
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

			{/* Charts Grid */}
			<div className="grid grid-cols-1 gap-2 lg:grid-cols-2 2xl:grid-cols-3">
				{/* Log Volume Chart */}
				<ChartCard
					title="Request Volume"
					loading={loadingHistogram}
					testId="chart-log-volume"
					headerActions={
						<div className={CHART_HEADER_ACTIONS_CLASS}>
							<div className={CHART_HEADER_LEGEND_CLASS}>
								<span className="flex items-center gap-1">
									<span className="h-2 w-2 rounded-full" style={{ backgroundColor: CHART_COLORS.success }} />
									<span className="text-muted-foreground">Success</span>
								</span>
								<span className="flex items-center gap-1">
									<span className="h-2 w-2 rounded-full" style={{ backgroundColor: CHART_COLORS.error }} />
									<span className="text-muted-foreground">Error</span>
								</span>
							</div>
							<div className={CHART_HEADER_CONTROLS_CLASS}>
								<ChartTypeToggle
									chartType={toChartType(urlState.volume_chart)}
									onToggle={handleVolumeChartToggle}
									data-testid="dashboard-volume-chart-toggle"
								/>
							</div>
						</div>
					}
				>
					<LogVolumeChart
						data={histogramData}
						chartType={toChartType(urlState.volume_chart)}
						startTime={urlState.start_time}
						endTime={urlState.end_time}
					/>
				</ChartCard>

				{/* Token Usage Chart */}
				<ChartCard
					title="Token Usage"
					loading={loadingTokens}
					testId="chart-token-usage"
					headerActions={
						<div className={CHART_HEADER_ACTIONS_CLASS}>
							<div className={CHART_HEADER_LEGEND_CLASS}>
								<span className="flex items-center gap-1">
									<span className="h-2 w-2 rounded-full" style={{ backgroundColor: CHART_COLORS.promptTokens }} />
									<span className="text-muted-foreground">Input</span>
								</span>
								<span className="flex items-center gap-1">
									<span className="h-2 w-2 rounded-full" style={{ backgroundColor: CHART_COLORS.completionTokens }} />
									<span className="text-muted-foreground">Output</span>
								</span>
							</div>
							<div className={CHART_HEADER_CONTROLS_CLASS}>
								<ChartTypeToggle
									chartType={toChartType(urlState.token_chart)}
									onToggle={handleTokenChartToggle}
									data-testid="dashboard-token-chart-toggle"
								/>
							</div>
						</div>
					}
				>
					<TokenUsageChart
						data={tokenData}
						chartType={toChartType(urlState.token_chart)}
						startTime={urlState.start_time}
						endTime={urlState.end_time}
					/>
				</ChartCard>

				{/* Cost Chart */}
				<ChartCard
					title="Cost"
					loading={loadingCost}
					testId="chart-cost-total"
					headerActions={
						<div className={CHART_HEADER_ACTIONS_CLASS}>
							<div className={CHART_HEADER_LEGEND_CLASS}>
								{urlState.cost_model === "all" ? (
									costModels.length > 0 && (
										<>
											<Tooltip>
												<TooltipTrigger asChild>
													<span className="flex items-center gap-1">
														<span className="h-2 w-2 shrink-0 rounded-full" style={{ backgroundColor: getModelColor(0) }} />
														<span className="text-muted-foreground max-w-[100px] truncate">{costModels[0]}</span>
													</span>
												</TooltipTrigger>
												<TooltipContent>{costModels[0]}</TooltipContent>
											</Tooltip>
											{costModels.length > 1 && (
												<Tooltip>
													<TooltipTrigger asChild>
														<span className="text-muted-foreground cursor-default">+{costModels.length - 1} more</span>
													</TooltipTrigger>
													<TooltipContent>
														<div className="flex flex-col gap-1">
															{costModels.slice(1).map((model, idx) => (
																<span key={model} className="flex items-center gap-1">
																	<span className="h-2 w-2 shrink-0 rounded-full" style={{ backgroundColor: getModelColor(idx + 1) }} />
																	{model}
																</span>
															))}
														</div>
													</TooltipContent>
												</Tooltip>
											)}
										</>
									)
								) : (
									<Tooltip>
										<TooltipTrigger asChild>
											<span className="flex items-center gap-1">
												<span className="h-2 w-2 shrink-0 rounded-full" style={{ backgroundColor: getModelColor(0) }} />
												<span className="text-muted-foreground max-w-[100px] truncate">{urlState.cost_model}</span>
											</span>
										</TooltipTrigger>
										<TooltipContent>{urlState.cost_model}</TooltipContent>
									</Tooltip>
								)}
							</div>
							<div className={CHART_HEADER_CONTROLS_CLASS}>
								<ModelFilterSelect
									models={availableModels}
									selectedModel={urlState.cost_model}
									onModelChange={handleCostModelChange}
									data-testid="dashboard-cost-model-filter"
								/>
								<ChartTypeToggle
									chartType={toChartType(urlState.cost_chart)}
									onToggle={handleCostChartToggle}
									data-testid="dashboard-cost-chart-toggle"
								/>
							</div>
						</div>
					}
				>
					<CostChart
						data={costData}
						chartType={toChartType(urlState.cost_chart)}
						startTime={urlState.start_time}
						endTime={urlState.end_time}
						selectedModel={urlState.cost_model}
					/>
				</ChartCard>

				{/* Model Usage Chart */}
				<ChartCard
					title="Model Usage"
					loading={loadingModels}
					testId="chart-model-usage"
					headerActions={
						<div className={CHART_HEADER_ACTIONS_CLASS}>
							<div className={CHART_HEADER_LEGEND_CLASS}>
								{urlState.usage_model === "all" ? (
									usageModels.length > 0 && (
										<>
											<Tooltip>
												<TooltipTrigger asChild>
													<span className="flex items-center gap-1">
														<span className="h-2 w-2 shrink-0 rounded-full" style={{ backgroundColor: getModelColor(0) }} />
														<span className="text-muted-foreground max-w-[100px] truncate">{usageModels[0]}</span>
													</span>
												</TooltipTrigger>
												<TooltipContent>{usageModels[0]}</TooltipContent>
											</Tooltip>
											{usageModels.length > 1 && (
												<Tooltip>
													<TooltipTrigger asChild>
														<span className="text-muted-foreground cursor-default">+{usageModels.length - 1} more</span>
													</TooltipTrigger>
													<TooltipContent>
														<div className="flex flex-col gap-1">
															{usageModels.slice(1).map((model, idx) => (
																<span key={model} className="flex items-center gap-1">
																	<span className="h-2 w-2 shrink-0 rounded-full" style={{ backgroundColor: getModelColor(idx + 1) }} />
																	{model}
																</span>
															))}
														</div>
													</TooltipContent>
												</Tooltip>
											)}
										</>
									)
								) : (
									<>
										<span className="flex items-center gap-1">
											<span className="h-2 w-2 shrink-0 rounded-full" style={{ backgroundColor: CHART_COLORS.success }} />
											<span className="text-muted-foreground">Success</span>
										</span>
										<span className="flex items-center gap-1">
											<span className="h-2 w-2 shrink-0 rounded-full" style={{ backgroundColor: CHART_COLORS.error }} />
											<span className="text-muted-foreground">Error</span>
										</span>
									</>
								)}
							</div>
							<div className={CHART_HEADER_CONTROLS_CLASS}>
								<ModelFilterSelect
									models={availableModels}
									selectedModel={urlState.usage_model}
									onModelChange={handleUsageModelChange}
									data-testid="dashboard-usage-model-filter"
								/>
								<ChartTypeToggle
									chartType={toChartType(urlState.model_chart)}
									onToggle={handleModelChartToggle}
									data-testid="dashboard-usage-chart-toggle"
								/>
							</div>
						</div>
					}
				>
					<ModelUsageChart
						data={modelData}
						chartType={toChartType(urlState.model_chart)}
						startTime={urlState.start_time}
						endTime={urlState.end_time}
						selectedModel={urlState.usage_model}
					/>
				</ChartCard>

				{/* Latency Chart */}
				<ChartCard
					title="Latency"
					loading={loadingLatency}
					testId="chart-latency"
					headerActions={
						<div className={CHART_HEADER_ACTIONS_CLASS}>
							<div className={CHART_HEADER_LEGEND_CLASS}>
								<span className="flex items-center gap-1">
									<span className="h-2 w-2 rounded-full" style={{ backgroundColor: LATENCY_COLORS.avg }} />
									<span className="text-muted-foreground">Avg</span>
								</span>
								<span className="flex items-center gap-1">
									<span className="h-2 w-2 rounded-full" style={{ backgroundColor: LATENCY_COLORS.p90 }} />
									<span className="text-muted-foreground">P90</span>
								</span>
								<span className="flex items-center gap-1">
									<span className="h-2 w-2 rounded-full" style={{ backgroundColor: LATENCY_COLORS.p95 }} />
									<span className="text-muted-foreground">P95</span>
								</span>
								<span className="flex items-center gap-1">
									<span className="h-2 w-2 rounded-full" style={{ backgroundColor: LATENCY_COLORS.p99 }} />
									<span className="text-muted-foreground">P99</span>
								</span>
							</div>
							<div className={CHART_HEADER_CONTROLS_CLASS}>
								<ChartTypeToggle
									chartType={toChartType(urlState.latency_chart)}
									onToggle={handleLatencyChartToggle}
									data-testid="dashboard-latency-chart-toggle"
								/>
							</div>
						</div>
					}
				>
					<LatencyChart
						data={latencyData}
						chartType={toChartType(urlState.latency_chart)}
						startTime={urlState.start_time}
						endTime={urlState.end_time}
					/>
				</ChartCard>
			</div>

			{/* Provider Level Metrics */}
			<h2 className="mt-2 text-sm font-semibold">Provider Level Metrics</h2>
			<div className="grid grid-cols-1 gap-2 lg:grid-cols-2 2xl:grid-cols-3">
				{/* Provider Cost Chart */}
				<ChartCard
					title="Provider Cost"
					loading={loadingProviderCost}
					testId="chart-provider-cost"
					headerActions={
						<div className={CHART_HEADER_ACTIONS_CLASS}>
							<div className={CHART_HEADER_LEGEND_CLASS}>
								{urlState.provider_cost_provider === "all" ? (
									providerCostProviders.length > 0 && (
										<>
											<Tooltip>
												<TooltipTrigger asChild>
													<span className="flex items-center gap-1">
														<span className="h-2 w-2 shrink-0 rounded-full" style={{ backgroundColor: getModelColor(0) }} />
														<span className="text-muted-foreground max-w-[100px] truncate">{providerCostProviders[0]}</span>
													</span>
												</TooltipTrigger>
												<TooltipContent>{providerCostProviders[0]}</TooltipContent>
											</Tooltip>
											{providerCostProviders.length > 1 && (
												<Tooltip>
													<TooltipTrigger asChild>
														<span className="text-muted-foreground cursor-default">+{providerCostProviders.length - 1} more</span>
													</TooltipTrigger>
													<TooltipContent>
														<div className="flex flex-col gap-1">
															{providerCostProviders.slice(1).map((provider, idx) => (
																<span key={provider} className="flex items-center gap-1">
																	<span className="h-2 w-2 shrink-0 rounded-full" style={{ backgroundColor: getModelColor(idx + 1) }} />
																	{provider}
																</span>
															))}
														</div>
													</TooltipContent>
												</Tooltip>
											)}
										</>
									)
								) : (
									<Tooltip>
										<TooltipTrigger asChild>
											<span className="flex items-center gap-1">
												<span className="h-2 w-2 shrink-0 rounded-full" style={{ backgroundColor: getModelColor(0) }} />
												<span className="text-muted-foreground max-w-[100px] truncate">{urlState.provider_cost_provider}</span>
											</span>
										</TooltipTrigger>
										<TooltipContent>{urlState.provider_cost_provider}</TooltipContent>
									</Tooltip>
								)}
							</div>
							<div className={CHART_HEADER_CONTROLS_CLASS}>
								<ProviderFilterSelect
									providers={availableProviders}
									selectedProvider={urlState.provider_cost_provider}
									onProviderChange={handleProviderCostProviderChange}
									data-testid="dashboard-provider-cost-filter"
								/>
								<ChartTypeToggle
									chartType={toChartType(urlState.provider_cost_chart)}
									onToggle={handleProviderCostChartToggle}
									data-testid="dashboard-provider-cost-chart-toggle"
								/>
							</div>
						</div>
					}
				>
					<ProviderCostChart
						data={providerCostData}
						chartType={toChartType(urlState.provider_cost_chart)}
						startTime={urlState.start_time}
						endTime={urlState.end_time}
						selectedProvider={urlState.provider_cost_provider}
					/>
				</ChartCard>

				{/* Provider Token Usage Chart */}
				<ChartCard
					title="Provider Token Usage"
					loading={loadingProviderTokens}
					testId="chart-provider-tokens"
					headerActions={
						<div className={CHART_HEADER_ACTIONS_CLASS}>
							<div className={CHART_HEADER_LEGEND_CLASS}>
								{urlState.provider_token_provider === "all" ? (
									providerTokenProviders.length > 0 && (
										<>
											<Tooltip>
												<TooltipTrigger asChild>
													<span className="flex items-center gap-1">
														<span className="h-2 w-2 shrink-0 rounded-full" style={{ backgroundColor: getModelColor(0) }} />
														<span className="text-muted-foreground max-w-[100px] truncate">{providerTokenProviders[0]}</span>
													</span>
												</TooltipTrigger>
												<TooltipContent>{providerTokenProviders[0]}</TooltipContent>
											</Tooltip>
											{providerTokenProviders.length > 1 && (
												<Tooltip>
													<TooltipTrigger asChild>
														<span className="text-muted-foreground cursor-default">+{providerTokenProviders.length - 1} more</span>
													</TooltipTrigger>
													<TooltipContent>
														<div className="flex flex-col gap-1">
															{providerTokenProviders.slice(1).map((provider, idx) => (
																<span key={provider} className="flex items-center gap-1">
																	<span className="h-2 w-2 shrink-0 rounded-full" style={{ backgroundColor: getModelColor(idx + 1) }} />
																	{provider}
																</span>
															))}
														</div>
													</TooltipContent>
												</Tooltip>
											)}
										</>
									)
								) : (
									<>
										<span className="flex items-center gap-1">
											<span className="h-2 w-2 shrink-0 rounded-full" style={{ backgroundColor: CHART_COLORS.promptTokens }} />
											<span className="text-muted-foreground">Input</span>
										</span>
										<span className="flex items-center gap-1">
											<span className="h-2 w-2 shrink-0 rounded-full" style={{ backgroundColor: CHART_COLORS.completionTokens }} />
											<span className="text-muted-foreground">Output</span>
										</span>
									</>
								)}
							</div>
							<div className={CHART_HEADER_CONTROLS_CLASS}>
								<ProviderFilterSelect
									providers={availableProviders}
									selectedProvider={urlState.provider_token_provider}
									onProviderChange={handleProviderTokenProviderChange}
									data-testid="dashboard-provider-token-filter"
								/>
								<ChartTypeToggle
									chartType={toChartType(urlState.provider_token_chart)}
									onToggle={handleProviderTokenChartToggle}
									data-testid="dashboard-provider-token-chart-toggle"
								/>
							</div>
						</div>
					}
				>
					<ProviderTokenChart
						data={providerTokenData}
						chartType={toChartType(urlState.provider_token_chart)}
						startTime={urlState.start_time}
						endTime={urlState.end_time}
						selectedProvider={urlState.provider_token_provider}
					/>
				</ChartCard>

				{/* Provider Latency Chart */}
				<ChartCard
					title="Provider Latency"
					loading={loadingProviderLatency}
					testId="chart-provider-latency"
					headerActions={
						<div className={CHART_HEADER_ACTIONS_CLASS}>
							<div className={CHART_HEADER_LEGEND_CLASS}>
								{urlState.provider_latency_provider === "all" ? (
									providerLatencyProviders.length > 0 && (
										<>
											<Tooltip>
												<TooltipTrigger asChild>
													<span className="flex items-center gap-1">
														<span className="h-2 w-2 shrink-0 rounded-full" style={{ backgroundColor: getModelColor(0) }} />
														<span className="text-muted-foreground max-w-[100px] truncate">{providerLatencyProviders[0]}</span>
													</span>
												</TooltipTrigger>
												<TooltipContent>{providerLatencyProviders[0]}</TooltipContent>
											</Tooltip>
											{providerLatencyProviders.length > 1 && (
												<Tooltip>
													<TooltipTrigger asChild>
														<span className="text-muted-foreground cursor-default">+{providerLatencyProviders.length - 1} more</span>
													</TooltipTrigger>
													<TooltipContent>
														<div className="flex flex-col gap-1">
															{providerLatencyProviders.slice(1).map((provider, idx) => (
																<span key={provider} className="flex items-center gap-1">
																	<span className="h-2 w-2 shrink-0 rounded-full" style={{ backgroundColor: getModelColor(idx + 1) }} />
																	{provider}
																</span>
															))}
														</div>
													</TooltipContent>
												</Tooltip>
											)}
										</>
									)
								) : (
									<>
										<span className="flex items-center gap-1">
											<span className="h-2 w-2 rounded-full" style={{ backgroundColor: LATENCY_COLORS.avg }} />
											<span className="text-muted-foreground">Avg</span>
										</span>
										<span className="flex items-center gap-1">
											<span className="h-2 w-2 rounded-full" style={{ backgroundColor: LATENCY_COLORS.p90 }} />
											<span className="text-muted-foreground">P90</span>
										</span>
										<span className="flex items-center gap-1">
											<span className="h-2 w-2 rounded-full" style={{ backgroundColor: LATENCY_COLORS.p95 }} />
											<span className="text-muted-foreground">P95</span>
										</span>
										<span className="flex items-center gap-1">
											<span className="h-2 w-2 rounded-full" style={{ backgroundColor: LATENCY_COLORS.p99 }} />
											<span className="text-muted-foreground">P99</span>
										</span>
									</>
								)}
							</div>
							<div className={CHART_HEADER_CONTROLS_CLASS}>
								<ProviderFilterSelect
									providers={availableProviders}
									selectedProvider={urlState.provider_latency_provider}
									onProviderChange={handleProviderLatencyProviderChange}
									data-testid="dashboard-provider-latency-filter"
								/>
								<ChartTypeToggle
									chartType={toChartType(urlState.provider_latency_chart)}
									onToggle={handleProviderLatencyChartToggle}
									data-testid="dashboard-provider-latency-chart-toggle"
								/>
							</div>
						</div>
					}
				>
					<ProviderLatencyChart
						data={providerLatencyData}
						chartType={toChartType(urlState.provider_latency_chart)}
						startTime={urlState.start_time}
						endTime={urlState.end_time}
						selectedProvider={urlState.provider_latency_provider}
					/>
				</ChartCard>
			</div>
		</div>
	);
}
