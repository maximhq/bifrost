import { LogsVolumeChart } from "@/app/workspace/logs/views/logsVolumeChart";
import { TracingFilterSidebar, type TracingSidebarFilters } from "@/components/filters/tracingFilterSidebar";
import FullPageLoader from "@/components/fullPageLoader";
import { ColumnConfigDropdown, useColumnConfig } from "@/components/table";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { parseAsSafeArrayOf, parseAsSafeString } from "@/lib/queryParamsParser";
import {
	useGetTraceExperimentsQuery,
	useGetTraceHistogramQuery,
	useGetTraceProjectsQuery,
	useGetTraceSessionsQuery,
	useGetTraceSpansQuery,
	useGetTraceStatsQuery,
	useGetTracesQuery,
} from "@/lib/store";
import type { TraceFilters, TracePagination } from "@/lib/types/traces";
import { RefreshCw, Search, Settings } from "lucide-react";
import { parseAsInteger, useQueryStates } from "nuqs";
import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { TracingEmptyState } from "./views/emptyState";
import { ProjectSettings } from "./views/projectSettings";
import { renderTraceSpanExtras } from "./views/traceExtras";
import { toVolumeHistogram, TraceMetricStrip } from "./views/traceOverview";
import { LogsDataTable, type LogsTablePagination } from "./views/logsDataTable";
import {
	createExperimentColumns,
	createSessionColumns,
	createSpanColumns,
	createTraceColumns,
	getTraceColumnLabels,
} from "./views/tracingColumns";
import { TraceViewer } from "./views/traceViewer";

const RANGE_KEYS = ["1h", "24h", "7d", "30d"] as const;
const RANGES: Record<string, number> = { "1h": 3600, "24h": 86400, "7d": 604800, "30d": 2592000 };
const arr = parseAsSafeArrayOf.withDefault([]);

/** Buckets that give roughly sixty bars across the chosen range. */
function bucketFor(rangeSeconds: number): number {
	return Math.max(60, Math.round(rangeSeconds / 60 / 60) * 60);
}

/**
 * Tracing: every trace pushed to Bifrost, whatever SDK sent it, in one list. It is laid out as the
 * log pages are: filters on the left, the summary strip and volume chart above the table. A model
 * call served by this gateway is linked to its gateway log and counted once.
 */
