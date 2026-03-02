// Package server provides the HTTP server for Bifrost.
package server

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/fasthttp/router"
	"github.com/google/uuid"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	dynamicPlugins "github.com/maximhq/bifrost/framework/plugins"
	"github.com/maximhq/bifrost/framework/tracing"
	"github.com/maximhq/bifrost/plugins/governance"
	"github.com/maximhq/bifrost/plugins/logging"
	"github.com/maximhq/bifrost/plugins/semanticcache"
	"github.com/maximhq/bifrost/plugins/telemetry"
	"github.com/maximhq/bifrost/transports/bifrost-http/handlers"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/fasthttpadaptor"
)

// Constants
const (
	DefaultHost           = "localhost"
	DefaultPort           = "8080"
	DefaultAppDir         = "" // Empty string means use OS-specific config directory
	DefaultLogLevel       = string(schemas.LogLevelInfo)
	DefaultLogOutputStyle = string(schemas.LoggerOutputTypeJSON)
)

var enterprisePlugins = []string{
	"datadog",
}

// ServerCallbacks is a interface that defines the callbacks for the server.
type ServerCallbacks interface {
	// Plugins callbacks
	ReloadPlugin(ctx context.Context, name string, path *string, pluginConfig any) error
	RemovePlugin(ctx context.Context, name string) error
	GetPluginStatus(ctx context.Context) map[string]schemas.PluginStatus
	// Auth related callbacks
	UpdateAuthConfig(ctx context.Context, authConfig *configstore.AuthConfig) error
	ReloadClientConfigFromConfigStore(ctx context.Context) error
	// Pricing related callbacks
	ReloadPricingManager(ctx context.Context) error
	ForceReloadPricing(ctx context.Context) error
	// Proxy related callbacks
	ReloadProxyConfig(ctx context.Context, config *tables.GlobalProxyConfig) error
	// Client config related callbacks
	ReloadHeaderFilterConfig(ctx context.Context, config *tables.GlobalHeaderFilterConfig) error
	UpdateDropExcessRequests(ctx context.Context, value bool)
	// Governance related callbacks
	GetGovernanceData() *governance.GovernanceData
	ReloadTeam(ctx context.Context, id string) (*tables.TableTeam, error)
	RemoveTeam(ctx context.Context, id string) error
	ReloadCustomer(ctx context.Context, id string) (*tables.TableCustomer, error)
	RemoveCustomer(ctx context.Context, id string) error
	// Virtual key related callbacks
	ReloadVirtualKey(ctx context.Context, id string) (*tables.TableVirtualKey, error)
	RemoveVirtualKey(ctx context.Context, id string) error
	// Provider related callbacks
	GetModelsForProvider(provider schemas.ModelProvider) []string
	GetUnfilteredModelsForProvider(provider schemas.ModelProvider) []string
	ReloadModelConfig(ctx context.Context, id string) (*tables.TableModelConfig, error)
	RemoveModelConfig(ctx context.Context, id string) error
	ReloadProvider(ctx context.Context, provider schemas.ModelProvider) (*tables.TableProvider, error)
	RemoveProvider(ctx context.Context, provider schemas.ModelProvider) error
	ReloadRoutingRule(ctx context.Context, id string) error
	RemoveRoutingRule(ctx context.Context, id string) error
	// MCP related callbacks
	AddMCPClient(ctx context.Context, clientConfig *schemas.MCPClientConfig) error
	RemoveMCPClient(ctx context.Context, id string) error
	UpdateMCPClient(ctx context.Context, id string, updatedConfig *schemas.MCPClientConfig) error
	UpdateMCPToolManagerConfig(ctx context.Context, maxAgentDepth int, toolExecutionTimeoutInSeconds int, codeModeBindingLevel string) error
	ReconnectMCPClient(ctx context.Context, id string) error
	// Logging related callbacks
	NewLogEntryAdded(ctx context.Context, logEntry *logstore.Log) error
}

// BifrostHTTPServer represents a HTTP server instance.
type BifrostHTTPServer struct {
	Ctx    *schemas.BifrostContext
	cancel context.CancelFunc

	Version   string
	UIContent embed.FS

	Port   string
	Host   string
	AppDir string

	LogLevel        string
	LogOutputStyle  string
	LogsCleaner     *logstore.LogsCleaner
	AsyncJobCleaner *logstore.AsyncJobCleaner

	Client *bifrost.Bifrost
	Config *lib.Config

	Server *fasthttp.Server
	Router *router.Router

	WebSocketHandler *handlers.WebSocketHandler
	MCPServerHandler *handlers.MCPServerHandler
	devPprofHandler  *handlers.DevPprofHandler

	AuthMiddleware    *handlers.AuthMiddleware
	TracingMiddleware *handlers.TracingMiddleware
}

var logger schemas.Logger

// SetLogger sets the logger for the server.
func SetLogger(l schemas.Logger) {
	logger = l
}

// NewBifrostHTTPServer creates a new instance of BifrostHTTPServer.
func NewBifrostHTTPServer(version string, uiContent embed.FS) *BifrostHTTPServer {
	return &BifrostHTTPServer{
		Version:        version,
		UIContent:      uiContent,
		Port:           DefaultPort,
		Host:           DefaultHost,
		AppDir:         DefaultAppDir,
		LogLevel:       DefaultLogLevel,
		LogOutputStyle: DefaultLogOutputStyle,
	}
}

type GovernanceInMemoryStore struct {
	Config *lib.Config
}

func (s *GovernanceInMemoryStore) GetConfiguredProviders() map[schemas.ModelProvider]configstore.ProviderConfig {
	// Use read lock for thread-safe access - no need to copy on hot path
	s.Config.Mu.RLock()
	defer s.Config.Mu.RUnlock()
	return s.Config.Providers
}

// AddMCPClient adds a new MCP client to the in-memory store
func (s *BifrostHTTPServer) AddMCPClient(ctx context.Context, clientConfig *schemas.MCPClientConfig) error {
	if err := s.Config.AddMCPClient(ctx, clientConfig); err != nil {
		return err
	}
	if err := s.MCPServerHandler.SyncAllMCPServers(ctx); err != nil {
		logger.Warn("failed to sync MCP servers after adding client: %v", err)
	}
	return nil
}

// ReconnectMCPClient reconnects an MCP client to the in-memory store
func (s *BifrostHTTPServer) ReconnectMCPClient(ctx context.Context, id string) error {
	// Check if client is registered in Bifrost (can be not registered if client initialization failed)
	if clients, err := s.Client.GetMCPClients(); err == nil && len(clients) > 0 {
		for _, client := range clients {
			if client.Config.ID == id {
				if err := s.Client.ReconnectMCPClient(id); err != nil {
					return err
				}
				return nil
			}
		}
	}
	// Config exists in store, but not in Bifrost (can happen if client initialization failed)
	clientConfig, err := s.Config.GetMCPClient(id)
	if err != nil {
		return err
	}
	if err := s.Client.AddMCPClient(clientConfig); err != nil {
		return err
	}
	if err := s.MCPServerHandler.SyncAllMCPServers(ctx); err != nil {
		logger.Warn("failed to sync MCP servers after adding client: %v", err)
	}
	return nil
}

// UpdateMCPClient updates an MCP client in the in-memory store
func (s *BifrostHTTPServer) UpdateMCPClient(ctx context.Context, id string, updatedConfig *schemas.MCPClientConfig) error {
	if err := s.Config.UpdateMCPClient(ctx, id, updatedConfig); err != nil {
		return err
	}
	if err := s.MCPServerHandler.SyncAllMCPServers(ctx); err != nil {
		logger.Warn("failed to sync MCP servers after editing client: %v", err)
	}
	return nil
}

// NewLogEntryAdded broadcasts a new log entry to the websocket clients
func (s *BifrostHTTPServer) NewLogEntryAdded(_ context.Context, logEntry *logstore.Log) error {
	if s.WebSocketHandler == nil {
		return nil
	}
	s.WebSocketHandler.BroadcastLogUpdate(logEntry)
	return nil
}

