import FullPageLoader from "@/components/fullPageLoader";
import { useColumnConfig } from "@/components/table";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Card, CardContent } from "@/components/ui/card";
import { LogsVolumeChart } from "@/app/workspace/logs/views/logsVolumeChart";
import { parseAsSafeArrayOf, parseAsSafeString } from "@/lib/queryParamsParser";
import { getErrorMessage } from "@/lib/store";
import {
	useDeleteAgentLogsMutation,
	useGetAgentHistogramQuery,
	useGetAgentLogByIdQuery,
	useGetAgentLogsQuery,
	useGetAgentLogStatsQuery,
	useLazyGetAgentLogsQuery,
} from "@/lib/store/apis/agentLogsApi";
import type { AgentLogFilters, AgentLogSummary, AgentPagination } from "@/lib/types/agentLogs";
import { dateUtils } from "@/lib/types/logs";
import { COMPACT_NUMBER_FORMAT } from "@/lib/utils/numbers";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import NumberFlow from "@number-flow/react";
import { useLocation } from "@tanstack/react-router";
import { AlertCircle, CheckCircle, Clock, Hash, XCircle } from "lucide-react";
import { parseAsBoolean, parseAsInteger, parseAsString, useQueryStates } from "nuqs";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { AgentContextDetailsSheet } from "./views/agentContextDetailsSheet";
import { AgentFilterSidebar } from "./views/agentFilterSidebar";
import { AgentHeaderView } from "./views/agentHeaderView";
import { AgentLogDetailSheet } from "./views/agentLogDetailsSheet";
import { AgentLogsDataTable } from "./views/agentLogsTable";
import { AGENT_COLUMN_LABELS, createAgentColumns } from "./views/columns";
import { AgentEmptyState } from "./views/emptyState";

// Filters carried in the URL, matching the backend's query parameter names.
// The multi-value ones round-trip as comma-separated lists, so pre-existing
// single-value links still parse into a one-element array.
const TEXT_FILTER_KEYS = ["request_id", "trace_id", "task_id", "context_id", "push_config_id", "delivery_id", "attempt_id"] as const;

const MULTI_FILTER_KEYS = [
	"agent_name",
	"operation",
	"user_id",
	"virtual_key_id",
	"team_id",
	"customer_id",
	"business_unit_id",
	"project_id",
	"status",
	"task_state",
] as const;

// "Has any logs" is probed over 30 days — the same period the page falls
// back to when the selected range is empty.
const FALLBACK_PERIOD = "30d";
const PROBE_ARGS = {
	filters: { period: FALLBACK_PERIOD },
	pagination: { limit: 1, offset: 0, sort_by: "timestamp" as const, order: "desc" as const },
};

