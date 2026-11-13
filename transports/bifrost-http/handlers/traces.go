package handlers

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// tracesMaxLimit caps one page of the trace and span lists.
const tracesMaxLimit = 500

// TracesHandler is the dashboard's API for Tracing: the trace list and detail, the overview, and
// the management of tracing projects and tracing keys. It reads what the ingest endpoints wrote.
type TracesHandler struct {
	store  logstore.TraceStore
	config configstore.ConfigStore
	ingest *TraceIngestHandler
	logger schemas.Logger
}

// NewTracesHandler builds the handler. ingest may be nil in tests that only read.
func NewTracesHandler(store logstore.TraceStore, config configstore.ConfigStore, ingest *TraceIngestHandler, logger schemas.Logger) *TracesHandler {
	return &TracesHandler{store: store, config: config, ingest: ingest, logger: logger}
}

// RegisterRoutes mounts the Tracing dashboard API.
func (h *TracesHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	r.GET("/api/traces", lib.ChainMiddlewares(h.searchTraces, middlewares...))
	r.GET("/api/traces/spans", lib.ChainMiddlewares(h.searchSpans, middlewares...))
	r.GET("/api/traces/stats", lib.ChainMiddlewares(h.stats, middlewares...))
	r.GET("/api/traces/histogram", lib.ChainMiddlewares(h.histogram, middlewares...))
	r.GET("/api/traces/sessions", lib.ChainMiddlewares(h.sessions, middlewares...))
	r.GET("/api/traces/filterdata", lib.ChainMiddlewares(h.filterData, middlewares...))
	r.GET("/api/traces/connect", lib.ChainMiddlewares(h.connect, middlewares...))
	r.GET("/api/traces/projects", lib.ChainMiddlewares(h.listProjects, middlewares...))
	r.POST("/api/traces/projects", lib.ChainMiddlewares(h.createProject, middlewares...))
	r.PUT("/api/traces/projects/{id}", lib.ChainMiddlewares(h.updateProject, middlewares...))
	r.DELETE("/api/traces/projects/{id}", lib.ChainMiddlewares(h.deleteProject, middlewares...))
	r.GET("/api/traces/keys", lib.ChainMiddlewares(h.listKeys, middlewares...))
	r.POST("/api/traces/keys", lib.ChainMiddlewares(h.createKey, middlewares...))
	r.PUT("/api/traces/keys/{id}", lib.ChainMiddlewares(h.updateKey, middlewares...))
	r.DELETE("/api/traces/keys/{id}", lib.ChainMiddlewares(h.deleteKey, middlewares...))
	r.GET("/api/traces/trace/{project_id}/{trace_id}", lib.ChainMiddlewares(h.getTrace, middlewares...))
	r.GET("/api/traces/uploads/{id}", lib.ChainMiddlewares(h.getUpload, middlewares...))
	for _, register := range tracesHandlerExtensions {
		register(h, r, middlewares...)
	}
}

// tracesHandlerExtensions are routes added by other files of this handler (review, deletion).
var tracesHandlerExtensions []func(h *TracesHandler, r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware)

// storeContext bounds one dashboard query.
func (h *TracesHandler) storeContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

func (h *TracesHandler) fail(ctx *fasthttp.RequestCtx, action string, err error) {
	switch {
	case errors.Is(err, logstore.ErrNotFound), errors.Is(err, configstore.ErrNotFound):
		SendError(ctx, fasthttp.StatusNotFound, "not found")
	case errors.Is(err, configstore.ErrTraceProjectExists):
		SendError(ctx, fasthttp.StatusConflict, "a tracing project with this name already exists")
	default:
		if h.logger != nil {
			h.logger.Error("failed to %s: %v", action, err)
		}
		SendError(ctx, fasthttp.StatusInternalServerError, "failed to "+action)
	}
}

