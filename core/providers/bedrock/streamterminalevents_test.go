package bedrock

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runBedrockResponsesStream replays a native Converse event sequence through
// ToBifrostResponsesStream with the given stream state, the way the provider's
// stream loop does, and returns every emitted Responses stream event.
func runBedrockResponsesStream(t *testing.T, state *BedrockResponsesStreamState, events []BedrockStreamEvent) []*schemas.BifrostResponsesStreamResponse {
	t.Helper()
	var collected []*schemas.BifrostResponsesStreamResponse
	seq := 0
	for i := range events {
		responses, bifrostErr, _ := events[i].ToBifrostResponsesStream(seq, state)
		require.Nil(t, bifrostErr, "event %d returned an error", i)
		collected = append(collected, responses...)
		seq += len(responses)
	}
	return collected
}

func bedrockMessageStartEvent() BedrockStreamEvent {
	return BedrockStreamEvent{Role: schemas.Ptr("assistant")}
}

func bedrockReasoningTextDeltaEvent(index int, text string) BedrockStreamEvent {
	return BedrockStreamEvent{
		ContentBlockIndex: schemas.Ptr(index),
		Delta: &BedrockContentBlockDelta{
			ReasoningContent: &BedrockReasoningContentText{Text: schemas.Ptr(text)},
		},
	}
}

func bedrockReasoningSignatureDeltaEvent(index int, signature string) BedrockStreamEvent {
	return BedrockStreamEvent{
		ContentBlockIndex: schemas.Ptr(index),
		Delta: &BedrockContentBlockDelta{
			ReasoningContent: &BedrockReasoningContentText{Signature: schemas.Ptr(signature)},
		},
	}
}

func bedrockTextDeltaEvent(index int, text string) BedrockStreamEvent {
	return BedrockStreamEvent{
		ContentBlockIndex: schemas.Ptr(index),
		Delta:             &BedrockContentBlockDelta{Text: schemas.Ptr(text)},
	}
}

func bedrockCitationDeltaEvent(index int, url, domain string) BedrockStreamEvent {
	return BedrockStreamEvent{
		ContentBlockIndex: schemas.Ptr(index),
		Delta: &BedrockContentBlockDelta{
			Citation: &BedrockCitation{
				Location: BedrockCitationLocation{
					Web: &BedrockWebCitationLocation{URL: url, Domain: domain},
				},
			},
		},
	}
}

func bedrockToolUseStartEvent(index int, toolUseID, name string) BedrockStreamEvent {
	return BedrockStreamEvent{
		ContentBlockIndex: schemas.Ptr(index),
		Start: &BedrockContentBlockStart{
			ToolUse: &BedrockToolUseStart{ToolUseID: toolUseID, Name: name},
		},
	}
}

func bedrockToolArgsDeltaEvent(index int, input string) BedrockStreamEvent {
	return BedrockStreamEvent{
		ContentBlockIndex: schemas.Ptr(index),
		Delta:             &BedrockContentBlockDelta{ToolUse: &BedrockToolUseDelta{Input: input}},
	}
}

func bedrockMessageStopEvent(stopReason string) BedrockStreamEvent {
	return BedrockStreamEvent{StopReason: schemas.Ptr(stopReason)}
}

func eventsOfType(events []*schemas.BifrostResponsesStreamResponse, eventType schemas.ResponsesStreamResponseType) []*schemas.BifrostResponsesStreamResponse {
	var matches []*schemas.BifrostResponsesStreamResponse
	for _, event := range events {
		if event != nil && event.Type == eventType {
			matches = append(matches, event)
		}
	}
	return matches
}

func reasoningDoneItems(events []*schemas.BifrostResponsesStreamResponse) []*schemas.BifrostResponsesStreamResponse {
	var matches []*schemas.BifrostResponsesStreamResponse
	for _, event := range eventsOfType(events, schemas.ResponsesStreamResponseTypeOutputItemDone) {
		if event.Item != nil && event.Item.Type != nil && *event.Item.Type == schemas.ResponsesMessageTypeReasoning {
			matches = append(matches, event)
		}
	}
	return matches
}

