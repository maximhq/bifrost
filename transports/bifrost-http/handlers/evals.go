package handlers

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/evals"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// Bounds on the page size of an evaluator listing.
const (
	evalsDefaultPageSize = 50
	evalsMaxPageSize     = 200
)

// EvalsHandler serves the evaluator API: definitions, versions, the built-in catalog
// and execution.
type EvalsHandler struct {
	service *evals.Service
}

// NewEvalsHandler creates an EvalsHandler. It returns nil without a service, which is
// the case when no config store is configured.
func NewEvalsHandler(service *evals.Service) *EvalsHandler {
	if service == nil {
		return nil
	}
	return &EvalsHandler{service: service}
}

// RegisterRoutes registers the routes for the EvalsHandler
func (h *EvalsHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	// Evaluators
	r.GET("/api/evals/evaluators", lib.ChainMiddlewares(h.listEvaluators, middlewares...))
	r.POST("/api/evals/evaluators", lib.ChainMiddlewares(h.createEvaluator, middlewares...))
	r.POST("/api/evals/evaluators/validate", lib.ChainMiddlewares(h.validateEvaluator, middlewares...))
	r.POST("/api/evals/evaluators/execute", lib.ChainMiddlewares(h.executeDraft, middlewares...))
	r.GET("/api/evals/evaluators/{id}", lib.ChainMiddlewares(h.getEvaluator, middlewares...))
	r.PUT("/api/evals/evaluators/{id}", lib.ChainMiddlewares(h.updateEvaluator, middlewares...))
	r.DELETE("/api/evals/evaluators/{id}", lib.ChainMiddlewares(h.deleteEvaluator, middlewares...))
	r.POST("/api/evals/evaluators/{id}/duplicate", lib.ChainMiddlewares(h.duplicateEvaluator, middlewares...))
	r.POST("/api/evals/evaluators/{id}/execute", lib.ChainMiddlewares(h.executeEvaluator, middlewares...))

	// Versions
	r.GET("/api/evals/evaluators/{id}/versions", lib.ChainMiddlewares(h.listVersions, middlewares...))
	r.GET("/api/evals/evaluators/{id}/versions/{version}", lib.ChainMiddlewares(h.getVersion, middlewares...))

	// Catalog
	r.GET("/api/evals/catalog", lib.ChainMiddlewares(h.listCatalog, middlewares...))
	r.POST("/api/evals/catalog/{key}/install", lib.ChainMiddlewares(h.installBuiltin, middlewares...))

	// Executions
	r.GET("/api/evals/executions/{id}", lib.ChainMiddlewares(h.getExecution, middlewares...))

	// Provider keys enabled for evaluations
	r.GET("/api/evals/keys", lib.ChainMiddlewares(h.listEvalKeys, middlewares...))
}

// sendEvalsError maps a service error to an HTTP status.
func sendEvalsError(ctx *fasthttp.RequestCtx, err error) {
	switch {
	case evals.IsInvalid(err):
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
	case errors.Is(err, configstore.ErrNotFound):
		SendError(ctx, fasthttp.StatusNotFound, "not found")
	case errors.Is(err, configstore.ErrEvaluatorNameExists):
		SendError(ctx, fasthttp.StatusConflict, err.Error())
	case errors.Is(err, evals.ErrNoEvalKey):
		// The request is well formed; the gateway is not set up to serve it yet.
		SendError(ctx, fasthttp.StatusUnprocessableEntity, err.Error())
	default:
		logger.Error("evals request failed: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
	}
}

// pathValue reads a path parameter.
func pathValue(ctx *fasthttp.RequestCtx, name string) string {
	value, _ := ctx.UserValue(name).(string)
	return value
}

// evalsCaller is the signed-in user behind a request, or "" when there is none.
func evalsCaller(ctx *fasthttp.RequestCtx) string {
	userID, _ := ctx.UserValue(schemas.BifrostContextKeyUserID).(string)
	return userID
}

// listEvaluators handles GET /api/evals/evaluators
func (h *EvalsHandler) listEvaluators(ctx *fasthttp.RequestCtx) {
	args := ctx.QueryArgs()
	limit := evalsDefaultPageSize
	if raw := string(args.Peek("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 {
			SendError(ctx, fasthttp.StatusBadRequest, "limit must be a positive number")
			return
		}
		limit = min(parsed, evalsMaxPageSize)
	}
	offset := 0
	if raw := string(args.Peek("offset")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			SendError(ctx, fasthttp.StatusBadRequest, "offset must not be negative")
			return
		}
		offset = parsed
	}
	// type takes one type or several separated by commas.
	var types []string
	for evaluatorType := range strings.SplitSeq(string(args.Peek("type")), ",") {
		if evaluatorType = strings.TrimSpace(evaluatorType); evaluatorType != "" {
			types = append(types, evaluatorType)
		}
	}
	source := string(args.Peek("source"))
	if source != "" && source != configstore.EvaluatorSourceCustom && source != configstore.EvaluatorSourceBuiltin {
		SendError(ctx, fasthttp.StatusBadRequest, "source must be custom or builtin")
		return
	}
	status := string(args.Peek("status"))
	if status != "" && status != configstore.EvaluatorKeyStatusReady && status != configstore.EvaluatorKeyStatusMissing {
		SendError(ctx, fasthttp.StatusBadRequest, "status must be ready or no_key")
		return
	}
	evaluators, total, err := h.service.List(ctx, configstore.EvaluatorListParams{
		Types:     types,
		Source:    source,
		KeyStatus: status,
		Search:    string(args.Peek("search")),
		Tag:       string(args.Peek("tag")),
		Limit:     limit,
		Offset:    offset,
	})
	if err != nil {
		sendEvalsError(ctx, err)
		return
	}
	SendJSON(ctx, map[string]any{"evaluators": evaluators, "total": total, "limit": limit, "offset": offset})
}

