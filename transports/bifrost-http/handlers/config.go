package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/fasthttp/router"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/network"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/encrypt"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/maximhq/bifrost/plugins/litellmcompat"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// securityHeaders is the list of headers that cannot be configured in allowlist/denylist
// These headers are always blocked for security reasons regardless of user configuration
var securityHeaders = []string{
	"authorization",
	"proxy-authorization",
	"cookie",
	"host",
	"content-length",
	"connection",
	"transfer-encoding",
	"x-api-key",
	"x-goog-api-key",
	"x-bf-api-key",
	"x-bf-vk",
}

// ConfigManager is the interface for the config manager
type ConfigManager interface {
	UpdateAuthConfig(ctx context.Context, authConfig *configstore.AuthConfig) error
	ReloadClientConfigFromConfigStore(ctx context.Context) error
	ReloadPricingManager(ctx context.Context) error
	ForceReloadPricing(ctx context.Context) error
	UpdateDropExcessRequests(ctx context.Context, value bool)
	UpdateMCPToolManagerConfig(ctx context.Context, maxAgentDepth int, toolExecutionTimeoutInSeconds int, codeModeBindingLevel string) error
	ReloadPlugin(ctx context.Context, name string, path *string, pluginConfig any) error
	RemovePlugin(ctx context.Context, name string) error
	ReloadProxyConfig(ctx context.Context, config *configstoreTables.GlobalProxyConfig) error
	ReloadHeaderFilterConfig(ctx context.Context, config *configstoreTables.GlobalHeaderFilterConfig) error
}

// ConfigHandler manages runtime configuration updates for Bifrost.
// It provides endpoints to update and retrieve settings persisted via the ConfigStore backed by sql database.
type ConfigHandler struct {
	store         *lib.Config
	configManager ConfigManager
}

// NewConfigHandler creates a new handler for configuration management.
// It requires the Bifrost client, a logger, and the config store.
func NewConfigHandler(configManager ConfigManager, store *lib.Config) *ConfigHandler {
	return &ConfigHandler{
		configManager: configManager,
		store:         store,
	}
}

// RegisterRoutes registers the configuration-related routes.
// It adds the `PUT /api/config` endpoint.
func (h *ConfigHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	r.GET("/api/config", lib.ChainMiddlewares(h.getConfig, middlewares...))
	r.PUT("/api/config", lib.ChainMiddlewares(h.updateConfig, middlewares...))
	r.GET("/api/version", lib.ChainMiddlewares(h.getVersion, middlewares...))
	r.GET("/api/proxy-config", lib.ChainMiddlewares(h.getProxyConfig, middlewares...))
	r.PUT("/api/proxy-config", lib.ChainMiddlewares(h.updateProxyConfig, middlewares...))
	r.POST("/api/pricing/force-sync", lib.ChainMiddlewares(h.forceSyncPricing, middlewares...))
}

// getVersion handles GET /api/version - Get the current version
func (h *ConfigHandler) getVersion(ctx *fasthttp.RequestCtx) {
	SendJSON(ctx, version)
}

