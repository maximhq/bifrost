package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/queryscope"
	"github.com/maximhq/bifrost/framework/sidekiq"
	"github.com/maximhq/bifrost/framework/vectorstore"
	"github.com/maximhq/bifrost/framework/warp"
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

// The agent runs after the handler returns and fasthttp has recycled the
// request. A snapshot that drops the query scope silently widens every tool to
// the whole deployment, so the copy is asserted rather than assumed.
func TestWarpSnapshotCarriesQueryScope(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	applied := false
	scope := queryscope.QueryScope(func(db *gorm.DB) *gorm.DB { applied = true; return db })
	ctx.SetUserValue(schemas.BifrostContextKeyQueryScope, scope)
	ctx.SetUserValue(schemas.BifrostContextKeyUserID, "u-1")

	snapshot, cancel := snapshotWarpContext(ctx, time.Second)
	defer cancel()
	carried := queryscope.FromContext(snapshot)
	require.NotNil(t, carried)
	carried(nil)
	require.True(t, applied, "the snapshot must carry the request's own scope, not a fresh one")
	require.Equal(t, "u-1", snapshot.Value(schemas.BifrostContextKeyUserID))
}
