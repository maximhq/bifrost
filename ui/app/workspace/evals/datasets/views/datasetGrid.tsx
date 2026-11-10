import { useToast } from "@/hooks/use-toast";
import {
	getErrorMessage,
	useAddEvalDatasetRowsMutation,
	useDeleteEvalDatasetRowsMutation,
	useEditEvalDatasetCellsMutation,
	useLazyGetEvalDatasetRowsQuery,
} from "@/lib/store";
import type { DatasetColumn, DatasetRow, DatasetVersionColumn } from "@/lib/types/evalDatasets";
import {
	type CellArray,
	CompactSelection,
	DataEditor,
	type DataEditorRef,
	type EditListItem,
	type GridCell,
	GridCellKind,
	type GridColumn,
	type GridKeyEventArgs,
	type GridSelection,
	type Item,
	type Rectangle,
	type Theme,
} from "@maximai/glide-data-grid";
import "@maximai/glide-data-grid/dist/index.css";
import { Plus } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";
import { fileCell, fileCellRenderer } from "./datasetFileCell";
import { jsonCell, jsonCellRenderer } from "./datasetJsonCell";
import {
	type CellChange,
	isStructuredColumn,
	pagesCovering,
	planCellEdits,
	planPaste,
	previousCells,
	removeAt,
	type RowCellPatch,
	UndoHistory,
} from "./datasetUtils";

// Rows are read a page at a time, by row position, as Maxim's datasheet does.
const PAGE_SIZE = 100;
// How many pages are read at once when a range spans several.
const MAX_CONCURRENT_PAGES = 3;
// A row the user just added, shown before the server has given it an ID.
const PENDING_ROW = "pending-";
const DEFAULT_COLUMN_WIDTH = 300;
// The same row and header heights as Maxim's datasheet.
const ROW_HEIGHT = 45;
const HEADER_HEIGHT = 36;
// The most rows one save call takes.
const MAX_ROWS_PER_CALL = 1000;
const customRenderers = [jsonCellRenderer, fileCellRenderer];

const emptySelection: GridSelection = { columns: CompactSelection.empty(), rows: CompactSelection.empty() };

// toRgb resolves any CSS color to an opaque rgb(), since the grid's own color math reads only
// hex and rgb. A translucent color is painted over `over` first: the dark theme's border is
// white at 10% opacity, and reading it without a backdrop would give plain white.
function toRgb(context: CanvasRenderingContext2D, color: string, fallback: string, over?: string): string {
	if (!color) return fallback;
	context.clearRect(0, 0, 1, 1);
	if (over) {
		context.fillStyle = over;
		context.fillRect(0, 0, 1, 1);
	}
	context.fillStyle = fallback;
	context.fillStyle = color;
	context.fillRect(0, 0, 1, 1);
	const [r, g, b] = context.getImageData(0, 0, 1, 1).data;
	return `rgb(${r}, ${g}, ${b})`;
}

// useGridTheme builds the grid theme from the app's design tokens, and rebuilds it when
// the light or dark class on the document changes.
function useGridTheme(): Partial<Theme> {
	const [revision, setRevision] = useState(0);
	useEffect(() => {
		const observer = new MutationObserver(() => setRevision((value) => value + 1));
		observer.observe(document.documentElement, { attributes: true, attributeFilter: ["class", "data-theme"] });
		return () => observer.disconnect();
	}, []);
	return useMemo(() => {
		const context = document.createElement("canvas").getContext("2d", { willReadFrequently: true });
		if (!context) return {};
		const styles = getComputedStyle(document.documentElement);
		const token = (name: string, fallback: string, over?: string) => toRgb(context, styles.getPropertyValue(name).trim(), fallback, over);
		const card = token("--card", "#ffffff");
		const foreground = token("--foreground", "#09090b");
		const muted = token("--muted", "#f4f4f5", card);
		const mutedForeground = token("--muted-foreground", "#52525b", card);
		const border = token("--border", "#e4e4e7", card);
		const primary = token("--primary", "#18181b");
		const accent = token("--accent", "#f4f4f5", card);
		return {
			accentColor: primary,
			accentLight: accent,
			textDark: foreground,
			textMedium: mutedForeground,
			textLight: mutedForeground,
			textHeader: mutedForeground,
			textBubble: foreground,
			bgIconHeader: mutedForeground,
			fgIconHeader: card,
			bgCell: card,
			bgCellMedium: muted,
			bgHeader: muted,
			bgHeaderHasFocus: accent,
			bgHeaderHovered: accent,
			bgBubble: muted,
			bgSearchResult: accent,
			borderColor: border,
			horizontalBorderColor: border,
			drilldownBorder: border,
			linkColor: primary,
			baseFontStyle: "13px",
			headerFontStyle: "500 12px",
			editorFontSize: "13px",
			lineHeight: 1.5,
			cellHorizontalPadding: 10,
			cellVerticalPadding: 8,
			fontFamily: styles.getPropertyValue("font-family") || "ui-sans-serif, system-ui, sans-serif",
		};
		// revision is the only input: it changes when the document theme does.
		// eslint-disable-next-line react-hooks/exhaustive-deps
	}, [revision]);
}

