import {
	averaged,
	formatCount,
	LatencyMetricSegment,
	muted,
	negative,
	positive,
	RatioBar,
	Sparkline,
	toneClass,
} from "@/app/workspace/logs/views/metricStrip";
import {
	buildSparkPoints,
	formatPctChange,
	formatPointDelta,
	metricsState,
	type SeriesPoint,
	windowTrend,
} from "@/app/workspace/logs/views/metricStrip.utils";
import { MetricSegment, MetricStripCard, MetricTooltipBody, MetricTooltipRow, MetricTrailing, MetricUnit } from "@/components/metricStrip";
import type { LatencyHistogramResponse, MCPCostHistogramResponse, MCPHistogramResponse, MCPToolLogStatsResponse } from "@/lib/types/logs";
import { COMPACT_NUMBER_FORMAT, formatCurrencyNumber } from "@/lib/utils/numbers";
import NumberFlow from "@number-flow/react";
import { useMemo } from "react";
import { useTranslation } from "react-i18next";

interface MCPMetricStripProps {
	stats?: MCPToolLogStatsResponse;
	/** The MCP volume histogram, read by the executions sparkline and its failed counts. */
	histogram?: MCPHistogramResponse;
	/** The MCP cost histogram, read by the Total Cost sparkline as the LLM strip reads its cost histogram. */
	costHistogram?: MCPCostHistogramResponse;
	/** The MCP latency percentiles, read by the Avg Latency tile as on the LLM and Relay strips. */
	latencyHistogram?: LatencyHistogramResponse;
	loading?: boolean;
	/** The /mcp-logs/stats failure, if it failed; unknown so the strip does not depend on RTK Query's error union. */
	error?: unknown;
}

/**
 * The summary row above the MCP logs table, built on the same tiles, sparklines and trend chips as the
 * LLM page's MetricStrip and the Relay page's RelayMetricStrip.
 */
