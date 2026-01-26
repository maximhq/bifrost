"use client";

import { Badge } from "@/components/ui/badge";
import { DateTimePickerWithRange } from "@/components/ui/datePickerWithRange";
import {
	useLazyGetLogsCostHistogramQuery,
	useLazyGetLogsHistogramQuery,
	useLazyGetLogsModelHistogramQuery,
	useLazyGetLogsTokenHistogramQuery,
} from "@/lib/store";
import type {
	CostHistogramResponse,
	LogFilters,
	LogsHistogramResponse,
	ModelHistogramResponse,
	TokenHistogramResponse,
} from "@/lib/types/logs";
import { dateUtils } from "@/lib/types/logs";
import { parseAsInteger, parseAsString, useQueryStates } from "nuqs";
import { useCallback, useEffect, useMemo, useState } from "react";
import { ChartCard } from "./components/chartCard";
import { type ChartType, ChartTypeToggle } from "./components/chartTypeToggle";
import { CostChart } from "./components/costChart";
import { LogVolumeChart } from "./components/logVolumeChart";
import { ModelFilterSelect } from "./components/modelFilterSelect";
import { ModelUsageChart } from "./components/modelUsageChart";
import { TokenUsageChart } from "./components/tokenUsageChart";
import { CHART_COLORS, getModelColor } from "./utils/chartUtils";

