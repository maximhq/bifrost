import { baseApi } from "@/lib/store/apis/baseApi";
import {
	DatasetCellEditResult,
	DatasetColumn,
	DatasetColumnInput,
	DatasetCurateResult,
	DatasetDiff,
	DatasetFile,
	DatasetFolder,
	DatasetFolderInput,
	DatasetImportColumn,
	DatasetImportPreview,
	DatasetImportResult,
	DatasetInput,
	DatasetResponse,
	DatasetRow,
	DatasetRowsPage,
	DatasetSplit,
	DatasetVersion,
	DatasetWriteResult,
	ListDatasetRowsParams,
	ListDatasetsParams,
	ListDatasetsResponse,
} from "@/lib/types/evalDatasets";

const base = "/evals/datasets";

// A write to a dataset can change its rows, its counters and its versions at once.
const datasetTags = (id: string) => [
	"EvalDatasets" as const,
	{ type: "EvalDatasets" as const, id },
	{ type: "EvalDatasetRows" as const, id },
	{ type: "EvalDatasetVersions" as const, id },
];

function queryString(params: Record<string, string | number | boolean | undefined>): string {
	const search = new URLSearchParams();
	for (const [key, value] of Object.entries(params)) {
		if (value !== undefined && value !== "") search.set(key, String(value));
	}
	const query = search.toString();
	return query ? `?${query}` : "";
}

