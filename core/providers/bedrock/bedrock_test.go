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

// assertBedrockRequestEqual compares two BedrockConverseRequest objects
// but ignores the order of tools in ToolConfig
func assertBedrockRequestEqual(t *testing.T, expected, actual *bedrock.BedrockConverseRequest) {
	t.Helper()

	assert.Equal(t, expected.ModelID, actual.ModelID)
	assert.Equal(t, expected.Messages, actual.Messages)
	assert.Equal(t, expected.System, actual.System)
	assert.Equal(t, expected.InferenceConfig, actual.InferenceConfig)
	assert.Equal(t, expected.GuardrailConfig, actual.GuardrailConfig)
	assert.Equal(t, expected.AdditionalModelRequestFields, actual.AdditionalModelRequestFields)
	assert.Equal(t, expected.AdditionalModelResponseFieldPaths, actual.AdditionalModelResponseFieldPaths)
	assert.Equal(t, expected.PerformanceConfig, actual.PerformanceConfig)
	assert.Equal(t, expected.PromptVariables, actual.PromptVariables)
	assert.Equal(t, expected.RequestMetadata, actual.RequestMetadata)
	assert.Equal(t, expected.ServiceTier, actual.ServiceTier)
	assert.Equal(t, expected.Stream, actual.Stream)
	assert.Equal(t, expected.ExtraParams, actual.ExtraParams)
	assert.Equal(t, expected.Fallbacks, actual.Fallbacks)

	if expected.ToolConfig == nil {
		assert.Nil(t, actual.ToolConfig)
		return
	}

	require.NotNil(t, actual.ToolConfig)
	assert.Equal(t, expected.ToolConfig.ToolChoice, actual.ToolConfig.ToolChoice)

	expectedTools := expected.ToolConfig.Tools
	actualTools := actual.ToolConfig.Tools

	assert.Equal(t, len(expectedTools), len(actualTools), "Tool count mismatch")

	expectedToolMap := make(map[string]bedrock.BedrockTool)
	for _, tool := range expectedTools {
		if tool.ToolSpec != nil {
			expectedToolMap[tool.ToolSpec.Name] = tool
		}
	}

	actualToolMap := make(map[string]bedrock.BedrockTool)
	for _, tool := range actualTools {
		if tool.ToolSpec != nil {
			actualToolMap[tool.ToolSpec.Name] = tool
		}
	}

	for name, expectedTool := range expectedToolMap {
		actualTool, exists := actualToolMap[name]
		assert.True(t, exists, "Tool %s not found in actual tools", name)
		if exists {
			assert.Equal(t, expectedTool, actualTool, "Tool %s differs", name)
		}
	}
}

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

	// Get Bedrock-specific configuration from environment
	s3Bucket := os.Getenv("AWS_S3_BUCKET")
	roleArn := os.Getenv("AWS_BEDROCK_ROLE_ARN")

	// Build extra params for batch and file operations
	var batchExtraParams map[string]interface{}
	var fileExtraParams map[string]interface{}

	if s3Bucket != "" {
		fileExtraParams = map[string]interface{}{
			"s3_bucket": s3Bucket,
		}
		batchExtraParams = map[string]interface{}{
			"output_s3_uri": "s3://" + s3Bucket + "/batch-output/",
		}
		if roleArn != "" {
			batchExtraParams["role_arn"] = roleArn
		}
	}

	testConfig := testutil.ComprehensiveTestConfig{
		Provider:    schemas.Bedrock,
		ChatModel:   "claude-4-sonnet",
		VisionModel: "claude-4-sonnet",
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.Bedrock, Model: "claude-4-sonnet"},
			{Provider: schemas.Bedrock, Model: "claude-4.5-sonnet"},
		},
		EmbeddingModel:     "cohere.embed-v4:0",
		ReasoningModel:     "claude-4.5-sonnet",
		PromptCachingModel: "claude-4.5-sonnet",
		BatchExtraParams:   batchExtraParams,
		FileExtraParams:    fileExtraParams,
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
			FileBase64:            true,
			FileURL:               false, // S3 urls supported for nova models
			CompleteEnd2End:       true,
			Embedding:             true,
			ListModels:            true,
			Reasoning:             true,
			PromptCaching:         true,
			BatchCreate:           true,
			BatchList:             true,
			BatchRetrieve:         true,
			BatchCancel:           true,
			BatchResults:          true,
			FileUpload:            true,
			FileList:              true,
			FileRetrieve:          true,
			FileDelete:            true,
			FileContent:           true,
			FileBatchInput:        true,
			CountTokens:           false, // Not supported
			StructuredOutputs:     true,
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
			name: "ParallelToolCalls",
			input: &schemas.BifrostChatRequest{
				Model: "claude-3-sonnet",
				Input: []schemas.ChatMessage{
					{
						Role: schemas.ChatMessageRoleUser,
						Content: &schemas.ChatMessageContent{
							ContentStr: schemas.Ptr("Invoke all tools in parallel that are available to you"),
						},
					},
					{
						Role: schemas.ChatMessageRoleAssistant,
						Content: &schemas.ChatMessageContent{
							ContentStr: schemas.Ptr("I'll invoke both available tools in parallel for you."),
						},
						ChatAssistantMessage: &schemas.ChatAssistantMessage{
							ToolCalls: []schemas.ChatAssistantMessageToolCall{
								{
									Index: 0,
									Type:  schemas.Ptr("function"),
									ID:    schemas.Ptr("tooluse_Yl388l8ES0G_3TQtDcKq_g"),
									Function: schemas.ChatAssistantMessageToolCallFunction{
										Name:      schemas.Ptr("hello"),
										Arguments: "{}",
									},
								},
								{
									Index: 1,
									Type:  schemas.Ptr("function"),
									ID:    schemas.Ptr("tooluse_eARDw2iqRXak8uyRC2KxXw"),
									Function: schemas.ChatAssistantMessageToolCallFunction{
										Name:      schemas.Ptr("world"),
										Arguments: "{}",
									},
								},
							},
						},
					},
					{
						Role: schemas.ChatMessageRoleTool,
						Content: &schemas.ChatMessageContent{
							ContentStr: schemas.Ptr("Hello"),
						},
						ChatToolMessage: &schemas.ChatToolMessage{
							ToolCallID: schemas.Ptr("tooluse_Yl388l8ES0G_3TQtDcKq_g"),
						},
					},
					{
						Role: schemas.ChatMessageRoleTool,
						Content: &schemas.ChatMessageContent{
							ContentStr: schemas.Ptr("World"),
						},
						ChatToolMessage: &schemas.ChatToolMessage{
							ToolCallID: schemas.Ptr("tooluse_eARDw2iqRXak8uyRC2KxXw"),
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
								Text: schemas.Ptr("Invoke all tools in parallel that are available to you"),
							},
						},
					},
					{
						Role: bedrock.BedrockMessageRoleAssistant,
						Content: []bedrock.BedrockContentBlock{
							{
								Text: schemas.Ptr("I'll invoke both available tools in parallel for you."),
							},
							{
								ToolUse: &bedrock.BedrockToolUse{
									ToolUseID: "tooluse_Yl388l8ES0G_3TQtDcKq_g",
									Name:      "hello",
									Input:     map[string]interface{}{},
								},
							},
							{
								ToolUse: &bedrock.BedrockToolUse{
									ToolUseID: "tooluse_eARDw2iqRXak8uyRC2KxXw",
									Name:      "world",
									Input:     map[string]interface{}{},
								},
							},
						},
					},
					{
						Role: bedrock.BedrockMessageRoleUser,
						Content: []bedrock.BedrockContentBlock{
							{
								ToolResult: &bedrock.BedrockToolResult{
									ToolUseID: "tooluse_Yl388l8ES0G_3TQtDcKq_g",
									Content: []bedrock.BedrockContentBlock{
										{
											Text: schemas.Ptr("Hello"),
										},
									},
									Status: schemas.Ptr("success"),
								},
							},
							{
								ToolResult: &bedrock.BedrockToolResult{
									ToolUseID: "tooluse_eARDw2iqRXak8uyRC2KxXw",
									Content: []bedrock.BedrockContentBlock{
										{
											Text: schemas.Ptr("World"),
										},
									},
									Status: schemas.Ptr("success"),
								},
							},
						},
					},
				},
				ToolConfig: &bedrock.BedrockToolConfig{
					Tools: []bedrock.BedrockTool{
						{
							ToolSpec: &bedrock.BedrockToolSpec{
								Name:        "hello",
								Description: schemas.Ptr("Tool extracted from conversation history"),
								InputSchema: bedrock.BedrockToolInputSchema{
									JSON: map[string]interface{}{
										"type":       "object",
										"properties": map[string]interface{}{},
									},
								},
							},
						},
						{
							ToolSpec: &bedrock.BedrockToolSpec{
								Name:        "world",
								Description: schemas.Ptr("Tool extracted from conversation history"),
								InputSchema: bedrock.BedrockToolInputSchema{
									JSON: map[string]interface{}{
										"type":       "object",
										"properties": map[string]interface{}{},
									},
								},
							},
						},
					},
				},
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
		{
			name: "ArrayToolMessage",
			input: &schemas.BifrostChatRequest{
				Model: "claude-3-sonnet",
				Input: []schemas.ChatMessage{
					{
						Role: schemas.ChatMessageRoleUser,
						Content: &schemas.ChatMessageContent{
							ContentStr: schemas.Ptr("What's the weather like in New York?"),
						},
					},
					{
						Role: schemas.ChatMessageRoleAssistant,
						Content: &schemas.ChatMessageContent{
							ContentStr: schemas.Ptr("I'll invoke get_weather tool to know the weather in New York."),
						},
						ChatAssistantMessage: &schemas.ChatAssistantMessage{
							ToolCalls: []schemas.ChatAssistantMessageToolCall{
								{
									Index: 0,
									Type:  schemas.Ptr("function"),
									ID:    schemas.Ptr("tooluse_Yl388l8ES0G_3TQtDcKq_g"),
									Function: schemas.ChatAssistantMessageToolCallFunction{
										Name:      schemas.Ptr("get_weather"),
										Arguments: `{"location":"New York"}`,
									},
								},
							},
						},
					},
					{
						Role: schemas.ChatMessageRoleTool,
						Content: &schemas.ChatMessageContent{
							ContentStr: schemas.Ptr(`[{"period":"now","weather":"sunny"},{"period":"next_1_hour","weather":"cloudy"}]`),
						},
						ChatToolMessage: &schemas.ChatToolMessage{
							ToolCallID: schemas.Ptr("tooluse_Yl388l8ES0G_3TQtDcKq_g"),
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
								Text: schemas.Ptr("What's the weather like in New York?"),
							},
						},
					},
					{
						Role: bedrock.BedrockMessageRoleAssistant,
						Content: []bedrock.BedrockContentBlock{
							{
								Text: schemas.Ptr("I'll invoke get_weather tool to know the weather in New York."),
							},
							{
								ToolUse: &bedrock.BedrockToolUse{
									ToolUseID: "tooluse_Yl388l8ES0G_3TQtDcKq_g",
									Name:      "get_weather",
									Input: map[string]any{
										"location": "New York",
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
									ToolUseID: "tooluse_Yl388l8ES0G_3TQtDcKq_g",
									Content: []bedrock.BedrockContentBlock{
										{
											JSON: map[string]any{
												"results": []any{
													any(map[string]any{"period": "now", "weather": "sunny"}),
													any(map[string]any{"period": "next_1_hour", "weather": "cloudy"}),
												},
											},
										},
									},
									Status: schemas.Ptr("success"),
								},
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			actual, err := bedrock.ToBedrockChatCompletionRequest(ctx, tt.input)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, actual)
				if tt.input == nil {
					assert.Contains(t, err.Error(), "nil")
				}
			} else {
				require.NoError(t, err)
				if tt.name == "ParallelToolCalls" {
					assertBedrockRequestEqual(t, tt.expected, actual)
				} else {
					assert.Equal(t, tt.expected, actual)
				}
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
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

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
						Type:   schemas.Ptr(schemas.ResponsesMessageTypeMessage),
						Role:   schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
						Status: schemas.Ptr("completed"),
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
						Type:   schemas.Ptr(schemas.ResponsesMessageTypeMessage),
						Role:   schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
						Status: schemas.Ptr("completed"),
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
						Type:   schemas.Ptr(schemas.ResponsesMessageTypeMessage),
						Role:   schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
						Status: schemas.Ptr("completed"),
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
						Type:   schemas.Ptr(schemas.ResponsesMessageTypeMessage),
						Role:   schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
						Status: schemas.Ptr("completed"),
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
						Type:   schemas.Ptr(schemas.ResponsesMessageTypeMessage),
						Role:   schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
						Status: schemas.Ptr("completed"),
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
						Type:   schemas.Ptr(schemas.ResponsesMessageTypeMessage),
						Role:   schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
						Status: schemas.Ptr("completed"),
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
				actual, err = bedrockReq.ToBifrostResponsesRequest(ctx)
			} else {
				actual, err = tt.input.ToBifrostResponsesRequest(ctx)
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
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

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
						Type:   schemas.Ptr(schemas.ResponsesMessageTypeMessage),
						Role:   schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
						Status: schemas.Ptr("completed"),
						Content: &schemas.ResponsesMessageContent{
							ContentBlocks: []schemas.ResponsesMessageContentBlock{
								{
									Type: schemas.ResponsesOutputMessageContentTypeText,
									Text: schemas.Ptr("Hello, world!"),
									ResponsesOutputMessageContentText: &schemas.ResponsesOutputMessageContentText{
										Annotations: []schemas.ResponsesOutputMessageContentTextAnnotation{},
										LogProbs:    []schemas.ResponsesOutputMessageContentTextLogProb{},
									},
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
						Type:   schemas.Ptr(schemas.ResponsesMessageTypeMessage),
						Role:   schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
						Status: schemas.Ptr("completed"),
						Content: &schemas.ResponsesMessageContent{
							ContentBlocks: []schemas.ResponsesMessageContentBlock{
								{
									Type: schemas.ResponsesOutputMessageContentTypeText,
									Text: schemas.Ptr("Hello!"),
									ResponsesOutputMessageContentText: &schemas.ResponsesOutputMessageContentText{
										Annotations: []schemas.ResponsesOutputMessageContentTextAnnotation{},
										LogProbs:    []schemas.ResponsesOutputMessageContentTextLogProb{},
									},
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
				actual, err = bedrockResp.ToBifrostResponsesResponse(ctx)
			} else {
				actual, err = tt.input.ToBifrostResponsesResponse(ctx)
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

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	bedrockReq, err := bedrock.ToBedrockResponsesRequest(ctx, req)
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

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	bedrockReq, err := bedrock.ToBedrockResponsesRequest(ctx, req)
	require.NoError(t, err)
	require.NotNil(t, bedrockReq)

	assert.Equal(t, []string{"/amazon-bedrock-invocationMetrics/inputTokenCount"}, bedrockReq.AdditionalModelResponseFieldPaths)
}

// TestConvertBifrostResponsesMessageContentBlocksToBedrockContentBlocks_EmptyBlocks tests that
// empty ContentBlocks are not created when required fields are missing, preventing the Bedrock API error:
// "ContentBlock object at messages.1.content.0 must set one of the following keys: text, image, toolUse, toolResult, document, video, cachePoint, reasoningContent, citationsContent, searchResult."
func TestConvertBifrostResponsesMessageContentBlocksToBedrockContentBlocks_EmptyBlocks(t *testing.T) {
	tests := []struct {
		name           string
		input          *schemas.BifrostResponsesResponse
		expectedBlocks int // Expected number of ContentBlocks in the output
		description    string
	}{
		{
			name: "ImageBlockWithNilImageURL_ShouldNotCreateEmptyBlock",
			input: &schemas.BifrostResponsesResponse{
				CreatedAt: 1234567890,
				Output: []schemas.ResponsesMessage{
					{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
						Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
						Content: &schemas.ResponsesMessageContent{
							ContentBlocks: []schemas.ResponsesMessageContentBlock{
								{
									Type: schemas.ResponsesInputMessageContentBlockTypeImage,
									ResponsesInputMessageContentBlockImage: &schemas.ResponsesInputMessageContentBlockImage{
										ImageURL: nil, // Missing ImageURL - should not create empty block
									},
								},
							},
						},
					},
				},
			},
			expectedBlocks: 0,
			description:    "Image block with nil ImageURL should not create an empty ContentBlock",
		},
		{
			name: "ImageBlockWithNilImageBlock_ShouldNotCreateEmptyBlock",
			input: &schemas.BifrostResponsesResponse{
				CreatedAt: 1234567890,
				Output: []schemas.ResponsesMessage{
					{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
						Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
						Content: &schemas.ResponsesMessageContent{
							ContentBlocks: []schemas.ResponsesMessageContentBlock{
								{
									Type:                                   schemas.ResponsesInputMessageContentBlockTypeImage,
									ResponsesInputMessageContentBlockImage: nil, // Missing image block - should not create empty block
								},
							},
						},
					},
				},
			},
			expectedBlocks: 0,
			description:    "Image block with nil ResponsesInputMessageContentBlockImage should not create an empty ContentBlock",
		},
		{
			name: "ReasoningBlockWithNilText_ShouldNotCreateEmptyBlock",
			input: &schemas.BifrostResponsesResponse{
				CreatedAt: 1234567890,
				Output: []schemas.ResponsesMessage{
					{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
						Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
						Content: &schemas.ResponsesMessageContent{
							ContentBlocks: []schemas.ResponsesMessageContentBlock{
								{
									Type: schemas.ResponsesOutputMessageContentTypeReasoning,
									Text: nil, // Missing Text - should not create empty block
								},
							},
						},
					},
				},
			},
			expectedBlocks: 0,
			description:    "Reasoning block with nil Text should not create an empty ContentBlock",
		},
		{
			name: "FileBlockWithNilFileData_ShouldNotCreateEmptyBlock",
			input: &schemas.BifrostResponsesResponse{
				CreatedAt: 1234567890,
				Output: []schemas.ResponsesMessage{
					{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
						Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
						Content: &schemas.ResponsesMessageContent{
							ContentBlocks: []schemas.ResponsesMessageContentBlock{
								{
									Type: schemas.ResponsesInputMessageContentBlockTypeFile,
									ResponsesInputMessageContentBlockFile: &schemas.ResponsesInputMessageContentBlockFile{
										FileData: nil, // Missing FileData - should not create empty block
										Filename: schemas.Ptr("test.pdf"),
										FileType: schemas.Ptr("application/pdf"),
									},
								},
							},
						},
					},
				},
			},
			expectedBlocks: 0,
			description:    "File block with nil FileData should not create an empty ContentBlock",
		},
		{
			name: "FileBlockWithNilFileBlock_ShouldNotCreateEmptyBlock",
			input: &schemas.BifrostResponsesResponse{
				CreatedAt: 1234567890,
				Output: []schemas.ResponsesMessage{
					{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
						Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
						Content: &schemas.ResponsesMessageContent{
							ContentBlocks: []schemas.ResponsesMessageContentBlock{
								{
									Type:                                  schemas.ResponsesInputMessageContentBlockTypeFile,
									ResponsesInputMessageContentBlockFile: nil, // Missing file block - should not create empty block
								},
							},
						},
					},
				},
			},
			expectedBlocks: 0,
			description:    "File block with nil ResponsesInputMessageContentBlockFile should not create an empty ContentBlock",
		},
		{
			name: "ValidTextBlock_ShouldCreateBlock",
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
									Text: schemas.Ptr("Valid text content"),
								},
							},
						},
					},
				},
			},
			expectedBlocks: 1,
			description:    "Valid text block should create a ContentBlock",
		},
		{
			name: "ValidReasoningBlock_ShouldCreateBlock",
			input: &schemas.BifrostResponsesResponse{
				CreatedAt: 1234567890,
				Output: []schemas.ResponsesMessage{
					{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
						Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
						Content: &schemas.ResponsesMessageContent{
							ContentBlocks: []schemas.ResponsesMessageContentBlock{
								{
									Type: schemas.ResponsesOutputMessageContentTypeReasoning,
									Text: schemas.Ptr("Valid reasoning content"),
								},
							},
						},
					},
				},
			},
			expectedBlocks: 1,
			description:    "Valid reasoning block should create a ContentBlock",
		},
		{
			name: "ValidFileBlock_ShouldCreateBlock",
			input: &schemas.BifrostResponsesResponse{
				CreatedAt: 1234567890,
				Output: []schemas.ResponsesMessage{
					{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
						Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
						Content: &schemas.ResponsesMessageContent{
							ContentBlocks: []schemas.ResponsesMessageContentBlock{
								{
									Type: schemas.ResponsesInputMessageContentBlockTypeFile,
									ResponsesInputMessageContentBlockFile: &schemas.ResponsesInputMessageContentBlockFile{
										FileData: schemas.Ptr("dGVzdCBmaWxlIGRhdGE="), // base64 encoded "test file data"
										Filename: schemas.Ptr("test.pdf"),
										FileType: schemas.Ptr("application/pdf"),
									},
								},
							},
						},
					},
				},
			},
			expectedBlocks: 1,
			description:    "Valid file block should create a ContentBlock",
		},
		{
			name: "MixedValidAndInvalidBlocks_ShouldOnlyCreateValidBlocks",
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
									Text: schemas.Ptr("Valid text"),
								},
								{
									Type:                                   schemas.ResponsesInputMessageContentBlockTypeImage,
									ResponsesInputMessageContentBlockImage: nil, // Invalid - should be skipped
								},
								{
									Type: schemas.ResponsesOutputMessageContentTypeReasoning,
									Text: schemas.Ptr("Valid reasoning"),
								},
								{
									Type: schemas.ResponsesInputMessageContentBlockTypeFile,
									ResponsesInputMessageContentBlockFile: &schemas.ResponsesInputMessageContentBlockFile{
										FileData: nil, // Invalid - should be skipped
									},
								},
							},
						},
					},
				},
			},
			expectedBlocks: 2, // Only valid text and reasoning blocks
			description:    "Mixed valid and invalid blocks should only create valid ContentBlocks",
		},
		{
			name: "CacheControlBlock_ShouldCreateCachePointBlock",
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
									Text: schemas.Ptr("Text with cache control"),
									CacheControl: &schemas.CacheControl{
										Type: schemas.CacheControlTypeEphemeral,
									},
								},
							},
						},
					},
				},
			},
			expectedBlocks: 2, // Text block + CachePoint block
			description:    "ContentBlock with CacheControl should create both content and CachePoint blocks",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual, err := bedrock.ToBedrockConverseResponse(tt.input)
			require.NoError(t, err, "Conversion should not error")
			require.NotNil(t, actual, "Response should not be nil")
			require.NotNil(t, actual.Output, "Output should not be nil")
			require.NotNil(t, actual.Output.Message, "Message should not be nil")

			actualBlocks := len(actual.Output.Message.Content)
			assert.Equal(t, tt.expectedBlocks, actualBlocks, tt.description)

			// Verify that all created blocks have at least one required field set
			for i, block := range actual.Output.Message.Content {
				hasRequiredField := block.Text != nil ||
					block.Image != nil ||
					block.Document != nil ||
					block.ToolUse != nil ||
					block.ToolResult != nil ||
					block.ReasoningContent != nil ||
					block.CachePoint != nil ||
					block.JSON != nil ||
					block.GuardContent != nil

				assert.True(t, hasRequiredField,
					"ContentBlock at index %d must have at least one required field set (text, image, toolUse, toolResult, document, video, cachePoint, reasoningContent, citationsContent, searchResult)",
					i)
			}
		})
	}
}
