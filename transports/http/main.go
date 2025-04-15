// Package http provides an HTTP service using FastHTTP that exposes endpoints
// for text and chat completions using various AI model providers (OpenAI, Anthropic, Bedrock, etc.).

// The HTTP service provides two main endpoints:
//   - /v1/text/completions: For text completion requests
//   - /v1/chat/completions: For chat completion requests

// Configuration is handled through a JSON config file and environment variables:
//   - Use -config flag to specify the config file location
//   - Use -env flag to specify the .env file location
//   - Use -port flag to specify the server port (default: 8080)
//   - Use -pool-size flag to specify the initial connection pool size (default: 300)

// try running the server with:
// go run http.go -config config.example.json -env .env -port 8080 -pool-size 300
// after setting the environment variables present in config.example.json in your .env file.

package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"reflect"
	"strings"
	"sync"

	"github.com/fasthttp/router"
	"github.com/joho/godotenv"
	bifrost "github.com/maximhq/bifrost/core"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/core/schemas/meta"
	"github.com/valyala/fasthttp"
)

// Command line flags
var (
	initialPoolSize int    // Initial size of the connection pool
	port            string // Port to run the server on
	configPath      string // Path to the config file
	envPath         string // Path to the .env file
)

// init initializes command line flags with default values.
// It also checks for environment variables that might override the defaults.
func init() {
	flag.IntVar(&initialPoolSize, "pool-size", 300, "Initial pool size for Bifrost")
	flag.StringVar(&port, "port", "8080", "Port to run the server on")
	flag.StringVar(&configPath, "config", "", "Path to the config file")
	flag.StringVar(&envPath, "env", "", "Path to the .env file")
	flag.Parse()

	if configPath == "" {
		log.Fatalf("config path is required")
	}

	if envPath == "" {
		log.Fatalf("env path is required")
	}
}

// ProviderConfig represents the configuration for a specific AI model provider.
// It includes API keys, network settings, provider-specific metadata, and concurrency settings.
type ProviderConfig struct {
	Keys                     []schemas.Key                     `json:"keys"`                                  // API keys for the provider
	NetworkConfig            *schemas.NetworkConfig            `json:"network_config,omitempty"`              // Network-related settings
	MetaConfig               *schemas.MetaConfig               `json:"-"`                                     // Provider-specific metadata
	ConcurrencyAndBufferSize *schemas.ConcurrencyAndBufferSize `json:"concurrency_and_buffer_size,omitempty"` // Concurrency settings
}

// ConfigMap maps provider names to their configurations.
type ConfigMap map[schemas.ModelProvider]ProviderConfig

