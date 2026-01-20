"use client";

import type { ModelHistogramResponse } from "@/lib/types/logs";
import { useMemo } from "react";
import { Area, AreaChart, Bar, BarChart, CartesianGrid, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts";
import { CHART_COLORS, formatFullTimestamp, formatTimestamp, getModelColor } from "../utils/chartUtils";
import { ChartErrorBoundary } from "./chartErrorBoundary";
import type { ChartType } from "./chartTypeToggle";

interface ModelUsageChartProps {
	data: ModelHistogramResponse | null;
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
							const stats = data.by_model?.[model];
							if (!stats || stats.total === 0) return null;
							return (
								<div key={model} className="flex items-center justify-between gap-4">
									<span className="flex items-center gap-1.5">
										<span className="h-2 w-2 rounded-full" style={{ backgroundColor: getModelColor(idx) }} />
										<span className="max-w-[120px] truncate text-zinc-600 dark:text-zinc-400">{model}</span>
									</span>
									<span className="font-medium">{stats.total.toLocaleString()}</span>
								</div>
							);
						})}
					</>
				) : (
					<>
						<div className="flex items-center justify-between gap-4">
							<span className="flex items-center gap-1.5">
								<span className="h-2 w-2 rounded-full bg-emerald-500" />
								<span className="text-zinc-600 dark:text-zinc-400">Success</span>
							</span>
							<span className="font-medium text-emerald-600 dark:text-emerald-400">
								{(data.by_model?.[selectedModel]?.success || 0).toLocaleString()}
							</span>
						</div>
						<div className="flex items-center justify-between gap-4">
							<span className="flex items-center gap-1.5">
								<span className="h-2 w-2 rounded-full bg-red-500" />
								<span className="text-zinc-600 dark:text-zinc-400">Error</span>
							</span>
							<span className="font-medium text-red-600 dark:text-red-400">
								{(data.by_model?.[selectedModel]?.error || 0).toLocaleString()}
							</span>
						</div>
					</>
				)}
			</div>
		</div>
	);
}

export function ModelUsageChart({ data, chartType, startTime, endTime, selectedModel }: ModelUsageChartProps) {
	const { chartData, displayModels } = useMemo(() => {
		if (!data?.buckets || !data.bucket_size_seconds) {
			return { chartData: [], displayModels: [] };
		}

		const models = data.models;

		const processed = data.buckets.map((bucket, index) => {
			const item: any = {
				...bucket,
				index,
				formattedTime: formatTimestamp(bucket.timestamp, data.bucket_size_seconds),
			};

			if (selectedModel === "all") {
				// For "all", show total per model
				models.forEach((model) => {
					item[`model_${model}`] = bucket.by_model?.[model]?.total || 0;
				});
			} else {
				// For specific model, show success/error breakdown
				const stats = bucket.by_model?.[selectedModel];
				item.success = stats?.success || 0;
				item.error = stats?.error || 0;
			}
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
							width={40}
							tickFormatter={(v) => v.toLocaleString()}
							domain={[0, (dataMax: number) => Math.max(dataMax, 1)]}
							allowDataOverflow={false}
						/>
						<Tooltip
							content={<CustomTooltip selectedModel={selectedModel} models={displayModels} />}
							cursor={{ fill: "#8c8c8f", fillOpacity: 0.15 }}
						/>
						{selectedModel === "all" ? (
							displayModels.map((model, idx) => (
								<Bar
									key={model}
									dataKey={`model_${model}`}
									stackId="models"
									fill={getModelColor(idx)}
									fillOpacity={0.9}
									barSize={30}
									radius={idx === displayModels.length - 1 ? [2, 2, 0, 0] : [0, 0, 0, 0]}
								/>
							))
						) : (
							<>
								<Bar dataKey="success" stackId="status" fill={CHART_COLORS.success} fillOpacity={0.9} radius={[0, 0, 0, 0]} barSize={30} />
								<Bar dataKey="error" stackId="status" fill={CHART_COLORS.error} fillOpacity={0.9} radius={[2, 2, 0, 0]} barSize={30} />
							</>
						)}
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
							width={40}
							tickFormatter={(v) => v.toLocaleString()}
							domain={[0, (dataMax: number) => Math.max(dataMax, 1)]}
							allowDataOverflow={false}
						/>
						<Tooltip content={<CustomTooltip selectedModel={selectedModel} models={displayModels} />} />
						{selectedModel === "all" ? (
							displayModels.map((model, idx) => (
								<Area
									key={model}
									type="monotone"
									dataKey={`model_${model}`}
									stackId="1"
									stroke={getModelColor(idx)}
									fill={getModelColor(idx)}
									fillOpacity={0.7}
								/>
							))
						) : (
							<>
								<Area
									type="monotone"
									dataKey="success"
									stackId="1"
									stroke={CHART_COLORS.success}
									fill={CHART_COLORS.success}
									fillOpacity={0.7}
								/>
								<Area type="monotone" dataKey="error" stackId="1" stroke={CHART_COLORS.error} fill={CHART_COLORS.error} fillOpacity={0.7} />
							</>
						)}
					</AreaChart>
				)}
			</ResponsiveContainer>
		</ChartErrorBoundary>
	);
}
