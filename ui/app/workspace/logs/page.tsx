"use client";

import { LogDetailSheet } from "@/app/workspace/logs/sheets/logDetailsSheet";
import { createColumns } from "@/app/workspace/logs/views/columns";
import { EmptyState } from "@/app/workspace/logs/views/emptyState";
import { LogsDataTable } from "@/app/workspace/logs/views/logsTable";
import { LogsVolumeChart } from "@/app/workspace/logs/views/logsVolumeChart";
import FullPageLoader from "@/components/fullPageLoader";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Card, CardContent } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import {
  getErrorMessage,
  useDeleteLogsMutation,
  useGetAvailableFilterDataQuery,
} from "@/lib/store";
import {
  useGetLogsHistogramQuery,
  useGetLogsQuery,
  useGetLogsStatsQuery,
  useLazyGetLogByIdQuery,
  useLazyGetLogsQuery,
} from "@/lib/store/apis/logsApi";
import type { LogEntry, LogFilters, Pagination } from "@/lib/types/logs";
import { dateUtils } from "@/lib/types/logs";
import { getRangeForPeriod } from "@/lib/utils/timeRange";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { AlertCircle, BarChart, CheckCircle, Clock, DollarSign, Hash } from "lucide-react";
import { useSearchParams } from "next/navigation";
import {
  parseAsArrayOf,
  parseAsBoolean,
  parseAsInteger,
  parseAsString,
  useQueryStates,
} from "nuqs";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";

