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
import { ComboboxSelect, type ComboboxSelectOption } from "@/components/ui/combobox";
import { Table, TableBody, TableCell, TableRow } from "@/components/ui/table";
import { DEFAULT_PAGE_SIZE_OPTIONS, useTablePageSizePreference } from "@/lib/hooks/useTablePageSizePreference";
import type { RelayLog, RelayLogPagination as Pagination } from "@/lib/types/relays";
import { cn } from "@/lib/utils";
import type { ColumnOrderState, ColumnPinningState, VisibilityState } from "@tanstack/react-table";
import { ColumnDef, flexRender, getCoreRowModel, SortingState, useReactTable } from "@tanstack/react-table";
import { ChevronLeft, ChevronRight, Loader2, RefreshCw } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";

// Fixed content height for every body cell, as in the LLM logs table: 32px plus
// the cell's py-1.5 makes every body row exactly 44px, whatever it holds.
const ROW_CONTENT_HEIGHT = "h-[32px]";

interface DataTableProps {
	columns: ColumnDef<RelayLog>[];
	data: RelayLog[];
	totalItems: number;
	loading?: boolean;
	pagination: Pagination;
	onPaginationChange: (pagination: Pagination) => void;
	onRowClick?: (log: RelayLog, columnId: string) => void;
	onRefresh?: () => void;
	polling?: boolean;
	/** Column config — computed by the parent via useColumnConfig */
	columnEntries: ColumnConfigEntry[];
	columnOrder: ColumnOrderState;
	columnVisibility: VisibilityState;
	columnPinning: ColumnPinningState;
	onToggleColumnVisibility: (id: string) => void;
	onTogglePin: (id: string, side: "left" | "right") => void;
	onReorderColumns: (entries: ColumnConfigEntry[]) => void;
}

