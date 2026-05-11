package gemini

import (
	"encoding/json"
	"testing"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestConvertPropertyToSchema_UnionType verifies that JSON Schema union types
// (e.g. "type": ["integer", "null"]) in tool parameter properties are correctly
// normalized to Gemini-compatible schemas. Gemini/Vertex AI rejects array-typed
// type fields with "schema didn't specify the schema type field".
func TestConvertPropertyToSchema_UnionType(t *testing.T) {
	tests := []struct {
		name           string
		propJSON       string
		wantType       Type
		wantNullable   *bool
		wantAnyOfLen   int
		wantAnyOfTypes []Type // optional: ordered types expected inside AnyOf
	}{
		{
			// Case A — simple string type: must be unchanged (no regression)
			name:     "plain string type is unchanged",
			propJSON: `{"type": "integer"}`,
			wantType: Type("integer"),
		},
		{
			// Case B — ["integer","null"]: single non-null + null → Type + Nullable
			name:         "integer null — becomes Type+Nullable",
			propJSON:     `{"type": ["integer", "null"], "description": "Timeout in seconds"}`,
			wantType:     Type("integer"),
			wantNullable: boolPtr(true),
		},
		{
			// Case B — ["string","null"]: same as above for string
			name:         "string null — becomes Type+Nullable",
			propJSON:     `{"type": ["string", "null"]}`,
			wantType:     Type("string"),
			wantNullable: boolPtr(true),
		},
		{
			// Case B — null-first ordering must not matter
			name:         "null first order should not matter",
			propJSON:     `{"type": ["null", "string"]}`,
			wantType:     Type("string"),
			wantNullable: boolPtr(true),
		},
		{
			// Case C — ["integer","string"]: multiple non-null types → anyOf, no Nullable
			name:           "multiple non-null types become anyOf",
			propJSON:       `{"type": ["integer", "string"]}`,
			wantType:       Type(""),
			wantAnyOfLen:   2,
			wantAnyOfTypes: []Type{Type("integer"), Type("string")},
		},
		{
			// Case D — ["integer","string","null"]: multiple non-null + null → anyOf with null branch
			name:           "multiple non-null types with null become anyOf with null branch",
			propJSON:       `{"type": ["integer", "string", "null"]}`,
			wantType:       Type(""),
			wantAnyOfLen:   3,
			wantAnyOfTypes: []Type{Type("integer"), Type("string"), Type("null")},
		},
		{
			name:           "explicit nullable with anyOf is folded into null branch",
			propJSON:       `{"anyOf": [{"type": "integer"}, {"type": "string"}], "nullable": true}`,
			wantType:       Type(""),
			wantAnyOfLen:   3,
			wantAnyOfTypes: []Type{Type("integer"), Type("string"), Type("null")},
		},
		{
			// Case E — ["null"] only: edge case, must produce TypeNULL not empty
			name:     "only null type becomes TypeNULL",
			propJSON: `{"type": ["null"]}`,
			wantType: TypeNULL,
		},
		{
			// Dedup — duplicate non-null types must not produce duplicate anyOf entries
			name:         "duplicate types are deduplicated",
			propJSON:     `{"type": ["integer", "integer", "null"]}`,
			wantType:     Type("integer"),
			wantNullable: boolPtr(true),
			wantAnyOfLen: 0, // single non-null after dedup → Type+Nullable, not anyOf
		},
		{
			// All-invalid elements ([1,2] after JSON decode becomes []interface{}{float64,float64}).
			// No usable type strings at all — Type must remain empty, NOT TypeNULL.
			name:     "all-invalid non-string elements leave Type empty",
			propJSON: `{"type": [1, 2]}`,
			wantType: Type(""),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var rawProp interface{}
			require.NoError(t, json.Unmarshal([]byte(tc.propJSON), &rawProp))

			schema := convertPropertyToSchema(rawProp)
			require.NotNil(t, schema)

			assert.Equal(t, tc.wantType, schema.Type)
			assert.Equal(t, tc.wantNullable, schema.Nullable)
			if tc.wantAnyOfLen > 0 {
				require.Len(t, schema.AnyOf, tc.wantAnyOfLen)
				if len(tc.wantAnyOfTypes) > 0 {
					for i, wantT := range tc.wantAnyOfTypes {
						assert.Equal(t, wantT, schema.AnyOf[i].Type, "anyOf[%d].Type", i)
					}
				}
			} else {
				assert.Empty(t, schema.AnyOf, "AnyOf must be empty for simple type")
			}
		})
	}
}

