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
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"syscall"
	"time"

	"github.com/bytedance/sonic"
	"github.com/fasthttp/router"
	"github.com/google/uuid"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	dynamicPlugins "github.com/maximhq/bifrost/framework/plugins"
	"github.com/maximhq/bifrost/framework/tracing"
	"github.com/maximhq/bifrost/plugins/governance"
	"github.com/maximhq/bifrost/plugins/litellmcompat"
	"github.com/maximhq/bifrost/plugins/logging"
	"github.com/maximhq/bifrost/plugins/maxim"
	"github.com/maximhq/bifrost/plugins/otel"
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
	ReloadPlugin(ctx context.Context, name string, path *string, pluginConfig any) error
	RemovePlugin(ctx context.Context, name string) error
	GetPluginStatus(ctx context.Context) []schemas.PluginStatus
	GetModelsForProvider(provider schemas.ModelProvider) []string
	UpdateAuthConfig(ctx context.Context, authConfig *configstore.AuthConfig) error
	ReloadClientConfigFromConfigStore(ctx context.Context) error
	ReloadPricingManager(ctx context.Context) error
	ForceReloadPricing(ctx context.Context) error
	ReloadProxyConfig(ctx context.Context, config *tables.GlobalProxyConfig) error
	ReloadHeaderFilterConfig(ctx context.Context, config *tables.GlobalHeaderFilterConfig) error
	UpdateDropExcessRequests(ctx context.Context, value bool)
	UpdateMCPToolManagerConfig(ctx context.Context, maxAgentDepth int, toolExecutionTimeoutInSeconds int, codeModeBindingLevel string) error
	ReloadTeam(ctx context.Context, id string) (*tables.TableTeam, error)
	RemoveTeam(ctx context.Context, id string) error
	ReloadCustomer(ctx context.Context, id string) (*tables.TableCustomer, error)
	RemoveCustomer(ctx context.Context, id string) error
	ReloadVirtualKey(ctx context.Context, id string) (*tables.TableVirtualKey, error)
	RemoveVirtualKey(ctx context.Context, id string) error
	ReloadModelConfig(ctx context.Context, id string) (*tables.TableModelConfig, error)
	RemoveModelConfig(ctx context.Context, id string) error
	ReloadProvider(ctx context.Context, provider schemas.ModelProvider) (*tables.TableProvider, error)
	RemoveProvider(ctx context.Context, provider schemas.ModelProvider) error
	GetGovernanceData() *governance.GovernanceData
	AddMCPClient(ctx context.Context, clientConfig schemas.MCPClientConfig) error
	RemoveMCPClient(ctx context.Context, id string) error
	EditMCPClient(ctx context.Context, id string, updatedConfig schemas.MCPClientConfig) error
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

	LogLevel       string
	LogOutputStyle string
	LogsCleaner    *logstore.LogsCleaner

	PluginsMutex      sync.RWMutex
	Plugins           []schemas.Plugin
	pluginStatusMutex sync.RWMutex
	pluginStatus      []schemas.PluginStatus

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

// GetDefaultConfigDir returns the OS-specific default configuration directory for Bifrost.
// This follows standard conventions:
// - Linux/macOS: ~/.config/bifrost
// - Windows: %APPDATA%\bifrost
// - If appDir is provided (non-empty), it returns that instead
func GetDefaultConfigDir(appDir string) string {
	// If appDir is provided, use it directly
	if appDir != "" {
		return appDir
	}

	// Get OS-specific config directory
	var configDir string
	switch runtime.GOOS {
	case "windows":
		// Windows: %APPDATA%\bifrost
		if appData := os.Getenv("APPDATA"); appData != "" {
			configDir = filepath.Join(appData, "bifrost")
		} else {
			// Fallback to user home directory
			if homeDir, err := os.UserHomeDir(); err == nil {
				configDir = filepath.Join(homeDir, "AppData", "Roaming", "bifrost")
			}
		}
	default:
		// Linux, macOS and other Unix-like systems: ~/.config/bifrost
		if homeDir, err := os.UserHomeDir(); err == nil {
			configDir = filepath.Join(homeDir, ".config", "bifrost")
		}
	}

	// If we couldn't determine the config directory, fall back to current directory
	if configDir == "" {
		configDir = "./bifrost-data"
	}

	return configDir
}

