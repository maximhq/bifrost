"use client";

import { Button } from "@/components/ui/button";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { useTablePageSize } from "@/hooks/useTablePageSize";
import type { LogEntry, LogFilters, Pagination } from "@/lib/types/logs";
import { ColumnDef, flexRender, getCoreRowModel, SortingState, useReactTable } from "@tanstack/react-table";
import { ChevronLeft, ChevronRight, Pause, RefreshCw, X } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { LogFilters as LogFiltersComponent } from "./filters";

interface DataTableProps {
	columns: ColumnDef<LogEntry>[];
	data: LogEntry[];
	totalItems: number;
	loading?: boolean;
	filters: LogFilters;
	pagination: Pagination;
	onFiltersChange: (filters: LogFilters) => void;
	onPaginationChange: (pagination: Pagination) => void;
	onRowClick?: (log: LogEntry, columnId: string) => void;
	isSocketConnected: boolean;
	liveEnabled: boolean;
	onLiveToggle: (enabled: boolean) => void;
	fetchLogs: () => Promise<void>;
	fetchStats: () => Promise<void>;
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
	liveEnabled,
	onLiveToggle,
	fetchLogs,
	fetchStats,
}: DataTableProps) {
	const [sorting, setSorting] = useState<SortingState>([{ id: pagination.sort_by, desc: pagination.order === "desc" }]);
	const tableContainerRef = useRef<HTMLDivElement>(null);
	const calculatedPageSize = useTablePageSize(tableContainerRef);

	// Refs to avoid stale closures in the page size effect
	const paginationRef = useRef(pagination);
	const onPaginationChangeRef = useRef(onPaginationChange);
	paginationRef.current = pagination;
	onPaginationChangeRef.current = onPaginationChange;

	// Update pagination limit when calculated page size increases (don't reduce on size reduction)
	useEffect(() => {
		if (calculatedPageSize && calculatedPageSize > paginationRef.current.limit) {
			onPaginationChangeRef.current({
				...paginationRef.current,
				limit: calculatedPageSize,
				offset: 0, // Reset to first page when page size changes
			});
		}
	}, [calculatedPageSize]);

	const handleSortingChange = (updaterOrValue: SortingState | ((old: SortingState) => SortingState)) => {
		const newSorting = typeof updaterOrValue === "function" ? updaterOrValue(sorting) : updaterOrValue;
		setSorting(newSorting);
		if (newSorting.length > 0) {
			const { id, desc } = newSorting[0];
			onPaginationChange({
				...pagination,
				sort_by: id as "timestamp" | "latency" | "tokens" | "cost",
				order: desc ? "desc" : "asc",
			});
		}
	};

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
	});

	const currentPage = Math.floor(pagination.offset / pagination.limit) + 1;
	const totalPages = Math.ceil(totalItems / pagination.limit);
	const startItem = pagination.offset + 1;
	const endItem = Math.min(pagination.offset + pagination.limit, totalItems);

	const goToPage = (page: number) => {
		const newOffset = (page - 1) * pagination.limit;
		onPaginationChange({
			...pagination,
			offset: newOffset,
		});
	};

	return (
		<div className="flex h-full flex-col gap-2">
			<div className="shrink-0">
				<LogFiltersComponent
					filters={filters}
					onFiltersChange={onFiltersChange}
					liveEnabled={liveEnabled}
					onLiveToggle={onLiveToggle}
					fetchLogs={fetchLogs}
					fetchStats={fetchStats}
				/>
			</div>
			
			<div ref={tableContainerRef} className="min-h-0 flex-1 overflow-hidden rounded-sm border">
				<Table containerClassName="h-full overflow-auto">
					<TableHeader className="px-2">
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
											{!isSocketConnected ? (
												<>
													<X className="h-4 w-4" />
													Not connected to socket, please refresh the page.
												</>
											) : liveEnabled ? (
												<>
													<RefreshCw className="h-4 w-4 animate-spin" />
													Listening for logs...
												</>
											) : (
												<>
													<Pause className="h-4 w-4" />
													Live updates paused
												</>
											)}
										</div>
									</TableCell>
								</TableRow>
								{table.getRowModel().rows.length ? (
									table.getRowModel().rows.map((row) => (
										<TableRow key={row.id} className="hover:bg-muted/50 h-12 cursor-pointer">
											{row.getVisibleCells().map((cell) => (
												<TableCell onClick={() => onRowClick?.(row.original, cell.column.id)} key={cell.id}>
													{flexRender(cell.column.columnDef.cell, cell.getContext())}
												</TableCell>
											))}
										</TableRow>
									))
								) : (
									<TableRow>
										<TableCell colSpan={columns.length} className="h-24 text-center">
											No results found. Try adjusting your filters and/or time range.
										</TableCell>
									</TableRow>
								)}
							</>
						)}
					</TableBody>
				</Table>
			</div>

			{/* Pagination Footer */}
			<div className="flex shrink-0 items-center justify-between text-xs">
				<div className="text-muted-foreground flex items-center gap-2">
					{startItem.toLocaleString()}-{endItem.toLocaleString()} of {totalItems.toLocaleString()} entries
				</div>

				<div className="flex items-center gap-2">
					<Button variant="ghost" size="sm" onClick={() => goToPage(currentPage - 1)} disabled={currentPage <= 1}>
						<ChevronLeft className="size-3" />
					</Button>

					<div className="flex items-center gap-1">
						<span>Page</span>
						<span>{currentPage}</span>
						<span>of {totalPages}</span>
					</div>

					<Button
						variant="ghost"
						size="sm"
						onClick={() => goToPage(currentPage + 1)}
						disabled={totalPages === 0 || currentPage >= totalPages}
					>
						<ChevronRight className="size-3" />
					</Button>
				</div>
			</div>
		</div>
	);
}
