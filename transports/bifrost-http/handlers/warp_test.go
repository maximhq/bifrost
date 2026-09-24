package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/queryscope"
	"github.com/maximhq/bifrost/framework/sidekiq"
	"github.com/maximhq/bifrost/framework/vectorstore"
	"github.com/maximhq/bifrost/framework/warp"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
	"gorm.io/gorm"
)

type recordingWarpStore struct {
	row      *tables.TableWarpConfig
	upserted []tables.TableWarpConfig
}

type handlerBackfillReader struct{ warp.LogReader }

func (handlerBackfillReader) Search(_ context.Context, _ *logstore.SearchFilters, pagination *logstore.PaginationOptions) (*logstore.SearchResult, error) {
	return &logstore.SearchResult{Pagination: *pagination}, nil
}

func (handlerBackfillReader) GetLog(context.Context, string) (*logstore.Log, error) { return nil, nil }

type handlerVectorStore struct{}

func (handlerVectorStore) Ping(context.Context) error { return nil }
func (handlerVectorStore) CreateNamespace(context.Context, string, int, map[string]vectorstore.VectorStoreProperties) error {
	return nil
}
func (handlerVectorStore) DeleteNamespace(context.Context, string) error { return nil }
func (handlerVectorStore) ListNamespaces(context.Context, string) ([]string, error) {
	return nil, nil
}
func (handlerVectorStore) GetChunk(context.Context, string, string) (vectorstore.SearchResult, error) {
	return vectorstore.SearchResult{}, nil
}
func (handlerVectorStore) GetChunks(context.Context, string, []string) ([]vectorstore.SearchResult, error) {
	return nil, nil
}
func (handlerVectorStore) GetAll(context.Context, string, []vectorstore.Query, []string, *string, int64) ([]vectorstore.SearchResult, *string, error) {
	return nil, nil, nil
}
func (handlerVectorStore) GetNearest(context.Context, string, []float32, []vectorstore.Query, []string, float64, int64) ([]vectorstore.SearchResult, error) {
	return nil, nil
}
func (handlerVectorStore) RequiresVectors() bool { return true }
func (handlerVectorStore) Add(context.Context, string, string, []float32, map[string]interface{}) error {
	return nil
}
func (handlerVectorStore) Delete(context.Context, string, string) error { return nil }
func (handlerVectorStore) DeleteAll(context.Context, string, []vectorstore.Query) ([]vectorstore.DeleteResult, error) {
	return nil, nil
}
func (handlerVectorStore) Close(context.Context, string) error { return nil }

func newBackfillTestHandler(t *testing.T) (*WarpHandler, *fakeSidekiqStore, func()) {
	t.Helper()
	config := &recordingWarpStore{row: &tables.TableWarpConfig{
		ID: tables.WarpConfigRowID, Enabled: true, Provider: "openai", Model: "gpt-4o",
		EmbeddingProvider: "openai", EmbeddingModel: "embed", EmbeddingDimension: 2, LogVectorStoreNamespace: "WarpLogs",
	}}
	jobs := newFakeSidekiqStore()
	runner := sidekiq.New(jobs, &mockLogger{}, 1, "")
	service := warp.NewService(nil, warp.WithConfigStore(config), warp.WithLogReader(handlerBackfillReader{}), warp.WithVectorStore(handlerVectorStore{}), warp.WithEmbeddingExecutor(func(_ *schemas.BifrostContext, _ *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		return &schemas.BifrostEmbeddingResponse{Data: []schemas.EmbeddingData{{Embedding: schemas.EmbeddingStruct{EmbeddingArray: []float64{0, 1}}}}}, nil
	}))
	service.RegisterBackfill(runner)
	handler := &WarpHandler{service: service, sidekiqRunner: runner, backfillStore: jobs}
	return handler, jobs, func() { runner.Shutdown(); service.Shutdown() }
}

func (s *recordingWarpStore) GetWarpConfig(context.Context) (*tables.TableWarpConfig, error) {
	return s.row, nil
}

func (s *recordingWarpStore) UpsertWarpConfig(_ context.Context, config *tables.TableWarpConfig) error {
	s.upserted = append(s.upserted, *config)
	s.row = config
	return nil
}

func newTestWarpHandler(store *recordingWarpStore) *WarpHandler {
	return &WarpHandler{service: warp.NewService(nil, warp.WithConfigStore(store))}
}

const validWarpConfigJSON = `{"enabled":true,"provider":"openai","model":"gpt-4o","embedding_provider":"openai","embedding_model":"text-embedding-3-small","embedding_dimension":1536,"log_vector_store_namespace":"BifrostWarpLogs"}`

// adminCtx builds a request context that passes the local-admin gate.
func adminCtx(body string) *fasthttp.RequestCtx {
	ctx := &fasthttp.RequestCtx{}
	ctx.SetUserValue(schemas.IsLocalAdminContextKey, true)
	ctx.Request.SetBodyString(body)
	return ctx
}

