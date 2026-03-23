"use client";

import type { MCPCostHistogramResponse, MCPHistogramResponse, MCPTopToolsResponse } from "@/lib/types/logs";
import { CHART_COLORS, CHART_HEADER_ACTIONS_CLASS, CHART_HEADER_CONTROLS_CLASS, CHART_HEADER_LEGEND_CLASS } from "../utils/chartUtils";
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

export function MCPTab({
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
	return (
		<div className="grid grid-cols-1 gap-2 lg:grid-cols-2 2xl:grid-cols-3">
			{/* MCP Tool Calls Volume */}
			<ChartCard
				title="MCP Tool Calls"
				loading={loadingMcpHistogram}
				testId="chart-mcp-volume"
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
								chartType={mcpVolumeChartType}
								onToggle={onMcpVolumeChartToggle}
								data-testid="dashboard-mcp-volume-chart-toggle"
							/>
						</div>
					</div>
				}
			>
				<MCPVolumeChart data={mcpHistogramData} chartType={mcpVolumeChartType} startTime={startTime} endTime={endTime} />
			</ChartCard>

			{/* MCP Cost */}
			<ChartCard
				title="MCP Cost"
				loading={loadingMcpCost}
				testId="chart-mcp-cost"
				headerActions={
					<div className={CHART_HEADER_ACTIONS_CLASS}>
						<div className={CHART_HEADER_LEGEND_CLASS}>
							<span className="flex items-center gap-1">
								<span className="h-2 w-2 rounded-full" style={{ backgroundColor: CHART_COLORS.cost }} />
								<span className="text-muted-foreground">Cost</span>
							</span>
						</div>
						<div className={CHART_HEADER_CONTROLS_CLASS}>
							<ChartTypeToggle chartType={mcpCostChartType} onToggle={onMcpCostChartToggle} data-testid="dashboard-mcp-cost-chart-toggle" />
						</div>
					</div>
				}
			>
				<MCPCostChart data={mcpCostData} chartType={mcpCostChartType} startTime={startTime} endTime={endTime} />
			</ChartCard>

			{/* Top 10 MCP Tools */}
			<ChartCard title="Top 10 MCP Tools" loading={loadingMcpTopTools} testId="chart-mcp-top-tools">
				<MCPTopToolsChart data={mcpTopToolsData} />
			</ChartCard>
		</div>
	);
}
