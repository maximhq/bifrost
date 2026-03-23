"use client";

import type { ProviderLatencyHistogramResponse } from "@/lib/types/logs";
import { useMemo } from "react";
import { Area, AreaChart, Bar, BarChart, CartesianGrid, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts";
import { formatFullTimestamp, formatLatency, formatTimestamp, getModelColor, LATENCY_COLORS } from "../../utils/chartUtils";
import { ChartErrorBoundary } from "./chartErrorBoundary";
import type { ChartType } from "./chartTypeToggle";

interface ProviderLatencyChartProps {
	data: ProviderLatencyHistogramResponse | null;
	chartType: ChartType;
	startTime: number;
	endTime: number;
	selectedProvider: string;
}

function AllProvidersTooltip({ active, payload, providers }: any) {
	if (!active || !payload || !payload.length) return null;

	const data = payload[0]?.payload;
	if (!data) return null;

	return (
		<div className="rounded-sm border border-zinc-200 bg-white px-3 py-2 shadow-lg dark:border-zinc-700 dark:bg-zinc-900">
			<div className="mb-1 text-xs text-zinc-500">{formatFullTimestamp(data.timestamp)}</div>
			<div className="space-y-1 text-sm">
				{providers.map((provider: string, idx: number) => {
					const stats = data.by_provider?.[provider];
					if (!stats || stats.avg_latency === 0) return null;
					return (
						<div key={provider} className="flex items-center justify-between gap-4">
							<span className="flex items-center gap-1.5">
								<span className="h-2 w-2 rounded-full" style={{ backgroundColor: getModelColor(idx) }} />
								<span className="max-w-[120px] truncate text-zinc-600 dark:text-zinc-400">{provider}</span>
							</span>
							<span className="font-medium">{formatLatency(stats.avg_latency)}</span>
						</div>
					);
				})}
			</div>
		</div>
	);
}

function SingleProviderTooltip({ active, payload, provider }: any) {
	if (!active || !payload || !payload.length) return null;

	const data = payload[0]?.payload;
	if (!data) return null;

	return (
		<div className="rounded-sm border border-zinc-200 bg-white px-3 py-2 shadow-lg dark:border-zinc-700 dark:bg-zinc-900">
			<div className="mb-1 text-xs text-zinc-500">{formatFullTimestamp(data.timestamp)}</div>
			<div className="space-y-1 text-sm">
				<div className="flex items-center justify-between gap-4">
					<span className="flex items-center gap-1.5">
						<span className="h-2 w-2 rounded-full" style={{ backgroundColor: LATENCY_COLORS.avg }} />
						<span className="text-zinc-600 dark:text-zinc-400">Avg</span>
					</span>
					<span className="font-medium">{formatLatency(data.avg_latency)}</span>
				</div>
				<div className="flex items-center justify-between gap-4">
					<span className="flex items-center gap-1.5">
						<span className="h-2 w-2 rounded-full" style={{ backgroundColor: LATENCY_COLORS.p90 }} />
						<span className="text-zinc-600 dark:text-zinc-400">P90</span>
					</span>
					<span className="font-medium">{formatLatency(data.p90_latency)}</span>
				</div>
				<div className="flex items-center justify-between gap-4">
					<span className="flex items-center gap-1.5">
						<span className="h-2 w-2 rounded-full" style={{ backgroundColor: LATENCY_COLORS.p95 }} />
						<span className="text-zinc-600 dark:text-zinc-400">P95</span>
					</span>
					<span className="font-medium">{formatLatency(data.p95_latency)}</span>
				</div>
				<div className="flex items-center justify-between gap-4">
					<span className="flex items-center gap-1.5">
						<span className="h-2 w-2 rounded-full" style={{ backgroundColor: LATENCY_COLORS.p99 }} />
						<span className="text-zinc-600 dark:text-zinc-400">P99</span>
					</span>
					<span className="font-medium">{formatLatency(data.p99_latency)}</span>
				</div>
				<div className="flex items-center justify-between gap-4 border-t border-zinc-200 pt-1 dark:border-zinc-700">
					<span className="text-zinc-600 dark:text-zinc-400">Requests</span>
					<span className="font-medium">{data.total_requests?.toLocaleString() || 0}</span>
				</div>
			</div>
		</div>
	);
}

export function ProviderLatencyChart({ data, chartType, startTime, endTime, selectedProvider }: ProviderLatencyChartProps) {
	const { chartData, mode, displayProviders } = useMemo(() => {
		if (!data?.buckets || !data.bucket_size_seconds) {
			return { chartData: [], mode: "all" as const, displayProviders: [] };
		}

		const isSingleProvider = selectedProvider !== "all";
		const providers = isSingleProvider ? [selectedProvider] : data.providers;

		const processed = data.buckets.map((bucket, index) => {
			const item: any = {
				...bucket,
				index,
				formattedTime: formatTimestamp(bucket.timestamp, data.bucket_size_seconds),
			};

			if (isSingleProvider) {
				const stats = bucket.by_provider?.[selectedProvider];
				item.avg_latency = stats?.avg_latency || 0;
				item.p90_latency = stats?.p90_latency || 0;
				item.p95_latency = stats?.p95_latency || 0;
				item.p99_latency = stats?.p99_latency || 0;
				item.total_requests = stats?.total_requests || 0;
			} else {
				providers.forEach((provider, idx) => {
					item[`provider_${idx}`] = bucket.by_provider?.[provider]?.avg_latency || 0;
				});
			}

			return item;
		});

		return { chartData: processed, mode: isSingleProvider ? ("single" as const) : ("all" as const), displayProviders: providers };
	}, [data, selectedProvider]);

	if (!data?.buckets || chartData.length === 0) {
		return <div className="text-muted-foreground flex h-full items-center justify-center text-sm">No data available</div>;
	}

	const commonProps = {
		data: chartData,
		margin: { top: 6, right: 4, left: 4, bottom: 0 },
	};

	return (
		<ChartErrorBoundary resetKey={`${startTime}-${endTime}-${chartData.length}-${selectedProvider}`}>
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
							width={55}
							tickFormatter={formatLatency}
							domain={[0, (dataMax: number) => Math.max(dataMax, 1)]}
							allowDataOverflow={false}
						/>
						{mode === "single" ? (
							<>
								<Tooltip content={<SingleProviderTooltip provider={selectedProvider} />} cursor={{ fill: "#8c8c8f", fillOpacity: 0.15 }} />
								<Bar
									isAnimationActive={false}
									dataKey="avg_latency"
									fill={LATENCY_COLORS.avg}
									fillOpacity={0.9}
									barSize={8}
									radius={[2, 2, 0, 0]}
								/>
								<Bar
									isAnimationActive={false}
									dataKey="p90_latency"
									fill={LATENCY_COLORS.p90}
									fillOpacity={0.9}
									barSize={8}
									radius={[2, 2, 0, 0]}
								/>
								<Bar
									isAnimationActive={false}
									dataKey="p95_latency"
									fill={LATENCY_COLORS.p95}
									fillOpacity={0.9}
									barSize={8}
									radius={[2, 2, 0, 0]}
								/>
								<Bar
									isAnimationActive={false}
									dataKey="p99_latency"
									fill={LATENCY_COLORS.p99}
									fillOpacity={0.9}
									barSize={8}
									radius={[2, 2, 0, 0]}
								/>
							</>
						) : (
							<>
								<Tooltip content={<AllProvidersTooltip providers={data.providers} />} cursor={{ fill: "#8c8c8f", fillOpacity: 0.15 }} />
								{displayProviders.map((provider, idx) => (
									<Bar
										key={provider}
										dataKey={`provider_${idx}`}
										fill={getModelColor(idx)}
										isAnimationActive={false}
										fillOpacity={0.9}
										barSize={8}
										radius={[2, 2, 0, 0]}
									/>
								))}
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
							width={55}
							tickFormatter={formatLatency}
							domain={[0, (dataMax: number) => Math.max(dataMax, 1)]}
							allowDataOverflow={false}
						/>
						{mode === "single" ? (
							<>
								<Tooltip content={<SingleProviderTooltip provider={selectedProvider} />} />
								<Area
									isAnimationActive={false}
									type="monotone"
									dataKey="p99_latency"
									stroke={LATENCY_COLORS.p99}
									fill={LATENCY_COLORS.p99}
									fillOpacity={0.15}
								/>
								<Area
									isAnimationActive={false}
									type="monotone"
									dataKey="p95_latency"
									stroke={LATENCY_COLORS.p95}
									fill={LATENCY_COLORS.p95}
									fillOpacity={0.2}
								/>
								<Area
									isAnimationActive={false}
									type="monotone"
									dataKey="p90_latency"
									stroke={LATENCY_COLORS.p90}
									fill={LATENCY_COLORS.p90}
									fillOpacity={0.25}
								/>
								<Area
									isAnimationActive={false}
									type="monotone"
									dataKey="avg_latency"
									stroke={LATENCY_COLORS.avg}
									fill={LATENCY_COLORS.avg}
									fillOpacity={0.4}
								/>
							</>
						) : (
							<>
								<Tooltip content={<AllProvidersTooltip providers={data.providers} />} />
								{displayProviders.map((provider, idx) => (
									<Area
										key={provider}
										type="monotone"
										isAnimationActive={false}
										dataKey={`provider_${idx}`}
										stroke={getModelColor(idx)}
										fill={getModelColor(idx)}
										fillOpacity={0.3}
									/>
								))}
							</>
						)}
					</AreaChart>
				)}
			</ResponsiveContainer>
		</ChartErrorBoundary>
	);
}