// getConfig handles GET /config - Get the current configuration
func (h *ConfigHandler) getConfig(ctx *fasthttp.RequestCtx) {
	var mapConfig = make(map[string]any)

	if query := string(ctx.QueryArgs().Peek("from_db")); query == "true" {
		if h.store.ConfigStore == nil {
			SendError(ctx, fasthttp.StatusServiceUnavailable, "config store not available")
			return
		}
		cc, err := h.store.ConfigStore.GetClientConfig(ctx)
		if err != nil {
			SendError(ctx, fasthttp.StatusInternalServerError,
				fmt.Sprintf("failed to fetch config from db: %v", err))
			return
		}
		if cc != nil {
			mapConfig["client_config"] = *cc
		}
		// Fetching framework config
		fc, err := h.store.ConfigStore.GetFrameworkConfig(ctx)
		if err != nil {
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to fetch framework config from db: %v", err))
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
				PricingSyncInterval: bifrost.Ptr(int64((*h.store.FrameworkConfig.Pricing.PricingSyncInterval).Seconds())),
			}
		}
	}
	if h.store.ConfigStore != nil {
		// Fetching governance config
		authConfig, err := h.store.ConfigStore.GetAuthConfig(ctx)
		if err != nil {
			logger.Warn("failed to get auth config from store: %v", err)
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to get auth config from store: %v", err))
			return
		}
		// Getting username and password from auth config
		// This username password is for the dashboard authentication
		if authConfig != nil {
			password := ""
			if authConfig.AdminPassword != "" {
				password = "<redacted>"
			}
			// Password we will hash it
			mapConfig["auth_config"] = map[string]any{
				"admin_username":            authConfig.AdminUserName,
				"admin_password":            password,
				"is_enabled":                authConfig.IsEnabled,
				"disable_auth_on_inference": authConfig.DisableAuthOnInference,
			}
		}
	} else {
		mapConfig["auth_config"] = map[string]any{
			"admin_username":            "",
			"admin_password":            "",
			"is_enabled":                false,
			"disable_auth_on_inference": false,
		}
	}
	mapConfig["is_db_connected"] = h.store.ConfigStore != nil
	mapConfig["is_cache_connected"] = h.store.VectorStore != nil
	mapConfig["is_logs_connected"] = h.store.LogsStore != nil
	// Fetching proxy config
	if h.store.ConfigStore != nil {
		proxyConfig, err := h.store.ConfigStore.GetProxyConfig(ctx)
		if err != nil {
			logger.Warn("failed to get proxy config from store: %v", err)
		} else if proxyConfig != nil {
			// Redact password if present
			if proxyConfig.Password != "" {
				proxyConfig.Password = "<redacted>"
			}
			mapConfig["proxy_config"] = proxyConfig
		}
		// Fetching restart required config
		restartConfig, err := h.store.ConfigStore.GetRestartRequiredConfig(ctx)
		if err != nil {
			logger.Warn("failed to get restart required config from store: %v", err)
		} else if restartConfig != nil {
			mapConfig["restart_required"] = restartConfig
		}
	}
	SendJSON(ctx, mapConfig)
}

