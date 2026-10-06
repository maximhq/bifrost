package logging

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// providerCostResponse mimics a provider that reports usage.cost in its own units (Cortecs credits).
func providerCostResponse() *schemas.BifrostResponse {
	return &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			Usage: &schemas.BifrostLLMUsage{
				PromptTokens:     100,
				CompletionTokens: 50,
				TotalTokens:      150,
				Cost:             &schemas.BifrostCost{TotalCost: 1067},
			},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.ChatCompletionRequest,
				RoutingInfo: schemas.RoutingInfo{Provider: schemas.OpenAI, Model: "gpt-4o"},
			},
		},
	}
}

func TestAttachCostBreakdownKeepsProviderCostByDefault(t *testing.T) {
	plugin := newCostFidelityPlugin(t)
	result := providerCostResponse()
	entry := &logstore.Log{Provider: string(schemas.OpenAI), Model: "gpt-4o"}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	plugin.applyNonStreamingOutputToEntry(entry, result, false, false)
	plugin.attachCostBreakdown(ctx, entry, result)

	require.NotNil(t, entry.TokenUsageParsed.Cost)
	assert.InDelta(t, 1067, entry.TokenUsageParsed.Cost.TotalCost, 1e-12)
}

func TestAttachCostBreakdownIgnoresProviderCostWhenToggled(t *testing.T) {
	plugin := newCostFidelityPlugin(t)
	plugin.pricingManager.SetIgnoreProviderCost(schemas.OpenAI, true)
	result := providerCostResponse()
	entry := &logstore.Log{Provider: string(schemas.OpenAI), Model: "gpt-4o"}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	plugin.applyNonStreamingOutputToEntry(entry, result, false, false)
	plugin.attachCostBreakdown(ctx, entry, result)

	// gpt-4o testdata rates: input 2.5e-6/token, output 1e-5/token.
	require.NotNil(t, entry.TokenUsageParsed.Cost)
	assert.InDelta(t, 100*2.5e-6, entry.TokenUsageParsed.Cost.InputCost, 1e-12)
	assert.InDelta(t, 50*1e-5, entry.TokenUsageParsed.Cost.OutputCost, 1e-12)
	assert.InDelta(t, 100*2.5e-6+50*1e-5, entry.TokenUsageParsed.Cost.TotalCost, 1e-12)
	// The client-facing usage keeps the provider's figure.
	assert.InDelta(t, 1067, result.ChatResponse.Usage.Cost.TotalCost, 1e-12)
}

func TestCalculateCostForLogIgnoresStoredProviderCostWhenToggled(t *testing.T) {
	plugin := newCostFidelityPlugin(t)
	entry := &logstore.Log{
		ID:               "req-provider-cost",
		Timestamp:        time.Now().UTC(),
		Object:           string(schemas.ChatCompletionRequest),
		Provider:         string(schemas.OpenAI),
		Model:            "gpt-4o",
		Status:           "success",
		PromptTokens:     100,
		CompletionTokens: 50,
		TotalTokens:      150,
		TokenUsageParsed: &schemas.BifrostLLMUsage{
			PromptTokens:     100,
			CompletionTokens: 50,
			TotalTokens:      150,
			Cost:             &schemas.BifrostCost{TotalCost: 1067},
		},
	}

	got, err := plugin.calculateCostForLog(entry)
	require.NoError(t, err)
	assertCostsEqual(t, "provider cost trusted by default", got, 1067)

	plugin.pricingManager.SetIgnoreProviderCost(schemas.OpenAI, true)
	got, err = plugin.calculateCostForLog(entry)
	require.NoError(t, err)
	assertCostsEqual(t, "recalc ignores provider cost", got, 100*2.5e-6+50*1e-5)
}

// A stream error with a response chunk prices BilledUsage first; the later
// attachCostBreakdown must not discard that catalog-priced breakdown.
func TestErrorBillingBreakdownSurvivesAttachWhenToggled(t *testing.T) {
	plugin := newCostFidelityPlugin(t)
	plugin.pricingManager.SetIgnoreProviderCost(schemas.OpenAI, true)
	entry := &logstore.Log{Provider: string(schemas.OpenAI), Model: "gpt-4o"}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	billed := &schemas.BifrostLLMUsage{
		PromptTokens:     100,
		CompletionTokens: 50,
		TotalTokens:      150,
		Cost:             &schemas.BifrostCost{TotalCost: 1067},
	}
	plugin.applyErrorBillingFromBilledUsage(ctx, entry, billed, schemas.ChatCompletionStreamRequest)

	want := 100*2.5e-6 + 50*1e-5
	require.NotNil(t, entry.Cost)
	assert.InDelta(t, want, *entry.Cost, 1e-12)
	require.NotNil(t, entry.TokenUsageParsed.Cost)
	assert.InDelta(t, want, entry.TokenUsageParsed.Cost.TotalCost, 1e-12)
	assert.InDelta(t, 1067, billed.Cost.TotalCost, 1e-12, "caller's billed usage is untouched")

	// The response chunk carries no usage, so it prices to nothing.
	chunk := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.ChatCompletionStreamRequest,
				RoutingInfo: schemas.RoutingInfo{Provider: schemas.OpenAI, Model: "gpt-4o"},
			},
		},
	}
	plugin.attachCostBreakdown(ctx, entry, chunk)

	require.NotNil(t, entry.TokenUsageParsed.Cost)
	assert.InDelta(t, want, entry.TokenUsageParsed.Cost.TotalCost, 1e-12)
}
