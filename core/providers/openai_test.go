package providers

import (
	"context"
	"os"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestOpenAIChatCompletion(t *testing.T) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		t.Skip("OPENAI_API_KEY not set")
	}

	provider := NewOpenAIProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:                        "https://api.openai.com",
			DefaultRequestTimeoutInSeconds: 30,
		},
		ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
			Concurrency: 10,
		},
	}, newTestLogger())

	ctx := context.Background()
	key := schemas.Key{Value: apiKey}

	request := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-3.5-turbo",
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

	resp, err := provider.ChatCompletion(ctx, key, request)
	if err != nil {
		t.Fatalf("ChatCompletion failed: %v", err)
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

func TestOpenAIChatCompletionWithTools(t *testing.T) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		t.Skip("OPENAI_API_KEY not set")
	}

	provider := NewOpenAIProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:                        "https://api.openai.com",
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
		Provider: schemas.OpenAI,
		Model:    "gpt-3.5-turbo",
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

	resp, err := provider.ChatCompletion(ctx, key, request)
	if err != nil {
		t.Fatalf("ChatCompletion with tools failed: %v", err)
	}

	if resp == nil {
		t.Fatal("Expected non-nil response")
	}
	if len(resp.Choices) == 0 {
		t.Fatal("Expected at least one choice")
	}
	t.Logf("Tool calls: %d", len(resp.Choices[0].Message.ToolCalls))
}

func TestOpenAIChatCompletionStream(t *testing.T) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		t.Skip("OPENAI_API_KEY not set")
	}

	provider := NewOpenAIProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:                        "https://api.openai.com",
			DefaultRequestTimeoutInSeconds: 30,
		},
		ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
			Concurrency: 10,
		},
	}, newTestLogger())

	ctx := context.Background()
	key := schemas.Key{Value: apiKey}

	request := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-3.5-turbo",
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

	streamChan, err := provider.ChatCompletionStream(ctx, mockPostHookRunner, key, request)
	if err != nil {
		t.Fatalf("ChatCompletionStream failed: %v", err)
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

func TestOpenAITextCompletion(t *testing.T) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		t.Skip("OPENAI_API_KEY not set")
	}

	provider := NewOpenAIProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:                        "https://api.openai.com",
			DefaultRequestTimeoutInSeconds: 30,
		},
		ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
			Concurrency: 10,
		},
	}, newTestLogger())

	ctx := context.Background()
	key := schemas.Key{Value: apiKey}

	request := &schemas.BifrostTextCompletionRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-3.5-turbo-instruct",
		Input:    &schemas.TextCompletionInput{PromptStr: stringPtr("Say hello")},
		Params: &schemas.TextCompletionParameters{
			Temperature: float64Ptr(0.7),
			MaxTokens:   intPtr(10),
		},
	}

	resp, err := provider.TextCompletion(ctx, key, request)
	if err != nil {
		t.Fatalf("TextCompletion failed: %v", err)
	}

	if resp == nil {
		t.Fatal("Expected non-nil response")
	}
	if len(resp.Choices) == 0 {
		t.Fatal("Expected at least one choice")
	}
	t.Logf("Response: %v", resp.Choices[0].Text)
}

func TestOpenAIEmbedding(t *testing.T) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		t.Skip("OPENAI_API_KEY not set")
	}

	provider := NewOpenAIProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:                        "https://api.openai.com",
			DefaultRequestTimeoutInSeconds: 30,
		},
		ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
			Concurrency: 10,
		},
	}, newTestLogger())

	ctx := context.Background()
	key := schemas.Key{Value: apiKey}

	request := &schemas.BifrostEmbeddingRequest{
		Provider: schemas.OpenAI,
		Model:    "text-embedding-ada-002",
		Input:    &schemas.EmbeddingInput{Texts: []string{"Hello world"}},
	}

	resp, err := provider.Embedding(ctx, key, request)
	if err != nil {
		t.Fatalf("Embedding failed: %v", err)
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

func TestOpenAIGetProviderKey(t *testing.T) {
	provider := NewOpenAIProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL: "https://api.openai.com",
		},
		ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
			Concurrency: 10,
		},
	}, newTestLogger())

	key := provider.GetProviderKey()
	if key != schemas.OpenAI {
		t.Errorf("Expected provider key %s, got %s", schemas.OpenAI, key)
	}
}
