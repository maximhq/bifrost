import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { COMPACT_NUMBER_FORMAT } from "@/lib/utils/numbers";
import NumberFlow from "@number-flow/react";
import { memo, useMemo } from "react";
import type {
	ProviderCostHistogramResponse,
	ProviderLatencyHistogramResponse,
	ProviderThroughputHistogramResponse,
	ProviderTokenHistogramResponse,
} from "@/lib/types/logs";
import {
	CHART_COLORS,
	CHART_HEADER_LEGEND_CLASS,
	LATENCY_COLORS,
	OTHER_SERIES_COLOR,
	OTHER_SERIES_KEY,
	OTHER_SERIES_LABEL,
	THROUGHPUT_COLOR,
	formatTokensPerSecond,
	getModelColor,
} from "../utils/chartUtils";
import { ChartCard } from "./charts/chartCard";
import { type ChartType, ChartTypeToggle } from "./charts/chartTypeToggle";
import { ProviderCostChart } from "./charts/providerCostChart";
import { ProviderFilterSelect } from "./charts/providerFilterSelect";
import { ProviderLatencyChart } from "./charts/providerLatencyChart";
import { ProviderThroughputChart } from "./charts/providerThroughputChart";
import { ProviderTokenChart } from "./charts/providerTokenChart";

export interface ProviderUsageTabProps {
	// Data
	providerCostData: ProviderCostHistogramResponse | null;
	providerTokenData: ProviderTokenHistogramResponse | null;
	providerLatencyData: ProviderLatencyHistogramResponse | null;
	providerThroughputData: ProviderThroughputHistogramResponse | null;

	// Loading states
	loadingProviderCost: boolean;
	loadingProviderTokens: boolean;
	loadingProviderLatency: boolean;
	loadingProviderThroughput: boolean;

	// Time range
	startTime: number;
	endTime: number;

	// Chart types
	providerCostChartType: ChartType;
	providerTokenChartType: ChartType;
	providerLatencyChartType: ChartType;
	providerThroughputChartType: ChartType;

	// Provider selections
	providerCostProvider: string;
	providerTokenProvider: string;
	providerLatencyProvider: string;
	providerThroughputProvider: string;

	// Derived provider lists
	availableProviders: string[];
	providerCostProviders: string[];
	providerTokenProviders: string[];
	providerLatencyProviders: string[];
	providerThroughputProviders: string[];

	// Chart type toggle callbacks
	onProviderCostChartToggle: (type: ChartType) => void;
	onProviderTokenChartToggle: (type: ChartType) => void;
	onProviderLatencyChartToggle: (type: ChartType) => void;
	onProviderThroughputChartToggle: (type: ChartType) => void;

	// Filter callbacks
	onProviderCostProviderChange: (provider: string) => void;
	onProviderTokenProviderChange: (provider: string) => void;
	onProviderLatencyProviderChange: (provider: string) => void;
	onProviderThroughputProviderChange: (provider: string) => void;
}