// updateConfig updates the core configuration settings.
// Currently, it supports hot-reloading of the `drop_excess_requests` setting.
// Note that settings like `prometheus_labels` cannot be changed at runtime.
func (h *ConfigHandler) updateConfig(ctx *fasthttp.RequestCtx) {
	if h.store.ConfigStore == nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Config store not initialized")
		return
	}

	payload := struct {
		ClientConfig    configstore.ClientConfig               `json:"client_config"`
		FrameworkConfig configstoreTables.TableFrameworkConfig `json:"framework_config"`
		AuthConfig      *configstore.AuthConfig                `json:"auth_config"`
	}{}

	if err := json.Unmarshal(ctx.PostBody(), &payload); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid request format: %v", err))
		return
	}

	// Validating framework config
	if payload.FrameworkConfig.PricingURL != nil && *payload.FrameworkConfig.PricingURL != modelcatalog.DefaultPricingURL {
		// Checking the accessibility of the pricing URL
		resp, err := http.Get(*payload.FrameworkConfig.PricingURL)
		if err != nil {
			logger.Warn("failed to check the accessibility of the pricing URL: %v", err)
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to check the accessibility of the pricing URL: %v", err))
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			logger.Warn("failed to check the accessibility of the pricing URL: %v", resp.StatusCode)
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to check the accessibility of the pricing URL: %v", resp.StatusCode))
			return
		}
	}

	// Checking the pricing sync interval
	if payload.FrameworkConfig.PricingSyncInterval != nil && *payload.FrameworkConfig.PricingSyncInterval <= 0 {
		logger.Warn("pricing sync interval must be greater than 0")
		SendError(ctx, fasthttp.StatusBadRequest, "pricing sync interval must be greater than 0")
		return
	}

	// Get current config with proper locking
	currentConfig := h.store.ClientConfig
	updatedConfig := currentConfig

	var restartReasons []string

	if payload.ClientConfig.DropExcessRequests != currentConfig.DropExcessRequests {
		h.configManager.UpdateDropExcessRequests(ctx, payload.ClientConfig.DropExcessRequests)
		updatedConfig.DropExcessRequests = payload.ClientConfig.DropExcessRequests
	}

	if payload.ClientConfig.MCPCodeModeBindingLevel != "" {
		if payload.ClientConfig.MCPCodeModeBindingLevel != string(schemas.CodeModeBindingLevelServer) && payload.ClientConfig.MCPCodeModeBindingLevel != string(schemas.CodeModeBindingLevelTool) {
			logger.Warn("mcp_code_mode_binding_level must be 'server' or 'tool'")
			SendError(ctx, fasthttp.StatusBadRequest, "mcp_code_mode_binding_level must be 'server' or 'tool'")
			return
		}
	}

	shouldReloadMCPToolManagerConfig := false

	if payload.ClientConfig.MCPAgentDepth != currentConfig.MCPAgentDepth {
		if payload.ClientConfig.MCPAgentDepth <= 0 {
			logger.Warn("mcp_agent_depth must be greater than 0")
			SendError(ctx, fasthttp.StatusBadRequest, "mcp_agent_depth must be greater than 0")
			return
		}
		updatedConfig.MCPAgentDepth = payload.ClientConfig.MCPAgentDepth
		shouldReloadMCPToolManagerConfig = true
	}

	if payload.ClientConfig.MCPToolExecutionTimeout != currentConfig.MCPToolExecutionTimeout {
		if payload.ClientConfig.MCPToolExecutionTimeout <= 0 {
			logger.Warn("mcp_tool_execution_timeout must be greater than 0")
			SendError(ctx, fasthttp.StatusBadRequest, "mcp_tool_execution_timeout must be greater than 0")
			return
		}
		updatedConfig.MCPToolExecutionTimeout = payload.ClientConfig.MCPToolExecutionTimeout
		shouldReloadMCPToolManagerConfig = true
	}

	if payload.ClientConfig.MCPCodeModeBindingLevel != "" && payload.ClientConfig.MCPCodeModeBindingLevel != currentConfig.MCPCodeModeBindingLevel {
		updatedConfig.MCPCodeModeBindingLevel = payload.ClientConfig.MCPCodeModeBindingLevel
		shouldReloadMCPToolManagerConfig = true
	}

	if shouldReloadMCPToolManagerConfig {
		if err := h.configManager.UpdateMCPToolManagerConfig(ctx, updatedConfig.MCPAgentDepth, updatedConfig.MCPToolExecutionTimeout, updatedConfig.MCPCodeModeBindingLevel); err != nil {
			logger.Warn(fmt.Sprintf("failed to update mcp tool manager config: %v", err))
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to update mcp tool manager config: %v", err))
			return
		}
	}

	if !slices.Equal(payload.ClientConfig.PrometheusLabels, currentConfig.PrometheusLabels) {
		updatedConfig.PrometheusLabels = payload.ClientConfig.PrometheusLabels
		restartReasons = append(restartReasons, "Prometheus labels")
	}

	if !slices.Equal(payload.ClientConfig.AllowedOrigins, currentConfig.AllowedOrigins) {
		updatedConfig.AllowedOrigins = payload.ClientConfig.AllowedOrigins
		restartReasons = append(restartReasons, "Allowed origins")
	}

	if !slices.Equal(payload.ClientConfig.AllowedHeaders, currentConfig.AllowedHeaders) {
		updatedConfig.AllowedHeaders = payload.ClientConfig.AllowedHeaders
		restartReasons = append(restartReasons, "Allowed headers")
	}

	if payload.ClientConfig.InitialPoolSize != currentConfig.InitialPoolSize {
		restartReasons = append(restartReasons, "Initial pool size")
	}
	updatedConfig.InitialPoolSize = payload.ClientConfig.InitialPoolSize

	if payload.ClientConfig.EnableLogging != currentConfig.EnableLogging {
		restartReasons = append(restartReasons, "Logging enabled")
	}
	updatedConfig.EnableLogging = payload.ClientConfig.EnableLogging

	if payload.ClientConfig.DisableContentLogging != currentConfig.DisableContentLogging {
		restartReasons = append(restartReasons, "Content logging")
	}
	updatedConfig.DisableContentLogging = payload.ClientConfig.DisableContentLogging
	updatedConfig.DisableDBPingsInHealth = payload.ClientConfig.DisableDBPingsInHealth

	if payload.ClientConfig.EnableGovernance != currentConfig.EnableGovernance {
		restartReasons = append(restartReasons, "Governance enabled")
	}
	updatedConfig.EnableGovernance = payload.ClientConfig.EnableGovernance

	updatedConfig.EnforceGovernanceHeader = payload.ClientConfig.EnforceGovernanceHeader
	updatedConfig.AllowDirectKeys = payload.ClientConfig.AllowDirectKeys

	if payload.ClientConfig.MaxRequestBodySizeMB != currentConfig.MaxRequestBodySizeMB {
		restartReasons = append(restartReasons, "Max request body size")
	}
	updatedConfig.MaxRequestBodySizeMB = payload.ClientConfig.MaxRequestBodySizeMB

	// Handle LiteLLM compat plugin toggle
	if payload.ClientConfig.EnableLiteLLMFallbacks != currentConfig.EnableLiteLLMFallbacks {
		if payload.ClientConfig.EnableLiteLLMFallbacks {
			// Load and register the litellmcompat plugin
			if err := h.configManager.ReloadPlugin(ctx, "litellmcompat", nil, &litellmcompat.Config{Enabled: true}); err != nil {
				logger.Warn(fmt.Sprintf("failed to load litellmcompat plugin: %v", err))
			}
		} else {
			// Remove the litellmcompat plugin
			disabledCtx := context.WithValue(ctx, "isDisabled", true)
			if err := h.configManager.RemovePlugin(disabledCtx, "litellmcompat"); err != nil {
				logger.Warn("failed to remove litellmcompat plugin: %v", err)
			}
		}
	}
	updatedConfig.EnableLiteLLMFallbacks = payload.ClientConfig.EnableLiteLLMFallbacks
	updatedConfig.MCPAgentDepth = payload.ClientConfig.MCPAgentDepth
	updatedConfig.MCPToolExecutionTimeout = payload.ClientConfig.MCPToolExecutionTimeout
	// Only update MCPCodeModeBindingLevel if payload is non-empty to avoid clearing stored value
	if payload.ClientConfig.MCPCodeModeBindingLevel != "" {
		updatedConfig.MCPCodeModeBindingLevel = payload.ClientConfig.MCPCodeModeBindingLevel
	}

	// Handle HeaderFilterConfig changes
	if !headerFilterConfigEqual(payload.ClientConfig.HeaderFilterConfig, currentConfig.HeaderFilterConfig) {
		// Validate that no security headers are in the allowlist or denylist
		if err := validateHeaderFilterConfig(payload.ClientConfig.HeaderFilterConfig); err != nil {
			logger.Warn("invalid header filter config: %v", err)
			SendError(ctx, fasthttp.StatusBadRequest, err.Error())
			return
		}
		updatedConfig.HeaderFilterConfig = payload.ClientConfig.HeaderFilterConfig
		if err := h.configManager.ReloadHeaderFilterConfig(ctx, payload.ClientConfig.HeaderFilterConfig); err != nil {
			logger.Warn("failed to reload header filter config: %v", err)
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to reload header filter config: %v", err))
			return
		}
	}

	// Validate LogRetentionDays
	if payload.ClientConfig.LogRetentionDays < 1 {
		logger.Warn("log_retention_days must be at least 1")
		SendError(ctx, fasthttp.StatusBadRequest, "log_retention_days must be at least 1")
		return
	}
	updatedConfig.LogRetentionDays = payload.ClientConfig.LogRetentionDays

	// Update the store with the new config
	h.store.ClientConfig = updatedConfig

	if err := h.store.ConfigStore.UpdateClientConfig(ctx, &updatedConfig); err != nil {
		logger.Warn("failed to save configuration: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to save configuration: %v", err))
		return
	}
	// Reloading client config from config store
	if err := h.configManager.ReloadClientConfigFromConfigStore(ctx); err != nil {
		logger.Warn("failed to reload client config from config store: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to reload client config from config store: %v", err))
		return
	}
	// Fetching existing framework config
	frameworkConfig, err := h.store.ConfigStore.GetFrameworkConfig(ctx)
	if err != nil {
		logger.Warn("failed to get framework config from store: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to get framework config from store: %v", err))
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
			logger.Warn("failed to check the accessibility of the pricing URL: %v", err)
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to check the accessibility of the pricing URL: %v", err))
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			logger.Warn("failed to check the accessibility of the pricing URL: %v", resp.StatusCode)
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to check the accessibility of the pricing URL: %v", resp.StatusCode))
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
	// Reload config if required
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
			logger.Warn("failed to save framework configuration: %v", err)
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to save framework configuration: %v", err))
			return
		}
		// Reloading pricing manager
		h.configManager.ReloadPricingManager(ctx)
	}
	// Checking auth config and trying to update if required
	if payload.AuthConfig != nil {
		// Getting current governance config
		authConfig, err := h.store.ConfigStore.GetAuthConfig(ctx)
		if err != nil {
			if !errors.Is(err, configstore.ErrNotFound) {
				logger.Warn("failed to get auth config from store: %v", err)
				SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to get auth config from store: %v", err))
				return
			}
		}

		// Check if auth config has changed
		authChanged := false
		if authConfig == nil {
			// No existing config, any enabled state is a change
			if payload.AuthConfig.IsEnabled {
				authChanged = true
			}
		} else {
			// Compare with existing config
			if payload.AuthConfig.IsEnabled != authConfig.IsEnabled ||
				payload.AuthConfig.AdminUserName != authConfig.AdminUserName ||
				(payload.AuthConfig.AdminPassword != "<redacted>" && payload.AuthConfig.AdminPassword != "") {
				authChanged = true
			}
		}

		if payload.AuthConfig.IsEnabled {
			if authConfig == nil && (payload.AuthConfig.AdminUserName == "" || payload.AuthConfig.AdminPassword == "") {
				SendError(ctx, fasthttp.StatusBadRequest, "auth username and password must be provided")
				return
			}
			// Fetching current Auth config
			if payload.AuthConfig.AdminUserName != "" {
				if payload.AuthConfig.AdminPassword == "<redacted>" {
					if authConfig == nil || authConfig.AdminPassword == "" {
						SendError(ctx, fasthttp.StatusBadRequest, "auth password must be provided")
						return
					}
					// Assuming that password hasn't been changed
					payload.AuthConfig.AdminPassword = authConfig.AdminPassword
				} else {
					// Password has been changed
					// We will hash the password
					hashedPassword, err := encrypt.Hash(payload.AuthConfig.AdminPassword)
					if err != nil {
						logger.Warn("failed to hash password: %v", err)
						SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to hash password: %v", err))
						return
					}
					payload.AuthConfig.AdminPassword = string(hashedPassword)
				}
			}
			// Save auth config - this handles both first-time creation and updates
			err = h.configManager.UpdateAuthConfig(ctx, payload.AuthConfig)
			if err != nil {
				logger.Warn("failed to update auth config: %v", err)
				SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to update auth config: %v", err))
				return
			}
		} else if authConfig != nil {
			// Auth is being disabled but there's an existing config - preserve credentials and update disabled state
			if payload.AuthConfig.AdminPassword == "<redacted>" || payload.AuthConfig.AdminPassword == "" {
				payload.AuthConfig.AdminPassword = authConfig.AdminPassword
			}
			if payload.AuthConfig.AdminUserName == "" {
				payload.AuthConfig.AdminUserName = authConfig.AdminUserName
			}
			err = h.configManager.UpdateAuthConfig(ctx, payload.AuthConfig)
			if err != nil {
				logger.Warn("failed to update auth config: %v", err)
				SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to update auth config: %v", err))
				return
			}
		}

		// Flush all existing sessions if auth details have been changed
		if authChanged {
			if err := h.store.ConfigStore.FlushSessions(ctx); err != nil {
				logger.Warn("updated auth config but failed to flush existing sessions, please restart the server: %v", err)
				SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("updated auth config but failed to flush existing sessions, please restart the server: %v", err))
				return
			}
		}
		// Note: AuthMiddleware is updated via ServerCallbacks.UpdateAuthConfig (handled by BifrostHTTPServer)
	}

	// Set restart required flag if any restart-requiring configs changed
	if len(restartReasons) > 0 {
		reason := fmt.Sprintf("%s settings have been updated. A restart is required for changes to take full effect.", strings.Join(restartReasons, ", "))
		if err := h.store.ConfigStore.SetRestartRequiredConfig(ctx, &configstoreTables.RestartRequiredConfig{
			Required: true,
			Reason:   reason,
		}); err != nil {
			logger.Warn("failed to set restart required config: %v", err)
		}
	}

	ctx.SetStatusCode(fasthttp.StatusOK)
	SendJSON(ctx, map[string]any{
		"status":  "success",
		"message": "configuration updated successfully",
	})
}

