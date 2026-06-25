import {
	useGetLogsCostHistogramQuery,
	useGetLogsHistogramQuery,
	useGetLogsLatencyHistogramQuery,
	useGetLogsModelHistogramQuery,
	useGetLogsStatsQuery,
	useGetLogsTokenHistogramQuery,
	useLazyGetLogsCostHistogramQuery,
	useLazyGetLogsHistogramQuery,
	useLazyGetLogsLatencyHistogramQuery,
	useLazyGetLogsModelHistogramQuery,
	useLazyGetLogsStatsQuery,
	useLazyGetLogsTokenHistogramQuery,
} from "@/lib/store";
import type { LogFilters } from "@/lib/types/logs";
import { forwardRef, useCallback, useImperativeHandle, useMemo } from "react";
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
	costModel: string;
	usageModel: string;
	onVolumeChartToggle: (type: ChartType) => void;
	onTokenChartToggle: (type: ChartType) => void;
	onCostChartToggle: (type: ChartType) => void;
	onModelChartToggle: (type: ChartType) => void;
	onLatencyChartToggle: (type: ChartType) => void;
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
		costModel,
		usageModel,
		onVolumeChartToggle,
		onTokenChartToggle,
		onCostChartToggle,
		onModelChartToggle,
		onLatencyChartToggle,
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
	const { data: logsStats, isLoading: loadingStats } = useGetLogsStatsQuery(fetchArg, skipOpts);

	const [triggerHistogram] = useLazyGetLogsHistogramQuery();
	const [triggerTokens] = useLazyGetLogsTokenHistogramQuery();
	const [triggerCost] = useLazyGetLogsCostHistogramQuery();
	const [triggerModels] = useLazyGetLogsModelHistogramQuery();
	const [triggerLatency] = useLazyGetLogsLatencyHistogramQuery();
	const [triggerStats] = useLazyGetLogsStatsQuery();

	const loadData = useCallback(async () => {
		await Promise.all([
			triggerHistogram(fetchArg, true),
			triggerTokens(fetchArg, true),
			triggerCost(fetchArg, true),
			triggerModels(fetchArg, true),
			triggerLatency(fetchArg, true),
			triggerStats(fetchArg, true),
		]);
	}, [fetchArg, triggerHistogram, triggerTokens, triggerCost, triggerModels, triggerLatency, triggerStats]);

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

	const costModels = useMemo(() => sanitizeSeriesLabels(costData?.models), [costData?.models]);
	const usageModels = useMemo(() => sanitizeSeriesLabels(modelData?.models), [modelData?.models]);
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
			logsStats={logsStats ?? null}
			loadingHistogram={loadingHistogram}
			loadingTokens={loadingTokens}
			loadingCost={loadingCost}
			loadingModels={loadingModels}
			loadingLatency={loadingLatency}
			loadingStats={loadingStats}
			startTime={startTime}
			endTime={endTime}
			volumeChartType={volumeChartType}
			tokenChartType={tokenChartType}
			costChartType={costChartType}
			modelChartType={modelChartType}
			latencyChartType={latencyChartType}
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
			onCostModelChange={onCostModelChange}
			onUsageModelChange={onUsageModelChange}
		/>
	);
});