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
			Value:  *schemas.NewSecretVar("env.OPENAI_API_KEY"),
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

// newResponsesStreamResponse builds a BifrostResponse for a responses stream delta event.
func newResponsesStreamResponse(responseID, delta string) *schemas.BifrostResponse {
	d := delta
	id := responseID
	return &schemas.BifrostResponse{
		ResponsesStreamResponse: &schemas.BifrostResponsesStreamResponse{
			Type:  schemas.ResponsesStreamResponseTypeOutputTextDelta,
			Delta: &d,
			Response: &schemas.BifrostResponsesResponse{
				ID: &id,
			},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.ResponsesStreamRequest,
			},
		},
	}
}

// newResponsesStreamNonDeltaResponse builds a BifrostResponse for a non-delta responses stream event.
func newResponsesStreamNonDeltaResponse(responseID string, eventType schemas.ResponsesStreamResponseType) *schemas.BifrostResponse {
	id := responseID
	return &schemas.BifrostResponse{
		ResponsesStreamResponse: &schemas.BifrostResponsesStreamResponse{
			Type: eventType,
			Response: &schemas.BifrostResponsesResponse{
				ID: &id,
			},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.ResponsesStreamRequest,
			},
		},
	}
}

// TestPostLLMHookResponsesStreamDelta verifies that output_text.delta events are accumulated
// and the Delta field is replaced with the repaired partial JSON after each chunk.
func TestPostLLMHookResponsesStreamDelta(t *testing.T) {
	plugin, err := Init(PluginConfig{
		Usage:           AllRequests,
		CleanupInterval: 5 * time.Minute,
		MaxAge:          30 * time.Minute,
	})
	if err != nil {
		t.Fatalf("failed to init plugin: %v", err)
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	const reqID = "test-responses-stream-id"

	chunks := []string{`{"name": "Jo`, `hn", "age": 3`, `0}`}

	for i, chunk := range chunks {
		resp := newResponsesStreamResponse(reqID, chunk)
		result, bifrostErr, hookErr := plugin.PostLLMHook(ctx, resp, nil)
		if hookErr != nil {
			t.Fatalf("chunk %d: unexpected error: %v", i, hookErr)
		}
		if bifrostErr != nil {
			t.Fatalf("chunk %d: unexpected bifrost error: %v", i, bifrostErr)
		}
		if result.ResponsesStreamResponse.Delta == nil {
			t.Fatalf("chunk %d: Delta is nil", i)
		}
		if !plugin.isValidJSON(*result.ResponsesStreamResponse.Delta) {
			t.Errorf("chunk %d: Delta is not valid JSON: %q", i, *result.ResponsesStreamResponse.Delta)
		}
		t.Logf("chunk %d: %q", i, *result.ResponsesStreamResponse.Delta)
	}
}

// TestPostLLMHookResponsesStreamNonDeltaPassthrough verifies that non-delta events
// (e.g. response.created) are passed through without modification.
func TestPostLLMHookResponsesStreamNonDeltaPassthrough(t *testing.T) {
	plugin, err := Init(PluginConfig{
		Usage:           AllRequests,
		CleanupInterval: 5 * time.Minute,
		MaxAge:          30 * time.Minute,
	})
	if err != nil {
		t.Fatalf("failed to init plugin: %v", err)
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	nonDeltaTypes := []schemas.ResponsesStreamResponseType{
		schemas.ResponsesStreamResponseTypeCreated,
		schemas.ResponsesStreamResponseTypeInProgress,
		schemas.ResponsesStreamResponseTypeCompleted,
		schemas.ResponsesStreamResponseTypeOutputItemAdded,
	}

	for _, eventType := range nonDeltaTypes {
		resp := newResponsesStreamNonDeltaResponse("req-passthrough", eventType)
		result, bifrostErr, hookErr := plugin.PostLLMHook(ctx, resp, nil)
		if hookErr != nil {
			t.Fatalf("event %q: unexpected error: %v", eventType, hookErr)
		}
		if bifrostErr != nil {
			t.Fatalf("event %q: unexpected bifrost error: %v", eventType, bifrostErr)
		}
		if result.ResponsesStreamResponse.Delta != nil {
			t.Errorf("event %q: expected nil Delta but got %q", eventType, *result.ResponsesStreamResponse.Delta)
		}
	}
}

// TestPostLLMHookResponsesStreamDoesNotMutateOriginal verifies that the original response
// pointer is not modified when the plugin rewrites Delta.
func TestPostLLMHookResponsesStreamDoesNotMutateOriginal(t *testing.T) {
	plugin, err := Init(PluginConfig{
		Usage:           AllRequests,
		CleanupInterval: 5 * time.Minute,
		MaxAge:          30 * time.Minute,
	})
	if err != nil {
		t.Fatalf("failed to init plugin: %v", err)
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	original := newResponsesStreamResponse("req-mutate", `{"name": "Jo`)
	originalDelta := *original.ResponsesStreamResponse.Delta

	result, bifrostErr, hookErr := plugin.PostLLMHook(ctx, original, nil)
	if hookErr != nil {
		t.Fatalf("unexpected error: %v", hookErr)
	}
	if bifrostErr != nil {
		t.Fatalf("unexpected bifrost error: %v", bifrostErr)
	}

	if *original.ResponsesStreamResponse.Delta != originalDelta {
		t.Errorf("original Delta was mutated: got %q, want %q",
			*original.ResponsesStreamResponse.Delta, originalDelta)
	}
	if result == original {
		t.Error("PostLLMHook returned the original pointer, expected a copy")
	}
}

// TestPostLLMHookResponsesStreamPerRequest verifies that with PerRequest usage the plugin
// only runs when EnableStreamingJSONParser is set in the context.
func TestPostLLMHookResponsesStreamPerRequest(t *testing.T) {
	plugin, err := Init(PluginConfig{
		Usage:           PerRequest,
		CleanupInterval: 5 * time.Minute,
		MaxAge:          30 * time.Minute,
	})
	if err != nil {
		t.Fatalf("failed to init plugin: %v", err)
	}

	resp := newResponsesStreamResponse("req-per", `{"name": "Jo`)
	originalDelta := *resp.ResponsesStreamResponse.Delta

	// Without the context key the plugin should be a no-op.
	ctxOff := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	result, bifrostErr, hookErr := plugin.PostLLMHook(ctxOff, resp, nil)
	if hookErr != nil {
		t.Fatalf("unexpected error (no-op path): %v", hookErr)
	}
	if bifrostErr != nil {
		t.Fatalf("unexpected bifrost error (no-op path): %v", bifrostErr)
	}
	if *result.ResponsesStreamResponse.Delta != originalDelta {
		t.Errorf("expected no-op but Delta changed to %q", *result.ResponsesStreamResponse.Delta)
	}

	// With the context key the plugin should process the chunk.
	ctxOn := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline).
		WithValue(EnableStreamingJSONParser, true)
	result, bifrostErr, hookErr = plugin.PostLLMHook(ctxOn, resp, nil)
	if hookErr != nil {
		t.Fatalf("unexpected error (enabled path): %v", hookErr)
	}
	if bifrostErr != nil {
		t.Fatalf("unexpected bifrost error (enabled path): %v", bifrostErr)
	}
	if !plugin.isValidJSON(*result.ResponsesStreamResponse.Delta) {
		t.Errorf("expected valid JSON but got %q", *result.ResponsesStreamResponse.Delta)
	}
}

// TestJsonParserPluginResponsesStreamEndToEnd tests the full integration against OpenAI's
// responses stream API. Skipped when OPENAI_API_KEY is not set.
func TestJsonParserPluginResponsesStreamEndToEnd(t *testing.T) {
	if os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("OPENAI_API_KEY is not set, skipping end-to-end test")
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	plugin, err := Init(PluginConfig{
		Usage:           AllRequests,
		CleanupInterval: 5 * time.Minute,
		MaxAge:          30 * time.Minute,
	})
	if err != nil {
		t.Fatalf("failed to init plugin: %v", err)
	}

	client, err := bifrost.Init(ctx, schemas.BifrostConfig{
		Account:    &BaseAccount{},
		LLMPlugins: []schemas.LLMPlugin{plugin},
		Logger:     bifrost.NewDefaultLogger(schemas.LogLevelDebug),
	})
	if err != nil {
		t.Fatalf("failed to init bifrost: %v", err)
	}
	defer client.Shutdown()

	userContent := schemas.ResponsesMessageContent{
		ContentStr: bifrost.Ptr(`Return a JSON object with name, age, and city fields. Example: {"name": "John", "age": 30, "city": "New York"}`),
	}
	request := &schemas.BifrostResponsesRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o-mini",
		Input: []schemas.ResponsesMessage{
			{
				Role:    bifrost.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &userContent,
			},
		},
		Params: &schemas.ResponsesParameters{
			Text: &schemas.ResponsesTextConfig{
				Format: &schemas.ResponsesTextConfigFormat{
					Type: "json_object",
				},
			},
		},
	}

	responseChan, bifrostErr := client.ResponsesStreamRequest(ctx, request)
	if bifrostErr != nil {
		t.Fatalf("request error: %v", bifrostErr)
	}

	chunkCount := 0
	for streamChunk := range responseChan {
		if streamChunk.BifrostError != nil {
			t.Logf("stream error: %v", streamChunk.BifrostError)
			continue
		}
		if streamChunk.BifrostResponsesStreamResponse != nil {
			resp := streamChunk.BifrostResponsesStreamResponse
			if resp.Type == schemas.ResponsesStreamResponseTypeOutputTextDelta && resp.Delta != nil && *resp.Delta != "" {
				chunkCount++
				if !plugin.isValidJSON(*resp.Delta) {
					t.Errorf("chunk %d not valid JSON: %q", chunkCount, *resp.Delta)
				}
				t.Logf("chunk %d: %s", chunkCount, *resp.Delta)
			}
		}
	}

	t.Logf("stream completed after %d delta chunks", chunkCount)
	if chunkCount == 0 {
		t.Error("no output_text.delta chunks received; JSON-repair behavior was not exercised")
	}
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