// TestBedrockResponsesStreamReasoningTerminalEventsCarryPayload verifies that
// closing a reasoning block on a following tool-use start emits
// reasoning_summary_text.done, content_part.done, and output_item.done with
// the accumulated reasoning text and signature instead of empty payloads.
func TestBedrockResponsesStreamReasoningTerminalEventsCarryPayload(t *testing.T) {
	state := NewBedrockResponsesStreamState()
	events := runBedrockResponsesStream(t, state, []BedrockStreamEvent{
		bedrockMessageStartEvent(),
		bedrockReasoningTextDeltaEvent(0, "Let me think"),
		bedrockReasoningTextDeltaEvent(0, " about this."),
		bedrockReasoningSignatureDeltaEvent(0, "sig-abc"),
		bedrockToolUseStartEvent(1, "tool_1", "get_weather"),
	})

	reasoningDone := eventsOfType(events, schemas.ResponsesStreamResponseTypeReasoningSummaryTextDone)
	require.Len(t, reasoningDone, 1)
	require.NotNil(t, reasoningDone[0].Text)
	assert.Equal(t, "Let me think about this.", *reasoningDone[0].Text)
	require.NotNil(t, reasoningDone[0].SummaryIndex)
	assert.Equal(t, 0, *reasoningDone[0].SummaryIndex)

	var reasoningPartDone *schemas.BifrostResponsesStreamResponse
	for _, event := range eventsOfType(events, schemas.ResponsesStreamResponseTypeContentPartDone) {
		if event.Part != nil && event.Part.Type == schemas.ResponsesOutputMessageContentTypeReasoning {
			reasoningPartDone = event
			break
		}
	}
	require.NotNil(t, reasoningPartDone, "no reasoning content_part.done emitted")
	require.NotNil(t, reasoningPartDone.Part.Text)
	assert.Equal(t, "Let me think about this.", *reasoningPartDone.Part.Text)
	require.NotNil(t, reasoningPartDone.Part.Signature)
	assert.Equal(t, "sig-abc", *reasoningPartDone.Part.Signature)

	doneItems := reasoningDoneItems(events)
	require.Len(t, doneItems, 1)
	item := doneItems[0].Item
	require.NotNil(t, item.Content, "reasoning output_item.done must carry the payload as content")
	require.Len(t, item.Content.ContentBlocks, 1)
	block := item.Content.ContentBlocks[0]
	assert.Equal(t, schemas.ResponsesOutputMessageContentTypeReasoning, block.Type)
	require.NotNil(t, block.Text)
	assert.Equal(t, "Let me think about this.", *block.Text)
	require.NotNil(t, block.Signature)
	assert.Equal(t, "sig-abc", *block.Signature)
}

// TestBedrockResponsesStreamReasoningCloseOnTextDelta covers the second close
// site: a text delta arriving on a later content block closes the open
// reasoning block, and the terminal events must carry the accumulated payload.
func TestBedrockResponsesStreamReasoningCloseOnTextDelta(t *testing.T) {
	state := NewBedrockResponsesStreamState()
	events := runBedrockResponsesStream(t, state, []BedrockStreamEvent{
		bedrockMessageStartEvent(),
		bedrockReasoningTextDeltaEvent(0, "step one"),
		bedrockReasoningSignatureDeltaEvent(0, "sig-text-close"),
		bedrockTextDeltaEvent(1, "Answer."),
	})

	reasoningDone := eventsOfType(events, schemas.ResponsesStreamResponseTypeReasoningSummaryTextDone)
	require.Len(t, reasoningDone, 1)
	require.NotNil(t, reasoningDone[0].Text)
	assert.Equal(t, "step one", *reasoningDone[0].Text)

	doneItems := reasoningDoneItems(events)
	require.Len(t, doneItems, 1)
	require.NotNil(t, doneItems[0].Item.Content)
	require.Len(t, doneItems[0].Item.Content.ContentBlocks, 1)
	block := doneItems[0].Item.Content.ContentBlocks[0]
	require.NotNil(t, block.Text)
	assert.Equal(t, "step one", *block.Text)
	require.NotNil(t, block.Signature)
	assert.Equal(t, "sig-text-close", *block.Signature)
}

