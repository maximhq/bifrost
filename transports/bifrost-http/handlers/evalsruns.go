// Package handlers provides HTTP request handlers for the Bifrost HTTP transport.
// This file contains the test runs API: runs, their entries, and comparison.
package handlers

import (
	"fmt"
	"strings"

	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/evals/runs"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// EvalsRunsHandler serves the test runs API.
type EvalsRunsHandler struct {
	service *runs.Service
}

// NewEvalsRunsHandler creates an EvalsRunsHandler. It returns nil without a service,
// which is the case when no config store is configured.
func NewEvalsRunsHandler(service *runs.Service) *EvalsRunsHandler {
	if service == nil {
		return nil
	}
	return &EvalsRunsHandler{service: service}
}

// RegisterRoutes registers the routes for the EvalsRunsHandler
func (h *EvalsRunsHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	route := func(method, path string, handler fasthttp.RequestHandler) {
		r.Handle(method, "/api/evals/runs"+path, lib.ChainMiddlewares(handler, middlewares...))
	}
	route(fasthttp.MethodGet, "", h.listRuns)
	route(fasthttp.MethodPost, "", h.createRun)
	// These two are registered before the routes that take a run ID in the same place.
	route(fasthttp.MethodPost, "/validate", h.validateRun)
	route(fasthttp.MethodGet, "/compare", h.compareRuns)
	route(fasthttp.MethodGet, "/{id}", h.getRun)
	route(fasthttp.MethodPut, "/{id}", h.updateRun)
	route(fasthttp.MethodDelete, "/{id}", h.deleteRun)
	route(fasthttp.MethodPost, "/{id}/stop", h.stopRun)
	route(fasthttp.MethodPost, "/{id}/rerun", h.rerunRun)
	route(fasthttp.MethodPost, "/{id}/evaluators", h.addEvaluators)
	route(fasthttp.MethodGet, "/{id}/entries", h.listEntries)
	route(fasthttp.MethodGet, "/{id}/entries/{entryId}", h.getEntry)
	route(fasthttp.MethodPut, "/{id}/entries/{entryId}/results/{evaluatorId}", h.reviewResult)
	route(fasthttp.MethodGet, "/{id}/export", h.exportRun)
}

// listRuns handles GET /api/evals/runs
func (h *EvalsRunsHandler) listRuns(ctx *fasthttp.RequestCtx) {
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
	list, total, err := h.service.List(ctx, configstore.EvalRunListParams{
		Search: string(args.Peek("search")), Status: string(args.Peek("status")), DatasetID: string(args.Peek("dataset_id")),
		Tag: string(args.Peek("tag")), Limit: limit, Offset: offset,
	})
	if err != nil {
		sendEvalsError(ctx, err)
		return
	}
	SendJSON(ctx, map[string]any{"runs": list, "total": total, "limit": limit, "offset": offset})
}

// createRun handles POST /api/evals/runs
func (h *EvalsRunsHandler) createRun(ctx *fasthttp.RequestCtx) {
	var req runs.RunInput
	if !decodeBody(ctx, &req) {
		return
	}
	req.CreatedBy = evalsCaller(ctx)
	run, err := h.service.Create(ctx, req)
	if err != nil {
		sendEvalsError(ctx, err)
		return
	}
	lib.RecordAuditChange(ctx, "eval_run.created", "eval_run", run.ID, run.Name, nil, run)
	SendJSON(ctx, map[string]any{"run": run})
}

// validateRun handles POST /api/evals/runs/validate
func (h *EvalsRunsHandler) validateRun(ctx *fasthttp.RequestCtx) {
	var req runs.RunInput
	if !decodeBody(ctx, &req) {
		return
	}
	check, err := h.service.Validate(ctx, req)
	if err != nil {
		sendEvalsError(ctx, err)
		return
	}
	SendJSON(ctx, check)
}

// getRun handles GET /api/evals/runs/{id}
func (h *EvalsRunsHandler) getRun(ctx *fasthttp.RequestCtx) {
	run, err := h.service.Get(ctx, pathValue(ctx, "id"))
	if err != nil {
		sendEvalsError(ctx, err)
		return
	}
	SendJSON(ctx, map[string]any{"run": run})
}

// updateRun handles PUT /api/evals/runs/{id}
func (h *EvalsRunsHandler) updateRun(ctx *fasthttp.RequestCtx) {
	var req runs.UpdateInput
	if !decodeBody(ctx, &req) {
		return
	}
	run, err := h.service.Update(ctx, pathValue(ctx, "id"), req)
	if err != nil {
		sendEvalsError(ctx, err)
		return
	}
	SendJSON(ctx, map[string]any{"run": run})
}

// deleteRun handles DELETE /api/evals/runs/{id}
func (h *EvalsRunsHandler) deleteRun(ctx *fasthttp.RequestCtx) {
	id := pathValue(ctx, "id")
	if err := h.service.Delete(ctx, id); err != nil {
		sendEvalsError(ctx, err)
		return
	}
	lib.RecordAuditChange(ctx, "eval_run.deleted", "eval_run", id, "", nil, nil)
	SendJSON(ctx, map[string]any{"deleted": true})
}

// stopRun handles POST /api/evals/runs/{id}/stop
func (h *EvalsRunsHandler) stopRun(ctx *fasthttp.RequestCtx) {
	run, err := h.service.Stop(ctx, pathValue(ctx, "id"))
	if err != nil {
		sendEvalsError(ctx, err)
		return
	}
	SendJSON(ctx, map[string]any{"run": run})
}

// rerunRun handles POST /api/evals/runs/{id}/rerun
func (h *EvalsRunsHandler) rerunRun(ctx *fasthttp.RequestCtx) {
	var req runs.RerunInput
	if !decodeBody(ctx, &req) {
		return
	}
	run, err := h.service.Rerun(ctx, pathValue(ctx, "id"), req)
	if err != nil {
		sendEvalsError(ctx, err)
		return
	}
	SendJSON(ctx, map[string]any{"run": run})
}

// addEvaluators handles POST /api/evals/runs/{id}/evaluators
func (h *EvalsRunsHandler) addEvaluators(ctx *fasthttp.RequestCtx) {
	var req struct {
		Evaluators []runs.EvaluatorRef `json:"evaluators"`
	}
	if !decodeBody(ctx, &req) {
		return
	}
	run, err := h.service.AddEvaluators(ctx, pathValue(ctx, "id"), req.Evaluators)
	if err != nil {
		sendEvalsError(ctx, err)
		return
	}
	SendJSON(ctx, map[string]any{"run": run})
}

// listEntries handles GET /api/evals/runs/{id}/entries
func (h *EvalsRunsHandler) listEntries(ctx *fasthttp.RequestCtx) {
	limit, ok := queryInt(ctx, "limit")
	if !ok {
		return
	}
	offset, ok := queryInt(ctx, "offset")
	if !ok {
		return
	}
	args := ctx.QueryArgs()
	query := runs.EntryQuery{
		Search: string(args.Peek("search")), Offset: offset, Limit: limit,
		Baseline: string(args.Peek("baseline")), Change: string(args.Peek("change")),
	}
	if status := string(args.Peek("status")); status != "" {
		query.Statuses = strings.Split(status, ",")
	}
	switch string(args.Peek("passed")) {
	case "":
	case "true":
		query.Passed = new(true)
	case "false":
		query.Passed = new(false)
	default:
		SendError(ctx, fasthttp.StatusBadRequest, "passed must be true or false")
		return
	}
	page, err := h.service.Entries(ctx, pathValue(ctx, "id"), query)
	if err != nil {
		sendEvalsError(ctx, err)
		return
	}
	SendJSON(ctx, page)
}

// getEntry handles GET /api/evals/runs/{id}/entries/{entryId}
func (h *EvalsRunsHandler) getEntry(ctx *fasthttp.RequestCtx) {
	entry, err := h.service.Entry(ctx, pathValue(ctx, "id"), pathValue(ctx, "entryId"))
	if err != nil {
		sendEvalsError(ctx, err)
		return
	}
	SendJSON(ctx, map[string]any{"entry": entry})
}

// reviewResult handles PUT /api/evals/runs/{id}/entries/{entryId}/results/{evaluatorId}
func (h *EvalsRunsHandler) reviewResult(ctx *fasthttp.RequestCtx) {
	var req runs.ReviewInput
	if !decodeBody(ctx, &req) {
		return
	}
	result, err := h.service.Review(ctx, pathValue(ctx, "id"), pathValue(ctx, "entryId"), pathValue(ctx, "evaluatorId"), req, evalsCaller(ctx))
	if err != nil {
		sendEvalsError(ctx, err)
		return
	}
	SendJSON(ctx, map[string]any{"result": result})
}

// exportRun handles GET /api/evals/runs/{id}/export
func (h *EvalsRunsHandler) exportRun(ctx *fasthttp.RequestCtx) {
	id := pathValue(ctx, "id")
	run, err := h.service.Get(ctx, id)
	if err != nil {
		sendEvalsError(ctx, err)
		return
	}
	ctx.SetContentType("text/csv; charset=utf-8")
	ctx.Response.Header.Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", exportFileName(run.Name, 0, "csv")))
	if err := h.service.Export(ctx, id, ctx.Response.BodyWriter()); err != nil {
		ctx.Response.ResetBody()
		ctx.Response.Header.Del("Content-Disposition")
		sendEvalsError(ctx, err)
	}
}

// compareRuns handles GET /api/evals/runs/compare
func (h *EvalsRunsHandler) compareRuns(ctx *fasthttp.RequestCtx) {
	limit, ok := queryInt(ctx, "limit")
	if !ok {
		return
	}
	offset, ok := queryInt(ctx, "offset")
	if !ok {
		return
	}
	var ids []string
	for _, id := range strings.Split(string(ctx.QueryArgs().Peek("ids")), ",") {
		if id = strings.TrimSpace(id); id != "" {
			ids = append(ids, id)
		}
	}
	comparison, err := h.service.Compare(ctx, ids, offset, limit)
	if err != nil {
		sendEvalsError(ctx, err)
		return
	}
	SendJSON(ctx, comparison)
}
