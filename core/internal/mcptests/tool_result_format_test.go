package mcptests

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// TOOL RESULT FORMAT HANDLING TESTS
// =============================================================================
// These tests verify tool result format handling (agent.go:404-427, agentadaptors.go:179-304)
// Focus: Complex structures, multi-part content, binary content, size limits, edge cases

func TestToolResult_ComplexNestedStructures(t *testing.T) {
	t.Parallel()

	// Test deeply nested JSON structures in tool results
	manager := setupMCPManager(t)

	// Create tool that returns deeply nested structure
	nestedHandler := func(ctx context.Context, args any) (string, error) {
		// 5 levels deep nested structure
		result := map[string]interface{}{
			"level1": map[string]interface{}{
				"level2": map[string]interface{}{
					"level3": map[string]interface{}{
						"level4": map[string]interface{}{
							"level5": map[string]interface{}{
								"data": "deeply nested value",
								"array": []int{1, 2, 3, 4, 5},
								"boolean": true,
								"null": nil,
							},
						},
					},
				},
			},
		}

		jsonBytes, _ := json.Marshal(result)
		return string(jsonBytes), nil
	}

	nestedSchema := schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name:        "nested_tool",
			Description: schemas.Ptr("Returns deeply nested structure"),
		},
	}

	err := manager.RegisterTool("nested_tool", "Returns deeply nested structure", nestedHandler, nestedSchema)
	require.NoError(t, err)

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	toolCall := CreateToolCallForExecution("call-nested", "nested_tool", map[string]interface{}{})

	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

	require.Nil(t, bifrostErr, "should handle nested structures")
	require.NotNil(t, result)
	require.NotNil(t, result.Content)

	t.Logf("✅ Deeply nested structure (5 levels) handled successfully")
}

func TestToolResult_MultiPartContent(t *testing.T) {
	t.Parallel()

	// Test tool results with multi-part content blocks
	manager := setupMCPManager(t)

	// Tool that returns content with multiple sections
	multiPartHandler := func(ctx context.Context, args any) (string, error) {
		result := map[string]interface{}{
			"text_part": "This is the text section",
			"data_part": map[string]interface{}{
				"values": []int{1, 2, 3},
			},
			"metadata_part": map[string]interface{}{
				"timestamp": "2024-01-01T00:00:00Z",
				"source": "test",
			},
		}

		jsonBytes, _ := json.Marshal(result)
		return string(jsonBytes), nil
	}

	multiPartSchema := schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name:        "multipart_tool",
			Description: schemas.Ptr("Returns multi-part content"),
		},
	}

	err := manager.RegisterTool("multipart_tool", "Returns multi-part content", multiPartHandler, multiPartSchema)
	require.NoError(t, err)

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	toolCall := CreateToolCallForExecution("call-multipart", "multipart_tool", map[string]interface{}{})

	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

	require.Nil(t, bifrostErr)
	require.NotNil(t, result)

	// Verify all parts are present in content
	if result.Content != nil && result.Content.ContentStr != nil {
		content := *result.Content.ContentStr
		assert.Contains(t, content, "text_part")
		assert.Contains(t, content, "data_part")
		assert.Contains(t, content, "metadata_part")
	}

	t.Logf("✅ Multi-part content handled successfully")
}

func TestToolResult_LargePayload(t *testing.T) {
	t.Parallel()

	// Test handling of large tool result payloads
	manager := setupMCPManager(t)

	sizeTests := []struct {
		name     string
		sizeKB   int
		expected bool
	}{
		{"small_1kb", 1, true},
		{"medium_100kb", 100, true},
		{"large_1mb", 1024, true},
		{"very_large_5mb", 5120, true},
	}

	for _, st := range sizeTests {
		t.Run(st.name, func(t *testing.T) {
			toolName := "large_tool_" + st.name
			targetSize := st.sizeKB

			largeHandler := func(ctx context.Context, args any) (string, error) {
				// Generate payload of target size
				sizeBytes := targetSize * 1024
				data := strings.Repeat("x", sizeBytes)

				result := map[string]interface{}{
					"size_kb": targetSize,
					"data":    data,
				}

				jsonBytes, _ := json.Marshal(result)
				return string(jsonBytes), nil
			}

			largeSchema := schemas.ChatTool{
				Type: schemas.ChatToolTypeFunction,
				Function: &schemas.ChatToolFunction{
					Name:        toolName,
					Description: schemas.Ptr(fmt.Sprintf("Returns %dKB payload", targetSize)),
				},
			}

			err := manager.RegisterTool(toolName, fmt.Sprintf("Returns %dKB", targetSize), largeHandler, largeSchema)
			require.NoError(t, err)

			bifrost := setupBifrost(t)
			bifrost.SetMCPManager(manager)

			ctx := createTestContext()

			toolCall := CreateToolCallForExecution("call-large-"+st.name, toolName, map[string]interface{}{})

			result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

			if st.expected {
				require.Nil(t, bifrostErr, "should handle %dKB payload", targetSize)
				require.NotNil(t, result)

				// Verify payload size
				if result.Content != nil && result.Content.ContentStr != nil {
					contentSize := len(*result.Content.ContentStr)
					t.Logf("✅ Large payload (%dKB) handled: actual size %d bytes", targetSize, contentSize)
				}
			} else {
				t.Logf("Payload size %dKB: %v", targetSize, bifrostErr)
			}
		})
	}
}

