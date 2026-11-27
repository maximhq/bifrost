package schemas

import (
	"sync"
	"testing"
)

// Core repopulates the extra fields after RunPostLLMHooks while plugins (governance billing,
// logging, telemetry) still read the response from goroutines their post-hooks started. With
// the values unchanged, that second pass must not write, or -race reports it.
func TestPopulateExtraFieldsLeavesUnchangedFieldsUnwritten(t *testing.T) {
	resp := &BifrostResponse{ChatResponse: &BifrostChatResponse{}}
	resp.PopulateExtraFields(ChatCompletionRequest, OpenAI, "gpt-6", "gpt-6")

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 100 {
			ef := resp.ChatResponse.ExtraFields
			_ = ef.RequestType
			_ = ef.Provider
			_ = ef.OriginalModelRequested
			_ = ef.ResolvedModelUsed
		}
	}()
	for range 100 {
		resp.PopulateExtraFields(ChatCompletionRequest, OpenAI, "gpt-6", "gpt-6")
	}
	wg.Wait()
}

func TestPopulateExtraFieldsOverwritesChangedFields(t *testing.T) {
	resp := &BifrostResponse{ChatResponse: &BifrostChatResponse{}}
	resp.ChatResponse.ExtraFields.RequestType = TextCompletionRequest
	resp.ChatResponse.ExtraFields.Provider = Anthropic
	resp.ChatResponse.ExtraFields.OriginalModelRequested = "stale"
	resp.ChatResponse.ExtraFields.ResolvedModelUsed = "stale-resolved"

	resp.PopulateExtraFields(ChatCompletionRequest, OpenAI, "gpt-6", "")

	ef := resp.ChatResponse.ExtraFields
	if ef.RequestType != ChatCompletionRequest || ef.Provider != OpenAI || ef.OriginalModelRequested != "gpt-6" || ef.ResolvedModelUsed != "gpt-6" {
		t.Fatalf("changed fields not written: %+v", ef)
	}
}

// BenchmarkPopulateExtraFieldsUnchanged measures the post-hook repopulation, where the values
// already match the pre-hook pass.
func BenchmarkPopulateExtraFieldsUnchanged(b *testing.B) {
	resp := &BifrostResponse{ChatResponse: &BifrostChatResponse{}}
	resp.PopulateExtraFields(ChatCompletionRequest, OpenAI, "gpt-6", "gpt-6")
	b.ReportAllocs()
	for b.Loop() {
		resp.PopulateExtraFields(ChatCompletionRequest, OpenAI, "gpt-6", "gpt-6")
	}
}
