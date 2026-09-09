package bedrock_test

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/providers/anthropic"
	"github.com/maximhq/bifrost/core/providers/bedrock"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// driveBedrockReasoningStream replays the Converse events AWS sends for a thinking
// block that streams in two deltas plus a signature, followed by the answer text.
func driveBedrockReasoningStream(t *testing.T) []*schemas.BifrostResponsesStreamResponse {
	t.Helper()

	state := bedrock.NewBedrockResponsesStreamState()
	state.Model = schemas.Ptr("anthropic.claude-3-7-sonnet-20250219-v1:0")

	var all []*schemas.BifrostResponsesStreamResponse
	seq := 0

	emit := func(chunk *bedrock.BedrockStreamEvent) {
		responses, bErr, _ := chunk.ToBifrostResponsesStream(seq, state)
		require.Nil(t, bErr, "no event in a clean reasoning stream may produce an error")
		all = append(all, responses...)
		seq += len(responses)
	}

	emit(&bedrock.BedrockStreamEvent{Role: schemas.Ptr("assistant")})
	emit(&bedrock.BedrockStreamEvent{
		ContentBlockIndex: schemas.Ptr(0),
		Delta: &bedrock.BedrockContentBlockDelta{
			ReasoningContent: &bedrock.BedrockReasoningContentText{Text: schemas.Ptr("First I check ")},
		},
	})
	emit(&bedrock.BedrockStreamEvent{
		ContentBlockIndex: schemas.Ptr(0),
		Delta: &bedrock.BedrockContentBlockDelta{
			ReasoningContent: &bedrock.BedrockReasoningContentText{Text: schemas.Ptr("the docs.")},
		},
	})
	emit(&bedrock.BedrockStreamEvent{
		ContentBlockIndex: schemas.Ptr(0),
		Delta: &bedrock.BedrockContentBlockDelta{
			ReasoningContent: &bedrock.BedrockReasoningContentText{Signature: schemas.Ptr("sig-abc")},
		},
	})
	emit(&bedrock.BedrockStreamEvent{ContentBlockIndex: schemas.Ptr(0), ContentBlockStop: true})
	emit(&bedrock.BedrockStreamEvent{
		ContentBlockIndex: schemas.Ptr(1),
		Delta:             &bedrock.BedrockContentBlockDelta{Text: schemas.Ptr("Done.")},
	})
	emit(&bedrock.BedrockStreamEvent{ContentBlockIndex: schemas.Ptr(1), ContentBlockStop: true})
	emit(&bedrock.BedrockStreamEvent{StopReason: schemas.Ptr("end_turn")})

	usage := &schemas.ResponsesResponseUsage{InputTokens: 12, OutputTokens: 9, TotalTokens: 21}
	all = append(all, bedrock.FinalizeBedrockStream(state, seq, usage, nil)...)

	return all
}

// TestBedrockReasoningSummaryEvents pins the two fields the Responses streaming spec
// requires on the reasoning_summary_* events: summary_index (absent entirely), and the
// full text on reasoning_summary_text.done, which went out as a hardcoded empty string
// so the whole summary was dropped from the terminal events.
func TestBedrockReasoningSummaryEvents(t *testing.T) {
	all := driveBedrockReasoningStream(t)

	var streamed string
	var deltas int
	for _, r := range all {
		if r.Type != schemas.ResponsesStreamResponseTypeReasoningSummaryTextDelta {
			continue
		}
		deltas++
		require.NotNil(t, r.SummaryIndex, "reasoning_summary_text.delta must carry summary_index")
		assert.Equal(t, 0, *r.SummaryIndex)
		if r.Delta != nil {
			streamed += *r.Delta
		}
	}
	// Two text deltas plus the signature delta, which rides the same event type.
	require.Equal(t, 3, deltas)
	require.Equal(t, "First I check the docs.", streamed, "precondition: reasoning deltas must have streamed")

	var done []*schemas.BifrostResponsesStreamResponse
	for _, r := range all {
		if r.Type == schemas.ResponsesStreamResponseTypeReasoningSummaryTextDone {
			done = append(done, r)
		}
	}
	require.Len(t, done, 1, "the reasoning block closes exactly once")
	require.NotNil(t, done[0].SummaryIndex, "reasoning_summary_text.done must carry summary_index")
	assert.Equal(t, 0, *done[0].SummaryIndex)
	require.NotNil(t, done[0].Text)
	assert.Equal(t, "First I check the docs.", *done[0].Text,
		"reasoning_summary_text.done must carry the accumulated summary, not an empty string")

	// The content_part.done closing the same block carried the same empty string.
	for _, r := range all {
		if r.Type == schemas.ResponsesStreamResponseTypeContentPartDone &&
			r.Part != nil && r.Part.Type == schemas.ResponsesOutputMessageContentTypeReasoning {
			require.NotNil(t, r.Part.Text)
			assert.Equal(t, "First I check the docs.", *r.Part.Text)
		}
	}
}

