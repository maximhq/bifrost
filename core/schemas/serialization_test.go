package schemas

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests use schemas.Marshal/Unmarshal (sonic) to verify round-trip
// behavior matches what the production pipeline actually does.

// --- ChatToolChoiceStruct ---

func TestSonic_ChatToolChoiceStruct_FunctionVariant(t *testing.T) {
	input := `{"type":"function","function":{"name":"AnswerResponseModel"}}`

	var s ChatToolChoiceStruct
	err := Unmarshal([]byte(input), &s)
	require.NoError(t, err)

	assert.Equal(t, ChatToolChoiceTypeFunction, s.Type)
	assert.NotNil(t, s.Function)
	assert.Equal(t, "AnswerResponseModel", s.Function.Name)
	assert.Nil(t, s.Custom, "Custom should be nil for function variant")
	assert.Nil(t, s.AllowedTools, "AllowedTools should be nil for function variant")

	output, err := Marshal(s)
	require.NoError(t, err)

	// Verify no extra fields
	assert.NotContains(t, string(output), `"custom"`)
	assert.NotContains(t, string(output), `"allowed_tools"`)

	// Verify type comes first
	typeIdx := strings.Index(string(output), `"type"`)
	funcIdx := strings.Index(string(output), `"function"`)
	assert.Greater(t, funcIdx, typeIdx, "type should come before function in output")
}

func TestSonic_ChatToolChoiceStruct_CustomVariant(t *testing.T) {
	input := `{"type":"custom","custom":{"name":"my_tool"}}`

	var s ChatToolChoiceStruct
	err := Unmarshal([]byte(input), &s)
	require.NoError(t, err)

	assert.Equal(t, ChatToolChoiceTypeCustom, s.Type)
	assert.NotNil(t, s.Custom)
	assert.Equal(t, "my_tool", s.Custom.Name)
	assert.Nil(t, s.Function, "Function should be nil for custom variant")
	assert.Nil(t, s.AllowedTools, "AllowedTools should be nil for custom variant")

	output, err := Marshal(s)
	require.NoError(t, err)

	assert.NotContains(t, string(output), `"function"`)
	assert.NotContains(t, string(output), `"allowed_tools"`)
}

func TestSonic_ChatToolChoiceStruct_AllowedToolsVariant(t *testing.T) {
	input := `{"type":"allowed_tools","allowed_tools":{"mode":"auto","tools":[{"type":"function","function":{"name":"search"}}]}}`

	var s ChatToolChoiceStruct
	err := Unmarshal([]byte(input), &s)
	require.NoError(t, err)

	assert.Equal(t, ChatToolChoiceTypeAllowedTools, s.Type)
	assert.NotNil(t, s.AllowedTools)
	assert.Equal(t, "auto", s.AllowedTools.Mode)
	assert.Nil(t, s.Function, "Function should be nil for allowed_tools variant")
	assert.Nil(t, s.Custom, "Custom should be nil for allowed_tools variant")

	output, err := Marshal(s)
	require.NoError(t, err)

	// Verify the top-level struct doesn't have "function" or "custom" as direct keys
	// (note: "function" does appear INSIDE the allowed_tools.tools array, which is expected)
	assert.NotContains(t, string(output), `"custom"`)
	// Check that "function" only appears inside the tools array, not as a top-level key
	outputStr := string(output)
	topLevelFuncIdx := strings.Index(outputStr, `{"type":"allowed_tools"`)
	require.NotEqual(t, -1, topLevelFuncIdx)
	// The output should start with {"type":"allowed_tools","allowed_tools":...}
	assert.True(t, strings.HasPrefix(outputStr, `{"type":"allowed_tools","allowed_tools":`),
		"output should only have type and allowed_tools keys, got: %s", outputStr)
}

func TestSonic_ChatToolChoice_UnionRoundTrip(t *testing.T) {
	// Test the ChatToolChoice union type (string or struct)
	tests := []struct {
		name  string
		input string
	}{
		{"string_auto", `"auto"`},
		{"string_none", `"none"`},
		{"string_required", `"required"`},
		{"struct_function", `{"type":"function","function":{"name":"my_func"}}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var tc ChatToolChoice
			err := Unmarshal([]byte(tt.input), &tc)
			require.NoError(t, err)

			output, err := Marshal(tc)
			require.NoError(t, err)

			if strings.HasPrefix(tt.input, `"`) {
				// String variant
				assert.Equal(t, tt.input, string(output))
			} else {
				// Struct variant - verify no extra fields
				assert.NotContains(t, string(output), `"custom"`)
				assert.NotContains(t, string(output), `"allowed_tools"`)
			}
		})
	}
}

// --- OrderedMap through sonic ---

func TestSonic_OrderedMap_PreservesKeyOrder(t *testing.T) {
	input := `{"answer":"string","chain_of_thought":"string","citations":"array","is_unanswered":"boolean"}`

	var om OrderedMap
	err := Unmarshal([]byte(input), &om)
	require.NoError(t, err)

	assert.Equal(t, []string{"answer", "chain_of_thought", "citations", "is_unanswered"}, om.Keys())

	output, err := Marshal(om)
	require.NoError(t, err)
	assert.Equal(t, input, string(output))
}

func TestSonic_OrderedMap_NestedPreservesOrder(t *testing.T) {
	input := `{"z_outer":{"b_inner":1,"a_inner":2},"a_outer":"simple"}`

	var om OrderedMap
	err := Unmarshal([]byte(input), &om)
	require.NoError(t, err)

	assert.Equal(t, []string{"z_outer", "a_outer"}, om.Keys())

	nested, ok := om.Get("z_outer")
	require.True(t, ok)
	nestedOM, ok := nested.(*OrderedMap)
	require.True(t, ok)
	assert.Equal(t, []string{"b_inner", "a_inner"}, nestedOM.Keys())

	output, err := Marshal(om)
	require.NoError(t, err)
	assert.Equal(t, input, string(output))
}

func TestSonic_EmbeddingStruct_PreservesFloat64Precision(t *testing.T) {
	const want = 0.12345678901234568

	var embedding EmbeddingStruct
	err := embedding.UnmarshalJSON([]byte(`[0.12345678901234568]`))
	require.NoError(t, err)

	require.Len(t, embedding.EmbeddingArray, 1)

	got := embedding.EmbeddingArray[0]
	assert.Equal(t, want, got)

	float32Rounded := float64(float32(want))
	assert.NotEqual(t, float32Rounded, got)

	marshaled, err := embedding.MarshalJSON()
	require.NoError(t, err)

	var roundTrip []float64
	err = Unmarshal(marshaled, &roundTrip)
	require.NoError(t, err)
	require.Len(t, roundTrip, 1)
	assert.Equal(t, math.Float64bits(got), math.Float64bits(roundTrip[0]))
}

// --- ToolFunctionParameters through sonic ---

