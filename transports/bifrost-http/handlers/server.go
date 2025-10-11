package handlers

import (
	"context"
	"embed"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/bytedance/sonic"
	"github.com/fasthttp/router"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/plugins/governance"
	"github.com/maximhq/bifrost/plugins/logging"
	"github.com/maximhq/bifrost/plugins/maxim"
	"github.com/maximhq/bifrost/plugins/otel"
	"github.com/maximhq/bifrost/plugins/semanticcache"
	"github.com/maximhq/bifrost/plugins/telemetry"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
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

	Plugins []schemas.Plugin
	Client  *bifrost.Bifrost
	Config  *lib.Config

	Server           *fasthttp.Server
	Router           *router.Router
	WebSocketHandler *WebSocketHandler
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

// RegisterCollectorSafely attempts to register a Prometheus collector,
// handling the case where it may already be registered.
// It logs any errors that occur during registration, except for AlreadyRegisteredError.
func RegisterCollectorSafely(collector prometheus.Collector) {
	if err := prometheus.Register(collector); err != nil {
		if _, ok := err.(prometheus.AlreadyRegisteredError); !ok {
			logger.Error("failed to register prometheus collector: %v", err)
		}
	}
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
func LoadPlugin[T schemas.Plugin](ctx context.Context, name string, pluginConfig any, bifrostConfig *lib.Config) (T, error) {
	var zero T
	switch name {
	case telemetry.PluginName:
		plugin, err := telemetry.Init(bifrostConfig.PricingManager, logger)
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
		plugin, err := maxim.Init(maximConfig)
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
func LoadPlugins(ctx context.Context, config *lib.Config) ([]schemas.Plugin, error) {
	var err error
	plugins := []schemas.Plugin{}

	// Initialize telemetry plugin
	promPlugin, err := LoadPlugin[*telemetry.PrometheusPlugin](ctx, telemetry.PluginName, nil, config)
	if err != nil {
		logger.Error("failed to initialize telemetry plugin: %v", err)
	} else {
		plugins = append(plugins, promPlugin)
	}
	// Initializing logger plugin
	var loggingPlugin *logging.LoggerPlugin
	if config.ClientConfig.EnableLogging && config.LogsStore != nil {
		// Use dedicated logs database with high-scale optimizations
		loggingPlugin, err = LoadPlugin[*logging.LoggerPlugin](ctx, logging.PluginName, nil, config)
		if err != nil {
			logger.Error("failed to initialize logging plugin: %v", err)
		} else {
			plugins = append(plugins, loggingPlugin)
		}
	}
	// Initializing governance plugin
	var governancePlugin *governance.GovernancePlugin
	if config.ClientConfig.EnableGovernance {
		// Initialize governance plugin
		governancePlugin, err = LoadPlugin[*governance.GovernancePlugin](ctx, governance.PluginName, &governance.Config{
			IsVkMandatory: &config.ClientConfig.EnforceGovernanceHeader,
		}, config)
		if err != nil {
			logger.Error("failed to initialize governance plugin: %s", err.Error())
		} else {
			plugins = append(plugins, governancePlugin)
		}
	}
	// Currently we support first party plugins only
	// Eventually same flow will be used for third party plugins
	for _, plugin := range config.PluginConfigs {
		if !plugin.Enabled {
			continue
		}
		pluginInstance, err := LoadPlugin[schemas.Plugin](ctx, plugin.Name, plugin.Config, config)
		if err != nil {
			logger.Error("failed to load plugin %s: %v", plugin.Name, err)
		} else {
			plugins = append(plugins, pluginInstance)
		}
	}

	// Atomically publish the plugin state
	config.Plugins.Store(&plugins)

	return plugins, nil
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

// ReloadPlugin reloads a plugin with new instance and updates Bifrost core.
// Uses atomic CompareAndSwap with retry loop to handle concurrent updates safely.
func (s *BifrostHTTPServer) ReloadPlugin(ctx context.Context, name string, pluginConfig any) error {
	logger.Debug("reloading plugin %s", name)
	newPlugin, err := LoadPlugin[schemas.Plugin](ctx, name, pluginConfig, s.Config)
	if err != nil {
		return err
	}
	if err := s.Client.ReloadPlugin(newPlugin); err != nil {
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

// RemovePlugin removes a plugin from the server.
// Uses atomic CompareAndSwap with retry loop to handle concurrent updates safely.
func (s *BifrostHTTPServer) RemovePlugin(ctx context.Context, name string) error {
	if err := s.Client.RemovePlugin(name); err != nil {
		return err
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
	var loggingHandler *LoggingHandler
	loggerPlugin, _ := FindPluginByName[*logging.LoggerPlugin](s.Plugins, logging.PluginName)
	if loggerPlugin != nil {
		loggingHandler = NewLoggingHandler(loggerPlugin.GetPluginLogManager(), logger)
	}
	var governanceHandler *GovernanceHandler
	governancePlugin, _ := FindPluginByName[*governance.GovernancePlugin](s.Plugins, governance.PluginName)
	if governancePlugin != nil {
		governanceHandler, err = NewGovernanceHandler(governancePlugin, s.Config.ConfigStore, logger)
		if err != nil {
			return fmt.Errorf("failed to initialize governance handler: %v", err)
		}
	}
	var cacheHandler *CacheHandler
	semanticCachePlugin, _ := FindPluginByName[*semanticcache.Plugin](s.Plugins, semanticcache.PluginName)
	if semanticCachePlugin != nil {
		cacheHandler = NewCacheHandler(semanticCachePlugin, logger)
	}
	// Websocket handler needs to go below UI handler
	logger.Debug("initializing websocket server")
	if loggerPlugin != nil {
		s.WebSocketHandler = NewWebSocketHandler(ctx, loggerPlugin.GetPluginLogManager(), logger, s.Config.ClientConfig.AllowedOrigins)
		loggerPlugin.SetLogCallback(s.WebSocketHandler.BroadcastLogUpdate)
	} else {
		s.WebSocketHandler = NewWebSocketHandler(ctx, nil, logger, s.Config.ClientConfig.AllowedOrigins)
	}
	// Start WebSocket heartbeat
	s.WebSocketHandler.StartHeartbeat()
	middlewaresWithTelemetry := append(middlewares, telemetry.PrometheusMiddleware)
	// Chaining all middlewares
	// lib.ChainMiddlewares chains multiple middlewares together
	// Initialize handlers
	providerHandler := NewProviderHandler(s.Config, s.Client, logger)
	inferenceHandler := NewInferenceHandler(s.Client, s.Config, logger)
	mcpHandler := NewMCPHandler(s.Client, logger, s.Config)
	integrationHandler := NewIntegrationHandler(s.Client, s.Config)
	configHandler := NewConfigHandler(s.Client, logger, s.Config, s)
	pluginsHandler := NewPluginsHandler(s, s.Config.ConfigStore, logger)
	// Register all handler routes
	providerHandler.RegisterRoutes(s.Router, middlewares...)
	inferenceHandler.RegisterRoutes(s.Router, middlewaresWithTelemetry...)
	mcpHandler.RegisterRoutes(s.Router, middlewares...)
	integrationHandler.RegisterRoutes(s.Router, middlewaresWithTelemetry...)
	configHandler.RegisterRoutes(s.Router, middlewares...)
	pluginsHandler.RegisterRoutes(s.Router, middlewares...)
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
	//
	// Add Prometheus /metrics endpoint
	s.Router.GET("/metrics", fasthttpadaptor.NewFastHTTPHandler(promhttp.Handler()))
	// 404 handler
	s.Router.NotFound = func(ctx *fasthttp.RequestCtx) {
		SendError(ctx, fasthttp.StatusNotFound, "Route not found: "+string(ctx.Path()), logger)
	}
	return nil
}

// InitializeTelemetry initializes Prometheus collectors for monitoring
func (s *BifrostHTTPServer) InitializeTelemetry() {
	RegisterCollectorSafely(collectors.NewGoCollector())
	RegisterCollectorSafely(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	// Initialize prometheus telemetry
	telemetry.InitPrometheusMetrics(s.Config.ClientConfig.PrometheusLabels)
}

// RegisterUIHandler registers the UI handler with the specified router
func (s *BifrostHTTPServer) RegisterUIHandler(middlewares ...lib.BifrostHTTPMiddleware) {
	// Register UI handlers
	// Registering UI handlers
	// WARNING: This UI handler needs to be registered after all the other handlers
	NewUIHandler(s.UIContent).RegisterRoutes(s.Router, middlewares...)
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
	SetVersion(s.Version)
	configDir := GetDefaultConfigDir(s.AppDir)

	// Ensure app directory exists
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return fmt.Errorf("failed to create app directory %s: %v", configDir, err)
	}
	// Initialize high-performance configuration store with dedicated database
	s.Config, err = lib.LoadConfig(ctx, configDir)
	if err != nil {
		return fmt.Errorf("failed to load config %v", err)
	}
	s.InitializeTelemetry()
	logger.Debug("prometheus Go/Process collectors registered.")
	// Load plugins
	s.Plugins, err = LoadPlugins(ctx, s.Config)
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
	s.Config.SetBifrostClient(s.Client)
	// Initialize routes
	s.Router = router.New()
	// Register routes
	err = s.RegisterRoutes(s.ctx)
	// Register UI handler
	s.RegisterUIHandler()
	if err != nil {
		return fmt.Errorf("failed to initialize routes: %v", err)
	}
	// Create fasthttp server instance
	s.Server = &fasthttp.Server{
		Handler:            CorsMiddleware(s.Config)(TransportInterceptorMiddleware(s.Config)(s.Router.Handler)),
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
