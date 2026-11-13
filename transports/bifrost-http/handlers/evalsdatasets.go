package handlers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"strconv"
	"strings"

	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/evals/datasets"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// evalsDatasetDraft is the path value that names the unsaved draft in a diff.
const evalsDatasetDraft = "draft"

// EvalsDatasetsHandler serves the dataset API: datasets, their columns, rows, versions
// and splits, CSV import and export, and rows built from logs and sessions.
type EvalsDatasetsHandler struct {
	service *datasets.Service
}

// NewEvalsDatasetsHandler creates an EvalsDatasetsHandler. It returns nil without a
// service, which is the case when no config store is configured.
func NewEvalsDatasetsHandler(service *datasets.Service) *EvalsDatasetsHandler {
	if service == nil {
		return nil
	}
	return &EvalsDatasetsHandler{service: service}
}

// RegisterRoutes registers the routes for the EvalsDatasetsHandler
func (h *EvalsDatasetsHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	route := func(method, path string, handler fasthttp.RequestHandler) {
		r.Handle(method, "/api/evals/datasets"+path, lib.ChainMiddlewares(handler, middlewares...))
	}
	// Folders. These are registered before the dataset routes they share a prefix with.
	route(fasthttp.MethodGet, "/folders", h.listFolders)
	route(fasthttp.MethodPost, "/folders", h.createFolder)
	route(fasthttp.MethodPut, "/folders/{folderId}", h.updateFolder)
	route(fasthttp.MethodPut, "/folders/{folderId}/parent", h.moveFolder)
	route(fasthttp.MethodDelete, "/folders/{folderId}", h.deleteFolder)

	// Datasets
	route(fasthttp.MethodGet, "", h.listDatasets)
	route(fasthttp.MethodPost, "", h.createDataset)
	route(fasthttp.MethodGet, "/{id}", h.getDataset)
	route(fasthttp.MethodPut, "/{id}", h.updateDataset)
	route(fasthttp.MethodDelete, "/{id}", h.deleteDataset)
	route(fasthttp.MethodPost, "/{id}/duplicate", h.duplicateDataset)
	route(fasthttp.MethodPut, "/{id}/folder", h.moveDataset)

	// Columns
	route(fasthttp.MethodPost, "/{id}/columns", h.addColumn)
	route(fasthttp.MethodPut, "/{id}/columns/order", h.reorderColumns)
	route(fasthttp.MethodPut, "/{id}/columns/{columnId}", h.updateColumn)
	route(fasthttp.MethodDelete, "/{id}/columns/{columnId}", h.deleteColumn)

	// Files
	route(fasthttp.MethodPost, "/{id}/files", h.uploadFile)
	route(fasthttp.MethodGet, "/{id}/files/{fileId}", h.getFile)

	// Rows
	route(fasthttp.MethodGet, "/{id}/rows", h.listRows)
	route(fasthttp.MethodPost, "/{id}/rows", h.addRows)
	route(fasthttp.MethodPost, "/{id}/rows/batch", h.editCells)
	route(fasthttp.MethodPost, "/{id}/rows/delete", h.deleteRows)
	route(fasthttp.MethodPost, "/{id}/rows/from-logs", h.addFromLogs)
	route(fasthttp.MethodPost, "/{id}/rows/from-spans", h.addFromSpans)
	route(fasthttp.MethodPost, "/{id}/rows/from-sessions", h.addFromSessions)
	route(fasthttp.MethodPatch, "/{id}/rows/{rowId}", h.updateRow)
	route(fasthttp.MethodGet, "/{id}/rows/{rowId}/variables", h.rowVariables)

	// Import and export
	route(fasthttp.MethodPost, "/{id}/import", h.importCSV)
	route(fasthttp.MethodPost, "/{id}/import/preview", h.previewImport)
	route(fasthttp.MethodGet, "/{id}/export", h.exportDataset)

	// Versions
	route(fasthttp.MethodGet, "/{id}/versions", h.listVersions)
	route(fasthttp.MethodPost, "/{id}/versions", h.saveVersion)
	route(fasthttp.MethodGet, "/{id}/versions/{number}", h.getVersion)
	route(fasthttp.MethodPut, "/{id}/versions/{number}", h.updateVersion)
	route(fasthttp.MethodDelete, "/{id}/versions/{number}", h.deleteVersion)
	route(fasthttp.MethodGet, "/{id}/versions/{number}/diff", h.diffVersion)
	route(fasthttp.MethodPost, "/{id}/versions/{number}/restore", h.restoreVersion)

	// Splits
	route(fasthttp.MethodPost, "/{id}/splits", h.createSplit)
	route(fasthttp.MethodPut, "/{id}/splits/{splitId}", h.updateSplit)
	route(fasthttp.MethodDelete, "/{id}/splits/{splitId}", h.deleteSplit)
	route(fasthttp.MethodPost, "/{id}/splits/{splitId}/rows", h.setSplitRows)
}

