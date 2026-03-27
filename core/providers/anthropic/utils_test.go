package anthropic

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

func TestExtractTypesFromValue(t *testing.T) {
	tests := []struct {
		name     string
		input    interface{}
		expected []string
	}{
		{
			name:     "string type",
			input:    "string",
			expected: []string{"string"},
		},
		{
			name:     "[]string array",
			input:    []string{"string", "null"},
			expected: []string{"string", "null"},
		},
		{
			name:     "[]interface{} array",
			input:    []interface{}{"string", "integer", "null"},
			expected: []string{"string", "integer", "null"},
		},
		{
			name:     "[]interface{} with non-string items (filtered out)",
			input:    []interface{}{"string", 123, "null"},
			expected: []string{"string", "null"},
		},
		{
			name:     "unsupported type returns nil",
			input:    123,
			expected: nil,
		},
		{
			name:     "nil returns nil",
			input:    nil,
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractTypesFromValue(tt.input)
			if !reflect.DeepEqual(result, tt.expected) {
				t.Errorf("extractTypesFromValue() mismatch:\ngot:  %+v\nwant: %+v", result, tt.expected)
			}
		})
	}
}

func TestNormalizeSchemaForAnthropic(t *testing.T) {
	tests := []struct {
		name     string
		input    map[string]interface{}
		expected map[string]interface{}
	}{
		{
			name: "type array with string and null - converts to anyOf",
			input: map[string]interface{}{
				"type":        []interface{}{"string", "null"},
				"description": "A nullable string field",
				"enum":        []string{"value1", "value2", ""},
			},
			expected: map[string]interface{}{
				"description": "A nullable string field",
				"anyOf": []interface{}{
					map[string]interface{}{
						"type": "string",
						"enum": []string{"value1", "value2", ""},
					},
					map[string]interface{}{"type": "null"},
				},
			},
		},
		{
			name: "type array with null and string - converts to anyOf",
			input: map[string]interface{}{
				"type":        []interface{}{"null", "string"},
				"description": "A nullable string field",
				"enum":        []string{"NODE-0", "NODE-1", ""},
			},
			expected: map[string]interface{}{
				"description": "A nullable string field",
				"anyOf": []interface{}{
					map[string]interface{}{
						"type": "string",
						"enum": []string{"NODE-0", "NODE-1", ""},
					},
					map[string]interface{}{"type": "null"},
				},
			},
		},
		{
			name: "type array as []string format with null - converts to anyOf",
			input: map[string]interface{}{
				"type": []string{"string", "null"},
				"enum": []string{"option1", "option2"},
			},
			expected: map[string]interface{}{
				"anyOf": []interface{}{
					map[string]interface{}{
						"type": "string",
						"enum": []string{"option1", "option2"},
					},
					map[string]interface{}{"type": "null"},
				},
			},
		},
		{
			name: "type array with single type (no null) - keeps as simple type",
			input: map[string]interface{}{
				"type": []string{"string"},
				"enum": []string{"option1", "option2"},
			},
			expected: map[string]interface{}{
				"type": "string",
				"enum": []string{"option1", "option2"},
			},
		},
		{
			name: "regular string type - no change",
			input: map[string]interface{}{
				"type":        "string",
				"description": "A regular string field",
			},
			expected: map[string]interface{}{
				"type":        "string",
				"description": "A regular string field",
			},
		},
		{
			name: "nested properties with nullable type arrays - converts to anyOf",
			input: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"field1": map[string]interface{}{
						"type": []interface{}{"string", "null"},
						"enum": []string{"a", "b"},
					},
					"field2": map[string]interface{}{
						"type": "number",
					},
				},
			},
			expected: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"field1": map[string]interface{}{
						"anyOf": []interface{}{
							map[string]interface{}{
								"type": "string",
								"enum": []string{"a", "b"},
							},
							map[string]interface{}{"type": "null"},
						},
					},
					"field2": map[string]interface{}{
						"type": "number",
					},
				},
			},
		},
		{
			name: "array items with nullable type array - converts to anyOf",
			input: map[string]interface{}{
				"type": "array",
				"items": map[string]interface{}{
					"type": []interface{}{"string", "null"},
					"enum": []string{"x", "y", "z"},
				},
			},
			expected: map[string]interface{}{
				"type": "array",
				"items": map[string]interface{}{
					"anyOf": []interface{}{
						map[string]interface{}{
							"type": "string",
							"enum": []string{"x", "y", "z"},
						},
						map[string]interface{}{"type": "null"},
					},
				},
			},
		},
		{
			name: "anyOf with type arrays - nested anyOf gets flattened conceptually",
			input: map[string]interface{}{
				"anyOf": []interface{}{
					map[string]interface{}{
						"type": []interface{}{"string", "null"},
					},
					map[string]interface{}{
						"type": "number",
					},
				},
			},
			expected: map[string]interface{}{
				"anyOf": []interface{}{
					map[string]interface{}{
						"anyOf": []interface{}{
							map[string]interface{}{"type": "string"},
							map[string]interface{}{"type": "null"},
						},
					},
					map[string]interface{}{
						"type": "number",
					},
				},
			},
		},
		{
			name: "oneOf with nullable type arrays",
			input: map[string]interface{}{
				"oneOf": []interface{}{
					map[string]interface{}{
						"type": []interface{}{"string", "null"},
					},
				},
			},
			expected: map[string]interface{}{
				"oneOf": []interface{}{
					map[string]interface{}{
						"anyOf": []interface{}{
							map[string]interface{}{"type": "string"},
							map[string]interface{}{"type": "null"},
						},
					},
				},
			},
		},
		{
			name: "allOf with nullable type arrays",
			input: map[string]interface{}{
				"allOf": []interface{}{
					map[string]interface{}{
						"type": []interface{}{"string", "null"},
					},
				},
			},
			expected: map[string]interface{}{
				"allOf": []interface{}{
					map[string]interface{}{
						"anyOf": []interface{}{
							map[string]interface{}{"type": "string"},
							map[string]interface{}{"type": "null"},
						},
					},
				},
			},
		},
		{
			name: "definitions with nullable type arrays",
			input: map[string]interface{}{
				"definitions": map[string]interface{}{
					"myDef": map[string]interface{}{
						"type": []interface{}{"string", "null"},
					},
				},
			},
			expected: map[string]interface{}{
				"definitions": map[string]interface{}{
					"myDef": map[string]interface{}{
						"anyOf": []interface{}{
							map[string]interface{}{"type": "string"},
							map[string]interface{}{"type": "null"},
						},
					},
				},
			},
		},
		{
			name: "$defs with nullable type arrays",
			input: map[string]interface{}{
				"$defs": map[string]interface{}{
					"myDef": map[string]interface{}{
						"type": []interface{}{"string", "null"},
					},
				},
			},
			expected: map[string]interface{}{
				"$defs": map[string]interface{}{
					"myDef": map[string]interface{}{
						"anyOf": []interface{}{
							map[string]interface{}{"type": "string"},
							map[string]interface{}{"type": "null"},
						},
					},
				},
			},
		},
		{
			name: "complex nested schema - real world example with nullable enum",
			input: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"action": map[string]interface{}{
						"type": "string",
						"enum": []string{"continue", "transition"},
					},
					"target_node_id": map[string]interface{}{
						"type":        []interface{}{"string", "null"},
						"description": "The ID of the node to transition to. Required when action is 'transition', null when action is 'continue'",
						"enum":        []string{"NODE-0", "NODE-1", "NODE-2", ""},
					},
				},
				"required": []string{"action"},
			},
			expected: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"action": map[string]interface{}{
						"type": "string",
						"enum": []string{"continue", "transition"},
					},
					"target_node_id": map[string]interface{}{
						"description": "The ID of the node to transition to. Required when action is 'transition', null when action is 'continue'",
						"anyOf": []interface{}{
							map[string]interface{}{
								"type": "string",
								"enum": []string{"NODE-0", "NODE-1", "NODE-2", ""},
							},
							map[string]interface{}{"type": "null"},
						},
					},
				},
				"required": []string{"action"},
			},
		},
		{
			name:     "nil schema - returns nil",
			input:    nil,
			expected: nil,
		},
		{
			name:     "empty schema - returns empty",
			input:    map[string]interface{}{},
			expected: map[string]interface{}{},
		},
		{
			name: "type array with multiple non-null types - converts to anyOf",
			input: map[string]interface{}{
				"type":        []interface{}{"string", "integer"},
				"description": "A field that can be string or integer",
			},
			expected: map[string]interface{}{
				"description": "A field that can be string or integer",
				"anyOf": []interface{}{
					map[string]interface{}{"type": "string"},
					map[string]interface{}{"type": "integer"},
				},
			},
		},
		{
			name: "type array with multiple types including null - converts to anyOf with null",
			input: map[string]interface{}{
				"type":        []interface{}{"string", "integer", "null"},
				"description": "A nullable field that can be string or integer",
			},
			expected: map[string]interface{}{
				"description": "A nullable field that can be string or integer",
				"anyOf": []interface{}{
					map[string]interface{}{"type": "string"},
					map[string]interface{}{"type": "integer"},
					map[string]interface{}{"type": "null"},
				},
			},
		},
		{
			name: "type array with multiple types and enum - filters enum values by type in anyOf branches",
			input: map[string]interface{}{
				"type": []interface{}{"string", "integer"},
				"enum": []interface{}{"value1", 123},
			},
			expected: map[string]interface{}{
				"anyOf": []interface{}{
					map[string]interface{}{
						"type": "string",
						"enum": []interface{}{"value1"},
					},
					map[string]interface{}{
						"type": "integer",
						"enum": []interface{}{123},
					},
				},
			},
		},
		{
			name: "nested properties with multi-type arrays - all convert to anyOf",
			input: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"field1": map[string]interface{}{
						"type": []interface{}{"string", "number"},
					},
					"field2": map[string]interface{}{
						"type": []interface{}{"boolean", "null"},
					},
				},
			},
			expected: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"field1": map[string]interface{}{
						"anyOf": []interface{}{
							map[string]interface{}{"type": "string"},
							map[string]interface{}{"type": "number"},
						},
					},
					"field2": map[string]interface{}{
						"anyOf": []interface{}{
							map[string]interface{}{"type": "boolean"},
							map[string]interface{}{"type": "null"},
						},
					},
				},
			},
		},
		{
			name: "real world priority field with mixed string and integer enum - filters correctly",
			input: map[string]interface{}{
				"type":        []interface{}{"string", "integer"},
				"description": "Priority level - can be a number (1-10) or a string label (low/medium/high)",
				"enum":        []interface{}{"low", "medium", "high", 1, 2, 3, 4, 5, 6, 7, 8, 9, 10},
			},
			expected: map[string]interface{}{
				"description": "Priority level - can be a number (1-10) or a string label (low/medium/high)",
				"anyOf": []interface{}{
					map[string]interface{}{
						"type": "string",
						"enum": []interface{}{"low", "medium", "high"},
					},
					map[string]interface{}{
						"type": "integer",
						"enum": []interface{}{1, 2, 3, 4, 5, 6, 7, 8, 9, 10},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := normalizeSchemaForAnthropic(tt.input)

			// Compare using JSON marshaling to handle []string vs []interface{} differences
			// Marshal both to JSON, then unmarshal back to normalized form for comparison
			// This ensures we compare actual structure, not field ordering
			gotJSON, err1 := sonic.Marshal(result)
			wantJSON, err2 := sonic.Marshal(tt.expected)

			if err1 != nil || err2 != nil {
				t.Fatalf("Failed to marshal for comparison: got err=%v, want err=%v", err1, err2)
			}

			// Unmarshal both back to interface{} to normalize the comparison
			// This handles both field ordering and []string vs []interface{} differences
			var gotNormalized, wantNormalized interface{}
			if err := sonic.Unmarshal(gotJSON, &gotNormalized); err != nil {
				t.Fatalf("Failed to unmarshal got JSON: %v", err)
			}
			if err := sonic.Unmarshal(wantJSON, &wantNormalized); err != nil {
				t.Fatalf("Failed to unmarshal want JSON: %v", err)
			}

			// Now compare the unmarshaled structures
			if !reflect.DeepEqual(gotNormalized, wantNormalized) {
				// Pretty print for error message
				gotJSONPretty, _ := sonic.MarshalIndent(result, "", "  ")
				wantJSONPretty, _ := sonic.MarshalIndent(tt.expected, "", "  ")
				t.Errorf("normalizeSchemaForAnthropic() mismatch:\ngot:  %s\nwant: %s", gotJSONPretty, wantJSONPretty)
			}
		})
	}
}

