package handlers

import (
	"encoding/json"
	"fmt"

	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// RedisHandler manages Redis plugin configuration for Bifrost.
// It provides endpoints to update and retrieve Redis caching settings.
type RedisHandler struct {
	store  *lib.ConfigStore
	logger schemas.Logger
}

// NewRedisHandler creates a new handler for Redis configuration management.
func NewRedisHandler(store *lib.ConfigStore, logger schemas.Logger) *RedisHandler {
	return &RedisHandler{
		store:  store,
		logger: logger,
	}
}

// RegisterRoutes registers the Redis configuration-related routes.
func (h *RedisHandler) RegisterRoutes(r *router.Router) {
	r.GET("/api/config/redis", h.GetRedisConfig)
	r.PUT("/api/config/redis", h.UpdateRedisConfig)
}

// GetRedisConfig handles GET /api/config/redis - Get the current Redis configuration
func (h *RedisHandler) GetRedisConfig(ctx *fasthttp.RequestCtx) {
	config, err := h.store.GetRedisConfig()
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to get Redis config: %v", err), h.logger)
		return
	}

	SendJSON(ctx, config, h.logger)
}

// UpdateRedisConfig handles PUT /api/config/redis - Update Redis configuration
func (h *RedisHandler) UpdateRedisConfig(ctx *fasthttp.RequestCtx) {
	var req lib.DBRedisConfig

	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid request format: %v", err), h.logger)
		return
	}

	// Validate required fields
	if req.Addr == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "Redis address is required", h.logger)
		return
	}

	// Validate TTL
	if req.TTLSeconds <= 0 {
		req.TTLSeconds = 300 // Default to 5 minutes
	}

	// Update Redis configuration in database
	if err := h.store.UpdateRedisConfig(&req); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to update Redis config: %v", err), h.logger)
		return
	}

	h.logger.Info("Redis configuration updated successfully")

	ctx.SetStatusCode(fasthttp.StatusOK)
	SendJSON(ctx, map[string]any{
		"status":  "success",
		"message": "Redis configuration updated successfully",
		"config":  req,
	}, h.logger)
}
