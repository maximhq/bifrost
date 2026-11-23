package openai

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestOpenAIConverters_PreserveRawToolParameters(t *testing.T) {
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	tests := []struct {
		name string
		raw  string
	}{
		{
			name: "empty schema",
			raw:  `{ }`,
		},
		{
			name: "populated schema",
			raw:  `{"x-provider-key":{"enabled":true},"type":"object"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params, err := schemas.NewRawToolFunctionParameters([]byte(tt.raw))
			if err != nil {
				t.Fatalf("create raw tool parameters: %v", err)
			}

			t.Run("chat completions", func(t *testing.T) {
				result := ToOpenAIChatRequest(ctx, &schemas.BifrostChatRequest{
					Provider: schemas.OpenAI,
					Model:    "gpt-4o",
					Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser}},
					Params: &schemas.ChatParameters{Tools: []schemas.ChatTool{{
						Type: schemas.ChatToolTypeFunction,
						Function: &schemas.ChatToolFunction{
							Name:       "test_tool",
							Parameters: params,
						},
					}}},
				})
				if result == nil {
					t.Fatal("expected OpenAI chat request")
				}
				assertRawToolParameters(t, result.Tools[0].Function.Parameters, tt.raw)
			})

			t.Run("responses", func(t *testing.T) {
				result := ToOpenAIResponsesRequest(nil, &schemas.BifrostResponsesRequest{
					Provider: schemas.OpenAI,
					Model:    "gpt-4o",
					Input: []schemas.ResponsesMessage{{
						Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
					}},
					Params: &schemas.ResponsesParameters{Tools: []schemas.ResponsesTool{{
						Type: schemas.ResponsesToolTypeFunction,
						Name: schemas.Ptr("test_tool"),
						ResponsesToolFunction: &schemas.ResponsesToolFunction{
							Parameters: params,
						},
					}}},
				})
				if result == nil {
					t.Fatal("expected OpenAI responses request")
				}
				assertRawToolParameters(t, result.Tools[0].ResponsesToolFunction.Parameters, tt.raw)
			})
		})
	}
}

func assertRawToolParameters(t *testing.T, params *schemas.ToolFunctionParameters, want string) {
	t.Helper()
	if params == nil || !params.HasRawJSON() {
		t.Fatal("expected raw-backed tool parameters")
	}

	marshaled, err := schemas.Marshal(params)
	if err != nil {
		t.Fatalf("marshal tool parameters: %v", err)
	}
	if string(marshaled) != want {
		t.Fatalf("unexpected tool parameters:\n got: %s\nwant: %s", marshaled, want)
	}
}
