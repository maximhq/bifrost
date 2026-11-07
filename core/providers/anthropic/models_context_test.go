package anthropic

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestToAnthropicListModelsResponseContextLengthFallback(t *testing.T) {
	ctxLen, maxIn := 131072, 200000
	resp := ToAnthropicListModelsResponse(&schemas.BifrostListModelsResponse{
		Data: []schemas.Model{
			{ID: "anthropic/only-context", ContextLength: &ctxLen},
			{ID: "anthropic/both", ContextLength: &ctxLen, MaxInputTokens: &maxIn},
			{ID: "anthropic/neither"},
		},
	})
	if got := resp.Data[0].MaxInputTokens; got == nil || *got != ctxLen {
		t.Fatalf("context_length only: want %d, got %v", ctxLen, got)
	}
	if got := resp.Data[1].MaxInputTokens; got == nil || *got != maxIn {
		t.Fatalf("max_input_tokens must win over context_length: want %d, got %v", maxIn, got)
	}
	if resp.Data[2].MaxInputTokens != nil {
		t.Fatalf("no figure must stay unset, got %v", *resp.Data[2].MaxInputTokens)
	}
}