func TestWarpConfigPutRequiresLocalAdmin(t *testing.T) {
	handler := newTestWarpHandler(&recordingWarpStore{})
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetBodyString(validWarpConfigJSON)
	handler.putConfig(ctx)

	require.Equal(t, fasthttp.StatusForbidden, ctx.Response.StatusCode())
}

func TestWarpConfigPutRejectsMalformedBody(t *testing.T) {
	store := &recordingWarpStore{}
	ctx := adminCtx(`{not json`)
	newTestWarpHandler(store).putConfig(ctx)

	require.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode())
	require.Empty(t, store.upserted)
}

// Validation failures are the service's ErrInvalidConfig family; the handler
// maps them all to 400 and passes the reason through.
func TestWarpConfigPutMapsValidationTo400(t *testing.T) {
	store := &recordingWarpStore{}
	ctx := adminCtx(`{"enabled":true,"provider":"","model":"gpt-4o"}`)
	newTestWarpHandler(store).putConfig(ctx)

	require.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode())
	require.Contains(t, string(ctx.Response.Body()), "provider is required")
	require.Empty(t, store.upserted)
}

// A store with no Warp support is a supported deployment: 503, not 500.
func TestWarpConfigWithoutStoreIs503(t *testing.T) {
	handler := &WarpHandler{service: warp.NewService(nil)}
	ctx := &fasthttp.RequestCtx{}
	handler.getConfig(ctx)
	require.Equal(t, fasthttp.StatusServiceUnavailable, ctx.Response.StatusCode())

	ctx = adminCtx(`{"enabled":false}`)
	handler.putConfig(ctx)
	require.Equal(t, fasthttp.StatusServiceUnavailable, ctx.Response.StatusCode())
}

func TestWarpConfigPutWithoutVectorStoreIs503(t *testing.T) {
	store := &recordingWarpStore{}
	ctx := adminCtx(validWarpConfigJSON)
	newTestWarpHandler(store).putConfig(ctx)
	require.Equal(t, fasthttp.StatusServiceUnavailable, ctx.Response.StatusCode())
	require.Contains(t, string(ctx.Response.Body()), string(schemas.WarpUnavailableNoVectorStore))
}

// The wire shape the settings page depends on: a key reference is a plain
// field, and defaults are resolved rather than sent as zero.
func TestWarpConfigGetBodyShape(t *testing.T) {
	handler := newTestWarpHandler(&recordingWarpStore{row: &tables.TableWarpConfig{
		ID: tables.WarpConfigRowID, Enabled: true, Provider: "openai", Model: "gpt-4o", APIKeyID: "key-abc",
		EmbeddingProvider: "openai", EmbeddingModel: "text-embedding-3-small", EmbeddingDimension: 1536,
		LogVectorStoreNamespace: schemas.WarpDefaultLogVectorStoreNamespace,
	}})
	ctx := &fasthttp.RequestCtx{}
	handler.getConfig(ctx)

	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode())
	var body map[string]any
	require.NoError(t, sonic.Unmarshal(ctx.Response.Body(), &body))
	require.Equal(t, true, body["configured"])
	require.Equal(t, "key-abc", body["api_key_id"])
	require.Equal(t, "text-embedding-3-small", body["embedding_model"])
	require.Equal(t, float64(schemas.WarpDefaultMaxIterations), body["max_iterations"])
	require.NotContains(t, body, "api_key")
}

func TestWarpBackfillAPIsRequireAdmin(t *testing.T) {
	handler, _, cleanup := newBackfillTestHandler(t)
	defer cleanup()
	for _, call := range []func(*fasthttp.RequestCtx){handler.startBackfill, handler.backfillStatus, handler.cancelBackfill} {
		ctx := &fasthttp.RequestCtx{}
		call(ctx)
		require.Equal(t, fasthttp.StatusForbidden, ctx.Response.StatusCode())
	}
}

func TestWarpStartBackfillEnqueuesDurableJob(t *testing.T) {
	handler, jobs, cleanup := newBackfillTestHandler(t)
	defer cleanup()
	ctx := adminCtx(`{"start_time":"2026-09-01T00:00:00Z","end_time":"2026-09-02T00:00:00Z"}`)
	ctx.SetUserValue(schemas.BifrostContextKeyUserID, "admin-1")
	handler.startBackfill(ctx)
	require.Equal(t, fasthttp.StatusAccepted, ctx.Response.StatusCode(), string(ctx.Response.Body()))
	require.Equal(t, 1, jobs.createdCount())
}

func TestWarpStartBackfillReturnsConflictForActiveJob(t *testing.T) {
	handler, jobs, cleanup := newBackfillTestHandler(t)
	defer cleanup()
	jobs.inFlight = &tables.TableSidekiqJob{ID: "running", Kind: warp.BackfillJobKind, Status: tables.SidekiqStatusRunning, Metadata: `{}`}
	ctx := adminCtx(`{"start_time":"2026-09-01T00:00:00Z","end_time":"2026-09-02T00:00:00Z"}`)
	handler.startBackfill(ctx)
	require.Equal(t, fasthttp.StatusConflict, ctx.Response.StatusCode())
	require.Zero(t, jobs.createdCount())
}