export default function LogsPage() {
  const [error, setError] = useState<string | null>(null);
  const [showEmptyState, setShowEmptyState] = useState(false);
  const hasCheckedEmptyState = useRef(false);

  const hasDeleteAccess = useRbac(RbacResource.Logs, RbacOperation.Delete);

  // RTK Query lazy hooks for navigation only
  const [triggerGetLogs] = useLazyGetLogsQuery();
  const [deleteLogs] = useDeleteLogsMutation();

  const [isChartOpen, setIsChartOpen] = useState(true);
  const [triggerGetLogById] = useLazyGetLogByIdQuery();
  const [fetchedLog, setFetchedLog] = useState<LogEntry | null>(null);

  // Track if user has manually modified the time range
  const userModifiedTimeRange = useRef<boolean>(false);

  // Capture initial defaults on mount to detect shared URLs with custom time ranges
  const initialDefaults = useRef(dateUtils.getDefaultTimeRange());

  // Memoize default time range to prevent recalculation on every render
  const defaultTimeRange = useMemo(() => dateUtils.getDefaultTimeRange(), []);

  // Get fresh default time range for refresh logic
  const getDefaultTimeRange = () => dateUtils.getDefaultTimeRange();

  // Check raw URL params before nuqs applies defaults — if both time bounds are
  // already present (carried over from another page), skip the "1h" period default
  // so the mount effect doesn't overwrite the custom range.
  const rawSearchParams = useSearchParams();
  const hasExplicitTimeRange = rawSearchParams.has("start_time") && rawSearchParams.has("end_time");

  // URL state management with nuqs - all filters and pagination in URL
  const [urlState, setUrlState] = useQueryStates(
    {
      providers: parseAsArrayOf(parseAsString).withDefault([]),
      models: parseAsArrayOf(parseAsString).withDefault([]),
      status: parseAsArrayOf(parseAsString).withDefault([]),
      objects: parseAsArrayOf(parseAsString).withDefault([]),
      selected_key_ids: parseAsArrayOf(parseAsString).withDefault([]),
      virtual_key_ids: parseAsArrayOf(parseAsString).withDefault([]),
      routing_rule_ids: parseAsArrayOf(parseAsString).withDefault([]),
      routing_engine_used: parseAsArrayOf(parseAsString).withDefault([]),
      content_search: parseAsString.withDefault(""),
      start_time: parseAsInteger.withDefault(defaultTimeRange.startTime),
      end_time: parseAsInteger.withDefault(defaultTimeRange.endTime),
      limit: parseAsInteger.withDefault(25),
      offset: parseAsInteger.withDefault(0),
      sort_by: parseAsString.withDefault("timestamp"),
      order: parseAsString.withDefault("desc"),
      polling: parseAsBoolean.withDefault(true).withOptions({ clearOnDefault: false }),
      period: parseAsString
        .withDefault(hasExplicitTimeRange ? "" : "1h")
        .withOptions({ clearOnDefault: false }),
      missing_cost_only: parseAsBoolean.withDefault(false),
      metadata_filters: parseAsString.withDefault(""),
      selected_log: parseAsString.withDefault(""),
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
      routing_rule_ids: urlState.routing_rule_ids,
      routing_engine_used: urlState.routing_engine_used,
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
    [
      urlState.providers,
      urlState.models,
      urlState.status,
      urlState.objects,
      urlState.selected_key_ids,
      urlState.virtual_key_ids,
      urlState.routing_rule_ids,
      urlState.routing_engine_used,
      urlState.content_search,
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

  const polling = urlState.polling;
  const period = urlState.period;

  // RTK Query hooks with polling
  const {
    data: logsData,
    isLoading: logsIsLoading,
    isFetching: logsIsFetching,
    refetch: refetchLogs,
  } = useGetLogsQuery(
    { filters, pagination },
    {
      pollingInterval: showEmptyState ? 3000 : polling && !period ? 5000 : 0,
      refetchOnMountOrArgChange: true,
      skipPollingIfUnfocused: true,
    },
  );

  const {
    data: statsData,
    isFetching: statsIsFetching,
    refetch: refetchStats,
  } = useGetLogsStatsQuery(
    { filters },
    {
      pollingInterval: polling && !period ? 5000 : 0,
      refetchOnMountOrArgChange: true,
      skipPollingIfUnfocused: true,
    },
  );

  const {
    data: histogram,
    isFetching: histogramIsFetching,
    refetch: refetchHistogram,
  } = useGetLogsHistogramQuery(
    { filters },
    {
      pollingInterval: polling && !period ? 5000 : 0,
      refetchOnMountOrArgChange: true,
      skipPollingIfUnfocused: true,
    },
  );

  const logs = logsData?.logs ?? [];
  const totalItems = logsData?.stats?.total_requests ?? 0;
  const stats = statsData ?? null;
  const histogramData = histogram ?? null;

  // showEmptyState effect — only set once on first data, then transition out
  useEffect(() => {
    if (!logsData) return;
    if (!hasCheckedEmptyState.current) {
      setShowEmptyState(!logsData.has_logs);
      hasCheckedEmptyState.current = true;
    } else if (showEmptyState && logsData.has_logs) {
      setShowEmptyState(false);
    }
  }, [logsData, showEmptyState]);

  // Freshen period timestamps on mount
  useEffect(() => {
    if (urlState.period) {
      const { from, to } = getRangeForPeriod(urlState.period);
      const freshEnd = Math.floor(to.getTime() / 1000);
      if (Math.abs(urlState.end_time - freshEnd) > 60) {
        setUrlState(
          {
            start_time: Math.floor(from.getTime() / 1000),
            end_time: freshEnd,
            period: urlState.period ?? "",
          },
          { history: "replace" },
        );
      }
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // Refresh time range defaults on page focus/visibility
  useEffect(() => {
    const refreshDefaultsIfStale = () => {
      // If there's a period set, update timestamps to keep the window fresh
      if (urlState.period) {
        const { from, to } = getRangeForPeriod(urlState.period);
        setUrlState(
          {
            start_time: Math.floor(from.getTime() / 1000),
            end_time: Math.floor(to.getTime() / 1000),
            period: urlState.period ?? "",
          },
          { history: "replace" },
        );
        return;
      }

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
          setUrlState(
            {
              start_time: defaults.startTime,
              end_time: defaults.endTime,
              period: urlState.period ?? "",
            },
            { history: "replace" },
          );
          // Update baseline so subsequent focus events compare against refreshed defaults
          initialDefaults.current.startTime = defaults.startTime;
          initialDefaults.current.endTime = defaults.endTime;
        }
      }
    };

    const handleVisibilityChange = () => {
      if (!polling) return;
      if (!document.hidden) {
        refreshDefaultsIfStale();
      }
    };

    const handleFocus = () => {
      if (!polling) return;
      refreshDefaultsIfStale();
    };

    document.addEventListener("visibilitychange", handleVisibilityChange);
    window.addEventListener("focus", handleFocus);
    return () => {
      document.removeEventListener("visibilitychange", handleVisibilityChange);
      window.removeEventListener("focus", handleFocus);
    };
  }, [urlState.start_time, urlState.end_time, urlState.period, setUrlState, polling]);

  // Refresh the time window every 5s while live polling is on and a relative period is active.
  // Updating start_time/end_time changes RTK args → triggers a refetch without needing pollingInterval.
  useEffect(() => {
    if (!polling || !urlState.period) return;

    const id = setInterval(() => {
      if (document.hidden) return;
      const { from, to } = getRangeForPeriod(urlState.period);
      setUrlState(
        {
          start_time: Math.floor(from.getTime() / 1000),
          end_time: Math.floor(to.getTime() / 1000),
          period: urlState.period ?? "",
        },
        { history: "replace" },
      );
    }, 5000);

    return () => clearInterval(id);
  }, [polling, urlState.period, setUrlState]);

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

  // Helper to update filters in URL
  const setFilters = useCallback(
    (newFilters: LogFilters) => {
      const timeChanged =
        newFilters.start_time !== filters.start_time || newFilters.end_time !== filters.end_time;
      if (timeChanged) userModifiedTimeRange.current = true;

      setUrlState({
        ...(timeChanged && { period: "" }),
        providers: newFilters.providers || [],
        models: newFilters.models || [],
        status: newFilters.status || [],
        objects: newFilters.objects || [],
        selected_key_ids: newFilters.selected_key_ids || [],
        virtual_key_ids: newFilters.virtual_key_ids || [],
        routing_rule_ids: newFilters.routing_rule_ids || [],
        routing_engine_used: newFilters.routing_engine_used || [],
        content_search: newFilters.content_search || "",
        start_time: newFilters.start_time
          ? dateUtils.toUnixTimestamp(new Date(newFilters.start_time))
          : undefined,
        end_time: newFilters.end_time
          ? dateUtils.toUnixTimestamp(new Date(newFilters.end_time))
          : undefined,
        missing_cost_only: newFilters.missing_cost_only ?? filters.missing_cost_only ?? false,
        metadata_filters: newFilters.metadata_filters
          ? JSON.stringify(newFilters.metadata_filters)
          : "",
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

  // Handler for resetting zoom to default 1h view
  const handleResetZoom = useCallback(() => {
    const now = Math.floor(Date.now() / 1000);
    const oneHoursAgo = now - 1 * 60 * 60;
    setUrlState({
      start_time: oneHoursAgo,
      end_time: now,
      offset: 0,
    });
  }, [setUrlState]);

  // Check if user has zoomed (time range is different from default 1h)
  const isZoomed = useMemo(() => {
    const currentRange = urlState.end_time - urlState.start_time;
    const defaultRange = 1 * 60 * 60; // 1 hours in seconds
    return currentRange < defaultRange * 0.9;
  }, [urlState.start_time, urlState.end_time]);

  const handleDelete = useCallback(
    async (log: LogEntry) => {
      try {
        await deleteLogs({ ids: [log.id] }).unwrap();
        if (urlState.selected_log === log.id) setUrlState({ selected_log: "" });
        refetchLogs();
      } catch (error) {
        setError(getErrorMessage(error));
      }
    },
    [deleteLogs, urlState.selected_log, setUrlState, refetchLogs],
  );

  const handleRefresh = useCallback(() => {
    if (period) {
      const { from, to } = getRangeForPeriod(period);
      setUrlState({
        start_time: Math.floor(from.getTime() / 1000),
        end_time: Math.floor(to.getTime() / 1000),
        period: urlState.period ?? "",
      });
    } else {
      refetchLogs();
      refetchStats();
      refetchHistogram();
    }
  }, [period, setUrlState, refetchLogs, refetchStats, refetchHistogram]);

  const handlePollToggle = useCallback(
    (enabled: boolean) => {
      setUrlState({ polling: enabled });
      if (enabled) {
        handleRefresh();
      }
    },
    [setUrlState, handleRefresh],
  );

  const handlePeriodChange = useCallback(
    (p: string, from: Date, to: Date) => {
      setUrlState({
        period: p,
        start_time: Math.floor(from.getTime() / 1000),
        end_time: Math.floor(to.getTime() / 1000),
        offset: 0,
      });
    },
    [setUrlState],
  );

  const statCards = useMemo(
    () => [
      {
        title: "Total Requests",
        value: statsIsFetching ? (
          <Skeleton className="h-8 w-20" />
        ) : (
          stats?.total_requests.toLocaleString() || "-"
        ),
        icon: <BarChart className="size-4" />,
      },
      {
        title: "Success Rate",
        value: statsIsFetching ? (
          <Skeleton className="h-8 w-16" />
        ) : stats ? (
          `${stats.success_rate.toFixed(2)}%`
        ) : (
          "-"
        ),
        icon: <CheckCircle className="size-4" />,
      },
      {
        title: "Avg Latency",
        value: statsIsFetching ? (
          <Skeleton className="h-8 w-20" />
        ) : stats ? (
          `${stats.average_latency.toFixed(2)}ms`
        ) : (
          "-"
        ),
        icon: <Clock className="size-4" />,
      },
      {
        title: "Total Tokens",
        value: statsIsFetching ? (
          <Skeleton className="h-8 w-24" />
        ) : (
          stats?.total_tokens.toLocaleString() || "-"
        ),
        icon: <Hash className="size-4" />,
      },
      {
        title: "Total Cost",
        value: statsIsFetching ? (
          <Skeleton className="h-8 w-20" />
        ) : stats ? (
          `$${(stats.total_cost ?? 0).toFixed(4)}`
        ) : (
          "-"
        ),
        icon: <DollarSign className="size-4" />,
      },
    ],
    [stats, statsIsFetching],
  );

  // Get metadata keys from filterdata API so columns always show even with no data on current page
  const { data: filterData } = useGetAvailableFilterDataQuery();
  const metadataKeys = useMemo(() => {
    if (!filterData?.metadata_keys) return [];
    return Object.keys(filterData.metadata_keys).sort();
  }, [filterData?.metadata_keys]);

  const columns = useMemo(
    () => createColumns(handleDelete, hasDeleteAccess, metadataKeys),
    [handleDelete, hasDeleteAccess, metadataKeys],
  );

  // Navigation for log detail sheet
  const selectedLogIndex = useMemo(
    () => (selectedLogId ? logs.findIndex((l) => l.id === selectedLogId) : -1),
    [selectedLogId, logs],
  );

  const handleLogNavigate = useCallback(
    (direction: "prev" | "next") => {
      const currentLogId = selectedLogId || "";
      if (direction === "prev") {
        if (selectedLogIndex > 0) {
          setUrlState({ selected_log: logs[selectedLogIndex - 1].id });
        } else if (pagination.offset > 0) {
          const newOffset = Math.max(0, pagination.offset - pagination.limit);
          setUrlState({ offset: newOffset, selected_log: "" });
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
          setUrlState({ selected_log: logs[selectedLogIndex + 1].id });
        } else if (pagination.offset + pagination.limit < totalItems) {
          const newOffset = pagination.offset + pagination.limit;
          setUrlState({ offset: newOffset, selected_log: "" });
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
    [
      selectedLogId,
      selectedLogIndex,
      logs,
      pagination,
      totalItems,
      filters,
      setUrlState,
      triggerGetLogs,
    ],
  );

  return (
    <div className="dark:bg-card h-[calc(100dvh-3.3rem)] max-h-[calc(100dvh-1.5rem)] bg-white">
      {logsIsLoading && !logsData ? (
        <FullPageLoader />
      ) : showEmptyState ? (
        <EmptyState error={error} />
      ) : (
        <div className="mx-auto flex h-full w-full flex-col">
          <div className="flex flex-1 flex-col gap-2 overflow-hidden">
            {/* Quick Stats */}
            <div className="grid shrink-0 grid-cols-1 gap-4 md:grid-cols-5">
              {statCards.map((card) => (
                <Card key={card.title} className="py-4 shadow-none">
                  <CardContent className="flex items-center justify-between px-4">
                    <div className="min-w-0 w-full">
                      <div className="text-muted-foreground text-xs">{card.title}</div>
                      <div className="truncate font-mono text-xl font-medium sm:text-2xl">
                        {card.value}
                      </div>
                    </div>
                  </CardContent>
                </Card>
              ))}
            </div>

            {/* Volume Chart */}
            <div className="shrink-0">
              <LogsVolumeChart
                data={histogramData}
                loading={histogramIsFetching}
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
                loading={logsIsFetching}
                filters={filters}
                pagination={pagination}
                onFiltersChange={setFilters}
                onPaginationChange={setPagination}
                onRowClick={(row, columnId) => {
                  if (columnId === "actions") return;
                  setUrlState({ selected_log: row.id }, { history: "replace" });
                }}
                polling={polling}
                onPollToggle={handlePollToggle}
                onRefresh={handleRefresh}
                period={period}
                onPeriodChange={handlePeriodChange}
                metadataKeys={metadataKeys}
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
            hasNext={
              selectedLogIndex !== -1 &&
              (selectedLogIndex < logs.length - 1 ||
                pagination.offset + pagination.limit < totalItems)
            }
          />
        </div>
      )}
    </div>
  );
}
