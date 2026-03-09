package schemas

import (
	"encoding/json"
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
