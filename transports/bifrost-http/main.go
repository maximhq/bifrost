// Package http provides an HTTP service using FastHTTP that exposes endpoints
// for text and chat completions using various AI model providers (OpenAI, Anthropic, Bedrock, etc.).
//
// The HTTP service provides two main endpoints:
//   - /v1/text/completions: For text completion requests
//   - /v1/chat/completions: For chat completion requests
//
// Configuration is handled through a JSON config file and environment variables:
//   - Use -config flag to specify the config file location
//   - Use -env flag to specify the .env file location
//   - Use -port flag to specify the server port (default: 8080)
//   - Use -pool-size flag to specify the initial connection pool size (default: 300)
//
// Example usage:
//   go run http.go -config config.example.json -env .env -port 8080 -pool-size 300
//   after setting the environment variables present in config.example.json in your .env file.

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/fasthttp/router"
	bifrost "github.com/maximhq/bifrost/core"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/plugins/maxim"
	"github.com/maximhq/bifrost/transports/bifrost-http/integrations"
	"github.com/maximhq/bifrost/transports/bifrost-http/integrations/genai"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/maximhq/bifrost/transports/bifrost-http/tracking"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/fasthttpadaptor"
)

// Command line flags
var (
	initialPoolSize    int      // Initial size of the connection pool
	dropExcessRequests bool     // Drop excess requests
	port               string   // Port to run the server on
	configPath         string   // Path to the config file
	envPath            string   // Path to the .env file
	pluginsToLoad      []string // Path to the plugins
	maximLogRepoId     string   // ID of the Maxim log repo
	prometheusLabels   []string // Labels to add to Prometheus metrics (optional)
)

// init initializes command line flags and validates required configuration.
// It sets up the following flags:
//   - pool-size: Initial connection pool size (default: 300)
//   - port: Server port (default: 8080)
//   - config: Path to config file (required)
//   - env: Path to .env file (required)
//   - drop-excess-requests: Whether to drop excess requests
func init() {
	pluginString := ""
	var prometheusLabelsString string

	flag.IntVar(&initialPoolSize, "pool-size", 300, "Initial pool size for Bifrost")
	flag.StringVar(&port, "port", "8080", "Port to run the server on")
	flag.StringVar(&configPath, "config", "", "Path to the config file")
	flag.StringVar(&envPath, "env", "", "Path to the .env file")
	flag.BoolVar(&dropExcessRequests, "drop-excess-requests", false, "Drop excess requests")
	flag.StringVar(&pluginString, "plugins", "", "Comma separated list of plugins to load")
	flag.StringVar(&maximLogRepoId, "maxim-log-repo-id", "", "ID of the Maxim log repo")
	flag.StringVar(&prometheusLabelsString, "prometheus-labels", "", "Labels to add to Prometheus metrics")
	flag.Parse()

	pluginsToLoad = strings.Split(pluginString, ",")

	if configPath == "" {
		log.Fatalf("config path is required")
	}

	if envPath == "" {
		log.Fatalf("env path is required")
	}

	if prometheusLabelsString != "" {
		// Split and filter out empty strings
		rawLabels := strings.Split(prometheusLabelsString, ",")
		prometheusLabels = make([]string, 0, len(rawLabels))
		for _, label := range rawLabels {
			if trimmed := strings.TrimSpace(label); trimmed != "" {
				prometheusLabels = append(prometheusLabels, strings.ToLower(trimmed))
			}
		}
	}
}

