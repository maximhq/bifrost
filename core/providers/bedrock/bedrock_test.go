package bedrock_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/internal/llmtests"
	"github.com/maximhq/bifrost/core/providers/anthropic"
	"github.com/maximhq/bifrost/core/providers/bedrock"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustMarshalJSON(v interface{}) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic("mustMarshalJSON: " + err.Error())
	}
	return json.RawMessage(b)
}

// jsonEqual compares two json.RawMessage values semantically (ignoring key order).
func jsonEqual(t *testing.T, expected, actual json.RawMessage, msgAndArgs ...interface{}) {
	t.Helper()
	if expected == nil && actual == nil {
		return
	}
	var e, a interface{}
	if err := json.Unmarshal(expected, &e); err != nil {
		t.Errorf("failed to unmarshal expected JSON: %v", err)
		return
	}
	if err := json.Unmarshal(actual, &a); err != nil {
		t.Errorf("failed to unmarshal actual JSON: %v", err)
		return
	}
	assert.Equal(t, e, a, msgAndArgs...)
}

// mustMarshalToolParams marshals ToolFunctionParameters to json.RawMessage,
// matching the conversion code path for deterministic output.
func mustMarshalToolParams(params *schemas.ToolFunctionParameters) json.RawMessage {
	b, err := json.Marshal(params)
	if err != nil {
		panic("mustMarshalToolParams: " + err.Error())
	}
	return json.RawMessage(b)
}

// Common test variables
var (
	testMaxTokens = 100
	testTemp      = 0.7
	testTopP      = 0.9
	testStop      = []string{"STOP"}
	testTrace     = "enabled"
	testLatency   = "optimized"
	testProps     = *schemas.NewOrderedMapFromPairs(
		schemas.KV("location", map[string]interface{}{
			"type":        "string",
			"description": "The city name",
		}),
	)
	// testPropsFromJSON is the same as testProps but with nested values as *OrderedMap
	// (as produced by json.Unmarshal -> OrderedMap.UnmarshalJSON)
	testPropsFromJSON = *schemas.NewOrderedMapFromPairs(
		schemas.KV("location", schemas.NewOrderedMapFromPairs(
			schemas.KV("type", "string"),
			schemas.KV("description", "The city name"),
		)),
	)
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
			// Compare tool specs field-by-field, using JSON-semantic comparison
			// for InputSchema to handle key ordering differences from sorted marshaling
			if expectedTool.ToolSpec != nil && actualTool.ToolSpec != nil {
				assert.Equal(t, expectedTool.ToolSpec.Name, actualTool.ToolSpec.Name, "Tool %s name differs", name)
				assert.Equal(t, expectedTool.ToolSpec.Description, actualTool.ToolSpec.Description, "Tool %s description differs", name)
				jsonEqual(t, expectedTool.ToolSpec.InputSchema.JSON, actualTool.ToolSpec.InputSchema.JSON, "Tool %s input schema differs", name)
			} else {
				assert.Equal(t, expectedTool, actualTool, "Tool %s differs", name)
			}
		}
	}
}

func TestBedrock(t *testing.T) {
	t.Parallel()

	if strings.TrimSpace(os.Getenv("AWS_ACCESS_KEY_ID")) == "" || strings.TrimSpace(os.Getenv("AWS_SECRET_ACCESS_KEY")) == "" {
		t.Skip("Skipping Bedrock tests because AWS_ACCESS_KEY_ID or AWS_SECRET_ACCESS_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	// Get Bedrock-specific configuration from environment
	s3Bucket := os.Getenv("AWS_S3_BUCKET")
	roleArn := os.Getenv("AWS_BEDROCK_ROLE_ARN")
	rerankModelARN := strings.TrimSpace(os.Getenv("AWS_BEDROCK_RERANK_MODEL_ARN"))

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

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:    schemas.Bedrock,
		ChatModel:   "claude-4.6-sonnet",
		VisionModel: "claude-4.6-sonnet",
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.Bedrock, Model: "claude-4-sonnet"},
			{Provider: schemas.Bedrock, Model: "claude-4.5-sonnet"},
		},
		EmbeddingModel:           "cohere.embed-v4:0",
		RerankModel:              rerankModelARN,
		ReasoningModel:           "claude-4.5-sonnet",
		PromptCachingModel:       "claude-4.5-sonnet",
		ImageEditModel:           "amazon.nova-canvas-v1:0",
		ImageVariationModel:      "amazon.nova-canvas-v1:0",
		InterleavedThinkingModel: "claude-opus-4-5",
		BatchExtraParams:         batchExtraParams,
		FileExtraParams:          fileExtraParams,
		Scenarios: llmtests.TestScenarios{
			TextCompletion:             false, // Not supported
			SimpleChat:                 true,
			CompletionStream:           true,
			MultiTurnConversation:      true,
			ToolCalls:                  true,
			ToolCallsStreaming:         true,
			MultipleToolCalls:          true,
			MultipleToolCallsStreaming: true,
			End2EndToolCalling:         true,
			AutomaticFunctionCall:      true,
			ImageURL:                   false, // Bedrock doesn't support image URL
			ImageBase64:                true,
			MultipleImages:             false, // Since one of the image is URL
			FileBase64:                 true,
			FileURL:                    false, // S3 urls supported for nova models
			CompleteEnd2End:            true,
			Embedding:                  true,
			Rerank:                     rerankModelARN != "",
			ListModels:                 true,
			Reasoning:                  true,
			PromptCaching:              true,
			BatchCreate:                true,
			BatchList:                  true,
			BatchRetrieve:              true,
			BatchCancel:                true,
			BatchResults:               true,
			FileUpload:                 true,
			FileList:                   true,
			FileRetrieve:               true,
			FileDelete:                 true,
			FileContent:                true,
			FileBatchInput:             true,
			CountTokens:                true,
			ImageEdit:                  true,
			ImageVariation:             true,
			StructuredOutputs:          true,
			InterleavedThinking:        true,
			EagerInputStreaming:        true, // fine-grained-tool-streaming-2025-05-14 (per B-header)
			// ServerToolsViaOpenAIEndpoint: Bedrock does not support web_search / web_fetch /
			// code_execution server tools per Table 20, so no cases would run. Left off.
		},
	}

	t.Run("BedrockTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})

	// BedrockOpus47Tests subtree: live end-to-end repro of the user-reported
	// regression on Claude Opus 4.7. GA structured outputs (output_config.format
	// with json_schema) against Opus 4.7 on Bedrock currently fails with
	// `output_config.format: Extra inputs are not permitted` after PR #3053
	// (commit 7df13ab45) tunneled `anthropic_beta: ["structured-outputs-2025-11-13"]`
	// into additionalModelRequestFields.
	//
	// This subtree reuses the existing structured-output scenarios from
	// core/internal/llmtests (RunStructuredOutputChatTest +
	// RunStructuredOutputResponsesTest) so we exercise the SAME wire path the
	// user's snippet (`client.messages.create(... output_config={"format":...})`)
	// takes: Anthropic SDK -> /v1/messages -> ToBifrostResponsesRequest ->
	// ToBedrockResponsesRequest.
	//
	// Naming places the leaf test at
	//   TestBedrock/BedrockOpus47Tests/TestBedrockOpus47StructuredOutputRegression
	// so the Makefile's TESTCASE convention works:
	//   make test-core PROVIDER=bedrock TESTCASE=TestBedrockOpus47StructuredOutputRegression
	//
	// Skipped unless BEDROCK_OPUS_47_MODEL_ID is set to the exact Bedrock model
	// id (or alias) for Claude Opus 4.7. We don't default this because per
	// Anthropic's docs
	// (cite: https://platform.claude.com/docs/en/docs/build-with-claude/structured-outputs)
	// "Claude Opus 4.7 ... [is] available through Claude in Amazon Bedrock
	// (the Messages-API Bedrock endpoint)" - i.e. not Converse - and the exact
	// inference-profile id depends on the caller's Bedrock entitlements.
	t.Run("BedrockOpus47Tests", func(t *testing.T) {
		t.Run("TestBedrockOpus47StructuredOutputRegression", func(t *testing.T) {
			modelID := strings.TrimSpace(os.Getenv("BEDROCK_OPUS_47_MODEL_ID"))
			if modelID == "" {
				t.Skip("Skipping Bedrock Opus 4.7 repro because BEDROCK_OPUS_47_MODEL_ID is not set (e.g. 'anthropic.claude-opus-4-7' or the inference-profile id you have entitlements for)")
			}
			t.Logf("Running Opus 4.7 structured-output repro against Bedrock model id: %s", modelID)

			// Mirror the user's failing Python snippet exactly:
			//   - Anthropic SDK call with system as a structured array (text block
			//     + cache_control: ephemeral)
			//   - user content as an array of text blocks
			//   - max_tokens: 4096
			//   - output_config.format with json_schema and anyOf-style nullable
			//     fields (`{"anyOf":[{"type":"string"},{"type":"null"}]}`)
			//   - NO outer `anthropic-beta` HTTP header (the SDK does not auto-set
			//     it for GA output_config; the existing llmtests scenarios DO set
			//     it, which is why those scenarios pass on Opus 4.7 even today)
			outputFormatJSON := json.RawMessage(`{
				"type": "json_schema",
				"schema": {
					"type": "object",
					"properties": {
						"isNewTopic": {"type": "boolean"},
						"title":      {"anyOf": [{"type": "string"}, {"type": "null"}]},
						"result":     {"anyOf": [{"type": "number"}, {"type": "null"}]}
					},
					"required": ["isNewTopic", "title", "result"],
					"additionalProperties": false
				}
			}`)

			anthropicReq := &anthropic.AnthropicMessageRequest{
				Model:     modelID,
				MaxTokens: 4096,
				System: &anthropic.AnthropicContent{
					ContentBlocks: []anthropic.AnthropicContentBlock{
						{
							Type:         anthropic.AnthropicContentBlockTypeText,
							Text:         schemas.Ptr("You are an AI assistant. Analyze the user's message and respond with structured JSON."),
							CacheControl: &schemas.CacheControl{Type: "ephemeral"},
						},
					},
				},
				Messages: []anthropic.AnthropicMessage{
					{
						Role: anthropic.AnthropicMessageRoleUser,
						Content: anthropic.AnthropicContent{
							ContentBlocks: []anthropic.AnthropicContentBlock{
								{
									Type: anthropic.AnthropicContentBlockTypeText,
									Text: schemas.Ptr("Hello, what's the result of 678*132?"),
								},
							},
						},
					},
				},
				OutputConfig: &anthropic.AnthropicOutputConfig{
					Format: outputFormatJSON,
				},
			}

			// Convert via the SAME entry point the HTTP integration uses
			// (transports/bifrost-http/integrations/anthropic.go RequestConverter
			// at lines 92-100 calls anthropicReq.ToBifrostResponsesRequest(ctx)).
			reqCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
			bifrostReq := anthropicReq.ToBifrostResponsesRequest(reqCtx)
			require.NotNil(t, bifrostReq, "ToBifrostResponsesRequest returned nil")
			bifrostReq.Provider = schemas.Bedrock
			bifrostReq.Model = modelID

			// Send. NO BifrostContextKeyExtraHeaders — this is the key delta
			// from llmtests.RunStructuredOutputResponsesTest (which sets
			// `anthropic-beta: structured-outputs-2025-11-13` outer header
			// at structured_outputs.go:411-418, masking the regression).
			resp, bifrostErr := client.ResponsesRequest(reqCtx, bifrostReq)

			if bifrostErr != nil {
				// Repro hit. Surface the full error for the user to confirm
				// it matches the reported "output_config.format: Extra inputs
				// are not permitted" Bedrock validator response.
				t.Fatalf("Bedrock Opus 4.7 structured-output request failed (this is the regression repro): %s", llmtests.GetErrorMessage(bifrostErr))
			}
			require.NotNil(t, resp, "expected non-nil response when error is nil")
			t.Logf("Bedrock Opus 4.7 structured-output request SUCCEEDED. Response id=%v", resp.ID)
		})
	})
}

