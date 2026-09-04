package openai

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// The Anthropic ingress encodes thinking:{type:"disabled"} as reasoning effort
// "none" (core/providers/anthropic/responses.go). Copilot publishes no "off"
// rung and rejects that value, so it is clamped to the model's floor rather than
// dropped — dropping would fall back to the model's default and turn reasoning
// on, the inverse of what the caller asked for.
var noneEffortCases = []struct {
	name     string
	provider schemas.ModelProvider
	model    string
	want     string
}{
	// Copilot's own 400 for this model listed [low medium high xhigh max], so "low" is its floor.
	{name: "copilot claude clamps to low", provider: schemas.Copilot, model: "claude-sonnet-5", want: schemas.ReasoningEffortLow},
	{name: "copilot gpt-5 clamps to its own floor", provider: schemas.Copilot, model: "gpt-5", want: schemas.ReasoningEffortMinimal},
	// Non-Copilot providers are deliberately untouched: only observed behaviour is asserted.
	{name: "openai keeps none", provider: schemas.OpenAI, model: "gpt-5.1", want: schemas.ReasoningEffortNone},
}

func TestToOpenAIChatRequest_NoneReasoningEffortClamps(t *testing.T) {
	for _, tt := range noneEffortCases {
		t.Run(tt.name, func(t *testing.T) {
			req := ToOpenAIChatRequest(schemas.NewBifrostContext(nil, schemas.NoDeadline), &schemas.BifrostChatRequest{
				Provider: tt.provider,
				Model:    tt.model,
				Input: []schemas.ChatMessage{{
					Role:    schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")},
				}},
				Params: &schemas.ChatParameters{
					Reasoning: &schemas.ChatReasoning{Effort: schemas.Ptr(schemas.ReasoningEffortNone)},
				},
			})

			if req == nil || req.Reasoning == nil || req.Reasoning.Effort == nil {
				t.Fatalf("expected a reasoning effort on the converted request, got %#v", req)
			}
			if got := *req.Reasoning.Effort; got != tt.want {
				t.Fatalf("expected reasoning effort %q, got %q", tt.want, got)
			}
		})
	}
}

func TestToOpenAIResponsesRequest_NoneReasoningEffortClamps(t *testing.T) {
	for _, tt := range noneEffortCases {
		t.Run(tt.name, func(t *testing.T) {
			req := ToOpenAIResponsesRequest(nil, &schemas.BifrostResponsesRequest{
				Provider: tt.provider,
				Model:    tt.model,
				Input: []schemas.ResponsesMessage{{
					Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
					Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hello")},
				}},
				Params: &schemas.ResponsesParameters{
					Reasoning: &schemas.ResponsesParametersReasoning{Effort: schemas.Ptr(schemas.ReasoningEffortNone)},
				},
			})

			if req == nil || req.Reasoning == nil || req.Reasoning.Effort == nil {
				t.Fatalf("expected a reasoning effort on the converted request, got %#v", req)
			}
			if got := *req.Reasoning.Effort; got != tt.want {
				t.Fatalf("expected reasoning effort %q, got %q", tt.want, got)
			}
		})
	}
}