export default function AgentLogsPage() {
	const [error, setError] = useState<string | null>(null);
	const didWidenPeriod = useRef(false);
	const hasDeleteAccess = useRbac(RbacResource.AgentLogs, RbacOperation.Delete);

	const [deleteLogs] = useDeleteAgentLogsMutation();
	// Lazy query kept only for handleLogNavigate (fetches adjacent pages on demand)
	const [triggerGetLogs] = useLazyGetAgentLogsQuery();

	const defaultTimeRange = useMemo(() => dateUtils.getDefaultTimeRange(), []);

	const { search } = useLocation();
	const hasExplicitTimeRange = (search as Record<string, unknown>)?.start_time && (search as Record<string, unknown>)?.end_time;

	// URL state management
	const [urlState, setUrlState] = useQueryStates(
		{
			agent_name: parseAsSafeArrayOf.withDefault([]),
			operation: parseAsSafeArrayOf.withDefault([]),
			user_id: parseAsSafeArrayOf.withDefault([]),
			virtual_key_id: parseAsSafeArrayOf.withDefault([]),
			team_id: parseAsSafeArrayOf.withDefault([]),
			customer_id: parseAsSafeArrayOf.withDefault([]),
			business_unit_id: parseAsSafeArrayOf.withDefault([]),
			project_id: parseAsSafeArrayOf.withDefault([]),
			request_id: parseAsSafeString.withDefault(""),
			trace_id: parseAsSafeString.withDefault(""),
			task_id: parseAsSafeString.withDefault(""),
			context_id: parseAsSafeString.withDefault(""),
			push_config_id: parseAsSafeString.withDefault(""),
			delivery_id: parseAsSafeString.withDefault(""),
			attempt_id: parseAsSafeString.withDefault(""),
			status: parseAsSafeArrayOf.withDefault([]),
			task_state: parseAsSafeArrayOf.withDefault([]),
			search: parseAsSafeString.withDefault(""),
			start_time: parseAsInteger.withDefault(defaultTimeRange.startTime),
			end_time: parseAsInteger.withDefault(defaultTimeRange.endTime),
			limit: parseAsInteger.withDefault(50),
			offset: parseAsInteger.withDefault(0),
			sort_by: parseAsString.withDefault("timestamp"),
			order: parseAsString.withDefault("desc"),
			polling: parseAsBoolean.withDefault(true).withOptions({ clearOnDefault: false }),
			period: parseAsString.withDefault(hasExplicitTimeRange ? "" : "1h").withOptions({ clearOnDefault: false }),
			selected_log: parseAsString.withDefault(""),
			selected_session: parseAsString.withDefault(""),
		},
		{
			history: "push",
			shallow: false,
		},
	);

	const selectedLogId = urlState.selected_log || null;
	const selectedContextId = urlState.selected_session || null;
	const polling = urlState.polling;
	// Rows opened from the detail-sheet timeline may not be on the current table
	// page; this holds their summary so the sheet can still render them.
	const [selectedOverride, setSelectedOverride] = useState<AgentLogSummary | null>(null);
	const [contextCurrentLogId, setContextCurrentLogId] = useState<string | null>(null);
	const [isChartOpen, setIsChartOpen] = useState(true);

	// Convert URL state to filters for the API. When a relative period is set,
	// the API slice computes a fresh time window on every request; for custom
	// absolute ranges (period === "") the stored timestamps are used.
	const filters: AgentLogFilters = useMemo(
		() => ({
			agent_name: urlState.agent_name.length > 0 ? urlState.agent_name : undefined,
			operation: urlState.operation.length > 0 ? urlState.operation : undefined,
			user_id: urlState.user_id.length > 0 ? urlState.user_id : undefined,
			virtual_key_id: urlState.virtual_key_id.length > 0 ? urlState.virtual_key_id : undefined,
			team_id: urlState.team_id.length > 0 ? urlState.team_id : undefined,
			customer_id: urlState.customer_id.length > 0 ? urlState.customer_id : undefined,
			business_unit_id: urlState.business_unit_id.length > 0 ? urlState.business_unit_id : undefined,
			project_id: urlState.project_id.length > 0 ? urlState.project_id : undefined,
			request_id: urlState.request_id || undefined,
			trace_id: urlState.trace_id || undefined,
			task_id: urlState.task_id || undefined,
			context_id: urlState.context_id || undefined,
			push_config_id: urlState.push_config_id || undefined,
			delivery_id: urlState.delivery_id || undefined,
			attempt_id: urlState.attempt_id || undefined,
			status: urlState.status.length > 0 ? urlState.status : undefined,
			record_kind: ["request"],
			task_state: urlState.task_state.length > 0 ? urlState.task_state : undefined,
			search: urlState.search || undefined,
			...(urlState.period
				? { period: urlState.period }
				: {
						start_time: dateUtils.toISOString(urlState.start_time),
						end_time: dateUtils.toISOString(urlState.end_time),
					}),
		}),
		[
			urlState.agent_name,
			urlState.operation,
			urlState.user_id,
			urlState.virtual_key_id,
			urlState.team_id,
			urlState.customer_id,
			urlState.business_unit_id,
			urlState.project_id,
			urlState.request_id,
			urlState.trace_id,
			urlState.task_id,
			urlState.context_id,
			urlState.push_config_id,
			urlState.delivery_id,
			urlState.attempt_id,
			urlState.status,
			urlState.task_state,
			urlState.search,
			urlState.period,
			urlState.start_time,
			urlState.end_time,
		],
	);

	const pagination: AgentPagination = useMemo(
		() => ({
			limit: urlState.limit,
			offset: urlState.offset,
			sort_by: urlState.sort_by as "timestamp" | "latency",
			order: urlState.order as "asc" | "desc",
		}),
		[urlState.limit, urlState.offset, urlState.sort_by, urlState.order],
	);

	const {
		data: logsData,
		isLoading: logsIsLoading,
		isFetching: logsIsFetching,
		error: logsError,
		refetch: refetchLogs,
	} = useGetAgentLogsQuery(
		{ filters, pagination },
		{
			pollingInterval: polling ? 10000 : 0,
			skipPollingIfUnfocused: true,
		},
	);

	// Unfiltered probe used only to tell "no Agent logs at all" apart from "no Agent
	// logs in the selected range". The history API exposes no has_logs flag, so
	// this is a one-row lookup over the widest window it allows.
	const {
		data: probeData,
		isLoading: probeIsLoading,
		refetch: refetchProbe,
	} = useGetAgentLogsQuery(PROBE_ARGS, {
		pollingInterval: 30000,
		skipPollingIfUnfocused: true,
	});
	const hasAnyLogs = (probeData?.pagination?.total_count ?? 0) > 0;

	const {
		data: statsData,
		isFetching: statsIsFetching,
		refetch: refetchStats,
	} = useGetAgentLogStatsQuery(
		{ filters },
		{
			pollingInterval: polling ? 10000 : 0,
			skipPollingIfUnfocused: true,
		},
	);

	const {
		data: histogram,
		isLoading: histogramIsLoading,
		refetch: refetchHistogram,
	} = useGetAgentHistogramQuery(
		{ filters },
		{
			pollingInterval: polling ? 10000 : 0,
			skipPollingIfUnfocused: true,
		},
	);

	const refreshAllData = useCallback(() => {
		setError(null);
		refetchLogs();
		refetchProbe();
		refetchStats();
		refetchHistogram();
	}, [refetchLogs, refetchProbe, refetchStats, refetchHistogram]);

	// Zero values keep the cards on screen when a filter matches nothing, rather
	// than collapsing the page chrome.
	const statCards = useMemo(
		() => [
			{
				title: "Operations",
				value: <NumberFlow value={statsData?.total_entries ?? 0} format={COMPACT_NUMBER_FORMAT} />,
				icon: <Hash className="size-4" />,
			},
			{
				title: "Success Rate",
				value: (
					<NumberFlow value={statsData?.success_rate ?? 0} format={{ minimumFractionDigits: 2, maximumFractionDigits: 2 }} suffix="%" />
				),
				icon: <CheckCircle className="size-4" />,
			},
			{
				title: "Failed Operations",
				value: <NumberFlow value={statsData?.error_count ?? 0} format={COMPACT_NUMBER_FORMAT} />,
				icon: <XCircle className="size-4" />,
			},
			{
				title: "Avg Duration",
				value: (
					<NumberFlow value={statsData?.average_latency ?? 0} format={{ minimumFractionDigits: 2, maximumFractionDigits: 2 }} suffix="ms" />
				),
				icon: <Clock className="size-4" />,
			},
		],
		[statsData],
	);

	// Derive data directly from RTK
	const logs = useMemo(() => logsData?.logs ?? [], [logsData]);
	const totalItems = logsData?.pagination?.total_count ?? 0;

	const selectedPageLog = useMemo(() => (selectedLogId ? logs.find((log) => log.id === selectedLogId) : undefined), [selectedLogId, logs]);
	const { data: selectedLogById } = useGetAgentLogByIdQuery(selectedLogId ?? "", {
		skip: !selectedLogId || Boolean(selectedPageLog) || selectedOverride?.id === selectedLogId,
	});
	const selectedLog = selectedPageLog ?? (selectedOverride?.id === selectedLogId ? selectedOverride : (selectedLogById ?? null));

	const hasNonTimeFilters = useMemo(
		() =>
			Boolean(urlState.search) ||
			TEXT_FILTER_KEYS.some((key) => Boolean(urlState[key])) ||
			MULTI_FILTER_KEYS.some((key) => urlState[key].length > 0),
		[urlState],
	);

	// The onboarding empty state is reserved for accounts with no Agent logs at
	// all. An empty selected range keeps the full page so the user can widen it.
	const showEmptyState = Boolean(probeData) && !hasAnyLogs;

	// The default range is one hour. When logs exist but none fall inside it,
	// widen to 30 days once so the user lands on data instead of an empty table.
	useEffect(() => {
		if (didWidenPeriod.current) return;
		if (!logsData || !hasAnyLogs) return;
		if (hasNonTimeFilters || totalItems > 0) return;
		if (urlState.period !== "1h") return;
		didWidenPeriod.current = true;
		setUrlState({ period: FALLBACK_PERIOD, offset: 0 }, { history: "replace" });
	}, [logsData, hasAnyLogs, hasNonTimeFilters, totalItems, urlState.period, setUrlState]);

	// Helper to update filters in URL
	const setFilters = useCallback(
		(newFilters: AgentLogFilters) => {
			setError(null);
			setUrlState({
				search: newFilters.search || "",
				agent_name: newFilters.agent_name || [],
				operation: newFilters.operation || [],
				user_id: newFilters.user_id || [],
				virtual_key_id: newFilters.virtual_key_id || [],
				team_id: newFilters.team_id || [],
				customer_id: newFilters.customer_id || [],
				business_unit_id: newFilters.business_unit_id || [],
				project_id: newFilters.project_id || [],
				request_id: newFilters.request_id || "",
				trace_id: newFilters.trace_id || "",
				task_id: newFilters.task_id || "",
				context_id: newFilters.context_id || "",
				push_config_id: newFilters.push_config_id || "",
				delivery_id: newFilters.delivery_id || "",
				attempt_id: newFilters.attempt_id || "",
				status: newFilters.status || [],
				task_state: newFilters.task_state || [],
				offset: 0,
			});
		},
		[setUrlState],
	);

	const setPagination = useCallback(
		(newPagination: AgentPagination) => {
			setUrlState({
				limit: newPagination.limit,
				offset: newPagination.offset,
				sort_by: newPagination.sort_by,
				order: newPagination.order,
			});
		},
		[setUrlState],
	);

	// The header debounces the input, so this only runs once the user settles.
	const handleSearchChange = useCallback(
		(search: string) => {
			setUrlState({ search, offset: 0 });
		},
		[setUrlState],
	);

	// Dragging or clicking the volume chart narrows the window, exactly as on
	// the LLM and MCP log pages.
	const handleTimeRangeChange = useCallback(
		(startTime: number, endTime: number) => {
			setUrlState({ period: "", start_time: startTime, end_time: endTime, offset: 0, polling: false });
		},
		[setUrlState],
	);

	const handleResetZoom = useCallback(() => {
		const now = Math.floor(Date.now() / 1000);
		setUrlState({ period: "1h", start_time: now - 60 * 60, end_time: now, offset: 0, polling: true });
	}, [setUrlState]);

	const isZoomed = useMemo(() => {
		if (urlState.period) return false;
		return urlState.end_time - urlState.start_time < 60 * 60 * 0.9;
	}, [urlState.start_time, urlState.end_time, urlState.period]);

	const handlePeriodChange = useCallback(
		(p?: string, from?: Date, to?: Date) => {
			if (p) {
				setUrlState({
					period: p,
					offset: 0,
					polling: true,
				});
			} else if (from && to) {
				setUrlState({
					start_time: Math.floor(from.getTime() / 1000),
					end_time: Math.floor(to.getTime() / 1000),
					offset: 0,
					polling: false,
					period: "",
				});
			}
		},
		[setUrlState],
	);

	const handlePollToggle = useCallback(
		(enabled: boolean) => {
			setUrlState({ polling: enabled }, { history: "replace" });
			if (enabled) refreshAllData();
		},
		[setUrlState, refreshAllData],
	);

	const handleDelete = useCallback(
		async (log: AgentLogSummary) => {
			if (!hasDeleteAccess) throw new Error("No delete access");
			setError(null);
			try {
				await deleteLogs({ ids: [log.id] }).unwrap();
				if (urlState.selected_log === log.id) {
					setUrlState({ selected_log: "" });
				}
				refreshAllData();
			} catch (err) {
				const errorMessage = getErrorMessage(err);
				setError(errorMessage);
				throw new Error(errorMessage);
			}
		},
		[deleteLogs, hasDeleteAccess, urlState.selected_log, setUrlState, refreshAllData],
	);

	const columns = useMemo(() => createAgentColumns(hasDeleteAccess ? handleDelete : undefined), [hasDeleteAccess, handleDelete]);

	const columnIds = useMemo(
		() => columns.map((col) => ("id" in col && col.id ? col.id : "accessorKey" in col ? String(col.accessorKey) : "")).filter(Boolean),
		[columns],
	);

	const {
		entries: columnEntries,
		columnOrder,
		columnVisibility,
		columnPinning,
		toggleVisibility: toggleColumnVisibility,
		togglePin: toggleColumnPin,
		reorder: reorderColumns,
		reset: resetColumns,
	} = useColumnConfig({
		columnIds,
		paramName: "agent_cols",
		storageKey: "bifrost.agent_logs.cols",
		fixedColumns: hasDeleteAccess ? { right: ["actions"] } : undefined,
	});

	const selectedLogIndex = useMemo(() => (selectedLogId ? logs.findIndex((l) => l.id === selectedLogId) : -1), [selectedLogId, logs]);

	const openLog = useCallback(
		(row: AgentLogSummary) => {
			setSelectedOverride(row);
			setUrlState({ selected_log: row.id }, { history: "push" });
		},
		[setUrlState],
	);

	const handleDetailFilter = useCallback(
		(filters: Partial<Omit<AgentLogFilters, "period" | "start_time" | "end_time">>) => {
			setContextCurrentLogId(null);
			setUrlState({ ...filters, selected_log: "", selected_session: "", offset: 0 }, { history: "push" });
		},
		[setUrlState],
	);

	const handleContextSheetOpenChange = useCallback(
		(open: boolean) => {
			if (!open) {
				setContextCurrentLogId(null);
				setUrlState({ selected_session: "" }, { history: "push" });
			}
		},
		[setUrlState],
	);

	const handleLogNavigate = useCallback(
		(direction: "prev" | "next") => {
			setError(null);
			const replaceHistory = { history: "replace" as const };
			const currentLogId = selectedLogId || "";
			if (direction === "prev") {
				if (selectedLogIndex > 0) {
					const target = logs[selectedLogIndex - 1];
					setSelectedOverride(target);
					setUrlState({ selected_log: target.id }, replaceHistory);
				} else if (pagination.offset > 0) {
					const newOffset = Math.max(0, pagination.offset - pagination.limit);
					setUrlState({ offset: newOffset, selected_log: "" }, replaceHistory);
					triggerGetLogs({
						filters,
						pagination: { ...pagination, offset: newOffset },
					}).then((result) => {
						const pageLogs = result.data?.logs;
						if (pageLogs?.length) {
							const target = pageLogs[pageLogs.length - 1];
							setSelectedOverride(target);
							setUrlState({ selected_log: target.id }, replaceHistory);
						} else if (result.error) {
							setUrlState({ offset: pagination.offset, selected_log: currentLogId }, replaceHistory);
							setError(getErrorMessage(result.error));
						}
					});
				}
			} else {
				if (selectedLogIndex >= 0 && selectedLogIndex < logs.length - 1) {
					const target = logs[selectedLogIndex + 1];
					setSelectedOverride(target);
					setUrlState({ selected_log: target.id }, replaceHistory);
				} else if (pagination.offset + pagination.limit < totalItems) {
					const newOffset = pagination.offset + pagination.limit;
					setUrlState({ offset: newOffset, selected_log: "" }, replaceHistory);
					triggerGetLogs({
						filters,
						pagination: { ...pagination, offset: newOffset },
					}).then((result) => {
						const pageLogs = result.data?.logs;
						if (pageLogs?.length) {
							const target = pageLogs[0];
							setSelectedOverride(target);
							setUrlState({ selected_log: target.id }, replaceHistory);
						} else if (result.error) {
							setUrlState({ offset: pagination.offset, selected_log: currentLogId }, replaceHistory);
							setError(getErrorMessage(result.error));
						}
					});
				}
			}
		},
		[selectedLogId, selectedLogIndex, logs, pagination, totalItems, filters, setUrlState, triggerGetLogs],
	);

	const displayError = error ?? (logsError ? getErrorMessage(logsError as Parameters<typeof getErrorMessage>[0]) : null);

	return (
		<div className="dark:bg-card bg-white">
			{logsIsLoading || probeIsLoading ? (
				<FullPageLoader />
			) : showEmptyState ? (
				<AgentEmptyState error={displayError} />
			) : (
				<div className="no-padding-parent no-border-parent bg-background flex h-[calc(var(--app-content-viewport)_-_var(--app-bottom-padding))] w-full gap-3">
					{/* Sidebar Filters */}
					<AgentFilterSidebar filters={filters} onFiltersChange={setFilters} />

					{/* Main Content */}
					<div className="bg-card flex min-w-0 flex-1 flex-col gap-2 overflow-hidden rounded-md border">
						<div className="p-4 pb-0">
							<AgentHeaderView
								filters={filters}
								onSearchChange={handleSearchChange}
								period={urlState.period}
								onPeriodChange={handlePeriodChange}
								polling={polling}
								onPollToggle={handlePollToggle}
								onRefresh={refreshAllData}
								loading={logsIsFetching}
								columnEntries={columnEntries}
								columnLabels={AGENT_COLUMN_LABELS}
								onToggleColumnVisibility={toggleColumnVisibility}
								onResetColumns={resetColumns}
							/>
						</div>

						{/* Quick Stats */}
						<div className="px-4">
							<div className="grid shrink-0 grid-cols-1 gap-4 md:grid-cols-4">
								{statCards.map((card) => (
									<Card key={card.title} className="py-4 shadow-none">
										<CardContent
											className={`flex items-center justify-between px-4 transition-opacity duration-200 ${statsIsFetching ? "opacity-50" : "opacity-100"}`}
										>
											<div className="w-full min-w-0">
												<div className="text-muted-foreground text-xs">{card.title}</div>
												<div className="truncate font-mono text-xl font-medium sm:text-2xl">{card.value}</div>
											</div>
										</CardContent>
									</Card>
								))}
							</div>

							<div className="mt-2">
								<LogsVolumeChart
									data={histogram ?? null}
									loading={histogramIsLoading}
									onTimeRangeChange={handleTimeRangeChange}
									onResetZoom={handleResetZoom}
									isZoomed={isZoomed}
									startTime={urlState.start_time}
									endTime={urlState.end_time}
									period={urlState.period}
									isOpen={isChartOpen}
									onOpenChange={setIsChartOpen}
								/>
							</div>
						</div>

						{displayError && (
							<div className="px-4">
								<Alert variant="destructive" className="shrink-0">
									<AlertCircle className="h-4 w-4" />
									<AlertDescription>{displayError}</AlertDescription>
								</Alert>
							</div>
						)}

						<AgentLogsDataTable
							columns={columns}
							data={logs}
							totalItems={totalItems}
							loading={logsIsFetching}
							pagination={pagination}
							onPaginationChange={setPagination}
							onRowClick={openLog}
							onRefresh={refreshAllData}
							polling={polling}
							columnEntries={columnEntries}
							columnOrder={columnOrder}
							columnVisibility={columnVisibility}
							columnPinning={columnPinning}
							onToggleColumnVisibility={toggleColumnVisibility}
							onTogglePin={toggleColumnPin}
							onReorderColumns={reorderColumns}
						/>
					</div>

					{/* Log Detail Sheet */}
					<AgentLogDetailSheet
						log={selectedLog}
						open={selectedLogId !== null}
						onOpenChange={(open) => !open && setUrlState({ selected_log: "" }, { history: "push" })}
						onFilter={handleDetailFilter}
						handleDelete={hasDeleteAccess ? handleDelete : undefined}
						onViewContext={(contextId, logId) => {
							setUrlState({ selected_log: "", selected_session: contextId }, { history: "push" });
							setContextCurrentLogId(logId);
						}}
						onNavigate={handleLogNavigate}
						hasPrev={selectedLogIndex > 0 || (selectedLogIndex !== -1 && pagination.offset > 0)}
						hasNext={selectedLogIndex !== -1 && (selectedLogIndex < logs.length - 1 || pagination.offset + pagination.limit < totalItems)}
					/>
					<AgentContextDetailsSheet
						contextId={selectedContextId}
						currentLogId={contextCurrentLogId}
						open={selectedContextId !== null}
						onOpenChange={handleContextSheetOpenChange}
						onLogClick={(log) => {
							setSelectedOverride(log);
							setContextCurrentLogId(log.id);
							setUrlState({ selected_session: "", selected_log: log.id }, { history: "push" });
						}}
						onFilterContext={(contextId) => handleDetailFilter({ context_id: contextId })}
					/>
				</div>
			)}
		</div>
	);
}