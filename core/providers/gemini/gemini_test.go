package gemini_test

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/internal/llmtests"
	"github.com/maximhq/bifrost/core/providers/gemini"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestGemini(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("GEMINI_API_KEY")) == "" {
		t.Skip("Skipping Gemini tests because GEMINI_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:  schemas.Gemini,
		ChatModel: "gemini-2.0-flash",
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.Gemini, Model: "gemini-2.5-flash"},
		},
		VisionModel:          "gemini-2.5-flash",
		EmbeddingModel:       "gemini-embedding-001",
		TranscriptionModel:   "gemini-2.5-flash",
		SpeechSynthesisModel: "gemini-2.5-flash-preview-tts",
		ImageGenerationModel: "gemini-2.5-flash-image",
		ImageEditModel:       "gemini-3-pro-image-preview",
		SpeechSynthesisFallbacks: []schemas.Fallback{
			{Provider: schemas.Gemini, Model: "gemini-2.5-pro-preview-tts"},
		},
		ReasoningModel:       "gemini-3-pro-preview",
		VideoGenerationModel: "veo-3.1-generate-preview",
		PassthroughModel:     "gemini-2.5-flash",
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
			WebSearchTool:              true,
			ImageURL:                   false,
			ImageBase64:                true,
			MultipleImages:             false,
			ImageGeneration:            true,
			ImageGenerationStream:      false,
			ImageEdit:                  true,
			VideoGeneration:            false, // disabled for now because of long running operations
			VideoRetrieve:              false,
			VideoDownload:              false,
			FileBase64:                 true,
			FileURL:                    false, // supported files via gemini files api
			CompleteEnd2End:            true,
			Embedding:                  true,
			Transcription:              false,
			TranscriptionStream:        false,
			SpeechSynthesis:            true,
			SpeechSynthesisStream:      true,
			Reasoning:                  true,
			ListModels:                 true,
			BatchCreate:                true,
			BatchList:                  true,
			BatchRetrieve:              true,
			BatchCancel:                true,
			BatchResults:               true,
			FileUpload:                 true,
			FileList:                   true,
			FileRetrieve:               true,
			FileDelete:                 true,
			FileContent:                false,
			FileBatchInput:             true,
			CountTokens:                true,
			StructuredOutputs:          true, // Structured outputs with nullable enum support
			PassthroughAPI:             true,
		},
	}

	t.Run("GeminiTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}

// TestEmptyCandidatesRegression is a regression test for PR #1018
// Ensures empty/filtered candidates never return empty choices arrays
func TestEmptyCandidatesRegression(t *testing.T) {
	tests := []struct {
		name         string
		response     *gemini.GenerateContentResponse
		isStream     bool
		expectFinish string
	}{
		{
			name: "EmptyCandidates_NonStream",
			response: &gemini.GenerateContentResponse{
				ResponseID:   "test-1",
				ModelVersion: "gemini-2.0-flash",
				Candidates:   []*gemini.Candidate{}, // Empty - the bug case
				UsageMetadata: &gemini.GenerateContentResponseUsageMetadata{
					PromptTokenCount: 10,
					TotalTokenCount:  10,
				},
			},
			isStream:     false,
			expectFinish: "stop",
		},
		{
			name: "EmptyCandidates_Stream",
			response: &gemini.GenerateContentResponse{
				ResponseID:   "test-2",
				ModelVersion: "gemini-2.0-flash",
				Candidates:   []*gemini.Candidate{},
				UsageMetadata: &gemini.GenerateContentResponseUsageMetadata{
					PromptTokenCount: 10,
					TotalTokenCount:  10,
				},
			},
			isStream:     true,
			expectFinish: "stop",
		},
		{
			name: "SafetyFilter_NonStream",
			response: &gemini.GenerateContentResponse{
				ResponseID:   "test-3",
				ModelVersion: "gemini-2.0-flash",
				Candidates: []*gemini.Candidate{
					{
						Index:        0,
						FinishReason: gemini.FinishReasonSafety,
						Content:      &gemini.Content{Role: string(gemini.RoleModel), Parts: []*gemini.Part{}},
					},
				},
			},
			isStream:     false,
			expectFinish: "content_filter",
		},
		{
			name: "MalformedFunctionCall_Stream",
			response: &gemini.GenerateContentResponse{
				ResponseID:   "test-4",
				ModelVersion: "gemini-2.0-flash",
				Candidates: []*gemini.Candidate{
					{
						Index:        0,
						FinishReason: gemini.FinishReasonMalformedFunctionCall,
						Content:      &gemini.Content{Role: string(gemini.RoleModel), Parts: []*gemini.Part{}},
					},
				},
			},
			isStream:     true,
			expectFinish: "stop",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var bifrostResp *schemas.BifrostChatResponse

			if tt.isStream {
				bifrostResp, _, _ = tt.response.ToBifrostChatCompletionStream(gemini.NewGeminiStreamState())
			} else {
				bifrostResp = tt.response.ToBifrostChatResponse()
			}

			// Critical: Choices must NEVER be empty (this was the PR #1018 bug)
			require.NotNil(t, bifrostResp, "Response should not be nil")
			require.NotEmpty(t, bifrostResp.Choices, "Empty choices array")
			require.Len(t, bifrostResp.Choices, 1, "Should have exactly one error choice")

			// Verify error signal
			choice := bifrostResp.Choices[0]
			require.NotNil(t, choice.FinishReason, "finish_reason must be set")
			assert.Equal(t, tt.expectFinish, *choice.FinishReason, "finish_reason should signal the error type")

			// Verify message structure exists
			if !tt.isStream {
				require.NotNil(t, choice.ChatNonStreamResponseChoice, "Non-stream should have message")
				require.NotNil(t, choice.ChatNonStreamResponseChoice.Message, "Should have message object")
				assert.Equal(t, schemas.ChatMessageRoleAssistant, choice.ChatNonStreamResponseChoice.Message.Role)
			}
		})
	}
}

func TestToBifrostEmbeddingResponsePreservesPrecision(t *testing.T) {
	const want = 0.12345678901234568

	resp := gemini.ToBifrostEmbeddingResponse(&gemini.GeminiEmbeddingResponse{
		Embeddings: []gemini.GeminiEmbedding{
			{
				Values: []float64{want},
			},
		},
	}, "gemini-embedding-001")

	require.NotNil(t, resp)

	got := resp.Data[0].Embedding.EmbeddingArray[0]
	assert.Equal(t, want, got)
	assert.NotEqual(t, float64(float32(want)), got)
}

// TestThoughtSignatureInToolCalls tests that thought signatures are properly embedded in tool call IDs
// for both streaming and non-streaming responses to enable round-trip compatibility
func TestThoughtSignatureInToolCalls(t *testing.T) {
	thoughtSig := []byte{0x01, 0x02, 0x03, 0x04, 0x05} // Sample signature

	tests := []struct {
		name     string
		response *gemini.GenerateContentResponse
		isStream bool
	}{
		{
			name: "NonStream_ToolCallWithThoughtSignature",
			response: &gemini.GenerateContentResponse{
				ResponseID:   "test-non-stream",
				ModelVersion: "gemini-3-pro-preview",
				Candidates: []*gemini.Candidate{
					{
						Index:        0,
						FinishReason: gemini.FinishReasonStop,
						Content: &gemini.Content{
							Role: string(gemini.RoleModel),
							Parts: []*gemini.Part{
								{
									FunctionCall: &gemini.FunctionCall{
										Name: "get_weather",
										ID:   "call_123",
										Args: json.RawMessage(`{"location":"San Francisco"}`),
									},
									ThoughtSignature: thoughtSig,
								},
							},
						},
					},
				},
			},
			isStream: false,
		},
		{
			name: "Stream_ToolCallWithThoughtSignature",
			response: &gemini.GenerateContentResponse{
				ResponseID:   "test-stream",
				ModelVersion: "gemini-3-pro-preview",
				Candidates: []*gemini.Candidate{
					{
						Index:        0,
						FinishReason: gemini.FinishReasonStop,
						Content: &gemini.Content{
							Role: string(gemini.RoleModel),
							Parts: []*gemini.Part{
								{
									FunctionCall: &gemini.FunctionCall{
										Name: "get_weather",
										ID:   "call_456",
										Args: json.RawMessage(`{"location":"New York"}`),
									},
									ThoughtSignature: thoughtSig,
								},
							},
						},
					},
				},
				UsageMetadata: &gemini.GenerateContentResponseUsageMetadata{
					PromptTokenCount: 10,
					TotalTokenCount:  20,
				},
			},
			isStream: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var bifrostResp *schemas.BifrostChatResponse

			if tt.isStream {
				bifrostResp, _, _ = tt.response.ToBifrostChatCompletionStream(gemini.NewGeminiStreamState())
			} else {
				bifrostResp = tt.response.ToBifrostChatResponse()
			}

			require.NotNil(t, bifrostResp, "Response should not be nil")
			require.NotEmpty(t, bifrostResp.Choices, "Should have choices")

			choice := bifrostResp.Choices[0]

			// Get tool calls from appropriate response type
			var toolCalls []schemas.ChatAssistantMessageToolCall
			if tt.isStream {
				require.NotNil(t, choice.ChatStreamResponseChoice, "Stream should have delta")
				require.NotNil(t, choice.ChatStreamResponseChoice.Delta, "Should have delta")
				toolCalls = choice.ChatStreamResponseChoice.Delta.ToolCalls
			} else {
				require.NotNil(t, choice.ChatNonStreamResponseChoice, "Non-stream should have message")
				require.NotNil(t, choice.ChatNonStreamResponseChoice.Message, "Should have message")
				require.NotNil(t, choice.ChatNonStreamResponseChoice.Message.ChatAssistantMessage, "Should have assistant message")
				toolCalls = choice.ChatNonStreamResponseChoice.Message.ChatAssistantMessage.ToolCalls
			}

			// Critical: Tool call ID must contain embedded thought signature
			require.Len(t, toolCalls, 1, "Should have exactly one tool call")
			toolCall := toolCalls[0]
			require.NotNil(t, toolCall.ID, "Tool call must have ID")

			// Verify thought signature is embedded in the ID (format: "call_id_ts_base64sig")
			assert.Contains(t, *toolCall.ID, "_ts_", "Tool call ID must contain thought signature separator")

			// Verify we can extract the thought signature from the ID for round-trip
			parts := strings.SplitN(*toolCall.ID, "_ts_", 2)
			require.Len(t, parts, 2, "Should be able to split ID into base and signature")
			assert.NotEmpty(t, parts[1], "Signature part should not be empty")

			// Verify reasoning details also contain the signature (backward compatibility)
			var reasoningDetails []schemas.ChatReasoningDetails
			if tt.isStream {
				reasoningDetails = choice.ChatStreamResponseChoice.Delta.ReasoningDetails
			} else {
				reasoningDetails = choice.ChatNonStreamResponseChoice.Message.ChatAssistantMessage.ReasoningDetails
			}

			assert.NotEmpty(t, reasoningDetails, "Should have reasoning details")
			foundEncrypted := false
			for _, detail := range reasoningDetails {
				if detail.Type == schemas.BifrostReasoningDetailsTypeEncrypted && detail.Signature != nil {
					foundEncrypted = true
					break
				}
			}
			assert.True(t, foundEncrypted, "Should have encrypted reasoning detail with signature")
		})
	}
}

func TestMissingThoughtSignatureUsesBypassSentinel(t *testing.T) {
	result, err := gemini.ToGeminiChatCompletionRequest(&schemas.BifrostChatRequest{
		Model: "gemini-3.1-pro-preview",
		Input: []schemas.ChatMessage{
			{
				Role:    schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("What is the weather?")},
			},
			{
				Role: schemas.ChatMessageRoleAssistant,
				ChatAssistantMessage: &schemas.ChatAssistantMessage{
					ToolCalls: []schemas.ChatAssistantMessageToolCall{{
						ID:   schemas.Ptr("call_1"),
						Type: schemas.Ptr("function"),
						Function: schemas.ChatAssistantMessageToolCallFunction{
							Name:      schemas.Ptr("get_weather"),
							Arguments: `{"location":"Boston"}`,
						},
					}},
				},
			},
			{
				Role:            schemas.ChatMessageRoleTool,
				ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: schemas.Ptr("call_1")},
				Content:         &schemas.ChatMessageContent{ContentStr: schemas.Ptr(`{"temperature":"10C"}`)},
			},
		},
	})

	require.Len(t, result.Contents, 3)
	require.Len(t, result.Contents[1].Parts, 1)
	assert.Equal(t, []byte("skip_thought_signature_validator"), result.Contents[1].Parts[0].ThoughtSignature)

	encoded, err := json.Marshal(result)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `"thoughtSignature":"skip_thought_signature_validator"`)
	assert.NotContains(t, string(encoded), `"thoughtSignature":"c2tpcF90aG91Z2h0X3NpZ25hdHVyZV92YWxpZGF0b3I="`)
}

