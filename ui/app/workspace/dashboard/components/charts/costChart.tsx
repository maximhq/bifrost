import type { CostHistogramResponse } from "@/lib/types/logs";
import { memo, useMemo } from "react";
import { Area, AreaChart, Bar, BarChart, CartesianGrid, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts";
import {
	formatCost,
	formatFullTimestamp,
	formatTimestamp,
	getModelColor,
	OTHER_SERIES_COLOR,
	OTHER_SERIES_KEY,
	OTHER_SERIES_LABEL,
	pickTopSeries,
} from "../../utils/chartUtils";
import { ChartErrorBoundary } from "./chartErrorBoundary";
import type { ChartType } from "./chartTypeToggle";

interface CostChartProps {
	data: CostHistogramResponse | null;
	chartType: ChartType;
	startTime: number;
	endTime: number;
	selectedModel: string;
}

function CustomTooltip({ active, payload, selectedModel, displayModels }: any) {
	if (!active || !payload || !payload.length) return null;

	const data = payload[0]?.payload;
	if (!data) return null;

	return (
		<div className="rounded-sm border border-zinc-200 bg-white px-3 py-2 shadow-lg dark:border-zinc-700 dark:bg-zinc-900">
			<div className="mb-1 text-xs text-zinc-500">{formatFullTimestamp(data.timestamp)}</div>
			<div className="space-y-1 text-sm">
				{selectedModel === "all" ? (
					<>
						{displayModels.map((model: string, idx: number) => {
							const isOther = model === OTHER_SERIES_KEY;
							const cost = isOther ? (data[OTHER_SERIES_KEY] ?? 0) : (data.by_model?.[model] || 0);
							if (cost === 0) return null;
							return (
								<div key={model} className="flex items-center justify-between gap-4">
									<span className="flex items-center gap-1.5">
										<span
											className="h-2 w-2 rounded-full"
											style={{ backgroundColor: isOther ? OTHER_SERIES_COLOR : getModelColor(idx) }}
										/>
										<span className="max-w-[120px] truncate text-zinc-600 dark:text-zinc-400">
											{isOther ? OTHER_SERIES_LABEL : model}
										</span>
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

function CostChartImpl({ data, chartType, startTime, endTime, selectedModel }: CostChartProps) {
	const { chartData, displayModels } = useMemo(() => {
		if (!data?.buckets || !data.bucket_size_seconds) {
			return { chartData: [], displayModels: [] };
		}

		let models: string[];
		let hasOther = false;
		if (selectedModel === "all") {
			const top = pickTopSeries(data.buckets, data.models, (b, m) => b.by_model?.[m] ?? 0);
			hasOther = top.length < data.models.length;
			models = hasOther ? [...top, OTHER_SERIES_KEY] : top;
		} else {
			models = [selectedModel];
		}
		const topSet = new Set(models);

		const processed = data.buckets.map((bucket, index) => {
			const item: any = {
				...bucket,
				index,
				formattedTime: formatTimestamp(bucket.timestamp, data.bucket_size_seconds),
			};
			if (hasOther && bucket.by_model) {
				let otherSum = 0;
				for (const model of data.models) {
					if (!topSet.has(model)) otherSum += bucket.by_model[model] ?? 0;
				}
				item[OTHER_SERIES_KEY] = otherSum;
			}
			models.forEach((model, idx) => {
				item[`model_${idx}`] = model === OTHER_SERIES_KEY ? (item[OTHER_SERIES_KEY] ?? 0) : (bucket.by_model?.[model] ?? 0);
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
		margin: { top: 6, right: 4, left: 4, bottom: 0 },
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
						<Tooltip content={<CustomTooltip selectedModel={selectedModel} displayModels={displayModels} />} />
						{displayModels.map((model, idx) => (
							<Bar
								isAnimationActive={false}
								key={model}
								dataKey={`model_${idx}`}
								stackId="cost"
								fill={model === OTHER_SERIES_KEY ? OTHER_SERIES_COLOR : getModelColor(idx)}
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
						<Tooltip content={<CustomTooltip selectedModel={selectedModel} displayModels={displayModels} />} />
						{displayModels.map((model, idx) => {
							const color = model === OTHER_SERIES_KEY ? OTHER_SERIES_COLOR : getModelColor(idx);
							return (
								<Area
									isAnimationActive={false}
									key={model}
									type="monotone"
									dataKey={`model_${idx}`}
									stackId="1"
									stroke={color}
									fill={color}
									fillOpacity={0.7}
								/>
							);
						})}
					</AreaChart>
				)}
			</ResponsiveContainer>
		</ChartErrorBoundary>
	);
}

export const CostChart = memo(CostChartImpl);