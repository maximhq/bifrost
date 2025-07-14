'use client'

import { ColumnDef, flexRender, getCoreRowModel, useReactTable, SortingState } from '@tanstack/react-table'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Button } from '@/components/ui/button'
import type { LogEntry, LogFilters, Pagination } from '@/lib/types/logs'
import { RefreshCw, ChevronLeft, ChevronRight, Search, X } from 'lucide-react'
import { LogFilters as LogFiltersComponent } from './filters'
import { useState } from 'react'
interface DataTableProps {
  columns: ColumnDef<LogEntry>[]
  data: LogEntry[]
  totalItems: number
  loading?: boolean
  filters: LogFilters
  pagination: Pagination
  onFiltersChange: (filters: LogFilters) => void
  onPaginationChange: (pagination: Pagination) => void
  onRowClick?: (log: LogEntry) => void
  isSocketConnected: boolean
}

export function LogsDataTable({
  columns,
  data,
  totalItems,
  loading = false,
  filters,
  pagination,
  onFiltersChange,
  onPaginationChange,
  onRowClick,
  isSocketConnected,
}: DataTableProps) {
  const [sorting, setSorting] = useState<SortingState>([{ id: pagination.sort_by, desc: pagination.order === 'desc' }])

  const handleSortingChange = (updaterOrValue: SortingState | ((old: SortingState) => SortingState)) => {
    const newSorting = typeof updaterOrValue === 'function' ? updaterOrValue(sorting) : updaterOrValue
    setSorting(newSorting)
    if (newSorting.length > 0) {
      const { id, desc } = newSorting[0]
      onPaginationChange({
        ...pagination,
        sort_by: id as 'timestamp' | 'latency' | 'tokens',
        order: desc ? 'desc' : 'asc',
      })
    }
  }

  const table = useReactTable({
    data,
    columns,
    getCoreRowModel: getCoreRowModel(),
    manualPagination: true,
    manualSorting: true,
    manualFiltering: true,
    pageCount: Math.ceil(totalItems / pagination.limit),
    state: {
      sorting,
    },
    onSortingChange: handleSortingChange,
  })

  const currentPage = Math.floor(pagination.offset / pagination.limit) + 1
  const totalPages = Math.ceil(totalItems / pagination.limit)
  const startItem = pagination.offset + 1
  const endItem = Math.min(pagination.offset + pagination.limit, totalItems)

  const goToPage = (page: number) => {
    const newOffset = (page - 1) * pagination.limit
    onPaginationChange({
      ...pagination,
      offset: newOffset,
    })
  }

  const handlePageSizeChange = (newSize: string) => {
    const newLimit = parseInt(newSize)
    onPaginationChange({
      ...pagination,
      limit: newLimit,
      offset: 0,
    })
  }

  return (
    <div className="space-y-2">
      <LogFiltersComponent filters={filters} onFiltersChange={onFiltersChange} />
      <div className="rounded-md border">
        <Table>
          <TableHeader>
            {table.getHeaderGroups().map((headerGroup) => (
              <TableRow key={headerGroup.id}>
                {headerGroup.headers.map((header) => (
                  <TableHead key={header.id}>
                    {header.isPlaceholder ? null : flexRender(header.column.columnDef.header, header.getContext())}
                  </TableHead>
                ))}
              </TableRow>
            ))}
          </TableHeader>
          <TableBody>
            {loading ? (
              <TableRow>
                <TableCell colSpan={columns.length} className="h-24 text-center">
                  <div className="flex items-center justify-center gap-2">
                    <RefreshCw className="h-4 w-4 animate-spin" />
                    Loading logs...
                  </div>
                </TableCell>
              </TableRow>
            ) : (
              <>
                <TableRow className="hover:bg-transparent">
                  <TableCell colSpan={columns.length} className="h-12 text-center">
                    <div className="flex items-center justify-center gap-2">
                      {isSocketConnected ? (
                        <>
                          <RefreshCw className="h-4 w-4 animate-spin" />
                          Listening for logs...
                        </>
                      ) : (
                        <>
                          <X className="h-4 w-4" />
                          Not connected to socket, please refresh the page.
                        </>
                      )}
                    </div>
                  </TableCell>
                </TableRow>
                {table.getRowModel().rows.length ? (
                  table.getRowModel().rows.map((row) => (
                    <TableRow key={row.id} className="hover:bg-muted/50 h-12 cursor-pointer" onClick={() => onRowClick?.(row.original)}>
                      {row.getVisibleCells().map((cell) => (
                        <TableCell key={cell.id}>{flexRender(cell.column.columnDef.cell, cell.getContext())}</TableCell>
                      ))}
                    </TableRow>
                  ))
                ) : (
                  <TableRow>
                    <TableCell colSpan={columns.length} className="h-24 text-center">
                      No results found.
                    </TableCell>
                  </TableRow>
                )}
              </>
            )}
          </TableBody>
        </Table>
      </div>

      {/* Pagination Footer */}
      <div className="flex items-center justify-between text-xs">
        <div className="text-muted-foreground flex items-center gap-2">
          {startItem.toLocaleString()}-{endItem.toLocaleString()} of {totalItems.toLocaleString()} entries
          {/* <div className="flex items-center gap-2">
						<Label className="text-muted-foreground text-xs">Page size </Label>
						<Select value={pagination.limit.toString()} onValueChange={handlePageSizeChange}>
							<SelectTrigger className="w-16 text-xs">
								<SelectValue className="shadow-none" />
							</SelectTrigger>
							<SelectContent className="text-xs">
								<SelectItem value="10">10</SelectItem>
								<SelectItem value="25">25</SelectItem>
								<SelectItem value="50">50</SelectItem>
								<SelectItem value="100">100</SelectItem>
								<SelectItem value="250">250</SelectItem>
							</SelectContent>
						</Select>
					</div> */}
        </div>

        <div className="flex items-center gap-2">
          {/* <Button variant="outline" size="sm" onClick={() => goToPage(1)} disabled={currentPage === 1}>
						First
					</Button> */}
          <Button variant="outline" size="sm" onClick={() => goToPage(currentPage - 1)} disabled={currentPage === 1}>
            <ChevronLeft className="size-3" />
          </Button>

          <div className="flex items-center gap-1">
            <span>Page</span>
            {/* <Input
							type="number"
							min="1"
							max={totalPages}
							value={currentPage}
							onChange={(e) => {
								const page = parseInt(e.target.value);
								if (page >= 1 && page <= totalPages) {
									goToPage(page);
								}
							}}
							className="w-16 text-center"
						/> */}
            <span>{currentPage}</span>
            <span>of {totalPages}</span>
          </div>

          <Button variant="outline" size="sm" onClick={() => goToPage(currentPage + 1)} disabled={currentPage === totalPages}>
            <ChevronRight className="size-3" />
          </Button>
          {/* <Button variant="outline" size="sm" onClick={() => goToPage(totalPages)} disabled={currentPage === totalPages}>
						Last
					</Button> */}
        </div>
      </div>
    </div>
  )
}
