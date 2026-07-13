package anthropic

import (
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// makeResponsesTextFormat returns a minimal json_schema text config for the
// Responses API structured-output request path.
func makeResponsesTextFormat(schemaName string) *schemas.ResponsesTextConfig {
	properties := map[string]any{
		"color":  map[string]interface{}{"type": "string"},
		"animal": map[string]interface{}{"type": "string"},
	}
	return &schemas.ResponsesTextConfig{
		Format: &schemas.ResponsesTextConfigFormat{
			Type: "json_schema",
			Name: schemas.Ptr(schemaName),
			JSONSchema: &schemas.ResponsesTextConfigFormatJSONSchema{
				Type:       schemas.Ptr("object"),
				Properties: schemas.OrderedMapFromMap(properties),
				Required:   []string{"color", "animal"},
			},
		},
	}
}

// TestToAnthropicResponsesRequest_StructuredOutput_ToolConversion verifies that,
// mirroring the Chat Completions path, providers whose native Anthropic endpoint
// rejects output_config.format get structured output converted into a synthetic
// bf_so_*/json_response tool instead. Any provider added to toolConversionProviders
// in the future must also be added to the branch under test in responses.go.
func TestToAnthropicResponsesRequest_StructuredOutput_ToolConversion(t *testing.T) {
	for _, provider := range toolConversionProviders {
		t.Run(string(provider), func(t *testing.T) {
			req := &schemas.BifrostResponsesRequest{
				Provider: provider,
				Model:    "claude-opus-4-6",
				Input: []schemas.ResponsesMessage{
					{
						Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
						Content: &schemas.ResponsesMessageContent{
							ContentStr: schemas.Ptr("Hello"),
						},
					},
				},
				Params: &schemas.ResponsesParameters{
					Text: makeResponsesTextFormat("my_schema"),
				},
			}

			ctx := schemas.NewBifrostContext(nil, time.Time{})
			result, err := ToAnthropicResponsesRequest(ctx, req)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if result.OutputConfig != nil {
				t.Errorf("expected OutputConfig to stay unset for %s (native field unsupported), got %+v", provider, result.OutputConfig)
			}

			found := false
			for _, tool := range result.Tools {
				if tool.Name == "bf_so_my_schema" {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("expected a synthetic tool named %q to be added for %s structured output", "bf_so_my_schema", provider)
			}

			if result.ToolChoice == nil || result.ToolChoice.Name != "bf_so_my_schema" {
				t.Errorf("expected ToolChoice to be forced to the synthetic tool for %s, got %+v", provider, result.ToolChoice)
			}
		})
	}
}

func TestConvertBifrostToolToAnthropic_RawParameters(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "empty schema uses Anthropic object fallback",
			raw:  `{ }`,
			want: `{"type":"object","properties":{}}`,
		},
		{
			name: "populated schema remains raw",
			raw:  `{"x-provider-key":{"enabled":true},"type":"object"}`,
			want: `{"x-provider-key":{"enabled":true},"type":"object"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params, err := schemas.NewRawToolFunctionParameters([]byte(tt.raw))
			if err != nil {
				t.Fatalf("create raw tool parameters: %v", err)
			}

			name := "test_tool"
			got := convertBifrostToolToAnthropic("claude-sonnet-4-20250514", &schemas.ResponsesTool{
				Type: schemas.ResponsesToolTypeFunction,
				Name: &name,
				ResponsesToolFunction: &schemas.ResponsesToolFunction{
					Parameters: params,
				},
			}, schemas.Anthropic, false)
			if got == nil || got.InputSchema == nil {
				t.Fatal("expected input schema")
			}

			marshaled, err := schemas.Marshal(got.InputSchema)
			if err != nil {
				t.Fatalf("marshal input schema: %v", err)
			}
			if string(marshaled) != tt.want {
				t.Fatalf("unexpected input schema:\n got: %s\nwant: %s", marshaled, tt.want)
			}
		})
	}
}

// TestToAnthropicResponsesRequest_StructuredOutput_NativeOutputConfig_Anthropic is the
// negative-case control: Anthropic itself supports output_config.format natively, so no
// synthetic tool should be added. This is the branch that Azure incorrectly took before
// being added to toolConversionProviders.
func TestToAnthropicResponsesRequest_StructuredOutput_NativeOutputConfig_Anthropic(t *testing.T) {
	req := &schemas.BifrostResponsesRequest{
		Provider: schemas.Anthropic,
		Model:    "claude-opus-4-6",
		Input: []schemas.ResponsesMessage{
			{
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{
					ContentStr: schemas.Ptr("Hello"),
				},
			},
		},
		Params: &schemas.ResponsesParameters{
			Text: makeResponsesTextFormat("my_schema"),
		},
	}

	ctx := schemas.NewBifrostContext(nil, time.Time{})
	result, err := ToAnthropicResponsesRequest(ctx, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.OutputConfig == nil || result.OutputConfig.Format == nil {
		t.Fatal("expected OutputConfig.Format to be set natively for Anthropic")
	}

	for _, tool := range result.Tools {
		if tool.Name == "bf_so_my_schema" {
			t.Errorf("did not expect a synthetic tool for Anthropic, got %q", tool.Name)
		}
	}
}
