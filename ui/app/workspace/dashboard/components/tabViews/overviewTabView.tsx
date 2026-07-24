import {
	useGetLogsCostHistogramQuery,
	useGetLogsHistogramQuery,
	useGetLogsLatencyHistogramQuery,
	useGetLogsModelHistogramQuery,
	useGetLogsStatsQuery,
	useGetLogsThroughputHistogramQuery,
	useGetLogsTokenHistogramQuery,
	useLazyGetLogsCostHistogramQuery,
	useLazyGetLogsHistogramQuery,
	useLazyGetLogsLatencyHistogramQuery,
	useLazyGetLogsModelHistogramQuery,
	useLazyGetLogsStatsQuery,
	useLazyGetLogsThroughputHistogramQuery,
	useLazyGetLogsTokenHistogramQuery,
} from "@/lib/store";
import type { LogFilters } from "@/lib/types/logs";
import { forwardRef, useCallback, useImperativeHandle, useMemo } from "react";
import { computeDisplaySeries } from "../../utils/chartUtils";
import type { DashboardData } from "../../utils/exportUtils";
import type { ChartType } from "../charts/chartTypeToggle";
import { OverviewTab } from "../overviewTab";

export interface OverviewTabViewHandle {
	getData: () => Partial<DashboardData>;
	loadData: () => Promise<void>;
}

const sanitizeSeriesLabels = (values?: string[]): string[] => {
	if (!values) return [];
	const trimmed = values.map((v) => v.trim()).filter((v) => v.length > 0);
	return [...new Set(trimmed)];
};

interface OverviewTabViewProps {
	filters: LogFilters;
	active: boolean;
	startTime: number;
	endTime: number;
	volumeChartType: ChartType;
	tokenChartType: ChartType;
	costChartType: ChartType;
	modelChartType: ChartType;
	latencyChartType: ChartType;
	overheadChartType: ChartType;
	throughputChartType: ChartType;
	costModel: string;
	usageModel: string;
	onVolumeChartToggle: (type: ChartType) => void;
	onTokenChartToggle: (type: ChartType) => void;
	onCostChartToggle: (type: ChartType) => void;
	onModelChartToggle: (type: ChartType) => void;
	onLatencyChartToggle: (type: ChartType) => void;
	onOverheadChartToggle: (type: ChartType) => void;
	onThroughputChartToggle: (type: ChartType) => void;
	onCostModelChange: (model: string) => void;
	onUsageModelChange: (model: string) => void;
}

