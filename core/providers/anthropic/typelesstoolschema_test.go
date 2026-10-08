package anthropic

import (
	"testing"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

// Anthropic rejects a custom tool without input_schema, and requires its root
// `type` to be "object". A chat function tool whose parameters root has no
// `type` is carried into input_schema: a root oneOf/anyOf/allOf (common for
// MCP and zod-generated schemas) is rewritten into the object schema Anthropic
// accepts, exactly as a typed root composition is, and any other typeless root
// gets `"type":"object"` with every other key kept. A root with another `type`
// is the client's error and is carried unchanged.
func TestConvertFunctionToolToAnthropic_TypelessRoot(t *testing.T) {
	for _, tc := range []struct {
		name string
		tool string // the OpenAI chat tool as a client sends it
		want string // its input_schema
	}{
		{"root_anyOf", `{"type":"function","function":{"name":"g","parameters":{"anyOf":[{"type":"object","properties":{"a":{"type":"string"}},"required":["a"]},{"type":"object","properties":{"b":{"type":"integer"}},"required":["b"]}]}}}`,
			`{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"integer"}}}`},
		{"root_oneOf", `{"type":"function","function":{"name":"g","parameters":{"oneOf":[{"type":"object","properties":{"a":{"type":"string"}},"required":["a"]},{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"integer"}},"required":["a","b"]}]}}}`,
			`{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"integer"}},"required":["a"]}`},
		{"root_allOf", `{"type":"function","function":{"name":"g","parameters":{"allOf":[{"type":"object","properties":{"a":{"type":"string"}},"required":["a"]},{"type":"object","properties":{"b":{"type":"integer"}}}]}}}`,
			`{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"integer"}},"required":["a"]}`},
		// Typeless non-composition roots: `"type":"object"` added, every other
		// key kept.
		{"root_empty_object", `{"type":"function","function":{"name":"g","parameters":{}}}`,
			`{"type":"object","properties":{}}`},
		{"root_description_only", `{"type":"function","function":{"name":"g","parameters":{"description":"d"}}}`,
			`{"type":"object","description":"d","properties":{}}`},
		{"root_bare_properties", `{"type":"function","function":{"name":"g","parameters":{"properties":{"a":{"type":"string"}},"required":["a"]}}}`,
			`{"type":"object","properties":{"a":{"type":"string"}},"required":["a"]}`},
		{"root_ref_only", `{"type":"function","function":{"name":"g","parameters":{"$ref":"#/$defs/A","$defs":{"A":{"type":"object","properties":{"a":{"type":"string"}}}}}}}`,
			`{"type":"object","properties":{},"$defs":{"A":{"type":"object","properties":{"a":{"type":"string"}}}},"$ref":"#/$defs/A"}`},
		{"root_extra_keys", `{"type":"function","function":{"name":"g","parameters":{"title":"T","additionalProperties":false,"properties":{"a":{"type":"string"}},"description":"d"}}}`,
			`{"type":"object","description":"d","properties":{"a":{"type":"string"}},"additionalProperties":false,"title":"T"}`},
		// A root with a type other than "object" is the client's error: it is
		// carried unchanged and Anthropic's 400 is the correct answer.
		{"root_type_not_object", `{"type":"function","function":{"name":"g","parameters":{"type":"array","items":{"type":"string"}}}}`,
			`{"type":"array","properties":{},"items":{"type":"string"}}`},
		// A typed root converts as before.
		{"typed_root_full", `{"type":"function","function":{"name":"g","parameters":{"type":"object","properties":{"a":{"type":"string"}},"required":["a"]}}}`,
			`{"type":"object","properties":{"a":{"type":"string"}},"required":["a"]}`},
		{"typed_root_oneOf", `{"type":"function","function":{"name":"g","parameters":{"type":"object","oneOf":[{"type":"object","properties":{"a":{"type":"string"}},"required":["a"]},{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"integer"}},"required":["a","b"]}]}}}`,
			`{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"integer"}},"required":["a"]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var tool schemas.ChatTool
			if err := sonic.Unmarshal([]byte(tc.tool), &tool); err != nil {
				t.Fatalf("decode tool: %v", err)
			}
			before, err := sonic.Marshal(tool.Function.Parameters)
			if err != nil {
				t.Fatalf("marshal parameters: %v", err)
			}
			converted, err := convertFunctionToolToAnthropic(tool)
			if err != nil {
				t.Fatalf("convert: %v", err)
			}
			// The caller's parameters are not mutated by the conversion.
			if after, err := sonic.Marshal(tool.Function.Parameters); err != nil || string(after) != string(before) {
				t.Errorf("parameters after convert = %s (err %v), want %s", after, err, before)
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
			// Every input_schema a valid client tool produces is rooted at
			// "type":"object", which Anthropic requires.
			var root struct {
				Type *string `json:"type"`
			}
			if err := sonic.Unmarshal(got.InputSchema, &root); err != nil {
				t.Fatalf("decode input_schema root: %v", err)
			}
			if tc.name != "root_type_not_object" && (root.Type == nil || *root.Type != "object") {
				t.Errorf("input_schema root type = %v, want \"object\" (%s)", root.Type, got.InputSchema)
			}
			// The input_schema on the wire survives a decode/encode round trip
			// unchanged, so a re-serialized request sends the same bytes.
			var params schemas.ToolFunctionParameters
			if err := sonic.Unmarshal(got.InputSchema, &params); err != nil {
				t.Fatalf("decode input_schema: %v", err)
			}
			again, err := sonic.Marshal(params)
			if err != nil {
				t.Fatalf("re-marshal input_schema: %v", err)
			}
			if string(again) != tc.want {
				t.Errorf("input_schema round trip = %s, want %s", again, tc.want)
			}
		})
	}
}

// The Responses converter sends the same input_schema roots: a typeless root
// gets `"type":"object"` (Anthropic requires it) with every other key kept, on
// a copy, so the caller's parameters are not mutated.
func TestConvertBifrostToolsToAnthropic_InputSchemaObjectRoot(t *testing.T) {
	for _, tc := range []struct {
		name       string
		parameters string // "" = no parameters
		want       string
	}{
		{"parameters_absent", "", `{"type":"object","properties":{}}`},
		{"parameters_empty_object", `{}`, `{"type":"object","properties":{}}`},
		{"root_description_only", `{"description":"d"}`, `{"type":"object","description":"d","properties":{}}`},
		{"root_bare_properties", `{"properties":{"a":{"type":"string"}},"required":["a"]}`,
			`{"type":"object","properties":{"a":{"type":"string"}},"required":["a"]}`},
		{"root_ref_only", `{"$ref":"#/$defs/A","$defs":{"A":{"type":"object"}}}`,
			`{"type":"object","properties":{},"$defs":{"A":{"type":"object"}},"$ref":"#/$defs/A"}`},
		{"root_anyOf", `{"anyOf":[{"type":"object","properties":{"a":{"type":"string"}},"required":["a"]},{"type":"object","properties":{"b":{"type":"integer"}},"required":["b"]}]}`,
			`{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"integer"}}}`},
		{"parameters_full", `{"type":"object","properties":{"a":{"type":"string"}},"required":["a"]}`,
			`{"type":"object","properties":{"a":{"type":"string"}},"required":["a"]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			name := "g"
			tool := schemas.ResponsesTool{
				Type:                  schemas.ResponsesToolTypeFunction,
				Name:                  &name,
				ResponsesToolFunction: &schemas.ResponsesToolFunction{},
			}
			var before []byte
			if tc.parameters != "" {
				var params schemas.ToolFunctionParameters
				if err := sonic.Unmarshal([]byte(tc.parameters), &params); err != nil {
					t.Fatalf("decode parameters: %v", err)
				}
				tool.ResponsesToolFunction.Parameters = &params
				var err error
				if before, err = sonic.Marshal(&params); err != nil {
					t.Fatalf("marshal parameters: %v", err)
				}
			}
			converted, _, err := convertBifrostToolsToAnthropic(
				schemas.ResolveModelCaps(schemas.Anthropic, "claude-fable-5"),
				[]schemas.ResponsesTool{tool},
				schemas.Anthropic,
			)
			if err != nil {
				t.Fatalf("convert: %v", err)
			}
			if len(converted) != 1 {
				t.Fatalf("converted %d tools, want 1", len(converted))
			}
			if tc.parameters != "" {
				if after, err := sonic.Marshal(tool.ResponsesToolFunction.Parameters); err != nil || string(after) != string(before) {
					t.Errorf("parameters after convert = %s (err %v), want %s", after, err, before)
				}
			}
			got, err := sonic.Marshal(converted[0].InputSchema)
			if err != nil {
				t.Fatalf("marshal input_schema: %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("input_schema = %s, want %s", got, tc.want)
			}
		})
	}
}