// Inject dataset endpoints into baseApi
export const evalDatasetsApi = baseApi.injectEndpoints({
	overrideExisting: true,
	endpoints: (builder) => ({
		getEvalDatasets: builder.query<ListDatasetsResponse, ListDatasetsParams | void>({
			query: (params) => `${base}${queryString({ ...params })}`,
			providesTags: ["EvalDatasets"],
		}),

		getEvalDataset: builder.query<DatasetResponse, string>({
			query: (id) => `${base}/${id}`,
			providesTags: (result, error, id) => [{ type: "EvalDatasets", id }],
		}),

		createEvalDataset: builder.mutation<DatasetResponse, DatasetInput>({
			query: (data) => ({ url: base, method: "POST", body: data }),
			invalidatesTags: ["EvalDatasets"],
		}),

		updateEvalDataset: builder.mutation<DatasetResponse, { id: string; data: DatasetInput }>({
			query: ({ id, data }) => ({ url: `${base}/${id}`, method: "PUT", body: data }),
			invalidatesTags: (result, error, { id }) => datasetTags(id),
		}),

		deleteEvalDataset: builder.mutation<{ deleted: boolean }, string>({
			query: (id) => ({ url: `${base}/${id}`, method: "DELETE" }),
			invalidatesTags: ["EvalDatasets"],
		}),

		// The file itself is the request body. Its name goes in the query and its type
		// in the Content-Type header.
		uploadEvalDatasetFile: builder.mutation<{ file: DatasetFile }, { id: string; file: File }>({
			query: ({ id, file }) => ({
				url: `${base}/${id}/files${queryString({ name: file.name })}`,
				method: "POST",
				body: file,
				headers: { "Content-Type": file.type || "application/octet-stream" },
			}),
		}),

		// Filing a dataset under a folder changes the listing, not the dataset's contents.
		moveEvalDataset: builder.mutation<DatasetResponse, { id: string; folderId: string | null }>({
			query: ({ id, folderId }) => ({ url: `${base}/${id}/folder`, method: "PUT", body: { folder_id: folderId } }),
			invalidatesTags: (result, error, { id }) => ["EvalDatasets", { type: "EvalDatasets", id }],
		}),

		getEvalDatasetFolders: builder.query<{ folders: DatasetFolder[] }, void>({
			query: () => `${base}/folders`,
			providesTags: ["EvalDatasetFolders"],
		}),

		createEvalDatasetFolder: builder.mutation<{ folder: DatasetFolder }, DatasetFolderInput>({
			query: (data) => ({ url: `${base}/folders`, method: "POST", body: data }),
			invalidatesTags: ["EvalDatasetFolders"],
		}),

		updateEvalDatasetFolder: builder.mutation<{ folder: DatasetFolder }, { id: string; data: DatasetFolderInput }>({
			query: ({ id, data }) => ({ url: `${base}/folders/${id}`, method: "PUT", body: data }),
			invalidatesTags: ["EvalDatasetFolders"],
		}),

		moveEvalDatasetFolder: builder.mutation<{ folder: DatasetFolder }, { id: string; parentId: string | null }>({
			query: ({ id, parentId }) => ({ url: `${base}/folders/${id}/parent`, method: "PUT", body: { parent_id: parentId } }),
			invalidatesTags: ["EvalDatasetFolders"],
		}),

		// Deleting a folder moves what it holds up one level, so the listing changes too.
		deleteEvalDatasetFolder: builder.mutation<{ deleted: boolean }, string>({
			query: (id) => ({ url: `${base}/folders/${id}`, method: "DELETE" }),
			invalidatesTags: ["EvalDatasetFolders", "EvalDatasets"],
		}),

		duplicateEvalDataset: builder.mutation<DatasetResponse, { id: string; name?: string }>({
			query: ({ id, name }) => ({ url: `${base}/${id}/duplicate`, method: "POST", body: { name } }),
			invalidatesTags: ["EvalDatasets"],
		}),

		// Columns
		addEvalDatasetColumn: builder.mutation<{ column: DatasetColumn }, { id: string; data: DatasetColumnInput }>({
			query: ({ id, data }) => ({ url: `${base}/${id}/columns`, method: "POST", body: data }),
			invalidatesTags: (result, error, { id }) => datasetTags(id),
		}),

		updateEvalDatasetColumn: builder.mutation<{ column: DatasetColumn }, { id: string; columnId: string; data: DatasetColumnInput }>({
			query: ({ id, columnId, data }) => ({ url: `${base}/${id}/columns/${columnId}`, method: "PUT", body: data }),
			invalidatesTags: (result, error, { id }) => datasetTags(id),
		}),

		deleteEvalDatasetColumn: builder.mutation<{ deleted: boolean }, { id: string; columnId: string }>({
			query: ({ id, columnId }) => ({ url: `${base}/${id}/columns/${columnId}`, method: "DELETE" }),
			invalidatesTags: (result, error, { id }) => datasetTags(id),
		}),

		reorderEvalDatasetColumns: builder.mutation<{ reordered: boolean }, { id: string; columnIds: string[] }>({
			query: ({ id, columnIds }) => ({ url: `${base}/${id}/columns/order`, method: "PUT", body: { column_ids: columnIds } }),
			invalidatesTags: (result, error, { id }) => datasetTags(id),
		}),

		// Rows
		getEvalDatasetRows: builder.query<DatasetRowsPage, ListDatasetRowsParams>({
			query: ({ id, ...params }) => `${base}/${id}/rows${queryString(params)}`,
			providesTags: (result, error, { id }) => [{ type: "EvalDatasetRows", id }],
		}),

		addEvalDatasetRows: builder.mutation<DatasetWriteResult, { id: string; rows: Record<string, string>[]; splitId?: string }>({
			query: ({ id, rows, splitId }) => ({ url: `${base}/${id}/rows`, method: "POST", body: { rows, split_id: splitId } }),
			invalidatesTags: (result, error, { id }) => datasetTags(id),
		}),

		// A cell edit does not refetch the rows: the grid already shows the new value.
		updateEvalDatasetRow: builder.mutation<
			{ row: DatasetRow; version: number | null },
			{ id: string; rowId: string; cells: Record<string, string> }
		>({
			query: ({ id, rowId, cells }) => ({ url: `${base}/${id}/rows/${rowId}`, method: "PATCH", body: { cells } }),
			invalidatesTags: (result, error, { id }) => ["EvalDatasets", { type: "EvalDatasets", id }, { type: "EvalDatasetVersions", id }],
		}),

		// A paste or a range delete: many cells, and maybe new rows, in one transaction.
		// Like a cell edit, it does not refetch the rows.
		editEvalDatasetCells: builder.mutation<
			DatasetCellEditResult,
			{ id: string; updates: { id: string; cells: Record<string, string> }[]; rows?: Record<string, string>[]; splitId?: string }
		>({
			query: ({ id, updates, rows, splitId }) => ({
				url: `${base}/${id}/rows/batch`,
				method: "POST",
				body: { updates, rows: rows ?? [], split_id: splitId },
			}),
			invalidatesTags: (result, error, { id }) => ["EvalDatasets", { type: "EvalDatasets", id }, { type: "EvalDatasetVersions", id }],
		}),

		deleteEvalDatasetRows: builder.mutation<{ deleted: number; version: number | null }, { id: string; rowIds: string[] }>({
			query: ({ id, rowIds }) => ({ url: `${base}/${id}/rows/delete`, method: "POST", body: { row_ids: rowIds } }),
			invalidatesTags: (result, error, { id }) => datasetTags(id),
		}),

		getEvalDatasetRowVariables: builder.query<{ variables: Record<string, string> }, { id: string; rowId: string; version?: number }>({
			query: ({ id, rowId, version }) => `${base}/${id}/rows/${rowId}/variables${queryString({ version })}`,
		}),

		addEvalDatasetRowsFromLogs: builder.mutation<DatasetCurateResult, { id: string; logIds: string[]; splitId?: string }>({
			query: ({ id, logIds, splitId }) => ({
				url: `${base}/${id}/rows/from-logs`,
				method: "POST",
				body: { log_ids: logIds, split_id: splitId },
			}),
			invalidatesTags: (result, error, { id }) => datasetTags(id),
		}),

		// Rows curated from spans of traces pushed by observability SDKs.
		addEvalDatasetRowsFromSpans: builder.mutation<
			DatasetCurateResult,
			{ id: string; spans: { project_id: string; trace_id: string; span_id: string }[]; splitId?: string }
		>({
			query: ({ id, spans, splitId }) => ({
				url: `${base}/${id}/rows/from-spans`,
				method: "POST",
				body: { spans, split_id: splitId },
			}),
			invalidatesTags: (result, error, { id }) => datasetTags(id),
		}),

		addEvalDatasetRowsFromSessions: builder.mutation<DatasetCurateResult, { id: string; sessionIds: string[]; splitId?: string }>({
			query: ({ id, sessionIds, splitId }) => ({
				url: `${base}/${id}/rows/from-sessions`,
				method: "POST",
				body: { session_ids: sessionIds, split_id: splitId },
			}),
			invalidatesTags: (result, error, { id }) => datasetTags(id),
		}),

		// Import: the CSV text is the request body, the options are query parameters.
		previewEvalDatasetImport: builder.mutation<DatasetImportPreview, { id: string; csv: string }>({
			query: ({ id, csv }) => ({
				url: `${base}/${id}/import/preview`,
				method: "POST",
				body: csv,
				headers: { "Content-Type": "text/csv" },
			}),
		}),

		importEvalDatasetCSV: builder.mutation<
			DatasetImportResult,
			{ id: string; file: Blob; hasHeader: boolean; mapping?: DatasetImportColumn[]; createColumns?: boolean; splitId?: string }
		>({
			query: ({ id, file, hasHeader, mapping, createColumns, splitId }) => ({
				url: `${base}/${id}/import${queryString({
					has_header: hasHeader,
					mapping: mapping ? JSON.stringify(mapping) : undefined,
					create_columns: createColumns ? true : undefined,
					split_id: splitId,
				})}`,
				method: "POST",
				body: file,
				headers: { "Content-Type": "text/csv" },
			}),
			invalidatesTags: (result, error, { id }) => datasetTags(id),
		}),

		// Versions
		getEvalDatasetVersions: builder.query<{ versions: DatasetVersion[] }, string>({
			query: (id) => `${base}/${id}/versions`,
			providesTags: (result, error, id) => [{ type: "EvalDatasetVersions", id }],
		}),

		saveEvalDatasetVersion: builder.mutation<{ version: DatasetVersion }, { id: string; name?: string; description?: string }>({
			query: ({ id, name, description }) => ({ url: `${base}/${id}/versions`, method: "POST", body: { name, description } }),
			invalidatesTags: (result, error, { id }) => datasetTags(id),
		}),

		updateEvalDatasetVersion: builder.mutation<
			{ version: DatasetVersion },
			{ id: string; number: number; name: string; description?: string }
		>({
			query: ({ id, number, name, description }) => ({
				url: `${base}/${id}/versions/${number}`,
				method: "PUT",
				body: { name, description },
			}),
			invalidatesTags: (result, error, { id }) => [{ type: "EvalDatasetVersions", id }],
		}),

		deleteEvalDatasetVersion: builder.mutation<{ deleted: boolean }, { id: string; number: number }>({
			query: ({ id, number }) => ({ url: `${base}/${id}/versions/${number}`, method: "DELETE" }),
			invalidatesTags: (result, error, { id }) => [{ type: "EvalDatasetVersions", id }],
		}),

		restoreEvalDatasetVersion: builder.mutation<{ version: DatasetVersion }, { id: string; number: number }>({
			query: ({ id, number }) => ({ url: `${base}/${id}/versions/${number}/restore`, method: "POST" }),
			invalidatesTags: (result, error, { id }) => datasetTags(id),
		}),

		// `number` may be "draft" to compare the unsaved draft with the last saved version.
		getEvalDatasetDiff: builder.query<DatasetDiff, { id: string; number: number | "draft"; against?: number; after?: number }>({
			query: ({ id, number, against, after }) => `${base}/${id}/versions/${number}/diff${queryString({ against, after })}`,
			providesTags: (result, error, { id }) => [
				{ type: "EvalDatasetVersions", id },
				{ type: "EvalDatasetRows", id },
			],
		}),

		// Splits
		createEvalDatasetSplit: builder.mutation<{ split: DatasetSplit }, { id: string; name: string; description?: string }>({
			query: ({ id, name, description }) => ({ url: `${base}/${id}/splits`, method: "POST", body: { name, description } }),
			invalidatesTags: (result, error, { id }) => ["EvalDatasets", { type: "EvalDatasets", id }],
		}),

		deleteEvalDatasetSplit: builder.mutation<{ deleted: boolean }, { id: string; splitId: string }>({
			query: ({ id, splitId }) => ({ url: `${base}/${id}/splits/${splitId}`, method: "DELETE" }),
			invalidatesTags: (result, error, { id }) => datasetTags(id),
		}),

		setEvalDatasetSplitRows: builder.mutation<
			{ added: number; removed: number },
			{ id: string; splitId: string; add?: string[]; remove?: string[] }
		>({
			query: ({ id, splitId, add, remove }) => ({
				url: `${base}/${id}/splits/${splitId}/rows`,
				method: "POST",
				body: { add: add ?? [], remove: remove ?? [] },
			}),
			invalidatesTags: (result, error, { id }) => datasetTags(id),
		}),
	}),
});

