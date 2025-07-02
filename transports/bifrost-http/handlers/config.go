package handlers

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/fasthttp/router"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// ConfigHandler manages runtime configuration updates for Bifrost.
// It provides an endpoint to hot-reload settings from the configuration file.
type ConfigHandler struct {
	client     *bifrost.Bifrost
	logger     schemas.Logger
	configPath string
}

// NewConfigHandler creates a new handler for configuration management.
// It requires the Bifrost client, a logger, and the path to the config file to be reloaded.
func NewConfigHandler(client *bifrost.Bifrost, logger schemas.Logger, configPath string) *ConfigHandler {
	return &ConfigHandler{
		client:     client,
		logger:     logger,
		configPath: configPath,
	}
}

// RegisterRoutes registers the configuration-related routes.
// It adds the `PUT /config` endpoint.
func (h *ConfigHandler) RegisterRoutes(r *router.Router) {
	r.PUT("/config", h.handleReloadConfig)
}

// handleReloadConfig re-reads the configuration file and applies updatable settings.
// Currently, it supports hot-reloading of the `drop_excess_requests` setting.
// Note that settings like `prometheus_labels` cannot be changed at runtime.
func (h *ConfigHandler) handleReloadConfig(ctx *fasthttp.RequestCtx) {
	var config struct {
		BifrostSettings struct {
			DropExcessRequests *bool `json:"drop_excess_requests,omitempty"`
		} `json:"bifrost_settings"`
	}

	data, err := os.ReadFile(h.configPath)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to read config file: %v", err), h.logger)
		return
	}

	if err := json.Unmarshal(data, &config); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("failed to parse config file: %v", err), h.logger)
		return
	}

	if config.BifrostSettings.DropExcessRequests != nil {
		h.client.UpdateDropExcessRequests(*config.BifrostSettings.DropExcessRequests)
	}

	ctx.SetStatusCode(fasthttp.StatusOK)
	SendJSON(ctx, map[string]interface{}{"status": "config reloaded", "drop_excess_requests": config.BifrostSettings.DropExcessRequests}, h.logger)
}
