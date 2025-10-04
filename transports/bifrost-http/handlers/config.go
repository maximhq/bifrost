package handlers

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/fasthttp/router"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/vectorstore"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// ConfigHandler manages runtime configuration updates for Bifrost.
// It provides endpoints to update and retrieve settings persisted via the ConfigStore backed by sql database.
type ConfigHandler struct {
	client *bifrost.Bifrost
	logger schemas.Logger
	store  *lib.Config
}

// NewConfigHandler creates a new handler for configuration management.
// It requires the Bifrost client, a logger, and the config store.
func NewConfigHandler(client *bifrost.Bifrost, logger schemas.Logger, store *lib.Config) *ConfigHandler {
	return &ConfigHandler{
		client: client,
		logger: logger,
		store:  store,
	}
}

// RegisterRoutes registers the configuration-related routes.
// It adds the `PUT /api/config` endpoint.
func (h *ConfigHandler) RegisterRoutes(r *router.Router) {
	r.GET("/api/config", h.getConfig)
	r.PUT("/api/config", h.updateConfig)
	r.GET("/api/version", h.getVersion)
	// Vector store configuration endpoints
	r.GET("/api/config/vector-store", h.getVectorStoreConfig)
	r.PUT("/api/config/vector-store", h.updateVectorStoreConfig)
	// Log store configuration endpoints
	r.GET("/api/config/log-store", h.getLogStoreConfig)
	r.PUT("/api/config/log-store", h.updateLogStoreConfig)
}

// getVersion handles GET /api/version - Get the current version
func (h *ConfigHandler) getVersion(ctx *fasthttp.RequestCtx) {
	SendJSON(ctx, version, h.logger)
}

// getConfig handles GET /config - Get the current configuration
func (h *ConfigHandler) getConfig(ctx *fasthttp.RequestCtx) {

	var mapConfig = make(map[string]any)

	if query := string(ctx.QueryArgs().Peek("from_db")); query == "true" {
		if h.store.ConfigStore == nil {
			SendError(ctx, fasthttp.StatusServiceUnavailable, "config store not available", h.logger)
			return
		}
		cc, err := h.store.ConfigStore.GetClientConfig()
		if err != nil {
			SendError(ctx, fasthttp.StatusInternalServerError,
				fmt.Sprintf("failed to fetch config from db: %v", err), h.logger)
			return
		}
		if cc != nil {
			mapConfig["client_config"] = *cc
		}
	} else {
		mapConfig["client_config"] = h.store.ClientConfig
	}

	mapConfig["is_db_connected"] = h.store.ConfigStore != nil
	mapConfig["is_cache_connected"] = h.store.VectorStore != nil
	mapConfig["is_logs_connected"] = h.store.LogsStore != nil

	SendJSON(ctx, mapConfig, h.logger)
}

// updateConfig updates the core configuration settings.
// Currently, it supports hot-reloading of the `drop_excess_requests` setting.
// Note that settings like `prometheus_labels` cannot be changed at runtime.
func (h *ConfigHandler) updateConfig(ctx *fasthttp.RequestCtx) {
	if h.store.ConfigStore == nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Config store not initialized", h.logger)
		return
	}

	var req configstore.ClientConfig

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
	updatedConfig.AllowDirectKeys = req.AllowDirectKeys
	updatedConfig.MaxRequestBodySizeMB = req.MaxRequestBodySizeMB

	// Update the store with the new config
	h.store.ClientConfig = updatedConfig

	if err := h.store.ConfigStore.UpdateClientConfig(&updatedConfig); err != nil {
		h.logger.Warn(fmt.Sprintf("failed to save configuration: %v", err))
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to save configuration: %v", err), h.logger)
		return
	}

	ctx.SetStatusCode(fasthttp.StatusOK)
	SendJSON(ctx, map[string]string{"status": "success"}, h.logger)
}

// getVectorStoreConfig handles GET /api/config/vector-store - Get the current vector store configuration
func (h *ConfigHandler) getVectorStoreConfig(ctx *fasthttp.RequestCtx) {
	if h.store.ConfigStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "config store not available", h.logger)
		return
	}

	config, err := h.store.GetVectorStoreConfigRedacted()
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError,
			fmt.Sprintf("failed to fetch vector store config: %v", err), h.logger)
		return
	}

	SendJSON(ctx, config, h.logger)
}

