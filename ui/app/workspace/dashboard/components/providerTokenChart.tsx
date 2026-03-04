"use client";

import type { ProviderTokenHistogramResponse } from "@/lib/types/logs";
import { useMemo } from "react";
import { Area, AreaChart, Bar, BarChart, CartesianGrid, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts";
import { CHART_COLORS, formatFullTimestamp, formatTimestamp, formatTokens, getModelColor } from "../utils/chartUtils";
import { ChartErrorBoundary } from "./chartErrorBoundary";
import type { ChartType } from "./chartTypeToggle";

interface ProviderTokenChartProps {
	data: ProviderTokenHistogramResponse | null;
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
					const tokens = data.by_provider?.[provider]?.total_tokens || 0;
					if (tokens === 0) return null;
					return (
						<div key={provider} className="flex items-center justify-between gap-4">
							<span className="flex items-center gap-1.5">
								<span className="h-2 w-2 rounded-full" style={{ backgroundColor: getModelColor(idx) }} />
								<span className="max-w-[120px] truncate text-zinc-600 dark:text-zinc-400">{provider}</span>
							</span>
							<span className="font-medium">{formatTokens(tokens)}</span>
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

	const stats = data.by_provider?.[provider];
	if (!stats) return null;

	return (
		<div className="rounded-sm border border-zinc-200 bg-white px-3 py-2 shadow-lg dark:border-zinc-700 dark:bg-zinc-900">
			<div className="mb-1 text-xs text-zinc-500">{formatFullTimestamp(data.timestamp)}</div>
			<div className="space-y-1 text-sm">
				<div className="flex items-center justify-between gap-4">
					<span className="flex items-center gap-1.5">
						<span className="h-2 w-2 rounded-full" style={{ backgroundColor: CHART_COLORS.promptTokens }} />
						<span className="text-zinc-600 dark:text-zinc-400">Input</span>
					</span>
					<span className="font-medium">{formatTokens(stats.prompt_tokens || 0)}</span>
				</div>
				<div className="flex items-center justify-between gap-4">
					<span className="flex items-center gap-1.5">
						<span className="h-2 w-2 rounded-full" style={{ backgroundColor: CHART_COLORS.completionTokens }} />
						<span className="text-zinc-600 dark:text-zinc-400">Output</span>
					</span>
					<span className="font-medium">{formatTokens(stats.completion_tokens || 0)}</span>
				</div>
				<div className="flex items-center justify-between gap-4 border-t border-zinc-200 pt-1 dark:border-zinc-700">
					<span className="text-zinc-600 dark:text-zinc-400">Total</span>
					<span className="font-medium">{formatTokens(stats.total_tokens || 0)}</span>
				</div>
			</div>
		</div>
	);
}

export function ProviderTokenChart({ data, chartType, startTime, endTime, selectedProvider }: ProviderTokenChartProps) {
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
				item.prompt_tokens = stats?.prompt_tokens || 0;
				item.completion_tokens = stats?.completion_tokens || 0;
			} else {
				providers.forEach((provider, idx) => {
					item[`provider_${idx}`] = bucket.by_provider?.[provider]?.total_tokens || 0;
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
		margin: { top: 6, right: 4, left: -8, bottom: 0 },
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
							width={50}
							tickFormatter={formatTokens}
							domain={[0, (dataMax: number) => Math.max(dataMax, 1)]}
							allowDataOverflow={false}
						/>
						{mode === "single" ? (
							<>
								<Tooltip content={<SingleProviderTooltip provider={selectedProvider} />} cursor={{ fill: "#8c8c8f", fillOpacity: 0.15 }} />
								<Bar dataKey="prompt_tokens" stackId="tokens" fill={CHART_COLORS.promptTokens} fillOpacity={0.9} barSize={30} radius={[0, 0, 0, 0]} />
								<Bar dataKey="completion_tokens" stackId="tokens" fill={CHART_COLORS.completionTokens} fillOpacity={0.9} barSize={30} radius={[2, 2, 0, 0]} />
							</>
						) : (
							<>
								<Tooltip content={<AllProvidersTooltip providers={data.providers} />} cursor={{ fill: "#8c8c8f", fillOpacity: 0.15 }} />
								{displayProviders.map((provider, idx) => (
									<Bar
										key={provider}
										dataKey={`provider_${idx}`}
										stackId="tokens"
										fill={getModelColor(idx)}
										fillOpacity={0.9}
										barSize={30}
										radius={idx === displayProviders.length - 1 ? [2, 2, 0, 0] : [0, 0, 0, 0]}
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
							width={50}
							tickFormatter={formatTokens}
							domain={[0, (dataMax: number) => Math.max(dataMax, 1)]}
							allowDataOverflow={false}
						/>
						{mode === "single" ? (
							<>
								<Tooltip content={<SingleProviderTooltip provider={selectedProvider} />} />
								<Area type="monotone" dataKey="completion_tokens" stackId="1" stroke={CHART_COLORS.completionTokens} fill={CHART_COLORS.completionTokens} fillOpacity={0.7} />
								<Area type="monotone" dataKey="prompt_tokens" stackId="1" stroke={CHART_COLORS.promptTokens} fill={CHART_COLORS.promptTokens} fillOpacity={0.7} />
							</>
						) : (
							<>
								<Tooltip content={<AllProvidersTooltip providers={data.providers} />} />
								{displayProviders.map((provider, idx) => (
									<Area
										key={provider}
										type="monotone"
										dataKey={`provider_${idx}`}
										stackId="1"
										stroke={getModelColor(idx)}
										fill={getModelColor(idx)}
										fillOpacity={0.7}
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
