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

// IsForced drives the Fable 5.1+ gate that drops a forced tool choice rather
// than letting the provider reject it. Only "none" and "auto" are unforced;
// an allowed-tools set is a constraint, so only its mode forces.

func TestChatToolChoice_IsForced(t *testing.T) {
	for _, tt := range []struct {
		name   string
		choice *ChatToolChoice
		want   bool
	}{
		{"nil", nil, false},
		{"empty", &ChatToolChoice{}, false},
		{"str auto", &ChatToolChoice{ChatToolChoiceStr: Ptr("auto")}, false},
		{"str none", &ChatToolChoice{ChatToolChoiceStr: Ptr("none")}, false},
		{"str any", &ChatToolChoice{ChatToolChoiceStr: Ptr("any")}, true},
		{"str required", &ChatToolChoice{ChatToolChoiceStr: Ptr("required")}, true},
		{"struct auto", &ChatToolChoice{ChatToolChoiceStruct: &ChatToolChoiceStruct{Type: ChatToolChoiceTypeAuto}}, false},
		{"struct any", &ChatToolChoice{ChatToolChoiceStruct: &ChatToolChoiceStruct{Type: ChatToolChoiceTypeAny}}, true},
		{"struct function", &ChatToolChoice{ChatToolChoiceStruct: &ChatToolChoiceStruct{
			Type: ChatToolChoiceTypeFunction, Function: &ChatToolChoiceFunction{Name: "get_weather"}}}, true},
		{"allowed_tools auto mode", &ChatToolChoice{ChatToolChoiceStruct: &ChatToolChoiceStruct{
			Type: ChatToolChoiceTypeAllowedTools, AllowedTools: &ChatToolChoiceAllowedTools{Mode: "auto"}}}, false},
		{"allowed_tools required mode", &ChatToolChoice{ChatToolChoiceStruct: &ChatToolChoiceStruct{
			Type: ChatToolChoiceTypeAllowedTools, AllowedTools: &ChatToolChoiceAllowedTools{Mode: "required"}}}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.choice.IsForced())
		})
	}
}

func TestResponsesToolChoice_IsForced(t *testing.T) {
	for _, tt := range []struct {
		name   string
		choice *ResponsesToolChoice
		want   bool
	}{
		{"nil", nil, false},
		{"empty", &ResponsesToolChoice{}, false},
		{"str auto", &ResponsesToolChoice{ResponsesToolChoiceStr: Ptr("auto")}, false},
		{"str none", &ResponsesToolChoice{ResponsesToolChoiceStr: Ptr("none")}, false},
		{"str any", &ResponsesToolChoice{ResponsesToolChoiceStr: Ptr("any")}, true},
		{"str required", &ResponsesToolChoice{ResponsesToolChoiceStr: Ptr("required")}, true},
		{"struct auto", &ResponsesToolChoice{ResponsesToolChoiceStruct: &ResponsesToolChoiceStruct{Type: ResponsesToolChoiceTypeAuto}}, false},
		{"struct any", &ResponsesToolChoice{ResponsesToolChoiceStruct: &ResponsesToolChoiceStruct{Type: ResponsesToolChoiceTypeAny}}, true},
		{"struct function", &ResponsesToolChoice{ResponsesToolChoiceStruct: &ResponsesToolChoiceStruct{
			Type: ResponsesToolChoiceTypeFunction, Name: Ptr("get_weather")}}, true},
		{"mode-only auto", &ResponsesToolChoice{ResponsesToolChoiceStruct: &ResponsesToolChoiceStruct{Mode: Ptr("auto")}}, false},
		{"mode-only required", &ResponsesToolChoice{ResponsesToolChoiceStruct: &ResponsesToolChoiceStruct{Mode: Ptr("required")}}, true},
		{"allowed_tools auto mode", &ResponsesToolChoice{ResponsesToolChoiceStruct: &ResponsesToolChoiceStruct{
			Type: ResponsesToolChoiceTypeAllowedTools, Mode: Ptr("auto")}}, false},
		{"allowed_tools required mode", &ResponsesToolChoice{ResponsesToolChoiceStruct: &ResponsesToolChoiceStruct{
			Type: ResponsesToolChoiceTypeAllowedTools, Mode: Ptr("required")}}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.choice.IsForced())
		})
	}
}
