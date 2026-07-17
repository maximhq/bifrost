import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import type {
	CostHistogramResponse,
	LatencyHistogramResponse,
	LogStats,
	LogsHistogramResponse,
	ModelHistogramResponse,
	ThroughputHistogramResponse,
	TokenHistogramResponse,
} from "@/lib/types/logs";
import { COMPACT_NUMBER_FORMAT } from "@/lib/utils/numbers";
import NumberFlow from "@number-flow/react";
import { memo, useMemo } from "react";
import { CHART_COLORS, CHART_HEADER_LEGEND_CLASS, LATENCY_COLORS, THROUGHPUT_COLOR, formatTokensPerSecond, getModelColor } from "../utils/chartUtils";
import { ChartCard } from "./charts/chartCard";
import { type ChartType, ChartTypeToggle } from "./charts/chartTypeToggle";
import { CostChart } from "./charts/costChart";
import ExternalCacheTokenMeterChart from "./charts/externalCacheTokenMeterChart";
import { LatencyChart } from "./charts/latencyChart";
import { ThroughputChart } from "./charts/throughputChart";
import LocalCacheTokenMeterChart from "./charts/localCacheTokenMeterChart";
import { LogVolumeChart } from "./charts/logVolumeChart";
import { ModelFilterSelect } from "./charts/modelFilterSelect";
import { ModelUsageChart } from "./charts/modelUsageChart";
import { TokenUsageChart } from "./charts/tokenUsageChart";

export interface OverviewTabProps {
	// Data
	histogramData: LogsHistogramResponse | null;
	tokenData: TokenHistogramResponse | null;
	costData: CostHistogramResponse | null;
	modelData: ModelHistogramResponse | null;
	latencyData: LatencyHistogramResponse | null;
	throughputData: ThroughputHistogramResponse | null;
	logsStats: LogStats | null;

	// Loading states
	loadingHistogram: boolean;
	loadingTokens: boolean;
	loadingCost: boolean;
	loadingModels: boolean;
	loadingLatency: boolean;
	loadingThroughput: boolean;
	loadingStats: boolean;

	// Time range
	startTime: number;
	endTime: number;

	// Chart types
	volumeChartType: ChartType;
	tokenChartType: ChartType;
	costChartType: ChartType;
	modelChartType: ChartType;
	latencyChartType: ChartType;
	throughputChartType: ChartType;

	// Model selections
	costModel: string;
	usageModel: string;

	// Derived model lists
	costModels: string[];
	usageModels: string[];
	availableModels: string[];

	// Chart type toggle callbacks
	onVolumeChartToggle: (type: ChartType) => void;
	onTokenChartToggle: (type: ChartType) => void;
	onCostChartToggle: (type: ChartType) => void;
	onModelChartToggle: (type: ChartType) => void;
	onLatencyChartToggle: (type: ChartType) => void;
	onThroughputChartToggle: (type: ChartType) => void;

	// Filter callbacks
	onCostModelChange: (model: string) => void;
	onUsageModelChange: (model: string) => void;
}

