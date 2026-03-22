"use client";

import { Button } from "@/components/ui/button";
import { buildPinStyle, ColumnConfigDropdown, DraggableColumnHeader, PIN_SHADOW_LEFT, PIN_SHADOW_RIGHT, useColumnConfig, useHeaderCellRefs, usePinOffsets } from "@/components/table";
import { Table, TableBody, TableCell, TableRow } from "@/components/ui/table";
import type { MCPToolLogEntry, MCPToolLogFilters, Pagination } from "@/lib/types/logs";
import { cn } from "@/lib/utils";
import { ColumnDef, flexRender, getCoreRowModel, SortingState, useReactTable } from "@tanstack/react-table";
import { ChevronLeft, ChevronRight, Pause, RefreshCw, X } from "lucide-react";
import { useCallback, useMemo, useState } from "react";
import { MCPLogFilters } from "./filters";

const COLUMN_LABELS: Record<string, string> = {
	timestamp: "Time",
	tool_name: "Tool Name",
	server_label: "Server",
	latency: "Latency",
	cost: "Cost",
};

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
}: DataTableProps) {
	const [sorting, setSorting] = useState<SortingState>([{ id: pagination.sort_by, desc: pagination.order === "desc" }]);

	// Derive all column IDs from column definitions
	const columnIds = useMemo(
		() => columns.map((col) => ("id" in col && col.id ? col.id : "accessorKey" in col ? String(col.accessorKey) : "")).filter(Boolean),
		[columns],
	);

	const fixedColumnIds = useMemo(() => new Set(["status", "actions"]), []);

	// Column config: order, visibility, pinning — persisted in URL
	const { entries, columnOrder, columnVisibility, columnPinning, toggleVisibility, togglePin, reorder, reset } = useColumnConfig({
		columnIds,
		paramName: "mcp_cols",
		fixedColumns: { left: ["status"], right: ["actions"] },
	});

	// Measure actual header cell widths for pixel-perfect pin offsets
	const { headerCellRefs, setHeaderCellRef } = useHeaderCellRefs();
	const pinOffsets = usePinOffsets(headerCellRefs, columnPinning);

	// Shadow on the edge of pinned groups
	const lastLeftPinId = columnPinning.left?.at(-1);
	const firstRightPinId = columnPinning.right?.at(0);

	// Handle native drag-and-drop reorder
	const handleColumnDrop = useCallback(
		(draggedId: string, targetId: string) => {
			const newEntries = [...entries];
			const draggedIdx = newEntries.findIndex((e) => e.id === draggedId);
			const targetIdx = newEntries.findIndex((e) => e.id === targetId);
			if (draggedIdx === -1 || targetIdx === -1) return;
			const [moved] = newEntries.splice(draggedIdx, 1);
			newEntries.splice(targetIdx, 0, moved);
			reorder(newEntries);
		},
		[entries, reorder],
	);

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
			columnOrder,
			columnVisibility,
			columnPinning,
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
			<div className="flex items-center gap-2">
				<div className="flex-1">
					<MCPLogFilters filters={filters} onFiltersChange={onFiltersChange} liveEnabled={liveEnabled} onLiveToggle={onLiveToggle} />
				</div>
				<ColumnConfigDropdown
					entries={entries}
					labels={COLUMN_LABELS}
					onToggleVisibility={toggleVisibility}
					onReset={reset}
				/>
			</div>
			<div className="max-h-[calc(100vh-16.5rem)] rounded-sm border">
				<Table containerClassName="max-h-[calc(100vh-16.5rem)]">
					<thead className={cn("sticky top-0 z-10 bg-[#f9f9f9] dark:bg-[#27272a] px-2 [&_tr]:border-b")}>
						{table.getHeaderGroups().map((headerGroup) => (
							<tr
								key={headerGroup.id}
								className="hover:bg-muted/50 dark:hover:bg-muted/75 data-[state=selected]:bg-muted border-b transition-colors"
							>
								{headerGroup.headers.map((header) => (
									<DraggableColumnHeader
										key={header.id}
										header={header}
										isConfigurable={!fixedColumnIds.has(header.column.id)}
										pinStyle={buildPinStyle(header.column, pinOffsets)}
										pinnedHeaderClassName="bg-[#f9f9f9] dark:bg-[#27272a]"
										className={cn(
											header.column.id === lastLeftPinId && PIN_SHADOW_LEFT,
											header.column.id === firstRightPinId && PIN_SHADOW_RIGHT,
										)}
										onHide={toggleVisibility}
										onPin={togglePin}
										onDrop={handleColumnDrop}
										cellRef={setHeaderCellRef(header.column.id)}
									/>
								))}
							</tr>
						))}
					</thead>
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
										<TableRow key={row.id} className="hover:bg-muted/50 h-12 cursor-pointer group/table-row">
											{row.getVisibleCells().map((cell) => {
												const pinned = cell.column.getIsPinned();
												return (
													<TableCell
														onClick={() => onRowClick?.(row.original, cell.column.id)}
														key={cell.id}
														style={buildPinStyle(cell.column, pinOffsets)}
														className={cn(
															pinned && "bg-card",
															cell.column.id === lastLeftPinId && PIN_SHADOW_LEFT,
															cell.column.id === firstRightPinId && PIN_SHADOW_RIGHT,
															"group-hover/table-row:bg-[#f7f7f7] dark:group-hover/table-row:bg-[#232327]",
														)}
													>
														{flexRender(cell.column.columnDef.cell, cell.getContext())}
													</TableCell>
												);
											})}
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
			<div className="flex items-center justify-between text-xs" data-testid="pagination">
				<div className="text-muted-foreground flex items-center gap-2">
					{startItemDisplay.toLocaleString()}-{endItemDisplay.toLocaleString()} of {totalItems.toLocaleString()} entries
				</div>

				<div className="flex items-center gap-2">
					<Button
						variant="ghost"
						size="sm"
						onClick={() => goToPage(currentPage - 1)}
						disabled={currentPage <= 1}
						data-testid="prev-page"
						aria-label="Previous page"
					>
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
						data-testid="next-page"
						aria-label="Next page"
					>
						<ChevronRight className="size-3" />
					</Button>
				</div>
			</div>
		</div>
	);
}
