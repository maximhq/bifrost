import { MCPFilterSidebar } from "@/components/filters/mcpFilterSidebar";
import FullPageLoader from "@/components/fullPageLoader";
import { useColumnConfig } from "@/components/table";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Card, CardContent } from "@/components/ui/card";
import { getErrorMessage, useDeleteMCPLogsMutation, useGetMCPLogsQuery, useGetMCPLogsStatsQuery } from "@/lib/store";
import { useLazyGetMCPLogsQuery } from "@/lib/store/apis/mcpLogsApi";
import type { MCPToolLogEntry, MCPToolLogFilters, Pagination } from "@/lib/types/logs";
import { dateUtils } from "@/lib/types/logs";
import { COMPACT_NUMBER_FORMAT } from "@/lib/utils/numbers";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import NumberFlow from "@number-flow/react";
import { useLocation } from "@tanstack/react-router";
import { AlertCircle, CheckCircle, Clock, DollarSign, Hash } from "lucide-react";
import { parseAsArrayOf, parseAsBoolean, parseAsInteger, parseAsString, useQueryStates } from "nuqs";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { createMCPColumns } from "./views/columns";
import { MCPEmptyState } from "./views/emptyState";
import { McpHeaderView } from "./views/mcpHeaderView";
import { MCPLogDetailSheet } from "./views/mcpLogDetailsSheet";
import { MCPLogsDataTable } from "./views/mcpLogsTable";

