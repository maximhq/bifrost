"use client";

import FullPageLoader from "@/components/fullPageLoader";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Card, CardContent } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { getErrorMessage, useDeleteMCPLogsMutation } from "@/lib/store";
import {
  useGetMCPLogsQuery,
  useGetMCPLogsStatsQuery,
  useLazyGetMCPLogsQuery,
} from "@/lib/store/apis/mcpLogsApi";
import type { MCPToolLogEntry, MCPToolLogFilters, Pagination } from "@/lib/types/logs";
import { dateUtils } from "@/lib/types/logs";
import { getRangeForPeriod } from "@/lib/utils/timeRange";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { AlertCircle, CheckCircle, Clock, DollarSign, Hash } from "lucide-react";
import { useSearchParams } from "next/navigation";
import {
  parseAsArrayOf,
  parseAsBoolean,
  parseAsInteger,
  parseAsString,
  useQueryStates,
} from "nuqs";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { createMCPColumns } from "./views/columns";
import { MCPEmptyState } from "./views/emptyState";
import { MCPLogDetailSheet } from "./views/mcpLogDetailsSheet";
import { MCPLogsDataTable } from "./views/mcpLogsTable";

export default function MCPLogsPage() {
  const [error, setError] = useState<string | null>(null);
  const [showEmptyState, setShowEmptyState] = useState(false);
  const hasCheckedEmptyState = useRef(false);
  const hasDeleteAccess = useRbac(RbacResource.Logs, RbacOperation.Delete);

  // Navigation-only lazy hook
  const [triggerGetLogs] = useLazyGetMCPLogsQuery();
  const [deleteLogs] = useDeleteMCPLogsMutation();

  // Track if user has manually modified the time range
  const userModifiedTimeRange = useRef<boolean>(false);

  // Capture initial defaults on mount to detect shared URLs with custom time ranges
  const initialDefaults = useRef(dateUtils.getDefaultTimeRange());

  // Memoize default time range to prevent recalculation on every render
  const defaultTimeRange = useMemo(() => dateUtils.getDefaultTimeRange(), []);

  // Get fresh default time range for refresh logic
  const getDefaultTimeRange = () => dateUtils.getDefaultTimeRange();

  const rawSearchParams = useSearchParams();
  const hasExplicitTimeRange = rawSearchParams.has("start_time") && rawSearchParams.has("end_time");

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
      period: parseAsString
        .withDefault(hasExplicitTimeRange ? "" : "1h")
        .withOptions({ clearOnDefault: false }),
      selected_log: parseAsString.withDefault(""),
    },
    {
      history: "push",
      shallow: false,
    },
  );

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
    [
      urlState.tool_names,
      urlState.server_labels,
      urlState.status,
      urlState.virtual_key_ids,
      urlState.content_search,
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

  const polling = urlState.polling;
  const period = urlState.period;

  // RTK Query hooks with polling
  const {
    data: logsData,
    isLoading: logsIsLoading,
    isFetching: logsIsFetching,
    refetch: refetchLogs,
  } = useGetMCPLogsQuery(
    { filters, pagination },
    {
      pollingInterval: showEmptyState || polling ? 5000 : 0,
      refetchOnMountOrArgChange: true,
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
      pollingInterval: polling ? 5000 : 0,
      refetchOnMountOrArgChange: true,
      skipPollingIfUnfocused: true,
    },
  );

  const logs = logsData?.logs ?? [];
  const totalItems = logsData?.stats?.total_executions ?? 0;
  const stats = statsData ?? null;

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
          { start_time: Math.floor(from.getTime() / 1000), end_time: freshEnd },
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
      const tolerance = 5;

      if (startTimeDiff <= tolerance && endTimeDiff <= tolerance) {
        const defaults = getDefaultTimeRange();
        const currentEndDiff = Math.abs(urlState.end_time - defaults.endTime);
        if (currentEndDiff > 300) {
          setUrlState(
            {
              start_time: defaults.startTime,
              end_time: defaults.endTime,
            },
            { history: "replace" },
          );
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

  // Derive selectedLog from URL param
  const selectedLogId = urlState.selected_log || null;
  const selectedLog = useMemo(
    () => (selectedLogId ? (logs.find((l) => l.id === selectedLogId) ?? null) : null),
    [selectedLogId, logs],
  );

  // Helper to update filters in URL
  const setFilters = useCallback(
    (newFilters: MCPToolLogFilters) => {
      const timeChanged =
        newFilters.start_time !== filters.start_time || newFilters.end_time !== filters.end_time;
      if (timeChanged) userModifiedTimeRange.current = true;

      setUrlState({
        ...(timeChanged && { period: "" }),
        tool_names: newFilters.tool_names || [],
        server_labels: newFilters.server_labels || [],
        status: newFilters.status || [],
        virtual_key_ids: newFilters.virtual_key_ids || [],
        content_search: newFilters.content_search || "",
        start_time: newFilters.start_time
          ? dateUtils.toUnixTimestamp(new Date(newFilters.start_time))
          : undefined,
        end_time: newFilters.end_time
          ? dateUtils.toUnixTimestamp(new Date(newFilters.end_time))
          : undefined,
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

  const handleDelete = useCallback(
    async (log: MCPToolLogEntry) => {
      if (!hasDeleteAccess) {
        throw new Error("No delete access");
      }

      try {
        await deleteLogs({ ids: [log.id] }).unwrap();
        if (urlState.selected_log === log.id) {
          setUrlState({ selected_log: "" });
        }
        refetchLogs();
      } catch (err) {
        const errorMessage = getErrorMessage(err);
        setError(errorMessage);
        throw new Error(errorMessage);
      }
    },
    [deleteLogs, hasDeleteAccess, urlState.selected_log, setUrlState, refetchLogs],
  );

  const handleRefresh = useCallback(() => {
    if (period) {
      const { from, to } = getRangeForPeriod(period);
      setUrlState({
        start_time: Math.floor(from.getTime() / 1000),
        end_time: Math.floor(to.getTime() / 1000),
      });
    } else {
      refetchLogs();
      refetchStats();
    }
  }, [period, setUrlState, refetchLogs, refetchStats]);

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
        title: "Total Executions",
        value: statsIsFetching ? (
          <Skeleton className="h-8 w-20" />
        ) : (
          stats?.total_executions.toLocaleString() || "-"
        ),
        icon: <Hash className="size-4" />,
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

  const columns = useMemo(
    () => createMCPColumns(handleDelete, hasDeleteAccess),
    [handleDelete, hasDeleteAccess],
  );

  // Navigation for log detail sheet
  const selectedLogIndex = useMemo(
    () => (selectedLogId ? logs.findIndex((l) => l.id === selectedLogId) : -1),
    [selectedLogId, logs],
  );

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
              setUrlState(
                { offset: pagination.offset, selected_log: currentLogId },
                replaceHistory,
              );
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
              setUrlState(
                { offset: pagination.offset, selected_log: currentLogId },
                replaceHistory,
              );
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
    <div className="dark:bg-card bg-white">
      {logsIsLoading && !logsData ? (
        <FullPageLoader />
      ) : showEmptyState ? (
        <MCPEmptyState error={error} />
      ) : (
        <div className="mx-auto w-full space-y-6">
          <div className="space-y-6">
            {/* Quick Stats */}
            <div className="grid grid-cols-1 gap-4 md:grid-cols-4">
              {statCards.map((card) => (
                <Card key={card.title} className="py-4 shadow-none">
                  <CardContent className="flex items-center justify-between px-4">
                    <div className="w-full min-w-0">
                      <div className="text-muted-foreground text-xs">{card.title}</div>
                      <div className="truncate font-mono text-xl font-medium sm:text-2xl">
                        {card.value}
                      </div>
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
            />
          </div>

          {/* Log Detail Sheet */}
          <MCPLogDetailSheet
            log={selectedLog}
            open={selectedLogId !== null}
            onOpenChange={(open) =>
              !open && setUrlState({ selected_log: "" }, { history: "replace" })
            }
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
