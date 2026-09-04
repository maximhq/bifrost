package copilot

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

func copilotError(status int, code, message string) *schemas.BifrostError {
	return &schemas.BifrostError{
		StatusCode: schemas.Ptr(status),
		Error:      &schemas.ErrorField{Code: schemas.Ptr(code), Message: message},
	}
}

// Copilot answers 400/invalid_reasoning_effort for two different situations, and
// only one of them may drop reasoning: a model with no reasoning surface at all.
// An effort merely outside the model's published set names its supported values,
// and is clamped in the request converter instead.
func TestReasoningRejectedByModel(t *testing.T) {
	tests := []struct {
		name string
		err  *schemas.BifrostError
		want bool
	}{
		{
			name: "model has no reasoning surface",
			err:  copilotError(fasthttp.StatusBadRequest, "invalid_reasoning_effort", `reasoning_effort "medium" was provided, but model claude-haiku-4.5 does not support reasoning effort`),
			want: true,
		},
		{
			name: "effort outside published set is clamped, not stripped",
			err:  copilotError(fasthttp.StatusBadRequest, "invalid_reasoning_effort", `reasoning_effort "none" is not supported by model claude-sonnet-5; supported values: [low medium high xhigh max]`),
			want: false,
		},
		{
			name: "unrelated 400",
			err:  copilotError(fasthttp.StatusBadRequest, "invalid_request_error", "missing required field"),
			want: false,
		},
		{
			name: "non-400 with matching text",
			err:  copilotError(fasthttp.StatusInternalServerError, "invalid_reasoning_effort", "does not support reasoning effort"),
			want: false,
		},
		{name: "nil error", err: nil, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := reasoningRejectedByModel(tt.err); got != tt.want {
				t.Fatalf("reasoningRejectedByModel = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWithoutReasoningLeavesCallerRequestIntact(t *testing.T) {
	chat := &schemas.BifrostChatRequest{
		Provider: schemas.Copilot,
		Model:    "claude-haiku-4.5",
		Params: &schemas.ChatParameters{
			Reasoning: &schemas.ChatReasoning{Effort: schemas.Ptr(schemas.ReasoningEffortMedium)},
		},
	}
	stripped := withoutChatReasoning(chat)
	if stripped == nil || stripped.Params.Reasoning != nil {
		t.Fatalf("expected reasoning to be dropped, got %#v", stripped)
	}
	if chat.Params.Reasoning == nil {
		t.Fatal("caller's chat request was mutated")
	}
	if withoutChatReasoning(&schemas.BifrostChatRequest{}) != nil {
		t.Fatal("expected nil when there is no reasoning to drop")
	}

	responses := &schemas.BifrostResponsesRequest{
		Provider: schemas.Copilot,
		Model:    "claude-haiku-4.5",
		Params: &schemas.ResponsesParameters{
			Reasoning: &schemas.ResponsesParametersReasoning{Effort: schemas.Ptr(schemas.ReasoningEffortMedium)},
		},
	}
	strippedResp := withoutResponsesReasoning(responses)
	if strippedResp == nil || strippedResp.Params.Reasoning != nil {
		t.Fatalf("expected reasoning to be dropped, got %#v", strippedResp)
	}
	if responses.Params.Reasoning == nil {
		t.Fatal("caller's responses request was mutated")
	}
	if withoutResponsesReasoning(&schemas.BifrostResponsesRequest{}) != nil {
		t.Fatal("expected nil when there is no reasoning to drop")
	}
}

func TestReasoningUnsupportedCache(t *testing.T) {
	provider := &CopilotProvider{}
	if provider.isReasoningUnsupported("claude-haiku-4.5") {
		t.Fatal("model should not start cached")
	}
	provider.markReasoningUnsupported("claude-haiku-4.5", copilotError(fasthttp.StatusBadRequest, "invalid_reasoning_effort", "does not support reasoning effort"))
	if !provider.isReasoningUnsupported("claude-haiku-4.5") {
		t.Fatal("model should be cached after being marked")
	}
	if provider.isReasoningUnsupported("claude-sonnet-5") {
		t.Fatal("marking one model must not affect another")
	}
}
