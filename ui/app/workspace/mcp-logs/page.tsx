"use client";

import FullPageLoader from "@/components/fullPageLoader";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Card, CardContent } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { useWebSocket } from "@/hooks/useWebSocket";
import {
	getErrorMessage,
	useDeleteMCPLogsMutation,
	useLazyGetMCPLogsQuery,
	useLazyGetMCPLogsStatsQuery,
} from "@/lib/store";
import type {
	MCPToolLogEntry,
	MCPToolLogFilters,
	MCPToolLogStats,
	Pagination,
} from "@/lib/types/logs";
import { dateUtils } from "@/lib/types/logs";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { AlertCircle, CheckCircle, Clock, DollarSign, Hash } from "lucide-react";
import {
	parseAsArrayOf,
	parseAsBoolean,
	parseAsInteger,
	parseAsString,
	useQueryStates,
} from "nuqs";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { MCPLogsDataTable } from "./views/mcpLogsTable";
import { createMCPColumns } from "./views/columns";
import { MCPLogDetailSheet } from "./views/mcpLogDetailsSheet";
import { MCPEmptyState } from "./views/emptyState";

// Calculate default timestamps
const DEFAULT_END_TIME = Math.floor(Date.now() / 1000);
const DEFAULT_START_TIME = (() => {
	const date = new Date();
	date.setHours(date.getHours() - 24);
	return Math.floor(date.getTime() / 1000);
})();