// listEvalKeys handles GET /api/evals/keys
func (h *EvalsHandler) listEvalKeys(ctx *fasthttp.RequestCtx) {
	keys, err := h.service.Keys(ctx)
	if err != nil {
		sendEvalsError(ctx, err)
		return
	}
	SendJSON(ctx, map[string]any{"keys": keys})
}

// createEvaluator handles POST /api/evals/evaluators
func (h *EvalsHandler) createEvaluator(ctx *fasthttp.RequestCtx) {
	var req evals.EvaluatorInput
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "invalid request body")
		return
	}
	req.CreatedBy = evalsCaller(ctx)
	evaluator, err := h.service.Create(ctx, req)
	if err != nil {
		sendEvalsError(ctx, err)
		return
	}
	lib.RecordAuditChange(ctx, "evaluator.created", "evaluator", evaluator.ID, evaluator.Name, nil, evaluator)
	SendJSON(ctx, map[string]any{"evaluator": evaluator})
}

// getEvaluator handles GET /api/evals/evaluators/{id}
func (h *EvalsHandler) getEvaluator(ctx *fasthttp.RequestCtx) {
	evaluator, err := h.service.Get(ctx, pathValue(ctx, "id"))
	if err != nil {
		sendEvalsError(ctx, err)
		return
	}
	SendJSON(ctx, map[string]any{"evaluator": evaluator})
}

// updateEvaluator handles PUT /api/evals/evaluators/{id}
func (h *EvalsHandler) updateEvaluator(ctx *fasthttp.RequestCtx) {
	var req evals.EvaluatorInput
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "invalid request body")
		return
	}
	req.CreatedBy = evalsCaller(ctx)
	id := pathValue(ctx, "id")
	before, err := h.service.Get(ctx, id)
	if err != nil {
		sendEvalsError(ctx, err)
		return
	}
	evaluator, err := h.service.Update(ctx, id, req)
	if err != nil {
		sendEvalsError(ctx, err)
		return
	}
	lib.RecordAuditChange(ctx, "evaluator.updated", "evaluator", evaluator.ID, evaluator.Name, before, evaluator)
	SendJSON(ctx, map[string]any{"evaluator": evaluator})
}

// deleteEvaluator handles DELETE /api/evals/evaluators/{id}
func (h *EvalsHandler) deleteEvaluator(ctx *fasthttp.RequestCtx) {
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
	lib.RecordAuditChange(ctx, "evaluator.deleted", "evaluator", before.ID, before.Name, before, nil)
	SendJSON(ctx, map[string]any{"message": "evaluator deleted"})
}

// duplicateEvaluator handles POST /api/evals/evaluators/{id}/duplicate
func (h *EvalsHandler) duplicateEvaluator(ctx *fasthttp.RequestCtx) {
	var req struct {
		Name string `json:"name"`
	}
	if body := ctx.PostBody(); len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			SendError(ctx, fasthttp.StatusBadRequest, "invalid request body")
			return
		}
	}
	evaluator, err := h.service.Duplicate(ctx, pathValue(ctx, "id"), req.Name, evalsCaller(ctx))
	if err != nil {
		sendEvalsError(ctx, err)
		return
	}
	lib.RecordAuditChange(ctx, "evaluator.created", "evaluator", evaluator.ID, evaluator.Name, nil, evaluator)
	SendJSON(ctx, map[string]any{"evaluator": evaluator})
}

