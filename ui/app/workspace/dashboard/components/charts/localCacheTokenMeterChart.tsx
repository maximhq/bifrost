import type { LogStats } from "@/lib/types/logs";
import { useMemo } from "react";
import { Cell, Pie, PieChart, ResponsiveContainer } from "recharts";
import { ChartErrorBoundary } from "./chartErrorBoundary";
import { GaugeNeedle, getGaugeGeometry, useGaugeSize } from "./gaugeUtils";

interface LocalCacheTokenMeterChartProps {
	data: LogStats | null;
}

const METER_COLORS = { direct: "#06b6d4", semantic: "#8b5cf6", remaining: "#3b82f6" };

export default function LocalCacheTokenMeterChart({ data }: LocalCacheTokenMeterChartProps) {
	const { ref, width, height } = useGaugeSize();

	const { percentage, directHits, semanticHits, totalRequests, hasCacheCounters } = useMemo(() => {
		const hasCacheCounters = data?.direct_cache_hits != null && data?.semantic_cache_hits != null;
		const direct = data?.direct_cache_hits ?? 0;
		const semantic = data?.semantic_cache_hits ?? 0;
		const total = data?.cache_hit_rate_total_requests ?? data?.total_requests ?? 0;
		if (total === 0) {
			return { percentage: 0, directHits: direct, semanticHits: semantic, totalRequests: total, hasCacheCounters };
		}
		return {
			percentage: Math.max(0, Math.min(100, ((direct + semantic) / total) * 100)),
			directHits: direct,
			semanticHits: semantic,
			totalRequests: total,
			hasCacheCounters,
		};
	}, [data]);

	const gaugeGeometry = useMemo(() => getGaugeGeometry(width, height), [width, height]);
	const hasData = !!data && hasCacheCounters && totalRequests > 0;

	const rawDirectPct = totalRequests > 0 ? (directHits / totalRequests) * 100 : 0;
	const rawSemanticPct = totalRequests > 0 ? (semanticHits / totalRequests) * 100 : 0;
	const directPct = Math.max(0, Math.min(100, rawDirectPct));
	const semanticPct = Math.max(0, Math.min(100 - directPct, rawSemanticPct));
	const valueData = [
		{ name: "direct", value: directPct },
		{ name: "semantic", value: semanticPct },
		{ name: "remaining", value: Math.max(0, 100 - directPct - semanticPct) },
	];

	return (
		<ChartErrorBoundary resetKey={`${directHits}-${semanticHits}-${totalRequests}`}>
			<div className="grid h-full grid-rows-[104px_auto] items-start overflow-hidden pt-8">
				<div ref={ref} className="relative h-[104px] w-full">
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