// Type-safe parser for chart type URL state
const toChartType = (value: string): ChartType => (value === 'line' ? 'line' : 'bar')

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

	// Loading states
	const [loadingHistogram, setLoadingHistogram] = useState(true);
	const [loadingTokens, setLoadingTokens] = useState(true);
	const [loadingCost, setLoadingCost] = useState(true);
	const [loadingModels, setLoadingModels] = useState(true);

	// RTK Query lazy hooks
	const [triggerHistogram] = useLazyGetLogsHistogramQuery({});
	const [triggerTokens] = useLazyGetLogsTokenHistogramQuery();
	const [triggerCost] = useLazyGetLogsCostHistogramQuery();
	const [triggerModels] = useLazyGetLogsModelHistogramQuery();

	// URL state management
	const [urlState, setUrlState] = useQueryStates(
		{
			start_time: parseAsInteger.withDefault(DEFAULT_START_TIME),
			end_time: parseAsInteger.withDefault(DEFAULT_END_TIME),
			period: parseAsString.withDefault("24h"),
			volume_chart: parseAsString.withDefault("bar"),
			token_chart: parseAsString.withDefault("bar"),
			cost_chart: parseAsString.withDefault("bar"),
			model_chart: parseAsString.withDefault("bar"),
			cost_model: parseAsString.withDefault("all"),
			usage_model: parseAsString.withDefault("all"),
		},
		{
			history: "push",
			shallow: false,
		},
	);

	// Derived filter for API calls
	const filters: LogFilters = useMemo(
		() => ({
			start_time: dateUtils.toISOString(urlState.start_time),
			end_time: dateUtils.toISOString(urlState.end_time),
		}),
		[urlState.start_time, urlState.end_time],
	);

	// Date range for picker
	const dateRange = useMemo(
		() => ({
			from: dateUtils.fromUnixTimestamp(urlState.start_time),
			to: dateUtils.fromUnixTimestamp(urlState.end_time),
		}),
		[urlState.start_time, urlState.end_time],
	);

	// Available models for dropdowns
	const availableModels = useMemo(() => {
		return costData?.models || modelData?.models || [];
	}, [costData?.models, modelData?.models]);

	// Fetch all data
	const fetchAllData = useCallback(async () => {
		setLoadingHistogram(true);
		setLoadingTokens(true);
		setLoadingCost(true);
		setLoadingModels(true);

		const fetchFilters = { filters };

		// Fetch all in parallel, forcing fresh data (preferCacheValue: false bypasses RTK Query cache)
		const [histogramResult, tokenResult, costResult, modelResult] = await Promise.all([
			triggerHistogram(fetchFilters, false),
			triggerTokens(fetchFilters, false),
			triggerCost(fetchFilters, false),
			triggerModels(fetchFilters, false),
		]);

		if (histogramResult.data) {
			setHistogramData(histogramResult.data);
		}
		setLoadingHistogram(false);

		if (tokenResult.data) {
			setTokenData(tokenResult.data);
		}
		setLoadingTokens(false);

		if (costResult.data) {
			setCostData(costResult.data);
		}
		setLoadingCost(false);

		if (modelResult.data) {
			setModelData(modelResult.data);
		}
		setLoadingModels(false);
	}, [filters, triggerHistogram, triggerTokens, triggerCost, triggerModels]);

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

	// Model filter changes
	const handleCostModelChange = useCallback((model: string) => setUrlState({ cost_model: model }), [setUrlState]);
	const handleUsageModelChange = useCallback((model: string) => setUrlState({ usage_model: model }), [setUrlState]);

	return (
		<div className="mx-auto flex h-full w-full flex-col gap-4">
			{/* Header with time filter */}
			<div className="flex items-center justify-between">
				<div className="flex items-center gap-2">
					<h1 className="text-lg font-semibold">Dashboard </h1>
					<Badge variant="secondary" className="text-xs">
						BETA
					</Badge>
				</div>
				<DateTimePickerWithRange
					dateTime={dateRange}
					onDateTimeUpdate={handleDateRangeChange}
					preDefinedPeriods={TIME_PERIODS}
					predefinedPeriod={urlState.period || undefined}
					onPredefinedPeriodChange={handlePeriodChange}
					popupAlignment="end"
				/>
			</div>

			{/* Charts Grid */}
			<div className="grid grid-cols-1 gap-2 lg:grid-cols-2">
				{/* Log Volume Chart */}
				<ChartCard
					title="Request Volume"
					loading={loadingHistogram}
					headerActions={
						<div className="flex items-center gap-3">
							<div className="flex items-center gap-2 text-xs">
								<span className="flex items-center gap-1">
									<span className="h-2 w-2 rounded-full" style={{ backgroundColor: CHART_COLORS.success }} />
									<span className="text-muted-foreground">Success</span>
								</span>
								<span className="flex items-center gap-1">
									<span className="h-2 w-2 rounded-full" style={{ backgroundColor: CHART_COLORS.error }} />
									<span className="text-muted-foreground">Error</span>
								</span>
							</div>
							<ChartTypeToggle chartType={toChartType(urlState.volume_chart)} onToggle={handleVolumeChartToggle} />
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
					headerActions={
						<div className="flex items-center gap-3">
							<div className="flex items-center gap-2 text-xs">
								<span className="flex items-center gap-1">
									<span className="h-2 w-2 rounded-full" style={{ backgroundColor: CHART_COLORS.promptTokens }} />
									<span className="text-muted-foreground">Input</span>
								</span>
								<span className="flex items-center gap-1">
									<span className="h-2 w-2 rounded-full" style={{ backgroundColor: CHART_COLORS.completionTokens }} />
									<span className="text-muted-foreground">Output</span>
								</span>
							</div>
							<ChartTypeToggle chartType={toChartType(urlState.token_chart)} onToggle={handleTokenChartToggle} />
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
					headerActions={
						<div className="flex items-center gap-3">
							<div className="flex items-center gap-2 text-xs">
								{urlState.cost_model === "all" ? (
									<>
										{availableModels.slice(0, 3).map((model, idx) => (
											<span key={model} className="flex items-center gap-1">
												<span className="h-2 w-2 rounded-full" style={{ backgroundColor: getModelColor(idx) }} />
												<span className="text-muted-foreground">{model}</span>
											</span>
										))}
										{availableModels.length > 3 && <span className="text-muted-foreground">+{availableModels.length - 3} more</span>}
									</>
								) : (
									<span className="flex items-center gap-1">
										<span
											className="h-2 w-2 rounded-full"
											style={{ backgroundColor: getModelColor(availableModels.indexOf(urlState.cost_model)) }}
										/>
										<span className="text-muted-foreground">{urlState.cost_model}</span>
									</span>
								)}
							</div>
							<ModelFilterSelect models={availableModels} selectedModel={urlState.cost_model} onModelChange={handleCostModelChange} />
							<ChartTypeToggle chartType={toChartType(urlState.cost_chart)} onToggle={handleCostChartToggle} />
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
					headerActions={
						<div className="flex items-center gap-3">
							<div className="flex items-center gap-2 text-xs">
								{urlState.usage_model === "all" ? (
									<>
										{availableModels.slice(0, 3).map((model, idx) => (
											<span key={model} className="flex items-center gap-1">
												<span className="h-2 w-2 rounded-full" style={{ backgroundColor: getModelColor(idx) }} />
												<span className="text-muted-foreground">{model}</span>
											</span>
										))}
										{availableModels.length > 3 && <span className="text-muted-foreground">+{availableModels.length - 3} more</span>}
									</>
								) : (
									<>
										<span className="flex items-center gap-1">
											<span className="h-2 w-2 rounded-full" style={{ backgroundColor: CHART_COLORS.success }} />
											<span className="text-muted-foreground">Success</span>
										</span>
										<span className="flex items-center gap-1">
											<span className="h-2 w-2 rounded-full" style={{ backgroundColor: CHART_COLORS.error }} />
											<span className="text-muted-foreground">Error</span>
										</span>
									</>
								)}
							</div>
							<ModelFilterSelect models={availableModels} selectedModel={urlState.usage_model} onModelChange={handleUsageModelChange} />
							<ChartTypeToggle chartType={toChartType(urlState.model_chart)} onToggle={handleModelChartToggle} />
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
			</div>
		</div>
	);
}