// forceSyncPricing triggers an immediate pricing sync and resets the pricing sync timer
func (h *ConfigHandler) forceSyncPricing(ctx *fasthttp.RequestCtx) {
	if h.store.ConfigStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "config store not available")
		return
	}

	if err := h.configManager.ForceReloadPricing(ctx); err != nil {
		logger.Warn("failed to force pricing sync: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to force pricing sync: %v", err))
		return
	}

	ctx.SetStatusCode(fasthttp.StatusOK)
	SendJSON(ctx, map[string]any{
		"status":  "success",
		"message": "pricing sync triggered",
	})
}

// getProxyConfig handles GET /api/proxy-config - Get the current proxy configuration
func (h *ConfigHandler) getProxyConfig(ctx *fasthttp.RequestCtx) {
	if h.store.ConfigStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "config store not available")
		return
	}
	proxyConfig, err := h.store.ConfigStore.GetProxyConfig(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to get proxy config: %v", err))
		return
	}
	if proxyConfig == nil {
		// Return default empty config
		SendJSON(ctx, configstoreTables.GlobalProxyConfig{
			Enabled: false,
			Type:    network.GlobalProxyTypeHTTP,
		})
		return
	}
	// Redact password if present
	if proxyConfig.Password != "" {
		proxyConfig.Password = "<redacted>"
	}
	SendJSON(ctx, proxyConfig)
}