function ProviderUsageTabImpl({
	providerCostData,
	providerTokenData,
	providerLatencyData,
	providerThroughputData,
	loadingProviderCost,
	loadingProviderTokens,
	loadingProviderLatency,
	loadingProviderThroughput,
	startTime,
	endTime,
	providerCostChartType,
	providerTokenChartType,
	providerLatencyChartType,
	providerThroughputChartType,
	providerCostProvider,
	providerTokenProvider,
	providerLatencyProvider,
	providerThroughputProvider,
	availableProviders,
	providerCostProviders,
	providerTokenProviders,
	providerLatencyProviders,
	providerThroughputProviders,
	onProviderCostChartToggle,
	onProviderTokenChartToggle,
	onProviderLatencyChartToggle,
	onProviderThroughputChartToggle,
	onProviderCostProviderChange,
	onProviderTokenProviderChange,
	onProviderLatencyProviderChange,
	onProviderThroughputProviderChange,
}: ProviderUsageTabProps) {
	const providerCostTotal = useMemo(() => {
		if (!providerCostData?.buckets) return null;
		if (providerCostProvider === "all") {
			return providerCostData.buckets.reduce((sum, b) => sum + (b.total_cost ?? 0), 0);
		}
		return providerCostData.buckets.reduce((sum, b) => sum + (b.by_provider?.[providerCostProvider] ?? 0), 0);
	}, [providerCostData, providerCostProvider]);

	const providerTokenTotal = useMemo(() => {
		if (!providerTokenData?.buckets) return null;
		let sum = 0;
		for (const b of providerTokenData.buckets) {
			if (!b.by_provider) continue;
			if (providerTokenProvider === "all") {
				for (const p of providerTokenData.providers) sum += b.by_provider[p]?.total_tokens ?? 0;
			} else {
				sum += b.by_provider[providerTokenProvider]?.total_tokens ?? 0;
			}
		}
		return sum;
	}, [providerTokenData, providerTokenProvider]);

	const providerLatencyAvg = useMemo(() => {
		if (!providerLatencyData?.buckets) return null;
		let weighted = 0;
		let count = 0;
		for (const b of providerLatencyData.buckets) {
			if (!b.by_provider) continue;
			const providers = providerLatencyProvider === "all" ? providerLatencyData.providers : [providerLatencyProvider];
			for (const p of providers) {
				const s = b.by_provider[p];
				if (!s || !s.total_requests) continue;
				weighted += (s.avg_latency ?? 0) * s.total_requests;
				count += s.total_requests;
			}
		}
		return count > 0 ? weighted / count : null;
	}, [providerLatencyData, providerLatencyProvider]);

	const providerThroughputAvg = useMemo(() => {
		if (!providerThroughputData?.buckets) return null;
		let weighted = 0;
		let count = 0;
		for (const b of providerThroughputData.buckets) {
			if (!b.by_provider) continue;
			const providers = providerThroughputProvider === "all" ? providerThroughputData.providers : [providerThroughputProvider];
			for (const p of providers) {
				const s = b.by_provider[p];
				if (!s || !s.total_requests) continue;
				weighted += (s.tokens_per_second ?? 0) * s.total_requests;
				count += s.total_requests;
			}
		}
		return count > 0 ? weighted / count : null;
	}, [providerThroughputData, providerThroughputProvider]);

	return (
		<div className="grid grid-cols-1 gap-2 lg:grid-cols-2 2xl:grid-cols-3">
			{/* Provider Cost Chart */}
			<ChartCard
				title="Provider Cost"
				loading={loadingProviderCost}
				testId="chart-provider-cost"
				totalLabel="Total"
				total={
					providerCostTotal !== null ? (
						<NumberFlow value={providerCostTotal} format={{ ...COMPACT_NUMBER_FORMAT, style: "currency", currency: "USD" }} />
					) : undefined
				}
				totalTooltip={
					providerCostTotal !== null
						? providerCostTotal.toLocaleString("en-US", { style: "currency", currency: "USD", maximumFractionDigits: 6 })
						: undefined
				}
				legend={
					<div className={CHART_HEADER_LEGEND_CLASS}>
						{providerCostProvider === "all" ? (
							providerCostProviders.length > 0 && (
								<>
									<Tooltip>
										<TooltipTrigger asChild>
											<span data-testid="provider-cost-legend-trigger" className="flex items-center gap-1">
												<span className="h-2 w-2 shrink-0 rounded-full" style={{ backgroundColor: getModelColor(0) }} />
												<span className="text-muted-foreground max-w-[100px] truncate">{providerCostProviders[0]}</span>
											</span>
										</TooltipTrigger>
										<TooltipContent>{providerCostProviders[0]}</TooltipContent>
									</Tooltip>
									{providerCostProviders.length > 1 && (
										<Tooltip>
											<TooltipTrigger asChild>
												<button
													type="button"
													data-testid="provider-cost-legend-more-trigger"
													className="text-muted-foreground cursor-default"
												>
													+{providerCostProviders.length - 1} more
												</button>
											</TooltipTrigger>
											<TooltipContent>
												<div className="flex flex-col gap-1">
													{providerCostProviders.slice(1).map((provider, idx) => (
														<span key={provider} className="flex items-center gap-1">
															<span
																className="h-2 w-2 shrink-0 rounded-full"
																style={{ backgroundColor: provider === OTHER_SERIES_KEY ? OTHER_SERIES_COLOR : getModelColor(idx + 1) }}
															/>
															{provider === OTHER_SERIES_KEY ? OTHER_SERIES_LABEL : provider}
														</span>
													))}
												</div>
											</TooltipContent>
										</Tooltip>
									)}
								</>
							)
						) : (
							<Tooltip>
								<TooltipTrigger asChild>
									<span data-testid="provider-cost-legend-single-trigger" className="flex items-center gap-1">
										<span className="h-2 w-2 shrink-0 rounded-full" style={{ backgroundColor: getModelColor(0) }} />
										<span className="text-muted-foreground max-w-[100px] truncate">{providerCostProvider}</span>
									</span>
								</TooltipTrigger>
								<TooltipContent>{providerCostProvider}</TooltipContent>
							</Tooltip>
						)}
					</div>
				}
				controls={
					<>
						<ProviderFilterSelect
							providers={availableProviders}
							selectedProvider={providerCostProvider}
							onProviderChange={onProviderCostProviderChange}
							data-testid="dashboard-provider-cost-filter"
						/>
						<ChartTypeToggle
							chartType={providerCostChartType}
							onToggle={onProviderCostChartToggle}
							data-testid="dashboard-provider-cost-chart-toggle"
						/>
					</>
				}
			>
				<ProviderCostChart
					data={providerCostData}
					chartType={providerCostChartType}
					startTime={startTime}
					endTime={endTime}
					selectedProvider={providerCostProvider}
				/>
			</ChartCard>

			{/* Provider Token Usage Chart */}
			<ChartCard
				title="Provider Token Usage"
				loading={loadingProviderTokens}
				testId="chart-provider-tokens"
				totalLabel="Total"
				total={providerTokenTotal !== null ? <NumberFlow value={providerTokenTotal} format={COMPACT_NUMBER_FORMAT} /> : undefined}
				totalTooltip={providerTokenTotal !== null ? providerTokenTotal.toLocaleString("en-US") : undefined}
				legend={
					<div className={CHART_HEADER_LEGEND_CLASS}>
						{providerTokenProvider === "all" ? (
							providerTokenProviders.length > 0 && (
								<>
									<Tooltip>
										<TooltipTrigger asChild>
											<span data-testid="provider-token-legend-trigger" className="flex items-center gap-1">
												<span className="h-2 w-2 shrink-0 rounded-full" style={{ backgroundColor: getModelColor(0) }} />
												<span className="text-muted-foreground max-w-[100px] truncate">{providerTokenProviders[0]}</span>
											</span>
										</TooltipTrigger>
										<TooltipContent>{providerTokenProviders[0]}</TooltipContent>
									</Tooltip>
									{providerTokenProviders.length > 1 && (
										<Tooltip>
											<TooltipTrigger asChild>
												<button
													type="button"
													data-testid="provider-token-legend-more-trigger"
													className="text-muted-foreground cursor-default"
												>
													+{providerTokenProviders.length - 1} more
												</button>
											</TooltipTrigger>
											<TooltipContent>
												<div className="flex flex-col gap-1">
													{providerTokenProviders.slice(1).map((provider, idx) => (
														<span key={provider} className="flex items-center gap-1">
															<span
																className="h-2 w-2 shrink-0 rounded-full"
																style={{ backgroundColor: provider === OTHER_SERIES_KEY ? OTHER_SERIES_COLOR : getModelColor(idx + 1) }}
															/>
															{provider === OTHER_SERIES_KEY ? OTHER_SERIES_LABEL : provider}
														</span>
													))}
												</div>
											</TooltipContent>
										</Tooltip>
									)}
								</>
							)
						) : (
							<>
								<span className="flex items-center gap-1">
									<span className="h-2 w-2 shrink-0 rounded-full" style={{ backgroundColor: CHART_COLORS.promptTokens }} />
									<span className="text-muted-foreground">Input</span>
								</span>
								<span className="flex items-center gap-1">
									<span className="h-2 w-2 shrink-0 rounded-full" style={{ backgroundColor: CHART_COLORS.completionTokens }} />
									<span className="text-muted-foreground">Output</span>
								</span>
							</>
						)}
					</div>
				}
				controls={
					<>
						<ProviderFilterSelect
							providers={availableProviders}
							selectedProvider={providerTokenProvider}
							onProviderChange={onProviderTokenProviderChange}
							data-testid="dashboard-provider-token-filter"
						/>
						<ChartTypeToggle
							chartType={providerTokenChartType}
							onToggle={onProviderTokenChartToggle}
							data-testid="dashboard-provider-token-chart-toggle"
						/>
					</>
				}
			>
				<ProviderTokenChart
					data={providerTokenData}
					chartType={providerTokenChartType}
					startTime={startTime}
					endTime={endTime}
					selectedProvider={providerTokenProvider}
				/>
			</ChartCard>

			{/* Provider Latency Chart */}
			<ChartCard
				title="Provider Latency"
				loading={loadingProviderLatency}
				testId="chart-provider-latency"
				totalLabel="Avg"
				total={
					providerLatencyAvg !== null ? (
						<NumberFlow value={providerLatencyAvg} format={{ minimumFractionDigits: 2, maximumFractionDigits: 2 }} suffix="ms" />
					) : undefined
				}
				totalTooltip={
					providerLatencyAvg !== null ? `${providerLatencyAvg.toLocaleString("en-US", { maximumFractionDigits: 6 })}ms` : undefined
				}
				legend={
					<div className={CHART_HEADER_LEGEND_CLASS}>
						{providerLatencyProvider === "all" ? (
							providerLatencyProviders.length > 0 && (
								<>
									<Tooltip>
										<TooltipTrigger asChild>
											<span data-testid="provider-latency-legend-trigger" className="flex items-center gap-1">
												<span className="h-2 w-2 shrink-0 rounded-full" style={{ backgroundColor: getModelColor(0) }} />
												<span className="text-muted-foreground max-w-[100px] truncate">{providerLatencyProviders[0]}</span>
											</span>
										</TooltipTrigger>
										<TooltipContent>{providerLatencyProviders[0]}</TooltipContent>
									</Tooltip>
									{providerLatencyProviders.length > 1 && (
										<Tooltip>
											<TooltipTrigger asChild>
												<button
													type="button"
													data-testid="provider-latency-legend-more-trigger"
													className="text-muted-foreground cursor-default"
												>
													+{providerLatencyProviders.length - 1} more
												</button>
											</TooltipTrigger>
											<TooltipContent>
												<div className="flex flex-col gap-1">
													{providerLatencyProviders.slice(1).map((provider, idx) => (
														<span key={provider} className="flex items-center gap-1">
															<span className="h-2 w-2 shrink-0 rounded-full" style={{ backgroundColor: getModelColor(idx + 1) }} />
															{provider}
														</span>
													))}
												</div>
											</TooltipContent>
										</Tooltip>
									)}
								</>
							)
						) : (
							<>
								<span className="flex items-center gap-1">
									<span className="h-2 w-2 rounded-full" style={{ backgroundColor: LATENCY_COLORS.avg }} />
									<span className="text-muted-foreground">Avg</span>
								</span>
								<span className="flex items-center gap-1">
									<span className="h-2 w-2 rounded-full" style={{ backgroundColor: LATENCY_COLORS.p90 }} />
									<span className="text-muted-foreground">P90</span>
								</span>
								<span className="flex items-center gap-1">
									<span className="h-2 w-2 rounded-full" style={{ backgroundColor: LATENCY_COLORS.p95 }} />
									<span className="text-muted-foreground">P95</span>
								</span>
								<span className="flex items-center gap-1">
									<span className="h-2 w-2 rounded-full" style={{ backgroundColor: LATENCY_COLORS.p99 }} />
									<span className="text-muted-foreground">P99</span>
								</span>
							</>
						)}
					</div>
				}
				controls={
					<>
						<ProviderFilterSelect
							providers={availableProviders}
							selectedProvider={providerLatencyProvider}
							onProviderChange={onProviderLatencyProviderChange}
							data-testid="dashboard-provider-latency-filter"
						/>
						<ChartTypeToggle
							chartType={providerLatencyChartType}
							onToggle={onProviderLatencyChartToggle}
							data-testid="dashboard-provider-latency-chart-toggle"
						/>
					</>
				}
			>
				<ProviderLatencyChart
					data={providerLatencyData}
					chartType={providerLatencyChartType}
					startTime={startTime}
					endTime={endTime}
					selectedProvider={providerLatencyProvider}
				/>
			</ChartCard>

			{/* Provider Throughput (tokens/sec) Chart */}
			<ChartCard
				title="Provider Throughput"
				loading={loadingProviderThroughput}
				testId="chart-provider-throughput"
				totalLabel="Avg"
				total={
					providerThroughputAvg !== null ? (
						<span className="truncate whitespace-nowrap">{formatTokensPerSecond(providerThroughputAvg)}</span>
					) : undefined
				}
				totalTooltip={
					providerThroughputAvg !== null
						? `${providerThroughputAvg.toLocaleString("en-US", { maximumFractionDigits: 2 })} tokens/sec`
						: undefined
				}
				legend={
					<div className={CHART_HEADER_LEGEND_CLASS}>
						{providerThroughputProvider === "all" ? (
							providerThroughputProviders.length > 0 && (
								<>
									<Tooltip>
										<TooltipTrigger asChild>
											<span data-testid="provider-throughput-legend-trigger" className="flex items-center gap-1">
												<span className="h-2 w-2 shrink-0 rounded-full" style={{ backgroundColor: getModelColor(0) }} />
												<span className="text-muted-foreground max-w-[100px] truncate">{providerThroughputProviders[0]}</span>
											</span>
										</TooltipTrigger>
										<TooltipContent>{providerThroughputProviders[0]}</TooltipContent>
									</Tooltip>
									{providerThroughputProviders.length > 1 && (
										<Tooltip>
											<TooltipTrigger asChild>
												<button
													type="button"
													data-testid="provider-throughput-legend-more-trigger"
													className="text-muted-foreground cursor-default"
												>
													+{providerThroughputProviders.length - 1} more
												</button>
											</TooltipTrigger>
											<TooltipContent>
												<div className="flex flex-col gap-1">
													{providerThroughputProviders.slice(1).map((provider, idx) => (
														<span key={provider} className="flex items-center gap-1">
															<span className="h-2 w-2 shrink-0 rounded-full" style={{ backgroundColor: getModelColor(idx + 1) }} />
															{provider}
														</span>
													))}
												</div>
											</TooltipContent>
										</Tooltip>
									)}
								</>
							)
						) : (
							<Tooltip>
								<TooltipTrigger asChild>
									<span data-testid="provider-throughput-legend-single-trigger" className="flex items-center gap-1">
										<span className="h-2 w-2 shrink-0 rounded-full" style={{ backgroundColor: THROUGHPUT_COLOR }} />
										<span className="text-muted-foreground max-w-[100px] truncate">{providerThroughputProvider}</span>
									</span>
								</TooltipTrigger>
								<TooltipContent>{providerThroughputProvider}</TooltipContent>
							</Tooltip>
						)}
					</div>
				}
				controls={
					<>
						<ProviderFilterSelect
							providers={availableProviders}
							selectedProvider={providerThroughputProvider}
							onProviderChange={onProviderThroughputProviderChange}
							data-testid="dashboard-provider-throughput-filter"
						/>
						<ChartTypeToggle
							chartType={providerThroughputChartType}
							onToggle={onProviderThroughputChartToggle}
							data-testid="dashboard-provider-throughput-chart-toggle"
						/>
					</>
				}
			>
				<ProviderThroughputChart
					data={providerThroughputData}
					chartType={providerThroughputChartType}
					startTime={startTime}
					endTime={endTime}
					selectedProvider={providerThroughputProvider}
				/>
			</ChartCard>
		</div>
	);
}
export const ProviderUsageTab = memo(ProviderUsageTabImpl);