export const OverviewTabView = forwardRef<OverviewTabViewHandle, OverviewTabViewProps>(function OverviewTabView(
	{
		filters,
		active,
		startTime,
		endTime,
		volumeChartType,
		tokenChartType,
		costChartType,
		modelChartType,
		latencyChartType,
		overheadChartType,
		throughputChartType,
		costModel,
		usageModel,
		onVolumeChartToggle,
		onTokenChartToggle,
		onCostChartToggle,
		onModelChartToggle,
		onLatencyChartToggle,
		onOverheadChartToggle,
		onThroughputChartToggle,
		onCostModelChange,
		onUsageModelChange,
	},
	ref,
) {
	const fetchArg = useMemo(() => ({ filters }), [filters]);
	const skipOpts = useMemo(() => ({ skip: !active }), [active]);

	const { data: histogramData, isLoading: loadingHistogram } = useGetLogsHistogramQuery(fetchArg, skipOpts);
	const { data: tokenData, isLoading: loadingTokens } = useGetLogsTokenHistogramQuery(fetchArg, skipOpts);
	const { data: costData, isLoading: loadingCost } = useGetLogsCostHistogramQuery(fetchArg, skipOpts);
	const { data: modelData, isLoading: loadingModels } = useGetLogsModelHistogramQuery(fetchArg, skipOpts);
	const { data: latencyData, isLoading: loadingLatency } = useGetLogsLatencyHistogramQuery(fetchArg, skipOpts);
	const { data: throughputData, isLoading: loadingThroughput } = useGetLogsThroughputHistogramQuery(fetchArg, skipOpts);
	const { data: logsStats, isLoading: loadingStats } = useGetLogsStatsQuery(fetchArg, skipOpts);

	const [triggerHistogram] = useLazyGetLogsHistogramQuery();
	const [triggerTokens] = useLazyGetLogsTokenHistogramQuery();
	const [triggerCost] = useLazyGetLogsCostHistogramQuery();
	const [triggerModels] = useLazyGetLogsModelHistogramQuery();
	const [triggerLatency] = useLazyGetLogsLatencyHistogramQuery();
	const [triggerThroughput] = useLazyGetLogsThroughputHistogramQuery();
	const [triggerStats] = useLazyGetLogsStatsQuery();

	const loadData = useCallback(async () => {
		await Promise.all([
			triggerHistogram(fetchArg, true),
			triggerTokens(fetchArg, true),
			triggerCost(fetchArg, true),
			triggerModels(fetchArg, true),
			triggerLatency(fetchArg, true),
			triggerThroughput(fetchArg, true),
			triggerStats(fetchArg, true),
		]);
	}, [fetchArg, triggerHistogram, triggerTokens, triggerCost, triggerModels, triggerLatency, triggerThroughput, triggerStats]);

	useImperativeHandle(
		ref,
		() => ({
			getData: () => ({
				histogramData: histogramData ?? null,
				tokenData: tokenData ?? null,
				costData: costData ?? null,
				modelData: modelData ?? null,
				latencyData: latencyData ?? null,
			}),
			loadData,
		}),
		[histogramData, tokenData, costData, modelData, latencyData, loadData],
	);

	// Legend lists mirror the charts' display order (top-N by volume + "Other"),
	// not the API's alphabetical order — index-based colors must match the bars.
	const costModels = useMemo(() => computeDisplaySeries(costData?.buckets, costData?.models, (b, m) => b.by_model?.[m] ?? 0), [costData]);
	const usageModels = useMemo(
		() => computeDisplaySeries(modelData?.buckets, modelData?.models, (b, m) => b.by_model?.[m]?.total ?? 0),
		[modelData],
	);
	const availableModels = useMemo(
		() => sanitizeSeriesLabels([...(costData?.models ?? []), ...(modelData?.models ?? [])]),
		[costData?.models, modelData?.models],
	);

	return (
		<OverviewTab
			histogramData={histogramData ?? null}
			tokenData={tokenData ?? null}
			costData={costData ?? null}
			modelData={modelData ?? null}
			latencyData={latencyData ?? null}
			throughputData={throughputData ?? null}
			logsStats={logsStats ?? null}
			loadingHistogram={loadingHistogram}
			loadingTokens={loadingTokens}
			loadingCost={loadingCost}
			loadingModels={loadingModels}
			loadingLatency={loadingLatency}
			loadingThroughput={loadingThroughput}
			loadingStats={loadingStats}
			startTime={startTime}
			endTime={endTime}
			volumeChartType={volumeChartType}
			tokenChartType={tokenChartType}
			costChartType={costChartType}
			modelChartType={modelChartType}
			latencyChartType={latencyChartType}
			overheadChartType={overheadChartType}
			throughputChartType={throughputChartType}
			costModel={costModel}
			usageModel={usageModel}
			costModels={costModels}
			usageModels={usageModels}
			availableModels={availableModels}
			onVolumeChartToggle={onVolumeChartToggle}
			onTokenChartToggle={onTokenChartToggle}
			onCostChartToggle={onCostChartToggle}
			onModelChartToggle={onModelChartToggle}
			onLatencyChartToggle={onLatencyChartToggle}
			onOverheadChartToggle={onOverheadChartToggle}
			onThroughputChartToggle={onThroughputChartToggle}
			onCostModelChange={onCostModelChange}
			onUsageModelChange={onUsageModelChange}
		/>
	);
});