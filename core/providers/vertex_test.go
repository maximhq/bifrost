package providers

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestVertexChatCompletion(t *testing.T) {
	credentials := os.Getenv("VERTEX_CREDENTIALS")
	projectID := os.Getenv("VERTEX_PROJECT_ID")
	if credentials == "" || projectID == "" {
		t.Fatal("VERTEX_CREDENTIALS or VERTEX_PROJECT_ID not set")
	}

	provider, err := NewVertexProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			DefaultRequestTimeoutInSeconds: 60,
		},
		ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
			Concurrency: 10,
		},
	}, newTestLogger())
	if err != nil {
		t.Fatalf("Failed to create Vertex provider: %v", err)
	}

	ctx := context.Background()
	key := schemas.Key{
		VertexKeyConfig: &schemas.VertexKeyConfig{
			ProjectID:       projectID,
			Region:          "us-central1",
			AuthCredentials: credentials,
		},
	}

	request := &schemas.BifrostChatRequest{
		Provider: schemas.Vertex,
		Model:    "claude-3-5-haiku",
		Input: []schemas.ChatMessage{
			{
				Role:    "user",
				Content: &schemas.ChatMessageContent{ContentStr: stringPtr("Say hello in one word")},
			},
		},
		Params: &schemas.ChatParameters{
			Temperature:         float64Ptr(0.7),
			MaxCompletionTokens: intPtr(10),
		},
	}

	resp, bfErr := provider.ChatCompletion(ctx, key, request)
	if bfErr != nil {
		if bfErr.Error != nil {
			errorMsg := bfErr.Error.Message
			if bfErr.Error.Error != nil {
				errorMsg += fmt.Sprintf(" | Details: %v", bfErr.Error.Error)
			}
			t.Fatalf("ChatCompletion failed: %s", errorMsg)
		}
		t.Fatalf("ChatCompletion failed: %v", bfErr)
	}

	if resp == nil {
		t.Fatal("Expected non-nil response")
	}
	if len(resp.Choices) == 0 {
		t.Fatal("Expected at least one choice")
	}
	if resp.Choices[0].Message.Content == nil {
		t.Fatal("Expected message content")
	}
	t.Logf("Response: %v", resp.Choices[0].Message.Content)
}

func TestVertexChatCompletionWithSingleTool(t *testing.T) {
	credentials := os.Getenv("VERTEX_CREDENTIALS")
	projectID := os.Getenv("VERTEX_PROJECT_ID")
	if credentials == "" || projectID == "" {
		t.Fatal("VERTEX_CREDENTIALS or VERTEX_PROJECT_ID not set")
	}

	provider, err := NewVertexProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			DefaultRequestTimeoutInSeconds: 60,
		},
		ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
			Concurrency: 10,
		},
	}, newTestLogger())
	if err != nil {
		t.Fatalf("Failed to create Vertex provider: %v", err)
	}

	ctx := context.Background()
	key := schemas.Key{
		VertexKeyConfig: &schemas.VertexKeyConfig{
			ProjectID:       projectID,
			Region:          "us-central1",
			AuthCredentials: credentials,
		},
	}

	props := map[string]interface{}{
		"location": map[string]interface{}{
			"type":        "string",
			"description": "The city name",
		},
	}

	request := &schemas.BifrostChatRequest{
		Provider: schemas.Vertex,
		Model:    "claude-3-5-haiku",
		Input: []schemas.ChatMessage{
			{
				Role:    "user",
				Content: &schemas.ChatMessageContent{ContentStr: stringPtr("What's the weather in San Francisco?")},
			},
		},
		Params: &schemas.ChatParameters{
			Temperature:         float64Ptr(0.7),
			MaxCompletionTokens: intPtr(100),
			Tools: []schemas.ChatTool{
				{
					Type: schemas.ChatToolTypeFunction,
					Function: &schemas.ChatToolFunction{
						Name:        "get_weather",
						Description: stringPtr("Get the current weather"),
						Parameters: &schemas.ToolFunctionParameters{
							Type:       "object",
							Properties: &props,
							Required:   []string{"location"},
						},
					},
				},
			},
		},
	}

	resp, bfErr := provider.ChatCompletion(ctx, key, request)
	if bfErr != nil {
		t.Fatalf("ChatCompletion with single tool failed: %v", bfErr)
	}

	if resp == nil {
		t.Fatal("Expected non-nil response")
	}
	if len(resp.Choices) == 0 {
		t.Fatal("Expected at least one choice")
	}
	t.Logf("Tool calls: %d", len(resp.Choices[0].Message.ToolCalls))
}