// TestBedrockReasoningSummaryEventsAtFinalize covers the other close path: a stream whose
// only block is the reasoning block, so it is closed by FinalizeBedrockStream rather than
// by the next content block starting.
func TestBedrockReasoningSummaryEventsAtFinalize(t *testing.T) {
	state := bedrock.NewBedrockResponsesStreamState()
	state.Model = schemas.Ptr("anthropic.claude-3-7-sonnet-20250219-v1:0")

	var all []*schemas.BifrostResponsesStreamResponse
	seq := 0
	emit := func(chunk *bedrock.BedrockStreamEvent) {
		responses, bErr, _ := chunk.ToBifrostResponsesStream(seq, state)
		require.Nil(t, bErr)
		all = append(all, responses...)
		seq += len(responses)
	}

	emit(&bedrock.BedrockStreamEvent{Role: schemas.Ptr("assistant")})
	emit(&bedrock.BedrockStreamEvent{
		ContentBlockIndex: schemas.Ptr(0),
		Delta: &bedrock.BedrockContentBlockDelta{
			ReasoningContent: &bedrock.BedrockReasoningContentText{Text: schemas.Ptr("Thinking it through.")},
		},
	})
	emit(&bedrock.BedrockStreamEvent{ContentBlockIndex: schemas.Ptr(0), ContentBlockStop: true})
	emit(&bedrock.BedrockStreamEvent{StopReason: schemas.Ptr("end_turn")})

	usage := &schemas.ResponsesResponseUsage{InputTokens: 5, OutputTokens: 4, TotalTokens: 9}
	all = append(all, bedrock.FinalizeBedrockStream(state, seq, usage, nil)...)

	var done []*schemas.BifrostResponsesStreamResponse
	for _, r := range all {
		if r.Type == schemas.ResponsesStreamResponseTypeReasoningSummaryTextDone {
			done = append(done, r)
		}
	}
	require.Len(t, done, 1)
	require.NotNil(t, done[0].SummaryIndex)
	assert.Equal(t, 0, *done[0].SummaryIndex)
	require.NotNil(t, done[0].Text)
	assert.Equal(t, "Thinking it through.", *done[0].Text)
}

// bedrockReasoningItems returns the reasoning items closed by output_item.done, in order.
func bedrockReasoningItems(events []*schemas.BifrostResponsesStreamResponse) []*schemas.ResponsesMessage {
	var items []*schemas.ResponsesMessage
	for _, r := range events {
		if r.Type != schemas.ResponsesStreamResponseTypeOutputItemDone {
			continue
		}
		if r.Item != nil && r.Item.Type != nil && *r.Item.Type == schemas.ResponsesMessageTypeReasoning {
			items = append(items, r.Item)
		}
	}
	return items
}

// assertBedrockReasoningItemSnapshot pins the shape of a closed reasoning item: the
// accumulated summary text, and the replay token that signs it.
//
// The two are asserted together on purpose. Replay signs the first summary entry with
// encrypted_content, so a summary that arrives without its signature reaches Converse as an
// unsigned reasoning block, which Bedrock rejects.
func assertBedrockReasoningItemSnapshot(t *testing.T, item *schemas.ResponsesMessage, wantText, wantSignature string) {
	t.Helper()

	require.NotNil(t, item)
	require.NotNil(t, item.Status)
	assert.Equal(t, "completed", *item.Status)
	require.NotNil(t, item.ResponsesReasoning)

	require.Len(t, item.ResponsesReasoning.Summary, 1,
		"the reasoning item must carry the summary its reasoning_summary_* events described")
	assert.Equal(t, schemas.ResponsesReasoningContentBlockTypeSummaryText, item.ResponsesReasoning.Summary[0].Type)
	assert.Equal(t, wantText, item.ResponsesReasoning.Summary[0].Text)

	require.NotNil(t, item.ResponsesReasoning.EncryptedContent,
		"summary text must not reach replay unsigned")
	assert.Equal(t, wantSignature, *item.ResponsesReasoning.EncryptedContent)
}