func TestEmbeddedThoughtSignatureDoesNotUseBypassSentinel(t *testing.T) {
	thoughtSig := base64.RawURLEncoding.EncodeToString([]byte{0x01, 0x02, 0x03})
	callID := "call_1_ts_" + thoughtSig

	result, err := gemini.ToGeminiChatCompletionRequest(&schemas.BifrostChatRequest{
		Model: "gemini-3.1-pro-preview",
		Input: []schemas.ChatMessage{{
			Role: schemas.ChatMessageRoleAssistant,
			ChatAssistantMessage: &schemas.ChatAssistantMessage{
				ToolCalls: []schemas.ChatAssistantMessageToolCall{{
					ID:   schemas.Ptr(callID),
					Type: schemas.Ptr("function"),
					Function: schemas.ChatAssistantMessageToolCallFunction{
						Name:      schemas.Ptr("get_weather"),
						Arguments: `{"location":"Boston"}`,
					},
				}},
			},
		}},
	})

	require.Len(t, result.Contents, 1)
	require.Len(t, result.Contents[0].Parts, 1)
	assert.NotEqual(t, []byte("skip_thought_signature_validator"), result.Contents[0].Parts[0].ThoughtSignature)

	encoded, err := json.Marshal(result)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), `"thoughtSignature":"skip_thought_signature_validator"`)
}

func TestThoughtSignatureBypassSentinelRoundTripsThroughJSON(t *testing.T) {
	part := gemini.Part{ThoughtSignature: []byte("skip_thought_signature_validator")}

	encoded, err := json.Marshal(part)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `"thoughtSignature":"skip_thought_signature_validator"`)

	var decoded gemini.Part
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	assert.Equal(t, []byte("skip_thought_signature_validator"), decoded.ThoughtSignature)
}

// TestBifrostToGeminiToolConversion tests the conversion of tools from Bifrost to Gemini format
func TestBifrostToGeminiToolConversion(t *testing.T) {
	tests := []struct {
		name     string
		input    *schemas.BifrostChatRequest
		validate func(t *testing.T, result *gemini.GeminiGenerationRequest)
	}{
		{
			name: "ComprehensiveToolWithArrayAndEnum",
			input: &schemas.BifrostChatRequest{
				Model: "gemini-2.0-flash",
				Input: []schemas.ChatMessage{
					{
						Role: schemas.ChatMessageRoleUser,
						Content: &schemas.ChatMessageContent{
							ContentStr: schemas.Ptr("Test comprehensive tool"),
						},
					},
				},
				Params: &schemas.ChatParameters{
					Tools: []schemas.ChatTool{
						{
							Type: schemas.ChatToolTypeFunction,
							Function: &schemas.ChatToolFunction{
								Name:        "search_products",
								Description: schemas.Ptr("Search for products with filters"),
								Parameters: &schemas.ToolFunctionParameters{
									Type: "object",
									Properties: schemas.NewOrderedMapFromPairs(
										schemas.KV("query", map[string]interface{}{
											"type":        "string",
											"description": "Search query",
										}),
										schemas.KV("category", map[string]interface{}{
											"type":        "string",
											"description": "Product category",
											"enum":        []interface{}{"electronics", "books", "clothing"},
										}),
										schemas.KV("tags", map[string]interface{}{
											"type":        "array",
											"description": "Filter tags",
											"items": map[string]interface{}{
												"type":        "string",
												"description": "A tag",
											},
										}),
									),
									Required: []string{"query"},
								},
							},
						},
					},
				},
			},
			validate: func(t *testing.T, result *gemini.GeminiGenerationRequest) {
				require.Len(t, result.Tools, 1)
				fd := result.Tools[0].FunctionDeclarations[0]

				// Basic validation
				assert.Equal(t, "search_products", fd.Name)
				assert.Equal(t, "Search for products with filters", fd.Description)
				assert.Equal(t, []string{"query"}, fd.Parameters.Required)

				// String property
				queryProp := fd.Parameters.Properties["query"]
				assert.Equal(t, gemini.Type("string"), queryProp.Type)

				// Enum property
				categoryProp := fd.Parameters.Properties["category"]
				assert.Equal(t, gemini.Type("string"), categoryProp.Type)
				assert.Equal(t, []string{"electronics", "books", "clothing"}, categoryProp.Enum)

				// Array with items (the critical bug fix)
				tagsProp := fd.Parameters.Properties["tags"]
				assert.Equal(t, gemini.Type("array"), tagsProp.Type)
				require.NotNil(t, tagsProp.Items, "items field must be present - this was the bug")
				assert.Equal(t, gemini.Type("string"), tagsProp.Items.Type)
			},
		},
		{
			name: "ComplexNestedStructures",
			input: &schemas.BifrostChatRequest{
				Model: "gemini-2.0-flash",
				Input: []schemas.ChatMessage{
					{
						Role: schemas.ChatMessageRoleUser,
						Content: &schemas.ChatMessageContent{
							ContentStr: schemas.Ptr("Test nested structures"),
						},
					},
				},
				Params: &schemas.ChatParameters{
					Tools: []schemas.ChatTool{
						{
							Type: schemas.ChatToolTypeFunction,
							Function: &schemas.ChatToolFunction{
								Name:        "process_order",
								Description: schemas.Ptr("Process customer order"),
								Parameters: &schemas.ToolFunctionParameters{
									Type: "object",
									Properties: schemas.NewOrderedMapFromPairs(
										schemas.KV("customer", map[string]interface{}{
											"type": "object",
											"properties": map[string]interface{}{
												"name": map[string]interface{}{
													"type": "string",
												},
												"email": map[string]interface{}{
													"type": "string",
												},
											},
											"required": []string{"name", "email"},
										}),
										schemas.KV("items", map[string]interface{}{
											"type": "array",
											"items": map[string]interface{}{
												"type": "object",
												"properties": map[string]interface{}{
													"product_id": map[string]interface{}{
														"type": "string",
													},
													"quantity": map[string]interface{}{
														"type": "integer",
													},
												},
												"required": []string{"product_id", "quantity"},
											},
										}),
									),
									Required: []string{"customer", "items"},
								},
							},
						},
					},
				},
			},
			validate: func(t *testing.T, result *gemini.GeminiGenerationRequest) {
				require.Len(t, result.Tools, 1)
				fd := result.Tools[0].FunctionDeclarations[0]

				// Nested object
				customerProp := fd.Parameters.Properties["customer"]
				assert.Equal(t, gemini.Type("object"), customerProp.Type)
				assert.Contains(t, customerProp.Properties, "name")
				assert.Contains(t, customerProp.Properties, "email")
				assert.Equal(t, []string{"name", "email"}, customerProp.Required)

				// Array of objects
				itemsProp := fd.Parameters.Properties["items"]
				assert.Equal(t, gemini.Type("array"), itemsProp.Type)
				require.NotNil(t, itemsProp.Items, "array items must be present")
				assert.Equal(t, gemini.Type("object"), itemsProp.Items.Type)
				assert.Contains(t, itemsProp.Items.Properties, "product_id")
				assert.Contains(t, itemsProp.Items.Properties, "quantity")
				assert.Equal(t, []string{"product_id", "quantity"}, itemsProp.Items.Required)
			},
		},
		{
			// This test reproduces the bug where nested properties inside array items
			// are *OrderedMap (from JSON deserialization) instead of map[string]interface{}.
			// The old code only handled map[string]interface{}, silently dropping properties
			// while keeping required, causing Gemini to reject with "property is not defined".
			name: "NestedOrderedMapPropertiesInArrayItems",
			input: &schemas.BifrostChatRequest{
				Model: "gemini-2.0-flash",
				Input: []schemas.ChatMessage{
					{
						Role: schemas.ChatMessageRoleUser,
						Content: &schemas.ChatMessageContent{
							ContentStr: schemas.Ptr("Test nested OrderedMap properties"),
						},
					},
				},
				Params: &schemas.ChatParameters{
					Tools: []schemas.ChatTool{
						{
							Type: schemas.ChatToolTypeFunction,
							Function: &schemas.ChatToolFunction{
								Name:        "browser_fill_form",
								Description: schemas.Ptr("Fill form fields"),
								Parameters: &schemas.ToolFunctionParameters{
									Type: "object",
									// Use OrderedMap for the nested items.properties to simulate
									// JSON deserialization, which stores nested objects as *OrderedMap
									Properties: schemas.NewOrderedMapFromPairs(
										schemas.KV("fields", map[string]interface{}{
											"type":        "array",
											"description": "Fields to fill in",
											"items": schemas.NewOrderedMapFromPairs(
												schemas.KV("type", "object"),
												schemas.KV("properties", schemas.NewOrderedMapFromPairs(
													schemas.KV("name", map[string]interface{}{
														"type":        "string",
														"description": "Human-readable field name",
													}),
													schemas.KV("ref", map[string]interface{}{
														"type":        "string",
														"description": "Target field reference",
													}),
													schemas.KV("value", map[string]interface{}{
														"type":        "string",
														"description": "Value to fill",
													}),
												)),
												schemas.KV("required", []interface{}{"name", "ref", "value"}),
												schemas.KV("additionalProperties", false),
											),
										}),
									),
									Required: []string{"fields"},
								},
							},
						},
					},
				},
			},
			validate: func(t *testing.T, result *gemini.GeminiGenerationRequest) {
				require.Len(t, result.Tools, 1)
				fd := result.Tools[0].FunctionDeclarations[0]
				assert.Equal(t, "browser_fill_form", fd.Name)

				fieldsProp := fd.Parameters.Properties["fields"]
				assert.Equal(t, gemini.Type("array"), fieldsProp.Type)
				require.NotNil(t, fieldsProp.Items, "array items must be present")
				assert.Equal(t, gemini.Type("object"), fieldsProp.Items.Type)

				// This is the critical assertion: nested properties inside items must
				// be preserved even when they come as *OrderedMap from JSON deserialization.
				require.NotNil(t, fieldsProp.Items.Properties, "nested properties must not be nil - this was the bug")
				assert.Contains(t, fieldsProp.Items.Properties, "name")
				assert.Contains(t, fieldsProp.Items.Properties, "ref")
				assert.Contains(t, fieldsProp.Items.Properties, "value")
				assert.Equal(t, []string{"name", "ref", "value"}, fieldsProp.Items.Required)
			},
		},
		{
			name: "EmptyItemsObject",
			input: &schemas.BifrostChatRequest{
				Model: "gemini-2.0-flash",
				Input: []schemas.ChatMessage{
					{
						Role: schemas.ChatMessageRoleUser,
						Content: &schemas.ChatMessageContent{
							ContentStr: schemas.Ptr("Edge case test"),
						},
					},
				},
				Params: &schemas.ChatParameters{
					Tools: []schemas.ChatTool{
						{
							Type: schemas.ChatToolTypeFunction,
							Function: &schemas.ChatToolFunction{
								Name: "test_tool",
								Parameters: &schemas.ToolFunctionParameters{
									Type: "object",
									Properties: schemas.NewOrderedMapFromPairs(
										schemas.KV("data", map[string]interface{}{
											"type":  "array",
											"items": map[string]interface{}{}, // Empty items object
										}),
									),
								},
							},
						},
					},
				},
			},
			validate: func(t *testing.T, result *gemini.GeminiGenerationRequest) {
				fd := result.Tools[0].FunctionDeclarations[0]
				dataProp := fd.Parameters.Properties["data"]

				// Even empty items should be converted (not nil)
				assert.NotNil(t, dataProp.Items, "empty items object should still be present")
			},
		},
		{
			name: "ToolWithValidationConstraints",
			input: &schemas.BifrostChatRequest{
				Model: "gemini-2.0-flash",
				Input: []schemas.ChatMessage{
					{
						Role: schemas.ChatMessageRoleUser,
						Content: &schemas.ChatMessageContent{
							ContentStr: schemas.Ptr("Test validation constraints"),
						},
					},
				},
				Params: &schemas.ChatParameters{
					Tools: []schemas.ChatTool{
						{
							Type: schemas.ChatToolTypeFunction,
							Function: &schemas.ChatToolFunction{
								Name:        "validate_input",
								Description: schemas.Ptr("Validate input with constraints"),
								Parameters: &schemas.ToolFunctionParameters{
									Type: "object",
									Properties: schemas.NewOrderedMapFromPairs(
										schemas.KV("username", map[string]interface{}{
											"type":        "string",
											"description": "Username with length constraints",
											"minLength":   float64(3),
											"maxLength":   float64(20),
											"pattern":     "^[a-zA-Z0-9_]+$",
										}),
										schemas.KV("age", map[string]interface{}{
											"type":    "integer",
											"minimum": float64(0),
											"maximum": float64(150),
										}),
										schemas.KV("tags", map[string]interface{}{
											"type":     "array",
											"minItems": float64(1),
											"maxItems": float64(5),
											"items": map[string]interface{}{
												"type": "string",
											},
										}),
									),
									Required: []string{"username"},
								},
							},
						},
					},
				},
			},
			validate: func(t *testing.T, result *gemini.GeminiGenerationRequest) {
				require.Len(t, result.Tools, 1)
				fd := result.Tools[0].FunctionDeclarations[0]

				// Validate string constraints
				usernameProp := fd.Parameters.Properties["username"]
				assert.Equal(t, gemini.Type("string"), usernameProp.Type)
				require.NotNil(t, usernameProp.MinLength, "minLength should be set")
				assert.Equal(t, int64(3), *usernameProp.MinLength)
				require.NotNil(t, usernameProp.MaxLength, "maxLength should be set")
				assert.Equal(t, int64(20), *usernameProp.MaxLength)
				assert.Equal(t, "^[a-zA-Z0-9_]+$", usernameProp.Pattern)

				// Validate number constraints
				ageProp := fd.Parameters.Properties["age"]
				assert.Equal(t, gemini.Type("integer"), ageProp.Type)
				require.NotNil(t, ageProp.Minimum, "minimum should be set")
				assert.Equal(t, float64(0), *ageProp.Minimum)
				require.NotNil(t, ageProp.Maximum, "maximum should be set")
				assert.Equal(t, float64(150), *ageProp.Maximum)

				// Validate array constraints
				tagsProp := fd.Parameters.Properties["tags"]
				assert.Equal(t, gemini.Type("array"), tagsProp.Type)
				require.NotNil(t, tagsProp.MinItems, "minItems should be set")
				assert.Equal(t, int64(1), *tagsProp.MinItems)
				require.NotNil(t, tagsProp.MaxItems, "maxItems should be set")
				assert.Equal(t, int64(5), *tagsProp.MaxItems)
			},
		},
		{
			name: "ToolWithAnyOfUnionTypes",
			input: &schemas.BifrostChatRequest{
				Model: "gemini-2.0-flash",
				Input: []schemas.ChatMessage{
					{
						Role: schemas.ChatMessageRoleUser,
						Content: &schemas.ChatMessageContent{
							ContentStr: schemas.Ptr("Test anyOf union types"),
						},
					},
				},
				Params: &schemas.ChatParameters{
					Tools: []schemas.ChatTool{
						{
							Type: schemas.ChatToolTypeFunction,
							Function: &schemas.ChatToolFunction{
								Name:        "process_id",
								Description: schemas.Ptr("Process ID that can be string or number"),
								Parameters: &schemas.ToolFunctionParameters{
									Type: "object",
									Properties: schemas.NewOrderedMapFromPairs(
										schemas.KV("id", map[string]interface{}{
											"anyOf": []interface{}{
												map[string]interface{}{"type": "string"},
												map[string]interface{}{"type": "integer"},
											},
											"description": "ID that can be string or integer",
										}),
									),
									Required: []string{"id"},
								},
							},
						},
					},
				},
			},
			validate: func(t *testing.T, result *gemini.GeminiGenerationRequest) {
				require.Len(t, result.Tools, 1)
				fd := result.Tools[0].FunctionDeclarations[0]

				// Validate anyOf is preserved
				idProp := fd.Parameters.Properties["id"]
				require.NotNil(t, idProp.AnyOf, "anyOf should be set")
				require.Len(t, idProp.AnyOf, 2, "anyOf should have 2 options")
				assert.Equal(t, gemini.Type("string"), idProp.AnyOf[0].Type)
				assert.Equal(t, gemini.Type("integer"), idProp.AnyOf[1].Type)
			},
		},
		{
			name: "ToolWithTopLevelItems",
			input: &schemas.BifrostChatRequest{
				Model: "gemini-2.0-flash",
				Input: []schemas.ChatMessage{
					{
						Role: schemas.ChatMessageRoleUser,
						Content: &schemas.ChatMessageContent{
							ContentStr: schemas.Ptr("Test top-level items field"),
						},
					},
				},
				Params: &schemas.ChatParameters{
					Tools: []schemas.ChatTool{
						{
							Type: schemas.ChatToolTypeFunction,
							Function: &schemas.ChatToolFunction{
								Name:        "process_list",
								Description: schemas.Ptr("Process a list of items"),
								Parameters: &schemas.ToolFunctionParameters{
									Type: "array",
									Items: schemas.NewOrderedMapFromPairs(
										schemas.KV("type", "string"),
										schemas.KV("description", "Item in the list"),
									),
									MinItems: schemas.Ptr(int64(1)),
									MaxItems: schemas.Ptr(int64(10)),
								},
							},
						},
					},
				},
			},
			validate: func(t *testing.T, result *gemini.GeminiGenerationRequest) {
				require.Len(t, result.Tools, 1)
				fd := result.Tools[0].FunctionDeclarations[0]

				// Validate top-level array schema
				assert.Equal(t, gemini.Type("array"), fd.Parameters.Type)
				require.NotNil(t, fd.Parameters.Items, "items should be set on top-level array")
				assert.Equal(t, gemini.Type("string"), fd.Parameters.Items.Type)
				require.NotNil(t, fd.Parameters.MinItems, "minItems should be set")
				assert.Equal(t, int64(1), *fd.Parameters.MinItems)
				require.NotNil(t, fd.Parameters.MaxItems, "maxItems should be set")
				assert.Equal(t, int64(10), *fd.Parameters.MaxItems)
			},
		},
		{
			name: "ToolWithMiscFields",
			input: &schemas.BifrostChatRequest{
				Model: "gemini-2.0-flash",
				Input: []schemas.ChatMessage{
					{
						Role: schemas.ChatMessageRoleUser,
						Content: &schemas.ChatMessageContent{
							ContentStr: schemas.Ptr("Test misc fields"),
						},
					},
				},
				Params: &schemas.ChatParameters{
					Tools: []schemas.ChatTool{
						{
							Type: schemas.ChatToolTypeFunction,
							Function: &schemas.ChatToolFunction{
								Name:        "config_tool",
								Description: schemas.Ptr("Tool with misc schema fields"),
								Parameters: &schemas.ToolFunctionParameters{
									Type:  "object",
									Title: schemas.Ptr("ConfigParameters"),
									Properties: schemas.NewOrderedMapFromPairs(
										schemas.KV("enabled", map[string]interface{}{
											"type":     "boolean",
											"default":  true,
											"nullable": true,
											"title":    "Enabled Flag",
										}),
										schemas.KV("format_type", map[string]interface{}{
											"type":   "string",
											"format": "email",
										}),
									),
								},
							},
						},
					},
				},
			},
			validate: func(t *testing.T, result *gemini.GeminiGenerationRequest) {
				require.Len(t, result.Tools, 1)
				fd := result.Tools[0].FunctionDeclarations[0]

				// Validate title at top level
				assert.Equal(t, "ConfigParameters", fd.Parameters.Title)

				// Validate misc fields on properties
				enabledProp := fd.Parameters.Properties["enabled"]
				assert.Equal(t, true, enabledProp.Default)
				require.NotNil(t, enabledProp.Nullable, "nullable should be set")
				assert.True(t, *enabledProp.Nullable)
				assert.Equal(t, "Enabled Flag", enabledProp.Title)

				formatTypeProp := fd.Parameters.Properties["format_type"]
				assert.Equal(t, "email", formatTypeProp.Format)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := gemini.ToGeminiChatCompletionRequest(tt.input)
			require.NoError(t, err)
			require.NotNil(t, result, "Conversion should not return nil")
			tt.validate(t, result)
		})
	}
}