func TestWarpBackfillStatusAndCancel(t *testing.T) {
	handler, jobs, cleanup := newBackfillTestHandler(t)
	defer cleanup()
	job := &tables.TableSidekiqJob{ID: "job-1", Kind: warp.BackfillJobKind, Status: tables.SidekiqStatusRunning, Metadata: `{"scanned":4,"indexed":3,"skipped":1}`}
	jobs.jobs[job.ID] = job
	jobs.inFlight = job

	statusCtx := adminCtx("")
	statusCtx.QueryArgs().Set("id", job.ID)
	handler.backfillStatus(statusCtx)
	require.Equal(t, fasthttp.StatusOK, statusCtx.Response.StatusCode())
	require.Contains(t, string(statusCtx.Response.Body()), `"indexed":3`)

	cancelCtx := adminCtx(`{"id":"job-1"}`)
	handler.cancelBackfill(cancelCtx)
	require.Equal(t, fasthttp.StatusOK, cancelCtx.Response.StatusCode())
	require.Contains(t, string(cancelCtx.Response.Body()), tables.SidekiqStatusCancelled)
}

// The tray shows whether semantic search is usable at a glance. This summary
// is readable by anyone who can chat, unlike the backfill controls, and folds
// the vector store connection and the latest indexing job into one state.
func TestWarpLogIndexStatusSummarisesState(t *testing.T) {
	handler, jobs, cleanup := newBackfillTestHandler(t)
	defer cleanup()

	ctx := adminCtx("")
	handler.logIndexStatus(ctx)
	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode())
	body := string(ctx.Response.Body())
	require.Contains(t, body, `"state":"ready"`)
	require.Contains(t, body, `"vector_store_connected":true`)

	jobs.latest = &tables.TableSidekiqJob{ID: "job-f", Kind: warp.BackfillJobKind, Status: tables.SidekiqStatusFailed, LastError: "no keys found", Metadata: `{"scanned":100,"failed":100,"total":5000}`}
	ctx = adminCtx("")
	handler.logIndexStatus(ctx)
	body = string(ctx.Response.Body())
	require.Contains(t, body, `"state":"failed"`)
	require.Contains(t, body, "no keys found")

	jobs.inFlight = &tables.TableSidekiqJob{ID: "job-r", Kind: warp.BackfillJobKind, Status: tables.SidekiqStatusRunning, Metadata: `{"scanned":40,"total":100}`}
	ctx = adminCtx("")
	handler.logIndexStatus(ctx)
	body = string(ctx.Response.Body())
	require.Contains(t, body, `"state":"indexing"`)
	require.Contains(t, body, `"scanned":40`)
	require.Contains(t, body, `"total":100`)
}

// A page reload has no job id in memory and asks for "whatever is current".
// Once a job finishes, nothing is in flight, so without a fallback the last
// outcome, including a failure and its cause, vanishes from the settings page.
func TestWarpBackfillStatusWithoutIDFallsBackToLatestJob(t *testing.T) {
	handler, jobs, cleanup := newBackfillTestHandler(t)
	defer cleanup()
	jobs.latest = &tables.TableSidekiqJob{
		ID: "job-old", Kind: warp.BackfillJobKind, Status: tables.SidekiqStatusFailed,
		LastError: "Warp backfill stopped after 100 consecutive failures: no keys found that support model: openai/embed",
		Metadata:  `{"scanned":100,"failed":100}`,
	}

	statusCtx := adminCtx("")
	handler.backfillStatus(statusCtx)
	require.Equal(t, fasthttp.StatusOK, statusCtx.Response.StatusCode())
	body := string(statusCtx.Response.Body())
	require.Contains(t, body, `"id":"job-old"`)
	require.Contains(t, body, `"status":"failed"`)
	require.Contains(t, body, `"failed":100`)
	require.Contains(t, body, "no keys found")

	// An in-flight job still takes priority over the historical one.
	jobs.inFlight = &tables.TableSidekiqJob{ID: "job-new", Kind: warp.BackfillJobKind, Status: tables.SidekiqStatusRunning, Metadata: `{}`}
	statusCtx = adminCtx("")
	handler.backfillStatus(statusCtx)
	require.Equal(t, fasthttp.StatusOK, statusCtx.Response.StatusCode())
	require.Contains(t, string(statusCtx.Response.Body()), `"id":"job-new"`)
}

func TestWarpBackfillStatusWithoutAnyJobIsIdle(t *testing.T) {
	handler, _, cleanup := newBackfillTestHandler(t)
	defer cleanup()
	statusCtx := adminCtx("")
	handler.backfillStatus(statusCtx)
	require.Equal(t, fasthttp.StatusOK, statusCtx.Response.StatusCode())
	require.Contains(t, string(statusCtx.Response.Body()), `"status":"idle"`)
}