func TestSonic_ToolFunctionParameters_PreservesPropertyOrder(t *testing.T) {
	input := `{"type":"object","properties":{"answer":{"type":"string"},"chain_of_thought":{"type":"string"},"citations":{"type":"array"},"is_unanswered":{"type":"boolean"}},"required":["answer"]}`

	var params ToolFunctionParameters
	err := Unmarshal([]byte(input), &params)
	require.NoError(t, err)

	require.NotNil(t, params.Properties)
	assert.Equal(t, []string{"answer", "chain_of_thought", "citations", "is_unanswered"}, params.Properties.Keys())

	output, err := Marshal(params)
	require.NoError(t, err)

	// Re-parse to check properties order
	var roundTripped ToolFunctionParameters
	err = Unmarshal(output, &roundTripped)
	require.NoError(t, err)
	assert.Equal(t, params.Properties.Keys(), roundTripped.Properties.Keys())
}

func TestSonic_ToolFunctionParameters_PreservesDefsPosition(t *testing.T) {
	// $defs at the TOP of the parameters object
	input := `{"$defs":{"Citation":{"type":"object","properties":{"url":{"type":"string"}}}},"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"]}`

	var params ToolFunctionParameters
	err := Unmarshal([]byte(input), &params)
	require.NoError(t, err)

	require.NotNil(t, params.Defs)

	output, err := Marshal(params)
	require.NoError(t, err)

	// Verify $defs comes first in output (as in input)
	keys := ExtractTopLevelKeyOrder(output)
	require.NotEmpty(t, keys)
	assert.Equal(t, "$defs", keys[0], "$defs should be first key in output, got keys: %v", keys)
}

func TestSonic_ToolFunctionParameters_FullSchemaRoundTrip(t *testing.T) {
	// A realistic tool schema with $defs at top, specific property order
	input := `{"$defs":{"Citation":{"type":"object","properties":{"url":{"type":"string"},"text":{"type":"string"}},"required":["url","text"]}},"properties":{"answer":{"type":"string","description":"The answer"},"chain_of_thought":{"type":"string","description":"Reasoning"},"citations":{"type":"array","items":{"$ref":"#/$defs/Citation"}},"is_unanswered":{"type":"boolean"}},"required":["answer","is_unanswered"],"type":"object"}`

	var params ToolFunctionParameters
	err := Unmarshal([]byte(input), &params)
	require.NoError(t, err)

	output, err := Marshal(params)
	require.NoError(t, err)

	// Verify top-level key order matches input
	inputKeys := ExtractTopLevelKeyOrder([]byte(input))
	outputKeys := ExtractTopLevelKeyOrder(output)
	assert.Equal(t, inputKeys, outputKeys, "top-level key order should be preserved")

	// Verify properties key order
	assert.Equal(t, []string{"answer", "chain_of_thought", "citations", "is_unanswered"}, params.Properties.Keys())
}

// --- ChatTool end-to-end through sonic ---

func TestSonic_ChatTool_ToolFunctionParametersPreservesOrder(t *testing.T) {
	// Test that ToolFunctionParameters within a ChatTool preserves order
	input := `{"type":"function","function":{"name":"AnswerResponseModel","parameters":{"$defs":{"Citation":{"type":"object"}},"type":"object","properties":{"answer":{"type":"string"},"chain_of_thought":{"type":"string"},"citations":{"type":"array"},"is_unanswered":{"type":"boolean"}},"required":["answer"]}}}`

	var tool ChatTool
	err := Unmarshal([]byte(input), &tool)
	require.NoError(t, err)

	require.NotNil(t, tool.Function)
	require.NotNil(t, tool.Function.Parameters)
	assert.Equal(t, []string{"answer", "chain_of_thought", "citations", "is_unanswered"}, tool.Function.Parameters.Properties.Keys())

	output, err := Marshal(tool)
	require.NoError(t, err)

	// Re-parse and verify
	var roundTripped ChatTool
	err = Unmarshal(output, &roundTripped)
	require.NoError(t, err)
	assert.Equal(t, tool.Function.Parameters.Properties.Keys(), roundTripped.Function.Parameters.Properties.Keys())

	// Verify $defs position in parameters
	paramKeys := ExtractTopLevelKeyOrder(output)
	// Find the parameters JSON within the output to check its key order
	var toolMap map[string]interface{}
	err = Unmarshal(output, &toolMap)
	require.NoError(t, err)
	_ = paramKeys // top-level tool keys don't need ordering check

	// Re-marshal just the parameters to check its key order
	paramOutput, err := Marshal(tool.Function.Parameters)
	require.NoError(t, err)
	paramOutputKeys := ExtractTopLevelKeyOrder(paramOutput)
	assert.Equal(t, "$defs", paramOutputKeys[0], "parameters should have $defs first")
}

// --- Normalized() property ordering tests ---

func TestNormalized_PreservesPropertyOrder_CoTBeforeAnswer(t *testing.T) {
	// The exact customer schema: chain_of_thought before answer
	params := &ToolFunctionParameters{
		Type: "object",
		Properties: NewOrderedMapFromPairs(
			KV("chain_of_thought", NewOrderedMapFromPairs(
				KV("description", "Step by step reasoning"),
				KV("type", "string"),
				KV("title", "Chain of Thought"),
			)),
			KV("answer", NewOrderedMapFromPairs(
				KV("description", "The detailed answer"),
				KV("type", "string"),
				KV("title", "Answer"),
			)),
			KV("citations", NewOrderedMapFromPairs(
				KV("description", "Supporting citations"),
				KV("type", "array"),
			)),
			KV("is_unanswered", NewOrderedMapFromPairs(
				KV("type", "boolean"),
				KV("title", "Is Unanswered"),
			)),
		),
		Required: []string{"chain_of_thought", "answer", "citations", "is_unanswered"},
	}

	normalized := params.Normalized()

	// CoT: property order preserved
	assert.Equal(t, []string{"chain_of_thought", "answer", "citations", "is_unanswered"}, normalized.Properties.Keys())

	// Caching: structural keys within each property are sorted by JSON Schema priority
	cot, _ := normalized.Properties.Get("chain_of_thought")
	cotOM := cot.(*OrderedMap)
	assert.Equal(t, []string{"type", "description", "title"}, cotOM.Keys(),
		"structural keys within property should be sorted: type > description > others alpha")

	// Immutability: original unchanged
	assert.Equal(t, []string{"chain_of_thought", "answer", "citations", "is_unanswered"}, params.Properties.Keys())
}

