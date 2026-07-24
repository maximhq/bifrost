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
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/fasthttp/router"
	"github.com/google/uuid"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/encrypt"
	"github.com/maximhq/bifrost/framework/logstore"
	dynamicPlugins "github.com/maximhq/bifrost/framework/plugins"
	"github.com/maximhq/bifrost/framework/sidekiq"
	"github.com/maximhq/bifrost/framework/temptoken"
	"github.com/maximhq/bifrost/framework/tracing"
	"github.com/maximhq/bifrost/framework/webhooks"
	"github.com/maximhq/bifrost/plugins/governance"
	"github.com/maximhq/bifrost/plugins/governance/complexity"
	"github.com/maximhq/bifrost/plugins/logging"
	"github.com/maximhq/bifrost/plugins/otel"
	"github.com/maximhq/bifrost/plugins/prompts"
	"github.com/maximhq/bifrost/plugins/semanticcache"
	"github.com/maximhq/bifrost/plugins/telemetry"
	"github.com/maximhq/bifrost/transports/bifrost-http/handlers"
	"github.com/maximhq/bifrost/transports/bifrost-http/integrations"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	bfws "github.com/maximhq/bifrost/transports/bifrost-http/websocket"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	dto "github.com/prometheus/client_model/go"
	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/fasthttpadaptor"
	"gorm.io/gorm"
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
	"bigquery",
	"pubsub",
	"kafka",
}

// ServerCallbacks is a interface that defines the callbacks for the server.
type ServerCallbacks interface {
	// Plugins callbacks
	ReloadPlugin(ctx context.Context, name string, path *string, pluginConfig any, placement *schemas.PluginPlacement, order *int) error
	RemovePlugin(ctx context.Context, name string) error
	GetPluginStatus(ctx context.Context) map[string]schemas.PluginStatus
	GetLoadedPluginNames() []string
	NormalizePluginConfig(name string, config map[string]any) (map[string]any, error)
	ExpandPluginConfigForAPI(name string, config map[string]any) (map[string]any, error)
	// Auth related callbacks
	UpdateAuthConfig(ctx context.Context, authConfig *configstore.AuthConfig) error
	ReloadClientConfigFromConfigStore(ctx context.Context) error
	// Pricing related callbacks
	UpdateSyncConfig(ctx context.Context) error
	ForceReloadPricing(ctx context.Context) error
	UpsertPricingOverride(ctx context.Context, override *tables.TablePricingOverride) error
	DeletePricingOverride(ctx context.Context, id string) error
	// UpsertModelPricingAttributes writes the additional_attributes JSON on
	// pricing rows. Enterprise wraps this so that after the local DB write
	// succeeds it can broadcast a peer reload via the existing pricing
	// EntityTypeModelCatalog/ActionReloadFromDB gossip path.
	UpsertModelPricingAttributes(ctx context.Context, entries []handlers.ModelPricingAttributesEntry) error
	// Proxy related callbacks
	ReloadProxyConfig(ctx context.Context, config *tables.GlobalProxyConfig) error
	// Client config related callbacks
	ReloadHeaderFilterConfig(ctx context.Context, config *tables.GlobalHeaderFilterConfig) error
	UpdateDropExcessRequests(ctx context.Context, value bool)
	// Governance related callbacks
	GetGovernanceData(ctx context.Context) *governance.GovernanceData
	ResetBudget(ctx context.Context, id string) (*tables.TableBudget, error)
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
	OnKeyAdded(ctx context.Context, provider schemas.ModelProvider, key schemas.Key) error
	OnKeyUpdated(ctx context.Context, provider schemas.ModelProvider, key schemas.Key) error
	OnKeyDeleted(ctx context.Context, provider schemas.ModelProvider, keyID string) error
	ReloadRoutingRule(ctx context.Context, id string) error
	RemoveRoutingRule(ctx context.Context, id string) error
	// Webhook related callbacks
	ReloadWebhookEndpoint(ctx context.Context, id string) error
	RemoveWebhookEndpoint(ctx context.Context, id string) error
	// MCP related callbacks
	AddMCPClient(ctx context.Context, clientConfig *schemas.MCPClientConfig) error
	RemoveMCPClient(ctx context.Context, id string) error
	UpdateMCPClient(ctx context.Context, id string, updatedConfig *schemas.MCPClientConfig) error
	// UpdateMCPClientConnection reconnects an existing MCP client using updated headers
	UpdateMCPClientConnection(ctx context.Context, id string, newConfig *schemas.MCPClientConfig) error
	UpdateMCPToolManagerConfig(ctx context.Context, maxAgentDepth int, toolExecutionTimeoutInSeconds int, codeModeBindingLevel string, disableAutoToolInject bool) error
	// VerifyPerUserOAuthConnection verifies an MCP server using a temporary token and discovers tools.
	VerifyPerUserOAuthConnection(ctx context.Context, config *schemas.MCPClientConfig, accessToken string) (map[string]schemas.ChatTool, map[string]string, error)
	// VerifyHeadersConnection verifies an MCP server using user-supplied header values and discovers tools.
	VerifyHeadersConnection(ctx context.Context, config *schemas.MCPClientConfig, userHeaders map[string]string) (map[string]schemas.ChatTool, map[string]string, error)
	// SetClientTools updates the tool map for an existing client.
	SetClientTools(clientID string, tools map[string]schemas.ChatTool, toolNameMapping map[string]string)
	ReconnectMCPClient(ctx context.Context, id string) error
	DisableMCPClient(ctx context.Context, id string) error
	EnableMCPClient(ctx context.Context, id string) error
}

