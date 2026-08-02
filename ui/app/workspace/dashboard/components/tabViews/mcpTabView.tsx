import {
	useGetMCPCostHistogramQuery,
	useGetMCPHistogramQuery,
	useGetMCPTopToolsQuery,
	useLazyGetMCPCostHistogramQuery,
	useLazyGetMCPHistogramQuery,
	useLazyGetMCPTopToolsQuery,
} from "@/lib/store";
import type { MCPToolLogFilters } from "@/lib/types/logs";
import { forwardRef, useCallback, useImperativeHandle, useMemo } from "react";
import type { DashboardData } from "../../utils/exportUtils";
import type { ChartType } from "../charts/chartTypeToggle";
import { MCPTab } from "../mcpTab";

export interface MCPTabViewHandle {
	getData: () => Partial<DashboardData>;
	loadData: () => Promise<void>;
}

interface MCPTabViewProps {
	filters: MCPToolLogFilters;
	active: boolean;
	startTime: number;
	endTime: number;
	mcpVolumeChartType: ChartType;
	mcpCostChartType: ChartType;
	onMcpVolumeChartToggle: (type: ChartType) => void;
	onMcpCostChartToggle: (type: ChartType) => void;
}

export const MCPTabView = forwardRef<MCPTabViewHandle, MCPTabViewProps>(function MCPTabView(
	{ filters, active, startTime, endTime, mcpVolumeChartType, mcpCostChartType, onMcpVolumeChartToggle, onMcpCostChartToggle },
	ref,
) {
	const fetchArg = useMemo(() => ({ filters }), [filters]);
	const skipOpts = useMemo(() => ({ skip: !active }), [active]);

	const { data: mcpHistogramData, isLoading: loadingMcpHistogram } = useGetMCPHistogramQuery(fetchArg, skipOpts);
	const { data: mcpCostData, isLoading: loadingMcpCost } = useGetMCPCostHistogramQuery(fetchArg, skipOpts);
	const { data: mcpTopToolsData, isLoading: loadingMcpTopTools } = useGetMCPTopToolsQuery(fetchArg, skipOpts);

	const [triggerMcpHistogram] = useLazyGetMCPHistogramQuery();
	const [triggerMcpCost] = useLazyGetMCPCostHistogramQuery();
	const [triggerMcpTopTools] = useLazyGetMCPTopToolsQuery();

	const loadData = useCallback(async () => {
		await Promise.all([triggerMcpHistogram(fetchArg, true), triggerMcpCost(fetchArg, true), triggerMcpTopTools(fetchArg, true)]);
	}, [fetchArg, triggerMcpHistogram, triggerMcpCost, triggerMcpTopTools]);

	useImperativeHandle(
		ref,
		() => ({
			getData: () => ({
				mcpHistogramData: mcpHistogramData ?? null,
				mcpCostData: mcpCostData ?? null,
				mcpTopToolsData: mcpTopToolsData ?? null,
			}),
			loadData,
		}),
		[mcpHistogramData, mcpCostData, mcpTopToolsData, loadData],
	);

	return (
		<MCPTab
			mcpHistogramData={mcpHistogramData ?? null}
			mcpCostData={mcpCostData ?? null}
			mcpTopToolsData={mcpTopToolsData ?? null}
			loadingMcpHistogram={loadingMcpHistogram}
			loadingMcpCost={loadingMcpCost}
			loadingMcpTopTools={loadingMcpTopTools}
			startTime={startTime}
			endTime={endTime}
			mcpVolumeChartType={mcpVolumeChartType}
			mcpCostChartType={mcpCostChartType}
			onMcpVolumeChartToggle={onMcpVolumeChartToggle}
			onMcpCostChartToggle={onMcpCostChartToggle}
		/>
	);
});