// TestBedrockResponsesStreamFinalizeClosesReasoningWithPayload covers the
// stream-end close site: a reasoning block still open at end of stream is
// closed by FinalizeBedrockStream with its accumulated text and signature.
func TestBedrockResponsesStreamFinalizeClosesReasoningWithPayload(t *testing.T) {
	state := NewBedrockResponsesStreamState()
	streamEvents := runBedrockResponsesStream(t, state, []BedrockStreamEvent{
		bedrockMessageStartEvent(),
		bedrockReasoningTextDeltaEvent(0, "only reasoning"),
		bedrockReasoningSignatureDeltaEvent(0, "sig-final"),
		bedrockMessageStopEvent("end_turn"),
	})

	usage := &schemas.ResponsesResponseUsage{InputTokens: 5, OutputTokens: 10, TotalTokens: 15}
	finalEvents := FinalizeBedrockStream(state, len(streamEvents), usage, nil)

	reasoningDone := eventsOfType(finalEvents, schemas.ResponsesStreamResponseTypeReasoningSummaryTextDone)
	require.Len(t, reasoningDone, 1)
	require.NotNil(t, reasoningDone[0].Text)
	assert.Equal(t, "only reasoning", *reasoningDone[0].Text)

	doneItems := reasoningDoneItems(finalEvents)
	require.Len(t, doneItems, 1)
	require.NotNil(t, doneItems[0].Item.Content)
	block := doneItems[0].Item.Content.ContentBlocks[0]
	require.NotNil(t, block.Signature)
	assert.Equal(t, "sig-final", *block.Signature)
}

// TestBedrockResponsesStreamCompletedCarriesOutputArray verifies that the
// terminal response.completed snapshot carries the full ordered output array
// (reasoning, message, function_call) with their payloads, like the
// non-streaming response does.
func TestBedrockResponsesStreamCompletedCarriesOutputArray(t *testing.T) {
	state := NewBedrockResponsesStreamState()
	streamEvents := runBedrockResponsesStream(t, state, []BedrockStreamEvent{
		bedrockMessageStartEvent(),
		bedrockReasoningTextDeltaEvent(0, "reasoning text"),
		bedrockReasoningSignatureDeltaEvent(0, "sig-snapshot"),
		bedrockTextDeltaEvent(1, "Hello "),
		bedrockTextDeltaEvent(1, "world."),
		bedrockToolUseStartEvent(2, "tool_call_1", "get_weather"),
		bedrockToolArgsDeltaEvent(2, `{"city":"Paris"}`),
		bedrockMessageStopEvent("tool_use"),
	})

	usage := &schemas.ResponsesResponseUsage{InputTokens: 20, OutputTokens: 30, TotalTokens: 50}
	finalEvents := FinalizeBedrockStream(state, len(streamEvents), usage, nil)
	require.NotEmpty(t, finalEvents)

	terminal := finalEvents[len(finalEvents)-1]
	assert.Equal(t, schemas.ResponsesStreamResponseTypeCompleted, terminal.Type)
	require.NotNil(t, terminal.Response)
	require.NotNil(t, terminal.Response.StopReason)
	assert.Equal(t, "tool_calls", *terminal.Response.StopReason)

	output := terminal.Response.Output
	require.Len(t, output, 3, "response.completed must carry every output item")

	require.NotNil(t, output[0].Type)
	assert.Equal(t, schemas.ResponsesMessageTypeReasoning, *output[0].Type)
	require.NotNil(t, output[0].Content)
	require.Len(t, output[0].Content.ContentBlocks, 1)
	require.NotNil(t, output[0].Content.ContentBlocks[0].Text)
	assert.Equal(t, "reasoning text", *output[0].Content.ContentBlocks[0].Text)
	require.NotNil(t, output[0].Content.ContentBlocks[0].Signature)
	assert.Equal(t, "sig-snapshot", *output[0].Content.ContentBlocks[0].Signature)

	require.NotNil(t, output[1].Type)
	assert.Equal(t, schemas.ResponsesMessageTypeMessage, *output[1].Type)
	require.NotNil(t, output[1].Content)
	require.Len(t, output[1].Content.ContentBlocks, 1)
	require.NotNil(t, output[1].Content.ContentBlocks[0].Text)
	assert.Equal(t, "Hello world.", *output[1].Content.ContentBlocks[0].Text)

	require.NotNil(t, output[2].Type)
	assert.Equal(t, schemas.ResponsesMessageTypeFunctionCall, *output[2].Type)
	require.NotNil(t, output[2].ResponsesToolMessage)
	require.NotNil(t, output[2].ResponsesToolMessage.CallID)
	assert.Equal(t, "tool_call_1", *output[2].ResponsesToolMessage.CallID)
	require.NotNil(t, output[2].ResponsesToolMessage.Arguments)
	assert.Equal(t, `{"city":"Paris"}`, *output[2].ResponsesToolMessage.Arguments)
}