// TestBifrostToBedrockRequestConversion tests the conversion from Bifrost request to Bedrock request
func TestBifrostToBedrockRequestConversion(t *testing.T) {
	maxTokens := testMaxTokens
	temp := testTemp
	topP := testTopP
	stop := testStop
	trace := testTrace
	latency := testLatency
	serviceTier := schemas.BifrostServiceTierPriority
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
					Type: bedrock.BedrockServiceTierTypePriority,
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
									Properties: schemas.NewOrderedMapFromPairs(
										schemas.KV("location", map[string]interface{}{
											"type":        "string",
											"description": "The city name",
										}),
									),
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
									JSON: mustMarshalToolParams(&schemas.ToolFunctionParameters{
										Type:       "object",
										Properties: &props,
										Required:   []string{"location"},
									}),
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
				AdditionalModelRequestFields: schemas.NewOrderedMapFromPairs(
					schemas.KV("customField", "customValue"),
				),
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
									Input:     json.RawMessage("{}"),
								},
							},
							{
								ToolUse: &bedrock.BedrockToolUse{
									ToolUseID: "tooluse_eARDw2iqRXak8uyRC2KxXw",
									Name:      "world",
									Input:     json.RawMessage("{}"),
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
									JSON: mustMarshalJSON(map[string]interface{}{
										"type":       "object",
										"properties": map[string]interface{}{},
									}),
								},
							},
						},
						{
							ToolSpec: &bedrock.BedrockToolSpec{
								Name:        "world",
								Description: schemas.Ptr("Tool extracted from conversation history"),
								InputSchema: bedrock.BedrockToolInputSchema{
									JSON: mustMarshalJSON(map[string]interface{}{
										"type":       "object",
										"properties": map[string]interface{}{},
									}),
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
									Properties: schemas.NewOrderedMapFromPairs(
										schemas.KV("location", map[string]interface{}{
											"type":        "string",
											"description": "The city name",
										}),
									),
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
									Input:     json.RawMessage(`{"location":"New York"}`),
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
											JSON: mustMarshalJSON(map[string]any{
												"results": []any{
													any(map[string]any{"period": "now", "weather": "sunny"}),
													any(map[string]any{"period": "next_1_hour", "weather": "cloudy"}),
												},
											}),
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
									JSON: mustMarshalToolParams(&schemas.ToolFunctionParameters{
										Type:       "object",
										Properties: &props,
										Required:   []string{"location"},
									}),
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
				assertBedrockRequestEqual(t, tt.expected, actual)
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
	_ = props // used in input construction

	// Build expected params via JSON round-trip so keyOrder and nested OrderedMap match
	expectedParamsJSON := mustMarshalJSON(map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"location": map[string]interface{}{
				"type":        "string",
				"description": "The city name",
			},
		},
		"required": []string{"location"},
	})
	var expectedParams schemas.ToolFunctionParameters
	_ = json.Unmarshal(expectedParamsJSON, &expectedParams)

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
									JSON: mustMarshalJSON(map[string]interface{}{
										"type": "object",
										"properties": map[string]interface{}{
											"location": map[string]interface{}{
												"type":        "string",
												"description": "The city name",
											},
										},
										"required": []string{"location"},
									}),
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
								Parameters: &expectedParams,
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
				AdditionalModelRequestFields: schemas.NewOrderedMapFromPairs(
					schemas.KV("customField", "customValue"),
				),
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
						"additionalModelRequestFieldPaths": schemas.NewOrderedMapFromPairs(
							schemas.KV("customField", "customValue"),
						),
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
									Input:     json.RawMessage(`{"location":"NYC"}`),
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
									Input:     json.RawMessage(`{"expression":"2+2"}`),
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
									Input:     json.RawMessage(`{"location":"NYC"}`),
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
									Input:     json.RawMessage("invalid json {"), // Should fallback to raw string
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
									Input:     json.RawMessage("{}"), // Should default to empty map
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
											JSON: mustMarshalJSON(map[string]interface{}{
												"temperature": float64(72),
												"location":    "NYC",
											}),
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
									Input:     json.RawMessage(`{"location":"NYC"}`),
								},
							},
							{
								ToolResult: &bedrock.BedrockToolResult{
									ToolUseID: "call-111",
									Status:    schemas.Ptr("success"),
									Content: []bedrock.BedrockContentBlock{
										{
											JSON: mustMarshalJSON(map[string]interface{}{
												"temperature": float64(72),
											}),
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
									Input:     json.RawMessage(`{"location":"NYC"}`),
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
	toolInput := json.RawMessage(`{"location":"NYC"}`)
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
							Arguments: schemas.Ptr(string(toolInput)),
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
	actualFields := bedrockReq.AdditionalModelRequestFields.ToMap()
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

// TestToBedrockResponsesRequest_GuardrailConfig verifies that guardrailConfig in ExtraParams
// is extracted into BedrockConverseRequest.GuardrailConfig (Responses path fix).
func TestToBedrockResponsesRequest_GuardrailConfig(t *testing.T) {
	trace := testTrace
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	req := &schemas.BifrostResponsesRequest{
		Model: "bedrock/anthropic.claude-3-sonnet-20240229-v1:0",
		Params: &schemas.ResponsesParameters{
			ExtraParams: map[string]interface{}{
				"guardrailConfig": map[string]interface{}{
					"guardrailIdentifier": "test-guardrail-id",
					"guardrailVersion":    "DRAFT",
					"trace":               trace,
				},
			},
		},
	}

	bedrockReq, err := bedrock.ToBedrockResponsesRequest(ctx, req)
	require.NoError(t, err)
	require.NotNil(t, bedrockReq)

	require.NotNil(t, bedrockReq.GuardrailConfig, "GuardrailConfig must be set from ExtraParams")
	assert.Equal(t, "test-guardrail-id", bedrockReq.GuardrailConfig.GuardrailIdentifier)
	assert.Equal(t, "DRAFT", bedrockReq.GuardrailConfig.GuardrailVersion)
	require.NotNil(t, bedrockReq.GuardrailConfig.Trace)
	assert.Equal(t, trace, *bedrockReq.GuardrailConfig.Trace)
	// guardrailConfig must be removed from ExtraParams so it is not double-sent
	assert.Nil(t, bedrockReq.ExtraParams)
}

// TestBedrockToBifrostResponse_TraceStoredInProviderExtraFields verifies that
// ToBifrostResponsesResponse carries guardrail trace in ProviderExtraFields.
func TestBedrockToBifrostResponse_TraceStoredInProviderExtraFields(t *testing.T) {
	action := "BLOCKED"
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	input := &bedrock.BedrockConverseResponse{
		StopReason: "guardrail_intervened",
		Output: &bedrock.BedrockConverseOutput{
			Message: &bedrock.BedrockMessage{
				Role:    bedrock.BedrockMessageRoleAssistant,
				Content: []bedrock.BedrockContentBlock{},
			},
		},
		Trace: &bedrock.BedrockConverseTrace{
			Guardrail: &bedrock.BedrockGuardrailTrace{
				Action: &action,
			},
		},
	}

	bifrostResp, err := input.ToBifrostResponsesResponse(ctx)
	require.NoError(t, err)
	require.NotNil(t, bifrostResp)

	require.NotNil(t, bifrostResp.ProviderExtraFields, "ProviderExtraFields must be set when trace is present")
	traceData, ok := bifrostResp.ProviderExtraFields["trace"]
	require.True(t, ok, "trace key must exist in ProviderExtraFields")
	trace, ok := traceData.(*bedrock.BedrockConverseTrace)
	require.True(t, ok, "trace must be *BedrockConverseTrace")
	require.NotNil(t, trace.Guardrail)
	assert.Equal(t, action, *trace.Guardrail.Action)
}

// TestBifrostToBedrockResponse_TraceRestoredFromProviderExtraFields verifies that
// ToBedrockConverseResponse restores trace from ProviderExtraFields (round-trip).
func TestBifrostToBedrockResponse_TraceRestoredFromProviderExtraFields(t *testing.T) {
	outputMsg := []schemas.ResponsesMessage{
		{
			Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
			Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
			Content: &schemas.ResponsesMessageContent{
				ContentBlocks: []schemas.ResponsesMessageContentBlock{
					{Type: schemas.ResponsesOutputMessageContentTypeText, Text: schemas.Ptr("blocked")},
				},
			},
		},
	}

	t.Run("TypedPointer", func(t *testing.T) {
		action := "BLOCKED"
		input := &schemas.BifrostResponsesResponse{
			Output: outputMsg,
			ProviderExtraFields: map[string]interface{}{
				"trace": &bedrock.BedrockConverseTrace{
					Guardrail: &bedrock.BedrockGuardrailTrace{Action: &action},
				},
			},
		}
		resp, err := bedrock.ToBedrockConverseResponse(input)
		require.NoError(t, err)
		require.NotNil(t, resp.Trace, "Trace must be restored from typed pointer")
		require.NotNil(t, resp.Trace.Guardrail)
		assert.Equal(t, action, *resp.Trace.Guardrail.Action)
	})

	t.Run("JSONDecodedMap", func(t *testing.T) {
		// Simulates ProviderExtraFields["trace"] after a JSON round-trip (e.g. async
		// job retrieval), where sonic.Unmarshal produces map[string]interface{}.
		input := &schemas.BifrostResponsesResponse{
			Output: outputMsg,
			ProviderExtraFields: map[string]interface{}{
				"trace": map[string]interface{}{
					"guardrail": map[string]interface{}{
						"action": "INTERVENED",
					},
				},
			},
		}
		resp, err := bedrock.ToBedrockConverseResponse(input)
		require.NoError(t, err)
		require.NotNil(t, resp.Trace, "Trace must be restored from JSON-decoded map")
		require.NotNil(t, resp.Trace.Guardrail)
		require.NotNil(t, resp.Trace.Guardrail.Action)
		assert.Equal(t, "INTERVENED", *resp.Trace.Guardrail.Action)
	})
}

// TestFinalizeBedrockStream_WithTrace verifies that a guardrail trace captured mid-stream
// is included in the response.completed event's ProviderExtraFields.
func TestFinalizeBedrockStream_WithTrace(t *testing.T) {
	action := "BLOCKED"
	trace := &bedrock.BedrockConverseTrace{
		Guardrail: &bedrock.BedrockGuardrailTrace{
			Action: &action,
		},
	}

	state := bedrock.NewBedrockResponsesStreamState()
	usage := &schemas.ResponsesResponseUsage{InputTokens: 5, OutputTokens: 10, TotalTokens: 15}

	finalResponses := bedrock.FinalizeBedrockStream(state, 0, usage, trace)
	require.NotEmpty(t, finalResponses)

	// The last event must be response.completed
	completed := finalResponses[len(finalResponses)-1]
	require.Equal(t, schemas.ResponsesStreamResponseTypeCompleted, completed.Type)
	require.NotNil(t, completed.Response)
	require.NotNil(t, completed.Response.ProviderExtraFields, "ProviderExtraFields must be set when trace is present")

	traceData, ok := completed.Response.ProviderExtraFields["trace"]
	require.True(t, ok)
	restoredTrace, ok := traceData.(*bedrock.BedrockConverseTrace)
	require.True(t, ok)
	require.NotNil(t, restoredTrace.Guardrail)
	assert.Equal(t, action, *restoredTrace.Guardrail.Action)
}

// TestGuardrailConfigRequestRoundTrip verifies the full guardrailConfig request round-trip:
//
//	BedrockConverseRequest.GuardrailConfig
//	  → ToBifrostResponsesRequest  (stored as ExtraParams["guardrailConfig"])
//	  → ToBedrockResponsesRequest  (extracted back to BedrockConverseRequest.GuardrailConfig)
//
// This is the regression path for the bug where guardrailConfig was silently
// dropped by ToBedrockResponsesRequest.
func TestGuardrailConfigRequestRoundTrip(t *testing.T) {
	trace := testTrace
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	original := &bedrock.BedrockConverseRequest{
		ModelID: "bedrock/anthropic.claude-3-sonnet-20240229-v1:0",
		Messages: []bedrock.BedrockMessage{
			{
				Role:    bedrock.BedrockMessageRoleUser,
				Content: []bedrock.BedrockContentBlock{{Text: schemas.Ptr("Hello")}},
			},
		},
		GuardrailConfig: &bedrock.BedrockGuardrailConfig{
			GuardrailIdentifier: "test-guardrail-id",
			GuardrailVersion:    "DRAFT",
			Trace:               &trace,
		},
	}

	// Step 1: BedrockConverseRequest → BifrostResponsesRequest
	bifrostReq, err := original.ToBifrostResponsesRequest(ctx)
	require.NoError(t, err)
	require.NotNil(t, bifrostReq.Params)
	require.NotNil(t, bifrostReq.Params.ExtraParams)
	_, hasGuardrail := bifrostReq.Params.ExtraParams["guardrailConfig"]
	require.True(t, hasGuardrail, "guardrailConfig must be present in ExtraParams after ToBifrostResponsesRequest")

	// Step 2: BifrostResponsesRequest → BedrockConverseRequest
	result, err := bedrock.ToBedrockResponsesRequest(ctx, bifrostReq)
	require.NoError(t, err)
	require.NotNil(t, result.GuardrailConfig, "GuardrailConfig must survive the round-trip through Bifrost")
	assert.Equal(t, original.GuardrailConfig.GuardrailIdentifier, result.GuardrailConfig.GuardrailIdentifier)
	assert.Equal(t, original.GuardrailConfig.GuardrailVersion, result.GuardrailConfig.GuardrailVersion)
	require.NotNil(t, result.GuardrailConfig.Trace)
	assert.Equal(t, trace, *result.GuardrailConfig.Trace)
	// guardrailConfig must be removed from ExtraParams after extraction
	assert.Nil(t, result.ExtraParams, "ExtraParams should be nil after all keys are extracted")
}

// TestGuardrailTraceResponseRoundTrip verifies the full trace response round-trip:
//
//	BedrockConverseResponse.Trace
//	  → ToBifrostResponsesResponse  (stored in ProviderExtraFields["trace"])
//	  → ToBedrockConverseResponse   (restored to BedrockConverseResponse.Trace)
//
// This is the regression path for the bug where response.Trace was dropped
// by ToBifrostResponsesResponse, making it invisible to callers.
func TestGuardrailTraceResponseRoundTrip(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	t.Run("ConversePath_ActionReason", func(t *testing.T) {
		// Converse (non-streaming) returns actionReason, not action
		actionReason := "Guardrail blocked."
		original := &bedrock.BedrockConverseResponse{
			StopReason: "guardrail_intervened",
			Output: &bedrock.BedrockConverseOutput{
				Message: &bedrock.BedrockMessage{
					Role:    bedrock.BedrockMessageRoleAssistant,
					Content: []bedrock.BedrockContentBlock{},
				},
			},
			Trace: &bedrock.BedrockConverseTrace{
				Guardrail: &bedrock.BedrockGuardrailTrace{
					ActionReason: &actionReason,
				},
			},
		}

		bifrostResp, err := original.ToBifrostResponsesResponse(ctx)
		require.NoError(t, err)
		require.NotNil(t, bifrostResp.ProviderExtraFields)
		_, hasTrace := bifrostResp.ProviderExtraFields["trace"]
		require.True(t, hasTrace)

		result, err := bedrock.ToBedrockConverseResponse(bifrostResp)
		require.NoError(t, err)
		require.NotNil(t, result.Trace, "Trace must survive round-trip")
		require.NotNil(t, result.Trace.Guardrail)
		require.NotNil(t, result.Trace.Guardrail.ActionReason)
		assert.Equal(t, actionReason, *result.Trace.Guardrail.ActionReason)
	})

	t.Run("StreamPath_Action", func(t *testing.T) {
		// ConverseStream returns action (enum), not actionReason
		action := "INTERVENED"
		original := &bedrock.BedrockConverseResponse{
			StopReason: "guardrail_intervened",
			Output: &bedrock.BedrockConverseOutput{
				Message: &bedrock.BedrockMessage{
					Role:    bedrock.BedrockMessageRoleAssistant,
					Content: []bedrock.BedrockContentBlock{},
				},
			},
			Trace: &bedrock.BedrockConverseTrace{
				Guardrail: &bedrock.BedrockGuardrailTrace{
					Action: &action,
				},
			},
		}

		bifrostResp, err := original.ToBifrostResponsesResponse(ctx)
		require.NoError(t, err)
		require.NotNil(t, bifrostResp.ProviderExtraFields)

		result, err := bedrock.ToBedrockConverseResponse(bifrostResp)
		require.NoError(t, err)
		require.NotNil(t, result.Trace)
		require.NotNil(t, result.Trace.Guardrail)
		require.NotNil(t, result.Trace.Guardrail.Action)
		assert.Equal(t, action, *result.Trace.Guardrail.Action)
	})

	t.Run("JSONDecodedMap", func(t *testing.T) {
		// Simulates the async-job-retrieval path where ProviderExtraFields["trace"]
		// has been JSON-decoded into map[string]interface{} instead of *BedrockConverseTrace.
		// extractBedrockTrace must fall back to the sonic marshal/unmarshal path.
		bifrostResp := &schemas.BifrostResponsesResponse{
			ProviderExtraFields: map[string]interface{}{
				"trace": map[string]interface{}{
					"guardrail": map[string]interface{}{
						"action": "INTERVENED",
					},
				},
			},
		}

		result, err := bedrock.ToBedrockConverseResponse(bifrostResp)
		require.NoError(t, err)
		require.NotNil(t, result.Trace, "Trace must be restored from JSON-decoded map")
		require.NotNil(t, result.Trace.Guardrail)
		require.NotNil(t, result.Trace.Guardrail.Action)
		assert.Equal(t, "INTERVENED", *result.Trace.Guardrail.Action)
	})
}

func TestToBedrockResponsesRequest_AnthropicTextFormatUsesOutputConfig(t *testing.T) {
	schemaObj := schemas.NewOrderedMapFromPairs(
		schemas.KV("type", "object"),
		schemas.KV("properties", schemas.NewOrderedMapFromPairs(
			schemas.KV("topic", schemas.NewOrderedMapFromPairs(
				schemas.KV("type", "string"),
			)),
		)),
		schemas.KV("required", []string{"topic"}),
	)

	req := &schemas.BifrostResponsesRequest{
		Model: "bedrock/anthropic.claude-3-sonnet-20240229-v1:0",
		Params: &schemas.ResponsesParameters{
			Text: &schemas.ResponsesTextConfig{
				Format: &schemas.ResponsesTextConfigFormat{
					Type: "json_schema",
					Name: schemas.Ptr("classification"),
					JSONSchema: &schemas.ResponsesTextConfigFormatJSONSchema{
						Schema: &schemas.JSONSchemaOrBool{SchemaMap: schemaObj},
					},
				},
			},
		},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	bedrockReq, err := bedrock.ToBedrockResponsesRequest(ctx, req)
	require.NoError(t, err)
	require.NotNil(t, bedrockReq)

	// PR #3184 moved Anthropic structured output off native output_config.format
	// (rejected by Opus 4.7) onto the synthetic bf_so_* tool path used by all
	// Bedrock models. The test now asserts the synthetic tool reached
	// toolConfig.tools.
	require.NotNil(t, bedrockReq.ToolConfig, "expected toolConfig for structured output")
	foundSyntheticTool := false
	for _, tool := range bedrockReq.ToolConfig.Tools {
		if tool.ToolSpec != nil && strings.HasPrefix(tool.ToolSpec.Name, "bf_so_") {
			foundSyntheticTool = true
			break
		}
	}
	require.True(t, foundSyntheticTool, "expected synthetic bf_so_* tool for structured output")
}

func TestToBedrockResponsesRequest_NonAnthropicTextFormatStillUsesToolConversion(t *testing.T) {
	schemaObj := schemas.NewOrderedMapFromPairs(
		schemas.KV("type", "object"),
		schemas.KV("properties", schemas.NewOrderedMapFromPairs(
			schemas.KV("topic", schemas.NewOrderedMapFromPairs(
				schemas.KV("type", "string"),
			)),
		)),
		schemas.KV("required", []string{"topic"}),
	)

	req := &schemas.BifrostResponsesRequest{
		Model: "bedrock/amazon.nova-pro-v1:0",
		Params: &schemas.ResponsesParameters{
			Text: &schemas.ResponsesTextConfig{
				Format: &schemas.ResponsesTextConfigFormat{
					Type: "json_schema",
					Name: schemas.Ptr("classification"),
					JSONSchema: &schemas.ResponsesTextConfigFormatJSONSchema{
						Schema: &schemas.JSONSchemaOrBool{SchemaMap: schemaObj},
					},
				},
			},
		},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	bedrockReq, err := bedrock.ToBedrockResponsesRequest(ctx, req)
	require.NoError(t, err)
	require.NotNil(t, bedrockReq)

	if bedrockReq.AdditionalModelRequestFields != nil {
		_, hasOutputConfig := bedrockReq.AdditionalModelRequestFields.Get("output_config")
		assert.False(t, hasOutputConfig, "expected no output_config for non-anthropic responses structured output")
	}

	require.NotNil(t, bedrockReq.ToolConfig, "expected tool_config for non-anthropic responses structured output")
	require.NotEmpty(t, bedrockReq.ToolConfig.Tools, "expected synthetic structured output tool to be added")
	require.NotNil(t, bedrockReq.ToolConfig.ToolChoice, "expected structured output tool choice to be forced")
	require.NotNil(t, bedrockReq.ToolConfig.ToolChoice.Tool, "expected structured output tool choice to target the synthetic tool")
	assert.Contains(t, bedrockReq.ToolConfig.ToolChoice.Tool.Name, "bf_so_", "expected forced tool choice to target the synthetic structured output tool")
}

func TestToBedrockResponsesRequest_NonAnthropicTextFormatPreservedWithUserTools(t *testing.T) {
	schemaObj := schemas.NewOrderedMapFromPairs(
		schemas.KV("type", "object"),
		schemas.KV("properties", schemas.NewOrderedMapFromPairs(
			schemas.KV("topic", schemas.NewOrderedMapFromPairs(
				schemas.KV("type", "string"),
			)),
		)),
		schemas.KV("required", []string{"topic"}),
	)

	toolParams := schemas.ToolFunctionParameters{
		Type: "object",
		Properties: schemas.NewOrderedMapFromPairs(
			schemas.KV("city", schemas.NewOrderedMapFromPairs(
				schemas.KV("type", "string"),
			)),
		),
	}

	req := &schemas.BifrostResponsesRequest{
		Model: "bedrock/amazon.nova-pro-v1:0",
		Params: &schemas.ResponsesParameters{
			Text: &schemas.ResponsesTextConfig{
				Format: &schemas.ResponsesTextConfigFormat{
					Type: "json_schema",
					Name: schemas.Ptr("classification"),
					JSONSchema: &schemas.ResponsesTextConfigFormatJSONSchema{
						Schema: &schemas.JSONSchemaOrBool{SchemaMap: schemaObj},
					},
				},
			},
			Tools: []schemas.ResponsesTool{
				{
					Type:        schemas.ResponsesToolTypeFunction,
					Name:        schemas.Ptr("get_weather"),
					Description: schemas.Ptr("Get weather information"),
					ResponsesToolFunction: &schemas.ResponsesToolFunction{
						Parameters: &toolParams,
					},
				},
			},
			ToolChoice: &schemas.ResponsesToolChoice{
				ResponsesToolChoiceStruct: &schemas.ResponsesToolChoiceStruct{
					Type: schemas.ResponsesToolChoiceTypeFunction,
					Name: schemas.Ptr("get_weather"),
				},
			},
		},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	bedrockReq, err := bedrock.ToBedrockResponsesRequest(ctx, req)
	require.NoError(t, err)
	require.NotNil(t, bedrockReq)
	require.NotNil(t, bedrockReq.ToolConfig, "expected tool_config to be initialized")
	require.Len(t, bedrockReq.ToolConfig.Tools, 2, "expected synthetic structured output tool plus user tool")
	require.NotNil(t, bedrockReq.ToolConfig.ToolChoice, "expected structured output tool choice to be forced")
	require.NotNil(t, bedrockReq.ToolConfig.ToolChoice.Tool, "expected structured output tool choice to target the synthetic tool")
	assert.Equal(t, "bf_so_classification", bedrockReq.ToolConfig.ToolChoice.Tool.Name)
	assert.Equal(t, "bf_so_classification", bedrockReq.ToolConfig.Tools[0].ToolSpec.Name)
	assert.Equal(t, "get_weather", bedrockReq.ToolConfig.Tools[1].ToolSpec.Name)
}

// TestToBedrockResponsesRequest_DropsMCPToolKeepsFunction is the regression
// guard for issue #3795: a /v1/responses request to Bedrock carrying a
// Bifrost-hosted `mcp` server tool alongside function tools must no longer fail
// conversion ("tool type 'mcp' is not supported by provider 'bedrock'"). The
// unsupported mcp tool is silently dropped; function tools survive; and the
// inbound request's tool slice is never mutated.
func TestToBedrockResponsesRequest_DropsMCPToolKeepsFunction(t *testing.T) {
	toolParams := schemas.ToolFunctionParameters{
		Type: "object",
		Properties: schemas.NewOrderedMapFromPairs(
			schemas.KV("city", schemas.NewOrderedMapFromPairs(
				schemas.KV("type", "string"),
			)),
		),
	}

	req := &schemas.BifrostResponsesRequest{
		Model: "bedrock/eu.anthropic.claude-sonnet-4-6",
		Params: &schemas.ResponsesParameters{
			Tools: []schemas.ResponsesTool{
				{
					Type:        schemas.ResponsesToolTypeFunction,
					Name:        schemas.Ptr("get_weather"),
					Description: schemas.Ptr("Get weather information"),
					ResponsesToolFunction: &schemas.ResponsesToolFunction{
						Parameters: &toolParams,
					},
				},
				{
					// Bifrost-hosted MCP server tool — server_url points back at
					// Bifrost itself; Bedrock's Converse API can't consume it.
					Type: schemas.ResponsesToolTypeMCP,
					ResponsesToolMCP: &schemas.ResponsesToolMCP{
						ServerLabel: "mongodb",
						ServerURL:   schemas.Ptr("https://bifrost.example.com/mcp/mongodb"),
					},
				},
			},
		},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	bedrockReq, err := bedrock.ToBedrockResponsesRequest(ctx, req)
	require.NoError(t, err, "mcp tool must be dropped, not error the conversion (issue #3795)")
	require.NotNil(t, bedrockReq)
	require.NotNil(t, bedrockReq.ToolConfig, "function tool must still produce a tool_config")
	require.Len(t, bedrockReq.ToolConfig.Tools, 1, "only the function tool should reach Bedrock; mcp dropped")
	assert.Equal(t, "get_weather", bedrockReq.ToolConfig.Tools[0].ToolSpec.Name)

	// The inbound request must not be mutated by the filtering.
	require.Len(t, req.Params.Tools, 2, "inbound Params.Tools must be left untouched")
	assert.Equal(t, schemas.ResponsesToolTypeMCP, req.Params.Tools[1].Type)
}

// TestToBedrockResponsesRequest_AllToolsDroppedNoToolConfig verifies that when
// every tool is provider-unsupported (e.g. a lone mcp tool), conversion still
// succeeds and simply emits no tool_config.
func TestToBedrockResponsesRequest_AllToolsDroppedNoToolConfig(t *testing.T) {
	req := &schemas.BifrostResponsesRequest{
		Model: "bedrock/eu.anthropic.claude-sonnet-4-6",
		Params: &schemas.ResponsesParameters{
			Tools: []schemas.ResponsesTool{
				{Type: schemas.ResponsesToolTypeMCP, ResponsesToolMCP: &schemas.ResponsesToolMCP{ServerLabel: "mongodb"}},
			},
		},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	bedrockReq, err := bedrock.ToBedrockResponsesRequest(ctx, req)
	require.NoError(t, err)
	require.NotNil(t, bedrockReq)
	assert.Nil(t, bedrockReq.ToolConfig, "no supported tools means no tool_config")
}

// TestToolResultJSONParsingResponsesAPI tests that tool results are correctly parsed and wrapped based on JSON type
// Tests only Responses API.
func TestToolResultJSONParsingResponsesAPI(t *testing.T) {
	tests := []struct {
		name                string
		toolResultContent   string
		expectedContentType string // "text" or "json"
		expectedJSON        json.RawMessage
		expectedText        *string
	}{
		{
			name:                "PlainTextResult",
			toolResultContent:   "Hello there! This is plain text, not JSON.",
			expectedContentType: "text",
			expectedText:        schemas.Ptr("Hello there! This is plain text, not JSON."),
		},
		{
			name:                "InvalidJSONResult",
			toolResultContent:   "{invalid json syntax",
			expectedContentType: "text",
			expectedText:        schemas.Ptr("{invalid json syntax"),
		},
		{
			name:                "JSONObjectResult",
			toolResultContent:   `{"location":"NYC","temperature":72}`,
			expectedContentType: "json",
			expectedJSON:        mustMarshalJSON(map[string]any{"location": "NYC", "temperature": float64(72)}),
		},
		{
			name:                "JSONArrayResult",
			toolResultContent:   `[{"period":"now","weather":"sunny"},{"period":"next_1_hour","weather":"cloudy"}]`,
			expectedContentType: "json",
			expectedJSON: mustMarshalJSON(map[string]any{
				"results": []any{
					map[string]any{"period": "now", "weather": "sunny"},
					map[string]any{"period": "next_1_hour", "weather": "cloudy"},
				},
			}),
		},
		{
			name:                "JSONPrimitiveNumberResult",
			toolResultContent:   `42`,
			expectedContentType: "json",
			expectedJSON:        mustMarshalJSON(map[string]any{"value": float64(42)}),
		},
		{
			name:                "JSONPrimitiveStringResult",
			toolResultContent:   `"hello world"`,
			expectedContentType: "json",
			expectedJSON:        mustMarshalJSON(map[string]any{"value": "hello world"}),
		},
		{
			name:                "JSONPrimitiveBooleanResult",
			toolResultContent:   `true`,
			expectedContentType: "json",
			expectedJSON:        mustMarshalJSON(map[string]any{"value": true}),
		},
		{
			name:                "JSONPrimitiveNullResult",
			toolResultContent:   `null`,
			expectedContentType: "json",
			expectedJSON:        mustMarshalJSON(map[string]any{"value": nil}),
		},
		{
			name:                "EmptyJSONObjectResult",
			toolResultContent:   `{}`,
			expectedContentType: "json",
			expectedJSON:        mustMarshalJSON(map[string]any{}),
		},
		{
			name:                "EmptyJSONArrayResult",
			toolResultContent:   `[]`,
			expectedContentType: "json",
			expectedJSON:        mustMarshalJSON(map[string]any{"results": []any{}}),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a Responses API message with function call output (tool result)
			input := []schemas.ResponsesMessage{
				{
					Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
					ResponsesToolMessage: &schemas.ResponsesToolMessage{
						CallID: schemas.Ptr("tooluse_test_123"),
						Output: &schemas.ResponsesToolMessageOutputStruct{
							ResponsesToolCallOutputStr: schemas.Ptr(tt.toolResultContent),
						},
					},
				},
			}

			messages, _, err := bedrock.ConvertBifrostMessagesToBedrockMessages(context.Background(), input, false)
			require.NoError(t, err)
			require.Len(t, messages, 1)

			// The tool result should be in a user message
			toolResultMsg := messages[0]
			assert.Equal(t, bedrock.BedrockMessageRoleUser, toolResultMsg.Role)
			require.Len(t, toolResultMsg.Content, 1)

			toolResult := toolResultMsg.Content[0].ToolResult
			require.NotNil(t, toolResult)
			assert.Equal(t, "tooluse_test_123", toolResult.ToolUseID)
			require.Len(t, toolResult.Content, 1)

			resultContent := toolResult.Content[0]
			if tt.expectedContentType == "text" {
				assert.NotNil(t, resultContent.Text, "Expected text content")
				assert.Nil(t, resultContent.JSON, "Expected no JSON content")
				assert.Equal(t, tt.expectedText, resultContent.Text)
			} else {
				assert.Nil(t, resultContent.Text, "Expected no text content")
				assert.Equal(t, tt.expectedJSON, resultContent.JSON)
			}
		})
	}
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

// TestToolResultDeduplication tests that duplicate tool results are properly handled
func TestToolResultDeduplication(t *testing.T) {
	t.Run("DuplicateResultInPendingResults", func(t *testing.T) {
		manager := bedrock.NewToolCallStateManager()

		// tool call and result
		manager.RegisterToolCall("call-123", "get_weather", `{"location":"NYC"}`, nil)
		content1 := []bedrock.BedrockContentBlock{{Text: schemas.Ptr("First result")}}
		manager.RegisterToolResult("call-123", content1, "success", nil)

		// duplicate result with different content
		content2 := []bedrock.BedrockContentBlock{{Text: schemas.Ptr("Duplicate result")}}
		manager.RegisterToolResult("call-123", content2, "success", nil)

		// Deduplicated regardless of content. Practically same ID should not ever has diff content.
		results := manager.GetPendingResults()
		require.Len(t, results, 1)
		require.NotNil(t, results["call-123"])
		assert.Equal(t, "First result", *results["call-123"].Content[0].Text)
	})

	t.Run("DuplicateResultAfterEmission", func(t *testing.T) {
		manager := bedrock.NewToolCallStateManager()

		// Register and emit a tool call
		manager.RegisterToolCall("call-456", "calculate", `{"x":1,"y":2}`, nil)
		callIDs := manager.EmitPendingToolCalls()
		require.Len(t, callIDs, 1)
		manager.MarkToolCallsEmitted(callIDs, 0)

		// register and emit the result
		content1 := []bedrock.BedrockContentBlock{{Text: schemas.Ptr("3")}}
		manager.RegisterToolResult("call-456", content1, "success", nil)
		manager.MarkResultsEmitted([]string{"call-456"})

		// Register a duplicate
		content2 := []bedrock.BedrockContentBlock{{Text: schemas.Ptr("Duplicate")}}
		manager.RegisterToolResult("call-456", content2, "success", nil)

		// Not added due to it being duplicated with the emitted result
		results := manager.GetPendingResults()
		assert.Empty(t, results)
	})

	t.Run("MultipleToolCallsWithDuplicateResults", func(t *testing.T) {
		manager := bedrock.NewToolCallStateManager()

		// Register multiple tool calls
		manager.RegisterToolCall("call-a", "tool_a", `{}`, nil)
		manager.RegisterToolCall("call-b", "tool_b", `{}`, nil)

		// Register results for both
		contentA := []bedrock.BedrockContentBlock{{Text: schemas.Ptr("Result A")}}
		contentB := []bedrock.BedrockContentBlock{{Text: schemas.Ptr("Result B")}}
		manager.RegisterToolResult("call-a", contentA, "success", nil)
		manager.RegisterToolResult("call-b", contentB, "success", nil)

		// Try to register duplicates
		contentADup := []bedrock.BedrockContentBlock{{Text: schemas.Ptr("Result A")}}
		contentBDup := []bedrock.BedrockContentBlock{{Text: schemas.Ptr("Result B")}}
		manager.RegisterToolResult("call-a", contentADup, "success", nil)
		manager.RegisterToolResult("call-b", contentBDup, "success", nil)

		// Verify original results are preserved
		results := manager.GetPendingResults()
		require.Len(t, results, 2)
		assert.Equal(t, "Result A", *results["call-a"].Content[0].Text)
		assert.Equal(t, "Result B", *results["call-b"].Content[0].Text)
	})
}

// TestToolCallDeduplication tests that duplicate tool calls are properly handled
func TestToolCallDeduplication(t *testing.T) {
	t.Run("DuplicateToolCallIgnored", func(t *testing.T) {
		manager := bedrock.NewToolCallStateManager()

		manager.RegisterToolCall("call-123", "get_weather", `{"location":"NYC"}`, nil)
		manager.RegisterToolCall("call-123", "get_weather", `{"location":"NYC"}`, nil)

		// Deduplicated regardless of content.
		callIDs := manager.EmitPendingToolCalls()
		require.Len(t, callIDs, 1)
		assert.Equal(t, "call-123", callIDs[0])
	})

	t.Run("MultipleDistinctToolCalls", func(t *testing.T) {
		manager := bedrock.NewToolCallStateManager()

		// initial registration
		manager.RegisterToolCall("call-a", "tool_a", `{"x":1}`, nil)
		manager.RegisterToolCall("call-b", "tool_b", `{"y":2}`, nil)
		manager.RegisterToolCall("call-c", "tool_c", `{"z":3}`, nil)

		// duplications
		manager.RegisterToolCall("call-a", "tool_a", `{"x":1}`, nil)
		manager.RegisterToolCall("call-b", "tool_b", `{"y":2}`, nil)
		manager.RegisterToolCall("call-c", "tool_c", `{"z":3}`, nil)

		// no duplicates
		callIDs := manager.EmitPendingToolCalls()
		require.Len(t, callIDs, 3)
		assert.Contains(t, callIDs, "call-a")
		assert.Contains(t, callIDs, "call-b")
		assert.Contains(t, callIDs, "call-c")
	})

	t.Run("DuplicateToolCallAfterEmission", func(t *testing.T) {
		manager := bedrock.NewToolCallStateManager()

		// register and emit a tool call
		manager.RegisterToolCall("call-789", "calculator", `{"expr":"1+1"}`, nil)
		callIDs := manager.EmitPendingToolCalls()
		require.Len(t, callIDs, 1)
		manager.MarkToolCallsEmitted(callIDs, 0)

		// register the same tool call again after emission
		manager.RegisterToolCall("call-789", "calculator", `{"expr":"1+1"}`, nil)

		// duplicate was rejected
		newCallIDs := manager.EmitPendingToolCalls()
		assert.Empty(t, newCallIDs)
	})
}

// TestAnthropicReasoningConfigUsesThinkinField verifies that Anthropic models use
// the "thinking" field (not "reasoning_config") in additionalModelRequestFields
// for the Bedrock Converse API.
func TestAnthropicReasoningConfigUsesThinkingField(t *testing.T) {
	tests := []struct {
		name                     string
		model                    string
		effort                   *string
		maxTokens                *int
		expectedFieldName        string
		expectedType             string
		expectBudgetTokens       bool
		expectNoOutputConfig     bool
		expectOutputConfigEffort string // expected effort value in output_config (empty string means no output_config expected)
	}{
		{
			name:                     "Opus4.6_AdaptiveThinking_UsesThinkingField",
			model:                    "anthropic.claude-opus-4-6-v1",
			effort:                   schemas.Ptr("high"),
			expectedFieldName:        "thinking",
			expectedType:             "adaptive",
			expectBudgetTokens:       false,
			expectNoOutputConfig:     false,
			expectOutputConfigEffort: "high",
		},
		{
			name:                 "Opus4.5_NativeEffort_UsesThinkingField",
			model:                "anthropic.claude-opus-4-5-v1",
			effort:               schemas.Ptr("high"),
			expectedFieldName:    "thinking",
			expectedType:         "enabled",
			expectBudgetTokens:   true,
			expectNoOutputConfig: true,
		},
		{
			name:                 "Sonnet3.7_OlderModel_UsesThinkingField",
			model:                "anthropic.claude-3-7-sonnet-v1",
			effort:               schemas.Ptr("medium"),
			expectedFieldName:    "thinking",
			expectedType:         "enabled",
			expectBudgetTokens:   true,
			expectNoOutputConfig: true,
		},
		{
			name:                 "Anthropic_MaxTokens_UsesThinkingField",
			model:                "anthropic.claude-3-7-sonnet-v1",
			maxTokens:            schemas.Ptr(2048),
			expectedFieldName:    "thinking",
			expectedType:         "enabled",
			expectBudgetTokens:   true,
			expectNoOutputConfig: true,
		},
		{
			name:                 "Anthropic_DisabledReasoning_UsesThinkingField",
			model:                "anthropic.claude-3-7-sonnet-v1",
			effort:               schemas.Ptr("none"),
			expectedFieldName:    "thinking",
			expectedType:         "disabled",
			expectBudgetTokens:   false,
			expectNoOutputConfig: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reasoning := &schemas.ChatReasoning{}
			if tt.effort != nil {
				reasoning.Effort = tt.effort
			}
			if tt.maxTokens != nil {
				reasoning.MaxTokens = tt.maxTokens
			}

			bifrostReq := &schemas.BifrostChatRequest{
				Model: tt.model,
				Input: []schemas.ChatMessage{
					{
						Role: schemas.ChatMessageRoleUser,
						Content: &schemas.ChatMessageContent{
							ContentStr: schemas.Ptr("Hello"),
						},
					},
				},
				Params: &schemas.ChatParameters{
					Reasoning: reasoning,
				},
			}

			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			result, err := bedrock.ToBedrockChatCompletionRequest(ctx, bifrostReq)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.NotNil(t, result.AdditionalModelRequestFields)

			// Verify the correct field name is used
			thinkingConfig, hasThinking := result.AdditionalModelRequestFields.Get(tt.expectedFieldName)
			assert.True(t, hasThinking, "expected field %q in AdditionalModelRequestFields", tt.expectedFieldName)

			// Verify reasoning_config is NOT used for Anthropic models
			_, hasReasoningConfig := result.AdditionalModelRequestFields.Get("reasoning_config")
			assert.False(t, hasReasoningConfig, "reasoning_config should NOT be set for Anthropic models")

			// Verify output_config handling
			if tt.expectNoOutputConfig {
				_, hasOutputConfig := result.AdditionalModelRequestFields.Get("output_config")
				assert.False(t, hasOutputConfig, "output_config should NOT be set for this model")
			} else if tt.expectOutputConfigEffort != "" {
				// Opus 4.6+ should have output_config.effort set
				outputConfig, hasOutputConfig := result.AdditionalModelRequestFields.Get("output_config")
				assert.True(t, hasOutputConfig, "output_config should be set for Opus 4.6+")
				if outputConfigMap, ok := outputConfig.(map[string]any); ok {
					effortStr, _ := outputConfigMap["effort"].(string)
					assert.Equal(t, tt.expectOutputConfigEffort, effortStr, "output_config.effort should match expected value")
				}
			}

			// Verify the type
			if configMap, ok := thinkingConfig.(map[string]any); ok {
				typeStr, _ := configMap["type"].(string)
				assert.Equal(t, tt.expectedType, typeStr)

				if tt.expectBudgetTokens {
					_, hasBudget := configMap["budget_tokens"]
					assert.True(t, hasBudget, "expected budget_tokens in thinking config")
				}
			} else if configMap, ok := thinkingConfig.(map[string]string); ok {
				assert.Equal(t, tt.expectedType, configMap["type"])
			}
		})
	}
}

func TestAnthropicOrderedOutputConfigRoundTripsReasoning(t *testing.T) {
	request := &bedrock.BedrockConverseRequest{
		ModelID: "anthropic.claude-opus-4-6-v1",
		Messages: []bedrock.BedrockMessage{
			{
				Role: bedrock.BedrockMessageRoleUser,
				Content: []bedrock.BedrockContentBlock{
					{
						Text: schemas.Ptr("Hello"),
					},
				},
			},
		},
		AdditionalModelRequestFields: schemas.NewOrderedMapFromPairs(
			schemas.KV("thinking", map[string]any{
				"type":          "adaptive",
				"budget_tokens": 2048,
			}),
			schemas.KV("output_config", schemas.NewOrderedMapFromPairs(
				schemas.KV("effort", "medium"),
			)),
		),
		ExtraParams: map[string]any{
			"reasoning_summary": "auto",
		},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	result, err := request.ToBifrostResponsesRequest(ctx)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.Params)
	require.NotNil(t, result.Params.Reasoning)
	require.NotNil(t, result.Params.Reasoning.Effort)
	assert.Equal(t, "medium", *result.Params.Reasoning.Effort)
	require.NotNil(t, result.Params.Reasoning.MaxTokens)
	assert.Equal(t, 2048, *result.Params.Reasoning.MaxTokens)
	require.NotNil(t, result.Params.Reasoning.Summary)
	assert.Equal(t, "auto", *result.Params.Reasoning.Summary)
}

func TestAnthropicOutputConfigFormatStillFallsBackToBudgetTokensForReasoning(t *testing.T) {
	request := &bedrock.BedrockConverseRequest{
		ModelID: "anthropic.claude-opus-4-6-v1",
		Messages: []bedrock.BedrockMessage{
			{
				Role: bedrock.BedrockMessageRoleUser,
				Content: []bedrock.BedrockContentBlock{
					{
						Text: schemas.Ptr("Hello"),
					},
				},
			},
		},
		AdditionalModelRequestFields: schemas.NewOrderedMapFromPairs(
			schemas.KV("thinking", map[string]any{
				"type":          "adaptive",
				"budget_tokens": 2048,
			}),
			schemas.KV("output_config", schemas.NewOrderedMapFromPairs(
				schemas.KV("format", schemas.NewOrderedMapFromPairs(
					schemas.KV("type", "json_schema"),
					schemas.KV("schema", schemas.NewOrderedMapFromPairs(
						schemas.KV("type", "object"),
					)),
				)),
			)),
		),
		ExtraParams: map[string]any{
			"reasoning_summary": "auto",
		},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	result, err := request.ToBifrostResponsesRequest(ctx)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.Params)
	require.NotNil(t, result.Params.Reasoning)
	require.NotNil(t, result.Params.Reasoning.Effort)
	// Effort is inferred from budget_tokens (2048) against the model-specific max output tokens
	// (128K for claude-opus-4-6) minus Anthropic's minimum reasoning budget (1024). That ratio
	// (~0.008) falls in the "low" bucket — see providerUtils.GetReasoningEffortFromBudgetTokens.
	assert.Equal(t, "low", *result.Params.Reasoning.Effort)
	require.NotNil(t, result.Params.Reasoning.MaxTokens)
	assert.Equal(t, 2048, *result.Params.Reasoning.MaxTokens)
	require.NotNil(t, result.Params.Reasoning.Summary)
	assert.Equal(t, "auto", *result.Params.Reasoning.Summary)
}

// TestAnthropicStructuredOutputUsesOutputConfigWithoutForcedToolChoice ensures
// Anthropic Bedrock structured output uses native output_config.format and does
// not synthesize a forced tool choice, while keeping reasoning (thinking) active.
func TestAnthropicStructuredOutputUsesOutputConfigWithoutForcedToolChoice(t *testing.T) {
	responseFormat := any(map[string]any{
		"type": "json_schema",
		"json_schema": map[string]any{
			"name": "classification",
			"schema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"topic": map[string]any{
						"type": "string",
					},
				},
				"required": []any{"topic"},
			},
		},
	})

	bifrostReq := &schemas.BifrostChatRequest{
		Model: "anthropic.claude-3-7-sonnet-v1",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: schemas.Ptr("Classify this"),
				},
			},
		},
		Params: &schemas.ChatParameters{
			ResponseFormat: &responseFormat,
			Reasoning: &schemas.ChatReasoning{
				MaxTokens: schemas.Ptr(2048),
			},
		},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	result, err := bedrock.ToBedrockChatCompletionRequest(ctx, bifrostReq)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.AdditionalModelRequestFields)

	// PR #3184 moved Anthropic structured output off native output_config.format
	// onto the synthetic bf_so_* tool path used by all Bedrock models.
	require.NotNil(t, result.ToolConfig, "expected toolConfig for structured output")
	foundSyntheticTool := false
	for _, tool := range result.ToolConfig.Tools {
		if tool.ToolSpec != nil && strings.HasPrefix(tool.ToolSpec.Name, "bf_so_") {
			foundSyntheticTool = true
			break
		}
	}
	require.True(t, foundSyntheticTool, "expected synthetic bf_so_* tool for structured output")

	// reasoning should still be preserved for anthropic
	thinkingRaw, hasThinking := result.AdditionalModelRequestFields.Get("thinking")
	require.True(t, hasThinking, "expected thinking field for anthropic reasoning")
	thinking, ok := thinkingRaw.(map[string]any)
	require.True(t, ok, "expected thinking to be a map")
	assert.Equal(t, "enabled", thinking["type"])
}

func TestAnthropicStructuredOutputAcceptsOrderedMaps(t *testing.T) {
	responseFormat := any(schemas.NewOrderedMapFromPairs(
		schemas.KV("type", "json_schema"),
		schemas.KV("json_schema", schemas.NewOrderedMapFromPairs(
			schemas.KV("name", "classification"),
			schemas.KV("schema", schemas.NewOrderedMapFromPairs(
				schemas.KV("type", "object"),
				schemas.KV("description", "Return structured classification"),
				schemas.KV("properties", schemas.NewOrderedMapFromPairs(
					schemas.KV("topic", schemas.NewOrderedMapFromPairs(
						schemas.KV("type", "string"),
					)),
				)),
				schemas.KV("required", []any{"topic"}),
			)),
		)),
	))

	bifrostReq := &schemas.BifrostChatRequest{
		Model: "anthropic.claude-3-7-sonnet-v1",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: schemas.Ptr("Classify this"),
				},
			},
		},
		Params: &schemas.ChatParameters{
			ResponseFormat: &responseFormat,
			Reasoning: &schemas.ChatReasoning{
				MaxTokens: schemas.Ptr(2048),
			},
		},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	result, err := bedrock.ToBedrockChatCompletionRequest(ctx, bifrostReq)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.AdditionalModelRequestFields)

	// PR #3184 moved Anthropic structured output off native output_config.format
	// onto the synthetic bf_so_* tool path. Test asserts the synthetic tool path
	// accepts OrderedMap-shaped response_format input without dropping the schema.
	require.NotNil(t, result.ToolConfig, "expected toolConfig for structured output")
	var syntheticTool *bedrock.BedrockTool
	for i, tool := range result.ToolConfig.Tools {
		if tool.ToolSpec != nil && strings.HasPrefix(tool.ToolSpec.Name, "bf_so_") {
			syntheticTool = &result.ToolConfig.Tools[i]
			break
		}
	}
	require.NotNil(t, syntheticTool, "expected synthetic bf_so_* tool for structured output")
	require.NotEmpty(t, syntheticTool.ToolSpec.InputSchema.JSON, "expected synthetic tool schema bytes")
}

