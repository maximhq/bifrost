'use client'

import { ColumnDef } from '@tanstack/react-table'
import { LogEntry } from '@/lib/types/logs'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ArrowUpDown } from 'lucide-react'
import { STATUS_COLORS, Provider, Status, REQUEST_TYPE_LABELS, REQUEST_TYPE_COLORS } from '@/lib/constants/logs'
import { renderProviderIcon, ProviderIconType } from '@/lib/constants/icons'

export const createColumns = (): ColumnDef<LogEntry>[] => [
  {
    accessorKey: 'timestamp',
    header: ({ column }) => (
      <Button variant="ghost" onClick={() => column.toggleSorting(column.getIsSorted() === 'asc')}>
        Time
        <ArrowUpDown className="ml-2 h-4 w-4" />
      </Button>
    ),
    cell: ({ row }) => {
      const timestamp = row.original.timestamp
      return <div className="font-mono text-sm">{new Date(timestamp).toLocaleString()}</div>
    },
  },
  {
    accessorKey: 'provider',
    header: 'Provider',
    cell: ({ row }) => {
      const provider = row.original.provider as Provider
      return (
        <Badge variant="secondary" className={`uppercase`}>
          {renderProviderIcon(provider as ProviderIconType, { size: 'sm' })}
          {provider}
        </Badge>
      )
    },
  },
  {
    accessorKey: 'model',
    header: 'Model',
    cell: ({ row }) => <div className="max-w-[240px] truncate text-sm font-medium">{row.original.model}</div>,
  },
  {
    accessorKey: 'status',
    header: 'Status',
    cell: ({ row }) => {
      const status = row.original.status as Status
      return (
        <Badge variant="secondary" className={STATUS_COLORS[status]}>
          {status}
        </Badge>
      )
    },
  },
  {
    accessorKey: 'latency',
    header: ({ column }) => (
      <Button variant="ghost" onClick={() => column.toggleSorting(column.getIsSorted() === 'asc')}>
        Latency
        <ArrowUpDown className="ml-2 h-4 w-4" />
      </Button>
    ),
    cell: ({ row }) => {
      const latency = row.original.latency
      return <div className="font-mono text-sm">{latency ? `${latency.toLocaleString()}ms` : 'N/A'}</div>
    },
  },
  {
    accessorKey: 'token_usage.total_tokens',
    header: ({ column }) => (
      <Button variant="ghost" onClick={() => column.toggleSorting(column.getIsSorted() === 'asc')}>
        Tokens
        <ArrowUpDown className="ml-2 h-4 w-4" />
      </Button>
    ),
    cell: ({ row }) => {
      const tokenUsage = row.original.token_usage
      if (!tokenUsage) {
        return <div className="font-mono text-sm">N/A</div>
      }

      return (
        <div className="text-sm">
          <div className="font-mono">{tokenUsage.total_tokens.toLocaleString()}</div>
          <div className="text-muted-foreground text-xs">
            {tokenUsage.prompt_tokens}+{tokenUsage.completion_tokens}
          </div>
        </div>
      )
    },
  },
  {
    id: 'request_type',
    header: 'Type',
    cell: ({ row }) => {
      return (
        <Badge variant="outline" className={`${REQUEST_TYPE_COLORS[row.original.object as keyof typeof REQUEST_TYPE_COLORS]} text-xs`}>
          {REQUEST_TYPE_LABELS[row.original.object as keyof typeof REQUEST_TYPE_LABELS]}
        </Badge>
      )
    },
  },
]