export function MCPMetricStrip({ stats, histogram, costHistogram, latencyHistogram, loading, error }: MCPMetricStripProps) {
	const { t } = useTranslation("observability");
	const state = metricsState(stats, loading ?? false, error);
	const buckets = useMemo(() => histogram?.buckets ?? [], [histogram]);
	const bucketSizeSeconds = histogram?.bucket_size_seconds;

	const executionPoints = useMemo(
		() => buildSparkPoints(buckets.map<SeriesPoint>((b) => ({ timestamp: b.timestamp, value: b.count }))),
		[buckets],
	);
	const errorPoints = useMemo(
		() => buildSparkPoints(buckets.map<SeriesPoint>((b) => ({ timestamp: b.timestamp, value: b.error + (b.cancelled ?? 0) }))),
		[buckets],
	);
	const costPoints = useMemo(
		() => buildSparkPoints((costHistogram?.buckets ?? []).map<SeriesPoint>((b) => ({ timestamp: b.timestamp, value: b.total_cost }))),
		[costHistogram],
	);

	const previous = stats?.has_previous_period ? stats.previous : undefined;
	const totalExecutions = stats?.total_executions ?? 0;
	const successRate = stats?.success_rate ?? 0;
	const totalCost = stats?.total_cost ?? 0;
	const costPerExecution = totalExecutions > 0 ? totalCost / totalExecutions : 0;
	// The server's own counts, as the Relay strip reads them: success_rate is measured over every
	// finished call, cancellations included.
	const succeeded = stats?.success_count ?? 0;
	const failed = stats?.error_count ?? 0;
	const cancelled = stats?.cancelled_count ?? 0;

	const executionsChange = previous ? formatPctChange(totalExecutions, previous.total_executions) : undefined;
	// With no previous period to compare against, the window's own direction fills the slot.
	const executionsSignal = executionsChange?.text ? executionsChange : windowTrend(executionPoints.map((point) => point.value));
	const successChange = previous ? formatPointDelta(successRate, previous.success_rate) : undefined;
	const costChange = previous ? formatPctChange(totalCost, previous.total_cost, "lower-is-better") : undefined;

	return (
		<div className="@container/metric-strip shrink-0">
			<MetricStripCard className="grid-cols-2 @5xl/metric-strip:grid-cols-4" loading={loading} testId="mcp-logs-metric-strip">
				<MetricSegment
					state={state}
					title={t("mcpLogs.stats.totalExecutions")}
					testId="mcp-logs-stat-executions"
					footer={
						<>
							<Sparkline
								points={executionPoints}
								bucketSizeSeconds={bucketSizeSeconds}
								className={toneClass[executionsSignal.tone]}
								rows={(point, index) => (
									<>
										<MetricTooltipRow name={t("mcpLogs.stats.executions")}>{averaged(point, formatCount(point.value), t)}</MetricTooltipRow>
										{errorPoints[index] && errorPoints[index].value > 0 && (
											<MetricTooltipRow name={t("logs.stats.failed")} className={negative}>
												{averaged(point, formatCount(errorPoints[index].value), t)}
											</MetricTooltipRow>
										)}
									</>
								)}
							/>
							{executionsSignal.text && (
								<MetricTrailing testId="mcp-logs-metric-trailing" className={toneClass[executionsSignal.tone]}>
									{/* windowTrend's fallback labels are English, so they are translated here as on the LLM strip. */}
									{executionsSignal.text === "↗ rising"
										? t("logs.stats.rising")
										: executionsSignal.text === "↘ falling"
											? t("logs.stats.falling")
											: executionsSignal.text === "→ steady"
												? t("logs.stats.steady")
												: executionsSignal.text}
								</MetricTrailing>
							)}
						</>
					}
				>
					<NumberFlow value={totalExecutions} format={COMPACT_NUMBER_FORMAT} />
				</MetricSegment>

				<MetricSegment
					state={state}
					title={t("mcpLogs.stats.successRate")}
					testId="mcp-logs-stat-success"
					footer={
						<>
							<RatioBar
								percent={successRate}
								tooltip={
									<MetricTooltipBody heading={t("mcpLogs.stats.ofAllExecutions")}>
										<MetricTooltipRow name={t("logs.stats.succeeded")} className={positive}>
											{formatCount(succeeded)}
										</MetricTooltipRow>
										<MetricTooltipRow name={t("logs.stats.failed")} className={negative}>
											{formatCount(failed)}
										</MetricTooltipRow>
										{cancelled > 0 && <MetricTooltipRow name={t("labels.cancelled")}>{formatCount(cancelled)}</MetricTooltipRow>}
										<MetricTooltipRow name={t("labels.total")}>{formatCount(totalExecutions)}</MetricTooltipRow>
										{previous && (
											<MetricTooltipRow name={t("logs.stats.previousPeriod")}>{`${previous.success_rate.toFixed(2)}%`}</MetricTooltipRow>
										)}
									</MetricTooltipBody>
								}
							/>
							{successChange && (
								<MetricTrailing testId="mcp-logs-metric-trailing" className={toneClass[successChange.tone]}>
									{successChange.text}
								</MetricTrailing>
							)}
						</>
					}
				>
					<NumberFlow value={successRate} format={{ minimumFractionDigits: 2, maximumFractionDigits: 2 }} />
					<MetricUnit>%</MetricUnit>
				</MetricSegment>

				<LatencyMetricSegment
					state={state}
					averageLatency={stats?.average_latency}
					latencyHistogram={latencyHistogram}
					testId="mcp-logs-stat-latency"
					trailingTestId="mcp-logs-metric-trailing"
				/>

				<MetricSegment
					state={state}
					title={t("mcpLogs.stats.totalCost")}
					testId="mcp-logs-stat-cost"
					footer={
						<>
							<Sparkline
								points={costPoints}
								bucketSizeSeconds={costHistogram?.bucket_size_seconds}
								className={costChange ? toneClass[costChange.tone] : muted}
								rows={(point) => (
									<MetricTooltipRow name={t("labels.cost")}>{averaged(point, formatCurrencyNumber(point.value), t)}</MetricTooltipRow>
								)}
							/>
							{costChange?.text ? (
								<MetricTrailing testId="mcp-logs-metric-trailing" className={toneClass[costChange.tone]}>
									{costChange.text}
								</MetricTrailing>
							) : (
								costPerExecution > 0 && (
									<MetricTrailing testId="mcp-logs-metric-trailing" className={muted}>
										{t("mcpLogs.stats.perExecution", { value: formatCurrencyNumber(costPerExecution) })}
									</MetricTrailing>
								)
							)}
						</>
					}
				>
					<NumberFlow value={totalCost} format={{ ...COMPACT_NUMBER_FORMAT, style: "currency", currency: "USD" }} />
				</MetricSegment>
			</MetricStripCard>
		</div>
	);
}