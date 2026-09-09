package gemini

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Regression tests for mode ANY losing the forced function name.
//
// Gemini expresses "you must call get_weather" as mode ANY plus a single entry in
// allowed_function_names. convertGeminiToolConfigToToolChoice mapped the mode to "required" and
// pushed the name into the allowed-tools LIST, but never set ToolChoice.Name - while still setting
// Type to "function". Downstream converters read type "function" and demand a name, find none, and
// the provider rejects the request:
//
//	openai/azure: Missing required parameter: 'tool_choice.name'
//	anthropic:    tool_choice: Input tag '' found using 'type'
//	deepseek:     unknown variant `required`, expected `function`
//	xai:          did not match any variant of untagged enum ModelToolChoice
//
// Harness cell 47.9.F /genai :generateContent. Fixing the snake_case binding of
// allowed_function_names (functioncallingconfigalias_test.go) was necessary but not sufficient:
// the name reached this converter and was then dropped again on the way out.

func geminiToolConfig(mode FunctionCallingConfigMode, names ...string) *ToolConfig {
	return &ToolConfig{
		FunctionCallingConfig: &FunctionCallingConfig{
			Mode:                 mode,
			AllowedFunctionNames: names,
		},
	}
}

// TestConvertGeminiToolConfig_SingleAllowedNameForcesThatFunction is cell 47.9.F.
func TestConvertGeminiToolConfig_SingleAllowedNameForcesThatFunction(t *testing.T) {
	tc := convertGeminiToolConfigToToolChoice(geminiToolConfig(FunctionCallingConfigModeAny, "get_weather"))

	require.NotNil(t, tc)
	require.NotNil(t, tc.ResponsesToolChoiceStruct)
	s := tc.ResponsesToolChoiceStruct

	assert.Equal(t, schemas.ResponsesToolChoiceTypeFunction, s.Type)
	require.NotNil(t, s.Name,
		"mode ANY with exactly one allowed function means 'call that function'; leaving Name nil "+
			"while Type is \"function\" makes every downstream provider reject the request")
	assert.Equal(t, "get_weather", *s.Name)
}

// With several allowed names there is no single function to force, so the allowed-tools list is
// the correct representation and Name must stay unset.
func TestConvertGeminiToolConfig_MultipleAllowedNamesUseToolsList(t *testing.T) {
	tc := convertGeminiToolConfigToToolChoice(
		geminiToolConfig(FunctionCallingConfigModeAny, "get_weather", "get_time"))

	require.NotNil(t, tc)
	s := tc.ResponsesToolChoiceStruct
	require.NotNil(t, s)

	assert.Nil(t, s.Name, "no single function to force when several are allowed")
	require.Len(t, s.Tools, 2)
	assert.Equal(t, "required", *s.Mode)
}

// Mode ANY with no names at all is "call some tool" - required, unnamed.
func TestConvertGeminiToolConfig_AnyWithoutNamesStaysRequired(t *testing.T) {
	tc := convertGeminiToolConfigToToolChoice(geminiToolConfig(FunctionCallingConfigModeAny))

	require.NotNil(t, tc)
	s := tc.ResponsesToolChoiceStruct
	require.NotNil(t, s)

	assert.Nil(t, s.Name)
	require.NotNil(t, s.Mode)
	assert.Equal(t, "required", *s.Mode)
}

// AUTO must not be turned into a forced call just because a name is listed: under AUTO the names
// restrict what the model MAY call, they do not compel a call.
func TestConvertGeminiToolConfig_AutoWithNameIsNotForced(t *testing.T) {
	tc := convertGeminiToolConfigToToolChoice(
		geminiToolConfig(FunctionCallingConfigModeAuto, "get_weather"))

	require.NotNil(t, tc)
	s := tc.ResponsesToolChoiceStruct
	require.NotNil(t, s)

	assert.Nil(t, s.Name, "AUTO permits a tool call, it does not force one")
	require.NotNil(t, s.Mode)
	assert.Equal(t, "auto", *s.Mode)
}
