package logging

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCostBreakdownDoesNotMutateClientUsage verifies the 1A invariant: attaching
// the cost breakdown for logging never mutates the usage object shared with the
// client-facing response. applyNonStreamingOutputToEntry hands the log entry a
// deep copy, so the cost write lands on the copy, not result.ChatResponse.Usage.
func TestCostBreakdownDoesNotMutateClientUsage(t *testing.T) {
	plugin := newCostFidelityPlugin(t)

	const promptTokens, completionTokens = 100, 50
	clientUsage := &schemas.BifrostLLMUsage{
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		TotalTokens:      promptTokens + completionTokens,
	}
	result := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			Usage: clientUsage,
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.ChatCompletionRequest,
				RoutingInfo: schemas.RoutingInfo{Provider: schemas.OpenAI, Model: "gpt-4o"},
			},
		},
	}
	entry := &logstore.Log{Provider: string(schemas.OpenAI), Model: "gpt-4o"}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	plugin.applyNonStreamingOutputToEntry(entry, result, false, false)
	plugin.attachCostBreakdown(ctx, entry, result)

	// The log entry owns a distinct usage copy that carries the computed cost.
	require.NotNil(t, entry.TokenUsageParsed)
	assert.NotSame(t, clientUsage, entry.TokenUsageParsed)
	require.NotNil(t, entry.TokenUsageParsed.Cost)
	assert.Positive(t, entry.TokenUsageParsed.Cost.TotalCost)

	// The client-facing usage is untouched: no cost leaked onto it.
	assert.Nil(t, clientUsage.Cost)
}

// TestAttachCostBreakdownDenormalizesSplitWithoutUsageCarrier covers the OCR-shaped
// case: no usage aliased into TokenUsageParsed. attachCostBreakdown must denormalize
// the per-category split directly onto the entry columns so they reconcile to the
// cost column (SerializeFields skips its cost block when TokenUsageParsed is nil).
func TestAttachCostBreakdownDenormalizesSplitWithoutUsageCarrier(t *testing.T) {
	plugin := newCostFidelityPlugin(t)

	const promptTokens, completionTokens = 100, 50
	result := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			Usage: &schemas.BifrostLLMUsage{
				PromptTokens:     promptTokens,
				CompletionTokens: completionTokens,
				TotalTokens:      promptTokens + completionTokens,
			},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.ChatCompletionRequest,
				RoutingInfo: schemas.RoutingInfo{Provider: schemas.OpenAI, Model: "gpt-4o"},
			},
		},
	}
	// No usage carrier on the log entry (the OCR shape).
	entry := &logstore.Log{Provider: string(schemas.OpenAI), Model: "gpt-4o"}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	plugin.attachCostBreakdown(ctx, entry, result)

	// gpt-4o testdata rates: input 2.5e-6/token, output 1e-5/token.
	wantIn := float64(promptTokens) * 2.5e-6
	wantOut := float64(completionTokens) * 1e-5
	assert.InDelta(t, wantIn, entry.InputCost, 1e-12)
	assert.InDelta(t, wantOut, entry.OutputCost, 1e-12)
	assert.Zero(t, entry.AdditionalCost)
	// The real split flowed through (both sides populated), not a lumped total.
	assert.Positive(t, entry.InputCost)
	assert.Positive(t, entry.OutputCost)
}
