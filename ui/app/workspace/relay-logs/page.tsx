import { LogsVolumeChart } from "@/app/workspace/logs/views/logsVolumeChart";
import { RelayFilterSidebar } from "@/components/filters/relayFilterSidebar";
import FullPageLoader from "@/components/fullPageLoader";
import { useColumnConfig } from "@/components/table";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { parseAsSafeString } from "@/lib/queryParamsParser";
import {
	getErrorMessage,
	useDeleteRelayLogsMutation,
	useGetRelayLogHistogramQuery,
	useGetRelayLogsQuery,
	useGetRelayLogStatsQuery,
	useGetUserAgentMappingsQuery,
	useLazyGetRelayLogsQuery,
} from "@/lib/store";
import type { RelayLog, RelayLogFilters, RelayLogPagination } from "@/lib/types/relays";
import { dateUtils } from "@/lib/types/logs";
import { getLiveToggleState } from "@/lib/utils/timeRange";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { useLocation } from "@tanstack/react-router";
import { AlertCircle } from "lucide-react";
import { parseAsArrayOf, parseAsBoolean, parseAsFloat, parseAsInteger, parseAsString, useQueryStates } from "nuqs";
import { useCallback, useMemo, useState, useRef } from "react";
import { RelayEmptyState } from "./views/emptyState";
import { createRelayColumns, RELAY_COLUMN_LABELS } from "./views/columns";
import { RelayHeaderView } from "./views/relayHeaderView";
import { RelayLogDetailSheet } from "./views/relayLogDetailsSheet";
import { RelayLogsDataTable } from "./views/relayLogsTable";
import { RelayMetricStrip } from "./views/relayMetricStrip";

const arr = parseAsArrayOf(parseAsString).withDefault([]);

