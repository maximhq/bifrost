"use client";

import type { CostHistogramResponse } from "@/lib/types/logs";
import { useMemo } from "react";
import { Area, AreaChart, Bar, BarChart, CartesianGrid, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts";
import { formatCost, formatFullTimestamp, formatTimestamp, getModelColor } from "../utils/chartUtils";
import { ChartErrorBoundary } from "./chartErrorBoundary";
import type { ChartType } from "./chartTypeToggle";

interface CostChartProps {
	data: CostHistogramResponse | null;
	chartType: ChartType;
	startTime: number;
	endTime: number;
	selectedModel: string;
}

function CustomTooltip({ active, payload, selectedModel, models }: any) {
	if (!active || !payload || !payload.length) return null;

	const data = payload[0]?.payload;
	if (!data) return null;

	return (
		<div className="rounded-sm border border-zinc-200 bg-white px-3 py-2 shadow-lg dark:border-zinc-700 dark:bg-zinc-900">
			<div className="mb-1 text-xs text-zinc-500">{formatFullTimestamp(data.timestamp)}</div>
			<div className="space-y-1 text-sm">
				{selectedModel === "all" ? (
					<>
						{models.map((model: string, idx: number) => {
							const cost = data.by_model?.[model] || 0;
							if (cost === 0) return null;
							return (
								<div key={model} className="flex items-center justify-between gap-4">
									<span className="flex items-center gap-1.5">
										<span className="h-2 w-2 rounded-full" style={{ backgroundColor: getModelColor(idx) }} />
										<span className="max-w-[120px] truncate text-zinc-600 dark:text-zinc-400">{model}</span>
									</span>
									<span className="font-medium">{formatCost(cost)}</span>
								</div>
							);
						})}
						<div className="flex items-center justify-between gap-4 border-t border-zinc-200 pt-1 dark:border-zinc-700">
							<span className="text-zinc-600 dark:text-zinc-400">Total</span>
							<span className="font-medium">{formatCost(data.total_cost)}</span>
						</div>
					</>
				) : (
					<div className="flex items-center justify-between gap-4">
						<span className="flex items-center gap-1.5">
							<span className="h-2 w-2 rounded-full" style={{ backgroundColor: getModelColor(0) }} />
							<span className="text-zinc-600 dark:text-zinc-400">{selectedModel}</span>
						</span>
						<span className="font-medium">{formatCost(data.by_model?.[selectedModel] || 0)}</span>
					</div>
				)}
			</div>
		</div>
	);
}

export function CostChart({ data, chartType, startTime, endTime, selectedModel }: CostChartProps) {
	const { chartData, displayModels } = useMemo(() => {
		if (!data?.buckets || !data.bucket_size_seconds) {
			return { chartData: [], displayModels: [] };
		}

		const models = selectedModel === "all" ? data.models : [selectedModel];

		const processed = data.buckets.map((bucket, index) => {
			const item: any = {
				...bucket,
				index,
				formattedTime: formatTimestamp(bucket.timestamp, data.bucket_size_seconds),
			};
			// Flatten by_model for easier chart access
			models.forEach((model) => {
				item[`model_${model}`] = bucket.by_model?.[model] || 0;
			});
			return item;
		});

		return { chartData: processed, displayModels: models };
	}, [data, selectedModel]);

	if (!data?.buckets || chartData.length === 0) {
		return <div className="text-muted-foreground flex h-full items-center justify-center text-sm">No data available</div>;
	}

	const commonProps = {
		data: chartData,
	};

	return (
		<ChartErrorBoundary resetKey={`${startTime}-${endTime}-${chartData.length}-${selectedModel}`}>
			<ResponsiveContainer width="100%" height="100%">
				{chartType === "bar" ? (
					<BarChart {...commonProps} barCategoryGap={1}>
						<CartesianGrid strokeDasharray="3 3" vertical={false} className="stroke-zinc-200 dark:stroke-zinc-700" />
						<XAxis
							dataKey="index"
							type="number"
							domain={[-0.5, chartData.length - 0.5]}
							tick={{ fontSize: 11, className: "fill-zinc-500", dy: 5 }}
							tickLine={false}
							axisLine={false}
							tickFormatter={(idx) => chartData[Math.round(idx)]?.formattedTime || ""}
							interval="preserveStartEnd"
						/>
						<YAxis
							tick={{ fontSize: 11, className: "fill-zinc-500" }}
							tickLine={false}
							axisLine={false}
							width={50}
							tickFormatter={(v) => formatCost(v)}
							domain={[0, (dataMax: number) => Math.max(dataMax, 0.01)]}
							allowDataOverflow={false}
						/>
						<Tooltip
							content={<CustomTooltip selectedModel={selectedModel} models={data.models} />}
							cursor={{ fill: "#8c8c8f", fillOpacity: 0.15 }}
						/>
						{displayModels.map((model, idx) => (
							<Bar
								key={model}
								dataKey={`model_${model}`}
								stackId="cost"
								fill={getModelColor(idx)}
								fillOpacity={0.9}
								barSize={30}
								radius={idx === displayModels.length - 1 ? [2, 2, 0, 0] : [0, 0, 0, 0]}
							/>
						))}
					</BarChart>
				) : (
					<AreaChart {...commonProps}>
						<CartesianGrid strokeDasharray="3 3" vertical={false} className="stroke-zinc-200 dark:stroke-zinc-700" />
						<XAxis
							dataKey="index"
							type="number"
							domain={[-0.5, chartData.length - 0.5]}
							tick={{ fontSize: 11, className: "fill-zinc-500" }}
							tickLine={false}
							axisLine={false}
							tickFormatter={(idx) => chartData[Math.round(idx)]?.formattedTime || ""}
							interval="preserveStartEnd"
						/>
						<YAxis
							tick={{ fontSize: 11, className: "fill-zinc-500" }}
							tickLine={false}
							axisLine={false}
							width={50}
							tickFormatter={(v) => formatCost(v)}
							domain={[0, (dataMax: number) => Math.max(dataMax, 0.01)]}
							allowDataOverflow={false}
						/>
						<Tooltip content={<CustomTooltip selectedModel={selectedModel} models={data.models} />} />
						{displayModels.map((model, idx) => (
							<Area
								key={model}
								type="monotone"
								dataKey={`model_${model}`}
								stackId="1"
								stroke={getModelColor(idx)}
								fill={getModelColor(idx)}
								fillOpacity={0.7}
							/>
						))}
					</AreaChart>
				)}
			</ResponsiveContainer>
		</ChartErrorBoundary>
	);
}