export interface DatasetGridColumnMenu {
	column: DatasetColumn;
	bounds: Rectangle;
}

interface DatasetGridProps {
	datasetId: string;
	columns: (DatasetColumn | DatasetVersionColumn)[];
	// A saved version to show read-only. 0 shows the draft, which can be edited.
	version: number;
	splitId?: string;
	search?: string;
	// Bump to reload the rows after a change made outside the grid.
	reloadKey: number;
	canEdit: boolean;
	onSelectedRowsChange: (rowIds: string[]) => void;
	onTotalChange?: (total: number) => void;
	onAddColumn?: () => void;
	onColumnMenu?: (menu: DatasetGridColumnMenu) => void;
	onColumnsReordered?: (columnIds: string[]) => void;
	// Called after an edit the server saved as a version, so the page can refresh its counters.
	onEdited?: () => void;
}

// The text an edited cell now holds, or null for a cell kind the grid does not edit.
function editedText(item: EditListItem): string | null {
	const value = item.value;
	if (value.kind === GridCellKind.Text) return value.data;
	if (value.kind === GridCellKind.Custom && jsonCellRenderer.isMatch(value)) return value.data.text;
	if (value.kind === GridCellKind.Custom && fileCellRenderer.isMatch(value)) return value.data.text;
	return null;
}

// One step the user can undo: the cells it changed, before and after, and the rows it added.
interface UndoStep {
	before: CellChange[];
	after: CellChange[];
	addedIds: string[];
	addedCells: Record<string, string>[];
}

function chunk<T>(items: T[], size: number): T[][] {
	const chunks: T[][] = [];
	for (let start = 0; start < items.length; start += size) chunks.push(items.slice(start, start + size));
	return chunks;
}