func TestVertexChatCompletionWithMultipleTools(t *testing.T) {
	credentials := os.Getenv("VERTEX_CREDENTIALS")
	projectID := os.Getenv("VERTEX_PROJECT_ID")
	if credentials == "" || projectID == "" {
		t.Fatal("VERTEX_CREDENTIALS or VERTEX_PROJECT_ID not set")
	}

	provider, err := NewVertexProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			DefaultRequestTimeoutInSeconds: 60,
		},
		ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
			Concurrency: 10,
		},
	}, newTestLogger())
	if err != nil {
		t.Fatalf("Failed to create Vertex provider: %v", err)
	}

	ctx := context.Background()
	key := schemas.Key{
		VertexKeyConfig: &schemas.VertexKeyConfig{
			ProjectID:       projectID,
			Region:          "us-central1",
			AuthCredentials: credentials,
		},
	}

	weatherProps := map[string]interface{}{
		"location": map[string]interface{}{
			"type":        "string",
			"description": "The city name",
		},
	}

	timeProps := map[string]interface{}{
		"timezone": map[string]interface{}{
			"type":        "string",
			"description": "The timezone identifier",
		},
	}

	request := &schemas.BifrostChatRequest{
		Provider: schemas.Vertex,
		Model:    "claude-3-7-sonnet@20240229",
		Input: []schemas.ChatMessage{
			{
				Role:    "user",
				Content: &schemas.ChatMessageContent{ContentStr: stringPtr("What's the weather and current time in New York?")},
			},
		},
		Params: &schemas.ChatParameters{
			Temperature:         float64Ptr(0.7),
			MaxCompletionTokens: intPtr(200),
			Tools: []schemas.ChatTool{
				{
					Type: schemas.ChatToolTypeFunction,
					Function: &schemas.ChatToolFunction{
						Name:        "get_weather",
						Description: stringPtr("Get the current weather"),
						Parameters: &schemas.ToolFunctionParameters{
							Type:       "object",
							Properties: &weatherProps,
							Required:   []string{"location"},
						},
					},
				},
				{
					Type: schemas.ChatToolTypeFunction,
					Function: &schemas.ChatToolFunction{
						Name:        "get_time",
						Description: stringPtr("Get the current time"),
						Parameters: &schemas.ToolFunctionParameters{
							Type:       "object",
							Properties: &timeProps,
							Required:   []string{"timezone"},
						},
					},
				},
			},
		},
	}

	resp, bfErr := provider.ChatCompletion(ctx, key, request)
	if bfErr != nil {
		t.Fatalf("ChatCompletion with multiple tools failed: %v", bfErr)
	}

	if resp == nil {
		t.Fatal("Expected non-nil response")
	}
	if len(resp.Choices) == 0 {
		t.Fatal("Expected at least one choice")
	}
	t.Logf("Tool calls: %d", len(resp.Choices[0].Message.ToolCalls))
}

func TestVertexChatCompletionWithParallelToolCalls(t *testing.T) {
	credentials := os.Getenv("VERTEX_CREDENTIALS")
	projectID := os.Getenv("VERTEX_PROJECT_ID")
	if credentials == "" || projectID == "" {
		t.Fatal("VERTEX_CREDENTIALS or VERTEX_PROJECT_ID not set")
	}

	provider, err := NewVertexProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			DefaultRequestTimeoutInSeconds: 60,
		},
		ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
			Concurrency: 10,
		},
	}, newTestLogger())
	if err != nil {
		t.Fatalf("Failed to create Vertex provider: %v", err)
	}

	ctx := context.Background()
	key := schemas.Key{
		VertexKeyConfig: &schemas.VertexKeyConfig{
			ProjectID:       projectID,
			Region:          "us-central1",
			AuthCredentials: credentials,
		},
	}

	weatherProps := map[string]interface{}{
		"location": map[string]interface{}{
			"type":        "string",
			"description": "The city name",
		},
	}

	request := &schemas.BifrostChatRequest{
		Provider: schemas.Vertex,
		Model:    "claude-3-5-haiku",
		Input: []schemas.ChatMessage{
			{
				Role:    "user",
				Content: &schemas.ChatMessageContent{ContentStr: stringPtr("What's the weather in San Francisco, New York, and London?")},
			},
		},
		Params: &schemas.ChatParameters{
			Temperature:         float64Ptr(0.7),
			MaxCompletionTokens: intPtr(200),
			ParallelToolCalls:   boolPtr(true),
			Tools: []schemas.ChatTool{
				{
					Type: schemas.ChatToolTypeFunction,
					Function: &schemas.ChatToolFunction{
						Name:        "get_weather",
						Description: stringPtr("Get the current weather"),
						Parameters: &schemas.ToolFunctionParameters{
							Type:       "object",
							Properties: &weatherProps,
							Required:   []string{"location"},
						},
					},
				},
			},
		},
	}

	resp, bfErr := provider.ChatCompletion(ctx, key, request)
	if bfErr != nil {
		t.Fatalf("ChatCompletion with parallel tool calls failed: %v", bfErr)
	}

	if resp == nil {
		t.Fatal("Expected non-nil response")
	}
	if len(resp.Choices) == 0 {
		t.Fatal("Expected at least one choice")
	}
	t.Logf("Parallel tool calls: %d", len(resp.Choices[0].Message.ToolCalls))
}