// updateProxyConfig handles PUT /api/proxy-config - Update the proxy configuration
func (h *ConfigHandler) updateProxyConfig(ctx *fasthttp.RequestCtx) {
	if h.store.ConfigStore == nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "config store not initialized")
		return
	}

	var payload configstoreTables.GlobalProxyConfig
	if err := json.Unmarshal(ctx.PostBody(), &payload); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("invalid request format: %v", err))
		return
	}

	// Validate proxy config
	if payload.Enabled {
		// Validate proxy type
		switch payload.Type {
		case network.GlobalProxyTypeHTTP:
			// HTTP proxy is supported
			// Make sure the URL is provided
			if payload.URL == "" {
				SendError(ctx, fasthttp.StatusBadRequest, "proxy URL is required when proxy is enabled")
				return
			}
			// Validate timeout if provided
			if payload.Timeout < 0 {
				SendError(ctx, fasthttp.StatusBadRequest, "proxy timeout must be non-negative")
				return
			}
		case network.GlobalProxyTypeSOCKS5, network.GlobalProxyTypeTCP:
			SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("proxy type %s is not yet supported", payload.Type))
			return
		default:
			SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("invalid proxy type: %s", payload.Type))
			return
		}

		// Validate URL is provided when enabled
		if payload.URL == "" {
			SendError(ctx, fasthttp.StatusBadRequest, "proxy URL is required when proxy is enabled")
			return
		}

		// Validate timeout if provided
		if payload.Timeout < 0 {
			SendError(ctx, fasthttp.StatusBadRequest, "proxy timeout must be non-negative")
			return
		}
	}

	// Handle password - if it's "<redacted>", keep the existing password
	if payload.Password == "<redacted>" {
		existingConfig, err := h.store.ConfigStore.GetProxyConfig(ctx)
		if err != nil && !errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to get existing proxy config: %v", err))
			return
		}
		if existingConfig != nil {
			payload.Password = existingConfig.Password
		} else {
			payload.Password = ""
		}
	}

	// Save proxy config
	if err := h.store.ConfigStore.UpdateProxyConfig(ctx, &payload); err != nil {
		logger.Warn("failed to save proxy configuration: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to save proxy configuration: %v", err))
		return
	}

	// Pulling the proxy config from the config store
	newProxyConfig, err := h.store.ConfigStore.GetProxyConfig(ctx)
	if err != nil {
		logger.Warn("failed to get proxy config from store: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to get proxy config from store: %v", err))
		return
	}
	if newProxyConfig == nil {
		newProxyConfig = &configstoreTables.GlobalProxyConfig{
			Enabled:       false,
			Type:          network.GlobalProxyTypeHTTP,
			URL:           "",
			Username:      "",
			Password:      "",
			NoProxy:       "",
			Timeout:       0,
			SkipTLSVerify: false,
		}
	}

	// Reload proxy config in the server
	if err := h.configManager.ReloadProxyConfig(ctx, newProxyConfig); err != nil {
		logger.Warn("failed to reload proxy config: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to reload proxy config: %v", err))
		return
	}

	// Set restart required flag for proxy config changes
	if err := h.store.ConfigStore.SetRestartRequiredConfig(ctx, &configstoreTables.RestartRequiredConfig{
		Required: true,
		Reason:   "Proxy configuration has been updated. A restart is required for all changes to take full effect.",
	}); err != nil {
		logger.Warn("failed to set restart required config: %v", err)
	}

	ctx.SetStatusCode(fasthttp.StatusOK)
	SendJSON(ctx, map[string]any{
		"status":  "success",
		"message": "proxy configuration updated successfully",
	})
}

