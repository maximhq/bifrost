import { attemptSequence, DeliveryGroup } from "@/app/workspace/webhooks/views/deliveries.utils";
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
import { cn } from "@/lib/utils";
import type { ColumnOrderState, ColumnPinningState, VisibilityState } from "@tanstack/react-table";
import { ColumnDef, flexRender, getCoreRowModel, useReactTable } from "@tanstack/react-table";
import { ChevronLeft, ChevronRight, RefreshCw } from "lucide-react";
import { Fragment, useCallback, useEffect, useMemo, useRef } from "react";
import { shouldShowEmptyState } from "../deliveries.page.utils";

export interface DeliveriesPagination {
	limit: number;
	offset: number;
}

interface DeliveriesTableProps {
	columns: ColumnDef<DeliveryGroup>[];
	data: DeliveryGroup[];
	totalItems: number;
	loading?: boolean;
	/** Set when the query failed — suppresses the "no deliveries" empty state. */
	error?: unknown;
	pagination: DeliveriesPagination;
	onPaginationChange: (pagination: DeliveriesPagination) => void;
	onRefresh?: () => void;
	polling?: boolean;
	expandedIds: Set<string>;
	/** Column config — computed by the parent via useColumnConfig */
	columnEntries: ColumnConfigEntry[];
	columnOrder: ColumnOrderState;
	columnVisibility: VisibilityState;
	columnPinning: ColumnPinningState;
	onToggleColumnVisibility: (id: string) => void;
	onTogglePin: (id: string, side: "left" | "right") => void;
	onReorderColumns: (entries: ColumnConfigEntry[]) => void;
}

export function DeliveriesTable({
	columns,
	data,
	totalItems,
	loading = false,
	error,
	pagination,
	onPaginationChange,
	onRefresh,
	polling = false,
	expandedIds,
	columnEntries,
	columnOrder,
	columnVisibility,
	columnPinning,
	onToggleColumnVisibility,
	onTogglePin,
	onReorderColumns,
}: DeliveriesTableProps) {
	// The expand chevron and the actions button are structural, not user-configurable.
	const fixedColumnIds = useMemo(() => new Set<string>(["expand", "actions"]), []);

	const { headerCellRefs, setHeaderCellRef } = useHeaderCellRefs();
	const pinOffsets = usePinOffsets(headerCellRefs, columnPinning);

	const lastLeftPinId = columnPinning.left?.at(-1);
	const firstRightPinId = columnPinning.right?.at(0);

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

	const [pageSizePref, setPageSizePref, pageSizeHydrated] = useTablePageSizePreference("bifrost.webhookDeliveries.pageSize");

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

	const table = useReactTable({
		data,
		columns,
		getCoreRowModel: getCoreRowModel(),
		manualPagination: true,
		// The store always returns delivery groups newest-first; there is no
		// server-side sort to drive, so the table exposes none.
		manualSorting: true,
		manualFiltering: true,
		pageCount: Math.ceil(totalItems / pagination.limit),
		state: { columnOrder, columnVisibility, columnPinning },
	});

	const currentPage = Math.floor(pagination.offset / pagination.limit) + 1;
	const totalPages = Math.ceil(totalItems / pagination.limit);
	const startItemDisplay = totalItems === 0 ? 0 : pagination.offset + 1;
	const endItemDisplay = totalItems === 0 ? 0 : Math.min(pagination.offset + pagination.limit, totalItems);
	const visibleColumnCount = table.getVisibleFlatColumns().length;

	const goToPage = (page: number) => {
		onPaginationChange({ ...pagination, offset: (page - 1) * pagination.limit });
	};

	return (
		<div className="flex grow flex-col gap-2 overflow-y-auto px-4 pb-2">
			<div className="flex h-full grow flex-col gap-2">
				<div className="grow overflow-y-auto rounded-sm border">
					<Table containerClassName="h-full">
						<thead className={cn("sticky top-0 z-10 bg-[#f9f9f9] px-2 dark:bg-[#27272a] [&_tr]:border-b")}>
							{table.getHeaderGroups().map((headerGroup) => (
								<tr key={headerGroup.id} className="border-b transition-colors">
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
								<TableCell colSpan={visibleColumnCount} className="h-12 text-center">
									<div className="text-muted-foreground flex items-center justify-center gap-2 text-sm">
										{polling ? (
											<>
												<RefreshCw className="h-4 w-4 animate-spin" />
												Waiting for new deliveries...
											</>
										) : (
											<Button
												variant="ghost"
												size="sm"
												onClick={onRefresh}
												disabled={loading}
												data-testid="webhook-deliveries-table-refresh-btn"
											>
												<RefreshCw className={`h-4 w-4 ${loading ? "animate-spin" : ""}`} />
												Refresh
											</Button>
										)}
									</div>
								</TableCell>
							</TableRow>
							{table.getRowModel().rows.length ? (
								table.getRowModel().rows.map((row) => {
									const expanded = expandedIds.has(row.original.webhookId);
									return (
										<Fragment key={row.id}>
											<TableRow className="hover:bg-muted/50 group/table-row h-12">
												{row.getVisibleCells().map((cell) => {
													const pinned = cell.column.getIsPinned();
													const size = cell.column.getSize();
													return (
														<TableCell
															key={cell.id}
															style={{ width: size, minWidth: size, maxWidth: size, ...buildPinStyle(cell.column, pinOffsets) }}
															className={cn(
																!pinned && "overflow-hidden",
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
											{/* Expanded sends: the original plus each manual redelivery, each
											    with its own attempt sequence. */}
											{expanded &&
												row.original.sends.map((send) => (
													<TableRow key={send.key} className="bg-muted/30 hover:bg-muted/30">
														<TableCell />
														<TableCell colSpan={Math.max(visibleColumnCount - 1, 1)}>
															<div className="flex items-center gap-3 text-sm">
																<span className="text-muted-foreground w-28 shrink-0">{send.label}</span>
																{attemptSequence(send.attempts)}
															</div>
														</TableCell>
													</TableRow>
												))}
										</Fragment>
									);
								})
							) : !shouldShowEmptyState({ rowCount: table.getRowModel().rows.length, loading, error }) ? null : (
								<TableRow>
									<TableCell colSpan={visibleColumnCount} className="h-24 text-center">
										No deliveries found. Try adjusting your filters and/or time range.
									</TableCell>
								</TableRow>
							)}
						</TableBody>
					</Table>
				</div>

				<div className="flex items-center justify-between text-xs" data-testid="pagination">
					<div className="text-muted-foreground flex items-center gap-2">
						{startItemDisplay.toLocaleString()}-{endItemDisplay.toLocaleString()} of {totalItems.toLocaleString()} deliveries
					</div>

					<div className="flex items-center gap-2">
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