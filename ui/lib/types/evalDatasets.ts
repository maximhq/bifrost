// Types for the evals dataset API (/api/evals/datasets).

export const DATASET_COLUMN_TYPES = [
	"input",
	"expected_output",
	"expected_tool_calls",
	"expected_steps",
	"conversation_history",
	"scenario",
	"output",
	"variable",
	"tags",
	"file",
] as const;

// One file in a file cell. An uploaded file is named by its id; a file somewhere
// else is named by its url.
export interface DatasetFile {
	id?: string;
	name?: string;
	content_type?: string;
	size?: number;
	url?: string;
}

export type DatasetColumnType = (typeof DATASET_COLUMN_TYPES)[number];

export type DatasetTemplate = "qa" | "retrieval" | "agent";

export interface DatasetColumn {
	id: string;
	dataset_id: string;
	name: string;
	type: DatasetColumnType;
	position: number;
}

export interface DatasetSplit {
	id: string;
	dataset_id: string;
	name: string;
	description: string;
	row_count: number;
}

interface DatasetBase {
	id: string;
	name: string;
	description: string;
	tags?: string[];
	// The folder the dataset is filed under. Absent when it is in none.
	folder_id?: string;
	// The last saved version number, 0 before the first save.
	head_version: number;
	// Edits made since the last saved version.
	draft_changes: number;
	max_versions?: number;
	created_by?: string;
	created_at: string;
	updated_at: string;
}

export interface DatasetSummary extends DatasetBase {
	column_count: number;
	row_count: number;
	split_count: number;
}

export interface Dataset extends DatasetBase {
	columns: DatasetColumn[];
	splits: DatasetSplit[];
	row_count: number;
}

export interface DatasetInput {
	name: string;
	description?: string;
	tags?: string[];
	max_versions?: number | null;
	template?: DatasetTemplate;
	columns?: DatasetColumnInput[];
	// The folder to create the dataset in. Create only: a later move has its own call.
	folder_id?: string;
}

// A folder groups datasets in the sidebar. Folders nest.
export interface DatasetFolder {
	id: string;
	name: string;
	// The folder this one sits in. Absent at the top level.
	parent_id?: string;
	description: string;
	created_at: string;
	updated_at: string;
}

export interface DatasetFolderInput {
	name: string;
	description?: string;
	// The folder to create this one in. Create only: a later move has its own call.
	parent_id?: string;
}

export interface DatasetColumnInput {
	name: string;
	type: DatasetColumnType;
}

export interface ListDatasetsParams {
	search?: string;
	tag?: string;
	limit?: number;
	offset?: number;
}

export interface ListDatasetsResponse {
	datasets: DatasetSummary[];
	total: number;
	limit: number;
	offset: number;
}

export interface DatasetResponse {
	dataset: Dataset;
}

// Cells map a column ID to its value. Empty cells are left out.
export type DatasetCells = Record<string, string>;

export interface DatasetRow {
	id: string;
	seq: number;
	cells: DatasetCells;
	created_at: string;
}

export interface ListDatasetRowsParams {
	id: string;
	// A saved version to read. Leave out to read the draft.
	version?: number;
	split?: string;
	search?: string;
	after?: number;
	// Skip this many rows, to read a page by row position. Not for use with `after`.
	offset?: number;
	limit?: number;
}

export interface DatasetRowsPage {
	rows: DatasetRow[];
	// Pass as `after` for the next page. 0 on the last page.
	next_after: number;
	total: number;
	version: number;
}

export interface DatasetWriteResult {
	rows?: DatasetRow[];
	count: number;
	// The version the write was saved in, when a save rule fired.
	version?: number;
}

// The outcome of a spreadsheet edit that touched many cells: a paste or a range delete.
export interface DatasetCellEditResult {
	updated: DatasetRow[];
	added: DatasetRow[];
	version?: number;
}

export interface DatasetVersionColumn {
	id: string;
	name: string;
	type: DatasetColumnType;
	position: number;
}

export interface DatasetVersion {
	id: string;
	dataset_id: string;
	number: number;
	name?: string;
	description?: string;
	columns: DatasetVersionColumn[];
	row_count: number;
	rows_added: number;
	rows_changed: number;
	rows_removed: number;
	restored_from?: number;
	created_by?: string;
	created_at: string;
}

export type DatasetChangeKind = "added" | "removed" | "changed";

export interface DatasetRowChange {
	row_id: string;
	seq: number;
	change: DatasetChangeKind;
	before?: DatasetCells;
	after?: DatasetCells;
}

export interface DatasetColumnChange {
	change: DatasetChangeKind;
	before?: { id: string; name: string; type: DatasetColumnType };
	after?: { id: string; name: string; type: DatasetColumnType };
}

export interface DatasetDiff {
	from: number;
	to: number;
	columns: DatasetColumnChange[];
	rows: DatasetRowChange[];
	next_after: number;
}

// One CSV field mapped to a column: an existing one by ID, or a new one by name and type.
export interface DatasetImportColumn {
	index: number;
	column_id?: string;
	name?: string;
	type?: DatasetColumnType;
}

export interface DatasetImportPreview {
	header: string[];
	rows: string[][];
	// One entry per header field: the matching column ID, or "".
	mapping: string[];
}

export interface DatasetImportResult {
	rows_added: number;
	columns_created?: string[];
	version?: number;
}

export interface DatasetCurateResult {
	rows_added: number;
	skipped: { id: string; reason: string }[];
	version?: number;
}