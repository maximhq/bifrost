package utils

import (
	"context"
	"strings"
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

func chatDeltaChunk(delta schemas.ChatStreamResponseChoiceDelta) *schemas.BifrostResponse {
	return &schemas.BifrostResponse{ChatResponse: &schemas.BifrostChatResponse{
		Choices: []schemas.BifrostResponseChoice{{
			ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{Delta: &delta},
		}},
	}}
}

func contentDeltaChunk(text string) *schemas.BifrostResponse {
	return chatDeltaChunk(schemas.ChatStreamResponseChoiceDelta{Content: schemas.Ptr(text)})
}

// newTalliedContext registers a usage handle and tally the way a streaming
// provider does, then feeds the chunks through the tally.
func newTalliedContext(usage *schemas.BifrostLLMUsage, chunks ...*schemas.BifrostResponse) *schemas.BifrostContext {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	RegisterStreamUsageHandle(ctx, usage)
	tally := ctx.Value(schemas.BifrostContextKeyStreamOutputTally).(*streamOutputTally)
	for _, chunk := range chunks {
		tally.observe(chunk)
	}
	return ctx
}

// cancelBilledUsage runs the same two steps HandleStreamCancellation does.
func cancelBilledUsage(ctx *schemas.BifrostContext, jsonBody []byte) *schemas.BifrostLLMUsage {
	bifrostErr := &schemas.BifrostError{}
	attachBilledUsageFromContext(ctx, bifrostErr)
	estimateUnreportedStreamUsage(ctx, bifrostErr, jsonBody)
	return bifrostErr.ExtraFields.BilledUsage
}

var testPromptBody = []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"Tell me a 400 token story."}],"stream":true}`)

// OpenAI-compatible upstreams report usage only in the trailing usage frame, so
// a cancel before it used to bill nothing (maximhq/bifrost#9219).
func TestEstimateUnreportedStreamUsage_UsageOnlyAtEnd(t *testing.T) {
	role := chatDeltaChunk(schemas.ChatStreamResponseChoiceDelta{Role: schemas.Ptr("assistant")})
	ctx := newTalliedContext(&schemas.BifrostLLMUsage{}, role, contentDeltaChunk("w0 "), contentDeltaChunk("w1 "), contentDeltaChunk("w2 "))

	billed := cancelBilledUsage(ctx, testPromptBody)
	if billed == nil {
		t.Fatal("BilledUsage is nil: the cancelled stream bills nothing")
	}
	if want := estimatePromptTokens(testPromptBody); billed.PromptTokens != want {
		t.Errorf("PromptTokens = %d, want the request estimate %d", billed.PromptTokens, want)
	}
	if billed.CompletionTokens != 3 {
		t.Errorf("CompletionTokens = %d, want 3 (one per content chunk)", billed.CompletionTokens)
	}
	if billed.TotalTokens != billed.PromptTokens+billed.CompletionTokens {
		t.Errorf("TotalTokens = %d, want %d", billed.TotalTokens, billed.PromptTokens+billed.CompletionTokens)
	}
	if !billed.Estimated {
		t.Error("estimated usage must be marked Estimated")
	}
}

// Anthropic reports the prompt exactly in message_start but only a placeholder
// output count; the prompt is kept and the output raised to what was streamed.
func TestEstimateUnreportedStreamUsage_KeepsReportedPromptRaisesPlaceholderOutput(t *testing.T) {
	usage := &schemas.BifrostLLMUsage{PromptTokens: 30, CompletionTokens: 1, TotalTokens: 31}
	ctx := newTalliedContext(usage, contentDeltaChunk("hello world "), contentDeltaChunk("hello world "), contentDeltaChunk("hello world "))

	billed := cancelBilledUsage(ctx, testPromptBody)
	if billed.PromptTokens != 30 {
		t.Errorf("PromptTokens = %d, want the reported 30", billed.PromptTokens)
	}
	// 3 chunks carrying 36 ASCII bytes: ceil(36/4) = 9 tokens.
	if billed.CompletionTokens != 9 || billed.TotalTokens != 39 {
		t.Errorf("CompletionTokens/TotalTokens = %d/%d, want 9/39", billed.CompletionTokens, billed.TotalTokens)
	}
	if !billed.Estimated {
		t.Error("estimated usage must be marked Estimated")
	}
	if usage.CompletionTokens != 1 {
		t.Errorf("the provider's usage handle was mutated: CompletionTokens = %d", usage.CompletionTokens)
	}
}

// A provider count that keeps pace with the stream (vLLM continuous_usage_stats)
// is exact and must not be replaced by the estimate.
func TestEstimateUnreportedStreamUsage_KeepsContinuousUsage(t *testing.T) {
	usage := &schemas.BifrostLLMUsage{PromptTokens: 25, CompletionTokens: 3, TotalTokens: 28}
	ctx := newTalliedContext(usage, contentDeltaChunk("a much longer chunk"), contentDeltaChunk("a much longer chunk"), contentDeltaChunk("a much longer chunk"))

	billed := cancelBilledUsage(ctx, testPromptBody)
	if billed.PromptTokens != 25 || billed.CompletionTokens != 3 || billed.TotalTokens != 28 {
		t.Errorf("reported usage changed: %+v", billed)
	}
	if billed.Estimated {
		t.Error("provider-reported usage must not be marked Estimated")
	}
}

// Before the upstream's first chunk nothing shows the prompt was processed, so
// nothing is billed, as before.
func TestEstimateUnreportedStreamUsage_NoChunkBillsNothing(t *testing.T) {
	ctx := newTalliedContext(&schemas.BifrostLLMUsage{})
	if billed := cancelBilledUsage(ctx, testPromptBody); billed != nil {
		t.Errorf("BilledUsage = %+v, want nil before the upstream answered", billed)
	}
}

// After a role-only chunk the prompt was processed but no output streamed yet.
func TestEstimateUnreportedStreamUsage_RoleOnlyBillsPrompt(t *testing.T) {
	ctx := newTalliedContext(&schemas.BifrostLLMUsage{}, chatDeltaChunk(schemas.ChatStreamResponseChoiceDelta{Role: schemas.Ptr("assistant")}))
	billed := cancelBilledUsage(ctx, testPromptBody)
	if billed == nil || billed.PromptTokens == 0 || billed.CompletionTokens != 0 || !billed.Estimated {
		t.Errorf("BilledUsage = %+v, want an estimated prompt and no output", billed)
	}
}

// A provider that never registered a handle keeps the old behaviour.
func TestEstimateUnreportedStreamUsage_UnregisteredProviderUnchanged(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	if billed := cancelBilledUsage(ctx, testPromptBody); billed != nil {
		t.Errorf("BilledUsage = %+v, want nil without a registered handle", billed)
	}
}

func TestStreamOutputTally_CountsEveryOutputKind(t *testing.T) {
	ctx := newTalliedContext(&schemas.BifrostLLMUsage{},
		chatDeltaChunk(schemas.ChatStreamResponseChoiceDelta{Reasoning: schemas.Ptr("abcd")}),
		chatDeltaChunk(schemas.ChatStreamResponseChoiceDelta{Refusal: schemas.Ptr("abcd")}),
		chatDeltaChunk(schemas.ChatStreamResponseChoiceDelta{ToolCalls: []schemas.ChatAssistantMessageToolCall{{
			Function: schemas.ChatAssistantMessageToolCallFunction{Name: schemas.Ptr("abcd"), Arguments: "abcd"},
		}}}),
		&schemas.BifrostResponse{TextCompletionResponse: &schemas.BifrostTextCompletionResponse{
			Choices: []schemas.BifrostResponseChoice{{TextCompletionResponseChoice: &schemas.TextCompletionResponseChoice{Text: schemas.Ptr("abcd")}}},
		}},
		&schemas.BifrostResponse{ResponsesStreamResponse: &schemas.BifrostResponsesStreamResponse{Delta: schemas.Ptr("abcd")}},
		// Not output: an empty delta and a lifecycle event.
		contentDeltaChunk(""),
		&schemas.BifrostResponse{ResponsesStreamResponse: &schemas.BifrostResponsesStreamResponse{Type: schemas.ResponsesStreamResponseTypeCreated}},
	)
	tally := ctx.Value(schemas.BifrostContextKeyStreamOutputTally).(*streamOutputTally)
	if got := tally.chunks.Load(); got != 7 {
		t.Errorf("chunks = %d, want 7", got)
	}
	if got := tally.deltas.Load(); got != 5 {
		t.Errorf("deltas = %d, want 5 (reasoning, refusal, tool call, text, Responses delta)", got)
	}
	if got := tally.asciiBytes.Load(); got != 24 {
		t.Errorf("asciiBytes = %d, want 24", got)
	}
}

func TestEstimateTextTokens(t *testing.T) {
	var c textCount
	c.add("日本語 ok") // 3 CJK characters, 3 ASCII bytes
	if got := estimateTextTokens(c.ascii, c.nonASCII); got != 4 {
		t.Errorf("estimateTextTokens = %d, want 4 (1 for the ASCII bytes, 1 per CJK character)", got)
	}
}

// Inline media is left out of the prompt estimate: its cost depends on the
// model, not on its base64 size.
func TestEstimatePromptTokens_SkipsInlineBinary(t *testing.T) {
	text := `{"messages":[{"role":"user","content":[{"type":"text","text":"Describe this image."},{"type":"image_url","image_url":{"url":"data:image/png;base64,`
	withImage := []byte(text + strings.Repeat("iVBORw0KGgo=", 4000) + `"}}]}]}`)
	withoutImage := []byte(text + `"}}]}]}`)
	if got, want := estimatePromptTokens(withImage), estimatePromptTokens(withoutImage); got != want {
		t.Errorf("estimate with image = %d, want %d (the text alone)", got, want)
	}
	if estimatePromptTokens(nil) != 0 {
		t.Error("an empty body must estimate to 0")
	}
}

// BenchmarkStreamOutputTallyObserve is the per-chunk cost the fix adds to every
// tallied stream chunk in ProcessAndSendResponse.
func BenchmarkStreamOutputTallyObserve(b *testing.B) {
	tally := &streamOutputTally{}
	chunk := contentDeltaChunk(" token")
	b.ReportAllocs()
	for b.Loop() {
		tally.observe(chunk)
	}
}