// RemoveMCPClient removes an MCP client from the in-memory store
func (s *BifrostHTTPServer) RemoveMCPClient(ctx context.Context, id string) error {
	if err := s.Config.RemoveMCPClient(ctx, id); err != nil {
		return err
	}
	if err := s.MCPServerHandler.SyncAllMCPServers(ctx); err != nil {
		logger.Warn("failed to sync MCP servers after removing client: %v", err)
	}
	return nil
}

// ExecuteChatMCPTool executes an MCP tool call and returns the result as a chat message.
func (s *BifrostHTTPServer) ExecuteChatMCPTool(ctx context.Context, toolCall *schemas.ChatAssistantMessageToolCall) (*schemas.ChatMessage, *schemas.BifrostError) {
	bifrostCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
	return s.Client.ExecuteChatMCPTool(bifrostCtx, toolCall)
}

// ExecuteResponsesMCPTool executes an MCP tool call and returns the result as a responses message.
func (s *BifrostHTTPServer) ExecuteResponsesMCPTool(ctx context.Context, toolCall *schemas.ResponsesToolMessage) (*schemas.ResponsesMessage, *schemas.BifrostError) {
	bifrostCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
	return s.Client.ExecuteResponsesMCPTool(bifrostCtx, toolCall)
}

func (s *BifrostHTTPServer) GetAvailableMCPTools(ctx context.Context) []schemas.ChatTool {
	return s.Client.GetAvailableMCPTools(ctx)
}

// markPluginDisabled marks a plugin as disabled in the plugin status
func (s *BifrostHTTPServer) markPluginDisabled(name string) error {
	return s.Config.UpdatePluginStatus(name, schemas.PluginStatusDisabled)
}

// getGovernancePluginName returns the governance plugin name from context or default
func (s *BifrostHTTPServer) getGovernancePluginName() string {
	if name, ok := s.Ctx.Value(schemas.BifrostContextKeyGovernancePluginName).(string); ok && name != "" {
		return name
	}
	return governance.PluginName
}

// getGovernancePlugin safely retrieves the governance plugin with proper locking.
// It acquires a read lock, finds the plugin, releases the lock, performs type assertion,
// and returns the BaseGovernancePlugin implementation or an error.
func (s *BifrostHTTPServer) getGovernancePlugin() (governance.BaseGovernancePlugin, error) {
	// Use type-safe finder from Config
	return lib.FindPluginAs[governance.BaseGovernancePlugin](s.Config, s.getGovernancePluginName())
}

// ReloadVirtualKey reloads a virtual key from the in-memory store
func (s *BifrostHTTPServer) ReloadVirtualKey(ctx context.Context, id string) (*tables.TableVirtualKey, error) {
	// Load relationships for response
	preloadedVk, err := s.Config.ConfigStore.RetryOnNotFound(ctx, func(ctx context.Context) (any, error) {
		preloadedVk, err := s.Config.ConfigStore.GetVirtualKey(ctx, id)
		if err != nil {
			return nil, err
		}
		return preloadedVk, nil
	}, lib.DBLookupMaxRetries, lib.DBLookupDelay)
	if err != nil {
		logger.Error("failed to load virtual key: %v", err)
		return nil, err
	}
	if preloadedVk == nil {
		logger.Error("virtual key not found")
		return nil, fmt.Errorf("virtual key not found")
	}
	// Type assertion (should never happen)
	virtualKey, ok := preloadedVk.(*tables.TableVirtualKey)
	if !ok {
		logger.Error("virtual key type assertion failed")
		return nil, fmt.Errorf("virtual key type assertion failed")
	}
	governancePlugin, err := s.getGovernancePlugin()
	if err != nil {
		return nil, err
	}
	governancePlugin.GetGovernanceStore().UpdateVirtualKeyInMemory(virtualKey, nil, nil, nil)
	s.MCPServerHandler.SyncVKMCPServer(virtualKey)
	return virtualKey, nil
}

// RemoveVirtualKey removes a virtual key from the in-memory store
func (s *BifrostHTTPServer) RemoveVirtualKey(ctx context.Context, id string) error {
	governancePlugin, err := s.getGovernancePlugin()
	if err != nil {
		return err
	}
	preloadedVk, err := s.Config.ConfigStore.GetVirtualKey(ctx, id)
	if err != nil {
		if !errors.Is(err, configstore.ErrNotFound) {
			return err
		}
	}
	if preloadedVk == nil {
		// This could be broadcast message from other server, so we will just clean up in-memory store
		governancePlugin.GetGovernanceStore().DeleteVirtualKeyInMemory(id)
		return nil
	}
	governancePlugin.GetGovernanceStore().DeleteVirtualKeyInMemory(id)
	s.MCPServerHandler.DeleteVKMCPServer(preloadedVk.Value)
	return nil
}

// ReloadTeam reloads a team from the in-memory store
func (s *BifrostHTTPServer) ReloadTeam(ctx context.Context, id string) (*tables.TableTeam, error) {
	// Load relationships for response
	preloadedTeam, err := s.Config.ConfigStore.GetTeam(ctx, id)
	if err != nil {
		logger.Error("failed to load relationships for created team: %v", err)
		return nil, err
	}
	governancePlugin, err := s.getGovernancePlugin()
	if err != nil {
		return nil, err
	}
	// Add to in-memory store
	governancePlugin.GetGovernanceStore().UpdateTeamInMemory(preloadedTeam, nil)
	return preloadedTeam, nil
}

// RemoveTeam removes a team from the in-memory store
func (s *BifrostHTTPServer) RemoveTeam(ctx context.Context, id string) error {
	governancePlugin, err := s.getGovernancePlugin()
	if err != nil {
		return err
	}
	preloadedTeam, err := s.Config.ConfigStore.GetTeam(ctx, id)
	if err != nil {
		if !errors.Is(err, configstore.ErrNotFound) {
			return err
		}
	}
	if preloadedTeam == nil {
		// At-least deleting from in-memory store to avoid conflicts
		governancePlugin.GetGovernanceStore().DeleteTeamInMemory(id)
		return nil
	}
	governancePlugin.GetGovernanceStore().DeleteTeamInMemory(id)
	return nil
}

// ReloadCustomer reloads a customer from the in-memory store
func (s *BifrostHTTPServer) ReloadCustomer(ctx context.Context, id string) (*tables.TableCustomer, error) {
	preloadedCustomer, err := s.Config.ConfigStore.GetCustomer(ctx, id)
	if err != nil {
		return nil, err
	}
	governancePlugin, err := s.getGovernancePlugin()
	if err != nil {
		return nil, err
	}
	// Add to in-memory store
	governancePlugin.GetGovernanceStore().UpdateCustomerInMemory(preloadedCustomer, nil)
	return preloadedCustomer, nil
}

// RemoveCustomer removes a customer from the in-memory store
func (s *BifrostHTTPServer) RemoveCustomer(ctx context.Context, id string) error {
	governancePlugin, err := s.getGovernancePlugin()
	if err != nil {
		return err
	}
	preloadedCustomer, err := s.Config.ConfigStore.GetCustomer(ctx, id)
	if err != nil {
		if !errors.Is(err, configstore.ErrNotFound) {
			return err
		}
	}
	if preloadedCustomer == nil {
		// At-least deleting from in-memory store to avoid conflicts
		governancePlugin.GetGovernanceStore().DeleteCustomerInMemory(id)
		return nil
	}
	governancePlugin.GetGovernanceStore().DeleteCustomerInMemory(id)
	return nil
}

