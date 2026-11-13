package handlers

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/evals"
	"github.com/maximhq/bifrost/framework/evals/datasets"
	"github.com/maximhq/bifrost/framework/traceingest"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// Columns of a dataset created through an SDK. Input and expected output are the two every SDK
// sends; metadata and the SDK's own row id are kept as variables.
const (
	sdkDatasetInputColumn    = "input"
	sdkDatasetExpectedColumn = "expected_output"
	sdkDatasetMetadataColumn = "metadata"
	sdkDatasetRowIDColumn    = "sdk_row_id"
	sdkDatasetActor          = "tracing-sdk"
)

// SetDatasetService gives the ingest handler the evals dataset service, so SDK dataset calls
// create and fill evals datasets. Without one, the dataset routes answer 404.
func (h *TraceIngestHandler) SetDatasetService(service *datasets.Service) {
	h.datasets = service
}

func init() {
	traceIngestAdapters = append(traceIngestAdapters, func(h *TraceIngestHandler, rt *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
		chain := func(handler fasthttp.RequestHandler) fasthttp.RequestHandler {
			return lib.ChainMiddlewares(handler, middlewares...)
		}
		// Braintrust: init_dataset(). Its rows then arrive through /logs3 with a dataset_id.
		rt.POST(braintrustPrefix+"/api/dataset/register", chain(h.braintrustRegisterDataset))
		// Langfuse: create_dataset(), get_dataset() and create_dataset_item().
		api := langfusePrefix + "/api/public"
		rt.POST(api+"/v2/datasets", chain(h.langfuseCreateDataset))
		rt.GET(api+"/v2/datasets", chain(h.langfuseListDatasets))
		rt.GET(api+"/v2/datasets/{name}", chain(h.langfuseGetDataset))
		rt.POST(api+"/dataset-items", chain(h.langfuseCreateDatasetItem))
		rt.GET(api+"/dataset-items", chain(h.langfuseListDatasetItems))
	})
}

// datasetUser authenticates a dataset call. Using datasets is a permission a tracing key has only
// when it was given: by default a key writes traces and does nothing else.
func (h *TraceIngestHandler) datasetUser(ctx *fasthttp.RequestCtx) (*traceingest.Principal, bool) {
	principal, ok := h.authenticate(ctx, bearerCredential(string(ctx.Request.Header.Peek("Authorization"))))
	if !ok {
		return nil, false
	}
	if !principal.CanUseDatasets && !principal.AllProjects {
		SendError(ctx, fasthttp.StatusForbidden, "this tracing key may not use datasets. Turn on dataset access for the key under Observability > Tracing.")
		return nil, false
	}
	if h.datasets == nil {
		SendError(ctx, fasthttp.StatusNotFound, "evals datasets are not available")
		return nil, false
	}
	return principal, true
}

// findDataset finds an evals dataset by its exact name, nil when there is none.
func (h *TraceIngestHandler) findDataset(ctx context.Context, name string) (*datasets.DatasetView, error) {
	summaries, _, err := h.datasets.List(ctx, configstore.EvalDatasetListParams{Search: name, Limit: 200})
	if err != nil {
		return nil, err
	}
	for i := range summaries {
		if summaries[i].Name == name {
			return h.datasets.Get(ctx, summaries[i].ID)
		}
	}
	return nil, nil
}

// ensureDataset returns the dataset with this name, creating it with the SDK columns when there
// is none. Two SDK processes may register the same name at once: the loser of that race reads the
// winner's dataset.
func (h *TraceIngestHandler) ensureDataset(ctx context.Context, name, description string) (*datasets.DatasetView, error) {
	if existing, err := h.findDataset(ctx, name); err != nil || existing != nil {
		return existing, err
	}
	created, err := h.datasets.Create(ctx, datasets.DatasetInput{Name: name, Description: description, CreatedBy: sdkDatasetActor, Columns: []datasets.ColumnSpec{
		{Name: sdkDatasetInputColumn, Type: datasets.ColumnInput}, {Name: sdkDatasetExpectedColumn, Type: datasets.ColumnExpectedOutput},
		{Name: sdkDatasetMetadataColumn, Type: datasets.ColumnVariable}, {Name: sdkDatasetRowIDColumn, Type: datasets.ColumnVariable},
	}})
	if err != nil {
		if existing, findErr := h.findDataset(ctx, name); findErr == nil && existing != nil {
			return existing, nil
		}
		return nil, err
	}
	return created, nil
}

