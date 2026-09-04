package handlers

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/fasthttp/router"
	"github.com/google/uuid"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/maximhq/bifrost/framework/sidekiq"
	"github.com/maximhq/bifrost/framework/vectorstore"
	"github.com/maximhq/bifrost/framework/warp"
	"github.com/maximhq/bifrost/plugins/logging"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

type warpBackfillJobStore interface {
	GetSidekiqJob(ctx context.Context, id string) (*tables.TableSidekiqJob, error)
	GetInFlightSidekiqJobByKind(ctx context.Context, kind string) (*tables.TableSidekiqJob, error)
}

// WarpHandler is the HTTP face of the Warp service. It parses requests, maps
// service errors to status codes and writes responses; everything Warp actually
// does lives in framework/warp.
type WarpHandler struct {
	service         *warp.Service
	unsubscribeLogs func()
	sidekiqRunner   *sidekiq.Runner
	backfillStore   warpBackfillJobStore
}

// NewWarpLogReader adapts a log manager to what Warp reads through. Exported so
// the server can rebind it when the logging plugin is reloaded.
func NewWarpLogReader(manager logging.LogManager) warp.LogReader {
	if manager == nil {
		return nil
	}
	return warpLogReader{manager}
}

// NewWarpHandler builds the handler and the service behind it.
//
// A nil loggerPlugin is a supported deployment (logging disabled): Warp then
// serves only its configuration routes, because its tools would have nothing to
// read. A nil catalog is likewise supported and simply leaves Warp's own spend
// unpriced. logsStore is separate from loggerPlugin because the two answer
// different questions - the plugin is what Warp researches through, the store
// is where it files what was said - and a deployment can have the store without
// the plugin.
func NewWarpHandler(store configstore.ConfigStore, loggerPlugin *logging.LoggerPlugin, client *bifrost.Bifrost, logsStore logstore.LogStore, vectors vectorstore.VectorStore, runner *sidekiq.Runner, catalog *modelcatalog.ModelCatalog, logger schemas.Logger) *WarpHandler {
	opts := []warp.Option{warp.WithLogger(logger), warp.WithModelCatalog(catalog), warp.WithVectorStore(vectors)}
	if client != nil {
		opts = append(opts, warp.WithEmbeddingExecutor(client.EmbeddingRequest))
	}
	if loggerPlugin != nil {
		opts = append(opts, warp.WithLogReader(warpLogReader{loggerPlugin.GetPluginLogManager()}))
	}
	if logsStore != nil {
		opts = append(opts, warp.WithConversationStore(logsStore))
	}
	handler := &WarpHandler{service: warp.NewService(store, opts...), sidekiqRunner: runner}
	handler.backfillStore, _ = store.(warpBackfillJobStore)
	handler.service.RegisterBackfill(runner)
	if loggerPlugin != nil {
		handler.unsubscribeLogs = loggerPlugin.SubscribeLogCallback(handler.service.IndexLog)
	}
	// Retention runs on a timer rather than on write: what expires is age, so a
	// deployment nobody has chatted on for a month is exactly where a
	// write-triggered sweep would never fire. It is a no-op without a history
	// store, and Shutdown stops it.
	handler.service.StartHistoryCleanup()
	return handler
}

// Shutdown releases the service's model client.
func (h *WarpHandler) Shutdown() {
	if h.unsubscribeLogs != nil {
		h.unsubscribeLogs()
		h.unsubscribeLogs = nil
	}
	h.service.Shutdown()
}

// Service exposes the underlying service to in-process callers.
func (h *WarpHandler) Service() *warp.Service {
	return h.service
}