export default function TracingPage() {
	const { t } = useTranslation("observability");
	const [url, setUrl] = useQueryStates({
		view: parseAsSafeString.withDefault("traces"),
		project_ids: arr,
		status: parseAsSafeString.withDefault(""),
		models: arr,
		kinds: arr,
		environments: arr,
		session_id: parseAsSafeString.withDefault(""),
		trace_id: parseAsSafeString.withDefault(""),
		experiment: parseAsSafeString.withDefault(""),
		search: parseAsSafeString.withDefault(""),
		range: parseAsSafeString.withDefault("24h"),
		// A zoomed window from the volume chart, in unix seconds. Unset means the range above.
		start_time: parseAsInteger,
		end_time: parseAsInteger,
		sort_by: parseAsSafeString.withDefault("timestamp"),
		order: parseAsSafeString.withDefault("desc"),
		offset: parseAsInteger.withDefault(0),
		limit: parseAsInteger.withDefault(50),
		// A link printed by an SDK opens one trace: its project (id or name), trace and span.
		project: parseAsSafeString.withDefault(""),
		trace: parseAsSafeString.withDefault(""),
		span: parseAsSafeString.withDefault(""),
	});
	const [settingsOpen, setSettingsOpen] = useState(false);
	const [chartOpen, setChartOpen] = useState(true);
	const [searchDraft, setSearchDraft] = useState(url.search);
	const { data: projectsData, isLoading: projectsLoading } = useGetTraceProjectsQuery();
	const projects = useMemo(() => projectsData?.projects ?? [], [projectsData]);

	// The window is fixed when a filter changes, not on every render, so the query key is stable.
	const [windowEnd, setWindowEnd] = useState(() => Date.now());
	useEffect(
		() => setWindowEnd(Date.now()),
		[url.range, url.view, url.search, url.status, url.session_id, url.trace_id, url.project_ids, url.models, url.kinds, url.environments],
	);
	const rangeSeconds = RANGES[url.range] ?? RANGES["24h"];
	const zoomed = url.start_time !== null && url.end_time !== null;
	const startSeconds = zoomed ? (url.start_time as number) : Math.floor((windowEnd - rangeSeconds * 1000) / 1000);
	const endSeconds = zoomed ? (url.end_time as number) : Math.floor((windowEnd + 60_000) / 1000);

	const filters: TraceFilters = useMemo(
		() => ({
			project_ids: url.project_ids.length > 0 ? url.project_ids : undefined,
			models: url.models.length > 0 ? url.models : undefined,
			kinds: url.kinds.length > 0 ? url.kinds : undefined,
			environments: url.environments.length > 0 ? url.environments : undefined,
			status: url.status || undefined,
			session_id: url.session_id || undefined,
			trace_id: url.trace_id || undefined,
			experiment_id: url.experiment || undefined,
			search: url.search || undefined,
			start_time: new Date(startSeconds * 1000).toISOString(),
			end_time: new Date(endSeconds * 1000).toISOString(),
		}),
		[
			url.project_ids,
			url.models,
			url.kinds,
			url.environments,
			url.status,
			url.session_id,
			url.trace_id,
			url.experiment,
			url.search,
			startSeconds,
			endSeconds,
		],
	);
	const pagination: TracePagination & LogsTablePagination = {
		limit: url.limit,
		offset: url.offset,
		sort_by: url.sort_by,
		order: url.order === "asc" ? "asc" : "desc",
	};
	const onPaginationChange = (next: LogsTablePagination) =>
		setUrl({ limit: next.limit, offset: next.offset, sort_by: next.sort_by, order: next.order });

	const live = { pollingInterval: 10_000, skipPollingIfUnfocused: true };
	const traces = useGetTracesQuery({ filters, pagination }, { skip: url.view !== "traces", ...live });
	const spans = useGetTraceSpansQuery({ filters, pagination }, { skip: url.view !== "spans", ...live });
	const sessions = useGetTraceSessionsQuery({ filters, pagination }, { skip: url.view !== "sessions", ...live });
	const experiments = useGetTraceExperimentsQuery({ projectIds: url.project_ids }, { skip: url.view !== "experiments", ...live });
	const stats = useGetTraceStatsQuery(filters, live);
	const histogram = useGetTraceHistogramQuery({ filters, bucketSizeSeconds: bucketFor(endSeconds - startSeconds) }, live);
	// Whether anything was ever received, independent of the filters: the empty state is for a gateway with no traces.
	const anyTrace = useGetTraceStatsQuery({}, { pollingInterval: 10_000, skipPollingIfUnfocused: true });

	const sidebarFilters: TracingSidebarFilters = {
		project_ids: url.project_ids,
		status: url.status,
		models: url.models,
		kinds: url.kinds,
		environments: url.environments,
		session_id: url.session_id,
		trace_id: url.trace_id,
	};
	const traceColumns = useMemo(() => createTraceColumns(t, projects), [t, projects]);
	const spanColumns = useMemo(() => createSpanColumns(t), [t]);
	const sessionColumns = useMemo(() => createSessionColumns(t), [t]);
	const experimentColumns = useMemo(() => createExperimentColumns(t), [t]);
	const columnLabels = useMemo(() => getTraceColumnLabels(t), [t]);
	// One column layout per list, each kept in its own URL key and storage slot, as the log pages keep theirs.
	const traceConfig = useColumnConfig({
		columnIds: traceColumns.map((column) => column.id as string),
		paramName: "trace_cols",
		storageKey: "bifrost.traces.cols",
		defaultHidden: ["tags", "project", "session"],
		fixedColumns: { left: ["status"] },
	});
	const spanConfig = useColumnConfig({
		columnIds: spanColumns.map((column) => column.id as string),
		paramName: "span_cols",
		storageKey: "bifrost.trace_spans.cols",
		fixedColumns: { left: ["status"] },
	});
	const sessionConfig = useColumnConfig({
		columnIds: sessionColumns.map((column) => column.id as string),
		paramName: "session_cols",
		storageKey: "bifrost.trace_sessions.cols",
		fixedColumns: { left: ["status"] },
	});
	const experimentConfig = useColumnConfig({
		columnIds: experimentColumns.map((column) => column.id as string),
		paramName: "experiment_cols",
		storageKey: "bifrost.trace_experiments.cols",
		fixedColumns: { left: ["status"] },
	});
	const activeConfig =
		url.view === "spans"
			? spanConfig
			: url.view === "sessions"
				? sessionConfig
				: url.view === "experiments"
					? experimentConfig
					: traceConfig;
	const tableProps = (config: typeof traceConfig) => ({
		columnEntries: config.entries,
		columnOrder: config.columnOrder,
		columnVisibility: config.columnVisibility,
		columnPinning: config.columnPinning,
		onToggleColumnVisibility: config.toggleVisibility,
		onTogglePin: config.togglePin,
		onReorderColumns: config.reorder,
	});
	// The sheet steps through the rows of the list behind it, as the log sheets do.
	const navigable =
		url.view === "spans"
			? (spans.data?.spans ?? []).map((span) => ({ project: span.project_id, trace: span.trace_id, span: span.span_id }))
			: (traces.data?.traces ?? []).map((trace) => ({ project: trace.project_id, trace: trace.trace_id, span: "" }));
	const position = navigable.findIndex((row) => row.trace === url.trace && (url.view !== "spans" || row.span === url.span));
	const navigate = (direction: "prev" | "next") => {
		const target = navigable[position + (direction === "prev" ? -1 : 1)];
		if (!target) return;
		setUrl({ project: target.project, trace: target.trace, span: target.span });
	};
	// A link with a trace and no project (older links, hand-edited URLs) finds the project by trace id.
	const traceProject = useGetTracesQuery(
		{ filters: { trace_id: url.trace }, pagination: { limit: 1, offset: 0 } },
		{ skip: !url.trace || !!url.project },
	);
	const linkedProject = useMemo(
		() => projects.find((project) => project.id === url.project || project.name === url.project)?.id ?? url.project,
		[projects, url.project],
	);
	const refresh = () => {
		setWindowEnd(Date.now());
		void stats.refetch();
		void histogram.refetch();
	};

	if (projectsLoading || anyTrace.isLoading) return <FullPageLoader />;
	if ((anyTrace.data?.stats.trace_count ?? 0) === 0 && !url.trace) {
		return (
			<div
				className="bg-card h-[calc(var(--app-content-viewport)_-_var(--app-bottom-padding))] rounded-md border"
				data-testid="tracing-page"
			>
				<TracingEmptyState />
			</div>
		);
	}

	const totalOf = (result?: { pagination: TracePagination }) => result?.pagination.total_count ?? 0;

	return (
		<div className="dark:bg-card bg-white" data-testid="tracing-page">
			<div className="no-padding-parent no-border-parent bg-background flex h-[calc(var(--app-content-viewport)_-_var(--app-bottom-padding))] w-full gap-3">
				<TracingFilterSidebar filters={sidebarFilters} onFiltersChange={(next) => setUrl({ ...next, offset: 0 })} projects={projects} />
				<div className="bg-card flex min-w-0 flex-1 flex-col gap-2 overflow-hidden rounded-md border">
					<div className="flex flex-wrap items-center gap-2 p-4 pb-0">
						<form
							className="relative min-w-[220px] flex-1"
							onSubmit={(event) => {
								event.preventDefault();
								setUrl({ search: searchDraft.trim(), offset: 0 });
							}}
						>
							<Search className="text-muted-foreground absolute top-1/2 left-2.5 h-4 w-4 -translate-y-1/2" />
							<Input
								data-testid="tracing-search-input"
								className="h-7.5 pl-8"
								placeholder={t("tracing.searchPlaceholder")}
								value={searchDraft}
								onChange={(event) => setSearchDraft(event.target.value)}
							/>
						</form>
						<Tabs
							value={url.view}
							// A tab switch starts the view clean: it drops what a session or experiment drill-down
							// set and any open trace. Sidebar filters stay, since they are visible and shared.
							onValueChange={(view) =>
								setUrl({
									view,
									session_id: null,
									experiment: null,
									trace_id: null,
									project: null,
									trace: null,
									span: null,
									offset: 0,
									sort_by: null,
									order: null,
								})
							}
						>
							<TabsList className="h-7.5">
								<TabsTrigger value="traces" className="text-xs" data-testid="tracing-traces-tab">
									{t("tracing.tabs.traces")}
								</TabsTrigger>
								<TabsTrigger value="spans" className="text-xs" data-testid="tracing-spans-tab">
									{t("tracing.tabs.spans")}
								</TabsTrigger>
								<TabsTrigger value="sessions" className="text-xs" data-testid="tracing-sessions-tab">
									{t("tracing.tabs.sessions")}
								</TabsTrigger>
								<TabsTrigger value="experiments" className="text-xs" data-testid="tracing-experiments-tab">
									{t("tracing.tabs.experiments")}
								</TabsTrigger>
							</TabsList>
						</Tabs>
						<Select
							value={zoomed ? "custom" : url.range}
							onValueChange={(range) => setUrl({ range, start_time: null, end_time: null, offset: 0 })}
						>
							<SelectTrigger className="!h-7.5 w-36" data-testid="tracing-range-select">
								<SelectValue />
							</SelectTrigger>
							<SelectContent>
								{RANGE_KEYS.map((range) => (
									<SelectItem key={range} value={range}>
										{t(`tracing.range.${range}`)}
									</SelectItem>
								))}
								{zoomed && <SelectItem value="custom">{t("tracing.range.custom")}</SelectItem>}
							</SelectContent>
						</Select>
						<ColumnConfigDropdown
							entries={activeConfig.entries}
							labels={columnLabels}
							onToggleVisibility={activeConfig.toggleVisibility}
							onReset={activeConfig.reset}
						/>
						<Button
							variant="outline"
							size="icon"
							className="size-7.5"
							aria-label={t("tracing.refresh")}
							data-testid="tracing-refresh-button"
							onClick={refresh}
						>
							<RefreshCw className="h-4 w-4" />
						</Button>
						<Button
							variant="outline"
							size="icon"
							className="size-7.5"
							aria-label={t("tracing.settings.button")}
							data-testid="tracing-settings-button"
							onClick={() => setSettingsOpen(true)}
						>
							<Settings className="h-4 w-4" />
						</Button>
					</div>
					{(url.experiment || url.session_id) && (
						<div className="text-muted-foreground flex items-center gap-2 px-4 text-xs">
							{url.experiment
								? t("tracing.experiments.filtered", { experiment: url.experiment })
								: t("tracing.sessions.filtered", { session: url.session_id })}
							<Button
								variant="link"
								size="sm"
								className="h-auto p-0 text-xs"
								data-testid="tracing-clear-scope-button"
								onClick={() => setUrl({ experiment: "", session_id: "", offset: 0 })}
							>
								{t("tracing.clear")}
							</Button>
						</div>
					)}
					<div className="shrink-0 px-4">
						<TraceMetricStrip data={stats.data} histogram={histogram.data} loading={stats.isFetching} error={stats.error} />
					</div>
					<div className="shrink-0 px-4">
						<LogsVolumeChart
							data={toVolumeHistogram(histogram.data)}
							loading={histogram.isLoading}
							onTimeRangeChange={(start, end) => setUrl({ start_time: start, end_time: end, offset: 0 })}
							onResetZoom={() => setUrl({ start_time: null, end_time: null, offset: 0 })}
							isZoomed={zoomed}
							startTime={startSeconds}
							endTime={endSeconds}
							isOpen={chartOpen}
							onOpenChange={setChartOpen}
						/>
					</div>
					{(url.view === "sessions" || url.view === "experiments") && (
						<p className="text-muted-foreground shrink-0 px-4 text-xs">
							{url.view === "sessions" ? t("tracing.sessions.hint") : t("tracing.experiments.hint")}
						</p>
					)}
					{url.view === "traces" && (
						<LogsDataTable
							columns={traceColumns}
							data={traces.data?.traces ?? []}
							getRowId={(trace) => `${trace.project_id}/${trace.trace_id}`}
							pageSizeStorageKey="bifrost.traces.pageSize"
							testIdPrefix="traces"
							waitingLabel={t("tracing.table.waiting")}
							totalItems={totalOf(traces.data)}
							loading={traces.isFetching && !traces.data}
							pagination={pagination}
							onPaginationChange={onPaginationChange}
							onRefresh={refresh}
							onRowClick={(trace) => {
								setUrl({ project: trace.project_id, trace: trace.trace_id, span: "" });
							}}
							{...tableProps(traceConfig)}
						/>
					)}
					{url.view === "spans" && (
						<LogsDataTable
							columns={spanColumns}
							data={spans.data?.spans ?? []}
							getRowId={(span) => `${span.project_id}/${span.trace_id}/${span.span_id}`}
							pageSizeStorageKey="bifrost.traceSpans.pageSize"
							testIdPrefix="trace-spans"
							waitingLabel={t("tracing.table.waiting")}
							totalItems={totalOf(spans.data)}
							loading={spans.isFetching && !spans.data}
							pagination={pagination}
							onPaginationChange={onPaginationChange}
							onRefresh={refresh}
							onRowClick={(span) => {
								setUrl({ project: span.project_id, trace: span.trace_id, span: span.span_id });
							}}
							{...tableProps(spanConfig)}
						/>
					)}
					{url.view === "sessions" && (
						<LogsDataTable
							columns={sessionColumns}
							data={sessions.data?.sessions ?? []}
							getRowId={(session) => `${session.project_id}/${session.session_id}`}
							pageSizeStorageKey="bifrost.traceSessions.pageSize"
							testIdPrefix="trace-sessions"
							waitingLabel={t("tracing.table.waiting")}
							totalItems={totalOf(sessions.data)}
							loading={sessions.isFetching && !sessions.data}
							pagination={pagination}
							onPaginationChange={onPaginationChange}
							onRefresh={refresh}
							onRowClick={(session) =>
								setUrl({
									view: "traces",
									session_id: session.session_id,
									project_ids: [session.project_id],
									offset: 0,
									sort_by: "timestamp",
									order: "asc",
								})
							}
							{...tableProps(sessionConfig)}
						/>
					)}
					{url.view === "experiments" && (
						<LogsDataTable
							columns={experimentColumns}
							data={experiments.data?.experiments ?? []}
							getRowId={(experiment) => `${experiment.project_id}/${experiment.id}`}
							pageSizeStorageKey="bifrost.traceExperiments.pageSize"
							testIdPrefix="trace-experiments"
							waitingLabel={t("tracing.table.waiting")}
							totalItems={experiments.data?.experiments?.length ?? 0}
							loading={experiments.isFetching && !experiments.data}
							pagination={{ ...pagination, offset: 0 }}
							onPaginationChange={onPaginationChange}
							onRefresh={refresh}
							onRowClick={(experiment) =>
								setUrl({
									view: "traces",
									experiment: experiment.id,
									project_ids: [experiment.project_id],
									range: "30d",
									start_time: null,
									end_time: null,
									offset: 0,
								})
							}
							{...tableProps(experimentConfig)}
						/>
					)}
				</div>
			</div>

			<TraceViewer
				projectId={url.trace ? linkedProject || traceProject.data?.traces?.[0]?.project_id || null : null}
				traceId={url.trace || null}
				initialSpanId={url.span || null}
				hasPrev={position > 0}
				hasNext={position !== -1 && position < navigable.length - 1}
				onNavigate={navigate}
				onClose={() => {
					setUrl({ trace: "", span: "", project: "" });
				}}
				renderSpanExtras={renderTraceSpanExtras}
			/>
			<ProjectSettings open={settingsOpen} onOpenChange={setSettingsOpen} projects={projects} initialProjectId={url.project_ids[0]} />
		</div>
	);
}