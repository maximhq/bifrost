package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/plugins/heimdall"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// HeimdallService is what the Heimdall handler needs from the plugin. *heimdall.Plugin implements it.
type HeimdallService interface {
	Status(ctx context.Context) (*heimdall.Status, error)
	// AllowUnauthenticatedMutations says whether POST routes may be used when dashboard auth is off.
	AllowUnauthenticatedMutations() bool
	ListSessions(ctx context.Context, q heimdall.SessionQuery) (*heimdall.SessionPage, error)
	GetSession(ctx context.Context, id string) (*heimdall.SessionDetail, error)
	StartRun(ctx context.Context, req heimdall.CreateRunRequest) (*heimdall.RunDetail, error)
	ListRuns(ctx context.Context, q heimdall.RunQuery) (*heimdall.RunPage, error)
	GetRun(ctx context.Context, id string) (*heimdall.RunDetail, error)
	CancelRun(ctx context.Context, id string) (*heimdall.RunDetail, error)
	ListRunSessions(ctx context.Context, id string, q heimdall.RunSessionQuery) (*heimdall.RunSessionPage, error)
	GetRunSession(ctx context.Context, id, sessionID string) (*heimdall.RunSessionDetail, error)
}

// The real plugin must satisfy the handler's interface.
var _ HeimdallService = (*heimdall.Plugin)(nil)

// HeimdallResolver returns the plugin when it is loaded, or nil.
type HeimdallResolver func() HeimdallService

// HeimdallHandler serves /api/heimdall. It resolves the plugin on every request, so enabling the plugin later
// needs no restart; until then each route answers 503 heimdall_disabled.
type HeimdallHandler struct {
	resolve HeimdallResolver
}

// NewHeimdallHandler returns a handler that is safe to wire whether or not the plugin is loaded.
func NewHeimdallHandler(resolve HeimdallResolver) *HeimdallHandler {
	return &HeimdallHandler{resolve: resolve}
}

// RegisterRoutes adds the Heimdall routes.
func (h *HeimdallHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	r.GET("/api/heimdall/status", lib.ChainMiddlewares(h.status, middlewares...))
	r.GET("/api/heimdall/sessions", lib.ChainMiddlewares(h.listSessions, middlewares...))
	r.GET("/api/heimdall/sessions/{session_id}", lib.ChainMiddlewares(h.getSession, middlewares...))
	r.POST("/api/heimdall/runs", lib.ChainMiddlewares(h.startRun, middlewares...))
	r.GET("/api/heimdall/runs", lib.ChainMiddlewares(h.listRuns, middlewares...))
	r.GET("/api/heimdall/runs/{id}", lib.ChainMiddlewares(h.getRun, middlewares...))
	r.POST("/api/heimdall/runs/{id}/cancel", lib.ChainMiddlewares(h.cancelRun, middlewares...))
	r.GET("/api/heimdall/runs/{id}/sessions", lib.ChainMiddlewares(h.listRunSessions, middlewares...))
	r.GET("/api/heimdall/runs/{id}/sessions/{session_id}", lib.ChainMiddlewares(h.getRunSession, middlewares...))
}

// service returns the plugin, or answers 503 and returns nil.
func (h *HeimdallHandler) service(ctx *fasthttp.RequestCtx) HeimdallService {
	if h == nil || h.resolve == nil {
		sendHeimdallDisabled(ctx)
		return nil
	}
	svc := h.resolve()
	if svc == nil {
		sendHeimdallDisabled(ctx)
		return nil
	}
	return svc
}

func sendHeimdallDisabled(ctx *fasthttp.RequestCtx) {
	SendError(ctx, fasthttp.StatusServiceUnavailable, "heimdall_disabled: the heimdall plugin is not loaded; enable it in the plugin settings")
}

