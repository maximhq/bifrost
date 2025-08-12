'use client'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ProviderIconType, renderProviderIcon } from '@/lib/constants/icons'
import { Provider, REQUEST_TYPE_COLORS, REQUEST_TYPE_LABELS, Status, STATUS_COLORS } from '@/lib/constants/logs'
import { LogEntry } from '@/lib/types/logs'
import { ColumnDef } from '@tanstack/react-table'
import { ArrowUpDown } from 'lucide-react'

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
      return <div className="font-mono text-xs">{new Date(timestamp).toLocaleString()}</div>
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
  {
    accessorKey: 'provider',
    header: 'Provider',
    cell: ({ row }) => {
      const provider = row.original.provider as Provider
      return (
        <Badge variant="secondary" className={`uppercase font-mono`}>
          {renderProviderIcon(provider as ProviderIconType, { size: 'sm' })}
          {provider}
        </Badge>
      )
    },
  },
  {
    accessorKey: 'model',
    header: 'Model',
    cell: ({ row }) => <div className="max-w-[240px] truncate text-xs font-normal font-mono">{row.original.model}</div>,
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
      return <div className="font-mono text-xs pl-4">{latency ? `${latency.toLocaleString()}ms` : 'N/A'}</div>
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
        return <div className="font-mono text-xs pl-4">N/A</div>
      }

      return (
        <div className="text-xs pl-4">
          <div className="font-mono">{tokenUsage.total_tokens.toLocaleString()} ({tokenUsage.prompt_tokens}+{tokenUsage.completion_tokens})</div>          
        </div>
      )
    },
  },
  
  {
    accessorKey: 'status',
    header: 'Status',
    cell: ({ row }) => {
      const status = row.original.status as Status
      return (
        <Badge variant="secondary" className={`${STATUS_COLORS[status]} font-mono`}>
          {status}
        </Badge>
      )
    },
  },
]
