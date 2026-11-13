import { LogsVolumeChart } from "@/app/workspace/logs/views/logsVolumeChart";
import { TracingFilterSidebar, type TracingSidebarFilters } from "@/components/filters/tracingFilterSidebar";
import FullPageLoader from "@/components/fullPageLoader";
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
import { ExperimentsTable, Pager, SessionsTable, SpansTable, TracesTable } from "./views/tracesTable";
import { TraceViewer } from "./views/traceViewer";

const RANGE_KEYS = ["1h", "24h", "7d", "30d"] as const;
const RANGES: Record<string, number> = { "1h": 3600, "24h": 86400, "7d": 604800, "30d": 2592000 };
const PAGE_SIZE = 50;
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
	const pagination: TracePagination = {
		limit: PAGE_SIZE,
		offset: url.offset,
		sort_by: url.sort_by,
		order: url.order === "asc" ? "asc" : "desc",
	};

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
	const onSort = (sortBy: string) =>
		setUrl({ sort_by: sortBy, order: url.sort_by === sortBy && url.order === "desc" ? "asc" : "desc", offset: 0 });
	const [viewerProject, setViewerProject] = useState<string | null>(null);
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

	const activePagination =
		(url.view === "spans" ? spans.data?.pagination : url.view === "sessions" ? sessions.data?.pagination : traces.data?.pagination) ??
		pagination;

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
						<Tabs value={url.view} onValueChange={(view) => setUrl({ view, offset: 0, sort_by: "timestamp", order: "desc" })}>
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
					<div className="min-h-0 flex-1 overflow-auto px-4">
						{url.view === "traces" && (
							<TracesTable
								traces={traces.data?.traces ?? []}
								pagination={activePagination}
								onSort={onSort}
								onOpen={(trace) => {
									setViewerProject(trace.project_id);
									setUrl({ trace: trace.trace_id, span: "" });
								}}
							/>
						)}
						{url.view === "spans" && (
							<SpansTable
								spans={spans.data?.spans ?? []}
								pagination={activePagination}
								onSort={onSort}
								onOpen={(span) => {
									setViewerProject(span.project_id);
									setUrl({ trace: span.trace_id, span: span.span_id });
								}}
							/>
						)}
						{url.view === "sessions" && (
							<>
								<p className="text-muted-foreground py-2 text-xs">{t("tracing.sessions.hint")}</p>
								<SessionsTable
									sessions={sessions.data?.sessions ?? []}
									onOpen={(session) =>
										setUrl({
											view: "traces",
											session_id: session.session_id,
											project_ids: [session.project_id],
											offset: 0,
											sort_by: "timestamp",
											order: "asc",
										})
									}
								/>
							</>
						)}
						{url.view === "experiments" && (
							<>
								<p className="text-muted-foreground py-2 text-xs">{t("tracing.experiments.hint")}</p>
								<ExperimentsTable
									experiments={experiments.data?.experiments ?? []}
									onOpen={(experiment) =>
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
								/>
							</>
						)}
					</div>
					{url.view !== "experiments" && (
						<div className="shrink-0 px-4 pb-2">
							<Pager pagination={activePagination} onPage={(offset) => setUrl({ offset })} />
						</div>
					)}
				</div>
			</div>

			<TraceViewer
				projectId={url.trace ? viewerProject || linkedProject || url.project_ids[0] || null : null}
				traceId={url.trace || null}
				initialSpanId={url.span || null}
				onClose={() => {
					setViewerProject(null);
					setUrl({ trace: "", span: "", project: "" });
				}}
				renderSpanExtras={renderTraceSpanExtras}
			/>
			<ProjectSettings open={settingsOpen} onOpenChange={setSettingsOpen} projects={projects} initialProjectId={url.project_ids[0]} />
		</div>
	);
}