import { LogCostCell, LogLatencyCell, LogTimeCell } from "@/app/workspace/logs/views/columns";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { TruncatedLabel } from "@/components/ui/truncatedLabel";
import { StatusBarColors } from "@/lib/constants/logs";
import type { Trace, TraceExperiment, TraceProject, TraceSession, TraceSpan } from "@/lib/types/traces";
import { cn } from "@/lib/utils";
import { formatCompactNumber } from "@/lib/utils/numbers";
import type { ColumnDef } from "@tanstack/react-table";
import type { TFunction } from "i18next";
import { ArrowUpDown, Loader2 } from "lucide-react";
import { spanUsage } from "../traceTree";
import { formatCost, formatDuration, previewValue, spanKindMeta } from "./format";

type T = TFunction<"observability">;

// The LLM and relay tables' sort header, so a sortable column looks the same on every log page.
const sortableHeader =
	(label: string, testId: string) =>
	({ column }: { column: { toggleSorting: (desc?: boolean) => void; getIsSorted: () => false | "asc" | "desc" } }) => (
		<Button variant="ghost" data-testid={testId} onClick={() => column.toggleSorting(column.getIsSorted() === "asc")}>
			{label}
			<ArrowUpDown className="ml-2 h-4 w-4" />
		</Button>
	);

function StatusBar({ status, title }: { status: keyof typeof StatusBarColors; title: string }) {
	return <div title={title} className={cn("h-full min-h-[24px] w-1 rounded-sm", StatusBarColors[status])} />;
}

const traceStatus = (trace: Trace): keyof typeof StatusBarColors =>
	trace.error_count > 0 ? "error" : trace.in_progress ? "processing" : "success";
const spanStatus = (span: TraceSpan): keyof typeof StatusBarColors =>
	span.status === "error" ? "error" : span.partial && !span.end_time ? "processing" : "success";

function Preview({ value }: { value?: string }) {
	const text = previewValue(value, 200);
	return text ? (
		<TruncatedLabel className="text-muted-foreground font-mono text-xs">{text}</TruncatedLabel>
	) : (
		<span className="text-muted-foreground text-xs">-</span>
	);
}

function Tokens({ value }: { value?: number }) {
	return <div className="pl-4 font-mono text-sm tabular-nums">{value ? formatCompactNumber(value) : "-"}</div>;
}

export const getTraceColumnLabels = (t: T): Record<string, string> => ({
	timestamp: t("tracing.columns.timestamp"),
	name: t("tracing.columns.name"),
	input: t("tracing.columns.input"),
	output: t("tracing.columns.output"),
	scores: t("tracing.columns.scores"),
	tags: t("tracing.columns.tags"),
	tokens: t("tracing.columns.tokens"),
	cost: t("tracing.columns.cost"),
	latency: t("tracing.columns.duration"),
	spans: t("tracing.columns.spans"),
	project: t("tracing.filters.projects"),
	session: t("filters.sessionID"),
	kind: t("tracing.filters.spanKind"),
	model: t("tracing.columns.model"),
});

