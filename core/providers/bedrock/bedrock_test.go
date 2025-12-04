package bedrock_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/internal/testutil"
	"github.com/maximhq/bifrost/core/providers/bedrock"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Common test variables
var (
	testMaxTokens = 100
	testTemp      = 0.7
	testTopP      = 0.9
	testStop      = []string{"STOP"}
	testTrace     = "enabled"
	testLatency   = "optimized"
	testProps     = schemas.OrderedMap{
		"location": map[string]interface{}{
			"type":        "string",
			"description": "The city name",
		},
	}
)

func TestBedrock(t *testing.T) {
	t.Parallel()

	if strings.TrimSpace(os.Getenv("AWS_ACCESS_KEY_ID")) == "" || strings.TrimSpace(os.Getenv("AWS_SECRET_ACCESS_KEY")) == "" {
		t.Skip("Skipping Bedrock tests because AWS_ACCESS_KEY_ID or AWS_SECRET_ACCESS_KEY is not set")
	}

	client, ctx, cancel, err := testutil.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()

	testConfig := testutil.ComprehensiveTestConfig{
		Provider:    schemas.Bedrock,
		ChatModel:   "claude-4-sonnet",
		VisionModel: "claude-4-sonnet",
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.Bedrock, Model: "claude-4-sonnet"},
			{Provider: schemas.Bedrock, Model: "claude-4.5-sonnet"},
		},
		EmbeddingModel: "cohere.embed-v4:0",
		ReasoningModel: "claude-4.5-sonnet",
		Scenarios: testutil.TestScenarios{
			TextCompletion:        false, // Not supported
			SimpleChat:            true,
			CompletionStream:      true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			ToolCallsStreaming:    true,
			MultipleToolCalls:     true,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,
			ImageURL:              false, // Bedrock doesn't support image URL
			ImageBase64:           true,
			MultipleImages:        false, // Since one of the image is URL
			CompleteEnd2End:       true,
			Embedding:             true,
			ListModels:            true,
			Reasoning:             true,
		},
	}

	t.Run("BedrockTests", func(t *testing.T) {
		testutil.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
	client.Shutdown()
}

// TestBifrostToBedrockRequestConversion tests the conversion from Bifrost request to Bedrock request
func TestBifrostToBedrockRequestConversion(t *testing.T) {
	maxTokens := testMaxTokens
	temp := testTemp
	topP := testTopP
	stop := testStop
	trace := testTrace
	latency := testLatency
	serviceTier := "priority"
	props := testProps

	tests := []struct {
		name     string
		input    *schemas.BifrostChatRequest
		expected *bedrock.BedrockConverseRequest
		wantErr  bool
	}{
		{
			name: "BasicTextMessage",
			input: &schemas.BifrostChatRequest{
				Model: "claude-3-sonnet",
				Input: []schemas.ChatMessage{
					{
						Role: schemas.ChatMessageRoleUser,
						Content: &schemas.ChatMessageContent{
							ContentStr: schemas.Ptr("Hello, world!"),
						},
					},
				},
			},
			expected: &bedrock.BedrockConverseRequest{
				ModelID: "claude-3-sonnet",
				Messages: []bedrock.BedrockMessage{
					{
						Role: bedrock.BedrockMessageRoleUser,
						Content: []bedrock.BedrockContentBlock{
							{
								Text: schemas.Ptr("Hello, world!"),
							},
						},
					},
				},
			},
		},
		{
			name: "SystemMessage",
			input: &schemas.BifrostChatRequest{
				Model: "claude-3-sonnet",
				Input: []schemas.ChatMessage{
					{
						Role: schemas.ChatMessageRoleSystem,
						Content: &schemas.ChatMessageContent{
							ContentStr: schemas.Ptr("System message 1"),
						},
					},
					{
						Role: schemas.ChatMessageRoleSystem,
						Content: &schemas.ChatMessageContent{
							ContentStr: schemas.Ptr("System message 2"),
						},
					},
					{
						Role: schemas.ChatMessageRoleUser,
						Content: &schemas.ChatMessageContent{
							ContentStr: schemas.Ptr("Hello!"),
						},
					},
				},
			},
			expected: &bedrock.BedrockConverseRequest{
				ModelID: "claude-3-sonnet",
				System: []bedrock.BedrockSystemMessage{
					{
						Text: schemas.Ptr("System message 1"),
					},
					{
						Text: schemas.Ptr("System message 2"),
					},
				},
				Messages: []bedrock.BedrockMessage{
					{
						Role: bedrock.BedrockMessageRoleUser,
						Content: []bedrock.BedrockContentBlock{
							{
								Text: schemas.Ptr("Hello!"),
							},
						},
					},
				},
			},
		},
		{
			name: "InferenceParameters",
			input: &schemas.BifrostChatRequest{
				Model: "claude-3-sonnet",
				Input: []schemas.ChatMessage{
					{
						Role: schemas.ChatMessageRoleUser,
						Content: &schemas.ChatMessageContent{
							ContentStr: schemas.Ptr("Hello!"),
						},
					},
				},
				Params: &schemas.ChatParameters{
					MaxCompletionTokens: &maxTokens,
					Temperature:         &temp,
					TopP:                &topP,
					Stop:                stop,
				},
			},
			expected: &bedrock.BedrockConverseRequest{
				ModelID: "claude-3-sonnet",
				Messages: []bedrock.BedrockMessage{
					{
						Role: bedrock.BedrockMessageRoleUser,
						Content: []bedrock.BedrockContentBlock{
							{
								Text: schemas.Ptr("Hello!"),
							},
						},
					},
				},
				InferenceConfig: &bedrock.BedrockInferenceConfig{
					MaxTokens:     &maxTokens,
					Temperature:   &temp,
					TopP:          &topP,
					StopSequences: stop,
				},
			},
		},
		{
			name: "ServiceTierProvided",
			input: &schemas.BifrostChatRequest{
				Model: "claude-3-sonnet",
				Input: []schemas.ChatMessage{
					{
						Role: schemas.ChatMessageRoleUser,
						Content: &schemas.ChatMessageContent{
							ContentStr: schemas.Ptr("Hello!"),
						},
					},
				},
				Params: &schemas.ChatParameters{
					ServiceTier: &serviceTier,
				},
			},
			expected: &bedrock.BedrockConverseRequest{
				ModelID: "claude-3-sonnet",
				Messages: []bedrock.BedrockMessage{
					{
						Role: bedrock.BedrockMessageRoleUser,
						Content: []bedrock.BedrockContentBlock{
							{
								Text: schemas.Ptr("Hello!"),
							},
						},
					},
				},
				InferenceConfig: &bedrock.BedrockInferenceConfig{},
				ServiceTier: &bedrock.BedrockServiceTier{
					Type: serviceTier,
				},
			},
		},
		{
			name: "ServiceTierNotProvided",
			input: &schemas.BifrostChatRequest{
				Model: "claude-3-sonnet",
				Input: []schemas.ChatMessage{
					{
						Role: schemas.ChatMessageRoleUser,
						Content: &schemas.ChatMessageContent{
							ContentStr: schemas.Ptr("Hello!"),
						},
					},
				},
				Params: &schemas.ChatParameters{
					Temperature: &temp,
				},
			},
			expected: &bedrock.BedrockConverseRequest{
				ModelID: "claude-3-sonnet",
				Messages: []bedrock.BedrockMessage{
					{
						Role: bedrock.BedrockMessageRoleUser,
						Content: []bedrock.BedrockContentBlock{
							{
								Text: schemas.Ptr("Hello!"),
							},
						},
					},
				},
				InferenceConfig: &bedrock.BedrockInferenceConfig{
					Temperature: &temp,
				},
			},
		},
		{
			name: "Tools",
			input: &schemas.BifrostChatRequest{
				Model: "claude-3-sonnet",
				Input: []schemas.ChatMessage{
					{
						Role: schemas.ChatMessageRoleUser,
						Content: &schemas.ChatMessageContent{
							ContentStr: schemas.Ptr("What's the weather?"),
						},
					},
				},
				Params: &schemas.ChatParameters{
					Tools: []schemas.ChatTool{
						{
							Type: schemas.ChatToolTypeFunction,
							Function: &schemas.ChatToolFunction{
								Name:        "get_weather",
								Description: schemas.Ptr("Get weather information"),
								Parameters: &schemas.ToolFunctionParameters{
									Type: "object",
									Properties: &schemas.OrderedMap{
										"location": map[string]interface{}{
											"type":        "string",
											"description": "The city name",
										},
									},
									Required: []string{"location"},
								},
							},
						},
					},
				},
			},
			expected: &bedrock.BedrockConverseRequest{
				ModelID: "claude-3-sonnet",
				Messages: []bedrock.BedrockMessage{
					{
						Role: bedrock.BedrockMessageRoleUser,
						Content: []bedrock.BedrockContentBlock{
							{
								Text: schemas.Ptr("What's the weather?"),
							},
						},
					},
				},
				InferenceConfig: &bedrock.BedrockInferenceConfig{},
				ToolConfig: &bedrock.BedrockToolConfig{
					Tools: []bedrock.BedrockTool{
						{
							ToolSpec: &bedrock.BedrockToolSpec{
								Name:        "get_weather",
								Description: schemas.Ptr("Get weather information"),
								InputSchema: bedrock.BedrockToolInputSchema{
									JSON: map[string]interface{}{
										"type":       "object",
										"properties": &props,
										"required":   []string{"location"},
									},
								},
							},
						},
					},
				},
			},
		},
		{
			name: "AllExtraParams",
			input: &schemas.BifrostChatRequest{
				Model: "claude-3-sonnet",
				Input: []schemas.ChatMessage{
					{
						Role: schemas.ChatMessageRoleUser,
						Content: &schemas.ChatMessageContent{
							ContentStr: schemas.Ptr("Hello!"),
						},
					},
				},
				Params: &schemas.ChatParameters{
					ExtraParams: map[string]interface{}{
						"guardrailConfig": map[string]interface{}{
							"guardrailIdentifier": "test-guardrail",
							"guardrailVersion":    "1",
							"trace":               trace,
						},
						"performanceConfig": map[string]interface{}{
							"latency": "optimized",
						},
						"promptVariables": map[string]interface{}{
							"username": map[string]interface{}{
								"text": "John",
							},
						},
						"requestMetadata": map[string]string{
							"user": "test-user",
						},
						"additionalModelRequestFieldPaths": map[string]interface{}{
							"customField": "customValue",
						},
						"additionalModelResponseFieldPaths": []interface{}{"field1", "field2"},
					},
				},
			},
			expected: &bedrock.BedrockConverseRequest{
				ModelID: "claude-3-sonnet",
				Messages: []bedrock.BedrockMessage{
					{
						Role: bedrock.BedrockMessageRoleUser,
						Content: []bedrock.BedrockContentBlock{
							{
								Text: schemas.Ptr("Hello!"),
							},
						},
					},
				},
				InferenceConfig: &bedrock.BedrockInferenceConfig{},
				GuardrailConfig: &bedrock.BedrockGuardrailConfig{
					GuardrailIdentifier: "test-guardrail",
					GuardrailVersion:    "1",
					Trace:               &trace,
				},
				PerformanceConfig: &bedrock.BedrockPerformanceConfig{
					Latency: &latency,
				},
				PromptVariables: map[string]bedrock.BedrockPromptVariable{
					"username": {
						Text: schemas.Ptr("John"),
					},
				},
				RequestMetadata: map[string]string{
					"user": "test-user",
				},
				AdditionalModelRequestFields: schemas.OrderedMap{
					"customField": "customValue",
				},
				AdditionalModelResponseFieldPaths: []string{"field1", "field2"},
			},
		},
		{
			name:    "NilRequest",
			input:   nil,
			wantErr: true,
		},
		{
			name: "EmptyMessages",
			input: &schemas.BifrostChatRequest{
				Model: "claude-3-sonnet",
				Input: []schemas.ChatMessage{},
			},
			expected: &bedrock.BedrockConverseRequest{
				ModelID:  "claude-3-sonnet",
				Messages: nil,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			actual, err := bedrock.ToBedrockChatCompletionRequest(&ctx, tt.input)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, actual)
				if tt.input == nil {
					assert.Contains(t, err.Error(), "nil")
				}
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expected, actual)
			}
		})
	}
}

