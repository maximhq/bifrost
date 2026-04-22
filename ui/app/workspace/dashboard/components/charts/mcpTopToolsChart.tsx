import type { MCPTopToolsResponse } from "@/lib/types/logs";
import { useMemo } from "react";
import { Bar, BarChart, CartesianGrid, Cell, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts";
import { formatCost, getModelColor } from "../../utils/chartUtils";
import { ChartErrorBoundary } from "./chartErrorBoundary";

interface MCPTopToolsChartProps {
	data: MCPTopToolsResponse | null;
}

function CustomTooltip({ active, payload }: any) {
	if (!active || !payload || !payload.length) return null;

	const data = payload[0]?.payload;
	if (!data) return null;

	return (
		<div className="rounded-sm border border-zinc-200 bg-white px-3 py-2 shadow-lg dark:border-zinc-700 dark:bg-zinc-900">
			<div className="mb-1 text-xs font-medium text-zinc-700 dark:text-zinc-300">{data.tool_name}</div>
			<div className="space-y-1 text-sm">
				<div className="flex items-center justify-between gap-4">
					<span className="text-zinc-600 dark:text-zinc-400">Count</span>
					<span className="font-medium">{data.count.toLocaleString()}</span>
				</div>
				<div className="flex items-center justify-between gap-4">
					<span className="text-zinc-600 dark:text-zinc-400">Cost</span>
					<span className="font-medium">{formatCost(data.cost)}</span>
				</div>
			</div>
		</div>
	);
}

export function MCPTopToolsChart({ data }: MCPTopToolsChartProps) {
	const chartData = useMemo(() => {
		if (!data?.tools || data.tools.length === 0) {
			return [];
		}

		return data.tools.slice(0, 10);
	}, [data]);

	if (!data?.tools || chartData.length === 0) {
		return <div className="text-muted-foreground flex h-full items-center justify-center text-sm">No data available</div>;
	}

	return (
		<ChartErrorBoundary resetKey={JSON.stringify(chartData.map(({ tool_name, count, cost }) => [tool_name, count, cost]))}>
			<ResponsiveContainer width="100%" height="100%">
				<BarChart data={chartData} layout="vertical" margin={{ top: 6, right: 4, left: 4, bottom: 0 }}>
					<CartesianGrid strokeDasharray="3 3" horizontal={false} className="stroke-zinc-200 dark:stroke-zinc-700" />
					<XAxis
						type="number"
						tick={{ fontSize: 11, className: "fill-zinc-500" }}
						tickLine={false}
						axisLine={false}
						tickFormatter={(v) => v.toLocaleString()}
						domain={[0, (dataMax: number) => Math.max(dataMax, 1)]}
						allowDataOverflow={false}
					/>
					<YAxis
						type="category"
						dataKey="tool_name"
						tick={{ fontSize: 11, className: "fill-zinc-500" }}
						tickLine={false}
						axisLine={false}
						width={120}
						interval={0}
					/>
					<Tooltip content={<CustomTooltip />} cursor={{ fill: "#8c8c8f", fillOpacity: 0.15 }} />
					<Bar isAnimationActive={false} dataKey="count" radius={[0, 2, 2, 0]} barSize={20}>
						{chartData.map((_, index) => (
							<Cell key={`cell-${index}`} fill={getModelColor(index)} fillOpacity={0.9} />
						))}
					</Bar>
				</BarChart>
			</ResponsiveContainer>
		</ChartErrorBoundary>
	);
}