func TestNormalized_CachingDeterminism_DifferentStructuralOrder(t *testing.T) {
	// Two schemas with same properties but different structural key orders
	// Should produce identical JSON after normalization
	propsA := NewOrderedMapFromPairs(
		KV("reasoning", NewOrderedMapFromPairs(
			KV("type", "string"),
			KV("description", "Step by step"),
		)),
		KV("answer", NewOrderedMapFromPairs(
			KV("type", "string"),
			KV("description", "Final answer"),
		)),
	)
	propsB := NewOrderedMapFromPairs(
		KV("reasoning", NewOrderedMapFromPairs(
			KV("description", "Step by step"),
			KV("type", "string"),
		)),
		KV("answer", NewOrderedMapFromPairs(
			KV("description", "Final answer"),
			KV("type", "string"),
		)),
	)

	schemaA := &ToolFunctionParameters{Type: "object", Properties: propsA, Required: []string{"reasoning"}}
	schemaB := &ToolFunctionParameters{Type: "object", Properties: propsB, Required: []string{"reasoning"}}

	jsonA, err := Marshal(schemaA.Normalized())
	require.NoError(t, err)
	jsonB, err := Marshal(schemaB.Normalized())
	require.NoError(t, err)

	// Caching: identical JSON regardless of input structural key order
	assert.Equal(t, string(jsonA), string(jsonB), "same schema with different structural key order should produce identical JSON")

	// CoT: property order preserved in both
	normA := schemaA.Normalized()
	normB := schemaB.Normalized()
	assert.Equal(t, []string{"reasoning", "answer"}, normA.Properties.Keys())
	assert.Equal(t, []string{"reasoning", "answer"}, normB.Properties.Keys())
}

func TestNormalized_WithDefs_PropertiesPreserved(t *testing.T) {
	params := &ToolFunctionParameters{
		Type: "object",
		Defs: NewOrderedMapFromPairs(
			KV("Citation", NewOrderedMapFromPairs(
				KV("type", "object"),
				KV("properties", NewOrderedMapFromPairs(
					KV("url", NewOrderedMapFromPairs(KV("type", "string"))),
					KV("text", NewOrderedMapFromPairs(KV("type", "string"))),
				)),
			)),
		),
		Properties: NewOrderedMapFromPairs(
			KV("chain_of_thought", NewOrderedMapFromPairs(KV("type", "string"))),
			KV("answer", NewOrderedMapFromPairs(KV("type", "string"))),
			KV("citations", NewOrderedMapFromPairs(KV("type", "array"))),
			KV("is_unanswered", NewOrderedMapFromPairs(KV("type", "boolean"))),
		),
		Required: []string{"answer", "is_unanswered"},
	}

	normalized := params.Normalized()

	// CoT: properties order preserved
	assert.Equal(t, []string{"chain_of_thought", "answer", "citations", "is_unanswered"}, normalized.Properties.Keys())

	// CoT: properties within $defs preserved
	citation, _ := normalized.Defs.Get("Citation")
	citOM := citation.(*OrderedMap)
	citProps, _ := citOM.Get("properties")
	citPropsOM := citProps.(*OrderedMap)
	assert.Equal(t, []string{"url", "text"}, citPropsOM.Keys())
}

func TestNormalized_NestedObjectProperties_PreservedAtAllLevels(t *testing.T) {
	params := &ToolFunctionParameters{
		Type: "object",
		Properties: NewOrderedMapFromPairs(
			KV("output", NewOrderedMapFromPairs(
				KV("type", "object"),
				KV("properties", NewOrderedMapFromPairs(
					KV("verdict", NewOrderedMapFromPairs(KV("type", "string"))),
					KV("metadata", NewOrderedMapFromPairs(
						KV("type", "object"),
						KV("properties", NewOrderedMapFromPairs(
							KV("timestamp", NewOrderedMapFromPairs(KV("type", "string"))),
							KV("source", NewOrderedMapFromPairs(KV("type", "string"))),
							KV("confidence", NewOrderedMapFromPairs(KV("type", "number"))),
							KV("author", NewOrderedMapFromPairs(KV("type", "string"))),
						)),
					)),
					KV("score", NewOrderedMapFromPairs(KV("type", "number"))),
				)),
			)),
			KV("chain_of_thought", NewOrderedMapFromPairs(KV("type", "string"))),
			KV("answer", NewOrderedMapFromPairs(KV("type", "string"))),
		),
	}

	normalized := params.Normalized()

	// Level 1: top-level properties preserved
	assert.Equal(t, []string{"output", "chain_of_thought", "answer"}, normalized.Properties.Keys())

	// Level 2: output.properties preserved
	output, _ := normalized.Properties.Get("output")
	outputOM := output.(*OrderedMap)
	outputProps, _ := outputOM.Get("properties")
	outputPropsOM := outputProps.(*OrderedMap)
	assert.Equal(t, []string{"verdict", "metadata", "score"}, outputPropsOM.Keys())

	// Level 3: metadata.properties preserved
	meta, _ := outputPropsOM.Get("metadata")
	metaOM := meta.(*OrderedMap)
	metaProps, _ := metaOM.Get("properties")
	metaPropsOM := metaProps.(*OrderedMap)
	assert.Equal(t, []string{"timestamp", "source", "confidence", "author"}, metaPropsOM.Keys())
}

func TestNormalized_OriginalNotMutated(t *testing.T) {
	params := &ToolFunctionParameters{
		Type: "object",
		Properties: NewOrderedMapFromPairs(
			KV("zebra", NewOrderedMapFromPairs(
				KV("description", "last alpha"),
				KV("type", "string"),
			)),
			KV("alpha", NewOrderedMapFromPairs(
				KV("description", "first alpha"),
				KV("type", "number"),
			)),
		),
	}

	_ = params.Normalized()

	// Original property order unchanged
	assert.Equal(t, []string{"zebra", "alpha"}, params.Properties.Keys())

	// Original structural key order within properties unchanged
	zebra, _ := params.Properties.Get("zebra")
	zebraOM := zebra.(*OrderedMap)
	assert.Equal(t, []string{"description", "type"}, zebraOM.Keys())
}

// --- Caching regression tests ---

func TestNormalized_CachingRegression_PropertyOrderDoesNotAffectCache(t *testing.T) {
	// Three independently constructed schemas with the SAME properties and
	// SAME structural key order. All three must produce byte-identical JSON.
	// This proves normalization is deterministic (no Go map iteration randomness).
	makeSchema := func() *ToolFunctionParameters {
		return &ToolFunctionParameters{
			Type: "object",
			Properties: NewOrderedMapFromPairs(
				KV("chain_of_thought", NewOrderedMapFromPairs(
					KV("type", "string"),
					KV("description", "Reasoning steps"),
				)),
				KV("answer", NewOrderedMapFromPairs(
					KV("type", "string"),
					KV("description", "The answer"),
				)),
			),
			Required: []string{"chain_of_thought", "answer"},
		}
	}

	jsonA, err := Marshal(makeSchema().Normalized())
	require.NoError(t, err)
	jsonB, err := Marshal(makeSchema().Normalized())
	require.NoError(t, err)
	jsonC, err := Marshal(makeSchema().Normalized())
	require.NoError(t, err)

	assert.Equal(t, string(jsonA), string(jsonB), "first two normalizations must be identical")
	assert.Equal(t, string(jsonB), string(jsonC), "all three normalizations must be identical")
}