func TestConvertChatResponseFormatToAnthropicOutputFormat(t *testing.T) {
	tests := []struct {
		name     string
		input    *interface{}
		expected interface{}
	}{
		{
			name: "chat format with nullable enum gets normalized to anyOf",
			input: func() *interface{} {
				val := interface{}(map[string]interface{}{
					"type": "json_schema",
					"json_schema": map[string]interface{}{
						"name": "TestSchema",
						"schema": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"field": map[string]interface{}{
									"type": []interface{}{"string", "null"},
									"enum": []string{"value1", "value2"},
								},
							},
						},
					},
				})
				return &val
			}(),
			expected: map[string]interface{}{
				"type": "json_schema",
				"schema": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"field": map[string]interface{}{
							"anyOf": []interface{}{
								map[string]interface{}{
									"type": "string",
									"enum": []string{"value1", "value2"},
								},
								map[string]interface{}{"type": "null"},
							},
						},
					},
				},
			},
		},
		{
			name:     "nil input returns nil",
			input:    nil,
			expected: nil,
		},
		{
			name: "non-json_schema type returns nil",
			input: func() *interface{} {
				val := interface{}(map[string]interface{}{
					"type": "json",
				})
				return &val
			}(),
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := convertChatResponseFormatToAnthropicOutputFormat(tt.input)

			// Compare using JSON marshaling to handle field ordering differences
			resultJSON, err1 := sonic.Marshal(result)
			expectedJSON, err2 := sonic.Marshal(tt.expected)

			if err1 != nil || err2 != nil {
				t.Fatalf("Failed to marshal for comparison: result err=%v, expected err=%v", err1, err2)
			}

			// Unmarshal both back to interface{} to normalize the comparison
			var resultNormalized, expectedNormalized interface{}
			if err := sonic.Unmarshal(resultJSON, &resultNormalized); err != nil {
				t.Fatalf("Failed to unmarshal result JSON: %v", err)
			}
			if err := sonic.Unmarshal(expectedJSON, &expectedNormalized); err != nil {
				t.Fatalf("Failed to unmarshal expected JSON: %v", err)
			}

			if !reflect.DeepEqual(resultNormalized, expectedNormalized) {
				t.Errorf("convertChatResponseFormatToAnthropicOutputFormat() mismatch:\ngot:  %+v\nwant: %+v", result, tt.expected)
			}
		})
	}
}