/** Relay Logs: the same layout, header, filters, chart and table as the LLM and MCP log pages. */
export default function RelayLogsPage() {
	const [error, setError] = useState<string | null>(null);
	const hasDeleteAccess = useRbac(RbacResource.RelayLogs, RbacOperation.Delete);
	const [deleteLogs] = useDeleteRelayLogsMutation();
	// Lazy query kept only for handleLogNavigate (fetches the adjacent page on demand).
	const [triggerGetLogs] = useLazyGetRelayLogsQuery();
	const { data: userAgentMappingsData } = useGetUserAgentMappingsQuery();
	const defaultTimeRange = useMemo(() => dateUtils.getDefaultTimeRange(), []);
	const { search } = useLocation();
	const hasExplicitTimeRange = (search as Record<string, unknown>)?.start_time && (search as Record<string, unknown>)?.end_time;

	const [urlState, setUrlState] = useQueryStates(
		{
			relay_names: arr,
			methods: arr,
			statuses: arr,
			kinds: arr,
			virtual_key_ids: arr,
			user_ids: arr,
			team_ids: arr,
			customer_ids: arr,
			business_unit_ids: arr,
			project_ids: arr,
			apps: arr,
			billable: parseAsString.withDefault(""),
			// No default: 0 is a legitimate bound ("max_cost=0" is free calls only), which a zero
			// default would make indistinguishable from no filter at all.
			min_cost: parseAsFloat,
			max_cost: parseAsFloat,
			request_id: parseAsSafeString.withDefault(""),
			search: parseAsSafeString.withDefault(""),
			start_time: parseAsInteger.withDefault(defaultTimeRange.startTime),
			end_time: parseAsInteger.withDefault(defaultTimeRange.endTime),
			limit: parseAsInteger.withDefault(25), // The table syncs its rows-per-page preference into this
			offset: parseAsInteger.withDefault(0),
			sort_by: parseAsString.withDefault("timestamp"),
			order: parseAsString.withDefault("desc"),
			polling: parseAsBoolean.withDefault(true).withOptions({ clearOnDefault: false }),
			period: parseAsString.withDefault(hasExplicitTimeRange ? "" : "1h").withOptions({ clearOnDefault: false }),
			selected_log: parseAsString.withDefault(""),
		},
		{ history: "push", shallow: false },
	);
	const polling = urlState.polling;
	const [isChartOpen, setIsChartOpen] = useState(true);

	const filters: RelayLogFilters = useMemo(
		() => ({
			relay_names: urlState.relay_names,
			methods: urlState.methods,
			statuses: urlState.statuses,
			kinds: urlState.kinds,
			virtual_key_ids: urlState.virtual_key_ids,
			user_ids: urlState.user_ids,
			team_ids: urlState.team_ids,
			customer_ids: urlState.customer_ids,
			business_unit_ids: urlState.business_unit_ids,
			project_ids: urlState.project_ids,
			apps: urlState.apps,
			billable: urlState.billable === "" ? undefined : urlState.billable === "true",
			min_cost: urlState.min_cost ?? undefined,
			max_cost: urlState.max_cost ?? undefined,
			request_id: urlState.request_id || undefined,
			search: urlState.search || undefined,
			...(urlState.period
				? { period: urlState.period }
				: { start_time: dateUtils.toISOString(urlState.start_time), end_time: dateUtils.toISOString(urlState.end_time) }),
		}),
		[urlState],
	);
	const pagination: RelayLogPagination = useMemo(
		() => ({
			limit: urlState.limit,
			offset: urlState.offset,
			sort_by: urlState.sort_by as RelayLogPagination["sort_by"],
			order: urlState.order as "asc" | "desc",
		}),
		[urlState.limit, urlState.offset, urlState.sort_by, urlState.order],
	);

	const pollingOptions = { pollingInterval: polling ? 10000 : 0, skipPollingIfUnfocused: true };
	const {
		data: logsData,
		isLoading: logsIsLoading,
		isFetching: logsIsFetching,
		error: logsError,
		refetch: refetchLogs,
	} = useGetRelayLogsQuery({ filters, pagination }, pollingOptions);
	const {
		data: statsData,
		isFetching: statsIsFetching,
		error: statsError,
		refetch: refetchStats,
	} = useGetRelayLogStatsQuery({ filters, comparePrevious: true }, pollingOptions);
	// One histogram feeds both the volume chart and every sparkline in the metric strip.
	const {
		data: histogram,
		isLoading: histogramIsLoading,
		isFetching: histogramIsFetching,
		refetch: refetchHistogram,
	} = useGetRelayLogHistogramQuery({ filters }, pollingOptions);

	const refreshAllData = useCallback(() => {
		refetchLogs();
		refetchStats();
		refetchHistogram();
	}, [refetchLogs, refetchStats, refetchHistogram]);

	const handleTimeRangeChange = useCallback(
		(startTime: number, endTime: number) =>
			setUrlState({ period: "", start_time: startTime, end_time: endTime, offset: 0, polling: false }),
		[setUrlState],
	);
	const handleResetZoom = useCallback(() => {
		const now = Math.floor(Date.now() / 1000);
		setUrlState({ period: "1h", start_time: now - 3600, end_time: now, offset: 0, polling: true });
	}, [setUrlState]);
	const isZoomed = useMemo(
		() => !urlState.period && urlState.end_time - urlState.start_time < 3600 * 0.9,
		[urlState.start_time, urlState.end_time, urlState.period],
	);

	const logs = useMemo(() => logsData?.logs ?? [], [logsData]);
	const totalItems = logsData?.pagination?.total_count ?? 0;
	const selectedLogId = urlState.selected_log || null;
	const selectedLogIndex = useMemo(() => (selectedLogId ? logs.findIndex((l) => l.id === selectedLogId) : -1), [selectedLogId, logs]);
	const selectedLog = selectedLogIndex >= 0 ? logs[selectedLogIndex] : null;

	const setFilters = useCallback(
		(next: RelayLogFilters) => {
			const timeChanged = next.start_time !== undefined || next.end_time !== undefined;
			setUrlState({
				...(timeChanged && !next.period ? { period: "" } : {}),
				relay_names: next.relay_names ?? [],
				methods: next.methods ?? [],
				statuses: next.statuses ?? [],
				kinds: next.kinds ?? [],
				virtual_key_ids: next.virtual_key_ids ?? [],
				user_ids: next.user_ids ?? [],
				team_ids: next.team_ids ?? [],
				customer_ids: next.customer_ids ?? [],
				business_unit_ids: next.business_unit_ids ?? [],
				project_ids: next.project_ids ?? [],
				apps: next.apps ?? [],
				billable: next.billable === undefined ? "" : String(next.billable),
				min_cost: next.min_cost ?? null,
				max_cost: next.max_cost ?? null,
				request_id: next.request_id ?? "",
				search: next.search ?? "",
				offset: 0,
			});
		},
		[setUrlState],
	);
	const setPagination = useCallback(
		(p: RelayLogPagination) => setUrlState({ limit: p.limit, offset: p.offset, sort_by: p.sort_by, order: p.order }),
		[setUrlState],
	);
	const handleDelete = useCallback(
		async (log: RelayLog) => {
			setError(null);
			try {
				await deleteLogs([log.id]).unwrap();
				if (urlState.selected_log === log.id) setUrlState({ selected_log: "" });
				refreshAllData();
			} catch (err) {
				setError(getErrorMessage(err));
				// Rethrown so the details sheet stays open on a failed delete.
				throw err;
			}
		},
		[deleteLogs, urlState.selected_log, setUrlState, refreshAllData],
	);
	const handlePeriodChange = useCallback(
		(p?: string, from?: Date, to?: Date) => {
			if (p) setUrlState({ period: p, offset: 0, polling: true });
			else if (from && to)
				setUrlState({
					start_time: Math.floor(from.getTime() / 1000),
					end_time: Math.floor(to.getTime() / 1000),
					offset: 0,
					polling: false,
					period: "",
				});
		},
		[setUrlState],
	);
	const handlePollToggle = useCallback(
		(enabled: boolean) => {
			const next = getLiveToggleState(enabled, urlState.period);
			setUrlState(next);
			if (enabled && !next.period) refreshAllData();
		},
		[setUrlState, refreshAllData, urlState.period],
	);
	// Prev/next in the details sheet. At a page edge the page is turned and the first (next) or last
	// (prev) row of the new page is selected, as the MCP logs page does; a failed fetch rolls back.
	const navigationSeq = useRef(0);
	// The URL state as of the latest render, so a late page reply can check that the person has not
	// picked a row, closed the sheet or moved on since the navigation started.
	const latestUrlState = useRef(urlState);
	latestUrlState.current = urlState;
	const handleLogNavigate = useCallback(
		(direction: "prev" | "next") => {
			const replaceHistory = { history: "replace" as const };
			const currentLogId = selectedLogId || "";
			setError(null);
			const target = direction === "prev" ? selectedLogIndex - 1 : selectedLogIndex + 1;
			if (selectedLogIndex >= 0 && target >= 0 && target < logs.length) {
				setUrlState({ selected_log: logs[target].id }, replaceHistory);
				return;
			}
			const canTurn = direction === "prev" ? pagination.offset > 0 : pagination.offset + pagination.limit < totalItems;
			if (!canTurn) return;
			const newOffset = direction === "prev" ? Math.max(0, pagination.offset - pagination.limit) : pagination.offset + pagination.limit;
			setUrlState({ offset: newOffset, selected_log: "" }, replaceHistory);
			// Tagged so a reply that lands after the person closed the sheet, picked another row or
			// navigated again cannot reopen a row they did not choose.
			const seq = ++navigationSeq.current;
			triggerGetLogs({ filters, pagination: { ...pagination, offset: newOffset } }).then((result) => {
				if (seq !== navigationSeq.current) return;
				if (latestUrlState.current.selected_log !== "" || latestUrlState.current.offset !== newOffset) return;
				const pageLogs = result.data?.logs;
				if (pageLogs?.length) {
					setUrlState({ selected_log: direction === "prev" ? pageLogs[pageLogs.length - 1].id : pageLogs[0].id }, replaceHistory);
				} else if (result.error) {
					setUrlState({ offset: pagination.offset, selected_log: currentLogId }, replaceHistory);
					setError(getErrorMessage(result.error));
				}
			});
		},
		[selectedLogId, selectedLogIndex, logs, pagination, totalItems, filters, setUrlState, triggerGetLogs],
	);

	// Custom app icons from user-agent mappings, keyed by app name (as the LLM and MCP log tables).
	const customAppIcons = useMemo(() => {
		const icons: Record<string, string> = {};
		for (const mapping of userAgentMappingsData?.mappings ?? []) {
			if (mapping.app && mapping.logo && mapping.logo_mime) {
				icons[mapping.app] = `data:${mapping.logo_mime};base64,${mapping.logo}`;
			}
		}
		return icons;
	}, [userAgentMappingsData?.mappings]);
	const columns = useMemo(
		() => createRelayColumns(handleDelete, hasDeleteAccess, customAppIcons),
		[customAppIcons, handleDelete, hasDeleteAccess],
	);
	const columnIds = useMemo(
		() => columns.map((c) => ("id" in c && c.id ? c.id : "accessorKey" in c ? String(c.accessorKey) : "")).filter(Boolean),
		[columns],
	);
	const {
		entries: columnEntries,
		columnOrder,
		columnVisibility,
		columnPinning,
		toggleVisibility,
		togglePin,
		reorder,
		reset,
	} = useColumnConfig({
		columnIds,
		paramName: "relay_cols",
		storageKey: "bifrost.relay_logs.cols",
		// App is visible by default, as on the LLM logs page; Model stays hidden since most relays carry none.
		defaultHidden: ["model", "virtual_key", "user", "team", "customer", "business_unit", "project"],
		fixedColumns: hasDeleteAccess ? { right: ["actions"] } : undefined,
	});

	const displayError = error ?? (logsError ? getErrorMessage(logsError as Parameters<typeof getErrorMessage>[0]) : null);

	if (logsIsLoading) return <FullPageLoader />;
	if (logsData && !logsData.has_logs) return <RelayEmptyState error={displayError} />;

	return (
		<div className="dark:bg-card bg-white" data-testid="relay-logs-page">
			<div className="no-padding-parent no-border-parent bg-background flex h-[calc(var(--app-content-viewport)_-_var(--app-bottom-padding))] w-full gap-3">
				<RelayFilterSidebar filters={filters} onFiltersChange={setFilters} />
				<div className="bg-card flex min-w-0 flex-1 flex-col gap-2 overflow-hidden rounded-md border">
					<div className="@container/relay-logs-header p-4 pb-0">
						<RelayHeaderView
							filters={filters}
							onFiltersChange={setFilters}
							period={urlState.period}
							onPeriodChange={handlePeriodChange}
							polling={polling}
							onPollToggle={handlePollToggle}
							onRefresh={refreshAllData}
							loading={logsIsFetching}
							totalLogs={totalItems}
							columnEntries={columnEntries}
							columnLabels={RELAY_COLUMN_LABELS}
							onToggleColumnVisibility={toggleVisibility}
							onResetColumns={reset}
							currentPageLogs={logs}
							sortBy={pagination.sort_by}
							order={pagination.order}
						/>
					</div>
					<div className="shrink-0 px-4">
						{/* The stats and histogram resolve independently, so the strip is dimmed until
						    both agree on the current filters, as the LLM page does. */}
						<RelayMetricStrip stats={statsData} histogram={histogram} loading={statsIsFetching || histogramIsFetching} error={statsError} />
					</div>
					{/* The LLM page's Request Volume chart, fed the relay histogram (a superset of its buckets). */}
					<div className="shrink-0 px-4">
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
					{displayError && (
						<Alert variant="destructive" className="mx-4 shrink-0">
							<AlertCircle className="h-4 w-4" />
							<AlertDescription>{displayError}</AlertDescription>
						</Alert>
					)}
					<RelayLogsDataTable
						columns={columns}
						data={logs}
						totalItems={totalItems}
						loading={logsIsFetching}
						pagination={pagination}
						onPaginationChange={setPagination}
						onRowClick={(row, columnId) => {
							if (columnId === "actions") return;
							navigationSeq.current++;
							setUrlState({ selected_log: row.id }, { history: "replace" });
						}}
						onRefresh={refreshAllData}
						polling={polling}
						columnEntries={columnEntries}
						columnOrder={columnOrder}
						columnVisibility={columnVisibility}
						columnPinning={columnPinning}
						onToggleColumnVisibility={toggleVisibility}
						onTogglePin={togglePin}
						onReorderColumns={reorder}
					/>
				</div>
				<RelayLogDetailSheet
					log={selectedLog}
					logId={selectedLogId}
					open={selectedLogId !== null}
					onOpenChange={(open) => {
						if (open) return;
						navigationSeq.current++;
						setUrlState({ selected_log: "" }, { history: "replace" });
					}}
					handleDelete={hasDeleteAccess ? handleDelete : undefined}
					onNavigate={handleLogNavigate}
					hasPrev={selectedLogIndex > 0 || (selectedLogIndex !== -1 && pagination.offset > 0)}
					hasNext={selectedLogIndex !== -1 && (selectedLogIndex < logs.length - 1 || pagination.offset + pagination.limit < totalItems)}
				/>
			</div>
		</div>
	);
}