// TestBedrockReasoningItemSummaryNextTextPath covers the close path taken when a text
// block starts after the reasoning block.
func TestBedrockReasoningItemSummaryNextTextPath(t *testing.T) {
	events := driveBedrockReasoningStream(t)

	items := bedrockReasoningItems(events)
	require.Len(t, items, 1)
	assertBedrockReasoningItemSnapshot(t, items[0], "First I check the docs.", "sig-abc")

	// Existing completion behavior is preserved: the terminal event still carries output.
	var terminal *schemas.BifrostResponsesStreamResponse
	for _, r := range events {
		if r.Type == schemas.ResponsesStreamResponseTypeCompleted {
			terminal = r
		}
	}
	require.NotNil(t, terminal)
	require.NotNil(t, terminal.Response)
	require.NotEmpty(t, terminal.Response.Output)

	var summarized []string
	for _, msg := range terminal.Response.Output {
		if msg.Type != nil && *msg.Type == schemas.ResponsesMessageTypeReasoning && msg.ResponsesReasoning != nil {
			for _, summary := range msg.ResponsesReasoning.Summary {
				summarized = append(summarized, summary.Text)
			}
		}
	}
	assert.Equal(t, []string{"First I check the docs."}, summarized,
		"the reasoning text must reach response.completed's output")
}

// TestBedrockReasoningItemSummaryNextToolPath covers the close path taken when a tool_use
// block starts after the reasoning block.
func TestBedrockReasoningItemSummaryNextToolPath(t *testing.T) {
	state := bedrock.NewBedrockResponsesStreamState()
	state.Model = schemas.Ptr("anthropic.claude-3-7-sonnet-20250219-v1:0")

	var all []*schemas.BifrostResponsesStreamResponse
	seq := 0
	emit := func(chunk *bedrock.BedrockStreamEvent) {
		responses, bErr, _ := chunk.ToBifrostResponsesStream(seq, state)
		require.Nil(t, bErr)
		all = append(all, responses...)
		seq += len(responses)
	}

	emit(&bedrock.BedrockStreamEvent{Role: schemas.Ptr("assistant")})
	emit(&bedrock.BedrockStreamEvent{
		ContentBlockIndex: schemas.Ptr(0),
		Delta: &bedrock.BedrockContentBlockDelta{
			ReasoningContent: &bedrock.BedrockReasoningContentText{Text: schemas.Ptr("Picking a tool.")},
		},
	})
	emit(&bedrock.BedrockStreamEvent{
		ContentBlockIndex: schemas.Ptr(0),
		Delta: &bedrock.BedrockContentBlockDelta{
			ReasoningContent: &bedrock.BedrockReasoningContentText{Signature: schemas.Ptr("sig-tool")},
		},
	})
	emit(&bedrock.BedrockStreamEvent{ContentBlockIndex: schemas.Ptr(0), ContentBlockStop: true})
	emit(&bedrock.BedrockStreamEvent{
		ContentBlockIndex: schemas.Ptr(1),
		Start: &bedrock.BedrockContentBlockStart{
			ToolUse: &bedrock.BedrockToolUseStart{ToolUseID: "tooluse_1", Name: "get_weather"},
		},
	})
	emit(&bedrock.BedrockStreamEvent{
		ContentBlockIndex: schemas.Ptr(1),
		Delta:             &bedrock.BedrockContentBlockDelta{ToolUse: &bedrock.BedrockToolUseDelta{Input: `{"city":"Paris"}`}},
	})
	emit(&bedrock.BedrockStreamEvent{ContentBlockIndex: schemas.Ptr(1), ContentBlockStop: true})
	emit(&bedrock.BedrockStreamEvent{StopReason: schemas.Ptr("tool_use")})

	usage := &schemas.ResponsesResponseUsage{InputTokens: 7, OutputTokens: 6, TotalTokens: 13}
	all = append(all, bedrock.FinalizeBedrockStream(state, seq, usage, nil)...)

	items := bedrockReasoningItems(all)
	require.Len(t, items, 1, "the reasoning block closes exactly once, on the tool block starting")
	assertBedrockReasoningItemSnapshot(t, items[0], "Picking a tool.", "sig-tool")

	// OpenCode replays these Anthropic blocks after executing the tool. A second
	// redacted block containing sig-tool makes Bedrock reject that continuation.
	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()
	var thinking, signature string
	var blockTypes []anthropic.AnthropicContentBlockType
	for _, frame := range all {
		frame.ExtraFields.Provider = schemas.Bedrock
		for _, event := range anthropic.ToAnthropicResponsesStreamResponse(ctx, frame) {
			if event.ContentBlock != nil {
				blockTypes = append(blockTypes, event.ContentBlock.Type)
			}
			if event.Delta != nil {
				if event.Delta.Thinking != nil {
					thinking += *event.Delta.Thinking
				}
				if event.Delta.Signature != nil {
					signature += *event.Delta.Signature
				}
			}
		}
	}
	assert.Equal(t, []anthropic.AnthropicContentBlockType{
		anthropic.AnthropicContentBlockTypeThinking, anthropic.AnthropicContentBlockTypeToolUse,
	}, blockTypes, "the Anthropic stream must preserve the original Bedrock block sequence")
	assert.Equal(t, "Picking a tool.", thinking)
	assert.Equal(t, "sig-tool", signature)
}

