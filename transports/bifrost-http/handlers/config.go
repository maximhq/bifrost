package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/fasthttp/router"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// ConfigManager is the interface for the config manager
type ConfigManager interface {
	ReloadClientConfigFromConfigStore() error
	ReloadPricingManager() error
	UpdateDropExcessRequests(value bool)
	ReloadPlugin(ctx context.Context, name string, pluginConfig any) error
}

// ConfigHandler manages runtime configuration updates for Bifrost.
// It provides endpoints to update and retrieve settings persisted via the ConfigStore backed by sql database.
type ConfigHandler struct {
	logger        schemas.Logger
	store         *lib.Config
	configManager ConfigManager
}

// NewConfigHandler creates a new handler for configuration management.
// It requires the Bifrost client, a logger, and the config store.
func NewConfigHandler(configManager ConfigManager, logger schemas.Logger, store *lib.Config) *ConfigHandler {
	return &ConfigHandler{
		configManager: configManager,
		logger:        logger,
		store:         store,
	}
}

// RegisterRoutes registers the configuration-related routes.
// It adds the `PUT /api/config` endpoint.
func (h *ConfigHandler) RegisterRoutes(r *router.Router, middlewares ...lib.BifrostHTTPMiddleware) {
	r.GET("/api/config", lib.ChainMiddlewares(h.getConfig, middlewares...))
	r.PUT("/api/config", lib.ChainMiddlewares(h.updateConfig, middlewares...))
	r.GET("/api/version", lib.ChainMiddlewares(h.getVersion, middlewares...))
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
		cc, err := h.store.ConfigStore.GetClientConfig(ctx)
		if err != nil {
			SendError(ctx, fasthttp.StatusInternalServerError,
				fmt.Sprintf("failed to fetch config from db: %v", err), h.logger)
			return
		}
		if cc != nil {
			mapConfig["client_config"] = *cc
		}
		// Fetching framework config
		fc, err := h.store.ConfigStore.GetFrameworkConfig(ctx)
		if err != nil {
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to fetch framework config from db: %v", err), h.logger)
			return
		}
		if fc != nil {
			mapConfig["framework_config"] = *fc
		} else {
			mapConfig["framework_config"] = configstoreTables.TableFrameworkConfig{
				PricingURL:          bifrost.Ptr(modelcatalog.DefaultPricingURL),
				PricingSyncInterval: bifrost.Ptr(int64(modelcatalog.DefaultPricingSyncInterval.Seconds())),
			}
		}
	} else {
		mapConfig["client_config"] = h.store.ClientConfig
		if h.store.FrameworkConfig == nil {
			mapConfig["framework_config"] = configstoreTables.TableFrameworkConfig{
				PricingURL:          bifrost.Ptr(modelcatalog.DefaultPricingURL),
				PricingSyncInterval: bifrost.Ptr(int64(modelcatalog.DefaultPricingSyncInterval.Seconds())),
			}
		} else if h.store.FrameworkConfig.Pricing != nil && h.store.FrameworkConfig.Pricing.PricingURL != nil {
			mapConfig["framework_config"] = configstoreTables.TableFrameworkConfig{
				PricingURL:          h.store.FrameworkConfig.Pricing.PricingURL,
				PricingSyncInterval: bifrost.Ptr(int64(*h.store.FrameworkConfig.Pricing.PricingSyncInterval)),
			}
		}
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

	payload := struct {
		ClientConfig    configstore.ClientConfig               `json:"client_config"`
		FrameworkConfig configstoreTables.TableFrameworkConfig `json:"framework_config"`
	}{}

	if err := json.Unmarshal(ctx.PostBody(), &payload); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid request format: %v", err), h.logger)
		return
	}

	// Validating framework config
	if payload.FrameworkConfig.PricingURL != nil && *payload.FrameworkConfig.PricingURL != modelcatalog.DefaultPricingURL {
		// Checking the accessibility of the pricing URL
		resp, err := http.Get(*payload.FrameworkConfig.PricingURL)
		if err != nil {
			h.logger.Warn(fmt.Sprintf("failed to check the accessibility of the pricing URL: %v", err))
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to check the accessibility of the pricing URL: %v", err), h.logger)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			h.logger.Warn(fmt.Sprintf("failed to check the accessibility of the pricing URL: %v", resp.StatusCode))
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to check the accessibility of the pricing URL: %v", resp.StatusCode), h.logger)
			return
		}
	}

	// Checking the pricing sync interval
	if payload.FrameworkConfig.PricingSyncInterval != nil && *payload.FrameworkConfig.PricingSyncInterval <= 0 {
		h.logger.Warn("pricing sync interval must be greater than 0")
		SendError(ctx, fasthttp.StatusBadRequest, "pricing sync interval must be greater than 0", h.logger)
		return
	}

	// Get current config with proper locking
	currentConfig := h.store.ClientConfig
	updatedConfig := currentConfig

	shouldReloadTelemetryPlugin := false

	if payload.ClientConfig.DropExcessRequests != currentConfig.DropExcessRequests {
		h.configManager.UpdateDropExcessRequests(payload.ClientConfig.DropExcessRequests)
		updatedConfig.DropExcessRequests = payload.ClientConfig.DropExcessRequests
	}

	if !slices.Equal(payload.ClientConfig.PrometheusLabels, currentConfig.PrometheusLabels) {
		updatedConfig.PrometheusLabels = payload.ClientConfig.PrometheusLabels
		shouldReloadTelemetryPlugin = true
	}

	if !slices.Equal(payload.ClientConfig.AllowedOrigins, currentConfig.AllowedOrigins) {
		updatedConfig.AllowedOrigins = payload.ClientConfig.AllowedOrigins
	}

	updatedConfig.InitialPoolSize = payload.ClientConfig.InitialPoolSize
	updatedConfig.EnableLogging = payload.ClientConfig.EnableLogging
	updatedConfig.EnableGovernance = payload.ClientConfig.EnableGovernance
	updatedConfig.EnforceGovernanceHeader = payload.ClientConfig.EnforceGovernanceHeader
	updatedConfig.AllowDirectKeys = payload.ClientConfig.AllowDirectKeys
	updatedConfig.MaxRequestBodySizeMB = payload.ClientConfig.MaxRequestBodySizeMB
	updatedConfig.EnableLiteLLMFallbacks = payload.ClientConfig.EnableLiteLLMFallbacks

	// Update the store with the new config
	h.store.ClientConfig = updatedConfig

	if err := h.store.ConfigStore.UpdateClientConfig(ctx, &updatedConfig); err != nil {
		h.logger.Warn(fmt.Sprintf("failed to save configuration: %v", err))
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to save configuration: %v", err), h.logger)
		return
	}
	// Reloading client config from config store
	if err := h.configManager.ReloadClientConfigFromConfigStore(); err != nil {
		h.logger.Warn(fmt.Sprintf("failed to reload client config from config store: %v", err))
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to reload client config from config store: %v", err), h.logger)
		return
	}
	// Fetching existing framework config
	frameworkConfig, err := h.store.ConfigStore.GetFrameworkConfig(ctx)
	if err != nil {
		h.logger.Warn(fmt.Sprintf("failed to get framework config from store: %v", err))
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to get framework config from store: %v", err), h.logger)
		return
	}
	// if framework config is nil, we will use the default pricing config
	if frameworkConfig == nil {
		frameworkConfig = &configstoreTables.TableFrameworkConfig{
			ID:                  0,
			PricingURL:          bifrost.Ptr(modelcatalog.DefaultPricingURL),
			PricingSyncInterval: bifrost.Ptr(int64(modelcatalog.DefaultPricingSyncInterval.Seconds())),
		}
	}
	// Handling individual nil cases
	if frameworkConfig.PricingURL == nil {
		frameworkConfig.PricingURL = bifrost.Ptr(modelcatalog.DefaultPricingURL)
	}
	if frameworkConfig.PricingSyncInterval == nil {
		frameworkConfig.PricingSyncInterval = bifrost.Ptr(int64(modelcatalog.DefaultPricingSyncInterval.Seconds()))
	}
	// Updating framework config
	shouldReloadFrameworkConfig := false
	if payload.FrameworkConfig.PricingURL != nil && *payload.FrameworkConfig.PricingURL != *frameworkConfig.PricingURL {
		// Checking the accessibility of the pricing URL
		resp, err := http.Get(*payload.FrameworkConfig.PricingURL)
		if err != nil {
			h.logger.Warn(fmt.Sprintf("failed to check the accessibility of the pricing URL: %v", err))
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to check the accessibility of the pricing URL: %v", err), h.logger)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			h.logger.Warn(fmt.Sprintf("failed to check the accessibility of the pricing URL: %v", resp.StatusCode))
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to check the accessibility of the pricing URL: %v", resp.StatusCode), h.logger)
			return
		}
		frameworkConfig.PricingURL = payload.FrameworkConfig.PricingURL
		shouldReloadFrameworkConfig = true
	}
	if payload.FrameworkConfig.PricingSyncInterval != nil {
		syncInterval := int64(*payload.FrameworkConfig.PricingSyncInterval)
		if syncInterval != *frameworkConfig.PricingSyncInterval {
			frameworkConfig.PricingSyncInterval = &syncInterval
			shouldReloadFrameworkConfig = true
		}
	}
	if shouldReloadFrameworkConfig {
		var syncDuration time.Duration
		if frameworkConfig.PricingSyncInterval != nil {
			syncDuration = time.Duration(*frameworkConfig.PricingSyncInterval) * time.Second
		} else {
			syncDuration = modelcatalog.DefaultPricingSyncInterval
		}
		h.store.FrameworkConfig = &framework.FrameworkConfig{
			Pricing: &modelcatalog.Config{
				PricingURL:          frameworkConfig.PricingURL,
				PricingSyncInterval: &syncDuration,
			},
		}
		// Saving framework config
		if err := h.store.ConfigStore.UpdateFrameworkConfig(ctx, frameworkConfig); err != nil {
			h.logger.Warn(fmt.Sprintf("failed to save framework configuration: %v", err))
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to save framework configuration: %v", err), h.logger)
			return
		}
		// Reloading pricing manager
		h.configManager.ReloadPricingManager()
	}
	if shouldReloadTelemetryPlugin {
		//TODO: Reload telemetry plugin - solvable problem by having a reference modifier on the metrics handler, but that will lead to loss of data on update
		// if err := h.configManager.ReloadPlugin(ctx, telemetry.PluginName, map[string]any{
		// 	"custom_labels": updatedConfig.PrometheusLabels,
		// }); err != nil {
		// 	h.logger.Warn(fmt.Sprintf("failed to reload telemetry plugin: %v", err))
		// 	SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to reload telemetry plugin: %v", err), h.logger)
		// 	return
		// }
	}
	ctx.SetStatusCode(fasthttp.StatusOK)
	SendJSON(ctx, map[string]any{
		"status":  "success",
		"message": "configuration updated successfully",
	}, h.logger)
}