// parseTraceFilters reads the filter query parameters shared by every list and overview route. A
// value that does not parse is refused: a silently dropped time bound would widen the query.
func parseTraceFilters(ctx *fasthttp.RequestCtx) (logstore.TraceFilters, error) {
	args := ctx.QueryArgs()
	value := func(name string) string { return strings.TrimSpace(string(args.Peek(name))) }
	values := func(name string) []string { return parseCommaSeparated(value(name)) }
	filters := logstore.TraceFilters{
		ProjectIDs: values("project_ids"), Sources: values("sources"), Kinds: values("kinds"), Models: values("models"),
		Environments: values("environments"), Tags: values("tags"), SessionID: value("session_id"), UserID: value("user_id"),
		ExperimentID: value("experiment_id"), TraceID: value("trace_id"), Status: value("status"), Search: value("search"),
	}
	for name, dst := range map[string]**time.Time{"start_time": &filters.StartTime, "end_time": &filters.EndTime} {
		if raw := value(name); raw != "" {
			parsed, err := time.Parse(time.RFC3339, raw)
			if err != nil {
				return filters, fmt.Errorf("invalid %s: must be an RFC 3339 time", name)
			}
			*dst = &parsed
		}
	}
	for name, dst := range map[string]**float64{"min_cost": &filters.MinCost, "min_duration_ms": &filters.MinDuration} {
		if raw := value(name); raw != "" {
			parsed, err := strconv.ParseFloat(raw, 64)
			if err != nil {
				return filters, fmt.Errorf("invalid %s: must be a number", name)
			}
			*dst = &parsed
		}
	}
	if raw := value("min_tokens"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			return filters, fmt.Errorf("invalid min_tokens: must be a number")
		}
		filters.MinTokens = &parsed
	}
	return filters, nil
}

func parseTracePagination(ctx *fasthttp.RequestCtx) (logstore.PaginationOptions, error) {
	args := ctx.QueryArgs()
	pagination := logstore.PaginationOptions{Limit: 50, SortBy: string(args.Peek("sort_by")), Order: string(args.Peek("order"))}
	if raw := string(args.Peek("limit")); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit <= 0 || limit > tracesMaxLimit {
			return pagination, fmt.Errorf("invalid limit: must be between 1 and %d", tracesMaxLimit)
		}
		pagination.Limit = limit
	}
	if raw := string(args.Peek("offset")); raw != "" {
		offset, err := strconv.Atoi(raw)
		if err != nil || offset < 0 {
			return pagination, fmt.Errorf("invalid offset: must be a non-negative number")
		}
		pagination.Offset = offset
	}
	return pagination, nil
}

// listQuery parses the filters and the page of a list route, writing the 400 itself.
func listQuery(ctx *fasthttp.RequestCtx) (logstore.TraceFilters, logstore.PaginationOptions, bool) {
	filters, err := parseTraceFilters(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return filters, logstore.PaginationOptions{}, false
	}
	pagination, err := parseTracePagination(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return filters, pagination, false
	}
	return filters, pagination, true
}

func (h *TracesHandler) searchTraces(ctx *fasthttp.RequestCtx) {
	filters, pagination, ok := listQuery(ctx)
	if !ok {
		return
	}
	storeCtx, cancel := h.storeContext()
	defer cancel()
	result, err := h.store.SearchTraces(storeCtx, filters, pagination)
	if err != nil {
		h.fail(ctx, "search traces", err)
		return
	}
	SendJSON(ctx, result)
}

func (h *TracesHandler) searchSpans(ctx *fasthttp.RequestCtx) {
	filters, pagination, ok := listQuery(ctx)
	if !ok {
		return
	}
	storeCtx, cancel := h.storeContext()
	defer cancel()
	result, err := h.store.SearchTraceSpans(storeCtx, filters, pagination)
	if err != nil {
		h.fail(ctx, "search trace spans", err)
		return
	}
	SendJSON(ctx, result)
}

func (h *TracesHandler) sessions(ctx *fasthttp.RequestCtx) {
	filters, pagination, ok := listQuery(ctx)
	if !ok {
		return
	}
	storeCtx, cancel := h.storeContext()
	defer cancel()
	result, err := h.store.SearchTraceSessions(storeCtx, filters, pagination)
	if err != nil {
		h.fail(ctx, "search trace sessions", err)
		return
	}
	SendJSON(ctx, result)
}