func TestBifrostToGeminiToolConversion_PropertyOrdering(t *testing.T) {
	input := &schemas.BifrostChatRequest{
		Model: "gemini-2.0-flash",
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("test")},
		}},
		Params: &schemas.ChatParameters{
			Tools: []schemas.ChatTool{{
				Type: schemas.ChatToolTypeFunction,
				Function: &schemas.ChatToolFunction{
					Name:        "AnswerResponseModel",
					Description: schemas.Ptr("Extract answer"),
					Parameters: &schemas.ToolFunctionParameters{
						Type: "object",
						Properties: schemas.NewOrderedMapFromPairs(
							schemas.KV("chain_of_thought", map[string]interface{}{"type": "string", "description": "Reasoning"}),
							schemas.KV("answer", map[string]interface{}{"type": "string", "description": "The answer"}),
							schemas.KV("citations", map[string]interface{}{"type": "array"}),
						),
						Required: []string{"chain_of_thought", "answer"},
					},
				},
			}},
		},
	}

	result, err := gemini.ToGeminiChatCompletionRequest(input)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, result.Tools, 1)
	fd := result.Tools[0].FunctionDeclarations[0]

	// CoT: PropertyOrdering preserves client's intended field order
	assert.Equal(t, []string{"chain_of_thought", "answer", "citations"}, fd.Parameters.PropertyOrdering,
		"PropertyOrdering should preserve original property order")

	// All properties present in map
	assert.Len(t, fd.Parameters.Properties, 3)
	assert.Contains(t, fd.Parameters.Properties, "chain_of_thought")
	assert.Contains(t, fd.Parameters.Properties, "answer")
	assert.Contains(t, fd.Parameters.Properties, "citations")
}

func TestBifrostToGeminiToolConversion_NestedPropertyOrdering(t *testing.T) {
	input := &schemas.BifrostChatRequest{
		Model: "gemini-2.0-flash",
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("test")},
		}},
		Params: &schemas.ChatParameters{
			Tools: []schemas.ChatTool{{
				Type: schemas.ChatToolTypeFunction,
				Function: &schemas.ChatToolFunction{
					Name: "nested_tool",
					Parameters: &schemas.ToolFunctionParameters{
						Type: "object",
						Properties: schemas.NewOrderedMapFromPairs(
							schemas.KV("output", schemas.NewOrderedMapFromPairs(
								schemas.KV("type", "object"),
								schemas.KV("properties", schemas.NewOrderedMapFromPairs(
									schemas.KV("verdict", schemas.NewOrderedMapFromPairs(schemas.KV("type", "string"))),
									schemas.KV("score", schemas.NewOrderedMapFromPairs(schemas.KV("type", "number"))),
									schemas.KV("explanation", schemas.NewOrderedMapFromPairs(schemas.KV("type", "string"))),
								)),
							)),
							schemas.KV("reasoning", map[string]interface{}{"type": "string"}),
						),
					},
				},
			}},
		},
	}

	result, err := gemini.ToGeminiChatCompletionRequest(input)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, result.Tools, 1)
	fd := result.Tools[0].FunctionDeclarations[0]

	// Top-level property ordering
	assert.Equal(t, []string{"output", "reasoning"}, fd.Parameters.PropertyOrdering)

	// Nested property ordering
	outputSchema := fd.Parameters.Properties["output"]
	require.NotNil(t, outputSchema)
	assert.Equal(t, []string{"verdict", "score", "explanation"}, outputSchema.PropertyOrdering,
		"nested PropertyOrdering should preserve original order")
}