// The agent runs after the handler returns and fasthttp has recycled the
// request. A snapshot that drops the query scope silently widens every tool to
// the whole deployment, so the copy is asserted rather than assumed.
func TestWarpSnapshotCarriesQueryScope(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	applied := false
	scope := queryscope.QueryScope(func(db *gorm.DB) *gorm.DB { applied = true; return db })
	ctx.SetUserValue(schemas.BifrostContextKeyQueryScope, scope)
	ctx.SetUserValue(schemas.BifrostContextKeyUserID, "u-1")

	snapshot, cancel, err := snapshotWarpContext(ctx, time.Second)
	require.NoError(t, err)
	defer cancel()
	carried := queryscope.FromContext(snapshot)
	require.NotNil(t, carried)
	carried(nil)
	require.True(t, applied, "the snapshot must carry the request's own scope, not a fresh one")
	require.Equal(t, "u-1", snapshot.Value(schemas.BifrostContextKeyUserID))
}

// Warp's model calls go through the gateway client in-process, so no HTTP
// transport settles who they are - and governance refuses a request that
// carries no grant. The snapshot must carry a grant settled from the dashboard
// request, attributed to the user who asked, or every chat fails with "request
// carries no grant".
func TestWarpSnapshotCarriesSettledGrant(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	ctx.SetUserValue(schemas.BifrostContextKeyUserID, "u-1")
	ctx.SetUserValue(schemas.BifrostContextKeyUserEmail, "u1@example.com")

	snapshot, cancel, err := snapshotWarpContext(ctx, time.Second)
	require.NoError(t, err)
	defer cancel()
	g := warp.GrantFromContext(snapshot)
	require.NotNil(t, g, "a turn without a grant is refused by governance on its first model call")
	require.NotNil(t, g.Identity(), "the grant's identity must be settled, not left open")
	require.NotNil(t, g.Identity().User())
	require.Equal(t, "u-1", g.Identity().User().ID)

	// A deployment with no auth still settles an identity - "nobody" - which is
	// distinct from never having been settled.
	anonymous, cancelAnonymous, err := snapshotWarpContext(&fasthttp.RequestCtx{}, time.Second)
	require.NoError(t, err)
	defer cancelAnonymous()
	require.NotNil(t, warp.GrantFromContext(anonymous))
	require.NotNil(t, warp.GrantFromContext(anonymous).Identity())
}

// A virtual key arrives as a request header, never as a user value, so the
// grant must read it from the headers the way lib.ConvertToBifrostContext does
// - otherwise a dashboard request that presents a key settles as the session
// alone and governance never sees the key.
func TestWarpSnapshotGrantCarriesHeaderVirtualKey(t *testing.T) {
	for name, set := range map[string]func(*fasthttp.RequestHeader){
		"x-bf-vk":              func(h *fasthttp.RequestHeader) { h.Set("x-bf-vk", "sk-bf-abc") },
		"authorization bearer": func(h *fasthttp.RequestHeader) { h.Set("Authorization", "Bearer sk-bf-abc") },
		"x-api-key":            func(h *fasthttp.RequestHeader) { h.Set("x-api-key", "sk-bf-abc") },
	} {
		t.Run(name, func(t *testing.T) {
			ctx := &fasthttp.RequestCtx{}
			set(&ctx.Request.Header)
			ctx.SetUserValue(schemas.BifrostContextKeyUserID, "u-1")

			snapshot, cancel, err := snapshotWarpContext(ctx, time.Second)
			require.NoError(t, err)
			defer cancel()
			identity := warp.GrantFromContext(snapshot).Identity()
			require.NotNil(t, identity)
			require.Equal(t, "sk-bf-abc", identity.Credential().Value)
			require.Equal(t, "u-1", identity.User().ID, "the session's user is kept alongside the key")
		})
	}

	// A bearer that is not a virtual key (a dashboard session token) is not one.
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("Authorization", "Bearer session-token")
	snapshot, cancel, err := snapshotWarpContext(ctx, time.Second)
	require.NoError(t, err)
	defer cancel()
	require.Empty(t, warp.GrantFromContext(snapshot).Identity().Credential().Value)
}

// A scope that was set on the request but cannot be carried over is the
// dangerous case: queryscope.FromContext reads a missing scope as "no
// restriction", so the agent would run unscoped over every tenant's prompts and
// costs with no error and no log line. That must fail closed.
//
// An absent scope is different and must keep working: the key is set by the
// enterprise wrapper, so an OSS deployment legitimately has none, and failing
// closed there would disable Warp entirely.
func TestWarpSnapshotFailsClosedOnUnusableScope(t *testing.T) {
	t.Run("absent scope is allowed", func(t *testing.T) {
		ctx := &fasthttp.RequestCtx{}
		snapshot, cancel, err := snapshotWarpContext(ctx, time.Second)
		require.NoError(t, err, "an OSS deployment has no scope wrapper and must still work")
		defer cancel()
		require.Nil(t, queryscope.FromContext(snapshot))
	})

	t.Run("wrong type fails closed", func(t *testing.T) {
		ctx := &fasthttp.RequestCtx{}
		ctx.SetUserValue(schemas.BifrostContextKeyQueryScope, "not-a-scope")
		_, cancel, err := snapshotWarpContext(ctx, time.Second)
		if cancel != nil {
			defer cancel()
		}
		require.Error(t, err, "a scope that cannot be carried must not degrade to unrestricted")
	})

	t.Run("nil scope of the right type fails closed", func(t *testing.T) {
		ctx := &fasthttp.RequestCtx{}
		ctx.SetUserValue(schemas.BifrostContextKeyQueryScope, queryscope.QueryScope(nil))
		_, cancel, err := snapshotWarpContext(ctx, time.Second)
		if cancel != nil {
			defer cancel()
		}
		require.Error(t, err)
	})
}

