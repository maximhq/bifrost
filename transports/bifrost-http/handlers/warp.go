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
	GetLatestSidekiqJobByKind(ctx context.Context, kind string) (*tables.TableSidekiqJob, error)
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

// NewWarpHandler builds the handler and the service behind it. A nil logManager
// is a supported deployment (logging disabled): Warp then serves only its
// configuration routes, because its tools would have nothing to read. A nil
// catalog is likewise supported and simply leaves Warp's own spend unpriced.
func NewWarpHandler(store configstore.ConfigStore, loggerPlugin *logging.LoggerPlugin, client *bifrost.Bifrost, vectors vectorstore.VectorStore, runner *sidekiq.Runner, catalog *modelcatalog.ModelCatalog, logger schemas.Logger) *WarpHandler {
	opts := []warp.Option{warp.WithLogger(logger), warp.WithModelCatalog(catalog), warp.WithVectorStore(vectors)}
	if client != nil {
		opts = append(opts, warp.WithEmbeddingExecutor(client.EmbeddingRequest))
	}
	if loggerPlugin != nil {
		opts = append(opts, warp.WithLogReader(warpLogReader{loggerPlugin.GetPluginLogManager()}))
	}
	handler := &WarpHandler{service: warp.NewService(store, opts...), sidekiqRunner: runner}
	handler.backfillStore, _ = store.(warpBackfillJobStore)
	handler.service.RegisterBackfill(runner)
	if loggerPlugin != nil {
		handler.unsubscribeLogs = loggerPlugin.SubscribeLogCallback(handler.service.IndexLog)
	}
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
	// A read-only summary for the tray. Unlike the backfill controls above it is
	// not admin-gated: whether semantic search is usable is something everyone
	// who can ask Warp a question needs to see.
	r.GET("/api/warp/log-index/status", lib.ChainMiddlewares(h.logIndexStatus, middlewares...))

	// The chat route exists only where Warp has data to read and a way to reach
	// a model; see Service.CanChat for why absent beats always-failing.
	if h.service.CanChat() {
		r.POST("/api/warp/chat", lib.ChainMiddlewares(h.chat, middlewares...))
	}

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
	ID          string     `json:"id,omitempty"`
	Status      string     `json:"status"`
	StartTime   time.Time  `json:"start_time,omitempty"`
	EndTime     time.Time  `json:"end_time,omitempty"`
	Total       int64      `json:"total"`
	Scanned     int        `json:"scanned"`
	Indexed     int        `json:"indexed"`
	Skipped     int        `json:"skipped"`
	Failed      int        `json:"failed"`
	LastError   string     `json:"last_error,omitempty"`
	Message     string     `json:"message,omitempty"`
	CreatedAt   time.Time  `json:"created_at,omitempty"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

func (h *WarpHandler) startBackfill(ctx *fasthttp.RequestCtx) {
	if !warpLocalAdmin(ctx) {
		SendError(ctx, fasthttp.StatusForbidden, "Only administrators can backfill Warp embeddings")
		return
	}
	if h.sidekiqRunner == nil || h.backfillStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "Background job runner is not available")
		return
	}
	var request warpBackfillRequest
	if err := sonic.Unmarshal(ctx.PostBody(), &request); err != nil || request.StartTime.IsZero() || request.EndTime.IsZero() {
		SendError(ctx, fasthttp.StatusBadRequest, "start_time and end_time must be RFC3339 timestamps")
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

func (h *WarpHandler) backfillStatus(ctx *fasthttp.RequestCtx) {
	if !warpLocalAdmin(ctx) {
		SendError(ctx, fasthttp.StatusForbidden, "Only administrators can inspect Warp backfills")
		return
	}
	if h.backfillStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "Background job runner is not available")
		return
	}
	id := strings.TrimSpace(string(ctx.QueryArgs().Peek("id")))
	var job *tables.TableSidekiqJob
	var err error
	if id != "" {
		job, err = h.backfillStore.GetSidekiqJob(ctx, id)
	} else {
		job, err = h.backfillStore.GetInFlightSidekiqJobByKind(ctx, warp.BackfillJobKind)
		if err == nil && job == nil {
			// Nothing running. A reloaded page still wants to see how the last
			// backfill ended, so fall back to the newest job of any status.
			job, err = h.backfillStore.GetLatestSidekiqJobByKind(ctx, warp.BackfillJobKind)
		}
	}
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to fetch Warp backfill status")
		return
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
		SendError(ctx, fasthttp.StatusServiceUnavailable, "Background job runner is not available")
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
	if job == nil {
		SendError(ctx, fasthttp.StatusNotFound, "Job not found")
		return
	}
	if _, err := h.sidekiqRunner.Cancel(ctx, job.ID); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to cancel Warp backfill")
		return
	}
	if refreshed, err := h.backfillStore.GetSidekiqJob(ctx, job.ID); err == nil && refreshed != nil {
		job = refreshed
	}
	SendJSON(ctx, warpBackfillStatusFromRow(job))
}

// warpLogIndexStatus folds the vector store connection, the embedding
// configuration and the latest indexing job into one state the tray can show.
type warpLogIndexStatus struct {
	// State is one of: unavailable (no vector store), not_configured (no
	// embedding model), indexing (a backfill is in flight), failed (the last
	// backfill failed), ready.
	State                string              `json:"state"`
	VectorStoreConnected bool                `json:"vector_store_connected"`
	EmbeddingConfigured  bool                `json:"embedding_configured"`
	Backfill             *warpBackfillStatus `json:"backfill,omitempty"`
}

const (
	warpIndexStateUnavailable   = "unavailable"
	warpIndexStateNotConfigured = "not_configured"
	warpIndexStateIndexing      = "indexing"
	warpIndexStateFailed        = "failed"
	warpIndexStateReady         = "ready"
)

// logIndexStatus serves the tray's indexing summary.
func (h *WarpHandler) logIndexStatus(ctx *fasthttp.RequestCtx) {
	view, err := h.service.ConfigView(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to read Warp configuration")
		return
	}
	status := warpLogIndexStatus{
		VectorStoreConnected: view.VectorStoreConnected,
		EmbeddingConfigured:  view.EmbeddingProvider != "" && view.EmbeddingModel != "",
	}
	switch {
	case !status.VectorStoreConnected:
		status.State = warpIndexStateUnavailable
	case !status.EmbeddingConfigured:
		status.State = warpIndexStateNotConfigured
	default:
		status.State = warpIndexStateReady
	}
	if h.backfillStore == nil || status.State != warpIndexStateReady {
		SendJSON(ctx, status)
		return
	}
	job, err := h.backfillStore.GetInFlightSidekiqJobByKind(ctx, warp.BackfillJobKind)
	if err == nil && job == nil {
		job, err = h.backfillStore.GetLatestSidekiqJobByKind(ctx, warp.BackfillJobKind)
	}
	if err != nil {
		// The index is still usable without the job history; the summary is
		// worth more than an error here.
		logger.Warn("failed to read Warp backfill job for index status: %v", err)
		SendJSON(ctx, status)
		return
	}
	if job != nil {
		backfill := warpBackfillStatusFromRow(job)
		status.Backfill = &backfill
		switch job.Status {
		case tables.SidekiqStatusPending, tables.SidekiqStatusRunning:
			status.State = warpIndexStateIndexing
		case tables.SidekiqStatusFailed:
			status.State = warpIndexStateFailed
		}
	}
	SendJSON(ctx, status)
}

func warpBackfillStatusFromRow(job *tables.TableSidekiqJob) warpBackfillStatus {
	status := warpBackfillStatus{ID: job.ID, Status: job.Status, LastError: job.LastError, CreatedAt: job.CreatedAt, StartedAt: job.StartedAt, CompletedAt: job.CompletedAt}
	var meta warp.BackfillJobMeta
	if sonic.Unmarshal([]byte(job.Metadata), &meta) == nil {
		status.StartTime, status.EndTime, status.Total = meta.StartTime, meta.EndTime, meta.Total
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