// datasetCell stores an SDK value in a cell: text as it is, anything else as JSON.
func datasetCell(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	}
	data, err := schemas.MarshalSorted(value)
	if err != nil {
		return ""
	}
	return string(data)
}

// failDataset writes a dataset service error: a validation message as a 400, the rest as a retry.
func (h *TraceIngestHandler) failDataset(ctx *fasthttp.RequestCtx, err error) {
	var invalid *evals.InvalidError
	switch {
	case errors.As(err, &invalid):
		SendError(ctx, fasthttp.StatusBadRequest, invalid.Error())
	case errors.Is(err, configstore.ErrNotFound):
		SendError(ctx, fasthttp.StatusNotFound, "dataset not found")
	default:
		h.fail(ctx, err)
	}
}

// braintrustRegisterDataset serves POST /api/dataset/register. The dataset is an evals dataset,
// found or created by name; its id is what rows carry as dataset_id.
func (h *TraceIngestHandler) braintrustRegisterDataset(ctx *fasthttp.RequestCtx) {
	body, ok := braintrustBody(ctx)
	if !ok {
		return
	}
	principal, ok := h.datasetUser(ctx)
	if !ok {
		return
	}
	name := stringField(body, "dataset_name", "name")
	if name == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "dataset_name is required")
		return
	}
	storeCtx, cancel := ingestContext()
	defer cancel()
	project, err := h.service.ResolveProject(storeCtx, principal, stringField(body, "project_id", "project_name"))
	if err != nil {
		h.fail(ctx, err)
		return
	}
	dataset, err := h.ensureDataset(storeCtx, name, stringField(body, "description"))
	if err != nil {
		h.failDataset(ctx, err)
		return
	}
	SendJSON(ctx, map[string]any{
		"project": braintrustProjectJSON(project),
		"dataset": map[string]any{"id": dataset.ID, "name": dataset.Name, "project_id": project.ID, "description": dataset.Description, "created": dataset.CreatedAt.UTC().Format("2006-01-02T15:04:05.000Z")},
	})
}

// addBraintrustDatasetRows adds the dataset records of a logs3 flush to their evals datasets. A
// record is a row with a dataset_id and no experiment. It returns how many rows it took.
func (h *TraceIngestHandler) addBraintrustDatasetRows(ctx context.Context, principal *traceingest.Principal, rows []any) (int, error) {
	if h.datasets == nil || (!principal.CanUseDatasets && !principal.AllProjects) {
		return 0, nil
	}
	byDataset := make(map[string][]map[string]string)
	for _, raw := range rows {
		row, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		datasetID, _ := row["dataset_id"].(string)
		if experiment, _ := row["experiment_id"].(string); datasetID == "" || experiment != "" {
			continue
		}
		if deleted, _ := row["_object_delete"].(bool); deleted {
			continue
		}
		cells := map[string]string{
			sdkDatasetInputColumn: datasetCell(row["input"]), sdkDatasetExpectedColumn: datasetCell(row["expected"]),
			sdkDatasetMetadataColumn: datasetCell(row["metadata"]), sdkDatasetRowIDColumn: datasetCell(row["id"]),
		}
		if cells[sdkDatasetExpectedColumn] == "" {
			cells[sdkDatasetExpectedColumn] = datasetCell(row["output"]) // older SDKs send the expected value as output
		}
		byDataset[datasetID] = append(byDataset[datasetID], cells)
	}
	added := 0
	for datasetID, cells := range byDataset {
		if _, err := h.datasets.AddRows(ctx, datasetID, cells, "", sdkDatasetActor); err != nil {
			if errors.Is(err, configstore.ErrNotFound) {
				continue // rows for a dataset Bifrost does not hold are dropped, not retried forever
			}
			return added, err
		}
		added += len(cells)
	}
	return added, nil
}

func langfuseDatasetJSON(dataset *datasets.DatasetView, projectID string) map[string]any {
	return map[string]any{
		"id": dataset.ID, "name": dataset.Name, "description": dataset.Description, "metadata": map[string]any{}, "projectId": projectID,
		"createdAt": langfuseTimestamp(dataset.CreatedAt), "updatedAt": langfuseTimestamp(dataset.UpdatedAt),
	}
}

