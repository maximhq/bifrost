import {
	useGetLogsProviderCostHistogramQuery,
	useGetLogsProviderLatencyHistogramQuery,
	useGetLogsProviderThroughputHistogramQuery,
	useGetLogsProviderTokenHistogramQuery,
	useLazyGetLogsProviderCostHistogramQuery,
	useLazyGetLogsProviderLatencyHistogramQuery,
	useLazyGetLogsProviderThroughputHistogramQuery,
	useLazyGetLogsProviderTokenHistogramQuery,
} from "@/lib/store";
import type { LogFilters } from "@/lib/types/logs";
import { forwardRef, useCallback, useImperativeHandle, useMemo } from "react";
import type { DashboardData } from "../../utils/exportUtils";
import type { ChartType } from "../charts/chartTypeToggle";
import { ProviderUsageTab } from "../providerUsageTab";

export interface ProviderUsageTabViewHandle {
	getData: () => Partial<DashboardData>;
	loadData: () => Promise<void>;
}

const sanitizeSeriesLabels = (values?: string[]): string[] => {
	if (!values) return [];
	const trimmed = values.map((v) => v.trim()).filter((v) => v.length > 0);
	return [...new Set(trimmed)];
};

interface ProviderUsageTabViewProps {
	filters: LogFilters;
	active: boolean;
	startTime: number;
	endTime: number;
	providerCostChartType: ChartType;
	providerTokenChartType: ChartType;
	providerLatencyChartType: ChartType;
	providerThroughputChartType: ChartType;
	providerCostProvider: string;
	providerTokenProvider: string;
	providerLatencyProvider: string;
	providerThroughputProvider: string;
	onProviderCostChartToggle: (type: ChartType) => void;
	onProviderTokenChartToggle: (type: ChartType) => void;
	onProviderLatencyChartToggle: (type: ChartType) => void;
	onProviderThroughputChartToggle: (type: ChartType) => void;
	onProviderCostProviderChange: (provider: string) => void;
	onProviderTokenProviderChange: (provider: string) => void;
	onProviderLatencyProviderChange: (provider: string) => void;
	onProviderThroughputProviderChange: (provider: string) => void;
}

export const ProviderUsageTabView = forwardRef<ProviderUsageTabViewHandle, ProviderUsageTabViewProps>(function ProviderUsageTabView(
	{
		filters,
		active,
		startTime,
		endTime,
		providerCostChartType,
		providerTokenChartType,
		providerLatencyChartType,
		providerThroughputChartType,
		providerCostProvider,
		providerTokenProvider,
		providerLatencyProvider,
		providerThroughputProvider,
		onProviderCostChartToggle,
		onProviderTokenChartToggle,
		onProviderLatencyChartToggle,
		onProviderThroughputChartToggle,
		onProviderCostProviderChange,
		onProviderTokenProviderChange,
		onProviderLatencyProviderChange,
		onProviderThroughputProviderChange,
	},
	ref,
) {
	const fetchArg = useMemo(() => ({ filters }), [filters]);
	const skipOpts = useMemo(() => ({ skip: !active }), [active]);

	const { data: providerCostData, isLoading: loadingProviderCost } = useGetLogsProviderCostHistogramQuery(fetchArg, skipOpts);
	const { data: providerTokenData, isLoading: loadingProviderTokens } = useGetLogsProviderTokenHistogramQuery(fetchArg, skipOpts);
	const { data: providerLatencyData, isLoading: loadingProviderLatency } = useGetLogsProviderLatencyHistogramQuery(fetchArg, skipOpts);
	const { data: providerThroughputData, isLoading: loadingProviderThroughput } = useGetLogsProviderThroughputHistogramQuery(fetchArg, skipOpts);

	const [triggerProviderCost] = useLazyGetLogsProviderCostHistogramQuery();
	const [triggerProviderTokens] = useLazyGetLogsProviderTokenHistogramQuery();
	const [triggerProviderLatency] = useLazyGetLogsProviderLatencyHistogramQuery();
	const [triggerProviderThroughput] = useLazyGetLogsProviderThroughputHistogramQuery();

	const loadData = useCallback(async () => {
		await Promise.all([
			triggerProviderCost(fetchArg, true),
			triggerProviderTokens(fetchArg, true),
			triggerProviderLatency(fetchArg, true),
			triggerProviderThroughput(fetchArg, true),
		]);
	}, [fetchArg, triggerProviderCost, triggerProviderTokens, triggerProviderLatency, triggerProviderThroughput]);

	useImperativeHandle(
		ref,
		() => ({
			getData: () => ({
				providerCostData: providerCostData ?? null,
				providerTokenData: providerTokenData ?? null,
				providerLatencyData: providerLatencyData ?? null,
			}),
			loadData,
		}),
		[providerCostData, providerTokenData, providerLatencyData, loadData],
	);

	const availableProviders = useMemo(
		() =>
			sanitizeSeriesLabels([
				...(providerCostData?.providers ?? []),
				...(providerTokenData?.providers ?? []),
				...(providerLatencyData?.providers ?? []),
				...(providerThroughputData?.providers ?? []),
			]),
		[providerCostData?.providers, providerTokenData?.providers, providerLatencyData?.providers, providerThroughputData?.providers],
	);
	const providerCostProviders = useMemo(() => sanitizeSeriesLabels(providerCostData?.providers), [providerCostData?.providers]);
	const providerTokenProviders = useMemo(() => sanitizeSeriesLabels(providerTokenData?.providers), [providerTokenData?.providers]);
	const providerLatencyProviders = useMemo(() => sanitizeSeriesLabels(providerLatencyData?.providers), [providerLatencyData?.providers]);
	const providerThroughputProviders = useMemo(
		() => sanitizeSeriesLabels(providerThroughputData?.providers),
		[providerThroughputData?.providers],
	);

	return (
		<ProviderUsageTab
			providerCostData={providerCostData ?? null}
			providerTokenData={providerTokenData ?? null}
			providerLatencyData={providerLatencyData ?? null}
			providerThroughputData={providerThroughputData ?? null}
			loadingProviderCost={loadingProviderCost}
			loadingProviderTokens={loadingProviderTokens}
			loadingProviderLatency={loadingProviderLatency}
			loadingProviderThroughput={loadingProviderThroughput}
			startTime={startTime}
			endTime={endTime}
			providerCostChartType={providerCostChartType}
			providerTokenChartType={providerTokenChartType}
			providerLatencyChartType={providerLatencyChartType}
			providerThroughputChartType={providerThroughputChartType}
			providerCostProvider={providerCostProvider}
			providerTokenProvider={providerTokenProvider}
			providerLatencyProvider={providerLatencyProvider}
			providerThroughputProvider={providerThroughputProvider}
			availableProviders={availableProviders}
			providerCostProviders={providerCostProviders}
			providerTokenProviders={providerTokenProviders}
			providerLatencyProviders={providerLatencyProviders}
			providerThroughputProviders={providerThroughputProviders}
			onProviderCostChartToggle={onProviderCostChartToggle}
			onProviderTokenChartToggle={onProviderTokenChartToggle}
			onProviderLatencyChartToggle={onProviderLatencyChartToggle}
			onProviderThroughputChartToggle={onProviderThroughputChartToggle}
			onProviderCostProviderChange={onProviderCostProviderChange}
			onProviderTokenProviderChange={onProviderTokenProviderChange}
			onProviderLatencyProviderChange={onProviderLatencyProviderChange}
			onProviderThroughputProviderChange={onProviderThroughputProviderChange}
		/>
	);
});