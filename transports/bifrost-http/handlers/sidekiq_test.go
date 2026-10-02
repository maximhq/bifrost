package handlers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/sidekiq"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

type fakeSidekiqJobsStore struct {
	jobs  []tables.TableSidekiqJob
	err   error
	since time.Time
	limit int
}

func (f *fakeSidekiqJobsStore) ListSidekiqJobs(_ context.Context, since time.Time, limit int) ([]tables.TableSidekiqJob, error) {
	f.since, f.limit = since, limit
	return f.jobs, f.err
}

type fakeSidekiqJobsRunner struct {
	summaries map[string]sidekiq.JobSummary
	cancelled bool
	err       error
	cancelID  string
}

func (f *fakeSidekiqJobsRunner) Cancel(_ context.Context, id string) (bool, error) {
	f.cancelID = id
	return f.cancelled, f.err
}

func (f *fakeSidekiqJobsRunner) Summarize(kind, _ string) (sidekiq.JobSummary, bool) {
	s, ok := f.summaries[kind]
	return s, ok
}

func sidekiqCtx(admin bool) *fasthttp.RequestCtx {
	SetLogger(&mockLogger{})
	ctx := &fasthttp.RequestCtx{}
	if admin {
		ctx.SetUserValue(schemas.IsLocalAdminContextKey, true)
	}
	return ctx
}

func TestSidekiqListJobsMapsProgressAndCancellable(t *testing.T) {
	now := time.Now()
	store := &fakeSidekiqJobsStore{jobs: []tables.TableSidekiqJob{
		{ID: "a", Kind: "counted", Status: tables.SidekiqStatusRunning, CreatedAt: now, UpdatedAt: now},
		{ID: "b", Kind: "plain", Status: tables.SidekiqStatusCompleted, CreatedAt: now, UpdatedAt: now, CompletedAt: &now, LastError: ""},
		{ID: "c", Kind: "plain", Status: tables.SidekiqStatusFailed, CreatedAt: now, UpdatedAt: now, LastError: "boom"},
	}}
	runner := &fakeSidekiqJobsRunner{summaries: map[string]sidekiq.JobSummary{"counted": {Done: 5, Total: 20, Message: "half-ish"}}}

	ctx := sidekiqCtx(true)
	NewSidekiqHandler(store, runner).listJobs(ctx)

	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))
	var resp sidekiqJobsResponse
	require.NoError(t, sonic.Unmarshal(ctx.Response.Body(), &resp))
	require.Len(t, resp.Jobs, 3)

	require.NotNil(t, resp.Jobs[0].Progress)
	require.Equal(t, sidekiqJobProgress{Done: 5, Total: 20}, *resp.Jobs[0].Progress)
	require.Equal(t, "half-ish", resp.Jobs[0].Message)
	require.True(t, resp.Jobs[0].Cancellable)

	require.Nil(t, resp.Jobs[1].Progress, "kinds without a summarizer report no progress")
	require.False(t, resp.Jobs[1].Cancellable)
	require.Equal(t, "boom", resp.Jobs[2].LastError)
	require.False(t, resp.Jobs[2].Cancellable)

	require.Equal(t, sidekiqJobsLimit, store.limit)
	require.WithinDuration(t, now.Add(-sidekiqJobsDefaultWindow), store.since, time.Minute)
}

func TestSidekiqListJobsSinceParam(t *testing.T) {
	store := &fakeSidekiqJobsStore{}
	h := NewSidekiqHandler(store, &fakeSidekiqJobsRunner{})

	ctx := sidekiqCtx(true)
	ctx.QueryArgs().Set("since", "2026-01-02T03:04:05Z")
	h.listJobs(ctx)
	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode())
	require.True(t, store.since.Equal(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)))

	bad := sidekiqCtx(true)
	bad.QueryArgs().Set("since", "yesterday")
	h.listJobs(bad)
	require.Equal(t, fasthttp.StatusBadRequest, bad.Response.StatusCode())
}

func TestSidekiqListJobsRequiresAdmin(t *testing.T) {
	ctx := sidekiqCtx(false)
	NewSidekiqHandler(&fakeSidekiqJobsStore{}, &fakeSidekiqJobsRunner{}).listJobs(ctx)
	require.Equal(t, fasthttp.StatusForbidden, ctx.Response.StatusCode())
}

func TestSidekiqListJobsUnavailableAndStoreError(t *testing.T) {
	ctx := sidekiqCtx(true)
	NewSidekiqHandler(nil, nil).listJobs(ctx)
	require.Equal(t, fasthttp.StatusServiceUnavailable, ctx.Response.StatusCode())

	ctx = sidekiqCtx(true)
	NewSidekiqHandler(&fakeSidekiqJobsStore{err: errors.New("db down")}, &fakeSidekiqJobsRunner{}).listJobs(ctx)
	require.Equal(t, fasthttp.StatusInternalServerError, ctx.Response.StatusCode())
}

func TestSidekiqCancelJob(t *testing.T) {
	t.Run("cancels and reports the transition", func(t *testing.T) {
		runner := &fakeSidekiqJobsRunner{cancelled: true}
		ctx := sidekiqCtx(true)
		ctx.SetUserValue("id", "job-1")
		NewSidekiqHandler(&fakeSidekiqJobsStore{}, runner).cancelJob(ctx)

		require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode())
		require.Equal(t, "job-1", runner.cancelID)
		require.JSONEq(t, `{"cancelled":true}`, string(ctx.Response.Body()))
	})

	t.Run("unknown or finished job is a no-op, not an error", func(t *testing.T) {
		ctx := sidekiqCtx(true)
		ctx.SetUserValue("id", "gone")
		NewSidekiqHandler(&fakeSidekiqJobsStore{}, &fakeSidekiqJobsRunner{cancelled: false}).cancelJob(ctx)

		require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode())
		require.JSONEq(t, `{"cancelled":false}`, string(ctx.Response.Body()))
	})

	t.Run("non-admin is rejected before the runner is touched", func(t *testing.T) {
		runner := &fakeSidekiqJobsRunner{cancelled: true}
		ctx := sidekiqCtx(false)
		ctx.SetUserValue("id", "job-1")
		NewSidekiqHandler(&fakeSidekiqJobsStore{}, runner).cancelJob(ctx)

		require.Equal(t, fasthttp.StatusForbidden, ctx.Response.StatusCode())
		require.Empty(t, runner.cancelID)
	})

	t.Run("missing id is a bad request", func(t *testing.T) {
		ctx := sidekiqCtx(true)
		NewSidekiqHandler(&fakeSidekiqJobsStore{}, &fakeSidekiqJobsRunner{}).cancelJob(ctx)
		require.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode())
	})

	t.Run("runner error is a 500", func(t *testing.T) {
		ctx := sidekiqCtx(true)
		ctx.SetUserValue("id", "job-1")
		NewSidekiqHandler(&fakeSidekiqJobsStore{}, &fakeSidekiqJobsRunner{err: errors.New("db down")}).cancelJob(ctx)
		require.Equal(t, fasthttp.StatusInternalServerError, ctx.Response.StatusCode())
	})
}