// mutating returns the plugin for a route that changes state. With dashboard auth off the API is open to the
// machine, so such a route is refused unless the plugin is configured to allow it.
func (h *HeimdallHandler) mutating(ctx *fasthttp.RequestCtx) HeimdallService {
	svc := h.service(ctx)
	if svc == nil {
		return nil
	}
	if isAuthBypassed(ctx) && !svc.AllowUnauthenticatedMutations() {
		SendError(ctx, fasthttp.StatusForbidden, "auth_required: starting or cancelling a run needs dashboard authentication; turn it on, or set allow_unauthenticated_mutations for a demo")
		return nil
	}
	return svc
}

// sendHeimdallError answers with the status and code the service chose. Anything else is a 500.
func sendHeimdallError(ctx *fasthttp.RequestCtx, err error) {
	var apiErr *heimdall.Error
	if errors.As(err, &apiErr) {
		SendError(ctx, apiErr.Status, apiErr.Code+": "+apiErr.Message)
		return
	}
	SendError(ctx, fasthttp.StatusInternalServerError, "internal: "+err.Error())
}

func badRequest(ctx *fasthttp.RequestCtx, format string, args ...any) {
	SendError(ctx, fasthttp.StatusBadRequest, "invalid_request: "+fmt.Sprintf(format, args...))
}

// pathValue reads a route parameter.
func pathValue(ctx *fasthttp.RequestCtx, name string) (string, bool) {
	v, ok := ctx.UserValue(name).(string)
	v = strings.TrimSpace(v)
	return v, ok && v != ""
}

// intQuery reads an optional non-negative integer query argument.
func intQuery(ctx *fasthttp.RequestCtx, name string) (int, bool) {
	raw := string(ctx.QueryArgs().Peek(name))
	if raw == "" {
		return 0, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		badRequest(ctx, "%s must be a non-negative integer", name)
		return 0, false
	}
	return n, true
}

// timeQuery reads an optional RFC 3339 time query argument.
func timeQuery(ctx *fasthttp.RequestCtx, name string) (*time.Time, bool) {
	raw := string(ctx.QueryArgs().Peek(name))
	if raw == "" {
		return nil, true
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		badRequest(ctx, "%s must be an RFC 3339 time", name)
		return nil, false
	}
	return &t, true
}

func (h *HeimdallHandler) status(ctx *fasthttp.RequestCtx) {
	svc := h.service(ctx)
	if svc == nil {
		return
	}
	st, err := svc.Status(ctx)
	if err != nil {
		sendHeimdallError(ctx, err)
		return
	}
	SendJSON(ctx, st)
}

func (h *HeimdallHandler) listSessions(ctx *fasthttp.RequestCtx) {
	svc := h.service(ctx)
	if svc == nil {
		return
	}
	q := heimdall.SessionQuery{Provider: string(ctx.QueryArgs().Peek("provider")), Model: string(ctx.QueryArgs().Peek("model"))}
	var ok bool
	if q.Limit, ok = intQuery(ctx, "limit"); !ok {
		return
	}
	if q.Offset, ok = intQuery(ctx, "offset"); !ok {
		return
	}
	if q.StartTime, ok = timeQuery(ctx, "start_time"); !ok {
		return
	}
	if q.EndTime, ok = timeQuery(ctx, "end_time"); !ok {
		return
	}
	page, err := svc.ListSessions(ctx, q)
	if err != nil {
		sendHeimdallError(ctx, err)
		return
	}
	SendJSON(ctx, page)
}

func (h *HeimdallHandler) getSession(ctx *fasthttp.RequestCtx) {
	svc := h.service(ctx)
	if svc == nil {
		return
	}
	id, ok := pathValue(ctx, "session_id")
	if !ok {
		badRequest(ctx, "a session id is required")
		return
	}
	detail, err := svc.GetSession(ctx, id)
	if err != nil {
		sendHeimdallError(ctx, err)
		return
	}
	SendJSON(ctx, detail)
}