/** Columns of the trace list. Time, latency and cost render through the LLM logs table's own cells. */
export function createTraceColumns(t: T, projects: TraceProject[]): ColumnDef<Trace>[] {
	const projectName = (id: string) => projects.find((project) => project.id === id)?.name ?? id;
	return [
		{
			id: "status",
			header: "",
			size: 8,
			maxSize: 8,
			cell: ({ row }) => <StatusBar status={traceStatus(row.original)} title={traceStatus(row.original)} />,
		},
		{
			id: "timestamp",
			header: sortableHeader(t("tracing.columns.timestamp"), "traces-time-sort-btn"),
			size: 150,
			cell: ({ row }) => <LogTimeCell timestamp={row.original.start_time} testId="traces-row-open-btn" />,
		},
		{
			id: "name",
			header: sortableHeader(t("tracing.columns.name"), "traces-name-sort-btn"),
			size: 220,
			cell: ({ row }) => (
				<div className="flex min-w-0 items-center gap-1.5">
					{row.original.in_progress && (
						<Loader2 className="text-muted-foreground size-3.5 shrink-0 animate-spin" aria-label={t("tracing.inProgress")} />
					)}
					<TruncatedLabel className="text-sm font-medium">{row.original.name || row.original.trace_id}</TruncatedLabel>
				</div>
			),
		},
		{ id: "input", header: t("tracing.columns.input"), size: 260, cell: ({ row }) => <Preview value={row.original.input} /> },
		{ id: "output", header: t("tracing.columns.output"), size: 260, cell: ({ row }) => <Preview value={row.original.output} /> },
		{
			id: "scores",
			header: t("tracing.columns.scores"),
			size: 180,
			cell: ({ row }) => (
				<div className="flex min-w-0 gap-1 overflow-hidden">
					{Object.entries(row.original.scores ?? {}).map(([name, value]) => (
						<Badge key={name} variant="outline" className="shrink-0 gap-1 font-mono text-[11px] tabular-nums">
							{name} {value.toFixed(2)}
						</Badge>
					))}
				</div>
			),
		},
		{
			id: "tags",
			header: t("tracing.columns.tags"),
			size: 160,
			cell: ({ row }) => (
				<div className="flex min-w-0 gap-1 overflow-hidden">
					{(row.original.tags ?? []).map((tag) => (
						<Badge key={tag} variant="secondary" className="shrink-0 text-[11px]">
							{tag}
						</Badge>
					))}
				</div>
			),
		},
		{
			id: "spans",
			header: t("tracing.columns.spans"),
			size: 80,
			cell: ({ row }) => <div className="pl-4 font-mono text-sm tabular-nums">{row.original.span_count}</div>,
		},
		{
			id: "tokens",
			header: sortableHeader(t("tracing.columns.tokens"), "traces-tokens-sort-btn"),
			size: 110,
			cell: ({ row }) => <Tokens value={row.original.total_tokens} />,
		},
		{
			id: "cost",
			header: sortableHeader(t("tracing.columns.cost"), "traces-cost-sort-btn"),
			size: 120,
			cell: ({ row }) => <LogCostCell cost={row.original.cost} format={formatCost} />,
		},
		{
			id: "latency",
			header: sortableHeader(t("tracing.columns.duration"), "traces-latency-sort-btn"),
			size: 150,
			cell: ({ row }) => <LogLatencyCell latency={row.original.duration_ms} />,
		},
		{
			id: "project",
			header: t("tracing.filters.projects"),
			size: 140,
			cell: ({ row }) => <TruncatedLabel className="text-xs">{projectName(row.original.project_id)}</TruncatedLabel>,
		},
		{
			id: "session",
			header: t("filters.sessionID"),
			size: 160,
			cell: ({ row }) => <TruncatedLabel className="font-mono text-xs">{row.original.session_id || "-"}</TruncatedLabel>,
		},
	];
}

/** Columns of the span list. */
export function createSpanColumns(t: T): ColumnDef<TraceSpan>[] {
	return [
		{
			id: "status",
			header: "",
			size: 8,
			maxSize: 8,
			cell: ({ row }) => <StatusBar status={spanStatus(row.original)} title={spanStatus(row.original)} />,
		},
		{
			id: "timestamp",
			header: sortableHeader(t("tracing.columns.timestamp"), "trace-spans-time-sort-btn"),
			size: 150,
			cell: ({ row }) => <LogTimeCell timestamp={row.original.start_time} testId="trace-spans-row-open-btn" />,
		},
		{
			id: "name",
			header: sortableHeader(t("tracing.columns.name"), "trace-spans-name-sort-btn"),
			size: 240,
			cell: ({ row }) => {
				const meta = spanKindMeta(row.original.kind);
				const Icon = meta.icon;
				return (
					<div className="flex min-w-0 items-center gap-2">
						<span className={cn("flex size-6 shrink-0 items-center justify-center rounded-sm", meta.tile)}>
							<Icon className="size-3.5" />
						</span>
						<TruncatedLabel className="text-sm font-medium">{row.original.name || row.original.span_id}</TruncatedLabel>
					</div>
				);
			},
		},
		{
			id: "kind",
			header: t("tracing.filters.spanKind"),
			size: 100,
			cell: ({ row }) => <span className="text-xs capitalize">{row.original.kind}</span>,
		},
		{
			id: "model",
			header: t("tracing.columns.model"),
			size: 160,
			cell: ({ row }) => (
				<TruncatedLabel className="font-mono text-xs">{row.original.link_snapshot?.model || row.original.model || "-"}</TruncatedLabel>
			),
		},
		{ id: "input", header: t("tracing.columns.input"), size: 240, cell: ({ row }) => <Preview value={row.original.input} /> },
		{ id: "output", header: t("tracing.columns.output"), size: 240, cell: ({ row }) => <Preview value={row.original.output} /> },
		{
			id: "tokens",
			header: sortableHeader(t("tracing.columns.tokens"), "trace-spans-tokens-sort-btn"),
			size: 110,
			cell: ({ row }) => <Tokens value={spanUsage(row.original).tokens} />,
		},
		{
			id: "cost",
			header: sortableHeader(t("tracing.columns.cost"), "trace-spans-cost-sort-btn"),
			size: 120,
			cell: ({ row }) => <LogCostCell cost={spanUsage(row.original).cost} format={formatCost} />,
		},
		{
			id: "latency",
			header: sortableHeader(t("tracing.columns.duration"), "trace-spans-latency-sort-btn"),
			size: 150,
			cell: ({ row }) => <LogLatencyCell latency={row.original.duration_ms} />,
		},
	];
}