func TestNormalized_CachingRegression_FullToolMarshal(t *testing.T) {
	// Tests the complete serialization path: ChatTool → ToolFunctionParameters.MarshalJSON
	// This is what actually hits the wire and forms the cache key.
	tool := ChatTool{
		Type: "function",
		Function: &ChatToolFunction{
			Name:        "AnswerResponseModel",
			Description: Ptr("Correctly extracted response model"),
			Parameters: &ToolFunctionParameters{
				Type: "object",
				Properties: NewOrderedMapFromPairs(
					KV("chain_of_thought", NewOrderedMapFromPairs(
						KV("description", "Step by step chain of thought"),
						KV("title", "Chain of Thought"),
						KV("type", "string"),
					)),
					KV("answer", NewOrderedMapFromPairs(
						KV("description", "The detailed answer"),
						KV("title", "Answer"),
						KV("type", "string"),
					)),
					KV("is_unanswered", NewOrderedMapFromPairs(
						KV("title", "Is Unanswered"),
						KV("type", "boolean"),
					)),
					KV("citations", NewOrderedMapFromPairs(
						KV("description", "List of citations"),
						KV("type", "array"),
					)),
				),
				Required: []string{"answer", "chain_of_thought", "citations", "is_unanswered"},
			},
		},
	}

	// Normalize and marshal twice
	normalizedParams := tool.Function.Parameters.Normalized()
	toolCopy1 := tool
	funcCopy1 := *tool.Function
	funcCopy1.Parameters = normalizedParams
	toolCopy1.Function = &funcCopy1

	normalizedParams2 := tool.Function.Parameters.Normalized()
	toolCopy2 := tool
	funcCopy2 := *tool.Function
	funcCopy2.Parameters = normalizedParams2
	toolCopy2.Function = &funcCopy2

	json1, err := Marshal(toolCopy1)
	require.NoError(t, err)
	json2, err := Marshal(toolCopy2)
	require.NoError(t, err)

	// Caching: full tool JSON is byte-identical
	assert.Equal(t, string(json1), string(json2),
		"full ChatTool marshal must be deterministic for prompt caching")

	// CoT: verify property order in the serialized JSON
	// Parse back and check properties key order
	var roundTripped ChatTool
	err = Unmarshal(json1, &roundTripped)
	require.NoError(t, err)
	keys := roundTripped.Function.Parameters.Properties.Keys()
	assert.Equal(t, []string{"chain_of_thought", "answer", "is_unanswered", "citations"}, keys,
		"property order must be preserved through full marshal round-trip")
}

// --- ResponsesTool deterministic serialization tests ---

// TestResponsesTool_MarshalJSON_Deterministic verifies that marshaling the same
// ResponsesTool struct produces byte-identical JSON every time. This is critical for
// OpenAI's prefix-based prompt caching — non-deterministic tool serialization
// would invalidate the cache on every other call.
func TestResponsesTool_MarshalJSON_Deterministic(t *testing.T) {
	tools := []ResponsesTool{
		{
			Type:        ResponsesToolTypeFunction,
			Name:        Ptr("weather"),
			Description: Ptr("Get current weather"),
			CacheControl: &CacheControl{
				Type: CacheControlTypeEphemeral,
			},
			ResponsesToolFunction: &ResponsesToolFunction{
				Parameters: &ToolFunctionParameters{
					Type: "object",
					Properties: NewOrderedMapFromPairs(
						KV("location", NewOrderedMapFromPairs(
							KV("type", "string"),
							KV("description", "City name"),
						)),
						KV("unit", NewOrderedMapFromPairs(
							KV("type", "string"),
							KV("enum", []string{"celsius", "fahrenheit"}),
						)),
					),
					Required: []string{"location"},
				},
				Strict: Ptr(true),
			},
		},
		{
			Type:                    ResponsesToolTypeFileSearch,
			ResponsesToolFileSearch: &ResponsesToolFileSearch{VectorStoreIDs: []string{"vs_1", "vs_2"}},
		},
		{
			Type:        ResponsesToolTypeWebSearch,
			Description: Ptr("Search the web"),
			ResponsesToolWebSearch: &ResponsesToolWebSearch{
				SearchContextSize: Ptr("medium"),
			},
		},
		{
			Type: ResponsesToolTypeComputerUsePreview,
			ResponsesToolComputerUsePreview: &ResponsesToolComputerUsePreview{
				DisplayWidth:  1024,
				DisplayHeight: 768,
				Environment:   "browser",
			},
		},
	}

	for _, tool := range tools {
		t.Run(string(tool.Type), func(t *testing.T) {
			first, err := Marshal(tool)
			require.NoError(t, err, "first marshal should succeed")

			for i := 0; i < 100; i++ {
				got, err := Marshal(tool)
				require.NoError(t, err)
				require.Equal(t, string(first), string(got),
					"iteration %d: marshal produced different bytes.\nfirst: %s\ngot:   %s", i, string(first), string(got))
			}
		})
	}
}

// TestResponsesTool_MarshalJSON_ContentPreservation verifies that the sjson-based
// MarshalJSON produces JSON with all expected fields and values.
func TestResponsesTool_MarshalJSON_ContentPreservation(t *testing.T) {
	tests := []struct {
		name         string
		tool         ResponsesTool
		wantContains []string // substrings that must appear in JSON
	}{
		{
			name: "function_with_all_common_fields",
			tool: ResponsesTool{
				Type:        ResponsesToolTypeFunction,
				Name:        Ptr("search_db"),
				Description: Ptr("Search database"),
				CacheControl: &CacheControl{
					Type: CacheControlTypeEphemeral,
				},
				ResponsesToolFunction: &ResponsesToolFunction{
					Strict: Ptr(false),
				},
			},
			wantContains: []string{
				`"type":"function"`,
				`"name":"search_db"`,
				`"description":"Search database"`,
				`"cache_control":{"type":"ephemeral"}`,
				`"strict":false`,
			},
		},
		{
			name: "function_with_parameters",
			tool: ResponsesTool{
				Type: ResponsesToolTypeFunction,
				Name: Ptr("get_weather"),
				ResponsesToolFunction: &ResponsesToolFunction{
					Parameters: &ToolFunctionParameters{
						Type: "object",
						Properties: NewOrderedMapFromPairs(
							KV("location", NewOrderedMapFromPairs(
								KV("type", "string"),
							)),
						),
					},
					Strict: Ptr(true),
				},
			},
			wantContains: []string{
				`"type":"function"`,
				`"name":"get_weather"`,
				`"parameters":{`,
				`"location":{`,
				`"strict":true`,
			},
		},
		{
			name: "file_search_tool",
			tool: ResponsesTool{
				Type: ResponsesToolTypeFileSearch,
				ResponsesToolFileSearch: &ResponsesToolFileSearch{
					VectorStoreIDs: []string{"vs_123"},
					MaxNumResults:  Ptr(10),
				},
			},
			wantContains: []string{
				`"type":"file_search"`,
				`"vector_store_ids":["vs_123"]`,
				`"max_num_results":10`,
			},
		},
		{
			name: "web_search_tool",
			tool: ResponsesTool{
				Type:        ResponsesToolTypeWebSearch,
				Description: Ptr("Web search tool"),
				ResponsesToolWebSearch: &ResponsesToolWebSearch{
					SearchContextSize: Ptr("high"),
				},
			},
			wantContains: []string{
				`"type":"web_search"`,
				`"description":"Web search tool"`,
				`"search_context_size":"high"`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := Marshal(tt.tool)
			require.NoError(t, err)
			jsonStr := string(data)
			for _, want := range tt.wantContains {
				assert.Contains(t, jsonStr, want, "JSON should contain %q, got: %s", want, jsonStr)
			}
		})
	}
}