// TestBedrockToBifrostRequestConversion tests the conversion from Bedrock request to Bifrost request
func TestBedrockToBifrostRequestConversion(t *testing.T) {
	maxTokens := testMaxTokens
	temp := testTemp
	topP := testTopP
	trace := testTrace
	latency := testLatency
	props := testProps

	tests := []struct {
		name     string
		input    *bedrock.BedrockConverseRequest
		expected *schemas.BifrostResponsesRequest
		wantErr  bool
	}{
		{
			name: "BasicTextMessage",
			input: &bedrock.BedrockConverseRequest{
				ModelID: "bedrock/claude-3-sonnet",
				Messages: []bedrock.BedrockMessage{
					{
						Role: bedrock.BedrockMessageRoleUser,
						Content: []bedrock.BedrockContentBlock{
							{
								Text: schemas.Ptr("Hello, world!"),
							},
						},
					},
				},
			},
			expected: &schemas.BifrostResponsesRequest{
				Provider: schemas.Bedrock,
				Model:    "claude-3-sonnet",
				Input: []schemas.ResponsesMessage{
					{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
						Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
						Content: &schemas.ResponsesMessageContent{
							ContentBlocks: []schemas.ResponsesMessageContentBlock{
								{
									Type: schemas.ResponsesInputMessageContentBlockTypeText,
									Text: schemas.Ptr("Hello, world!"),
								},
							},
						},
					},
				},
				Params: &schemas.ResponsesParameters{},
			},
		},
		{
			name: "SystemMessage",
			input: &bedrock.BedrockConverseRequest{
				ModelID: "bedrock/claude-3-sonnet",
				System: []bedrock.BedrockSystemMessage{
					{
						Text: schemas.Ptr("You are a helpful assistant."),
					},
				},
				Messages: []bedrock.BedrockMessage{
					{
						Role: bedrock.BedrockMessageRoleUser,
						Content: []bedrock.BedrockContentBlock{
							{
								Text: schemas.Ptr("Hello!"),
							},
						},
					},
				},
			},
			expected: &schemas.BifrostResponsesRequest{
				Provider: schemas.Bedrock,
				Model:    "claude-3-sonnet",
				Input: []schemas.ResponsesMessage{
					{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
						Role: schemas.Ptr(schemas.ResponsesInputMessageRoleSystem),
						Content: &schemas.ResponsesMessageContent{
							ContentBlocks: []schemas.ResponsesMessageContentBlock{
								{
									Type: schemas.ResponsesInputMessageContentBlockTypeText,
									Text: schemas.Ptr("You are a helpful assistant."),
								},
							},
						},
					},
					{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
						Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
						Content: &schemas.ResponsesMessageContent{
							ContentBlocks: []schemas.ResponsesMessageContentBlock{
								{
									Type: schemas.ResponsesInputMessageContentBlockTypeText,
									Text: schemas.Ptr("Hello!"),
								},
							},
						},
					},
				},
				Params: &schemas.ResponsesParameters{},
			},
		},
		{
			name: "InferenceParameters",
			input: &bedrock.BedrockConverseRequest{
				ModelID: "bedrock/claude-3-sonnet",
				Messages: []bedrock.BedrockMessage{
					{
						Role: bedrock.BedrockMessageRoleUser,
						Content: []bedrock.BedrockContentBlock{
							{
								Text: schemas.Ptr("Hello!"),
							},
						},
					},
				},
				InferenceConfig: &bedrock.BedrockInferenceConfig{
					MaxTokens:   &maxTokens,
					Temperature: &temp,
					TopP:        &topP,
				},
			},
			expected: &schemas.BifrostResponsesRequest{
				Provider: schemas.Bedrock,
				Model:    "claude-3-sonnet",
				Input: []schemas.ResponsesMessage{
					{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
						Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
						Content: &schemas.ResponsesMessageContent{
							ContentBlocks: []schemas.ResponsesMessageContentBlock{
								{
									Type: schemas.ResponsesInputMessageContentBlockTypeText,
									Text: schemas.Ptr("Hello!"),
								},
							},
						},
					},
				},
				Params: &schemas.ResponsesParameters{
					MaxOutputTokens: &maxTokens,
					Temperature:     &temp,
					TopP:            &topP,
				},
			},
		},
		{
			name: "InferenceParametersWithStopSequences",
			input: &bedrock.BedrockConverseRequest{
				ModelID: "bedrock/claude-3-sonnet",
				Messages: []bedrock.BedrockMessage{
					{
						Role: bedrock.BedrockMessageRoleUser,
						Content: []bedrock.BedrockContentBlock{
							{
								Text: schemas.Ptr("Hello!"),
							},
						},
					},
				},
				InferenceConfig: &bedrock.BedrockInferenceConfig{
					MaxTokens:     &maxTokens,
					Temperature:   &temp,
					TopP:          &topP,
					StopSequences: testStop,
				},
			},
			expected: &schemas.BifrostResponsesRequest{
				Provider: schemas.Bedrock,
				Model:    "claude-3-sonnet",
				Input: []schemas.ResponsesMessage{
					{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
						Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
						Content: &schemas.ResponsesMessageContent{
							ContentBlocks: []schemas.ResponsesMessageContentBlock{
								{
									Type: schemas.ResponsesInputMessageContentBlockTypeText,
									Text: schemas.Ptr("Hello!"),
								},
							},
						},
					},
				},
				Params: &schemas.ResponsesParameters{
					MaxOutputTokens: &maxTokens,
					Temperature:     &temp,
					TopP:            &topP,
					ExtraParams: map[string]interface{}{
						"stop": testStop,
					},
				},
			},
		},
		{
			name: "Tools",
			input: &bedrock.BedrockConverseRequest{
				ModelID: "bedrock/claude-3-sonnet",
				Messages: []bedrock.BedrockMessage{
					{
						Role: bedrock.BedrockMessageRoleUser,
						Content: []bedrock.BedrockContentBlock{
							{
								Text: schemas.Ptr("What's the weather?"),
							},
						},
					},
				},
				ToolConfig: &bedrock.BedrockToolConfig{
					Tools: []bedrock.BedrockTool{
						{
							ToolSpec: &bedrock.BedrockToolSpec{
								Name:        "get_weather",
								Description: schemas.Ptr("Get weather information"),
								InputSchema: bedrock.BedrockToolInputSchema{
									JSON: map[string]interface{}{
										"type": "object",
										"properties": map[string]interface{}{
											"location": map[string]interface{}{
												"type":        "string",
												"description": "The city name",
											},
										},
										"required": []string{"location"},
									},
								},
							},
						},
					},
				},
			},
			expected: &schemas.BifrostResponsesRequest{
				Provider: schemas.Bedrock,
				Model:    "claude-3-sonnet",
				Input: []schemas.ResponsesMessage{
					{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
						Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
						Content: &schemas.ResponsesMessageContent{
							ContentBlocks: []schemas.ResponsesMessageContentBlock{
								{
									Type: schemas.ResponsesInputMessageContentBlockTypeText,
									Text: schemas.Ptr("What's the weather?"),
								},
							},
						},
					},
				},
				Params: &schemas.ResponsesParameters{
					Tools: []schemas.ResponsesTool{
						{
							Type:        schemas.ResponsesToolTypeFunction,
							Name:        schemas.Ptr("get_weather"),
							Description: schemas.Ptr("Get weather information"),
							ResponsesToolFunction: &schemas.ResponsesToolFunction{
								Parameters: &schemas.ToolFunctionParameters{
									Type:       "object",
									Properties: &props,
									Required:   []string{"location"},
								},
							},
						},
					},
				},
			},
		},
		{
			name: "AllExtraParams",
			input: &bedrock.BedrockConverseRequest{
				ModelID: "bedrock/claude-3-sonnet",
				Messages: []bedrock.BedrockMessage{
					{
						Role: bedrock.BedrockMessageRoleUser,
						Content: []bedrock.BedrockContentBlock{
							{
								Text: schemas.Ptr("Hello!"),
							},
						},
					},
				},
				GuardrailConfig: &bedrock.BedrockGuardrailConfig{
					GuardrailIdentifier: "test-guardrail",
					GuardrailVersion:    "1",
					Trace:               &trace,
				},
				PerformanceConfig: &bedrock.BedrockPerformanceConfig{
					Latency: &latency,
				},
				PromptVariables: map[string]bedrock.BedrockPromptVariable{
					"username": {
						Text: schemas.Ptr("John"),
					},
				},
				RequestMetadata: map[string]string{
					"user": "test-user",
				},
				AdditionalModelRequestFields: schemas.OrderedMap{
					"customField": "customValue",
				},
				AdditionalModelResponseFieldPaths: []string{"field1", "field2"},
			},
			expected: &schemas.BifrostResponsesRequest{
				Provider: schemas.Bedrock,
				Model:    "claude-3-sonnet",
				Input: []schemas.ResponsesMessage{
					{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
						Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
						Content: &schemas.ResponsesMessageContent{
							ContentBlocks: []schemas.ResponsesMessageContentBlock{
								{
									Type: schemas.ResponsesInputMessageContentBlockTypeText,
									Text: schemas.Ptr("Hello!"),
								},
							},
						},
					},
				},
				Params: &schemas.ResponsesParameters{
					ExtraParams: map[string]interface{}{
						"guardrailConfig": map[string]interface{}{
							"guardrailIdentifier": "test-guardrail",
							"guardrailVersion":    "1",
							"trace":               trace,
						},
						"performanceConfig": map[string]interface{}{
							"latency": latency,
						},
						"promptVariables": map[string]interface{}{
							"username": map[string]interface{}{
								"text": "John",
							},
						},
						"requestMetadata": map[string]string{
							"user": "test-user",
						},
						"additionalModelRequestFieldPaths": schemas.OrderedMap{
							"customField": "customValue",
						},
						"additionalModelResponseFieldPaths": []string{"field1", "field2"},
					},
				},
			},
		},
		{
			name: "MessageWithToolUse",
			input: &bedrock.BedrockConverseRequest{
				ModelID: "bedrock/claude-3-sonnet",
				Messages: []bedrock.BedrockMessage{
					{
						Role: bedrock.BedrockMessageRoleAssistant,
						Content: []bedrock.BedrockContentBlock{
							{
								ToolUse: &bedrock.BedrockToolUse{
									ToolUseID: "tool-use-123",
									Name:      "get_weather",
									Input: map[string]interface{}{
										"location": "NYC",
									},
								},
							},
						},
					},
				},
			},
			expected: &schemas.BifrostResponsesRequest{
				Provider: schemas.Bedrock,
				Model:    "claude-3-sonnet",
				Input: []schemas.ResponsesMessage{
					{
						Type:   schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
						Status: schemas.Ptr("completed"),
						ResponsesToolMessage: &schemas.ResponsesToolMessage{
							CallID:    schemas.Ptr("tool-use-123"),
							Name:      schemas.Ptr("get_weather"),
							Arguments: schemas.Ptr(`{"location":"NYC"}`),
						},
					},
				},
				Params: &schemas.ResponsesParameters{},
			},
		},
		{
			name: "MessageWithToolResult",
			input: &bedrock.BedrockConverseRequest{
				ModelID: "bedrock/claude-3-sonnet",
				Messages: []bedrock.BedrockMessage{
					{
						Role: bedrock.BedrockMessageRoleUser,
						Content: []bedrock.BedrockContentBlock{
							{
								ToolResult: &bedrock.BedrockToolResult{
									ToolUseID: "tool-use-123",
									Content: []bedrock.BedrockContentBlock{
										{
											Text: schemas.Ptr("The weather in NYC is sunny, 72°F"),
										},
									},
								},
							},
						},
					},
				},
			},
			expected: &schemas.BifrostResponsesRequest{
				Provider: schemas.Bedrock,
				Model:    "claude-3-sonnet",
				Input: []schemas.ResponsesMessage{
					{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
						ResponsesToolMessage: &schemas.ResponsesToolMessage{
							CallID: schemas.Ptr("tool-use-123"),
							Output: &schemas.ResponsesToolMessageOutputStruct{
								ResponsesToolCallOutputStr: schemas.Ptr("The weather in NYC is sunny, 72°F"),
							},
						},
					},
				},
				Params: &schemas.ResponsesParameters{},
			},
		},
		{
			name: "MessageWithBothToolUseAndToolResult",
			input: &bedrock.BedrockConverseRequest{
				ModelID: "bedrock/claude-3-sonnet",
				Messages: []bedrock.BedrockMessage{
					{
						Role: bedrock.BedrockMessageRoleAssistant,
						Content: []bedrock.BedrockContentBlock{
							{
								ToolUse: &bedrock.BedrockToolUse{
									ToolUseID: "tool-use-456",
									Name:      "calculate",
									Input: map[string]interface{}{
										"expression": "2+2",
									},
								},
							},
						},
					},
					{
						Role: bedrock.BedrockMessageRoleUser,
						Content: []bedrock.BedrockContentBlock{
							{
								ToolResult: &bedrock.BedrockToolResult{
									ToolUseID: "tool-use-456",
									Content: []bedrock.BedrockContentBlock{
										{
											Text: schemas.Ptr("4"),
										},
									},
								},
							},
						},
					},
				},
			},
			expected: &schemas.BifrostResponsesRequest{
				Provider: schemas.Bedrock,
				Model:    "claude-3-sonnet",
				Input: []schemas.ResponsesMessage{
					{
						Type:   schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
						Status: schemas.Ptr("completed"),
						ResponsesToolMessage: &schemas.ResponsesToolMessage{
							CallID:    schemas.Ptr("tool-use-456"),
							Name:      schemas.Ptr("calculate"),
							Arguments: schemas.Ptr(`{"expression":"2+2"}`),
						},
					},
					{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
						ResponsesToolMessage: &schemas.ResponsesToolMessage{
							CallID: schemas.Ptr("tool-use-456"),
							Output: &schemas.ResponsesToolMessageOutputStruct{
								ResponsesToolCallOutputStr: schemas.Ptr("4"),
							},
						},
					},
				},
				Params: &schemas.ResponsesParameters{},
			},
		},
		{
			name:    "NilRequest",
			input:   nil,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var actual *schemas.BifrostResponsesRequest
			var err error
			if tt.input == nil {
				var bedrockReq *bedrock.BedrockConverseRequest
				actual, err = bedrockReq.ToBifrostResponsesRequest()
			} else {
				actual, err = tt.input.ToBifrostResponsesRequest()
			}
			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, actual)
				if tt.input == nil {
					assert.Contains(t, err.Error(), "nil")
				}
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expected, actual)
			}
		})
	}
}