/** Columns of the trace session list. */
export function createSessionColumns(t: T): ColumnDef<TraceSession>[] {
	return [
		{
			id: "status",
			header: "",
			size: 8,
			maxSize: 8,
			cell: ({ row }) => <StatusBar status={row.original.error_count > 0 ? "error" : "success"} title="" />,
		},
		{
			id: "timestamp",
			header: t("tracing.sessions.lastSeen"),
			size: 150,
			cell: ({ row }) => <LogTimeCell timestamp={row.original.last_seen} testId="trace-sessions-row-open-btn" />,
		},
		{
			id: "session",
			header: t("tracing.sessions.session"),
			size: 300,
			cell: ({ row }) => <TruncatedLabel className="font-mono text-xs">{row.original.session_id}</TruncatedLabel>,
		},
		{
			id: "user",
			header: t("tracing.sessions.user"),
			size: 160,
			cell: ({ row }) => <TruncatedLabel className="text-xs">{row.original.user_id || "-"}</TruncatedLabel>,
		},
		{
			id: "traces",
			header: t("tracing.overview.traces"),
			size: 90,
			cell: ({ row }) => <div className="pl-4 font-mono text-sm tabular-nums">{row.original.trace_count}</div>,
		},
		{ id: "tokens", header: t("tracing.columns.tokens"), size: 110, cell: ({ row }) => <Tokens value={row.original.total_tokens} /> },
		{
			id: "cost",
			header: t("tracing.columns.cost"),
			size: 120,
			cell: ({ row }) => <LogCostCell cost={row.original.total_cost} format={formatCost} />,
		},
	];
}

/** Columns of the experiment list. */
export function createExperimentColumns(t: T): ColumnDef<TraceExperiment>[] {
	return [
		{
			id: "status",
			header: "",
			size: 8,
			maxSize: 8,
			cell: ({ row }) => <StatusBar status={row.original.summary.error_count > 0 ? "error" : "success"} title="" />,
		},
		{
			id: "timestamp",
			header: t("tracing.columns.timestamp"),
			size: 150,
			cell: ({ row }) => <LogTimeCell timestamp={row.original.created_at} testId="trace-experiments-row-open-btn" />,
		},
		{
			id: "name",
			header: t("tracing.columns.name"),
			size: 240,
			cell: ({ row }) => (
				<div className="flex min-w-0 flex-col">
					<TruncatedLabel className="text-sm font-medium">{row.original.name}</TruncatedLabel>
					{row.original.dataset_name && (
						<TruncatedLabel className="text-muted-foreground text-[11px]">{row.original.dataset_name}</TruncatedLabel>
					)}
				</div>
			),
		},
		{
			id: "scores",
			header: t("tracing.columns.scores"),
			size: 240,
			cell: ({ row }) => (
				<div className="flex min-w-0 gap-1 overflow-hidden">
					{Object.entries(row.original.summary.scores ?? {}).map(([name, value]) => (
						<Badge key={name} variant="outline" className="shrink-0 gap-1 font-mono text-[11px] tabular-nums">
							{name} {value.toFixed(2)}
						</Badge>
					))}
				</div>
			),
		},
		{
			id: "traces",
			header: t("tracing.overview.traces"),
			size: 90,
			cell: ({ row }) => (
				<div className="pl-4 font-mono text-sm tabular-nums">
					{row.original.summary.trace_count}
					{row.original.summary.truncated ? "+" : ""}
				</div>
			),
		},
		{
			id: "tokens",
			header: t("tracing.columns.tokens"),
			size: 110,
			cell: ({ row }) => <Tokens value={row.original.summary.total_tokens} />,
		},
		{
			id: "cost",
			header: t("tracing.columns.cost"),
			size: 120,
			cell: ({ row }) => <LogCostCell cost={row.original.summary.total_cost} format={formatCost} />,
		},
		{
			id: "latency",
			header: t("tracing.overview.avgDuration"),
			size: 150,
			cell: ({ row }) => <span className="pl-4 font-mono text-xs">{formatDuration(row.original.summary.avg_duration_ms)}</span>,
		},
	];
}