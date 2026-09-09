package gemini

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Regression tests for FunctionCallingConfig dropping allowed_function_names.
//
// Third instance of the snake_case drop fixed for system_instruction (systeminstructionalias_test.go)
// and GenerationConfig (generationconfigalias_test.go). ToolConfig already aliases
// `function_calling_config`, so the outer level bound fine and the bug hid one layer deeper:
// FunctionCallingConfig itself had no UnmarshalJSON. `mode` survived - it is a single lowercase
// word, spelled identically either way - while `allowed_function_names` was silently discarded.
//
// The consequence is worse than a dropped field. Mode ANY with no names means "call some tool"
// instead of "call get_weather", so downstream converters emit a forced tool choice with no
// function name and the provider rejects the request outright. The harness caught it as 4xx on
// 47.9.F /genai :generateContent:
//
//	anthropic: tool_choice: Input tag '' found using 'type' does not match any of the expected tags
//	openai:    Missing required parameter: 'tool_choice.name'
//	deepseek:  unknown variant `required`, expected `function`
//	xai:       data did not match any variant of untagged enum ModelToolChoice
//
// Gemini and Vertex returned 200 only because native genai picks from the bound declarations, and
// the cell binds exactly one tool - so the missing name made no observable difference there.

// TestFunctionCallingConfig_AcceptsSnakeCaseAllowedNames is the cell 47.9.F sends verbatim.
func TestFunctionCallingConfig_AcceptsSnakeCaseAllowedNames(t *testing.T) {
	body := `{"mode": "ANY", "allowed_function_names": ["get_weather"]}`

	var cfg FunctionCallingConfig
	require.NoError(t, json.Unmarshal([]byte(body), &cfg))

	assert.Equal(t, FunctionCallingConfigMode("ANY"), cfg.Mode)
	assert.Equal(t, []string{"get_weather"}, cfg.AllowedFunctionNames,
		"allowed_function_names must not be dropped; mode ANY without names degrades "+
			"'force get_weather' into 'force some tool', which providers reject for lacking a name")
}

// TestFunctionCallingConfig_AcceptsCamelCaseAllowedNames is the control.
func TestFunctionCallingConfig_AcceptsCamelCaseAllowedNames(t *testing.T) {
	body := `{"mode": "ANY", "allowedFunctionNames": ["get_weather"]}`

	var cfg FunctionCallingConfig
	require.NoError(t, json.Unmarshal([]byte(body), &cfg))

	assert.Equal(t, []string{"get_weather"}, cfg.AllowedFunctionNames)
}

// TestFunctionCallingConfig_CamelCaseWinsOverSnakeCase pins precedence, matching every other
// alias in this package.
func TestFunctionCallingConfig_CamelCaseWinsOverSnakeCase(t *testing.T) {
	body := `{"allowedFunctionNames": ["camel"], "allowed_function_names": ["snake"]}`

	var cfg FunctionCallingConfig
	require.NoError(t, json.Unmarshal([]byte(body), &cfg))

	assert.Equal(t, []string{"camel"}, cfg.AllowedFunctionNames)
}

// TestToolConfig_AcceptsFullySnakeCasePayload is the end-to-end shape: the outer alias on
// ToolConfig was never the problem, so a test that stops at ToolConfig would have passed while
// the nested names still vanished.
func TestToolConfig_AcceptsFullySnakeCasePayload(t *testing.T) {
	body := `{"function_calling_config": {"mode": "ANY", "allowed_function_names": ["get_weather"]}}`

	var cfg ToolConfig
	require.NoError(t, json.Unmarshal([]byte(body), &cfg))

	require.NotNil(t, cfg.FunctionCallingConfig, "function_calling_config dropped")
	assert.Equal(t, FunctionCallingConfigMode("ANY"), cfg.FunctionCallingConfig.Mode)
	assert.Equal(t, []string{"get_weather"}, cfg.FunctionCallingConfig.AllowedFunctionNames,
		"nested allowed_function_names dropped even though the outer alias resolved")
}
