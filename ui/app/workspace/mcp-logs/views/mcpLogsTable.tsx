"use client";

import { Button } from "@/components/ui/button";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import type { MCPToolLogEntry, MCPToolLogFilters, Pagination } from "@/lib/types/logs";
import { ColumnDef, flexRender, getCoreRowModel, SortingState, useReactTable } from "@tanstack/react-table";
import { ChevronLeft, ChevronRight, Pause, RefreshCw, X } from "lucide-react";
import { useState } from "react";
import { MCPLogFilters } from "./filters";

interface DataTableProps {
	columns: ColumnDef<MCPToolLogEntry>[];
	data: MCPToolLogEntry[];
	totalItems: number;
	loading?: boolean;
	filters: MCPToolLogFilters;
	pagination: Pagination;
	onFiltersChange: (filters: MCPToolLogFilters) => void;
	onPaginationChange: (pagination: Pagination) => void;
	onRowClick?: (log: MCPToolLogEntry, columnId: string) => void;
	isSocketConnected: boolean;
	liveEnabled: boolean;
	onLiveToggle: (enabled: boolean) => void;
	fetchLogs: () => Promise<void>;
	fetchStats: () => Promise<void>;
}

export function MCPLogsDataTable({
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

	const handleSortingChange = (updaterOrValue: SortingState | ((old: SortingState) => SortingState)) => {
		const newSorting = typeof updaterOrValue === "function" ? updaterOrValue(sorting) : updaterOrValue;
		setSorting(newSorting);
		if (newSorting.length > 0) {
			const { id, desc } = newSorting[0];
			onPaginationChange({
				...pagination,
				sort_by: id as "timestamp" | "latency",
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

	// Display values that handle the case when totalItems is 0
	const startItemDisplay = totalItems === 0 ? 0 : startItem;
	const endItemDisplay = totalItems === 0 ? 0 : endItem;

	const goToPage = (page: number) => {
		const newOffset = (page - 1) * pagination.limit;
		onPaginationChange({
			...pagination,
			offset: newOffset,
		});
	};

	return (
		<div className="space-y-2">
			<MCPLogFilters filters={filters} onFiltersChange={onFiltersChange} liveEnabled={liveEnabled} onLiveToggle={onLiveToggle} />
			<div className="max-h-[calc(100vh-16.5rem)] rounded-sm border">
				<Table containerClassName="max-h-[calc(100vh-16.5rem)]">
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
			<div className="flex items-center justify-between text-xs">
				<div className="text-muted-foreground flex items-center gap-2">
					{startItemDisplay.toLocaleString()}-{endItemDisplay.toLocaleString()} of {totalItems.toLocaleString()} entries
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