// TestBedrockResponsesStreamReasoningDoneItemReplaysThroughRequestConverter
// walks the full multi-turn chain: the reasoning output_item.done emitted by
// the stream is echoed back as JSON (the way SDK clients replay history) and
// converted into the next Converse request, which must reconstruct the
// reasoningContent block with its text and signature.
func TestBedrockResponsesStreamReasoningDoneItemReplaysThroughRequestConverter(t *testing.T) {
	state := NewBedrockResponsesStreamState()
	events := runBedrockResponsesStream(t, state, []BedrockStreamEvent{
		bedrockMessageStartEvent(),
		bedrockReasoningTextDeltaEvent(0, "replayable reasoning"),
		bedrockReasoningSignatureDeltaEvent(0, "sig-replay"),
		bedrockToolUseStartEvent(1, "tool_replay", "get_weather"),
		bedrockToolArgsDeltaEvent(1, `{"city":"Paris"}`),
		bedrockMessageStopEvent("tool_use"),
	})
	finalEvents := FinalizeBedrockStream(state, len(events), &schemas.ResponsesResponseUsage{}, nil)
	events = append(events, finalEvents...)

	var reasoningItem, toolItem *schemas.ResponsesMessage
	for _, event := range eventsOfType(events, schemas.ResponsesStreamResponseTypeOutputItemDone) {
		if event.Item == nil || event.Item.Type == nil {
			continue
		}
		switch *event.Item.Type {
		case schemas.ResponsesMessageTypeReasoning:
			reasoningItem = event.Item
		case schemas.ResponsesMessageTypeFunctionCall:
			toolItem = event.Item
		}
	}
	require.NotNil(t, reasoningItem)
	require.NotNil(t, toolItem)

	// Echo the items through JSON, the way a client replays conversation history.
	echo := func(message *schemas.ResponsesMessage) schemas.ResponsesMessage {
		raw, err := json.Marshal(message)
		require.NoError(t, err)
		var echoed schemas.ResponsesMessage
		require.NoError(t, json.Unmarshal(raw, &echoed))
		return echoed
	}

	userMessage := schemas.ResponsesMessage{
		Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
		Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("What is the weather?")},
	}
	toolOutput := schemas.ResponsesMessage{
		Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
		ResponsesToolMessage: &schemas.ResponsesToolMessage{
			CallID: schemas.Ptr("tool_replay"),
			Output: &schemas.ResponsesToolMessageOutputStruct{ResponsesToolCallOutputStr: schemas.Ptr("sunny")},
		},
	}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	bedrockReq, err := ToBedrockResponsesRequest(ctx, &schemas.BifrostResponsesRequest{
		Provider: schemas.Bedrock,
		Model:    "anthropic.claude-sonnet-4-5-20250929-v1:0",
		Input:    []schemas.ResponsesMessage{userMessage, echo(reasoningItem), echo(toolItem), toolOutput},
	})
	require.NoError(t, err)
	require.NotNil(t, bedrockReq)

	reasoningBlockIndex, toolUseBlockIndex := -1, -1
	var reasoningText *BedrockReasoningContentText
	for _, message := range bedrockReq.Messages {
		if message.Role != BedrockMessageRoleAssistant {
			continue
		}
		for i, block := range message.Content {
			if block.ReasoningContent != nil && block.ReasoningContent.ReasoningText != nil {
				reasoningBlockIndex = i
				reasoningText = block.ReasoningContent.ReasoningText
			}
			if block.ToolUse != nil {
				toolUseBlockIndex = i
			}
		}
	}
	require.NotNil(t, reasoningText, "replayed reasoning item must reconstruct a Converse reasoningContent block")
	require.NotNil(t, reasoningText.Text)
	assert.Equal(t, "replayable reasoning", *reasoningText.Text)
	require.NotNil(t, reasoningText.Signature)
	assert.Equal(t, "sig-replay", *reasoningText.Signature)
	require.GreaterOrEqual(t, toolUseBlockIndex, 0, "replayed function_call must reconstruct a toolUse block")
	assert.Less(t, reasoningBlockIndex, toolUseBlockIndex,
		"the reasoning block must precede the toolUse block in the assistant turn")
}