// CompletionRequest represents a request for either text or chat completion.
// It includes all necessary fields for both types of completions.
type CompletionRequest struct {
	Provider  schemas.ModelProvider    `json:"provider"`  // The AI model provider to use
	Messages  []schemas.Message        `json:"messages"`  // Chat messages (for chat completion)
	Text      string                   `json:"text"`      // Text input (for text completion)
	Model     string                   `json:"model"`     // Model to use
	Params    *schemas.ModelParameters `json:"params"`    // Additional model parameters
	Fallbacks []schemas.Fallback       `json:"fallbacks"` // Fallback providers and models
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
	// Register Prometheus collectors
	registerCollectorSafely(collectors.NewGoCollector())
	registerCollectorSafely(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	tracking.InitPrometheusMetrics(prometheusLabels)

	log.Println("Prometheus Go/Process collectors registered.")

	config := lib.ReadConfig(configPath)
	account := &lib.BaseAccount{Config: config}

	if err := account.ReadKeys(envPath); err != nil {
		log.Printf("warning: failed to read environment variables: %v", err)
	}

	loadedPlugins := []schemas.Plugin{}

	for _, plugin := range pluginsToLoad {
		switch strings.ToLower(plugin) {
		case "maxim":
			if maximLogRepoId == "" {
				log.Println("warning: maxim log repo id is required to initialize maxim plugin")
				continue
			}
			if os.Getenv("MAXIM_API_KEY") == "" {
				log.Println("warning: maxim api key is required in environment variable MAXIM_API_KEY to initialize maxim plugin")
				continue
			}

			maximPlugin, err := maxim.NewMaximLoggerPlugin(os.Getenv("MAXIM_API_KEY"), maximLogRepoId)
			if err != nil {
				log.Printf("warning: failed to initialize maxim plugin: %v", err)
				continue
			}

			loadedPlugins = append(loadedPlugins, maximPlugin)
		}
	}

	promPlugin := tracking.NewPrometheusPlugin()
	loadedPlugins = append(loadedPlugins, promPlugin)

	client, err := bifrost.Init(schemas.BifrostConfig{
		Account:            account,
		InitialPoolSize:    initialPoolSize,
		DropExcessRequests: dropExcessRequests,
		Plugins:            loadedPlugins,
	})
	if err != nil {
		log.Fatalf("failed to initialize bifrost: %v", err)
	}

	r := router.New()

	extensions := []integrations.ExtensionRouter{genai.NewGenAIRouter(client)}

	r.POST("/v1/text/completions", func(ctx *fasthttp.RequestCtx) {
		handleCompletion(ctx, client, false)
	})

	r.POST("/v1/chat/completions", func(ctx *fasthttp.RequestCtx) {
		handleCompletion(ctx, client, true)
	})

	for _, extension := range extensions {
		extension.RegisterRoutes(r)
	}

	// Add Prometheus /metrics endpoint
	r.GET("/metrics", fasthttpadaptor.NewFastHTTPHandler(promhttp.Handler()))

	r.NotFound = func(ctx *fasthttp.RequestCtx) {
		ctx.SetStatusCode(fasthttp.StatusNotFound)
		ctx.SetContentType("text/plain")
		ctx.SetBodyString("Route not found")
	}

	server := &fasthttp.Server{
		// A custom handler that excludes middleware from /metrics
		Handler: func(ctx *fasthttp.RequestCtx) {
			if string(ctx.Path()) == "/metrics" {
				r.Handler(ctx)
				return
			}
			tracking.PrometheusMiddleware(r.Handler)(ctx)
		},
	}

	log.Println("Started Bifrost HTTP server on port", port)
	if err := server.ListenAndServe(fmt.Sprintf(":%s", port)); err != nil {
		log.Fatalf("failed to start server: %v", err)
	}

	client.Shutdown()
}

// handleCompletion processes both text and chat completion requests.
// It handles request parsing, validation, and response formatting.
//
// Parameters:
//   - ctx: The FastHTTP request context
//   - client: The Bifrost client instance
//   - isChat: Whether this is a chat completion request (true) or text completion (false)
//
// The function:
// 1. Parses the request body into a CompletionRequest
// 2. Validates required fields based on the request type
// 3. Creates a BifrostRequest with the appropriate input type
// 4. Calls the appropriate completion method on the client
// 5. Handles any errors and formats the response
func handleCompletion(ctx *fasthttp.RequestCtx, client *bifrost.Bifrost, isChat bool) {
	var req CompletionRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		ctx.SetStatusCode(fasthttp.StatusBadRequest)
		ctx.SetBodyString(fmt.Sprintf("invalid request format: %v", err))
		return
	}

	if req.Provider == "" {
		ctx.SetStatusCode(fasthttp.StatusBadRequest)
		ctx.SetBodyString("Provider is required")
		return
	}

	bifrostReq := &schemas.BifrostRequest{
		Model:     req.Model,
		Params:    req.Params,
		Fallbacks: req.Fallbacks,
	}

	if isChat {
		if len(req.Messages) == 0 {
			ctx.SetStatusCode(fasthttp.StatusBadRequest)
			ctx.SetBodyString("Messages array is required")
			return
		}
		bifrostReq.Input = schemas.RequestInput{
			ChatCompletionInput: &req.Messages,
		}
	} else {
		if req.Text == "" {
			ctx.SetStatusCode(fasthttp.StatusBadRequest)
			ctx.SetBodyString("Text is required")
			return
		}
		bifrostReq.Input = schemas.RequestInput{
			TextCompletionInput: &req.Text,
		}
	}

	bifrostCtx := lib.ConvertToBifrostContext(ctx)

	var resp *schemas.BifrostResponse
	var err *schemas.BifrostError
	if isChat {
		resp, err = client.ChatCompletionRequest(req.Provider, bifrostReq, *bifrostCtx)
	} else {
		resp, err = client.TextCompletionRequest(req.Provider, bifrostReq, *bifrostCtx)
	}

	if err != nil {
		if err.IsBifrostError {
			ctx.SetStatusCode(fasthttp.StatusInternalServerError)
		} else {
			ctx.SetStatusCode(fasthttp.StatusBadRequest)
		}
		ctx.SetContentType("application/json")
		json.NewEncoder(ctx).Encode(err)
		return
	}

	ctx.SetStatusCode(fasthttp.StatusOK)
	ctx.SetContentType("application/json")
	json.NewEncoder(ctx).Encode(resp)
}