// LogRedactionMappingResolverProvider is implemented by servers that can attach reveal data to log-detail responses.
type LogRedactionMappingResolverProvider interface {
	// GetLogRedactionMappingResolver returns the resolver used by the logging handler.
	GetLogRedactionMappingResolver() handlers.LogRedactionMappingResolver
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

	WebSocketHandler   *handlers.WebSocketHandler
	MCPServerHandler   *handlers.MCPServerHandler
	devPprofHandler    *handlers.DevPprofHandler
	IntegrationHandler *handlers.IntegrationHandler

	AuthMiddleware       *handlers.AuthMiddleware
	CORSMiddleware       *handlers.CorsMiddleware
	TracingMiddleware    *handlers.TracingMiddleware
	WSTicketStore        *handlers.WSTicketStore
	TempTokens           *temptoken.Service
	TempTokenSweepWorker *temptoken.SweepWorker
	OAuth2SweepWorker    *oauth2SweepWorker
	// OAuth2IdentityResolver scopes a user-mode /mcp request to the user's own
	// tools. Optional; wired at server init when user-mode identity resolution
	// is available, otherwise left nil (user-mode requests fall back to the
	// global server).
	OAuth2IdentityResolver handlers.OAuth2IdentityResolver
	// ExternalQuotaBudgetResolver supplies budgets/usage for VKs whose
	// authoritative usage is tracked outside their own budget rows (enterprise
	// access-profile-managed VKs). Optional; wired at server init when available,
	// otherwise left nil so the quota endpoint reads the VK's own budget rows.
	ExternalQuotaBudgetResolver handlers.ExternalQuotaBudgetResolver

	SidekiqRunner         *sidekiq.Runner
	SidekiqDispatcherStop func()

	WebhookDispatcher *webhooks.Dispatcher

	wsPool *bfws.Pool
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

func (s *GovernanceInMemoryStore) GetMCPClientsAllowingAllVirtualKeys() map[string]string {
	return s.Config.GetAllowOnAllVirtualKeysClients()
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
	if err := s.Client.AddMCPClient(ctx, clientConfig); err != nil {
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

// UpdateMCPClientConnection reconnects an existing MCP client using updated headers
func (s *BifrostHTTPServer) UpdateMCPClientConnection(ctx context.Context, id string, newConfig *schemas.MCPClientConfig) error {
	if err := s.Config.UpdateMCPClientConnection(ctx, id, newConfig); err != nil {
		return err
	}
	if err := s.MCPServerHandler.SyncAllMCPServers(ctx); err != nil {
		logger.Warn("failed to sync MCP servers after updating client connection: %v", err)
	}
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

// DisableMCPClient shuts down an MCP client's connection and workers without removing it.
func (s *BifrostHTTPServer) DisableMCPClient(ctx context.Context, id string) error {
	if err := s.Config.DisableMCPClient(ctx, id); err != nil {
		return err
	}
	if err := s.MCPServerHandler.SyncAllMCPServers(ctx); err != nil {
		logger.Warn("failed to sync MCP servers after disabling client: %v", err)
	}
	return nil
}

// EnableMCPClient reconnects a disabled MCP client and restarts its health monitor and tool syncer.
func (s *BifrostHTTPServer) EnableMCPClient(ctx context.Context, id string) error {
	if err := s.Config.EnableMCPClient(ctx, id); err != nil {
		return err
	}
	if err := s.MCPServerHandler.SyncAllMCPServers(ctx); err != nil {
		logger.Warn("failed to sync MCP servers after enabling client: %v", err)
	}
	return nil
}

// VerifyHeadersConnection delegates to the Bifrost client to verify an MCP
// server with caller-supplied header values and discover its tools.
func (s *BifrostHTTPServer) VerifyHeadersConnection(ctx context.Context, config *schemas.MCPClientConfig, userHeaders map[string]string) (map[string]schemas.ChatTool, map[string]string, error) {
	return s.Client.VerifyHeadersConnection(ctx, config, userHeaders)
}

// VerifyPerUserOAuthConnection delegates to the Bifrost client to verify an MCP
// server using a temporary access token and discover available tools.
func (s *BifrostHTTPServer) VerifyPerUserOAuthConnection(ctx context.Context, config *schemas.MCPClientConfig, accessToken string) (map[string]schemas.ChatTool, map[string]string, error) {
	return s.Client.VerifyPerUserOAuthConnection(ctx, config, accessToken)
}

// SetClientTools delegates to the Bifrost client to update tool map for an existing MCP client,
// then re-syncs the MCP server so the new tools are immediately visible via /mcp.
func (s *BifrostHTTPServer) SetClientTools(clientID string, tools map[string]schemas.ChatTool, toolNameMapping map[string]string) {
	s.Client.SetClientTools(clientID, tools, toolNameMapping)
	if err := s.MCPServerHandler.SyncAllMCPServers(context.Background()); err != nil {
		logger.Warn("failed to sync MCP servers after setting client tools: %v", err)
	}
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
	bifrostCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
	return s.Client.GetAvailableMCPTools(bifrostCtx)
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

// getPromptsPluginName returns the prompts plugin name from context or default
func (s *BifrostHTTPServer) getPromptsPluginName() string {
	if name, ok := s.Ctx.Value(schemas.BifrostContextKeyPromptsPluginName).(string); ok && name != "" {
		return name
	}
	return prompts.PluginName
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
	}, lib.DBLookupMaxRetries, 2*lib.DBLookupDelay)
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
	// Fetch VK-scoped model configs up front, alongside the VK load, so that a DB
	// failure here aborts before we mutate any in-memory state. Reloading these
	// reflects governance changes made via the VK sheet (syncVKGovernanceToModelConfigs)
	// in memory immediately — both on the node that handled the update and on peers
	// that receive this reload via the cluster gossip broadcast.
	mcs, err := s.Config.ConfigStore.GetModelConfigsByScopeAndScopeIDs(
		ctx, tables.ModelConfigScopeVirtualKey, []string{id},
	)
	if err != nil {
		return virtualKey, fmt.Errorf("failed to reload VK-scoped model configs for VK %s: %w", id, err)
	}
	if governanceData := governancePlugin.GetGovernanceStore().GetGovernanceData(ctx); governanceData != nil {
		for _, existingVK := range governanceData.VirtualKeys {
			if existingVK != nil && existingVK.ID == virtualKey.ID && existingVK.Value.IsSet() && existingVK.Value.GetValue() != virtualKey.Value.GetValue() {
				s.MCPServerHandler.DeleteVKMCPServer(existingVK.Value.GetValue())
				break
			}
		}
	}
	store := governancePlugin.GetGovernanceStore()
	store.UpdateVirtualKeyInMemory(ctx, virtualKey, nil, nil, nil)
	// Snapshot in-memory VK-scoped config IDs before the upserts so we can evict
	// the ones that no longer exist in the DB (e.g. a standalone VK adopted into
	// an access profile has its VK-scoped governance model configs deleted).
	// Without this their stale budgets keep enforcing.
	staleIDs := make(map[string]bool)
	for _, mcID := range store.ScopedModelConfigIDs(tables.ModelConfigScopeVirtualKey, id) {
		staleIDs[mcID] = true
	}
	for i := range mcs {
		delete(staleIDs, mcs[i].ID)
		store.UpdateModelConfigInMemory(ctx, &mcs[i])
	}
	for mcID := range staleIDs {
		store.DeleteModelConfigInMemory(ctx, mcID)
	}
	s.MCPServerHandler.SyncVKMCPServer(virtualKey)
	return virtualKey, nil
}

// ResetBudget manually resets a budget's usage via the governance plugin's store
func (s *BifrostHTTPServer) ResetBudget(ctx context.Context, id string) (*tables.TableBudget, error) {
	governancePlugin, err := s.getGovernancePlugin()
	if err != nil {
		return nil, err
	}
	return governancePlugin.GetGovernanceStore().ResetBudget(ctx, id)
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
		governancePlugin.GetGovernanceStore().DeleteVirtualKeyInMemory(ctx, id)
		return nil
	}
	governancePlugin.GetGovernanceStore().DeleteVirtualKeyInMemory(ctx, id)
	s.MCPServerHandler.DeleteVKMCPServer(preloadedVk.Value.GetValue())
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
	governancePlugin.GetGovernanceStore().UpdateTeamInMemory(ctx, preloadedTeam, nil)
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
		governancePlugin.GetGovernanceStore().DeleteTeamInMemory(ctx, id)
		return nil
	}
	governancePlugin.GetGovernanceStore().DeleteTeamInMemory(ctx, id)
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
	governancePlugin.GetGovernanceStore().UpdateCustomerInMemory(ctx, preloadedCustomer, nil)
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
		governancePlugin.GetGovernanceStore().DeleteCustomerInMemory(ctx, id)
		return nil
	}
	governancePlugin.GetGovernanceStore().DeleteCustomerInMemory(ctx, id)
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
	updatedMC := governancePlugin.GetGovernanceStore().UpdateModelConfigInMemory(ctx, preloadedMC)
	if updatedMC == nil {
		return preloadedMC, nil
	}

	// Sync updated budget usage values back to database if they changed (per budget ID,
	// since a model config may own multiple budgets).
	preloadedUsage := make(map[string]float64, len(preloadedMC.Budgets))
	for i := range preloadedMC.Budgets {
		preloadedUsage[preloadedMC.Budgets[i].ID] = preloadedMC.Budgets[i].CurrentUsage
	}
	for i := range updatedMC.Budgets {
		b := &updatedMC.Budgets[i]
		if old, ok := preloadedUsage[b.ID]; ok && old != b.CurrentUsage {
			if err := s.Config.ConfigStore.UpdateBudgetUsage(ctx, b.ID, b.CurrentUsage); err != nil {
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
	governancePlugin.GetGovernanceStore().DeleteModelConfigInMemory(ctx, id)
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
			govUpdated := governancePlugin.GetGovernanceStore().UpdateProviderInMemory(ctx, providerInfo)
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

	// In-memory store holds the latest schemas.Key slice after the most recent
	// CRUD write — read from there to avoid re-fetching + re-converting from DB.
	inMemoryKeys, err := s.Config.GetProviderKeysRaw(provider)
	if err != nil {
		return nil, fmt.Errorf("failed to read provider keys for %s: %w", provider, err)
	}
	isKeylessProvider := providerInfo.CustomProviderConfig != nil && providerInfo.CustomProviderConfig.IsKeyLess
	hasNoKeys := len(inMemoryKeys) == 0 && !isKeylessProvider

	// Refresh keyconfig from the current key list, then drop any stale live
	// entries (for keys removed in this update) before refetching per-key.
	s.Config.ModelCatalog.SetKeyConfigForProvider(provider, inMemoryKeys)
	s.Config.ModelCatalog.InvalidateLiveProvider(provider)
	if hasNoKeys {
		logger.Warn("model discovery skipped for provider %s: no keys configured", provider)
	} else {
		s.RefreshLiveModelsForProvider(ctx, provider, inMemoryKeys)
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
	governancePlugin.GetGovernanceStore().DeleteProviderInMemory(ctx, string(provider))
	if s.Config == nil || s.Config.ModelCatalog == nil {
		return fmt.Errorf("pricing manager not found")
	}
	s.Config.ModelCatalog.InvalidateLiveProvider(provider)
	s.Config.ModelCatalog.RemoveKeyConfigForProvider(provider)

	return nil
}

// OnKeyAdded refreshes the keyconfig snapshot and fetches list-models for the
// new key only — 2 calls instead of ReloadProvider's 2×N. Called by the key
// handler after a successful AddProviderKey write.
func (s *BifrostHTTPServer) OnKeyAdded(ctx context.Context, provider schemas.ModelProvider, key schemas.Key) error {
	if s.Config == nil || s.Config.ModelCatalog == nil {
		return fmt.Errorf("model catalog not found")
	}
	keys, err := s.Config.GetProviderKeysRaw(provider)
	if err != nil {
		return fmt.Errorf("failed to read provider keys for %s: %w", provider, err)
	}
	s.Config.ModelCatalog.SetKeyConfigForProvider(provider, keys)
	// Keyless providers: empty keyID sentinel.
	keyID := key.ID
	if isKeylessProvider(provider, s.Config) {
		keyID = ""
	}
	// Skip the fetch for a disabled key — core rejects list-models calls
	// scoped to a disabled key's ID, so it would just fail and fall back
	// onto the (usually empty, for custom providers) static datasheet.
	if !keyEnabled(key) {
		return nil
	}
	s.FetchAndStoreLiveForKey(ctx, provider, keyID)
	return nil
}

// OnKeyUpdated invalidates the affected key's live entries (the gate may have
// changed even when Value didn't), refreshes the keyconfig, then refetches
// for just that key. 2 calls regardless of N keys on the provider.
func (s *BifrostHTTPServer) OnKeyUpdated(ctx context.Context, provider schemas.ModelProvider, key schemas.Key) error {
	if s.Config == nil || s.Config.ModelCatalog == nil {
		return fmt.Errorf("model catalog not found")
	}
	keys, err := s.Config.GetProviderKeysRaw(provider)
	if err != nil {
		return fmt.Errorf("failed to read provider keys for %s: %w", provider, err)
	}
	s.Config.ModelCatalog.SetKeyConfigForProvider(provider, keys)
	keyID := key.ID
	if isKeylessProvider(provider, s.Config) {
		keyID = ""
	}
	s.Config.ModelCatalog.InvalidateLive(provider, keyID)
	// Skip the fetch for a disabled key — the invalidate above still clears
	// its stale cached entries, but re-fetching would just fail against core.
	if !keyEnabled(key) {
		return nil
	}
	s.FetchAndStoreLiveForKey(ctx, provider, keyID)
	return nil
}

// OnKeyDeleted invalidates the deleted key's live entries and refreshes the
// keyconfig. No list-models calls — the provider's remaining keys' cached
// entries stay valid.
func (s *BifrostHTTPServer) OnKeyDeleted(ctx context.Context, provider schemas.ModelProvider, keyID string) error {
	if s.Config == nil || s.Config.ModelCatalog == nil {
		return fmt.Errorf("model catalog not found")
	}
	keys, err := s.Config.GetProviderKeysRaw(provider)
	if err != nil {
		return fmt.Errorf("failed to read provider keys for %s: %w", provider, err)
	}
	s.Config.ModelCatalog.SetKeyConfigForProvider(provider, keys)
	s.Config.ModelCatalog.InvalidateLive(provider, keyID)
	return nil
}

// keyEnabled reports whether a key should be treated as active for
// scheduling model-discovery fetches. Enabled defaults to true when nil,
// matching the convention core.getAllSupportedKeys uses to filter keys for
// ListModels requests — a key discovery schedules a fetch for must be one
// core will actually accept, or the call is a guaranteed
// "no key found with id..." failure.
func keyEnabled(key schemas.Key) bool {
	return key.Enabled == nil || *key.Enabled
}

// isKeylessProvider returns true when the provider's config marks it
// keyless. Used to pick the live-cache key for OnKey* helpers: keyless
// providers cache under the empty-string sentinel.
func isKeylessProvider(provider schemas.ModelProvider, cfg *lib.Config) bool {
	if cfg == nil {
		return false
	}
	pc, err := cfg.GetProviderConfigRaw(provider)
	if err != nil || pc == nil || pc.CustomProviderConfig == nil {
		return false
	}
	return pc.CustomProviderConfig.IsKeyLess
}

// GetGovernanceData returns the governance data
func (s *BifrostHTTPServer) GetGovernanceData(ctx context.Context) *governance.GovernanceData {
	// Use type-safe finder from Config
	governancePlugin, err := lib.FindPluginAs[governance.BaseGovernancePlugin](s.Config, s.getGovernancePluginName())
	if err != nil {
		return nil
	}
	return governancePlugin.GetGovernanceStore().GetGovernanceData(ctx)
}

// ReloadComplexityAnalyzerConfig reloads the complexity analyzer config into the governance plugin.
func (s *BifrostHTTPServer) ReloadComplexityAnalyzerConfig(ctx context.Context, config *complexity.AnalyzerConfig) error {
	governancePlugin, err := s.getGovernancePlugin()
	if err != nil {
		return fmt.Errorf("governance plugin not found: %w", err)
	}
	reloader, ok := governancePlugin.(interface {
		ReloadComplexityAnalyzerConfig(config *complexity.AnalyzerConfig)
	})
	if !ok {
		return fmt.Errorf("governance plugin does not support complexity analyzer config reload")
	}
	reloader.ReloadComplexityAnalyzerConfig(config)
	return nil
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
	if err := store.UpdateRoutingRuleInMemory(ctx, rule); err != nil {
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
	if err := store.DeleteRoutingRuleInMemory(ctx, id); err != nil {
		return fmt.Errorf("failed to delete routing rule from store: %w", err)
	}
	return nil
}

// ReloadWebhookEndpoint refreshes a single webhook endpoint in the in-memory
// store from the database after a mutation. A clustered deployment overrides
// this to also notify peers so their in-memory copies stay current.
func (s *BifrostHTTPServer) ReloadWebhookEndpoint(ctx context.Context, id string) error {
	endpoint, err := s.Config.ConfigStore.GetWebhookEndpointByID(ctx, id)
	if err != nil {
		return err
	}
	s.Config.SetWebhookEndpoint(endpoint)
	return nil
}

// RemoveWebhookEndpoint drops a webhook endpoint from the in-memory store after
// a database delete. A clustered deployment overrides this to also notify peers.
func (s *BifrostHTTPServer) RemoveWebhookEndpoint(ctx context.Context, id string) error {
	s.Config.RemoveWebhookEndpoint(id)
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
	if config == nil {
		return fmt.Errorf("client config not found")
	}
	*s.Config.ClientConfig = *config
	// Reloading whitelisted routes from the client config
	if s.AuthMiddleware != nil {
		s.AuthMiddleware.UpdateWhitelistedRoutes(config.WhitelistedRoutes)
		s.AuthMiddleware.UpdateTempTokenAuthEnabled(config.MCPEnableTempTokenAuth)
	}
	// Refresh the CORS middleware's immutable snapshot so its requests pick up the
	// new AllowedOrigins/AllowedHeaders/DumpErrorsInConsoleLogs without racing the
	// in-place ClientConfig mutation above.
	if s.CORSMiddleware != nil {
		s.CORSMiddleware.UpdateConfig(s.Config)
	}
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
		if err := s.Client.UpdateToolManagerConfig(
			s.Config.ClientConfig.MCPAgentDepth,
			s.Config.ClientConfig.MCPToolExecutionTimeout,
			s.Config.ClientConfig.MCPCodeModeBindingLevel,
			s.Config.ClientConfig.MCPDisableAutoToolInject,
		); err != nil {
			logger.Warn("failed to sync MCP tool manager config during client config reload: %v", err)
		}
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

// UpdateMCPToolManagerConfig updates the MCP tool manager config.
// Always pass the current disableAutoToolInject value so it is never reset.
func (s *BifrostHTTPServer) UpdateMCPToolManagerConfig(ctx context.Context, maxAgentDepth int, toolExecutionTimeoutInSeconds int, codeModeBindingLevel string, disableAutoToolInject bool) error {
	if s.Config == nil {
		return fmt.Errorf("config not found")
	}
	return s.Client.UpdateToolManagerConfig(maxAgentDepth, toolExecutionTimeoutInSeconds, codeModeBindingLevel, disableAutoToolInject)
}

// reloadObservabilityPlugins reloads all observability plugins in the tracing middleware
func (s *BifrostHTTPServer) reloadObservabilityPlugins() {
	observabilityPlugins := s.CollectObservabilityPlugins()
	// Always update the tracing middleware, even with empty slice, to clear stale plugins
	s.TracingMiddleware.SetObservabilityPlugins(observabilityPlugins)
}

// ReloadPricingManager reloads the pricing manager
func (s *BifrostHTTPServer) UpdateSyncConfig(ctx context.Context) error {
	if s.Config == nil || s.Config.ModelCatalog == nil {
		return fmt.Errorf("pricing manager not found")
	}
	if s.Config.FrameworkConfig == nil || s.Config.FrameworkConfig.Pricing == nil {
		return fmt.Errorf("framework config not found")
	}
	return s.Config.ModelCatalog.UpdateSyncConfig(ctx, s.Config.FrameworkConfig.Pricing)
}

// RefreshLiveModelsForProvider runs filtered + unfiltered list-models for the
// provider, fanning out per key in parallel so the live cache ends up with
// per-(provider, keyID) entries. Keyless providers cache under the "" sentinel.
//
// Callers are responsible for invalidating stale entries first when keys
// have been removed from the provider's set.
func (s *BifrostHTTPServer) RefreshLiveModelsForProvider(ctx context.Context, provider schemas.ModelProvider, keys []schemas.Key) {
	if len(keys) == 0 {
		// Empty key slice + non-keyless provider would write under the "" sentinel
		// reserved for keyless providers — colliding with the keyless namespace and
		// triggering an unauthenticated fetch for a provider that requires a key.
		if !isKeylessProvider(provider, s.Config) {
			logger.Warn("model discovery skipped for provider %s: no keys configured", provider)
			return
		}
		s.FetchAndStoreLiveForKey(ctx, provider, "")
		return
	}
	var wg sync.WaitGroup
	enabledCount := 0
	for _, key := range keys {
		// Skip disabled keys — core rejects list-models calls scoped to a
		// disabled key's ID ("no key found with id..."), so fetching for one
		// is guaranteed to fail and only adds "falling back onto the static
		// datasheet" log noise per disabled key.
		if !keyEnabled(key) {
			continue
		}
		enabledCount++
		wg.Add(1)
		go func(keyID string) {
			defer wg.Done()
			s.FetchAndStoreLiveForKey(ctx, provider, keyID)
		}(key.ID)
	}
	if enabledCount == 0 {
		logger.Warn("model discovery skipped for provider %s: no enabled keys configured", provider)
		return
	}
	wg.Wait()
}

// FetchAndStoreLiveForKey issues the filtered and unfiltered list-models
// calls for one (provider, keyID) in parallel and writes the results into
// the catalog. Errors are logged and surfaced via updateKeyStatus when the
// provider returns per-key statuses, but they do not abort the other call.
// keyID="" scopes to "no specific key" — used for keyless providers and as
// the legacy sentinel. Always validates keys for the providers that opt into
// the check (today: OpenRouter, whose /v1/models is unauthenticated) so the
// routing graph is the same at boot, after a key add, and after a reload —
// stale-but-routable behavior would diverge otherwise.
func (s *BifrostHTTPServer) FetchAndStoreLiveForKey(ctx context.Context, provider schemas.ModelProvider, keyID string) {
	// Skip the fetch entirely when the provider has disabled list_models via
	// allowed_requests — every per-(provider,keyID) call would just bounce with
	// "operation not allowed", wasting two goroutines and one bfCtx per attempt.
	if s.Config != nil {
		if pc, err := s.Config.GetProviderConfigRaw(provider); err == nil && pc != nil &&
			pc.CustomProviderConfig != nil &&
			!pc.CustomProviderConfig.IsOperationAllowed(schemas.ListModelsRequest) {
			return
		}
	}
	// One BifrostContext per goroutine. BifrostContext.SetValue mutates state
	// in place, so the request-scoped metadata core sets during a routing pass
	// (RequestID, FallbackIndex, span IDs, ...) would otherwise bleed between
	// the filtered and unfiltered calls and conflate them in logs/billing.
	newListModelsCtx := func() *schemas.BifrostContext {
		c := schemas.NewBifrostContext(ctx, time.Now().Add(15*time.Second))
		c.SetValue(schemas.BifrostContextKeySkipPluginPipeline, true)
		c.SetValue(schemas.BifrostContextKeyValidateKeys, true)
		return c
	}

	var keyIDPtr *string
	if keyID != "" {
		keyIDPtr = &keyID
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		bfCtx := newListModelsCtx()
		defer bfCtx.Cancel()
		resp, bfErr := s.Client.ListModelsRequest(bfCtx, &schemas.BifrostListModelsRequest{
			Provider: provider,
			KeyID:    keyIDPtr,
		})
		if bfErr != nil {
			logger.Warn("filtered list-models failed for provider %s key %s: %v: falling back onto the static datasheet", provider, keyID, bifrost.GetErrorMessage(bfErr))
			if len(bfErr.ExtraFields.KeyStatuses) > 0 && s.Config.ConfigStore != nil {
				s.updateKeyStatus(ctx, bfErr.ExtraFields.KeyStatuses)
			}
			return
		}
		if resp == nil {
			return
		}
		s.Config.ModelCatalog.UpsertLiveFromResponse(provider, keyID, false, resp)
		if len(resp.KeyStatuses) > 0 && s.Config.ConfigStore != nil {
			s.updateKeyStatus(ctx, resp.KeyStatuses)
		}
	}()
	go func() {
		defer wg.Done()
		bfCtx := newListModelsCtx()
		defer bfCtx.Cancel()
		resp, bfErr := s.Client.ListModelsRequest(bfCtx, &schemas.BifrostListModelsRequest{
			Provider:   provider,
			KeyID:      keyIDPtr,
			Unfiltered: true,
		})
		if bfErr != nil {
			logger.Warn("unfiltered list-models failed for provider %s key %s: %v: falling back onto the static datasheet", provider, keyID, bifrost.GetErrorMessage(bfErr))
			return
		}
		if resp == nil {
			return
		}
		s.Config.ModelCatalog.UpsertLiveFromResponse(provider, keyID, true, resp)
	}()
	wg.Wait()
}

// ForceReloadPricing triggers an immediate pricing sync and resets the sync
// timer. No longer triggers a list-models refresh — pricing reload is now
// pricing-only.
func (s *BifrostHTTPServer) ForceReloadPricing(ctx context.Context) error {
	if s.Config == nil {
		return fmt.Errorf("server config not initialized")
	}
	if s.Config.ModelCatalog != nil {
		if err := s.Config.ModelCatalog.ForceReloadPricing(ctx); err != nil {
			return fmt.Errorf("failed to force reload pricing: %w", err)
		}
	}
	return nil
}

// ReloadPricingFromDBAndPopulateModelPool reloads the pricing from DB. The
// list-models refresh that used to follow is gone — pricing reload is now
// pricing-only.
func (s *BifrostHTTPServer) ReloadPricingFromDBAndPopulateModelPool(ctx context.Context) error {
	if s.Config == nil {
		return fmt.Errorf("server config not initialized")
	}
	if s.Config.ModelCatalog != nil {
		if err := s.Config.ModelCatalog.ReloadFromDB(ctx); err != nil {
			return fmt.Errorf("failed to reload pricing from DB: %w", err)
		}
	}
	return nil
}

// UpsertPricingOverride inserts or updates a pricing override in the in-memory model catalog.
func (s *BifrostHTTPServer) UpsertPricingOverride(ctx context.Context, override *tables.TablePricingOverride) error {
	if s.Config == nil || s.Config.ModelCatalog == nil {
		return fmt.Errorf("pricing manager not found")
	}
	return s.Config.ModelCatalog.UpsertPricingOverrides(override)
}

// DeletePricingOverride removes a pricing override from the in-memory model catalog.
func (s *BifrostHTTPServer) DeletePricingOverride(ctx context.Context, id string) error {
	if s.Config == nil || s.Config.ModelCatalog == nil {
		return fmt.Errorf("pricing manager not found")
	}
	s.Config.ModelCatalog.DeletePricingOverride(id)
	return nil
}

// UpsertModelPricingAttributes writes the additional_attributes JSON for the
// pricing rows keyed by (model, provider) for every entry in the batch. The
// whole batch is wrapped in a single transaction so a missing pricing row
// rolls back the lot. After a successful commit the in-memory pricing cache
// is reloaded once. Enterprise overrides this method to broadcast a peer
// reload after commit.
func (s *BifrostHTTPServer) UpsertModelPricingAttributes(ctx context.Context, entries []handlers.ModelPricingAttributesEntry) error {
	if s.Config == nil || s.Config.ModelCatalog == nil {
		return fmt.Errorf("model catalog not initialized")
	}
	if s.Config.ConfigStore == nil {
		return fmt.Errorf("model catalog requires a config store")
	}
	var missing []string
	err := s.Config.ConfigStore.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
		for _, e := range entries {
			rows, err := s.Config.ConfigStore.UpsertModelPricingAttributes(ctx, e.Model, e.Provider, e.AdditionalAttributes, tx)
			if err != nil {
				return err
			}
			if rows == 0 {
				missing = append(missing, fmt.Sprintf("%s/%s", e.Provider, e.Model))
			}
		}
		if len(missing) > 0 {
			return fmt.Errorf("no pricing row for one or more (model, provider) entries: %s", strings.Join(missing, ", "))
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := s.Config.ModelCatalog.ReloadPricing(ctx); err != nil {
		return fmt.Errorf("failed to reload pricing cache after attribute write: %w", err)
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
	// Store the raw header filter config in ClientConfig
	s.Config.ClientConfig.HeaderFilterConfig = config
	// Compile into optimized matcher for O(1) per-request lookups
	s.Config.SetHeaderMatcher(lib.NewHeaderMatcher(config))
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

// GetLoadedPluginNames returns the sanitized names of all currently loaded plugins,
// matching the names embedded in their trace span names.
func (s *BifrostHTTPServer) GetLoadedPluginNames() []string {
	if s.Config == nil {
		return []string{}
	}
	return s.Config.GetLoadedPluginNames()
}

// NormalizePluginConfig implements handlers.PluginsLoader. It looks up the plugin
// by name in the ConfigMarshallers cache and calls MarshalConfigForStorage if found.
// Returns nil, nil when the plugin is not loaded or does not implement ConfigMarshallerPlugin.
func (s *BifrostHTTPServer) NormalizePluginConfig(name string, config map[string]any) (map[string]any, error) {
	if m := s.Config.ConfigMarshallers.Load(); m != nil {
		if cm, ok := (*m)[name]; ok {
			return cm.MarshalConfigForStorage(config)
		}
	}
	return nil, nil
}

// ExpandPluginConfigForAPI implements handlers.PluginsLoader. It looks up the plugin
// by name in the ConfigMarshallers cache and calls RedactConfig if found.
// Returns nil, nil when the plugin is not loaded or does not implement ConfigMarshallerPlugin.
func (s *BifrostHTTPServer) ExpandPluginConfigForAPI(name string, config map[string]any) (map[string]any, error) {
	if m := s.Config.ConfigMarshallers.Load(); m != nil {
		if cm, ok := (*m)[name]; ok {
			return cm.RedactConfig(config)
		}
	}
	return nil, nil
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
func (s *BifrostHTTPServer) SyncLoadedPlugin(ctx context.Context, name string, plugin schemas.BasePlugin, placement *schemas.PluginPlacement, order *int) error {
	// 2. Register (replaces old version atomically)
	if err := s.Config.ReloadPlugin(plugin); err != nil {
		return s.updatePluginErrorStatus(plugin.GetName(), "registering", err)
	}
	// 2b. Set order info and re-sort
	s.Config.SetPluginOrderInfo(plugin.GetName(), placement, order)
	s.Config.SortAndRebuildPlugins()
	// 3. Update Bifrost client
	if err := s.Client.ReloadPlugin(plugin, InferPluginTypes(plugin)); err != nil {
		return s.updatePluginErrorStatus(plugin.GetName(), "reloading bifrost config for", err)
	}
	// 3b. Sync plugin execution order from config to core
	s.Client.ReorderPlugins(s.Config.GetPluginOrder())
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
func (s *BifrostHTTPServer) ReloadPlugin(ctx context.Context, name string, path *string, pluginConfig any, placement *schemas.PluginPlacement, order *int) error {
	logger.Debug("reloading plugin %s", name)
	// 1. Instantiate new version
	plugin, err := InstantiatePlugin(ctx, name, path, pluginConfig, s.Config)
	if err != nil {
		return s.updatePluginErrorStatus(name, "loading", err)
	}
	// Wire the embedding executor on the new instance before syncing.
	if semanticCachePlugin, ok := plugin.(*semanticcache.Plugin); ok {
		semanticCachePlugin.SetEmbeddingRequestExecutor(s.Client.EmbeddingRequest)
	}
	return s.SyncLoadedPlugin(ctx, name, plugin, placement, order)
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

	// 4. Update status and marshaller
	if isDisabled, _ := ctx.Value(handlers.PluginDisabledKey).(bool); isDisabled {
		s.markPluginDisabled(name)
	} else {
		s.Config.DeletePluginOverallStatus(name)
		// Plugin is being permanently deleted: remove its config marshaller too.
		s.Config.RemoveConfigMarshaller(name)
	}

	return nil
}

// StartOAuth2SweepWorker creates and starts the janitor for the OAuth2
// issuance tables (expired authorize requests, aged-out revoked refresh
// tokens, orphaned dynamically-registered clients). Call it once from
// single-threaded bootstrap wiring, like the other worker fields on this
// struct — the nil-check makes double-wiring a no-op, it is not a concurrency
// guard. It is also a no-op when no config store is configured. The Start()
// shutdown path stops the worker.
//
// shouldSweep, when non-nil, is consulted before each pass; returning false
// skips that pass. Deployments running several instances against one config
// store can use it to restrict sweeping to a single instance. nil means
// always sweep.
func (s *BifrostHTTPServer) StartOAuth2SweepWorker(ctx context.Context, shouldSweep func() bool) {
	if s.OAuth2SweepWorker != nil || s.Config == nil || s.Config.ConfigStore == nil {
		return
	}
	s.OAuth2SweepWorker = newOAuth2SweepWorker(s.Config.ConfigStore, shouldSweep)
	s.OAuth2SweepWorker.start(ctx)
}

// RegisterInferenceRoutes initializes the routes for the inference handler
func (s *BifrostHTTPServer) RegisterInferenceRoutes(ctx context.Context, middlewares ...schemas.BifrostHTTPMiddleware) error {
	// Initialize WebSocket pool and handler before integrations so it can be wired through
	s.wsPool = bfws.NewPool(s.Config.WebSocketConfig.Pool)
	wsResponsesHandler := handlers.NewWSResponsesHandler(s.Client, s.Config, s.wsPool)
	wsRealtimeHandler := handlers.NewWSRealtimeHandler(s.Client, s.Config, s.wsPool)
	webrtcRealtimeHandler := handlers.NewWebRTCRealtimeHandler(s.Client, s.Config)
	realtimeClientSecretsHandler := handlers.NewRealtimeClientSecretsHandler(s.Client, s.Config)

	inferenceHandler := handlers.NewInferenceHandler(s.Client, s.Config)
	s.IntegrationHandler = handlers.NewIntegrationHandler(s.Client, s.Config, wsResponsesHandler, wsRealtimeHandler, webrtcRealtimeHandler, realtimeClientSecretsHandler)
	mcpInferenceHandler := handlers.NewMCPInferenceHandler(s.Client, s.Config)
	// Serve by-ID virtual key lookups on the /mcp JWT auth path from the
	// governance in-memory store (avoiding a per-request DB read). Best-effort:
	// any store that exposes GetVirtualKeyByID qualifies; otherwise the handler
	// falls back to the config store.
	var vkCache handlers.VirtualKeyCache
	if gp, gerr := s.getGovernancePlugin(); gerr == nil && gp != nil {
		if c, ok := gp.GetGovernanceStore().(handlers.VirtualKeyCache); ok {
			vkCache = c
		}
	}
	mcpServerHandler, err := handlers.NewMCPServerHandler(ctx, s.Config, s, s.OAuth2IdentityResolver, vkCache)
	if err != nil {
		return fmt.Errorf("failed to initialize mcp server handler: %v", err)
	}
	s.MCPServerHandler = mcpServerHandler
	asyncHandler := handlers.NewAsyncHandler(s.Client, s.Config)
	s.IntegrationHandler.RegisterRoutes(s.Router, middlewares...)
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
	var govLogManager logging.LogManager
	if loggerPlugin != nil {
		loggingHandler = handlers.NewLoggingHandler(loggerPlugin.GetPluginLogManager(), s, s.Config)
		if resolverProvider, ok := callbacks.(LogRedactionMappingResolverProvider); ok {
			loggingHandler.SetLogRedactionMappingResolver(resolverProvider.GetLogRedactionMappingResolver())
		}
		// Wire the sidekiq runner so cost recalculation runs as a durable background
		// job. Registering the handler here (before RecoverIncomplete) lets a job
		// interrupted by a restart resume on boot.
		if s.SidekiqRunner != nil && s.Config != nil && s.Config.ConfigStore != nil {
			loggingHandler.SetSidekiqBackend(s.SidekiqRunner, s.Config.ConfigStore)
		}
		govLogManager = loggerPlugin.GetPluginLogManager()
	}
	var governanceHandler *handlers.GovernanceHandler
	governancePluginName := governance.PluginName
	if name, ok := ctx.Value(schemas.BifrostContextKeyGovernancePluginName).(string); ok && name != "" {
		governancePluginName = name
	}
	governancePlugin, _ := lib.FindPluginAs[schemas.LLMPlugin](s.Config, governancePluginName)
	if governancePlugin != nil {
		governanceHandler, err = handlers.NewGovernanceHandler(callbacks, s.Config.ConfigStore, govLogManager, s.ExternalQuotaBudgetResolver)
		if err != nil {
			return fmt.Errorf("failed to initialize governance handler: %v", err)
		}
	}
	// Resolve the semantic_cache plugin per request so plugin reloads via
	// /api/plugins are honored — the previous boot-time capture left stale
	// references and (worse) skipped route registration entirely when the
	// plugin wasn't in config.json at startup, causing 405 on all cache-clear
	// endpoints for the process lifetime.
	cacheHandler := handlers.NewCacheHandler(func() handlers.CacheClearer {
		p, err := lib.FindPluginAs[*semanticcache.Plugin](s.Config, semanticcache.PluginName)
		if err != nil || p == nil {
			return nil
		}
		return p
	})
	var promptsReloader handlers.PromptCacheReloader
	if promptsPlugin, err := lib.FindPluginAs[handlers.PromptCacheReloader](s.Config, s.getPromptsPluginName()); err == nil && promptsPlugin != nil {
		promptsReloader = promptsPlugin
	}
	// Websocket handler needs to go below UI handler
	logger.Debug("initializing websocket server")
	if s.WebSocketHandler == nil {
		s.WebSocketHandler = handlers.NewWebSocketHandler(s.Ctx, s.Config.ClientConfig.AllowedOrigins)
	}
	// Start WebSocket heartbeat
	s.WebSocketHandler.StartHeartbeat()
	// Adding telemetry middleware
	// Chaining all middlewares
	// lib.ChainMiddlewares chains multiple middlewares together
	healthHandler := handlers.NewHealthHandler(s.Config)
	providerHandler := handlers.NewProviderHandler(callbacks, s.Config, s.Client)
	oauthHandler := handlers.NewOAuthHandler(s.Config.OAuthProvider, s.Client, s.Config)
	mcpHandler := handlers.NewMCPHandler(callbacks, callbacks, s.Client, s.Config, oauthHandler)
	mcpPerUserHeadersHandler := handlers.NewMCPPerUserHeadersHandler(callbacks, s.Config, s.TempTokens)
	mcpSessionsHandler := handlers.NewMCPSessionsHandler(s.Config)
	configHandler := handlers.NewConfigHandler(callbacks, s.Config)
	pluginsHandler := handlers.NewPluginsHandler(callbacks, s.Config.ConfigStore)
	sessionHandler := handlers.NewSessionHandler(s.Config.ConfigStore, s.WSTicketStore)
	promptsHandler := handlers.NewPromptsHandler(s.Config.ConfigStore, promptsReloader)
	featureFlagsHandler := handlers.NewFeatureFlagsHandler(s.Config.FeatureFlags, s.Config.ConfigStore)
	// Going ahead with API handlers
	oauth2DiscoveryHandler := handlers.NewOAuth2DiscoveryHandler(s.Config)
	oauth2IssuanceHandler := handlers.NewOAuth2IssuanceHandler(s.Config, s.TempTokens, s.OAuth2IdentityResolver)
	oauth2SessionsHandler := handlers.NewOAuth2SessionsHandler(s.Config)
	oauth2ConsentHandler := handlers.NewOAuth2ConsentHandler(s.Config, s.TempTokens, s.OAuth2IdentityResolver)

	oauth2DiscoveryHandler.RegisterRoutes(s.Router, middlewares...)
	// No middleware needed for mcp issuance routes, they should be open
	oauth2IssuanceHandler.RegisterRoutes(s.Router)
	oauth2SessionsHandler.RegisterRoutes(s.Router, middlewares...)
	oauth2ConsentHandler.RegisterRoutes(s.Router, middlewares...)
	healthHandler.RegisterRoutes(s.Router, middlewares...)
	providerHandler.RegisterRoutes(s.Router, middlewares...)
	mcpHandler.RegisterRoutes(s.Router, middlewares...)
	mcpPerUserHeadersHandler.RegisterRoutes(s.Router, middlewares...)
	mcpSessionsHandler.RegisterRoutes(s.Router, middlewares...)
	configHandler.RegisterRoutes(s.Router, middlewares...)
	oauthHandler.RegisterRoutes(s.Router, middlewares...)
	if pluginsHandler != nil {
		pluginsHandler.RegisterRoutes(s.Router, middlewares...)
	}
	if sessionHandler != nil {
		sessionHandler.RegisterRoutes(s.Router, middlewares...)
	}
	if promptsHandler != nil {
		promptsHandler.RegisterRoutes(s.Router, middlewares...)
	}
	skillsHandler := handlers.NewSkillsHandler(s.Config.ConfigStore, s.Config.ObjectStore)
	if skillsHandler != nil {
		skillsHandler.RegisterRoutes(s.Router, middlewares...)
	}
	webhookHandler := handlers.NewWebhookHandler(callbacks, s.Config, s.WebhookDispatcher)
	webhookHandler.RegisterRoutes(s.Router, middlewares...)
	skillsServingHandler := handlers.NewSkillsServingHandler(s.Config.ConfigStore, s.Config.ObjectStore)
	if skillsServingHandler != nil {
		skillsServingHandler.RegisterRoutes(s.Router, middlewares...)
	}
	cacheHandler.RegisterRoutes(s.Router, middlewares...)
	if featureFlagsHandler != nil {
		featureFlagsHandler.RegisterRoutes(s.Router, middlewares...)
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
	metricsGatherer := prometheus.GathererFunc(func() ([]*dto.MetricFamily, error) {
		plugin, err := lib.FindPluginAs[*telemetry.PrometheusPlugin](s.Config, telemetry.PluginName)
		if err != nil || plugin == nil {
			return nil, nil
		}
		return plugin.GetMetricsGatherer().Gather()
	})
	metricsAdapter := fasthttpadaptor.NewFastHTTPHandler(promhttp.HandlerFor(metricsGatherer, promhttp.HandlerOpts{}))
	metricsHandler := func(ctx *fasthttp.RequestCtx) {
		plugin, err := lib.FindPluginAs[*telemetry.PrometheusPlugin](s.Config, telemetry.PluginName)
		if err != nil || plugin == nil || !plugin.IsMetricsEnabled() {
			handlers.SendError(ctx, fasthttp.StatusNotFound, "Route not found: "+string(ctx.Path()))
			return
		}
		metricsAdapter(ctx)
	}
	s.Router.GET("/metrics", lib.ChainMiddlewares(metricsHandler, middlewares...))
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
	// Copy the matched route template saved by the router (SaveMatchedRoutePath) into a
	// stable, router-agnostic user value so metrics middlewares below can label by route
	// template instead of the raw URL path.
	commonMiddlewares = append(commonMiddlewares, func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			if route, ok := ctx.UserValue(router.MatchedRoutePathParam).(string); ok && route != "" {
				ctx.SetUserValue(string(schemas.BifrostContextKeyHTTPRoute), route)
				// Drop the router's randomized key so it doesn't leak into request PathParams.
				ctx.RemoveUserValue(router.MatchedRoutePathParam)
			}
			next(ctx)
		}
	})
	// Preparing middlewares
	// Initializing prometheus plugin
	prometheusPlugin, err := lib.FindPluginAs[*telemetry.PrometheusPlugin](s.Config, telemetry.PluginName)
	if err == nil {
		commonMiddlewares = append(commonMiddlewares, prometheusPlugin.HTTPMiddleware)
	} else {
		logger.Warn("prometheus plugin not found, skipping telemetry middleware")
	}
	// OTel HTTP metrics (http_requests_total etc., pushed via OTLP). The otel plugin is
	// resolved per request rather than captured here: a config reload swaps in a freshly
	// constructed plugin instance, and a pointer captured at startup would keep recording
	// against exporters whose meter provider has been shut down.
	commonMiddlewares = append(commonMiddlewares, func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			start := time.Now()
			reqSize := float64(ctx.Request.Header.ContentLength())
			next(ctx)
			otelPlugin, err := lib.FindPluginAs[*otel.OtelPlugin](s.Config, otel.PluginName)
			if err != nil {
				return
			}
			// Label by the matched route template when available (set by the middleware
			// above) so path params (model names, batch/file IDs) don't explode cardinality.
			path := string(ctx.Path())
			if route, ok := ctx.UserValue(string(schemas.BifrostContextKeyHTTPRoute)).(string); ok && route != "" {
				path = route
			}
			otelPlugin.RecordHTTPMetrics(ctx,
				path,
				string(ctx.Method()),
				strconv.Itoa(ctx.Response.StatusCode()),
				time.Since(start).Seconds(),
				reqSize,
				float64(ctx.Response.Header.ContentLength()),
			)
		}
	})
	return commonMiddlewares
}

func startSkillsOrphanCleanupWorker(ctx context.Context, config *lib.Config) {
	if config == nil || config.ConfigStore == nil {
		return
	}

	// Run once on startup asynchronously with a 10-minute timeout so a stalled
	// DB or object-store call does not leave the goroutine hanging indefinitely.
	go func() {
		cleanupCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		defer cancel()
		result, err := handlers.CleanupOrphanSkillFiles(cleanupCtx, config.ConfigStore, config.ObjectStore, false)
		if err != nil {
			logger.Warn("skills orphan cleanup failed during startup: %v", err)
		} else {
			logger.Info("skills orphan cleanup completed during startup: deleted_db_blobs=%d deleted_storage_objects=%d", result.DeletedDBBlobs, result.DeletedStorageObjects)
		}
	}()
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
	if s.Config.KVStore != nil {
		integrations.RegisterKVDecoders(s.Config.KVStore)
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

	// Initialize the webhook delivery dispatcher (requires both stores; the
	// in-memory endpoint store on Config serves endpoint lookups).
	if s.Config.LogsStore != nil && s.Config.ConfigStore != nil {
		s.WebhookDispatcher = webhooks.NewDispatcher(ctx, "", s.Config.ClientConfig.WebhookConfig.DeliveryHistoryRetention(), s.Config.ConfigStore, s.Config.LogsStore, s.Config, logger)
		s.WebhookDispatcher.Start()
		logger.Info("webhook dispatcher initialized")
	}

	// Initialize async job executor (requires LogsStore + governance plugin)
	if s.Config.LogsStore != nil {
		governancePlugin, govErr := lib.FindPluginAs[governance.BaseGovernancePlugin](s.Config, s.getGovernancePluginName())
		if govErr == nil {
			// The dispatcher interface value must stay nil when no dispatcher
			// exists — a typed-nil pointer would defeat the executor's nil check.
			var jobDispatcher logstore.WebhookDispatcher
			if s.WebhookDispatcher != nil {
				jobDispatcher = s.WebhookDispatcher
			}
			s.Config.AsyncJobExecutor = logstore.NewAsyncJobExecutor(s.Config.LogsStore, governancePlugin.GetGovernanceStore(), jobDispatcher, s.Config, logger)
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
		MCPHeadersProvider: s.Config.MCPHeadersProvider,
		Logger:             logger,
		KVStore:            s.Config.KVStore,
	})
	if err != nil {
		return fmt.Errorf("failed to initialize bifrost: %v", err)
	}
	logger.Info("bifrost client initialized")
	// Sync plugin execution order from config to core (defensive — Init receives sorted list,
	// but this ensures order consistency if the loading path changes in the future)
	s.Client.ReorderPlugins(s.Config.GetPluginOrder())
	// Seed the catalog: push the initial keyconfig snapshot and fetch per-key
	// live models for every provider concurrently.
	logger.Info("listing all models and adding to model catalog")
	if s.Config.ModelCatalog != nil {
		snapshot := make(map[schemas.ModelProvider][]schemas.Key, len(s.Config.Providers))
		for provider, providerConfig := range s.Config.Providers {
			snapshot[provider] = providerConfig.Keys
		}
		s.Config.ModelCatalog.ReplaceKeyConfig(snapshot)

		var wg sync.WaitGroup
		for provider, providerConfig := range s.Config.Providers {
			wg.Add(1)
			go func(p schemas.ModelProvider, keys []schemas.Key) {
				defer wg.Done()
				s.RefreshLiveModelsForProvider(ctx, p, keys)
			}(provider, providerConfig.Keys)
		}
		wg.Wait()
	}
	logger.Info("models added to catalog")
	s.Config.SetBifrostClient(s.Client)
	// Initialize routes
	s.Router = router.New()
	// Save the matched route template on each request
	// so metrics can use it as the `path` label instead of the raw URL path.
	s.Router.SaveMatchedRoutePath = true
	// Initialize CORS middleware
	s.CORSMiddleware = handlers.NewCorsMiddleware(s.Config)
	commonMiddlewares := s.PrepareCommonMiddlewares()
	apiMiddlewares := commonMiddlewares
	inferenceMiddlewares := commonMiddlewares
	if s.Config.ConfigStore == nil {
		logger.Error("auth middleware requires config store, skipping auth middleware initialization")
	} else {
		// Use a signed (stateless) ticket store when an encryption key is configured
		// so tickets are verifiable across nodes; otherwise fall back to in-memory.
		// NewSignedWSTicketStore handles empty key by degrading to in-memory mode.
		s.WSTicketStore = handlers.NewSignedWSTicketStore(encrypt.Key())
		// Initialize the temp-token service and register all scopes owned by the
		// handlers package. The service is the seam every "scoped, anonymous,
		// browser-only" workflow plugs into (currently just the MCP per-user OAuth
		// auth page
		s.TempTokens = temptoken.NewService(s.Config.ConfigStore, temptoken.NewRegistry())
		if regErr := handlers.RegisterTempTokenScopes(s.TempTokens); regErr != nil {
			s.WSTicketStore.Stop()
			s.WSTicketStore = nil
			return fmt.Errorf("failed to register temp token scopes: %v", regErr)
		}
		// Centralized janitor that reaps expired temp_tokens rows. Independent
		// of the per-user OAuth sweep so any future scope (not just mcp_auth)
		// benefits from the same cleanup loop without piggybacking on OAuth.
		s.TempTokenSweepWorker = temptoken.NewSweepWorker(s.TempTokens, logger)
		if s.TempTokenSweepWorker != nil {
			s.TempTokenSweepWorker.Start(s.Ctx)
		}
		s.StartOAuth2SweepWorker(s.Ctx, nil)
		// Hand the service to the OAuth provider so InitiateUserOAuthFlow mints
		// a mcp_auth token and embeds it as a URL fragment on the auth-page link.
		if s.Config.OAuthProvider != nil {
			s.Config.OAuthProvider.SetTempTokenService(s.TempTokens)
		}
		// Same wiring for the per-user-headers provider — mints
		// mcp_headers_auth tokens on the headers submission URL.
		if s.Config.MCPHeadersProvider != nil {
			s.Config.MCPHeadersProvider.SetTempTokenService(s.TempTokens)
		}
		s.AuthMiddleware, err = handlers.InitAuthMiddleware(s.Config.ConfigStore, s.WSTicketStore, s.TempTokens)
		if err != nil {
			s.WSTicketStore.Stop()
			s.WSTicketStore = nil
			if s.TempTokenSweepWorker != nil {
				s.TempTokenSweepWorker.Stop()
				s.TempTokenSweepWorker = nil
			}
			if s.OAuth2SweepWorker != nil {
				s.OAuth2SweepWorker.stop()
				s.OAuth2SweepWorker = nil
			}
			return fmt.Errorf("failed to initialize auth middleware: %v", err)
		}
		if ctx.Value(schemas.BifrostContextKeyIsEnterprise) == nil {
			apiMiddlewares = append(apiMiddlewares, s.AuthMiddleware.APIMiddleware())
		}
	}
	// Add semantic cache plugin embedding request executor if it exists
	semanticCachePlugin, err := lib.FindPluginAs[*semanticcache.Plugin](s.Config, semanticcache.PluginName)
	if err == nil && semanticCachePlugin != nil {
		semanticCachePlugin.SetEmbeddingRequestExecutor(s.Client.EmbeddingRequest)
	}

	// Initialize Sidekiq runner for background jobs
	if s.Config != nil && s.Config.ConfigStore != nil {
		s.SidekiqRunner = sidekiq.New(s.Config.ConfigStore, logger, 4, "")
	}

	// Register routes
	err = s.RegisterAPIRoutes(s.Ctx, s, apiMiddlewares...)
	if err != nil {
		if s.WSTicketStore != nil {
			s.WSTicketStore.Stop()
			s.WSTicketStore = nil
		}
		if s.TempTokenSweepWorker != nil {
			s.TempTokenSweepWorker.Stop()
			s.TempTokenSweepWorker = nil
		}
		if s.OAuth2SweepWorker != nil {
			s.OAuth2SweepWorker.stop()
			s.OAuth2SweepWorker = nil
		}
		return fmt.Errorf("failed to initialize routes: %v", err)
	}
	// Registering inference routes
	if ctx.Value(schemas.BifrostContextKeyIsEnterprise) == nil && s.AuthMiddleware != nil {
		inferenceMiddlewares = append(inferenceMiddlewares, s.AuthMiddleware.InferenceMiddleware())
	}
	// Once auth is done we will first add the Tracing middleware
	// Always add tracing middleware when tracer is enabled - it creates traces and sets traceID in context
	// The observability plugins are optional (can be empty if only logging is enabled)
	// Curating observability plugins
	observabilityPlugins := s.CollectObservabilityPlugins()
	// This enables the central streaming accumulator for both use cases
	// Initializing tracer with embedded streaming accumulator
	traceStore := tracing.NewTraceStore(60*time.Minute, logger)
	tracer := tracing.NewTracer(traceStore, s.Config.ModelCatalog, logger)
	tracer.SetObservabilityPlugins(observabilityPlugins)
	s.Client.SetTracer(tracer)
	s.TracingMiddleware = handlers.NewTracingMiddleware(tracer)
	// TransportInterceptor must be inside TracingMiddleware so that the tracing defer
	// runs AFTER transport post-hooks (capturing HTTPTransportPostHook plugin logs).
	// Order: Tracing.pre → TransportInterceptor.pre → handler → TransportInterceptor.post → Tracing.defer
	inferenceMiddlewares = append([]schemas.BifrostHTTPMiddleware{handlers.TransportInterceptorMiddleware(s.Config)}, inferenceMiddlewares...)
	inferenceMiddlewares = append([]schemas.BifrostHTTPMiddleware{s.TracingMiddleware.Middleware()}, inferenceMiddlewares...)

	err = s.RegisterInferenceRoutes(s.Ctx, inferenceMiddlewares...)
	if err != nil {
		if s.WSTicketStore != nil {
			s.WSTicketStore.Stop()
			s.WSTicketStore = nil
		}
		if s.TempTokenSweepWorker != nil {
			s.TempTokenSweepWorker.Stop()
			s.TempTokenSweepWorker = nil
		}
		if s.OAuth2SweepWorker != nil {
			s.OAuth2SweepWorker.stop()
			s.OAuth2SweepWorker = nil
		}
		return fmt.Errorf("failed to initialize inference routes: %v", err)
	}
	// Dial configured MCP clients now that every plugin is registered in the core.
	// Construction (bifrost.Init) no longer connects MCP, so connecting here ensures
	// each client's PreMCPConnectionHook runs against the full plugin set rather than
	// the point-in-time snapshot captured at Init (which would skip plugins — e.g.
	// enterprise ones — registered after that snapshot, causing the client to fail
	// and only recover on a later health-monitor reconnect).
	s.Client.ConnectConfiguredMCPClients(s.Ctx)
	// Serve a minimal robots.txt so crawlers/CLI tools (e.g. Claude Code) don't
	// trigger 404 warnings when probing the host before marketplace fetches.
	s.Router.GET("/robots.txt", func(ctx *fasthttp.RequestCtx) {
		ctx.SetContentType("text/plain; charset=utf-8")
		ctx.SetBodyString("User-agent: *\nAllow: /\n")
	})
	// Register UI handler
	s.RegisterUIRoutes()

	// Start the Sidekiq dispatcher: on every node it periodically claims pending and
	// stale (orphaned) jobs, with an atomic claim guaranteeing exactly one node runs
	// each job. Subsumes startup recovery of jobs left behind by a crash or restart.
	if s.SidekiqRunner != nil {
		s.SidekiqDispatcherStop = s.SidekiqRunner.StartDispatcher(sidekiq.DispatchInterval, sidekiq.StaleAfter)
	}

	// Checking if config has server config and use it to set read buffer size
	logger.Debug("server read buffer size: %d", s.Config.ServerConfig.ReadBufferSize)
	// Create fasthttp server instance
	s.Server = &fasthttp.Server{
		Handler:            handlers.SecurityHeadersMiddleware()(s.CORSMiddleware.Middleware()(handlers.RequestDecompressionMiddleware(s.Config)(s.Router.Handler))),
		MaxRequestBodySize: s.Config.ClientConfig.MaxRequestBodySizeMB * 1024 * 1024,
		ReadBufferSize:     s.Config.ServerConfig.ReadBufferSize,
	}
	startSkillsOrphanCleanupWorker(s.Ctx, s.Config)
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
		logger.Info("successfully started bifrost, serving UI on http://%s", serverAddr)
		if err := s.Server.Serve(ln); err != nil {
			errChan <- err
		}
	}()
	// Wait for either termination signal or server error
	select {
	case sig := <-sigChan:
		logger.Info("received signal %v, initiating graceful shutdown...", sig)
		if s.IntegrationHandler != nil {
			logger.Info("closing realtime transport sessions...")
			s.IntegrationHandler.Close()
		}
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
			// Cleanup server-specific components
			if s.LogsCleaner != nil {
				logger.Info("stopping log retention cleaner...")
				s.LogsCleaner.StopCleanupRoutine()
			}
			if s.AsyncJobCleaner != nil {
				logger.Info("stopping async job cleaner...")
				s.AsyncJobCleaner.StopCleanupRoutine()
			}
			if s.WebhookDispatcher != nil {
				logger.Info("stopping webhook dispatcher...")
				s.WebhookDispatcher.Stop()
			}
			if s.WSTicketStore != nil {
				logger.Info("stopping ws ticket store...")
				s.WSTicketStore.Stop()
			}
			if s.TempTokenSweepWorker != nil {
				logger.Info("stopping temp-token sweep worker...")
				s.TempTokenSweepWorker.Stop()
			}
			if s.OAuth2SweepWorker != nil {
				logger.Info("stopping oauth2 sweep worker...")
				s.OAuth2SweepWorker.stop()
				s.OAuth2SweepWorker = nil
			}
			if s.SidekiqDispatcherStop != nil {
				logger.Info("stopping sidekiq dispatcher...")
				s.SidekiqDispatcherStop()
			}
			if s.SidekiqRunner != nil {
				logger.Info("stopping sidekiq runner...")
				s.SidekiqRunner.Shutdown()
			}
			if s.devPprofHandler != nil {
				logger.Info("stopping dev pprof handler...")
				s.devPprofHandler.Cleanup()
			}
			if s.wsPool != nil {
				logger.Info("closing websocket connection pool...")
				s.wsPool.Close()
			}
			// Cleanup Config and all its background components
			if s.Config != nil {
				s.Config.Close(shutdownCtx)
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
		if s.IntegrationHandler != nil {
			s.IntegrationHandler.Close()
		}
		if s.wsPool != nil {
			s.wsPool.Close()
		}
		return err
	}
	return nil
}
