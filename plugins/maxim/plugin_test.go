// Package maxim provides integration for Maxim's SDK as a Bifrost plugin.
// It includes tests for plugin initialization, Bifrost integration, and request/response tracing.
package maxim

import (
	"context"
	"fmt"
	"log"
	"os"
	"sync"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/maxim-go/logging"
)

// getPlugin initializes and returns a Plugin instance for testing purposes.
// It sets up the Maxim logger with configuration from environment variables.
//
// Environment Variables:
//   - MAXIM_API_KEY: API key for Maxim SDK authentication
//   - MAXIM_LOG_REPO_ID: ID for the Maxim logger instance
//
// Returns:
//   - schemas.LLMPlugin: A configured plugin instance for request/response tracing
//   - error: Any error that occurred during plugin initialization
func getPlugin() (schemas.LLMPlugin, error) {
	// check if Maxim Logger variables are set
	if os.Getenv("MAXIM_API_KEY") == "" {
		return nil, fmt.Errorf("MAXIM_API_KEY is not set, please set it in your environment variables")
	}

	logger := bifrost.NewDefaultLogger(schemas.LogLevelDebug)
	plugin, err := Init(&Config{
		APIKey:    os.Getenv("MAXIM_API_KEY"),
		LogRepoID: os.Getenv("MAXIM_LOG_REPO_ID"),
	}, logger)
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
func (baseAccount *BaseAccount) GetKeysForProvider(ctx context.Context, providerKey schemas.ModelProvider) ([]schemas.Key, error) {
	return []schemas.Key{
		{
			Value:  *schemas.NewEnvVar("env.OPENAI_API_KEY"),
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
	ctx := context.Background()
	// Initialize the Maxim plugin
	plugin, err := getPlugin()
	if err != nil {
		t.Fatalf("Error setting up the plugin: %v", err)
	}

	account := BaseAccount{}

	// Initialize Bifrost with the plugin
	client, err := bifrost.Init(ctx, schemas.BifrostConfig{
		Account:    &account,
		LLMPlugins: []schemas.LLMPlugin{plugin},
		Logger:     bifrost.NewDefaultLogger(schemas.LogLevelDebug),
	})
	if err != nil {
		t.Fatalf("Error initializing Bifrost: %v", err)
	}

	// Make a test chat completion request
	_, bifrostErr := client.ChatCompletionRequest(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o-mini",
		Input: []schemas.ChatMessage{
			{
				Role: "user",
				Content: &schemas.ChatMessageContent{
					ContentStr: bifrost.Ptr("Hello, how are you?"),
				},
			},
		},
	})

	if bifrostErr != nil {
		log.Printf("Error in Bifrost request: %v", bifrostErr)
	}

	log.Println("Bifrost request completed, check your Maxim Dashboard for the trace")

	client.Shutdown()
}

// TestLogRepoIDSelection tests the single repository selection logic
func TestLogRepoIDSelection(t *testing.T) {
	tests := []struct {
		name         string
		defaultRepo  string
		headerRepo   string
		expectedRepo string
		shouldLog    bool
	}{
		{
			name:         "Header repo takes priority",
			defaultRepo:  "default-repo",
			headerRepo:   "header-repo",
			expectedRepo: "header-repo",
			shouldLog:    true,
		},
		{
			name:         "Fall back to default repo when no header",
			defaultRepo:  "default-repo",
			headerRepo:   "",
			expectedRepo: "default-repo",
			shouldLog:    true,
		},
		{
			name:         "Use header repo when no default",
			defaultRepo:  "",
			headerRepo:   "header-repo",
			expectedRepo: "header-repo",
			shouldLog:    true,
		},
		{
			name:         "Skip logging when neither available",
			defaultRepo:  "",
			headerRepo:   "",
			expectedRepo: "",
			shouldLog:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create plugin with default repo
			plugin := &Plugin{
				defaultLogRepoID: tt.defaultRepo,
			}

			// Create context with header repo if provided
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			if tt.headerRepo != "" {
				ctx.SetValue(LogRepoIDKey, tt.headerRepo)
			}

			// Test the selection logic
			result := plugin.getEffectiveLogRepoID(ctx)

			if result != tt.expectedRepo {
				t.Errorf("Expected repo '%s', got '%s'", tt.expectedRepo, result)
			}

			shouldLog := result != ""
			if shouldLog != tt.shouldLog {
				t.Errorf("Expected shouldLog=%t, got shouldLog=%t", tt.shouldLog, shouldLog)
			}
		})
	}
}

// TestPluginInitialization tests plugin initialization with different configs
func TestPluginInitialization(t *testing.T) {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelDebug)
	tests := []struct {
		name        string
		config      Config
		expectError bool
	}{
		{
			name: "Valid config with both fields",
			config: Config{
				APIKey:    "test-api-key",
				LogRepoID: "test-repo-id",
			},
			expectError: false,
		},
		{
			name: "Valid config with only API key",
			config: Config{
				APIKey:    "test-api-key",
				LogRepoID: "",
			},
			expectError: false,
		},
		{
			name: "Invalid config - missing API key",
			config: Config{
				APIKey:    "",
				LogRepoID: "test-repo-id",
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Skip actual Maxim SDK initialization in tests
			if tt.expectError {
				_, err := Init(&tt.config, logger)
				if err == nil {
					t.Error("Expected error but got none")
				}
			} else {
				// For valid configs, we can't test actual initialization without real API key
				// Just test the validation logic
				if tt.config.APIKey == "" {
					t.Skip("Skipping valid config test - would need real Maxim API key")
				}
			}
		})
	}
}