// TestConvertBifrostToolsToGemini_UnionTypeProperty is the end-to-end test
// that reproduces the Goose+Vertex bug: a tool parameter with
// "type": ["integer", "null"] must produce a non-empty Gemini type field.
func TestConvertBifrostToolsToGemini_UnionTypeProperty(t *testing.T) {
	toolJSON := `{
		"type": "function",
		"function": {
			"name": "run_with_timeout",
			"description": "Run something with a timeout",
			"parameters": {
				"type": "object",
				"properties": {
					"timeout_secs": {
						"type": ["integer", "null"],
						"description": "Timeout in seconds"
					},
					"command": {
						"type": "string",
						"description": "Command to run"
					}
				},
				"required": ["command"]
			}
		}
	}`

	var chatTool schemas.ChatTool
	require.NoError(t, json.Unmarshal([]byte(toolJSON), &chatTool))

	geminiTools := convertBifrostToolsToGemini([]schemas.ChatTool{chatTool})
	require.Len(t, geminiTools, 1)
	require.Len(t, geminiTools[0].FunctionDeclarations, 1)

	fd := geminiTools[0].FunctionDeclarations[0]
	require.NotNil(t, fd.Parameters)

	timeoutSchema, ok := fd.Parameters.Properties["timeout_secs"]
	require.True(t, ok, "timeout_secs property must be present")

	// Before the fix this was "" — Vertex AI rejected with
	// "parameters.timeout_secs schema didn't specify the schema type field"
	assert.NotEmpty(t, timeoutSchema.Type, "Type must not be empty for union-typed property")
	assert.Equal(t, Type("integer"), timeoutSchema.Type)
	require.NotNil(t, timeoutSchema.Nullable)
	assert.True(t, *timeoutSchema.Nullable)

	// The non-union "command" property must be unaffected
	commandSchema, ok := fd.Parameters.Properties["command"]
	require.True(t, ok)
	assert.Equal(t, Type("string"), commandSchema.Type)
	assert.Nil(t, commandSchema.Nullable)
}

func boolPtr(b bool) *bool { return &b }

func TestConvertFunctionParametersToSchema_AnyOfNullable(t *testing.T) {
	params := schemas.ToolFunctionParameters{
		AnyOf: []schemas.OrderedMap{
			*schemas.NewOrderedMapFromPairs(schemas.KV("type", "integer")),
			*schemas.NewOrderedMapFromPairs(schemas.KV("type", "string")),
		},
		Nullable: boolPtr(true),
	}

	schema := convertFunctionParametersToSchema(params)
	require.NotNil(t, schema)
	require.Len(t, schema.AnyOf, 3)
	assert.Equal(t, Type("integer"), schema.AnyOf[0].Type)
	assert.Equal(t, Type("string"), schema.AnyOf[1].Type)
	assert.Equal(t, Type("null"), schema.AnyOf[2].Type)
	assert.Nil(t, schema.Nullable)

	wire, err := providerUtils.MarshalSorted(schema)
	require.NoError(t, err)
	assert.Contains(t, string(wire), `"anyOf":[{"type":"integer"},{"type":"string"},{"type":"null"}]`)
	assert.NotContains(t, string(wire), `"nullable":true`)
}

// TestExtractUnionTypes directly tests the extractUnionTypes helper for both
// []interface{} and []string inputs.
func TestExtractUnionTypes(t *testing.T) {
	tests := []struct {
		name        string
		input       interface{}
		wantNonNull []string
		wantHasNull bool
	}{
		{
			name:        "[]interface{} integer+null",
			input:       []interface{}{"integer", "null"},
			wantNonNull: []string{"integer"},
			wantHasNull: true,
		},
		{
			name:        "[]string integer+null",
			input:       []string{"integer", "null"},
			wantNonNull: []string{"integer"},
			wantHasNull: true,
		},
		{
			name:        "[]string dedup",
			input:       []string{"string", "string", "null"},
			wantNonNull: []string{"string"},
			wantHasNull: true,
		},
		{
			name:        "[]interface{} all-invalid non-string elements",
			input:       []interface{}{float64(1), float64(2)},
			wantNonNull: nil,
			wantHasNull: false,
		},
		{
			name:        "[]string null-only",
			input:       []string{"null"},
			wantNonNull: nil,
			wantHasNull: true,
		},
		{
			name:        "[]interface{} multi-type without null",
			input:       []interface{}{"integer", "string"},
			wantNonNull: []string{"integer", "string"},
			wantHasNull: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			nonNull, hasNull := extractUnionTypes(tc.input)
			assert.Equal(t, tc.wantNonNull, nonNull)
			assert.Equal(t, tc.wantHasNull, hasNull)
		})
	}
}