// TestStructuredOutputConversion tests that response_format with json_schema is properly converted to Gemini's responseJsonSchema
func TestStructuredOutputConversion(t *testing.T) {
	tests := []struct {
		name     string
		input    *schemas.BifrostChatRequest
		validate func(t *testing.T, result *gemini.GeminiGenerationRequest)
	}{
		{
			name: "JSONSchemaWithUnionTypes_ConvertedToAnyOf",
			input: &schemas.BifrostChatRequest{
				Model: "gemini-2.5-pro",
				Input: []schemas.ChatMessage{
					{
						Role: schemas.ChatMessageRoleUser,
						Content: &schemas.ChatMessageContent{
							ContentStr: schemas.Ptr("Extract information: User ID is 12345, Status is \"active\""),
						},
					},
				},
				Params: &schemas.ChatParameters{
					ResponseFormat: schemas.Ptr[interface{}](map[string]interface{}{
						"type": "json_schema",
						"json_schema": map[string]interface{}{
							"name": "UserInfo",
							"schema": map[string]interface{}{
								"type": "object",
								"properties": map[string]interface{}{
									"user_id": map[string]interface{}{
										"type":        []interface{}{"string", "integer"},
										"description": "User ID as string or integer",
									},
									"status": map[string]interface{}{
										"type": "string",
										"enum": []interface{}{"active", "inactive"},
									},
								},
								"required":             []interface{}{"user_id", "status"},
								"additionalProperties": false,
							},
						},
					}),
				},
			},
			validate: func(t *testing.T, result *gemini.GeminiGenerationRequest) {
				// Verify ResponseMIMEType is set
				assert.Equal(t, "application/json", result.GenerationConfig.ResponseMIMEType, "responseMimeType should be application/json")

				// Verify ResponseJSONSchema is set
				assert.NotNil(t, result.GenerationConfig.ResponseJSONSchema, "responseJsonSchema should be set")

				// Validate the schema structure
				schemaMap, ok := result.GenerationConfig.ResponseJSONSchema.(map[string]interface{})
				require.True(t, ok, "ResponseJSONSchema should be a map")

				// Check properties
				properties, ok := schemaMap["properties"].(map[string]interface{})
				require.True(t, ok, "properties should be a map")

				// Validate user_id property - should be converted to anyOf
				userID, ok := properties["user_id"].(map[string]interface{})
				require.True(t, ok, "user_id should exist in properties")

				// user_id should have anyOf instead of type array
				anyOf, hasAnyOf := userID["anyOf"]
				assert.True(t, hasAnyOf, "user_id should have anyOf for union types")

				anyOfSlice, ok := anyOf.([]interface{})
				require.True(t, ok, "anyOf should be a slice")
				require.Len(t, anyOfSlice, 2, "anyOf should have 2 branches for string and integer")

				// Verify the anyOf branches
				stringBranch := anyOfSlice[0].(map[string]interface{})
				assert.Equal(t, "string", stringBranch["type"])

				integerBranch := anyOfSlice[1].(map[string]interface{})
				assert.Equal(t, "integer", integerBranch["type"])

				// Validate status property - should remain unchanged
				status, ok := properties["status"].(map[string]interface{})
				require.True(t, ok, "status should exist in properties")
				assert.Equal(t, "string", status["type"])
				enum := status["enum"].([]interface{})
				assert.Len(t, enum, 2)
			},
		},
		{
			name: "JSONSchemaWithNullableType_KeptAsArray",
			input: &schemas.BifrostChatRequest{
				Model: "gemini-2.5-pro",
				Input: []schemas.ChatMessage{
					{
						Role: schemas.ChatMessageRoleUser,
						Content: &schemas.ChatMessageContent{
							ContentStr: schemas.Ptr("Extract nullable field"),
						},
					},
				},
				Params: &schemas.ChatParameters{
					ResponseFormat: schemas.Ptr[interface{}](map[string]interface{}{
						"type": "json_schema",
						"json_schema": map[string]interface{}{
							"name": "NullableData",
							"schema": map[string]interface{}{
								"type": "object",
								"properties": map[string]interface{}{
									"name": map[string]interface{}{
										"type": []interface{}{"string", "null"},
									},
								},
							},
						},
					}),
				},
			},
			validate: func(t *testing.T, result *gemini.GeminiGenerationRequest) {
				schemaMap := result.GenerationConfig.ResponseJSONSchema.(map[string]interface{})
				properties := schemaMap["properties"].(map[string]interface{})
				name := properties["name"].(map[string]interface{})

				// Nullable types should be kept as array (Gemini supports this)
				typeVal := name["type"]
				typeSlice, ok := typeVal.([]interface{})
				require.True(t, ok, "type should remain as array for nullable types")
				require.Len(t, typeSlice, 2)
				assert.Contains(t, typeSlice, "string")
				assert.Contains(t, typeSlice, "null")
			},
		},
		{
			name: "JSONSchemaComplex",
			input: &schemas.BifrostChatRequest{
				Model: "gemini-2.5-pro",
				Input: []schemas.ChatMessage{
					{
						Role: schemas.ChatMessageRoleUser,
						Content: &schemas.ChatMessageContent{
							ContentStr: schemas.Ptr("Extract nested data"),
						},
					},
				},
				Params: &schemas.ChatParameters{
					ResponseFormat: schemas.Ptr[interface{}](map[string]interface{}{
						"type": "json_schema",
						"json_schema": map[string]interface{}{
							"name": "ComplexData",
							"schema": map[string]interface{}{
								"type": "object",
								"properties": map[string]interface{}{
									"items": map[string]interface{}{
										"type": "array",
										"items": map[string]interface{}{
											"type": "object",
											"properties": map[string]interface{}{
												"id": map[string]interface{}{
													"type": "integer",
												},
												"name": map[string]interface{}{
													"type": "string",
												},
											},
											"required": []interface{}{"id", "name"},
										},
									},
								},
								"required": []interface{}{"items"},
							},
						},
					}),
				},
			},
			validate: func(t *testing.T, result *gemini.GeminiGenerationRequest) {
				assert.Equal(t, "application/json", result.GenerationConfig.ResponseMIMEType)
				assert.NotNil(t, result.GenerationConfig.ResponseJSONSchema)

				schemaMap := result.GenerationConfig.ResponseJSONSchema.(map[string]interface{})
				properties := schemaMap["properties"].(map[string]interface{})
				items := properties["items"].(map[string]interface{})

				// Validate array items
				assert.Equal(t, "array", items["type"])
				itemsSchema := items["items"].(map[string]interface{})
				assert.Equal(t, "object", itemsSchema["type"])

				// Validate nested properties
				nestedProps := itemsSchema["properties"].(map[string]interface{})
				assert.Contains(t, nestedProps, "id")
				assert.Contains(t, nestedProps, "name")
			},
		},
		{
			name: "JSONObjectFormat",
			input: &schemas.BifrostChatRequest{
				Model: "gemini-2.5-pro",
				Input: []schemas.ChatMessage{
					{
						Role: schemas.ChatMessageRoleUser,
						Content: &schemas.ChatMessageContent{
							ContentStr: schemas.Ptr("Return JSON"),
						},
					},
				},
				Params: &schemas.ChatParameters{
					ResponseFormat: schemas.Ptr[interface{}](map[string]interface{}{
						"type": "json_object",
					}),
				},
			},
			validate: func(t *testing.T, result *gemini.GeminiGenerationRequest) {
				// json_object should only set ResponseMIMEType without schema
				assert.Equal(t, "application/json", result.GenerationConfig.ResponseMIMEType)
				assert.Nil(t, result.GenerationConfig.ResponseJSONSchema)
				assert.Nil(t, result.GenerationConfig.ResponseSchema)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := gemini.ToGeminiChatCompletionRequest(tt.input)
			require.NoError(t, err)
			require.NotNil(t, result, "Conversion should not return nil")
			tt.validate(t, result)
		})
	}
}

// TestResponsesStructuredOutputConversion tests that Responses API text config with union types is properly handled
func TestResponsesStructuredOutputConversion(t *testing.T) {
	tests := []struct {
		name     string
		input    *schemas.BifrostResponsesRequest
		validate func(t *testing.T, result *gemini.GeminiGenerationRequest)
	}{
		{
			name: "ResponsesAPI_UnionTypes_ConvertedToAnyOf",
			input: &schemas.BifrostResponsesRequest{
				Provider: schemas.Gemini,
				Model:    "gemini-2.5-pro",
				Input: []schemas.ResponsesMessage{
					{
						Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
						Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
						Content: &schemas.ResponsesMessageContent{
							ContentStr: schemas.Ptr("Extract info with union types"),
						},
					},
				},
				Params: &schemas.ResponsesParameters{
					Text: &schemas.ResponsesTextConfig{
						Format: &schemas.ResponsesTextConfigFormat{
							Type: "json_schema",
							Name: schemas.Ptr("UserInfo"),
							JSONSchema: &schemas.ResponsesTextConfigFormatJSONSchema{
								Type: schemas.Ptr("object"),
								Properties: &map[string]interface{}{
									"user_id": map[string]interface{}{
										"type":        []interface{}{"string", "integer"},
										"description": "User ID as string or integer",
									},
									"status": map[string]interface{}{
										"type": "string",
										"enum": []interface{}{"active", "inactive"},
									},
								},
								Required: []string{"user_id", "status"},
								AdditionalProperties: &schemas.AdditionalPropertiesStruct{
									AdditionalPropertiesBool: schemas.Ptr(false),
								},
							},
						},
					},
				},
			},
			validate: func(t *testing.T, result *gemini.GeminiGenerationRequest) {
				// Verify ResponseMIMEType is set
				assert.Equal(t, "application/json", result.GenerationConfig.ResponseMIMEType)
				assert.NotNil(t, result.GenerationConfig.ResponseJSONSchema)

				// Validate the schema structure
				schemaMap, ok := result.GenerationConfig.ResponseJSONSchema.(map[string]interface{})
				require.True(t, ok, "ResponseJSONSchema should be a map")

				properties, ok := schemaMap["properties"].(map[string]interface{})
				require.True(t, ok, "properties should be a map")

				// Validate user_id property - should be converted to anyOf
				userID, ok := properties["user_id"].(map[string]interface{})
				require.True(t, ok, "user_id should exist in properties")

				// user_id should have anyOf instead of type array
				anyOf, hasAnyOf := userID["anyOf"]
				assert.True(t, hasAnyOf, "user_id should have anyOf for union types in Responses API")

				anyOfSlice, ok := anyOf.([]interface{})
				require.True(t, ok, "anyOf should be a slice")
				require.Len(t, anyOfSlice, 2, "anyOf should have 2 branches for string and integer")

				// Verify the anyOf branches
				stringBranch := anyOfSlice[0].(map[string]interface{})
				assert.Equal(t, "string", stringBranch["type"])

				integerBranch := anyOfSlice[1].(map[string]interface{})
				assert.Equal(t, "integer", integerBranch["type"])
			},
		},
		{
			name: "ResponsesAPI_NullableType_KeptAsArray",
			input: &schemas.BifrostResponsesRequest{
				Provider: schemas.Gemini,
				Model:    "gemini-2.5-pro",
				Input: []schemas.ResponsesMessage{
					{
						Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
						Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
						Content: &schemas.ResponsesMessageContent{
							ContentStr: schemas.Ptr("Extract nullable field"),
						},
					},
				},
				Params: &schemas.ResponsesParameters{
					Text: &schemas.ResponsesTextConfig{
						Format: &schemas.ResponsesTextConfigFormat{
							Type: "json_schema",
							Name: schemas.Ptr("NullableData"),
							JSONSchema: &schemas.ResponsesTextConfigFormatJSONSchema{
								Type: schemas.Ptr("object"),
								Properties: &map[string]interface{}{
									"name": map[string]interface{}{
										"type": []interface{}{"string", "null"},
									},
								},
							},
						},
					},
				},
			},
			validate: func(t *testing.T, result *gemini.GeminiGenerationRequest) {
				schemaMap := result.GenerationConfig.ResponseJSONSchema.(map[string]interface{})
				properties := schemaMap["properties"].(map[string]interface{})
				name := properties["name"].(map[string]interface{})

				// Nullable types should be kept as array (Gemini supports this)
				typeVal := name["type"]
				typeSlice, ok := typeVal.([]interface{})
				require.True(t, ok, "type should remain as array for nullable types in Responses API")
				require.Len(t, typeSlice, 2)
				assert.Contains(t, typeSlice, "string")
				assert.Contains(t, typeSlice, "null")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := gemini.ToGeminiResponsesRequest(tt.input)
			require.NoError(t, err)
			require.NotNil(t, result, "Responses API conversion should not return nil")
			tt.validate(t, result)
		})
	}
}

