package anthropic

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

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
