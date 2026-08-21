import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import type { LogStats } from "@/lib/types/logs";
import { clampPercentage, resolveLocalCacheGauge } from "@/lib/utils/cacheGauge";
import { formatCompactNumber } from "@/lib/utils/numbers";
import { Info } from "lucide-react";
import { memo, useMemo } from "react";
import { Cell, Pie, PieChart, ResponsiveContainer } from "recharts";
import { ChartErrorBoundary } from "./chartErrorBoundary";
import { GaugeNeedle, getGaugeGeometry, useGaugeSize } from "./gaugeUtils";

interface LocalCacheTokenMeterChartProps {
	data: LogStats | null;
}

const METER_COLORS = { direct: "#06b6d4", semantic: "#8b5cf6", remaining: "#3b82f6" };

function LocalCacheTokenMeterChartImpl({ data }: LocalCacheTokenMeterChartProps) {
	const { ref, width, height } = useGaugeSize();

	const { state, percentage, directHits, semanticHits, totalRequests } = useMemo(() => resolveLocalCacheGauge(data), [data]);

	const gaugeGeometry = useMemo(() => getGaugeGeometry(width, height), [width, height]);
	const hasData = state === "ready";

	const directPct = totalRequests > 0 ? clampPercentage((directHits / totalRequests) * 100) : 0;
	const semanticPct = totalRequests > 0 ? clampPercentage((semanticHits / totalRequests) * 100, 100 - directPct) : 0;
	const valueData = [
		{ name: "direct", value: directPct },
		{ name: "semantic", value: semanticPct },
		{ name: "remaining", value: Math.max(0, 100 - directPct - semanticPct) },
	];

	return (
		<ChartErrorBoundary resetKey={`${state}-${directHits}-${semanticHits}-${totalRequests}`}>
			<div className="grid h-full grid-rows-[104px_auto] items-start overflow-hidden pt-8">
				<div ref={ref} className="relative h-[104px] w-full">
					{state === "no-data" && (
						<div className="text-muted-foreground flex h-full items-center justify-center text-sm">No data available</div>
					)}
					{state === "not-engaged" && (
						<div
							className="text-muted-foreground flex h-full flex-col items-center justify-center gap-1 text-center text-sm"
							data-testid="local-cache-meter-not-engaged"
						>
							<span>Cache not engaged</span>
							<span className="flex items-center gap-1 text-[11px] text-zinc-400">
								<span>{formatCompactNumber(totalRequests)} requests, none used the cache</span>
								<Tooltip>
									<TooltipTrigger asChild>
										<button
											type="button"
											data-testid="local-cache-meter-not-engaged-info-btn"
											className="text-zinc-500 transition-colors hover:text-zinc-300"
											aria-label="Why the local cache was not engaged"
										>
											<Info className="h-3 w-3" />
										</button>
									</TooltipTrigger>
									<TooltipContent side="top">
										Requests bypass the cache unless they carry an x-bf-cache-key header, or the semantic cache plugin sets a
										default_cache_key.
									</TooltipContent>
								</Tooltip>
							</span>
						</div>
					)}
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
										<Cell fill={METER_COLORS.direct} />
										<Cell fill={METER_COLORS.semantic} />
										<Cell fill={METER_COLORS.remaining} opacity={0.22} />
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
						<div className="flex flex-col items-center pt-1 leading-none">
							<div className="text-muted-foreground text-3xl font-semibold tracking-tight">{percentage.toFixed(1)}%</div>
							<div className="mt-1 text-[11px] text-zinc-400">of requests served from local cache</div>
						</div>
						<div className="flex flex-wrap items-center justify-center gap-x-4 gap-y-1 pt-2 text-[11px] leading-none">
							<span className="flex items-center gap-1.5">
								<span className="h-2 w-2 rounded-full" style={{ backgroundColor: METER_COLORS.direct }} />
								<span className="text-primary">Direct: {directHits}</span>
							</span>
							<span className="flex items-center gap-1.5">
								<span className="h-2 w-2 rounded-full" style={{ backgroundColor: METER_COLORS.semantic }} />
								<span className="text-primary">Semantic: {semanticHits}</span>
							</span>
						</div>
					</div>
				)}
			</div>
		</ChartErrorBoundary>
	);
}

export default memo(LocalCacheTokenMeterChartImpl);