// decodeBody reads a JSON request body into v and answers 400 when it cannot.
func decodeBody(ctx *fasthttp.RequestCtx, v any) bool {
	if err := json.Unmarshal(ctx.PostBody(), v); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "invalid request body")
		return false
	}
	return true
}

// queryInt reads an optional non-negative integer query parameter.
func queryInt(ctx *fasthttp.RequestCtx, name string) (int, bool) {
	raw := string(ctx.QueryArgs().Peek(name))
	if raw == "" {
		return 0, true
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		SendError(ctx, fasthttp.StatusBadRequest, name+" must be a number that is not negative")
		return 0, false
	}
	return value, true
}

// versionNumber reads the {number} path parameter as a saved version number.
func versionNumber(ctx *fasthttp.RequestCtx) (int, bool) {
	number, err := strconv.Atoi(pathValue(ctx, "number"))
	if err != nil || number < 1 {
		SendError(ctx, fasthttp.StatusBadRequest, "the version must be a positive number")
		return 0, false
	}
	return number, true
}

// listDatasets handles GET /api/evals/datasets
func (h *EvalsDatasetsHandler) listDatasets(ctx *fasthttp.RequestCtx) {
	limit, ok := queryInt(ctx, "limit")
	if !ok {
		return
	}
	offset, ok := queryInt(ctx, "offset")
	if !ok {
		return
	}
	if limit == 0 {
		limit = evalsDefaultPageSize
	}
	limit = min(limit, evalsMaxPageSize)
	args := ctx.QueryArgs()
	list, total, err := h.service.List(ctx, configstore.EvalDatasetListParams{
		Search: string(args.Peek("search")), Tag: string(args.Peek("tag")), Limit: limit, Offset: offset,
	})
	if err != nil {
		sendEvalsError(ctx, err)
		return
	}
	SendJSON(ctx, map[string]any{"datasets": list, "total": total, "limit": limit, "offset": offset})
}

// createDataset handles POST /api/evals/datasets
func (h *EvalsDatasetsHandler) createDataset(ctx *fasthttp.RequestCtx) {
	var req datasets.DatasetInput
	if !decodeBody(ctx, &req) {
		return
	}
	req.CreatedBy = evalsCaller(ctx)
	dataset, err := h.service.Create(ctx, req)
	if err != nil {
		sendEvalsError(ctx, err)
		return
	}
	lib.RecordAuditChange(ctx, "dataset.created", "dataset", dataset.ID, dataset.Name, nil, dataset)
	SendJSON(ctx, map[string]any{"dataset": dataset})
}

// getDataset handles GET /api/evals/datasets/{id}
func (h *EvalsDatasetsHandler) getDataset(ctx *fasthttp.RequestCtx) {
	dataset, err := h.service.Get(ctx, pathValue(ctx, "id"))
	if err != nil {
		sendEvalsError(ctx, err)
		return
	}
	SendJSON(ctx, map[string]any{"dataset": dataset})
}

// updateDataset handles PUT /api/evals/datasets/{id}
func (h *EvalsDatasetsHandler) updateDataset(ctx *fasthttp.RequestCtx) {
	var req datasets.DatasetInput
	if !decodeBody(ctx, &req) {
		return
	}
	id := pathValue(ctx, "id")
	before, err := h.service.Get(ctx, id)
	if err != nil {
		sendEvalsError(ctx, err)
		return
	}
	dataset, err := h.service.Update(ctx, id, req)
	if err != nil {
		sendEvalsError(ctx, err)
		return
	}
	lib.RecordAuditChange(ctx, "dataset.updated", "dataset", dataset.ID, dataset.Name, before.TableEvalDataset, dataset.TableEvalDataset)
	SendJSON(ctx, map[string]any{"dataset": dataset})
}

