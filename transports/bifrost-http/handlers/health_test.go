package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/vectorstore"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// fakeVectorStore is a minimal vectorstore.VectorStore stub for exercising the
// health handler's branches without a real backend. pingErr controls Ping;
// poolStats/hasPool controls whether it also implements vectorstore.PoolStatsProvider.
type fakeVectorStore struct {
	pingErr   error
	poolStats vectorstore.PoolStats
	hasPool   bool
}

func (f *fakeVectorStore) Ping(ctx context.Context) error { return f.pingErr }
func (f *fakeVectorStore) CreateNamespace(ctx context.Context, namespace string, dimension int, properties map[string]vectorstore.VectorStoreProperties) error {
	return nil
}
func (f *fakeVectorStore) DeleteNamespace(ctx context.Context, namespace string) error { return nil }
func (f *fakeVectorStore) ListNamespaces(ctx context.Context, prefix string) ([]string, error) {
	return nil, nil
}
func (f *fakeVectorStore) GetChunk(ctx context.Context, namespace string, id string) (vectorstore.SearchResult, error) {
	return vectorstore.SearchResult{}, nil
}
func (f *fakeVectorStore) GetChunks(ctx context.Context, namespace string, ids []string) ([]vectorstore.SearchResult, error) {
	return nil, nil
}
func (f *fakeVectorStore) GetAll(ctx context.Context, namespace string, queries []vectorstore.Query, selectFields []string, cursor *string, limit int64) ([]vectorstore.SearchResult, *string, error) {
	return nil, nil, nil
}
func (f *fakeVectorStore) GetNearest(ctx context.Context, namespace string, vector []float32, queries []vectorstore.Query, selectFields []string, threshold float64, limit int64) ([]vectorstore.SearchResult, error) {
	return nil, nil
}
func (f *fakeVectorStore) RequiresVectors() bool { return false }
func (f *fakeVectorStore) Add(ctx context.Context, namespace string, id string, embedding []float32, metadata map[string]interface{}) error {
	return nil
}
func (f *fakeVectorStore) Delete(ctx context.Context, namespace string, id string) error { return nil }
func (f *fakeVectorStore) DeleteAll(ctx context.Context, namespace string, queries []vectorstore.Query) ([]vectorstore.DeleteResult, error) {
	return nil, nil
}
func (f *fakeVectorStore) Close(ctx context.Context, namespace string) error { return nil }

// fakeVectorStoreWithPool additionally implements vectorstore.PoolStatsProvider,
// mirroring how PgVectorStore reports pgxpool.Stat().
type fakeVectorStoreWithPool struct {
	fakeVectorStore
}

func (f *fakeVectorStoreWithPool) PoolStats() vectorstore.PoolStats { return f.poolStats }

func newHealthTestConfig(store vectorstore.VectorStore, disablePings bool) *lib.Config {
	return &lib.Config{
		ClientConfig: &configstore.ClientConfig{DisableDBPingsInHealth: disablePings},
		VectorStore:  store,
	}
}

func decodeHealthResponse(t *testing.T, ctx *fasthttp.RequestCtx) VectorStoreHealthResponse {
	t.Helper()
	var resp VectorStoreHealthResponse
	if err := json.Unmarshal(ctx.Response.Body(), &resp); err != nil {
		t.Fatalf("failed to decode response body %s: %v", ctx.Response.Body(), err)
	}
	return resp
}

// -----------------------------------------------------------------------------
// getVectorStoreHealth (GET /api/health/vectorstore)
// -----------------------------------------------------------------------------

func TestVectorStoreHealth_Disabled(t *testing.T) {
	h := NewHealthHandler(newHealthTestConfig(nil, false))
	ctx := &fasthttp.RequestCtx{}
	h.getVectorStoreHealth(ctx)

	if got := ctx.Response.StatusCode(); got != fasthttp.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", got, ctx.Response.Body())
	}
	resp := decodeHealthResponse(t, ctx)
	if resp.Status != VectorStoreHealthStatusDisabled {
		t.Fatalf("expected status disabled, got %q", resp.Status)
	}
}

func TestVectorStoreHealth_Healthy(t *testing.T) {
	store := &fakeVectorStoreWithPool{fakeVectorStore{
		poolStats: vectorstore.PoolStats{MaxConns: 10, TotalConns: 3, IdleConns: 2, AcquiredConns: 1},
	}}
	h := NewHealthHandler(newHealthTestConfig(store, false))
	ctx := &fasthttp.RequestCtx{}
	h.getVectorStoreHealth(ctx)

	if got := ctx.Response.StatusCode(); got != fasthttp.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", got, ctx.Response.Body())
	}
	resp := decodeHealthResponse(t, ctx)
	if resp.Status != VectorStoreHealthStatusHealthy {
		t.Fatalf("expected status healthy, got %q: %s", resp.Status, ctx.Response.Body())
	}
	if resp.Type != "fakevectorstorewithpool" {
		t.Fatalf("expected type derived from *fakeVectorStoreWithPool's Go type name, got %q", resp.Type)
	}
	if resp.Pool == nil || resp.Pool.MaxConns != 10 || resp.Pool.AcquiredConns != 1 {
		t.Fatalf("expected pool stats to be reported, got %+v", resp.Pool)
	}
}