// TestBedrockReasoningItemSummaryFinalizePath covers the close path taken when the
// reasoning block is still open at the end of the stream.
func TestBedrockReasoningItemSummaryFinalizePath(t *testing.T) {
	state := bedrock.NewBedrockResponsesStreamState()
	state.Model = schemas.Ptr("anthropic.claude-3-7-sonnet-20250219-v1:0")

	var all []*schemas.BifrostResponsesStreamResponse
	seq := 0
	emit := func(chunk *bedrock.BedrockStreamEvent) {
		responses, bErr, _ := chunk.ToBifrostResponsesStream(seq, state)
		require.Nil(t, bErr)
		all = append(all, responses...)
		seq += len(responses)
	}

	emit(&bedrock.BedrockStreamEvent{Role: schemas.Ptr("assistant")})
	emit(&bedrock.BedrockStreamEvent{
		ContentBlockIndex: schemas.Ptr(0),
		Delta: &bedrock.BedrockContentBlockDelta{
			ReasoningContent: &bedrock.BedrockReasoningContentText{Text: schemas.Ptr("Thinking it through.")},
		},
	})
	emit(&bedrock.BedrockStreamEvent{
		ContentBlockIndex: schemas.Ptr(0),
		Delta: &bedrock.BedrockContentBlockDelta{
			ReasoningContent: &bedrock.BedrockReasoningContentText{Signature: schemas.Ptr("sig-final")},
		},
	})
	emit(&bedrock.BedrockStreamEvent{ContentBlockIndex: schemas.Ptr(0), ContentBlockStop: true})
	emit(&bedrock.BedrockStreamEvent{StopReason: schemas.Ptr("end_turn")})

	usage := &schemas.ResponsesResponseUsage{InputTokens: 5, OutputTokens: 4, TotalTokens: 9}
	all = append(all, bedrock.FinalizeBedrockStream(state, seq, usage, nil)...)

	items := bedrockReasoningItems(all)
	require.Len(t, items, 1)
	assertBedrockReasoningItemSnapshot(t, items[0], "Thinking it through.", "sig-final")
}

// TestBedrockReasoningItemTextlessBlockKeepsEmptySummary pins that a reasoning block with
// no text still closes with an empty summary and its signature -- the signature-only
// replay shape, which must keep working.
func TestBedrockReasoningItemTextlessBlockKeepsEmptySummary(t *testing.T) {
	state := bedrock.NewBedrockResponsesStreamState()
	state.Model = schemas.Ptr("anthropic.claude-3-7-sonnet-20250219-v1:0")

	var all []*schemas.BifrostResponsesStreamResponse
	seq := 0
	emit := func(chunk *bedrock.BedrockStreamEvent) {
		responses, bErr, _ := chunk.ToBifrostResponsesStream(seq, state)
		require.Nil(t, bErr)
		all = append(all, responses...)
		seq += len(responses)
	}

	emit(&bedrock.BedrockStreamEvent{Role: schemas.Ptr("assistant")})
	emit(&bedrock.BedrockStreamEvent{
		ContentBlockIndex: schemas.Ptr(0),
		Delta: &bedrock.BedrockContentBlockDelta{
			ReasoningContent: &bedrock.BedrockReasoningContentText{Signature: schemas.Ptr("sig-only")},
		},
	})
	emit(&bedrock.BedrockStreamEvent{ContentBlockIndex: schemas.Ptr(0), ContentBlockStop: true})
	emit(&bedrock.BedrockStreamEvent{StopReason: schemas.Ptr("end_turn")})

	usage := &schemas.ResponsesResponseUsage{InputTokens: 5, OutputTokens: 1, TotalTokens: 6}
	all = append(all, bedrock.FinalizeBedrockStream(state, seq, usage, nil)...)

	items := bedrockReasoningItems(all)
	require.Len(t, items, 1)
	require.NotNil(t, items[0].ResponsesReasoning)
	assert.Empty(t, items[0].ResponsesReasoning.Summary,
		"a text-less block keeps the signature-only replay shape")
	require.NotNil(t, items[0].ResponsesReasoning.EncryptedContent)
	assert.Equal(t, "sig-only", *items[0].ResponsesReasoning.EncryptedContent)
}
