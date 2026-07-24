import { LogDetailSheet } from "@/app/workspace/logs/sheets/logDetailsSheet";
import { SessionDetailsSheet } from "@/app/workspace/logs/sheets/sessionDetailsSheet";
import { createColumns } from "@/app/workspace/logs/views/columns";
import { EmptyState } from "@/app/workspace/logs/views/emptyState";
import { LogsHeaderView } from "@/app/workspace/logs/views/logsHeaderView";
import { LogsDataTable } from "@/app/workspace/logs/views/logsTable";
import { LogsVolumeChart } from "@/app/workspace/logs/views/logsVolumeChart";
import { LogsFilterSidebar } from "@/components/filters/logsFilterSidebar";
import { useColumnConfig } from "@/components/table";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Card, CardContent } from "@/components/ui/card";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import {
	getErrorMessage,
	useDeleteLogsMutation,
	useGetAvailableFilterDataQuery,
	useGetLogsHistogramQuery,
	useGetLogsQuery,
	useGetLogsStatsQuery,
} from "@/lib/store";
import { useLazyGetLogByIdQuery, useLazyGetLogSessionByIdQuery, useLazyGetLogsQuery } from "@/lib/store/apis/logsApi";
import type { DisplayLogEntry, LogEntry, LogFilters, Pagination } from "@/lib/types/logs";
import { dateUtils } from "@/lib/types/logs";
import { COMPACT_NUMBER_FORMAT } from "@/lib/utils/numbers";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import NumberFlow from "@number-flow/react";
import { useLocation } from "@tanstack/react-router";
import { AlertCircle, BarChart, CheckCircle, Clock, DollarSign, Hash, Info } from "lucide-react";
import { parseAsSafeArrayOf, parseAsSafeString } from "@/lib/queryParamsParser";
import { parseAsBoolean, parseAsInteger, parseAsString, useQueryStates } from "nuqs";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";