// TestParallelFunctionCallingConversion tests that multiple consecutive tool responses are properly grouped
func TestParallelFunctionCallingConversion(t *testing.T) {
	tests := []struct {
		name     string
		input    *schemas.BifrostChatRequest
		validate func(t *testing.T, result *gemini.GeminiGenerationRequest)
	}{
		{
			name: "SingleToolResponse_NotGrouped",
			input: &schemas.BifrostChatRequest{
				Model: "gemini-2.0-flash",
				Input: []schemas.ChatMessage{
					{
						Role: schemas.ChatMessageRoleUser,
						Content: &schemas.ChatMessageContent{
							ContentStr: schemas.Ptr("What's the weather?"),
						},
					},
					{
						Role: schemas.ChatMessageRoleAssistant,
						ChatAssistantMessage: &schemas.ChatAssistantMessage{
							ToolCalls: []schemas.ChatAssistantMessageToolCall{
								{
									ID:   schemas.Ptr("call_1"),
									Type: schemas.Ptr("function"),
									Function: schemas.ChatAssistantMessageToolCallFunction{
										Name:      schemas.Ptr("get_weather"),
										Arguments: `{"location":"Tokyo"}`,
									},
								},
							},
						},
					},
					{
						Role: schemas.ChatMessageRoleTool,
						ChatToolMessage: &schemas.ChatToolMessage{
							ToolCallID: schemas.Ptr("call_1"),
						},
						Content: &schemas.ChatMessageContent{
							ContentStr: schemas.Ptr(`{"temperature":22,"condition":"sunny"}`),
						},
					},
				},
			},
			validate: func(t *testing.T, result *gemini.GeminiGenerationRequest) {
				require.NotNil(t, result)
				require.Len(t, result.Contents, 3, "Should have 3 Contents: user, assistant with tool calls, tool response")

				// Validate tool response content (last Content)
				toolResponseContent := result.Contents[2]
				assert.Equal(t, "model", toolResponseContent.Role, "Tool responses use 'model' role in Gemini")
				require.Len(t, toolResponseContent.Parts, 1, "Should have exactly 1 part for single tool response")

				// Verify ONLY functionResponse part (no text part)
				part := toolResponseContent.Parts[0]
				assert.Empty(t, part.Text, "Tool response should NOT have text part")
				require.NotNil(t, part.FunctionResponse, "Tool response must have functionResponse")
				assert.Equal(t, "call_1", part.FunctionResponse.ID)
				assert.Equal(t, "get_weather", part.FunctionResponse.Name)
			},
		},
		{
			name: "ParallelFunctionCalling_TwoToolResponses_Grouped",
			input: &schemas.BifrostChatRequest{
				Model: "gemini-2.0-flash",
				Input: []schemas.ChatMessage{
					{
						Role: schemas.ChatMessageRoleUser,
						Content: &schemas.ChatMessageContent{
							ContentStr: schemas.Ptr("What's the weather and time in Tokyo?"),
						},
					},
					{
						Role: schemas.ChatMessageRoleAssistant,
						ChatAssistantMessage: &schemas.ChatAssistantMessage{
							ToolCalls: []schemas.ChatAssistantMessageToolCall{
								{
									ID:   schemas.Ptr("call_1"),
									Type: schemas.Ptr("function"),
									Function: schemas.ChatAssistantMessageToolCallFunction{
										Name:      schemas.Ptr("get_weather"),
										Arguments: `{"location":"Tokyo"}`,
									},
								},
								{
									ID:   schemas.Ptr("call_2"),
									Type: schemas.Ptr("function"),
									Function: schemas.ChatAssistantMessageToolCallFunction{
										Name:      schemas.Ptr("get_time"),
										Arguments: `{"timezone":"Asia/Tokyo"}`,
									},
								},
							},
						},
					},
					{
						Role: schemas.ChatMessageRoleTool,
						ChatToolMessage: &schemas.ChatToolMessage{
							ToolCallID: schemas.Ptr("call_1"),
						},
						Content: &schemas.ChatMessageContent{
							ContentStr: schemas.Ptr(`{"temperature":22,"condition":"sunny"}`),
						},
					},
					{
						Role: schemas.ChatMessageRoleTool,
						ChatToolMessage: &schemas.ChatToolMessage{
							ToolCallID: schemas.Ptr("call_2"),
						},
						Content: &schemas.ChatMessageContent{
							ContentStr: schemas.Ptr(`{"time":"10:30 AM","date":"2026-01-20"}`),
						},
					},
				},
			},
			validate: func(t *testing.T, result *gemini.GeminiGenerationRequest) {
				require.NotNil(t, result)
				require.Len(t, result.Contents, 3, "Should have 3 Contents: user, assistant with tool calls, grouped tool responses")

				// Validate grouped tool responses (last Content)
				toolResponseContent := result.Contents[2]
				assert.Equal(t, "model", toolResponseContent.Role, "Grouped tool responses use 'model' role")
				require.Len(t, toolResponseContent.Parts, 2, "Should have exactly 2 parts for 2 tool responses (parallel calling)")

				// Verify first tool response - ONLY functionResponse
				part1 := toolResponseContent.Parts[0]
				assert.Empty(t, part1.Text, "Tool response 1 should NOT have text part")
				require.NotNil(t, part1.FunctionResponse, "Tool response 1 must have functionResponse")
				assert.Equal(t, "call_1", part1.FunctionResponse.ID)
				assert.Equal(t, "get_weather", part1.FunctionResponse.Name)

				// Verify second tool response - ONLY functionResponse
				part2 := toolResponseContent.Parts[1]
				assert.Empty(t, part2.Text, "Tool response 2 should NOT have text part")
				require.NotNil(t, part2.FunctionResponse, "Tool response 2 must have functionResponse")
				assert.Equal(t, "call_2", part2.FunctionResponse.ID)
				assert.Equal(t, "get_time", part2.FunctionResponse.Name)
			},
		},
		{
			name: "ParallelFunctionCalling_ThreeToolResponses_AllGrouped",
			input: &schemas.BifrostChatRequest{
				Model: "gemini-2.0-flash",
				Input: []schemas.ChatMessage{
					{
						Role: schemas.ChatMessageRoleUser,
						Content: &schemas.ChatMessageContent{
							ContentStr: schemas.Ptr("Get weather, time, and news for Tokyo"),
						},
					},
					{
						Role: schemas.ChatMessageRoleAssistant,
						ChatAssistantMessage: &schemas.ChatAssistantMessage{
							ToolCalls: []schemas.ChatAssistantMessageToolCall{
								{ID: schemas.Ptr("call_1"), Type: schemas.Ptr("function"), Function: schemas.ChatAssistantMessageToolCallFunction{Name: schemas.Ptr("get_weather"), Arguments: `{}`}},
								{ID: schemas.Ptr("call_2"), Type: schemas.Ptr("function"), Function: schemas.ChatAssistantMessageToolCallFunction{Name: schemas.Ptr("get_time"), Arguments: `{}`}},
								{ID: schemas.Ptr("call_3"), Type: schemas.Ptr("function"), Function: schemas.ChatAssistantMessageToolCallFunction{Name: schemas.Ptr("get_news"), Arguments: `{}`}},
							},
						},
					},
					{
						Role:            schemas.ChatMessageRoleTool,
						ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: schemas.Ptr("call_1")},
						Content:         &schemas.ChatMessageContent{ContentStr: schemas.Ptr(`{"temperature":22}`)},
					},
					{
						Role:            schemas.ChatMessageRoleTool,
						ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: schemas.Ptr("call_2")},
						Content:         &schemas.ChatMessageContent{ContentStr: schemas.Ptr(`{"time":"10:30"}`)},
					},
					{
						Role:            schemas.ChatMessageRoleTool,
						ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: schemas.Ptr("call_3")},
						Content:         &schemas.ChatMessageContent{ContentStr: schemas.Ptr(`{"headline":"Breaking"}`)},
					},
				},
			},
			validate: func(t *testing.T, result *gemini.GeminiGenerationRequest) {
				require.Len(t, result.Contents, 3, "Should have 3 Contents: user, assistant with tool calls, grouped tool responses")

				toolResponseContent := result.Contents[2]
				assert.Equal(t, "model", toolResponseContent.Role)
				require.Len(t, toolResponseContent.Parts, 3, "Should have exactly 3 parts for 3 tool responses")

				// Verify all are functionResponse only (no text)
				for i, part := range toolResponseContent.Parts {
					assert.Empty(t, part.Text, "Tool response %d should NOT have text part", i+1)
					require.NotNil(t, part.FunctionResponse, "Tool response %d must have functionResponse", i+1)
				}
			},
		},
		{
			name: "MixedMessages_ToolResponsesFollowedByUser_ProperGrouping",
			input: &schemas.BifrostChatRequest{
				Model: "gemini-2.0-flash",
				Input: []schemas.ChatMessage{
					{
						Role: schemas.ChatMessageRoleUser,
						Content: &schemas.ChatMessageContent{
							ContentStr: schemas.Ptr("First question"),
						},
					},
					{
						Role: schemas.ChatMessageRoleAssistant,
						ChatAssistantMessage: &schemas.ChatAssistantMessage{
							ToolCalls: []schemas.ChatAssistantMessageToolCall{
								{ID: schemas.Ptr("call_1"), Type: schemas.Ptr("function"), Function: schemas.ChatAssistantMessageToolCallFunction{Name: schemas.Ptr("tool1"), Arguments: `{}`}},
								{ID: schemas.Ptr("call_2"), Type: schemas.Ptr("function"), Function: schemas.ChatAssistantMessageToolCallFunction{Name: schemas.Ptr("tool2"), Arguments: `{}`}},
							},
						},
					},
					{
						Role:            schemas.ChatMessageRoleTool,
						ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: schemas.Ptr("call_1")},
						Content:         &schemas.ChatMessageContent{ContentStr: schemas.Ptr(`{"result":"1"}`)},
					},
					{
						Role:            schemas.ChatMessageRoleTool,
						ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: schemas.Ptr("call_2")},
						Content:         &schemas.ChatMessageContent{ContentStr: schemas.Ptr(`{"result":"2"}`)},
					},
					{
						Role: schemas.ChatMessageRoleUser,
						Content: &schemas.ChatMessageContent{
							ContentStr: schemas.Ptr("Follow up question"),
						},
					},
				},
			},
			validate: func(t *testing.T, result *gemini.GeminiGenerationRequest) {
				require.Len(t, result.Contents, 4, "Should have 4 Contents: user, assistant with tool calls, grouped tool responses, user")

				// First user message
				assert.Equal(t, "user", result.Contents[0].Role)

				// Assistant with tool calls
				assert.Equal(t, "model", result.Contents[1].Role)

				// Grouped tool responses
				toolContent := result.Contents[2]
				assert.Equal(t, "model", toolContent.Role)
				require.Len(t, toolContent.Parts, 2, "Tool responses should be grouped")
				for _, part := range toolContent.Parts {
					assert.NotNil(t, part.FunctionResponse)
					assert.Empty(t, part.Text)
				}

				// Second user message (should trigger flushing of tool responses)
				assert.Equal(t, "user", result.Contents[3].Role)
			},
		},
		{
			name: "ToolResponsesAtEnd_ProperlyFlushed",
			input: &schemas.BifrostChatRequest{
				Model: "gemini-2.0-flash",
				Input: []schemas.ChatMessage{
					{
						Role: schemas.ChatMessageRoleUser,
						Content: &schemas.ChatMessageContent{
							ContentStr: schemas.Ptr("Question"),
						},
					},
					{
						Role: schemas.ChatMessageRoleAssistant,
						ChatAssistantMessage: &schemas.ChatAssistantMessage{
							ToolCalls: []schemas.ChatAssistantMessageToolCall{
								{ID: schemas.Ptr("call_1"), Type: schemas.Ptr("function"), Function: schemas.ChatAssistantMessageToolCallFunction{Name: schemas.Ptr("tool1"), Arguments: `{}`}},
								{ID: schemas.Ptr("call_2"), Type: schemas.Ptr("function"), Function: schemas.ChatAssistantMessageToolCallFunction{Name: schemas.Ptr("tool2"), Arguments: `{}`}},
							},
						},
					},
					{
						Role:            schemas.ChatMessageRoleTool,
						ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: schemas.Ptr("call_1")},
						Content:         &schemas.ChatMessageContent{ContentStr: schemas.Ptr(`{"result":"1"}`)},
					},
					{
						Role:            schemas.ChatMessageRoleTool,
						ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: schemas.Ptr("call_2")},
						Content:         &schemas.ChatMessageContent{ContentStr: schemas.Ptr(`{"result":"2"}`)},
					},
					// No message after tool responses - they're at the end
				},
			},
			validate: func(t *testing.T, result *gemini.GeminiGenerationRequest) {
				require.Len(t, result.Contents, 3, "Should have 3 Contents: user, assistant with tool calls, grouped tool responses")

				// Grouped tool responses at the end should still be flushed
				toolContent := result.Contents[2]
				assert.Equal(t, "model", toolContent.Role)
				require.Len(t, toolContent.Parts, 2, "Tool responses at end should be grouped and flushed")
				for _, part := range toolContent.Parts {
					assert.NotNil(t, part.FunctionResponse)
					assert.Empty(t, part.Text)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := gemini.ToGeminiChatCompletionRequest(tt.input)
			require.NoError(t, err)
			require.NotNil(t, result, "Conversion should not return nil")
			tt.validate(t, result)
		})
	}
}