// betaListContains reports whether the OrderedMap's anthropic_beta entry
// (regardless of slice element type) contains the given header value.
// Mirrors the multiple shapes appendAnthropicBetaToFields can leave behind
// (string, []string, []interface{}) so each test covers all three.
func betaListContains(t *testing.T, fields *schemas.OrderedMap, header string) bool {
	t.Helper()
	if fields == nil {
		return false
	}
	raw, ok := fields.Get("anthropic_beta")
	if !ok {
		return false
	}
	switch v := raw.(type) {
	case string:
		return v == header
	case []string:
		for _, s := range v {
			if s == header {
				return true
			}
		}
	case []interface{}:
		for _, item := range v {
			if s, ok := item.(string); ok && s == header {
				return true
			}
		}
	default:
		t.Logf("unexpected anthropic_beta type %T: %#v", v, v)
	}
	return false
}

// TestBedrockAnthropicChatStructuredOutputUsesSyntheticTool locks in Route A:
// Bedrock + Anthropic + json_schema response_format routes through the
// synthetic `bf_so_*` tool path (same as non-Anthropic Bedrock providers),
// not Bedrock's native `output_config.format`. Bedrock Converse's support for
// `output_config.format` is inconsistent across Claude variants (Opus 4.7
// rejects with "output_config.format: Extra inputs are not permitted"); the
// synthetic-tool path is a regular Converse tool call that all variants
// accept reliably.
func TestBedrockAnthropicChatStructuredOutputUsesSyntheticTool(t *testing.T) {
	responseFormat := any(map[string]any{
		"type": "json_schema",
		"json_schema": map[string]any{
			"name": "classification",
			"schema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"isNewTopic": map[string]any{"type": "boolean"},
					"title":      map[string]any{"type": "string"},
					"result":     map[string]any{"type": "number"},
				},
				"required": []any{"isNewTopic", "title", "result"},
			},
		},
	})

	bifrostReq := &schemas.BifrostChatRequest{
		Model: "anthropic.claude-opus-4-7-v1:0",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: schemas.Ptr("Hello, what's the result of 678*132?"),
				},
			},
		},
		Params: &schemas.ChatParameters{
			ResponseFormat: &responseFormat,
		},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	result, err := bedrock.ToBedrockChatCompletionRequest(ctx, bifrostReq)
	require.NoError(t, err)
	require.NotNil(t, result)

	// Negative: no `output_config` and no structured-outputs beta tunnel
	// in additionalModelRequestFields. PR #3053 added both; Route A removes them.
	if result.AdditionalModelRequestFields != nil {
		_, hasOutputConfig := result.AdditionalModelRequestFields.Get("output_config")
		assert.False(t, hasOutputConfig, "expected NO output_config for Anthropic on Bedrock under Route A")
		assert.False(
			t,
			betaListContains(t, result.AdditionalModelRequestFields, "structured-outputs-2025-11-13"),
			"additionalModelRequestFields.anthropic_beta should NOT contain structured-outputs-2025-11-13",
		)
	}

	// Positive: synthetic bf_so_* tool present and forced via tool_choice —
	// this is the contract that replaces output_config.format on Bedrock.
	require.NotNil(t, result.ToolConfig, "expected toolConfig with synthetic bf_so_* tool")
	require.NotEmpty(t, result.ToolConfig.Tools, "expected at least one tool (the synthetic bf_so_*)")
	require.NotNil(t, result.ToolConfig.ToolChoice, "expected forced tool_choice")
	require.NotNil(t, result.ToolConfig.ToolChoice.Tool, "expected tool_choice to target a specific tool")
	assert.Contains(t, result.ToolConfig.ToolChoice.Tool.Name, "bf_so_", "expected forced tool_choice to target bf_so_*")
	assert.Equal(t, "bf_so_classification", result.ToolConfig.ToolChoice.Tool.Name)
}

// TestToBedrockResponsesRequest_AnthropicStructuredOutputUsesSyntheticTool
// is the responses-path twin of TestBedrockAnthropicChatStructuredOutputUsesSyntheticTool.
// The user's failing request comes through the Anthropic Messages SDK
// (`client.messages.create`), routed via /v1/messages -> ToBifrostResponsesRequest
// -> ToBedrockResponsesRequest with Params.Text.Format set.
func TestToBedrockResponsesRequest_AnthropicStructuredOutputUsesSyntheticTool(t *testing.T) {
	schemaObj := schemas.NewOrderedMapFromPairs(
		schemas.KV("type", "object"),
		schemas.KV("properties", schemas.NewOrderedMapFromPairs(
			schemas.KV("isNewTopic", schemas.NewOrderedMapFromPairs(schemas.KV("type", "boolean"))),
			schemas.KV("title", schemas.NewOrderedMapFromPairs(schemas.KV("type", "string"))),
			schemas.KV("result", schemas.NewOrderedMapFromPairs(schemas.KV("type", "number"))),
		)),
		schemas.KV("required", []string{"isNewTopic", "title", "result"}),
	)

	req := &schemas.BifrostResponsesRequest{
		Model: "anthropic.claude-opus-4-7-v1:0",
		Params: &schemas.ResponsesParameters{
			Text: &schemas.ResponsesTextConfig{
				Format: &schemas.ResponsesTextConfigFormat{
					Type: "json_schema",
					Name: schemas.Ptr("classification"),
					JSONSchema: &schemas.ResponsesTextConfigFormatJSONSchema{
						Schema: &schemas.JSONSchemaOrBool{SchemaMap: schemaObj},
					},
				},
			},
		},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	bedrockReq, err := bedrock.ToBedrockResponsesRequest(ctx, req)
	require.NoError(t, err)
	require.NotNil(t, bedrockReq)

	// Negative: no output_config, no structured-outputs beta tunnel.
	if bedrockReq.AdditionalModelRequestFields != nil {
		_, hasOutputConfig := bedrockReq.AdditionalModelRequestFields.Get("output_config")
		assert.False(t, hasOutputConfig, "expected NO output_config for Anthropic on Bedrock under Route A")
		assert.False(
			t,
			betaListContains(t, bedrockReq.AdditionalModelRequestFields, "structured-outputs-2025-11-13"),
			"additionalModelRequestFields.anthropic_beta should NOT contain structured-outputs-2025-11-13",
		)
	}

	// Positive: synthetic bf_so_* tool injected and forced.
	require.NotNil(t, bedrockReq.ToolConfig, "expected toolConfig with synthetic bf_so_* tool")
	require.NotEmpty(t, bedrockReq.ToolConfig.Tools, "expected at least one tool (the synthetic bf_so_*)")
	require.NotNil(t, bedrockReq.ToolConfig.ToolChoice, "expected forced tool_choice")
	require.NotNil(t, bedrockReq.ToolConfig.ToolChoice.Tool, "expected tool_choice to target a specific tool")
	assert.Contains(t, bedrockReq.ToolConfig.ToolChoice.Tool.Name, "bf_so_", "expected forced tool_choice to target bf_so_*")
	assert.Equal(t, "bf_so_classification", bedrockReq.ToolConfig.ToolChoice.Tool.Name)
}

// TestNonAnthropicStructuredOutputStillUsesToolConversion ensures Bedrock models
// other than Anthropic continue to use the legacy response_format->tool path.
func TestNonAnthropicStructuredOutputStillUsesToolConversion(t *testing.T) {
	responseFormat := any(schemas.NewOrderedMapFromPairs(
		schemas.KV("type", "json_schema"),
		schemas.KV("json_schema", schemas.NewOrderedMapFromPairs(
			schemas.KV("name", "classification"),
			schemas.KV("schema", schemas.NewOrderedMapFromPairs(
				schemas.KV("type", "object"),
				schemas.KV("description", "Return structured classification"),
				schemas.KV("properties", schemas.NewOrderedMapFromPairs(
					schemas.KV("topic", schemas.NewOrderedMapFromPairs(
						schemas.KV("type", "string"),
					)),
				)),
				schemas.KV("required", []any{"topic"}),
			)),
		)),
	))

	bifrostReq := &schemas.BifrostChatRequest{
		Model: "amazon.nova-pro-v1",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: schemas.Ptr("Classify this"),
				},
			},
		},
		Params: &schemas.ChatParameters{
			ResponseFormat: &responseFormat,
		},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	result, err := bedrock.ToBedrockChatCompletionRequest(ctx, bifrostReq)
	require.NoError(t, err)
	require.NotNil(t, result)

	// Non-Anthropic models should not use output_config.format.
	if result.AdditionalModelRequestFields != nil {
		_, hasOutputConfig := result.AdditionalModelRequestFields.Get("output_config")
		assert.False(t, hasOutputConfig, "expected no output_config for non-anthropic structured output")
	}

	require.NotNil(t, result.ToolConfig, "expected tool_config for non-anthropic structured output")
	require.NotEmpty(t, result.ToolConfig.Tools, "expected synthetic structured output tool to be added")
	require.NotNil(t, result.ToolConfig.ToolChoice, "expected structured output tool choice to be forced")
	require.NotNil(t, result.ToolConfig.ToolChoice.Tool, "expected structured output tool choice to target the synthetic tool")
	assert.Equal(t, "bf_so_classification", result.ToolConfig.ToolChoice.Tool.Name)
	assert.Equal(t, "bf_so_classification", result.ToolConfig.Tools[0].ToolSpec.Name)

	schemaRaw := result.ToolConfig.Tools[0].ToolSpec.InputSchema.JSON
	var schema schemas.OrderedMap
	require.NoError(t, schema.UnmarshalJSON(schemaRaw))
	schemaType, ok := schema.Get("type")
	require.True(t, ok, "expected tool schema type")
	assert.Equal(t, "object", schemaType)
}

