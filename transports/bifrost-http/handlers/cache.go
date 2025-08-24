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

func (h *CacheHandler) RegisterRoutes(r *router.Router) {
	r.DELETE("/api/cache/clear/{request-id}", h.ClearCache)
	r.DELETE("/api/cache/clear-by-key/{cache-key}", h.ClearCacheByKey)
}

func (h *CacheHandler) ClearCache(ctx *fasthttp.RequestCtx) {
	requestID, ok := ctx.UserValue("request-id").(string)
	if !ok {
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid request ID", h.logger)
		return
	}
	if err := h.plugin.ClearCacheForRequestID(requestID); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to clear cache", h.logger)
		return
	}

	SendJSON(ctx, map[string]interface{}{
		"message": "Cache cleared successfully",
	}, h.logger)
}

func (h *CacheHandler) ClearCacheByKey(ctx *fasthttp.RequestCtx) {
	cacheKey, ok := ctx.UserValue("cache-key").(string)
	if !ok {
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid cache key", h.logger)
		return
	}
	if err := h.plugin.ClearCacheForKey(cacheKey); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to clear cache", h.logger)
		return
	}

	SendJSON(ctx, map[string]interface{}{
		"message": "Cache cleared successfully",
	}, h.logger)
}