// The chat route must exist whether or not Warp can currently answer.
//
// RegisterRoutes runs once, at startup. Gating the route on CanChat() meant a
// deployment that enabled logging afterwards - through /api/plugins, which
// reloads plugins without re-registering routes - kept returning 405 for the
// rest of the process's life, with nothing to indicate the feature had become
// available. A registered route that answers 503 carries a machine-readable
// reason the dashboard already branches on, so "present but unusable" and
// "absent" stay distinguishable without making the state permanent.
func TestWarpChatRouteIsRegisteredWithoutALogReader(t *testing.T) {
	handler := newTestWarpHandler(&recordingWarpStore{})
	require.False(t, handler.service.CanChat(), "precondition: no log reader, so Warp cannot answer")

	router := router.New()
	handler.RegisterRoutes(router)

	ctx := adminCtx(`{"messages":[{"role":"user","content":"how much did we spend?"}]}`)
	ctx.Request.Header.SetMethod("POST")
	ctx.Request.SetRequestURI("/api/warp/chat")
	router.Handler(ctx)

	require.Equal(t, fasthttp.StatusServiceUnavailable, ctx.Response.StatusCode(),
		"the route must answer 503, not 405")

	var body schemas.WarpUnavailableResponse
	require.NoError(t, sonic.Unmarshal(ctx.Response.Body(), &body))
	require.Equal(t, schemas.WarpUnavailableNoLogStore, body.Reason,
		"the dashboard hides the launcher on this reason, so it must be reported accurately")
}

// A heartbeat has to be a complete SSE block, not a bare comment line.
//
// SendEvent terminates every Warp frame with "\n\n", and the browser splitter
// looks for exactly that boundary. A heartbeat ending in a single "\n" has no
// boundary, so the client holds it in its carry buffer until the next real
// event arrives - on an idle turn the keep-alive that exists to prove the
// connection is alive is the one thing the reader cannot see.
func TestWarpHeartbeatIsADelimitedSSEBlock(t *testing.T) {
	reader := lib.NewSSEStreamReader()
	go func() {
		warpHeartbeat(reader)()
		reader.Done()
	}()

	buf := make([]byte, 4096)
	n, err := reader.Read(buf)
	require.NoError(t, err)
	require.Equal(t, ": heartbeat\n\n", string(buf[:n]),
		"the heartbeat must carry its own frame boundary")
}

// Both endpoints take an explicit job id and looked it up without checking what
// kind of job came back. The Warp cancel endpoint could therefore cancel any
// pending or running sidekiq job in the deployment - a pricing sync, a
// governance reset - and the status endpoint would hand back that job's
// metadata to a caller asking about a backfill.
func TestWarpBackfillEndpointsRejectForeignJobs(t *testing.T) {
	handler, jobs, cleanup := newBackfillTestHandler(t)
	defer cleanup()

	foreign := &tables.TableSidekiqJob{
		ID: "pricing-sync-1", Kind: "pricing_sync",
		Status: tables.SidekiqStatusRunning, Metadata: `{"scanned":99,"indexed":98}`,
	}
	jobs.jobs[foreign.ID] = foreign

	statusCtx := adminCtx("")
	statusCtx.QueryArgs().Set("id", foreign.ID)
	handler.backfillStatus(statusCtx)
	require.Equal(t, fasthttp.StatusNotFound, statusCtx.Response.StatusCode(),
		"a job of another kind is not a Warp backfill and must not be described as one")
	require.NotContains(t, string(statusCtx.Response.Body()), `"indexed":98`,
		"another job's metadata must not leak through this endpoint")

	cancelCtx := adminCtx(`{"id":"pricing-sync-1"}`)
	handler.cancelBackfill(cancelCtx)
	require.Equal(t, fasthttp.StatusNotFound, cancelCtx.Response.StatusCode(),
		"the Warp endpoint must not be able to cancel an unrelated job")
	require.Equal(t, tables.SidekiqStatusRunning, jobs.jobs[foreign.ID].Status,
		"the foreign job must still be running")
}