function OverviewTabImpl({
	histogramData,
	tokenData,
	costData,
	modelData,
	latencyData,
	throughputData,
	logsStats,
	loadingHistogram,
	loadingTokens,
	loadingCost,
	loadingModels,
	loadingLatency,
	loadingThroughput,
	loadingStats,
	startTime,
	endTime,
	volumeChartType,
	tokenChartType,
	costChartType,
	modelChartType,
	latencyChartType,
	throughputChartType,
	costModel,
	usageModel,
	costModels,
	usageModels,
	availableModels,
	onVolumeChartToggle,
	onTokenChartToggle,
	onCostChartToggle,
	onModelChartToggle,
	onLatencyChartToggle,
	onThroughputChartToggle,
	onCostModelChange,
	onUsageModelChange,
}: OverviewTabProps) {
	const volumeTotal = useMemo(() => {
		if (!histogramData?.buckets) return null;
		return histogramData.buckets.reduce((sum, b) => sum + (b.count ?? 0), 0);
	}, [histogramData]);

	const tokenTotal = useMemo(() => {
		if (!tokenData?.buckets) return null;
		return tokenData.buckets.reduce((sum, b) => sum + (b.total_tokens ?? 0), 0);
	}, [tokenData]);

	const costTotal = useMemo(() => {
		if (!costData?.buckets) return null;
		if (costModel === "all") {
			return costData.buckets.reduce((sum, b) => sum + (b.total_cost ?? 0), 0);
		}
		return costData.buckets.reduce((sum, b) => sum + (b.by_model?.[costModel] ?? 0), 0);
	}, [costData, costModel]);

	const modelUsageTotal = useMemo(() => {
		if (!modelData?.buckets) return null;
		if (usageModel === "all") {
			let sum = 0;
			for (const b of modelData.buckets) {
				if (!b.by_model) continue;
				for (const m of modelData.models) sum += b.by_model[m]?.total ?? 0;
			}
			return sum;
		}
		return modelData.buckets.reduce((sum, b) => sum + (b.by_model?.[usageModel]?.total ?? 0), 0);
	}, [modelData, usageModel]);

	const latencyAvg = useMemo(() => {
		if (!latencyData?.buckets || latencyData.buckets.length === 0) return null;
		let weighted = 0;
		let count = 0;
		for (const b of latencyData.buckets) {
			const reqs = b.total_requests ?? 0;
			if (reqs === 0) continue;
			weighted += (b.avg_latency ?? 0) * reqs;
			count += reqs;
		}
		return count > 0 ? weighted / count : null;
	}, [latencyData]);

	// Weighted-average throughput across buckets (weighted by request count) so
	// the header figure matches how the aggregate rate is computed per bucket.
	const throughputAvg = useMemo(() => {
		if (!throughputData?.buckets || throughputData.buckets.length === 0) return null;
		let weighted = 0;
		let count = 0;
		for (const b of throughputData.buckets) {
			const reqs = b.total_requests ?? 0;
			if (reqs === 0) continue;
			weighted += (b.tokens_per_second ?? 0) * reqs;
			count += reqs;
		}
		return count > 0 ? weighted / count : null;
	}, [throughputData]);

	return (
		<>
			{/* Charts Grid */}
			<div className="grid grid-cols-1 gap-2 lg:grid-cols-2 2xl:grid-cols-3">
				{/* Log Volume Chart */}
				<ChartCard
					title="Request Volume"
					loading={loadingHistogram}
					testId="chart-log-volume"
					totalLabel="Total"
					total={volumeTotal !== null ? <NumberFlow value={volumeTotal} format={COMPACT_NUMBER_FORMAT} /> : undefined}
					totalTooltip={volumeTotal !== null ? volumeTotal.toLocaleString("en-US") : undefined}
					legend={
						<div className={CHART_HEADER_LEGEND_CLASS}>
							<span className="flex items-center gap-1">
								<span className="h-2 w-2 rounded-full" style={{ backgroundColor: CHART_COLORS.success }} />
								<span className="text-muted-foreground">Success</span>
							</span>
							<span className="flex items-center gap-1">
								<span className="h-2 w-2 rounded-full" style={{ backgroundColor: CHART_COLORS.error }} />
								<span className="text-muted-foreground">Error</span>
							</span>
							<span className="flex items-center gap-1">
								<span className="h-2 w-2 rounded-full" style={{ backgroundColor: CHART_COLORS.cancelled }} />
								<span className="text-muted-foreground">Cancelled</span>
							</span>
						</div>
					}
					controls={
						<ChartTypeToggle chartType={volumeChartType} onToggle={onVolumeChartToggle} data-testid="dashboard-volume-chart-toggle" />
					}
				>
					<LogVolumeChart data={histogramData} chartType={volumeChartType} startTime={startTime} endTime={endTime} />
				</ChartCard>

				{/* Token Usage Chart */}
				<ChartCard
					title="Token Usage"
					loading={loadingTokens}
					testId="chart-token-usage"
					totalLabel="Total"
					total={tokenTotal !== null ? <NumberFlow value={tokenTotal} format={COMPACT_NUMBER_FORMAT} /> : undefined}
					totalTooltip={tokenTotal !== null ? tokenTotal.toLocaleString("en-US") : undefined}
					legend={
						<div className={CHART_HEADER_LEGEND_CLASS}>
							<span className="flex items-center gap-1">
								<span className="h-2 w-2 rounded-full" style={{ backgroundColor: CHART_COLORS.promptTokens }} />
								<span className="text-muted-foreground">Input</span>
							</span>
							<span className="flex items-center gap-1">
								<span className="h-2 w-2 rounded-full" style={{ backgroundColor: CHART_COLORS.completionTokens }} />
								<span className="text-muted-foreground">Output</span>
							</span>
							<span className="flex items-center gap-1">
								<span className="h-2 w-2 rounded-full" style={{ backgroundColor: CHART_COLORS.cachedReadTokens }} />
								<span className="text-muted-foreground">Cached</span>
							</span>
						</div>
					}
					controls={<ChartTypeToggle chartType={tokenChartType} onToggle={onTokenChartToggle} data-testid="dashboard-token-chart-toggle" />}
				>
					<TokenUsageChart data={tokenData} chartType={tokenChartType} startTime={startTime} endTime={endTime} />
				</ChartCard>

				{/* External Cache Hit Rate Meter */}
				<ChartCard title="External Cache Hit Rate" loading={loadingTokens} testId="chart-cache-external">
					<ExternalCacheTokenMeterChart data={tokenData} />
				</ChartCard>

				{/* Local Cache Hit Rate Meter */}
				<ChartCard title="Local Cache Hit Rate" loading={loadingStats} testId="chart-cache-local">
					<LocalCacheTokenMeterChart data={logsStats} />
				</ChartCard>

				{/* Cost Chart */}
				<ChartCard
					title="Cost"
					loading={loadingCost}
					testId="chart-cost-total"
					totalLabel="Total"
					total={
						costTotal !== null ? (
							<NumberFlow value={costTotal} format={{ ...COMPACT_NUMBER_FORMAT, style: "currency", currency: "USD" }} />
						) : undefined
					}
					totalTooltip={
						costTotal !== null
							? costTotal.toLocaleString("en-US", { style: "currency", currency: "USD", maximumFractionDigits: 6 })
							: undefined
					}
					legend={
						<div className={CHART_HEADER_LEGEND_CLASS}>
							{costModel === "all" ? (
								costModels.length > 0 && (
									<>
										<Tooltip>
											<TooltipTrigger asChild>
												<span tabIndex={0} data-testid="cost-legend-trigger" className="flex items-center gap-1">
													<span className="h-2 w-2 shrink-0 rounded-full" style={{ backgroundColor: getModelColor(0) }} />
													<span className="text-muted-foreground max-w-[100px] truncate">{costModels[0]}</span>
												</span>
											</TooltipTrigger>
											<TooltipContent>{costModels[0]}</TooltipContent>
										</Tooltip>
										{costModels.length > 1 && (
											<Tooltip>
												<TooltipTrigger asChild>
													<span tabIndex={0} data-testid="cost-legend-more-trigger" className="text-muted-foreground cursor-default">
														+{costModels.length - 1} more
													</span>
												</TooltipTrigger>
												<TooltipContent>
													<div className="flex flex-col gap-1">
														{costModels.slice(1).map((model, idx) => (
															<span key={model} className="flex items-center gap-1">
																<span
																	className="h-2 w-2 shrink-0 rounded-full"
																	style={{
																		backgroundColor: getModelColor(idx + 1),
																	}}
																/>
																{model}
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
										<span tabIndex={0} data-testid="cost-legend-single-trigger" className="flex items-center gap-1">
											<span className="h-2 w-2 shrink-0 rounded-full" style={{ backgroundColor: getModelColor(0) }} />
											<span className="text-muted-foreground max-w-[100px] truncate">{costModel}</span>
										</span>
									</TooltipTrigger>
									<TooltipContent>{costModel}</TooltipContent>
								</Tooltip>
							)}
						</div>
					}
					controls={
						<>
							<ModelFilterSelect
								models={availableModels}
								selectedModel={costModel}
								onModelChange={onCostModelChange}
								data-testid="dashboard-cost-model-filter"
							/>
							<ChartTypeToggle chartType={costChartType} onToggle={onCostChartToggle} data-testid="dashboard-cost-chart-toggle" />
						</>
					}
				>
					<CostChart data={costData} chartType={costChartType} startTime={startTime} endTime={endTime} selectedModel={costModel} />
				</ChartCard>

				{/* Model Usage Chart */}
				<ChartCard
					title="Model Usage"
					loading={loadingModels}
					testId="chart-model-usage"
					totalLabel="Total"
					total={modelUsageTotal !== null ? <NumberFlow value={modelUsageTotal} format={COMPACT_NUMBER_FORMAT} /> : undefined}
					totalTooltip={modelUsageTotal !== null ? modelUsageTotal.toLocaleString("en-US") : undefined}
					legend={
						<div className={CHART_HEADER_LEGEND_CLASS}>
							{usageModel === "all" ? (
								usageModels.length > 0 && (
									<>
										<Tooltip>
											<TooltipTrigger asChild>
												<span tabIndex={0} data-testid="usage-legend-trigger" className="flex items-center gap-1">
													<span className="h-2 w-2 shrink-0 rounded-full" style={{ backgroundColor: getModelColor(0) }} />
													<span className="text-muted-foreground max-w-[100px] truncate">{usageModels[0]}</span>
												</span>
											</TooltipTrigger>
											<TooltipContent>{usageModels[0]}</TooltipContent>
										</Tooltip>
										{usageModels.length > 1 && (
											<Tooltip>
												<TooltipTrigger asChild>
													<span tabIndex={0} data-testid="usage-legend-more-trigger" className="text-muted-foreground cursor-default">
														+{usageModels.length - 1} more
													</span>
												</TooltipTrigger>
												<TooltipContent>
													<div className="flex flex-col gap-1">
														{usageModels.slice(1).map((model, idx) => (
															<span key={model} className="flex items-center gap-1">
																<span
																	className="h-2 w-2 shrink-0 rounded-full"
																	style={{
																		backgroundColor: getModelColor(idx + 1),
																	}}
																/>
																{model}
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
										<span className="h-2 w-2 shrink-0 rounded-full" style={{ backgroundColor: CHART_COLORS.success }} />
										<span className="text-muted-foreground">Success</span>
									</span>
									<span className="flex items-center gap-1">
										<span className="h-2 w-2 shrink-0 rounded-full" style={{ backgroundColor: CHART_COLORS.error }} />
										<span className="text-muted-foreground">Error</span>
									</span>
									<span className="flex items-center gap-1">
										<span className="h-2 w-2 shrink-0 rounded-full" style={{ backgroundColor: CHART_COLORS.cancelled }} />
										<span className="text-muted-foreground">Cancelled</span>
									</span>
								</>
							)}
						</div>
					}
					controls={
						<>
							<ModelFilterSelect
								models={availableModels}
								selectedModel={usageModel}
								onModelChange={onUsageModelChange}
								data-testid="dashboard-usage-model-filter"
							/>
							<ChartTypeToggle chartType={modelChartType} onToggle={onModelChartToggle} data-testid="dashboard-usage-chart-toggle" />
						</>
					}
				>
					<ModelUsageChart data={modelData} chartType={modelChartType} startTime={startTime} endTime={endTime} selectedModel={usageModel} />
				</ChartCard>

				{/* Latency Chart */}
				<ChartCard
					title="Latency"
					loading={loadingLatency}
					testId="chart-latency"
					totalLabel="Avg"
					total={
						latencyAvg !== null ? (
							<NumberFlow value={latencyAvg} format={{ minimumFractionDigits: 2, maximumFractionDigits: 2 }} suffix="ms" />
						) : undefined
					}
					totalTooltip={latencyAvg !== null ? `${latencyAvg.toLocaleString("en-US", { maximumFractionDigits: 6 })}ms` : undefined}
					legend={
						<div className={CHART_HEADER_LEGEND_CLASS}>
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
						</div>
					}
					controls={
						<ChartTypeToggle chartType={latencyChartType} onToggle={onLatencyChartToggle} data-testid="dashboard-latency-chart-toggle" />
					}
				>
					<LatencyChart data={latencyData} chartType={latencyChartType} startTime={startTime} endTime={endTime} />
				</ChartCard>

				{/* Throughput (tokens/sec) Chart */}
				<ChartCard
					title="Throughput"
					loading={loadingThroughput}
					testId="chart-throughput"
					totalLabel="Avg"
					total={throughputAvg !== null ? <span className="truncate whitespace-nowrap">{formatTokensPerSecond(throughputAvg)}</span> : undefined}
					totalTooltip={
						throughputAvg !== null ? `${throughputAvg.toLocaleString("en-US", { maximumFractionDigits: 2 })} tokens/sec` : undefined
					}
					legend={
						<div className={CHART_HEADER_LEGEND_CLASS}>
							<span className="flex items-center gap-1">
								<span className="h-2 w-2 rounded-full" style={{ backgroundColor: THROUGHPUT_COLOR }} />
								<span className="text-muted-foreground">Tokens/sec</span>
							</span>
						</div>
					}
					controls={
						<ChartTypeToggle chartType={throughputChartType} onToggle={onThroughputChartToggle} data-testid="dashboard-throughput-chart-toggle" />
					}
				>
					<ThroughputChart data={throughputData} chartType={throughputChartType} startTime={startTime} endTime={endTime} />
				</ChartCard>
			</div>
		</>
	);
}
export const OverviewTab = memo(OverviewTabImpl);
