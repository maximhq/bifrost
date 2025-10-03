package handlers

import (
	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/plugins/semanticcache"
	"github.com/valyala/fasthttp"
)

type CacheHandler struct {
	logger schemas.Logger
	plugin *semanticcache.Plugin
}

func NewCacheHandler(plugin schemas.Plugin, logger schemas.Logger) *CacheHandler {
	semanticCachePlugin, ok := plugin.(*semanticcache.Plugin)
	if !ok {
		logger.Fatal("Cache handler requires a semantic cache plugin")
	}

	return &CacheHandler{
		plugin: semanticCachePlugin,
		logger: logger,
	}
}

func (h *CacheHandler) RegisterRoutes(r *router.Router, middlewares ...BifrostHTTPMiddleware) {
	r.DELETE("/api/cache/clear/{requestId}", ChainMiddlewares(h.clearCache, middlewares...))
	r.DELETE("/api/cache/clear-by-key/{cacheKey}", ChainMiddlewares(h.clearCacheByKey, middlewares...))
}

func (h *CacheHandler) clearCache(ctx *fasthttp.RequestCtx) {
	requestID, ok := ctx.UserValue("requestId").(string)
	if !ok {
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid request ID", h.logger)
		return
	}
	if err := h.plugin.ClearCacheForRequestID(requestID); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to clear cache", h.logger)
		return
	}

	SendJSON(ctx, map[string]any{
		"message": "Cache cleared successfully",
	}, h.logger)
}

func (h *CacheHandler) clearCacheByKey(ctx *fasthttp.RequestCtx) {
	cacheKey, ok := ctx.UserValue("cacheKey").(string)
	if !ok {
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid cache key", h.logger)
		return
	}
	if err := h.plugin.ClearCacheForKey(cacheKey); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to clear cache", h.logger)
		return
	}

	SendJSON(ctx, map[string]any{
		"message": "Cache cleared successfully",
	}, h.logger)
}