// moveDataset handles PUT /api/evals/datasets/{id}/folder
func (h *EvalsDatasetsHandler) moveDataset(ctx *fasthttp.RequestCtx) {
	var req struct {
		FolderID *string `json:"folder_id"`
	}
	if !decodeBody(ctx, &req) {
		return
	}
	dataset, err := h.service.Move(ctx, pathValue(ctx, "id"), req.FolderID)
	if err != nil {
		sendEvalsError(ctx, err)
		return
	}
	SendJSON(ctx, map[string]any{"dataset": dataset})
}

// listFolders handles GET /api/evals/datasets/folders
func (h *EvalsDatasetsHandler) listFolders(ctx *fasthttp.RequestCtx) {
	folders, err := h.service.Folders(ctx)
	if err != nil {
		sendEvalsError(ctx, err)
		return
	}
	SendJSON(ctx, map[string]any{"folders": folders})
}

// createFolder handles POST /api/evals/datasets/folders
func (h *EvalsDatasetsHandler) createFolder(ctx *fasthttp.RequestCtx) {
	var req datasets.FolderInput
	if !decodeBody(ctx, &req) {
		return
	}
	folder, err := h.service.CreateFolder(ctx, req)
	if err != nil {
		sendEvalsError(ctx, err)
		return
	}
	lib.RecordAuditChange(ctx, "dataset_folder.created", "dataset_folder", folder.ID, folder.Name, nil, folder)
	SendJSON(ctx, map[string]any{"folder": folder})
}

// updateFolder handles PUT /api/evals/datasets/folders/{folderId}
func (h *EvalsDatasetsHandler) updateFolder(ctx *fasthttp.RequestCtx) {
	var req datasets.FolderInput
	if !decodeBody(ctx, &req) {
		return
	}
	folder, err := h.service.UpdateFolder(ctx, pathValue(ctx, "folderId"), req)
	if err != nil {
		sendEvalsError(ctx, err)
		return
	}
	lib.RecordAuditChange(ctx, "dataset_folder.updated", "dataset_folder", folder.ID, folder.Name, nil, folder)
	SendJSON(ctx, map[string]any{"folder": folder})
}

// moveFolder handles PUT /api/evals/datasets/folders/{folderId}/parent
func (h *EvalsDatasetsHandler) moveFolder(ctx *fasthttp.RequestCtx) {
	var req struct {
		ParentID *string `json:"parent_id"`
	}
	if !decodeBody(ctx, &req) {
		return
	}
	folder, err := h.service.MoveFolder(ctx, pathValue(ctx, "folderId"), req.ParentID)
	if err != nil {
		sendEvalsError(ctx, err)
		return
	}
	SendJSON(ctx, map[string]any{"folder": folder})
}

// deleteFolder handles DELETE /api/evals/datasets/folders/{folderId}
func (h *EvalsDatasetsHandler) deleteFolder(ctx *fasthttp.RequestCtx) {
	id := pathValue(ctx, "folderId")
	if err := h.service.DeleteFolder(ctx, id); err != nil {
		sendEvalsError(ctx, err)
		return
	}
	lib.RecordAuditChange(ctx, "dataset_folder.deleted", "dataset_folder", id, "", nil, nil)
	SendJSON(ctx, map[string]any{"deleted": true})
}

// deleteDataset handles DELETE /api/evals/datasets/{id}
func (h *EvalsDatasetsHandler) deleteDataset(ctx *fasthttp.RequestCtx) {
	id := pathValue(ctx, "id")
	before, err := h.service.Get(ctx, id)
	if err != nil {
		sendEvalsError(ctx, err)
		return
	}
	if err := h.service.Delete(ctx, id); err != nil {
		sendEvalsError(ctx, err)
		return
	}
	lib.RecordAuditChange(ctx, "dataset.deleted", "dataset", id, before.Name, before.TableEvalDataset, nil)
	SendJSON(ctx, map[string]any{"deleted": true})
}

// duplicateDataset handles POST /api/evals/datasets/{id}/duplicate
func (h *EvalsDatasetsHandler) duplicateDataset(ctx *fasthttp.RequestCtx) {
	var req struct {
		Name string `json:"name"`
	}
	if len(ctx.PostBody()) > 0 && !decodeBody(ctx, &req) {
		return
	}
	dataset, err := h.service.Duplicate(ctx, pathValue(ctx, "id"), req.Name, evalsCaller(ctx))
	if err != nil {
		sendEvalsError(ctx, err)
		return
	}
	lib.RecordAuditChange(ctx, "dataset.created", "dataset", dataset.ID, dataset.Name, nil, dataset.TableEvalDataset)
	SendJSON(ctx, map[string]any{"dataset": dataset})
}

