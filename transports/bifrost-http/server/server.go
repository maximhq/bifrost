// Package server provides the HTTP server for Bifrost.
package server

import (
	"context"
	"embed"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/bytedance/sonic"
	"github.com/fasthttp/router"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	dynamicPlugins "github.com/maximhq/bifrost/framework/plugins"
	"github.com/maximhq/bifrost/plugins/governance"
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

// BifrostHTTPServer represents a HTTP server instance.
type BifrostHTTPServer struct {
	ctx    context.Context
	cancel context.CancelFunc

	Version   string
	UIContent embed.FS

	Port   string
	Host   string
	AppDir string

	LogLevel       string
	LogOutputStyle string

	Plugins           []schemas.Plugin
	pluginStatusMutex sync.RWMutex
	pluginStatus      []schemas.PluginStatus

	Client *bifrost.Bifrost
	Config *lib.Config

	Server           *fasthttp.Server
	Router           *router.Router
	WebSocketHandler *handlers.WebSocketHandler
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
	config *lib.Config
}

func (s *GovernanceInMemoryStore) GetConfiguredProviders() map[schemas.ModelProvider]configstore.ProviderConfig {
	// Use read lock for thread-safe access - no need to copy on hot path
	s.config.Mu.RLock()
	defer s.config.Mu.RUnlock()
	return s.config.Providers
}

// LoadPlugin loads a plugin by name and returns it as type T.
func LoadPlugin[T schemas.Plugin](ctx context.Context, name string, path *string, pluginConfig any, bifrostConfig *lib.Config) (T, error) {
	var zero T
	if path != nil {
		logger.Info("loading dynamic plugin %s from path %s", name, *path)
		// Load dynamic plugin
		plugins, err := dynamicPlugins.LoadPlugins(&dynamicPlugins.Config{
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
		plugin, err := logging.Init(ctx, logger, bifrostConfig.LogsStore, bifrostConfig.PricingManager)
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
			config: bifrostConfig,
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
		plugin, err := otel.Init(ctx, otelConfig, logger, bifrostConfig.PricingManager)
		if err != nil {
			return zero, err
		}
		if p, ok := any(plugin).(T); ok {
			return p, nil
		}
		return zero, fmt.Errorf("otel plugin type mismatch")
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
		loggingPlugin, err = LoadPlugin[*logging.LoggerPlugin](ctx, logging.PluginName, nil, nil, config)
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
	var governancePlugin *governance.GovernancePlugin
	if config.ClientConfig.EnableGovernance {
		// Initialize governance plugin
		governancePlugin, err = LoadPlugin[*governance.GovernancePlugin](ctx, governance.PluginName, nil, &governance.Config{
			IsVkMandatory: &config.ClientConfig.EnforceGovernanceHeader,
		}, config)
		if err != nil {
			logger.Error("failed to initialize governance plugin: %s", err.Error())
			pluginStatus = append(pluginStatus, schemas.PluginStatus{
				Name:   governance.PluginName,
				Status: schemas.PluginStatusError,
				Logs:   []string{fmt.Sprintf("error initializing governance plugin %v", err)},
			})
		} else {
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

// ReloadClientConfigFromConfigStore reloads the client config from config store
func (s *BifrostHTTPServer) ReloadClientConfigFromConfigStore() error {
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

// UpdateAuthConfig updates auth config
func (s *BifrostHTTPServer) UpdateAuthConfig(ctx context.Context, authConfig *configstore.AuthConfig) error {
	if authConfig == nil {
		return fmt.Errorf("config store not found")
	}
	if s.Config == nil || s.Config.ConfigStore == nil {
		return fmt.Errorf("config store not found")
	}
	if authConfig.AdminUserName == "" || authConfig.AdminPassword == "" {
		return fmt.Errorf("username and password are required")
	}
	return s.Config.ConfigStore.UpdateAuthConfig(ctx, authConfig)
}

// UpdateDropExcessRequests updates excess requests config
func (s *BifrostHTTPServer) UpdateDropExcessRequests(value bool) {
	if s.Config == nil {
		return
	}
	s.Client.UpdateDropExcessRequests(value)
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

// GetPluginStatus returns the status of all plugins
func (s *BifrostHTTPServer) GetPluginStatus() []schemas.PluginStatus {
	s.pluginStatusMutex.RLock()
	defer s.pluginStatusMutex.RUnlock()
	result := make([]schemas.PluginStatus, len(s.pluginStatus))
	copy(result, s.pluginStatus)
	return result
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
	if err := s.Client.ReloadPlugin(newPlugin); err != nil {
		s.UpdatePluginStatus(name, schemas.PluginStatusError, []string{fmt.Sprintf("error reloading plugin %s: %v", name, err)})
		return err
	}
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
			if existing.GetName() == name {
				newPlugins[i] = newPlugin
				found = true
				break
			}
		}
		if !found {
			newPlugins = append(newPlugins, newPlugin)
		}

		// Atomic compare-and-swap
		if s.Config.Plugins.CompareAndSwap(oldPlugins, &newPlugins) {
			s.Plugins = newPlugins // Keep BifrostHTTPServer.Plugins in sync
			return nil
		}
		// Retry on contention (extremely rare for plugin updates)
	}
}

// ReloadPricingManager reloads the pricing manager
func (s *BifrostHTTPServer) ReloadPricingManager() error {
	if s.Config == nil || s.Config.PricingManager == nil {
		return fmt.Errorf("pricing manager not found")
	}
	if s.Config.FrameworkConfig == nil || s.Config.FrameworkConfig.Pricing == nil {
		return fmt.Errorf("framework config not found")
	}
	return s.Config.PricingManager.ReloadPricing(context.Background(), s.Config.FrameworkConfig.Pricing)
}

// RefetchModelsForProvider deletes existing models for a provider and refetches them from the provider
func (s *BifrostHTTPServer) RefetchModelsForProvider(ctx context.Context, provider schemas.ModelProvider) error {
	if s.Config == nil || s.Config.PricingManager == nil {
		return fmt.Errorf("pricing manager not found")
	}
	if s.Client == nil {
		return fmt.Errorf("bifrost client not found")
	}

	allModels, err := s.Client.ListModelsRequest(ctx, &schemas.BifrostListModelsRequest{
		Provider: provider,
	})
	if err != nil {
		return fmt.Errorf("failed to update provider model catalog: failed to list all models: %s", bifrost.GetErrorMessage(err))
	}

	s.Config.PricingManager.DeleteModelDataForProvider(provider)

	s.Config.PricingManager.AddModelDataToPool(allModels)

	return nil
}

// DeleteModelsForProvider deletes all models for a specific provider from the model catalog
func (s *BifrostHTTPServer) DeleteModelsForProvider(provider schemas.ModelProvider) error {
	if s.Config == nil || s.Config.PricingManager == nil {
		return fmt.Errorf("pricing manager not found")
	}

	s.Config.PricingManager.DeleteModelDataForProvider(provider)

	return nil
}

// RemovePlugin removes a plugin from the server.
// Uses atomic CompareAndSwap with retry loop to handle concurrent updates safely.
func (s *BifrostHTTPServer) RemovePlugin(ctx context.Context, name string) error {
	if err := s.Client.RemovePlugin(name); err != nil {
		return err
	}
	isDisabled := ctx.Value("isDisabled")
	if isDisabled != nil && isDisabled.(bool) {
		s.UpdatePluginStatus(name, schemas.PluginStatusDisabled, []string{fmt.Sprintf("plugin %s is disabled", name)})
	} else {
		// Removing plugin from plugin status
		s.UpdatePluginStatus(name, schemas.PluginStatusDisabled, []string{fmt.Sprintf("plugin %s is removed", name)})
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
			s.Plugins = newPlugins // Keep BifrostHTTPServer.Plugins in sync
			return nil
		}
		// Retry on contention (extremely rare for plugin updates)
	}
}

// RegisterRoutes initializes the routes for the Bifrost HTTP server.
func (s *BifrostHTTPServer) RegisterRoutes(ctx context.Context, middlewares ...lib.BifrostHTTPMiddleware) error {
	var err error
	// Initializing plugin specific handlers
	var loggingHandler *handlers.LoggingHandler
	loggerPlugin, _ := FindPluginByName[*logging.LoggerPlugin](s.Plugins, logging.PluginName)
	if loggerPlugin != nil {
		loggingHandler = handlers.NewLoggingHandler(loggerPlugin.GetPluginLogManager())
	}
	var governanceHandler *handlers.GovernanceHandler
	governancePlugin, _ := FindPluginByName[*governance.GovernancePlugin](s.Plugins, governance.PluginName)
	if governancePlugin != nil {
		governanceHandler, err = handlers.NewGovernanceHandler(governancePlugin, s.Config.ConfigStore)
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
		loggerPlugin.SetLogCallback(s.WebSocketHandler.BroadcastLogUpdate)
	} else {
		s.WebSocketHandler = handlers.NewWebSocketHandler(ctx, nil, s.Config.ClientConfig.AllowedOrigins)
	}
	// Start WebSocket heartbeat
	s.WebSocketHandler.StartHeartbeat()
	// Adding telemetry middleware
	middlewaresWithTelemetry := middlewares
	prometheusPlugin, err := FindPluginByName[*telemetry.PrometheusPlugin](s.Plugins, telemetry.PluginName)
	if err == nil {
		middlewaresWithTelemetry = append(middlewaresWithTelemetry, prometheusPlugin.HTTPMiddleware)
	} else {
		logger.Warn("prometheus plugin not found, skipping telemetry middleware")
		middlewaresWithTelemetry = middlewares
	}

	// Chaining all middlewares
	// lib.ChainMiddlewares chains multiple middlewares together
	// Initialize
	healthHandler := handlers.NewHealthHandler(s.Config)
	providerHandler := handlers.NewProviderHandler(s, s.Config, s.Client)
	inferenceHandler := handlers.NewInferenceHandler(s.Client, s.Config)
	mcpHandler := handlers.NewMCPHandler(s.Client, s.Config)
	integrationHandler := handlers.NewIntegrationHandler(s.Client, s.Config)
	configHandler := handlers.NewConfigHandler(s, s.Config)
	pluginsHandler := handlers.NewPluginsHandler(s, s.Config.ConfigStore)
	sessionHandler := handlers.NewSessionHandler(s.Config.ConfigStore)
	// Register all handler routes
	healthHandler.RegisterRoutes(s.Router, middlewares...)
	providerHandler.RegisterRoutes(s.Router, middlewares...)
	inferenceHandler.RegisterRoutes(s.Router, middlewaresWithTelemetry...)
	mcpHandler.RegisterRoutes(s.Router, middlewares...)
	integrationHandler.RegisterRoutes(s.Router, middlewaresWithTelemetry...)
	configHandler.RegisterRoutes(s.Router, middlewares...)
	pluginsHandler.RegisterRoutes(s.Router, middlewares...)
	sessionHandler.RegisterRoutes(s.Router, middlewares...)
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

	// Add Prometheus /metrics endpoint
	prometheusPlugin, err = FindPluginByName[*telemetry.PrometheusPlugin](s.Plugins, telemetry.PluginName)
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

// RegisterUIHandler registers the UI handler with the specified router
func (s *BifrostHTTPServer) RegisterUIHandler(middlewares ...lib.BifrostHTTPMiddleware) {
	// Register UI handlers
	// Registering UI handlers
	// WARNING: This UI handler needs to be registered after all the other handlers
	handlers.NewUIHandler(s.UIContent).RegisterRoutes(s.Router, middlewares...)
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
	s.ctx, s.cancel = context.WithCancel(ctx)
	handlers.SetVersion(s.Version)
	configDir := GetDefaultConfigDir(s.AppDir)
	s.pluginStatusMutex = sync.RWMutex{}
	// Ensure app directory exists
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return fmt.Errorf("failed to create app directory %s: %v", configDir, err)
	}
	// Initialize high-performance configuration store with dedicated database
	s.Config, err = lib.LoadConfig(ctx, configDir)
	if err != nil {
		return fmt.Errorf("failed to load config %v", err)
	}
	// Load plugins
	s.pluginStatusMutex.Lock()
	defer s.pluginStatusMutex.Unlock()
	s.Plugins, s.pluginStatus, err = LoadPlugins(ctx, s.Config)
	if err != nil {
		return fmt.Errorf("failed to load plugins %v", err)
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
		MCPConfig:          s.Config.MCPConfig,
		Logger:             logger,
	})
	if err != nil {
		return fmt.Errorf("failed to initialize bifrost: %v", err)
	}
	logger.Info("bifrost client initialized")
	// List all models and add to model catalog
	logger.Info("listing all models and adding to model catalog")
	modelData, listModelsErr := s.Client.ListAllModels(ctx, nil)
	if listModelsErr != nil {
		if listModelsErr.Error != nil {
			logger.Error("failed to list all models: %s", listModelsErr.Error.Message)
		} else {
			logger.Error("failed to list all models: %v", listModelsErr)
		}
	} else {
		s.Config.PricingManager.AddModelDataToPool(modelData)
	}
	logger.Info("models added to catalog")
	s.Config.SetBifrostClient(s.Client)
	// Initializing middlewares
	// Checking if auth config is set; if yes we will add middleware
	var middlewares []lib.BifrostHTTPMiddleware
	if ctx.Value("isEnterprise") == nil {
		middlewares = append(middlewares, handlers.AuthMiddleware(s.Config.ConfigStore))
	}
	// Initialize routes
	s.Router = router.New()
	// Register routes
	err = s.RegisterRoutes(s.ctx, middlewares...)
	// Register UI handler
	s.RegisterUIHandler()
	if err != nil {
		return fmt.Errorf("failed to initialize routes: %v", err)
	}
	// Create fasthttp server instance
	s.Server = &fasthttp.Server{
		Handler:            handlers.CorsMiddleware(s.Config)(handlers.TransportInterceptorMiddleware(s.Config)(s.Router.Handler)),
		MaxRequestBodySize: s.Config.ClientConfig.MaxRequestBodySizeMB * 1024 * 1024,
	}
	return nil
}

// Start starts the HTTP server at the specified host and port
// Also watches signals and errors
func (s *BifrostHTTPServer) Start() error {
	// Create channels for signal and error handling
	sigChan := make(chan os.Signal, 1)
	errChan := make(chan error, 1)
	// Watching for signals
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	// Start server in a goroutine
	serverAddr := net.JoinHostPort(s.Host, s.Port)
	go func() {
		logger.Info("successfully started bifrost, serving UI on http://%s:%s", s.Host, s.Port)
		if err := s.Server.ListenAndServe(serverAddr); err != nil {
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
