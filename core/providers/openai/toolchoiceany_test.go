package openai

import (
	"testing"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

// The Anthropic integration maps tool_choice {"type": "any"} to the
// provider-generic string "any". OpenAI accepts only "none", "auto" and
// "required" as string tool choices, so "any" must be serialized as "required"
// on both OpenAI egress paths while every other choice is left untouched and
// destinations that accept "any" natively keep it.

// responsesToolChoiceRequest builds a minimal Responses request with one user
// message so conversion reaches the tool-choice handling.
func responsesToolChoiceRequest(provider schemas.ModelProvider, model string, toolChoice *schemas.ResponsesToolChoice) *schemas.BifrostResponsesRequest {
	return &schemas.BifrostResponsesRequest{
		Provider: provider,
		Model:    model,
		Input: []schemas.ResponsesMessage{{
			Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("What is the weather in Tokyo?")},
		}},
		Params: &schemas.ResponsesParameters{ToolChoice: toolChoice},
	}
}

// chatToolChoiceRequest builds a minimal Chat request with one user message so
// conversion reaches the tool-choice handling.
func chatToolChoiceRequest(provider schemas.ModelProvider, model string, toolChoice *schemas.ChatToolChoice) *schemas.BifrostChatRequest {
	return &schemas.BifrostChatRequest{
		Provider: provider,
		Model:    model,
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("What is the weather in Tokyo?")},
		}},
		Params: &schemas.ChatParameters{ToolChoice: toolChoice},
	}
}

// marshalToolChoice returns the wire JSON for a converted tool choice.
func marshalToolChoice(t *testing.T, toolChoice any) string {
	t.Helper()
	got, err := sonic.Marshal(toolChoice)
	if err != nil {
		t.Fatalf("marshal tool_choice: %v", err)
	}
	return string(got)
}

