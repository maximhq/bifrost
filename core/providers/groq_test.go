package providers

import (
	"context"
	"os"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestGroqChatCompletion(t *testing.T) {
	apiKey := os.Getenv("GROQ_API_KEY")
	if apiKey == "" {
		t.Skip("GROQ_API_KEY not set")
	}

	provider, err := NewGroqProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:                        "https://api.groq.com/openai",
			DefaultRequestTimeoutInSeconds: 30,
		},
		ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
			Concurrency: 10,
		},
	}, newTestLogger())
	if err != nil {
		t.Fatalf("Failed to create Groq provider: %v", err)
	}

	ctx := context.Background()
	key := schemas.Key{Value: apiKey}

	request := &schemas.BifrostChatRequest{
		Provider: schemas.Groq,
		Model:    "llama-3.3-70b-versatile",
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

func TestGroqChatCompletionWithTools(t *testing.T) {
	apiKey := os.Getenv("GROQ_API_KEY")
	if apiKey == "" {
		t.Skip("GROQ_API_KEY not set")
	}

	provider, err := NewGroqProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:                        "https://api.groq.com/openai",
			DefaultRequestTimeoutInSeconds: 30,
		},
		ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
			Concurrency: 10,
		},
	}, newTestLogger())
	if err != nil {
		t.Fatalf("Failed to create Groq provider: %v", err)
	}

	ctx := context.Background()
	key := schemas.Key{Value: apiKey}

	props := map[string]interface{}{
		"location": map[string]interface{}{
			"type":        "string",
			"description": "The city name",
		},
	}

	request := &schemas.BifrostChatRequest{
		Provider: schemas.Groq,
		Model:    "llama-3.3-70b-versatile",
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

func TestGroqChatCompletionStream(t *testing.T) {
	apiKey := os.Getenv("GROQ_API_KEY")
	if apiKey == "" {
		t.Skip("GROQ_API_KEY not set")
	}

	provider, err := NewGroqProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:                        "https://api.groq.com/openai",
			DefaultRequestTimeoutInSeconds: 30,
		},
		ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
			Concurrency: 10,
		},
	}, newTestLogger())
	if err != nil {
		t.Fatalf("Failed to create Groq provider: %v", err)
	}

	ctx := context.Background()
	key := schemas.Key{Value: apiKey}

	request := &schemas.BifrostChatRequest{
		Provider: schemas.Groq,
		Model:    "llama-3.3-70b-versatile",
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

func TestGroqTranscription(t *testing.T) {
	apiKey := os.Getenv("GROQ_API_KEY")
	if apiKey == "" {
		t.Skip("GROQ_API_KEY not set")
	}

	provider, err := NewGroqProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:                        "https://api.groq.com/openai",
			DefaultRequestTimeoutInSeconds: 30,
		},
		ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
			Concurrency: 10,
		},
	}, newTestLogger())
	if err != nil {
		t.Fatalf("Failed to create Groq provider: %v", err)
	}

	ctx := context.Background()
	key := schemas.Key{Value: apiKey}

	// Note: This test requires an actual audio file. Skipping for now.
	// In a real test, you would provide a base64-encoded audio file.
	t.Skip("Transcription test requires audio file - implement when needed")

	request := &schemas.BifrostTranscriptionRequest{
		Provider: schemas.Groq,
		Model:    "whisper-large-v3",
		Input: &schemas.TranscriptionInput{
			// Would need actual audio data here
		},
	}

	resp, bfErr := provider.Transcription(ctx, key, request)
	if bfErr != nil {
		t.Fatalf("Transcription failed: %v", bfErr)
	}

	if resp == nil {
		t.Fatal("Expected non-nil response")
	}
	t.Logf("Transcription: %v", resp.Text)
}

func TestGroqGetProviderKey(t *testing.T) {
	provider, err := NewGroqProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL: "https://api.groq.com/openai",
		},
		ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
			Concurrency: 10,
		},
	}, newTestLogger())
	if err != nil {
		t.Fatalf("Failed to create Groq provider: %v", err)
	}

	key := provider.GetProviderKey()
	if key != schemas.Groq {
		t.Errorf("Expected provider key %s, got %s", schemas.Groq, key)
	}
}
