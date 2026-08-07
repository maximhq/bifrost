package bedrock

import (
	"strings"
	"testing"
)

// Issue #5762: a tool-result string that parses as a JSON object is promoted to
// a Converse toolResult json block. "__type" is Smithy's type discriminator, and
// Bedrock's Converse validation rejects any json document carrying a nested
// "__type" key with a non-retryable ValidationException. The offending content
// then lives in conversation history, so every subsequent turn replays it and
// the session is permanently wedged. Documents with a nested "__type" must
// travel as text blocks instead (JSON-in-text is valid Converse input). The
// cases mirror the reporter's live-verified behavior matrix.
func TestToolResultNestedSmithyTypeStaysText(t *testing.T) {
	for _, tc := range []struct {
		name     string
		text     string
		wantJSON bool
	}{
		{"nested __type", `{"q": {"__type": 1}}`, false},
		{"nested __type with escaped underscore", `{"q": {"\u005f_type": 1}}`, false},
		{"array wraps to nested __type", `[{"__type": 1}]`, false},
		{"deeply nested __type", `{"a": {"b": [{"c": {"__type": "x"}}]}}`, false},
		{"top-level __type only", `{"__type": 1}`, true},
		{"__typename is fine", `{"q": {"__typename": 1}}`, true},
		{"__schema is fine", `{"q": {"__schema": 1}}`, true},
		{"plain object", `{"q": 1}`, true},
		{"plain array", `[1, 2]`, true},
		{"primitive", `42`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			block := tryParseJSONIntoContentBlock(tc.text)
			gotJSON := block.JSON != nil
			if gotJSON != tc.wantJSON {
				t.Fatalf("promoted to json = %v, want %v (json=%s text=%v)", gotJSON, tc.wantJSON, block.JSON, block.Text)
			}
			if !tc.wantJSON {
				if block.Text == nil || *block.Text != tc.text {
					t.Errorf("demoted block must carry the original text, got %v", block.Text)
				}
			}
			if gotJSON && !strings.HasPrefix(string(block.JSON), "{") {
				t.Errorf("json block must be an object, got %s", block.JSON)
			}
		})
	}
}