// TestConvertPropertyToSchema_StringSlice verifies that a Go caller passing
// []string{"integer","null"} (rather than the JSON-decoded []interface{} form)
// is also handled correctly.
func TestConvertPropertyToSchema_StringSlice(t *testing.T) {
	// Build the prop map directly as a Go caller would.
	prop := map[string]interface{}{
		"type":        []string{"integer", "null"},
		"description": "direct Go caller path",
	}
	schema := convertPropertyToSchema(prop)
	require.NotNil(t, schema)
	assert.Equal(t, Type("integer"), schema.Type)
	require.NotNil(t, schema.Nullable)
	assert.True(t, *schema.Nullable)
}

// TestConvertBifrostToolsToGemini_WirePayload verifies that the final
// serialized JSON bytes sent to Gemini/Vertex are correct for union-typed
// tool parameters. The original bug manifested at the serialization level
// (empty "type" field rejected by Vertex), so struct-level checks alone
// are not sufficient.
func TestConvertBifrostToolsToGemini_WirePayload(t *testing.T) {
	tests := []struct {
		name         string
		propertyJSON string
		propertyName string
		wantContains []string // substrings that must appear in the wire JSON
		wantAbsent   []string // substrings that must NOT appear in the wire JSON
	}{
		{
			name:         "nullable type produces type+nullable fields not array",
			propertyJSON: `"timeout_secs":{"type":["integer","null"],"description":"Timeout"}`,
			propertyName: "timeout_secs",
			// Vertex requires a single string "type"; array would be rejected
			wantContains: []string{`"type":"integer"`, `"nullable":true`},
			wantAbsent:   []string{`"type":["integer"`, `"type":["null"`},
		},
		{
			name:         "plain string type passes through unchanged",
			propertyJSON: `"command":{"type":"string","description":"Command to run"}`,
			propertyName: "command",
			wantContains: []string{`"type":"string"`},
			wantAbsent:   []string{`"nullable"`, `"anyOf"`},
		},
		{
			name:         "multi-type union produces anyOf not array type",
			propertyJSON: `"value":{"type":["integer","string"]}`,
			propertyName: "value",
			wantContains: []string{`"anyOf":[{"type":"integer"},{"type":"string"}]`},
			wantAbsent:   []string{`"type":["integer"`, `"type":["string"`},
		},
		{
			name:         "multi-type nullable union folds null into anyOf",
			propertyJSON: `"value":{"type":["integer","string","null"]}`,
			propertyName: "value",
			wantContains: []string{`"anyOf":[{"type":"integer"},{"type":"string"},{"type":"null"}]`},
			wantAbsent:   []string{`"type":["integer"`, `"nullable":true`},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			toolJSON := `{"type":"function","function":{"name":"test_fn","parameters":{"type":"object","properties":{` +
				tc.propertyJSON + `}}}}`

			var chatTool schemas.ChatTool
			require.NoError(t, json.Unmarshal([]byte(toolJSON), &chatTool))

			geminiTools := convertBifrostToolsToGemini([]schemas.ChatTool{chatTool})
			require.Len(t, geminiTools, 1)

			// Serialize to the exact bytes that would be sent to Vertex
			wire, err := providerUtils.MarshalSorted(geminiTools[0])
			require.NoError(t, err)
			wireStr := string(wire)

			for _, want := range tc.wantContains {
				assert.Contains(t, wireStr, want, "wire JSON must contain %q", want)
			}
			for _, absent := range tc.wantAbsent {
				assert.NotContains(t, wireStr, absent, "wire JSON must not contain %q", absent)
			}
		})
	}
}
