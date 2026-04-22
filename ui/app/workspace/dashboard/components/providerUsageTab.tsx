import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import type { ProviderCostHistogramResponse, ProviderLatencyHistogramResponse, ProviderTokenHistogramResponse } from "@/lib/types/logs";
import {
	CHART_COLORS,
	CHART_HEADER_ACTIONS_CLASS,
	CHART_HEADER_CONTROLS_CLASS,
	CHART_HEADER_LEGEND_CLASS,
	LATENCY_COLORS,
	getModelColor,
} from "../utils/chartUtils";
import { ChartCard } from "./charts/chartCard";
import { type ChartType, ChartTypeToggle } from "./charts/chartTypeToggle";
import { ProviderCostChart } from "./charts/providerCostChart";
import { ProviderFilterSelect } from "./charts/providerFilterSelect";
import { ProviderLatencyChart } from "./charts/providerLatencyChart";
import { ProviderTokenChart } from "./charts/providerTokenChart";

export interface ProviderUsageTabProps {
	// Data
	providerCostData: ProviderCostHistogramResponse | null;
	providerTokenData: ProviderTokenHistogramResponse | null;
	providerLatencyData: ProviderLatencyHistogramResponse | null;

	// Loading states
	loadingProviderCost: boolean;
	loadingProviderTokens: boolean;
	loadingProviderLatency: boolean;

	// Time range
	startTime: number;
	endTime: number;

	// Chart types
	providerCostChartType: ChartType;
	providerTokenChartType: ChartType;
	providerLatencyChartType: ChartType;

	// Provider selections
	providerCostProvider: string;
	providerTokenProvider: string;
	providerLatencyProvider: string;

	// Derived provider lists
	availableProviders: string[];
	providerCostProviders: string[];
	providerTokenProviders: string[];
	providerLatencyProviders: string[];

	// Chart type toggle callbacks
	onProviderCostChartToggle: (type: ChartType) => void;
	onProviderTokenChartToggle: (type: ChartType) => void;
	onProviderLatencyChartToggle: (type: ChartType) => void;

	// Filter callbacks
	onProviderCostProviderChange: (provider: string) => void;
	onProviderTokenProviderChange: (provider: string) => void;
	onProviderLatencyProviderChange: (provider: string) => void;
}

export function ProviderUsageTab({
	providerCostData,
	providerTokenData,
	providerLatencyData,
	loadingProviderCost,
	loadingProviderTokens,
	loadingProviderLatency,
	startTime,
	endTime,
	providerCostChartType,
	providerTokenChartType,
	providerLatencyChartType,
	providerCostProvider,
	providerTokenProvider,
	providerLatencyProvider,
	availableProviders,
	providerCostProviders,
	providerTokenProviders,
	providerLatencyProviders,
	onProviderCostChartToggle,
	onProviderTokenChartToggle,
	onProviderLatencyChartToggle,
	onProviderCostProviderChange,
	onProviderTokenProviderChange,
	onProviderLatencyProviderChange,
}: ProviderUsageTabProps) {
	return (
		<div className="grid grid-cols-1 gap-2 lg:grid-cols-2 2xl:grid-cols-3">
			{/* Provider Cost Chart */}
			<ChartCard
				title="Provider Cost"
				loading={loadingProviderCost}
				testId="chart-provider-cost"
				headerActions={
					<div className={CHART_HEADER_ACTIONS_CLASS}>
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
										<span data-testid="provider-cost-legend-single-trigger" className="flex items-center gap-1">
											<span className="h-2 w-2 shrink-0 rounded-full" style={{ backgroundColor: getModelColor(0) }} />
											<span className="text-muted-foreground max-w-[100px] truncate">{providerCostProvider}</span>
										</span>
									</TooltipTrigger>
									<TooltipContent>{providerCostProvider}</TooltipContent>
								</Tooltip>
							)}
						</div>
						<div className={CHART_HEADER_CONTROLS_CLASS}>
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
						</div>
					</div>
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
				headerActions={
					<div className={CHART_HEADER_ACTIONS_CLASS}>
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
						<div className={CHART_HEADER_CONTROLS_CLASS}>
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
						</div>
					</div>
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
				headerActions={
					<div className={CHART_HEADER_ACTIONS_CLASS}>
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
						<div className={CHART_HEADER_CONTROLS_CLASS}>
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
						</div>
					</div>
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
		</div>
	);
}