// TestResponsesAPIParallelFunctionCalling tests parallel function calling for Responses API
func TestResponsesAPIParallelFunctionCalling(t *testing.T) {
	tests := []struct {
		name     string
		input    *schemas.BifrostResponsesRequest
		validate func(t *testing.T, result *gemini.GeminiGenerationRequest)
	}{
		{
			name: "ResponsesAPI_ParallelFunctionCalling_TwoOutputs_Grouped",
			input: &schemas.BifrostResponsesRequest{
				Provider: schemas.Gemini,
				Model:    "gemini-2.0-flash",
				Input: []schemas.ResponsesMessage{
					{
						Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
						Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
						Content: &schemas.ResponsesMessageContent{
							ContentStr: schemas.Ptr("What's the weather and time?"),
						},
					},
					{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
						ResponsesToolMessage: &schemas.ResponsesToolMessage{
							CallID:    schemas.Ptr("call_1"),
							Name:      schemas.Ptr("get_weather"),
							Arguments: schemas.Ptr(`{"location":"Tokyo"}`),
						},
					},
					{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
						ResponsesToolMessage: &schemas.ResponsesToolMessage{
							CallID:    schemas.Ptr("call_2"),
							Name:      schemas.Ptr("get_time"),
							Arguments: schemas.Ptr(`{"timezone":"Asia/Tokyo"}`),
						},
					},
					{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
						ResponsesToolMessage: &schemas.ResponsesToolMessage{
							CallID: schemas.Ptr("call_1"),
							Name:   schemas.Ptr("get_weather"),
							Output: &schemas.ResponsesToolMessageOutputStruct{
								ResponsesToolCallOutputStr: schemas.Ptr(`{"temperature":22,"condition":"sunny"}`),
							},
						},
					},
					{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
						ResponsesToolMessage: &schemas.ResponsesToolMessage{
							CallID: schemas.Ptr("call_2"),
							Name:   schemas.Ptr("get_time"),
							Output: &schemas.ResponsesToolMessageOutputStruct{
								ResponsesToolCallOutputStr: schemas.Ptr(`{"time":"10:30 AM"}`),
							},
						},
					},
				},
			},
			validate: func(t *testing.T, result *gemini.GeminiGenerationRequest) {
				require.NotNil(t, result)

				// Find the Content with function responses
				var toolResponseContent *gemini.Content
				for i := range result.Contents {
					content := &result.Contents[i]
					if len(content.Parts) > 0 && content.Parts[0].FunctionResponse != nil {
						toolResponseContent = content
						break
					}
				}

				require.NotNil(t, toolResponseContent, "Should have a Content with function responses")
				assert.Equal(t, "model", toolResponseContent.Role, "Function responses use 'model' role")
				require.Len(t, toolResponseContent.Parts, 2, "Should have exactly 2 parts for 2 function outputs (parallel calling)")

				// Verify first function response - ONLY functionResponse
				part1 := toolResponseContent.Parts[0]
				assert.Empty(t, part1.Text, "Function response 1 should NOT have text part")
				require.NotNil(t, part1.FunctionResponse, "Function response 1 must have functionResponse")
				assert.Equal(t, "call_1", part1.FunctionResponse.ID)
				assert.Equal(t, "get_weather", part1.FunctionResponse.Name)

				// Verify second function response - ONLY functionResponse
				part2 := toolResponseContent.Parts[1]
				assert.Empty(t, part2.Text, "Function response 2 should NOT have text part")
				require.NotNil(t, part2.FunctionResponse, "Function response 2 must have functionResponse")
				assert.Equal(t, "call_2", part2.FunctionResponse.ID)
				assert.Equal(t, "get_time", part2.FunctionResponse.Name)
			},
		},
		{
			name: "ResponsesAPI_SingleFunctionOutput_NotGrouped",
			input: &schemas.BifrostResponsesRequest{
				Provider: schemas.Gemini,
				Model:    "gemini-2.0-flash",
				Input: []schemas.ResponsesMessage{
					{
						Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
						Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
						Content: &schemas.ResponsesMessageContent{
							ContentStr: schemas.Ptr("What's the weather?"),
						},
					},
					{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
						ResponsesToolMessage: &schemas.ResponsesToolMessage{
							CallID:    schemas.Ptr("call_1"),
							Name:      schemas.Ptr("get_weather"),
							Arguments: schemas.Ptr(`{}`),
						},
					},
					{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
						ResponsesToolMessage: &schemas.ResponsesToolMessage{
							CallID: schemas.Ptr("call_1"),
							Name:   schemas.Ptr("get_weather"),
							Output: &schemas.ResponsesToolMessageOutputStruct{
								ResponsesToolCallOutputStr: schemas.Ptr(`{"temperature":22}`),
							},
						},
					},
				},
			},
			validate: func(t *testing.T, result *gemini.GeminiGenerationRequest) {
				// Find the Content with function response
				var toolResponseContent *gemini.Content
				for i := range result.Contents {
					content := &result.Contents[i]
					if len(content.Parts) > 0 && content.Parts[0].FunctionResponse != nil {
						toolResponseContent = content
						break
					}
				}

				require.NotNil(t, toolResponseContent)
				assert.Equal(t, "model", toolResponseContent.Role)
				require.Len(t, toolResponseContent.Parts, 1, "Single function output should have 1 part")

				// Verify ONLY functionResponse part (no text/content)
				part := toolResponseContent.Parts[0]
				assert.Empty(t, part.Text, "Function response should NOT have text part")
				require.NotNil(t, part.FunctionResponse, "Function response must have functionResponse")
			},
		},
		{
			name: "ResponsesAPI_MixedMessages_ProperGrouping",
			input: &schemas.BifrostResponsesRequest{
				Provider: schemas.Gemini,
				Model:    "gemini-2.0-flash",
				Input: []schemas.ResponsesMessage{
					{
						Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
						Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
						Content: &schemas.ResponsesMessageContent{
							ContentStr: schemas.Ptr("First question"),
						},
					},
					{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
						ResponsesToolMessage: &schemas.ResponsesToolMessage{
							CallID:    schemas.Ptr("call_1"),
							Name:      schemas.Ptr("tool1"),
							Arguments: schemas.Ptr(`{}`),
						},
					},
					{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
						ResponsesToolMessage: &schemas.ResponsesToolMessage{
							CallID:    schemas.Ptr("call_2"),
							Name:      schemas.Ptr("tool2"),
							Arguments: schemas.Ptr(`{}`),
						},
					},
					{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
						ResponsesToolMessage: &schemas.ResponsesToolMessage{
							CallID: schemas.Ptr("call_1"),
							Output: &schemas.ResponsesToolMessageOutputStruct{
								ResponsesToolCallOutputStr: schemas.Ptr(`{"result":"1"}`),
							},
						},
					},
					{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
						ResponsesToolMessage: &schemas.ResponsesToolMessage{
							CallID: schemas.Ptr("call_2"),
							Output: &schemas.ResponsesToolMessageOutputStruct{
								ResponsesToolCallOutputStr: schemas.Ptr(`{"result":"2"}`),
							},
						},
					},
					{
						Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
						Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
						Content: &schemas.ResponsesMessageContent{
							ContentStr: schemas.Ptr("Follow up question"),
						},
					},
				},
			},
			validate: func(t *testing.T, result *gemini.GeminiGenerationRequest) {
				// Find grouped function responses
				var groupedToolContent *gemini.Content
				for i := range result.Contents {
					content := &result.Contents[i]
					if len(content.Parts) >= 2 && content.Parts[0].FunctionResponse != nil {
						groupedToolContent = content
						break
					}
				}

				require.NotNil(t, groupedToolContent, "Should have grouped function responses")
				assert.Equal(t, "model", groupedToolContent.Role)
				require.Len(t, groupedToolContent.Parts, 2, "Function outputs should be grouped before user message")

				// Verify both are functionResponse only
				for _, part := range groupedToolContent.Parts {
					assert.Empty(t, part.Text)
					assert.NotNil(t, part.FunctionResponse)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := gemini.ToGeminiResponsesRequest(tt.input)
			require.NoError(t, err)
			require.NotNil(t, result, "Responses API conversion should not return nil")
			tt.validate(t, result)
		})
	}
}

// TestBifrostResponsesToGeminiToolConversion tests the conversion of tools from Bifrost Responses API to Gemini format
func TestBifrostResponsesToGeminiToolConversion(t *testing.T) {
	tests := []struct {
		name     string
		input    *schemas.BifrostResponsesRequest
		validate func(t *testing.T, result *gemini.GeminiGenerationRequest)
	}{
		{
			name: "ResponsesAPI_ArrayWithItems",
			input: &schemas.BifrostResponsesRequest{
				Provider: schemas.Gemini,
				Model:    "gemini-2.0-flash",
				Input: []schemas.ResponsesMessage{
					{
						Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
						Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
						Content: &schemas.ResponsesMessageContent{
							ContentStr: schemas.Ptr("Test array items"),
						},
					},
				},
				Params: &schemas.ResponsesParameters{
					Tools: []schemas.ResponsesTool{
						{
							Type:        schemas.ResponsesToolTypeFunction,
							Name:        schemas.Ptr("filter_data"),
							Description: schemas.Ptr("Filter data with criteria"),
							ResponsesToolFunction: &schemas.ResponsesToolFunction{
								Parameters: &schemas.ToolFunctionParameters{
									Type: "object",
									Properties: schemas.NewOrderedMapFromPairs(
										schemas.KV("filters", map[string]interface{}{
											"type":        "array",
											"description": "List of filters",
											"items": map[string]interface{}{
												"type":        "string",
												"description": "Filter criterion",
											},
										}),
										schemas.KV("sort_order", map[string]interface{}{
											"type": "string",
											"enum": []interface{}{"asc", "desc"},
										}),
									),
									Required: []string{"filters"},
								},
							},
						},
					},
				},
			},
			validate: func(t *testing.T, result *gemini.GeminiGenerationRequest) {
				require.Len(t, result.Tools, 1)
				fd := result.Tools[0].FunctionDeclarations[0]

				assert.Equal(t, "filter_data", fd.Name)
				assert.Equal(t, "Filter data with criteria", fd.Description)

				// Array with items - critical test
				filtersProp := fd.Parameters.Properties["filters"]
				assert.Equal(t, gemini.Type("array"), filtersProp.Type)
				require.NotNil(t, filtersProp.Items, "items field must be present in Responses API conversion")
				assert.Equal(t, gemini.Type("string"), filtersProp.Items.Type)
				assert.Equal(t, "Filter criterion", filtersProp.Items.Description)

				// Enum validation
				sortProp := fd.Parameters.Properties["sort_order"]
				assert.Equal(t, []string{"asc", "desc"}, sortProp.Enum)
			},
		},
		{
			name: "ResponsesAPI_ComplexNestedArrayOfObjects",
			input: &schemas.BifrostResponsesRequest{
				Provider: schemas.Gemini,
				Model:    "gemini-2.0-flash",
				Input: []schemas.ResponsesMessage{
					{
						Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
						Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
						Content: &schemas.ResponsesMessageContent{
							ContentStr: schemas.Ptr("Complex test"),
						},
					},
				},
				Params: &schemas.ResponsesParameters{
					Tools: []schemas.ResponsesTool{
						{
							Type:        schemas.ResponsesToolTypeFunction,
							Name:        schemas.Ptr("batch_update"),
							Description: schemas.Ptr("Update multiple records"),
							ResponsesToolFunction: &schemas.ResponsesToolFunction{
								Parameters: &schemas.ToolFunctionParameters{
									Type: "object",
									Properties: schemas.NewOrderedMapFromPairs(
										schemas.KV("updates", map[string]interface{}{
											"type": "array",
											"items": map[string]interface{}{
												"type": "object",
												"properties": map[string]interface{}{
													"id": map[string]interface{}{
														"type": "string",
													},
													"fields": map[string]interface{}{
														"type": "object",
														"properties": map[string]interface{}{
															"name": map[string]interface{}{
																"type": "string",
															},
															"status": map[string]interface{}{
																"type": "string",
																"enum": []string{"active", "inactive"},
															},
														},
													},
												},
												"required": []string{"id", "fields"},
											},
										}),
									),
									Required: []string{"updates"},
								},
							},
						},
					},
				},
			},
			validate: func(t *testing.T, result *gemini.GeminiGenerationRequest) {
				require.Len(t, result.Tools, 1)
				fd := result.Tools[0].FunctionDeclarations[0]

				updatesProp := fd.Parameters.Properties["updates"]
				assert.Equal(t, gemini.Type("array"), updatesProp.Type)

				// Nested object in array items
				require.NotNil(t, updatesProp.Items)
				assert.Equal(t, gemini.Type("object"), updatesProp.Items.Type)
				assert.Contains(t, updatesProp.Items.Properties, "id")
				assert.Contains(t, updatesProp.Items.Properties, "fields")
				assert.Equal(t, []string{"id", "fields"}, updatesProp.Items.Required)

				// Deeply nested object
				fieldsProp := updatesProp.Items.Properties["fields"]
				assert.Equal(t, gemini.Type("object"), fieldsProp.Type)
				assert.Contains(t, fieldsProp.Properties, "name")
				assert.Contains(t, fieldsProp.Properties, "status")

				// Nested enum
				statusProp := fieldsProp.Properties["status"]
				assert.Equal(t, []string{"active", "inactive"}, statusProp.Enum)
			},
		},
		{
			name: "ResponsesAPI_EmptyItems",
			input: &schemas.BifrostResponsesRequest{
				Provider: schemas.Gemini,
				Model:    "gemini-2.0-flash",
				Input: []schemas.ResponsesMessage{
					{
						Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
						Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
						Content: &schemas.ResponsesMessageContent{
							ContentStr: schemas.Ptr("Edge case"),
						},
					},
				},
				Params: &schemas.ResponsesParameters{
					Tools: []schemas.ResponsesTool{
						{
							Type: schemas.ResponsesToolTypeFunction,
							Name: schemas.Ptr("edge_case_tool"),
							ResponsesToolFunction: &schemas.ResponsesToolFunction{
								Parameters: &schemas.ToolFunctionParameters{
									Type: "object",
									Properties: schemas.NewOrderedMapFromPairs(
										schemas.KV("any_array", map[string]interface{}{
											"type":  "array",
											"items": map[string]interface{}{}, // Empty items
										}),
									),
								},
							},
						},
					},
				},
			},
			validate: func(t *testing.T, result *gemini.GeminiGenerationRequest) {
				fd := result.Tools[0].FunctionDeclarations[0]
				arrayProp := fd.Parameters.Properties["any_array"]

				// Empty items should still be converted
				assert.NotNil(t, arrayProp.Items, "empty items must be present in Responses API")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := gemini.ToGeminiResponsesRequest(tt.input)
			require.NoError(t, err)
			require.NotNil(t, result, "Responses API conversion should not return nil")
			tt.validate(t, result)
		})
	}
}

func TestConvertGeminiUsageMetadataToChatUsage(t *testing.T) {
	tests := []struct {
		name     string
		metadata *gemini.GenerateContentResponseUsageMetadata
		expected *schemas.BifrostLLMUsage
	}{
		{
			name: "CompleteModalityBreakdown",
			metadata: &gemini.GenerateContentResponseUsageMetadata{
				PromptTokenCount:     6,
				CandidatesTokenCount: 42,
				TotalTokenCount:      48,
				PromptTokensDetails: []*gemini.ModalityTokenCount{
					{Modality: gemini.ModalityText, TokenCount: 6},
					{Modality: gemini.ModalityAudio, TokenCount: 0},
					{Modality: gemini.ModalityImage, TokenCount: 0},
				},
				CandidatesTokensDetails: []*gemini.ModalityTokenCount{
					{Modality: gemini.ModalityText, TokenCount: 1},
				},
				ThoughtsTokenCount: 41,
			},
			expected: &schemas.BifrostLLMUsage{
				PromptTokens:     6,
				CompletionTokens: 83,
				TotalTokens:      48,
				PromptTokensDetails: &schemas.ChatPromptTokensDetails{
					TextTokens:  6,
					AudioTokens: 0,
					ImageTokens: 0,
				},
				CompletionTokensDetails: &schemas.ChatCompletionTokensDetails{
					TextTokens:      1,
					ReasoningTokens: 41,
				},
			},
		},
		{
			name: "MultimodalInputWithCache",
			metadata: &gemini.GenerateContentResponseUsageMetadata{
				PromptTokenCount:        150,
				CandidatesTokenCount:    50,
				TotalTokenCount:         200,
				CachedContentTokenCount: 100,
				PromptTokensDetails: []*gemini.ModalityTokenCount{
					{Modality: gemini.ModalityText, TokenCount: 50},
					{Modality: gemini.ModalityImage, TokenCount: 100},
				},
				CandidatesTokensDetails: []*gemini.ModalityTokenCount{
					{Modality: gemini.ModalityText, TokenCount: 50},
				},
			},
			expected: &schemas.BifrostLLMUsage{
				PromptTokens:     150,
				CompletionTokens: 50,
				TotalTokens:      200,
				PromptTokensDetails: &schemas.ChatPromptTokensDetails{
					TextTokens:       50,
					ImageTokens:      100,
					CachedReadTokens: 100,
				},
				CompletionTokensDetails: &schemas.ChatCompletionTokensDetails{
					TextTokens: 50,
				},
			},
		},
		{
			name: "AudioOutputGeneration",
			metadata: &gemini.GenerateContentResponseUsageMetadata{
				PromptTokenCount:     20,
				CandidatesTokenCount: 80,
				TotalTokenCount:      100,
				PromptTokensDetails: []*gemini.ModalityTokenCount{
					{Modality: gemini.ModalityText, TokenCount: 20},
				},
				CandidatesTokensDetails: []*gemini.ModalityTokenCount{
					{Modality: gemini.ModalityAudio, TokenCount: 80},
				},
			},
			expected: &schemas.BifrostLLMUsage{
				PromptTokens:     20,
				CompletionTokens: 80,
				TotalTokens:      100,
				PromptTokensDetails: &schemas.ChatPromptTokensDetails{
					TextTokens: 20,
				},
				CompletionTokensDetails: &schemas.ChatCompletionTokensDetails{
					AudioTokens: 80,
				},
			},
		},
		{
			name: "ImageOutputGeneration",
			metadata: &gemini.GenerateContentResponseUsageMetadata{
				PromptTokenCount:     30,
				CandidatesTokenCount: 120,
				TotalTokenCount:      150,
				PromptTokensDetails: []*gemini.ModalityTokenCount{
					{Modality: gemini.ModalityText, TokenCount: 30},
				},
				CandidatesTokensDetails: []*gemini.ModalityTokenCount{
					{Modality: gemini.ModalityImage, TokenCount: 120},
				},
			},
			expected: &schemas.BifrostLLMUsage{
				PromptTokens:     30,
				CompletionTokens: 120,
				TotalTokens:      150,
				PromptTokensDetails: &schemas.ChatPromptTokensDetails{
					TextTokens: 30,
				},
				CompletionTokensDetails: &schemas.ChatCompletionTokensDetails{
					ImageTokens: func() *int { v := 120; return &v }(),
				},
			},
		},
		{
			name: "BasicUsageNoDetails",
			metadata: &gemini.GenerateContentResponseUsageMetadata{
				PromptTokenCount:     10,
				CandidatesTokenCount: 20,
				TotalTokenCount:      30,
			},
			expected: &schemas.BifrostLLMUsage{
				PromptTokens:     10,
				CompletionTokens: 20,
				TotalTokens:      30,
			},
		},
		{
			name:     "NilMetadata",
			metadata: nil,
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := gemini.ConvertGeminiUsageMetadataToChatUsage(tt.metadata)
			if tt.expected == nil {
				assert.Nil(t, result)
				return
			}

			require.NotNil(t, result)
			assert.Equal(t, tt.expected.PromptTokens, result.PromptTokens)
			assert.Equal(t, tt.expected.CompletionTokens, result.CompletionTokens)
			assert.Equal(t, tt.expected.TotalTokens, result.TotalTokens)

			// Check prompt token details
			if tt.expected.PromptTokensDetails != nil {
				require.NotNil(t, result.PromptTokensDetails)
				assert.Equal(t, tt.expected.PromptTokensDetails.TextTokens, result.PromptTokensDetails.TextTokens)
				assert.Equal(t, tt.expected.PromptTokensDetails.AudioTokens, result.PromptTokensDetails.AudioTokens)
				assert.Equal(t, tt.expected.PromptTokensDetails.ImageTokens, result.PromptTokensDetails.ImageTokens)
				assert.Equal(t, tt.expected.PromptTokensDetails.CachedReadTokens, result.PromptTokensDetails.CachedReadTokens)
			} else {
				assert.Nil(t, result.PromptTokensDetails)
			}

			// Check completion token details
			if tt.expected.CompletionTokensDetails != nil {
				require.NotNil(t, result.CompletionTokensDetails)
				assert.Equal(t, tt.expected.CompletionTokensDetails.TextTokens, result.CompletionTokensDetails.TextTokens)
				assert.Equal(t, tt.expected.CompletionTokensDetails.AudioTokens, result.CompletionTokensDetails.AudioTokens)
				assert.Equal(t, tt.expected.CompletionTokensDetails.ReasoningTokens, result.CompletionTokensDetails.ReasoningTokens)

				if tt.expected.CompletionTokensDetails.ImageTokens != nil {
					require.NotNil(t, result.CompletionTokensDetails.ImageTokens)
					assert.Equal(t, *tt.expected.CompletionTokensDetails.ImageTokens, *result.CompletionTokensDetails.ImageTokens)
				} else {
					assert.Nil(t, result.CompletionTokensDetails.ImageTokens)
				}
			} else {
				assert.Nil(t, result.CompletionTokensDetails)
			}
		})
	}
}