// stats returns the overview totals with their split by project and by source.
func (h *TracesHandler) stats(ctx *fasthttp.RequestCtx) {
	filters, err := parseTraceFilters(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	storeCtx, cancel := h.storeContext()
	defer cancel()
	stats, err := h.store.GetTraceStats(storeCtx, filters)
	if err != nil {
		h.fail(ctx, "get trace stats", err)
		return
	}
	byProject, err := h.store.GetTraceBreakdown(storeCtx, filters, "project")
	if err != nil {
		h.fail(ctx, "get trace stats", err)
		return
	}
	bySource, err := h.store.GetTraceBreakdown(storeCtx, filters, "source")
	if err != nil {
		h.fail(ctx, "get trace stats", err)
		return
	}
	SendJSON(ctx, map[string]any{"stats": stats, "by_project": byProject, "by_source": bySource})
}

func (h *TracesHandler) histogram(ctx *fasthttp.RequestCtx) {
	filters, err := parseTraceFilters(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	bucket := int64(3600)
	if raw := string(ctx.QueryArgs().Peek("bucket_size_seconds")); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 1 {
			SendError(ctx, fasthttp.StatusBadRequest, "invalid bucket_size_seconds: must be a positive number")
			return
		}
		bucket = parsed
	}
	// The zero-filled range is bounded so a tiny bucket over a long range cannot build a huge list.
	if filters.StartTime != nil && filters.EndTime != nil && filters.EndTime.Sub(*filters.StartTime) > time.Duration(bucket)*time.Second*2000 {
		SendError(ctx, fasthttp.StatusBadRequest, "bucket_size_seconds is too small for this time range")
		return
	}
	storeCtx, cancel := h.storeContext()
	defer cancel()
	result, err := h.store.GetTraceHistogram(storeCtx, filters, bucket)
	if err != nil {
		h.fail(ctx, "get trace histogram", err)
		return
	}
	SendJSON(ctx, result)
}

func (h *TracesHandler) filterData(ctx *fasthttp.RequestCtx) {
	storeCtx, cancel := h.storeContext()
	defer cancel()
	result, err := h.store.GetTraceFilterData(storeCtx, parseCommaSeparated(string(ctx.QueryArgs().Peek("project_ids"))))
	if err != nil {
		h.fail(ctx, "get trace filter data", err)
		return
	}
	SendJSON(ctx, result)
}

// getTrace returns one trace whole: its rollup, every span, the gateway links, scores and comments.
func (h *TracesHandler) getTrace(ctx *fasthttp.RequestCtx) {
	projectID, _ := ctx.UserValue("project_id").(string)
	traceID, _ := ctx.UserValue("trace_id").(string)
	storeCtx, cancel := h.storeContext()
	defer cancel()
	spans, err := h.store.ListTraceSpans(storeCtx, projectID, traceID)
	if err != nil {
		h.fail(ctx, "load trace", err)
		return
	}
	trace, err := h.store.FindTrace(storeCtx, projectID, traceID)
	if errors.Is(err, logstore.ErrNotFound) && len(spans) > 0 {
		// Spans are stored before the first rollup runs: build the rollup the viewer needs now.
		trace, err = h.store.RecomputeTraceRollup(storeCtx, projectID, traceID)
	}
	if err != nil {
		h.fail(ctx, "load trace", err)
		return
	}
	links, err := h.store.ListTraceSpanLinks(storeCtx, projectID, traceID)
	if err != nil {
		h.fail(ctx, "load trace", err)
		return
	}
	scores, err := h.store.ListTraceScores(storeCtx, projectID, traceID)
	if err != nil {
		h.fail(ctx, "load trace", err)
		return
	}
	comments, err := h.store.ListTraceComments(storeCtx, projectID, traceID)
	if err != nil {
		h.fail(ctx, "load trace", err)
		return
	}
	SendJSON(ctx, map[string]any{"trace": trace, "spans": spans, "links": links, "scores": scores, "comments": comments})
}

// connect returns what the Connect panel shows: the URL each SDK should be pointed at.
func (h *TracesHandler) connect(ctx *fasthttp.RequestCtx) {
	base := ""
	if h.ingest != nil {
		base = h.ingest.publicBaseURL(ctx)
	}
	uploads := h.store.TraceUploadsAvailable()
	SendJSON(ctx, map[string]any{
		"base_url": base,
		"endpoints": map[string]string{
			logstore.TraceSourceBraintrust: base + tracingPrefix + "/braintrust",
			logstore.TraceSourceLangfuse:   base + tracingPrefix + "/langfuse",
			logstore.TraceSourceArize:      base + tracingPrefix + "/arize",
			logstore.TraceSourceOTel:       base + tracingPrefix + "/otel",
		},
		"uploads_available": uploads,
	})
}

func (h *TracesHandler) listProjects(ctx *fasthttp.RequestCtx) {
	storeCtx, cancel := h.storeContext()
	defer cancel()
	projects, err := h.config.ListTraceProjects(storeCtx)
	if err != nil {
		h.fail(ctx, "list tracing projects", err)
		return
	}
	for _, project := range projects {
		redactTraceForward(project)
	}
	SendJSON(ctx, map[string]any{"projects": projects})
}

// traceProjectPayload is the editable part of a tracing project.
type traceProjectPayload struct {
	Name          string                     `json:"name"`
	Description   string                     `json:"description"`
	RetentionDays *int                       `json:"retention_days"`
	MaskingRules  []tables.TraceMaskingRule  `json:"masking_rules"`
	Forward       *tables.TraceForwardTarget `json:"forward"`
	// Evaluators is nil when the caller did not send the field, which leaves the stored rules alone.
	Evaluators *[]tables.TraceEvaluatorRule `json:"evaluators"`
}

func (p *traceProjectPayload) validate() error {
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" || len(p.Name) > 255 {
		return fmt.Errorf("name is required and must be at most 255 characters")
	}
	if p.RetentionDays != nil && (*p.RetentionDays < 1 || *p.RetentionDays > 3650) {
		return fmt.Errorf("retention_days must be between 1 and 3650")
	}
	return validateTraceProjectSettings(p)
}