// TestResponsesTool_MarshalJSON_RoundTrip verifies that unmarshal→marshal→unmarshal
// produces structurally identical results.
func TestResponsesTool_MarshalJSON_RoundTrip(t *testing.T) {
	inputs := []string{
		`{"type":"function","name":"get_weather","description":"Get weather","strict":true}`,
		`{"type":"function","name":"search_db","description":"Search database","cache_control":{"type":"ephemeral"},"strict":false}`,
		`{"type":"file_search","vector_store_ids":["vs_1"],"max_num_results":10}`,
	}

	for _, input := range inputs {
		name := input
		if len(name) > 50 {
			name = name[:50]
		}
		t.Run(name, func(t *testing.T) {
			// Round 1: unmarshal → marshal
			var tool1 ResponsesTool
			require.NoError(t, Unmarshal([]byte(input), &tool1))
			data1, err := Marshal(tool1)
			require.NoError(t, err)

			// Round 2: unmarshal → marshal
			var tool2 ResponsesTool
			require.NoError(t, Unmarshal(data1, &tool2))
			data2, err := Marshal(tool2)
			require.NoError(t, err)

			// Round-trip stability: second marshal must match first
			require.Equal(t, string(data1), string(data2),
				"round-trip produced different bytes.\nround1: %s\nround2: %s", string(data1), string(data2))

			// Content equivalence with original input
			var original, roundTripped map[string]interface{}
			require.NoError(t, Unmarshal([]byte(input), &original))
			require.NoError(t, Unmarshal(data1, &roundTripped))
			assert.Equal(t, original, roundTripped, "content should match original input")
		})
	}
}

// TestResponsesTool_RoundTrip_AnthropicFields ensures the Anthropic-native tool
// flags promoted onto ResponsesTool (defer_loading, allowed_callers,
// input_examples, eager_input_streaming) survive a full Marshal→Unmarshal→
// Marshal cycle. Before MarshalJSON/UnmarshalJSON were taught to handle these
// keys, all four were silently dropped at the JSON boundary.
func TestResponsesTool_RoundTrip_AnthropicFields(t *testing.T) {
	original := ResponsesTool{
		Type:                ResponsesToolTypeFunction,
		Name:                Ptr("lookup"),
		Description:         Ptr("lookup something"),
		DeferLoading:        Ptr(true),
		AllowedCallers:      []string{"direct", "agent"},
		EagerInputStreaming: Ptr(false),
		InputExamples: []ChatToolInputExample{
			{Input: json.RawMessage(`{"q":"hello"}`), Description: Ptr("basic")},
			{Input: json.RawMessage(`{"q":"world"}`)},
		},
		ResponsesToolFunction: &ResponsesToolFunction{
			Parameters: &ToolFunctionParameters{},
		},
	}

	data, err := Marshal(original)
	require.NoError(t, err)

	// All four keys must appear in the wire bytes.
	for _, key := range []string{`"defer_loading"`, `"allowed_callers"`, `"input_examples"`, `"eager_input_streaming"`} {
		assert.Contains(t, string(data), key,
			"%s must be emitted by MarshalJSON — otherwise it is silently dropped", key)
	}

	var decoded ResponsesTool
	require.NoError(t, Unmarshal(data, &decoded))

	require.NotNil(t, decoded.DeferLoading)
	assert.True(t, *decoded.DeferLoading)
	assert.Equal(t, []string{"direct", "agent"}, decoded.AllowedCallers)
	require.NotNil(t, decoded.EagerInputStreaming)
	assert.False(t, *decoded.EagerInputStreaming)
	require.Len(t, decoded.InputExamples, 2)
	assert.JSONEq(t, `{"q":"hello"}`, string(decoded.InputExamples[0].Input))
	require.NotNil(t, decoded.InputExamples[0].Description)
	assert.Equal(t, "basic", *decoded.InputExamples[0].Description)
	assert.JSONEq(t, `{"q":"world"}`, string(decoded.InputExamples[1].Input))

	// Second-round marshal must be byte-stable.
	data2, err := Marshal(decoded)
	require.NoError(t, err)
	assert.Equal(t, string(data), string(data2), "round-trip must be stable")
}

// TestChatTool_MarshalJSON_EnforcesUnion verifies that the custom codec
// canonicalizes mixed-state ChatTools on the wire, regardless of what the
// caller populated in memory. Exactly one variant's fields survive marshal —
// matching Type — so downstream provider converters can't misinterpret or
// forward stray fields from a different shape.
func TestChatTool_MarshalJSON_EnforcesUnion(t *testing.T) {
	t.Run("function_type_clears_custom_and_server_tool_fields", func(t *testing.T) {
		tool := ChatTool{
			Type:     ChatToolTypeFunction,
			Function: &ChatToolFunction{Name: "get_weather"},
			// Mixed state: server-tool + custom fields also populated.
			Custom:        &ChatToolCustom{},
			Name:          "leaked_name",
			MaxUses:       Ptr(5),
			DisplayWidthPx: Ptr(1280),
			MCPServerName: "leaked_server",
		}
		data, err := Marshal(tool)
		require.NoError(t, err)
		raw := string(data)

		assert.Contains(t, raw, `"type":"function"`)
		assert.Contains(t, raw, `"get_weather"`)
		for _, leak := range []string{`"custom"`, `"leaked_name"`, `"max_uses"`, `"display_width_px"`, `"mcp_server_name"`} {
			assert.NotContains(t, raw, leak, "function-type wire must not carry %s", leak)
		}
	})

	t.Run("custom_type_clears_function_and_server_tool_fields", func(t *testing.T) {
		tool := ChatTool{
			Type:   ChatToolTypeCustom,
			Custom: &ChatToolCustom{Format: &ChatToolCustomFormat{Type: "text"}},
			Name:   "my_custom",
			// Leaks
			Function: &ChatToolFunction{Name: "should_be_stripped"},
			MaxUses:  Ptr(5),
		}
		data, err := Marshal(tool)
		require.NoError(t, err)
		raw := string(data)

		assert.Contains(t, raw, `"type":"custom"`)
		assert.Contains(t, raw, `"my_custom"`)    // custom tool retains top-level Name
		assert.Contains(t, raw, `"format"`)       // custom's format field
		assert.NotContains(t, raw, `"function"`)
		assert.NotContains(t, raw, `"should_be_stripped"`)
		assert.NotContains(t, raw, `"max_uses"`)
	})

	t.Run("server_tool_type_clears_function_and_custom", func(t *testing.T) {
		tool := ChatTool{
			Type:    "web_search_20260209",
			Name:    "web_search",
			MaxUses: Ptr(5),
			AllowedCallers: []string{"direct"},
			// Leaks
			Function: &ChatToolFunction{Name: "should_be_stripped"},
			Custom:   &ChatToolCustom{},
		}
		data, err := Marshal(tool)
		require.NoError(t, err)
		raw := string(data)

		assert.Contains(t, raw, `"type":"web_search_20260209"`)
		assert.Contains(t, raw, `"web_search"`)
		assert.Contains(t, raw, `"max_uses":5`)
		assert.Contains(t, raw, `"allowed_callers":["direct"]`)
		assert.NotContains(t, raw, `"function"`)
		assert.NotContains(t, raw, `"custom"`)
		assert.NotContains(t, raw, `"should_be_stripped"`)
	})
}