// TestBedrockResponsesStreamAnnotationsCarriedIntoTerminalEvents verifies that
// url_citation annotations streamed for a text block are carried on the
// block's content_part.done, output_item.done, and the response.completed
// snapshot instead of being dropped.
func TestBedrockResponsesStreamAnnotationsCarriedIntoTerminalEvents(t *testing.T) {
	state := NewBedrockResponsesStreamState()
	streamEvents := runBedrockResponsesStream(t, state, []BedrockStreamEvent{
		bedrockMessageStartEvent(),
		bedrockTextDeltaEvent(0, "Cited answer."),
		bedrockCitationDeltaEvent(0, "https://example.com/a", "example.com"),
		bedrockCitationDeltaEvent(0, "https://example.org/b", "example.org"),
		bedrockMessageStopEvent("end_turn"),
	})

	usage := &schemas.ResponsesResponseUsage{InputTokens: 5, OutputTokens: 5, TotalTokens: 10}
	finalEvents := FinalizeBedrockStream(state, len(streamEvents), usage, nil)

	partDone := eventsOfType(finalEvents, schemas.ResponsesStreamResponseTypeContentPartDone)
	require.NotEmpty(t, partDone)
	require.NotNil(t, partDone[0].Part)
	require.NotNil(t, partDone[0].Part.ResponsesOutputMessageContentText)
	annotations := partDone[0].Part.ResponsesOutputMessageContentText.Annotations
	require.Len(t, annotations, 2, "content_part.done must carry the streamed annotations")
	require.NotNil(t, annotations[0].URL)
	assert.Equal(t, "https://example.com/a", *annotations[0].URL)

	itemDone := eventsOfType(finalEvents, schemas.ResponsesStreamResponseTypeOutputItemDone)
	require.NotEmpty(t, itemDone)
	require.NotNil(t, itemDone[0].Item)
	require.NotNil(t, itemDone[0].Item.Content)
	require.Len(t, itemDone[0].Item.Content.ContentBlocks, 1)
	itemBlock := itemDone[0].Item.Content.ContentBlocks[0]
	require.NotNil(t, itemBlock.ResponsesOutputMessageContentText)
	assert.Len(t, itemBlock.ResponsesOutputMessageContentText.Annotations, 2)

	terminal := finalEvents[len(finalEvents)-1]
	require.NotNil(t, terminal.Response)
	require.Len(t, terminal.Response.Output, 1)
	snapshotBlock := terminal.Response.Output[0].Content.ContentBlocks[0]
	require.NotNil(t, snapshotBlock.ResponsesOutputMessageContentText)
	assert.Len(t, snapshotBlock.ResponsesOutputMessageContentText.Annotations, 2)
}

