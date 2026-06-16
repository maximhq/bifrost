import {
	buildPinStyle,
	type ColumnConfigEntry,
	DraggableColumnHeader,
	PIN_SHADOW_LEFT,
	PIN_SHADOW_RIGHT,
	useHeaderCellRefs,
	usePinOffsets,
} from "@/components/table";
import { Button } from "@/components/ui/button";
import { Table, TableBody, TableCell, TableRow } from "@/components/ui/table";
import type { MCPToolLogEntry, Pagination } from "@/lib/types/logs";
import { cn } from "@/lib/utils";
import type { ColumnOrderState, ColumnPinningState, VisibilityState } from "@tanstack/react-table";
import { ColumnDef, flexRender, getCoreRowModel, SortingState, useReactTable } from "@tanstack/react-table";
import { ChevronLeft, ChevronRight, Pause, RefreshCw, X } from "lucide-react";
import { useCallback, useMemo, useState } from "react";

interface DataTableProps {
	columns: ColumnDef<MCPToolLogEntry>[];
	data: MCPToolLogEntry[];
	totalItems: number;
	loading?: boolean;
	pagination: Pagination;
	onPaginationChange: (pagination: Pagination) => void;
	onRowClick?: (log: MCPToolLogEntry, columnId: string) => void;
	isSocketConnected: boolean;
	liveEnabled: boolean;
	/** Column config — computed by the parent via useColumnConfig */
	columnEntries: ColumnConfigEntry[];
	columnOrder: ColumnOrderState;
	columnVisibility: VisibilityState;
	columnPinning: ColumnPinningState;
	onToggleColumnVisibility: (id: string) => void;
	onTogglePin: (id: string, side: "left" | "right") => void;
	onReorderColumns: (entries: ColumnConfigEntry[]) => void;
}

export function MCPLogsDataTable({
	columns,
	data,
	totalItems,
	loading = false,
	pagination,
	onPaginationChange,
	onRowClick,
	isSocketConnected,
	liveEnabled,
	columnEntries,
	columnOrder,
	columnVisibility,
	columnPinning,
	onToggleColumnVisibility,
	onTogglePin,
	onReorderColumns,
}: DataTableProps) {
	const [sorting, setSorting] = useState<SortingState>([{ id: pagination.sort_by, desc: pagination.order === "desc" }]);

	const fixedColumnIds = useMemo(() => new Set<string>([]), []);

	// Measure actual header cell widths for pixel-perfect pin offsets
	const { headerCellRefs, setHeaderCellRef } = useHeaderCellRefs();
	const pinOffsets = usePinOffsets(headerCellRefs, columnPinning);

	// Shadow on the edge of pinned groups
	const lastLeftPinId = columnPinning.left?.at(-1);
	const firstRightPinId = columnPinning.right?.at(0);

	// Handle native drag-and-drop reorder
	const handleColumnDrop = useCallback(
		(draggedId: string, targetId: string) => {
			const newEntries = [...columnEntries];
			const draggedIdx = newEntries.findIndex((e) => e.id === draggedId);
			const targetIdx = newEntries.findIndex((e) => e.id === targetId);
			if (draggedIdx === -1 || targetIdx === -1) return;
			const [moved] = newEntries.splice(draggedIdx, 1);
			newEntries.splice(targetIdx, 0, moved);
			onReorderColumns(newEntries);
		},
		[columnEntries, onReorderColumns],
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
		<div className="flex grow flex-col gap-2 overflow-y-auto px-4 pb-2">
			<div className="flex h-full grow flex-col gap-2">
				<div className="grow overflow-y-auto rounded-sm border">
					<Table containerClassName="h-full">
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
											onHide={onToggleColumnVisibility}
											onPin={onTogglePin}
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
									<TableCell colSpan={columns.length} className="h-12 text-center">
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
											<TableRow key={row.id} className="hover:bg-muted/50 group/table-row h-12 cursor-pointer">
												{row.getVisibleCells().map((cell) => {
													const pinned = cell.column.getIsPinned();
													const size = cell.column.getSize();
													return (
														<TableCell
															onClick={() => onRowClick?.(row.original, cell.column.id)}
															key={cell.id}
															style={{ width: size, minWidth: size, maxWidth: size, ...buildPinStyle(cell.column, pinOffsets) }}
															className={cn(
																"overflow-hidden",
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
		</div>
	);
}