export default function MCPLogsPage() {
	const [error, setError] = useState<string | null>(null);
	const [showEmptyState, setShowEmptyState] = useState(false);
	const hasCheckedEmptyState = useRef(false);
	const hasDeleteAccess = useRbac(RbacResource.Logs, RbacOperation.Delete);

	const [deleteLogs] = useDeleteMCPLogsMutation();
	// Lazy query kept only for handleLogNavigate (fetches adjacent pages on demand)
	const [triggerGetLogs] = useLazyGetMCPLogsQuery();

	// Track if user has manually modified the time range
	const userModifiedTimeRange = useRef<boolean>(false);

	const defaultTimeRange = useMemo(() => dateUtils.getDefaultTimeRange(), []);

	const { search } = useLocation();
	const hasExplicitTimeRange = (search as Record<string, unknown>)?.start_time && (search as Record<string, unknown>)?.end_time;

	// URL state management
	const [urlState, setUrlState] = useQueryStates(
		{
			tool_names: parseAsArrayOf(parseAsString).withDefault([]),
			server_labels: parseAsArrayOf(parseAsString).withDefault([]),
			status: parseAsArrayOf(parseAsString).withDefault([]),
			virtual_key_ids: parseAsArrayOf(parseAsString).withDefault([]),
			content_search: parseAsString.withDefault(""),
			start_time: parseAsInteger.withDefault(defaultTimeRange.startTime),
			end_time: parseAsInteger.withDefault(defaultTimeRange.endTime),
			limit: parseAsInteger.withDefault(50),
			offset: parseAsInteger.withDefault(0),
			sort_by: parseAsString.withDefault("timestamp"),
			order: parseAsString.withDefault("desc"),
			polling: parseAsBoolean.withDefault(true).withOptions({ clearOnDefault: false }),
			period: parseAsString.withDefault(hasExplicitTimeRange ? "" : "1h").withOptions({ clearOnDefault: false }),
			selected_log: parseAsString.withDefault(""),
		},
		{
			history: "push",
			shallow: false,
		},
	);

	const selectedLogId = urlState.selected_log || null;
	const polling = urlState.polling;


	// Convert URL state to filters and pagination for API calls.
	// When period is set, send it to the backend so the server computes the time window fresh
	// on every request. For custom absolute ranges (period === "") use the stored timestamps.
	const filters: MCPToolLogFilters = useMemo(
		() => ({
			tool_names: urlState.tool_names,
			server_labels: urlState.server_labels,
			status: urlState.status,
			virtual_key_ids: urlState.virtual_key_ids,
			content_search: urlState.content_search,
			...(urlState.period
				? { period: urlState.period }
				: {
					start_time: dateUtils.toISOString(urlState.start_time),
					end_time: dateUtils.toISOString(urlState.end_time),
				}),
		}),
		[
			urlState.tool_names,
			urlState.server_labels,
			urlState.status,
			urlState.virtual_key_ids,
			urlState.content_search,
			urlState.period,
			urlState.start_time,
			urlState.end_time,
		],
	);

	const pagination: Pagination = useMemo(
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
	} = useGetMCPLogsQuery(
		{ filters, pagination },
		{
			pollingInterval: showEmptyState || polling ? 10000 : 0,
			skipPollingIfUnfocused: true,
		},
	);

	const {
		data: statsData,
		isFetching: statsIsFetching,
		refetch: refetchStats,
	} = useGetMCPLogsStatsQuery(
		{ filters },
		{
			pollingInterval: polling ? 10000 : 0,
			skipPollingIfUnfocused: true,
		},
	);

	const refreshAllData = useCallback(() => {
		refetchLogs();
		refetchStats();
	}, [refetchLogs, refetchStats]);

	// Derive data directly from RTK
	const logs = logsData?.logs ?? [];
	const totalItems = logsData?.stats?.total_executions ?? 0;

	const selectedLog = useMemo(() => (selectedLogId ? (logs.find((l) => l.id === selectedLogId) ?? null) : null), [selectedLogId, logs]);

	// Set showEmptyState on first response; clear it as soon as logs appear.
	useEffect(() => {
		if (!logsData) return;
		if (!hasCheckedEmptyState.current) {
			setShowEmptyState(!logsData.has_logs);
			hasCheckedEmptyState.current = true;
		} else if (showEmptyState && logsData.has_logs) {
			setShowEmptyState(false);
		}
	}, [logsData, showEmptyState]);

	// Helper to update filters in URL
	const setFilters = useCallback(
		(newFilters: MCPToolLogFilters) => {
			const timeChanged = newFilters.start_time !== undefined || newFilters.end_time !== undefined;
			if (timeChanged) userModifiedTimeRange.current = true;

			setUrlState({
				...(timeChanged && { period: "" }),
				tool_names: newFilters.tool_names || [],
				server_labels: newFilters.server_labels || [],
				status: newFilters.status || [],
				virtual_key_ids: newFilters.virtual_key_ids || [],
				content_search: newFilters.content_search || "",
				start_time: newFilters.start_time ? dateUtils.toUnixTimestamp(new Date(newFilters.start_time)) : undefined,
				end_time: newFilters.end_time ? dateUtils.toUnixTimestamp(new Date(newFilters.end_time)) : undefined,
				offset: 0,
			});
		},
		[setUrlState],
	);

	// Helper to update pagination in URL
	const setPagination = useCallback(
		(newPagination: Pagination) => {
			setUrlState({
				limit: newPagination.limit,
				offset: newPagination.offset,
				sort_by: newPagination.sort_by,
				order: newPagination.order,
			});
		},
		[setUrlState],
	);

	const handleDelete = useCallback(
		async (log: MCPToolLogEntry) => {
			if (!hasDeleteAccess) throw new Error("No delete access");
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

	const handlePeriodChange = useCallback(
		(p?: string, from?: Date, to?: Date) => {
			if (p) {
				setUrlState({
					period: p,
					offset: 0,
					polling: true
				});
			} else if (from && to) {
				setUrlState({
					start_time: Math.floor(from.getTime() / 1000),
					end_time: Math.floor(to.getTime() / 1000),
					offset: 0,
					polling: false,
					period: ""
				});
			}
		},
		[setUrlState],
	);

	const handlePollToggle = useCallback(
		(enabled: boolean) => {
			setUrlState({ polling: enabled });
			if (enabled) refreshAllData();
		},
		[setUrlState, refreshAllData],
	);

	const statCards = useMemo(
		() => [
			{
				title: "Total Executions",
				value: <NumberFlow value={statsData?.total_executions ?? 0} format={COMPACT_NUMBER_FORMAT} />,
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
				title: "Avg Latency",
				value: (
					<NumberFlow value={statsData?.average_latency ?? 0} format={{ minimumFractionDigits: 2, maximumFractionDigits: 2 }} suffix="ms" />
				),
				icon: <Clock className="size-4" />,
			},
			{
				title: "Total Cost",
				value: (
					<NumberFlow
						value={statsData?.total_cost ?? 0}
						format={{
							...COMPACT_NUMBER_FORMAT,
							style: "currency",
							currency: "USD",
						}}
					/>
				),
				icon: <DollarSign className="size-4" />,
			},
		],
		[statsData],
	);

	const columns = useMemo(() => createMCPColumns(handleDelete, hasDeleteAccess), [handleDelete, hasDeleteAccess]);

	const columnIds = useMemo(
		() => columns.map((col) => ("id" in col && col.id ? col.id : "accessorKey" in col ? String(col.accessorKey) : "")).filter(Boolean),
		[columns],
	);

	const MCP_COLUMN_LABELS: Record<string, string> = useMemo(
		() => ({
			timestamp: "Time",
			tool_name: "Tool Name",
			server_label: "Server",
			latency: "Latency",
			cost: "Cost",
		}),
		[],
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
		paramName: "mcp_cols",
		fixedColumns: { left: [], right: [] },
	});

	const selectedLogIndex = useMemo(() => (selectedLogId ? logs.findIndex((l) => l.id === selectedLogId) : -1), [selectedLogId, logs]);

	const handleLogNavigate = useCallback(
		(direction: "prev" | "next") => {
			const replaceHistory = { history: "replace" as const };
			const currentLogId = selectedLogId || "";
			if (direction === "prev") {
				if (selectedLogIndex > 0) {
					setUrlState({ selected_log: logs[selectedLogIndex - 1].id }, replaceHistory);
				} else if (pagination.offset > 0) {
					const newOffset = Math.max(0, pagination.offset - pagination.limit);
					setUrlState({ offset: newOffset, selected_log: "" }, replaceHistory);
					triggerGetLogs({
						filters,
						pagination: { ...pagination, offset: newOffset },
					}).then((result) => {
						const pageLogs = result.data?.logs;
						if (pageLogs?.length) {
							setUrlState({ selected_log: pageLogs[pageLogs.length - 1].id }, replaceHistory);
						} else if (result.error) {
							setUrlState({ offset: pagination.offset, selected_log: currentLogId }, replaceHistory);
							setError(getErrorMessage(result.error));
						}
					});
				}
			} else {
				if (selectedLogIndex >= 0 && selectedLogIndex < logs.length - 1) {
					setUrlState({ selected_log: logs[selectedLogIndex + 1].id }, replaceHistory);
				} else if (pagination.offset + pagination.limit < totalItems) {
					const newOffset = pagination.offset + pagination.limit;
					setUrlState({ offset: newOffset, selected_log: "" }, replaceHistory);
					triggerGetLogs({
						filters,
						pagination: { ...pagination, offset: newOffset },
					}).then((result) => {
						const pageLogs = result.data?.logs;
						if (pageLogs?.length) {
							setUrlState({ selected_log: pageLogs[0].id }, replaceHistory);
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
			{logsIsLoading ? (
				<FullPageLoader />
			) : showEmptyState ? (
				<MCPEmptyState error={displayError} />
			) : (
				<div className="no-padding-parent no-border-parent bg-background flex h-[calc(100vh_-_16px)] w-full gap-3">
					{/* Sidebar Filters */}
					<MCPFilterSidebar filters={filters} onFiltersChange={setFilters} />

					{/* Main Content */}
					<div className="bg-card flex min-w-0 flex-1 flex-col gap-2 overflow-hidden rounded-l-md">
						<div className="p-4 pb-0">
							<McpHeaderView
								filters={filters}
								onFiltersChange={setFilters}
								period={urlState.period}
								onPeriodChange={handlePeriodChange}
								polling={polling}
								onPollToggle={handlePollToggle}
								onRefresh={refreshAllData}
								loading={logsIsFetching}
								columnEntries={columnEntries}
								columnLabels={MCP_COLUMN_LABELS}
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

							{displayError && (
								<Alert variant="destructive" className="shrink-0">
									<AlertCircle className="h-4 w-4" />
									<AlertDescription>{displayError}</AlertDescription>
								</Alert>
							)}
						</div>

						<MCPLogsDataTable
							columns={columns}
							data={logs}
							totalItems={totalItems}
							loading={logsIsFetching}
							pagination={pagination}
							onPaginationChange={setPagination}
							onRowClick={(row, columnId) => {
								if (columnId === "actions") return;
								setUrlState({ selected_log: row.id }, { history: "replace" });
							}}
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
					<MCPLogDetailSheet
						log={selectedLog}
						open={selectedLogId !== null}
						onOpenChange={(open) => !open && setUrlState({ selected_log: "" }, { history: "replace" })}
						handleDelete={handleDelete}
						onNavigate={handleLogNavigate}
						hasPrev={selectedLogIndex > 0 || (selectedLogIndex !== -1 && pagination.offset > 0)}
						hasNext={selectedLogIndex !== -1 && (selectedLogIndex < logs.length - 1 || pagination.offset + pagination.limit < totalItems)}
					/>
				</div>
			)}
		</div>
	);
}