// ReloadModelConfig reloads a model config from the database into in-memory store
// If usage was modified (e.g., reset due to config change), syncs it back to DB
func (s *BifrostHTTPServer) ReloadModelConfig(ctx context.Context, id string) (*tables.TableModelConfig, error) {
	preloadedMC, err := s.Config.ConfigStore.GetModelConfigByID(ctx, id)
	if err != nil {
		logger.Error("failed to load model config: %v", err)
		return nil, err
	}
	governancePlugin, err := s.getGovernancePlugin()
	if err != nil {
		return nil, err
	}
	// Update in memory and get back the potentially modified model config
	updatedMC := governancePlugin.GetGovernanceStore().UpdateModelConfigInMemory(preloadedMC)
	if updatedMC == nil {
		return preloadedMC, nil
	}

	// Sync updated usage values back to database if they changed
	if updatedMC.Budget != nil && preloadedMC.Budget != nil {
		if updatedMC.Budget.CurrentUsage != preloadedMC.Budget.CurrentUsage {
			if err := s.Config.ConfigStore.UpdateBudgetUsage(ctx, updatedMC.Budget.ID, updatedMC.Budget.CurrentUsage); err != nil {
				logger.Error("failed to sync budget usage to database: %v", err)
			}
		}
	}
	if updatedMC.RateLimit != nil && preloadedMC.RateLimit != nil {
		tokenUsageChanged := updatedMC.RateLimit.TokenCurrentUsage != preloadedMC.RateLimit.TokenCurrentUsage
		requestUsageChanged := updatedMC.RateLimit.RequestCurrentUsage != preloadedMC.RateLimit.RequestCurrentUsage
		if tokenUsageChanged || requestUsageChanged {
			if err := s.Config.ConfigStore.UpdateRateLimitUsage(ctx, updatedMC.RateLimit.ID, updatedMC.RateLimit.TokenCurrentUsage, updatedMC.RateLimit.RequestCurrentUsage); err != nil {
				logger.Error("failed to sync rate limit usage to database: %v", err)
			}
		}
	}

	return updatedMC, nil
}

// RemoveModelConfig removes a model config from the in-memory store
func (s *BifrostHTTPServer) RemoveModelConfig(ctx context.Context, id string) error {
	governancePlugin, err := s.getGovernancePlugin()
	if err != nil {
		return err
	}
	governancePlugin.GetGovernanceStore().DeleteModelConfigInMemory(id)
	return nil
}

func (s *BifrostHTTPServer) ReloadProvider(ctx context.Context, provider schemas.ModelProvider) (*tables.TableProvider, error) {
	if s.Config == nil || s.Config.ConfigStore == nil {
		return nil, fmt.Errorf("config store not found")
	}
	if s.Config.ModelCatalog == nil {
		return nil, fmt.Errorf("pricing manager not found")
	}
	if s.Client == nil {
		return nil, fmt.Errorf("bifrost client not found")
	}

	// Load provider from DB
	providerInfo, err := s.Config.ConfigStore.GetProvider(ctx, provider)
	if err != nil {
		logger.Error("failed to load provider: %v", err)
		return nil, err
	}

	// Initialize updatedProvider
	updatedProvider := providerInfo

	// Sync model level budgets in governance plugin (if governance is enabled)
	if s.Config.IsPluginLoaded(s.getGovernancePluginName()) {
		governancePlugin, err := s.getGovernancePlugin()
		if err != nil {
			logger.Warn("governance plugin found but failed to get: %v", err)
		} else {
			// Update in memory and get back the potentially modified provider
			govUpdated := governancePlugin.GetGovernanceStore().UpdateProviderInMemory(providerInfo)
			if govUpdated != nil {
				updatedProvider = govUpdated
			}

			// Sync updated usage values back to database if they changed
			if updatedProvider.Budget != nil && providerInfo.Budget != nil {
				if updatedProvider.Budget.CurrentUsage != providerInfo.Budget.CurrentUsage {
					if err := s.Config.ConfigStore.UpdateBudgetUsage(ctx, updatedProvider.Budget.ID, updatedProvider.Budget.CurrentUsage); err != nil {
						logger.Error("failed to sync budget usage to database: %v", err)
					}
				}
			}
			if updatedProvider.RateLimit != nil && providerInfo.RateLimit != nil {
				tokenUsageChanged := updatedProvider.RateLimit.TokenCurrentUsage != providerInfo.RateLimit.TokenCurrentUsage
				requestUsageChanged := updatedProvider.RateLimit.RequestCurrentUsage != providerInfo.RateLimit.RequestCurrentUsage
				if tokenUsageChanged || requestUsageChanged {
					if err := s.Config.ConfigStore.UpdateRateLimitUsage(ctx, updatedProvider.RateLimit.ID, updatedProvider.RateLimit.TokenCurrentUsage, updatedProvider.RateLimit.RequestCurrentUsage); err != nil {
						logger.Error("failed to sync rate limit usage to database: %v", err)
					}
				}
			}
		}
	}

	// Syncing models (this part always runs regardless of governance)
	if err := s.Config.ModelCatalog.SetProviderPricingOverrides(provider, providerInfo.PricingOverrides); err != nil {
		logger.Warn("failed to refresh pricing overrides for provider %s: %v", provider, err)
	}

	bfCtx := schemas.NewBifrostContext(ctx, time.Now().Add(15*time.Second))
	bfCtx.SetValue(schemas.BifrostContextKeySkipPluginPipeline, true)
	bfCtx.SetValue(schemas.BifrostContextKeyValidateKeys, true) // Validate keys during provider add/update
	defer bfCtx.Cancel()

	allModels, bifrostErr := s.Client.ListModelsRequest(bfCtx, &schemas.BifrostListModelsRequest{
		Provider: provider,
	})
	if allModels != nil && len(allModels.KeyStatuses) > 0 && s.Config.ConfigStore != nil {
		s.updateKeyStatus(ctx, allModels.KeyStatuses)
	}
	if bifrostErr != nil {
		if len(bifrostErr.ExtraFields.KeyStatuses) > 0 && s.Config.ConfigStore != nil {
			s.updateKeyStatus(ctx, bifrostErr.ExtraFields.KeyStatuses)
		}

		logger.Warn("failed to update provider model catalog: failed to list all models: %s. We are falling back onto the static datasheet", bifrost.GetErrorMessage(bifrostErr))
		// In case of error, we return an empty list of models, and fallback onto the static datasheet
		allModels = &schemas.BifrostListModelsResponse{
			Data: make([]schemas.Model, 0),
		}
	}
	// Getting allowed models from all provider keys
	providerKeys, err := s.Config.ConfigStore.GetKeysByProvider(ctx, string(provider))
	if err != nil {
		return nil, fmt.Errorf("failed to update provider model catalog: failed to get keys by provider: %s", err)
	}
	modelsInKeys := make([]schemas.Model, 0)
	for _, key := range providerKeys {
		for _, model := range key.Models {
			modelsInKeys = append(modelsInKeys, schemas.Model{
				ID: string(provider) + "/" + model,
			})
		}
	}
	s.Config.ModelCatalog.UpsertModelDataForProvider(provider, allModels, modelsInKeys)
	unfilteredModelData, listModelsErr := s.Client.ListModelsRequest(bfCtx, &schemas.BifrostListModelsRequest{
		Provider:   provider,
		Unfiltered: true,
	})
	if listModelsErr != nil {
		logger.Error("failed to list unfiltered models for provider %s: %v: falling back onto the static datasheet", provider, bifrost.GetErrorMessage(listModelsErr))
	} else {
		s.Config.ModelCatalog.UpsertUnfilteredModelDataForProvider(provider, unfilteredModelData)
	}
	return updatedProvider, nil
}

