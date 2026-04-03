package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"sort"
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
				MCPServers: []AnthropicMCPServerV2{{URL: "http://example.com"}},
			},
			unexpectHeaders: []string{AnthropicMCPClientBetaHeader},
		},
		{
			name:     "Anthropic gets MCP header",
			provider: schemas.Anthropic,
			req: &AnthropicMessageRequest{
				MCPServers: []AnthropicMCPServerV2{{URL: "http://example.com"}},
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
		// Interleaved thinking tests
		{
			name:     "Anthropic gets interleaved thinking header for enabled",
			provider: schemas.Anthropic,
			req: &AnthropicMessageRequest{
				Thinking: &AnthropicThinking{Type: "enabled", BudgetTokens: schemas.Ptr(2048)},
			},
			expectHeaders: []string{AnthropicInterleavedThinkingBetaHeader},
		},
		{
			name:     "Anthropic does not get interleaved thinking header for adaptive",
			provider: schemas.Anthropic,
			req: &AnthropicMessageRequest{
				Thinking: &AnthropicThinking{Type: "adaptive"},
			},
			unexpectHeaders: []string{AnthropicInterleavedThinkingBetaHeader},
		},
		{
			name:     "Vertex gets interleaved thinking header",
			provider: schemas.Vertex,
			req: &AnthropicMessageRequest{
				Thinking: &AnthropicThinking{Type: "enabled", BudgetTokens: schemas.Ptr(2048)},
			},
			expectHeaders: []string{AnthropicInterleavedThinkingBetaHeader},
		},
		{
			name:     "Bedrock gets interleaved thinking header",
			provider: schemas.Bedrock,
			req: &AnthropicMessageRequest{
				Thinking: &AnthropicThinking{Type: "enabled", BudgetTokens: schemas.Ptr(2048)},
			},
			expectHeaders: []string{AnthropicInterleavedThinkingBetaHeader},
		},
		{
			name:     "Disabled thinking does not get interleaved thinking header",
			provider: schemas.Anthropic,
			req: &AnthropicMessageRequest{
				Thinking: &AnthropicThinking{Type: "disabled"},
			},
			unexpectHeaders: []string{AnthropicInterleavedThinkingBetaHeader},
		},
		// Fast mode tests
		{
			name:     "Anthropic gets fast mode header",
			provider: schemas.Anthropic,
			req: &AnthropicMessageRequest{
				Speed: schemas.Ptr("fast"),
			},
			expectHeaders: []string{AnthropicFastModeBetaHeader},
		},
		{
			name:     "Bedrock skips fast mode header",
			provider: schemas.Bedrock,
			req: &AnthropicMessageRequest{
				Speed: schemas.Ptr("fast"),
			},
			unexpectHeaders: []string{AnthropicFastModeBetaHeader},
		},
		{
			name:     "Azure skips fast mode header",
			provider: schemas.Azure,
			req: &AnthropicMessageRequest{
				Speed: schemas.Ptr("fast"),
			},
			unexpectHeaders: []string{AnthropicFastModeBetaHeader},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := schemas.NewBifrostContext(nil, time.Time{})
			AddMissingBetaHeadersToContext(ctx, tt.req, tt.provider)

			var headers []string
			if extraHeaders, ok := ctx.Value(schemas.BifrostContextKeyExtraHeaders).(map[string][]string); ok {
				headers = extraHeaders[AnthropicBetaHeader]
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

func TestAddMissingBetaHeadersToContext_PassthroughWins(t *testing.T) {
	// When a same-prefix header is already set from passthrough, auto-injection should NOT add a second version.
	t.Run("passthrough_mcp_header_prevents_auto_inject", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(nil, time.Time{})
		// Simulate passthrough setting an old MCP header
		ctx.SetValue(schemas.BifrostContextKeyExtraHeaders, map[string][]string{
			"anthropic-beta": {AnthropicMCPClientBetaHeaderDeprecated},
		})
		// Request has MCP servers, which would normally auto-inject the new header
		req := &AnthropicMessageRequest{
			MCPServers: []AnthropicMCPServerV2{{URL: "http://example.com"}},
		}
		AddMissingBetaHeadersToContext(ctx, req, schemas.Anthropic)

		extraHeaders := ctx.Value(schemas.BifrostContextKeyExtraHeaders).(map[string][]string)
		betaHeaders := extraHeaders[AnthropicBetaHeader]
		// Should only have the old header, not both
		if len(betaHeaders) != 1 {
			t.Errorf("expected 1 header, got %d: %v", len(betaHeaders), betaHeaders)
		}
		if betaHeaders[0] != AnthropicMCPClientBetaHeaderDeprecated {
			t.Errorf("expected passthrough header %q, got %q", AnthropicMCPClientBetaHeaderDeprecated, betaHeaders[0])
		}
	})

	t.Run("passthrough_computer_use_header_prevents_auto_inject", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(nil, time.Time{})
		// Simulate passthrough setting an older computer-use header
		ctx.SetValue(schemas.BifrostContextKeyExtraHeaders, map[string][]string{
			"anthropic-beta": {AnthropicComputerUseBetaHeader20250124},
		})
		req := &AnthropicMessageRequest{
			Tools: []AnthropicTool{{
				Type: schemas.Ptr(AnthropicToolTypeComputer20251124),
				Name: string(AnthropicToolNameComputer),
			}},
		}
		AddMissingBetaHeadersToContext(ctx, req, schemas.Anthropic)

		extraHeaders := ctx.Value(schemas.BifrostContextKeyExtraHeaders).(map[string][]string)
		betaHeaders := extraHeaders[AnthropicBetaHeader]
		if len(betaHeaders) != 1 {
			t.Errorf("expected 1 header, got %d: %v", len(betaHeaders), betaHeaders)
		}
		if betaHeaders[0] != AnthropicComputerUseBetaHeader20250124 {
			t.Errorf("expected passthrough header %q, got %q", AnthropicComputerUseBetaHeader20250124, betaHeaders[0])
		}
	})

	t.Run("no_passthrough_allows_auto_inject", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(nil, time.Time{})
		req := &AnthropicMessageRequest{
			MCPServers: []AnthropicMCPServerV2{{URL: "http://example.com"}},
		}
		AddMissingBetaHeadersToContext(ctx, req, schemas.Anthropic)

		extraHeaders := ctx.Value(schemas.BifrostContextKeyExtraHeaders).(map[string][]string)
		betaHeaders := extraHeaders[AnthropicBetaHeader]
		if len(betaHeaders) != 1 || betaHeaders[0] != AnthropicMCPClientBetaHeader {
			t.Errorf("expected [%q], got %v", AnthropicMCPClientBetaHeader, betaHeaders)
		}
	})
}

func TestMergeBetaHeaders(t *testing.T) {
	t.Run("context_extra_headers_case_insensitive_key", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
		ctx.SetValue(schemas.BifrostContextKeyExtraHeaders, map[string][]string{
			"Anthropic-Beta": {"structured-outputs-2025-11-13"},
		})
		got := MergeBetaHeaders(nil, ctx)
		want := []string{"structured-outputs-2025-11-13"}
		if !slices.Equal(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("provider_extra_headers_case_insensitive_key", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
		got := MergeBetaHeaders(map[string]string{
			"Anthropic-Beta": "mcp-client-2025-04-04",
		}, ctx)
		want := []string{"mcp-client-2025-04-04"}
		if !slices.Equal(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("merges_provider_then_context_deduping_tokens", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
		ctx.SetValue(schemas.BifrostContextKeyExtraHeaders, map[string][]string{
			"ANTHROPIC-BETA": {"foo,bar", "bar,baz"},
		})
		got := MergeBetaHeaders(map[string]string{
			"anthropic-beta": "foo",
		}, ctx)
		sort.Strings(got)
		wantSorted := []string{"bar", "baz", "foo"}
		if !slices.Equal(got, wantSorted) {
			t.Fatalf("got %v, want %v", got, wantSorted)
		}
	})
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
		AnthropicInterleavedThinkingBetaHeader,
		AnthropicSkillsBetaHeader,
		AnthropicContext1MBetaHeader,
		AnthropicFastModeBetaHeader,
		AnthropicRedactThinkingBetaHeader,
	}

	containsHeader := func(result []string, h string) bool {
		for _, r := range result {
			if r == h {
				return true
			}
		}
		return false
	}

	t.Run("Anthropic/keeps_all_headers", func(t *testing.T) {
		result := FilterBetaHeadersForProvider(allHeaders, schemas.Anthropic)
		for _, h := range allHeaders {
			if !containsHeader(result, h) {
				t.Errorf("expected header %q to be kept for Anthropic, got %v", h, result)
			}
		}
	})

	t.Run("Vertex/drops_unsupported_headers", func(t *testing.T) {
		unsupported := []string{
			AnthropicStructuredOutputsBetaHeader,
			AnthropicMCPClientBetaHeader,
			AnthropicPromptCachingScopeBetaHeader,
			AnthropicAdvancedToolUseBetaHeader,
			AnthropicFilesAPIBetaHeader,
			AnthropicSkillsBetaHeader,
			AnthropicFastModeBetaHeader,
			AnthropicRedactThinkingBetaHeader,
		}
		for _, h := range unsupported {
			result := FilterBetaHeadersForProvider([]string{h}, schemas.Vertex)
			if len(result) != 0 {
				t.Errorf("expected header %q to be dropped for Vertex, got %v", h, result)
			}
		}
	})

	t.Run("Vertex/keeps_supported_headers", func(t *testing.T) {
		supported := []string{
			AnthropicComputerUseBetaHeader20251124,
			AnthropicCompactionBetaHeader,
			AnthropicContextManagementBetaHeader,
			AnthropicInterleavedThinkingBetaHeader,
			AnthropicContext1MBetaHeader,
		}
		result := FilterBetaHeadersForProvider(supported, schemas.Vertex)
		if len(result) != len(supported) {
			t.Errorf("expected %d headers, got %d: %v", len(supported), len(result), result)
		}
	})

	t.Run("Bedrock/drops_unsupported_headers", func(t *testing.T) {
		unsupported := []string{
			AnthropicMCPClientBetaHeader,
			AnthropicPromptCachingScopeBetaHeader,
			AnthropicAdvancedToolUseBetaHeader,
			AnthropicFilesAPIBetaHeader,
			AnthropicSkillsBetaHeader,
			AnthropicFastModeBetaHeader,
			AnthropicRedactThinkingBetaHeader,
		}
		for _, h := range unsupported {
			result := FilterBetaHeadersForProvider([]string{h}, schemas.Bedrock)
			if len(result) != 0 {
				t.Errorf("expected header %q to be dropped for Bedrock, got %v", h, result)
			}
		}
	})

	t.Run("Azure/drops_unsupported_headers", func(t *testing.T) {
		unsupported := []string{
			AnthropicFastModeBetaHeader,
		}
		for _, h := range unsupported {
			result := FilterBetaHeadersForProvider([]string{h}, schemas.Azure)
			if len(result) != 0 {
				t.Errorf("expected header %q to be dropped for Azure, got %v", h, result)
			}
		}
	})

	t.Run("Azure/keeps_supported_headers", func(t *testing.T) {
		supported := []string{
			AnthropicComputerUseBetaHeader20251124,
			AnthropicStructuredOutputsBetaHeader,
			AnthropicMCPClientBetaHeader,
			AnthropicPromptCachingScopeBetaHeader,
			AnthropicCompactionBetaHeader,
			AnthropicContextManagementBetaHeader,
			AnthropicAdvancedToolUseBetaHeader,
			AnthropicFilesAPIBetaHeader,
			AnthropicInterleavedThinkingBetaHeader,
			AnthropicSkillsBetaHeader,
			AnthropicContext1MBetaHeader,
			AnthropicRedactThinkingBetaHeader,
		}
		result := FilterBetaHeadersForProvider(supported, schemas.Azure)
		if len(result) != len(supported) {
			t.Errorf("expected %d headers, got %d: %v", len(supported), len(result), result)
		}
	})

	t.Run("Bedrock/keeps_supported_headers", func(t *testing.T) {
		supported := []string{
			AnthropicComputerUseBetaHeader20251124,
			AnthropicStructuredOutputsBetaHeader,
			AnthropicCompactionBetaHeader,
			AnthropicContextManagementBetaHeader,
			AnthropicInterleavedThinkingBetaHeader,
			AnthropicContext1MBetaHeader,
		}
		result := FilterBetaHeadersForProvider(supported, schemas.Bedrock)
		if len(result) != len(supported) {
			t.Errorf("expected %d headers, got %d: %v", len(supported), len(result), result)
		}
	})

	t.Run("unknown_headers_dropped_for_non_anthropic", func(t *testing.T) {
		result := FilterBetaHeadersForProvider([]string{"some-future-beta-2025"}, schemas.Vertex)
		if len(result) != 0 {
			t.Errorf("expected unknown header to be dropped for Vertex, got %v", result)
		}
	})

	t.Run("unknown_headers_forwarded_for_anthropic", func(t *testing.T) {
		headers := []string{"some-future-beta-2025"}
		result := FilterBetaHeadersForProvider(headers, schemas.Anthropic)
		if len(result) != len(headers) {
			t.Errorf("expected unknown header to be forwarded for Anthropic, got %v", result)
		}
	})

	t.Run("unknown_provider_allows_all", func(t *testing.T) {
		result := FilterBetaHeadersForProvider(allHeaders, schemas.ModelProvider("custom-provider"))
		if len(result) != len(allHeaders) {
			t.Errorf("expected all headers for unknown provider, got %v", result)
		}
	})

	t.Run("override_enables_unsupported_header", func(t *testing.T) {
		// redact-thinking is not supported on Vertex by default
		overrides := map[string]bool{AnthropicRedactThinkingBetaHeaderPrefix: true}
		result := FilterBetaHeadersForProvider([]string{AnthropicRedactThinkingBetaHeader}, schemas.Vertex, overrides)
		if len(result) != 1 || result[0] != AnthropicRedactThinkingBetaHeader {
			t.Errorf("expected override to allow header, got %v", result)
		}
	})

	t.Run("override_disables_supported_header", func(t *testing.T) {
		// compaction is supported on Vertex by default; override to false should drop it silently
		overrides := map[string]bool{"compact-": false}
		result := FilterBetaHeadersForProvider([]string{AnthropicCompactionBetaHeader}, schemas.Vertex, overrides)
		if len(result) != 0 {
			t.Errorf("expected override false to drop supported header, got %v", result)
		}
	})

	t.Run("override_nil_uses_defaults", func(t *testing.T) {
		// Passing nil overrides should behave identically to no overrides
		result := FilterBetaHeadersForProvider([]string{AnthropicCompactionBetaHeader}, schemas.Vertex, nil)
		if len(result) != 1 {
			t.Errorf("expected default behavior with nil overrides, got %v", result)
		}
	})

	// Custom override tests for all providers
	customOverrideProviders := []struct {
		provider                schemas.ModelProvider
		expectForwardNoOverride bool // unknown headers forwarded without override?
	}{
		{schemas.Anthropic, true},
		{schemas.Vertex, false},
		{schemas.Bedrock, false},
		{schemas.Azure, false},
	}

	for _, tc := range customOverrideProviders {
		tc := tc
		t.Run(fmt.Sprintf("%s/custom_override_enables_unknown_header", tc.provider), func(t *testing.T) {
			overrides := map[string]bool{"new-feature-": true}
			result := FilterBetaHeadersForProvider([]string{"new-feature-2026-01-01"}, tc.provider, overrides)
			if len(result) != 1 || result[0] != "new-feature-2026-01-01" {
				t.Errorf("expected custom override to allow header on %s, got %v", tc.provider, result)
			}
		})

		t.Run(fmt.Sprintf("%s/custom_override_disables_unknown_header", tc.provider), func(t *testing.T) {
			overrides := map[string]bool{"new-feature-": false}
			result := FilterBetaHeadersForProvider([]string{"new-feature-2026-01-01"}, tc.provider, overrides)
			if len(result) != 0 {
				t.Errorf("expected custom override false to drop header on %s, got %v", tc.provider, result)
			}
		})

		t.Run(fmt.Sprintf("%s/custom_override_no_match_still_handled_correctly", tc.provider), func(t *testing.T) {
			overrides := map[string]bool{"new-feature-": true}
			result := FilterBetaHeadersForProvider([]string{"other-thing-2026"}, tc.provider, overrides)
			if tc.expectForwardNoOverride {
				if len(result) != 1 {
					t.Errorf("expected unknown header forwarded to %s, got %v", tc.provider, result)
				}
			} else {
				if len(result) != 0 {
					t.Errorf("expected unknown header dropped for %s, got %v", tc.provider, result)
				}
			}
		})

		t.Run(fmt.Sprintf("%s/custom_override_with_multiple_prefixes", tc.provider), func(t *testing.T) {
			overrides := map[string]bool{
				"alpha-": true,
				"beta-":  false,
				"gamma-": true,
			}
			result := FilterBetaHeadersForProvider([]string{"alpha-2026-01"}, tc.provider, overrides)
			if len(result) != 1 {
				t.Errorf("expected alpha- allowed on %s, got %v", tc.provider, result)
			}
			result = FilterBetaHeadersForProvider([]string{"beta-2026-01"}, tc.provider, overrides)
			if len(result) != 0 {
				t.Errorf("expected beta- dropped on %s, got %v", tc.provider, result)
			}
			result = FilterBetaHeadersForProvider([]string{"gamma-2026-01"}, tc.provider, overrides)
			if len(result) != 1 {
				t.Errorf("expected gamma- allowed on %s, got %v", tc.provider, result)
			}
		})
	}
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

func TestAnthropicToolUnmarshalJSON_MCPToolset(t *testing.T) {
	t.Run("mcp_toolset is properly unmarshaled", func(t *testing.T) {
		data := []byte(`{
			"type": "mcp_toolset",
			"mcp_server_name": "example-mcp",
			"default_config": {"enabled": false},
			"configs": {
				"search_events": {"enabled": true},
				"create_event": {"enabled": true, "defer_loading": true}
			}
		}`)

		var tool AnthropicTool
		if err := sonic.Unmarshal(data, &tool); err != nil {
			t.Fatalf("unexpected unmarshal error: %v", err)
		}

		if tool.MCPToolset == nil {
			t.Fatal("expected MCPToolset to be populated, got nil")
		}
		if tool.MCPToolset.Type != "mcp_toolset" {
			t.Errorf("expected type 'mcp_toolset', got %q", tool.MCPToolset.Type)
		}
		if tool.MCPToolset.MCPServerName != "example-mcp" {
			t.Errorf("expected mcp_server_name 'example-mcp', got %q", tool.MCPToolset.MCPServerName)
		}
		if tool.MCPToolset.DefaultConfig == nil || tool.MCPToolset.DefaultConfig.Enabled == nil || *tool.MCPToolset.DefaultConfig.Enabled != false {
			t.Error("expected default_config.enabled to be false")
		}
		if len(tool.MCPToolset.Configs) != 2 {
			t.Fatalf("expected 2 configs, got %d", len(tool.MCPToolset.Configs))
		}
		if tool.MCPToolset.Configs["search_events"] == nil || *tool.MCPToolset.Configs["search_events"].Enabled != true {
			t.Error("expected search_events to be enabled")
		}
		if tool.MCPToolset.Configs["create_event"] == nil || tool.MCPToolset.Configs["create_event"].DeferLoading == nil || *tool.MCPToolset.Configs["create_event"].DeferLoading != true {
			t.Error("expected create_event defer_loading to be true")
		}
	})

	t.Run("regular tool is not affected by mcp_toolset unmarshal", func(t *testing.T) {
		data := []byte(`{
			"name": "get_weather",
			"description": "Get weather info",
			"input_schema": {"type": "object", "properties": {}}
		}`)

		var tool AnthropicTool
		if err := sonic.Unmarshal(data, &tool); err != nil {
			t.Fatalf("unexpected unmarshal error: %v", err)
		}

		if tool.MCPToolset != nil {
			t.Error("expected MCPToolset to be nil for regular tool")
		}
		if tool.Name != "get_weather" {
			t.Errorf("expected name 'get_weather', got %q", tool.Name)
		}
	})

	t.Run("mcp_toolset round-trips through marshal/unmarshal", func(t *testing.T) {
		original := AnthropicTool{
			MCPToolset: &AnthropicMCPToolsetTool{
				Type:          "mcp_toolset",
				MCPServerName: "test-server",
				DefaultConfig: &AnthropicMCPToolsetConfig{Enabled: new(false)},
				Configs: map[string]*AnthropicMCPToolsetConfig{
					"tool_a": {Enabled: new(true)},
				},
			},
		}

		marshaled, err := json.Marshal(original)
		if err != nil {
			t.Fatalf("unexpected marshal error: %v", err)
		}

		var restored AnthropicTool
		if err := sonic.Unmarshal(marshaled, &restored); err != nil {
			t.Fatalf("unexpected unmarshal error: %v", err)
		}

		if restored.MCPToolset == nil {
			t.Fatal("expected MCPToolset to be populated after round-trip")
		}
		if restored.MCPToolset.MCPServerName != "test-server" {
			t.Errorf("expected mcp_server_name 'test-server', got %q", restored.MCPToolset.MCPServerName)
		}
		if len(restored.MCPToolset.Configs) != 1 {
			t.Fatalf("expected 1 config, got %d", len(restored.MCPToolset.Configs))
		}
	})

	t.Run("tools array with mixed regular and mcp_toolset tools", func(t *testing.T) {
		data := []byte(`[
			{"name": "get_weather", "description": "Get weather"},
			{"type": "mcp_toolset", "mcp_server_name": "my-mcp"},
			{"type": "computer_20251124", "name": "computer"}
		]`)

		var tools []AnthropicTool
		if err := sonic.Unmarshal(data, &tools); err != nil {
			t.Fatalf("unexpected unmarshal error: %v", err)
		}

		if len(tools) != 3 {
			t.Fatalf("expected 3 tools, got %d", len(tools))
		}

		// First: regular tool
		if tools[0].Name != "get_weather" {
			t.Errorf("expected first tool name 'get_weather', got %q", tools[0].Name)
		}
		if tools[0].MCPToolset != nil {
			t.Error("expected first tool MCPToolset to be nil")
		}

		// Second: mcp_toolset
		if tools[1].MCPToolset == nil {
			t.Fatal("expected second tool MCPToolset to be populated")
		}
		if tools[1].MCPToolset.MCPServerName != "my-mcp" {
			t.Errorf("expected mcp_server_name 'my-mcp', got %q", tools[1].MCPToolset.MCPServerName)
		}

		// Third: typed tool (computer)
		if tools[2].MCPToolset != nil {
			t.Error("expected third tool MCPToolset to be nil")
		}
	})
}

func TestApplyMCPToolsetConfigToBifrostTool(t *testing.T) {
	t.Run("allowlist pattern merges correctly", func(t *testing.T) {
		bifrostTool := &schemas.ResponsesTool{
			Type: schemas.ResponsesToolTypeMCP,
			ResponsesToolMCP: &schemas.ResponsesToolMCP{
				ServerLabel: "test-server",
				ServerURL:   schemas.Ptr("https://example.com/mcp"),
			},
		}

		toolset := &AnthropicMCPToolsetTool{
			Type:          "mcp_toolset",
			MCPServerName: "test-server",
			DefaultConfig: &AnthropicMCPToolsetConfig{Enabled: schemas.Ptr(false)},
			Configs: map[string]*AnthropicMCPToolsetConfig{
				"search": {Enabled: new(true)},
				"create": {Enabled: schemas.Ptr(true)},
				"delete": {Enabled: schemas.Ptr(false)},
			},
		}

		applyMCPToolsetConfigToBifrostTool(bifrostTool, toolset)

		if bifrostTool.ResponsesToolMCP.AllowedTools == nil {
			t.Fatal("expected AllowedTools to be set")
		}
		allowedNames := bifrostTool.ResponsesToolMCP.AllowedTools.ToolNames
		if len(allowedNames) != 2 {
			t.Fatalf("expected 2 allowed tools, got %d: %v", len(allowedNames), allowedNames)
		}
		// Check that both "search" and "create" are present (order may vary due to map iteration)
		found := map[string]bool{}
		for _, name := range allowedNames {
			found[name] = true
		}
		if !found["search"] || !found["create"] {
			t.Errorf("expected allowed tools to contain 'search' and 'create', got %v", allowedNames)
		}
	})

	t.Run("all enabled by default does not set allowlist", func(t *testing.T) {
		bifrostTool := &schemas.ResponsesTool{
			Type: schemas.ResponsesToolTypeMCP,
			ResponsesToolMCP: &schemas.ResponsesToolMCP{
				ServerLabel: "test-server",
			},
		}

		toolset := &AnthropicMCPToolsetTool{
			Type:          "mcp_toolset",
			MCPServerName: "test-server",
			// No default_config (defaults to enabled=true)
		}

		applyMCPToolsetConfigToBifrostTool(bifrostTool, toolset)

		if bifrostTool.ResponsesToolMCP.AllowedTools != nil {
			t.Error("expected AllowedTools to be nil when all tools are enabled by default")
		}
	})

	t.Run("nil inputs are handled safely", func(t *testing.T) {
		// Should not panic
		applyMCPToolsetConfigToBifrostTool(nil, nil)
		applyMCPToolsetConfigToBifrostTool(&schemas.ResponsesTool{}, nil)
	})
}
