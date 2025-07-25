'use client'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Skeleton } from '@/components/ui/skeleton'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Badge } from '@/components/ui/badge'
import { apiService } from '@/lib/api'
import { AuditLog } from '@/lib/types/governance'
import { FileText, Search, RefreshCw, ChevronLeft, ChevronRight } from 'lucide-react'
import { useState, useEffect } from 'react'
import { toast } from 'sonner'

interface AuditLogsSectionProps {
  auditLogs: AuditLog[]
  onRefresh: () => void
  loading: boolean
}

export default function AuditLogsSection({ auditLogs: initialLogs, onRefresh, loading }: AuditLogsSectionProps) {
  const [auditLogs, setAuditLogs] = useState<AuditLog[]>(initialLogs)
  const [filters, setFilters] = useState({
    action: '',
    vkId: '',
    ruleId: '',
  })
  const [pagination, setPagination] = useState({
    limit: 50,
    offset: 0,
    total: 0,
  })
  const [localLoading, setLocalLoading] = useState(false)

  useEffect(() => {
    setAuditLogs(initialLogs)
  }, [initialLogs])

  const fetchAuditLogs = async (newFilters = filters, newPagination = pagination) => {
    setLocalLoading(true)
    try {
      const params: any = {
        limit: newPagination.limit,
        offset: newPagination.offset,
      }

      if (newFilters.action) params.action = newFilters.action
      if (newFilters.vkId) params.vk_id = newFilters.vkId
      if (newFilters.ruleId) params.rule_id = newFilters.ruleId

      const [result, error] = await apiService.getAuditLogs(params)

      if (error) {
        toast.error(`Failed to load audit logs: ${error}`)
      } else if (result) {
        setAuditLogs(result.audit_logs)
        setPagination((prev) => ({
          ...prev,
          total: result.pagination.total,
          offset: result.pagination.offset,
        }))
      }
    } catch (error) {
      toast.error('Failed to load audit logs')
    } finally {
      setLocalLoading(false)
    }
  }

  const handleFilterChange = (key: string, value: string) => {
    const newFilters = { ...filters, [key]: value }
    setFilters(newFilters)
    const newPagination = { ...pagination, offset: 0 }
    setPagination(newPagination)
    fetchAuditLogs(newFilters, newPagination)
  }

  const handlePageChange = (newOffset: number) => {
    const newPagination = { ...pagination, offset: newOffset }
    setPagination(newPagination)
    fetchAuditLogs(filters, newPagination)
  }

  const formatTimestamp = (timestamp: string) => {
    return new Date(timestamp).toLocaleString()
  }

  const getStatusColor = (status: string) => {
    switch (status.toLowerCase()) {
      case 'success':
      case 'allowed':
        return 'default'
      case 'blocked':
      case 'denied':
      case 'error':
        return 'destructive'
      case 'warning':
        return 'secondary'
      default:
        return 'outline'
    }
  }

  const clearFilters = () => {
    const newFilters = { action: '', vkId: '', ruleId: '' }
    const newPagination = { ...pagination, offset: 0 }
    setFilters(newFilters)
    setPagination(newPagination)
    fetchAuditLogs(newFilters, newPagination)
  }

  const currentPage = Math.floor(pagination.offset / pagination.limit) + 1
  const totalPages = Math.ceil(pagination.total / pagination.limit)
  const hasNextPage = pagination.offset + pagination.limit < pagination.total
  const hasPrevPage = pagination.offset > 0

  if (loading) {
    return (
      <div className="space-y-4">
        <div className="flex items-center justify-between">
          <div>
            <Skeleton className="h-8 w-48" />
            <Skeleton className="mt-2 h-4 w-96" />
          </div>
          <Skeleton className="h-10 w-32" />
        </div>
        <div className="grid grid-cols-1 gap-4 md:grid-cols-4">
          {Array.from({ length: 4 }).map((_, i) => (
            <Skeleton key={i} className="h-10" />
          ))}
        </div>
        <div className="rounded-md border">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Timestamp</TableHead>
                <TableHead>Action</TableHead>
                <TableHead>Virtual Key</TableHead>
                <TableHead>Status</TableHead>
                <TableHead>Details</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {Array.from({ length: 10 }).map((_, i) => (
                <TableRow key={i}>
                  <TableCell>
                    <Skeleton className="h-4 w-32" />
                  </TableCell>
                  <TableCell>
                    <Skeleton className="h-4 w-20" />
                  </TableCell>
                  <TableCell>
                    <Skeleton className="h-4 w-24" />
                  </TableCell>
                  <TableCell>
                    <Skeleton className="h-4 w-16" />
                  </TableCell>
                  <TableCell>
                    <Skeleton className="h-4 w-40" />
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      </div>
    )
  }

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <div>
          <h3 className="flex items-center gap-2 text-lg font-semibold">
            <FileText className="h-5 w-5" />
            Audit Logs
          </h3>
          <p className="text-muted-foreground text-sm">Track all governance-related actions and access attempts.</p>
        </div>
        <Button onClick={() => fetchAuditLogs()} disabled={localLoading}>
          <RefreshCw className={`h-4 w-4 ${localLoading ? 'animate-spin' : ''}`} />
          Refresh
        </Button>
      </div>

      {/* Filters */}
      <div className="grid grid-cols-1 gap-4 md:grid-cols-4">
        <Select value={filters.action} onValueChange={(value) => handleFilterChange('action', value)}>
          <SelectTrigger>
            <SelectValue placeholder="Filter by action" />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">All actions</SelectItem>
            <SelectItem value="request">Request</SelectItem>
            <SelectItem value="block">Block</SelectItem>
            <SelectItem value="allow">Allow</SelectItem>
            <SelectItem value="rate_limit">Rate Limit</SelectItem>
            <SelectItem value="budget_check">Budget Check</SelectItem>
          </SelectContent>
        </Select>

        <Input placeholder="Filter by Virtual Key ID" value={filters.vkId} onChange={(e) => handleFilterChange('vkId', e.target.value)} />

        <Input placeholder="Filter by Rule ID" value={filters.ruleId} onChange={(e) => handleFilterChange('ruleId', e.target.value)} />

        <Button variant="outline" onClick={clearFilters}>
          Clear Filters
        </Button>
      </div>

      {/* Audit Logs Table */}
      <div className="rounded-md border">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Timestamp</TableHead>
              <TableHead>Action</TableHead>
              <TableHead>Virtual Key</TableHead>
              {/* <TableHead>Status</TableHead> */}
              <TableHead>Details</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {auditLogs.length === 0 ? (
              <TableRow>
                <TableCell colSpan={5} className="text-muted-foreground py-8 text-center">
                  {Object.values(filters).some((f) => f) ? 'No audit logs found matching the current filters.' : 'No audit logs available.'}
                </TableCell>
              </TableRow>
            ) : (
              auditLogs.map((log) => (
                <TableRow key={log.id}>
                  <TableCell>
                    <span className="font-mono text-sm">{formatTimestamp(log.timestamp)}</span>
                  </TableCell>
                  <TableCell>
                    <Badge variant="outline">{log.action}</Badge>
                  </TableCell>
                  <TableCell>
                    {log.vk_id ? (
                      <code className="bg-muted rounded px-2 py-1 font-mono text-sm">{log.vk_id.substring(0, 8)}...</code>
                    ) : (
                      <span className="text-muted-foreground">N/A</span>
                    )}
                  </TableCell>
                  {/* <TableCell>
                    <Badge variant={getStatusColor(log.status)}>{log.status}</Badge>
                  </TableCell> */}
                  <TableCell>
                    <div className="max-w-xs truncate text-sm">{log.details || 'No details available'}</div>
                  </TableCell>
                </TableRow>
              ))
            )}
          </TableBody>
        </Table>
      </div>

      {/* Pagination */}
      {totalPages > 1 && (
        <div className="flex items-center justify-between">
          <p className="text-muted-foreground text-sm">
            Showing {pagination.offset + 1} to {Math.min(pagination.offset + pagination.limit, pagination.total)} of {pagination.total}{' '}
            entries
          </p>
          <div className="flex items-center gap-2">
            <Button
              variant="outline"
              size="sm"
              onClick={() => handlePageChange(pagination.offset - pagination.limit)}
              disabled={!hasPrevPage || localLoading}
            >
              <ChevronLeft className="h-4 w-4" />
              Previous
            </Button>
            <span className="text-sm">
              Page {currentPage} of {totalPages}
            </span>
            <Button
              variant="outline"
              size="sm"
              onClick={() => handlePageChange(pagination.offset + pagination.limit)}
              disabled={!hasNextPage || localLoading}
            >
              Next
              <ChevronRight className="h-4 w-4" />
            </Button>
          </div>
        </div>
      )}
    </div>
  )
}
