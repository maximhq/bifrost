package schemas

import (
	"context"
	"testing"
)

func TestRoutingDebugContextReturnsOwnedInitialAttemptSnapshot(t *testing.T) {
	ctx := NewBifrostContext(context.Background(), NoDeadline)
	provider, model, tokens := "openai", "text-embedding-3-small", 17
	requireSet := SetRoutingDebugOnContext(ctx, &BifrostRoutingDebug{
		ProviderUsed:       &provider,
		ModelUsed:          &model,
		InputTokens:        &tokens,
		CountTowardBudgets: true,
	})
	if !requireSet {
		t.Fatal("SetRoutingDebugOnContext() = false")
	}

	first, ok := InitialAttemptRoutingDebugFromContext(ctx)
	if !ok {
		t.Fatal("InitialAttemptRoutingDebugFromContext() = false")
	}
	*first.InputTokens = 99
	second, ok := InitialAttemptRoutingDebugFromContext(ctx)
	if !ok || *second.InputTokens != 17 {
		t.Fatalf("owned snapshot input tokens = %v, want 17", second)
	}
}

func TestInitialAttemptRoutingDebugRejectsRetriesAndFallbacks(t *testing.T) {
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
			SetRoutingDebugOnContext(ctx, &BifrostRoutingDebug{
				ProviderUsed: &provider,
				ModelUsed:    &model,
				InputTokens:  &tokens,
			})
			if _, ok := InitialAttemptRoutingDebugFromContext(ctx); ok {
				t.Fatal("InitialAttemptRoutingDebugFromContext() = true")
			}
		})
	}
}

func TestSetRoutingDebugOnContextRejectsMalformedUsage(t *testing.T) {
	ctx := NewBifrostContext(context.Background(), NoDeadline)
	provider, model, negativeTokens := "openai", "text-embedding-3-small", -1
	if SetRoutingDebugOnContext(ctx, &BifrostRoutingDebug{
		ProviderUsed: &provider,
		ModelUsed:    &model,
		InputTokens:  &negativeTokens,
	}) {
		t.Fatal("SetRoutingDebugOnContext() accepted negative input tokens")
	}
}