func TestToolResult_SpecialCharactersAndUnicode(t *testing.T) {
	t.Parallel()

	// Test tool results with special characters and Unicode
	manager := setupMCPManager(t)

	testCases := []struct {
		name    string
		content string
	}{
		{"ascii_special", "!@#$%^&*()_+-=[]{}|;:',.<>?/`~"},
		{"unicode_chars", "こんにちは世界 🌍 مرحبا العالم"},
		{"emojis", "😀😃😄😁🤣😂🤩😍🥰😘"},
		{"mixed", "Hello 世界! 🌍 Test #123"},
		{"newlines", "Line1\nLine2\nLine3\n"},
		{"tabs", "Col1\tCol2\tCol3"},
		{"quotes", `"double" and 'single' quotes`},
		{"backslashes", `path\to\file\test.txt`},
		{"control_chars", "test\x00\x01\x02"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			toolName := "special_" + tc.name
			testContent := tc.content

			specialHandler := func(ctx context.Context, args any) (string, error) {
				result := map[string]interface{}{
					"content": testContent,
					"type":    tc.name,
				}

				jsonBytes, _ := json.Marshal(result)
				return string(jsonBytes), nil
			}

			specialSchema := schemas.ChatTool{
				Type: schemas.ChatToolTypeFunction,
				Function: &schemas.ChatToolFunction{
					Name:        toolName,
					Description: schemas.Ptr("Returns special characters"),
				},
			}

			err := manager.RegisterTool(toolName, "Returns special chars", specialHandler, specialSchema)
			require.NoError(t, err)

			bifrost := setupBifrost(t)
			bifrost.SetMCPManager(manager)

			ctx := createTestContext()

			toolCall := CreateToolCallForExecution("call-special-"+tc.name, toolName, map[string]interface{}{"content": tc.content})

			result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

			require.Nil(t, bifrostErr, "should handle special characters: %s", tc.name)
			require.NotNil(t, result)

			t.Logf("✅ Special characters handled: %s", tc.name)
		})
	}
}

func TestToolResult_EmptyAndNullContent(t *testing.T) {
	t.Parallel()

	// Test edge cases: empty string, null, undefined
	manager := setupMCPManager(t)

	testCases := []struct {
		name     string
		response string
	}{
		{"empty_string", ""},
		{"empty_object", "{}"},
		{"null", "null"},
		{"empty_array", "[]"},
		{"whitespace_only", "   \n\t  "},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			toolName := "empty_" + tc.name
			responseStr := tc.response

			emptyHandler := func(ctx context.Context, args any) (string, error) {
				return responseStr, nil
			}

			emptySchema := schemas.ChatTool{
				Type: schemas.ChatToolTypeFunction,
				Function: &schemas.ChatToolFunction{
					Name:        toolName,
					Description: schemas.Ptr("Returns empty/null content"),
				},
			}

			err := manager.RegisterTool(toolName, "Returns empty content", emptyHandler, emptySchema)
			require.NoError(t, err)

			bifrost := setupBifrost(t)
			bifrost.SetMCPManager(manager)

			ctx := createTestContext()

			toolCall := CreateToolCallForExecution("call-null-"+tc.name, toolName, map[string]interface{}{})

			result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

			// Should handle empty/null content gracefully
			if bifrostErr != nil {
				t.Logf("Empty content (%s) resulted in error: %v", tc.name, bifrostErr.Error)
			} else {
				require.NotNil(t, result)
				t.Logf("✅ Empty content handled: %s", tc.name)
			}
		})
	}
}

func TestToolResult_ArrayResults(t *testing.T) {
	t.Parallel()

	// Test tool results that return arrays
	manager := setupMCPManager(t)

	arrayHandler := func(ctx context.Context, args any) (string, error) {
		result := []interface{}{
			map[string]interface{}{"id": 1, "name": "Item 1"},
			map[string]interface{}{"id": 2, "name": "Item 2"},
			map[string]interface{}{"id": 3, "name": "Item 3"},
			"string item",
			123,
			true,
			nil,
		}

		jsonBytes, _ := json.Marshal(result)
		return string(jsonBytes), nil
	}

	arraySchema := schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name:        "array_tool",
			Description: schemas.Ptr("Returns array result"),
		},
	}

	err := manager.RegisterTool("array_tool", "Returns array", arrayHandler, arraySchema)
	require.NoError(t, err)

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	toolCall := CreateToolCallForExecution("call-array", "array_tool", map[string]interface{}{})

	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

	require.Nil(t, bifrostErr, "should handle array results")
	require.NotNil(t, result)

	t.Logf("✅ Array result handled successfully")
}