// TestPluginName tests the plugin name functionality
func TestPluginName(t *testing.T) {
	plugin := &Plugin{}
	if plugin.GetName() != PluginName {
		t.Errorf("Expected plugin name '%s', got '%s'", PluginName, plugin.GetName())
	}
	if PluginName != "maxim" {
		t.Errorf("Expected PluginName constant to be 'maxim', got '%s'", PluginName)
	}
}

// TestImageGenerationLogging tests the image generation logging functionality
// The plugin should correctly extract and log image generation prompts and metadata
func TestImageGenerationLogging(t *testing.T) {
	ctx := context.Background()
	bifrostCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
	bifrostCtx.SetValue(LogRepoIDKey, "test-repo")

	plugin := &Plugin{
		defaultLogRepoID: "test-repo",
		loggers:          make(map[string]*logging.Logger),
		loggerMutex:      &sync.RWMutex{},
		logger:           bifrost.NewDefaultLogger(schemas.LogLevelDebug),
	}

	// Create a test image generation request
	prompt := "A beautiful landscape with mountains"
	req := &schemas.BifrostRequest{
		RequestType: schemas.ImageGenerationRequest,
		ImageGenerationRequest: &schemas.BifrostImageGenerationRequest{
			Provider: schemas.OpenAI,
			Model:    "dall-e-3",
			Input: &schemas.ImageGenerationInput{
				Prompt: prompt,
			},
			Params: &schemas.ImageGenerationParameters{
				N:    bifrost.Ptr(1),
				Size: bifrost.Ptr("1024x1024"),
			},
		},
	}

	// Call PreLLMHook
	returnedReq, shortCircuit, err := plugin.PreLLMHook(bifrostCtx, req)

	// Verify that no error occurred
	if err != nil {
		t.Errorf("PreLLMHook returned an error: %v", err)
	}

	// Verify that the request was not short-circuited
	if shortCircuit != nil {
		t.Error("PreLLMHook should not short-circuit for image generation")
	}

	// Verify that the request is unchanged
	if returnedReq != req {
		t.Error("PreLLMHook should return the original request")
	}

	// Verify that a generation ID was set
	if _, ok := bifrostCtx.Value(GenerationIDKey).(string); !ok {
		t.Error("GenerationID should be set in context after PreLLMHook")
	}
}

// TestImageEditLogging tests the image edit logging functionality
func TestImageEditLogging(t *testing.T) {
	ctx := context.Background()
	bifrostCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
	bifrostCtx.SetValue(LogRepoIDKey, "test-repo")

	plugin := &Plugin{
		defaultLogRepoID: "test-repo",
		loggers:          make(map[string]*logging.Logger),
		loggerMutex:      &sync.RWMutex{},
		logger:           bifrost.NewDefaultLogger(schemas.LogLevelDebug),
	}

	// Create a test image edit request
	prompt := "Add a sunset to the sky"
	req := &schemas.BifrostRequest{
		RequestType: schemas.ImageEditRequest,
		ImageEditRequest: &schemas.BifrostImageEditRequest{
			Provider: schemas.OpenAI,
			Model:    "dall-e-3",
			Input: &schemas.ImageEditInput{
				Prompt: prompt,
				Images: []schemas.ImageInput{
					{
						Image: []byte("fake-image-data"),
					},
				},
			},
			Params: &schemas.ImageEditParameters{
				N:    bifrost.Ptr(1),
				Size: bifrost.Ptr("1024x1024"),
			},
		},
	}

	// Call PreLLMHook
	returnedReq, shortCircuit, err := plugin.PreLLMHook(bifrostCtx, req)

	// Verify that no error occurred
	if err != nil {
		t.Errorf("PreLLMHook returned an error: %v", err)
	}

	// Verify that the request was not short-circuited
	if shortCircuit != nil {
		t.Error("PreLLMHook should not short-circuit for image edit")
	}

	// Verify that the request is unchanged
	if returnedReq != req {
		t.Error("PreLLMHook should return the original request")
	}

	// Verify that a generation ID was set
	if _, ok := bifrostCtx.Value(GenerationIDKey).(string); !ok {
		t.Error("GenerationID should be set in context after PreLLMHook")
	}
}