// startRun answers 202: the run is queued and goes on in the background.
func (h *HeimdallHandler) startRun(ctx *fasthttp.RequestCtx) {
	svc := h.mutating(ctx)
	if svc == nil {
		return
	}
	var req heimdall.CreateRunRequest
	dec := json.NewDecoder(bytes.NewReader(ctx.PostBody()))
	dec.DisallowUnknownFields() // a misspelt field would otherwise be silently ignored
	if err := dec.Decode(&req); err != nil {
		badRequest(ctx, "the body is not a valid run request: %v", err)
		return
	}
	if dec.More() {
		badRequest(ctx, "the body holds more than one JSON value")
		return
	}
	run, err := svc.StartRun(ctx, req)
	if err != nil {
		sendHeimdallError(ctx, err)
		return
	}
	lib.RecordAuditChange(ctx, "heimdall.run.started", "heimdall_run", run.ID, run.Name, nil,
		map[string]any{"baseline": run.Baseline, "candidate": run.Candidate, "max_usd": run.MaxUSD})
	SendJSONWithStatus(ctx, run, fasthttp.StatusAccepted)
}

func (h *HeimdallHandler) listRuns(ctx *fasthttp.RequestCtx) {
	svc := h.service(ctx)
	if svc == nil {
		return
	}
	q := heimdall.RunQuery{Status: string(ctx.QueryArgs().Peek("status")), Verdict: string(ctx.QueryArgs().Peek("verdict"))}
	var ok bool
	if q.Limit, ok = intQuery(ctx, "limit"); !ok {
		return
	}
	if q.Offset, ok = intQuery(ctx, "offset"); !ok {
		return
	}
	page, err := svc.ListRuns(ctx, q)
	if err != nil {
		sendHeimdallError(ctx, err)
		return
	}
	SendJSON(ctx, page)
}

func (h *HeimdallHandler) getRun(ctx *fasthttp.RequestCtx) {
	svc := h.service(ctx)
	if svc == nil {
		return
	}
	id, ok := pathValue(ctx, "id")
	if !ok {
		badRequest(ctx, "a run id is required")
		return
	}
	run, err := svc.GetRun(ctx, id)
	if err != nil {
		sendHeimdallError(ctx, err)
		return
	}
	SendJSON(ctx, run)
}

func (h *HeimdallHandler) cancelRun(ctx *fasthttp.RequestCtx) {
	svc := h.mutating(ctx)
	if svc == nil {
		return
	}
	id, ok := pathValue(ctx, "id")
	if !ok {
		badRequest(ctx, "a run id is required")
		return
	}
	run, err := svc.CancelRun(ctx, id)
	if err != nil {
		sendHeimdallError(ctx, err)
		return
	}
	lib.RecordAuditChange(ctx, "heimdall.run.cancelled", "heimdall_run", run.ID, run.Name, nil, map[string]any{"status": run.Status})
	SendJSON(ctx, run)
}

func (h *HeimdallHandler) listRunSessions(ctx *fasthttp.RequestCtx) {
	svc := h.service(ctx)
	if svc == nil {
		return
	}
	id, ok := pathValue(ctx, "id")
	if !ok {
		badRequest(ctx, "a run id is required")
		return
	}
	q := heimdall.RunSessionQuery{Filter: string(ctx.QueryArgs().Peek("filter"))}
	if q.Limit, ok = intQuery(ctx, "limit"); !ok {
		return
	}
	if q.Offset, ok = intQuery(ctx, "offset"); !ok {
		return
	}
	page, err := svc.ListRunSessions(ctx, id, q)
	if err != nil {
		sendHeimdallError(ctx, err)
		return
	}
	SendJSON(ctx, page)
}

func (h *HeimdallHandler) getRunSession(ctx *fasthttp.RequestCtx) {
	svc := h.service(ctx)
	if svc == nil {
		return
	}
	id, okID := pathValue(ctx, "id")
	sessionID, okSession := pathValue(ctx, "session_id")
	if !okID || !okSession {
		badRequest(ctx, "a run id and a session id are required")
		return
	}
	detail, err := svc.GetRunSession(ctx, id, sessionID)
	if err != nil {
		sendHeimdallError(ctx, err)
		return
	}
	SendJSON(ctx, detail)
}
