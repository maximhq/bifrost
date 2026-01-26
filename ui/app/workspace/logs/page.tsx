"use client";

import { createColumns } from "@/app/workspace/logs/views/columns";
import { EmptyState } from "@/app/workspace/logs/views/emptyState";
import { LogDetailSheet } from "@/app/workspace/logs/views/logDetailsSheet";
import { LogsDataTable } from "@/app/workspace/logs/views/logsTable";
import { LogsVolumeChart } from "@/app/workspace/logs/views/logsVolumeChart";
import FullPageLoader from "@/components/fullPageLoader";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Card, CardContent } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { useWebSocket } from "@/hooks/useWebSocket";
import {
	getErrorMessage,
	useDeleteLogsMutation,
	useLazyGetLogsHistogramQuery,
	useLazyGetLogsQuery,
	useLazyGetLogsStatsQuery,
} from "@/lib/store";
import type {
	ChatMessage,
	ChatMessageContent,
	ContentBlock,
	LogEntry,
	LogFilters,
	LogsHistogramResponse,
	LogStats,
	Pagination,
} from "@/lib/types/logs";
import { dateUtils } from "@/lib/types/logs";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { AlertCircle, BarChart, CheckCircle, Clock, DollarSign, Hash } from "lucide-react";
import { parseAsArrayOf, parseAsBoolean, parseAsInteger, parseAsString, useQueryStates } from "nuqs";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";

// Calculate default timestamps once at module level to prevent constant recalculation
const DEFAULT_END_TIME = Math.floor(Date.now() / 1000);
const DEFAULT_START_TIME = (() => {
	const date = new Date();
	date.setHours(date.getHours() - 24);
	return Math.floor(date.getTime() / 1000);
})();