// addColumn handles POST /api/evals/datasets/{id}/columns
func (h *EvalsDatasetsHandler) addColumn(ctx *fasthttp.RequestCtx) {
	var req datasets.ColumnSpec
	if !decodeBody(ctx, &req) {
		return
	}
	column, err := h.service.AddColumn(ctx, pathValue(ctx, "id"), req, evalsCaller(ctx))
	if err != nil {
		sendEvalsError(ctx, err)
		return
	}
	SendJSON(ctx, map[string]any{"column": column})
}

// updateColumn handles PUT /api/evals/datasets/{id}/columns/{columnId}
func (h *EvalsDatasetsHandler) updateColumn(ctx *fasthttp.RequestCtx) {
	var req datasets.ColumnSpec
	if !decodeBody(ctx, &req) {
		return
	}
	column, err := h.service.UpdateColumn(ctx, pathValue(ctx, "id"), pathValue(ctx, "columnId"), req, evalsCaller(ctx))
	if err != nil {
		sendEvalsError(ctx, err)
		return
	}
	SendJSON(ctx, map[string]any{"column": column})
}

// deleteColumn handles DELETE /api/evals/datasets/{id}/columns/{columnId}
func (h *EvalsDatasetsHandler) deleteColumn(ctx *fasthttp.RequestCtx) {
	if err := h.service.DeleteColumn(ctx, pathValue(ctx, "id"), pathValue(ctx, "columnId"), evalsCaller(ctx)); err != nil {
		sendEvalsError(ctx, err)
		return
	}
	SendJSON(ctx, map[string]any{"deleted": true})
}

// reorderColumns handles PUT /api/evals/datasets/{id}/columns/order
func (h *EvalsDatasetsHandler) reorderColumns(ctx *fasthttp.RequestCtx) {
	var req struct {
		ColumnIDs []string `json:"column_ids"`
	}
	if !decodeBody(ctx, &req) {
		return
	}
	if err := h.service.ReorderColumns(ctx, pathValue(ctx, "id"), req.ColumnIDs, evalsCaller(ctx)); err != nil {
		sendEvalsError(ctx, err)
		return
	}
	SendJSON(ctx, map[string]any{"reordered": true})
}

// inlineFileTypes are the content types a browser may show in place when a dataset
// file is opened. They are images that cannot carry script. Everything else, SVG and
// HTML included, is sent as a download: an uploaded file must never run as a page of
// the gateway's own origin.
var inlineFileTypes = map[string]bool{
	"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true, "image/avif": true, "image/bmp": true,
}

// uploadFile handles POST /api/evals/datasets/{id}/files. The body is the file itself;
// its name comes in the name query parameter and its type in the Content-Type header.
func (h *EvalsDatasetsHandler) uploadFile(ctx *fasthttp.RequestCtx) {
	name := string(ctx.QueryArgs().Peek("name"))
	contentType, _, _ := strings.Cut(string(ctx.Request.Header.ContentType()), ";")
	// The body is copied: the request's buffer is reused once the handler returns.
	data := append([]byte(nil), ctx.PostBody()...)
	file, err := h.service.UploadFile(ctx, pathValue(ctx, "id"), name, strings.TrimSpace(contentType), data, evalsCaller(ctx))
	if err != nil {
		sendEvalsError(ctx, err)
		return
	}
	SendJSON(ctx, map[string]any{"file": file})
}

// getFile handles GET /api/evals/datasets/{id}/files/{fileId}
func (h *EvalsDatasetsHandler) getFile(ctx *fasthttp.RequestCtx) {
	file, data, err := h.service.File(ctx, pathValue(ctx, "id"), pathValue(ctx, "fileId"))
	if err != nil {
		sendEvalsError(ctx, err)
		return
	}
	disposition, contentType := "attachment", "application/octet-stream"
	if inlineFileTypes[strings.ToLower(file.ContentType)] {
		disposition, contentType = "inline", file.ContentType
	}
	ctx.Response.Header.SetContentType(contentType)
	ctx.Response.Header.Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": file.Name}))
	ctx.Response.Header.Set("X-Content-Type-Options", "nosniff")
	ctx.Response.Header.Set("Content-Security-Policy", "sandbox; default-src 'none'")
	ctx.Response.Header.Set("Cache-Control", "private, max-age=3600")
	ctx.SetBody(data)
}