// validateTraceProjectSettings checks the masking rules and the forward target. It is a variable
// so the file that implements those features owns their rules.
var validateTraceProjectSettings = func(*traceProjectPayload) error { return nil }

func (p *traceProjectPayload) apply(project *tables.TableTraceProject) {
	project.Name, project.Description, project.RetentionDays = p.Name, p.Description, p.RetentionDays
	project.MaskingRules, project.Forward = p.MaskingRules, p.Forward
	if p.Evaluators != nil {
		project.Evaluators = *p.Evaluators
	}
}

func (h *TracesHandler) createProject(ctx *fasthttp.RequestCtx) {
	var payload traceProjectPayload
	if err := sonic.Unmarshal(ctx.PostBody(), &payload); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "invalid request body")
		return
	}
	if err := payload.validate(); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	project := &tables.TableTraceProject{}
	payload.apply(project)
	storeCtx, cancel := h.storeContext()
	defer cancel()
	if err := h.config.CreateTraceProject(storeCtx, project); err != nil {
		h.fail(ctx, "create tracing project", err)
		return
	}
	redactTraceForward(project)
	SendJSON(ctx, map[string]any{"project": project})
}

func (h *TracesHandler) updateProject(ctx *fasthttp.RequestCtx) {
	id, _ := ctx.UserValue("id").(string)
	var payload traceProjectPayload
	if err := sonic.Unmarshal(ctx.PostBody(), &payload); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "invalid request body")
		return
	}
	if err := payload.validate(); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	storeCtx, cancel := h.storeContext()
	defer cancel()
	project, err := h.config.GetTraceProject(storeCtx, id)
	if err != nil {
		h.fail(ctx, "update tracing project", err)
		return
	}
	keepStoredForwardSecrets(payload.Forward, project.Forward)
	payload.apply(project)
	if err := h.config.UpdateTraceProject(storeCtx, project); err != nil {
		h.fail(ctx, "update tracing project", err)
		return
	}
	h.invalidate()
	redactTraceForward(project)
	SendJSON(ctx, map[string]any{"project": project})
}

