// Package http provides an HTTP service using FastHTTP that exposes endpoints
// for text and chat completions using various AI model providers (OpenAI, Anthropic, Bedrock, Mistral, Ollama, etc.).
//
// The HTTP service provides the following main endpoints:
//   - /v1/text/completions: For text completion requests
//   - /v1/chat/completions: For chat completion requests
//   - /v1/mcp/tool/execute: For MCP tool execution requests
//   - /providers/*: For provider configuration management
//
// Configuration is handled through a JSON config file, high-performance ConfigStore, and environment variables:
//   - Use -app-dir flag to specify the application data directory (contains config.json and logs)
//   - Use -port flag to specify the server port (default: 8080)
//   - When no config file exists, common environment variables are auto-detected (OPENAI_API_KEY, ANTHROPIC_API_KEY, MISTRAL_API_KEY)
//
// ConfigStore Features:
//   - Pure in-memory storage for ultra-fast config access
//   - Environment variable processing for secure configuration management
//   - Real-time configuration updates via HTTP API
//   - Explicit persistence control via POST /config/save endpoint
//   - Provider-specific key config support (Azure, Bedrock, Vertex)
//   - Thread-safe operations with concurrent request handling
//   - Statistics and monitoring endpoints for operational insights
//
// Performance Optimizations:
//   - Configuration data is processed once during startup and stored in memory
//   - Ultra-fast memory access eliminates I/O overhead on every request
//   - All environment variable processing done upfront during configuration loading
//   - Thread-safe concurrent access with read-write mutex protection
//
// Example usage:
//
//	go run main.go -app-dir ./data -port 8080 -host 0.0.0.0
//	after setting provider API keys like OPENAI_API_KEY in the environment.
//
//	To bind to all interfaces for container usage, set BIFROST_HOST=0.0.0.0 or use -host 0.0.0.0
//
// Integration Support:
// Bifrost supports multiple AI provider integrations through dedicated HTTP endpoints.
// Each integration exposes API-compatible endpoints that accept the provider's native request format,
// automatically convert it to Bifrost's unified format, process it, and return the expected response format.
//
// Integration endpoints follow the pattern: /{provider}/{provider_api_path}
// Examples:
//   - OpenAI: POST /openai/v1/chat/completions (accepts OpenAI ChatCompletion requests)
//   - GenAI:  POST /genai/v1beta/models/{model} (accepts Google GenAI requests)
//   - Anthropic: POST /anthropic/v1/messages (accepts Anthropic Messages requests)
//
// This allows clients to use their existing integration code without modification while benefiting
// from Bifrost's unified model routing, fallbacks, monitoring capabilities, and high-performance configuration management.
//
// NOTE: Streaming is supported for chat completions via Server-Sent Events (SSE)
package main

import (
	"context"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"mime"
	"net"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/fasthttp/router"
	bifrost "github.com/maximhq/bifrost/core"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/plugins/governance"
	"github.com/maximhq/bifrost/plugins/logging"
	"github.com/maximhq/bifrost/plugins/maxim"
	"github.com/maximhq/bifrost/plugins/semanticcache"
	"github.com/maximhq/bifrost/plugins/telemetry"
	"github.com/maximhq/bifrost/transports/bifrost-http/handlers"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/fasthttpadaptor"
)

//go:embed all:ui
var uiContent embed.FS
var logger = bifrost.NewDefaultLogger(schemas.LogLevelInfo)

// Command line flags
var (
	port          string   // Port to run the server on
	host          string   // Host to bind the server to
	appDir        string   // Application data directory
	pluginsToLoad []string // Plugins to load

	logLevel       string // Logger level: debug, info, warn, error
	logOutputStyle string // Logger output style: json, pretty
)

// init initializes command line flags and validates required configuration.
// It sets up the following flags:
//   - host: Host to bind the server to (default: localhost, can be overridden with BIFROST_HOST env var)
//   - port: Server port (default: 8080)
//   - app-dir: Application data directory (default: current directory)
//   - plugins: Comma-separated list of plugins to load
//   - log-level: Logger level (debug, info, warn, error). Default is info.
//   - log-style: Logger output type (json or pretty). Default is JSON.

