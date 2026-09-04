package schemas

import (
	"context"
	"testing"
)

func TestRoutingMetadataContextReturnsOwnedInitialAttemptSnapshot(t *testing.T) {
	ctx := NewBifrostContext(context.Background(), NoDeadline)
	provider, model, tokens, outputTokens := "openai", "gpt-4o-mini", 17, 3
	requireAppend := AppendRoutingCallOnContext(ctx, BifrostRoutingCall{
		ProviderUsed:       &provider,
		ModelUsed:          &model,
		InputTokens:        &tokens,
		OutputTokens:       &outputTokens,
		CountTowardBudgets: true,
	})
	if !requireAppend {
		t.Fatal("AppendRoutingCallOnContext() = false")
	}

	first, ok := InitialAttemptRoutingMetadataFromContext(ctx)
	if !ok || len(first.Calls) != 1 {
		t.Fatalf("InitialAttemptRoutingMetadataFromContext() = %v, %v", first, ok)
	}
	*first.Calls[0].InputTokens = 99
	*first.Calls[0].OutputTokens = 99
	second, ok := InitialAttemptRoutingMetadataFromContext(ctx)
	if !ok || len(second.Calls) != 1 || *second.Calls[0].InputTokens != 17 || *second.Calls[0].OutputTokens != 3 {
		t.Fatalf("owned snapshot = %v, want input=17 output=3", second)
	}
}

// TestRoutingMetadataAppendsBothSemanticAndLLMCalls pins the fix for the bug
// where a request that classifies via semantic and then falls back to the llm
// classifier lost the embedding's usage: a second call must add to the first
// rather than replacing it.
func TestRoutingMetadataAppendsBothSemanticAndLLMCalls(t *testing.T) {
	ctx := NewBifrostContext(context.Background(), NoDeadline)
	embedProvider, embedModel, embedTokens := "openai", "text-embedding-3-small", 12
	if !AppendRoutingCallOnContext(ctx, BifrostRoutingCall{
		ProviderUsed: &embedProvider,
		ModelUsed:    &embedModel,
		InputTokens:  &embedTokens,
	}) {
		t.Fatal("AppendRoutingCallOnContext(embed) = false")
	}

	llmProvider, llmModel, llmInput, llmOutput := "anthropic", "claude-haiku-4-5", 40, 8
	if !AppendRoutingCallOnContext(ctx, BifrostRoutingCall{
		ProviderUsed: &llmProvider,
		ModelUsed:    &llmModel,
		InputTokens:  &llmInput,
		OutputTokens: &llmOutput,
	}) {
		t.Fatal("AppendRoutingCallOnContext(llm) = false")
	}

	metadata, ok := RoutingMetadataFromContext(ctx)
	if !ok || len(metadata.Calls) != 2 {
		t.Fatalf("RoutingMetadataFromContext() = %v, %v; want 2 calls", metadata, ok)
	}
	if *metadata.Calls[0].ProviderUsed != embedProvider || metadata.Calls[0].OutputTokens != nil {
		t.Fatalf("Calls[0] = %+v, want the embed call with no OutputTokens", metadata.Calls[0])
	}
	if *metadata.Calls[1].ProviderUsed != llmProvider || *metadata.Calls[1].OutputTokens != llmOutput {
		t.Fatalf("Calls[1] = %+v, want the llm call with OutputTokens=%d", metadata.Calls[1], llmOutput)
	}
}

func TestInitialAttemptRoutingMetadataRejectsRetriesAndFallbacks(t *testing.T) {
	provider, model, tokens := "openai", "text-embedding-3-small", 17
	for _, test := range []struct {
		name          string
		retryNumber   int
		fallbackIndex int
	}{
		{name: "retry", retryNumber: 1},
		{name: "fallback", fallbackIndex: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := NewBifrostContext(context.Background(), NoDeadline)
			ctx.SetValue(BifrostContextKeyNumberOfRetries, test.retryNumber)
			ctx.SetValue(BifrostContextKeyFallbackIndex, test.fallbackIndex)
			AppendRoutingCallOnContext(ctx, BifrostRoutingCall{
				ProviderUsed: &provider,
				ModelUsed:    &model,
				InputTokens:  &tokens,
			})
			if _, ok := InitialAttemptRoutingMetadataFromContext(ctx); ok {
				t.Fatal("InitialAttemptRoutingMetadataFromContext() = true")
			}
		})
	}
}

func TestRoutingMetadataInitialAttemptAPI(t *testing.T) {
	ctx := NewBifrostContext(context.Background(), NoDeadline)
	provider, model, tokens := "openai", "text-embedding-3-small", 1
	requireAppend := AppendRoutingCallOnContext(ctx, BifrostRoutingCall{
		ProviderUsed: &provider,
		ModelUsed:    &model,
		InputTokens:  &tokens,
	})
	if !requireAppend {
		t.Fatal("AppendRoutingCallOnContext() = false")
	}

	metadata, ok := InitialAttemptRoutingMetadataFromContext(ctx)
	if !ok || len(metadata.Calls) != 1 {
		t.Fatalf("InitialAttemptRoutingMetadataFromContext() = %v, %v", metadata, ok)
	}
}

func TestAppendRoutingCallOnContextRejectsMalformedUsage(t *testing.T) {
	provider, model := "openai", "gpt-4o-mini"
	negativeOutput := -1
	for _, test := range []struct {
		name         string
		inputTokens  int
		outputTokens *int
	}{
		{name: "negative input", inputTokens: -1},
		{name: "negative output", outputTokens: &negativeOutput},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := NewBifrostContext(context.Background(), NoDeadline)
			if AppendRoutingCallOnContext(ctx, BifrostRoutingCall{
				ProviderUsed: &provider,
				ModelUsed:    &model,
				InputTokens:  &test.inputTokens,
				OutputTokens: test.outputTokens,
			}) {
				t.Fatal("AppendRoutingCallOnContext() accepted malformed usage")
			}
		})
	}
}
