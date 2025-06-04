package tests

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/joho/godotenv"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestToolCallingEndToEnd(t *testing.T) {
	// Load environment variables
	if err := godotenv.Load(); err != nil && !os.IsNotExist(err) {
		t.Fatalf("Error loading .env: %v", err)
	}

	// Initialize Bifrost client
	client, err := getBifrost()
	require.NoError(t, err)
	require.NotNil(t, client)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	provider := schemas.Bedrock
	model := "anthropic.claude-3-sonnet-20240229-v1:0"

	// Step 1: User asks for weather, LLM should request tool usage
	userMessage := schemas.BifrostMessage{
		Role:    schemas.ModelChatMessageRoleUser,
		Content: bifrost.Ptr("What's the weather in London?"),
	}

	toolParams := WeatherToolParams
	toolParams.ToolChoice = &schemas.ToolChoice{
		Type: schemas.ToolChoiceTypeFunction,
		Function: schemas.ToolChoiceFunction{
			Name: "get_weather",
		},
	}
	toolParams.MaxTokens = bifrost.Ptr(1000)

	firstRequest := &schemas.BifrostRequest{
		Provider: provider,
		Model:    model,
		Input: schemas.RequestInput{
			ChatCompletionInput: &[]schemas.BifrostMessage{userMessage},
		},
		Params: &toolParams,
	}

	// Execute first request
	firstResponse, bifrostErr := client.ChatCompletionRequest(ctx, firstRequest)
	require.Nilf(t, bifrostErr, "First request failed: %v", bifrostErr)
	require.NotNil(t, firstResponse)
	require.NotEmpty(t, firstResponse.Choices)

	// Verify tool call was requested
	message := firstResponse.Choices[0].Message
	require.NotNil(t, message.AssistantMessage)
	require.NotNil(t, message.AssistantMessage.ToolCalls)
	require.Len(t, *message.AssistantMessage.ToolCalls, 1)

	toolCall := (*message.AssistantMessage.ToolCalls)[0]
	// Only assert on Type if it's populated by the provider
	if toolCall.Type != nil {
		assert.Equal(t, "function", *toolCall.Type)
	}
	// Only assert on Function.Name if it's not nil to prevent panic
	require.NotNil(t, toolCall.Function.Name, "toolCall.Function.Name should not be nil")
	assert.Equal(t, "get_weather", *toolCall.Function.Name)

	// Verify tool arguments contain location
	var params map[string]interface{}
	err = json.Unmarshal([]byte(toolCall.Function.Arguments), &params)
	require.NoError(t, err)
	assert.Contains(t, params, "location")

	// Step 2: Simulate tool execution and provide result to LLM
	toolResult := `{"temperature": "15", "unit": "celsius", "description": "Partly cloudy"}`

	conversationMessages := []schemas.BifrostMessage{
		userMessage,
		message,
		{
			Role:    schemas.ModelChatMessageRoleTool,
			Content: &toolResult,
			ToolMessage: &schemas.ToolMessage{
				ToolCallID: toolCall.ID,
			},
		},
	}

	secondRequest := &schemas.BifrostRequest{
		Provider: provider,
		Model:    model,
		Input: schemas.RequestInput{
			ChatCompletionInput: &conversationMessages,
		},
		Params: &schemas.ModelParameters{
			MaxTokens: bifrost.Ptr(1000),
		},
	}

	// Execute second request
	finalResponse, bifrostErr := client.ChatCompletionRequest(ctx, secondRequest)
	require.Nilf(t, bifrostErr, "Second request failed: %v", bifrostErr)
	require.NotNil(t, finalResponse)
	require.NotEmpty(t, finalResponse.Choices)

	// Verify final response
	finalMessage := finalResponse.Choices[0].Message
	require.NotNil(t, finalMessage.Content)

	content := *finalMessage.Content
	assert.Contains(t, content, "London", "Response should mention London")
	assert.Contains(t, content, "15", "Response should mention temperature")
	assert.Contains(t, content, "cloudy", "Response should mention weather description")

	t.Logf("Final response: %s", content)
}
