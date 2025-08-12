package handlers

import (
	"fmt"
	"net/url"

	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/plugins/redis"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// CacheHandler manages Cache plugin configuration for Bifrost.
// It provides endpoints to update and retrieve Cache caching settings.
type CacheHandler struct {
	store  *lib.Config
	plugin *redis.Plugin
	logger schemas.Logger
}

// NewCacheHandler creates a new handler for Cache configuration management.
func NewCacheHandler(store *lib.Config, plugin *redis.Plugin, logger schemas.Logger) *CacheHandler {
	return &CacheHandler{
		store:  store,
		plugin: plugin,
		logger: logger,
	}
}

// RegisterRoutes registers the Cache configuration-related routes.
func (h *CacheHandler) RegisterRoutes(r *router.Router) {
	r.GET("/api/config/cache", h.GetCacheConfig)
	r.PUT("/api/config/cache", h.UpdateCacheConfig)
	r.DELETE("/api/cache/{key}", h.DeleteCacheCache)
}

// GetCacheConfig handles GET /api/config/cache - Get the current Cache configuration
func (h *CacheHandler) GetCacheConfig(ctx *fasthttp.RequestCtx) {
	config, err := h.store.GetCacheConfigRedacted()
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to get Cache config: %v", err), h.logger)
		return
	}

	SendJSON(ctx, config, h.logger)
}

// UpdateCacheConfig handles PUT /api/config/cache - Update Cache configuration
func (h *CacheHandler) UpdateCacheConfig(ctx *fasthttp.RequestCtx) {
	// var req lib.TableVectorStoreConfig

	// if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
	// 	SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("invalid request format: %v", err), h.logger)
	// 	return
	// }

	// // Validate required fields
	// if req.Addr == "" {
	// 	SendError(ctx, fasthttp.StatusBadRequest, "cache address is required", h.logger)
	// 	return
	// }

	// // Validate address format (host:port)
	// if !strings.Contains(req.Addr, ":") {
	// 	SendError(ctx, fasthttp.StatusBadRequest, "cache address must be in format 'host:port'", h.logger)
	// 	return
	// }

	// hostPort := strings.SplitN(req.Addr, ":", 2)
	// if len(hostPort) != 2 || hostPort[0] == "" {
	// 	SendError(ctx, fasthttp.StatusBadRequest, "cache address must have a non-empty host part before the colon", h.logger)
	// 	return
	// }

	// // Validate TTL
	// if req.TTLSeconds <= 0 {
	// 	req.TTLSeconds = 300 // Default to 5 minutes
	// }

	// // Handle password redaction - if password is redacted, preserve existing password
	// if req.Password != "" && lib.IsRedacted(req.Password) {
	// 	// Get current config to preserve the existing password
	// 	currentConfig, err := h.store.GetCacheConfig()
	// 	if err != nil {
	// 		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to get current Cache config: %v", err), h.logger)
	// 		return
	// 	}
	// 	// Preserve the existing password
	// 	req.Password = currentConfig.Password
	// }

	// // Update Cache configuration in database
	// if err := h.store.UpdateCacheConfig(&req); err != nil {
	// 	SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to update Cache config: %v", err), h.logger)
	// 	return
	// }

	// // Redact the password
	// req.Password = lib.RedactKey(req.Password)

	// h.logger.Info("Cache configuration updated successfully")

	SendJSON(ctx, map[string]any{
		"status":  "success",
		"message": "Cache configuration updated successfully",
		// "config":  req,
	}, h.logger)
}

// DeleteCacheCache handles DELETE /api/cache/{key} - Delete a specific cache key from Cache
func (h *CacheHandler) DeleteCacheCache(ctx *fasthttp.RequestCtx) {
	// Safely extract and validate the key parameter
	keyValue := ctx.UserValue("key")
	if keyValue == nil {
		SendError(ctx, fasthttp.StatusBadRequest, "cache key parameter is required", h.logger)
		return
	}

	keyStr, ok := keyValue.(string)
	if !ok {
		SendError(ctx, fasthttp.StatusBadRequest, "cache key parameter must be a string", h.logger)
		return
	}

	// URL unescape the key to handle percent-encoded path segments
	unescapedKey, err := url.PathUnescape(keyStr)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("invalid URL-encoded key: %v", err), h.logger)
		return
	}

	// Guard against nil plugin
	if h.plugin == nil {
		h.logger.Error(fmt.Errorf("redis plugin is not available for cache deletion"))
		SendError(ctx, fasthttp.StatusInternalServerError, "cache plugin is not available", h.logger)
		return
	}

	// Clear the cache key and handle errors
	if err := h.plugin.ClearCacheForKey(unescapedKey); err != nil {
		h.logger.Error(fmt.Errorf("failed to delete Cache cache for key '%s': %w", unescapedKey, err))
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to delete Cache cache: %v", err), h.logger)
		return
	}

	SendJSON(ctx, map[string]any{
		"status":  "success",
		"message": "cache cache deleted successfully",
	}, h.logger)
}
