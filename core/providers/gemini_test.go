package providers

import (
	"context"
	"os"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestGeminiChatCompletion(t *testing.T) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		t.Fatal("GEMINI_API_KEY not set")
	}

	provider := NewGeminiProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			DefaultRequestTimeoutInSeconds: 30,
		},
		ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
			Concurrency: 10,
		},
	}, newTestLogger())

	ctx := context.Background()
	key := schemas.Key{Value: apiKey}

	request := &schemas.BifrostChatRequest{
		Provider: schemas.Gemini,
		Model:    "gemini-pro",
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
			if bfErr.Error.Error != nil {
				t.Fatalf("ChatCompletion failed - error: %v", bfErr.Error.Error)
			}
			t.Fatalf("ChatCompletion failed - message: %s", bfErr.Error.Message)
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

func TestGeminiChatCompletionWithTools(t *testing.T) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		t.Fatal("GEMINI_API_KEY not set")
	}

	provider := NewGeminiProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			DefaultRequestTimeoutInSeconds: 30,
		},
		ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
			Concurrency: 10,
		},
	}, newTestLogger())

	ctx := context.Background()
	key := schemas.Key{Value: apiKey}

	props := map[string]interface{}{
		"location": map[string]interface{}{
			"type":        "string",
			"description": "The city name",
		},
	}

	request := &schemas.BifrostChatRequest{
		Provider: schemas.Gemini,
		Model:    "gemini-pro",
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
		t.Fatalf("ChatCompletion with tools failed: %v", bfErr)
	}

	if resp == nil {
		t.Fatal("Expected non-nil response")
	}
	if len(resp.Choices) == 0 {
		t.Fatal("Expected at least one choice")
	}
	t.Logf("Tool calls: %d", len(resp.Choices[0].Message.ToolCalls))
}

func TestGeminiChatCompletionStream(t *testing.T) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		t.Fatal("GEMINI_API_KEY not set")
	}

	provider := NewGeminiProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			DefaultRequestTimeoutInSeconds: 30,
		},
		ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
			Concurrency: 10,
		},
	}, newTestLogger())

	ctx := context.Background()
	key := schemas.Key{Value: apiKey}

	request := &schemas.BifrostChatRequest{
		Provider: schemas.Gemini,
		Model:    "gemini-pro",
		Input: []schemas.ChatMessage{
			{
				Role:    "user",
				Content: &schemas.ChatMessageContent{ContentStr: stringPtr("Count from 1 to 3")},
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

func TestGeminiEmbedding(t *testing.T) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		t.Fatal("GEMINI_API_KEY not set")
	}

	provider := NewGeminiProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			DefaultRequestTimeoutInSeconds: 30,
		},
		ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
			Concurrency: 10,
		},
	}, newTestLogger())

	ctx := context.Background()
	key := schemas.Key{Value: apiKey}

	request := &schemas.BifrostEmbeddingRequest{
		Provider: schemas.Gemini,
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

func TestGeminiGetProviderKey(t *testing.T) {
	provider := NewGeminiProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL: "https://generativelanguage.googleapis.com",
		},
		ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
			Concurrency: 10,
		},
	}, newTestLogger())

	key := provider.GetProviderKey()
	if key != schemas.Gemini {
		t.Errorf("Expected provider key %s, got %s", schemas.Gemini, key)
	}
}
