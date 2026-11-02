import {
	averaged,
	formatCount,
	muted,
	negative,
	positive,
	RatioBar,
	Sparkline,
	toneClass,
	warning,
} from "@/app/workspace/logs/views/metricStrip";
import {
	buildSparkPoints,
	formatMs,
	formatPctChange,
	formatPointDelta,
	metricsState,
	type SeriesPoint,
	windowTrend,
} from "@/app/workspace/logs/views/metricStrip.utils";
import { MetricSegment, MetricStripCard, MetricTooltipBody, MetricTooltipRow, MetricTrailing, MetricUnit } from "@/components/metricStrip";
import type { RelayHistogramResponse, RelayLogStatsResponse } from "@/lib/types/relays";
import { COMPACT_NUMBER_FORMAT, formatCurrencyNumber } from "@/lib/utils/numbers";
import NumberFlow from "@number-flow/react";
import { useMemo } from "react";
import { useTranslation } from "react-i18next";

interface RelayMetricStripProps {
	stats?: RelayLogStatsResponse;
	/** The relay histogram; every sparkline reads one of its series. */
	histogram?: RelayHistogramResponse;
	loading?: boolean;
	/** The /relay-logs/stats failure, if it failed; unknown so the strip does not depend on RTK Query's error union. */
	error?: unknown;
}

/**
 * The summary row above the relay logs table, built on the same tiles, sparklines and
 * trend chips as the LLM page's MetricStrip. One histogram carries every series, so the
 * sparklines cost no request beyond the one the volume chart already makes.
 */
