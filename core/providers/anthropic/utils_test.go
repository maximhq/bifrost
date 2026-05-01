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
		// Fast mode tests — fast mode is Opus 4.6 only (research preview),
		// so tests must set Model to exercise the path. Non-Opus-4.6 models
		// are model-gated out regardless of provider flag.
		{
			name:     "Anthropic gets fast mode header",
			provider: schemas.Anthropic,
			req: &AnthropicMessageRequest{
				Model: "claude-opus-4-6",
				Speed: schemas.Ptr("fast"),
			},
			expectHeaders: []string{AnthropicFastModeBetaHeader},
		},
		{
			name:     "Anthropic skips fast mode header on non-Opus-4.6 model",
			provider: schemas.Anthropic,
			req: &AnthropicMessageRequest{
				Model: "claude-sonnet-4-6",
				Speed: schemas.Ptr("fast"),
			},
			unexpectHeaders: []string{AnthropicFastModeBetaHeader},
		},
		{
			name:     "Bedrock skips fast mode header",
			provider: schemas.Bedrock,
			req: &AnthropicMessageRequest{
				Model: "claude-opus-4-6", // fast mode is model-gated; set a supporting model so the test actually exercises provider suppression
				Speed: schemas.Ptr("fast"),
			},
			unexpectHeaders: []string{AnthropicFastModeBetaHeader},
		},
		{
			name:     "Azure skips fast mode header",
			provider: schemas.Azure,
			req: &AnthropicMessageRequest{
				Model: "claude-opus-4-6", // fast mode is model-gated; set a supporting model so the test actually exercises provider suppression
				Speed: schemas.Ptr("fast"),
			},
			unexpectHeaders: []string{AnthropicFastModeBetaHeader},
		},
		// Fine-grained tool streaming (eager_input_streaming) — per Table 20:
		// GA on Anthropic / Bedrock / Vertex, Beta on Azure. All four should
		// auto-inject fine-grained-tool-streaming-2025-05-14 when a tool has
		// eager_input_streaming: true.
		{
			name:     "Anthropic gets eager_input_streaming header",
			provider: schemas.Anthropic,
			req: &AnthropicMessageRequest{
				Tools: []AnthropicTool{{Name: "t1", EagerInputStreaming: schemas.Ptr(true)}},
			},
			expectHeaders: []string{AnthropicEagerInputStreamingBetaHeader},
		},
		{
			name:     "Bedrock gets eager_input_streaming header",
			provider: schemas.Bedrock,
			req: &AnthropicMessageRequest{
				Tools: []AnthropicTool{{Name: "t1", EagerInputStreaming: schemas.Ptr(true)}},
			},
			expectHeaders: []string{AnthropicEagerInputStreamingBetaHeader},
		},
		{
			name:     "Vertex gets eager_input_streaming header",
			provider: schemas.Vertex,
			req: &AnthropicMessageRequest{
				Tools: []AnthropicTool{{Name: "t1", EagerInputStreaming: schemas.Ptr(true)}},
			},
			expectHeaders: []string{AnthropicEagerInputStreamingBetaHeader},
		},
		{
			name:     "Azure gets eager_input_streaming header",
			provider: schemas.Azure,
			req: &AnthropicMessageRequest{
				Tools: []AnthropicTool{{Name: "t1", EagerInputStreaming: schemas.Ptr(true)}},
			},
			expectHeaders: []string{AnthropicEagerInputStreamingBetaHeader},
		},
		{
			name:     "eager_input_streaming header absent when flag is false",
			provider: schemas.Anthropic,
			req: &AnthropicMessageRequest{
				Tools: []AnthropicTool{{Name: "t1", EagerInputStreaming: schemas.Ptr(false)}},
			},
			unexpectHeaders: []string{AnthropicEagerInputStreamingBetaHeader},
		},
		{
			name:     "eager_input_streaming header absent when unset",
			provider: schemas.Anthropic,
			req: &AnthropicMessageRequest{
				Tools: []AnthropicTool{{Name: "t1"}},
			},
			unexpectHeaders: []string{AnthropicEagerInputStreamingBetaHeader},
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
		got := MergeBetaHeaders(ctx, nil)
		want := []string{"structured-outputs-2025-11-13"}
		if !slices.Equal(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("provider_extra_headers_case_insensitive_key", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
		got := MergeBetaHeaders(ctx, map[string]string{
			"Anthropic-Beta": "mcp-client-2025-04-04",
		})
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
		got := MergeBetaHeaders(ctx, map[string]string{
			"anthropic-beta": "foo",
		})
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
			AnthropicEagerInputStreamingBetaHeader,
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
			AnthropicEagerInputStreamingBetaHeader,
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
			AnthropicEagerInputStreamingBetaHeader,
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

// TestNetworkConfigBetaOverridesFlow proves the production sequence
//
//	FilterBetaHeadersForProvider(MergeBetaHeaders(ctx, networkConfig.ExtraHeaders), provider, networkConfig.BetaHeaderOverrides)
//
// honours operator-configured BetaHeaderOverrides for each Anthropic-compatible provider.
// This is the exact call sequence used at anthropic.go:205, vertex.go:407,
// bedrock.go:208, and azure.go:259 — the wire layer where headers are set on the outbound request.
func TestNetworkConfigBetaOverridesFlow(t *testing.T) {
	type pCase struct {
		provider            schemas.ModelProvider
		droppedByDefault    string
		droppedByDefaultPfx string
		allowedByDefault    string
		allowedByDefaultPfx string
	}
	cases := []pCase{
		{schemas.Anthropic, "interleaved-thinking-2025-05-14", AnthropicInterleavedThinkingBetaHeaderPrefix,
			"prompt-caching-2024-07-31", "prompt-caching-"},
		{schemas.Vertex, "mcp-client-2025-11-20", AnthropicMCPClientBetaHeaderPrefix,
			"interleaved-thinking-2025-05-14", AnthropicInterleavedThinkingBetaHeaderPrefix},
		{schemas.Bedrock, "files-api-2025-04-14", "files-api-",
			"context-management-2025-06-27", AnthropicContextManagementBetaHeaderPrefix},
		{schemas.Azure, "fast-mode-2026-02-01", AnthropicFastModeBetaHeaderPrefix,
			"context-management-2025-06-27", AnthropicContextManagementBetaHeaderPrefix},
	}

	for _, tc := range cases {
		tc := tc

		t.Run(string(tc.provider)+"/override_enables_default_dropped", func(t *testing.T) {
			if tc.provider == schemas.Anthropic {
				t.Skip("Anthropic accepts all known betas by default")
			}
			ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
			ctx.SetValue(schemas.BifrostContextKeyExtraHeaders, map[string][]string{
				AnthropicBetaHeader: {tc.droppedByDefault},
			})
			overrides := map[string]bool{tc.droppedByDefaultPfx: true}
			got := FilterBetaHeadersForProvider(MergeBetaHeaders(ctx, nil), tc.provider, overrides)
			if len(got) != 1 || got[0] != tc.droppedByDefault {
				t.Fatalf("expected override to enable %q for %s, got %v", tc.droppedByDefault, tc.provider, got)
			}
		})

		t.Run(string(tc.provider)+"/override_disables_default_allowed", func(t *testing.T) {
			ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
			ctx.SetValue(schemas.BifrostContextKeyExtraHeaders, map[string][]string{
				AnthropicBetaHeader: {tc.allowedByDefault},
			})
			overrides := map[string]bool{tc.allowedByDefaultPfx: false}
			got := FilterBetaHeadersForProvider(MergeBetaHeaders(ctx, nil), tc.provider, overrides)
			if len(got) != 0 {
				t.Fatalf("expected override to disable %q for %s, got %v", tc.allowedByDefault, tc.provider, got)
			}
		})

		t.Run(string(tc.provider)+"/override_only_affects_targeted_prefix", func(t *testing.T) {
			const otherAllowed = "interleaved-thinking-2025-05-14"
			if tc.allowedByDefaultPfx == AnthropicInterleavedThinkingBetaHeaderPrefix {
				t.Skip("test fixture uses interleaved-thinking as the allowed beta")
			}
			ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
			ctx.SetValue(schemas.BifrostContextKeyExtraHeaders, map[string][]string{
				AnthropicBetaHeader: {tc.allowedByDefault + "," + otherAllowed},
			})
			overrides := map[string]bool{tc.allowedByDefaultPfx: false}
			got := FilterBetaHeadersForProvider(MergeBetaHeaders(ctx, nil), tc.provider, overrides)
			if len(got) != 1 || got[0] != otherAllowed {
				t.Fatalf("expected only %q to survive for %s, got %v", otherAllowed, tc.provider, got)
			}
		})

		t.Run(string(tc.provider)+"/override_works_through_merge_with_provider_extra_headers", func(t *testing.T) {
			ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
			ctx.SetValue(schemas.BifrostContextKeyExtraHeaders, map[string][]string{
				AnthropicBetaHeader: {tc.allowedByDefault},
			})
			providerExtra := map[string]string{
				AnthropicBetaHeader: tc.allowedByDefault,
			}
			overrides := map[string]bool{tc.allowedByDefaultPfx: false}
			got := FilterBetaHeadersForProvider(MergeBetaHeaders(ctx, providerExtra), tc.provider, overrides)
			if len(got) != 0 {
				t.Fatalf("expected override to drop %q from merged sources for %s, got %v", tc.allowedByDefault, tc.provider, got)
			}
		})
	}
}

func TestStripUnsupportedFieldsFromRawBody(t *testing.T) {
	t.Run("bedrock_strips_new_request_level_fields", func(t *testing.T) {
		// Raw body with every new typed field. Targeting Bedrock: speed (no FastMode),
		// inference_geo (no InferenceGeo), mcp_servers (no MCP), container.skills
		// (no Skills), top-level cache_control.scope (no PromptCachingScope),
		// output_config.task_budget (no TaskBudgets). All should be stripped.
		input := []byte(`{
			"model":"claude-opus-4-6",
			"speed":"fast",
			"inference_geo":"us-east-1",
			"mcp_servers":[{"type":"url","url":"https://example.com","name":"x"}],
			"container":{"id":"c-1","skills":[{"skill_id":"s","type":"anthropic"}]},
			"cache_control":{"type":"ephemeral","ttl":"5m","scope":"user"},
			"output_config":{"task_budget":{"type":"tokens","total":20000}}
		}`)
		result, err := StripUnsupportedFieldsFromRawBody(input, schemas.Bedrock, "claude-opus-4-6")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		for _, path := range []string{"speed", "inference_geo", "mcp_servers", "container", "cache_control.scope", "output_config.task_budget"} {
			if providerUtils.JSONFieldExists(result, path) {
				t.Errorf("expected %q to be stripped for Bedrock, got: %s", path, string(result))
			}
		}
		// Confirm non-scope cache_control fields are retained.
		if !providerUtils.JSONFieldExists(result, "cache_control.ttl") {
			t.Errorf("expected cache_control.ttl to survive, got: %s", string(result))
		}
	})

	t.Run("vertex_keeps_supported_context_management_edits", func(t *testing.T) {
		// Vertex now accepts context_management with compact (Compaction:true) and
		// clear_tool_uses/clear_thinking (ContextEditing:true) edits. Re-enabled
		// 2026-05-01 (see core/providers/anthropic/types.go:153-168).
		input := []byte(`{"model":"claude-sonnet-4-6","context_management":{"edits":[{"type":"` + string(ContextManagementEditTypeCompact) + `"},{"type":"` + string(ContextManagementEditTypeClearToolUses) + `"}]}}`)
		result, err := StripUnsupportedFieldsFromRawBody(input, schemas.Vertex, "claude-sonnet-4-6")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !providerUtils.JSONFieldExists(result, "context_management") {
			t.Errorf("expected context_management to be kept for Vertex, got: %s", string(result))
		}
	})

	t.Run("anthropic_keeps_context_management_per_edit_type", func(t *testing.T) {
		// Anthropic supports context_management; compact edits are kept, clear edits are also kept.
		input := []byte(`{"model":"claude-sonnet-4-6","context_management":{"edits":[{"type":"` + string(ContextManagementEditTypeCompact) + `"},{"type":"` + string(ContextManagementEditTypeClearToolUses) + `"}]}}`)
		result, err := StripUnsupportedFieldsFromRawBody(input, schemas.Anthropic, "claude-sonnet-4-6")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !providerUtils.JSONFieldExists(result, "context_management") {
			t.Errorf("expected context_management to be kept for Anthropic, got: %s", string(result))
		}
	})

	t.Run("vertex_strips_mcp_strict_and_input_examples_via_feature_check", func(t *testing.T) {
		// Vertex: no MCP, no InputExamples, no StructuredOutputs.
		// tool.strict stripped; tool.input_examples stripped; mcp_servers stripped.
		// tool.cache_control.scope stripped (Vertex has no PromptCachingScope).
		input := []byte(`{
			"model":"claude-sonnet-4-6",
			"mcp_servers":[{"type":"url","url":"u","name":"n"}],
			"tools":[{"name":"t1","strict":true,"input_examples":[{"input":{"a":1}}],"cache_control":{"type":"ephemeral","scope":"user"}}]
		}`)
		result, err := StripUnsupportedFieldsFromRawBody(input, schemas.Vertex, "claude-sonnet-4-6")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		for _, path := range []string{"mcp_servers", "tools.0.strict", "tools.0.input_examples", "tools.0.cache_control.scope"} {
			if providerUtils.JSONFieldExists(result, path) {
				t.Errorf("expected %q to be stripped for Vertex, got: %s", path, string(result))
			}
		}
		if !providerUtils.JSONFieldExists(result, "tools.0.name") {
			t.Errorf("expected tool name to survive")
		}
	})

	t.Run("bedrock_keeps_input_examples_via_standalone_flag", func(t *testing.T) {
		// Bedrock has InputExamples=true via tool-examples-2025-10-29 but
		// AdvancedToolUse=false. input_examples should be KEPT; defer_loading
		// and allowed_callers (bundle-only) should be STRIPPED.
		input := []byte(`{
			"model":"claude-opus-4-6",
			"tools":[{"name":"t1","input_examples":[{"input":{"a":1}}],"defer_loading":true,"allowed_callers":["direct"]}]
		}`)
		result, err := StripUnsupportedFieldsFromRawBody(input, schemas.Bedrock, "claude-opus-4-6")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !providerUtils.JSONFieldExists(result, "tools.0.input_examples") {
			t.Errorf("expected tools[0].input_examples to survive on Bedrock, got: %s", string(result))
		}
		for _, path := range []string{"tools.0.defer_loading", "tools.0.allowed_callers"} {
			if providerUtils.JSONFieldExists(result, path) {
				t.Errorf("expected %q to be stripped for Bedrock (AdvancedToolUse bundle unsupported), got: %s", path, string(result))
			}
		}
	})

	t.Run("speed_stripped_on_non_opus_46_even_on_anthropic", func(t *testing.T) {
		// Model gate: fast-mode is Opus 4.6 only per docs. Even on Anthropic
		// direct where FastMode=true, targeting a different model must strip.
		input := []byte(`{"model":"claude-sonnet-4-6","speed":"fast"}`)
		result, err := StripUnsupportedFieldsFromRawBody(input, schemas.Anthropic, "claude-sonnet-4-6")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if providerUtils.JSONFieldExists(result, "speed") {
			t.Errorf("expected speed stripped for non-Opus-4.6 model on Anthropic, got: %s", string(result))
		}
	})

	t.Run("anthropic_direct_is_noop", func(t *testing.T) {
		// Anthropic supports everything — body should survive untouched.
		input := []byte(`{"model":"claude-opus-4-6","speed":"fast","mcp_servers":[{"type":"url","url":"u","name":"n"}],"container":{"id":"c"},"tools":[{"name":"t","defer_loading":true,"input_examples":[{"input":{"a":1}}]}]}`)
		result, err := StripUnsupportedFieldsFromRawBody(input, schemas.Anthropic, "claude-opus-4-6")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		for _, path := range []string{"speed", "mcp_servers", "container", "tools.0.defer_loading", "tools.0.input_examples"} {
			if !providerUtils.JSONFieldExists(result, path) {
				t.Errorf("expected %q preserved on Anthropic direct, got: %s", path, string(result))
			}
		}
	})

	t.Run("nested_scope_stripped_on_messages_and_system", func(t *testing.T) {
		// Nested scope on system blocks and message blocks must also be stripped
		// when the provider lacks PromptCachingScope.
		input := []byte(`{
			"model":"claude-opus-4-6",
			"system":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral","scope":"user"}}],
			"messages":[{"role":"user","content":[{"type":"text","text":"q","cache_control":{"type":"ephemeral","scope":"global"}}]}]
		}`)
		result, err := StripUnsupportedFieldsFromRawBody(input, schemas.Bedrock, "claude-opus-4-6")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		for _, path := range []string{"system.0.cache_control.scope", "messages.0.content.0.cache_control.scope"} {
			if providerUtils.JSONFieldExists(result, path) {
				t.Errorf("expected nested %q stripped, got: %s", path, string(result))
			}
		}
	})

	t.Run("unknown_provider_is_safe_noop", func(t *testing.T) {
		input := []byte(`{"model":"claude-opus-4-6","speed":"fast"}`)
		result, err := StripUnsupportedFieldsFromRawBody(input, schemas.ModelProvider("custom"), "claude-opus-4-6")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !providerUtils.JSONFieldExists(result, "speed") {
			t.Errorf("expected speed preserved for unknown provider (safe default), got: %s", string(result))
		}
	})

	t.Run("container_empty_skills_stripped_but_container_preserved", func(t *testing.T) {
		// Skills=false provider (Bedrock), ContainerBasic=true.
		// skills:[] is a caller oversight — strip the empty key, preserve container.
		input := []byte(`{"model":"claude-opus-4-6","container":{"id":"c-1","skills":[]}}`)
		result, err := StripUnsupportedFieldsFromRawBody(input, schemas.Bedrock, "claude-opus-4-6")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if providerUtils.JSONFieldExists(result, "container.skills") {
			t.Errorf("expected empty container.skills stripped on Skills=false provider, got: %s", string(result))
		}
		if !providerUtils.JSONFieldExists(result, "container.id") {
			t.Errorf("expected container.id preserved (bare form still valid), got: %s", string(result))
		}
	})

	t.Run("container_nonempty_skills_drops_whole_container", func(t *testing.T) {
		// Non-empty skills signals caller intent; provider doesn't support — drop container.
		input := []byte(`{"model":"claude-opus-4-6","container":{"id":"c-1","skills":[{"skill_id":"s","type":"anthropic"}]}}`)
		result, err := StripUnsupportedFieldsFromRawBody(input, schemas.Bedrock, "claude-opus-4-6")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if providerUtils.JSONFieldExists(result, "container") {
			t.Errorf("expected whole container dropped for non-empty skills on Skills=false, got: %s", string(result))
		}
	})

	t.Run("container_empty_skills_on_skills_capable_provider_preserved", func(t *testing.T) {
		// On Anthropic direct (Skills=true), the empty skills array must be preserved
		// as-is — our strip logic only fires when !features.Skills.
		input := []byte(`{"model":"claude-opus-4-6","container":{"id":"c-1","skills":[]}}`)
		result, err := StripUnsupportedFieldsFromRawBody(input, schemas.Anthropic, "claude-opus-4-6")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !providerUtils.JSONFieldExists(result, "container.skills") {
			t.Errorf("expected container.skills preserved on Skills=true provider, got: %s", string(result))
		}
	})
}

// TestStripUnsupportedAnthropicFields_ContainerSkillsGating mirrors the raw-path
// tests above on the typed path — ensures the typed sanitizer treats explicit
// empty skills arrays as a stripable (not drop-triggering) signal.
func TestStripUnsupportedAnthropicFields_ContainerSkillsGating(t *testing.T) {
	t.Run("empty_skills_on_skills_false_provider_strips_skills_keeps_container", func(t *testing.T) {
		req := &AnthropicMessageRequest{
			Model: "claude-opus-4-6",
			Container: &AnthropicContainer{
				ContainerObject: &AnthropicContainerObject{
					ID:     schemas.Ptr("c-1"),
					Skills: []AnthropicContainerSkill{}, // explicit empty
				},
			},
		}
		stripUnsupportedAnthropicFields(req, schemas.Bedrock, "claude-opus-4-6")
		if req.Container == nil {
			t.Fatalf("expected container preserved (bare form valid with empty skills), got nil")
		}
		if req.Container.ContainerObject == nil || req.Container.ContainerObject.Skills != nil {
			t.Errorf("expected skills cleared on Skills=false, got %v", req.Container.ContainerObject)
		}
	})

	t.Run("nonempty_skills_on_skills_false_provider_drops_container", func(t *testing.T) {
		req := &AnthropicMessageRequest{
			Model: "claude-opus-4-6",
			Container: &AnthropicContainer{
				ContainerObject: &AnthropicContainerObject{
					ID:     schemas.Ptr("c-1"),
					Skills: []AnthropicContainerSkill{{SkillID: "s", Type: "anthropic"}},
				},
			},
		}
		stripUnsupportedAnthropicFields(req, schemas.Bedrock, "claude-opus-4-6")
		if req.Container != nil {
			t.Errorf("expected whole container dropped for non-empty skills on Skills=false, got %v", req.Container)
		}
	})

	t.Run("empty_skills_on_skills_true_provider_preserved", func(t *testing.T) {
		req := &AnthropicMessageRequest{
			Model: "claude-opus-4-6",
			Container: &AnthropicContainer{
				ContainerObject: &AnthropicContainerObject{
					ID:     schemas.Ptr("c-1"),
					Skills: []AnthropicContainerSkill{},
				},
			},
		}
		stripUnsupportedAnthropicFields(req, schemas.Anthropic, "claude-opus-4-6")
		if req.Container == nil || req.Container.ContainerObject == nil {
			t.Fatalf("expected container preserved on Skills=true provider, got %v", req.Container)
		}
		if req.Container.ContainerObject.Skills == nil {
			t.Errorf("expected empty skills preserved on Skills=true provider (not nilled)")
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
		// When only code_execution + web_search (newer version), strip code_execution, keep web_search
		// Note: web_search_20260209 auto-injects code_execution, so explicit code_execution is stripped
		input := []byte(`{"model":"test","tools":[{"type":"code_execution_20250825","name":"code_execution"},{"type":"web_search_20260209","name":"web_search"}]}`)
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

func TestGetRequestBodyForResponses_RawBodyStripsFallbacks(t *testing.T) {
	rawBody := []byte(`{"model":"claude-sonnet-4-5","max_tokens":1024,"messages":[{"role":"user","content":"hello"}],"fallbacks":["claude-haiku-4-5"],"temperature":0.7}`)

	ctx := schemas.NewBifrostContext(nil, time.Time{})
	ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)

	request := &schemas.BifrostResponsesRequest{
		Provider:       schemas.Anthropic,
		Model:          "claude-sonnet-4-5",
		RawRequestBody: rawBody,
	}

	result, bifrostErr := getRequestBodyForResponses(ctx, request, false, nil, false, false)
	if bifrostErr != nil {
		t.Fatalf("unexpected error: %v", bifrostErr)
	}

	if providerUtils.GetJSONField(result, "fallbacks").Exists() {
		t.Error("expected 'fallbacks' to be absent from raw-body output")
	}

	// Other fields must survive the round-trip
	if !providerUtils.GetJSONField(result, "model").Exists() {
		t.Error("expected 'model' to be present")
	}
	if !providerUtils.GetJSONField(result, "max_tokens").Exists() {
		t.Error("expected 'max_tokens' to be present")
	}
	if !providerUtils.GetJSONField(result, "temperature").Exists() {
		t.Error("expected 'temperature' to be present")
	}
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

func TestSupportsAdaptiveThinking(t *testing.T) {
	tests := []struct {
		model    string
		expected bool
	}{
		{"claude-opus-4-7-20260401", true},
		{"claude-opus-4.7-20260401", true},
		{"claude-opus-4-6-20250514", true},
		{"claude-opus-4.6-20250514", true},
		{"claude-sonnet-4-6-20250514", true},
		{"claude-sonnet-4.6-20250514", true},
		{"claude-opus-4-5-20241022", false},
		{"claude-sonnet-4-5-20241022", false},
		{"claude-haiku-4-6-20250514", false}, // haiku does not support adaptive
		{"claude-haiku-4-7-20260401", false}, // haiku, not opus
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			got := SupportsAdaptiveThinking(tt.model)
			if got != tt.expected {
				t.Errorf("SupportsAdaptiveThinking(%q) = %v, want %v", tt.model, got, tt.expected)
			}
		})
	}
}

func TestAddMissingBetaHeadersToContext_TaskBudgets(t *testing.T) {
	tests := []struct {
		name            string
		provider        schemas.ModelProvider
		req             *AnthropicMessageRequest
		expectHeaders   []string
		unexpectHeaders []string
	}{
		{
			name:     "Anthropic gets task-budgets header when task_budget set",
			provider: schemas.Anthropic,
			req: &AnthropicMessageRequest{
				OutputConfig: &AnthropicOutputConfig{
					TaskBudget: &AnthropicTaskBudget{Type: "tokens", Total: 50000},
				},
			},
			expectHeaders: []string{AnthropicTaskBudgetsBetaHeader},
		},
		{
			name:     "Vertex does not get task-budgets header when task_budget set",
			provider: schemas.Vertex,
			req: &AnthropicMessageRequest{
				OutputConfig: &AnthropicOutputConfig{
					TaskBudget: &AnthropicTaskBudget{Type: "tokens", Total: 50000},
				},
			},
			unexpectHeaders: []string{AnthropicTaskBudgetsBetaHeader},
		},
		{
			name:     "no task-budgets header when task_budget is nil",
			provider: schemas.Anthropic,
			req: &AnthropicMessageRequest{
				OutputConfig: &AnthropicOutputConfig{},
			},
			unexpectHeaders: []string{AnthropicTaskBudgetsBetaHeader},
		},
		{
			name:            "no task-budgets header when output_config is nil",
			provider:        schemas.Anthropic,
			req:             &AnthropicMessageRequest{},
			unexpectHeaders: []string{AnthropicTaskBudgetsBetaHeader},
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

// TestComputerUseGeneration verifies the (model -> generation) classifier
// covers every Claude model that Anthropic explicitly maps to a computer-use
// beta header version, plus the fallback for unknown / non-Claude models.
func TestComputerUseGeneration(t *testing.T) {
	cases := []struct {
		model string
		want  string
	}{
		{"claude-opus-4-7", ComputerUseGen20251124},
		{"claude-opus-4.7", ComputerUseGen20251124},
		{"Claude-Opus-4-7", ComputerUseGen20251124},
		{"claude-opus-4-7-20260321", ComputerUseGen20251124},
		{"claude-opus-4-6", ComputerUseGen20251124},
		{"claude-sonnet-4-6", ComputerUseGen20251124},
		{"claude-sonnet-4.6", ComputerUseGen20251124},
		{"claude-opus-4-5", ComputerUseGen20251124},
		{"claude-opus-4-5-20251101", ComputerUseGen20251124},
		{"claude-sonnet-4-5", ComputerUseGen20250124},
		{"claude-sonnet-4-5-20250929", ComputerUseGen20250124},
		{"claude-haiku-4-5", ComputerUseGen20250124},
		{"claude-haiku-4-5-20251001", ComputerUseGen20250124},
		{"claude-opus-4-1", ComputerUseGen20250124},
		{"claude-opus-4-1-20250805", ComputerUseGen20250124},
		{"claude-sonnet-4", ComputerUseGen20250124},
		{"claude-sonnet-4-20250514", ComputerUseGen20250124},
		{"claude-opus-4", ComputerUseGen20250124},
		{"claude-opus-4-20250514", ComputerUseGen20250124},
		{"claude-3-7-sonnet-20250219", ComputerUseGen20250124},
		{"claude-3-5-sonnet-20241022", ComputerUseGen20250124},
		{"", ComputerUseGen20250124},
		{"some-unknown-model", ComputerUseGen20250124},
		{"global.anthropic.claude-opus-4-7", ComputerUseGen20251124},
		{"global.anthropic.claude-sonnet-4-6", ComputerUseGen20251124},
		{"global.anthropic.claude-haiku-4-5-20251001-v1:0", ComputerUseGen20250124},
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			got := ComputerUseGeneration(tc.model)
			if got != tc.want {
				t.Errorf("ComputerUseGeneration(%q) = %q, want %q", tc.model, got, tc.want)
			}
		})
	}
}

// TestNormalizedToolSpec verifies the canonical {type, name} pair returned per
// (generation, base-tool) pair matches Anthropic's strict Pydantic validators.
func TestNormalizedToolSpec(t *testing.T) {
	cases := []struct {
		generation string
		baseTool   string
		wantType   string
		wantName   string
	}{
		{ComputerUseGen20251124, "computer", "computer_20251124", "computer"},
		{ComputerUseGen20251124, "text_editor", "text_editor_20250728", "str_replace_based_edit_tool"},
		{ComputerUseGen20251124, "bash", "bash_20250124", "bash"},
		{ComputerUseGen20250124, "computer", "computer_20250124", "computer"},
		{ComputerUseGen20250124, "text_editor", "text_editor_20250124", "str_replace_editor"},
		{ComputerUseGen20250124, "bash", "bash_20250124", "bash"},
		{ComputerUseGen20251124, "web_search", "", ""},
		{ComputerUseGen20250124, "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.generation+"/"+tc.baseTool, func(t *testing.T) {
			gotType, gotName := NormalizedToolSpec(tc.generation, tc.baseTool)
			if gotType != tc.wantType {
				t.Errorf("NormalizedToolSpec(%q, %q) type = %q, want %q", tc.generation, tc.baseTool, gotType, tc.wantType)
			}
			if gotName != tc.wantName {
				t.Errorf("NormalizedToolSpec(%q, %q) name = %q, want %q", tc.generation, tc.baseTool, gotName, tc.wantName)
			}
		})
	}
}

// TestRemapRawToolVersionsForProvider_NormalizesComputerUse covers the four
// permutations of (model generation, supplied tool generation):
//   - matched (no-op)
//   - mismatched (auto-corrects type AND name)
//
// for both directions, plus mixed-tool requests where only some tools need
// normalization.
func TestRemapRawToolVersionsForProvider_NormalizesComputerUse(t *testing.T) {
	type expectedTool struct {
		toolType string
		toolName string
	}
	cases := []struct {
		name      string
		model     string
		inputBody string
		expected  []expectedTool
	}{
		{
			name:  "sonnet-4-6 with new-gen tools (no-op)",
			model: "claude-sonnet-4-6",
			inputBody: `{"model":"claude-sonnet-4-6","tools":[
				{"type":"computer_20251124","name":"computer","display_width_px":1024,"display_height_px":768},
				{"type":"text_editor_20250728","name":"str_replace_based_edit_tool"},
				{"type":"bash_20250124","name":"bash"}
			]}`,
			expected: []expectedTool{
				{"computer_20251124", "computer"},
				{"text_editor_20250728", "str_replace_based_edit_tool"},
				{"bash_20250124", "bash"},
			},
		},
		{
			name:  "sonnet-4-5 with old-gen tools (no-op)",
			model: "claude-sonnet-4-5",
			inputBody: `{"model":"claude-sonnet-4-5","tools":[
				{"type":"computer_20250124","name":"computer","display_width_px":1024,"display_height_px":768},
				{"type":"text_editor_20250124","name":"str_replace_editor"},
				{"type":"bash_20250124","name":"bash"}
			]}`,
			expected: []expectedTool{
				{"computer_20250124", "computer"},
				{"text_editor_20250124", "str_replace_editor"},
				{"bash_20250124", "bash"},
			},
		},
		{
			name:  "sonnet-4-6 with old-gen tools auto-upgrades",
			model: "claude-sonnet-4-6",
			inputBody: `{"model":"claude-sonnet-4-6","tools":[
				{"type":"computer_20250124","name":"computer","display_width_px":1024,"display_height_px":768},
				{"type":"text_editor_20250124","name":"str_replace_editor"},
				{"type":"bash_20250124","name":"bash"}
			]}`,
			expected: []expectedTool{
				{"computer_20251124", "computer"},
				{"text_editor_20250728", "str_replace_based_edit_tool"},
				{"bash_20250124", "bash"},
			},
		},
		{
			name:  "sonnet-4-5 with new-gen tools auto-downgrades",
			model: "claude-sonnet-4-5",
			inputBody: `{"model":"claude-sonnet-4-5","tools":[
				{"type":"computer_20251124","name":"computer","display_width_px":1024,"display_height_px":768},
				{"type":"text_editor_20250728","name":"str_replace_based_edit_tool"},
				{"type":"bash_20250124","name":"bash"}
			]}`,
			expected: []expectedTool{
				{"computer_20250124", "computer"},
				{"text_editor_20250124", "str_replace_editor"},
				{"bash_20250124", "bash"},
			},
		},
		{
			name:  "opus-4-7 with old-gen text_editor mid-list (only that tool changes)",
			model: "claude-opus-4-7",
			inputBody: `{"model":"claude-opus-4-7","tools":[
				{"type":"web_search_20250305","name":"web_search","max_uses":3},
				{"type":"text_editor_20250124","name":"str_replace_editor"},
				{"type":"computer_20251124","name":"computer","display_width_px":1024,"display_height_px":768}
			]}`,
			expected: []expectedTool{
				{"web_search_20250305", "web_search"},
				{"text_editor_20250728", "str_replace_based_edit_tool"},
				{"computer_20251124", "computer"},
			},
		},
		{
			name:      "no tools array is a clean no-op",
			model:     "claude-sonnet-4-6",
			inputBody: `{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}]}`,
			expected:  nil,
		},
		{
			name:  "bedrock-style global. prefix model classifies correctly",
			model: "global.anthropic.claude-opus-4-7",
			inputBody: `{"model":"global.anthropic.claude-opus-4-7","tools":[
				{"type":"text_editor_20250124","name":"str_replace_editor"}
			]}`,
			expected: []expectedTool{
				{"text_editor_20250728", "str_replace_based_edit_tool"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := RemapRawToolVersionsForProvider([]byte(tc.inputBody), schemas.Anthropic, tc.model)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			toolsResult := providerUtils.GetJSONField(out, "tools")
			if tc.expected == nil {
				if toolsResult.Exists() && toolsResult.IsArray() && len(toolsResult.Array()) > 0 {
					t.Fatalf("expected no tools array, got %s", toolsResult.Raw)
				}
				return
			}
			tools := toolsResult.Array()
			if len(tools) != len(tc.expected) {
				t.Fatalf("got %d tools, want %d (body=%s)", len(tools), len(tc.expected), out)
			}
			for i, want := range tc.expected {
				gotType := tools[i].Get("type").String()
				gotName := tools[i].Get("name").String()
				if gotType != want.toolType {
					t.Errorf("tool[%d].type = %q, want %q (body=%s)", i, gotType, want.toolType, out)
				}
				if gotName != want.toolName {
					t.Errorf("tool[%d].name = %q, want %q (body=%s)", i, gotName, want.toolName, out)
				}
			}
		})
	}
}