// deleteProject removes a project, the bindings keys hold to it, and every trace stored under it.
func (h *TracesHandler) deleteProject(ctx *fasthttp.RequestCtx) {
	id, _ := ctx.UserValue("id").(string)
	storeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if _, err := h.config.GetTraceProject(storeCtx, id); err != nil {
		h.fail(ctx, "delete tracing project", err)
		return
	}
	keys, err := h.config.ListTracingKeys(storeCtx)
	if err != nil {
		h.fail(ctx, "delete tracing project", err)
		return
	}
	for _, key := range keys {
		if !key.AllowsProject(id) && key.DefaultProjectID != id {
			continue
		}
		kept := make([]string, 0, len(key.ProjectIDs))
		for _, projectID := range key.ProjectIDs {
			if projectID != id {
				kept = append(kept, projectID)
			}
		}
		key.ProjectIDs = kept
		if key.DefaultProjectID == id {
			key.DefaultProjectID = ""
		}
		if err := h.config.UpdateTracingKey(storeCtx, key); err != nil {
			h.fail(ctx, "delete tracing project", err)
			return
		}
	}
	// The project row goes first, so no new trace can land while the old ones are removed.
	if err := h.config.DeleteTraceProject(storeCtx, id); err != nil {
		h.fail(ctx, "delete tracing project", err)
		return
	}
	h.invalidate()
	for {
		batch, err := h.store.ListProjectTraceKeys(storeCtx, id, 200)
		if err != nil {
			h.fail(ctx, "delete the project's traces", err)
			return
		}
		if len(batch) == 0 {
			break
		}
		if err := h.store.DeleteTraces(storeCtx, batch); err != nil {
			h.fail(ctx, "delete the project's traces", err)
			return
		}
	}
	SendJSON(ctx, map[string]any{"deleted": true})
}

// invalidate makes the ingest path re-read keys and projects after an edit.
func (h *TracesHandler) invalidate() {
	if h.ingest != nil {
		h.ingest.service.InvalidateKeys()
	}
}

func (h *TracesHandler) listKeys(ctx *fasthttp.RequestCtx) {
	storeCtx, cancel := h.storeContext()
	defer cancel()
	keys, err := h.config.ListTracingKeys(storeCtx)
	if err != nil {
		h.fail(ctx, "list tracing keys", err)
		return
	}
	SendJSON(ctx, map[string]any{"keys": keys})
}

// tracingKeyPayload is the editable part of a tracing key. The key's value is never accepted from
// the caller: Bifrost generates it.
type tracingKeyPayload struct {
	Name              string   `json:"name"`
	ProjectIDs        []string `json:"project_ids"`
	DefaultProjectID  string   `json:"default_project_id"`
	VirtualKeyIDs     []string `json:"virtual_key_ids"`
	LogContent        *bool    `json:"log_content"`
	CanCreateProjects bool     `json:"can_create_projects"`
	CanReadPrompts    bool     `json:"can_read_prompts"`
	Enabled           *bool    `json:"enabled"`
}

// validate checks the payload against the stored projects.
func (h *TracesHandler) validateKey(ctx context.Context, payload *tracingKeyPayload) error {
	payload.Name = strings.TrimSpace(payload.Name)
	if payload.Name == "" || len(payload.Name) > 255 {
		return fmt.Errorf("name is required and must be at most 255 characters")
	}
	if len(payload.ProjectIDs) == 0 && !payload.CanCreateProjects {
		return fmt.Errorf("a tracing key needs at least one project, or permission to create projects")
	}
	for _, id := range payload.ProjectIDs {
		if _, err := h.config.GetTraceProject(ctx, id); err != nil {
			return fmt.Errorf("project %q does not exist", id)
		}
	}
	if payload.DefaultProjectID != "" {
		found := false
		for _, id := range payload.ProjectIDs {
			found = found || id == payload.DefaultProjectID
		}
		if !found {
			return fmt.Errorf("default_project_id must be one of project_ids")
		}
	}
	return nil
}