// langfuseDatasetCall authenticates a Langfuse dataset call and resolves the key's project, which
// Langfuse's dataset objects name.
func (h *TraceIngestHandler) langfuseDatasetCall(ctx *fasthttp.RequestCtx) (projectID string, ok bool) {
	principal, ok := h.datasetUser(ctx)
	if !ok {
		return "", false
	}
	storeCtx, cancel := ingestContext()
	defer cancel()
	project, err := h.service.ResolveProject(storeCtx, principal, "")
	if err != nil {
		h.fail(ctx, err)
		return "", false
	}
	return project.ID, true
}

func (h *TraceIngestHandler) langfuseCreateDataset(ctx *fasthttp.RequestCtx) {
	projectID, ok := h.langfuseDatasetCall(ctx)
	if !ok {
		return
	}
	var body struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := sonic.Unmarshal(ctx.PostBody(), &body); err != nil || strings.TrimSpace(body.Name) == "" {
		langfuseError(ctx, fasthttp.StatusBadRequest, "name is required")
		return
	}
	storeCtx, cancel := ingestContext()
	defer cancel()
	dataset, err := h.ensureDataset(storeCtx, strings.TrimSpace(body.Name), body.Description)
	if err != nil {
		h.failDataset(ctx, err)
		return
	}
	SendJSON(ctx, langfuseDatasetJSON(dataset, projectID))
}

func (h *TraceIngestHandler) langfuseGetDataset(ctx *fasthttp.RequestCtx) {
	projectID, ok := h.langfuseDatasetCall(ctx)
	if !ok {
		return
	}
	name, _ := ctx.UserValue("name").(string)
	if unescaped, err := pathUnescape(name); err == nil {
		name = unescaped
	}
	storeCtx, cancel := ingestContext()
	defer cancel()
	dataset, err := h.findDataset(storeCtx, name)
	if err != nil {
		h.failDataset(ctx, err)
		return
	}
	if dataset == nil {
		langfuseError(ctx, fasthttp.StatusNotFound, "dataset not found")
		return
	}
	SendJSON(ctx, langfuseDatasetJSON(dataset, projectID))
}

// langfusePage reads Langfuse's page and limit query parameters. Pages start at 1.
func langfusePage(ctx *fasthttp.RequestCtx) (page, limit int) {
	page, limit = 1, 50
	if parsed, err := strconv.Atoi(string(ctx.QueryArgs().Peek("page"))); err == nil && parsed > 0 {
		page = parsed
	}
	if parsed, err := strconv.Atoi(string(ctx.QueryArgs().Peek("limit"))); err == nil && parsed > 0 && parsed <= 500 {
		limit = parsed
	}
	return page, limit
}

func langfusePageMeta(page, limit int, total int64) map[string]any {
	pages := int((total + int64(limit) - 1) / int64(limit))
	return map[string]any{"page": page, "limit": limit, "totalItems": total, "totalPages": pages}
}

func (h *TraceIngestHandler) langfuseListDatasets(ctx *fasthttp.RequestCtx) {
	projectID, ok := h.langfuseDatasetCall(ctx)
	if !ok {
		return
	}
	page, limit := langfusePage(ctx)
	storeCtx, cancel := ingestContext()
	defer cancel()
	summaries, total, err := h.datasets.List(storeCtx, configstore.EvalDatasetListParams{Limit: limit, Offset: (page - 1) * limit})
	if err != nil {
		h.failDataset(ctx, err)
		return
	}
	data := make([]map[string]any, 0, len(summaries))
	for i := range summaries {
		data = append(data, langfuseDatasetJSON(&datasets.DatasetView{TableEvalDataset: summaries[i].TableEvalDataset}, projectID))
	}
	SendJSON(ctx, map[string]any{"data": data, "meta": langfusePageMeta(page, limit, total)})
}