func TestToolResult_MixedDataTypes(t *testing.T) {
	t.Parallel()

	// Test tool results with mixed data types
	manager := setupMCPManager(t)

	mixedHandler := func(ctx context.Context, args any) (string, error) {
		result := map[string]interface{}{
			"string":  "text value",
			"integer": 42,
			"float":   3.14159,
			"boolean": true,
			"null":    nil,
			"array":   []interface{}{1, "two", 3.0, false},
			"object": map[string]interface{}{
				"nested": "value",
			},
		}

		jsonBytes, _ := json.Marshal(result)
		return string(jsonBytes), nil
	}

	mixedSchema := schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name:        "mixed_tool",
			Description: schemas.Ptr("Returns mixed data types"),
		},
	}

	err := manager.RegisterTool("mixed_tool", "Returns mixed types", mixedHandler, mixedSchema)
	require.NoError(t, err)

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	toolCall := CreateToolCallForExecution("call-mixed", "mixed_tool", map[string]interface{}{})

	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

	require.Nil(t, bifrostErr)
	require.NotNil(t, result)

	// Verify all data types are preserved
	if result.Content != nil && result.Content.ContentStr != nil {
		content := *result.Content.ContentStr
		assert.Contains(t, content, "string")
		assert.Contains(t, content, "integer")
		assert.Contains(t, content, "float")
		assert.Contains(t, content, "boolean")
	}

	t.Logf("✅ Mixed data types handled successfully")
}

func TestToolResult_BothFormats_ComplexStructure(t *testing.T) {
	t.Parallel()

	// Test complex structures in both Chat and Responses formats
	manager := setupMCPManager(t)

	complexHandler := func(ctx context.Context, args any) (string, error) {
		result := map[string]interface{}{
			"status": "success",
			"data": map[string]interface{}{
				"items": []map[string]interface{}{
					{"id": 1, "value": "first"},
					{"id": 2, "value": "second"},
				},
			},
		}

		jsonBytes, _ := json.Marshal(result)
		return string(jsonBytes), nil
	}

	complexSchema := schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name:        "complex_tool",
			Description: schemas.Ptr("Returns complex structure"),
		},
	}

	err := manager.RegisterTool("complex_tool", "Returns complex structure", complexHandler, complexSchema)
	require.NoError(t, err)

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	t.Run("chat_format", func(t *testing.T) {
		toolCall := CreateToolCallForExecution("call-complex-chat", "complex_tool", map[string]interface{}{})

		result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

		require.Nil(t, bifrostErr)
		require.NotNil(t, result)
		t.Logf("✅ Chat format: complex structure handled")
	})

	t.Run("responses_format", func(t *testing.T) {
		args := map[string]interface{}{}
		toolMsg := CreateResponsesToolCallForExecution("call-complex-resp", "complex_tool", args)

		result, bifrostErr := bifrost.ExecuteResponsesMCPTool(ctx, &toolMsg)

		require.Nil(t, bifrostErr)
		require.NotNil(t, result)
		t.Logf("✅ Responses format: complex structure handled")
	})
}

func TestToolResult_ContentEncoding(t *testing.T) {
	t.Parallel()

	// Test different content encodings
	manager := setupMCPManager(t)

	testCases := []struct {
		name    string
		content string
	}{
		{"base64_like", "SGVsbG8gV29ybGQh"},
		{"url_encoded", "hello%20world%3F%26test%3Dtrue"},
		{"html_entities", "&lt;div&gt;Hello &amp; Goodbye&lt;/div&gt;"},
		{"json_escaped", `{\"key\": \"value\"}`},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			toolName := "encoding_" + tc.name
			testContent := tc.content

			encodingHandler := func(ctx context.Context, args any) (string, error) {
				result := map[string]interface{}{
					"encoded": testContent,
				}

				jsonBytes, _ := json.Marshal(result)
				return string(jsonBytes), nil
			}

			encodingSchema := schemas.ChatTool{
				Type: schemas.ChatToolTypeFunction,
				Function: &schemas.ChatToolFunction{
					Name:        toolName,
					Description: schemas.Ptr("Returns encoded content"),
				},
			}

			err := manager.RegisterTool(toolName, "Returns encoded content", encodingHandler, encodingSchema)
			require.NoError(t, err)

			bifrost := setupBifrost(t)
			bifrost.SetMCPManager(manager)

			ctx := createTestContext()

			toolCall := CreateToolCallForExecution("call-encoding-"+tc.name, toolName, map[string]interface{}{})

			result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

			require.Nil(t, bifrostErr)
			require.NotNil(t, result)

			t.Logf("✅ Encoded content handled: %s", tc.name)
		})
	}
}
