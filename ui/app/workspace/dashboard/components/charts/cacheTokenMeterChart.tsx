import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import type { TokenHistogramResponse } from "@/lib/types/logs";
import { Info } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import { Cell, Pie, PieChart, ResponsiveContainer } from "recharts";
import { ChartErrorBoundary } from "./chartErrorBoundary";

interface CacheTokenMeterChartProps {
	data: TokenHistogramResponse | null;
}

const METER_COLORS = {
	cached: "#06b6d4",
	input: "#3b82f6",
};

const formatTokenCount = (count: number): string => {
	if (count >= 1000000) return `${(count / 1000000).toFixed(1)}M`;
	if (count >= 1000) return `${(count / 1000).toFixed(1)}K`;
	return count.toLocaleString();
};

interface GaugeGeometry {
	cx: number;
	cy: number;
	innerRadius: number;
	outerRadius: number;
}

function Needle({ percentage, geometry }: { percentage: number; geometry: GaugeGeometry }) {
	const normalizedPercentage = Math.max(0, Math.min(percentage, 100));
	const { cx, cy, outerRadius } = geometry;

	const angle = 180 - (normalizedPercentage / 100) * 180;
	const rad = (Math.PI * angle) / 180;
	const needleLen = outerRadius * 0.94;

	const tipX = cx + needleLen * Math.cos(rad);
	const tipY = cy - needleLen * Math.sin(rad);

	const perpRad = rad + Math.PI / 2;
	const hw = 3.5;
	const bx1 = cx + hw * Math.cos(perpRad);
	const by1 = cy - hw * Math.sin(perpRad);
	const bx2 = cx - hw * Math.cos(perpRad);
	const by2 = cy + hw * Math.sin(perpRad);

	return (
		<g>
			<path d={`M ${bx1} ${by1} L ${tipX} ${tipY} L ${bx2} ${by2} Z`} fill="#71717a" />
			<circle cx={cx} cy={cy} r={5} fill="#71717a" />
		</g>
	);
}

export default function CacheTokenMeterChart({ data }: CacheTokenMeterChartProps) {
	const chartContainerRef = useRef<HTMLDivElement | null>(null);
	const [{ width, height }, setChartSize] = useState({ width: 0, height: 0 });

	useEffect(() => {
		const node = chartContainerRef.current;
		if (!node) return;

		const updateSize = () => {
			const nextWidth = node.clientWidth;
			const nextHeight = node.clientHeight;
			setChartSize((current) =>
				current.width === nextWidth && current.height === nextHeight ? current : { width: nextWidth, height: nextHeight },
			);
		};

		updateSize();

		const resizeObserver = new ResizeObserver(updateSize);
		resizeObserver.observe(node);

		return () => resizeObserver.disconnect();
	}, []);

	const { percentage, totalCachedRead, totalPromptTokens } = useMemo(() => {
		if (!data?.buckets || data.buckets.length === 0) {
			return { percentage: 0, totalCachedRead: 0, totalPromptTokens: 0 };
		}

		let cachedRead = 0;
		let promptTokens = 0;

		for (const bucket of data.buckets) {
			cachedRead += bucket.cached_read_tokens;
			promptTokens += bucket.prompt_tokens;
		}

		if (promptTokens === 0) {
			return { percentage: 0, totalCachedRead: cachedRead, totalPromptTokens: promptTokens };
		}
		return {
			percentage: Math.max(0, Math.min(100, (cachedRead / promptTokens) * 100)),
			totalCachedRead: cachedRead,
			totalPromptTokens: promptTokens,
		};
	}, [data]);

	const gaugeGeometry = useMemo<GaugeGeometry | null>(() => {
		if (width <= 0 || height <= 0) return null;

		const horizontalRadius = width * 0.4;
		const verticalRadius = Math.max(24, height - 10);
		const outerRadius = Math.min(horizontalRadius, verticalRadius);
		const innerRadius = outerRadius * 0.58;
		const cx = width / 2;
		const cy = Math.min(height - 4, outerRadius + 4);

		return { cx, cy, innerRadius, outerRadius };
	}, [width, height]);

	if (!data?.buckets || data.buckets.length === 0 || totalPromptTokens === 0) {
		return <div className="text-muted-foreground flex h-full items-center justify-center text-sm">No data available</div>;
	}

	const valueData = [
		{ name: "cached", value: percentage },
		{ name: "remaining", value: 100 - percentage },
	];

	return (
		<ChartErrorBoundary resetKey={`${data?.buckets?.length ?? 0}-${totalCachedRead}-${totalPromptTokens}`}>
			<div className="grid h-full grid-rows-[104px_auto_auto] items-start overflow-hidden">
				<div ref={chartContainerRef} className="relative h-[104px] w-full">
					{gaugeGeometry && (
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
					)}
					{gaugeGeometry && (
						<svg className="pointer-events-none absolute inset-0" viewBox={`0 0 ${width} ${height}`} aria-hidden="true">
							<Needle percentage={percentage} geometry={gaugeGeometry} />
						</svg>
					)}
				</div>
				<div className="flex flex-col items-center pt-1 leading-none">
					<div className="text-muted-foreground text-3xl font-semibold tracking-tight">{percentage.toFixed(1)}%</div>
					<div className="mt-1 flex items-center gap-1 text-[11px] text-zinc-400">
						<span>of input tokens cached</span>
						<Tooltip>
							<TooltipTrigger asChild>
								<button
									type="button"
									data-testid="cache-meter-info-btn"
									className="text-zinc-500 transition-colors hover:text-zinc-300"
									aria-label="More information about cache hit rate"
								>
									<Info className="h-3 w-3" />
								</button>
							</TooltipTrigger>
							<TooltipContent side="top">This reflects provider-level caching, not Bifrost semantic cache hits.</TooltipContent>
						</Tooltip>
					</div>
				</div>
				<div className="flex flex-wrap items-center justify-center gap-x-4 gap-y-1 pt-2 text-[11px] leading-none">
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
		</ChartErrorBoundary>
	);
}