// headerFilterConfigEqual compares two GlobalHeaderFilterConfig for equality
func headerFilterConfigEqual(a, b *configstoreTables.GlobalHeaderFilterConfig) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return slices.Equal(a.Allowlist, b.Allowlist) && slices.Equal(a.Denylist, b.Denylist)
}

// validateHeaderFilterConfig validates that no security headers are in the allowlist or denylist
// Returns an error if any security headers are found
func validateHeaderFilterConfig(config *configstoreTables.GlobalHeaderFilterConfig) error {
	if config == nil {
		return nil
	}

	var foundSecurityHeaders []string

	// Check allowlist for security headers
	for _, header := range config.Allowlist {
		headerLower := strings.ToLower(strings.TrimSpace(header))
		if slices.Contains(securityHeaders, headerLower) {
			foundSecurityHeaders = append(foundSecurityHeaders, headerLower)
		}
	}

	// Check denylist for security headers
	for _, header := range config.Denylist {
		headerLower := strings.ToLower(strings.TrimSpace(header))
		if slices.Contains(securityHeaders, headerLower) && !slices.Contains(foundSecurityHeaders, headerLower) {
			foundSecurityHeaders = append(foundSecurityHeaders, headerLower)
		}
	}

	if len(foundSecurityHeaders) > 0 {
		return fmt.Errorf("the following headers are not allowed to be configured: %s. These headers are security headers and are always blocked", strings.Join(foundSecurityHeaders, ", "))
	}

	return nil
}
