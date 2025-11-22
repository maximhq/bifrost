package bedrock

import (
	"testing"

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
	testProps     = map[string]interface{}{
		"location": map[string]interface{}{
			"type":        "string",
			"description": "The city name",
		},
	}
)

// TestBifrostToBedrockRequestConversion tests the conversion from Bifrost request to Bedrock request
func TestBifrostToBedrockRequestConversion(t *testing.T) {
	maxTokens := testMaxTokens
	temp := testTemp
	topP := testTopP
	stop := testStop
	trace := testTrace
	latency := testLatency
	props := testProps

	tests := []struct {
		name     string
		input    *schemas.BifrostChatRequest
		expected *BedrockConverseRequest
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
			expected: &BedrockConverseRequest{
				ModelID: "claude-3-sonnet",
				Messages: []BedrockMessage{
					{
						Role: BedrockMessageRoleUser,
						Content: []BedrockContentBlock{
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
			expected: &BedrockConverseRequest{
				ModelID: "claude-3-sonnet",
				System: []BedrockSystemMessage{
					{
						Text: schemas.Ptr("System message 1"),
					},
					{
						Text: schemas.Ptr("System message 2"),
					},
				},
				Messages: []BedrockMessage{
					{
						Role: BedrockMessageRoleUser,
						Content: []BedrockContentBlock{
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
			expected: &BedrockConverseRequest{
				ModelID: "claude-3-sonnet",
				Messages: []BedrockMessage{
					{
						Role: BedrockMessageRoleUser,
						Content: []BedrockContentBlock{
							{
								Text: schemas.Ptr("Hello!"),
							},
						},
					},
				},
				InferenceConfig: &BedrockInferenceConfig{
					MaxTokens:     &maxTokens,
					Temperature:   &temp,
					TopP:          &topP,
					StopSequences: stop,
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
									Properties: &map[string]interface{}{
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
			expected: &BedrockConverseRequest{
				ModelID: "claude-3-sonnet",
				Messages: []BedrockMessage{
					{
						Role: BedrockMessageRoleUser,
						Content: []BedrockContentBlock{
							{
								Text: schemas.Ptr("What's the weather?"),
							},
						},
					},
				},
				InferenceConfig: &BedrockInferenceConfig{},
				ToolConfig: &BedrockToolConfig{
					Tools: []BedrockTool{
						{
							ToolSpec: &BedrockToolSpec{
								Name:        "get_weather",
								Description: schemas.Ptr("Get weather information"),
								InputSchema: BedrockToolInputSchema{
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
			expected: &BedrockConverseRequest{
				ModelID: "claude-3-sonnet",
				Messages: []BedrockMessage{
					{
						Role: BedrockMessageRoleUser,
						Content: []BedrockContentBlock{
							{
								Text: schemas.Ptr("Hello!"),
							},
						},
					},
				},
				InferenceConfig: &BedrockInferenceConfig{},
				GuardrailConfig: &BedrockGuardrailConfig{
					GuardrailIdentifier: "test-guardrail",
					GuardrailVersion:    "1",
					Trace:               &trace,
				},
				PerformanceConfig: &BedrockPerformanceConfig{
					Latency: &latency,
				},
				PromptVariables: map[string]BedrockPromptVariable{
					"username": {
						Text: schemas.Ptr("John"),
					},
				},
				RequestMetadata: map[string]string{
					"user": "test-user",
				},
				AdditionalModelRequestFields: map[string]interface{}{
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
			expected: &BedrockConverseRequest{
				ModelID:  "claude-3-sonnet",
				Messages: nil,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual, err := ToBedrockChatCompletionRequest(tt.input)
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
		input    *BedrockConverseRequest
		expected *schemas.BifrostResponsesRequest
		wantErr  bool
	}{
		{
			name: "BasicTextMessage",
			input: &BedrockConverseRequest{
				ModelID: "bedrock/claude-3-sonnet",
				Messages: []BedrockMessage{
					{
						Role: BedrockMessageRoleUser,
						Content: []BedrockContentBlock{
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
			input: &BedrockConverseRequest{
				ModelID: "bedrock/claude-3-sonnet",
				System: []BedrockSystemMessage{
					{
						Text: schemas.Ptr("You are a helpful assistant."),
					},
				},
				Messages: []BedrockMessage{
					{
						Role: BedrockMessageRoleUser,
						Content: []BedrockContentBlock{
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
			input: &BedrockConverseRequest{
				ModelID: "bedrock/claude-3-sonnet",
				Messages: []BedrockMessage{
					{
						Role: BedrockMessageRoleUser,
						Content: []BedrockContentBlock{
							{
								Text: schemas.Ptr("Hello!"),
							},
						},
					},
				},
				InferenceConfig: &BedrockInferenceConfig{
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
			input: &BedrockConverseRequest{
				ModelID: "bedrock/claude-3-sonnet",
				Messages: []BedrockMessage{
					{
						Role: BedrockMessageRoleUser,
						Content: []BedrockContentBlock{
							{
								Text: schemas.Ptr("Hello!"),
							},
						},
					},
				},
				InferenceConfig: &BedrockInferenceConfig{
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
			input: &BedrockConverseRequest{
				ModelID: "bedrock/claude-3-sonnet",
				Messages: []BedrockMessage{
					{
						Role: BedrockMessageRoleUser,
						Content: []BedrockContentBlock{
							{
								Text: schemas.Ptr("What's the weather?"),
							},
						},
					},
				},
				ToolConfig: &BedrockToolConfig{
					Tools: []BedrockTool{
						{
							ToolSpec: &BedrockToolSpec{
								Name:        "get_weather",
								Description: schemas.Ptr("Get weather information"),
								InputSchema: BedrockToolInputSchema{
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
			input: &BedrockConverseRequest{
				ModelID: "bedrock/claude-3-sonnet",
				Messages: []BedrockMessage{
					{
						Role: BedrockMessageRoleUser,
						Content: []BedrockContentBlock{
							{
								Text: schemas.Ptr("Hello!"),
							},
						},
					},
				},
				GuardrailConfig: &BedrockGuardrailConfig{
					GuardrailIdentifier: "test-guardrail",
					GuardrailVersion:    "1",
					Trace:               &trace,
				},
				PerformanceConfig: &BedrockPerformanceConfig{
					Latency: &latency,
				},
				PromptVariables: map[string]BedrockPromptVariable{
					"username": {
						Text: schemas.Ptr("John"),
					},
				},
				RequestMetadata: map[string]string{
					"user": "test-user",
				},
				AdditionalModelRequestFields: map[string]interface{}{
					"customField": "customValue",
				},
				AdditionalModelResponseFieldPaths: []string{"field1", "field2"},
			},
			expected: &schemas.BifrostResponsesRequest{
				Provider: schemas.Bedrock,
				Model:    "claude-3-sonnet",
				Input: []schemas.ResponsesMessage{
					{
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
						"additionalModelRequestFieldPaths": map[string]interface{}{
							"customField": "customValue",
						},
						"additionalModelResponseFieldPaths": []string{"field1", "field2"},
					},
				},
			},
		},
		{
			name: "MessageWithToolUse",
			input: &BedrockConverseRequest{
				ModelID: "bedrock/claude-3-sonnet",
				Messages: []BedrockMessage{
					{
						Role: BedrockMessageRoleAssistant,
						Content: []BedrockContentBlock{
							{
								ToolUse: &BedrockToolUse{
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
						Role:   schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
						Type:   schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
						Status: schemas.Ptr("in_progress"),
						ResponsesToolMessage: &schemas.ResponsesToolMessage{
							CallID:    schemas.Ptr("tool-use-123"),
							Name:      schemas.Ptr("get_weather"),
							Arguments: schemas.Ptr(`{"location":"NYC"}`),
						},
						Content: &schemas.ResponsesMessageContent{
							ContentBlocks: []schemas.ResponsesMessageContentBlock{},
						},
					},
				},
				Params: &schemas.ResponsesParameters{},
			},
		},
		{
			name: "MessageWithToolResult",
			input: &BedrockConverseRequest{
				ModelID: "bedrock/claude-3-sonnet",
				Messages: []BedrockMessage{
					{
						Role: BedrockMessageRoleUser,
						Content: []BedrockContentBlock{
							{
								ToolResult: &BedrockToolResult{
									ToolUseID: "tool-use-123",
									Content: []BedrockContentBlock{
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
						Role:   schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
						Type:   schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
						Status: schemas.Ptr("completed"),
						ResponsesToolMessage: &schemas.ResponsesToolMessage{
							CallID: schemas.Ptr("tool-use-123"),
							Output: &schemas.ResponsesToolMessageOutputStruct{
								ResponsesFunctionToolCallOutputBlocks: []schemas.ResponsesMessageContentBlock{
									{
										Type: schemas.ResponsesInputMessageContentBlockTypeText,
										Text: schemas.Ptr("The weather in NYC is sunny, 72°F"),
									},
								},
							},
						},
						Content: &schemas.ResponsesMessageContent{
							ContentBlocks: []schemas.ResponsesMessageContentBlock{},
						},
					},
				},
				Params: &schemas.ResponsesParameters{},
			},
		},
		{
			name: "MessageWithBothToolUseAndToolResult",
			input: &BedrockConverseRequest{
				ModelID: "bedrock/claude-3-sonnet",
				Messages: []BedrockMessage{
					{
						Role: BedrockMessageRoleAssistant,
						Content: []BedrockContentBlock{
							{
								ToolUse: &BedrockToolUse{
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
						Role: BedrockMessageRoleUser,
						Content: []BedrockContentBlock{
							{
								ToolResult: &BedrockToolResult{
									ToolUseID: "tool-use-456",
									Content: []BedrockContentBlock{
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
						Role:   schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
						Type:   schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
						Status: schemas.Ptr("in_progress"),
						ResponsesToolMessage: &schemas.ResponsesToolMessage{
							CallID:    schemas.Ptr("tool-use-456"),
							Name:      schemas.Ptr("calculate"),
							Arguments: schemas.Ptr(`{"expression":"2+2"}`),
						},
						Content: &schemas.ResponsesMessageContent{
							ContentBlocks: []schemas.ResponsesMessageContentBlock{},
						},
					},
					{
						Role:   schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
						Type:   schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
						Status: schemas.Ptr("completed"),
						ResponsesToolMessage: &schemas.ResponsesToolMessage{
							CallID: schemas.Ptr("tool-use-456"),
							Output: &schemas.ResponsesToolMessageOutputStruct{
								ResponsesFunctionToolCallOutputBlocks: []schemas.ResponsesMessageContentBlock{
									{
										Type: schemas.ResponsesInputMessageContentBlockTypeText,
										Text: schemas.Ptr("4"),
									},
								},
							},
						},
						Content: &schemas.ResponsesMessageContent{
							ContentBlocks: []schemas.ResponsesMessageContentBlock{},
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
				var bedrockReq *BedrockConverseRequest
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
		expected *BedrockConverseResponse
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
			expected: &BedrockConverseResponse{
				StopReason: "end_turn", // Default stop reason when IncompleteDetails is nil
				Output: &BedrockConverseOutput{
					Message: &BedrockMessage{
						Role: BedrockMessageRoleAssistant,
						Content: []BedrockContentBlock{
							{
								Text: schemas.Ptr("Hello, world!"),
							},
						},
					},
				},
				Usage:   &BedrockTokenUsage{},
				Metrics: &BedrockConverseMetrics{},
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
			expected: &BedrockConverseResponse{
				StopReason: "end_turn",
				Output: &BedrockConverseOutput{
					Message: &BedrockMessage{
						Role: BedrockMessageRoleAssistant,
						Content: []BedrockContentBlock{
							{
								Text: schemas.Ptr("Hello!"),
							},
						},
					},
				},
				Usage: &BedrockTokenUsage{
					InputTokens:  inputTokens,
					OutputTokens: outputTokens,
					TotalTokens:  totalTokens,
				},
				Metrics: &BedrockConverseMetrics{},
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
			expected: &BedrockConverseResponse{
				StopReason: "tool_use",
				Output: &BedrockConverseOutput{
					Message: &BedrockMessage{
						Role: BedrockMessageRoleAssistant,
						Content: []BedrockContentBlock{
							{
								ToolUse: &BedrockToolUse{
									ToolUseID: callID,
									Name:      toolName,
									Input:     map[string]interface{}{"location": "NYC"},
								},
							},
						},
					},
				},
				Usage:   &BedrockTokenUsage{},
				Metrics: &BedrockConverseMetrics{},
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
			expected: &BedrockConverseResponse{
				StopReason: "tool_use",
				Output: &BedrockConverseOutput{
					Message: &BedrockMessage{
						Role: BedrockMessageRoleAssistant,
						Content: []BedrockContentBlock{
							{
								ToolUse: &BedrockToolUse{
									ToolUseID: callID,
									Name:      toolName,
									Input:     "invalid json {", // Should fallback to raw string
								},
							},
						},
					},
				},
				Usage:   &BedrockTokenUsage{},
				Metrics: &BedrockConverseMetrics{},
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
			expected: &BedrockConverseResponse{
				StopReason: "tool_use",
				Output: &BedrockConverseOutput{
					Message: &BedrockMessage{
						Role: BedrockMessageRoleAssistant,
						Content: []BedrockContentBlock{
							{
								ToolUse: &BedrockToolUse{
									ToolUseID: callID,
									Name:      toolName,
									Input:     map[string]interface{}{}, // Should default to empty map
								},
							},
						},
					},
				},
				Usage:   &BedrockTokenUsage{},
				Metrics: &BedrockConverseMetrics{},
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
			expected: &BedrockConverseResponse{
				StopReason: "end_turn",
				Output: &BedrockConverseOutput{
					Message: &BedrockMessage{
						Role: BedrockMessageRoleAssistant,
						Content: []BedrockContentBlock{
							{
								Text: schemas.Ptr("Hello!"),
							},
						},
					},
				},
				Usage: &BedrockTokenUsage{},
				Metrics: &BedrockConverseMetrics{
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
			expected: &BedrockConverseResponse{
				StopReason: reason, // Should use IncompleteDetails.Reason when present
				Output: &BedrockConverseOutput{
					Message: &BedrockMessage{
						Role: BedrockMessageRoleAssistant,
						Content: []BedrockContentBlock{
							{
								Text: schemas.Ptr("Hello!"),
							},
						},
					},
				},
				Usage:   &BedrockTokenUsage{},
				Metrics: &BedrockConverseMetrics{},
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
			expected: &BedrockConverseResponse{
				StopReason: "end_turn",
				Output: &BedrockConverseOutput{
					Message: &BedrockMessage{
						Role: BedrockMessageRoleAssistant,
						Content: []BedrockContentBlock{
							{
								ToolResult: &BedrockToolResult{
									ToolUseID: "call-123",
									Status:    schemas.Ptr("success"),
									Content: []BedrockContentBlock{
										{
											Text: schemas.Ptr("Tool result text"),
										},
									},
								},
							},
						},
					},
				},
				Usage:   &BedrockTokenUsage{},
				Metrics: &BedrockConverseMetrics{},
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
			expected: &BedrockConverseResponse{
				StopReason: "end_turn",
				Output: &BedrockConverseOutput{
					Message: &BedrockMessage{
						Role: BedrockMessageRoleAssistant,
						Content: []BedrockContentBlock{
							{
								ToolResult: &BedrockToolResult{
									ToolUseID: "call-456",
									Status:    schemas.Ptr("success"),
									Content: []BedrockContentBlock{
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
				Usage:   &BedrockTokenUsage{},
				Metrics: &BedrockConverseMetrics{},
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
			expected: &BedrockConverseResponse{
				StopReason: "end_turn",
				Output: &BedrockConverseOutput{
					Message: &BedrockMessage{
						Role: BedrockMessageRoleAssistant,
						Content: []BedrockContentBlock{
							{
								ToolResult: &BedrockToolResult{
									ToolUseID: "call-789",
									Status:    schemas.Ptr("success"),
									Content: []BedrockContentBlock{
										{
											Text: schemas.Ptr("Result from tool"),
										},
									},
								},
							},
						},
					},
				},
				Usage:   &BedrockTokenUsage{},
				Metrics: &BedrockConverseMetrics{},
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
			expected: &BedrockConverseResponse{
				StopReason: "tool_use",
				Output: &BedrockConverseOutput{
					Message: &BedrockMessage{
						Role: BedrockMessageRoleAssistant,
						Content: []BedrockContentBlock{
							{
								ToolUse: &BedrockToolUse{
									ToolUseID: "call-111",
									Name:      "get_weather",
									Input: map[string]interface{}{
										"location": "NYC",
									},
								},
							},
							{
								ToolResult: &BedrockToolResult{
									ToolUseID: "call-111",
									Status:    schemas.Ptr("success"),
									Content: []BedrockContentBlock{
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
				Usage:   &BedrockTokenUsage{},
				Metrics: &BedrockConverseMetrics{},
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
			expected: &BedrockConverseResponse{
				StopReason: reason, // Should use IncompleteDetails.Reason even when tool use is present
				Output: &BedrockConverseOutput{
					Message: &BedrockMessage{
						Role: BedrockMessageRoleAssistant,
						Content: []BedrockContentBlock{
							{
								ToolUse: &BedrockToolUse{
									ToolUseID: callID,
									Name:      toolName,
									Input:     map[string]interface{}{"location": "NYC"},
								},
							},
						},
					},
				},
				Usage:   &BedrockTokenUsage{},
				Metrics: &BedrockConverseMetrics{},
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
			actual, err := ToBedrockConverseResponse(tt.input)
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
		input    *BedrockConverseResponse
		expected *schemas.BifrostResponsesResponse
		wantErr  bool
	}{
		{
			name: "BasicTextResponse",
			input: &BedrockConverseResponse{
				StopReason: "end_turn",
				Output: &BedrockConverseOutput{
					Message: &BedrockMessage{
						Role: BedrockMessageRoleAssistant,
						Content: []BedrockContentBlock{
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
			input: &BedrockConverseResponse{
				StopReason: "end_turn",
				Output: &BedrockConverseOutput{
					Message: &BedrockMessage{
						Role: BedrockMessageRoleAssistant,
						Content: []BedrockContentBlock{
							{
								Text: schemas.Ptr("Hello!"),
							},
						},
					},
				},
				Usage: &BedrockTokenUsage{
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
			input: &BedrockConverseResponse{
				StopReason: "end_turn",
				Output: &BedrockConverseOutput{
					Message: &BedrockMessage{
						Role: BedrockMessageRoleAssistant,
						Content: []BedrockContentBlock{
							{
								ToolUse: &BedrockToolUse{
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
			input: &BedrockConverseResponse{
				StopReason: "end_turn",
				Output: &BedrockConverseOutput{
					Message: &BedrockMessage{
						Role:    BedrockMessageRoleAssistant,
						Content: []BedrockContentBlock{},
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
				var bedrockResp *BedrockConverseResponse
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
				// Note: CreatedAt is set to current time, so we can't compare it exactly
				if actual != nil {
					assert.Greater(t, actual.CreatedAt, 0)
					actual.CreatedAt = tt.expected.CreatedAt
				}
				assert.Equal(t, tt.expected, actual)
			}
		})
	}
}
