"use client";

import FullPageLoader from "@/components/fullPageLoader";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Card, CardContent } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { useWebSocket } from "@/hooks/useWebSocket";
import { getErrorMessage, useDeleteMCPLogsMutation, useLazyGetMCPLogsQuery, useLazyGetMCPLogsStatsQuery } from "@/lib/store";
import type { MCPToolLogEntry, MCPToolLogFilters, MCPToolLogStats, Pagination } from "@/lib/types/logs";
import { dateUtils } from "@/lib/types/logs";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { AlertCircle, CheckCircle, Clock, DollarSign, Hash } from "lucide-react";
import { parseAsArrayOf, parseAsBoolean, parseAsInteger, parseAsString, useQueryStates } from "nuqs";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { createMCPColumns } from "./views/columns";
import { MCPEmptyState } from "./views/emptyState";
import { MCPLogDetailSheet } from "./views/mcpLogDetailsSheet";
import { MCPLogsDataTable } from "./views/mcpLogsTable";

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

	// Track if user has manually modified the time range
	const userModifiedTimeRange = useRef<boolean>(false);

	// Capture initial defaults on mount to detect shared URLs with custom time ranges
	const initialDefaults = useRef(dateUtils.getDefaultTimeRange());

	// Memoize default time range to prevent recalculation on every render
	// This is crucial to avoid triggering re-fetches when the sheet opens/closes
	const defaultTimeRange = useMemo(() => dateUtils.getDefaultTimeRange(), []);

	// Get fresh default time range for refresh logic
	const getDefaultTimeRange = () => dateUtils.getDefaultTimeRange();

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
			live_enabled: parseAsBoolean.withDefault(true),
		},
		{
			history: "push",
			shallow: false,
		},
	);

	// Refresh time range defaults on page focus/visibility
	useEffect(() => {
		const refreshDefaultsIfStale = () => {
			// Skip refresh if user has manually modified the time range
			if (userModifiedTimeRange.current) {
				return;
			}

			// Check if current time range matches the initial defaults (within tolerance)
			const startTimeDiff = Math.abs(urlState.start_time - initialDefaults.current.startTime);
			const endTimeDiff = Math.abs(urlState.end_time - initialDefaults.current.endTime);
			const tolerance = 5; // 5 seconds tolerance for slight timing differences

			// Only refresh if current values match the initial defaults
			// This preserves shared URLs with custom time ranges
			if (startTimeDiff <= tolerance && endTimeDiff <= tolerance) {
				const defaults = getDefaultTimeRange();
				const currentEndDiff = Math.abs(urlState.end_time - defaults.endTime);
				// If end time is more than 5 minutes old, refresh both
				if (currentEndDiff > 300) {
					setUrlState({
						start_time: defaults.startTime,
						end_time: defaults.endTime,
					});
					// Update baseline so subsequent focus events compare against refreshed defaults
					initialDefaults.current.startTime = defaults.startTime;
					initialDefaults.current.endTime = defaults.endTime;
				}
			}
		};

		const handleVisibilityChange = () => {
			if (!document.hidden) {
				refreshDefaultsIfStale();
			}
		};

		const handleFocus = () => {
			refreshDefaultsIfStale();
		};

		document.addEventListener("visibilitychange", handleVisibilityChange);
		window.addEventListener("focus", handleFocus);
		return () => {
			document.removeEventListener("visibilitychange", handleVisibilityChange);
			window.removeEventListener("focus", handleFocus);
		};
	}, [urlState.start_time, urlState.end_time, setUrlState]);

	// Convert URL state to filters and pagination
	const filters: MCPToolLogFilters = useMemo(
		() => ({
			tool_names: urlState.tool_names,
			server_labels: urlState.server_labels,
			status: urlState.status,
			virtual_key_ids: urlState.virtual_key_ids,
			content_search: urlState.content_search,
			start_time: dateUtils.toISOString(urlState.start_time),
			end_time: dateUtils.toISOString(urlState.end_time),
		}),
		[urlState],
	);

	const pagination: Pagination = useMemo(
		() => ({
			limit: urlState.limit,
			offset: urlState.offset,
			sort_by: urlState.sort_by as "timestamp" | "latency",
			order: urlState.order as "asc" | "desc",
		}),
		[urlState],
	);

	const liveEnabled = urlState.live_enabled;

	// Helper to update filters in URL
	const setFilters = useCallback(
		(newFilters: MCPToolLogFilters) => {
			// Mark time range as user-modified if start_time or end_time is being set
			if (newFilters.start_time !== undefined || newFilters.end_time !== undefined) {
				userModifiedTimeRange.current = true;
			}

			setUrlState({
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
			// Guard against unauthorized delete attempts
			if (!hasDeleteAccess) {
				throw new Error("No delete access");
			}

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
		[deleteLogs, hasDeleteAccess],
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
		if (filters.virtual_key_ids?.length && (!log.virtual_key_id || !filters.virtual_key_ids.includes(log.virtual_key_id))) {
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
						newStats.total_cost = (Number(newStats.total_cost) || 0) + Number(log.cost ?? 0);

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
		[setUrlState, fetchLogs],
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
		[stats, fetchingStats],
	);

	const columns = useMemo(() => createMCPColumns(handleDelete, hasDeleteAccess), [handleDelete, hasDeleteAccess]);

	return (
		<div className="dark:bg-card bg-white">
			{initialLoading ? (
				<FullPageLoader />
			) : showEmptyState ? (
				<MCPEmptyState
					error={error}
					statusIndicator={
						isSocketConnected && (
							<div className="inline-flex items-center rounded-full border border-green-200 bg-green-50 px-3 py-1 text-xs font-medium text-green-700 sm:px-4 sm:text-sm">
								<span className="relative mr-2 flex h-2 w-2 sm:mr-3">
									<span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-green-500 opacity-75"></span>
									<span className="relative inline-flex h-2 w-2 rounded-full bg-green-600"></span>
								</span>
								<span>Listening for tool executions...</span>
							</div>
						)
					}
				/>
			) : (
				<div className="mx-auto w-full space-y-6">
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
