import { LogDetailSheet } from "@/app/workspace/logs/sheets/logDetailsSheet";
import { SessionDetailsSheet } from "@/app/workspace/logs/sheets/sessionDetailsSheet";
import { createColumns } from "@/app/workspace/logs/views/columns";
import { EmptyState } from "@/app/workspace/logs/views/emptyState";
import { LogsHeaderView } from "@/app/workspace/logs/views/logsHeaderView";
import { LogsDataTable } from "@/app/workspace/logs/views/logsTable";
import { LogsVolumeChart } from "@/app/workspace/logs/views/logsVolumeChart";
import { LogsFilterSidebar } from "@/components/filters/logsFilterSidebar";
import FullPageLoader from "@/components/fullPageLoader";
import { useColumnConfig } from "@/components/table";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Card, CardContent } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { useWebSocket } from "@/hooks/useWebSocket";
import {
	getErrorMessage,
	useDeleteLogsMutation,
	useGetAvailableFilterDataQuery,
	useLazyGetLogsHistogramQuery,
	useLazyGetLogsQuery,
	useLazyGetLogsStatsQuery,
} from "@/lib/store";
import { useLazyGetLogByIdQuery } from "@/lib/store/apis/logsApi";
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
import { COMPACT_NUMBER_FORMAT } from "@/lib/utils/numbers";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import NumberFlow from "@number-flow/react";
import { AlertCircle, BarChart, CheckCircle, Clock, DollarSign, Hash, Info } from "lucide-react";
import { parseAsArrayOf, parseAsBoolean, parseAsInteger, parseAsString, useQueryStates } from "nuqs";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";

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

	const [selectedSessionId, setSelectedSessionId] = useState<string | null>(null);
	const [sessionHighlightedLogId, setSessionHighlightedLogId] = useState<string | null>(null);
	// Stable handler so SessionDetailsSheet's loadSessionPage useCallback doesn't
	// recreate on every parent re-render. Without this, every live WebSocket log
	// tick would re-render LogsPage, hand the sheet a fresh inline arrow, recreate
	// loadSessionPage, and trip the reset effect — wiping sessionLogs and
	// refetching from offset 0 while the sheet is open.
	const handleSessionSheetOpenChange = useCallback((open: boolean) => {
		if (!open) {
			setSelectedSessionId(null);
			setSessionHighlightedLogId(null);
		}
	}, []);
	const [isChartOpen, setIsChartOpen] = useState(true);
	const [triggerGetLogById] = useLazyGetLogByIdQuery();
	const [fetchedLog, setFetchedLog] = useState<LogEntry | null>(null);

	// Debouncing for streaming updates (client-side)
	const streamingUpdateTimeouts = useRef<Map<string, ReturnType<typeof setTimeout>>>(new Map());

	// Track if user has manually modified the time range
	const userModifiedTimeRange = useRef<boolean>(false);

	// Capture initial defaults on mount to detect shared URLs with custom time ranges
	const initialDefaults = useRef(dateUtils.getDefaultTimeRange());

	// Memoize default time range to prevent recalculation on every render
	// This is crucial to avoid triggering refetches when the sheet opens/closes
	const defaultTimeRange = useMemo(() => dateUtils.getDefaultTimeRange(), []);

	// Get fresh default time range for refresh logic
	const getDefaultTimeRange = () => dateUtils.getDefaultTimeRange();

	// URL state management with nuqs - all filters and pagination in URL
	const [urlState, setUrlState] = useQueryStates(
		{
			parent_request_id: parseAsString.withDefault(""),
			providers: parseAsArrayOf(parseAsString).withDefault([]),
			models: parseAsArrayOf(parseAsString).withDefault([]),
			aliases: parseAsArrayOf(parseAsString).withDefault([]),
			status: parseAsArrayOf(parseAsString).withDefault([]),
			objects: parseAsArrayOf(parseAsString).withDefault([]),
			selected_key_ids: parseAsArrayOf(parseAsString).withDefault([]),
			virtual_key_ids: parseAsArrayOf(parseAsString).withDefault([]),
			routing_rule_ids: parseAsArrayOf(parseAsString).withDefault([]),
			routing_engine_used: parseAsArrayOf(parseAsString).withDefault([]),
			user_ids: parseAsArrayOf(parseAsString).withDefault([]),
			team_ids: parseAsArrayOf(parseAsString).withDefault([]),
			customer_ids: parseAsArrayOf(parseAsString).withDefault([]),
			business_unit_ids: parseAsArrayOf(parseAsString).withDefault([]),
			content_search: parseAsString.withDefault(""),
			start_time: parseAsInteger.withDefault(defaultTimeRange.startTime),
			end_time: parseAsInteger.withDefault(defaultTimeRange.endTime),
			limit: parseAsInteger.withDefault(25), // Default fallback, actual value calculated based on table height
			offset: parseAsInteger.withDefault(0),
			sort_by: parseAsString.withDefault("timestamp"),
			order: parseAsString.withDefault("desc"),
			live_enabled: parseAsBoolean.withDefault(true),
			missing_cost_only: parseAsBoolean.withDefault(false),
			metadata_filters: parseAsString.withDefault(""),
			selected_log: parseAsString.withDefault(""),
		},
		{
			history: "push",
			shallow: false,
		},
	);

	// Derive selectedLog: find in current logs array, or fetch by ID from API
	const selectedLogId = urlState.selected_log || null;
	const selectedLogFromData = useMemo(
		() => (selectedLogId ? (logs.find((l) => l.id === selectedLogId) ?? null) : null),
		[selectedLogId, logs],
	);

	const activeLogFetchId = useRef<string | null>(null);
	useEffect(() => {
		if (!selectedLogId || selectedLogFromData) {
			setFetchedLog(null);
			activeLogFetchId.current = null;
			return;
		}
		// Track which log ID this fetch is for to prevent stale responses
		const fetchId = selectedLogId;
		activeLogFetchId.current = fetchId;
		triggerGetLogById(selectedLogId).then((result) => {
			if (activeLogFetchId.current === fetchId) {
				if (result.data) {
					setFetchedLog(result.data);
				} else if (result.error) {
					setError(getErrorMessage(result.error));
				}
			}
		});
	}, [selectedLogId, selectedLogFromData, triggerGetLogById]);

	const selectedLog = selectedLogFromData ?? fetchedLog;

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

	// Convert URL state to filters and pagination for API calls
	const filters: LogFilters = useMemo(
		() => ({
			parent_request_id: urlState.parent_request_id,
			providers: urlState.providers,
			models: urlState.models,
			aliases: urlState.aliases,
			status: urlState.status,
			objects: urlState.objects,
			selected_key_ids: urlState.selected_key_ids,
			virtual_key_ids: urlState.virtual_key_ids,
			routing_rule_ids: urlState.routing_rule_ids,
			routing_engine_used: urlState.routing_engine_used,
			user_ids: urlState.user_ids,
			team_ids: urlState.team_ids,
			customer_ids: urlState.customer_ids,
			business_unit_ids: urlState.business_unit_ids,
			content_search: urlState.content_search,
			start_time: dateUtils.toISOString(urlState.start_time),
			end_time: dateUtils.toISOString(urlState.end_time),
			missing_cost_only: urlState.missing_cost_only,
			metadata_filters: urlState.metadata_filters
				? (() => {
						try {
							return JSON.parse(urlState.metadata_filters);
						} catch {
							return undefined;
						}
					})()
				: undefined,
		}),
		// Only re-derive filters when filter-related URL params change (not pagination)
		[
			urlState.providers,
			urlState.models,
			urlState.aliases,
			urlState.status,
			urlState.objects,
			urlState.selected_key_ids,
			urlState.virtual_key_ids,
			urlState.routing_rule_ids,
			urlState.routing_engine_used,
			urlState.user_ids,
			urlState.team_ids,
			urlState.customer_ids,
			urlState.business_unit_ids,
			urlState.content_search,
			urlState.parent_request_id,
			urlState.start_time,
			urlState.end_time,
			urlState.missing_cost_only,
			urlState.metadata_filters,
		],
	);

	const pagination: Pagination = useMemo(
		() => ({
			limit: urlState.limit,
			offset: urlState.offset,
			sort_by: urlState.sort_by as "timestamp" | "latency" | "tokens" | "cost",
			order: urlState.order as "asc" | "desc",
		}),
		[urlState.limit, urlState.offset, urlState.sort_by, urlState.order],
	);

	const liveEnabled = urlState.live_enabled;

	// Helper to update filters in URL
	const setFilters = useCallback(
		(newFilters: LogFilters) => {
			// Mark time range as user-modified only if start_time or end_time actually changed
			if (newFilters.start_time !== filters.start_time || newFilters.end_time !== filters.end_time) {
				userModifiedTimeRange.current = true;
			}

			setUrlState({
				parent_request_id: newFilters.parent_request_id || "",
				providers: newFilters.providers || [],
				models: newFilters.models || [],
				aliases: newFilters.aliases || [],
				status: newFilters.status || [],
				objects: newFilters.objects || [],
				selected_key_ids: newFilters.selected_key_ids || [],
				virtual_key_ids: newFilters.virtual_key_ids || [],
				routing_rule_ids: newFilters.routing_rule_ids || [],
				routing_engine_used: newFilters.routing_engine_used || [],
				user_ids: newFilters.user_ids || [],
				team_ids: newFilters.team_ids || [],
				customer_ids: newFilters.customer_ids || [],
				business_unit_ids: newFilters.business_unit_ids || [],
				content_search: newFilters.content_search || "",
				start_time: newFilters.start_time ? dateUtils.toUnixTimestamp(new Date(newFilters.start_time)) : undefined,
				end_time: newFilters.end_time ? dateUtils.toUnixTimestamp(new Date(newFilters.end_time)) : undefined,
				missing_cost_only: newFilters.missing_cost_only ?? false,
				metadata_filters: newFilters.metadata_filters ? JSON.stringify(newFilters.metadata_filters) : "",
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

	const handleFilterByParentRequestId = useCallback(
		(parentRequestId: string) => {
			setSelectedSessionId(null);
			setSessionHighlightedLogId(null);
			setUrlState({ selected_log: "" }, { history: "replace" });
			setFilters({
				...filters,
				parent_request_id: parentRequestId,
			});
		},
		[filters, setFilters],
	);

	const handleDelete = useCallback(
		async (log: LogEntry) => {
			try {
				await deleteLogs({ ids: [log.id] }).unwrap();
				setLogs((prevLogs) => prevLogs.filter((l) => l.id !== log.id));
				setTotalItems((prev) => prev - 1);
				// Clear selected log if it was the deleted one
				if (urlState.selected_log === log.id) {
					setUrlState({ selected_log: "" });
				}
			} catch (error) {
				setError(getErrorMessage(error));
			}
		},
		[deleteLogs, urlState.selected_log, setUrlState],
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

				// Update fetchedLog if it matches (for real-time detail sheet updates when log is not on current page)
				setFetchedLog((prev) => {
					if (prev && prev.id === log.id) {
						return log;
					}
					return prev;
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

						// Update user-facing success rate (same approximation as success_rate)
						const userSuccessCount = ((prevStats.user_facing_success_rate ?? 0) / 100) * prevStats.total_requests;
						const newUserSuccessCount = log.status === "success" ? userSuccessCount + 1 : userSuccessCount;
						newStats.user_facing_success_rate = (newUserSuccessCount / newStats.total_requests) * 100;

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
						if (!prevHistogram || typeof prevHistogram.bucket_size_seconds !== "number" || prevHistogram.bucket_size_seconds <= 0) {
							return prevHistogram;
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

		// Update fetchedLog if it matches the updated log (for real-time detail sheet updates when log is not on current page)
		setFetchedLog((prev) => {
			if (prev && prev.id === updatedLog.id) {
				return updatedLog;
			}
			return prev;
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
		if (filters.user_ids?.length) {
			if (!log.user_id || !filters.user_ids.includes(log.user_id)) return false;
		}
		if (filters.team_ids?.length) {
			if (!log.team_id || !filters.team_ids.includes(log.team_id)) return false;
		}
		if (filters.customer_ids?.length) {
			if (!log.customer_id || !filters.customer_ids.includes(log.customer_id)) return false;
		}
		if (filters.business_unit_ids?.length) {
			if (!log.business_unit_id || !filters.business_unit_ids.includes(log.business_unit_id)) return false;
		}
		if (filters.missing_cost_only && typeof log.cost === "number" && log.cost > 0) {
			return false;
		}
		if (filters.parent_request_id && log.parent_request_id !== filters.parent_request_id) {
			return false;
		}
		if (filters.providers?.length && !filters.providers.includes(log.provider)) {
			return false;
		}
		if (filters.aliases?.length && !filters.aliases.includes(log.alias ?? "")) {
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
		if (filters.selected_key_ids?.length && log.selected_key_id && !filters.selected_key_ids.includes(log.selected_key_id)) {
			return false;
		}
		if (filters.virtual_key_ids?.length) {
			if (!log.virtual_key_id || !filters.virtual_key_ids.includes(log.virtual_key_id)) {
				return false;
			}
		}
		if (filters.routing_rule_ids?.length) {
			if (!log.routing_rule_id || !filters.routing_rule_ids.includes(log.routing_rule_id)) {
				return false;
			}
		}
		if (filters.routing_engine_used?.length) {
			if (!log.routing_engines_used || !log.routing_engines_used.some((engine) => filters.routing_engine_used!.includes(engine))) {
				return false;
			}
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
		if (filters.metadata_filters) {
			for (const [key, value] of Object.entries(filters.metadata_filters)) {
				const metadataValue = log.metadata?.[key];
				if (metadataValue === undefined || String(metadataValue) !== value) {
					return false;
				}
			}
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
				value: <NumberFlow value={stats?.total_requests ?? 0} format={COMPACT_NUMBER_FORMAT} />,
				icon: <BarChart className="size-4" />,
			},
			{
				title: "Success Rate",
				value: <NumberFlow value={stats?.success_rate ?? 0} format={{ minimumFractionDigits: 2, maximumFractionDigits: 2 }} suffix="%" />,
				icon: <CheckCircle className="size-4" />,
				description:
					"Success rate as perceived by the system. Each fallback counts as a separate attempt. Retries on the same request are counted as one attempt.",
			},
			{
				title: "User Success Rate",
				value: fetchingStats ? <Skeleton className="h-8 w-16" /> : stats ? `${(stats.user_facing_success_rate ?? 0).toFixed(2)}%` : "-",
				icon: <CheckCircle className="size-4" />,
				description: "Success rate as perceived by the end user. It includes fallback chains as one request.",
			},
			{
				title: "Avg Latency",
				value: (
					<NumberFlow value={stats?.average_latency ?? 0} format={{ minimumFractionDigits: 2, maximumFractionDigits: 2 }} suffix="ms" />
				),
				icon: <Clock className="size-4" />,
			},
			{
				title: "Total Tokens",
				value: <NumberFlow value={stats?.total_tokens ?? 0} format={COMPACT_NUMBER_FORMAT} />,
				icon: <Hash className="size-4" />,
			},
			{
				title: "Total Cost",
				value: <NumberFlow value={stats?.total_cost ?? 0} format={{ ...COMPACT_NUMBER_FORMAT, style: "currency", currency: "USD" }} />,
				icon: <DollarSign className="size-4" />,
			},
		],
		[stats],
	);

	const { data: filterData } = useGetAvailableFilterDataQuery();

	// Get metadata keys from filterdata API so columns always show even with no data on current page
	const metadataKeys = useMemo(() => {
		if (!filterData?.metadata_keys) return [];
		return Object.keys(filterData.metadata_keys).sort();
	}, [filterData?.metadata_keys]);

	const columns = useMemo(
		() => createColumns(handleDelete, hasDeleteAccess, metadataKeys),
		[handleDelete, hasDeleteAccess, metadataKeys],
	);

	const columnIds = useMemo(
		() => columns.map((col) => ("id" in col && col.id ? col.id : "accessorKey" in col ? String(col.accessorKey) : "")).filter(Boolean),
		[columns],
	);

	const COLUMN_LABELS: Record<string, string> = useMemo(
		() => ({
			timestamp: "Time",
			request_type: "Type",
			input: "Message",
			provider: "Provider",
			model: "Model",
			latency: "Latency",
			tokens: "Tokens",
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
	} = useColumnConfig({ columnIds, paramName: "cols" });

	// Navigation for log detail sheet
	const selectedLogIndex = useMemo(() => (selectedLogId ? logs.findIndex((l) => l.id === selectedLogId) : -1), [selectedLogId, logs]);

	const handleLogNavigate = useCallback(
		(direction: "prev" | "next") => {
			const currentLogId = selectedLogId || "";
			if (direction === "prev") {
				if (selectedLogIndex > 0) {
					// Navigate to previous log on current page
					setUrlState({ selected_log: logs[selectedLogIndex - 1].id });
				} else if (pagination.offset > 0) {
					// Go to previous page and select the last item
					const newOffset = Math.max(0, pagination.offset - pagination.limit);
					setUrlState({ offset: newOffset, selected_log: "" });
					// Fetch previous page, then select last log
					triggerGetLogs({
						filters,
						pagination: { ...pagination, offset: newOffset },
					}).then((result) => {
						if (result.data?.logs?.length) {
							const lastLog = result.data.logs[result.data.logs.length - 1];
							setUrlState({ selected_log: lastLog.id });
						} else if (result.error) {
							setUrlState({ offset: pagination.offset, selected_log: currentLogId });
							setError(getErrorMessage(result.error));
						}
					});
				}
			} else {
				if (selectedLogIndex >= 0 && selectedLogIndex < logs.length - 1) {
					// Navigate to next log on current page
					setUrlState({ selected_log: logs[selectedLogIndex + 1].id });
				} else if (pagination.offset + pagination.limit < totalItems) {
					// Go to next page and select the first item
					const newOffset = pagination.offset + pagination.limit;
					setUrlState({ offset: newOffset, selected_log: "" });
					// Fetch next page, then select first log
					triggerGetLogs({
						filters,
						pagination: { ...pagination, offset: newOffset },
					}).then((result) => {
						if (result.data?.logs?.length) {
							const firstLog = result.data.logs[0];
							setUrlState({ selected_log: firstLog.id });
						} else if (result.error) {
							setUrlState({ offset: pagination.offset, selected_log: currentLogId });
							setError(getErrorMessage(result.error));
						}
					});
				}
			}
		},
		[selectedLogId, selectedLogIndex, logs, pagination, totalItems, filters, setUrlState, triggerGetLogs],
	);

	return (
		<div className="dark:bg-card no-padding-parent no-border-parent h-[calc(100vh_-_16px)]">
			{initialLoading ? (
				<FullPageLoader />
			) : showEmptyState ? (
				<EmptyState isSocketConnected={isSocketConnected} error={error} />
			) : (
				<div className="bg-background flex h-full w-full grow gap-3">
					{/* Sidebar Filters */}
					<LogsFilterSidebar filters={filters} onFiltersChange={setFilters} />

					{/* Main Content */}
					<div className="bg-card flex min-w-0 flex-1 flex-col gap-2 overflow-hidden rounded-l-md p-4 pb-2">
						<div className="shrink-0">
							<LogsHeaderView
								filters={filters}
								onFiltersChange={setFilters}
								liveEnabled={liveEnabled}
								onLiveToggle={handleLiveToggle}
								fetchLogs={fetchLogs}
								fetchStats={fetchStats}
								columnEntries={columnEntries}
								columnLabels={COLUMN_LABELS}
								onToggleColumnVisibility={toggleColumnVisibility}
								onResetColumns={resetColumns}
							/>
						</div>
						<div className="grid shrink-0 grid-cols-2 gap-4 md:grid-cols-3 lg:grid-cols-6">
							{statCards.map((card) => (
								<Card key={card.title} className="py-4 shadow-none">
									<CardContent
										className={`flex items-center justify-between px-4 transition-opacity duration-200 ${fetchingStats ? "opacity-50" : "opacity-100"}`}
									>
										<div className="w-full min-w-0">
											<div className="text-muted-foreground flex items-center gap-1 text-xs">
												{card.title}
												{"description" in card && card.description && (
													<Tooltip>
														<TooltipTrigger asChild>
															<button
																type="button"
																aria-label={`${card.title} info`}
																data-testid={`logs-metric-info-${card.title.toLowerCase().replace(/\s+/g, "-")}`}
																className="inline-flex items-center"
															>
																<Info className="size-3 cursor-help" />
															</button>
														</TooltipTrigger>
														<TooltipContent className="max-w-72 text-left text-xs text-wrap">{card.description}</TooltipContent>
													</Tooltip>
												)}
											</div>
											<div className="truncate font-mono text-xl font-medium sm:text-2xl">{card.value}</div>
										</div>
									</CardContent>
								</Card>
							))}
						</div>

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
								pagination={pagination}
								onPaginationChange={setPagination}
								onRowClick={(row, columnId) => {
									if (columnId === "actions") return;
									setUrlState({ selected_log: row.id }, { history: "replace" });
									setSelectedSessionId(null);
									setSessionHighlightedLogId(null);
								}}
								liveEnabled={liveEnabled}
								isSocketConnected={isSocketConnected}
								columnEntries={columnEntries}
								columnOrder={columnOrder}
								columnVisibility={columnVisibility}
								columnPinning={columnPinning}
								onToggleColumnVisibility={toggleColumnVisibility}
								onTogglePin={toggleColumnPin}
								onReorderColumns={reorderColumns}
							/>
						</div>
					</div>

					{/* Log Detail Sheet */}
					<LogDetailSheet
						log={selectedLog}
						open={selectedLog !== null}
						onOpenChange={(open) => !open && setUrlState({ selected_log: "" })}
						handleDelete={handleDelete}
						onNavigate={handleLogNavigate}
						hasPrev={selectedLogIndex > 0 || (selectedLogIndex !== -1 && pagination.offset > 0)}
						hasNext={selectedLogIndex !== -1 && (selectedLogIndex < logs.length - 1 || pagination.offset + pagination.limit < totalItems)}
						onFilterByParentRequestId={handleFilterByParentRequestId}
						onViewSession={(sessionId, logId) => {
							setUrlState({ selected_log: "" }, { history: "replace" });
							setSessionHighlightedLogId(logId);
							setSelectedSessionId(sessionId);
						}}
					/>
					<SessionDetailsSheet
						sessionId={selectedSessionId}
						highlightedLogId={sessionHighlightedLogId}
						open={selectedSessionId !== null}
						onOpenChange={handleSessionSheetOpenChange}
						liveEnabled={liveEnabled}
						onLogClick={(log) => {
							setSelectedSessionId(null);
							setUrlState({ selected_log: log.id }, { history: "replace" });
						}}
						onFilterByParentRequestId={handleFilterByParentRequestId}
					/>
				</div>
			)}
		</div>
	);
}