// RemoveProvider removes a provider from the in-memory store
func (s *BifrostHTTPServer) RemoveProvider(ctx context.Context, provider schemas.ModelProvider) error {
	err := s.Client.RemoveProvider(provider)
	if err != nil && !strings.Contains(err.Error(), "not found") {
		logger.Error("failed to remove provider from client: %v", err)
		return err
	}
	err = s.Config.RemoveProvider(ctx, provider)
	if err != nil && !errors.Is(err, lib.ErrNotFound) {
		logger.Error("failed to remove provider from config: %v. Client and config may be out of sync, please restart bifrost", err)
		return fmt.Errorf("failed to remove provider from config: %w. Client and config may be out of sync, please restart bifrost", err)
	}
	governancePlugin, err := s.getGovernancePlugin()
	if err != nil {
		return err
	}
	governancePlugin.GetGovernanceStore().DeleteProviderInMemory(string(provider))
	if s.Config == nil || s.Config.ModelCatalog == nil {
		return fmt.Errorf("pricing manager not found")
	}
	s.Config.ModelCatalog.DeleteModelDataForProvider(provider)
	s.Config.ModelCatalog.DeleteProviderPricingOverrides(provider)

	return nil
}

// GetGovernanceData returns the governance data
func (s *BifrostHTTPServer) GetGovernanceData() *governance.GovernanceData {
	// Use type-safe finder from Config
	governancePlugin, err := lib.FindPluginAs[governance.BaseGovernancePlugin](s.Config, s.getGovernancePluginName())
	if err != nil {
		return nil
	}

	return governancePlugin.GetGovernanceStore().GetGovernanceData()
}

// ReloadRoutingRule reloads a routing rule from the database into the governance store
func (s *BifrostHTTPServer) ReloadRoutingRule(ctx context.Context, id string) error {
	governancePluginName := governance.PluginName
	if name, ok := s.Ctx.Value(schemas.BifrostContextKeyGovernancePluginName).(string); ok && name != "" {
		governancePluginName = name
	}
	governancePlugin, err := lib.FindPluginAs[governance.BaseGovernancePlugin](s.Config, governancePluginName)
	if err != nil {
		return fmt.Errorf("governance plugin not found: %w", err)
	}
	// Get the governance store from the plugin
	store := governancePlugin.GetGovernanceStore()
	rule, err := s.Config.ConfigStore.GetRoutingRule(ctx, id)
	if err != nil {
		return fmt.Errorf("failed to get routing rule from config store: %w", err)
	}
	// Update the rule in the store (this updates the in-memory cache)
	if err := store.UpdateRoutingRuleInMemory(rule); err != nil {
		return fmt.Errorf("failed to update routing rule in store: %w", err)
	}
	return nil
}

// RemoveRoutingRule removes a routing rule from the governance store
func (s *BifrostHTTPServer) RemoveRoutingRule(ctx context.Context, id string) error {
	governancePluginName := governance.PluginName
	if name, ok := s.Ctx.Value(schemas.BifrostContextKeyGovernancePluginName).(string); ok && name != "" {
		governancePluginName = name
	}
	governancePlugin, err := lib.FindPluginAs[governance.BaseGovernancePlugin](s.Config, governancePluginName)
	if err != nil {
		return fmt.Errorf("governance plugin not found: %w", err)
	}
	// Get the governance store from the plugin
	store := governancePlugin.GetGovernanceStore()
	// Delete the rule from the store (this removes from in-memory cache)
	if err := store.DeleteRoutingRuleInMemory(id); err != nil {
		return fmt.Errorf("failed to delete routing rule from store: %w", err)
	}
	return nil
}

// ReloadClientConfigFromConfigStore reloads the client config from config store
func (s *BifrostHTTPServer) ReloadClientConfigFromConfigStore(ctx context.Context) error {
	if s.Config == nil || s.Config.ConfigStore == nil {
		return fmt.Errorf("config store not found")
	}
	config, err := s.Config.ConfigStore.GetClientConfig(context.Background())
	if err != nil {
		return fmt.Errorf("failed to get client config: %v", err)
	}	
	s.Config.ClientConfig = *config
	// Reloading config in bifrost client
	if s.Client != nil {
		account := lib.NewBaseAccount(s.Config)
		var mcpConfig *schemas.MCPConfig
		if s.Config.MCPConfig != nil {
			mcpConfig = s.Config.MCPConfig
		}
		s.Client.ReloadConfig(schemas.BifrostConfig{
			Account:            account,
			InitialPoolSize:    s.Config.ClientConfig.InitialPoolSize,
			DropExcessRequests: s.Config.ClientConfig.DropExcessRequests,
			LLMPlugins:         s.Config.GetLoadedLLMPlugins(),
			MCPPlugins:         s.Config.GetLoadedMCPPlugins(),
			MCPConfig:          mcpConfig,
			Logger:             logger,
		})
	}
	return nil
}

// UpdateAuthConfig updates auth config in the config store and updates the AuthMiddleware's in-memory config
func (s *BifrostHTTPServer) UpdateAuthConfig(ctx context.Context, authConfig *configstore.AuthConfig) error {
	if authConfig == nil {
		return fmt.Errorf("auth config is nil")
	}
	if s.Config == nil || s.Config.ConfigStore == nil {
		return fmt.Errorf("config store not found")
	}
	// Allow disabling auth without credentials, but require them when enabling
	if authConfig.IsEnabled && (authConfig.AdminUserName == nil || authConfig.AdminUserName.GetValue() == "" || authConfig.AdminPassword == nil || authConfig.AdminPassword.GetValue() == "") {
		return fmt.Errorf("username and password are required when auth is enabled")
	}
	// Update the config store
	if err := s.Config.ConfigStore.UpdateAuthConfig(ctx, authConfig); err != nil {
		return err
	}
	// Update the AuthMiddleware's in-memory config
	if s.AuthMiddleware != nil {
		// Fetch the updated config from the store to ensure we have the latest
		updatedAuthConfig, err := s.Config.ConfigStore.GetAuthConfig(ctx)
		if err != nil {
			logger.Warn("failed to get auth config from store after update: %v", err)
			// Still update with what we have
			s.AuthMiddleware.UpdateAuthConfig(authConfig)
		} else {
			s.AuthMiddleware.UpdateAuthConfig(updatedAuthConfig)
		}
	}
	return nil
}

// UpdateDropExcessRequests updates excess requests config
func (s *BifrostHTTPServer) UpdateDropExcessRequests(ctx context.Context, value bool) {
	if s.Config == nil {
		return
	}
	s.Client.UpdateDropExcessRequests(value)
}

// UpdateMCPToolManagerConfig updates the MCP tool manager config
func (s *BifrostHTTPServer) UpdateMCPToolManagerConfig(ctx context.Context, maxAgentDepth int, toolExecutionTimeoutInSeconds int, codeModeBindingLevel string) error {
	if s.Config == nil {
		return fmt.Errorf("config not found")
	}
	return s.Client.UpdateToolManagerConfig(maxAgentDepth, toolExecutionTimeoutInSeconds, codeModeBindingLevel)
}

// reloadObservabilityPlugins reloads all observability plugins in the tracing middleware
func (s *BifrostHTTPServer) reloadObservabilityPlugins() {
	observabilityPlugins := s.CollectObservabilityPlugins()
	// Always update the tracing middleware, even with empty slice, to clear stale plugins
	s.TracingMiddleware.SetObservabilityPlugins(observabilityPlugins)
}

// ReloadPricingManager reloads the pricing manager
func (s *BifrostHTTPServer) ReloadPricingManager(ctx context.Context) error {
	if s.Config == nil || s.Config.ModelCatalog == nil {
		return fmt.Errorf("pricing manager not found")
	}
	if s.Config.FrameworkConfig == nil || s.Config.FrameworkConfig.Pricing == nil {
		return fmt.Errorf("framework config not found")
	}
	return s.Config.ModelCatalog.ReloadPricing(ctx, s.Config.FrameworkConfig.Pricing)
}