// listRows handles GET /api/evals/datasets/{id}/rows
func (h *EvalsDatasetsHandler) listRows(ctx *fasthttp.RequestCtx) {
	version, ok := queryInt(ctx, "version")
	if !ok {
		return
	}
	limit, ok := queryInt(ctx, "limit")
	if !ok {
		return
	}
	after, ok := queryInt(ctx, "after")
	if !ok {
		return
	}
	offset, ok := queryInt(ctx, "offset")
	if !ok {
		return
	}
	args := ctx.QueryArgs()
	page, err := h.service.Rows(ctx, pathValue(ctx, "id"), datasets.RowQuery{
		Version: version, SplitID: string(args.Peek("split")), Search: string(args.Peek("search")), After: int64(after), Offset: offset, Limit: limit,
	})
	if err != nil {
		sendEvalsError(ctx, err)
		return
	}
	SendJSON(ctx, page)
}

// addRows handles POST /api/evals/datasets/{id}/rows
func (h *EvalsDatasetsHandler) addRows(ctx *fasthttp.RequestCtx) {
	var req struct {
		Rows    []map[string]string `json:"rows"`
		SplitID string              `json:"split_id"`
	}
	if !decodeBody(ctx, &req) {
		return
	}
	result, err := h.service.AddRows(ctx, pathValue(ctx, "id"), req.Rows, req.SplitID, evalsCaller(ctx))
	if err != nil {
		sendEvalsError(ctx, err)
		return
	}
	SendJSON(ctx, result)
}

// updateRow handles PATCH /api/evals/datasets/{id}/rows/{rowId}
func (h *EvalsDatasetsHandler) updateRow(ctx *fasthttp.RequestCtx) {
	var req struct {
		Cells map[string]string `json:"cells"`
	}
	if !decodeBody(ctx, &req) {
		return
	}
	result, err := h.service.UpdateRow(ctx, pathValue(ctx, "id"), pathValue(ctx, "rowId"), req.Cells, evalsCaller(ctx))
	if err != nil {
		sendEvalsError(ctx, err)
		return
	}
	SendJSON(ctx, map[string]any{"row": result.Rows[0], "version": result.Version})
}

// editCells handles POST /api/evals/datasets/{id}/rows/batch
func (h *EvalsDatasetsHandler) editCells(ctx *fasthttp.RequestCtx) {
	var req datasets.CellEdits
	if !decodeBody(ctx, &req) {
		return
	}
	result, err := h.service.EditCells(ctx, pathValue(ctx, "id"), req, evalsCaller(ctx))
	if err != nil {
		sendEvalsError(ctx, err)
		return
	}
	SendJSON(ctx, result)
}

// deleteRows handles POST /api/evals/datasets/{id}/rows/delete
func (h *EvalsDatasetsHandler) deleteRows(ctx *fasthttp.RequestCtx) {
	var req struct {
		RowIDs []string `json:"row_ids"`
	}
	if !decodeBody(ctx, &req) {
		return
	}
	result, err := h.service.DeleteRows(ctx, pathValue(ctx, "id"), req.RowIDs, evalsCaller(ctx))
	if err != nil {
		sendEvalsError(ctx, err)
		return
	}
	SendJSON(ctx, map[string]any{"deleted": result.Count, "version": result.Version})
}

// rowVariables handles GET /api/evals/datasets/{id}/rows/{rowId}/variables
func (h *EvalsDatasetsHandler) rowVariables(ctx *fasthttp.RequestCtx) {
	version, ok := queryInt(ctx, "version")
	if !ok {
		return
	}
	variables, err := h.service.RowVariables(ctx, pathValue(ctx, "id"), pathValue(ctx, "rowId"), version)
	if err != nil {
		sendEvalsError(ctx, err)
		return
	}
	SendJSON(ctx, map[string]any{"variables": variables})
}

