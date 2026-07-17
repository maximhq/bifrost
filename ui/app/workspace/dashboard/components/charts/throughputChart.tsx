import type { ThroughputHistogramResponse } from "@/lib/types/logs";
import { memo, useMemo } from "react";
import { Area, AreaChart, Bar, BarChart, CartesianGrid, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts";
import { formatFullTimestamp, formatTimestamp, formatTokensPerSecond, THROUGHPUT_COLOR } from "../../utils/chartUtils";
import { ChartErrorBoundary } from "./chartErrorBoundary";
import type { ChartType } from "./chartTypeToggle";

interface ThroughputChartProps {
	data: ThroughputHistogramResponse | null;
	chartType: ChartType;
	startTime: number;
	endTime: number;
}

function CustomTooltip({ active, payload }: any) {
	if (!active || !payload || !payload.length) return null;

	const data = payload[0]?.payload;
	if (!data) return null;

	return (
		<div className="rounded-sm border border-zinc-200 bg-white px-3 py-2 shadow-lg dark:border-zinc-700 dark:bg-zinc-900">
			<div className="mb-1 text-xs text-zinc-500">{formatFullTimestamp(data.timestamp)}</div>
			<div className="space-y-1 text-sm">
				<div className="flex items-center justify-between gap-4">
					<span className="flex items-center gap-1.5">
						<span className="h-2 w-2 rounded-full" style={{ backgroundColor: THROUGHPUT_COLOR }} />
						<span className="text-zinc-600 dark:text-zinc-400">Throughput</span>
					</span>
					<span className="font-medium">{formatTokensPerSecond(data.tokens_per_second)}</span>
				</div>
				<div className="flex items-center justify-between gap-4">
					<span className="text-zinc-600 dark:text-zinc-400">Completion tokens</span>
					<span className="font-medium">{data.total_completion_tokens.toLocaleString()}</span>
				</div>
				<div className="flex items-center justify-between gap-4 border-t border-zinc-200 pt-1 dark:border-zinc-700">
					<span className="text-zinc-600 dark:text-zinc-400">Requests</span>
					<span className="font-medium">{data.total_requests.toLocaleString()}</span>
				</div>
			</div>
		</div>
	);
}

function ThroughputChartImpl({ data, chartType, startTime, endTime }: ThroughputChartProps) {
	const chartData = useMemo(() => {
		if (!data?.buckets || !data.bucket_size_seconds) {
			return [];
		}

		return data.buckets.map((bucket, index) => ({
			...bucket,
			index,
			formattedTime: formatTimestamp(bucket.timestamp, data.bucket_size_seconds),
		}));
	}, [data]);

	if (!data?.buckets || chartData.length === 0) {
		return <div className="text-muted-foreground flex h-full items-center justify-center text-sm">No data available</div>;
	}

	const commonProps = {
		data: chartData,
		margin: { top: 6, right: 4, left: 4, bottom: 0 },
	};

	return (
		<ChartErrorBoundary resetKey={`${startTime}-${endTime}-${chartData.length}`}>
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
							width={70}
							tickFormatter={formatTokensPerSecond}
							domain={[0, (dataMax: number) => Math.max(dataMax, 1)]}
							allowDataOverflow={false}
						/>
						<Tooltip content={<CustomTooltip />} cursor={{ fill: "#8c8c8f", fillOpacity: 0.15 }} />
						<Bar
							isAnimationActive={false}
							dataKey="tokens_per_second"
							fill={THROUGHPUT_COLOR}
							fillOpacity={0.9}
							barSize={8}
							radius={[2, 2, 0, 0]}
						/>
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
							width={70}
							tickFormatter={formatTokensPerSecond}
							domain={[0, (dataMax: number) => Math.max(dataMax, 1)]}
							allowDataOverflow={false}
						/>
						<Tooltip content={<CustomTooltip />} cursor={{ fill: "#8c8c8f", fillOpacity: 0.15 }} />
						<Area
							isAnimationActive={false}
							type="monotone"
							dataKey="tokens_per_second"
							stroke={THROUGHPUT_COLOR}
							fill={THROUGHPUT_COLOR}
							fillOpacity={0.4}
						/>
					</AreaChart>
				)}
			</ResponsiveContainer>
		</ChartErrorBoundary>
	);
}
export const ThroughputChart = memo(ThroughputChartImpl);
