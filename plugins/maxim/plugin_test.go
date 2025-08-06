// Package maxim provides integration for Maxim's SDK as a Bifrost plugin.
// It includes tests for plugin initialization, Bifrost integration, and request/response tracing.
package maxim

import (
	"context"
	"fmt"
	"log"
	"os"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// getPlugin initializes and returns a Plugin instance for testing purposes.
// It sets up the Maxim logger with configuration from environment variables.
//
// Environment Variables:
//   - MAXIM_API_KEY: API key for Maxim SDK authentication
//   - MAXIM_LOGGER_ID: ID for the Maxim logger instance
//
// Returns:
//   - schemas.Plugin: A configured plugin instance for request/response tracing
//   - error: Any error that occurred during plugin initialization
func getPlugin() (schemas.Plugin, error) {
	// check if Maxim Logger variables are set
	if os.Getenv("MAXIM_API_KEY") == "" {
		return nil, fmt.Errorf("MAXIM_API_KEY is not set, please set it in your environment variables")
	}

	if os.Getenv("MAXIM_LOGGER_ID") == "" {
		return nil, fmt.Errorf("MAXIM_LOGGER_ID is not set, please set it in your environment variables")
	}

	plugin, err := NewMaximLoggerPlugin(os.Getenv("MAXIM_API_KEY"), os.Getenv("MAXIM_LOGGER_ID"))
	if err != nil {
		return nil, err
	}

	return plugin, nil
}

// BaseAccount implements the schemas.Account interface for testing purposes.
// It provides mock implementations of the required methods to test the Maxim plugin
// with a basic OpenAI configuration.
type BaseAccount struct{}

// GetConfiguredProviders returns a list of supported providers for testing.
// Currently only supports OpenAI for simplicity in testing. You are free to add more providers as needed.
func (baseAccount *BaseAccount) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
	return []schemas.ModelProvider{schemas.OpenAI}, nil
}

// GetKeysForProvider returns a mock API key configuration for testing.
// Uses the OPENAI_API_KEY environment variable for authentication.
func (baseAccount *BaseAccount) GetKeysForProvider(ctx *context.Context, providerKey schemas.ModelProvider) ([]schemas.Key, error) {
	return []schemas.Key{
		{
			Value:  os.Getenv("OPENAI_API_KEY"),
			Models: []string{"gpt-4o-mini", "gpt-4-turbo"},
			Weight: 1.0,
		},
	}, nil
}

// GetConfigForProvider returns default provider configuration for testing.
// Uses standard network and concurrency settings.
func (baseAccount *BaseAccount) GetConfigForProvider(providerKey schemas.ModelProvider) (*schemas.ProviderConfig, error) {
	return &schemas.ProviderConfig{
		NetworkConfig:            schemas.DefaultNetworkConfig,
		ConcurrencyAndBufferSize: schemas.DefaultConcurrencyAndBufferSize,
	}, nil
}

// TestMaximLoggerPlugin tests the integration of the Maxim Logger plugin with Bifrost.
// It performs the following steps:
// 1. Initializes the Maxim plugin with environment variables
// 2. Sets up a test Bifrost instance with the plugin
// 3. Makes a test chat completion request
//
// Required environment variables:
//   - MAXIM_API_KEY: Your Maxim API key
//   - MAXIM_LOGGER_ID: Your Maxim logger repository ID
//   - OPENAI_API_KEY: Your OpenAI API key for the test request
func TestMaximLoggerPlugin(t *testing.T) {
	// Initialize the Maxim plugin
	plugin, err := getPlugin()
	if err != nil {
		log.Fatalf("Error setting up the plugin: %v", err)
	}

	account := BaseAccount{}

	// Initialize Bifrost with the plugin
	client, err := bifrost.Init(schemas.BifrostConfig{
		Account: &account,
		Plugins: []schemas.Plugin{plugin},
		Logger:  bifrost.NewDefaultLogger(schemas.LogLevelDebug),
	})
	if err != nil {
		log.Fatalf("Error initializing Bifrost: %v", err)
	}

	// Make a test chat completion request
	_, bifrostErr := client.ChatCompletionRequest(context.Background(), &schemas.BifrostRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o-mini",
		Input: schemas.RequestInput{
			ChatCompletionInput: &[]schemas.BifrostMessage{
				{
					Role: "user",
					Content: schemas.MessageContent{
						ContentStr: bifrost.Ptr("Hello, how are you?"),
					},
				},
			},
		},
	})

	if bifrostErr != nil {
		log.Printf("Error in Bifrost request: %v", bifrostErr)
	}

	log.Println("Bifrost request completed, check your Maxim Dashboard for the trace")

	client.Cleanup()
}
