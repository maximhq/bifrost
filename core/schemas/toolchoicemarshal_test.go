package schemas

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Regression tests for mode-only tool choices serializing as objects.
//
// The object form's `type` names a TOOL TYPE - "function", "code_interpreter", "namespace", ... -
// so a mode-only choice must go on the wire as a bare string. Emitting the struct verbatim sent
// {"type":"auto","mode":"auto"} and providers rejected it:
//
//	Invalid value: 'auto'. Supported values are: 'code_interpreter', ..., 'function', 'namespace'
//	Invalid value: 'required'. ...
//	Invalid value: 'none'. ...
//
// Found by probing /genai tool_config directly: harness cell 47.9.F always sends
// allowed_function_names, so the AUTO, NONE and nameless-ANY paths had no coverage and were
// broken for every caller.

func TestResponsesToolChoice_ModeOnlyMarshalsAsString(t *testing.T) {
	for _, tt := range []struct {
		choiceType ResponsesToolChoiceType
		want       string
	}{
		{ResponsesToolChoiceTypeAuto, `"auto"`},
		{ResponsesToolChoiceTypeNone, `"none"`},
		{ResponsesToolChoiceTypeRequired, `"required"`},
		// "any" is in MarshalJSON's normalization switch alongside the three above, so it belongs
		// in the table: without it the branch that handles Anthropic's spelling has no coverage.
		{ResponsesToolChoiceTypeAny, `"any"`},
	} {
		t.Run(string(tt.choiceType), func(t *testing.T) {
			tc := ResponsesToolChoice{
				ResponsesToolChoiceStruct: &ResponsesToolChoiceStruct{
					Type: tt.choiceType,
					Mode: Ptr(string(tt.choiceType)),
				},
			}

			got, err := tc.MarshalJSON()
			require.NoError(t, err)
			assert.JSONEq(t, tt.want, string(got),
				"a mode-only choice must serialize as a bare string; the object form's `type` names a tool type")
		})
	}
}

// A forced function is a real object choice and must keep that form.
func TestResponsesToolChoice_NamedFunctionStaysAnObject(t *testing.T) {
	tc := ResponsesToolChoice{
		ResponsesToolChoiceStruct: &ResponsesToolChoiceStruct{
			Type: ResponsesToolChoiceTypeFunction,
			Name: Ptr("get_weather"),
		},
	}

	got, err := tc.MarshalJSON()
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"function","name":"get_weather"}`, string(got))
}

// An allowed-tools choice carries a list and must also keep the object form, so the
// normalization must key on the payload rather than on the mode alone.
func TestResponsesToolChoice_AllowedToolsStaysAnObject(t *testing.T) {
	tc := ResponsesToolChoice{
		ResponsesToolChoiceStruct: &ResponsesToolChoiceStruct{
			Type: ResponsesToolChoiceTypeAllowedTools,
			Mode: Ptr("required"),
			Tools: []ResponsesToolChoiceAllowedToolDef{
				{Type: "function", Name: Ptr("get_weather")},
				{Type: "function", Name: Ptr("get_time")},
			},
		},
	}

	got, err := tc.MarshalJSON()
	require.NoError(t, err)
	assert.Contains(t, string(got), `"tools"`, "an allowed-tools choice must not collapse to a string")
}

// The plain string form is untouched.
func TestResponsesToolChoice_StringFormUnchanged(t *testing.T) {
	tc := ResponsesToolChoice{ResponsesToolChoiceStr: Ptr("auto")}

	got, err := tc.MarshalJSON()
	require.NoError(t, err)
	assert.JSONEq(t, `"auto"`, string(got))
}