// TestChatTool_UnmarshalJSON_NormalizesMixedInput verifies that tolerant
// decode of a mixed-shape payload produces a canonical single-variant struct
// so downstream provider conversion code doesn't have to defend against
// the untrusted shape.
func TestChatTool_UnmarshalJSON_NormalizesMixedInput(t *testing.T) {
	t.Run("function_type_mixed_with_server_fields_normalizes", func(t *testing.T) {
		// Caller sends a function tool but also includes server-tool metadata.
		raw := []byte(`{
			"type":"function",
			"function":{"name":"get_weather"},
			"name":"stray_server_name",
			"max_uses":5,
			"display_width_px":1280
		}`)
		var tool ChatTool
		require.NoError(t, Unmarshal(raw, &tool))

		assert.Equal(t, ChatToolTypeFunction, tool.Type)
		require.NotNil(t, tool.Function)
		assert.Equal(t, "get_weather", tool.Function.Name)
		assert.Empty(t, tool.Name, "function-type must nil top-level Name (lives in Function.Name)")
		assert.Nil(t, tool.MaxUses)
		assert.Nil(t, tool.DisplayWidthPx)
	})

	t.Run("server_tool_type_mixed_with_function_normalizes", func(t *testing.T) {
		// Caller sends a server-tool but also includes function.
		raw := []byte(`{
			"type":"web_search_20260209",
			"name":"web_search",
			"max_uses":5,
			"function":{"name":"stray"}
		}`)
		var tool ChatTool
		require.NoError(t, Unmarshal(raw, &tool))

		assert.Equal(t, ChatToolType("web_search_20260209"), tool.Type)
		assert.Equal(t, "web_search", tool.Name)
		require.NotNil(t, tool.MaxUses)
		assert.Equal(t, 5, *tool.MaxUses)
		assert.Nil(t, tool.Function, "server-tool must nil Function")
		assert.Nil(t, tool.Custom, "server-tool must nil Custom")
	})
}

// TestChatTool_RoundTrip_SurvivesMixedInput verifies that a mixed-input
// payload, once canonicalized by Unmarshal and re-emitted by Marshal, drops
// the stray fields and produces a deterministic single-variant wire format.
func TestChatTool_RoundTrip_SurvivesMixedInput(t *testing.T) {
	raw := []byte(`{
		"type":"web_search_20260209",
		"name":"web_search",
		"max_uses":5,
		"function":{"name":"stray"},
		"custom":{"format":{"type":"text"}}
	}`)
	var tool ChatTool
	require.NoError(t, Unmarshal(raw, &tool))

	out, err := Marshal(tool)
	require.NoError(t, err)
	outStr := string(out)
	assert.NotContains(t, outStr, `"function"`)
	assert.NotContains(t, outStr, `"custom"`)
	assert.Contains(t, outStr, `"web_search_20260209"`)

	// Second pass must be byte-stable (critical for prompt caching keys).
	var tool2 ChatTool
	require.NoError(t, Unmarshal(out, &tool2))
	out2, err := Marshal(tool2)
	require.NoError(t, err)
	assert.Equal(t, string(out), string(out2), "round-trip must be stable")
}

func TestToolFunctionParameters_ExplicitEmptyObjectPreserved(t *testing.T) {
	var params ToolFunctionParameters
	err := Unmarshal([]byte(`{}`), &params)
	require.NoError(t, err)

	marshaled, err := Marshal(params)
	require.NoError(t, err)
	assert.Equal(t, `{}`, string(marshaled))

	normalized, err := Marshal(params.Normalized())
	require.NoError(t, err)
	assert.Equal(t, `{}`, string(normalized))
}

func TestToolFunctionParameters_ExplicitEmptyObjectWhitespacePreserved(t *testing.T) {
	var params ToolFunctionParameters
	err := Unmarshal([]byte(` { } `), &params)
	require.NoError(t, err)

	marshaled, err := Marshal(params)
	require.NoError(t, err)
	assert.Equal(t, `{}`, string(marshaled))

	normalized, err := Marshal(params.Normalized())
	require.NoError(t, err)
	assert.Equal(t, `{}`, string(normalized))
}

func TestToolFunctionParameters_ExplicitObjectSchemaPreserved(t *testing.T) {
	var params ToolFunctionParameters
	err := Unmarshal([]byte(`{"type":"object","properties":{}}`), &params)
	require.NoError(t, err)

	marshaled, err := Marshal(params)
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"object","properties":{}}`, string(marshaled))

	normalized, err := Marshal(params.Normalized())
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"object","properties":{}}`, string(normalized))
}

// TestResponsesToolFileSearchFilter_MarshalJSON_Deterministic verifies deterministic
// serialization for file search filters.
func TestResponsesToolFileSearchFilter_MarshalJSON_Deterministic(t *testing.T) {
	filters := []*ResponsesToolFileSearchFilter{
		{
			Type: "eq",
			ResponsesToolFileSearchComparisonFilter: &ResponsesToolFileSearchComparisonFilter{
				Key:   "status",
				Value: "active",
			},
		},
		{
			Type: "and",
			ResponsesToolFileSearchCompoundFilter: &ResponsesToolFileSearchCompoundFilter{
				Filters: []ResponsesToolFileSearchFilter{
					{
						Type: "eq",
						ResponsesToolFileSearchComparisonFilter: &ResponsesToolFileSearchComparisonFilter{
							Key:   "type",
							Value: "document",
						},
					},
				},
			},
		},
	}

	for _, filter := range filters {
		t.Run(filter.Type, func(t *testing.T) {
			first, err := Marshal(filter)
			require.NoError(t, err)

			for i := 0; i < 100; i++ {
				got, err := Marshal(filter)
				require.NoError(t, err)
				require.Equal(t, string(first), string(got),
					"iteration %d: marshal produced different bytes", i)
			}
		})
	}
}

