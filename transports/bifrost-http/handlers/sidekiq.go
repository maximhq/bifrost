package handlers

import (
	"context"
	"strings"
	"time"

	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/sidekiq"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

const (
	sidekiqJobsDefaultWindow = 24 * time.Hour
	sidekiqJobsLimit         = 50
)

// SidekiqJobsStore is the read surface the generic jobs endpoint needs from the job store.
type SidekiqJobsStore interface {
	ListSidekiqJobs(ctx context.Context, terminalSince time.Time, limit int) ([]tables.TableSidekiqJob, error)
}

// SidekiqJobsRunner is the slice of *sidekiq.Runner the generic jobs endpoint uses.
type SidekiqJobsRunner interface {
	Cancel(ctx context.Context, id string) (bool, error)
	Summarize(kind, metadata string) (sidekiq.JobSummary, bool)
}

// SidekiqHandler exposes every sidekiq job, whatever its kind, so one UI surface can show
// status and progress and cancel any of them. Feature-specific routes (cost recalculation,
// Warp backfill) stay as they are and remain the way to start a job.
type SidekiqHandler struct {
	store  SidekiqJobsStore
	runner SidekiqJobsRunner
}

// NewSidekiqHandler returns a handler; a nil store or runner makes its routes answer 503.
func NewSidekiqHandler(store SidekiqJobsStore, runner SidekiqJobsRunner) *SidekiqHandler {
	return &SidekiqHandler{store: store, runner: runner}
}

// RegisterRoutes registers the generic job routes.
func (h *SidekiqHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	r.GET("/api/sidekiq/jobs", lib.ChainMiddlewares(h.listJobs, middlewares...))
	r.POST("/api/sidekiq/jobs/{id}/cancel", lib.ChainMiddlewares(h.cancelJob, middlewares...))
}

type sidekiqJobProgress struct {
	Done  int64 `json:"done"`
	Total int64 `json:"total"`
}

type sidekiqJobView struct {
	ID          string              `json:"id"`
	Kind        string              `json:"kind"`
	Status      string              `json:"status"`
	Progress    *sidekiqJobProgress `json:"progress,omitempty"` // nil when the kind reports none
	Message     string              `json:"message,omitempty"`
	LastError   string              `json:"last_error,omitempty"`
	Cancellable bool                `json:"cancellable"`
	CreatedAt   time.Time           `json:"created_at"`
	StartedAt   *time.Time          `json:"started_at,omitempty"`
	UpdatedAt   time.Time           `json:"updated_at"`
	CompletedAt *time.Time          `json:"completed_at,omitempty"`
}

type sidekiqJobsResponse struct {
	Jobs []sidekiqJobView `json:"jobs"`
}

// sidekiqAdmin gates both routes. Warp backfills are admin-only on their own routes, so the
// generic surface must not be a way around that: it admits the same callers Warp does.
func sidekiqAdmin(ctx *fasthttp.RequestCtx) bool {
	return warpAdmin(ctx)
}

// listJobs handles GET /api/sidekiq/jobs: every active job plus those finished within
// the window (?since=<RFC3339>, default the last 24h), newest first.
func (h *SidekiqHandler) listJobs(ctx *fasthttp.RequestCtx) {
	if h.store == nil || h.runner == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "Background job runner is not available")
		return
	}
	if !sidekiqAdmin(ctx) {
		SendError(ctx, fasthttp.StatusForbidden, "Only administrators can view background jobs")
		return
	}
	since := time.Now().Add(-sidekiqJobsDefaultWindow)
	if raw := strings.TrimSpace(string(ctx.QueryArgs().Peek("since"))); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			SendError(ctx, fasthttp.StatusBadRequest, "since must be an RFC3339 timestamp")
			return
		}
		since = parsed
	}
	rows, err := h.store.ListSidekiqJobs(ctx, since, sidekiqJobsLimit)
	if err != nil {
		logger.Error("failed to list sidekiq jobs: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to list background jobs")
		return
	}
	jobs := make([]sidekiqJobView, 0, len(rows))
	for i := range rows {
		jobs = append(jobs, h.jobView(&rows[i]))
	}
	SendJSON(ctx, sidekiqJobsResponse{Jobs: jobs})
}

func (h *SidekiqHandler) jobView(job *tables.TableSidekiqJob) sidekiqJobView {
	view := sidekiqJobView{
		ID:          job.ID,
		Kind:        job.Kind,
		Status:      job.Status,
		LastError:   job.LastError,
		Cancellable: !tables.IsSidekiqTerminalStatus(job.Status),
		CreatedAt:   job.CreatedAt,
		StartedAt:   job.StartedAt,
		UpdatedAt:   job.UpdatedAt,
		CompletedAt: job.CompletedAt,
	}
	if summary, ok := h.runner.Summarize(job.Kind, job.Metadata); ok {
		view.Progress = &sidekiqJobProgress{Done: summary.Done, Total: summary.Total}
		view.Message = summary.Message
	}
	return view
}

// cancelJob handles POST /api/sidekiq/jobs/{id}/cancel. Work a job already committed
// stands; cancelling stops further work. An unknown or already-finished job is a no-op
// ({"cancelled": false}) rather than an error, so a click racing the last batch is harmless.
func (h *SidekiqHandler) cancelJob(ctx *fasthttp.RequestCtx) {
	if h.store == nil || h.runner == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "Background job runner is not available")
		return
	}
	if !sidekiqAdmin(ctx) {
		SendError(ctx, fasthttp.StatusForbidden, "Only administrators can cancel background jobs")
		return
	}
	id, _ := ctx.UserValue("id").(string)
	id = strings.TrimSpace(id)
	if id == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "job id is required")
		return
	}
	cancelled, err := h.runner.Cancel(ctx, id)
	if err != nil {
		logger.Error("failed to cancel sidekiq job %s: %v", id, err)
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to cancel the job")
		return
	}
	SendJSON(ctx, map[string]bool{"cancelled": cancelled})
}