// RegisterRoutes wires the configuration API. Every route goes through the
// standard middleware chain: Warp reads the deployment's own telemetry, so it
// must never be reachable on an unauthenticated route the way the OAuth2
// issuance handler deliberately is.
func (h *WarpHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	r.GET("/api/warp/config", lib.ChainMiddlewares(h.getConfig, middlewares...))
	r.PUT("/api/warp/config", lib.ChainMiddlewares(h.putConfig, middlewares...))
	r.POST("/api/warp/log-index/backfill", lib.ChainMiddlewares(h.startBackfill, middlewares...))
	r.GET("/api/warp/log-index/backfill/status", lib.ChainMiddlewares(h.backfillStatus, middlewares...))
	r.POST("/api/warp/log-index/backfill/cancel", lib.ChainMiddlewares(h.cancelBackfill, middlewares...))

	// Registered unconditionally, and 503 while Warp cannot answer.
	//
	// This runs once, at startup. Gating the route on CanChat() meant a
	// deployment that turned logging on afterwards kept answering 405 for the
	// life of the process, with nothing to say the feature had become available.
	// The 503 body carries a machine-readable reason, which is what keeps
	// "present but unusable" distinguishable from "absent" - the concern the
	// gate was there for in the first place.
	r.POST("/api/warp/chat", lib.ChainMiddlewares(h.chat, middlewares...))

	// History rides on the same middleware chain. Every route resolves its owner
	// from the request context, so an unauthenticated deployment shares one
	// history and an authenticated one gives each person their own, with no
	// second code path between them.
	if h.service.HasHistory() {
		r.GET("/api/warp/conversations", lib.ChainMiddlewares(h.listConversations, middlewares...))
		r.GET("/api/warp/conversations/{id}", lib.ChainMiddlewares(h.getConversation, middlewares...))
		r.DELETE("/api/warp/conversations/{id}", lib.ChainMiddlewares(h.deleteConversation, middlewares...))
	}
}

type warpBackfillRequest struct {
	StartTime time.Time `json:"start_time"`
	EndTime   time.Time `json:"end_time"`
}

type warpBackfillCancelRequest struct {
	ID string `json:"id,omitempty"`
}