// TestResponsesToolMCPApprovalSetting_MarshalJSON_Deterministic verifies deterministic
// serialization for MCP approval settings.
func TestResponsesToolMCPApprovalSetting_MarshalJSON_Deterministic(t *testing.T) {
	settings := []ResponsesToolMCPAllowedToolsApprovalSetting{
		{
			Setting: Ptr("always"),
		},
		{
			Always: &ResponsesToolMCPAllowedToolsApprovalFilter{
				ToolNames: []string{"tool1", "tool2"},
			},
		},
		{
			Never: &ResponsesToolMCPAllowedToolsApprovalFilter{
				ToolNames: []string{"dangerous_tool"},
			},
		},
		{
			Always: &ResponsesToolMCPAllowedToolsApprovalFilter{
				ToolNames: []string{"safe_tool"},
			},
			Never: &ResponsesToolMCPAllowedToolsApprovalFilter{
				ToolNames: []string{"risky_tool"},
			},
		},
	}

	for i, setting := range settings {
		t.Run(strings.Repeat("_", i), func(t *testing.T) {
			first, err := Marshal(setting)
			require.NoError(t, err)

			for j := 0; j < 100; j++ {
				got, err := Marshal(setting)
				require.NoError(t, err)
				require.Equal(t, string(first), string(got),
					"iteration %d: marshal produced different bytes", j)
			}
		})
	}
}

// TestNetworkConfig_TLSFieldsRoundTrip verifies that insecure_skip_verify and ca_cert_pem
// round-trip correctly through JSON marshaling (used by config.json).
func TestNetworkConfig_TLSFieldsRoundTrip(t *testing.T) {
	nc := NetworkConfig{
		BaseURL:                        "https://example.com",
		DefaultRequestTimeoutInSeconds: 60,
		MaxRetries:                     3,
		InsecureSkipVerify:             true,
		CACertPEM:                      "-----BEGIN CERTIFICATE-----\nMIIB...\n-----END CERTIFICATE-----",
	}

	data, err := json.Marshal(nc)
	require.NoError(t, err)

	var decoded NetworkConfig
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, nc.InsecureSkipVerify, decoded.InsecureSkipVerify, "insecure_skip_verify should round-trip")
	assert.Equal(t, nc.CACertPEM, decoded.CACertPEM, "ca_cert_pem should round-trip")
	assert.Contains(t, string(data), `"insecure_skip_verify":true`)
	assert.Contains(t, string(data), `"ca_cert_pem"`)
}

// TestNetworkConfig_StreamIdleTimeoutRoundTrip verifies that stream_idle_timeout_in_seconds
// round-trips correctly through JSON marshaling.
func TestNetworkConfig_StreamIdleTimeoutRoundTrip(t *testing.T) {
	nc := NetworkConfig{
		DefaultRequestTimeoutInSeconds: 30,
		StreamIdleTimeoutInSeconds:     120,
	}

	data, err := json.Marshal(nc)
	require.NoError(t, err)

	var decoded NetworkConfig
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, 120, decoded.StreamIdleTimeoutInSeconds, "stream_idle_timeout_in_seconds should round-trip")
	assert.Contains(t, string(data), `"stream_idle_timeout_in_seconds":120`)
}