// ForceReloadPricing triggers an immediate pricing sync and resets the sync timer
func (s *BifrostHTTPServer) ForceReloadPricing(ctx context.Context) error {
	if s.Config == nil {
		return fmt.Errorf("server config not initialized")
	}
	if s.Config.ModelCatalog == nil {
		if s.Config.FrameworkConfig == nil || s.Config.FrameworkConfig.Pricing == nil {
			return fmt.Errorf("framework pricing config not initialized")
		}
		// Create a new model catalog
		modelCatalog, err := modelcatalog.Init(ctx, s.Config.FrameworkConfig.Pricing, s.Config.ConfigStore, nil, logger)
		if err != nil {
			return fmt.Errorf("failed to initialize new model catalog: %w", err)
		}
		s.Config.ModelCatalog = modelCatalog
		for provider, providerConfig := range s.Config.Providers {
			if err := s.Config.ModelCatalog.SetProviderPricingOverrides(provider, providerConfig.PricingOverrides); err != nil {
				logger.Warn("failed to seed pricing overrides for provider %s: %v", provider, err)
			}
		}
	} else {
		if err := s.Config.ModelCatalog.ForceReloadPricing(ctx); err != nil {
			return fmt.Errorf("failed to force reload pricing: %w", err)
		}
		// Fetching keys for all providers and allowed models first
		// Based on allowed models we will set the data in the model catalog
		for provider, providerConfig := range s.Config.Providers {
			bfCtx := schemas.NewBifrostContext(ctx, time.Now().Add(15*time.Second))
			bfCtx.SetValue(schemas.BifrostContextKeySkipPluginPipeline, true)
			modelData, listModelsErr := s.Client.ListModelsRequest(bfCtx, &schemas.BifrostListModelsRequest{
				Provider: provider,
			})
			if listModelsErr != nil {
				logger.Error("failed to list models for provider %s: %v: falling back onto the static datasheet", provider, bifrost.GetErrorMessage(listModelsErr))
			}
			allowedModels := make([]schemas.Model, 0)
			for _, key := range providerConfig.Keys {
				for _, model := range key.Models {
					allowedModels = append(allowedModels, schemas.Model{
						ID: string(provider) + "/" + model,
					})
				}
			}
			s.Config.ModelCatalog.UpsertModelDataForProvider(provider, modelData, allowedModels)
			unfilteredModelData, listModelsErr := s.Client.ListModelsRequest(bfCtx, &schemas.BifrostListModelsRequest{
				Provider:   provider,
				Unfiltered: true,
			})
			if listModelsErr != nil {
				logger.Error("failed to list unfiltered models for provider %s: %v: falling back onto the static datasheet", provider, bifrost.GetErrorMessage(listModelsErr))
			} else {
				s.Config.ModelCatalog.UpsertUnfilteredModelDataForProvider(provider, unfilteredModelData)
			}
			bfCtx.Cancel()
		}
	}
	return nil
}

// ReloadProxyConfig reloads the proxy configuration
func (s *BifrostHTTPServer) ReloadProxyConfig(ctx context.Context, config *tables.GlobalProxyConfig) error {
	if s.Config == nil {
		return fmt.Errorf("config not found")
	}
	// Store the proxy config in memory for use by components that need it
	s.Config.ProxyConfig = config
	logger.Info("proxy configuration reloaded: enabled=%t, type=%s", config.Enabled, config.Type)
	return nil
}

// ReloadHeaderFilterConfig reloads the header filter configuration
func (s *BifrostHTTPServer) ReloadHeaderFilterConfig(ctx context.Context, config *tables.GlobalHeaderFilterConfig) error {
	if s.Config == nil {
		return fmt.Errorf("config not found")
	}
	// Store the header filter config in ClientConfig
	s.Config.ClientConfig.HeaderFilterConfig = config
	allowlistLen := 0
	denylistLen := 0
	if config != nil {
		allowlistLen = len(config.Allowlist)
		denylistLen = len(config.Denylist)
	}
	logger.Info("header filter configuration reloaded: allowlist=%d, denylist=%d", allowlistLen, denylistLen)
	return nil
}

// GetModelsForProvider returns all models for a specific provider from the model catalog
func (s *BifrostHTTPServer) GetModelsForProvider(provider schemas.ModelProvider) []string {
	if s.Config == nil || s.Config.ModelCatalog == nil {
		return []string{}
	}
	return s.Config.ModelCatalog.GetModelsForProvider(provider)
}

// GetUnfilteredModelsForProvider returns all unfiltered models for a specific provider from the model catalog
func (s *BifrostHTTPServer) GetUnfilteredModelsForProvider(provider schemas.ModelProvider) []string {
	if s.Config == nil || s.Config.ModelCatalog == nil {
		return []string{}
	}
	return s.Config.ModelCatalog.GetUnfilteredModelsForProvider(provider)
}

// GetPluginStatus returns the status of all plugins
// Delegates to Config for centralized plugin status management
func (s *BifrostHTTPServer) GetPluginStatus(ctx context.Context) map[string]schemas.PluginStatus {
	return s.Config.GetPluginStatus()
}

// Helper to update error status
// Uses UpdatePluginOverallStatus to create the status entry if it doesn't exist,
// ensuring plugins that were never loaded can still have their error status tracked.
// Always returns the original error so the actual failure reason is surfaced to the user.
func (s *BifrostHTTPServer) updatePluginErrorStatus(name, step string, originalErr error) error {
	logs := []string{fmt.Sprintf("error %s plugin %s: %v", step, name, originalErr)}
	s.Config.UpdatePluginOverallStatus(name, name, schemas.PluginStatusError, logs, []schemas.PluginType{})
	return originalErr
}

// SyncLoadedPlugin syncs a loaded plugin to the Bifrost client and updates the plugin status
func (s *BifrostHTTPServer) SyncLoadedPlugin(ctx context.Context, name string, plugin schemas.BasePlugin) error {
	// 2. Register (replaces old version atomically)
	if err := s.Config.ReloadPlugin(plugin); err != nil {
		return s.updatePluginErrorStatus(plugin.GetName(), "registering", err)
	}
	// 3. Update Bifrost client
	if err := s.Client.ReloadPlugin(plugin, InferPluginTypes(plugin)); err != nil {
		return s.updatePluginErrorStatus(plugin.GetName(), "reloading bifrost config for", err)
	}
	// 4. Special handling for observability plugins
	if _, ok := plugin.(schemas.ObservabilityPlugin); ok {
		s.reloadObservabilityPlugins()
	}
	// 5. Update plugin status
	s.Config.UpdatePluginOverallStatus(plugin.GetName(), name, schemas.PluginStatusActive,
		[]string{fmt.Sprintf("plugin %s reloaded successfully", name)}, InferPluginTypes(plugin))
	return nil
}

// ReloadPlugin reloads a plugin with new instance and updates Bifrost core.
// The plugin is checked for LLM and MCP interfaces independently and registered
// to the appropriate arrays based on which interfaces it implements.
func (s *BifrostHTTPServer) ReloadPlugin(ctx context.Context, name string, path *string, pluginConfig any) error {
	logger.Debug("reloading plugin %s", name)
	// 1. Instantiate new version
	plugin, err := InstantiatePlugin(ctx, name, path, pluginConfig, s.Config)
	if err != nil {
		return s.updatePluginErrorStatus(name, "loading", err)
	}
	return s.SyncLoadedPlugin(ctx, name, plugin)
}