// updateVectorStoreConfig handles PUT /api/config/vector-store - Update vector store configuration
func (h *ConfigHandler) updateVectorStoreConfig(ctx *fasthttp.RequestCtx) {
	if h.store.ConfigStore == nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Config store not initialized", h.logger)
		return
	}

	var req vectorstore.Config

	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid request format: %v", err), h.logger)
		return
	}

	// Get the raw config to access actual values for merging with redacted request values
	oldConfigRaw, err := h.store.ConfigStore.GetVectorStoreConfig()
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to get current vector store config: %v", err), h.logger)
		return
	}

	if oldConfigRaw == nil {
		oldConfigRaw = &vectorstore.Config{}
	}

	// Merge redacted values with actual values
	mergedConfig, err := h.mergeVectorStoreConfig(oldConfigRaw, &req)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("failed to merge vector store config: %v", err), h.logger)
		return
	}

	if err := h.store.ConfigStore.UpdateVectorStoreConfig(mergedConfig); err != nil {
		h.logger.Warn(fmt.Sprintf("failed to save vector store configuration: %v", err))
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to save vector store configuration: %v", err), h.logger)
		return
	}

	ctx.SetStatusCode(fasthttp.StatusOK)
	SendJSON(ctx, map[string]string{"status": "success"}, h.logger)
}

// getLogStoreConfig handles GET /api/config/log-store - Get the current log store configuration
func (h *ConfigHandler) getLogStoreConfig(ctx *fasthttp.RequestCtx) {
	if h.store.ConfigStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "config store not available", h.logger)
		return
	}

	config, err := h.store.ConfigStore.GetLogsStoreConfig()
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError,
			fmt.Sprintf("failed to fetch log store config: %v", err), h.logger)
		return
	}

	SendJSON(ctx, config, h.logger)
}

// updateLogStoreConfig handles PUT /api/config/log-store - Update log store configuration
func (h *ConfigHandler) updateLogStoreConfig(ctx *fasthttp.RequestCtx) {
	if h.store.ConfigStore == nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Config store not initialized", h.logger)
		return
	}

	var req logstore.Config

	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid request format: %v", err), h.logger)
		return
	}

	if err := h.store.ConfigStore.UpdateLogsStoreConfig(&req); err != nil {
		h.logger.Warn(fmt.Sprintf("failed to save log store configuration: %v", err))
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to save log store configuration: %v", err), h.logger)
		return
	}

	ctx.SetStatusCode(fasthttp.StatusOK)
	SendJSON(ctx, map[string]string{"status": "success"}, h.logger)
}

// mergeVectorStoreConfig merges new config with old, preserving values that are redacted in the new config
func (h *ConfigHandler) mergeVectorStoreConfig(oldConfig *vectorstore.Config, newConfig *vectorstore.Config) (*vectorstore.Config, error) {
	// Start with the new config
	merged := *newConfig

	// Handle different vector store types
	if oldConfig.Type == newConfig.Type {
		switch newConfig.Type {
		case vectorstore.VectorStoreTypeWeaviate:
			oldWeaviateConfig, oldOk := oldConfig.Config.(*vectorstore.WeaviateConfig)
			newWeaviateConfig, newOk := newConfig.Config.(*vectorstore.WeaviateConfig)
			if oldOk && newOk {
				mergedWeaviateConfig := *newWeaviateConfig
				// Preserve old API key if new one is redacted
				if lib.IsRedacted(newWeaviateConfig.ApiKey) && oldWeaviateConfig.ApiKey != "" {
					mergedWeaviateConfig.ApiKey = oldWeaviateConfig.ApiKey
				}
				merged.Config = &mergedWeaviateConfig
			}
		case vectorstore.VectorStoreTypeRedis:
			oldRedisConfig, oldOk := oldConfig.Config.(*vectorstore.RedisConfig)
			newRedisConfig, newOk := newConfig.Config.(*vectorstore.RedisConfig)
			if oldOk && newOk {
				mergedRedisConfig := *newRedisConfig
				// Preserve old password if new one is redacted
				if lib.IsRedacted(newRedisConfig.Addr) && oldRedisConfig.Addr != "" {
					mergedRedisConfig.Addr = oldRedisConfig.Addr
				}
				merged.Config = &mergedRedisConfig
			}
		}
	}

	return &merged, nil
}
