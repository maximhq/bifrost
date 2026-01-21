package gemini_test

import (
	"os"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/internal/testutil"
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

	client, ctx, cancel, err := testutil.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()

	testConfig := testutil.ComprehensiveTestConfig{
		Provider:  schemas.Gemini,
		ChatModel: "gemini-2.0-flash",
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.Gemini, Model: "gemini-2.5-flash"},
		},
		VisionModel:          "gemini-2.5-flash",
		EmbeddingModel:       "text-embedding-004",
		TranscriptionModel:   "gemini-2.5-flash",
		SpeechSynthesisModel: "gemini-2.5-flash-preview-tts",
		ImageGenerationModel: "gemini-2.5-flash-image",
		SpeechSynthesisFallbacks: []schemas.Fallback{
			{Provider: schemas.Gemini, Model: "gemini-2.5-pro-preview-tts"},
		},
		ReasoningModel: "gemini-3-pro-preview",
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
			WebSearchTool:         true,
			ImageURL:              false,
			ImageBase64:           true,
			MultipleImages:        false,
			ImageGeneration:       true,
			ImageGenerationStream: false,
			FileBase64:            true,
			FileURL:               false, // supported files via gemini files api
			CompleteEnd2End:       true,
			Embedding:             true,
			Transcription:         false,
			TranscriptionStream:   false,
			SpeechSynthesis:       true,
			SpeechSynthesisStream: true,
			Reasoning:             true,
			ListModels:            true,
			BatchCreate:           true,
			BatchList:             true,
			BatchRetrieve:         true,
			BatchCancel:           true,
			BatchResults:          true,
			FileUpload:            true,
			FileList:              true,
			FileRetrieve:          true,
			FileDelete:            true,
			FileContent:           false,
			FileBatchInput:        true,
			CountTokens:           true,
			StructuredOutputs:     true, // Structured outputs with nullable enum support
		},
	}

	t.Run("GeminiTests", func(t *testing.T) {
		testutil.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
	client.Shutdown()
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
				bifrostResp, _, _ = tt.response.ToBifrostChatCompletionStream()
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
									Properties: &schemas.OrderedMap{
										"query": map[string]interface{}{
											"type":        "string",
											"description": "Search query",
										},
										"category": map[string]interface{}{
											"type":        "string",
											"description": "Product category",
											"enum":        []interface{}{"electronics", "books", "clothing"},
										},
										"tags": map[string]interface{}{
											"type":        "array",
											"description": "Filter tags",
											"items": map[string]interface{}{
												"type":        "string",
												"description": "A tag",
											},
										},
									},
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
									Properties: &schemas.OrderedMap{
										"customer": map[string]interface{}{
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
										},
										"items": map[string]interface{}{
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
										},
									},
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
									Properties: &schemas.OrderedMap{
										"data": map[string]interface{}{
											"type":  "array",
											"items": map[string]interface{}{}, // Empty items object
										},
									},
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
									Properties: &schemas.OrderedMap{
										"username": map[string]interface{}{
											"type":        "string",
											"description": "Username with length constraints",
											"minLength":   float64(3),
											"maxLength":   float64(20),
											"pattern":     "^[a-zA-Z0-9_]+$",
										},
										"age": map[string]interface{}{
											"type":    "integer",
											"minimum": float64(0),
											"maximum": float64(150),
										},
										"tags": map[string]interface{}{
											"type":     "array",
											"minItems": float64(1),
											"maxItems": float64(5),
											"items": map[string]interface{}{
												"type": "string",
											},
										},
									},
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
									Properties: &schemas.OrderedMap{
										"id": map[string]interface{}{
											"anyOf": []interface{}{
												map[string]interface{}{"type": "string"},
												map[string]interface{}{"type": "integer"},
											},
											"description": "ID that can be string or integer",
										},
									},
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
									Items: &schemas.OrderedMap{
										"type":        "string",
										"description": "Item in the list",
									},
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
									Properties: &schemas.OrderedMap{
										"enabled": map[string]interface{}{
											"type":     "boolean",
											"default":  true,
											"nullable": true,
											"title":    "Enabled Flag",
										},
										"format_type": map[string]interface{}{
											"type":   "string",
											"format": "email",
										},
									},
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
			result := gemini.ToGeminiChatCompletionRequest(tt.input)
			require.NotNil(t, result, "Conversion should not return nil")
			tt.validate(t, result)
		})
	}
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
			result := gemini.ToGeminiChatCompletionRequest(tt.input)
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
			result := gemini.ToGeminiResponsesRequest(tt.input)
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
			result := gemini.ToGeminiChatCompletionRequest(tt.input)
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
			result := gemini.ToGeminiResponsesRequest(tt.input)
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
									Properties: &schemas.OrderedMap{
										"filters": map[string]interface{}{
											"type":        "array",
											"description": "List of filters",
											"items": map[string]interface{}{
												"type":        "string",
												"description": "Filter criterion",
											},
										},
										"sort_order": map[string]interface{}{
											"type": "string",
											"enum": []interface{}{"asc", "desc"},
										},
									},
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
									Properties: &schemas.OrderedMap{
										"updates": map[string]interface{}{
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
										},
									},
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
									Properties: &schemas.OrderedMap{
										"any_array": map[string]interface{}{
											"type":  "array",
											"items": map[string]interface{}{}, // Empty items
										},
									},
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
			result := gemini.ToGeminiResponsesRequest(tt.input)
			require.NotNil(t, result, "Responses API conversion should not return nil")
			tt.validate(t, result)
		})
	}
}