func TestValidateToolsForProvider(t *testing.T) {
	tests := []struct {
		name      string
		tools     []schemas.ResponsesTool
		provider  schemas.ModelProvider
		expectErr bool
	}{
		{
			name:      "Anthropic allows web_search",
			tools:     []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeWebSearch}},
			provider:  schemas.Anthropic,
			expectErr: false,
		},
		{
			name:      "Anthropic allows web_fetch",
			tools:     []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeWebFetch}},
			provider:  schemas.Anthropic,
			expectErr: false,
		},
		{
			name:      "Vertex allows web_search",
			tools:     []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeWebSearch}},
			provider:  schemas.Vertex,
			expectErr: false,
		},
		{
			name:      "Vertex rejects web_fetch",
			tools:     []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeWebFetch}},
			provider:  schemas.Vertex,
			expectErr: true,
		},
		{
			name:      "Vertex rejects code_interpreter",
			tools:     []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeCodeInterpreter}},
			provider:  schemas.Vertex,
			expectErr: true,
		},
		{
			name:      "Vertex rejects MCP",
			tools:     []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeMCP}},
			provider:  schemas.Vertex,
			expectErr: true,
		},
		{
			name:      "Bedrock rejects web_search",
			tools:     []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeWebSearch}},
			provider:  schemas.Bedrock,
			expectErr: true,
		},
		{
			name:      "Bedrock rejects web_fetch",
			tools:     []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeWebFetch}},
			provider:  schemas.Bedrock,
			expectErr: true,
		},
		{
			name:      "Bedrock allows computer_use",
			tools:     []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeComputerUsePreview}},
			provider:  schemas.Bedrock,
			expectErr: false,
		},
		{
			name:      "Azure allows everything",
			tools:     []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeWebFetch}, {Type: schemas.ResponsesToolTypeCodeInterpreter}, {Type: schemas.ResponsesToolTypeMCP}},
			provider:  schemas.Azure,
			expectErr: false,
		},
		{
			name:      "Unknown provider allows all",
			tools:     []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeWebFetch}},
			provider:  "custom_provider",
			expectErr: false,
		},
		{
			name:      "Function tools always allowed",
			tools:     []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeFunction}},
			provider:  schemas.Bedrock,
			expectErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateToolsForProvider(tt.tools, tt.provider)
			if tt.expectErr && err == nil {
				t.Errorf("expected error but got nil")
			}
			if !tt.expectErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestAddMissingBetaHeadersToContext_PerProvider(t *testing.T) {
	tests := []struct {
		name            string
		provider        schemas.ModelProvider
		req             *AnthropicMessageRequest
		expectHeaders   []string
		unexpectHeaders []string
	}{
		{
			name:     "Anthropic gets structured outputs header",
			provider: schemas.Anthropic,
			req: &AnthropicMessageRequest{
				OutputFormat: json.RawMessage(`{"type":"json_schema"}`),
			},
			expectHeaders: []string{AnthropicStructuredOutputsBetaHeader},
		},
		{
			name:     "Vertex skips structured outputs header",
			provider: schemas.Vertex,
			req: &AnthropicMessageRequest{
				OutputFormat: json.RawMessage(`{"type":"json_schema"}`),
			},
			unexpectHeaders: []string{AnthropicStructuredOutputsBetaHeader},
		},
		{
			name:     "Vertex skips MCP header",
			provider: schemas.Vertex,
			req: &AnthropicMessageRequest{
				MCPServers: []AnthropicMCPServer{{URL: "http://example.com"}},
			},
			unexpectHeaders: []string{AnthropicMCPClientBetaHeader},
		},
		{
			name:     "Anthropic gets MCP header",
			provider: schemas.Anthropic,
			req: &AnthropicMessageRequest{
				MCPServers: []AnthropicMCPServer{{URL: "http://example.com"}},
			},
			expectHeaders: []string{AnthropicMCPClientBetaHeader},
		},
		{
			name:     "Vertex gets compaction header",
			provider: schemas.Vertex,
			req: &AnthropicMessageRequest{
				ContextManagement: &ContextManagement{
					Edits: []ContextManagementEdit{{Type: ContextManagementEditTypeCompact}},
				},
			},
			expectHeaders: []string{AnthropicCompactionBetaHeader},
		},
		{
			name:     "Bedrock gets compaction header",
			provider: schemas.Bedrock,
			req: &AnthropicMessageRequest{
				ContextManagement: &ContextManagement{
					Edits: []ContextManagementEdit{{Type: ContextManagementEditTypeCompact}},
				},
			},
			expectHeaders: []string{AnthropicCompactionBetaHeader},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := schemas.NewBifrostContext(nil, time.Time{})
			AddMissingBetaHeadersToContext(ctx, tt.req, tt.provider)

			var headers []string
			if extraHeaders, ok := ctx.Value(schemas.BifrostContextKeyExtraHeaders).(map[string][]string); ok {
				headers = extraHeaders["anthropic-beta"]
			}

			for _, expected := range tt.expectHeaders {
				found := false
				for _, h := range headers {
					if h == expected {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected header %q not found in %v", expected, headers)
				}
			}

			for _, unexpected := range tt.unexpectHeaders {
				for _, h := range headers {
					if h == unexpected {
						t.Errorf("unexpected header %q found in %v", unexpected, headers)
					}
				}
			}
		})
	}
}

