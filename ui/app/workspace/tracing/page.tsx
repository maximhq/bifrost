import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { parseAsSafeString } from "@/lib/queryParamsParser";
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
import { Loader2, Plug, Search, Settings } from "lucide-react";
import { parseAsInteger, useQueryStates } from "nuqs";
import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { ConnectPanel } from "./views/connectPanel";
import { ProjectSettings } from "./views/projectSettings";
import { SOURCE_LABELS } from "./views/format";
import { TraceOverview } from "./views/traceOverview";
import { ExperimentsTable, Pager, SessionsTable, SpansTable, TracesTable } from "./views/tracesTable";
import { TraceViewer } from "./views/traceViewer";
import { renderTraceSpanExtras } from "./views/traceExtras";

const RANGE_KEYS = ["1h", "24h", "7d", "30d"] as const;
const RANGES: Record<string, number> = { "1h": 3600, "24h": 86400, "7d": 604800, "30d": 2592000 };
const PAGE_SIZE = 50;
const ALL = "all";

/** Buckets that give roughly sixty bars across the chosen range. */
function bucketFor(rangeSeconds: number): number {
	return Math.max(60, Math.round(rangeSeconds / 60 / 60) * 60);
}

/**
 * Tracing: traces pushed by observability SDKs (Braintrust, Langfuse, Arize, any OTLP exporter).
 * It is separate from the gateway's own logs. A model call served by this gateway is linked to its
 * gateway log and counted once, with the gateway's numbers.
 */