// RemovePlugin removes a plugin from the server.
// The plugin is removed from both LLM and MCP arrays independently if it exists in them.
func (s *BifrostHTTPServer) RemovePlugin(ctx context.Context, displayName string) error {
	// Get the actual plugin name from the display name
	name, ok := s.Config.GetPluginNameByDisplayName(displayName)
	if !ok {
		return dynamicPlugins.ErrPluginNotFound
	}

	// Check if plugin implements ObservabilityPlugin before removal
	var isObservability bool
	var err error
	var plugin schemas.BasePlugin
	if plugin, err = s.Config.FindPluginByName(name); err == nil {
		_, isObservability = plugin.(schemas.ObservabilityPlugin)
	}

	// 1. Unregister from config
	if err := s.Config.UnregisterPlugin(name); err != nil {
		return err
	}

	// 2. Update Bifrost client
	if err := s.Client.RemovePlugin(name, InferPluginTypes(plugin)); err != nil {
		logger.Warn("failed to reload bifrost config after plugin removal: %v", err)
	}

	// 3. Reload observability plugins if necessary
	if isObservability {
		s.reloadObservabilityPlugins()
	}

	// 4. Update status
	if isDisabled, _ := ctx.Value(handlers.PluginDisabledKey).(bool); isDisabled {
		s.markPluginDisabled(name)
	} else {
		s.Config.DeletePluginOverallStatus(name)
	}

	return nil
}

// RegisterInferenceRoutes initializes the routes for the inference handler
func (s *BifrostHTTPServer) RegisterInferenceRoutes(ctx context.Context, middlewares ...schemas.BifrostHTTPMiddleware) error {
	inferenceHandler := handlers.NewInferenceHandler(s.Client, s.Config)
	integrationHandler := handlers.NewIntegrationHandler(s.Client, s.Config)
	mcpInferenceHandler := handlers.NewMCPInferenceHandler(s.Client, s.Config)
	mcpServerHandler, err := handlers.NewMCPServerHandler(ctx, s.Config, s)
	if err != nil {
		return fmt.Errorf("failed to initialize mcp server handler: %v", err)
	}
	s.MCPServerHandler = mcpServerHandler
	asyncHandler := handlers.NewAsyncHandler(s.Client, s.Config)
	integrationHandler.RegisterRoutes(s.Router, middlewares...)
	inferenceHandler.RegisterRoutes(s.Router, middlewares...)
	asyncHandler.RegisterRoutes(s.Router, middlewares...)
	mcpInferenceHandler.RegisterRoutes(s.Router, middlewares...)
	s.MCPServerHandler.RegisterRoutes(s.Router, middlewares...)
	return nil
}

// RegisterAPIRoutes initializes the routes for the Bifrost HTTP server.
func (s *BifrostHTTPServer) RegisterAPIRoutes(ctx context.Context, callbacks ServerCallbacks, middlewares ...schemas.BifrostHTTPMiddleware) error {
	var err error
	// Initializing plugin specific handlers
	var loggingHandler *handlers.LoggingHandler
	loggerPlugin, _ := lib.FindPluginAs[*logging.LoggerPlugin](s.Config, logging.PluginName)
	if loggerPlugin != nil {
		loggingHandler = handlers.NewLoggingHandler(loggerPlugin.GetPluginLogManager(), s)
	}
	var governanceHandler *handlers.GovernanceHandler
	governancePluginName := governance.PluginName
	if name, ok := ctx.Value(schemas.BifrostContextKeyGovernancePluginName).(string); ok && name != "" {
		governancePluginName = name
	}
	governancePlugin, _ := lib.FindPluginAs[schemas.LLMPlugin](s.Config, governancePluginName)
	if governancePlugin != nil {
		governanceHandler, err = handlers.NewGovernanceHandler(callbacks, s.Config.ConfigStore)
		if err != nil {
			return fmt.Errorf("failed to initialize governance handler: %v", err)
		}
	}
	var cacheHandler *handlers.CacheHandler
	semanticCachePlugin, _ := lib.FindPluginAs[*semanticcache.Plugin](s.Config, semanticcache.PluginName)
	if semanticCachePlugin != nil {
		cacheHandler = handlers.NewCacheHandler(semanticCachePlugin)
	}
	// Websocket handler needs to go below UI handler
	logger.Debug("initializing websocket server")
	if s.WebSocketHandler == nil {
		s.WebSocketHandler = handlers.NewWebSocketHandler(s.Ctx, s.Config.ClientConfig.AllowedOrigins)
	}
	if loggerPlugin != nil {
		loggerPlugin.SetLogCallback(func(ctx context.Context, logEntry *logstore.Log) {
			err := s.NewLogEntryAdded(ctx, logEntry)
			if err != nil {
				logger.Error("failed to add log entry: %v", err)
			}
		})
		loggerPlugin.SetMCPToolLogCallback(s.WebSocketHandler.BroadcastMCPLogUpdate)
	}
	// Start WebSocket heartbeat
	s.WebSocketHandler.StartHeartbeat()
	// Adding telemetry middleware
	// Chaining all middlewares
	// lib.ChainMiddlewares chains multiple middlewares together
	healthHandler := handlers.NewHealthHandler(s.Config)
	providerHandler := handlers.NewProviderHandler(callbacks, s.Config, s.Client)
	oauthHandler := handlers.NewOAuthHandler(s.Config.OAuthProvider, s.Client, s.Config)
	mcpHandler := handlers.NewMCPHandler(callbacks, s.Client, s.Config, oauthHandler)
	configHandler := handlers.NewConfigHandler(callbacks, s.Config)
	pluginsHandler := handlers.NewPluginsHandler(callbacks, s.Config.ConfigStore)
	sessionHandler := handlers.NewSessionHandler(s.Config.ConfigStore)
	// Going ahead with API handlers
	healthHandler.RegisterRoutes(s.Router, middlewares...)
	providerHandler.RegisterRoutes(s.Router, middlewares...)
	mcpHandler.RegisterRoutes(s.Router, middlewares...)
	configHandler.RegisterRoutes(s.Router, middlewares...)
	oauthHandler.RegisterRoutes(s.Router, middlewares...)
	if pluginsHandler != nil {
		pluginsHandler.RegisterRoutes(s.Router, middlewares...)
	}
	if sessionHandler != nil {
		sessionHandler.RegisterRoutes(s.Router, middlewares...)
	}
	if cacheHandler != nil {
		cacheHandler.RegisterRoutes(s.Router, middlewares...)
	}
	if governanceHandler != nil {
		governanceHandler.RegisterRoutes(s.Router, middlewares...)
	}
	if loggingHandler != nil {
		loggingHandler.RegisterRoutes(s.Router, middlewares...)
	}
	if s.WebSocketHandler != nil {
		s.WebSocketHandler.RegisterRoutes(s.Router, middlewares...)
	}
	// Register dev pprof handler only in dev mode
	if handlers.IsDevMode() {
		logger.Info("dev mode enabled, registering pprof endpoints")
		s.devPprofHandler = handlers.NewDevPprofHandler()
		s.devPprofHandler.RegisterRoutes(s.Router, middlewares...)
	}
	// Add Prometheus /metrics endpoint
	prometheusPlugin, err := lib.FindPluginAs[*telemetry.PrometheusPlugin](s.Config, telemetry.PluginName)
	if err == nil && prometheusPlugin.GetRegistry() != nil {
		// Use the plugin's dedicated registry if available
		metricsHandler := fasthttpadaptor.NewFastHTTPHandler(promhttp.HandlerFor(prometheusPlugin.GetRegistry(), promhttp.HandlerOpts{}))
		s.Router.GET("/metrics", metricsHandler)
	} else {
		logger.Warn("prometheus plugin not found or registry is nil, skipping metrics endpoint")
	}
	// 404 handler
	s.Router.NotFound = func(ctx *fasthttp.RequestCtx) {
		handlers.SendError(ctx, fasthttp.StatusNotFound, "Route not found: "+string(ctx.Path()))
	}
	return nil
}

// RegisterUIRoutes registers the UI handler with the specified router
func (s *BifrostHTTPServer) RegisterUIRoutes(middlewares ...schemas.BifrostHTTPMiddleware) {
	// WARNING: This UI handler needs to be registered after all the other handlers
	handlers.NewUIHandler(s.UIContent).RegisterRoutes(s.Router, middlewares...)
}

