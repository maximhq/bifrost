import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import type { Trace, TraceExperiment, TracePagination, TraceSession, TraceSpan } from "@/lib/types/traces";
import { cn } from "@/lib/utils";
import { AlertCircle, ArrowDown, ArrowUp, Loader2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import { spanUsage } from "../traceTree";
import { formatCost, formatDuration, formatTokens, previewValue, SOURCE_LABELS, spanKindMeta } from "./format";

interface SortProps {
	pagination: TracePagination;
	onSort: (sortBy: string) => void;
}

function SortHead({ id, label, pagination, onSort, className }: SortProps & { id: string; label: string; className?: string }) {
	const active = (pagination.sort_by || "timestamp") === id;
	return (
		<TableHead className={className}>
			<button
				type="button"
				data-testid={`traces-sort-${id}-button`}
				onClick={() => onSort(id)}
				className="hover:text-foreground inline-flex items-center gap-1"
			>
				{label}
				{active && (pagination.order === "asc" ? <ArrowUp className="h-3 w-3" /> : <ArrowDown className="h-3 w-3" />)}
			</button>
		</TableHead>
	);
}

export function Pager({ pagination, onPage }: { pagination: TracePagination; onPage: (offset: number) => void }) {
	const { t } = useTranslation("observability");
	const total = pagination.total_count ?? 0;
	const from = total === 0 ? 0 : pagination.offset + 1;
	const to = Math.min(pagination.offset + pagination.limit, total);
	return (
		<div className="text-muted-foreground flex items-center justify-between px-1 py-2 text-xs">
			<span>{t("tracing.table.range", { from, to, total })}</span>
			<div className="flex gap-2">
				<Button
					variant="outline"
					size="sm"
					data-testid="traces-prev-page-button"
					disabled={pagination.offset === 0}
					onClick={() => onPage(Math.max(pagination.offset - pagination.limit, 0))}
				>
					{t("tracing.table.previous")}
				</Button>
				<Button
					variant="outline"
					size="sm"
					data-testid="traces-next-page-button"
					disabled={to >= total}
					onClick={() => onPage(pagination.offset + pagination.limit)}
				>
					{t("tracing.table.next")}
				</Button>
			</div>
		</div>
	);
}

export function TracesTable({ traces, onOpen, ...sort }: SortProps & { traces: Trace[]; onOpen: (trace: Trace) => void }) {
	const { t } = useTranslation("observability");
	return (
		<Table data-testid="traces-table">
			<TableHeader>
				<TableRow>
					<SortHead id="timestamp" label={t("tracing.columns.timestamp")} {...sort} />
					<SortHead id="name" label={t("tracing.columns.name")} {...sort} />
					<TableHead>{t("tracing.columns.input")}</TableHead>
					<TableHead>{t("tracing.columns.output")}</TableHead>
					<TableHead>{t("tracing.columns.tags")}</TableHead>
					<TableHead>{t("tracing.columns.scores")}</TableHead>
					<SortHead id="tokens" label={t("tracing.columns.tokens")} className="text-right" {...sort} />
					<SortHead id="cost" label={t("tracing.columns.cost")} className="text-right" {...sort} />
					<SortHead id="duration" label={t("tracing.columns.duration")} className="text-right" {...sort} />
					<TableHead>{t("tracing.columns.source")}</TableHead>
				</TableRow>
			</TableHeader>
			<TableBody>
				{traces.map((trace) => (
					<TableRow
						key={`${trace.project_id}/${trace.trace_id}`}
						data-testid={`traces-row-${trace.trace_id}`}
						className="cursor-pointer"
						onClick={() => onOpen(trace)}
					>
						<TableCell className="text-muted-foreground text-xs whitespace-nowrap">{new Date(trace.start_time).toLocaleString()}</TableCell>
						<TableCell className="max-w-[220px]">
							<div className="flex items-center gap-1.5">
								{trace.error_count > 0 && <AlertCircle className="text-destructive h-3.5 w-3.5 shrink-0" />}
								{trace.in_progress && (
									<Loader2 className="text-muted-foreground h-3.5 w-3.5 shrink-0 animate-spin" aria-label={t("tracing.inProgress")} />
								)}
								<span className="truncate font-medium">{trace.name || trace.trace_id}</span>
							</div>
						</TableCell>
						<TableCell className="text-muted-foreground max-w-[240px] truncate text-xs">{previewValue(trace.input)}</TableCell>
						<TableCell className="text-muted-foreground max-w-[240px] truncate text-xs">{previewValue(trace.output)}</TableCell>
						<TableCell>
							<div className="flex flex-wrap gap-1">
								{(trace.tags ?? []).slice(0, 3).map((tag) => (
									<Badge key={tag} variant="secondary">
										{tag}
									</Badge>
								))}
							</div>
						</TableCell>
						<TableCell>
							<div className="flex flex-wrap gap-1">
								{Object.entries(trace.scores ?? {})
									.slice(0, 3)
									.map(([name, value]) => (
										<Badge key={name} variant="outline" className="gap-1 tabular-nums">
											{name} {value.toFixed(2)}
										</Badge>
									))}
							</div>
						</TableCell>
						<TableCell className="text-right tabular-nums">{formatTokens(trace.total_tokens)}</TableCell>
						<TableCell className="text-right tabular-nums">
							{trace.estimated_cost ? "~" : ""}
							{formatCost(trace.cost)}
						</TableCell>
						<TableCell className="text-right tabular-nums">{formatDuration(trace.duration_ms)}</TableCell>
						<TableCell className="text-xs">{SOURCE_LABELS[trace.source] ?? trace.source}</TableCell>
					</TableRow>
				))}
			</TableBody>
		</Table>
	);
}

export function SpansTable({ spans, onOpen, ...sort }: SortProps & { spans: TraceSpan[]; onOpen: (span: TraceSpan) => void }) {
	const { t } = useTranslation("observability");
	return (
		<Table data-testid="trace-spans-table">
			<TableHeader>
				<TableRow>
					<SortHead id="timestamp" label={t("tracing.columns.timestamp")} {...sort} />
					<SortHead id="name" label={t("tracing.columns.name")} {...sort} />
					<TableHead>{t("tracing.columns.model")}</TableHead>
					<TableHead>{t("tracing.columns.input")}</TableHead>
					<TableHead>{t("tracing.columns.output")}</TableHead>
					<SortHead id="tokens" label={t("tracing.columns.tokens")} className="text-right" {...sort} />
					<SortHead id="cost" label={t("tracing.columns.cost")} className="text-right" {...sort} />
					<SortHead id="duration" label={t("tracing.columns.duration")} className="text-right" {...sort} />
				</TableRow>
			</TableHeader>
			<TableBody>
				{spans.map((span) => {
					const meta = spanKindMeta(span.kind);
					const Icon = meta.icon;
					const usage = spanUsage(span);
					return (
						<TableRow
							key={`${span.project_id}/${span.trace_id}/${span.span_id}`}
							data-testid={`trace-spans-row-${span.span_id}`}
							className="cursor-pointer"
							onClick={() => onOpen(span)}
						>
							<TableCell className="text-muted-foreground text-xs whitespace-nowrap">
								{new Date(span.start_time).toLocaleString()}
							</TableCell>
							<TableCell className="max-w-[240px]">
								<div className="flex items-center gap-1.5">
									<Icon className={cn("h-3.5 w-3.5 shrink-0", meta.text)} />
									<span className="truncate font-medium">{span.name || span.span_id}</span>
									{span.status === "error" && <AlertCircle className="text-destructive h-3.5 w-3.5 shrink-0" />}
								</div>
							</TableCell>
							<TableCell className="text-xs">{span.link_snapshot?.model || span.model || "-"}</TableCell>
							<TableCell className="text-muted-foreground max-w-[240px] truncate text-xs">{previewValue(span.input)}</TableCell>
							<TableCell className="text-muted-foreground max-w-[240px] truncate text-xs">{previewValue(span.output)}</TableCell>
							<TableCell className="text-right tabular-nums">{formatTokens(usage.tokens)}</TableCell>
							<TableCell className="text-right tabular-nums">
								{usage.estimated ? "~" : ""}
								{formatCost(usage.cost)}
							</TableCell>
							<TableCell className="text-right tabular-nums">{formatDuration(span.duration_ms)}</TableCell>
						</TableRow>
					);
				})}
			</TableBody>
		</Table>
	);
}

/** Trace sessions: the traces that share a session id, as one conversation. */
export function SessionsTable({ sessions, onOpen }: { sessions: TraceSession[]; onOpen: (session: TraceSession) => void }) {
	const { t } = useTranslation("observability");
	return (
		<Table data-testid="trace-sessions-table">
			<TableHeader>
				<TableRow>
					<TableHead>{t("tracing.sessions.session")}</TableHead>
					<TableHead>{t("tracing.sessions.user")}</TableHead>
					<TableHead className="text-right">{t("tracing.overview.traces")}</TableHead>
					<TableHead className="text-right">{t("tracing.columns.tokens")}</TableHead>
					<TableHead className="text-right">{t("tracing.columns.cost")}</TableHead>
					<TableHead>{t("tracing.sessions.lastSeen")}</TableHead>
				</TableRow>
			</TableHeader>
			<TableBody>
				{sessions.map((session) => (
					<TableRow
						key={`${session.project_id}/${session.session_id}`}
						data-testid={`trace-sessions-row-${session.session_id}`}
						className="cursor-pointer"
						onClick={() => onOpen(session)}
					>
						<TableCell className="max-w-[320px]">
							<div className="flex items-center gap-1.5">
								{session.error_count > 0 && <AlertCircle className="text-destructive h-3.5 w-3.5 shrink-0" />}
								<span className="truncate font-mono text-xs">{session.session_id}</span>
							</div>
						</TableCell>
						<TableCell className="text-xs">{session.user_id || "-"}</TableCell>
						<TableCell className="text-right tabular-nums">{session.trace_count}</TableCell>
						<TableCell className="text-right tabular-nums">{formatTokens(session.total_tokens)}</TableCell>
						<TableCell className="text-right tabular-nums">{formatCost(session.total_cost)}</TableCell>
						<TableCell className="text-muted-foreground text-xs whitespace-nowrap">
							{new Date(session.last_seen).toLocaleString()}
						</TableCell>
					</TableRow>
				))}
			</TableBody>
		</Table>
	);
}
/** Experiments registered by SDKs: Braintrust experiments and Langfuse dataset runs, each with the mean of its scores. */
export function ExperimentsTable({
	experiments,
	onOpen,
}: {
	experiments: TraceExperiment[];
	onOpen: (experiment: TraceExperiment) => void;
}) {
	const { t } = useTranslation("observability");
	return (
		<Table data-testid="trace-experiments-table">
			<TableHeader>
				<TableRow>
					<TableHead>{t("tracing.columns.name")}</TableHead>
					<TableHead>{t("tracing.columns.source")}</TableHead>
					<TableHead>{t("tracing.columns.scores")}</TableHead>
					<TableHead className="text-right">{t("tracing.overview.traces")}</TableHead>
					<TableHead className="text-right">{t("tracing.columns.tokens")}</TableHead>
					<TableHead className="text-right">{t("tracing.columns.cost")}</TableHead>
					<TableHead className="text-right">{t("tracing.overview.avgDuration")}</TableHead>
					<TableHead>{t("tracing.columns.timestamp")}</TableHead>
				</TableRow>
			</TableHeader>
			<TableBody>
				{experiments.map((experiment) => (
					<TableRow
						key={`${experiment.project_id}/${experiment.id}`}
						data-testid={`trace-experiments-row-${experiment.id}`}
						className="cursor-pointer"
						onClick={() => onOpen(experiment)}
					>
						<TableCell className="max-w-[260px]">
							<div className="flex items-center gap-1.5">
								{experiment.summary.error_count > 0 && <AlertCircle className="text-destructive h-3.5 w-3.5 shrink-0" />}
								<span className="truncate font-medium">{experiment.name}</span>
							</div>
							{experiment.dataset_name && <div className="text-muted-foreground truncate text-xs">{experiment.dataset_name}</div>}
						</TableCell>
						<TableCell className="text-xs">{SOURCE_LABELS[experiment.source] ?? experiment.source}</TableCell>
						<TableCell>
							<div className="flex flex-wrap gap-1">
								{Object.entries(experiment.summary.scores ?? {}).map(([name, value]) => (
									<Badge key={name} variant="outline" className="gap-1 tabular-nums">
										{name} {value.toFixed(2)}
									</Badge>
								))}
							</div>
						</TableCell>
						<TableCell className="text-right tabular-nums">
							{experiment.summary.trace_count}
							{experiment.summary.truncated ? "+" : ""}
						</TableCell>
						<TableCell className="text-right tabular-nums">{formatTokens(experiment.summary.total_tokens)}</TableCell>
						<TableCell className="text-right tabular-nums">{formatCost(experiment.summary.total_cost)}</TableCell>
						<TableCell className="text-right tabular-nums">{formatDuration(experiment.summary.avg_duration_ms)}</TableCell>
						<TableCell className="text-muted-foreground text-xs whitespace-nowrap">
							{new Date(experiment.created_at).toLocaleString()}
						</TableCell>
					</TableRow>
				))}
			</TableBody>
		</Table>
	);
}