// TestAnthropicStructuredOutputMergesAdditionalModelRequestFieldPaths ensures
// additionalModelRequestFieldPaths are merged into existing AdditionalModelRequestFields
// and output_config is deep-merged instead of overwritten.
func TestAnthropicStructuredOutputMergesAdditionalModelRequestFieldPaths(t *testing.T) {
	responseFormat := any(map[string]any{
		"type": "json_schema",
		"json_schema": map[string]any{
			"name": "classification",
			"schema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"topic": map[string]any{
						"type": "string",
					},
				},
				"required": []any{"topic"},
			},
		},
	})

	bifrostReq := &schemas.BifrostChatRequest{
		Model: "anthropic.claude-3-7-sonnet-v1",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: schemas.Ptr("Classify this"),
				},
			},
		},
		Params: &schemas.ChatParameters{
			ResponseFormat: &responseFormat,
			Reasoning: &schemas.ChatReasoning{
				MaxTokens: schemas.Ptr(2048),
			},
			ExtraParams: map[string]any{
				"additionalModelRequestFieldPaths": schemas.NewOrderedMapFromPairs(
					schemas.KV("output_config", map[string]any{
						"foo": "bar",
					}),
					schemas.KV("customField", "customValue"),
				),
			},
		},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	result, err := bedrock.ToBedrockChatCompletionRequest(ctx, bifrostReq)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.AdditionalModelRequestFields)

	// Structured output is routed through the synthetic bf_so_* tool path on all
	// Bedrock models (see PR #3184 and utils.go:1172). Native output_config.format
	// is intentionally not written for any Bedrock model, so the merge test
	// asserts the synthetic tool reached toolConfig.tools instead.
	require.NotNil(t, result.ToolConfig, "expected toolConfig for structured output")
	foundSyntheticTool := false
	for _, tool := range result.ToolConfig.Tools {
		if tool.ToolSpec != nil && strings.HasPrefix(tool.ToolSpec.Name, "bf_so_") {
			foundSyntheticTool = true
			break
		}
	}
	require.True(t, foundSyntheticTool, "expected synthetic bf_so_* tool for structured output")

	// Incoming additionalModelRequestFieldPaths.output_config key must be merged
	// into AdditionalModelRequestFields.output_config even though the structured
	// output path no longer writes output_config.format itself.
	outputConfigRaw, hasOutputConfig := result.AdditionalModelRequestFields.Get("output_config")
	require.True(t, hasOutputConfig, "expected output_config to exist from user-provided fields")
	outputConfig, ok := outputConfigRaw.(*schemas.OrderedMap)
	require.True(t, ok, "expected output_config to be an ordered map")
	foo, hasFoo := outputConfig.Get("foo")
	require.True(t, hasFoo, "expected output_config.foo to be preserved")
	assert.Equal(t, "bar", foo)

	// Existing top-level field (thinking) must not be lost.
	_, hasThinking := result.AdditionalModelRequestFields.Get("thinking")
	assert.True(t, hasThinking, "expected thinking to be preserved")

	// Incoming top-level keys must be merged.
	customField, hasCustomField := result.AdditionalModelRequestFields.Get("customField")
	require.True(t, hasCustomField, "expected customField to be merged")
	assert.Equal(t, "customValue", customField)
}

// TestNovaReasoningConfigUsesReasoningConfigField verifies that Nova models use
// the "reasoningConfig" field (camelCase) and NOT "thinking".
func TestNovaReasoningConfigUsesReasoningConfigField(t *testing.T) {
	bifrostReq := &schemas.BifrostChatRequest{
		Model: "amazon.nova-pro-v1",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: schemas.Ptr("Hello"),
				},
			},
		},
		Params: &schemas.ChatParameters{
			Reasoning: &schemas.ChatReasoning{
				Effort: schemas.Ptr("medium"),
			},
		},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	result, err := bedrock.ToBedrockChatCompletionRequest(ctx, bifrostReq)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.AdditionalModelRequestFields)

	// Nova should use reasoningConfig (camelCase)
	_, hasReasoningConfig := result.AdditionalModelRequestFields.Get("reasoningConfig")
	assert.True(t, hasReasoningConfig, "Nova models should use reasoningConfig field")

	// Nova should NOT use "thinking"
	_, hasThinking := result.AdditionalModelRequestFields.Get("thinking")
	assert.False(t, hasThinking, "Nova models should NOT use thinking field")
}

// TestNovaReasoningEffortClamped verifies efforts above Nova's enum (xhigh/max)
// are clamped to "high" so Nova doesn't 400 on an invalid maxReasoningEffort.
func TestNovaReasoningEffortClamped(t *testing.T) {
	cases := map[string]string{
		"xhigh":   "high",
		"max":     "high",
		"high":    "high",
		"medium":  "medium",
		"minimal": "low",
	}
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			bifrostReq := &schemas.BifrostChatRequest{
				Model: "amazon.nova-pro-v1",
				Input: []schemas.ChatMessage{
					{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Hello")}},
				},
				Params: &schemas.ChatParameters{Reasoning: &schemas.ChatReasoning{Effort: schemas.Ptr(in)}},
			}

			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			result, err := bedrock.ToBedrockChatCompletionRequest(ctx, bifrostReq)
			require.NoError(t, err)

			cfg, ok := result.AdditionalModelRequestFields.Get("reasoningConfig")
			require.True(t, ok, "expected reasoningConfig")
			cfgMap, ok := cfg.(map[string]any)
			require.True(t, ok)
			assert.Equal(t, want, cfgMap["maxReasoningEffort"], "effort %q should map to %q", in, want)
		})
	}
}

// TestReasoningSignatureEchoedOnlyWhenNonEmpty verifies that an empty reasoning
// signature is dropped before sending to Bedrock (MiniMax emits ""), while a real
// signature is preserved (Anthropic requires it). Keyed on the value, not the model.
func TestReasoningSignatureEchoedOnlyWhenNonEmpty(t *testing.T) {
	cases := map[string]struct {
		in   *string
		want *string
	}{
		"empty signature dropped":  {in: schemas.Ptr(""), want: nil},
		"nil signature dropped":    {in: nil, want: nil},
		"real signature preserved": {in: schemas.Ptr("ErUBCkYI"), want: schemas.Ptr("ErUBCkYI")},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			bifrostReq := &schemas.BifrostChatRequest{
				Model: "anthropic.claude-sonnet-4-5",
				Input: []schemas.ChatMessage{
					{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}},
					{
						Role:    schemas.ChatMessageRoleAssistant,
						Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("ok")},
						ChatAssistantMessage: &schemas.ChatAssistantMessage{
							ReasoningDetails: []schemas.ChatReasoningDetails{
								{Type: schemas.BifrostReasoningDetailsTypeText, Text: schemas.Ptr("thinking"), Signature: tc.in},
							},
						},
					},
				},
			}

			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			result, err := bedrock.ToBedrockChatCompletionRequest(ctx, bifrostReq)
			require.NoError(t, err)

			var got *string
			for _, m := range result.Messages {
				for _, b := range m.Content {
					if b.ReasoningContent != nil && b.ReasoningContent.ReasoningText != nil {
						got = b.ReasoningContent.ReasoningText.Signature
					}
				}
			}
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestStandaloneCachePointBlockHandling tests that standalone cachePoint content blocks
// (those with only cachePoint field and no type) are properly converted.
func TestStandaloneCachePointBlockHandling(t *testing.T) {
	t.Run("UserMessage_WithStandaloneCachePoint", func(t *testing.T) {
		bifrostReq := &schemas.BifrostChatRequest{
			Model: "anthropic.claude-3-sonnet-20240229-v1:0",
			Input: []schemas.ChatMessage{
				{
					Role: schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{
						ContentBlocks: []schemas.ChatContentBlock{
							{
								Type: schemas.ChatContentBlockTypeText,
								Text: schemas.Ptr("Hello, this is a test message"),
							},
							{
								// Standalone cachePoint block (no type, just cachePoint)
								CachePoint: &schemas.CachePoint{
									Type: "default",
								},
							},
						},
					},
				},
			},
		}

		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		result, err := bedrock.ToBedrockChatCompletionRequest(ctx, bifrostReq)
		require.NoError(t, err)
		require.NotNil(t, result)
		require.Len(t, result.Messages, 1)
		require.Len(t, result.Messages[0].Content, 2)

		// First block should be text
		assert.NotNil(t, result.Messages[0].Content[0].Text)
		assert.Equal(t, "Hello, this is a test message", *result.Messages[0].Content[0].Text)

		// Second block should be cachePoint
		assert.NotNil(t, result.Messages[0].Content[1].CachePoint)
		assert.Equal(t, bedrock.BedrockCachePointTypeDefault, result.Messages[0].Content[1].CachePoint.Type)
	})

	t.Run("BedrockNativeFormat_TextWithoutType", func(t *testing.T) {
		// This tests the Bedrock native format where text blocks don't have a "type" field
		// Example: {"text": "hello"} instead of {"type": "text", "text": "hello"}
		bifrostReq := &schemas.BifrostChatRequest{
			Model: "anthropic.claude-3-sonnet-20240229-v1:0",
			Input: []schemas.ChatMessage{
				{
					Role: schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{
						ContentBlocks: []schemas.ChatContentBlock{
							{
								// No Type field set, but Text is present (Bedrock native format)
								Text: schemas.Ptr("hello this is a test request"),
							},
							{
								// Standalone cachePoint block
								CachePoint: &schemas.CachePoint{
									Type: "default",
								},
							},
						},
					},
				},
			},
		}

		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		result, err := bedrock.ToBedrockChatCompletionRequest(ctx, bifrostReq)
		require.NoError(t, err)
		require.NotNil(t, result)
		require.Len(t, result.Messages, 1)
		require.Len(t, result.Messages[0].Content, 2)

		// First block should be text (even without explicit type)
		assert.NotNil(t, result.Messages[0].Content[0].Text)
		assert.Equal(t, "hello this is a test request", *result.Messages[0].Content[0].Text)

		// Second block should be cachePoint
		assert.NotNil(t, result.Messages[0].Content[1].CachePoint)
		assert.Equal(t, bedrock.BedrockCachePointTypeDefault, result.Messages[0].Content[1].CachePoint.Type)
	})

	t.Run("SystemMessage_WithStandaloneCachePoint", func(t *testing.T) {
		bifrostReq := &schemas.BifrostChatRequest{
			Model: "anthropic.claude-3-sonnet-20240229-v1:0",
			Input: []schemas.ChatMessage{
				{
					Role: schemas.ChatMessageRoleSystem,
					Content: &schemas.ChatMessageContent{
						ContentBlocks: []schemas.ChatContentBlock{
							{
								Type: schemas.ChatContentBlockTypeText,
								Text: schemas.Ptr("You are a helpful assistant"),
							},
							{
								// Standalone cachePoint block
								CachePoint: &schemas.CachePoint{
									Type: "default",
								},
							},
							{
								Type: schemas.ChatContentBlockTypeText,
								Text: schemas.Ptr("Additional system instructions"),
							},
						},
					},
				},
				{
					Role: schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{
						ContentStr: schemas.Ptr("Hello"),
					},
				},
			},
		}

		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		result, err := bedrock.ToBedrockChatCompletionRequest(ctx, bifrostReq)
		require.NoError(t, err)
		require.NotNil(t, result)
		require.NotNil(t, result.System)
		require.Len(t, result.System, 3) // Two text blocks + one cachePoint

		// First system message should be text
		assert.NotNil(t, result.System[0].Text)
		assert.Equal(t, "You are a helpful assistant", *result.System[0].Text)

		// Second should be cachePoint
		assert.NotNil(t, result.System[1].CachePoint)

		// Third should be text
		assert.NotNil(t, result.System[2].Text)
		assert.Equal(t, "Additional system instructions", *result.System[2].Text)
	})
}

func TestMultiTurnReasoningContentPassthrough(t *testing.T) {
	t.Parallel()

	t.Run("AssistantMessage_WithReasoningDetails_ConvertsToBedrockReasoningContent", func(t *testing.T) {
		reasoningText := "Let me think step by step..."
		signature := "abc123signature"
		assistantContent := "The answer is 42."

		bifrostReq := &schemas.BifrostChatRequest{
			Provider: schemas.Bedrock,
			Model:    "anthropic.claude-opus-4-6-v1",
			Input: []schemas.ChatMessage{
				{
					Role: schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{
						ContentStr: schemas.Ptr("What is the meaning of life?"),
					},
				},
				{
					Role: schemas.ChatMessageRoleAssistant,
					Content: &schemas.ChatMessageContent{
						ContentStr: &assistantContent,
					},
					ChatAssistantMessage: &schemas.ChatAssistantMessage{
						ReasoningDetails: []schemas.ChatReasoningDetails{
							{
								Index:     0,
								Type:      schemas.BifrostReasoningDetailsTypeText,
								Text:      &reasoningText,
								Signature: &signature,
							},
						},
					},
				},
				{
					Role: schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{
						ContentStr: schemas.Ptr("Can you elaborate?"),
					},
				},
			},
		}

		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		result, err := bedrock.ToBedrockChatCompletionRequest(ctx, bifrostReq)
		require.NoError(t, err)
		require.NotNil(t, result)

		// The assistant message (index 1) should have reasoning content blocks
		require.Len(t, result.Messages, 3) // user, assistant, user
		assistantMsg := result.Messages[1]
		assert.Equal(t, bedrock.BedrockMessageRoleAssistant, assistantMsg.Role)

		// Should have text block + reasoning content block
		require.GreaterOrEqual(t, len(assistantMsg.Content), 2)

		// Find the reasoning content block
		var foundReasoning bool
		for _, block := range assistantMsg.Content {
			if block.ReasoningContent != nil {
				foundReasoning = true
				require.NotNil(t, block.ReasoningContent.ReasoningText)
				assert.Equal(t, &reasoningText, block.ReasoningContent.ReasoningText.Text)
				assert.Equal(t, &signature, block.ReasoningContent.ReasoningText.Signature)
			}
		}
		assert.True(t, foundReasoning, "Expected reasoning content block in assistant message")
	})

	t.Run("AssistantMessage_WithReasoningAndToolCalls_ReasoningComesFirst", func(t *testing.T) {
		reasoningText := "I need to call a tool to answer this."
		signature := "sig_abc123"
		assistantContent := "Let me check that for you."
		toolCallID := "tooluse_abc123"

		bifrostReq := &schemas.BifrostChatRequest{
			Provider: schemas.Bedrock,
			Model:    "anthropic.claude-sonnet-4-6",
			Input: []schemas.ChatMessage{
				{
					Role:    schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("What time is it?")},
				},
				{
					Role:    schemas.ChatMessageRoleAssistant,
					Content: &schemas.ChatMessageContent{ContentStr: &assistantContent},
					ChatAssistantMessage: &schemas.ChatAssistantMessage{
						ReasoningDetails: []schemas.ChatReasoningDetails{
							{
								Index:     0,
								Type:      schemas.BifrostReasoningDetailsTypeText,
								Text:      &reasoningText,
								Signature: &signature,
							},
						},
						ToolCalls: []schemas.ChatAssistantMessageToolCall{
							{
								ID:   &toolCallID,
								Type: schemas.Ptr("function"),
								Function: schemas.ChatAssistantMessageToolCallFunction{
									Name:      schemas.Ptr("get_time"),
									Arguments: "{}",
								},
							},
						},
					},
				},
			},
		}

		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		result, err := bedrock.ToBedrockChatCompletionRequest(ctx, bifrostReq)
		require.NoError(t, err)
		require.NotNil(t, result)

		assistantMsg := result.Messages[1]
		// reasoning + text + tool_use = at least 3 blocks
		require.GreaterOrEqual(t, len(assistantMsg.Content), 3)

		// Reasoning MUST be the first block
		assert.NotNil(t, assistantMsg.Content[0].ReasoningContent,
			"reasoning block must be first content block; got %+v", assistantMsg.Content[0])

		// tool_use must appear after reasoning
		var foundToolUse bool
		for _, block := range assistantMsg.Content[1:] {
			if block.ToolUse != nil {
				foundToolUse = true
				break
			}
		}
		assert.True(t, foundToolUse, "tool_use block must appear after reasoning block")
	})

	t.Run("AssistantMessage_WithoutReasoningDetails_NoReasoningContent", func(t *testing.T) {
		assistantContent := "Simple response"

		bifrostReq := &schemas.BifrostChatRequest{
			Provider: schemas.Bedrock,
			Model:    "anthropic.claude-opus-4-6-v1",
			Input: []schemas.ChatMessage{
				{
					Role: schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{
						ContentStr: schemas.Ptr("Hello"),
					},
				},
				{
					Role: schemas.ChatMessageRoleAssistant,
					Content: &schemas.ChatMessageContent{
						ContentStr: &assistantContent,
					},
				},
				{
					Role: schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{
						ContentStr: schemas.Ptr("Hi again"),
					},
				},
			},
		}

		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		result, err := bedrock.ToBedrockChatCompletionRequest(ctx, bifrostReq)
		require.NoError(t, err)
		require.NotNil(t, result)

		assistantMsg := result.Messages[1]
		for _, block := range assistantMsg.Content {
			assert.Nil(t, block.ReasoningContent, "Should not have reasoning content without ReasoningDetails")
		}
	})
}

func TestDocumentFormatMapping(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		fileType       string
		expectedFormat string
	}{
		{"PDF_MimeType", "application/pdf", "pdf"},
		{"PDF_Short", "pdf", "pdf"},
		{"TXT_MimeType", "text/plain", "txt"},
		{"TXT_Short", "txt", "txt"},
		{"Markdown_MimeType", "text/markdown", "md"},
		{"Markdown_Short", "md", "md"},
		{"HTML_MimeType", "text/html", "html"},
		{"HTML_Short", "html", "html"},
		{"CSV_MimeType", "text/csv", "csv"},
		{"CSV_Short", "csv", "csv"},
		{"DOC_MimeType", "application/msword", "doc"},
		{"DOC_Short", "doc", "doc"},
		{"DOCX_MimeType", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", "docx"},
		{"DOCX_Short", "docx", "docx"},
		{"XLS_MimeType", "application/vnd.ms-excel", "xls"},
		{"XLS_Short", "xls", "xls"},
		{"XLSX_MimeType", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", "xlsx"},
		{"XLSX_Short", "xlsx", "xlsx"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fileData := "Hello World" // plain text; base64 requires a data: URL prefix
			bifrostReq := &schemas.BifrostChatRequest{
				Provider: schemas.Bedrock,
				Model:    "anthropic.claude-3-5-sonnet-v2",
				Input: []schemas.ChatMessage{
					{
						Role: schemas.ChatMessageRoleUser,
						Content: &schemas.ChatMessageContent{
							ContentBlocks: []schemas.ChatContentBlock{
								{
									Type: schemas.ChatContentBlockTypeFile,
									File: &schemas.ChatInputFile{
										Filename: schemas.Ptr("testfile"),
										FileType: &tt.fileType,
										FileData: &fileData,
									},
								},
							},
						},
					},
				},
			}

			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			result, err := bedrock.ToBedrockChatCompletionRequest(ctx, bifrostReq)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Len(t, result.Messages, 1)
			require.Len(t, result.Messages[0].Content, 1)
			require.NotNil(t, result.Messages[0].Content[0].Document)
			assert.Equal(t, tt.expectedFormat, result.Messages[0].Content[0].Document.Format,
				"File type %q should map to format %q", tt.fileType, tt.expectedFormat)
		})
	}
}

func TestBedrockStopReasonMapping(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		bedrockStopReason string
		expectedBifrost   string
	}{
		{"EndTurn", "end_turn", "stop"},
		{"MaxTokens", "max_tokens", "length"},
		{"StopSequence", "stop_sequence", "stop"},
		{"ToolUse", "tool_use", "tool_calls"},
		{"GuardrailIntervened", "guardrail_intervened", "guardrail_intervened"}, // no clean mapping — passes through
		{"ContentFiltered", "content_filtered", "content_filter"},
		{"UnknownReason", "some_unknown_reason", "some_unknown_reason"}, // no clean mapping — passes through
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := &bedrock.BedrockConverseResponse{
				Output: &bedrock.BedrockConverseOutput{
					Message: &bedrock.BedrockMessage{
						Role: bedrock.BedrockMessageRoleAssistant,
						Content: []bedrock.BedrockContentBlock{
							{Text: schemas.Ptr("Response text")},
						},
					},
				},
				StopReason: tt.bedrockStopReason,
				Usage: &bedrock.BedrockTokenUsage{
					InputTokens:  10,
					OutputTokens: 5,
					TotalTokens:  15,
				},
			}

			bifrostResp, err := response.ToBifrostChatResponse(context.Background(), "test-model")
			require.NoError(t, err)
			require.NotNil(t, bifrostResp)
			require.Len(t, bifrostResp.Choices, 1)
			require.NotNil(t, bifrostResp.Choices[0].FinishReason)
			assert.Equal(t, tt.expectedBifrost, *bifrostResp.Choices[0].FinishReason,
				"Bedrock stop reason %q should map to %q", tt.bedrockStopReason, tt.expectedBifrost)
		})
	}
}

// TestBedrockStopReasonMappingResponsesPath tests stop reason normalisation for
// the Responses API path (BedrockConverseResponse.ToBifrostResponsesResponse).
func TestBedrockStopReasonMappingResponsesPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                      string
		bedrockReason             string
		expectedBifrost           string
		expectedStatus            string // "" means Status should be nil
		expectedIncompleteDetails string // "" means IncompleteDetails should be nil
	}{
		{"EndTurn", "end_turn", "stop", "completed", ""},
		{"MaxTokens", "max_tokens", "length", "incomplete", "max_output_tokens"},
		{"StopSequence", "stop_sequence", "stop", "completed", ""},
		{"ToolUse", "tool_use", "tool_calls", "completed", ""},
		{"ContentFiltered", "content_filtered", "content_filter", "", ""},               // no clean mapping — passes through, no Status
		{"GuardrailIntervened", "guardrail_intervened", "guardrail_intervened", "", ""}, // no clean mapping — passes through, no Status
		{"UnknownReason", "some_unknown_reason", "some_unknown_reason", "", ""},         // no clean mapping — passes through, no Status
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := &bedrock.BedrockConverseResponse{
				StopReason: tt.bedrockReason,
				Output: &bedrock.BedrockConverseOutput{
					Message: &bedrock.BedrockMessage{
						Role: bedrock.BedrockMessageRoleAssistant,
						Content: []bedrock.BedrockContentBlock{
							{Text: schemas.Ptr("Response text")},
						},
					},
				},
			}

			bifrostResp, err := response.ToBifrostResponsesResponse(ctx)
			require.NoError(t, err)
			require.NotNil(t, bifrostResp)
			require.NotNil(t, bifrostResp.StopReason, "StopReason should be set")
			assert.Equal(t, tt.expectedBifrost, *bifrostResp.StopReason,
				"Bedrock stop reason %q should map to %q in responses path", tt.bedrockReason, tt.expectedBifrost)

			// Status + IncompleteDetails mirror the OpenAI Responses-API spec.
			// Truncation must surface as Status="incomplete" with the canonical
			// IncompleteDetails.Reason; clean completions get Status="completed";
			// unmapped reasons leave both unset (preserves prior behavior).
			if tt.expectedStatus == "" {
				assert.Nil(t, bifrostResp.Status, "Status must be nil for unmapped stop reasons")
				assert.Nil(t, bifrostResp.IncompleteDetails, "IncompleteDetails must be nil for unmapped stop reasons")
			} else {
				require.NotNil(t, bifrostResp.Status, "Status must be set for mapped stop reason %q", tt.bedrockReason)
				assert.Equal(t, tt.expectedStatus, *bifrostResp.Status)
			}
			if tt.expectedIncompleteDetails == "" {
				assert.Nil(t, bifrostResp.IncompleteDetails)
			} else {
				require.NotNil(t, bifrostResp.IncompleteDetails)
				assert.Equal(t, tt.expectedIncompleteDetails, bifrostResp.IncompleteDetails.Reason)
			}
		})
	}
}