// TestToOpenAIResponsesRequest_ToolChoiceAnyBecomesRequired verifies the
// Responses egress maps "any" to "required" for OpenAI, keeps every other
// choice as is, and leaves "any" intact for Mistral destinations.
func TestToOpenAIResponsesRequest_ToolChoiceAnyBecomesRequired(t *testing.T) {
	tests := []struct {
		name       string
		provider   schemas.ModelProvider
		model      string
		toolChoice *schemas.ResponsesToolChoice
		wantJSON   string
	}{
		{
			name:       "any is mapped to required",
			provider:   schemas.OpenAI,
			model:      "gpt-4o",
			toolChoice: &schemas.ResponsesToolChoice{ResponsesToolChoiceStr: schemas.Ptr("any")},
			wantJSON:   `"required"`,
		},
		{
			name:       "required is preserved",
			provider:   schemas.OpenAI,
			model:      "gpt-4o",
			toolChoice: &schemas.ResponsesToolChoice{ResponsesToolChoiceStr: schemas.Ptr("required")},
			wantJSON:   `"required"`,
		},
		{
			name:       "auto is preserved",
			provider:   schemas.OpenAI,
			model:      "gpt-4o",
			toolChoice: &schemas.ResponsesToolChoice{ResponsesToolChoiceStr: schemas.Ptr("auto")},
			wantJSON:   `"auto"`,
		},
		{
			name:       "none is preserved",
			provider:   schemas.OpenAI,
			model:      "gpt-4o",
			toolChoice: &schemas.ResponsesToolChoice{ResponsesToolChoiceStr: schemas.Ptr("none")},
			wantJSON:   `"none"`,
		},
		{
			name:     "named function is preserved",
			provider: schemas.OpenAI,
			model:    "gpt-4o",
			toolChoice: &schemas.ResponsesToolChoice{ResponsesToolChoiceStruct: &schemas.ResponsesToolChoiceStruct{
				Type: schemas.ResponsesToolChoiceTypeFunction,
				Name: schemas.Ptr("get_weather"),
			}},
			wantJSON: `{"type":"function","name":"get_weather"}`,
		},
		{
			name:       "Mistral keeps any",
			provider:   schemas.Mistral,
			model:      "mistral-large-latest",
			toolChoice: &schemas.ResponsesToolChoice{ResponsesToolChoiceStr: schemas.Ptr("any")},
			wantJSON:   `"any"`,
		},
		{
			name:       "Mistral on Vertex keeps any",
			provider:   schemas.Vertex,
			model:      "mistral-large",
			toolChoice: &schemas.ResponsesToolChoice{ResponsesToolChoiceStr: schemas.Ptr("any")},
			wantJSON:   `"any"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ToOpenAIResponsesRequest(nil, responsesToolChoiceRequest(tt.provider, tt.model, tt.toolChoice))
			if result == nil {
				t.Fatal("ToOpenAIResponsesRequest returned nil")
			}
			if got := marshalToolChoice(t, result.ToolChoice); got != tt.wantJSON {
				t.Fatalf("tool_choice on the wire = %s, want %s", got, tt.wantJSON)
			}
		})
	}
}

// TestToOpenAIResponsesRequest_ToolChoiceAnyDoesNotMutateInput verifies the
// Responses egress rewrites a copy of the tool choice, not the caller's value.
func TestToOpenAIResponsesRequest_ToolChoiceAnyDoesNotMutateInput(t *testing.T) {
	toolChoice := &schemas.ResponsesToolChoice{ResponsesToolChoiceStr: schemas.Ptr("any")}
	bifrostReq := responsesToolChoiceRequest(schemas.OpenAI, "gpt-4o", toolChoice)

	result := ToOpenAIResponsesRequest(nil, bifrostReq)
	if result == nil {
		t.Fatal("ToOpenAIResponsesRequest returned nil")
	}
	if got := marshalToolChoice(t, result.ToolChoice); got != `"required"` {
		t.Fatalf("conversion did not run: tool_choice on the wire = %s", got)
	}
	if got := *bifrostReq.Params.ToolChoice.ResponsesToolChoiceStr; got != "any" {
		t.Fatalf("caller's tool_choice was mutated to %q", got)
	}
	if bifrostReq.Params.ToolChoice != toolChoice {
		t.Fatal("caller's tool_choice pointer was replaced")
	}
}

// TestToOpenAIChatRequest_ToolChoiceAnyBecomesRequired verifies the Chat
// egress maps "any" to "required" for OpenAI, keeps every other choice as is,
// and leaves "any" intact for Mistral destinations.
func TestToOpenAIChatRequest_ToolChoiceAnyBecomesRequired(t *testing.T) {
	tests := []struct {
		name       string
		provider   schemas.ModelProvider
		model      string
		toolChoice *schemas.ChatToolChoice
		wantJSON   string
	}{
		{
			name:       "any is mapped to required",
			provider:   schemas.OpenAI,
			model:      "gpt-4o",
			toolChoice: &schemas.ChatToolChoice{ChatToolChoiceStr: schemas.Ptr("any")},
			wantJSON:   `"required"`,
		},
		{
			name:       "required is preserved",
			provider:   schemas.OpenAI,
			model:      "gpt-4o",
			toolChoice: &schemas.ChatToolChoice{ChatToolChoiceStr: schemas.Ptr("required")},
			wantJSON:   `"required"`,
		},
		{
			name:       "auto is preserved",
			provider:   schemas.OpenAI,
			model:      "gpt-4o",
			toolChoice: &schemas.ChatToolChoice{ChatToolChoiceStr: schemas.Ptr("auto")},
			wantJSON:   `"auto"`,
		},
		{
			name:       "none is preserved",
			provider:   schemas.OpenAI,
			model:      "gpt-4o",
			toolChoice: &schemas.ChatToolChoice{ChatToolChoiceStr: schemas.Ptr("none")},
			wantJSON:   `"none"`,
		},
		{
			name:     "named function is preserved",
			provider: schemas.OpenAI,
			model:    "gpt-4o",
			toolChoice: &schemas.ChatToolChoice{ChatToolChoiceStruct: &schemas.ChatToolChoiceStruct{
				Type:     schemas.ChatToolChoiceTypeFunction,
				Function: &schemas.ChatToolChoiceFunction{Name: "get_weather"},
			}},
			wantJSON: `{"type":"function","function":{"name":"get_weather"}}`,
		},
		{
			name:       "Mistral keeps any",
			provider:   schemas.Mistral,
			model:      "mistral-large-latest",
			toolChoice: &schemas.ChatToolChoice{ChatToolChoiceStr: schemas.Ptr("any")},
			wantJSON:   `"any"`,
		},
		{
			name:       "Mistral on Vertex keeps any",
			provider:   schemas.Vertex,
			model:      "mistral-large",
			toolChoice: &schemas.ChatToolChoice{ChatToolChoiceStr: schemas.Ptr("any")},
			wantJSON:   `"any"`,
		},
		{
			name:       "non-Mistral model on Vertex is mapped to required",
			provider:   schemas.Vertex,
			model:      "moonshotai/kimi-k2-thinking-maas",
			toolChoice: &schemas.ChatToolChoice{ChatToolChoiceStr: schemas.Ptr("any")},
			wantJSON:   `"required"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ToOpenAIChatRequest(schemas.NewBifrostContext(nil, schemas.NoDeadline), chatToolChoiceRequest(tt.provider, tt.model, tt.toolChoice))
			if result == nil {
				t.Fatal("ToOpenAIChatRequest returned nil")
			}
			if got := marshalToolChoice(t, result.ToolChoice); got != tt.wantJSON {
				t.Fatalf("tool_choice on the wire = %s, want %s", got, tt.wantJSON)
			}
		})
	}
}

// TestToOpenAIChatRequest_ToolChoiceAnyDoesNotMutateInput verifies the Chat
// egress rewrites a copy of the tool choice, not the caller's value.
func TestToOpenAIChatRequest_ToolChoiceAnyDoesNotMutateInput(t *testing.T) {
	toolChoice := &schemas.ChatToolChoice{ChatToolChoiceStr: schemas.Ptr("any")}
	bifrostReq := chatToolChoiceRequest(schemas.OpenAI, "gpt-4o", toolChoice)

	result := ToOpenAIChatRequest(schemas.NewBifrostContext(nil, schemas.NoDeadline), bifrostReq)
	if result == nil {
		t.Fatal("ToOpenAIChatRequest returned nil")
	}
	if got := marshalToolChoice(t, result.ToolChoice); got != `"required"` {
		t.Fatalf("conversion did not run: tool_choice on the wire = %s", got)
	}
	if got := *bifrostReq.Params.ToolChoice.ChatToolChoiceStr; got != "any" {
		t.Fatalf("caller's tool_choice was mutated to %q", got)
	}
	if bifrostReq.Params.ToolChoice != toolChoice {
		t.Fatal("caller's tool_choice pointer was replaced")
	}
}