export const {
	useGetEvalDatasetsQuery,
	useGetEvalDatasetQuery,
	useCreateEvalDatasetMutation,
	useUpdateEvalDatasetMutation,
	useDeleteEvalDatasetMutation,
	useDuplicateEvalDatasetMutation,
	useMoveEvalDatasetMutation,
	useUploadEvalDatasetFileMutation,
	useGetEvalDatasetFoldersQuery,
	useCreateEvalDatasetFolderMutation,
	useUpdateEvalDatasetFolderMutation,
	useMoveEvalDatasetFolderMutation,
	useDeleteEvalDatasetFolderMutation,
	useAddEvalDatasetColumnMutation,
	useUpdateEvalDatasetColumnMutation,
	useDeleteEvalDatasetColumnMutation,
	useReorderEvalDatasetColumnsMutation,
	useLazyGetEvalDatasetRowsQuery,
	useGetEvalDatasetRowsQuery,
	useAddEvalDatasetRowsMutation,
	useUpdateEvalDatasetRowMutation,
	useEditEvalDatasetCellsMutation,
	useDeleteEvalDatasetRowsMutation,
	useLazyGetEvalDatasetRowVariablesQuery,
	useAddEvalDatasetRowsFromLogsMutation,
	useAddEvalDatasetRowsFromSpansMutation,
	useAddEvalDatasetRowsFromSessionsMutation,
	usePreviewEvalDatasetImportMutation,
	useImportEvalDatasetCSVMutation,
	useGetEvalDatasetVersionsQuery,
	useSaveEvalDatasetVersionMutation,
	useUpdateEvalDatasetVersionMutation,
	useDeleteEvalDatasetVersionMutation,
	useRestoreEvalDatasetVersionMutation,
	useGetEvalDatasetDiffQuery,
	useCreateEvalDatasetSplitMutation,
	useDeleteEvalDatasetSplitMutation,
	useSetEvalDatasetSplitRowsMutation,
} = evalDatasetsApi;