// TestFinalizeBedrockStream_MaxTokensTruncation guards the streaming counterpart:
// when Bedrock's stopReason is "max_tokens", the terminal SSE event must be
// response.incomplete (not response.completed) and the embedded response must
// carry Status="incomplete" + IncompleteDetails.Reason="max_output_tokens" so
// streaming consumers can detect truncation.
func TestFinalizeBedrockStream_MaxTokensTruncation(t *testing.T) {
	state := bedrock.NewBedrockResponsesStreamState()
	state.StopReason = schemas.Ptr("length") // mapped from bedrock's "max_tokens"
	usage := &schemas.ResponsesResponseUsage{InputTokens: 30, OutputTokens: 15, TotalTokens: 45}

	finalResponses := bedrock.FinalizeBedrockStream(state, 0, usage, nil)
	require.NotEmpty(t, finalResponses)

	terminal := finalResponses[len(finalResponses)-1]
	assert.Equal(t, schemas.ResponsesStreamResponseTypeIncomplete, terminal.Type,
		"terminal event must be response.incomplete on max_output_tokens truncation")
	require.NotNil(t, terminal.Response)
	require.NotNil(t, terminal.Response.Status)
	assert.Equal(t, "incomplete", *terminal.Response.Status)
	require.NotNil(t, terminal.Response.IncompleteDetails)
	assert.Equal(t, "max_output_tokens", terminal.Response.IncompleteDetails.Reason)
}

// TestFinalizeBedrockStream_CleanCompletionUnaffected verifies non-truncation
// stop reasons still produce response.completed with Status="completed".
func TestFinalizeBedrockStream_CleanCompletionUnaffected(t *testing.T) {
	state := bedrock.NewBedrockResponsesStreamState()
	state.StopReason = schemas.Ptr("stop")
	usage := &schemas.ResponsesResponseUsage{InputTokens: 5, OutputTokens: 10, TotalTokens: 15}

	finalResponses := bedrock.FinalizeBedrockStream(state, 0, usage, nil)
	require.NotEmpty(t, finalResponses)

	terminal := finalResponses[len(finalResponses)-1]
	assert.Equal(t, schemas.ResponsesStreamResponseTypeCompleted, terminal.Type)
	require.NotNil(t, terminal.Response)
	require.NotNil(t, terminal.Response.Status)
	assert.Equal(t, "completed", *terminal.Response.Status)
	assert.Nil(t, terminal.Response.IncompleteDetails)
}

// TestFinalizeBedrockStream_UnmappedReasonLeavesStatusUnset keeps the streaming
// path aligned with the non-streaming mapping: an unmapped stop reason (e.g.
// content_filter) ends the stream as response.completed but must leave Status
// unset rather than asserting "completed".
func TestFinalizeBedrockStream_UnmappedReasonLeavesStatusUnset(t *testing.T) {
	state := bedrock.NewBedrockResponsesStreamState()
	state.StopReason = schemas.Ptr("content_filter")
	usage := &schemas.ResponsesResponseUsage{InputTokens: 5, OutputTokens: 10, TotalTokens: 15}

	finalResponses := bedrock.FinalizeBedrockStream(state, 0, usage, nil)
	require.NotEmpty(t, finalResponses)

	terminal := finalResponses[len(finalResponses)-1]
	assert.Equal(t, schemas.ResponsesStreamResponseTypeCompleted, terminal.Type)
	require.NotNil(t, terminal.Response)
	assert.Nil(t, terminal.Response.Status, "unmapped stop reasons must leave Status unset, matching the non-streaming path")
	assert.Nil(t, terminal.Response.IncompleteDetails)
}

// TestBifrostToBedrockStopReasonReverseMapping tests the reverse conversion
// (BifrostResponsesResponse.StopReason → BedrockConverseResponse.StopReason).
func TestBifrostToBedrockStopReasonReverseMapping(t *testing.T) {
	t.Parallel()

	textOutput := []schemas.ResponsesMessage{
		{
			Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
			Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
			Content: &schemas.ResponsesMessageContent{
				ContentBlocks: []schemas.ResponsesMessageContentBlock{
					{
						Type: schemas.ResponsesOutputMessageContentTypeText,
						Text: schemas.Ptr("Hello"),
					},
				},
			},
		},
	}

	tests := []struct {
		name              string
		stopReason        *string
		incompleteDetails *schemas.ResponsesResponseIncompleteDetails
		expectedBedrock   string
	}{
		{"Stop", schemas.Ptr("stop"), nil, "end_turn"},
		{"Length", schemas.Ptr("length"), nil, "max_tokens"},
		{"ToolCalls", schemas.Ptr("tool_calls"), nil, "tool_use"},
		{"ContentFilter", schemas.Ptr("content_filter"), nil, "content_filtered"},
		{"GuardrailIntervened", schemas.Ptr("guardrail_intervened"), nil, "guardrail_intervened"}, // passes through
		{"UnknownPassthrough", schemas.Ptr("some_unknown_reason"), nil, "some_unknown_reason"},    // passes through
		{
			// StopReason takes priority over IncompleteDetails
			name:              "StopReasonOverridesIncompleteDetails",
			stopReason:        schemas.Ptr("stop"),
			incompleteDetails: &schemas.ResponsesResponseIncompleteDetails{Reason: "max_tokens"},
			expectedBedrock:   "end_turn",
		},
		{
			// IncompleteDetails is used when StopReason is nil
			name:              "IncompleteDetailsFallback",
			stopReason:        nil,
			incompleteDetails: &schemas.ResponsesResponseIncompleteDetails{Reason: "max_tokens"},
			expectedBedrock:   "max_tokens",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := &schemas.BifrostResponsesResponse{
				Output:            textOutput,
				StopReason:        tt.stopReason,
				IncompleteDetails: tt.incompleteDetails,
			}

			actual, err := bedrock.ToBedrockConverseResponse(input)
			require.NoError(t, err)
			require.NotNil(t, actual)
			assert.Equal(t, tt.expectedBedrock, actual.StopReason,
				"Bifrost stop reason %v should reverse-map to Bedrock %q", tt.stopReason, tt.expectedBedrock)
		})
	}
}

func TestGuardrailConfigStreamProcessingMode(t *testing.T) {
	t.Parallel()

	t.Run("WithStreamProcessingMode", func(t *testing.T) {
		mode := "async"
		bifrostReq := &schemas.BifrostChatRequest{
			Provider: schemas.Bedrock,
			Model:    "anthropic.claude-3-5-sonnet-v2",
			Input: []schemas.ChatMessage{
				{
					Role: schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{
						ContentStr: schemas.Ptr("Hello"),
					},
				},
			},
			Params: &schemas.ChatParameters{
				ExtraParams: map[string]interface{}{
					"guardrailConfig": map[string]interface{}{
						"guardrailIdentifier":  "test-guardrail",
						"guardrailVersion":     "1",
						"trace":                "enabled",
						"streamProcessingMode": mode,
					},
				},
			},
		}

		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		result, err := bedrock.ToBedrockChatCompletionRequest(ctx, bifrostReq)
		require.NoError(t, err)
		require.NotNil(t, result)
		require.NotNil(t, result.GuardrailConfig)
		assert.Equal(t, "test-guardrail", result.GuardrailConfig.GuardrailIdentifier)
		assert.Equal(t, "1", result.GuardrailConfig.GuardrailVersion)
		require.NotNil(t, result.GuardrailConfig.Trace)
		assert.Equal(t, "enabled", *result.GuardrailConfig.Trace)
		require.NotNil(t, result.GuardrailConfig.StreamProcessingMode)
		assert.Equal(t, mode, *result.GuardrailConfig.StreamProcessingMode)
	})

	t.Run("WithoutStreamProcessingMode", func(t *testing.T) {
		bifrostReq := &schemas.BifrostChatRequest{
			Provider: schemas.Bedrock,
			Model:    "anthropic.claude-3-5-sonnet-v2",
			Input: []schemas.ChatMessage{
				{
					Role: schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{
						ContentStr: schemas.Ptr("Hello"),
					},
				},
			},
			Params: &schemas.ChatParameters{
				ExtraParams: map[string]interface{}{
					"guardrailConfig": map[string]interface{}{
						"guardrailIdentifier": "test-guardrail",
						"guardrailVersion":    "1",
					},
				},
			},
		}

		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		result, err := bedrock.ToBedrockChatCompletionRequest(ctx, bifrostReq)
		require.NoError(t, err)
		require.NotNil(t, result)
		require.NotNil(t, result.GuardrailConfig)
		assert.Nil(t, result.GuardrailConfig.StreamProcessingMode)
	})
}

func TestToolChoiceAutoHandling(t *testing.T) {
	t.Parallel()

	t.Run("AutoToolChoice_OmitsToolChoice", func(t *testing.T) {
		autoStr := "auto"
		bifrostReq := &schemas.BifrostChatRequest{
			Provider: schemas.Bedrock,
			Model:    "anthropic.claude-3-5-sonnet-v2",
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
							Description: schemas.Ptr("Get weather"),
							Parameters: &schemas.ToolFunctionParameters{
								Type:       "object",
								Properties: &testProps,
							},
						},
					},
				},
				ToolChoice: &schemas.ChatToolChoice{
					ChatToolChoiceStr: &autoStr,
				},
			},
		}

		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		result, err := bedrock.ToBedrockChatCompletionRequest(ctx, bifrostReq)
		require.NoError(t, err)
		require.NotNil(t, result)
		require.NotNil(t, result.ToolConfig)
		assert.Nil(t, result.ToolConfig.ToolChoice, "Auto tool choice should be omitted (nil) as it's the default")
	})

	t.Run("RequiredToolChoice_SetsAny", func(t *testing.T) {
		requiredStr := "required"
		bifrostReq := &schemas.BifrostChatRequest{
			Provider: schemas.Bedrock,
			Model:    "anthropic.claude-3-5-sonnet-v2",
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
							Description: schemas.Ptr("Get weather"),
							Parameters: &schemas.ToolFunctionParameters{
								Type:       "object",
								Properties: &testProps,
							},
						},
					},
				},
				ToolChoice: &schemas.ChatToolChoice{
					ChatToolChoiceStr: &requiredStr,
				},
			},
		}

		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		result, err := bedrock.ToBedrockChatCompletionRequest(ctx, bifrostReq)
		require.NoError(t, err)
		require.NotNil(t, result)
		require.NotNil(t, result.ToolConfig)
		require.NotNil(t, result.ToolConfig.ToolChoice)
		assert.NotNil(t, result.ToolConfig.ToolChoice.Any, "Required tool choice should map to Any")
	})
}

func TestDocumentFormatResponseMapping(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		bedrockFormat    string
		expectedMimeType string
	}{
		{"PDF", "pdf", "application/pdf"},
		{"TXT", "txt", "text/plain"},
		{"Markdown", "md", "text/markdown"},
		{"HTML", "html", "text/html"},
		{"CSV", "csv", "text/csv"},
		{"DOC", "doc", "application/msword"},
		{"DOCX", "docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document"},
		{"XLS", "xls", "application/vnd.ms-excel"},
		{"XLSX", "xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"},
		{"Unknown", "xyz", "application/pdf"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			docBytes := "SGVsbG8=" // base64 "Hello"
			response := &bedrock.BedrockConverseResponse{
				Output: &bedrock.BedrockConverseOutput{
					Message: &bedrock.BedrockMessage{
						Role: bedrock.BedrockMessageRoleAssistant,
						Content: []bedrock.BedrockContentBlock{
							{
								Document: &bedrock.BedrockDocumentSource{
									Format: tt.bedrockFormat,
									Name:   "testdoc",
									Source: &bedrock.BedrockDocumentSourceData{
										Bytes: &docBytes,
									},
								},
							},
						},
					},
				},
				StopReason: "end_turn",
				Usage: &bedrock.BedrockTokenUsage{
					InputTokens:  10,
					OutputTokens: 5,
					TotalTokens:  15,
				},
			}

			bifrostResp, err := response.ToBifrostChatResponse(context.Background(), "test-model")
			require.NoError(t, err)
			require.NotNil(t, bifrostResp)
			require.Len(t, bifrostResp.Choices, 1)

			choice := bifrostResp.Choices[0]
			require.NotNil(t, choice.ChatNonStreamResponseChoice)
			require.NotNil(t, choice.ChatNonStreamResponseChoice.Message)
			require.NotNil(t, choice.ChatNonStreamResponseChoice.Message.Content)

			blocks := choice.ChatNonStreamResponseChoice.Message.Content.ContentBlocks
			require.Len(t, blocks, 1)
			assert.Equal(t, schemas.ChatContentBlockTypeFile, blocks[0].Type)
			require.NotNil(t, blocks[0].File)
			require.NotNil(t, blocks[0].File.FileType)
			assert.Equal(t, tt.expectedMimeType, *blocks[0].File.FileType,
				"Bedrock format %q should map to MIME type %q", tt.bedrockFormat, tt.expectedMimeType)
		})
	}
}

// TestDocumentFormatResponsesPathRoundTrip verifies that the Bedrock Converse
// responses path (Bedrock -> Bifrost -> Bedrock) preserves the document format
// for every Bedrock-supported format. This guards against the regression where
// xlsx/xls/doc/docx (and md/html/csv) collapsed to "pdf"/"txt" because the
// responses-path converters lagged behind the chat path. See issue #4622.
func TestDocumentFormatResponsesPathRoundTrip(t *testing.T) {
	t.Parallel()

	formats := []string{"pdf", "txt", "md", "html", "csv", "doc", "docx", "xls", "xlsx"}

	for _, format := range formats {
		t.Run(format, func(t *testing.T) {
			docBytes := "UEsDBA==" // arbitrary base64; only the format mapping is under test
			bedrockMessages := []bedrock.BedrockMessage{
				{
					Role: bedrock.BedrockMessageRoleUser,
					Content: []bedrock.BedrockContentBlock{
						{
							Document: &bedrock.BedrockDocumentSource{
								Format: format,
								Name:   "testdoc",
								Source: &bedrock.BedrockDocumentSourceData{
									Bytes: &docBytes,
								},
							},
						},
					},
				},
			}

			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

			// Inbound: Bedrock -> Bifrost responses messages
			bifrostMessages := bedrock.ConvertBedrockMessagesToBifrostMessages(ctx, bedrockMessages, nil, false)
			require.NotEmpty(t, bifrostMessages, "expected at least one Bifrost message")

			// Outbound: Bifrost responses messages -> Bedrock
			roundTripped, _, err := bedrock.ConvertBifrostMessagesToBedrockMessages(ctx, bifrostMessages, false)
			require.NoError(t, err)
			require.NotEmpty(t, roundTripped, "expected at least one Bedrock message after round-trip")

			// Locate the document block in the round-tripped output.
			var doc *bedrock.BedrockDocumentSource
			for _, msg := range roundTripped {
				for _, block := range msg.Content {
					if block.Document != nil {
						doc = block.Document
					}
				}
			}
			require.NotNil(t, doc, "document block should survive the round-trip")
			assert.Equal(t, format, doc.Format,
				"format %q should be preserved through the responses path, got %q", format, doc.Format)
		})
	}
}

// TestBedrockToolInputKeyOrderPreservation verifies that multiple parallel tool calls
// preserve the client's original key ordering after conversion to Bedrock format.
func TestBedrockToolInputKeyOrderPreservation(t *testing.T) {
	bifrostReq := &schemas.BifrostChatRequest{
		Model: "anthropic.claude-3-sonnet",
		Input: []schemas.ChatMessage{
			{
				Role:    schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("test")},
			},
			{
				Role: schemas.ChatMessageRoleAssistant,
				ChatAssistantMessage: &schemas.ChatAssistantMessage{
					ToolCalls: []schemas.ChatAssistantMessageToolCall{
						{
							Index: 0,
							Type:  schemas.Ptr("function"),
							ID:    schemas.Ptr("toolu_001"),
							Function: schemas.ChatAssistantMessageToolCallFunction{
								Name:      schemas.Ptr("bash"),
								Arguments: `{"description":"Find references quickly","timeout":30000,"command":"grep -r auth_injector ."}`,
							},
						},
						{
							Index: 1,
							Type:  schemas.Ptr("function"),
							ID:    schemas.Ptr("toolu_002"),
							Function: schemas.ChatAssistantMessageToolCallFunction{
								Name:      schemas.Ptr("bash"),
								Arguments: `{"command":"git diff main...HEAD --stat","description":"Show diff of commits"}`,
							},
						},
						{
							Index: 2,
							Type:  schemas.Ptr("function"),
							ID:    schemas.Ptr("toolu_003"),
							Function: schemas.ChatAssistantMessageToolCallFunction{
								Name:      schemas.Ptr("bash"),
								Arguments: `{"command":"git log main..HEAD","description":"Show commits in branch"}`,
							},
						},
					},
				},
			},
		},
	}

	ctx, cancel := schemas.NewBifrostContextWithCancel(nil)
	defer cancel()
	result, err := bedrock.ToBedrockChatCompletionRequest(ctx, bifrostReq)
	require.NoError(t, err)

	// Collect all tool use content blocks from assistant messages
	var toolUseInputs []interface{}
	for _, msg := range result.Messages {
		for _, block := range msg.Content {
			if block.ToolUse != nil {
				toolUseInputs = append(toolUseInputs, block.ToolUse.Input)
			}
		}
	}

	require.Len(t, toolUseInputs, 3, "expected 3 tool use blocks")

	// Block 0: keys should be description, timeout, command (NOT alphabetical)
	json0, _ := json.Marshal(toolUseInputs[0])
	s0 := string(json0)
	descIdx0 := strings.Index(s0, `"description"`)
	timeIdx0 := strings.Index(s0, `"timeout"`)
	cmdIdx0 := strings.Index(s0, `"command"`)
	if descIdx0 < 0 || timeIdx0 < 0 || cmdIdx0 < 0 {
		t.Fatalf("block 0: missing expected key(s) in: %s", s0)
	}
	assert.True(t, descIdx0 < timeIdx0 && timeIdx0 < cmdIdx0,
		"block 0: key order not preserved, expected description < timeout < command in: %s", s0)

	// Block 1: keys should be command, description (NOT alphabetical)
	json1, _ := json.Marshal(toolUseInputs[1])
	s1 := string(json1)
	cmdIdx1 := strings.Index(s1, `"command"`)
	descIdx1 := strings.Index(s1, `"description"`)
	if cmdIdx1 < 0 || descIdx1 < 0 {
		t.Fatalf("block 1: missing expected key(s) in: %s", s1)
	}
	assert.True(t, cmdIdx1 < descIdx1,
		"block 1: key order not preserved, expected command < description in: %s", s1)

	// Block 2: keys should be command, description
	json2, _ := json.Marshal(toolUseInputs[2])
	s2 := string(json2)
	cmdIdx2 := strings.Index(s2, `"command"`)
	descIdx2 := strings.Index(s2, `"description"`)
	if cmdIdx2 < 0 || descIdx2 < 0 {
		t.Fatalf("block 2: missing expected key(s) in: %s", s2)
	}
	assert.True(t, cmdIdx2 < descIdx2,
		"block 2: key order not preserved, expected command < description in: %s", s2)
}

// TestToBedrockInvokeMessagesStreamResponse_NoDuplicateContentBlockStop verifies that
// ContentPartDone does not emit a content_block_stop event (only OutputItemDone does),
// preventing duplicate content_block_stop events in the stream. (Issue #2293)
func TestToBedrockInvokeMessagesStreamResponse_NoDuplicateContentBlockStop(t *testing.T) {
	ctx := &schemas.BifrostContext{}
	contentIdx := 0
	model := "anthropic.claude-sonnet-4-5-20250929-v1:0"

	// Simulate the sequence FinalizeBedrockStream emits for a text block:
	// 1. OutputTextDone  — should be skipped
	// 2. ContentPartDone — should be skipped (was previously emitting content_block_stop)
	// 3. OutputItemDone  — should emit content_block_stop
	events := []*schemas.BifrostResponsesStreamResponse{
		{
			Type:         schemas.ResponsesStreamResponseTypeOutputTextDone,
			ContentIndex: &contentIdx,
			ExtraFields:  schemas.BifrostResponseExtraFields{OriginalModelRequested: model},
		},
		{
			Type:         schemas.ResponsesStreamResponseTypeContentPartDone,
			ContentIndex: &contentIdx,
			ExtraFields:  schemas.BifrostResponseExtraFields{OriginalModelRequested: model},
		},
		{
			Type:         schemas.ResponsesStreamResponseTypeOutputItemDone,
			ContentIndex: &contentIdx,
			ExtraFields:  schemas.BifrostResponseExtraFields{OriginalModelRequested: model},
		},
	}

	type bedrockChunk struct {
		InvokeModelRawChunks [][]byte `json:"invokeModelRawChunks"`
	}

	var stopCount int
	for _, ev := range events {
		_, result, err := bedrock.ToBedrockInvokeMessagesStreamResponse(ctx, ev)
		require.NoError(t, err)
		if result == nil {
			continue
		}
		raw, err := json.Marshal(result)
		require.NoError(t, err)
		var chunk bedrockChunk
		require.NoError(t, json.Unmarshal(raw, &chunk))
		for _, rawChunk := range chunk.InvokeModelRawChunks {
			if strings.Contains(string(rawChunk), "content_block_stop") {
				stopCount++
			}
		}
	}

	assert.Equal(t, 1, stopCount, "expected exactly one content_block_stop event, got %d", stopCount)
}

