package handlers

import (
	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// CacheClearer is the minimal contract the handler needs from the semantic
// cache plugin. Exported so the server wiring can supply a resolver without
// pulling in the plugin's concrete type and so tests can substitute a fake.
type CacheClearer interface {
	ClearCacheForCacheID(cacheID string) error
	ClearCacheForKey(cacheKey string) error
}

// CacheClearerResolver returns the currently-loaded cache plugin or nil if
// none is loaded. Called on every cache-clear request so plugin lifecycle
// (POST/PUT/DELETE /api/plugins) is honored — without this, the handler
// would hold a stale pointer after a plugin reload and the routes would
// silently misbehave (or never exist at all if the plugin was loaded
// post-boot rather than at startup).
type CacheClearerResolver func() CacheClearer

type CacheHandler struct {
	resolve CacheClearerResolver
}

// NewCacheHandler returns a CacheHandler that resolves the current plugin
// at request time. The handler is safe to wire unconditionally — when no
// plugin is loaded, each cache-clear request returns HTTP 400 with a clear
// message rather than the route being absent (HTTP 405).
func NewCacheHandler(resolve CacheClearerResolver) *CacheHandler {
	return &CacheHandler{resolve: resolve}
}

func (h *CacheHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	r.DELETE("/api/cache/clear/{cacheId}", lib.ChainMiddlewares(h.clearCache, middlewares...))
	r.DELETE("/api/cache/clear-by-key/{cacheKey}", lib.ChainMiddlewares(h.clearCacheByKey, middlewares...))
}

func (h *CacheHandler) clearCache(ctx *fasthttp.RequestCtx) {
	plugin := h.resolve()
	if plugin == nil {
		SendError(ctx, fasthttp.StatusBadRequest, "semantic_cache plugin is not loaded")
		return
	}
	cacheID, ok := ctx.UserValue("cacheId").(string)
	if !ok || cacheID == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid cache ID")
		return
	}
	if err := plugin.ClearCacheForCacheID(cacheID); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to clear cache")
		return
	}

	SendJSON(ctx, map[string]any{
		"message": "Cache cleared successfully",
	})
}

func (h *CacheHandler) clearCacheByKey(ctx *fasthttp.RequestCtx) {
	plugin := h.resolve()
	if plugin == nil {
		SendError(ctx, fasthttp.StatusBadRequest, "semantic_cache plugin is not loaded")
		return
	}
	cacheKey, ok := ctx.UserValue("cacheKey").(string)
	if !ok {
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid cache key")
		return
	}
	if err := plugin.ClearCacheForKey(cacheKey); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to clear cache")
		return
	}

	SendJSON(ctx, map[string]any{
		"message": "Cache cleared successfully",
	})
}
