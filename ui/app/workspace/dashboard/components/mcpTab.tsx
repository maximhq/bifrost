import type { MCPCostHistogramResponse, MCPHistogramResponse, MCPTopToolsResponse } from "@/lib/types/logs";
import { COMPACT_NUMBER_FORMAT } from "@/lib/utils/numbers";
import NumberFlow from "@number-flow/react";
import { memo, useMemo } from "react";
import { CHART_COLORS, CHART_HEADER_LEGEND_CLASS } from "../utils/chartUtils";
import { ChartCard } from "./charts/chartCard";
import { type ChartType, ChartTypeToggle } from "./charts/chartTypeToggle";
import { MCPCostChart } from "./charts/mcpCostChart";
import { MCPTopToolsChart } from "./charts/mcpTopToolsChart";
import { MCPVolumeChart } from "./charts/mcpVolumeChart";

export interface MCPTabProps {
	// Data
	mcpHistogramData: MCPHistogramResponse | null;
	mcpCostData: MCPCostHistogramResponse | null;
	mcpTopToolsData: MCPTopToolsResponse | null;

	// Loading states
	loadingMcpHistogram: boolean;
	loadingMcpCost: boolean;
	loadingMcpTopTools: boolean;

	// Time range
	startTime: number;
	endTime: number;

	// Chart types
	mcpVolumeChartType: ChartType;
	mcpCostChartType: ChartType;

	// Chart type toggle callbacks
	onMcpVolumeChartToggle: (type: ChartType) => void;
	onMcpCostChartToggle: (type: ChartType) => void;
}

function MCPTabImpl({
	mcpHistogramData,
	mcpCostData,
	mcpTopToolsData,
	loadingMcpHistogram,
	loadingMcpCost,
	loadingMcpTopTools,
	startTime,
	endTime,
	mcpVolumeChartType,
	mcpCostChartType,
	onMcpVolumeChartToggle,
	onMcpCostChartToggle,
}: MCPTabProps) {
	const mcpVolumeTotal = useMemo(() => {
		if (!mcpHistogramData?.buckets) return null;
		return mcpHistogramData.buckets.reduce((sum, b) => sum + (b.count ?? 0), 0);
	}, [mcpHistogramData]);

	const mcpCostTotal = useMemo(() => {
		if (!mcpCostData?.buckets) return null;
		return mcpCostData.buckets.reduce((sum, b) => sum + (b.total_cost ?? 0), 0);
	}, [mcpCostData]);

	const mcpTopToolsTotal = useMemo(() => {
		if (!mcpTopToolsData?.tools) return null;
		return mcpTopToolsData.tools.reduce((sum, t) => sum + (t.count ?? 0), 0);
	}, [mcpTopToolsData]);

	return (
		<div className="grid grid-cols-1 gap-2 lg:grid-cols-2 2xl:grid-cols-3">
			{/* MCP Tool Calls Volume */}
			<ChartCard
				title="MCP Tool Calls"
				loading={loadingMcpHistogram}
				testId="chart-mcp-volume"
				totalLabel="Total"
				total={mcpVolumeTotal !== null ? <NumberFlow value={mcpVolumeTotal} format={COMPACT_NUMBER_FORMAT} /> : undefined}
				totalTooltip={mcpVolumeTotal !== null ? mcpVolumeTotal.toLocaleString("en-US") : undefined}
				legend={
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
				}
				controls={
					<ChartTypeToggle
						chartType={mcpVolumeChartType}
						onToggle={onMcpVolumeChartToggle}
						data-testid="dashboard-mcp-volume-chart-toggle"
					/>
				}
			>
				<MCPVolumeChart data={mcpHistogramData} chartType={mcpVolumeChartType} startTime={startTime} endTime={endTime} />
			</ChartCard>

			{/* MCP Cost */}
			<ChartCard
				title="MCP Cost"
				loading={loadingMcpCost}
				testId="chart-mcp-cost"
				totalLabel="Total"
				total={
					mcpCostTotal !== null ? (
						<NumberFlow value={mcpCostTotal} format={{ ...COMPACT_NUMBER_FORMAT, style: "currency", currency: "USD" }} />
					) : undefined
				}
				totalTooltip={
					mcpCostTotal !== null
						? mcpCostTotal.toLocaleString("en-US", { style: "currency", currency: "USD", maximumFractionDigits: 6 })
						: undefined
				}
				legend={
					<div className={CHART_HEADER_LEGEND_CLASS}>
						<span className="flex items-center gap-1">
							<span className="h-2 w-2 rounded-full" style={{ backgroundColor: CHART_COLORS.cost }} />
							<span className="text-muted-foreground">Cost</span>
						</span>
					</div>
				}
				controls={
					<ChartTypeToggle chartType={mcpCostChartType} onToggle={onMcpCostChartToggle} data-testid="dashboard-mcp-cost-chart-toggle" />
				}
			>
				<MCPCostChart data={mcpCostData} chartType={mcpCostChartType} startTime={startTime} endTime={endTime} />
			</ChartCard>

			{/* Top 10 MCP Tools */}
			<ChartCard
				title="Top 10 MCP Tools"
				loading={loadingMcpTopTools}
				testId="chart-mcp-top-tools"
				totalLabel="Total"
				total={mcpTopToolsTotal !== null ? <NumberFlow value={mcpTopToolsTotal} format={COMPACT_NUMBER_FORMAT} /> : undefined}
				totalTooltip={mcpTopToolsTotal !== null ? mcpTopToolsTotal.toLocaleString("en-US") : undefined}
			>
				<MCPTopToolsChart data={mcpTopToolsData} />
			</ChartCard>
		</div>
	);
}
export const MCPTab = memo(MCPTabImpl);