func init() {
	// Welcome to bifrost!
	fmt.Println(`
╔═══════════════════════════════════════════════════════════╗
║                                                           ║
║   ██████╗ ██╗███████╗██████╗  ██████╗ ███████╗████████╗   ║
║   ██╔══██╗██║██╔════╝██╔══██╗██╔═══██╗██╔════╝╚══██╔══╝   ║
║   ██████╔╝██║█████╗  ██████╔╝██║   ██║███████╗   ██║      ║
║   ██╔══██╗██║██╔══╝  ██╔══██╗██║   ██║╚════██║   ██║      ║
║   ██████╔╝██║██║     ██║  ██║╚██████╔╝███████║   ██║      ║
║   ╚═════╝ ╚═╝╚═╝     ╚═╝  ╚═╝ ╚═════╝ ╚══════╝   ╚═╝      ║
║                                                           ║
║═══════════════════════════════════════════════════════════║
║                The Fastest LLM Gateway                    ║
║═══════════════════════════════════════════════════════════║
║            https://github.com/maximhq/bifrost             ║
╚═══════════════════════════════════════════════════════════╝`)

	// Set default host from environment variable or use localhost
	defaultHost := os.Getenv("BIFROST_HOST")
	if defaultHost == "" {
		defaultHost = "localhost"
	}

	flag.StringVar(&port, "port", "8080", "Port to run the server on")
	flag.StringVar(&host, "host", defaultHost, "Host to bind the server to (default: localhost, override with BIFROST_HOST env var)")
	flag.StringVar(&appDir, "app-dir", "./bifrost-data", "Application data directory (contains config.json and logs)")
	flag.StringVar(&logLevel, "log-level", string(schemas.LogLevelInfo), "Logger level (debug, info, warn, error). Default is info.")
	flag.StringVar(&logOutputStyle, "log-style", string(schemas.LoggerOutputTypeJSON), "Logger output type (json or pretty). Default is JSON.")
	flag.Parse()

	// Configure logger from flags
	logger.SetOutputType(schemas.LoggerOutputType(logOutputStyle))
	logger.SetLevel(schemas.LogLevel(logLevel))
}

// registerCollectorSafely attempts to register a Prometheus collector,
// handling the case where it may already be registered.
// It logs any errors that occur during registration, except for AlreadyRegisteredError.
func registerCollectorSafely(collector prometheus.Collector) {
	if err := prometheus.Register(collector); err != nil {
		if _, ok := err.(prometheus.AlreadyRegisteredError); !ok {
			logger.Error("failed to register prometheus collector: %v", err)
		}
	}
}

