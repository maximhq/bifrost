"use client";

import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { Skeleton } from "@/components/ui/skeleton";
import type { HistogramBucket, LogsHistogramResponse } from "@/lib/types/logs";
import { ChevronDown, RotateCcw } from "lucide-react";
import { Component, type ErrorInfo, type ReactNode, useCallback, useMemo, useState } from "react";
import { Bar, BarChart, CartesianGrid, ReferenceArea, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts";

// Empty chart placeholder when data fails to render
function EmptyChart() {
	return (
		<ResponsiveContainer width="100%" height="100%">
			<BarChart
				data={[
					{ name: "", value: 0 },
					{ name: " ", value: 0 },
				]}
			>
				<CartesianGrid strokeDasharray="3 3" vertical={false} className="stroke-zinc-200 dark:stroke-zinc-700" />
				<XAxis dataKey="name" tick={{ fontSize: 13, className: "fill-zinc-500", dy: 5 }} tickLine={false} axisLine={false} />
				<YAxis tick={{ fontSize: 13, className: "fill-zinc-500" }} tickLine={false} axisLine={false} width={40} domain={[0, 1]} />
			</BarChart>
		</ResponsiveContainer>
	);
}

// Error boundary to catch Recharts rendering errors
class ChartErrorBoundary extends Component<{ children: ReactNode; resetKey?: string }, { hasError: boolean }> {
	constructor(props: { children: ReactNode; resetKey?: string }) {
		super(props);
		this.state = { hasError: false };
	}

	static getDerivedStateFromError(_: Error) {
		return { hasError: true };
	}

	static getDerivedStateFromProps(props: { resetKey?: string }, state: { hasError: boolean; prevResetKey?: string }) {
		// Reset error state when resetKey changes
		if (props.resetKey !== state.prevResetKey) {
			return { hasError: false, prevResetKey: props.resetKey };
		}
		return null;
	}

	componentDidCatch(error: Error, _errorInfo: ErrorInfo) {
		console.warn("Chart rendering error:", error.message);
	}

	render() {
		if (this.state.hasError) {
			return <EmptyChart />;
		}
		return this.props.children;
	}
}

interface LogsVolumeChartProps {
	data: LogsHistogramResponse | null;
	loading?: boolean;
	onTimeRangeChange: (startTime: number, endTime: number) => void;
	onResetZoom?: () => void;
	isZoomed?: boolean;
	startTime: number; // Unix timestamp in seconds
	endTime: number; // Unix timestamp in seconds
	isOpen: boolean;
	onOpenChange: (open: boolean) => void;
}

// Format timestamp based on bucket size
function formatTimestamp(timestamp: string, bucketSizeSeconds: number): string {
	const date = new Date(timestamp);

	if (bucketSizeSeconds >= 86400) {
		// Daily buckets: "Jan 20"
		return date.toLocaleDateString("en-US", { month: "short", day: "numeric" });
	} else if (bucketSizeSeconds >= 3600) {
		// Hourly buckets: "10:00"
		return date.toLocaleTimeString("en-US", { hour: "2-digit", minute: "2-digit", hour12: false });
	} else {
		// Sub-hourly: "10:15"
		return date.toLocaleTimeString("en-US", { hour: "2-digit", minute: "2-digit", hour12: false });
	}
}

// Format full timestamp for tooltip
function formatFullTimestamp(timestamp: string): string {
	const date = new Date(timestamp);
	return date.toLocaleString("en-US", {
		month: "short",
		day: "numeric",
		hour: "2-digit",
		minute: "2-digit",
		hour12: false,
	});
}

// Custom tooltip component
function CustomTooltip({ active, payload, label }: any) {
	if (!active || !payload || !payload.length) return null;

	const data = payload[0]?.payload as HistogramBucket & { formattedTime: string };
	if (!data) return null;

	return (
		<div className="rounded-sm border border-zinc-200 bg-white px-3 py-2 shadow-lg dark:border-zinc-700 dark:bg-zinc-900">
			<div className="mb-1 text-xs text-zinc-500">{formatFullTimestamp(data.timestamp)}</div>
			<div className="space-y-1 text-sm">
				<div className="mt-2 flex items-center justify-between gap-4">
					<span className="flex items-center gap-1.5">
						<span className="h-2 w-2 rounded-full bg-blue-500" />
						<span className="text-zinc-600 dark:text-zinc-400">Total</span>
					</span>
					<span className="font-medium">{data.count.toLocaleString()}</span>
				</div>
				<div className="flex items-center justify-between gap-4">
					<span className="flex items-center gap-1.5">
						<span className="h-2 w-2 rounded-full bg-emerald-500" />
						<span className="text-zinc-600 dark:text-zinc-400">Success</span>
					</span>
					<span className="font-medium text-emerald-600 dark:text-emerald-400">{data.success.toLocaleString()}</span>
				</div>
				<div className="flex items-center justify-between gap-4">
					<span className="flex items-center gap-1.5">
						<span className="h-2 w-2 rounded-full bg-red-500" />
						<span className="text-zinc-600 dark:text-zinc-400">Error</span>
					</span>
					<span className="font-medium text-red-600 dark:text-red-400">{data.error.toLocaleString()}</span>
				</div>
			</div>
		</div>
	);
}

export function LogsVolumeChart({
	data,
	loading,
	onTimeRangeChange,
	onResetZoom,
	isZoomed,
	startTime,
	endTime,
	isOpen,
	onOpenChange,
}: LogsVolumeChartProps) {
	// State for drag selection
	const [refAreaLeft, setRefAreaLeft] = useState<number | null>(null);
	const [refAreaRight, setRefAreaRight] = useState<number | null>(null);
	const [isSelecting, setIsSelecting] = useState(false);

	// Transform data for chart, filling in empty buckets for the full time range
	const chartData = useMemo(() => {
		// Need bucket_size_seconds and valid time range
		if (!data?.bucket_size_seconds || !startTime || !endTime || startTime >= endTime) {
			return [];
		}

		const bucketSizeMs = data.bucket_size_seconds * 1000;

		// Align start time to bucket boundary
		const minTime = Math.floor((startTime * 1000) / bucketSizeMs) * bucketSizeMs;
		const maxTime = endTime * 1000;

		// Safety: limit maximum number of buckets to prevent performance issues
		const maxBuckets = 500;
		const estimatedBuckets = Math.ceil((maxTime - minTime) / bucketSizeMs);

		if (estimatedBuckets > maxBuckets) {
			// If too many buckets, just return the original data without filling
			const result = (data.buckets || []).map((bucket, index) => ({
				...bucket,
				index,
				formattedTime: formatTimestamp(bucket.timestamp, data.bucket_size_seconds),
			}));
			// Ensure at least 2 data points for Recharts
			if (result.length === 1) {
				const nextTimestamp = new Date(new Date(result[0].timestamp).getTime() + bucketSizeMs).toISOString();
				result.push({
					timestamp: nextTimestamp,
					count: 0,
					success: 0,
					error: 0,
					index: 1,
					formattedTime: formatTimestamp(nextTimestamp, data.bucket_size_seconds),
				});
			}
			return result;
		}

		// First, create all empty buckets for the time range
		const filledBuckets: Array<HistogramBucket & { formattedTime: string; index: number }> = [];
		for (let time = minTime, idx = 0; time <= maxTime; time += bucketSizeMs, idx++) {
			const timestamp = new Date(time).toISOString();
			filledBuckets.push({
				timestamp,
				count: 0,
				success: 0,
				error: 0,
				index: idx,
				formattedTime: formatTimestamp(timestamp, data.bucket_size_seconds),
			});
		}

		// Then, place API buckets at their correct positions using index calculation
		// This is more robust than exact timestamp matching
		for (const bucket of data.buckets || []) {
			const bucketTime = new Date(bucket.timestamp).getTime();
			// Calculate the index for this bucket based on its offset from minTime
			const bucketIndex = Math.round((bucketTime - minTime) / bucketSizeMs);

			if (bucketIndex >= 0 && bucketIndex < filledBuckets.length) {
				filledBuckets[bucketIndex] = {
					...bucket,
					index: bucketIndex,
					formattedTime: formatTimestamp(bucket.timestamp, data.bucket_size_seconds),
				};
			}
		}

		// Ensure at least 2 data points for Recharts
		if (filledBuckets.length === 1) {
			const nextTimestamp = new Date(new Date(filledBuckets[0].timestamp).getTime() + bucketSizeMs).toISOString();
			filledBuckets.push({
				timestamp: nextTimestamp,
				count: 0,
				success: 0,
				error: 0,
				index: 1,
				formattedTime: formatTimestamp(nextTimestamp, data.bucket_size_seconds),
			});
		}

		return filledBuckets;
	}, [data, startTime, endTime]);

	// Handle mouse down on chart (start selection)
	const handleMouseDown = useCallback((e: any) => {
		if (e?.activeTooltipIndex !== undefined) {
			setRefAreaLeft(e.activeTooltipIndex);
			setIsSelecting(true);
		}
	}, []);

	// Handle mouse move on chart (during selection)
	const handleMouseMove = useCallback(
		(e: any) => {
			if (isSelecting && e?.activeTooltipIndex !== undefined) {
				setRefAreaRight(e.activeTooltipIndex);
			}
		},
		[isSelecting],
	);

	// Handle mouse up on chart (end selection)
	const handleMouseUp = useCallback(() => {
		if (refAreaLeft === null || refAreaRight === null || !data?.bucket_size_seconds || chartData.length === 0) {
			setRefAreaLeft(null);
			setRefAreaRight(null);
			setIsSelecting(false);
			return;
		}

		// Get the buckets by index
		const leftBucket = chartData[refAreaLeft];
		const rightBucket = chartData[refAreaRight];

		if (leftBucket && rightBucket) {
			const leftTime = new Date(leftBucket.timestamp).getTime() / 1000;
			const rightTime = new Date(rightBucket.timestamp).getTime() / 1000 + data.bucket_size_seconds;

			// Ensure left < right
			const selectionStart = Math.min(leftTime, rightTime);
			const selectionEnd = Math.max(leftTime, rightTime);

			// Only trigger if selection spans at least one bucket
			if (selectionEnd - selectionStart >= data.bucket_size_seconds) {
				onTimeRangeChange(selectionStart, selectionEnd);
			}
		}

		setRefAreaLeft(null);
		setRefAreaRight(null);
		setIsSelecting(false);
	}, [refAreaLeft, refAreaRight, data, chartData, onTimeRangeChange]);

	// Handle click on a bar (zoom into that bucket)
	const handleBarClick = useCallback(
		(barData: any) => {
			if (!data || !barData?.timestamp) return;

			const bucket = barData as HistogramBucket;
			const startTime = new Date(bucket.timestamp).getTime() / 1000;
			const endTime = startTime + data.bucket_size_seconds;

			onTimeRangeChange(startTime, endTime);
		},
		[data, onTimeRangeChange],
	);

	if (loading) {
		return (
			<Card className="gap-0 rounded-sm px-2 py-2 shadow-none">
				<div className="flex items-center justify-between">
					<div className="flex items-center gap-2">
						<ChevronDown className="text-muted-foreground h-4 w-4" />
						<span className="text-muted-foreground text-sm font-medium">Request Volume</span>
					</div>
					<div className="mr-2 flex items-center gap-4">
						<div className="flex items-center gap-3 text-xs">
							<span className="flex items-center gap-1.5">
								<span className="h-2 w-2 rounded-full bg-emerald-500" />
								<span className="text-muted-foreground">Success</span>
							</span>
							<span className="flex items-center gap-1.5">
								<span className="h-2 w-2 rounded-full bg-red-500" />
								<span className="text-muted-foreground">Error</span>
							</span>
						</div>
					</div>
				</div>
				<div className="" style={{ height: "131px", marginTop: 4 }}>
					<Skeleton className="h-full w-full" />
				</div>
			</Card>
		);
	}

	// Check if we have valid data for the chart
	const hasValidData = data && startTime && endTime && chartData.length >= 2;

	return (
		<Card className="rounded-sm px-2 py-2 shadow-none">
			<Collapsible open={isOpen} onOpenChange={onOpenChange}>
				<div className="flex items-center justify-between">
					<CollapsibleTrigger className="flex items-center gap-2 hover:opacity-80">
						<ChevronDown className={`text-muted-foreground h-4 w-4 transition-transform duration-200 ${isOpen ? "" : "-rotate-90"}`} />
						<span className="text-muted-foreground text-sm font-medium">Request Volume</span>
						<Badge variant="secondary" className="text-xs">
							BETA
						</Badge>
					</CollapsibleTrigger>
					<div className="mr-2 flex items-center gap-4">
						{isOpen && (
							<div className="flex items-center gap-3 text-xs">
								<span className="flex items-center gap-1.5">
									<span className="h-2 w-2 rounded-full bg-emerald-500" />
									<span className="text-muted-foreground">Success</span>
								</span>
								<span className="flex items-center gap-1.5">
									<span className="h-2 w-2 rounded-full bg-red-500" />
									<span className="text-muted-foreground">Error</span>
								</span>
							</div>
						)}
						{isZoomed && onResetZoom && (
							<button
								onClick={onResetZoom}
								className="text-muted-foreground hover:text-foreground flex items-center gap-1 text-xs transition-colors"
							>
								<RotateCcw className="h-3 w-3" />
								Reset zoom
							</button>
						)}
					</div>
				</div>
				<CollapsibleContent className="data-[state=closed]:animate-collapse-up data-[state=open]:animate-collapse-down overflow-hidden">
					<div className="mt-2 h-32 select-none">
						{hasValidData ? (
							<ChartErrorBoundary resetKey={`${startTime}-${endTime}-${chartData.length}`}>
								<ResponsiveContainer width="100%" height="100%">
									<BarChart
										data={chartData}
										onMouseDown={handleMouseDown}
										onMouseMove={handleMouseMove}
										onMouseUp={handleMouseUp}
										onMouseLeave={handleMouseUp}
										barCategoryGap={1}
									>
										<CartesianGrid strokeDasharray="3 3" vertical={false} className="stroke-zinc-200 dark:stroke-zinc-700" />
										<XAxis
											dataKey="index"
											type="number"
											domain={[-0.5, chartData.length - 0.5]}
											tick={{ fontSize: 13, className: "fill-zinc-500", dy: 5 }}
											tickLine={true}
											axisLine={false}
											tickFormatter={(idx) => chartData[Math.round(idx)]?.formattedTime || ""}
											interval="preserveStartEnd"
										/>
										<YAxis
											tick={{ fontSize: 13, className: "fill-zinc-500" }}
											tickLine={false}
											axisLine={false}
											width={40}
											tickFormatter={(v) => v.toLocaleString()}
											domain={[0, (dataMax: number) => Math.max(dataMax, 5)]}
											allowDataOverflow={false}
										/>
										<Tooltip content={<CustomTooltip />} cursor={{ fill: "#8c8c8f", fillOpacity: 0.15 }} />
										<Bar
											dataKey="success"
											stackId="requests"
											barSize={30}
											fill="#10b981"
											fillOpacity={0.7}
											radius={[0, 0, 0, 0]}
											cursor="pointer"
											onClick={(data: any) => handleBarClick(data)}
										/>
										<Bar
											dataKey="error"
											stackId="requests"
											fill="#ef4444"
											barSize={30}
											fillOpacity={0.7}
											radius={[2, 2, 0, 0]}
											cursor="pointer"
											onClick={(data: any) => handleBarClick(data)}
										/>
										{refAreaLeft !== null && refAreaRight !== null && chartData[refAreaLeft] && chartData[refAreaRight] && (
											<ReferenceArea x1={refAreaLeft} x2={refAreaRight} strokeOpacity={0.3} fill="#6366f1" fillOpacity={0.2} />
										)}
									</BarChart>
								</ResponsiveContainer>
							</ChartErrorBoundary>
						) : (
							<EmptyChart />
						)}
					</div>
				</CollapsibleContent>
			</Collapsible>
		</Card>
	);
}