// GetAllRedactedKeys gets all redacted keys from the config store
func (s *BifrostHTTPServer) GetAllRedactedKeys(ctx context.Context, ids []string) []schemas.Key {
	if s.Config == nil || s.Config.ConfigStore == nil {
		return nil
	}
	redactedKeys, err := s.Config.ConfigStore.GetAllRedactedKeys(ctx, ids)
	if err != nil {
		logger.Error("failed to get all redacted keys: %v", err)
		return nil
	}
	return redactedKeys
}

// GetAllRedactedVirtualKeys gets all redacted virtual keys from the config store
func (s *BifrostHTTPServer) GetAllRedactedVirtualKeys(ctx context.Context, ids []string) []tables.TableVirtualKey {
	if s.Config == nil || s.Config.ConfigStore == nil {
		return nil
	}
	virtualKeys, err := s.Config.ConfigStore.GetRedactedVirtualKeys(ctx, ids)
	if err != nil {
		logger.Error("failed to get all redacted virtual keys: %v", err)
		return nil
	}
	return virtualKeys
}

// GetAllRedactedRoutingRules gets all redacted routing rules from the config store
func (s *BifrostHTTPServer) GetAllRedactedRoutingRules(ctx context.Context, ids []string) []tables.TableRoutingRule {
	if s.Config == nil || s.Config.ConfigStore == nil {
		return nil
	}
	routingRules, err := s.Config.ConfigStore.GetRedactedRoutingRules(ctx, ids)
	if err != nil {
		logger.Error("failed to get all redacted routing rules: %v", err)
		return nil
	}
	return routingRules
}

// PrepareCommonMiddlewares gets the common middlewares for the Bifrost HTTP server
func (s *BifrostHTTPServer) PrepareCommonMiddlewares() []schemas.BifrostHTTPMiddleware {
	commonMiddlewares := []schemas.BifrostHTTPMiddleware{}
	// Preparing middlewares
	// Initializing prometheus plugin
	prometheusPlugin, err := lib.FindPluginAs[*telemetry.PrometheusPlugin](s.Config, telemetry.PluginName)
	if err == nil {
		commonMiddlewares = append(commonMiddlewares, prometheusPlugin.HTTPMiddleware)
	} else {
		logger.Warn("prometheus plugin not found, skipping telemetry middleware")
	}
	return commonMiddlewares
}

// Bootstrap initializes the Bifrost HTTP server with all necessary components.
// It:
// 1. Initializes Prometheus collectors for monitoring
// 2. Reads and parses configuration from the specified config file
// 3. Initializes the Bifrost client with the configuration
// 4. Sets up HTTP routes for text and chat completions
//
// The server exposes the following endpoints:
//   - POST /v1/text/completions: For text completion requests
//   - POST /v1/chat/completions: For chat completion requests
//   - GET /metrics: For Prometheus metrics
func (s *BifrostHTTPServer) Bootstrap(ctx context.Context) error {
	var err error
	s.Ctx, s.cancel = schemas.NewBifrostContextWithCancel(ctx)
	handlers.SetVersion(s.Version)
	configDir := GetDefaultConfigDir(s.AppDir)

	// Ensure app directory exists
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return fmt.Errorf("failed to create app directory %s: %v", configDir, err)
	}
	// Initialize high-performance configuration store with dedicated database
	s.Config, err = lib.LoadConfig(ctx, configDir)
	if err != nil {
		return fmt.Errorf("failed to load config %v", err)
	}
	// Initialize WebSocket handler early so plugins can wire event broadcasters during Init.
	// Log callbacks are registered later in RegisterAPIRoutes when logging plugin is available.
	s.WebSocketHandler = handlers.NewWebSocketHandler(s.Ctx, s.Config.ClientConfig.AllowedOrigins)
	s.Config.EventBroadcaster = s.WebSocketHandler.BroadcastEvent
	// Initializing plugin loader
	s.Config.PluginLoader = &dynamicPlugins.SharedObjectPluginLoader{}
	// Initialize log retention cleaner if log store is configured
	if s.Config.LogsStore != nil {
		// If log retention days remains 0, then we wont be initializing the log retention cleaner
		logRetentionDays := 0
		if s.Config.ConfigStore != nil {
			// Get logs store config from config store
			clientConfig, err := s.Config.ConfigStore.GetClientConfig(ctx)
			if err != nil {
				logger.Warn("failed to get logs store config: %v", err)
				// So we wont be initializing the log retention cleaner
			}
			if clientConfig != nil {
				logRetentionDays = clientConfig.LogRetentionDays
			}
		} else {
			// We will check if the config file has the log retention days set
			logRetentionDays = s.Config.ClientConfig.LogRetentionDays
		}
		logger.Info("log retention days: %d", logRetentionDays)
		if logRetentionDays > 0 {
			// Type assert to get RDBLogStore (which implements LogRetentionManager)
			if rdbStore, ok := s.Config.LogsStore.(logstore.LogRetentionManager); ok {
				cleanerConfig := logstore.CleanerConfig{
					RetentionDays: logRetentionDays,
				}
				s.LogsCleaner = logstore.NewLogsCleaner(rdbStore, cleanerConfig, logger)
				s.LogsCleaner.StartCleanupRoutine()
				logger.Info("log retention cleaner initialized with %d days retention",
					logRetentionDays)
			}
		}
	}
	// Initialize async job cleaner if log store is configured
	if s.Config.LogsStore != nil {
		s.AsyncJobCleaner = logstore.NewAsyncJobCleaner(s.Config.LogsStore, logger)
		s.AsyncJobCleaner.StartCleanupRoutine()
	}
	// Load all plugins
	if err := s.LoadPlugins(ctx); err != nil {
		return fmt.Errorf("failed to instantiate plugins: %v", err)
	}

	// Initialize async job executor (requires LogsStore + governance plugin)
	if s.Config.LogsStore != nil {
		governancePlugin, govErr := lib.FindPluginAs[governance.BaseGovernancePlugin](s.Config, s.getGovernancePluginName())
		if govErr == nil {
			s.Config.AsyncJobExecutor = logstore.NewAsyncJobExecutor(s.Config.LogsStore, governancePlugin.GetGovernanceStore(), logger)
			logger.Info("async job executor initialized")
		}
	}

	tableMCPConfig := s.Config.MCPConfig
	var mcpConfig *schemas.MCPConfig
	if tableMCPConfig != nil {
		mcpConfig = s.Config.MCPConfig
		if mcpConfig != nil {
			mcpConfig.FetchNewRequestIDFunc = func(ctx *schemas.BifrostContext) string {
				return uuid.New().String()
			}
		}
	}
	// Initialize bifrost client
	// Create account backed by the high-performance store (all processing is done in LoadFromDatabase)
	// The account interface now benefits from ultra-fast config access times via in-memory storage
	account := lib.NewBaseAccount(s.Config)
	s.Client, err = bifrost.Init(ctx, schemas.BifrostConfig{
		Account:            account,
		InitialPoolSize:    s.Config.ClientConfig.InitialPoolSize,
		DropExcessRequests: s.Config.ClientConfig.DropExcessRequests,
		LLMPlugins:         s.Config.GetLoadedLLMPlugins(),
		MCPPlugins:         s.Config.GetLoadedMCPPlugins(),
		MCPConfig:          mcpConfig,
		OAuth2Provider:     s.Config.OAuthProvider,
		Logger:             logger,
	})
	if err != nil {
		return fmt.Errorf("failed to initialize bifrost: %v", err)
	}
	logger.Info("bifrost client initialized")
	// List all models and add to model catalog with per-provider status tracking
	logger.Info("listing all models and adding to model catalog")
	if s.Config.ModelCatalog != nil {
		// Fetching keys for all providers and allowed models first
		// Based on allowed models we will set the data in the model catalog
		for provider, providerConfig := range s.Config.Providers {
			bfCtx := schemas.NewBifrostContext(ctx, time.Now().Add(15*time.Second))
			bfCtx.SetValue(schemas.BifrostContextKeySkipPluginPipeline, true)

			modelData, listModelsErr := s.Client.ListModelsRequest(bfCtx, &schemas.BifrostListModelsRequest{
				Provider: provider,
			})
			if modelData != nil && len(modelData.KeyStatuses) > 0 && s.Config.ConfigStore != nil {
				s.updateKeyStatus(ctx, modelData.KeyStatuses)
			}
			if listModelsErr != nil {
				if len(listModelsErr.ExtraFields.KeyStatuses) > 0 && s.Config.ConfigStore != nil {
					s.updateKeyStatus(ctx, listModelsErr.ExtraFields.KeyStatuses)
				}
				logger.Error("failed to list models for provider %s: %v: falling back onto the static datasheet", provider, bifrost.GetErrorMessage(listModelsErr))
			}
			allowedModels := make([]schemas.Model, 0)
			for _, key := range providerConfig.Keys {
				for _, model := range key.Models {
					allowedModels = append(allowedModels, schemas.Model{
						ID: string(provider) + "/" + model,
					})
				}
			}
			s.Config.ModelCatalog.UpsertModelDataForProvider(provider, modelData, allowedModels)
			unfilteredModelData, listModelsErr := s.Client.ListModelsRequest(bfCtx, &schemas.BifrostListModelsRequest{
				Provider:   provider,
				Unfiltered: true,
			})
			if listModelsErr != nil {
				logger.Error("failed to list unfiltered models for provider %s: %v: falling back onto the static datasheet", provider, bifrost.GetErrorMessage(listModelsErr))
			} else {
				s.Config.ModelCatalog.UpsertUnfilteredModelDataForProvider(provider, unfilteredModelData)
			}
			bfCtx.Cancel()
		}
	}

	logger.Info("models added to catalog")
	s.Config.SetBifrostClient(s.Client)
	// Initialize routes
	s.Router = router.New()
	commonMiddlewares := s.PrepareCommonMiddlewares()
	apiMiddlewares := commonMiddlewares
	inferenceMiddlewares := commonMiddlewares
	if s.Config.ConfigStore == nil {
		logger.Error("auth middleware requires config store, skipping auth middleware initialization")
	} else {
		s.AuthMiddleware, err = handlers.InitAuthMiddleware(s.Config.ConfigStore)
		if err != nil {
			return fmt.Errorf("failed to initialize auth middleware: %v", err)
		}
		if ctx.Value(schemas.BifrostContextKeyIsEnterprise) == nil {
			apiMiddlewares = append(apiMiddlewares, s.AuthMiddleware.APIMiddleware())
		}
	}
	// Register routes
	err = s.RegisterAPIRoutes(s.Ctx, s, apiMiddlewares...)
	if err != nil {
		return fmt.Errorf("failed to initialize routes: %v", err)
	}
	// Registering inference routes
	if ctx.Value(schemas.BifrostContextKeyIsEnterprise) == nil && s.AuthMiddleware != nil {
		inferenceMiddlewares = append(inferenceMiddlewares, s.AuthMiddleware.InferenceMiddleware())
	}
	// Registering inference middlewares
	inferenceMiddlewares = append([]schemas.BifrostHTTPMiddleware{handlers.TransportInterceptorMiddleware(s.Config)}, inferenceMiddlewares...)
	// Curating observability plugins
	observabilityPlugins := s.CollectObservabilityPlugins()
	// This enables the central streaming accumulator for both use cases
	// Initializing tracer with embedded streaming accumulator
	traceStore := tracing.NewTraceStore(60*time.Minute, logger)
	tracer := tracing.NewTracer(traceStore, s.Config.ModelCatalog, logger)
	s.Client.SetTracer(tracer)
	// Always add tracing middleware when tracer is enabled - it creates traces and sets traceID in context
	// The observability plugins are optional (can be empty if only logging is enabled)
	s.TracingMiddleware = handlers.NewTracingMiddleware(tracer, observabilityPlugins)
	inferenceMiddlewares = append([]schemas.BifrostHTTPMiddleware{s.TracingMiddleware.Middleware()}, inferenceMiddlewares...)
	err = s.RegisterInferenceRoutes(s.Ctx, inferenceMiddlewares...)
	if err != nil {
		return fmt.Errorf("failed to initialize inference routes: %v", err)
	}
	// Register UI handler
	s.RegisterUIRoutes()
	// Create fasthttp server instance
	s.Server = &fasthttp.Server{
		Handler:            handlers.CorsMiddleware(s.Config)(s.Router.Handler),
		MaxRequestBodySize: s.Config.ClientConfig.MaxRequestBodySizeMB * 1024 * 1024,
		ReadBufferSize:     1024 * 64, // 64kb
	}
	return nil
}

