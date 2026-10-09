package anthropic

import (
	"testing"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

// Anthropic requires input_schema on every custom tool. A chat function tool
// without parameters, or with a raw `{}`, still sends one; any other schema is
// converted as before.
func TestConvertFunctionToolToAnthropic_InputSchema(t *testing.T) {
	for _, tc := range []struct {
		name string
		tool string // the OpenAI chat tool as a client sends it
		want string // its input_schema; "" = absent
	}{
		{"parameters_absent", `{"type":"function","function":{"name":"g"}}`, `{"type":"object","properties":{}}`},
		{"parameters_empty_object", `{"type":"function","function":{"name":"g","parameters":{}}}`, `{}`},
		{"parameters_type_only", `{"type":"function","function":{"name":"g","parameters":{"type":"object"}}}`, `{"type":"object","properties":{}}`},
		{"parameters_full", `{"type":"function","function":{"name":"g","parameters":{"type":"object","properties":{"a":{"type":"string"}},"required":["a"]}}}`,
			`{"type":"object","properties":{"a":{"type":"string"}},"required":["a"]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var tool schemas.ChatTool
			if err := sonic.Unmarshal([]byte(tc.tool), &tool); err != nil {
				t.Fatalf("decode tool: %v", err)
			}
			converted, err := convertFunctionToolToAnthropic(tool)
			if err != nil {
				t.Fatalf("convert: %v", err)
			}
			wire, err := sonic.Marshal(converted)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var got struct {
				InputSchema sonic.NoCopyRawMessage `json:"input_schema"`
			}
			if err := sonic.Unmarshal(wire, &got); err != nil {
				t.Fatalf("decode wire: %v", err)
			}
			if string(got.InputSchema) != tc.want {
				t.Errorf("input_schema = %s, want %s (tool on the wire: %s)", got.InputSchema, tc.want, wire)
			}
		})
	}
}
