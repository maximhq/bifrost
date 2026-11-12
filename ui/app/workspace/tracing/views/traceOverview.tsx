import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import type { TraceBreakdownRow, TraceHistogramResponse, TraceProject, TraceStatsResponse } from "@/lib/types/traces";
import { useTranslation } from "react-i18next";
import { formatCost, formatDuration, formatTokens, SOURCE_LABELS } from "./format";

function Tile({ label, value, hint, testId }: { label: string; value: React.ReactNode; hint?: React.ReactNode; testId: string }) {
	return (
		<div className="rounded-md border p-3" data-testid={testId}>
			<div className="text-muted-foreground text-xs">{label}</div>
			<div className="mt-1 text-xl font-semibold tabular-nums">{value}</div>
			{hint && <div className="text-muted-foreground mt-0.5 text-[11px]">{hint}</div>}
		</div>
	);
}

/** Trace volume over time. Each bar is one bucket; the red part is the traces with an error. */
export function TraceHistogram({ histogram }: { histogram?: TraceHistogramResponse }) {
	const { t } = useTranslation("observability");
	const buckets = histogram?.buckets ?? [];
	const max = Math.max(1, ...buckets.map((bucket) => bucket.count));
	if (buckets.length === 0) return null;
	return (
		<div
			className="flex h-20 items-end gap-px rounded-md border px-2 pt-2"
			data-testid="trace-histogram"
			role="img"
			aria-label={t("tracing.overview.volume")}
		>
			{buckets.map((bucket) => {
				const height = (bucket.count / max) * 100;
				const errorShare = bucket.count > 0 ? bucket.errors / bucket.count : 0;
				return (
					<Tooltip key={bucket.timestamp}>
						<TooltipTrigger asChild>
							<div className="flex h-full min-w-0 flex-1 items-end">
								{/* Radius on the open end only, a quarter of the bar's thickness at most. */}
								<div
									className="bg-chart-info w-full overflow-hidden rounded-t-[2px]"
									style={{ height: `${height}%`, minHeight: bucket.count > 0 ? 2 : 0 }}
								>
									<div className="bg-chart-error w-full" style={{ height: `${errorShare * 100}%` }} />
								</div>
							</div>
						</TooltipTrigger>
						<TooltipContent>
							<div className="text-xs">
								<div>{new Date(bucket.timestamp).toLocaleString()}</div>
								<div>{t("tracing.overview.bucket", { count: bucket.count, errors: bucket.errors })}</div>
							</div>
						</TooltipContent>
					</Tooltip>
				);
			})}
		</div>
	);
}

function Breakdown({
	title,
	rows,
	label,
	testId,
}: {
	title: string;
	rows: TraceBreakdownRow[];
	label: (key: string) => string;
	testId: string;
}) {
	const { t } = useTranslation("observability");
	if (rows.length === 0) return null;
	return (
		<div className="rounded-md border" data-testid={testId}>
			<div className="border-b px-3 py-2 text-sm font-medium">{title}</div>
			<table className="w-full text-sm">
				<thead className="text-muted-foreground text-xs">
					<tr>
						<th className="px-3 py-1.5 text-left font-normal">{t("tracing.columns.name")}</th>
						<th className="px-3 py-1.5 text-right font-normal">{t("tracing.overview.traces")}</th>
						<th className="px-3 py-1.5 text-right font-normal">{t("tracing.columns.tokens")}</th>
						<th className="px-3 py-1.5 text-right font-normal">{t("tracing.columns.cost")}</th>
						<th className="px-3 py-1.5 text-right font-normal">{t("tracing.overview.linkRate")}</th>
					</tr>
				</thead>
				<tbody>
					{rows.map((row) => (
						<tr key={row.key} className="border-t">
							<td className="px-3 py-1.5">{label(row.key)}</td>
							<td className="px-3 py-1.5 text-right tabular-nums">{row.trace_count}</td>
							<td className="px-3 py-1.5 text-right tabular-nums">{formatTokens(row.total_tokens)}</td>
							<td className="px-3 py-1.5 text-right tabular-nums">{formatCost(row.total_cost)}</td>
							<td className="px-3 py-1.5 text-right tabular-nums">
								{row.llm_span_count > 0 ? `${Math.round((row.linked_span_count / row.llm_span_count) * 100)}%` : "-"}
							</td>
						</tr>
					))}
				</tbody>
			</table>
		</div>
	);
}

/**
 * The Tracing overview. Its figures count each model call once: a call served by this gateway with
 * the gateway's numbers, any other with the SDK's. The link rate says how many calls were matched.
 */
export function TraceOverview({
	data,
	histogram,
	projects,
}: {
	data?: TraceStatsResponse;
	histogram?: TraceHistogramResponse;
	projects: TraceProject[];
}) {
	const { t } = useTranslation("observability");
	const stats = data?.stats;
	if (!stats) return null;
	const linkRate = stats.llm_span_count > 0 ? Math.round((stats.linked_span_count / stats.llm_span_count) * 100) : null;
	const projectName = (id: string) => projects.find((project) => project.id === id)?.name ?? id;
	return (
		<div className="space-y-4" data-testid="trace-overview">
			<div className="grid grid-cols-2 gap-3 lg:grid-cols-6">
				<Tile
					testId="trace-overview-traces"
					label={t("tracing.overview.traces")}
					value={stats.trace_count.toLocaleString()}
					hint={stats.in_progress_count > 0 ? t("tracing.overview.inProgress", { count: stats.in_progress_count }) : undefined}
				/>
				<Tile testId="trace-overview-tokens" label={t("tracing.columns.tokens")} value={formatTokens(stats.total_tokens)} />
				<Tile
					testId="trace-overview-cost"
					label={t("tracing.columns.cost")}
					value={formatCost(stats.total_cost)}
					hint={
						<>
							{stats.estimated_cost > 0 && <span>{t("tracing.estimatedShare", { cost: formatCost(stats.estimated_cost) })}</span>}
							{stats.estimated_cost > 0 && stats.unpriced_span_count > 0 && " · "}
							{stats.unpriced_span_count > 0 && <span>{t("tracing.unpricedCalls", { count: stats.unpriced_span_count })}</span>}
						</>
					}
				/>
				<Tile testId="trace-overview-latency" label={t("tracing.overview.avgDuration")} value={formatDuration(stats.avg_duration_ms)} />
				<Tile
					testId="trace-overview-errors"
					label={t("tracing.overview.errors")}
					value={stats.error_trace_count.toLocaleString()}
					hint={stats.trace_count > 0 ? `${((stats.error_trace_count / stats.trace_count) * 100).toFixed(1)}%` : undefined}
				/>
				<Tile
					testId="trace-overview-link-rate"
					label={t("tracing.overview.linkRate")}
					value={linkRate === null ? "-" : `${linkRate}%`}
					hint={t("tracing.overview.linkRateHint", { linked: stats.linked_span_count, total: stats.llm_span_count })}
				/>
			</div>
			<TraceHistogram histogram={histogram} />
			<div className="grid gap-4 lg:grid-cols-2">
				<Breakdown
					testId="trace-overview-by-project"
					title={t("tracing.overview.byProject")}
					rows={data?.by_project ?? []}
					label={projectName}
				/>
				<Breakdown
					testId="trace-overview-by-source"
					title={t("tracing.overview.bySource")}
					rows={data?.by_source ?? []}
					label={(key) => SOURCE_LABELS[key as keyof typeof SOURCE_LABELS] ?? key}
				/>
			</div>
		</div>
	);
}