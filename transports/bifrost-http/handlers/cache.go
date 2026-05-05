package handlers

import (
	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/plugins/localcache"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// cacheClearer is the minimal contract the handler needs from the semantic
// cache plugin. Defined here (rather than imported) so tests can substitute
// a fake without spinning up a real vector store.
type cacheClearer interface {
	ClearCacheForCacheID(cacheID string) error
	ClearCacheForKey(cacheKey string) error
}

type CacheHandler struct {
	plugin cacheClearer
}

func NewCacheHandler(plugin schemas.LLMPlugin) *CacheHandler {
	localCachePlugin, ok := plugin.(*localcache.Plugin)
	if !ok {
		logger.Fatal("Cache handler requires a local cache plugin")
	}

	return &CacheHandler{
		plugin: localCachePlugin,
	}
}

func (h *CacheHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	r.DELETE("/api/cache/clear/{cacheId}", lib.ChainMiddlewares(h.clearCache, middlewares...))
	r.DELETE("/api/cache/clear-by-key/{cacheKey}", lib.ChainMiddlewares(h.clearCacheByKey, middlewares...))
}

func (h *CacheHandler) clearCache(ctx *fasthttp.RequestCtx) {
	cacheID, ok := ctx.UserValue("cacheId").(string)
	if !ok || cacheID == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid cache ID")
		return
	}
	if err := h.plugin.ClearCacheForCacheID(cacheID); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to clear cache")
		return
	}

	SendJSON(ctx, map[string]any{
		"message": "Cache cleared successfully",
	})
}

func (h *CacheHandler) clearCacheByKey(ctx *fasthttp.RequestCtx) {
	cacheKey, ok := ctx.UserValue("cacheKey").(string)
	if !ok {
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid cache key")
		return
	}
	if err := h.plugin.ClearCacheForKey(cacheKey); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to clear cache")
		return
	}

	SendJSON(ctx, map[string]any{
		"message": "Cache cleared successfully",
	})
}
