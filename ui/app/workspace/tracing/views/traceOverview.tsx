import { averaged, formatCount, muted, negative, positive, RatioBar, Sparkline } from "@/app/workspace/logs/views/metricStrip";
import { buildSparkPoints, metricsState, type SeriesPoint } from "@/app/workspace/logs/views/metricStrip.utils";
import { MetricSegment, MetricStripCard, MetricTooltipBody, MetricTooltipRow, MetricTrailing, MetricUnit } from "@/components/metricStrip";
import type { LogsHistogramResponse } from "@/lib/types/logs";
import type { TraceHistogramResponse, TraceStatsResponse } from "@/lib/types/traces";
import { COMPACT_NUMBER_FORMAT, formatCurrencyNumber } from "@/lib/utils/numbers";
import NumberFlow from "@number-flow/react";
import { useMemo } from "react";
import { useTranslation } from "react-i18next";
import { formatCost, formatDuration } from "./format";

/** The trace histogram in the shape the logs page's volume chart reads. */
export function toVolumeHistogram(histogram?: TraceHistogramResponse): LogsHistogramResponse | null {
	if (!histogram) return null;
	return {
		bucket_size_seconds: histogram.bucket_size_seconds,
		buckets: (histogram.buckets ?? []).map((bucket) => ({
			timestamp: bucket.timestamp,
			count: bucket.count,
			success: bucket.count - bucket.errors,
			error: bucket.errors,
			cancelled: 0,
		})),
	};
}

/**
 * The summary row above the traces table, built on the same tiles and sparklines as the LLM logs
 * page. Every figure counts a model call once: a call served by this gateway with the gateway's
 * numbers, any other with the SDK's.
 */
export function TraceMetricStrip({
	data,
	histogram,
	loading,
	error,
}: {
	data?: TraceStatsResponse;
	histogram?: TraceHistogramResponse;
	loading?: boolean;
	error?: unknown;
}) {
	const { t } = useTranslation("observability");
	const stats = data?.stats;
	const state = metricsState(stats, loading ?? false, error);
	const buckets = useMemo(() => histogram?.buckets ?? [], [histogram]);
	const bucketSizeSeconds = histogram?.bucket_size_seconds;
	const tracePoints = useMemo(
		() => buildSparkPoints(buckets.map<SeriesPoint>((b) => ({ timestamp: b.timestamp, value: b.count }))),
		[buckets],
	);
	const tokenPoints = useMemo(
		() => buildSparkPoints(buckets.map<SeriesPoint>((b) => ({ timestamp: b.timestamp, value: b.total_tokens }))),
		[buckets],
	);
	const costPoints = useMemo(
		() => buildSparkPoints(buckets.map<SeriesPoint>((b) => ({ timestamp: b.timestamp, value: b.total_cost }))),
		[buckets],
	);

	const traceCount = stats?.trace_count ?? 0;
	const errorTraces = stats?.error_trace_count ?? 0;
	const successRate = traceCount > 0 ? ((traceCount - errorTraces) / traceCount) * 100 : 0;
	const linked = stats?.linked_span_count ?? 0;
	const llmCalls = stats?.llm_span_count ?? 0;

	return (
		<div className="@container/metric-strip shrink-0">
			<MetricStripCard className="grid-cols-2 @5xl/metric-strip:grid-cols-4" loading={loading} testId="tracing-metric-strip">
				<MetricSegment
					state={state}
					title={t("tracing.overview.traces")}
					testId="tracing-stat-traces"
					footer={
						<>
							<Sparkline
								points={tracePoints}
								bucketSizeSeconds={bucketSizeSeconds}
								className={muted}
								rows={(point) => (
									<MetricTooltipRow name={t("tracing.overview.traces")}>{averaged(point, formatCount(point.value), t)}</MetricTooltipRow>
								)}
							/>
							{(stats?.in_progress_count ?? 0) > 0 && (
								<MetricTrailing className={muted} testId="tracing-stat-in-progress">
									{t("tracing.overview.inProgress", { count: stats?.in_progress_count ?? 0 })}
								</MetricTrailing>
							)}
						</>
					}
				>
					<NumberFlow value={traceCount} format={COMPACT_NUMBER_FORMAT} />
				</MetricSegment>

				<MetricSegment
					state={state}
					title={t("logs.stats.successRate")}
					testId="tracing-stat-success"
					footer={
						<>
							<RatioBar
								percent={successRate}
								tooltip={
									<MetricTooltipBody heading={t("tracing.overview.traces")}>
										<MetricTooltipRow name={t("tracing.status.ok")} className={positive}>
											{formatCount(traceCount - errorTraces)}
										</MetricTooltipRow>
										<MetricTooltipRow name={t("tracing.status.error")} className={negative}>
											{formatCount(errorTraces)}
										</MetricTooltipRow>
										<MetricTooltipRow name={t("tracing.overview.avgDuration")}>{formatDuration(stats?.avg_duration_ms)}</MetricTooltipRow>
									</MetricTooltipBody>
								}
							/>
							<MetricTrailing className={muted}>{formatDuration(stats?.avg_duration_ms)}</MetricTrailing>
						</>
					}
				>
					<NumberFlow value={successRate} format={{ minimumFractionDigits: 2, maximumFractionDigits: 2 }} />
					<MetricUnit>%</MetricUnit>
				</MetricSegment>

				<MetricSegment
					state={state}
					title={t("tracing.columns.tokens")}
					testId="tracing-stat-tokens"
					footer={
						<>
							<Sparkline
								points={tokenPoints}
								bucketSizeSeconds={bucketSizeSeconds}
								className={muted}
								rows={(point) => (
									<MetricTooltipRow name={t("tracing.columns.tokens")}>{averaged(point, formatCount(point.value), t)}</MetricTooltipRow>
								)}
							/>
							{llmCalls > 0 && (
								<MetricTrailing className={muted} testId="tracing-stat-linked">
									{t("tracing.overview.linkedShort", { percent: Math.round((linked / llmCalls) * 100) })}
								</MetricTrailing>
							)}
						</>
					}
				>
					<NumberFlow value={stats?.total_tokens ?? 0} format={COMPACT_NUMBER_FORMAT} />
				</MetricSegment>

				<MetricSegment
					state={state}
					title={t("tracing.columns.cost")}
					testId="tracing-stat-cost"
					footer={
						<>
							<Sparkline
								points={costPoints}
								bucketSizeSeconds={bucketSizeSeconds}
								className={muted}
								rows={(point) => (
									<MetricTooltipRow name={t("tracing.columns.cost")}>{averaged(point, formatCost(point.value), t)}</MetricTooltipRow>
								)}
							/>
							{((stats?.estimated_cost ?? 0) > 0 || (stats?.unpriced_span_count ?? 0) > 0) && (
								<MetricTrailing className={muted} testId="tracing-stat-cost-notes">
									{(stats?.unpriced_span_count ?? 0) > 0
										? t("tracing.unpricedCalls", { count: stats?.unpriced_span_count ?? 0 })
										: t("tracing.estimatedShare", { cost: formatCost(stats?.estimated_cost) })}
								</MetricTrailing>
							)}
						</>
					}
				>
					{formatCurrencyNumber(stats?.total_cost ?? 0)}
				</MetricSegment>
			</MetricStripCard>
		</div>
	);
}