export default function LogsPage() {
	const [error, setError] = useState<string | null>(null);
	const [showEmptyState, setShowEmptyState] = useState(false);
	const hasCheckedEmptyState = useRef(false);

	const hasDeleteAccess = useRbac(RbacResource.Logs, RbacOperation.Delete);
	const hasRevealAccess = useRbac(RbacResource.Logs, RbacOperation.Reveal);

	const [deleteLogs] = useDeleteLogsMutation();
	// Lazy query kept only for handleLogNavigate (fetches adjacent pages on demand)
	const [triggerGetLogs] = useLazyGetLogsQuery();

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

	// Track if user has manually modified the time range
	const userModifiedTimeRange = useRef<boolean>(false);

	// Memoize default time range to prevent recalculation on every render
	// This is crucial to avoid triggering refetches when the sheet opens/closes
	const defaultTimeRange = useMemo(() => dateUtils.getDefaultTimeRange(), []);

	const { search } = useLocation();
	const hasExplicitTimeRange = (search as Record<string, unknown>)?.start_time && (search as Record<string, unknown>)?.end_time;

	// URL state management with nuqs - all filters and pagination in URL
	const [urlState, setUrlState] = useQueryStates(
		{
			parent_request_id: parseAsString.withDefault(""),
			providers: parseAsSafeArrayOf.withDefault([]),
			models: parseAsSafeArrayOf.withDefault([]),
			aliases: parseAsSafeArrayOf.withDefault([]),
			status: parseAsSafeArrayOf.withDefault([]),
			stop_reasons: parseAsSafeArrayOf.withDefault([]),
			objects: parseAsSafeArrayOf.withDefault([]),
			selected_key_ids: parseAsSafeArrayOf.withDefault([]),
			virtual_key_ids: parseAsSafeArrayOf.withDefault([]),
			routing_rule_ids: parseAsSafeArrayOf.withDefault([]),
			routing_engine_used: parseAsSafeArrayOf.withDefault([]),
			user_ids: parseAsSafeArrayOf.withDefault([]),
			team_ids: parseAsSafeArrayOf.withDefault([]),
			customer_ids: parseAsSafeArrayOf.withDefault([]),
			business_unit_ids: parseAsSafeArrayOf.withDefault([]),
			content_search: parseAsSafeString.withDefault(""),
			start_time: parseAsInteger.withDefault(defaultTimeRange.startTime),
			end_time: parseAsInteger.withDefault(defaultTimeRange.endTime),
			limit: parseAsInteger.withDefault(25), // Default fallback, actual value calculated based on table height
			offset: parseAsInteger.withDefault(0),
			sort_by: parseAsString.withDefault("timestamp"),
			order: parseAsString.withDefault("desc"),
			polling: parseAsBoolean.withDefault(true).withOptions({ clearOnDefault: false }),
			period: parseAsString.withDefault(hasExplicitTimeRange ? "" : "1h").withOptions({ clearOnDefault: false }),
			missing_cost_only: parseAsBoolean.withDefault(false),
			cache_hit_types: parseAsSafeArrayOf.withDefault([]),
			metadata_filters: parseAsString.withDefault(""),
			selected_log: parseAsString.withDefault(""),
			grouped: parseAsBoolean.withDefault(false),
		},
		{
			history: "push",
			shallow: false,
		},
	);

	// Derive selectedLog: find in current logs array, or fetch by ID from API
	const selectedLogId = urlState.selected_log || null;
	const activeLogFetchId = useRef<string | null>(null);
	const polling = urlState.polling;
	// Grouped view collapses fallback chains under their root. Disabled while a
	// session filter is active — that view is already scoped to one chain/session.
	const grouped = urlState.grouped && !urlState.parent_request_id;

	// Convert URL state to filters and pagination for API calls
	const filters: LogFilters = useMemo(
		() => ({
			parent_request_id: urlState.parent_request_id,
			providers: urlState.providers,
			models: urlState.models,
			aliases: urlState.aliases,
			status: urlState.status,
			stop_reasons: urlState.stop_reasons,
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
			missing_cost_only: urlState.missing_cost_only,
			cache_hit_types: urlState.cache_hit_types,
			metadata_filters: urlState.metadata_filters
				? (() => {
						try {
							return JSON.parse(urlState.metadata_filters);
						} catch {
							return undefined;
						}
					})()
				: undefined,
			// Use a period if present
			...(urlState.period
				? { period: urlState.period }
				: {
						start_time: dateUtils.toISOString(urlState.start_time),
						end_time: dateUtils.toISOString(urlState.end_time),
					}),
		}),
		// Only re-derive filters when filter-related URL params change (not pagination)
		[
			urlState.providers,
			urlState.models,
			urlState.aliases,
			urlState.status,
			urlState.stop_reasons,
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
			urlState.missing_cost_only,
			urlState.cache_hit_types,
			urlState.metadata_filters,
			urlState.start_time,
			urlState.end_time,
			urlState.period,
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

	const period = urlState.period;

	// Helper to update filters in URL
	const setFilters = useCallback(
		(newFilters: LogFilters) => {
			// The sidebar/header only manage dimension filters, never the time range: in
			// period mode `newFilters` carries no start/end, so only touch time when an
			// explicit range is actually provided — otherwise we'd wipe the active period/range.
			const hasExplicitTime = !!newFilters.start_time && !!newFilters.end_time;
			const timeChanged =
				hasExplicitTime && (newFilters.start_time !== filters.start_time || newFilters.end_time !== filters.end_time);
			if (timeChanged) {
				userModifiedTimeRange.current = true;
			}

			setUrlState({
				// Clear the period and apply the absolute range only when an explicit one is provided
				...(timeChanged && {
					period: "",
					start_time: dateUtils.toUnixTimestamp(new Date(newFilters.start_time!)),
					end_time: dateUtils.toUnixTimestamp(new Date(newFilters.end_time!)),
				}),
				parent_request_id: newFilters.parent_request_id || "",
				providers: newFilters.providers || [],
				models: newFilters.models || [],
				aliases: newFilters.aliases || [],
				status: newFilters.status || [],
				stop_reasons: newFilters.stop_reasons || [],
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
				missing_cost_only: newFilters.missing_cost_only ?? false,
				cache_hit_types: newFilters.cache_hit_types || [],
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
			userModifiedTimeRange.current = true;
			setUrlState({
				period: "",
				start_time: startTime,
				end_time: endTime,
				offset: 0,
				polling: false,
			});
		},
		[setUrlState],
	);

	// Handler for resetting zoom to default 1h view
	const handleResetZoom = useCallback(() => {
		const now = Math.floor(Date.now() / 1000);
		const oneHour = now - 1 * 60 * 60;
		setUrlState({
			period: "1h",
			start_time: oneHour,
			end_time: now,
			offset: 0,
			polling: true,
		});
	}, [setUrlState]);

	// Zoomed only when a custom absolute range is active (period cleared) and
	// the range is meaningfully narrower than 1h.
	const isZoomed = useMemo(() => {
		if (urlState.period) return false;
		const currentRange = urlState.end_time - urlState.start_time;
		const defaultRange = 1 * 60 * 60;
		return currentRange < defaultRange * 0.9;
	}, [urlState.start_time, urlState.end_time, urlState.period]);

	const {
		data: logsData,
		isFetching: logsIsFetching,
		error: logsError,
		refetch: refetchLogs,
	} = useGetLogsQuery(
		{
			filters,
			pagination,
			rootsOnly: grouped,
		},
		{
			pollingInterval: showEmptyState || polling ? 10000 : 0,
			skipPollingIfUnfocused: true,
		},
	);

	const {
		data: stats,
		isFetching: statsIsFetching,
		refetch: refetchStats,
	} = useGetLogsStatsQuery(
		{
			filters,
		},
		{
			pollingInterval: polling ? 10000 : 0,
			skipPollingIfUnfocused: true,
		},
	);

	const {
		data: histogram,
		isLoading: histogramIsLoading,
		refetch: refetchHistogram,
	} = useGetLogsHistogramQuery(
		{
			filters,
		},
		{
			pollingInterval: polling ? 10000 : 0,
			skipPollingIfUnfocused: true,
		},
	);

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

	// --- Grouped view: chain expansion state -------------------------------
	// Children of an expanded root, keyed by root log id. Loaded lazily via the
	// sessions endpoint (a chain's "session id" is just the root log's id).
	const [expandedChainIds, setExpandedChainIds] = useState<Set<string>>(new Set());
	const [chainChildren, setChainChildren] = useState<Record<string, LogEntry[]>>({});
	const [loadingChainIds, setLoadingChainIds] = useState<Set<string>>(new Set());
	const [triggerGetChainChildren] = useLazyGetLogSessionByIdQuery();

	// Collapse everything when the page of roots changes — expanded ids from the
	// previous page are meaningless and cached children may be stale.
	useEffect(() => {
		setExpandedChainIds(new Set());
		setChainChildren({});
		setLoadingChainIds(new Set());
	}, [filters, pagination, grouped]);

	const handleToggleChain = useCallback(
		(log: LogEntry) => {
			const isExpanded = expandedChainIds.has(log.id);
			setExpandedChainIds((prev) => {
				const next = new Set(prev);
				if (next.has(log.id)) {
					next.delete(log.id);
				} else {
					next.add(log.id);
				}
				return next;
			});
			if (isExpanded || chainChildren[log.id] || loadingChainIds.has(log.id)) return;

			setLoadingChainIds((prev) => new Set(prev).add(log.id));
			triggerGetChainChildren({ sessionId: log.id, pagination: { limit: 500, offset: 0, order: "asc" } }).then((result) => {
				setLoadingChainIds((prev) => {
					const next = new Set(prev);
					next.delete(log.id);
					return next;
				});
				if (result.data) {
					const children = result.data.logs;
					setChainChildren((prevCache) => ({ ...prevCache, [log.id]: children }));
				} else if (result.error) {
					setExpandedChainIds((prev) => {
						const next = new Set(prev);
						next.delete(log.id);
						return next;
					});
					setError(getErrorMessage(result.error));
				}
			});
		},
		[expandedChainIds, chainChildren, loadingChainIds, triggerGetChainChildren],
	);

	const handleDelete = useCallback(
		async (log: LogEntry) => {
			try {
				await deleteLogs({ ids: [log.id] }).unwrap();
				if (urlState.selected_log === log.id) {
					setUrlState({ selected_log: "" });
				}
				refetchLogs();
				refetchStats();
				refetchHistogram();
			} catch (err) {
				setError(getErrorMessage(err));
			}
		},
		[deleteLogs, urlState.selected_log, setUrlState, refetchLogs, refetchStats, refetchHistogram],
	);

	const handlePollToggle = useCallback(
		(enabled: boolean) => {
			setUrlState({ polling: enabled });
			if (enabled) {
				refetchLogs();
				refetchStats();
				refetchHistogram();
			}
		},
		[setUrlState, refetchLogs, refetchStats, refetchHistogram],
	);

	// Period selection: store relative period + fresh timestamps in URL (bypasses setFilters
	// so userModifiedTimeRange stays false and tab-focus refresh keeps working)
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
				value: (
					<NumberFlow
						value={stats?.user_facing_success_rate ?? 0}
						format={{ minimumFractionDigits: 2, maximumFractionDigits: 2 }}
						suffix="%"
					/>
				),
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
				subValue: (
					<>
						<NumberFlow value={stats?.prompt_tokens ?? 0} format={COMPACT_NUMBER_FORMAT} />
						<span> in / </span>
						<NumberFlow value={stats?.completion_tokens ?? 0} format={COMPACT_NUMBER_FORMAT} />
						<span> out</span>
					</>
				),
				description: "Total tokens used, split into input (prompt) and output (completion) tokens.",
			},
			{
				title: "Total Cost",
				value: (
					<NumberFlow
						value={stats?.total_cost ?? 0}
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
		[stats],
	);

	// Only need metadata_keys here (used to render dynamic columns even when the
	// current page has no rows). Scope the request to that one dimension.
	const { data: filterData } = useGetAvailableFilterDataQuery({ dimensions: ["metadata_keys"] });

	// Get metadata keys from filterdata API so columns always show even with no data on current page
	const metadataKeys = useMemo(() => {
		if (!filterData?.metadata_keys) return [];
		return Object.keys(filterData.metadata_keys).sort();
	}, [filterData?.metadata_keys]);

	const columns = useMemo(
		() => createColumns(handleDelete, hasDeleteAccess, metadataKeys, grouped),
		[handleDelete, hasDeleteAccess, metadataKeys, grouped],
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
			virtual_key: "Virtual Key",
			routing_rule: "Routing Rule",
			team: "Team",
			customer: "Customer",
			user: "User",
			business_unit: "Business Unit",
		}),
		[],
	);

	const DEFAULT_HIDDEN_COLUMNS = useMemo(() => ["virtual_key", "routing_rule", "team", "customer", "user", "business_unit"], []);

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
		paramName: "cols",
		storageKey: "bifrost.logs.cols",
		defaultHidden: DEFAULT_HIDDEN_COLUMNS,
		fixedColumns: {
			...(grouped ? { left: ["expand"] } : {}),
			...(hasDeleteAccess ? { right: ["actions"] } : {}),
		},
	});

	// Navigation for log detail sheet
	const logs = logsData?.logs ?? [];
	const totalItems = logsData?.stats?.total_requests ?? 0;

	// Grouped view: splice loaded children in below their expanded root. Children
	// are marked so the table can indent them; they don't affect pagination.
	const displayLogs: DisplayLogEntry[] = useMemo(() => {
		if (!grouped || expandedChainIds.size === 0) return logs;
		const out: DisplayLogEntry[] = [];
		for (const log of logs) {
			out.push(log);
			if (expandedChainIds.has(log.id)) {
				for (const child of chainChildren[log.id] ?? []) {
					out.push({ ...child, __chainChild: true });
				}
			}
		}
		return out;
	}, [logs, grouped, expandedChainIds, chainChildren]);

	const tableMeta = useMemo(
		() => ({ expandedChainIds, loadingChainIds, onToggleChain: handleToggleChain }),
		[expandedChainIds, loadingChainIds, handleToggleChain],
	);
	const selectedLogFromData = useMemo(
		() => (selectedLogId ? (logs.find((l) => l.id === selectedLogId) ?? null) : null),
		[selectedLogId, logs],
	);

	useEffect(() => {
		if (!selectedLogId || selectedLogFromData) {
			setFetchedLog(null);
			activeLogFetchId.current = null;
			return;
		}
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
						rootsOnly: grouped,
					}).then((result) => {
						if (result.data?.logs?.length) {
							const lastLog = result.data.logs[result.data.logs.length - 1];
							setUrlState({ selected_log: lastLog.id });
						} else if (result.error) {
							setUrlState({
								offset: pagination.offset,
								selected_log: currentLogId,
							});
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
						rootsOnly: grouped,
					}).then((result) => {
						if (result.data?.logs?.length) {
							const firstLog = result.data.logs[0];
							setUrlState({ selected_log: firstLog.id });
						} else if (result.error) {
							setUrlState({
								offset: pagination.offset,
								selected_log: currentLogId,
							});
							setError(getErrorMessage(result.error));
						}
					});
				}
			}
		},
		[selectedLogId, selectedLogIndex, logs, pagination, totalItems, filters, grouped, setUrlState, triggerGetLogs],
	);

	return (
		<div className="dark:bg-card no-padding-parent no-border-parent h-[calc(100vh_-_16px)]">
			{showEmptyState ? (
				<EmptyState error={error ?? (logsError ? getErrorMessage(logsError as Parameters<typeof getErrorMessage>[0]) : null)} />
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
								fetchLogs={async () => {
									await refetchLogs();
								}}
								fetchStats={async () => {
									await refetchStats();
								}}
								fetchHistogram={async () => {
									await refetchHistogram();
								}}
								loading={logsIsFetching}
								polling={polling}
								onPollToggle={handlePollToggle}
								grouped={grouped}
								onGroupedToggle={(enabled) => setUrlState({ grouped: enabled, offset: 0 })}
								period={period}
								onPeriodChange={handlePeriodChange}
								totalLogs={totalItems}
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
										className={`flex items-center justify-between px-4 transition-opacity duration-200 ${statsIsFetching ? "opacity-50" : "opacity-100"}`}
									>
										<div className="w-full min-w-0">
											<div className="text-muted-foreground flex items-center gap-1 text-xs">
												<span className="truncate">{card.title}</span>
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
											{"subValue" in card && card.subValue && (
												<div className="truncate font-mono text-[10.5px] tabular-nums">{card.subValue}</div>
											)}
										</div>
									</CardContent>
								</Card>
							))}
						</div>

						<div className="shrink-0">
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

						{(error || !!logsError) && (
							<Alert variant="destructive" className="shrink-0">
								<AlertCircle className="h-4 w-4" />
								<AlertDescription>
									{error ?? (logsError ? getErrorMessage(logsError as Parameters<typeof getErrorMessage>[0]) : "")}
								</AlertDescription>
							</Alert>
						)}

						<div className="min-h-0 flex-1">
							<LogsDataTable
								columns={columns}
								data={displayLogs}
								tableMeta={tableMeta}
								loading={logsIsFetching}
								totalItems={totalItems}
								pagination={pagination}
								onPaginationChange={setPagination}
								onRowClick={(row, columnId) => {
									if (columnId === "actions") return;
									setUrlState({ selected_log: row.id }, { history: "replace" });
									setSelectedSessionId(null);
									setSessionHighlightedLogId(null);
								}}
								polling={polling}
								onRefresh={refetchLogs}
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
						handleDelete={hasDeleteAccess ? handleDelete : undefined}
						canReveal={hasRevealAccess}
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