// Cancel writes the terminal status itself, so once it succeeds the job is
// cancelled whatever the re-read does. Returning the pre-cancel row when that
// read fails reports "running" for a job that has already been stopped.
func TestWarpCancelBackfillReportsCancelledWhenRereadFails(t *testing.T) {
	handler, jobs, cleanup := newBackfillTestHandler(t)
	defer cleanup()
	job := &tables.TableSidekiqJob{ID: "job-1", Kind: warp.BackfillJobKind, Status: tables.SidekiqStatusRunning, Metadata: `{}`}
	jobs.jobs[job.ID] = job
	jobs.inFlight = job
	jobs.failGetAfterCancel = true

	ctx := adminCtx(`{"id":"job-1"}`)
	handler.cancelBackfill(ctx)

	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode())
	body := string(ctx.Response.Body())
	require.Contains(t, body, tables.SidekiqStatusCancelled,
		"the cancel succeeded, so the response must not say the job is still running")
	require.NotContains(t, body, `"status":"running"`)
}

// Runner.Cancel returns false for a job that had already finished. Reporting
// "cancelled" there claims an outcome this request did not produce - the job may
// well have completed successfully - so the fallback must be conditional on
// having actually cancelled something.
func TestWarpCancelBackfillDoesNotClaimCancellingATerminalJob(t *testing.T) {
	handler, jobs, cleanup := newBackfillTestHandler(t)
	defer cleanup()
	job := &tables.TableSidekiqJob{ID: "job-1", Kind: warp.BackfillJobKind, Status: tables.SidekiqStatusCompleted, Metadata: `{}`}
	jobs.jobs[job.ID] = job
	// The re-read fails as well, which is the only way the fallback is reached.
	jobs.failGetAfterCancel = true

	ctx := adminCtx(`{"id":"job-1"}`)
	handler.cancelBackfill(ctx)

	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode())
	body := string(ctx.Response.Body())
	require.NotContains(t, body, tables.SidekiqStatusCancelled,
		"nothing was cancelled, so the response must not say it was")
	require.Contains(t, body, tables.SidekiqStatusCompleted,
		"the row we already hold is the most honest thing available")
}

// An absent timestamp must be absent from the JSON, not sent as year 1.
//
// omitempty does not omit a zero time.Time - it is a struct, never "empty" to
// the encoder - so the idle and pending responses shipped 0001-01-01 for
// start_time, end_time and created_at. Those are optional properties in the
// schema, and a client reading them gets a date that looks real and is not.
func TestWarpBackfillStatusOmitsAbsentTimestamps(t *testing.T) {
	for name, status := range map[string]warpBackfillStatus{
		"idle":    {Status: "idle"},
		"pending": {ID: "job-1", Status: tables.SidekiqStatusPending},
	} {
		encoded, err := sonic.Marshal(status)
		require.NoError(t, err)

		var shape map[string]any
		require.NoError(t, sonic.Unmarshal(encoded, &shape))
		for _, field := range []string{"start_time", "end_time", "created_at"} {
			require.NotContains(t, shape, field, "%s: %s has no value and must not be sent", name, field)
		}
		require.NotContains(t, string(encoded), "0001-01-01", name)
	}
}

// A real timestamp still has to travel.
func TestWarpBackfillStatusKeepsRealTimestamps(t *testing.T) {
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	encoded, err := sonic.Marshal(warpBackfillStatus{
		ID: "job-1", Status: "running", StartTime: &at, EndTime: &at, CreatedAt: &at,
	})
	require.NoError(t, err)
	var shape map[string]any
	require.NoError(t, sonic.Unmarshal(encoded, &shape))
	for _, field := range []string{"start_time", "end_time", "created_at"} {
		require.Contains(t, shape, field)
	}
}

// The job's embedding spend travels from its checkpoint to the status payload
// the backfill panel renders; a cost the deployment cannot price stays absent
// rather than arriving as 0, which would read as free.
func TestWarpBackfillStatusCarriesEmbeddingSpend(t *testing.T) {
	priced := warpBackfillStatusFromRow(&tables.TableSidekiqJob{
		ID: "job-1", Kind: warp.BackfillJobKind, Status: tables.SidekiqStatusRunning,
		Metadata: `{"total":10,"scanned":4,"embedding_tokens":1200,"embedding_cost":0.000024}`,
	})
	require.Equal(t, int64(1200), priced.EmbeddingTokens)
	require.NotNil(t, priced.EmbeddingCost)
	require.InDelta(t, 0.000024, *priced.EmbeddingCost, 1e-12)

	unpriced := warpBackfillStatusFromRow(&tables.TableSidekiqJob{
		ID: "job-2", Kind: warp.BackfillJobKind, Status: tables.SidekiqStatusRunning,
		Metadata: `{"total":10,"scanned":4,"embedding_tokens":1200}`,
	})
	encoded, err := sonic.Marshal(unpriced)
	require.NoError(t, err)
	var shape map[string]any
	require.NoError(t, sonic.Unmarshal(encoded, &shape))
	require.Equal(t, float64(1200), shape["embedding_tokens"])
	require.NotContains(t, shape, "embedding_cost")
}