// DatasetGrid is the dataset as a spreadsheet, built the way Maxim's datasheet is: the
// same canvas grid, type to edit, copy and paste of ranges, Delete to clear cells or
// remove selected rows, and fill down or right.
//
// Rows are read by row position, a page at a time, for whatever part of the dataset is
// on screen: a jump to the last row of a large dataset reads one page, not every page
// before it. The rows live in a ref, indexed by position, with gaps where nothing is
// loaded yet. An edit changes one row in place and asks the grid to redraw, so it
// costs the same in a dataset of fifty rows or fifty thousand.
export function DatasetGrid({
	datasetId,
	columns,
	version,
	splitId,
	search,
	reloadKey,
	canEdit,
	onSelectedRowsChange,
	onTotalChange,
	onAddColumn,
	onColumnMenu,
	onColumnsReordered,
	onEdited,
}: DatasetGridProps) {
	const { t } = useTranslation("config");
	const { t: tCommon } = useTranslation("common");
	const { toast } = useToast();
	const theme = useGridTheme();
	const readOnly = !canEdit || version > 0;

	const [fetchRows] = useLazyGetEvalDatasetRowsQuery();
	const [editCells] = useEditEvalDatasetCellsMutation();
	const [addRows] = useAddEvalDatasetRowsMutation();
	const [deleteRows] = useDeleteEvalDatasetRowsMutation();

	const editor = useRef<DataEditorRef>(null);
	// Rows by position. A position with nothing in it is not loaded yet.
	const data = useRef<(DatasetRow | undefined)[]>([]);
	const [total, setTotal] = useState(0);
	// Bumped after every change to `data`, so the grid reads the cells again.
	const [revision, setRevision] = useState(0);
	const redraw = useCallback(() => setRevision((value) => value + 1), []);
	const [selection, setSelection] = useState<GridSelection>(emptySelection);
	const selectionRef = useRef(selection);
	selectionRef.current = selection;
	const [widths, setWidths] = useState<Record<string, number>>({});
	// The pages that are loaded or being loaded. A page is read once.
	const pages = useRef(new Map<number, Promise<void>>());
	const generation = useRef(0);
	const visible = useRef({ first: 0, last: 0 });
	const totalRef = useRef(0);
	// False until the server has said how many rows there are: zero is then really zero.
	const countKnown = useRef(false);
	// Rows added on screen that the server has not confirmed yet.
	const pending = useRef({ next: 0, open: 0, totalStale: false });
	const history = useRef(new UndoHistory<UndoStep>());
	// Saves, undo and redo run one after another, in the order the user made them.
	const queue = useRef<Promise<unknown>>(Promise.resolve());
	const enqueue = useCallback(<T,>(work: () => Promise<T>): Promise<T> => {
		const next = queue.current.then(work, work);
		queue.current = next.catch(() => undefined);
		return next;
	}, []);

	const changeTotal = useCallback(
		(next: number) => {
			totalRef.current = next;
			setTotal(next);
			onTotalChange?.(next);
		},
		[onTotalChange],
	);

	const showError = useCallback(
		(error: unknown, title?: string) =>
			toast({ title: title ?? tCommon("error"), description: getErrorMessage(error), variant: "destructive" }),
		[toast, tCommon],
	);

	// loadPage reads one page of rows by position. A page that is loaded or on its way
	// is not read again.
	const loadPage = useCallback(
		(page: number): Promise<void> => {
			const known = pages.current.get(page);
			if (known) return known;
			const gen = generation.current;
			const run: Promise<void> = (async () => {
				// A page that was forgotten while it was being read (a filter change, or rows
				// removed above it) holds rows at positions that no longer hold.
				const current = () => gen === generation.current && pages.current.get(page) === run;
				try {
					const result = await fetchRows({
						id: datasetId,
						version: version || undefined,
						split: splitId,
						search,
						offset: page * PAGE_SIZE,
						limit: PAGE_SIZE,
					}).unwrap();
					if (!current()) return;
					result.rows.forEach((row, offset) => {
						data.current[page * PAGE_SIZE + offset] = row;
					});
					// The server does not know the rows still on their way to it.
					if (pending.current.open > 0) pending.current.totalStale = true;
					countKnown.current = true;
					changeTotal(result.total + pending.current.open);
					redraw();
				} catch (error) {
					if (!current()) return;
					// Forget the page, so that the next scroll over it tries again.
					pages.current.delete(page);
					showError(error);
				}
			})();
			pages.current.set(page, run);
			return run;
		},
		[datasetId, version, splitId, search, fetchRows, changeTotal, redraw, showError],
	);

	// ensureRange loads the pages that hold rows `first` to `last`, a few at a time.
	const ensureRange = useCallback(
		async (first: number, last: number) => {
			for (const part of chunk(pagesCovering(first, last, PAGE_SIZE), MAX_CONCURRENT_PAGES)) {
				await Promise.all(part.map(loadPage));
			}
		},
		[loadPage],
	);

	// loadVisible reads the rows on screen, and half a page either side of them.
	const loadVisible = useCallback(() => {
		const last = Math.min(visible.current.last + PAGE_SIZE / 2, totalRef.current - 1);
		void ensureRange(visible.current.first - PAGE_SIZE / 2, last);
	}, [ensureRange]);

	// forgetPagesFrom drops what is known about `page` and every page after it, and
	// reads again what is on screen. The rows stay on screen until the fresh ones land.
	const forgetPagesFrom = useCallback(
		(page: number) => {
			for (const known of [...pages.current.keys()]) {
				if (known >= page) pages.current.delete(known);
			}
			loadVisible();
		},
		[loadVisible],
	);

	// Undo steps belong to one draft. A reload after a change made outside the grid
	// (an import, a restore, a column change) makes them stale.
	useEffect(() => {
		history.current.clear();
	}, [datasetId, version, reloadKey]);

	// Start over whenever what is being shown changes. The first page gives the row
	// count; the rows on screen are read as well, because the view may be scrolled far
	// down and will not report its position again unless it moves.
	useEffect(() => {
		generation.current += 1;
		pages.current.clear();
		countKnown.current = false;
		data.current = [];
		pending.current = { next: pending.current.next, open: 0, totalStale: false };
		setSelection(emptySelection);
		onSelectedRowsChange([]);
		redraw();
		void loadPage(0).then(loadVisible);
		// onSelectedRowsChange is a setter owned by the page; it is not a reason to reload.
		// eslint-disable-next-line react-hooks/exhaustive-deps
	}, [datasetId, version, splitId, search, reloadKey]);

	const columnIds = useMemo(() => columns.map((column) => column.id), [columns]);
	// The ID of every loaded row by its position, with gaps for rows that are not loaded.
	// A row the server has not confirmed has no ID to save under yet.
	const rowIds = useCallback(() => Array.from(data.current, (row) => (row && !row.id.startsWith(PENDING_ROW) ? row.id : undefined)), []);

	const gridColumns: GridColumn[] = useMemo(
		() =>
			columns.map((column) => ({
				id: column.id,
				title: column.name,
				width: widths[column.id] ?? DEFAULT_COLUMN_WIDTH,
				hasMenu: !readOnly && Boolean(onColumnMenu),
			})),
		[columns, widths, readOnly, onColumnMenu],
	);

	const getCellContent = useCallback(
		([col, row]: Item): GridCell => {
			const loaded = data.current[row];
			const column = columns[col];
			if (!loaded || !column) {
				return { kind: GridCellKind.Loading, allowOverlay: false };
			}
			const value = loaded.cells[column.id] ?? "";
			if (column.type === "file") return fileCell(value, datasetId, readOnly);
			if (isStructuredColumn(column.type)) return jsonCell(value, readOnly);
			return { kind: GridCellKind.Text, data: value, displayData: value, allowOverlay: true, readonly: readOnly, allowWrapping: true };
		},
		// revision is here so that a change to `data` gives the grid a new reader.
		// eslint-disable-next-line react-hooks/exhaustive-deps
		[columns, readOnly, revision],
	);
	const readCell = useRef(getCellContent);
	readCell.current = getCellContent;

	// A copy can reach past what is on screen (select all, then copy), so the rows of
	// the range are loaded before their cells are handed over. A range that is already
	// loaded is answered at once.
	const getCellsForSelection = useCallback(
		(range: Rectangle): CellArray | (() => Promise<CellArray>) => {
			const read = (): CellArray => {
				const cells: GridCell[][] = [];
				for (let row = range.y; row < range.y + range.height; row++) {
					const line: GridCell[] = [];
					for (let col = range.x; col < range.x + range.width; col++) line.push(readCell.current([col, row]));
					cells.push(line);
				}
				return cells;
			};
			const last = Math.min(range.y + range.height, totalRef.current) - 1;
			let loaded = true;
			for (let row = range.y; row <= last && loaded; row++) loaded = data.current[row] !== undefined;
			if (loaded) return read();
			return async () => {
				await ensureRange(range.y, last);
				return read();
			};
		},
		[ensureRange],
	);

	// save writes patches and new rows to the server, at most MAX_ROWS_PER_CALL of each
	// per call. Patched rows show their new cells at once; if a call fails, the rows it
	// carried go back to what they were. It returns the patches and the new rows the
	// server took.
	const save = useCallback(
		async (patches: RowCellPatch[], newRows: Record<string, string>[]): Promise<{ saved: RowCellPatch[]; added: DatasetRow[] }> => {
			const outcome: { saved: RowCellPatch[]; added: DatasetRow[] } = { saved: [], added: [] };
			if (patches.length === 0 && newRows.length === 0) return outcome;
			const gen = generation.current;
			const before = new Map<string, DatasetRow>();
			for (const patch of patches) {
				const row = data.current[patch.index];
				if (!row || row.id !== patch.id) continue;
				before.set(row.id, row);
				data.current[patch.index] = { ...row, cells: { ...row.cells, ...patch.cells } };
			}
			redraw();

			const patchChunks = chunk(patches, MAX_ROWS_PER_CALL);
			const rowChunks = chunk(newRows, MAX_ROWS_PER_CALL);
			for (let call = 0; call < Math.max(patchChunks.length, rowChunks.length); call++) {
				const updates = patchChunks[call] ?? [];
				try {
					const result = await editCells({
						id: datasetId,
						updates: updates.map(({ id, cells }) => ({ id, cells })),
						rows: rowChunks[call],
						splitId,
					}).unwrap();
					outcome.saved.push(...updates);
					outcome.added.push(...result.added);
					if (gen !== generation.current) continue;
					const stored = new Map(result.updated.map((row) => [row.id, row]));
					for (const patch of updates) {
						const row = stored.get(patch.id);
						if (row && data.current[patch.index]?.id === patch.id) data.current[patch.index] = row;
					}
					// New rows go after the last row. Under a search they may not match, so
					// they are not given a place in the grid.
					if (result.added.length > 0 && !search) {
						let at = totalRef.current;
						for (const row of result.added) data.current[at++] = row;
						changeTotal(at);
					}
				} catch (error) {
					if (gen !== generation.current) break;
					for (const patch of patchChunks.slice(call).flat()) {
						const row = before.get(patch.id);
						if (row && data.current[patch.index]?.id === patch.id) data.current[patch.index] = row;
					}
					showError(error, t("evals.datasets.grid.editFailed"));
					break;
				}
			}
			if (gen === generation.current) redraw();
			onEdited?.();
			return outcome;
		},
		[datasetId, splitId, search, editCells, redraw, changeTotal, showError, onEdited, t],
	);

	// commit saves an edit the user made and records it as one undo step.
	const commit = useCallback(
		(
			plan: () =>
				| { updates: RowCellPatch[]; rows: Record<string, string>[] }
				| Promise<{ updates: RowCellPatch[]; rows: Record<string, string>[] }>,
		) =>
			enqueue(async () => {
				const { updates, rows } = await plan();
				const cellsNow = new Map(updates.map((patch) => [patch.id, data.current[patch.index]?.cells]));
				const before = previousCells(updates, (id) => cellsNow.get(id));
				const { saved, added } = await save(updates, rows);
				if (saved.length === 0 && added.length === 0) return;
				const savedIds = new Set(saved.map((patch) => patch.id));
				history.current.push({
					before: before.filter((change) => savedIds.has(change.id)),
					after: saved.map(({ id, cells }) => ({ id, cells })),
					addedIds: added.map((row) => row.id),
					addedCells: rows.slice(0, added.length),
				});
			}),
		[enqueue, save],
	);

	// removeRows deletes rows on the server and takes them out of the grid. The rows
	// below them move up, so every page from the first removed row on is read again.
	const removeRows = useCallback(
		async (ids: string[]) => {
			const gen = generation.current;
			for (const part of chunk(ids, MAX_ROWS_PER_CALL)) {
				await deleteRows({ id: datasetId, rowIds: part }).unwrap();
			}
			onEdited?.();
			if (gen !== generation.current) return;
			const removed = new Set(ids);
			const indexes: number[] = [];
			data.current.forEach((row, index) => {
				if (row && removed.has(row.id)) indexes.push(index);
			});
			data.current = removeAt(data.current, indexes);
			// Under a search, a removed row that did not match was never counted.
			changeTotal(Math.max(0, totalRef.current - (search ? indexes.length : ids.length)));
			redraw();
			// A removed row that was not loaded here could be anywhere: read everything again.
			forgetPagesFrom(indexes.length === ids.length ? Math.floor(indexes[0] / PAGE_SIZE) : 0);
		},
		[datasetId, deleteRows, search, changeTotal, redraw, forgetPagesFrom, onEdited],
	);

	// toPatches finds where each changed row sits in the grid. A row that is not
	// loaded gets index -1: it is still saved, just not redrawn.
	const toPatches = useCallback((changes: CellChange[]): RowCellPatch[] => {
		const indexes = new Map<string, number>();
		data.current.forEach((row, index) => {
			if (row) indexes.set(row.id, index);
		});
		return changes.map((change) => ({ index: indexes.get(change.id) ?? -1, id: change.id, cells: change.cells }));
	}, []);

	// undo puts back the cells of the last step and removes the rows it added.
	const undo = useCallback(
		() =>
			enqueue(async () => {
				const step = history.current.takeUndo();
				if (!step) return;
				try {
					if (step.before.length > 0) {
						const { saved } = await save(toPatches(step.before), []);
						// A refused save has already shown its error. The step is dropped.
						if (saved.length < step.before.length) return;
					}
					if (step.addedIds.length > 0) await removeRows(step.addedIds);
					history.current.pushRedo({ ...step, addedIds: [] });
				} catch (error) {
					showError(error);
				}
			}),
		[enqueue, save, toPatches, removeRows, showError],
	);

	// redo applies the last undone step again. Rows it adds again get new IDs.
	const redo = useCallback(
		() =>
			enqueue(async () => {
				const step = history.current.takeRedo();
				if (!step) return;
				const { saved, added } = await save(toPatches(step.after), step.addedCells);
				if (saved.length < step.after.length || added.length < step.addedCells.length) return;
				history.current.pushUndo({ ...step, addedIds: added.map((row) => row.id) });
			}),
		[enqueue, save, toPatches],
	);

	// Every edit the grid makes arrives here as one batch: a typed cell, a cleared
	// range, a fill. Cells that did not change are left out. The rows are looked up
	// when the save runs, not now: a row that was just added has its ID by then.
	const onCellsEdited = useCallback(
		(items: readonly EditListItem[]) => {
			const edits: { col: number; row: number; value: string }[] = [];
			for (const item of items) {
				const value = editedText(item);
				const [col, row] = item.location;
				const columnId = columnIds[col];
				const current = data.current[row];
				if (value === null || !columnId || !current) continue;
				if ((current.cells[columnId] ?? "") !== value) edits.push({ col, row, value });
			}
			if (edits.length === 0) return true;
			void commit(() => ({ updates: planCellEdits(edits, columnIds, rowIds()), rows: [] }));
			return true;
		},
		[columnIds, rowIds, commit],
	);

	// A paste writes over the cells from the selected one, and adds rows for whatever
	// runs past the last row. The rows it lands on are loaded first, so that a long
	// paste does not skip the ones that were never on screen.
	const onPaste = useCallback(
		(target: Item, values: readonly (readonly string[])[]) => {
			void commit(async () => {
				await ensureRange(target[1], Math.min(target[1] + values.length, totalRef.current) - 1);
				return planPaste(target, values, columnIds, rowIds(), totalRef.current);
			});
			// The grid must not also apply the paste cell by cell.
			return false;
		},
		[columnIds, rowIds, ensureRange, commit],
	);

	// Delete with rows selected removes those rows. Any other selection is cleared
	// cell by cell, which arrives in onCellsEdited. Removing rows is not an undo step:
	// a removed row cannot come back at its place or in its splits, so the way back
	// is to restore a version.
	const onDelete = useCallback(
		(current: GridSelection): boolean | GridSelection => {
			if (current.rows.length === 0) return true;
			const known = rowIds();
			const ids = current.rows
				.toArray()
				.map((index) => known[index])
				.filter((id): id is string => Boolean(id));
			if (ids.length === 0) return false;
			void enqueue(async () => {
				try {
					await removeRows(ids);
					toast({ title: t("evals.datasets.grid.rowsDeleted", { count: ids.length }) });
				} catch (error) {
					showError(error);
				}
			});
			setSelection(emptySelection);
			onSelectedRowsChange([]);
			return false;
		},
		[rowIds, enqueue, removeRows, showError, toast, t, onSelectedRowsChange],
	);

	// appendRow adds an empty row at the end and puts the cursor in it, in the given
	// column, ready to type. The row is on screen and selected before the server
	// answers, so the keyboard never waits: what is typed into it is saved once the
	// row has its ID, because saves run in order.
	const appendRow = useCallback(
		(column: number) => {
			const gen = generation.current;
			// Under a search the new row would not match, so it gets no place in the grid.
			const index = search ? -1 : totalRef.current;
			const placeholder = `${PENDING_ROW}${pending.current.next++}`;
			if (index >= 0) {
				pending.current.open += 1;
				data.current[index] = { id: placeholder, seq: 0, cells: {}, created_at: "" };
				changeTotal(index + 1);
				redraw();
				setSelection({
					current: { cell: [column, index], range: { x: column, y: index, width: 1, height: 1 }, rangeStack: [] },
					rows: CompactSelection.empty(),
					columns: CompactSelection.empty(),
				});
				onSelectedRowsChange([]);
				// The grid learns of the new row on its next render. Scroll once it has.
				window.requestAnimationFrame(() => {
					editor.current?.scrollTo(column, index);
					editor.current?.focus();
				});
			}
			void enqueue(async () => {
				const settle = () => {
					// After a reload the placeholder is gone, and so is the count it was part of.
					if (index < 0 || gen !== generation.current) return -1;
					pending.current.open -= 1;
					return data.current.findIndex((row) => row?.id === placeholder);
				};
				try {
					const result = await addRows({ id: datasetId, rows: [{}], splitId }).unwrap();
					const row = result.rows?.[0];
					const at = settle();
					if (row) {
						if (at >= 0) data.current[at] = { ...row, cells: { ...row.cells, ...data.current[at]?.cells } };
						history.current.push({ before: [], after: [], addedIds: [row.id], addedCells: [{}] });
					}
					onEdited?.();
				} catch (error) {
					const at = settle();
					if (at >= 0) {
						data.current = removeAt(data.current, [at]);
						changeTotal(Math.max(0, totalRef.current - 1));
					}
					showError(error);
				}
				if (gen !== generation.current) return;
				redraw();
				// A page that was read while the row was on its way counted it by guess.
				if (pending.current.open === 0 && pending.current.totalStale) {
					pending.current.totalStale = false;
					forgetPagesFrom(Math.floor(Math.max(0, totalRef.current - 1) / PAGE_SIZE));
				}
			});
		},
		[addRows, datasetId, splitId, search, enqueue, changeTotal, redraw, forgetPagesFrom, onEdited, onSelectedRowsChange, showError],
	);

	// An empty dataset starts its first row on a click anywhere in the grid body. The
	// click may also reach the grid's own "Add row" strip, in either order, so the two
	// are told apart by time and only one row is added.
	const emptyClickAt = useRef(0);
	const onEmptyClick = useCallback(
		(event: React.MouseEvent<HTMLDivElement>) => {
			if (readOnly || search || totalRef.current !== 0 || !countKnown.current) return;
			if ((event.target as HTMLElement).closest("button")) return;
			// The header row is not the body: a click there sorts out columns, not rows.
			if (event.clientY - event.currentTarget.getBoundingClientRect().top <= HEADER_HEIGHT) return;
			emptyClickAt.current = Date.now();
			appendRow(0);
		},
		[readOnly, search, appendRow],
	);
	const onTrailingRow = useCallback(() => {
		if (Date.now() - emptyClickAt.current < 400) return;
		emptyClickAt.current = Date.now();
		appendRow(0);
	}, [appendRow]);

	// onLastRow gives the column of the cursor when it sits on the last row, where
	// moving down starts a new row as in a spreadsheet. It is null anywhere else, and
	// under a search, where a new row would not be shown.
	const onLastRow = useCallback((): number | null => {
		const cell = selectionRef.current.current?.cell;
		const last = totalRef.current - 1;
		if (!cell || search || last < 0 || cell[1] !== last || !data.current[last]) return null;
		return cell[0];
	}, [search]);

	// Arrow down on the last row adds a row and moves into it. A held key adds one row,
	// not one per repeat. Cmd or Ctrl+Z undoes, with Shift it redoes, and Ctrl+Y redoes
	// too. This runs only while the grid has the focus: an open cell editor keeps its
	// own keys.
	const onKeyDown = useCallback(
		(event: GridKeyEventArgs) => {
			if (event.key === "ArrowDown" && !event.metaKey && !event.ctrlKey && !event.altKey && !event.shiftKey) {
				const column = onLastRow();
				if (column === null) return;
				event.cancel();
				event.preventDefault();
				if (!event.rawEvent?.repeat) appendRow(column);
				return;
			}
			if (!(event.metaKey || event.ctrlKey) || event.altKey) return;
			const key = event.key.toLowerCase();
			if (key !== "z" && key !== "y") return;
			event.cancel();
			event.preventDefault();
			void (key === "y" || event.shiftKey ? redo() : undo());
		},
		[undo, redo, onLastRow, appendRow],
	);

	// Enter in an open cell saves it and moves down. On the last row that also starts
	// a new row, so a column can be typed top to bottom without leaving the keyboard.
	// The grid has just moved the cursor one row down, onto the place the new row takes.
	const onFinishedEditing = useCallback(
		(value: GridCell | undefined, movement: Item) => {
			if (value === undefined || movement[0] !== 0 || movement[1] !== 1) return;
			const column = onLastRow();
			if (column !== null) appendRow(column);
		},
		[onLastRow, appendRow],
	);

	const onVisibleRegionChanged = useCallback(
		(range: Rectangle) => {
			visible.current = { first: range.y, last: range.y + range.height };
			loadVisible();
		},
		[loadVisible],
	);

	const onGridSelectionChange = useCallback(
		(next: GridSelection) => {
			setSelection(next);
			const known = rowIds();
			const ids: string[] = [];
			for (const index of next.rows.toArray()) {
				const id = known[index];
				if (id) ids.push(id);
			}
			onSelectedRowsChange(ids);
		},
		[rowIds, onSelectedRowsChange],
	);

	return (
		<div className="h-full w-full" data-testid="dataset-grid" role="presentation" onClick={onEmptyClick}>
			<DataEditor
				ref={editor}
				width="100%"
				height="100%"
				theme={theme}
				customRenderers={customRenderers}
				columns={gridColumns}
				rows={total}
				getCellContent={getCellContent}
				onCellsEdited={readOnly ? undefined : onCellsEdited}
				onPaste={readOnly ? false : onPaste}
				onDelete={readOnly ? undefined : onDelete}
				onKeyDown={readOnly ? undefined : onKeyDown}
				onFinishedEditing={readOnly ? undefined : onFinishedEditing}
				gridSelection={selection}
				onGridSelectionChange={onGridSelectionChange}
				rowMarkers={{ kind: readOnly ? "number" : "both", width: 44, checkboxStyle: "square" }}
				rowSelect={readOnly ? "none" : "multi"}
				rowSelectionMode="multi"
				columnSelect="none"
				rangeSelect="multi-rect"
				getCellsForSelection={getCellsForSelection}
				editOnType={!readOnly}
				fillHandle={!readOnly}
				keybindings={{ downFill: true, rightFill: true }}
				smoothScrollX={true}
				smoothScrollY={true}
				rowHeight={ROW_HEIGHT}
				headerHeight={HEADER_HEIGHT}
				experimental={{ hyperWrapping: true }}
				onVisibleRegionChanged={onVisibleRegionChanged}
				onColumnResize={(column, width) => setWidths((current) => ({ ...current, [column.id ?? ""]: width }))}
				onColumnMoved={
					readOnly || !onColumnsReordered
						? undefined
						: (from, to) => {
								const ids = columns.map((column) => column.id);
								const [moved] = ids.splice(from, 1);
								ids.splice(to, 0, moved);
								onColumnsReordered(ids);
							}
				}
				onHeaderMenuClick={
					readOnly || !onColumnMenu
						? undefined
						: (col, bounds) => {
								const column = columns[col];
								if (column && "dataset_id" in column) onColumnMenu({ column, bounds });
							}
				}
				onRowAppended={readOnly ? undefined : onTrailingRow}
				trailingRowOptions={{ sticky: true, tint: true, hint: t("evals.datasets.grid.addRow"), targetColumn: 0 }}
				rightElement={
					!readOnly && onAddColumn ? (
						<button
							type="button"
							onClick={onAddColumn}
							aria-label={t("evals.datasets.columns.add")}
							data-testid="dataset-grid-add-column-btn"
							className="text-muted-foreground hover:bg-accent hover:text-foreground bg-muted flex w-11 items-center justify-center"
							style={{ height: HEADER_HEIGHT }}
						>
							<Plus className="h-4 w-4" />
						</button>
					) : undefined
				}
				rightElementProps={{ fill: false, sticky: false }}
			/>
			{/* The grid mounts its cell editor into an element with this id. */}
			{createPortal(<div id="portal" style={{ position: "fixed", left: 0, top: 0, zIndex: 9999 }} />, document.body)}
		</div>
	);
}