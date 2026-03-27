"use client";

import { Button } from "@/components/ui/button";
import { buildPinStyle, ColumnConfigDropdown, DraggableColumnHeader, PIN_SHADOW_LEFT, PIN_SHADOW_RIGHT, useColumnConfig, useHeaderCellRefs, usePinOffsets } from "@/components/table";
import { Table, TableBody, TableCell, TableRow } from "@/components/ui/table";
import { useTablePageSize } from "@/hooks/useTablePageSize";
import type { LogEntry, LogFilters, Pagination } from "@/lib/types/logs";
import { cn } from "@/lib/utils";
import { ChevronLeft, ChevronRight, Pause, RefreshCw, X } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { ColumnDef, flexRender, getCoreRowModel, SortingState, useReactTable } from "@tanstack/react-table";
import { LogFilters as LogFiltersComponent } from "./filters";

const COLUMN_LABELS: Record<string, string> = {
	timestamp: "Time",
	request_type: "Type",
	input: "Message",
	provider: "Provider",
	model: "Model",
	latency: "Latency",
	tokens: "Tokens",
	cost: "Cost",
};

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

	const columnIds = useMemo(
		() => columns.map((col) => ("id" in col && col.id ? col.id : "accessorKey" in col ? String(col.accessorKey) : "")).filter(Boolean),
		[columns],
	);

	const fixedColumnIds = useMemo(() => new Set<string>([]), []);

	// Column config: order, visibility, pinning — persisted in URL
	const { entries, columnOrder, columnVisibility, columnPinning, toggleVisibility, togglePin, reorder, reset } = useColumnConfig({
		columnIds,
		paramName: "cols",
	});

	// Measure actual header cell widths for pixel-perfect pin offsets
	const { headerCellRefs, setHeaderCellRef } = useHeaderCellRefs();
	const pinOffsets = usePinOffsets(headerCellRefs, columnPinning);

	// Shadow on the edge of pinned groups
	const lastLeftPinId = columnPinning.left?.at(-1);
	const firstRightPinId = columnPinning.right?.at(0);

	const columnLabels = useMemo(() => ({ ...COLUMN_LABELS }), []);

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

	// Refs to avoid stale closures in the page size effect
	const paginationRef = useRef(pagination);
	const onPaginationChangeRef = useRef(onPaginationChange);
	paginationRef.current = pagination;
	onPaginationChangeRef.current = onPaginationChange;

	useEffect(() => {
		if (calculatedPageSize && calculatedPageSize > paginationRef.current.limit) {
			onPaginationChangeRef.current({
				...paginationRef.current,
				limit: calculatedPageSize,
				offset: 0,
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

	const goToPage = (page: number) => {
		const newOffset = (page - 1) * pagination.limit;
		onPaginationChange({
			...pagination,
			offset: newOffset,
		});
	};

	return (
		<div className="flex h-full flex-col gap-2">
			<div className="flex shrink-0 items-center gap-2">
				<div className="flex-1">
					<LogFiltersComponent
						filters={filters}
						onFiltersChange={onFiltersChange}
						liveEnabled={liveEnabled}
						onLiveToggle={onLiveToggle}
						fetchLogs={fetchLogs}
						fetchStats={fetchStats}
					/>
				</div>
				<ColumnConfigDropdown
					entries={entries}
					labels={columnLabels}
					onToggleVisibility={toggleVisibility}
					onReset={reset}
				/>
			</div>

			<div ref={tableContainerRef} className="min-h-0 flex-1 overflow-hidden rounded-sm border">
				<Table containerClassName="h-full overflow-auto">
					<thead className={cn("[&_tr]:border-b px-2 sticky top-0 z-10 bg-[#f9f9f9] dark:bg-[#27272a]")}>
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
			<div className="flex shrink-0 items-center justify-between text-xs" data-testid="pagination">
				<div className="text-muted-foreground flex items-center gap-2">
					{startItem.toLocaleString()}-{endItem.toLocaleString()} of {totalItems.toLocaleString()} entries
				</div>

				<div className="flex items-center gap-2">
					<Button variant="ghost" size="sm" onClick={() => goToPage(currentPage - 1)} disabled={currentPage <= 1} data-testid="prev-page" aria-label="Previous page">
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