// A malformed time range is a bad request whether or not a job is running.
//
// startBackfill checked for an active job before BuildBackfillJobMeta, which is
// what rejects start >= end - so with a backfill in flight an inverted range
// came back 409 with the running job's status. The caller then debugs a
// conflict it does not have instead of the range it got wrong.
func TestWarpStartBackfillValidatesRangeBeforeConflict(t *testing.T) {
	handler, jobs, cleanup := newBackfillTestHandler(t)
	defer cleanup()

	running := &tables.TableSidekiqJob{
		ID: "warp-backfill-1", Kind: warp.BackfillJobKind,
		Status: tables.SidekiqStatusRunning, Metadata: `{}`,
	}
	jobs.jobs[running.ID] = running
	// The conflict check reads inFlight, not the map.
	jobs.inFlight = running

	// end before start, with that job in flight.
	ctx := adminCtx(`{"start_time":"2026-09-02T00:00:00Z","end_time":"2026-09-01T00:00:00Z"}`)
	handler.startBackfill(ctx)
	require.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode(),
		"the range is wrong regardless of what else is running")

	// With no job running the same range must still be rejected the same way.
	jobs.inFlight = nil
	delete(jobs.jobs, running.ID)
	ctx = adminCtx(`{"start_time":"2026-09-02T00:00:00Z","end_time":"2026-09-01T00:00:00Z"}`)
	handler.startBackfill(ctx)
	require.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode())
}

// The tray's index status has no admin gate, unlike the backfill endpoints, and
// LastError carries whatever the provider or vector store said - endpoints,
// model names, internal detail. An administrator debugging a stalled index
// needs that text; everyone else needs the state and the counts.
func TestWarpLogIndexStatusHidesProviderErrorFromNonAdmins(t *testing.T) {
	handler, jobs, cleanup := newBackfillTestHandler(t)
	defer cleanup()
	jobs.latest = &tables.TableSidekiqJob{
		ID: "job-f", Kind: warp.BackfillJobKind, Status: tables.SidekiqStatusFailed,
		LastError: "dial tcp 10.0.3.7:6333: connect: connection refused",
		Metadata:  `{"scanned":100,"failed":100,"total":5000}`,
	}

	// No local-admin marker on the context.
	ctx := &fasthttp.RequestCtx{}
	handler.logIndexStatus(ctx)
	body := string(ctx.Response.Body())
	require.Contains(t, body, `"state":"failed"`, "the state itself is not sensitive")
	require.Contains(t, body, `"failed":100`, "nor are the counts")
	require.NotContains(t, body, "10.0.3.7", "the provider's own error text must not reach a non-admin")
	require.NotContains(t, body, "connection refused")

	// An administrator still gets it, which is the point of keeping it at all.
	adminRequest := adminCtx("")
	handler.logIndexStatus(adminRequest)
	require.Contains(t, string(adminRequest.Response.Body()), "connection refused")
}

// rbacAdminCtx builds a request context the way enterprise's RBAC middleware
// leaves it for an SSO caller it authorized: a user and role ID, and no
// local-admin marker.
func rbacAdminCtx(body string) *fasthttp.RequestCtx {
	ctx := &fasthttp.RequestCtx{}
	ctx.SetUserValue(schemas.BifrostContextKeyUserID, "sso-admin")
	ctx.SetUserValue(schemas.BifrostContextKeyUserRoleID, uint(1))
	ctx.Request.SetBodyString(body)
	return ctx
}

// An SSO admin never carries the local-admin marker, because enterprise turns the
// password login off once SSO is on. Enterprise RBAC has already checked the
// Warp permission by the time these handlers run, so its role ID has to be
// enough, or no SSO user can ever configure Warp or run a backfill.
func TestWarpAdminRoutesAdmitRBACAuthorizedCaller(t *testing.T) {
	store := &recordingWarpStore{}
	putCtx := rbacAdminCtx(validWarpConfigJSON)
	(&WarpHandler{service: warp.NewService(nil, warp.WithConfigStore(store), warp.WithVectorStore(handlerVectorStore{}))}).putConfig(putCtx)
	require.NotEqual(t, fasthttp.StatusForbidden, putCtx.Response.StatusCode(), string(putCtx.Response.Body()))

	handler, jobs, cleanup := newBackfillTestHandler(t)
	defer cleanup()

	startCtx := rbacAdminCtx(`{"start_time":"2026-09-01T00:00:00Z","end_time":"2026-09-02T00:00:00Z"}`)
	handler.startBackfill(startCtx)
	require.Equal(t, fasthttp.StatusAccepted, startCtx.Response.StatusCode(), string(startCtx.Response.Body()))
	require.Equal(t, 1, jobs.createdCount())

	job := &tables.TableSidekiqJob{ID: "job-1", Kind: warp.BackfillJobKind, Status: tables.SidekiqStatusRunning, Metadata: `{}`}
	jobs.jobs[job.ID] = job
	jobs.inFlight = job

	statusCtx := rbacAdminCtx("")
	statusCtx.QueryArgs().Set("id", job.ID)
	handler.backfillStatus(statusCtx)
	require.Equal(t, fasthttp.StatusOK, statusCtx.Response.StatusCode(), string(statusCtx.Response.Body()))

	cancelCtx := rbacAdminCtx(`{"id":"job-1"}`)
	handler.cancelBackfill(cancelCtx)
	require.Equal(t, fasthttp.StatusOK, cancelCtx.Response.StatusCode(), string(cancelCtx.Response.Body()))
}

