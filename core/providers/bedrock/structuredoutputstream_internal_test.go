package bedrock

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/providers/anthropic"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The bf_so_* tool input deltas used to be forwarded as bare output_text.delta
// events with no item id, output index or content index, and no item/part
// start, so the anthropic egress opened a fresh block index for every delta and
// never wrote content_block_start / content_block_stop (#8158). Rewriting them
// as text deltas on the same content block must give one well-formed block.
func TestStructuredOutputToolDeltasStreamAsSingleTextBlock(t *testing.T) {
	state := NewBedrockResponsesStreamState()
	state.UsedStructuredOutputTool = true

	idx := func(i int) *int { return &i }
	role := "assistant"
	pieces := []string{`{"answer":`, `"Paris",`, `"confident":true}`}

	var events []BedrockStreamEvent
	events = append(events, BedrockStreamEvent{Role: &role})
	for _, p := range pieces {
		raw := BedrockStreamEvent{
			ContentBlockIndex: idx(0),
			Delta:             &BedrockContentBlockDelta{ToolUse: &BedrockToolUseDelta{Input: p}},
		}
		events = append(events, structuredOutputTextDeltaEvent(&raw))
	}
	stop := "end_turn"
	events = append(events, BedrockStreamEvent{StopReason: &stop})

	var stream []*schemas.BifrostResponsesStreamResponse
	seq := 0
	for i := range events {
		out, bErr, _ := events[i].ToBifrostResponsesStream(seq, state)
		require.Nil(t, bErr)
		for _, r := range out {
			if r != nil {
				stream = append(stream, r)
				seq++
			}
		}
	}
	stream = append(stream, FinalizeBedrockStream(state, seq, &schemas.ResponsesResponseUsage{}, nil)...)

	for _, r := range stream {
		if r.Type == schemas.ResponsesStreamResponseTypeOutputTextDelta {
			require.NotNil(t, r.ItemID, "text delta without item id")
			require.NotNil(t, r.OutputIndex, "text delta without output index")
			require.NotNil(t, r.ContentIndex, "text delta without content index")
		}
	}

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()
	starts := map[int]int{}
	stops := map[int]int{}
	deltaIdx := map[int]bool{}
	text := ""
	for _, r := range stream {
		for _, ev := range anthropic.ToAnthropicResponsesStreamResponse(ctx, r) {
			switch ev.Type {
			case anthropic.AnthropicStreamEventTypeContentBlockStart:
				require.NotNil(t, ev.Index)
				starts[*ev.Index]++
			case anthropic.AnthropicStreamEventTypeContentBlockStop:
				require.NotNil(t, ev.Index)
				stops[*ev.Index]++
			case anthropic.AnthropicStreamEventTypeContentBlockDelta:
				require.NotNil(t, ev.Index)
				deltaIdx[*ev.Index] = true
				if ev.Delta != nil && ev.Delta.Text != nil {
					text += *ev.Delta.Text
				}
			}
		}
	}

	require.Len(t, deltaIdx, 1, "all deltas should share one block index, got %v", deltaIdx)
	for i := range deltaIdx {
		assert.Equal(t, 1, starts[i], "content_block_start count for index %d", i)
		assert.Equal(t, 1, stops[i], "content_block_stop count for index %d", i)
	}
	assert.Equal(t, `{"answer":"Paris","confident":true}`, text)
}