// langfuseCreateDatasetItem serves POST /api/public/dataset-items: one row of a dataset.
func (h *TraceIngestHandler) langfuseCreateDatasetItem(ctx *fasthttp.RequestCtx) {
	if _, ok := h.langfuseDatasetCall(ctx); !ok {
		return
	}
	var body map[string]any
	if err := sonic.Unmarshal(ctx.PostBody(), &body); err != nil {
		langfuseError(ctx, fasthttp.StatusBadRequest, "invalid request body")
		return
	}
	name := stringField(body, "datasetName")
	if name == "" {
		langfuseError(ctx, fasthttp.StatusBadRequest, "datasetName is required")
		return
	}
	storeCtx, cancel := ingestContext()
	defer cancel()
	dataset, err := h.findDataset(storeCtx, name)
	if err != nil {
		h.failDataset(ctx, err)
		return
	}
	if dataset == nil {
		langfuseError(ctx, fasthttp.StatusNotFound, "dataset not found")
		return
	}
	cells := map[string]string{
		sdkDatasetInputColumn: datasetCell(body["input"]), sdkDatasetExpectedColumn: datasetCell(body["expectedOutput"]),
		sdkDatasetMetadataColumn: datasetCell(body["metadata"]), sdkDatasetRowIDColumn: stringField(body, "id"),
	}
	result, err := h.datasets.AddRows(storeCtx, dataset.ID, []map[string]string{cells}, "", sdkDatasetActor)
	if err != nil {
		h.failDataset(ctx, err)
		return
	}
	id := ""
	if len(result.Rows) > 0 {
		id = result.Rows[0].RowID
	}
	SendJSON(ctx, langfuseDatasetItemJSON(dataset, id, cells))
}

// langfuseDatasetItemJSON renders one dataset row as a Langfuse DatasetItem. cells are keyed by
// column name.
func langfuseDatasetItemJSON(dataset *datasets.DatasetView, rowID string, cells map[string]string) map[string]any {
	return map[string]any{
		"id": rowID, "status": "ACTIVE", "input": langfuseContent(cells[sdkDatasetInputColumn]), "expectedOutput": langfuseContent(cells[sdkDatasetExpectedColumn]),
		"metadata": langfuseContent(cells[sdkDatasetMetadataColumn]), "sourceTraceId": nil, "sourceObservationId": nil,
		"datasetId": dataset.ID, "datasetName": dataset.Name,
		"createdAt": langfuseTimestamp(dataset.CreatedAt), "updatedAt": langfuseTimestamp(dataset.UpdatedAt),
	}
}

// langfuseListDatasetItems serves GET /api/public/dataset-items?datasetName=..., which
// get_dataset() pages through to load a dataset's items.
func (h *TraceIngestHandler) langfuseListDatasetItems(ctx *fasthttp.RequestCtx) {
	if _, ok := h.langfuseDatasetCall(ctx); !ok {
		return
	}
	name := string(ctx.QueryArgs().Peek("datasetName"))
	page, limit := langfusePage(ctx)
	storeCtx, cancel := ingestContext()
	defer cancel()
	dataset, err := h.findDataset(storeCtx, name)
	if err != nil {
		h.failDataset(ctx, err)
		return
	}
	if dataset == nil {
		langfuseError(ctx, fasthttp.StatusNotFound, "dataset not found")
		return
	}
	rows, err := h.datasets.Rows(storeCtx, dataset.ID, datasets.RowQuery{Offset: (page - 1) * limit, Limit: limit})
	if err != nil {
		h.failDataset(ctx, err)
		return
	}
	// Cells are keyed by column id; the item is built from column names.
	names := make(map[string]string, len(dataset.Columns))
	for _, column := range dataset.Columns {
		names[column.ID] = column.Name
	}
	data := make([]map[string]any, 0, len(rows.Rows))
	for i := range rows.Rows {
		row := &rows.Rows[i]
		var byID map[string]string
		_ = sonic.Unmarshal([]byte(row.CellsJSON), &byID)
		for id, value := range row.Cells {
			if byID == nil {
				byID = map[string]string{}
			}
			byID[id] = value
		}
		cells := make(map[string]string, len(byID))
		for id, value := range byID {
			cells[names[id]] = value
		}
		data = append(data, langfuseDatasetItemJSON(dataset, row.RowID, cells))
	}
	SendJSON(ctx, map[string]any{"data": data, "meta": langfusePageMeta(page, limit, rows.Total)})
}
