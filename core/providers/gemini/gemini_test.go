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
