package providers

import (
	"context"
	"os"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestAzureChatCompletion(t *testing.T) {
	apiKey := os.Getenv("AZURE_API_KEY")
	endpoint := os.Getenv("AZURE_ENDPOINT")
	if apiKey == "" || endpoint == "" {
		t.Skip("AZURE_API_KEY or AZURE_ENDPOINT not set")
	}

	provider, err := NewAzureProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			DefaultRequestTimeoutInSeconds: 60,
		},
		ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
			Concurrency: 10,
		},
	}, newTestLogger())
	if err != nil {
		t.Fatalf("Failed to create Azure provider: %v", err)
	}

	ctx := context.Background()
	key := schemas.Key{
		Value: apiKey,
		AzureKeyConfig: &schemas.AzureKeyConfig{
			Endpoint: endpoint,
			Deployments: map[string]string{
				"gpt-4o-mini": "gpt-4o-mini",
			},
		},
	}

	request := &schemas.BifrostChatRequest{
		Provider: schemas.Azure,
		Model:    "gpt-4o-mini",
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

func TestAzureChatCompletionWithSingleTool(t *testing.T) {
	apiKey := os.Getenv("AZURE_API_KEY")
	endpoint := os.Getenv("AZURE_ENDPOINT")
	if apiKey == "" || endpoint == "" {
		t.Skip("AZURE_API_KEY or AZURE_ENDPOINT not set")
	}

	provider, err := NewAzureProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			DefaultRequestTimeoutInSeconds: 60,
		},
		ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
			Concurrency: 10,
		},
	}, newTestLogger())
	if err != nil {
		t.Fatalf("Failed to create Azure provider: %v", err)
	}

	ctx := context.Background()
	key := schemas.Key{
		Value: apiKey,
		AzureKeyConfig: &schemas.AzureKeyConfig{
			Endpoint: endpoint,
			Deployments: map[string]string{
				"gpt-4o-mini": "gpt-4o-mini",
			},
		},
	}

	props := map[string]interface{}{
		"location": map[string]interface{}{
			"type":        "string",
			"description": "The city name",
		},
	}

	request := &schemas.BifrostChatRequest{
		Provider: schemas.Azure,
		Model:    "gpt-4o-mini",
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

func TestAzureChatCompletionWithMultipleTools(t *testing.T) {
	apiKey := os.Getenv("AZURE_API_KEY")
	endpoint := os.Getenv("AZURE_ENDPOINT")
	if apiKey == "" || endpoint == "" {
		t.Skip("AZURE_API_KEY or AZURE_ENDPOINT not set")
	}

	provider, err := NewAzureProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			DefaultRequestTimeoutInSeconds: 60,
		},
		ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
			Concurrency: 10,
		},
	}, newTestLogger())
	if err != nil {
		t.Fatalf("Failed to create Azure provider: %v", err)
	}

	ctx := context.Background()
	key := schemas.Key{
		Value: apiKey,
		AzureKeyConfig: &schemas.AzureKeyConfig{
			Endpoint: endpoint,
			Deployments: map[string]string{
				"gpt-4o-mini": "gpt-4o-mini",
			},
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
		Provider: schemas.Azure,
		Model:    "gpt-4o-mini",
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

func TestAzureChatCompletionWithParallelToolCalls(t *testing.T) {
	apiKey := os.Getenv("AZURE_API_KEY")
	endpoint := os.Getenv("AZURE_ENDPOINT")
	if apiKey == "" || endpoint == "" {
		t.Skip("AZURE_API_KEY or AZURE_ENDPOINT not set")
	}

	provider, err := NewAzureProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			DefaultRequestTimeoutInSeconds: 60,
		},
		ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
			Concurrency: 10,
		},
	}, newTestLogger())
	if err != nil {
		t.Fatalf("Failed to create Azure provider: %v", err)
	}

	ctx := context.Background()
	key := schemas.Key{
		Value: apiKey,
		AzureKeyConfig: &schemas.AzureKeyConfig{
			Endpoint: endpoint,
			Deployments: map[string]string{
				"gpt-4o-mini": "gpt-4o-mini",
			},
		},
	}

	weatherProps := map[string]interface{}{
		"location": map[string]interface{}{
			"type":        "string",
			"description": "The city name",
		},
	}

	request := &schemas.BifrostChatRequest{
		Provider: schemas.Azure,
		Model:    "gpt-4o-mini",
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

func TestAzureChatCompletionStream(t *testing.T) {
	apiKey := os.Getenv("AZURE_API_KEY")
	endpoint := os.Getenv("AZURE_ENDPOINT")
	if apiKey == "" || endpoint == "" {
		t.Skip("AZURE_API_KEY or AZURE_ENDPOINT not set")
	}

	provider, err := NewAzureProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			DefaultRequestTimeoutInSeconds: 60,
		},
		ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
			Concurrency: 10,
		},
	}, newTestLogger())
	if err != nil {
		t.Fatalf("Failed to create Azure provider: %v", err)
	}

	ctx := context.Background()
	key := schemas.Key{
		Value: apiKey,
		AzureKeyConfig: &schemas.AzureKeyConfig{
			Endpoint: endpoint,
			Deployments: map[string]string{
				"gpt-4o-mini": "gpt-4o-mini",
			},
		},
	}

	request := &schemas.BifrostChatRequest{
		Provider: schemas.Azure,
		Model:    "gpt-4o-mini",
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

func TestAzureEmbedding(t *testing.T) {
	apiKey := os.Getenv("AZURE_EMB_API_KEY")
	endpoint := os.Getenv("AZURE_EMB_ENDPOINT")
	if apiKey == "" || endpoint == "" {
		t.Skip("AZURE_EMB_API_KEY or AZURE_EMB_ENDPOINT not set")
	}

	provider, err := NewAzureProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			DefaultRequestTimeoutInSeconds: 60,
		},
		ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
			Concurrency: 10,
		},
	}, newTestLogger())
	if err != nil {
		t.Fatalf("Failed to create Azure provider: %v", err)
	}

	ctx := context.Background()
	key := schemas.Key{
		Value: apiKey,
		AzureKeyConfig: &schemas.AzureKeyConfig{
			Endpoint: endpoint,
			Deployments: map[string]string{
				"text-embedding-ada-002": "text-embedding-ada-002",
			},
		},
	}

	request := &schemas.BifrostEmbeddingRequest{
		Provider: schemas.Azure,
		Model:    "text-embedding-ada-002",
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

func TestAzureGetProviderKey(t *testing.T) {
	provider, err := NewAzureProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			DefaultRequestTimeoutInSeconds: 60,
		},
		ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
			Concurrency: 10,
		},
	}, newTestLogger())
	if err != nil {
		t.Fatalf("Failed to create Azure provider: %v", err)
	}

	key := provider.GetProviderKey()
	if key != schemas.Azure {
		t.Errorf("Expected provider key %s, got %s", schemas.Azure, key)
	}
}