export default function TracingPage() {
	const { t } = useTranslation("observability");
	const [url, setUrl] = useQueryStates({
		view: parseAsSafeString.withDefault("traces"),
		project: parseAsSafeString.withDefault(""),
		trace: parseAsSafeString.withDefault(""),
		span: parseAsSafeString.withDefault(""),
		source: parseAsSafeString.withDefault(""),
		status: parseAsSafeString.withDefault(""),
		session: parseAsSafeString.withDefault(""),
		experiment: parseAsSafeString.withDefault(""),
		search: parseAsSafeString.withDefault(""),
		range: parseAsSafeString.withDefault("24h"),
		sort_by: parseAsSafeString.withDefault("timestamp"),
		order: parseAsSafeString.withDefault("desc"),
		offset: parseAsInteger.withDefault(0),
	});
	const [connectOpen, setConnectOpen] = useState(false);
	const [settingsOpen, setSettingsOpen] = useState(false);
	const [searchDraft, setSearchDraft] = useState(url.search);
	const { data: projectsData, isLoading: projectsLoading } = useGetTraceProjectsQuery();
	const projects = useMemo(() => projectsData?.projects ?? [], [projectsData]);

	// A link printed by an SDK may name the project, not its id.
	const projectId = useMemo(() => {
		if (!url.project) return "";
		return projects.find((project) => project.id === url.project || project.name === url.project)?.id ?? url.project;
	}, [projects, url.project]);

	// The time window is fixed when the range or a filter changes, not on every render, so the
	// query key is stable and the list does not refetch in a loop.
	const [windowEnd, setWindowEnd] = useState(() => Date.now());
	useEffect(() => setWindowEnd(Date.now()), [url.range, url.project, url.source, url.status, url.session, url.search, url.view]);
	const rangeSeconds = RANGES[url.range] ?? RANGES["24h"];

	const filters: TraceFilters = useMemo(
		() => ({
			project_ids: projectId ? [projectId] : undefined,
			sources: url.source ? [url.source] : undefined,
			status: url.status || undefined,
			session_id: url.session || undefined,
			experiment_id: url.experiment || undefined,
			search: url.search || undefined,
			start_time: new Date(windowEnd - rangeSeconds * 1000).toISOString(),
			end_time: new Date(windowEnd + 60_000).toISOString(),
		}),
		[projectId, url.source, url.status, url.session, url.experiment, url.search, windowEnd, rangeSeconds],
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
	const experiments = useGetTraceExperimentsQuery(
		{ projectIds: projectId ? [projectId] : undefined },
		{ skip: url.view !== "experiments", ...live },
	);
	const stats = useGetTraceStatsQuery(filters, live);
	const histogram = useGetTraceHistogramQuery({ filters, bucketSizeSeconds: bucketFor(rangeSeconds) }, live);

	const onSort = (sortBy: string) =>
		setUrl({ sort_by: sortBy, order: url.sort_by === sortBy && url.order === "desc" ? "asc" : "desc", offset: 0 });
	const setFilter = (patch: Partial<typeof url>) => setUrl({ ...patch, offset: 0 });
	const openTrace = (project: string, trace: string, span = "") => setUrl({ project: url.project || project, trace, span });
	const [viewerProject, setViewerProject] = useState<string | null>(null);

	const isEmpty =
		!projectsLoading && (stats.data?.stats.trace_count ?? 0) === 0 && !url.search && !url.source && !url.status && !url.session;
	const loading = traces.isLoading || spans.isLoading || sessions.isLoading;

	return (
		<div className="flex h-full flex-col gap-4 p-4" data-testid="tracing-page">
			<div className="flex flex-wrap items-center gap-2">
				<div className="mr-auto">
					<h1 className="text-lg font-semibold">{t("tracing.title")}</h1>
					<p className="text-muted-foreground text-xs">{t("tracing.subtitle")}</p>
				</div>
				<form
					className="relative"
					onSubmit={(event) => {
						event.preventDefault();
						setFilter({ search: searchDraft.trim() });
					}}
				>
					<Search className="text-muted-foreground absolute top-2.5 left-2 h-4 w-4" />
					<Input
						data-testid="tracing-search-input"
						className="w-64 pl-8"
						placeholder={t("tracing.searchPlaceholder")}
						value={searchDraft}
						onChange={(event) => setSearchDraft(event.target.value)}
					/>
				</form>
				<Select value={projectId || ALL} onValueChange={(value) => setFilter({ project: value === ALL ? "" : value })}>
					<SelectTrigger className="w-44" data-testid="tracing-project-select">
						<SelectValue />
					</SelectTrigger>
					<SelectContent>
						<SelectItem value={ALL}>{t("tracing.allProjects")}</SelectItem>
						{projects.map((project) => (
							<SelectItem key={project.id} value={project.id}>
								{project.name}
							</SelectItem>
						))}
					</SelectContent>
				</Select>
				<Select value={url.source || ALL} onValueChange={(value) => setFilter({ source: value === ALL ? "" : value })}>
					<SelectTrigger className="w-40" data-testid="tracing-source-select">
						<SelectValue />
					</SelectTrigger>
					<SelectContent>
						<SelectItem value={ALL}>{t("tracing.allSources")}</SelectItem>
						{Object.entries(SOURCE_LABELS).map(([value, label]) => (
							<SelectItem key={value} value={value}>
								{label}
							</SelectItem>
						))}
					</SelectContent>
				</Select>
				<Select value={url.status || ALL} onValueChange={(value) => setFilter({ status: value === ALL ? "" : value })}>
					<SelectTrigger className="w-36" data-testid="tracing-status-select">
						<SelectValue />
					</SelectTrigger>
					<SelectContent>
						<SelectItem value={ALL}>{t("tracing.status.any")}</SelectItem>
						<SelectItem value="error">{t("tracing.status.error")}</SelectItem>
						<SelectItem value="ok">{t("tracing.status.ok")}</SelectItem>
						<SelectItem value="in_progress">{t("tracing.inProgress")}</SelectItem>
					</SelectContent>
				</Select>
				<Select value={url.range} onValueChange={(value) => setFilter({ range: value })}>
					<SelectTrigger className="w-28" data-testid="tracing-range-select">
						<SelectValue />
					</SelectTrigger>
					<SelectContent>
						{RANGE_KEYS.map((range) => (
							<SelectItem key={range} value={range}>
								{t(`tracing.range.${range}`)}
							</SelectItem>
						))}
					</SelectContent>
				</Select>
				<Button
					variant="outline"
					size="icon"
					aria-label={t("tracing.settings.button")}
					data-testid="tracing-settings-button"
					onClick={() => setSettingsOpen(true)}
				>
					<Settings className="h-4 w-4" />
				</Button>
				<Button data-testid="tracing-connect-button" onClick={() => setConnectOpen(true)}>
					<Plug className="mr-1 h-4 w-4" />
					{t("tracing.connect.button")}
				</Button>
			</div>

			{isEmpty ? (
				<div
					className="flex flex-1 flex-col items-center justify-center gap-3 rounded-md border border-dashed p-10 text-center"
					data-testid="tracing-empty-state"
				>
					<Plug className="text-muted-foreground h-8 w-8" />
					<h2 className="text-base font-medium">{t("tracing.empty.title")}</h2>
					<p className="text-muted-foreground max-w-md text-sm">{t("tracing.empty.description")}</p>
					<Button data-testid="tracing-empty-connect-button" onClick={() => setConnectOpen(true)}>
						{t("tracing.connect.button")}
					</Button>
				</div>
			) : (
				<Tabs
					value={url.view}
					onValueChange={(view) => setUrl({ view, offset: 0, sort_by: "timestamp", order: "desc" })}
					className="flex min-h-0 flex-1 flex-col"
				>
					<TabsList>
						<TabsTrigger value="overview" data-testid="tracing-overview-tab">
							{t("tracing.tabs.overview")}
						</TabsTrigger>
						<TabsTrigger value="traces" data-testid="tracing-traces-tab">
							{t("tracing.tabs.traces")}
						</TabsTrigger>
						<TabsTrigger value="spans" data-testid="tracing-spans-tab">
							{t("tracing.tabs.spans")}
						</TabsTrigger>
						<TabsTrigger value="sessions" data-testid="tracing-sessions-tab">
							{t("tracing.tabs.sessions")}
						</TabsTrigger>
						<TabsTrigger value="experiments" data-testid="tracing-experiments-tab">
							{t("tracing.tabs.experiments")}
						</TabsTrigger>
					</TabsList>
					{url.experiment && (
						<div className="text-muted-foreground flex items-center gap-2 pt-2 text-xs">
							{t("tracing.experiments.filtered", { experiment: url.experiment })}
							<Button
								variant="link"
								size="sm"
								className="h-auto p-0 text-xs"
								data-testid="tracing-clear-experiment-button"
								onClick={() => setFilter({ experiment: "" })}
							>
								{t("tracing.clear")}
							</Button>
						</div>
					)}
					{url.session && (
						<div className="text-muted-foreground flex items-center gap-2 pt-2 text-xs">
							{t("tracing.sessions.filtered", { session: url.session })}
							<Button
								variant="link"
								size="sm"
								className="h-auto p-0 text-xs"
								data-testid="tracing-clear-session-button"
								onClick={() => setFilter({ session: "" })}
							>
								{t("tracing.clear")}
							</Button>
						</div>
					)}
					<TabsContent value="overview" className="min-h-0 flex-1 overflow-auto">
						<TraceOverview data={stats.data} histogram={histogram.data} projects={projects} />
					</TabsContent>
					<TabsContent value="traces" className="min-h-0 flex-1 overflow-auto">
						{loading && <Loader2 className="text-muted-foreground m-6 h-5 w-5 animate-spin" />}
						<TracesTable
							traces={traces.data?.traces ?? []}
							pagination={traces.data?.pagination ?? pagination}
							onSort={onSort}
							onOpen={(trace) => {
								setViewerProject(trace.project_id);
								openTrace(trace.project_id, trace.trace_id);
							}}
						/>
						<Pager pagination={traces.data?.pagination ?? pagination} onPage={(offset) => setUrl({ offset })} />
					</TabsContent>
					<TabsContent value="spans" className="min-h-0 flex-1 overflow-auto">
						<SpansTable
							spans={spans.data?.spans ?? []}
							pagination={spans.data?.pagination ?? pagination}
							onSort={onSort}
							onOpen={(span) => {
								setViewerProject(span.project_id);
								openTrace(span.project_id, span.trace_id, span.span_id);
							}}
						/>
						<Pager pagination={spans.data?.pagination ?? pagination} onPage={(offset) => setUrl({ offset })} />
					</TabsContent>
					<TabsContent value="sessions" className="min-h-0 flex-1 overflow-auto">
						<p className="text-muted-foreground pb-2 text-xs">{t("tracing.sessions.hint")}</p>
						<SessionsTable
							sessions={sessions.data?.sessions ?? []}
							onOpen={(session) =>
								setUrl({
									view: "traces",
									session: session.session_id,
									project: session.project_id,
									offset: 0,
									sort_by: "timestamp",
									order: "asc",
								})
							}
						/>
						<Pager pagination={sessions.data?.pagination ?? pagination} onPage={(offset) => setUrl({ offset })} />
					</TabsContent>
					<TabsContent value="experiments" className="min-h-0 flex-1 overflow-auto">
						<p className="text-muted-foreground pb-2 text-xs">{t("tracing.experiments.hint")}</p>
						<ExperimentsTable
							experiments={experiments.data?.experiments ?? []}
							onOpen={(experiment) =>
								setUrl({
									view: "traces",
									experiment: experiment.id,
									project: experiment.project_id,
									range: "30d",
									offset: 0,
									sort_by: "timestamp",
									order: "desc",
								})
							}
						/>
					</TabsContent>
				</Tabs>
			)}

			<TraceViewer
				projectId={url.trace ? viewerProject || projectId || null : null}
				traceId={url.trace || null}
				initialSpanId={url.span || null}
				onClose={() => {
					setViewerProject(null);
					setUrl({ trace: "", span: "" });
				}}
				renderSpanExtras={renderTraceSpanExtras}
			/>
			<ConnectPanel open={connectOpen} onOpenChange={setConnectOpen} projects={projects} />
			<ProjectSettings open={settingsOpen} onOpenChange={setSettingsOpen} projects={projects} initialProjectId={projectId} />
		</div>
	);
}