// addFromSpans handles POST /api/evals/datasets/{id}/rows/from-spans: rows curated from spans of
// traces pushed by observability SDKs.
func (h *EvalsDatasetsHandler) addFromSpans(ctx *fasthttp.RequestCtx) {
	var req struct {
		Spans   []datasets.SpanRef `json:"spans"`
		SplitID string             `json:"split_id"`
	}
	if !decodeBody(ctx, &req) {
		return
	}
	result, err := h.service.AddFromSpans(ctx, pathValue(ctx, "id"), req.Spans, req.SplitID, evalsCaller(ctx))
	if err != nil {
		sendEvalsError(ctx, err)
		return
	}
	SendJSON(ctx, result)
}

// addFromLogs handles POST /api/evals/datasets/{id}/rows/from-logs
func (h *EvalsDatasetsHandler) addFromLogs(ctx *fasthttp.RequestCtx) {
	var req struct {
		LogIDs  []string `json:"log_ids"`
		SplitID string   `json:"split_id"`
	}
	if !decodeBody(ctx, &req) {
		return
	}
	result, err := h.service.AddFromLogs(ctx, pathValue(ctx, "id"), req.LogIDs, req.SplitID, evalsCaller(ctx))
	if err != nil {
		sendEvalsError(ctx, err)
		return
	}
	SendJSON(ctx, result)
}

// addFromSessions handles POST /api/evals/datasets/{id}/rows/from-sessions
func (h *EvalsDatasetsHandler) addFromSessions(ctx *fasthttp.RequestCtx) {
	var req struct {
		SessionIDs []string `json:"session_ids"`
		SplitID    string   `json:"split_id"`
	}
	if !decodeBody(ctx, &req) {
		return
	}
	result, err := h.service.AddFromSessions(ctx, pathValue(ctx, "id"), req.SessionIDs, req.SplitID, evalsCaller(ctx))
	if err != nil {
		sendEvalsError(ctx, err)
		return
	}
	SendJSON(ctx, result)
}

// requestBody returns the request body as a stream. The gateway's request body limit
// has already been applied to it.
func requestBody(ctx *fasthttp.RequestCtx) io.Reader {
	if stream := ctx.RequestBodyStream(); stream != nil {
		return stream
	}
	return bytes.NewReader(ctx.PostBody())
}

// importCSV handles POST /api/evals/datasets/{id}/import. The CSV is the request
// body; the options are query parameters.
func (h *EvalsDatasetsHandler) importCSV(ctx *fasthttp.RequestCtx) {
	args := ctx.QueryArgs()
	opts := datasets.ImportOptions{
		HasHeader:     string(args.Peek("has_header")) != "false",
		CreateColumns: string(args.Peek("create_columns")) == "true",
		SplitID:       string(args.Peek("split_id")),
		Actor:         evalsCaller(ctx),
	}
	if raw := args.Peek("mapping"); len(raw) > 0 {
		if err := json.Unmarshal(raw, &opts.Mapping); err != nil {
			SendError(ctx, fasthttp.StatusBadRequest, "mapping must be a JSON list of {index, column_id} or {index, name, type}")
			return
		}
	}
	id := pathValue(ctx, "id")
	result, err := h.service.Import(ctx, id, requestBody(ctx), opts)
	if err != nil {
		sendEvalsError(ctx, err)
		return
	}
	lib.RecordAuditChange(ctx, "dataset.imported", "dataset", id, "", nil, result)
	SendJSON(ctx, result)
}

// previewImport handles POST /api/evals/datasets/{id}/import/preview
func (h *EvalsDatasetsHandler) previewImport(ctx *fasthttp.RequestCtx) {
	preview, err := h.service.PreviewImport(ctx, pathValue(ctx, "id"), requestBody(ctx))
	if err != nil {
		sendEvalsError(ctx, err)
		return
	}
	SendJSON(ctx, preview)
}