// Start starts the HTTP server at the specified host and port
// Also watches signals and errors
func (s *BifrostHTTPServer) Start() error {
	// Printing plugin status in a table
	for _, pluginStatus := range s.Config.GetPluginStatus() {
		logger.Info("plugin status: %s - %s", pluginStatus.Name, pluginStatus.Status)
	}
	// Create channels for signal and error handling
	sigChan := make(chan os.Signal, 1)
	errChan := make(chan error, 1)
	// Watching for signals
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	// Start server in a goroutine
	serverAddr := net.JoinHostPort(s.Host, s.Port)
	ln, err := net.Listen("tcp", serverAddr)
	if err != nil {
		return fmt.Errorf("failed to create listener on %s: %v", serverAddr, err)
	}
	go func() {
		logger.Info("successfully started bifrost, serving UI on http://%s:%s", s.Host, s.Port)
		if err := s.Server.Serve(ln); err != nil {
			errChan <- err
		}
	}()
	// Wait for either termination signal or server error
	select {
	case sig := <-sigChan:
		logger.Info("received signal %v, initiating graceful shutdown...", sig)
		// Create shutdown context with timeout
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		// Perform graceful shutdown
		if err := s.Server.Shutdown(); err != nil {
			logger.Error("error during graceful shutdown: %v", err)
		} else {
			logger.Info("server gracefully shutdown")
		}
		// Cancelling main context
		if s.cancel != nil {
			s.cancel()
		}
		// Wait for shutdown to complete or timeout
		done := make(chan struct{})
		go func() {
			defer close(done)
			logger.Info("shutting down bifrost client...")
			s.Client.Shutdown()
			logger.Info("bifrost client shutdown completed")
			logger.Info("cleaning up storage engines...")
			// Cleaning up storage engines
			if s.Config != nil && s.Config.ModelCatalog != nil {
				s.Config.ModelCatalog.Cleanup()
			}
			if s.Config != nil && s.Config.ConfigStore != nil {
				s.Config.ConfigStore.Close(shutdownCtx)
			}
			if s.LogsCleaner != nil {
				logger.Info("stopping log retention cleaner...")
				s.LogsCleaner.StopCleanupRoutine()
			}
			if s.AsyncJobCleaner != nil {
				logger.Info("stopping async job cleaner...")
				s.AsyncJobCleaner.StopCleanupRoutine()
			}
			if s.Config != nil && s.Config.TokenRefreshWorker != nil {
				logger.Info("stopping token refresh worker...")
				s.Config.TokenRefreshWorker.Stop()
			}
			if s.devPprofHandler != nil {
				logger.Info("stopping dev pprof handler...")
				s.devPprofHandler.Cleanup()
			}
			if s.Config != nil && s.Config.LogsStore != nil {
				s.Config.LogsStore.Close(shutdownCtx)
			}
			if s.Config != nil && s.Config.VectorStore != nil {
				s.Config.VectorStore.Close(shutdownCtx, "")
			}
			logger.Info("storage engines cleanup completed")
		}()
		select {
		case <-done:
			logger.Info("cleanup completed")
		case <-shutdownCtx.Done():
			logger.Warn("cleanup timed out after 30 seconds")
		}

	case err := <-errChan:
		return err
	}
	return nil
}
