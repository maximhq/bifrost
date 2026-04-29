import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import type { TokenHistogramResponse } from "@/lib/types/logs";
import { Info } from "lucide-react";
import { useMemo } from "react";
import { Cell, Pie, PieChart, ResponsiveContainer } from "recharts";
import { ChartErrorBoundary } from "./chartErrorBoundary";
import { GaugeNeedle, getGaugeGeometry, useGaugeSize } from "./gaugeUtils";

interface ExternalCacheTokenMeterChartProps {
	data: TokenHistogramResponse | null;
}

const METER_COLORS = { cached: "#06b6d4", input: "#3b82f6" };

const formatTokenCount = (count: number): string => {
	if (count >= 1000000) return `${(count / 1000000).toFixed(1)}M`;
	if (count >= 1000) return `${(count / 1000).toFixed(1)}K`;
	return count.toLocaleString();
};

export default function ExternalCacheTokenMeterChart({ data }: ExternalCacheTokenMeterChartProps) {
	const { ref, width, height } = useGaugeSize();

	const { percentage, totalCachedRead, totalPromptTokens } = useMemo(() => {
		if (!data?.buckets || data.buckets.length === 0) return { percentage: 0, totalCachedRead: 0, totalPromptTokens: 0 };
		let cachedRead = 0;
		let promptTokens = 0;
		for (const bucket of data.buckets) {
			cachedRead += bucket.cached_read_tokens;
			promptTokens += bucket.prompt_tokens;
		}
		if (promptTokens === 0) return { percentage: 0, totalCachedRead: cachedRead, totalPromptTokens: promptTokens };
		return {
			percentage: Math.max(0, Math.min(100, (cachedRead / promptTokens) * 100)),
			totalCachedRead: cachedRead,
			totalPromptTokens: promptTokens,
		};
	}, [data]);

	const gaugeGeometry = useMemo(() => getGaugeGeometry(width, height), [width, height]);
	const hasData = !!data?.buckets && data.buckets.length > 0 && totalPromptTokens > 0;
	const valueData = [
		{ name: "cached", value: percentage },
		{ name: "remaining", value: 100 - percentage },
	];

	return (
		<ChartErrorBoundary resetKey={`${data?.buckets?.length ?? 0}-${totalCachedRead}-${totalPromptTokens}`}>
			<div className="grid h-full grid-rows-[104px_auto] items-start overflow-hidden pt-8">
				<div ref={ref} className="relative grow h-full w-full">
					{!hasData && <div className="text-muted-foreground flex h-full items-center justify-center text-sm">No data available</div>}
					{hasData && gaugeGeometry && (
						<>
							<ResponsiveContainer width="100%" height="100%">
								<PieChart>
									<Pie
										data={valueData}
										cx={gaugeGeometry.cx}
										cy={gaugeGeometry.cy}
										startAngle={180}
										endAngle={0}
										innerRadius={gaugeGeometry.innerRadius}
										outerRadius={gaugeGeometry.outerRadius}
										dataKey="value"
										stroke="none"
										isAnimationActive={false}
									>
										<Cell fill={METER_COLORS.cached} />
										<Cell fill={METER_COLORS.input} opacity={0.22} />
									</Pie>
								</PieChart>
							</ResponsiveContainer>
							<svg className="pointer-events-none absolute inset-0" viewBox={`0 0 ${width} ${height}`} aria-hidden="true">
								<GaugeNeedle percentage={percentage} geometry={gaugeGeometry} />
							</svg>
						</>
					)}
				</div>

				{hasData && (
					<div>
						<div className="flex flex-col items-center pt-1 leading-none shrink-0">
							<div className="text-muted-foreground text-3xl font-semibold tracking-tight">{percentage.toFixed(1)}%</div>
							<div className="mt-1 flex items-center gap-1 text-[11px] text-zinc-400">
								<span>of input tokens cached by provider</span>
								<Tooltip>
									<TooltipTrigger asChild>
										<button
											type="button"
											data-testid="external-cache-meter-info-btn"
											className="text-zinc-500 transition-colors hover:text-zinc-300"
											aria-label="More information about external cache hit rate"
										>
											<Info className="h-3 w-3" />
										</button>
									</TooltipTrigger>
									<TooltipContent side="top">This reflects provider-level caching, not Bifrost semantic cache hits.</TooltipContent>
								</Tooltip>
							</div>
						</div>
						<div className="flex flex-wrap items-center justify-center gap-x-4 gap-y-1 pt-2 text-[11px] leading-none shrink-0">
							<span className="flex items-center gap-1.5">
								<span className="h-2 w-2 rounded-full" style={{ backgroundColor: METER_COLORS.cached }} />
								<span className="text-primary">Cached: {formatTokenCount(totalCachedRead)}</span>
							</span>
							<span className="flex items-center gap-1.5">
								<span className="h-2 w-2 rounded-full" style={{ backgroundColor: METER_COLORS.input }} />
								<span className="text-muted-foreground">Input: {formatTokenCount(totalPromptTokens)}</span>
							</span>
						</div>
					</div>
				)}
			</div>
		</ChartErrorBoundary>
	);
}