// A zero role ID is what an unhydrated context carries. It must not count as
// authorization.
func TestWarpAdminRoutesRejectZeroRoleID(t *testing.T) {
	handler, _, cleanup := newBackfillTestHandler(t)
	defer cleanup()
	calls := []func(*fasthttp.RequestCtx){handler.startBackfill, handler.backfillStatus, handler.cancelBackfill, newTestWarpHandler(&recordingWarpStore{}).putConfig}
	for _, call := range calls {
		ctx := &fasthttp.RequestCtx{}
		ctx.SetUserValue(schemas.BifrostContextKeyUserID, "someone")
		ctx.SetUserValue(schemas.BifrostContextKeyUserRoleID, uint(0))
		call(ctx)
		require.Equal(t, fasthttp.StatusForbidden, ctx.Response.StatusCode())
	}
}

// The index summary only needs Warp View in enterprise, so a role ID on its
// context says nothing about admin. The provider's error text stays local-admin
// only there.
func TestWarpLogIndexStatusHidesProviderErrorFromRBACCaller(t *testing.T) {
	handler, jobs, cleanup := newBackfillTestHandler(t)
	defer cleanup()
	jobs.latest = &tables.TableSidekiqJob{
		ID: "job-f", Kind: warp.BackfillJobKind, Status: tables.SidekiqStatusFailed,
		LastError: "dial tcp 10.0.3.7:6333: connect: connection refused",
		Metadata:  `{"scanned":100,"failed":100,"total":5000}`,
	}
	ctx := rbacAdminCtx("")
	handler.logIndexStatus(ctx)
	require.NotContains(t, string(ctx.Response.Body()), "connection refused")
}

func TestWarpJobMatchesNamespace(t *testing.T) {
	job := func(metadata string) *tables.TableSidekiqJob {
		return &tables.TableSidekiqJob{Metadata: metadata}
	}
	require.True(t, warpJobMatchesNamespace(job(`{"namespace":"warp-logs"}`), "warp-logs"))
	require.False(t, warpJobMatchesNamespace(job(`{"namespace":"warp-logs"}`), "warp-logs-v2"),
		"a run against the previous embedding space says nothing about the current one")
	// Pre-upgrade jobs carry no namespace at all. Blanking the tray for an index
	// that was legitimately built is worse than trusting them.
	require.True(t, warpJobMatchesNamespace(job(`{}`), "warp-logs"))
	require.True(t, warpJobMatchesNamespace(job(`{"namespace":"  "}`), "warp-logs"))
	require.False(t, warpJobMatchesNamespace(job(`{ not json`), "warp-logs"),
		"metadata we cannot read is not evidence about the current index")
}

// While the Warp feature flag is off every route answers 404, and switching it
// on takes effect on the next request - routes are registered once at startup,
// so the flag has to be read per request rather than at registration.
func TestWarpRoutesAre404WhileFeatureFlagIsOff(t *testing.T) {
	handler := newTestWarpHandler(&recordingWarpStore{})
	enabled := false
	handler.enabled = func() bool { return enabled }

	r := router.New()
	handler.RegisterRoutes(r)

	serve := func(method, uri, body string) *fasthttp.RequestCtx {
		ctx := adminCtx(body)
		ctx.Request.Header.SetMethod(method)
		ctx.Request.SetRequestURI(uri)
		r.Handler(ctx)
		return ctx
	}

	for _, route := range []struct{ method, uri, body string }{
		{"GET", "/api/warp/config", ""},
		{"PUT", "/api/warp/config", validWarpConfigJSON},
		{"POST", "/api/warp/chat", `{"messages":[{"role":"user","content":"hi"}]}`},
		{"GET", "/api/warp/log-index/status", ""},
		{"POST", "/api/warp/log-index/backfill", `{}`},
		{"GET", "/api/warp/log-index/backfill/status", ""},
		{"POST", "/api/warp/log-index/backfill/cancel", `{}`},
	} {
		ctx := serve(route.method, route.uri, route.body)
		require.Equal(t, fasthttp.StatusNotFound, ctx.Response.StatusCode(), "%s %s must be hidden while the flag is off", route.method, route.uri)
	}

	enabled = true
	ctx := serve("POST", "/api/warp/chat", `{"messages":[{"role":"user","content":"hi"}]}`)
	require.Equal(t, fasthttp.StatusServiceUnavailable, ctx.Response.StatusCode(),
		"with the flag on the request must reach the handler, which reports the missing log store")
}