// TestNormalizeResponsesToolType verifies that versioned/provider-specific tool type
// strings are normalized to their canonical ResponsesToolType values.
func TestNormalizeResponsesToolType(t *testing.T) {
	tests := []struct {
		input ResponsesToolType
		want  ResponsesToolType
	}{
		// Already canonical — returned unchanged
		{ResponsesToolTypeWebSearch, ResponsesToolTypeWebSearch},
		{ResponsesToolTypeWebSearchPreview, ResponsesToolTypeWebSearchPreview},
		{ResponsesToolTypeWebFetch, ResponsesToolTypeWebFetch},
		{ResponsesToolTypeComputerUsePreview, ResponsesToolTypeComputerUsePreview},
		{ResponsesToolTypeCodeInterpreter, ResponsesToolTypeCodeInterpreter},
		{ResponsesToolTypeMemory, ResponsesToolTypeMemory},
		{ResponsesToolTypeFunction, ResponsesToolTypeFunction},
		{ResponsesToolTypeCustom, ResponsesToolTypeCustom},

		// web_search versioned aliases
		{"web_search_20250305", ResponsesToolTypeWebSearch},
		{"web_search_20260209", ResponsesToolTypeWebSearch},
		{"web_search_2025_08_26", ResponsesToolTypeWebSearch},

		// web_search_preview versioned aliases (must not collide with web_search)
		{"web_search_preview_2025_03_11", ResponsesToolTypeWebSearchPreview},

		// web_fetch versioned aliases
		{"web_fetch_20250910", ResponsesToolTypeWebFetch},
		{"web_fetch_20260209", ResponsesToolTypeWebFetch},
		{"web_fetch_20260309", ResponsesToolTypeWebFetch},

		// computer versioned aliases
		{"computer_20250124", ResponsesToolTypeComputerUsePreview},
		{"computer_20251124", ResponsesToolTypeComputerUsePreview},

		// code_execution versioned aliases → code_interpreter
		{"code_execution_20250522", ResponsesToolTypeCodeInterpreter},
		{"code_execution_20250825", ResponsesToolTypeCodeInterpreter},
		{"code_execution_20260120", ResponsesToolTypeCodeInterpreter},

		// memory versioned aliases
		{"memory_20250818", ResponsesToolTypeMemory},

		// Unrecognized types pass through unchanged
		{"totally_unknown", "totally_unknown"},
		{"mcp", ResponsesToolTypeMCP},
	}

	for _, tt := range tests {
		t.Run(string(tt.input), func(t *testing.T) {
			got := normalizeResponsesToolType(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestResponsesTool_UnmarshalJSON_NormalizesVersionedToolTypes verifies that versioned
// tool types sent in Responses API requests are normalized and their embedded structs populated.
func TestResponsesTool_UnmarshalJSON_NormalizesVersionedToolTypes(t *testing.T) {
	tests := []struct {
		name           string
		input          string
		wantType       ResponsesToolType
		wantWebSearch  bool
		wantWebFetch   bool
		wantComputer   bool
		wantCodeInterp bool
	}{
		// web_search variants
		{name: "web_search canonical", input: `{"type":"web_search"}`, wantType: ResponsesToolTypeWebSearch, wantWebSearch: true},
		{name: "web_search_20250305", input: `{"type":"web_search_20250305"}`, wantType: ResponsesToolTypeWebSearch, wantWebSearch: true},
		{name: "web_search_20260209", input: `{"type":"web_search_20260209"}`, wantType: ResponsesToolTypeWebSearch, wantWebSearch: true},
		{name: "web_search_20250305 with max_uses", input: `{"type":"web_search_20250305","max_uses":1}`, wantType: ResponsesToolTypeWebSearch, wantWebSearch: true},

		// web_search_preview variants
		{name: "web_search_preview canonical", input: `{"type":"web_search_preview"}`, wantType: ResponsesToolTypeWebSearchPreview},
		{name: "web_search_preview_2025_03_11", input: `{"type":"web_search_preview_2025_03_11"}`, wantType: ResponsesToolTypeWebSearchPreview},

		// web_fetch variants
		{name: "web_fetch canonical", input: `{"type":"web_fetch"}`, wantType: ResponsesToolTypeWebFetch, wantWebFetch: true},
		{name: "web_fetch_20250910", input: `{"type":"web_fetch_20250910"}`, wantType: ResponsesToolTypeWebFetch, wantWebFetch: true},
		{name: "web_fetch_20260309", input: `{"type":"web_fetch_20260309"}`, wantType: ResponsesToolTypeWebFetch, wantWebFetch: true},

		// computer variants
		{name: "computer_use_preview canonical", input: `{"type":"computer_use_preview","display_width":1024,"display_height":768,"environment":"browser"}`, wantType: ResponsesToolTypeComputerUsePreview, wantComputer: true},
		{name: "computer_20250124", input: `{"type":"computer_20250124","display_width":1024,"display_height":768,"environment":"browser"}`, wantType: ResponsesToolTypeComputerUsePreview, wantComputer: true},
		{name: "computer_20251124", input: `{"type":"computer_20251124","display_width":1024,"display_height":768,"environment":"browser"}`, wantType: ResponsesToolTypeComputerUsePreview, wantComputer: true},

		// code_execution variants → code_interpreter
		{name: "code_interpreter canonical", input: `{"type":"code_interpreter"}`, wantType: ResponsesToolTypeCodeInterpreter, wantCodeInterp: true},
		{name: "code_execution_20250522", input: `{"type":"code_execution_20250522"}`, wantType: ResponsesToolTypeCodeInterpreter, wantCodeInterp: true},
		{name: "code_execution_20250825", input: `{"type":"code_execution_20250825"}`, wantType: ResponsesToolTypeCodeInterpreter, wantCodeInterp: true},

		// unrecognized types pass through unchanged
		{name: "function unchanged", input: `{"type":"function","name":"foo","strict":true}`, wantType: ResponsesToolTypeFunction},
		{name: "custom unchanged", input: `{"type":"custom","name":"bar"}`, wantType: ResponsesToolTypeCustom},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var tool ResponsesTool
			err := Unmarshal([]byte(tt.input), &tool)
			require.NoError(t, err)
			assert.Equal(t, tt.wantType, tool.Type)

			if tt.wantWebSearch {
				assert.NotNil(t, tool.ResponsesToolWebSearch, "ResponsesToolWebSearch should be populated")
			}
			if tt.wantWebFetch {
				assert.NotNil(t, tool.ResponsesToolWebFetch, "ResponsesToolWebFetch should be populated")
			}
			if tt.wantComputer {
				assert.NotNil(t, tool.ResponsesToolComputerUsePreview, "ResponsesToolComputerUsePreview should be populated")
			}
			if tt.wantCodeInterp {
				assert.NotNil(t, tool.ResponsesToolCodeInterpreter, "ResponsesToolCodeInterpreter should be populated")
			}
		})
	}
}

// TestSonic_ChatTool_AnnotationsNeverSerialized verifies that MCPToolAnnotations
// (json:"-") are never included in the JSON payload sent to providers.
func TestSonic_ChatTool_AnnotationsNeverSerialized(t *testing.T) {
	readOnly := true
	destructive := false

	tool := ChatTool{
		Type: ChatToolTypeFunction,
		Function: &ChatToolFunction{
			Name:        "read_file",
			Description: Ptr("Reads a file from the filesystem"),
			Parameters: &ToolFunctionParameters{
				Type:       "object",
				Properties: NewOrderedMapFromPairs(KV("path", map[string]interface{}{"type": "string"})),
				Required:   []string{"path"},
			},
		},
		Annotations: &MCPToolAnnotations{
			Title:           "File Reader",
			ReadOnlyHint:    &readOnly,
			DestructiveHint: &destructive,
			IdempotentHint:  Ptr(true),
		},
	}

	output, err := Marshal(tool)
	require.NoError(t, err)

	s := string(output)

	// Annotations must be absent — json:"-" must suppress the entire field
	assert.NotContains(t, s, "annotations", "annotations field must not appear in provider payload")
	assert.NotContains(t, s, "readOnlyHint", "readOnlyHint must not appear in provider payload")
	assert.NotContains(t, s, "destructiveHint", "destructiveHint must not appear in provider payload")
	assert.NotContains(t, s, "idempotentHint", "idempotentHint must not appear in provider payload")
	assert.NotContains(t, s, "File Reader", "annotation title must not appear in provider payload")

	// The function definition itself must still be present
	assert.Contains(t, s, "read_file", "function name must be in payload")
	assert.Contains(t, s, "path", "parameter must be in payload")
}

// TestSonic_ChatTool_DeepCopy_AnnotationsPreserved verifies that DeepCopyChatTool
// correctly copies Annotations so they survive any clone-based flows.
func TestSonic_ChatTool_DeepCopy_AnnotationsPreserved(t *testing.T) {
	readOnly := true
	idempotent := false

	original := ChatTool{
		Type: ChatToolTypeFunction,
		Function: &ChatToolFunction{
			Name: "query_db",
		},
		Annotations: &MCPToolAnnotations{
			Title:          "DB Query",
			ReadOnlyHint:   &readOnly,
			IdempotentHint: &idempotent,
		},
	}

	copied := DeepCopyChatTool(original)

	require.NotNil(t, copied.Annotations)
	assert.Equal(t, "DB Query", copied.Annotations.Title)
	assert.Equal(t, true, *copied.Annotations.ReadOnlyHint)
	assert.Equal(t, false, *copied.Annotations.IdempotentHint)
	assert.Nil(t, copied.Annotations.DestructiveHint)
	assert.Nil(t, copied.Annotations.OpenWorldHint)

	// Verify it's a true deep copy — mutations don't bleed back
	*original.Annotations.ReadOnlyHint = false
	assert.True(t, *copied.Annotations.ReadOnlyHint, "copy must not share pointer with original")
}

// TestSonic_ChatTool_DeepCopy_NilAnnotationsStaysNil verifies that a tool
// without annotations deep-copies cleanly with Annotations remaining nil.
func TestSonic_ChatTool_DeepCopy_NilAnnotationsStaysNil(t *testing.T) {
	original := ChatTool{
		Type:     ChatToolTypeFunction,
		Function: &ChatToolFunction{Name: "plain_tool"},
	}

	copied := DeepCopyChatTool(original)

	assert.Nil(t, copied.Annotations, "Annotations should stay nil when original has none")
}
