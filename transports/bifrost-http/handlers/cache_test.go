package handlers

import (
	"errors"
	"strings"
	"testing"

	"github.com/valyala/fasthttp"
)

// fakeCacheClearer records calls and returns configured errors so the handler
// branches can be exercised without a real local cache plugin.
type fakeCacheClearer struct {
	clearByID    func(string) error
	clearByKey   func(string) error
	idCalls      []string
	keyCalls     []string
}

func (f *fakeCacheClearer) ClearCacheForCacheID(id string) error {
	f.idCalls = append(f.idCalls, id)
	if f.clearByID != nil {
		return f.clearByID(id)
	}
	return nil
}

func (f *fakeCacheClearer) ClearCacheForKey(key string) error {
	f.keyCalls = append(f.keyCalls, key)
	if f.clearByKey != nil {
		return f.clearByKey(key)
	}
	return nil
}

func newCacheCtx(userKey, userVal string) *fasthttp.RequestCtx {
	ctx := &fasthttp.RequestCtx{}
	if userKey != "" {
		ctx.SetUserValue(userKey, userVal)
	}
	return ctx
}

// -----------------------------------------------------------------------------
// clearCache (DELETE /api/cache/clear/{cacheId})
// -----------------------------------------------------------------------------

func TestClearCache_OK(t *testing.T) {
	clearer := &fakeCacheClearer{}
	h := &CacheHandler{plugin: clearer}

	ctx := newCacheCtx("cacheId", "abc-123")
	h.clearCache(ctx)

	if got := ctx.Response.StatusCode(); got != fasthttp.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", got, ctx.Response.Body())
	}
	if len(clearer.idCalls) != 1 || clearer.idCalls[0] != "abc-123" {
		t.Fatalf("expected ClearCacheForCacheID('abc-123'), got %v", clearer.idCalls)
	}
}

func TestClearCache_RejectsEmptyID(t *testing.T) {
	clearer := &fakeCacheClearer{}
	h := &CacheHandler{plugin: clearer}

	ctx := newCacheCtx("cacheId", "")
	h.clearCache(ctx)

	if got := ctx.Response.StatusCode(); got != fasthttp.StatusBadRequest {
		t.Fatalf("expected 400 for empty id, got %d", got)
	}
	if len(clearer.idCalls) != 0 {
		t.Fatalf("expected no Clear calls on bad id, got %v", clearer.idCalls)
	}
}

func TestClearCache_MissingUserValue(t *testing.T) {
	clearer := &fakeCacheClearer{}
	h := &CacheHandler{plugin: clearer}

	// No user value set at all (simulates a routing misconfiguration).
	ctx := &fasthttp.RequestCtx{}
	h.clearCache(ctx)

	if got := ctx.Response.StatusCode(); got != fasthttp.StatusBadRequest {
		t.Fatalf("expected 400 when cacheId user value missing, got %d", got)
	}
}

func TestClearCache_PluginErrorReturns500(t *testing.T) {
	clearer := &fakeCacheClearer{
		clearByID: func(string) error { return errors.New("store unavailable") },
	}
	h := &CacheHandler{plugin: clearer}

	ctx := newCacheCtx("cacheId", "abc-123")
	h.clearCache(ctx)

	if got := ctx.Response.StatusCode(); got != fasthttp.StatusInternalServerError {
		t.Fatalf("expected 500 on plugin error, got %d", got)
	}
	if !strings.Contains(string(ctx.Response.Body()), "Failed to clear cache") {
		t.Fatalf("expected 'Failed to clear cache' in body, got %s", ctx.Response.Body())
	}
}

// -----------------------------------------------------------------------------
// clearCacheByKey (DELETE /api/cache/clear-by-key/{cacheKey})
// -----------------------------------------------------------------------------

func TestClearCacheByKey_OK(t *testing.T) {
	clearer := &fakeCacheClearer{}
	h := &CacheHandler{plugin: clearer}

	ctx := newCacheCtx("cacheKey", "session-42")
	h.clearCacheByKey(ctx)

	if got := ctx.Response.StatusCode(); got != fasthttp.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", got, ctx.Response.Body())
	}
	if len(clearer.keyCalls) != 1 || clearer.keyCalls[0] != "session-42" {
		t.Fatalf("expected ClearCacheForKey('session-42'), got %v", clearer.keyCalls)
	}
}

func TestClearCacheByKey_PluginErrorReturns500(t *testing.T) {
	clearer := &fakeCacheClearer{
		clearByKey: func(string) error { return errors.New("vector store down") },
	}
	h := &CacheHandler{plugin: clearer}

	ctx := newCacheCtx("cacheKey", "session-42")
	h.clearCacheByKey(ctx)

	if got := ctx.Response.StatusCode(); got != fasthttp.StatusInternalServerError {
		t.Fatalf("expected 500 on plugin error, got %d", got)
	}
}