func TestToolResultImageContentResponsesAPI(t *testing.T) {
	// Minimal 1x1 red PNG
	pngBase64 := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAIAAACQd1PeAAAADElEQVR4nGP4z8AAAAMBAQDJ/pLvAAAAAElFTkSuQmCC"

	t.Run("ImageBlockPreservedInToolResult", func(t *testing.T) {
		input := []schemas.ResponsesMessage{
			{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID: schemas.Ptr("tooluse_screenshot_001"),
					Output: &schemas.ResponsesToolMessageOutputStruct{
						ResponsesFunctionToolCallOutputBlocks: []schemas.ResponsesMessageContentBlock{
							{
								Type: schemas.ResponsesInputMessageContentBlockTypeImage,
								ResponsesInputMessageContentBlockImage: &schemas.ResponsesInputMessageContentBlockImage{
									ImageURL: schemas.Ptr("data:image/png;base64," + pngBase64),
								},
							},
						},
					},
				},
			},
		}

		messages, _, err := bedrock.ConvertBifrostMessagesToBedrockMessages(context.Background(), input, false)
		require.NoError(t, err)
		require.Len(t, messages, 1)

		toolResultMsg := messages[0]
		assert.Equal(t, bedrock.BedrockMessageRoleUser, toolResultMsg.Role)
		require.Len(t, toolResultMsg.Content, 1)

		toolResult := toolResultMsg.Content[0].ToolResult
		require.NotNil(t, toolResult, "expected tool result in content block")
		assert.Equal(t, "tooluse_screenshot_001", toolResult.ToolUseID)
		require.Len(t, toolResult.Content, 1, "tool result should contain exactly one content block")

		imageBlock := toolResult.Content[0]
		require.NotNil(t, imageBlock.Image, "tool result content should be an image")
		assert.Equal(t, "png", imageBlock.Image.Format)
		require.NotNil(t, imageBlock.Image.Source.Bytes)
		assert.Equal(t, pngBase64, *imageBlock.Image.Source.Bytes)
	})

	t.Run("MixedTextAndImageBlocksPreserved", func(t *testing.T) {
		input := []schemas.ResponsesMessage{
			{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID: schemas.Ptr("tooluse_mixed_002"),
					Output: &schemas.ResponsesToolMessageOutputStruct{
						ResponsesFunctionToolCallOutputBlocks: []schemas.ResponsesMessageContentBlock{
							{
								Type: schemas.ResponsesOutputMessageContentTypeText,
								Text: schemas.Ptr("Screenshot captured successfully"),
							},
							{
								Type: schemas.ResponsesInputMessageContentBlockTypeImage,
								ResponsesInputMessageContentBlockImage: &schemas.ResponsesInputMessageContentBlockImage{
									ImageURL: schemas.Ptr("data:image/png;base64," + pngBase64),
								},
							},
						},
					},
				},
			},
		}

		messages, _, err := bedrock.ConvertBifrostMessagesToBedrockMessages(context.Background(), input, false)
		require.NoError(t, err)
		require.Len(t, messages, 1)

		toolResult := messages[0].Content[0].ToolResult
		require.NotNil(t, toolResult)
		require.Len(t, toolResult.Content, 2, "both text and image blocks should be preserved")

		assert.NotNil(t, toolResult.Content[0].Text, "first block should be text")
		assert.NotNil(t, toolResult.Content[1].Image, "second block should be image")
		assert.Equal(t, "png", toolResult.Content[1].Image.Format)
	})

	t.Run("RemoteURLImageGracefullyDropped", func(t *testing.T) {
		input := []schemas.ResponsesMessage{
			{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID: schemas.Ptr("tooluse_remote_003"),
					Output: &schemas.ResponsesToolMessageOutputStruct{
						ResponsesFunctionToolCallOutputBlocks: []schemas.ResponsesMessageContentBlock{
							{
								Type: schemas.ResponsesInputMessageContentBlockTypeImage,
								ResponsesInputMessageContentBlockImage: &schemas.ResponsesInputMessageContentBlockImage{
									ImageURL: schemas.Ptr("https://example.com/screenshot.png"),
								},
							},
						},
					},
				},
			},
		}

		messages, _, err := bedrock.ConvertBifrostMessagesToBedrockMessages(context.Background(), input, false)
		require.NoError(t, err)
		require.Len(t, messages, 1)

		toolResult := messages[0].Content[0].ToolResult
		require.NotNil(t, toolResult)
		assert.Empty(t, toolResult.Content, "remote URL image should be dropped (Bedrock only supports base64)")
	})
}

// TestBedrockLlamaChatStructuredOutputOmitsForcedToolChoice locks in the
// per-model gate for Meta Llama on Bedrock. Bedrock Converse rejects
// `toolConfig.toolChoice.tool` on Llama variants with HTTP 400
// ("This model doesn't support the toolConfig.toolChoice.tool field. Remove
// toolConfig.toolChoice.tool and try again."). The synthetic `bf_so_*` tool
// is still injected — Llama receives a single tool to call — but no forced
// tool_choice is emitted. With one tool bound and Bedrock's default "auto"
// behavior, the structured-output contract is preserved (the model has
// exactly one tool it can call, so "any" and "the named one" converge).
//
// See per-model support matrix at
// https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_ToolChoice.html
// and the langchain-aws ChatBedrockConverse implementation
// (`supports_tool_choice_values`) for prior art that ships the same gate.
func TestBedrockLlamaChatStructuredOutputOmitsForcedToolChoice(t *testing.T) {
	responseFormat := any(map[string]any{
		"type": "json_schema",
		"json_schema": map[string]any{
			"name": "PlannerOutput",
			"schema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"intent": map[string]any{"type": "string"},
				},
				"required": []any{"intent"},
			},
		},
	})

	bifrostReq := &schemas.BifrostChatRequest{
		Model: "us.meta.llama4-maverick-17b-instruct-v1:0",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: schemas.Ptr("classify this message"),
				},
			},
		},
		Params: &schemas.ChatParameters{
			ResponseFormat: &responseFormat,
		},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	result, err := bedrock.ToBedrockChatCompletionRequest(ctx, bifrostReq)
	require.NoError(t, err)
	require.NotNil(t, result)

	// Positive: synthetic bf_so_* tool still injected so the model has the
	// schema available to call.
	require.NotNil(t, result.ToolConfig, "expected toolConfig with synthetic bf_so_* tool")
	require.NotEmpty(t, result.ToolConfig.Tools, "expected at least one tool (the synthetic bf_so_*)")
	require.NotNil(t, result.ToolConfig.Tools[0].ToolSpec, "expected ToolSpec on synthetic tool")
	assert.Contains(t, result.ToolConfig.Tools[0].ToolSpec.Name, "bf_so_", "expected synthetic bf_so_* tool to be present")
	assert.Equal(t, "bf_so_PlannerOutput", result.ToolConfig.Tools[0].ToolSpec.Name)

	// Negative: NO forced tool_choice on Llama. With one tool bound, Bedrock's
	// default "auto" produces equivalent behavior without triggering the
	// 400 ValidationException.
	assert.Nil(t, result.ToolConfig.ToolChoice, "expected NO forced tool_choice on Llama (Bedrock Converse rejects toolChoice.tool)")
}

// TestBedrockNonLlamaChatStructuredOutputForcesToolChoice is the regression
// guard for the non-Llama side of the gate added in
// TestBedrockLlamaChatStructuredOutputOmitsForcedToolChoice. Non-Llama models
// (Anthropic, Nova, etc.) MUST continue to receive the forced tool_choice
// pinning the synthetic bf_so_* tool — that's the contract that makes
// structured output reliable on those families.
func TestBedrockNonLlamaChatStructuredOutputForcesToolChoice(t *testing.T) {
	responseFormat := any(map[string]any{
		"type": "json_schema",
		"json_schema": map[string]any{
			"name": "PlannerOutput",
			"schema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"intent": map[string]any{"type": "string"},
				},
				"required": []any{"intent"},
			},
		},
	})

	bifrostReq := &schemas.BifrostChatRequest{
		Model: "us.amazon.nova-pro-v1:0",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: schemas.Ptr("classify this message"),
				},
			},
		},
		Params: &schemas.ChatParameters{
			ResponseFormat: &responseFormat,
		},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	result, err := bedrock.ToBedrockChatCompletionRequest(ctx, bifrostReq)
	require.NoError(t, err)
	require.NotNil(t, result)

	require.NotNil(t, result.ToolConfig, "expected toolConfig with synthetic bf_so_* tool")
	require.NotEmpty(t, result.ToolConfig.Tools, "expected at least one tool")
	require.NotNil(t, result.ToolConfig.ToolChoice, "expected forced tool_choice on non-Llama models")
	require.NotNil(t, result.ToolConfig.ToolChoice.Tool, "expected tool_choice to target a specific tool")
	assert.Equal(t, "bf_so_PlannerOutput", result.ToolConfig.ToolChoice.Tool.Name)
}

// TestToBedrockResponsesRequest_LlamaStructuredOutputOmitsForcedToolChoice
// is the responses-path twin of
// TestBedrockLlamaChatStructuredOutputOmitsForcedToolChoice. The OpenAI
// Responses API surface routes structured output via Params.Text.Format
// rather than Params.ResponseFormat, but lands at the same Bedrock Converse
// constraint: toolChoice.tool is rejected on Llama.
func TestToBedrockResponsesRequest_LlamaStructuredOutputOmitsForcedToolChoice(t *testing.T) {
	schemaObj := schemas.NewOrderedMapFromPairs(
		schemas.KV("type", "object"),
		schemas.KV("properties", schemas.NewOrderedMapFromPairs(
			schemas.KV("intent", schemas.NewOrderedMapFromPairs(schemas.KV("type", "string"))),
		)),
		schemas.KV("required", []string{"intent"}),
	)

	req := &schemas.BifrostResponsesRequest{
		Model: "us.meta.llama4-maverick-17b-instruct-v1:0",
		Params: &schemas.ResponsesParameters{
			Text: &schemas.ResponsesTextConfig{
				Format: &schemas.ResponsesTextConfigFormat{
					Type: "json_schema",
					Name: schemas.Ptr("PlannerOutput"),
					JSONSchema: &schemas.ResponsesTextConfigFormatJSONSchema{
						Schema: &schemas.JSONSchemaOrBool{SchemaMap: schemaObj},
					},
				},
			},
		},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	bedrockReq, err := bedrock.ToBedrockResponsesRequest(ctx, req)
	require.NoError(t, err)
	require.NotNil(t, bedrockReq)

	// Positive: synthetic bf_so_* tool still injected.
	require.NotNil(t, bedrockReq.ToolConfig, "expected toolConfig with synthetic bf_so_* tool")
	require.NotEmpty(t, bedrockReq.ToolConfig.Tools, "expected at least one tool (the synthetic bf_so_*)")
	require.NotNil(t, bedrockReq.ToolConfig.Tools[0].ToolSpec, "expected ToolSpec on synthetic tool")
	assert.Contains(t, bedrockReq.ToolConfig.Tools[0].ToolSpec.Name, "bf_so_", "expected synthetic bf_so_* tool to be present")

	// Negative: no forced tool_choice on Llama for the Responses API path either.
	assert.Nil(t, bedrockReq.ToolConfig.ToolChoice, "expected NO forced tool_choice on Llama (Bedrock Converse rejects toolChoice.tool)")
}

// TestBedrockLlamaConvertToolConfigOmitsForcedToolChoice exercises the
// defense-in-depth gate at the bind_tools entry point. Callers that pass an
// explicit `tool_choice = {"type": "function", "function": {"name": "X"}}`
// (the OpenAI SDK shape; emitted by some LangChain bind_tools callers) hit
// `convertToolChoice` -> `BedrockToolChoice{Tool: ...}` rather than the
// synthetic-tool path. The same Llama 400 applies, and the same gate
// applies: drop the forced specific-tool pin and let the model "auto"
// choose from the bound tool list.
func TestBedrockLlamaConvertToolConfigOmitsForcedToolChoice(t *testing.T) {
	bifrostReq := &schemas.BifrostChatRequest{
		Model: "us.meta.llama4-maverick-17b-instruct-v1:0",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: schemas.Ptr("classify this message"),
				},
			},
		},
		Params: &schemas.ChatParameters{
			Tools: []schemas.ChatTool{
				{
					Type: schemas.ChatToolTypeFunction,
					Function: &schemas.ChatToolFunction{
						Name:        "PlannerOutput",
						Description: schemas.Ptr("Return the planner output as JSON"),
					},
				},
			},
			ToolChoice: &schemas.ChatToolChoice{
				ChatToolChoiceStruct: &schemas.ChatToolChoiceStruct{
					Type: schemas.ChatToolChoiceTypeFunction,
					Function: &schemas.ChatToolChoiceFunction{
						Name: "PlannerOutput",
					},
				},
			},
		},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	result, err := bedrock.ToBedrockChatCompletionRequest(ctx, bifrostReq)
	require.NoError(t, err)
	require.NotNil(t, result)

	// Positive: tools list survives intact.
	require.NotNil(t, result.ToolConfig)
	require.Len(t, result.ToolConfig.Tools, 1)
	require.NotNil(t, result.ToolConfig.Tools[0].ToolSpec)
	assert.Equal(t, "PlannerOutput", result.ToolConfig.Tools[0].ToolSpec.Name)

	// Negative: forced specific-tool selection dropped on Llama.
	assert.Nil(t, result.ToolConfig.ToolChoice, "expected NO forced tool_choice on Llama (Bedrock Converse rejects toolChoice.tool)")
}

// TestToBedrockResponsesRequest_LlamaConvertResponsesToolChoiceOmitsForcedToolChoice
// is the responses-path twin of
// TestBedrockLlamaConvertToolConfigOmitsForcedToolChoice. The Responses API
// surface routes explicit tool_choice through
// `convertResponsesToolChoice`, which yields `BedrockToolChoice{Tool: ...}`
// for `{"type": "function", "name": "X"}`. The same Llama 400 applies, and
// the same gate must apply: drop the forced specific-tool pin so the request
// passes Bedrock's per-model toolChoice support matrix.
func TestToBedrockResponsesRequest_LlamaConvertResponsesToolChoiceOmitsForcedToolChoice(t *testing.T) {
	toolName := "PlannerOutput"
	req := &schemas.BifrostResponsesRequest{
		Model: "us.meta.llama4-maverick-17b-instruct-v1:0",
		Params: &schemas.ResponsesParameters{
			Tools: []schemas.ResponsesTool{
				{
					Type: schemas.ResponsesToolTypeFunction,
					Name: &toolName,
					ResponsesToolFunction: &schemas.ResponsesToolFunction{
						Parameters: &schemas.ToolFunctionParameters{
							Type:       "object",
							Properties: &schemas.OrderedMap{},
						},
					},
				},
			},
			ToolChoice: &schemas.ResponsesToolChoice{
				ResponsesToolChoiceStruct: &schemas.ResponsesToolChoiceStruct{
					Type: schemas.ResponsesToolChoiceTypeFunction,
					Name: &toolName,
				},
			},
		},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	bedrockReq, err := bedrock.ToBedrockResponsesRequest(ctx, req)
	require.NoError(t, err)
	require.NotNil(t, bedrockReq)

	// Positive: explicit tools list still present.
	require.NotNil(t, bedrockReq.ToolConfig)
	require.Len(t, bedrockReq.ToolConfig.Tools, 1)
	require.NotNil(t, bedrockReq.ToolConfig.Tools[0].ToolSpec)
	assert.Equal(t, toolName, bedrockReq.ToolConfig.Tools[0].ToolSpec.Name)

	// Negative: forced specific-tool selection dropped on Llama.
	assert.Nil(t, bedrockReq.ToolConfig.ToolChoice, "expected NO forced tool_choice on Llama (Bedrock Converse rejects toolChoice.tool)")
}

// TestToBedrockResponsesRequest_NonLlamaConvertResponsesToolChoiceForcesToolChoice
// is the regression guard for the Llama gate above: Nova / Anthropic must
// still receive the explicit forced tool_choice when callers ask for it on
// the Responses API path.
func TestToBedrockResponsesRequest_NonLlamaConvertResponsesToolChoiceForcesToolChoice(t *testing.T) {
	toolName := "PlannerOutput"
	req := &schemas.BifrostResponsesRequest{
		Model: "us.anthropic.claude-sonnet-4-5-20250929-v1:0",
		Params: &schemas.ResponsesParameters{
			Tools: []schemas.ResponsesTool{
				{
					Type: schemas.ResponsesToolTypeFunction,
					Name: &toolName,
					ResponsesToolFunction: &schemas.ResponsesToolFunction{
						Parameters: &schemas.ToolFunctionParameters{
							Type:       "object",
							Properties: &schemas.OrderedMap{},
						},
					},
				},
			},
			ToolChoice: &schemas.ResponsesToolChoice{
				ResponsesToolChoiceStruct: &schemas.ResponsesToolChoiceStruct{
					Type: schemas.ResponsesToolChoiceTypeFunction,
					Name: &toolName,
				},
			},
		},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	bedrockReq, err := bedrock.ToBedrockResponsesRequest(ctx, req)
	require.NoError(t, err)
	require.NotNil(t, bedrockReq)

	// Anthropic / Nova still get the forced specific-tool selection — the
	// Llama gate must not over-fire on supported model families.
	require.NotNil(t, bedrockReq.ToolConfig)
	require.NotNil(t, bedrockReq.ToolConfig.ToolChoice)
	require.NotNil(t, bedrockReq.ToolConfig.ToolChoice.Tool, "expected forced tool_choice for non-Llama models")
	assert.Equal(t, toolName, bedrockReq.ToolConfig.ToolChoice.Tool.Name)
}

func TestToBedrockChatCompletionRequest_AliasesLongMCPToolNames(t *testing.T) {
	toolName := "mcp__plugin_chrome-devtools-mcp_chrome-devtools__list_network_requests"
	req := &schemas.BifrostChatRequest{
		Model: "us.anthropic.claude-sonnet-4-5-20250929-v1:0",
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("use devtools")},
		}},
		Params: &schemas.ChatParameters{
			Tools: []schemas.ChatTool{{
				Type: schemas.ChatToolTypeFunction,
				Function: &schemas.ChatToolFunction{
					Name:        toolName,
					Description: schemas.Ptr("List network requests"),
				},
			}},
			ToolChoice: &schemas.ChatToolChoice{
				ChatToolChoiceStruct: &schemas.ChatToolChoiceStruct{
					Type:     schemas.ChatToolChoiceTypeFunction,
					Function: &schemas.ChatToolChoiceFunction{Name: toolName},
				},
			},
		},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	result, err := bedrock.ToBedrockChatCompletionRequest(ctx, req)
	require.NoError(t, err)
	require.NotNil(t, result.ToolConfig)
	require.Len(t, result.ToolConfig.Tools, 1)

	alias := result.ToolConfig.Tools[0].ToolSpec.Name
	require.LessOrEqual(t, len(alias), 64)
	assert.NotEqual(t, toolName, alias)
	assert.Contains(t, alias, "_list_network_requests")
	assert.Regexp(t, `^[A-Za-z0-9_-]{1,64}$`, alias)
	assert.Regexp(t, `^[0-9a-f]{8}_`, alias)
	require.NotNil(t, result.ToolConfig.ToolChoice)
	require.NotNil(t, result.ToolConfig.ToolChoice.Tool)
	assert.Equal(t, alias, result.ToolConfig.ToolChoice.Tool.Name)
}

func TestToBedrockChatCompletionRequest_AliasesToolNamesWithInvalidChars(t *testing.T) {
	// Short name (<=64 chars) but with characters disallowed by Bedrock's
	// `[a-zA-Z0-9_-]{1,64}` tool-name pattern. It must still be aliased into a
	// Bedrock-valid name rather than passed through unchanged.
	toolName := "search files/in dir:now.fast"
	require.LessOrEqual(t, len(toolName), 64)
	req := &schemas.BifrostChatRequest{
		Model: "us.anthropic.claude-sonnet-4-5-20250929-v1:0",
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("search")},
		}},
		Params: &schemas.ChatParameters{
			Tools: []schemas.ChatTool{{
				Type: schemas.ChatToolTypeFunction,
				Function: &schemas.ChatToolFunction{
					Name:        toolName,
					Description: schemas.Ptr("Search files"),
				},
			}},
			ToolChoice: &schemas.ChatToolChoice{
				ChatToolChoiceStruct: &schemas.ChatToolChoiceStruct{
					Type:     schemas.ChatToolChoiceTypeFunction,
					Function: &schemas.ChatToolChoiceFunction{Name: toolName},
				},
			},
		},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	result, err := bedrock.ToBedrockChatCompletionRequest(ctx, req)
	require.NoError(t, err)
	require.NotNil(t, result.ToolConfig)
	require.Len(t, result.ToolConfig.Tools, 1)

	alias := result.ToolConfig.Tools[0].ToolSpec.Name
	assert.NotEqual(t, toolName, alias, "name with disallowed chars must be aliased")
	assert.Regexp(t, `^[A-Za-z0-9_-]{1,64}$`, alias)
	assert.Regexp(t, `^[0-9a-f]{8}_`, alias)
	require.NotNil(t, result.ToolConfig.ToolChoice)
	require.NotNil(t, result.ToolConfig.ToolChoice.Tool)
	assert.Equal(t, alias, result.ToolConfig.ToolChoice.Tool.Name)
}

func TestBedrockToBifrostChatResponse_RestoresAliasedToolName(t *testing.T) {
	toolName := "mcp__plugin_chrome-devtools-mcp_chrome-devtools__list_network_requests"
	req := &schemas.BifrostChatRequest{
		Model: "us.anthropic.claude-sonnet-4-5-20250929-v1:0",
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("use devtools")},
		}},
		Params: &schemas.ChatParameters{
			Tools: []schemas.ChatTool{{
				Type:     schemas.ChatToolTypeFunction,
				Function: &schemas.ChatToolFunction{Name: toolName},
			}},
		},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	result, err := bedrock.ToBedrockChatCompletionRequest(ctx, req)
	require.NoError(t, err)
	alias := result.ToolConfig.Tools[0].ToolSpec.Name

	response := &bedrock.BedrockConverseResponse{
		StopReason: "tool_use",
		Output: &bedrock.BedrockConverseOutput{
			Message: &bedrock.BedrockMessage{
				Role: bedrock.BedrockMessageRoleAssistant,
				Content: []bedrock.BedrockContentBlock{{
					ToolUse: &bedrock.BedrockToolUse{
						ToolUseID: "tooluse_123",
						Name:      alias,
						Input:     json.RawMessage(`{"limit":10}`),
					},
				}},
			},
		},
	}

	converted, err := response.ToBifrostChatResponse(ctx, req.Model)
	require.NoError(t, err)
	require.Len(t, converted.Choices, 1)
	toolCalls := converted.Choices[0].ChatNonStreamResponseChoice.Message.ChatAssistantMessage.ToolCalls
	require.Len(t, toolCalls, 1)
	require.NotNil(t, toolCalls[0].Function.Name)
	assert.Equal(t, toolName, *toolCalls[0].Function.Name)
}