func TestFilterBetaHeadersForProvider(t *testing.T) {
	allHeaders := []string{
		AnthropicComputerUseBetaHeader20251124,
		AnthropicStructuredOutputsBetaHeader,
		AnthropicMCPClientBetaHeader,
		AnthropicPromptCachingScopeBetaHeader,
		AnthropicCompactionBetaHeader,
		AnthropicContextManagementBetaHeader,
		AnthropicAdvancedToolUseBetaHeader,
		AnthropicFilesAPIBetaHeader,
	}

	t.Run("Anthropic/keeps_all_headers", func(t *testing.T) {
		result, err := FilterBetaHeadersForProvider(allHeaders, schemas.Anthropic)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		for _, h := range allHeaders {
			found := false
			for _, r := range result {
				if r == h {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("expected header %q to be kept for Anthropic, got %v", h, result)
			}
		}
	})

	t.Run("Vertex/errors_on_unsupported_headers", func(t *testing.T) {
		unsupported := []string{
			AnthropicStructuredOutputsBetaHeader,
			AnthropicMCPClientBetaHeader,
			AnthropicPromptCachingScopeBetaHeader,
			AnthropicAdvancedToolUseBetaHeader,
			AnthropicFilesAPIBetaHeader,
		}
		for _, h := range unsupported {
			_, err := FilterBetaHeadersForProvider([]string{h}, schemas.Vertex)
			if err == nil {
				t.Errorf("expected error for header %q on Vertex, got nil", h)
			}
		}
	})

	t.Run("Vertex/allows_supported_headers", func(t *testing.T) {
		supported := []string{
			AnthropicComputerUseBetaHeader20251124,
			AnthropicCompactionBetaHeader,
			AnthropicContextManagementBetaHeader,
		}
		result, err := FilterBetaHeadersForProvider(supported, schemas.Vertex)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != len(supported) {
			t.Errorf("expected %d headers, got %d: %v", len(supported), len(result), result)
		}
	})

	t.Run("Bedrock/errors_on_unsupported_headers", func(t *testing.T) {
		unsupported := []string{
			AnthropicMCPClientBetaHeader,
			AnthropicPromptCachingScopeBetaHeader,
			AnthropicAdvancedToolUseBetaHeader,
			AnthropicFilesAPIBetaHeader,
		}
		for _, h := range unsupported {
			_, err := FilterBetaHeadersForProvider([]string{h}, schemas.Bedrock)
			if err == nil {
				t.Errorf("expected error for header %q on Bedrock, got nil", h)
			}
		}
	})

	t.Run("unknown_headers_forwarded", func(t *testing.T) {
		headers := []string{"some-future-beta-2025"}
		result, err := FilterBetaHeadersForProvider(headers, schemas.Vertex)
		if err != nil {
			t.Fatalf("unexpected error for unknown headers: %v", err)
		}
		if len(result) != len(headers) {
			t.Errorf("expected all unknown headers to be forwarded, got %v", result)
		}
	})

	t.Run("unknown_provider_allows_all", func(t *testing.T) {
		result, err := FilterBetaHeadersForProvider(allHeaders, schemas.ModelProvider("custom-provider"))
		if err != nil {
			t.Fatalf("unexpected error for unknown provider: %v", err)
		}
		if len(result) != len(allHeaders) {
			t.Errorf("expected all headers for unknown provider, got %v", result)
		}
	})
}

func TestStripAutoInjectableTools(t *testing.T) {
	t.Run("code_execution_without_web_search_preserved", func(t *testing.T) {
		// code_execution alone should NOT be stripped (no web_search/web_fetch to trigger auto-injection)
		input := []byte(`{"model":"claude-opus-4-6","tools":[{"type":"custom","name":"my_tool"},{"type":"code_execution_20250825","name":"code_execution"}]}`)
		result, err := StripAutoInjectableTools(input)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		tools := providerUtils.GetJSONField(result, "tools")
		arr := tools.Array()
		if len(arr) != 2 {
			t.Fatalf("expected 2 tools (preserved), got %d", len(arr))
		}
	})

	t.Run("code_execution_with_web_search_stripped", func(t *testing.T) {
		// code_execution should be stripped when web_search is present (auto-injection conflict)
		input := []byte(`{"tools":[{"type":"code_execution_20250825","name":"code_execution"},{"type":"web_search_20260209","name":"web_search"},{"type":"custom","name":"my_tool"}]}`)
		result, err := StripAutoInjectableTools(input)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		tools := providerUtils.GetJSONField(result, "tools")
		arr := tools.Array()
		if len(arr) != 2 {
			t.Fatalf("expected 2 tools, got %d", len(arr))
		}
		if arr[0].Get("name").String() != "web_search" {
			t.Errorf("expected first tool to be 'web_search', got '%s'", arr[0].Get("name").String())
		}
		if arr[1].Get("name").String() != "my_tool" {
			t.Errorf("expected second tool to be 'my_tool', got '%s'", arr[1].Get("name").String())
		}
	})

	t.Run("code_execution_with_web_fetch_stripped", func(t *testing.T) {
		// code_execution should be stripped when web_fetch is present
		input := []byte(`{"tools":[{"type":"code_execution_20250825","name":"code_execution"},{"type":"web_fetch_20250305","name":"web_fetch"},{"type":"custom","name":"my_tool"}]}`)
		result, err := StripAutoInjectableTools(input)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		tools := providerUtils.GetJSONField(result, "tools")
		arr := tools.Array()
		if len(arr) != 2 {
			t.Fatalf("expected 2 tools, got %d", len(arr))
		}
		if arr[0].Get("name").String() != "web_fetch" {
			t.Errorf("expected first tool to be 'web_fetch', got '%s'", arr[0].Get("name").String())
		}
		if arr[1].Get("name").String() != "my_tool" {
			t.Errorf("expected second tool to be 'my_tool', got '%s'", arr[1].Get("name").String())
		}
	})

	t.Run("web_search_alone_preserved", func(t *testing.T) {
		// web_search without code_execution should be preserved entirely
		input := []byte(`{"tools":[{"type":"web_search_20250305","name":"web_search"},{"type":"custom","name":"search"}]}`)
		result, err := StripAutoInjectableTools(input)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		tools := providerUtils.GetJSONField(result, "tools")
		arr := tools.Array()
		if len(arr) != 2 {
			t.Fatalf("expected 2 tools (preserved), got %d", len(arr))
		}
	})

	t.Run("web_fetch_alone_preserved", func(t *testing.T) {
		// web_fetch without code_execution should be preserved entirely
		input := []byte(`{"tools":[{"type":"web_fetch_20250305","name":"web_fetch"},{"type":"custom","name":"fetch"}]}`)
		result, err := StripAutoInjectableTools(input)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		tools := providerUtils.GetJSONField(result, "tools")
		arr := tools.Array()
		if len(arr) != 2 {
			t.Fatalf("expected 2 tools (preserved), got %d", len(arr))
		}
	})

	t.Run("preserves_custom_tools_only", func(t *testing.T) {
		input := []byte(`{"tools":[{"type":"custom","name":"tool_a"},{"type":"custom","name":"tool_b"}]}`)
		result, err := StripAutoInjectableTools(input)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		tools := providerUtils.GetJSONField(result, "tools")
		arr := tools.Array()
		if len(arr) != 2 {
			t.Fatalf("expected 2 tools, got %d", len(arr))
		}
	})

	t.Run("no_tools_key", func(t *testing.T) {
		input := []byte(`{"model":"claude-opus-4-6","messages":[]}`)
		result, err := StripAutoInjectableTools(input)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if string(result) != string(input) {
			t.Errorf("expected body unchanged, got %s", string(result))
		}
	})

	t.Run("empty_tools_array", func(t *testing.T) {
		input := []byte(`{"tools":[]}`)
		result, err := StripAutoInjectableTools(input)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if string(result) != string(input) {
			t.Errorf("expected body unchanged, got %s", string(result))
		}
	})

	t.Run("code_execution_and_web_search_only_strips_code_execution", func(t *testing.T) {
		// When only code_execution + web_search, strip code_execution, keep web_search
		input := []byte(`{"model":"test","tools":[{"type":"code_execution_20250825","name":"code_execution"},{"type":"web_search_20250305","name":"web_search"}]}`)
		result, err := StripAutoInjectableTools(input)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		tools := providerUtils.GetJSONField(result, "tools")
		arr := tools.Array()
		if len(arr) != 1 {
			t.Fatalf("expected 1 tool, got %d", len(arr))
		}
		if arr[0].Get("name").String() != "web_search" {
			t.Errorf("expected remaining tool to be 'web_search', got '%s'", arr[0].Get("name").String())
		}
	})

	t.Run("strips_code_execution_keeps_web_search_and_custom", func(t *testing.T) {
		input := []byte(`{"tools":[{"type":"code_execution_20250825","name":"code_execution"},{"type":"custom","name":"my_tool"},{"type":"web_search_20260209","name":"web_search"},{"type":"custom","name":"other_tool"}]}`)
		result, err := StripAutoInjectableTools(input)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		tools := providerUtils.GetJSONField(result, "tools")
		arr := tools.Array()
		if len(arr) != 3 {
			t.Fatalf("expected 3 tools, got %d", len(arr))
		}
		if arr[0].Get("name").String() != "my_tool" {
			t.Errorf("expected first tool to be 'my_tool', got '%s'", arr[0].Get("name").String())
		}
		if arr[1].Get("name").String() != "web_search" {
			t.Errorf("expected second tool to be 'web_search', got '%s'", arr[1].Get("name").String())
		}
		if arr[2].Get("name").String() != "other_tool" {
			t.Errorf("expected third tool to be 'other_tool', got '%s'", arr[2].Get("name").String())
		}
	})
}