export function RelayMetricStrip({ stats, histogram, loading, error }: RelayMetricStripProps) {
	const { t } = useTranslation("observability");
	const state = metricsState(stats, loading ?? false, error);
	const buckets = useMemo(() => histogram?.buckets ?? [], [histogram]);
	const bucketSizeSeconds = histogram?.bucket_size_seconds;

	const requestPoints = useMemo(
		() => buildSparkPoints(buckets.map<SeriesPoint>((b) => ({ timestamp: b.timestamp, value: b.count }))),
		[buckets],
	);
	const errorPoints = useMemo(
		() => buildSparkPoints(buckets.map<SeriesPoint>((b) => ({ timestamp: b.timestamp, value: b.error + b.cancelled }))),
		[buckets],
	);
	const latencyPoints = useMemo(
		() => buildSparkPoints(buckets.map<SeriesPoint>((b) => ({ timestamp: b.timestamp, value: b.avg_latency ?? 0 }))),
		[buckets],
	);
	const costPoints = useMemo(
		() => buildSparkPoints(buckets.map<SeriesPoint>((b) => ({ timestamp: b.timestamp, value: b.cost }))),
		[buckets],
	);

	const previous = stats?.has_previous_period ? stats.previous : undefined;
	const totalRequests = stats?.total_requests ?? 0;
	const successRate = stats?.success_rate ?? 0;
	const totalCost = stats?.total_cost ?? 0;
	const costPerRequest = totalRequests > 0 ? totalCost / totalRequests : 0;
	// success_rate is measured over succeeded plus failed calls, so the counts come from the
	// server's own figures and cancellations are shown on their own rather than folded in.
	const succeeded = stats?.success_count ?? 0;
	const failed = stats?.error_count ?? 0;
	const cancelled = Math.max(0, totalRequests - succeeded - failed);

	const requestsChange = previous ? formatPctChange(totalRequests, previous.total_requests) : undefined;
	// With no previous period to compare against, the window's own direction fills the slot.
	const requestsSignal = requestsChange?.text ? requestsChange : windowTrend(requestPoints.map((point) => point.value));
	const successChange = previous ? formatPointDelta(successRate, previous.success_rate) : undefined;
	const latencyChange = previous ? formatPctChange(stats?.average_latency ?? 0, previous.average_latency, "lower-is-better") : undefined;
	const costChange = previous ? formatPctChange(totalCost, previous.total_cost, "lower-is-better") : undefined;

	return (
		<div className="@container/metric-strip shrink-0">
			<MetricStripCard className="grid-cols-2 @5xl/metric-strip:grid-cols-4" loading={loading} testId="relay-logs-metric-strip">
				<MetricSegment
					state={state}
					title={t("logs.stats.totalRequests")}
					testId="relay-logs-stat-requests"
					footer={
						<>
							<Sparkline
								points={requestPoints}
								bucketSizeSeconds={bucketSizeSeconds}
								className={toneClass[requestsSignal.tone]}
								rows={(point, index) => (
									<>
										<MetricTooltipRow name={t("logs.stats.requests")}>{averaged(point, formatCount(point.value))}</MetricTooltipRow>
										{errorPoints[index] && errorPoints[index].value > 0 && (
											<MetricTooltipRow name={t("logs.stats.failed")} className={negative}>
												{averaged(point, formatCount(errorPoints[index].value))}
											</MetricTooltipRow>
										)}
									</>
								)}
							/>
							{requestsSignal.text && (
								<MetricTrailing testId="relay-logs-metric-trailing" className={toneClass[requestsSignal.tone]}>
									{requestsSignal.text}
								</MetricTrailing>
							)}
						</>
					}
				>
					<NumberFlow value={totalRequests} format={COMPACT_NUMBER_FORMAT} />
				</MetricSegment>

				<MetricSegment
					state={state}
					title={t("logs.stats.successRate")}
					testId="relay-logs-stat-success"
					footer={
						<>
							<RatioBar
								percent={successRate}
								tooltip={
									<MetricTooltipBody heading={t("relayLogs.stats.ofAllCalls")}>
										<MetricTooltipRow name={t("logs.stats.succeeded")} className={positive}>
											{formatCount(succeeded)}
										</MetricTooltipRow>
										<MetricTooltipRow name={t("logs.stats.failed")} className={negative}>
											{formatCount(failed)}
										</MetricTooltipRow>
										{cancelled > 0 && <MetricTooltipRow name={t("labels.cancelled")}>{formatCount(cancelled)}</MetricTooltipRow>}
										<MetricTooltipRow name={t("labels.total")}>{formatCount(totalRequests)}</MetricTooltipRow>
										{previous && <MetricTooltipRow name={t("logs.stats.previousPeriod")}>{`${previous.success_rate.toFixed(2)}%`}</MetricTooltipRow>}
									</MetricTooltipBody>
								}
							/>
							{successChange && (
								<MetricTrailing testId="relay-logs-metric-trailing" className={toneClass[successChange.tone]}>
									{successChange.text}
								</MetricTrailing>
							)}
						</>
					}
				>
					<NumberFlow value={successRate} format={{ minimumFractionDigits: 2, maximumFractionDigits: 2 }} />
					<MetricUnit>%</MetricUnit>
				</MetricSegment>

				<MetricSegment
					state={state}
					title={t("logs.stats.avgLatency")}
					testId="relay-logs-stat-latency"
					footer={
						<>
							<Sparkline
								points={latencyPoints}
								bucketSizeSeconds={bucketSizeSeconds}
								className={warning}
								rows={(point) => <MetricTooltipRow name={t("logs.stats.tooltipAverage")}>{averaged(point, formatMs(point.value))}</MetricTooltipRow>}
							/>
							{latencyChange?.text && (
								<MetricTrailing testId="relay-logs-metric-trailing" className={toneClass[latencyChange.tone]}>
									{latencyChange.text}
								</MetricTrailing>
							)}
						</>
					}
				>
					<NumberFlow value={Math.round(stats?.average_latency ?? 0)} />
					<MetricUnit>ms</MetricUnit>
				</MetricSegment>

				<MetricSegment
					state={state}
					title={t("logs.stats.totalCost")}
					testId="relay-logs-stat-cost"
					footer={
						<>
							<Sparkline
								points={costPoints}
								bucketSizeSeconds={bucketSizeSeconds}
								className={costChange ? toneClass[costChange.tone] : muted}
								rows={(point) => <MetricTooltipRow name={t("labels.cost")}>{averaged(point, formatCurrencyNumber(point.value))}</MetricTooltipRow>}
							/>
							{costChange?.text ? (
								<MetricTrailing testId="relay-logs-metric-trailing" className={toneClass[costChange.tone]}>
									{costChange.text}
								</MetricTrailing>
							) : (
								costPerRequest > 0 && (
									<MetricTrailing testId="relay-logs-metric-trailing" className={muted}>
										{t("relayLogs.stats.perRequest", { value: formatCurrencyNumber(costPerRequest) })}
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