func TestToBedrockResponsesRequest_AliasesLongMCPToolNames(t *testing.T) {
	toolName := "mcp__bifrost-this-is-imp-nasdkjadk-kanbsdjkabdkjbaskjdbasdaskjdbajksdkas__notion-notion-search"
	req := &schemas.BifrostResponsesRequest{
		Model: "us.anthropic.claude-opus-4-7",
		Input: []schemas.ResponsesMessage{{
			Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
			Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{
				ContentStr: schemas.Ptr("search docs for openai"),
			},
		}},
		Params: &schemas.ResponsesParameters{
			Tools: []schemas.ResponsesTool{{
				Type:        schemas.ResponsesToolTypeFunction,
				Name:        &toolName,
				Description: schemas.Ptr("Search Notion"),
				ResponsesToolFunction: &schemas.ResponsesToolFunction{
					Parameters: &schemas.ToolFunctionParameters{
						Type:       "object",
						Properties: schemas.NewOrderedMap(),
					},
				},
			}},
			ToolChoice: &schemas.ResponsesToolChoice{
				ResponsesToolChoiceStruct: &schemas.ResponsesToolChoiceStruct{
					Type: schemas.ResponsesToolChoiceTypeFunction,
					Name: &toolName,
				},
			},
		},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	result, err := bedrock.ToBedrockResponsesRequest(ctx, req)
	require.NoError(t, err)
	require.NotNil(t, result.ToolConfig)
	require.Len(t, result.ToolConfig.Tools, 1)

	alias := result.ToolConfig.Tools[0].ToolSpec.Name
	require.LessOrEqual(t, len(alias), 64)
	assert.NotEqual(t, toolName, alias)
	assert.Contains(t, alias, "_notion-notion-search")
	assert.Regexp(t, `^[A-Za-z0-9_-]{1,64}$`, alias)
	assert.Regexp(t, `^[0-9a-f]{8}_`, alias)
	require.NotNil(t, result.ToolConfig.ToolChoice)
	require.NotNil(t, result.ToolConfig.ToolChoice.Tool)
	assert.Equal(t, alias, result.ToolConfig.ToolChoice.Tool.Name)
}

func TestBedrockToBifrostResponsesResponse_RestoresAliasedToolName(t *testing.T) {
	toolName := "mcp__bifrost-this-is-imp-nasdkjadk-kanbsdjkabdkjbaskjdbasdaskjdbajksdkas__notion-notion-search"
	req := &schemas.BifrostResponsesRequest{
		Model: "us.anthropic.claude-opus-4-7",
		Input: []schemas.ResponsesMessage{{
			Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
			Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{
				ContentStr: schemas.Ptr("search docs for openai"),
			},
		}},
		Params: &schemas.ResponsesParameters{
			Tools: []schemas.ResponsesTool{{
				Type:                  schemas.ResponsesToolTypeFunction,
				Name:                  &toolName,
				ResponsesToolFunction: &schemas.ResponsesToolFunction{},
			}},
		},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	result, err := bedrock.ToBedrockResponsesRequest(ctx, req)
	require.NoError(t, err)
	alias := result.ToolConfig.Tools[0].ToolSpec.Name

	response := &bedrock.BedrockConverseResponse{
		StopReason: "tool_use",
		Output: &bedrock.BedrockConverseOutput{
			Message: &bedrock.BedrockMessage{
				Role: bedrock.BedrockMessageRoleAssistant,
				Content: []bedrock.BedrockContentBlock{{
					ToolUse: &bedrock.BedrockToolUse{
						ToolUseID: "tooluse_456",
						Name:      alias,
						Input:     json.RawMessage(`{"query":"openai"}`),
					},
				}},
			},
		},
	}

	converted, err := response.ToBifrostResponsesResponse(ctx)
	require.NoError(t, err)
	require.Len(t, converted.Output, 1)
	require.NotNil(t, converted.Output[0].ResponsesToolMessage)
	require.NotNil(t, converted.Output[0].ResponsesToolMessage.Name)
	assert.Equal(t, toolName, *converted.Output[0].ResponsesToolMessage.Name)
}

// ---------------------------------------------------------------------------
// Structured output (response_format: json_schema) round-trip tests – Bedrock
// ---------------------------------------------------------------------------

// TestBedrockToBifrostChatResponse_StructuredOutput_FinishReasonStop verifies that when
// the model returns only the synthetic bf_so_* tool block (no real tool calls),
// finish_reason is mapped to "stop", not "tool_calls".
func TestBedrockToBifrostChatResponse_StructuredOutput_FinishReasonStop(t *testing.T) {
	const soToolName = "bf_so_my_schema"

	response := &bedrock.BedrockConverseResponse{
		StopReason: "tool_use",
		Output: &bedrock.BedrockConverseOutput{
			Message: &bedrock.BedrockMessage{
				Role: bedrock.BedrockMessageRoleAssistant,
				Content: []bedrock.BedrockContentBlock{
					{
						ToolUse: &bedrock.BedrockToolUse{
							ToolUseID: "toolu_001",
							Name:      soToolName,
							Input:     json.RawMessage(`{"color":"blue","animal":"fox"}`),
						},
					},
				},
			},
		},
		Usage: &bedrock.BedrockTokenUsage{InputTokens: 10, OutputTokens: 20, TotalTokens: 30},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyStructuredOutputToolName, soToolName)

	result, err := response.ToBifrostChatResponse(ctx, "claude-opus-4-6")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, result.Choices, 1, "expected exactly one choice")

	choice := result.Choices[0]
	require.NotNil(t, choice.ChatNonStreamResponseChoice, "expected non-streaming response choice")

	// Content must be the JSON from the SO tool.
	msg := choice.ChatNonStreamResponseChoice.Message
	assert.NotNil(t, msg.Content.ContentStr, "expected ContentStr to be set from SO tool input")

	// No real tool calls should be surfaced.
	if msg.ChatAssistantMessage != nil {
		assert.Empty(t, msg.ChatAssistantMessage.ToolCalls, "expected no tool calls in output")
	}

	// Finish reason must be "stop", not "tool_calls".
	require.NotNil(t, choice.FinishReason)
	assert.Equal(t, string(schemas.BifrostFinishReasonStop), *choice.FinishReason,
		"expected finish_reason=stop when only SO tool was consumed")
}

// TestBedrockToBifrostChatResponse_StructuredOutput_MixedWithRealTools verifies that
// when both the SO tool and a real tool call appear in the response, finish_reason
// remains "tool_calls" so the caller knows to handle the real tool.
func TestBedrockToBifrostChatResponse_StructuredOutput_MixedWithRealTools(t *testing.T) {
	const soToolName = "bf_so_my_schema"

	response := &bedrock.BedrockConverseResponse{
		StopReason: "tool_use",
		Output: &bedrock.BedrockConverseOutput{
			Message: &bedrock.BedrockMessage{
				Role: bedrock.BedrockMessageRoleAssistant,
				Content: []bedrock.BedrockContentBlock{
					{
						ToolUse: &bedrock.BedrockToolUse{
							ToolUseID: "toolu_001",
							Name:      soToolName,
							Input:     json.RawMessage(`{"color":"blue","animal":"fox"}`),
						},
					},
					{
						ToolUse: &bedrock.BedrockToolUse{
							ToolUseID: "toolu_real_001",
							Name:      "get_weather",
							Input:     json.RawMessage(`{"location":"NYC"}`),
						},
					},
				},
			},
		},
		Usage: &bedrock.BedrockTokenUsage{InputTokens: 10, OutputTokens: 20, TotalTokens: 30},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyStructuredOutputToolName, soToolName)

	result, err := response.ToBifrostChatResponse(ctx, "claude-opus-4-6")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, result.Choices, 1, "expected exactly one choice")

	choice := result.Choices[0]
	require.NotNil(t, choice.ChatNonStreamResponseChoice, "expected non-streaming response choice")

	// The real tool call must be surfaced.
	msg := choice.ChatNonStreamResponseChoice.Message
	require.NotNil(t, msg.ChatAssistantMessage)
	assert.NotEmpty(t, msg.ChatAssistantMessage.ToolCalls, "expected real tool calls to be present")

	// Finish reason must remain "tool_calls".
	require.NotNil(t, choice.FinishReason)
	assert.Equal(t, string(schemas.BifrostFinishReasonToolCalls), *choice.FinishReason,
		"expected finish_reason=tool_calls when real tool calls are also present")
}

// TestBedrockToBifrostResponsesResponse_StructuredOutput_FinishReasonStop verifies that
// ToBifrostResponsesResponse maps stop_reason to "stop" (not "tool_calls") when only the
// synthetic SO tool was consumed.
func TestBedrockToBifrostResponsesResponse_StructuredOutput_FinishReasonStop(t *testing.T) {
	const soToolName = "bf_so_user_info"

	response := &bedrock.BedrockConverseResponse{
		StopReason: "tool_use",
		Output: &bedrock.BedrockConverseOutput{
			Message: &bedrock.BedrockMessage{
				Role: bedrock.BedrockMessageRoleAssistant,
				Content: []bedrock.BedrockContentBlock{
					{
						ToolUse: &bedrock.BedrockToolUse{
							ToolUseID: "toolu_001",
							Name:      soToolName,
							Input:     json.RawMessage(`{"name":"John Doe","age":28,"city":"Pune"}`),
						},
					},
				},
			},
		},
		Usage: &bedrock.BedrockTokenUsage{InputTokens: 10, OutputTokens: 20, TotalTokens: 30},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyStructuredOutputToolName, soToolName)

	result, err := response.ToBifrostResponsesResponse(ctx)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.StopReason)
	assert.Equal(t, "stop", *result.StopReason,
		"expected stop_reason=stop when only SO tool was consumed")
}

// TestBedrockToBifrostResponsesResponse_StructuredOutput_MixedWithRealTools verifies that
// stop_reason stays "tool_calls" when both the SO tool and a real tool call are present.
func TestBedrockToBifrostResponsesResponse_StructuredOutput_MixedWithRealTools(t *testing.T) {
	const soToolName = "bf_so_user_info"

	response := &bedrock.BedrockConverseResponse{
		StopReason: "tool_use",
		Output: &bedrock.BedrockConverseOutput{
			Message: &bedrock.BedrockMessage{
				Role: bedrock.BedrockMessageRoleAssistant,
				Content: []bedrock.BedrockContentBlock{
					{
						ToolUse: &bedrock.BedrockToolUse{
							ToolUseID: "toolu_001",
							Name:      soToolName,
							Input:     json.RawMessage(`{"name":"John Doe","age":28,"city":"Pune"}`),
						},
					},
					{
						ToolUse: &bedrock.BedrockToolUse{
							ToolUseID: "toolu_real_001",
							Name:      "get_weather",
							Input:     json.RawMessage(`{"location":"Pune"}`),
						},
					},
				},
			},
		},
		Usage: &bedrock.BedrockTokenUsage{InputTokens: 10, OutputTokens: 20, TotalTokens: 30},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyStructuredOutputToolName, soToolName)

	result, err := response.ToBifrostResponsesResponse(ctx)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.StopReason)
	assert.Equal(t, "tool_calls", *result.StopReason,
		"expected stop_reason=tool_calls when real tool calls are also present")
}

