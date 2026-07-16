// Package main provides an HTTP service using FastHTTP that exposes endpoints
// for text and chat completions using various AI model providers (OpenAI, Anthropic, Bedrock, Mistral, Ollama, etc.).
//
// The HTTP service provides the following main endpoints:
//   - /v1/completions: For text completion requests
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
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	_ "go.uber.org/automaxprocs" // Automatically set GOMAXPROCS based on container cgroup limits

	bifrost "github.com/maximhq/bifrost/core"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/migrator"
	"github.com/maximhq/bifrost/transports/bifrost-http/handlers"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/maximhq/bifrost/transports/bifrost-http/profiling"
	bifrostServer "github.com/maximhq/bifrost/transports/bifrost-http/server"
)

//go:embed all:ui
var uiContent embed.FS

var Version string

var logger = bifrost.NewDefaultLogger(schemas.LogLevelInfo)
var server *bifrostServer.BifrostHTTPServer

var migrateOnly bool
var noMigrate bool
var matviewRefreshOnly bool

// init initializes command line flags (but does not parse them).
// Flag parsing is deferred to main() to avoid conflicts with test flags.
// It sets up the following flags:
//   - host: Host to bind the server to (default: localhost, can be overridden with BIFROST_HOST env var)
//   - port: Server port (default: 8080)
//   - app-dir: Application data directory (default: current directory)
//   - log-level: Logger level (debug, info, warn, error). Default is info.
//   - log-style: Logger output type (json or pretty). Default is JSON.

func init() {
	if Version == "" {
		Version = "v1.0.0"
	}
	// Set default host from environment variable or use localhost
	defaultHost := os.Getenv("BIFROST_HOST")
	if defaultHost == "" {
		defaultHost = bifrostServer.DefaultHost
	}
	defaultLogLevel := strings.ToLower(os.Getenv("LOG_LEVEL"))
	if defaultLogLevel == "" {
		defaultLogLevel = bifrostServer.DefaultLogLevel
	}
	// Initializing server
	server = bifrostServer.NewBifrostHTTPServer(Version, uiContent)
	// Updating server properties from flags
	flag.StringVar(&server.Port, "port", bifrostServer.DefaultPort, "Port to run the server on")
	flag.StringVar(&server.Host, "host", defaultHost, "Host to bind the server to (default: localhost, override with BIFROST_HOST env var)")
	flag.StringVar(&server.AppDir, "app-dir", bifrostServer.DefaultAppDir, "Application data directory (contains config.json and logs)")
	flag.StringVar(&server.LogLevel, "log-level", defaultLogLevel, "Logger level (debug, info, warn, error). Default is info.")
	flag.StringVar(&server.LogOutputStyle, "log-style", bifrostServer.DefaultLogOutputStyle, "Logger output type (json or pretty). Default is JSON.")
	flag.BoolVar(&migrateOnly, "migrate-only", false, "Run database migrations and exit without starting the server (for one-shot migration jobs)")
	flag.BoolVar(&noMigrate, "no-migrate", false, "Skip database migrations on startup; fails if migrations are pending (apply them out of band, e.g. with --migrate-only)")
	flag.BoolVar(&matviewRefreshOnly, "matview-refresh-only", false, "Refresh logstore materialized views once and exit without starting the server (for cron jobs); implies --no-migrate")
}

// main is the entry point of the application.
func main() {
	// Parse command line flags
	flag.Parse()

	// Printing version
	versionLine := fmt.Sprintf("║%s%s%s║", strings.Repeat(" ", (61-2-len(Version))/2), Version, strings.Repeat(" ", (61-2-len(Version)+1)/2))
	// Welcome to bifrost!
	fmt.Printf(`
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
%s
║═══════════════════════════════════════════════════════════║
║                 The Fastest LLM Gateway                   ║
║═══════════════════════════════════════════════════════════║
║             https://github.com/maximhq/bifrost            ║
╚═══════════════════════════════════════════════════════════╝

`, versionLine)

	// Start profiling
	pprofServer := profiling.Start()

	// Configure logger from flags
	logger.SetOutputType(schemas.LoggerOutputType(server.LogOutputStyle))
	logger.SetLevel(schemas.LogLevel(server.LogLevel))
	// Setting up logger
	lib.SetLogger(logger)
	bifrostServer.SetLogger(logger)
	handlers.SetLogger(logger)

	ctx := context.Background()

	if migrateOnly && (noMigrate || matviewRefreshOnly) {
		logger.Error("--migrate-only cannot be combined with --no-migrate or --matview-refresh-only")
		os.Exit(1)
	}
	if noMigrate {
		migrator.SetSkipStartupMigrations(true)
	}
	if migrateOnly {
		migrator.SetOneShotMaintenance(true)
		t := time.Now()
		if err := server.RunMaintenance(ctx); err != nil {
			logger.Error("migration run failed: %v", err)
			os.Exit(1)
		}
		logger.Info("migrations applied in %d ms; exiting (--migrate-only)", time.Since(t).Milliseconds())
		return
	}
	if matviewRefreshOnly {
		migrator.SetSkipStartupMigrations(true)
		migrator.SetOneShotMaintenance(true)
		t := time.Now()
		if err := server.RunMaintenance(ctx); err != nil {
			logger.Error("matview refresh run failed: %v", err)
			os.Exit(1)
		}
		logger.Info("materialized views refreshed in %d ms; exiting (--matview-refresh-only)", time.Since(t).Milliseconds())
		return
	}

	t := time.Now()
	err := server.Bootstrap(ctx)
	if err != nil {
		logger.Error("failed to bootstrap server: %v", err)
		os.Exit(1)
	}
	logger.Info("Time spent in Bifrost server bootstrap %d ms", time.Since(t).Milliseconds())
	err = server.Start()
	if err != nil {
		logger.Error("failed to start server: %v", err)
		os.Exit(1)
	}
	// server.Start() blocks until SIGINT/SIGTERM triggers graceful shutdown, so
	// by here the main server is draining/done. Shut the pprof server down too
	// to let any in-flight profile requests finish instead of being killed.
	if pprofServer != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		if err := pprofServer.Shutdown(shutdownCtx); err != nil {
			logger.Warn("pprof server shutdown error: %v", err)
		}
	}
	logger.Info("🏁 server stopped")
}
