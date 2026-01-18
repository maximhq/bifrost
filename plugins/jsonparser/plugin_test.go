package jsonparser

import (
	"context"
	"os"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// BaseAccount implements the schemas.Account interface for testing purposes.
// It provides mock implementations of the required methods to test the JSON parser plugin
// with a basic OpenAI configuration.
type BaseAccount struct{}

// GetConfiguredProviders returns a list of supported providers for testing.
// Currently only supports OpenAI for simplicity in testing.
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

// TestJsonParserPluginEndToEnd tests the integration of the JSON parser plugin with Bifrost.
// It performs the following steps:
// 1. Initializes the JSON parser plugin with AllRequests usage
// 2. Sets up a test Bifrost instance with the plugin
// 3. Makes a test chat completion request with streaming enabled
// 4. Verifies that the plugin processes the streaming response correctly
//
// Required environment variables:
//   - OPENAI_API_KEY: Your OpenAI API key for the test request
func TestJsonParserPluginEndToEnd(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	// Check if OpenAI API key is set
	if os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("OPENAI_API_KEY is not set, skipping end-to-end test")
	}

	// Initialize the JSON parser plugin for all requests
	plugin, err := Init(PluginConfig{
		Usage:           AllRequests,
		CleanupInterval: 5 * time.Minute,
		MaxAge:          30 * time.Minute,
	})
	if err != nil {
		t.Fatalf("Error initializing JSON parser plugin: %v", err)
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
	defer client.Shutdown()

	// Make a test responses request with streaming enabled
	// Request JSON output to test the parser
	var responseFormat interface{} = map[string]interface{}{
		"type": "json_object",
	}

	request := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o-mini",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: bifrost.Ptr("Return a JSON object with name, age, and city fields. Example: {\"name\": \"John\", \"age\": 30, \"city\": \"New York\"}"),
				},
			},
		},
		Params: &schemas.ChatParameters{
			ResponseFormat: &responseFormat,
		},
	}
	// Make the streaming request
	responseChan, bifrostErr := client.ChatCompletionStreamRequest(ctx, request)

	if bifrostErr != nil {
		t.Fatalf("Error in Bifrost request: %v", bifrostErr)
	}

	// Process streaming responses
	if responseChan != nil {
		t.Logf("Streaming response channel received")

		// Read from the channel to see the streaming responses
		responseCount := 0

		for streamResponse := range responseChan {
			responseCount++

			if streamResponse.BifrostError != nil {
				t.Logf("Streaming response error: %v", streamResponse.BifrostError)
			}

			if streamResponse.BifrostChatResponse != nil {
				if streamResponse.BifrostChatResponse.Choices != nil {
					for _, outputMsg := range streamResponse.BifrostChatResponse.Choices {
						if outputMsg.ChatStreamResponseChoice != nil && outputMsg.ChatStreamResponseChoice.Delta.Content != nil {
							content := *outputMsg.ChatStreamResponseChoice.Delta.Content
							if content != "" {
								t.Logf("Chunk %d: %s", responseCount, content)
							}
						}
					}
				}
			}
		}

		t.Logf("Stream completed after %d responses", responseCount)
	} else {
		t.Logf("No streaming response channel received")
	}

	t.Log("End-to-end test completed - check logs for JSON parsing behavior")
}

