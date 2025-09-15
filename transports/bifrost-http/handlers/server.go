// server package wraps http server implementation behind simple interface
package handlers

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/fasthttp/router"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/pricing"
	"github.com/maximhq/bifrost/plugins/governance"
	"github.com/maximhq/bifrost/plugins/logging"
	"github.com/maximhq/bifrost/plugins/maxim"
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
	DefaultAppDir         = "./bifrost-data"
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

	Server *fasthttp.Server
	Router *router.Router
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
	if appDir != "" && appDir != "./bifrost-data" {
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

// CorsMiddleware handles CORS headers for localhost and configured allowed origins
func CorsMiddleware(config *lib.Config, next fasthttp.RequestHandler) fasthttp.RequestHandler {
	return func(ctx *fasthttp.RequestCtx) {
		origin := string(ctx.Request.Header.Peek("Origin"))

		// Check if origin is allowed (localhost always allowed + configured origins)
		if IsOriginAllowed(origin, config.ClientConfig.AllowedOrigins) {
			ctx.Response.Header.Set("Access-Control-Allow-Origin", origin)
		}

		ctx.Response.Header.Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		ctx.Response.Header.Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Requested-With")
		ctx.Response.Header.Set("Access-Control-Allow-Credentials", "true")
		ctx.Response.Header.Set("Access-Control-Max-Age", "86400")

		// Handle preflight OPTIONS requests
		if string(ctx.Method()) == "OPTIONS" {
			ctx.SetStatusCode(fasthttp.StatusOK)
			return
		}

		next(ctx)
	}
}

// LoadPlugins loads the plugins for the server.
func LoadPlugins(ctx context.Context, config *lib.Config) ([]schemas.Plugin, error) {
	plugins := []schemas.Plugin{}
	// Initialize pricing manager
	pricingManager, err := pricing.Init(config.ConfigStore, logger)
	if err != nil {
		logger.Error("failed to initialize pricing manager: %v", err)
	}
	promPlugin := telemetry.Init(pricingManager, logger)

	plugins = append(plugins, promPlugin)

	var loggingPlugin *logging.LoggerPlugin

	if config.ClientConfig.EnableLogging && config.LogsStore != nil {
		// Use dedicated logs database with high-scale optimizations
		loggingPlugin, err = logging.Init(logger, config.LogsStore, pricingManager)
		if err != nil {
			logger.Fatal("failed to initialize logging plugin: %v", err)
		}
		plugins = append(plugins, loggingPlugin)
	}

	var governancePlugin *governance.GovernancePlugin

	if config.ClientConfig.EnableGovernance {
		// Initialize governance plugin
		governancePlugin, err = governance.Init(ctx, &governance.Config{
			IsVkMandatory: &config.ClientConfig.EnforceGovernanceHeader,
		}, logger, config.ConfigStore, config.GovernanceConfig, pricingManager)
		if err != nil {
			logger.Error("failed to initialize governance plugin: %s", err.Error())
		} else {
			plugins = append(plugins, governancePlugin)

		}
	}

	// Currently we support first party plugins only
	// Eventually same flow will be used for third party plugins
	for _, plugin := range config.Plugins {
		if !plugin.Enabled {
			continue
		}
		switch strings.ToLower(plugin.Name) {
		case maxim.PluginName:
			if os.Getenv("MAXIM_LOG_REPO_ID") == "" {
				logger.Warn("maxim log repo id is required to initialize maxim plugin")
				continue
			}
			if os.Getenv("MAXIM_API_KEY") == "" {
				logger.Warn("maxim api key is required in environment variable MAXIM_API_KEY to initialize maxim plugin")
				continue
			}
			maximPlugin, err := maxim.Init(maxim.Config{
				ApiKey:    os.Getenv("MAXIM_API_KEY"),
				LogRepoId: os.Getenv("MAXIM_LOG_REPO_ID"),
			})
			if err != nil {
				logger.Warn("failed to initialize maxim plugin: %v", err)
			} else {
				plugins = append(plugins, maximPlugin)
			}
		case semanticcache.PluginName:
			if !plugin.Enabled {
				logger.Debug("semantic cache plugin is disabled, skipping initialization")
				continue
			}

			if config.VectorStore == nil {
				logger.Error("vector store is required to initialize semantic cache plugin, skipping initialization")
				continue
			}

			// Convert config map to semanticcache.Config struct
			var semCacheConfig semanticcache.Config
			if plugin.Config != nil {
				configBytes, err := json.Marshal(plugin.Config)
				if err != nil {
					logger.Fatal("failed to marshal semantic cache config: %v", err)
				}
				if err := json.Unmarshal(configBytes, &semCacheConfig); err != nil {
					logger.Fatal("failed to unmarshal semantic cache config: %v", err)
				}
			}

			semanticCachePlugin, err := semanticcache.Init(ctx, semCacheConfig, logger, config.VectorStore)
			if err != nil {
				logger.Error("failed to initialize semantic cache: %v", err)
			} else {
				plugins = append(plugins, semanticCachePlugin)
				logger.Info("successfully initialized semantic cache")
			}
		}
	}
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

// RegisterRoutes initializes the routes for the Bifrost HTTP server.
func (s *BifrostHTTPServer) RegisterRoutes(ctx context.Context) error {
	var err error
	// Initialize routes
	s.Router = router.New()
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
	var wsHandler *WebSocketHandler
	if loggerPlugin != nil {
		logger.Debug("initializing websocket server")
		wsHandler = NewWebSocketHandler(ctx, loggerPlugin.GetPluginLogManager(), logger, s.Config.ClientConfig.AllowedOrigins)
		loggerPlugin.SetLogCallback(wsHandler.BroadcastLogUpdate)
		// Start WebSocket heartbeat
		wsHandler.StartHeartbeat()
	}
	// Initialize handlers
	providerHandler := NewProviderHandler(s.Config, s.Client, logger)
	completionHandler := NewCompletionHandler(s.Client, s.Config, logger)
	mcpHandler := NewMCPHandler(s.Client, logger, s.Config)
	integrationHandler := NewIntegrationHandler(s.Client, s.Config)
	configHandler := NewConfigHandler(s.Client, logger, s.Config)
	pluginsHandler := NewPluginsHandler(s.Config.ConfigStore, logger)
	// Register all handler routes
	providerHandler.RegisterRoutes(s.Router)
	completionHandler.RegisterRoutes(s.Router)
	mcpHandler.RegisterRoutes(s.Router)
	integrationHandler.RegisterRoutes(s.Router)
	configHandler.RegisterRoutes(s.Router)
	pluginsHandler.RegisterRoutes(s.Router)
	if cacheHandler != nil {
		cacheHandler.RegisterRoutes(s.Router)
	}
	if governanceHandler != nil {
		governanceHandler.RegisterRoutes(s.Router)
	}
	if loggingHandler != nil {
		loggingHandler.RegisterRoutes(s.Router)
	}
	if wsHandler != nil {
		wsHandler.RegisterRoutes(s.Router)
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
func (s *BifrostHTTPServer) RegisterUIHandler() {
	// Register UI handlers
	// Registering UI handlers
	// WARNING: This UI handler needs to be registered after all the other handlers
	NewUIHandler(s.UIContent).RegisterRoutes(s.Router)
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
		logger.Fatal("failed to create app directory %s: %v", configDir, err)
	}
	// Initialize high-performance configuration store with dedicated database
	s.Config, err = lib.LoadConfig(ctx, configDir)
	if err != nil {
		logger.Fatal("failed to load config %v", err)
	}
	s.InitializeTelemetry()
	logger.Debug("prometheus Go/Process collectors registered.")
	// Load plugins
	s.Plugins, err = LoadPlugins(ctx, s.Config)
	if err != nil {
		logger.Fatal("failed to load plugins %v", err)
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
		logger.Fatal("failed to initialize bifrost: %v", err)
	}
	s.Config.SetBifrostClient(s.Client)
	err = s.RegisterRoutes(s.ctx)
	// Register UI handler
	s.RegisterUIHandler()
	if err != nil {
		return fmt.Errorf("failed to initialize routes: %v", err)
	}
	// Create fasthttp server instance
	s.Server = &fasthttp.Server{
		Handler:            CorsMiddleware(s.Config, s.Router.Handler),
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
	// Initializing server		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
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
		shutdownCtx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
		defer cancel()
		// Perform graceful shutdown
		if err := s.Server.Shutdown(); err != nil {
			logger.Error("error during graceful shutdown: %v", err)
		} else {
			logger.Info("server gracefully shutdown")
		}
		// Cancelling main context
		s.cancel()
		// Wait for shutdown to complete or timeout
		done := make(chan struct{})
		go func() {
			defer close(done)
			s.Client.Shutdown()
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