// TestBedrockSearchResultToolResultRoundTrip is the regression gate for
// https://github.com/maximhq/bifrost/issues/3537 — a Bedrock-native passthrough
// request containing toolResult.content[].searchResult must survive
// ToBifrostResponsesRequest → ToBedrockResponsesRequest with all fields intact.
// Pre-fix, the SearchResult field is dropped during JSON unmarshal and the
// outbound request shows toolResult.content = [{"text": ""}].
func TestBedrockSearchResultToolResultRoundTrip(t *testing.T) {
	original := &bedrock.BedrockConverseRequest{
		ModelID: "anthropic.claude-sonnet-4-5",
		Messages: []bedrock.BedrockMessage{
			{
				Role: bedrock.BedrockMessageRoleUser,
				Content: []bedrock.BedrockContentBlock{
					{Text: schemas.Ptr("What is Apptio?")},
				},
			},
			{
				Role: bedrock.BedrockMessageRoleAssistant,
				Content: []bedrock.BedrockContentBlock{
					{
						ToolUse: &bedrock.BedrockToolUse{
							ToolUseID: "tooluse_a4rBqeZNRTKj2lTskvaO4H",
							Name:      "RAGRequest",
							Input:     json.RawMessage(`{"query":"What is Apptio?"}`),
						},
					},
				},
			},
			{
				Role: bedrock.BedrockMessageRoleUser,
				Content: []bedrock.BedrockContentBlock{
					{
						ToolResult: &bedrock.BedrockToolResult{
							ToolUseID: "tooluse_a4rBqeZNRTKj2lTskvaO4H",
							Status:    schemas.Ptr("success"),
							Content: []bedrock.BedrockContentBlock{
								{
									SearchResult: &bedrock.BedrockSearchResultBlock{
										Source: "Great Source of Information About Apptio",
										Title:  "12adbd74-46bd-4a88-88b2-0048755f6eb5",
										Content: []bedrock.BedrockSearchResultContent{
											{Text: "Apptio is a company that makes calls to Bedrock using passthrough APIs via Bifrost"},
										},
										Citations: &bedrock.BedrockCitationsConfig{Enabled: true},
									},
								},
							},
						},
					},
				},
			},
		},
		System: []bedrock.BedrockSystemMessage{
			{Text: schemas.Ptr("Do not rely on your knowledge to answer. Use only the tool results.")},
		},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	// First leg: Bedrock → Bifrost intermediate.
	bifrostReq, err := original.ToBifrostResponsesRequest(ctx)
	require.NoError(t, err)
	require.NotNil(t, bifrostReq)

	// Second leg: Bifrost intermediate → Bedrock.
	rebuilt, err := bedrock.ToBedrockResponsesRequest(ctx, bifrostReq)
	require.NoError(t, err)
	require.NotNil(t, rebuilt)

	// Locate the rebuilt toolResult content block (its position may shift
	// because the converter groups assistant tool calls and user tool results
	// by state-machine emission, but a toolResult with our toolUseId must exist).
	var got *bedrock.BedrockSearchResultBlock
	for _, msg := range rebuilt.Messages {
		for _, block := range msg.Content {
			if block.ToolResult == nil {
				continue
			}
			if block.ToolResult.ToolUseID != "tooluse_a4rBqeZNRTKj2lTskvaO4H" {
				continue
			}
			for _, c := range block.ToolResult.Content {
				if c.SearchResult != nil {
					got = c.SearchResult
					break
				}
			}
		}
	}

	require.NotNil(t, got, "expected toolResult.content[].searchResult to round-trip; got nil (regression of #3537)")
	assert.Equal(t, "Great Source of Information About Apptio", got.Source)
	assert.Equal(t, "12adbd74-46bd-4a88-88b2-0048755f6eb5", got.Title)
	require.Len(t, got.Content, 1)
	assert.Equal(t, "Apptio is a company that makes calls to Bedrock using passthrough APIs via Bifrost", got.Content[0].Text)
	require.NotNil(t, got.Citations)
	assert.True(t, got.Citations.Enabled)
}

// TestBedrockVideoToolResultRoundTrip verifies that a video block inside
// toolResult.content survives ToBifrostResponsesRequest → ToBedrockResponsesRequest
// via the same sentinel-envelope mechanism that preserves searchResult. Without
// the schema fix + envelope trigger extension, Video is silently dropped at JSON
// unmarshal and the outbound request carries an empty text block instead.
func TestBedrockVideoToolResultRoundTrip(t *testing.T) {
	// Smallest plausible base64 payload — content doesn't matter for the round-trip,
	// only that the Video struct is preserved verbatim.
	videoBytes := "AAAA"

	original := &bedrock.BedrockConverseRequest{
		ModelID: "anthropic.claude-sonnet-4-5",
		Messages: []bedrock.BedrockMessage{
			{
				Role: bedrock.BedrockMessageRoleUser,
				Content: []bedrock.BedrockContentBlock{
					{Text: schemas.Ptr("Describe the attached clip.")},
				},
			},
			{
				Role: bedrock.BedrockMessageRoleAssistant,
				Content: []bedrock.BedrockContentBlock{
					{
						ToolUse: &bedrock.BedrockToolUse{
							ToolUseID: "tooluse_video_xyz",
							Name:      "FetchClip",
							Input:     json.RawMessage(`{"id":"abc"}`),
						},
					},
				},
			},
			{
				Role: bedrock.BedrockMessageRoleUser,
				Content: []bedrock.BedrockContentBlock{
					{
						ToolResult: &bedrock.BedrockToolResult{
							ToolUseID: "tooluse_video_xyz",
							Status:    schemas.Ptr("success"),
							Content: []bedrock.BedrockContentBlock{
								{
									Video: &bedrock.BedrockVideoBlock{
										Format: "mp4",
										Source: bedrock.BedrockVideoSource{
											Bytes: &videoBytes,
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	bifrostReq, err := original.ToBifrostResponsesRequest(ctx)
	require.NoError(t, err)
	require.NotNil(t, bifrostReq)

	rebuilt, err := bedrock.ToBedrockResponsesRequest(ctx, bifrostReq)
	require.NoError(t, err)
	require.NotNil(t, rebuilt)

	var got *bedrock.BedrockVideoBlock
	for _, msg := range rebuilt.Messages {
		for _, block := range msg.Content {
			if block.ToolResult == nil || block.ToolResult.ToolUseID != "tooluse_video_xyz" {
				continue
			}
			for _, c := range block.ToolResult.Content {
				if c.Video != nil {
					got = c.Video
					break
				}
			}
		}
	}

	require.NotNil(t, got, "expected toolResult.content[].video to round-trip; got nil")
	assert.Equal(t, "mp4", got.Format)
	require.NotNil(t, got.Source.Bytes)
	assert.Equal(t, videoBytes, *got.Source.Bytes)
	assert.Nil(t, got.Source.S3Location, "expected union member s3Location to be nil when bytes is set")
}

// TestBedrockMixedBlockToolResultRoundTrip covers a toolResult.content array that
// mixes a representable block (text) with an unrepresentable one (searchResult).
// Because the envelope path triggers on *any* unrepresentable block and serializes
// the entire content array, the whole array is bundled and must be decoded back
// intact — both blocks, in order. This guards against the decode leg dropping the
// representable block (or vice-versa) when the two are interleaved.
func TestBedrockMixedBlockToolResultRoundTrip(t *testing.T) {
	original := &bedrock.BedrockConverseRequest{
		ModelID: "anthropic.claude-sonnet-4-5",
		Messages: []bedrock.BedrockMessage{
			{
				Role: bedrock.BedrockMessageRoleUser,
				Content: []bedrock.BedrockContentBlock{
					{Text: schemas.Ptr("What is Apptio?")},
				},
			},
			{
				Role: bedrock.BedrockMessageRoleAssistant,
				Content: []bedrock.BedrockContentBlock{
					{
						ToolUse: &bedrock.BedrockToolUse{
							ToolUseID: "tooluse_mixed_blocks",
							Name:      "RAGRequest",
							Input:     json.RawMessage(`{"query":"What is Apptio?"}`),
						},
					},
				},
			},
			{
				Role: bedrock.BedrockMessageRoleUser,
				Content: []bedrock.BedrockContentBlock{
					{
						ToolResult: &bedrock.BedrockToolResult{
							ToolUseID: "tooluse_mixed_blocks",
							Status:    schemas.Ptr("success"),
							Content: []bedrock.BedrockContentBlock{
								{Text: schemas.Ptr("Summary: Apptio is a Bedrock passthrough customer.")},
								{
									SearchResult: &bedrock.BedrockSearchResultBlock{
										Source: "Great Source of Information About Apptio",
										Title:  "12adbd74-46bd-4a88-88b2-0048755f6eb5",
										Content: []bedrock.BedrockSearchResultContent{
											{Text: "Apptio is a company that makes calls to Bedrock using passthrough APIs via Bifrost"},
										},
										Citations: &bedrock.BedrockCitationsConfig{Enabled: true},
									},
								},
							},
						},
					},
				},
			},
		},
		System: []bedrock.BedrockSystemMessage{
			{Text: schemas.Ptr("Do not rely on your knowledge to answer. Use only the tool results.")},
		},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	bifrostReq, err := original.ToBifrostResponsesRequest(ctx)
	require.NoError(t, err)
	require.NotNil(t, bifrostReq)

	rebuilt, err := bedrock.ToBedrockResponsesRequest(ctx, bifrostReq)
	require.NoError(t, err)
	require.NotNil(t, rebuilt)

	// Locate the rebuilt toolResult content array for our toolUseId.
	var gotContent []bedrock.BedrockContentBlock
	for _, msg := range rebuilt.Messages {
		for _, block := range msg.Content {
			if block.ToolResult == nil || block.ToolResult.ToolUseID != "tooluse_mixed_blocks" {
				continue
			}
			gotContent = block.ToolResult.Content
		}
	}

	require.NotNil(t, gotContent, "expected toolResult with our toolUseId to round-trip; got nil")
	require.Len(t, gotContent, 2, "expected both the text and searchResult blocks to survive the round-trip")

	// Order is preserved: the envelope is a JSON array, so block[0] is the text
	// block and block[1] is the searchResult block.
	require.NotNil(t, gotContent[0].Text, "expected first block to remain a text block")
	assert.Equal(t, "Summary: Apptio is a Bedrock passthrough customer.", *gotContent[0].Text)
	assert.Nil(t, gotContent[0].SearchResult, "text block must not gain a searchResult")

	got := gotContent[1].SearchResult
	require.NotNil(t, got, "expected second block to remain a searchResult block")
	assert.Nil(t, gotContent[1].Text, "searchResult block must not gain a text field")
	assert.Equal(t, "Great Source of Information About Apptio", got.Source)
	assert.Equal(t, "12adbd74-46bd-4a88-88b2-0048755f6eb5", got.Title)
	require.Len(t, got.Content, 1)
	assert.Equal(t, "Apptio is a company that makes calls to Bedrock using passthrough APIs via Bifrost", got.Content[0].Text)
	require.NotNil(t, got.Citations)
	assert.True(t, got.Citations.Enabled)
}

// systemReminderTextMsg builds a role=system message with a single text block.
func systemReminderTextMsg(text string) schemas.ResponsesMessage {
	return schemas.ResponsesMessage{
		Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
		Role: schemas.Ptr(schemas.ResponsesInputMessageRoleSystem),
		Content: &schemas.ResponsesMessageContent{
			ContentBlocks: []schemas.ResponsesMessageContentBlock{
				{Type: schemas.ResponsesInputMessageContentBlockTypeText, Text: schemas.Ptr(text)},
			},
		},
	}
}

// userReminderTextMsg builds a role=user message with a single text block.
func userReminderTextMsg(text string) schemas.ResponsesMessage {
	return schemas.ResponsesMessage{
		Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
		Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
		Content: &schemas.ResponsesMessageContent{
			ContentBlocks: []schemas.ResponsesMessageContentBlock{
				{Type: schemas.ResponsesInputMessageContentBlockTypeText, Text: schemas.Ptr(text)},
			},
		},
	}
}

// TestMidConversationSystemReminderStaysInline verifies that only the leading run of
// role=system messages is hoisted into Bedrock's top-level system block. Reminders that
// Claude Code injects mid-conversation (also role=system) must stay inline as user
// messages, otherwise they grow the system block in front of the cached conversation
// prefix and break Bedrock prompt caching (collapsing reads to the tools/system floor).
func TestMidConversationSystemReminderStaysInline(t *testing.T) {
	input := []schemas.ResponsesMessage{
		systemReminderTextMsg("You are Claude Code."), // leading system prompt
		userReminderTextMsg("first user turn"),
		systemReminderTextMsg("The task tools haven't been used recently."), // injected reminder
		userReminderTextMsg("second user turn"),
	}

	messages, systemMessages, err := bedrock.ConvertBifrostMessagesToBedrockMessages(context.Background(), input, true)
	require.NoError(t, err)

	// Only the leading system prompt should be hoisted into the system block.
	require.Len(t, systemMessages, 1, "only the leading system prompt belongs in the system block")
	require.NotNil(t, systemMessages[0].Text)
	assert.Equal(t, "You are Claude Code.", *systemMessages[0].Text)

	// The injected reminder stays inline as user content. Because it is rendered as a user
	// message between two user turns, the consecutive-same-role merge folds all three into a
	// single user message — preserving order. The key invariant is that the reminder text is
	// NOT in the system block and appears in chronological order in the conversation.
	require.Len(t, messages, 1, "three consecutive user-role messages merge into one")
	assert.Equal(t, bedrock.BedrockMessageRoleUser, messages[0].Role)
	require.Len(t, messages[0].Content, 3)
	require.NotNil(t, messages[0].Content[0].Text)
	assert.Equal(t, "first user turn", *messages[0].Content[0].Text)
	require.NotNil(t, messages[0].Content[1].Text)
	assert.Equal(t, "<system-reminder>\nThe task tools haven't been used recently.\n</system-reminder>\n",
		*messages[0].Content[1].Text, "reminder must stay inline at its position, wrapped")
	require.NotNil(t, messages[0].Content[2].Text)
	assert.Equal(t, "second user turn", *messages[0].Content[2].Text)
}

// TestMidConversationSystemReminderHoistedForNonAnthropic verifies the Anthropic-only gating:
// for a non-Anthropic Bedrock model (e.g. Nova), the historical behavior is preserved — every
// role=system message, including mid-conversation ones, is hoisted into the top-level system
// block and nothing is inlined as a <system-reminder>. The inlining is a prompt-cache workaround
// specific to Anthropic-on-Bedrock and must not change the wire shape for other models.
func TestMidConversationSystemReminderHoistedForNonAnthropic(t *testing.T) {
	input := []schemas.ResponsesMessage{
		systemReminderTextMsg("You are a helpful assistant."), // leading system prompt
		userReminderTextMsg("first user turn"),
		systemReminderTextMsg("Mid-conversation reminder."), // would be inlined for Anthropic
		userReminderTextMsg("second user turn"),
	}

	messages, systemMessages, err := bedrock.ConvertBifrostMessagesToBedrockMessages(context.Background(), input, false)
	require.NoError(t, err)

	// Both system messages are hoisted (historical behavior), not just the leading one.
	require.Len(t, systemMessages, 2, "non-Anthropic models hoist every system message")
	assert.Equal(t, "You are a helpful assistant.", *systemMessages[0].Text)
	assert.Equal(t, "Mid-conversation reminder.", *systemMessages[1].Text)

	// No reminder is inlined into the conversation, and nothing is <system-reminder>-wrapped.
	for _, m := range messages {
		for _, b := range m.Content {
			if b.Text != nil {
				assert.NotContains(t, *b.Text, "<system-reminder>", "non-Anthropic path must not wrap reminders")
			}
		}
	}
}

// TestMultipleLeadingSystemMessagesAllHoisted verifies that a leading run of more than one
// system message is fully hoisted, and the boundary closes at the first non-system message.
func TestMultipleLeadingSystemMessagesAllHoisted(t *testing.T) {
	input := []schemas.ResponsesMessage{
		systemReminderTextMsg("System prompt part one."),
		systemReminderTextMsg("System prompt part two."),
		userReminderTextMsg("hello"),
		systemReminderTextMsg("Injected reminder."),
	}

	messages, systemMessages, err := bedrock.ConvertBifrostMessagesToBedrockMessages(context.Background(), input, true)
	require.NoError(t, err)

	require.Len(t, systemMessages, 2, "both leading system messages belong in the system block")
	assert.Equal(t, "System prompt part one.", *systemMessages[0].Text)
	assert.Equal(t, "System prompt part two.", *systemMessages[1].Text)

	// Consecutive same-role merge folds the inlined reminder into the preceding user message.
	require.Len(t, messages, 1)
	assert.Equal(t, bedrock.BedrockMessageRoleUser, messages[0].Role)
	require.Len(t, messages[0].Content, 2)
	assert.Equal(t, "hello", *messages[0].Content[0].Text)
	assert.Equal(t, "<system-reminder>\nInjected reminder.\n</system-reminder>\n", *messages[0].Content[1].Text)
}

// TestSystemReminderAfterToolResultPreservesPairing verifies that a reminder arriving after a
// tool result does not split the tool_use/tool_result pairing and ends up correctly ordered
// after the tool result (both are user-role and merge). This is the dominant production shape.
func TestSystemReminderAfterToolResultPreservesPairing(t *testing.T) {
	input := []schemas.ResponsesMessage{
		systemReminderTextMsg("You are Claude Code."),
		userReminderTextMsg("do a thing"),
		{
			Type:                 schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
			ResponsesToolMessage: &schemas.ResponsesToolMessage{CallID: schemas.Ptr("tooluse_1"), Name: schemas.Ptr("read")},
		},
		{
			Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				CallID: schemas.Ptr("tooluse_1"),
				Output: &schemas.ResponsesToolMessageOutputStruct{ResponsesToolCallOutputStr: schemas.Ptr("file contents")},
			},
		},
		systemReminderTextMsg("The task tools haven't been used recently."), // reminder right after tool result
	}

	messages, systemMessages, err := bedrock.ConvertBifrostMessagesToBedrockMessages(context.Background(), input, true)
	require.NoError(t, err)
	require.Len(t, systemMessages, 1)

	// Find the assistant tool_use and assert it is immediately followed by a user tool_result.
	toolUseIdx := -1
	for i, m := range messages {
		for _, b := range m.Content {
			if b.ToolUse != nil {
				toolUseIdx = i
			}
		}
	}
	require.GreaterOrEqual(t, toolUseIdx, 0, "tool_use must be present")
	require.Less(t, toolUseIdx+1, len(messages), "tool_use must be followed by a message")
	next := messages[toolUseIdx+1]
	assert.Equal(t, bedrock.BedrockMessageRoleUser, next.Role)
	var hasToolResult bool
	for _, b := range next.Content {
		if b.ToolResult != nil {
			hasToolResult = true
		}
	}
	assert.True(t, hasToolResult, "tool_use must be immediately followed by its tool_result")

	// The reminder must NOT be hoisted into the system block.
	for _, sm := range systemMessages {
		if sm.Text != nil {
			assert.NotContains(t, *sm.Text, "task tools haven't been used", "reminder must not be hoisted")
		}
	}

	// The reminder text appears inline, wrapped, somewhere after the tool result.
	var foundReminder bool
	for _, m := range messages {
		for _, b := range m.Content {
			if b.Text != nil && *b.Text == "<system-reminder>\nThe task tools haven't been used recently.\n</system-reminder>\n" {
				foundReminder = true
			}
		}
	}
	assert.True(t, foundReminder, "reminder must be inlined as wrapped user text")
}

// developerReminderTextMsg builds a role=developer message with a single text block.
func developerReminderTextMsg(text string) schemas.ResponsesMessage {
	return schemas.ResponsesMessage{
		Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
		Role: schemas.Ptr(schemas.ResponsesInputMessageRoleDeveloper),
		Content: &schemas.ResponsesMessageContent{
			ContentBlocks: []schemas.ResponsesMessageContentBlock{
				{Type: schemas.ResponsesInputMessageContentBlockTypeText, Text: schemas.Ptr(text)},
			},
		},
	}
}

// TestMidConversationDeveloperReminderStaysInline mirrors the system-reminder test for the
// developer role, which the converter treats identically (both are hoisted only when leading,
// inlined otherwise). Without coverage, a future change special-casing developer could regress.
func TestMidConversationDeveloperReminderStaysInline(t *testing.T) {
	input := []schemas.ResponsesMessage{
		systemReminderTextMsg("You are Claude Code."), // leading system prompt
		userReminderTextMsg("first user turn"),
		developerReminderTextMsg("Developer note injected mid-conversation."),
		userReminderTextMsg("second user turn"),
	}

	messages, systemMessages, err := bedrock.ConvertBifrostMessagesToBedrockMessages(context.Background(), input, true)
	require.NoError(t, err)

	// Only the leading system prompt is hoisted; the developer reminder is NOT.
	require.Len(t, systemMessages, 1)
	assert.Equal(t, "You are Claude Code.", *systemMessages[0].Text)
	for _, sm := range systemMessages {
		if sm.Text != nil {
			assert.NotContains(t, *sm.Text, "Developer note", "developer reminder must not be hoisted")
		}
	}

	// It appears inline, wrapped, in chronological order.
	var found bool
	for _, m := range messages {
		for _, b := range m.Content {
			if b.Text != nil && *b.Text == "<system-reminder>\nDeveloper note injected mid-conversation.\n</system-reminder>\n" {
				found = true
			}
		}
	}
	assert.True(t, found, "developer reminder must be inlined as wrapped user text")
}

// TestMidConversationReminderContentStrInlined covers the ContentStr branch of the helper
// (simple string content) rather than ContentBlocks, which all the other tests use. A
// regression in that branch (e.g. forgetting to wrap) would otherwise pass CI.
func TestMidConversationReminderContentStrInlined(t *testing.T) {
	input := []schemas.ResponsesMessage{
		systemReminderTextMsg("You are Claude Code."),
		userReminderTextMsg("hello"),
		{
			Type:    schemas.Ptr(schemas.ResponsesMessageTypeMessage),
			Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleSystem),
			Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("Reminder via ContentStr.")},
		},
	}

	messages, systemMessages, err := bedrock.ConvertBifrostMessagesToBedrockMessages(context.Background(), input, true)
	require.NoError(t, err)
	require.Len(t, systemMessages, 1, "only the leading prompt is hoisted")

	var found bool
	for _, m := range messages {
		for _, b := range m.Content {
			if b.Text != nil && *b.Text == "<system-reminder>\nReminder via ContentStr.\n</system-reminder>\n" {
				found = true
			}
		}
	}
	assert.True(t, found, "ContentStr reminder must be inlined as wrapped user text")
}

// TestMidConversationReminderEmptyContentDropped pins the nil-return contract: a mid-conversation
// reminder with no text content produces no Bedrock message (the helper returns nil and the
// caller skips it), rather than an empty user message that Bedrock would reject.
func TestMidConversationReminderEmptyContentDropped(t *testing.T) {
	input := []schemas.ResponsesMessage{
		systemReminderTextMsg("You are Claude Code."),
		userReminderTextMsg("hello"),
		{
			Type:    schemas.Ptr(schemas.ResponsesMessageTypeMessage),
			Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleSystem),
			Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{}},
		},
	}

	messages, systemMessages, err := bedrock.ConvertBifrostMessagesToBedrockMessages(context.Background(), input, true)
	require.NoError(t, err)
	require.Len(t, systemMessages, 1)

	// No empty content blocks anywhere; the empty reminder was dropped, not emitted.
	for _, m := range messages {
		require.NotEmpty(t, m.Content, "no message should have empty content")
		for _, b := range m.Content {
			if b.Text != nil {
				assert.NotEqual(t, "<system-reminder>\n\n</system-reminder>\n", *b.Text, "empty reminder must not be emitted as a wrapped blank")
			}
		}
	}
}

// TestSystemReminderBetweenToolCallAndResult covers a reminder arriving BETWEEN a function_call
// and its output — a defensive edge case (Claude Code never interleaves reminders into a tool
// exchange). Asserts the tool_use/tool_result pair is preserved. Known quirk, not asserted: the
// merged user turn places the reminder text before the tool_result block; revisit if real traffic
// ever interleaves this way.
func TestSystemReminderBetweenToolCallAndResult(t *testing.T) {
	input := []schemas.ResponsesMessage{
		systemReminderTextMsg("You are Claude Code."),
		userReminderTextMsg("do a thing"),
		{
			Type:                 schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
			ResponsesToolMessage: &schemas.ResponsesToolMessage{CallID: schemas.Ptr("tooluse_x"), Name: schemas.Ptr("read")},
		},
		systemReminderTextMsg("Injected between call and result."), // the interleaved reminder
		{
			Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				CallID: schemas.Ptr("tooluse_x"),
				Output: &schemas.ResponsesToolMessageOutputStruct{ResponsesToolCallOutputStr: schemas.Ptr("contents")},
			},
		},
	}

	messages, _, err := bedrock.ConvertBifrostMessagesToBedrockMessages(context.Background(), input, true)
	require.NoError(t, err)

	// Locate the assistant message carrying the tool_use.
	toolUseMsgIdx := -1
	for i, m := range messages {
		for _, b := range m.Content {
			if b.ToolUse != nil {
				toolUseMsgIdx = i
			}
		}
	}
	require.GreaterOrEqual(t, toolUseMsgIdx, 0, "tool_use must be present")

	// No reminder text may share the tool_use assistant message (that would split the pair).
	for _, b := range messages[toolUseMsgIdx].Content {
		assert.Nil(t, b.Text, "tool_use message must not contain an inlined reminder text block")
	}

	// The pair invariant: the next message is the user turn and it contains the matching
	// tool_result (block order within that turn is not asserted — see the known limitation above).
	require.Less(t, toolUseMsgIdx+1, len(messages), "tool_use must be followed by a message")
	next := messages[toolUseMsgIdx+1]
	assert.Equal(t, bedrock.BedrockMessageRoleUser, next.Role)
	var hasResult bool
	for _, b := range next.Content {
		if b.ToolResult != nil && b.ToolResult.ToolUseID == "tooluse_x" {
			hasResult = true
		}
	}
	assert.True(t, hasResult, "user message after tool_use must contain the matching tool_result")
}

// TestSystemReminderDoesNotCarryCachePoint pins the deliberate omission flagged in review: an
// inlined mid-conversation reminder must NOT emit a CachePoint, even if its block carries
// CacheControl. A breakpoint at the moving conversation tail would shift every turn and defeat
// the prefix caching this whole change exists to preserve.
func TestSystemReminderDoesNotCarryCachePoint(t *testing.T) {
	reminder := systemReminderTextMsg("Reminder that happens to carry a breakpoint.")
	reminder.Content.ContentBlocks[0].CacheControl = &schemas.CacheControl{Type: schemas.CacheControlTypeEphemeral}

	input := []schemas.ResponsesMessage{
		systemReminderTextMsg("You are Claude Code."),
		userReminderTextMsg("hello"),
		reminder,
	}

	messages, _, err := bedrock.ConvertBifrostMessagesToBedrockMessages(context.Background(), input, true)
	require.NoError(t, err)

	for _, m := range messages {
		for _, b := range m.Content {
			assert.Nil(t, b.CachePoint, "inlined reminder must not introduce a CachePoint block")
		}
	}
}

// TestToolCacheControlBecomesCachePointWithTTL is the positive counterpart to
// TestSystemReminderDoesNotCarryCachePoint: a cache_control breakpoint Claude Code places on a
// tool call / tool result must survive into Bedrock as a CachePoint block adjacent to the
// ToolUse/ToolResult, AND must carry the requested TTL ("1h"). This pins the bug review caught
// where the breakpoints were emitted with the default TTL dropped.
func TestToolCacheControlBecomesCachePointWithTTL(t *testing.T) {
	ttl := "1h"
	toolMsgs := func() []schemas.ResponsesMessage {
		return []schemas.ResponsesMessage{
			userReminderTextMsg("call the tool"),
			{
				Type:         schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
				CacheControl: &schemas.CacheControl{Type: schemas.CacheControlTypeEphemeral, TTL: &ttl},
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID:    schemas.Ptr("tooluse_ttl"),
					Name:      schemas.Ptr("get_weather"),
					Arguments: schemas.Ptr(`{"location":"NYC"}`),
				},
			},
			{
				Type:         schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
				CacheControl: &schemas.CacheControl{Type: schemas.CacheControlTypeEphemeral, TTL: &ttl},
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID: schemas.Ptr("tooluse_ttl"),
					Output: &schemas.ResponsesToolMessageOutputStruct{ResponsesToolCallOutputStr: schemas.Ptr("sunny")},
				},
			},
		}
	}

	assertTTLPreserved := func(t *testing.T, input []schemas.ResponsesMessage) {
		messages, _, err := bedrock.ConvertBifrostMessagesToBedrockMessages(context.Background(), input, true)
		require.NoError(t, err)

		var afterToolUse, afterToolResult bool
		for _, m := range messages {
			for i, b := range m.Content {
				if b.ToolUse != nil && b.ToolUse.ToolUseID == "tooluse_ttl" {
					require.Greater(t, len(m.Content), i+1, "a CachePoint block must follow the ToolUse")
					cp := m.Content[i+1].CachePoint
					require.NotNil(t, cp, "ToolUse with cache_control must be followed by a CachePoint")
					require.NotNil(t, cp.TTL, "CachePoint must carry the requested TTL, not drop it")
					assert.Equal(t, "1h", *cp.TTL)
					afterToolUse = true
				}
				if b.ToolResult != nil && b.ToolResult.ToolUseID == "tooluse_ttl" {
					require.Greater(t, len(m.Content), i+1, "a CachePoint block must follow the ToolResult")
					cp := m.Content[i+1].CachePoint
					require.NotNil(t, cp, "ToolResult with cache_control must be followed by a CachePoint")
					require.NotNil(t, cp.TTL, "CachePoint must carry the requested TTL, not drop it")
					assert.Equal(t, "1h", *cp.TTL)
					afterToolResult = true
				}
			}
		}
		assert.True(t, afterToolUse, "expected a TTL-carrying CachePoint after the tool_use")
		assert.True(t, afterToolResult, "expected a TTL-carrying CachePoint after the tool_result")
	}

	// End-of-sequence flush: the tool call/result are the last messages (isLastResultInSequence).
	t.Run("end of sequence", func(t *testing.T) {
		assertTTLPreserved(t, toolMsgs())
	})
	// Flush-before-message: a following user message triggers the flush while tool results are
	// pending (the path this PR added inside case ResponsesMessageTypeMessage).
	t.Run("followed by a message", func(t *testing.T) {
		assertTTLPreserved(t, append(toolMsgs(), userReminderTextMsg("and then continue")))
	})
}

// TestLoneSystemMessageReturnsUserMessage covers the single-element early return: a lone
// system/developer message is converted to a user message and returned, with no system block,
// regardless of the inlineSystemReminders gate.
func TestLoneSystemMessageReturnsUserMessage(t *testing.T) {
	// Both system and developer roles take the single-message early return.
	roles := map[string]schemas.ResponsesMessage{
		"system":    systemReminderTextMsg("You are Claude Code."),
		"developer": developerReminderTextMsg("You are Claude Code."),
	}
	for role, msg := range roles {
		for _, inline := range []bool{true, false} {
			input := []schemas.ResponsesMessage{msg}
			messages, systemMessages, err := bedrock.ConvertBifrostMessagesToBedrockMessages(context.Background(), input, inline)
			require.NoError(t, err)
			assert.Empty(t, systemMessages, "lone %s message must not populate the system block (inline=%v)", role, inline)
			require.Len(t, messages, 1, "lone %s message must yield exactly one message (inline=%v)", role, inline)
			assert.Equal(t, bedrock.BedrockMessageRoleUser, messages[0].Role)
		}
	}
}

// TestNoLeadingSystemBlockReminderInlined covers the seenNonSystemMessage gate when the
// conversation does NOT start with a system message: the first message is a user turn, so a later
// role=system reminder is mid-conversation and must be inlined (nothing hoisted into system).
func TestNoLeadingSystemBlockReminderInlined(t *testing.T) {
	input := []schemas.ResponsesMessage{
		userReminderTextMsg("hello"),
		systemReminderTextMsg("Reminder with no leading system block."),
		userReminderTextMsg("continue"),
	}

	messages, systemMessages, err := bedrock.ConvertBifrostMessagesToBedrockMessages(context.Background(), input, true)
	require.NoError(t, err)

	assert.Empty(t, systemMessages, "no leading system block means nothing should be hoisted")
	var inlined bool
	for _, m := range messages {
		for _, b := range m.Content {
			if b.Text != nil && strings.Contains(*b.Text, "<system-reminder>") &&
				strings.Contains(*b.Text, "Reminder with no leading system block.") {
				assert.Equal(t, bedrock.BedrockMessageRoleUser, m.Role, "inlined reminder must be a user message")
				inlined = true
			}
		}
	}
	assert.True(t, inlined, "the mid-conversation reminder must be inlined as a wrapped user message")
}

// TestReasoningConfigSurvivesHTTPUnmarshal guards against reasoning_config /
// thinking being silently dropped when a Bedrock Converse body is unmarshaled
// from JSON (nested objects decode to *OrderedMap, not map[string]interface{})
// and then translated to a non-Bedrock target.
func TestReasoningConfigSurvivesHTTPUnmarshal(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	t.Run("reasoning_config", func(t *testing.T) {
		body := []byte(`{
			"messages": [{"role": "user", "content": [{"text": "Think step-by-step."}]}],
			"inferenceConfig": {"maxTokens": 2012, "temperature": 1.0},
			"additionalModelRequestFields": {
				"reasoning_config": {"type": "enabled", "budget_tokens": 1500}
			}
		}`)

		var req bedrock.BedrockConverseRequest
		require.NoError(t, json.Unmarshal(body, &req))
		req.ModelID = "anthropic/claude-sonnet-4-5-20250929"

		out, err := req.ToBifrostResponsesRequest(ctx)
		require.NoError(t, err)
		require.NotNil(t, out.Params, "Params is nil")
		require.NotNil(t, out.Params.Reasoning, "reasoning_config was silently dropped")
		require.NotNil(t, out.Params.Reasoning.MaxTokens)
		assert.Equal(t, 1500, *out.Params.Reasoning.MaxTokens)
	})

	t.Run("nova_reasoningConfig", func(t *testing.T) {
		body := []byte(`{
			"messages": [{"role": "user", "content": [{"text": "Think step-by-step."}]}],
			"inferenceConfig": {"maxTokens": 2012},
			"additionalModelRequestFields": {
				"reasoningConfig": {"type": "enabled", "maxReasoningEffort": "high"}
			}
		}`)

		var req bedrock.BedrockConverseRequest
		require.NoError(t, json.Unmarshal(body, &req))
		req.ModelID = "amazon.nova-premier-v1:0"

		out, err := req.ToBifrostResponsesRequest(ctx)
		require.NoError(t, err)
		require.NotNil(t, out.Params, "Params is nil")
		require.NotNil(t, out.Params.Reasoning, "nova reasoningConfig was silently dropped")
		require.NotNil(t, out.Params.Reasoning.Effort)
		assert.Equal(t, "high", *out.Params.Reasoning.Effort)
	})
}

// TestReasoningConfigNoDoubleEmissionOnEgress guards against issue #5108's
// follow-on regression: a reasoning key consumed into Params.Reasoning on ingress
// must not also be forwarded verbatim via additionalModelRequestFieldPaths, or the
// Bedrock egress carries two copies and Converse rejects the collision
// ("The additional field thinking/type conflicts with an existing field").
func TestReasoningConfigNoDoubleEmissionOnEgress(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	cases := []struct {
		name        string
		model       string
		inputField  string
		inputConfig string
		wantKey     string // the single reasoning key expected on egress
	}{
		{"anthropic_reasoning_config", "us.anthropic.claude-haiku-4-5-20251001-v1:0", "reasoning_config", `{"type": "enabled", "budget_tokens": 1500}`, "thinking"},
		{"anthropic_thinking", "us.anthropic.claude-haiku-4-5-20251001-v1:0", "thinking", `{"type": "enabled", "budget_tokens": 1500}`, "thinking"},
		{"nova_reasoningConfig", "amazon.nova-premier-v1:0", "reasoningConfig", `{"type": "enabled", "maxReasoningEffort": "high"}`, "reasoningConfig"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{
				"messages": [{"role": "user", "content": [{"text": "What's my best offer?"}]}],
				"inferenceConfig": {"maxTokens": 4096},
				"additionalModelRequestFields": {"` + tc.inputField + `": ` + tc.inputConfig + `}
			}`)

			var req bedrock.BedrockConverseRequest
			require.NoError(t, json.Unmarshal(body, &req))
			req.ModelID = tc.model

			// Ingress: Bedrock Converse -> Bifrost
			mid, err := req.ToBifrostResponsesRequest(ctx)
			require.NoError(t, err)
			require.NotNil(t, mid.Params, "Params is nil")
			require.NotNil(t, mid.Params.Reasoning, "reasoning was not consumed on ingress")

			// Egress: Bifrost -> Bedrock Converse
			out, err := bedrock.ToBedrockResponsesRequest(ctx, mid)
			require.NoError(t, err)
			require.NotNil(t, out.AdditionalModelRequestFields)

			// Exactly one reasoning key on the wire, and never both spellings.
			_, hasThinking := out.AdditionalModelRequestFields.Get("thinking")
			_, hasReasoningConfig := out.AdditionalModelRequestFields.Get("reasoning_config")
			_, hasNova := out.AdditionalModelRequestFields.Get("reasoningConfig")

			assert.False(t, hasThinking && hasReasoningConfig,
				"both thinking and reasoning_config emitted — Bedrock would reject the collision")

			switch tc.wantKey {
			case "thinking":
				assert.True(t, hasThinking, "expected thinking on egress")
				assert.False(t, hasReasoningConfig, "reasoning_config must not be forwarded verbatim once consumed")
			case "reasoningConfig":
				assert.True(t, hasNova, "expected Nova reasoningConfig on egress")
			}
		})
	}
}