// corsMiddleware handles CORS headers for localhost and configured allowed origins
func corsMiddleware(config *lib.Config, next fasthttp.RequestHandler) fasthttp.RequestHandler {
	return func(ctx *fasthttp.RequestCtx) {
		origin := string(ctx.Request.Header.Peek("Origin"))

		// Check if origin is allowed (localhost always allowed + configured origins)
		if handlers.IsOriginAllowed(origin, config.ClientConfig.AllowedOrigins) {
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

// uiHandler serves the embedded Next.js UI files
func uiHandler(ctx *fasthttp.RequestCtx) {
	// Get the request path
	requestPath := string(ctx.Path())

	// Clean the path to prevent directory traversal
	cleanPath := path.Clean(requestPath)

	// Handle .txt files (Next.js RSC payload files) - map from /{page}.txt to /{page}/index.txt
	if strings.HasSuffix(cleanPath, ".txt") {
		// Remove .txt extension and add /index.txt
		basePath := strings.TrimSuffix(cleanPath, ".txt")
		if basePath == "/" || basePath == "" {
			basePath = "/index"
		}
		cleanPath = basePath + "/index.txt"
	}

	// Remove leading slash and add ui prefix
	if cleanPath == "/" {
		cleanPath = "ui/index.html"
	} else {
		cleanPath = "ui" + cleanPath
	}

	// Check if this is a static asset request (has file extension)
	hasExtension := strings.Contains(filepath.Base(cleanPath), ".")

	// Try to read the file from embedded filesystem
	data, err := uiContent.ReadFile(cleanPath)
	if err != nil {

		// If it's a static asset (has extension) and not found, return 404
		if hasExtension {
			ctx.SetStatusCode(fasthttp.StatusNotFound)
			ctx.SetBodyString("404 - Static asset not found: " + requestPath)
			return
		}

		// For routes without extensions (SPA routing), try {path}/index.html first
		if !hasExtension {
			indexPath := cleanPath + "/index.html"
			data, err = uiContent.ReadFile(indexPath)
			if err == nil {
				cleanPath = indexPath
			} else {
				// If that fails, serve root index.html as fallback
				data, err = uiContent.ReadFile("ui/index.html")
				if err != nil {
					ctx.SetStatusCode(fasthttp.StatusNotFound)
					ctx.SetBodyString("404 - File not found")
					return
				}
				cleanPath = "ui/index.html"
			}
		} else {
			ctx.SetStatusCode(fasthttp.StatusNotFound)
			ctx.SetBodyString("404 - File not found")
			return
		}
	}

	// Set content type based on file extension
	ext := filepath.Ext(cleanPath)
	contentType := mime.TypeByExtension(ext)
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	ctx.SetContentType(contentType)

	// Set cache headers for static assets
	if strings.HasPrefix(cleanPath, "ui/_next/static/") {
		ctx.Response.Header.Set("Cache-Control", "public, max-age=31536000, immutable")
	} else if ext == ".html" {
		ctx.Response.Header.Set("Cache-Control", "no-cache")
	} else {
		ctx.Response.Header.Set("Cache-Control", "public, max-age=3600")
	}

	// Send the file content
	ctx.SetBody(data)
}

// GetDefaultConfigDir returns the OS-specific default configuration directory for Bifrost.
// This follows standard conventions:
// - Linux/macOS: ~/.config/bifrost
// - Windows: %APPDATA%\bifrost
// - If appDir is provided (non-empty), it returns that instead
func getDefaultConfigDir(appDir string) string {
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

// main is the entry point of the application.
// It:
// 1. Initializes Prometheus collectors for monitoring
// 2. Reads and parses configuration from the specified config file
// 3. Initializes the Bifrost client with the configuration
// 4. Sets up HTTP routes for text and chat completions
// 5. Starts the HTTP server on the specified host and port
//
// The server exposes the following endpoints:
//   - POST /v1/text/completions: For text completion requests
//   - POST /v1/chat/completions: For chat completion requests
//   - GET /metrics: For Prometheus metrics
func main() {
	ctx := context.Background()
	configDir := getDefaultConfigDir(appDir)
	// Ensure app directory exists
	if err := os.MkdirAll(configDir, 0755); err != nil {
		logger.Fatal("failed to create app directory %s: %v", configDir, err)
	}

	// Register Prometheus collectors
	registerCollectorSafely(collectors.NewGoCollector())
	registerCollectorSafely(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	// Initialize high-performance configuration store with dedicated database
	config, err := lib.LoadConfig(ctx, configDir)
	if err != nil {
		logger.Fatal("failed to load config %v", err)
	}

	// Create account backed by the high-performance store (all processing is done in LoadFromDatabase)
	// The account interface now benefits from ultra-fast config access times via in-memory storage
	account := lib.NewBaseAccount(config)

	// Initialize plugins
	loadedPlugins := []schemas.Plugin{}

	telemetry.InitPrometheusMetrics(config.ClientConfig.PrometheusLabels)
	logger.Debug("prometheus Go/Process collectors registered.")

	promPlugin := telemetry.NewPrometheusPlugin()

	var loggingPlugin *logging.LoggerPlugin
	var loggingHandler *handlers.LoggingHandler
	var wsHandler *handlers.WebSocketHandler

	if config.ClientConfig.EnableLogging && config.LogsStore != nil {
		// Use dedicated logs database with high-scale optimizations
		loggingPlugin, err = logging.Init(logger, config.LogsStore)
		if err != nil {
			logger.Fatal("failed to initialize logging plugin: %v", err)
		}

		loadedPlugins = append(loadedPlugins, loggingPlugin)
		loggingHandler = handlers.NewLoggingHandler(loggingPlugin.GetPluginLogManager(), logger)
		wsHandler = handlers.NewWebSocketHandler(loggingPlugin.GetPluginLogManager(), logger, config.ClientConfig.AllowedOrigins)
	}

	var governancePlugin *governance.GovernancePlugin
	var governanceHandler *handlers.GovernanceHandler

	if config.ClientConfig.EnableGovernance {
		// Initialize governance plugin
		governancePlugin, err = governance.Init(ctx, &governance.Config{
			IsVkMandatory: &config.ClientConfig.EnforceGovernanceHeader,
		}, logger, config.ConfigStore, config.GovernanceConfig)
		if err != nil {
			logger.Error("failed to initialize governance plugin: %s", err.Error())
		} else {
			loadedPlugins = append(loadedPlugins, governancePlugin)

			governanceHandler, err = handlers.NewGovernanceHandler(governancePlugin, config.ConfigStore, logger)
			if err != nil {
				logger.Error("failed to initialize governance handler: %s", err.Error())
			}
		}
	}

	// Currently we support first party plugins only
	// Eventually same flow will be used for third party plugins
	for _, plugin := range config.Plugins {
		if !plugin.Enabled {
			continue
		}
		switch strings.ToLower(plugin.Name) {
		case "maxim":
			if os.Getenv("MAXIM_LOG_REPO_ID") == "" {
				logger.Warn("maxim log repo id is required to initialize maxim plugin")
				continue
			}
			if os.Getenv("MAXIM_API_KEY") == "" {
				logger.Warn("maxim api key is required in environment variable MAXIM_API_KEY to initialize maxim plugin")
				continue
			}

			maximPlugin, err := maxim.NewMaximLoggerPlugin(os.Getenv("MAXIM_API_KEY"), os.Getenv("MAXIM_LOG_REPO_ID"))
			if err != nil {
				logger.Warn("failed to initialize maxim plugin: %v", err)
			} else {
				loadedPlugins = append(loadedPlugins, maximPlugin)
			}
		case "semantic_cache":
			if !plugin.Enabled {
				logger.Debug("semantic cache plugin is disabled, skipping initialization")
				continue
			}

			if config.VectorStore == nil {
				logger.Fatal("vector store is required to initialize semantic cache plugin")
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

			// Set hardcoded values
			semCacheConfig.CacheKey = "request-cache-key"
			semCacheConfig.CacheTTLKey = "request-cache-ttl"

			semanticCachePlugin, err := semanticcache.Init(ctx, semCacheConfig, logger, config.VectorStore)
			if err != nil {
				logger.Fatal("failed to initialize semantic cache plugin: %v", err)
			} else {
				loadedPlugins = append(loadedPlugins, semanticCachePlugin)
			}
		}
	}

	loadedPlugins = append(loadedPlugins, promPlugin)

	client, err := bifrost.Init(schemas.BifrostConfig{
		Account:            account,
		InitialPoolSize:    config.ClientConfig.InitialPoolSize,
		DropExcessRequests: config.ClientConfig.DropExcessRequests,
		Plugins:            loadedPlugins,
		MCPConfig:          config.MCPConfig,
		Logger:             logger,
	})
	if err != nil {
		logger.Fatal("failed to initialize bifrost: %v", err)
	}

	config.SetBifrostClient(client)

	// Initialize handlers
	providerHandler := handlers.NewProviderHandler(config, client, logger)
	completionHandler := handlers.NewCompletionHandler(client, config, logger)
	mcpHandler := handlers.NewMCPHandler(client, logger, config)
	integrationHandler := handlers.NewIntegrationHandler(client, config)
	configHandler := handlers.NewConfigHandler(client, logger, config)

	// Set up WebSocket callback for real-time log updates
	if wsHandler != nil && loggingPlugin != nil {
		loggingPlugin.SetLogCallback(wsHandler.BroadcastLogUpdate)

		// Start WebSocket heartbeat
		wsHandler.StartHeartbeat()
	}

	r := router.New()

	// Register all handler routes
	providerHandler.RegisterRoutes(r)
	completionHandler.RegisterRoutes(r)
	mcpHandler.RegisterRoutes(r)
	integrationHandler.RegisterRoutes(r)
	configHandler.RegisterRoutes(r)
	if governanceHandler != nil {
		governanceHandler.RegisterRoutes(r)
	}
	if loggingHandler != nil {
		loggingHandler.RegisterRoutes(r)
	}
	if wsHandler != nil {
		wsHandler.RegisterRoutes(r)
	}

	// Add Prometheus /metrics endpoint
	r.GET("/metrics", fasthttpadaptor.NewFastHTTPHandler(promhttp.Handler()))

	// Add UI routes - serve the embedded Next.js build
	r.GET("/", uiHandler)
	r.GET("/{filepath:*}", uiHandler)

	r.NotFound = func(ctx *fasthttp.RequestCtx) {
		handlers.SendError(ctx, fasthttp.StatusNotFound, "Route not found: "+string(ctx.Path()), logger)
	}

	// Apply CORS middleware to all routes
	corsHandler := corsMiddleware(config, r.Handler)

	// Create fasthttp server instance
	server := &fasthttp.Server{
		Handler: corsHandler,
	}

	// Create channels for signal and error handling
	sigChan := make(chan os.Signal, 1)
	errChan := make(chan error, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Start server in a goroutine
	serverAddr := net.JoinHostPort(host, port)
	go func() {
		logger.Info("successfully started bifrost, serving UI on http://%s:%s", host, port)
		if err := server.ListenAndServe(serverAddr); err != nil {
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
		if err := server.Shutdown(); err != nil {
			logger.Error("error during graceful shutdown: %v", err)
		} else {
			logger.Info("server gracefully shutdown")
		}

		// Wait for shutdown to complete or timeout
		done := make(chan struct{})
		go func() {
			defer close(done)
			// Cleanup resources
			if wsHandler != nil {
				wsHandler.Stop()
			}
			client.Cleanup()
		}()

		select {
		case <-done:
			logger.Info("cleanup completed")
		case <-shutdownCtx.Done():
			logger.Warn("cleanup timed out after 30 seconds")
		}

	case err := <-errChan:
		logger.Error("server failed to start: %v", err)
		os.Exit(1)
	}
}