func TestVectorStoreHealth_Unhealthy_ReportsRealErrorWithoutLeakingSecrets(t *testing.T) {
	// A realistic pgx connection-refused error embeds host:port, never a password.
	connErr := errors.New("failed to connect to `host=127.0.0.1 port=5999 database=bifrost`: dial error (dial tcp 127.0.0.1:5999: connectex: No connection could be made because the target machine actively refused it.)")
	store := &fakeVectorStore{pingErr: connErr}
	h := NewHealthHandler(newHealthTestConfig(store, false))
	ctx := &fasthttp.RequestCtx{}
	h.getVectorStoreHealth(ctx)

	if got := ctx.Response.StatusCode(); got != fasthttp.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d body=%s", got, ctx.Response.Body())
	}
	resp := decodeHealthResponse(t, ctx)
	if resp.Status != VectorStoreHealthStatusUnhealthy {
		t.Fatalf("expected status unhealthy, got %q", resp.Status)
	}
	if !strings.Contains(resp.Detail, "127.0.0.1:5999") {
		t.Fatalf("expected the real connection error in detail, got %q", resp.Detail)
	}
	if strings.Contains(strings.ToLower(resp.Detail), "password") {
		t.Fatalf("detail must never mention a password, got %q", resp.Detail)
	}
}

func TestVectorStoreHealth_Degraded_WhenPoolExhausted(t *testing.T) {
	store := &fakeVectorStoreWithPool{fakeVectorStore{
		poolStats: vectorstore.PoolStats{MaxConns: 10, TotalConns: 10, IdleConns: 0, AcquiredConns: 10},
	}}
	h := NewHealthHandler(newHealthTestConfig(store, false))
	ctx := &fasthttp.RequestCtx{}
	h.getVectorStoreHealth(ctx)

	if got := ctx.Response.StatusCode(); got != fasthttp.StatusOK {
		t.Fatalf("expected 200 (still reachable) for degraded, got %d body=%s", got, ctx.Response.Body())
	}
	resp := decodeHealthResponse(t, ctx)
	if resp.Status != VectorStoreHealthStatusDegraded {
		t.Fatalf("expected status degraded when pool is saturated, got %q", resp.Status)
	}
	if resp.Detail == "" {
		t.Fatalf("expected a detail explaining the degraded status")
	}
}

func TestVectorStoreHealth_DBPingsDisabled_SkipsPingButReportsPool(t *testing.T) {
	store := &fakeVectorStoreWithPool{fakeVectorStore{
		poolStats: vectorstore.PoolStats{MaxConns: 10, TotalConns: 1, IdleConns: 1, AcquiredConns: 0},
	}}
	h := NewHealthHandler(newHealthTestConfig(store, true))
	ctx := &fasthttp.RequestCtx{}
	h.getVectorStoreHealth(ctx)

	if got := ctx.Response.StatusCode(); got != fasthttp.StatusOK {
		t.Fatalf("expected 200, got %d", got)
	}
	resp := decodeHealthResponse(t, ctx)
	if resp.Status != VectorStoreHealthStatusHealthy {
		t.Fatalf("expected healthy when pings are disabled and pool is not exhausted, got %q", resp.Status)
	}
	if resp.Pool == nil {
		t.Fatalf("expected pool stats even when pings are disabled")
	}
}

func TestVectorStoreTypeName(t *testing.T) {
	name := vectorStoreTypeName(&fakeVectorStore{})
	if name != "fakevector" {
		t.Fatalf("expected 'fakevector' derived from *fakeVectorStore, got %q", name)
	}
}

// TestVectorStoreTypeName_PgVector pins the derived name for the real backend this handler
// was built for. vectorStoreTypeName only inspects the type (reflect.TypeOf), never calls a
// method, so a nil *PgVectorStore is safe to pass in here.
func TestVectorStoreTypeName_PgVector(t *testing.T) {
	var store vectorstore.VectorStore = (*vectorstore.PgVectorStore)(nil)
	if name := vectorStoreTypeName(store); name != "pgvector" {
		t.Fatalf("expected 'pgvector', got %q", name)
	}
}
