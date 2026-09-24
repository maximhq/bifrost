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
	// enabled reports the Warp feature flag. It is read per request and per
	// log rather than once at startup, so toggling the flag takes effect
	// without a restart. Nil means ungated.
	enabled func() bool
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
//
// enabled is the Warp feature flag. The service is still built when it is off,
// because the flag can be switched on at runtime and routes are only
// registered once; what the flag withholds is every route and the indexing of
// new logs.
func NewWarpHandler(store configstore.ConfigStore, loggerPlugin *logging.LoggerPlugin, client *bifrost.Bifrost, logsStore logstore.LogStore, vectors vectorstore.VectorStore, runner *sidekiq.Runner, catalog *modelcatalog.ModelCatalog, logger schemas.Logger, enabled func() bool) *WarpHandler {
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
	handler := &WarpHandler{service: warp.NewService(store, opts...), sidekiqRunner: runner, enabled: enabled}
	handler.backfillStore, _ = store.(warpBackfillJobStore)
	handler.service.RegisterBackfill(runner)
	if loggerPlugin != nil {
		handler.unsubscribeLogs = loggerPlugin.SubscribeLogCallback(handler.indexLog)
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

// isEnabled reports whether the Warp feature flag is on.
func (h *WarpHandler) isEnabled() bool {
	return h.enabled == nil || h.enabled()
}

// indexLog feeds a finished log to Warp's indexer while the flag is on. With it
// off nothing is embedded: indexing spends provider calls on every request, and
// a feature nobody can use should not be paying for them. Logs written while it
// was off can be indexed afterwards with a backfill.
func (h *WarpHandler) indexLog(ctx context.Context, entry *logstore.Log) {
	if !h.isEnabled() {
		return
	}
	h.service.IndexLog(ctx, entry)
}

// gated answers 404 while the Warp feature flag is off, so a disabled Warp is
// indistinguishable from one this build does not have. The check runs per
// request because routes are registered once and the flag can change after.
func (h *WarpHandler) gated(next fasthttp.RequestHandler) fasthttp.RequestHandler {
	return func(ctx *fasthttp.RequestCtx) {
		if !h.isEnabled() {
			SendError(ctx, fasthttp.StatusNotFound, "Warp is not enabled. Turn on the \"warp\" feature flag to use it.")
			return
		}
		next(ctx)
	}
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
	r.GET("/api/warp/config", lib.ChainMiddlewares(h.gated(h.getConfig), middlewares...))
	r.PUT("/api/warp/config", lib.ChainMiddlewares(h.gated(h.putConfig), middlewares...))
	r.POST("/api/warp/log-index/backfill", lib.ChainMiddlewares(h.gated(h.startBackfill), middlewares...))
	r.GET("/api/warp/log-index/backfill/status", lib.ChainMiddlewares(h.gated(h.backfillStatus), middlewares...))
	r.POST("/api/warp/log-index/backfill/cancel", lib.ChainMiddlewares(h.gated(h.cancelBackfill), middlewares...))
	// A read-only summary for the tray. Unlike the backfill controls above it is
	// not admin-gated: whether semantic search is usable is something everyone
	// who can ask Warp a question needs to see.
	r.GET("/api/warp/log-index/status", lib.ChainMiddlewares(h.gated(h.logIndexStatus), middlewares...))

	// Registered unconditionally, and 503 while Warp cannot answer.
	//
	// This runs once, at startup. Gating the route on CanChat() meant a
	// deployment that turned logging on afterwards kept answering 405 for the
	// life of the process, with nothing to say the feature had become available.
	// The 503 body carries a machine-readable reason, which is what keeps
	// "present but unusable" distinguishable from "absent" - the concern the
	// gate was there for in the first place.
	r.POST("/api/warp/chat", lib.ChainMiddlewares(h.gated(h.chat), middlewares...))

	// History rides on the same middleware chain. Every route resolves its owner
	// from the request context, so an unauthenticated deployment shares one
	// history and an authenticated one gives each person their own, with no
	// second code path between them.
	if h.service.HasHistory() {
		r.GET("/api/warp/conversations", lib.ChainMiddlewares(h.gated(h.listConversations), middlewares...))
		r.GET("/api/warp/conversations/{id}", lib.ChainMiddlewares(h.gated(h.getConversation), middlewares...))
		r.DELETE("/api/warp/conversations/{id}", lib.ChainMiddlewares(h.gated(h.deleteConversation), middlewares...))
	}
}

type warpBackfillRequest struct {
	StartTime time.Time `json:"start_time"`
	EndTime   time.Time `json:"end_time"`
	// Restart discards a resumable checkpoint from a prior failed/cancelled run
	// over this same window and scans from the beginning anyway. Omitted (the
	// common case) resumes automatically when one is found.
	Restart bool `json:"restart,omitempty"`
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
	if !warpAdmin(ctx) {
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
	metadata, err := h.service.BuildBackfillJobMeta(ctx, request.StartTime, request.EndTime, request.Restart)
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
	if !warpAdmin(ctx) {
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
	if !warpAdmin(ctx) {
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
	if errors.Is(err, warp.ErrUnavailable) {
		// The same unavailable state getConfig reports, so the tray gets an
		// answer it already knows how to render rather than a generic failure.
		SendError(ctx, fasthttp.StatusServiceUnavailable, "Warp configuration is unavailable")
		return
	}
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
	// A job from a previous embedding space says nothing about the current one.
	// GetLatestSidekiqJobByKind returns the newest Warp backfill regardless of
	// the namespace it ran against, so after a namespace change the tray showed
	// the old run's counts - or its failure - as the state of an index that had
	// never been built.
	if job != nil && !warpJobMatchesNamespace(job, view.LogVectorStoreNamespace) {
		job = nil
	}
	if job != nil {
		backfill := warpBackfillStatusFromRow(job)
		// This route has no admin gate, unlike the backfill endpoints. LastError
		// carries whatever the provider or vector store said, which can name
		// endpoints, models and internal detail. An administrator debugging a
		// stalled index needs exactly that text; an ordinary dashboard user needs
		// the state and the counts, and gets those without the provider's words.
		// Local admin only: this route needs just WarpSession View, so a role ID proves nothing here.
		if !warpLocalAdmin(ctx) {
			backfill.LastError = ""
		}
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

// warpJobMatchesNamespace reports whether a backfill ran against the namespace
// currently configured. A job whose metadata names no namespace predates the
// field and is treated as matching, so an upgrade does not blank the tray.
//
// Metadata that does not parse is a different case and does not match: an
// absent namespace is a job we know predates the field, while an unreadable one
// is a job we know nothing about, and reporting its "failed" as the state of
// the current index asserts something unsupported by anything on the row.
func warpJobMatchesNamespace(job *tables.TableSidekiqJob, namespace string) bool {
	var meta warp.BackfillJobMeta
	if sonic.Unmarshal([]byte(job.Metadata), &meta) != nil {
		return false
	}
	if strings.TrimSpace(meta.Namespace) == "" {
		return true
	}
	return strings.TrimSpace(meta.Namespace) == strings.TrimSpace(namespace)
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

// warpLocalAdmin reports whether the caller is the local admin: the password
// login, or any caller while dashboard auth is off.
func warpLocalAdmin(ctx *fasthttp.RequestCtx) bool {
	admin, _ := ctx.UserValue(schemas.IsLocalAdminContextKey).(bool)
	return admin
}

// warpAdmin admits the local admin, or a caller enterprise RBAC already authorized for Warp Update (the only place a role ID is set).
func warpAdmin(ctx *fasthttp.RequestCtx) bool {
	if warpLocalAdmin(ctx) {
		return true
	}
	roleID, _ := ctx.UserValue(schemas.BifrostContextKeyUserRoleID).(uint)
	return roleID != 0
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
	if !warpAdmin(ctx) {
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
