package handlers

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/fasthttp/router"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// ConfigHandler manages runtime configuration updates for Bifrost.
// It provides endpoints to update and retrieve settings persisted via the ConfigStore backed by sql database.
type ConfigHandler struct {
	client *bifrost.Bifrost
	logger schemas.Logger
	store  *lib.ConfigStore
}

// NewConfigHandler creates a new handler for configuration management.
// It requires the Bifrost client, a logger, and the config store.
func NewConfigHandler(client *bifrost.Bifrost, logger schemas.Logger, store *lib.ConfigStore) *ConfigHandler {
	return &ConfigHandler{
		client: client,
		logger: logger,
		store:  store,
	}
}

// RegisterRoutes registers the configuration-related routes.
// It adds the `PUT /api/config` endpoint.
func (h *ConfigHandler) RegisterRoutes(r *router.Router) {
	r.GET("/api/config", h.GetConfig)
	r.PUT("/api/config", h.handleUpdateConfig)
}

// GetConfig handles GET /config - Get the current configuration
func (h *ConfigHandler) GetConfig(ctx *fasthttp.RequestCtx) {
	if query := string(ctx.QueryArgs().Peek("from_db")); query == "true" {
		config, err := h.store.GetClientConfigFromDB()
		if err != nil {
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to get client config from database: %v", err), h.logger)
			return
		}
		SendJSON(ctx, config, h.logger)
		return
	} else {
		config := h.store.ClientConfig
		SendJSON(ctx, config, h.logger)
	}
}

// handleUpdateConfig updates the core configuration settings.
// Currently, it supports hot-reloading of the `drop_excess_requests` setting.
// Note that settings like `prometheus_labels` cannot be changed at runtime.
func (h *ConfigHandler) handleUpdateConfig(ctx *fasthttp.RequestCtx) {
	var req lib.ClientConfig

	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid request format: %v", err), h.logger)
		return
	}

	// Get current config with proper locking
	currentConfig := h.store.ClientConfig
	updatedConfig := currentConfig

	if req.DropExcessRequests != currentConfig.DropExcessRequests {
		h.client.UpdateDropExcessRequests(req.DropExcessRequests)
		updatedConfig.DropExcessRequests = req.DropExcessRequests
	}

	if !slices.Equal(req.PrometheusLabels, currentConfig.PrometheusLabels) {
		updatedConfig.PrometheusLabels = req.PrometheusLabels
	}

	if !slices.Equal(req.AllowedOrigins, currentConfig.AllowedOrigins) {
		updatedConfig.AllowedOrigins = req.AllowedOrigins
	}

	updatedConfig.InitialPoolSize = req.InitialPoolSize
	updatedConfig.EnableLogging = req.EnableLogging
	updatedConfig.EnableGovernance = req.EnableGovernance
	updatedConfig.EnforceGovernanceHeader = req.EnforceGovernanceHeader
	updatedConfig.EnableCaching = req.EnableCaching

	// Update the store with the new config
	h.store.ClientConfig = updatedConfig

	if err := h.store.SaveConfig(); err != nil {
		h.logger.Warn(fmt.Sprintf("Failed to save configuration: %v", err))
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to save configuration: %v", err), h.logger)
		return
	}

	ctx.SetStatusCode(fasthttp.StatusOK)
	SendJSON(ctx, map[string]any{
		"status":  "success",
		"message": "Configuration updated successfully",
	}, h.logger)
}