// TestConvertGeminiUsageMetadataToResponsesUsage tests the conversion of Gemini usage metadata to Bifrost responses usage
func TestConvertGeminiUsageMetadataToResponsesUsage(t *testing.T) {
	tests := []struct {
		name     string
		metadata *gemini.GenerateContentResponseUsageMetadata
		expected *schemas.ResponsesResponseUsage
	}{
		{
			name: "CompleteModalityBreakdown",
			metadata: &gemini.GenerateContentResponseUsageMetadata{
				PromptTokenCount:     100,
				CandidatesTokenCount: 50,
				TotalTokenCount:      150,
				PromptTokensDetails: []*gemini.ModalityTokenCount{
					{Modality: gemini.ModalityText, TokenCount: 60},
					{Modality: gemini.ModalityAudio, TokenCount: 20},
					{Modality: gemini.ModalityImage, TokenCount: 20},
				},
				CandidatesTokensDetails: []*gemini.ModalityTokenCount{
					{Modality: gemini.ModalityText, TokenCount: 40},
					{Modality: gemini.ModalityAudio, TokenCount: 10},
				},
				ThoughtsTokenCount: 5,
			},
			expected: &schemas.ResponsesResponseUsage{
				TotalTokens:  150,
				InputTokens:  100,
				OutputTokens: 55,
				InputTokensDetails: &schemas.ResponsesResponseInputTokens{
					TextTokens:  60,
					AudioTokens: 20,
					ImageTokens: 20,
				},
				OutputTokensDetails: &schemas.ResponsesResponseOutputTokens{
					TextTokens:      40,
					AudioTokens:     10,
					ReasoningTokens: 5,
				},
			},
		},
		{
			name: "WithCachedTokens",
			metadata: &gemini.GenerateContentResponseUsageMetadata{
				PromptTokenCount:        200,
				CandidatesTokenCount:    100,
				TotalTokenCount:         300,
				CachedContentTokenCount: 150,
				PromptTokensDetails: []*gemini.ModalityTokenCount{
					{Modality: gemini.ModalityText, TokenCount: 200},
				},
				CandidatesTokensDetails: []*gemini.ModalityTokenCount{
					{Modality: gemini.ModalityText, TokenCount: 100},
				},
			},
			expected: &schemas.ResponsesResponseUsage{
				TotalTokens:  300,
				InputTokens:  200,
				OutputTokens: 100,
				InputTokensDetails: &schemas.ResponsesResponseInputTokens{
					TextTokens:       200,
					CachedReadTokens: 150,
				},
				OutputTokensDetails: &schemas.ResponsesResponseOutputTokens{
					TextTokens: 100,
				},
			},
		},
		{
			name: "AudioOnlyOutput",
			metadata: &gemini.GenerateContentResponseUsageMetadata{
				PromptTokenCount:     50,
				CandidatesTokenCount: 200,
				TotalTokenCount:      250,
				PromptTokensDetails: []*gemini.ModalityTokenCount{
					{Modality: gemini.ModalityText, TokenCount: 50},
				},
				CandidatesTokensDetails: []*gemini.ModalityTokenCount{
					{Modality: gemini.ModalityAudio, TokenCount: 200},
				},
			},
			expected: &schemas.ResponsesResponseUsage{
				TotalTokens:  250,
				InputTokens:  50,
				OutputTokens: 200,
				InputTokensDetails: &schemas.ResponsesResponseInputTokens{
					TextTokens: 50,
				},
				OutputTokensDetails: &schemas.ResponsesResponseOutputTokens{
					AudioTokens: 200,
				},
			},
		},
		{
			name: "BasicUsageNoDetails",
			metadata: &gemini.GenerateContentResponseUsageMetadata{
				PromptTokenCount:     10,
				CandidatesTokenCount: 20,
				TotalTokenCount:      30,
			},
			expected: &schemas.ResponsesResponseUsage{
				TotalTokens:         30,
				InputTokens:         10,
				OutputTokens:        20,
				InputTokensDetails:  &schemas.ResponsesResponseInputTokens{},
				OutputTokensDetails: &schemas.ResponsesResponseOutputTokens{},
			},
		},
		{
			name:     "NilMetadata",
			metadata: nil,
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := gemini.ConvertGeminiUsageMetadataToResponsesUsage(tt.metadata)
			if tt.expected == nil {
				assert.Nil(t, result)
				return
			}

			require.NotNil(t, result)
			assert.Equal(t, tt.expected.TotalTokens, result.TotalTokens)
			assert.Equal(t, tt.expected.InputTokens, result.InputTokens)
			assert.Equal(t, tt.expected.OutputTokens, result.OutputTokens)

			// Check input token details
			if tt.expected.InputTokensDetails != nil {
				require.NotNil(t, result.InputTokensDetails)
				assert.Equal(t, tt.expected.InputTokensDetails.TextTokens, result.InputTokensDetails.TextTokens)
				assert.Equal(t, tt.expected.InputTokensDetails.AudioTokens, result.InputTokensDetails.AudioTokens)
				assert.Equal(t, tt.expected.InputTokensDetails.ImageTokens, result.InputTokensDetails.ImageTokens)
				assert.Equal(t, tt.expected.InputTokensDetails.CachedReadTokens, result.InputTokensDetails.CachedReadTokens)
			}

			// Check output token details
			if tt.expected.OutputTokensDetails != nil {
				require.NotNil(t, result.OutputTokensDetails)
				assert.Equal(t, tt.expected.OutputTokensDetails.TextTokens, result.OutputTokensDetails.TextTokens)
				assert.Equal(t, tt.expected.OutputTokensDetails.AudioTokens, result.OutputTokensDetails.AudioTokens)
				assert.Equal(t, tt.expected.OutputTokensDetails.ReasoningTokens, result.OutputTokensDetails.ReasoningTokens)
			}
		})
	}
}