// TestBifrostToBedrockResponseConversion tests the conversion from Bifrost Responses response to Bedrock response
func TestBifrostToBedrockResponseConversion(t *testing.T) {
	inputTokens := 10
	outputTokens := 20
	totalTokens := 30
	latency := int64(100)
	callID := "call-123"
	toolName := "get_weather"
	arguments := `{"location":"NYC"}`
	reason := "max_tokens"

	tests := []struct {
		name     string
		input    *schemas.BifrostResponsesResponse
		expected *bedrock.BedrockConverseResponse
		wantErr  bool
	}{
		{
			name: "BasicTextResponse",
			input: &schemas.BifrostResponsesResponse{
				CreatedAt: 1234567890,
				Output: []schemas.ResponsesMessage{
					{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
						Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
						Content: &schemas.ResponsesMessageContent{
							ContentBlocks: []schemas.ResponsesMessageContentBlock{
								{
									Type: schemas.ResponsesOutputMessageContentTypeText,
									Text: schemas.Ptr("Hello, world!"),
								},
							},
						},
					},
				},
				// IncompleteDetails is nil, so should default to "end_turn"
			},
			expected: &bedrock.BedrockConverseResponse{
				StopReason: "end_turn", // Default stop reason when IncompleteDetails is nil
				Output: &bedrock.BedrockConverseOutput{
					Message: &bedrock.BedrockMessage{
						Role: bedrock.BedrockMessageRoleAssistant,
						Content: []bedrock.BedrockContentBlock{
							{
								Text: schemas.Ptr("Hello, world!"),
							},
						},
					},
				},
				Usage:   &bedrock.BedrockTokenUsage{},
				Metrics: &bedrock.BedrockConverseMetrics{},
			},
		},
		{
			name: "ResponseWithUsage",
			input: &schemas.BifrostResponsesResponse{
				CreatedAt: 1234567890,
				Output: []schemas.ResponsesMessage{
					{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
						Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
						Content: &schemas.ResponsesMessageContent{
							ContentBlocks: []schemas.ResponsesMessageContentBlock{
								{
									Type: schemas.ResponsesOutputMessageContentTypeText,
									Text: schemas.Ptr("Hello!"),
								},
							},
						},
					},
				},
				Usage: &schemas.ResponsesResponseUsage{
					InputTokens:  inputTokens,
					OutputTokens: outputTokens,
					TotalTokens:  totalTokens,
				},
			},
			expected: &bedrock.BedrockConverseResponse{
				StopReason: "end_turn",
				Output: &bedrock.BedrockConverseOutput{
					Message: &bedrock.BedrockMessage{
						Role: bedrock.BedrockMessageRoleAssistant,
						Content: []bedrock.BedrockContentBlock{
							{
								Text: schemas.Ptr("Hello!"),
							},
						},
					},
				},
				Usage: &bedrock.BedrockTokenUsage{
					InputTokens:  inputTokens,
					OutputTokens: outputTokens,
					TotalTokens:  totalTokens,
				},
				Metrics: &bedrock.BedrockConverseMetrics{},
			},
		},
		{
			name: "ResponseWithToolUse",
			input: &schemas.BifrostResponsesResponse{
				CreatedAt: 1234567890,
				Output: []schemas.ResponsesMessage{
					{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
						Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
						ResponsesToolMessage: &schemas.ResponsesToolMessage{
							CallID:    &callID,
							Name:      &toolName,
							Arguments: &arguments,
						},
					},
				},
			},
			expected: &bedrock.BedrockConverseResponse{
				StopReason: "tool_use",
				Output: &bedrock.BedrockConverseOutput{
					Message: &bedrock.BedrockMessage{
						Role: bedrock.BedrockMessageRoleAssistant,
						Content: []bedrock.BedrockContentBlock{
							{
								ToolUse: &bedrock.BedrockToolUse{
									ToolUseID: callID,
									Name:      toolName,
									Input:     map[string]interface{}{"location": "NYC"},
								},
							},
						},
					},
				},
				Usage:   &bedrock.BedrockTokenUsage{},
				Metrics: &bedrock.BedrockConverseMetrics{},
			},
		},
		{
			name: "ResponseWithToolUseInvalidJSON",
			input: &schemas.BifrostResponsesResponse{
				CreatedAt: 1234567890,
				Output: []schemas.ResponsesMessage{
					{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
						Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
						ResponsesToolMessage: &schemas.ResponsesToolMessage{
							CallID:    &callID,
							Name:      &toolName,
							Arguments: schemas.Ptr("invalid json {"),
						},
					},
				},
			},
			expected: &bedrock.BedrockConverseResponse{
				StopReason: "tool_use",
				Output: &bedrock.BedrockConverseOutput{
					Message: &bedrock.BedrockMessage{
						Role: bedrock.BedrockMessageRoleAssistant,
						Content: []bedrock.BedrockContentBlock{
							{
								ToolUse: &bedrock.BedrockToolUse{
									ToolUseID: callID,
									Name:      toolName,
									Input:     "invalid json {", // Should fallback to raw string
								},
							},
						},
					},
				},
				Usage:   &bedrock.BedrockTokenUsage{},
				Metrics: &bedrock.BedrockConverseMetrics{},
			},
		},
		{
			name: "ResponseWithToolUseNilArguments",
			input: &schemas.BifrostResponsesResponse{
				CreatedAt: 1234567890,
				Output: []schemas.ResponsesMessage{
					{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
						Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
						ResponsesToolMessage: &schemas.ResponsesToolMessage{
							CallID:    &callID,
							Name:      &toolName,
							Arguments: nil,
						},
					},
				},
			},
			expected: &bedrock.BedrockConverseResponse{
				StopReason: "tool_use",
				Output: &bedrock.BedrockConverseOutput{
					Message: &bedrock.BedrockMessage{
						Role: bedrock.BedrockMessageRoleAssistant,
						Content: []bedrock.BedrockContentBlock{
							{
								ToolUse: &bedrock.BedrockToolUse{
									ToolUseID: callID,
									Name:      toolName,
									Input:     map[string]interface{}{}, // Should default to empty map
								},
							},
						},
					},
				},
				Usage:   &bedrock.BedrockTokenUsage{},
				Metrics: &bedrock.BedrockConverseMetrics{},
			},
		},
		{
			name: "ResponseWithMetrics",
			input: &schemas.BifrostResponsesResponse{
				CreatedAt: 1234567890,
				Output: []schemas.ResponsesMessage{
					{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
						Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
						Content: &schemas.ResponsesMessageContent{
							ContentBlocks: []schemas.ResponsesMessageContentBlock{
								{
									Type: schemas.ResponsesOutputMessageContentTypeText,
									Text: schemas.Ptr("Hello!"),
								},
							},
						},
					},
				},
				ExtraFields: schemas.BifrostResponseExtraFields{
					Latency: latency,
				},
			},
			expected: &bedrock.BedrockConverseResponse{
				StopReason: "end_turn",
				Output: &bedrock.BedrockConverseOutput{
					Message: &bedrock.BedrockMessage{
						Role: bedrock.BedrockMessageRoleAssistant,
						Content: []bedrock.BedrockContentBlock{
							{
								Text: schemas.Ptr("Hello!"),
							},
						},
					},
				},
				Usage: &bedrock.BedrockTokenUsage{},
				Metrics: &bedrock.BedrockConverseMetrics{
					LatencyMs: latency,
				},
			},
		},
		{
			name: "ResponseWithIncompleteDetails",
			input: &schemas.BifrostResponsesResponse{
				CreatedAt: 1234567890,
				Output: []schemas.ResponsesMessage{
					{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
						Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
						Content: &schemas.ResponsesMessageContent{
							ContentBlocks: []schemas.ResponsesMessageContentBlock{
								{
									Type: schemas.ResponsesOutputMessageContentTypeText,
									Text: schemas.Ptr("Hello!"),
								},
							},
						},
					},
				},
				IncompleteDetails: &schemas.ResponsesResponseIncompleteDetails{
					Reason: reason, // This should be used as stop reason instead of default "end_turn"
				},
			},
			expected: &bedrock.BedrockConverseResponse{
				StopReason: reason, // Should use IncompleteDetails.Reason when present
				Output: &bedrock.BedrockConverseOutput{
					Message: &bedrock.BedrockMessage{
						Role: bedrock.BedrockMessageRoleAssistant,
						Content: []bedrock.BedrockContentBlock{
							{
								Text: schemas.Ptr("Hello!"),
							},
						},
					},
				},
				Usage:   &bedrock.BedrockTokenUsage{},
				Metrics: &bedrock.BedrockConverseMetrics{},
			},
		},
		{
			name: "ResponseWithToolResultString",
			input: &schemas.BifrostResponsesResponse{
				CreatedAt: 1234567890,
				Output: []schemas.ResponsesMessage{
					{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
						Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
						ResponsesToolMessage: &schemas.ResponsesToolMessage{
							CallID: schemas.Ptr("call-123"),
							Output: &schemas.ResponsesToolMessageOutputStruct{
								ResponsesToolCallOutputStr: schemas.Ptr("Tool result text"),
							},
						},
					},
				},
			},
			expected: &bedrock.BedrockConverseResponse{
				StopReason: "end_turn",
				Output: &bedrock.BedrockConverseOutput{
					Message: &bedrock.BedrockMessage{
						Role: bedrock.BedrockMessageRoleAssistant,
						Content: []bedrock.BedrockContentBlock{
							{
								ToolResult: &bedrock.BedrockToolResult{
									ToolUseID: "call-123",
									Status:    schemas.Ptr("success"),
									Content: []bedrock.BedrockContentBlock{
										{
											Text: schemas.Ptr("Tool result text"),
										},
									},
								},
							},
						},
					},
				},
				Usage:   &bedrock.BedrockTokenUsage{},
				Metrics: &bedrock.BedrockConverseMetrics{},
			},
		},
		{
			name: "ResponseWithToolResultJSON",
			input: &schemas.BifrostResponsesResponse{
				CreatedAt: 1234567890,
				Output: []schemas.ResponsesMessage{
					{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
						Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
						ResponsesToolMessage: &schemas.ResponsesToolMessage{
							CallID: schemas.Ptr("call-456"),
							Output: &schemas.ResponsesToolMessageOutputStruct{
								ResponsesToolCallOutputStr: schemas.Ptr(`{"temperature": 72, "location": "NYC"}`),
							},
						},
					},
				},
			},
			expected: &bedrock.BedrockConverseResponse{
				StopReason: "end_turn",
				Output: &bedrock.BedrockConverseOutput{
					Message: &bedrock.BedrockMessage{
						Role: bedrock.BedrockMessageRoleAssistant,
						Content: []bedrock.BedrockContentBlock{
							{
								ToolResult: &bedrock.BedrockToolResult{
									ToolUseID: "call-456",
									Status:    schemas.Ptr("success"),
									Content: []bedrock.BedrockContentBlock{
										{
											JSON: map[string]interface{}{
												"temperature": float64(72),
												"location":    "NYC",
											},
										},
									},
								},
							},
						},
					},
				},
				Usage:   &bedrock.BedrockTokenUsage{},
				Metrics: &bedrock.BedrockConverseMetrics{},
			},
		},
		{
			name: "ResponseWithToolResultContentBlocks",
			input: &schemas.BifrostResponsesResponse{
				CreatedAt: 1234567890,
				Output: []schemas.ResponsesMessage{
					{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
						Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
						ResponsesToolMessage: &schemas.ResponsesToolMessage{
							CallID: schemas.Ptr("call-789"),
							Output: &schemas.ResponsesToolMessageOutputStruct{
								ResponsesFunctionToolCallOutputBlocks: []schemas.ResponsesMessageContentBlock{
									{
										Type: schemas.ResponsesOutputMessageContentTypeText,
										Text: schemas.Ptr("Result from tool"),
									},
								},
							},
						},
					},
				},
			},
			expected: &bedrock.BedrockConverseResponse{
				StopReason: "end_turn",
				Output: &bedrock.BedrockConverseOutput{
					Message: &bedrock.BedrockMessage{
						Role: bedrock.BedrockMessageRoleAssistant,
						Content: []bedrock.BedrockContentBlock{
							{
								ToolResult: &bedrock.BedrockToolResult{
									ToolUseID: "call-789",
									Status:    schemas.Ptr("success"),
									Content: []bedrock.BedrockContentBlock{
										{
											Text: schemas.Ptr("Result from tool"),
										},
									},
								},
							},
						},
					},
				},
				Usage:   &bedrock.BedrockTokenUsage{},
				Metrics: &bedrock.BedrockConverseMetrics{},
			},
		},
		{
			name: "ResponseWithToolUseAndToolResult",
			input: &schemas.BifrostResponsesResponse{
				CreatedAt: 1234567890,
				Output: []schemas.ResponsesMessage{
					{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
						Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
						ResponsesToolMessage: &schemas.ResponsesToolMessage{
							CallID:    schemas.Ptr("call-111"),
							Name:      schemas.Ptr("get_weather"),
							Arguments: schemas.Ptr(`{"location": "NYC"}`),
						},
					},
					{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
						Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
						ResponsesToolMessage: &schemas.ResponsesToolMessage{
							CallID: schemas.Ptr("call-111"),
							Output: &schemas.ResponsesToolMessageOutputStruct{
								ResponsesToolCallOutputStr: schemas.Ptr(`{"temperature": 72}`),
							},
						},
					},
				},
			},
			expected: &bedrock.BedrockConverseResponse{
				StopReason: "tool_use",
				Output: &bedrock.BedrockConverseOutput{
					Message: &bedrock.BedrockMessage{
						Role: bedrock.BedrockMessageRoleAssistant,
						Content: []bedrock.BedrockContentBlock{
							{
								ToolUse: &bedrock.BedrockToolUse{
									ToolUseID: "call-111",
									Name:      "get_weather",
									Input: map[string]interface{}{
										"location": "NYC",
									},
								},
							},
							{
								ToolResult: &bedrock.BedrockToolResult{
									ToolUseID: "call-111",
									Status:    schemas.Ptr("success"),
									Content: []bedrock.BedrockContentBlock{
										{
											JSON: map[string]interface{}{
												"temperature": float64(72),
											},
										},
									},
								},
							},
						},
					},
				},
				Usage:   &bedrock.BedrockTokenUsage{},
				Metrics: &bedrock.BedrockConverseMetrics{},
			},
		},
		{
			name: "ResponseWithToolUseAndIncompleteDetails",
			input: &schemas.BifrostResponsesResponse{
				CreatedAt: 1234567890,
				Output: []schemas.ResponsesMessage{
					{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
						Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
						ResponsesToolMessage: &schemas.ResponsesToolMessage{
							CallID:    &callID,
							Name:      &toolName,
							Arguments: &arguments,
						},
					},
				},
				IncompleteDetails: &schemas.ResponsesResponseIncompleteDetails{
					Reason: reason, // IncompleteDetails should take priority over tool_use
				},
			},
			expected: &bedrock.BedrockConverseResponse{
				StopReason: reason, // Should use IncompleteDetails.Reason even when tool use is present
				Output: &bedrock.BedrockConverseOutput{
					Message: &bedrock.BedrockMessage{
						Role: bedrock.BedrockMessageRoleAssistant,
						Content: []bedrock.BedrockContentBlock{
							{
								ToolUse: &bedrock.BedrockToolUse{
									ToolUseID: callID,
									Name:      toolName,
									Input:     map[string]interface{}{"location": "NYC"},
								},
							},
						},
					},
				},
				Usage:   &bedrock.BedrockTokenUsage{},
				Metrics: &bedrock.BedrockConverseMetrics{},
			},
		},
		{
			name:    "NilResponse",
			input:   nil,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual, err := bedrock.ToBedrockConverseResponse(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, actual)
				if tt.input == nil {
					assert.Contains(t, err.Error(), "nil")
				}
			} else {
				require.NoError(t, err)
				// Compare structure instead of exact equality since IDs may be generated
				if tt.expected != nil && actual != nil {
					assert.Equal(t, tt.expected.StopReason, actual.StopReason)
					assert.Equal(t, tt.expected.Output.Message.Role, actual.Output.Message.Role)
					assert.Equal(t, len(tt.expected.Output.Message.Content), len(actual.Output.Message.Content))
					if tt.expected.Usage != nil {
						assert.Equal(t, tt.expected.Usage.InputTokens, actual.Usage.InputTokens)
						assert.Equal(t, tt.expected.Usage.OutputTokens, actual.Usage.OutputTokens)
						assert.Equal(t, tt.expected.Usage.TotalTokens, actual.Usage.TotalTokens)
					}
					if tt.expected.Metrics != nil {
						assert.Equal(t, tt.expected.Metrics.LatencyMs, actual.Metrics.LatencyMs)
					}
				} else {
					assert.Equal(t, tt.expected, actual)
				}
			}
		})
	}
}