// readConfig reads and parses the configuration file.
// It handles case conversion for provider names and sets up provider-specific metadata.
// Returns a ConfigMap containing all provider configurations.
// Panics if the config file cannot be read or parsed.
//
// In the config file, use placeholder keys (e.g., env.OPEN_AI_API_KEY) instead of hardcoding actual values.
// These placeholders will be replaced with the corresponding values from the .env file.
// Location of the .env file is specified by the -env flag. It
// Example:
//
//	"keys":[{
//		 "value": "env.OPEN_AI_API_KEY"
//	     "models": ["gpt-4o-mini", "gpt-4-turbo"],
//	     "weight": 1.0
//	}]
//
// In this example, OPEN_AI_API_KEY refers to a key in the .env file. At runtime, its value will be used to replace the placeholder.
// Same setup applies to keys in meta configs of all the providers.
// Example:
//
//	"meta_config": {
//		"secret_access_key": "env.BEDROCK_ACCESS_KEY"
//		"region": "env.BEDROCK_REGION"
//	}
//
// In this example, BEDROCK_ACCESS_KEY and BEDROCK_REGION refer to keys in the .env file.
func readConfig(configLocation string) ConfigMap {
	data, err := os.ReadFile(configLocation)
	if err != nil {
		log.Fatalf("failed to read config JSON file: %v", err)
	}

	// First unmarshal into a map with string keys to handle case conversion
	var rawConfig map[string]ProviderConfig
	if err := json.Unmarshal(data, &rawConfig); err != nil {
		log.Fatalf("failed to unmarshal JSON: %v", err)
	}

	if rawConfig == nil {
		log.Fatalf("provided config is nil")
	}

	// Create a new config map with lowercase provider names
	config := make(ConfigMap)
	for rawProvider, cfg := range rawConfig {
		provider := schemas.ModelProvider(strings.ToLower(rawProvider))

		switch provider {
		case schemas.Azure:
			var azureMetaConfig meta.AzureMetaConfig
			if err := json.Unmarshal(data, &struct {
				Azure struct {
					MetaConfig *meta.AzureMetaConfig `json:"meta_config"`
				} `json:"Azure"`
			}{Azure: struct {
				MetaConfig *meta.AzureMetaConfig `json:"meta_config"`
			}{&azureMetaConfig}}); err != nil {
				log.Printf("warning: failed to unmarshal Azure meta config: %v", err)
			}
			var metaConfig schemas.MetaConfig = &azureMetaConfig
			cfg.MetaConfig = &metaConfig
		case schemas.Bedrock:
			var bedrockMetaConfig meta.BedrockMetaConfig
			if err := json.Unmarshal(data, &struct {
				Bedrock struct {
					MetaConfig *meta.BedrockMetaConfig `json:"meta_config"`
				} `json:"Bedrock"`
			}{Bedrock: struct {
				MetaConfig *meta.BedrockMetaConfig `json:"meta_config"`
			}{&bedrockMetaConfig}}); err != nil {
				log.Printf("warning: failed to unmarshal Bedrock meta config: %v", err)
			}
			var metaConfig schemas.MetaConfig = &bedrockMetaConfig
			cfg.MetaConfig = &metaConfig
		}

		config[provider] = cfg
	}

	return config
}

// BaseAccount implements the Account interface for Bifrost.
// It manages provider configurations and API keys.
type BaseAccount struct {
	Config ConfigMap  // Map of provider configurations
	mu     sync.Mutex // Mutex to protect Config access
}

// GetConfiguredProviders returns a list of all configured providers.
// Implements the Account interface.
func (baseAccount *BaseAccount) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
	baseAccount.mu.Lock()
	defer baseAccount.mu.Unlock()

	providers := make([]schemas.ModelProvider, 0, len(baseAccount.Config))
	for provider := range baseAccount.Config {
		providers = append(providers, provider)
	}
	return providers, nil
}

// GetKeysForProvider returns the API keys configured for a specific provider.
// Implements the Account interface.
func (baseAccount *BaseAccount) GetKeysForProvider(providerKey schemas.ModelProvider) ([]schemas.Key, error) {
	baseAccount.mu.Lock()
	defer baseAccount.mu.Unlock()

	return baseAccount.Config[providerKey].Keys, nil
}

// GetConfigForProvider returns the complete configuration for a specific provider.
// Implements the Account interface.
func (baseAccount *BaseAccount) GetConfigForProvider(providerKey schemas.ModelProvider) (*schemas.ProviderConfig, error) {
	baseAccount.mu.Lock()
	defer baseAccount.mu.Unlock()

	config, exists := baseAccount.Config[providerKey]
	if !exists {
		return nil, errors.New("config for provider not found")
	}

	providerConfig := &schemas.ProviderConfig{}

	if config.NetworkConfig != nil {
		providerConfig.NetworkConfig = *config.NetworkConfig
	}

	if config.MetaConfig != nil {
		providerConfig.MetaConfig = *config.MetaConfig
	}

	if config.ConcurrencyAndBufferSize != nil {
		providerConfig.ConcurrencyAndBufferSize = *config.ConcurrencyAndBufferSize
	}

	return providerConfig, nil
}