func (p *tracingKeyPayload) apply(key *tables.TableTracingKey) {
	key.Name, key.ProjectIDs, key.DefaultProjectID, key.VirtualKeyIDs = p.Name, p.ProjectIDs, p.DefaultProjectID, p.VirtualKeyIDs
	key.CanCreateProjects, key.CanReadPrompts = p.CanCreateProjects, p.CanReadPrompts
	key.LogContent = p.LogContent == nil || *p.LogContent
	key.Enabled = p.Enabled == nil || *p.Enabled
}

// createKey makes a tracing key and returns its value. This response is the only time the value is
// shown: only its hash is stored.
func (h *TracesHandler) createKey(ctx *fasthttp.RequestCtx) {
	var payload tracingKeyPayload
	if err := sonic.Unmarshal(ctx.PostBody(), &payload); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "invalid request body")
		return
	}
	storeCtx, cancel := h.storeContext()
	defer cancel()
	if err := h.validateKey(storeCtx, &payload); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	value, err := tables.NewTracingKeyValue()
	if err != nil {
		h.fail(ctx, "create tracing key", err)
		return
	}
	key := &tables.TableTracingKey{}
	payload.apply(key)
	key.SetValue(value)
	if err := h.config.CreateTracingKey(storeCtx, key); err != nil {
		h.fail(ctx, "create tracing key", err)
		return
	}
	SendJSON(ctx, map[string]any{"key": key, "value": value})
}

func (h *TracesHandler) updateKey(ctx *fasthttp.RequestCtx) {
	id, _ := ctx.UserValue("id").(string)
	var payload tracingKeyPayload
	if err := sonic.Unmarshal(ctx.PostBody(), &payload); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "invalid request body")
		return
	}
	storeCtx, cancel := h.storeContext()
	defer cancel()
	if err := h.validateKey(storeCtx, &payload); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	key, err := h.config.GetTracingKey(storeCtx, id)
	if err != nil {
		h.fail(ctx, "update tracing key", err)
		return
	}
	payload.apply(key)
	if err := h.config.UpdateTracingKey(storeCtx, key); err != nil {
		h.fail(ctx, "update tracing key", err)
		return
	}
	h.invalidate()
	SendJSON(ctx, map[string]any{"key": key})
}

func (h *TracesHandler) deleteKey(ctx *fasthttp.RequestCtx) {
	id, _ := ctx.UserValue("id").(string)
	storeCtx, cancel := h.storeContext()
	defer cancel()
	if err := h.config.DeleteTracingKey(storeCtx, id); err != nil {
		h.fail(ctx, "delete tracing key", err)
		return
	}
	h.invalidate()
	SendJSON(ctx, map[string]any{"deleted": true})
}

// getUpload serves the bytes of a file an SDK uploaded beside its spans: a Braintrust attachment,
// Langfuse media. It is sent as a download with nosniff, so an uploaded HTML or SVG file can never
// run as a page of the dashboard's origin.
func (h *TracesHandler) getUpload(ctx *fasthttp.RequestCtx) {
	id, _ := ctx.UserValue("id").(string)
	storeCtx, cancel := h.storeContext()
	defer cancel()
	upload, data, err := h.store.ReadTraceUploadBytes(storeCtx, id)
	if err != nil {
		h.fail(ctx, "load upload", err)
		return
	}
	if upload.Status != logstore.TraceUploadStatusDone {
		SendError(ctx, fasthttp.StatusNotFound, "upload has no content yet")
		return
	}
	contentType := upload.ContentType
	disposition := "attachment"
	if strings.HasPrefix(contentType, "image/") && contentType != "image/svg+xml" || strings.HasPrefix(contentType, "audio/") || strings.HasPrefix(contentType, "video/") {
		disposition = "inline" // media the viewer shows in place; none of these can run script
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	ctx.Response.Header.Set("X-Content-Type-Options", "nosniff")
	ctx.Response.Header.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	ctx.Response.Header.Set("Content-Disposition", disposition)
	ctx.SetContentType(contentType)
	ctx.SetBody(data)
}
