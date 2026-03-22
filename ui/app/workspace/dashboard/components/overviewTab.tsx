"use client";

import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import type {
	CostHistogramResponse,
	LatencyHistogramResponse,
	LogsHistogramResponse,
	ModelHistogramResponse,
	TokenHistogramResponse,
} from "@/lib/types/logs";
import {
	CHART_COLORS,
	CHART_HEADER_ACTIONS_CLASS,
	CHART_HEADER_CONTROLS_CLASS,
	CHART_HEADER_LEGEND_CLASS,
	LATENCY_COLORS,
	getModelColor,
} from "../utils/chartUtils";
import CacheTokenMeterChart from "./charts/cacheTokenMeterChart";
import { ChartCard } from "./charts/chartCard";
import { type ChartType, ChartTypeToggle } from "./charts/chartTypeToggle";
import { CostChart } from "./charts/costChart";
import { LatencyChart } from "./charts/latencyChart";
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

	// Loading states
	loadingHistogram: boolean;
	loadingTokens: boolean;
	loadingCost: boolean;
	loadingModels: boolean;
	loadingLatency: boolean;

	// Time range
	startTime: number;
	endTime: number;

	// Chart types
	volumeChartType: ChartType;
	tokenChartType: ChartType;
	costChartType: ChartType;
	modelChartType: ChartType;
	latencyChartType: ChartType;

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

	// Filter callbacks
	onCostModelChange: (model: string) => void;
	onUsageModelChange: (model: string) => void;
}

export function OverviewTab({
	histogramData,
	tokenData,
	costData,
	modelData,
	latencyData,
	loadingHistogram,
	loadingTokens,
	loadingCost,
	loadingModels,
	loadingLatency,
	startTime,
	endTime,
	volumeChartType,
	tokenChartType,
	costChartType,
	modelChartType,
	latencyChartType,
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
	onCostModelChange,
	onUsageModelChange,
}: OverviewTabProps) {
	return (
		<>
			{/* Charts Grid */}
			<div className="grid grid-cols-1 gap-2 lg:grid-cols-2 2xl:grid-cols-3">
				{/* Log Volume Chart */}
				<ChartCard
					title="Request Volume"
					loading={loadingHistogram}
					testId="chart-log-volume"
					headerActions={
						<div className={CHART_HEADER_ACTIONS_CLASS}>
							<div className={CHART_HEADER_LEGEND_CLASS}>
								<span className="flex items-center gap-1">
									<span className="h-2 w-2 rounded-full" style={{ backgroundColor: CHART_COLORS.success }} />
									<span className="text-muted-foreground">Success</span>
								</span>
								<span className="flex items-center gap-1">
									<span className="h-2 w-2 rounded-full" style={{ backgroundColor: CHART_COLORS.error }} />
									<span className="text-muted-foreground">Error</span>
								</span>
							</div>
							<div className={CHART_HEADER_CONTROLS_CLASS}>
								<ChartTypeToggle chartType={volumeChartType} onToggle={onVolumeChartToggle} data-testid="dashboard-volume-chart-toggle" />
							</div>
						</div>
					}
				>
					<LogVolumeChart data={histogramData} chartType={volumeChartType} startTime={startTime} endTime={endTime} />
				</ChartCard>

				{/* Token Usage Chart */}
				<ChartCard
					title="Token Usage"
					loading={loadingTokens}
					testId="chart-token-usage"
					headerActions={
						<div className={CHART_HEADER_ACTIONS_CLASS}>
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
							<div className={CHART_HEADER_CONTROLS_CLASS}>
								<ChartTypeToggle chartType={tokenChartType} onToggle={onTokenChartToggle} data-testid="dashboard-token-chart-toggle" />
							</div>
						</div>
					}
				>
					<TokenUsageChart data={tokenData} chartType={tokenChartType} startTime={startTime} endTime={endTime} />
				</ChartCard>

				{/* Cache Hit Rate Meter */}
				<ChartCard title="Cache Hit Rate" loading={loadingTokens} testId="chart-cache-meter">
					<CacheTokenMeterChart data={tokenData} />
				</ChartCard>

				{/* Cost Chart */}
				<ChartCard
					title="Cost"
					loading={loadingCost}
					testId="chart-cost-total"
					headerActions={
						<div className={CHART_HEADER_ACTIONS_CLASS}>
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
														<span tabIndex={0} data-testid="cost-legend-more-trigger" className="text-muted-foreground cursor-default">+{costModels.length - 1} more</span>
													</TooltipTrigger>
													<TooltipContent>
														<div className="flex flex-col gap-1">
															{costModels.slice(1).map((model, idx) => (
																<span key={model} className="flex items-center gap-1">
																	<span className="h-2 w-2 shrink-0 rounded-full" style={{ backgroundColor: getModelColor(idx + 1) }} />
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
							<div className={CHART_HEADER_CONTROLS_CLASS}>
								<ModelFilterSelect
									models={availableModels}
									selectedModel={costModel}
									onModelChange={onCostModelChange}
									data-testid="dashboard-cost-model-filter"
								/>
								<ChartTypeToggle chartType={costChartType} onToggle={onCostChartToggle} data-testid="dashboard-cost-chart-toggle" />
							</div>
						</div>
					}
				>
					<CostChart data={costData} chartType={costChartType} startTime={startTime} endTime={endTime} selectedModel={costModel} />
				</ChartCard>

				{/* Model Usage Chart */}
				<ChartCard
					title="Model Usage"
					loading={loadingModels}
					testId="chart-model-usage"
					headerActions={
						<div className={CHART_HEADER_ACTIONS_CLASS}>
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
														<span tabIndex={0} data-testid="usage-legend-more-trigger" className="text-muted-foreground cursor-default">+{usageModels.length - 1} more</span>
													</TooltipTrigger>
													<TooltipContent>
														<div className="flex flex-col gap-1">
															{usageModels.slice(1).map((model, idx) => (
																<span key={model} className="flex items-center gap-1">
																	<span className="h-2 w-2 shrink-0 rounded-full" style={{ backgroundColor: getModelColor(idx + 1) }} />
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
									</>
								)}
							</div>
							<div className={CHART_HEADER_CONTROLS_CLASS}>
								<ModelFilterSelect
									models={availableModels}
									selectedModel={usageModel}
									onModelChange={onUsageModelChange}
									data-testid="dashboard-usage-model-filter"
								/>
								<ChartTypeToggle chartType={modelChartType} onToggle={onModelChartToggle} data-testid="dashboard-usage-chart-toggle" />
							</div>
						</div>
					}
				>
					<ModelUsageChart data={modelData} chartType={modelChartType} startTime={startTime} endTime={endTime} selectedModel={usageModel} />
				</ChartCard>

				{/* Latency Chart */}
				<ChartCard
					title="Latency"
					loading={loadingLatency}
					testId="chart-latency"
					headerActions={
						<div className={CHART_HEADER_ACTIONS_CLASS}>
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
							<div className={CHART_HEADER_CONTROLS_CLASS}>
								<ChartTypeToggle
									chartType={latencyChartType}
									onToggle={onLatencyChartToggle}
									data-testid="dashboard-latency-chart-toggle"
								/>
							</div>
						</div>
					}
				>
					<LatencyChart data={latencyData} chartType={latencyChartType} startTime={startTime} endTime={endTime} />
				</ChartCard>
			</div>
		</>
	);
}
