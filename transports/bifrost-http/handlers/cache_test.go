package handlers

import (
	"errors"
	"strings"
	"testing"

	"github.com/valyala/fasthttp"
)

// fakeCacheClearer records calls and returns configured errors so the handler
// branches can be exercised without a real semantic cache plugin.
type fakeCacheClearer struct {
	clearByID  func(string) error
	clearByKey func(string) error
	idCalls    []string
	keyCalls   []string
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

// newCacheHandler builds a CacheHandler whose resolver always returns the
// given fake — mimics a steady-state "plugin loaded" environment.
func newCacheHandler(clearer CacheClearer) *CacheHandler {
	return NewCacheHandler(func() CacheClearer { return clearer })
}

// -----------------------------------------------------------------------------
// clearCache (DELETE /api/cache/clear/{cacheId})
// -----------------------------------------------------------------------------

func TestClearCache_OK(t *testing.T) {
	clearer := &fakeCacheClearer{}
	h := newCacheHandler(clearer)

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
	h := newCacheHandler(clearer)

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
	h := newCacheHandler(clearer)

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
	h := newCacheHandler(clearer)

	ctx := newCacheCtx("cacheId", "abc-123")
	h.clearCache(ctx)

	if got := ctx.Response.StatusCode(); got != fasthttp.StatusInternalServerError {
		t.Fatalf("expected 500 on plugin error, got %d", got)
	}
	if !strings.Contains(string(ctx.Response.Body()), "Failed to clear cache") {
		t.Fatalf("expected 'Failed to clear cache' in body, got %s", ctx.Response.Body())
	}
}

// TestClearCache_PluginNotLoaded covers the regression where the handler
// would 405 (route absent) or panic on a nil pointer when the plugin
// wasn't loaded at boot. The new resolver-based handler must return 400.
func TestClearCache_PluginNotLoaded(t *testing.T) {
	h := NewCacheHandler(func() CacheClearer { return nil })

	ctx := newCacheCtx("cacheId", "abc-123")
	h.clearCache(ctx)

	if got := ctx.Response.StatusCode(); got != fasthttp.StatusBadRequest {
		t.Fatalf("expected 400 when plugin not loaded, got %d", got)
	}
	if !strings.Contains(string(ctx.Response.Body()), "semantic_cache plugin is not loaded") {
		t.Fatalf("expected plugin-not-loaded message, got %s", ctx.Response.Body())
	}
}

// -----------------------------------------------------------------------------
// clearCacheByKey (DELETE /api/cache/clear-by-key/{cacheKey})
// -----------------------------------------------------------------------------

func TestClearCacheByKey_OK(t *testing.T) {
	clearer := &fakeCacheClearer{}
	h := newCacheHandler(clearer)

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
	h := newCacheHandler(clearer)

	ctx := newCacheCtx("cacheKey", "session-42")
	h.clearCacheByKey(ctx)

	if got := ctx.Response.StatusCode(); got != fasthttp.StatusInternalServerError {
		t.Fatalf("expected 500 on plugin error, got %d", got)
	}
}

func TestClearCacheByKey_PluginNotLoaded(t *testing.T) {
	h := NewCacheHandler(func() CacheClearer { return nil })

	ctx := newCacheCtx("cacheKey", "session-42")
	h.clearCacheByKey(ctx)

	if got := ctx.Response.StatusCode(); got != fasthttp.StatusBadRequest {
		t.Fatalf("expected 400 when plugin not loaded, got %d", got)
	}
	if !strings.Contains(string(ctx.Response.Body()), "semantic_cache plugin is not loaded") {
		t.Fatalf("expected plugin-not-loaded message, got %s", ctx.Response.Body())
	}
}
