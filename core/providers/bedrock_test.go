package providers

import (
	"context"
	"os"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestBedrockChatCompletion(t *testing.T) {
	apiKey := os.Getenv("BEDROCK_API_KEY")
	if apiKey == "" {
		t.Fatal("BEDROCK_API_KEY not set")
	}

	provider, err := NewBedrockProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			DefaultRequestTimeoutInSeconds: 60,
		},
		ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
			Concurrency: 10,
		},
	}, newTestLogger())
	if err != nil {
		t.Fatalf("Failed to create Bedrock provider: %v", err)
	}

	ctx := context.Background()
	region := "us-east-1"
	key := schemas.Key{
		Value: apiKey,
		BedrockKeyConfig: &schemas.BedrockKeyConfig{
			Region: &region,
		},
	}

	request := &schemas.BifrostChatRequest{
		Provider: schemas.Bedrock,
		Model:    "anthropic.claude-3-haiku-20240307-v1:0",
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

func TestBedrockChatCompletionWithSingleTool(t *testing.T) {
	apiKey := os.Getenv("BEDROCK_API_KEY")
	if apiKey == "" {
		t.Fatal("BEDROCK_API_KEY not set")
	}

	provider, err := NewBedrockProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			DefaultRequestTimeoutInSeconds: 60,
		},
		ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
			Concurrency: 10,
		},
	}, newTestLogger())
	if err != nil {
		t.Fatalf("Failed to create Bedrock provider: %v", err)
	}

	ctx := context.Background()
	region := "us-east-1"
	key := schemas.Key{
		Value: apiKey,
		BedrockKeyConfig: &schemas.BedrockKeyConfig{
			Region: &region,
		},
	}

	props := map[string]interface{}{
		"location": map[string]interface{}{
			"type":        "string",
			"description": "The city name",
		},
	}

	request := &schemas.BifrostChatRequest{
		Provider: schemas.Bedrock,
		Model:    "anthropic.claude-3-haiku-20240307-v1:0",
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

func TestBedrockChatCompletionWithMultipleTools(t *testing.T) {
	apiKey := os.Getenv("BEDROCK_API_KEY")
	if apiKey == "" {
		t.Fatal("BEDROCK_API_KEY not set")
	}

	provider, err := NewBedrockProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			DefaultRequestTimeoutInSeconds: 60,
		},
		ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
			Concurrency: 10,
		},
	}, newTestLogger())
	if err != nil {
		t.Fatalf("Failed to create Bedrock provider: %v", err)
	}

	ctx := context.Background()
	region := "us-east-1"
	key := schemas.Key{
		Value: apiKey,
		BedrockKeyConfig: &schemas.BedrockKeyConfig{
			Region: &region,
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
		Provider: schemas.Bedrock,
		Model:    "anthropic.claude-3-haiku-20240307-v1:0",
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

func TestBedrockChatCompletionStream(t *testing.T) {
	apiKey := os.Getenv("BEDROCK_API_KEY")
	if apiKey == "" {
		t.Fatal("BEDROCK_API_KEY not set")
	}

	provider, err := NewBedrockProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			DefaultRequestTimeoutInSeconds: 60,
		},
		ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
			Concurrency: 10,
		},
	}, newTestLogger())
	if err != nil {
		t.Fatalf("Failed to create Bedrock provider: %v", err)
	}

	ctx := context.Background()
	region := "us-east-1"
	key := schemas.Key{
		Value: apiKey,
		BedrockKeyConfig: &schemas.BedrockKeyConfig{
			Region: &region,
		},
	}

	request := &schemas.BifrostChatRequest{
		Provider: schemas.Bedrock,
		Model:    "anthropic.claude-3-haiku-20240307-v1:0",
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

func TestBedrockResponses(t *testing.T) {
	apiKey := os.Getenv("BEDROCK_API_KEY")
	if apiKey == "" {
		t.Fatal("BEDROCK_API_KEY not set")
	}

	provider, err := NewBedrockProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			DefaultRequestTimeoutInSeconds: 60,
		},
		ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
			Concurrency: 10,
		},
	}, newTestLogger())
	if err != nil {
		t.Fatalf("Failed to create Bedrock provider: %v", err)
	}

	ctx := context.Background()
	region := "us-east-1"
	key := schemas.Key{
		Value: apiKey,
		BedrockKeyConfig: &schemas.BedrockKeyConfig{
			Region: &region,
		},
	}

	userRole := schemas.ResponsesInputMessageRoleUser
	request := &schemas.BifrostResponsesRequest{
		Provider: schemas.Bedrock,
		Model:    "anthropic.claude-3-haiku-20240307-v1:0",
		Input: []schemas.ResponsesMessage{
			{
				Role:    &userRole,
				Content: &schemas.ResponsesMessageContent{ContentStr: stringPtr("What is 2+2?")},
			},
		},
		Params: &schemas.ResponsesParameters{
			MaxOutputTokens: intPtr(100),
		},
	}

	resp, bfErr := provider.Responses(ctx, key, request)
	if bfErr != nil {
		if bfErr.Error != nil {
			if bfErr.Error.Error != nil {
				t.Fatalf("Responses failed - error: %v", bfErr.Error.Error)
			}
			t.Fatalf("Responses failed - message: %s", bfErr.Error.Message)
		}
		t.Fatalf("Responses failed: %v", bfErr)
	}

	if resp == nil {
		t.Fatal("Expected non-nil response")
	}
	if len(resp.Output) == 0 {
		t.Fatal("Expected at least one output message")
	}
	t.Logf("Response output messages: %d", len(resp.Output))
}

func TestBedrockGetProviderKey(t *testing.T) {
	provider, err := NewBedrockProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			DefaultRequestTimeoutInSeconds: 60,
		},
		ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
			Concurrency: 10,
		},
	}, newTestLogger())
	if err != nil {
		t.Fatalf("Failed to create Bedrock provider: %v", err)
	}

	key := provider.GetProviderKey()
	if key != schemas.Bedrock {
		t.Errorf("Expected provider key %s, got %s", schemas.Bedrock, key)
	}
}
