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
//	go run main.go -app-dir ./data -port 8080
//	after setting provider API keys like OPENAI_API_KEY in the environment.
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
	"embed"
	"flag"
	"log"
	"mime"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/fasthttp/router"
	bifrost "github.com/maximhq/bifrost/core"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/plugins/maxim"
	"github.com/maximhq/bifrost/transports/bifrost-http/handlers"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/maximhq/bifrost/transports/bifrost-http/plugins/governance"
	"github.com/maximhq/bifrost/transports/bifrost-http/plugins/logging"
	"github.com/maximhq/bifrost/transports/bifrost-http/plugins/telemetry"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/fasthttpadaptor"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormLogger "gorm.io/gorm/logger"
)

//go:embed all:ui
var uiContent embed.FS

// Command line flags
var (
	port          string   // Port to run the server on
	appDir        string   // Application data directory
	pluginsToLoad []string // Plugins to load
)

// init initializes command line flags and validates required configuration.
// It sets up the following flags:
//   - port: Server port (default: 8080)
//   - app-dir: Application data directory (default: current directory)
//   - plugins: Comma-separated list of plugins to load
func init() {
	pluginString := ""

	flag.StringVar(&port, "port", "8080", "Port to run the server on")
	flag.StringVar(&appDir, "app-dir", "./bifrost-data", "Application data directory (contains config.json and logs)")
	flag.StringVar(&pluginString, "plugins", "", "Comma separated list of plugins to load")
	flag.Parse()

	pluginsToLoad = strings.Split(pluginString, ",")
}

// registerCollectorSafely attempts to register a Prometheus collector,
// handling the case where it may already be registered.
// It logs any errors that occur during registration, except for AlreadyRegisteredError.
func registerCollectorSafely(collector prometheus.Collector) {
	if err := prometheus.Register(collector); err != nil {
		if _, ok := err.(prometheus.AlreadyRegisteredError); !ok {
			log.Printf("Failed to register collector: %v", err)
		}
	}
}