export default function MCPLogsPage() {
	const [logs, setLogs] = useState<MCPToolLogEntry[]>([]);
	const [totalItems, setTotalItems] = useState(0);
	const [stats, setStats] = useState<MCPToolLogStats | null>(null);
	const [initialLoading, setInitialLoading] = useState(true);
	const [fetchingLogs, setFetchingLogs] = useState(false);
	const [fetchingStats, setFetchingStats] = useState(false);
	const [error, setError] = useState<string | null>(null);
	const [showEmptyState, setShowEmptyState] = useState(false);
	const [selectedLog, setSelectedLog] = useState<MCPToolLogEntry | null>(null);

	const hasDeleteAccess = useRbac(RbacResource.Logs, RbacOperation.Delete);

	const [triggerGetLogs] = useLazyGetMCPLogsQuery();
	const [triggerGetStats] = useLazyGetMCPLogsStatsQuery();
	const [deleteLogs] = useDeleteMCPLogsMutation();

	// URL state management
	const [urlState, setUrlState] = useQueryStates(
		{
			tool_names: parseAsArrayOf(parseAsString).withDefault([]),
			server_labels: parseAsArrayOf(parseAsString).withDefault([]),
			status: parseAsArrayOf(parseAsString).withDefault([]),
			content_search: parseAsString.withDefault(""),
			start_time: parseAsInteger.withDefault(DEFAULT_START_TIME),
			end_time: parseAsInteger.withDefault(DEFAULT_END_TIME),
			limit: parseAsInteger.withDefault(50),
			offset: parseAsInteger.withDefault(0),
			sort_by: parseAsString.withDefault("timestamp"),
			order: parseAsString.withDefault("desc"),
			live_enabled: parseAsBoolean.withDefault(true),
		},
		{
			history: "push",
			shallow: false,
		}
	);

	// Convert URL state to filters and pagination
	const filters: MCPToolLogFilters = useMemo(
		() => ({
			tool_names: urlState.tool_names,
			server_labels: urlState.server_labels,
			status: urlState.status,
			content_search: urlState.content_search,
			start_time: dateUtils.toISOString(urlState.start_time),
			end_time: dateUtils.toISOString(urlState.end_time),
		}),
		[urlState]
	);

	const pagination: Pagination = useMemo(
		() => ({
			limit: urlState.limit,
			offset: urlState.offset,
			sort_by: urlState.sort_by as "timestamp" | "latency",
			order: urlState.order as "asc" | "desc",
		}),
		[urlState]
	);

	const liveEnabled = urlState.live_enabled;

	// Helper to update filters in URL
	const setFilters = useCallback(
		(newFilters: MCPToolLogFilters) => {
			setUrlState({
				tool_names: newFilters.tool_names || [],
				server_labels: newFilters.server_labels || [],
				status: newFilters.status || [],
				content_search: newFilters.content_search || "",
				start_time: newFilters.start_time ? dateUtils.toUnixTimestamp(new Date(newFilters.start_time)) : undefined,
				end_time: newFilters.end_time ? dateUtils.toUnixTimestamp(new Date(newFilters.end_time)) : undefined,
				offset: 0,
			});
		},
		[setUrlState]
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
		[setUrlState]
	);

	const handleDelete = useCallback(
		async (log: MCPToolLogEntry) => {
			try {
				await deleteLogs({ ids: [log.id] }).unwrap();
				setLogs((prevLogs) => prevLogs.filter((l) => l.id !== log.id));
				setTotalItems((prev) => prev - 1);
			} catch (err) {
				const errorMessage = getErrorMessage(err);
				setError(errorMessage);
				throw new Error(errorMessage);
			}
		},
		[deleteLogs]
	);

	// Ref to track latest state for WebSocket callbacks
	const latest = useRef({ logs, filters, pagination, showEmptyState, liveEnabled });
	useEffect(() => {
		latest.current = { logs, filters, pagination, showEmptyState, liveEnabled };
	}, [logs, filters, pagination, showEmptyState, liveEnabled]);

	// Helper to check if a log matches current filters
	const matchesFilters = (log: MCPToolLogEntry, filters: MCPToolLogFilters, applyTimeFilters = true): boolean => {
		if (filters.tool_names?.length && !filters.tool_names.includes(log.tool_name)) {
			return false;
		}
		if (filters.server_labels?.length && (!log.server_label || !filters.server_labels.includes(log.server_label))) {
			return false;
		}
		if (filters.status?.length && !filters.status.includes(log.status)) {
			return false;
		}
		if (filters.start_time && new Date(log.timestamp) < new Date(filters.start_time)) {
			return false;
		}
		if (applyTimeFilters && filters.end_time && new Date(log.timestamp) > new Date(filters.end_time)) {
			return false;
		}
		return true;
	};

	// Handle WebSocket log messages
	const handleMCPLogMessage = useCallback((log: MCPToolLogEntry, operation: "create" | "update") => {
		const { logs, filters, pagination, showEmptyState, liveEnabled } = latest.current;

		// Exit empty state if we now have logs
		if (showEmptyState) {
			setShowEmptyState(false);
		}

		if (operation === "create") {
			// Only prepend new log if on first page and sorted by timestamp desc
			if (pagination.offset === 0 && pagination.sort_by === "timestamp" && pagination.order === "desc") {
				if (!matchesFilters(log, filters, !liveEnabled)) {
					return;
				}

				setLogs((prevLogs: MCPToolLogEntry[]) => {
					// Prevent duplicates
					if (prevLogs.some((existingLog) => existingLog.id === log.id)) {
						return prevLogs;
					}

					const updatedLogs = [log, ...prevLogs];
					if (updatedLogs.length > pagination.limit) {
						updatedLogs.pop();
					}
					return updatedLogs;
				});

				// Update selected log if it matches
				setSelectedLog((prevSelectedLog) => {
					if (prevSelectedLog && prevSelectedLog.id === log.id) {
						return log;
					}
					return prevSelectedLog;
				});

				setTotalItems((prev: number) => prev + 1);
			}
		} else if (operation === "update") {
			const logExists = logs.some((existingLog) => existingLog.id === log.id);

			if (!logExists) {
				// Fallback: if log doesn't exist, treat as create
				if (pagination.offset === 0 && pagination.sort_by === "timestamp" && pagination.order === "desc") {
					if (matchesFilters(log, filters, !liveEnabled)) {
						setLogs((prevLogs: MCPToolLogEntry[]) => {
							if (prevLogs.some((existingLog) => existingLog.id === log.id)) {
								return prevLogs.map((existingLog) => (existingLog.id === log.id ? log : existingLog));
							}

							const updatedLogs = [log, ...prevLogs];
							if (updatedLogs.length > pagination.limit) {
								updatedLogs.pop();
							}
							return updatedLogs;
						});
					}
				}
			} else {
				// Update existing log
				setLogs((prevLogs: MCPToolLogEntry[]) => {
					return prevLogs.map((existingLog) => (existingLog.id === log.id ? log : existingLog));
				});

				// Update selected log if it matches
				setSelectedLog((prevSelectedLog) => {
					if (prevSelectedLog && prevSelectedLog.id === log.id) {
						return log;
					}
					return prevSelectedLog;
				});

				// Update stats for completed requests
				if (log.status === "success" || log.status === "error") {
					setStats((prevStats) => {
						if (!prevStats) return prevStats;

						const newStats = { ...prevStats };
						const completed_executions = prevStats.total_executions + 1;
						newStats.total_executions = completed_executions;

						// Update success rate
						const successCount = (prevStats.success_rate / 100) * prevStats.total_executions;
						const newSuccessCount = log.status === "success" ? successCount + 1 : successCount;
						newStats.success_rate = (newSuccessCount / completed_executions) * 100;

						// Update average latency
						if (log.latency) {
							const totalLatency = prevStats.average_latency * prevStats.total_executions;
							newStats.average_latency = (totalLatency + log.latency) / completed_executions;
						}

						// Update total cost
						if (log.cost) {
							newStats.total_cost += log.cost;
						}

						return newStats;
					});
				}
			}
		}
	}, []);

	const { isConnected: isSocketConnected, subscribe } = useWebSocket();

	// Subscribe to MCP log messages - only when live updates are enabled
	useEffect(() => {
		if (!liveEnabled) {
			return;
		}

		const unsubscribe = subscribe("mcp_log", (data) => {
			const { payload, operation } = data;
			handleMCPLogMessage(payload, operation);
		});

		return unsubscribe;
	}, [handleMCPLogMessage, subscribe, liveEnabled]);

	// Fetch logs
	const fetchLogs = useCallback(async () => {
		setFetchingLogs(true);
		setError(null);
		try {
			const result = await triggerGetLogs({ filters, pagination }).unwrap();
			setLogs(result.logs || []);
			setTotalItems(result.stats?.total_executions || 0);

			if (initialLoading) {
				setShowEmptyState(result.has_logs === false);
			}
		} catch (err) {
			setError(getErrorMessage(err));
			setLogs([]);
			setTotalItems(0);
			setShowEmptyState(true);
		} finally {
			setFetchingLogs(false);
		}
	}, [filters, pagination, triggerGetLogs, initialLoading]);

	const fetchStats = useCallback(async () => {
		setFetchingStats(true);
		try {
			const result = await triggerGetStats({ filters }).unwrap();
			setStats(result);
		} catch (err) {
			console.error("Failed to fetch stats:", err);
		} finally {
			setFetchingStats(false);
		}
	}, [filters, triggerGetStats]);

	// Helper to toggle live updates
	const handleLiveToggle = useCallback(
		(enabled: boolean) => {
			setUrlState({ live_enabled: enabled });
			// When re-enabling, refetch logs to get latest data
			if (enabled) {
				fetchLogs();
			}
		},
		[setUrlState, fetchLogs]
	);

	// Initial load
	useEffect(() => {
		const initialLoad = async () => {
			await fetchLogs();
			fetchStats();
			setInitialLoading(false);
		};
		initialLoad();
		// eslint-disable-next-line react-hooks/exhaustive-deps
	}, []);

	// Fetch logs when filters or pagination change
	useEffect(() => {
		if (!initialLoading) {
			fetchLogs();
		}
		// eslint-disable-next-line react-hooks/exhaustive-deps
	}, [filters, pagination, initialLoading]);

	// Fetch stats when filters change
	useEffect(() => {
		if (!initialLoading) {
			fetchStats();
		}
		// eslint-disable-next-line react-hooks/exhaustive-deps
	}, [filters, initialLoading]);

	const statCards = useMemo(
		() => [
			{
				title: "Total Executions",
				value: fetchingStats ? <Skeleton className="h-8 w-20" /> : stats?.total_executions.toLocaleString() || "-",
				icon: <Hash className="size-4" />,
			},
			{
				title: "Success Rate",
				value: fetchingStats ? <Skeleton className="h-8 w-16" /> : stats ? `${stats.success_rate.toFixed(2)}%` : "-",
				icon: <CheckCircle className="size-4" />,
			},
			{
				title: "Avg Latency",
				value: fetchingStats ? <Skeleton className="h-8 w-20" /> : stats ? `${stats.average_latency.toFixed(2)}ms` : "-",
				icon: <Clock className="size-4" />,
			},
			{
				title: "Total Cost",
				value: fetchingStats ? <Skeleton className="h-8 w-20" /> : stats ? `$${(stats.total_cost ?? 0).toFixed(4)}` : "-",
				icon: <DollarSign className="size-4" />,
			},
		],
		[stats, fetchingStats]
	);

	const columns = useMemo(() => createMCPColumns(handleDelete, hasDeleteAccess), [handleDelete, hasDeleteAccess]);

	return (
		<div className="dark:bg-card bg-white">
			{initialLoading ? (
				<FullPageLoader />
			) : showEmptyState ? (
				<MCPEmptyState error={error} />
			) : (
				<div className="mx-auto max-w-7xl space-y-6">
					<div className="space-y-6">
						{/* Quick Stats */}
						<div className="grid grid-cols-1 gap-4 md:grid-cols-4">
							{statCards.map((card) => (
								<Card key={card.title} className="py-4 shadow-none">
									<CardContent className="flex items-center justify-between px-4">
										<div>
											<div className="text-muted-foreground text-xs">{card.title}</div>
											<div className="font-mono text-2xl font-medium">{card.value}</div>
										</div>
									</CardContent>
								</Card>
							))}
						</div>

						{/* Error Alert */}
						{error && (
							<Alert variant="destructive">
								<AlertCircle className="h-4 w-4" />
								<AlertDescription>{error}</AlertDescription>
							</Alert>
						)}

						<MCPLogsDataTable
							columns={columns}
							data={logs}
							totalItems={totalItems}
							loading={fetchingLogs}
							filters={filters}
							pagination={pagination}
							onFiltersChange={setFilters}
							onPaginationChange={setPagination}
							onRowClick={(row, columnId) => {
								if (columnId === "actions") return;
								setSelectedLog(row);
							}}
							isSocketConnected={isSocketConnected}
							liveEnabled={liveEnabled}
							onLiveToggle={handleLiveToggle}
							fetchLogs={fetchLogs}
							fetchStats={fetchStats}
						/>
					</div>

					{/* Log Detail Sheet */}
					<MCPLogDetailSheet
						log={selectedLog}
						open={selectedLog !== null}
						onOpenChange={(open) => !open && setSelectedLog(null)}
						handleDelete={handleDelete}
					/>
				</div>
			)}
		</div>
	);
}
