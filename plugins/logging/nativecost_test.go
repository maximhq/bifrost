package logging

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAttachCostToNativeUsage covers the speech / transcription / OCR modalities
// whose usage object is not aliased into TokenUsageParsed, so the breakdown has
// to be written onto the native response explicitly.
func TestAttachCostToNativeUsage(t *testing.T) {
	bd := &schemas.BifrostCost{InputCost: 0.002, OutputCost: 0.001, TotalCost: 0.003}

	t.Run("speech", func(t *testing.T) {
		r := &schemas.BifrostResponse{SpeechResponse: &schemas.BifrostSpeechResponse{Usage: &schemas.SpeechUsage{}}}
		attachCostToNativeUsage(r, bd)
		require.NotNil(t, r.SpeechResponse.Usage.Cost)
		assert.InDelta(t, 0.003, r.SpeechResponse.Usage.Cost.TotalCost, 1e-12)
	})

	t.Run("transcription", func(t *testing.T) {
		r := &schemas.BifrostResponse{TranscriptionResponse: &schemas.BifrostTranscriptionResponse{Usage: &schemas.TranscriptionUsage{}}}
		attachCostToNativeUsage(r, bd)
		require.NotNil(t, r.TranscriptionResponse.Usage.Cost)
		assert.InDelta(t, 0.003, r.TranscriptionResponse.Usage.Cost.TotalCost, 1e-12)
	})

	t.Run("ocr", func(t *testing.T) {
		r := &schemas.BifrostResponse{OCRResponse: &schemas.BifrostOCRResponse{UsageInfo: &schemas.OCRUsageInfo{}}}
		attachCostToNativeUsage(r, bd)
		require.NotNil(t, r.OCRResponse.UsageInfo.Cost)
		assert.InDelta(t, 0.003, r.OCRResponse.UsageInfo.Cost.TotalCost, 1e-12)
	})

	t.Run("preserves provider-supplied cost", func(t *testing.T) {
		existing := &schemas.BifrostCost{TotalCost: 9.99}
		r := &schemas.BifrostResponse{SpeechResponse: &schemas.BifrostSpeechResponse{Usage: &schemas.SpeechUsage{Cost: existing}}}
		attachCostToNativeUsage(r, bd)
		assert.Same(t, existing, r.SpeechResponse.Usage.Cost)
	})

	t.Run("no usage slot is a no-op", func(t *testing.T) {
		r := &schemas.BifrostResponse{SpeechResponse: &schemas.BifrostSpeechResponse{}}
		attachCostToNativeUsage(r, bd)
		assert.Nil(t, r.SpeechResponse.Usage)
	})
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
