package gemini

// Regression tests for issue #6507: the native /genai ingress silently dropped
// tool parameters when the client (google-genai >= 2.0.0, incl. Google ADK)
// serialized FunctionDeclaration's schema as snake_case
// `parameters_json_schema` instead of camelCase `parametersJsonSchema`.
// Tool.UnmarshalJSON already aliased the outer function_declarations key, but
// FunctionDeclaration itself had no alias shim, so the nested schema fell
// through struct-tag matching (case-insensitive, punctuation-sensitive) and the
// tool was forwarded upstream with no parameters, silently.
//
// Uses sonic.Unmarshal, the same library the real ingress uses
// (transports/bifrost-http/integrations/router.go unmarshals the raw body into
// GeminiGenerationRequest, whose Tools field holds []Tool).

import (
	"testing"

	"github.com/bytedance/sonic"
)

func unmarshalSingleDeclTool(t *testing.T, payload string) *FunctionDeclaration {
	t.Helper()
	var tool Tool
	if err := sonic.Unmarshal([]byte(payload), &tool); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if len(tool.FunctionDeclarations) != 1 {
		t.Fatalf("expected 1 function declaration, got %d", len(tool.FunctionDeclarations))
	}
	return tool.FunctionDeclarations[0]
}

func TestFunctionDeclarationSnakeCaseAliases(t *testing.T) {
	t.Run("camelCase parametersJsonSchema binds", func(t *testing.T) {
		fd := unmarshalSingleDeclTool(t, `{
			"functionDeclarations": [{
				"name": "get_weather",
				"description": "Get the weather",
				"parametersJsonSchema": {
					"type": "object",
					"properties": {"location": {"type": "string"}},
					"required": ["location"]
				}
			}]
		}`)
		if fd.ParametersJSONSchema == nil {
			t.Fatalf("camelCase parametersJsonSchema was dropped: %+v", fd)
		}
	})

	t.Run("snake_case parameters_json_schema binds", func(t *testing.T) {
		// The exact shape google-genai >= 2.0.0 / Google ADK emits.
		fd := unmarshalSingleDeclTool(t, `{
			"functionDeclarations": [{
				"name": "get_weather",
				"description": "Get the weather",
				"parameters_json_schema": {
					"type": "object",
					"properties": {"location": {"type": "string"}},
					"required": ["location"]
				}
			}]
		}`)
		if fd.ParametersJSONSchema == nil {
			t.Fatalf("snake_case parameters_json_schema was dropped: tool forwarded with no parameters. fd=%+v", fd)
		}
		schema, ok := fd.ParametersJSONSchema.(map[string]interface{})
		if !ok {
			t.Fatalf("expected schema object, got %T", fd.ParametersJSONSchema)
		}
		if schema["type"] != "object" {
			t.Errorf("schema content lost: %+v", schema)
		}
	})

	t.Run("snake_case response_json_schema binds", func(t *testing.T) {
		fd := unmarshalSingleDeclTool(t, `{
			"functionDeclarations": [{
				"name": "get_weather",
				"response_json_schema": {"type": "string"}
			}]
		}`)
		if fd.ResponseJSONSchema == nil {
			t.Fatalf("snake_case response_json_schema was dropped. fd=%+v", fd)
		}
	})

	t.Run("camelCase wins when both spellings present", func(t *testing.T) {
		fd := unmarshalSingleDeclTool(t, `{
			"functionDeclarations": [{
				"name": "get_weather",
				"parametersJsonSchema": {"type": "object", "marker": "camel"},
				"parameters_json_schema": {"type": "object", "marker": "snake"}
			}]
		}`)
		schema, ok := fd.ParametersJSONSchema.(map[string]interface{})
		if !ok {
			t.Fatalf("expected schema object, got %T", fd.ParametersJSONSchema)
		}
		if schema["marker"] != "camel" {
			t.Errorf("camelCase key must take precedence, got marker=%v", schema["marker"])
		}
	})

	t.Run("explicit camelCase null wins over snake_case value", func(t *testing.T) {
		// An explicit null decodes to nil just like an omitted key; presence of
		// the camelCase key must still block the snake_case fallback.
		fd := unmarshalSingleDeclTool(t, `{
			"functionDeclarations": [{
				"name": "get_weather",
				"parametersJsonSchema": null,
				"parameters_json_schema": {"type": "object", "marker": "snake"}
			}]
		}`)
		if fd.ParametersJSONSchema != nil {
			t.Errorf("explicit camelCase null must not be overridden by snake_case, got %+v", fd.ParametersJSONSchema)
		}
	})

	t.Run("explicit snake_case null clears a reused declaration", func(t *testing.T) {
		// Decoding into a reused struct: a payload carrying an explicit
		// parameters_json_schema null (and no camelCase key) must clear the
		// stale value, not leave the previous schema behind.
		var fd FunctionDeclaration
		if err := sonic.Unmarshal([]byte(`{"name":"a","parameters_json_schema":{"type":"object"}}`), &fd); err != nil {
			t.Fatalf("first unmarshal: %v", err)
		}
		if fd.ParametersJSONSchema == nil {
			t.Fatal("precondition: first decode must set the schema")
		}
		if err := sonic.Unmarshal([]byte(`{"name":"b","parameters_json_schema":null}`), &fd); err != nil {
			t.Fatalf("second unmarshal: %v", err)
		}
		if fd.ParametersJSONSchema != nil {
			t.Errorf("explicit snake_case null must clear the reused declaration, got %+v", fd.ParametersJSONSchema)
		}
	})

	t.Run("plain parameters still binds through the shim", func(t *testing.T) {
		fd := unmarshalSingleDeclTool(t, `{
			"function_declarations": [{
				"name": "get_weather",
				"parameters": {
					"type": "OBJECT",
					"properties": {"location": {"type": "STRING"}},
					"required": ["location"]
				}
			}]
		}`)
		if fd.Parameters == nil {
			t.Fatalf("plain parameters was dropped. fd=%+v", fd)
		}
		if len(fd.Parameters.Properties) != 1 {
			t.Fatalf("parameters properties lost: %+v", fd.Parameters)
		}
	})
}