// listVersions handles GET /api/evals/evaluators/{id}/versions
func (h *EvalsHandler) listVersions(ctx *fasthttp.RequestCtx) {
	versions, err := h.service.Versions(ctx, pathValue(ctx, "id"))
	if err != nil {
		sendEvalsError(ctx, err)
		return
	}
	SendJSON(ctx, map[string]any{"versions": versions})
}

// getVersion handles GET /api/evals/evaluators/{id}/versions/{version}
func (h *EvalsHandler) getVersion(ctx *fasthttp.RequestCtx) {
	number, err := strconv.Atoi(pathValue(ctx, "version"))
	if err != nil || number < 1 {
		SendError(ctx, fasthttp.StatusBadRequest, "version must be a positive number")
		return
	}
	evaluator, err := h.service.Version(ctx, pathValue(ctx, "id"), number)
	if err != nil {
		sendEvalsError(ctx, err)
		return
	}
	SendJSON(ctx, map[string]any{"evaluator": evaluator})
}

// validateEvaluator handles POST /api/evals/evaluators/validate
func (h *EvalsHandler) validateEvaluator(ctx *fasthttp.RequestCtx) {
	var req struct {
		Type       evals.EvaluatorType `json:"type"`
		BuiltinKey string              `json:"builtin_key"`
		Config     json.RawMessage     `json:"config"`
	}
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "invalid request body")
		return
	}
	// An invalid config is the answer here, not a failed request.
	SendJSON(ctx, h.service.Validate(req.Type, req.BuiltinKey, req.Config))
}

// listCatalog handles GET /api/evals/catalog
func (h *EvalsHandler) listCatalog(ctx *fasthttp.RequestCtx) {
	SendJSON(ctx, map[string]any{"catalog": evals.Catalog()})
}

// installBuiltin handles POST /api/evals/catalog/{key}/install
func (h *EvalsHandler) installBuiltin(ctx *fasthttp.RequestCtx) {
	var req evals.InstallInput
	if body := ctx.PostBody(); len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			SendError(ctx, fasthttp.StatusBadRequest, "invalid request body")
			return
		}
	}
	req.CreatedBy = evalsCaller(ctx)
	evaluator, err := h.service.Install(ctx, pathValue(ctx, "key"), req)
	if err != nil {
		sendEvalsError(ctx, err)
		return
	}
	lib.RecordAuditChange(ctx, "evaluator.created", "evaluator", evaluator.ID, evaluator.Name, nil, evaluator)
	SendJSON(ctx, map[string]any{"evaluator": evaluator})
}

// executeEvaluator handles POST /api/evals/evaluators/{id}/execute
func (h *EvalsHandler) executeEvaluator(ctx *fasthttp.RequestCtx) {
	var req evals.ExecuteInput
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "invalid request body")
		return
	}
	// The path names the evaluator; a draft in the body would run something else.
	req.Draft = nil
	req.EvaluatorID = pathValue(ctx, "id")
	h.execute(ctx, req)
}

// executeDraft handles POST /api/evals/evaluators/execute
func (h *EvalsHandler) executeDraft(ctx *fasthttp.RequestCtx) {
	var req evals.ExecuteInput
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "invalid request body")
		return
	}
	if req.Draft == nil {
		SendError(ctx, fasthttp.StatusBadRequest, "draft is required")
		return
	}
	h.execute(ctx, req)
}

// execute queues a run and answers 200 with the result, or 202 when the run is still
// going at the end of the wait. The client polls the execution for the rest.
func (h *EvalsHandler) execute(ctx *fasthttp.RequestCtx, req evals.ExecuteInput) {
	req.CreatedBy = evalsCaller(ctx)
	execution, err := h.service.Execute(ctx, req)
	if err != nil {
		sendEvalsError(ctx, err)
		return
	}
	status := fasthttp.StatusOK
	if execution.Result == nil {
		status = fasthttp.StatusAccepted
	}
	SendJSONWithStatus(ctx, map[string]any{"execution": execution}, status)
}

// getExecution handles GET /api/evals/executions/{id}
func (h *EvalsHandler) getExecution(ctx *fasthttp.RequestCtx) {
	execution, err := h.service.Execution(ctx, pathValue(ctx, "id"))
	if err != nil {
		sendEvalsError(ctx, err)
		return
	}
	SendJSON(ctx, map[string]any{"execution": execution})
}