export function RelayLogsDataTable({
	columns,
	data,
	totalItems,
	loading = false,
	pagination,
	onPaginationChange,
	onRowClick,
	onRefresh,
	polling = false,
	columnEntries,
	columnOrder,
	columnVisibility,
	columnPinning,
	onToggleColumnVisibility,
	onTogglePin,
	onReorderColumns,
}: DataTableProps) {
	const [sorting, setSorting] = useState<SortingState>([{ id: pagination.sort_by, desc: pagination.order === "desc" }]);
	const [pageSizePref, setPageSizePref, pageSizeHydrated] = useTablePageSizePreference("bifrost.relayLogs.pageSize");

	const fixedColumnIds = useMemo(() => new Set<string>(["actions"]), []);

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

	// Refs so the page-size effect never closes over a stale pagination or callback.
	const paginationRef = useRef(pagination);
	const onPaginationChangeRef = useRef(onPaginationChange);
	paginationRef.current = pagination;
	onPaginationChangeRef.current = onPaginationChange;

	// Apply the page-size preference as the `limit` query param, as the LLM table does. It
	// waits for localStorage to hydrate: writing the pre-hydration default would clobber an
	// explicit `limit` already in the URL and make it flip-flop across refreshes.
	useEffect(() => {
		if (!pageSizeHydrated) return;
		if (paginationRef.current.limit !== pageSizePref) {
			onPaginationChangeRef.current({ ...paginationRef.current, limit: pageSizePref, offset: 0 });
		}
	}, [pageSizePref, pageSizeHydrated]);

	const pageSizeOptions = useMemo<ComboboxSelectOption[]>(
		() => DEFAULT_PAGE_SIZE_OPTIONS.map((size) => ({ label: String(size), value: String(size) })),
		[],
	);

	const handlePageSizeChange = useCallback(
		(value: string | null) => {
			if (!value) return;
			const next = Number(value);
			setPageSizePref(next);
			onPaginationChange({ ...pagination, limit: next, offset: 0 });
		},
		[onPaginationChange, pagination, setPageSizePref],
	);

	const handleSortingChange = (updaterOrValue: SortingState | ((old: SortingState) => SortingState)) => {
		const newSorting = typeof updaterOrValue === "function" ? updaterOrValue(sorting) : updaterOrValue;
		setSorting(newSorting);
		if (newSorting.length > 0) {
			const { id, desc } = newSorting[0];
			onPaginationChange({
				...pagination,
				sort_by: id as Pagination["sort_by"],
				order: desc ? "desc" : "asc",
			});
		}
	};

	const table = useReactTable({
		data,
		columns,
		getRowId: (row) => row.id,
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

	const hasItems = totalItems > 0;
	const currentPage = hasItems ? Math.floor(pagination.offset / pagination.limit) + 1 : 0;
	const totalPages = hasItems ? Math.ceil(totalItems / pagination.limit) : 0;
	const startItem = hasItems ? pagination.offset + 1 : 0;
	const endItem = hasItems ? Math.min(pagination.offset + pagination.limit, totalItems) : 0;

	const goToPage = (page: number) => {
		const newOffset = (page - 1) * pagination.limit;
		onPaginationChange({
			...pagination,
			offset: newOffset,
		});
	};

	return (
		<div className="flex min-h-0 flex-1 flex-col gap-2 px-4 pb-2">
			<div className="min-h-0 flex-1 overflow-hidden rounded-sm border">
				<Table containerClassName="@container/relay-logs-table h-full overflow-auto">
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
						<TableRow className="hover:bg-transparent">
							<TableCell colSpan={columns.length} className="h-12 text-center">
								{/* The row spans the whole scrollable table, so centring within it lands
								    off to the right; sticking to the visible width keeps it in view. */}
								<div
									className="text-muted-foreground sticky left-4 flex w-[calc(100cqw-2rem)] items-center justify-center gap-2 text-sm"
									data-testid="relay-logs-table-status-row"
								>
									{loading ? (
										<>
											<RefreshCw className="h-4 w-4 animate-spin" />
											Loading logs...
										</>
									) : polling ? (
										<>
											<RefreshCw className="h-4 w-4 animate-spin" />
											Waiting for new relay calls...
										</>
									) : (
										<Button
											type="button"
											onClick={onRefresh}
											data-testid="relay-logs-table-refresh-btn"
											className="hover:text-foreground inline-flex items-center gap-1.5 transition-colors"
											variant={"ghost"}
										>
											{loading ? <Loader2 className="h-4 w-4 animate-spin" /> : <RefreshCw className="h-4 w-4" />}
											Refresh
										</Button>
									)}
								</div>
							</TableCell>
						</TableRow>
						{table.getRowModel().rows.length ? (
							table.getRowModel().rows.map((row) => (
								<TableRow
									key={row.id}
									data-testid="relay-logs-row"
									data-log-id={row.original.id}
									className="hover:bg-muted/50 group/table-row min-h-[40px] cursor-pointer"
								>
									{row.getVisibleCells().map((cell) => {
										const pinned = cell.column.getIsPinned();
										const size = cell.column.getSize();
										return (
											<TableCell
												onClick={() => onRowClick?.(row.original, cell.column.id)}
												key={cell.id}
												style={{ width: size, minWidth: size, maxWidth: size, ...buildPinStyle(cell.column, pinOffsets) }}
												className={cn(
													"py-1.5 align-middle",
													pinned && "bg-card",
													cell.column.id === lastLeftPinId && PIN_SHADOW_LEFT,
													cell.column.id === firstRightPinId && PIN_SHADOW_RIGHT,
													"group-hover/table-row:bg-[#f7f7f7] dark:group-hover/table-row:bg-[#232327]",
												)}
											>
												{/* The status bar overflows the cell's padding on purpose, so it stays
												    unwrapped. Everything else sits in a fixed-height box so every row
												    is the same height whatever it holds. */}
												{cell.column.id === "status" ? (
													flexRender(cell.column.columnDef.cell, cell.getContext())
												) : (
													<div className={cn("relative grid grid-cols-[minmax(0,1fr)] items-center overflow-hidden", ROW_CONTENT_HEIGHT)}>
														{flexRender(cell.column.columnDef.cell, cell.getContext())}
													</div>
												)}
											</TableCell>
										);
									})}
								</TableRow>
							))
						) : loading ? null : (
							<TableRow>
								<TableCell colSpan={columns.length} className="h-24 text-center">
									No results found. Try adjusting your filters and/or time range.
								</TableCell>
							</TableRow>
						)}
					</TableBody>
				</Table>
			</div>

			{/* Pagination Footer */}
			<div className="flex shrink-0 items-center justify-between text-xs" data-testid="pagination">
				<div className="text-muted-foreground flex items-center gap-2">
					{startItem.toLocaleString()}-{endItem.toLocaleString()} of {totalItems.toLocaleString()} entries
				</div>

				<div className="flex items-center gap-3">
					<div className="flex items-center gap-1.5">
						<span className="text-muted-foreground">Rows per page</span>
						<ComboboxSelect
							options={pageSizeOptions}
							value={String(pageSizePref)}
							onValueChange={handlePageSizeChange}
							disableSearch
							hideClear
							className="h-7 w-fit gap-1 text-xs"
							aria-label="Rows per page"
							data-testid="relay-logs-page-size-select"
						/>
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