// TestBedrockToBifrostResponseConversion tests the conversion from Bedrock response to Bifrost Responses response
func TestBedrockToBifrostResponseConversion(t *testing.T) {
	inputTokens := 10
	outputTokens := 20
	totalTokens := 30
	toolUseID := "call-123"
	toolName := "get_weather"
	toolInput := map[string]interface{}{
		"location": "NYC",
	}

	tests := []struct {
		name     string
		input    *bedrock.BedrockConverseResponse
		expected *schemas.BifrostResponsesResponse
		wantErr  bool
	}{
		{
			name: "BasicTextResponse",
			input: &bedrock.BedrockConverseResponse{
				StopReason: "end_turn",
				Output: &bedrock.BedrockConverseOutput{
					Message: &bedrock.BedrockMessage{
						Role: bedrock.BedrockMessageRoleAssistant,
						Content: []bedrock.BedrockContentBlock{
							{
								Text: schemas.Ptr("Hello, world!"),
							},
						},
					},
				},
			},
			expected: &schemas.BifrostResponsesResponse{
				Output: []schemas.ResponsesMessage{
					{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
						Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
						Content: &schemas.ResponsesMessageContent{
							ContentBlocks: []schemas.ResponsesMessageContentBlock{
								{
									Type: schemas.ResponsesOutputMessageContentTypeText,
									Text: schemas.Ptr("Hello, world!"),
								},
							},
						},
					},
				},
			},
		},
		{
			name: "ResponseWithUsage",
			input: &bedrock.BedrockConverseResponse{
				StopReason: "end_turn",
				Output: &bedrock.BedrockConverseOutput{
					Message: &bedrock.BedrockMessage{
						Role: bedrock.BedrockMessageRoleAssistant,
						Content: []bedrock.BedrockContentBlock{
							{
								Text: schemas.Ptr("Hello!"),
							},
						},
					},
				},
				Usage: &bedrock.BedrockTokenUsage{
					InputTokens:  inputTokens,
					OutputTokens: outputTokens,
					TotalTokens:  totalTokens,
				},
			},
			expected: &schemas.BifrostResponsesResponse{
				Output: []schemas.ResponsesMessage{
					{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
						Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
						Content: &schemas.ResponsesMessageContent{
							ContentBlocks: []schemas.ResponsesMessageContentBlock{
								{
									Type: schemas.ResponsesOutputMessageContentTypeText,
									Text: schemas.Ptr("Hello!"),
								},
							},
						},
					},
				},
				Usage: &schemas.ResponsesResponseUsage{
					InputTokens:  inputTokens,
					OutputTokens: outputTokens,
					TotalTokens:  totalTokens,
				},
			},
		},
		{
			name: "ResponseWithToolUse",
			input: &bedrock.BedrockConverseResponse{
				StopReason: "end_turn",
				Output: &bedrock.BedrockConverseOutput{
					Message: &bedrock.BedrockMessage{
						Role: bedrock.BedrockMessageRoleAssistant,
						Content: []bedrock.BedrockContentBlock{
							{
								ToolUse: &bedrock.BedrockToolUse{
									ToolUseID: toolUseID,
									Name:      toolName,
									Input:     toolInput,
								},
							},
						},
					},
				},
			},
			expected: &schemas.BifrostResponsesResponse{
				Output: []schemas.ResponsesMessage{
					{
						Type:   schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
						Role:   schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
						Status: schemas.Ptr("completed"),
						ResponsesToolMessage: &schemas.ResponsesToolMessage{
							CallID:    &toolUseID,
							Name:      &toolName,
							Arguments: schemas.Ptr(schemas.JsonifyInput(toolInput)),
						},
					},
				},
			},
		},
		{
			name:    "NilResponse",
			input:   nil,
			wantErr: true,
		},
		{
			name: "EmptyOutput",
			input: &bedrock.BedrockConverseResponse{
				StopReason: "end_turn",
				Output: &bedrock.BedrockConverseOutput{
					Message: &bedrock.BedrockMessage{
						Role:    bedrock.BedrockMessageRoleAssistant,
						Content: []bedrock.BedrockContentBlock{},
					},
				},
			},
			expected: &schemas.BifrostResponsesResponse{
				Output: nil, // Empty content blocks result in nil output
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var actual *schemas.BifrostResponsesResponse
			var err error
			if tt.input == nil {
				var bedrockResp *bedrock.BedrockConverseResponse
				actual, err = bedrockResp.ToBifrostResponsesResponse()
			} else {
				actual, err = tt.input.ToBifrostResponsesResponse()
			}
			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, actual)
				if tt.input == nil {
					assert.Contains(t, err.Error(), "nil")
				}
			} else {
				require.NoError(t, err)
				// Note: CreatedAt and IDs are set at runtime, so compare structure instead
				if actual != nil {
					assert.Greater(t, actual.CreatedAt, 0)
					actual.CreatedAt = tt.expected.CreatedAt

					// For output messages, IDs are generated, so we need to compare by value not identity
					if len(actual.Output) > 0 && len(tt.expected.Output) > 0 {
						assert.Equal(t, len(tt.expected.Output), len(actual.Output))
						for i := range actual.Output {
							assert.Equal(t, tt.expected.Output[i].Type, actual.Output[i].Type)
							assert.Equal(t, tt.expected.Output[i].Role, actual.Output[i].Role)
							assert.Equal(t, tt.expected.Output[i].Status, actual.Output[i].Status)
							if tt.expected.Output[i].ResponsesToolMessage != nil {
								assert.NotNil(t, actual.Output[i].ResponsesToolMessage)
								require.NotNil(t, actual.Output[i].ResponsesToolMessage.Name)
								require.NotNil(t, actual.Output[i].ResponsesToolMessage.CallID)
								require.NotNil(t, actual.Output[i].ResponsesToolMessage.Arguments)
								assert.Equal(t, *tt.expected.Output[i].ResponsesToolMessage.Name, *actual.Output[i].ResponsesToolMessage.Name)
								assert.Equal(t, *tt.expected.Output[i].ResponsesToolMessage.CallID, *actual.Output[i].ResponsesToolMessage.CallID)
								assert.Equal(t, *tt.expected.Output[i].ResponsesToolMessage.Arguments, *actual.Output[i].ResponsesToolMessage.Arguments)
							}
							if tt.expected.Output[i].Content != nil {
								assert.Equal(t, tt.expected.Output[i].Content, actual.Output[i].Content)
							}
						}
					}

					// Compare usage if present
					if tt.expected.Usage != nil {
						assert.NotNil(t, actual.Usage)
						assert.Equal(t, tt.expected.Usage.InputTokens, actual.Usage.InputTokens)
						assert.Equal(t, tt.expected.Usage.OutputTokens, actual.Usage.OutputTokens)
						assert.Equal(t, tt.expected.Usage.TotalTokens, actual.Usage.TotalTokens)
					}
				}
			}
		})
	}
}