// TestBedrockResponsesStreamLateAnnotationsFoldedIntoSnapshot verifies the
// other arrival order: citations that stream after their text block was
// already closed still reach the response.completed snapshot.
func TestBedrockResponsesStreamLateAnnotationsFoldedIntoSnapshot(t *testing.T) {
	state := NewBedrockResponsesStreamState()
	streamEvents := runBedrockResponsesStream(t, state, []BedrockStreamEvent{
		bedrockMessageStartEvent(),
		bedrockTextDeltaEvent(0, "Answer first."),
		bedrockToolUseStartEvent(1, "tool_late", "search"),
		bedrockCitationDeltaEvent(0, "https://late.example.com", "late.example.com"),
		bedrockMessageStopEvent("tool_use"),
	})

	usage := &schemas.ResponsesResponseUsage{InputTokens: 5, OutputTokens: 5, TotalTokens: 10}
	finalEvents := FinalizeBedrockStream(state, len(streamEvents), usage, nil)

	terminal := finalEvents[len(finalEvents)-1]
	require.NotNil(t, terminal.Response)
	require.Len(t, terminal.Response.Output, 2)

	messageItem := terminal.Response.Output[0]
	require.NotNil(t, messageItem.Content)
	require.NotEmpty(t, messageItem.Content.ContentBlocks)
	textBlock := messageItem.Content.ContentBlocks[0]
	require.NotNil(t, textBlock.ResponsesOutputMessageContentText)
	require.Len(t, textBlock.ResponsesOutputMessageContentText.Annotations, 1)
	require.NotNil(t, textBlock.ResponsesOutputMessageContentText.Annotations[0].URL)
	assert.Equal(t, "https://late.example.com", *textBlock.ResponsesOutputMessageContentText.Annotations[0].URL)
}

// TestBedrockResponsesStreamMultipleReasoningBlocksKeepDistinctPayloads
// verifies that two reasoning blocks buffered concurrently keep their own
// text and signature on their respective terminal events.
func TestBedrockResponsesStreamMultipleReasoningBlocksKeepDistinctPayloads(t *testing.T) {
	state := NewBedrockResponsesStreamState()
	events := runBedrockResponsesStream(t, state, []BedrockStreamEvent{
		bedrockMessageStartEvent(),
		bedrockReasoningTextDeltaEvent(0, "first block"),
		bedrockReasoningSignatureDeltaEvent(0, "sig-first"),
		bedrockReasoningTextDeltaEvent(1, "second block"),
		bedrockReasoningSignatureDeltaEvent(1, "sig-second"),
		bedrockToolUseStartEvent(2, "tool_multi", "get_weather"),
	})

	doneItems := reasoningDoneItems(events)
	require.Len(t, doneItems, 2)

	payloads := map[string]string{}
	for _, done := range doneItems {
		require.NotNil(t, done.Item.Content)
		require.Len(t, done.Item.Content.ContentBlocks, 1)
		block := done.Item.Content.ContentBlocks[0]
		require.NotNil(t, block.Text)
		require.NotNil(t, block.Signature)
		payloads[*block.Text] = *block.Signature
	}
	assert.Equal(t, map[string]string{
		"first block":  "sig-first",
		"second block": "sig-second",
	}, payloads)
}