// exportDataset handles GET /api/evals/datasets/{id}/export. The rows are streamed:
// the response is written page by page and never held whole.
func (h *EvalsDatasetsHandler) exportDataset(ctx *fasthttp.RequestCtx) {
	version, ok := queryInt(ctx, "version")
	if !ok {
		return
	}
	args := ctx.QueryArgs()
	format := string(args.Peek("format"))
	if format == "" {
		format = datasets.FormatCSV
	}
	if format != datasets.FormatCSV && format != datasets.FormatJSONL {
		SendError(ctx, fasthttp.StatusBadRequest, "format must be csv or jsonl")
		return
	}
	id, splitID := pathValue(ctx, "id"), string(args.Peek("split"))
	// Anything that can fail with a status is checked before the stream starts.
	dataset, err := h.service.Get(ctx, id)
	if err != nil {
		sendEvalsError(ctx, err)
		return
	}
	if _, err := h.service.Rows(ctx, id, datasets.RowQuery{Version: version, SplitID: splitID, Limit: 1}); err != nil {
		sendEvalsError(ctx, err)
		return
	}

	contentType := "text/csv; charset=utf-8"
	if format == datasets.FormatJSONL {
		contentType = "application/x-ndjson"
	}
	ctx.Response.Header.SetContentType(contentType)
	ctx.Response.Header.Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", exportFileName(dataset.Name, version, format)))

	streamCtx, cancel := context.WithCancel(context.Background())
	requestDone := ctx.Done()
	go func() {
		select {
		case <-requestDone:
			cancel()
		case <-streamCtx.Done():
		}
	}()
	ctx.Response.SetBodyStreamWriter(func(w *bufio.Writer) {
		defer cancel()
		if err := h.service.Export(streamCtx, id, w, format, version, splitID); err != nil {
			logger.Error("dataset export %s stopped: %v", id, err)
		}
		_ = w.Flush()
	})
}

// exportFileName builds a download name from the dataset name, keeping only
// characters that are safe in a header and on a file system.
func exportFileName(name string, version int, format string) string {
	safe := make([]rune, 0, len(name))
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			safe = append(safe, r)
		default:
			safe = append(safe, '-')
		}
	}
	if len(safe) == 0 {
		safe = []rune("dataset")
	}
	if version > 0 {
		return fmt.Sprintf("%s-v%d.%s", string(safe), version, format)
	}
	return fmt.Sprintf("%s.%s", string(safe), format)
}

// listVersions handles GET /api/evals/datasets/{id}/versions
func (h *EvalsDatasetsHandler) listVersions(ctx *fasthttp.RequestCtx) {
	versions, err := h.service.Versions(ctx, pathValue(ctx, "id"))
	if err != nil {
		sendEvalsError(ctx, err)
		return
	}
	SendJSON(ctx, map[string]any{"versions": versions})
}

// saveVersion handles POST /api/evals/datasets/{id}/versions
func (h *EvalsDatasetsHandler) saveVersion(ctx *fasthttp.RequestCtx) {
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if len(ctx.PostBody()) > 0 && !decodeBody(ctx, &req) {
		return
	}
	id := pathValue(ctx, "id")
	version, err := h.service.SaveVersion(ctx, id, req.Name, req.Description, evalsCaller(ctx))
	if err != nil {
		sendEvalsError(ctx, err)
		return
	}
	lib.RecordAuditChange(ctx, "dataset.version_saved", "dataset", id, strconv.Itoa(version.Number), nil, version)
	SendJSON(ctx, map[string]any{"version": version})
}

// getVersion handles GET /api/evals/datasets/{id}/versions/{number}
func (h *EvalsDatasetsHandler) getVersion(ctx *fasthttp.RequestCtx) {
	number, ok := versionNumber(ctx)
	if !ok {
		return
	}
	version, err := h.service.Version(ctx, pathValue(ctx, "id"), number)
	if err != nil {
		sendEvalsError(ctx, err)
		return
	}
	SendJSON(ctx, map[string]any{"version": version})
}

// updateVersion handles PUT /api/evals/datasets/{id}/versions/{number}
func (h *EvalsDatasetsHandler) updateVersion(ctx *fasthttp.RequestCtx) {
	number, ok := versionNumber(ctx)
	if !ok {
		return
	}
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if !decodeBody(ctx, &req) {
		return
	}
	version, err := h.service.UpdateVersion(ctx, pathValue(ctx, "id"), number, req.Name, req.Description)
	if err != nil {
		sendEvalsError(ctx, err)
		return
	}
	SendJSON(ctx, map[string]any{"version": version})
}

// deleteVersion handles DELETE /api/evals/datasets/{id}/versions/{number}
func (h *EvalsDatasetsHandler) deleteVersion(ctx *fasthttp.RequestCtx) {
	number, ok := versionNumber(ctx)
	if !ok {
		return
	}
	id := pathValue(ctx, "id")
	if err := h.service.DeleteVersion(ctx, id, number); err != nil {
		sendEvalsError(ctx, err)
		return
	}
	lib.RecordAuditChange(ctx, "dataset.version_deleted", "dataset", id, strconv.Itoa(number), nil, nil)
	SendJSON(ctx, map[string]any{"deleted": true})
}