// MarshalPluginConfig marshals the plugin configuration
func MarshalPluginConfig[T any](source any) (*T, error) {
	// If its a *T, then we will confirm
	if config, ok := source.(*T); ok {
		return config, nil
	}
	// Initialize a new instance for unmarshaling
	config := new(T)
	// If its a map[string]any, then we will JSON parse and confirm
	if configMap, ok := source.(map[string]any); ok {
		configString, err := sonic.Marshal(configMap)
		if err != nil {
			return nil, err
		}
		if err := sonic.Unmarshal([]byte(configString), config); err != nil {
			return nil, err
		}
		return config, nil
	}
	// If its a string, then we will JSON parse and confirm
	if configStr, ok := source.(string); ok {
		if err := sonic.Unmarshal([]byte(configStr), config); err != nil {
			return nil, err
		}
		return config, nil
	}
	return nil, fmt.Errorf("invalid config type")
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

// LoadPlugin loads a plugin by name and returns it as type T.
func LoadPlugin[T schemas.Plugin](ctx context.Context, name string, path *string, pluginConfig any, bifrostConfig *lib.Config) (T, error) {
	var zero T
	if path != nil {
		logger.Info("loading dynamic plugin %s from path %s", name, *path)
		// Load dynamic plugin
		plugins, err := dynamicPlugins.LoadPlugins(bifrostConfig.PluginLoader, &dynamicPlugins.Config{
			Plugins: []dynamicPlugins.DynamicPluginConfig{
				{
					Path:    *path,
					Name:    name,
					Enabled: true,
					Config:  pluginConfig,
				},
			},
		})
		if err != nil {
			return zero, fmt.Errorf("failed to load dynamic plugin %s: %v", name, err)
		}
		if len(plugins) == 0 {
			return zero, fmt.Errorf("dynamic plugin %s returned no instances", name)
		}
		if p, ok := any(plugins[0]).(T); ok {
			return p, nil
		}
		return zero, fmt.Errorf("dynamic plugin type mismatch")
	}
	switch name {
	case telemetry.PluginName:
		plugin, err := telemetry.Init(&telemetry.Config{
			CustomLabels: bifrostConfig.ClientConfig.PrometheusLabels,
		}, bifrostConfig.PricingManager, logger)
		if err != nil {
			return zero, err
		}
		if p, ok := any(plugin).(T); ok {
			return p, nil
		}
		return zero, fmt.Errorf("telemetry plugin type mismatch")
	case logging.PluginName:
		loggingConfig, err := MarshalPluginConfig[logging.Config](pluginConfig)
		if err != nil {
			return zero, fmt.Errorf("failed to marshal logging plugin config: %v", err)
		}
		plugin, err := logging.Init(ctx, loggingConfig, logger, bifrostConfig.LogsStore, bifrostConfig.PricingManager)
		if err != nil {
			return zero, err
		}
		if p, ok := any(plugin).(T); ok {
			return p, nil
		}
		return zero, fmt.Errorf("logging plugin type mismatch")
	case governance.PluginName:
		governanceConfig, err := MarshalPluginConfig[governance.Config](pluginConfig)
		if err != nil {
			return zero, fmt.Errorf("failed to marshal governance plugin config: %v", err)
		}
		inMemoryStore := &GovernanceInMemoryStore{
			Config: bifrostConfig,
		}
		plugin, err := governance.Init(ctx, governanceConfig, logger, bifrostConfig.ConfigStore, bifrostConfig.GovernanceConfig, bifrostConfig.PricingManager, inMemoryStore)
		if err != nil {
			return zero, err
		}
		if p, ok := any(plugin).(T); ok {
			return p, nil
		}
		return zero, fmt.Errorf("governance plugin type mismatch")
	case maxim.PluginName:
		// And keep backward compatibility for ENV variables
		maximConfig, err := MarshalPluginConfig[maxim.Config](pluginConfig)
		if err != nil {
			return zero, fmt.Errorf("failed to marshal maxim plugin config: %v", err)
		}
		plugin, err := maxim.Init(maximConfig, logger)
		if err != nil {
			return zero, err
		}
		if p, ok := any(plugin).(T); ok {
			return p, nil
		}
		return zero, fmt.Errorf("maxim plugin type mismatch")
	case semanticcache.PluginName:
		semanticcacheConfig, err := MarshalPluginConfig[semanticcache.Config](pluginConfig)
		if err != nil {
			return zero, fmt.Errorf("failed to marshal semantic cache plugin config: %v", err)
		}
		plugin, err := semanticcache.Init(ctx, semanticcacheConfig, logger, bifrostConfig.VectorStore)
		if err != nil {
			return zero, err
		}
		if p, ok := any(plugin).(T); ok {
			return p, nil
		}
		return zero, fmt.Errorf("semantic cache plugin type mismatch")
	case otel.PluginName:
		otelConfig, err := MarshalPluginConfig[otel.Config](pluginConfig)
		if err != nil {
			return zero, fmt.Errorf("failed to marshal otel plugin config: %v", err)
		}
		plugin, err := otel.Init(ctx, otelConfig, logger, bifrostConfig.PricingManager, handlers.GetVersion())
		if err != nil {
			return zero, err
		}
		if p, ok := any(plugin).(T); ok {
			return p, nil
		}
		return zero, fmt.Errorf("otel plugin type mismatch")
	case litellmcompat.PluginName:
		litellmConfig, err := MarshalPluginConfig[litellmcompat.Config](pluginConfig)
		if err != nil {
			return zero, fmt.Errorf("failed to marshal litellmcompat plugin config: %v", err)
		}
		plugin, err := litellmcompat.Init(*litellmConfig, logger)
		if err != nil {
			return zero, err
		}
		if p, ok := any(plugin).(T); ok {
			return p, nil
		}
		return zero, fmt.Errorf("litellmcompat plugin type mismatch")
	}
	return zero, fmt.Errorf("plugin %s not found", name)
}

// LoadPlugins loads the plugins for the server.
func LoadPlugins(ctx context.Context, config *lib.Config) ([]schemas.Plugin, []schemas.PluginStatus, error) {
	var err error
	pluginStatus := []schemas.PluginStatus{}
	plugins := []schemas.Plugin{}
	// Initialize telemetry plugin
	promPlugin, err := LoadPlugin[*telemetry.PrometheusPlugin](ctx, telemetry.PluginName, nil, nil, config)
	if err != nil {
		logger.Error("failed to initialize telemetry plugin: %v", err)
		pluginStatus = append(pluginStatus, schemas.PluginStatus{
			Name:   telemetry.PluginName,
			Status: schemas.PluginStatusError,
			Logs:   []string{fmt.Sprintf("error initializing telemetry plugin %v", err)},
		})
	} else {
		plugins = append(plugins, promPlugin)
		pluginStatus = append(pluginStatus, schemas.PluginStatus{
			Name:   telemetry.PluginName,
			Status: schemas.PluginStatusActive,
			Logs:   []string{"telemetry plugin initialized successfully"},
		})
	}
	// Initializing logger plugin
	var loggingPlugin *logging.LoggerPlugin
	if config.ClientConfig.EnableLogging && config.LogsStore != nil {
		// Use dedicated logs database with high-scale optimizations
		loggingPlugin, err = LoadPlugin[*logging.LoggerPlugin](ctx, logging.PluginName, nil, &logging.Config{
			DisableContentLogging: &config.ClientConfig.DisableContentLogging,
		}, config)
		if err != nil {
			logger.Error("failed to initialize logging plugin: %v", err)
			pluginStatus = append(pluginStatus, schemas.PluginStatus{
				Name:   logging.PluginName,
				Status: schemas.PluginStatusError,
				Logs:   []string{fmt.Sprintf("error initializing logging plugin %v", err)},
			})
		} else {
			plugins = append(plugins, loggingPlugin)
			pluginStatus = append(pluginStatus, schemas.PluginStatus{
				Name:   logging.PluginName,
				Status: schemas.PluginStatusActive,
				Logs:   []string{"logging plugin initialized successfully"},
			})
		}
	} else {
		pluginStatus = append(pluginStatus, schemas.PluginStatus{
			Name:   logging.PluginName,
			Status: schemas.PluginStatusDisabled,
			Logs:   []string{"logging plugin disabled"},
		})
	}
	// Initializing governance plugin
	if config.ClientConfig.EnableGovernance && ctx.Value(schemas.BifrostContextKeyIsEnterprise) == nil {
		// Initialize governance plugin
		governancePlugin, err := LoadPlugin[*governance.GovernancePlugin](ctx, governance.PluginName, nil, &governance.Config{
			IsVkMandatory: &config.ClientConfig.EnforceGovernanceHeader,
		}, config)
		if err != nil {
			logger.Error("failed to initialize governance plugin: %s", err.Error())
			pluginStatus = append(pluginStatus, schemas.PluginStatus{
				Name:   governance.PluginName,
				Status: schemas.PluginStatusError,
				Logs:   []string{fmt.Sprintf("error initializing governance plugin %v", err)},
			})
		} else if governancePlugin != nil {
			plugins = append(plugins, governancePlugin)
			pluginStatus = append(pluginStatus, schemas.PluginStatus{
				Name:   governance.PluginName,
				Status: schemas.PluginStatusActive,
				Logs:   []string{"governance plugin initialized successfully"},
			})
		}
	} else {
		pluginStatus = append(pluginStatus, schemas.PluginStatus{
			Name:   governance.PluginName,
			Status: schemas.PluginStatusDisabled,
			Logs:   []string{"governance plugin disabled"},
		})
	}
	for _, plugin := range config.PluginConfigs {
		// Skip built-in plugins that are already handled above
		if plugin.Name == telemetry.PluginName || plugin.Name == logging.PluginName || plugin.Name == governance.PluginName {
			continue
		}
		if !plugin.Enabled {
			pluginStatus = append(pluginStatus, schemas.PluginStatus{
				Name:   plugin.Name,
				Status: schemas.PluginStatusDisabled,
				Logs:   []string{fmt.Sprintf("plugin %s disabled", plugin.Name)},
			})
			continue
		}
		pluginInstance, err := LoadPlugin[schemas.Plugin](ctx, plugin.Name, plugin.Path, plugin.Config, config)
		if err != nil {
			if slices.Contains(enterprisePlugins, plugin.Name) {
				continue
			}
			logger.Error("failed to load plugin %s: %v", plugin.Name, err)
			pluginStatus = append(pluginStatus, schemas.PluginStatus{
				Name:   plugin.Name,
				Status: schemas.PluginStatusError,
				Logs:   []string{fmt.Sprintf("error loading plugin %s: %v", plugin.Name, err)},
			})
		} else {
			plugins = append(plugins, pluginInstance)
			pluginStatus = append(pluginStatus, schemas.PluginStatus{
				Name:   plugin.Name,
				Status: schemas.PluginStatusActive,
				Logs:   []string{fmt.Sprintf("plugin %s initialized successfully", plugin.Name)},
			})
		}
	}
	// Initialize litellmcompat plugin if LiteLLM fallbacks are enabled
	// We initialize litellm plugin at the end to make sure it runs after all the load-balancing plugins
	if config.ClientConfig.EnableLiteLLMFallbacks {
		litellmCompatPlugin, err := LoadPlugin[schemas.Plugin](ctx, litellmcompat.PluginName, nil, &litellmcompat.Config{Enabled: true}, config)
		if err != nil {
			logger.Error("failed to initialize litellmcompat plugin: %v", err)
			pluginStatus = append(pluginStatus, schemas.PluginStatus{
				Name:   litellmcompat.PluginName,
				Status: schemas.PluginStatusError,
				Logs:   []string{fmt.Sprintf("error initializing litellmcompat plugin %v", err)},
			})
		} else {
			plugins = append(plugins, litellmCompatPlugin)
			pluginStatus = append(pluginStatus, schemas.PluginStatus{
				Name:   litellmcompat.PluginName,
				Status: schemas.PluginStatusActive,
				Logs:   []string{"litellmcompat plugin initialized successfully"},
			})
		}
	} else {
		pluginStatus = append(pluginStatus, schemas.PluginStatus{
			Name:   litellmcompat.PluginName,
			Status: schemas.PluginStatusDisabled,
			Logs:   []string{"litellmcompat plugin disabled"},
		})
	}

	// Atomically publish the plugin state
	config.Plugins.Store(&plugins)

	return plugins, pluginStatus, nil
}

// FindPluginByName retrieves a plugin by name and returns it as type T.
// T must satisfy schemas.Plugin.
func FindPluginByName[T schemas.Plugin](plugins []schemas.Plugin, name string) (T, error) {
	for _, plugin := range plugins {
		if plugin.GetName() == name {
			if p, ok := plugin.(T); ok {
				return p, nil
			}
			var zero T
			return zero, fmt.Errorf("plugin %q found but type mismatch", name)
		}
	}
	var zero T
	return zero, fmt.Errorf("plugin %q not found", name)
}

// AddMCPClient adds a new MCP client to the in-memory store
func (s *BifrostHTTPServer) AddMCPClient(ctx context.Context, clientConfig schemas.MCPClientConfig) error {
	if err := s.Config.AddMCPClient(ctx, clientConfig); err != nil {
		return err
	}
	if err := s.MCPServerHandler.SyncAllMCPServers(ctx); err != nil {
		logger.Warn("failed to sync MCP servers after adding client: %v", err)
	}
	return nil
}

// EditMCPClient edits an MCP client in the in-memory store
func (s *BifrostHTTPServer) EditMCPClient(ctx context.Context, id string, updatedConfig schemas.MCPClientConfig) error {
	if err := s.Config.EditMCPClient(ctx, id, updatedConfig); err != nil {
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
func (s *BifrostHTTPServer) ExecuteChatMCPTool(ctx context.Context, toolCall schemas.ChatAssistantMessageToolCall) (*schemas.ChatMessage, *schemas.BifrostError) {
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

// getGovernancePlugin safely retrieves the governance plugin with proper locking.
// It acquires a read lock, finds the plugin, releases the lock, performs type assertion,
// and returns the BaseGovernancePlugin implementation or an error.
func (s *BifrostHTTPServer) getGovernancePlugin() (governance.BaseGovernancePlugin, error) {
	governancePluginName := governance.PluginName
	if name, ok := s.Ctx.Value(schemas.BifrostContextKeyGovernancePluginName).(string); ok && name != "" {
		governancePluginName = name
	}
	s.PluginsMutex.RLock()
	plugin, err := FindPluginByName[schemas.Plugin](s.Plugins, governancePluginName)
	s.PluginsMutex.RUnlock()
	if err != nil {
		return nil, err
	}
	if plugin == nil {
		return nil, fmt.Errorf("governance plugin not found")
	}
	governancePlugin, ok := plugin.(governance.BaseGovernancePlugin)
	if !ok {
		return nil, fmt.Errorf("governance plugin does not implement BaseGovernancePlugin")
	}
	return governancePlugin, nil
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

// ReloadProvider reloads a provider from the database into in-memory store
// If usage was modified (e.g., reset due to config change), syncs it back to DB
func (s *BifrostHTTPServer) ReloadProvider(ctx context.Context, provider schemas.ModelProvider) (*tables.TableProvider, error) {
	// Sync model level budgets in governance plugin
	providerInfo, err := s.Config.ConfigStore.GetProvider(ctx, provider)
	if err != nil {
		logger.Error("failed to load provider: %v", err)
		return nil, err
	}
	governancePlugin, err := s.getGovernancePlugin()
	if err != nil {
		return nil, err
	}
	// Update in memory and get back the potentially modified provider
	updatedProvider := governancePlugin.GetGovernanceStore().UpdateProviderInMemory(providerInfo)
	if updatedProvider == nil {
		return providerInfo, nil
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
	// Syncing models
	if s.Config == nil || s.Config.PricingManager == nil {
		return nil, fmt.Errorf("pricing manager not found")
	}
	if s.Client == nil {
		return nil, fmt.Errorf("bifrost client not found")
	}
	bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
	defer bfCtx.Cancel()
	allModels, bifrostErr := s.Client.ListModelsRequest(bfCtx, &schemas.BifrostListModelsRequest{
		Provider: provider,
	})
	if bifrostErr != nil {
		return nil, fmt.Errorf("failed to update provider model catalog: failed to list all models: %s", bifrost.GetErrorMessage(bifrostErr))
	}
	s.Config.PricingManager.DeleteModelDataForProvider(provider)
	s.Config.PricingManager.AddModelDataToPool(allModels)
	return updatedProvider, nil
}

// RemoveProvider removes a provider from the in-memory store
func (s *BifrostHTTPServer) RemoveProvider(ctx context.Context, provider schemas.ModelProvider) error {
	err := s.Config.RemoveProvider(ctx, provider)
	// For not found, we continue to remove the provider from the client
	if err != nil && !errors.Is(err, configstore.ErrNotFound) {
		logger.Error("failed to remove provider from config: %v", err)
		return err
	}
	err = s.Client.RemoveProvider(provider)
	if err != nil {
		logger.Error("failed to remove provider: %v", err)
		return err
	}
	governancePlugin, err := s.getGovernancePlugin()
	if err != nil {
		return err
	}
	governancePlugin.GetGovernanceStore().DeleteProviderInMemory(string(provider))
	if s.Config == nil || s.Config.PricingManager == nil {
		return fmt.Errorf("pricing manager not found")
	}
	s.Config.PricingManager.DeleteModelDataForProvider(provider)

	return nil
}

// GetGovernanceData returns the governance data
func (s *BifrostHTTPServer) GetGovernanceData() *governance.GovernanceData {
	governancePluginName := governance.PluginName
	if name, ok := s.Ctx.Value(schemas.BifrostContextKeyGovernancePluginName).(string); ok && name != "" {
		governancePluginName = name
	}
	s.PluginsMutex.RLock()
	governancePlugin, err := FindPluginByName[schemas.Plugin](s.Plugins, governancePluginName)
	s.PluginsMutex.RUnlock()
	if err != nil {
		return nil
	}
	// Check if GetGovernanceStore method is implemented
	if governancePlugin, ok := governancePlugin.(governance.BaseGovernancePlugin); ok {
		return governancePlugin.GetGovernanceStore().GetGovernanceData()
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
		s.Client.ReloadConfig(schemas.BifrostConfig{
			Account:            account,
			InitialPoolSize:    s.Config.ClientConfig.InitialPoolSize,
			DropExcessRequests: s.Config.ClientConfig.DropExcessRequests,
			Plugins:            s.Config.GetLoadedPlugins(),
			MCPConfig:          s.Config.MCPConfig,
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
	if authConfig.IsEnabled && (authConfig.AdminUserName == "" || authConfig.AdminPassword == "") {
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

// UpdatePluginStatus updates the status of a plugin
func (s *BifrostHTTPServer) UpdatePluginStatus(name string, status string, logs []string) error {
	s.pluginStatusMutex.Lock()
	defer s.pluginStatusMutex.Unlock()
	// Remove plugin status if already exists
	for i, pluginStatus := range s.pluginStatus {
		if pluginStatus.Name == name {
			s.pluginStatus = append(s.pluginStatus[:i], s.pluginStatus[i+1:]...)
			break
		}
	}
	logsCopy := make([]string, len(logs))
	copy(logsCopy, logs)
	// Add new plugin status
	s.pluginStatus = append(s.pluginStatus, schemas.PluginStatus{
		Name:   name,
		Status: status,
		Logs:   logsCopy,
	})
	return nil
}

// DeletePluginStatus completely removes a plugin status entry
func (s *BifrostHTTPServer) DeletePluginStatus(name string) {
	s.pluginStatusMutex.Lock()
	defer s.pluginStatusMutex.Unlock()
	for i, pluginStatus := range s.pluginStatus {
		if pluginStatus.Name == name {
			s.pluginStatus = append(s.pluginStatus[:i], s.pluginStatus[i+1:]...)
			return
		}
	}
}

// GetPluginStatus returns the status of all plugins
func (s *BifrostHTTPServer) GetPluginStatus(ctx context.Context) []schemas.PluginStatus {
	s.pluginStatusMutex.RLock()
	defer s.pluginStatusMutex.RUnlock()
	// De-duplicate by name, keeping the last occurrence (most recent status)
	statusMap := make(map[string]schemas.PluginStatus)
	order := make([]string, 0, len(s.pluginStatus))
	for _, status := range s.pluginStatus {
		if _, exists := statusMap[status.Name]; !exists {
			order = append(order, status.Name)
		}
		statusMap[status.Name] = status
	}
	result := make([]schemas.PluginStatus, 0, len(statusMap))
	for _, name := range order {
		result = append(result, statusMap[name])
	}
	return result
}

// SyncLoadedPlugin syncs the loaded plugin to the Bifrost core.
// configName is the name from the configuration/database, used for status tracking.
func (s *BifrostHTTPServer) SyncLoadedPlugin(ctx context.Context, configName string, plugin schemas.Plugin) error {
	if err := s.Client.ReloadPlugin(plugin); err != nil {
		s.UpdatePluginStatus(configName, schemas.PluginStatusError, []string{fmt.Sprintf("error reloading plugin %s: %v", configName, err)})
		return err
	}
	s.UpdatePluginStatus(configName, schemas.PluginStatusActive, []string{fmt.Sprintf("plugin %s reloaded successfully", configName)})
	// CAS retry loop (matching bifrost.go pattern)
	for {
		oldPlugins := s.Config.Plugins.Load()
		oldPluginsSlice := []schemas.Plugin{}
		if oldPlugins != nil {
			oldPluginsSlice = *oldPlugins
		}

		// Create new slice with replaced/appended plugin
		newPlugins := make([]schemas.Plugin, len(oldPluginsSlice))
		copy(newPlugins, oldPluginsSlice)

		found := false
		for i, existing := range newPlugins {
			if existing.GetName() == plugin.GetName() {
				newPlugins[i] = plugin
				found = true
				break
			}
		}
		if !found {
			newPlugins = append(newPlugins, plugin)
		}

		// Atomic compare-and-swap
		if s.Config.Plugins.CompareAndSwap(oldPlugins, &newPlugins) {
			s.PluginsMutex.Lock()
			defer s.PluginsMutex.Unlock()
			s.Plugins = newPlugins // Keep BifrostHTTPServer.Plugins in sync
			return nil
		}
		// Retry on contention (extremely rare for plugin updates)
	}
}

// ReloadPlugin reloads a plugin with new instance and updates Bifrost core.
// Uses atomic CompareAndSwap with retry loop to handle concurrent updates safely.
func (s *BifrostHTTPServer) ReloadPlugin(ctx context.Context, name string, path *string, pluginConfig any) error {
	logger.Debug("reloading plugin %s", name)
	newPlugin, err := LoadPlugin[schemas.Plugin](ctx, name, path, pluginConfig, s.Config)
	if err != nil {
		s.UpdatePluginStatus(name, schemas.PluginStatusError, []string{fmt.Sprintf("error loading plugin %s: %v", name, err)})
		return err
	}
	err = s.SyncLoadedPlugin(ctx, name, newPlugin)
	if err != nil {
		return err
	}
	// Here if its observability plugin, we need to reload it
	if _, ok := newPlugin.(schemas.ObservabilityPlugin); ok {
		// We will re-collect the observability plugins from the plugins list
		observabilityPlugins := []schemas.ObservabilityPlugin{}
		s.PluginsMutex.RLock()
		defer s.PluginsMutex.RUnlock()
		for _, plugin := range s.Plugins {
			if observabilityPlugin, ok := plugin.(schemas.ObservabilityPlugin); ok {
				observabilityPlugins = append(observabilityPlugins, observabilityPlugin)
			}
		}
		if len(observabilityPlugins) > 0 {
			s.TracingMiddleware.SetObservabilityPlugins(observabilityPlugins)
		}
	}
	return nil
}

// ReloadPricingManager reloads the pricing manager
func (s *BifrostHTTPServer) ReloadPricingManager(ctx context.Context) error {
	if s.Config == nil || s.Config.PricingManager == nil {
		return fmt.Errorf("pricing manager not found")
	}
	if s.Config.FrameworkConfig == nil || s.Config.FrameworkConfig.Pricing == nil {
		return fmt.Errorf("framework config not found")
	}
	return s.Config.PricingManager.ReloadPricing(ctx, s.Config.FrameworkConfig.Pricing)
}

// ForceReloadPricing triggers an immediate pricing sync and resets the sync timer
func (s *BifrostHTTPServer) ForceReloadPricing(ctx context.Context) error {
	if s.Config == nil || s.Config.PricingManager == nil {
		return fmt.Errorf("pricing manager not found")
	}
	return s.Config.PricingManager.ForceReloadPricing(ctx)
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
	if s.Config == nil || s.Config.PricingManager == nil {
		return []string{}
	}
	return s.Config.PricingManager.GetModelsForProvider(provider)
}

// RemovePlugin removes a plugin from the server.
// Uses atomic CompareAndSwap with retry loop to handle concurrent updates safely.
func (s *BifrostHTTPServer) RemovePlugin(ctx context.Context, name string) error {
	// Get plugin
	plugin, _ := FindPluginByName[schemas.Plugin](s.Plugins, name)
	if err := s.Client.RemovePlugin(name); err != nil {
		return err
	}
	isDisabled := ctx.Value("isDisabled")
	if isDisabled != nil && isDisabled.(bool) {
		s.UpdatePluginStatus(name, schemas.PluginStatusDisabled, []string{fmt.Sprintf("plugin %s is disabled", name)})
	} else {
		// Plugin is being deleted - remove the status entry completely
		s.DeletePluginStatus(name)
	}

	// CAS retry loop (matching bifrost.go pattern)
	for {
		oldPlugins := s.Config.Plugins.Load()
		oldPluginsSlice := []schemas.Plugin{}
		if oldPlugins != nil {
			oldPluginsSlice = *oldPlugins
		}
		// Create new slice without the removed plugin
		newPlugins := make([]schemas.Plugin, 0, len(oldPluginsSlice))
		for _, existing := range oldPluginsSlice {
			if existing.GetName() != name {
				newPlugins = append(newPlugins, existing)
			}
		}
		// Atomic compare-and-swap
		if s.Config.Plugins.CompareAndSwap(oldPlugins, &newPlugins) {
			s.PluginsMutex.Lock()
			s.Plugins = newPlugins // Keep BifrostHTTPServer.Plugins in sync
			s.PluginsMutex.Unlock()
			break
		}
		// Retry on contention (extremely rare for plugin updates)
	}
	// Here if its observability plugin, we need to reload it
	if _, ok := plugin.(schemas.ObservabilityPlugin); ok {
		// We will re-collect the observability plugins from the plugins list
		observabilityPlugins := []schemas.ObservabilityPlugin{}
		s.PluginsMutex.RLock()
		defer s.PluginsMutex.RUnlock()
		for _, plugin := range s.Plugins {
			if observabilityPlugin, ok := plugin.(schemas.ObservabilityPlugin); ok {
				observabilityPlugins = append(observabilityPlugins, observabilityPlugin)
			}
		}
		s.TracingMiddleware.SetObservabilityPlugins(observabilityPlugins)
	}
	return nil
}

// RegisterInferenceRoutes initializes the routes for the inference handler
func (s *BifrostHTTPServer) RegisterInferenceRoutes(ctx context.Context, middlewares ...schemas.BifrostHTTPMiddleware) error {
	inferenceHandler := handlers.NewInferenceHandler(s.Client, s.Config)
	integrationHandler := handlers.NewIntegrationHandler(s.Client, s.Config)

	integrationHandler.RegisterRoutes(s.Router, middlewares...)
	inferenceHandler.RegisterRoutes(s.Router, middlewares...)
	return nil
}

// RegisterAPIRoutes initializes the routes for the Bifrost HTTP server.
func (s *BifrostHTTPServer) RegisterAPIRoutes(ctx context.Context, callbacks ServerCallbacks, middlewares ...schemas.BifrostHTTPMiddleware) error {
	var err error
	// Initializing plugin specific handlers
	var loggingHandler *handlers.LoggingHandler
	loggerPlugin, _ := FindPluginByName[*logging.LoggerPlugin](s.Plugins, logging.PluginName)
	if loggerPlugin != nil {
		loggingHandler = handlers.NewLoggingHandler(loggerPlugin.GetPluginLogManager(), s)
	}
	var governanceHandler *handlers.GovernanceHandler
	governancePluginName := governance.PluginName
	if name, ok := ctx.Value(schemas.BifrostContextKeyGovernancePluginName).(string); ok && name != "" {
		governancePluginName = name
	}
	governancePlugin, _ := FindPluginByName[schemas.Plugin](s.Plugins, governancePluginName)
	if governancePlugin != nil {
		governanceHandler, err = handlers.NewGovernanceHandler(callbacks, s.Config.ConfigStore)
		if err != nil {
			return fmt.Errorf("failed to initialize governance handler: %v", err)
		}
	}
	var cacheHandler *handlers.CacheHandler
	semanticCachePlugin, _ := FindPluginByName[*semanticcache.Plugin](s.Plugins, semanticcache.PluginName)
	if semanticCachePlugin != nil {
		cacheHandler = handlers.NewCacheHandler(semanticCachePlugin)
	}
	// Websocket handler needs to go below UI handler
	logger.Debug("initializing websocket server")
	if loggerPlugin != nil {
		s.WebSocketHandler = handlers.NewWebSocketHandler(ctx, loggerPlugin.GetPluginLogManager(), s.Config.ClientConfig.AllowedOrigins)
		loggerPlugin.SetLogCallback(func(ctx context.Context, logEntry *logstore.Log) {
			err := s.NewLogEntryAdded(ctx, logEntry)
			if err != nil {
				logger.Error("failed to add log entry: %v", err)
			}
		})
	} else {
		s.WebSocketHandler = handlers.NewWebSocketHandler(ctx, nil, s.Config.ClientConfig.AllowedOrigins)
	}
	// Start WebSocket heartbeat
	s.WebSocketHandler.StartHeartbeat()
	// Adding telemetry middleware
	// Chaining all middlewares
	// lib.ChainMiddlewares chains multiple middlewares together
	healthHandler := handlers.NewHealthHandler(s.Config)
	providerHandler := handlers.NewProviderHandler(callbacks, s.Config.ConfigStore, s.Config, s.Client)
	mcpHandler := handlers.NewMCPHandler(callbacks, s.Client, s.Config)
	mcpServerHandler, err := handlers.NewMCPServerHandler(ctx, s.Config, s)
	if err != nil {
		return fmt.Errorf("failed to initialize mcp server handler: %v", err)
	}
	s.MCPServerHandler = mcpServerHandler
	configHandler := handlers.NewConfigHandler(callbacks, s.Config)
	pluginsHandler := handlers.NewPluginsHandler(callbacks, s.Config.ConfigStore)
	sessionHandler := handlers.NewSessionHandler(s.Config.ConfigStore)
	// Going ahead with API handlers
	healthHandler.RegisterRoutes(s.Router, middlewares...)
	providerHandler.RegisterRoutes(s.Router, middlewares...)
	mcpHandler.RegisterRoutes(s.Router, middlewares...)
	mcpServerHandler.RegisterRoutes(s.Router, middlewares...)
	configHandler.RegisterRoutes(s.Router, middlewares...)
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
	prometheusPlugin, err := FindPluginByName[*telemetry.PrometheusPlugin](s.Plugins, telemetry.PluginName)
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

// PrepareCommonMiddlewares gets the common middlewares for the Bifrost HTTP server
func (s *BifrostHTTPServer) PrepareCommonMiddlewares() []schemas.BifrostHTTPMiddleware {
	commonMiddlewares := []schemas.BifrostHTTPMiddleware{}
	// Preparing middlewares
	// Initializing prometheus plugin
	prometheusPlugin, err := FindPluginByName[*telemetry.PrometheusPlugin](s.Plugins, telemetry.PluginName)
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
	s.pluginStatusMutex = sync.RWMutex{}
	s.PluginsMutex = sync.RWMutex{}
	// Ensure app directory exists
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return fmt.Errorf("failed to create app directory %s: %v", configDir, err)
	}
	// Initialize high-performance configuration store with dedicated database
	s.Config, err = lib.LoadConfig(ctx, configDir)
	if err != nil {
		return fmt.Errorf("failed to load config %v", err)
	}
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
	// Load plugins
	s.pluginStatusMutex.Lock()
	defer s.pluginStatusMutex.Unlock()
	s.Plugins, s.pluginStatus, err = LoadPlugins(ctx, s.Config)
	if err != nil {
		return fmt.Errorf("failed to load plugins %v", err)
	}
	mcpConfig := s.Config.MCPConfig
	if mcpConfig != nil {
		mcpConfig.FetchNewRequestIDFunc = func(ctx *schemas.BifrostContext) string {
			return uuid.New().String()
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
		Plugins:            s.Plugins,
		MCPConfig:          mcpConfig,
		Logger:             logger,
	})
	if err != nil {
		return fmt.Errorf("failed to initialize bifrost: %v", err)
	}
	logger.Info("bifrost client initialized")
	// List all models and add to model catalog
	logger.Info("listing all models and adding to model catalog")
	modelData, listModelsErr := s.Client.ListAllModels(s.Ctx, nil)
	if listModelsErr != nil {
		if listModelsErr.Error != nil {
			logger.Error("failed to list all models: %s", listModelsErr.Error.Message)
		} else {
			logger.Error("failed to list all models: %v", listModelsErr)
		}
	} else if s.Config.PricingManager != nil {
		s.Config.PricingManager.AddModelDataToPool(modelData)
	}
	// Add pricing data to the client
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
	observabilityPlugins := []schemas.ObservabilityPlugin{}
	for _, plugin := range s.Plugins {
		if observabilityPlugin, ok := plugin.(schemas.ObservabilityPlugin); ok {
			observabilityPlugins = append(observabilityPlugins, observabilityPlugin)
		}
	}
	// This enables the central streaming accumulator for both use cases
	// Initializing tracer with embedded streaming accumulator
	traceStore := tracing.NewTraceStore(60*time.Minute, logger)
	tracer := tracing.NewTracer(traceStore, s.Config.PricingManager, logger)
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
	s.pluginStatusMutex.RLock()
	for _, pluginStatus := range s.pluginStatus {
		logger.Info("plugin status: %s - %s", pluginStatus.Name, pluginStatus.Status)
	}
	s.pluginStatusMutex.RUnlock()
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
			if s.Config != nil && s.Config.PricingManager != nil {
				s.Config.PricingManager.Cleanup()
			}
			if s.Config != nil && s.Config.ConfigStore != nil {
				s.Config.ConfigStore.Close(shutdownCtx)
			}
			if s.LogsCleaner != nil {
				logger.Info("stopping log retention cleaner...")
				s.LogsCleaner.StopCleanupRoutine()
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
