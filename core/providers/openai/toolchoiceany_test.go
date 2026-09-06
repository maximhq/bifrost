package openai

import (
	"testing"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

// The Anthropic integration maps tool_choice {"type": "any"} to the
// provider-generic string "any". OpenAI accepts only "none", "auto" and
// "required" as string tool choices, so "any" must be serialized as "required"
// on both OpenAI egress paths while every other choice is left untouched.

func TestToOpenAIResponsesRequest_ToolChoiceAnyBecomesRequired(t *testing.T) {
	tests := []struct {
		name       string
		toolChoice *schemas.ResponsesToolChoice
		wantJSON   string
	}{
		{
			name:       "any is mapped to required",
			toolChoice: &schemas.ResponsesToolChoice{ResponsesToolChoiceStr: schemas.Ptr("any")},
			wantJSON:   `"required"`,
		},
		{
			name:       "required is preserved",
			toolChoice: &schemas.ResponsesToolChoice{ResponsesToolChoiceStr: schemas.Ptr("required")},
			wantJSON:   `"required"`,
		},
		{
			name:       "auto is preserved",
			toolChoice: &schemas.ResponsesToolChoice{ResponsesToolChoiceStr: schemas.Ptr("auto")},
			wantJSON:   `"auto"`,
		},
		{
			name:       "none is preserved",
			toolChoice: &schemas.ResponsesToolChoice{ResponsesToolChoiceStr: schemas.Ptr("none")},
			wantJSON:   `"none"`,
		},
		{
			name: "named function is preserved",
			toolChoice: &schemas.ResponsesToolChoice{ResponsesToolChoiceStruct: &schemas.ResponsesToolChoiceStruct{
				Type: schemas.ResponsesToolChoiceTypeFunction,
				Name: schemas.Ptr("get_weather"),
			}},
			wantJSON: `{"type":"function","name":"get_weather"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bifrostReq := &schemas.BifrostResponsesRequest{
				Provider: schemas.OpenAI,
				Model:    "gpt-4o",
				Input: []schemas.ResponsesMessage{{
					Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
					Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("What is the weather in Tokyo?")},
				}},
				Params: &schemas.ResponsesParameters{ToolChoice: tt.toolChoice},
			}

			result := ToOpenAIResponsesRequest(nil, bifrostReq)
			if result == nil {
				t.Fatal("ToOpenAIResponsesRequest returned nil")
			}
			got, err := sonic.Marshal(result.ToolChoice)
			if err != nil {
				t.Fatalf("marshal tool_choice: %v", err)
			}
			if string(got) != tt.wantJSON {
				t.Fatalf("tool_choice on the OpenAI wire = %s, want %s", got, tt.wantJSON)
			}
		})
	}
}

func TestToOpenAIResponsesRequest_ToolChoiceAnyDoesNotMutateInput(t *testing.T) {
	params := &schemas.ResponsesParameters{
		ToolChoice: &schemas.ResponsesToolChoice{ResponsesToolChoiceStr: schemas.Ptr("any")},
	}
	bifrostReq := &schemas.BifrostResponsesRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o",
		Params:   params,
	}

	ToOpenAIResponsesRequest(nil, bifrostReq)

	if got := *params.ToolChoice.ResponsesToolChoiceStr; got != "any" {
		t.Fatalf("caller's tool_choice was mutated to %q", got)
	}
}

func TestToOpenAIChatRequest_ToolChoiceAnyBecomesRequired(t *testing.T) {
	tests := []struct {
		name       string
		toolChoice *schemas.ChatToolChoice
		wantJSON   string
	}{
		{
			name:       "any is mapped to required",
			toolChoice: &schemas.ChatToolChoice{ChatToolChoiceStr: schemas.Ptr("any")},
			wantJSON:   `"required"`,
		},
		{
			name:       "required is preserved",
			toolChoice: &schemas.ChatToolChoice{ChatToolChoiceStr: schemas.Ptr("required")},
			wantJSON:   `"required"`,
		},
		{
			name:       "auto is preserved",
			toolChoice: &schemas.ChatToolChoice{ChatToolChoiceStr: schemas.Ptr("auto")},
			wantJSON:   `"auto"`,
		},
		{
			name:       "none is preserved",
			toolChoice: &schemas.ChatToolChoice{ChatToolChoiceStr: schemas.Ptr("none")},
			wantJSON:   `"none"`,
		},
		{
			name: "named function is preserved",
			toolChoice: &schemas.ChatToolChoice{ChatToolChoiceStruct: &schemas.ChatToolChoiceStruct{
				Type:     schemas.ChatToolChoiceTypeFunction,
				Function: &schemas.ChatToolChoiceFunction{Name: "get_weather"},
			}},
			wantJSON: `{"type":"function","function":{"name":"get_weather"}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bifrostReq := &schemas.BifrostChatRequest{
				Provider: schemas.OpenAI,
				Model:    "gpt-4o",
				Input: []schemas.ChatMessage{{
					Role:    schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("What is the weather in Tokyo?")},
				}},
				Params: &schemas.ChatParameters{ToolChoice: tt.toolChoice},
			}

			result := ToOpenAIChatRequest(schemas.NewBifrostContext(nil, schemas.NoDeadline), bifrostReq)
			if result == nil {
				t.Fatal("ToOpenAIChatRequest returned nil")
			}
			got, err := sonic.Marshal(result.ToolChoice)
			if err != nil {
				t.Fatalf("marshal tool_choice: %v", err)
			}
			if string(got) != tt.wantJSON {
				t.Fatalf("tool_choice on the OpenAI wire = %s, want %s", got, tt.wantJSON)
			}
		})
	}
}

func TestToOpenAIChatRequest_ToolChoiceAnyDoesNotMutateInput(t *testing.T) {
	params := &schemas.ChatParameters{
		ToolChoice: &schemas.ChatToolChoice{ChatToolChoiceStr: schemas.Ptr("any")},
	}
	bifrostReq := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o",
		Params:   params,
	}

	ToOpenAIChatRequest(schemas.NewBifrostContext(nil, schemas.NoDeadline), bifrostReq)

	if got := *params.ToolChoice.ChatToolChoiceStr; got != "any" {
		t.Fatalf("caller's tool_choice was mutated to %q", got)
	}
}