func TestVertexChatCompletionStream(t *testing.T) {
	credentials := os.Getenv("VERTEX_CREDENTIALS")
	projectID := os.Getenv("VERTEX_PROJECT_ID")
	if credentials == "" || projectID == "" {
		t.Fatal("VERTEX_CREDENTIALS or VERTEX_PROJECT_ID not set")
	}

	provider, err := NewVertexProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			DefaultRequestTimeoutInSeconds: 60,
		},
		ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
			Concurrency: 10,
		},
	}, newTestLogger())
	if err != nil {
		t.Fatalf("Failed to create Vertex provider: %v", err)
	}

	ctx := context.Background()
	key := schemas.Key{
		VertexKeyConfig: &schemas.VertexKeyConfig{
			ProjectID:       projectID,
			Region:          "us-central1",
			AuthCredentials: credentials,
		},
	}

	request := &schemas.BifrostChatRequest{
		Provider: schemas.Vertex,
		Model:    "claude-3-5-haiku",
		Input: []schemas.ChatMessage{
			{
				Role:    "user",
				Content: &schemas.ChatMessageContent{ContentStr: stringPtr("Count from 1 to 5")},
			},
		},
		Params: &schemas.ChatParameters{
			Temperature: float64Ptr(0.7),
		},
	}

	streamChan, bfErr := provider.ChatCompletionStream(ctx, mockPostHookRunner, key, request)
	if bfErr != nil {
		t.Fatalf("ChatCompletionStream failed: %v", bfErr)
	}

	count := 0
	for chunk := range streamChan {
		if chunk == nil {
			continue
		}
		if chunk.BifrostError != nil {
			t.Fatalf("Stream error: %v", chunk.BifrostError)
		}
		count++
	}

	if count == 0 {
		t.Fatal("Expected at least one chunk")
	}
	t.Logf("Received %d chunks", count)
}

func TestVertexEmbedding(t *testing.T) {
	credentials := os.Getenv("VERTEX_CREDENTIALS")
	projectID := os.Getenv("VERTEX_PROJECT_ID")
	if credentials == "" || projectID == "" {
		t.Fatal("VERTEX_CREDENTIALS or VERTEX_PROJECT_ID not set")
	}

	provider, err := NewVertexProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			DefaultRequestTimeoutInSeconds: 60,
		},
		ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
			Concurrency: 10,
		},
	}, newTestLogger())
	if err != nil {
		t.Fatalf("Failed to create Vertex provider: %v", err)
	}

	ctx := context.Background()
	key := schemas.Key{
		VertexKeyConfig: &schemas.VertexKeyConfig{
			ProjectID:       projectID,
			Region:          "us-central1",
			AuthCredentials: credentials,
		},
	}

	request := &schemas.BifrostEmbeddingRequest{
		Provider: schemas.Vertex,
		Model:    "text-embedding-004",
		Input:    &schemas.EmbeddingInput{Texts: []string{"Hello world"}},
	}

	resp, bfErr := provider.Embedding(ctx, key, request)
	if bfErr != nil {
		t.Fatalf("Embedding failed: %v", bfErr)
	}

	if resp == nil {
		t.Fatal("Expected non-nil response")
	}
	if len(resp.Data) == 0 {
		t.Fatal("Expected at least one embedding")
	}
	if len(resp.Data[0].Embedding.EmbeddingArray) == 0 {
		t.Fatal("Expected non-empty embedding vector")
	}
	t.Logf("Embedding dimension: %d", len(resp.Data[0].Embedding.EmbeddingArray))
}

func TestVertexGetProviderKey(t *testing.T) {
	provider, err := NewVertexProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			DefaultRequestTimeoutInSeconds: 60,
		},
		ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
			Concurrency: 10,
		},
	}, newTestLogger())
	if err != nil {
		t.Fatalf("Failed to create Vertex provider: %v", err)
	}

	key := provider.GetProviderKey()
	if key != schemas.Vertex {
		t.Errorf("Expected provider key %s, got %s", schemas.Vertex, key)
	}
}

// Helper function for bool pointers
func boolPtr(b bool) *bool {
	return &b
}
