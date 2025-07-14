'use client'

import FullPageLoader from '@/components/full-page-loader'
import { createColumns } from '@/components/logs/columns'
import { EmptyState } from '@/components/logs/empty-state'
import { LogDetailSheet } from '@/components/logs/log-detail-sheet'
import { LogsDataTable } from '@/components/logs/logs-table'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Card, CardContent } from '@/components/ui/card'
import { useWebSocket } from '@/hooks/useWebSocket'
import { apiService } from '@/lib/api'
import type { BifrostMessage, ContentBlock, LogEntry, LogFilters, LogStats, MessageContent, Pagination } from '@/lib/types/logs'
import { AlertCircle, BarChart, CheckCircle, Clock, Hash } from 'lucide-react'
import { useCallback, useEffect, useMemo, useState, useRef } from 'react'

export default function LogsPage() {
  const [logs, setLogs] = useState<LogEntry[]>([])
  const [totalItems, setTotalItems] = useState(0) // changes with filters
  const [stats, setStats] = useState<LogStats | null>(null)
  const [initialLoading, setInitialLoading] = useState(true) // on initial load
  const [fetchingLogs, setFetchingLogs] = useState(false) // on pagination/filters change
  const [error, setError] = useState<string | null>(null)
  const [showEmptyState, setShowEmptyState] = useState(false)

  const [selectedLog, setSelectedLog] = useState<LogEntry | null>(null)

  // Debouncing for streaming updates (client-side)
  const streamingUpdateTimeouts = useRef<Map<string, NodeJS.Timeout>>(new Map())

  const [filters, setFilters] = useState<LogFilters>({
    providers: [],
    models: [],
    status: [],
    content_search: '',
  })
  const [pagination, setPagination] = useState<Pagination>({
    limit: 50,
    offset: 0,
    sort_by: 'timestamp',
    order: 'desc',
  })

  const handleLogMessage = useCallback(
    (log: LogEntry, operation: 'create' | 'update') => {
      // If we were in empty state, exit it since we now have logs
      if (showEmptyState) {
        setShowEmptyState(false)
      }

      if (operation === 'create') {
        // Handle new log creation
        // Only prepend the new log if we're on the first page and sorted by timestamp desc
        if (pagination.offset === 0 && pagination.sort_by === 'timestamp' && pagination.order === 'desc') {
          // Check if the log matches current filters
          if (!matchesFilters(log, filters)) {
            return
          }

          setLogs((prevLogs: LogEntry[]) => {
            // Check if log already exists (prevent duplicates)
            if (prevLogs.some((existingLog) => existingLog.id === log.id)) {
              return prevLogs
            }

            // Remove the last log if we're at the page limit
            const updatedLogs = [log, ...prevLogs]
            if (updatedLogs.length > pagination.limit) {
              updatedLogs.pop()
            }
            return updatedLogs
          })

          // Update selectedLog if it matches (for detail sheet real-time updates)
          setSelectedLog((prevSelectedLog) => {
            if (prevSelectedLog && prevSelectedLog.id === log.id) {
              return log
            }
            return prevSelectedLog
          })

          setTotalItems((prev: number) => prev + 1)

          setStats((prevStats) => {
            if (!prevStats) return prevStats

            const newStats = { ...prevStats }
            newStats.total_requests += 1

            // Update success rate
            const successCount = (prevStats.success_rate / 100) * prevStats.total_requests
            const newSuccessCount = log.status === 'success' ? successCount + 1 : successCount
            newStats.success_rate = (newSuccessCount / newStats.total_requests) * 100

            // Update average latency
            if (log.latency) {
              const totalLatency = prevStats.average_latency * prevStats.total_requests
              newStats.average_latency = (totalLatency + log.latency) / newStats.total_requests
            }

            // Update total tokens
            if (log.token_usage) {
              newStats.total_tokens += log.token_usage.total_tokens
            }

            return newStats
          })
        }
      } else if (operation === 'update') {
        // Handle log updates with debouncing for streaming

        // Check if the log exists in our current list
        const logExists = logs.some((existingLog) => existingLog.id === log.id)

        if (!logExists) {
          // Fallback: if log doesn't exist, treat as create (e.g., user was on different page when created)
          if (pagination.offset === 0 && pagination.sort_by === 'timestamp' && pagination.order === 'desc') {
            // Check if the log matches current filters
            if (matchesFilters(log, filters)) {
              setLogs((prevLogs: LogEntry[]) => {
                // Double-check it doesn't exist (race condition protection)
                if (prevLogs.some((existingLog) => existingLog.id === log.id)) {
                  return prevLogs.map((existingLog) => (existingLog.id === log.id ? log : existingLog))
                }

                // Add as new log
                const updatedLogs = [log, ...prevLogs]
                if (updatedLogs.length > pagination.limit) {
                  updatedLogs.pop()
                }
                return updatedLogs
              })
            }
          }
        } else {
          // Normal update flow for existing logs
          if (log.stream) {
            // For streaming logs, debounce updates to avoid UI thrashing
            const existingTimeout = streamingUpdateTimeouts.current.get(log.id)
            if (existingTimeout) {
              clearTimeout(existingTimeout)
            }

            const timeout = setTimeout(() => {
              updateExistingLog(log)
              streamingUpdateTimeouts.current.delete(log.id)
            }, 100) // 100ms debounce for streaming updates

            streamingUpdateTimeouts.current.set(log.id, timeout)
          } else {
            // For non-streaming updates, update immediately
            updateExistingLog(log)
          }
        }
      }
    },
    [pagination.offset, pagination.sort_by, pagination.order, pagination.limit, filters, showEmptyState, logs],
  )

  const updateExistingLog = useCallback((updatedLog: LogEntry) => {
    setLogs((prevLogs: LogEntry[]) => {
      return prevLogs.map((existingLog) => (existingLog.id === updatedLog.id ? updatedLog : existingLog))
    })

    // Update selectedLog if it matches the updated log (for real-time detail sheet updates)
    setSelectedLog((prevSelectedLog) => {
      if (prevSelectedLog && prevSelectedLog.id === updatedLog.id) {
        return updatedLog
      }
      return prevSelectedLog
    })

    // Update stats for completed requests
    if (updatedLog.status === 'success' || updatedLog.status === 'error') {
      setStats((prevStats) => {
        if (!prevStats) return prevStats

        const newStats = { ...prevStats }

        // Recalculate success rate (this is an approximation)
        const successCount = (prevStats.success_rate / 100) * prevStats.total_requests
        const newSuccessCount = updatedLog.status === 'success' ? successCount + 1 : successCount
        newStats.success_rate = (newSuccessCount / newStats.total_requests) * 100

        // Update average latency
        if (updatedLog.latency) {
          // This is an approximation - for exact stats, we'd need to refetch
          const totalLatency = prevStats.average_latency * prevStats.total_requests
          newStats.average_latency = (totalLatency + updatedLog.latency) / newStats.total_requests
        }

        // Update total tokens
        if (updatedLog.token_usage) {
          newStats.total_tokens += updatedLog.token_usage.total_tokens
        }

        return newStats
      })
    }
  }, [])

  const { isConnected: isSocketConnected, setMessageHandler } = useWebSocket()

  // Set up the message handler when the component mounts
  useEffect(() => {
    setMessageHandler(handleLogMessage)
  }, [handleLogMessage, setMessageHandler])

  // Cleanup timeouts on unmount
  useEffect(() => {
    return () => {
      streamingUpdateTimeouts.current.forEach((timeout) => clearTimeout(timeout))
      streamingUpdateTimeouts.current.clear()
    }
  }, [])

  const fetchLogs = useCallback(async () => {
    setFetchingLogs(true)
    setError(null)

    try {
      const [response, err] = await apiService.getLogs(filters, pagination)

      if (err) {
        setError(err)
        setLogs([])
        setTotalItems(0)
      } else if (response) {
        setLogs(response.logs || [])
        setTotalItems(response.stats.total_requests)
        setStats(response.stats)
      }

      // Only set showEmptyState on initial load and only based on total logs
      if (initialLoading) {
        // Check if there are any logs globally, not just in the current filter
        setShowEmptyState(response ? response.stats.total_requests === 0 : true)
      }
    } catch {
      setError('Cannot fetch logs. Please check if logs are enabled in your Bifrost config.')
      setLogs([])
      setTotalItems(0)
      setShowEmptyState(true)
    } finally {
      setFetchingLogs(false)
    }
  }, [filters, pagination, initialLoading])

  // Fetch logs when filters or pagination change
  useEffect(() => {
    if (!initialLoading) fetchLogs()
  }, [fetchLogs, initialLoading])

  useEffect(() => {
    fetchLogs()
    setInitialLoading(false)
  }, [])

  const getMessageText = (content: MessageContent): string => {
    if (typeof content === 'string') {
      return content
    }
    if (Array.isArray(content)) {
      return content.reduce((acc: string, block: ContentBlock) => {
        if (block.type === 'text' && block.text) {
          return acc + block.text
        }
        return acc
      }, '')
    }
    return ''
  }

  // Helper function to check if a log matches the current filters
  const matchesFilters = (log: LogEntry, filters: LogFilters): boolean => {
    if (filters.providers?.length && !filters.providers.includes(log.provider)) {
      return false
    }
    if (filters.models?.length && !filters.models.includes(log.model)) {
      return false
    }
    if (filters.status?.length && !filters.status.includes(log.status)) {
      return false
    }
    if (filters.start_time && new Date(log.timestamp) < new Date(filters.start_time)) {
      return false
    }
    if (filters.end_time && new Date(log.timestamp) > new Date(filters.end_time)) {
      return false
    }
    if (filters.min_latency && (!log.latency || log.latency < filters.min_latency)) {
      return false
    }
    if (filters.max_latency && (!log.latency || log.latency > filters.max_latency)) {
      return false
    }
    if (filters.min_tokens && (!log.token_usage || log.token_usage.total_tokens < filters.min_tokens)) {
      return false
    }
    if (filters.max_tokens && (!log.token_usage || log.token_usage.total_tokens > filters.max_tokens)) {
      return false
    }
    if (filters.content_search) {
      const search = filters.content_search.toLowerCase()
      const content = [
        ...(log.input_history || []).map((msg: BifrostMessage) => getMessageText(msg.content)),
        log.output_message ? getMessageText(log.output_message.content) : '',
      ]
        .join(' ')
        .toLowerCase()

      if (!content.includes(search)) {
        return false
      }
    }
    return true
  }

  const statCards = useMemo(
    () => [
      {
        title: 'Total Requests',
        value: stats?.total_requests.toLocaleString() || '-',
        icon: <BarChart className="size-4" />,
      },
      {
        title: 'Success Rate',
        value: stats ? `${stats.success_rate.toFixed(2)}%` : '-',
        icon: <CheckCircle className="size-4" />,
      },
      {
        title: 'Avg Latency',
        value: stats ? `${stats.average_latency.toFixed(2)}ms` : '-',
        icon: <Clock className="size-4" />,
      },
      {
        title: 'Total Tokens',
        value: stats?.total_tokens.toLocaleString() || '-',
        icon: <Hash className="size-4" />,
      },
    ],
    [stats],
  )

  const columns = createColumns()

  return (
    <div className="bg-background">
      {initialLoading ? (
        <FullPageLoader />
      ) : showEmptyState ? (
        <EmptyState isSocketConnected={isSocketConnected} error={error} />
      ) : (
        <div className="space-y-6">
          <div>
            <h1 className="mb-2 text-3xl font-bold">Request Logs</h1>
            <p className="text-muted-foreground">Monitor and analyze all API requests and responses</p>
          </div>

          <div className="space-y-6">
            {/* Quick Stats */}
            <div className="grid grid-cols-1 gap-4 md:grid-cols-4">
              {statCards.map((card) => (
                <Card key={card.title} className="py-4">
                  <CardContent className="flex items-center justify-between px-4">
                    <div>
                      <div className="text-muted-foreground text-xs">{card.title}</div>
                      <div className="text-2xl font-bold">{card.value}</div>
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

            <LogsDataTable
              columns={columns}
              data={logs}
              totalItems={totalItems}
              loading={fetchingLogs}
              filters={filters}
              pagination={pagination}
              onFiltersChange={setFilters}
              onPaginationChange={setPagination}
              onRowClick={setSelectedLog}
              isSocketConnected={isSocketConnected}
            />
          </div>

          {/* Log Detail Sheet */}
          <LogDetailSheet log={selectedLog} open={selectedLog !== null} onOpenChange={(open) => !open && setSelectedLog(null)} />
        </div>
      )}
    </div>
  )
}