func TestToBedrockResponsesRequest_AdditionalFields(t *testing.T) {
	req := &schemas.BifrostResponsesRequest{
		Model: "bedrock/anthropic.claude-3-sonnet-20240229-v1:0",
		Params: &schemas.ResponsesParameters{
			ExtraParams: map[string]interface{}{
				"additionalModelRequestFieldPaths": map[string]interface{}{
					"top_k": 200,
				},
				"additionalModelResponseFieldPaths": []string{
					"/amazon-bedrock-invocationMetrics/inputTokenCount",
				},
			},
		},
	}

	bedrockReq, err := bedrock.ToBedrockResponsesRequest(req)
	require.NoError(t, err)
	require.NotNil(t, bedrockReq)

	// Convert OrderedMap to map[string]interface{} for comparison
	expectedFields := map[string]interface{}{"top_k": 200}
	actualFields := make(map[string]interface{})
	for k, v := range bedrockReq.AdditionalModelRequestFields {
		actualFields[k] = v
	}
	assert.Equal(t, expectedFields, actualFields)
	assert.Equal(t, []string{"/amazon-bedrock-invocationMetrics/inputTokenCount"}, bedrockReq.AdditionalModelResponseFieldPaths)
}

func TestToBedrockResponsesRequest_AdditionalFields_InterfaceSlice(t *testing.T) {
	req := &schemas.BifrostResponsesRequest{
		Model: "bedrock/anthropic.claude-3-sonnet-20240229-v1:0",
		Params: &schemas.ResponsesParameters{
			ExtraParams: map[string]interface{}{
				"additionalModelResponseFieldPaths": []interface{}{
					"/amazon-bedrock-invocationMetrics/inputTokenCount",
				},
			},
		},
	}

	bedrockReq, err := bedrock.ToBedrockResponsesRequest(req)
	require.NoError(t, err)
	require.NotNil(t, bedrockReq)

	assert.Equal(t, []string{"/amazon-bedrock-invocationMetrics/inputTokenCount"}, bedrockReq.AdditionalModelResponseFieldPaths)
}
