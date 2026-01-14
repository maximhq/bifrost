package anthropic

import (
	"reflect"
	"testing"

	"github.com/bytedance/sonic"
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