// TestGeminiToolInputKeyOrderPreservation verifies that multiple parallel tool calls
// preserve the client's original key ordering after conversion to Gemini format.
func TestGeminiToolInputKeyOrderPreservation(t *testing.T) {
	bifrostReq := &schemas.BifrostChatRequest{
		Provider: schemas.Gemini,
		Model:    "gemini-2.0-flash",
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
								Arguments: `{"command":"git diff main...HEAD --stat","description":"Show diff"}`,
							},
						},
					},
				},
			},
		},
	}

	result, err := gemini.ToGeminiChatCompletionRequest(bifrostReq)
	require.NoError(t, err)
	require.NotNil(t, result)

	// Collect all FunctionCall parts
	var argsList []json.RawMessage
	for _, content := range result.Contents {
		for _, part := range content.Parts {
			if part.FunctionCall != nil {
				argsList = append(argsList, part.FunctionCall.Args)
			}
		}
	}

	require.Len(t, argsList, 2, "expected 2 FunctionCall parts")

	// Block 0: keys should be description, timeout, command (NOT alphabetical)
	s0 := string(argsList[0])
	descIdx := strings.Index(s0, `"description"`)
	timeoutIdx := strings.Index(s0, `"timeout"`)
	commandIdx := strings.Index(s0, `"command"`)
	require.NotEqual(t, -1, descIdx, "block 0: missing description key in: %s", s0)
	require.NotEqual(t, -1, timeoutIdx, "block 0: missing timeout key in: %s", s0)
	require.NotEqual(t, -1, commandIdx, "block 0: missing command key in: %s", s0)
	assert.True(t, descIdx < timeoutIdx && timeoutIdx < commandIdx,
		"block 0: key order not preserved, expected description < timeout < command in: %s", s0)

	// Block 1: keys should be command, description (NOT alphabetical)
	s1 := string(argsList[1])
	commandIdx = strings.Index(s1, `"command"`)
	descIdx = strings.Index(s1, `"description"`)
	require.NotEqual(t, -1, commandIdx, "block 1: missing command key in: %s", s1)
	require.NotEqual(t, -1, descIdx, "block 1: missing description key in: %s", s1)
	assert.True(t, commandIdx < descIdx,
		"block 1: key order not preserved, expected command < description in: %s", s1)
}

// minimalChatInput returns a single user message for use in budget tests.
func minimalChatInput() []schemas.ChatMessage {
	return []schemas.ChatMessage{
		{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Hello")},
		},
	}
}

// TestThinkingBudgetValidation_Chat verifies that ToGeminiChatCompletionRequest
// enforces per-model thinking budget bounds when max_tokens is set explicitly.
func TestThinkingBudgetValidation_Chat(t *testing.T) {
	tests := []struct {
		name         string
		model        string
		budget       int
		wantErr      bool
		wantDisabled bool   // budget=0: expect IncludeThoughts=false
		wantDynamic  bool   // budget=-1: expect ThinkingBudget=-1
		wantBudget   *int32 // expected ThinkingBudget value when no error
	}{
		// gemini-2.5-pro: valid range [128, 32768]
		{
			name:       "pro_valid_budget",
			model:      "gemini-2.5-pro",
			budget:     1000,
			wantBudget: func() *int32 { v := int32(1000); return &v }(),
		},
		{
			name:       "pro_at_minimum",
			model:      "gemini-2.5-pro",
			budget:     128,
			wantBudget: func() *int32 { v := int32(128); return &v }(),
		},
		{
			name:       "pro_at_maximum",
			model:      "gemini-2.5-pro",
			budget:     32768,
			wantBudget: func() *int32 { v := int32(32768); return &v }(),
		},
		{
			name:    "pro_below_minimum",
			model:   "gemini-2.5-pro",
			budget:  50,
			wantErr: true,
		},
		{
			name:    "pro_above_maximum",
			model:   "gemini-2.5-pro",
			budget:  40000,
			wantErr: true,
		},

		// gemini-2.5-flash: valid range [0, 24576]
		{
			name:       "flash_valid_budget",
			model:      "gemini-2.5-flash",
			budget:     5000,
			wantBudget: func() *int32 { v := int32(5000); return &v }(),
		},
		{
			name:    "flash_above_maximum",
			model:   "gemini-2.5-flash",
			budget:  30000,
			wantErr: true,
		},
		// budget=300 is valid for flash (min=0) — this is the key disambiguation test:
		// the same budget is rejected for flash-lite (min=512) but accepted for flash.
		{
			name:       "flash_budget_300_valid",
			model:      "gemini-2.5-flash",
			budget:     300,
			wantBudget: func() *int32 { v := int32(300); return &v }(),
		},

		// gemini-2.5-flash-lite: valid range [512, 24576]
		// budget=300 must be rejected here even though it passes for flash.
		{
			name:    "flash_lite_below_minimum",
			model:   "gemini-2.5-flash-lite",
			budget:  300,
			wantErr: true,
		},
		{
			name:       "flash_lite_valid_budget",
			model:      "gemini-2.5-flash-lite",
			budget:     1000,
			wantBudget: func() *int32 { v := int32(1000); return &v }(),
		},
		{
			name:    "flash_lite_above_maximum",
			model:   "gemini-2.5-flash-lite",
			budget:  30000,
			wantErr: true,
		},

		// Unknown model — no entry in thinkingBudgetRanges, validation is skipped.
		{
			name:       "unknown_model_skips_validation",
			model:      "gemini-2.0-flash-thinking",
			budget:     50, // would be rejected for pro (min=128) but unknown model is not validated
			wantBudget: func() *int32 { v := int32(50); return &v }(),
		},

		// Special values — exempt from range checks on any model.
		{
			name:         "budget_zero_disables_thinking",
			model:        "gemini-2.5-pro",
			budget:       0,
			wantDisabled: true,
		},
		{
			name:        "budget_minus_one_dynamic",
			model:       "gemini-2.5-flash",
			budget:      -1,
			wantDynamic: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &schemas.BifrostChatRequest{
				Model: tt.model,
				Input: minimalChatInput(),
				Params: &schemas.ChatParameters{
					Reasoning: &schemas.ChatReasoning{
						MaxTokens: &tt.budget,
					},
				},
			}

			result, err := gemini.ToGeminiChatCompletionRequest(req)

			if tt.wantErr {
				require.Error(t, err, "expected error for budget %d on model %s", tt.budget, tt.model)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, result)
			require.NotNil(t, result.GenerationConfig.ThinkingConfig, "ThinkingConfig should be set")

			tc := result.GenerationConfig.ThinkingConfig
			switch {
			case tt.wantDisabled:
				assert.False(t, tc.IncludeThoughts, "IncludeThoughts should be false for budget=0")
				require.NotNil(t, tc.ThinkingBudget)
				assert.Equal(t, int32(0), *tc.ThinkingBudget)
			case tt.wantDynamic:
				require.NotNil(t, tc.ThinkingBudget)
				assert.Equal(t, int32(-1), *tc.ThinkingBudget)
			default:
				require.NotNil(t, tc.ThinkingBudget)
				assert.Equal(t, *tt.wantBudget, *tc.ThinkingBudget)
			}
		})
	}
}

// TestThinkingBudgetValidation_Responses verifies the same budget validation
// for the Responses API path (ToGeminiResponsesRequest).
func TestThinkingBudgetValidation_Responses(t *testing.T) {
	tests := []struct {
		name         string
		model        string
		budget       int
		wantErr      bool
		wantDisabled bool
		wantDynamic  bool
		wantBudget   *int32
	}{
		// gemini-2.5-pro
		{
			name:       "pro_valid_budget",
			model:      "gemini-2.5-pro",
			budget:     2000,
			wantBudget: func() *int32 { v := int32(2000); return &v }(),
		},
		{
			name:    "pro_below_minimum",
			model:   "gemini-2.5-pro",
			budget:  100,
			wantErr: true,
		},
		{
			name:    "pro_above_maximum",
			model:   "gemini-2.5-pro",
			budget:  33000,
			wantErr: true,
		},

		// gemini-2.5-flash-lite vs gemini-2.5-flash disambiguation
		{
			name:    "flash_lite_below_minimum",
			model:   "gemini-2.5-flash-lite",
			budget:  300,
			wantErr: true,
		},
		{
			name:       "flash_budget_300_valid",
			model:      "gemini-2.5-flash",
			budget:     300,
			wantBudget: func() *int32 { v := int32(300); return &v }(),
		},

		// Special values
		{
			name:         "budget_zero_disables_thinking",
			model:        "gemini-2.5-flash",
			budget:       0,
			wantDisabled: true,
		},
		{
			name:        "budget_minus_one_dynamic",
			model:       "gemini-2.5-pro",
			budget:      -1,
			wantDynamic: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &schemas.BifrostResponsesRequest{
				Model: tt.model,
				Params: &schemas.ResponsesParameters{
					Reasoning: &schemas.ResponsesParametersReasoning{
						MaxTokens: &tt.budget,
					},
				},
			}

			result, err := gemini.ToGeminiResponsesRequest(req)

			if tt.wantErr {
				require.Error(t, err, "expected error for budget %d on model %s", tt.budget, tt.model)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, result)
			require.NotNil(t, result.GenerationConfig.ThinkingConfig, "ThinkingConfig should be set")

			tc := result.GenerationConfig.ThinkingConfig
			switch {
			case tt.wantDisabled:
				assert.False(t, tc.IncludeThoughts)
				require.NotNil(t, tc.ThinkingBudget)
				assert.Equal(t, int32(0), *tc.ThinkingBudget)
			case tt.wantDynamic:
				require.NotNil(t, tc.ThinkingBudget)
				assert.Equal(t, int32(-1), *tc.ThinkingBudget)
			default:
				require.NotNil(t, tc.ThinkingBudget)
				assert.Equal(t, *tt.wantBudget, *tc.ThinkingBudget)
			}
		})
	}
}

// TestThinkingBudgetEffortUsesModelRange verifies that effort-based budget
// calculation uses the correct model-specific range, not a global default.
// In particular, gemini-2.5-flash-lite (min=512) must not use gemini-2.5-flash's
// range (min=0), which would produce budgets below the model's minimum.
func TestThinkingBudgetEffortUsesModelRange(t *testing.T) {
	effort := "low"

	t.Run("flash_lite_budget_respects_min_512", func(t *testing.T) {
		req := &schemas.BifrostChatRequest{
			Model: "gemini-2.5-flash-lite",
			Input: minimalChatInput(),
			Params: &schemas.ChatParameters{
				Reasoning: &schemas.ChatReasoning{Effort: &effort},
			},
		}
		result, err := gemini.ToGeminiChatCompletionRequest(req)
		require.NoError(t, err)
		require.NotNil(t, result)
		require.NotNil(t, result.GenerationConfig.ThinkingConfig)
		require.NotNil(t, result.GenerationConfig.ThinkingConfig.ThinkingBudget)
		assert.GreaterOrEqual(t, *result.GenerationConfig.ThinkingConfig.ThinkingBudget, int32(512),
			"flash-lite effort budget must be >= model minimum 512")
	})

	t.Run("flash_budget_may_start_from_zero", func(t *testing.T) {
		req := &schemas.BifrostChatRequest{
			Model: "gemini-2.5-flash",
			Input: minimalChatInput(),
			Params: &schemas.ChatParameters{
				Reasoning: &schemas.ChatReasoning{Effort: &effort},
			},
		}
		result, err := gemini.ToGeminiChatCompletionRequest(req)
		require.NoError(t, err)
		require.NotNil(t, result)
		require.NotNil(t, result.GenerationConfig.ThinkingConfig)
		require.NotNil(t, result.GenerationConfig.ThinkingConfig.ThinkingBudget)
		assert.GreaterOrEqual(t, *result.GenerationConfig.ThinkingConfig.ThinkingBudget, int32(0))
		assert.LessOrEqual(t, *result.GenerationConfig.ThinkingConfig.ThinkingBudget, int32(24576),
			"flash effort budget must not exceed model maximum 24576")
	})
}