// readKeys reads environment variables from a .env file and updates the provider configurations.
// It replaces values starting with "env." in the config with actual values from the environment.
// Returns an error if any required environment variable is missing.
func (baseAccount *BaseAccount) readKeys(envLocation string) error {
	envVars, err := godotenv.Read(envLocation)
	if err != nil {
		return fmt.Errorf("failed to read .env file: %w", err)
	}

	// Helper function to check and replace env values
	replaceEnvValue := func(value string) (string, error) {
		if strings.HasPrefix(value, "env.") {
			envKey := strings.TrimPrefix(value, "env.")
			if envValue, exists := envVars[envKey]; exists {
				return envValue, nil
			}
			return "", fmt.Errorf("environment variable %s not found in .env file", envKey)
		}
		return value, nil
	}

	// Helper function to recursively check and replace env values in a struct
	var processStruct func(interface{}) error
	processStruct = func(v interface{}) error {
		val := reflect.ValueOf(v)

		// Dereference pointer if present
		if val.Kind() == reflect.Ptr {
			val = val.Elem()
		}

		// Handle interface types
		if val.Kind() == reflect.Interface {
			val = val.Elem()
			// If the interface value is a pointer, dereference it
			if val.Kind() == reflect.Ptr {
				val = val.Elem()
			}
		}

		if val.Kind() != reflect.Struct {
			return nil
		}

		typ := val.Type()
		for i := 0; i < val.NumField(); i++ {
			field := val.Field(i)
			fieldType := typ.Field(i)

			// Skip unexported fields
			if !field.CanSet() {
				continue
			}

			switch field.Kind() {
			case reflect.String:
				if field.CanSet() {
					value := field.String()
					if strings.HasPrefix(value, "env.") {
						newValue, err := replaceEnvValue(value)
						if err != nil {
							return fmt.Errorf("field %s: %w", fieldType.Name, err)
						}
						field.SetString(newValue)
					}
				}
			case reflect.Interface:
				if !field.IsNil() {
					if err := processStruct(field.Interface()); err != nil {
						return err
					}
				}
			}
		}
		return nil
	}

	// Lock the config map for the entire update operation
	baseAccount.mu.Lock()
	defer baseAccount.mu.Unlock()

	// Check and replace values in provider configs
	for provider, config := range baseAccount.Config {
		// Check keys
		for i, key := range config.Keys {
			newValue, err := replaceEnvValue(key.Value)
			if err != nil {
				return fmt.Errorf("provider %s: %w", provider, err)
			}
			config.Keys[i].Value = newValue
		}

		// Check meta config if it exists
		if config.MetaConfig != nil {
			if err := processStruct(config.MetaConfig); err != nil {
				return fmt.Errorf("provider %s: %w", provider, err)
			}
		}

		baseAccount.Config[provider] = config
	}

	return nil
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

// handleCompletion processes both text and chat completion requests.
// It handles request parsing, validation, and response formatting.
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

	var resp *schemas.BifrostResponse
	var err *schemas.BifrostError
	if isChat {
		resp, err = client.ChatCompletionRequest(req.Provider, bifrostReq, ctx)
	} else {
		resp, err = client.TextCompletionRequest(req.Provider, bifrostReq, ctx)
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

// main is the entry point of the application.
// It:
// 1. Reads and parses configuration
// 2. Initializes the Bifrost client
// 3. Sets up HTTP routes
// 4. Starts the HTTP server
func main() {
	config := readConfig(configPath)
	account := &BaseAccount{Config: config}

	if err := account.readKeys(envPath); err != nil {
		log.Printf("warning: failed to read environment variables: %v", err)
	}

	client, err := bifrost.Init(schemas.BifrostConfig{
		Account:         account,
		InitialPoolSize: initialPoolSize,
	})
	if err != nil {
		log.Fatalf("failed to initialize bifrost: %v", err)
	}

	r := router.New()

	r.POST("/v1/text/completions", func(ctx *fasthttp.RequestCtx) {
		handleCompletion(ctx, client, false)
	})

	r.POST("/v1/chat/completions", func(ctx *fasthttp.RequestCtx) {
		handleCompletion(ctx, client, true)
	})

	server := &fasthttp.Server{
		Handler: r.Handler,
	}

	fmt.Printf("Starting HTTP server on port %s\n", port)
	if err := server.ListenAndServe(fmt.Sprintf(":%s", port)); err != nil {
		log.Fatalf("failed to start server: %v", err)
	}

	client.Shutdown()
}
