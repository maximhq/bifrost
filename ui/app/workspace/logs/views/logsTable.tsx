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
import type { LogEntry, Pagination } from "@/lib/types/logs";
import { cn } from "@/lib/utils";
import type { ColumnOrderState, ColumnPinningState, VisibilityState } from "@tanstack/react-table";
import { ColumnDef, flexRender, getCoreRowModel, SortingState, useReactTable } from "@tanstack/react-table";
import { ChevronLeft, ChevronRight, Loader2, RefreshCw } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";

interface DataTableProps {
	columns: ColumnDef<LogEntry>[];
	data: LogEntry[];
	totalItems: number;
	pagination: Pagination;
	onPaginationChange: (pagination: Pagination) => void;
	onRowClick?: (log: LogEntry, columnId: string) => void;
	polling: boolean;
	loading?: boolean;
	onRefresh: () => void;
	/** Column config — computed by the parent via useColumnConfig */
	columnEntries: ColumnConfigEntry[];
	columnOrder: ColumnOrderState;
	columnVisibility: VisibilityState;
	columnPinning: ColumnPinningState;
	onToggleColumnVisibility: (id: string) => void;
	onTogglePin: (id: string, side: "left" | "right") => void;
	onReorderColumns: (entries: ColumnConfigEntry[]) => void;
}

export function LogsDataTable({
	columns,
	data,
	totalItems,
	pagination,
	onPaginationChange,
	onRowClick,
	polling,
	loading,
	onRefresh,
	columnEntries,
	columnOrder,
	columnVisibility,
	columnPinning,
	onToggleColumnVisibility,
	onTogglePin,
	onReorderColumns,
}: DataTableProps) {
	const [sorting, setSorting] = useState<SortingState>([{ id: pagination.sort_by, desc: pagination.order === "desc" }]);
	const [pageSizePref, setPageSizePref, pageSizeHydrated] = useTablePageSizePreference("bifrost.logs.pageSize");

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

	// Refs to avoid stale closures in the page size effect
	const paginationRef = useRef(pagination);
	const onPaginationChangeRef = useRef(onPaginationChange);
	paginationRef.current = pagination;
	onPaginationChangeRef.current = onPaginationChange;

	// Apply the page-size preference as the `limit` query param. Wait until the
	// localStorage value has hydrated — writing the pre-hydration default would
	// clobber an explicit `limit` already present in the URL (nuqs clears the
	// default from the URL), causing the param to flip-flop across refreshes.
	useEffect(() => {
		if (!pageSizeHydrated) return;
		if (paginationRef.current.limit !== pageSizePref) {
			onPaginationChangeRef.current({
				...paginationRef.current,
				limit: pageSizePref,
				offset: 0,
			});
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
		<div className="flex h-full flex-col gap-2">
			<div className="min-h-0 flex-1 overflow-hidden rounded-sm border">
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
								<div className="text-muted-foreground flex items-center justify-center gap-2 text-sm">
									{loading ? (
										<>
											<RefreshCw className="h-4 w-4 animate-spin" />
											Loading logs...
										</>
									) : polling ? (
										<>
											<RefreshCw className="h-4 w-4 animate-spin" />
											Waiting for new logs...
										</>
									) : (
										<Button
											type="button"
											onClick={onRefresh}
											data-testid="logs-table-refresh-btn"
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
								<TableRow key={row.id} className="hover:bg-muted/50 group/table-row min-h-[40px] cursor-pointer">
									{row.getVisibleCells().map((cell) => {
										const pinned = cell.column.getIsPinned();
										const size = cell.column.getSize();
										return (
											<TableCell
												onClick={() => onRowClick?.(row.original, cell.column.id)}
												key={cell.id}
												style={{
													width: size,
													minWidth: size,
													maxWidth: size,
													...buildPinStyle(cell.column, pinOffsets),
												}}
												className={cn(
													"py-1.5 align-middle",
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
							data-testid="page-size-select"
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