// diffVersion handles GET /api/evals/datasets/{id}/versions/{number}/diff. The
// number may be "draft" to compare the unsaved draft with the last saved version.
func (h *EvalsDatasetsHandler) diffVersion(ctx *fasthttp.RequestCtx) {
	id := pathValue(ctx, "id")
	var to int
	if pathValue(ctx, "number") == evalsDatasetDraft {
		dataset, err := h.service.Get(ctx, id)
		if err != nil {
			sendEvalsError(ctx, err)
			return
		}
		to = dataset.HeadVersion + 1
	} else {
		number, ok := versionNumber(ctx)
		if !ok {
			return
		}
		to = number
	}
	var against *int
	if raw := string(ctx.QueryArgs().Peek("against")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 {
			SendError(ctx, fasthttp.StatusBadRequest, "against must be a version number")
			return
		}
		against = new(value)
	}
	after, ok := queryInt(ctx, "after")
	if !ok {
		return
	}
	limit, ok := queryInt(ctx, "limit")
	if !ok {
		return
	}
	diff, err := h.service.Diff(ctx, id, to, against, int64(after), limit)
	if err != nil {
		sendEvalsError(ctx, err)
		return
	}
	SendJSON(ctx, diff)
}

// restoreVersion handles POST /api/evals/datasets/{id}/versions/{number}/restore
func (h *EvalsDatasetsHandler) restoreVersion(ctx *fasthttp.RequestCtx) {
	number, ok := versionNumber(ctx)
	if !ok {
		return
	}
	id := pathValue(ctx, "id")
	version, err := h.service.Restore(ctx, id, number, evalsCaller(ctx))
	if err != nil {
		sendEvalsError(ctx, err)
		return
	}
	lib.RecordAuditChange(ctx, "dataset.version_restored", "dataset", id, strconv.Itoa(number), nil, version)
	SendJSON(ctx, map[string]any{"version": version})
}

// createSplit handles POST /api/evals/datasets/{id}/splits
func (h *EvalsDatasetsHandler) createSplit(ctx *fasthttp.RequestCtx) {
	var req datasets.SplitInput
	if !decodeBody(ctx, &req) {
		return
	}
	split, err := h.service.CreateSplit(ctx, pathValue(ctx, "id"), req)
	if err != nil {
		sendEvalsError(ctx, err)
		return
	}
	SendJSON(ctx, map[string]any{"split": split})
}

// updateSplit handles PUT /api/evals/datasets/{id}/splits/{splitId}
func (h *EvalsDatasetsHandler) updateSplit(ctx *fasthttp.RequestCtx) {
	var req datasets.SplitInput
	if !decodeBody(ctx, &req) {
		return
	}
	split, err := h.service.UpdateSplit(ctx, pathValue(ctx, "id"), pathValue(ctx, "splitId"), req)
	if err != nil {
		sendEvalsError(ctx, err)
		return
	}
	SendJSON(ctx, map[string]any{"split": split})
}

// deleteSplit handles DELETE /api/evals/datasets/{id}/splits/{splitId}
func (h *EvalsDatasetsHandler) deleteSplit(ctx *fasthttp.RequestCtx) {
	if err := h.service.DeleteSplit(ctx, pathValue(ctx, "id"), pathValue(ctx, "splitId")); err != nil {
		sendEvalsError(ctx, err)
		return
	}
	SendJSON(ctx, map[string]any{"deleted": true})
}

// setSplitRows handles POST /api/evals/datasets/{id}/splits/{splitId}/rows
func (h *EvalsDatasetsHandler) setSplitRows(ctx *fasthttp.RequestCtx) {
	var req struct {
		Add    []string `json:"add"`
		Remove []string `json:"remove"`
	}
	if !decodeBody(ctx, &req) {
		return
	}
	added, removed, err := h.service.SetSplitRows(ctx, pathValue(ctx, "id"), pathValue(ctx, "splitId"), req.Add, req.Remove)
	if err != nil {
		sendEvalsError(ctx, err)
		return
	}
	SendJSON(ctx, map[string]any{"added": added, "removed": removed})
}
