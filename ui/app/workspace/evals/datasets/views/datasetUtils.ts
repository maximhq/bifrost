import type { DatasetColumnType, DatasetFile, DatasetImportColumn, DatasetVersion } from "@/lib/types/evalDatasets";
import { getApiBaseUrl } from "@/lib/utils/port";

// Columns whose cells hold JSON text.
const STRUCTURED_TYPES: DatasetColumnType[] = ["expected_tool_calls", "conversation_history", "tags"];

export function isStructuredColumn(type: DatasetColumnType): boolean {
	return STRUCTURED_TYPES.includes(type);
}

// The evaluator variable each typed column fills. A variable column fills a variable
// with its own name, so it cannot use one of these names.
const TYPED_VARIABLES: Partial<Record<DatasetColumnType, string>> = {
	input: "input",
	expected_output: "expected_output",
	expected_tool_calls: "expected_tool_calls",
	expected_steps: "expected_steps",
	conversation_history: "history",
	scenario: "scenario",
	output: "output",
};

export function isReservedVariableName(name: string): boolean {
	return Object.values(TYPED_VARIABLES).includes(name.trim());
}

// columnTypesLeft lists the types a new column can still take: every typed column
// exists at most once, and there can be any number of variable columns.
export function columnTypesLeft(
	all: readonly DatasetColumnType[],
	used: DatasetColumnType[],
	keep?: DatasetColumnType,
): DatasetColumnType[] {
	return all.filter((type) => type === "variable" || type === "file" || type === keep || !used.includes(type));
}

export function parseTags(text: string): string[] {
	return [
		...new Set(
			text
				.split(",")
				.map((tag) => tag.trim())
				.filter(Boolean),
		),
	];
}

export function datasetExportUrl(id: string, format: "csv" | "jsonl", version?: number, splitId?: string): string {
	const params = new URLSearchParams({ format });
	if (version) params.set("version", String(version));
	if (splitId) params.set("split", splitId);
	return `${getApiBaseUrl()}/evals/datasets/${id}/export?${params.toString()}`;
}

// versionLabel is how a version is named in pickers: its number, and its name when it has one.
export function versionLabel(version: Pick<DatasetVersion, "number" | "name">): string {
	return version.name ? `v${version.number} · ${version.name}` : `v${version.number}`;
}

// Where one CSV field goes in an import: an existing column, a new column, or nowhere.
export type ImportTarget = { kind: "column"; columnId: string } | { kind: "new"; name: string; type: DatasetColumnType } | { kind: "skip" };

// importMapping turns the user's choices into the mapping the API takes. Skipped
// fields are left out. It returns null when nothing is mapped.
export function importMapping(targets: ImportTarget[]): DatasetImportColumn[] | null {
	const mapping: DatasetImportColumn[] = [];
	targets.forEach((target, index) => {
		if (target.kind === "column") mapping.push({ index, column_id: target.columnId });
		if (target.kind === "new") mapping.push({ index, name: target.name, type: target.type });
	});
	return mapping.length > 0 ? mapping : null;
}

function normalizeField(field: string): string {
	return field
		.trim()
		.toLowerCase()
		.replace(/[\s_-]+/g, "_");
}

// newColumnType decides the type of a column created from a CSV header field. A field
// named like a column type, such as input or expected_output, becomes that typed
// column when the dataset does not have one yet. Any other field becomes a variable
// column. It returns null when no column can be made from the field.
export function newColumnType(field: string, usedTypes: DatasetColumnType[]): DatasetColumnType | null {
	const name = field.trim();
	if (!name) return null;
	const normalized = normalizeField(name);
	const typed = (Object.keys(TYPED_VARIABLES) as DatasetColumnType[]).find(
		(type) => type === normalized || TYPED_VARIABLES[type] === normalized,
	);
	const wanted = typed ?? (normalized === "tags" ? "tags" : undefined);
	if (wanted) return usedTypes.includes(wanted) ? null : wanted;
	return isReservedVariableName(name) ? null : "variable";
}

// defaultImportTargets starts from the server's suggestion: a matched column, or a new
// column made from the header field, or skip when no column can be made from it.
export function defaultImportTargets(header: string[], suggested: string[], existingTypes: DatasetColumnType[] = []): ImportTarget[] {
	const usedTypes = [...existingTypes];
	const taken = new Set<string>();
	return header.map((field, index) => {
		const columnId = suggested[index];
		if (columnId) return { kind: "column", columnId };
		const name = field.trim();
		const type = newColumnType(name, usedTypes);
		if (!type || taken.has(name)) return { kind: "skip" };
		taken.add(name);
		if (type !== "variable") usedTypes.push(type);
		return { kind: "new", name, type };
	});
}
// One row's share of a spreadsheet edit: where the row sits in the grid, its ID, and
// the cells that change, keyed by column ID.
export interface RowCellPatch {
	index: number;
	id: string;
	cells: Record<string, string>;
}