type warpBackfillStatus struct {
	ID     string `json:"id,omitempty"`
	Status string `json:"status"`
	// Pointers, because omitempty does not omit a zero time.Time - it is a
	// struct, never "empty" to the encoder - so the idle and pending responses
	// shipped 0001-01-01 for timestamps they simply do not have. These are
	// optional properties in the schema, and a year-1 date reads as real.
	StartTime   *time.Time `json:"start_time,omitempty"`
	EndTime     *time.Time `json:"end_time,omitempty"`
	Total       int64      `json:"total"`
	Scanned     int        `json:"scanned"`
	Indexed     int        `json:"indexed"`
	Skipped     int        `json:"skipped"`
	Failed      int        `json:"failed"`
	LastError   string     `json:"last_error,omitempty"`
	Message     string     `json:"message,omitempty"`
	CreatedAt   *time.Time `json:"created_at,omitempty"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

func (h *WarpHandler) startBackfill(ctx *fasthttp.RequestCtx) {
	if !warpLocalAdmin(ctx) {
		SendError(ctx, fasthttp.StatusForbidden, "Only administrators can backfill Warp embeddings")
		return
	}
	if h.sidekiqRunner == nil || h.backfillStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, warpBackfillUnavailable)
		return
	}
	var request warpBackfillRequest
	if err := sonic.Unmarshal(ctx.PostBody(), &request); err != nil || request.StartTime.IsZero() || request.EndTime.IsZero() {
		SendError(ctx, fasthttp.StatusBadRequest, "start_time and end_time must be RFC3339 timestamps")
		return
	}
	// Checked here rather than left to BuildBackfillJobMeta, which runs after the
	// conflict lookup: an inverted range sent while a backfill was in flight came
	// back 409 with the running job's status, so the caller went looking for a
	// conflict they did not have instead of the range they got wrong. The service
	// still enforces the same rule for in-process callers.
	if !request.EndTime.After(request.StartTime) {
		SendError(ctx, fasthttp.StatusBadRequest, "end_time must be after start_time")
		return
	}
	if existing, err := h.backfillStore.GetInFlightSidekiqJobByKind(ctx, warp.BackfillJobKind); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to check running jobs")
		return
	} else if existing != nil {
		ctx.SetStatusCode(fasthttp.StatusConflict)
		SendJSON(ctx, warpBackfillStatusFromRow(existing))
		return
	}
	metadata, err := h.service.BuildBackfillJobMeta(ctx, request.StartTime, request.EndTime)
	switch {
	case errors.Is(err, warp.ErrInvalidConfig):
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	case errors.Is(err, warp.ErrUnavailable), errors.Is(err, warp.ErrNoVectorStore):
		SendError(ctx, fasthttp.StatusServiceUnavailable, err.Error())
		return
	case err != nil:
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to prepare Warp backfill")
		return
	}
	id := uuid.NewString()
	createdBy, _ := ctx.UserValue(schemas.BifrostContextKeyUserID).(string)
	if err := h.sidekiqRunner.EnqueuePartitioned(ctx, id, warp.BackfillJobKind, "warp_log_embeddings", metadata, createdBy); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to start Warp backfill")
		return
	}
	ctx.SetStatusCode(fasthttp.StatusAccepted)
	if job, err := h.backfillStore.GetSidekiqJob(ctx, id); err == nil && job != nil {
		SendJSON(ctx, warpBackfillStatusFromRow(job))
		return
	}
	SendJSON(ctx, warpBackfillStatus{ID: id, Status: tables.SidekiqStatusPending})
}

// warpBackfillUnavailable names both dependencies the start and cancel routes
// need, because either can be missing independently: NewWarpHandler takes the
// runner directly but derives backfillStore from a type assertion on the store,
// so a non-nil runner with a nil store is reachable - and blaming the runner
// there points at the wrong thing.
const warpBackfillUnavailable = "Background job runner or backfill store is not available"

func (h *WarpHandler) backfillStatus(ctx *fasthttp.RequestCtx) {
	if !warpLocalAdmin(ctx) {
		SendError(ctx, fasthttp.StatusForbidden, "Only administrators can inspect Warp backfills")
		return
	}
	// This endpoint reads job rows and never enqueues, so the store is the only
	// dependency it has. Naming the runner pointed at the wrong thing and
	// contradicted the documented status contract.
	if h.backfillStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "Warp backfill history is not available on this deployment")
		return
	}
	id := strings.TrimSpace(string(ctx.QueryArgs().Peek("id")))
	var job *tables.TableSidekiqJob
	var err error
	if id != "" {
		job, err = h.backfillStore.GetSidekiqJob(ctx, id)
	} else {
		job, err = h.backfillStore.GetInFlightSidekiqJobByKind(ctx, warp.BackfillJobKind)
	}
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to fetch Warp backfill status")
		return
	}
	// An explicit id has to name a Warp backfill. Without the kind check this
	// endpoint describes any sidekiq job in the deployment - handing a caller
	// asking about a backfill the progress metadata of a pricing sync.
	if job != nil && id != "" && job.Kind != warp.BackfillJobKind {
		job = nil
	}
	if job == nil {
		if id != "" {
			SendError(ctx, fasthttp.StatusNotFound, "Job not found")
			return
		}
		SendJSON(ctx, warpBackfillStatus{Status: "idle"})
		return
	}
	SendJSON(ctx, warpBackfillStatusFromRow(job))
}

func (h *WarpHandler) cancelBackfill(ctx *fasthttp.RequestCtx) {
	if !warpLocalAdmin(ctx) {
		SendError(ctx, fasthttp.StatusForbidden, "Only administrators can cancel Warp backfills")
		return
	}
	if h.sidekiqRunner == nil || h.backfillStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, warpBackfillUnavailable)
		return
	}
	var request warpBackfillCancelRequest
	if len(ctx.PostBody()) > 0 {
		if err := sonic.Unmarshal(ctx.PostBody(), &request); err != nil {
			SendError(ctx, fasthttp.StatusBadRequest, "Invalid request payload")
			return
		}
	}
	var job *tables.TableSidekiqJob
	var err error
	if strings.TrimSpace(request.ID) != "" {
		job, err = h.backfillStore.GetSidekiqJob(ctx, strings.TrimSpace(request.ID))
	} else {
		job, err = h.backfillStore.GetInFlightSidekiqJobByKind(ctx, warp.BackfillJobKind)
	}
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to fetch Warp backfill")
		return
	}
	// CancelSidekiqJob does not check Kind, so without this the Warp endpoint
	// could cancel any pending or running job in the deployment by id.
	if job != nil && job.Kind != warp.BackfillJobKind {
		job = nil
	}
	if job == nil {
		SendError(ctx, fasthttp.StatusNotFound, "Job not found")
		return
	}
	cancelled, err := h.sidekiqRunner.Cancel(ctx, job.ID)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to cancel Warp backfill")
		return
	}
	status := warpBackfillStatusFromRow(job)
	if refreshed, err := h.backfillStore.GetSidekiqJob(ctx, job.ID); err == nil && refreshed != nil {
		status = warpBackfillStatusFromRow(refreshed)
	} else if cancelled {
		// Cancel succeeded but the re-read did not, so the row we hold predates
		// it. Reporting its "running" back would say the cancellation failed when
		// it did not - and CancelSidekiqJob writes this status directly, so it is
		// the state we know to be true.
		//
		// Only when Cancel actually cancelled. It returns false for a job that had
		// already finished, and claiming "cancelled" there reports an outcome this
		// request did not produce - the job may well have completed successfully.
		// With a failed re-read and nothing cancelled, the row we already hold is
		// the most honest thing available.
		status.Status = tables.SidekiqStatusCancelled
	}
	SendJSON(ctx, status)
}

func warpBackfillStatusFromRow(job *tables.TableSidekiqJob) warpBackfillStatus {
	status := warpBackfillStatus{ID: job.ID, Status: job.Status, LastError: job.LastError, CreatedAt: &job.CreatedAt, StartedAt: job.StartedAt, CompletedAt: job.CompletedAt}
	var meta warp.BackfillJobMeta
	if sonic.Unmarshal([]byte(job.Metadata), &meta) == nil {
		status.StartTime, status.EndTime, status.Total = &meta.StartTime, &meta.EndTime, meta.Total
		status.Scanned, status.Indexed, status.Skipped, status.Failed = meta.Scanned, meta.Indexed, meta.Skipped, meta.Failed
		status.Message = meta.Message
		if status.LastError == "" {
			status.LastError = meta.LastError
		}
	}
	return status
}

func warpLocalAdmin(ctx *fasthttp.RequestCtx) bool {
	admin, _ := ctx.UserValue(schemas.IsLocalAdminContextKey).(bool)
	return admin
}

// getConfig serves the settings page. It is safe for any authenticated caller
// because the stored credential is never part of the response.
func (h *WarpHandler) getConfig(ctx *fasthttp.RequestCtx) {
	view, err := h.service.ConfigView(ctx)
	if errors.Is(err, warp.ErrUnavailable) {
		SendError(ctx, fasthttp.StatusServiceUnavailable, err.Error())
		return
	}
	if err != nil {
		logger.Warn("failed to read warp configuration: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to read warp configuration")
		return
	}
	SendJSON(ctx, view)
}

// putConfig is admin-only, on the same reasoning notifications.go applies to
// publishing: RBAC in this transport is enterprise-only and path-based, so the
// OSS floor is enforced in the handler. A single PUT plants a credential that
// the server will then use to make outbound calls, which is not something an
// ordinary dashboard user should be able to do.
func (h *WarpHandler) putConfig(ctx *fasthttp.RequestCtx) {
	if localAdmin, _ := ctx.UserValue(schemas.IsLocalAdminContextKey).(bool); !localAdmin {
		SendError(ctx, fasthttp.StatusForbidden, "Only administrators can configure Warp")
		return
	}
	var input warp.ConfigInput
	if err := sonic.Unmarshal(ctx.PostBody(), &input); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid request payload")
		return
	}
	view, err := h.service.SaveConfig(ctx, &input)
	switch {
	case errors.Is(err, warp.ErrUnavailable):
		SendError(ctx, fasthttp.StatusServiceUnavailable, err.Error())
		return
	case errors.Is(err, warp.ErrNoVectorStore):
		h.sendUnavailable(ctx, schemas.WarpUnavailableNoVectorStore, err.Error())
		return
	case errors.Is(err, warp.ErrInvalidConfig):
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	case errors.Is(err, warp.ErrBackfillInProgress):
		SendError(ctx, fasthttp.StatusConflict, err.Error())
		return
	case err != nil:
		// Log the cause. The client gets a generic message because a raw driver
		// error can name columns and constraints, but swallowing it entirely
		// leaves an operator staring at a 500 with nothing to act on - which is
		// exactly what a schema drift between the table and the row struct
		// produces.
		logger.Warn("failed to save warp configuration: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to save warp configuration")
		return
	}
	SendJSON(ctx, view)
}
