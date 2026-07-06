package schemas

import "testing"

// TestNewOpenAIToolSearchCallItem_RejectsNonObjectArguments guards the JSON
// object requirement documented on NewOpenAIToolSearchCallItem: OpenAI's
// tool_search_call.arguments must be a JSON object on the wire, unlike
// function_call's JSON string. Malformed/non-object input (e.g. forwarded
// verbatim from an upstream provider) must fail fast with a clear error
// instead of reaching an OpenAI-compatible backend as invalid raw JSON.
// Found by an automated codeRabbit review pass.
func TestNewOpenAIToolSearchCallItem_RejectsNonObjectArguments(t *testing.T) {
	cases := []struct {
		name    string
		args    string
		wantErr bool
	}{
		{"empty defaults to empty object", "", false},
		{"valid empty object", "{}", false},
		{"valid populated object", `{"query":"weather"}`, false},
		{"bare string rejected", `"weather"`, true},
		{"array rejected", `["weather"]`, true},
		{"malformed json rejected", `{query:weather}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewOpenAIToolSearchCallItem("call_1", tc.args)
			if tc.wantErr && err == nil {
				t.Errorf("expected an error for arguments %q, got nil", tc.args)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("expected no error for arguments %q, got %v", tc.args, err)
			}
		})
	}
}