// corsMiddleware handles CORS headers for localhost requests
func corsMiddleware(next fasthttp.RequestHandler) fasthttp.RequestHandler {
	return func(ctx *fasthttp.RequestCtx) {
		origin := string(ctx.Request.Header.Peek("Origin"))

		// Allow requests from localhost on any port
		if strings.HasPrefix(origin, "http://localhost:") || strings.HasPrefix(origin, "https://localhost:") ||
			strings.HasPrefix(origin, "http://127.0.0.1:") || strings.HasPrefix(origin, "https://127.0.0.1:") {
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
// 5. Starts the HTTP server on the specified port
//
// The server exposes the following endpoints:
//   - POST /v1/text/completions: For text completion requests
//   - POST /v1/chat/completions: For chat completion requests
//   - GET /metrics: For Prometheus metrics
func main() {

	configDir := getDefaultConfigDir(appDir)
	// Ensure app directory exists
	if err := os.MkdirAll(configDir, 0755); err != nil {
		log.Fatalf("failed to create app directory %s: %v", configDir, err)
	}

	// Register Prometheus collectors
	registerCollectorSafely(collectors.NewGoCollector())
	registerCollectorSafely(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	logger := bifrost.NewDefaultLogger(schemas.LogLevelInfo)

	// Initialize separate database connections for optimal performance at scale
	configDBPath := filepath.Join(configDir, "config.db")
	configFilePath := filepath.Join(configDir, "config.json")
	logsDBPath := filepath.Join(configDir, "logs.db")

	// Config database: Optimized for high concurrency governance workload
	configDB, err := gorm.Open(sqlite.Open(configDBPath+"?_journal_mode=WAL&_synchronous=NORMAL&_cache_size=10000&_busy_timeout=60000&_wal_autocheckpoint=1000"), &gorm.Config{
		Logger: gormLogger.Default.LogMode(gormLogger.Silent),
	})
	if err != nil {
		log.Fatalf("failed to initialize config database: %v", err)
	}

	// Configure config database for read-heavy workload
	configSQLDB, err := configDB.DB()
	if err != nil {
		log.Fatalf("failed to get config database: %v", err)
	}
	configSQLDB.SetMaxIdleConns(20) // More idle connections for high load

	// Initialize high-performance configuration store with dedicated database
	store, err := lib.NewConfigStore(logger, configDB, configFilePath)
	if err != nil {
		log.Fatalf("failed to initialize config store: %v", err)
	}

	// Load configuration using hybrid file-database approach
	// This checks for config.json file, compares hash with database, and loads accordingly
	if err := store.LoadConfiguration(); err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	// Logs database: Optimized for high-volume writes
	var logsDB *gorm.DB
	if store.ClientConfig.EnableLogging {
		logsDB, err = gorm.Open(sqlite.Open(logsDBPath+"?_journal_mode=WAL&_synchronous=NORMAL&_cache_size=2000&_busy_timeout=30000"), &gorm.Config{
			Logger: gormLogger.Default.LogMode(gormLogger.Silent),
		})
		if err != nil {
			log.Fatalf("failed to initialize logs database: %v", err)
		}

		// Configure logs database for write-heavy workload at scale
		logsSQLDB, err := logsDB.DB()
		if err != nil {
			log.Fatalf("failed to get logs database: %v", err)
		}
		logsSQLDB.SetMaxIdleConns(20) // Higher for concurrent writes
	}

	// Create account backed by the high-performance store (all processing is done in LoadFromDatabase)
	// The account interface now benefits from ultra-fast config access times via in-memory storage
	account := lib.NewBaseAccount(store)

	loadedPlugins := []schemas.Plugin{}

	for _, plugin := range pluginsToLoad {
		switch strings.ToLower(plugin) {
		case "maxim":
			if os.Getenv("MAXIM_LOG_REPO_ID") == "" {
				log.Println("warning: maxim log repo id is required to initialize maxim plugin")
				continue
			}
			if os.Getenv("MAXIM_API_KEY") == "" {
				log.Println("warning: maxim api key is required in environment variable MAXIM_API_KEY to initialize maxim plugin")
				continue
			}

			maximPlugin, err := maxim.NewMaximLoggerPlugin(os.Getenv("MAXIM_API_KEY"), os.Getenv("MAXIM_LOG_REPO_ID"))
			if err != nil {
				log.Printf("warning: failed to initialize maxim plugin: %v", err)
				continue
			}

			loadedPlugins = append(loadedPlugins, maximPlugin)
		}
	}

	telemetry.InitPrometheusMetrics(store.ClientConfig.PrometheusLabels)
	log.Println("Prometheus Go/Process collectors registered.")

	promPlugin := telemetry.NewPrometheusPlugin()

	var loggingPlugin *logging.LoggerPlugin
	var loggingHandler *handlers.LoggingHandler
	var wsHandler *handlers.WebSocketHandler

	if store.ClientConfig.EnableLogging && logsDB != nil {
		// Use dedicated logs database with high-scale optimizations
		loggingPlugin, err = logging.NewLoggerPlugin(logsDB, logger)
		if err != nil {
			log.Fatalf("failed to initialize logging plugin: %v", err)
		}

		loadedPlugins = append(loadedPlugins, loggingPlugin)

		loggingHandler = handlers.NewLoggingHandler(loggingPlugin.GetPluginLogManager(), logger)
		wsHandler = handlers.NewWebSocketHandler(loggingPlugin.GetPluginLogManager(), logger)
	}

	var governancePlugin *governance.GovernancePlugin
	var governanceHandler *handlers.GovernanceHandler

	if store.ClientConfig.EnableGovernance {
		// Initialize governance plugin
		governancePlugin, err = governance.NewGovernancePlugin(configDB, logger, &store.ClientConfig.EnforceGovernanceHeader)
		if err != nil {
			log.Fatalf("failed to initialize governance plugin: %v", err)
		}

		loadedPlugins = append(loadedPlugins, governancePlugin)

		governanceHandler = handlers.NewGovernanceHandler(governancePlugin, configDB, logger)
	}

	loadedPlugins = append(loadedPlugins, promPlugin)

	client, err := bifrost.Init(schemas.BifrostConfig{
		Account:            account,
		InitialPoolSize:    store.ClientConfig.InitialPoolSize,
		DropExcessRequests: store.ClientConfig.DropExcessRequests,
		Plugins:            loadedPlugins,
		MCPConfig:          store.MCPConfig,
		Logger:             logger,
	})
	if err != nil {
		log.Fatalf("failed to initialize bifrost: %v", err)
	}

	store.SetBifrostClient(client)

	// Initialize handlers
	providerHandler := handlers.NewProviderHandler(store, client, logger)
	completionHandler := handlers.NewCompletionHandler(client, logger)
	mcpHandler := handlers.NewMCPHandler(client, logger, store)
	integrationHandler := handlers.NewIntegrationHandler(client)
	configHandler := handlers.NewConfigHandler(client, logger, store)

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
	corsHandler := corsMiddleware(r.Handler)

	log.Printf("Successfully started bifrost. Serving UI on http://localhost:%s", port)
	if err := fasthttp.ListenAndServe(":"+port, corsHandler); err != nil {
		log.Fatalf("Error starting server: %v", err)
	}

	// Cleanup resources on shutdown
	if wsHandler != nil {
		wsHandler.Stop()
	}

	client.Cleanup()
}