// TestJsonParserPluginPerRequest tests the per-request configuration of the JSON parser plugin.
// It tests how the plugin behaves when enabled via context for specific requests.
//
// Required environment variables:
//   - OPENAI_API_KEY: Your OpenAI API key for the test request
func TestJsonParserPluginPerRequest(t *testing.T) {
	ctx := context.Background()
	// Check if OpenAI API key is set
	if os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("OPENAI_API_KEY is not set, skipping per-request test")
	}

	// Initialize the JSON parser plugin for per-request usage
	plugin, err := Init(PluginConfig{
		Usage:           PerRequest,
		CleanupInterval: 5 * time.Minute,
		MaxAge:          30 * time.Minute,
	})
	if err != nil {
		t.Fatalf("Error initializing JSON parser plugin: %v", err)
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
	defer client.Shutdown()

	// Test request with plugin enabled via context
	var responseFormat interface{} = map[string]interface{}{
		"type": "json_object",
	}

	request := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o-mini",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: bifrost.Ptr("Return a JSON object with name and age fields."),
				},
			},
		},
		Params: &schemas.ChatParameters{
			ResponseFormat: &responseFormat,
		},
	}

	// Create context with plugin enabled
	newContext := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline).WithValue(EnableStreamingJSONParser, true)

	// Make the streaming request
	responseChan, bifrostErr := client.ChatCompletionStreamRequest(newContext, request)

	if bifrostErr != nil {
		t.Logf("Error in Bifrost request: %v", bifrostErr)
	}

	// Process streaming responses
	if responseChan != nil {
		t.Logf("Streaming response channel received for per-request test")

		// Read from the channel to see the streaming responses
		responseCount := 0

		for streamResponse := range responseChan {
			responseCount++

			if streamResponse.BifrostError != nil {
				t.Logf("Streaming response error: %v", streamResponse.BifrostError)
			}

			if streamResponse.BifrostChatResponse != nil {
				for _, choice := range streamResponse.BifrostChatResponse.Choices {
					if choice.ChatStreamResponseChoice != nil && choice.ChatStreamResponseChoice.Delta.Content != nil {
						content := *choice.ChatStreamResponseChoice.Delta.Content
						if content != "" {
							t.Logf("Per-request chunk %d: %s", responseCount, content)
						}
					}
				}
			}
		}

		t.Logf("Per-request stream completed after %d responses", responseCount)
	} else {
		t.Logf("No streaming response channel received for per-request test")
	}

	t.Log("Per-request test completed - check logs for JSON parsing behavior")
}

func TestParsePartialJSON(t *testing.T) {
	plugin, err := Init(PluginConfig{
		Usage:           AllRequests,
		CleanupInterval: 5 * time.Minute,
		MaxAge:          30 * time.Minute,
	})
	if err != nil {
		t.Fatalf("Error initializing JSON parser plugin: %v", err)
	}

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Already valid JSON object",
			input:    `{"name": "John", "age": 30}`,
			expected: `{"name": "John", "age": 30}`,
		},
		{
			name:     "Partial JSON object missing closing brace",
			input:    `{"name": "John", "age": 30, "city": "New York"`,
			expected: `{"name": "John", "age": 30, "city": "New York"}`,
		},
		{
			name:     "Partial JSON array missing closing bracket",
			input:    `["apple", "banana", "cherry"`,
			expected: `["apple", "banana", "cherry"]`,
		},
		{
			name:     "Nested partial JSON",
			input:    `{"user": {"name": "John", "details": {"age": 30, "city": "NY"`,
			expected: `{"user": {"name": "John", "details": {"age": 30, "city": "NY"}}}`,
		},
		{
			name:     "Partial JSON with string containing newline",
			input:    `{"message": "Hello\nWorld"`,
			expected: `{"message": "Hello\nWorld"}`,
		},
		{
			name:     "Empty string",
			input:    "",
			expected: "{}",
		},
		{
			name:     "Whitespace only",
			input:    "   \n\t  ",
			expected: "{}",
		},
		{
			name:     "Non-JSON string",
			input:    "This is not JSON",
			expected: "This is not JSON",
		},
		{
			name:     "Partial JSON with escaped quotes",
			input:    `{"message": "He said \"Hello\""`,
			expected: `{"message": "He said \"Hello\""}`,
		},
		{
			name:     "Complex nested structure",
			input:    `{"data": {"users": [{"id": 1, "name": "John"}, {"id": 2, "name": "Jane"`,
			expected: `{"data": {"users": [{"id": 1, "name": "John"}, {"id": 2, "name": "Jane"}]}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := plugin.parsePartialJSON(tt.input)
			if result != tt.expected {
				t.Errorf("parsePartialJSON(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}