export default function LogsPage() {
	const [logs, setLogs] = useState<LogEntry[]>([]);
	const [totalItems, setTotalItems] = useState(0); // changes with filters
	const [stats, setStats] = useState<LogStats | null>(null);
	const [histogram, setHistogram] = useState<LogsHistogramResponse | null>(null);
	const [initialLoading, setInitialLoading] = useState(true); // on initial load
	const [fetchingLogs, setFetchingLogs] = useState(false); // on pagination/filters change
	const [fetchingStats, setFetchingStats] = useState(false); // on stats fetch
	const [fetchingHistogram, setFetchingHistogram] = useState(false); // on histogram fetch
	const [error, setError] = useState<string | null>(null);
	const [showEmptyState, setShowEmptyState] = useState(false);

	const hasDeleteAccess = useRbac(RbacResource.Logs, RbacOperation.Delete);

	// RTK Query lazy hooks for manual triggering
	const [triggerGetLogs] = useLazyGetLogsQuery();
	const [triggerGetStats] = useLazyGetLogsStatsQuery();
	const [triggerGetHistogram] = useLazyGetLogsHistogramQuery();
	const [deleteLogs] = useDeleteLogsMutation();

	const [selectedLog, setSelectedLog] = useState<LogEntry | null>(null);
	const [isChartOpen, setIsChartOpen] = useState(true);

	// Debouncing for streaming updates (client-side)
	const streamingUpdateTimeouts = useRef<Map<string, ReturnType<typeof setTimeout>>>(new Map());

	// URL state management with nuqs - all filters and pagination in URL
	const [urlState, setUrlState] = useQueryStates(
		{
			providers: parseAsArrayOf(parseAsString).withDefault([]),
			models: parseAsArrayOf(parseAsString).withDefault([]),
			status: parseAsArrayOf(parseAsString).withDefault([]),
			objects: parseAsArrayOf(parseAsString).withDefault([]),
			selected_key_ids: parseAsArrayOf(parseAsString).withDefault([]),
			virtual_key_ids: parseAsArrayOf(parseAsString).withDefault([]),
			content_search: parseAsString.withDefault(""),
			start_time: parseAsInteger.withDefault(DEFAULT_START_TIME),
			end_time: parseAsInteger.withDefault(DEFAULT_END_TIME),
			limit: parseAsInteger.withDefault(25), // Default fallback, actual value calculated based on table height
			offset: parseAsInteger.withDefault(0),
			sort_by: parseAsString.withDefault("timestamp"),
			order: parseAsString.withDefault("desc"),
			live_enabled: parseAsBoolean.withDefault(true),
			missing_cost_only: parseAsBoolean.withDefault(false),
		},
		{
			history: "push",
			shallow: false,
		},
	);

	// Convert URL state to filters and pagination for API calls
	const filters: LogFilters = useMemo(
		() => ({
			providers: urlState.providers,
			models: urlState.models,
			status: urlState.status,
			objects: urlState.objects,
			selected_key_ids: urlState.selected_key_ids,
			virtual_key_ids: urlState.virtual_key_ids,
			content_search: urlState.content_search,
			start_time: dateUtils.toISOString(urlState.start_time),
			end_time: dateUtils.toISOString(urlState.end_time),
			missing_cost_only: urlState.missing_cost_only,
		}),
		[urlState],
	);

	const pagination: Pagination = useMemo(
		() => ({
			limit: urlState.limit,
			offset: urlState.offset,
			sort_by: urlState.sort_by as "timestamp" | "latency" | "tokens" | "cost",
			order: urlState.order as "asc" | "desc",
		}),
		[urlState],
	);

	const liveEnabled = urlState.live_enabled;

	// Helper to update filters in URL
	const setFilters = useCallback(
		(newFilters: LogFilters) => {
			setUrlState({
				providers: newFilters.providers || [],
				models: newFilters.models || [],
				status: newFilters.status || [],
				objects: newFilters.objects || [],
				selected_key_ids: newFilters.selected_key_ids || [],
				virtual_key_ids: newFilters.virtual_key_ids || [],
				content_search: newFilters.content_search || "",
				start_time: newFilters.start_time ? dateUtils.toUnixTimestamp(new Date(newFilters.start_time)) : undefined,
				end_time: newFilters.end_time ? dateUtils.toUnixTimestamp(new Date(newFilters.end_time)) : undefined,
				missing_cost_only: newFilters.missing_cost_only ?? filters.missing_cost_only ?? false,
				offset: 0,
			});
		},
		[setUrlState, filters],
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

	// Handler for time range changes from the volume chart
	const handleTimeRangeChange = useCallback(
		(startTime: number, endTime: number) => {
			setUrlState({
				start_time: startTime,
				end_time: endTime,
				offset: 0,
			});
		},
		[setUrlState],
	);

	// Handler for resetting zoom to default 24h view
	const handleResetZoom = useCallback(() => {
		const now = Math.floor(Date.now() / 1000);
		const twentyFourHoursAgo = now - 24 * 60 * 60;
		setUrlState({
			start_time: twentyFourHoursAgo,
			end_time: now,
			offset: 0,
		});
	}, [setUrlState]);

	// Check if user has zoomed (time range is different from default 24h)
	const isZoomed = useMemo(() => {
		const currentRange = urlState.end_time - urlState.start_time;
		const defaultRange = 24 * 60 * 60; // 24 hours in seconds
		// Consider zoomed if range is less than 90% of default (to account for minor differences)
		return currentRange < defaultRange * 0.9;
	}, [urlState.start_time, urlState.end_time]);

	const latest = useRef({ logs, filters, pagination, showEmptyState, liveEnabled });
	useEffect(() => {
		latest.current = { logs, filters, pagination, showEmptyState, liveEnabled };
	}, [logs, filters, pagination, showEmptyState, liveEnabled]);

	const handleDelete = useCallback(
		async (log: LogEntry) => {
			try {
				await deleteLogs({ ids: [log.id] }).unwrap();
				setLogs((prevLogs) => prevLogs.filter((l) => l.id !== log.id));
				setTotalItems((prev) => prev - 1);
			} catch (error) {
				setError(getErrorMessage(error));
			}
		},
		[deleteLogs],
	);

	const handleLogMessage = useCallback((log: LogEntry, operation: "create" | "update") => {
		const { logs, filters, pagination, showEmptyState, liveEnabled } = latest.current;
		// If we were in empty state, exit it since we now have logs
		if (showEmptyState) {
			setShowEmptyState(false);
		}

		if (operation === "create") {
			// Handle new log creation
			// Only prepend the new log if we're on the first page and sorted by timestamp desc
			if (pagination.offset === 0 && pagination.sort_by === "timestamp" && pagination.order === "desc") {
				// Check if the log matches current filters
				if (!matchesFilters(log, filters, !liveEnabled)) {
					return;
				}

				setLogs((prevLogs: LogEntry[]) => {
					// Check if log already exists (prevent duplicates)
					if (prevLogs.some((existingLog) => existingLog.id === log.id)) {
						return prevLogs;
					}

					// Remove the last log if we're at the page limit
					const updatedLogs = [log, ...prevLogs];
					if (updatedLogs.length > pagination.limit) {
						updatedLogs.pop();
					}
					return updatedLogs;
				});

				// Update selectedLog if it matches (for detail sheet real-time updates)
				setSelectedLog((prevSelectedLog) => {
					if (prevSelectedLog && prevSelectedLog.id === log.id) {
						return log;
					}
					return prevSelectedLog;
				});

				setTotalItems((prev: number) => prev + 1);
			}
		} else if (operation === "update") {
			// Handle log updates with debouncing for streaming

			// Check if the log exists in our current list
			const logExists = logs.some((existingLog) => existingLog.id === log.id);

			if (!logExists) {
				// Fallback: if log doesn't exist, treat as create (e.g., user was on different page when created)
				if (pagination.offset === 0 && pagination.sort_by === "timestamp" && pagination.order === "desc") {
					// Check if the log matches current filters
					if (matchesFilters(log, filters, !liveEnabled)) {
						setLogs((prevLogs: LogEntry[]) => {
							// Double-check it doesn't exist (race condition protection)
							if (prevLogs.some((existingLog) => existingLog.id === log.id)) {
								return prevLogs.map((existingLog) => (existingLog.id === log.id ? log : existingLog));
							}

							// Add as new log
							const updatedLogs = [log, ...prevLogs];
							if (updatedLogs.length > pagination.limit) {
								updatedLogs.pop();
							}
							return updatedLogs;
						});
					}
				}
			} else {
				// Normal update flow for existing logs
				if (log.stream) {
					// For streaming logs, debounce updates to avoid UI thrashing
					const existingTimeout = streamingUpdateTimeouts.current.get(log.id);
					if (existingTimeout) {
						clearTimeout(existingTimeout);
					}

					const timeout = setTimeout(() => {
						updateExistingLog(log);
						streamingUpdateTimeouts.current.delete(log.id);
					}, 100); // 100ms debounce for streaming updates

					streamingUpdateTimeouts.current.set(log.id, timeout);
				} else {
					// For non-streaming updates, update immediately
					updateExistingLog(log);
				}

				// Update stats for completed requests
				if (log.status == "success" || log.status == "error") {
					setStats((prevStats) => {
						if (!prevStats) return prevStats;

						const newStats = { ...prevStats };
						newStats.total_requests += 1;

						// Update success rate
						const successCount = (prevStats.success_rate / 100) * prevStats.total_requests;
						const newSuccessCount = log.status === "success" ? successCount + 1 : successCount;
						newStats.success_rate = (newSuccessCount / newStats.total_requests) * 100;

						// Update average latency
						if (log.latency) {
							const totalLatency = prevStats.average_latency * prevStats.total_requests;
							newStats.average_latency = (totalLatency + log.latency) / newStats.total_requests;
						}

						// Update total tokens
						if (log.token_usage) {
							newStats.total_tokens += log.token_usage.total_tokens;
						}

						// Update total cost
						if (log.cost) {
							newStats.total_cost += log.cost;
						}

						return newStats;
					});

					// Update histogram for completed requests
					setHistogram((prevHistogram) => {
						if (
							!prevHistogram ||
							typeof prevHistogram.bucket_size_seconds !== 'number' ||
							prevHistogram.bucket_size_seconds <= 0
						) {
							return prevHistogram
						}

						const logTime = new Date(log.timestamp).getTime();
						const bucketSizeMs = prevHistogram.bucket_size_seconds * 1000;
						const bucketTime = Math.floor(logTime / bucketSizeMs) * bucketSizeMs;

						const updatedBuckets = [...prevHistogram.buckets];
						const bucketIndex = updatedBuckets.findIndex((b) => {
							const bTime = new Date(b.timestamp).getTime();
							return Math.floor(bTime / bucketSizeMs) * bucketSizeMs === bucketTime;
						});

						if (bucketIndex >= 0) {
							// Update existing bucket
							updatedBuckets[bucketIndex] = {
								...updatedBuckets[bucketIndex],
								count: updatedBuckets[bucketIndex].count + 1,
								success: updatedBuckets[bucketIndex].success + (log.status === "success" ? 1 : 0),
								error: updatedBuckets[bucketIndex].error + (log.status === "error" ? 1 : 0),
							};
						} else {
							// Create new bucket for this timestamp
							const newBucket = {
								timestamp: new Date(bucketTime).toISOString(),
								count: 1,
								success: log.status === "success" ? 1 : 0,
								error: log.status === "error" ? 1 : 0,
							};
							// Insert in sorted order
							const insertIndex = updatedBuckets.findIndex((b) => new Date(b.timestamp).getTime() > bucketTime);
							if (insertIndex === -1) {
								updatedBuckets.push(newBucket);
							} else {
								updatedBuckets.splice(insertIndex, 0, newBucket);
							}
						}

						return { ...prevHistogram, buckets: updatedBuckets };
					});
				}
			}
		}
	}, []);

	const updateExistingLog = useCallback((updatedLog: LogEntry) => {
		setLogs((prevLogs: LogEntry[]) => {
			return prevLogs.map((existingLog) => (existingLog.id === updatedLog.id ? updatedLog : existingLog));
		});

		// Update selectedLog if it matches the updated log (for real-time detail sheet updates)
		setSelectedLog((prevSelectedLog) => {
			if (prevSelectedLog && prevSelectedLog.id === updatedLog.id) {
				return updatedLog;
			}
			return prevSelectedLog;
		});
	}, []);

	const { isConnected: isSocketConnected, subscribe } = useWebSocket();

	// Subscribe to log messages - only when live updates are enabled
	useEffect(() => {
		if (!liveEnabled) {
			return;
		}

		const unsubscribe = subscribe("log", (data) => {
			const { payload, operation } = data;
			handleLogMessage(payload, operation);
		});

		return unsubscribe;
	}, [handleLogMessage, subscribe, liveEnabled]);

	// Cleanup timeouts on unmount
	useEffect(() => {
		return () => {
			streamingUpdateTimeouts.current.forEach((timeout) => clearTimeout(timeout));
			streamingUpdateTimeouts.current.clear();
		};
	}, []);

	const fetchLogs = useCallback(async () => {
		setFetchingLogs(true);
		setError(null);

		try {
			const result = await triggerGetLogs({ filters, pagination });

			if (result.error) {
				const errorMessage = getErrorMessage(result.error);
				setError(errorMessage);
				setLogs([]);
				setTotalItems(0);
			} else if (result.data) {
				setLogs(result.data.logs || []);
				setTotalItems(result.data.stats.total_requests);
			}

			// Only set showEmptyState on initial load and only based on total logs
			if (initialLoading) {
				// Check if there are any logs globally, not just in the current filter
				setShowEmptyState(result.data ? !result.data.has_logs : true);
			}
		} catch {
			setError("Cannot fetch logs. Please check if logs are enabled in your Bifrost config.");
			setLogs([]);
			setTotalItems(0);
			setShowEmptyState(true);
		} finally {
			setFetchingLogs(false);
		}

		// eslint-disable-next-line react-hooks/exhaustive-deps
	}, [filters, pagination]);

	const fetchStats = useCallback(async () => {
		setFetchingStats(true);

		try {
			const result = await triggerGetStats({ filters });

			if (result.error) {
				// Don't show error for stats failure, just log it
				console.error("Failed to fetch stats:", result.error);
			} else if (result.data) {
				setStats(result.data);
			}
		} catch (error) {
			console.error("Failed to fetch stats:", error);
		} finally {
			setFetchingStats(false);
		}
	}, [filters, triggerGetStats]);

	const fetchHistogram = useCallback(async () => {
		setFetchingHistogram(true);

		try {
			const result = await triggerGetHistogram({ filters });

			if (result.error) {
				// Don't show error for histogram failure, just log it
				console.error("Failed to fetch histogram:", result.error);
			} else if (result.data) {
				setHistogram(result.data);
			}
		} catch (error) {
			console.error("Failed to fetch histogram:", error);
		} finally {
			setFetchingHistogram(false);
		}
	}, [filters, triggerGetHistogram]);

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

	// Fetch logs when filters or pagination change
	useEffect(() => {
		if (!initialLoading) {
			fetchLogs();
		}
		// eslint-disable-next-line react-hooks/exhaustive-deps
	}, [filters, pagination, initialLoading]);

	// Fetch stats and histogram when filters change (but not pagination)
	useEffect(() => {
		if (!initialLoading) {
			fetchStats();
			fetchHistogram();
		}
		// eslint-disable-next-line react-hooks/exhaustive-deps
	}, [filters, initialLoading]);

	// Initial load
	useEffect(() => {
		const initialLoad = async () => {
			// Load logs and stats in parallel, don't wait for stats to show the page
			await fetchLogs();
			fetchStats(); // Don't await - let it load in background
			fetchHistogram(); // Don't await - let it load in background
			setInitialLoading(false);
		};
		initialLoad();
		// eslint-disable-next-line react-hooks/exhaustive-deps
	}, []);

	const getMessageText = (content: ChatMessageContent): string => {
		if (typeof content === "string") {
			return content;
		}
		if (Array.isArray(content)) {
			return content.reduce((acc: string, block: ContentBlock) => {
				if (block.type === "text" && block.text) {
					return acc + block.text;
				}
				return acc;
			}, "");
		}
		return "";
	};

	// Helper function to check if a log matches the current filters
	const matchesFilters = (log: LogEntry, filters: LogFilters, applyTimeFilters = true): boolean => {
		if (filters.missing_cost_only && typeof log.cost === "number" && log.cost > 0) {
			return false;
		}
		if (filters.providers?.length && !filters.providers.includes(log.provider)) {
			return false;
		}
		if (filters.models?.length && !filters.models.includes(log.model)) {
			return false;
		}
		if (filters.status?.length && !filters.status.includes(log.status)) {
			return false;
		}
		if (filters.objects?.length && !filters.objects.includes(log.object)) {
			return false;
		}
		if (filters.selected_key_ids?.length && !filters.selected_key_ids.includes(log.selected_key_id)) {
			return false;
		}
		if (filters.virtual_key_ids?.length && log.virtual_key_id && !filters.virtual_key_ids.includes(log.virtual_key_id)) {
			return false;
		}
		if (filters.start_time && new Date(log.timestamp) < new Date(filters.start_time)) {
			return false;
		}
		if (applyTimeFilters && filters.end_time && new Date(log.timestamp) > new Date(filters.end_time)) {
			return false;
		}
		if (filters.min_latency && (!log.latency || log.latency < filters.min_latency)) {
			return false;
		}
		if (filters.max_latency && (!log.latency || log.latency > filters.max_latency)) {
			return false;
		}
		if (filters.min_tokens && (!log.token_usage || log.token_usage.total_tokens < filters.min_tokens)) {
			return false;
		}
		if (filters.max_tokens && (!log.token_usage || log.token_usage.total_tokens > filters.max_tokens)) {
			return false;
		}
		if (filters.content_search) {
			const search = filters.content_search.toLowerCase();
			const content = [
				...(log.input_history || []).map((msg: ChatMessage) => getMessageText(msg.content)),
				log.output_message ? getMessageText(log.output_message.content) : "",
			]
				.join(" ")
				.toLowerCase();

			if (!content.includes(search)) {
				return false;
			}
		}
		return true;
	};

	const statCards = useMemo(
		() => [
			{
				title: "Total Requests",
				value: fetchingStats ? <Skeleton className="h-8 w-20" /> : stats?.total_requests.toLocaleString() || "-",
				icon: <BarChart className="size-4" />,
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
				title: "Total Tokens",
				value: fetchingStats ? <Skeleton className="h-8 w-24" /> : stats?.total_tokens.toLocaleString() || "-",
				icon: <Hash className="size-4" />,
			},
			{
				title: "Total Cost",
				value: fetchingStats ? <Skeleton className="h-8 w-20" /> : stats ? `$${(stats.total_cost ?? 0).toFixed(4)}` : "-",
				icon: <DollarSign className="size-4" />,
			},
		],
		[stats, fetchingStats],
	);

	const columns = useMemo(() => createColumns(handleDelete, hasDeleteAccess), [handleDelete, hasDeleteAccess]);

	return (
		<div className="dark:bg-card h-[calc(100dvh-3.3rem)] max-h-[calc(100dvh-1.5rem)] bg-white">
			{initialLoading ? (
				<FullPageLoader />
			) : showEmptyState ? (
				<EmptyState isSocketConnected={isSocketConnected} error={error} />
			) : (
				<div className="mx-auto flex h-full w-full flex-col">
					<div className="flex flex-1 flex-col gap-2 overflow-hidden">
						{/* Quick Stats */}
						<div className="grid shrink-0 grid-cols-1 gap-4 md:grid-cols-5">
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

						{/* Volume Chart */}
						<div className="shrink-0">
							<LogsVolumeChart
								data={histogram}
								loading={fetchingHistogram}
								onTimeRangeChange={handleTimeRangeChange}
								onResetZoom={handleResetZoom}
								isZoomed={isZoomed}
								startTime={urlState.start_time}
								endTime={urlState.end_time}
								isOpen={isChartOpen}
								onOpenChange={setIsChartOpen}
							/>
						</div>

						{/* Error Alert */}
						{error && (
							<Alert variant="destructive" className="shrink-0">
								<AlertCircle className="h-4 w-4" />
								<AlertDescription>{error}</AlertDescription>
							</Alert>
						)}

						<div className="min-h-0 flex-1">
							<LogsDataTable
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
					</div>

					{/* Log Detail Sheet */}
					<LogDetailSheet
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