// planCellEdits groups single-cell edits by row, in row order. `rowIds` holds the ID
// of every loaded row by its grid index. Edits that point at a missing column or at a
// row that is not loaded are dropped.
export function planCellEdits(
	edits: { col: number; row: number; value: string }[],
	columnIds: string[],
	rowIds: (string | undefined)[],
): RowCellPatch[] {
	const byRow = new Map<number, RowCellPatch>();
	for (const edit of edits) {
		const columnId = columnIds[edit.col];
		const id = rowIds[edit.row];
		if (!columnId || !id) continue;
		let patch = byRow.get(edit.row);
		if (!patch) {
			patch = { index: edit.row, id, cells: {} };
			byRow.set(edit.row, patch);
		}
		patch.cells[columnId] = edit.value;
	}
	return [...byRow.values()].sort((a, b) => a.index - b.index);
}

// planPaste lays pasted lines over the grid from `target` ([column, row]). Lines that
// land on existing rows become patches; lines past the last row (`total`) become new
// rows. Values to the right of the last column are dropped.
export function planPaste(
	target: readonly [number, number],
	values: readonly (readonly string[])[],
	columnIds: string[],
	rowIds: (string | undefined)[],
	total: number,
): { updates: RowCellPatch[]; rows: Record<string, string>[] } {
	const [startCol, startRow] = target;
	const updates: RowCellPatch[] = [];
	const rows: Record<string, string>[] = [];
	values.forEach((line, offset) => {
		const cells: Record<string, string> = {};
		line.forEach((value, column) => {
			const columnId = columnIds[startCol + column];
			if (columnId) cells[columnId] = value;
		});
		if (Object.keys(cells).length === 0) return;
		const index = startRow + offset;
		if (index >= total) {
			rows.push(cells);
			return;
		}
		const id = rowIds[index];
		if (id) updates.push({ index, id, cells });
	});
	return { updates, rows };
}
// Cells of one row, keyed by column ID, as an edit sets them.
export interface CellChange {
	id: string;
	cells: Record<string, string>;
}

// previousCells is the inverse of a set of changes: for every cell a change sets, what
// that cell holds now. `cellsOf` returns a row's current cells; a row it does not know
// is left out.
export function previousCells(changes: CellChange[], cellsOf: (id: string) => Record<string, string> | undefined): CellChange[] {
	const previous: CellChange[] = [];
	for (const change of changes) {
		const current = cellsOf(change.id);
		if (!current) continue;
		const cells: Record<string, string> = {};
		for (const columnId of Object.keys(change.cells)) cells[columnId] = current[columnId] ?? "";
		previous.push({ id: change.id, cells });
	}
	return previous;
}

// UndoHistory is the pair of stacks behind undo and redo. A new step clears redo, and
// the oldest step is dropped once there are more than `limit`.
export class UndoHistory<T> {
	private undo: T[] = [];
	private redo: T[] = [];

	constructor(private readonly limit = 100) {}

	// push records a step the user just made.
	push(step: T): void {
		this.pushUndo(step);
		this.redo = [];
	}

	// pushUndo puts back a step that was redone, keeping the redo stack.
	pushUndo(step: T): void {
		this.undo.push(step);
		if (this.undo.length > this.limit) this.undo.shift();
	}

	pushRedo(step: T): void {
		this.redo.push(step);
	}

	takeUndo(): T | undefined {
		return this.undo.pop();
	}

	takeRedo(): T | undefined {
		return this.redo.pop();
	}

	clear(): void {
		this.undo = [];
		this.redo = [];
	}
}
// removeAt takes the items at `indexes` out of a list that may have gaps (rows that
// are not loaded). Later items move up, and gaps stay gaps.
export function removeAt<T>(items: (T | undefined)[], indexes: number[]): (T | undefined)[] {
	const drop = new Set(indexes);
	const next: (T | undefined)[] = [];
	let removed = 0;
	for (let index = 0; index < items.length; index++) {
		if (drop.has(index)) {
			removed++;
			continue;
		}
		if (index in items) next[index - removed] = items[index];
	}
	return next;
}

