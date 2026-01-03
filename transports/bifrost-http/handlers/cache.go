package handlers

import (
	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/plugins/semanticcache"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

type CacheHandler struct {
	plugin *semanticcache.Plugin
}

func NewCacheHandler(plugin schemas.LLMPlugin) *CacheHandler {
	semanticCachePlugin, ok := plugin.(*semanticcache.Plugin)
	if !ok {
		logger.Fatal("Cache handler requires a semantic cache plugin")
	}

	return &CacheHandler{
		plugin: semanticCachePlugin,
	}
}

func (h *CacheHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	r.DELETE("/api/cache/clear/{requestId}", lib.ChainMiddlewares(h.clearCache, middlewares...))
	r.DELETE("/api/cache/clear-by-key/{cacheKey}", lib.ChainMiddlewares(h.clearCacheByKey, middlewares...))
}

func (h *CacheHandler) clearCache(ctx *fasthttp.RequestCtx) {
	requestID, ok := ctx.UserValue("requestId").(string)
	if !ok {
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid request ID")
		return
	}
	if err := h.plugin.ClearCacheForRequestID(requestID); err != nil {
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