// TestBedrockResponsesStreamStructuredOutputSnapshot verifies the
// structured-output interception path: the stream handler swallows the
// synthetic tool's start event and converts its argument fragments to text
// deltas, so the state must carry a tracked text item for them or the done
// events and the completed snapshot stay empty on that path.
func TestBedrockResponsesStreamStructuredOutputSnapshot(t *testing.T) {
	state := NewBedrockResponsesStreamState()
	startEvents := runBedrockResponsesStream(t, state, []BedrockStreamEvent{
		bedrockMessageStartEvent(),
	})
	seq := len(startEvents)

	// Mirror the handler's interception: tool start opens the tracked item,
	// argument fragments become buffered text deltas.
	state.UsedStructuredOutputTool = true
	opened := state.beginStructuredOutputTextItem(0, seq)
	require.Len(t, opened, 2)
	assert.Equal(t, schemas.ResponsesStreamResponseTypeOutputItemAdded, opened[0].Type)
	assert.Equal(t, schemas.ResponsesStreamResponseTypeContentPartAdded, opened[1].Type)
	seq += len(opened)

	first := state.appendStructuredOutputText(`{"answer":`, 0, seq)
	require.Len(t, first, 1)
	require.NotNil(t, first[0].OutputIndex, "structured-output deltas must carry output_index")
	require.NotNil(t, first[0].ItemID, "structured-output deltas must carry item_id")
	seq += len(first)
	second := state.appendStructuredOutputText(`"yes"}`, 0, seq)
	require.Len(t, second, 1)
	seq += len(second)

	stopEvents := runBedrockResponsesStream(t, state, []BedrockStreamEvent{
		bedrockMessageStopEvent("tool_use"),
	})
	seq += len(stopEvents)

	usage := &schemas.ResponsesResponseUsage{InputTokens: 5, OutputTokens: 5, TotalTokens: 10}
	finalEvents := FinalizeBedrockStream(state, seq, usage, nil)

	textDone := eventsOfType(finalEvents, schemas.ResponsesStreamResponseTypeOutputTextDone)
	require.Len(t, textDone, 1)
	require.NotNil(t, textDone[0].Text)
	assert.Equal(t, `{"answer":"yes"}`, *textDone[0].Text)

	itemDone := eventsOfType(finalEvents, schemas.ResponsesStreamResponseTypeOutputItemDone)
	require.Len(t, itemDone, 1)
	require.NotNil(t, itemDone[0].Item)
	require.NotNil(t, itemDone[0].Item.Content)
	require.Len(t, itemDone[0].Item.Content.ContentBlocks, 1)
	require.NotNil(t, itemDone[0].Item.Content.ContentBlocks[0].Text)
	assert.Equal(t, `{"answer":"yes"}`, *itemDone[0].Item.Content.ContentBlocks[0].Text)

	terminal := finalEvents[len(finalEvents)-1]
	assert.Equal(t, schemas.ResponsesStreamResponseTypeCompleted, terminal.Type)
	require.NotNil(t, terminal.Response)
	require.Len(t, terminal.Response.Output, 1, "structured-output stream must not complete with an empty output snapshot")
	item := terminal.Response.Output[0]
	require.NotNil(t, item.Type)
	assert.Equal(t, schemas.ResponsesMessageTypeMessage, *item.Type)
	require.NotNil(t, item.Content)
	require.Len(t, item.Content.ContentBlocks, 1)
	require.NotNil(t, item.Content.ContentBlocks[0].Text)
	assert.Equal(t, `{"answer":"yes"}`, *item.Content.ContentBlocks[0].Text)
	require.NotNil(t, terminal.Response.StopReason)
	assert.Equal(t, "stop", *terminal.Response.StopReason,
		"the structured-output stop reason downgrade must survive")
}

// TestBedrockResponsesStreamStateRecycleLeaksNothing verifies that a pooled
// stream state carries no reasoning text, signature, annotation, or output
// item from a previous stream after flush.
func TestBedrockResponsesStreamStateRecycleLeaksNothing(t *testing.T) {
	state := NewBedrockResponsesStreamState()
	firstStream := runBedrockResponsesStream(t, state, []BedrockStreamEvent{
		bedrockMessageStartEvent(),
		bedrockReasoningTextDeltaEvent(0, "leaky reasoning"),
		bedrockReasoningSignatureDeltaEvent(0, "sig-leak"),
		bedrockTextDeltaEvent(1, "first stream text"),
		bedrockCitationDeltaEvent(1, "https://leak.example.com", "leak.example.com"),
		bedrockMessageStopEvent("end_turn"),
	})
	FinalizeBedrockStream(state, len(firstStream), &schemas.ResponsesResponseUsage{}, nil)

	state.flush()

	secondStream := runBedrockResponsesStream(t, state, []BedrockStreamEvent{
		bedrockMessageStartEvent(),
		bedrockTextDeltaEvent(0, "second stream text"),
		bedrockMessageStopEvent("end_turn"),
	})
	finalEvents := FinalizeBedrockStream(state, len(secondStream), &schemas.ResponsesResponseUsage{}, nil)

	terminal := finalEvents[len(finalEvents)-1]
	require.NotNil(t, terminal.Response)
	require.Len(t, terminal.Response.Output, 1, "recycled state must not resurrect items from the previous stream")
	require.NotNil(t, terminal.Response.Output[0].Content)
	require.NotEmpty(t, terminal.Response.Output[0].Content.ContentBlocks)
	require.NotNil(t, terminal.Response.Output[0].Content.ContentBlocks[0].Text)
	assert.Equal(t, "second stream text", *terminal.Response.Output[0].Content.ContentBlocks[0].Text)
	textMeta := terminal.Response.Output[0].Content.ContentBlocks[0].ResponsesOutputMessageContentText
	require.NotNil(t, textMeta)
	assert.Empty(t, textMeta.Annotations, "annotations from the previous stream must not leak")
}