// pagesCovering lists the page numbers that hold rows `first` to `last`, both included.
export function pagesCovering(first: number, last: number, pageSize: number): number[] {
	const pages: number[] = [];
	if (last < first) return pages;
	for (let page = Math.floor(Math.max(0, first) / pageSize); page <= Math.floor(last / pageSize); page++) pages.push(page);
	return pages;
}
// One folder in the sidebar tree, with the folders and the datasets directly in it.
export interface DatasetTreeNode<F, D> {
	folder: F;
	folders: DatasetTreeNode<F, D>[];
	datasets: D[];
}

// buildDatasetTree lays folders and datasets out for the sidebar: the top-level
// folders, each with what it holds, then the datasets in no folder (`root`). A folder
// or a dataset whose parent is not in the list is shown at the top level, so nothing
// can go missing. With a search, a folder whose name matches keeps everything under
// it; any other folder keeps only what matches below it, and is left out when
// nothing does.
export function buildDatasetTree<
	F extends { id: string; name: string; parent_id?: string },
	D extends { name: string; folder_id?: string },
>(folders: F[], datasets: D[], search: string): { folders: DatasetTreeNode<F, D>[]; root: D[] } {
	const query = search.trim().toLowerCase();
	const matches = (name: string) => !query || name.toLowerCase().includes(query);
	const known = new Set(folders.map((folder) => folder.id));
	const childFolders = new Map<string, F[]>();
	const top: F[] = [];
	for (const folder of folders) {
		if (!folder.parent_id || !known.has(folder.parent_id) || folder.parent_id === folder.id) {
			top.push(folder);
			continue;
		}
		childFolders.set(folder.parent_id, [...(childFolders.get(folder.parent_id) ?? []), folder]);
	}
	const inFolder = new Map<string, D[]>();
	const root: D[] = [];
	for (const dataset of datasets) {
		if (!dataset.folder_id || !known.has(dataset.folder_id)) {
			if (matches(dataset.name)) root.push(dataset);
			continue;
		}
		inFolder.set(dataset.folder_id, [...(inFolder.get(dataset.folder_id) ?? []), dataset]);
	}
	// `seen` stops a loop in stored data from recursing forever.
	const seen = new Set<string>();
	const build = (folder: F, keepAll: boolean): DatasetTreeNode<F, D> | null => {
		if (seen.has(folder.id)) return null;
		seen.add(folder.id);
		const all = keepAll || (query !== "" && matches(folder.name));
		const nested = (childFolders.get(folder.id) ?? [])
			.map((child) => build(child, all))
			.filter((node): node is DatasetTreeNode<F, D> => node !== null);
		const own = (inFolder.get(folder.id) ?? []).filter((dataset) => all || matches(dataset.name));
		if (query && !all && nested.length === 0 && own.length === 0) return null;
		return { folder, folders: nested, datasets: own };
	};
	return {
		folders: top.map((folder) => build(folder, false)).filter((node): node is DatasetTreeNode<F, D> => node !== null),
		root,
	};
}

// folderWithDescendants is a folder's ID with the ID of every folder below it: the
// places that folder cannot be moved into.
export function folderWithDescendants(folders: { id: string; parent_id?: string }[], id: string): Set<string> {
	const found = new Set([id]);
	let grew = true;
	while (grew) {
		grew = false;
		for (const folder of folders) {
			if (folder.parent_id && found.has(folder.parent_id) && !found.has(folder.id)) {
				found.add(folder.id);
				grew = true;
			}
		}
	}
	return found;
}
// parseFileCell reads a file cell's list of files. A cell that does not hold one reads as empty.
export function parseFileCell(text: string): DatasetFile[] {
	if (!text) return [];
	try {
		const parsed: unknown = JSON.parse(text);
		return Array.isArray(parsed) ? (parsed as DatasetFile[]) : [];
	} catch {
		return [];
	}
}

// fileCellText writes a list of files as a cell's text. No files is an empty cell.
export function fileCellText(files: DatasetFile[]): string {
	return files.length === 0 ? "" : JSON.stringify(files);
}

const IMAGE_TYPES = ["image/png", "image/jpeg", "image/gif", "image/webp", "image/avif", "image/bmp"];
const IMAGE_EXTENSIONS = /\.(png|jpe?g|gif|webp|avif|bmp)$/i;

// isImageFile reports whether a file can be shown as a picture: by its type, or by
// its name when it has no type. SVG is left out: it can carry script.
export function isImageFile(file: DatasetFile): boolean {
	if (file.content_type) return IMAGE_TYPES.includes(file.content_type.toLowerCase());
	return IMAGE_EXTENSIONS.test(file.name ?? file.url ?? "");
}